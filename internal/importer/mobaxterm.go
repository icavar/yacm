// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"errors"
	"strconv"
	"strings"

	"github.com/yacm-app/yacm/internal/store"
)

// MobaXterm keeps sessions in the [Bookmarks] / [Bookmarks_N] sections of
// MobaXterm.ini (a .mxtsessions export is an extract of them). Each folder
// section has SubRep= (folder path with backslashes) and ImgNum=, followed
// by one line per session: name=<flag>#<icon>#<connection %-fields>#...
// Field positions follow the public format notes at
// gist.github.com/Ruzgfpegk/ab597838e4abbe8de30d7224afd062ea.

var mobaTypes = map[string]string{
	"2": "Rsh", "3": "XDMCP", "4": "RDP", "5": "VNC", "6": "FTP", "7": "SFTP",
	"9": "File", "10": "Shell", "11": "Browser", "12": "Mosh", "13": "AWS S3", "14": "WSL",
}

var standardBauds = map[int]bool{300: true, 600: true, 1200: true, 2400: true, 4800: true, 9600: true, 14400: true, 19200: true, 38400: true, 57600: true, 115200: true, 230400: true, 460800: true, 921600: true}

func field(f []string, i int) string {
	if i < len(f) {
		return strings.TrimSpace(f[i])
	}
	return ""
}

func mobaPath(p string) string {
	p = strings.ReplaceAll(p, "_CurrentDrive_", "C")
	return p
}

func importMobaXterm(text, parentID string) (*Result, error) {
	b := newBuilder("MobaXterm", parentID)
	type section struct {
		path  []string
		lines [][2]string
	}
	var sections []*section
	var cur *section
	hasPasswords := false
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l := strings.TrimRight(raw, " \t")
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			name := strings.ToLower(t[1 : len(t)-1])
			cur = nil
			if name == "bookmarks" || strings.HasPrefix(name, "bookmarks_") {
				cur = &section{}
				sections = append(sections, cur)
			}
			if name == "passwords" || name == "credentials" {
				hasPasswords = true
			}
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "SubRep":
			for _, p := range strings.Split(v, `\`) {
				if p = strings.TrimSpace(p); p != "" {
					cur.path = append(cur.path, p)
				}
			}
		case "ImgNum":
		default:
			cur.lines = append(cur.lines, [2]string{strings.TrimSpace(k), v})
		}
	}
	if len(sections) == 0 {
		return nil, errors.New("no [Bookmarks] sections found")
	}
	for _, sec := range sections {
		for _, kv := range sec.lines {
			mobaSession(b, sec.path, kv[0], kv[1])
		}
	}
	if hasPasswords {
		b.warn("", "Passwords saved inside MobaXterm are not imported; YACM asks for them when connecting and can save them in its vault.")
	}
	return b.done(), nil
}

func mobaSession(b *builder, path []string, name, value string) {
	parts := strings.Split(value, "#")
	if len(parts) < 3 {
		b.skip(name, "", "unrecognised session line")
		return
	}
	f := strings.Split(parts[2], "%")
	typ := field(f, 0)
	n := &store.Node{ID: store.NewID(), Name: name}
	if len(parts) > 5 {
		n.Description = strings.TrimSpace(strings.ReplaceAll(parts[5], "__DIEZE__", "#"))
	}
	ss := &store.Session{}
	switch typ {
	case "0": // SSH
		ss.Protocol = store.ProtoSSH
		ss.Host, ss.User = field(f, 1), field(f, 3)
		ss.Port, _ = strconv.Atoi(field(f, 2))
		ss.Algorithms = store.AlgoCompatible
		ss.Auth = store.AuthAsk
		if key := mobaPath(field(f, 14)); key != "" {
			ss.Auth, ss.KeyPath = store.AuthPublicKey, key
		} else if field(f, 33) == "-1" {
			ss.Auth = store.AuthAgent
		}
		if gw := field(f, 8); gw != "" {
			hosts := strings.Split(gw, "__PIPE__")
			ports := strings.Split(field(f, 9), "__PIPE__")
			users := strings.Split(field(f, 10), "__PIPE__")
			keys := strings.Split(field(f, 15), "__PIPE__")
			for i, h := range hosts {
				if h = strings.TrimSpace(h); h == "" {
					continue
				}
				j := store.JumpHost{Host: h, Port: 22, User: field(users, i), Auth: store.AuthAgent}
				if p, err := strconv.Atoi(field(ports, i)); err == nil && p > 0 {
					j.Port = p
				}
				if k := mobaPath(field(keys, i)); k != "" {
					j.Auth, j.KeyPath = store.AuthPublicKey, k
				}
				ss.Jump = append(ss.Jump, j)
			}
		}
	case "1": // Telnet
		ss.Protocol = store.ProtoTelnet
		ss.Host, ss.User = field(f, 1), field(f, 3)
		ss.Port, _ = strconv.Atoi(field(f, 2))
	case "8": // Serial
		ss.Protocol = store.ProtoSerial
		dev := field(f, 8)
		if i := strings.IndexAny(dev, " \t("); i > 0 {
			dev = dev[:i]
		}
		baud := 9600
		raw := field(f, 2)
		if v, err := strconv.Atoi(raw); err == nil && standardBauds[v] {
			baud = v
		} else if rest, ok := strings.CutPrefix(raw, "100"); ok {
			if v, err := strconv.Atoi(rest); err == nil && standardBauds[v] {
				baud = v
			}
		}
		bits, _ := strconv.Atoi(field(f, 3))
		par, _ := strconv.Atoi(field(f, 4))
		stop, _ := strconv.Atoi(field(f, 5))
		ss.Serial = &store.Serial{Device: dev, Baud: baud, DataBits: []int{5, 6, 7, 8}[clampIndexDefault(bits, 4, 3)],
			Parity: []string{"none", "odd", "even", "mark", "space"}[clampIndex(par, 5)], StopBits: []string{"1", "1.5", "2"}[clampIndex(stop, 3)]}
		b.warn(name, "check the serial port and line settings after importing")
	default:
		label := mobaTypes[typ]
		if label == "" {
			label = "type " + typ
		}
		b.skip(name, label, "only SSH, Telnet and serial sessions are supported")
		return
	}
	n.Session = ss
	b.add(path, n)
}

func clampIndexDefault(i, n, def int) int {
	if i < 0 || i >= n {
		return def
	}
	return i
}
