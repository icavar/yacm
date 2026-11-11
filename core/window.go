// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/yacm-app/yacm/internal/store"
)

// windowClass is the Win32 class of the main window, so it can be found
// again to read and restore its placement.
const windowClass = "YACMWindow"

// windowState is the remembered size and position of the main window. On
// Windows Placement holds the raw WINDOWPLACEMENT (physical pixels, work
// area coordinates); elsewhere the logical size and position are used.
type windowState struct {
	Placement []byte `json:"placement,omitempty"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Maximised bool   `json:"maximised"`
}

// windowFile lives in the local default folder: window placement belongs to
// this computer's screens, also when the data folder is shared or synced.
func (a *App) windowFile() string { return filepath.Join(a.loc.Default, "window.json") }

func (a *App) loadWindowState() *windowState {
	data, err := os.ReadFile(a.windowFile())
	if err != nil {
		return nil
	}
	var ws windowState
	if json.Unmarshal(data, &ws) != nil {
		return nil
	}
	return &ws
}

func (a *App) saveWindowState(ws *windowState) {
	if ws == nil {
		return
	}
	if data, err := json.Marshal(ws); err == nil {
		_ = store.WriteFileAtomic(a.windowFile(), data, 0o600)
	}
}

// domReady places the window inside the visible work area of its monitor
// (never under the taskbar), restores the last size and position, and only
// then shows it.
//
// Linux and macOS call the DOM-ready hook on the UI thread, and the window
// calls used here wait for that thread, so the work runs in a goroutine.
func (a *App) domReady(ctx context.Context) {
	go func() {
		defer a.shown.Store(true)
		defer wruntime.WindowShow(ctx)
		defer func() { _ = recover() }() // a placement problem must never keep the window hidden
		placeWindow(ctx, a.loadWindowState())
	}()
}

// showFallback makes sure the window appears even if the page never
// reports that it is ready (it starts hidden).
func (a *App) showFallback(ctx context.Context) {
	go func() {
		time.Sleep(6 * time.Second)
		if !a.shown.Load() {
			wruntime.WindowShow(ctx)
		}
	}()
}

// rememberWindow stores the current placement for the next start.
func (a *App) rememberWindow(ctx context.Context) {
	defer func() { _ = recover() }()
	a.saveWindowState(currentWindowState(ctx))
}

// RequestAttention asks the system to point at YACM (Windows: the taskbar
// button flashes) when an output alert comes while it is in the background.
func (a *App) RequestAttention() {
	if a.ctx != nil {
		requestAttention(a.ctx)
	}
}
