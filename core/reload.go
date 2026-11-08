// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/sshconn"
	"github.com/icavar/yacm/internal/store"
)

// Reload watch: after a reload, reboot or software activation the session
// drops; YACM waits until the device answers again and reconnects. It can
// also be started by hand (Wait for the device and reconnect).

// reloadCommands are commands that restart a device.
var reloadCommands = regexp.MustCompile(`(?i)^\s*(` + strings.Join([]string{
	`(do\s+)?reload\b`,            // Cisco IOS, IOS XE, NX-OS, ASA, Arista
	`reboot\b`,                    // Huawei, Linux
	`request\s+system\s+reboot\b`, // Junos
	`request\s+system\s+software\s+add\b.*\breboot\b`,
	`request\s+restart\s+system\b`, // PAN-OS
	`install\b.*\bactivate\b`,      // IOS XE install mode
	`execute\s+reboot\b`,           // FortiOS
	`/?system\s+reboot\b`,          // RouterOS
	`(sudo\s+)?(reboot|shutdown\s+-r|systemctl\s+reboot)\b`,
}, "|") + `)`)

// reloadAnswers keep a pending reload: the confirmations devices ask for
// ([confirm], Save? [yes/no], (y/n)).
func reloadAnswer(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "n", "yes", "no", "confirm":
		return true
	}
	return false
}

// reloadWindow is how long after a reload command a dropped connection
// still counts as the reload.
const reloadWindow = 30 * time.Minute

// inputLine follows what the user types on the current command line, well
// enough to recognise a reload command. Arrow keys and history make the
// line unknown until the next Enter.
type inputLine struct {
	buf     []rune
	unknown bool
	esc     int // 0 none, 1 after ESC, 2 inside CSI
}

// feed processes typed data and returns the lines finished with Enter
// (an unknown line is returned as "\x00").
func (l *inputLine) feed(data string) []string {
	var done []string
	for len(data) > 0 {
		r, size := utf8.DecodeRuneInString(data)
		data = data[size:]
		switch {
		case l.esc == 1:
			l.esc = 0
			if r == '[' || r == 'O' {
				l.esc = 2
			}
			l.unknown = true
		case l.esc == 2:
			if r >= 0x40 && r <= 0x7e {
				l.esc = 0
			}
		case r == 0x1b:
			l.esc = 1
		case r == '\r' || r == '\n':
			if l.unknown {
				done = append(done, "\x00")
			} else {
				done = append(done, string(l.buf))
			}
			l.buf, l.unknown = l.buf[:0], false
		case r == 0x7f || r == '\b':
			if len(l.buf) > 0 {
				l.buf = l.buf[:len(l.buf)-1]
			}
		case r == 0x15 || r == 0x03: // Ctrl+U, Ctrl+C
			l.buf, l.unknown = l.buf[:0], false
		case r == '\t':
			l.unknown = true // the device completes the word
		case r < 0x20:
			// other control keys: ignored
		default:
			if len(l.buf) < 256 {
				l.buf = append(l.buf, r)
			}
		}
	}
	return done
}

// noteTyped updates the reload state from user input (called with t.mu held).
func (t *tab) noteTyped(data string, now time.Time) {
	for _, line := range t.input.feed(data) {
		switch {
		case line != "\x00" && reloadCommands.MatchString(line):
			t.reloadAt = now
		case line != "\x00" && reloadAnswer(line):
			// keeps a pending reload
		default:
			t.reloadAt = time.Time{}
		}
	}
}

// WaitStatus is emitted as "tab:wait".
type WaitStatus struct {
	TabID   string `json:"tabId"`
	State   string `json:"state"` // waiting, up, timeout, stopped, error
	Since   int64  `json:"since"` // unix ms when waiting started
	Max     int    `json:"max"`   // seconds
	Message string `json:"message,omitempty"`
}

func (a *App) reloadWaitMax() time.Duration {
	m := a.store.Settings().ReloadWaitMinutes
	if m <= 0 {
		m = 30
	}
	return time.Duration(m) * time.Minute
}

// afterClose starts waiting when the connection dropped soon after a reload
// command and the setting is on.
func (a *App) afterClose(t *tab) {
	t.mu.Lock()
	at := t.reloadAt
	t.mu.Unlock()
	if at.IsZero() || time.Since(at) > reloadWindow || a.store.Settings().NoReloadReconnect {
		return
	}
	if p := t.node.Session.Protocol; p != store.ProtoSSH && p != store.ProtoTelnet {
		return
	}
	a.startWaiting(t, true)
}

// WaitForDevice waits until the device of a disconnected tab answers again,
// then the UI reconnects.
func (a *App) WaitForDevice(tabID string) error {
	t, err := a.getTab(tabID)
	if err != nil {
		return err
	}
	t.mu.Lock()
	state := t.state
	t.mu.Unlock()
	if state == "connected" || state == "connecting" {
		return errors.New("the session is connected")
	}
	if p := t.node.Session.Protocol; p != store.ProtoSSH && p != store.ProtoTelnet {
		return errors.New("only SSH and telnet sessions can be watched")
	}
	a.startWaiting(t, false)
	return nil
}

