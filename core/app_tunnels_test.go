// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/yacm-app/yacm/internal/fakedevice"
	"github.com/yacm-app/yacm/internal/fileserver"
	"github.com/yacm-app/yacm/internal/store"
	"github.com/yacm-app/yacm/internal/tunnel"
)

func shellDevice(t *testing.T, gateway bool) int {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	srv := &fakedevice.Server{Config: fakedevice.PasswordConfig("admin", "pw", signer), RemoteForward: true,
		Shell: func(rw io.ReadWriter) { io.WriteString(rw, "R9#"); io.Copy(io.Discard, rw) }}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return ln.Addr().(*net.TCPAddr).Port
}

func waitState(t *testing.T, a *App, id, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		tb, _ := a.getTab(id)
		if tb != nil {
			tb.mu.Lock()
			st := tb.state
			tb.mu.Unlock()
			if st == want {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("tab never %s", want)
}

func TestTunnelsProxyAndFileServerTunnel(t *testing.T) {
	a, _ := fileTestApp(t)
	jumpPort := shellDevice(t, true)
	devPort := shellDevice(t, false)
	webLn, _ := net.Listen("tcp", "127.0.0.1:0")
	defer webLn.Close()
	go http.Serve(webLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "device web UI") }))
	webPort := webLn.Addr().(*net.TCPAddr).Port

	// An outbound SOCKS5 proxy: a dynamic tunnel of another SSH connection.
	pc, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", jumpPort), &ssh.ClientConfig{User: "admin", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	socks, err := tunnel.Start(pc, tunnel.Spec{Type: tunnel.Dynamic}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer socks.Close()
	_, sp, _ := net.SplitHostPort(socks.Addr())
	var socksPort int
	fmt.Sscan(sp, &socksPort)

	g, err := a.SaveNode(store.Node{Name: "Lab", IsGroup: true, Defaults: &store.GroupDefaults{Proxy: &store.Proxy{Type: "socks5", Host: "127.0.0.1", Port: socksPort}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := freeTCP(t)
	n, err := a.SaveNode(store.Node{Name: "R9", ParentID: g.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: devPort, User: "admin", Auth: store.AuthPassword,
		Jump:    []store.JumpHost{{Host: "127.0.0.1", Port: jumpPort, User: "admin", Auth: store.AuthPassword}},
		Tunnels: []store.Tunnel{{Type: "local", Port: local, Host: "127.0.0.1", HostPort: webPort}, {Type: "dynamic", Port: freeTCP(t), Off: true}}}},
		&SecretInput{Password: sp2("pw"), Jump: map[string]*string{"0": sp2("pw")}})
	if err != nil {
		t.Fatal(err)
	}
	eff, _ := a.store.Effective(n)
	if eff.Proxy == nil || eff.Proxy.From != "Lab" {
		t.Fatalf("proxy not inherited: %+v", eff.Proxy)
	}
	id, err := a.Connect(ConnectRequest{NodeID: n.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, a, id, "connected")
	if socks.Status().Total == 0 {
		t.Fatal("the session did not go through the proxy")
	}
	// The saved local tunnel runs; the switched-off one does not.
	var body string
	for i := 0; i < 40 && body == ""; i++ {
		if resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", local)); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
		} else {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if body != "device web UI" {
		t.Fatalf("local tunnel: %q", body)
	}
	sts, _ := a.TunnelStatuses(id)
	states := map[string]string{}
	for _, s := range sts {
		states[s.Type] = s.State
	}
	if states["local"] != "running" || states["dynamic"] != "off" {
		t.Fatalf("statuses %+v", sts)
	}
	// Start the switched-off one by hand, stop it again.
	for _, s := range sts {
		if s.Type == "dynamic" {
			if _, err := a.TunnelStart(id, s.Spec); err != nil {
				t.Fatal(err)
			}
			if err := a.TunnelStop(id, s.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	// A file server reached through the jump host.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ios.bin"), []byte("image"), 0o644)
	if _, err := a.FileServerStart(fileserver.Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: freeTCP(t)}); err != nil {
		t.Fatal(err)
	}
	defer a.FileServerStop("http")
	if _, err := a.FileServerViaTunnel(id, "tftp"); err == nil {
		t.Fatal("TFTP through a tunnel")
	}
	ft, err := a.FileServerViaTunnel(id, "http")
	if err != nil {
		t.Fatal(err)
	}
	if !ft.Reachable || ft.Address != "127.0.0.1" {
		t.Fatalf("file server tunnel %+v", ft)
	}
	resp, err := http.Get(fmt.Sprintf("http://%s:%d/ios.bin", ft.Address, ft.Port))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "image" {
		t.Fatalf("through the tunnel: %q", b)
	}

	// Disconnecting stops the tunnels.
	a.Disconnect(id)
	waitState(t, a, id, "closed")
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), time.Second); err == nil {
		t.Fatal("tunnel still listening after disconnect")
	}

	// A telnet session of the same group goes through the proxy too.
	tln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer tln.Close()
	got := make(chan bool, 1)
	go func() {
		c, err := tln.Accept()
		if err != nil {
			return
		}
		got <- true
		fmt.Fprint(c, "R1>")
		time.Sleep(time.Second)
		c.Close()
	}()
	before := socks.Status().Total
	tn, err := a.SaveNode(store.Node{Name: "old router", ParentID: g.ID, Session: &store.Session{Protocol: store.ProtoTelnet, Host: "127.0.0.1", Port: tln.Addr().(*net.TCPAddr).Port}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eff, _ := a.store.Effective(tn); eff.Proxy == nil || eff.Proxy.From != "Lab" {
		t.Fatalf("telnet: proxy not inherited: %+v", eff.Proxy)
	}
	tid, err := a.Connect(ConnectRequest{NodeID: tn.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, a, tid, "connected")
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the telnet session did not arrive")
	}
	if socks.Status().Total == before {
		t.Fatal("the telnet session did not go through the proxy")
	}
	a.Disconnect(tid)
}

func TestImportedTunnelsAreOff(t *testing.T) {
	n := &store.Node{Session: &store.Session{Tunnels: []store.Tunnel{{Type: "remote", Port: 22}}}}
	store.DisarmTunnels(n)
	if !n.Session.Tunnels[0].Off {
		t.Fatal("not switched off")
	}
	if err := store.Validate(&store.Node{Name: "x", Session: &store.Session{Protocol: store.ProtoSSH, Host: "h", Tunnels: []store.Tunnel{{Type: "local", Port: 80}}}}); err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("validate: %v", err)
	}
}

func freeTCP(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func sp2(s string) *string { return &s }
