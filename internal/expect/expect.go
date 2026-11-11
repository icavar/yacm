// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package expect runs an ordered list of expect/send rules against a
// terminal output stream, like Ásbrú's "Expect" tab.
package expect

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// Rule is a compiled expect/send step. A nil Pattern sends at once,
// without waiting for any output.
type Rule struct {
	Label      string // shown in status events (never the secret)
	Pattern    *regexp.Regexp
	Send       string
	Return     bool
	Timeout    time.Duration
	StopOnFail bool
	// Quiet: the pattern must match at the very end of the output and the
	// device must then stay quiet this long before the rule sends. A real
	// prompt ends the output; the same words in a banner, a log line or a
	// file being shown are followed by more output.
	Quiet time.Duration
}

// Event reports progress to the UI.
type Event struct {
	Index   int    `json:"index"` // 0-based rule index
	Total   int    `json:"total"`
	State   string `json:"state"` // waiting, matched, timeout, done, stopped
	Pattern string `json:"pattern"`
}

const maxBuf = 64 * 1024

// Engine is safe for concurrent Feed/Stop calls.
type Engine struct {
	mu     sync.Mutex
	rules  []Rule
	idx    int
	buf    strings.Builder
	strip  Stripper
	send   func([]byte) error
	notify func(Event)
	timer  *time.Timer
	gen    int
	qtimer *time.Timer // Quiet rules: fires when the output stayed quiet
	qgen   int
	done   bool
}

// New prepares an engine; call Start to begin.
func New(rules []Rule, send func([]byte) error, notify func(Event)) *Engine {
	if notify == nil {
		notify = func(Event) {}
	}
	return &Engine{rules: rules, send: send, notify: notify}
}

// Compile turns a user pattern into a regexp. Patterns that are not valid
// RE2 (e.g. Perl-only syntax from imported configs) are matched literally.
func Compile(pattern string) (*regexp.Regexp, bool) {
	if re, err := regexp.Compile(pattern); err == nil {
		return re, true
	}
	return regexp.MustCompile(regexp.QuoteMeta(pattern)), false
}

// Start arms the first rule.
func (e *Engine) Start() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.rules) == 0 {
		e.done = true
		return
	}
	e.armLocked()
}

// Active reports whether rules are still pending.
func (e *Engine) Active() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.done
}

func (e *Engine) armLocked() {
	if e.timer != nil {
		e.timer.Stop()
	}
	e.stopQuietLocked()
	// Rules without a pattern send straight away.
	for !e.done && e.rules[e.idx].Pattern == nil {
		r := e.rules[e.idx]
		e.notify(Event{Index: e.idx, Total: len(e.rules), State: "matched", Pattern: r.Label})
		if !e.sendLocked(r) {
			return
		}
		e.idx++
		if e.idx >= len(e.rules) {
			e.finishLocked("done")
			return
		}
	}
	if e.done {
		return
	}
	r := e.rules[e.idx]
	e.notify(Event{Index: e.idx, Total: len(e.rules), State: "waiting", Pattern: r.Label})
	e.gen++
	gen := e.gen
	to := r.Timeout
	if to <= 0 {
		to = 20 * time.Second
	}
	e.timer = time.AfterFunc(to, func() { e.onTimeout(gen) })
}

func (e *Engine) onTimeout(gen int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done || gen != e.gen {
		return
	}
	r := e.rules[e.idx]
	e.notify(Event{Index: e.idx, Total: len(e.rules), State: "timeout", Pattern: r.Label})
	if r.StopOnFail {
		e.finishLocked("stopped")
		return
	}
	e.advanceLocked()
}

func (e *Engine) advanceLocked() {
	e.idx++
	if e.idx >= len(e.rules) {
		e.finishLocked("done")
		return
	}
	e.armLocked()
}

func (e *Engine) stopQuietLocked() {
	if e.qtimer != nil {
		e.qtimer.Stop()
		e.qtimer = nil
	}
	e.qgen++
}