// StopWaiting stops waiting for a device.
func (a *App) StopWaiting(tabID string) {
	if t, err := a.getTab(tabID); err == nil {
		t.stopWaiting()
	}
}

func (t *tab) stopWaiting() {
	t.mu.Lock()
	cancel := t.waitCancel
	t.waitCancel = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) startWaiting(t *tab, auto bool) {
	t.stopWaiting()
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.waitCancel = cancel
	t.mu.Unlock()
	go a.waitLoop(ctx, t, auto)
}

// probeInterval is how often the device is tried; settleDelay gives the
// service a moment after its port opens.
var (
	probeInterval = 5 * time.Second
	settleDelay   = 3 * time.Second
)

// waitLoop tries the session's port until the device answers. An automatic
// wait (after a reload) first has to see the device go down, so a session
// that dropped without a reload does not reconnect at once; if it never goes
// down within downWithin, waiting stops.
func (a *App) waitLoop(ctx context.Context, t *tab, auto bool) {
	start := time.Now()
	max := a.reloadWaitMax()
	ss := t.node.Session
	addr := net.JoinHostPort(ss.Host, strconv.Itoa(ss.Port))
	emit := func(state, msg string) {
		a.emit("tab:wait", WaitStatus{TabID: t.id, State: state, Since: start.UnixMilli(), Max: int(max / time.Second), Message: msg})
	}
	emit("waiting", "")
	applog.Infof("session %s: waiting for %s to answer again", t.node.Name, addr)

	t.mu.Lock()
	eff := t.eff
	t.mu.Unlock()
	var jumps *sshconn.Jumps
	defer func() {
		if jumps != nil {
			jumps.Close()
		}
	}()
	interval := probeInterval
	if eff != nil && len(eff.Hops) > 0 {
		interval *= 2
	}
	seenDown := !auto
	downWithin := 2 * time.Minute
	tick := time.NewTimer(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			emit("stopped", "")
			return
		case <-tick.C:
		}
		if time.Since(start) > max {
			emit("timeout", fmt.Sprintf("%s did not answer within %d minutes", t.node.Name, int(max/time.Minute)))
			t.stopWaiting()
			return
		}
		dial := func(pctx context.Context) (net.Conn, error) {
			if pd := a.proxyDial(eff); pd != nil {
				return pd(pctx, addr, 5*time.Second)
			}
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(pctx, "tcp", addr)
		}
		if eff != nil && len(eff.Hops) > 0 {
			if jumps == nil {
				j, err := sshconn.ConnectJumps(ctx, sshconn.Config{
					Hops: a.jumpHopsQuiet(t.node, eff), Profile: store.AlgoSecure, ConnectTimeout: 10 * time.Second,
					KnownHosts: a.kh, Prompter: tabPrompter{a, t.id, t.node.Name},
					Dial: a.proxyDial(eff),
				})
				if err != nil {
					tick.Reset(interval)
					continue // the jump host may be restarting too
				}
				jumps = j
			}
			dial = func(pctx context.Context) (net.Conn, error) { return jumps.DialContext(pctx, addr) }
		}
		up := probe(ctx, dial, ss.Protocol == store.ProtoSSH)
		if up == probeJumpLost {
			jumps.Close()
			jumps = nil
		}
		switch {
		case up == probeUp && seenDown:
			// Give the SSH or telnet service a moment after it starts listening.
			select {
			case <-ctx.Done():
				emit("stopped", "")
				return
			case <-time.After(settleDelay):
			}
			emit("up", "")
			applog.Infof("session %s: %s answers again after %s", t.node.Name, addr, time.Since(start).Round(time.Second))
			t.mu.Lock()
			t.waitCancel = nil
			t.mu.Unlock()
			return
		case up != probeUp:
			seenDown = true
		case !seenDown && time.Since(start) > downWithin:
			emit("error", fmt.Sprintf("%s did not go down, so it was not reconnected", t.node.Name))
			t.stopWaiting()
			return
		}
		tick.Reset(interval)
	}
}

type probeResult int

const (
	probeDown probeResult = iota
	probeUp
	probeJumpLost
)

// probe connects once; for SSH the server must also send its banner.
func probe(ctx context.Context, dial func(context.Context) (net.Conn, error), ssh bool) probeResult {
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nc, err := dial(pctx)
	if err != nil {
		if strings.Contains(err.Error(), "via ") && (errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed")) {
			return probeJumpLost
		}
		return probeDown
	}
	defer nc.Close()
	if !ssh {
		return probeUp
	}
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, _ := io.ReadAtLeast(nc, buf, 4)
	if n >= 4 && string(buf[:4]) == "SSH-" {
		return probeUp
	}
	return probeDown
}
