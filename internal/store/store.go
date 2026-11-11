// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/icavar/yacm/internal/jsonx"
)

// schemaVersion 2 added credential profiles, group defaults and colours.
// Older YACM versions refuse a version 2 file instead of silently dropping
// the profiles when they save.
const schemaVersion = 2

type fileFormat struct {
	Version     int           `json:"version"`
	Settings    Settings      `json:"settings"`
	Credentials []*Credential `json:"credentials,omitempty"`
	Nodes       []*Node       `json:"nodes"`
	// Extra keeps top-level sections written by a newer YACM.
	Extra jsonx.Unknown `json:"-"`
}

// Store is a thread-safe, file-backed session tree.
type Store struct {
	mu       sync.RWMutex
	path     string
	nodes    map[string]*Node
	creds    map[string]*Credential
	settings Settings
	// fileExtra keeps top-level sections of sessions.json written by a newer
	// YACM, so saving here does not drop them.
	fileExtra jsonx.Unknown
	readOnly  bool
	// beforeSave runs (under the lock) just before sessions.json is written,
	// for automatic backups. It must not call back into the store.
	beforeSave func()

	// Team libraries: their nodes are shown and used like your own but are
	// read-only and never saved here (see team.go).
	overlay map[string]*Node
	libs    map[string]*LibraryView
}

// ErrNotFound is returned for unknown node IDs.
var ErrNotFound = errors.New("node not found")

// NewID returns a random UUIDv4 string.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Open loads the store from path, creating an empty one if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, nodes: map[string]*Node{}, creds: map[string]*Credential{}, settings: DefaultSettings()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Version > schemaVersion {
		return nil, fmt.Errorf("%s was written by a newer version (schema %d)", path, f.Version)
	}
	s.fileExtra = f.Extra
	if f.Settings.FontSize == 0 {
		// Settings with font size 0 (a hand edit, another tool) are still
		// settings: keep them over the defaults rather than drop them all,
		// team libraries included, at the next save.
		var probe struct {
			Settings json.RawMessage `json:"settings"`
		}
		if json.Unmarshal(data, &probe) == nil && len(bytes.TrimSpace(probe.Settings)) > 2 {
			st := DefaultSettings()
			if json.Unmarshal(probe.Settings, &st) == nil {
				if st.FontSize == 0 {
					st.FontSize = DefaultSettings().FontSize
				}
				f.Settings = st
			}
		}
	}
	if f.Settings.FontSize != 0 {
		s.settings = f.Settings
		if f.Version < 2 {
			s.settings.Highlight = true // schema 1 had no such setting: on unless switched off
		}
		if s.settings.HighlightRules == nil {
			s.settings.HighlightRules = DefaultHighlightRules()
		}
		if !s.settings.AlertRulesAdded {
			// Settings from before the alert rules: the standard alert rules go in front of the
			// rules people already have (once; removing them later sticks).
			s.settings.HighlightRules = addAlertRules(s.settings.HighlightRules)
			s.settings.AlertRulesAdded = true
		}
		if !s.settings.LinkAlertQuiet {
			s.settings.HighlightRules = quietLinkAlert(s.settings.HighlightRules)
			s.settings.LinkAlertQuiet = true
		}
		if s.settings.Snippets == nil {
			s.settings.Snippets = DefaultSnippets()
		}
	}
	for _, c := range f.Credentials {
		if c != nil && c.ID != "" {
			s.creds[c.ID] = c
		}
	}
	for _, n := range f.Nodes {
		if n == nil || n.ID == "" {
			continue
		}
		s.nodes[n.ID] = n
	}
	// Re-home orphans so a damaged file never hides sessions.
	for _, n := range s.nodes {
		if n.ParentID != "" {
			if p, ok := s.nodes[n.ParentID]; !ok || !p.IsGroup {
				n.ParentID = ""
			}
		}
	}
	return s, nil
}

// Path returns the backing file path.
// ErrReadOnly is returned by changes while the store is read-only (the data
// folder is in use by YACM on another computer, or is not available).
var ErrReadOnly = errors.New("this window is read-only: the data folder is in use elsewhere, so changes cannot be saved")

// SetReadOnly makes every change fail with ErrReadOnly. Call it before the
// store is used.
func (s *Store) SetReadOnly(ro bool) {
	s.mu.Lock()
	s.readOnly = ro
	s.mu.Unlock()
}

