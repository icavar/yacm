// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotListRestore(t *testing.T) {
	d := t.TempDir()
	m := New(d, "YACM test")
	clock := time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local)
	m.now = func() time.Time { return clock }

	// Nothing to back up before the first save.
	if in, err := m.Snapshot("auto"); in != nil || err != nil {
		t.Fatalf("empty folder: %v %v", in, err)
	}
	write(t, d, "sessions.json", `{"nodes":[{"isGroup":true},{"name":"r1"},{"name":"r2"}]}`)
	write(t, d, "vault.json", `{"v":1}`)
	in, err := m.Snapshot("before import")
	if err != nil || in == nil || in.Sessions != 2 || in.Groups != 1 || in.Reason != "before import" {
		t.Fatalf("snapshot: %+v %v", in, err)
	}

	// Auto backups are at most every ten minutes.
	clock = clock.Add(time.Minute)
	if err := m.Auto(); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(5 * time.Minute)
	_ = m.Auto()
	if got := len(m.List()); got != 2 {
		t.Fatalf("auto throttling: %d backups", got)
	}

	// Restore brings the old file back and keeps the current one as a backup.
	write(t, d, "sessions.json", `{"nodes":[]}`)
	clock = clock.Add(time.Minute)
	if err := m.Restore(in.ID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(d, "sessions.json"))
	if string(data) != `{"nodes":[{"isGroup":true},{"name":"r1"},{"name":"r2"}]}` {
		t.Fatalf("restored: %s", data)
	}
	list := m.List()
	if len(list) != 3 || list[0].Reason != "before restore" || list[0].Sessions != 0 {
		t.Fatalf("after restore: %+v", list)
	}
	if err := m.Restore("../x"); err == nil {
		t.Fatal("path in backup id accepted")
	}
}

func TestPrune(t *testing.T) {
	d := t.TempDir()
	write(t, d, "sessions.json", `{"nodes":[]}`)
	m := New(d, "YACM test")
	clock := time.Date(2026, 9, 1, 8, 0, 0, 0, time.Local)
	m.now = func() time.Time { return clock }
	// Five backups a day for 30 days.
	for day := 0; day < 30; day++ {
		for i := 0; i < 5; i++ {
			if _, err := m.Snapshot("auto"); err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Hour)
		}
		clock = clock.Add(19 * time.Hour)
	}
	list := m.List()
	// 10 newest (two days' worth) + first of each of 14 days (two of those
	// are among the 10 newest already) = 22.
	if len(list) != 22 {
		t.Fatalf("kept %d backups", len(list))
	}
	if list[len(list)-1].Created.Format("2006-01-02") != "2026-09-17" {
		t.Fatalf("oldest kept: %v", list[len(list)-1].Created)
	}
}
