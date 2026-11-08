// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package tunnel forwards ports through an SSH connection: local (-L),
// remote (-R) and dynamic (-D, a SOCKS5, SOCKS4a and HTTP proxy), and
// dials the first SSH hop through an outbound SOCKS5 or HTTP proxy.
package tunnel

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Types of tunnels.
const (
	Local   = "local"   // listen here, connect from the SSH server
	Remote  = "remote"  // listen on the SSH server, connect from here
	Dynamic = "dynamic" // listen here as a SOCKS / HTTP proxy
)

// Spec describes one tunnel.
type Spec struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Bind     string `json:"bind,omitempty"` // listen address; "" = 127.0.0.1
	Port     int    `json:"port"`           // listen port (0 = any free port)
	Host     string `json:"host,omitempty"` // target (local, remote)
	HostPort int    `json:"hostPort,omitempty"`
}

// Validate checks a spec.
func (s Spec) Validate() error {
	switch s.Type {
	case Local, Remote, Dynamic:
	default:
		return fmt.Errorf("unknown tunnel type %q", s.Type)
	}
	if s.Port < 0 || s.Port > 65535 {
		return errors.New("the listen port must be 1 to 65535")
	}
	if s.Bind != "" && s.Bind != "localhost" && net.ParseIP(s.Bind) == nil {
		return fmt.Errorf("%q is not an IP address to listen on", s.Bind)
	}
	if s.Type != Dynamic {
		if strings.TrimSpace(s.Host) == "" || strings.ContainsAny(s.Host, " /") {
			return errors.New("enter the host to forward to")
		}
		if s.HostPort < 1 || s.HostPort > 65535 {
			return errors.New("the destination port must be 1 to 65535")
		}
	}
	return nil
}

// Describe is the ssh-style summary: "L 127.0.0.1:8443 → 10.0.0.1:443".
func (s Spec) Describe() string {
	bind := s.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	l := net.JoinHostPort(bind, strconv.Itoa(s.Port))
	switch s.Type {
	case Local:
		return fmt.Sprintf("L %s → %s", l, net.JoinHostPort(s.Host, strconv.Itoa(s.HostPort)))
	case Remote:
		return fmt.Sprintf("R %s (on the server) → %s", l, net.JoinHostPort(s.Host, strconv.Itoa(s.HostPort)))
	}
	return fmt.Sprintf("D %s (SOCKS / HTTP proxy)", l)
}

// Status is a running (or failed) tunnel.
type Status struct {
	Spec
	Listen string `json:"listen"` // actual address
	State  string `json:"state"`  // running, error, stopped
	Error  string `json:"error,omitempty"`
	Conns  int64  `json:"conns"` // open connections
	Total  int64  `json:"total"` // connections so far
	In     int64  `json:"in"`    // bytes towards this computer
	Out    int64  `json:"out"`   // bytes away from it
	Last   string `json:"last,omitempty"`
}

// Tunnel is one running forward.
type Tunnel struct {
	spec   Spec
	ln     net.Listener
	client *ssh.Client
	conns  atomic.Int64
	total  atomic.Int64
	in     atomic.Int64
	out    atomic.Int64
	mu     sync.Mutex
	err    string
	last   string
	open   map[net.Conn]struct{}
	closed atomic.Bool
	notify func()
}

// Start opens the listener of spec on (or through) client. notify is told
// when connections open or close.
func Start(client *ssh.Client, spec Spec, notify func()) (*Tunnel, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	bind := spec.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := net.JoinHostPort(bind, strconv.Itoa(spec.Port))
	t := &Tunnel{spec: spec, client: client, open: map[net.Conn]struct{}{}, notify: notify}
	var err error
	if spec.Type == Remote {
		t.ln, err = client.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("the SSH server refused to listen on %s (it may not allow remote forwarding, or the port is in use): %v", addr, err)
		}
	} else {
		t.ln, err = net.Listen("tcp", addr)
		if err != nil {
			if strings.Contains(err.Error(), "in use") || strings.Contains(err.Error(), "Only one usage") {
				return nil, fmt.Errorf("port %d is already used on this computer", spec.Port)
			}
			return nil, err
		}
	}
	go t.serve()
	return t, nil
}

