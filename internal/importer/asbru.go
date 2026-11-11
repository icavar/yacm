// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package importer converts Ásbrú Connection Manager / PAC Manager
// configurations (asbru.yml / pac.yml exports) into YACM sessions.
package importer

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/yacm-app/yacm/internal/expect"
	"github.com/yacm-app/yacm/internal/store"
)

// Skip records a connection that was not imported.
type Skip struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	Reason string `json:"reason"`
}

// Report summarises an import.
type Report struct {
	Source        string   `json:"source"`
	Groups        int      `json:"groups"`
	Sessions      int      `json:"sessions"`
	Secrets       int      `json:"secrets"`
	SecretsFailed int      `json:"secretsFailed"` // stored passwords that could not be decrypted
	Automated     int      `json:"automated"`     // sessions that came with login automation rules
	Profiles      int      `json:"profiles"`      // credential profiles added
	ProfilesKept  int      `json:"profilesKept"`  // existing profiles used instead of adding the same one again
	Skipped       []Skip   `json:"skipped"`
	Warnings      []string `json:"warnings"`
}

// Result is what the caller stores.
type Result struct {
	Nodes       []*store.Node
	Credentials []*store.Credential // credential profiles the nodes use (SecureCRT credential sets)
	Secrets     map[string]string
	Report      Report
}

type env = map[string]any

func str(m env, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(t)
	}
}

func num(m env, k string, def int) int {
	s := strings.TrimSpace(str(m, k))
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		if f, ferr := strconv.ParseFloat(s, 64); ferr == nil {
			return int(f)
		}
		return def
	}
	return n
}

