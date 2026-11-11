// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package fakedevice is the file side of a pretend Cisco IOS XE device: a
// "flash:" file system kept in a local folder, with the exec commands and
// SCP server that file transfers use (dir, verify /md5, delete, show file
// systems, scp -t / -f). The demo device and the tests use it.
package fakedevice

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yacm-app/yacm/internal/xfer"
)

// Total is the pretend size of flash:.
const Total = 7897796608

// FS is a flash: file system in Root.
type FS struct {
	Root string
	// Log is told about uploads and downloads (optional).
	Log func(format string, args ...any)
}

func (fs *FS) logf(format string, args ...any) {
	if fs.Log != nil {
		fs.Log(format, args...)
	}
}

// Seed puts a few typical files into an empty flash.
func (fs *FS) Seed(host string) error {
	if err := os.MkdirAll(fs.Root, 0o755); err != nil {
		return err
	}
	if ents, _ := os.ReadDir(fs.Root); len(ents) > 0 {
		return nil
	}
	img := make([]byte, 3<<20)
	for i := range img {
		img[i] = byte(i*7 + i>>9)
	}
	files := map[string][]byte{
		"c8000v-universalk9.17.12.04.SPA.bin": img,
		"packages.conf":                       []byte("#! /usr/binos/bin/packages_conf.sh\nsha1sum: 0000000000000000000000000000000000000000\nboot rp 0 0 rp_boot c8000v-rpboot.17.12.04.SPA.pkg\n"),
		"vlan.dat":                            make([]byte, 3384),
		"pnp-tech-time":                       []byte("1761000000\n"),
		"startup-config":                      []byte("!\nhostname " + host + "\n!\ninterface GigabitEthernet1\n ip address 10.0.0.1 255.255.255.0\n!\nend\n"),
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(fs.Root, n), b, 0o644); err != nil {
			return err
		}
	}
	_ = os.MkdirAll(filepath.Join(fs.Root, "tracelogs"), 0o755)
	_ = os.WriteFile(filepath.Join(fs.Root, "tracelogs", "IOSRP_R0-0.log"), []byte("demo trace log\n"), 0o644)
	return nil
}

// local maps a device path (flash:dir/file, bootflash:…, or a bare name)
// to a file in Root.
func (fs *FS) local(p string) (string, error) {
	if i := strings.Index(p, ":"); i > 0 {
		switch strings.ToLower(p[:i]) {
		case "flash", "bootflash", "harddisk":
		default:
			return "", fmt.Errorf("%%Error opening %s (Invalid argument)", p)
		}
	}
	full, err := xfer.InRoot(fs.Root, p, "")
	if err != nil && strings.Contains(err.Error(), "not a file") {
		rest := p
		if i := strings.Index(p, ":"); i > 0 {
			rest = p[i+1:]
		}
		return filepath.Join(fs.Root, filepath.FromSlash(filepath.Clean("/"+rest))), nil
	}
	return full, err
}

