// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package sshconn opens interactive SSH shells with an embedded SSH client
// (golang.org/x/crypto/ssh): password, keyboard-interactive, private keys,
// OpenSSH user certificates, ssh-agent, ProxyJump chains, known_hosts with
// @cert-authority, keepalives and selectable algorithm profiles.
package sshconn

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/yacm-app/yacm/internal/ppk"
	"github.com/yacm-app/yacm/internal/store"
)

// SecretRequest asks the user for a password or passphrase.
type SecretRequest struct {
	Kind    string `json:"kind"` // password, passphrase, username
	Title   string `json:"title"`
	Message string `json:"message"`
	Retry   bool   `json:"retry"`
	CanSave bool   `json:"canSave"`
}

// Prompter is implemented by the UI layer.
type Prompter interface {
	HostKey(ctx context.Context, info HostKeyInfo) (bool, error)
	Secret(ctx context.Context, req SecretRequest) (value string, save bool, err error)
	Questions(ctx context.Context, title, instruction string, questions []string, echos []bool) ([]string, error)
}

// Hop is one SSH server in the chain (jump hosts first, target last).
type Hop struct {
	Host          string
	Port          int
	User          string
	Auth          store.AuthMethod
	KeyPath       string
	CertPath      string
	Password      string // stored secret, may be empty
	Passphrase    string
	PasswordKey   string // vault name used when the user ticks "save"
	PassphraseKey string
	Profile       store.AlgoProfile // overrides Config.Profile for this hop
	typed         *string           // receives the password that was used (Reconnect)
	// UserKey says where a username typed for this hop can be saved
	// (Config.SaveUser); empty: it cannot.
	UserKey string
}

func (cfg *Config) profileFor(h Hop) store.AlgoProfile {
	if h.Profile != "" {
		return h.Profile
	}
	return cfg.Profile
}

func (h Hop) addr() string { return net.JoinHostPort(h.Host, strconv.Itoa(h.Port)) }

// Label is user@host:port.
func (h Hop) Label() string {
	if h.User == "" {
		return h.addr()
	}
	return h.User + "@" + h.addr()
}

// askUser asks for the username of a hop that has none: sending an empty
// one makes devices answer with odd errors ("Non-assigned port!").
func (cfg *Config) askUser(ctx context.Context, h Hop) (string, error) {
	missing := fmt.Errorf("%s: no username set (Edit > Connection, or its group or credential profile)", h.addr())
	if cfg.Prompter == nil {
		return "", missing
	}
	v, save, err := cfg.Prompter.Secret(ctx, SecretRequest{
		Kind: "username", Title: "Username", Message: "Username for " + h.addr(),
		CanSave: h.UserKey != "" && cfg.SaveUser != nil,
	})
	v = strings.TrimSpace(v)
	if err != nil || v == "" {
		return "", missing
	}
	if save && h.UserKey != "" && cfg.SaveUser != nil {
		if err := cfg.SaveUser(h.UserKey, v); err != nil {
			cfg.status("could not save the username: %v", err)
		}
	}
	return v, nil
}

// Config describes a connection.
type Config struct {
	Hops           []Hop
	Profile        store.AlgoProfile
	ConnectTimeout time.Duration
	KeepAlive      time.Duration
	AgentForward   bool
	Cols, Rows     int
	Term           string
	KnownHosts     *KnownHosts
	Prompter       Prompter
	SaveSecret     func(name, value string) error
	SaveUser       func(key, user string) error // a username typed for a hop (Hop.UserKey)
	Status         func(msg string)
	// Dial reaches the first hop (an outbound proxy); nil dials directly.
	Dial func(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error)
}

// Conn is an interactive shell.
type Conn struct {
	clients []*ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	out     *io.PipeReader
	summary string
	closeMu sync.Mutex
	closed  bool
	done    chan struct{}
	agentC  io.Closer
	// For Client.Reconnect: the settings, the agent and the device hop with
	// the password that worked.
	cfg    Config
	ag     agent.ExtendedAgent
	target Hop
}

