// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

const fx = "testdata/foreign/"

func run(t *testing.T, path string, wantKind Kind, opts Options) (*Result, map[string]*store.Node) {
	t.Helper()
	k, err := Detect(path)
	if err != nil {
		t.Fatalf("detect %s: %v", path, err)
	}
	if k != wantKind {
		t.Fatalf("detect %s: got %s want %s", path, k, wantKind)
	}
	res, err := Run(k, path, "ROOT", opts)
	if err != nil {
		t.Fatalf("run %s: %v", path, err)
	}
	by := map[string]*store.Node{}
	for _, n := range res.Nodes {
		by[n.Name] = n
	}
	return res, by
}

func parentName(by map[string]*store.Node, n *store.Node) string {
	for _, p := range by {
		if p.ID == n.ParentID {
			return p.Name
		}
	}
	return n.ParentID
}

func TestSecureCRTCryptoVectors(t *testing.T) {
	// Published examples from HyperSine/how-does-SecureCRT-encrypt-password.
	if pw, err := secureCRTDecryptV2("03:7f59810d05b03f8e49b96e091dad49cb474c2e8435a5dbe53fc5d1e7aa228a8df8938cb01a7dd0c72cc361595ef5c2b675d8b2a64663776b95b065fec9b0fc36f168ffe3ae6fdedc3e1897389609536f", ""); err != nil || pw != "Hypersine" {
		t.Fatalf("03: %q %v", pw, err)
	}
	if pw, err := secureCRTDecryptV1("uc71bd1c86f3b804e42432f53247c50d9287f410c7e59166969acab69daa6eaadbe15c0c54c0e076e945a6d82f9e13df2"); err != nil || pw != "DoubleLabyrinth" {
		t.Fatalf("v1: %q %v", pw, err)
	}
}

func TestSecureCRTFolder(t *testing.T) {
	res, by := run(t, fx+"securecrt/Sessions", KindSecureCRT, Options{})
	r := res.Report
	if r.Sessions != 6 || len(r.Skipped) != 1 || r.SecretsFailed != 0 {
		t.Fatalf("report %+v", r)
	}
	if _, ok := by["Default"]; ok {
		t.Fatal("template session imported")
	}
	r1 := by["R1"]
	s := r1.Session
	if parentName(by, r1) != "Core" || s.Host != "10.0.0.1" || s.Port != 2222 || s.User != "admin" || s.Auth != store.AuthPassword {
		t.Fatalf("R1 %+v", s)
	}
	if res.Secrets[store.SecretPassword(r1.ID)] != "Šifra-123" || r1.Description != "Core router\nZagreb" {
		t.Fatalf("R1 secret/desc %q %q", res.Secrets[store.SecretPassword(r1.ID)], r1.Description)
	}
	if len(s.Jump) != 1 || s.Jump[0].Host != "192.0.2.10" || s.Jump[0].User != "jump" || s.Jump[0].Auth != store.AuthPassword ||
		res.Secrets[store.SecretJumpPassword(r1.ID, 0)] != "jumppw" {
		t.Fatalf("jump %+v", s.Jump)
	}
	if res.Secrets[store.SecretPassword(by["Legacy"].ID)] != "oldpw" {
		t.Fatal("v1 password")
	}
	if k := by["Key session"].Session; k.Auth != store.AuthPublicKey || k.KeyPath != `C:\keys\id_ed25519` {
		t.Fatalf("key %+v", k)
	}
	if c := by["Console"].Session; c.Protocol != store.ProtoSerial || c.Serial.Device != "COM3" || c.Serial.Baud != 9600 || c.Serial.Parity != "even" {
		t.Fatalf("serial %+v", c.Serial)
	}
	if tl := by["Telnet SW"]; tl.Session.Protocol != store.ProtoTelnet || tl.Session.Port != 23 || parentName(by, tl) != "Lab" {
		t.Fatalf("telnet %+v", tl.Session)
	}
}

