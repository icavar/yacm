// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// YACM, Yet Another Connection Manager. The program itself is in package
// core; main only embeds the built frontend and starts it.
package main

import (
	"embed"
	"io/fs"
	"os"

	"github.com/yacm-app/yacm/core"
)

//go:embed all:frontend/dist
var assets embed.FS

// The window icon: GTK drops icons larger than about 256 pixels.
//
//go:embed build/appicon-256.png
var icon []byte

func main() {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		panic(err)
	}
	os.Exit(core.Main(core.Config{Assets: dist, Icon: icon}))
}
