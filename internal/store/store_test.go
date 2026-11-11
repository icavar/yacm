// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTreeOps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	s, _ := Open(p)
	g, err := s.Upsert(&Node{Name: "Core", IsGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := s.Upsert(&Node{Name: "Sub", IsGroup: true, ParentID: g.ID})
	n, err := s.Upsert(&Node{Name: "R1", ParentID: sub.ID, Session: &Session{Protocol: ProtoSSH, Host: "10.0.0.1", Clusters: []string{"CORE"}}})
	if err != nil || n.Session.Port != 22 || n.Session.Algorithms != AlgoSecure {
		t.Fatalf("%v %+v", err, n)
	}
	if err := s.Move(g.ID, sub.ID, 0); err == nil {
		t.Fatal("cycle allowed")
	}
	d, _, err := s.Duplicate(n.ID)
	if err != nil || d.Name != "R1 (copy)" {
		t.Fatal(err)
	}
	if ids := s.SessionsInCluster("core"); len(ids) != 2 {
		t.Fatalf("cluster %v", ids)
	}
	s2, err := Open(p)
	if err != nil || len(s2.List()) != 4 {
		t.Fatalf("reload %v %d", err, len(s2.List()))
	}
	removed, err := s2.Delete(g.ID)
	if err != nil || len(removed) != 4 || len(s2.List()) != 0 {
		t.Fatalf("delete %v %v", removed, err)
	}
	if _, err := s2.Upsert(&Node{Name: "bad", Session: &Session{Protocol: ProtoSerial}}); err == nil {
		t.Fatal("serial without device accepted")
	}
}

// Settings written with font size 0 are kept (with the default font size),
// not replaced by the defaults.
func TestSettingsWithFontSizeZero(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	os.WriteFile(p, []byte(`{"version":2,"settings":{"fontSize":0,"theme":"light","scrollback":2000},"nodes":[]}`), 0o600)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	st := s.Settings()
	if st.Theme != "light" || st.Scrollback != 2000 || st.FontSize != DefaultSettings().FontSize {
		t.Fatalf("settings %+v", st)
	}
}
