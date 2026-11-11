// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package exchange

import (
	"strings"
	"testing"

	"github.com/yacm-app/yacm/internal/store"
)

func tree() []*store.Node {
	return []*store.Node{
		{ID: "g1", IsGroup: true, Name: "Core"},
		{ID: "g2", IsGroup: true, Name: "Edge", ParentID: "g1"},
		{ID: "s1", Name: "R1", ParentID: "g1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1",
			Before: []store.LocalCommand{{Command: "ping -c1 10.0.0.1"}}}},
		{ID: "s2", Name: "SW1", ParentID: "g2", Session: &store.Session{Protocol: store.ProtoTelnet, Host: "10.0.1.1"}},
		{ID: "s3", Name: "Other", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.9.9.9"}},
	}
}

func TestCollectSubtree(t *testing.T) {
	nodes, scope, err := Collect(tree(), []string{"g2", "g1"})
	if err != nil {
		t.Fatal(err)
	}
	if scope != "Core" || len(nodes) != 4 {
		t.Fatalf("scope %q nodes %d", scope, len(nodes))
	}
	for _, n := range nodes {
		if n.ID == "g1" && n.ParentID != "" {
			t.Fatal("root parent not cleared")
		}
		if n.ID == "g2" && n.ParentID != "g1" {
			t.Fatal("nested group lost its parent")
		}
	}
	one, _, _ := Collect(tree(), []string{"s2"})
	if len(one) != 1 || one[0].ParentID != "" {
		t.Fatalf("%+v", one)
	}
}

