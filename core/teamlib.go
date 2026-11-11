// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yacm-app/yacm/internal/applog"
	"github.com/yacm-app/yacm/internal/store"
	"github.com/yacm-app/yacm/internal/team"
)

// loadedLib is a followed team library as last read.
type loadedLib struct {
	ref   store.TeamLibraryRef
	file  *team.File
	err   string
	stale bool   // shown from the cached copy: the file could not be read
	stamp string // size and time of the file when it was read
	at    time.Time
}

// Published libraries record this version; a library from a newer one is
// used but not edited here.
func init() { team.AppVersion = appVersion }

func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())
}

func (a *App) teamCachePath(id string) string {
	if a.dataDir == "" {
		return ""
	}
	return filepath.Join(a.dataDir, "team-cache", id+".json")
}

// refreshTeam reads followed libraries whose file changed (all of them with
// force) and shows them in the tree. It reports whether anything changed.
// File access happens without holding teamMu: a share that does not answer
// must not freeze the window.
func (a *App) refreshTeam(force bool) bool {
	a.teamIO.Lock()
	defer a.teamIO.Unlock()
	refs := a.store.Settings().TeamLibraries

	type prev struct {
		path, stamp string
		loaded      bool
	}
	a.teamMu.Lock()
	if a.team == nil {
		a.team = map[string]*loadedLib{}
	}
	before := map[string]prev{}
	for id, l := range a.team {
		before[id] = prev{l.ref.Path, l.stamp, l.file != nil}
	}
	a.teamMu.Unlock()

	type result struct {
		ref    store.TeamLibraryRef
		stamp  string
		read   bool // the file was (tried to be) read
		file   *team.File
		err    error
		cached *team.File
		prev   *team.File // the cached copy before this read: what you saw last time
	}
	results := make([]result, 0, len(refs))
	for _, ref := range refs {
		r := result{ref: ref, stamp: fileStamp(ref.Path)}
		p, known := before[ref.ID]
		if force || !known || !p.loaded || p.path != ref.Path || r.stamp != p.stamp || r.stamp == "" {
			r.read = true
			var raw []byte
			r.file, raw, r.err = team.Load(ref.Path)
			if r.err == nil {
				if cp := a.teamCachePath(ref.ID); cp != "" && (!p.loaded || p.path != ref.Path) {
					if data, cerr := os.ReadFile(cp); cerr == nil {
						r.prev, _ = team.Parse(data)
					}
				}
				if cp := a.teamCachePath(ref.ID); cp != "" && !a.store.ReadOnly() {
					_ = os.MkdirAll(filepath.Dir(cp), 0o700)
					_ = store.WriteFileAtomic(cp, raw, 0o600)
				}
			} else if !p.loaded || p.path != ref.Path {
				if cp := a.teamCachePath(ref.ID); cp != "" {
					if data, cerr := os.ReadFile(cp); cerr == nil {
						r.cached, _ = team.Parse(data)
					}
				}
			}
		}
		results = append(results, r)
	}

	var diffs []*TeamDiff
	defer func() {
		for _, d := range diffs {
			a.noteTeamDiff(d)
		}
	}()
	a.teamMu.Lock()
	defer a.teamMu.Unlock()
	changed := false
	keep := map[string]bool{}
	for _, r := range results {
		keep[r.ref.ID] = true
		l := a.team[r.ref.ID]
		if l == nil {
			l = &loadedLib{}
			a.team[r.ref.ID] = l
			changed = true
		}
		if fmt.Sprint(l.ref) != fmt.Sprint(r.ref) {
			changed = true
		}
		if l.ref.Path != r.ref.Path {
			l.file = nil
		}
		l.ref = r.ref
		if !r.read {
			continue
		}
		switch {
		case r.err == nil:
			old := l.file
			if old == nil || l.ref.Path != r.ref.Path {
				old = r.prev
			}
			if old != nil && old.Revision != r.file.Revision && a.ownPublish[r.ref.Path] != r.file.Revision {
				diffs = append(diffs, diffLibrary(old, r.file))
			}
			l.file, l.err, l.stale, l.stamp, l.at = r.file, "", false, r.stamp, time.Now()
			changed = true
		case l.file != nil:
			if !l.stale || l.err != r.err.Error() {
				changed = true
			}
			l.stale, l.err, l.stamp = true, r.err.Error(), r.stamp
		default:
			l.err, l.stamp = r.err.Error(), r.stamp
			if r.cached != nil {
				l.file, l.stale = r.cached, true
			}
			changed = true
		}
	}
	for id := range a.team {
		if !keep[id] {
			delete(a.team, id)
			changed = true
		}
	}
	if changed {
		a.applyTeamLocked()
	}
	return changed
}

