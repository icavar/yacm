// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package tunnel

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/yacm-app/yacm/internal/fakedevice"
)

func sshServer(t *testing.T) *ssh.Client {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	srv := &fakedevice.Server{Config: fakedevice.PasswordConfig("u", "p", signer), RemoteForward: true}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "u", Auth: []ssh.AuthMethod{ssh.Password("p")}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// web is a tiny web server ("the device's HTTPS UI").
func web(t *testing.T) (string, int) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "device UI %s", r.URL.Path) }))
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

func get(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestLocalAndRemote(t *testing.T) {
	c := sshServer(t)
	host, port := web(t)
	l, err := Start(c, Spec{ID: "l", Type: Local, Host: host, HostPort: port}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if got := get(t, http.DefaultClient, "http://"+l.Addr()+"/x"); got != "device UI /x" {
		t.Fatalf("local: %q", got)
	}
	// The byte counters follow the copy, which can end just after the
	// client has read the reply: give them a moment.
	st := l.Status()
	for end := time.Now().Add(2 * time.Second); (st.In == 0 || st.Out == 0) && time.Now().Before(end); st = l.Status() {
		time.Sleep(10 * time.Millisecond)
	}
	if st.Total != 1 || st.In == 0 || st.Out == 0 || !strings.HasPrefix(st.Listen, "127.0.0.1:") {
		t.Fatalf("status %+v", st)
	}
	// Remote: the server listens, this computer answers.
	r, err := Start(c, Spec{ID: "r", Type: Remote, Host: host, HostPort: port}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := get(t, http.DefaultClient, "http://"+r.Addr()+"/y"); got != "device UI /y" {
		t.Fatalf("remote: %q", got)
	}
	// Port in use.
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	if _, err := Start(c, Spec{Type: Local, Port: busy.Addr().(*net.TCPAddr).Port, Host: host, HostPort: port}, nil); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("busy: %v", err)
	}
	if err := (Spec{Type: Local, Host: "", HostPort: 1}).Validate(); err == nil {
		t.Fatal("empty host accepted")
	}
}

func TestDynamicProxy(t *testing.T) {
	c := sshServer(t)
	host, port := web(t)
	d, err := Start(c, Spec{ID: "d", Type: Dynamic}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// HTTP proxy (plain and CONNECT).
	pc := &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*urlT, error) { return parseURL("http://" + d.Addr()) }}}
	if got := get(t, pc, fmt.Sprintf("http://%s:%d/plain", host, port)); got != "device UI /plain" {
		t.Fatalf("http proxy: %q", got)
	}
	conn, _ := net.Dial("tcp", d.Addr())
	fmt.Fprintf(conn, "CONNECT %s:%d HTTP/1.1\r\nHost: x\r\n\r\n", host, port)
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("connect: %q", line)
	}
	for l, _ := br.ReadString('\n'); strings.TrimSpace(l) != ""; l, _ = br.ReadString('\n') {
	}
	fmt.Fprintf(conn, "GET /tls HTTP/1.0\r\n\r\n")
	b, _ := io.ReadAll(br)
	conn.Close()
	if !strings.Contains(string(b), "device UI /tls") {
		t.Fatalf("connect body: %q", b)
	}
	// SOCKS5 through our own outbound proxy client, by name and by IP.
	for _, target := range []string{net.JoinHostPort(host, strconv.Itoa(port)), net.JoinHostPort("localhost", strconv.Itoa(port))} {
		pconn, err := Proxy{Type: "socks5", Addr: d.Addr()}.Dial(context.Background(), target, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(pconn, "GET /socks HTTP/1.0\r\n\r\n")
		b, _ := io.ReadAll(pconn)
		pconn.Close()
		if !strings.Contains(string(b), "device UI /socks") {
			t.Fatalf("socks5 %s: %q", target, b)
		}
	}
	// HTTP CONNECT outbound proxy client against our dynamic tunnel.
	pconn, err := Proxy{Type: "http", Addr: d.Addr(), User: "a", Password: "b"}.Dial(context.Background(), net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(pconn, "GET /hc HTTP/1.0\r\n\r\n")
	b, _ = io.ReadAll(pconn)
	pconn.Close()
	if !strings.Contains(string(b), "device UI /hc") {
		t.Fatalf("http connect client: %q", b)
	}
	// SOCKS4a.
	conn, _ = net.Dial("tcp", d.Addr())
	req := []byte{4, 1, byte(port >> 8), byte(port), 0, 0, 0, 1, 0}
	req = append(append(req, "localhost"...), 0)
	conn.Write(req)
	resp := make([]byte, 8)
	io.ReadFull(conn, resp)
	if resp[1] != 90 {
		t.Fatalf("socks4a: %v", resp)
	}
	fmt.Fprintf(conn, "GET /s4 HTTP/1.0\r\n\r\n")
	b, _ = io.ReadAll(conn)
	conn.Close()
	if !strings.Contains(string(b), "device UI /s4") {
		t.Fatalf("socks4a body: %q", b)
	}
	// A refused destination is reported.
	if _, err := (Proxy{Type: "socks5", Addr: d.Addr()}).Dial(context.Background(), "10.255.255.1:1", 5*time.Second); err == nil {
		t.Fatal("blocked destination connected")
	}
	time.Sleep(50 * time.Millisecond)
	if d.Status().Error == "" {
		t.Fatal("no error recorded")
	}
}
