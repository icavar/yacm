// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package vault stores secrets (passwords, key passphrases, hidden expect
// strings) in a single file encrypted with AES-256-GCM. The key is derived
// from a master password with Argon2id (RFC 9106 parameters) or, for builds
// that must use FIPS 140-3 approved algorithms only, PBKDF2-HMAC-SHA256.
// Every build reads both; NewVaultKDF chooses the one for new vaults.
//
// File layout (JSON):
//
//	{"v":1,"kdf":{"alg":"argon2id","t":3,"m":65536,"p":4,"salt":"…"},
//	 "nonce":"…","ct":"…"}
//	{"v":1,"kdf":{"alg":"pbkdf2-sha256","i":600000,"salt":"…"}, …}
//
// The header (everything except "ct") is authenticated as GCM additional
// data, so parameters cannot be downgraded without detection.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/icavar/yacm/internal/store"
)

const (
	formatVersion = 1
	keyLen        = 32
	saltLen       = 16
	minPassword   = 12 // new vaults and password changes; existing ones still open
)

// KDFParams are the key derivation parameters: Time, MemKiB and Threads
// for Argon2id, Iter for PBKDF2.
type KDFParams struct {
	Alg     string `json:"alg"`
	Time    uint32 `json:"t"`
	MemKiB  uint32 `json:"m"`
	Threads uint8  `json:"p"`
	Iter    uint32 `json:"i,omitempty"`
	Salt    []byte `json:"salt"`
}

// KDF names.
const (
	KDFArgon2id = "argon2id"
	KDFPBKDF2   = "pbkdf2-sha256"
)

// pbkdf2Min is the OWASP (2023) minimum for PBKDF2-HMAC-SHA256.
const pbkdf2Min = 600_000

// DefaultKDF follows RFC 9106 §4 "second recommended option".
func DefaultKDF() KDFParams {
	return KDFParams{Alg: KDFArgon2id, Time: 3, MemKiB: 64 * 1024, Threads: 4}
}

// PBKDF2KDF uses only FIPS 140-3 approved algorithms (PBKDF2-HMAC-SHA256).
func PBKDF2KDF() KDFParams {
	return KDFParams{Alg: KDFPBKDF2, Iter: pbkdf2Min}
}

// NewVaultKDF gives the parameters for new vaults and encrypted exports.
// A build may replace it (before the first vault is created) with
// PBKDF2KDF; existing vaults keep the algorithm they were made with.
var NewVaultKDF = DefaultKDF

// freshKDF returns new parameters with a new salt, of the same algorithm as
// p (a changed password keeps the vault's algorithm), or NewVaultKDF's.
func freshKDF(alg string) (KDFParams, error) {
	var p KDFParams
	switch alg {
	case KDFArgon2id:
		p = DefaultKDF()
	case KDFPBKDF2:
		p = PBKDF2KDF()
	default:
		p = NewVaultKDF()
	}
	p.Salt = make([]byte, saltLen)
	_, err := rand.Read(p.Salt)
	return p, err
}

type header struct {
	V     int       `json:"v"`
	KDF   KDFParams `json:"kdf"`
	Nonce []byte    `json:"nonce"`
}

type fileFormat struct {
	header
	CT []byte `json:"ct"`
}

// Errors.
var (
	ErrLocked        = errors.New("vault is locked")
	ErrWrongPassword = errors.New("wrong master password or damaged vault")
	ErrExists        = errors.New("vault already exists")
	ErrReadOnly      = errors.New("this window is read-only: the data folder is in use elsewhere, so passwords cannot be saved")
	ErrWeakPassword  = fmt.Errorf("master password must be at least %d characters", minPassword)
)

// Vault is an in-memory decrypted secret map plus the key to re-seal it.
type Vault struct {
	mu   sync.Mutex
	path string
	kdf  KDFParams
	key  []byte
	data map[string]string

	readOnly bool
}

// SetReadOnly makes every change fail with ErrReadOnly (unlocking still
// works). Call it before the vault is used.
func (v *Vault) SetReadOnly(ro bool) {
	v.mu.Lock()
	v.readOnly = ro
	v.mu.Unlock()
}

// New returns a locked vault bound to path.
func New(path string) *Vault { return &Vault{path: path} }

// Exists reports whether the vault file is present.
func (v *Vault) Exists() bool {
	_, err := os.Stat(v.path)
	return err == nil
}

