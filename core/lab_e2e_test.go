// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build clab

// End-to-end tests against the containerlab test lab (testlab/clab): jump
// host chains, telnet through jump hosts, keyboard-interactive and legacy
// algorithms, file transfer to Linux, Arista EOS and the IOS-like demo
// device, tunnels, the outbound proxy and the file servers. Run them with
// "make -C testlab/clab e2e" (it starts and stops the lab).
package core

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yacm-app/yacm/internal/fileserver"
	"github.com/yacm-app/yacm/internal/sshconn"
	"github.com/yacm-app/yacm/internal/store"
	"github.com/yacm-app/yacm/internal/tunnel"
	"github.com/yacm-app/yacm/internal/xfer"
)

const (
	labBastion1   = "172.30.0.11"
	labLocked     = "172.30.0.12"
	labHostOnMgmt = "172.30.0.1" // this computer on the lab's management network
	labPass       = "lab"
)

func labFabric() string {
	if f := os.Getenv("YACM_LAB_FABRIC"); f != "" {
		return f
	}
	return "ceos"
}

func needCEOS(t *testing.T) {
	if labFabric() != "ceos" {
		t.Skip("needs the Arista cEOS fabric (FABRIC=ceos)")
	}
}

// labExec runs a command in a lab container.
func labExec(t *testing.T, node string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", "clab-yacm-" + node}, args...)...).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("docker exec %s %v: no answer after 2 minutes\n%s", node, args, out)
	}
	if err != nil {
		t.Fatalf("docker exec %s %v: %v\n%s", node, args, err, out)
	}
	return string(out)
}

// ---- an App that answers prompts and records what happens --------------------

type labEvents struct {
	mu            sync.Mutex
	out           map[string]*strings.Builder
	status        map[string]TabStatus
	xfer          map[string]TransferEvent
	fs            []fileserver.Event
	rejectChanged bool
	hostKeys      int
}

func labApp(t *testing.T) (*App, *labEvents) {
	t.Helper()
	a := newTestApp(t)
	ev := &labEvents{out: map[string]*strings.Builder{}, status: map[string]TabStatus{}, xfer: map[string]TransferEvent{}}
	a.emitter = func(name string, data any) {
		ev.mu.Lock()
		defer ev.mu.Unlock()
		switch name {
		case "tab:data":
			m := data.(map[string]string)
			b, _ := base64.StdEncoding.DecodeString(m["data"])
			if ev.out[m["tabId"]] == nil {
				ev.out[m["tabId"]] = &strings.Builder{}
			}
			ev.out[m["tabId"]].Write(b)
		case "tab:status":
			st := data.(TabStatus)
			ev.status[st.TabID] = st
		case "xfer:progress":
			x := data.(TransferEvent)
			ev.xfer[x.ID] = x
		case "fileserver:event":
			ev.fs = append(ev.fs, data.(fileserver.Event))
		case "prompt":
			p := data.(map[string]any)
			ans := PromptAnswer{Accept: true}
			switch p["kind"] {
			case "hostkey":
				ev.hostKeys++
				if hk, ok := p["hostKey"].(sshconn.HostKeyInfo); ok && hk.Changed && ev.rejectChanged {
					ans.Accept = false
				}
			case "secret", "questions":
				ans.Accept = false // every password is stored: a question here is a failure
				t.Logf("unexpected %v prompt: %v", p["kind"], p)
			}
			go a.AnswerPrompt(p["id"].(string), ans)
		}
	}
	t.Cleanup(func() {
		for _, ti := range a.GetTabs() {
			a.CloseTab(ti.TabID)
		}
		a.closeAllFiles()
		if a.fsrv != nil {
			a.fsrv.StopAll()
		}
	})
	return a, ev
}

func (ev *labEvents) output(tabID string) string {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if b := ev.out[tabID]; b != nil {
		return b.String()
	}
	return ""
}

func (ev *labEvents) state(tabID string) TabStatus {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	return ev.status[tabID]
}