func roundTrip(t *testing.T, pw string) *File {
	nodes, scope, _ := Collect(tree(), nil)
	secrets := map[string]string{store.SecretPassword("s1"): "pw1", store.SecretExpect("s1", "r1"): "enable"}
	f, err := Build("YACM test", nodes, nil, scope, secrets, ExportOptions{IncludeSecrets: true, Password: pw, IncludeKnownHosts: true, IncludeSettings: true},
		&store.Settings{FontSize: 16, Scrollback: 5000, Theme: "light"}, []string{"10.0.0.1 ssh-ed25519 AAAA"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := f.Marshal()
	if strings.Contains(string(data), "pw1") || strings.Contains(string(data), "enable") {
		t.Fatal("secret in clear text")
	}
	g, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestImportIntoEmpty(t *testing.T) {
	f := roundTrip(t, "export-password")
	if _, err := PlanImport(f, nil, nil, ImportOptions{Target: "root", Password: "nope-wrong"}); err != ErrNeedPassword {
		t.Fatalf("wrong password: %v", err)
	}
	p, err := PlanImport(f, nil, nil, ImportOptions{Target: "root", Password: "export-password", AddKnownHosts: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Report.Groups != 2 || p.Report.Sessions != 3 || p.Report.Secrets != 2 || len(p.Insert) != 5 || p.GroupID != "" {
		t.Fatalf("%+v", p.Report)
	}
	for _, n := range p.Insert {
		if n.ID == "s1" && (!n.Session.Before[0].Confirm || n.ParentID != "g1") {
			t.Fatalf("s1 %+v", n)
		}
	}
	if p.Secrets[store.SecretPassword("s1")] != "pw1" || len(p.KnownHostLines()) != 1 {
		t.Fatal("secrets / host keys")
	}
}

func TestConflictModes(t *testing.T) {
	f := roundTrip(t, "export-password")
	existing := map[string]*store.Node{}
	for _, n := range tree() {
		existing[n.ID] = n
	}
	existing["s1"].ParentID = "elsewhere"

	// copy: everything new, under a new group, secrets follow new IDs
	p, _ := PlanImport(f, existing, nil, ImportOptions{Target: "group", GroupName: "Restore", OnConflict: "copy", Password: "export-password"})
	if p.GroupID == "" || len(p.Insert) != 6 || len(p.Update) != 0 {
		t.Fatalf("copy: %d inserts %d updates", len(p.Insert), len(p.Update))
	}
	var r1 *store.Node
	for _, n := range p.Insert {
		if n.Name == "R1" {
			r1 = n
		}
	}
	if r1.ID == "s1" || p.Secrets[store.SecretPassword(r1.ID)] != "pw1" {
		t.Fatal("copy did not remap secrets")
	}

	// replace: same IDs, existing position kept
	p, _ = PlanImport(f, existing, nil, ImportOptions{Target: "group", OnConflict: "replace", Password: "export-password"})
	if len(p.Update) != 5 || len(p.Insert) != 0 || p.Report.Replaced != 3 {
		t.Fatalf("replace: %+v ins %d", p.Report, len(p.Insert))
	}
	for _, n := range p.Update {
		if n.ID == "s1" && n.ParentID != "elsewhere" {
			t.Fatal("replace moved the node")
		}
	}
	if len(p.ClearSecrets) != 1 {
		t.Fatalf("clear %v", p.ClearSecrets)
	}

	// skip: nothing changes, secrets untouched
	p, _ = PlanImport(f, existing, nil, ImportOptions{Target: "root", OnConflict: "skip", Password: "export-password"})
	if len(p.Insert) != 0 || len(p.Update) != 0 || p.Report.SkippedExisting != 3 || len(p.Secrets) != 0 {
		t.Fatalf("skip: %+v", p.Report)
	}
}

func TestRejectsForeignAndFutureFiles(t *testing.T) {
	if _, err := Parse([]byte(`{"environments":{}}`)); err != ErrNotYACM {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`{"format":"yacm-sessions","version":99,"nodes":[]}`)); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatal(err)
	}
}

func TestNoPasswordSkipsSecrets(t *testing.T) {
	f := roundTrip(t, "export-password")
	p, err := PlanImport(f, nil, nil, ImportOptions{Target: "root"})
	if err != nil || len(p.Secrets) != 0 || len(p.Report.Warnings) == 0 {
		t.Fatalf("%v %+v", err, p.Report)
	}
}

func TestProfilesInheritanceAndJumpsSurviveExport(t *testing.T) {
	cred := &store.Credential{ID: "cred-ad", Name: "AD", User: "ivan", Auth: store.AuthPassword}
	g := &store.Node{ID: "g", IsGroup: true, Name: "DC", Color: "red", Defaults: &store.GroupDefaults{CredentialID: "cred-ad", JumpVia: "bastion"}}
	bastion := &store.Node{ID: "bastion", Name: "bastion", Session: &store.Session{Protocol: store.ProtoSSH, Host: "192.0.2.1", Port: 22, CredentialID: "cred-ad"}}
	sw := &store.Node{ID: "sw", ParentID: "g", Name: "sw1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22, CredentialID: store.Inherit, JumpVia: store.Inherit}}
	all := []*store.Node{g, bastion, sw}

	// Exporting only sw1 copies what it inherits from the group.
	nodes, scope, err := Collect(all, []string{"sw"})
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Session.CredentialID != "cred-ad" || nodes[0].Session.JumpVia != "bastion" || nodes[0].Color != "red" {
		t.Fatalf("materialized: %+v color %q", nodes[0].Session, nodes[0].Color)
	}
	creds := ReferencedCredentials(nodes, []*store.Credential{cred, {ID: "unused", Name: "x"}})
	if len(creds) != 1 || creds[0].ID != "cred-ad" {
		t.Fatalf("referenced credentials = %+v", creds)
	}
	prefixes := SecretPrefixes(nodes, creds)
	f, err := Build("YACM test", nodes, creds, scope, map[string]string{store.SecretCredPassword("cred-ad"): "s3cret"}, ExportOptions{IncludeSecrets: true, Password: "export-pw-123"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 2 || f.Version != 2 {
		t.Fatalf("prefixes %v version %d", prefixes, f.Version)
	}
	data, _ := f.Marshal()
	f2, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	// On an empty computer: the profile and its password arrive, the jump
	// host session is not in the file, so the reference is dropped.
	p, err := PlanImport(f2, nil, nil, ImportOptions{Target: "root", Password: "export-pw-123"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Credentials) != 1 || p.Secrets[store.SecretCredPassword("cred-ad")] != "s3cret" || p.Report.Profiles != 1 {
		t.Fatalf("credentials not imported: %+v %v", p.Credentials, p.Secrets)
	}
	if p.Insert[0].Session.JumpVia != "" || len(p.Report.Warnings) == 0 {
		t.Fatal("missing jump host must be cleared with a warning")
	}

	// Full export into a tree that already has the same IDs, "copy" mode:
	// the copied session must jump through the copied bastion.
	nodes, scope, _ = Collect(all, nil)
	f, _ = Build("YACM test", nodes, []*store.Credential{cred}, scope, nil, ExportOptions{}, nil, nil)
	existing := map[string]*store.Node{"g": g, "bastion": bastion, "sw": sw}
	p, err = PlanImport(f, existing, map[string]*store.Credential{"cred-ad": cred}, ImportOptions{Target: "root", OnConflict: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	var newBastion, newGroup string
	for _, n := range p.Insert {
		if n.Name == "bastion" {
			newBastion = n.ID
		}
		if n.Name == "DC" {
			newGroup = n.ID
		}
	}
	for _, n := range p.Insert {
		if n.Name == "DC" && n.Defaults.JumpVia != newBastion {
			t.Fatalf("group default jump host not remapped: %s != %s", n.Defaults.JumpVia, newBastion)
		}
		if n.Name == "sw1" && n.ParentID != newGroup {
			t.Fatal("copied session must be inside the copied group")
		}
	}
	if len(p.Credentials) != 0 {
		t.Fatal("an existing profile is reused, not duplicated, unless replacing")
	}
}

// A file cannot borrow your stored secrets or profiles through crafted IDs
// or references, and cannot make Windows open a file on another computer.
func TestImportFromSomeoneElse(t *testing.T) {
	existing := map[string]*store.Node{
		"victim": {ID: "victim", Name: "Prod", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22}},
	}
	creds := map[string]*store.Credential{"ad": {ID: "ad", Name: "AD account", User: "ivan"}}
	data := `{"format":"` + Format + `","version":1,"nodes":[
	  {"id":"victim/jump/0","name":"alias","session":{"protocol":"ssh","host":"203.0.113.9","port":22}},
	  {"id":"n2","name":"borrow","session":{"protocol":"ssh","host":"203.0.113.9","port":22,"credentialId":"ad",
	    "proxy":{"type":"http","host":"203.0.113.9","port":3128,"credentialId":"ad"},
	    "keyPath":"\\\\203.0.113.9\\s\\k","auth":"publickey","before":[{"command":"calc"}]}},
	  {"id":"n3","name":"serial","session":{"protocol":"serial","serial":{"device":"\\\\.\\COM10","baud":9600}}}
	],"credentials":[{"id":"bad/id","name":"x"}]}`
	f, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Credentials) != 0 {
		t.Fatal("a profile with a bad ID was kept")
	}
	p, err := PlanImport(f, existing, creds, ImportOptions{Target: "root", OnConflict: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*store.Node{}
	for _, n := range p.Insert {
		byName[n.Name] = n
	}
	if a := byName["alias"]; a == nil || !store.ValidID(a.ID) || strings.Contains(a.ID, "victim") {
		t.Fatalf("crafted ID kept: %+v", a)
	}
	b := byName["borrow"].Session
	if b.CredentialID != "" || b.Proxy != nil {
		t.Fatalf("reference to a profile not in the file, or the file's proxy, kept: %q %+v", b.CredentialID, b.Proxy)
	}
	if b.KeyPath != "" || !b.Before[0].Confirm {
		t.Fatalf("network path or unconfirmed local command kept: %+v", b)
	}
	if d := byName["serial"].Session.Serial.Device; d != `\\.\COM10` {
		t.Fatalf("a local device path was dropped: %q", d)
	}
}

// A file cannot turn on agent forwarding, route your logins through its
// proxy or choose where device output is written on your computer.
func TestImportDropsForwardingProxyAndLogPaths(t *testing.T) {
	data := `{"format":"` + Format + `","version":1,"nodes":[
	  {"id":"g1","name":"Site","isGroup":true,"defaults":{"logName":"run.bat","proxy":{"type":"socks5","host":"203.0.113.9","port":1080}}},
	  {"id":"s1","parentId":"g1","name":"R1","session":{"protocol":"ssh","host":"203.0.113.9","port":22,
	    "agentForward":true,"logToFile":true,"logDir":"/tmp/startup","logName":"run.bat",
	    "proxy":{"type":"socks5","host":"203.0.113.9","port":1080}}}]}`
	f, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanImport(f, nil, nil, ImportOptions{Target: "root", OnConflict: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Insert) != 2 { // Site, R1
		t.Fatalf("inserted %d, skipped %+v", len(p.Insert), p.Report.Skipped)
	}
	for _, n := range p.Insert {
		if s := n.Session; s != nil && (s.AgentForward || s.Proxy != nil || s.LogDir != "" || s.LogName != "") {
			t.Fatalf("session kept: %+v", s)
		}
		if d := n.Defaults; d != nil && (d.Proxy != nil || d.LogName != "") {
			t.Fatalf("group kept: %+v", d)
		}
	}
	if w := strings.Join(p.Report.Warnings, "\n"); !strings.Contains(w, "agent forwarding") || !strings.Contains(w, "outbound proxy") {
		t.Fatalf("warnings %q", w)
	}
}

// Replacing a session with one that points elsewhere does not keep your
// saved password for the new address.
func TestReplaceElsewhereClearsSecrets(t *testing.T) {
	existing := map[string]*store.Node{
		"s1": {ID: "s1", Name: "R1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1"}},
		"s2": {ID: "s2", Name: "R2", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.2", Port: 22}},
	}
	data := `{"format":"` + Format + `","version":1,"nodes":[
	  {"id":"s1","name":"R1","session":{"protocol":"ssh","host":"203.0.113.9","port":22}},
	  {"id":"s2","name":"R2","session":{"protocol":"ssh","host":"10.0.0.2"}}]}`
	f, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanImport(f, existing, nil, ImportOptions{Target: "root", OnConflict: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ClearSecrets) != 1 || p.ClearSecrets[0] != store.SecretPrefix("s1") {
		t.Fatalf("clear = %v", p.ClearSecrets)
	}
}

// Trusted host keys from a file only cover the hosts of its sessions,
// written literally.
func TestImportedHostKeysStayWithTheirHosts(t *testing.T) {
	f := &File{
		Nodes: []*store.Node{{ID: "s1", Name: "R1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1",
			Jump: []store.JumpHost{{Host: "bastion.example.net", Port: 22}}}}},
		KnownHosts: []string{
			"10.0.0.1 ssh-ed25519 AAAA",
			"[10.0.0.1]:2222 ssh-ed25519 AAAA",
			"bastion.example.net ssh-ed25519 AAAA",
			"* ssh-ed25519 AAAA",
			"@cert-authority * ssh-ed25519 AAAA",
			"10.0.0.* ssh-ed25519 AAAA",
			"|1|abc=|def= ssh-ed25519 AAAA",
			"203.0.113.9 ssh-ed25519 AAAA",
			"10.0.0.1,203.0.113.9 ssh-ed25519 AAAA",
			// Forms the host key check cannot read (one would make it skip
			// the whole file), and revocations, which a file cannot add.
			"[10.0.0.1] ssh-ed25519 AAAA",
			"[10.0.0.1]x:22 ssh-ed25519 AAAA",
			"[10.0.0.1]:abc ssh-ed25519 AAAA",
			"@revoked 10.0.0.1 ssh-ed25519 AAAA",
		},
	}
	keep, dropped := sessionHostKeys(f)
	if len(keep) != 3 || dropped != 10 {
		t.Fatalf("keep %q, dropped %d", keep, dropped)
	}
}

// Replacing a session whose jump host moved drops your saved passwords too.
func TestReplaceWithOtherJumpClearsSecrets(t *testing.T) {
	existing := map[string]*store.Node{
		"s1": {ID: "s1", Name: "R1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Jump: []store.JumpHost{{Host: "bastion", Port: 22}}}},
	}
	data := `{"format":"` + Format + `","version":1,"nodes":[
	  {"id":"s1","name":"R1","session":{"protocol":"ssh","host":"10.0.0.1","jump":[{"host":"203.0.113.9","port":22}]}}]}`
	f, _ := Parse([]byte(data))
	p, err := PlanImport(f, existing, nil, ImportOptions{Target: "root", OnConflict: "replace"})
	if err != nil || len(p.ClearSecrets) != 1 {
		t.Fatalf("clear = %v (%v)", p.ClearSecrets, err)
	}
}

// A group in a file that names one of your profiles keeps the reference only
// when the file's profile is yours (same name and user).
func TestImportedGroupCannotBorrowAProfile(t *testing.T) {
	creds := map[string]*store.Credential{"ad": {ID: "ad", Name: "AD account", User: "ivan"}}
	data := `{"format":"` + Format + `","version":2,"nodes":[
	  {"id":"g1","isGroup":true,"name":"G","defaults":{"credentialId":"ad"}},
	  {"id":"s1","parentId":"g1","name":"x","session":{"protocol":"ssh","host":"203.0.113.9","credentialId":"inherit"}}],
	  "credentials":[{"id":"ad","name":"stub","user":"x"}]}`
	f, _ := Parse([]byte(data))
	p, err := PlanImport(f, nil, creds, ImportOptions{Target: "root", OnConflict: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Insert {
		if n.IsGroup && n.Defaults != nil && n.Defaults.CredentialID == "ad" {
			t.Fatal("the group kept a reference to your profile")
		}
	}
}
