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

// SecureCRT credential sets (Config/Credentials/<title>.ini) become
// credential profiles, and sessions that name one by "Credential Title" use
// the profile instead of their own (stale) login.
func TestSecureCRTCredentialSets(t *testing.T) {
	root := t.TempDir()
	const pp = "scrt-pass"
	files := map[string]string{
		// As SecureCRT writes it: the title is the file name, the member
		// "Credential Title" inside is empty.
		"Credentials/A1-admin.ini":  "S:\"Username\"=admin\nS:\"Password V2\"=" + scrtEncrypt(t, "a1-pw", pp) + "\nS:\"Credential Title\"=\nD:\"Session Password Saved\"=00000001\n",
		"Credentials/Key-admin.ini": "S:\"Username\"=ops\nS:\"Identity Filename V2\"=/home/ops/.ssh/id_ed25519::rawkey\n",
		"Credentials/Unused.ini":    "S:\"Username\"=nobody\n",
		"Sessions/Lab/APIC1.ini":    "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.12.0.36\nS:\"Username\"=old\nS:\"Password V2\"=" + scrtEncrypt(t, "old-pw", pp) + "\nS:\"Credential Title\"=A1-admin\nS:\"Firewall Name\"=None\n",
		"Sessions/Lab/Gone.ini":     "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.12.0.37\nS:\"Username\"=own\nS:\"Credential Title\"=Removed\n",
		"Sessions/Bastion.ini":      "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=bastion.example.net\nS:\"Credential Title\"=key-admin\n",
		"Sessions/Lab/Inner.ini":    "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.12.0.38\nS:\"Credential Title\"=A1-admin\nS:\"Firewall Name\"=Session:Bastion\n",
	}
	for f, text := range files {
		p := filepath.Join(root, "Config", filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, by := run(t, root, KindSecureCRT, Options{Password: pp})
	if res.Report.Profiles != 3 || len(res.Credentials) != 3 {
		t.Fatalf("profiles %d %+v", res.Report.Profiles, res.Credentials)
	}
	cred := map[string]*store.Credential{}
	for _, c := range res.Credentials {
		cred[c.Name] = c
	}
	a1, key := cred["A1-admin"], cred["Key-admin"]
	if a1 == nil || a1.User != "admin" || a1.Auth != store.AuthPassword || res.Secrets[store.SecretCredPassword(a1.ID)] != "a1-pw" {
		t.Fatalf("A1-admin %+v", a1)
	}
	if key == nil || key.User != "ops" || key.Auth != store.AuthPublicKey || key.KeyPath != "/home/ops/.ssh/id_ed25519" {
		t.Fatalf("Key-admin %+v", key)
	}
	apic := by["APIC1"]
	if ss := apic.Session; ss.CredentialID != a1.ID || ss.User != "" {
		t.Fatalf("APIC1 %+v", ss)
	}
	if _, ok := res.Secrets[store.SecretPassword(apic.ID)]; ok {
		t.Fatal("the session's own password was kept next to its credential set")
	}
	if ss := by["Gone"].Session; ss.CredentialID != "" || ss.User != "own" {
		t.Fatalf("Gone %+v", ss)
	}
	if !strings.Contains(strings.Join(res.Report.Warnings, "\n"), `credential set "Removed" was not found`) {
		t.Fatalf("warnings %v", res.Report.Warnings)
	}
	inner := by["Inner"].Session
	if inner.CredentialID != a1.ID || len(inner.Jump) != 1 || inner.Jump[0].CredentialID != key.ID || inner.Jump[0].User != "" {
		t.Fatalf("Inner %+v", inner)
	}
}

// Logon actions that send the credential set's user name and password
// become <USER> and <PASS>.
func TestSecureCRTCredentialSetLogon(t *testing.T) {
	root := t.TempDir()
	const pp = "scrt-pass"
	var b strings.Builder
	b.WriteString("S:\"Protocol Name\"=Telnet\nS:\"Hostname\"=10.1.1.1\nS:\"Credential Title\"=tacacs\nD:\"Use Login Script\"=00000001\nZ:\"Login Script V4\"=00000002\n")
	for _, l := range []string{"0\x1fUsername:\x1fnetops\x1f\x1f", "0\x1fPassword:\x1fnet-pw\x1f\x1f"} {
		b.WriteString(" " + scrtEncrypt(t, l, pp) + "\n")
	}
	files := map[string]string{
		"Credentials/tacacs.ini": "S:\"Username\"=netops\nS:\"Password V2\"=" + scrtEncrypt(t, "net-pw", pp) + "\n",
		"Sessions/r1.ini":        b.String(),
	}
	for f, text := range files {
		p := filepath.Join(root, "Config", filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o600)
	}
	_, by := run(t, root, KindSecureCRT, Options{Password: pp})
	ss := by["r1"].Session
	if ss.CredentialID == "" || len(ss.Expect) != 2 || ss.Expect[0].Send != "<USER>" || ss.Expect[1].Send != "<PASS>" {
		t.Fatalf("r1 %+v", ss)
	}
}

// The XML export keeps credential sets under the key "Credentials".
func TestSecureCRTCredentialSetsXML(t *testing.T) {
	xmlText := `<?xml version="1.0" encoding="UTF-8"?>
<VanDyke version="3.0">
  <key name="Credentials">
    <key name="lab"><string name="Username">labuser</string></key>
  </key>
  <key name="Sessions">
    <key name="S1"><string name="Protocol Name">SSH2</string><string name="Hostname">10.0.0.9</string><string name="Credential Title">lab</string></key>
  </key>
</VanDyke>`
	p := filepath.Join(t.TempDir(), "export.xml")
	os.WriteFile(p, []byte(xmlText), 0o600)
	res, by := run(t, p, KindSecureCRTXML, Options{})
	if len(res.Credentials) != 1 || res.Credentials[0].User != "labuser" || by["S1"].Session.CredentialID != res.Credentials[0].ID {
		t.Fatalf("credentials %+v, session %+v", res.Credentials, by["S1"].Session)
	}
}

// Importing one folder inside Sessions still finds the credential sets next
// to Sessions; a set that is not among the files is taken from the
// profiles YACM already has.
func TestSecureCRTCredentialSetsSubfolder(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"Credentials/lab.ini":         "\ufeffS:\"Username\"=labuser\r\n",
		"Sessions/Site A/r1.ini":      "\ufeffS:\"Protocol Name\"=SSH2\r\nS:\"Hostname\"=10.0.0.1\r\nS:\"Credential Title\"=lab\r\n",
		"Sessions/Site A/r2.ini":      "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.0.0.2\nS:\"Credential Title\"=Old Admin\n",
		"Sessions/Site A/Default.ini": "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=\n",
	}
	for f, text := range files {
		p := filepath.Join(root, "Config", filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o600)
	}
	res, by := run(t, filepath.Join(root, "Config", "Sessions", "Site A"), KindSecureCRT, Options{Profiles: map[string]string{"old admin": "existing-id"}})
	if len(res.Credentials) != 1 || res.Credentials[0].User != "labuser" || by["r1"].Session.CredentialID != res.Credentials[0].ID {
		t.Fatalf("credentials %+v, r1 %+v", res.Credentials, by["r1"].Session)
	}
	if by["r2"].Session.CredentialID != "existing-id" {
		t.Fatalf("r2 %+v", by["r2"].Session)
	}
	// A session named Default in a folder is a real one, not the template.
	var skipped []string
	for _, s := range res.Report.Skipped {
		skipped = append(skipped, s.Name)
	}
	if strings.Join(skipped, ",") != "Default" {
		t.Fatalf("skipped %v", skipped)
	}
}

// Sessions that cannot be imported are named with their folder.
func TestSecureCRTSkippedWithFolder(t *testing.T) {
	root := t.TempDir()
	for f, text := range map[string]string{
		"Config/Sessions/Default.ini":       "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=\n",
		"Config/Sessions/Marko/Default.ini": "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=\n",
		"Config/Sessions/Marko/r1.ini":      "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.0.0.1\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o600)
	}
	res, _ := run(t, root, KindSecureCRT, Options{})
	if len(res.Report.Skipped) != 1 || res.Report.Skipped[0].Name != "Marko/Default" {
		t.Fatalf("skipped %+v", res.Report.Skipped)
	}
}
