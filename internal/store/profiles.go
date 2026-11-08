// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/icavar/yacm/internal/jsonx"
)

// ---- credential profiles ------------------------------------------------------

// Credentials returns copies of all credential profiles, sorted by name.
func (s *Store) Credentials() []*Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Credential, 0, len(s.creds))
	for _, c := range s.creds {
		cc := *c
		out = append(out, &cc)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Credential returns one profile.
func (s *Store) Credential(id string) (*Credential, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.creds[id]
	if !ok {
		return nil, false
	}
	cc := *c
	return &cc, true
}

// UpsertCredential creates or updates a credential profile.
func (s *Store) UpsertCredential(c *Credential) (*Credential, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return nil, errors.New("name is required")
	}
	switch c.Auth {
	case "":
		c.Auth = AuthPassword
	case AuthPassword, AuthPublicKey, AuthAgent, AuthAsk:
	default:
		return nil, fmt.Errorf("unknown login method %q", c.Auth)
	}
	if c.Auth != AuthPublicKey {
		c.KeyPath, c.CertPath = "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	if c.ID == "" {
		c.ID = NewID()
	}
	for _, o := range s.creds {
		if o.ID != c.ID && strings.EqualFold(o.Name, c.Name) {
			return nil, fmt.Errorf("a credential profile named %q already exists", c.Name)
		}
	}
	cc := *c
	cc.Updated = time.Now().UTC()
	if old, ok := s.creds[cc.ID]; ok {
		cc.Extra = jsonx.Keep(cc.Extra, old.Extra)
	}
	s.creds[cc.ID] = &cc
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	out := cc
	return &out, nil
}

// CredentialUsage counts sessions and groups that refer to a profile.
func (s *Store) CredentialUsage(id string) (sessions, groups int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.nodes {
		if n.Session != nil {
			used := n.Session.CredentialID == id
			for _, j := range n.Session.Jump {
				used = used || j.CredentialID == id
			}
			if used {
				sessions++
			}
		}
		if n.Defaults != nil && n.Defaults.CredentialID == id {
			groups++
		}
	}
	return
}

// DeleteCredential removes a profile. Sessions that used it keep the
// profile's user name and ask for the password from then on.
func (s *Store) DeleteCredential(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return ErrReadOnly
	}
	c, ok := s.creds[id]
	if !ok {
		return ErrNotFound
	}
	for _, n := range s.nodes {
		if ss := n.Session; ss != nil {
			if ss.CredentialID == id {
				ss.CredentialID = ""
				if c.User != "" {
					ss.User = c.User
				}
				if ss.Protocol == ProtoSSH {
					ss.Auth = AuthAsk
					if c.Auth == AuthPublicKey || c.Auth == AuthAgent {
						ss.Auth, ss.KeyPath, ss.CertPath = c.Auth, c.KeyPath, c.CertPath
					}
				}
			}
			for i := range ss.Jump {
				if ss.Jump[i].CredentialID == id {
					ss.Jump[i].CredentialID = ""
					ss.Jump[i].User = c.User
					ss.Jump[i].Auth = AuthAsk
				}
			}
		}
		if n.Defaults != nil && n.Defaults.CredentialID == id {
			n.Defaults.CredentialID = ""
			if n.Defaults.IsEmpty() {
				n.Defaults = nil
			}
		}
	}
	delete(s.creds, id)
	return s.saveLocked()
}

// ---- inheritance and effective settings -----------------------------------------

// Hop is one resolved SSH hop with the vault keys of its secrets.
type Hop struct {
	Name          string      `json:"name"` // session name when the hop is a saved session
	Host          string      `json:"host"`
	Port          int         `json:"port"`
	User          string      `json:"user"`
	Auth          AuthMethod  `json:"auth"`
	KeyPath       string      `json:"keyPath,omitempty"`
	CertPath      string      `json:"certPath,omitempty"`
	Algorithms    AlgoProfile `json:"algorithms,omitempty"`
	PasswordKey   string      `json:"-"`
	PassphraseKey string      `json:"-"`
	Credential    string      `json:"credential,omitempty"` // profile name, for display
}

