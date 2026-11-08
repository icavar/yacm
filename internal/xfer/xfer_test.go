// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package xfer_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/icavar/yacm/internal/fakedevice"
	"github.com/icavar/yacm/internal/xfer"
)

// startDevice serves a pretend IOS device (exec + SCP, and SFTP when
// withSFTP) and returns a client connected to it.
func startDevice(t *testing.T, withSFTP bool) (*ssh.Client, *fakedevice.FS) {
	t.Helper()
	fs := &fakedevice.FS{Root: t.TempDir()}
	if err := fs.Seed("R1"); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	srv := &fakedevice.Server{Config: fakedevice.PasswordConfig("admin", "pw", signer), FS: fs, SFTP: withSFTP}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "admin", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, fs
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

func TestSCPDevice(t *testing.T) {
	c, fs := startDevice(t, false)
	ctx := context.Background()
	r, err := xfer.Open(xfer.NewLink(c, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind() != "scp" {
		t.Fatalf("kind %s, want scp", r.Kind())
	}
	if h := r.Home(ctx); h != "flash:" {
		t.Fatalf("home %q", h)
	}
	roots := r.Roots(ctx)
	if len(roots) == 0 || roots[0].Path != "flash:" || roots[0].Size != fakedevice.Total || roots[0].Free <= 0 {
		t.Fatalf("roots %+v", roots)
	}
	es, err := r.List(ctx, "flash:")
	if err != nil {
		t.Fatal(err)
	}
	if len(es) < 5 || !es[0].Dir || es[0].Name != "tracelogs" || es[0].Path != "flash:tracelogs" {
		t.Fatalf("entries %+v", es)
	}
	var img xfer.Entry
	for _, e := range es {
		if strings.HasSuffix(e.Name, ".bin") {
			img = e
		}
	}
	if img.Size != 3<<20 || img.Mod.IsZero() {
		t.Fatalf("image entry %+v", img)
	}
	sub, err := r.List(ctx, "flash:tracelogs")
	if err != nil || len(sub) != 1 || sub[0].Path != "flash:tracelogs/IOSRP_R0-0.log" {
		t.Fatalf("subfolder %+v %v", sub, err)
	}

	data := payload(1<<20 + 123)
	var last int64
	if err := r.Put(ctx, "flash:new image.bin", bytes.NewReader(data), int64(len(data)), func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if last != int64(len(data)) {
		t.Fatalf("progress ended at %d", last)
	}
	got, _ := os.ReadFile(filepath.Join(fs.Root, "new image.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("uploaded file differs")
	}
	var buf bytes.Buffer
	if err := r.Get(ctx, "flash:new image.bin", &buf, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatal("downloaded file differs")
	}
	sum := md5.Sum(data)
	if got, err := xfer.MD5(ctx, c, "flash:new image.bin"); err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("md5 %q %v", got, err)
	}
	if e, err := r.Stat(ctx, "flash:new image.bin"); err != nil || e.Size != int64(len(data)) {
		t.Fatalf("stat %+v %v", e, err)
	}
	if err := r.Remove(ctx, "flash:new image.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fs.Root, "new image.bin")); !os.IsNotExist(err) {
		t.Fatal("file not deleted")
	}
	if err := r.Get(ctx, "flash:missing.bin", io.Discard, nil); err == nil {
		t.Fatal("missing file downloaded")
	}
	if _, err := r.List(ctx, "flash:nope"); err == nil || !strings.Contains(err.Error(), "No such file") {
		t.Fatalf("missing folder: %v", err)
	}
	if err := r.Put(ctx, "flash:../escape.txt", strings.NewReader("x"), 1, nil); err == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(fs.Root), "escape.txt")); err == nil {
			t.Fatal("upload left the flash folder")
		}
	}
	if err := r.Mkdir(ctx, "flash:x"); err != xfer.ErrNotSupported {
		t.Fatalf("mkdir: %v", err)
	}
}

func TestSFTPDevice(t *testing.T) {
	c, fs := startDevice(t, true)
	ctx := context.Background()
	r, err := xfer.Open(xfer.NewLink(c, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Kind() != "sftp" {
		t.Fatalf("kind %s", r.Kind())
	}
	home := r.Home(ctx)
	// The SFTP server reports Windows folders as /C:/…
	if want := filepath.ToSlash(fs.Root); strings.TrimPrefix(home, "/") != strings.TrimPrefix(want, "/") {
		t.Fatalf("home %q, want %q", home, want)
	}
	es, err := r.List(ctx, home)
	if err != nil || len(es) < 5 || !es[0].Dir {
		t.Fatalf("list %+v %v", es, err)
	}
	data := payload(2<<20 + 7)
	dst := xfer.Join(home, "up.bin")
	if err := r.Put(ctx, dst, bytes.NewReader(data), int64(len(data)), nil); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := r.Get(ctx, dst, &buf, nil); err != nil || !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("round trip: %v", err)
	}
	if err := r.Mkdir(ctx, xfer.Join(home, "d")); err != nil {
		t.Fatal(err)
	}
	if err := r.Rename(ctx, dst, xfer.Join(home, "d/up2.bin")); err != nil {
		t.Fatal(err)
	}
	if e, err := r.Stat(ctx, xfer.Join(home, "d/up2.bin")); err != nil || e.Size != int64(len(data)) {
		t.Fatalf("stat %+v %v", e, err)
	}
	if err := r.Remove(ctx, xfer.Join(home, "d/up2.bin")); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(ctx, xfer.Join(home, "d")); err != nil {
		t.Fatal(err)
	}
}

func TestCancelTransfer(t *testing.T) {
	c, _ := startDevice(t, false)
	r := xfer.OpenSCP(xfer.NewLink(c, nil))
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	go func() {
		pw.Write(payload(64 << 10))
		cancel()
		time.Sleep(50 * time.Millisecond)
		pw.Write(payload(64 << 10))
		pw.Close()
	}()
	err := r.Put(ctx, "flash:big.bin", pr, 10<<20, nil)
	if err == nil {
		t.Fatal("cancelled upload succeeded")
	}
}

func TestPaths(t *testing.T) {
	for _, c := range [][3]string{
		{"flash:", "a.bin", "flash:a.bin"},
		{"flash:dir", "a.bin", "flash:dir/a.bin"},
		{"/home/u", "a", "/home/u/a"},
		{"/", "a", "/a"},
	} {
		if got := xfer.Join(c[0], c[1]); got != c[2] {
			t.Errorf("Join(%q,%q)=%q", c[0], c[1], got)
		}
	}
	for in, want := range map[string]string{"flash:dir/a": "flash:dir", "flash:a": "flash:", "flash:": "flash:", "/a/b": "/a", "/a": "/", "/": "/"} {
		if got := xfer.Parent(in); got != want {
			t.Errorf("Parent(%q)=%q want %q", in, got, want)
		}
	}
	root := t.TempDir()
	for p, ok := range map[string]bool{"a.txt": true, "/a.txt": true, "../a.txt": true, "flash:x/../../a": true, "sub/": true} {
		got, err := xfer.InRoot(root, p, "n.txt")
		if (err == nil) != ok {
			t.Errorf("InRoot(%q): %v", p, err)
		}
		if err == nil && !strings.HasPrefix(got, root) {
			t.Errorf("InRoot(%q) = %q left the root", p, got)
		}
	}
}

func TestParseDirFormats(t *testing.T) {
	nxos := "       4096    Oct 20 09:12:33 2026  scripts/\n  1234567    Oct 20 09:12:33 2026  nxos64-cs.10.3.4a.M.bin\n\nUsage for bootflash://sup-local\n 2000000 bytes used\n 5000000 bytes free\n 7000000 bytes total\n"
	es, free := xfer.ParseDir("bootflash:", nxos)
	if len(es) != 2 || !es[0].Dir || es[0].Name != "scripts" || es[1].Size != 1234567 || free != 5000000 {
		t.Fatalf("nx-os %+v %d", es, free)
	}
	ios := "Directory of flash:/\n\n    2  -rwx     33591768   Mar 1 1993 00:04:20 +00:00  c2960-lanbasek9-mz.150-2.SE11.bin\n    3  drwx          512   Mar 1 1993 00:05:00 +00:00  html\n    4  -rwx          736  <no date>  vlan.dat\n\n64016384 bytes total (20928512 bytes free)\n"
	es, free = xfer.ParseDir("flash:", ios)
	if len(es) != 3 || es[0].Name != "html" || es[1].Size != 33591768 || es[2].Name != "vlan.dat" || free != 20928512 {
		t.Fatalf("ios %+v %d", es, free)
	}
	xe := "Directory of bootflash:/\n\n   11  -rw-             1923  Oct 20 2026 09:12:33.000000000 +00:00  vlan.dat\n"
	es, _ = xfer.ParseDir("bootflash:", xe)
	if len(es) != 1 || es[0].Mod.Year() != 2026 {
		t.Fatalf("xe %+v", es)
	}
}

// Arista EOS shows Linux paths over SFTP, but its exec channel is the EOS
// CLI: md5sum is unknown there, verify /md5 file:<path> works.
func TestMD5AristaEOS(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	var tried []string
	srv := &fakedevice.Server{Config: fakedevice.PasswordConfig("admin", "pw", signer),
		Exec: func(rw io.ReadWriter, cmd string) (int, bool) {
			tried = append(tried, cmd)
			if cmd == "verify /md5 file:/home/lab/image.swi" {
				io.WriteString(rw, "verify /md5 (file:/home/lab/image.swi) = 1D897A9DDD0C2F2B766F2F956718A167\n")
				return 0, true
			}
			return 1, false
		}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "admin", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := xfer.MD5(context.Background(), c, "/home/lab/image.swi")
	if err != nil || got != "1d897a9ddd0c2f2b766f2f956718a167" {
		t.Fatalf("md5 %q %v (tried %q)", got, err, tried)
	}
}

// Cisco IOS without ip scp server enable answers "Administratively
// disabled."; YACM says what to configure.
func TestSCPDisabledSaysWhatToDo(t *testing.T) {
	fs := &fakedevice.FS{Root: t.TempDir()}
	if err := fs.Seed("R1"); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	srv := &fakedevice.Server{Config: fakedevice.PasswordConfig("admin", "pw", signer), FS: fs, SCPDisabled: true}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "admin", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := xfer.OpenSCP(xfer.NewLink(c, nil))
	var b bytes.Buffer
	err = r.Get(context.Background(), "flash:startup-config", &b, nil)
	if err == nil || !strings.Contains(err.Error(), "ip scp server enable") {
		t.Fatalf("get: %v", err)
	}
	err = r.Put(context.Background(), "flash:x.bin", strings.NewReader("x"), 1, nil)
	if err == nil || !strings.Contains(err.Error(), "ip scp server enable") {
		t.Fatalf("put: %v", err)
	}
}

// Huawei VRP lists sizes with thousands separators, folders with "-" and
// the free space in KB.
func TestParseDirHuaweiVRP(t *testing.T) {
	b, err := os.ReadFile("testdata/huawei_vrp_dir.txt")
	if err != nil {
		t.Fatal(err)
	}
	es, free := xfer.ParseDir("cfcard:", string(b))
	if len(es) != 30 || free != 488547*1024 {
		t.Fatalf("%d entries, free %d", len(es), free)
	}
	byName := map[string]xfer.Entry{}
	for _, e := range es {
		byName[e.Name] = e
	}
	if e := byName["NE20E-S2F_V800R022C00SPC600.cc"]; e.Size != 328071444 || e.Dir || e.Path != "cfcard:NE20E-S2F_V800R022C00SPC600.cc" {
		t.Fatalf("image %+v", e)
	}
	if e := byName["KPISTAT"]; !e.Dir || e.Mod.Year() != 2026 {
		t.Fatalf("folder %+v", e)
	}
	if e := byName["$_checkpoint"]; !e.Dir {
		t.Fatalf("system folder %+v", e)
	}
	if e := byName["backup_bkp_elb.txt"]; e.Size != 0 || e.Dir {
		t.Fatalf("empty file %+v", e)
	}
}

// A device that accepts SFTP and then says nothing does not hang the file
// browser: after the time limit it is SCP.
func TestSilentSFTPFallsBackToSCP(t *testing.T) {
	defer xfer.SetSFTPStart(300 * time.Millisecond)()
	fs := &fakedevice.FS{Root: t.TempDir()}
	if err := fs.Seed("R1"); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	srv := &fakedevice.Server{Config: fakedevice.PasswordConfig("admin", "pw", signer), FS: fs, SFTPSilent: true}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "admin", Auth: []ssh.AuthMethod{ssh.Password("pw")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	r, why := xfer.OpenWhy(xfer.NewLink(c, nil))
	if r.Kind() != "scp" || !strings.Contains(why, "no SFTP answer") || time.Since(start) > 3*time.Second {
		t.Fatalf("kind %s, why %q, took %v", r.Kind(), why, time.Since(start))
	}
	if es, err := r.List(context.Background(), "flash:"); err != nil || len(es) < 3 {
		t.Fatalf("list after the silent SFTP: %v %v", es, err)
	}
}

func TestDeviceNameAndSpaceAlias(t *testing.T) {
	if got := xfer.DeviceName("cbr copy\tv2.txt"); got != "cbr_copy_v2.txt" {
		t.Errorf("DeviceName = %q", got)
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "cbr copy.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "real_name.txt"), []byte("y"), 0o644)
	if got := xfer.SpaceAlias(filepath.Join(dir, "cbr_copy.txt")); got != filepath.Join(dir, "cbr copy.txt") {
		t.Errorf("alias = %q", got)
	}
	if got := xfer.SpaceAlias(filepath.Join(dir, "real_name.txt")); got != filepath.Join(dir, "real_name.txt") {
		t.Errorf("an existing file changed to %q", got)
	}
	// Two candidates: no guess.
	_ = os.WriteFile(filepath.Join(dir, "a b.txt"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a\tb.txt"), nil, 0o644)
	if got := xfer.SpaceAlias(filepath.Join(dir, "a_b.txt")); got != filepath.Join(dir, "a_b.txt") {
		t.Errorf("ambiguous alias = %q", got)
	}
}
