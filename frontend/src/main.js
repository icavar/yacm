// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

import './style.css';
import { api, on, state, reload, saveSettings, runtime } from './api.js';
import { initTree, render as renderTree } from './tree.js';
import { atStart } from './guide.js';
import { initUpdates } from './updates.js';
import { initTerms, openSession, openQuick, openMany, applySettings, restoreTabs } from './terms.js';
import { initPrompts } from './prompts.js';
import { initFiles, openFiles } from './files.js';
import { initServers } from './servers.js';
import { initActivity } from './activity.js';
import { editSession, editGroup } from './editor.js';
import { openSettings, lockScreen } from './settings.js';
import { importMenu, exportDialog } from './transfer.js';
import { openProfiles } from './profiles.js';
import { editMany } from './bulk.js';
import { editLibrary } from './snippets.js';
import { openSwitcher } from './switcher.js';
import { toast, errMsg, anyDialogOpen, shortcut, KEY, textFieldMenus } from './ui.js';
import { loadDataStatus, unavailableScreen, showReadOnlyBanner, watchDataStatus } from './datafolder.js';
import { initTeam, copyToMine, publishDialog, editLibrary as editTeamLibrary, stopEditing, openTeamSettings } from './teamui.js';

const SIDE_DEFAULT = 320;

function setSidebarWidth(w) {
  document.getElementById('app').style.setProperty('--side-w', `${w}px`);
}

// The session list can be made wider by dragging its right edge.
function initResizer() {
  const r = document.getElementById('resizer');
  const clamp = w => Math.round(Math.max(200, Math.min(w, Math.min(900, window.innerWidth - 410))));
  let width = clamp(state.settings.sidebarWidth || SIDE_DEFAULT);
  setSidebarWidth(width);
  const save = () => saveSettings({ sidebarWidth: width === SIDE_DEFAULT ? 0 : width }).catch(() => {});
  r.addEventListener('pointerdown', e => {
    e.preventDefault();
    r.setPointerCapture(e.pointerId);
    r.classList.add('dragging');
    document.body.classList.add('resizing');
    const startX = e.clientX;
    const startW = document.getElementById('sidebar').getBoundingClientRect().width;
    const move = ev => { width = clamp(startW + ev.clientX - startX); setSidebarWidth(width); };
    const up = () => {
      r.removeEventListener('pointermove', move);
      r.classList.remove('dragging');
      document.body.classList.remove('resizing');
      save();
    };
    r.addEventListener('pointermove', move);
    r.addEventListener('pointerup', up, { once: true });
    r.addEventListener('pointercancel', up, { once: true });
  });
  r.addEventListener('dblclick', () => { width = SIDE_DEFAULT; setSidebarWidth(width); save(); });
  r.addEventListener('keydown', e => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    width = clamp(width + (e.key === 'ArrowRight' ? 20 : -20));
    setSidebarWidth(width);
    save();
  });
  window.addEventListener('resize', () => { const w = clamp(width); if (w !== width) { width = w; setSidebarWidth(w); } });
}

function sessionsUnder(groupId) {
  const out = [];
  const walk = pid => (state.children.get(pid) || []).forEach(n => (n.isGroup ? walk(n.id) : out.push(n.id)));
  walk(groupId);
  return out;
}

async function refreshLockButton() {
  state.vault = await api.GetVaultStatus();
  const b = document.getElementById('btn-lock');
  // A padlock that shows the state of the vault: open = passwords usable,
  // closed or missing = in the warning colour.
  const { exists, unlocked } = state.vault;
  b.classList.toggle('locked', exists && !unlocked);
  b.classList.toggle('no-vault', !exists);
  const body = '<rect x="5" y="10.5" width="14" height="10" rx="2.2"/>';
  const shackle = exists && !unlocked ? '<path d="M8.5 10.5V7.5a3.5 3.5 0 0 1 7 0v3"/>' : '<path d="M8.5 10.5V7.5a3.5 3.5 0 0 1 6.8-1.2"/>';
  b.innerHTML = `<svg viewBox="0 0 24 24" aria-hidden="true">${body}${shackle}${exists ? '' : '<path d="M12 14.2v2.6"/>'}</svg>`;
  const tip = !exists ? 'Vault: none yet, click to set it up' : unlocked ? 'Vault: unlocked, click to lock it' : 'Vault: locked, click to unlock it';
  b.dataset.tip = tip;
  b.setAttribute('aria-label', !exists ? 'Set up vault' : unlocked ? 'Lock' : 'Unlock');
}

async function lockOrUnlock() {
  if (state.vault.exists && state.vault.unlocked) {
    await api.LockVault();
    await refreshLockButton();
  }
  await lockScreen();
  await refreshLockButton();
}

function setupAutoLock() {
  let last = Date.now();
  const bump = () => (last = Date.now());
  ['keydown', 'mousedown', 'wheel'].forEach(ev => document.addEventListener(ev, bump, true));
  setInterval(async () => {
    const mins = state.settings.autoLockMinutes || 0;
    if (!mins || !state.vault.unlocked) return;
    if (Date.now() - last > mins * 60000) {
      await api.LockVault();
      await refreshLockButton();
      toast('The vault was locked after inactivity.');
      await lockScreen({ idle: true });
      await refreshLockButton();
    }
  }, 15000);
}

