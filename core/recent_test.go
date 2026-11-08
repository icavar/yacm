// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"fmt"
	"reflect"
	"testing"
)

func TestAddRecent(t *testing.T) {
	ids := addRecent(nil, "a")
	ids = addRecent(ids, "b")
	ids = addRecent(ids, "a")
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("got %v", ids)
	}
	for i := 0; i < 30; i++ {
		ids = addRecent(ids, fmt.Sprint(i))
	}
	if len(ids) != maxRecent || ids[0] != "29" {
		t.Fatalf("the list keeps the newest %d: %v", maxRecent, ids)
	}
}