// algorithms returns kex, ciphers, MACs, host key algorithms for a profile.
func algorithms(p store.AlgoProfile) (kex, ciphers, macs, hostKeys []string) {
	sup := ssh.SupportedAlgorithms()
	ins := ssh.InsecureAlgorithms()
	kex, ciphers, macs, hostKeys = sup.KeyExchanges, sup.Ciphers, sup.MACs, sup.HostKeys
	switch p {
	case store.AlgoCompatible:
		kex = append(kex, ssh.InsecureKeyExchangeDH14SHA1, ssh.InsecureKeyExchangeDHGEXSHA1)
		hostKeys = append(hostKeys, ssh.KeyAlgoRSA, ssh.CertAlgoRSAv01)
	case store.AlgoLegacy:
		kex = append(kex, ins.KeyExchanges...)
		ciphers = append(ciphers, ins.Ciphers...)
		macs = append(macs, ins.MACs...)
		hostKeys = append(hostKeys, ins.HostKeys...)
	}
	return
}

// keyTypeOf maps a host key algorithm to the key type stored in known_hosts.
func keyTypeOf(algo string) string {
	switch algo {
	case ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512:
		return ssh.KeyAlgoRSA
	case ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSASHA512v01:
		return ssh.CertAlgoRSAv01
	}
	return algo
}

