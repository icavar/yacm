// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build windows

package sshconn

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/crypto/ssh/agent"
)

// openAgent connects to the Windows OpenSSH Authentication Agent service
// (or another agent exposing the same named pipe, e.g. 1Password, KeePassXC,
// or a Pageant build with OpenSSH pipe support). SSH_AUTH_SOCK may point to
// a different pipe or unix socket.
func openAgent() (agent.ExtendedAgent, io.Closer, error) {
	paths := []string{}
	if s := os.Getenv("SSH_AUTH_SOCK"); s != "" {
		paths = append(paths, s)
	}
	paths = append(paths, `\\.\pipe\openssh-ssh-agent`)
	var lastErr error
	for _, p := range paths {
		var c net.Conn
		var err error
		if strings.HasPrefix(p, `\\.\pipe\`) {
			to := 2 * time.Second
			c, err = winio.DialPipe(p, &to)
		} else {
			c, err = net.Dial("unix", p)
		}
		if err == nil {
			return agent.NewClient(c), c, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no agent")
	}
	return nil, nil, lastErr
}
