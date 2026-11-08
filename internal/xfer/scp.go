// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package xfer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// scpRemote works with devices that have an SCP server and exec commands
// but no SFTP (Cisco IOS and IOS XE, NX-OS with sftp-server off).
type scpRemote struct{ l *Link }

// exec runs a command, on a new connection when the device closed the old.
func (s *scpRemote) exec(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	var out string
	err := s.l.Do(ctx, func(c *ssh.Client) error {
		var err error
		out, err = Exec(ctx, c, cmd, timeout)
		return err
	})
	return out, err
}

func (s *scpRemote) Kind() string { return "scp" }

// newChannel opens a session channel. Cisco IOS takes one per connection:
// while a transfer or a terminal holds it, say so instead of "resource
// shortage".
func newChannel(c *ssh.Client) (*ssh.Session, error) {
	s, err := c.NewSession()
	if err == nil {
		return s, nil
	}
	var oce *ssh.OpenChannelError
	if !errors.As(err, &oce) {
		// Not a refusal: the connection is gone (io.EOF, or a close that
		// crossed the channel request).
		return nil, fmt.Errorf("%w: %v", ErrConnClosed, err)
	}
	if oce.Reason == ssh.ResourceShortage {
		return nil, fmt.Errorf("the device is busy: it takes one channel per SSH connection, and a transfer or the terminal is using it (%w)", err)
	}
	return nil, err
}

// Exec runs one command on its own channel and returns what it printed.
func Exec(ctx context.Context, c *ssh.Client, cmd string, timeout time.Duration) (string, error) {
	sess, err := newChannel(c)
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out := &lockedBuffer{}
	sess.Stdout = out
	sess.Stderr = out
	done := make(chan error, 1)
	if err := sess.Start(cmd); err != nil {
		return "", err
	}
	go func() { done <- sess.Wait() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = sess.Close()
		return out.String(), ctx.Err()
	case <-t.C:
		_ = sess.Close()
		return out.String(), fmt.Errorf("%q did not finish within %s", cmd, timeout)
	}
	text := strings.ReplaceAll(out.String(), "\r", "")
	var exit *ssh.ExitError
	if errors.As(err, &exit) || errors.Is(err, io.EOF) {
		err = nil // devices often end exec channels without a clean status
		if exit != nil && exit.ExitStatus() != 0 && strings.TrimSpace(text) == "" {
			err = fmt.Errorf("%q failed (exit status %d)", cmd, exit.ExitStatus())
		}
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		err = nil
	}
	if err == nil && DeviceError(text) != "" {
		err = errors.New(DeviceError(text))
	}
	return text, err
}

// lockedBuffer collects stdout and stderr, which are copied concurrently.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// DeviceError picks the error line of a Cisco-like reply.
func DeviceError(out string) string {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		low := strings.ToLower(l)
		switch {
		case strings.HasPrefix(l, "% "), strings.HasPrefix(l, "%Error"), strings.HasPrefix(low, "invalid command"),
			strings.HasPrefix(low, "syntax error"), strings.HasPrefix(low, "no such file"):
			return strings.TrimLeft(l, "% ")
		}
	}
	return ""
}

func (s *scpRemote) Home(ctx context.Context) string {
	out, err := s.exec(ctx, "dir", 20*time.Second)
	if err == nil {
		if m := reDirOf.FindStringSubmatch(out); m != nil {
			return cleanDirName(m[1])
		}
		if m := reNXUsage.FindStringSubmatch(out); m != nil {
			return m[1] + ":"
		}
	}
	return "flash:"
}

// cleanDirName turns "flash:/" into "flash:" and "bootflash:/scripts/" into
// "bootflash:scripts".
func cleanDirName(d string) string {
	d = strings.TrimSpace(d)
	if i := strings.Index(d, ":"); i > 0 {
		rest := strings.Trim(d[i+1:], "/")
		return d[:i+1] + rest
	}
	return d
}