// Spec returns the tunnel's settings.
func (t *Tunnel) Spec() Spec { return t.spec }

// Addr is where the tunnel listens.
func (t *Tunnel) Addr() string { return t.ln.Addr().String() }

// Status reports the tunnel's counters.
func (t *Tunnel) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := Status{Spec: t.spec, Listen: t.ln.Addr().String(), State: "running", Error: t.err, Conns: t.conns.Load(), Total: t.total.Load(), In: t.in.Load(), Out: t.out.Load(), Last: t.last}
	if t.closed.Load() {
		st.State = "stopped"
	}
	return st
}

// Close stops listening and drops open connections.
func (t *Tunnel) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	err := t.ln.Close()
	t.mu.Lock()
	for c := range t.open {
		c.Close()
	}
	t.mu.Unlock()
	return err
}

func (t *Tunnel) setErr(format string, a ...any) {
	t.mu.Lock()
	t.err = fmt.Sprintf(format, a...)
	t.mu.Unlock()
	t.changed()
}

func (t *Tunnel) changed() {
	if t.notify != nil {
		t.notify()
	}
}

func (t *Tunnel) serve() {
	for {
		c, err := t.ln.Accept()
		if err != nil {
			if !t.closed.Load() {
				t.setErr("stopped listening: %v", err)
			}
			return
		}
		go t.handle(c)
	}
}

func (t *Tunnel) track(c net.Conn, on bool) {
	t.mu.Lock()
	if on {
		t.open[c] = struct{}{}
	} else {
		delete(t.open, c)
	}
	t.mu.Unlock()
}

func (t *Tunnel) handle(c net.Conn) {
	t.track(c, true)
	defer t.track(c, false)
	defer c.Close()
	t.conns.Add(1)
	t.total.Add(1)
	t.changed()
	defer func() { t.conns.Add(-1); t.changed() }()

	var target net.Conn
	var err error
	var rd io.Reader = c
	switch t.spec.Type {
	case Local:
		dest := net.JoinHostPort(t.spec.Host, strconv.Itoa(t.spec.HostPort))
		target, err = t.client.Dial("tcp", dest)
		if err != nil {
			t.setErr("%s: %v", dest, err)
			return
		}
		t.note(dest)
	case Remote:
		dest := net.JoinHostPort(t.spec.Host, strconv.Itoa(t.spec.HostPort))
		target, err = net.DialTimeout("tcp", dest, 10*time.Second)
		if err != nil {
			t.setErr("%s: %v", dest, err)
			return
		}
		t.note(dest)
	case Dynamic:
		br := bufio.NewReader(c)
		var dest string
		target, dest, err = t.proxyHandshake(c, br)
		if err != nil {
			t.setErr("proxy: %v", err)
			return
		}
		t.note(dest)
		rd = br
	}
	defer target.Close()
	t.track(target, true)
	defer t.track(target, false)
	t.mu.Lock()
	t.err = ""
	t.mu.Unlock()
	pipe(c, rd, target, &t.in, &t.out, t.spec.Type == Remote)
}

func (t *Tunnel) note(dest string) {
	t.mu.Lock()
	t.last = dest
	t.mu.Unlock()
}

type closeWriter interface{ CloseWrite() error }

// pipe copies both ways until both sides are done. For a remote tunnel the
// accepted side is the far one, so the counters swap.
func pipe(c net.Conn, rd io.Reader, target net.Conn, in, out *atomic.Int64, swap bool) {
	up, down := out, in
	if swap {
		up, down = in, out
	}
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(countW{target, up}, rd)
		_ = n
		if cw, ok := target.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			target.Close()
		}
		close(done)
	}()
	_, _ = io.Copy(countW{c, down}, target)
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	} else {
		c.Close()
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}
}

type countW struct {
	w io.Writer
	n *atomic.Int64
}

