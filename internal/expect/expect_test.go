// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package expect

import (
	"sync"
	"testing"
	"time"
)

type rec struct {
	mu     sync.Mutex
	sent   []string
	events []Event
}

func (r *rec) send(b []byte) error {
	r.mu.Lock()
	r.sent = append(r.sent, string(b))
	r.mu.Unlock()
	return nil
}
func (r *rec) note(e Event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }

func TestSequenceAcrossChunks(t *testing.T) {
	r := &rec{}
	p1, _ := Compile(`[Pp]assword:\s*$`)
	p2, _ := Compile(`#\s*$`)
	e := New([]Rule{{Pattern: p1, Send: "secret", Return: true}, {Pattern: p2, Send: "terminal length 0", Return: true}}, r.send, r.note)
	e.Start()
	e.Feed([]byte("User Access Verification\r\n\x1b[1mPass"))
	e.Feed([]byte("word: "))
	e.Feed([]byte("\r\nR1#"))
	if len(r.sent) != 2 || r.sent[0] != "secret\r" || r.sent[1] != "terminal length 0\r" {
		t.Fatalf("sent %q", r.sent)
	}
	if e.Active() {
		t.Fatal("should be done")
	}
}

func TestTimeoutContinueAndStop(t *testing.T) {
	r := &rec{}
	a, _ := Compile("never")
	b, _ := Compile(">")
	e := New([]Rule{{Pattern: a, Send: "x", Timeout: 50 * time.Millisecond}, {Pattern: b, Send: "enable", Return: true}}, r.send, r.note)
	e.Start()
	time.Sleep(120 * time.Millisecond)
	e.Feed([]byte("R1>"))
	if len(r.sent) != 1 || r.sent[0] != "enable\r" {
		t.Fatalf("sent %q", r.sent)
	}
	r2 := &rec{}
	e2 := New([]Rule{{Pattern: a, Send: "x", Timeout: 50 * time.Millisecond, StopOnFail: true}, {Pattern: b, Send: "enable"}}, r2.send, r2.note)
	e2.Start()
	time.Sleep(120 * time.Millisecond)
	e2.Feed([]byte("R1>"))
	if len(r2.sent) != 0 || e2.Active() {
		t.Fatalf("stop on fail: %q", r2.sent)
	}
}

func TestLiteralFallback(t *testing.T) {
	re, ok := Compile(`(?<=foo)bar`) // Perl lookbehind, not RE2
	if ok || !re.MatchString("(?<=foo)bar") {
		t.Fatal("literal fallback")
	}
}

func TestStripper(t *testing.T) {
	var s Stripper
	out := string(s.Strip([]byte("a\x1b[3"))) + string(s.Strip([]byte("1mb\x1b]0;title\x07c\x1b(Bd")))
	if out != "abcd" {
		t.Fatalf("%q", out)
	}
}

// Rules without a pattern send at once: at the start, between waiting
// rules and at the end.
func TestSendAtOnce(t *testing.T) {
	r := &rec{}
	p, _ := Compile(`word:`)
	e := New([]Rule{
		{Send: "", Return: true},
		{Pattern: p, Send: "pw", Return: true},
		{Send: "terminal length 0", Return: true},
		{Send: "show clock", Return: true},
	}, r.send, r.note)
	e.Start()
	if len(r.sent) != 1 || r.sent[0] != "\r" {
		t.Fatalf("start sent %q", r.sent)
	}
	e.Feed([]byte("Password: word:"))
	if len(r.sent) != 4 || r.sent[1] != "pw\r" || r.sent[2] != "terminal length 0\r" || r.sent[3] != "show clock\r" {
		t.Fatalf("sent %q", r.sent)
	}
	if e.Active() {
		t.Fatal("should be done")
	}
	only := &rec{}
	e2 := New([]Rule{{Send: "x"}}, only.send, only.note)
	e2.Start()
	if e2.Active() || len(only.sent) != 1 {
		t.Fatalf("single immediate rule: active %v, sent %q", e2.Active(), only.sent)
	}
}
