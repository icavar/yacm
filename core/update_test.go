// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckForUpdate(t *testing.T) {
	body := `[
		{"tag_name":"v0.9.1-rc.1","html_url":"https://x/rc","prerelease":true},
		{"tag_name":"v0.9.0","html_url":"https://x/090","published_at":"2026-11-10T10:00:00Z"},
		{"tag_name":"v1.0.0","html_url":"https://x/draft","draft":true},
		{"tag_name":"v0.9.0-rc.2","html_url":"https://x/rc2","prerelease":true},
		{"tag_name":"nightly","html_url":"https://x/n"}
	]`
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Write([]byte(body))
	}))
	defer srv.Close()
	ctx := context.Background()
	cases := []struct {
		cur, latest string
		newer       bool
	}{
		{"0.8.11", "0.9.0", true},          // final users see finals only
		{"0.9.0", "0.9.0", false},          // up to date
		{"0.9.0-rc.1", "0.9.1-rc.1", true}, // candidates see candidates too
		{"0.9.2", "0.9.0", false},
	}
	for _, c := range cases {
		info, err := checkForUpdate(ctx, srv.Client(), srv.URL, c.cur)
		if err != nil {
			t.Fatal(err)
		}
		if info.Latest != c.latest || info.Newer != c.newer {
			t.Errorf("%s: got %+v", c.cur, info)
		}
	}
	if ua != "YACM/0.9.2" {
		t.Errorf("user agent %q", ua)
	}
	srv404 := httptest.NewServer(http.NotFoundHandler())
	defer srv404.Close()
	if _, err := checkForUpdate(ctx, srv404.Client(), srv404.URL, "0.9.0"); err == nil {
		t.Error("404 without an error")
	}
}

func TestVersionOrder(t *testing.T) {
	order := []string{"0.8.11", "0.9.0-rc.1", "0.9.0-rc.2", "0.9.0-rc.10", "0.9.0", "0.10.0", "1.0.0"}
	for i := 1; i < len(order); i++ {
		a, _ := parseVersion(order[i-1])
		b, _ := parseVersion(order[i])
		if a.compare(b) >= 0 {
			t.Errorf("%s should come before %s", order[i-1], order[i])
		}
	}
}
