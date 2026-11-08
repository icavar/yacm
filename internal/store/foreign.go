// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"regexp"
	"strings"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// ValidID reports whether id can be a node or profile ID. Vault keys are
// built from IDs ("node/<id>/password"), so an ID from someone else's file
// with a "/" in it could name another session's secret.
func ValidID(id string) bool { return validID.MatchString(id) }

// IsNetworkPath reports whether p could name a file on another computer: a
// UNC path (\\server\share\key, //server/share) or a device-namespace path
// that reaches one (\\.\UNC\…, \\?\UNC\…, \\.\GLOBALROOT\…). Opening one on
// Windows sends the user's login hash to that server. Only a serial port
// (\\.\COM10) is allowed in that namespace.
func IsNetworkPath(p string) bool {
	q := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(p), "/", `\`))
	if strings.HasPrefix(q, `\\`) || strings.HasPrefix(q, `\??\`) {
		return !localDevice.MatchString(q) || strings.Contains(q, "..")
	}
	return strings.HasPrefix(q, "UNC\\") || strings.HasPrefix(q, "GLOBALROOT\\")
}

// localDevice: a serial port, or a local long path (\\?\C:\…) without "..".
var localDevice = regexp.MustCompile(`^(?:\\\\\.\\COM\d+|\\\\[.?]\\[A-Z]:\\[^:]*)$`)

// SerialDeviceOK reports whether a serial device name from someone else's
// file is a plain local port: COM3, \\.\COM10 or /dev/….
func SerialDeviceOK(d string) bool {
	return serialDevice.MatchString(strings.TrimSpace(d)) && !strings.Contains(d, "..")
}

var serialDevice = regexp.MustCompile(`(?i)^(?:COM\d+|\\\\\.\\COM\d+|/dev/[A-Za-z0-9._/:+-]+)$`)

// FromElsewhere makes a node from someone else's file (an import, a .yacm
// file) safe to keep with your own sessions: local commands ask before they
// run and paths to other computers are dropped. It returns how many local
// commands were set to ask and the paths it dropped.
func FromElsewhere(n *Node) (asked int, dropped []string) {
	if n == nil {
		return 0, nil
	}
	drop := func(p *string) {
		if *p != "" && IsNetworkPath(*p) {
			dropped = append(dropped, *p)
			*p = ""
		}
	}
	if s := n.Session; s != nil {
		for i := range s.Before {
			if !s.Before[i].Confirm {
				s.Before[i].Confirm = true
				asked++
			}
		}
		for i := range s.After {
			if !s.After[i].Confirm {
				s.After[i].Confirm = true
				asked++
			}
		}
		drop(&s.KeyPath)
		drop(&s.CertPath)
		drop(&s.LogDir)
		for i := range s.Jump {
			drop(&s.Jump[i].KeyPath)
		}
		if s.Serial != nil && s.Serial.Device != "" && !SerialDeviceOK(s.Serial.Device) {
			dropped = append(dropped, s.Serial.Device)
			s.Serial.Device = ""
		}
		if s.Auth == AuthPublicKey && s.KeyPath == "" && s.CredentialID == "" {
			s.Auth = AuthAsk
		}
	}
	return asked, dropped
}
