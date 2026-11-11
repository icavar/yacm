// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package sshconn

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/yacm-app/yacm/internal/store"
)

// KnownHosts verifies server host keys against YACM's own known_hosts file
// (writable) and, optionally, the user's ~/.ssh/known_hosts (read-only).
// Both files use the OpenSSH format, so @cert-authority (host certificates)
// and @revoked markers work as in OpenSSH.
type KnownHosts struct {
	mu        sync.Mutex
	ownPath   string
	extraPath []string
	// ReadOnly: accepted keys are trusted for that connection only and the
	// file is not changed (the data folder is in use elsewhere).
	ReadOnly bool
	// Warn, if set, is told about lines the host key check cannot read.
	Warn func(msg string)

	warned map[string]bool // file + its size and time: already told
}

// ErrReadOnly is returned when the known_hosts file may not be changed.
var ErrReadOnly = errors.New("trusted host keys cannot be changed in a read-only window")

// NewKnownHosts creates the checker. ownPath is created on first write.
func NewKnownHosts(ownPath string, useSystem bool) *KnownHosts {
	kh := &KnownHosts{ownPath: ownPath}
	if useSystem {
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, ".ssh", "known_hosts")
			if _, err := os.Stat(p); err == nil {
				kh.extraPath = append(kh.extraPath, p)
			}
		}
	}
	return kh
}

// HostKeyInfo describes a key that needs the user's decision.
type HostKeyInfo struct {
	Host         string   `json:"host"` // normalized host[:port]
	KeyType      string   `json:"keyType"`
	Fingerprint  string   `json:"fingerprint"` // SHA256:...
	Changed      bool     `json:"changed"`     // a different key is on record
	KnownKeys    []string `json:"knownKeys"`   // fingerprints on record
	KnownSources []string `json:"knownSources"`
}

// ErrHostKeyRejected is returned when the user refuses a host key.
var ErrHostKeyRejected = errors.New("host key not accepted")

// ErrHostKeyRevoked is returned for keys marked @revoked.
var ErrHostKeyRevoked = errors.New("host key is revoked in known_hosts")

func (k *KnownHosts) files() []string {
	var out []string
	if _, err := os.Stat(k.ownPath); err == nil {
		out = append(out, k.ownPath)
	}
	return append(out, k.extraPath...)
}

// check returns nil when the key is trusted, otherwise a *knownhosts.KeyError
// or *knownhosts.RevokedError.
func (k *KnownHosts) check(hostname string, remote net.Addr, key ssh.PublicKey) error {
	var keyErrs []*knownhosts.KeyError
	for _, f := range k.files() {
		cb, err := knownhosts.New(f)
		if err != nil {
			// One line the parser cannot read makes it refuse the whole
			// file, which would quietly drop every key in it: use the
			// lines it can read instead.
			if cb, err = k.readableLines(f); err != nil {
				continue // unreadable file: ignore rather than block
			}
		}
		err = cb(hostname, remote, key)
		if err == nil {
			return nil
		}
		var rev *knownhosts.RevokedError
		if errors.As(err, &rev) {
			return err
		}
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			keyErrs = append(keyErrs, ke)
			continue
		}
		if _, isCert := key.(*ssh.Certificate); isCert {
			// No @cert-authority matches this host: fall through to the
			// plain-key check below, like OpenSSH does.
			continue
		}
		return err
	}
	if cert, ok := key.(*ssh.Certificate); ok {
		return k.check(hostname, remote, cert.Key)
	}
	merged := &knownhosts.KeyError{}
	for _, ke := range keyErrs {
		merged.Want = append(merged.Want, ke.Want...)
	}
	return merged
}

// readableLines builds the check for a file from the lines the known_hosts
// parser can read; the others are left out (and reported once).
func (k *KnownHosts) readableLines(path string) (ssh.HostKeyCallback, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	var bad []string
	for i, l := range lines {
		if !LineReadable(l) {
			bad = append(bad, fmt.Sprint(i+1))
			lines[i] = "" // keep the line numbers
		}
	}
	tmp, err := os.CreateTemp("", "yacm-known-hosts-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(strings.Join(lines, "\n"))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	cb, err := knownhosts.New(tmp.Name())
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 && k.Warn != nil {
		key := path
		if st, err := os.Stat(path); err == nil {
			key = fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
		}
		if k.warned == nil {
			k.warned = map[string]bool{}
		}
		if !k.warned[key] {
			k.warned[key] = true
			k.Warn(fmt.Sprintf("%s: line %s cannot be read and is ignored; the other host keys in it are still checked", path, strings.Join(bad, ", ")))
		}
	}
	tmpName := tmp.Name()
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			for i := range ke.Want {
				if ke.Want[i].Filename == tmpName {
					ke.Want[i].Filename = path
				}
			}
		}
		return err
	}, nil
}

