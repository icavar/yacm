// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package team

import (
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

// Publishing a group never takes personal or secret parts along.
func TestBuildLeavesOutSecrets(t *testing.T) {
	root := &store.Node{ID: "g", Name: "DC", IsGroup: true, Defaults: &store.GroupDefaults{CredentialID: "c1", JumpVia: "outside"}}
	nodes := []*store.Node{
		root,
		{ID: "j", ParentID: "g", Name: "bastion", Order: 0, Session: &store.Session{Protocol: store.ProtoSSH, Host: "b", CredentialID: "c1"}},
		{ID: "s", ParentID: "g", Name: "core", Order: 1, Session: &store.Session{
			Protocol: store.ProtoSSH, Host: "10.0.0.1", Auth: store.AuthPublicKey, KeyPath: "/home/ivan/.ssh/id_ed25519", LogDir: "/home/ivan/logs",
			JumpVia: "j", Before: []store.LocalCommand{{Command: "rm -rf ~"}},
			Expect: []store.ExpectRule{{ID: "r1", Expect: "Password:", Hidden: true}, {ID: "r2", Expect: ">", Send: "enable"}},
		}},
		{ID: "x", ParentID: "", Name: "outside", Session: &store.Session{Protocol: store.ProtoSSH, Host: "o"}},
	}
	creds := map[string]*store.Credential{"c1": {ID: "c1", Name: "TACACS", User: "ivan", Auth: store.AuthPassword}}
	f, warn := Build(BuildInput{Root: root, Nodes: nodes, Credentials: creds})
	if f.Name != "DC" || len(f.Nodes) != 2 {
		t.Fatalf("file %+v", f)
	}
	core := f.Nodes[1].Session
	if core.KeyPath != "" || core.LogDir != "" || len(core.Before) != 0 || core.Auth != store.AuthAsk {
		t.Fatalf("personal parts were published: %+v", core)
	}
	if len(core.Expect) != 1 || core.Expect[0].ID != "r2" {
		t.Fatalf("a hidden automation value was published: %+v", core.Expect)
	}
	if core.JumpVia != "j" {
		t.Fatal("a jump host inside the group stays")
	}
	if f.Defaults == nil || f.Defaults.CredentialID != store.SlotPrefix+"c1" || f.Defaults.JumpVia != "" {
		t.Fatalf("group defaults %+v", f.Defaults)
	}
	if len(f.Logins) != 1 || f.Logins[0].Name != "TACACS" {
		t.Fatalf("profiles become logins without user names: %+v", f.Logins)
	}
	all := strings.Join(warn, "\n")
	for _, want := range []string{"local commands", "hidden value", "own key file", "not inside this group"} {
		if !strings.Contains(all, want) {
			t.Errorf("no warning about %q in %q", want, all)
		}
	}
}