func TestSecureCRTPassphrase(t *testing.T) {
	res, _ := run(t, fx+"securecrt-pass/Sessions", KindSecureCRT, Options{})
	if res.Report.SecretsFailed != 2 || len(res.Secrets) != 0 {
		t.Fatalf("without passphrase: %+v", res.Report)
	}
	res, by := run(t, fx+"securecrt-pass/Sessions", KindSecureCRT, Options{Password: "scrt-pass"})
	if res.Report.SecretsFailed != 0 || res.Secrets[store.SecretPassword(by["P1"].ID)] != "pw-two" || res.Secrets[store.SecretPassword(by["P2"].ID)] != "pw-three" {
		t.Fatalf("with passphrase: %+v %v", res.Report, res.Secrets)
	}
}

func TestSecureCRTXML(t *testing.T) {
	res, by := run(t, fx+"securecrt-export.xml", KindSecureCRTXML, Options{})
	if res.Report.Sessions != 2 || res.Secrets[store.SecretPassword(by["R1"].ID)] != "xml-pw" || by["R1"].Session.Port != 2222 {
		t.Fatalf("%+v %+v", res.Report, by["R1"].Session)
	}
	if c := by["Console"].Session.Serial; c.Device != "COM4" || c.Baud != 115200 {
		t.Fatalf("%+v", c)
	}
}

func TestXshell(t *testing.T) {
	opts := Options{WinUser: "ivan", WinSID: "S-1-5-21-1111-2222-3333-1001"}
	res, by := run(t, fx+"xshell/Sessions", KindXshell, opts)
	r := res.Report
	if r.Sessions != 5 || len(r.Skipped) != 1 || r.SecretsFailed != 0 {
		t.Fatalf("report %+v", r)
	}
	if res.Secrets[store.SecretPassword(by["R7"].ID)] != "x7-pass" || parentName(by, by["R7"]) != "Core" {
		t.Fatal("v7 password / folder")
	}
	if s := by["R6"].Session; s.Port != 2200 || s.User != "root" || res.Secrets[store.SecretPassword(by["R6"].ID)] != "x6-pass" {
		t.Fatalf("R6 %+v", s)
	}
	if by["Keyed"].Session.Auth != store.AuthAgent || by["Old switch"].Session.Protocol != store.ProtoTelnet {
		t.Fatal("keyed / telnet")
	}
	if c := by["Console"].Session.Serial; c.Device != "COM5" || c.Baud != 9600 {
		t.Fatalf("serial %+v", c)
	}
	// Another Windows account cannot decrypt them.
	res, _ = run(t, fx+"xshell/Sessions", KindXshell, Options{WinUser: "other", WinSID: "S-1-5-21-9"})
	if res.Report.SecretsFailed != 2 || len(res.Secrets) != 0 {
		t.Fatalf("foreign account: %+v", res.Report)
	}
}

func TestMobaXterm(t *testing.T) {
	res, by := run(t, fx+"mobaxterm.mxtsessions", KindMobaXterm, Options{})
	r := res.Report
	if r.Sessions != 4 || len(r.Skipped) != 1 || r.Skipped[0].Method != "RDP" {
		t.Fatalf("report %+v", r)
	}
	jb := by["jump box"]
	if jb.Session.KeyPath != `C:\keys\b.ppk` || jb.Description != "Café #1" || jb.ParentID != "ROOT" {
		t.Fatalf("jump box %+v %q", jb.Session, jb.Description)
	}
	r1 := by["zg-r1"]
	if parentName(by, r1) != "Core" || parentName(by, by["Core"]) != "Zagreb" || r1.Session.Port != 2222 || len(r1.Session.Jump) != 2 ||
		r1.Session.Jump[1].Host != "192.0.2.21" || r1.Session.Jump[1].Port != 2022 || r1.Session.Jump[1].User != "jump2" {
		t.Fatalf("zg-r1 %+v", r1.Session)
	}
	if c := by["zg-console"].Session.Serial; c.Device != "COM7" || c.Baud != 9600 || c.DataBits != 8 {
		t.Fatalf("serial %+v", c)
	}
	if by["zg-sw-telnet"].Session.Port != 23 {
		t.Fatal("telnet")
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "Passwords saved inside MobaXterm") {
		t.Fatal("password warning missing")
	}
}

