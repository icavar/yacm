// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"errors"
	"os"
	"time"

	"github.com/yacm-app/yacm/internal/applog"
	"github.com/yacm-app/yacm/internal/backup"
)

// snapshot makes a backup before a risky operation. A failure is logged and
// the operation goes on (a missing backup must not block the user).
func (a *App) snapshot(reason string) {
	if a.backups == nil || a.store.ReadOnly() {
		return
	}
	// Several deletes or imports in a row need one backup, not one each.
	a.snapMu.Lock()
	if a.snapLast == nil {
		a.snapLast = map[string]time.Time{}
	}
	if t, ok := a.snapLast[reason]; ok && time.Since(t) < time.Minute {
		a.snapMu.Unlock()
		return
	}
	a.snapLast[reason] = time.Now()
	a.snapMu.Unlock()
	if _, err := a.backups.Snapshot(reason); err != nil {
		applog.Warnf("backup %s: %v", reason, err)
	}
}

// ListBackups returns the backups of the data folder, newest first.
func (a *App) ListBackups() []backup.Info {
	if a.backups == nil {
		return nil
	}
	return a.backups.List()
}

// BackupNow makes a backup on request.
func (a *App) BackupNow() (*backup.Info, error) {
	if a.backups == nil || a.store.ReadOnly() {
		return nil, errors.New("the data folder is read-only")
	}
	in, err := a.backups.Snapshot("made by hand")
	if err == nil && in == nil {
		err = errors.New("there is nothing to back up yet")
	}
	return in, err
}

// RestoreBackup puts a backup back and restarts YACM, so every part of the
// app reads the restored files. The current state is backed up first.
func (a *App) RestoreBackup(id string) (err error) {
	defer logOp("restore backup "+id, &err)
	if a.backups == nil || a.store.ReadOnly() {
		return errors.New("the data folder is read-only")
	}
	if n := a.openTabCount(); n > 0 {
		return errors.New("close the open sessions first")
	}
	a.vault.Lock()
	if err := a.backups.Restore(id); err != nil {
		return err
	}
	if a.restart != nil {
		return a.restart()
	}
	return nil
}

// OpenBackupFolder shows the backups folder.
func (a *App) OpenBackupFolder() error {
	if a.backups == nil {
		return errors.New("no backups folder")
	}
	if err := os.MkdirAll(a.backups.Dir(), 0o700); err != nil {
		return err
	}
	return openInFileManager(a.backups.Dir())
}
