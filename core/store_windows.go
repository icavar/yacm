// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build windows

package core

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetCurrentPackageFamilyName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentPackageFamilyName")

// storePackage is the package family name when YACM runs from an MSIX
// package (the Microsoft Store), "" for the zip.
func storePackage() string {
	if procGetCurrentPackageFamilyName.Find() != nil {
		return ""
	}
	var buf [256]uint16
	n := uint32(len(buf))
	if r, _, _ := procGetCurrentPackageFamilyName.Call(uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return "" // APPMODEL_ERROR_NO_PACKAGE: not packaged
	}
	return windows.UTF16ToString(buf[:])
}