// ReadOnly reports whether changes are refused.
func (s *Store) ReadOnly() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readOnly
}

func (s *Store) Path() string { return s.path }

// SetBeforeSave installs a hook that runs before every write of the file.
func (s *Store) SetBeforeSave(f func()) {
	s.mu.Lock()
	s.beforeSave = f
	s.mu.Unlock()
}

func (s *Store) saveLocked() error {
	f := fileFormat{Version: schemaVersion, Settings: s.settings, Extra: s.fileExtra}
	for _, n := range s.nodes {
		f.Nodes = append(f.Nodes, n)
	}
	for _, c := range s.creds {
		f.Credentials = append(f.Credentials, c)
	}
	sort.Slice(f.Credentials, func(i, j int) bool {
		return strings.ToLower(f.Credentials[i].Name) < strings.ToLower(f.Credentials[j].Name)
	})
	sort.Slice(f.Nodes, func(i, j int) bool {
		if f.Nodes[i].ParentID != f.Nodes[j].ParentID {
			return f.Nodes[i].ParentID < f.Nodes[j].ParentID
		}
		return f.Nodes[i].Order < f.Nodes[j].Order
	})
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if s.beforeSave != nil {
		s.beforeSave()
	}
	return WriteFileAtomic(s.path, data, 0o600)
}

// WriteFileAtomic writes data to a temp file in the same directory and renames it.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func clone(n *Node) *Node {
	data, _ := json.Marshal(n)
	var c Node
	_ = json.Unmarshal(data, &c)
	return &c
}

// List returns copies of all nodes ordered by parent then order.
func (s *Store) List() []*Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, clone(n))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ParentID != out[j].ParentID {
			return out[i].ParentID < out[j].ParentID
		}
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Get returns a copy of one node.
func (s *Store) Get(id string) (*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodeLocked(id)
	if !ok {
		return nil, ErrNotFound
	}
	return clone(n), nil
}

// Settings returns application settings.
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetSettings replaces application settings.
func (s *Store) SetSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return ErrReadOnly
	}
	if st.FontSize < 6 || st.FontSize > 48 {
		st.FontSize = DefaultSettings().FontSize
	}
	if st.Scrollback <= 0 {
		st.Scrollback = DefaultSettings().Scrollback
	}
	if st.FontWeight != "light" && st.FontWeight != "medium" {
		st.FontWeight = ""
	}
	if st.RightClick != "paste" {
		st.RightClick = "menu"
	}
	if st.UpdateCheck != "daily" && st.UpdateCheck != "off" {
		st.UpdateCheck = ""
	}
	if st.HostnameRename != "always" && st.HostnameRename != "off" {
		st.HostnameRename = "ask"
	}
	if st.PasteDelay < 0 || st.PasteDelay > 10000 {
		st.PasteDelay = 0
	}
	if st.ReloadWaitMinutes < 0 || st.ReloadWaitMinutes > 240 {
		st.ReloadWaitMinutes = 0
	}
	if st.AskTicket != "all" && st.AskTicket != "groups" {
		st.AskTicket = ""
	}
	if !ValidTermColor(st.TermText) || st.TermText == TermColorTheme {
		st.TermText = ""
	}
	if !ValidTermColor(st.TermBackground) || st.TermBackground == TermColorTheme {
		st.TermBackground = ""
	}
	if st.SidebarWidth != 0 && (st.SidebarWidth < 180 || st.SidebarWidth > 900) {
		st.SidebarWidth = 0
	}
	if st.HighlightRules == nil {
		st.HighlightRules = []HighlightRule{}
	}
	if st.Snippets == nil {
		st.Snippets = []Snippet{}
	}
	for i := range st.HighlightRules {
		if st.HighlightRules[i].ID == "" {
			st.HighlightRules[i].ID = NewID()
		}
	}
	for i := range st.Snippets {
		if st.Snippets[i].ID == "" {
			st.Snippets[i].ID = NewID()
		}
	}
	st.Extra = jsonx.Keep(st.Extra, s.settings.Extra)
	s.settings = st
	return s.saveLocked()
}

