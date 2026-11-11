// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/icavar/yacm/internal/applog"
)

// Config is what a program built on YACM gives Main.
type Config struct {
	// Assets is the built frontend (frontend/dist), embedded by the program.
	Assets fs.FS
	// Bind are more Go values whose methods the frontend may call, next to
	// the App (window.go.<package>.<Type> in the frontend).
	Bind []any
	// Icon is the application icon (PNG, at most 256 pixels: GTK drops larger
	// window icons): the window icon on Linux and the
	// icon of the menu entry YACM keeps for itself there.
	Icon []byte
}

// Main runs YACM: data folder, log, the window. It returns the exit code.
func Main(cfg Config) int {
	args := waitForPID(os.Args[1:])
	loc, err := resolveDataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "data directory:", err)
		return 1
	}
	if err := applog.Open(loc.Default); err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
	}
	defer applog.Close()
	applog.Infof("YACM %s starting on %s/%s; data folder %s (%s)", appVersion, runtime.GOOS, runtime.GOARCH, firstNonEmpty(loc.Dir, loc.Wanted, loc.Default), loc.Source)
	if p := storePackage(); p != "" {
		applog.Infof("installed from the Microsoft Store (%s)", p)
	}
	desktopEntry(cfg.Icon) // Linux: menu entry and dock icon
	app, err := newAppFor(loc, hasArg(args, "--take-over"))
	if err != nil {
		applog.Errorf("start: %v", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lockDir := loc.Dir
	if lockDir == "" {
		lockDir = loc.Wanted
	}
	err = wails.Run(&options.App{
		Title:                    appName,
		Width:                    1180,
		Height:                   740,
		MinWidth:                 720,
		MinHeight:                460,
		StartHidden:              true, // shown by domReady once it fits the screen
		BackgroundColour:         &options.RGBA{R: 17, G: 19, B: 23, A: 1},
		AssetServer:              &assetserver.Options{Assets: cfg.Assets},
		OnStartup:                app.startup,
		OnDomReady:               app.domReady,
		OnShutdown:               app.shutdown,
		OnBeforeClose:            app.beforeClose,
		Bind:                     append([]any{app}, cfg.Bind...),
		SingleInstanceLock:       &options.SingleInstanceLock{UniqueId: instanceID(lockDir), OnSecondInstanceLaunch: app.secondInstance},
		EnableDefaultContextMenu: false,
		Windows: &windows.Options{
			Theme:               windows.SystemDefault,
			WebviewUserDataPath: filepath.Join(loc.Default, "webview"),
			DisablePinchZoom:    true,
			WindowClassName:     windowClass,
		},
		Mac:   &mac.Options{About: &mac.AboutInfo{Title: appName, Message: "Yet Another Connection Manager " + appVersion}},
		Linux: &linux.Options{ProgramName: "yacm", Icon: cfg.Icon},
	})
	if err != nil {
		applog.Errorf("wails: %v", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	applog.Infof("YACM stopped")
	return 0
}