func (fs *FS) used() int64 {
	var n int64
	_ = filepath.Walk(fs.Root, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

func devName(p string) (prefix, rest string) {
	prefix = "flash:"
	if i := strings.Index(p, ":"); i > 0 {
		prefix, rest = p[:i+1], p[i+1:]
	} else {
		rest = p
	}
	rest = strings.Trim(filepath.ToSlash(filepath.Clean("/"+rest)), "/")
	return prefix, rest
}

// Dir is the output of "dir <p>".
func (fs *FS) Dir(p string) string {
	if p == "" {
		p = "flash:"
	}
	local, err := fs.local(p)
	if err != nil {
		return err.Error() + "\n"
	}
	prefix, rest := devName(p)
	st, err := os.Stat(local)
	if err != nil {
		return fmt.Sprintf("%%Error opening %s%s (No such file or directory)\n", prefix, "/"+rest)
	}
	var ents []os.FileInfo
	shown := "/" + rest
	if st.IsDir() {
		des, _ := os.ReadDir(local)
		for _, d := range des {
			if fi, err := d.Info(); err == nil && !strings.HasSuffix(d.Name(), ".part") {
				ents = append(ents, fi)
			}
		}
		if rest != "" {
			shown += "/"
		}
	} else {
		ents = []os.FileInfo{st}
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	var b strings.Builder
	fmt.Fprintf(&b, "Directory of %s%s\n\n", prefix, shown)
	if len(ents) == 0 {
		b.WriteString("No files in directory\n")
	}
	for i, fi := range ents {
		perm, size := "-rw-", fi.Size()
		if fi.IsDir() {
			perm, size = "drwx", 4096
		}
		fmt.Fprintf(&b, "%6d  %s %12d  %s  %s\n", 16*(i+1)+3, perm, size, fi.ModTime().UTC().Format("Jan 2 2006 15:04:05 -07:00"), fi.Name())
	}
	used := fs.used()
	fmt.Fprintf(&b, "\n%d bytes total (%d bytes free)\n", int64(Total), int64(Total)-used)
	return b.String()
}

// FileSystems is the output of "show file systems".
func (fs *FS) FileSystems() string {
	return fmt.Sprintf("File Systems:\n\n       Size(b)       Free(b)      Type  Flags  Prefixes\n"+
		"*  %11d   %11d      disk     rw   bootflash: flash:\n"+
		"             -             -    opaque     rw   system:\n"+
		"      33554432      33539614     nvram     rw   nvram:\n"+
		"             -             -   network     rw   tftp:\n"+
		"             -             -   network     rw   scp:\n"+
		"             -             -   network     rw   http:\n"+
		"             -             -   network     rw   https:\n", int64(Total), int64(Total)-fs.used())
}

// MD5 returns the checksum of a device file.
func (fs *FS) MD5(p string) (string, error) {
	local, err := fs.local(p)
	if err != nil {
		return "", err
	}
	f, err := os.Open(local)
	if err != nil {
		return "", fmt.Errorf("%%Error opening %s (No such file or directory)", p)
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Verify is the output of "verify /md5 <p>".
func (fs *FS) Verify(p string) string {
	sum, err := fs.MD5(p)
	if err != nil {
		return err.Error() + "\n"
	}
	return fmt.Sprintf("..........Done!\nverify /md5 (%s) = %s\n", p, sum)
}

// Delete removes a device file.
func (fs *FS) Delete(p string) error {
	local, err := fs.local(p)
	if err != nil {
		return err
	}
	st, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("%%Error deleting %s (No such file or directory)", p)
	}
	if st.IsDir() {
		return fmt.Errorf("%%Error deleting %s (Is a directory)", p)
	}
	return os.Remove(local)
}

// WriteFile writes a device file (show … | redirect).
func (fs *FS) WriteFile(p string, data []byte) error {
	local, err := fs.local(p)
	if err != nil {
		return err
	}
	return os.WriteFile(local, data, 0o644)
}

// SCP serves scp -t / -f inside flash:.
func (fs *FS) SCP() xfer.SCPHandler {
	return xfer.SCPHandler{
		Resolve: func(p, name string, write bool) (string, error) {
			if i := strings.Index(p, ":"); i > 0 && !strings.Contains(p[:i], "/") {
				if _, err := fs.local(p[:i+1]); err != nil {
					return "", errors.New("invalid file system")
				}
			}
			return xfer.InRoot(fs.Root, p, name)
		},
		Done: func(local string, size int64, upload bool, err error) {
			rel, _ := filepath.Rel(fs.Root, local)
			dir := "sent"
			if upload {
				dir = "received"
			}
			if err != nil {
				fs.logf("scp: flash:%s %s failed: %v", filepath.ToSlash(rel), dir, err)
				return
			}
			fs.logf("scp: flash:%s %s (%d bytes)", filepath.ToSlash(rel), dir, size)
		},
	}
}

// Exec runs one exec-channel command (ssh device "dir flash:"), writing
// its output to w; ch carries an SCP transfer. It returns the exit status
// and false for a command it does not know.
func (fs *FS) Exec(ch io.ReadWriter, cmd string) (int, bool) {
	cmd = strings.TrimSpace(cmd)
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return 0, false
	}
	out := func(s string) { io.WriteString(ch, strings.ReplaceAll(s, "\n", "\r\n")) }
	switch {
	case xfer.IsSCPCommand(cmd):
		return fs.SCP().Serve(ch, cmd), true
	case f[0] == "dir":
		arg := strings.Join(f[1:], " ")
		out(fs.Dir(arg))
		return 0, true
	case len(f) >= 3 && f[0] == "verify" && strings.HasPrefix(f[1], "/md5"):
		out(fs.Verify(strings.Join(f[2:], " ")))
		return 0, true
	case f[0] == "delete":
		args := f[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "/") {
			args = args[1:]
		}
		if len(args) > 0 && args[len(args)-1] == "no-prompt" {
			args = args[:len(args)-1]
		}
		p := strings.Join(args, " ")
		if err := fs.Delete(p); err != nil {
			out(err.Error() + "\n")
		}
		return 0, true
	case cmd == "show file systems" || cmd == "sh file sys":
		out(fs.FileSystems())
		return 0, true
	}
	return 0, false
}
