// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/yacm-app/yacm/internal/store"
)

// buildLoadNodes makes groups and sessions shaped like a big NOC: sites,
// roles, devices, a few credential profiles and jump hosts.
func buildLoadNodes(groups, sessions int) []*store.Node {
	var out []*store.Node
	gids := make([]string, 0, groups)
	for i := 0; i < groups; i++ {
		parent := ""
		if i >= 30 {
			parent = gids[i%30] // 30 sites with sub-groups below them
		}
		id := fmt.Sprintf("g%04d", i)
		gids = append(gids, id)
		out = append(out, &store.Node{ID: id, ParentID: parent, Name: fmt.Sprintf("Group %d", i), IsGroup: true, Order: i})
	}
	for i := 0; i < sessions; i++ {
		ss := &store.Session{Protocol: store.ProtoSSH, Host: fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256), Port: 22, User: "admin",
			Clusters: []string{fmt.Sprintf("C%d", i%20)}}
		if i%10 == 0 {
			ss.JumpVia = store.Inherit
		}
		out = append(out, &store.Node{ID: fmt.Sprintf("s%05d", i), ParentID: gids[i%groups], Name: fmt.Sprintf("sw-%05d", i), Session: ss, Order: i})
	}
	return out
}

// TestLoadManySessions checks that YACM stays quick with 5,000 sessions in
// 300 groups: loading, the state sent to the window, saving, inheritance,
// and editing every session at once.
func TestLoadManySessions(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	a := newTestApp(t)
	nodes := buildLoadNodes(300, 5000)
	start := time.Now()
	if err := a.store.ApplyImport(nodes, nil, nil); err != nil {
		t.Fatal(err)
	}
	importTime := time.Since(start)

	start = time.Now()
	st := a.GetState()
	stateTime := time.Since(start)
	data, _ := json.Marshal(st)

	start = time.Now()
	for _, n := range st.Nodes {
		if n.Session != nil {
			if _, err := a.store.Effective(n); err != nil {
				t.Fatal(err)
			}
		}
	}
	effTime := time.Since(start)

	ids := []string{}
	for _, n := range nodes[:30] {
		ids = append(ids, n.ID)
	}
	start = time.Now()
	port := 2222
	changed, err := a.store.BulkUpdate(ids, store.BulkChange{Port: &port})
	if err != nil || len(changed) != 5000 {
		t.Fatalf("bulk: %d %v", len(changed), err)
	}
	bulkTime := time.Since(start)

	start = time.Now()
	if _, err := store.Open(a.store.Path()); err != nil {
		t.Fatal(err)
	}
	openTime := time.Since(start)
	fi, _ := os.Stat(a.store.Path())

	t.Logf("5,000 sessions / 300 groups: import+save %v, state %v (%d KB JSON), effective for all %v, edit all %v, open file %v (%d KB)",
		importTime.Round(time.Millisecond), stateTime.Round(time.Millisecond), len(data)/1024, effTime.Round(time.Millisecond),
		bulkTime.Round(time.Millisecond), openTime.Round(time.Millisecond), fi.Size()/1024)
	// Generous limits for a slow CI machine with the race detector.
	for name, d := range map[string]time.Duration{"import": importTime, "state": stateTime, "effective": effTime, "bulk": bulkTime, "open": openTime} {
		if d > 10*time.Second {
			t.Errorf("%s took %v", name, d)
		}
	}
}