// preferKnown moves algorithms whose key type is already on record to the
// front, like OpenSSH does, to avoid false "host key changed" warnings.
func preferKnown(algos, known []string) []string {
	if len(known) == 0 {
		return algos
	}
	var first, rest []string
	for _, a := range algos {
		if slices.Contains(known, keyTypeOf(a)) {
			first = append(first, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(first, rest...)
}

type pendingSave struct{ name, value string }

func (c *Config) status(format string, a ...any) {
	if c.Status != nil {
		c.Status(fmt.Sprintf(format, a...))
	}
}

func readKeyFile(path string) ([]byte, error) {
	p := expandHome(path)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	return data, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// parseKey reads an OpenSSH/PEM private key or a PuTTY .ppk file. A nil
// passphrase means "try without"; errors match the x/crypto/ssh ones, so the
// passphrase prompt works the same for both formats.
func parseKey(data, pass []byte) (ssh.Signer, error) {
	if ppk.IsPPK(data) {
		k, err := ppk.Parse(data, pass)
		switch {
		case errors.Is(err, ppk.ErrEncrypted) && pass != nil:
			return nil, x509.IncorrectPasswordError // an empty passphrase was entered
		case errors.Is(err, ppk.ErrEncrypted):
			return nil, &ssh.PassphraseMissingError{}
		case errors.Is(err, ppk.ErrBadPassphrase):
			return nil, x509.IncorrectPasswordError
		case err != nil:
			return nil, err
		}
		return ssh.NewSignerFromKey(k)
	}
	if pass == nil {
		return ssh.ParsePrivateKey(data)
	}
	return ssh.ParsePrivateKeyWithPassphrase(data, pass)
}

// keySigners loads a private key (asking for its passphrase if needed) and
// returns the certificate signer first, if a certificate is present.
func keySigners(ctx context.Context, cfg *Config, h Hop, saves *[]pendingSave) ([]ssh.Signer, error) {
	data, err := readKeyFile(h.KeyPath)
	if err != nil {
		return nil, err
	}
	signer, err := parseKey(data, nil)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		pass := h.Passphrase
		retry := false
		for attempt := 0; attempt < 4; attempt++ {
			if pass == "" || retry {
				if cfg.Prompter == nil {
					return nil, errors.New("key is encrypted and no passphrase is stored")
				}
				v, save, perr := cfg.Prompter.Secret(ctx, SecretRequest{
					Kind: "passphrase", Title: "Key passphrase",
					Message: fmt.Sprintf("Passphrase for %s (%s)", filepath.Base(h.KeyPath), h.Label()),
					Retry:   retry, CanSave: h.PassphraseKey != "",
				})
				if perr != nil {
					return nil, perr
				}
				pass = v
				if save {
					*saves = append(*saves, pendingSave{h.PassphraseKey, v})
				}
			}
			signer, err = parseKey(data, []byte(pass))
			if err == nil {
				break
			}
			if !errors.Is(err, x509.IncorrectPasswordError) && !strings.Contains(err.Error(), "incorrect passphrase") {
				return nil, err
			}
			retry = true
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load key %s: %w", h.KeyPath, err)
	}
	certPath := h.CertPath
	if certPath == "" {
		certPath = expandHome(h.KeyPath) + "-cert.pub"
	}
	var out []ssh.Signer
	if cdata, cerr := os.ReadFile(expandHome(certPath)); cerr == nil {
		pub, _, _, _, perr := ssh.ParseAuthorizedKey(cdata)
		if perr != nil {
			return nil, fmt.Errorf("parse certificate %s: %w", certPath, perr)
		}
		cert, ok := pub.(*ssh.Certificate)
		if !ok {
			return nil, fmt.Errorf("%s is not an OpenSSH certificate", certPath)
		}
		now := uint64(time.Now().Unix())
		if cert.ValidBefore != ssh.CertTimeInfinity && now >= cert.ValidBefore {
			cfg.status("certificate %s has expired", filepath.Base(certPath))
		} else {
			cs, cerr := ssh.NewCertSigner(cert, signer)
			if cerr != nil {
				return nil, fmt.Errorf("certificate does not match key: %w", cerr)
			}
			out = append(out, cs)
			cfg.status("offering certificate %s (key id %q)", filepath.Base(certPath), cert.KeyId)
		}
	} else if h.CertPath != "" {
		return nil, fmt.Errorf("read certificate: %w", cerr)
	}
	return append(out, signer), nil
}

func (h Hop) remember(pw string) {
	if h.typed != nil {
		*h.typed = pw
	}
}

func authMethods(ctx context.Context, cfg *Config, h Hop, ag agent.ExtendedAgent, saves *[]pendingSave) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	switch h.Auth {
	case store.AuthPublicKey:
		signers, err := keySigners(ctx, cfg, h, saves)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signers...))
	case store.AuthAgent:
		if ag == nil {
			return nil, errors.New("no SSH agent available (start ssh-agent / OpenSSH Authentication Agent service)")
		}
		methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
	}
	if h.Auth == store.AuthPassword || h.Auth == store.AuthAsk || h.Auth == "" {
		attempt := 0
		pw := func() (string, error) {
			defer func() { attempt++ }()
			if attempt == 0 && h.Password != "" && h.Auth != store.AuthAsk {
				h.remember(h.Password)
				return h.Password, nil
			}
			if cfg.Prompter == nil {
				return "", errors.New("password required")
			}
			v, save, err := cfg.Prompter.Secret(ctx, SecretRequest{
				Kind: "password", Title: "Password",
				Message: "Password for " + h.Label(),
				Retry:   attempt > 0, CanSave: h.PasswordKey != "",
			})
			if err != nil {
				return "", err
			}
			if save {
				*saves = append(*saves, pendingSave{h.PasswordKey, v})
			}
			h.remember(v)
			return v, nil
		}
		methods = append(methods, ssh.RetryableAuthMethod(ssh.PasswordCallback(pw), 3))
		// Many network devices only offer keyboard-interactive.
		kiAttempt := 0
		ki := func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			if len(questions) == 0 {
				return nil, nil
			}
			if len(questions) == 1 && !echos[0] && strings.Contains(strings.ToLower(questions[0]), "password") {
				kiAttempt++
				if kiAttempt == 1 && h.Password != "" && h.Auth != store.AuthAsk {
					h.remember(h.Password)
					return []string{h.Password}, nil
				}
				v, err := pw()
				return []string{v}, err
			}
			if cfg.Prompter == nil {
				return nil, errors.New("server asked questions but no prompter")
			}
			return cfg.Prompter.Questions(ctx, name, instruction, questions, echos)
		}
		methods = append(methods, ssh.RetryableAuthMethod(ssh.KeyboardInteractive(ki), 3))
	}
	return methods, nil
}

