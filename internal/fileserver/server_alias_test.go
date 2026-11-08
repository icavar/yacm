// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package fileserver

import (
	"os"
	"path/filepath"
	"testing"
)

// A device asks for "cbr_copy.txt" (the helper's command for "cbr copy.txt").
func TestResolveSpaceAlias(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "cbr copy.txt"), []byte("x"), 0o644)
	got, err := resolve(dir, "/cbr_copy.txt", false)
	if err != nil || got != filepath.Join(dir, "cbr copy.txt") {
		t.Fatalf("read: %q %v", got, err)
	}
	// Uploads keep the name the device gives.
	got, err = resolve(dir, "cbr_copy.txt", true)
	if err != nil || got != filepath.Join(dir, "cbr_copy.txt") {
		t.Fatalf("write: %q %v", got, err)
	}
}
