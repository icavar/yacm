// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icavar/yacm/internal/store"
)

// A follower is warned before logging in to a team session whose address a
// publisher changed, and sees the list of changes when the library updates.
func TestTeamTargetChange(t *testing.T) {
	libPath := filepath.Join(t.TempDir(), "noc.json")
	pub := newTestApp(t)
	noc, _ := pub.store.Upsert(&store.Node{Name: "NOC", IsGroup: true})
	r1, _ := pub.store.Upsert(&store.Node{Name: "core-r1", ParentID: noc.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22}})
	pub.store.Upsert(&store.Node{Name: "core-r2", ParentID: noc.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.2", Port: 22}})
	if _, err := pub.PublishTeamLibrary(PublishRequest{GroupID: noc.ID, Path: libPath}); err != nil {
		t.Fatal(err)
	}
	// The publisher is not told about its own changes.
	if d := pub.TakeTeamChanges(); len(d) != 0 {
		t.Fatalf("publisher got its own changes: %+v", d)
	}

	fol := newTestApp(t)
	if _, err := fol.AddTeamLibrary(libPath); err != nil {
		t.Fatal(err)
	}
	var asked []map[string]any
	answer := true
	fol.emitter = func(name string, data any) {
		if name == "prompt" {
			p := data.(map[string]any)
			asked = append(asked, p)
			go fol.AnswerPrompt(p["id"].(string), PromptAnswer{Accept: answer})
		}
	}
	node := findNode(fol, "core-r1")
	tb := &tab{id: "t1", node: node}
	eff, _ := fol.store.Effective(node)
	tb.eff = eff
	// First connection: nothing known yet, no question; remembered after it.
	if !fol.checkTeamTarget(context.Background(), tb, eff) || len(asked) != 0 {
		t.Fatal("asked on the first connection")
	}
	fol.rememberTeamTarget(tb)

	// The publisher points core-r1 elsewhere and adds a session.
	r1.Session.Host = "203.0.113.66"
	pub.store.Upsert(r1)
	pub.store.Upsert(&store.Node{Name: "core-r3", ParentID: noc.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.3", Port: 22}})
	time.Sleep(10 * time.Millisecond) // a different file time
	if _, err := pub.PublishTeamLibrary(PublishRequest{GroupID: noc.ID, Path: libPath}); err != nil {
		t.Fatal(err)
	}
	fol.refreshTeam(false)
	diffs := fol.TakeTeamChanges()
	if len(diffs) != 1 || len(diffs[0].Added) != 1 || diffs[0].Added[0].Name != "core-r3" || len(diffs[0].Moved) != 1 ||
		!strings.Contains(diffs[0].Moved[0].Before, "10.0.0.1") || !strings.Contains(diffs[0].Moved[0].Now, "203.0.113.66") {
		t.Fatalf("diff: %+v", diffs)
	}

	// Connecting now asks first; cancelling stops the connection.
	node = findNode(fol, "core-r1")
	eff, _ = fol.store.Effective(node)
	tb = &tab{id: "t2", node: node, eff: eff}
	answer = false
	if fol.checkTeamTarget(context.Background(), tb, eff) {
		t.Fatal("connected to the changed address although the user cancelled")
	}
	if len(asked) != 1 || asked[0]["kind"] != "teamchange" {
		t.Fatalf("prompts: %+v", asked)
	}
	ch := asked[0]["change"].(map[string]any)
	if !strings.Contains(ch["before"].(string), "10.0.0.1") || !strings.Contains(ch["now"].(string), "203.0.113.66") || ch["library"] != "NOC" || ch["updatedBy"] == "" {
		t.Fatalf("change shown: %+v", ch)
	}
	// Accepting connects and remembers the new address: no question next time.
	answer = true
	if !fol.checkTeamTarget(context.Background(), tb, eff) {
		t.Fatal("accepted change refused")
	}
	fol.rememberTeamTarget(tb)
	if !fol.checkTeamTarget(context.Background(), tb, eff) || len(asked) != 2 {
		t.Fatalf("asked again after accepting: %d", len(asked))
	}

	// Changes made while YACM was closed are listed at the next start (the
	// cached copy is what was seen last).
	r1.Session.Port = 2222
	pub.store.Upsert(r1)
	if _, err := pub.PublishTeamLibrary(PublishRequest{GroupID: noc.ID, Path: libPath}); err != nil {
		t.Fatal(err)
	}
	again, err := NewApp(fol.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	d := again.TakeTeamChanges()
	if len(d) != 1 || len(d[0].Moved) != 1 || !strings.Contains(d[0].Moved[0].Now, ":2222") {
		t.Fatalf("changes since last start: %+v", d)
	}
	if _, err := os.Stat(filepath.Join(fol.dataDir, "team-seen.json")); err != nil {
		t.Fatal("team-seen.json not written")
	}
}