func (cfg *Config) clientConfig(ctx context.Context, h Hop, ag agent.ExtendedAgent, saves *[]pendingSave) (*ssh.ClientConfig, error) {
	methods, err := authMethods(ctx, cfg, h, ag, saves)
	if err != nil {
		return nil, err
	}
	kex, ciphers, macs, hostKeys := algorithms(cfg.profileFor(h))
	if cfg.KnownHosts != nil {
		hostKeys = preferKnown(hostKeys, cfg.KnownHosts.KnownKeyTypes(h.addr()))
	}
	var cb ssh.HostKeyCallback
	if cfg.KnownHosts != nil {
		cb = cfg.KnownHosts.Callback(func(info HostKeyInfo) (bool, error) {
			if cfg.Prompter == nil {
				return false, nil
			}
			return cfg.Prompter.HostKey(ctx, info)
		})
	} else {
		return nil, errors.New("host key verification is not configured")
	}
	return &ssh.ClientConfig{
		User:              h.User,
		Auth:              methods,
		HostKeyCallback:   cb,
		HostKeyAlgorithms: hostKeys,
		Timeout:           cfg.ConnectTimeout,
		ClientVersion:     "SSH-2.0-YACM",
		Config: ssh.Config{
			KeyExchanges: kex,
			Ciphers:      ciphers,
			MACs:         macs,
		},
	}, nil
}

// explainHandshake adds a hint when algorithm negotiation failed.
func explainHandshake(err error, profile store.AlgoProfile) error {
	msg := err.Error()
	if strings.Contains(msg, "no common algorithm") {
		hint := "the server only supports older algorithms; set Algorithms to \"Compatible\" or \"Legacy\" for this session"
		if profile == store.AlgoLegacy {
			hint = "even the legacy algorithm set does not match this server"
		}
		return fmt.Errorf("%w (%s)", err, hint)
	}
	if strings.Contains(msg, "unable to authenticate") {
		return fmt.Errorf("authentication failed: %w", err)
	}
	return err
}

// Dial connects through all hops and starts an interactive shell.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if len(cfg.Hops) == 0 {
		return nil, errors.New("no host")
	}
	cfg.defaults()
	c, ag, err := connectHops(ctx, &cfg, len(cfg.Hops)-1)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Conn, error) {
		c.Close()
		return nil, err
	}

	target := c.clients[len(c.clients)-1]
	sess, err := target.NewSession()
	if err != nil {
		return fail(err)
	}
	c.session = sess
	if cfg.AgentForward && ag != nil {
		if err := agent.ForwardToAgent(target, ag); err == nil {
			_ = agent.RequestAgentForwarding(sess)
		}
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := sess.RequestPty(cfg.Term, cfg.Rows, cfg.Cols, modes); err != nil {
		return fail(fmt.Errorf("request pty: %w", err))
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return fail(err)
	}
	c.stdin = stdin
	pr, pw := io.Pipe()
	sess.Stdout = pw
	sess.Stderr = pw
	c.out = pr
	if err := sess.Shell(); err != nil {
		return fail(fmt.Errorf("start shell: %w", err))
	}
	go func() {
		err := sess.Wait()
		if err == nil {
			err = io.EOF
		}
		pw.CloseWithError(err)
		close(c.done)
	}()
	if cfg.KeepAlive > 0 {
		go c.keepAlive(cfg.KeepAlive)
	}
	return c, nil
}

