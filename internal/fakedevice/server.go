// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package fakedevice

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server is the SSH side of a pretend device: a shell, exec commands and
// SCP on FS, and jump host forwarding to 127.0.0.1.
type Server struct {
	Config *ssh.ServerConfig
	FS     *FS
	// Shell runs an interactive session (nil: none).
	Shell func(rw io.ReadWriter)
	// SFTP offers the sftp subsystem in FS.Root (tests only: it is not
	// confined to the folder).
	SFTP bool
	// Exec handles exec commands FS does not know (nil: "Invalid input").
	Exec func(rw io.ReadWriter, cmd string) (int, bool)
	// RemoteForward allows "ssh -R" listeners on 127.0.0.1.
	RemoteForward bool
	// MaxSessions limits the session channels open at once on one
	// connection, as Cisco IOS and IOS XE do (one): more are rejected with
	// "resource shortage". 0: no limit.
	MaxSessions int
	// SFTPSilent accepts the sftp subsystem and never answers (Huawei VRP
	// for a user without SFTP rights).
	SFTPSilent bool
	// SCPDisabled answers SCP like Cisco IOS without ip scp server enable.
	SCPDisabled bool
	// CloseAfterSession closes the connection when a session channel ends,
	// as Cisco IOS does: one command (dir, an SCP copy) per connection.
	CloseAfterSession bool
	// ExecDelay waits before running each exec command, like a slow device
	// (tests use it so that commands overlap).
	ExecDelay time.Duration
	Logf      func(format string, args ...any)
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve(ln net.Listener) {
	for {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		go s.ServeConn(nc)
	}
}

// ServeConn handles one SSH connection.
func (s *Server) ServeConn(nc net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, s.Config)
	if err != nil {
		nc.Close()
		return
	}
	defer sc.Close()
	go s.globalRequests(sc, reqs)
	// A slot per session channel. A device handles the messages of a
	// connection in order, so a channel the client just closed is free for
	// the next one: wait a moment for its slot before refusing.
	var slots chan struct{}
	if s.MaxSessions > 0 {
		slots = make(chan struct{}, s.MaxSessions)
	}
	for nch := range chans {
		switch nch.ChannelType() {
		case "direct-tcpip":
			go s.forward(nch)
		case "session":
			if slots != nil {
				select {
				case slots <- struct{}{}:
				case <-time.After(10 * time.Millisecond):
					s.logf("session channel refused: %d open", s.MaxSessions)
					nch.Reject(ssh.ResourceShortage, "")
					continue
				}
			}
			free := func() {
				if slots != nil {
					<-slots
				}
			}
			ch, creqs, err := nch.Accept()
			if err != nil {
				free()
				continue
			}
			go func() {
				s.session(ch, creqs)
				free()
				if s.CloseAfterSession {
					sc.Close()
				}
			}()
		default:
			nch.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func exit(ch ssh.Channel, code int) {
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{uint32(code)}))
	ch.Close()
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for r := range reqs {
		switch r.Type {
		case "pty-req", "window-change", "env":
			if r.WantReply {
				r.Reply(true, nil)
			}
		case "shell":
			if s.Shell == nil {
				r.Reply(false, nil)
				continue
			}
			r.Reply(true, nil)
			go func() { s.Shell(ch); exit(ch, 0) }()
		case "exec":
			var p struct{ Cmd string }
			_ = ssh.Unmarshal(r.Payload, &p)
			r.Reply(true, nil)
			go func() {
				if s.ExecDelay > 0 {
					time.Sleep(s.ExecDelay)
				}
				if s.SCPDisabled && strings.HasPrefix(p.Cmd, "scp ") {
					io.WriteString(ch, "\x01Administratively disabled.\n")
					exit(ch, 1)
					return
				}
				code, ok := 0, false
				if s.FS != nil {
					code, ok = s.FS.Exec(ch, p.Cmd)
				}
				if !ok && s.Exec != nil {
					code, ok = s.Exec(ch, p.Cmd)
				}
				if !ok {
					io.WriteString(ch, "% Invalid input detected at '^' marker.\r\n")
				}
				exit(ch, code)
			}()
		case "subsystem":
			var p struct{ Name string }
			_ = ssh.Unmarshal(r.Payload, &p)
			if s.SFTPSilent && p.Name == "sftp" {
				r.Reply(true, nil)
				go io.Copy(io.Discard, ch)
				continue
			}
			if !s.SFTP || p.Name != "sftp" || s.FS == nil {
				r.Reply(false, nil)
				continue
			}
			r.Reply(true, nil)
			go func() {
				srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.FS.Root))
				if err == nil {
					_ = srv.Serve()
				}
				ch.Close()
			}()
		default:
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
	}
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// forward handles a jump-host request (ssh -J, -L): connect to the
// requested address (loopback only) and copy bytes both ways.
func (s *Server) forward(nch ssh.NewChannel) {
	var req struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(nch.ExtraData(), &req); err != nil {
		nch.Reject(ssh.ConnectionFailed, "bad request")
		return
	}
	if !loopback(req.Host) {
		nch.Reject(ssh.Prohibited, "this test device only forwards to 127.0.0.1")
		return
	}
	target, err := net.DialTimeout("tcp", net.JoinHostPort(req.Host, fmt.Sprint(req.Port)), 5*time.Second)
	if err != nil {
		nch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	s.logf("forwarding to %s:%d", req.Host, req.Port)
	go func() { io.Copy(target, ch); target.(*net.TCPConn).CloseWrite() }()
	io.Copy(ch, target)
	ch.CloseWrite()
	ch.Close()
	target.Close()
}

// globalRequests serves "tcpip-forward" (ssh -R) on 127.0.0.1.
func (s *Server) globalRequests(sc *ssh.ServerConn, reqs <-chan *ssh.Request) {
	listeners := map[string]net.Listener{}
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for r := range reqs {
		switch r.Type {
		case "tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			if !s.RemoteForward || ssh.Unmarshal(r.Payload, &p) != nil {
				r.Reply(false, nil)
				continue
			}
			addr := p.Addr
			if addr == "" || addr == "0.0.0.0" || addr == "localhost" {
				addr = "127.0.0.1"
			}
			if !loopback(addr) {
				r.Reply(false, nil)
				continue
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(addr, fmt.Sprint(p.Port)))
			if err != nil {
				r.Reply(false, nil)
				continue
			}
			port := uint32(ln.Addr().(*net.TCPAddr).Port)
			listeners[fmt.Sprintf("%s:%d", p.Addr, port)] = ln
			if p.Port == 0 {
				r.Reply(true, ssh.Marshal(struct{ Port uint32 }{port}))
			} else {
				r.Reply(true, nil)
			}
			s.logf("remote forward listening on %s", ln.Addr())
			go func(bindAddr string, port uint32) {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						ra := c.RemoteAddr().(*net.TCPAddr)
						ch, creqs, err := sc.OpenChannel("forwarded-tcpip", ssh.Marshal(struct {
							Addr       string
							Port       uint32
							OriginAddr string
							OriginPort uint32
						}{bindAddr, port, ra.IP.String(), uint32(ra.Port)}))
						if err != nil {
							return
						}
						go ssh.DiscardRequests(creqs)
						go func() { io.Copy(ch, c); ch.CloseWrite() }()
						io.Copy(c, ch)
						ch.Close()
					}()
				}
			}(p.Addr, port)
		case "cancel-tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			_ = ssh.Unmarshal(r.Payload, &p)
			key := fmt.Sprintf("%s:%d", p.Addr, p.Port)
			if l := listeners[key]; l != nil {
				l.Close()
				delete(listeners, key)
				r.Reply(true, nil)
			} else {
				r.Reply(false, nil)
			}
		default:
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
	}
}

// PasswordConfig accepts one user and password (and keyboard-interactive).
func PasswordConfig(user, password string, hostKey ssh.Signer) *ssh.ServerConfig {
	c := &ssh.ServerConfig{
		PasswordCallback: func(m ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if m.User() == user && string(p) == password {
				return nil, nil
			}
			time.Sleep(300 * time.Millisecond)
			return nil, fmt.Errorf("denied")
		},
		KeyboardInteractiveCallback: func(m ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			a, err := ch("", "", []string{"Password: "}, []bool{false})
			if err == nil && m.User() == user && len(a) == 1 && a[0] == password {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	c.AddHostKey(hostKey)
	return c
}
