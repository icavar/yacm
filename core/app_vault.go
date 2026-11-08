// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"errors"
	"time"

	"github.com/icavar/yacm/internal/vault"
)

// ---- vault -----------------------------------------------------------------

// VaultStatus describes the secret store.
type VaultStatus struct {
	Exists   bool   `json:"exists"`
	Unlocked bool   `json:"unlocked"`
	Secrets  int    `json:"secrets"`
	DataDir  string `json:"dataDir"`
}

// GetVaultStatus reports whether the vault exists / is unlocked.
func (a *App) GetVaultStatus() VaultStatus {
	return VaultStatus{Exists: a.vault.Exists(), Unlocked: a.vault.Unlocked(), Secrets: a.vault.Count(), DataDir: a.dataDir}
}

// CreateVault sets the master password on first run.
func (a *App) CreateVault(password string) error { return a.vault.Create(password) }

// UnlockVault opens the vault.
func (a *App) UnlockVault(password string) error {
	// Slow down online guessing a little (Argon2id already costs ~0.3 s).
	err := a.vault.Unlock(password)
	if errors.Is(err, vault.ErrWrongPassword) {
		time.Sleep(700 * time.Millisecond)
	}
	return err
}

// LockVault forgets the key. Running sessions keep running.
func (a *App) LockVault() { a.vault.Lock() }

// ChangeMasterPassword re-encrypts the vault.
func (a *App) ChangeMasterPassword(oldPw, newPw string) error {
	if a.vault.VerifyPassword(oldPw) {
		a.snapshot("before changing the master password")
	}
	return a.vault.ChangePassword(oldPw, newPw)
}
