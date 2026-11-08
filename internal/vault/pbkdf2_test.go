// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A vault made with PBKDF2 (a build restricted to FIPS 140-3 algorithms)
// opens in every build, keeps its algorithm when the password changes, and
// argon2id vaults stay as they are.
func TestPBKDF2Vault(t *testing.T) {
	old := NewVaultKDF
	NewVaultKDF = PBKDF2KDF
	defer func() { NewVaultKDF = old }()

	path := filepath.Join(t.TempDir(), "vault.json")
	v := New(path)
	if err := v.Create("correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("session:1:password", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if alg := kdfOf(t, path); alg != KDFPBKDF2 {
		t.Fatalf("kdf %q, want %q", alg, KDFPBKDF2)
	}
	NewVaultKDF = old // a build with the default reads it too
	v = New(path)
	if err := v.Unlock("wrong password"); err != ErrWrongPassword {
		t.Fatalf("wrong password: %v", err)
	}
	if err := v.Unlock("correct horse"); err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Get("session:1:password"); got != "s3cret" {
		t.Fatalf("secret %q", got)
	}
	if err := v.ChangePassword("correct horse", "battery staple"); err != nil {
		t.Fatal(err)
	}
	if alg := kdfOf(t, path); alg != KDFPBKDF2 {
		t.Fatalf("after a password change: kdf %q", alg)
	}
	v.Lock()
	if err := v.Unlock("battery staple"); err != nil {
		t.Fatal(err)
	}
}

func TestPBKDF2Envelope(t *testing.T) {
	old := NewVaultKDF
	NewVaultKDF = PBKDF2KDF
	e, err := SealMap("export password", map[string]string{"k": "v"})
	NewVaultKDF = old
	if err != nil {
		t.Fatal(err)
	}
	if e.KDF.Alg != KDFPBKDF2 || e.KDF.Iter < pbkdf2Min {
		t.Fatalf("kdf %+v", e.KDF)
	}
	m, err := OpenMap("export password", e)
	if err != nil || m["k"] != "v" {
		t.Fatalf("open: %v %v", m, err)
	}
}

// Too few iterations (a tampered file) are refused before deriving.
func TestPBKDF2WeakParamsRefused(t *testing.T) {
	p := PBKDF2KDF()
	p.Salt = make([]byte, saltLen)
	p.Iter = 1000
	if _, err := derive("password", p); err == nil {
		t.Fatal("1000 iterations accepted")
	}
	p.Iter = pbkdf2Min
	p.Salt = p.Salt[:8]
	if _, err := derive("password", p); err == nil {
		t.Fatal("short salt accepted")
	}
}

// An argon2id vault's header is written exactly as before (the new
// iteration member is left out), so older versions still open it.
func TestArgon2HeaderUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.json")
	v := New(path)
	if err := v.Create("correct horse"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var f struct {
		KDF map[string]any `json:"kdf"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.KDF["i"]; ok || f.KDF["alg"] != KDFArgon2id {
		t.Fatalf("argon2id header: %v", f.KDF)
	}
}

func kdfOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.KDF.Alg
}
