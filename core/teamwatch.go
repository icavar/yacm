// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/store"
	"github.com/icavar/yacm/internal/team"
)

// Whoever can write to a team library could point a session at their own
// server and collect the colleagues' logins. YACM therefore remembers where
// each team session pointed when you last connected, warns before sending a
// login to a new address, and lists what changed whenever a library is
// updated.

// teamTarget is where a team session connects: protocol, address and the
// jump hosts on the way, and what else could send a login elsewhere or act
// on a device: the outbound proxy, agent forwarding and the automation
// rules and saved commands (as a short digest).
type teamTarget struct {
	Protocol string   `json:"protocol"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Jumps    []string `json:"jumps,omitempty"` // host:port of each hop
	Proxy    string   `json:"proxy,omitempty"` // type host:port
	Agent    bool     `json:"agent,omitempty"`
	Auto     string   `json:"auto,omitempty"` // digest of automation rules and saved commands
	// V is 2 for records that include Proxy, Agent and Auto; older records
	// only compare the address and jump hosts (and are then rewritten).
	V int `json:"v,omitempty"`
}

const teamTargetV = 2

func (t teamTarget) address() string {
	s := fmt.Sprintf("%s %s:%d", t.Protocol, t.Host, t.Port)
	if t.Protocol == string(store.ProtoSerial) {
		s = fmt.Sprintf("serial %s", t.Host)
	}
	if len(t.Jumps) > 0 {
		s += " via " + strings.Join(t.Jumps, " → ")
	}
	return s
}

func (t teamTarget) String() string {
	s := t.address()
	if t.Proxy != "" {
		s += " through proxy " + t.Proxy
	}
	if t.Agent {
		s += ", agent forwarding on"
	}
	if t.Auto != "" {
		s += ", automation " + t.Auto
	}
	return s
}

func (t teamTarget) equal(o teamTarget) bool {
	if t.V < teamTargetV || o.V < teamTargetV {
		return t.address() == o.address()
	}
	return t.String() == o.String()
}

func targetOf(n *store.Node, eff *store.Effective) teamTarget {
	ss := n.Session
	idle := ""
	if ss.AntiIdle > 0 {
		idle = ss.AntiIdleText
	}
	if eff != nil {
		idle = ""
		if eff.AntiIdle > 0 {
			idle = eff.AntiIdleText
		}
	}
	t := teamTarget{Protocol: string(ss.Protocol), Host: ss.Host, Port: ss.Port, Agent: ss.AgentForward, Auto: autoDigest(ss, idle), V: teamTargetV}
	if ss.Protocol == store.ProtoSerial && ss.Serial != nil {
		t.Host, t.Port = ss.Serial.Device, 0
	}
	if p := ss.Proxy; p != nil && p.Type != "" && p.Type != "none" {
		t.Proxy = fmt.Sprintf("%s %s:%d", p.Type, p.Host, p.Port)
	}
	if eff != nil {
		for _, h := range eff.Hops {
			t.Jumps = append(t.Jumps, fmt.Sprintf("%s:%d", h.Host, h.Port))
		}
		t.Proxy = ""
		if p := eff.Proxy; p != nil && p.Type != "" && p.Type != "none" {
			t.Proxy = fmt.Sprintf("%s %s:%d", p.Type, p.Host, p.Port)
		}
	}
	return t
}

// autoDigest is a short digest of what a session sends to the device by
// itself: its active automation rules, the anti-idle text and the Enter at
// the start, so a change to them is noticed ("" when there is nothing).
// Saved commands are left out: they only go when you pick them.
func autoDigest(ss *store.Session, idleText string) string {
	h := sha256.New()
	n := 0
	for _, r := range ss.Expect {
		if r.Active {
			fmt.Fprintf(h, "rule\x00%s\x00%s\x00%t\x00%t\x00", r.Expect, r.Send, r.Hidden, r.Return)
			n++
		}
	}
	if idleText != "" {
		fmt.Fprintf(h, "idle\x00%s\x00", idleText)
		n++
	}
	if ss.InitialReturn {
		fmt.Fprint(h, "enter\x00")
		n++
	}
	if n == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// teamSeen remembers, per team session, the target of the last connection.
type teamSeen struct {
	mu     sync.Mutex
	path   string
	loaded bool
	m      map[string]teamTarget
}

func (s *teamSeen) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.m = map[string]teamTarget{}
	if data, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(data, &s.m)
	}
}

func (s *teamSeen) get(id string) (teamTarget, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	t, ok := s.m[id]
	return t, ok
}

func (s *teamSeen) set(id string, t teamTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if old, ok := s.m[id]; ok && old.equal(t) && old.V == t.V {
		return
	}
	s.m[id] = t
	if s.path == "" {
		return
	}
	if data, err := json.MarshalIndent(s.m, "", "  "); err == nil {
		_ = store.WriteFileAtomic(s.path, data, 0o600)
	}
}

func (a *App) seenTargets() *teamSeen {
	a.teamSeenOnce.Do(func() {
		p := ""
		if a.dataDir != "" {
			p = filepath.Join(a.dataDir, "team-seen.json")
		}
		a.teamSeenS = &teamSeen{path: p}
	})
	return a.teamSeenS
}

// checkTeamTarget asks before connecting when a team session now points
// somewhere else than at the last connection. It returns false when the
// user cancels.
func (a *App) checkTeamTarget(ctx context.Context, t *tab, eff *store.Effective) bool {
	n := t.node
	if n.Session == nil || !a.store.IsLibraryNode(n.ID) {
		return true
	}
	now := targetOf(n, eff)
	before, ok := a.seenTargets().get(n.ID)
	if !ok || before.equal(now) {
		return true
	}
	lib := a.libraryInfo(n)
	applog.Warnf("team session %s (%s): target changed from %s to %s", n.Name, lib.Name, before, now)
	ans, err := a.ask(ctx, map[string]any{
		"kind":    "teamchange",
		"tabId":   t.id,
		"session": n.Name,
		"change": map[string]any{
			"library":   lib.Name,
			"updatedBy": lib.UpdatedBy,
			"updated":   lib.Updated,
			"before":    before.String(),
			"now":       now.String(),
		},
	})
	if err != nil || !ans.Accept {
		applog.Infof("team session %s: connection to the changed target cancelled", n.Name)
		return false
	}
	applog.Infof("team session %s: user accepted the changed target", n.Name)
	return true
}

// rememberTeamTarget records where a team session connected.
func (a *App) rememberTeamTarget(t *tab) {
	if t.node.Session == nil || !a.store.IsLibraryNode(t.node.ID) {
		return
	}
	a.seenTargets().set(t.node.ID, targetOf(t.node, t.eff))
}

type libInfo struct {
	Name      string
	UpdatedBy string
	Updated   string
}

func (a *App) libraryInfo(n *store.Node) libInfo {
	id := n.Library
	a.teamMu.Lock()
	defer a.teamMu.Unlock()
	if l := a.team[id]; l != nil && l.file != nil {
		in := libInfo{Name: l.file.Name, UpdatedBy: l.file.UpdatedBy}
		if !l.file.Updated.IsZero() {
			in.Updated = l.file.Updated.Local().Format("2006-01-02 15:04")
		}
		return in
	}
	if name := a.store.LibraryOf(n.ID); name != "" {
		return libInfo{Name: name}
	}
	return libInfo{Name: "team library"}
}

// ---- what changed in a library ------------------------------------------------------

// TeamChange is one session that is new, gone, or points somewhere else.
type TeamChange struct {
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	Now    string `json:"now,omitempty"`
}

// TeamDiff lists what changed in a library since you last saw it.
type TeamDiff struct {
	Library   string       `json:"library"`
	Revision  int          `json:"revision"`
	UpdatedBy string       `json:"updatedBy,omitempty"`
	Updated   string       `json:"updated,omitempty"`
	Added     []TeamChange `json:"added,omitempty"`
	Removed   []TeamChange `json:"removed,omitempty"`
	Moved     []TeamChange `json:"moved,omitempty"` // address, port, protocol, jump host, proxy, agent forwarding or automation changed
}

// Empty reports whether nothing worth showing changed.
func (d *TeamDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Moved) == 0
}

// diffLibrary compares two versions of a library file.
func diffLibrary(old, cur *team.File) *TeamDiff {
	d := &TeamDiff{Library: cur.Name, Revision: cur.Revision, UpdatedBy: cur.UpdatedBy}
	if !cur.Updated.IsZero() {
		d.Updated = cur.Updated.Local().Format("2006-01-02 15:04")
	}
	sessions := func(f *team.File) map[string]*store.Node {
		m := map[string]*store.Node{}
		for _, n := range f.Nodes {
			if !n.IsGroup && n.Session != nil {
				m[n.ID] = n
			}
		}
		return m
	}
	jumpsOf := func(f *team.File, n *store.Node) []string {
		// Jump hosts inside the library are referenced by ID; show where they point.
		var out []string
		byID := map[string]*store.Node{}
		for _, x := range f.Nodes {
			byID[x.ID] = x
		}
		for _, j := range n.Session.Jump {
			out = append(out, fmt.Sprintf("%s:%d", j.Host, j.Port))
		}
		if v := n.Session.JumpVia; v != "" && v != store.Inherit {
			if jn := byID[v]; jn != nil && jn.Session != nil {
				out = append(out, fmt.Sprintf("%s:%d", jn.Session.Host, jn.Session.Port))
			} else {
				out = append(out, v)
			}
		}
		return out
	}
	target := func(f *team.File, n *store.Node) teamTarget {
		t := targetOf(n, nil)
		t.Jumps = jumpsOf(f, n)
		return t
	}
	before, now := sessions(old), sessions(cur)
	for id, n := range now {
		o, ok := before[id]
		switch {
		case !ok:
			d.Added = append(d.Added, TeamChange{Name: n.Name, Now: target(cur, n).String()})
		case !target(old, o).equal(target(cur, n)):
			d.Moved = append(d.Moved, TeamChange{Name: n.Name, Before: target(old, o).String(), Now: target(cur, n).String()})
		}
	}
	for id, o := range before {
		if _, ok := now[id]; !ok {
			d.Removed = append(d.Removed, TeamChange{Name: o.Name, Before: target(old, o).String()})
		}
	}
	for _, l := range [][]TeamChange{d.Added, d.Removed, d.Moved} {
		sort.Slice(l, func(i, j int) bool { return strings.ToLower(l[i].Name) < strings.ToLower(l[j].Name) })
	}
	return d
}

// noteTeamDiff keeps a library change for the UI (also when it happens
// before the window is ready) and tells the UI.
func (a *App) noteTeamDiff(d *TeamDiff) {
	if d == nil || d.Empty() {
		return
	}
	applog.Infof("team library %s revision %d by %s: %d added, %d removed, %d moved",
		d.Library, d.Revision, d.UpdatedBy, len(d.Added), len(d.Removed), len(d.Moved))
	a.teamDiffMu.Lock()
	a.teamDiffs = append(a.teamDiffs, *d)
	a.teamDiffMu.Unlock()
	a.emit("team:diff", nil)
}

// TakeTeamChanges returns the library changes not shown yet and forgets them.
func (a *App) TakeTeamChanges() []TeamDiff {
	a.teamDiffMu.Lock()
	defer a.teamDiffMu.Unlock()
	out := a.teamDiffs
	a.teamDiffs = nil
	return out
}
