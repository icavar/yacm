// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package team

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

func TestLibraryFromNewerYACM(t *testing.T) {
	old := AppVersion
	AppVersion = "0.7.0"
	defer func() { AppVersion = old }()

	plain := `{"format":"yacm-team-library","version":1,"name":"Lab","revision":1,"app":"0.7.0","logins":[],
	  "nodes":[{"id":"s1","name":"r1","session":{"protocol":"ssh","host":"10.0.0.1"}}]}`
	f, err := Parse([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if newer, _ := f.NeedsNewer(); newer {
		t.Fatal("a library from the same version can be edited")
	}

	for name, data := range map[string]string{
		"newer app":    strings.Replace(plain, `"app":"0.7.0"`, `"app":"1.2.0"`, 1),
		"new protocol": strings.Replace(plain, `"nodes":[`, `"nodes":[{"id":"w1","name":"win","session":{"protocol":"rdp","host":"10.0.0.50","rdp":{"width":1920}}},`, 1),
		"new member":   strings.Replace(plain, `"host":"10.0.0.1"`, `"host":"10.0.0.1","x11":true`, 1),
		"new section":  strings.Replace(plain, `"logins":[]`, `"logins":[],"gateways":[{"id":"g"}]`, 1),
	} {
		f, err := Parse([]byte(data))
		if err != nil {
			t.Fatalf("%s: a library from a newer YACM must still be readable: %v", name, err)
		}
		if newer, why := f.NeedsNewer(); !newer || why == "" {
			t.Errorf("%s: NeedsNewer = false", name)
		}
		// Replacing it from here is refused, even when forced (expect -1).
		path := filepath.Join(t.TempDir(), "lib.json")
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		mine := &File{Name: "Mine", Logins: []Login{}, Nodes: []*store.Node{{ID: "x", Name: "x", Session: &store.Session{Protocol: store.ProtoSSH, Host: "h"}}}}
		var en *ErrNewer
		if _, err := Save(path, mine, -1, "me@pc"); !errors.As(err, &en) {
			t.Errorf("%s: Save over a newer library: %v", name, err)
		}
		if raw, _ := os.ReadFile(path); string(raw) != data {
			t.Errorf("%s: the newer library was changed", name)
		}
	}

	// Its members survive a parse and save round trip of the same file (as
	// when a newer version re-reads its cache).
	f, _ = Parse([]byte(strings.Replace(plain, `"host":"10.0.0.1"`, `"host":"10.0.0.1","x11":true`, 1)))
	path := filepath.Join(t.TempDir(), "lib.json")
	AppVersion = "9.9.9" // pretend to be the newer version publishing it
	if _, err := Save(path, f, -1, "me@pc"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"x11": true`) || !strings.Contains(string(raw), `"app": "9.9.9"`) {
		t.Fatalf("saved library:\n%s", raw)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"0.7.0", "0.7.0", 0}, {"0.10.0", "0.9.9", 1}, {"YACM 1.0", "1.0.0", 0}, {"0.6.9", "0.7", -1}, {"2.0.0-beta", "1.9.9", 1}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
