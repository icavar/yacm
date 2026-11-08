// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package telnet

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestNegotiationAndData(t *testing.T) {
	client, server := net.Pipe()
	c := NewFromConn(client, 100, 30)
	defer c.Close()
	got := make(chan []byte, 1)
	go func() {
		// Server asks for terminal type and window size, then sends data
		// containing an escaped 0xFF byte.
		server.Write([]byte{iac, do, optTType, iac, do, optNAWS, iac, will, optEcho})
		buf := make([]byte, 256)
		var all []byte
		server.SetReadDeadline(time.Now().Add(2 * time.Second))
		for !bytes.Contains(all, []byte{iac, sb, optNAWS}) || !bytes.Contains(all, []byte{iac, do, optEcho}) {
			n, err := server.Read(buf)
			if err != nil {
				break
			}
			all = append(all, buf[:n]...)
		}
		server.Write([]byte{iac, sb, optTType, ttypeSend, iac, se})
		n, _ := server.Read(buf)
		all = append(all, buf[:n]...)
		server.Write([]byte{'h', 'i', iac, iac, '!'})
		got <- all
	}()
	data := make([]byte, 16)
	var out []byte
	for len(out) < 4 {
		n, err := c.Read(data)
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		out = append(out, data[:n]...)
	}
	if string(out) != "hi\xff!" {
		t.Fatalf("data %q", out)
	}
	neg := <-got
	for _, want := range [][]byte{{iac, will, optTType}, {iac, will, optNAWS}, {iac, sb, optNAWS, 0, 100, 0, 30, iac, se}, {iac, do, optEcho}, append([]byte{iac, sb, optTType, ttypeIs}, []byte("XTERM-256COLOR")...)} {
		if !bytes.Contains(neg, want) {
			t.Fatalf("missing %v in %v", want, neg)
		}
	}
}

func TestWriteEscaping(t *testing.T) {
	client, server := net.Pipe()
	c := NewFromConn(client, 80, 24)
	defer c.Close()
	go c.Write([]byte("a\r\xffb\r\n"))
	buf := make([]byte, 32)
	server.SetReadDeadline(time.Now().Add(time.Second))
	n, _ := server.Read(buf)
	if !bytes.Equal(buf[:n], []byte{'a', '\r', 0, iac, iac, 'b', '\r', '\n'}) {
		t.Fatalf("%v", buf[:n])
	}
}