// applyTeamLocked gives the store the libraries to show. A library you are
// editing is not shown: your own copy of it is.
func (a *App) applyTeamLocked() {
	var views []*store.LibraryView
	ids := make([]string, 0, len(a.team))
	for id := range a.team {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i, id := range ids {
		l := a.team[id]
		if l.file == nil {
			continue
		}
		if l.ref.Source != "" {
			if _, err := a.store.Get(l.ref.Source); err == nil {
				continue
			}
		}
		views = append(views, l.file.View(id, l.ref.Logins, 1_000_000+i))
	}
	a.store.SetLibraries(views)
}

// watchTeam re-reads changed library files every few seconds.
func (a *App) watchTeam() {
	for {
		time.Sleep(5 * time.Second)
		if a.quitting.Load() {
			return
		}
		if a.refreshTeam(false) {
			a.emit("team:changed", nil)
		}
	}
}

// ---- API ----------------------------------------------------------------------------

// TeamLogin is a library login and the profile linked to it.
type TeamLogin struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Auth        store.AuthMethod `json:"auth,omitempty"`
	Linked      string           `json:"linked"` // credential profile ID or ""
}

// TeamLibraryStatus describes a followed library for Settings.
type TeamLibraryStatus struct {
	ID             string      `json:"id"`
	Root           string      `json:"root"` // group ID in the tree
	Path           string      `json:"path"`
	Name           string      `json:"name"`
	Description    string      `json:"description,omitempty"`
	Sessions       int         `json:"sessions"`
	Revision       int         `json:"revision"`
	Updated        time.Time   `json:"updated"`
	UpdatedBy      string      `json:"updatedBy,omitempty"`
	Logins         []TeamLogin `json:"logins"`
	Snippets       int         `json:"snippets"`
	Stale          bool        `json:"stale"`
	Error          string      `json:"error,omitempty"`
	LoadedAt       time.Time   `json:"loadedAt"`
	Editing        bool        `json:"editing"`
	SourceGroup    string      `json:"sourceGroup,omitempty"`
	SourceRevision int         `json:"sourceRevision"`
	Newer          bool        `json:"newer"` // someone published after your copy was made
	Writable       bool        `json:"writable"`
	// NeedsNewer says why this YACM may use but not edit the library (it was
	// published by a newer version); empty when it can be edited.
	NeedsNewer string `json:"needsNewer,omitempty"`
}

// teamStatusLocked describes a library; checkWrite tests whether its folder
// is writable (a file is created there, so only when Settings asks).
func (a *App) teamStatusLocked(l *loadedLib, checkWrite bool) TeamLibraryStatus {
	st := TeamLibraryStatus{ID: l.ref.ID, Root: team.RootID(l.ref.ID), Path: l.ref.Path, Stale: l.stale, Error: l.err, LoadedAt: l.at, Logins: []TeamLogin{}}
	st.Writable = !checkWrite || canWrite(filepath.Dir(l.ref.Path))
	if l.ref.Source != "" {
		if _, err := a.store.Get(l.ref.Source); err == nil {
			st.Editing, st.SourceGroup, st.SourceRevision = true, l.ref.Source, l.ref.Revision
		}
	}
	if f := l.file; f != nil {
		st.Name, st.Description, st.Sessions, st.Revision, st.Updated, st.UpdatedBy, st.Snippets = f.Name, f.Description, f.Sessions(), f.Revision, f.Updated, f.UpdatedBy, len(f.Snippets)
		for _, lg := range f.Logins {
			st.Logins = append(st.Logins, TeamLogin{ID: lg.ID, Name: lg.Name, Description: lg.Description, Auth: lg.Auth, Linked: l.ref.Logins[lg.ID]})
		}
		st.Newer = st.Editing && f.Revision > l.ref.Revision
		if newer, why := f.NeedsNewer(); newer {
			st.NeedsNewer = why
		}
	} else {
		st.Name = strings.TrimSuffix(filepath.Base(l.ref.Path), filepath.Ext(l.ref.Path))
	}
	return st
}

