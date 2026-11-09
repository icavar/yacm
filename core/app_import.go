// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icavar/yacm/internal/exchange"
	"github.com/icavar/yacm/internal/importer"
	"github.com/icavar/yacm/internal/store"
)

// ForeignRequest imports sessions from another program.
type ForeignRequest struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Password  string `json:"password"`  // SecureCRT passphrase, mRemoteNG / Xshell master password
	GroupName string `json:"groupName"` // name of the group that receives everything
}

func (a *App) foreignOptions(password string) importer.Options {
	user, sid := importer.CurrentWindowsAccount()
	a.importStop.Store(false)
	profiles := map[string]string{}
	for _, c := range a.store.Credentials() {
		profiles[strings.ToLower(c.Name)] = c.ID
	}
	return importer.Options{Password: password, WinUser: user, WinSID: sid,
		Progress: a.importProgress(), Cancelled: a.importStop.Load, Profiles: profiles}
}

// ImportProgress is sent while a slow import step runs.
type ImportProgress struct {
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Step  string `json:"step"`
}

// importProgress returns a callback that reports progress to the UI at most
// ten times a second (and always the last step).
func (a *App) importProgress() func(done, total int) {
	var last time.Time
	return func(done, total int) {
		if now := time.Now(); done == total || now.Sub(last) >= 100*time.Millisecond {
			last = now
			a.emit("import:progress", ImportProgress{Done: done, Total: total, Step: "Decrypting saved passwords"})
		}
	}
}

// CancelImport stops a running import preview or import.
func (a *App) CancelImport() { a.importStop.Store(true) }

// previewCache keeps the result of the last preview, so pressing Import does
// not read and decrypt everything a second time.
type previewCache struct {
	kind, path, password string
	stamp                string
	at                   time.Time
	res                  *importer.Result
}

// sourceStamp changes when the file or any file in the folder changes.
func sourceStamp(path string) string {
	var size int64
	var latest time.Time
	count := 0
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if count > 20000 {
			return filepath.SkipAll
		}
		if fi, e := d.Info(); e == nil {
			count++
			size += fi.Size()
			if fi.ModTime().After(latest) {
				latest = fi.ModTime()
			}
		}
		return nil
	})
	return fmt.Sprintf("%d/%d/%d", count, size, latest.UnixNano())
}

func (a *App) runForeign(kind importer.Kind, path, password string) (*importer.Result, error) {
	stamp := ""
	if kind != importer.KindPuTTYLive {
		stamp = sourceStamp(path)
	}
	a.previewMu.Lock()
	c := a.preview
	a.previewMu.Unlock()
	if c != nil && c.kind == string(kind) && c.path == path && c.password == password && c.stamp == stamp && kind != importer.KindPuTTYLive && time.Since(c.at) < 30*time.Minute {
		return c.res, nil
	}
	res, err := importer.Run(kind, path, "", a.foreignOptions(password))
	if err != nil {
		return nil, err
	}
	a.previewMu.Lock()
	a.preview = &previewCache{kind: string(kind), path: path, password: password, stamp: stamp, at: time.Now(), res: res}
	a.previewMu.Unlock()
	return res, nil
}

// ImportForeign imports from Ásbrú, SecureCRT, Xshell, MobaXterm, mRemoteNG,
// PuTTY or an OpenSSH config into a new group.
func (a *App) ImportForeign(req ForeignRequest) (retRep *importer.Report, err error) {
	defer logOp("import from "+req.Kind, &err)
	a.snapshot("before import from " + req.Kind)
	kind := importer.Kind(req.Kind)
	if _, ok := importer.Labels[kind]; !ok {
		return nil, fmt.Errorf("unknown import type %q", req.Kind)
	}
	name := strings.TrimSpace(req.GroupName)
	if name == "" {
		name = "Imported from " + importer.Labels[kind] + " " + time.Now().Format("2006-01-02")
	}
	group := &store.Node{ID: store.NewID(), IsGroup: true, Expanded: true, Name: name}
	res, err := a.runForeign(kind, req.Path, req.Password)
	if err != nil {
		return nil, err
	}
	// The result is used once: the next import reads the source again.
	a.previewMu.Lock()
	a.preview = nil
	a.previewMu.Unlock()
	if len(res.Nodes) == 0 {
		return &res.Report, nil
	}
	for _, n := range res.Nodes {
		if n.ParentID == "" {
			n.ParentID = group.ID
		}
	}
	res.Nodes = fromElsewhere(res.Nodes, &res.Report)
	creds := foreignProfiles(res, a.store.Credentials())
	if len(res.Secrets) > 0 {
		if !a.vault.Unlocked() {
			res.Report.Warnings = append(res.Report.Warnings, fmt.Sprintf("%d saved password(s) were not imported because the vault is locked.", len(res.Secrets)))
			res.Report.Secrets = 0
		} else if err := a.vault.SetMany(res.Secrets); err != nil {
			return nil, err
		}
	}
	if err := a.store.ApplyImport(append([]*store.Node{group}, res.Nodes...), nil, creds); err != nil {
		return nil, err
	}
	return &res.Report, nil
}

