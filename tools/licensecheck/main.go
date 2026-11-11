// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Command licensecheck makes sure every Go module YACM is built from has a
// licence that allows any use of the program, so a copyleft dependency never
// comes in unnoticed. It lists the modules of all release builds (Windows,
// Linux, macOS, with the desktop tags), reads each module's licence file
// from the module cache and fails on GPL, LGPL or AGPL, and on a licence it
// does not recognise. Run from the repository root (CI does):
//
//	go run ./tools/licensecheck
//
// With -notices FILE it also writes the licence texts of those modules and
// of the frontend's npm packages (frontend/node_modules, after npm ci) into
// FILE: the notices that must go with every package of YACM.
//
//	go run ./tools/licensecheck -notices dist/THIRD-PARTY-NOTICES.txt
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// allowed licences, recognised by a phrase of their text.
var kinds = []struct{ name, phrase string }{
	{"Apache-2.0", "Apache License"},
	{"MPL-2.0", "Mozilla Public License"},
	{"MIT", "Permission is hereby granted, free of charge"},
	{"BSD", "Redistribution and use in source and binary forms"},
	{"ISC/0BSD", "Permission to use, copy, modify, and/or distribute"},
	{"ISC/0BSD", "Permission to use, copy, modify, and distribute this software for any"},
	{"Unlicense", "This is free and unencumbered software"},
}

// Copyleft licences YACM must not depend on.
var denied = []struct{ name, phrase string }{
	{"AGPL", "GNU AFFERO GENERAL PUBLIC LICENSE"},
	{"LGPL", "GNU LESSER GENERAL PUBLIC LICENSE"},
	{"LGPL", "GNU LIBRARY GENERAL PUBLIC LICENSE"},
	{"GPL", "GNU GENERAL PUBLIC LICENSE"},
}

var builds = []struct{ goos, tags string }{
	{"windows", "desktop,production"},
	{"linux", "desktop,production,webkit2_41"},
	{"darwin", "desktop,production"},
}

func main() {
	notices := flag.String("notices", "", "write the third-party licence texts to this file")
	flag.Parse()
	root, _ := os.Getwd()
	mods := map[string]string{} // module path -> directory
	for _, b := range builds {
		cmd := exec.Command("go", "list", "-deps", "-tags", b.tags, "-f", "{{with .Module}}{{.Path}}\t{{.Dir}}{{end}}", "./...")
		cmd.Env = append(os.Environ(), "GOOS="+b.goos)
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "go list (%s): %v\n", b.goos, err)
			os.Exit(2)
		}
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			path, dir, ok := strings.Cut(sc.Text(), "\t")
			if !ok || dir == "" {
				continue
			}
			// YACM itself and the code kept in this repository (third_party).
			if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
				continue
			}
			mods[path] = dir
		}
	}
	paths := make([]string, 0, len(mods))
	for p := range mods {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	bad := 0
	for _, p := range paths {
		kind, file := classify(mods[p])
		ok := kind != "" && !strings.HasPrefix(kind, "denied:")
		mark := "ok"
		if !ok {
			mark = "NOT ALLOWED"
			bad++
			if kind == "" {
				kind = "unknown licence"
				if file == "" {
					kind = "no licence file"
				}
			}
		}
		fmt.Printf("%-12s %-55s %s\n", mark, p, kind)
	}
	if bad > 0 {
		fmt.Printf("\n%d module(s) without an allowed licence: YACM's dependencies stay permissively licensed (no GPL, LGPL or AGPL).\n", bad)
		os.Exit(1)
	}
	fmt.Printf("\n%d modules, all with permissive licences.\n", len(paths))
	if *notices != "" {
		if err := writeNotices(*notices, root, paths, mods); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Printf("Licence texts written to %s.\n", *notices)
	}
}

// writeNotices writes the licence (and NOTICE) files of the Go modules and
// of the frontend's npm dependencies, one section each.
func writeNotices(out, root string, paths []string, mods map[string]string) error {
	var b strings.Builder
	b.WriteString("YACM includes the following third-party software. Each part is used\nunder its own licence, given below in full. YACM itself is licensed under\nthe GNU GPL version 3 or later with the terms in NOTICE.md.\n")
	section := func(name, dir string, required bool) error {
		files := licenceFiles(dir)
		if len(files) == 0 {
			if required {
				return fmt.Errorf("%s: no licence file in %s", name, dir)
			}
			return nil
		}
		fmt.Fprintf(&b, "\n\n==============================================================================\n%s\n==============================================================================\n", name)
		for _, f := range files {
			t, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				return err
			}
			b.WriteString("\n" + strings.TrimSpace(strings.ReplaceAll(string(t), "\r\n", "\n")) + "\n")
		}
		return nil
	}
	b.WriteString("\n\nGo modules (the program)\n")
	if goroot, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
		if err := section("Go standard library and runtime", strings.TrimSpace(string(goroot)), true); err != nil {
			return err
		}
	}
	for _, p := range paths {
		name := p
		if _, v, ok := strings.Cut(filepath.Base(mods[p]), "@"); ok {
			name += " " + v
		}
		if err := section(name, mods[p], true); err != nil {
			return err
		}
	}
	// The repository keeps one stub module of its own (third_party): it is
	// YACM's code, under YACM's licence.
	b.WriteString("\n\nnpm packages (the window: terminal and fonts)\n")
	pkg, err := os.ReadFile(filepath.Join(root, "frontend", "package.json"))
	if err != nil {
		return err
	}
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(pkg, &pj); err != nil {
		return err
	}
	deps := make([]string, 0, len(pj.Dependencies))
	for d := range pj.Dependencies {
		deps = append(deps, d)
	}
	sort.Strings(deps)
	for _, d := range deps {
		dir := filepath.Join(root, "frontend", "node_modules", filepath.FromSlash(d))
		name := d
		if v, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			var p struct{ Version string }
			if json.Unmarshal(v, &p) == nil && p.Version != "" {
				name += " " + p.Version
			}
		}
		if err := section(name, dir, true); err != nil {
			return fmt.Errorf("%w (run npm ci in frontend first)", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, []byte(b.String()), 0o644)
}

// licenceFiles lists the licence and notice files of a module or package.
func licenceFiles(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		n := strings.ToUpper(e.Name())
		if !e.IsDir() && (strings.HasPrefix(n, "LICENSE") || strings.HasPrefix(n, "LICENCE") || strings.HasPrefix(n, "COPYING") || strings.HasPrefix(n, "NOTICE")) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// classify reads the licence file of a module directory.
func classify(dir string) (kind, file string) {
	entries, _ := os.ReadDir(dir)
	var texts []string
	for _, e := range entries {
		n := strings.ToUpper(e.Name())
		if e.IsDir() || !(strings.HasPrefix(n, "LICENSE") || strings.HasPrefix(n, "LICENCE") || strings.HasPrefix(n, "COPYING")) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil {
			texts = append(texts, string(b))
			file = e.Name()
		}
	}
	// The GNU licences name themselves in their title (the GPL mentions the
	// AGPL further down, so only the top of each file counts).
	for _, d := range denied {
		for _, t := range texts {
			if len(t) > 400 {
				t = t[:400]
			}
			if strings.Contains(strings.ToUpper(t), d.phrase) {
				return "denied: " + d.name, file
			}
		}
	}
	norm := strings.Join(strings.Fields(strings.Join(texts, "\n")), " ")
	for _, k := range kinds {
		if strings.Contains(norm, k.phrase) {
			return k.name, file
		}
	}
	return "", file
}
