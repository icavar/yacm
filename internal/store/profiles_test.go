// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustUpsert(t *testing.T, s *Store, n *Node) *Node {
	t.Helper()
	out, err := s.Upsert(n)
	if err != nil {
		t.Fatalf("upsert %s: %v", n.Name, err)
	}
	return out
}

func sshNode(name, parent, host string) *Node {
	return &Node{Name: name, ParentID: parent, Session: &Session{Protocol: ProtoSSH, Host: host, User: "own-" + name, Auth: AuthPassword}}
}

func TestEffectiveInheritance(t *testing.T) {
	s := newTestStore(t)
	ad, err := s.UpsertCredential(&Credential{Name: "AD account", User: "ivan", Auth: AuthPassword})
	if err != nil {
		t.Fatal(err)
	}
	local, _ := s.UpsertCredential(&Credential{Name: "Local admin", User: "admin"})

	bastion := mustUpsert(t, s, sshNode("bastion-zg", "", "192.0.2.1"))
	bastion.Session.CredentialID = ad.ID
	bastion.Session.Algorithms = AlgoSecure
	bastion = mustUpsert(t, s, bastion)

	dc := mustUpsert(t, s, &Node{Name: "DC Zagreb", IsGroup: true, Color: "red",
		Defaults: &GroupDefaults{CredentialID: ad.ID, JumpVia: bastion.ID, Algorithms: AlgoCompatible}})
	core := mustUpsert(t, s, &Node{Name: "Core", IsGroup: true, ParentID: dc.ID})

	sw := sshNode("sw1", core.ID, "10.0.0.1")
	sw.Session.CredentialID, sw.Session.JumpVia, sw.Session.Algorithms = Inherit, Inherit, Inherit
	sw = mustUpsert(t, s, sw)

	e, err := s.Effective(sw)
	if err != nil {
		t.Fatal(err)
	}
	if e.User != "ivan" || e.PasswordKey != SecretCredPassword(ad.ID) || e.Credential != "AD account" {
		t.Fatalf("credential not inherited: %+v", e)
	}
	if e.Algorithms != AlgoCompatible {
		t.Fatalf("algorithms = %s", e.Algorithms)
	}
	if e.Color != "red" {
		t.Fatalf("colour = %q", e.Color)
	}
	if len(e.Hops) != 1 || e.Hops[0].Host != "192.0.2.1" || e.Hops[0].User != "ivan" || e.Hops[0].Algorithms != AlgoSecure {
		t.Fatalf("jump hops = %+v", e.Hops)
	}

	// The bastion itself is inside no group; even if a group gave it its own
	// ID as jump host, it would not jump through itself.
	own := sshNode("router", dc.ID, "10.0.0.9")
	own.Session.CredentialID = local.ID
	own = mustUpsert(t, s, own)
	e, _ = s.Effective(own)
	if e.User != "admin" || len(e.Hops) != 0 || e.Algorithms != AlgoSecure {
		t.Fatalf("own settings must win over the group: %+v", e)
	}

	// A session without "inherit" keeps its own login.
	plain := mustUpsert(t, s, sshNode("plain", core.ID, "10.0.0.2"))
	e, _ = s.Effective(plain)
	if e.User != "own-plain" || e.PasswordKey != SecretPassword(plain.ID) {
		t.Fatalf("own login expected: %+v", e)
	}
}

func TestJumpChainAndLoop(t *testing.T) {
	s := newTestStore(t)
	a := mustUpsert(t, s, sshNode("a", "", "198.51.100.1"))
	b := sshNode("b", "", "198.51.100.2")
	b.Session.JumpVia = a.ID
	b = mustUpsert(t, s, b)
	c := sshNode("c", "", "10.1.1.1")
	c.Session.JumpVia = b.ID
	c = mustUpsert(t, s, c)
	e, err := s.Effective(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Hops) != 2 || e.Hops[0].Name != "a" || e.Hops[1].Name != "b" {
		t.Fatalf("chain = %+v", e.Hops)
	}
	// a -> c makes a loop.
	a.Session.JumpVia = c.ID
	mustUpsert(t, s, a)
	if _, err := s.Effective(c); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("expected loop error, got %v", err)
	}
	// Deleting a jump host clears references to it.
	if _, err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	cc, _ := s.Get(c.ID)
	if cc.Session.JumpVia != "" {
		t.Fatal("jump reference to a deleted session must be cleared")
	}
}

func TestDeleteCredentialKeepsSessionsUsable(t *testing.T) {
	s := newTestStore(t)
	cr, _ := s.UpsertCredential(&Credential{Name: "Shared", User: "netops"})
	n := sshNode("r1", "", "10.0.0.1")
	n.Session.CredentialID = cr.ID
	n = mustUpsert(t, s, n)
	g := mustUpsert(t, s, &Node{Name: "G", IsGroup: true, Defaults: &GroupDefaults{CredentialID: cr.ID}})
	if ses, grp := s.CredentialUsage(cr.ID); ses != 1 || grp != 1 {
		t.Fatalf("usage = %d sessions %d groups", ses, grp)
	}
	if err := s.DeleteCredential(cr.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(n.ID)
	if got.Session.CredentialID != "" || got.Session.User != "netops" || got.Session.Auth != AuthAsk {
		t.Fatalf("session after delete: %+v", got.Session)
	}
	gg, _ := s.Get(g.ID)
	if gg.Defaults != nil {
		t.Fatal("empty group defaults must be removed")
	}
	if _, err := s.UpsertCredential(&Credential{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertCredential(&Credential{Name: "X"}); err == nil {
		t.Fatal("duplicate profile names must be refused")
	}
}

func TestBulkUpdate(t *testing.T) {
	s := newTestStore(t)
	g := mustUpsert(t, s, &Node{Name: "Lab", IsGroup: true})
	sub := mustUpsert(t, s, &Node{Name: "Sub", IsGroup: true, ParentID: g.ID})
	a := mustUpsert(t, s, sshNode("a", g.ID, "10.0.0.1"))
	b := mustUpsert(t, s, sshNode("b", sub.ID, "10.0.0.2"))
	tel := mustUpsert(t, s, &Node{Name: "t", ParentID: g.ID, Session: &Session{Protocol: ProtoTelnet, Host: "10.0.0.3"}})
	other := mustUpsert(t, s, sshNode("other", "", "10.9.9.9"))
	cr, _ := s.UpsertCredential(&Credential{Name: "AD", User: "ivan"})

	cid, algo, port, red := cr.ID, string(AlgoLegacy), 2222, "red"
	ts := true
	changed, err := s.BulkUpdate([]string{g.ID}, BulkChange{CredentialID: &cid, Algorithms: &algo, Port: &port, Color: &red, LogTimestamps: &ts, AddClusters: []string{"LAB"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 3 {
		t.Fatalf("changed %d sessions, want 3", len(changed))
	}
	ga, _ := s.Get(a.ID)
	gb, _ := s.Get(b.ID)
	gt, _ := s.Get(tel.ID)
	go_, _ := s.Get(other.ID)
	gg, _ := s.Get(g.ID)
	if ga.Session.CredentialID != cr.ID || gb.Session.Algorithms != AlgoLegacy || gb.Session.Port != 2222 || !gb.Session.LogTimestamps {
		t.Fatalf("ssh sessions not changed: %+v %+v", ga.Session, gb.Session)
	}
	if gt.Session.Algorithms != "" || gt.Session.Port != 2222 || gt.Session.CredentialID != cr.ID {
		t.Fatalf("telnet: algorithms must stay, port and profile change: %+v", gt.Session)
	}
	if gg.Color != "red" || ga.Color != "" {
		t.Fatal("colour applies to the selected group, sessions inherit it")
	}
	if go_.Session.CredentialID != "" {
		t.Fatal("sessions outside the selection must not change")
	}
	if len(ga.Session.Clusters) != 1 || ga.Session.Clusters[0] != "LAB" {
		t.Fatalf("clusters = %v", ga.Session.Clusters)
	}
	// Moving a group into its own subgroup is refused.
	if _, err := s.BulkUpdate([]string{g.ID}, BulkChange{ParentID: &sub.ID}); err == nil {
		t.Fatal("moving a group into itself must fail")
	}
	bad := "pink"
	if _, err := s.BulkUpdate([]string{a.ID}, BulkChange{Color: &bad}); err == nil {
		t.Fatal("unknown colour must fail")
	}
}

func TestOpenVersion1TurnsOnHighlighting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sessions.json")
	v1 := `{"version":1,"settings":{"fontFamily":"x","fontSize":13,"scrollback":5000,"theme":"dark"},"nodes":[]}`
	if err := os.WriteFile(p, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	st := s.Settings()
	if !st.Highlight || len(st.HighlightRules) == 0 || len(st.Snippets) == 0 || st.FontSize != 13 {
		t.Fatalf("migration: %+v", st)
	}
	// Removing all rules sticks.
	st.HighlightRules = []HighlightRule{}
	if err := s.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(p)
	if len(s2.Settings().HighlightRules) != 0 {
		t.Fatal("an emptied rule list must stay empty")
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `"version": 2`) {
		t.Fatal("file must be written as version 2")
	}
}
