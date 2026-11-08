// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Opt-in update check, off by default and never offered by itself: YACM is
// updated through the channel it came from (a store, a package manager or a
// new zip). With "daily", switched on in Settings > General, YACM asks GitHub
// once a day (per computer) whether a newer release is out and says so once
// per version.
import { api, state, runtime } from './api.js';
import { h, toast, errMsg } from './ui.js';

const DAY = 24 * 3600 * 1000;
const LSKEY = 'yacm.update';

function load() {
  try { return JSON.parse(localStorage.getItem(LSKEY) || '{}'); } catch { return {}; }
}
function store(v) {
  try { localStorage.setItem(LSKEY, JSON.stringify({ ...load(), ...v })); } catch { /* private mode */ }
}

// check asks GitHub. By hand (manual) it always reports the outcome.
/** @param {boolean} manual */
export async function check(manual) {
  let info;
  try {
    info = await api.CheckForUpdate();
  } catch (e) {
    if (manual) toast(`Could not check for updates: ${errMsg(e)}`, 'error', 8000);
    return null;
  }
  store({ last: Date.now() });
  if (!info.newer) {
    if (manual) toast(info.latest ? `YACM ${info.current} is the newest version.` : 'No releases found.');
    return info;
  }
  if (manual || load().seen !== info.latest) {
    store({ seen: info.latest });
    notify(info);
  }
  return info;
}

/** @param {{ latest: string, url: string, prerelease?: boolean }} info */
function notify(info) {
  const box = h('div', { class: 'toast info update-toast', role: 'status' },
    h('span', { text: `YACM ${info.latest}${info.prerelease ? ' (release candidate)' : ''} is out.` }),
    h('button', { type: 'button', class: 'btn small primary', text: 'Download', on: { click: () => { runtime.openURL(info.url); box.remove(); } } }),
    h('button', { type: 'button', class: 'btn small', text: 'Later', on: { click: () => box.remove() } }));
  document.getElementById('toasts')?.append(box);
}

// initUpdates runs once after start.
export function initUpdates() {
  const due = () => state.settings.updateCheck === 'daily' && Date.now() - (load().last || 0) > DAY;
  setTimeout(() => {
    if (due()) check(false);
    setInterval(() => { if (due()) check(false); }, 3600 * 1000);
  }, 20000);
}
