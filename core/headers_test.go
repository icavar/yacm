// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every Go and JavaScript source file names the licence and points at the
// additional terms (GPLv3 section 7 asks for that in the source files).
func TestSourceHeaders(t *testing.T) {
	const want = "// SPDX-License-Identifier: GPL-3.0-or-later\n// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.\n"
	skip := map[string]bool{"node_modules": true, "dist": true, "third_party": true, ".git": true, "test-results": true, "dist-macos": true}
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(p)
		js := strings.HasSuffix(p, ".js") && (strings.HasPrefix(slash, "../frontend/src/") || strings.HasPrefix(slash, "../frontend/tests/") || filepath.Dir(slash) == "../frontend")
		if !strings.HasSuffix(p, ".go") && !js {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), want) {
			t.Errorf("%s: no licence header (see CONTRIBUTING.md)", slash)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
