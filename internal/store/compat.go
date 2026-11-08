// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import "github.com/icavar/yacm/internal/jsonx"

// Forward compatibility: every stored type keeps the JSON members it does not
// know (written by a newer YACM) and writes them back unchanged, so sessions,
// .yacm files and team libraries survive a trip through an older version.
// New features must therefore be additive: new members and new protocols,
// never a changed meaning of an existing member (that needs a new
// schemaVersion, which older versions refuse to open).

func (v *Node) UnmarshalJSON(b []byte) error {
	type plain Node
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Node) MarshalJSON() ([]byte, error) { type plain Node; return jsonx.Marshal(plain(v), v.Extra) }

func (v *Session) UnmarshalJSON(b []byte) error {
	type plain Session
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Session) MarshalJSON() ([]byte, error) {
	type plain Session
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *GroupDefaults) UnmarshalJSON(b []byte) error {
	type plain GroupDefaults
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v GroupDefaults) MarshalJSON() ([]byte, error) {
	type plain GroupDefaults
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *Credential) UnmarshalJSON(b []byte) error {
	type plain Credential
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Credential) MarshalJSON() ([]byte, error) {
	type plain Credential
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *HighlightRule) UnmarshalJSON(b []byte) error {
	type plain HighlightRule
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v HighlightRule) MarshalJSON() ([]byte, error) {
	type plain HighlightRule
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *Snippet) UnmarshalJSON(b []byte) error {
	type plain Snippet
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Snippet) MarshalJSON() ([]byte, error) {
	type plain Snippet
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *JumpHost) UnmarshalJSON(b []byte) error {
	type plain JumpHost
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v JumpHost) MarshalJSON() ([]byte, error) {
	type plain JumpHost
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *Tunnel) UnmarshalJSON(b []byte) error {
	type plain Tunnel
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Tunnel) MarshalJSON() ([]byte, error) {
	type plain Tunnel
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *Proxy) UnmarshalJSON(b []byte) error {
	type plain Proxy
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Proxy) MarshalJSON() ([]byte, error) {
	type plain Proxy
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *ExpectRule) UnmarshalJSON(b []byte) error {
	type plain ExpectRule
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v ExpectRule) MarshalJSON() ([]byte, error) {
	type plain ExpectRule
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *LocalCommand) UnmarshalJSON(b []byte) error {
	type plain LocalCommand
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v LocalCommand) MarshalJSON() ([]byte, error) {
	type plain LocalCommand
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *Macro) UnmarshalJSON(b []byte) error {
	type plain Macro
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Macro) MarshalJSON() ([]byte, error) {
	type plain Macro
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *Serial) UnmarshalJSON(b []byte) error {
	type plain Serial
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Serial) MarshalJSON() ([]byte, error) {
	type plain Serial
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *Settings) UnmarshalJSON(b []byte) error {
	type plain Settings
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v Settings) MarshalJSON() ([]byte, error) {
	type plain Settings
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *TeamLibraryRef) UnmarshalJSON(b []byte) error {
	type plain TeamLibraryRef
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v TeamLibraryRef) MarshalJSON() ([]byte, error) {
	type plain TeamLibraryRef
	return jsonx.Marshal(plain(v), v.Extra)
}

func (v *fileFormat) UnmarshalJSON(b []byte) error {
	type plain fileFormat
	return jsonx.Unmarshal(b, (*plain)(v), &v.Extra)
}
func (v fileFormat) MarshalJSON() ([]byte, error) {
	type plain fileFormat
	return jsonx.Marshal(plain(v), v.Extra)
}

// KnownProtocol reports whether this version can connect with p. Sessions
// with other protocols (from a newer YACM) are kept and shown, but cannot be
// opened or edited here.
func KnownProtocol(p Protocol) bool {
	switch p {
	case ProtoSSH, ProtoTelnet, ProtoSerial:
		return true
	}
	return false
}