// LineReadable reports whether the known_hosts parser used for the host key
// check can read line (an empty line or a comment can be read). The parser
// refuses a whole file for a single host pattern it cannot read, such as
// "[10.0.0.1]" without a port.
func LineReadable(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || t[0] == '#' {
		return true
	}
	_, hosts, _, _, _, err := ssh.ParseKnownHosts([]byte(t + "\n"))
	if err != nil || len(hosts) == 0 {
		return false
	}
	for _, h := range hosts {
		if strings.HasPrefix(h, "|") {
			// |1|salt|hash, both base64
			p := strings.Split(h, "|")
			if len(p) != 4 || p[1] != "1" {
				return false
			}
			for _, b := range p[2:] {
				if _, err := base64.StdEncoding.DecodeString(b); err != nil {
					return false
				}
			}
			continue
		}
		h = strings.TrimPrefix(h, "!")
		if h == "" {
			return false
		}
		if h[0] == '[' {
			if _, _, err := net.SplitHostPort(h); err != nil {
				return false
			}
		}
	}
	return true
}

// Callback returns an ssh.HostKeyCallback that consults the files and asks
// via decide() for unknown or changed keys.
func (k *KnownHosts) Callback(decide func(HostKeyInfo) (bool, error)) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		k.mu.Lock()
		err := k.check(hostname, remote, key)
		k.mu.Unlock()
		if err == nil {
			return nil
		}
		var rev *knownhosts.RevokedError
		if errors.As(err, &rev) {
			return ErrHostKeyRevoked
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		if cert, ok := key.(*ssh.Certificate); ok {
			key = cert.Key // decide on (and remember) the underlying host key
		}
		info := HostKeyInfo{
			Host:        knownhosts.Normalize(hostname),
			KeyType:     key.Type(),
			Fingerprint: ssh.FingerprintSHA256(key),
			Changed:     len(ke.Want) > 0,
		}
		for _, w := range ke.Want {
			info.KnownKeys = append(info.KnownKeys, w.Key.Type()+" "+ssh.FingerprintSHA256(w.Key))
			info.KnownSources = append(info.KnownSources, fmt.Sprintf("%s:%d", w.Filename, w.Line))
		}
		ok, derr := decide(info)
		if derr != nil {
			return derr
		}
		if !ok {
			return ErrHostKeyRejected
		}
		return k.Trust(hostname, key, info.Changed)
	}
}