var (
	reDirOf   = regexp.MustCompile(`(?m)^Directory of (\S+)`)
	reNXUsage = regexp.MustCompile(`(?m)^Usage for (\w+):`)
	// IOS / IOS XE: "  3  -rw-   33591768  Mar 1 1993 00:04:20 +00:00  c2960.bin"
	reIOSLine = regexp.MustCompile(`^\s*\d+\s+([-a-z]{4,10})\s+(\d+)\s+(<no date>|[A-Z][a-z]{2}\s+\d+\s+\d{4}\s+[\d:.]+(?:\s+[+-]\d\d:?\d\d)?|[A-Z][a-z]{2}\s+\d+\s+[\d:]+\s+\d{4}(?:\s+\w+)?)\s+(.+?)\s*$`)
	// NX-OS: "   33591768    Oct 20 09:12:33 2026  nxos.bin"
	reNXLine  = regexp.MustCompile(`^\s*(\d+)\s+([A-Z][a-z]{2}\s+\d+\s+[\d:]+\s+\d{4})\s+(.+?)\s*$`)
	reIOSFree = regexp.MustCompile(`(\d+) bytes total \((\d+) bytes free\)`)
	// Huawei VRP: "  10  -rw-    328,071,444  Nov 23 2023 13:10:46   NAME",
	// folders with "-" as size; "1,982,464 KB total (488,547 KB free)".
	reVRPLine  = regexp.MustCompile(`^\s*\d+\s+([-a-z]{4,10})\s+([\d,]+|-)\s+([A-Z][a-z]{2}\s+\d+\s+\d{4}\s+[\d:]+)\s+(.+?)\s*$`)
	reVRPFree  = regexp.MustCompile(`([\d,]+) KB total \(([\d,]+) KB free\)`)
	reNXFree   = regexp.MustCompile(`(?m)^\s*(\d+) bytes free`)
	reMD5      = regexp.MustCompile(`\b[0-9a-fA-F]{32}\b`)
	dateLayout = []string{"Jan 2 2006 15:04:05 -07:00", "Jan 2 2006 15:04:05.000000000 -07:00", "Jan 2 2006 15:04:05", "Jan 2 15:04:05 2006", "Jan 2 15:04:05 2006 MST"}
)

