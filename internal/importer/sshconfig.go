// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/icavar/yacm/internal/store"
)

// OpenSSH client configuration (~/.ssh/config). Every concrete Host alias
// becomes a session; options come from the first matching block that sets
// them, as in OpenSSH, so "Host *" defaults apply too.

type sshBlock struct {
	patterns []string
	opts     [][2]string // keyword (lower), value
}

var sshHostLine = regexp.MustCompile(`(?mi)^\s*Host\s+\S`)

func looksLikeSSHConfig(t string) bool {
	if !sshHostLine.MatchString(t) {
		return false
	}
	for _, l := range strings.Split(t, "\n") {
		f := strings.Fields(strings.ToLower(l))
		if len(f) > 0 {
			switch f[0] {
			case "hostname", "user", "port", "identityfile", "proxyjump", "host":
			default:
				if !strings.HasPrefix(f[0], "#") && !isSSHKeyword(f[0]) {
					return false
				}
			}
		}
	}
	return true
}

func isSSHKeyword(k string) bool {
	k = strings.TrimSuffix(k, "=")
	if i := strings.Index(k, "="); i > 0 {
		k = k[:i]
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z') {
			return false
		}
	}
	return len(k) > 2
}

func splitSSHLine(l string) (string, string) {
	l = strings.TrimSpace(l)
	if l == "" || strings.HasPrefix(l, "#") {
		return "", ""
	}
	i := strings.IndexAny(l, " \t=")
	if i < 0 {
		return strings.ToLower(l), ""
	}
	k := strings.ToLower(l[:i])
	v := strings.TrimLeft(l[i:], " \t=")
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return k, v
}

func parseSSHConfig(text, baseDir string, depth int, blocks []*sshBlock) []*sshBlock {
	cur := &sshBlock{patterns: []string{"*"}} // options before any Host apply to all
	if len(blocks) > 0 {
		cur = blocks[len(blocks)-1]
	} else {
		blocks = append(blocks, cur)
	}
	skipping := false
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		k, v := splitSSHLine(l)
		switch k {
		case "":
			continue
		case "host":
			cur = &sshBlock{patterns: strings.Fields(v)}
			blocks = append(blocks, cur)
			skipping = false
			continue
		case "match":
			skipping = true // Match conditions cannot be evaluated offline
			continue
		case "include":
			if depth > 5 {
				continue
			}
			for _, pat := range strings.Fields(v) {
				pat = expandTilde(pat)
				if !filepath.IsAbs(pat) {
					pat = filepath.Join(baseDir, pat)
				}
				files, _ := filepath.Glob(pat)
				for _, f := range files {
					if data, err := readFile(f); err == nil {
						blocks = parseSSHConfig(decodeText(data), baseDir, depth+1, blocks)
						cur = blocks[len(blocks)-1]
					}
				}
			}
			continue
		}
		if !skipping {
			cur.opts = append(cur.opts, [2]string{k, v})
		}
	}
	return blocks
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func sshMatch(patterns []string, host string) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		ok, _ := path.Match(strings.ToLower(p), strings.ToLower(host))
		if ok && neg {
			return false
		}
		if ok {
			matched = true
		}
	}
	return matched
}

// resolve returns the effective options for alias (first value wins).
func resolveSSH(blocks []*sshBlock, alias string) map[string]string {
	out := map[string]string{}
	for _, b := range blocks {
		if !sshMatch(b.patterns, alias) {
			continue
		}
		for _, kv := range b.opts {
			if _, set := out[kv[0]]; !set {
				out[kv[0]] = kv[1]
			}
		}
	}
	return out
}

