// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package sshconn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/yacm-app/yacm/internal/store"
)

// ---- test server ----------------------------------------------------------

type testServer struct {
	addr    string
	host    string
	port    int
	ln      net.Listener
	mu      sync.Mutex
	windows [][2]int // window-change requests (cols, rows)
	authed  []string // methods that succeeded
}

func newSigner(t *testing.T) ssh.Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startServer(t *testing.T, cfg *ssh.ServerConfig, forward bool) *testServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{ln: ln, addr: ln.Addr().String()}
	h, p, _ := net.SplitHostPort(ts.addr)
	ts.host = h
	ts.port, _ = strconv.Atoi(p)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go ts.serve(nc, cfg, forward)
		}
	}()
	return ts
}

func (ts *testServer) serve(nc net.Conn, cfg *ssh.ServerConfig, forward bool) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	if sc.Permissions != nil {
		ts.mu.Lock()
		ts.authed = append(ts.authed, sc.Permissions.Extensions["method"])
		ts.mu.Unlock()
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		switch nch.ChannelType() {
		case "session":
			ch, creqs, err := nch.Accept()
			if err != nil {
				continue
			}
			go ts.session(ch, creqs)
		case "direct-tcpip":
			if !forward {
				nch.Reject(ssh.Prohibited, "no forwarding")
				continue
			}
			var d struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}
			if err := ssh.Unmarshal(nch.ExtraData(), &d); err != nil {
				nch.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			up, err := net.Dial("tcp", net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
			if err != nil {
				nch.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, r, err := nch.Accept()
			if err != nil {
				up.Close()
				continue
			}
			go ssh.DiscardRequests(r)
			go func() { io.Copy(ch, up); ch.CloseWrite() }()
			go func() { io.Copy(up, ch); up.Close() }()
		default:
			nch.Reject(ssh.UnknownChannelType, "nope")
		}
	}
}

func (ts *testServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			req.Reply(true, nil)
		case "window-change":
			if len(req.Payload) >= 8 {
				cols := int(binary.BigEndian.Uint32(req.Payload[0:4]))
				rows := int(binary.BigEndian.Uint32(req.Payload[4:8]))
				ts.mu.Lock()
				ts.windows = append(ts.windows, [2]int{cols, rows})
				ts.mu.Unlock()
			}
		case "shell":
			req.Reply(true, nil)
			go func() {
				io.WriteString(ch, "Welcome\r\nR1#")
				buf := make([]byte, 1024)
				for {
					n, err := ch.Read(buf)
					if err != nil {
						return
					}
					line := string(buf[:n])
					if strings.Contains(line, "exit") {
						ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{0}))
						ch.Close()
						return
					}
					io.WriteString(ch, "echo:"+line)
				}
			}()
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func passwordServer(t *testing.T, hostKey ssh.Signer, pw string) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == "admin" && string(p) == pw {
				return &ssh.Permissions{Extensions: map[string]string{"method": "password"}}, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostKey)
	return cfg
}

// ---- prompter -------------------------------------------------------------

type fakePrompter struct {
	mu        sync.Mutex
	acceptKey bool
	hostKeys  []HostKeyInfo
	secrets   []SecretRequest
	answers   []string
	saveIt    bool
	questions [][]string
}

func (f *fakePrompter) HostKey(_ context.Context, info HostKeyInfo) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostKeys = append(f.hostKeys, info)
	return f.acceptKey, nil
}

func (f *fakePrompter) Secret(_ context.Context, req SecretRequest) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets = append(f.secrets, req)
	if len(f.answers) == 0 {
		return "", false, errors.New("cancelled")
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, f.saveIt, nil
}

func (f *fakePrompter) Questions(_ context.Context, _, _ string, qs []string, _ []bool) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.questions = append(f.questions, qs)
	out := make([]string, len(qs))
	for i := range qs {
		out[i] = "123456"
	}
	return out, nil
}

func readUntil(t *testing.T, c *Conn, want string) string {
	t.Helper()
	var buf bytes.Buffer
	tmp := make([]byte, 256)
	deadline := time.After(3 * time.Second)
	got := make(chan error, 1)
	go func() {
		for !strings.Contains(buf.String(), want) {
			n, err := c.Read(tmp)
			buf.Write(tmp[:n])
			if err != nil {
				got <- err
				return
			}
		}
		got <- nil
	}()
	select {
	case err := <-got:
		if err != nil && !strings.Contains(buf.String(), want) {
			t.Fatalf("read %q: %v", buf.String(), err)
		}
	case <-deadline:
		t.Fatalf("timeout waiting for %q, got %q", want, buf.String())
	}
	return buf.String()
}