func flag(m env, k string) bool {
	switch strings.ToLower(strings.TrimSpace(str(m, k))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func list(m env, k string) []env {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	var out []env
	for _, it := range raw {
		if e, ok := it.(map[string]any); ok {
			out = append(out, e)
		}
	}
	return out
}

func strList(m env, k string) []string {
	raw, ok := m[k].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, it := range raw {
		if s := strings.TrimSpace(fmt.Sprint(it)); s != "" && it != nil {
			out = append(out, s)
		}
	}
	return out
}

// secretOf decrypts an Ásbrú field, tolerating clear-text values.
func secretOf(m env, k string, warn func(string)) string {
	v := str(m, k)
	if v == "" {
		return ""
	}
	pt, err := decryptAsbru(v)
	if err == errNotEncrypted {
		return v
	}
	if err != nil {
		warn(fmt.Sprintf("could not decrypt %q: %v", k, err))
		return ""
	}
	return pt
}

var (
	reVar   = regexp.MustCompile(`<V:(\d+)>`)
	reGV    = regexp.MustCompile(`<GV:([^>]+)>`)
	reOther = regexp.MustCompile(`<(ASK|CMD|KPX_[a-z]+|ENV|TIMESTAMP|DATE_[A-Z]+|TIME_[A-Z]+)[^>]*>`)
)

// asbruVariables resolves <V:n> and <GV:name> at import time. It reports
// whether a secret value was substituted.
func resolveVars(s string, vars []string, hiddenVars map[int]bool, gv map[string]string, hiddenGV map[string]bool) (string, bool) {
	secret := false
	s = reVar.ReplaceAllStringFunc(s, func(m string) string {
		i, _ := strconv.Atoi(reVar.FindStringSubmatch(m)[1])
		if i < len(vars) {
			if hiddenVars[i] {
				secret = true
			}
			return vars[i]
		}
		return m
	})
	s = reGV.ReplaceAllStringFunc(s, func(m string) string {
		name := reGV.FindStringSubmatch(m)[1]
		if v, ok := gv[name]; ok {
			if hiddenGV[name] {
				secret = true
			}
			return v
		}
		return m
	})
	return s, secret
}

// parseSerialOptions reads cu-style options: -l /dev/ttyUSB0 -s 9600 --parity=even
func parseSerialOptions(opts string) *store.Serial {
	s := &store.Serial{Baud: 9600, DataBits: 8, Parity: "none", StopBits: "1"}
	f := strings.Fields(opts)
	for i := 0; i < len(f); i++ {
		switch {
		case f[i] == "-l" && i+1 < len(f):
			s.Device = f[i+1]
			i++
		case f[i] == "-s" && i+1 < len(f):
			if n, err := strconv.Atoi(f[i+1]); err == nil && n > 0 {
				s.Baud = n
			}
			i++
		case strings.HasPrefix(f[i], "--parity="):
			p := strings.TrimPrefix(f[i], "--parity=")
			if p == "even" || p == "odd" || p == "none" {
				s.Parity = p
			}
		}
	}
	return s
}

// Asbru converts an Ásbrú/PAC YAML config. Imported top-level entries are
// placed under parentID ("" = root).
func Asbru(data []byte, parentID string) (*Result, error) {
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("not a valid Ásbrú/PAC YAML file: %w", err)
	}
	envs, ok := cfg["environments"].(map[string]any)
	if !ok || len(envs) == 0 {
		return nil, fmt.Errorf("no \"environments\" section found — is this an Ásbrú or PAC Manager config?")
	}
	res := &Result{Secrets: map[string]string{}}
	warnf := func(name, msg string) {
		res.Report.Warnings = append(res.Report.Warnings, name+": "+msg)
	}

	// Global variables.
	gv := map[string]string{}
	hiddenGV := map[string]bool{}
	if defaults, ok := cfg["defaults"].(map[string]any); ok {
		if gvs, ok := defaults["global variables"].(map[string]any); ok {
			for name, raw := range gvs {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if flag(m, "hidden") {
					gv[name] = secretOf(m, "value", func(s string) { warnf("global variable "+name, s) })
					hiddenGV[name] = true
				} else {
					gv[name] = str(m, "value")
				}
			}
		}
	}

	ids := map[string]string{} // asbru uuid -> new id
	var keys []string
	for k, v := range envs {
		if _, ok := v.(map[string]any); !ok {
			continue
		}
		if k == "__PAC__ROOT__" || k == "__PAC_SHELL__" || strings.HasPrefix(k, "HASH(") {
			continue
		}
		keys = append(keys, k)
		ids[k] = store.NewID()
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := envs[keys[i]].(map[string]any), envs[keys[j]].(map[string]any)
		ga, gb := flag(a, "_is_group"), flag(b, "_is_group")
		if ga != gb {
			return ga
		}
		return strings.ToLower(str(a, "name")) < strings.ToLower(str(b, "name"))
	})

	// Only keep entries reachable from groups we import (a skipped method's
	// node has no children, so parent resolution is simple).
	order := map[string]int{}
	for _, k := range keys {
		e := envs[k].(map[string]any)
		name := strings.TrimSpace(str(e, "name"))
		if name == "" {
			name = k
		}
		parent := parentID
		if p := str(e, "parent"); p != "" && p != "__PAC__ROOT__" {
			if nid, ok := ids[p]; ok {
				parent = nid
			}
		}
		node := &store.Node{ID: ids[k], ParentID: parent, Name: name, Description: str(e, "description")}
		node.Order = order[parent]

		if flag(e, "_is_group") {
			node.IsGroup = true
			node.Expanded = true
			order[parent]++
			res.Nodes = append(res.Nodes, node)
			res.Report.Groups++
			continue
		}

		method := str(e, "method")
		s := &store.Session{}
		switch method {
		case "SSH":
			s.Protocol = store.ProtoSSH
		case "Telnet":
			s.Protocol = store.ProtoTelnet
		case "Serial (cu)", "Serial (remote-tty)":
			s.Protocol = store.ProtoSerial
			s.Serial = parseSerialOptions(str(e, "options"))
			if s.Serial.Device == "" {
				res.Report.Skipped = append(res.Report.Skipped, Skip{name, method, "serial device not found in options"})
				continue
			}
		default:
			reason := "only terminal protocols (SSH, Telnet, serial) are supported"
			if method == "SFTP" || method == "FTP" {
				reason = "file transfer sessions are not supported yet"
			}
			res.Report.Skipped = append(res.Report.Skipped, Skip{name, method, reason})
			continue
		}

		w := func(msg string) { warnf(name, msg) }
		if s.Protocol != store.ProtoSerial {
			s.Host = strings.TrimSpace(str(e, "ip"))
			s.Port = num(e, "port", store.DefaultPort(s.Protocol))
			if s.Host == "" {
				res.Report.Skipped = append(res.Report.Skipped, Skip{name, method, "no host/IP"})
				continue
			}
		}
		s.User = str(e, "user")
		pass := secretOf(e, "pass", w)

		if s.Protocol == store.ProtoSSH {
			s.Algorithms = store.AlgoCompatible // old network gear is common in PAC/Ásbrú configs
			switch str(e, "auth type") {
			case "publickey":
				s.Auth = store.AuthPublicKey
				s.KeyPath = str(e, "public key")
				if s.User == "" {
					s.User = str(e, "passphrase user")
				}
				if pp := secretOf(e, "passphrase", w); pp != "" {
					res.Secrets[store.SecretPassphrase(node.ID)] = pp
				}
				if s.KeyPath == "" {
					s.Auth = store.AuthAgent
					w("public key auth without key file — set to SSH agent")
				}
			case "manual":
				s.Auth = store.AuthAsk
			default:
				s.Auth = store.AuthPassword
				if pass == "" {
					s.Auth = store.AuthAsk
				}
			}
			if pass != "" && s.Auth == store.AuthPassword {
				res.Secrets[store.SecretPassword(node.ID)] = pass
			}
			if jip := strings.TrimSpace(str(e, "jump ip")); jip != "" {
				j := store.JumpHost{Host: jip, Port: num(e, "jump port", 22), User: str(e, "jump user")}
				switch {
				case str(e, "jump key") != "":
					j.Auth, j.KeyPath = store.AuthPublicKey, str(e, "jump key")
				case str(e, "jump pass") != "":
					j.Auth = store.AuthPassword
					res.Secrets[store.SecretJumpPassword(node.ID, 0)] = secretOf(e, "jump pass", w)
				default:
					j.Auth = store.AuthAgent
				}
				s.Jump = append(s.Jump, j)
			}
			if flag(e, "use proxy") {
				w("SOCKS/HTTP proxy settings were not imported")
			}
			opts := str(e, "options")
			for _, f := range []string{" -L", " -R", " -D"} {
				if strings.Contains(" "+opts, f) {
					w("SSH port forwarding options were not imported (" + strings.TrimSpace(opts) + ")")
					break
				}
			}
			if strings.Contains(opts, " -A") || strings.HasPrefix(strings.TrimSpace(opts), "-A") {
				s.AgentForward = true
			}
		} else if pass != "" {
			// Telnet/serial: keep the password so <PASS> works in expect rules.
			res.Secrets[store.SecretPassword(node.ID)] = pass
		}

		// Per-connection variables for <V:n>.
		var vars []string
		hiddenVars := map[int]bool{}
		for i, v := range list(e, "variables") {
			if flag(v, "hide") {
				vars = append(vars, secretOf(v, "txt", w))
				hiddenVars[i] = true
			} else {
				vars = append(vars, str(v, "txt"))
			}
		}

		if to, ok := e["terminal options"].(map[string]any); ok && flag(to, "use personal settings") {
			s.ExpectTimeout = num(to, "timeout command", 0)
		}
		if s.ExpectTimeout == 0 {
			s.ExpectTimeout = 40
		}

		rules := list(e, "expect")
		for i, r := range rules {
			rule := store.ExpectRule{ID: store.NewID(), Active: flag(r, "active"), Return: flag(r, "return")}
			rule.Expect = str(r, "expect")
			if _, okRE := expect.Compile(rule.Expect); !okRE {
				w(fmt.Sprintf("expect rule %d: pattern %q is not RE2 syntax, it will be matched literally", i+1, rule.Expect))
			}
			send := str(r, "send")
			hidden := flag(r, "hidden")
			if hidden {
				send = secretOf(r, "send", w)
			}
			send, gotSecret := resolveVars(send, vars, hiddenVars, gv, hiddenGV)
			hidden = hidden || gotSecret
			if reOther.MatchString(send) {
				w(fmt.Sprintf("expect rule %d uses %s, which is not supported", i+1, reOther.FindString(send)))
			}
			if t := num(r, "time_out", -1); t > 0 {
				rule.Timeout = t
			}
			switch onFail := num(r, "on_fail", -1); {
			case onFail == -1 || onFail == -2:
				rule.OnFail = "stop"
			default:
				rule.OnFail = "continue"
				if onFail != i+1 {
					w(fmt.Sprintf("expect rule %d jumps to rule %d on timeout; jumps are not supported, it continues with the next rule", i+1, onFail))
				}
			}
			if om := num(r, "on_match", -1); om >= 0 && om != i+1 {
				w(fmt.Sprintf("expect rule %d jumps to rule %d on match; jumps are not supported", i+1, om))
			}
			rule.Hidden = hidden
			if hidden {
				if send != "" {
					res.Secrets[store.SecretExpect(node.ID, rule.ID)] = send
				}
			} else {
				rule.Send = send
			}
			s.Expect = append(s.Expect, rule)
		}

		for _, c := range list(e, "local before") {
			cmd := strings.TrimSpace(str(c, "command"))
			if cmd == "" {
				continue
			}
			if _, has := c["default"]; has && !flag(c, "default") {
				w("disabled \"before\" command skipped: " + cmd)
				continue
			}
			s.Before = append(s.Before, store.LocalCommand{Command: cmd, Confirm: true})
		}
		for _, c := range list(e, "local after") {
			cmd := strings.TrimSpace(str(c, "command"))
			if cmd == "" {
				continue
			}
			if _, has := c["default"]; has && !flag(c, "default") {
				continue
			}
			s.After = append(s.After, store.LocalCommand{Command: cmd, Confirm: true})
		}
		for _, m := range list(e, "macros") {
			txt := str(m, "txt")
			if strings.TrimSpace(txt) == "" {
				continue
			}
			label := strings.Split(strings.TrimSpace(txt), "\n")[0]
			if len([]rune(label)) > 40 {
				label = string([]rune(label)[:40]) + "…"
			}
			_, has := m["intro"]
			s.Macros = append(s.Macros, store.Macro{Name: label, Text: txt, Return: !has || flag(m, "intro")})
		}
		s.Clusters = strList(e, "cluster")
		if flag(e, "save session logs") {
			s.LogToFile = true
			s.LogDir = str(e, "session logs folder")
		}
		if t := strings.TrimSpace(str(e, "title")); t != "" && t != name && node.Description == "" {
			node.Description = "Title: " + t
		}
		node.Session = s
		if err := store.Validate(node); err != nil {
			res.Report.Skipped = append(res.Report.Skipped, Skip{name, method, err.Error()})
			continue
		}
		order[parent]++
		res.Nodes = append(res.Nodes, node)
		res.Report.Sessions++
	}
	for k, v := range res.Secrets {
		if v == "" {
			delete(res.Secrets, k)
		}
	}
	res.Report.Secrets = len(res.Secrets)
	return res, nil
}
