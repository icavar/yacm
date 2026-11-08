// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/icavar/yacm/internal/importer"
	"github.com/icavar/yacm/internal/netbox"
	"github.com/icavar/yacm/internal/store"
)

// ---- spreadsheet and NetBox import ----------------------------------------------

// TablePreview shows the first rows of a device list and the guessed columns.
type TablePreview struct {
	Path    string           `json:"path"`
	Name    string           `json:"name"`
	Sheet   string           `json:"sheet,omitempty"`
	Headers []string         `json:"headers"`
	Rows    [][]string       `json:"rows"`
	Total   int              `json:"total"`
	Mapping importer.Mapping `json:"mapping"`
	Fields  []string         `json:"fields"`
}

// PreviewTable reads a CSV or Excel file for the column mapping dialog.
func (a *App) PreviewTable(path string) (*TablePreview, error) {
	t, err := importer.ReadTable(path)
	if err != nil {
		return nil, err
	}
	rows := t.Rows
	if len(rows) > 8 {
		rows = rows[:8]
	}
	return &TablePreview{Path: path, Name: filepath.Base(path), Sheet: t.Sheet, Headers: t.Headers, Rows: rows, Total: len(t.Rows),
		Mapping: importer.GuessMapping(t.Headers), Fields: importer.TableFields}, nil
}

// ImportTarget is where an import puts its sessions and what they inherit.
type ImportTarget struct {
	GroupName    string `json:"groupName"`
	CredentialID string `json:"credentialId"` // given to the new group as default login
	JumpVia      string `json:"jumpVia"`      // given to the new group as default jump host
	User         string `json:"user"`         // user name for sessions without a profile or their own
}

func (a *App) importGroup(t ImportTarget, fallback string) (*store.Node, error) {
	name := strings.TrimSpace(t.GroupName)
	if name == "" {
		name = fallback
	}
	g := &store.Node{ID: store.NewID(), IsGroup: true, Expanded: true, Name: name}
	d := &store.GroupDefaults{}
	if t.CredentialID != "" {
		if _, ok := a.store.Credential(t.CredentialID); !ok {
			return nil, errors.New("credential profile not found")
		}
		d.CredentialID = t.CredentialID
	}
	if t.JumpVia != "" {
		j, err := a.store.Get(t.JumpVia)
		if err != nil || j.Session == nil || j.Session.Protocol != store.ProtoSSH {
			return nil, errors.New("the jump host must be a saved SSH session")
		}
		d.JumpVia = t.JumpVia
	}
	if !d.IsEmpty() {
		g.Defaults = d
	}
	return g, nil
}

// TableRequest imports a device list.
type TableRequest struct {
	ImportTarget
	Path     string           `json:"path"`
	Mapping  importer.Mapping `json:"mapping"`
	Protocol string           `json:"protocol"` // used when there is no protocol column
}

// ImportTable imports a CSV or Excel device list into a new group.
func (a *App) ImportTable(req TableRequest) (retRep *importer.Report, err error) {
	defer logOp("import CSV/Excel", &err)
	a.snapshot("before import of a device list")
	t, err := importer.ReadTable(req.Path)
	if err != nil {
		return nil, err
	}
	g, err := a.importGroup(req.ImportTarget, "Imported from "+filepath.Base(req.Path))
	if err != nil {
		return nil, err
	}
	opts := importer.TableOptions{DefaultProtocol: req.Protocol, Credentials: map[string]string{}, Sessions: map[string]string{}}
	for _, c := range a.store.Credentials() {
		opts.Credentials[strings.ToLower(c.Name)] = c.ID
	}
	for _, n := range a.store.List() {
		if n.Session != nil && n.Session.Protocol == store.ProtoSSH {
			opts.Sessions[strings.ToLower(n.Name)] = n.ID
		}
	}
	res, err := importer.ImportTable(t, req.Mapping, g.ID, opts)
	if err != nil {
		return nil, err
	}
	// Rows without their own login take the group's profile; SSH rows
	// without a jump host take the group's jump host.
	for _, n := range res.Nodes {
		ss := n.Session
		if ss == nil {
			continue
		}
		if req.CredentialID != "" && ss.CredentialID == "" && ss.User == "" && res.Secrets[store.SecretPassword(n.ID)] == "" {
			ss.CredentialID = store.Inherit
		}
		if req.JumpVia != "" && ss.Protocol == store.ProtoSSH && ss.JumpVia == "" {
			ss.JumpVia = store.Inherit
		}
		if ss.User == "" && ss.Protocol != store.ProtoSerial {
			ss.User = strings.TrimSpace(req.User)
		}
	}
	return a.storeImported(g, res)
}