func (c countW) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// ---- dynamic: SOCKS5, SOCKS4a, HTTP CONNECT and plain HTTP proxying -------

func (t *Tunnel) dial(dest string) (net.Conn, error) {
	return t.client.Dial("tcp", dest)
}

func (t *Tunnel) proxyHandshake(c net.Conn, br *bufio.Reader) (net.Conn, string, error) {
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	first, err := br.Peek(1)
	if err != nil {
		return nil, "", err
	}
	switch first[0] {
	case 5:
		return t.socks5(c, br)
	case 4:
		return t.socks4(c, br)
	default:
		return t.httpProxy(c, br)
	}
}

func (t *Tunnel) socks5(c net.Conn, br *bufio.Reader) (net.Conn, string, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return nil, "", err
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return nil, "", err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0 {
			noAuth = true
		}
	}
	if !noAuth {
		c.Write([]byte{5, 0xff})
		return nil, "", errors.New("the client wants a SOCKS login; this proxy needs none")
	}
	c.Write([]byte{5, 0})
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return nil, "", err
	}
	if req[1] != 1 { // CONNECT only
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return nil, "", errors.New("only CONNECT is supported")
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return nil, "", err
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return nil, "", err
		}
		host = net.IP(b).String()
	case 3:
		l, err := br.ReadByte()
		if err != nil {
			return nil, "", err
		}
		b := make([]byte, l)
		if _, err := io.ReadFull(br, b); err != nil {
			return nil, "", err
		}
		host = string(b)
	default:
		c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return nil, "", errors.New("bad address type")
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return nil, "", err
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(pb[0])<<8|int(pb[1])))
	target, err := t.dial(dest)
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return nil, dest, fmt.Errorf("%s: %v", dest, err)
	}
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	return target, dest, nil
}

func (t *Tunnel) socks4(c net.Conn, br *bufio.Reader) (net.Conn, string, error) {
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return nil, "", err
	}
	if hdr[1] != 1 {
		c.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
		return nil, "", errors.New("only CONNECT is supported")
	}
	if _, err := br.ReadString(0); err != nil { // user id
		return nil, "", err
	}
	port := int(hdr[2])<<8 | int(hdr[3])
	host := net.IP(hdr[4:8]).String()
	if hdr[4] == 0 && hdr[5] == 0 && hdr[6] == 0 && hdr[7] != 0 { // SOCKS4a: name follows
		name, err := br.ReadString(0)
		if err != nil {
			return nil, "", err
		}
		host = strings.TrimSuffix(name, "\x00")
	}
	dest := net.JoinHostPort(host, strconv.Itoa(port))
	target, err := t.dial(dest)
	if err != nil {
		c.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
		return nil, dest, fmt.Errorf("%s: %v", dest, err)
	}
	c.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0})
	return target, dest, nil
}

