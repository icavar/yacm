// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package ppk

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// The test keys in testdata were made with PuTTYgen 0.81 (puttygen -t ...,
// then converted with --ppk-param version=2 / kdf=argon2i). Passphrase of the
// encrypted ones: "test-pass".

func pubOf(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".pub"))
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(data))
	return []byte(f[0] + " " + f[1])
}

func TestPuTTYgenKeys(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.ppk")
	if len(files) < 20 {
		t.Fatalf("only %d test keys", len(files))
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			data, _ := os.ReadFile(path)
			if !IsPPK(data) {
				t.Fatal("not detected as ppk")
			}
			kind := name[:strings.Index(name, "-v")]
			encrypted := strings.Contains(name, "enc") || strings.Contains(name, "argon2i") || strings.Contains(name, "default")
			if Encrypted(data) != encrypted {
				t.Fatalf("Encrypted() = %v", !encrypted)
			}
			pass := []byte(nil)
			if encrypted {
				if _, err := Parse(data, nil); !errors.Is(err, ErrEncrypted) {
					t.Fatalf("no passphrase: %v", err)
				}
				if _, err := Parse(data, []byte("wrong")); !errors.Is(err, ErrBadPassphrase) {
					t.Fatalf("wrong passphrase: %v", err)
				}
				pass = []byte("test-pass")
			}
			k, err := Parse(data, pass)
			if err != nil {
				t.Fatal(err)
			}
			s, err := ssh.NewSignerFromKey(k)
			if err != nil {
				t.Fatal(err)
			}
			got := bytes.TrimSpace(ssh.MarshalAuthorizedKey(s.PublicKey()))
			if want := pubOf(t, kind); !bytes.Equal(got, want) {
				t.Fatalf("public key\n got %s\nwant %s", got, want)
			}
			sig, err := s.Sign(nil, []byte("hello"))
			if err != nil || s.PublicKey().Verify([]byte("hello"), sig) != nil {
				t.Fatal("signature does not verify", err)
			}
			if Comment(data) != "yacm test "+kind {
				t.Fatalf("comment %q", Comment(data))
			}
		})
	}
}

func TestDamagedAndForeign(t *testing.T) {
	data, _ := os.ReadFile("testdata/ed25519-v3-plain.ppk")
	bad := bytes.Replace(data, []byte("Comment: yacm test"), []byte("Comment: yacm TEST"), 1)
	if _, err := Parse(bad, nil); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("changed comment accepted: %v", err)
	}
	crlf := bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	if _, err := Parse(crlf, nil); err != nil {
		t.Fatalf("CRLF file: %v", err)
	}
	openssh, _ := os.ReadFile("testdata/ed25519-openssh")
	if IsPPK(openssh) {
		t.Fatal("OpenSSH key detected as ppk")
	}
	if _, err := Parse([]byte("PuTTY-User-Key-File-3: ssh-ed25519\nEncryption: none\n"), nil); err == nil {
		t.Fatal("truncated file accepted")
	}
}
