// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build windows

package importer

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const puttyRegPath = `Software\SimonTatham\PuTTY\Sessions`

func puttyLiveAvailable() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, puttyRegPath, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	names, _ := k.ReadSubKeyNames(-1)
	return len(names) > 0
}

// importPuTTYLive reads PuTTY sessions straight from this user's registry.
func importPuTTYLive(parentID string) (*Result, error) {
	root, err := registry.OpenKey(registry.CURRENT_USER, puttyRegPath, registry.READ)
	if err != nil {
		return nil, errors.New("no PuTTY sessions found on this computer")
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	var sessions []puttyValues
	for _, name := range names {
		k, err := registry.OpenKey(root, name, registry.READ)
		if err != nil {
			continue
		}
		v := puttyValues{name: puttyName(name), str: map[string]string{}, num: map[string]int{}}
		vals, _ := k.ReadValueNames(-1)
		for _, vn := range vals {
			key := strings.ToLower(vn)
			if s, _, err := k.GetStringValue(vn); err == nil {
				v.str[key] = s
			} else if n, _, err := k.GetIntegerValue(vn); err == nil {
				v.num[key] = int(int32(uint32(n)))
			}
		}
		k.Close()
		sessions = append(sessions, v)
	}
	if len(sessions) == 0 {
		return nil, errors.New("no PuTTY sessions found on this computer")
	}
	return puttyImport("PuTTY", sessions, parentID), nil
}
