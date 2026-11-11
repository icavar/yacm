// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/yacm-app/yacm/internal/store"
)

// OpenTabs is the set of sessions that were open, kept per computer (next to
// window.json in the default folder) so they can be opened again at start.
type OpenTabs struct {
	Sessions []string `json:"sessions"`         // node IDs in tab order (no quick connects)
	Layout   string   `json:"layout,omitempty"` // split view layout
	Panes    []int    `json:"panes,omitempty"`  // index into Sessions per pane, -1 = empty
	Active   int      `json:"active"`           // index into Sessions, -1 = none
	Clean    bool     `json:"clean"`            // YACM closed normally after this was saved
	Saved    string   `json:"saved,omitempty"`
	// Crashed is set by GetOpenTabs: YACM did not close normally while these
	// sessions were open.
	Crashed bool `json:"crashed,omitempty"`
}

func (a *App) openTabsFile() string { return filepath.Join(a.loc.Default, "open-tabs.json") }

// SaveOpenTabs records the open sessions (called by the UI when tabs change).
func (a *App) SaveOpenTabs(o OpenTabs) error {
	if a.tabsDone.Load() {
		return nil // shutting down: tabs closing now must not be recorded
	}
	o.Clean, o.Crashed = false, false
	o.Saved = time.Now().UTC().Format(time.RFC3339)
	return a.writeOpenTabs(&o)
}

func (a *App) writeOpenTabs(o *OpenTabs) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(a.openTabsFile(), data, 0o600)
}

func (a *App) readOpenTabs() *OpenTabs {
	data, err := os.ReadFile(a.openTabsFile())
	if err != nil {
		return nil
	}
	var o OpenTabs
	if json.Unmarshal(data, &o) != nil {
		return nil
	}
	return &o
}

// GetOpenTabs returns the sessions that were open last time, and whether
// YACM ended without closing normally.
func (a *App) GetOpenTabs() OpenTabs {
	o := a.readOpenTabs()
	if o == nil {
		return OpenTabs{Active: -1}
	}
	o.Crashed = !o.Clean && len(o.Sessions) > 0
	return *o
}

// closeOpenTabs marks a normal exit. Without the "reopen" setting the list
// is cleared, so only a crash offers the sessions again.
func (a *App) closeOpenTabs() {
	a.tabsDone.Store(true)
	o := a.readOpenTabs()
	if o == nil {
		return
	}
	o.Clean = true
	if a.store == nil || !a.store.Settings().ReopenSessions {
		*o = OpenTabs{Active: -1, Clean: true}
	}
	_ = a.writeOpenTabs(o)
}
