// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yacm-app/yacm/internal/store"
)

func TestOpenLogUsesTemplateAndTicket(t *testing.T) {
	a := newTestApp(t)
	logs := t.TempDir()
	st := a.store.Settings()
	st.DefaultLogDir = logs
	st.LogNameTemplateOn, st.LogNameTemplate = true, "{path}/{ticket}_{name}.log"
	if err := a.store.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	g, _ := a.store.Upsert(&store.Node{Name: "DC", IsGroup: true})
	z, _ := a.store.Upsert(&store.Node{Name: "Zagreb", IsGroup: true, ParentID: g.ID})
	n, _ := a.store.Upsert(&store.Node{Name: "core-sw1", ParentID: z.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", LogToFile: true}})
	tb := &tab{id: "t1", node: n, ticket: cleanTicket(" CHG0012345\n")}
	f := a.openLog(tb)
	if f == nil {
		t.Fatal("no log file")
	}
	f.Close()
	want := filepath.Join(logs, "DC", "Zagreb", "CHG0012345_core-sw1.log")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s: %v", want, err)
	}
	// Templates off: the classic name in the log folder.
	st.LogNameTemplateOn = false
	_ = a.store.SetSettings(st)
	f = a.openLog(tb)
	f.Close()
	entries, _ := os.ReadDir(logs)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "core-sw1_") && strings.HasSuffix(e.Name(), ".log") {
			found = true
		}
	}
	if !found {
		t.Fatalf("classic name missing: %v", entries)
	}
}
