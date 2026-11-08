// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/icavar/yacm/internal/store"
)

// Spreadsheet import: a CSV file or the first sheet of an Excel workbook
// with one device per row. Columns are matched by their header (English
// and Croatian names) and can be re-mapped by the user before importing.

// Table is a parsed spreadsheet.
type Table struct {
	Sheet   string     `json:"sheet,omitempty"`
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// TableFields are the session fields a column can fill, in display order.
var TableFields = []string{"name", "host", "port", "protocol", "user", "password", "group", "description", "clusters", "credential", "jump", "serial", "baud", "color"}

// Mapping maps a field to a column index (-1 or missing = not used).
type Mapping map[string]int

var headerSynonyms = map[string][]string{
	"name":        {"name", "hostname", "devicename", "device", "sessionname", "session", "label", "title", "naziv", "ime", "uredaj", "uređaj"},
	"host":        {"host", "ip", "ipaddress", "ipadresa", "address", "adresa", "mgmtip", "managementip", "mgmt", "primaryip", "primaryipv4", "fqdn", "target", "dns"},
	"port":        {"port"},
	"protocol":    {"protocol", "proto", "type", "method", "protokol", "connection", "tip"},
	"user":        {"user", "username", "login", "korisnik", "account", "korisnickoime", "korisničkoime"},
	"password":    {"password", "pass", "pwd", "secret", "lozinka", "zaporka"},
	"group":       {"group", "folder", "site", "location", "path", "region", "grupa", "lokacija", "mapa"},
	"description": {"description", "desc", "comment", "comments", "notes", "note", "opis", "napomena", "model", "platform"},
	"clusters":    {"cluster", "clusters", "tags", "tag"},
	"credential":  {"credential", "credentials", "profile", "credentialprofile", "profil"},
	"jump":        {"jump", "jumphost", "bastion", "via", "proxyjump", "gateway"},
	"serial":      {"serialport", "comport", "com", "tty", "serialline", "consoleport"},
	"baud":        {"baud", "baudrate", "speed", "brzina"},
	"color":       {"color", "colour", "boja"},
}

func normHeader(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// GuessMapping matches columns to fields by header name.
func GuessMapping(headers []string) Mapping {
	m := Mapping{}
	used := map[int]bool{}
	for _, field := range TableFields {
		m[field] = -1
		for _, syn := range headerSynonyms[field] {
			for i, h := range headers {
				if !used[i] && normHeader(h) == normHeader(syn) {
					m[field] = i
					used[i] = true
					break
				}
			}
			if m[field] >= 0 {
				break
			}
		}
	}
	return m
}

// ReadTable reads a .csv/.tsv/.txt or .xlsx file.
func ReadTable(path string) (*Table, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > 32<<20 {
		return nil, errors.New("the file is too large for a device list")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx", ".xlsm":
		return readXLSX(path)
	case ".xls":
		return nil, errors.New("old .xls workbooks cannot be read; save the sheet as .xlsx or .csv in Excel")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseCSV(decodeText(data))
}

func parseCSV(text string) (*Table, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	first := text
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		first = text[:i]
	}
	// Excel in Croatian and most of Europe writes ";" because "," is the
	// decimal separator.
	delim := ','
	best := strings.Count(first, ",")
	for _, d := range []rune{';', '\t', '|'} {
		if c := strings.Count(first, string(d)); c > best {
			delim, best = d, c
		}
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	var rows [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", len(rows)+1, err)
		}
		rows = append(rows, rec)
	}
	return tableFromRows(rows)
}

func tableFromRows(rows [][]string) (*Table, error) {
	// Skip leading empty rows.
	for len(rows) > 0 && rowEmpty(rows[0]) {
		rows = rows[1:]
	}
	if len(rows) < 2 {
		return nil, errors.New("the sheet needs a header row and at least one device")
	}
	t := &Table{Headers: rows[0]}
	width := len(t.Headers)
	for _, r := range rows[1:] {
		if rowEmpty(r) {
			continue
		}
		if len(r) > width {
			width = len(r)
		}
		t.Rows = append(t.Rows, r)
	}
	for len(t.Headers) < width {
		t.Headers = append(t.Headers, fmt.Sprintf("Column %d", len(t.Headers)+1))
	}
	for i, h := range t.Headers {
		if strings.TrimSpace(h) == "" {
			t.Headers[i] = fmt.Sprintf("Column %d", i+1)
		}
	}
	return t, nil
}

func rowEmpty(r []string) bool {
	for _, c := range r {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// TableOptions control how rows become sessions.
type TableOptions struct {
	DefaultProtocol string            // "ssh" (default), "telnet" or "serial"
	Credentials     map[string]string // profile name (lower case) -> ID
	Sessions        map[string]string // existing session name (lower case) -> ID, for the jump column
}

var (
	cidrSuffix = regexp.MustCompile(`/\d{1,3}$`)
	splitList  = regexp.MustCompile(`[,;|]+`)
	splitPath  = regexp.MustCompile(`\s*[/\\>]\s*`)
)

func cell(row []string, m Mapping, field string) string {
	i, ok := m[field]
	if !ok || i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func parseNumber(s string) int {
	if s == "" {
		return 0
	}
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	if f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64); err == nil {
		return int(f)
	}
	return 0
}

// ImportTable turns spreadsheet rows into sessions under parentID.
func ImportTable(t *Table, m Mapping, parentID string, opts TableOptions) (*Result, error) {
	full := Mapping{}
	for _, f := range TableFields {
		full[f] = -1
		if i, ok := m[f]; ok && i >= 0 && i < len(t.Headers) {
			full[f] = i
		}
	}
	m = full
	if m["host"] < 0 && m["serial"] < 0 {
		return nil, errors.New("choose which column holds the IP address or host name")
	}
	b := newBuilder("spreadsheet", parentID)
	defProto := store.Protocol(strings.ToLower(opts.DefaultProtocol))
	if defProto != store.ProtoTelnet && defProto != store.ProtoSerial {
		defProto = store.ProtoSSH
	}
	jumpRows := map[*store.Node]string{}
	byName := map[string]string{}
	for line, row := range t.Rows {
		name := cell(row, m, "name")
		host := cell(row, m, "host")
		serialDev := cell(row, m, "serial")
		label := name
		if label == "" {
			label = fmt.Sprintf("row %d", line+2)
		}
		proto := defProto
		switch p := strings.ToLower(cell(row, m, "protocol")); {
		case p == "":
		case strings.HasPrefix(p, "ssh"):
			proto = store.ProtoSSH
		case strings.HasPrefix(p, "telnet"):
			proto = store.ProtoTelnet
		case p == "serial" || p == "console" || p == "com" || strings.HasPrefix(p, "rs232"):
			proto = store.ProtoSerial
		default:
			b.skip(label, p, "only SSH, Telnet and serial sessions are supported")
			continue
		}
		if proto == store.ProtoSerial && serialDev == "" {
			serialDev = host
		}
		user := cell(row, m, "user")
		if u, h, ok := strings.Cut(host, "@"); ok && proto != store.ProtoSerial {
			if user == "" {
				user = u
			}
			host = h
		}
		host = cidrSuffix.ReplaceAllString(host, "")
		if name == "" {
			name = host
			if proto == store.ProtoSerial {
				name = serialDev
			}
		}
		if name == "" {
			continue // nothing usable in this row
		}
		n := &store.Node{ID: store.NewID(), Name: name, Description: cell(row, m, "description")}
		ss := &store.Session{Protocol: proto, Host: host, User: user, Port: parseNumber(cell(row, m, "port"))}
		if proto == store.ProtoSerial {
			ss.Host, ss.Port = "", 0
			ss.Serial = &store.Serial{Device: serialDev, Baud: parseNumber(cell(row, m, "baud"))}
		}
		if proto == store.ProtoSSH {
			ss.Auth, ss.Algorithms = store.AuthAsk, store.AlgoCompatible
		}
		pw := cell(row, m, "password")
		if pw != "" {
			if proto == store.ProtoSSH {
				ss.Auth = store.AuthPassword
			}
		}
		if cname := cell(row, m, "credential"); cname != "" {
			if id, ok := opts.Credentials[strings.ToLower(cname)]; ok {
				ss.CredentialID = id
			} else {
				b.warn(name, fmt.Sprintf("credential profile %q does not exist", cname))
			}
		}
		for _, c := range splitList.Split(cell(row, m, "clusters"), -1) {
			if c = strings.TrimSpace(c); c != "" {
				ss.Clusters = append(ss.Clusters, c)
			}
		}
		if c := strings.ToLower(cell(row, m, "color")); c != "" {
			if store.ValidColor(c) {
				n.Color = c
			} else {
				b.warn(name, fmt.Sprintf("unknown colour %q", c))
			}
		}
		n.Session = ss
		var path []string
		if g := cell(row, m, "group"); g != "" {
			path = splitPath.Split(g, -1)
		}
		if b.add(path, n) {
			b.secret(store.SecretPassword(n.ID), pw)
			byName[strings.ToLower(n.Name)] = n.ID
			if j := cell(row, m, "jump"); j != "" && proto == store.ProtoSSH {
				jumpRows[n] = j
			}
		}
	}
	// The jump column names a session: one from the same sheet or one that
	// already exists.
	for n, j := range jumpRows {
		key := strings.ToLower(j)
		if id, ok := byName[key]; ok && id != n.ID {
			n.Session.JumpVia = id
		} else if id, ok := opts.Sessions[key]; ok {
			n.Session.JumpVia = id
		} else {
			b.warn(n.Name, fmt.Sprintf("jump host %q is not a session in the sheet or in YACM", j))
		}
	}
	if pwCol := m["password"]; pwCol >= 0 && len(b.res.Secrets) > 0 {
		b.warn("", "Passwords from the sheet were moved into the vault. Delete the sheet or remove that column; it holds them in clear text.")
	}
	return b.done(), nil
}
