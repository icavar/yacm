// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package sshconn

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

// A telnet session behind a jump host: DialTCP opens a forwarded TCP
// connection from the jump host to the device.
func TestDialTCPThroughJumpHost(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				_, _ = c.Write([]byte("echo: " + line))
			}()
		}
	}()
	jump := startServer(t, passwordServer(t, newSigner(t), "jumppw"), true)
	cfg := Config{
		Hops:       []Hop{{Host: jump.host, Port: jump.port, User: "admin", Password: "jumppw", Auth: store.AuthPassword}},
		KnownHosts: NewKnownHosts(filepath.Join(t.TempDir(), "kh"), false), Prompter: &fakePrompter{acceptKey: true},
	}
	tun, err := DialTCP(context.Background(), cfg, echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tun.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	got, _ := bufio.NewReader(tun).ReadString('\n')
	if got != "echo: hello\n" {
		t.Fatalf("got %q", got)
	}
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}

	// A jump host that does not forward gives a clear message.
	closed := startServer(t, passwordServer(t, newSigner(t), "jumppw"), false)
	cfg.Hops[0].Host, cfg.Hops[0].Port = closed.host, closed.port
	_, err = DialTCP(context.Background(), cfg, echo.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "AllowTcpForwarding") {
		t.Fatalf("expected a forwarding hint, got %v", err)
	}
}