// TeamLibraries lists the followed libraries (for Settings).
func (a *App) TeamLibraries() []TeamLibraryStatus {
	a.refreshTeam(false)
	return a.teamStatusList(true)
}

func (a *App) teamStatuses() []TeamLibraryStatus { return a.teamStatusList(false) }

func (a *App) teamStatusList(checkWrite bool) []TeamLibraryStatus {
	a.teamMu.Lock()
	defer a.teamMu.Unlock()
	out := []TeamLibraryStatus{}
	for _, ref := range a.store.Settings().TeamLibraries {
		if l := a.team[ref.ID]; l != nil {
			out = append(out, a.teamStatusLocked(l, checkWrite))
		}
	}
	return out
}

// ReloadTeamLibraries reads every library file again.
func (a *App) ReloadTeamLibraries() []TeamLibraryStatus {
	a.refreshTeam(true)
	return a.TeamLibraries()
}

func (a *App) updateTeamRefs(fn func(refs []store.TeamLibraryRef) ([]store.TeamLibraryRef, error)) error {
	st := a.store.Settings()
	refs := make([]store.TeamLibraryRef, len(st.TeamLibraries))
	copy(refs, st.TeamLibraries)
	refs, err := fn(refs)
	if err != nil {
		return err
	}
	st.TeamLibraries = refs
	if err := a.store.SetSettings(st); err != nil {
		return err
	}
	a.refreshTeam(true)
	return nil
}

// AddTeamLibrary follows a library file. Logins are linked to your profiles
// with the same name.
func (a *App) AddTeamLibrary(path string) (retSt *TeamLibraryStatus, err error) {
	defer logOp("add team library", &err)
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("choose the library file")
	}
	f, _, err := team.Load(path)
	if err != nil {
		return nil, err
	}
	for _, r := range a.store.Settings().TeamLibraries {
		if samePath(r.Path, path) {
			return nil, errors.New("you already follow this library")
		}
	}
	ref := store.TeamLibraryRef{ID: store.NewID()[:8], Path: path, Logins: map[string]string{}}
	creds := a.store.Credentials()
	for _, lg := range f.Logins {
		for _, c := range creds {
			if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(lg.Name)) {
				ref.Logins[lg.ID] = c.ID
				break
			}
		}
	}
	if err := a.updateTeamRefs(func(refs []store.TeamLibraryRef) ([]store.TeamLibraryRef, error) {
		return append(refs, ref), nil
	}); err != nil {
		return nil, err
	}
	a.teamMu.Lock()
	defer a.teamMu.Unlock()
	st := a.teamStatusLocked(a.team[ref.ID], true)
	return &st, nil
}

// RemoveTeamLibrary stops following a library (the file is not touched).
func (a *App) RemoveTeamLibrary(id string) error {
	return a.updateTeamRefs(func(refs []store.TeamLibraryRef) ([]store.TeamLibraryRef, error) {
		out := refs[:0]
		for _, r := range refs {
			if r.ID != id {
				out = append(out, r)
			}
		}
		return out, nil
	})
}

// LinkTeamLogin links a library login to one of your profiles ("" unlinks).
func (a *App) LinkTeamLogin(id, login, credID string) error {
	if credID != "" {
		if _, ok := a.store.Credential(credID); !ok {
			return errors.New("credential profile not found")
		}
	}
	return a.updateTeamRefs(func(refs []store.TeamLibraryRef) ([]store.TeamLibraryRef, error) {
		for i := range refs {
			if refs[i].ID == id {
				if refs[i].Logins == nil {
					refs[i].Logins = map[string]string{}
				}
				if credID == "" {
					delete(refs[i].Logins, login)
				} else {
					refs[i].Logins[login] = credID
				}
				return refs, nil
			}
		}
		return nil, errors.New("library not found")
	})
}

