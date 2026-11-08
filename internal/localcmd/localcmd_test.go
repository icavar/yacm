// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package localcmd

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunPassesSessionValuesAsVariables(t *testing.T) {
	cmd := `echo "$YACM_NAME $YACM_HOST $YACM_PORT $YACM_USER"`
	if runtime.GOOS == "windows" {
		cmd = `echo %YACM_NAME% %YACM_HOST% %YACM_PORT% %YACM_USER%`
	}
	r := Run(context.Background(), cmd, Env{Name: "core-sw1", Host: "10.0.0.1", Port: 22, User: "ivan"}, 0)
	if r.Error != "" || strings.TrimSpace(r.Output) != "core-sw1 10.0.0.1 22 ivan" {
		t.Fatalf("%+v", r)
	}
	// A host name is a value, never shell syntax.
	r = Run(context.Background(), cmd, Env{Host: "$(touch /tmp/yacm-pwned)"}, 0)
	if !strings.Contains(r.Output, "$(touch") {
		t.Fatalf("the value was not passed literally: %q", r.Output)
	}
}

func TestRunExitCodeAndTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	r := Run(context.Background(), "exit 3", Env{}, 0)
	if r.ExitCode != 3 {
		t.Fatalf("exit code %+v", r)
	}
	start := time.Now()
	r = Run(context.Background(), "sleep 5", Env{}, 200*time.Millisecond)
	if time.Since(start) > 3*time.Second || r.Error == "" {
		t.Fatalf("the timeout did not stop the command: %+v", r)
	}
}
