// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// mkdemo writes a .yacm file with credential profiles, a bastion and a
// group with defaults, for manual testing against cmd/yacm-demo-device.
package main

import (
	"fmt"
	"os"

	"github.com/yacm-app/yacm/internal/exchange"
	"github.com/yacm-app/yacm/internal/store"
)

func main() {
	lab := &store.Credential{ID: "cred-lab", Name: "Lab admin", User: "admin", Auth: store.AuthPassword}
	ad := &store.Credential{ID: "cred-ad", Name: "AD account", User: "ivan", Auth: store.AuthPassword}
	ssh := func(id, parent, name string, port int) *store.Node {
		return &store.Node{ID: id, ParentID: parent, Name: name, Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: port,
			Auth: store.AuthPassword, CredentialID: store.Inherit, JumpVia: store.Inherit, Algorithms: store.Inherit}}
	}
	bastion := ssh("bastion", "", "bastion-zg", 2222)
	bastion.Session.CredentialID, bastion.Session.JumpVia, bastion.Session.Algorithms = "cred-lab", "", store.AlgoSecure
	bastion.Color = "green"
	// New in 0.8: a tunnel to try (tunnels from a .yacm file arrive switched
	// off; start it from Tunnels in the status bar).
	bastion.Session.Tunnels = []store.Tunnel{{ID: "tun-demo", Type: "local", Name: "Demo telnet", Port: 12323, Host: "127.0.0.1", HostPort: 2323}}
	dc := &store.Node{ID: "dc", IsGroup: true, Expanded: true, Name: "DC Zagreb", Color: "red",
		Defaults: &store.GroupDefaults{CredentialID: "cred-lab", JumpVia: "bastion", Algorithms: store.AlgoLegacy}}
	access := &store.Node{ID: "access", IsGroup: true, ParentID: "dc", Name: "Access switches", Color: "orange"}
	nodes := []*store.Node{bastion, dc, access,
		ssh("core1", "dc", "core-sw1", 2224),
		ssh("core2", "dc", "zg-dc1-core-switch-02-rack-A14-row-3", 2222),
	}
	for i := 1; i <= 30; i++ {
		nodes = append(nodes, ssh(fmt.Sprintf("acc%02d", i), "access", fmt.Sprintf("acc-sw%02d", i), 2222))
	}
	labG := &store.Node{ID: "labg", IsGroup: true, Expanded: true, Name: "Lab"}
	tel := &store.Node{ID: "tel", ParentID: "labg", Name: "old-router (telnet)", Session: &store.Session{Protocol: store.ProtoTelnet, Host: "127.0.0.1", Port: 2323, CredentialID: "cred-lab",
		Expect: []store.ExpectRule{
			{ID: "e1", Active: true, Expect: "[Uu]sername:\\s*$", Send: "<USER>", Return: true, OnFail: "stop"},
			{ID: "e2", Active: true, Expect: "[Pp]assword:\\s*$", Send: "<PASS>", Return: true, OnFail: "stop"},
		}}}
	own := &store.Node{ID: "own", ParentID: "labg", Name: "r1-own-login", Session: &store.Session{Protocol: store.ProtoSSH, Host: "127.0.0.1", Port: 2222, User: "admin", Auth: store.AuthPassword, Algorithms: store.AlgoSecure,
		Clusters: []string{"LAB"}, TermBackground: "#0f2a1a",
		Macros: []store.Macro{{Name: "Interfaces and BGP", Text: "show ip interface brief\nshow ip bgp summary", Return: true}}}}
	// New in 0.7: telnet through the bastion, with the same login automation.
	telJump := &store.Node{ID: "teljump", ParentID: "labg", Name: "old-router via bastion (telnet)", Session: &store.Session{Protocol: store.ProtoTelnet, Host: "127.0.0.1", Port: 2323, CredentialID: "cred-lab", JumpVia: "bastion",
		Clusters: []string{"LAB"}, Expect: tel.Session.Expect}}
	tel.Session.Clusters = []string{"LAB"}
	nodes = append(nodes, labG, tel, telJump, own)
	secrets := map[string]string{
		store.SecretCredPassword("cred-lab"): "cisco123",
		store.SecretCredPassword("cred-ad"):  "Ad-Pass-1",
		store.SecretPassword("own"):          "cisco123",
	}
	f, err := exchange.Build("YACM demo", nodes, []*store.Credential{lab, ad}, "Demo", secrets,
		exchange.ExportOptions{IncludeSecrets: true, Password: "demo-export-pw"}, nil, nil)
	if err != nil {
		panic(err)
	}
	data, _ := f.Marshal()
	if err := os.WriteFile(os.Args[1], data, 0o600); err != nil {
		panic(err)
	}
}
