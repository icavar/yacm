// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/icavar/yacm/internal/exchange"
	"github.com/icavar/yacm/internal/store"
)

// The demo file that ships with YACM imports and contains the 0.7 examples.
func TestDemoFileImports(t *testing.T) {
	if testing.Short() {
		t.Skip("builds tools/mkdemo")
	}
	path := filepath.Join(t.TempDir(), "demo.yacm")
	cmd := exec.Command("go", "run", "../tools/mkdemo", path)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	rep, err := a.ImportSessions(path, exchange.ImportOptions{Password: "demo-export-pw"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions < 35 {
		t.Fatalf("report %+v", rep)
	}
	var tel *store.Node
	for _, n := range a.store.List() {
		if n.Name == "old-router via bastion (telnet)" {
			tel = n
		}
	}
	if tel == nil || tel.Session.JumpVia == "" {
		t.Fatalf("the telnet session through the bastion is missing or lost its jump host: %+v", tel)
	}
	e, err := a.store.Effective(tel)
	if err != nil || len(e.Hops) != 1 {
		t.Fatalf("hops %+v %v", e, err)
	}
}

// The lab's sessions file imports, with the jump chain and switched-off tunnels.
func TestLabFileImports(t *testing.T) {
	if testing.Short() {
		t.Skip("builds tools/mklab")
	}
	path := filepath.Join(t.TempDir(), "testlab.yacm")
	cmd := exec.Command("go", "run", "../tools/mklab", path)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	rep, err := a.ImportSessions(path, exchange.ImportOptions{Password: "lab-export-pw"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions < 18 {
		t.Fatalf("report %+v", rep)
	}
	byName := map[string]*store.Node{}
	for _, n := range a.store.List() {
		byName[n.Name] = n
	}
	leaf := byName["leaf1"]
	if leaf == nil {
		t.Fatal("leaf1 missing")
	}
	e, err := a.store.Effective(leaf)
	if err != nil || len(e.Hops) != 2 || e.Hops[0].Host != "172.30.0.11" || e.Hops[1].Host != "10.0.1.2" || e.User != "lab" {
		t.Fatalf("leaf1 effective %+v %v", e, err)
	}
	sftp := byName["srv-sftp (SFTP; tunnels to srv-web)"]
	if sftp == nil || len(sftp.Session.Tunnels) != 2 || !sftp.Session.Tunnels[0].Off {
		t.Fatalf("tunnels from a file must arrive switched off: %+v", sftp)
	}
	px := byName["bastion2 directly, through the proxy"]
	if e, err := a.store.Effective(px); err != nil || e.Proxy == nil || e.Proxy.Port != 11080 || len(e.Hops) != 0 {
		t.Fatalf("proxy session %+v %v", e, err)
	}
}

// The welcome screen finds demo.yacm from the separate demo download.
func TestDemoFileNear(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "yacm-0.9.0-windows-x64")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := demoFileNear(app); got != "" {
		t.Fatalf("no demo: got %q", got)
	}
	write := func(p string) string {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Both downloads unpacked next to each other.
	sibling := write(filepath.Join(root, "yacm-0.9.0-demo-windows", "demo.yacm"))
	if got := demoFileNear(app); got != sibling {
		t.Fatalf("sibling demo: got %q, want %q", got, sibling)
	}
	// The newer name (0.9.1) comes after the older one.
	newer := write(filepath.Join(root, "yacm-demo-device-0.9.1-windows", "demo.yacm"))
	if got := demoFileNear(app); got != newer {
		t.Fatalf("newer demo: got %q, want %q", got, newer)
	}
	// The demo unpacked into YACM's own folder comes first.
	inside := write(filepath.Join(app, "yacm-demo-device-0.9.1-windows", "demo.yacm"))
	if got := demoFileNear(app); got != inside {
		t.Fatalf("demo inside: got %q, want %q", got, inside)
	}
	// A demo folder next to the executable comes before everything else.
	own := write(filepath.Join(app, "demo", "demo.yacm"))
	if got := demoFileNear(app); got != own {
		t.Fatalf("demo folder: got %q, want %q", got, own)
	}
}
