// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package sshconn

import (
	"context"
	"strings"
	"testing"
)

// Without a username and nobody to ask, the error says what is missing
// instead of what a device answers to an empty username.
func TestMissingUsernameIsExplained(t *testing.T) {
	_, err := DialClient(context.Background(), Config{Hops: []Hop{{Host: "192.0.2.1", Port: 22}}})
	if err == nil || !strings.Contains(err.Error(), "no username set") {
		t.Fatalf("got %v", err)
	}
}