// Unlocked reports whether secrets are available.
func (v *Vault) Unlocked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.key != nil
}

func derive(password string, p KDFParams) ([]byte, error) {
	// Refuse absurd parameters from a tampered file (DoS) and too weak ones.
	switch p.Alg {
	case KDFArgon2id:
		if p.Time < 1 || p.Time > 64 || p.MemKiB < 19*1024 || p.MemKiB > 4*1024*1024 || p.Threads < 1 || len(p.Salt) < saltLen {
			return nil, errors.New("vault kdf parameters out of range")
		}
		return argon2.IDKey([]byte(password), p.Salt, p.Time, p.MemKiB, p.Threads, keyLen), nil
	case KDFPBKDF2:
		if p.Iter < pbkdf2Min || p.Iter > 20_000_000 || len(p.Salt) < saltLen {
			return nil, errors.New("vault kdf parameters out of range")
		}
		return pbkdf2.Key(sha256.New, password, p.Salt, int(p.Iter), keyLen)
	}
	return nil, fmt.Errorf("unsupported kdf %q", p.Alg)
}

// Create initialises a new empty vault with the given master password.
func (v *Vault) Create(password string) error {
	if len(password) < minPassword {
		return ErrWeakPassword
	}
	if v.Exists() {
		return ErrExists
	}
	p, err := freshKDF("")
	if err != nil {
		return err
	}
	key, err := derive(password, p)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.readOnly {
		return ErrReadOnly
	}
	v.kdf, v.key, v.data = p, key, map[string]string{}
	return v.sealLocked()
}

// Unlock decrypts the vault file.
func (v *Vault) Unlock(password string) error {
	raw, err := os.ReadFile(v.path)
	if err != nil {
		return err
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return ErrWrongPassword
	}
	if f.V != formatVersion {
		return fmt.Errorf("unsupported vault version %d", f.V)
	}
	key, err := derive(password, f.KDF)
	if err != nil {
		return err
	}
	aad, _ := json.Marshal(f.header)
	plain, err := open(key, f.Nonce, f.CT, aad)
	if err != nil {
		wipe(key)
		return ErrWrongPassword
	}
	data := map[string]string{}
	if err := json.Unmarshal(plain, &data); err != nil {
		wipe(key)
		return ErrWrongPassword
	}
	wipe(plain)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.kdf, v.key, v.data = f.KDF, key, data
	return nil
}

// Lock forgets the key and all decrypted secrets.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	wipe(v.key)
	v.key = nil
	v.data = nil
}

// VerifyPassword checks a password against the unlocked vault without
// changing state (used to confirm sensitive actions).
func (v *Vault) VerifyPassword(password string) bool {
	v.mu.Lock()
	p, key := v.kdf, v.key
	v.mu.Unlock()
	if key == nil {
		return false
	}
	k, err := derive(password, p)
	if err != nil {
		return false
	}
	defer wipe(k)
	return subtle.ConstantTimeCompare(k, key) == 1
}

// ChangePassword re-encrypts the vault under a new password and new salt.
func (v *Vault) ChangePassword(oldPw, newPw string) error {
	if len(newPw) < minPassword {
		return ErrWeakPassword
	}
	if !v.VerifyPassword(oldPw) {
		return ErrWrongPassword
	}
	v.mu.Lock()
	alg := v.kdf.Alg
	v.mu.Unlock()
	p, err := freshKDF(alg)
	if err != nil {
		return err
	}
	key, err := derive(newPw, p)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.readOnly {
		return ErrReadOnly
	}
	oldKDF, old := v.kdf, v.key
	v.kdf, v.key = p, key
	if err := v.sealLocked(); err != nil {
		// Nothing was written: keep the old password in force.
		v.kdf, v.key = oldKDF, old
		wipe(key)
		return err
	}
	wipe(old)
	return nil
}

// Get returns a secret ("" if absent).
func (v *Vault) Get(name string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return "", ErrLocked
	}
	return v.data[name], nil
}

// Has reports whether a non-empty secret is stored.
func (v *Vault) Has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.key != nil && v.data[name] != ""
}

// SetMany stores several secrets in one write; empty values delete.
func (v *Vault) SetMany(kv map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.readOnly {
		return ErrReadOnly
	}
	if v.key == nil {
		return ErrLocked
	}
	for k, val := range kv {
		if val == "" {
			delete(v.data, k)
		} else {
			v.data[k] = val
		}
	}
	return v.sealLocked()
}

