// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build linux

package core

// On Linux the dock and the activities overview take the icon of a running
// program from its menu entry (a .desktop file whose name matches the
// program name, "yacm"), not from the window. A YACM unpacked from the
// tar.gz has no such entry, so it keeps one of its own in the user's folder,
// without administrator rights: ~/.local/share/applications/yacm.desktop,
// pointing at the program wherever it is, and the icon next to it. GTK names
// the window class after the program file (yacm in the packages), so the
// entry matches that name.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/icavar/yacm/internal/applog"
)

const desktopMarker = "X-YACM-Managed=true"

func desktopEntry(icon []byte) {
	if len(icon) == 0 || os.Getenv("FLATPAK_ID") != "" || os.Getenv("SNAP") != "" || os.Getenv("APPIMAGE") != "" {
		return // a package brings its own entry
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		data = filepath.Join(home, ".local", "share")
	}
	for _, sys := range []string{"/usr/share/applications/yacm.desktop", "/usr/local/share/applications/yacm.desktop"} {
		if _, err := os.Stat(sys); err == nil {
			// Installed by a package: an entry of ours from an earlier
			// tar.gz would hide the package's, so it goes.
			removeOwnEntry(data)
			return
		}
	}
	if err := writeDesktopEntry(data, exe, icon); err != nil {
		applog.Warnf("menu entry: %v", err)
	}
}

// removeOwnEntry deletes the entry (and icon) YACM wrote under data, never
// one the user wrote.
func removeOwnEntry(data string) {
	entry := filepath.Join(data, "applications", "yacm.desktop")
	if b, err := os.ReadFile(entry); err == nil && strings.Contains(string(b), desktopMarker) {
		_ = os.Remove(entry)
		_ = os.Remove(filepath.Join(data, "icons", "yacm.png"))
	}
}

// writeDesktopEntry writes the icon and the entry under data when they are
// missing or out of date. An entry the user (or a package) wrote is left alone.
func writeDesktopEntry(data, exe string, icon []byte) error {
	iconPath := filepath.Join(data, "icons", "yacm.png")
	if old, err := os.ReadFile(iconPath); err != nil || !bytes.Equal(old, icon) {
		if err := os.MkdirAll(filepath.Dir(iconPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(iconPath, icon, 0o644); err != nil {
			return err
		}
	}
	entry := filepath.Join(data, "applications", "yacm.desktop")
	want := desktopFile(exe, iconPath)
	old, err := os.ReadFile(entry)
	switch {
	case err == nil && string(old) == want:
		return nil
	case err == nil && !strings.Contains(string(old), desktopMarker):
		return nil // not ours
	}
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		return err
	}
	tmp := entry + ".tmp"
	if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, entry); err != nil {
		return err
	}
	applog.Infof("menu entry %s points at %s", entry, exe)
	return nil
}

func desktopFile(exe, icon string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=YACM\n" +
		"GenericName=Connection Manager\n" +
		"Comment=SSH, telnet and serial connection manager\n" +
		"Exec=" + desktopQuote(exe) + "\n" +
		"TryExec=" + exe + "\n" +
		"Icon=" + icon + "\n" +
		"Terminal=false\n" +
		"Categories=Network;RemoteAccess;\n" +
		"Keywords=ssh;telnet;serial;console;terminal;network;\n" +
		"StartupWMClass=" + filepath.Base(exe) + "\n" +
		"StartupNotify=true\n" +
		desktopMarker + "\n"
}

// desktopQuote quotes a path for the Exec key (Desktop Entry Specification:
// inside double quotes, ", `, $ and \ are escaped with a backslash, and the
// key value escapes the backslash once more; % is doubled).
func desktopQuote(p string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range p {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`)
			b.WriteRune(r)
		case '\\':
			b.WriteString(`\\\\`)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
