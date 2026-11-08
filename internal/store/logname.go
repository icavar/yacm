// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Log file names. With "Use a file name template" off, a session log is
// called <name>_<yyyymmdd-hhmmss>.log, as before. A template uses the tokens
// below; "/" starts a subfolder inside the log folder.

// LegacyLogName is the name used when no template applies.
const LegacyLogName = "{name}_{stamp}"

// DefaultLogNameTemplate is offered when the template is first switched on.
const DefaultLogNameTemplate = "{group}/{name}_{date}_{time}.log"

// LogNameValues are the values a template can use.
type LogNameValues struct {
	Name, Host, Protocol, User, Group, Path, Ticket string
	Time                                            time.Time
}

// LogNameTokens lists the tokens for the settings help text.
var LogNameTokens = []string{"{name}", "{host}", "{protocol}", "{user}", "{group}", "{path}", "{date}", "{time}", "{ticket}"}

var (
	logBadChars  = regexp.MustCompile(`[\\:*?"<>|\x00-\x1f]+`)
	logToken     = regexp.MustCompile(`\{([a-z]+)\}`)
	logReserved  = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\.|$)`)
	logEdgeChars = "_-. "
	// logExt is what counts as a file extension (not ".1" of an address).
	logExt = regexp.MustCompile(`^\.[A-Za-z][A-Za-z0-9]{0,7}$`)
)

// LogFileName expands a template into a relative path (with "/" between
// folders). Empty tokens are left out together with one separator next to
// them, unsafe characters become "_", ".." and empty folders are dropped,
// and ".log" is added when the name has no extension.
func LogFileName(tmpl string, v LogNameValues) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = LegacyLogName
	}
	t := v.Time
	if t.IsZero() {
		t = time.Now()
	}
	vals := map[string]string{
		"name": v.Name, "host": v.Host, "protocol": v.Protocol, "user": v.User,
		"group": v.Group, "ticket": v.Ticket,
		"date": t.Format("2006-01-02"), "time": t.Format("15-04-05"),
		"stamp": t.Format("20060102-150405"),
	}
	// {path} is the group path: one folder per group. Slashes inside other
	// values are not folders.
	pathSegs := []string{}
	for _, p := range strings.Split(v.Path, "/") {
		if p = cleanLogSegment(p); p != "" {
			pathSegs = append(pathSegs, p)
		}
	}
	const pathMark = "\x00path\x00"
	tmpl = strings.ReplaceAll(tmpl, "\\", "/")
	var out []string
	for _, seg := range strings.Split(tmpl, "/") {
		if strings.TrimSpace(seg) == "{path}" {
			out = append(out, pathSegs...)
			continue
		}
		// Leave out empty tokens with one separator next to them.
		seg = logToken.ReplaceAllStringFunc(seg, func(m string) string {
			k := m[1 : len(m)-1]
			if k == "path" {
				return pathMark
			}
			val, ok := vals[k]
			if !ok {
				return m // unknown token: kept as typed
			}
			if val == "" {
				return "\x01"
			}
			return strings.NewReplacer("/", "_", "\\", "_").Replace(val)
		})
		seg = strings.ReplaceAll(seg, pathMark, strings.Join(pathSegs, "_"))
		seg = dropEmpty(seg)
		if seg = cleanLogSegment(seg); seg != "" {
			out = append(out, seg)
		}
	}
	if len(out) == 0 {
		out = []string{cleanLogSegment(v.Name)}
		if out[0] == "" {
			out[0] = "session"
		}
	}
	last := out[len(out)-1]
	if !logExt.MatchString(filepath.Ext(last)) {
		out[len(out)-1] = last + ".log"
	}
	return strings.Join(out, "/")
}

// dropEmpty removes the \x01 marks of empty tokens and one separator next
// to each (the one after it, or before it at the end).
func dropEmpty(seg string) string {
	for strings.Contains(seg, "\x01") {
		i := strings.Index(seg, "\x01")
		switch {
		case i+1 < len(seg) && seg[i+1] != '.' && strings.ContainsRune(logEdgeChars, rune(seg[i+1])):
			seg = seg[:i] + seg[i+2:]
		case i > 0 && seg[i-1] != '.' && strings.ContainsRune(logEdgeChars, rune(seg[i-1])):
			seg = seg[:i-1] + seg[i+1:]
		default:
			seg = seg[:i] + seg[i+1:]
		}
	}
	return seg
}

func cleanLogSegment(s string) string {
	s = logBadChars.ReplaceAllString(s, "_")
	s = strings.Trim(s, " .")
	if s == ".." || s == "." {
		return ""
	}
	if logReserved.MatchString(s) {
		s = "_" + s
	}
	return s
}

// maxLogPath keeps log paths within the classic Windows limit.
const maxLogPath = 240

// CreateLogFile creates the log file for rel (from LogFileName) inside dir.
// It never overwrites: an existing name gets -2, -3 … A path that is too
// long for Windows is shortened; shortened reports that.
func CreateLogFile(dir, rel string) (f *os.File, path string, shortened bool, err error) {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, filepath.Clean(dir)+string(filepath.Separator)) {
		return nil, "", false, fmt.Errorf("log file name %q leaves the log folder", rel)
	}
	if runtime.GOOS == "windows" || os.Getenv("YACM_TEST_SHORT_PATHS") != "" {
		if len(full) > maxLogPath {
			ext := filepath.Ext(full)
			base := strings.TrimSuffix(filepath.Base(full), ext)
			cut := len(full) - maxLogPath + 6
			if cut >= len(base) {
				return nil, "", false, fmt.Errorf("the log folder path is too long (%d characters)", len(filepath.Dir(full)))
			}
			full = filepath.Join(filepath.Dir(full), base[:len(base)-cut]+ext)
			shortened = true
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, "", false, err
	}
	ext := filepath.Ext(full)
	stem := strings.TrimSuffix(full, ext)
	for i := 1; i < 1000; i++ {
		p := full
		if i > 1 {
			p = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		f, err = os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return f, p, shortened, nil
		}
		if !os.IsExist(err) {
			return nil, "", false, err
		}
	}
	return nil, "", false, fmt.Errorf("too many log files named %s", filepath.Base(full))
}

// LogNameTemplate returns the template for a session: its own, then its
// nearest group's (when "own templates" is on), then the one in Settings
// (when templates are on), else the classic name.
func (s *Store) LogNameTemplate(n *Node) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.settings
	if !st.LogNameTemplateOn {
		return LegacyLogName
	}
	if st.LogNameOwn {
		if n.Session != nil && strings.TrimSpace(n.Session.LogName) != "" {
			return n.Session.LogName
		}
		if v := s.inheritedLocked(n.ParentID, func(d *GroupDefaults) string { return strings.TrimSpace(d.LogName) }); v != "" {
			return v
		}
	}
	if strings.TrimSpace(st.LogNameTemplate) != "" {
		return st.LogNameTemplate
	}
	return DefaultLogNameTemplate
}

// GroupPath returns the names of the groups above a node, outermost first.
func (s *Store) GroupPath(n *Node) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for cur, guard := n.ParentID, 0; cur != "" && guard < 64; guard++ {
		g, ok := s.nodeLocked(cur)
		if !ok {
			break
		}
		out = append([]string{g.Name}, out...)
		cur = g.ParentID
	}
	return out
}
