// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package netbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

func TestFetchPagesRolesAndVMs(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token abc123" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("has_primary_ip") != "true" || r.URL.Query().Get("status") != "active" {
			t.Errorf("filters missing: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/dcim/devices/" && r.URL.Query().Get("offset") == "":
			if r.URL.Query().Get("site") != "zg-dc1" || r.URL.Query().Get("role") != "core" {
				t.Errorf("site/role filter: %s", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"count":3,"next":"%s/api/dcim/devices/?has_primary_ip=true&limit=500&offset=2&role=core&site=zg-dc1&status=active","results":[
			 {"id":1,"name":"zg-core-1","site":{"name":"Zagreb DC1","slug":"zg-dc1"},"role":{"name":"Core"},"platform":{"name":"IOS-XE"},"status":{"value":"active"},"primary_ip":{"address":"10.10.0.1/24"}},
			 {"id":2,"name":"zg-core-2","site":{"name":"Zagreb DC1"},"device_role":{"name":"Core"},"status":{"value":"active"},"primary_ip":{"address":"10.10.0.2/24"},"tags":[{"name":"prod"}]}]}`, srv.URL)
		case r.URL.Path == "/api/dcim/devices/":
			fmt.Fprint(w, `{"count":3,"next":null,"results":[{"id":3,"name":"","display":"unnamed-3","site":{"name":"Zagreb DC1"},"primary_ip":{"address":"2001:db8::3/64"}},{"id":4,"name":"no-ip","primary_ip":null}]}`)
		case r.URL.Path == "/api/virtualization/virtual-machines/":
			fmt.Fprint(w, `{"count":1,"next":null,"results":[{"id":9,"name":"jump-vm","cluster":{"name":"VMware ZG"},"role":{"name":"Bastion"},"primary_ip":{"address":"10.20.0.5/24"}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	devs, err := Fetch(context.Background(), Query{URL: srv.URL + "/api/", Token: "abc123", Site: "zg-dc1", Role: "core", IncludeVMs: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 4 {
		t.Fatalf("got %d devices: %+v", len(devs), devs)
	}
	byName := map[string]Device{}
	for _, d := range devs {
		byName[d.Name] = d
	}
	if d := byName["zg-core-1"]; d.Host != "10.10.0.1" || d.Role != "Core" || d.Platform != "IOS-XE" {
		t.Fatalf("device 1: %+v", d)
	}
	if d := byName["zg-core-2"]; d.Role != "Core" || len(d.Tags) != 1 {
		t.Fatalf("NetBox 3 device_role: %+v", d)
	}
	if d := byName["unnamed-3"]; d.Host != "2001:db8::3" {
		t.Fatalf("display name and IPv6: %+v", d)
	}
	if d := byName["jump-vm"]; d.Kind != "vm" || d.Site != "VMware ZG" {
		t.Fatalf("vm: %+v", d)
	}

	nodes := BuildNodes(devs, "root", GroupBySiteRole, store.ProtoSSH, 22)
	var groups, sessions int
	for _, n := range nodes {
		if n.IsGroup {
			groups++
		} else {
			sessions++
			if n.Session.CredentialID != store.Inherit || n.Session.JumpVia != store.Inherit {
				t.Fatal("imported sessions must inherit login and jump host from the import group")
			}
		}
	}
	// Zagreb DC1 > Core, Zagreb DC1 > No role, VMware ZG > Bastion
	if sessions != 4 || groups != 5 {
		t.Fatalf("groups %d sessions %d", groups, sessions)
	}

	_, err = Fetch(context.Background(), Query{URL: srv.URL, Token: "wrong"})
	if err != ErrUnauthorized {
		t.Fatalf("expected unauthorized, got %v", err)
	}
	_, err = Fetch(context.Background(), Query{URL: srv.URL + "/nothing", Token: "abc123"})
	if err == nil || !strings.Contains(err.Error(), "not look like NetBox") {
		t.Fatalf("expected not-found message, got %v", err)
	}
}

func TestAuthHeaderAndBaseURL(t *testing.T) {
	if authHeader("nbt_abc.def") != "Bearer nbt_abc.def" || authHeader(" 0123 ") != "Token 0123" {
		t.Fatal("auth header")
	}
	u, err := baseURL("netbox.example.net/api/")
	if err != nil || u.String() != "https://netbox.example.net" {
		t.Fatalf("base url %v %v", u, err)
	}
	if _, err := baseURL("ftp://x"); err == nil {
		t.Fatal("ftp must be refused")
	}
	// The token would cross the network in clear text.
	if _, err := baseURL("http://netbox.example.net"); err == nil {
		t.Fatal("http to another computer must be refused")
	}
	for _, local := range []string{"http://127.0.0.1:8000", "http://localhost/", "http://[::1]:8000"} {
		if _, err := baseURL(local); err != nil {
			t.Fatalf("%s: %v", local, err)
		}
	}
}
