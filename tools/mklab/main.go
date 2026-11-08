// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// mklab writes testlab.yacm: sessions for every node of the containerlab
// test lab (testlab/clab), to click through it in YACM. Import it with the
// export password lab-export-pw.
package main

import (
	"fmt"
	"os"

	"github.com/icavar/yacm/internal/exchange"
	"github.com/icavar/yacm/internal/store"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mklab testlab.yacm")
		os.Exit(2)
	}
	lab := &store.Credential{ID: "cred-lab", Name: "Lab (lab / lab)", User: "lab", Auth: store.AuthPassword}
	demo := &store.Credential{ID: "cred-demo", Name: "Demo IOS (admin / cisco123)", User: "admin", Auth: store.AuthPassword}
	session := func(id, parent, name, host string, mod func(*store.Session)) *store.Node {
		ss := &store.Session{Protocol: store.ProtoSSH, Host: host, Port: 22, Auth: store.AuthPassword,
			CredentialID: store.Inherit, JumpVia: store.Inherit, Algorithms: store.Inherit, KeepAlive: 30}
		if mod != nil {
			mod(ss)
		}
		return &store.Node{ID: id, ParentID: parent, Name: name, Session: ss}
	}
	group := func(id, parent, name, color string, d *store.GroupDefaults) *store.Node {
		return &store.Node{ID: id, ParentID: parent, IsGroup: true, Expanded: true, Name: name, Color: color, Defaults: d}
	}
	root := group("lab", "", "YACM test lab", "green", &store.GroupDefaults{CredentialID: "cred-lab", Algorithms: store.AlgoCompatible})
	nodes := []*store.Node{root}

	// Jump hosts: bastion1 is the only one reachable from this computer
	// (with bastion-locked); bastion2 sits behind it.
	jumps := group("jumps", "lab", "Jump hosts", "", nil)
	b1 := session("bastion1", "jumps", "bastion1", "172.30.0.11", func(s *store.Session) { s.JumpVia = "" })
	b2 := session("bastion2", "jumps", "bastion2 (via bastion1)", "10.0.1.2", func(s *store.Session) { s.JumpVia = "bastion1" })
	locked := session("locked", "jumps", "bastion-locked (GatewayPorts no)", "172.30.0.12", func(s *store.Session) { s.JumpVia = "" })
	nodes = append(nodes, jumps, b1, b2, locked)

	// The Arista cEOS fabric through bastion1 -> bastion2.
	fabric := group("fabric", "lab", "Fabric (Arista cEOS)", "blue", &store.GroupDefaults{JumpVia: "bastion2"})
	nodes = append(nodes, fabric)
	for _, d := range []struct{ id, ip string }{{"edge1", "10.255.0.1"}, {"edge2", "10.255.0.2"}, {"spine1", "10.255.0.11"}, {"spine2", "10.255.0.12"},
		{"leaf1", "10.255.0.21"}, {"leaf2", "10.255.0.22"}, {"leaf3", "10.255.0.23"}, {"leaf4", "10.255.0.24"}} {
		nodes = append(nodes, session(d.id, "fabric", d.id, d.ip, func(s *store.Session) { s.Clusters = []string{"FABRIC"} }))
	}
	edge1Telnet := &store.Node{ID: "edge1-telnet", ParentID: "fabric", Name: "edge1 (telnet through the jump hosts)", Session: &store.Session{
		Protocol: store.ProtoTelnet, Host: "10.255.0.1", Port: 23, CredentialID: "cred-lab", JumpVia: "bastion2",
		Expect: []store.ExpectRule{
			{ID: "u", Active: true, Expect: "(?i)(login|username):\\s*$", Send: "<USER>", Return: true, OnFail: "stop"},
			{ID: "p", Active: true, Expect: "[Pp]assword:\\s*$", Send: "<PASS>", Return: true, OnFail: "stop"},
		}}}
	nodes = append(nodes, edge1Telnet)

	// End points behind the fabric.
	servers := group("servers", "lab", "Servers", "", &store.GroupDefaults{JumpVia: "bastion2"})
	sftp := session("srv-sftp", "servers", "srv-sftp (SFTP; tunnels to srv-web)", "10.2.1.10", func(s *store.Session) {
		s.Tunnels = []store.Tunnel{
			{ID: "t-web", Type: "local", Name: "srv-web on 127.0.0.1:18080", Port: 18080, Host: "10.2.2.10", HostPort: 80},
			{ID: "t-socks", Type: "dynamic", Name: "SOCKS/HTTP proxy on 127.0.0.1:11080", Port: 11080},
		}
	})
	nodes = append(nodes, servers, sftp,
		session("srv-web", "servers", "srv-web (internal web server)", "10.2.2.10", nil),
		session("srv-kbd", "servers", "srv-kbd (keyboard-interactive)", "10.2.3.10", nil),
		session("srv-legacy", "servers", "srv-legacy (old algorithms only)", "10.2.4.10", func(s *store.Session) { s.Algorithms = store.AlgoLegacy }),
		session("ios1", "servers", "ios1 (YACM demo device, IOS-like, SCP only)", "10.2.5.10", func(s *store.Session) { s.CredentialID = "cred-demo" }),
	)

	// Behind the locked bastion: file servers through it cannot work.
	lk := group("behind-locked", "lab", "Behind bastion-locked", "orange", &store.GroupDefaults{JumpVia: "locked"})
	nodes = append(nodes, lk,
		session("edge2-locked", "behind-locked", "edge2 (via bastion-locked)", "10.255.0.2", nil),
		session("web-locked", "behind-locked", "srv-web (via bastion-locked)", "10.2.2.10", nil))

	// Outbound proxy: start the SOCKS tunnel of srv-sftp first.
	px := group("proxy", "lab", "Through the SOCKS proxy of srv-sftp", "purple",
		&store.GroupDefaults{Proxy: &store.Proxy{Type: "socks5", Host: "127.0.0.1", Port: 11080}})
	nodes = append(nodes, px, session("b2-proxy", "proxy", "bastion2 directly, through the proxy", "10.0.1.2", func(s *store.Session) { s.JumpVia = "" }))

	secrets := map[string]string{
		store.SecretCredPassword("cred-lab"):  "lab",
		store.SecretCredPassword("cred-demo"): "cisco123",
	}
	f, err := exchange.Build("YACM test lab", nodes, []*store.Credential{lab, demo}, "Lab", secrets,
		exchange.ExportOptions{IncludeSecrets: true, Password: "lab-export-pw"}, nil, nil)
	if err != nil {
		panic(err)
	}
	data, _ := f.Marshal()
	if err := os.WriteFile(os.Args[1], data, 0o600); err != nil {
		panic(err)
	}
}
