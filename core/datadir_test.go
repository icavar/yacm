// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yacm-app/yacm/internal/store"
)

func setConfigHome(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", base)
	t.Setenv("APPDATA", base)
	t.Setenv("YACM_DATA_DIR", "")
	t.Setenv("MOST_DATA_DIR", "")
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(cfg, appName)
}

func TestResolveDataDirPointer(t *testing.T) {
	def := setConfigHome(t)
	loc, err := resolveDataDir()
	if err != nil || loc.Dir != def || loc.Source != "default" {
		t.Fatalf("default: %+v %v", loc, err)
	}
	chosen := filepath.Join(t.TempDir(), "YACM data")
	os.MkdirAll(chosen, 0o700)
	if err := writePointer(def, chosen); err != nil {
		t.Fatal(err)
	}
	if loc, _ := resolveDataDir(); loc.Dir != chosen || loc.Source != "chosen" || loc.Default != def {
		t.Fatalf("chosen: %+v", loc)
	}
	// A chosen folder that is gone (unplugged disk) is reported, not created.
	os.RemoveAll(chosen)
	loc, _ = resolveDataDir()
	if loc.Dir != "" || loc.Wanted != chosen {
		t.Fatalf("unavailable: %+v", loc)
	}
	if _, err := os.Stat(chosen); err == nil {
		t.Fatal("missing folder was created")
	}
	a, err := newAppFor(loc, false)
	if err != nil {
		t.Fatal(err)
	}
	st := a.DataFolderStatus()
	if !st.Unavailable || !st.ReadOnly || st.Wanted != chosen {
		t.Fatalf("status %+v", st)
	}
	if _, err := a.store.Upsert(&store.Node{Name: "x", IsGroup: true}); err != store.ErrReadOnly {
		t.Fatalf("write in unavailable mode: %v", err)
	}
	if err := writePointer(def, def); err != nil {
		t.Fatal(err)
	}
	if loc, _ := resolveDataDir(); loc.Dir != def || loc.Source != "default" {
		t.Fatalf("back to default: %+v", loc)
	}
	t.Setenv("YACM_DATA_DIR", chosen)
	if loc, _ := resolveDataDir(); loc.Dir != chosen || loc.Source != "env" {
		t.Fatalf("env: %+v", loc)
	}
}

func TestDataFolderLock(t *testing.T) {
	dir := t.TempDir()
	l, other, err := acquireLock(dir, false)
	if err != nil || other != nil || l == nil {
		t.Fatalf("first: %v %v", other, err)
	}
	// Our own lock from this process is not "someone else".
	if li, _ := readLock(filepath.Join(dir, lockFile)); lockOwnedElsewhere(li, l.info.Host) {
		t.Fatal("own lock seen as foreign")
	}
	l.release()
	if _, err := os.Stat(filepath.Join(dir, lockFile)); err == nil {
		t.Fatal("lock file left behind")
	}

	// Another computer, seen a moment ago: read-only.
	write := func(li LockInfo) {
		l := &dirLock{path: filepath.Join(dir, lockFile), info: li}
		if err := l.write(); err != nil {
			t.Fatal(err)
		}
	}
	write(LockInfo{Host: "OTHER-PC", User: "kolega", PID: 4242, Started: time.Now(), Seen: time.Now()})
	a, err := newAppFor(dataLocation{Dir: dir, Default: dir, Source: "default"}, false)
	if err != nil {
		t.Fatal(err)
	}
	st := a.DataFolderStatus()
	if !st.ReadOnly || st.LockedBy == nil || st.LockedBy.Host != "OTHER-PC" {
		t.Fatalf("locked: %+v", st)
	}
	if err := a.vault.Create("some-master-pw"); err == nil {
		t.Fatal("vault created in a read-only window")
	}
	if err := a.SaveSettings(a.store.Settings()); err != store.ErrReadOnly {
		t.Fatalf("settings saved: %v", err)
	}

	// Stale (computer asleep or crashed): taken over.
	write(LockInfo{Host: "OTHER-PC", PID: 4242, Started: time.Now().Add(-time.Hour), Seen: time.Now().Add(-10 * time.Minute)})
	b, _ := newAppFor(dataLocation{Dir: dir, Default: dir, Source: "default"}, false)
	if b.DataFolderStatus().ReadOnly || b.lock == nil {
		t.Fatal("stale lock not taken over")
	}
	b.lock.release()

	// Same computer, process gone: taken over.
	write(LockInfo{Host: l.info.Host, PID: 999999, Started: time.Now(), Seen: time.Now()})
	c, _ := newAppFor(dataLocation{Dir: dir, Default: dir, Source: "default"}, false)
	if c.DataFolderStatus().ReadOnly {
		t.Fatal("lock of a dead process kept the folder read-only")
	}
	c.lock.release()

	// "Open anyway".
	write(LockInfo{Host: "OTHER-PC", PID: 4242, Started: time.Now(), Seen: time.Now()})
	d, _ := newAppFor(dataLocation{Dir: dir, Default: dir, Source: "default"}, true)
	if d.DataFolderStatus().ReadOnly {
		t.Fatal("take-over still read-only")
	}
	d.lock.release()
}