func TestMRemoteNG(t *testing.T) {
	for _, tc := range []struct {
		dir  string
		opts Options
	}{
		{"mremoteng", Options{}},
		{"mremoteng-custom", Options{Password: "secret-mrng"}},
		{"mremoteng-full", Options{Password: "secret-mrng"}},
		{"mremoteng-legacy", Options{}},
	} {
		res, by := run(t, fx+tc.dir+"/confCons.xml", KindMRemoteNG, tc.opts)
		r := res.Report
		if r.Sessions != 3 || len(r.Skipped) != 1 || r.SecretsFailed != 0 {
			t.Fatalf("%s report %+v", tc.dir, r)
		}
		if res.Secrets[store.SecretPassword(by["zg-ssh"].ID)] != "mr-ssh" || res.Secrets[store.SecretPassword(by["zg-telnet"].ID)] != "mr-tel" {
			t.Fatalf("%s secrets %v", tc.dir, res.Secrets)
		}
		if parentName(by, by["zg-ssh"]) != "Zagreb" || by["loose"].Session.Port != 2200 {
			t.Fatalf("%s structure", tc.dir)
		}
	}
	res, _ := run(t, fx+"mremoteng-custom/confCons.xml", KindMRemoteNG, Options{})
	if res.Report.SecretsFailed != 2 || !strings.Contains(strings.Join(res.Report.Warnings, " "), "custom mRemoteNG password") {
		t.Fatalf("custom without password: %+v", res.Report)
	}
	if _, err := Run(KindMRemoteNG, fx+"mremoteng-full/confCons.xml", "", Options{}); err == nil {
		t.Fatal("fully encrypted file opened without password")
	}
}

func TestPuTTYReg(t *testing.T) {
	res, by := run(t, fx+"putty-sessions.reg", KindPuTTYReg, Options{})
	r := res.Report
	if r.Sessions != 4 || len(r.Skipped) != 1 {
		t.Fatalf("report %+v", r)
	}
	s := by["Core R1"].Session
	if s.User != "admin" || s.Host != "10.40.0.1" || s.KeyPath != `C:\Users\ivan\keys\r1.ppk` || !s.AgentForward ||
		len(s.Jump) != 1 || s.Jump[0].Host != "bastion.example.net" || s.Jump[0].User != "jump" {
		t.Fatalf("Core R1 %+v", s)
	}
	if c := by["Console COM4"].Session.Serial; c.Device != "COM4" || c.Baud != 115200 || c.StopBits != "2" || c.Parity != "even" {
		t.Fatalf("serial %+v", c)
	}
	if _, ok := by["Default Settings"]; ok {
		t.Fatal("template imported")
	}
	res, by = run(t, fx+"putty-unix/sessions", KindPuTTYDir, Options{})
	if res.Report.Sessions != 1 || by["lab box"].Session.Port != 2201 {
		t.Fatalf("unix %+v", res.Report)
	}
}

func TestSSHConfig(t *testing.T) {
	res, by := run(t, fx+"ssh/config", KindSSHConfig, Options{})
	if res.Report.Sessions != 5 { // "core-*" is a pattern, not a host
		t.Fatalf("report %+v names %v", res.Report, keys(by))
	}
	r1 := by["core-r1"].Session
	if r1.Host != "10.50.0.1" || r1.Port != 2222 || r1.User != "netops" || len(r1.Jump) != 1 || r1.Jump[0].Host != "192.0.2.50" || r1.Jump[0].User != "jump" || r1.Jump[0].KeyPath != "~/.ssh/id_bastion" {
		t.Fatalf("core-r1 %+v", r1)
	}
	if e := by["edge1"].Session; e.Host != "edge1" || e.User != "netops" {
		t.Fatalf("edge1 %+v", e)
	}
	if by["old-asa"].Session.Algorithms != store.AlgoLegacy || by["bastion"].Session.User != "jump" {
		t.Fatal("algorithms / first value wins")
	}
	if v := by["via-cmd"].Session; len(v.Jump) != 1 || v.Jump[0].Host != "192.0.2.50" || v.User != "defaultuser" {
		t.Fatalf("include + ProxyCommand %+v", v)
	}
}

