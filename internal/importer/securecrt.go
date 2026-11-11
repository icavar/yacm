// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"golang.org/x/crypto/blowfish"

	"github.com/yacm-app/yacm/internal/store"
)

// ---- password decryption ----------------------------------------------------
//
// SecureCRT stores passwords in three ways (public analysis by HyperSine,
// github.com/HyperSine/how-does-SecureCRT-encrypt-password):
//   "Password"          (< 7.3.3): two Blowfish-CBC passes with fixed keys
//   "Password V2" "02:" AES-256-CBC, key = SHA-256(config passphrase), IV 0
//   "Password V2" "03:" AES-256-CBC, key/IV = bcrypt_pbkdf(passphrase, salt, 16 rounds)
// V2 plaintext is: uint32 LE length | UTF-8 password | SHA-256(password) | padding.

var (
	scrtKey1 = []byte{0x24, 0xa6, 0x3d, 0xde, 0x5b, 0xd3, 0xb3, 0x82, 0x9c, 0x7e, 0x06, 0xf4, 0x08, 0x16, 0xaa, 0x07}
	scrtKey2 = []byte{0x5f, 0xb0, 0x45, 0xa2, 0x94, 0x17, 0xd9, 0x16, 0xc6, 0xc6, 0xa2, 0xff, 0x06, 0x41, 0x82, 0xb7}
)

func secureCRTDecryptV1(hexStr string) (string, error) {
	hexStr = strings.TrimPrefix(strings.TrimSpace(hexStr), "u")
	ct, err := hex.DecodeString(hexStr)
	if err != nil || len(ct) <= 8 || len(ct)%8 != 0 {
		return "", errors.New("bad ciphertext")
	}
	c1, _ := blowfish.NewCipher(scrtKey1)
	c2, _ := blowfish.NewCipher(scrtKey2)
	iv := make([]byte, 8)
	step1 := make([]byte, len(ct))
	cipher.NewCBCDecrypter(c1, iv).CryptBlocks(step1, ct)
	inner := step1[4 : len(step1)-4]
	if len(inner)%8 != 0 {
		return "", errors.New("bad ciphertext")
	}
	plain := make([]byte, len(inner))
	cipher.NewCBCDecrypter(c2, make([]byte, 8)).CryptBlocks(plain, inner)
	end := -1
	for i := 0; i+1 < len(plain); i += 2 {
		if plain[i] == 0 && plain[i+1] == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return "", errors.New("bad ciphertext")
	}
	u := make([]uint16, end/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(plain[2*i:])
	}
	return string(utf16.Decode(u)), nil
}

func secureCRTDecryptV2(value, passphrase string) (string, error) {
	prefix, hexStr, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return "", errors.New("missing prefix")
	}
	ct, err := hex.DecodeString(hexStr)
	if err != nil {
		return "", errors.New("bad ciphertext")
	}
	var key, iv []byte
	switch prefix {
	case "02":
		k := sha256.Sum256([]byte(passphrase))
		key, iv = k[:], make([]byte, aes.BlockSize)
	case "03":
		if len(ct) < 16 {
			return "", errors.New("bad ciphertext")
		}
		salt := ct[:16]
		ct = ct[16:]
		kdf := bcryptPBKDF([]byte(passphrase), salt, 16, 32+aes.BlockSize)
		key, iv = kdf[:32], kdf[32:]
	default:
		return "", fmt.Errorf("unknown password format %q", prefix)
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("bad ciphertext")
	}
	block, _ := aes.NewCipher(key)
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	if len(plain) < 4 {
		return "", errors.New("bad ciphertext")
	}
	n := int(binary.LittleEndian.Uint32(plain))
	if n < 0 || 4+n+sha256.Size > len(plain) {
		return "", errors.New("wrong passphrase")
	}
	pw := plain[4 : 4+n]
	sum := sha256.Sum256(pw)
	if !bytes.Equal(sum[:], plain[4+n:4+n+sha256.Size]) {
		return "", errors.New("wrong passphrase")
	}
	return string(pw), nil
}

// ---- session model -------------------------------------------------------------

// scrtSession is a parsed SecureCRT session, independent of ini/XML origin.
type scrtSession struct {
	path []string // folders
	rel  string   // "Folder/Name" used by jump references
	name string
	s    map[string]string
	d    map[string]int
}

