// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"errors"
	"sort"
)

// SlotPrefix marks a credential reference to a named login of a team
// library ("slot:<id>"); each user links it to one of their own profiles.
const SlotPrefix = "slot:"

// LibraryView is a team library as the store shows it: its nodes with IDs
// already made unique (see team.NodeID) and the profiles this user linked
// to its logins.
type LibraryView struct {
	ID        string
	Name      string
	Nodes     []*Node           // Library set, IDs unique
	Slots     map[string]string // login ID -> credential profile ID
	SlotNames map[string]string // login ID -> login name (for messages)
}

// ErrLibraryNode is returned when a team library node would be changed.
var ErrLibraryNode = errors.New("this belongs to a team library and cannot be changed here; use “Copy to my sessions” to make your own copy")

// SetLibraries replaces the team library nodes. Fold states survive a
// reload of the same library.
func (s *Store) SetLibraries(libs []*LibraryView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.overlay
	s.overlay = map[string]*Node{}
	s.libs = map[string]*LibraryView{}
	for _, l := range libs {
		s.libs[l.ID] = l
		for _, n := range l.Nodes {
			if _, clash := s.nodes[n.ID]; clash {
				continue // never hide one of your own nodes
			}
			c := clone(n)
			c.Library = l.ID
			if o, ok := old[c.ID]; ok {
				c.Expanded = o.Expanded
			}
			s.overlay[c.ID] = c
		}
	}
}

// ListAll returns your nodes followed by the team library nodes.
func (s *Store) ListAll() []*Node {
	out := s.List()
	s.mu.RLock()
	defer s.mu.RUnlock()
	extra := make([]*Node, 0, len(s.overlay))
	for _, n := range s.overlay {
		extra = append(extra, clone(n))
	}
	sort.Slice(extra, func(i, j int) bool {
		if extra[i].ParentID != extra[j].ParentID {
			return extra[i].ParentID < extra[j].ParentID
		}
		return extra[i].Order < extra[j].Order
	})
	return append(out, extra...)
}

// IsLibraryNode reports whether id is a team library node.
func (s *Store) IsLibraryNode(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.overlay[id]
	return ok
}

func (s *Store) nodeLocked(id string) (*Node, bool) {
	if n, ok := s.nodes[id]; ok {
		return n, true
	}
	n, ok := s.overlay[id]
	return n, ok
}

// allLocked lists your nodes and the library nodes (not copies).
func (s *Store) allLocked() []*Node {
	out := make([]*Node, 0, len(s.nodes)+len(s.overlay))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	for _, n := range s.overlay {
		out = append(out, n)
	}
	return out
}

// libraryRefusalLocked refuses changes to library nodes and putting your
// own nodes into a library group.
func (s *Store) libraryRefusalLocked(id, parentID string) error {
	if _, ok := s.overlay[id]; ok && id != "" {
		return ErrLibraryNode
	}
	if _, ok := s.overlay[parentID]; ok && parentID != "" {
		return errors.New("team library groups are read-only; put your own sessions into one of your groups")
	}
	return nil
}

// slotLocked maps a library login to the linked profile; missing is the
// login's name when it is not linked.
func (s *Store) slotLocked(lib, slot string) (credID, missing string) {
	l := s.libs[lib]
	if l == nil {
		return "", slot
	}
	if c := l.Slots[slot]; c != "" {
		if _, ok := s.creds[c]; ok {
			return c, ""
		}
	}
	name := l.SlotNames[slot]
	if name == "" {
		name = slot
	}
	return "", name
}

// LibraryOf returns the library name of a node, or "".
func (s *Store) LibraryOf(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n, ok := s.overlay[id]; ok {
		if l := s.libs[n.Library]; l != nil {
			return l.Name
		}
		return n.Library
	}
	return ""
}
