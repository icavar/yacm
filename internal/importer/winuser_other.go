// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build !windows

package importer

// CurrentWindowsAccount is only meaningful on Windows.
func CurrentWindowsAccount() (name, sid string) { return "", "" }