func TestChangeDataFolderMove(t *testing.T) {
	def := setConfigHome(t)
	os.MkdirAll(def, 0o700)
	a, err := newAppFor(dataLocation{Dir: def, Default: def, Source: "default"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.lock.release()
	restarts := 0
	a.restart = func(...string) error { restarts++; return nil }
	if err := a.vault.Create("move-test-pw"); err != nil {
		t.Fatal(err)
	}
	a.vault.Set("node/x/password", "secret")
	n, _ := a.store.Upsert(&store.Node{Name: "core1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1"}})
	os.MkdirAll(filepath.Join(def, "logs"), 0o700)
	os.WriteFile(filepath.Join(def, "logs", "core1.log"), []byte("R1#show ver\n"), 0o600)
	os.WriteFile(filepath.Join(def, "known_hosts"), []byte("[10.0.0.1]:22 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAYAstgRpMeBeN4y48HoPtlb4GMc84nKugeToFacv979\n"), 0o600)

	target := filepath.Join(t.TempDir(), "OneDrive - Firma", "YACM")
	info, err := a.InspectDataFolder(target)
	if err != nil || info.Exists || !info.Writable || info.Cloud != "OneDrive" {
		t.Fatalf("inspect: %+v %v", info, err)
	}
	if err := a.ChangeDataFolder(target, "move"); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || readPointer(def) != target {
		t.Fatalf("restart %d, pointer %q", restarts, readPointer(def))
	}
	for _, f := range []string{"sessions.json", "vault.json", "known_hosts", filepath.Join("logs", "core1.log")} {
		if _, err := os.Stat(filepath.Join(target, f)); err != nil {
			t.Errorf("%s not moved: %v", f, err)
		}
		if _, err := os.Stat(filepath.Join(def, f)); err == nil {
			t.Errorf("%s left in the old folder", f)
		}
	}
	if _, err := os.Stat(filepath.Join(def, "DATA-MOVED.txt")); err != nil {
		t.Error("no note in the old folder")
	}
	// The moved data opens with the same master password.
	b, err := NewApp(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.vault.Unlock("move-test-pw"); err != nil || b.secret("node/x/password") != "secret" {
		t.Fatalf("vault after move: %v", err)
	}
	if _, err := b.store.Get(n.ID); err != nil {
		t.Fatal("session lost")
	}

	// Moving onto existing data needs "replace", which keeps a backup.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "sessions.json"), []byte(`{"nodes":[{"isGroup":false},{"isGroup":true}]}`), 0o600)
	b.loc = dataLocation{Dir: target, Default: def, Source: "chosen"}
	b.restart = func(...string) error { return nil }
	if i, _ := b.InspectDataFolder(other); !i.HasData || i.Sessions != 1 {
		t.Fatalf("inspect existing: %+v", i)
	}
	if err := b.ChangeDataFolder(other, "move"); err == nil || !strings.Contains(err.Error(), "already has YACM data") {
		t.Fatalf("move onto data: %v", err)
	}
	if err := b.ChangeDataFolder(other, "replace"); err != nil {
		t.Fatal(err)
	}
	baks, _ := filepath.Glob(filepath.Join(other, "backup-*", "sessions.json"))
	if len(baks) != 1 {
		t.Fatal("existing data not backed up")
	}
	// Back to the default folder: the pointer goes away.
	b.loc = dataLocation{Dir: other, Default: def, Source: "chosen"}
	if err := b.ResetDataFolder("move"); err != nil {
		t.Fatal(err)
	}
	if readPointer(def) != "" {
		t.Fatal("pointer kept after going back to the default folder")
	}
	if _, err := os.Stat(filepath.Join(def, "vault.json")); err != nil {
		t.Fatal("vault not back in the default folder")
	}
}
