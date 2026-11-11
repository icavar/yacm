// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yacm-app/yacm/internal/applog"
	"github.com/yacm-app/yacm/internal/expect"
	"github.com/yacm-app/yacm/internal/localcmd"
	"github.com/yacm-app/yacm/internal/logmask"
	"github.com/yacm-app/yacm/internal/serialconn"
	"github.com/yacm-app/yacm/internal/sshconn"
	"github.com/yacm-app/yacm/internal/store"
	"github.com/yacm-app/yacm/internal/telnet"
)

// ---- sessions ------------------------------------------------------------------

// TabStatus is emitted as "tab:status".
type TabStatus struct {
	TabID   string `json:"tabId"`
	State   string `json:"state"` // connecting, connected, closed, error
	Message string `json:"message"`
	Summary string `json:"summary,omitempty"`
}

func (a *App) setState(t *tab, state, msg string) {
	t.mu.Lock()
	t.state = state
	t.mu.Unlock()
	if state == "error" || state == "connected" {
		where := t.node.Name
		if ss := t.node.Session; ss != nil {
			where = fmt.Sprintf("%s (%s %s:%d)", t.node.Name, ss.Protocol, ss.Host, ss.Port)
		}
		if state == "error" {
			applog.Warnf("session %s: %s", where, msg)
		} else {
			applog.Infof("session %s: connected", where)
		}
	}
	a.emit("tab:status", TabStatus{TabID: t.id, State: state, Message: msg})
}

// tabLog prints an informational line into the terminal (dim).
func (a *App) tabLog(t *tab, format string, args ...any) {
	a.emit("tab:log", map[string]string{"tabId": t.id, "text": fmt.Sprintf(format, args...)})
}

func (a *App) getTab(id string) (*tab, error) {
	a.tabsMu.Lock()
	defer a.tabsMu.Unlock()
	t, ok := a.tabs[id]
	if !ok {
		return nil, errors.New("no such tab")
	}
	return t, nil
}

// ConnectRequest opens a session in a new tab (TabID empty) or reconnects.
type ConnectRequest struct {
	NodeID        string `json:"nodeId"`
	TabID         string `json:"tabId"`
	Cols          int    `json:"cols"`
	Rows          int    `json:"rows"`
	ApproveBefore bool   `json:"approveBefore"`
	// Quick connect (unsaved): "ssh://user@host:port", "telnet://host", "serial://COM3?baud=9600"
	Quick string `json:"quick"`
	// Ticket is the change/ticket number for {ticket} in the log file name
	// (a reconnect keeps the one given before).
	Ticket string `json:"ticket"`
}

var (
	quickRe  = regexp.MustCompile(`^(?:(ssh|telnet)://)?(?:([^@\s]+)@)?(\[[0-9A-Fa-f:.%]+\]|[A-Za-z0-9._-]+)(?::(\d{1,5}))?$`)
	serialRe = regexp.MustCompile(`^serial://([^?\s]+)(?:\?baud=(\d+))?$`)
)

func parseQuick(s string) (*store.Node, error) {
	s = strings.TrimSpace(s)
	var n *store.Node
	if m := serialRe.FindStringSubmatch(s); m != nil {
		baud := 9600
		if m[2] != "" {
			baud, _ = strconv.Atoi(m[2])
		}
		n = &store.Node{Name: m[1], Session: &store.Session{Protocol: store.ProtoSerial, Serial: &store.Serial{Device: m[1], Baud: baud}}}
	} else if m := quickRe.FindStringSubmatch(s); m != nil {
		proto := store.Protocol(m[1])
		if proto == "" {
			proto = store.ProtoSSH
		}
		n = &store.Node{Name: strings.TrimPrefix(strings.TrimPrefix(s, "ssh://"), "telnet://"),
			Session: &store.Session{Protocol: proto, Host: strings.Trim(m[3], "[]"), User: m[2], Auth: store.AuthAsk, Algorithms: store.AlgoCompatible}}
		if m[4] != "" {
			n.Session.Port, _ = strconv.Atoi(m[4])
		}
	} else {
		return nil, errors.New("use user@host[:port], telnet://host[:port] or serial://COM3?baud=9600")
	}
	n.ID = "quick-" + store.NewID()
	if err := store.Validate(n); err != nil {
		return nil, err
	}
	return n, nil
}

