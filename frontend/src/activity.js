// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// The bar on the left edge of the window: the session list, the command and
// credential libraries and the tools at the top, settings and the vault at
// the bottom. Each entry is an icon whose label shows beside it on hover.
// Other parts of YACM add their own entries with addActivity.
import { state, saveSettings } from './api.js';
import { h } from './ui.js';

// initActivity wires the Sessions icon, which folds the session list away
// and back (the list remembers it).
export function initActivity() {
  const btn = document.getElementById('btn-sessions');
  btn?.addEventListener('click', () => setSidebar(document.getElementById('app').classList.contains('no-side')));
  setSidebar(!state.settings.sidebarHidden, false);
}

/** @param {boolean} visible @param {boolean} [save] */
export function setSidebar(visible, save = true) {
  document.getElementById('app').classList.toggle('no-side', !visible);
  const btn = document.getElementById('btn-sessions');
  btn?.classList.toggle('on', visible);
  btn?.setAttribute('aria-pressed', String(visible));
  if (btn) btn.dataset.tip = visible ? 'Sessions (click to fold the list away)' : 'Sessions (click to show the list)';
  if (save && !!state.settings.sidebarHidden === visible) saveSettings({ sidebarHidden: !visible }).catch(() => {});
}

// showSidebar brings the session list back (to show a session in it).
export function showSidebar() { if (document.getElementById('app').classList.contains('no-side')) setSidebar(true); }

/**
 * addActivity puts an entry into the bar, after the built-in tools unless
 * order says otherwise (built-in: 10 sessions, 20 commands, 30 profiles,
 * 40 file servers). icon is the inner markup of a 24×24 SVG drawn with
 * strokes. Returns the button, so its owner can mark it (classList) later.
 * @param {{ id: string, label: string, icon: string, action: () => any, order?: number }} e
 */
export function addActivity({ id, label, icon, action, order = 100 }) {
  const top = document.getElementById('act-top');
  document.getElementById(id)?.remove();
  const b = h('button', { id, class: 'act-btn', 'aria-label': label, dataset: { tip: label, order: String(order) }, on: { click: action } });
  b.innerHTML = `<svg viewBox="0 0 24 24" aria-hidden="true">${icon}</svg>`;
  const after = [...top.children].find(el => Number(/** @type {HTMLElement} */ (el).dataset.order || 0) > order);
  top.insertBefore(b, after || null);
  return b;
}
