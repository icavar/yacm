// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/yacm-app/yacm/internal/applog"
	"github.com/yacm-app/yacm/internal/importer"
	"github.com/yacm-app/yacm/internal/serialconn"
	"github.com/yacm-app/yacm/internal/sshconn"
)

// ---- dialogs & misc ----------------------------------------------------------

// ChooseFile opens a native file picker.
func (a *App) ChooseFile(title string, kind string) (string, error) {
	opts := wruntime.OpenDialogOptions{Title: title, ShowHiddenFiles: true}
	home, _ := os.UserHomeDir()
	switch kind {
	case "key":
		if d := filepath.Join(home, ".ssh"); dirExists(d) {
			opts.DefaultDirectory = d
		}
	case "asbru":
		for _, d := range []string{filepath.Join(home, ".config", "asbru"), filepath.Join(home, ".config", "pac")} {
			if dirExists(d) {
				opts.DefaultDirectory = d
				break
			}
		}
		opts.Filters = []wruntime.FileFilter{{DisplayName: "Ásbrú / PAC config (*.yml, *.yaml)", Pattern: "*.yml;*.yaml"}}
	case "import":
		opts.Filters = []wruntime.FileFilter{
			{DisplayName: "Sessions (YACM, Ásbrú, other programs, CSV, Excel)", Pattern: "*.yacm;*.json;*.yml;*.yaml;*.xml;*.ini;*.mxtsessions;*.reg;*.csv;*.tsv;*.xlsx;*.xsh;config"},
			{DisplayName: "All files", Pattern: "*"},
		}
	case "table":
		opts.Filters = []wruntime.FileFilter{
			{DisplayName: "Device list (*.csv, *.xlsx)", Pattern: "*.csv;*.tsv;*.txt;*.xlsx;*.xlsm"},
			{DisplayName: "All files", Pattern: "*"},
		}
	case "team":
		opts.Filters = []wruntime.FileFilter{
			{DisplayName: "YACM team library (*.json)", Pattern: "*.json"},
			{DisplayName: "All files", Pattern: "*"},
		}
	}
	return wruntime.OpenFileDialog(a.ctx, opts)
}

// ChooseTeamFile asks where to save a team library.
func (a *App) ChooseTeamFile(defaultName string) (string, error) {
	p, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title: "Save the team library", DefaultFilename: defaultName,
		Filters: []wruntime.FileFilter{{DisplayName: "YACM team library (*.json)", Pattern: "*.json"}},
	})
	if err != nil || p == "" {
		return p, err
	}
	if filepath.Ext(p) == "" {
		p += ".json"
	}
	return p, nil
}

// ChooseSaveFile opens a native save dialog for exports.
func (a *App) ChooseSaveFile(title, defaultName string) (string, error) {
	p, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title: title, DefaultFilename: defaultName,
		Filters: []wruntime.FileFilter{{DisplayName: "YACM sessions (*.yacm)", Pattern: "*.yacm"}},
	})
	if err != nil || p == "" {
		return p, err
	}
	if filepath.Ext(p) == "" {
		p += ".yacm"
	}
	return p, nil
}

// ChooseDirectory opens a native folder picker.
func (a *App) ChooseDirectory(title string) (string, error) {
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{Title: title})
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ListSerialPorts returns available serial devices.
func (a *App) ListSerialPorts() ([]serialconn.PortInfo, error) { return serialconn.List() }

// ImportSources lists configurations of other programs found on this computer.
func (a *App) ImportSources() []importer.Source { return importer.FindSources() }

func (a *App) KnownHostEntries() ([]sshconn.Entry, error) { return a.kh.List() }

// RemoveKnownHost forgets a host key.
func (a *App) RemoveKnownHost(host string) error { return a.kh.Remove(host) }

// About returns version information relevant for security reviews.
func (a *App) About() map[string]string {
	out := map[string]string{"app": appVersion, "dataDir": a.dataDir}
	if p := storePackage(); p != "" {
		out["store"] = p // the Microsoft Store updates this copy
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		out["go"] = bi.GoVersion
		for _, d := range bi.Deps {
			v := d.Version
			if d.Replace != nil {
				v = d.Replace.Version
			}
			switch d.Path {
			case "golang.org/x/crypto":
				out["xcrypto"] = v
			case "github.com/wailsapp/wails/v2":
				out["wails"] = v
			case "github.com/wailsapp/go-webview2":
				out["webview2"] = v
			}
		}
	}
	return out
}

