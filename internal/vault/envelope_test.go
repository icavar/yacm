// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package vault

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSealOpenMap(t *testing.T) {
	data := map[string]string{"node/a/password": "s3cret", "cred/c/password": "p@ss"}
	if _, err := SealMap("short", data); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("a short export password must be refused, got %v", err)
	}
	env, err := SealMap("export-password-1", data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenMap("export-password-1", env)
	if err != nil || got["node/a/password"] != "s3cret" || len(got) != 2 {
		t.Fatalf("round trip: %v %v", got, err)
	}
	if _, err := OpenMap("wrong-password-1", env); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	env.CT[0] ^= 1
	if _, err := OpenMap("export-password-1", env); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("a changed ciphertext must fail: %v", err)
	}
	if _, err := OpenMap("x", nil); err == nil {
		t.Fatal("nil envelope")
	}
}

func TestSnapshotKeysCountReadOnly(t *testing.T) {
	v := New(filepath.Join(t.TempDir(), "vault.json"))
	if v.Unlocked() {
		t.Fatal("a new vault is locked")
	}
	if _, err := v.Snapshot([]string{"node/"}); !errors.Is(err, ErrLocked) {
		t.Fatalf("snapshot of a locked vault: %v", err)
	}
	if err := v.Create("master-password-1"); err != nil {
		t.Fatal(err)
	}
	for k, val := range map[string]string{"node/a/password": "1", "node/b/password": "2", "cred/c/password": "3"} {
		if err := v.Set(k, val); err != nil {
			t.Fatal(err)
		}
	}
	if v.Count() != 3 {
		t.Fatalf("count %d", v.Count())
	}
	if k := v.Keys("node/"); len(k) != 2 || k[0] != "node/a/password" {
		t.Fatalf("keys %v", k)
	}
	snap, err := v.Snapshot([]string{"cred/"})
	if err != nil || len(snap) != 1 || snap["cred/c/password"] != "3" {
		t.Fatalf("snapshot %v %v", snap, err)
	}
	v.SetReadOnly(true)
	if err := v.Set("node/x/password", "y"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a read-only vault must refuse changes: %v", err)
	}
}