// httpProxy handles CONNECT (HTTPS) and plain requests with an absolute URL.
func (t *Tunnel) httpProxy(c net.Conn, br *bufio.Reader) (net.Conn, string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, "", err
	}
	f := strings.Fields(line)
	if len(f) != 3 {
		return nil, "", fmt.Errorf("not a proxy request: %q", strings.TrimSpace(line))
	}
	var hdrs []string
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			return nil, "", err
		}
		if strings.TrimSpace(h) == "" {
			break
		}
		hdrs = append(hdrs, h)
	}
	fail := func(code string) {
		fmt.Fprintf(c, "HTTP/1.1 %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code)
	}
	if strings.EqualFold(f[0], "CONNECT") {
		dest := f[1]
		if _, _, err := net.SplitHostPort(dest); err != nil {
			dest = net.JoinHostPort(dest, "443")
		}
		target, err := t.dial(dest)
		if err != nil {
			fail("502 Bad Gateway")
			return nil, dest, fmt.Errorf("%s: %v", dest, err)
		}
		io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
		return target, dest, nil
	}
	// GET http://host[:port]/path HTTP/1.1
	u := f[1]
	if !strings.HasPrefix(strings.ToLower(u), "http://") {
		fail("400 Bad Request")
		return nil, "", errors.New("plain HTTP requests need an absolute http:// address")
	}
	rest := u[len("http://"):]
	hostport, path := rest, "/"
	if i := strings.Index(rest, "/"); i >= 0 {
		hostport, path = rest[:i], rest[i:]
	}
	dest := hostport
	if _, _, err := net.SplitHostPort(dest); err != nil {
		dest = net.JoinHostPort(hostport, "80")
	}
	target, err := t.dial(dest)
	if err != nil {
		fail("502 Bad Gateway")
		return nil, dest, fmt.Errorf("%s: %v", dest, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\r\n", f[0], path, f[2])
	for _, h := range hdrs {
		k := strings.ToLower(strings.TrimSpace(strings.SplitN(h, ":", 2)[0]))
		if k == "proxy-connection" || k == "proxy-authorization" || k == "connection" || k == "keep-alive" {
			continue
		}
		b.WriteString(h)
	}
	b.WriteString("Connection: close\r\n\r\n")
	if _, err := io.WriteString(target, b.String()); err != nil {
		target.Close()
		return nil, dest, err
	}
	return target, dest, nil
}

// ---- outbound proxy for the first hop -------------------------------------

// Proxy is an outbound proxy the first SSH hop is reached through.
type Proxy struct {
	Type     string // socks5, http
	Addr     string // host:port
	User     string
	Password string
}

// Dial connects to addr through the proxy.
func (p Proxy) Dial(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %v", p.Addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	} else if timeout > 0 {
		_ = c.SetDeadline(time.Now().Add(timeout))
	}
	switch p.Type {
	case "http":
		err = p.connect(c, addr)
	default:
		err = p.socks5(c, addr)
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("proxy %s: %v", p.Addr, err)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func (p Proxy) connect(c net.Conn, addr string) error {
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
	if p.User != "" {
		req += "Proxy-Authorization: Basic " + basicAuth(p.User, p.Password) + "\r\n"
	}
	if _, err := io.WriteString(c, req+"\r\n"); err != nil {
		return err
	}
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(l) == "" {
			break
		}
	}
	f := strings.Fields(status)
	if len(f) < 2 || f[1] != "200" {
		return fmt.Errorf("the proxy refused: %s", strings.TrimSpace(status))
	}
	if br.Buffered() > 0 {
		return errors.New("the proxy sent data too early")
	}
	return nil
}

func (p Proxy) socks5(c net.Conn, addr string) error {
	host, portS, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(portS)
	methods := []byte{0}
	if p.User != "" {
		methods = []byte{0, 2}
	}
	if _, err := c.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	r := make([]byte, 2)
	if _, err := io.ReadFull(c, r); err != nil {
		return err
	}
	switch r[1] {
	case 0:
	case 2:
		if len(p.User) > 255 || len(p.Password) > 255 {
			return errors.New("proxy login too long")
		}
		msg := append([]byte{1, byte(len(p.User))}, p.User...)
		msg = append(append(msg, byte(len(p.Password))), p.Password...)
		if _, err := c.Write(msg); err != nil {
			return err
		}
		if _, err := io.ReadFull(c, r); err != nil {
			return err
		}
		if r[1] != 0 {
			return errors.New("the proxy refused the login")
		}
	default:
		return errors.New("the proxy wants a login method YACM does not have")
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(append(req, 1), ip.To4()...)
	} else if ip != nil {
		req = append(append(req, 4), ip.To16()...)
	} else {
		if len(host) > 255 {
			return errors.New("host name too long")
		}
		req = append(append(req, 3, byte(len(host))), host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 4)
	if _, err := io.ReadFull(c, resp); err != nil {
		return err
	}
	if resp[1] != 0 {
		return fmt.Errorf("the proxy could not connect to %s (SOCKS error %d)", addr, resp[1])
	}
	skip := 0
	switch resp[3] {
	case 1:
		skip = 4
	case 4:
		skip = 16
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return err
		}
		skip = int(l[0])
	}
	_, err = io.ReadFull(c, make([]byte, skip+2))
	return err
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}