// logOp writes the outcome of a user operation into yacm.log.
func logOp(op string, err *error) {
	if *err != nil {
		applog.Warnf("%s: %v", op, *err)
	} else {
		applog.Infof("%s: done", op)
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// LogUIError records an error from the user interface (JavaScript) in yacm.log.
func (a *App) LogUIError(msg string) {
	if len(msg) > 2000 {
		msg = msg[:2000] + "…"
	}
	applog.Errorf("ui: %s", msg)
}

// Diagnostics is a short report for bug reports (About > Copy diagnostics):
// versions, system, settings that matter for support and the end of yacm.log.
// It holds no passwords or session contents, but the end of the log names
// sessions and the hosts they connect to; the home folder in paths is
// shortened to ~.
func (a *App) Diagnostics(userAgent string) string {
	var b strings.Builder
	about := a.About()
	fmt.Fprintf(&b, "YACM %s on %s/%s\n", appVersion, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "Go %s, Wails %s, x/crypto %s", about["go"], about["wails"], about["xcrypto"])
	if about["webview2"] != "" {
		fmt.Fprintf(&b, ", WebView2 loader %s", about["webview2"])
	}
	b.WriteString("\n")
	if userAgent != "" {
		fmt.Fprintf(&b, "Web view: %s\n", userAgent)
	}
	a.locMu.Lock()
	loc := a.loc
	a.locMu.Unlock()
	fmt.Fprintf(&b, "Data folder: %s (%s)", shortHome(firstNonEmpty(loc.Dir, loc.Wanted)), loc.Source)
	if a.lockedBy != nil {
		b.WriteString(", read-only (open elsewhere)")
	}
	b.WriteString("\n")
	if a.store != nil {
		groups, sessions := 0, 0
		for _, n := range a.store.List() {
			if n.IsGroup {
				groups++
			} else {
				sessions++
			}
		}
		st := a.store.Settings()
		fmt.Fprintf(&b, "Sessions %d, groups %d, credential profiles %d, team libraries %d\n", sessions, groups, len(a.store.Credentials()), len(st.TeamLibraries))
		fmt.Fprintf(&b, "Settings: theme %s, font %d, right click %s, hostname rename %s, reopen sessions %v, mask log secrets %v, auto-lock %d min\n",
			st.Theme, st.FontSize, st.RightClick, st.HostnameRename, st.ReopenSessions, !st.LogSecretsInClear, st.AutoLockMinutes)
	}
	fmt.Fprintf(&b, "Vault: exists %v, unlocked %v\n", a.vault.Exists(), a.vault.Unlocked())
	b.WriteString("\n--- yacm.log (last 200 lines) ---\n")
	b.WriteString(shortHome(applog.Tail(200)))
	b.WriteString("\n")
	return b.String()
}

// shortHome replaces the user's home folder with ~ (it usually holds the
// user name).
func shortHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || len(home) < 3 {
		return s
	}
	s = strings.ReplaceAll(s, home, "~")
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, strings.ToLower(home), "~")
	}
	return s
}

// StartInfo tells the UI what to show at start: the first-run guide or
// "What's new".
type StartInfo struct {
	Version  string `json:"version"`
	LastSeen string `json:"lastSeen"` // version whose news was shown last ("" = none)
	Fresh    bool   `json:"fresh"`    // nothing set up yet: show the guide
	DemoFile string `json:"demoFile"` // demo.yacm next to YACM, if there is one
}

// GetStartInfo is read once after the window opens.
func (a *App) GetStartInfo() StartInfo {
	st := a.store.Settings()
	return StartInfo{
		Version:  appVersion,
		LastSeen: st.LastSeenVersion,
		Fresh:    st.LastSeenVersion == "" && len(a.store.List()) == 0 && len(st.TeamLibraries) == 0,
		DemoFile: demoFile(),
	}
}

// MarkVersionSeen records that the news of this version were shown.
func (a *App) MarkVersionSeen() error {
	if a.store.ReadOnly() {
		return nil
	}
	st := a.store.Settings()
	if st.LastSeenVersion == appVersion {
		return nil
	}
	st.LastSeenVersion = appVersion
	return a.store.SetSettings(st)
}

// demoFile finds demo.yacm from the separate demo download: in a demo
// folder next to the executable, or in an unpacked yacm-demo-device-* (or
// older yacm-*-demo-*) folder next to it or next to YACM's own folder (both
// downloads unpacked in one place).
func demoFile() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return demoFileNear(filepath.Dir(exe))
}

func demoFileNear(dir string) string {
	isFile := func(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }
	for _, p := range []string{filepath.Join(dir, "demo", "demo.yacm"), filepath.Join(dir, "demo.yacm")} {
		if isFile(p) {
			return p
		}
	}
	for _, d := range []string{dir, filepath.Dir(dir)} {
		m, _ := filepath.Glob(filepath.Join(d, "yacm-*-demo-*", "demo.yacm"))
		// yacm-demo-device-<version>-<os> (0.9.1 and newer) sorts after
		// the older yacm-<version>-demo-<os>.
		n, _ := filepath.Glob(filepath.Join(d, "yacm-demo-device-*", "demo.yacm"))
		m = append(m, n...)
		sort.Strings(m)
		if len(m) > 0 {
			return m[len(m)-1] // several versions: the last by name
		}
	}
	return ""
}