// Trust records key for host in the own file; replace drops earlier entries
// for that host first.
func (k *KnownHosts) Trust(hostname string, key ssh.PublicKey, replace bool) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ReadOnly {
		return nil
	}
	host := knownhosts.Normalize(hostname)
	if replace {
		if err := k.removeLocked(host); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(k.ownPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(k.ownPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(knownhosts.Line([]string{host}, key) + "\n")
	return err
}

// Remove deletes all plain entries for host from the own file.
func (k *KnownHosts) Remove(host string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ReadOnly {
		return ErrReadOnly
	}
	return k.removeLocked(knownhosts.Normalize(host))
}

func (k *KnownHosts) removeLocked(host string) error {
	data, err := os.ReadFile(k.ownPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if lineMatchesHost(line, host) {
			continue
		}
		out.WriteString(line + "\n")
	}
	return store.WriteFileAtomic(k.ownPath, out.Bytes(), 0o600)
}

func lineMatchesHost(line, host string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "@") {
		return false
	}
	fields := strings.Fields(t)
	if len(fields) < 2 {
		return false
	}
	for _, h := range strings.Split(fields[0], ",") {
		if h == host {
			return true
		}
	}
	return false
}

// Entry is one line of the own known_hosts file for display.
type Entry struct {
	Hosts       string `json:"hosts"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	Marker      string `json:"marker,omitempty"`
}

// List returns the entries of the own file.
func (k *KnownHosts) List() ([]Entry, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	data, err := os.ReadFile(k.ownPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	rest := data
	for len(rest) > 0 {
		marker, hosts, key, _, r, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			break
		}
		rest = r
		out = append(out, Entry{Hosts: strings.Join(hosts, ","), KeyType: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), Marker: marker})
	}
	return out, nil
}

// KnownKeyTypes returns key types on record for host, used to order host key
// algorithms so an existing ECDSA/RSA entry is not reported as "changed"
// just because the server also has an Ed25519 key.
func (k *KnownHosts) KnownKeyTypes(hostname string) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	host := knownhosts.Normalize(hostname)
	seen := map[string]bool{}
	var out []string
	for _, f := range k.files() {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rest := data
		for len(rest) > 0 {
			marker, hosts, key, _, r, err := ssh.ParseKnownHosts(rest)
			if err != nil {
				break
			}
			rest = r
			if marker != "" {
				continue
			}
			for _, h := range hosts {
				if h == host || matchHashed(h, host) {
					if !seen[key.Type()] {
						seen[key.Type()] = true
						out = append(out, key.Type())
					}
				}
			}
		}
	}
	return out
}

func matchHashed(pattern, host string) bool {
	if !strings.HasPrefix(pattern, "|1|") {
		return false
	}
	// knownhosts.HashHostname uses a random salt, so compare via the salt
	// embedded in the pattern.
	parts := strings.Split(pattern, "|")
	if len(parts) != 4 {
		return false
	}
	return hashWithSalt(parts[2], host) == pattern
}

// ExportLines returns the entries of the own known_hosts file (public data).
func (k *KnownHosts) ExportLines() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	data, err := os.ReadFile(k.ownPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ExportLinesFor returns own known_hosts entries for the given host:port
// addresses, plus all @cert-authority / @revoked lines.
func (k *KnownHosts) ExportLinesFor(addrs []string) []string {
	want := map[string]bool{}
	for _, a := range addrs {
		want[knownhosts.Normalize(a)] = true
	}
	var out []string
	for _, l := range k.ExportLines() {
		if strings.HasPrefix(l, "@") {
			out = append(out, l)
			continue
		}
		for _, h := range lineHosts(l) {
			if want[h] || hashedMatchAny(h, want) {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

func lineHosts(line string) []string {
	f := strings.Fields(line)
	if len(f) < 2 || strings.HasPrefix(f[0], "@") {
		return nil
	}
	return strings.Split(f[0], ",")
}

// normEntry writes a known_hosts host entry the way knownhosts does for an
// address, so "[10.0.0.1]:22" and "10.0.0.1" compare equal.
func normEntry(h string) string {
	host, port := h, "22"
	if strings.HasPrefix(h, "[") {
		if end := strings.Index(h, "]"); end > 0 {
			host = h[1:end]
			if p, ok := strings.CutPrefix(h[end+1:], ":"); ok {
				port = p
			}
		}
	}
	return knownhosts.Normalize(net.JoinHostPort(strings.ToLower(host), port))
}

func hashedMatchAny(pattern string, hosts map[string]bool) bool {
	if !strings.HasPrefix(pattern, "|1|") {
		return false
	}
	for h := range hosts {
		if matchHashed(pattern, h) {
			return true
		}
	}
	return false
}

// ImportLines appends valid known_hosts lines that are not present yet and
// returns how many were added. A plain entry is skipped when this computer
// already trusts a key for that host, so an import never overrides (or adds
// a second, older key next to) a key the user accepted here.
func (k *KnownHosts) ImportLines(lines []string) (int, error) {
	existing := map[string]bool{}
	knownHost := map[string]bool{}
	for _, l := range k.ExportLines() {
		existing[l] = true
		for _, h := range lineHosts(l) {
			knownHost[normEntry(h)] = true
		}
	}
	// A host is also trusted through the system known_hosts and hashed
	// names: ask KnownKeyTypes (which reads every file) before locking.
	trusted := map[string]bool{}
	for _, l := range lines {
		f := strings.Fields(strings.TrimSpace(l))
		if len(f) < 2 || strings.HasPrefix(f[0], "@") {
			continue
		}
		for _, h := range strings.Split(f[0], ",") {
			e := normEntry(h)
			if _, done := trusted[e]; done {
				continue
			}
			addr := e
			if !strings.HasPrefix(e, "[") {
				addr = net.JoinHostPort(e, "22")
			} else if end := strings.Index(e, "]"); end > 0 {
				addr = net.JoinHostPort(e[1:end], strings.TrimPrefix(e[end+1:], ":"))
			}
			trusted[e] = len(k.KnownKeyTypes(addr)) > 0
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var add []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || existing[t] || strings.ContainsAny(t, "\r\n") {
			continue
		}
		if !LineReadable(t) {
			continue // one such line would make the check skip the whole file
		}
		// Imported lines name hosts literally: markers (@cert-authority,
		// and @revoked, which could not be removed here), hashed names and
		// patterns would reach far beyond the hosts the lines came with.
		f := strings.Fields(t)
		if len(f) < 2 || strings.HasPrefix(f[0], "@") || strings.HasPrefix(f[0], "|") || strings.ContainsAny(f[0], "*?!") {
			continue
		}
		skip := false
		for _, h := range strings.Split(f[0], ",") {
			if knownHost[normEntry(h)] || trusted[normEntry(h)] {
				skip = true
			}
		}
		if skip {
			continue
		}
		existing[t] = true
		add = append(add, t)
	}
	if len(add) == 0 {
		return 0, nil
	}
	if k.ReadOnly {
		return 0, ErrReadOnly
	}
	if err := os.MkdirAll(filepath.Dir(k.ownPath), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(k.ownPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(add, "\n") + "\n")
	return len(add), err
}
