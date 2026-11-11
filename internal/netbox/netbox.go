// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package netbox reads devices (and optionally virtual machines) from a
// NetBox server through its REST API and turns them into sessions.
package netbox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yacm-app/yacm/internal/store"
)

// Query selects what to read.
type Query struct {
	URL         string `json:"url"`
	Token       string `json:"token"`
	Site        string `json:"site"`   // site slug
	Role        string `json:"role"`   // device role slug
	Tag         string `json:"tag"`    // tag slug
	Status      string `json:"status"` // default "active"; "any" = all
	IncludeVMs  bool   `json:"includeVms"`
	InsecureTLS bool   `json:"insecureTls"` // accept a self-signed certificate
}

// Device is one NetBox device or VM that has a primary IP address.
type Device struct {
	ID       int      `json:"id"`
	Kind     string   `json:"kind"` // "device" or "vm"
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Site     string   `json:"site"`
	Role     string   `json:"role"`
	Platform string   `json:"platform"`
	Tenant   string   `json:"tenant"`
	Status   string   `json:"status"`
	Tags     []string `json:"tags"`
}

type named struct {
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Display string `json:"display"`
}

func (n *named) label() string {
	if n == nil {
		return ""
	}
	if n.Name != "" {
		return n.Name
	}
	return n.Display
}