func keys(m map[string]*store.Node) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDetectUnknown(t *testing.T) {
	if _, err := detectData("x.txt", []byte("hello world")); err != ErrUnknownFormat {
		t.Fatal(err)
	}
	if k, _ := detectData("asbru.yml", mustRead(t, "testdata/asbru.yml")); k != KindAsbru {
		t.Fatal(k)
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := readFile(filepath.Clean(p))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The SecureCRT folder chosen by hand: only Config/Sessions holds sessions.
func TestSecureCRTRootFolder(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "Config", "Sessions")
	if err := os.CopyFS(sessions, os.DirFS(fx+"securecrt/Sessions")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Global.ini":                           "S:\"Username\"=x\n",
		"ButtonBarV5.ini":                      "S:\"Username\"=x\n",
		filepath.Join("Credentials", "AD.ini"): "S:\"Username\"=x\n",
		// As SecureCRT writes them (the password left out).
		filepath.Join("Firewalls", "AS101.ini"):         "S:\"Firewall Address\"=127.0.0.1\nS:\"Proxy Command\"=\nD:\"Firewall Port\"=0000043a\nS:\"Firewall Type\"=SOCKSv5\nS:\"Firewall User\"=\nS:\"Proxy Prompt\"=\n",
		filepath.Join("Firewalls", "corp.ini"):          "S:\"Firewall Address\"=proxy.example.net\nD:\"Firewall Port\"=00000c38\nS:\"Firewall Type\"=HTTP\nS:\"Firewall User\"=netops\n",
		filepath.Join("Firewalls", "old.ini"):           "S:\"Firewall Address\"=10.9.9.9\nS:\"Firewall Type\"=SOCKSv4\n",
		filepath.Join("Sessions", "Fw", "isp01asr.ini"): "S:\"Protocol Name\"=Telnet\nS:\"Hostname\"=isp01asr\nD:\"Port\"=00000017\nS:\"Firewall Name\"=AS101\n",
		filepath.Join("Sessions", "Fw", "edge.ini"):     "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.1.1.1\nS:\"Firewall Name\"=corp\n",
		filepath.Join("Sessions", "Fw", "legacy.ini"):   "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.1.1.2\nS:\"Firewall Name\"=old\n",
		// SecureCRT on Linux keeps the serial settings under "Linux ...".
		filepath.Join("Sessions", "Serial-ttyUSB0 - 115200.ini"): "S:\"Protocol Name\"=Serial\nS:\"Linux Com Port\"=/dev/ttyUSB0\nD:\"Linux Baud Rate\"=0001c200\nD:\"Linux Data Bits\"=00000008\nD:\"Linux Parity\"=00000000\nD:\"Linux Stop Bits\"=00000000\n",
	}
	for f, text := range files {
		p := filepath.Join(root, "Config", f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, by := run(t, root, KindSecureCRT, Options{})
	if r := res.Report; r.Sessions != 10 || len(r.Skipped) != 1 {
		t.Fatalf("report %+v", r)
	}
	if p := by["isp01asr"].Session.Proxy; p == nil || p.Type != "socks5" || p.Host != "127.0.0.1" || p.Port != 1082 {
		t.Fatalf("AS101 proxy %+v", p)
	}
	if p := by["edge"].Session.Proxy; p == nil || p.Type != "http" || p.Host != "proxy.example.net" || p.Port != 3128 {
		t.Fatalf("corp proxy %+v", p)
	}
	if by["legacy"].Session.Proxy != nil {
		t.Fatal("a SOCKS4 firewall became a proxy")
	}
	var notes string
	for _, w := range res.Report.Warnings {
		notes += w + "\n"
	}
	for _, want := range []string{"on this computer", "its login was not taken over", "(SOCKSv4) is not taken over"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("no note %q in\n%s", want, notes)
		}
	}
	if c := by["Serial-ttyUSB0 - 115200"].Session; c.Serial == nil || c.Serial.Device != "/dev/ttyUSB0" || c.Serial.Baud != 115200 || c.Serial.DataBits != 8 || c.Serial.Parity != "none" {
		t.Fatalf("Linux serial %+v", c.Serial)
	}
	if _, ok := by["Config"]; ok {
		t.Fatal("the Config folder became a group")
	}
	if r1 := by["R1"]; parentName(by, r1) != "Core" || len(r1.Session.Jump) != 1 {
		t.Fatalf("R1 %+v", r1.Session)
	}
}
