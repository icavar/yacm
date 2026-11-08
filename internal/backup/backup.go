// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package backup keeps copies of the data files (sessions.json, vault.json,
// known_hosts) in the backups folder inside the data folder, so a deleted
// group, an import gone wrong or a damaged file can be undone.
//
// Copies are made automatically before saving (at most every ten minutes)
// and before risky operations (import, restore, password change). The newest
// ten are kept, plus the first copy of each of the last fourteen days. The
// vault copy stays encrypted with the same master password.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Files are the data files that are copied.
var Files = []string{"sessions.json", "vault.json", "known_hosts"}

const (
	keepRecent = 10
	keepDays   = 14
	autoEvery  = 10 * time.Minute
	stampFmt   = "2006-01-02_15-04-05"
	infoFile   = "backup.json"
)

// Info describes one backup.
type Info struct {
	ID       string    `json:"id"`
	Created  time.Time `json:"created"`
	Reason   string    `json:"reason"` // "auto" or what was about to happen
	App      string    `json:"app"`
	Sessions int       `json:"sessions"`
	Groups   int       `json:"groups"`
	Size     int64     `json:"size"`
}

// Manager makes and restores backups of one data folder.
type Manager struct {
	dataDir string
	app     string
	now     func() time.Time

	mu       sync.Mutex
	lastAuto time.Time
}

// New returns a manager for the data folder dir.
func New(dataDir, app string) *Manager {
	return &Manager{dataDir: dataDir, app: app, now: time.Now}
}

// Dir is the backups folder.
func (m *Manager) Dir() string { return filepath.Join(m.dataDir, "backups") }

// Auto makes a backup before a save unless one was made in the last ten
// minutes. Errors are returned for logging; saving goes on regardless.
func (m *Manager) Auto() error {
	m.mu.Lock()
	if !m.lastAuto.IsZero() && m.now().Sub(m.lastAuto) < autoEvery {
		m.mu.Unlock()
		return nil
	}
	m.lastAuto = m.now()
	m.mu.Unlock()
	_, err := m.Snapshot("auto")
	return err
}

// Snapshot copies the data files now. reason is shown in the list
// ("before import", "before restore", ...). Nothing is made when there are
// no data files yet.
func (m *Manager) Snapshot(reason string) (*Info, error) {
	if _, err := os.Stat(filepath.Join(m.dataDir, Files[0])); err != nil {
		return nil, nil // nothing saved yet
	}
	now := m.now()
	id := now.Format(stampFmt)
	dir := filepath.Join(m.Dir(), id)
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			break
		}
		id = fmt.Sprintf("%s-%d", now.Format(stampFmt), i)
		dir = filepath.Join(m.Dir(), id)
	}
	tmp := dir + ".tmp"
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	info := Info{ID: id, Created: now, Reason: reason, App: m.app}
	for _, f := range Files {
		n, err := copyFile(filepath.Join(m.dataDir, f), filepath.Join(tmp, f))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		info.Size += n
	}
	info.Sessions, info.Groups = countNodes(filepath.Join(tmp, Files[0]))
	data, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, infoFile), data, 0o600); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	m.prune()
	return &info, nil
}

// List returns the backups, newest first.
func (m *Manager) List() []Info {
	entries, err := os.ReadDir(m.Dir())
	if err != nil {
		return nil
	}
	var out []Info
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.Dir(), e.Name(), infoFile))
		if err != nil {
			continue
		}
		var in Info
		if json.Unmarshal(data, &in) != nil || in.ID != e.Name() {
			continue
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// prune keeps the newest keepRecent backups and the first one of each of the
// last keepDays days.
func (m *Manager) prune() {
	all := m.List()
	keep := map[string]bool{}
	for i, in := range all {
		if i < keepRecent {
			keep[in.ID] = true
		}
	}
	firstOfDay := map[string]Info{}
	for _, in := range all {
		day := in.Created.Format("2006-01-02")
		if cur, ok := firstOfDay[day]; !ok || in.Created.Before(cur.Created) {
			firstOfDay[day] = in
		}
	}
	var days []string
	for d := range firstOfDay {
		days = append(days, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for i, d := range days {
		if i < keepDays {
			keep[firstOfDay[d].ID] = true
		}
	}
	for _, in := range all {
		if !keep[in.ID] {
			os.RemoveAll(filepath.Join(m.Dir(), in.ID))
		}
	}
}

// Restore copies the files of backup id back into the data folder, after a
// backup of the current state ("before restore"). The app must reopen its
// files afterwards (YACM restarts).
func (m *Manager) Restore(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return errors.New("invalid backup")
	}
	src := filepath.Join(m.Dir(), id)
	if _, err := os.Stat(filepath.Join(src, Files[0])); err != nil {
		return errors.New("this backup has no sessions file")
	}
	if _, err := m.Snapshot("before restore"); err != nil {
		return fmt.Errorf("could not save the current state first: %w", err)
	}
	for _, f := range Files {
		from := filepath.Join(src, f)
		if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
			continue
		}
		tmp := filepath.Join(m.dataDir, f+".restore")
		if _, err := copyFile(from, tmp); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, filepath.Join(m.dataDir, f)); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}

func copyFile(from, to string) (int64, error) {
	in, err := os.Open(from)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// countNodes reads only how many sessions and groups a sessions file has.
func countNodes(path string) (sessions, groups int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	var f struct {
		Nodes []struct {
			IsGroup bool `json:"isGroup"`
		} `json:"nodes"`
	}
	if json.Unmarshal(data, &f) != nil {
		return 0, 0
	}
	for _, n := range f.Nodes {
		if n.IsGroup {
			groups++
		} else {
			sessions++
		}
	}
	return sessions, groups
}
