// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	// YACM's own licence is the GPL: it must be refused for a dependency.
	if kind, _ := classify("../.."); kind != "denied: GPL" {
		t.Fatalf("GPL: %q", kind)
	}
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("LICENSE", "Copyright (c) 2020\n\nPermission is hereby granted, free of\n  charge, to any person")
	if kind, _ := classify(dir); kind != "MIT" {
		t.Fatalf("MIT across lines: %q", kind)
	}
	write("COPYING.LESSER", "GNU LESSER GENERAL PUBLIC LICENSE Version 3")
	if kind, _ := classify(dir); kind != "denied: LGPL" {
		t.Fatalf("LGPL next to MIT: %q", kind)
	}
	if kind, file := classify(t.TempDir()); kind != "" || file != "" {
		t.Fatalf("no licence: %q %q", kind, file)
	}
}
