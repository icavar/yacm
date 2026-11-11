// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package team

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yacm-app/yacm/internal/store"
)

// A hand-edited (or malicious) library cannot make YACM run commands, use
// someone's key files or send hidden values, and bad references are dropped.
func TestParseCleansLibrary(t *testing.T) {
	data := `{"format":"yacm-team-library","version":1,"name":"Lab","revision":4,
	"logins":[{"id":"ad","name":"AD account"}],
	"defaults":{"credentialId":"slot:nope","jumpVia":"missing"},
	"nodes":[
	 {"id":"g1","isGroup":true,"name":"Core"},
	 {"id":"s1","parentId":"g1","name":"r1","session":{"protocol":"ssh","host":"10.0.0.1","credentialId":"slot:ad",
	   "keyPath":"C:/Users/x/id","before":[{"command":"rm -rf /"}],"after":[{"command":"x"}],"logDir":"\\\\srv\\logs",
	   "jumpVia":"gone","expect":[{"id":"e1","active":true,"expect":"word:","hidden":true},{"id":"e2","active":true,"expect":"#","send":"term len 0"}]}},
	 {"id":"s2","parentId":"nope","name":"r2","session":{"protocol":"telnet","host":"10.0.0.2","credentialId":"some-local-profile-id"}},
	 {"id":"s3","name":"r3","session":{"protocol":"ssh","host":"10.0.0.3","agentForward":true,
	   "proxy":{"type":"http","host":"203.0.113.9","port":3128,"credentialId":"some-local-profile-id"}}}
	]}`
	f, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	s1, s2 := f.Nodes[1].Session, f.Nodes[2].Session
	if len(s1.Before)+len(s1.After) != 0 || s1.KeyPath != "" || s1.LogDir != "" || s1.JumpVia != "" || len(s1.Expect) != 1 || s1.CredentialID != "slot:ad" {
		t.Fatalf("s1 not cleaned: %+v", s1)
	}
	if s2.CredentialID != "" || f.Nodes[2].ParentID != "" {
		t.Fatalf("s2: %+v parent %q", s2, f.Nodes[2].ParentID)
	}
	if s3 := f.Nodes[3].Session; s3.AgentForward || s3.Proxy == nil || s3.Proxy.CredentialID != "" {
		t.Fatalf("s3 keeps agent forwarding or a proxy login: %+v %+v", s3, s3.Proxy)
	}
	if f.Defaults.CredentialID != "" || f.Defaults.JumpVia != "" {
		t.Fatalf("defaults %+v", f.Defaults)
	}
	v := f.View("abc", map[string]string{"ad": "cred1"}, 5)
	if v.Nodes[0].ID != "tabc" || v.Nodes[1].ParentID != "tabc" || v.Nodes[2].ID != "tabc-s1" || v.Nodes[2].ParentID != "tabc-g1" || v.Slots["ad"] != "cred1" {
		t.Fatalf("view ids: %+v %+v %+v", v.Nodes[0], v.Nodes[1], v.Nodes[2])
	}
	for _, bad := range []string{`{}`, `{"format":"other"}`, `{"format":"yacm-team-library","version":99}`, `not json`,
		`{"format":"yacm-team-library","nodes":[{"id":"a","name":"x","isGroup":true},{"id":"a","name":"y","isGroup":true}]}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestSaveLockAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib.json")
	f := &File{Name: "Lab", Logins: []Login{}, Nodes: []*store.Node{{ID: "s", Name: "r1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "h"}}}}
	if rev, err := Save(path, f, -1, "ivan@pc"); err != nil || rev != 1 {
		t.Fatalf("first save %d %v", rev, err)
	}
	if rev, err := Save(path, f, 1, "ivan@pc"); err != nil || rev != 2 {
		t.Fatalf("second save %d %v", rev, err)
	}
	var changed *ErrChanged
	if _, err := Save(path, f, 1, "kolega@pc2"); !errors.As(err, &changed) || changed.By != "ivan@pc" {
		t.Fatalf("stale save: %v", err)
	}
	os.WriteFile(path+".lock", []byte("kolega@pc2 at now"), 0o644)
	var busy *ErrBusy
	if _, err := Save(path, f, 2, "ivan@pc"); !errors.As(err, &busy) || !strings.Contains(busy.By, "kolega") {
		t.Fatalf("locked save: %v", err)
	}
	os.Remove(path + ".lock")
	got, _, err := Load(path)
	if err != nil || got.Revision != 2 || got.UpdatedBy != "ivan@pc" {
		t.Fatalf("load %+v %v", got, err)
	}
}