func (s *Store) isAncestor(ancestorID, id string) bool {
	for cur := id; cur != ""; {
		if cur == ancestorID {
			return true
		}
		n, ok := s.nodes[cur]
		if !ok {
			return false
		}
		cur = n.ParentID
	}
	return false
}

func (s *Store) nextOrder(parentID string) int {
	max := -1
	for _, n := range s.nodes {
		if n.ParentID == parentID && n.Order > max {
			max = n.Order
		}
	}
	return max + 1
}

// Validate checks a node before saving it.
func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func Validate(n *Node) error {
	if strings.TrimSpace(n.Name) == "" {
		return errors.New("name is required")
	}
	// Names go into terminal output and commands (<NAME>).
	if hasControl(n.Name) {
		return errors.New("the name contains control characters")
	}
	if !ValidColor(n.Color) {
		return fmt.Errorf("unknown colour %q", n.Color)
	}
	if d := n.Defaults; d != nil && n.IsGroup {
		if !ValidTermColor(d.TermText) || !ValidTermColor(d.TermBackground) {
			return errors.New("terminal colours must look like #1a2b3c")
		}
	}
	if n.IsGroup {
		n.Session = nil
		if n.Defaults.IsEmpty() {
			n.Defaults = nil
		} else if a := n.Defaults.Algorithms; a != "" && a != AlgoSecure && a != AlgoCompatible && a != AlgoLegacy {
			return fmt.Errorf("unknown algorithm profile %q", a)
		}
		if n.Defaults != nil {
			if err := validAntiIdle(n.Defaults.AntiIdle, &n.Defaults.AntiIdleText); err != nil {
				return err
			}
			if err := validProxy(n.Defaults.Proxy); err != nil {
				return err
			}
		}
		return nil
	}
	n.Defaults = nil
	if n.Session == nil {
		return errors.New("session settings missing")
	}
	ss := n.Session
	switch ss.Protocol {
	case ProtoSSH, ProtoTelnet:
		if strings.TrimSpace(ss.Host) == "" {
			return errors.New("host is required")
		}
		// Hosts go into protocol lines (an HTTP CONNECT to a proxy, say):
		// control characters could add lines of their own.
		if hasControl(ss.Host) {
			return errors.New("the host contains control characters")
		}
		for _, j := range ss.Jump {
			if hasControl(j.Host) {
				return errors.New("a jump host contains control characters")
			}
		}
		if ss.Proxy != nil && hasControl(ss.Proxy.Host) {
			return errors.New("the proxy host contains control characters")
		}
		if ss.Port == 0 {
			ss.Port = DefaultPort(ss.Protocol)
		}
		if ss.Port < 1 || ss.Port > 65535 {
			return fmt.Errorf("invalid port %d", ss.Port)
		}
	case ProtoSerial:
		if ss.Serial == nil || strings.TrimSpace(ss.Serial.Device) == "" {
			return errors.New("serial device is required")
		}
		if ss.Serial.Baud == 0 {
			ss.Serial.Baud = 9600
		}
		if ss.Serial.DataBits == 0 {
			ss.Serial.DataBits = 8
		}
		if ss.Serial.Parity == "" {
			ss.Serial.Parity = "none"
		}
		if ss.Serial.StopBits == "" {
			ss.Serial.StopBits = "1"
		}
	case "":
		return errors.New("connection type is required")
	default:
		// A connection type from a newer YACM: keep the session exactly as
		// it is (it cannot be opened or edited here).
		return nil
	}
	if ss.PasteDelay < -1 || ss.PasteDelay > 10000 {
		ss.PasteDelay = 0
	}
	if !ValidTermColor(ss.TermText) || !ValidTermColor(ss.TermBackground) {
		return errors.New("terminal colours must look like #1a2b3c")
	}
	if err := validAntiIdle(ss.AntiIdle, &ss.AntiIdleText); err != nil {
		return err
	}
	if ss.Protocol != ProtoSSH && ss.Protocol != ProtoTelnet {
		ss.JumpVia, ss.Jump = "", nil
	}
	if ss.JumpVia != "" && ss.JumpVia == n.ID {
		return errors.New("a session cannot be its own jump host")
	}
	for i := range ss.Jump {
		if strings.TrimSpace(ss.Jump[i].Host) == "" {
			return fmt.Errorf("jump host %d: host is required", i+1)
		}
		if ss.Jump[i].Port == 0 {
			ss.Jump[i].Port = 22
		}
		if ss.Jump[i].Auth == "" {
			ss.Jump[i].Auth = AuthAgent
		}
	}
	if ss.Protocol == ProtoSSH {
		if ss.Auth == "" {
			ss.Auth = AuthPassword
		}
		switch ss.Algorithms {
		case "":
			ss.Algorithms = AlgoSecure
		case AlgoSecure, AlgoCompatible, AlgoLegacy, Inherit:
		default:
			return fmt.Errorf("unknown algorithm profile %q", ss.Algorithms)
		}
	}
	for i := range ss.Expect {
		if ss.Expect[i].ID == "" {
			ss.Expect[i].ID = NewID()
		}
		if ss.Expect[i].OnFail == "" {
			ss.Expect[i].OnFail = "stop"
		}
	}
	if ss.Protocol != ProtoSSH {
		ss.Tunnels = nil
	}
	for i := range ss.Tunnels {
		if err := validTunnel(&ss.Tunnels[i]); err != nil {
			return fmt.Errorf("tunnel %d: %w", i+1, err)
		}
	}
	if err := validProxy(ss.Proxy); err != nil {
		return err
	}
	return nil
}

