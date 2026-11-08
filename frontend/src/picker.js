// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Session picker: a search field instead of a long <select> for choosing a
// saved session (the jump host of a session, a group or a bulk edit). With
// thousands of sessions a select is a scroll marathon; here typing part of
// the name, address, group or tag narrows the list.
import { state } from './api.js';
import { h } from './ui.js';
import { entriesFor, search, rowEl } from './switcher.js';

const SHOW = 40;

/**
 * sessionPicker returns an element with a value property (the chosen
 * session id or a special value) that fires "change" when it changes.
 * @param {{ value?: string, nodes: () => any[], specials?: [string, string][], placeholder?: string, label?: string }} o
 */
export function sessionPicker({ value = '', nodes, specials = [], placeholder = 'Type part of a name, address, group or tag', label = 'Session' }) {
  let current = value;
  const input = h('input', { type: 'text', class: 'picker-input', spellcheck: false, autocomplete: 'off', placeholder, role: 'combobox', 'aria-expanded': 'false', 'aria-label': label });
  const list = h('ul', { role: 'listbox', class: 'picker-list', hidden: true });
  const wrap = h('div', { class: 'picker palette switcher' }, input, list);
  /** @type {{ kind: string, value: string, label?: string, e?: any }[]} */
  let rows = [];
  let cur = 0;
  let entries = null;

  const nameOf = v => {
    const sp = specials.find(([k]) => k === v);
    if (sp) return sp[1];
    const n = state.byId.get(v);
    if (!n) return v ? 'missing session' : '';
    const s = n.session || {};
    return `${n.name} (${s.host || ''}${s.port && s.port !== 22 ? ':' + s.port : ''})`;
  };
  const showValue = () => { input.value = nameOf(current); input.title = input.value; };
  // Before typing: favourites and the sessions already used as jump hosts.
  const suggested = () => {
    const used = new Map();
    for (const n of state.nodes) {
      for (const v of [n.session?.jumpVia, n.defaults?.jumpVia]) if (v && v !== 'inherit') used.set(v, (used.get(v) || 0) + 1);
    }
    const favs = entries.filter(e => state.favorites?.has(e.n.id));
    const jumps = entries.filter(e => used.has(e.n.id) && !state.favorites?.has(e.n.id)).sort((a, b) => used.get(b.n.id) - used.get(a.n.id));
    const rest = entries.filter(e => !used.has(e.n.id) && !state.favorites?.has(e.n.id)).sort((a, b) => a.n.name.localeCompare(b.n.name));
    return [...favs, ...jumps, ...rest].slice(0, SHOW);
  };
  const paint = () => {
    const opts = [...list.querySelectorAll('li[role=option]')];
    opts.forEach((li, i) => { li.classList.toggle('on', i === cur); li.setAttribute('aria-selected', String(i === cur)); });
    opts[cur]?.scrollIntoView({ block: 'nearest' });
  };
  const render = q => {
    entries ||= entriesFor(nodes());
    const found = q ? search(entries, q).slice(0, SHOW) : suggested();
    rows = [
      ...(q ? [] : specials.map(([v, t]) => ({ kind: 'special', value: v, label: t }))),
      ...found.map(e => ({ kind: 'session', value: e.n.id, e })),
    ];
    const total = q ? search(entries, q).length : entries.length;
    list.replaceChildren(...rows.map((r, i) => {
      const li = r.kind === 'special' ? h('li', { role: 'option', class: 'special' }, h('span', { class: 't', text: r.label })) : rowEl(r);
      li.addEventListener('mousedown', ev => { ev.preventDefault(); cur = i; choose(); });
      li.addEventListener('mousemove', () => { if (cur !== i) { cur = i; paint(); } });
      return li;
    }));
    if (!rows.length) list.append(h('li', { class: 'empty-note', text: 'No SSH session matches.' }));
    if (total > found.length) list.append(h('li', { class: 'heading', text: q ? `First ${found.length} of ${total}: type more to narrow` : `${entries.length} sessions: type to find one` }));
    cur = Math.max(0, rows.findIndex(r => r.value === current && !q));
    list.hidden = false;
    input.setAttribute('aria-expanded', 'true');
    paint();
  };
  const close = () => { list.hidden = true; input.setAttribute('aria-expanded', 'false'); showValue(); };
  const choose = () => {
    const r = rows[cur];
    if (!r) return;
    const changed = r.value !== current;
    current = r.value;
    close();
    if (changed) wrap.dispatchEvent(new Event('change', { bubbles: true }));
  };
  input.addEventListener('focus', () => { entries = null; input.select(); render(''); });
  input.addEventListener('input', () => render(input.value.trim()));
  input.addEventListener('click', () => { if (list.hidden) { input.select(); render(''); } });
  input.addEventListener('blur', close);
  input.addEventListener('keydown', e => {
    if (list.hidden && (e.key === 'ArrowDown' || e.key === 'Enter')) { e.preventDefault(); render(''); return; }
    if (e.key === 'ArrowDown') { e.preventDefault(); cur = Math.min(rows.length - 1, cur + 1); paint(); }
    if (e.key === 'ArrowUp') { e.preventDefault(); cur = Math.max(0, cur - 1); paint(); }
    if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); choose(); }
    if (e.key === 'Escape' && !list.hidden) { e.preventDefault(); e.stopPropagation(); close(); }
  });
  Object.defineProperty(wrap, 'value', {
    get: () => current,
    set: v => { current = v || ''; showValue(); },
  });
  showValue();
  return /** @type {HTMLElement & { value: string }} */ (wrap);
}
