// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build linux

package fileserver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// LowPortsConf is the file AllowPort writes: the setting stays after a
// restart and after YACM updates.
const LowPortsConf = "/etc/sysctl.d/60-yacm-file-servers.conf"

const unprivPortStart = "/proc/sys/net/ipv4/ip_unprivileged_port_start"

// AllowPort lets programs without administrator rights listen on port and
// above (the Linux setting net.ipv4.ip_unprivileged_port_start), so the TFTP
// server can use port 69. The system asks for the administrator password
// (pkexec shows the desktop's password dialog).
func AllowPort(port int) error {
	if port < 1 || port > 1023 {
		return fmt.Errorf("port %d needs no administrator rights", port)
	}
	if cur, err := os.ReadFile(unprivPortStart); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(cur))); err == nil && n <= port {
			return nil // already allowed
		}
	}
	pk, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("pkexec is not installed: as administrator, run " + allowCommand(port))
	}
	script := fmt.Sprintf("printf '# Written by YACM: lets its TFTP server use port %d without administrator rights.\\nnet.ipv4.ip_unprivileged_port_start = %d\\n' > %s && sysctl -q -w net.ipv4.ip_unprivileged_port_start=%d", port, port, LowPortsConf, port)
	out, err := exec.Command(pk, "/bin/sh", "-c", script).CombinedOutput()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && (ee.ExitCode() == 126 || ee.ExitCode() == 127):
		return errors.New("not allowed: the password dialog was cancelled or refused")
	case err != nil:
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func allowCommand(port int) string {
	return fmt.Sprintf("echo 'net.ipv4.ip_unprivileged_port_start = %d' | sudo tee %s && sudo sysctl --system", port, LowPortsConf)
}

// LowPortsConfName is where the setting is kept.
func LowPortsConfName() string { return LowPortsConf }
