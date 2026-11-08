// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

// Command output to a file, through the open session: the command is typed
// into the terminal with the rights the session already has (enable mode),
// pagers (--More--, ---(more)---, ---- More ----) are answered with a space,
// and what the device prints is written to a file until its prompt comes
// back. It works for SSH, telnet and serial and for any vendor, unlike a
// separate SSH exec channel (IOS XE prints nothing for show tech-support
// there, and such a channel never has enable rights).

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/icavar/yacm/internal/expect"
)

// pagerRe matches a pager prompt at the end of the unfinished line.
var pagerRe = regexp.MustCompile(`(?i)(-{2,}\s*\(?\s*more\b[^-\r\n]{0,12}\)?\s*-{2,}|<-+\s*more\s*-+>)\s*$`)

// captureQuiet is how long the prompt must stay the last thing printed
// before the capture ends (a prompt-like line in the middle of the output
// is followed by more).
const captureQuiet = 400 * time.Millisecond

// capture follows the output of one command in a session.
type capture struct {
	mu       sync.Mutex
	command  string
	prompt   string // the session's prompt when the command was sent
	w        *bufio.Writer
	strip    expect.Stripper
	line     []byte // the unfinished line
	cr       bool   // a carriage return was the last character
	echoSeen bool   // the device has echoed the command line
	n        int64  // bytes written
	eol      string
	send     func(b []byte) error // answers a pager
	tick     func(int64)
	quiet    *time.Timer
	done     chan error
	finished bool
	pages    int
}

func newCapture(command, prompt string, w *bufio.Writer, send func([]byte) error, tick func(int64)) *capture {
	eol := "\n"
	if runtime.GOOS == "windows" {
		eol = "\r\n"
	}
	return &capture{command: command, prompt: strings.TrimSpace(prompt), w: w, eol: eol, send: send, tick: tick, done: make(chan error, 1)}
}

// feed takes output from the device. It reports whether the output belongs
// to the capture (and is kept out of the terminal).
func (c *capture) feed(b []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return false
	}
	if c.quiet != nil {
		c.quiet.Stop()
		c.quiet = nil
	}
	for _, ch := range c.strip.Strip(cursorBack(b)) {
		switch {
		case ch == '\n':
			c.flushLine()
			c.cr = false
		case ch == '\r':
			c.cr = true
		default:
			if c.cr {
				// A carriage return without a new line: the line is written
				// again from its start (pagers erase themselves like this).
				c.line = c.line[:0]
				c.cr = false
			}
			switch {
			case ch == '\b' || ch == 0x7f:
				if len(c.line) > 0 {
					c.line = c.line[:len(c.line)-1]
				}
			case ch >= 0x20 || ch == '\t':
				c.line = append(c.line, ch)
			}
		}
	}
	partial := strings.TrimRight(string(c.line), " ")
	switch {
	case len(partial) < 60 && pagerRe.MatchString(partial):
		c.line = c.line[:0]
		c.pages++
		go func() { _ = c.send([]byte(" ")) }()
	case c.echoSeen && strings.TrimSpace(partial) == c.prompt:
		c.quiet = time.AfterFunc(captureQuiet, func() { c.finish(nil) })
	case c.echoSeen && looksLikePrompt(strings.TrimSpace(partial)):
		// Another prompt (the command changed mode): end after a longer
		// quiet spell.
		c.quiet = time.AfterFunc(8*captureQuiet, func() { c.finish(nil) })
	}
	return true
}

// flushLine writes the finished line (the echo of the command is left out).
func (c *capture) flushLine() {
	text := strings.TrimRight(string(c.line), " \t")
	c.line = c.line[:0]
	if !c.echoSeen {
		// The first line is the command as the device echoes it.
		c.echoSeen = true
		return
	}
	n, _ := c.w.WriteString(text + c.eol)
	c.n += int64(n)
	if c.tick != nil {
		c.tick(c.n)
	}
}

// finish ends the capture once (err nil: the prompt came back).
func (c *capture) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	c.finished = true
	if c.quiet != nil {
		c.quiet.Stop()
	}
	c.done <- err
}

