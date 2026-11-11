// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yacm-app/yacm/internal/store"
	"github.com/yacm-app/yacm/internal/team"
)

func findNode(a *App, name string) *store.Node {
	for _, n := range a.store.ListAll() {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// A publishes a group, B follows it: B sees read-only sessions that log in
// with B's own profile, can copy them, edit the library and publish; a
// stale editor gets a conflict instead of overwriting.
func TestTeamLibraryFlow(t *testing.T) {
	dir := t.TempDir()
	libPath := filepath.Join(dir, "share", "noc.json")
	os.MkdirAll(filepath.Dir(libPath), 0o755)

	// ---- publisher A
	a := newTestApp(t)
	ad, _ := a.store.UpsertCredential(&store.Credential{Name: "AD account", User: "ivan", Auth: store.AuthPassword})
	a.vault.Set(store.SecretCredPassword(ad.ID), "ivans-secret")
	noc, _ := a.store.Upsert(&store.Node{Name: "NOC", IsGroup: true, Defaults: &store.GroupDefaults{CredentialID: ad.ID, AntiIdle: 240}})
	core, _ := a.store.Upsert(&store.Node{Name: "Core", IsGroup: true, ParentID: noc.ID})
	bastion, _ := a.store.Upsert(&store.Node{Name: "bastion", ParentID: noc.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "192.0.2.10", CredentialID: store.Inherit}})
	r1, _ := a.store.Upsert(&store.Node{Name: "core-r1", ParentID: core.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", CredentialID: store.Inherit, JumpVia: bastion.ID,
		Before: []store.LocalCommand{{Command: "calc.exe"}},
		Expect: []store.ExpectRule{{ID: "x1", Active: true, Expect: "#", Send: "terminal length 0", Return: true}, {ID: "x2", Active: true, Expect: "word:", Hidden: true, Return: true}}}})
	a.store.Upsert(&store.Node{Name: "core-r2", ParentID: core.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.2", Auth: store.AuthPublicKey, KeyPath: `C:\Users\ivan\.ssh\id_ed25519`}})
	st := a.store.Settings()
	st.Snippets = append(st.Snippets, store.Snippet{ID: "s1", Name: "BGP", Folder: "NOC", Text: "show bgp summary", Return: true})
	a.store.SetSettings(st)

	res, err := a.PublishTeamLibrary(PublishRequest{GroupID: noc.ID, Path: libPath, SnippetFolder: "NOC"})
	if err != nil || res.Conflict != "" || res.Revision != 1 || res.Sessions != 3 || res.Logins != 1 {
		t.Fatalf("publish: %v %+v", err, res)
	}
	warn := strings.Join(res.Warnings, "\n")
	for _, w := range []string{"local commands are never shared", "hidden value", "own key file"} {
		if !strings.Contains(warn, w) {
			t.Errorf("warning %q missing in %q", w, warn)
		}
	}
	raw, _ := os.ReadFile(libPath)
	for _, leak := range []string{"ivans-secret", "calc.exe", "id_ed25519", ad.ID + `"`} {
		if strings.Contains(string(raw), leak) && leak != ad.ID+`"` {
			t.Errorf("library file contains %q", leak)
		}
	}
	// The publisher sees their own group, not a second copy.
	if st := a.TeamLibraries(); len(st) != 1 || !st[0].Editing || st[0].SourceGroup != noc.ID {
		t.Fatalf("publisher status %+v", st)
	}
	if findNode(a, "NOC") == nil || a.store.IsLibraryNode(team.RootID(a.store.Settings().TeamLibraries[0].ID)) {
		t.Fatal("publisher should not see the library twice")
	}

	// ---- follower B with a profile of the same name
	b := newTestApp(t)
	bad, _ := b.store.UpsertCredential(&store.Credential{Name: "AD account", User: "kolega", Auth: store.AuthPassword})
	lib, err := b.AddTeamLibrary(libPath)
	if err != nil || lib.Sessions != 3 || len(lib.Logins) != 1 || lib.Logins[0].Linked != bad.ID || lib.Snippets != 1 {
		t.Fatalf("follow: %v %+v", err, lib)
	}
	tr1 := findNode(b, "core-r1")
	if tr1 == nil || tr1.Library != lib.ID || !strings.HasPrefix(tr1.ID, "t"+lib.ID+"-") {
		t.Fatalf("team node %+v", tr1)
	}
	eff, err := b.store.Effective(tr1)
	if err != nil || eff.User != "kolega" || eff.PasswordKey != store.SecretCredPassword(bad.ID) || len(eff.Hops) != 1 || eff.Hops[0].Host != "192.0.2.10" || eff.AntiIdle != 240 {
		t.Fatalf("effective %+v %v", eff, err)
	}
	if len(tr1.Session.Before) != 0 || len(tr1.Session.Expect) != 1 {
		t.Fatalf("library session not cleaned: %+v", tr1.Session)
	}
	if got := b.GetState().TeamSnippets; len(got) != 1 || !strings.HasPrefix(got[0].Folder, "Team: NOC") {
		t.Fatalf("team snippets %+v", got)
	}
	// Read-only for followers.
	tr1.Name = "renamed"
	if _, err := b.SaveNode(*tr1, nil); err == nil {
		t.Fatal("follower changed a library session")
	}
	if err := b.DeleteNode(tr1.ID); err == nil {
		t.Fatal("follower deleted a library session")
	}
	// Unlinking the login: asked when connecting.
	b.LinkTeamLogin(lib.ID, lib.Logins[0].ID, "")
	if eff, _ := b.store.Effective(findNode(b, "core-r1")); eff.Auth != store.AuthAsk || len(eff.Warnings) == 0 {
		t.Fatalf("unlinked login: %+v", eff)
	}
	b.LinkTeamLogin(lib.ID, lib.Logins[0].ID, bad.ID)

	// Copy to my sessions: an editable copy with B's profile.
	top, err := b.CopyToMySessions([]string{findNode(b, "Core").ID})
	if err != nil {
		t.Fatal(err)
	}
	var copyR1 *store.Node
	for _, n := range b.store.List() {
		if n.Name == "core-r1" {
			copyR1 = n
		}
	}
	if copyR1 == nil || copyR1.Library != "" || top == "" {
		t.Fatal("copy missing")
	}
	if eff, err := b.store.Effective(copyR1); err != nil || eff.User != "kolega" || len(eff.Hops) != 1 {
		t.Fatalf("copy effective %+v %v (jump host stays the team session)", eff, err)
	}

	// ---- A changes something and publishes again; B picks it up.
	r1.Name = "core-r1-new"
	a.store.Upsert(r1)
	if res, err := a.PublishTeamLibrary(PublishRequest{GroupID: noc.ID}); err != nil || res.Revision != 2 {
		t.Fatalf("republish: %v %+v", err, res)
	}
	b.refreshTeam(false)
	if findNode(b, "core-r1-new") == nil {
		t.Fatal("follower did not see the new revision")
	}

	// ---- B becomes an editor and publishes revision 3.
	src, err := b.EditTeamLibrary(lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.store.IsLibraryNode(findNode(b, "core-r1-new").ID) {
		t.Fatal("editor still sees the read-only library")
	}
	n2, _ := b.store.Upsert(&store.Node{Name: "core-r3", ParentID: src, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.3", CredentialID: store.Inherit}})
	_ = n2
	if res, err := b.PublishTeamLibrary(PublishRequest{GroupID: src}); err != nil || res.Revision != 3 || res.Conflict != "" {
		t.Fatalf("editor publish: %v %+v", err, res)
	}
	// The login keeps its ID, so A's link still works.
	f, _, _ := team.Load(libPath)
	if len(f.Logins) != 1 || f.Logins[0].ID != ad.ID || f.Sessions() != 4 {
		t.Fatalf("library after B: %+v", f.Logins)
	}
	// A's copy is now behind: publishing reports a conflict and writes nothing.
	res, err = a.PublishTeamLibrary(PublishRequest{GroupID: noc.ID})
	if err != nil || res.Conflict == "" {
		t.Fatalf("stale publish: %v %+v", err, res)
	}
	if f, _, _ := team.Load(libPath); f.Revision != 3 {
		t.Fatal("stale publish wrote the file")
	}
	baks, _ := filepath.Glob(filepath.Join(filepath.Dir(libPath), ".yacm-backup", "*.json"))
	if len(baks) != 2 {
		t.Fatalf("backups: %v", baks)
	}

	// ---- the share goes away: the cached copy is shown, marked stale.
	c := newTestApp(t)
	clib, _ := c.AddTeamLibrary(libPath)
	os.Rename(libPath, libPath+".off")
	c.refreshTeam(true)
	cs := c.TeamLibraries()
	if len(cs) != 1 || !cs[0].Stale || cs[0].Sessions != 4 || findNode(c, "core-r3") == nil {
		t.Fatalf("stale library: %+v", cs)
	}
	c2, err := NewApp(c.dataDir) // a restart while the share is still away
	if err != nil {
		t.Fatal(err)
	}
	if st := c2.TeamLibraries(); len(st) != 1 || !st[0].Stale || findNode(c2, "core-r3") == nil {
		t.Fatalf("after restart: %+v", st)
	}
	_ = clib
}