func (a *App) teamLib(id string) (*loadedLib, error) {
	a.teamMu.Lock()
	defer a.teamMu.Unlock()
	l := a.team[id]
	if l == nil {
		return nil, errors.New("library not found")
	}
	if l.file == nil {
		return nil, fmt.Errorf("the library cannot be read: %s", l.err)
	}
	cp := *l
	return &cp, nil
}

// copyNodes makes personal copies of library nodes: new IDs unless keepIDs,
// logins replaced by the linked profiles, jump hosts inside the copy
// pointed at the copies.
func copyNodes(nodes []*store.Node, logins map[string]string, keepIDs bool, rename func(old string) string) []*store.Node {
	ids := map[string]string{}
	for _, n := range nodes {
		if keepIDs {
			ids[n.ID] = n.ID
		} else {
			ids[n.ID] = store.NewID()
		}
	}
	slot := func(cid string) string {
		if s, ok := strings.CutPrefix(cid, store.SlotPrefix); ok {
			return logins[s]
		}
		return cid
	}
	out := make([]*store.Node, 0, len(nodes))
	for _, src := range nodes {
		data, _ := json.Marshal(src)
		var n store.Node
		_ = json.Unmarshal(data, &n)
		// Members only a newer YACM knows are not carried from a shared
		// file into your own sessions.
		store.DropUnknown(&n)
		store.DisarmTunnels(&n)
		n.Library = ""
		n.ID = ids[src.ID]
		if p, ok := ids[n.ParentID]; ok {
			n.ParentID = p
		} else if rename != nil {
			n.ParentID = rename(n.ParentID)
		}
		if d := n.Defaults; d != nil {
			d.CredentialID = slot(d.CredentialID)
			if j, ok := ids[d.JumpVia]; ok {
				d.JumpVia = j
			}
		}
		if ss := n.Session; ss != nil {
			ss.CredentialID = slot(ss.CredentialID)
			if j, ok := ids[ss.JumpVia]; ok {
				ss.JumpVia = j
			}
			for i := range ss.Jump {
				ss.Jump[i].CredentialID = slot(ss.Jump[i].CredentialID)
			}
			for i := range ss.Expect {
				ss.Expect[i].ID = store.NewID()
			}
			// Your own copy resolves logins to your profiles with no library
			// checks left: keep its automation within the library's limits.
			if c := sanitizeTeamCopy(ss); c > 0 {
				applog.Warnf("copy of team session %s: %d automation rule(s) or saved command(s) that would send a login elsewhere were switched off or left out", n.Name, c)
			}
		}
		n.Updated = time.Now().UTC()
		out = append(out, &n)
	}
	return out
}

