// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"crypto/md5"
	"crypto/rc4"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/icavar/yacm/internal/store"
)

// parseIni reads a classic [section] key=value file (case-insensitive).
func parseIni(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	cur := ""
	out[cur] = map[string]string{}
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			cur = strings.ToLower(t[1 : len(t)-1])
			if out[cur] == nil {
				out[cur] = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		out[cur][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

func iniGet(ini map[string]map[string]string, section, key string) string {
	if s, ok := ini[strings.ToLower(section)]; ok {
		return s[strings.ToLower(key)]
	}
	return ""
}

// ---- password decryption ----------------------------------------------------
//
// Xshell (public analysis: HyperSine/how-does-Xshell-encrypt-password,
// JDArmy/SharpXDecrypt) encrypts with RC4. The key depends on the version:
//   < 5.1        MD5("!X@s#h$e%l^l&")
//   5.1          SHA-256(user SID)
//   5.2 - 6.x    SHA-256(user name + SID), ciphertext = RC4(pw) | SHA-256(pw)
//   7.x and up   SHA-256(reverse(reverse(user name) + SID)), same layout
//   master pw    SHA-256(master password), same layout

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

type xshKey struct {
	key      []byte
	checksum bool
}

func xshellKeys(version float64, opts Options) []xshKey {
	var keys []xshKey
	add := func(k []byte, sum bool) { keys = append(keys, xshKey{k, sum}) }
	if opts.Password != "" {
		k := sha256.Sum256([]byte(opts.Password))
		add(k[:], true)
	}
	if opts.WinSID != "" {
		k7 := sha256.Sum256([]byte(reverseString(reverseString(opts.WinUser) + opts.WinSID)))
		k6 := sha256.Sum256([]byte(opts.WinUser + opts.WinSID))
		k51 := sha256.Sum256([]byte(opts.WinSID))
		if version >= 7 {
			add(k7[:], true)
			add(k6[:], true)
		} else {
			add(k6[:], true)
			add(k7[:], true)
		}
		if version > 0 && version < 5.2 {
			add(k51[:], false)
		}
	}
	if version > 0 && version < 5.1 {
		k := md5.Sum([]byte("!X@s#h$e%l^l&"))
		add(k[:], false)
	}
	return keys
}

func xshellDecrypt(b64 string, version float64, opts Options) (string, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(data) == 0 {
		return "", errors.New("bad ciphertext")
	}
	for _, k := range xshellKeys(version, opts) {
		c, err := rc4.NewCipher(k.key)
		if err != nil {
			continue
		}
		if k.checksum {
			if len(data) <= sha256.Size {
				continue
			}
			ct, sum := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
			pt := make([]byte, len(ct))
			c.XORKeyStream(pt, ct)
			if got := sha256.Sum256(pt); string(got[:]) == string(sum) {
				return string(pt), nil
			}
			continue
		}
		pt := make([]byte, len(data))
		c.XORKeyStream(pt, data)
		if utf8.Valid(pt) && printable(string(pt)) {
			return string(pt), nil
		}
	}
	return "", errors.New("cannot decrypt")
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return false
		}
	}
	return s != ""
}

// ---- sessions -------------------------------------------------------------------

func importXshellDir(dir, parentID string, opts Options) (*Result, error) {
	b := newBuilder("Xshell", parentID)
	found := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".xsh") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		data, err := readFile(p)
		if err != nil {
			b.warn(rel, err.Error())
			return nil
		}
		found++
		name := strings.TrimSuffix(parts[len(parts)-1], filepath.Ext(p))
		xshellFile(b, parts[:len(parts)-1], name, decodeText(data), opts)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == 0 {
		return nil, errors.New("no Xshell sessions (.xsh) found")
	}
	if opts.WinSID == "" && b.res.Report.SecretsFailed > 0 {
		b.warn("", "Xshell ties saved passwords to the Windows account that saved them, so they can only be decrypted when YACM runs on that Windows account.")
	}
	return b.done(), nil
}

func xshellFile(b *builder, path []string, name, text string, opts Options) {
	ini := parseIni(text)
	version, _ := strconv.ParseFloat(iniGet(ini, "SessionInfo", "Version"), 64)
	proto := strings.ToUpper(iniGet(ini, "CONNECTION", "Protocol"))
	n := &store.Node{ID: store.NewID(), Name: name, Description: iniGet(ini, "SessionInfo", "Description")}
	ss := &store.Session{}
	port, _ := strconv.Atoi(iniGet(ini, "CONNECTION", "Port"))
	switch proto {
	case "SSH", "":
		ss.Protocol, ss.Port = store.ProtoSSH, port
	case "TELNET":
		ss.Protocol, ss.Port = store.ProtoTelnet, port
	case "SERIAL":
		ss.Protocol = store.ProtoSerial
		dev := iniGet(ini, "CONNECTION:SERIAL", "Port")
		if _, err := strconv.Atoi(dev); err == nil {
			dev = "COM" + dev
		}
		baud, _ := strconv.Atoi(iniGet(ini, "CONNECTION:SERIAL", "BaudRate"))
		bits, _ := strconv.Atoi(iniGet(ini, "CONNECTION:SERIAL", "DataBits"))
		par, _ := strconv.Atoi(iniGet(ini, "CONNECTION:SERIAL", "Parity"))
		stop, _ := strconv.Atoi(iniGet(ini, "CONNECTION:SERIAL", "StopBits"))
		ss.Serial = &store.Serial{Device: dev, Baud: baud, DataBits: bits,
			Parity: []string{"none", "odd", "even", "mark", "space"}[clampIndex(par, 5)], StopBits: []string{"1", "1.5", "2"}[clampIndex(stop, 3)]}
		b.warn(name, "check the serial port and line settings after importing")
	default:
		b.skip(name, iniGet(ini, "CONNECTION", "Protocol"), "only SSH, Telnet and serial sessions are supported")
		return
	}
	ss.Host = iniGet(ini, "CONNECTION", "Host")
	ss.User = iniGet(ini, "CONNECTION:AUTHENTICATION", "UserName")
	pw := ""
	if enc := iniGet(ini, "CONNECTION:AUTHENTICATION", "Password"); enc != "" {
		if p, err := xshellDecrypt(enc, version, opts); err == nil {
			pw = p
		} else {
			b.failed()
		}
	}
	if ss.Protocol == store.ProtoSSH {
		ss.Algorithms = store.AlgoCompatible
		switch {
		case iniGet(ini, "CONNECTION:AUTHENTICATION", "UserKey") != "":
			ss.Auth = store.AuthAgent
			b.warn(name, "uses the key \""+iniGet(ini, "CONNECTION:AUTHENTICATION", "UserKey")+"\" from Xshell's own key store; export it (Tools > User Key Manager > Export) and set it as the key file, or load it into your SSH agent")
		case pw != "":
			ss.Auth = store.AuthPassword
		default:
			ss.Auth = store.AuthAsk
		}
		if iniGet(ini, "CONNECTION:PROXY", "Proxy") != "" {
			b.warn(name, "proxy / jump host settings were not imported")
		}
	}
	n.Session = ss
	if b.add(path, n) {
		b.secret(store.SecretPassword(n.ID), pw)
	}
}
