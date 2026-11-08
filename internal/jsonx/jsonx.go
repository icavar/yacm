// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Package jsonx keeps JSON object members that a struct does not know, so
// data written by a newer YACM survives being loaded and saved by an older
// one (forward compatibility).
//
// A type opts in with a field "Extra jsonx.Unknown" tagged json:"-" and two
// methods that decode and encode through a method-less copy of itself:
//
//	func (s *Session) UnmarshalJSON(b []byte) error {
//		type plain Session
//		return jsonx.Unmarshal(b, (*plain)(s), &s.Extra)
//	}
//
//	func (s Session) MarshalJSON() ([]byte, error) {
//		type plain Session
//		return jsonx.Marshal(plain(s), s.Extra)
//	}
package jsonx

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Unknown holds object members a type has no field for, as raw JSON.
type Unknown map[string]json.RawMessage

var known sync.Map // reflect.Type -> map[string]bool (lower-case JSON names)

// knownNames returns the lower-case JSON member names that the fields of the
// struct type t use (encoding/json matches names without regard to case).
func knownNames(t reflect.Type) map[string]bool {
	if v, ok := known.Load(t); ok {
		return v.(map[string]bool)
	}
	names := map[string]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				ft := f.Type
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					walk(ft)
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			names[strings.ToLower(name)] = true
		}
	}
	walk(t)
	known.Store(t, names)
	return names
}

// Unmarshal decodes b into v (a pointer to a struct without its own
// UnmarshalJSON) and stores the members v has no field for in extra.
func Unmarshal(b []byte, v any, extra *Unknown) error {
	if err := json.Unmarshal(b, v); err != nil {
		return err
	}
	*extra = nil
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &all); err != nil {
		return nil
	}
	names := knownNames(reflect.TypeOf(v).Elem())
	for k := range all {
		if names[strings.ToLower(k)] {
			delete(all, k)
		}
	}
	if len(all) > 0 {
		*extra = all
	}
	return nil
}

// Marshal encodes v (a struct without its own MarshalJSON) and appends the
// extra members after its own, in name order. A member v already writes is
// never written twice.
func Marshal(v any, extra Unknown) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	b = bytes.TrimSpace(b)
	if len(b) < 2 || b[0] != '{' || b[len(b)-1] != '}' {
		return b, nil
	}
	names := knownNames(reflect.TypeOf(v))
	keys := make([]string, 0, len(extra))
	for k, raw := range extra {
		if names[strings.ToLower(k)] || !json.Valid(raw) {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return b, nil
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.Write(b[:len(b)-1])
	empty := len(bytes.TrimSpace(b[1:len(b)-1])) == 0
	for _, k := range keys {
		if !empty {
			out.WriteByte(',')
		}
		empty = false
		kb, _ := json.Marshal(k)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(extra[k])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// Keep returns cur, or old when cur is empty: used when a copy that went
// through the UI (which may drop members it does not know) replaces the
// stored one.
func Keep(cur, old Unknown) Unknown {
	if len(cur) > 0 {
		return cur
	}
	return old
}
