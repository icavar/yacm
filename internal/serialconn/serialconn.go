// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package serialconn opens serial console lines (COMx, /dev/ttyUSB*, …).
package serialconn

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"

	"github.com/icavar/yacm/internal/store"
)

// Conn is an open serial port.
type Conn struct {
	port      serial.Port
	closeOnce sync.Once
	closed    chan struct{}
}

// Open opens the port described by s.
func Open(s store.Serial) (*Conn, error) {
	mode := &serial.Mode{BaudRate: s.Baud, DataBits: s.DataBits}
	switch strings.ToLower(s.Parity) {
	case "", "none":
		mode.Parity = serial.NoParity
	case "odd":
		mode.Parity = serial.OddParity
	case "even":
		mode.Parity = serial.EvenParity
	case "mark":
		mode.Parity = serial.MarkParity
	case "space":
		mode.Parity = serial.SpaceParity
	default:
		return nil, fmt.Errorf("unknown parity %q", s.Parity)
	}
	switch s.StopBits {
	case "", "1":
		mode.StopBits = serial.OneStopBit
	case "1.5":
		mode.StopBits = serial.OnePointFiveStopBits
	case "2":
		mode.StopBits = serial.TwoStopBits
	default:
		return nil, fmt.Errorf("unknown stop bits %q", s.StopBits)
	}
	if mode.BaudRate == 0 {
		mode.BaudRate = 9600
	}
	if mode.DataBits == 0 {
		mode.DataBits = 8
	}
	p, err := serial.Open(s.Device, mode)
	if err != nil {
		var pe *serial.PortError
		if errors.As(err, &pe) && pe.Code() == serial.PortBusy {
			return nil, fmt.Errorf("%s is in use by another program", s.Device)
		}
		return nil, fmt.Errorf("open %s: %w", s.Device, err)
	}
	// A read timeout lets Close interrupt a blocked Read on every platform.
	_ = p.SetReadTimeout(250 * time.Millisecond)
	return &Conn{port: p, closed: make(chan struct{})}, nil
}

// Read blocks until data arrives or the port is closed.
func (c *Conn) Read(b []byte) (int, error) {
	for {
		n, err := c.port.Read(b)
		if n > 0 || err != nil {
			select {
			case <-c.closed:
				if n == 0 {
					return 0, errors.New("port closed")
				}
			default:
			}
			return n, err
		}
		select {
		case <-c.closed:
			return 0, errors.New("port closed")
		default:
		}
	}
}

// Write sends data to the line.
func (c *Conn) Write(b []byte) (int, error) { return c.port.Write(b) }

// Resize is a no-op: serial lines have no window size.
func (c *Conn) Resize(cols, rows int) error { return nil }

// SendBreak holds the line in break state for 500 ms (Cisco ROMMON etc.).
func (c *Conn) SendBreak() error { return c.port.Break(500 * time.Millisecond) }

// Close closes the port.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.port.Close()
	})
	return err
}

// PortInfo describes an available serial port.
type PortInfo struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	IsUSB   bool   `json:"isUsb"`
	VIDPID  string `json:"vidpid,omitempty"`
	Product string `json:"product,omitempty"`
}

// List returns the serial ports present on this machine.
func List() ([]PortInfo, error) {
	var out []PortInfo
	details, err := enumerator.GetDetailedPortsList()
	if err == nil && len(details) > 0 {
		for _, d := range details {
			pi := PortInfo{Name: d.Name, Label: d.Name, IsUSB: d.IsUSB, Product: d.Product}
			if d.IsUSB {
				pi.VIDPID = d.VID + ":" + d.PID
				if d.Product != "" {
					pi.Label = d.Name + " — " + d.Product
				} else {
					pi.Label = d.Name + " — USB " + pi.VIDPID
				}
			}
			out = append(out, pi)
		}
	} else {
		names, err := serial.GetPortsList()
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, PortInfo{Name: n, Label: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsUSB != out[j].IsUSB {
			return out[i].IsUSB
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
