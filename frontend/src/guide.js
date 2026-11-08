// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// First-run guide and "What's new" after an update.
import { api, state, runtime } from './api.js';
import { h, openDialog, toast, errMsg } from './ui.js';
import { importMenu, importSource, importFlow } from './transfer.js';
import { PROJECT_URL } from './legal.js';
import { openTeamSettings } from './teamui.js';
import { whatsNewFor } from './whatsnew.js';

// showWhatsNew lists what is new since `since` (or just the current
// version), at most two versions back.
export function showWhatsNew(current, since = '') {
  const list = whatsNewFor(current, since);
  if (!list.length) return null;
  const body = h('div', { class: 'whatsnew' }, ...list.map(v => h('div', {},
    list.length > 1 ? h('h3', { text: `YACM ${v.version}` }) : null,
    h('ul', {}, ...v.items.map(([t, d]) => h('li', {}, h('strong', { text: t }), h('span', { text: ` ${d}` })))))));
  return openDialog({
    title: `What's new in YACM ${current}`,
    body,
    medium: true,
    buttons: [{ label: 'Got it', kind: 'primary', default: true }],
  });
}

// startGuide helps a new user get going: import, team library or demo.
export function startGuide(info, after) {
  let dlg;
  const card = (title, text, ...buttons) => h('div', { class: 'guide-card' },
    h('div', { class: 'title', text: title }), h('p', { text }), h('div', { class: 'row' }, ...buttons));
  const btn = (label, fn, primary) => h('button', { type: 'button', class: `btn small${primary ? ' primary' : ''}`, text: label, on: { click: fn } });
  const found = h('p', { class: 'hint', style: { margin: '-4px 0 8px' } });
  api.ImportSources().then(src => {
    const names = [...new Set((src || []).map(s => s.label))];
    if (names.length) found.textContent = `Found on this computer: ${names.join(', ')}.`;
  }).catch(() => {});
  const vaultNote = state.vault.exists ? null
    : h('p', { class: 'warning', text: 'You have not set up the vault yet, so passwords cannot be saved. Click the padlock at the bottom of the bar on the left when you are ready.' });
  const body = h('div', { class: 'guide' },
    h('p', { class: 'lead', text: 'YACM keeps your SSH, telnet and serial sessions, logs you in with credential profiles and works through jump hosts. Pick how you want to start; everything here can also be done later from the buttons above the session list.' }),
    vaultNote,
    card('Bring your sessions over', 'Import from SecureCRT, Xshell, MobaXterm, mRemoteNG, PuTTY, Ásbrú, ~/.ssh/config, a CSV or Excel list, or NetBox. Saved passwords go into the vault.',
      btn('Import sessions…', e => importMenu(e.currentTarget, async () => { dlg?.close(); await after(); }), true)),
    found,
    card('Use your team’s library', 'If a colleague published the team’s devices as a library file (on a share or in git), add it and link its logins to your own profiles.',
      btn('Add a team library…', () => { dlg?.close(); openTeamSettings(); })),
    info.demoFile
      ? card('Try it with the demo device', 'The demo download next to YACM has a pretend router that runs on this computer (start yacm-demo-device) and example sessions with profiles, a jump host and login automation. Export password: demo-export-pw',
        btn('Import the demo sessions', () => { dlg?.close(); importSource(info.demoFile, null, after).catch(e => toast(errMsg(e), 'error')); }))
      : card('Try it with the demo device', 'No network devices at hand? The separate demo download from the release page has a pretend router that runs on this computer and example sessions for it. Unpack it, start yacm-demo-device and import demo.yacm (export password: demo-export-pw).',
        btn('Get the demo', () => runtime.openURL(`${PROJECT_URL}/releases`)),
        btn('Import demo.yacm…', () => { dlg?.close(); importFlow(after).catch(e => toast(errMsg(e), 'error')); })),
  );
  dlg = openDialog({
    title: 'Welcome to YACM',
    body,
    medium: true,
    buttons: [{ label: 'Start with an empty list', default: true }],
  });
  return dlg;
}

// atStart shows the guide on a fresh install, or what is new after an update.
export async function atStart(after) {
  const info = await api.GetStartInfo().catch(() => null);
  if (!info) return;
  if (info.lastSeen === info.version) return;
  if (info.fresh) startGuide(info, after);
  else showWhatsNew(info.version, info.lastSeen);
  state.settings.lastSeenVersion = info.version;
  api.MarkVersionSeen().catch(() => {});
}