func parseDate(s string) time.Time {
	s = strings.Join(strings.Fields(s), " ")
	for _, l := range dateLayout {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ParseDir reads the output of "dir <dir>" on Cisco IOS, IOS XE and NX-OS.
func ParseDir(dir, out string) ([]Entry, int64) {
	var es []Entry
	free := int64(-1)
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if m := reIOSLine.FindStringSubmatch(l); m != nil {
			size, _ := strconv.ParseInt(m[2], 10, 64)
			name := m[4]
			isDir := strings.HasPrefix(m[1], "d")
			es = append(es, Entry{Name: name, Path: Join(dir, name), Size: size, Dir: isDir, Mod: parseDate(m[3])})
			continue
		}
		if m := reVRPLine.FindStringSubmatch(l); m != nil {
			size, _ := strconv.ParseInt(strings.ReplaceAll(m[2], ",", ""), 10, 64)
			name := m[4]
			es = append(es, Entry{Name: name, Path: Join(dir, name), Size: size, Dir: strings.HasPrefix(m[1], "d"), Mod: parseDate(m[3])})
			continue
		}
		if m := reNXLine.FindStringSubmatch(l); m != nil {
			size, _ := strconv.ParseInt(m[1], 10, 64)
			name := m[3]
			isDir := strings.HasSuffix(name, "/")
			name = strings.TrimSuffix(name, "/")
			es = append(es, Entry{Name: name, Path: Join(dir, name), Size: size, Dir: isDir, Mod: parseDate(m[2])})
			continue
		}
		if m := reIOSFree.FindStringSubmatch(l); m != nil {
			free, _ = strconv.ParseInt(m[2], 10, 64)
		} else if m := reVRPFree.FindStringSubmatch(l); m != nil {
			kb, _ := strconv.ParseInt(strings.ReplaceAll(m[2], ",", ""), 10, 64)
			free = kb * 1024
		} else if m := reNXFree.FindStringSubmatch(l); m != nil {
			free, _ = strconv.ParseInt(m[1], 10, 64)
		}
	}
	SortEntries(es)
	return es, free
}

func (s *scpRemote) dir(ctx context.Context, dir string) ([]Entry, int64, error) {
	arg := dir
	if IsDevicePath(dir) && !strings.HasSuffix(dir, ":") {
		arg = dir + "/"
	}
	out, err := s.exec(ctx, "dir "+arg, 30*time.Second)
	if err != nil {
		return nil, -1, err
	}
	es, free := ParseDir(dir, out)
	if len(es) == 0 && !reDirOf.MatchString(out) && !strings.Contains(out, "bytes free") && !strings.Contains(out, "KB free") {
		return nil, -1, fmt.Errorf("could not read the folder %s: %s", dir, firstLine(out))
	}
	return es, free, nil
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "no reply"
}

func (s *scpRemote) List(ctx context.Context, dir string) ([]Entry, error) {
	es, _, err := s.dir(ctx, dir)
	return es, err
}

func (s *scpRemote) Free(ctx context.Context, p string) int64 {
	d := p
	if i := strings.Index(p, ":"); i > 0 && IsDevicePath(p) {
		d = p[:i+1]
	}
	_, free, err := s.dir(ctx, d)
	if err != nil {
		return -1
	}
	return free
}

func (s *scpRemote) Stat(ctx context.Context, p string) (Entry, error) {
	parent := Parent(p)
	name := strings.TrimPrefix(strings.TrimPrefix(p, parent), "/")
	es, _, err := s.dir(ctx, parent)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range es {
		if e.Name == name {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("%s: %w", p, ErrNoSuchFile)
}

func (s *scpRemote) Remove(ctx context.Context, p string) error {
	out, err := s.exec(ctx, "delete /force "+p, 60*time.Second)
	if err != nil && strings.Contains(strings.ToLower(err.Error()+out), "invalid") {
		_, err = s.exec(ctx, "delete "+p+" no-prompt", 60*time.Second) // NX-OS
	}
	return err
}

func (s *scpRemote) Mkdir(context.Context, string) error          { return ErrNotSupported }
func (s *scpRemote) Rename(context.Context, string, string) error { return ErrNotSupported }
func (s *scpRemote) Close() error                                 { return nil }

func (s *scpRemote) Get(ctx context.Context, p string, w io.Writer, progress func(int64)) error {
	return s.l.Do(ctx, func(c *ssh.Client) error { return SCPGet(ctx, c, p, w, progress) })
}

func (s *scpRemote) Put(ctx context.Context, p string, r io.Reader, size int64, progress func(int64)) error {
	return s.l.Do(ctx, func(c *ssh.Client) error { return SCPPut(ctx, c, p, r, size, progress) })
}

// scpAck reads one SCP reply: 0 is fine, 1 and 2 carry an error message.
func scpAck(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("the SCP server closed the connection: %w", err)
	}
	if b == 0 {
		return nil
	}
	msg, _ := r.ReadString('\n')
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "the SCP server refused the transfer"
	}
	return deviceSCPError(msg)
}

// deviceSCPError adds what to do to the short SCP refusals of Cisco IOS.
func deviceSCPError(msg string) error {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "administratively disabled"):
		return fmt.Errorf("%s The SCP server is off on the device. On Cisco IOS: ip scp server enable (with AAA also aaa authorization exec)", msg)
	case strings.Contains(low, "permission denied"):
		return fmt.Errorf("%s The user may not copy with SCP. On Cisco IOS: privilege 15, or aaa authorization exec for this user", msg)
	}
	return errors.New(msg)
}

