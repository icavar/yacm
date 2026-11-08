// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build !windows

package importer

import "errors"

func puttyLiveAvailable() bool { return false }

func importPuTTYLive(parentID string) (*Result, error) {
	return nil, errors.New("reading PuTTY sessions from the registry is only possible on Windows")
}
