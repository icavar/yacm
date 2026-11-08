// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"bytes"
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/icavar/yacm/internal/applog"
	"github.com/icavar/yacm/internal/expect"
	"github.com/icavar/yacm/internal/store"
)

// promptWatch follows the end of a session's output, so YACM knows when the
// device sits at a CLI prompt (for anti-idle and the hostname offer).
type promptWatch struct {
	strip   expect.Stripper
	line    []byte    // the last, unfinished line of plain output
	lastOut time.Time // last output from the device
	lastIn  time.Time // last input sent to the device (typing, paste, automation)
	// idleTyped is what anti-idle has typed on the command line without
	// Enter. It is erased before the user's next input, so their command does
	// not end up behind "!!!" (which would make it a comment).
	idleTyped string
}

const maxPromptLine = 200

func (p *promptWatch) output(b []byte, now time.Time) {
	for _, c := range p.strip.Strip(b) {
		switch {
		case c == '\n' || c == '\r':
			p.line = p.line[:0]
		case c == '\b' || c == 0x7f:
			if len(p.line) > 0 {
				p.line = p.line[:len(p.line)-1]
			}
		case c >= 0x20 || c == '\t':
			if len(p.line) >= maxPromptLine {
				copy(p.line, p.line[1:])
				p.line = p.line[:maxPromptLine-1]
			}
			p.line = append(p.line, c)
		}
	}
	p.lastOut = now
}

// promptRe matches a CLI prompt that waits for a command and captures the
// hostname: R1>, R1#, R1(config-if)#, admin@R1>, RP/0/RSP0/CPU0:xr1#,
// root@srv:~#, [admin@MikroTik] >. Dialogs such as [confirm], Password:,
// "Destination filename [x]?" and --More-- do not end in > or #.
var promptRe = regexp.MustCompile(`^\[?(?:[\w./-]+[:@])?([A-Za-z0-9][\w.-]{0,62})\]?(?:\([\w./:-]*\))?(?::\S*)?\s?[>#]\s?$`)

// prompt returns the hostname shown by the prompt on the last line, or ok
// false when the last line is not a prompt.
func (p *promptWatch) prompt() (host string, ok bool) {
	line := p.line
	if p.idleTyped != "" && bytes.HasSuffix(line, []byte(p.idleTyped)) {
		line = line[:len(line)-len(p.idleTyped)]
	}
	m := promptRe.FindSubmatch(line)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// genericHostnames are factory defaults that make poor session names.
var genericHostnames = map[string]bool{
	"router": true, "switch": true, "localhost": true, "ios": true, "ap": true, "rommon": true,
	"cisco": true, "firewall": true, "mikrotik": true, "nxos": true, "ciscoasa": true, "ubnt": true,
}

// watchSession runs while a tab is connected: it sends the anti-idle text at
// an idle prompt and reports the device hostname once after login.
func (a *App) watchSession(ctx context.Context, t *tab) {
	defer applog.Recover("anti-idle "+t.node.Name, nil)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	hostChecked := false
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			t.mu.Lock()
			c, eng, eff, state := t.conn, t.engine, t.eff, t.state
			if c == nil || state != "connected" {
				t.mu.Unlock()
				return
			}
			quiet := now.Sub(t.watch.lastOut)
			host, atPrompt := t.watch.prompt()
			idle := now.Sub(t.watch.lastIn)
			t.mu.Unlock()
			// Never call the engine while holding t.mu: its send callback
			// takes t.mu (touchInput).
			if !atPrompt || (eng != nil && eng.Active()) {
				continue
			}
			if !hostChecked && quiet >= 700*time.Millisecond {
				hostChecked = true
				a.offerHostname(t, host)
			}
			if eff == nil || eff.AntiIdle <= 0 || quiet < 2*time.Second {
				continue
			}
			if idle >= time.Duration(eff.AntiIdle)*time.Second {
				t.mu.Lock()
				t.watch.lastIn = now
				send := antiIdleSend(&t.watch.idleTyped, eff.AntiIdleText)
				t.mu.Unlock()
				if _, err := c.Write([]byte(send)); err != nil {
					return
				}
			}
		}
	}
}

// maxIdleTyped limits how much anti-idle leaves on a command line: beyond it
// the line is cleared and typing starts again, so no device's line buffer
// fills up during a long idle period.
const maxIdleTyped = 40

// eraseKey is what the Backspace key sends in the terminal (xterm default).
const eraseKey = "\x7f"

// antiIdleSend returns the bytes anti-idle writes for text and updates what
// is left typed on the command line. Without {enter} the text is only typed
// (the default "!"): devices reset their exec-timeout on every key. With
// {enter} it is sent as a line.
func antiIdleSend(typed *string, text string) string {
	payload := strings.ReplaceAll(text, "{enter}", "\r")
	if payload == "" {
		payload = store.DefaultAntiIdleText
	}
	erase := strings.Repeat(eraseKey, utf8.RuneCountInString(*typed))
	if strings.Contains(payload, "\r") {
		*typed = ""
		return erase + payload
	}
	if len(*typed)+len(payload) > maxIdleTyped {
		*typed = payload
		return erase + payload
	}
	*typed += payload
	return payload
}

// takeIdleErase returns the keys that remove what anti-idle typed (under t.mu).
func (p *promptWatch) takeIdleErase() string {
	if p.idleTyped == "" {
		return ""
	}
	n := utf8.RuneCountInString(p.idleTyped)
	p.idleTyped = ""
	return strings.Repeat(eraseKey, n)
}

// HostnameOffer tells the UI that the device calls itself something else.
type HostnameOffer struct {
	TabID    string `json:"tabId"`
	NodeID   string `json:"nodeId"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Renamed  bool   `json:"renamed"` // setting "always": already renamed
}

func (a *App) offerHostname(t *tab, host string) {
	mode := a.store.Settings().HostnameRename
	n := t.node
	if mode == "off" || n.Session == nil || n.Session.KeepName || strings.HasPrefix(n.ID, "quick-") || a.readOnlyNode(n.ID) {
		return
	}
	if host == "" || genericHostnames[strings.ToLower(host)] || net.ParseIP(host) != nil ||
		strings.EqualFold(host, n.Name) || strings.EqualFold(host, n.Session.Host) {
		return
	}
	// A name that already starts with the hostname ("core-sw1 (lab)") is fine.
	if l := strings.ToLower(n.Name); strings.HasPrefix(l, strings.ToLower(host)+" ") || strings.HasPrefix(l, strings.ToLower(host)+"(") {
		return
	}
	offer := HostnameOffer{TabID: t.id, NodeID: n.ID, Name: n.Name, Hostname: host}
	if mode == "always" {
		if err := a.RenameSession(n.ID, host); err != nil {
			return
		}
		offer.Renamed = true
	}
	a.emit("tab:hostname", offer)
}

// RenameSession gives a session a new name (from the hostname offer).
func (a *App) RenameSession(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name is required")
	}
	n, err := a.store.Get(id)
	if err != nil {
		return err
	}
	if n.IsGroup {
		return errors.New("not a session")
	}
	n.Name = name
	_, err = a.store.Upsert(n)
	return err
}

// KeepSessionName stops the hostname offer for one session.
func (a *App) KeepSessionName(id string) error {
	n, err := a.store.Get(id)
	if err != nil {
		return err
	}
	if n.Session == nil {
		return errors.New("not a session")
	}
	n.Session.KeepName = true
	_, err = a.store.Upsert(n)
	return err
}

// readOnlyNode reports whether a node comes from a team library that this
// user does not edit.
func (a *App) readOnlyNode(id string) bool {
	return a.store.IsLibraryNode(id)
}
