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
package main

import (
	"bufio"
	"bytes"
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
