// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

import { api, state, reload, colorOf, effectiveLogin, effectiveJump, teamLibOf, isNewerType, runtime } from './api.js';
import { h, contextMenu, confirmDialog, toast, errMsg, portGlyph, plural } from './ui.js';
import { looksLikeAddress } from './switcher.js';
import { showSidebar } from './activity.js';

// hooks: connect(id), connectMany(ids), edit(node), editMany(ids), newSession(pid), newGroup(pid),
// connectGroup(id), connectCluster(name), exportNode(id), exportMany(ids), exportAll(), importHere()
let hooks = {};
let filter = '';
let showAll = false; // show every match even in a very large result
const MAX_ROWS = 1000; // rows drawn at once when filtering; the rest on request
const haystacks = new WeakMap(); // node -> text the filter searches
let dragIds = null;
let anchorId = null; // start of a Shift+click range

// Favourites: personal, kept per computer (api.Favorites); shown in their
// own section at the top of the tree.
const FAV_KEY = 'yacm.favoritesClosed';
function favClosed() { try { return localStorage.getItem(FAV_KEY) === '1'; } catch { return false; } }
function setFavClosed(v) { try { localStorage.setItem(FAV_KEY, v ? '1' : '0'); } catch { /* storage off */ } }
export const isFavorite = id => state.favorites?.has(id);
export async function toggleFavorite(id) {
  try {
    const ids = await api.SetFavorite(id, !isFavorite(id));
    state.favorites = new Set(ids || []);
    render();
  } catch (e) { toast(errMsg(e), 'error'); }
}

export function initTree(h_) {
  hooks = h_;
  const tree = document.getElementById('tree');
  // Typing a word renders once, not once per letter (large trees).
  const search = /** @type {HTMLInputElement} */ (document.getElementById('search'));
  search.addEventListener('input', () => {
    clearTimeout(filterTimer);
    paintSearchHint();
    filterTimer = setTimeout(applyFilter, state.nodes.length > 1000 ? 150 : 0);
  });
  // One field finds and connects: Enter does what the hint under it says.
  search.addEventListener('keydown', e => {
    if (e.key === 'Escape') { e.preventDefault(); clearSearch(); search.blur(); return; }
    if (e.key === 'ArrowDown') { e.preventDefault(); chooseInTree(); return; }
    if (e.key !== 'Enter') return;
    e.preventDefault();
    const act = enterAction(search.value);
    if (!act) return;
    if (act.kind === 'choose') { chooseInTree(); return; }
    clearSearch();
    search.blur();
    if (act.kind === 'session') hooks.connect(act.node.id);
    else hooks.quickConnect(act.spec);
  });
  tree.addEventListener('contextmenu', e => {
    if (/** @type {Element} */ (e.target).closest('.row')) return;
    e.preventDefault();
    contextMenu(e.clientX, e.clientY, [
      { label: 'New session', action: () => hooks.newSession('') },
      { label: 'New group', action: () => hooks.newGroup('') },
      '-',
      { label: 'Import sessions…', action: () => hooks.importHere() },
      { label: 'Export all sessions…', action: () => hooks.exportAll(), disabled: !state.nodes.length },
    ]);
  });
  tree.addEventListener('keydown', onKey);
  // Dropping on empty space moves to root.
  tree.addEventListener('dragover', e => { if (dragIds && !/** @type {Element} */ (e.target).closest('.row')) e.preventDefault(); });
  tree.addEventListener('drop', async e => {
    if (!dragIds || /** @type {Element} */ (e.target).closest('.row')) return;
    e.preventDefault();
    await moveMany(dragIds, '', -1);
  });
}

