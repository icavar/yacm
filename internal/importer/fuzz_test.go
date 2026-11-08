// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Files from other programs come from other people: a broken or hostile one
// must never crash or hang YACM. These fuzz tests feed mutated versions of
// the sample configurations to every importer.

var fuzzKinds = []Kind{KindAsbru, KindSecureCRT, KindSecureCRTXML, KindXshell, KindMobaXterm, KindMRemoteNG, KindPuTTYReg, KindSSHConfig}

func FuzzForeign(f *testing.F) {
	seeds := map[Kind]string{
		KindAsbru:        "testdata/asbru.yml",
		KindSecureCRT:    "testdata/foreign/securecrt/Sessions/Lab/Telnet SW.ini",
		KindSecureCRTXML: "testdata/foreign/securecrt-export.xml",
		KindXshell:       "testdata/foreign/xshell/Sessions/R6.xsh",
		KindMobaXterm:    "testdata/foreign/mobaxterm.mxtsessions",
		KindMRemoteNG:    "testdata/foreign/mremoteng/confCons.xml",
		KindPuTTYReg:     "testdata/foreign/putty-sessions.reg",
		KindSSHConfig:    "testdata/foreign/ssh/config",
	}
	for i, k := range fuzzKinds {
		if data, err := os.ReadFile(seeds[k]); err == nil {
			f.Add(uint8(i), data)
		}
	}
	dir := f.TempDir() // Include in ssh config resolves against an empty folder
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		k := fuzzKinds[int(kind)%len(fuzzKinds)]
		start := time.Now()
		res, err := runData(k, filepath.Join(dir, "input"), data, "", Options{})
		if err == nil && res == nil {
			t.Fatalf("%s: nil result without error", k)
		}
		if d := time.Since(start); d > 20*time.Second {
			t.Fatalf("%s: took %v for %d bytes", k, d, len(data))
		}
	})
}

func FuzzCSV(f *testing.F) {
	f.Add("Name;IP adresa;Grupa;Korisnik\nri-core-1;10.30.0.1;Rijeka/DC;admin\n")
	f.Add("name,host,port,protocol,jump\nr1,10.0.0.1,22,ssh,\nr2,10.0.0.2,2222,ssh,r1\n")
	f.Add("\ufeffhost\ttelnet\n\"a,b\";\"x\"\"y\"\n")
	f.Fuzz(func(t *testing.T, text string) {
		tb, err := parseCSV(text)
		if err != nil {
			return
		}
		m := GuessMapping(tb.Headers)
		_, _ = ImportTable(tb, m, "", TableOptions{})
	})
}

func FuzzXLSX(f *testing.F) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Type="worksheet" Target="worksheets/s.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Hostname</t></si><si><t>IP</t></si></sst>`,
		"xl/worksheets/s.xml":        `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>r1</t></is></c><c r="B2" t="str"><v>10.0.0.1</v></c></row></sheetData></worksheet>`,
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Add(buf.Bytes())
	f.Add([]byte("PK\x03\x04not really a zip"))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(dir, "in.xlsx")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Skip()
		}
		done := make(chan struct{})
		go func() { _, _ = ReadTable(p); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("ReadTable takes over 10 s on %d bytes", len(data))
		}
	})
}
