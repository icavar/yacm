// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package xfer moves files between this computer and a device over an SSH
// connection (through its jump hosts): SFTP where the device has it, SCP
// plus exec commands (dir, delete, verify /md5) on network devices that
// only have an SCP server, such as Cisco IOS.
package xfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Entry is a file or folder on a device or on this computer.
type Entry struct {
	Name string    `json:"name"`
	Path string    `json:"path"`
	Size int64     `json:"size"`
	Dir  bool      `json:"dir"`
	Mod  time.Time `json:"mod"`
}

// Root is a file system of a device. Size and Free are -1 when unknown.
type Root struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Free int64  `json:"free"`
}

// Remote is the file system of a device.
type Remote interface {
	// Kind is "sftp" or "scp".
	Kind() string
	// Home is where browsing starts (a home folder, or "flash:").
	Home(ctx context.Context) string
	List(ctx context.Context, dir string) ([]Entry, error)
	Stat(ctx context.Context, p string) (Entry, error)
	Get(ctx context.Context, p string, w io.Writer, progress func(int64)) error
	Put(ctx context.Context, p string, r io.Reader, size int64, progress func(int64)) error
	Remove(ctx context.Context, p string) error
	Mkdir(ctx context.Context, p string) error
	Rename(ctx context.Context, from, to string) error
	// Free reports the free space where p lives, -1 when unknown.
	Free(ctx context.Context, p string) int64
	// Roots lists the file systems to browse (flash:, bootflash:, usb0: …,
	// or / and the home folder).
	Roots(ctx context.Context) []Root
	Close() error
}

// ErrNotSupported is returned for an operation a device cannot do.
var ErrNotSupported = errors.New("the device does not support this")

// ErrNoSuchFile is returned by Stat when the file is not in its folder.
var ErrNoSuchFile = errors.New("no such file")

// Open picks SFTP when the device offers it, else SCP with exec commands.
func Open(l *Link) (Remote, error) {
	r, _ := OpenWhy(l)
	return r, nil
}

// OpenWhy is Open that also says why SFTP was not used ("" with SFTP).
func OpenWhy(l *Link) (Remote, string) {
	r, err := openSFTP(l.Client())
	if err == nil {
		return r, ""
	}
	return &scpRemote{l: l}, err.Error()
}

// sftpStart limits how long a device may take to answer the start of
// SFTP: some accept the subsystem and then say nothing (Huawei VRP for a
// user without SFTP rights), which would hang the file browser.
var sftpStart = 5 * time.Second

// openSFTP starts the sftp subsystem on a channel of its own and closes
// that channel when the device refuses: Cisco IOS takes one session channel
// per connection, and a channel left open blocks everything after it.
// (sftp.NewClient keeps the channel open when the subsystem is refused.)
func openSFTP(c *ssh.Client) (Remote, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (Remote, error) {
		_ = s.Close()
		return nil, err
	}
	w, err := s.StdinPipe()
	if err != nil {
		return fail(err)
	}
	r, err := s.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	type result struct {
		c   *sftp.Client
		err error
	}
	done := make(chan result, 1)
	go func() {
		if err := s.RequestSubsystem("sftp"); err != nil {
			done <- result{err: err}
			return
		}
		sc, err := sftp.NewClientPipe(r, w, sftp.UseConcurrentWrites(true), sftp.UseConcurrentReads(true), sftp.MaxConcurrentRequestsPerFile(64))
		done <- result{sc, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			return fail(res.err)
		}
		return &sftpRemote{c: res.c, s: s}, nil
	case <-time.After(sftpStart):
		_ = s.Close() // ends the goroutine: its read fails
		return nil, fmt.Errorf("no SFTP answer within %v", sftpStart)
	}
}

// OpenSCP uses SCP even when SFTP is there (some devices, such as Cisco
// NX-OS with sftp-server off, answer the subsystem but fail later).
func OpenSCP(l *Link) Remote { return &scpRemote{l: l} }

// IsDevicePath reports a network device path such as flash:dir/file.
func IsDevicePath(p string) bool {
	i := strings.Index(p, ":")
	return i > 0 && !strings.Contains(p[:i], "/")
}

// Join joins a folder and a name, for device (flash:) and Unix paths.
func Join(dir, name string) string {
	if dir == "" {
		return name
	}
	if IsDevicePath(dir) {
		if strings.HasSuffix(dir, ":") || strings.HasSuffix(dir, "/") {
			return dir + name
		}
		return dir + "/" + name
	}
	return path.Join(dir, name)
}

// Parent returns the folder above p ("flash:" stays "flash:").
func Parent(p string) string {
	if IsDevicePath(p) {
		i := strings.Index(p, ":")
		rest := strings.Trim(p[i+1:], "/")
		if rest == "" {
			return p[:i+1]
		}
		if j := strings.LastIndex(rest, "/"); j >= 0 {
			return p[:i+1] + rest[:j]
		}
		return p[:i+1]
	}
	if p == "/" || p == "" {
		return "/"
	}
	return path.Dir(strings.TrimRight(p, "/"))
}

// counting wraps progress reporting.
type countingWriter struct {
	w    io.Writer
	n    int64
	tick func(int64)
	last time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.tick != nil && time.Since(c.last) > 150*time.Millisecond {
		c.last = time.Now()
		c.tick(c.n)
	}
	return n, err
}

type countingReader struct {
	r    io.Reader
	n    int64
	tick func(int64)
	last time.Time
	ctx  context.Context
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.ctx != nil && c.ctx.Err() != nil {
		return 0, c.ctx.Err()
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.tick != nil && time.Since(c.last) > 150*time.Millisecond {
		c.last = time.Now()
		c.tick(c.n)
	}
	return n, err
}

// ctxWriter stops a copy when the context ends.
type ctxWriter struct {
	ctx context.Context
	w   io.Writer
}

func (c ctxWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.w.Write(p)
}