// CopyToMySessions copies team library sessions or groups into your own
// sessions, where they can be changed. It returns the new top node.
func (a *App) CopyToMySessions(ids []string) (string, error) {
	all := a.store.ListAll()
	byID := map[string]*store.Node{}
	kids := map[string][]*store.Node{}
	for _, n := range all {
		byID[n.ID] = n
		kids[n.ParentID] = append(kids[n.ParentID], n)
	}
	var pick []*store.Node
	seen := map[string]bool{}
	var add func(n *store.Node)
	add = func(n *store.Node) {
		if seen[n.ID] || n.Library == "" {
			return
		}
		seen[n.ID] = true
		pick = append(pick, n)
		for _, k := range kids[n.ID] {
			add(k)
		}
	}
	lib := ""
	for _, id := range ids {
		if n := byID[id]; n != nil && n.Library != "" {
			add(n)
			lib = n.Library
		}
	}
	if len(pick) == 0 {
		return "", errors.New("choose sessions or groups of a team library")
	}
	l, err := a.teamLib(lib)
	if err != nil {
		return "", err
	}
	rootID := team.RootID(lib)
	var nodes []*store.Node
	var newer []string
	for _, n := range pick {
		if n.ID == rootID {
			continue
		}
		if n.Session != nil && !store.KnownProtocol(n.Session.Protocol) {
			newer = append(newer, n.Name)
			continue
		}
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 && len(newer) > 0 {
		return "", fmt.Errorf("%s uses a connection type from a newer version of YACM, so it cannot be copied here", newer[0])
	}
	if len(newer) > 0 {
		applog.Infof("copy to my sessions: %d session(s) of a newer connection type left out", len(newer))
	}
	// The library's own top group becomes a new group; otherwise the copies
	// go into a new group named after the library.
	group := &store.Node{ID: store.NewID(), IsGroup: true, Expanded: true, Name: "Copy of " + l.file.Name, Updated: time.Now().UTC()}
	if len(nodes) == 1 && !nodes[0].IsGroup {
		group = nil
	}
	// What the copied nodes inherited from library groups that are not
	// copied goes along, so they log in and connect the same way.
	inSet := map[string]bool{}
	for _, n := range nodes {
		inSet[n.ID] = true
	}
	merged := func(parent string) store.GroupDefaults {
		var d store.GroupDefaults
		for cur, guard := parent, 0; cur != "" && guard < 64; guard++ {
			g := byID[cur]
			if g == nil {
				break
			}
			if gd := g.Defaults; gd != nil {
				if d.CredentialID == "" {
					d.CredentialID = gd.CredentialID
				}
				if d.JumpVia == "" {
					d.JumpVia = gd.JumpVia
				}
				if d.Algorithms == "" {
					d.Algorithms = gd.Algorithms
				}
				if d.AntiIdle == 0 {
					d.AntiIdle = gd.AntiIdle
				}
				if d.AntiIdleText == "" {
					d.AntiIdleText = gd.AntiIdleText
				}
			}
			cur = g.ParentID
		}
		return d
	}
	for i, n := range nodes {
		if inSet[n.ParentID] {
			continue
		}
		d := merged(n.ParentID)
		if d.IsEmpty() {
			continue
		}
		data, _ := json.Marshal(n)
		var c store.Node
		_ = json.Unmarshal(data, &c)
		if c.IsGroup {
			if c.Defaults == nil {
				c.Defaults = &store.GroupDefaults{}
			}
			g := c.Defaults
			if g.CredentialID == "" {
				g.CredentialID = d.CredentialID
			}
			if g.JumpVia == "" {
				g.JumpVia = d.JumpVia
			}
			if g.Algorithms == "" {
				g.Algorithms = d.Algorithms
			}
			if g.AntiIdle == 0 {
				g.AntiIdle = d.AntiIdle
			}
			if g.AntiIdleText == "" {
				g.AntiIdleText = d.AntiIdleText
			}
		} else if ss := c.Session; ss != nil {
			if ss.CredentialID == store.Inherit {
				ss.CredentialID = d.CredentialID
			}
			if ss.JumpVia == store.Inherit {
				ss.JumpVia = d.JumpVia
			}
			if ss.Algorithms == store.Inherit && d.Algorithms != "" {
				ss.Algorithms = d.Algorithms
			}
			if ss.AntiIdle == 0 {
				ss.AntiIdle = d.AntiIdle
			}
			if ss.AntiIdleText == "" {
				ss.AntiIdleText = d.AntiIdleText
			}
		}
		nodes[i] = &c
	}
	copies := copyNodes(nodes, l.ref.Logins, false, func(old string) string {
		if group != nil {
			return group.ID
		}
		return ""
	})
	if group != nil {
		copies = append([]*store.Node{group}, copies...)
	}
	if err := a.store.ApplyImport(copies, nil, nil); err != nil {
		return "", err
	}
	return copies[0].ID, nil
}

// EditTeamLibrary makes your own copy of a library to change it; publishing
// that group updates the library. Logins without a linked profile get a
// new profile (asking for the password) so nothing is lost.
func (a *App) EditTeamLibrary(id string) (string, error) {
	l, err := a.teamLib(id)
	if err != nil {
		return "", err
	}
	if l.stale {
		return "", errors.New("the library file cannot be read right now; editing needs the current version")
	}
	if newer, why := l.file.NeedsNewer(); newer {
		return "", fmt.Errorf("this library cannot be edited with this version of YACM: %s. Update YACM to edit it; you can still use its sessions", why)
	}
	if l.ref.Source != "" {
		if _, err := a.store.Get(l.ref.Source); err == nil {
			return l.ref.Source, nil
		}
	}
	for _, n := range l.file.Nodes {
		if _, err := a.store.Get(n.ID); err == nil {
			return "", fmt.Errorf("you already have sessions of this library (for example %q); delete that copy or publish from it", n.Name)
		}
	}
	logins := map[string]string{}
	for k, v := range l.ref.Logins {
		logins[k] = v
	}
	for _, lg := range l.file.Logins {
		if c := logins[lg.ID]; c != "" {
			if _, ok := a.store.Credential(c); ok {
				continue
			}
		}
		auth := lg.Auth
		if auth == "" || auth == store.AuthPublicKey {
			auth = store.AuthAsk
		}
		c, err := a.store.UpsertCredential(&store.Credential{Name: lg.Name, Description: lg.Description, Auth: auth})
		if err != nil {
			c, err = a.store.UpsertCredential(&store.Credential{Name: lg.Name + " (" + l.file.Name + ")", Description: lg.Description, Auth: auth})
			if err != nil {
				return "", err
			}
		}
		logins[lg.ID] = c.ID
	}
	root := &store.Node{ID: store.NewID(), IsGroup: true, Expanded: true, Name: l.file.Name, Description: l.file.Description, Color: l.file.Color, Updated: time.Now().UTC()}
	if d := l.file.Defaults; d != nil {
		rd := *d
		if s, ok := strings.CutPrefix(rd.CredentialID, store.SlotPrefix); ok {
			rd.CredentialID = logins[s]
		}
		root.Defaults = &rd
	}
	copies := copyNodes(l.file.Nodes, logins, true, func(string) string { return root.ID })
	if err := a.store.ApplyImport(append([]*store.Node{root}, copies...), nil, nil); err != nil {
		return "", err
	}
	err = a.updateTeamRefs(func(refs []store.TeamLibraryRef) ([]store.TeamLibraryRef, error) {
		for i := range refs {
			if refs[i].ID == id {
				refs[i].Logins, refs[i].Source, refs[i].Revision = logins, root.ID, l.file.Revision
			}
		}
		return refs, nil
	})
	return root.ID, err
}

// StopEditingTeamLibrary goes back to following the library; deleteCopy
// removes your copy.
func (a *App) StopEditingTeamLibrary(id string, deleteCopy bool) error {
	var src string
	err := a.updateTeamRefs(func(refs []store.TeamLibraryRef) ([]store.TeamLibraryRef, error) {
		for i := range refs {
			if refs[i].ID == id {
				src = refs[i].Source
				refs[i].Source, refs[i].Revision = "", 0
			}
		}
		return refs, nil
	})
	if err != nil || !deleteCopy || src == "" {
		return err
	}
	return a.DeleteNode(src)
}

// PublishRequest publishes one of your groups as a team library.
type PublishRequest struct {
	GroupID       string `json:"groupId"`
	Path          string `json:"path"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	SnippetFolder string `json:"snippetFolder"` // commands of this library folder go along ("" = none)
	Force         bool   `json:"force"`         // replace a newer or foreign library
}

// PublishResult reports a publish.
type PublishResult struct {
	Revision int      `json:"revision"`
	Sessions int      `json:"sessions"`
	Logins   int      `json:"logins"`
	Warnings []string `json:"warnings"`
	// Conflict is set (and nothing written) when the file changed since you
	// started editing, or a library you do not edit is already there.
	Conflict string `json:"conflict,omitempty"`
}

// PublishTeamLibrary writes a group to a library file (a backup of the
// previous version is kept next to it) and follows it as its editor.
func (a *App) PublishTeamLibrary(req PublishRequest) (retRes *PublishResult, err error) {
	defer logOp("publish team library", &err)
	root, err := a.store.Get(req.GroupID)
	if err != nil || !root.IsGroup || root.Library != "" {
		return nil, errors.New("choose one of your own groups to publish")
	}
	path := strings.TrimSpace(req.Path)
	var ref *store.TeamLibraryRef
	for _, r := range a.store.Settings().TeamLibraries {
		r := r
		if r.Source == req.GroupID || (path != "" && samePath(r.Path, path)) {
			ref = &r
			break
		}
	}
	if path == "" && ref != nil {
		path = ref.Path
	}
	if path == "" {
		return nil, errors.New("choose where to save the library file")
	}
	expect := -1
	if ref != nil && ref.Source == req.GroupID && samePath(ref.Path, path) {
		expect = ref.Revision
	}
	if _, err := os.Stat(path); err == nil && expect < 0 && !req.Force {
		return &PublishResult{Conflict: "A team library already exists in that file. Replacing it overwrites what your colleagues see; to change it, use Edit on that library instead."}, nil
	}
	if req.Force {
		expect = -1
	}
	reverse := map[string]string{}
	if ref != nil {
		for slot, cred := range ref.Logins {
			reverse[cred] = slot
		}
	}
	creds := map[string]*store.Credential{}
	for _, c := range a.store.Credentials() {
		creds[c.ID] = c
	}
	var snippets []store.Snippet
	if req.SnippetFolder != "" {
		for _, s := range a.store.Settings().Snippets {
			if s.Folder == req.SnippetFolder {
				snippets = append(snippets, s)
			}
		}
	}
	name := req.Name
	if strings.TrimSpace(name) == "" {
		name = root.Name
	}
	f, warns := team.Build(team.BuildInput{
		Name: name, Description: req.Description, Root: root, Nodes: a.store.List(), Credentials: creds,
		SlotFor:  func(c string) (string, bool) { s, ok := reverse[c]; return s, ok },
		Snippets: snippets,
	})
	host, usr := thisMachine()
	rev, err := team.Save(path, f, expect, usr+"@"+host)
	if err == nil {
		a.teamMu.Lock()
		if a.ownPublish == nil {
			a.ownPublish = map[string]int{}
		}
		a.ownPublish[path] = rev // your own changes are not listed back to you
		a.teamMu.Unlock()
	}
	var newer *team.ErrNewer
	if errors.As(err, &newer) {
		return nil, err
	}
	var changed *team.ErrChanged
	if errors.As(err, &changed) {
		return &PublishResult{Conflict: changed.Error() + ". Publishing now would overwrite those changes."}, nil
	}
	if err != nil {
		return nil, err
	}
	logins := map[string]string{}
	for k, v := range reverse {
		logins[v] = k
	}
	for _, lg := range f.Logins {
		if _, ok := logins[lg.ID]; !ok {
			if _, isCred := creds[lg.ID]; isCred {
				logins[lg.ID] = lg.ID
			}
		}
	}
	err = a.updateTeamRefs(func(refs []store.TeamLibraryRef) ([]store.TeamLibraryRef, error) {
		for i := range refs {
			if (ref != nil && refs[i].ID == ref.ID) || samePath(refs[i].Path, path) {
				refs[i].Path, refs[i].Source, refs[i].Revision, refs[i].Logins = path, req.GroupID, rev, logins
				return refs, nil
			}
		}
		return append(refs, store.TeamLibraryRef{ID: store.NewID()[:8], Path: path, Source: req.GroupID, Revision: rev, Logins: logins}), nil
	})
	if err != nil {
		return nil, err
	}
	return &PublishResult{Revision: rev, Sessions: f.Sessions(), Logins: len(f.Logins), Warnings: warns}, nil
}

// teamSnippets lists the commands of the followed libraries for the
// command palette (read-only).
func (a *App) teamSnippets() []store.Snippet {
	a.teamMu.Lock()
	defer a.teamMu.Unlock()
	out := []store.Snippet{}
	for _, ref := range a.store.Settings().TeamLibraries {
		l := a.team[ref.ID]
		if l == nil || l.file == nil {
			continue
		}
		for _, s := range l.file.Snippets {
			s.ID = "t" + ref.ID + "-" + s.ID
			folder := "Team: " + l.file.Name
			if s.Folder != "" {
				folder += " / " + s.Folder
			}
			s.Folder = folder
			out = append(out, s)
		}
	}
	return out
}
