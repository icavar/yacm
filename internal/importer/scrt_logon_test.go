// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

// scrtEncrypt makes a "03:" value the way SecureCRT does (inverse of
// secureCRTDecryptV2), for building test sessions.
func scrtEncrypt(t *testing.T, plain, passphrase string) string {
	t.Helper()
	salt := make([]byte, 16)
	rand.Read(salt)
	kdf := bcryptPBKDF([]byte(passphrase), salt, 16, 48)
	body := make([]byte, 4, 4+len(plain)+32+16)
	binary.LittleEndian.PutUint32(body, uint32(len(plain)))
	body = append(body, plain...)
	sum := sha256.Sum256([]byte(plain))
	body = append(body, sum[:]...)
	for len(body)%aes.BlockSize != 0 {
		body = append(body, 0)
	}
	block, _ := aes.NewCipher(kdf[:32])
	out := make([]byte, len(body))
	cipher.NewCBCEncrypter(block, kdf[32:]).CryptBlocks(out, body)
	return "03:" + hex.EncodeToString(append(salt, out...))
}

// The colleague's session (passwords are test values): telnet with three
// logon actions and "Send initial carriage return".
func TestSecureCRTLogonActionsSample(t *testing.T) {
	res, by := run(t, fx+"securecrt-logon/Sessions", KindSecureCRT, Options{})
	n := by["Router 10.1.1.1"]
	if n == nil {
		t.Fatalf("session missing: %+v", res.Report)
	}
	ss := n.Session
	if ss.Protocol != store.ProtoTelnet || ss.Host != "10.1.1.1" || !ss.InitialReturn {
		t.Fatalf("session %+v", ss)
	}
	if len(ss.Expect) != 3 {
		t.Fatalf("rules %+v", ss.Expect)
	}
	want := []struct{ expect, secret string }{{"word:", "test1"}, {"_1>", "ena"}, {"word:", "test2"}}
	for i, w := range want {
		r := ss.Expect[i]
		if r.Expect != w.expect || !r.Hidden || r.Send != "" || !r.Return || !r.Active {
			t.Errorf("rule %d: %+v", i, r)
		}
		if got := res.Secrets[store.SecretExpect(n.ID, r.ID)]; got != w.secret {
			t.Errorf("rule %d secret %q", i, got)
		}
	}
	if len(res.Report.Warnings) != 0 {
		t.Errorf("warnings %v", res.Report.Warnings)
	}
}

// Session password and user name become <PASS> and <USER>, an empty
// expect sends at once, special characters are matched literally, and a
// configuration passphrase is needed for "03:" values made with one.
func TestSecureCRTLogonActionsTokens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Sessions")
	os.MkdirAll(dir, 0o700)
	lines := []string{
		"0\x1flogin:\x1fnetops\x1f\x1f",
		"0\x1fPassword:\x1fpw-123\x1f\x1f",
		"0\x1f\x1fterminal length 0\x1f\x1f",
		"0\x1fR1(config)#\x1fend\x1f\x1f",
		"1\x1fEnable secret?\x1fsuper\x1f\x1f",
	}
	var b strings.Builder
	b.WriteString("S:\"Protocol Name\"=SSH2\r\nS:\"Hostname\"=10.9.9.9\r\nS:\"Username\"=netops\r\n")
	b.WriteString("S:\"Password V2\"=" + scrtEncrypt(t, "pw-123", "scrt-pass") + "\r\n")
	b.WriteString("D:\"Use Login Script\"=00000001\r\nD:\"Send Initial Carriage Return\"=00000000\r\n")
	b.WriteString("D:\"Use Script File\"=00000001\r\nS:\"Script Filename V2\"=C:\\scripts\\login.vbs\r\n")
	b.WriteString("Z:\"Login Script V4\"=00000005\r\n")
	for _, l := range lines {
		b.WriteString(" " + scrtEncrypt(t, l, "scrt-pass") + "\r\n")
	}
	os.WriteFile(filepath.Join(dir, "edge.ini"), []byte(b.String()), 0o600)

	res, by := run(t, dir, KindSecureCRT, Options{Password: "scrt-pass"})
	n := by["edge"]
	ss := n.Session
	if ss.InitialReturn || len(ss.Expect) != 5 {
		t.Fatalf("session %+v", ss)
	}
	exp := []struct{ expect, send string }{{"login:", "<USER>"}, {"Password:", "<PASS>"}, {"", "terminal length 0"}, {`R1\(config\)#`, "end"}, {`Enable secret\?`, ""}}
	for i, e := range exp {
		if r := ss.Expect[i]; r.Expect != e.expect || r.Send != e.send {
			t.Errorf("rule %d: %+v", i, r)
		}
	}
	if !ss.Expect[4].Hidden || res.Secrets[store.SecretExpect(n.ID, ss.Expect[4].ID)] != "super" {
		t.Errorf("hidden rule %+v", ss.Expect[4])
	}
	if !strings.Contains(strings.Join(res.Report.Warnings, "\n"), "login.vbs") {
		t.Errorf("logon script not reported: %v", res.Report.Warnings)
	}

	// Without the passphrase nothing can be read and the report says so.
	res2, by2 := run(t, dir, KindSecureCRT, Options{})
	if len(by2["edge"].Session.Expect) != 0 || !strings.Contains(strings.Join(res2.Report.Warnings, "\n"), "5 logon action(s) could not be read") {
		t.Fatalf("without passphrase: %+v %v", by2["edge"].Session.Expect, res2.Report.Warnings)
	}
}
