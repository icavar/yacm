// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build !linux

package fileserver

import "errors"

// AllowPort is needed on Linux only; elsewhere low ports either work or need
// YACM to run as administrator.
func AllowPort(int) error {
	return errors.New("only needed on Linux")
}

// LowPortsConfName is where the setting is kept.
func LowPortsConfName() string { return "" }