// newerTypeError explains why a session from a newer YACM cannot be opened.
func newerTypeError(n *store.Node) error {
	return fmt.Errorf("%s uses the connection type %q from a newer version of YACM; update YACM to open it", n.Name, n.Session.Protocol)
}

// Connect starts a connection asynchronously and returns the tab ID.
func (a *App) Connect(req ConnectRequest) (string, error) {
	var node *store.Node
	var err error
	if len(req.TabID) > 64 {
		return "", errors.New("invalid tab id")
	}
	existing, _ := a.getTab(req.TabID)
	ticket := cleanTicket(req.Ticket)
	if existing != nil {
		existing.stopWaiting()
		t := existing
		t.mu.Lock()
		busy := t.state == "connected" || t.state == "connecting"
		node = t.node
		if ticket == "" {
			ticket = t.ticket
		}
		t.mu.Unlock()
		if busy {
			return "", errors.New("already connected")
		}
		if !strings.HasPrefix(node.ID, "quick-") {
			if fresh, err := a.store.Get(node.ID); err == nil {
				node = fresh
			}
		}
	} else if req.Quick != "" {
		node, err = parseQuick(req.Quick)
		if err != nil {
			return "", err
		}
	} else {
		node, err = a.store.Get(req.NodeID)
		if err != nil {
			return "", err
		}
		if node.IsGroup {
			return "", errors.New("cannot connect to a group")
		}
		go a.noteRecent(node.ID)
	}
	if node.Session != nil && !store.KnownProtocol(node.Session.Protocol) {
		return "", newerTypeError(node)
	}
	t := &tab{id: req.TabID, node: node, cols: req.Cols, rows: req.Rows, state: "connecting", ticket: ticket}
	if t.id == "" {
		t.id = store.NewID()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	a.tabsMu.Lock()
	if cur, ok := a.tabs[t.id]; (ok || existing != nil) && cur != existing {
		// Another Connect for this tab (a double click on Reconnect, or a
		// reload racing a manual reconnect) got there first.
		a.tabsMu.Unlock()
		cancel()
		return "", errors.New("already connected")
	}
	a.tabs[t.id] = t
	a.tabsMu.Unlock()
	go a.run(ctx, t, req.ApproveBefore)
	return t.id, nil
}

// ConnectCluster opens every session of a cluster and returns tab IDs.
func (a *App) ConnectCluster(name string, cols, rows int) ([]string, error) {
	var ids []string
	for _, nid := range a.store.SessionsInCluster(name) {
		id, err := a.Connect(ConnectRequest{NodeID: nid, Cols: cols, Rows: rows})
		if err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("cluster %q has no sessions", name)
	}
	return ids, nil
}

func (a *App) secret(name string) string {
	v, _ := a.vault.Get(name)
	return v
}

func (a *App) resolveTokens(t *tab, s string) string {
	ss := t.node.Session
	user, passKey := ss.User, store.SecretPassword(t.node.ID)
	t.mu.Lock()
	if t.eff != nil {
		user, passKey = t.eff.User, t.eff.PasswordKey
	}
	t.mu.Unlock()
	r := strings.NewReplacer(
		"<USER>", user,
		"<PASS>", a.secret(passKey),
		"<IP>", ss.Host, "<HOST>", ss.Host,
		"<PORT>", strconv.Itoa(ss.Port),
		"<NAME>", t.node.Name,
	)
	return r.Replace(s)
}

func (a *App) run(ctx context.Context, t *tab, approveBefore bool) {
	defer applog.Recover("session "+t.node.Name, func(v any) {
		a.closeConn(t, false)
		a.setState(t, "error", fmt.Sprintf("internal error: %v (details in yacm.log)", v))
	})
	node := t.node
	ss := node.Session
	a.setState(t, "connecting", "")
	eff, err := a.store.Effective(node)
	if err != nil {
		a.setState(t, "error", err.Error())
		return
	}
	for _, w := range eff.Warnings {
		a.tabLog(t, "%s", w)
	}
	t.mu.Lock()
	t.eff = eff
	t.mu.Unlock()
	if !a.checkTeamTarget(ctx, t, eff) {
		a.setState(t, "closed", "cancelled: the team session points somewhere else now")
		return
	}
	env := localcmd.Env{Name: node.Name, Host: ss.Host, Port: ss.Port, User: eff.User}
	for _, c := range ss.Before {
		if c.Confirm && !approveBefore {
			a.tabLog(t, "skipped local command (not confirmed): %s", c.Command)
			continue
		}
		a.tabLog(t, "$ %s", c.Command)
		r := localcmd.Run(ctx, c.Command, env, 0)
		if out := strings.TrimRight(r.Output, "\r\n"); out != "" {
			a.tabLog(t, "%s", out)
		}
		if r.Error != "" {
			a.tabLog(t, "local command failed: %s", r.Error)
		}
	}
	if ctx.Err() != nil {
		a.setState(t, "closed", "cancelled")
		return
	}
	if !a.vault.Unlocked() && a.vault.Exists() {
		a.tabLog(t, "vault is locked: stored passwords are not available, you will be asked")
	}

	var c conn
	summary := ""
	timeout := time.Duration(ss.ConnectTimeout) * time.Second
	switch ss.Protocol {
	case store.ProtoSSH:
		hops := a.jumpHops(t, node, eff)
		if eff.Credential != "" {
			a.tabLog(t, "login: credential profile %q", eff.Credential)
		}
		hops = append(hops, sshconn.Hop{
			Host: ss.Host, Port: ss.Port, User: eff.User, Auth: eff.Auth, KeyPath: eff.KeyPath, CertPath: eff.CertPath,
			Password:      a.secret(eff.PasswordKey),
			Passphrase:    a.secret(eff.PassphraseKey),
			PasswordKey:   a.savableKey(node, eff.PasswordKey),
			PassphraseKey: a.savableKey(node, eff.PassphraseKey),
			UserKey:       userKey(node, -1),
		})
		if p := proxyText(eff); p != "" {
			a.tabLog(t, "connecting %s", p)
		}
		var sc *sshconn.Conn
		sc, err = sshconn.Dial(ctx, sshconn.Config{
			Hops: hops, Profile: eff.Algorithms, ConnectTimeout: timeout,
			KeepAlive:    time.Duration(ss.KeepAlive) * time.Second,
			AgentForward: ss.AgentForward, Cols: t.cols, Rows: t.rows,
			KnownHosts: a.kh, Prompter: tabPrompter{a, t.id, node.Name},
			SaveSecret: func(name, v string) error { return a.vault.Set(name, v) },
			SaveUser:   a.saveUser,
			Status:     func(m string) { a.tabLog(t, "%s", m) },
			Dial:       a.proxyDial(eff),
		})
		if err == nil {
			c, summary = sc, sc.Summary()
		}
	case store.ProtoTelnet:
		if len(eff.Hops) > 0 {
			// Through SSH jump hosts: a forwarded TCP channel to the device.
			hops := a.jumpHops(t, node, eff)
			addr := net.JoinHostPort(ss.Host, strconv.Itoa(ss.Port))
			var tun *sshconn.Tunnel
			if p := proxyText(eff); p != "" {
				a.tabLog(t, "connecting %s", p)
			}
			tun, err = sshconn.DialTCP(ctx, sshconn.Config{
				Hops: hops, Profile: store.AlgoSecure, ConnectTimeout: timeout,
				KnownHosts: a.kh, Prompter: tabPrompter{a, t.id, node.Name},
				SaveSecret: func(name, v string) error { return a.vault.Set(name, v) },
				SaveUser:   a.saveUser,
				Status:     func(m string) { a.tabLog(t, "%s", m) },
				Dial:       a.proxyDial(eff),
			}, addr)
			if err == nil {
				a.tabLog(t, "telnet to %s through %s (encrypted up to the jump host, clear text after it)", addr, hops[len(hops)-1].Label())
				c, summary = telnet.NewFromConn(tun, t.cols, t.rows), "telnet · through jump host "+hops[len(hops)-1].Host
			}
			break
		}
		if dial := a.proxyDial(eff); dial != nil {
			// Through the outbound proxy to the device.
			addr := net.JoinHostPort(ss.Host, strconv.Itoa(ss.Port))
			a.tabLog(t, "connecting to %s (telnet, unencrypted) %s…", addr, proxyText(eff))
			var nc net.Conn
			if nc, err = dial(ctx, addr, timeout); err == nil {
				c, summary = telnet.NewFromConn(nc, t.cols, t.rows), "telnet · no encryption · through a proxy"
			}
			break
		}
		a.tabLog(t, "connecting to %s:%d (telnet, unencrypted)…", ss.Host, ss.Port)
		var tc *telnet.Conn
		tc, err = telnet.Dial(ctx, ss.Host, ss.Port, timeout, t.cols, t.rows)
		if err == nil {
			c, summary = tc, "telnet · no encryption"
		}
	case store.ProtoSerial:
		s := *ss.Serial
		a.tabLog(t, "opening %s at %d %d%s%s…", s.Device, s.Baud, s.DataBits, strings.ToUpper(s.Parity[:1]), s.StopBits)
		var pc *serialconn.Conn
		pc, err = serialconn.Open(s)
		if err == nil {
			c, summary = pc, fmt.Sprintf("serial %s · %d %d%s%s", s.Device, s.Baud, s.DataBits, strings.ToUpper(s.Parity[:1]), s.StopBits)
		}
	default:
		err = fmt.Errorf("unsupported protocol %q", ss.Protocol)
	}
	if err != nil {
		if ctx.Err() != nil {
			a.setState(t, "closed", "cancelled")
		} else {
			a.setState(t, "error", err.Error())
		}
		a.afterCommands(t)
		return
	}

	t.mu.Lock()
	t.conn = c
	now := time.Now()
	t.watch = promptWatch{lastIn: now, lastOut: now}
	if ss.LogToFile {
		t.logF = a.openLog(t)
		t.stamp, t.midLn = ss.LogTimestamps, false
		t.mask = nil
		if !a.store.Settings().LogSecretsInClear {
			t.mask = &logmask.Masker{}
		}
	}
	t.mu.Unlock()
	a.emit("tab:status", TabStatus{TabID: t.id, State: "connected", Summary: summary})
	t.mu.Lock()
	t.state = "connected"
	t.mu.Unlock()
	applog.Infof("session %s (%s %s:%d): connected", node.Name, ss.Protocol, ss.Host, ss.Port)
	a.rememberTeamTarget(t)

	if ss.InitialReturn {
		t.touchInput()
		_, _ = c.Write([]byte("\r"))
	}
	if rules := a.buildRules(t); len(rules) > 0 {
		eng := expect.New(rules, func(b []byte) error { t.touchInput(); _, err := c.Write(b); return err },
			func(ev expect.Event) { a.emit("tab:expect", map[string]any{"tabId": t.id, "event": ev}) })
		t.mu.Lock()
		t.engine = eng
		t.mu.Unlock()
		eng.Start()
	}
	go a.watchSession(ctx, t)
	if sc, ok := c.(*sshconn.Conn); ok {
		a.startTunnels(t, sc)
	}

	buf := make([]byte, 32*1024)
	for {
		n, rerr := c.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			t.mu.Lock()
			cp := t.capture
			t.mu.Unlock()
			// Output of a command going to a file stays out of the terminal.
			if cp == nil || !cp.feed(chunk) {
				a.emit("tab:data", map[string]string{"tabId": t.id, "data": base64.StdEncoding.EncodeToString(chunk)})
			}
			t.mu.Lock()
			eng, lf := t.engine, t.logF
			t.watch.output(chunk, time.Now())
			var plain []byte
			if lf != nil {
				plain = t.strip.Strip(chunk)
				if t.mask != nil {
					plain = t.mask.Write(plain)
				}
				if t.stamp {
					plain, t.midLn = stampLines(plain, t.midLn, time.Now())
				}
			}
			t.mu.Unlock()
			if eng != nil {
				eng.Feed(chunk)
			}
			if lf != nil {
				lf.Write(plain)
			}
		}
		if rerr != nil {
			break
		}
	}
	a.closeConn(t, true)
	msg := "connection closed"
	if ctx.Err() != nil {
		msg = "disconnected"
	}
	a.setState(t, "closed", msg)
	a.afterCommands(t)
	if ctx.Err() == nil {
		a.afterClose(t)
	}
}