// ---- tests ----------------------------------------------------------------

func TestPasswordTOFUAndResize(t *testing.T) {
	hk := newSigner(t)
	srv := startServer(t, passwordServer(t, hk, "s3cret"), false)
	dir := t.TempDir()
	kh := NewKnownHosts(filepath.Join(dir, "known_hosts"), false)
	pr := &fakePrompter{acceptKey: true}
	saved := map[string]string{}
	cfg := Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "admin", Auth: store.AuthPassword, PasswordKey: "pw"}},
		KnownHosts: kh, Prompter: pr,
		SaveSecret: func(n, v string) error { saved[n] = v; return nil },
	}
	pr.answers = []string{"wrong", "s3cret"}
	pr.saveIt = true
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "R1#")
	if len(pr.hostKeys) != 1 || pr.hostKeys[0].Changed || pr.hostKeys[0].Fingerprint != ssh.FingerprintSHA256(hk.PublicKey()) {
		t.Fatalf("host key prompt: %+v", pr.hostKeys)
	}
	if len(pr.secrets) != 2 || !pr.secrets[1].Retry {
		t.Fatalf("password prompts: %+v", pr.secrets)
	}
	// Both prompted values are queued for saving; the last one wins.
	if saved["pw"] != "s3cret" {
		t.Fatalf("saved %v", saved)
	}
	if !strings.Contains(c.Summary(), "mlkem768x25519-sha256") {
		t.Fatalf("expected post-quantum kex, got %q", c.Summary())
	}
	c.Write([]byte("show ver\r"))
	readUntil(t, c, "echo:show ver")
	c.Resize(132, 40)
	time.Sleep(100 * time.Millisecond)
	srv.mu.Lock()
	w := srv.windows
	srv.mu.Unlock()
	if len(w) != 1 || w[0] != [2]int{132, 40} {
		t.Fatalf("window changes %v", w)
	}
	c.Close()

	// Second connection: key is known, stored password used, no prompts.
	pr2 := &fakePrompter{}
	cfg.Prompter = pr2
	cfg.Hops[0].Password = "s3cret"
	c2, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, c2, "R1#")
	if len(pr2.hostKeys) != 0 || len(pr2.secrets) != 0 {
		t.Fatalf("unexpected prompts %+v %+v", pr2.hostKeys, pr2.secrets)
	}
	c2.Write([]byte("exit\r"))
	buf := make([]byte, 64)
	for {
		if _, err := c2.Read(buf); err != nil {
			break
		}
	}
	c2.Close()
}

func TestChangedHostKeyRejected(t *testing.T) {
	hk1, hk2 := newSigner(t), newSigner(t)
	srv := startServer(t, passwordServer(t, hk2, "x"), false)
	dir := t.TempDir()
	kh := NewKnownHosts(filepath.Join(dir, "known_hosts"), false)
	if err := kh.Trust(srv.addr, hk1.PublicKey(), false); err != nil {
		t.Fatal(err)
	}
	pr := &fakePrompter{acceptKey: false}
	_, err := Dial(context.Background(), Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "admin", Password: "x", Auth: store.AuthPassword}},
		KnownHosts: kh, Prompter: pr,
	})
	if !errors.Is(err, ErrHostKeyRejected) {
		t.Fatalf("expected rejection, got %v", err)
	}
	if len(pr.hostKeys) != 1 || !pr.hostKeys[0].Changed || len(pr.hostKeys[0].KnownKeys) != 1 {
		t.Fatalf("prompt %+v", pr.hostKeys)
	}
	// Accepting replaces the old entry.
	pr.acceptKey = true
	c, err := Dial(context.Background(), Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "admin", Password: "x", Auth: store.AuthPassword}},
		KnownHosts: kh, Prompter: pr,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	entries, _ := kh.List()
	if len(entries) != 1 || entries[0].Fingerprint != ssh.FingerprintSHA256(hk2.PublicKey()) {
		t.Fatalf("entries %+v", entries)
	}
}

