// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package exchange implements YACM's own export/import format (.yacm).
//
// A .yacm file is UTF-8 JSON:
//
//	{
//	  "format": "yacm-sessions", "version": 1,
//	  "app": "YACM 0.9.0", "exportedAt": "...", "scope": "All sessions",
//	  "nodes": [ ...the same node objects as sessions.json... ],
//	  "credentials": [ ...credential profiles used by the nodes... ], // version 2
//	  "settings":   { ... }            // optional
//	  "knownHosts": [ "host key-type base64", ... ]   // optional, public keys
//	  "secrets":    { "v":1, "kdf":{argon2id...}, "nonce":..., "ct":... } // optional
//	}
//
// Secrets (passwords, passphrases, hidden automation values) are never
// written in clear text: when included they are sealed with a password
// chosen at export time (Argon2id + AES-256-GCM, same as the vault).
package exchange

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/icavar/yacm/internal/store"
	"github.com/icavar/yacm/internal/vault"
)

// Format identifies a YACM export file.
// Version 2 added credential profiles, group defaults and
// colours; version 1 files still import.
const (
	Format  = "yacm-sessions"
	Version = 2
)

// File is the on-disk structure.
type File struct {
	Format      string              `json:"format"`
	Version     int                 `json:"version"`
	App         string              `json:"app"`
	ExportedAt  time.Time           `json:"exportedAt"`
	Scope       string              `json:"scope"`
	Nodes       []*store.Node       `json:"nodes"`
	Credentials []*store.Credential `json:"credentials,omitempty"`
	Settings    *store.Settings     `json:"settings,omitempty"`
	KnownHosts  []string            `json:"knownHosts,omitempty"`
	Secrets     *vault.Envelope     `json:"secrets,omitempty"`
}

// ExportOptions select what goes into the file.
type ExportOptions struct {
	NodeIDs        []string `json:"nodeIds"` // subtrees to export; empty = everything
	IncludeSecrets bool     `json:"includeSecrets"`
	Password       string   `json:"password"`
	// Master is the vault's master password, needed to export passwords
	// (an unlocked vault alone is not enough: it is like Reveal for all).
	Master            string `json:"master,omitempty"`
	IncludeSettings   bool   `json:"includeSettings"`
	IncludeKnownHosts bool   `json:"includeKnownHosts"`
}

// Collect returns copies of the selected subtrees. Selected nodes become
// top-level entries of the export (their parent is cleared), so exporting a
// single session or group does not drag its ancestors along.
func Collect(all []*store.Node, roots []string) ([]*store.Node, string, error) {
	byID := map[string]*store.Node{}
	children := map[string][]*store.Node{}
	for _, n := range all {
		byID[n.ID] = n
		children[n.ParentID] = append(children[n.ParentID], n)
	}
	if len(roots) == 0 {
		out := make([]*store.Node, len(all))
		for i, n := range all {
			out[i] = clone(n)
		}
		return out, "All sessions", nil
	}
	seen := map[string]bool{}
	var out []*store.Node
	var walk func(n *store.Node, top bool)
	walk = func(n *store.Node, top bool) {
		if seen[n.ID] {
			return
		}
		seen[n.ID] = true
		c := clone(n)
		if top {
			materialize(c, n.ParentID, byID)
			c.ParentID = ""
		}
		out = append(out, c)
		for _, ch := range children[n.ID] {
			walk(ch, false)
		}
	}
	// A root that is inside another selected root is exported once, in place.
	isUnderSelected := func(id string) bool {
		sel := map[string]bool{}
		for _, r := range roots {
			sel[r] = true
		}
		for cur := byID[id]; cur != nil && cur.ParentID != ""; cur = byID[cur.ParentID] {
			if sel[cur.ParentID] {
				return true
			}
		}
		return false
	}
	var names []string
	for _, r := range roots {
		n, ok := byID[r]
		if !ok {
			return nil, "", fmt.Errorf("node %s not found", r)
		}
		if isUnderSelected(r) {
			continue
		}
		names = append(names, n.Name)
		walk(n, true)
	}
	scope := strings.Join(names, ", ")
	return out, scope, nil
}