// savableKey returns the vault key if the user may save a prompted secret.
// A quick-connect session may still save into a credential profile.
func (a *App) savableKey(n *store.Node, key string) string {
	if key == "" || (strings.HasPrefix(n.ID, "quick-") && !strings.HasPrefix(key, "cred/")) {
		return ""
	}
	return key
}

// stampLines puts the time at the start of every line of plain log text.
// midLine says whether the previous chunk ended inside a line.
func stampLines(b []byte, midLine bool, now time.Time) ([]byte, bool) {
	if len(b) == 0 {
		return b, midLine
	}
	stamp := []byte(now.Format("[2006-01-02 15:04:05] "))
	out := make([]byte, 0, len(b)+len(stamp)*4)
	for _, c := range b {
		if !midLine && c != '\n' && c != '\r' {
			out = append(out, stamp...)
			midLine = true
		}
		out = append(out, c)
		if c == '\n' {
			midLine = false
		}
	}
	return out, midLine
}

func (a *App) buildRules(t *tab) []expect.Rule {
	ss := t.node.Session
	var allowed []bool
	if a.store.IsLibraryNode(t.node.ID) {
		allowed = teamRulesOK(ss.Expect)
	}
	var out []expect.Rule
	for i, r := range ss.Expect {
		if !r.Active {
			continue
		}
		if allowed != nil && !allowed[i] {
			applog.Warnf("session %s: team library automation rule %q skipped: it sends a login outside a login prompt, or control characters", t.node.Name, r.Expect)
			a.tabLog(t, "An automation rule from the team library was skipped: it would send your user name or password somewhere other than a login prompt, or control characters.")
			continue
		}
		var re *regexp.Regexp
		label := r.Expect
		if allowed != nil && usesLogin(r.Send) {
			// A library's login answer waits for its prompt as plain text.
			re = teamLoginPattern(r.Expect)
		} else if strings.TrimSpace(r.Expect) == "" {
			// No text to wait for: send at once (SecureCRT allows this too).
			if r.Send == "" && !r.Hidden {
				continue
			}
			label = "(send at once)"
		} else {
			re, _ = expect.Compile(r.Expect)
		}
		send := r.Send
		if r.Hidden {
			send = a.secret(store.SecretExpect(t.node.ID, r.ID))
		}
		to := r.Timeout
		if to <= 0 {
			to = ss.ExpectTimeout
		}
		if to <= 0 {
			to = 20
		}
		resolved := a.resolveTokens(t, send)
		if !r.Hidden && usesLogin(r.Send) && strings.TrimSpace(resolved) == "" {
			// No user name or password to send (a login without one, a
			// library login not linked to a profile): an empty answer is
			// never sent. A rule that may fail is skipped; otherwise the
			// automation stops here and you type the login yourself.
			if r.OnFail == "continue" {
				continue
			}
			a.tabLog(t, "automation stops at “%s”: no user name or password to send, type it yourself (credential profile, or Settings > Team libraries for a library login)", label)
			break
		}
		var quiet time.Duration
		if allowed != nil {
			// Library rules: tokens such as <NAME> must not bring control
			// characters in, waits are short, and login answers go only to
			// a prompt that ends the output.
			if hasControl(resolved, false) {
				applog.Warnf("session %s: team library automation rule %q skipped: control characters", t.node.Name, r.Expect)
				continue
			}
			to = min(to, teamRuleTimeout)
			if usesLogin(r.Send) {
				quiet = teamLoginQuiet
			}
		}
		out = append(out, expect.Rule{
			Label: label, Pattern: re, Send: resolved, Return: r.Return,
			Timeout: time.Duration(to) * time.Second, StopOnFail: r.OnFail != "continue", Quiet: quiet,
		})
	}
	return out
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

// jumpHopsQuiet is jumpHops without status lines (used while waiting).
func (a *App) jumpHopsQuiet(node *store.Node, eff *store.Effective) []sshconn.Hop {
	return a.jumpHops(nil, node, eff)
}

// jumpHops turns the jump hosts of a session into SSH hops (with their
// stored secrets) and logs which saved sessions are used.
func (a *App) jumpHops(t *tab, node *store.Node, eff *store.Effective) []sshconn.Hop {
	hops := make([]sshconn.Hop, 0, len(eff.Hops)+1)
	own := node.Session != nil && len(node.Session.Jump) == len(eff.Hops) && node.Session.JumpVia == ""
	for i, j := range eff.Hops {
		if j.Name != "" && t != nil {
			a.tabLog(t, "jump host: %s (%s)", j.Name, j.Host)
		}
		key := ""
		if own && node.Session.Jump[i].Host == j.Host {
			key = userKey(node, i) // the session's own jump host
		}
		hops = append(hops, sshconn.Hop{
			Host: j.Host, Port: j.Port, User: j.User, Auth: j.Auth, KeyPath: j.KeyPath, CertPath: j.CertPath,
			Password: a.secret(j.PasswordKey), Passphrase: a.secret(j.PassphraseKey),
			PasswordKey: a.savableKey(node, j.PasswordKey), PassphraseKey: a.savableKey(node, j.PassphraseKey),
			Profile: j.Algorithms, UserKey: key,
		})
	}
	return hops
}

// userKey names where a username typed at login is saved: the session
// (jump < 0) or one of its own jump hosts. Quick connect and team library
// sessions save nothing.
func userKey(n *store.Node, jump int) string {
	if strings.HasPrefix(n.ID, "quick-") || n.Library != "" {
		return ""
	}
	if jump < 0 {
		return "session:" + n.ID
	}
	return fmt.Sprintf("jump:%d:%s", jump, n.ID)
}

// saveUser stores a username typed at login (sshconn.Config.SaveUser).
func (a *App) saveUser(key, user string) error {
	kind, rest, _ := strings.Cut(key, ":")
	jump := -1
	if kind == "jump" {
		i, id, ok := strings.Cut(rest, ":")
		n, err := strconv.Atoi(i)
		if !ok || err != nil {
			return fmt.Errorf("bad key %q", key)
		}
		jump, rest = n, id
	} else if kind != "session" {
		return fmt.Errorf("bad key %q", key)
	}
	n, err := a.store.Get(rest)
	if err != nil || n.Session == nil {
		return fmt.Errorf("session not found")
	}
	if jump < 0 {
		n.Session.User = user
	} else if jump < len(n.Session.Jump) {
		n.Session.Jump[jump].User = user
	} else {
		return fmt.Errorf("jump host %d not found", jump+1)
	}
	if _, err := a.store.Upsert(n); err != nil {
		return err
	}
	a.emit("tree:changed", nil)
	return nil
}

// cleanTicket keeps a change/ticket number short and on one line.
func cleanTicket(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
	if len([]rune(s)) > 64 {
		s = string([]rune(s)[:64])
	}
	return s
}

// openLog creates the session log file (called with t.mu held).
func (a *App) openLog(t *tab) *os.File {
	n := t.node
	dir := n.Session.LogDir
	if dir == "" {
		dir = a.store.Settings().DefaultLogDir
	}
	if dir == "" {
		base := a.dataDir
		if base == "" {
			base = a.loc.Default
		}
		dir = filepath.Join(base, "logs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.tabLog(t, "session log: %v", err)
		return nil
	}
	tmpl := store.LegacyLogName
	path := []string{}
	if !strings.HasPrefix(n.ID, "quick-") {
		tmpl = a.store.LogNameTemplate(n)
		path = a.store.GroupPath(n)
	}
	v := store.LogNameValues{Name: unsafeName.ReplaceAllString(n.Name, "_"), Host: n.Session.Host, Protocol: string(n.Session.Protocol),
		Path: strings.Join(path, "/"), Ticket: t.ticket, Time: time.Now()}
	if tmpl != store.LegacyLogName {
		v.Name = n.Name
	}
	if len(path) > 0 {
		v.Group = path[len(path)-1]
	}
	if n.Session.Protocol == store.ProtoSerial && n.Session.Serial != nil {
		v.Host = n.Session.Serial.Device
	}
	if t.eff != nil {
		v.User = t.eff.User
	} else {
		v.User = n.Session.User
	}
	f, p, short, err := store.CreateLogFile(dir, store.LogFileName(tmpl, v))
	if err != nil {
		a.tabLog(t, "session log: %v", err)
		applog.Warnf("session %s: log file: %v", n.Name, err)
		return nil
	}
	if short {
		a.tabLog(t, "session log name shortened (path too long for Windows): %s", p)
	} else if tmpl != store.LegacyLogName {
		a.tabLog(t, "logging to %s", p)
	}
	return f
}

func (a *App) closeConn(t *tab, fromReader bool) {
	a.stopTunnels(t)
	t.mu.Lock()
	c, eng, lf, cancel := t.conn, t.engine, t.logF, t.cancel
	t.conn, t.engine, t.logF = nil, nil, nil
	var rest []byte
	if lf != nil && t.mask != nil {
		rest = t.mask.Flush()
		if t.stamp && len(rest) > 0 {
			rest, t.midLn = stampLines(rest, t.midLn, time.Now())
		}
	}
	t.mu.Unlock()
	if !fromReader && cancel != nil {
		cancel()
	}
	if eng != nil {
		eng.Stop()
	}
	if c != nil {
		c.Close()
	}
	if lf != nil {
		if len(rest) > 0 {
			lf.Write(rest)
		}
		lf.Close()
	}
}

// afterCommands runs unconfirmed "after" commands and asks about the rest.
func (a *App) afterCommands(t *tab) {
	t.mu.Lock()
	if t.after {
		t.mu.Unlock()
		return
	}
	t.after = true
	t.mu.Unlock()
	ss := t.node.Session
	var ask []string
	env := localcmd.Env{Name: t.node.Name, Host: ss.Host, Port: ss.Port, User: a.tabUser(t)}
	for _, c := range ss.After {
		if c.Confirm {
			ask = append(ask, c.Command)
			continue
		}
		r := localcmd.Run(context.Background(), c.Command, env, 0)
		a.tabLog(t, "$ %s", c.Command)
		if out := strings.TrimRight(r.Output, "\r\n"); out != "" {
			a.tabLog(t, "%s", out)
		}
	}
	if len(ask) > 0 {
		a.emit("tab:after", map[string]any{"tabId": t.id, "commands": ask})
	}
}

// RunAfterCommands runs the confirmed "after disconnect" commands.
func (a *App) RunAfterCommands(tabID string) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	ss := t.node.Session
	env := localcmd.Env{Name: t.node.Name, Host: ss.Host, Port: ss.Port, User: a.tabUser(t)}
	for _, c := range ss.After {
		if !c.Confirm {
			continue
		}
		a.tabLog(t, "$ %s", c.Command)
		r := localcmd.Run(context.Background(), c.Command, env, 0)
		if out := strings.TrimRight(r.Output, "\r\n"); out != "" {
			a.tabLog(t, "%s", out)
		}
	}
	return nil
}

func (a *App) tabUser(t *tab) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.eff != nil {
		return t.eff.User
	}
	return t.node.Session.User
}

