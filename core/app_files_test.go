// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/yacm-app/yacm/internal/fakedevice"
	"github.com/yacm-app/yacm/internal/sshconn"
	"github.com/yacm-app/yacm/internal/store"
)

// startFakeDevice runs a pretend device (exec, SCP, jump forwarding).
func startFakeDevice(t *testing.T, host string) (int, *fakedevice.FS) {
	t.Helper()
	return startFakeDeviceWith(t, host, nil)
}

// startFakeDeviceWith lets a test change the server before it starts.
func startFakeDeviceWith(t *testing.T, host string, change func(*fakedevice.Server)) (int, *fakedevice.FS) {
	t.Helper()
	fs := &fakedevice.FS{Root: t.TempDir()}
	if err := fs.Seed(host); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	srv := &fakedevice.Server{Config: fakedevice.PasswordConfig("admin", "pw", signer), FS: fs, RemoteForward: true,
		Exec: func(rw io.ReadWriter, cmd string) (int, bool) {
			if strings.HasPrefix(cmd, "show tech") {
				for i := 0; i < 2000; i++ {
					rw.Write([]byte("GigabitEthernet1/0/1 is up, line protocol is up\r\n"))
				}
				return 0, true
			}
			return 0, false
		}}
	if change != nil {
		change(srv)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return ln.Addr().(*net.TCPAddr).Port, fs
}

// autoAccept answers host key prompts and records transfer events.
type xferEvents struct {
	mu sync.Mutex
	ev map[string]TransferEvent
}

func (x *xferEvents) wait(t *testing.T, id string) TransferEvent {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		x.mu.Lock()
		ev := x.ev[id]
		x.mu.Unlock()
		if ev.State != "" && ev.State != "running" && ev.State != "verifying" {
			return ev
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("transfer %s did not finish", id)
	return TransferEvent{}
}

func fileTestApp(t *testing.T) (*App, *xferEvents) {
	a := newTestApp(t)
	x := &xferEvents{ev: map[string]TransferEvent{}}
	a.emitter = func(name string, data any) {
		switch name {
		case "prompt":
			p := data.(map[string]any)
			go a.AnswerPrompt(p["id"].(string), PromptAnswer{Accept: true})
		case "xfer:progress":
			ev := data.(TransferEvent)
			x.mu.Lock()
			x.ev[ev.ID] = ev
			x.mu.Unlock()
		}
	}
	return a, x
}

func TestFilesThroughJumpHost(t *testing.T) {
	a, x := fileTestApp(t)
	jumpPort, _ := startFakeDevice(t, "bastion")
	devPort, fs := startFakeDevice(t, "R9")
	n, err := a.SaveNode(store.Node{Name: "R9", Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: devPort, User: "admin", Auth: store.AuthPassword,
		Jump: []store.JumpHost{{Host: "127.0.0.1", Port: jumpPort, User: "admin", Auth: store.AuthPassword}}}},
		&SecretInput{Password: sp("pw"), Jump: map[string]*string{"0": sp("pw")}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := a.FilesOpen(FilesOpenRequest{NodeID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	defer a.FilesClose(info.ID)
	if info.Kind != "scp" || info.Home != "flash:" || info.Shared || len(info.Roots) == 0 {
		t.Fatalf("info %+v", info)
	}
	es, err := a.FilesList(info.ID, "flash:")
	if err != nil || len(es) < 5 {
		t.Fatalf("list %v %v", es, err)
	}

	// Upload with MD5 verification.
	local := filepath.Join(t.TempDir(), "ios-image.bin")
	data := bytes.Repeat([]byte("yacm-0.8.0 "), 200000)
	if err := os.WriteFile(local, data, 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := a.FilesUpload(info.ID, local, "flash:ios-image.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	ev := x.wait(t, id)
	if ev.State != "done" || ev.Verified != "ok" || ev.Done != int64(len(data)) {
		t.Fatalf("upload %+v", ev)
	}
	if got, _ := os.ReadFile(filepath.Join(fs.Root, "ios-image.bin")); !bytes.Equal(got, data) {
		t.Fatal("device file differs")
	}

	// A name with a space would be cut by the device: refused with a hint.
	if _, err := a.FilesUpload(info.ID, local, "flash:ios image.bin", false); err == nil || !strings.Contains(err.Error(), "send it as ios_image.bin") {
		t.Fatalf("space in a device name: %v", err)
	}
	// Without MD5 the upload still checks the file is there with its size.
	id, err = a.FilesUpload(info.ID, local, "flash:copy2.bin", false)
	if err != nil {
		t.Fatal(err)
	}
	if ev := x.wait(t, id); ev.State != "done" {
		t.Fatalf("upload without MD5 %+v", ev)
	}

	// Download with the original name into a chosen folder.
	dst := filepath.Join(t.TempDir(), "ios-image.bin")
	id, err = a.FilesDownload(info.ID, "flash:ios-image.bin", dst, int64(len(data)), true)
	if err != nil {
		t.Fatal(err)
	}
	if ev := x.wait(t, id); ev.State != "done" || ev.Verified != "ok" {
		t.Fatalf("download %+v", ev)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("downloaded file differs")
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Fatal("partial file left behind")
	}

	// Get show tech: the output of an exec command into a file.
	tech := filepath.Join(t.TempDir(), "show_tech_R9.txt")
	id, err = a.FilesCommandToFile(info.ID, "show tech-support", tech)
	if err != nil {
		t.Fatal(err)
	}
	if ev := x.wait(t, id); ev.State != "done" || ev.Done < 50000 {
		t.Fatalf("show tech %+v", ev)
	}
	if b, _ := os.ReadFile(tech); !strings.Contains(string(b), "line protocol is up") {
		t.Fatal("show tech output missing")
	}

	// A command the device refuses is an error, not a file.
	id, _ = a.FilesCommandToFile(info.ID, "show nonsense", filepath.Join(t.TempDir(), "x.txt"))
	if ev := x.wait(t, id); ev.State != "error" || !strings.Contains(ev.Error, "Invalid input") {
		t.Fatalf("refused command %+v", ev)
	}

	// Delete a file.
	if err := a.FilesRemove(info.ID, "flash:ios-image.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fs.Root, "ios-image.bin")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}

	// A missing file fails cleanly.
	id, _ = a.FilesDownload(info.ID, "flash:nope.bin", filepath.Join(t.TempDir(), "nope.bin"), 0, false)
	if ev := x.wait(t, id); ev.State != "error" {
		t.Fatalf("missing file %+v", ev)
	}
}

func TestFilesRefusesTelnet(t *testing.T) {
	a, _ := fileTestApp(t)
	n, err := a.SaveNode(store.Node{Name: "old", Session: &store.Session{Protocol: store.ProtoTelnet, Host: "127.0.0.1", Port: 23}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.FilesOpen(FilesOpenRequest{NodeID: n.ID}); err == nil || !strings.Contains(err.Error(), "need SSH") {
		t.Fatalf("telnet: %v", err)
	}
}

// Cisco IOS and IOS XE take one session channel per SSH connection and no
// SFTP: the file browser must not lose its connection to the SFTP attempt,
// and Send/Get file from an open session must not need a second channel on
// the terminal's connection.
func TestFilesOneSessionPerConnection(t *testing.T) {
	a, x := fileTestApp(t)
	devPort, fs := startFakeDeviceWith(t, "C9200L", func(s *fakedevice.Server) {
		s.MaxSessions = 1
		s.CloseAfterSession = true
		s.Shell = func(rw io.ReadWriter) { io.WriteString(rw, "C9200L#"); io.Copy(io.Discard, rw) }
	})
	jumpPort, _ := startFakeDevice(t, "bastion")
	n, err := a.SaveNode(store.Node{Name: "C9200L", Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: devPort, User: "admin", Auth: store.AuthPassword,
		Jump: []store.JumpHost{{Host: "127.0.0.1", Port: jumpPort, User: "admin", Auth: store.AuthPassword}}}},
		&SecretInput{Password: sp("pw"), Jump: map[string]*string{"0": sp("pw")}})
	if err != nil {
		t.Fatal(err)
	}

	// The file browser from the tree: SFTP refused, then SCP.
	info, err := a.FilesOpen(FilesOpenRequest{NodeID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "scp" || info.Home != "flash:" {
		t.Fatalf("info %+v", info)
	}
	// Every command after the first needs a new connection to the device.
	for i := 0; i < 3; i++ {
		if es, err := a.FilesList(info.ID, "flash:"); err != nil || len(es) < 5 {
			t.Fatalf("list %d after the SFTP attempt: %v %v", i, es, err)
		}
	}
	if sum, err := a.FilesMD5(info.ID, "flash:startup-config"); err != nil || len(sum) != 32 {
		t.Fatalf("md5 %q %v", sum, err)
	}
	a.FilesClose(info.ID)

	// The same from an open terminal tab: its connection has no channel to
	// spare, so the files get a connection of their own (and the SFTP
	// attempt on that one a fresh connection again).
	a.filesMu.Lock()
	a.scpOnly = nil
	a.filesMu.Unlock()
	id, err := a.Connect(ConnectRequest{NodeID: n.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, a, id, "connected")
	for _, scp := range []bool{false, true} {
		info, err := a.FilesOpen(FilesOpenRequest{TabID: id, OwnerID: id, SCP: scp})
		if err != nil {
			t.Fatalf("scp=%v: %v", scp, err)
		}
		if info.Shared || info.Summary != "own connection: the device allows one channel per SSH connection" {
			t.Fatalf("scp=%v: info %+v", scp, info)
		}
		local := filepath.Join(t.TempDir(), "cfg.txt")
		os.WriteFile(local, []byte("hostname C9200L\n"), 0o644)
		up, err := a.FilesUpload(info.ID, local, "flash:cfg.txt", true)
		if err != nil {
			t.Fatal(err)
		}
		if ev := x.wait(t, up); ev.State != "done" || ev.Verified != "ok" {
			t.Fatalf("scp=%v: upload %+v", scp, ev)
		}
		if _, err := os.Stat(filepath.Join(fs.Root, "cfg.txt")); err != nil {
			t.Fatal(err)
		}
		a.FilesClose(info.ID)
	}
	tb, _ := a.getTab(id)
	tb.mu.Lock()
	st := tb.state
	tb.mu.Unlock()
	if st != "connected" {
		t.Fatalf("terminal tab after the file transfers: %s", st)
	}
}

// A session without a username: YACM asks for one before the login
// (instead of sending an empty one) and saves it with the session.
func TestConnectAsksForMissingUsername(t *testing.T) {
	a := newTestApp(t)
	var mu sync.Mutex
	var asked []string
	a.emitter = func(name string, data any) {
		if name != "prompt" {
			return
		}
		p := data.(map[string]any)
		id := p["id"].(string)
		if p["kind"] == "secret" {
			req := p["secret"].(sshconn.SecretRequest)
			mu.Lock()
			asked = append(asked, req.Kind)
			mu.Unlock()
			if req.Kind == "username" {
				go a.AnswerPrompt(id, PromptAnswer{Accept: true, Value: "admin", Save: req.CanSave})
				return
			}
		}
		go a.AnswerPrompt(id, PromptAnswer{Accept: true})
	}
	devPort := shellDevice(t, false)
	n, err := a.SaveNode(store.Node{Name: "no-user", Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: devPort, Auth: store.AuthPassword}},
		&SecretInput{Password: sp("pw")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.Connect(ConnectRequest{NodeID: n.ID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, a, id, "connected")
	got, _ := a.store.Get(n.ID)
	mu.Lock()
	defer mu.Unlock()
	if len(asked) == 0 || asked[0] != "username" || got.Session.User != "admin" {
		t.Fatalf("asked %v, saved user %q", asked, got.Session.User)
	}
}

// Several transfers at once to a device that takes one channel per
// connection: they start in parallel, the device refuses, and the rest
// wait their turn ("queued") instead of failing. A device that takes
// several channels keeps them in parallel.
func TestFilesManyAtOnceOneChannelDevice(t *testing.T) {
	for _, oneChannel := range []bool{true, false} {
		a := newTestApp(t)
		var mu sync.Mutex
		states := map[string][]string{}
		final := map[string]TransferEvent{}
		a.emitter = func(name string, data any) {
			switch name {
			case "prompt":
				go a.AnswerPrompt(data.(map[string]any)["id"].(string), PromptAnswer{Accept: true})
			case "xfer:progress":
				ev := data.(TransferEvent)
				mu.Lock()
				if s := states[ev.ID]; len(s) == 0 || s[len(s)-1] != ev.State {
					states[ev.ID] = append(s, ev.State)
				}
				final[ev.ID] = ev
				mu.Unlock()
			}
		}
		devPort, fs := startFakeDeviceWith(t, "C9300", func(s *fakedevice.Server) {
			if oneChannel {
				s.MaxSessions, s.CloseAfterSession = 1, true
			}
			// Each copy holds its channel a while, so the three overlap
			// however fast this machine is.
			s.ExecDelay = 150 * time.Millisecond
		})
		n, err := a.SaveNode(store.Node{Name: "C9300", Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: devPort, User: "admin", Auth: store.AuthPassword}},
			&SecretInput{Password: sp("pw")})
		if err != nil {
			t.Fatal(err)
		}
		info, err := a.FilesOpen(FilesOpenRequest{NodeID: n.ID, SCP: true})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for i := 0; i < 3; i++ {
			local := filepath.Join(t.TempDir(), fmt.Sprintf("f%d.bin", i))
			os.WriteFile(local, bytes.Repeat([]byte{byte(i)}, 4<<20), 0o644)
			id, err := a.FilesUpload(info.ID, local, fmt.Sprintf("flash:f%d.bin", i), true)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			mu.Lock()
			done := 0
			for _, id := range ids {
				if s := final[id].State; s == "done" || s == "error" || s == "cancelled" {
					done++
				}
			}
			mu.Unlock()
			if done == len(ids) || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		mu.Lock()
		queued := 0
		for _, id := range ids {
			if ev := final[id]; ev.State != "done" || ev.Verified != "ok" {
				t.Fatalf("oneChannel=%v: %s %+v (states %v)", oneChannel, id, ev, states[id])
			}
			for _, s := range states[id] {
				if s == "queued" {
					queued++
				}
			}
		}
		mu.Unlock()
		if oneChannel && queued == 0 {
			t.Fatal("one channel: no transfer waited")
		}
		if !oneChannel && queued != 0 {
			t.Fatal("several channels: transfers should run in parallel")
		}
		for i := 0; i < 3; i++ {
			if fi, err := os.Stat(filepath.Join(fs.Root, fmt.Sprintf("f%d.bin", i))); err != nil || fi.Size() != 4<<20 {
				t.Fatalf("f%d: %v %v", i, fi, err)
			}
		}
		a.FilesClose(info.ID)
	}
}

// A file name from a device cannot make a download land outside the chosen
// folder.
func TestLocalJoinRefusesPaths(t *testing.T) {
	a := &App{}
	dir := t.TempDir()
	for _, bad := range []string{"../x", "..", ".", "", "a/b", `..\..\Startup\x.bat`, "/etc/passwd", "x\x00y"} {
		if p, err := a.LocalJoin(dir, bad); err == nil {
			t.Errorf("LocalJoin(%q) = %q", bad, p)
		}
	}
	if p, err := a.LocalJoin(dir, "show_tech.txt"); err != nil || p != filepath.Join(dir, "show_tech.txt") {
		t.Fatalf("plain name: %q %v", p, err)
	}
}

func TestWindowsDeviceNames(t *testing.T) {
	for _, n := range []string{"COM1", "con", "NUL.txt", "LPT3", "conin$"} {
		if !windowsDevice.MatchString(n) {
			t.Errorf("%q not seen as a device", n)
		}
	}
	for _, n := range []string{"COM1x", "console.log", "nulls.txt", "show_tech.txt"} {
		if windowsDevice.MatchString(n) {
			t.Errorf("%q seen as a device", n)
		}
	}
}