func (cfg *Config) defaults() {
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 15 * time.Second
	}
	if cfg.Term == "" {
		cfg.Term = "xterm-256color"
	}
	if cfg.Cols <= 0 || cfg.Rows <= 0 {
		cfg.Cols, cfg.Rows = 120, 32
	}
}

// connectHops opens SSH connections through cfg.Hops; the hop at index
// target is called the target in status messages, the others jump hosts.
// Secrets the user chose to save are saved once every hop is connected.
func connectHops(ctx context.Context, cfg *Config, target int) (*Conn, agent.ExtendedAgent, error) {
	var ag agent.ExtendedAgent
	var agCloser io.Closer
	needAgent := cfg.AgentForward
	for _, h := range cfg.Hops {
		if h.Auth == store.AuthAgent {
			needAgent = true
		}
	}
	if needAgent {
		a, c, err := openAgent()
		if err == nil {
			ag, agCloser = a, c
		} else {
			cfg.status("ssh-agent not available: %v", err)
		}
	}

	c := &Conn{done: make(chan struct{}), agentC: agCloser}
	var saves []pendingSave
	fail := func(err error) (*Conn, agent.ExtendedAgent, error) {
		c.Close()
		return nil, nil, err
	}

	var typed string
	for i, h := range cfg.Hops {
		if h.User == "" {
			u, err := cfg.askUser(ctx, h)
			if err != nil {
				return fail(err)
			}
			h.User, cfg.Hops[i].User = u, u
		}
		role := "target"
		if i != target {
			role = fmt.Sprintf("jump %d", i+1)
		} else {
			h.typed = &typed
		}
		cfg.status("connecting to %s (%s)…", h.Label(), role)
		cc, err := cfg.clientConfig(ctx, h, ag, &saves)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", h.Label(), err))
		}
		var raw net.Conn
		if i == 0 {
			if cfg.Dial != nil {
				raw, err = cfg.Dial(ctx, h.addr(), cfg.ConnectTimeout)
			} else {
				d := net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30 * time.Second}
				raw, err = d.DialContext(ctx, "tcp", h.addr())
			}
		} else {
			dctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
			raw, err = c.clients[i-1].DialContext(dctx, "tcp", h.addr())
			cancel()
		}
		if err != nil {
			return fail(fmt.Errorf("%s: %w", h.Label(), err))
		}
		// Bound the handshake itself; prompts may legitimately take longer,
		// so only the TCP-level stall is limited via the context.
		stop := context.AfterFunc(ctx, func() { raw.Close() })
		sc, chans, reqs, err := ssh.NewClientConn(raw, h.addr(), cc)
		stop()
		if err != nil {
			raw.Close()
			return fail(fmt.Errorf("%s: %w", h.Label(), explainHandshake(err, cfg.profileFor(h))))
		}
		client := ssh.NewClient(sc, chans, reqs)
		c.clients = append(c.clients, client)
		if am, ok := sc.(ssh.AlgorithmsConnMetadata); ok {
			a := am.Algorithms()
			c.summary = fmt.Sprintf("%s · %s · %s · %s", a.KeyExchange, a.HostKey, a.Write.Cipher, string(sc.ServerVersion()))
			cfg.status("connected to %s with %s key exchange, %s host key, %s cipher (%s)", h.Label(), a.KeyExchange, a.HostKey, a.Write.Cipher, string(sc.ServerVersion()))
		}
	}

	c.cfg, c.ag = *cfg, ag
	if target >= 0 && target < len(cfg.Hops) {
		c.target = cfg.Hops[target]
	}
	if typed != "" {
		c.target.Password = typed
		if c.target.Auth == store.AuthAsk {
			c.target.Auth = store.AuthPassword // typed once for this connection
		}
	}
	if cfg.SaveSecret != nil {
		for _, s := range saves {
			if s.name != "" {
				if err := cfg.SaveSecret(s.name, s.value); err != nil {
					cfg.status("could not save secret: %v", err)
				}
			}
		}
	}
	return c, ag, nil
}

