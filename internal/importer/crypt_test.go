// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import "testing"

func TestDecryptAsbru(t *testing.T) {
	got, err := decryptAsbru("53616c7465645f5f4e61bc00000000007bc84fc432ae3881")
	if err != nil || got != "test" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = decryptAsbru("53616c7465645f5f4e61bc000000000040c36c7a0c22235716c5446266d553ac")
	if err != nil || got != "sudo-secret" {
		t.Fatalf("got %q, %v", got, err)
	}
	if v, err := decryptAsbru("jp-plain"); err != errNotEncrypted || v != "jp-plain" {
		t.Fatalf("plain passthrough: %q %v", v, err)
	}
}
