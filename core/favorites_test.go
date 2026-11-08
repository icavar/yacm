// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"reflect"
	"testing"
)

func TestFavorites(t *testing.T) {
	a := newTestApp(t)
	if got := a.Favorites(); len(got) != 0 {
		t.Fatalf("no favourites yet: %v", got)
	}
	a.SetFavorite("s1", true)
	a.SetFavorite("s2", true)
	a.SetFavorite("s1", true) // again: no duplicate
	if got := a.Favorites(); !reflect.DeepEqual(got, []string{"s2", "s1"}) {
		t.Fatalf("got %v", got)
	}
	if got, _ := a.SetFavorite("s2", false); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("remove: %v", got)
	}
}
