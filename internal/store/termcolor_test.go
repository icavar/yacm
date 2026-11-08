// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"path/filepath"
	"testing"
)

func TestValidTermColor(t *testing.T) {
	for _, c := range []string{"", "theme", "#000000", "#A1b2C3"} {
		if !ValidTermColor(c) {
			t.Errorf("%q should be valid", c)
		}
	}
	for _, c := range []string{"red", "#fff", "#12345g", "1234567", "#1234567"} {
		if ValidTermColor(c) {
			t.Errorf("%q should be refused", c)
		}
	}
}

func TestTermColorsValidateAndBulk(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(&Node{Name: "x", Session: &Session{Protocol: ProtoSSH, Host: "h", TermBackground: "dark red"}}); err == nil {
		t.Fatal("a colour that is not #rrggbb must be refused")
	}
	g, err := s.Upsert(&Node{Name: "prod", IsGroup: true, Defaults: &GroupDefaults{TermBackground: "#3a1214"}})
	if err != nil {
		t.Fatal(err)
	}
	if g.Defaults == nil || g.Defaults.TermBackground != "#3a1214" {
		t.Fatal("group terminal colours are kept (a group with only colours is not empty)")
	}
	a, _ := s.Upsert(&Node{Name: "a", ParentID: g.ID, Session: &Session{Protocol: ProtoSSH, Host: "a"}})
	b, _ := s.Upsert(&Node{Name: "b", ParentID: g.ID, Session: &Session{Protocol: ProtoTelnet, Host: "b", TermText: "#ffffff"}})
	bg, theme := "#0f2a1a", TermColorTheme
	if _, err := s.BulkUpdate([]string{a.ID, b.ID}, BulkChange{TermBackground: &bg, TermText: &theme}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		n, _ := s.Get(id)
		if n.Session.TermBackground != bg || n.Session.TermText != TermColorTheme {
			t.Fatalf("bulk edit did not set the colours: %+v", n.Session)
		}
	}
	bad := "nope"
	if _, err := s.BulkUpdate([]string{a.ID}, BulkChange{TermText: &bad}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Get(a.ID); n.Session.TermText != TermColorTheme {
		t.Fatal("bulk edit must ignore an invalid colour")
	}

	st := s.Settings()
	st.TermText, st.TermBackground = "theme", "#zzzzzz"
	if err := s.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings(); got.TermText != "" || got.TermBackground != "" {
		t.Fatalf("settings keep only #rrggbb colours: %q %q", got.TermText, got.TermBackground)
	}
}
