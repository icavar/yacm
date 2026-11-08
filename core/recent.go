// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/icavar/yacm/internal/store"
)

// Recently opened sessions, newest first, for the quick switcher. Kept per
// computer next to open-tabs.json (not in a shared data folder).

const maxRecent = 20

var recentMu sync.Mutex

func (a *App) recentFile() string { return filepath.Join(a.loc.Default, "recent.json") }

func (a *App) readRecent() []string {
	data, err := os.ReadFile(a.recentFile())
	if err != nil {
		return nil
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil {
		return nil
	}
	return ids
}

// noteRecent moves a session to the front of the recent list.
func (a *App) noteRecent(id string) {
	if id == "" {
		return
	}
	recentMu.Lock()
	defer recentMu.Unlock()
	ids := addRecent(a.readRecent(), id)
	if data, err := json.Marshal(ids); err == nil {
		_ = store.WriteFileAtomic(a.recentFile(), data, 0o600)
	}
}

func addRecent(ids []string, id string) []string {
	out := []string{id}
	for _, x := range ids {
		if x != id && len(out) < maxRecent {
			out = append(out, x)
		}
	}
	return out
}

// RecentSessions returns the IDs of the sessions opened last, newest first.
func (a *App) RecentSessions() []string {
	recentMu.Lock()
	defer recentMu.Unlock()
	if ids := a.readRecent(); ids != nil {
		return ids
	}
	return []string{}
}
