// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package team reads and writes team libraries: one JSON file on a share, in
// a synced folder or in git that a whole team uses as a common, read-only
// set of sessions. A library never contains passwords, passphrases, key
// files or commands that run on the user's computer; logins are named
// placeholders ("AD account") that every user links to a profile of their
// own.
package team

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/icavar/yacm/internal/jsonx"
	"github.com/icavar/yacm/internal/store"
)

// Format identifies a team library file.
const Format = "yacm-team-library"

// Version is the file format version this code writes. It only changes for
// changes older versions cannot read; new members and new connection types
// keep it (older versions keep what they do not know, see NeedsNewer).
const Version = 1

// AppVersion is this YACM's version ("0.9.0"), set at start. It is written
// into published libraries and compared with theirs.
var AppVersion = ""

const maxSize = 32 << 20

// Login is a named placeholder for credentials.
type Login struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Auth        store.AuthMethod `json:"auth,omitempty"` // a hint: password, publickey, agent, ask

	Extra jsonx.Unknown `json:"-"`
}

func (v *Login) UnmarshalJSON(b []byte) error {
	type plain Login
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}

func (v Login) MarshalJSON() ([]byte, error) {
	type plain Login
	return jsonx.Marshal(plain(v), v.Extra)
}

// File is a team library.
type File struct {
	Format      string    `json:"format"`
	Version     int       `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Revision    int       `json:"revision"`
	Updated     time.Time `json:"updated"`
	UpdatedBy   string    `json:"updatedBy,omitempty"`
	// App is the YACM version that published the library ("0.9.0"; empty
	// in libraries from versions that did not record it).
	App    string  `json:"app,omitempty"`
	Logins []Login `json:"logins"`
	// Defaults and Color belong to the library's top group (the group that
	// was published).
	Defaults *store.GroupDefaults `json:"defaults,omitempty"`
	Color    string               `json:"color,omitempty"`
	Nodes    []*store.Node        `json:"nodes"`
	Snippets []store.Snippet      `json:"snippets,omitempty"`

	// Extra keeps members written by a newer YACM.
	Extra jsonx.Unknown `json:"-"`
}

func (v *File) UnmarshalJSON(b []byte) error {
	type plain File
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}

func (v File) MarshalJSON() ([]byte, error) {
	type plain File
	return jsonx.Marshal(plain(v), v.Extra)
}

var versionRe = regexp.MustCompile(`\d+(?:\.\d+)*`)

// compareVersions compares "0.9.0"-style versions (any text around the
// numbers is ignored; a missing part counts as 0).
func compareVersions(a, b string) int {
	pa := strings.Split(versionRe.FindString(a), ".")
	pb := strings.Split(versionRe.FindString(b), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// NeedsNewer reports whether the library holds something only a newer YACM
// knows: it was published by a newer version, or it has connection types or
// members this version does not have. Such a library can be used here, but
// not edited or replaced, because publishing from here would drop what this
// version does not understand. The reason is meant for the user.
func (f *File) NeedsNewer() (bool, string) {
	if f.App != "" && AppVersion != "" && compareVersions(f.App, AppVersion) > 0 {
		return true, fmt.Sprintf("it was published with YACM %s and this is %s", f.App, AppVersion)
	}
	newer := len(f.Extra) > 0 || (f.Defaults != nil && len(f.Defaults.Extra) > 0)
	for _, l := range f.Logins {
		newer = newer || len(l.Extra) > 0
	}
	for _, s := range f.Snippets {
		newer = newer || len(s.Extra) > 0
	}
	for _, n := range f.Nodes {
		newer = newer || store.HasUnknown(n)
	}
	if newer {
		return true, "it contains settings or connection types from a newer version of YACM"
	}
	return false, ""
}

// Sessions counts the sessions.
func (f *File) Sessions() int {
	n := 0
	for _, x := range f.Nodes {
		if !x.IsGroup {
			n++
		}
	}
	return n
}

// Load reads and checks a library file.
func Load(path string) (*File, []byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if fi.IsDir() {
		return nil, nil, errors.New("this is a folder; choose the library file (.json)")
	}
	if fi.Size() > maxSize {
		return nil, nil, errors.New("the file is too large to be a team library")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := Parse(data)
	return f, data, err
}

// Parse decodes and checks library data.
func Parse(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("not a team library: %w", err)
	}
	if f.Format != Format {
		return nil, errors.New("not a YACM team library (format field missing)")
	}
	if f.Version > Version {
		return nil, fmt.Errorf("this library was made by a newer YACM (format %d); update YACM", f.Version)
	}
	if err := f.check(); err != nil {
		return nil, err
	}
	return &f, nil
}

// check validates the nodes and removes anything a library must not carry.
func (f *File) check() error {
	if strings.TrimSpace(f.Name) == "" {
		f.Name = "Team library"
	}
	logins := map[string]bool{}
	for _, l := range f.Logins {
		if l.ID == "" || logins[l.ID] {
			return errors.New("the library has a login without an ID or with a duplicate ID")
		}
		logins[l.ID] = true
	}
	if !store.ValidColor(f.Color) {
		f.Color = ""
	}
	ids := map[string]*store.Node{}
	for _, n := range f.Nodes {
		if n == nil || n.ID == "" || strings.ContainsAny(n.ID, " /\\:") {
			return errors.New("the library has a node without a valid ID")
		}
		if ids[n.ID] != nil {
			return fmt.Errorf("the library has the ID %q twice", n.ID)
		}
		ids[n.ID] = n
	}
	if d := f.Defaults; d != nil {
		root := &store.Node{Name: f.Name, IsGroup: true, Defaults: d}
		if err := store.Validate(root); err != nil {
			return fmt.Errorf("library defaults: %w", err)
		}
		f.Defaults = root.Defaults
		if d := f.Defaults; d != nil {
			if slot, ok := strings.CutPrefix(d.CredentialID, store.SlotPrefix); !ok || !logins[slot] {
				d.CredentialID = ""
			}
			if d.JumpVia != "" && ids[d.JumpVia] == nil {
				d.JumpVia = ""
			}
			// A proxy login from a library could only name someone's own
			// profile: proxies in libraries never log in.
			if d.Proxy != nil {
				d.Proxy.CredentialID = ""
			}
			d.AntiIdleText = plainText(d.AntiIdleText)
		}
	}
	for _, n := range f.Nodes {
		n.Library = ""
		if n.ParentID != "" {
			if p := ids[n.ParentID]; p == nil || !p.IsGroup {
				n.ParentID = ""
			}
		}
		if err := store.Validate(n); err != nil {
			return fmt.Errorf("%s: %w", n.Name, err)
		}
		checkRef := func(cid string) string {
			if slot, ok := strings.CutPrefix(cid, store.SlotPrefix); ok && logins[slot] {
				return cid
			}
			if cid == store.Inherit {
				return cid
			}
			return ""
		}
		if n.Defaults != nil {
			n.Defaults.CredentialID = checkRef(n.Defaults.CredentialID)
			if n.Defaults.Proxy != nil {
				n.Defaults.Proxy.CredentialID = ""
			}
			n.Defaults.AntiIdleText = plainText(n.Defaults.AntiIdleText)
			if j := n.Defaults.JumpVia; j != "" && ids[j] == nil {
				n.Defaults.JumpVia = ""
			}
		}
		if ss := n.Session; ss != nil {
			ss.CredentialID = checkRef(ss.CredentialID)
			// Never run commands from a shared file on this computer, and
			// never point at someone else's files.
			ss.Before, ss.After = nil, nil
			ss.KeyPath, ss.CertPath, ss.LogDir = "", "", ""
			// No proxy logins (they could only name someone's own profile) and
			// no agent forwarding to hosts the library's editors may run.
			if ss.Proxy != nil {
				ss.Proxy.CredentialID = ""
			}
			ss.AgentForward = false
			// The anti-idle text is typed at the prompt by itself: no control
			// characters (they could edit what is on the command line).
			ss.AntiIdleText = plainText(ss.AntiIdleText)
			if ss.Serial != nil && store.IsNetworkPath(ss.Serial.Device) {
				ss.Serial.Device = ""
			}
			if ss.Auth == store.AuthPublicKey && ss.CredentialID == "" {
				ss.Auth = store.AuthAsk
			}
			if j := ss.JumpVia; j != "" && j != store.Inherit && ids[j] == nil {
				ss.JumpVia = ""
			}
			for i := range ss.Jump {
				ss.Jump[i].KeyPath = ""
				ss.Jump[i].CredentialID = checkRef(ss.Jump[i].CredentialID)
				if ss.Jump[i].Auth == store.AuthPublicKey && ss.Jump[i].CredentialID == "" {
					ss.Jump[i].Auth = store.AuthAgent
				}
			}
			var rules []store.ExpectRule
			for _, r := range ss.Expect {
				if !r.Hidden {
					rules = append(rules, r)
				}
			}
			ss.Expect = rules
		}
	}
	return nil
}

// NodeID is the ID a library node gets in the session tree, so it can never
// clash with a node of the user's own.
func NodeID(lib, id string) string { return "t" + lib + "-" + id }

// RootID is the ID of the group that shows a library in the tree.
func RootID(lib string) string { return "t" + lib }

// View turns a library into what the store shows: a top-level group with
// the library's name and every node below it, IDs made unique.
func (f *File) View(lib string, slots map[string]string, order int) *store.LibraryView {
	v := &store.LibraryView{ID: lib, Name: f.Name, Slots: map[string]string{}, SlotNames: map[string]string{}}
	for k, c := range slots {
		v.Slots[k] = c
	}
	for _, l := range f.Logins {
		v.SlotNames[l.ID] = l.Name
	}
	root := &store.Node{ID: RootID(lib), IsGroup: true, Name: f.Name, Description: f.Description, Order: order, Expanded: true, Updated: f.Updated, Color: f.Color}
	if f.Defaults != nil {
		d := *f.Defaults
		if d.JumpVia != "" {
			d.JumpVia = NodeID(lib, d.JumpVia)
		}
		root.Defaults = &d
	}
	v.Nodes = append(v.Nodes, root)
	data, _ := json.Marshal(f.Nodes)
	var nodes []*store.Node
	_ = json.Unmarshal(data, &nodes)
	mapID := func(id string) string {
		if id == "" || id == store.Inherit {
			return id
		}
		return NodeID(lib, id)
	}
	for _, n := range nodes {
		n.ID = mapID(n.ID)
		if n.ParentID == "" {
			n.ParentID = root.ID
		} else {
			n.ParentID = mapID(n.ParentID)
		}
		if n.Defaults != nil {
			n.Defaults.JumpVia = mapID(n.Defaults.JumpVia)
		}
		if n.Session != nil {
			n.Session.JumpVia = mapID(n.Session.JumpVia)
		}
		v.Nodes = append(v.Nodes, n)
	}
	return v
}

// ---- building a library from a group ---------------------------------------------

// BuildInput describes what to publish.
type BuildInput struct {
	Name        string
	Description string
	Root        *store.Node   // the group being published (not itself included)
	Nodes       []*store.Node // all of the user's nodes
	Credentials map[string]*store.Credential
	// SlotFor maps a credential profile to the library login it stands for
	// (an editor keeps the IDs that colleagues already linked). ok false
	// makes a new login named after the profile.
	SlotFor  func(credID string) (slot string, ok bool)
	Snippets []store.Snippet
}

// Build makes a library file from a group: everything inside it, with
// profiles turned into logins and personal or secret parts left out. It
// also returns what was left out, for the user to check.
func Build(in BuildInput) (*File, []string) {
	f := &File{Format: Format, Version: Version, Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description)}
	if f.Name == "" {
		f.Name = in.Root.Name
	}
	var warn []string
	byParent := map[string][]*store.Node{}
	for _, n := range in.Nodes {
		byParent[n.ParentID] = append(byParent[n.ParentID], n)
	}
	inside := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		kids := byParent[id]
		sort.Slice(kids, func(i, j int) bool { return kids[i].Order < kids[j].Order })
		for _, k := range kids {
			inside[k.ID] = true
			f.Nodes = append(f.Nodes, k)
			if k.IsGroup {
				walk(k.ID)
			}
		}
	}
	walk(in.Root.ID)

	logins := map[string]*Login{}
	var loginOrder []string
	slot := func(cid, where string) string {
		if cid == "" || cid == store.Inherit {
			return cid
		}
		c := in.Credentials[cid]
		if c == nil {
			warn = append(warn, where+": its credential profile no longer exists; it will ask for the login")
			return ""
		}
		id := c.ID
		if in.SlotFor != nil {
			if s, ok := in.SlotFor(c.ID); ok {
				id = s
			}
		}
		if logins[id] == nil {
			logins[id] = &Login{ID: id, Name: c.Name, Description: c.Description, Auth: c.Auth}
			loginOrder = append(loginOrder, id)
		}
		return store.SlotPrefix + id
	}

	if in.Root.Defaults != nil {
		d := *in.Root.Defaults
		d.CredentialID = slot(d.CredentialID, in.Root.Name)
		if d.JumpVia != "" && !inside[d.JumpVia] {
			warn = append(warn, in.Root.Name+": the jump host session is not inside this group, so it was left out (move it into the group to publish it too)")
			d.JumpVia = ""
		}
		if !d.IsEmpty() {
			f.Defaults = &d
		}
	}
	if in.Root.Color != "none" {
		f.Color = in.Root.Color
	}
	out := make([]*store.Node, 0, len(f.Nodes))
	for _, src := range f.Nodes {
		data, _ := json.Marshal(src)
		var n store.Node
		_ = json.Unmarshal(data, &n)
		n.Library, n.Expanded = "", n.IsGroup && n.Expanded
		if n.ParentID == in.Root.ID {
			n.ParentID = ""
		}
		if d := n.Defaults; d != nil {
			d.CredentialID = slot(d.CredentialID, n.Name)
			if d.JumpVia != "" && !inside[d.JumpVia] {
				warn = append(warn, n.Name+": the jump host session is not inside this group, so it was left out (move it into the group to publish it too)")
				d.JumpVia = ""
			}
			if d.IsEmpty() {
				n.Defaults = nil
			}
		}
		if ss := n.Session; ss != nil {
			ss.CredentialID = slot(ss.CredentialID, n.Name)
			if len(ss.Before) > 0 || len(ss.After) > 0 {
				warn = append(warn, n.Name+": local commands are never shared (they would run on colleagues' computers)")
				ss.Before, ss.After = nil, nil
			}
			if ss.CredentialID == "" && ss.Auth == store.AuthPublicKey {
				warn = append(warn, n.Name+": uses your own key file; in the library it asks for the login")
				ss.Auth = store.AuthAsk
			}
			ss.KeyPath, ss.CertPath, ss.LogDir = "", "", ""
			if ss.JumpVia != "" && ss.JumpVia != store.Inherit && !inside[ss.JumpVia] {
				warn = append(warn, n.Name+": the jump host session is not inside this group, so it was left out (move it into the group to publish it too)")
				ss.JumpVia = ""
			}
			for i := range ss.Jump {
				ss.Jump[i].CredentialID = slot(ss.Jump[i].CredentialID, n.Name)
				ss.Jump[i].KeyPath = ""
			}
			var rules []store.ExpectRule
			for _, r := range ss.Expect {
				if r.Hidden {
					warn = append(warn, n.Name+": an automation rule with a hidden value was left out; use <PASS> instead")
					continue
				}
				rules = append(rules, r)
			}
			ss.Expect = rules
		}
		out = append(out, &n)
	}
	f.Nodes = out
	for _, id := range loginOrder {
		f.Logins = append(f.Logins, *logins[id])
	}
	if f.Logins == nil {
		f.Logins = []Login{}
	}
	f.Snippets = in.Snippets
	return f, dedupe(warn)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- saving ---------------------------------------------------------------------

// ErrChanged is returned when someone else published since the editor's
// copy was made.
type ErrChanged struct {
	Revision int
	By       string
	At       time.Time
}

func (e *ErrChanged) Error() string {
	return fmt.Sprintf("the library was changed by %s on %s (revision %d) since you started editing", e.By, e.At.Local().Format("2006-01-02 15:04"), e.Revision)
}

// ErrBusy is returned while someone else is saving the library.
type ErrBusy struct{ By string }

// ErrNewer is returned when the library in the file needs a newer YACM:
// replacing it from here would drop what this version does not know.
type ErrNewer struct{ Reason string }

func (e *ErrNewer) Error() string {
	return "this library cannot be changed with this version of YACM: " + e.Reason + ". Update YACM to edit it; you can still use its sessions"
}

func (e *ErrBusy) Error() string {
	return "the library is being published by " + e.By + " right now; try again in a moment"
}

const lockStale = 2 * time.Minute

// Save writes the library. expect is the revision the editor started from
// (-1 skips the check, for a new library). The previous file is kept in a
// .yacm-backup folder next to it (the last 10).
func Save(path string, f *File, expect int, by string) (int, error) {
	lock := path + ".lock"
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(lock)
			lf, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		}
		if err != nil {
			who, _ := os.ReadFile(lock)
			return 0, &ErrBusy{By: strings.TrimSpace(string(who))}
		}
	}
	fmt.Fprintf(lf, "%s at %s", by, time.Now().Format(time.RFC3339))
	lf.Close()
	defer os.Remove(lock)

	rev := 0
	if old, raw, err := Load(path); err == nil {
		if newer, why := old.NeedsNewer(); newer {
			return 0, &ErrNewer{Reason: why}
		}
		if expect >= 0 && old.Revision != expect {
			return 0, &ErrChanged{Revision: old.Revision, By: old.UpdatedBy, At: old.Updated}
		}
		rev = old.Revision
		if err := backup(path, raw, old.Revision); err != nil {
			return 0, fmt.Errorf("could not keep a backup of the current library: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		if _, serr := os.Stat(path); serr == nil {
			return 0, fmt.Errorf("the file exists but is not a team library: %w", err)
		}
	}
	f.Format, f.Version = Format, Version
	f.App = AppVersion
	f.Revision = rev + 1
	f.Updated = time.Now().UTC()
	f.UpdatedBy = by
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := store.WriteFileAtomic(path, append(data, '\n'), 0o644); err != nil {
		return 0, err
	}
	return f.Revision, nil
}

func backup(path string, raw []byte, rev int) error {
	dir := filepath.Join(filepath.Dir(path), ".yacm-backup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name := fmt.Sprintf("%s-%s-r%d.json", base, time.Now().Format("20060102-150405"), rev)
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, base+"-2*-r*.json"))
	sort.Strings(old)
	for len(old) > 10 {
		_ = os.Remove(old[0])
		old = old[1:]
	}
	return nil
}

// plainText drops a text with control characters (anti-idle texts write
// Enter as {enter}).
func plainText(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }) >= 0 {
		return ""
	}
	return s
}
