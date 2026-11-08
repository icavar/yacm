// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// The data folder: where sessions, the vault and trusted host keys live.
// Settings > Data and backups changes it; a start-up screen handles a folder
// that cannot be reached; a banner explains a read-only window.
import { api, on } from './api.js';
import { h, openDialog, toast, errMsg, confirmDialog, plural } from './ui.js';
import { addSettingsSection } from './settings.js';

let status = null;

export async function loadDataStatus() {
  status = await api.DataFolderStatus();
  return status;
}

const when = t => (t ? new Date(t).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' }) : '');
const who = l => `${l.host}${l.user ? ` (${l.user})` : ''}`;

function placeNote(info) {
  if (info.cloud) return `This folder is synced by ${info.cloud}. That works, but open YACM on one computer at a time: a second computer opens it read-only, and a sync conflict could otherwise lose changes.`;
  if (info.network) return 'This is a network folder. YACM opens it read-only on a second computer while it is open here, and asks what to do when the network is not reachable at start.';
  return '';
}

// ---- Settings > Data and backups ----

function dataSection(ctx) {
  const s = status || {};
  const out = [h('h3', { text: 'Data folder' }),
    ctx.hint('Sessions, groups, credential profiles, commands, settings, the password vault and trusted host keys are kept here. Copy this folder to back everything up.')];
  const pills = [];
  if (s.readOnly) pills.push(h('span', { class: 'pill warn', text: 'read-only' }));
  if (s.cloud) pills.push(h('span', { class: 'pill', text: s.cloud }));
  if (s.network) pills.push(h('span', { class: 'pill', text: 'network folder' }));
  out.push(h('div', { class: 'data-path' }, h('code', { text: s.path || s.wanted || '' }), ...pills));
  if (s.source === 'env' || s.source === 'portable') {
    out.push(h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: s.source === 'env'
      ? 'Set by the YACM_DATA_DIR environment variable. Remove the variable to choose the folder here.'
      : 'Portable mode: yacm.portable next to the program keeps the data in yacm-data beside it.' }));
    out.push(h('div', { class: 'data-actions' }, h('button', { type: 'button', class: 'btn small', text: 'Open folder', on: { click: () => api.OpenDataFolder().catch(e => toast(errMsg(e), 'error')) } })));
    return out;
  }
  out.push(h('div', { class: 'data-actions' },
    h('button', { type: 'button', class: 'btn small', text: 'Change…', on: { click: chooseFolder } }),
    h('button', { type: 'button', class: 'btn small', text: 'Open folder', on: { click: () => api.OpenDataFolder().catch(e => toast(errMsg(e), 'error')) } }),
    s.source === 'chosen' ? h('button', { type: 'button', class: 'btn small', text: 'Back to the default folder', title: s.default, on: { click: backToDefault } }) : null));
  if (s.source === 'chosen') out.push(h('p', { class: 'hint', style: { color: 'var(--faint)', marginTop: '6px' }, text: `Default folder: ${s.default}` }));
  const note = placeNote(s);
  if (note) out.push(h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: note }));
  return out;
}

async function chooseFolder() {
  let path;
  try { path = await api.ChooseDirectory('Choose the folder for YACM data'); } catch (e) { toast(errMsg(e), 'error'); return; }
  if (!path) return;
  let info;
  try { info = await api.InspectDataFolder(path); } catch (e) { toast(errMsg(e), 'error'); return; }
  if (info.current) { toast('YACM already uses this folder.'); return; }
  if (!info.writable) { toast(info.problem || 'YACM cannot write to this folder.', 'error', 8000); return; }
  moveDialog(info);
}

function moveDialog(info, { toDefault = false } = {}) {
  const has = info.hasData;
  const choices = has
    ? [['use', `Use the data already there (${plural(info.sessions, 'session', 'sessions')}${info.hasVault ? ', with its own vault and master password' : ''})`, 'Your current data stays where it is.'],
      ['replace', 'Replace it with my data', 'What is in the folder now is kept in a backup folder inside it.']]
    : [['move', 'Move my data there', 'Copied and checked first; only then removed from the current folder.'],
      ['use', 'Start with an empty folder', 'Your current data stays where it is; switch back to it any time.']];
  let mode = choices[0][0];
  const radios = h('div', { class: 'choice-list' }, ...choices.map(([v, label, sub], i) => {
    const r = h('input', { type: 'radio', name: 'data-mode', value: v, checked: i === 0, on: { change: () => (mode = v) } });
    return h('label', { class: 'check' }, r, h('span', {}, label, h('small', { text: sub })));
  }));
  const note = placeNote(info);
  openDialog({
    title: toDefault ? 'Back to the default folder' : 'Move the data folder',
    sub: info.path,
    medium: true,
    body: h('div', {},
      info.problem && info.writable ? h('p', { class: 'hint', text: info.problem }) : null,
      radios,
      note ? h('p', { class: 'warning', text: note }) : null,
      h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'YACM restarts to use the folder. Close open sessions first. Session logs go along when they use the default logs folder.' })),
    buttons: [
      { label: 'Cancel' },
      { label: 'Continue and restart', kind: 'primary', default: true, onClick: async () => {
        if (toDefault) await api.ResetDataFolder(mode); else await api.ChangeDataFolder(info.path, mode);
        toast('Restarting YACM…');
      } },
    ],
  });
}