func validTunnel(t *Tunnel) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	switch t.Type {
	case "local", "remote", "dynamic":
	default:
		return fmt.Errorf("unknown type %q", t.Type)
	}
	if t.Port < 0 || t.Port > 65535 || (t.Type != "remote" && t.Port == 0) {
		return errors.New("the listen port must be 1 to 65535")
	}
	if t.Bind != "" && t.Bind != "localhost" && net.ParseIP(t.Bind) == nil {
		return fmt.Errorf("%q is not an IP address", t.Bind)
	}
	if t.Type == "dynamic" {
		t.Host, t.HostPort = "", 0
		return nil
	}
	t.Host = strings.TrimSpace(t.Host)
	if t.Host == "" || strings.ContainsAny(t.Host, " /") {
		return errors.New("the destination host is required")
	}
	if t.HostPort < 1 || t.HostPort > 65535 {
		return errors.New("the destination port must be 1 to 65535")
	}
	return nil
}

func validProxy(p *Proxy) error {
	if p == nil {
		return nil
	}
	switch p.Type {
	case "none":
		p.Host, p.Port, p.CredentialID = "", 0, ""
		return nil
	case "socks5", "http":
	default:
		return fmt.Errorf("unknown proxy type %q", p.Type)
	}
	p.Host = strings.TrimSpace(p.Host)
	if p.Host == "" {
		return errors.New("proxy: the host is required")
	}
	if p.Port == 0 {
		p.Port = map[string]int{"socks5": 1080, "http": 3128}[p.Type]
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("proxy: invalid port")
	}
	return nil
}

// validAntiIdle checks an anti-idle interval (0 = inherit, -1 = off,
// otherwise 10 seconds to 1 hour) and cleans up its text.
func validAntiIdle(sec int, text *string) error {
	if sec != 0 && sec != -1 && (sec < 10 || sec > 3600) {
		return errors.New("anti-idle: send after 10 to 3600 seconds")
	}
	t := strings.TrimRight(*text, "\r\n")
	if strings.ContainsAny(t, "\r\n") {
		return errors.New("anti-idle: the text must be a single line")
	}
	if len(t) > 64 {
		return errors.New("anti-idle: the text is too long (64 characters at most)")
	}
	*text = t
	return nil
}

// Upsert creates or updates a node. A new node gets an ID and is appended to
// its parent. It returns the stored copy.
func (s *Store) Upsert(n *Node) (*Node, error) {
	if err := Validate(n); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	if err := s.libraryRefusalLocked(n.ID, n.ParentID); err != nil {
		return nil, err
	}
	if n.ParentID != "" {
		p, ok := s.nodes[n.ParentID]
		if !ok || !p.IsGroup {
			return nil, errors.New("parent group not found")
		}
	}
	c := clone(n)
	c.Updated = time.Now().UTC()
	if old, ok := s.nodes[c.ID]; ok && c.ID != "" {
		if old.IsGroup != c.IsGroup {
			return nil, errors.New("cannot change a group into a session")
		}
		keepUnknownNode(c, old)
		if c.ParentID != old.ParentID {
			if s.isAncestor(c.ID, c.ParentID) {
				return nil, errors.New("cannot move a group into itself")
			}
			c.Order = s.nextOrder(c.ParentID)
		}
	} else {
		if c.ID == "" {
			c.ID = NewID()
		}
		c.Order = s.nextOrder(c.ParentID)
	}
	s.nodes[c.ID] = c
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return clone(c), nil
}

