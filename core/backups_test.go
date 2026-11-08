// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"os"
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

func TestBackupAndRestore(t *testing.T) {
	a := newTestApp(t)
	restarted := false
	a.restart = func(...string) error { restarted = true; return nil }
	g, err := a.store.Upsert(&store.Node{Name: "DC", IsGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Upsert(&store.Node{Name: "r1", ParentID: g.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	// Deleting a group makes a backup first.
	if err := a.DeleteNode(g.ID); err != nil {
		t.Fatal(err)
	}
	list := a.ListBackups()
	var before string
	for _, b := range list {
		if b.Reason == "before deleting a group" {
			before = b.ID
			if b.Sessions != 1 || b.Groups != 1 {
				t.Fatalf("backup counts: %+v", b)
			}
		}
	}
	if before == "" {
		t.Fatalf("no backup before the delete: %+v", list)
	}
	if err := a.RestoreBackup(before); err != nil {
		t.Fatal(err)
	}
	if !restarted {
		t.Fatal("YACM was not restarted after the restore")
	}
	data, _ := os.ReadFile(a.store.Path())
	if !strings.Contains(string(data), `"name": "r1"`) {
		t.Fatal("sessions.json not restored")
	}
	if a.ListBackups()[0].Reason != "before restore" {
		t.Fatal("current state not saved before the restore")
	}
}
