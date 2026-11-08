// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icavar/yacm/internal/sshconn"
	"github.com/icavar/yacm/internal/store"
	"github.com/icavar/yacm/internal/vault"
)

// ---- tree ------------------------------------------------------------------

// State is everything the sidebar needs.
type State struct {
	Nodes       []*store.Node    `json:"nodes"`
	Settings    store.Settings   `json:"settings"`
	Clusters    map[string]int   `json:"clusters"`
	Credentials []CredentialInfo `json:"credentials"`
	// TeamSnippets are the commands of followed team libraries (read-only).
	TeamSnippets []store.Snippet `json:"teamSnippets"`
	// TeamLibraries describes the followed team libraries.
	TeamLibraries []TeamLibraryStatus `json:"teamLibraries"`
}

// CredentialInfo is a profile plus which secrets are stored (never values).
type CredentialInfo struct {
	store.Credential
	HasPassword   bool `json:"hasPassword"`
	HasPassphrase bool `json:"hasPassphrase"`
	Sessions      int  `json:"sessions"` // sessions that use it directly
	Groups        int  `json:"groups"`   // groups that give it to their sessions
}

// MarshalJSON writes the profile with the members above. Without it the
// MarshalJSON of the embedded store.Credential would be used and leave them
// out, so the UI showed every profile as unused and without a password.
func (c CredentialInfo) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(c.Credential)
	if err != nil {
		return nil, err
	}
	more, err := json.Marshal(struct {
		HasPassword   bool `json:"hasPassword"`
		HasPassphrase bool `json:"hasPassphrase"`
		Sessions      int  `json:"sessions"`
		Groups        int  `json:"groups"`
	}{c.HasPassword, c.HasPassphrase, c.Sessions, c.Groups})
	if err != nil {
		return nil, err
	}
	if len(base) < 2 || base[len(base)-1] != '}' {
		return nil, errors.New("credential profile is not a JSON object")
	}
	if string(base) == "{}" {
		return more, nil
	}
	return append(append(base[:len(base)-1], ','), more[1:]...), nil
}

// GetState returns the session tree and settings.
func (a *App) GetState() State {
	st := State{Nodes: a.store.ListAll(), Settings: a.store.Settings(), Clusters: a.store.Clusters(), Credentials: []CredentialInfo{}, TeamSnippets: a.teamSnippets(), TeamLibraries: a.teamStatuses()}
	for _, c := range a.store.Credentials() {
		ci := CredentialInfo{Credential: *c, HasPassword: a.vault.Has(store.SecretCredPassword(c.ID)), HasPassphrase: a.vault.Has(store.SecretCredPassphrase(c.ID))}
		ci.Sessions, ci.Groups = a.store.CredentialUsage(c.ID)
		st.Credentials = append(st.Credentials, ci)
	}
	return st
}

// CredentialSecrets carries secret edits for a profile (nil = unchanged,
// "" = delete).
type CredentialSecrets struct {
	Password   *string `json:"password"`
	Passphrase *string `json:"passphrase"`
}

// SaveCredential creates or updates a credential profile and its secrets.
func (a *App) SaveCredential(c store.Credential, secrets *CredentialSecrets) (*store.Credential, error) {
	if secrets != nil && (secrets.Password != nil || secrets.Passphrase != nil) && !a.vault.Unlocked() {
		return nil, errors.New("unlock the vault to store passwords")
	}
	saved, err := a.store.UpsertCredential(&c)
	if err != nil {
		return nil, err
	}
	kv := map[string]string{}
	if secrets != nil {
		if secrets.Password != nil {
			kv[store.SecretCredPassword(saved.ID)] = *secrets.Password
		}
		if secrets.Passphrase != nil {
			kv[store.SecretCredPassphrase(saved.ID)] = *secrets.Passphrase
		}
	}
	if saved.Auth != store.AuthPublicKey && a.vault.Has(store.SecretCredPassphrase(saved.ID)) {
		kv[store.SecretCredPassphrase(saved.ID)] = ""
	}
	if len(kv) > 0 {
		if err := a.vault.SetMany(kv); err != nil {
			return saved, fmt.Errorf("profile saved, but the password was not: %w", err)
		}
	}
	return saved, nil
}

// DeleteCredential removes a profile and its secrets.
func (a *App) DeleteCredential(id string) error {
	if err := a.store.DeleteCredential(id); err != nil {
		return err
	}
	if a.vault.Unlocked() {
		_ = a.vault.DeletePrefix(store.SecretCredPrefix(id))
	}
	return nil
}

// BulkRequest edits many sessions at once.
type BulkRequest struct {
	IDs      []string         `json:"ids"`
	Change   store.BulkChange `json:"change"`
	Password *string          `json:"password"` // sets (or with "" removes) each session's own password
}

