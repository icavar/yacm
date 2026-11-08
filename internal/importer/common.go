// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/icavar/yacm/internal/store"
)

// Kind identifies a foreign configuration format.
type Kind string

const (
	KindAsbru        Kind = "asbru"
	KindSecureCRT    Kind = "securecrt"     // Sessions folder or a single .ini
	KindSecureCRTXML Kind = "securecrt-xml" // File > Export Settings
	KindXshell       Kind = "xshell"        // Sessions folder or a single .xsh
	KindMobaXterm    Kind = "mobaxterm"     // .mxtsessions or MobaXterm.ini
	KindMRemoteNG    Kind = "mremoteng"     // confCons.xml
	KindPuTTYReg     Kind = "putty-reg"     // registry export (.reg)
	KindPuTTYDir     Kind = "putty-dir"     // ~/.putty/sessions
	KindPuTTYLive    Kind = "putty-live"    // Windows registry of this computer
	KindSSHConfig    Kind = "ssh-config"    // OpenSSH client config
)

// Labels are shown in the UI.
var Labels = map[Kind]string{
	KindAsbru:        "Ásbrú / PAC Manager",
	KindSecureCRT:    "SecureCRT",
	KindSecureCRTXML: "SecureCRT (XML export)",
	KindXshell:       "Xshell",
	KindMobaXterm:    "MobaXterm",
	KindMRemoteNG:    "mRemoteNG",
	KindPuTTYReg:     "PuTTY (registry export)",
	KindPuTTYDir:     "PuTTY",
	KindPuTTYLive:    "PuTTY",
	KindSSHConfig:    "OpenSSH config",
}

// Options carry what some formats need to decrypt passwords.
type Options struct {
	// Password is the SecureCRT config passphrase, the mRemoteNG master
	// password or the Xshell master password. Empty = the program's default.
	Password string
	// WinUser and WinSID identify the Windows account that saved Xshell
	// passwords. Filled automatically on Windows.
	WinUser string
	WinSID  string
	// Progress, when set, is told how far slow steps (decrypting saved
	// passwords) have come. It may be called from several goroutines, but
	// never at the same time.
	Progress func(done, total int)
	// Cancelled, when set, is polled during slow steps; returning true
	// stops the import with ErrCancelled.
	Cancelled func() bool
}

// ErrCancelled is returned when Options.Cancelled asked to stop.
var ErrCancelled = errors.New("import cancelled")

func (o Options) cancelled() bool { return o.Cancelled != nil && o.Cancelled() }

// ErrUnknownFormat is returned when a file cannot be recognised.
var ErrUnknownFormat = errors.New("unrecognised file: expected an export from YACM, Ásbrú, SecureCRT, Xshell, MobaXterm, mRemoteNG, PuTTY or an OpenSSH config")

const maxFile = 64 << 20

func readFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxFile {
		return nil, fmt.Errorf("%s is too large", filepath.Base(path))
	}
	return os.ReadFile(path)
}

// decodeText turns UTF-8 / UTF-16 (with or without BOM) / Windows-1252
// bytes into a Go string.
func decodeText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return utf16Decode(b[2:], binary.LittleEndian)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return utf16Decode(b[2:], binary.BigEndian)
	}
	// UTF-16LE without BOM: lots of zero bytes at odd positions.
	if len(b) >= 4 && len(b)%2 == 0 {
		zeros := 0
		for i := 1; i < len(b) && i < 400; i += 2 {
			if b[i] == 0 {
				zeros++
			}
		}
		if zeros*2 > min(len(b), 400)/2 {
			return utf16Decode(b, binary.LittleEndian)
		}
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return cp1252(b)
}

