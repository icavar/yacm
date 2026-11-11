// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package vault

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVaultRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vault.json")
	v := New(p)
	if err := v.Create("short"); err != ErrWeakPassword {
		t.Fatalf("weak: %v", err)
	}
	if err := v.Create("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("node/a/password", "Šifra!"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "Šifra") || strings.Contains(string(raw), "node/a") {
		t.Fatal("plaintext leaked into vault file")
	}
	// Windows has no Unix permission bits (the data folder in the user
	// profile is protected by its ACL), so the mode is only checked elsewhere.
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", fi.Mode().Perm())
	}
	v.Lock()
	if _, err := v.Get("node/a/password"); err != ErrLocked {
		t.Fatal("locked vault returned data")
	}
	v2 := New(p)
	if err := v2.Unlock("wrong password"); err != ErrWrongPassword {
		t.Fatalf("wrong pw: %v", err)
	}
	if err := v2.Unlock("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if got, _ := v2.Get("node/a/password"); got != "Šifra!" {
		t.Fatalf("got %q", got)
	}
	if err := v2.ChangePassword("correct horse battery", "new master password"); err != nil {
		t.Fatal(err)
	}
	v3 := New(p)
	if err := v3.Unlock("new master password"); err != nil {
		t.Fatal(err)
	}
	if err := v3.CopyPrefix("node/a/", "node/b/", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := v3.Get("node/b/password"); got != "Šifra!" {
		t.Fatal("copy prefix")
	}
	if err := v3.DeletePrefix("node/a/"); err != nil || v3.Has("node/a/password") {
		t.Fatal("delete prefix")
	}
}

func TestTamperedHeaderRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vault.json")
	v := New(p)
	if err := v.Create("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	// Downgrade the KDF time cost; AAD must catch it.
	tampered := strings.Replace(string(raw), `"t": 3`, `"t": 1`, 1)
	if tampered == string(raw) {
		t.Fatal("pattern not found")
	}
	os.WriteFile(p, []byte(tampered), 0o600)
	if err := New(p).Unlock("correct horse battery"); err != ErrWrongPassword {
		t.Fatalf("tamper not detected: %v", err)
	}
}

// A password change that cannot be written leaves the old password in force,
// also for the next save.
func TestChangePasswordWriteFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vault.json")
	v := New(p)
	if err := v.Create("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o600)
	v.path = filepath.Join(blocker, "vault.json") // a folder that cannot exist
	if err := v.ChangePassword("correct horse battery", "new master password"); err == nil {
		t.Fatal("the change was written")
	}
	v.path = p
	if !v.VerifyPassword("correct horse battery") {
		t.Fatal("old password no longer accepted")
	}
	if err := v.Set("node/a/password", "x"); err != nil {
		t.Fatal(err)
	}
	v2 := New(p)
	if err := v2.Unlock("correct horse battery"); err != nil {
		t.Fatalf("saved under another password: %v", err)
	}
}