// keepUnknownNode carries the members a newer YACM wrote from the stored copy
// old into c, where c came back from the UI or another path that may have
// dropped them.
func keepUnknownNode(c, old *Node) {
	if c == nil || old == nil {
		return
	}
	c.Extra = jsonx.Keep(c.Extra, old.Extra)
	if c.Defaults != nil && old.Defaults != nil {
		c.Defaults.Extra = jsonx.Keep(c.Defaults.Extra, old.Defaults.Extra)
		if c.Defaults.Proxy != nil && old.Defaults.Proxy != nil {
			c.Defaults.Proxy.Extra = jsonx.Keep(c.Defaults.Proxy.Extra, old.Defaults.Proxy.Extra)
		}
	}
	if c.Session != nil && old.Session != nil {
		c.Session.Extra = jsonx.Keep(c.Session.Extra, old.Session.Extra)
		if c.Session.Serial != nil && old.Session.Serial != nil {
			c.Session.Serial.Extra = jsonx.Keep(c.Session.Serial.Extra, old.Session.Serial.Extra)
		}
		for i := range c.Session.Tunnels {
			for _, o := range old.Session.Tunnels {
				if o.ID == c.Session.Tunnels[i].ID {
					c.Session.Tunnels[i].Extra = jsonx.Keep(c.Session.Tunnels[i].Extra, o.Extra)
				}
			}
		}
		if c.Session.Proxy != nil && old.Session.Proxy != nil {
			c.Session.Proxy.Extra = jsonx.Keep(c.Session.Proxy.Extra, old.Session.Proxy.Extra)
		}
	}
}

// DropUnknown removes members written by a newer YACM from n. It is used
// where data crosses from someone else's file (a .yacm import, a copy of a
// team library session) into your own sessions: a newer version may treat a
// member it knows (say, a command to run locally) with care that this
// version cannot apply, so such members are not carried into trusted data.
func DropUnknown(n *Node) {
	if n == nil {
		return
	}
	n.Extra = nil
	if n.Defaults != nil {
		n.Defaults.Extra = nil
	}
	ss := n.Session
	if ss == nil {
		return
	}
	ss.Extra = nil
	if ss.Serial != nil {
		ss.Serial.Extra = nil
	}
	for i := range ss.Jump {
		ss.Jump[i].Extra = nil
	}
	for i := range ss.Expect {
		ss.Expect[i].Extra = nil
	}
	for i := range ss.Before {
		ss.Before[i].Extra = nil
	}
	for i := range ss.After {
		ss.After[i].Extra = nil
	}
	for i := range ss.Macros {
		ss.Macros[i].Extra = nil
	}
	for i := range ss.Tunnels {
		ss.Tunnels[i].Extra = nil
	}
	if ss.Proxy != nil {
		ss.Proxy.Extra = nil
	}
	if n.Defaults != nil && n.Defaults.Proxy != nil {
		n.Defaults.Proxy.Extra = nil
	}
}

// DisarmTunnels switches off the tunnels of a session from someone else's
// file (a .yacm import, a copy of a team library session): a port forward
// can expose this computer, so it starts only after you turn it on.
func DisarmTunnels(n *Node) {
	if n == nil || n.Session == nil {
		return
	}
	for i := range n.Session.Tunnels {
		n.Session.Tunnels[i].Off = true
	}
}

// HasUnknown reports whether n carries anything only a newer YACM knows: a
// connection type or members this version does not have.
func HasUnknown(n *Node) bool {
	if n == nil {
		return false
	}
	if len(n.Extra) > 0 || (n.Defaults != nil && len(n.Defaults.Extra) > 0) {
		return true
	}
	ss := n.Session
	if ss == nil {
		return false
	}
	if !KnownProtocol(ss.Protocol) || len(ss.Extra) > 0 || (ss.Serial != nil && len(ss.Serial.Extra) > 0) {
		return true
	}
	for _, j := range ss.Jump {
		if len(j.Extra) > 0 {
			return true
		}
	}
	for _, r := range ss.Expect {
		if len(r.Extra) > 0 {
			return true
		}
	}
	for _, c := range append(append([]LocalCommand{}, ss.Before...), ss.After...) {
		if len(c.Extra) > 0 {
			return true
		}
	}
	for _, m := range ss.Macros {
		if len(m.Extra) > 0 {
			return true
		}
	}
	for _, t := range ss.Tunnels {
		if len(t.Extra) > 0 {
			return true
		}
	}
	if ss.Proxy != nil && len(ss.Proxy.Extra) > 0 {
		return true
	}
	return false
}