func TestEncryptedKeyWithCertificateAndHostCA(t *testing.T) {
	userCA, hostCA := newSigner(t), newSigner(t)
	// Host certificate.
	hostKey := newSigner(t)
	hcert := &ssh.Certificate{Key: hostKey.PublicKey(), CertType: ssh.HostCert, KeyId: "r1", ValidPrincipals: []string{"127.0.0.1"}, ValidBefore: ssh.CertTimeInfinity, Serial: 7}
	if err := hcert.SignCert(rand.Reader, hostCA); err != nil {
		t.Fatal(err)
	}
	hostCertSigner, _ := ssh.NewCertSigner(hcert, hostKey)

	checker := &ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool {
		return bytes.Equal(k.Marshal(), userCA.PublicKey().Marshal())
	}}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if _, ok := k.(*ssh.Certificate); !ok {
			return nil, errors.New("certificate required")
		}
		p, err := checker.Authenticate(c, k)
		if err != nil {
			return nil, err
		}
		if p == nil {
			p = &ssh.Permissions{}
		}
		if p.Extensions == nil {
			p.Extensions = map[string]string{}
		}
		p.Extensions["method"] = "cert"
		return p, nil
	}}
	cfg.AddHostKey(hostCertSigner)
	srv := startServer(t, cfg, false)

	// User key, encrypted, plus certificate next to it.
	dir := t.TempDir()
	_, upriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, err := ssh.MarshalPrivateKeyWithPassphrase(upriv, "user@laptop", []byte("kp"))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	os.WriteFile(keyPath, pem.EncodeToMemory(blk), 0o600)
	usigner, _ := ssh.NewSignerFromKey(upriv)
	ucert := &ssh.Certificate{Key: usigner.PublicKey(), CertType: ssh.UserCert, KeyId: "ivan", ValidPrincipals: []string{"ivan"}, ValidBefore: ssh.CertTimeInfinity}
	if err := ucert.SignCert(rand.Reader, userCA); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(keyPath+"-cert.pub", ssh.MarshalAuthorizedKey(ucert), 0o600)

	khPath := filepath.Join(dir, "known_hosts")
	os.WriteFile(khPath, []byte(fmt.Sprintf("@cert-authority [*]:%d ", srv.port)+string(ssh.MarshalAuthorizedKey(hostCA.PublicKey()))), 0o600)
	kh := NewKnownHosts(khPath, false)
	pr := &fakePrompter{answers: []string{"bad", "kp"}}
	var statuses []string
	c, err := Dial(context.Background(), Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "ivan", Auth: store.AuthPublicKey, KeyPath: keyPath}},
		KnownHosts: kh, Prompter: pr,
		Status: func(s string) { statuses = append(statuses, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readUntil(t, c, "R1#")
	if len(pr.hostKeys) != 0 {
		t.Fatalf("host CA should make the key trusted, got prompt %+v", pr.hostKeys)
	}
	if len(pr.secrets) != 2 || pr.secrets[0].Kind != "passphrase" || !pr.secrets[1].Retry {
		t.Fatalf("passphrase prompts %+v", pr.secrets)
	}
	if !strings.Contains(strings.Join(statuses, "\n"), `key id "ivan"`) {
		t.Fatalf("certificate not offered: %v", statuses)
	}
}

func TestKeyboardInteractiveOnly(t *testing.T) {
	hk := newSigner(t)
	cfg := &ssh.ServerConfig{KeyboardInteractiveCallback: func(c ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		a, err := ch("", "", []string{"Password: "}, []bool{false})
		if err != nil || len(a) != 1 || a[0] != "kipw" {
			return nil, errors.New("bad")
		}
		a, err = ch("2FA", "Enter code", []string{"OTP: "}, []bool{true})
		if err != nil || a[0] != "123456" {
			return nil, errors.New("bad otp")
		}
		return &ssh.Permissions{Extensions: map[string]string{"method": "ki"}}, nil
	}}
	cfg.AddHostKey(hk)
	srv := startServer(t, cfg, false)
	pr := &fakePrompter{acceptKey: true}
	c, err := Dial(context.Background(), Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "admin", Password: "kipw", Auth: store.AuthPassword}},
		KnownHosts: NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false), Prompter: pr,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readUntil(t, c, "R1#")
	if len(pr.questions) != 1 || pr.questions[0][0] != "OTP: " {
		t.Fatalf("questions %v", pr.questions)
	}
}

