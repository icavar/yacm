// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

//go:build !windows

package core

// storePackage is "" outside Windows: only the Windows MSIX is a store package.
func storePackage() string { return "" }