type apiObject struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Display    string `json:"display"`
	Site       *named `json:"site"`
	Cluster    *named `json:"cluster"`
	Role       *named `json:"role"`
	DeviceRole *named `json:"device_role"` // NetBox 3.x
	Platform   *named `json:"platform"`
	Tenant     *named `json:"tenant"`
	Status     *struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"status"`
	PrimaryIP *struct {
		Address string `json:"address"`
	} `json:"primary_ip"`
	Tags []named `json:"tags"`
}

type page struct {
	Count   int         `json:"count"`
	Next    *string     `json:"next"`
	Results []apiObject `json:"results"`
}

// ErrUnauthorized means the token was refused.
var ErrUnauthorized = errors.New("NetBox refused the API token (check it and that it may read devices)")

func baseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("enter the NetBox address")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%q is not a valid address", raw)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		// NetBox on this computer: the token stays here.
	case u.Scheme == "http":
		return nil, errors.New("use https:// for NetBox: with http:// the API token would cross the network in clear text")
	default:
		return nil, errors.New("the address must start with https://")
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api")
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func client(q Query) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if q.InsecureTLS {
		tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // explicit user choice for self-signed lab servers
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

func authHeader(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "nbt_") { // NetBox 4.5+ v2 tokens
		return "Bearer " + token
	}
	return "Token " + token
}

// Fetch reads all matching devices (and VMs) that have a primary IP.
func Fetch(ctx context.Context, q Query) ([]Device, error) {
	base, err := baseURL(q.URL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(q.Token) == "" {
		return nil, errors.New("enter an API token")
	}
	hc := client(q)
	params := url.Values{"limit": {"500"}, "has_primary_ip": {"true"}}
	status := strings.TrimSpace(q.Status)
	if status == "" {
		status = "active"
	}
	if status != "any" {
		params.Set("status", status)
	}
	if q.Site != "" {
		params.Set("site", strings.TrimSpace(q.Site))
	}
	if q.Tag != "" {
		params.Set("tag", strings.TrimSpace(q.Tag))
	}
	var out []Device
	devParams := url.Values{}
	for k, v := range params {
		devParams[k] = v
	}
	if q.Role != "" {
		devParams.Set("role", strings.TrimSpace(q.Role))
	}
	devs, err := fetchAll(ctx, hc, base, "/api/dcim/devices/", devParams, q.Token, "device")
	if err != nil {
		return nil, err
	}
	out = append(out, devs...)
	if q.IncludeVMs {
		vmParams := url.Values{}
		for k, v := range params {
			vmParams[k] = v
		}
		if q.Role != "" {
			vmParams.Set("role", strings.TrimSpace(q.Role))
		}
		if q.Site != "" { // VMs belong to a site through their cluster too
			vmParams.Set("site", strings.TrimSpace(q.Site))
		}
		vms, err := fetchAll(ctx, hc, base, "/api/virtualization/virtual-machines/", vmParams, q.Token, "vm")
		if err != nil {
			return nil, fmt.Errorf("virtual machines: %w", err)
		}
		out = append(out, vms...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Site != out[j].Site {
			return out[i].Site < out[j].Site
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func fetchAll(ctx context.Context, hc *http.Client, base *url.URL, path string, params url.Values, token, kind string) ([]Device, error) {
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + path
	u.RawQuery = params.Encode()
	next := u.String()
	var out []Device
	for pages := 0; next != "" && pages < 100; pages++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", authHeader(token))
		req.Header.Set("Accept", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				var certErr *tls.CertificateVerificationError
				if errors.As(err, &certErr) {
					return nil, errors.New("the NetBox certificate is not trusted; if this is your own self-signed server, tick \"Accept a self-signed certificate\"")
				}
				var dnsErr *net.DNSError
				if errors.As(err, &dnsErr) {
					return nil, fmt.Errorf("cannot find %s", dnsErr.Name)
				}
			}
			return nil, fmt.Errorf("cannot reach NetBox: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusNotFound:
			return nil, errors.New("this address does not look like NetBox (the API was not found)")
		case resp.StatusCode == http.StatusBadRequest:
			return nil, fmt.Errorf("NetBox did not accept the filter: %s", strings.TrimSpace(string(body)))
		case resp.StatusCode >= 300:
			return nil, fmt.Errorf("NetBox answered %s", resp.Status)
		}
		var p page
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, errors.New("NetBox sent something that is not a device list")
		}
		for _, o := range p.Results {
			d := toDevice(o, kind)
			if d.Host != "" {
				out = append(out, d)
			}
		}
		next = ""
		if p.Next != nil && *p.Next != "" {
			// Follow the server's own link, but never to a different host.
			if nu, err := url.Parse(*p.Next); err == nil && nu.Host == base.Host {
				nu.Scheme = base.Scheme
				next = nu.String()
			}
		}
	}
	return out, nil
}

func toDevice(o apiObject, kind string) Device {
	d := Device{ID: o.ID, Kind: kind, Name: o.Name}
	if d.Name == "" {
		d.Name = o.Display
	}
	if d.Name == "" {
		d.Name = fmt.Sprintf("%s-%d", kind, o.ID)
	}
	if o.PrimaryIP != nil {
		d.Host = strings.SplitN(o.PrimaryIP.Address, "/", 2)[0]
	}
	d.Site = o.Site.label()
	if d.Site == "" && o.Cluster != nil {
		d.Site = o.Cluster.label()
	}
	d.Role = o.Role.label()
	if d.Role == "" {
		d.Role = o.DeviceRole.label()
	}
	d.Platform, d.Tenant = o.Platform.label(), o.Tenant.label()
	if o.Status != nil {
		d.Status = o.Status.Value
	}
	for _, t := range o.Tags {
		d.Tags = append(d.Tags, t.label())
	}
	return d
}

// Layout options for BuildNodes.
const (
	GroupBySite     = "site"
	GroupBySiteRole = "site-role"
	GroupByNone     = "none"
)

// BuildNodes turns devices into SSH (or telnet) sessions under parentID,
// grouped by site (and role). Every device gets "inherit" for the login,
// jump host and algorithms so the import group's defaults apply.
func BuildNodes(devs []Device, parentID, groupBy string, proto store.Protocol, port int) []*store.Node {
	if proto != store.ProtoTelnet {
		proto = store.ProtoSSH
	}
	var out []*store.Node
	groups := map[string]string{}
	order := map[string]int{}
	group := func(path ...string) string {
		parent := parentID
		for i := range path {
			if path[i] == "" {
				path[i] = "No site"
			}
			key := strings.Join(path[:i+1], "\x00")
			if id, ok := groups[key]; ok {
				parent = id
				continue
			}
			g := &store.Node{ID: store.NewID(), IsGroup: true, Name: path[i], ParentID: parent, Order: order[parent]}
			order[parent]++
			groups[key] = g.ID
			out = append(out, g)
			parent = g.ID
		}
		return parent
	}
	for _, d := range devs {
		var parent string
		switch groupBy {
		case GroupByNone:
			parent = parentID
		case GroupBySiteRole:
			role := d.Role
			if role == "" {
				role = "No role"
			}
			parent = group(d.Site, role)
		default:
			parent = group(d.Site)
		}
		var desc []string
		for _, s := range []string{d.Role, d.Platform, d.Tenant} {
			if s != "" {
				desc = append(desc, s)
			}
		}
		if d.Kind == "vm" {
			desc = append(desc, "virtual machine")
		}
		n := &store.Node{ID: store.NewID(), ParentID: parent, Name: d.Name, Description: strings.Join(desc, " · "), Order: order[parent],
			Session: &store.Session{Protocol: proto, Host: d.Host, Port: port, CredentialID: store.Inherit, Clusters: nil}}
		order[parent]++
		if proto == store.ProtoSSH {
			n.Session.Auth = store.AuthAsk
			n.Session.JumpVia = store.Inherit
			n.Session.Algorithms = store.Inherit
		}
		if store.Validate(n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// Sites returns the distinct site names, for the preview.
func Sites(devs []Device) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range devs {
		if d.Site != "" && !seen[d.Site] {
			seen[d.Site] = true
			out = append(out, d.Site)
		}
	}
	sort.Strings(out)
	return out
}