// foreignProfiles returns the credential profiles an import brings that
// are not here yet. A profile with the name and login of an existing one
// is not added again: its sessions use the existing profile and its
// password (also after it was named "name (imported)"). Another profile
// of the same name is added as "name (imported)".
// Sessions, jump hosts and secrets are updated to match.
func foreignProfiles(res *importer.Result, existing []*store.Credential) []*store.Credential {
	if len(res.Credentials) == 0 {
		return nil
	}
	taken := map[string]bool{}
	for _, c := range existing {
		taken[strings.ToLower(c.Name)] = true
	}
	// same: an earlier import of this profile, renamed or not.
	same := func(o, c *store.Credential) bool {
		on, cn := strings.ToLower(o.Name), strings.ToLower(c.Name)
		return (on == cn || strings.HasPrefix(on, cn+" (imported")) && o.User == c.User && o.Auth == c.Auth && o.KeyPath == c.KeyPath
	}
	remap := map[string]string{}
	var out []*store.Credential
	res.Report.Profiles, res.Report.ProfilesKept = 0, 0
	for _, c := range res.Credentials {
		cc := *c
		for _, p := range []*string{&cc.KeyPath, &cc.CertPath} {
			if store.IsNetworkPath(*p) {
				res.Report.Warnings = append(res.Report.Warnings, fmt.Sprintf("credential profile %s: a path to a file on another computer was left out", cc.Name))
				*p = ""
			}
		}
		if cc.Auth == store.AuthPublicKey && cc.KeyPath == "" {
			cc.Auth = store.AuthAsk
		}
		kept := ""
		for _, o := range existing {
			if same(o, &cc) {
				kept = o.ID
				break
			}
		}
		if kept != "" {
			remap[c.ID] = kept
			delete(res.Secrets, store.SecretCredPassword(c.ID))
			res.Report.ProfilesKept++
			continue
		}
		base := cc.Name
		for i := 1; taken[strings.ToLower(cc.Name)]; i++ {
			cc.Name = base + " (imported)"
			if i > 1 {
				cc.Name = fmt.Sprintf("%s (imported %d)", base, i)
			}
		}
		if cc.Name != base {
			res.Report.Warnings = append(res.Report.Warnings, fmt.Sprintf("credential profile %s: you already have a profile of this name with another login, so this one is named %s", base, cc.Name))
		}
		taken[strings.ToLower(cc.Name)] = true
		out = append(out, &cc)
		res.Report.Profiles++
	}
	for _, n := range res.Nodes {
		if ss := n.Session; ss != nil {
			if id, ok := remap[ss.CredentialID]; ok {
				ss.CredentialID = id
			}
			for i := range ss.Jump {
				if id, ok := remap[ss.Jump[i].CredentialID]; ok {
					ss.Jump[i].CredentialID = id
				}
			}
		}
	}
	res.Report.Secrets = len(res.Secrets)
	return out
}

// ImportAsbru is kept for older frontends.
func (a *App) ImportAsbru(path string) (*importer.Report, error) {
	return a.ImportForeign(ForeignRequest{Kind: string(importer.KindAsbru), Path: path})
}

// ExportSummary is returned after a successful export.
type ExportSummary struct {
	Path       string `json:"path"`
	Groups     int    `json:"groups"`
	Sessions   int    `json:"sessions"`
	Secrets    int    `json:"secrets"`
	KnownHosts int    `json:"knownHosts"`
	Settings   bool   `json:"settings"`
}

