// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package xfer

import (
	"context"
	"strings"
	"testing"
)

// A path with shell characters never goes unquoted into a command.
func TestMD5ShellPathsQuoted(t *testing.T) {
	var sent []string
	_, _ = md5Cmds(context.Background(), "/tmp/x; reboot", func(cmd string) (string, error) {
		sent = append(sent, cmd)
		return "", nil
	})
	for _, c := range sent {
		if strings.Contains(c, "x; reboot") && !strings.Contains(c, "'") {
			t.Fatalf("unquoted: %q", c)
		}
	}
	sent = nil
	_, _ = md5Cmds(context.Background(), "/mnt/flash/EOS.swi", func(cmd string) (string, error) {
		sent = append(sent, cmd)
		return "", nil
	})
	if len(sent) != 4 {
		t.Fatalf("plain path: %q", sent)
	}
}
