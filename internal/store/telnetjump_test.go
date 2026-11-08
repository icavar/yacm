// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"path/filepath"
	"testing"
)

func TestTelnetThroughJumpHost(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	jump, _ := s.Upsert(&Node{Name: "bastion", Session: &Session{Protocol: ProtoSSH, Host: "bastion.example.net", User: "ivan"}})
	g, _ := s.Upsert(&Node{Name: "OOB", IsGroup: true, Defaults: &GroupDefaults{JumpVia: jump.ID}})
	tel, err := s.Upsert(&Node{Name: "old-sw", ParentID: g.ID, Session: &Session{Protocol: ProtoTelnet, Host: "10.1.1.1", JumpVia: Inherit}})
	if err != nil {
		t.Fatal(err)
	}
	if tel.Session.JumpVia != Inherit {
		t.Fatal("a telnet session keeps its jump host")
	}
	e, err := s.Effective(tel)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Hops) != 1 || e.Hops[0].Host != "bastion.example.net" || e.Hops[0].User != "ivan" {
		t.Fatalf("hops %+v", e.Hops)
	}
	inline, _ := s.Upsert(&Node{Name: "old-sw2", Session: &Session{Protocol: ProtoTelnet, Host: "10.1.1.2", Jump: []JumpHost{{Host: "j1"}}}})
	if e, _ := s.Effective(inline); len(e.Hops) != 1 || e.Hops[0].Port != 22 {
		t.Fatalf("inline hop %+v", e.Hops)
	}
	ser, _ := s.Upsert(&Node{Name: "console", Session: &Session{Protocol: ProtoSerial, Serial: &Serial{Device: "COM3"}, JumpVia: jump.ID}})
	if ser.Session.JumpVia != "" {
		t.Fatal("serial sessions have no jump host")
	}
	// Bulk edit sets the jump host on telnet sessions too.
	none := ""
	if _, err := s.BulkUpdate([]string{tel.ID}, BulkChange{JumpVia: &none}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Get(tel.ID); n.Session.JumpVia != "" {
		t.Fatal("bulk edit did not change the telnet jump host")
	}
}
