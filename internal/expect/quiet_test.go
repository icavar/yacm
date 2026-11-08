// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package expect

import (
	"regexp"
	"sync"
	"testing"
	"time"
)

// A Quiet rule answers a prompt that ends the output and is followed by
// silence, not the same words in a banner or a log line.
func TestQuietRuleOnlyAtAQuietPrompt(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	e := New([]Rule{{Label: "pw", Pattern: regexp.MustCompile(`Password:[ \t]*$`), Send: "secret", Return: true, Timeout: 5 * time.Second, Quiet: 80 * time.Millisecond}},
		func(b []byte) error { mu.Lock(); sent = append(sent, string(b)); mu.Unlock(); return nil }, nil)
	e.Start()
	got := func() int { mu.Lock(); defer mu.Unlock(); return len(sent) }
	// A banner line: followed by more output.
	e.Feed([]byte("*** Password: is required for this device ***\r\n"))
	// A prompt at the end, but output goes on before the quiet time ends.
	e.Feed([]byte("Password:"))
	time.Sleep(20 * time.Millisecond)
	e.Feed([]byte(" was changed by admin\r\nrouter#"))
	time.Sleep(150 * time.Millisecond)
	if got() != 0 {
		t.Fatalf("answered a line that was not a prompt: %q", sent)
	}
	// A real prompt.
	e.Feed([]byte("\r\nPassword: "))
	time.Sleep(200 * time.Millisecond)
	if got() != 1 || sent[0] != "secret\r" {
		t.Fatalf("prompt not answered: %q", sent)
	}
}
