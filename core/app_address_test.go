// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"testing"

	"github.com/yacm-app/yacm/internal/store"
)

func TestSessionAddress(t *testing.T) {
	cases := []struct {
		s    store.Session
		user string
		want string
	}{
		{store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 22}, "admin", "admin@10.0.0.1"},
		{store.Session{Protocol: store.ProtoSSH, Host: "10.0.0.1", Port: 2222}, "admin", "admin@10.0.0.1:2222"},
		{store.Session{Protocol: store.ProtoSSH, Host: "2001:db8::1", Port: 2222}, "", "[2001:db8::1]:2222"},
		{store.Session{Protocol: store.ProtoSSH, Host: "core-sw1"}, "", "core-sw1"},
		{store.Session{Protocol: store.ProtoTelnet, Host: "10.9.9.9", Port: 23}, "admin", "10.9.9.9"},
		{store.Session{Protocol: store.ProtoTelnet, Host: "ts1", Port: 2003}, "", "ts1:2003"},
		{store.Session{Protocol: store.ProtoSerial, Serial: &store.Serial{Device: "COM3", Baud: 9600}}, "", "COM3 9600"},
	}
	for _, c := range cases {
		if got := sessionAddress(&c.s, c.user); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.s, got, c.want)
		}
	}
}
