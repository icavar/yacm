// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"errors"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/icavar/yacm/internal/store"
)

// puttyValues is one saved PuTTY session (registry values or file lines).
type puttyValues struct {
	name string
	str  map[string]string
	num  map[string]int
}

func (v puttyValues) s(k string) string { return strings.TrimSpace(v.str[strings.ToLower(k)]) }
func (v puttyValues) n(k string, def int) int {
	if x, ok := v.num[strings.ToLower(k)]; ok {
		return x
	}
	if x, err := strconv.Atoi(v.s(k)); err == nil {
		return x
	}
	return def
}

func puttyName(raw string) string {
	if d, err := url.PathUnescape(raw); err == nil {
		return d
	}
	return raw
}

// unescapeReg handles \\ and \" inside .reg string values.
func unescapeReg(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func parseRegSessions(text string) []puttyValues {
	var out []puttyValues
	var cur *puttyValues
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(raw)
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			cur = nil
			key := l[1 : len(l)-1]
			i := strings.LastIndex(strings.ToLower(key), `\sessions\`)
			if i < 0 || strings.HasPrefix(key, "-") {
				continue
			}
			name := key[i+len(`\sessions\`):]
			if strings.Contains(name, `\`) {
				continue
			}
			out = append(out, puttyValues{name: puttyName(name), str: map[string]string{}, num: map[string]int{}})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil || !strings.HasPrefix(l, `"`) {
			continue
		}
		end := strings.Index(l[1:], `"=`)
		if end < 0 {
			continue
		}
		k := strings.ToLower(l[1 : end+1])
		v := l[end+3:]
		switch {
		case strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2:
			cur.str[k] = unescapeReg(v[1 : len(v)-1])
		case strings.HasPrefix(v, "dword:"):
			if x, err := strconv.ParseUint(strings.TrimPrefix(v, "dword:"), 16, 32); err == nil {
				cur.num[k] = int(int32(uint32(x)))
			}
		}
	}
	return out
}

func importPuTTYReg(text, parentID string) (*Result, error) {
	sessions := parseRegSessions(text)
	if len(sessions) == 0 {
		return nil, errors.New("no PuTTY sessions found in this registry export")
	}
	return puttyImport("PuTTY", sessions, parentID), nil
}

func importPuTTYDir(dir, parentID string) (*Result, error) {
	var sessions []puttyValues
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := readFile(p)
		if err != nil {
			return nil
		}
		v := puttyValues{name: puttyName(d.Name()), str: map[string]string{}, num: map[string]int{}}
		for _, l := range strings.Split(strings.ReplaceAll(decodeText(data), "\r\n", "\n"), "\n") {
			if k, val, ok := strings.Cut(l, "="); ok {
				v.str[strings.ToLower(strings.TrimSpace(k))] = val
			}
		}
		sessions = append(sessions, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, errors.New("no PuTTY sessions found in this folder")
	}
	return puttyImport("PuTTY", sessions, parentID), nil
}

func puttyImport(source string, sessions []puttyValues, parentID string) *Result {
	b := newBuilder(source, parentID)
	sort.Slice(sessions, func(i, j int) bool { return strings.ToLower(sessions[i].name) < strings.ToLower(sessions[j].name) })
	for _, v := range sessions {
		if strings.EqualFold(v.name, "Default Settings") {
			continue
		}
		puttySession(b, v)
	}
	return b.done()
}

func puttySession(b *builder, v puttyValues) {
	n := &store.Node{ID: store.NewID(), Name: v.name}
	ss := &store.Session{}
	host := v.s("HostName")
	user := v.s("UserName")
	if u, h, ok := strings.Cut(host, "@"); ok && user == "" {
		user, host = u, h
	}
	switch strings.ToLower(v.s("Protocol")) {
	case "ssh", "":
		if host == "" {
			b.skip(v.name, "", "no host name")
			return
		}
		ss.Protocol, ss.Algorithms = store.ProtoSSH, store.AlgoCompatible
		ss.Port = v.n("PortNumber", 22)
		ss.Auth = store.AuthAsk
		if key := v.s("PublicKeyFile"); key != "" {
			ss.Auth, ss.KeyPath = store.AuthPublicKey, key
		}
		if cert := v.s("DetachedCertificate"); cert != "" {
			ss.CertPath = cert
		}
		ss.AgentForward = v.n("AgentFwd", 0) == 1
		switch m := v.n("ProxyMethod", 0); m {
		case 0:
		case 6, 7, 8: // SSH proxy ("jump host") in PuTTY 0.77+
			j := store.JumpHost{Host: v.s("ProxyHost"), Port: v.n("ProxyPort", 22), User: v.s("ProxyUsername"), Auth: store.AuthAgent}
			if j.Host != "" {
				ss.Jump = append(ss.Jump, j)
			}
		default:
			b.warn(v.name, "proxy settings ("+[]string{"", "SOCKS4", "SOCKS5", "HTTP", "Telnet", "local command"}[clampIndex(m, 6)]+") were not imported")
		}
	case "telnet":
		ss.Protocol = store.ProtoTelnet
		ss.Port = v.n("PortNumber", 23)
	case "serial":
		ss.Protocol = store.ProtoSerial
		stop := map[int]string{2: "1", 3: "1.5", 4: "2"}[v.n("SerialStopHalfbits", 2)]
		if stop == "" {
			stop = "1"
		}
		ss.Serial = &store.Serial{Device: v.s("SerialLine"), Baud: v.n("SerialSpeed", 9600), DataBits: v.n("SerialDataBits", 8),
			Parity: []string{"none", "odd", "even", "mark", "space"}[clampIndex(v.n("SerialParity", 0), 5)], StopBits: stop}
	default:
		b.skip(v.name, v.s("Protocol"), "only SSH, Telnet and serial sessions are supported")
		return
	}
	ss.Host, ss.User = host, user
	if ss.Protocol == store.ProtoSerial {
		ss.Host, ss.User = "", ""
	}
	n.Session = ss
	b.add(nil, n)
}