// onQuiet sends a Quiet rule once the output stayed quiet after its match.
func (e *Engine) onQuiet(qgen, idx int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done || qgen != e.qgen || idx != e.idx {
		return
	}
	r := e.rules[idx]
	if !r.Pattern.MatchString(e.buf.String()) {
		return
	}
	e.buf.Reset()
	e.notify(Event{Index: e.idx, Total: len(e.rules), State: "matched", Pattern: r.Label})
	if !e.sendLocked(r) {
		return
	}
	e.advanceLocked()
}

func (e *Engine) finishLocked(state string) {
	e.done = true
	if e.timer != nil {
		e.timer.Stop()
	}
	e.stopQuietLocked()
	e.notify(Event{Index: e.idx, Total: len(e.rules), State: state})
	e.buf.Reset()
}

// Feed passes terminal output to the engine.
func (e *Engine) Feed(p []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	e.buf.Write(e.strip.Strip(p))
	if e.buf.Len() > maxBuf {
		s := e.buf.String()
		e.buf.Reset()
		e.buf.WriteString(s[len(s)-maxBuf/2:])
	}
	for !e.done {
		r := e.rules[e.idx]
		if r.Pattern == nil {
			e.armLocked()
			continue
		}
		text := e.buf.String()
		if r.Quiet > 0 {
			// Any new output restarts the wait; the pattern must still end it.
			e.stopQuietLocked()
			if r.Pattern.MatchString(text) {
				qgen, idx := e.qgen, e.idx
				e.qtimer = time.AfterFunc(r.Quiet, func() { e.onQuiet(qgen, idx) })
			}
			return
		}
		loc := r.Pattern.FindStringIndex(text)
		if loc == nil {
			return
		}
		rest := text[loc[1]:]
		e.buf.Reset()
		e.buf.WriteString(rest)
		e.notify(Event{Index: e.idx, Total: len(e.rules), State: "matched", Pattern: r.Label})
		if !e.sendLocked(r) {
			return
		}
		e.advanceLocked()
	}
}

// sendLocked writes a rule's text. Sending outside the lock would reorder
// with Feed; writes are non-blocking network writes, so holding it is fine.
func (e *Engine) sendLocked(r Rule) bool {
	out := r.Send
	if r.Return {
		out += "\r"
	}
	if out != "" && e.send != nil {
		if err := e.send([]byte(out)); err != nil {
			e.finishLocked("stopped")
			return false
		}
	}
	return true
}

// Stop cancels pending rules.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.done {
		e.finishLocked("stopped")
	}
}

// Stripper removes ANSI escape sequences from a byte stream, keeping state
// across chunk boundaries.
type Stripper struct {
	state int // 0 text, 1 after ESC, 2 CSI, 3 OSC/DCS string, 4 string after ESC
	cont  int // UTF-8 continuation bytes still expected in text
}

// Strip returns p without escape sequences and NUL bytes.
func (s *Stripper) Strip(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch s.state {
		case 0:
			if s.cont > 0 && b >= 0x80 && b <= 0xbf {
				// Inside a UTF-8 character (ě is c4 9b): text, never CSI.
				s.cont--
				out = append(out, b)
				continue
			}
			s.cont = 0
			switch {
			case b == 0x1b:
				s.state = 1
			case b == 0x9b: // 8-bit CSI (outside a UTF-8 character)
				s.state = 2
			case b == 0 || b == 0x07:
			default:
				switch {
				case b >= 0xc2 && b <= 0xdf:
					s.cont = 1
				case b >= 0xe0 && b <= 0xef:
					s.cont = 2
				case b >= 0xf0 && b <= 0xf4:
					s.cont = 3
				}
				out = append(out, b)
			}
		case 1:
			switch b {
			case '[':
				s.state = 2
			case ']', 'P', '_', '^', 'X':
				s.state = 3
			case '(', ')', '*', '+', '#', '%':
				// charset designation: consume one more byte
				s.state = 5
			default:
				s.state = 0
			}
		case 2:
			if b >= 0x40 && b <= 0x7e {
				s.state = 0
			}
		case 3:
			if b == 0x07 {
				s.state = 0
			} else if b == 0x1b {
				s.state = 4
			}
		case 4:
			if b == '\\' {
				s.state = 0
			} else {
				s.state = 3
			}
		case 5:
			s.state = 0
		}
	}
	return out
}