// SendText sends a command from the library to one or more tabs. Tokens
// such as <HOST> and <USER> are resolved per tab; line breaks become Enter.
// fromTeam marks a command from a team library (or a library session's saved
// commands): <USER> and <PASS> then only work as lines of their own.
func (a *App) SendText(tabIDs []string, text string, enter bool, fromTeam bool) error {
	if fromTeam && !teamCommandOK(text) {
		return errors.New("this command comes from a team library and uses <PASS>, or <USER> inside a command; commands from a library never send your password")
	}
	var firstErr error
	for _, id := range tabIDs {
		t, err := a.getTab(id)
		if err != nil {
			continue
		}
		resolved := a.resolveTokens(t, text)
		if fromTeam && hasControl(resolved, true) {
			if firstErr == nil {
				firstErr = errors.New("this command from a team library would send control characters")
			}
			continue
		}
		txt := strings.ReplaceAll(strings.ReplaceAll(resolved, "\r\n", "\n"), "\n", "\r")
		if enter {
			txt += "\r"
		}
		if err := a.Write(id, txt); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Write sends user input to a tab.
func (a *App) Write(tabID, data string) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	return a.writeTab(t, data)
}

// writeTab sends user input, first erasing what anti-idle typed.
func (a *App) writeTab(t *tab, data string) error {
	t.mu.Lock()
	c := t.conn
	t.watch.lastIn = time.Now()
	erase := ""
	if c != nil {
		erase = t.watch.takeIdleErase()
		t.noteTyped(data, t.watch.lastIn)
	}
	t.mu.Unlock()
	if c == nil {
		return errors.New("not connected")
	}
	_, err := c.Write([]byte(erase + data))
	return err
}

// touchInput records that something was sent to the device (not under t.mu).
func (t *tab) touchInput() {
	t.mu.Lock()
	t.watch.lastIn = time.Now()
	t.watch.idleTyped = "" // automation and the initial Enter run before anti-idle
	t.mu.Unlock()
}

// Broadcast sends the same input to several tabs (cluster typing).
func (a *App) Broadcast(tabIDs []string, data string) {
	for _, id := range tabIDs {
		_ = a.Write(id, data)
	}
}

// Resize updates the terminal size.
func (a *App) Resize(tabID string, cols, rows int) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	c := t.conn
	t.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Resize(cols, rows)
}

