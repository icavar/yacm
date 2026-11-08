// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Quick switcher: find a session by part of its name, address, group or tag
// and connect, faster than the tree with thousands of sessions.
import { api, state, pathOf, isNewerType } from './api.js';
import { h, openDialog, portGlyph, KEY } from './ui.js';
import { openSession, openQuick, openBeside } from './terms.js';
import { reveal } from './tree.js';

const LIMIT = 60;

// An entry the user typed that looks like an address gets a quick connect.
/** @param {string} q */
export function looksLikeAddress(q) {
  if (/^(ssh|telnet|serial):\/\//i.test(q)) return true;
  if (/\s/.test(q)) return false;
  return /@/.test(q) || /^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(q) || /^[\w-]+(\.[\w-]+)+(:\d+)?$/.test(q) || /^\[?[0-9a-f:]+:[0-9a-f:]*\]?(:\d+)?$/i.test(q);
}

// index prepares lower-case search text once per opening.
function index() { return entriesFor(state.nodes.filter(n => !n.isGroup && n.session)); }

// entriesFor prepares search entries for some sessions (see search).
/** @param {any[]} nodes */
export function entriesFor(nodes) {
  return nodes.map(n => {
    const s = n.session;
    const path = pathOf(n.parentId).join(' / ') + (n.library ? ' (team library)' : '');
    return {
      n, path,
      name: n.name.toLowerCase(),
      host: (s.host || s.serial?.device || '').toLowerCase(),
      group: path.toLowerCase(),
      tags: (s.clusters || []).join(' ').toLowerCase(),
      desc: (n.description || '').toLowerCase(),
    };
  });
}

// subsequence reports whether all letters of q appear in t in order.
function subsequence(q, t) {
  let i = 0;
  for (let j = 0; j < t.length && i < q.length; j++) if (t[j] === q[i]) i++;
  return i === q.length;
}

// scoreWord rates how well one search word matches an entry (0 = not at all).
function scoreWord(w, e) {
  if (e.name === w) return 100;
  if (e.name.startsWith(w)) return 80;
  if (e.host.startsWith(w)) return 70;
  const at = e.name.indexOf(w);
  if (at >= 0) return /[\s._-]/.test(e.name[at - 1] || '') ? 65 : 55;
  if (e.host.includes(w)) return 50;
  if (e.tags.split(' ').includes(w)) return 45;
  if (e.group.includes(w)) return 35;
  if (e.tags.includes(w)) return 30;
  if (e.desc.includes(w)) return 20;
  if (w.length >= 2 && subsequence(w, e.name)) return 10;
  return 0;
}

export function search(entries, query) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return [];
  const out = [];
  for (const e of entries) {
    let total = 0;
    for (const w of words) {
      const s = scoreWord(w, e);
      if (!s) { total = 0; break; }
      total += s;
    }
    if (total) out.push({ e, total: total + (state.favorites?.has(e.n.id) ? 15 : 0) });
  }
  out.sort((a, b) => b.total - a.total || a.e.name.length - b.e.name.length || a.e.name.localeCompare(b.e.name));
  return out.slice(0, LIMIT).map(x => x.e);
}

// rowEl draws one result row: a saved session or a quick connect.
export function rowEl(r) {
  return r.kind === 'quick'
    ? h('li', { role: 'option' }, h('span', { class: 't' }, 'Quick connect to ', h('b', { text: r.spec })), h('span', { class: 'f', text: 'new connection' }))
    : h('li', { role: 'option', class: isNewerType(r.e.n) ? 'newer' : '' },
      h('span', { class: 't' }, portGlyph(r.e.n.session.protocol), r.e.n.name),
      h('span', { class: 'f', text: r.e.n.session.host || r.e.n.session.serial?.device || '' }),
      r.e.path || r.e.n.session.clusters?.length ? h('code', { text: [r.e.path, ...(r.e.n.session.clusters || []).map(c => `#${c}`)].filter(Boolean).join('  ') }) : null);
}

export async function openSwitcher() {
  const entries = index();
  const byId = new Map(entries.map(e => [e.n.id, e]));
  const favs = [...(state.favorites || [])].map(id => byId.get(id)).filter(Boolean);
  const recent = ((await api.RecentSessions().catch(() => [])) || []).map(id => byId.get(id)).filter(e => e && !state.favorites?.has(e.n.id));
  const input = h('input', { class: 'search', type: 'text', spellcheck: false, placeholder: 'Name, address, group or tag', 'aria-label': 'Find a session' });
  const list = h('ul', { role: 'listbox' });
  const note = h('span', { class: 'target' });
  /** @type {{ kind: string, e?: any, spec?: string }[]} */
  let rows = [];
  let cur = 0;
  let dlg = null;

  const render = () => {
    const q = input.value.trim();
    const found = q ? search(entries, q) : [...favs, ...recent];
    rows = found.map(e => ({ kind: 'session', e }));
    if (q && looksLikeAddress(q)) rows.push({ kind: 'quick', spec: q });
    cur = Math.min(cur, Math.max(0, rows.length - 1));
    list.replaceChildren();
    if (!q && !favs.length) list.append(h('li', { class: 'heading', text: recent.length ? 'Recent' : 'Start typing to find a session' }));
    if (!q && favs.length) list.append(h('li', { class: 'heading', text: 'Favourites' }));
    if (q && !rows.length) list.append(h('li', { class: 'empty-note', text: 'No session matches.' }));
    rows.forEach((r, i) => {
      const li = rowEl(r);
      li.addEventListener('click', ev => { cur = i; go(ev); });
      if (!q && favs.length && recent.length && i === favs.length) list.append(h('li', { class: 'heading', text: 'Recent' }));
      li.addEventListener('mousemove', () => { if (cur !== i) { cur = i; paint(); } });
      list.append(li);
    });
    paint();
    const more = q && rows.filter(r => r.kind === 'session').length >= LIMIT;
    note.textContent = `Enter connects · Shift+Enter opens it next to the current tab · Ctrl+Enter shows it in the list${more ? ` · first ${LIMIT} matches` : ''}`;
  };
  const paint = () => {
    const opts = [...list.querySelectorAll('li[role=option]')];
    opts.forEach((li, i) => { li.classList.toggle('on', i === cur); li.setAttribute('aria-selected', String(i === cur)); });
    opts[cur]?.scrollIntoView({ block: 'nearest' });
  };
  const go = ev => {
    const r = rows[cur];
    if (!r) return;
    dlg.close();
    if (r.kind === 'quick') { openQuick(r.spec); return; }
    const id = r.e.n.id;
    if (ev?.ctrlKey || ev?.metaKey) { reveal(id); return; }
    if (ev?.shiftKey) { openBeside(id); return; }
    openSession(id);
  };
  input.addEventListener('input', () => { cur = 0; render(); });
  input.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown') { e.preventDefault(); cur = Math.min(rows.length - 1, cur + 1); paint(); }
    if (e.key === 'ArrowUp') { e.preventDefault(); cur = Math.max(0, cur - 1); paint(); }
    if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); go(e); }
  });
  render();
  dlg = openDialog({
    title: 'Go to session',
    sub: `${KEY.switcher} anywhere`,
    medium: true,
    body: h('div', { class: 'palette switcher' }, input, list, note),
    buttons: [{ label: 'Close' }],
    initialFocus: input,
  });
}
