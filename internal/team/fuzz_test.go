// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package team

import "testing"

// Team library files live on shares anyone with write access can change.
func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"format":"yacm-team-library","version":1,"name":"NOC","revision":3,"logins":[{"id":"l1","name":"AD account"}],"nodes":[{"id":"g","name":"Core","isGroup":true},{"id":"r1","parentId":"g","name":"r1","session":{"protocol":"ssh","host":"10.0.0.1","port":22,"credentialId":"l1","before":[{"command":"calc"}]}}]}`))
	f.Add([]byte(`{"format":"yacm-team-library","version":99}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		lib, err := Parse(data)
		if err != nil {
			return
		}
		// Whatever the file said, nothing that could run or hold a secret survives.
		for _, n := range lib.Nodes {
			if n == nil || n.Session == nil {
				continue
			}
			if len(n.Session.Before) > 0 || len(n.Session.After) > 0 {
				t.Fatalf("local commands survived parsing in %q", n.Name)
			}
		}
		_ = lib.Sessions()
	})
}
