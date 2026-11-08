// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package xfer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrConnClosed: the SSH connection was gone before a channel could open.
// Cisco IOS closes the connection when its one session channel closes, so
// after dir or an SCP copy the next command finds it closed.
var ErrConnClosed = errors.New("the SSH connection is closed")

// Link is the SSH connection the file commands use.
//
// Commands start in parallel, each on a channel of its own. A device that
// refuses a second channel (Cisco IOS takes one per connection) switches
// the link to one command at a time: the refused command waits for the
// others and runs after them. When the device has closed the connection,
// Do gets a new one from Redial and runs the command again.
type Link struct {
	mu     sync.Mutex
	cond   *sync.Cond
	c      *ssh.Client
	redial func(ctx context.Context) (*ssh.Client, error)
	active int  // commands running
	serial bool // one command at a time
	// OnSerial is called once when the link switches to one at a time.
	OnSerial func()
	// Logf receives what happens on the link (for YACM's log).
	Logf func(format string, args ...any)
}

// NewLink uses c; redial (may be nil) gives a new connection to the device.
func NewLink(c *ssh.Client, redial func(ctx context.Context) (*ssh.Client, error)) *Link {
	l := &Link{c: c, redial: redial}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// SetSerial starts the link in one-at-a-time mode (a device known to take
// one channel per connection).
func (l *Link) SetSerial() {
	l.mu.Lock()
	l.serial = true
	l.mu.Unlock()
}

// Serial reports whether commands run one at a time.
func (l *Link) Serial() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.serial
}

func (l *Link) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

// Client is the current connection.
func (l *Link) Client() *ssh.Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.c
}

type waitKey struct{}

// WithWaitNotice returns a context whose commands report when they wait
// for others (true) and when they start after waiting (false).
func WithWaitNotice(ctx context.Context, f func(waiting bool)) context.Context {
	return context.WithValue(ctx, waitKey{}, f)
}

// isBusy reports a channel refused because the device has none to spare.
func isBusy(err error) bool {
	var oce *ssh.OpenChannelError
	return errors.As(err, &oce) && oce.Reason == ssh.ResourceShortage
}

// acquire waits for a turn (with l.mu held) and returns the connection.
func (l *Link) acquire(ctx context.Context) (*ssh.Client, error) {
	if l.serial && l.active > 0 {
		notice, _ := ctx.Value(waitKey{}).(func(bool))
		if notice != nil {
			l.mu.Unlock()
			notice(true)
			l.mu.Lock()
		}
		stop := context.AfterFunc(ctx, func() {
			l.mu.Lock()
			l.cond.Broadcast()
			l.mu.Unlock()
		})
		for l.serial && l.active > 0 && ctx.Err() == nil {
			l.cond.Wait()
		}
		stop()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if notice != nil {
			l.mu.Unlock()
			notice(false)
			l.mu.Lock()
		}
	}
	l.active++
	return l.c, nil
}

func (l *Link) release() {
	l.mu.Lock()
	l.active--
	l.cond.Broadcast()
	l.mu.Unlock()
}

// Do runs fn on the connection: once more on a new connection when the
// device had closed the old one before fn could open its channel, and
// after the others when the device had no channel to spare.
func (l *Link) Do(ctx context.Context, fn func(c *ssh.Client) error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		l.mu.Lock()
		c, aerr := l.acquire(ctx)
		l.mu.Unlock()
		if aerr != nil {
			return aerr
		}
		err = fn(c)
		// The device closed the connection (Cisco IOS after each command):
		// a new one, then the command again, keeping this command's turn.
		for r := 0; errors.Is(err, ErrConnClosed) && l.redial != nil && r < 3; r++ {
			nc, rerr := l.reconnect(ctx, c)
			if rerr != nil {
				l.release()
				return fmt.Errorf("the device closed the connection, and reconnecting failed: %w", rerr)
			}
			c = nc
			err = fn(c)
		}
		switch {
		case isBusy(err):
			// No channel to spare: from now on one command at a time.
			l.mu.Lock()
			first := !l.serial
			l.serial = true
			l.active--
			l.cond.Broadcast()
			l.mu.Unlock()
			if first {
				l.logf("the device takes one channel per connection: one command at a time")
				if l.OnSerial != nil {
					l.OnSerial()
				}
			}
		default:
			l.release()
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}

// reconnect replaces c with a new connection unless another command did
// so already.
func (l *Link) reconnect(ctx context.Context, c *ssh.Client) (*ssh.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.c != c {
		return l.c, nil
	}
	start := time.Now()
	nc, err := l.redial(ctx)
	if err != nil {
		l.logf("reconnecting failed after %v: %v", time.Since(start).Round(time.Millisecond), err)
		return nil, err
	}
	l.logf("the device closed the connection: reconnected in %v", time.Since(start).Round(time.Millisecond))
	l.c = nc
	return nc, nil
}