// enterAction tells what Enter in the search field does with text q: a
// session whose name or address is exactly q, or the only session that
// matches, connects; an address nothing saved matches gets a quick connect;
// several matches move the choice into the tree.
/** @param {string} text @returns {{ kind: 'session', node: any } | { kind: 'quick', spec: string } | { kind: 'choose', count: number } | null} */
export function enterAction(text) {
  const q = text.trim();
  if (!q) return null;
  const low = q.toLowerCase();
  const found = state.nodes.filter(n => !n.isGroup && n.session && !isNewerType(n) && hayOf(n).includes(low));
  const exact = found.find(n => n.name.toLowerCase() === low || (n.session.host || '').toLowerCase() === low);
  if (exact) return { kind: 'session', node: exact };
  if (found.length === 1) return { kind: 'session', node: found[0] };
  if (!found.length || looksLikeAddress(q)) return { kind: 'quick', spec: q };
  return { kind: 'choose', count: found.length };
}

function paintSearchHint() {
  const hint = document.getElementById('search-hint');
  if (!hint) return;
  const act = enterAction(/** @type {HTMLInputElement} */ (document.getElementById('search')).value);
  hint.hidden = !act;
  if (!act) return;
  if (act.kind === 'session') hint.replaceChildren(h('kbd', { text: 'Enter' }), ` connects to ${act.node.name}`, h('span', { class: 'f', text: ` ${hostOf(act.node)}` }));
  else if (act.kind === 'quick') hint.replaceChildren(h('kbd', { text: 'Enter' }), ' quick connect to ', h('b', { text: act.spec }));
  else hint.replaceChildren(h('kbd', { text: '↓' }), ` or `, h('kbd', { text: 'Enter' }), ` to choose one of ${act.count} sessions`);
}

function clearSearch() {
  const search = /** @type {HTMLInputElement} */ (document.getElementById('search'));
  if (!search.value) return;
  search.value = '';
  search.dispatchEvent(new Event('input'));
}

let filterTimer = 0;

// applyFilter draws the tree for what the search field holds now.
function applyFilter() {
  clearTimeout(filterTimer);
  const q = /** @type {HTMLInputElement} */ (document.getElementById('search')).value.trim().toLowerCase();
  if (q === filter) return;
  filter = q;
  showAll = false;
  render();
}

// chooseInTree selects the first matching session and puts the keyboard in
// the tree (arrows, Enter connects).
function chooseInTree() {
  applyFilter(); // typed faster than the filter waits on big trees
  const tree = document.getElementById('tree');
  const first = /** @type {HTMLElement} */ (tree.querySelector('.row.session:not(.fav):not(.newer)'));
  if (first?.dataset.id) select(first.dataset.id);
  tree.focus();
}

function hayOf(n) {
  let hay = haystacks.get(n);
  if (hay === undefined) {
    const s = n.session || {};
    const login = effectiveLogin(n);
    hay = [n.name, n.description, s.host, s.user, login.cred?.name, s.serial?.device, ...(s.clusters || [])].filter(Boolean).join(' ').toLowerCase();
    haystacks.set(n, hay);
  }
  return hay;
}

function matches(n) {
  if (!filter) return true;
  return hayOf(n).includes(filter);
}

function visibleSet() {
  if (!filter) return null;
  const vis = new Set();
  for (const n of state.nodes) {
    if (!n.isGroup && matches(n)) {
      for (let c = n; c; c = state.byId.get(c.parentId)) vis.add(c.id);
    } else if (n.isGroup && n.name.toLowerCase().includes(filter)) {
      vis.add(n.id);
      for (let c = state.byId.get(n.parentId); c; c = state.byId.get(c.parentId)) vis.add(c.id);
    }
  }
  return vis;
}

export function hostOf(n) {
  const s = n.session;
  if (!s) return '';
  if (s.protocol === 'serial') return s.serial?.device || '';
  return s.host + (s.port && s.port !== (s.protocol === 'ssh' ? 22 : 23) ? ':' + s.port : '');
}

// counts caches session counts for one render of the tree.
let counts = null;
export function countSessions(id) {
  if (counts && counts.has(id)) return counts.get(id);
  let c = 0;
  for (const n of state.children.get(id) || []) c += n.isGroup ? countSessions(n.id) : 1;
  if (counts) counts.set(id, c);
  return c;
}

// tooltip: the full name (long names are cut in the list) plus details.
function tooltip(n) {
  const lines = [n.name];
  if (n.isGroup) {
    lines.push(plural(countSessions(n.id), 'session', 'sessions'));
    const d = n.defaults || {};
    if (n.library && !n.parentId) {
      const lib = teamLibOf(n);
      lines.push(`Team library, read-only${lib?.updatedBy ? `, published by ${lib.updatedBy}` : ''}${lib?.stale ? '. The file cannot be reached; this is the last copy YACM read' : ''}`);
    }
    if (d.credentialId) lines.push(`Login for sessions: ${d.credentialId.startsWith('slot:') ? 'team login' : state.credById.get(d.credentialId)?.name || 'missing profile'}`);
    if (d.jumpVia) lines.push(`Jump host for sessions: ${state.byId.get(d.jumpVia)?.name || 'missing session'}`);
  } else {
    const s = n.session || {};
    if (isNewerType(n)) {
      lines.push(`Connection type “${s.protocol}” from a newer version of YACM: kept as it is, but it can only be opened and edited after updating YACM`);
      if (n.description) lines.push(n.description);
      return lines.join('\n');
    }
    const login = effectiveLogin(n);
    lines.push(`${s.protocol?.toUpperCase()} ${login.user ? login.user + '@' : ''}${hostOf(n)}`);
    if (login.cred) lines.push(`Login: ${login.cred.name}${login.from ? ` (from ${login.from.name})` : ''}`);
    else if (login.slot) lines.push(`Login: team login “${login.slot.name}”, not linked to a profile yet`);
    const j = effectiveJump(n);
    if (j.node) lines.push(`Through: ${j.node.name}${j.from ? ` (from ${j.from.name})` : ''}`);
    else if (j.inline) lines.push(`Through: ${s.jump.map(x => x.host).join(' → ')}`);
  }
  if (n.description) lines.push(n.description);
  return lines.join('\n');
}