// materialize copies what a node inherits from groups that are not part of
// the export into the node itself, so a partial export behaves the same
// after importing.
func materialize(c *store.Node, parentID string, byID map[string]*store.Node) {
	inherited := func(get func(*store.GroupDefaults) string) string {
		for cur, guard := parentID, 0; cur != "" && guard < 64; guard++ {
			g, ok := byID[cur]
			if !ok {
				break
			}
			if g.Defaults != nil {
				if v := get(g.Defaults); v != "" {
					return v
				}
			}
			cur = g.ParentID
		}
		return ""
	}
	if c.Color == "" {
		for cur, guard := byID[parentID], 0; cur != nil && guard < 64; guard++ {
			if cur.Color != "" {
				if cur.Color != "none" {
					c.Color = cur.Color
				}
				break
			}
			cur = byID[cur.ParentID]
		}
	}
	cred := inherited(func(d *store.GroupDefaults) string { return d.CredentialID })
	jump := inherited(func(d *store.GroupDefaults) string { return d.JumpVia })
	algo := inherited(func(d *store.GroupDefaults) string { return string(d.Algorithms) })
	idleText := inherited(func(d *store.GroupDefaults) string { return d.AntiIdleText })
	idle := 0
	for cur, guard := parentID, 0; cur != "" && guard < 64; guard++ {
		g, ok := byID[cur]
		if !ok {
			break
		}
		if g.Defaults != nil && g.Defaults.AntiIdle != 0 {
			idle = g.Defaults.AntiIdle
			break
		}
		cur = g.ParentID
	}
	if c.IsGroup {
		if cred == "" && jump == "" && algo == "" && idle == 0 && idleText == "" {
			return
		}
		if c.Defaults == nil {
			c.Defaults = &store.GroupDefaults{}
		}
		if c.Defaults.CredentialID == "" {
			c.Defaults.CredentialID = cred
		}
		if c.Defaults.JumpVia == "" {
			c.Defaults.JumpVia = jump
		}
		if c.Defaults.Algorithms == "" {
			c.Defaults.Algorithms = store.AlgoProfile(algo)
		}
		if c.Defaults.AntiIdle == 0 {
			c.Defaults.AntiIdle = idle
		}
		if c.Defaults.AntiIdleText == "" {
			c.Defaults.AntiIdleText = idleText
		}
		return
	}
	if ss := c.Session; ss != nil {
		if ss.CredentialID == store.Inherit && cred != "" {
			ss.CredentialID = cred
		}
		if ss.JumpVia == store.Inherit && jump != "" {
			ss.JumpVia = jump
		}
		if ss.Algorithms == store.Inherit && algo != "" {
			ss.Algorithms = store.AlgoProfile(algo)
		}
		if ss.AntiIdle == 0 {
			ss.AntiIdle = idle
		}
		if ss.AntiIdleText == "" {
			ss.AntiIdleText = idleText
		}
	}
}

// ReferencedCredentials returns the profiles that nodes use.
func ReferencedCredentials(nodes []*store.Node, all []*store.Credential) []*store.Credential {
	want := map[string]bool{}
	for _, n := range nodes {
		for _, id := range *credRefs(n) {
			want[*id] = true
		}
	}
	var out []*store.Credential
	for _, c := range all {
		if want[c.ID] {
			cc := *c
			out = append(out, &cc)
		}
	}
	return out
}

// credRefs lists every place in a node that names a credential profile.
func credRefs(n *store.Node) *[]*string {
	var out []*string
	if ss := n.Session; ss != nil {
		out = append(out, &ss.CredentialID)
		for i := range ss.Jump {
			out = append(out, &ss.Jump[i].CredentialID)
		}
		if ss.Proxy != nil {
			out = append(out, &ss.Proxy.CredentialID)
		}
	}
	if d := n.Defaults; d != nil {
		out = append(out, &d.CredentialID)
		if d.Proxy != nil {
			out = append(out, &d.Proxy.CredentialID)
		}
	}
	return &out
}

// SecretPrefixes lists the vault prefixes belonging to nodes.
func SecretPrefixes(nodes []*store.Node, creds []*store.Credential) []string {
	var out []string
	for _, n := range nodes {
		if !n.IsGroup {
			out = append(out, store.SecretPrefix(n.ID))
		}
	}
	for _, c := range creds {
		out = append(out, store.SecretCredPrefix(c.ID))
	}
	return out
}

