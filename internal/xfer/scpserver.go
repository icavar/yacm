// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package xfer

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// SCPHandler serves the remote end of "scp -t" (a device sends a file to
// us) and "scp -f" (a device fetches a file from us) on one SSH channel.
// It serves single files; recursive copies are refused.
type SCPHandler struct {
	// Resolve maps the path a device asked for to a local file, or refuses.
	// name is the file name a sender announced (only for writes), so a
	// folder target can be completed.
	Resolve func(p, name string, write bool) (string, error)
	// MaxSize refuses larger uploads (0: no limit).
	MaxSize int64
	// Done is told about each finished file.
	Done func(local string, size int64, upload bool, err error)
}

// IsSCPCommand reports an exec command this handler serves.
func IsSCPCommand(cmd string) bool {
	f := strings.Fields(cmd)
	return len(f) >= 2 && filepath.Base(f[0]) == "scp"
}

// Serve runs cmd and returns the exit status for the channel.
func (h SCPHandler) Serve(ch io.ReadWriter, cmd string) int {
	f := strings.Fields(cmd)
	var sink, source, recursive bool
	target := ""
	for i := 1; i < len(f); i++ {
		a := f[i]
		switch {
		case a == "--":
			target = strings.Join(f[i+1:], " ")
			i = len(f)
		case strings.HasPrefix(a, "-") && target == "":
			sink = sink || strings.Contains(a, "t")
			source = source || strings.Contains(a, "f")
			recursive = recursive || strings.Contains(a, "r")
		default:
			target = strings.Join(f[i:], " ")
			i = len(f)
		}
	}
	target = strings.Trim(target, `'"`)
	r := bufio.NewReader(ch)
	fail := func(msg string) int {
		fmt.Fprintf(ch, "\x01scp: %s\n", msg)
		return 1
	}
	switch {
	case recursive:
		return fail("folders are not supported, send single files")
	case sink == source:
		return fail("unsupported scp command")
	case sink:
		return h.sink(ch, r, target, fail)
	default:
		return h.source(ch, r, target, fail)
	}
}

func (h SCPHandler) sink(w io.Writer, r *bufio.Reader, target string, fail func(string) int) int {
	_, _ = w.Write([]byte{0})
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0 // the sender is done
		}
		switch line[0] {
		case 'T':
			_, _ = w.Write([]byte{0})
			continue
		case 'D', 'E':
			return fail("folders are not supported, send single files")
		case 'C':
		default:
			return fail("unexpected input")
		}
		parts := strings.SplitN(strings.TrimRight(line, "\n"), " ", 3)
		if len(parts) < 3 {
			return fail("bad file header")
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || size < 0 {
			return fail("bad file size")
		}
		name := filepath.Base(filepath.Clean("/" + parts[2]))
		if h.MaxSize > 0 && size > h.MaxSize {
			return fail(fmt.Sprintf("the file is larger than the %d bytes allowed", h.MaxSize))
		}
		local, err := h.Resolve(target, name, true)
		if err != nil {
			return fail(err.Error())
		}
		tmp := local + ".part"
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fail("cannot write the file: " + err.Error())
		}
		_, _ = w.Write([]byte{0})
		_, err = io.CopyN(out, r, size)
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
		if err == nil {
			err = scpAck(r)
		}
		if err == nil {
			_ = os.Remove(local)
			err = os.Rename(tmp, local)
		}
		if h.Done != nil {
			h.Done(local, size, true, err)
		}
		if err != nil {
			_ = os.Remove(tmp)
			return fail("transfer failed: " + err.Error())
		}
		_, _ = w.Write([]byte{0})
	}
}

func (h SCPHandler) source(w io.Writer, r *bufio.Reader, target string, fail func(string) int) int {
	if err := scpAck(r); err != nil {
		return 1
	}
	local, err := h.Resolve(target, "", false)
	if err != nil {
		return fail(err.Error())
	}
	in, err := os.Open(local)
	if err != nil {
		return fail(errors.Unwrap(err).Error())
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil || st.IsDir() {
		return fail("not a regular file")
	}
	fmt.Fprintf(w, "C0644 %d %s\n", st.Size(), filepath.Base(local))
	if err := scpAck(r); err != nil {
		return 1
	}
	_, err = io.CopyN(w, in, st.Size())
	if err == nil {
		_, _ = w.Write([]byte{0})
		err = scpAck(r)
	}
	if h.Done != nil {
		h.Done(local, st.Size(), false, err)
	}
	if err != nil {
		return 1
	}
	return 0
}

// InRoot resolves p inside root: absolute paths and ".." cannot leave it.
// Device-style prefixes ("flash:") are ignored. A folder (or a path ending
// in "/") gets name appended.
func InRoot(root, p, name string) (string, error) {
	if i := strings.Index(p, ":"); i > 0 && !strings.ContainsAny(p[:i], `/\`) {
		p = p[i+1:]
	}
	clean := filepath.Clean("/" + filepath.FromSlash(strings.ReplaceAll(p, `\`, "/")))
	full := filepath.Join(root, clean)
	if st, err := os.Stat(full); (err == nil && st.IsDir()) || strings.HasSuffix(p, "/") || p == "" || p == "." {
		if name == "" {
			return "", errors.New("not a file")
		}
		full = filepath.Join(full, filepath.Base(filepath.Clean("/"+name)))
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("outside the shared folder")
	}
	return full, nil
}

// DeviceName is a file name a network device keeps whole: device commands
// (copy, scp -t, verify) end a name at the first space, so spaces become
// underscores ("cbr copy.txt" -> "cbr_copy.txt").
func DeviceName(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return '_'
		}
		return r
	}, name)
}

// SpaceAlias finds the file a device asked for under its DeviceName: when
// full does not exist, a file in the same folder whose DeviceName is the
// asked name is served instead ("cbr_copy.txt" serves "cbr copy.txt").
// Otherwise full is returned as is.
func SpaceAlias(full string) string {
	if _, err := os.Stat(full); err == nil {
		return full
	}
	dir, want := filepath.Split(full)
	if !strings.Contains(want, "_") {
		return full
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return full
	}
	found := ""
	for _, e := range es {
		if !e.IsDir() && e.Name() != want && DeviceName(e.Name()) == want {
			if found != "" {
				return full // more than one: ambiguous
			}
			found = filepath.Join(dir, e.Name())
		}
	}
	if found == "" {
		return full
	}
	return found
}
