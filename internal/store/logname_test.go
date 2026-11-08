// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogFileName(t *testing.T) {
	at := time.Date(2026, 10, 28, 13, 35, 1, 0, time.UTC)
	v := LogNameValues{Name: "core-sw1", Host: "10.0.0.1", Protocol: "ssh", User: "ivan", Group: "Zagreb", Path: "DC/Zagreb", Time: at}
	cases := []struct{ tmpl, want string }{
		{"", "core-sw1_20261028-133501.log"},
		{DefaultLogNameTemplate, "Zagreb/core-sw1_2026-10-28_13-35-01.log"},
		{"{path}/{date}/{name}", "DC/Zagreb/2026-10-28/core-sw1.log"},
		{"{ticket}_{name}_{date}", "core-sw1_2026-10-28.log"},
		{"{name}-{ticket}.txt", "core-sw1.txt"},
		{"{user}@{host} {protocol}", "ivan@10.0.0.1 ssh.log"},
		{"../../{name}", "core-sw1.log"},
		{"logs/./{name}?*:", "logs/core-sw1_.log"},
		{"{name}{unknown}", "core-sw1{unknown}.log"},
		{"con", "_con.log"},
		{"{ticket}", "core-sw1.log"},
	}
	for _, c := range cases {
		if got := LogFileName(c.tmpl, v); got != c.want {
			t.Errorf("%q: got %q, want %q", c.tmpl, got, c.want)
		}
	}
	v.Ticket = "CHG0012345"
	if got := LogFileName("{ticket}_{name}_{date}", v); got != "CHG0012345_core-sw1_2026-10-28.log" {
		t.Errorf("ticket: %q", got)
	}
	v.Name, v.Group, v.Path = `a/b\c:d`, "", ""
	if got := LogFileName("{group}/{name}", v); got != "a_b_c_d.log" {
		t.Errorf("slashes in values are not folders: %q", got)
	}
}

func TestCreateLogFileNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 3; i++ {
		f, p, _, err := CreateLogFile(dir, "Zagreb/core-sw1.log")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		paths = append(paths, filepath.Base(p))
	}
	want := []string{"core-sw1.log", "core-sw1-2.log", "core-sw1-3.log"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("got %v, want %v", paths, want)
		}
	}
	if _, _, _, err := CreateLogFile(dir, "../x.log"); err == nil {
		t.Fatal("a name outside the log folder must be refused")
	}
}

func TestCreateLogFileShortensLongPaths(t *testing.T) {
	t.Setenv("YACM_TEST_SHORT_PATHS", "1")
	dir := t.TempDir()
	f, p, short, err := CreateLogFile(dir, strings.Repeat("x", 300)+".log")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !short || len(p) > maxLogPath || filepath.Ext(p) != ".log" {
		t.Fatalf("not shortened: %d %v %s", len(p), short, filepath.Ext(p))
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestLogNameTemplateChoice(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := s.Upsert(&Node{Name: "Prod", IsGroup: true, Defaults: &GroupDefaults{LogName: "{ticket}_{name}"}})
	n, _ := s.Upsert(&Node{Name: "r1", ParentID: g.ID, Session: &Session{Protocol: ProtoSSH, Host: "h"}})
	if got := s.LogNameTemplate(n); got != LegacyLogName {
		t.Fatalf("templates off: %q", got)
	}
	st := s.Settings()
	st.LogNameTemplateOn = true
	_ = s.SetSettings(st)
	if got := s.LogNameTemplate(n); got != DefaultLogNameTemplate {
		t.Fatalf("on without a template of its own: %q", got)
	}
	st.LogNameOwn, st.LogNameTemplate = true, "{name}"
	_ = s.SetSettings(st)
	if got := s.LogNameTemplate(n); got != "{ticket}_{name}" {
		t.Fatalf("group template: %q", got)
	}
	n.Session.LogName = "{host}"
	n, _ = s.Upsert(n)
	if got := s.LogNameTemplate(n); got != "{host}" {
		t.Fatalf("session template: %q", got)
	}
	if p := s.GroupPath(n); len(p) != 1 || p[0] != "Prod" {
		t.Fatalf("group path %v", p)
	}
}