export function render() {
  const tree = document.getElementById('tree');
  counts = new Map();
  const vis = visibleSet();
  tree.replaceChildren();
  const frag = document.createDocumentFragment();
  let drawn = 0, hidden = 0;
  // Favourites first, when not filtering.
  const favs = vis ? [] : [...(state.favorites || [])].map(id => state.byId.get(id)).filter(n => n && !n.isGroup);
  if (favs.length) {
    const closed = favClosed();
    const head = h('div', { class: 'row group fav-head', role: 'treeitem', 'aria-expanded': String(!closed), style: { paddingLeft: '8px' } },
      h('span', { class: 'caret', text: closed ? '▸' : '▾' }), h('span', { class: 'star on', text: '★' }), h('span', { class: 'name', text: 'Favourites' }), h('span', { class: 'count', text: favs.length }));
    head.addEventListener('click', () => { setFavClosed(!closed); render(); });
    frag.append(head);
    if (!closed) {
      for (const n of favs) {
        const color = colorOf(n);
        const row = h('div', { class: `row session fav${isNewerType(n) ? ' newer' : ''}${state.selected === n.id ? ' selected' : ''}`, role: 'treeitem', draggable: 'false', dataset: { id: n.id }, style: { paddingLeft: '24px' } },
          h('span', { class: 'caret' }), portGlyph(n.session?.protocol),
          color ? h('span', { class: 'swatch inherited', dataset: { color } }) : null,
          h('span', { class: 'name', text: n.name }), h('span', { class: 'host', text: hostOf(n) }));
        row.addEventListener('mouseenter', () => { if (!row.title) row.title = tooltip(n); }, { once: true });
        bindRow(row, n);
        frag.append(row);
      }
    }
  }
  const walk = (pid, depth) => {
    for (const n of state.children.get(pid) || []) {
      if (vis && !vis.has(n.id)) continue;
      if (vis && !showAll && drawn >= MAX_ROWS) { hidden++; if (n.isGroup) walk(n.id, depth + 1); continue; }
      drawn++;
      const open = vis ? true : n.expanded;
      const color = n.isGroup ? (n.color && n.color !== 'none' ? n.color : '') : colorOf(n);
      const libRoot = n.library && !n.parentId;
      const lib = libRoot ? teamLibOf(n) : null;
      const editing = n.isGroup && !n.library ? state.teamLibs.find(l => l.editing && l.sourceGroup === n.id) : null;
      const newer = isNewerType(n);
      const row = h('div', {
        class: `row ${n.isGroup ? 'group' : 'session'}${newer ? ' newer' : ''}${n.library ? ' lib' : ''}${libRoot ? ' lib-root' : ''}${state.selected === n.id ? ' selected' : state.selection.has(n.id) ? ' multi' : ''}`,
        role: 'treeitem', 'aria-expanded': n.isGroup ? String(!!open) : undefined, 'aria-selected': String(state.selection.has(n.id)),
        draggable: n.library ? 'false' : 'true', dataset: { id: n.id },
        style: { paddingLeft: 8 + depth * 16 + 'px' },
      },
        n.isGroup ? h('span', { class: 'caret', text: open ? '▾' : '▸' }) : h('span', { class: 'caret' }),
        n.isGroup ? null : portGlyph(n.session?.protocol),
        color ? h('span', { class: `swatch${n.color ? '' : ' inherited'}`, dataset: { color } }) : null,
        h('span', { class: 'name', text: n.name }),
        !n.isGroup && isFavorite(n.id) ? h('span', { class: 'star on', title: 'Favourite', text: '★' }) : null,
        newer ? h('span', { class: 'badge newer', text: 'newer YACM' }) : null,
        libRoot ? h('span', { class: `badge team${lib?.stale ? ' stale' : ''}`, text: lib?.stale ? 'team · offline' : 'team' }) : null,
        editing ? h('span', { class: `badge team${editing.newer ? ' stale' : ''}`, title: editing.newer ? `${editing.updatedBy} published a newer version` : 'You edit this team library; publish to share changes', text: editing.newer ? 'team · newer' : 'team · editing' }) : null,
        !libRoot && n.isGroup && n.defaults && (n.defaults.credentialId || n.defaults.jumpVia || n.defaults.algorithms || n.defaults.antiIdle) ? h('span', { class: 'badge', text: 'defaults' }) : null,
        n.isGroup ? h('span', { class: 'count', text: countSessions(n.id) || '' }) : h('span', { class: 'host', text: hostOf(n) }),
      );
      // The tooltip is built when the pointer gets there, not for every row.
      row.addEventListener('mouseenter', () => { if (!row.title) row.title = tooltip(n); }, { once: true });
      bindRow(row, n);
      frag.append(row);
      if (n.isGroup && open) walk(n.id, depth + 1);
    }
  };
  walk('', 0);
  tree.append(frag);
  if (hidden) {
    tree.append(h('button', { type: 'button', class: 'tree-more', text: `${plural(hidden, 'more match', 'more matches')} not shown. Type more to narrow the list, or show all.`, on: { click: () => { showAll = true; render(); } } }));
  }
  counts = null;
  if (!state.nodes.length) {
    tree.append(h('div', { class: 'tree-empty' }, 'No sessions yet. Use + to add one, or the import button next to it to bring them over from SecureCRT, Xshell, MobaXterm, mRemoteNG, PuTTY, Ásbrú, ~/.ssh/config, a CSV or Excel list, or NetBox.'));
  } else if (vis && vis.size === 0) {
    tree.append(h('div', { class: 'tree-empty', text: `No saved session matches “${filter}”. Enter connects to it as a new connection.` }));
  }
  renderClusters();
}

