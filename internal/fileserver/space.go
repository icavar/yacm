// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package fileserver

import (
	"errors"
	"fmt"
)

// DefaultMaxUpload is the largest file a device may upload when nothing
// else is set: room for any device image, not for filling the disk.
const DefaultMaxUpload = 16 << 30

// keepFree is the disk space uploads always leave free.
const keepFree = 1 << 30

var errNoSpace = errors.New("not enough free disk space for uploads")

// uploadLimit is the most one upload may write now: MaxUpload, and no more
// than the free space in the shared folder minus keepFree. Uploads from
// anyone who reaches the server (TFTP and HTTP have no login) cannot fill
// the disk.
func (c *Config) uploadLimit() (int64, error) {
	limit := c.MaxUpload
	if free, err := freeSpace(c.Folder); err == nil {
		room := free - keepFree
		if room <= 0 {
			return 0, errNoSpace
		}
		if limit <= 0 || room < limit {
			limit = room
		}
	}
	return limit, nil
}

func tooLarge(limit int64) error {
	return fmt.Errorf("file too large (at most %d MB here)", limit>>20)
}
