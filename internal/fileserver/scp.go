// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package fileserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/icavar/yacm/internal/xfer"
)

var osStat = os.Stat

// SCP is an SSH server that only copies files (scp -t / -f) in one folder,
// with one generated login. It offers old algorithms too: it runs only while
// switched on, and older IOS releases know nothing newer.
type SCP struct {
	cfg    Config
	ln     net.Listener
	signer []ssh.Signer
}

func randomPassword() string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 14)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

// hostKeys loads or creates the server's Ed25519 and RSA host keys.
func hostKeys(file string) ([]ssh.Signer, error) {
	var out []ssh.Signer
	for _, kind := range []string{"ed25519", "rsa"} {
		path := file + "_" + kind
		if file != "" {
			if b, err := os.ReadFile(path); err == nil {
				if s, err := ssh.ParsePrivateKey(b); err == nil {
					out = append(out, s)
					continue
				}
			}
		}
		var priv any
		if kind == "ed25519" {
			_, k, _ := ed25519.GenerateKey(rand.Reader)
			priv = k
		} else {
			k, err := rsa.GenerateKey(rand.Reader, 3072)
			if err != nil {
				return nil, err
			}
			priv = k
		}
		s, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			return nil, err
		}
		if file != "" {
			if block, err := ssh.MarshalPrivateKey(priv, "yacm file server"); err == nil {
				_ = os.MkdirAll(filepath.Dir(path), 0o700)
				_ = os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

func startSCP(cfg Config, keyFile string) (*SCP, error) {
	keys, err := hostKeys(keyFile)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, listenError(err, cfg.Port)
	}
	s := &SCP{cfg: cfg, ln: ln, signer: keys}
	go s.serve()
	return s, nil
}

// Port is the TCP port.
func (s *SCP) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Fingerprint is the SHA256 fingerprint of the Ed25519 host key.
func (s *SCP) Fingerprint() string { return ssh.FingerprintSHA256(s.signer[0].PublicKey()) }

// Close stops accepting connections.
func (s *SCP) Close() error { return s.ln.Close() }

func (s *SCP) config() *ssh.ServerConfig {
	sup, ins := ssh.SupportedAlgorithms(), ssh.InsecureAlgorithms()
	c := &ssh.ServerConfig{
		Config: ssh.Config{
			KeyExchanges: append(append([]string{}, sup.KeyExchanges...), ins.KeyExchanges...),
			Ciphers:      append(append([]string{}, sup.Ciphers...), ins.Ciphers...),
			MACs:         append(append([]string{}, sup.MACs...), ins.MACs...),
		},
		PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			okUser := subtle.ConstantTimeCompare([]byte(m.User()), []byte(s.cfg.User)) == 1
			okPass := subtle.ConstantTimeCompare(p, []byte(s.cfg.Password)) == 1
			if okUser && okPass {
				return nil, nil
			}
			time.Sleep(500 * time.Millisecond)
			return nil, errors.New("denied")
		},
		KeyboardInteractiveCallback: func(m ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			a, err := ch("", "", []string{"Password: "}, []bool{false})
			if err == nil && len(a) == 1 && m.User() == s.cfg.User && subtle.ConstantTimeCompare([]byte(a[0]), []byte(s.cfg.Password)) == 1 {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		MaxAuthTries: 4,
	}
	for _, k := range s.signer {
		c.AddHostKey(k)
	}
	return c
}

func (s *SCP) serve() {
	cfg := s.config()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.conn(nc, cfg)
	}
}

func (s *SCP) conn(nc net.Conn, cfg *ssh.ServerConfig) {
	if ra, ok := nc.RemoteAddr().(*net.TCPAddr); ok && !s.cfg.allowed(ra.IP) {
		s.cfg.emit(Event{Kind: "scp", Remote: ra.IP.String(), State: "error", Error: "refused: address not allowed"})
		nc.Close()
		return
	}
	_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	remote, _, _ := net.SplitHostPort(nc.RemoteAddr().String())
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.Prohibited, "this server only copies files")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, creqs, remote)
	}
}

func (s *SCP) session(ch ssh.Channel, reqs <-chan *ssh.Request, remote string) {
	defer ch.Close()
	for r := range reqs {
		switch r.Type {
		case "env", "pty-req":
			if r.WantReply {
				r.Reply(true, nil)
			}
		case "exec":
			var p struct{ Cmd string }
			_ = ssh.Unmarshal(r.Payload, &p)
			if !xfer.IsSCPCommand(p.Cmd) {
				r.Reply(true, nil)
				io.WriteString(ch.Stderr(), "This YACM server only copies files with scp.\r\n")
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{1}))
				return
			}
			r.Reply(true, nil)
			code := s.handler(remote).Serve(ch, p.Cmd)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
			return
		case "shell":
			r.Reply(true, nil)
			io.WriteString(ch, "This YACM server only copies files with scp.\r\n")
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{1}))
			return
		default:
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
	}
}

func (s *SCP) handler(remote string) xfer.SCPHandler {
	return xfer.SCPHandler{
		MaxSize: s.cfg.MaxUpload,
		Resolve: func(p, name string, write bool) (string, error) {
			if write && !s.cfg.AllowUpload {
				return "", errors.New("uploads are off on this server")
			}
			full, err := xfer.InRoot(s.cfg.Folder, p, name)
			if err != nil {
				return "", err
			}
			if !write {
				full = xfer.SpaceAlias(full)
			}
			if write {
				if _, err := os.Stat(full); err == nil && !s.cfg.Overwrite {
					return "", errors.New("file already exists")
				}
				s.cfg.emit(Event{Kind: "scp", Remote: remote, File: displayName(s.cfg.Folder, full), Upload: true, State: "start"})
			} else {
				st, err := os.Stat(full)
				if err != nil {
					return "", errors.New("file not found")
				}
				s.cfg.emit(Event{Kind: "scp", Remote: remote, File: displayName(s.cfg.Folder, full), Total: st.Size(), State: "start"})
			}
			return full, nil
		},
		Done: func(local string, size int64, upload bool, err error) {
			ev := Event{Kind: "scp", Remote: remote, File: displayName(s.cfg.Folder, local), Upload: upload, Done: size, Total: size, State: "done"}
			if err != nil {
				ev.State, ev.Error = "error", err.Error()
			}
			s.cfg.emit(ev)
		},
	}
}
