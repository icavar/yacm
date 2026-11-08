// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Generates rsrc_windows_*.syso (icon, manifest, version info) so a plain
// `go build` produces a proper Windows executable:
//
//	go run ./tools/winres
package main

import (
	"image"
	_ "image/png"
	"log"
	"os"
	"regexp"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

// release is the version number written into both executables: the numbers
// of appVersion in core/version.go (Windows versions are four numbers, so a
// release candidate "0.9.0-rc.1" is 0.9.0.0 there).
var release = func() string {
	b, err := os.ReadFile("core/version.go")
	if err != nil {
		log.Fatal(err)
	}
	m := regexp.MustCompile(`appVersion\s*=\s*"(\d+\.\d+\.\d+)`).FindSubmatch(b)
	if m == nil {
		log.Fatal("no appVersion in core/version.go")
	}
	return string(m[1])
}()

func main() {
	f, err := os.Open("build/appicon.png")
	if err != nil {
		log.Fatal(err)
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}
	icon, err := winres.NewIconFromResizedImage(img, nil)
	if err != nil {
		log.Fatal(err)
	}
	man, err := os.ReadFile("build/yacm.exe.manifest")
	if err != nil {
		log.Fatal(err)
	}
	// The first assemblyIdentity is YACM's own (the others are dependencies).
	if loc := regexp.MustCompile(`<assemblyIdentity[^>]*\sversion="([^"]*)"`).FindSubmatchIndex(man); loc != nil {
		man = append(append(append([]byte{}, man[:loc[2]]...), release+".0"...), man[loc[3]:]...)
	}

	app := winres.ResourceSet{}
	must(app.SetIcon(winres.ID(1), icon))
	app.Set(winres.RT_MANIFEST, winres.ID(1), 0, man)
	app.SetVersionInfo(info("YACM, Yet Another Connection Manager", "YACM.exe"))
	write(&app, ".")

	// The demo device is a console program; its description says what it is
	// so nobody takes it for something suspicious.
	demo := winres.ResourceSet{}
	must(demo.SetIcon(winres.ID(1), icon))
	demo.SetVersionInfo(info("YACM demo device - local SSH/telnet test server for trying YACM", "yacm-demo-device.exe"))
	write(&demo, "cmd/yacm-demo-device")
}

func info(desc, file string) version.Info {
	vi := version.Info{}
	vi.SetFileVersion(release + ".0")
	vi.SetProductVersion(release + ".0")
	vi.Set(0, version.ProductName, "YACM")
	vi.Set(0, version.FileDescription, desc)
	vi.Set(0, version.OriginalFilename, file)
	vi.Set(0, version.LegalCopyright, "GPL-3.0-or-later")
	return vi
}

func write(rs *winres.ResourceSet, dir string) {
	for _, arch := range []winres.Arch{winres.ArchAMD64, winres.ArchARM64} {
		out, err := os.Create(dir + "/rsrc_windows_" + string(arch) + ".syso")
		if err != nil {
			log.Fatal(err)
		}
		if err := rs.WriteObject(out, arch); err != nil {
			log.Fatal(err)
		}
		out.Close()
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
