// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package sshconn

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
)

// hashWithSalt reproduces OpenSSH HashKnownHosts entries: |1|salt|HMAC-SHA1(salt, host).
func hashWithSalt(saltB64, host string) string {
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return ""
	}
	m := hmac.New(sha1.New, salt)
	m.Write([]byte(host))
	return "|1|" + saltB64 + "|" + base64.StdEncoding.EncodeToString(m.Sum(nil))
}