func TestJumpHostChain(t *testing.T) {
	jumpKey, targetKey := newSigner(t), newSigner(t)
	jump := startServer(t, passwordServer(t, jumpKey, "jumppw"), true)
	target := startServer(t, passwordServer(t, targetKey, "targetpw"), false)
	pr := &fakePrompter{acceptKey: true}
	c, err := Dial(context.Background(), Config{
		Hops: []Hop{
			{Host: jump.host, Port: jump.port, User: "admin", Password: "jumppw", Auth: store.AuthPassword},
			{Host: target.host, Port: target.port, User: "admin", Password: "targetpw", Auth: store.AuthPassword},
		},
		KnownHosts: NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false), Prompter: pr,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readUntil(t, c, "R1#")
	if len(pr.hostKeys) != 2 || pr.hostKeys[1].Fingerprint != ssh.FingerprintSHA256(targetKey.PublicKey()) {
		t.Fatalf("expected two host key prompts, got %+v", pr.hostKeys)
	}
}

func TestAlgorithmProfiles(t *testing.T) {
	hk := newSigner(t)
	cfg := passwordServer(t, hk, "x")
	cfg.KeyExchanges = []string{ssh.InsecureKeyExchangeDH14SHA1}
	srv := startServer(t, cfg, false)
	kh := NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false)
	pr := &fakePrompter{acceptKey: true}
	base := Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "admin", Password: "x", Auth: store.AuthPassword}},
		KnownHosts: kh, Prompter: pr, Profile: store.AlgoSecure,
	}
	_, err := Dial(context.Background(), base)
	if err == nil || !strings.Contains(err.Error(), "Compatible") {
		t.Fatalf("secure profile should fail with hint, got %v", err)
	}
	base.Profile = store.AlgoCompatible
	c, err := Dial(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !strings.HasPrefix(c.Summary(), "diffie-hellman-group14-sha1") {
		t.Fatalf("summary %q", c.Summary())
	}
}

func TestPreferKnown(t *testing.T) {
	got := preferKnown([]string{ssh.KeyAlgoED25519, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoECDSA256}, []string{ssh.KeyAlgoRSA})
	if got[0] != ssh.KeyAlgoRSASHA512 {
		t.Fatalf("%v", got)
	}
}

func TestHashedKnownHosts(t *testing.T) {
	hk := newSigner(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "kh")
	salt := "c2FsdHNhbHRzYWx0c2FsdHNhbHQ="
	line := fmt.Sprintf("%s %s", hashWithSalt(salt, "[127.0.0.1]:2222"), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hk.PublicKey()))))
	os.WriteFile(p, []byte(line+"\n"), 0o600)
	kh := NewKnownHosts(p, false)
	types := kh.KnownKeyTypes("127.0.0.1:2222")
	if len(types) != 1 || types[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("types %v", types)
	}
}

func TestHostCertWithoutCAFallsBackToKey(t *testing.T) {
	hostCA, hostKey := newSigner(t), newSigner(t)
	hcert := &ssh.Certificate{Key: hostKey.PublicKey(), CertType: ssh.HostCert, ValidBefore: ssh.CertTimeInfinity}
	hcert.SignCert(rand.Reader, hostCA)
	cs, _ := ssh.NewCertSigner(hcert, hostKey)
	cfg := passwordServer(t, hostKey, "x")
	cfg.AddHostKey(cs)
	srv := startServer(t, cfg, false)
	pr := &fakePrompter{acceptKey: true}
	c, err := Dial(context.Background(), Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "admin", Password: "x", Auth: store.AuthPassword}},
		KnownHosts: NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false), Prompter: pr,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(pr.hostKeys) != 1 || pr.hostKeys[0].Fingerprint != ssh.FingerprintSHA256(hostKey.PublicKey()) {
		t.Fatalf("prompt %+v", pr.hostKeys)
	}
}

func TestKnownHostsExportImport(t *testing.T) {
	a, b := newSigner(t), newSigner(t)
	src := NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false)
	src.Trust("10.0.0.1:22", a.PublicKey(), false)
	src.Trust("10.0.0.2:2222", b.PublicKey(), false)
	lines := src.ExportLinesFor([]string{"10.0.0.2:2222"})
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "[10.0.0.2]:2222 ") {
		t.Fatalf("filtered export %v", lines)
	}
	dst := NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false)
	other := newSigner(t)
	dst.Trust("10.0.0.1:22", other.PublicKey(), false) // already trusted differently here
	n, err := dst.ImportLines(append(src.ExportLines(), "garbage line"))
	if err != nil || n != 1 {
		t.Fatalf("added %d, %v", n, err)
	}
	if types := dst.KnownKeyTypes("10.0.0.1:22"); len(types) != 1 {
		t.Fatal("existing trust was changed")
	}
	if n, _ := dst.ImportLines(src.ExportLines()); n != 0 {
		t.Fatal("duplicates added")
	}
}

