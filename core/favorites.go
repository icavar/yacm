// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/yacm-app/yacm/internal/store"
)

// Favourite sessions are personal: kept per computer next to recent.json,
// never in sessions.json (which may be in a shared folder) or in a team
// library, so they also work for team library sessions.

var favMu sync.Mutex

func (a *App) favoritesFile() string { return filepath.Join(a.loc.Default, "favorites.json") }

func (a *App) readFavorites() []string {
	data, err := os.ReadFile(a.favoritesFile())
	if err != nil {
		return []string{}
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil || ids == nil {
		return []string{}
	}
	return ids
}

// Favorites returns the IDs of the favourite sessions, in the order added.
func (a *App) Favorites() []string {
	favMu.Lock()
	defer favMu.Unlock()
	return a.readFavorites()
}

// SetFavorite adds a session to the favourites or removes it.
func (a *App) SetFavorite(id string, on bool) ([]string, error) {
	favMu.Lock()
	defer favMu.Unlock()
	ids := a.readFavorites()
	out := make([]string, 0, len(ids)+1)
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	if on && id != "" && len(out) < 500 {
		out = append(out, id)
	}
	data, err := json.Marshal(out)
	if err != nil {
		return ids, err
	}
	if err := store.WriteFileAtomic(a.favoritesFile(), data, 0o600); err != nil {
		return ids, err
	}
	return out, nil
}