// BulkUpdate applies one change to all selected sessions.
func (a *App) BulkUpdate(req BulkRequest) (int, error) {
	if req.Password != nil && !a.vault.Unlocked() {
		return 0, errors.New("unlock the vault to store passwords")
	}
	a.snapshot("before editing many sessions")
	changed, err := a.store.BulkUpdate(req.IDs, req.Change)
	if err != nil {
		return 0, err
	}
	if req.Password != nil {
		kv := map[string]string{}
		for _, id := range changed {
			kv[store.SecretPassword(id)] = *req.Password
		}
		if err := a.vault.SetMany(kv); err != nil {
			return len(changed), fmt.Errorf("sessions changed, but passwords were not saved: %w", err)
		}
	}
	return len(changed), nil
}

// DefaultHighlightRules returns the standard keyword colours.
func (a *App) DefaultHighlightRules() []store.HighlightRule { return store.DefaultHighlightRules() }

// SessionsUnder expands a selection of groups and sessions to session IDs.
func (a *App) SessionsUnder(ids []string) []string { return a.store.SessionsUnder(ids) }

// SecretInput carries secret edits from the editor. A nil pointer means
// "unchanged", an empty string means "delete".
type SecretInput struct {
	Password   *string            `json:"password"`
	Passphrase *string            `json:"passphrase"`
	Jump       map[string]*string `json:"jump"`   // hop index -> password
	Expect     map[string]*string `json:"expect"` // rule id -> hidden send
	// JumpOrigin maps each current hop index to its index before editing
	// (-1 = new hop), so stored jump passwords follow reordered hops.
	JumpOrigin []int `json:"jumpOrigin"`
}

// SecretFlags tells the editor which secrets are stored (never the values).
type SecretFlags struct {
	Password   bool            `json:"password"`
	Passphrase bool            `json:"passphrase"`
	Jump       map[string]bool `json:"jump"`
	Expect     map[string]bool `json:"expect"`
}

// GetSecretFlags returns which secrets exist for a node.
func (a *App) GetSecretFlags(id string) SecretFlags {
	f := SecretFlags{Jump: map[string]bool{}, Expect: map[string]bool{}}
	n, err := a.store.Get(id)
	if err != nil || n.Session == nil {
		return f
	}
	f.Password = a.vault.Has(store.SecretPassword(id))
	f.Passphrase = a.vault.Has(store.SecretPassphrase(id))
	for i := range n.Session.Jump {
		f.Jump[strconv.Itoa(i)] = a.vault.Has(store.SecretJumpPassword(id, i))
	}
	for _, r := range n.Session.Expect {
		f.Expect[r.ID] = a.vault.Has(store.SecretExpect(id, r.ID))
	}
	return f
}

