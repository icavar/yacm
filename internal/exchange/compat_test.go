// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package exchange

import (
	"strings"
	"testing"

	"github.com/yacm-app/yacm/internal/store"
)

// A .yacm file from a newer YACM imports what this version understands:
// sessions of unknown connection types are left out with a reason, and new
// members do not come into your own sessions from someone else's file.
func TestImportFromNewerYACM(t *testing.T) {
	data := `{"format":"yacm-sessions","version":2,"app":"YACM 2.0.0","scope":"all",
	  "nodes":[
	    {"id":"g1","isGroup":true,"name":"DC","order":0,"pinned":true},
	    {"id":"s1","parentId":"g1","name":"core-sw1","order":0,"session":{"protocol":"ssh","host":"10.0.0.1","port":22,
	       "before":[{"command":"echo hi","confirm":false,"shell":"pwsh"}],"x11":true}},
	    {"id":"s2","parentId":"g1","name":"jump-win","order":1,"session":{"protocol":"rdp","host":"10.0.0.50","rdp":{"width":1920}}}
	  ],
	  "credentials":[{"id":"c1","name":"AD","user":"ivan","auth":"password","domain":"CORP"}]}`
	f, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, err := PlanImport(f, map[string]*store.Node{}, map[string]*store.Credential{}, ImportOptions{Target: "root", OnConflict: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range p.Insert {
		names = append(names, n.Name)
		if store.HasUnknown(n) {
			t.Errorf("%s still carries members from the newer version", n.Name)
		}
	}
	if strings.Join(names, ",") != "DC,core-sw1" {
		t.Fatalf("imported %v", names)
	}
	if len(p.Report.Skipped) != 1 || p.Report.Skipped[0].Name != "jump-win" || !strings.Contains(p.Report.Skipped[0].Reason, "newer version") {
		t.Fatalf("skipped: %+v", p.Report.Skipped)
	}
	for _, c := range p.Credentials {
		if len(c.Extra) > 0 {
			t.Fatal("profile kept members from the newer version")
		}
	}
	if s := p.Insert[1].Session; len(s.Before) != 1 || !s.Before[0].Confirm {
		t.Fatal("local commands from a file still have to be confirmed")
	}
}