func importSSHConfig(text, baseDir, parentID string) (*Result, error) {
	blocks := parseSSHConfig(text, baseDir, 0, nil)
	b := newBuilder("OpenSSH config", parentID)
	seen := map[string]bool{}
	for _, blk := range blocks {
		for _, alias := range blk.patterns {
			if strings.ContainsAny(alias, "*?!") || seen[alias] {
				continue
			}
			seen[alias] = true
			sshConfigHost(b, blocks, alias)
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("no concrete Host entries found (only wildcard patterns)")
	}
	return b.done(), nil
}

func sshEndpoint(blocks []*sshBlock, spec string) (host string, port int, user, key string) {
	spec = strings.TrimPrefix(spec, "ssh://")
	if u, h, ok := strings.Cut(spec, "@"); ok {
		user, spec = u, h
	}
	alias := spec
	if h, p, ok := strings.Cut(spec, ":"); ok && !strings.Contains(p, ":") {
		alias = h
		port, _ = strconv.Atoi(p)
	}
	o := resolveSSH(blocks, alias)
	host = alias
	if hn := o["hostname"]; hn != "" {
		host = strings.ReplaceAll(hn, "%h", alias)
	}
	if port == 0 {
		port, _ = strconv.Atoi(o["port"])
	}
	if port == 0 {
		port = 22
	}
	if user == "" {
		user = o["user"]
	}
	key = o["identityfile"]
	return
}

func sshConfigHost(b *builder, blocks []*sshBlock, alias string) {
	o := resolveSSH(blocks, alias)
	host, port, user, key := sshEndpoint(blocks, alias)
	n := &store.Node{ID: store.NewID(), Name: alias}
	ss := &store.Session{Protocol: store.ProtoSSH, Host: host, Port: port, User: user, Auth: store.AuthAsk, Algorithms: store.AlgoSecure}
	if key != "" && !strings.EqualFold(key, "none") {
		ss.Auth, ss.KeyPath = store.AuthPublicKey, strings.ReplaceAll(key, "%d", "~")
	}
	if c := o["certificatefile"]; c != "" {
		ss.CertPath = strings.ReplaceAll(c, "%d", "~")
	}
	ss.AgentForward = strings.EqualFold(o["forwardagent"], "yes")
	algos := strings.ToLower(o["kexalgorithms"] + "," + o["hostkeyalgorithms"] + "," + o["ciphers"])
	switch {
	case strings.Contains(algos, "group1-sha1") || strings.Contains(algos, "-cbc") || strings.Contains(algos, "3des"):
		ss.Algorithms = store.AlgoLegacy
	case strings.Contains(algos, "+ssh-rsa") || strings.Contains(algos, "group14-sha1") || strings.Contains(algos, "ssh-rsa"):
		ss.Algorithms = store.AlgoCompatible
	}
	if pj := o["proxyjump"]; pj != "" && !strings.EqualFold(pj, "none") {
		for _, hop := range strings.Split(pj, ",") {
			h, p, u, k := sshEndpoint(blocks, strings.TrimSpace(hop))
			j := store.JumpHost{Host: h, Port: p, User: u, Auth: store.AuthAgent}
			if k != "" && !strings.EqualFold(k, "none") {
				j.Auth, j.KeyPath = store.AuthPublicKey, k
			}
			ss.Jump = append(ss.Jump, j)
		}
	} else if pc := o["proxycommand"]; pc != "" && !strings.EqualFold(pc, "none") {
		if m := regexp.MustCompile(`ssh\s+(?:-\S+\s+)*-W\s+%h:%p\s+(\S+)`).FindStringSubmatch(pc); m != nil {
			h, p, u, k := sshEndpoint(blocks, m[1])
			j := store.JumpHost{Host: h, Port: p, User: u, Auth: store.AuthAgent}
			if k != "" {
				j.Auth, j.KeyPath = store.AuthPublicKey, k
			}
			ss.Jump = append(ss.Jump, j)
		} else {
			b.warn(alias, "ProxyCommand \""+pc+"\" was not imported")
		}
	}
	if lf := o["localforward"] + o["remoteforward"] + o["dynamicforward"]; lf != "" {
		b.warn(alias, "port forwarding options were not imported")
	}
	n.Session = ss
	b.add(nil, n)
}