// Build assembles an export file.
func Build(app string, nodes []*store.Node, creds []*store.Credential, scope string, secrets map[string]string, opts ExportOptions, settings *store.Settings, knownHosts []string) (*File, error) {
	f := &File{Format: Format, Version: Version, App: app, ExportedAt: time.Now().UTC().Truncate(time.Second), Scope: scope, Nodes: nodes, Credentials: creds}
	if opts.IncludeSettings && settings != nil {
		s := *settings
		f.Settings = &s
	}
	if opts.IncludeKnownHosts {
		f.KnownHosts = knownHosts
	}
	if opts.IncludeSecrets && len(secrets) > 0 {
		env, err := vault.SealMap(opts.Password, secrets)
		if err != nil {
			return nil, err
		}
		f.Secrets = env
	}
	return f, nil
}

// Marshal renders the file as indented JSON.
func (f *File) Marshal() ([]byte, error) { return json.MarshalIndent(f, "", "  ") }

// ErrNotYACM means the data is not a YACM export.
var ErrNotYACM = errors.New("not a YACM export file")

// Parse reads and validates an export file.
func Parse(data []byte) (*File, error) {
	var probe struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.Format != Format {
		return nil, ErrNotYACM
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("damaged YACM file: %w", err)
	}
	if f.Version < 1 || f.Version > Version {
		return nil, fmt.Errorf("this file was written by a newer YACM (format version %d); update YACM to import it", f.Version)
	}
	// IDs name vault entries ("node/<id>/password"): an ID that is not a
	// plain word could name another session's secret. Such nodes get a new
	// ID (their secrets in the file are left behind); such profiles are left
	// out.
	ids := map[string]bool{}
	renamed := map[string]string{}
	clean := f.Nodes[:0]
	for _, n := range f.Nodes {
		if n == nil || n.ID == "" || ids[n.ID] {
			continue
		}
		if !store.ValidID(n.ID) {
			nid := store.NewID()
			renamed[n.ID] = nid
			n.ID = nid
		}
		ids[n.ID] = true
		clean = append(clean, n)
	}
	f.Nodes = clean
	for _, n := range f.Nodes {
		if nid, ok := renamed[n.ParentID]; ok {
			n.ParentID = nid
		} else if n.ParentID != "" && !store.ValidID(n.ParentID) {
			n.ParentID = ""
		}
		if ss := n.Session; ss != nil {
			if nid, ok := renamed[ss.JumpVia]; ok {
				ss.JumpVia = nid
			}
		}
		if d := n.Defaults; d != nil {
			if nid, ok := renamed[d.JumpVia]; ok {
				d.JumpVia = nid
			}
		}
	}
	cleanCreds := f.Credentials[:0]
	cids := map[string]bool{}
	for _, c := range f.Credentials {
		if c == nil || !store.ValidID(c.ID) || cids[c.ID] {
			continue
		}
		cids[c.ID] = true
		cleanCreds = append(cleanCreds, c)
	}
	f.Credentials = cleanCreds
	return &f, nil
}

// Info summarises a file before importing.
type Info struct {
	App        string    `json:"app"`
	ExportedAt time.Time `json:"exportedAt"`
	Scope      string    `json:"scope"`
	Groups     int       `json:"groups"`
	Sessions   int       `json:"sessions"`
	HasSecrets bool      `json:"hasSecrets"`
	Settings   bool      `json:"settings"`
	KnownHosts int       `json:"knownHosts"`
	Existing   int       `json:"existing"` // nodes whose ID already exists here
	LocalCmds  int       `json:"localCommands"`
	Profiles   int       `json:"profiles"` // credential profiles in the file
}

// Inspect describes f relative to the current tree.
func Inspect(f *File, existing map[string]*store.Node) Info {
	in := Info{App: f.App, ExportedAt: f.ExportedAt, Scope: f.Scope, HasSecrets: f.Secrets != nil, Settings: f.Settings != nil, KnownHosts: len(f.KnownHosts), Profiles: len(f.Credentials)}
	for _, n := range f.Nodes {
		if n.IsGroup {
			in.Groups++
		} else {
			in.Sessions++
			if n.Session != nil {
				in.LocalCmds += len(n.Session.Before) + len(n.Session.After)
			}
		}
		if _, ok := existing[n.ID]; ok {
			in.Existing++
		}
	}
	return in
}

