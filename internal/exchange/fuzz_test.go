// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package exchange

import (
	"testing"

	"github.com/icavar/yacm/internal/store"
)

// A .yacm file can come from anyone: parsing, inspecting and planning its
// import must not crash, whatever it contains.
func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"format":"yacm-sessions","version":2,"nodes":[{"id":"g","name":"G","isGroup":true},{"id":"s","parentId":"g","name":"r1","session":{"protocol":"ssh","host":"10.0.0.1","port":22,"jumpVia":"s"}}],"credentials":[{"id":"c","name":"AD"}]}`))
	f.Add([]byte(`{"format":"yacm-sessions","version":1,"nodes":[]}`))
	f.Add([]byte(`{"format":"yacm-sessions","version":2,"secrets":{"kdf":{"alg":"argon2id","t":1,"m":19456,"p":1,"salt":"AAAAAAAAAAAAAAAAAAAAAA=="},"nonce":"","data":""}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := Parse(data)
		if err != nil {
			return
		}
		existing := map[string]*store.Node{"s": {ID: "s", Name: "old", Session: &store.Session{Protocol: store.ProtoSSH, Host: "h"}}}
		_ = Inspect(file, existing)
		for _, mode := range []string{"copy", "replace", "skip"} {
			for _, target := range []string{"group", "root"} {
				_, _ = PlanImport(file, existing, map[string]*store.Credential{}, ImportOptions{Target: target, GroupName: "Imported", OnConflict: mode, AddKnownHosts: true})
			}
		}
	})
}
