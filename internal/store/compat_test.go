// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sessions.json as a future YACM might write it: a new top-level section,
// new members on settings, profiles, groups and sessions, and a session of a
// connection type this version does not know.
const futureFile = `{
  "version": 2,
  "rdpGateways": [{"id": "gw1", "host": "rdgw.example.com"}],
  "settings": {"fontSize": 15, "scrollback": 5000, "theme": "dark", "highlight": true,
    "highlightRules": [], "snippets": [], "futureSetting": {"on": true}},
  "credentials": [{"id": "c1", "name": "AD", "user": "ivan", "auth": "password", "domain": "CORP"}],
  "nodes": [
    {"id": "g1", "parentId": "", "isGroup": true, "name": "DC", "order": 0, "defaults": {"credentialId": "c1", "rdpGateway": "gw1"}, "pinned": true},
    {"id": "s1", "parentId": "g1", "isGroup": false, "name": "core-sw1", "order": 0,
      "session": {"protocol": "ssh", "host": "10.0.0.1", "port": 22, "auth": "password", "algorithms": "secure",
        "jumpVia": "", "x11": true, "serial": null}},
    {"id": "s2", "parentId": "g1", "isGroup": false, "name": "jump-win", "order": 1,
      "session": {"protocol": "rdp", "host": "10.0.0.50", "port": 3389, "credentialId": "c1", "jumpVia": "s1",
        "rdp": {"width": 1920, "height": 1080, "gateway": "gw1"}}}
  ]
}`

func TestFutureMembersSurviveLoadAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(path, []byte(futureFile), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rdp, err := s.Get("s2")
	if err != nil {
		t.Fatalf("the session of an unknown type must be kept: %v", err)
	}
	if KnownProtocol(rdp.Session.Protocol) {
		t.Fatal("rdp is not a known protocol in this version")
	}

	// The UI sends an SSH session back without the members it does not know
	// (it builds its own copy); renaming it must not drop them.
	ssh, _ := s.Get("s1")
	data, _ := json.Marshal(ssh)
	var fromUI Node
	_ = json.Unmarshal(data, &fromUI)
	fromUI.Extra, fromUI.Session.Extra = nil, nil
	fromUI.Name = "core-sw1-renamed"
	if _, err := s.Upsert(&fromUI); err != nil {
		t.Fatal(err)
	}
	// Renaming the unknown-type session keeps everything too.
	rdp.Name = "jump-win-2"
	if _, err := s.Upsert(rdp); err != nil {
		t.Fatalf("an unknown-type session can still be renamed or moved: %v", err)
	}
	st := s.Settings()
	st.Extra = nil
	st.FontSize = 16
	if err := s.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Credential("c1")
	c.Extra = nil
	c.Description = "changed"
	if _, err := s.UpsertCredential(c); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	out := string(raw)
	for _, want := range []string{`"rdpGateways"`, `"futureSetting"`, `"domain": "CORP"`, `"rdpGateway": "gw1"`, `"pinned": true`, `"x11": true`, `"protocol": "rdp"`, `"jumpVia": "s1"`, `"gateway": "gw1"`, `core-sw1-renamed`, `jump-win-2`, `"fontSize": 16`} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %s after saving:\n%s", want, out)
		}
	}

	// And it loads again.
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
}

func TestValidateUnknownProtocol(t *testing.T) {
	n := &Node{Name: "x", Session: &Session{Protocol: "rdp", JumpVia: "j1", PasteDelay: 99999}}
	if err := Validate(n); err != nil {
		t.Fatal(err)
	}
	if n.Session.JumpVia != "j1" || n.Session.PasteDelay != 99999 {
		t.Fatal("an unknown-type session must be kept exactly as it is")
	}
	if err := Validate(&Node{Name: "x", Session: &Session{}}); err == nil {
		t.Fatal("an empty connection type is an error")
	}
}

func TestBulkUpdateLeavesUnknownTypesAlone(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := s.Upsert(&Node{Name: "g", IsGroup: true})
	rdp := &Node{Name: "w", ParentID: g.ID, Session: &Session{Protocol: "rdp", User: "admin", Port: 3389}}
	rdp, _ = s.Upsert(rdp)
	user, port := "other", 2222
	if _, err := s.BulkUpdate([]string{g.ID}, BulkChange{User: &user, Port: &port, AddClusters: []string{"WIN"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(rdp.ID)
	if got.Session.User != "admin" || got.Session.Port != 3389 {
		t.Fatalf("bulk edit changed an unknown-type session: %+v", got.Session)
	}
	if len(got.Session.Clusters) != 1 {
		t.Fatal("clusters are generic and may be changed")
	}
}

func TestDropAndHasUnknown(t *testing.T) {
	var n Node
	_ = json.Unmarshal([]byte(`{"id":"a","name":"a","session":{"protocol":"ssh","host":"h","before":[{"command":"x","confirm":true,"shell":"pwsh"}],"newThing":1}}`), &n)
	if !HasUnknown(&n) {
		t.Fatal("HasUnknown should see the new members")
	}
	DropUnknown(&n)
	if HasUnknown(&n) {
		t.Fatal("DropUnknown should remove them all")
	}
	if HasUnknown(&Node{Name: "r", Session: &Session{Protocol: "rdp"}}) != true {
		t.Fatal("an unknown connection type counts as newer")
	}
}