// chain is the jump host route bastion1 -> bastion2.
func labChain() []store.JumpHost {
	return []store.JumpHost{
		{Host: labBastion1, Port: 22, User: "lab", Auth: store.AuthPassword},
		{Host: "10.0.1.2", Port: 22, User: "lab", Auth: store.AuthPassword},
	}
}

func labSecrets(password string, jumps int) *SecretInput {
	s := &SecretInput{Password: &password, Jump: map[string]*string{}}
	for i := 0; i < jumps; i++ {
		p := labPass
		s.Jump[strconv.Itoa(i)] = &p
	}
	return s
}

// labSession saves an SSH session through the jump chain.
func labSession(t *testing.T, a *App, name, host, user, password string, mod func(*store.Session)) *store.Node {
	t.Helper()
	ss := &store.Session{Protocol: store.ProtoSSH, Host: host, Port: 22, User: user, Auth: store.AuthPassword, Algorithms: store.AlgoCompatible, Jump: labChain(), ConnectTimeout: 20}
	if mod != nil {
		mod(ss)
	}
	n, err := a.SaveNode(store.Node{Name: name, Session: ss}, labSecrets(password, len(ss.Jump)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// labConnect opens a terminal tab and waits until it is connected.
func labConnect(t *testing.T, a *App, ev *labEvents, n *store.Node) string {
	t.Helper()
	id, err := a.Connect(ConnectRequest{NodeID: n.ID, Cols: 160, Rows: 48})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		switch st := ev.state(id); st.State {
		case "connected":
			return id
		case "error", "closed":
			t.Fatalf("%s: %s %s\n%s", n.Name, st.State, st.Message, ev.output(id))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: not connected after 90 s\n%s", n.Name, ev.output(id))
	return ""
}

// labExpect waits for text in a tab's output.
func labExpect(t *testing.T, ev *labEvents, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(ev.output(id), want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	out := ev.output(id)
	if len(out) > 3000 {
		out = out[len(out)-3000:]
	}
	t.Fatalf("no %q within %s; output:\n%s", want, timeout, out)
}

func (ev *labEvents) waitXfer(t *testing.T, id string, timeout time.Duration) TransferEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ev.mu.Lock()
		x := ev.xfer[id]
		ev.mu.Unlock()
		if x.State != "" && x.State != "running" && x.State != "verifying" {
			return x
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("transfer %s did not finish within %s", id, timeout)
	return TransferEvent{}
}

func labFile(t *testing.T, size int) (string, string) {
	t.Helper()
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i*31 + i>>11)
	}
	p := filepath.Join(t.TempDir(), fmt.Sprintf("yacm-lab-%d.bin", size))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(b)
	return p, hex.EncodeToString(sum[:])
}

func fileMD5(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// ---- tests --------------------------------------------------------------------

// The fabric is reachable only through the jump hosts.
func TestLabIsolation(t *testing.T) {
	needCEOS(t)
	out, err := exec.Command("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "clab-yacm-leaf1").Output()
	if err != nil {
		t.Fatal(err)
	}
	ip := strings.TrimSpace(string(out))
	a, _ := labApp(t)
	n, err := a.SaveNode(store.Node{Name: "leaf1 direct", Session: &store.Session{Protocol: store.ProtoSSH, Host: ip, Port: 22, User: "lab", Auth: store.AuthPassword, ConnectTimeout: 8}}, labSecrets(labPass, 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if c, err := a.dialSession(ctx, n, ""); err == nil {
		c.Close()
		t.Fatalf("leaf1 answered SSH on its management address %s: the lab's ACL does not work", ip)
	} else {
		t.Logf("direct SSH to leaf1 (%s) refused as it should be: %v", ip, err)
	}
}

func TestLabJumpChain(t *testing.T) {
	a, ev := labApp(t)
	n := labSession(t, a, "leaf1", "10.255.0.21", "lab", labPass, nil)
	id := labConnect(t, a, ev, n)
	if labFabric() == "ceos" {
		_ = a.Write(id, "show version\r")
		labExpect(t, ev, id, "cEOS", 20*time.Second)
	} else {
		_ = a.Write(id, "hostname\r")
		labExpect(t, ev, id, "leaf1", 20*time.Second)
	}
}

// A saved bastion session as the jump host of another session (JumpVia),
// itself behind bastion1.
func TestLabJumpViaSavedSession(t *testing.T) {
	a, ev := labApp(t)
	b1 := labSession(t, a, "bastion1", labBastion1, "lab", labPass, func(s *store.Session) { s.Jump = nil })
	b2 := labSession(t, a, "bastion2", "10.0.1.2", "lab", labPass, func(s *store.Session) { s.Jump = nil; s.JumpVia = b1.ID })
	n := labSession(t, a, "srv-sftp via saved jump", "10.2.1.10", "lab", labPass, func(s *store.Session) { s.Jump = nil; s.JumpVia = b2.ID })
	id := labConnect(t, a, ev, n)
	_ = a.Write(id, "hostname\r")
	labExpect(t, ev, id, "srv-sftp", 20*time.Second)
}

// Every fabric node at once through the same chain.
func TestLabManySessions(t *testing.T) {
	a, ev := labApp(t)
	nodes := map[string]string{"edge1": "10.255.0.1", "edge2": "10.255.0.2", "spine1": "10.255.0.11", "spine2": "10.255.0.12",
		"leaf1": "10.255.0.21", "leaf2": "10.255.0.22", "leaf3": "10.255.0.23", "leaf4": "10.255.0.24"}
	ids := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, ip := range nodes {
		n := labSession(t, a, name, ip, "lab", labPass, nil)
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			id := labConnect(t, a, ev, n)
			mu.Lock()
			ids[name] = id
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	all := make([]string, 0, len(ids))
	for _, id := range ids {
		all = append(all, id)
	}
	cmd := "hostname\r"
	if labFabric() == "ceos" {
		cmd = "show hostname\r"
	}
	a.Broadcast(all, cmd) // Type to many
	for name, id := range ids {
		labExpect(t, ev, id, name, 20*time.Second)
	}
}

func TestLabTelnetThroughJump(t *testing.T) {
	needCEOS(t)
	a, ev := labApp(t)
	n, err := a.SaveNode(store.Node{Name: "edge1 telnet", Session: &store.Session{Protocol: store.ProtoTelnet, Host: "10.255.0.1", Port: 23, User: "lab", Jump: labChain(),
		Expect: []store.ExpectRule{
			{ID: "u", Active: true, Expect: "(?i)(login|username):\\s*$", Send: "<USER>", Return: true, OnFail: "stop"},
			{ID: "p", Active: true, Expect: "[Pp]assword:\\s*$", Send: "<PASS>", Return: true, OnFail: "stop"},
		}}}, labSecrets(labPass, 2))
	if err != nil {
		t.Fatal(err)
	}
	id := labConnect(t, a, ev, n)
	labExpect(t, ev, id, "edge1#", 40*time.Second)
	_ = a.Write(id, "show hostname\r")
	labExpect(t, ev, id, "Hostname: edge1", 20*time.Second)
}

func TestLabKeyboardInteractive(t *testing.T) {
	a, ev := labApp(t)
	n := labSession(t, a, "srv-kbd", "10.2.3.10", "lab", labPass, nil)
	id := labConnect(t, a, ev, n)
	_ = a.Write(id, "hostname\r")
	labExpect(t, ev, id, "srv-kbd", 20*time.Second)
}

func TestLabKeyLogin(t *testing.T) {
	key := os.Getenv("YACM_LAB_KEY")
	if key == "" {
		t.Skip("YACM_LAB_KEY not set (make test sets it)")
	}
	a, ev := labApp(t)
	n := labSession(t, a, "srv-sftp key", "10.2.1.10", "lab", "", func(s *store.Session) { s.Auth = store.AuthPublicKey; s.KeyPath = key })
	id := labConnect(t, a, ev, n)
	_ = a.Write(id, "whoami\r")
	labExpect(t, ev, id, "lab", 20*time.Second)
}

func TestLabLegacyAlgorithms(t *testing.T) {
	a, ev := labApp(t)
	secure := labSession(t, a, "srv-legacy secure", "10.2.4.10", "lab", labPass, func(s *store.Session) { s.Algorithms = store.AlgoSecure })
	id, err := a.Connect(ConnectRequest{NodeID: secure.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for ev.state(id).State != "error" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if st := ev.state(id); st.State != "error" || !strings.Contains(strings.ToLower(st.Message), "legacy") {
		t.Fatalf("secure profile against an old server: %+v (want an error that suggests Legacy)", st)
	}
	legacy := labSession(t, a, "srv-legacy", "10.2.4.10", "lab", labPass, func(s *store.Session) { s.Algorithms = store.AlgoLegacy })
	id = labConnect(t, a, ev, legacy)
	_ = a.Write(id, "hostname\r")
	labExpect(t, ev, id, "srv-legacy", 20*time.Second)
}

func TestLabLockedBastion(t *testing.T) {
	a, ev := labApp(t)
	n := labSession(t, a, "srv-web via locked bastion", "10.2.2.10", "lab", labPass, func(s *store.Session) {
		s.Jump = []store.JumpHost{{Host: labLocked, Port: 22, User: "lab", Auth: store.AuthPassword}}
	})
	id := labConnect(t, a, ev, n)
	_ = a.Write(id, "hostname\r")
	labExpect(t, ev, id, "srv-web", 20*time.Second)
}

func TestLabFilesSFTP(t *testing.T) {
	a, ev := labApp(t)
	n := labSession(t, a, "srv-sftp", "10.2.1.10", "lab", labPass, nil)
	info, err := a.FilesOpen(FilesOpenRequest{NodeID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "sftp" || info.Home != "/home/lab" {
		t.Fatalf("files %+v", info)
	}
	local, sum := labFile(t, 32<<20)
	start := time.Now()
	id, err := a.FilesUpload(info.ID, local, "/home/lab/big.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	if x := ev.waitXfer(t, id, 3*time.Minute); x.State != "done" || x.Verified != "ok" || x.MD5 != sum {
		t.Fatalf("upload %+v", x)
	}
	took := time.Since(start)
	t.Logf("32 MB up through two jump hosts with MD5 check: %s", took.Round(time.Millisecond))
	// On the lab's veth links this takes well under a second; seconds mean
	// one SFTP request at a time, which is crippling over a real WAN.
	if took > 5*time.Second {
		t.Errorf("SFTP upload too slow: 32 MB in %s", took.Round(time.Millisecond))
	}
	dst := filepath.Join(t.TempDir(), "big.bin")
	id, _ = a.FilesDownload(info.ID, "/home/lab/big.bin", dst, 32<<20, true)
	if x := ev.waitXfer(t, id, 3*time.Minute); x.State != "done" || x.Verified != "ok" || fileMD5(t, dst) != sum {
		t.Fatalf("download %+v", x)
	}
	if err := a.FilesMkdir(info.ID, "/home/lab/d"); err != nil {
		t.Fatal(err)
	}
	if err := a.FilesRename(info.ID, "/home/lab/big.bin", "/home/lab/d/big2.bin"); err != nil {
		t.Fatal(err)
	}
	es, err := a.FilesList(info.ID, "/home/lab/d")
	if err != nil || len(es) != 1 || es[0].Size != 32<<20 {
		t.Fatalf("list %+v %v", es, err)
	}
	if err := a.FilesRemove(info.ID, "/home/lab/d/big2.bin"); err != nil {
		t.Fatal(err)
	}
	if err := a.FilesRemove(info.ID, "/home/lab/d"); err != nil {
		t.Fatal(err)
	}
	// Output of a command to a file.
	out := filepath.Join(t.TempDir(), "uname.txt")
	id, _ = a.FilesCommandToFile(info.ID, "uname -a", out)
	if x := ev.waitXfer(t, id, time.Minute); x.State != "done" {
		t.Fatalf("command %+v", x)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "Linux") {
		t.Fatalf("uname output %q", b)
	}
}

// The IOS-like demo device: SCP only, dir, verify /md5, show tech.
func TestLabFilesIOSDemo(t *testing.T) {
	a, ev := labApp(t)
	n := labSession(t, a, "ios1", "10.2.5.10", "admin", "cisco123", nil)
	info, err := a.FilesOpen(FilesOpenRequest{NodeID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "scp" || info.Home != "flash:" {
		t.Fatalf("files %+v", info)
	}
	local, sum := labFile(t, 4<<20)
	id, _ := a.FilesUpload(info.ID, local, "flash:"+filepath.Base(local), true)
	if x := ev.waitXfer(t, id, 2*time.Minute); x.State != "done" || x.Verified != "ok" || x.MD5 != sum {
		t.Fatalf("upload %+v", x)
	}
	es, err := a.FilesList(info.ID, "flash:")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range es {
		found = found || (e.Name == filepath.Base(local) && e.Size == 4<<20)
	}
	if !found {
		t.Fatalf("uploaded file not in dir: %+v", es)
	}
	tech := filepath.Join(t.TempDir(), "show_tech_ios1.txt")
	id, _ = a.FilesCommandToFile(info.ID, "show tech-support", tech)
	if x := ev.waitXfer(t, id, 2*time.Minute); x.State != "done" || x.Done < 100000 {
		t.Fatalf("show tech %+v", x)
	}
	a.FilesClose(info.ID)

	// Get file from the open session: ios1, like IOS, takes one session
	// channel per connection, and the terminal holds it.
	tab := labConnect(t, a, ev, n)
	got, err := a.FilesOpen(FilesOpenRequest{TabID: tab, OwnerID: tab, SCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Get file from the open ios1 session: %s", got.Summary)
	dst := filepath.Join(t.TempDir(), "back.bin")
	id, _ = a.FilesDownload(got.ID, "flash:"+filepath.Base(local), dst, 4<<20, true)
	if x := ev.waitXfer(t, id, 2*time.Minute); x.State != "done" || x.Verified != "ok" || fileMD5(t, dst) != sum {
		t.Fatalf("download from the open session %+v", x)
	}
	// The terminal still works.
	_ = a.Write(tab, "show version\r")
	labExpect(t, ev, tab, "ios1>show version", 10*time.Second)
}

// Arista EOS: SFTP or SCP, whatever the device offers.
func TestLabFilesEOS(t *testing.T) {
	needCEOS(t)
	a, ev := labApp(t)
	n := labSession(t, a, "leaf2", "10.255.0.22", "lab", labPass, nil)
	info, err := a.FilesOpen(FilesOpenRequest{NodeID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("EOS file access: %s, home %q, roots %+v", info.Kind, info.Home, info.Roots)
	es, err := a.FilesList(info.ID, info.Home)
	if err != nil {
		t.Fatalf("list %s: %v", info.Home, err)
	}
	t.Logf("%d entries in %s", len(es), info.Home)
	local, sum := labFile(t, 1<<20)
	remote := xfer.Join(info.Home, filepath.Base(local))
	id, _ := a.FilesUpload(info.ID, local, remote, true)
	x := ev.waitXfer(t, id, 2*time.Minute)
	if x.State != "done" {
		t.Fatalf("upload %+v", x)
	}
	if x.Verified != "ok" || x.MD5 != sum {
		t.Fatalf("MD5 check on EOS: %q (local %s, device %s)", x.Verified, sum, x.MD5)
	}
	dst := filepath.Join(t.TempDir(), "back.bin")
	id, _ = a.FilesDownload(info.ID, remote, dst, 1<<20, false)
	if x := ev.waitXfer(t, id, 2*time.Minute); x.State != "done" || fileMD5(t, dst) != sum {
		t.Fatalf("download %+v", x)
	}
	tech := filepath.Join(t.TempDir(), "show_tech_leaf2.txt")
	id, _ = a.FilesCommandToFile(info.ID, "show tech-support", tech)
	if x := ev.waitXfer(t, id, 5*time.Minute); x.State != "done" || x.Done < 20000 {
		t.Fatalf("show tech %+v", x)
	}
	_ = a.FilesRemove(info.ID, remote)
}

// Tunnels started with the session: -L to the internal web server, -D as
// a SOCKS and HTTP proxy, -R back to this computer.
func TestLabTunnels(t *testing.T) {
	a, ev := labApp(t)
	local, socks := freeTCP(t), freeTCP(t)
	web := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello from this computer") })}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go web.Serve(ln)
	defer web.Close()
	n := labSession(t, a, "srv-sftp tunnels", "10.2.1.10", "lab", labPass, func(s *store.Session) {
		s.Tunnels = []store.Tunnel{
			{Type: "local", Port: local, Host: "10.2.2.10", HostPort: 80},
			{Type: "dynamic", Port: socks},
			{Type: "remote", Port: 18080, Host: "127.0.0.1", HostPort: ln.Addr().(*net.TCPAddr).Port},
		}
	})
	id := labConnect(t, a, ev, n)
	get := func(c *http.Client, url string) string {
		t.Helper()
		var last error
		for i := 0; i < 30; i++ {
			resp, err := c.Get(url)
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				return string(b)
			}
			last = err
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("%s: %v", url, last)
		return ""
	}
	if got := get(&http.Client{Timeout: 20 * time.Second}, fmt.Sprintf("http://127.0.0.1:%d/", local)); !strings.Contains(got, "internal web server") {
		t.Fatalf("-L: %q", got)
	}
	proxy := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse(fmt.Sprintf("http://127.0.0.1:%d", socks)) }}}
	if got := get(proxy, "http://10.2.2.10/"); !strings.Contains(got, "internal web server") {
		t.Fatalf("-D as HTTP proxy: %q", got)
	}
	c, err := tunnel.Proxy{Type: "socks5", Addr: fmt.Sprintf("127.0.0.1:%d", socks)}.Dial(context.Background(), "10.2.2.10:80", 10*time.Second)
	if err != nil {
		t.Fatalf("-D as SOCKS5: %v", err)
	}
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))
	fmt.Fprint(c, "GET /1mb.bin HTTP/1.0\r\n\r\n")
	b, _ := io.ReadAll(c)
	c.Close()
	if len(b) < 1<<20 {
		t.Fatalf("SOCKS5 download: %d bytes", len(b))
	}
	if got := labExec(t, "srv-sftp", "curl", "-s", "--max-time", "10", "http://127.0.0.1:18080/"); got != "hello from this computer" {
		t.Fatalf("-R: %q", got)
	}
	sts, _ := a.TunnelStatuses(id)
	for _, s := range sts {
		if s.State != "running" {
			t.Fatalf("tunnel %+v", s)
		}
	}
	a.Disconnect(id)
	time.Sleep(time.Second)
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), time.Second); err == nil {
		c.Close()
		t.Fatal("tunnel still listening after disconnect")
	}
}

// The outbound proxy: bastion2 reached directly (no jump hosts) through a
// SOCKS5 or HTTP proxy, here a dynamic tunnel on bastion1.
func TestLabOutboundProxy(t *testing.T) {
	a, ev := labApp(t)
	port := freeTCP(t)
	b1 := labSession(t, a, "bastion1 proxy", labBastion1, "lab", labPass, func(s *store.Session) {
		s.Jump = nil
		s.Tunnels = []store.Tunnel{{Type: "dynamic", Port: port}}
	})
	b1tab := labConnect(t, a, ev, b1)
	proxied := func() int64 {
		sts, _ := a.TunnelStatuses(b1tab)
		for _, s := range sts {
			if s.Type == "dynamic" {
				return s.Total
			}
		}
		return 0
	}
	for _, typ := range []string{"socks5", "http"} {
		g, err := a.SaveNode(store.Node{Name: "proxied " + typ, IsGroup: true, Defaults: &store.GroupDefaults{Proxy: &store.Proxy{Type: typ, Host: "127.0.0.1", Port: port}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		n := labSession(t, a, "bastion2 via "+typ, "10.0.1.2", "lab", labPass, func(s *store.Session) { s.Jump = nil })
		n.ParentID = g.ID
		if n, err = a.SaveNode(*n, nil); err != nil {
			t.Fatal(err)
		}
		before := proxied()
		id := labConnect(t, a, ev, n)
		if proxied() <= before {
			t.Fatalf("%s: the connection did not go through the proxy", typ)
		}
		_ = a.Write(id, "hostname\r")
		labExpect(t, ev, id, "bastion2", 20*time.Second)
	}
}

// A device behind the jump hosts copies from YACM's HTTP server through a
// remote tunnel on the last jump host (bastion2, GatewayPorts on); behind
// the locked bastion YACM says why it cannot work.
func TestLabFileServerThroughJump(t *testing.T) {
	a, ev := labApp(t)
	dir := t.TempDir()
	local, sum := labFile(t, 2<<20)
	if err := os.Rename(local, filepath.Join(dir, "image.bin")); err != nil {
		t.Fatal(err)
	}
	st, err := a.FileServerStart(fileserver.Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: freeTCP(t), AllowUpload: true})
	if err != nil {
		t.Fatal(err)
	}
	n := labSession(t, a, "srv-web", "10.2.2.10", "lab", labPass, nil)
	id := labConnect(t, a, ev, n)
	ft, err := a.FileServerViaTunnel(id, "http")
	if err != nil {
		t.Fatal(err)
	}
	if !ft.Reachable || ft.Address != "10.0.1.2" && ft.Address != "10.0.2.1" {
		t.Logf("tunnel %+v", ft)
	}
	if !ft.Reachable {
		t.Fatalf("tunnel on bastion2 not reachable: %+v", ft)
	}
	// The device uses the jump host address it can reach (bastion2's side
	// towards the fabric).
	imgURL := fmt.Sprintf("http://10.0.2.1:%d/image.bin", ft.Port)
	got := labExec(t, "srv-web", "sh", "-c", "curl -s --max-time 20 "+imgURL+" | md5sum")
	if !strings.HasPrefix(got, sum) {
		t.Fatalf("download through the tunnel: %q, want %s (server port %d)", got, sum, st.Port)
	}
	if labFabric() == "ceos" {
		eos := labSession(t, a, "leaf3 copy", "10.255.0.23", "lab", labPass, nil)
		c, err := a.dialSession(context.Background(), eos, "")
		if err != nil {
			t.Fatal(err)
		}
		out, err := xfer.Exec(context.Background(), c.SSH(), "copy "+imgURL+" flash:image.bin", 2*time.Minute)
		c.Close()
		t.Logf("EOS copy http: %v\n%s", err, out)
		if err != nil {
			t.Fatalf("EOS copy through the tunnel: %v", err)
		}
	}

	// Behind bastion-locked (GatewayPorts no) the device cannot reach it.
	locked := labSession(t, a, "edge2 locked", "10.255.0.2", "lab", labPass, func(s *store.Session) {
		s.Jump = []store.JumpHost{{Host: labLocked, Port: 22, User: "lab", Auth: store.AuthPassword}}
	})
	lid := labConnect(t, a, ev, locked)
	ft, err = a.FileServerViaTunnel(lid, "http")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Reachable || !strings.Contains(ft.Note, "GatewayPorts") {
		t.Fatalf("locked bastion: %+v", ft)
	}
}

// TFTP to and from a device on the management network (bastion1 plays the
// device; with cEOS a leaf copies too).
func TestLabTFTPDirect(t *testing.T) {
	a, ev := labApp(t)
	dir := t.TempDir()
	local, sum := labFile(t, 3<<20)
	if err := os.Rename(local, filepath.Join(dir, "image.bin")); err != nil {
		t.Fatal(err)
	}
	port := 6969
	if _, err := a.FileServerStart(fileserver.Config{Kind: "tftp", Folder: dir, Port: port, AllowUpload: true}); err != nil {
		t.Fatal(err)
	}
	tftpURL := fmt.Sprintf("tftp://%s:%d/image.bin", labHostOnMgmt, port)
	got := labExec(t, "bastion1", "sh", "-c", "curl -s --max-time 30 "+tftpURL+" | md5sum")
	if !strings.HasPrefix(got, sum) {
		t.Fatalf("TFTP download from bastion1: %q want %s (a firewall on this computer, ufw say, blocking UDP %d from the lab?)", got, sum, port)
	}
	labExec(t, "bastion1", "sh", "-c", fmt.Sprintf("head -c 200000 /dev/urandom > /tmp/up.bin && curl -s --max-time 30 -T /tmp/up.bin tftp://%s:%d/from-bastion1.bin", labHostOnMgmt, port))
	if fi, err := os.Stat(filepath.Join(dir, "from-bastion1.bin")); err != nil || fi.Size() != 200000 {
		t.Fatalf("TFTP upload: %v %v", fi, err)
	}
	if labFabric() != "ceos" {
		return
	}
	// leaf4 reaches this computer over its Management0 (172.30.0.0/24).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := fmt.Sprintf("copy tftp://%s:%d/image.bin flash:tftp-image.bin", labHostOnMgmt, port)
	out, err := exec.CommandContext(ctx, "docker", "exec", "clab-yacm-leaf4", "timeout", "60", "Cli", "-p", "15", "-c", cmd).CombinedOutput()
	t.Logf("EOS %s: %v\n%s", cmd, err, out)
	if err == nil && !strings.Contains(string(out), "% Error") {
		return
	}
	// Where it got stuck: what the TFTP server saw from leaf4, plain curl from
	// leaf4, and curl again with a control-plane ACL that permits everything.
	// EOS's default control-plane ACL lets in replies only on tracked
	// connections; TFTP replies from a new port (the RFC 1350 TID) are not.
	time.Sleep(4 * time.Second) // the server gives up on a silent client
	ev.mu.Lock()
	var seen []string
	for _, e := range ev.fs {
		if e.Kind == "tftp" && e.Remote != labBastion1 && e.State != "progress" {
			seen = append(seen, fmt.Sprintf("  %s %s %s %d/%d %s", e.Remote, e.File, e.State, e.Done, e.Total, e.Error))
		}
	}
	ev.mu.Unlock()
	leaf4 := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		out, _ := exec.CommandContext(ctx, "docker", append([]string{"exec", "clab-yacm-leaf4"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	curl := func() string {
		return leaf4("bash", "-c", "curl -s -o /dev/null --max-time 15 -w '%{size_download} bytes, exit %{exitcode}' "+tftpURL)
	}
	before := curl()
	acl := leaf4("Cli", "-p", "15", "-c", "configure\nip access-list YACM-CP-ALL\npermit ip any any\nexit\nsystem control-plane\nip access-group YACM-CP-ALL in\nend")
	after := curl()
	t.Fatalf("EOS copy tftp failed.\nTFTP server events from leaf4:\n%s\ncurl in leaf4: %s\ncurl in leaf4 with control-plane ACL permit ip any any: %s %s",
		strings.Join(seen, "\n"), before, after, acl)
}

// A changed host key is refused, and the session does not connect.
func TestLabChangedHostKey(t *testing.T) {
	a, ev := labApp(t)
	n := labSession(t, a, "srv-kbd hostkey", "10.2.3.10", "lab", labPass, nil)
	id := labConnect(t, a, ev, n)
	a.CloseTab(id)
	labExec(t, "srv-kbd", "sh", "-c", "rm -f /etc/ssh/ssh_host_* && ssh-keygen -A >/dev/null && kill -HUP $(pgrep -o sshd)")
	time.Sleep(time.Second)
	ev.mu.Lock()
	ev.rejectChanged = true
	ev.mu.Unlock()
	id, err := a.Connect(ConnectRequest{NodeID: n.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if st := ev.state(id); st.State == "error" || st.State == "closed" {
			if !strings.Contains(st.Message, "host key") {
				t.Fatalf("changed key: %+v", st)
			}
			return
		}
		if ev.state(id).State == "connected" {
			t.Fatal("connected despite a changed host key")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no answer after the host key changed")
}
