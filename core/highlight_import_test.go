// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"strings"
	"testing"

	"github.com/yacm-app/yacm/internal/store"
)

// Highlight rules from a file cannot freeze the window.
func TestSafeHighlightRules(t *testing.T) {
	in := []store.HighlightRule{
		{Pattern: `\b(down|err-disabled)\b`},
		{Pattern: `(a+)+$`},
		{Pattern: `(.*)*x`},
		{Pattern: `(\d{1,3}\.){3}\d{1,3}`}, // an IP address: one quantifier inside, fine
		{Pattern: strings.Repeat("a", 400)},
	}
	out := safeHighlightRules(in)
	if len(out) != 2 || out[0].Pattern != in[0].Pattern || out[1].Pattern != in[3].Pattern {
		t.Fatalf("kept %+v", out)
	}
}
