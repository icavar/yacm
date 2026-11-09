// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package fileserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/icavar/yacm/internal/xfer"
)

type events struct {
	mu sync.Mutex
	ev []Event
}

func (e *events) add(ev Event) { e.mu.Lock(); e.ev = append(e.ev, ev); e.mu.Unlock() }
func (e *events) last(state string) *Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.ev) - 1; i >= 0; i-- {
		if e.ev[i].State == state {
			ev := e.ev[i]
			return &ev
		}
	}
	return nil
}

func data(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + i>>10)
	}
	return b
}

func newManager(t *testing.T) (*Manager, *events, string) {
	ev := &events{}
	m := NewManager(filepath.Join(t.TempDir(), "hostkey"), ev.add)
	t.Cleanup(m.StopAll)
	return m, ev, t.TempDir()
}

func TestTFTP(t *testing.T) {
	m, ev, dir := newManager(t)
	img := data(300000)
	os.WriteFile(filepath.Join(dir, "ios.bin"), img, 0o644)
	st, err := m.Start(Config{Kind: "tftp", Folder: dir, Bind: "127.0.0.1", Port: freeUDP(t), AllowUpload: true})
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(st.Port)
	for _, blk := range []int{0, 1468, 8192} {
		var buf bytes.Buffer
		n, err := TFTPGet(addr, "ios.bin", &buf, blk)
		if err != nil || n != int64(len(img)) || !bytes.Equal(buf.Bytes(), img) {
			t.Fatalf("get blksize %d: %d %v", blk, n, err)
		}
	}
	// Upload: a show tech from the device.
	up := data(123457)
	if _, err := TFTPPut(addr, "show_tech.txt", bytes.NewReader(up), int64(len(up)), 1024); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "show_tech.txt")); !bytes.Equal(got, up) {
		t.Fatal("upload differs")
	}
	var e *Event
	for i := 0; i < 100; i++ {
		if e = ev.last("done"); e != nil && e.Upload {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e == nil || !e.Upload || e.File != "show_tech.txt" || e.Done != int64(len(up)) {
		t.Fatalf("event %+v", e)
	}
	// Exactly a multiple of the block size ends with an empty block.
	exact := data(4096)
	if _, err := TFTPPut(addr, "exact.bin", bytes.NewReader(exact), 4096, 0); err != nil {
		t.Fatal(err)
	}
	var back bytes.Buffer
	if _, err := TFTPGet(addr, "exact.bin", &back, 0); err != nil || !bytes.Equal(back.Bytes(), exact) {
		t.Fatalf("exact: %v", err)
	}
	// No replacing, no escaping, missing files.
	if _, err := TFTPPut(addr, "show_tech.txt", bytes.NewReader(up), int64(len(up)), 0); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("overwrite: %v", err)
	}
	if _, err := TFTPGet(addr, "../../etc/passwd", io.Discard, 0); err == nil {
		t.Fatal("escaped the folder")
	}
	if _, err := TFTPGet(addr, "nope.bin", io.Discard, 0); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing: %v", err)
	}
	// Uploads off.
	st, err = m.Start(Config{Kind: "tftp", Folder: dir, Bind: "127.0.0.1", Port: st.Port})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TFTPPut(addr, "new.txt", strings.NewReader("x"), 1, 0); err == nil || !strings.Contains(err.Error(), "uploads are off") {
		t.Fatalf("uploads off: %v", err)
	}
}

func TestTFTPRollover(t *testing.T) {
	m, _, dir := newManager(t)
	big := data(70000*16 + 5) // more than 65535 blocks of 16 bytes
	os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644)
	st, err := m.Start(Config{Kind: "tftp", Folder: dir, Bind: "127.0.0.1", Port: freeUDP(t)})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := TFTPGet("127.0.0.1:"+strconv.Itoa(st.Port), "big.bin", &buf, 16); err != nil || !bytes.Equal(buf.Bytes(), big) {
		t.Fatalf("rollover: %v (%d bytes)", err, buf.Len())
	}
}

