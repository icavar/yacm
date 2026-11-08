// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

package tunnel

import "net/url"

type urlT = url.URL

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }
