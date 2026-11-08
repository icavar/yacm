// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/icavar/yacm/internal/store"
)

func TestCSVCroatianExcel(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "uredaji.csv")
	// Excel "CSV (Comma delimited)" in a Croatian locale: semicolons, CP1250-ish
	// text saved as UTF-8 with BOM.
	data := "\ufeffNaziv;IP adresa;Port;Protokol;Korisnik;Lokacija;Opis;Lozinka;Jump\r\n" +
		"zg-core-1;10.10.0.1/24;;SSH;admin;Zagreb/DC1;Core switch;tajna;bastion\r\n" +
		";10.10.0.2;2222;ssh;;Zagreb/DC1;;;\r\n" +
		"bastion;192.0.2.10;22;ssh;ivan;Zagreb;;;\r\n" +
		"stari-ruter;10.0.0.9;23;telnet;;Split;;;\r\n" +
		"konzola;COM3;;serial;;;;;\r\n" +
		"rdp-host;10.0.0.50;3389;rdp;;;;;\r\n" +
		";;;;;;;;\r\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := ReadTable(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 6 || len(tbl.Headers) != 9 {
		t.Fatalf("rows %d headers %v", len(tbl.Rows), tbl.Headers)
	}
	m := GuessMapping(tbl.Headers)
	want := map[string]int{"name": 0, "host": 1, "port": 2, "protocol": 3, "user": 4, "group": 5, "description": 6, "password": 7, "jump": 8, "clusters": -1}
	for f, i := range want {
		if m[f] != i {
			t.Fatalf("mapping %s = %d, want %d (%v)", f, m[f], i, m)
		}
	}
	res, err := ImportTable(tbl, m, "root", TableOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Sessions != 5 || len(res.Report.Skipped) != 1 {
		t.Fatalf("report %+v", res.Report)
	}
	byName := map[string]*store.Node{}
	for _, n := range res.Nodes {
		byName[n.Name] = n
	}
	core := byName["zg-core-1"]
	if core == nil || core.Session.Host != "10.10.0.1" || core.Session.Port != 22 || core.Session.User != "admin" || core.Description != "Core switch" {
		t.Fatalf("core: %+v", core)
	}
	if res.Secrets[store.SecretPassword(core.ID)] != "tajna" {
		t.Fatal("password must go to the vault")
	}
	if core.Session.JumpVia != byName["bastion"].ID {
		t.Fatal("jump column must point to the bastion row")
	}
	if n := byName["10.10.0.2"]; n == nil || n.Session.Port != 2222 {
		t.Fatal("a row without a name is named after its address")
	}
	if n := byName["konzola"]; n == nil || n.Session.Serial.Device != "COM3" {
		t.Fatal("serial row")
	}
	if byName["stari-ruter"].Session.Protocol != store.ProtoTelnet {
		t.Fatal("telnet row")
	}
	if dc := byName["DC1"]; dc == nil || !dc.IsGroup || byName["Zagreb"] == nil || dc.ParentID != byName["Zagreb"].ID {
		t.Fatal("Zagreb/DC1 must become nested groups")
	}
}

func TestXLSX(t *testing.T) {
	p := filepath.Join(t.TempDir(), "devices.xlsx")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	add := func(name, body string) {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	add("xl/workbook.xml", `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Inventory" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="worksheet" Target="worksheets/inv.xml"/></Relationships>`)
	add("xl/sharedStrings.xml", `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Hostname</t></si><si><t>Mgmt IP</t></si><si><t>Port</t></si><si><r><t>Site</t></r><r><t xml:space="preserve"> </t></r></si><si><t>sw-01</t></si><si><t>10.1.1.1</t></si><si><t>Rijeka</t></si></sst>`)
	add("xl/worksheets/inv.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
	<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" t="s"><v>3</v></c></row>
	<row r="2"><c r="A2" t="s"><v>4</v></c><c r="B2" t="s"><v>5</v></c><c r="C2"><v>22.0</v></c><c r="D2" t="s"><v>6</v></c></row>
	<row r="3"><c r="A3" t="inlineStr"><is><t>sw-02</t></is></c><c r="B3" t="str"><v>10.1.1.2</v></c><c r="D3" t="s"><v>6</v></c></row>
	</sheetData></worksheet>`)
	zw.Close()
	f.Close()

	tbl, err := ReadTable(p)
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Sheet != "Inventory" || len(tbl.Rows) != 2 || tbl.Rows[0][2] != "22" || tbl.Rows[1][1] != "10.1.1.2" || tbl.Rows[1][2] != "" {
		t.Fatalf("table %+v", tbl)
	}
	m := GuessMapping(tbl.Headers)
	if m["name"] != 0 || m["host"] != 1 || m["group"] != 3 {
		t.Fatalf("mapping %v (headers %q)", m, tbl.Headers)
	}
	res, err := ImportTable(tbl, m, "", TableOptions{})
	if err != nil || res.Report.Sessions != 2 {
		t.Fatalf("import: %v %+v", err, res)
	}
}