// ExportSessions writes a .yacm file.
func (a *App) ExportSessions(path string, opts exchange.ExportOptions) (retSum *ExportSummary, err error) {
	defer logOp("export", &err)
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("no file chosen")
	}
	nodes, scope, err := exchange.Collect(a.store.List(), opts.NodeIDs)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("there are no sessions to export")
	}
	creds := exchange.ReferencedCredentials(nodes, a.store.Credentials())
	var secrets map[string]string
	if opts.IncludeSecrets {
		if !a.vault.Unlocked() {
			return nil, errors.New("unlock the vault to export passwords")
		}
		if !a.vault.VerifyPassword(opts.Master) {
			time.Sleep(700 * time.Millisecond)
			return nil, errors.New("the master password is wrong: exporting passwords needs it")
		}
		if secrets, err = a.vault.Snapshot(exchange.SecretPrefixes(nodes, creds)); err != nil {
			return nil, err
		}
	}
	st := a.store.Settings()
	var addrs []string
	for _, n := range nodes {
		if s := n.Session; s != nil && s.Protocol == store.ProtoSSH {
			addrs = append(addrs, net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
			for _, j := range s.Jump {
				addrs = append(addrs, net.JoinHostPort(j.Host, strconv.Itoa(j.Port)))
			}
		}
	}
	f, err := exchange.Build(appName+" "+appVersion, nodes, creds, scope, secrets, opts, &st, a.kh.ExportLinesFor(addrs))
	if err != nil {
		return nil, err
	}
	data, err := f.Marshal()
	if err != nil {
		return nil, err
	}
	if err := store.WriteFileAtomic(path, data, 0o600); err != nil {
		return nil, err
	}
	sum := &ExportSummary{Path: path, KnownHosts: len(f.KnownHosts), Settings: f.Settings != nil}
	for _, n := range nodes {
		if n.IsGroup {
			sum.Groups++
		} else {
			sum.Sessions++
		}
	}
	if f.Secrets != nil {
		sum.Secrets = len(secrets)
	}
	return sum, nil
}

// ForeignInfo previews an import from another program.
type ForeignInfo struct {
	Label         string `json:"label"`
	Groups        int    `json:"groups"`
	Sessions      int    `json:"sessions"`
	Skipped       int    `json:"skipped"`
	Secrets       int    `json:"secrets"`
	SecretsFailed int    `json:"secretsFailed"`
	Automated     int    `json:"automated"`
	Profiles      int    `json:"profiles"`                // credential profiles (SecureCRT credential sets)
	PasswordLabel string `json:"passwordLabel,omitempty"` // set when a passphrase can unlock more
}

// ImportInfo tells the UI what kind of file or folder was picked.
type ImportInfo struct {
	Kind    string         `json:"kind"` // "yacm" or an importer kind
	Name    string         `json:"name"`
	Path    string         `json:"path"`
	Info    *exchange.Info `json:"info,omitempty"`
	Foreign *ForeignInfo   `json:"foreign,omitempty"`
}

var passwordLabels = map[importer.Kind]string{
	importer.KindSecureCRT:    "SecureCRT configuration passphrase",
	importer.KindSecureCRTXML: "SecureCRT configuration passphrase",
	importer.KindMRemoteNG:    "mRemoteNG password",
	importer.KindXshell:       "Xshell master password",
}

// PreviewForeign dry-runs a foreign import (nothing is stored).
func (a *App) PreviewForeign(kind, path, password string) (*ImportInfo, error) {
	k := importer.Kind(kind)
	res, err := a.runForeign(k, path, password)
	if err != nil {
		return nil, err
	}
	r := res.Report
	fi := &ForeignInfo{Label: importer.Labels[k], Groups: r.Groups, Sessions: r.Sessions, Skipped: len(r.Skipped), Secrets: r.Secrets, SecretsFailed: r.SecretsFailed, Automated: r.Automated, Profiles: r.Profiles}
	if l, ok := passwordLabels[k]; ok && (r.SecretsFailed > 0 || password != "") {
		fi.PasswordLabel = l
	}
	name := importer.DisplayPath(path)
	if k == importer.KindPuTTYLive {
		name = "PuTTY sessions on this computer"
	}
	return &ImportInfo{Kind: kind, Name: name, Path: path, Foreign: fi}, nil
}

const maxImportSize = 64 << 20

func readImport(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxImportSize {
		return nil, errors.New("file is too large to be a session export")
	}
	return os.ReadFile(path)
}

func (a *App) existingNodes() map[string]*store.Node {
	m := map[string]*store.Node{}
	for _, n := range a.store.List() {
		m[n.ID] = n
	}
	return m
}

func isTableFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv", ".xlsx", ".xlsm", ".xls":
		return true
	}
	return false
}