// A PuTTY .ppk key (made with PuTTYgen, encrypted, format 3) logs in
// directly; a wrong passphrase is asked again like for OpenSSH keys.
func TestPuTTYKey(t *testing.T) {
	pubData, err := os.ReadFile("../ppk/testdata/ecdsa-256.pub")
	if err != nil {
		t.Fatal(err)
	}
	want, _, _, _, err := ssh.ParseAuthorizedKey(pubData)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), want.Marshal()) {
			return &ssh.Permissions{Extensions: map[string]string{"method": "ppk"}}, nil
		}
		return nil, errors.New("unknown key")
	}}
	cfg.AddHostKey(newSigner(t))
	srv := startServer(t, cfg, false)
	pr := &fakePrompter{acceptKey: true, answers: []string{"nope", "test-pass"}}
	c, err := Dial(context.Background(), Config{
		Hops:       []Hop{{Host: srv.host, Port: srv.port, User: "ivan", Auth: store.AuthPublicKey, KeyPath: "../ppk/testdata/ecdsa-256-v3-enc.ppk"}},
		KnownHosts: NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false), Prompter: pr,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readUntil(t, c, "R1#")
	if len(pr.secrets) != 2 || !pr.secrets[1].Retry {
		t.Fatalf("passphrase prompts %+v", pr.secrets)
	}
}

// Imported lines never trust wildcards, patterns, hashed names or a
// certificate authority.
func TestImportLinesRefusesWideEntries(t *testing.T) {
	key := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey())))
	dst := NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false)
	n, err := dst.ImportLines([]string{"* " + key, "@cert-authority * " + key, "10.0.0.* " + key, "!10.0.0.9,* " + key, "10.0.0.5 " + key})
	if err != nil || n != 1 {
		t.Fatalf("added %d, %v", n, err)
	}
	if types := dst.KnownKeyTypes("10.0.0.6:22"); len(types) != 0 {
		t.Fatal("a pattern was trusted")
	}
}

// "[host]:22" is the plain host: an import cannot add a second key for a
// host you trust that way. Revocations do not come along (they could not be
// removed in YACM).
func TestImportLinesNormalisesHosts(t *testing.T) {
	a, b := newSigner(t), newSigner(t)
	dst := NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false)
	dst.Trust("10.0.0.1:22", a.PublicKey(), false)
	keyB := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(b.PublicKey())))
	n, err := dst.ImportLines([]string{"[10.0.0.1]:22 " + keyB, "@revoked 10.0.0.9 " + keyB})
	if err != nil || n != 0 {
		t.Fatalf("added %d, %v", n, err)
	}
	if types := dst.KnownKeyTypes("10.0.0.1:22"); len(types) != 1 {
		t.Fatalf("a second key for a trusted host: %v", types)
	}
}

// A host trusted only through the system known_hosts (with a hashed name)
// does not get a second key from an import.
func TestImportLinesSeesSystemKnownHosts(t *testing.T) {
	a, b := newSigner(t), newSigner(t)
	sys := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.HashHostname("10.0.0.1")}, a.PublicKey())
	if err := os.WriteFile(sys, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false)
	dst.extraPath = []string{sys}
	keyB := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(b.PublicKey())))
	if n, err := dst.ImportLines([]string{"10.0.0.1 " + keyB}); err != nil || n != 0 {
		t.Fatalf("added %d, %v", n, err)
	}
}

// A line the host key check cannot read ("[host]" without a port) is not
// imported, and one that is in the file anyway (a hand edit) does not make
// the check drop the other keys in it.
func TestUnreadableKnownHostsLine(t *testing.T) {
	a, b := newSigner(t), newSigner(t)
	path := filepath.Join(t.TempDir(), "kh")
	kh := NewKnownHosts(path, false)
	keyB := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(b.PublicKey())))
	for _, bad := range []string{"[10.9.9.9] " + keyB, "[a]b:1 " + keyB, "|1|x|y " + keyB} {
		if LineReadable(bad) {
			t.Errorf("%q counted as readable", bad)
		}
		if n, _ := kh.ImportLines([]string{bad}); n != 0 {
			t.Errorf("%q imported", bad)
		}
	}
	if err := kh.Trust("10.0.0.1:22", a.PublicKey(), false); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("[10.9.9.9] " + keyB + "\n")
	f.Close()
	var warned string
	kh.Warn = func(m string) { warned = m }
	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22}
	asked := 0
	cb := kh.Callback(func(info HostKeyInfo) (bool, error) {
		asked++
		if !info.Changed || len(info.KnownKeys) != 1 {
			t.Errorf("changed key not reported as changed: %+v", info)
		}
		return false, nil
	})
	if err := cb("10.0.0.1:22", addr, a.PublicKey()); err != nil {
		t.Fatalf("trusted key: %v", err)
	}
	if err := cb("10.0.0.1:22", addr, b.PublicKey()); err == nil || asked != 1 {
		t.Fatalf("changed key: %v, asked %d", err, asked)
	}
	if !strings.Contains(warned, "line 2") {
		t.Fatalf("warning %q", warned)
	}
}