func (s *scrtSession) str(k string) string { return strings.TrimSpace(s.s[strings.ToLower(k)]) }

// serialKey is k as this session stores it: SecureCRT on Linux and macOS
// writes the serial settings as "Linux Com Port", "Mac Baud Rate" and so on.
func (s *scrtSession) serialKey(k string) string {
	for _, pre := range []string{"", "Linux ", "Mac "} {
		key := strings.ToLower(pre + k)
		if _, ok := s.s[key]; ok {
			return pre + k
		}
		if _, ok := s.d[key]; ok {
			return pre + k
		}
	}
	return k
}
func (s *scrtSession) num(k string, def int) int {
	if v, ok := s.d[strings.ToLower(k)]; ok {
		return v
	}
	return def
}

// parseSCRTIni reads SecureCRT's S:/D:/Z:/B: key format.
func parseSCRTIni(text string) (map[string]string, map[string]int) {
	s, d := map[string]string{}, map[string]int{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if len(l) < 5 || l[1] != ':' || l[2] != '"' {
			continue
		}
		typ := l[0]
		rest := l[3:]
		end := strings.Index(rest, `"=`)
		if end < 0 {
			continue
		}
		key := strings.ToLower(rest[:end])
		val := rest[end+2:]
		switch typ {
		case 'S':
			s[key] = val
		case 'D':
			if n, err := strconv.ParseUint(strings.TrimSpace(val), 16, 32); err == nil {
				d[key] = int(int32(uint32(n)))
			}
		case 'Z', 'B':
			cnt, _ := strconv.ParseUint(strings.TrimSpace(val), 16, 32)
			var parts []string
			for j := 0; j < int(cnt) && i+1 < len(lines) && strings.HasPrefix(lines[i+1], " "); j++ {
				i++
				parts = append(parts, strings.TrimPrefix(lines[i], " "))
			}
			if typ == 'Z' {
				s[key] = strings.Join(parts, "\n")
			}
		}
	}
	return s, d
}

func importSecureCRTDir(dir, parentID string, opts Options) (*Result, error) {
	b := newBuilder("SecureCRT", parentID)
	// The SecureCRT folder or its Config folder chosen by hand: the sessions
	// are in Config/Sessions (the other files there are settings, button
	// bars, credentials and firewalls, not sessions).
	for _, sub := range []string{filepath.Join("Config", "Sessions"), "Sessions"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err == nil && fi.IsDir() {
			dir = filepath.Join(dir, sub)
			break
		}
	}
	// Credential sets and firewalls sit next to the Sessions folder, also
	// when only a folder inside it was chosen.
	var creds []*scrtSession
	sroot := scrtSessionsRoot(dir)
	if sroot != "" {
		b.scrtFW = readSCRTFirewalls(filepath.Join(filepath.Dir(sroot), "Firewalls"))
		creds = readSCRTCredentials(b, filepath.Join(filepath.Dir(sroot), "Credentials"))
	}
	var sessions []*scrtSession
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".ini") {
			return nil
		}
		base := d.Name()
		if strings.EqualFold(base, "__FolderData__.ini") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(strings.TrimSuffix(rel, filepath.Ext(rel)))
		// The templates new sessions start from, in the Sessions folder
		// itself (a session named Default in a folder is a real one).
		top := rel
		if sroot != "" {
			if r, err := filepath.Rel(sroot, p); err == nil {
				top = filepath.ToSlash(strings.TrimSuffix(r, filepath.Ext(r)))
			}
		}
		if strings.EqualFold(top, "Default") || strings.EqualFold(top, "Default_LocalShell") || strings.EqualFold(top, "Default_RDP") {
			return nil
		}
		data, err := readFile(p)
		if err != nil {
			b.warn(rel, err.Error())
			return nil
		}
		s, dw := parseSCRTIni(decodeText(data))
		parts := strings.Split(rel, "/")
		sessions = append(sessions, &scrtSession{path: parts[:len(parts)-1], rel: rel, name: parts[len(parts)-1], s: s, d: dw})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, errors.New("no SecureCRT session files (.ini) found")
	}
	if err := prefetchSCRT(b, append(creds, sessions...), opts); err != nil {
		return nil, err
	}
	addSCRTCredentials(b, creds, opts)
	addSCRTSessions(b, sessions, opts)
	return b.done(), nil
}

