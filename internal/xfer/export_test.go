// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package xfer

import "time"

// SetSFTPStart changes how long SFTP may take to start (tests).
func SetSFTPStart(d time.Duration) (restore func()) {
	old := sftpStart
	sftpStart = d
	return func() { sftpStart = old }
}
