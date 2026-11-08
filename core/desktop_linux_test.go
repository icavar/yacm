// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build linux

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopEntry(t *testing.T) {
	data := t.TempDir()
	exe := "/home/ana/My Apps/yacm-0.8.11/yacm"
	if err := writeDesktopEntry(data, exe, []byte("png")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(data, "applications", "yacm.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`Exec="/home/ana/My Apps/yacm-0.8.11/yacm"`, "Icon=" + filepath.Join(data, "icons", "yacm.png"), "StartupWMClass=yacm\n", desktopMarker} {
		if !strings.Contains(s, want) {
			t.Errorf("entry lacks %q:\n%s", want, s)
		}
	}
	// Moved: the entry follows the program.
	if err := writeDesktopEntry(data, "/opt/yacm/yacm", []byte("png")); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(data, "applications", "yacm.desktop"))
	if !strings.Contains(string(b), `Exec="/opt/yacm/yacm"`) {
		t.Errorf("entry not updated:\n%s", b)
	}
	// An entry someone else wrote is left alone.
	own := "[Desktop Entry]\nName=My YACM\nExec=/usr/bin/yacm\n"
	_ = os.WriteFile(filepath.Join(data, "applications", "yacm.desktop"), []byte(own), 0o644)
	if err := writeDesktopEntry(data, exe, []byte("png")); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(data, "applications", "yacm.desktop"))
	if string(b) != own {
		t.Errorf("foreign entry changed:\n%s", b)
	}
}

func TestDesktopQuote(t *testing.T) {
	if got := desktopQuote(`/a b/$x"%`); got != `"/a b/\\$x\\"%%"` {
		t.Errorf("got %s", got)
	}
}