// SessionCommandToFile types command into the open session and saves what
// the device prints until its prompt is back. The output is kept out of the
// terminal; a line there says where it went. It returns a transfer id.
func (a *App) SessionCommandToFile(tabID, command, localPath string) (string, error) {
	t, err := a.getTab(tabID)
	if err != nil {
		return "", err
	}
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, "\r\n") {
		return "", errors.New("enter one command")
	}
	t.mu.Lock()
	connected := t.conn != nil && t.state == "connected"
	prompt := strings.TrimSpace(string(t.watch.line))
	busy := t.capture != nil
	name := t.node.Name
	t.mu.Unlock()
	switch {
	case !connected:
		return "", errors.New("the session is not connected")
	case busy:
		return "", errors.New("the session is already saving the output of a command")
	case !looksLikePrompt(prompt):
		return "", errors.New("the session is not at the device's prompt: wait for the prompt (and clear the command line), then try again")
	}
	ev := TransferEvent{Name: command, Dir: "cmd", Local: localPath, Remote: command}
	return a.startJob("", name, ev, func(ctx context.Context, ev *TransferEvent, tick func(int64)) error {
		tmp := localPath + ".part"
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		w := bufio.NewWriterSize(f, 64<<10)
		send := func(b []byte) error {
			t.mu.Lock()
			c := t.conn
			t.watch.lastIn = time.Now()
			t.mu.Unlock()
			if c == nil {
				return errors.New("not connected")
			}
			_, err := c.Write(b)
			return err
		}
		cp := newCapture(command, prompt, w, send, tick)
		t.mu.Lock()
		t.capture = cp
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			if t.capture == cp {
				t.capture = nil
			}
			t.mu.Unlock()
		}()
		if err := a.writeTab(t, command+"\r"); err != nil {
			cp.finish(err)
		}
		// The device goes quiet for a long time only when something is
		// wrong: show tech pauses for seconds, not minutes.
		idle := time.NewTicker(5 * time.Second)
		defer idle.Stop()
		var result error
	wait:
		for {
			select {
			case result = <-cp.done:
				break wait
			case <-ctx.Done():
				cp.finish(ctx.Err())
				result = ctx.Err()
				break wait
			case <-idle.C:
				t.mu.Lock()
				gone := t.conn == nil
				last := t.watch.lastOut
				t.mu.Unlock()
				switch {
				case gone:
					cp.finish(errors.New("the session closed"))
				case time.Since(last) > 5*time.Minute:
					cp.finish(errors.New("the device printed nothing for 5 minutes"))
				}
			}
		}
		if ferr := w.Flush(); result == nil {
			result = ferr
		}
		if cerr := f.Close(); result == nil {
			result = cerr
		}
		cp.mu.Lock()
		n, pages, rest := cp.n, cp.pages, strings.TrimRight(string(cp.line), " ")
		cp.mu.Unlock()
		if result == nil {
			_ = os.Remove(localPath)
			result = os.Rename(tmp, localPath)
		} else {
			_ = os.Remove(tmp)
		}
		// The terminal gets back the command, a note and the prompt.
		note := fmt.Sprintf("[YACM] output saved to %s (%s%s)", localPath, humanBytes(n), map[bool]string{true: fmt.Sprintf(", %d pages answered", pages)}[pages > 0])
		if result != nil {
			note = "[YACM] output not saved: " + result.Error()
		}
		a.emit("tab:data", map[string]string{"tabId": t.id, "data": base64.StdEncoding.EncodeToString([]byte(command + "\r\n\x1b[2m" + note + "\x1b[0m\r\n" + rest))})
		return result
	}), nil
}

// cursorBackRe is "cursor back n columns", which pagers (Huawei VRP) use to
// erase themselves.
var cursorBackRe = regexp.MustCompile(`\x1b\[(\d*)D`)

// cursorBack turns cursor-back sequences into backspaces, which the line
// buffer understands.
func cursorBack(b []byte) []byte {
	if !cursorBackRe.Match(b) {
		return b
	}
	return cursorBackRe.ReplaceAllFunc(b, func(m []byte) []byte {
		n := 1
		if d := string(m[2 : len(m)-1]); d != "" {
			if v, err := strconv.Atoi(d); err == nil && v > 0 && v < 1000 {
				n = v
			}
		}
		return []byte(strings.Repeat("\b", n))
	})
}

// looksLikePrompt reports whether the unfinished line is a CLI prompt
// (router#, switch>, user@host:~$, [~HUAWEI], user@router>, RP/0/RP0/CPU0:xr#).
func looksLikePrompt(s string) bool {
	if s == "" || len(s) > 120 {
		return false
	}
	switch s[len(s)-1] {
	case '#', '>', '$', '%', ']', ':':
		return true
	}
	return false
}
