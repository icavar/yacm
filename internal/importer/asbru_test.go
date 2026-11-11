// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"os"
	"strings"
	"testing"

	"github.com/yacm-app/yacm/internal/store"
)

func TestImportFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/asbru.yml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Asbru(data, "")
	if err != nil {
		t.Fatal(err)
	}
	r := res.Report
	if r.Groups != 2 || r.Sessions != 4 || len(r.Skipped) != 1 {
		t.Fatalf("report: %+v", r)
	}
	if r.Skipped[0].Method != "RDP (xfreerdp)" {
		t.Fatalf("skip: %+v", r.Skipped)
	}
	byName := map[string]*store.Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	core, edge := byName["Core"], byName["Edge"]
	if core == nil || edge == nil || core.ParentID != "" || edge.ParentID != core.ID {
		t.Fatalf("groups wrong: %+v %+v", core, edge)
	}
	r1 := byName["R1 core"]
	s := r1.Session
	if r1.ParentID != core.ID || s.Protocol != store.ProtoSSH || s.Host != "10.0.0.1" || s.Port != 22 || s.User != "admin" || s.Auth != store.AuthPassword {
		t.Fatalf("r1: %+v / %+v", r1, s)
	}
	if got := res.Secrets[store.SecretPassword(r1.ID)]; got != "Šifra-č123" {
		t.Fatalf("r1 password %q", got)
	}
	if len(s.Jump) != 1 || s.Jump[0].Host != "192.0.2.10" || s.Jump[0].Port != 2222 || s.Jump[0].User != "jumper" || s.Jump[0].Auth != store.AuthPassword {
		t.Fatalf("jump %+v", s.Jump)
	}
	if res.Secrets[store.SecretJumpPassword(r1.ID, 0)] != "jp-plain" {
		t.Fatal("jump password")
	}
	if len(s.Expect) != 3 || !s.Expect[0].Hidden || s.Expect[0].Send != "" || res.Secrets[store.SecretExpect(r1.ID, s.Expect[0].ID)] != "enable-secret" {
		t.Fatalf("expect 0: %+v", s.Expect)
	}
	if s.Expect[1].Send != "terminal length 0" || s.Expect[1].Timeout != 10 || s.Expect[1].OnFail != "stop" || s.Expect[2].Active {
		t.Fatalf("expect 1/2: %+v", s.Expect)
	}
	// Local commands from someone else's file always ask first.
	if len(s.Before) != 1 || !s.Before[0].Confirm || len(s.After) != 1 || !s.After[0].Confirm {
		t.Fatalf("local cmds %+v %+v", s.Before, s.After)
	}
	if len(s.Clusters) != 2 || len(s.Macros) != 1 || s.Macros[0].Text != "show ip bgp summary" || !s.Macros[0].Return {
		t.Fatalf("clusters/macros %+v %+v", s.Clusters, s.Macros)
	}
	r2 := byName["R2 core"].Session
	if r2.Auth != store.AuthPublicKey || r2.KeyPath != "/home/ivan/.ssh/id_ed25519" || res.Secrets[store.SecretPassphrase(byName["R2 core"].ID)] != "key-pass" {
		t.Fatalf("r2 %+v", r2)
	}
	tel := byName["Old switch"]
	if tel.ParentID != edge.ID || tel.Session.Protocol != store.ProtoTelnet || tel.Session.Port != 23 || res.Secrets[store.SecretPassword(tel.ID)] != "cisco" {
		t.Fatalf("telnet %+v", tel.Session)
	}
	ser := byName["Console"].Session
	if ser.Protocol != store.ProtoSerial || ser.Serial.Device != "/dev/ttyUSB0" || ser.Serial.Baud != 9600 {
		t.Fatalf("serial %+v", ser.Serial)
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "decrypt") {
			t.Fatalf("unexpected warning %s", w)
		}
	}
}

func TestResolveVars(t *testing.T) {
	out, secret := resolveVars("x <V:1> <GV:site> <PASS>", []string{"a", "b"}, map[int]bool{1: true}, map[string]string{"site": "zg"}, nil)
	if out != "x b zg <PASS>" || !secret {
		t.Fatalf("%q %v", out, secret)
	}
}