// InspectImport detects the format of a file or folder and summarises it.
func (a *App) InspectImport(path string) (*ImportInfo, error) {
	if isTableFile(path) {
		return &ImportInfo{Kind: "table", Name: filepath.Base(path), Path: path}, nil
	}
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		data, err := readImport(path)
		if err != nil {
			return nil, err
		}
		f, err := exchange.Parse(data)
		if err == nil {
			in := exchange.Inspect(f, a.existingNodes())
			return &ImportInfo{Kind: "yacm", Name: filepath.Base(path), Path: path, Info: &in}, nil
		}
		if !errors.Is(err, exchange.ErrNotYACM) {
			return nil, err
		}
	}
	kind, err := importer.Detect(path)
	if err != nil {
		return nil, err
	}
	return a.PreviewForeign(string(kind), path, "")
}

// ImportSessions imports a .yacm file.
func (a *App) ImportSessions(path string, opts exchange.ImportOptions) (retRep *exchange.Report, err error) {
	defer logOp("import .yacm", &err)
	a.snapshot("before import of a .yacm file")
	data, err := readImport(path)
	if err != nil {
		return nil, err
	}
	f, err := exchange.Parse(data)
	if err != nil {
		return nil, err
	}
	if f.Secrets != nil && opts.Password != "" && !a.vault.Unlocked() {
		return nil, errors.New("unlock the vault first so imported passwords can be stored safely")
	}
	existingCreds := map[string]*store.Credential{}
	for _, c := range a.store.Credentials() {
		existingCreds[c.ID] = c
	}
	plan, err := exchange.PlanImport(f, a.existingNodes(), existingCreds, opts)
	if err != nil {
		return nil, err
	}
	if err := a.store.ApplyImport(plan.Insert, plan.Update, plan.Credentials); err != nil {
		return nil, err
	}
	rep := plan.Report
	if len(plan.Secrets) > 0 {
		for _, prefix := range plan.ClearSecrets {
			_ = a.vault.DeletePrefix(prefix)
		}
		if err := a.vault.SetMany(plan.Secrets); err != nil {
			rep.Warnings = append(rep.Warnings, "Sessions were imported, but saving passwords failed: "+err.Error())
			rep.Secrets = 0
		}
	}
	if lines := plan.KnownHostLines(); len(lines) > 0 {
		n, err := a.kh.ImportLines(lines)
		if err != nil {
			rep.Warnings = append(rep.Warnings, "Could not add trusted host keys: "+err.Error())
		}
		rep.KnownHosts = n
	}
	if opts.ApplySettings && f.Settings != nil {
		if err := a.SaveSettings(lookFrom(a.store.Settings(), *f.Settings)); err == nil {
			rep.SettingsApplied = true
		}
	}
	return &rep, nil
}

// lookFrom takes only the look and feel from settings in someone else's
// file: theme, fonts, colours, highlighting and terminal habits. Security
// settings (vault lock, log masking, known hosts, team libraries, NetBox,
// log folder), saved commands and the rest stay yours.
func lookFrom(mine, file store.Settings) store.Settings {
	out := mine
	out.Theme = file.Theme
	out.FontFamily = file.FontFamily
	if file.FontSize > 0 {
		out.FontSize = file.FontSize
	}
	out.FontWeight = file.FontWeight
	out.TermText, out.TermBackground = file.TermText, file.TermBackground
	if file.Scrollback > 0 {
		out.Scrollback = file.Scrollback
	}
	out.CopyOnSelect = file.CopyOnSelect
	out.RightClick = file.RightClick
	out.PageKeys = file.PageKeys
	out.PasteDelay = file.PasteDelay
	out.Highlight = file.Highlight
	if file.HighlightRules != nil {
		out.HighlightRules = file.HighlightRules
	}
	out.AlertSound = file.AlertSound
	return out
}

// fromElsewhere applies store.FromElsewhere to imported nodes (local
// commands ask first, paths to other computers and odd serial devices go),
// reports what it changed and leaves out sessions that are no longer valid
// (a serial session whose device was dropped).
func fromElsewhere(nodes []*store.Node, rep *importer.Report) []*store.Node {
	asked, dropped := 0, 0
	out := nodes[:0]
	for _, n := range nodes {
		a, d := store.FromElsewhere(n)
		asked += a
		dropped += len(d)
		if err := store.Validate(n); err != nil {
			rep.Skipped = append(rep.Skipped, importer.Skip{Name: n.Name, Reason: err.Error()})
			continue
		}
		out = append(out, n)
	}
	if asked > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d local command(s) on this computer were imported; YACM asks before running each one.", asked))
	}
	if dropped > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d path(s) to files or devices on other computers (\\\\server\\share) were left out; on Windows, opening one sends your login to that server.", dropped))
	}
	return out
}