// Set stores one secret; an empty value deletes it.
func (v *Vault) Set(name, value string) error {
	return v.SetMany(map[string]string{name: value})
}

// DeletePrefix removes all secrets starting with prefix.
func (v *Vault) DeletePrefix(prefix string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.readOnly {
		return ErrReadOnly
	}
	if v.key == nil {
		return ErrLocked
	}
	changed := false
	for k := range v.data {
		if strings.HasPrefix(k, prefix) {
			delete(v.data, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return v.sealLocked()
}

// CopyPrefix duplicates secrets from one prefix to another, renaming parts
// via the rename callback (used when duplicating sessions).
func (v *Vault) CopyPrefix(from, to string, rename func(rest string) string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.readOnly {
		return ErrReadOnly
	}
	if v.key == nil {
		return ErrLocked
	}
	var keys []string
	for k := range v.data {
		if strings.HasPrefix(k, from) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	for _, k := range keys {
		rest := strings.TrimPrefix(k, from)
		if rename != nil {
			rest = rename(rest)
		}
		v.data[to+rest] = v.data[k]
	}
	return v.sealLocked()
}

// Keys returns the names (never the values) of secrets under prefix.
func (v *Vault) Keys(prefix string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for k := range v.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Count returns the number of stored secrets.
func (v *Vault) Count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.data)
}

func (v *Vault) sealLocked() error {
	plain, err := json.Marshal(v.data)
	if err != nil {
		return err
	}
	defer wipe(plain)
	h := header{V: formatVersion, KDF: v.kdf, Nonce: make([]byte, 12)}
	if _, err := rand.Read(h.Nonce); err != nil {
		return err
	}
	aad, _ := json.Marshal(h)
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	ct := gcm.Seal(nil, h.Nonce, plain, aad)
	out, err := json.MarshalIndent(fileFormat{header: h, CT: ct}, "", " ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(v.path, out, 0o600)
}

func open(key, nonce, ct, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("bad nonce")
	}
	return gcm.Open(nil, nonce, ct, aad)
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Envelope is a password-protected secret map in the same format as the
// vault file. It is used for exports, protected by a separate password.
type Envelope = fileFormat

// SealMap encrypts data with a fresh salt and nonce under password.
func SealMap(password string, data map[string]string) (*Envelope, error) {
	if len(password) < minPassword {
		return nil, ErrWeakPassword
	}
	p, err := freshKDF("")
	if err != nil {
		return nil, err
	}
	key, err := derive(password, p)
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	plain, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	defer wipe(plain)
	h := header{V: formatVersion, KDF: p, Nonce: make([]byte, 12)}
	if _, err := rand.Read(h.Nonce); err != nil {
		return nil, err
	}
	aad, _ := json.Marshal(h)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Envelope{header: h, CT: gcm.Seal(nil, h.Nonce, plain, aad)}, nil
}

// OpenMap decrypts an envelope made by SealMap.
func OpenMap(password string, e *Envelope) (map[string]string, error) {
	if e == nil {
		return nil, errors.New("no encrypted data")
	}
	if e.V != formatVersion {
		return nil, fmt.Errorf("unsupported format version %d", e.V)
	}
	// An envelope comes from someone else's file: tighter limits than for
	// your own vault, so a file cannot make YACM take gigabytes of memory
	// or minutes of work (YACM writes 64 MiB, 3 passes).
	if k := e.KDF; (k.Alg == KDFArgon2id && (k.MemKiB > 512*1024 || k.Time > 16 || k.Threads > 16)) ||
		(k.Alg == KDFPBKDF2 && k.Iter > 5_000_000) {
		return nil, errors.New("the encrypted data asks for too much work to open")
	}
	key, err := derive(password, e.KDF)
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	aad, _ := json.Marshal(e.header)
	plain, err := open(key, e.Nonce, e.CT, aad)
	if err != nil {
		return nil, ErrWrongPassword
	}
	defer wipe(plain)
	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, ErrWrongPassword
	}
	return out, nil
}

// Snapshot returns the secrets whose names start with any of the prefixes.
func (v *Vault) Snapshot(prefixes []string) (map[string]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return nil, ErrLocked
	}
	out := map[string]string{}
	for k, val := range v.data {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				out[k] = val
				break
			}
		}
	}
	return out, nil
}
