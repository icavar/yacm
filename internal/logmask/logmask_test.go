// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package logmask

import (
	"strings"
	"testing"
)

func TestLine(t *testing.T) {
	cases := []struct{ in, want string }{
		// Cisco IOS / IOS XE
		{"enable secret 9 $9$abcdEFGH$xyz", "enable secret 9 *****"},
		{"enable password 7 0822455D0A16", "enable password 7 *****"},
		{"username admin privilege 15 secret 5 $1$mERr$hx5rVt7rPNoS4wqbXKX7m0", "username admin privilege 15 secret 5 *****"},
		{"username ops password 0 Cisco123!", "username ops password 0 *****"},
		{" neighbor 192.0.2.1 password 7 045802150C2E", " neighbor 192.0.2.1 password 7 *****"},
		{"snmp-server community s3cr3tRO RO 10", "snmp-server community ***** RO 10"},
		{"tacacs-server key 7 0822455D0A16", "tacacs-server key 7 *****"},
		{"radius-server host 10.1.1.1 key MyRadiusKey", "radius-server host 10.1.1.1 key *****"},
		{" key 7 01100F175804", " key 7 *****"},
		{"snmp-server user v3u grp v3 auth sha AuthPass priv aes 128 PrivPass", "snmp-server user v3u grp v3 auth sha ***** priv aes 128 *****"},
		{" key-string 7 13061E010803", " key-string 7 *****"},
		{" ip ospf message-digest-key 1 md5 7 0822455D0A16", " ip ospf message-digest-key 1 md5 7 *****"},
		{"ntp authentication-key 1 md5 104D000A0618 7", "ntp authentication-key 1 md5 ***** 7"},
		{"crypto isakmp key 0 Sup3rPSK address 198.51.100.1", "crypto isakmp key 0 ***** address 198.51.100.1"},
		{" pre-shared-key local MyLocalKey", " pre-shared-key local *****"},
		{" wpa-psk ascii 0 wifi-secret", " wpa-psk ascii 0 *****"},
		// NX-OS
		{"username admin password 5 $5$KIBGP$F3nhLKZGDYNEqUPKPXaU3a  role network-admin", "username admin password 5 *****  role network-admin"},
		{"snmp-server user nms network-admin auth md5 0x1234abcd5678ef90 priv aes-128 0x9876fedcba543210 localizedkey", "snmp-server user nms network-admin auth md5 ***** priv aes-128 ***** localizedkey"},
		// ASA
		{" ikev1 pre-shared-key Tunn3lK3y", " ikev1 pre-shared-key *****"},
		// Junos
		{`        encrypted-password "$6$abc$def"; ## SECRET-DATA`, `        encrypted-password "*****"; ## SECRET-DATA`},
		{`    authentication-key "$9$dkVYoZGi.fz6A"; ## SECRET-DATA`, `    authentication-key "*****"; ## SECRET-DATA`},
		{`        pre-shared-key ascii-text "$9$Hk.fz6A0OhrKMz"; ## SECRET-DATA`, `        pre-shared-key ascii-text "*****"; ## SECRET-DATA`},
		// Must stay as they are
		{"R1#show running-config | include password", "R1#show running-config | include password"},
		{"Password:", "Password:"},
		{"service password-encryption", "service password-encryption"},
		{"%SYS-5-CONFIG_I: Configured from console by admin", "%SYS-5-CONFIG_I: Configured from console by admin"},
		{"interface GigabitEthernet0/1", "interface GigabitEthernet0/1"},
		{"key chain EIGRP-KEYS", "key chain EIGRP-KEYS"},
	}
	for _, c := range cases {
		if got := Line(c.in); got != c.want {
			t.Errorf("Line(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

func TestMaskerStream(t *testing.T) {
	var m Masker
	var out strings.Builder
	// A line split across reads, CRLF endings, a prompt without a newline.
	for _, piece := range []string{"enable sec", "ret 5 $1$ab$cd\r\n", "hostname R1\r\nR1#"} {
		out.Write(m.Write([]byte(piece)))
	}
	if got := out.String(); got != "enable secret 5 *****\r\nhostname R1\r\n" {
		t.Fatalf("stream: %q", got)
	}
	if got := string(m.Flush()); got != "R1#" {
		t.Fatalf("flush: %q", got)
	}
}

func TestMaskerPrivateKey(t *testing.T) {
	var m Masker
	in := "crypto pki import\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\nabcdef\n-----END RSA PRIVATE KEY-----\nquit\n"
	got := string(m.Write([]byte(in)))
	want := "crypto pki import\n-----BEGIN RSA PRIVATE KEY-----\n*****\n*****\n-----END RSA PRIVATE KEY-----\nquit\n"
	if got != want {
		t.Fatalf("pem:\n%q\nwant\n%q", got, want)
	}
}

func TestMaskerLongLine(t *testing.T) {
	var m Masker
	out := m.Write([]byte(strings.Repeat("x", maxLine+10)))
	if len(out) != maxLine+10 || len(m.partial) != 0 {
		t.Fatalf("long line held back: out %d, partial %d", len(out), len(m.partial))
	}
}