func utf16Decode(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

var cp1252High = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

func cp1252(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// builder accumulates nodes for one import, creating groups on demand.
type builder struct {
	res       *Result
	root      string            // parent of top-level entries
	groups    map[string]string // "a/b" -> node id
	order     map[string]int
	source    string
	dec       map[string]decrypted    // SecureCRT values decrypted ahead of time
	scrtFW    map[string]scrtFirewall // SecureCRT named firewalls, by lower-case name
	scrtCreds map[string]*scrtCred    // SecureCRT credential sets, by lower-case title
}

type decrypted struct {
	plain string
	err   error
}

func newBuilder(source, parentID string) *builder {
	return &builder{
		res:    &Result{Secrets: map[string]string{}, Report: Report{Source: source}},
		root:   parentID,
		groups: map[string]string{},
		order:  map[string]int{},
		source: source,
	}
}

func cleanPath(path []string) []string {
	var out []string
	for _, p := range path {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// group returns the id of the group at path, creating missing groups.
func (b *builder) group(path []string) string {
	path = cleanPath(path)
	parent := b.root
	for i := range path {
		key := strings.Join(path[:i+1], "\x00")
		if id, ok := b.groups[key]; ok {
			parent = id
			continue
		}
		n := &store.Node{ID: store.NewID(), ParentID: parent, IsGroup: true, Name: path[i], Expanded: true, Order: b.order[parent]}
		b.order[parent]++
		b.groups[key] = n.ID
		b.res.Nodes = append(b.res.Nodes, n)
		b.res.Report.Groups++
		parent = n.ID
	}
	return parent
}

// add validates and appends a session under path.
func (b *builder) add(path []string, n *store.Node) bool {
	if n.ID == "" {
		n.ID = store.NewID()
	}
	if err := store.Validate(n); err != nil {
		b.skip(n.Name, "", err.Error())
		return false
	}
	n.ParentID = b.group(path)
	n.Order = b.order[n.ParentID]
	b.order[n.ParentID]++
	b.res.Nodes = append(b.res.Nodes, n)
	b.res.Report.Sessions++
	return true
}

func (b *builder) skip(name, method, reason string) {
	b.res.Report.Skipped = append(b.res.Report.Skipped, Skip{Name: name, Method: method, Reason: reason})
}

func (b *builder) warn(name, msg string) {
	if name != "" {
		msg = name + ": " + msg
	}
	b.res.Report.Warnings = append(b.res.Report.Warnings, msg)
}

func (b *builder) secret(key, val string) {
	if val != "" {
		b.res.Secrets[key] = val
	}
}

func (b *builder) failed() { b.res.Report.SecretsFailed++ }

func (b *builder) done() *Result {
	b.res.Report.Secrets = len(b.res.Secrets)
	b.res.Report.Profiles = len(b.res.Credentials)
	for _, n := range b.res.Nodes {
		if n.Session != nil && len(n.Session.Expect) > 0 {
			b.res.Report.Automated++
		}
	}
	// Drop groups that ended up empty (e.g. everything inside was skipped).
	used := map[string]bool{}
	for _, n := range b.res.Nodes {
		if !n.IsGroup {
			for p := n.ParentID; p != "" && p != b.root; {
				used[p] = true
				var next string
				for _, g := range b.res.Nodes {
					if g.ID == p {
						next = g.ParentID
						break
					}
				}
				p = next
			}
		}
	}
	var kept []*store.Node
	for _, n := range b.res.Nodes {
		if n.IsGroup && !used[n.ID] {
			b.res.Report.Groups--
			continue
		}
		kept = append(kept, n)
	}
	b.res.Nodes = kept
	if b.res.Report.SecretsFailed > 0 {
		b.res.Report.Warnings = append(b.res.Report.Warnings,
			fmt.Sprintf("%d stored password(s) could not be decrypted. If %s is protected with a passphrase or master password, enter it and import again.", b.res.Report.SecretsFailed, b.source))
	}
	return b.res
}

// Detect works out what kind of configuration path is.
func Detect(path string) (Kind, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return detectDir(path)
	}
	data, err := readFile(path)
	if err != nil {
		return "", err
	}
	return detectData(filepath.Base(path), data)
}

func detectDir(dir string) (Kind, error) {
	var ini, xsh, putty int
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		switch {
		case strings.HasSuffix(name, ".xsh"):
			xsh++
		case strings.HasSuffix(name, ".ini"):
			if b, err := readFile(p); err == nil && strings.Contains(decodeText(b), `S:"Hostname"`) {
				ini++
			}
		default:
			if b, err := readFile(p); err == nil && len(b) < 1<<20 {
				t := decodeText(b)
				if strings.Contains(t, "\nHostName=") || strings.HasPrefix(t, "HostName=") || strings.Contains(t, "\nProtocol=") {
					putty++
				}
			}
		}
		return nil
	})
	switch {
	case xsh > 0 && xsh >= ini:
		return KindXshell, nil
	case ini > 0:
		return KindSecureCRT, nil
	case putty > 0:
		return KindPuTTYDir, nil
	}
	return "", errors.New("no SecureCRT, Xshell or PuTTY sessions found in this folder")
}

func detectData(name string, data []byte) (Kind, error) {
	t := decodeText(data)
	trim := strings.TrimSpace(t)
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".xsh") || (strings.Contains(t, "[CONNECTION]") && strings.Contains(t, "[CONNECTION:AUTHENTICATION]")):
		return KindXshell, nil
	case strings.HasPrefix(trim, "<?xml") || strings.HasPrefix(trim, "<"):
		head := t
		if len(head) > 4096 {
			head = head[:4096]
		}
		if strings.Contains(head, "<VanDyke") {
			return KindSecureCRTXML, nil
		}
		if strings.Contains(head, "Connections") && (strings.Contains(head, "ConfVersion") || strings.Contains(head, "mrng")) {
			return KindMRemoteNG, nil
		}
	case strings.Contains(t, `S:"Hostname"`):
		return KindSecureCRT, nil
	case strings.Contains(t, "[Bookmarks") && strings.Contains(t, "SubRep="):
		return KindMobaXterm, nil
	case strings.Contains(t, "Windows Registry Editor") || strings.HasPrefix(trim, "REGEDIT4"):
		if strings.Contains(t, `\Sessions\`) {
			return KindPuTTYReg, nil
		}
	case strings.Contains(t, "environments:"):
		return KindAsbru, nil
	}
	if looksLikeSSHConfig(t) {
		return KindSSHConfig, nil
	}
	return "", ErrUnknownFormat
}

// Run imports path as kind, placing everything under parentID.
func Run(kind Kind, path, parentID string, opts Options) (*Result, error) {
	switch kind {
	case KindPuTTYLive:
		return importPuTTYLive(parentID)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		switch kind {
		case KindSecureCRT:
			return importSecureCRTDir(path, parentID, opts)
		case KindXshell:
			return importXshellDir(path, parentID, opts)
		case KindPuTTYDir:
			return importPuTTYDir(path, parentID)
		}
		return nil, fmt.Errorf("%s cannot be imported from a folder", Labels[kind])
	}
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return runData(kind, path, data, parentID, opts)
}

// runData imports the contents of one file (path names it and, for
// ~/.ssh/config, gives the folder for Include).
func runData(kind Kind, path string, data []byte, parentID string, opts Options) (*Result, error) {
	switch kind {
	case KindAsbru:
		return Asbru(data, parentID)
	case KindSecureCRT:
		b := newBuilder("SecureCRT", parentID)
		secureCRTFile(b, nil, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), decodeText(data), opts, nil)
		return b.done(), nil
	case KindSecureCRTXML:
		return importSecureCRTXML(data, parentID, opts)
	case KindXshell:
		b := newBuilder("Xshell", parentID)
		xshellFile(b, nil, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), decodeText(data), opts)
		return b.done(), nil
	case KindMobaXterm:
		return importMobaXterm(decodeText(data), parentID)
	case KindMRemoteNG:
		return importMRemoteNG(data, parentID, opts)
	case KindPuTTYReg:
		return importPuTTYReg(decodeText(data), parentID)
	case KindSSHConfig:
		return importSSHConfig(decodeText(data), filepath.Dir(path), parentID)
	}
	return nil, ErrUnknownFormat
}

// Source is an import source found on this computer.
type Source struct {
	Kind  Kind   `json:"kind"`
	Label string `json:"label"`
	Path  string `json:"path"`
	// Display is Path shortened for people: ~ for the home folder and
	// %APPDATA% on Windows.
	Display string `json:"display"`
}

// DisplayPath shortens p for display.
func DisplayPath(p string) string {
	if a := os.Getenv("APPDATA"); a != "" && strings.HasPrefix(strings.ToLower(p), strings.ToLower(a)+string(filepath.Separator)) {
		return "%APPDATA%" + p[len(a):]
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// FindSources looks for configurations of other programs in their default
// locations on this computer.
func FindSources() []Source {
	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	var out []Source
	add := func(kind Kind, path string) {
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		for _, s := range out {
			if s.Path == path {
				return
			}
		}
		out = append(out, Source{Kind: kind, Label: Labels[kind], Path: path, Display: DisplayPath(path)})
	}
	if appData != "" {
		add(KindSecureCRT, filepath.Join(appData, "VanDyke", "Config", "Sessions"))
		add(KindMobaXterm, filepath.Join(appData, "MobaXterm", "MobaXterm.ini"))
		add(KindMRemoteNG, filepath.Join(appData, "mRemoteNG", "confCons.xml"))
	}
	if home != "" {
		for _, v := range []string{"8", "7", "6"} {
			add(KindXshell, filepath.Join(home, "Documents", "NetSarang Computer", v, "Xshell", "Sessions"))
		}
		add(KindSecureCRT, filepath.Join(home, "Library", "Application Support", "VanDyke", "SecureCRT", "Config", "Sessions"))
		add(KindSecureCRT, filepath.Join(home, ".vandyke", "SecureCRT", "Config", "Sessions"))
		add(KindPuTTYDir, filepath.Join(home, ".putty", "sessions"))
		add(KindAsbru, filepath.Join(home, ".config", "asbru", "asbru.yml"))
		add(KindAsbru, filepath.Join(home, ".config", "pac", "pac.yml"))
		add(KindSSHConfig, filepath.Join(home, ".ssh", "config"))
	}
	if puttyLiveAvailable() {
		out = append(out, Source{Kind: KindPuTTYLive, Label: "PuTTY", Path: `HKEY_CURRENT_USER\Software\SimonTatham\PuTTY\Sessions`, Display: "Windows registry"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