// Effective is a session with group inheritance and profiles applied.
type Effective struct {
	User          string      `json:"user"`
	Auth          AuthMethod  `json:"auth"`
	KeyPath       string      `json:"keyPath,omitempty"`
	CertPath      string      `json:"certPath,omitempty"`
	PasswordKey   string      `json:"-"`
	PassphraseKey string      `json:"-"`
	CredentialID  string      `json:"credentialId,omitempty"`
	Credential    string      `json:"credential,omitempty"` // profile name
	Algorithms    AlgoProfile `json:"algorithms,omitempty"`
	JumpVia       string      `json:"jumpVia,omitempty"`
	Hops          []Hop       `json:"hops,omitempty"` // jump hops in order, target excluded
	Color         string      `json:"color,omitempty"`
	AntiIdle      int         `json:"antiIdle,omitempty"` // seconds, 0 = off
	AntiIdleText  string      `json:"antiIdleText,omitempty"`
	Proxy         *EffProxy   `json:"proxy,omitempty"` // outbound proxy for the first hop
	Warnings      []string    `json:"warnings,omitempty"`
}

// EffProxy is the outbound proxy a session uses, with its login.
type EffProxy struct {
	Type        string `json:"type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user,omitempty"`
	PasswordKey string `json:"-"`
	From        string `json:"from,omitempty"` // group name when inherited
}

// proxyLocked resolves the outbound proxy: the session's own, else the
// nearest group's; type "none" stops the search.
func (s *Store) proxyLocked(n *Node) (*EffProxy, string) {
	p, from := n.Session.Proxy, ""
	for cur, guard := n.ParentID, 0; p == nil && cur != "" && guard < 64; guard++ {
		g, ok := s.nodeLocked(cur)
		if !ok {
			break
		}
		if g.Defaults != nil && g.Defaults.Proxy != nil {
			p, from = g.Defaults.Proxy, g.Name
		}
		cur = g.ParentID
	}
	if p == nil || p.Type == "none" || p.Type == "" {
		return nil, ""
	}
	e := &EffProxy{Type: p.Type, Host: p.Host, Port: p.Port, From: from}
	warn := ""
	if p.CredentialID != "" {
		if c, ok := s.creds[p.CredentialID]; ok {
			e.User, e.PasswordKey = c.User, SecretCredPassword(c.ID)
		} else {
			warn = "the credential profile of the proxy no longer exists; connecting to the proxy without a login"
		}
	}
	return e, warn
}

// antiIdleLocked resolves the anti-idle interval and text of a session.
func (s *Store) antiIdleLocked(n *Node) (int, string) {
	sec, text := n.Session.AntiIdle, n.Session.AntiIdleText
	for cur, guard := n.ParentID, 0; cur != "" && guard < 64 && (sec == 0 || text == ""); guard++ {
		g, ok := s.nodeLocked(cur)
		if !ok {
			break
		}
		if d := g.Defaults; d != nil {
			if sec == 0 && d.AntiIdle != 0 {
				sec = d.AntiIdle
			}
			if text == "" {
				text = d.AntiIdleText
			}
		}
		cur = g.ParentID
	}
	if sec < 0 {
		sec = 0
	}
	if text == "" {
		text = DefaultAntiIdleText
	}
	return sec, text
}

