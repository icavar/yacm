// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package applog

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestLogRotateTailRecover(t *testing.T) {
	setCrashOutput = func(*os.File, debug.CrashOptions) error { return nil }
	d := t.TempDir()
	if err := Open(d); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close) // the log file must be closed before TempDir is removed (Windows)
	Infof("started %s", "YACM test")
	Errorf("connect %s: %v", "r1 (10.0.0.1:22)", "connection refused")
	func() {
		defer Recover("test", nil)
		var m map[string]int
		m["x"] = 1 // panics
	}()
	tail := Tail(50)
	for _, want := range []string{"INFO  started YACM test", "ERROR connect r1 (10.0.0.1:22): connection refused", "PANIC test: assignment to entry in nil map", "applog_test.go"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("log misses %q:\n%s", want, tail)
		}
	}
	// Rotation at 1 MB keeps four older files.
	big := strings.Repeat("x", 4000)
	for i := 0; i < 1500; i++ {
		Infof("%s", big)
	}
	if _, err := os.Stat(filepath.Join(d, "yacm.1.log")); err != nil {
		t.Fatal("not rotated")
	}
	if fi, _ := os.Stat(filepath.Join(d, "yacm.log")); fi.Size() >= maxSize {
		t.Fatalf("current log too big: %d", fi.Size())
	}
	if got := strings.Count(Tail(3), "\n"); got != 2 {
		t.Fatalf("tail lines: %d", got+1)
	}
}
