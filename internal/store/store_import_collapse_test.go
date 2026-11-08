// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

// A large import keeps its folders closed; its top group stays open, and a
// small import stays open throughout.
func TestImportKeepsFoldersClosed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	build := func(top string, sessions int) []*Node {
		g := &Node{ID: NewID(), IsGroup: true, Expanded: true, Name: top}
		sub := &Node{ID: NewID(), ParentID: g.ID, IsGroup: true, Expanded: true, Name: "Site"}
		deep := &Node{ID: NewID(), ParentID: sub.ID, IsGroup: true, Expanded: true, Name: "Access"}
		nodes := []*Node{g, sub, deep}
		for i := 0; i < sessions; i++ {
			nodes = append(nodes, &Node{ID: NewID(), ParentID: deep.ID, Name: fmt.Sprintf("sw%d", i), Session: &Session{Protocol: ProtoSSH, Host: "10.0.0.1", Port: 22}})
		}
		return nodes
	}
	big, small := build("big", 500), build("small", 5)
	if err := s.ImportNodes(big); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportNodes(small); err != nil {
		t.Fatal(err)
	}
	open := func(n *Node) bool { got, _ := s.Get(n.ID); return got.Expanded }
	if !open(big[0]) || open(big[1]) || open(big[2]) {
		t.Fatalf("large import: top %v site %v access %v", open(big[0]), open(big[1]), open(big[2]))
	}
	if !open(small[0]) || !open(small[1]) || !open(small[2]) {
		t.Fatal("small import should stay open")
	}
}

// Imported sessions log to a file unless Settings turn that off.
func TestImportedSessionsLog(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	one := func() *Node {
		return &Node{ID: NewID(), Name: "r1", Session: &Session{Protocol: ProtoSSH, Host: "10.0.0.1", Port: 22}}
	}
	a := one()
	if err := s.ImportNodes([]*Node{a}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(a.ID); !got.Session.LogToFile {
		t.Fatal("imported session should log")
	}
	st := s.Settings()
	st.LogNewOff = true
	if err := s.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	b := one()
	if err := s.ImportNodes([]*Node{b}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(b.ID); got.Session.LogToFile {
		t.Fatal("logging for new sessions is off")
	}
}
