// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icavar/yacm/internal/store"
)

func TestPromptRecognition(t *testing.T) {
	cases := map[string]string{
		"R1>": "R1", "R1#": "R1", "core-sw1(config-if)#": "core-sw1", "N9K-1(config)# ": "N9K-1",
		"admin@mx1>": "mx1", "RP/0/RSP0/CPU0:xr-pe1#": "xr-pe1", "root@srv01:~#": "srv01",
		"[admin@MikroTik] >": "MikroTik", "sw.zg.lab#": "sw.zg.lab",
	}
	for line, host := range cases {
		var w promptWatch
		w.output([]byte("\r\nsome output\r\n"+line), time.Now())
		if h, ok := w.prompt(); !ok || h != host {
			t.Errorf("%q: got %q %v, want %q", line, h, ok, host)
		}
	}
	for _, line := range []string{
		"Proceed with reload? [confirm]", "Password: ", "Destination filename [startup-config]? ",
		" --More-- ", "Username:", "rommon 1 >", "R1#show ver", "user@host:~$ ", "Building configuration...",
		"interface Gi1/0/1 is up, line protocol is up",
	} {
		var w promptWatch
		w.output([]byte("x\r\n"+line), time.Now())
		if h, ok := w.prompt(); ok {
			t.Errorf("%q taken as a prompt (%q)", line, h)
		}
	}
	// Escape sequences, backspaces and chunk boundaries.
	var w promptWatch
	w.output([]byte("R1#sh\x1b[1m"), time.Now())
	w.output([]byte("\b\b\x1b[0m"), time.Now())
	if h, ok := w.prompt(); !ok || h != "R1" {
		t.Fatalf("after erasing typed text: %q %v (line %q)", h, ok, w.line)
	}
}

// fakeConn records what the session sends and lets the test play output.
type fakeConn struct {
	mu   sync.Mutex
	sent bytes.Buffer
	out  chan []byte
}

func (f *fakeConn) Read(p []byte) (int, error) {
	b, ok := <-f.out
	if !ok {
		return 0, context.Canceled
	}
	return copy(p, b), nil
}
func (f *fakeConn) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent.Write(p)
}
func (f *fakeConn) Close() error          { return nil }
func (f *fakeConn) Resize(int, int) error { return nil }
func (f *fakeConn) SendBreak() error      { return nil }
func (f *fakeConn) written() string       { f.mu.Lock(); defer f.mu.Unlock(); return f.sent.String() }

func TestAntiIdleOnlyAtPrompt(t *testing.T) {
	a := newTestApp(t)
	var mu sync.Mutex
	var events []HostnameOffer
	a.emitter = func(name string, data any) {
		if name == "tab:hostname" {
			mu.Lock()
			events = append(events, data.(HostnameOffer))
			mu.Unlock()
		}
	}
	node, err := a.store.Upsert(&store.Node{Name: "10.0.0.1", Session: &store.Session{Protocol: store.ProtoTelnet, Host: "10.0.0.1", AntiIdle: 10}})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeConn{out: make(chan []byte)}
	tb := &tab{id: "t1", node: node, state: "connected", conn: fc}
	eff, _ := a.store.Effective(node)
	tb.eff = eff
	if eff.AntiIdle != 10 || eff.AntiIdleText != "!" {
		t.Fatalf("effective anti-idle %d %q", eff.AntiIdle, eff.AntiIdleText)
	}
	old := time.Now().Add(-time.Minute)
	tb.watch = promptWatch{lastIn: old, lastOut: old}
	tb.watch.output([]byte("Proceed with reload? [confirm]"), old)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.watchSession(ctx, tb)
	time.Sleep(1500 * time.Millisecond)
	if got := fc.written(); got != "" {
		t.Fatalf("sent %q at a [confirm] dialog", got)
	}
	tb.mu.Lock()
	tb.watch.output([]byte("\r\ncore-sw1#"), old)
	tb.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for fc.written() == "" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := fc.written(); got != "!" {
		t.Fatalf("anti-idle sent %q", got)
	}
	// The send counts as input: nothing more until the interval passes again.
	time.Sleep(1500 * time.Millisecond)
	if got := fc.written(); got != "!" {
		t.Fatalf("sent again too early: %q", got)
	}
	// The echoed "!" does not hide the prompt from the next round.
	tb.mu.Lock()
	tb.watch.output([]byte("!"), old)
	_, still := tb.watch.prompt()
	tb.mu.Unlock()
	if !still {
		t.Fatal("prompt not recognised behind the typed !")
	}
	// The user's first key erases what anti-idle typed.
	if err := a.writeTab(tb, "show ver\r"); err != nil {
		t.Fatal(err)
	}
	if got := fc.written(); got != "!\x7fshow ver\r" {
		t.Fatalf("after typing: %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Hostname != "core-sw1" || events[0].Name != "10.0.0.1" || events[0].Renamed {
		t.Fatalf("hostname offers %+v", events)
	}
}

func TestAntiIdleInheritance(t *testing.T) {
	a := newTestApp(t)
	g, _ := a.store.Upsert(&store.Node{Name: "DC", IsGroup: true, Defaults: &store.GroupDefaults{AntiIdle: 300, AntiIdleText: "#"}})
	sub, _ := a.store.Upsert(&store.Node{Name: "Sub", IsGroup: true, ParentID: g.ID})
	s1, _ := a.store.Upsert(&store.Node{Name: "a", ParentID: sub.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "a"}})
	s2, _ := a.store.Upsert(&store.Node{Name: "b", ParentID: sub.ID, Session: &store.Session{Protocol: store.ProtoSSH, Host: "b", AntiIdle: -1}})
	s3, _ := a.store.Upsert(&store.Node{Name: "c", Session: &store.Session{Protocol: store.ProtoSSH, Host: "c"}})
	for _, c := range []struct {
		n    *store.Node
		sec  int
		text string
	}{{s1, 300, "#"}, {s2, 0, "#"}, {s3, 0, "!"}} {
		e, err := a.store.Effective(c.n)
		if err != nil || e.AntiIdle != c.sec || e.AntiIdleText != c.text {
			t.Errorf("%s: %d %q %v", c.n.Name, e.AntiIdle, e.AntiIdleText, err)
		}
	}
	if _, err := a.store.Upsert(&store.Node{Name: "x", Session: &store.Session{Protocol: store.ProtoSSH, Host: "x", AntiIdle: 3}}); err == nil {
		t.Fatal("3 seconds accepted")
	}
}

func TestAntiIdleSend(t *testing.T) {
	typed := ""
	if got := antiIdleSend(&typed, "!"); got != "!" || typed != "!" {
		t.Fatalf("first: %q %q", got, typed)
	}
	if got := antiIdleSend(&typed, "!"); got != "!" || typed != "!!" {
		t.Fatalf("second: %q %q", got, typed)
	}
	// With {enter} the line is cleared first and then sent.
	if got := antiIdleSend(&typed, " {enter}"); got != "\x7f\x7f \r" || typed != "" {
		t.Fatalf("enter: %q %q", got, typed)
	}
	// A long idle period never fills the line: it starts over at the limit.
	typed = ""
	for i := 0; i < maxIdleTyped; i++ {
		antiIdleSend(&typed, "!")
	}
	got := antiIdleSend(&typed, "!")
	if got != strings.Repeat("\x7f", maxIdleTyped)+"!" || typed != "!" {
		t.Fatalf("limit: %q %q", got, typed)
	}
	if got := antiIdleSend(&typed, ""); got != "!" {
		t.Fatalf("empty text: %q", got)
	}
}
