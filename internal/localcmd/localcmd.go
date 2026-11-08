// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package localcmd runs the "before connect" / "after disconnect" commands
// of a session on the local machine.
package localcmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// Env describes the session the command runs for; exposed as YACM_* vars.
type Env struct {
	Name string
	Host string
	Port int
	User string
}

// Result of one command.
type Result struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

const maxOutput = 64 * 1024

// Run executes cmdline through the platform shell with a timeout.
func Run(ctx context.Context, cmdline string, env Env, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", cmdline)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	}
	cmd.Env = append(os.Environ(),
		"YACM_NAME="+env.Name,
		"YACM_HOST="+env.Host,
		"YACM_PORT="+strconv.Itoa(env.Port),
		"YACM_USER="+env.User,
	)
	hideWindow(cmd)
	// When the time is up the shell is killed; a program it started may
	// still hold the output open, so stop waiting for it shortly after.
	cmd.WaitDelay = 2 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &limitWriter{w: &buf, n: maxOutput}
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	r := Result{Command: cmdline, Output: buf.String()}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			r.ExitCode = ee.ExitCode()
		} else {
			r.ExitCode = -1
		}
		r.Error = err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			r.Error = "timed out after " + timeout.String()
		}
	}
	return r
}

type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