// SaveNode creates or updates a group/session plus its secrets.
func (a *App) SaveNode(n store.Node, secrets *SecretInput) (*store.Node, error) {
	hasSecrets := secrets != nil && (secrets.Password != nil || secrets.Passphrase != nil || len(secrets.Jump) > 0 || len(secrets.Expect) > 0)
	if hasSecrets && !a.vault.Unlocked() {
		return nil, errors.New("unlock the vault to store passwords")
	}
	if n.Session != nil {
		for i := range n.Session.Expect {
			if n.Session.Expect[i].Hidden {
				n.Session.Expect[i].Send = "" // hidden values live in the vault only
			}
		}
	}
	saved, err := a.store.Upsert(&n)
	if err != nil {
		return nil, err
	}
	if !a.vault.Unlocked() || saved.Session == nil {
		return saved, nil
	}
	kv := map[string]string{}
	if secrets != nil {
		if secrets.Password != nil {
			kv[store.SecretPassword(saved.ID)] = *secrets.Password
		}
		if secrets.Passphrase != nil {
			kv[store.SecretPassphrase(saved.ID)] = *secrets.Passphrase
		}
		for k, v := range secrets.Jump {
			if i, err := strconv.Atoi(k); err == nil && v != nil {
				kv[store.SecretJumpPassword(saved.ID, i)] = *v
			}
		}
		if secrets.JumpOrigin != nil {
			old := map[int]string{}
			for i := 0; i < 16; i++ {
				if v := a.secret(store.SecretJumpPassword(saved.ID, i)); v != "" {
					old[i] = v
				}
			}
			for i, o := range secrets.JumpOrigin {
				if _, edited := secrets.Jump[strconv.Itoa(i)]; edited {
					continue
				}
				if o >= 0 {
					kv[store.SecretJumpPassword(saved.ID, i)] = old[o]
				} else {
					kv[store.SecretJumpPassword(saved.ID, i)] = ""
				}
			}
		}
		for rid, v := range secrets.Expect {
			if v != nil {
				kv[store.SecretExpect(saved.ID, rid)] = *v
			}
		}
	}
	// Drop secrets of expect rules that were removed or made visible.
	keep := map[string]bool{}
	for _, r := range saved.Session.Expect {
		if r.Hidden {
			keep[r.ID] = true
		}
	}
	prefix := store.SecretPrefix(saved.ID) + "expect/"
	for _, k := range append(a.vault.Keys(prefix), keysOf(kv)...) {
		if strings.HasPrefix(k, prefix) && !keep[strings.TrimPrefix(k, prefix)] {
			kv[k] = "" // rule removed or no longer hidden
		}
	}
	// Drop jump passwords beyond the current number of hops.
	for i := len(saved.Session.Jump); i < len(saved.Session.Jump)+8; i++ {
		if a.vault.Has(store.SecretJumpPassword(saved.ID, i)) {
			kv[store.SecretJumpPassword(saved.ID, i)] = ""
		}
	}
	if len(kv) > 0 {
		if err := a.vault.SetMany(kv); err != nil {
			return saved, fmt.Errorf("session saved, but secrets were not: %w", err)
		}
	}
	return saved, nil
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// DeleteNode removes a node (recursively) and its secrets.
func (a *App) DeleteNode(id string) error {
	if n, err := a.store.Get(id); err == nil && n.IsGroup {
		a.snapshot("before deleting a group")
	} else {
		a.snapshot("before deleting sessions")
	}
	removed, err := a.store.Delete(id)
	if err != nil {
		return err
	}
	if a.vault.Unlocked() {
		for _, r := range removed {
			_ = a.vault.DeletePrefix(store.SecretPrefix(r))
		}
	}
	return nil
}

// MoveNode re-parents / reorders a node.
func (a *App) MoveNode(id, parentID string, index int) error {
	return a.store.Move(id, parentID, index)
}

// DuplicateNode copies a session and its secrets.
func (a *App) DuplicateNode(id string) (*store.Node, error) {
	c, ruleMap, err := a.store.Duplicate(id)
	if err != nil {
		return nil, err
	}
	if a.vault.Unlocked() {
		err = a.vault.CopyPrefix(store.SecretPrefix(id), store.SecretPrefix(c.ID), func(rest string) string {
			if strings.HasPrefix(rest, "expect/") {
				if nid, ok := ruleMap[strings.TrimPrefix(rest, "expect/")]; ok {
					return "expect/" + nid
				}
			}
			return rest
		})
	}
	return c, err
}

// SetExpanded remembers group state.
func (a *App) SetExpanded(id string, open bool) error { return a.store.SetExpanded(id, open) }

// SaveSettings stores preferences.
func (a *App) SaveSettings(s store.Settings) error {
	if s.LastSeenVersion == "" {
		s.LastSeenVersion = a.store.Settings().LastSeenVersion // kept by the backend, not edited in the UI
	}
	if err := a.store.SetSettings(s); err != nil {
		return err
	}
	a.kh = sshconn.NewKnownHosts(filepath.Join(a.dataDir, "known_hosts"), s.UseSystemKnownHost)
	return nil
}

// RevealSecret shows a stored password after re-entering the master password.
func (a *App) RevealSecret(nodeID, kind, ref, master string) (string, error) {
	if !a.vault.VerifyPassword(master) {
		time.Sleep(700 * time.Millisecond)
		return "", vault.ErrWrongPassword
	}
	var key string
	switch kind {
	case "password":
		key = store.SecretPassword(nodeID)
	case "passphrase":
		key = store.SecretPassphrase(nodeID)
	case "jump":
		i, _ := strconv.Atoi(ref)
		key = store.SecretJumpPassword(nodeID, i)
	case "expect":
		key = store.SecretExpect(nodeID, ref)
	case "cred-password":
		key = store.SecretCredPassword(ref)
	case "cred-passphrase":
		key = store.SecretCredPassphrase(ref)
	default:
		return "", errors.New("unknown secret")
	}
	return a.vault.Get(key)
}

// SessionAddresses gives the address of each session, for pasting into a
// ticket or a chat: user@host (with :port when it is not the default) for
// SSH and telnet, the port and speed for serial. Groups are skipped.
func (a *App) SessionAddresses(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		n, err := a.store.Get(id)
		if err != nil || n.Session == nil {
			continue
		}
		user := ""
		if eff, err := a.store.Effective(n); err == nil {
			user = eff.User
		}
		out = append(out, sessionAddress(n.Session, user))
	}
	return out
}

func sessionAddress(s *store.Session, user string) string {
	if s.Protocol == store.ProtoSerial {
		if s.Serial == nil {
			return s.Host
		}
		if s.Serial.Baud > 0 {
			return fmt.Sprintf("%s %d", s.Serial.Device, s.Serial.Baud)
		}
		return s.Serial.Device
	}
	host := s.Host
	def := map[store.Protocol]int{store.ProtoSSH: 22, store.ProtoTelnet: 23}[s.Protocol]
	if s.Port != 0 && s.Port != def {
		host = net.JoinHostPort(host, strconv.Itoa(s.Port))
	}
	if user != "" && s.Protocol == store.ProtoSSH {
		return user + "@" + host
	}
	return host
}
