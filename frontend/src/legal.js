// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// The licence and the notices, bundled so About shows them without a
// network (the GPL's "Appropriate Legal Notices").
import licence from '../../LICENSE?raw';
import notice from '../../NOTICE.md?raw';
import trademarks from '../../TRADEMARKS.md?raw';
import { h, openDialog, seg } from './ui.js';

export const PROJECT_URL = 'https://github.com/icavar/yacm';
// SUPPORT_URL is the README section with every way to support YACM, so the
// app needs no change when one is added or removed.
export const SUPPORT_URL = 'https://github.com/icavar/yacm#support-yacm';

export function showLegal() {
  const texts = { notice, trademarks, licence };
  const pre = h('pre', { class: 'legal-text', text: notice });
  const pick = seg([{ value: 'notice', label: 'Notices and terms' }, { value: 'trademarks', label: 'Name and logo' }, { value: 'licence', label: 'GNU GPL version 3' }], 'notice', v => { pre.textContent = texts[v]; pre.scrollTop = 0; });
  openDialog({ title: 'Licence and terms', body: h('div', {}, pick, pre), medium: true, buttons: [{ label: 'Close', default: true }] });
}
