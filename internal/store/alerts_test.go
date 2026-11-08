// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

func TestAlertRulesAddedOnceToOldRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	old := `{"version":2,"settings":{"fontSize":14,"scrollback":1000,"theme":"dark","highlight":true,
	  "highlightRules":[{"id":"mine","enabled":true,"pattern":"BOOM","color":"red"}],"snippets":[]},"nodes":[]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rules := s.Settings().HighlightRules
	if len(rules) != len(DefaultAlertRules())+1 || rules[len(rules)-1].ID != "mine" || !rules[0].Alert {
		t.Fatalf("alert rules not put in front: %+v", rules)
	}
	// Removing one later sticks.
	st := s.Settings()
	st.HighlightRules = st.HighlightRules[1:]
	if err := s.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(path)
	if got := len(s2.Settings().HighlightRules); got != len(DefaultAlertRules()) {
		t.Fatalf("a removed alert rule came back: %d rules", got)
	}
}

func TestAlertRulePatternsMatch(t *testing.T) {
	lines := map[string]string{
		"al-neighbor":   "%BGP-5-ADJCHANGE: neighbor 10.255.0.2 Down BGP Notification sent",
		"al-errdisable": "%PM-4-ERR_DISABLE: bpduguard error detected on Gi1/0/5, putting Gi1/0/5 in err-disable state",
		"al-reload":     "%SYS-5-RELOAD: Reload requested by admin on console.",
	}
	for _, r := range DefaultAlertRules() {
		re := regexp.MustCompile(r.Pattern)
		if !re.MatchString(lines[r.ID]) {
			t.Errorf("%s does not match %q", r.ID, lines[r.ID])
		}
		if re.MatchString("%LINEPROTO-5-UPDOWN: Line protocol on Interface Gi1/0/24, changed state to up") {
			t.Errorf("%s matches an up message", r.ID)
		}
	}
}

func TestNoLinkDownAlertByDefault(t *testing.T) {
	for _, r := range DefaultHighlightRules() {
		if r.Alert && regexp.MustCompile(r.Pattern).MatchString("%LINEPROTO-5-UPDOWN: Line protocol on Interface Gi1/0/24, changed state to down") {
			t.Fatalf("rule %s alerts on a link going down", r.ID)
		}
	}
	if !DefaultSettings().LinkAlertQuiet {
		t.Fatal("new settings would quieten the link rule again")
	}
}

func TestOldLinkAlertBecomesAColourRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	old := `{"version":2,"settings":{"fontSize":14,"scrollback":1000,"theme":"dark","highlight":true,"alertRulesAdded":true,
	  "highlightRules":[
	    {"id":"al-link","enabled":true,"pattern":` + strconv.Quote(oldLinkAlertPattern) + `,"color":"bright-red","bold":true,"alert":true},
	    {"id":"al-neighbor","enabled":true,"pattern":"BGP","color":"bright-red","bold":true,"alert":true},
	    {"id":"mine","enabled":true,"pattern":"LINK-3-UPDOWN","color":"red","alert":true}],"snippets":[]},"nodes":[]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]HighlightRule{}
	for _, r := range s.Settings().HighlightRules {
		got[r.ID] = r
	}
	if r := got["al-link"]; r.Alert || !r.Enabled || r.Color != "bright-red" {
		t.Fatalf("al-link should stay as a colour rule without an alert: %+v", r)
	}
	if !got["al-neighbor"].Alert || !got["mine"].Alert {
		t.Fatalf("other alert rules changed: %+v", got)
	}
	// Once only: an alert switched on again later stays on.
	st := s.Settings()
	for i := range st.HighlightRules {
		if st.HighlightRules[i].ID == "al-link" {
			st.HighlightRules[i].Alert = true
		}
	}
	if err := s.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(path)
	for _, r := range s2.Settings().HighlightRules {
		if r.ID == "al-link" && !r.Alert {
			t.Fatal("the link alert switched on again was turned off once more")
		}
	}
}

func TestChangedLinkAlertIsKept(t *testing.T) {
	rules := quietLinkAlert([]HighlightRule{{ID: "al-link", Enabled: true, Pattern: "LINK-3-UPDOWN.*down", Alert: true}})
	if !rules[0].Alert {
		t.Fatal("a link rule the user changed lost its alert")
	}
}