// inheritedLocked returns the first non-empty value of get() on the
// ancestors of a node, starting with its parent.
func (s *Store) inheritedLocked(parentID string, get func(*GroupDefaults) string) string {
	for cur, guard := parentID, 0; cur != "" && guard < 64; guard++ {
		g, ok := s.nodeLocked(cur)
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

// colorLocked returns the colour of a node or its nearest coloured group.
func (s *Store) colorLocked(n *Node) string {
	for cur, guard := n, 0; cur != nil && guard < 64; guard++ {
		if cur.Color == "none" {
			return ""
		}
		if cur.Color != "" {
			return cur.Color
		}
		cur, _ = s.nodeLocked(cur.ParentID)
	}
	return ""
}

// Effective resolves inheritance, credential profiles and jump sessions.
// The node does not need to be in the store (quick connect).
func (s *Store) Effective(n *Node) (*Effective, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveLocked(n, map[string]bool{})
}

func (s *Store) effectiveLocked(n *Node, visiting map[string]bool) (*Effective, error) {
	if n == nil || n.Session == nil {
		return nil, errors.New("not a session")
	}
	ss := n.Session
	e := &Effective{Color: s.colorLocked(n)}
	e.AntiIdle, e.AntiIdleText = s.antiIdleLocked(n)

	cid := ss.CredentialID
	if cid == Inherit {
		cid = s.inheritedLocked(n.ParentID, func(d *GroupDefaults) string { return d.CredentialID })
	}
	slotMissing := ""
	if slot, ok := strings.CutPrefix(cid, SlotPrefix); ok {
		// A team library login: use the profile this user linked to it.
		cid, slotMissing = s.slotLocked(n.Library, slot)
	}
	if c, ok := s.creds[cid]; cid != "" && ok {
		e.CredentialID, e.Credential = c.ID, c.Name
		e.User, e.Auth, e.KeyPath, e.CertPath = c.User, c.Auth, c.KeyPath, c.CertPath
		if e.User == "" {
			e.User = ss.User
		}
		e.PasswordKey, e.PassphraseKey = SecretCredPassword(c.ID), SecretCredPassphrase(c.ID)
	} else {
		switch {
		case slotMissing != "":
			e.Warnings = append(e.Warnings, fmt.Sprintf("the team library login %q is not linked to one of your credential profiles (Settings > Team libraries); you will be asked", slotMissing))
		case cid != "":
			e.Warnings = append(e.Warnings, "the credential profile of this session no longer exists; using the session's own login")
		}
		e.User, e.Auth, e.KeyPath, e.CertPath = ss.User, ss.Auth, ss.KeyPath, ss.CertPath
		e.PasswordKey, e.PassphraseKey = SecretPassword(n.ID), SecretPassphrase(n.ID)
		if slotMissing != "" && ss.Protocol == ProtoSSH {
			e.Auth = AuthAsk
		}
	}
	if ss.Protocol != ProtoSSH {
		e.Auth, e.KeyPath, e.CertPath = "", "", ""
		if ss.Protocol == ProtoTelnet {
			// Telnet can go through SSH jump hosts too (a forwarded TCP
			// channel to port 23), and through the outbound proxy (to the
			// first jump host, or to the device).
			if err := s.jumpHopsLocked(n, e, visiting); err != nil {
				return nil, err
			}
			var warn string
			if e.Proxy, warn = s.proxyLocked(n); warn != "" {
				e.Warnings = append(e.Warnings, warn)
			}
		}
		return e, nil
	}
	if e.Auth == "" {
		e.Auth = AuthPassword
	}
	var warn string
	if e.Proxy, warn = s.proxyLocked(n); warn != "" {
		e.Warnings = append(e.Warnings, warn)
	}

	e.Algorithms = ss.Algorithms
	if e.Algorithms == Inherit {
		e.Algorithms = AlgoProfile(s.inheritedLocked(n.ParentID, func(d *GroupDefaults) string { return string(d.Algorithms) }))
	}
	if e.Algorithms == "" || e.Algorithms == Inherit {
		e.Algorithms = AlgoSecure
	}

	if err := s.jumpHopsLocked(n, e, visiting); err != nil {
		return nil, err
	}
	return e, nil
}

// jumpHopsLocked fills e.Hops (and e.JumpVia) with the jump hosts in front
// of a session: a saved session (its own, or the group's) or inline hops.
func (s *Store) jumpHopsLocked(n *Node, e *Effective, visiting map[string]bool) error {
	ss := n.Session
	via := ss.JumpVia
	if via == Inherit {
		via = s.inheritedLocked(n.ParentID, func(d *GroupDefaults) string { return d.JumpVia })
	}
	if via == n.ID {
		via = "" // a group default never makes the jump host jump through itself
	}
	if via != "" {
		e.JumpVia = via
		hops, err := s.viaHopsLocked(via, visiting, n.ID)
		if err != nil {
			return err
		}
		e.Hops = hops
		return nil
	}
	for i, j := range ss.Jump {
		h := Hop{Host: j.Host, Port: j.Port, User: j.User, Auth: j.Auth, KeyPath: j.KeyPath,
			PasswordKey: SecretJumpPassword(n.ID, i)}
		jc := j.CredentialID
		if slot, ok := strings.CutPrefix(jc, SlotPrefix); ok {
			var missing string
			if jc, missing = s.slotLocked(n.Library, slot); missing != "" {
				e.Warnings = append(e.Warnings, fmt.Sprintf("jump host %s: the team library login %q is not linked to one of your profiles", j.Host, missing))
			}
		}
		if c, ok := s.creds[jc]; jc != "" && ok {
			h.User, h.Auth, h.KeyPath, h.CertPath, h.Credential = c.User, c.Auth, c.KeyPath, c.CertPath, c.Name
			if h.User == "" {
				h.User = j.User
			}
			h.PasswordKey, h.PassphraseKey = SecretCredPassword(c.ID), SecretCredPassphrase(c.ID)
		}
		if h.Port == 0 {
			h.Port = 22
		}
		if h.Auth == "" {
			h.Auth = AuthAgent
		}
		e.Hops = append(e.Hops, h)
	}
	return nil
}

// viaHopsLocked returns the hops needed to reach the jump session plus the
// jump session itself.
func (s *Store) viaHopsLocked(id string, visiting map[string]bool, from string) ([]Hop, error) {
	j, ok := s.nodeLocked(id)
	if !ok || j.Session == nil {
		return nil, errors.New("the jump host session no longer exists; choose another one in the session settings")
	}
	if j.Session.Protocol != ProtoSSH {
		return nil, fmt.Errorf("jump host %q is not an SSH session", j.Name)
	}
	if visiting[id] || id == from || len(visiting) > 8 {
		return nil, fmt.Errorf("jump hosts loop through %q", j.Name)
	}
	visiting[id] = true
	defer delete(visiting, id)
	je, err := s.effectiveLocked(j, visiting)
	if err != nil {
		return nil, fmt.Errorf("jump host %q: %w", j.Name, err)
	}
	return append(je.Hops, Hop{
		Name: j.Name, Host: j.Session.Host, Port: j.Session.Port, User: je.User, Auth: je.Auth,
		KeyPath: je.KeyPath, CertPath: je.CertPath, Algorithms: je.Algorithms,
		PasswordKey: je.PasswordKey, PassphraseKey: je.PassphraseKey, Credential: je.Credential,
	}), nil
}

// UsedAsJump returns the names of sessions and groups that jump through id.
func (s *Store) UsedAsJump(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, n := range s.nodes {
		if (n.Session != nil && n.Session.JumpVia == id) || (n.Defaults != nil && n.Defaults.JumpVia == id) {
			out = append(out, n.Name)
		}
	}
	sort.Strings(out)
	return out
}

// ---- bulk edit -------------------------------------------------------------------

// BulkChange describes one change applied to many nodes. Nil fields are
// left alone.
type BulkChange struct {
	CredentialID   *string  `json:"credentialId"`
	User           *string  `json:"user"`
	JumpVia        *string  `json:"jumpVia"`
	ClearJumpHosts bool     `json:"clearJumpHosts"` // drop inline hops when JumpVia is set to ""
	Algorithms     *string  `json:"algorithms"`
	Port           *int     `json:"port"`
	Color          *string  `json:"color"`
	LogToFile      *bool    `json:"logToFile"`
	LogTimestamps  *bool    `json:"logTimestamps"`
	FontSize       *int     `json:"fontSize"`
	PasteDelay     *int     `json:"pasteDelay"`
	NoHighlight    *bool    `json:"noHighlight"`
	TermText       *string  `json:"termText"`
	TermBackground *string  `json:"termBackground"`
	AntiIdle       *int     `json:"antiIdle"` // 0 = from group, -1 = off
	AntiIdleText   *string  `json:"antiIdleText"`
	AddClusters    []string `json:"addClusters"`
	RemoveClusters []string `json:"removeClusters"`
	ParentID       *string  `json:"parentId"` // move the selected nodes into this group ("" = top level)
}

// SessionsUnder expands a selection: sessions stay, groups contribute every
// session inside them. Order follows the tree.
func (s *Store) SessionsUnder(ids []string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionsUnderLocked(ids)
}

func (s *Store) sessionsUnderLocked(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(id string)
	walk = func(id string) {
		n, ok := s.nodeLocked(id)
		if !ok || seen[id] {
			return
		}
		seen[id] = true
		if !n.IsGroup {
			out = append(out, id)
			return
		}
		var kids []*Node
		for _, c := range s.allLocked() {
			if c.ParentID == id {
				kids = append(kids, c)
			}
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].Order < kids[j].Order })
		for _, c := range kids {
			walk(c.ID)
		}
	}
	for _, id := range ids {
		walk(id)
	}
	return out
}