// ImportOptions control how a file is merged.
type ImportOptions struct {
	Target        string `json:"target"`     // "group" (new group) or "root"
	GroupName     string `json:"groupName"`  // for Target=="group"
	OnConflict    string `json:"onConflict"` // "copy", "replace" or "skip"
	Password      string `json:"password"`   // export password; empty = skip secrets
	ApplySettings bool   `json:"applySettings"`
	AddKnownHosts bool   `json:"addKnownHosts"`
}

// Skip records something that was not imported.
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Report is shown after importing.
type Report struct {
	Groups          int      `json:"groups"`
	Sessions        int      `json:"sessions"`
	Replaced        int      `json:"replaced"`
	Profiles        int      `json:"profiles"` // credential profiles added or replaced
	SkippedExisting int      `json:"skippedExisting"`
	Secrets         int      `json:"secrets"`
	KnownHosts      int      `json:"knownHosts"`
	SettingsApplied bool     `json:"settingsApplied"`
	Skipped         []Skip   `json:"skipped"`
	Warnings        []string `json:"warnings"`
}

// Plan is the set of changes an import makes.
type Plan struct {
	Insert        []*store.Node
	Update        []*store.Node
	Credentials   []*store.Credential // profiles to add or replace
	Secrets       map[string]string   // final vault keys
	ClearSecrets  []string            // prefixes to wipe before writing (replaced nodes)
	Report        Report
	GroupID       string // ID of the created target group, if any
	knownHostsRaw []string
}

// KnownHostLines returns the host-key lines to add (when requested).
func (p *Plan) KnownHostLines() []string { return p.knownHostsRaw }

// ErrNeedPassword is returned when secrets are present, a password was
// given, and it is wrong.
var ErrNeedPassword = errors.New("wrong export password")