// Tunnel is a TCP connection through SSH jump hosts (a direct-tcpip
// channel, like ssh -J); closing it closes the jump hosts too.
type Tunnel struct {
	net.Conn
	hops *Conn
}

// Close closes the channel and the SSH connections behind it.
func (t *Tunnel) Close() error {
	err := t.Conn.Close()
	t.hops.Close()
	return err
}

// Client is an SSH connection to a device through its jump hosts, without a
// shell: for file transfers and tunnels.
type Client struct{ c *Conn }

// DialClient connects through cfg.Hops; the last hop is the device.
func DialClient(ctx context.Context, cfg Config) (*Client, error) {
	if len(cfg.Hops) == 0 {
		return nil, errors.New("no host")
	}
	cfg.defaults()
	c, _, err := connectHops(ctx, &cfg, len(cfg.Hops)-1)
	if err != nil {
		return nil, err
	}
	if cfg.KeepAlive > 0 {
		go c.keepAlive(cfg.KeepAlive)
	}
	return &Client{c: c}, nil
}

// SSH returns the connection to the device.
func (cl *Client) SSH() *ssh.Client {
	cl.c.closeMu.Lock()
	defer cl.c.closeMu.Unlock()
	return cl.c.clients[len(cl.c.clients)-1]
}

// Reconnect opens a new connection to the device over the same jump hosts,
// with the password that worked the first time, and replaces the old one.
// Cisco IOS closes the connection when its one channel closes, so every
// file command after the first needs this.
func (cl *Client) Reconnect(ctx context.Context) error {
	c := cl.c
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return errors.New("connection closed")
	}
	n := len(c.clients)
	var jump *ssh.Client
	if n > 1 {
		jump = c.clients[n-2]
	}
	c.closeMu.Unlock()
	cfg, h := c.cfg, c.target
	var saves []pendingSave
	cc, err := cfg.clientConfig(ctx, h, c.ag, &saves)
	if err != nil {
		return err
	}
	var raw net.Conn
	dctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	switch {
	case jump != nil:
		raw, err = jump.DialContext(dctx, "tcp", h.addr())
	case cfg.Dial != nil:
		raw, err = cfg.Dial(dctx, h.addr(), cfg.ConnectTimeout)
	default:
		d := net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30 * time.Second}
		raw, err = d.DialContext(dctx, "tcp", h.addr())
	}
	if err != nil {
		return fmt.Errorf("%s: %w", h.Label(), err)
	}
	stop := context.AfterFunc(dctx, func() { raw.Close() })
	sc, chans, reqs, err := ssh.NewClientConn(raw, h.addr(), cc)
	stop()
	if err != nil {
		raw.Close()
		return fmt.Errorf("%s: %w", h.Label(), explainHandshake(err, cfg.profileFor(h)))
	}
	nc := ssh.NewClient(sc, chans, reqs)
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		nc.Close()
		return errors.New("connection closed")
	}
	old := c.clients[n-1]
	c.clients[n-1] = nc
	c.closeMu.Unlock()
	old.Close()
	return nil
}

// Summary describes the negotiated algorithms of the device.
func (cl *Client) Summary() string { return cl.c.summary }

// Close closes the device and jump host connections.
func (cl *Client) Close() error {
	cl.c.closeMu.Lock()
	if !cl.c.closed {
		select {
		case <-cl.c.done:
		default:
			close(cl.c.done) // stops the keepalive
		}
	}
	cl.c.closeMu.Unlock()
	return cl.c.Close()
}

// JumpClient returns the connection to the last jump host (nil without
// jump hosts): a file server tunnel listens there for the device.
func (c *Conn) JumpClient() *ssh.Client {
	if len(c.clients) < 2 {
		return nil
	}
	return c.clients[len(c.clients)-2]
}