// quoteSCP quotes a Unix path for the remote shell; device paths go as is.
func quoteSCP(p string) string {
	if IsDevicePath(p) {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// SCPGet copies one remote file (scp -f).
func SCPGet(ctx context.Context, c *ssh.Client, p string, w io.Writer, progress func(int64)) error {
	sess, err := newChannel(c)
	if err != nil {
		return err
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()
	in, _ := sess.StdinPipe()
	outPipe, _ := sess.StdoutPipe()
	var stderr lockedBuffer // written by the session while we read it
	sess.Stderr = &stderr
	if err := sess.Start("scp -f " + quoteSCP(p)); err != nil {
		return err
	}
	r := bufio.NewReaderSize(outPipe, 64<<10)
	// A device that refuses (Cisco IOS without its SCP server) may have
	// closed the channel already: its reason is still in the output.
	_, _ = in.Write([]byte{0})
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return scpFailure(stderr.String(), line, err)
		}
		switch line[0] {
		case 'T':
			_, _ = in.Write([]byte{0})
			continue
		case 1, 2:
			return deviceSCPError(strings.TrimSpace(line[1:]))
		case 'C':
		default:
			return scpFailure(stderr.String(), line, errors.New("unexpected reply"))
		}
		f := strings.SplitN(strings.TrimSpace(line), " ", 3)
		if len(f) < 3 {
			return fmt.Errorf("unexpected SCP header %q", line)
		}
		size, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return fmt.Errorf("unexpected SCP header %q", line)
		}
		_, _ = in.Write([]byte{0})
		cw := &countingWriter{w: w, tick: progress}
		if _, err := io.CopyN(cw, r, size); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if progress != nil {
			progress(cw.n)
		}
		if err := scpAck(r); err != nil {
			return err
		}
		_, _ = in.Write([]byte{0})
		_ = in.Close()
		_ = sess.Wait()
		return nil
	}
}

func scpFailure(stderr, line string, err error) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(line)
	}
	if msg == "" {
		return fmt.Errorf("the device did not start an SCP transfer (is its SCP server on? On Cisco IOS: ip scp server enable): %v", err)
	}
	return deviceSCPError(msg)
}

// SCPPut writes one remote file (scp -t).
func SCPPut(ctx context.Context, c *ssh.Client, p string, src io.Reader, size int64, progress func(int64)) error {
	sess, err := newChannel(c)
	if err != nil {
		return err
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()
	in, _ := sess.StdinPipe()
	outPipe, _ := sess.StdoutPipe()
	var stderr lockedBuffer // written by the session while we read it
	sess.Stderr = &stderr
	if err := sess.Start("scp -t " + quoteSCP(p)); err != nil {
		return err
	}
	r := bufio.NewReader(outPipe)
	if err := scpAck(r); err != nil {
		return scpFailure(stderr.String(), "", err)
	}
	name := p
	if i := strings.LastIndexAny(p, "/:"); i >= 0 {
		name = p[i+1:]
	}
	if _, err := fmt.Fprintf(in, "C0644 %d %s\n", size, name); err != nil {
		return err
	}
	if err := scpAck(r); err != nil {
		return err
	}
	cr := &countingReader{r: src, tick: progress, ctx: ctx}
	n, err := io.CopyN(in, cr, size)
	if progress != nil {
		progress(cr.n)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sent %d of %d bytes: %w", n, size, err)
	}
	if _, err := in.Write([]byte{0}); err != nil {
		return err
	}
	if err := scpAck(r); err != nil {
		return err
	}
	_ = in.Close()
	_ = sess.Wait()
	return nil
}

// MD5 asks the device for the MD5 checksum of a file: verify /md5 (IOS),
// show file … md5sum (NX-OS), md5sum (Linux), file checksum md5 (Junos),
// verify /md5 file:<path> (Arista EOS, whose exec channel is its CLI while
// SFTP shows Linux paths).
func MD5(ctx context.Context, c *ssh.Client, p string) (string, error) {
	return md5Cmds(ctx, p, func(cmd string) (string, error) { return Exec(ctx, c, cmd, 10*time.Minute) })
}

// MD5 asks the device for a file's MD5 checksum, each command on a new
// connection when the device closed the old one.
func (l *Link) MD5(ctx context.Context, p string) (string, error) {
	return md5Cmds(ctx, p, func(cmd string) (string, error) {
		var out string
		err := l.Do(ctx, func(c *ssh.Client) error {
			var err error
			out, err = Exec(ctx, c, cmd, 10*time.Minute)
			return err
		})
		return out, err
	})
}

func md5Cmds(ctx context.Context, p string, exec func(cmd string) (string, error)) (string, error) {
	var cmds []string
	if IsDevicePath(p) {
		cmds = []string{"verify /md5 " + p, "show file " + p + " md5sum"}
	} else {
		cmds = []string{"md5sum -- " + quoteSCP(p), "md5 -q " + quoteSCP(p), "file checksum md5 " + p, "verify /md5 file:" + p}
	}
	var last error
	for _, cmd := range cmds {
		out, err := exec(cmd)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if m := reMD5.FindString(out); m != "" && err == nil {
			return strings.ToLower(m), nil
		}
		if err == nil {
			err = fmt.Errorf("%s: %s", cmd, firstLine(out))
		}
		last = err
	}
	return "", fmt.Errorf("the device did not report an MD5 checksum: %v", last)
}

// reFSLine is a "show file systems" row: size, free, type, flags, prefixes.
var reFSLine = regexp.MustCompile(`^\*?\s*(\d+|-)\s+(\d+|-)\s+(\w+)\s+(\w+)\s+(.+?)\s*$`)

// ParseFileSystems reads "show file systems" (IOS, IOS XE) and returns the
// writable disks and flashes, the default one first.
func ParseFileSystems(out string) []Root {
	var roots []Root
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		m := reFSLine.FindStringSubmatch(l)
		if m == nil || !strings.Contains(m[4], "rw") {
			continue
		}
		switch m[3] {
		case "disk", "flash", "usbflash":
		default:
			continue
		}
		size, err1 := strconv.ParseInt(m[1], 10, 64)
		free, err2 := strconv.ParseInt(m[2], 10, 64)
		if err1 != nil {
			size = -1
		}
		if err2 != nil {
			free = -1
		}
		prefix := strings.Fields(m[5])[0]
		// "bootflash: flash:" names one disk twice; show the short alias.
		for _, p := range strings.Fields(m[5]) {
			if p == "flash:" {
				prefix = p
			}
		}
		r := Root{Path: prefix, Size: size, Free: free}
		if strings.HasPrefix(strings.TrimSpace(l), "*") {
			roots = append([]Root{r}, roots...)
		} else {
			roots = append(roots, r)
		}
	}
	return roots
}