// PlanImport works out what importing f does to the current tree.
func PlanImport(f *File, existing map[string]*store.Node, existingCreds map[string]*store.Credential, opts ImportOptions) (*Plan, error) {
	p := &Plan{Secrets: map[string]string{}}
	var secrets map[string]string
	if f.Secrets != nil && opts.Password != "" {
		s, err := vault.OpenMap(opts.Password, f.Secrets)
		if err != nil {
			if errors.Is(err, vault.ErrWrongPassword) {
				return nil, ErrNeedPassword
			}
			return nil, err
		}
		secrets = s
	} else if f.Secrets != nil {
		p.Report.Warnings = append(p.Report.Warnings, "Passwords in the file were not imported (no export password given).")
	}

	rootParent := ""
	if opts.Target != "root" {
		name := strings.TrimSpace(opts.GroupName)
		if name == "" {
			name = "Imported " + time.Now().Format("2006-01-02 15:04")
		}
		g := &store.Node{ID: store.NewID(), IsGroup: true, Name: name, Expanded: true}
		p.Insert = append(p.Insert, g)
		p.GroupID = g.ID
		rootParent = g.ID
	}

	inFile := map[string]*store.Node{}
	for _, n := range f.Nodes {
		inFile[n.ID] = n
	}
	// Parents before children, keeping the file's order otherwise.
	ordered := topoOrder(f.Nodes, inFile)

	idMap := map[string]string{} // file ID -> ID here
	skipped := map[string]bool{} // file IDs not imported (and their subtree)
	confirmed := 0
	for _, src := range ordered {
		n := clone(src)
		if src.ParentID != "" && skipped[src.ParentID] {
			skipped[src.ID] = true
			continue
		}
		if err := store.Validate(n); err != nil {
			p.Report.Skipped = append(p.Report.Skipped, Skip{n.Name, err.Error()})
			skipped[src.ID] = true
			continue
		}
		if n.Session != nil && !store.KnownProtocol(n.Session.Protocol) {
			p.Report.Skipped = append(p.Report.Skipped, Skip{n.Name, fmt.Sprintf("uses the connection type %q from a newer version of YACM; import it there", n.Session.Protocol)})
			skipped[src.ID] = true
			continue
		}
		// Members only a newer YACM knows are not carried into your own
		// sessions from someone else's file.
		store.DropUnknown(n)
		store.DisarmTunnels(n)
		if _, dropped := store.FromElsewhere(n); len(dropped) > 0 {
			p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%s: %d path(s) to files or devices on other computers were left out (on Windows, opening one sends your login to that server)", n.Name, len(dropped)))
			if err := store.Validate(n); err != nil {
				p.Report.Skipped = append(p.Report.Skipped, Skip{n.Name, err.Error()})
				skipped[src.ID] = true
				continue
			}
		}
		parent := rootParent
		if src.ParentID != "" {
			if mapped, ok := idMap[src.ParentID]; ok {
				parent = mapped
			}
		}
		// Local commands from a file run on this computer: always ask first.
		if s := n.Session; s != nil {
			for i := range s.Before {
				if !s.Before[i].Confirm {
					s.Before[i].Confirm = true
					confirmed++
				}
			}
			for i := range s.After {
				if !s.After[i].Confirm {
					s.After[i].Confirm = true
					confirmed++
				}
			}
		}
		cur, exists := existing[src.ID]
		sameKind := exists && cur.IsGroup == n.IsGroup
		switch {
		case exists && sameKind && opts.OnConflict == "skip":
			// Keep what is here; children of a skipped group still merge into it.
			idMap[src.ID] = src.ID
			if !n.IsGroup {
				p.Report.SkippedExisting++
			}
			continue
		case exists && sameKind && opts.OnConflict == "replace":
			n.ParentID, n.Order, n.Expanded = cur.ParentID, cur.Order, cur.Expanded
			idMap[src.ID] = src.ID
			p.Update = append(p.Update, n)
			if n.IsGroup {
				p.Report.Groups++
			} else {
				p.Report.Replaced++
			}
		default:
			if exists {
				n.ID = store.NewID()
			}
			idMap[src.ID] = n.ID
			n.ParentID = parent
			p.Insert = append(p.Insert, n)
			if n.IsGroup {
				p.Report.Groups++
			} else {
				p.Report.Sessions++
			}
		}
		if n.IsGroup {
			continue
		}
		// Secrets follow the node to its (possibly new) ID.
		if secrets != nil {
			oldPrefix := store.SecretPrefix(src.ID)
			newPrefix := store.SecretPrefix(n.ID)
			found := false
			for k, v := range secrets {
				if strings.HasPrefix(k, oldPrefix) && v != "" {
					p.Secrets[newPrefix+strings.TrimPrefix(k, oldPrefix)] = v
					found = true
				}
			}
			if exists && opts.OnConflict == "replace" && found {
				p.ClearSecrets = append(p.ClearSecrets, newPrefix)
			}
		}
		// A replaced session that now points elsewhere does not take your saved
		// password along to the new address.
		if exists && sameKind && opts.OnConflict == "replace" && cur.Session != nil && n.Session != nil && !sameTarget(cur.Session, n.Session) {
			prefix := store.SecretPrefix(n.ID)
			has := false
			for k := range p.Secrets {
				if strings.HasPrefix(k, prefix) {
					has = true
					break
				}
			}
			if !has {
				p.ClearSecrets = append(p.ClearSecrets, prefix)
				p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%s now connects to %s: its saved passwords were removed", n.Name, n.Session.Host))
			}
		}
		p.Report.Warnings = append(p.Report.Warnings, missingFiles(n)...)
	}
	// Credential profiles: new ones are added; one that already exists here
	// is replaced only in "replace" mode, otherwise the local one is used.
	for _, c := range f.Credentials {
		_, exists := existingCreds[c.ID]
		if exists && opts.OnConflict != "replace" {
			continue
		}
		cc := *c
		cc.Extra = nil // members only a newer YACM knows stay out of your profiles
		for _, path := range []*string{&cc.KeyPath, &cc.CertPath} {
			if store.IsNetworkPath(*path) {
				p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("profile %s: a path to a file on another computer was left out", cc.Name))
				*path = ""
			}
		}
		if strings.TrimSpace(cc.Name) == "" {
			cc.Name = "Imported profile"
		}
		for _, o := range existingCreds {
			if o.ID != cc.ID && strings.EqualFold(o.Name, cc.Name) {
				cc.Name += " (imported)"
				break
			}
		}
		p.Credentials = append(p.Credentials, &cc)
		p.Report.Profiles++
		if secrets != nil {
			prefix := store.SecretCredPrefix(c.ID)
			found := false
			for k, v := range secrets {
				if strings.HasPrefix(k, prefix) && v != "" {
					p.Secrets[k] = v
					found = true
				}
			}
			if exists && found {
				p.ClearSecrets = append(p.ClearSecrets, prefix)
			}
		}
	}
	// Jump-host references follow sessions that got new IDs; references to
	// sessions that are neither in the file nor here are dropped.
	fix := func(n *store.Node, id *string) {
		if *id == "" || *id == store.Inherit {
			return
		}
		if mapped, ok := idMap[*id]; ok {
			*id = mapped
			return
		}
		if _, ok := existing[*id]; ok {
			return
		}
		p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%s: its jump host session was not part of the file, so it connects directly now", n.Name))
		*id = ""
	}
	// Profile references must name a profile in the file: a reference to one
	// of yours that the file does not carry would log in with your account.
	// A profile that is in the file but also here (and not replaced) is
	// yours; sessions that will use it are named in the report.
	inFileCred := map[string]bool{}
	fileCred := map[string]*store.Credential{}
	for _, c := range f.Credentials {
		inFileCred[c.ID] = true
		fileCred[c.ID] = c
	}
	for _, n := range append(append([]*store.Node{}, p.Insert...), p.Update...) {
		if n.Session != nil && n.Session.JumpVia != "" {
			fix(n, &n.Session.JumpVia)
		}
		if n.Defaults != nil && n.Defaults.JumpVia != "" {
			fix(n, &n.Defaults.JumpVia)
		}
		for _, id := range *credRefs(n) {
			if *id == "" || *id == store.Inherit {
				continue
			}
			if !inFileCred[*id] {
				p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%s: its credential profile was not part of the file, so it asks for a login now", n.Name))
				*id = ""
				continue
			}
			if c, mine := existingCreds[*id]; mine && opts.OnConflict != "replace" {
				// The file names one of your profiles. Only your own export
				// carries it with the same name and user; anything else could
				// be a borrowed ID, so the reference is dropped.
				if fc := fileCred[*id]; fc == nil || fc.Name != c.Name || fc.User != c.User {
					p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%s: it named your profile %s, which the file does not match, so it asks for a login now", n.Name, c.Name))
					*id = ""
					continue
				}
				what := n.Name
				if n.Session != nil {
					what = fmt.Sprintf("%s (%s)", n.Name, n.Session.Host)
				} else if n.IsGroup {
					what = "group " + n.Name
				}
				p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%s logs in with your profile %s", what, c.Name))
			}
		}
	}
	// A new session that lands in one of your groups and takes its login from
	// the group logs in with your account: name it in the report.
	inserted := map[string]bool{}
	for _, n := range p.Insert {
		inserted[n.ID] = true
	}
	for _, n := range p.Insert {
		if n.Session == nil || n.Session.CredentialID != store.Inherit || inserted[n.ParentID] {
			continue
		}
		if g, ok := existing[n.ParentID]; ok && g.IsGroup {
			p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%s (%s) joins your group %s and logs in with the group's login", n.Name, n.Session.Host, g.Name))
		}
	}
	p.Report.Secrets = len(p.Secrets)
	if confirmed > 0 {
		p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%d local command(s) were set to ask before running, because they come from a file.", confirmed))
	}
	if opts.AddKnownHosts {
		var dropped int
		p.knownHostsRaw, dropped = sessionHostKeys(f)
		if dropped > 0 {
			p.Report.Warnings = append(p.Report.Warnings, fmt.Sprintf("%d trusted host key line(s) were left out: only plain keys for the hosts of the sessions in the file are added (no wildcards, certificate authorities or other hosts).", dropped))
		}
	}
	// An empty target group is pointless.
	if p.GroupID != "" && len(p.Insert) == 1 {
		p.Insert = nil
		p.GroupID = ""
	}
	return p, nil
}