func freeUDP(t *testing.T) int {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func TestHTTP(t *testing.T) {
	m, ev, dir := newManager(t)
	img := data(200000)
	os.WriteFile(filepath.Join(dir, "ios.bin"), img, 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	st, err := m.Start(Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: 0, AllowUpload: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = st
	m.Stop("http")
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if _, err := m.Start(Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: port, AllowUpload: true}); err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	// Folder listing is off unless asked for.
	if resp, err := http.Get(base + "/"); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("listing without Listing: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	m.Stop("http")
	if _, err := m.Start(Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: port, AllowUpload: true, Listing: true}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(base + "/ios.bin")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, img) {
		t.Fatal("download differs")
	}
	resp, _ = http.Get(base + "/")
	list, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(list), "ios.bin") || !strings.Contains(string(list), "sub/") {
		t.Fatalf("listing: %s", list)
	}
	up := data(54321)
	req, _ := http.NewRequest(http.MethodPut, base+"/R1-confg", bytes.NewReader(up))
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("put: %v %v", resp.Status, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "R1-confg")); !bytes.Equal(b, up) {
		t.Fatal("upload differs")
	}
	time.Sleep(20 * time.Millisecond)
	if e := ev.last("done"); e == nil || !e.Upload || e.Done != int64(len(up)) {
		t.Fatalf("event %+v", e)
	}
	req, _ = http.NewRequest(http.MethodPut, base+"/R1-confg", bytes.NewReader(up))
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusConflict {
		t.Fatalf("overwrite: %s", resp.Status)
	}
	if resp, _ := http.Get(base + "/../../etc/passwd"); resp.StatusCode == http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(b), "root:") {
			t.Fatal("escaped the folder")
		}
	}
}

func TestSCPServer(t *testing.T) {
	m, ev, dir := newManager(t)
	img := data(400000)
	os.WriteFile(filepath.Join(dir, "ios.bin"), img, 0o644)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	st, err := m.Start(Config{Kind: "scp", Folder: dir, Bind: "127.0.0.1", Port: port, AllowUpload: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.User != "yacm" || len(st.Password) < 12 || !strings.HasPrefix(st.HostKey, "SHA256:") {
		t.Fatalf("status %+v", st)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	if _, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "yacm", Auth: []ssh.AuthMethod{ssh.Password("wrong")}, HostKeyCallback: ssh.InsecureIgnoreHostKey()}); err == nil {
		t.Fatal("wrong password accepted")
	}
	c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "yacm", Auth: []ssh.AuthMethod{ssh.Password(st.Password)}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	var buf bytes.Buffer
	if err := xfer.SCPGet(ctx, c, "ios.bin", &buf, nil); err != nil || !bytes.Equal(buf.Bytes(), img) {
		t.Fatalf("get: %v", err)
	}
	up := data(77777)
	if err := xfer.SCPPut(ctx, c, "/show_tech_R1.txt", bytes.NewReader(up), int64(len(up)), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "show_tech_R1.txt")); !bytes.Equal(b, up) {
		t.Fatal("upload differs")
	}
	if e := ev.last("done"); e == nil || !e.Upload || e.File != "show_tech_R1.txt" {
		t.Fatalf("event %+v", e)
	}
	if err := xfer.SCPPut(ctx, c, "show_tech_R1.txt", bytes.NewReader(up), int64(len(up)), nil); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("overwrite: %v", err)
	}
	if err := xfer.SCPPut(ctx, c, "../escape.txt", strings.NewReader("x"), 1, nil); err == nil {
		if _, serr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); serr == nil {
			t.Fatal("escaped the folder")
		}
	}
	// No shell, no other commands.
	if out, err := xfer.Exec(ctx, c, "id", 5*time.Second); err == nil && strings.Contains(out, "uid=") {
		t.Fatal("ran a command")
	}
	// The host key stays the same after a restart.
	st2, err := m.Start(Config{Kind: "scp", Folder: dir, Bind: "127.0.0.1", Port: port})
	if err != nil || st2.HostKey != st.HostKey {
		t.Fatalf("host key changed: %v", err)
	}
}

