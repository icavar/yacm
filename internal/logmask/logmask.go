// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package logmask hides secrets in session log files: passwords, keys and
// communities that devices print in their configuration (show running-config,
// show configuration) are replaced with ***** before a line is written.
//
// Only the log file is changed; the terminal shows the real output.
package logmask

import (
	"bytes"
	"regexp"
)

// Mask is what a secret is replaced with.
const Mask = "*****"

// maxLine limits how much of an unfinished line is held back.
const maxLine = 8 << 10

// rule replaces the capture groups of re with Mask.
type rule struct {
	re *regexp.Regexp
}

// The patterns cover Cisco IOS / IOS XE / IOS XR / NX-OS / ASA, Junos,
// Arista EOS and similar CLIs. Each one captures only the secret itself, so
// the rest of the line stays readable.
var rules = []rule{
	// enable secret 9 $9$..., username x privilege 15 secret 5 $1$...,
	// neighbor 192.0.2.1 password 7 0822..., ppp chap password x, ...
	{regexp.MustCompile(`(?i)\b(?:secret|password)\s+(?:(?:0|4|5|6|7|8|9|sha512|sha256|encrypted|hash|cleartext)\s+)?("[^"]*"|\S+)`)},
	// snmp-server community public RO
	{regexp.MustCompile(`(?i)\bsnmp-server\s+community\s+(\S+)`)},
	// snmp-server user u g auth md5 0x... priv aes-128 0x... (NX-OS, IOS v3)
	{regexp.MustCompile(`(?i)\bauth\s+(?:md5|sha\S*)\s+(\S+)`)},
	{regexp.MustCompile(`(?i)\bpriv\s+(?:(?:aes(?:[-\s]?\d+)?|3?des)\s+)?(\S+)`)},
	// tacacs-server key 7 ..., radius-server key ..., " key 7 ..." in a server block
	{regexp.MustCompile(`(?i)\b(?:tacacs-server|radius-server)\s+(?:host\s+\S+\s+)?key\s+(?:[0-9]\s+)?(\S+)`)},
	{regexp.MustCompile(`(?i)^\s*(?:server-)?key\s+[0-9]\s+(\S+)`)},
	// key-string 7 ..., authentication-key 7 ..., message-digest-key 1 md5 7 ...
	{regexp.MustCompile(`(?i)\bkey-string\s+(?:[0-9]\s+)?(\S+)`)},
	{regexp.MustCompile(`(?i)\bauthentication-key\s+(?:\d+\s+md5\s+)?(?:[0-9]\s+)?("[^"]*"|\S+)`)},
	{regexp.MustCompile(`(?i)\bmessage-digest-key\s+\d+\s+md5\s+(?:[0-9]\s+)?(\S+)`)},
	// crypto isakmp key 0 xyz address ..., pre-shared-key ..., ikev2 local-authentication pre-shared-key
	{regexp.MustCompile(`(?i)\bcrypto\s+isakmp\s+key\s+(?:[0-9]\s+)?(\S+)`)},
	{regexp.MustCompile(`(?i)\bpre-shared-key\s+(?:(?:local|remote|ascii-text|hexadecimal|[0-9])\s+)*("[^"]*"|\S+)`)},
	// wpa-psk ascii 0 xyz
	{regexp.MustCompile(`(?i)\bwpa-psk\s+(?:ascii|hex)\s+(?:[0-9]\s+)?(\S+)`)},
	// Junos: encrypted-password "$6$...", any "$9$..." value
	{regexp.MustCompile(`(?i)\bencrypted-password\s+("[^"]*"|\S+)`)},
	{regexp.MustCompile(`("\$9\$[^"]*")`)},
	// Linux-style crypt hashes anywhere: $1$, $5$, $6$, $8$, $9$, $y$
	{regexp.MustCompile(`(\$(?:1|5|6|8|9|y)\$[^\s"';]+)`)},
}

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// Line masks the secrets in one line of plain text (no line break).
func Line(s string) string {
	for _, r := range rules {
		idx := r.re.FindAllStringSubmatchIndex(s, -1)
		if idx == nil {
			continue
		}
		var b []byte
		last := 0
		for _, m := range idx {
			for g := 2; g+1 < len(m); g += 2 {
				if m[g] < 0 || m[g] < last {
					continue
				}
				val := s[m[g]:m[g+1]]
				if val == "" || val == Mask || val == `"`+Mask+`"` {
					continue
				}
				b = append(b, s[last:m[g]]...)
				if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
					b = append(b, '"')
					b = append(b, Mask...)
					b = append(b, '"')
				} else {
					b = append(b, Mask...)
				}
				last = m[g+1]
			}
		}
		if b != nil {
			s = string(append(b, s[last:]...))
		}
	}
	return s
}

// Masker masks a stream of plain text. Lines are passed on once complete;
// an unfinished line (a prompt, say) waits for its end or for Flush.
type Masker struct {
	partial []byte
	inKey   bool // inside a PEM private key block
}

// Write takes the next piece of text and returns what can be logged now.
func (m *Masker) Write(p []byte) []byte {
	m.partial = append(m.partial, p...)
	var out []byte
	for {
		i := bytes.IndexAny(m.partial, "\r\n")
		if i < 0 {
			break
		}
		j := i + 1
		if m.partial[i] == '\r' && j < len(m.partial) && m.partial[j] == '\n' {
			j++
		}
		out = append(out, m.line(string(m.partial[:i]))...)
		out = append(out, m.partial[i:j]...)
		m.partial = m.partial[j:]
	}
	if len(m.partial) > maxLine {
		out = append(out, m.line(string(m.partial))...)
		m.partial = m.partial[:0]
	}
	// Keep the buffer from growing: copy what is left to a fresh slice.
	if len(m.partial) > 0 && cap(m.partial) > 4*maxLine {
		m.partial = append([]byte(nil), m.partial...)
	}
	return out
}

// Flush returns the unfinished line (at disconnect).
func (m *Masker) Flush() []byte {
	if len(m.partial) == 0 {
		return nil
	}
	out := []byte(m.line(string(m.partial)))
	m.partial = m.partial[:0]
	return out
}

func (m *Masker) line(s string) string {
	if m.inKey {
		if pemEnd.MatchString(s) {
			m.inKey = false
			return s
		}
		if s == "" {
			return s
		}
		return Mask
	}
	if pemBegin.MatchString(s) && !pemEnd.MatchString(s) {
		m.inKey = true
		return s
	}
	return Line(s)
}
