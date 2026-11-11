// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yacm-app/yacm/internal/store"
)

func TestReloadCommandDetection(t *testing.T) {
	cases := []struct {
		typed  string
		reload bool
	}{
		{"reload\r", true},
		{"reload\r\r", true},                   // [confirm]
		{"reload\rno\r\r", true},               // Save? [yes/no]: no, then [confirm]
		{"relaod\x7f\x7f\x7foad\r", true},      // typo fixed with backspace
		{"request system reboot\ryes\r", true}, // Junos
		{"install add file flash:x.bin activate commit\r", true},
		{"sudo reboot\r", true},
		{"reload\rshow version\r", false}, // cancelled, then another command
		{"show reload\r", false},
		{"\x1b[A\r", false},   // history: unknown line
		{"rel\t\r", false},    // completed by the device: unknown
		{"reload\x03", false}, // Ctrl+C before Enter
	}
	for _, c := range cases {
		tb := &tab{}
		tb.noteTyped(c.typed, time.Now())
		if got := !tb.reloadAt.IsZero(); got != c.reload {
			t.Errorf("%q: reload=%v, want %v", c.typed, got, c.reload)
		}
	}
}

// waitEvents collects tab:wait events.
type waitEvents struct {
	mu sync.Mutex
	ev []WaitStatus
}

func (w *waitEvents) last() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.ev) == 0 {
		return ""
	}
	return w.ev[len(w.ev)-1].State
}

func waitFor(t *testing.T, w *waitEvents, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if w.last() == state {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %q event; last %q", state, w.last())
}

func TestWaitForDeviceAfterReload(t *testing.T) {
	oldP, oldS := probeInterval, settleDelay
	probeInterval, settleDelay = 30*time.Millisecond, 10*time.Millisecond
	defer func() { probeInterval, settleDelay = oldP, oldS }()

	a := newTestApp(t)
	w := &waitEvents{}
	a.emitter = func(name string, data any) {
		if ws, ok := data.(WaitStatus); ok && name == "tab:wait" {
			w.mu.Lock()
			w.ev = append(w.ev, ws)
			w.mu.Unlock()
		}
	}
	// A port that is closed first (the device is reloading).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	host, ps, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(ps)
	n := &store.Node{ID: "n1", Name: "r1", Session: &store.Session{Protocol: store.ProtoTelnet, Host: host, Port: port}}
	tb := &tab{id: "t1", node: n, state: "closed"}
	a.tabsMu.Lock()
	a.tabs["t1"] = tb
	a.tabsMu.Unlock()

	tb.reloadAt = time.Now()
	a.afterClose(tb)
	waitFor(t, w, "waiting")
	time.Sleep(150 * time.Millisecond) // a few failed probes: seen down
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skip("port taken again:", err)
	}
	defer ln2.Close()
	go func() {
		for {
			c, err := ln2.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	waitFor(t, w, "up")

	// Stop works, and the setting turns automatic waiting off.
	if err := a.WaitForDevice("t1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, w, "waiting")
	a.StopWaiting("t1")
	waitFor(t, w, "stopped")
	st := a.store.Settings()
	st.NoReloadReconnect = true
	_ = a.store.SetSettings(st)
	before := len(w.ev)
	a.afterClose(tb)
	time.Sleep(100 * time.Millisecond)
	if len(w.ev) != before {
		t.Fatal("waiting started although the setting is off")
	}
}