// BulkUpdate applies ch to the selection in one save and returns the IDs of
// the sessions that were changed. Colour and moving apply to the selected
// nodes themselves (a coloured or moved group carries its sessions along);
// everything else applies to every session in the selection.
func (s *Store) BulkUpdate(ids []string, ch BulkChange) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	if ch.Color != nil && !ValidColor(*ch.Color) {
		return nil, fmt.Errorf("unknown colour %q", *ch.Color)
	}
	if ch.AntiIdle != nil || ch.AntiIdleText != nil {
		sec, text := 0, ""
		if ch.AntiIdle != nil {
			sec = *ch.AntiIdle
		}
		if ch.AntiIdleText != nil {
			text = *ch.AntiIdleText
		}
		if err := validAntiIdle(sec, &text); err != nil {
			return nil, err
		}
		if ch.AntiIdleText != nil {
			ch.AntiIdleText = &text
		}
	}
	if ch.Algorithms != nil {
		switch AlgoProfile(*ch.Algorithms) {
		case AlgoSecure, AlgoCompatible, AlgoLegacy, Inherit:
		default:
			return nil, fmt.Errorf("unknown algorithm profile %q", *ch.Algorithms)
		}
	}
	if ch.CredentialID != nil && *ch.CredentialID != "" && *ch.CredentialID != Inherit {
		if _, ok := s.creds[*ch.CredentialID]; !ok {
			return nil, errors.New("credential profile not found")
		}
	}
	if ch.JumpVia != nil && *ch.JumpVia != "" && *ch.JumpVia != Inherit {
		j, ok := s.nodeLocked(*ch.JumpVia)
		if !ok || j.Session == nil || j.Session.Protocol != ProtoSSH {
			return nil, errors.New("the jump host must be a saved SSH session")
		}
	}
	if ch.Port != nil && (*ch.Port < 1 || *ch.Port > 65535) {
		return nil, fmt.Errorf("invalid port %d", *ch.Port)
	}
	if ch.ParentID != nil && *ch.ParentID != "" {
		p, ok := s.nodes[*ch.ParentID]
		if !ok || !p.IsGroup {
			return nil, errors.New("target group not found")
		}
		for _, id := range ids {
			if s.isAncestor(id, *ch.ParentID) {
				return nil, errors.New("cannot move a group into itself")
			}
		}
	}
	now := time.Now().UTC()
	for _, id := range ids {
		n, ok := s.nodes[id]
		if !ok {
			continue
		}
		if ch.Color != nil {
			n.Color = *ch.Color
			n.Updated = now
		}
		if ch.ParentID != nil && n.ParentID != *ch.ParentID {
			n.ParentID = *ch.ParentID
			n.Order = s.nextOrder(n.ParentID)
			n.Updated = now
		}
	}
	changed := s.sessionsUnderLocked(ids)
	for _, id := range changed {
		n, ok := s.nodes[id]
		if !ok || n.Session == nil {
			continue // team library sessions are read-only
		}
		ss := n.Session
		if !KnownProtocol(ss.Protocol) {
			// A connection type from a newer YACM: only the generic
			// cluster list is changed; its other settings mean something
			// this version does not know.
			if len(ch.AddClusters) > 0 || len(ch.RemoveClusters) > 0 {
				ss.Clusters = editClusters(ss.Clusters, ch.AddClusters, ch.RemoveClusters)
				n.Updated = now
			}
			continue
		}
		if ch.CredentialID != nil {
			ss.CredentialID = *ch.CredentialID
		}
		if ch.User != nil {
			ss.User = *ch.User
		}
		if ch.LogToFile != nil {
			ss.LogToFile = *ch.LogToFile
		}
		if ch.LogTimestamps != nil {
			ss.LogTimestamps = *ch.LogTimestamps
		}
		if ch.FontSize != nil {
			ss.FontSize = *ch.FontSize
		}
		if ch.PasteDelay != nil {
			ss.PasteDelay = *ch.PasteDelay
		}
		if ch.NoHighlight != nil {
			ss.NoHighlight = *ch.NoHighlight
		}
		if ch.TermText != nil && ValidTermColor(*ch.TermText) {
			ss.TermText = *ch.TermText
		}
		if ch.TermBackground != nil && ValidTermColor(*ch.TermBackground) {
			ss.TermBackground = *ch.TermBackground
		}
		if ch.AntiIdle != nil {
			ss.AntiIdle = *ch.AntiIdle
		}
		if ch.AntiIdleText != nil {
			ss.AntiIdleText = *ch.AntiIdleText
		}
		if len(ch.AddClusters) > 0 || len(ch.RemoveClusters) > 0 {
			ss.Clusters = editClusters(ss.Clusters, ch.AddClusters, ch.RemoveClusters)
		}
		if ss.Protocol == ProtoSSH || ss.Protocol == ProtoTelnet {
			if ch.Port != nil {
				ss.Port = *ch.Port
			}
		}
		if ss.Protocol == ProtoSSH && ch.Algorithms != nil {
			ss.Algorithms = AlgoProfile(*ch.Algorithms)
		}
		if (ss.Protocol == ProtoSSH || ss.Protocol == ProtoTelnet) && ch.JumpVia != nil && *ch.JumpVia != id {
			ss.JumpVia = *ch.JumpVia
			if ss.JumpVia != "" || ch.ClearJumpHosts {
				ss.Jump = nil
			}
		}
		n.Updated = now
	}
	return changed, s.saveLocked()
}

func editClusters(cur, add, remove []string) []string {
	drop := map[string]bool{}
	for _, r := range remove {
		drop[strings.ToLower(strings.TrimSpace(r))] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range append(append([]string{}, cur...), add...) {
		c = strings.TrimSpace(c)
		k := strings.ToLower(c)
		if c == "" || drop[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}