// SendBreak sends a break (serial: line break, telnet: IAC BRK, ssh: RFC 4335).
func (a *App) SendBreak(tabID string) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	t.mu.Lock()
	c := t.conn
	t.mu.Unlock()
	if c == nil {
		return errors.New("not connected")
	}
	return c.SendBreak()
}

// SendMacro sends a saved macro with tokens resolved.
func (a *App) SendMacro(tabID string, index int) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	ms := t.node.Session.Macros
	if index < 0 || index >= len(ms) {
		return errors.New("no such macro")
	}
	if a.store.IsLibraryNode(t.node.ID) && !teamCommandOK(ms[index].Text) {
		return errors.New("this saved command comes from a team library and uses <PASS>, or <USER> inside a command; commands from a library never send your password")
	}
	resolved := a.resolveTokens(t, ms[index].Text)
	if a.store.IsLibraryNode(t.node.ID) && hasControl(resolved, true) {
		return errors.New("this saved command from a team library would send control characters")
	}
	txt := strings.ReplaceAll(resolved, "\n", "\r")
	if ms[index].Return {
		txt += "\r"
	}
	return a.Write(tabID, txt)
}

// StopExpect cancels pending expect rules in a tab.
func (a *App) StopExpect(tabID string) {
	if t, err := a.getTab(tabID); err == nil {
		t.mu.Lock()
		eng := t.engine
		t.mu.Unlock()
		if eng != nil {
			eng.Stop()
		}
	}
}

