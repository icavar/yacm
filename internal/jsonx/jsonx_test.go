// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package jsonx

import (
	"encoding/json"
	"strings"
	"testing"
)

type inner struct {
	Port  int     `json:"port"`
	Extra Unknown `json:"-"`
}

func (s *inner) UnmarshalJSON(b []byte) error {
	type plain inner
	return Unmarshal(b, (*plain)(s), &s.Extra)
}

func (s inner) MarshalJSON() ([]byte, error) {
	type plain inner
	return Marshal(plain(s), s.Extra)
}

type outer struct {
	Name   string  `json:"name"`
	Note   string  `json:"note,omitempty"`
	Hidden string  `json:"-"`
	Inner  *inner  `json:"inner,omitempty"`
	Items  []inner `json:"items"`
	Extra  Unknown `json:"-"`
}

func (s *outer) UnmarshalJSON(b []byte) error {
	type plain outer
	return Unmarshal(b, (*plain)(s), &s.Extra)
}

func (s outer) MarshalJSON() ([]byte, error) {
	type plain outer
	return Marshal(plain(s), s.Extra)
}

func TestRoundTripKeepsUnknownMembers(t *testing.T) {
	in := `{"name":"r1","rdp":{"gateway":"gw1","width":1920},"inner":{"port":22,"proxy":"socks5://x"},
		"items":[{"port":1,"tag":"a"},{"port":2}],"zeta":[1,2,3],"NAME":"ignored-dup","-":"dash"}`
	var o outer
	if err := json.Unmarshal([]byte(in), &o); err != nil {
		t.Fatal(err)
	}
	if o.Name == "" || o.Inner.Port != 22 || len(o.Items) != 2 {
		t.Fatalf("known fields: %+v", o)
	}
	if _, ok := o.Extra["NAME"]; ok {
		t.Fatal("a case variant of a known name must not be kept as unknown")
	}
	for _, k := range []string{"rdp", "zeta", "-"} {
		if _, ok := o.Extra[k]; !ok {
			t.Fatalf("lost unknown member %q: %v", k, o.Extra)
		}
	}
	o.Name = "r1-renamed"
	out, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"name":"r1-renamed"`, `"rdp":{"gateway":"gw1","width":1920}`, `"proxy":"socks5://x"`, `"tag":"a"`, `"zeta":[1,2,3]`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
	if strings.Count(s, `"name"`) != 1 {
		t.Fatalf("name written twice: %s", s)
	}
	// And again: a second round trip changes nothing.
	var o2 outer
	if err := json.Unmarshal(out, &o2); err != nil {
		t.Fatal(err)
	}
	out2, _ := json.Marshal(o2)
	if string(out2) != s {
		t.Fatalf("not stable:\n%s\n%s", s, out2)
	}
}

func TestNoUnknownMembersWritesPlainJSON(t *testing.T) {
	o := outer{Name: "x", Items: []inner{}}
	got, _ := json.Marshal(o)
	if string(got) != `{"name":"x","items":[]}` {
		t.Fatalf("got %s", got)
	}
	var e struct {
		Extra Unknown `json:"-"`
	}
	b, err := Marshal(struct{}{}, Unknown{"a": json.RawMessage(`1`), "bad": json.RawMessage(`{`)})
	if err != nil || string(b) != `{"a":1}` {
		t.Fatalf("empty object with extras: %s %v %v", b, err, e)
	}
}

func TestNullAndKeep(t *testing.T) {
	var o *outer
	if err := json.Unmarshal([]byte(`null`), &o); err != nil || o != nil {
		t.Fatalf("null: %v %v", o, err)
	}
	old := Unknown{"a": json.RawMessage(`1`)}
	if got := Keep(nil, old); len(got) != 1 {
		t.Fatal("Keep should fall back to old")
	}
	if got := Keep(Unknown{"b": json.RawMessage(`2`)}, old); got["b"] == nil {
		t.Fatal("Keep should prefer cur")
	}
}
