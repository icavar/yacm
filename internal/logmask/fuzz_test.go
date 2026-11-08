// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package logmask

import (
	"bytes"
	"testing"
)

// The masker must not crash, and how the device's output happens to be split
// into reads must not change what ends up in the log.
func FuzzMasker(f *testing.F) {
	f.Add([]byte("enable secret 9 $9$abc\r\nsnmp-server community x RO\r\nR1#"), uint16(7))
	f.Add([]byte("-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\n"), uint16(20))
	f.Fuzz(func(t *testing.T, data []byte, cut uint16) {
		var whole Masker
		a := append(whole.Write(data), whole.Flush()...)
		var split Masker
		c := 0
		if len(data) > 0 {
			c = int(cut) % (len(data) + 1)
		}
		b := append(split.Write(data[:c]), split.Write(data[c:])...)
		b = append(b, split.Flush()...)
		if !bytes.Equal(a, b) {
			t.Fatalf("split at %d changes the log:\n%q\n%q", c, a, b)
		}
	})
}