// Disconnect closes the connection but keeps the tab (for reconnect).
func (a *App) Disconnect(tabID string) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	t.mu.Lock()
	connecting := t.state == "connecting"
	t.mu.Unlock()
	a.closeConn(t, false)
	if connecting {
		a.setState(t, "closed", "cancelled")
	}
	return nil
}

// CloseTab disconnects and forgets the tab.
func (a *App) CloseTab(tabID string) {
	t, err := a.getTab(tabID)
	if err != nil {
		return
	}
	t.stopWaiting()
	a.closeConn(t, false)
	a.tabsMu.Lock()
	delete(a.tabs, tabID)
	a.tabsMu.Unlock()
}

// TabInfo is returned to the UI on request.
type TabInfo struct {
	TabID  string        `json:"tabId"`
	Node   *store.Node   `json:"node"`
	State  string        `json:"state"`
	Macros []store.Macro `json:"macros"`
}

// GetTabs lists open tabs (e.g. after a UI reload).
func (a *App) GetTabs() []TabInfo {
	a.tabsMu.Lock()
	defer a.tabsMu.Unlock()
	var out []TabInfo
	for _, t := range a.tabs {
		t.mu.Lock()
		out = append(out, TabInfo{TabID: t.id, Node: t.node, State: t.state, Macros: t.node.Session.Macros})
		t.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TabID < out[j].TabID })
	return out
}

// LogNamePreview shows what a log file name template makes of an example
// session, for the settings and editors.
func (a *App) LogNamePreview(tmpl string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = store.DefaultLogNameTemplate
	}
	return store.LogFileName(tmpl, store.LogNameValues{Name: "core-sw1", Host: "10.0.0.1", Protocol: "ssh", User: "admin",
		Group: "Zagreb", Path: "DC/Zagreb", Ticket: "CHG0012345", Time: time.Now()})
}
