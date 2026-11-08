// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"testing"
)

func TestOpenTabsRestore(t *testing.T) {
	a := newTestApp(t)
	if got := a.GetOpenTabs(); len(got.Sessions) != 0 || got.Crashed {
		t.Fatalf("fresh: %+v", got)
	}
	if err := a.SaveOpenTabs(OpenTabs{Sessions: []string{"a", "b"}, Layout: "2v", Panes: []int{1, 0}, Active: 1}); err != nil {
		t.Fatal(err)
	}
	// Not closed normally (crash): offered again.
	if got := a.GetOpenTabs(); !got.Crashed || len(got.Sessions) != 2 || got.Layout != "2v" || got.Active != 1 {
		t.Fatalf("after crash: %+v", got)
	}
	// Normal exit without the setting: forgotten.
	a.closeOpenTabs()
	if got := a.GetOpenTabs(); len(got.Sessions) != 0 || got.Crashed {
		t.Fatalf("after normal exit: %+v", got)
	}
	// Saves after shutdown started are ignored.
	_ = a.SaveOpenTabs(OpenTabs{Sessions: []string{"late"}})
	if got := a.GetOpenTabs(); len(got.Sessions) != 0 {
		t.Fatalf("saved during shutdown: %+v", got)
	}

	// With the setting the list survives a normal exit, not marked as a crash.
	a.tabsDone.Store(false)
	st := a.store.Settings()
	st.ReopenSessions = true
	if err := a.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	_ = a.SaveOpenTabs(OpenTabs{Sessions: []string{"a"}, Active: 0})
	a.closeOpenTabs()
	if got := a.GetOpenTabs(); len(got.Sessions) != 1 || got.Crashed {
		t.Fatalf("with reopen setting: %+v", got)
	}
}