// scrtSessionsRoot returns dir or the folder above it named Sessions (the
// SecureCRT Config/Sessions folder), or "".
func scrtSessionsRoot(dir string) string {
	for d, i := filepath.Clean(dir), 0; i < 32; d, i = filepath.Dir(d), i+1 {
		if strings.EqualFold(filepath.Base(d), "Sessions") {
			return d
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return ""
}

// readSCRTCredentials reads SecureCRT's credential sets
// (Config/Credentials/<title>.ini); a missing folder gives none.
func readSCRTCredentials(b *builder, dir string) []*scrtSession {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*scrtSession
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".ini") || strings.EqualFold(e.Name(), "__FolderData__.ini") {
			continue
		}
		title := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		data, err := readFile(filepath.Join(dir, e.Name()))
		if err != nil {
			b.warn("credential set "+title, err.Error())
			continue
		}
		sv, dv := parseSCRTIni(decodeText(data))
		out = append(out, &scrtSession{rel: "credential set " + title, name: title, s: sv, d: dv})
	}
	return out
}

// addSCRTCredentials turns SecureCRT credential sets into credential
// profiles; sessions that name one by "Credential Title" use the profile.
func addSCRTCredentials(b *builder, creds []*scrtSession, opts Options) {
	sort.SliceStable(creds, func(i, j int) bool { return strings.ToLower(creds[i].name) < strings.ToLower(creds[j].name) })
	for _, cs := range creds {
		key := strings.ToLower(strings.TrimSpace(cs.name))
		if key == "" || b.scrtCreds[key] != nil {
			continue
		}
		c := &store.Credential{ID: store.NewID(), Name: strings.TrimSpace(cs.name), Description: "From SecureCRT", User: cs.str("Username")}
		pw := b.scrtPassword(cs, opts)
		switch k := scrtKeyPath(cs); {
		case k != "":
			c.Auth, c.KeyPath = store.AuthPublicKey, k
		case pw != "" || c.User != "":
			c.Auth = store.AuthPassword
		default:
			c.Auth = store.AuthAsk
		}
		if b.scrtCreds == nil {
			b.scrtCreds = map[string]*scrtCred{}
		}
		b.scrtCreds[key] = &scrtCred{c: c, pw: pw}
		b.res.Credentials = append(b.res.Credentials, c)
		b.secret(store.SecretCredPassword(c.ID), pw)
	}
}

// scrtCred is a SecureCRT credential set imported as a credential profile.
type scrtCred struct {
	c        *store.Credential
	pw       string
	existing bool // a profile YACM already has: its user name is not known here
}

// scrtCredential returns the credential set a session uses ("Credential
// Title"), or nil when it uses its own login.
func (b *builder) scrtCredential(sess *scrtSession, opts Options) *scrtCred {
	t := strings.TrimSpace(sess.str("Credential Title"))
	if t == "" {
		return nil
	}
	if c := b.scrtCreds[strings.ToLower(t)]; c != nil {
		return c
	}
	// Not in the imported files: a profile of that name YACM already has
	// (from an earlier import of the whole folder, say).
	if id := opts.Profiles[strings.ToLower(t)]; id != "" {
		c := &scrtCred{c: &store.Credential{ID: id, Name: t}, existing: true}
		if b.scrtCreds == nil {
			b.scrtCreds = map[string]*scrtCred{}
		}
		b.scrtCreds[strings.ToLower(t)] = c
		b.warn("credential set "+t, "not among the imported files; the sessions use your credential profile of that name")
		return c
	}
	b.warn(sess.rel, "credential set \""+t+"\" was not found (it is in SecureCRT's Config/Credentials folder); the session's own login is used")
	return nil
}

// secureCRTFile imports a single session .ini.
func secureCRTFile(b *builder, path []string, name, text string, opts Options, _ []*scrtSession) {
	s, d := parseSCRTIni(text)
	sessions := []*scrtSession{{path: path, rel: name, name: name, s: s, d: d}}
	_ = prefetchSCRT(b, sessions, opts)
	addSCRTSessions(b, sessions, opts)
}

