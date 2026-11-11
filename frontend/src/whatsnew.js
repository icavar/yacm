// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// What's new, shown once after an update and from Settings > About, and the
// changes in the release notes (build/release-notes.mjs). Every version from
// 0.9.1 on has an entry. Newest first; short, user-facing lines, the user
// guide has the details:
//   { version: '1.1.0', items: [['Title', 'One or two sentences.'], …] },
export const WHATS_NEW = [
  {
    version: '0.9.3',
    items: [
      ['Microsoft Store', 'YACM for Windows comes as a Microsoft Store package too, which the Store keeps up to date. It uses the same data folder as the zip version, so sessions and the vault stay where they are.'],
      ['New project address', 'The project moved to github.com/yacm-app/yacm; the old address forwards there. Homebrew: brew install --cask yacm-app/tap/yacm.'],
    ],
  },
  {
    version: '0.9.2',
    items: [
      ['Linux packages', '.deb (Ubuntu 22.04+, Debian 12+) and .rpm (Fedora) add the signed package repository pkg.yacm.app, so apt upgrade and dnf upgrade bring new versions.'],
      ['Safer .yacm import', 'Agent forwarding, outbound proxies and log folders from someone else\'s file are left out, and the import report says so. Imported host key lines are checked like the trusted host keys file, and one unreadable line there no longer switches off the whole file.'],
      ['Session logs', 'Logs and output files keep UTF-8 text whole (Czech, Romanian, Cyrillic, CJK).'],
      ['Vault', 'A master password change that cannot be saved keeps the old password.'],
      ['File servers', 'Uploads are limited (16 GiB, at least 1 GiB of free space kept); stopping the SCP server also ends its open connections.'],
      ['NetBox', 'Only https (http just for this computer); the saved token goes only to the address it was saved for.'],
      ['Third-party licences', 'In every package and in Settings > About.'],
    ],
  },
  {
    version: '0.9.1',
    items: [
      ['File transfers', 'A transfer started from a terminal tab goes on when the device logs the terminal out, and shows its speed and the time left. An SCP transfer that Cisco IOS closes as it starts is tried again.'],
      ['SecureCRT import', 'Credential sets are found also when you import a folder inside Sessions.'],
      ['Terminal', 'Shift+Insert pastes once; Ctrl+Shift+Tab goes to the previous tab on Linux too.'],
      ['Session logs', 'The SNMP community in snmp-server host and a server key given without a type are masked.'],
      ['Vault backups', 'After a master password change the backups are encrypted with the new password, so the old one opens none of them.'],
      ['File servers', 'The TFTP server takes at most 32 transfers at a time.'],
      ['Releases', 'Every release has its build provenance attached as a file, for checking the packages offline.'],
    ],
  },
];

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