// sessionHostKeys keeps the known_hosts lines of the file that name only
// hosts its sessions (or their jump hosts) connect to, written literally: a
// line for "*", a pattern, a hashed name or a marker such as
// @cert-authority would make YACM trust servers far beyond the file.
func sessionHostKeys(f *File) (keep []string, dropped int) {
	hosts := map[string]bool{}
	for _, n := range f.Nodes {
		if s := n.Session; s != nil {
			hosts[strings.ToLower(strings.TrimSpace(s.Host))] = true
			for _, j := range s.Jump {
				hosts[strings.ToLower(strings.TrimSpace(j.Host))] = true
			}
		}
	}
	for _, line := range f.KnownHosts {
		if knownHostLineFor(line, hosts) {
			keep = append(keep, line)
		} else if strings.TrimSpace(line) != "" {
			dropped++
		}
	}
	return keep, dropped
}

func knownHostLineFor(line string, hosts map[string]bool) bool {
	fields := strings.Fields(line)
	if len(fields) > 0 && fields[0] == "@revoked" {
		fields = fields[1:] // revoking a key only takes trust away
	}
	if len(fields) < 3 || strings.HasPrefix(fields[0], "@") || strings.HasPrefix(fields[0], "|") {
		return false
	}
	for _, h := range strings.Split(fields[0], ",") {
		if h == "" || strings.ContainsAny(h, "*?!") {
			return false
		}
		if strings.HasPrefix(h, "[") {
			end := strings.Index(h, "]")
			if end < 0 || h[end+1:] == ":22" {
				return false // [host]:22 is the plain host written another way
			}
			h = h[1:end]
		}
		if !hosts[strings.ToLower(h)] {
			return false
		}
	}
	return true
}

