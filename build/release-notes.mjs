// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Release notes from the What's new entry of a version (frontend/src/whatsnew.js):
//   node build/release-notes.mjs 1.1.0-rc.1 > notes.md
// What's new starts with 1.1; 0.9.x and 1.0.x releases say what they are.
import { readFileSync } from 'node:fs';

const version = (process.argv[2] || '').replace(/^v/, '');
const num = version.split('-')[0];
const src = readFileSync(new URL('../frontend/src/whatsnew.js', import.meta.url), 'utf8');
const at = src.indexOf(`version: '${num}'`);
const lines = [];
if (at >= 0) {
  const end = src.indexOf('\n  },', at);
  const block = src.slice(at, end);
  const unq = s => s.replace(/\\(.)/g, '$1');
  for (const m of block.matchAll(/\['((?:[^'\\]|\\.)*)',\s*'((?:[^'\\]|\\.)*)'\]/g)) lines.push(`- **${unq(m[1])}**: ${unq(m[2])}`);
}
const rc = version.includes('-rc.');
console.log([
  rc ? `Release candidate of YACM ${num}, for testing.` : `YACM ${num}.`,
  '',
  ...(num.startsWith('0.9.') ? ['The public beta of YACM. Until 1.0 the releases bring fixes only; the [README](https://github.com/icavar/yacm#readme) lists what YACM does.', ''] : []),
  ...(lines.length ? ['## What is new', '', ...lines, ''] : []),
  '## Downloads',
  '',
  '- **Windows** 10 and 11: `yacm-*-windows-x64.zip` for most PCs, `yacm-*-windows-arm64.zip` for Windows on ARM',
  '- **macOS** 13 or newer: `YACM-*-macos.dmg` (Apple silicon and Intel, signed and notarised)',
  '- **Linux** x64: `yacm-*-linux-amd64.tar.gz` (needs WebKitGTK 4.1, as in Ubuntu 22.04 and newer)',
  '',
  'To try YACM without network devices: `yacm-*-demo-windows.zip`, `yacm-*-demo-macos.zip` or `yacm-*-demo-linux-amd64.tar.gz` hold a pretend router that runs on your computer and example sessions for it. YACM itself does not need them.',
  '',
  '`SHA256SUMS.txt` has the checksums, `*.spdx.json` the software bill of materials. In the public repository each file also has a build provenance attestation: `gh attestation verify <file> --repo icavar/yacm`.',
].join('\n'));