function renderClusters() {
  const box = document.getElementById('clusters');
  box.replaceChildren();
  const names = Object.keys(state.clusters).sort((a, b) => a.localeCompare(b));
  if (!names.length) return;
  box.append(h('div', { class: 'label', title: 'Labels across folders, for example CORE in every site. Click a tag to open all its sessions; Type to many pre-selects the open sessions with the same tag.', text: 'Tags (click to open all sessions)' }));
  for (const name of names) {
    box.append(h('button', { class: 'chip', title: `Open all ${state.clusters[name]} sessions tagged ${name}`, on: { click: () => hooks.connectCluster(name) } },
      name, h('span', { class: 'n', text: state.clusters[name] })));
  }
}

function paintSelection() {
  /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('#tree .row')).forEach(r => {
    const id = r.dataset.id;
    r.classList.toggle('selected', id === state.selected);
    r.classList.toggle('multi', id !== state.selected && state.selection.has(id));
    r.setAttribute('aria-selected', String(state.selection.has(id)));
  });
}

export function select(id, mode = 'single') {
  const rows = [.../** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('#tree .row'))].map(r => r.dataset.id);
  if (mode === 'toggle') {
    if (state.selection.has(id) && state.selection.size > 1) {
      state.selection.delete(id);
      if (state.selected === id) state.selected = [...state.selection].pop();
      paintSelection();
      return;
    }
    state.selection.add(id);
    anchorId = id;
  } else if (mode === 'range' && anchorId && rows.includes(anchorId)) {
    const [a, b] = [rows.indexOf(anchorId), rows.indexOf(id)].sort((x, y) => x - y);
    state.selection = new Set(rows.slice(a, b + 1));
  } else {
    state.selection = new Set([id]);
    anchorId = id;
  }
  state.selected = id;
  paintSelection();
}

// reveal opens the groups above a node, selects it and scrolls to it.
export async function reveal(id) {
  showSidebar();
  const n = state.byId.get(id);
  if (!n) return;
  for (let p = state.byId.get(n.parentId); p; p = state.byId.get(p.parentId)) {
    if (!p.expanded) { p.expanded = true; api.SetExpanded(p.id, true).catch(() => {}); }
  }
  const search = /** @type {HTMLInputElement} */ (document.getElementById('search'));
  if (search.value) { search.value = ''; search.dispatchEvent(new Event('input')); }
  render();
  select(id);
  const row = document.querySelector(`#tree .row[data-id="${CSS.escape(id)}"]`);
  row?.scrollIntoView({ block: 'center' });
  document.getElementById('tree').focus();
}

export function selectedIds() {
  if (state.selection.size > 1) return [...state.selection];
  return state.selected ? [state.selected] : [];
}

async function toggle(n) {
  n.expanded = !n.expanded;
  render();
  try { await api.SetExpanded(n.id, n.expanded); } catch { /* cosmetic */ }
}

async function moveMany(ids, parentId, index) {
  try {
    // Keep only the top-most selected nodes; their children move with them.
    const top = ids.filter(id => !ids.some(o => o !== id && isAncestor(o, id)));
    for (const [i, id] of top.entries()) await api.MoveNode(id, parentId, index < 0 ? -1 : index + i);
    if (parentId) {
      const p = state.byId.get(parentId);
      if (p && !p.expanded) await api.SetExpanded(parentId, true);
    }
    await reload();
    render();
  } catch (e) { toast(errMsg(e), 'error'); }
}

function isAncestor(ancestorId, id) {
  for (let n = state.byId.get(id); n; n = state.byId.get(n.parentId)) if (n.parentId === ancestorId) return true;
  return false;
}

// copyAddresses copies user@host (one per line) for pasting into a ticket.
async function copyAddresses(ids) {
  try {
    const list = await api.SessionAddresses(ids);
    if (!list.length) return;
    await runtime.copy(list.join('\n'));
    toast(list.length === 1 ? `Copied ${list[0]}` : `Copied ${list.length} addresses`);
  } catch (e) { toast(errMsg(e), 'error'); }
}

function multiMenu(ids) {
  const sessions = ids.filter(id => !state.byId.get(id)?.isGroup);
  const groups = ids.length - sessions.length;
  const what = [sessions.length ? plural(sessions.length, 'session', 'sessions') : '', groups ? plural(groups, 'group', 'groups') : ''].filter(Boolean).join(' and ');
  const libs = ids.filter(id => state.byId.get(id)?.library);
  if (libs.length) {
    return [
      { label: `Connect ${what}`, action: () => hooks.connectMany(ids) },
      '-',
      { label: 'Copy to my sessions', action: () => hooks.copyToMine(libs), disabled: libs.length !== ids.length },
      { label: 'Copy addresses', action: () => copyAddresses(ids), disabled: !sessions.length },
    ];
  }
  return [
    { label: `Connect ${what}`, action: () => hooks.connectMany(ids) },
    '-',
    { label: `Edit ${what} together…`, action: () => hooks.editMany(ids) },
    { label: 'Copy addresses', action: () => copyAddresses(ids), disabled: !sessions.length, title: 'user@host of each session, one per line' },
    { label: 'Export…', action: () => hooks.exportMany(ids) },
    '-',
    { label: `Delete ${what}`, danger: true, action: () => removeMany(ids) },
  ];
}

function bindRow(row, n) {
  row.addEventListener('click', e => {
    const mode = e.shiftKey ? 'range' : (e.ctrlKey || e.metaKey) ? 'toggle' : 'single';
    select(n.id, mode);
    if (mode === 'single' && n.isGroup && (e.target.classList.contains('caret') || e.detail === 2)) toggle(n);
  });
  row.addEventListener('dblclick', e => { if (!n.isGroup && !e.ctrlKey && !e.shiftKey) hooks.connect(n.id); });
  row.addEventListener('contextmenu', e => {
    e.preventDefault();
    if (!state.selection.has(n.id)) select(n.id);
    if (state.selection.size > 1) { contextMenu(e.clientX, e.clientY, multiMenu([...state.selection])); return; }
    if (n.library) { contextMenu(e.clientX, e.clientY, libraryMenu(n)); return; }
    const editing = n.isGroup ? state.teamLibs.find(l => l.editing && l.sourceGroup === n.id) : null;
    const items = n.isGroup ? [
      { label: `Connect all (${countSessions(n.id)})`, action: () => hooks.connectGroup(n.id), disabled: !countSessions(n.id) },
      '-',
      { label: 'New session here', action: () => hooks.newSession(n.id) },
      { label: 'New group here', action: () => hooks.newGroup(n.id) },
      '-',
      { label: 'Edit group and defaults', key: 'F4', action: () => hooks.edit(n) },
      { label: 'Edit all sessions in it…', action: () => hooks.editMany([n.id]), disabled: !countSessions(n.id) },
      '-',
      { label: 'Export group…', action: () => hooks.exportNode(n.id) },
      editing ? { label: `Publish changes to the team library`, action: () => hooks.publishGroup(n.id) } : { label: 'Publish as team library…', action: () => hooks.publishGroup(n.id), disabled: !countSessions(n.id) },
      editing ? { label: 'Stop editing the team library…', action: () => hooks.stopEditing(editing.id) } : null,
      '-',
      { label: 'Delete group', danger: true, action: () => remove(n) },
    ] : [
      { label: 'Connect', key: 'Enter', action: () => hooks.connect(n.id) },
      { label: 'Browse files', action: () => hooks.openFiles(n.id), disabled: n.session?.protocol !== 'ssh', title: n.session?.protocol === 'ssh' ? 'Copy files to and from the device (SFTP or SCP, through its jump hosts)' : 'Needs an SSH session' },
      '-',
      { label: isFavorite(n.id) ? 'Remove from favourites' : 'Add to favourites', action: () => toggleFavorite(n.id) },
      { label: 'Copy address', action: () => copyAddresses([n.id]), title: 'user@host (and :port) for a ticket or a chat' },
      '-',
      { label: 'Edit', key: 'F4', action: () => hooks.edit(n) },
      { label: 'Duplicate', action: () => duplicate(n) },
      { label: 'Export…', action: () => hooks.exportNode(n.id) },
      '-',
      { label: 'Delete', key: 'Del', danger: true, action: () => remove(n) },
    ];
    contextMenu(e.clientX, e.clientY, items);
  });
  row.addEventListener('dragstart', e => {
    dragIds = state.selection.has(n.id) && state.selection.size > 1 ? [...state.selection] : [n.id];
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', dragIds.join(','));
  });
  row.addEventListener('dragend', () => { dragIds = null; clearDrop(); });
  row.addEventListener('dragover', e => {
    if (!dragIds || dragIds.includes(n.id) || n.library) return;
    e.preventDefault();
    clearDrop();
    const r = row.getBoundingClientRect();
    const upper = e.clientY < r.top + r.height * (n.isGroup ? 0.3 : 0.5);
    row.classList.add(n.isGroup && !upper ? 'drop-into' : 'drop-before');
  });
  row.addEventListener('dragleave', () => row.classList.remove('drop-into', 'drop-before'));
  row.addEventListener('drop', async e => {
    if (!dragIds || dragIds.includes(n.id) || n.library) return;
    e.preventDefault();
    const into = row.classList.contains('drop-into');
    clearDrop();
    if (into) return moveMany(dragIds, n.id, -1);
    const siblings = (state.children.get(n.parentId || '') || []).filter(s => !dragIds.includes(s.id));
    moveMany(dragIds, n.parentId || '', siblings.findIndex(s => s.id === n.id));
  });
}

// libraryMenu: team library nodes can be used and copied, not changed.
function libraryMenu(n) {
  const lib = teamLibOf(n);
  const root = !n.parentId;
  return [
    n.isGroup ? { label: `Connect all (${countSessions(n.id)})`, action: () => hooks.connectGroup(n.id), disabled: !countSessions(n.id) } : { label: 'Connect', key: 'Enter', action: () => hooks.connect(n.id) },
    n.isGroup ? null : { label: 'Browse files', action: () => hooks.openFiles(n.id), disabled: n.session?.protocol !== 'ssh' },
    n.isGroup ? null : '-',
    n.isGroup ? null : { label: isFavorite(n.id) ? 'Remove from favourites' : 'Add to favourites', action: () => toggleFavorite(n.id) },
    n.isGroup ? null : { label: 'Copy address', action: () => copyAddresses([n.id]) },
    '-',
    { label: 'Copy to my sessions', action: () => hooks.copyToMine([n.id]), title: 'An editable copy in your own sessions' },
    root ? { label: 'Edit this library…', action: () => hooks.editLibrary(n.library), disabled: !lib || lib.stale || !!lib.needsNewer, title: lib?.needsNewer ? `Update YACM to edit it: ${lib.needsNewer}` : undefined } : null,
    { label: 'Team library settings…', action: () => hooks.teamSettings() },
  ];
}

function clearDrop() {
  document.querySelectorAll('.drop-into, .drop-before').forEach(r => r.classList.remove('drop-into', 'drop-before'));
}

async function duplicate(n) {
  try {
    const c = await api.DuplicateNode(n.id);
    await reload();
    state.selected = c.id;
    state.selection = new Set([c.id]);
    render();
  } catch (e) { toast(errMsg(e), 'error'); }
}

function jumpUsers(ids) {
  const gone = new Set(ids);
  for (const id of ids) for (const d of descendants(id)) gone.add(d);
  return state.nodes.filter(n => !gone.has(n.id) && ((n.session?.jumpVia && gone.has(n.session.jumpVia)) || (n.defaults?.jumpVia && gone.has(n.defaults.jumpVia)))).map(n => n.name);
}

function descendants(id) {
  const out = [];
  const walk = pid => (state.children.get(pid) || []).forEach(c => { out.push(c.id); walk(c.id); });
  walk(id);
  return out;
}

export async function remove(n) {
  const cnt = n.isGroup ? countSessions(n.id) : 0;
  const users = jumpUsers([n.id]);
  const ok = await confirmDialog({
    title: n.isGroup ? `Delete group “${n.name}”?` : `Delete “${n.name}”?`,
    message: (n.isGroup
      ? `This deletes the group${cnt ? ` and the ${cnt} session${cnt === 1 ? '' : 's'} inside it` : ''}, including stored passwords. This cannot be undone.`
      : 'The session and its stored passwords will be deleted. This cannot be undone.')
      + (users.length ? ` ${users.slice(0, 5).join(', ')}${users.length > 5 ? ` and ${users.length - 5} more` : ''} use${users.length === 1 ? 's' : ''} it as jump host and will connect directly afterwards.` : ''),
    ok: 'Delete', danger: true,
  });
  if (!ok) return;
  try {
    await api.DeleteNode(n.id);
    await reload();
    render();
  } catch (e) { toast(errMsg(e), 'error'); }
}

async function removeMany(ids) {
  const top = ids.filter(id => !ids.some(o => o !== id && isAncestor(o, id)));
  const sessions = new Set();
  for (const id of top) {
    const n = state.byId.get(id);
    if (!n) continue;
    if (n.isGroup) descendants(id).forEach(d => { if (!state.byId.get(d)?.isGroup) sessions.add(d); });
    else sessions.add(id);
  }
  const users = jumpUsers(top);
  const ok = await confirmDialog({
    title: `Delete ${plural(top.length, 'item', 'items')}?`,
    message: `This deletes ${plural(sessions.size, 'session', 'sessions')} and their stored passwords. This cannot be undone.` + (users.length ? ` ${users.length} other session(s) use one of them as jump host and will connect directly afterwards.` : ''),
    ok: 'Delete', danger: true,
  });
  if (!ok) return;
  try {
    for (const id of top) await api.DeleteNode(id);
    state.selection.clear();
    state.selected = null;
    await reload();
    render();
  } catch (e) { toast(errMsg(e), 'error'); await reload(); render(); }
}

function onKey(e) {
  const rows = [.../** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('#tree .row'))];
  if (!rows.length) return;
  let i = rows.findIndex(r => r.dataset.id === state.selected);
  const n = state.byId.get(state.selected);
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
    e.preventDefault();
    // With a filter, every match counts, also those not drawn in a long list.
    const vis = visibleSet();
    state.selection = vis ? new Set([...vis].filter(id => { const n = state.byId.get(id); return n && (!n.isGroup || n.name.toLowerCase().includes(filter)); })) : new Set(rows.map(r => r.dataset.id));
    paintSelection();
    return;
  }
  switch (e.key) {
    case 'ArrowDown': i = Math.min(rows.length - 1, i + 1); break;
    case 'ArrowUp': i = Math.max(0, i - 1); break;
    case 'ArrowRight': if (n?.isGroup && !n.expanded) toggle(n); return;
    case 'ArrowLeft': if (n?.isGroup && n.expanded) toggle(n); else if (n?.parentId) select(n.parentId); return;
    case 'Enter':
      if (state.selection.size > 1) hooks.connectMany([...state.selection]);
      else if (n && !n.isGroup) hooks.connect(n.id); else if (n) toggle(n);
      return;
    case 'F4':
      if (n?.library) { toast('Team library sessions are read-only. Use Copy to my sessions to change one.'); return; }
      if (state.selection.size > 1) hooks.editMany([...state.selection]); else if (n) hooks.edit(n);
      return;
    case 'Delete':
      if (n?.library) { toast('Team library sessions are read-only. Remove the library in Settings > Team libraries.'); return; }
      if (state.selection.size > 1) removeMany([...state.selection]); else if (n) remove(n);
      return;
    case 'Escape': if (n) select(n.id); return;
    default: return;
  }
  e.preventDefault();
  select(rows[i].dataset.id, e.shiftKey ? 'range' : 'single');
  rows[i].scrollIntoView({ block: 'nearest' });
}
