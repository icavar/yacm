// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package ppk

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob("testdata/*.ppk")
	for i, p := range files {
		if i >= 6 {
			break
		}
		if data, err := os.ReadFile(p); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte("PuTTY-User-Key-File-3: ssh-ed25519\nEncryption: none\nComment: x\nPublic-Lines: 1\nAAAA\nPrivate-Lines: 1\nAAAA\nPrivate-MAC: 00\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(data, nil)
		_, _ = Parse(data, []byte("passphrase"))
	})
}
