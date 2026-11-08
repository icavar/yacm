// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

// Sessions and team libraries from a newer YACM: kept and listed, but not
// opened, edited or copied with what this version does not know.
func TestNewerYACMData(t *testing.T) {
	a := newTestApp(t)
	g, _ := a.store.Upsert(&store.Node{Name: "DC", IsGroup: true})
	rdp, err := a.store.Upsert(&store.Node{Name: "jump-win", ParentID: g.ID, Session: &store.Session{Protocol: "rdp", Host: "10.0.0.50"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Connect(ConnectRequest{NodeID: rdp.ID, TabID: "t1", Cols: 80, Rows: 24}); err == nil || !strings.Contains(err.Error(), "newer version of YACM") {
		t.Fatalf("connect to an rdp session: %v", err)
	}
	if tb, _ := a.getTab("t1"); tb != nil {
		t.Fatal("no tab should be left behind")
	}

	// A library published by a newer YACM with an RDP session.
	libPath := filepath.Join(t.TempDir(), "noc.json")
	lib := `{"format":"yacm-team-library","version":1,"name":"NOC","revision":3,"app":"2.0.0","updatedBy":"kolega@pc","logins":[],
	  "nodes":[{"id":"s1","name":"core-sw1","session":{"protocol":"ssh","host":"10.0.0.1","x11":true}},
	           {"id":"w1","name":"jump-win","session":{"protocol":"rdp","host":"10.0.0.50","rdp":{"width":1920}}}]}`
	if err := os.WriteFile(libPath, []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := a.AddTeamLibrary(libPath)
	if err != nil {
		t.Fatalf("a library from a newer YACM must be usable: %v", err)
	}
	if st.Sessions != 2 || !strings.Contains(st.NeedsNewer, "2.0.0") {
		t.Fatalf("status %+v", st)
	}
	if _, err := a.EditTeamLibrary(st.ID); err == nil || !strings.Contains(err.Error(), "Update YACM") {
		t.Fatalf("editing a newer library: %v", err)
	}
	ssh := findNode(a, "core-sw1")
	win := findNodeIn(a, "jump-win", st.ID)
	if ssh == nil || win == nil {
		t.Fatal("library sessions should be listed")
	}
	if _, err := a.CopyToMySessions([]string{win.ID}); err == nil {
		t.Fatal("an unknown-type session cannot be copied")
	}
	id, err := a.CopyToMySessions([]string{ssh.ID})
	if err != nil {
		t.Fatal(err)
	}
	cp, _ := a.store.Get(id)
	if store.HasUnknown(cp) {
		t.Fatal("the copy must not carry members from the newer version")
	}
	if raw, _ := os.ReadFile(libPath); string(raw) != lib {
		t.Fatal("the library file must not change")
	}
}

func findNodeIn(a *App, name, lib string) *store.Node {
	for _, n := range a.store.ListAll() {
		if n.Name == name && n.Library == lib {
			return n
		}
	}
	return nil
}