// JavaScript errors go into yacm.log too, so they show up in diagnostics.
window.addEventListener('error', e => api.LogUIError(`${e.message} at ${e.filename || ''}:${e.lineno || 0}:${e.colno || 0}`).catch(() => {}));
window.addEventListener('unhandledrejection', e => {
  const r = e.reason;
  api.LogUIError(`unhandled: ${r && r.stack ? r.stack : String(r)}`).catch(() => {});
});

async function boot() {
  try {
    await reload();
  } catch (e) {
    document.body.textContent = `Could not start: ${errMsg(e)}`;
    return;
  }
  const data = await loadDataStatus().catch(() => ({}));
  if (data.unavailable) { await unavailableScreen(); return; } // resolved by restarting
  watchDataStatus();
  state.vault = await api.GetVaultStatus();
  // A read-only window cannot create a vault, only unlock an existing one.
  if (!state.vault.unlocked && !(data.readOnly && !state.vault.exists)) await lockScreen();
  await refreshLockButton();

  initPrompts();
  initTerms();
  initFiles();
  initServers();
  initTree({
    connect: id => openSession(id),
    quickConnect: spec => openQuick(spec),
    openFiles: id => openFiles(id),
    edit: n => (n.isGroup ? editGroup(n) : editSession(n)),
    newSession: pid => editSession(null, pid),
    newGroup: pid => editGroup(null, pid),
    connectGroup: id => openMany(sessionsUnder(id)),
    connectMany: async ids => openMany(await api.SessionsUnder(ids)),
    editMany: ids => editMany(ids),
    exportNode: id => exportDialog(id),
    exportMany: ids => exportDialog(ids),
    exportAll: () => exportDialog(null),
    importHere: () => importMenu(document.getElementById('btn-more'), afterImport, { withExport: true }),
    copyToMine: ids => copyToMine(ids),
    publishGroup: id => publishDialog(id),
    editLibrary: id => editTeamLibrary(id),
    stopEditing: id => stopEditing(id),
    teamSettings: () => openTeamSettings(),
    connectCluster: async name => {
      const ids = state.nodes.filter(n => n.session?.clusters?.some(c => c.toLowerCase() === name.toLowerCase())).map(n => n.id);
      await openMany(ids);
    },
  });
  initActivity();
  renderTree();
  showReadOnlyBanner();
  initResizer();
  window.addEventListener('yacm:tree', () => { renderTree(); applySettings(); });
  initTeam(() => { renderTree(); applySettings(); });
  // A followed team library file changed (a colleague published).
  on('team:changed', async () => { await reload(); renderTree(); });
  on('tree:changed', async () => { await reload(); renderTree(); });

  const parentForNew = () => {
    const sel = state.byId.get(state.selected);
    return sel ? (sel.isGroup ? sel.id : sel.parentId) : '';
  };
  document.getElementById('btn-new-session').title = `New session (${KEY.newSession})`;
  document.getElementById('btn-commands').dataset.tip = `Saved commands: the library all sessions use\n(to send one: Commands… in the status bar or ${KEY.palette})`;
  document.getElementById('btn-new-session').onclick = () => editSession(null, parentForNew());
  document.getElementById('btn-new-group').onclick = () => editGroup(null, parentForNew());
  document.getElementById('empty-new').onclick = () => editSession(null, parentForNew());
  const afterImport = async () => { await refreshLockButton(); renderTree(); applySettings(); };
  document.getElementById('btn-more').onclick = e => importMenu(e.currentTarget, afterImport, { withExport: true });
  document.getElementById('empty-import').onclick = e => importMenu(e.currentTarget, afterImport);
  document.getElementById('btn-profiles').onclick = () => openProfiles(() => { renderTree(); applySettings(); });
  document.getElementById('btn-commands').onclick = () => editLibrary();
  document.getElementById('btn-settings').onclick = () => openSettings(() => { renderTree(); applySettings(); });
  document.getElementById('btn-lock').onclick = lockOrUnlock;
  document.getElementById('btn-switcher').onclick = () => openSwitcher();
  document.getElementById('btn-switcher').title = `Go to session (${KEY.switcher})`;
  document.getElementById('empty-keys').textContent = KEY.switcher;


  document.addEventListener('keydown', e => {
    if (anyDialogOpen()) return;
    if (shortcut(e) === 'n') { e.preventDefault(); editSession(null, parentForNew()); }
    if (shortcut(e) === 'o') { e.preventDefault(); openSwitcher(); }
  });
  // No browser navigation or zoom surprises inside the desktop app.
  document.addEventListener('dragover', e => e.preventDefault());
  // Never let a dropped link or file navigate the window (also on the tree).
  document.addEventListener('drop', e => e.preventDefault());
  setupAutoLock();
  textFieldMenus(runtime);
  // One question at a time: first the sessions from last time, then news.
  restoreTabs().catch(() => {}).finally(() => atStart(afterImport));
  initUpdates();
}

boot();