// ImportNodes inserts already-built nodes in one save (used by importers).
func (s *Store) ImportNodes(nodes []*Node) error { return s.ApplyImport(nodes, nil, nil) }

// ApplyImport inserts new nodes, replaces existing ones and adds or
// replaces credential profiles in one save. Inserted nodes keep their
// relative order and are appended after the current children of their
// parent.
func (s *Store) ApplyImport(insert, update []*Node, creds []*Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return ErrReadOnly
	}
	for _, c := range creds {
		if c == nil || c.ID == "" {
			return errors.New("credential profile without id")
		}
	}
	for _, c := range creds {
		cc := *c
		cc.Updated = time.Now().UTC()
		s.creds[cc.ID] = &cc
	}
	for _, n := range insert {
		if _, exists := s.nodes[n.ID]; exists {
			return fmt.Errorf("duplicate node id %s", n.ID)
		}
	}
	for _, n := range update {
		cur, ok := s.nodes[n.ID]
		if !ok || cur.IsGroup != n.IsGroup {
			return fmt.Errorf("cannot replace node %s", n.ID)
		}
	}
	now := time.Now().UTC()
	for _, n := range update {
		c := clone(n)
		c.Updated = now
		s.nodes[c.ID] = c
	}
	base := map[string]int{}
	collapse := collapseImported(insert)
	for _, n := range insert {
		c := clone(n)
		c.Updated = now
		if collapse[c.ID] {
			c.Expanded = false
		}
		if c.Session != nil && !s.settings.LogNewOff {
			c.Session.LogToFile = true // imported sessions log, like new ones
		}
		if _, ok := base[c.ParentID]; !ok {
			base[c.ParentID] = s.nextOrder(c.ParentID)
		}
		c.Order += base[c.ParentID]
		s.nodes[c.ID] = c
	}
	// Never leave a node pointing at a missing parent.
	for _, n := range s.nodes {
		if n.ParentID != "" {
			if p, ok := s.nodes[n.ParentID]; !ok || !p.IsGroup {
				n.ParentID = ""
			}
		}
	}
	return s.saveLocked()
}

// importOpenLimit: an import with more sessions than this keeps its
// folders closed, so thousands of sessions do not unfold the whole tree.
const importOpenLimit = 30

// collapseImported returns the groups of a large import to show closed:
// every group inside the import; its top groups stay as they are.
func collapseImported(insert []*Node) map[string]bool {
	ids := map[string]bool{}
	sessions := 0
	for _, n := range insert {
		ids[n.ID] = true
		if !n.IsGroup {
			sessions++
		}
	}
	out := map[string]bool{}
	if sessions <= importOpenLimit {
		return out
	}
	for _, n := range insert {
		if n.IsGroup && ids[n.ParentID] {
			out[n.ID] = true
		}
	}
	return out
}

// SetExpanded remembers whether a group is open in the tree.
func (s *Store) SetExpanded(id string, open bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.overlay[id]; ok {
		o.Expanded = open // remembered while YACM runs
		return nil
	}
	n, ok := s.nodes[id]
	if !ok {
		return ErrNotFound
	}
	if n.Expanded == open {
		return nil
	}
	n.Expanded = open
	if s.readOnly {
		return nil // folding the tree is fine in a read-only window
	}
	return s.saveLocked()
}

// Delete removes a node and, for groups, everything under it. It returns the
// IDs that were removed so the caller can purge their secrets.
func (s *Store) Delete(id string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, ErrReadOnly
	}
	if err := s.libraryRefusalLocked(id, ""); err != nil {
		return nil, err
	}
	if _, ok := s.nodes[id]; !ok {
		return nil, ErrNotFound
	}
	var removed []string
	var walk func(string)
	walk = func(cur string) {
		for cid, c := range s.nodes {
			if c.ParentID == cur {
				walk(cid)
			}
		}
		delete(s.nodes, cur)
		removed = append(removed, cur)
	}
	walk(id)
	gone := map[string]bool{}
	for _, r := range removed {
		gone[r] = true
	}
	s.clearMissingRefsLocked(gone)
	return removed, s.saveLocked()
}

