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

// The full texts are in THIRD-PARTY-NOTICES.txt in every package (written by
// tools/licensecheck when the packages are built).
const thirdParty = `YACM includes third-party software, each part under its own licence:

- the Go standard library and runtime (BSD-3-Clause)
- Wails and its modules, among them go-webview2, go-ole, godbus (MIT, BSD)
- golang.org/x/crypto, net, sys, text, image (BSD-3-Clause)
- github.com/pkg/sftp (BSD-2-Clause), go.bug.st/serial (BSD-3-Clause)
- github.com/goccy/go-yaml, github.com/google/uuid and other small modules
  (MIT, BSD, Apache-2.0)
- xterm.js and its addons (MIT)
- the fonts JetBrains Mono and Barlow Semi Condensed (SIL Open Font
  Licence 1.1)

The complete list with every licence text is THIRD-PARTY-NOTICES.txt, next
to YACM in the download (on macOS: in the disk image).`;

export function showLegal() {
  const texts = { notice, trademarks, licence, thirdParty };
  const pre = h('pre', { class: 'legal-text', text: notice });
  const pick = seg([{ value: 'notice', label: 'Notices and terms' }, { value: 'trademarks', label: 'Name and logo' }, { value: 'licence', label: 'GNU GPL version 3' }, { value: 'thirdParty', label: 'Third-party software' }], 'notice', v => { pre.textContent = texts[v]; pre.scrollTop = 0; });
  openDialog({ title: 'Licence and terms', body: h('div', {}, pick, pre), medium: true, buttons: [{ label: 'Close', default: true }] });
}
