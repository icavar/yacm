// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// What's new, shown once after an update and from Settings > About.
// It starts with 1.1: 0.9 is the first public release and up to 1.0 only
// fixes go out, so there is nothing to list before that. Newest first;
// short, user-facing lines, the user guide has the details:
//   { version: '1.1.0', items: [['Title', 'One or two sentences.'], …] },
export const WHATS_NEW = [];

// cmp compares the numbers of two versions; a release candidate (1.1.0-rc.1)
// counts as its version, so its testers see that version's notes.
const cmp = (a, b) => {
  const nums = v => String(v).split('-')[0].split('.').map(Number);
  const pa = nums(a), pb = nums(b);
  for (let i = 0; i < 3; i++) { if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0); }
  return 0;
};

// whatsNewFor lists the entries new since `since` (or just the current
// version), at most two versions back.
export function whatsNewFor(current, since = '') {
  return WHATS_NEW.filter(v => cmp(v.version, current) <= 0 && (!since || cmp(v.version, since) > 0)).slice(0, 2);
}