// scrtDecrypt decrypts a "02:"/"03:" value, using the results of
// prefetchSCRT when there are any.
func (b *builder) scrtDecrypt(value, passphrase string) (string, error) {
	value = strings.TrimSpace(value)
	if r, ok := b.dec[value]; ok {
		return r.plain, r.err
	}
	return secureCRTDecryptV2(value, passphrase)
}

// scrtEncrypted lists the encrypted values a session will need.
func scrtEncrypted(sess *scrtSession) []string {
	var out []string
	if v := sess.str("Password V2"); strings.HasPrefix(v, "03:") {
		out = append(out, v)
	}
	if sess.num("Use Login Script", 0) == 1 {
		raw := sess.s["login script v4"]
		if raw == "" {
			raw = sess.s["login script v3"]
		}
		for _, l := range strings.Split(raw, "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "03:") {
				out = append(out, l)
			}
		}
	}
	return out
}

// prefetchSCRT decrypts every "03:" value on all CPU cores. Each one costs a
// deliberately slow bcrypt key derivation (about 0.15 s), so a few hundred
// sessions would otherwise take minutes.
func prefetchSCRT(b *builder, sessions []*scrtSession, opts Options) error {
	seen := map[string]bool{}
	var values []string
	for _, s := range sessions {
		for _, v := range scrtEncrypted(s) {
			if !seen[v] {
				seen[v] = true
				values = append(values, v)
			}
		}
	}
	b.dec = make(map[string]decrypted, len(values))
	if len(values) == 0 {
		return nil
	}
	workers := runtime.NumCPU()
	if workers > len(values) {
		workers = len(values)
	}
	jobs := make(chan string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	done := 0
	if opts.Progress != nil {
		opts.Progress(0, len(values))
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for v := range jobs {
				p, err := secureCRTDecryptV2(v, opts.Password)
				mu.Lock()
				b.dec[v] = decrypted{p, err}
				done++
				if opts.Progress != nil {
					opts.Progress(done, len(values))
				}
				mu.Unlock()
			}
		}()
	}
	cancelled := false
	for _, v := range values {
		if opts.cancelled() {
			cancelled = true
			break
		}
		jobs <- v
	}
	close(jobs)
	wg.Wait()
	if cancelled {
		return ErrCancelled
	}
	return nil
}

func (b *builder) scrtPassword(sess *scrtSession, opts Options) string {
	if v := sess.str("Password V2"); v != "" {
		pw, err := b.scrtDecrypt(v, opts.Password)
		if err != nil {
			b.failed()
			return ""
		}
		return pw
	}
	if v := sess.str("Password"); v != "" {
		pw, err := secureCRTDecryptV1(v)
		if err != nil {
			b.failed()
			return ""
		}
		return pw
	}
	return ""
}

func scrtKeyPath(sess *scrtSession) string {
	for _, k := range []string{"Identity Filename V2", "Identity Filename"} {
		if v := sess.str(k); v != "" {
			if i := strings.Index(v, "::"); i >= 0 {
				v = v[:i]
			}
			return v
		}
	}
	return ""
}

func addSCRTSessions(b *builder, sessions []*scrtSession, opts Options) {
	byRel := map[string]*scrtSession{}
	for _, s := range sessions {
		byRel[strings.ToLower(s.rel)] = s
	}
	sort.SliceStable(sessions, func(i, j int) bool { return strings.ToLower(sessions[i].rel) < strings.ToLower(sessions[j].rel) })
	for _, sess := range sessions {
		proto := strings.ToLower(sess.str("Protocol Name"))
		n := &store.Node{ID: store.NewID(), Name: sess.name, Description: sess.str("Description")}
		ss := &store.Session{}
		switch proto {
		case "ssh2", "ssh1", "":
			if proto == "" && sess.str("Hostname") == "" {
				b.skip(sess.rel, "", "no host name")
				continue
			}
			ss.Protocol = store.ProtoSSH
			ss.Port = sess.num("[SSH2] Port", 22)
			if proto == "ssh1" {
				ss.Port = sess.num("[SSH1] Port", 22)
				b.warn(sess.rel, "SSH1 is obsolete and insecure; imported as SSH2")
			}
		case "telnet":
			ss.Protocol = store.ProtoTelnet
			ss.Port = sess.num("Port", 23)
		case "serial":
			ss.Protocol = store.ProtoSerial
			k := sess.serialKey
			ss.Serial = &store.Serial{Device: sess.str(k("Com Port")), Baud: sess.num(k("Baud Rate"), 9600), DataBits: sess.num(k("Data Bits"), 8)}
			ss.Serial.Parity = []string{"none", "odd", "even", "mark", "space"}[clampIndex(sess.num(k("Parity"), 0), 5)]
			ss.Serial.StopBits = []string{"1", "1.5", "2"}[clampIndex(sess.num(k("Stop Bits"), 0), 3)]
		default:
			b.skip(sess.rel, sess.str("Protocol Name"), "only SSH, Telnet and serial sessions are supported")
			continue
		}
		ss.Host = sess.str("Hostname")
		// loginUser and loginPW are what logon actions may send: the
		// session's own login or that of its credential set.
		var pw, loginUser, loginPW string
		cred := b.scrtCredential(sess, opts)
		if cred != nil {
			ss.CredentialID = cred.c.ID
			loginUser, loginPW = cred.c.User, cred.pw
		} else {
			ss.User = sess.str("Username")
			pw = b.scrtPassword(sess, opts)
			loginUser, loginPW = ss.User, pw
		}
		jumpPW := ""
		if ss.Protocol == store.ProtoSSH {
			ss.Algorithms = store.AlgoCompatible
			if cred != nil {
				ss.Auth = store.AuthAsk // used only if the profile is deleted
			} else if key := scrtKeyPath(sess); key != "" {
				ss.Auth, ss.KeyPath = store.AuthPublicKey, key
			} else if pw != "" {
				ss.Auth = store.AuthPassword
			} else {
				ss.Auth = store.AuthAsk
			}
		}
		// A jump session ("Firewall" = Session:…) works for SSH and, since
		// 0.7, for telnet through the SSH jump host.
		if ss.Protocol == store.ProtoSSH || ss.Protocol == store.ProtoTelnet {
			if fw := sess.str("Firewall Name"); fw != "" && !strings.EqualFold(fw, "None") {
				if ref, ok := strings.CutPrefix(fw, "Session:"); ok {
					ref = strings.ToLower(strings.Trim(strings.ReplaceAll(ref, `\`, "/"), "/"))
					if js, ok := byRel[ref]; ok {
						j := store.JumpHost{Host: js.str("Hostname"), Port: js.num("[SSH2] Port", 22), User: js.str("Username"), Auth: store.AuthAgent}
						if jc := b.scrtCredential(js, opts); jc != nil {
							j.CredentialID, j.User = jc.c.ID, ""
						} else if key := scrtKeyPath(js); key != "" {
							j.Auth, j.KeyPath = store.AuthPublicKey, key
						} else if jpw := b.scrtPassword(js, opts); jpw != "" {
							j.Auth = store.AuthPassword
							jumpPW = jpw
						}
						ss.Jump = append(ss.Jump, j)
					} else {
						b.warn(sess.rel, "jump session "+fw+" was not found")
					}
				} else if px, note := b.scrtProxy(fw); px != nil {
					ss.Proxy = px
					if note != "" {
						b.warn(sess.rel, note)
					}
				} else {
					b.warn(sess.rel, note)
				}
			}
		}
		n.Session = ss
		hidden := b.scrtLogonActions(sess, n, loginUser, loginPW, opts)
		if b.add(sess.path, n) {
			b.secret(store.SecretPassword(n.ID), pw)
			b.secret(store.SecretJumpPassword(n.ID, 0), jumpPW)
			for k, v := range hidden {
				b.secret(k, v)
			}
		}
	}
}

// passwordPrompt recognises expect strings that wait for a secret.
var passwordPrompt = regexp.MustCompile(`(?i)(pass(word|code)?|word|secret|pin|lozinka)\s*:?\s*$`)

// scrtLogonActions turns SecureCRT's Logon Actions (Session Options >
// Connection > Logon Actions > Automate logon) into automation rules. It
// fills n.Session.Expect and InitialReturn and returns the vault entries of
// hidden values.
//
// "Login Script V4" is a list of lines, each encrypted like a password
// ("03:" with the configuration passphrase, or "02:"), whose plain text is
// flag \x1f expect \x1f send \x1f \x1f. Expect is plain text, matched
// literally; SecureCRT sends Enter after every send string.
func (b *builder) scrtLogonActions(sess *scrtSession, n *store.Node, user, sessionPW string, opts Options) map[string]string {
	ss := n.Session
	if sess.num("Use Script File", 0) == 1 {
		if f := sess.str("Script Filename V2"); f != "" {
			b.warn(sess.rel, "uses the logon script "+f+"; scripts are not imported, add automation rules instead")
		}
	}
	if sess.num("Use Login Script", 0) != 1 {
		return nil
	}
	if sess.num("Send Initial Carriage Return", 0) == 1 {
		ss.InitialReturn = true
	}
	raw := sess.s["login script v4"]
	if raw == "" {
		raw = sess.s["login script v3"]
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	secrets := map[string]string{}
	failed := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		plain := line
		if strings.HasPrefix(line, "02:") || strings.HasPrefix(line, "03:") {
			p, err := b.scrtDecrypt(line, opts.Password)
			if err != nil {
				failed++
				continue
			}
			plain = p
		}
		f := strings.Split(plain, "\x1f")
		if len(f) < 3 {
			failed++
			continue
		}
		flag, exp, send := strings.TrimSpace(f[0]), f[1], f[2]
		if exp == "" && send == "" {
			continue
		}
		r := store.ExpectRule{ID: store.NewID(), Active: true, Return: true, OnFail: "stop"}
		if exp != "" {
			r.Expect = regexp.QuoteMeta(exp)
		}
		switch {
		case sessionPW != "" && send == sessionPW:
			r.Send = "<PASS>"
		case user != "" && send == user:
			r.Send = "<USER>"
		case flag == "1" || passwordPrompt.MatchString(exp):
			r.Hidden = true
			secrets[store.SecretExpect(n.ID, r.ID)] = send
		default:
			r.Send = send
		}
		ss.Expect = append(ss.Expect, r)
	}
	if failed > 0 {
		b.warn(sess.rel, fmt.Sprintf("%d logon action(s) could not be read; if SecureCRT uses a configuration passphrase, enter it when importing", failed))
	}
	return secrets
}

func clampIndex(i, n int) int {
	if i < 0 || i >= n {
		return 0
	}
	return i
}

// ---- XML export (Tools > Export Settings) --------------------------------------

type scrtXMLNode struct {
	XMLName xml.Name
	Name    string        `xml:"name,attr"`
	Value   string        `xml:",chardata"`
	Kids    []scrtXMLNode `xml:",any"`
}

func parseDword(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if len(v) == 8 {
		if n, err := strconv.ParseUint(v, 16, 32); err == nil {
			return int(int32(uint32(n))), true
		}
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n, true
	}
	return 0, false
}

func importSecureCRTXML(data []byte, parentID string, opts Options) (*Result, error) {
	var root scrtXMLNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("not a valid SecureCRT XML export: %w", err)
	}
	var sessionsKey, credsKey *scrtXMLNode
	var find func(n *scrtXMLNode)
	find = func(n *scrtXMLNode) {
		for i := range n.Kids {
			k := &n.Kids[i]
			if k.XMLName.Local == "key" {
				switch {
				case sessionsKey == nil && strings.EqualFold(k.Name, "Sessions"):
					sessionsKey = k
					continue
				case credsKey == nil && strings.EqualFold(k.Name, "Credentials"):
					credsKey = k
					continue
				}
			}
			find(k)
		}
	}
	find(&root)
	if sessionsKey == nil {
		return nil, errors.New("the XML export contains no Sessions; export again with sessions selected")
	}
	b := newBuilder("SecureCRT", parentID)
	var sessions []*scrtSession
	var walk func(n *scrtXMLNode, path []string)
	walk = func(n *scrtXMLNode, path []string) {
		for i := range n.Kids {
			k := &n.Kids[i]
			if k.XMLName.Local != "key" {
				continue
			}
			s, d := scrtXMLValues(k)
			_, host := s["hostname"]
			_, proto := s["protocol name"]
			if host || proto {
				if len(path) == 0 && strings.EqualFold(k.Name, "Default") {
					continue
				}
				rel := strings.Join(append(append([]string{}, path...), k.Name), "/")
				sessions = append(sessions, &scrtSession{path: append([]string{}, path...), rel: rel, name: k.Name, s: s, d: d})
			} else {
				walk(k, append(path, k.Name))
			}
		}
	}
	walk(sessionsKey, nil)
	if len(sessions) == 0 {
		return nil, errors.New("no sessions found in the SecureCRT XML export")
	}
	var creds []*scrtSession
	if credsKey != nil {
		for i := range credsKey.Kids {
			if k := &credsKey.Kids[i]; k.XMLName.Local == "key" {
				s, d := scrtXMLValues(k)
				creds = append(creds, &scrtSession{rel: "credential set " + k.Name, name: k.Name, s: s, d: d})
			}
		}
	}
	if err := prefetchSCRT(b, append(creds, sessions...), opts); err != nil {
		return nil, err
	}
	addSCRTCredentials(b, creds, opts)
	addSCRTSessions(b, sessions, opts)
	return b.done(), nil
}

// scrtXMLValues reads the values of one key of the XML export.
func scrtXMLValues(k *scrtXMLNode) (map[string]string, map[string]int) {
	s, d := map[string]string{}, map[string]int{}
	for _, v := range k.Kids {
		name := strings.ToLower(v.Name)
		switch v.XMLName.Local {
		case "string":
			s[name] = v.Value
		case "dword":
			if x, ok := parseDword(v.Value); ok {
				d[name] = x
			}
		case "array":
			// Multi-line values such as "Login Script V4".
			var lines []string
			for _, it := range v.Kids {
				lines = append(lines, strings.TrimSpace(it.Value))
			}
			s[name] = strings.Join(lines, "\n")
		}
	}
	return s, d
}

// scrtFirewall is a named firewall of SecureCRT (Config/Firewalls/<name>.ini).
type scrtFirewall struct {
	typ, host, user string
	port            int
}

// readSCRTFirewalls reads SecureCRT's named firewalls; a missing folder
// gives none.
func readSCRTFirewalls(dir string) map[string]scrtFirewall {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]scrtFirewall{}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".ini") {
			continue
		}
		data, err := readFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sv, dv := parseSCRTIni(decodeText(data))
		f := &scrtSession{s: sv, d: dv}
		out[strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))] = scrtFirewall{
			typ: f.str("Firewall Type"), host: f.str("Firewall Address"), user: f.str("Firewall User"), port: f.num("Firewall Port", 0),
		}
	}
	return out
}

// scrtProxy turns a named SecureCRT firewall into YACM's outbound proxy.
// It returns nil and the reason when it cannot, and a note to show with a
// proxy that needs one.
func (b *builder) scrtProxy(name string) (*store.Proxy, string) {
	notTaken := "SecureCRT firewall \"" + name + "\" is not taken over: connects directly until a jump host or a proxy is set (on the session or its group)"
	fw, ok := b.scrtFW[strings.ToLower(name)]
	if !ok {
		return nil, notTaken
	}
	t := strings.ToLower(strings.ReplaceAll(fw.typ, " ", ""))
	p := &store.Proxy{Host: fw.host, Port: fw.port}
	switch {
	case strings.Contains(t, "socks") && strings.Contains(t, "5"):
		p.Type = "socks5"
		if p.Port == 0 {
			p.Port = 1080
		}
	case strings.Contains(t, "http"):
		p.Type = "http"
		if p.Port == 0 {
			p.Port = 8080
		}
	default:
		return nil, "SecureCRT firewall \"" + name + "\" (" + fw.typ + ") is not taken over, YACM has SOCKS5 and HTTP proxies: connects directly until a jump host or a proxy is set (on the session or its group)"
	}
	if p.Host == "" {
		return nil, notTaken
	}
	label := "HTTP"
	if p.Type == "socks5" {
		label = "SOCKS5"
	}
	where := fmt.Sprintf("%s:%d", p.Host, p.Port)
	switch {
	case fw.user != "":
		return p, "SecureCRT firewall \"" + name + "\" became the " + label + " proxy " + where + "; its login was not taken over: choose a credential profile as the proxy login (Edit > Tunnels and proxy, or the group)"
	case p.Host == "127.0.0.1" || p.Host == "localhost" || p.Host == "::1":
		return p, "SecureCRT firewall \"" + name + "\" became the " + label + " proxy " + where + " on this computer: whatever served that port for SecureCRT (often a -D tunnel of another session) must run for YACM too"
	}
	return p, ""
}
