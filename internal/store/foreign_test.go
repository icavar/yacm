// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package store

import "testing"

func TestNetworkPaths(t *testing.T) {
	for _, p := range []string{`\\srv\s\k`, `//srv/s/k`, `\\.\UNC\srv\s\k`, `//./UNC/srv/s/k`, `\\.\GLOBALROOT\Device\Mup\srv\s\k`, `\\?\UNC\srv\s`, `\??\UNC\srv\s`, ` \\srv\s`, `UNC\srv\share`, `\\.\pipe\x`, `\\?\C:\..\..\UNC\srv\s`, `\\.\COM1\..\..\UNC\srv`} {
		if !IsNetworkPath(p) {
			t.Errorf("%q not seen as a network path", p)
		}
	}
	for _, p := range []string{`C:\Users\x\id`, `~/.ssh/id`, `/home/x/id`, `\\.\COM10`, `relative\key`, `\\?\C:\Users\x\id`} {
		if IsNetworkPath(p) {
			t.Errorf("%q seen as a network path", p)
		}
	}
	for d, ok := range map[string]bool{"COM3": true, `\\.\COM10`: true, "/dev/ttyUSB0": true, "/dev/serial/by-id/usb-FTDI_A1-if00": true, "/dev/cu.usbserial-1410": true,
		`UNC\srv\share`: false, `\\.\UNC\srv\s`: false, `\\srv\s`: false, "../x": false} {
		if SerialDeviceOK(d) != ok {
			t.Errorf("SerialDeviceOK(%q) = %v", d, !ok)
		}
	}
	n := &Node{Name: "s", Session: &Session{Protocol: ProtoSerial, Serial: &Serial{Device: `UNC\srv\share`}}}
	if _, d := FromElsewhere(n); len(d) != 1 || n.Session.Serial.Device != "" {
		t.Fatalf("serial device kept: %+v", n.Session.Serial)
	}
}
