// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Release notes from the What's new entry of a version (frontend/src/whatsnew.js):
//   node build/release-notes.mjs 1.1.0-rc.1 > notes.md
// Every version from 0.9.1 on has an entry; 0.9.x releases also say they
// are the public beta.
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
// Direct links to the release files: GitHub lists them in one flat list.
const repo = process.env.GITHUB_REPOSITORY || 'yacm-app/yacm';
const link = f => `[${f}](https://github.com/${repo}/releases/download/v${version}/${f})`;
console.log([
  rc ? `Release candidate of YACM ${num}, for testing.` : `YACM ${num}.`,
  '',
  ...(num.startsWith('0.9.') ? ['The public beta of YACM: until 1.0 the releases bring fixes and packaging only. The [README](https://github.com/yacm-app/yacm#readme) lists what YACM does.', ''] : []),
  ...(lines.length ? [num.startsWith('0.') ? '## Changes' : '## What is new', '', ...lines, ''] : []),
  '## Downloads',
  '',
  '| YACM | |',
  '|---|---|',
  `| **Windows** 10 and 11, x64 (most PCs) | ${link(`yacm-${version}-windows-x64.zip`)} |`,
  `| **Windows** 10 and 11 on ARM | ${link(`yacm-${version}-windows-arm64.zip`)} |`,
  `| **macOS** 13 or newer, Apple silicon and Intel (signed and notarised) | ${link(`YACM-${version}-macos.dmg`)} |`,
  `| **Linux** x64, Ubuntu 22.04+ and Debian 12+ (updates with apt from pkg.yacm.app) | ${link(`yacm-${version}-linux-amd64.deb`)} |`,
  `| **Linux** x64, Fedora and other RPM systems with WebKitGTK 4.1 (updates with dnf from pkg.yacm.app) | ${link(`yacm-${version}-linux-amd64.rpm`)} |`,
  `| **Linux** x64, any distribution with WebKitGTK 4.1 | ${link(`yacm-${version}-linux-amd64.tar.gz`)} |`,
  '',
  '| Demo device | |',
  '|---|---|',
  `| Windows | ${link(`yacm-demo-device-${version}-windows.zip`)} |`,
  `| macOS | ${link(`yacm-demo-device-${version}-macos.zip`)} |`,
  `| Linux x64 | ${link(`yacm-demo-device-${version}-linux-amd64.tar.gz`)} |`,
  '',
  'The demo device is a pretend router that runs on your computer, with example sessions for it, to try YACM without network devices. YACM itself does not need it.',
  '',
  '| Verification | |',
  '|---|---|',
  `| Checksums | ${link('SHA256SUMS.txt')} |`,
  `| Software bill of materials (SPDX) | ${link(`yacm-${version}-sbom.spdx.json`)} |`,
  `| Build provenance | ${link(`yacm-${version}-provenance.intoto.jsonl`)} |`,
  '',
  `Check a file with \`gh attestation verify <file> --repo ${repo}\`. [Code signing policy](https://github.com/${repo}/blob/main/docs/code-signing-policy.md).`,
].join('\n'));