async function backToDefault() {
  let info;
  try { info = await api.InspectDataFolder(status.default); } catch (e) { toast(errMsg(e), 'error'); return; }
  moveDialog(info, { toDefault: true });
}

addSettingsSection({ id: 'data', label: 'Data and backups', render: dataSection });

// ---- start-up: folder not available ----

export function unavailableScreen() {
  return new Promise(() => {
    const err = h('div', { class: 'error-line', role: 'alert' });
    const busy = fn => async () => { err.textContent = ''; try { await fn(); } catch (e) { err.textContent = errMsg(e); } };
    const el = h('div', { class: 'lock' },
      h('h1', { text: 'Data folder not available' }),
      h('p', {}, 'YACM keeps its sessions and passwords in ', h('code', { text: status.wanted }), ', but that folder cannot be reached right now: a disconnected disk, no network or VPN, or the folder was renamed. Nothing has been changed.'),
      h('div', { class: 'actions', style: { flexWrap: 'wrap' } },
        h('button', { class: 'btn primary', text: 'Try again', on: { click: busy(() => api.RetryDataFolder()) } }),
        h('button', { class: 'btn', text: 'Choose another folder…', on: { click: busy(async () => {
          const p = await api.ChooseDirectory('Choose the folder with YACM data');
          if (!p) return;
          const info = await api.InspectDataFolder(p);
          if (!info.writable) throw new Error(info.problem || 'YACM cannot write to this folder.');
          const ok = await confirmDialog({ title: 'Use this folder?', message: info.hasData ? `${p} has YACM data with ${plural(info.sessions, 'session', 'sessions')}.` : `${p} has no YACM data yet; YACM starts empty there.`, ok: 'Use and restart' });
          if (ok) await api.ChangeDataFolder(p, 'use');
        }) } }),
        h('button', { class: 'btn', text: 'Use the default folder', title: status.default, on: { click: busy(async () => {
          const ok = await confirmDialog({ title: 'Use the default folder?', message: `YACM will use ${status.default} again. It has the data from before the folder was changed, if any; the other folder is not touched.`, ok: 'Use and restart' });
          if (ok) await api.ResetDataFolder('use');
        }) } })),
      err);
    document.body.append(el);
  });
}

// ---- read-only banner ----

export function showReadOnlyBanner() {
  document.getElementById('ro-banner')?.remove();
  if (!status?.readOnly || status.unavailable) return;
  const l = status.lockedBy || {};
  const text = status.lockLost
    ? `YACM on ${who(l)} took over the data folder. This window is now read-only; restart it to see their changes.`
    : `Read-only: YACM on ${who(l)} has these sessions open since ${when(l.started)}. You can connect, but changes are not saved. Close YACM there, then press Try again.`;
  const b = h('div', { id: 'ro-banner', class: 'ro-banner', role: 'status' },
    h('span', { text }),
    h('div', { class: 'row-btns' },
      h('button', { class: 'btn small', text: status.lockLost ? 'Restart' : 'Try again', on: { click: () => api.RetryDataFolder().catch(e => toast(errMsg(e), 'error')) } }),
      status.lockLost ? null : h('button', { class: 'btn small', text: 'Open anyway…', on: { click: async () => {
        const ok = await confirmDialog({ title: 'Open the data folder anyway?', danger: true, ok: 'Open anyway',
          message: `Use this only if YACM on ${who(l)} is really closed (for example the computer crashed). If it is still open, the changes made there and here overwrite each other.` });
        if (ok) api.TakeOverDataFolder().catch(e => toast(errMsg(e), 'error'));
      } } })));
  document.getElementById('sidebar').prepend(b);
}

export function watchDataStatus() {
  on('data:status', s => { status = s; showReadOnlyBanner(); });
}