// clearMissingRefsLocked removes jump-host references to deleted sessions.
func (s *Store) clearMissingRefsLocked(gone map[string]bool) {
	for _, n := range s.nodes {
		if n.Session != nil && gone[n.Session.JumpVia] {
			n.Session.JumpVia = ""
		}
		if n.Defaults != nil && gone[n.Defaults.JumpVia] {
			n.Defaults.JumpVia = ""
			if n.Defaults.IsEmpty() {
				n.Defaults = nil
			}
		}
	}
}

// Move puts a node under newParent at position index (-1 = end).
func (s *Store) Move(id, newParent string, index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return ErrReadOnly
	}
	if err := s.libraryRefusalLocked(id, newParent); err != nil {
		return err
	}
	n, ok := s.nodes[id]
	if !ok {
		return ErrNotFound
	}
	if newParent != "" {
		p, ok := s.nodes[newParent]
		if !ok || !p.IsGroup {
			return errors.New("target is not a group")
		}
		if s.isAncestor(id, newParent) {
			return errors.New("cannot move a group into itself")
		}
	}
	var siblings []*Node
	for _, c := range s.nodes {
		if c.ParentID == newParent && c.ID != id {
			siblings = append(siblings, c)
		}
	}
	sort.Slice(siblings, func(i, j int) bool { return siblings[i].Order < siblings[j].Order })
	if index < 0 || index > len(siblings) {
		index = len(siblings)
	}
	siblings = append(siblings[:index], append([]*Node{n}, siblings[index:]...)...)
	n.ParentID = newParent
	for i, c := range siblings {
		c.Order = i
	}
	n.Updated = time.Now().UTC()
	return s.saveLocked()
}

// Duplicate copies a session (not groups) next to the original and returns
// the copy plus a map of old->new expect rule IDs (for copying secrets).
func (s *Store) Duplicate(id string) (*Node, map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, nil, ErrReadOnly
	}
	if err := s.libraryRefusalLocked(id, ""); err != nil {
		return nil, nil, err
	}
	n, ok := s.nodes[id]
	if !ok {
		return nil, nil, ErrNotFound
	}
	if n.IsGroup {
		return nil, nil, errors.New("only sessions can be duplicated")
	}
	c := clone(n)
	c.ID = NewID()
	c.Name = n.Name + " (copy)"
	c.Order = s.nextOrder(c.ParentID)
	c.Updated = time.Now().UTC()
	ruleMap := map[string]string{}
	for i := range c.Session.Expect {
		nid := NewID()
		ruleMap[c.Session.Expect[i].ID] = nid
		c.Session.Expect[i].ID = nid
	}
	s.nodes[c.ID] = c
	return clone(c), ruleMap, s.saveLocked()
}

// SessionsInCluster returns session IDs that belong to the named cluster.
func (s *Store) SessionsInCluster(name string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []*Node
	for _, n := range s.allLocked() {
		if n.Session == nil {
			continue
		}
		for _, c := range n.Session.Clusters {
			if strings.EqualFold(c, name) {
				ids = append(ids, n)
				break
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return strings.ToLower(ids[i].Name) < strings.ToLower(ids[j].Name) })
	out := make([]string, len(ids))
	for i, n := range ids {
		out[i] = n.ID
	}
	return out
}

// Clusters returns all cluster names with member counts.
func (s *Store) Clusters() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{}
	for _, n := range s.allLocked() {
		if n.Session == nil {
			continue
		}
		for _, c := range n.Session.Clusters {
			if c = strings.TrimSpace(c); c != "" {
				out[c]++
			}
		}
	}
	return out
}

// addAlertRules puts the standard alert rules that are missing in front.
func addAlertRules(rules []HighlightRule) []HighlightRule {
	have := map[string]bool{}
	for _, r := range rules {
		have[r.ID] = true
	}
	var out []HighlightRule
	for _, r := range DefaultAlertRules() {
		if !have[r.ID] {
			out = append(out, r)
		}
	}
	return append(out, rules...)
}
