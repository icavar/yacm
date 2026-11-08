// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build windows

package importer

import (
	"os/user"
	"strings"

	"golang.org/x/sys/windows"
)

// CurrentWindowsAccount returns the user name and SID that Xshell uses to
// protect saved passwords.
func CurrentWindowsAccount() (name, sid string) {
	if u, err := user.Current(); err == nil {
		name = u.Username
		if i := strings.LastIndex(name, `\`); i >= 0 {
			name = name[i+1:]
		}
	}
	tok := windows.GetCurrentProcessToken()
	if tu, err := tok.GetTokenUser(); err == nil {
		sid = tu.User.Sid.String()
	}
	return name, sid
}