// SSHClient returns the connection to the device of an interactive shell,
// so file transfers and tunnels can use it without logging in again.
func (c *Conn) SSHClient() *ssh.Client {
	if len(c.clients) == 0 {
		return nil
	}
	return c.clients[len(c.clients)-1]
}

// Jumps is a chain of connected SSH jump hosts that can open TCP
// connections from the last one (used to wait for a device behind them).
type Jumps struct {
	c    *Conn
	last string
}

// ConnectJumps connects through cfg.Hops, all of them jump hosts.
func ConnectJumps(ctx context.Context, cfg Config) (*Jumps, error) {
	if len(cfg.Hops) == 0 {
		return nil, errors.New("no jump host")
	}
	cfg.defaults()
	c, _, err := connectHops(ctx, &cfg, -1)
	if err != nil {
		return nil, err
	}
	return &Jumps{c: c, last: cfg.Hops[len(cfg.Hops)-1].Label()}, nil
}

// DialContext opens a TCP connection from the last jump host to addr.
func (j *Jumps) DialContext(ctx context.Context, addr string) (net.Conn, error) {
	nc, err := j.c.clients[len(j.c.clients)-1].DialContext(ctx, "tcp", addr)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "administratively prohibited") || strings.Contains(msg, "open failed") {
			return nil, fmt.Errorf("the jump host %s does not forward connections to %s (%v); it may not allow TCP forwarding (AllowTcpForwarding)", j.last, addr, err)
		}
		return nil, fmt.Errorf("%s via %s: %w", addr, j.last, err)
	}
	return nc, nil
}

// Close closes the jump host connections.
func (j *Jumps) Close() error { return j.c.Close() }

// DialTCP connects through cfg.Hops (all of them jump hosts) and opens a
// TCP connection from the last one to addr (host:port), for telnet
// sessions behind a jump host.
func DialTCP(ctx context.Context, cfg Config, addr string) (*Tunnel, error) {
	j, err := ConnectJumps(ctx, cfg)
	if err != nil {
		return nil, err
	}
	cfg.status("opening a connection from %s to %s…", j.last, addr)
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 15 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	nc, err := j.DialContext(dctx, addr)
	if err != nil {
		j.Close()
		return nil, err
	}
	return &Tunnel{Conn: nc, hops: j.c}, nil
}

func (c *Conn) keepAlive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.closeMu.Lock()
			target := c.clients[len(c.clients)-1]
			c.closeMu.Unlock()
			errc := make(chan error, 1)
			go func() {
				_, _, err := target.SendRequest("keepalive@openssh.com", true, nil)
				errc <- err
			}()
			select {
			case err := <-errc:
				if err != nil {
					misses++
				} else {
					misses = 0
				}
			case <-time.After(every):
				misses++
			case <-c.done:
				return
			}
			if misses >= 3 {
				c.Close()
				return
			}
		}
	}
}

// Read returns shell output.
func (c *Conn) Read(p []byte) (int, error) { return c.out.Read(p) }

// Write sends keystrokes.
func (c *Conn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

// Resize informs the server about the terminal size.
func (c *Conn) Resize(cols, rows int) error {
	if c.session == nil {
		return nil
	}
	return c.session.WindowChange(rows, cols)
}

// Summary describes negotiated algorithms of the target hop.
func (c *Conn) Summary() string { return c.summary }

// SendBreak sends an RFC 4335 break (useful for some console servers).
func (c *Conn) SendBreak() error {
	if c.session == nil {
		return nil
	}
	_, err := c.session.SendRequest("break", true, ssh.Marshal(struct{ Ms uint32 }{500}))
	return err
}

// Close tears down the session and all hops (target first).
func (c *Conn) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.session != nil {
		c.session.Close()
	}
	for i := len(c.clients) - 1; i >= 0; i-- {
		c.clients[i].Close()
	}
	if c.agentC != nil {
		c.agentC.Close()
	}
	if c.out != nil {
		c.out.Close()
	}
	return nil
}