func (s *scpRemote) Roots(ctx context.Context) []Root {
	if out, err := s.exec(ctx, "show file systems", 20*time.Second); err == nil {
		if roots := ParseFileSystems(out); len(roots) > 0 {
			return roots
		}
	}
	home := s.Home(ctx)
	return []Root{{Path: home, Size: -1, Free: s.Free(ctx, home)}}
}

// ExecTo runs cmd and streams what it prints to w, for long outputs such
// as show tech-support. It ends when the device closes the channel or ctx
// ends; idle stops it after that long without output (0: no limit).
func ExecTo(ctx context.Context, c *ssh.Client, cmd string, w io.Writer, progress func(int64), idle time.Duration) (int64, error) {
	sess, err := newChannel(c)
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()
	out, _ := sess.StdoutPipe()
	errOut := &lockedBuffer{}
	sess.Stderr = errOut
	if err := sess.Start(cmd); err != nil {
		return 0, err
	}
	var idleTimer *time.Timer
	var timedOut atomic.Bool
	if idle > 0 {
		idleTimer = time.AfterFunc(idle, func() { timedOut.Store(true); _ = sess.Close() })
		defer idleTimer.Stop()
	}
	cw := &countingWriter{w: w, tick: func(n int64) {
		if progress != nil {
			progress(n)
		}
	}}
	buf := make([]byte, 32<<10)
	for {
		n, rerr := out.Read(buf)
		if n > 0 {
			if idleTimer != nil {
				idleTimer.Reset(idle)
			}
			if _, werr := cw.Write(buf[:n]); werr != nil {
				return cw.n, werr
			}
		}
		if rerr != nil {
			break
		}
	}
	_ = sess.Wait()
	if e := errOut.String(); e != "" {
		_, _ = cw.Write([]byte(e))
	}
	if progress != nil {
		progress(cw.n)
	}
	if ctx.Err() != nil {
		return cw.n, ctx.Err()
	}
	if timedOut.Load() {
		return cw.n, fmt.Errorf("no output for %s, stopped", idle)
	}
	return cw.n, nil
}
