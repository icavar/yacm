// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package importer

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// A minimal .xlsx reader: the first worksheet's cell values as text. It
// handles shared strings, inline strings, numbers and booleans, which is
// what device lists contain; formulas are read as their cached value.

const maxXLSXPart = 64 << 20

func readZipPart(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, name) {
			if f.UncompressedSize64 > maxXLSXPart {
				return nil, errors.New("workbook part is too large")
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxXLSXPart))
		}
	}
	return nil, fmt.Errorf("%s missing", name)
}

func readXLSX(p string) (*Table, error) {
	zf, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("not an Excel workbook: %w", err)
	}
	defer zf.Close()
	zr := &zf.Reader

	sheetName, sheetPath, err := firstSheet(zr)
	if err != nil {
		return nil, err
	}
	var shared []string
	if data, err := readZipPart(zr, "xl/sharedStrings.xml"); err == nil {
		shared = parseSharedStrings(data)
	}
	data, err := readZipPart(zr, sheetPath)
	if err != nil {
		return nil, err
	}
	rows, err := parseSheet(data, shared)
	if err != nil {
		return nil, err
	}
	t, err := tableFromRows(rows)
	if err != nil {
		return nil, err
	}
	t.Sheet = sheetName
	return t, nil
}

func firstSheet(zr *zip.Reader) (name, target string, err error) {
	wb, err := readZipPart(zr, "xl/workbook.xml")
	if err != nil {
		return "", "", errors.New("not an Excel workbook (xl/workbook.xml missing)")
	}
	var w struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(wb, &w); err != nil || len(w.Sheets) == 0 {
		return "", "", errors.New("the workbook has no sheets")
	}
	name = w.Sheets[0].Name
	target = "xl/worksheets/sheet1.xml"
	if rels, err := readZipPart(zr, "xl/_rels/workbook.xml.rels"); err == nil {
		var r struct {
			Rel []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		if xml.Unmarshal(rels, &r) == nil {
			for _, rel := range r.Rel {
				if rel.ID == w.Sheets[0].RID {
					t := rel.Target
					if strings.HasPrefix(t, "/") {
						target = strings.TrimPrefix(t, "/")
					} else {
						target = path.Join("xl", t)
					}
				}
			}
		}
	}
	return name, target, nil
}

func parseSharedStrings(data []byte) []string {
	var out []string
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var cur strings.Builder
	inSI, inT, inRPh := false, false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = true
			case "rPh": // phonetic hints, not part of the text
				inRPh = true
			}
		case xml.EndElement:
			switch el.Name.Local {
			case "si":
				inSI = false
				out = append(out, cur.String())
			case "t":
				inT = false
			case "rPh":
				inRPh = false
			}
		case xml.CharData:
			if inSI && inT && !inRPh {
				cur.Write(el)
			}
		}
	}
	return out
}

// colIndex converts the letters of a cell reference ("C12") to 0-based.
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			n = n*26 + int(r-'A'+1)
		} else if r >= 'a' && r <= 'z' {
			n = n*26 + int(r-'a'+1)
		} else {
			break
		}
	}
	return n - 1
}

func parseSheet(data []byte, shared []string) ([][]string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var rows [][]string
	var row []string
	var cellType, cellRef string
	var val strings.Builder
	inV, inIS, inT := false, false, false
	next := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("damaged worksheet: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "row":
				row, next = nil, 0
			case "c":
				cellType, cellRef = "", ""
				for _, a := range el.Attr {
					switch a.Name.Local {
					case "t":
						cellType = a.Value
					case "r":
						cellRef = a.Value
					}
				}
				val.Reset()
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch el.Name.Local {
			case "v":
				inV = false
			case "is":
				inIS = false
			case "t":
				inT = false
			case "c":
				idx := next
				if cellRef != "" {
					idx = colIndex(cellRef)
				}
				if idx < 0 || idx > 1000 {
					continue
				}
				text := val.String()
				switch cellType {
				case "s":
					if i, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && i >= 0 && i < len(shared) {
						text = shared[i]
					}
				case "b":
					if text == "1" {
						text = "TRUE"
					} else {
						text = "FALSE"
					}
				case "", "n":
					// Whole numbers such as ports come as "22" or "22.0".
					if f, err := strconv.ParseFloat(text, 64); err == nil && f == float64(int64(f)) {
						text = strconv.FormatInt(int64(f), 10)
					}
				}
				for len(row) < idx {
					row = append(row, "")
				}
				row = append(row, text)
				next = idx + 1
			case "row":
				rows = append(rows, row)
				if len(rows) > 50000 {
					return nil, errors.New("the sheet has more than 50,000 rows")
				}
			}
		case xml.CharData:
			if inV || (inIS && inT) {
				val.Write(el)
			}
		}
	}
	return rows, nil
}
