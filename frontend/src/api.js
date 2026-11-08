// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Thin wrapper around the Wails bindings (window.go.core.App) and runtime.

function call(method, ...args) {
  const fn = window.go?.core?.App?.[method];
  if (!fn) return Promise.reject(new Error(`Backend method ${method} is not available`));
  return fn(...args);
}

/** @type {import('./backend').Backend} */
export const api = /** @type {any} */ (new Proxy({}, { get: (_, m) => (...a) => call(m, ...a) }));

export function on(event, cb) {
  return window.runtime.EventsOn(event, cb);
}

export const runtime = {
  copy: text => window.runtime.ClipboardSetText(text),
  paste: () => window.runtime.ClipboardGetText(),
  openURL: url => window.runtime.BrowserOpenURL(url),
};

// Shared application state.
export const state = {
  nodes: [],
  byId: new Map(),
  children: new Map(),
  settings: {},
  clusters: {},
  credentials: [],
  credById: new Map(),
  teamLibs: [],
  teamSnippets: [],
  favorites: new Set(), // personal favourite session IDs (per computer)
  selected: null,
  selection: new Set(), // multi-select in the tree (always contains selected)
  vault: { exists: false, unlocked: false },
};

export async function reload() {
  const s = await api.GetState();
  state.nodes = s.nodes || [];
  state.settings = s.settings || {};
  state.clusters = s.clusters || {};
  state.credentials = s.credentials || [];
  state.teamSnippets = s.teamSnippets || [];
  state.teamLibs = s.teamLibraries || [];
  state.credById = new Map(state.credentials.map(c => [c.id, c]));
  state.byId = new Map(state.nodes.map(n => [n.id, n]));
  for (const id of [...state.selection]) if (!state.byId.has(id)) state.selection.delete(id);
  state.children = new Map();
  for (const n of state.nodes) {
    const k = n.parentId || '';
    if (!state.children.has(k)) state.children.set(k, []);
    state.children.get(k).push(n);
  }
  for (const list of state.children.values()) {
    // Your groups, then your sessions, then team libraries at the bottom.
    const rank = n => (n.library && !n.parentId ? 2 : n.isGroup ? 0 : 1);
    list.sort((a, b) => rank(a) - rank(b) || a.order - b.order || a.name.localeCompare(b.name));
  }
  document.documentElement.dataset.theme = state.settings.theme === 'light' ? 'light' : 'dark';
  state.favorites = new Set((await api.Favorites().catch(() => [])) || []);
  return s;
}

export function pathOf(id) {
  const parts = [];
  for (let n = state.byId.get(id); n; n = state.byId.get(n.parentId)) parts.unshift(n.name);
  return parts;
}

export function groupsFlat() {
  const out = [];
  const walk = (pid, depth) => {
    for (const n of state.children.get(pid) || []) {
      if (!n.isGroup || n.library) continue; // team library groups are read-only
      out.push({ id: n.id, name: n.name, depth });
      walk(n.id, depth + 1);
    }
  };
  walk('', 0);
  return out;
}

// ---- inheritance (mirrors store.Effective for display) ----

// inherited returns { value, from } for a group default field, starting at
// the parent of the node.
export function inherited(parentId, field) {
  for (let g = state.byId.get(parentId), i = 0; g && i < 64; g = state.byId.get(g.parentId), i++) {
    const v = g.defaults?.[field];
    if (v) return { value: v, from: g };
  }
  return { value: '', from: null };
}

// colorOf returns the colour a node shows (its own or its group's).
// Connection types this version can open. Sessions of other types come from
// a newer YACM: they are kept and shown, but cannot be opened or edited here.
export const KNOWN_PROTOCOLS = ['ssh', 'telnet', 'serial'];
export function isNewerType(n) {
  return !!(n && !n.isGroup && n.session && !KNOWN_PROTOCOLS.includes(n.session.protocol));
}
export function newerTypeText(n) {
  return `${n.name} uses the connection type “${n.session?.protocol}” from a newer version of YACM. It is kept as it is; update YACM to open or edit it.`;
}

export function colorOf(n) {
  for (let c = n, i = 0; c && i < 64; c = state.byId.get(c.parentId), i++) {
    if (c.color === 'none') return '';
    if (c.color) return c.color;
  }
  return '';
}

// effectiveLogin describes the login a session uses.
export function effectiveLogin(n) {
  const s = n.session || {};
  let cid = s.credentialId || '';
  let from = null;
  if (cid === 'inherit') ({ value: cid, from } = inherited(n.parentId, 'credentialId'));
  let slot = null;
  if (cid.startsWith('slot:')) {
    // A team library login: the profile this user linked to it.
    const lib = state.teamLibs.find(l => l.id === n.library);
    slot = lib?.logins.find(l => l.id === cid.slice(5)) || { name: cid.slice(5) };
    cid = slot.linked || '';
  }
  const cred = cid ? state.credById.get(cid) : null;
  if (cred) return { cred, user: cred.user || s.user, from, slot };
  return { cred: null, user: s.user, from, slot };
}

// teamLibOf returns the status of the team library a node belongs to.
export function teamLibOf(n) { return n?.library ? state.teamLibs.find(l => l.id === n.library) || null : null; }

// effectiveJump returns the session used as jump host (or null).
export function effectiveJump(n) {
  const s = n.session || {};
  let via = s.jumpVia || '';
  let from = null;
  if (via === 'inherit') ({ value: via, from } = inherited(n.parentId, 'jumpVia'));
  if (via === n.id) via = '';
  return { node: via ? state.byId.get(via) || null : null, from, inline: !via && (s.jump || []).length > 0 };
}

export const COLORS = ['red', 'orange', 'yellow', 'green', 'teal', 'blue', 'purple', 'gray'];
export const COLOR_LABELS = { red: 'Red', orange: 'Orange', yellow: 'Yellow', green: 'Green', teal: 'Teal', blue: 'Blue', purple: 'Purple', gray: 'Gray' };

export async function saveSettings(patch) {
  const st = { ...state.settings, ...patch };
  await api.SaveSettings(st);
  state.settings = st;
}