// storeImported saves an import's group, nodes and secrets.
func (a *App) storeImported(g *store.Node, res *importer.Result) (*importer.Report, error) {
	res.Nodes = fromElsewhere(res.Nodes, &res.Report)
	if len(res.Nodes) == 0 {
		return &res.Report, nil
	}
	if len(res.Secrets) > 0 {
		if !a.vault.Unlocked() {
			res.Report.Warnings = append(res.Report.Warnings, fmt.Sprintf("%d password(s) were not imported because the vault is locked.", len(res.Secrets)))
			res.Report.Secrets = 0
		} else if err := a.vault.SetMany(res.Secrets); err != nil {
			return nil, err
		}
	}
	if err := a.store.ImportNodes(append([]*store.Node{g}, res.Nodes...)); err != nil {
		return nil, err
	}
	return &res.Report, nil
}

const netboxTokenKey = "app/netbox/token"

// NetBoxRequest reads devices from NetBox.
type NetBoxRequest struct {
	netbox.Query
	ImportTarget
	SaveToken bool   `json:"saveToken"`
	GroupBy   string `json:"groupBy"`  // site, site-role, none
	Protocol  string `json:"protocol"` // ssh or telnet
	Port      int    `json:"port"`
}

// NetBoxInfo pre-fills the NetBox dialog.
type NetBoxInfo struct {
	URL      string `json:"url"`
	HasToken bool   `json:"hasToken"`
}

// NetBoxDefaults returns the last NetBox address and whether a token is saved.
func (a *App) NetBoxDefaults() NetBoxInfo {
	return NetBoxInfo{URL: a.store.Settings().NetBoxURL, HasToken: a.vault.Has(netboxTokenKey)}
}

// ForgetNetBoxToken removes the stored NetBox API token.
func (a *App) ForgetNetBoxToken() error {
	if !a.vault.Unlocked() {
		return errors.New("unlock the vault first")
	}
	return a.vault.DeletePrefix(netboxTokenKey)
}

// NetBoxPreview summarises what an import would bring in.
type NetBoxPreview struct {
	Devices int             `json:"devices"`
	VMs     int             `json:"vms"`
	Sites   []string        `json:"sites"`
	Sample  []netbox.Device `json:"sample"`
}

func (a *App) netboxFetch(req *NetBoxRequest) ([]netbox.Device, error) {
	if strings.TrimSpace(req.Token) == "" {
		req.Token = a.secret(netboxTokenKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	devs, err := netbox.Fetch(ctx, req.Query)
	if err != nil {
		return nil, err
	}
	// Remember the address, and the token when asked to.
	if st := a.store.Settings(); st.NetBoxURL != strings.TrimSpace(req.URL) {
		st.NetBoxURL = strings.TrimSpace(req.URL)
		_ = a.store.SetSettings(st)
	}
	if req.SaveToken && a.vault.Unlocked() && req.Token != "" {
		_ = a.vault.Set(netboxTokenKey, req.Token)
	}
	return devs, nil
}

// NetBoxPreview reads devices without importing them.
func (a *App) NetBoxPreview(req NetBoxRequest) (*NetBoxPreview, error) {
	devs, err := a.netboxFetch(&req)
	if err != nil {
		return nil, err
	}
	p := &NetBoxPreview{Sites: netbox.Sites(devs), Sample: devs}
	if len(p.Sample) > 12 {
		p.Sample = p.Sample[:12]
	}
	for _, d := range devs {
		if d.Kind == "vm" {
			p.VMs++
		} else {
			p.Devices++
		}
	}
	return p, nil
}

// NetBoxImport reads devices and imports them into a new group.
func (a *App) NetBoxImport(req NetBoxRequest) (retRep *importer.Report, err error) {
	defer logOp("import from NetBox", &err)
	a.snapshot("before import from NetBox")
	devs, err := a.netboxFetch(&req)
	if err != nil {
		return nil, err
	}
	if len(devs) == 0 {
		return nil, errors.New("NetBox has no devices with a primary IP address that match the filter")
	}
	g, err := a.importGroup(req.ImportTarget, "NetBox "+time.Now().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	nodes := netbox.BuildNodes(devs, g.ID, req.GroupBy, store.Protocol(req.Protocol), req.Port)
	for _, n := range nodes {
		if n.Session != nil {
			n.Session.User = strings.TrimSpace(req.User)
		}
	}
	res := &importer.Result{Nodes: nodes, Secrets: map[string]string{}, Report: importer.Report{Source: "NetBox"}}
	for _, n := range nodes {
		if n.IsGroup {
			res.Report.Groups++
		} else {
			res.Report.Sessions++
		}
	}
	if req.CredentialID == "" && strings.TrimSpace(req.User) == "" {
		res.Report.Warnings = append(res.Report.Warnings, "No credential profile or user name was chosen. Set a profile on the group (Edit group > Defaults) before connecting.")
	}
	return a.storeImported(g, res)
}

// KnownHostEntries lists trusted host keys.
