// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package telnet is a small RFC 854 client with the options terminals need:
// ECHO, SUPPRESS-GO-AHEAD, TERMINAL-TYPE (RFC 1091) and NAWS (RFC 1073).
package telnet

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	iac  = 255
	dont = 254
	do   = 253
	wont = 252
	will = 251
	sb   = 250
	brk  = 243
	se   = 240

	optBinary = 0
	optEcho   = 1
	optSGA    = 3
	optTType  = 24
	optNAWS   = 31

	ttypeIs   = 0
	ttypeSend = 1
)

// Conn is a telnet session.
type Conn struct {
	nc       net.Conn
	out      *io.PipeReader
	wmu      sync.Mutex
	nawsOn   atomic.Bool
	binary   atomic.Bool
	cols     atomic.Int32
	rows     atomic.Int32
	term     string
	closeOne sync.Once
}

// Dial connects to host:port.
func Dial(ctx context.Context, host string, port int, timeout time.Duration, cols, rows int) (*Conn, error) {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	d := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	c := &Conn{nc: nc, out: pr, term: "XTERM-256COLOR"}
	c.cols.Store(int32(cols))
	c.rows.Store(int32(rows))
	go c.readLoop(pw)
	return c, nil
}

// NewFromConn wraps an existing connection (used by tests).
func NewFromConn(nc net.Conn, cols, rows int) *Conn {
	pr, pw := io.Pipe()
	c := &Conn{nc: nc, out: pr, term: "XTERM-256COLOR"}
	c.cols.Store(int32(cols))
	c.rows.Store(int32(rows))
	go c.readLoop(pw)
	return c
}

func (c *Conn) sendRaw(b ...byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.nc.Write(b)
}

func (c *Conn) sendNAWS() {
	w, h := int(c.cols.Load()), int(c.rows.Load())
	if w <= 0 || h <= 0 {
		return
	}
	payload := []byte{byte(w >> 8), byte(w), byte(h >> 8), byte(h)}
	msg := []byte{iac, sb, optNAWS}
	for _, b := range payload {
		msg = append(msg, b)
		if b == iac {
			msg = append(msg, iac)
		}
	}
	msg = append(msg, iac, se)
	c.sendRaw(msg...)
}

func (c *Conn) handleCommand(cmd, opt byte) {
	switch cmd {
	case will:
		switch opt {
		case optEcho, optSGA, optBinary:
			if opt == optBinary {
				c.binary.Store(true)
			}
			c.sendRaw(iac, do, opt)
		default:
			c.sendRaw(iac, dont, opt)
		}
	case wont:
		if opt == optBinary {
			c.binary.Store(false)
		}
		c.sendRaw(iac, dont, opt)
	case do:
		switch opt {
		case optTType, optSGA, optBinary:
			c.sendRaw(iac, will, opt)
		case optNAWS:
			c.nawsOn.Store(true)
			c.sendRaw(iac, will, optNAWS)
			c.sendNAWS()
		default:
			c.sendRaw(iac, wont, opt)
		}
	case dont:
		if opt == optNAWS {
			c.nawsOn.Store(false)
		}
		c.sendRaw(iac, wont, opt)
	}
}

func (c *Conn) handleSub(data []byte) {
	if len(data) >= 2 && data[0] == optTType && data[1] == ttypeSend {
		msg := append([]byte{iac, sb, optTType, ttypeIs}, []byte(c.term)...)
		msg = append(msg, iac, se)
		c.sendRaw(msg...)
	}
}

func (c *Conn) readLoop(pw *io.PipeWriter) {
	r := bufio.NewReaderSize(c.nc, 32*1024)
	var out []byte
	flush := func() error {
		if len(out) == 0 {
			return nil
		}
		_, err := pw.Write(out)
		out = out[:0]
		return err
	}
	for {
		b, err := r.ReadByte()
		if err != nil {
			flush()
			pw.CloseWithError(err)
			return
		}
		if b != iac {
			out = append(out, b)
			if r.Buffered() == 0 || len(out) >= 16*1024 {
				if flush() != nil {
					return
				}
			}
			continue
		}
		cmd, err := r.ReadByte()
		if err != nil {
			flush()
			pw.CloseWithError(err)
			return
		}
		switch cmd {
		case iac:
			out = append(out, iac)
		case will, wont, do, dont:
			opt, err := r.ReadByte()
			if err != nil {
				flush()
				pw.CloseWithError(err)
				return
			}
			c.handleCommand(cmd, opt)
		case sb:
			var sub []byte
			for {
				x, err := r.ReadByte()
				if err != nil {
					flush()
					pw.CloseWithError(err)
					return
				}
				if x == iac {
					y, err := r.ReadByte()
					if err != nil {
						flush()
						pw.CloseWithError(err)
						return
					}
					if y == se {
						break
					}
					if y == iac {
						sub = append(sub, iac)
						continue
					}
					continue
				}
				sub = append(sub, x)
				if len(sub) > 4096 {
					break
				}
			}
			c.handleSub(sub)
		default:
			// NOP, GA, DM, etc: ignore
		}
		if r.Buffered() == 0 {
			if flush() != nil {
				return
			}
		}
	}
}

// Read returns data with telnet commands removed.
func (c *Conn) Read(p []byte) (int, error) { return c.out.Read(p) }

// Write escapes IAC and maps a bare CR to CR NUL (NVT rules).
func (c *Conn) Write(p []byte) (int, error) {
	buf := make([]byte, 0, len(p)+8)
	for i, b := range p {
		switch {
		case b == iac:
			buf = append(buf, iac, iac)
		case b == '\r' && !c.binary.Load() && (i+1 >= len(p) || p[i+1] != '\n'):
			buf = append(buf, '\r', 0)
		default:
			buf = append(buf, b)
		}
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.nc.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize updates NAWS if the server asked for it.
func (c *Conn) Resize(cols, rows int) error {
	c.cols.Store(int32(cols))
	c.rows.Store(int32(rows))
	if c.nawsOn.Load() {
		c.sendNAWS()
	}
	return nil
}

// SendBreak sends IAC BRK.
func (c *Conn) SendBreak() error {
	c.sendRaw(iac, brk)
	return nil
}

// Close closes the connection.
func (c *Conn) Close() error {
	var err error
	c.closeOne.Do(func() {
		err = c.nc.Close()
		c.out.Close()
	})
	return err
}