// sameTarget reports whether two sessions connect to the same place.
func sameTarget(a, b *store.Session) bool {
	port := func(s *store.Session) int {
		switch {
		case s.Port != 0:
			return s.Port
		case s.Protocol == store.ProtoTelnet:
			return 23
		case s.Protocol == store.ProtoSSH:
			return 22
		}
		return 0
	}
	if a.Protocol != b.Protocol || !strings.EqualFold(strings.TrimSpace(a.Host), strings.TrimSpace(b.Host)) || port(a) != port(b) {
		return false
	}
	if (a.Serial == nil) != (b.Serial == nil) || (a.Serial != nil && a.Serial.Device != b.Serial.Device) {
		return false
	}
	// The jump hosts have their own saved passwords.
	if a.JumpVia != b.JumpVia || len(a.Jump) != len(b.Jump) {
		return false
	}
	for i := range a.Jump {
		ja, jb := a.Jump[i], b.Jump[i]
		pa, pb := ja.Port, jb.Port
		if pa == 0 {
			pa = 22
		}
		if pb == 0 {
			pb = 22
		}
		if !strings.EqualFold(strings.TrimSpace(ja.Host), strings.TrimSpace(jb.Host)) || pa != pb {
			return false
		}
	}
	return true
}

func topoOrder(nodes []*store.Node, inFile map[string]*store.Node) []*store.Node {
	depth := func(n *store.Node) int {
		d := 0
		for cur := n; cur.ParentID != "" && d < 1000; d++ {
			p, ok := inFile[cur.ParentID]
			if !ok {
				break
			}
			cur = p
		}
		return d
	}
	out := append([]*store.Node(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool { return depth(out[i]) < depth(out[j]) })
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// missingFiles warns about key or certificate paths that do not exist here.
func missingFiles(n *store.Node) []string {
	s := n.Session
	if s == nil || s.Protocol != store.ProtoSSH {
		return nil
	}
	var out []string
	check := func(label, p string) {
		if p == "" {
			return
		}
		if _, err := os.Stat(expandHome(p)); err != nil {
			out = append(out, fmt.Sprintf("%s: %s %s does not exist on this computer", n.Name, label, p))
		}
	}
	if s.Auth == store.AuthPublicKey {
		check("key file", s.KeyPath)
		check("certificate", s.CertPath)
	}
	for _, j := range s.Jump {
		if j.Auth == store.AuthPublicKey {
			check("jump host key file", j.KeyPath)
		}
	}
	return out
}

func clone(n *store.Node) *store.Node {
	data, _ := json.Marshal(n)
	var c store.Node
	_ = json.Unmarshal(data, &c)
	return &c
}
