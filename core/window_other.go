// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build !windows

package core

import (
	"context"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func placeWindow(ctx context.Context, ws *windowState) {
	screenW, screenH := 0, 0
	if screens, err := wruntime.ScreenGetAll(ctx); err == nil {
		for _, s := range screens {
			if s.IsCurrent || screenW == 0 {
				screenW, screenH = s.Size.Width, s.Size.Height
			}
		}
	}
	if ws == nil || ws.Width < 400 || ws.Height < 300 {
		// First start: fit the default size to the screen, keeping room for
		// panels and docks.
		if screenW > 0 {
			w, h := wruntime.WindowGetSize(ctx)
			if w > screenW-40 || h > screenH-80 {
				wruntime.WindowMaximise(ctx)
			} else {
				wruntime.WindowCenter(ctx)
			}
		}
		return
	}
	if ws.Maximised || (screenW > 0 && (ws.Width > screenW || ws.Height > screenH)) {
		wruntime.WindowMaximise(ctx)
		return
	}
	wruntime.WindowSetSize(ctx, ws.Width, ws.Height)
	if screenW > 0 && ws.X >= 0 && ws.Y >= 0 && ws.X+ws.Width <= screenW && ws.Y+ws.Height <= screenH {
		wruntime.WindowSetPosition(ctx, ws.X, ws.Y)
	} else {
		wruntime.WindowCenter(ctx)
	}
}

func currentWindowState(ctx context.Context) *windowState {
	ws := &windowState{Maximised: wruntime.WindowIsMaximised(ctx)}
	if ws.Maximised {
		return ws
	}
	ws.Width, ws.Height = wruntime.WindowGetSize(ctx)
	ws.X, ws.Y = wruntime.WindowGetPosition(ctx)
	return ws
}

// requestAttention would bounce the dock icon or mark the window; Wails v2
// offers no way to do that on macOS and Linux, so the alert stays in the
// window.
func requestAttention(_ context.Context) {}