func TestStartErrors(t *testing.T) {
	m, _, dir := newManager(t)
	if _, err := m.Start(Config{Kind: "http", Folder: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing folder accepted")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := m.Start(Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: port}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("busy port: %v", err)
	}
	if len(LocalAddresses()) == 0 {
		t.Log("no non-loopback addresses on this machine")
	}
	if a := AddressToward("127.0.0.1"); a != "127.0.0.1" {
		t.Fatalf("toward loopback: %q", a)
	}
}

func TestAllowListAndIdleStop(t *testing.T) {
	m, _, dir := newManager(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if _, err := m.Start(Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: port, Allow: []string{"10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/a.txt")
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("not allowed address got in: %v %v", resp, err)
	}
	if _, err := m.Start(Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: port, Allow: []string{"127.0.0.1", "10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/a.txt"); resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed address refused: %s", resp.Status)
	}
	if _, err := m.Start(Config{Kind: "http", Folder: dir, Allow: []string{"nonsense"}}); err == nil {
		t.Fatal("bad allow entry accepted")
	}
	// Idle stop (with a short timer for the test).
	stopped := make(chan string, 1)
	m.Stopped = func(k string) { stopped <- k }
	if _, err := m.Start(Config{Kind: "http", Folder: dir, Bind: "127.0.0.1", Port: port, IdleStop: 1}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.timers["http"].Reset(50 * time.Millisecond)
	m.mu.Unlock()
	select {
	case k := <-stopped:
		if k != "http" || m.Statuses()[1].Running {
			t.Fatal("not stopped")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle stop did not happen")
	}
}

// Replies come from the port the server listens on (stateful filters such
// as Arista EOS's control-plane ACL drop replies from a new port), and
// transfers from several devices at once stay apart.
func TestTFTPOnePort(t *testing.T) {
	m, _, dir := newManager(t)
	img := data(200000)
	os.WriteFile(filepath.Join(dir, "img.bin"), img, 0o644)
	st, err := m.Start(Config{Kind: "tftp", Folder: dir, Bind: "127.0.0.1", Port: freeUDP(t), AllowUpload: true})
	if err != nil {
		t.Fatal(err)
	}
	// A bare RRQ: the first DATA block answers from the server's port.
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: st.Port}
	c.WriteToUDP(append([]byte{0, opRRQ}, "img.bin\x00octet\x00"...), srv)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1024)
	n, from, err := c.ReadFromUDP(buf)
	if err != nil || n != 516 || binary.BigEndian.Uint16(buf) != opDATA {
		t.Fatalf("first block: %d %v", n, err)
	}
	if from.Port != st.Port {
		t.Fatalf("reply from port %d, want the server's %d", from.Port, st.Port)
	}
	c.WriteToUDP([]byte{0, opERROR, 0, 0, 0}, srv) // stop that transfer

	addr := "127.0.0.1:" + strconv.Itoa(st.Port)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				var b bytes.Buffer
				if _, err := TFTPGet(addr, "img.bin", &b, 512*(i+1)); err != nil || !bytes.Equal(b.Bytes(), img) {
					errs <- fmt.Errorf("get %d: %v", i, err)
				}
				return
			}
			up := data(50000 + i)
			name := fmt.Sprintf("up%d.bin", i)
			if _, err := TFTPPut(addr, name, bytes.NewReader(up), int64(len(up)), 1024); err != nil {
				errs <- fmt.Errorf("put %d: %v", i, err)
				return
			}
			if got, _ := os.ReadFile(filepath.Join(dir, name)); !bytes.Equal(got, up) {
				errs <- fmt.Errorf("put %d: file differs", i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Requests beyond the limit of transfers at the same time are refused.
func TestTFTPTooManyTransfers(t *testing.T) {
	defer func(n int) { tftpMaxTransfers = n }(tftpMaxTransfers)
	tftpMaxTransfers = 1
	m, _, dir := newManager(t)
	os.WriteFile(filepath.Join(dir, "img.bin"), data(200000), 0o644)
	st, err := m.Start(Config{Kind: "tftp", Folder: dir, Bind: "127.0.0.1", Port: freeUDP(t)})
	if err != nil {
		t.Fatal(err)
	}
	srv := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: st.Port}
	ask := func() (*net.UDPConn, uint16) {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		c.WriteToUDP(append([]byte{0, opRRQ}, "img.bin\x00octet\x00"...), srv)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 1024)
		if _, _, err := c.ReadFromUDP(buf); err != nil {
			t.Fatal(err)
		}
		return c, binary.BigEndian.Uint16(buf)
	}
	first, op := ask()
	defer first.Close()
	if op != opDATA {
		t.Fatalf("first transfer: op %d", op)
	}
	second, op := ask()
	defer second.Close()
	if op != opERROR {
		t.Fatalf("second transfer: op %d, want an error", op)
	}
	first.WriteToUDP([]byte{0, opERROR, 0, 0, 0}, srv)
}

// Cisco IOS asks for a file and stops right away before the real copy:
// that is a check, not an error.
func TestTFTPCheckIsNotAnError(t *testing.T) {
	m, ev, dir := newManager(t)
	os.WriteFile(filepath.Join(dir, "img.bin"), data(5000), 0o644)
	st, err := m.Start(Config{Kind: "tftp", Folder: dir, Bind: "127.0.0.1", Port: freeUDP(t)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: st.Port}
	c.WriteToUDP(append([]byte{0, opRRQ}, "img.bin\x00octet\x00tsize\x000\x00"...), srv)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1024)
	if _, _, err := c.ReadFromUDP(buf); err != nil || binary.BigEndian.Uint16(buf) != opOACK {
		t.Fatalf("OACK: %v", err)
	}
	c.WriteToUDP(append([]byte{0, opERROR, 0, 0}, "Session terminated\x00"...), srv)
	var e *Event
	for i := 0; i < 100 && e == nil; i++ {
		e = ev.last("checked")
		time.Sleep(20 * time.Millisecond)
	}
	if e == nil || ev.last("error") != nil {
		t.Fatalf("checked %+v, error %+v", e, ev.last("error"))
	}
	// IOS XE reads several blocks (the image header) before it stops and
	// starts the real copy: still a check, never a red error.
	big := data(200000)
	os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644)
	c.WriteToUDP(append([]byte{0, opRRQ}, "big.bin\x00octet\x00"...), srv)
	for i := 0; i < 3; i++ { // ack three blocks, then stop
		n, _, err := c.ReadFromUDP(buf)
		if err != nil || n < 4 {
			t.Fatal(err)
		}
		c.WriteToUDP([]byte{0, opACK, buf[2], buf[3]}, srv)
	}
	c.ReadFromUDP(buf)
	c.WriteToUDP(append([]byte{0, opERROR, 0, 0}, "Session terminated\x00"...), srv)
	var mid *Event
	for i := 0; i < 100; i++ {
		if e := ev.last("checked"); e != nil && e.File == "big.bin" {
			mid = e
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mid == nil || mid.Done < 1024 || ev.last("error") != nil {
		t.Fatalf("a stop by the device after some blocks: %+v, error %+v", mid, ev.last("error"))
	}
}

// An old OpenSSH (5.3, CentOS 6 jump hosts) offers only ssh-rsa host keys,
// SHA-1 key exchanges, CTR ciphers and SHA-1 MACs.
func TestSCPServerOldOpenSSH(t *testing.T) {
	m, _, dir := newManager(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	st, err := m.Start(Config{Kind: "scp", Folder: dir, Bind: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	for _, kex := range []string{"diffie-hellman-group-exchange-sha256", "diffie-hellman-group14-sha1", "diffie-hellman-group1-sha1"} {
		cfg := &ssh.ClientConfig{User: "yacm", Auth: []ssh.AuthMethod{ssh.Password(st.Password)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			HostKeyAlgorithms: []string{"ssh-rsa", "ssh-dss"}}
		cfg.KeyExchanges = []string{kex}
		cfg.Ciphers = []string{"aes128-ctr"}
		cfg.MACs = []string{"hmac-sha1"}
		c, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), cfg)
		if err != nil {
			t.Errorf("%s: %v", kex, err)
			continue
		}
		c.Close()
	}
}
