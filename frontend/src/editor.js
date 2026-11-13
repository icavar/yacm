// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Session and group editor.
import { api, state, reload, groupsFlat, inherited, COLORS, COLOR_LABELS, isNewerType, newerTypeText } from './api.js';
import { h, openDialog, field, check, seg, portGlyph, uuid, errMsg, toast, colorPicker, plural, KEY } from './ui.js';
import { openProfiles } from './profiles.js';
import { sessionPicker } from './picker.js';
import { picker as termPicker, preview as termPreview, resolve as termResolve, colorsFor, sourceText } from './termcolors.js';

const BAUDS = [1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400];

function blankSession(protocol = 'ssh') {
  return {
    protocol, host: '', port: protocol === 'telnet' ? 23 : 22, user: '',
    auth: 'password', keyPath: '', certPath: '', jump: [], algorithms: 'secure', keepAlive: 30, agentForward: false, connectTimeout: 15,
    serial: { device: '', baud: 9600, dataBits: 8, parity: 'none', stopBits: '1' },
    expect: [], expectTimeout: 20, before: [], after: [], clusters: [], macros: [],
    logToFile: false, logDir: '', logTimestamps: false, fontSize: 0, scrollback: 0, pasteDelay: 0, noHighlight: false,
    credentialId: '', jumpVia: '', termText: '', termBackground: '',
  };
}

const ALGO_LABELS = { secure: 'Secure (recommended)', compatible: 'Compatible with older devices', legacy: 'Legacy (very old IOS, ASA, switches)' };

function sshSessions(excludeId) {
  return state.nodes.filter(n => n.session?.protocol === 'ssh' && n.id !== excludeId)
    .sort((a, b) => a.name.localeCompare(b.name));
}

// credentialSelect lists profiles; first options depend on the context.
/** @param {string} value @param {{ own?: string, inherit?: string }} [opts] */
function credentialSelect(value, { own, inherit } = {}) {
  const sel = h('select');
  if (own) sel.append(h('option', { value: '', text: own }));
  if (inherit) sel.append(h('option', { value: 'inherit', text: inherit }));
  if (state.credentials.length) {
    const og = h('optgroup', { label: 'Credential profiles' });
    for (const c of state.credentials) og.append(h('option', { value: c.id, text: `${c.name}${c.user ? ` (${c.user})` : ''}` }));
    sel.append(og);
  }
  sel.value = value || '';
  if (sel.value !== (value || '')) sel.value = '';
  return sel;
}

function groupSelect(value, excludeId) {
  const sel = h('select');
  sel.append(h('option', { value: '', text: 'Top level' }));
  for (const g of groupsFlat()) {
    if (excludeId && (g.id === excludeId)) continue;
    sel.append(h('option', { value: g.id, text: '  '.repeat(g.depth + 1) + g.name }));
  }
  sel.value = value || '';
  return sel;
}

// antiIdleText describes what a session inherits from its groups.
function inheritedAntiIdle(parentId) {
  const sec = inherited(parentId, 'antiIdle');
  const text = inherited(parentId, 'antiIdleText');
  return { sec: sec.value > 0 ? sec.value : 0, from: sec.from, text: text.value || '!' };
}

// antiIdleFields edits {antiIdle, antiIdleText} on a session or on group
// defaults: 0 = from the group (or "not set" on a group), -1 = off.
function antiIdleFields(o, parentId, { group = false } = {}) {
  const inh = inheritedAntiIdle(parentId);
  const mode = h('select');
  const inhText = inh.sec ? `From group: every ${inh.sec} s (${inh.from.name})` : 'From group (the group sets none: off)';
  mode.append(h('option', { value: 'inherit', text: group ? (inh.sec ? `Not set here (parent: every ${inh.sec} s)` : 'Not set here (each session decides)') : inhText }),
    h('option', { value: 'off', text: 'Off' }), h('option', { value: 'on', text: 'Send text when idle' }));
  mode.value = o.antiIdle > 0 ? 'on' : o.antiIdle < 0 ? 'off' : 'inherit';
  const secs = h('input', { type: 'number', min: 10, max: 3600, step: 10, value: o.antiIdle > 0 ? o.antiIdle : 240, on: { input: e => { if (mode.value === 'on') o.antiIdle = +e.target.value || 0; } } });
  const text = h('input', { type: 'text', value: o.antiIdleText || '', maxLength: 64, spellcheck: false, placeholder: group ? '!' : inh.text, class: 'mono-input', on: { input: e => (o.antiIdleText = e.target.value) } });
  const details = h('div', { class: 'grid2' }, field('After this many idle seconds', secs), field('Text to type ({enter} adds Enter)', text));
  const sync = () => {
    o.antiIdle = mode.value === 'on' ? (+secs.value || 240) : mode.value === 'off' ? -1 : 0;
    details.hidden = mode.value !== 'on' && !(mode.value === 'inherit' && !group && inh.sec);
    secs.disabled = mode.value !== 'on';
  };
  mode.addEventListener('change', sync);
  sync();
  return h('div', {},
    field('Anti-idle', mode, 'Types “!” on the command line every so many seconds while you are not typing, without Enter, so the device does not log the session out (exec-timeout). Before your next key the typed “!” are erased again, so your command starts on a clean line. Only at a CLI prompt ending in > or #, never in a [confirm] or password question, and never while pasting or while automation runs. For systems that only count whole lines (a Linux shell), use a text with {enter}.'),
    details,
    h('p', { class: 'hint', style: { color: 'var(--faint)', margin: '-4px 0 8px' }, text: 'Note: this works around an exec-timeout your security policy may require; a session left open stays logged in.' }));
}

// proxyFields edits an outbound proxy ({type, host, port, credentialId}) of
// a session (null = from the group) or of group defaults (null = none).
// get/set read and write the proxy object.
function proxyFields(get, set, parentId, { group = false } = {}) {
  const g = inherited(parentId, 'proxy');
  const gp = g.value;
  const wrap = h('div', {});
  const paint = () => {
    const p = get();
    const mode = h('select');
    if (!group) mode.append(h('option', { value: 'inherit', text: gp ? `From group: ${gp.type === 'http' ? 'HTTP' : 'SOCKS5'} proxy ${gp.host}:${gp.port} (${g.from.name})` : 'From group (the group sets none: direct)' }));
    mode.append(h('option', { value: 'none', text: group ? 'Not set here (each session decides)' : 'No proxy' }),
      h('option', { value: 'socks5', text: 'SOCKS5 proxy' }), h('option', { value: 'http', text: 'HTTP proxy (CONNECT)' }));
    mode.value = !p ? (group ? 'none' : 'inherit') : p.type;
    mode.addEventListener('change', () => {
      const v = mode.value;
      if (v === 'inherit' || (group && v === 'none')) set(null);
      else if (v === 'none') set({ type: 'none' });
      else set({ type: v, host: p?.host || '', port: p?.port || (v === 'http' ? 3128 : 1080), credentialId: p?.credentialId || '' });
      paint();
    });
    const parts = [field('Outbound proxy', mode, 'Reaches the device, or its first SSH jump host, through a company SOCKS5 or HTTP proxy, for SSH and telnet. Tunnels and file transfers use the same connection.')];
    if (p && (p.type === 'socks5' || p.type === 'http')) {
      const host = h('input', { type: 'text', value: p.host || '', spellcheck: false, placeholder: 'proxy.example.net', on: { input: e => (p.host = e.target.value.trim()) } });
      const port = h('input', { type: 'number', min: 1, max: 65535, value: p.port || '', on: { input: e => (p.port = +e.target.value) } });
      const login = credentialSelect(p.credentialId || '', { own: 'No login' });
      login.addEventListener('change', () => (p.credentialId = login.value));
      parts.push(h('div', { class: 'grid-host' }, h('label', { class: 'field mono' }, h('span', { text: 'Proxy host' }), host), h('label', { class: 'field mono' }, h('span', { text: 'Port' }), port)),
        field('Proxy login (credential profile)', login, 'User name and password from a credential profile, kept in the vault.'));
    }
    wrap.replaceChildren(...parts);
  };
  paint();
  return wrap;
}

export function editGroup(node, parentId = '') {
  const n = node ? structuredClone(node) : { id: '', parentId, isGroup: true, name: '', description: '', color: '' };
  const d = { credentialId: '', jumpVia: '', algorithms: '', antiIdle: 0, antiIdleText: '', termText: '', termBackground: '', logName: '', askTicket: false, proxy: null, ...(n.defaults || {}) };
  const proxyBox = proxyFields(() => d.proxy, v => (d.proxy = v), n.parentId, { group: true });
  const before = JSON.stringify({ credentialId: d.credentialId, jumpVia: d.jumpVia, algorithms: d.algorithms, antiIdle: d.antiIdle });
  const idleBox = antiIdleFields(d, n.parentId, { group: true });
  const name = h('input', { type: 'text', value: n.name, spellcheck: false });
  const desc = h('input', { type: 'text', value: n.description || '' });
  const parent = groupSelect(n.parentId, n.id);
  const color = colorPicker(n.color, v => (n.color = v), { colors: COLORS, labels: COLOR_LABELS, inheritText: 'From parent' });

  const cred = credentialSelect(d.credentialId, { own: 'Nothing (each session decides)' });
  const jump = sessionPicker({ value: d.jumpVia || '', nodes: () => sshSessions(''), specials: [['', 'Nothing (each session decides)']], label: 'Jump host for the sessions' });
  const algo = h('select', {}, h('option', { value: '', text: 'Nothing (each session decides)' }), ...Object.entries(ALGO_LABELS).map(([v, t]) => h('option', { value: v, text: t })));
  algo.value = d.algorithms || '';
  const sessions = node ? countUnder(node.id) : 0;
  const apply = h('input', { type: 'checkbox', checked: true });
  const applyBox = check(`Make the ${plural(sessions, 'session', 'sessions')} already in this group use these defaults`, apply,
    'Their login, jump host, algorithms or anti-idle (only the ones set above) switch to “From group”. You can still override any session.');
  applyBox.hidden = true;
  const refreshApply = () => {
    applyBox.hidden = !sessions || JSON.stringify({ credentialId: cred.value, jumpVia: jump.value, algorithms: algo.value, antiIdle: d.antiIdle }) === before || !(cred.value || jump.value || algo.value || d.antiIdle);
  };
  [cred, jump, algo].forEach(x => x.addEventListener('change', refreshApply));
  idleBox.addEventListener('change', refreshApply);
  idleBox.addEventListener('input', refreshApply);
  const inhGroup = key => { const r = termResolve(null, key, parent.value); return `Not set here (${sourceText(r)})`; };
  const gText = termPicker(d.termText, v => { d.termText = v; gPrev.update(); }, { kind: 'text', inheritText: inhGroup('termText') });
  const gBg = termPicker(d.termBackground, v => { d.termBackground = v; gPrev.update(); }, { kind: 'bg', inheritText: inhGroup('termBackground') });
  const gPrev = termPreview(() => colorsFor({ id: n.id, isGroup: true, parentId: parent.value, defaults: d }));
  const gLogName = h('input', { type: 'text', value: d.logName || '', spellcheck: false, class: 'mono-input', placeholder: state.settings.logNameTemplate || '{group}/{name}_{date}_{time}.log', on: { input: e => (d.logName = e.target.value) } });
  const gTicket = h('input', { type: 'checkbox', checked: !!d.askTicket, on: { change: e => (d.askTicket = e.target.checked) } });
  const logBits = [
    state.settings.logNameTemplateOn && state.settings.logNameOwn ? field('Log file name template', gLogName, 'For the sessions inside; empty = the template in Settings. Tokens as in Settings > Session logs.') : null,
    state.settings.askTicket === 'groups' ? check('Ask for a change or ticket number when its sessions connect', gTicket, 'For sessions that write a log; the number goes into {ticket} in the log file name.') : null,
  ];
  const manage = h('button', { type: 'button', class: 'btn small', text: 'Profiles…', on: { click: () => openProfiles(() => {
    const v = cred.value; const fresh = credentialSelect(v, { own: 'Nothing (each session decides)' });
    cred.replaceChildren(...fresh.childNodes); cred.value = v;
  }) } });

  openDialog({
    title: node ? `Edit group ${node.name}` : 'New group',
    medium: true,
    body: h('div', {},
      field('Name', name), field('Description', desc),
      h('div', { class: 'grid2' }, field('Inside', parent), h('div', { class: 'field' }, h('span', { text: 'Colour of its tabs' }), color)),
      h('h3', { text: 'Defaults for the sessions inside', style: { margin: '10px 0 4px', fontSize: '16px', fontWeight: '600' } }),
      h('p', { class: 'hint', style: { color: 'var(--muted)', margin: '0 0 10px' }, text: 'Sessions set to “From group” use these, also in subgroups. Change the password once in the profile and every session follows.' }),
      h('label', { class: 'field' }, h('span', { text: 'Login (credential profile)' }), h('div', { class: 'with-btn' }, cred, manage)),
      field('Jump host (a saved SSH session)', jump),
      field('SSH algorithms', algo),
      proxyBox,
      idleBox,
      h('div', { class: 'field' }, h('span', { text: 'Terminal text colour' }), gText),
      h('div', { class: 'field' }, h('span', { text: 'Terminal background' }), gBg),
      gPrev.el,
      ...logBits,
      applyBox),
    buttons: [
      { label: 'Cancel' },
      { label: node ? 'Save group' : 'Create group', kind: 'primary', default: true, onClick: async () => {
        n.name = name.value.trim(); n.description = desc.value.trim(); n.parentId = parent.value;
        n.defaults = { ...(n.defaults || {}), credentialId: cred.value, jumpVia: jump.value, algorithms: algo.value, antiIdle: d.antiIdle, antiIdleText: (d.antiIdleText || '').trim(), termText: d.termText, termBackground: d.termBackground, logName: (d.logName || '').trim(), askTicket: !!d.askTicket, proxy: d.proxy || null };
        if (!n.defaults.proxy) delete n.defaults.proxy;
        const saved = await api.SaveNode(n, null);
        if (!applyBox.hidden && apply.checked) {
          const change = {};
          if (cred.value) change.credentialId = 'inherit';
          if (jump.value) change.jumpVia = 'inherit';
          if (algo.value) change.algorithms = 'inherit';
          if (d.antiIdle) { change.antiIdle = 0; change.antiIdleText = ''; }
          await api.BulkUpdate({ ids: [saved.id], change, password: null });
        }
        await reload();
        window.dispatchEvent(new Event('yacm:tree'));
      } },
    ],
  });
}

function countUnder(id) {
  let c = 0;
  for (const n of state.children.get(id) || []) c += n.isGroup ? countUnder(n.id) : 1;
  return c;
}

// Secret input: shows whether a value is stored, lets the user replace,
// reveal (after re-entering the master password) or remove it.
export function secretField(label, stored, onChange, reveal) {
  const inp = h('input', { type: 'password', autocomplete: 'off', spellcheck: false, placeholder: stored ? 'Stored in the vault. Type to replace.' : '' });
  let removed = false;
  const status = h('span', { class: 'secret-state', text: stored ? 'Stored' : '' });
  inp.addEventListener('input', () => { removed = false; onChange(inp.value === '' ? undefined : inp.value); status.textContent = inp.value ? 'Will be replaced' : (stored ? 'Stored' : ''); });
  const btns = [];
  if (stored && reveal) {
    btns.push(h('button', { type: 'button', class: 'btn small', text: 'Show', on: { click: async () => {
      const v = await askMaster();
      if (v === null) return;
      try { inp.value = await reveal(v); inp.type = 'text'; onChange(undefined); status.textContent = 'Shown'; } catch (e) { toast(errMsg(e), 'error'); }
    } } }));
  }
  if (stored) {
    btns.push(h('button', { type: 'button', class: 'btn small', text: 'Remove', on: { click: () => {
      removed = !removed; inp.value = ''; inp.disabled = removed; onChange(removed ? '' : undefined);
      status.textContent = removed ? 'Will be removed' : 'Stored'; status.style.color = removed ? 'var(--danger)' : '';
    } } }));
  }
  return h('label', { class: 'field' }, h('span', {}, label, ' ', status), h('div', { class: 'with-btn' }, inp, ...btns));
}

export function askMaster() {
  return new Promise(resolve => {
    const inp = h('input', { type: 'password', autocomplete: 'off' });
    let val = null;
    openDialog({
      title: 'Confirm with master password', body: field('Master password', inp),
      buttons: [{ label: 'Cancel' }, { label: 'Show', kind: 'primary', default: true, onClick: () => { val = inp.value; } }],
      onClose: () => resolve(val), initialFocus: inp,
    });
  });
}

export async function editSession(node, parentId = '', startSection = '') {
  if (isNewerType(node)) {
    openDialog({ title: node.name, body: h('p', { class: 'lead', text: newerTypeText(node) }), buttons: [{ label: 'Close', kind: 'primary', default: true }] });
    return;
  }
  const isNew = !node;
  const n = node ? structuredClone(node) : { id: '', parentId, isGroup: false, name: '', description: '', color: '', session: blankSession() };
  n.session = { ...blankSession(n.session.protocol), ...n.session };
  if (isNew) {
    n.session.logToFile = !state.settings.logNewOff;
    if (inherited(parentId, 'credentialId').value) n.session.credentialId = 'inherit';
    if (inherited(parentId, 'jumpVia').value) n.session.jumpVia = 'inherit';
    if (inherited(parentId, 'algorithms').value) n.session.algorithms = 'inherit';
  }
  n.session.serial = { ...blankSession().serial, ...(n.session.serial || {}) };
  const s = n.session;
  for (const k of ['jump', 'expect', 'before', 'after', 'clusters', 'macros', 'tunnels']) s[k] = s[k] || [];
  const flags = node ? await api.GetSecretFlags(node.id).catch(() => ({ jump: {}, expect: {} })) : { jump: {}, expect: {} };
  const sec = { password: undefined, passphrase: undefined, jump: {}, expect: {}, jumpOrigin: s.jump.map((_, i) => i) };
  const vaultOK = state.vault.unlocked;
  const reveal = (kind, ref = '') => master => api.RevealSecret(n.id, kind, ref, master);

  // profileInUse returns { cred, from } when the login comes from a profile.
  const profileInUse = () => {
    let cid = s.credentialId || '';
    let from = null;
    if (cid === 'inherit') ({ value: cid, from } = inherited(n.parentId, 'credentialId'));
    const cred = cid ? state.credById.get(cid) : null;
    return cred ? { cred, from } : null;
  };
  const groupProfileText = () => {
    const g = inherited(n.parentId, 'credentialId');
    const c = g.value ? state.credById.get(g.value) : null;
    return c ? `From group: ${c.name}${c.user ? ` (${c.user})` : ''}` : 'From group (the group sets no login)';
  };
  const loginPicker = () => {
    const sel = credentialSelect(s.credentialId, { own: 'This session’s own login', inherit: groupProfileText() });
    sel.addEventListener('change', () => { s.credentialId = sel.value; show(current); });
    const manage = h('button', { type: 'button', class: 'btn small', text: 'Profiles…', on: { click: () => openProfiles(() => show(current)) } });
    const prof = profileInUse();
    return h('label', { class: 'field' }, h('span', { text: 'Login' }), h('div', { class: 'with-btn' }, sel, manage),
      prof ? h('span', { class: 'inherit-note' }, 'User ', h('b', { text: prof.cred.user || s.user || '(none)' }), `, ${prof.cred.auth === 'publickey' ? 'key file' : prof.cred.auth === 'agent' ? 'SSH agent' : prof.cred.auth === 'ask' ? 'asked when connecting' : prof.cred.hasPassword ? 'password stored in the profile' : 'no password stored yet'}. Changing the profile changes it for every session that uses it.`)
        : s.credentialId === 'inherit' ? h('span', { class: 'inherit-note', text: 'The group defines no login, so the session’s own login below is used.' }) : null);
  };

  const pane = h('div', { class: 'pane' });
  const nav = h('nav');
  /** @type {[string, () => any, (() => boolean)?][]} */
  const sections = [
    ['Connection', connection],
    ['Authentication', auth],
    ['Jump hosts', jumps, () => s.protocol === 'ssh' || s.protocol === 'telnet'],
    ['Tunnels and proxy', tunnels, () => s.protocol === 'ssh' || s.protocol === 'telnet'],
    ['Automation', automation],
    ['Saved commands', macros],
    ['Terminal and logs', terminal],
  ];
  let current = 0;
  const show = i => {
    current = i;
    nav.querySelectorAll('button').forEach((b, j) => b.classList.toggle('on', j === i));
    pane.replaceChildren(...[sections[i][1]()].flat().filter(Boolean));
    pane.scrollTop = 0;
  };
  const refreshNav = () => nav.querySelectorAll('button').forEach((b, j) => { const en = sections[j][2]; b.disabled = en ? !en() : false; });
  sections.forEach(([label], i) => nav.append(h('button', { type: 'button', text: label, on: { click: () => show(i) } })));

  // ---- sections ----
  function connection() {
    const name = h('input', { type: 'text', value: n.name, spellcheck: false, on: { input: e => (n.name = e.target.value) } });
    const desc = h('textarea', { rows: 2, on: { input: e => (n.description = e.target.value) } }); desc.value = n.description || '';
    const parent = groupSelect(n.parentId); parent.addEventListener('change', () => (n.parentId = parent.value));
    const proto = seg([
      { value: 'ssh', label: 'SSH', icon: portGlyph('ssh') },
      { value: 'telnet', label: 'Telnet', icon: portGlyph('telnet') },
      { value: 'serial', label: 'Serial', icon: portGlyph('serial') },
    ], s.protocol, v => {
      const defPort = { ssh: 22, telnet: 23 };
      if (!s.port || s.port === defPort[s.protocol]) s.port = defPort[v] || 0;
      s.protocol = v; refreshNav(); show(0);
    });
    const known = Object.keys(state.clusters).sort((a, b) => a.localeCompare(b));
    const tags = h('input', { type: 'text', value: s.clusters.join(', '), spellcheck: false, list: 'tag-names', placeholder: 'CORE, EDGE', on: { input: e => (s.clusters = e.target.value.split(',').map(x => x.trim()).filter(Boolean)) } });
    const out = [h('h3', { text: 'Connection' }), field('Name', name), h('div', { class: 'grid2' }, field('Group', parent), h('div', { class: 'field' }, h('span', { text: 'Protocol' }), proto)), field('Description', desc),
      field('Tags (comma separated)', tags, `Labels across folders, for example CORE in every site. A tag opens all its sessions at once and pre-selects them for Type to many.${known.length ? ` Existing: ${known.join(', ')}` : ''}`),
      h('datalist', { id: 'tag-names' }, ...known.map(k => h('option', { value: k })))];
    if (s.protocol === 'serial') {
      const dev = h('input', { type: 'text', value: s.serial.device, list: 'serial-ports', spellcheck: false, placeholder: navigator.platform.startsWith('Win') ? 'COM3' : '/dev/ttyUSB0', on: { input: e => (s.serial.device = e.target.value.trim()) } });
      const dl = h('datalist', { id: 'serial-ports' });
      const scan = async () => {
        try {
          const ports = await api.ListSerialPorts();
          dl.replaceChildren(...(ports || []).map(p => h('option', { value: p.name, text: p.label })));
          if (!s.serial.device && ports?.length) { s.serial.device = ports[0].name; dev.value = ports[0].name; }
          hint.textContent = ports?.length ? `Found: ${ports.map(p => p.label).join(', ')}` : 'No serial ports found. Plug in the console cable and press Rescan.';
        } catch (e) { hint.textContent = errMsg(e); }
      };
      const hint = h('span', { class: 'hint', text: 'Looking for ports…' });
      const baud = h('select', { on: { change: e => (s.serial.baud = +e.target.value) } }, ...BAUDS.map(b => h('option', { value: b, text: b })));
      baud.value = s.serial.baud;
      const bits = h('select', { on: { change: e => (s.serial.dataBits = +e.target.value) } }, ...[8, 7, 6, 5].map(b => h('option', { value: b, text: b }))); bits.value = s.serial.dataBits;
      const par = h('select', { on: { change: e => (s.serial.parity = e.target.value) } }, ...['none', 'even', 'odd', 'mark', 'space'].map(b => h('option', { value: b, text: b }))); par.value = s.serial.parity;
      const stop = h('select', { on: { change: e => (s.serial.stopBits = e.target.value) } }, ...['1', '1.5', '2'].map(b => h('option', { value: b, text: b }))); stop.value = s.serial.stopBits;
      scan();
      out.push(h('label', { class: 'field mono' }, h('span', { text: 'Port' }), h('div', { class: 'with-btn' }, dev, h('button', { type: 'button', class: 'btn small', text: 'Rescan', on: { click: scan } })), hint), dl,
        h('div', { class: 'grid2' }, field('Speed (baud)', baud), field('Data bits', bits), field('Parity', par), field('Stop bits', stop)),
        h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Cisco and most network consoles use 9600 8N1.' }));
    } else {
      const host = h('input', { type: 'text', value: s.host, spellcheck: false, placeholder: '10.0.0.1 or router.example.net', on: { input: e => (s.host = e.target.value.trim()) } });
      const port = h('input', { type: 'number', min: 1, max: 65535, value: s.port, on: { input: e => (s.port = +e.target.value) } });
      const user = h('input', { type: 'text', value: s.user, spellcheck: false, on: { input: e => (s.user = e.target.value) } });
      const prof = profileInUse();
      if (prof) { user.disabled = true; user.value = prof.cred.user || s.user; }
      out.push(h('div', { class: 'grid-host' }, h('label', { class: 'field mono' }, h('span', { text: 'Host' }), host), h('label', { class: 'field mono' }, h('span', { text: 'Port' }), port)),
        h('label', { class: 'field mono' }, h('span', { text: 'Username' }), user,
          prof ? h('span', { class: 'inherit-note' }, 'From credential profile ', h('b', { text: prof.cred.name }), prof.from ? ` (set by group ${prof.from.name})` : '', '. Change it under Authentication.') : null));
      if (s.protocol === 'telnet') out.push(h('p', { class: 'warning', text: 'Telnet is not encrypted: the password and everything you type cross the network in clear text. Use it only on isolated lab or out-of-band networks.' }));
    }
    out.push(...keepOpenFields());
    return out;
  }

  // Keepalive, connect timeout and anti-idle: what keeps the session open.
  function keepOpenFields() {
    const out = [h('h3', { text: 'Keeping the session open', style: { marginTop: '18px' } })];
    if (s.protocol !== 'serial') {
      const ka = h('input', { type: 'number', min: 0, max: 600, value: s.keepAlive || 0, disabled: s.protocol !== 'ssh', on: { input: e => (s.keepAlive = Math.max(0, +e.target.value || 0)) } });
      const ct = h('input', { type: 'number', min: 1, max: 300, value: s.connectTimeout || 15, on: { input: e => (s.connectTimeout = +e.target.value) } });
      out.push(h('div', { class: 'grid2' },
        field('SSH keepalive every (seconds, 0 = off)', ka, s.protocol === 'ssh' ? 'Invisible packets that keep firewalls and NAT from dropping a quiet connection, and notice a dead one. They do not stop the device’s exec-timeout.' : 'SSH only.'),
        field('Connect timeout (seconds)', ct)));
    }
    out.push(antiIdleFields(s, n.parentId));
    return out;
  }

  function auth() {
    const out = [h('h3', { text: 'Authentication' })];
    if (!vaultOK) out.push(h('p', { class: 'warning', text: 'The vault is locked, so passwords cannot be stored. Unlock it to save credentials; until then you will be asked when connecting.' }));
    out.push(loginPicker());
    const prof = profileInUse();
    if (s.protocol !== 'ssh') {
      out.push(h('p', { class: 'lead', text: s.protocol === 'telnet' ? 'Telnet has no login protocol of its own. The user name and password are sent by automation rules using <USER> and <PASS>.' : 'Serial consoles log in through the device prompt. Automation rules can send the user name and password using <USER> and <PASS>.' }));
      if (vaultOK && !prof) out.push(secretField('Password', flags.password, v => (sec.password = v), reveal('password')));
      return out;
    }
    if (prof) { out.push(...algoFields()); return out; }
    const method = seg([
      { value: 'password', label: 'Password' }, { value: 'publickey', label: 'Key file' },
      { value: 'agent', label: 'SSH agent' }, { value: 'ask', label: 'Ask every time' },
    ], s.auth, v => { s.auth = v; show(current); });
    out.push(h('div', { class: 'field' }, h('span', { text: 'Method' }), method));
    if (s.auth === 'password' && vaultOK) {
      out.push(secretField('Password', flags.password, v => (sec.password = v), reveal('password')),
        h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Also answers keyboard-interactive password prompts, which many network devices use.' }));
    }
    if (s.auth === 'publickey') {
      const key = h('input', { type: 'text', value: s.keyPath, spellcheck: false, placeholder: '~/.ssh/id_ed25519', on: { input: e => (s.keyPath = e.target.value.trim()) } });
      const cert = h('input', { type: 'text', value: s.certPath, spellcheck: false, placeholder: 'Automatic: <key>-cert.pub when present', on: { input: e => (s.certPath = e.target.value.trim()) } });
      const browse = (inp, setter) => h('button', { type: 'button', class: 'btn small', text: 'Browse', on: { click: async () => { const p = await api.ChooseFile('Choose file', 'key'); if (p) { inp.value = p; setter(p); } } } });
      out.push(h('label', { class: 'field mono' }, h('span', { text: 'Private key (OpenSSH or PuTTY .ppk)' }), h('div', { class: 'with-btn' }, key, browse(key, v => (s.keyPath = v)))));
      if (vaultOK) out.push(secretField('Key passphrase', flags.passphrase, v => (sec.passphrase = v), reveal('passphrase')));
      out.push(h('label', { class: 'field mono' }, h('span', { text: 'OpenSSH certificate' }), h('div', { class: 'with-btn' }, cert, browse(cert, v => (s.certPath = v))), h('span', { class: 'hint', text: 'Signed by your SSH CA (ssh-keygen -s). Expired certificates are skipped and reported in the terminal.' })));
    }
    if (s.auth === 'agent') out.push(h('p', { class: 'lead', text: 'Uses keys loaded in the Windows OpenSSH Authentication Agent service, or ssh-agent on macOS and Linux. Keys never leave the agent.' }));
    if (s.auth === 'ask') out.push(h('p', { class: 'lead', text: 'Nothing is stored; you are asked for the password on every connection.' }));
    out.push(...algoFields());
    return out;
  }

  function algoFields() {
    const g = inherited(n.parentId, 'algorithms');
    const algo = h('select', { on: { change: e => { s.algorithms = e.target.value; algoHint.textContent = algoText[s.algorithms === 'inherit' ? (g.value || 'secure') : s.algorithms]; } } },
      h('option', { value: 'inherit', text: g.value ? `From group: ${ALGO_LABELS[g.value].replace(' (recommended)', '')}` : 'From group (group sets none: Secure)' }),
      ...Object.entries(ALGO_LABELS).map(([v, t]) => h('option', { value: v, text: t })));
    algo.value = s.algorithms || 'secure';
    const algoText = {
      secure: 'Only algorithms without known weaknesses, starting with post-quantum mlkem768x25519. Newer IOS-XE, NX-OS, Junos, EOS and OpenSSH all support this.',
      compatible: 'Adds SHA-1 based key exchange and ssh-rsa host keys, still common on IOS 15 and older appliances.',
      legacy: 'Also allows CBC and 3DES ciphers and diffie-hellman-group1. Use only for devices that cannot be upgraded.',
    };
    const algoHint = h('span', { class: 'hint', text: algoText[s.algorithms === 'inherit' ? (g.value || 'secure') : s.algorithms] || '' });
    const fwd = h('input', { type: 'checkbox', checked: s.agentForward, on: { change: e => (s.agentForward = e.target.checked) } });
    return [h('h3', { text: 'Security', style: { marginTop: '18px' } }), field('Algorithms', algo, algoHint),
      check('Forward SSH agent', fwd, 'Lets this server use your agent keys for onward hops. Enable only on servers you trust.')];
  }

  function jumps() {
    const out = [h('h3', { text: 'Jump hosts' }), h('p', { class: 'lead', text: 'Connect through a bastion host, like ssh -J. The easiest way is to pick a saved SSH session: its address, login and its own jump host are used, so changing the bastion once changes it for everyone.' })];
    if (s.protocol === 'telnet') out.push(h('p', { class: 'hint', style: { color: 'var(--muted)', marginTop: '-6px' }, text: 'Telnet goes through the SSH jump host as a forwarded connection: encrypted up to the jump host, clear text from there to the device. The jump host must allow TCP forwarding.' }));
    const mode = s.jumpVia === 'inherit' ? 'inherit' : s.jumpVia ? 'session' : s.jump.length ? 'custom' : 'direct';
    const g = inherited(n.parentId, 'jumpVia');
    const gNode = g.value ? state.byId.get(g.value) : null;
    const modeSel = h('select', {},
      h('option', { value: 'direct', text: 'Connect directly' }),
      h('option', { value: 'inherit', text: gNode ? `From group: through ${gNode.name}` : 'From group (the group sets none: direct)' }),
      h('option', { value: 'session', text: 'Through a saved session' }),
      h('option', { value: 'custom', text: 'Through hosts I enter here' }));
    modeSel.value = mode;
    modeSel.addEventListener('change', () => {
      const v = modeSel.value;
      if (v === 'direct') { s.jumpVia = ''; s.jump = []; sec.jumpOrigin = []; sec.jump = {}; }
      if (v === 'inherit') { s.jumpVia = 'inherit'; }
      if (v === 'session') { s.jumpVia = sshSessions(n.id)[0]?.id || ''; if (!s.jumpVia) toast('Save the bastion as an SSH session first.', 'error'); }
      if (v === 'custom') { s.jumpVia = ''; if (!s.jump.length) { s.jump.push({ host: '', port: 22, user: s.user, auth: 'agent', keyPath: '', credentialId: '' }); sec.jumpOrigin.push(-1); } }
      show(current);
    });
    out.push(field('Route', modeSel));
    if (mode === 'session') {
      const via = sessionPicker({ value: s.jumpVia, nodes: () => sshSessions(n.id), label: 'Jump host session' });
      via.addEventListener('change', () => (s.jumpVia = via.value));
      out.push(field('Jump host session', via, 'Its own jump host is used too, so chains work.'));
      return out;
    }
    if (mode !== 'custom') return out;
    const table = h('table', { class: 'rules' }, h('tr', {}, h('th', { text: '' }), h('th', { text: 'Host' }), h('th', { text: 'Port' }), h('th', { text: 'User' }), h('th', { text: 'Login' }), h('th', { text: 'Key file or password' }), h('th')));
    s.jump.forEach((j, i) => {
      const jc = j.credentialId ? state.credById.get(j.credentialId) : null;
      const cred = jc
        ? h('span', { class: 'hint', text: `profile ${jc.name}` })
        : j.auth === 'publickey'
          ? h('input', { type: 'text', value: j.keyPath || '', placeholder: '~/.ssh/id_ed25519', on: { input: e => (j.keyPath = e.target.value.trim()) } })
          : j.auth === 'password' && vaultOK
            ? h('input', { type: 'password', placeholder: flags.jump[String(sec.jumpOrigin[i])] ? 'Stored. Type to replace.' : 'Password', on: { input: e => (sec.jump[i] = e.target.value === '' ? undefined : e.target.value) } })
            : h('span', { class: 'hint', text: j.auth === 'agent' ? 'from agent' : 'asked when connecting' });
      const authSel = h('select', { on: { change: e => {
        const v = e.target.value;
        if (v.startsWith('cred:')) { j.credentialId = v.slice(5); } else { j.credentialId = ''; j.auth = v; }
        show(current);
      } } }, ...[['agent', 'SSH agent'], ['publickey', 'Key file'], ['password', 'Password'], ['ask', 'Ask']].map(([v, t]) => h('option', { value: v, text: t })),
        ...state.credentials.map(c => h('option', { value: 'cred:' + c.id, text: `Profile: ${c.name}` })));
      authSel.value = jc ? 'cred:' + jc.id : (j.auth || 'agent');
      table.append(h('tr', {},
        h('td', { class: 'n', text: i + 1 }),
        h('td', {}, h('input', { type: 'text', value: j.host, spellcheck: false, on: { input: e => (j.host = e.target.value.trim()) } })),
        h('td', { style: { width: '80px' } }, h('input', { type: 'number', value: j.port || 22, on: { input: e => (j.port = +e.target.value) } })),
        h('td', {}, h('input', { type: 'text', value: j.user || '', spellcheck: false, on: { input: e => (j.user = e.target.value) } })),
        h('td', { style: { width: '120px' } }, authSel),
        h('td', {}, cred),
        h('td', { style: { width: '24px' } }, h('button', { type: 'button', class: 'del', title: 'Remove hop', text: '×', on: { click: () => { s.jump.splice(i, 1); sec.jumpOrigin.splice(i, 1); const nj = {}; Object.entries(sec.jump).forEach(([k, v]) => { if (+k < i) nj[k] = v; else if (+k > i) nj[+k - 1] = v; }); sec.jump = nj; show(current); } } }))));
    });
    out.push(table, h('button', { type: 'button', class: 'btn small', text: 'Add jump host', on: { click: () => { s.jump.push({ host: '', port: 22, user: s.user, auth: 'agent', keyPath: '', credentialId: '' }); sec.jumpOrigin.push(-1); show(current); } } }));
    return out;
  }

  function tunnels() {
    if (s.protocol === 'telnet') {
      return [h('h3', { text: 'Outbound proxy' }), proxyFields(() => s.proxy, v => (s.proxy = v), n.parentId)];
    }
    const out = [h('h3', { text: 'Tunnels' }),
      h('p', { class: 'lead', text: 'Port forwards through this SSH connection, like ssh -L, -R and -D. Ticked tunnels start when the session connects and stop with it; the others can be started from Tunnels in the status bar. Network devices often refuse forwarding: define tunnels on the jump host’s session instead.' })];
    const table = h('table', { class: 'rules tunnels' }, h('tr', {}, h('th', { text: 'Start' }), h('th', { text: 'Type' }), h('th', { text: 'Listen port' }), h('th', { text: 'Destination host' }), h('th', { text: 'Port' }), h('th', { text: 'Shared', title: 'Other computers may connect' }), h('th', { text: 'Name' }), h('th')));
    s.tunnels.forEach((t, i) => {
      const type = h('select', { on: { change: e => { t.type = e.target.value; show(current); } } },
        h('option', { value: 'local', text: 'Local -L' }), h('option', { value: 'remote', text: 'Remote -R' }), h('option', { value: 'dynamic', text: 'Proxy -D' }));
      type.value = t.type;
      const dyn = t.type === 'dynamic';
      const open = h('input', { type: 'checkbox', checked: !!t.bind && t.bind !== '127.0.0.1' && t.bind !== 'localhost',
        title: t.type === 'remote' ? 'Listen on all addresses of the SSH server (it needs GatewayPorts); otherwise only on its loopback' : 'Let other computers use this tunnel; otherwise only this computer (127.0.0.1)',
        on: { change: e => (t.bind = e.target.checked ? '0.0.0.0' : '') } });
      table.append(h('tr', {},
        h('td', { style: { width: '44px' } }, h('input', { type: 'checkbox', checked: !t.off, title: 'Start when the session connects', on: { change: e => (t.off = !e.target.checked) } })),
        h('td', { style: { width: '128px' } }, type),
        h('td', { style: { width: '78px' } }, h('input', { type: 'number', min: 0, max: 65535, value: t.port || '', placeholder: t.type === 'remote' ? 'any' : '', on: { input: e => (t.port = +e.target.value || 0) } })),
        h('td', {}, dyn ? h('span', { class: 'hint', text: 'any (the application chooses)' }) : h('input', { type: 'text', value: t.host || '', spellcheck: false, placeholder: t.type === 'remote' ? '127.0.0.1 (this computer)' : '10.0.0.1 (seen from the server)', on: { input: e => (t.host = e.target.value.trim()) } })),
        h('td', { style: { width: '72px' } }, dyn ? null : h('input', { type: 'number', min: 1, max: 65535, value: t.hostPort || '', on: { input: e => (t.hostPort = +e.target.value || 0) } })),
        h('td', { style: { width: '54px', textAlign: 'center' } }, open),
        h('td', { style: { width: '96px' } }, h('input', { type: 'text', value: t.name || '', placeholder: 'optional', on: { input: e => (t.name = e.target.value) } })),
        h('td', { style: { width: '24px' } }, h('button', { type: 'button', class: 'del', title: 'Remove tunnel', text: '×', on: { click: () => { s.tunnels.splice(i, 1); show(current); } } }))));
    });
    const add = (type, extra = {}) => { s.tunnels.push({ id: uuid(), type, port: 0, host: '', hostPort: 0, bind: '', name: '', off: false, ...extra }); show(current); };
    out.push(table, h('div', { style: { display: 'flex', gap: '8px', flexWrap: 'wrap', marginBottom: '6px' } },
      h('button', { type: 'button', class: 'btn small', text: 'Add local tunnel', on: { click: () => add('local') } }),
      h('button', { type: 'button', class: 'btn small', text: 'Add remote tunnel', on: { click: () => add('remote', { host: '127.0.0.1' }) } }),
      h('button', { type: 'button', class: 'btn small', text: 'Add SOCKS/HTTP proxy', on: { click: () => add('dynamic', { port: 1080 }) } })),
      h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Examples: Local 8443 → 10.1.1.1:443 opens the device’s web interface at https://127.0.0.1:8443. A SOCKS/HTTP proxy on 1080 lets a browser reach everything behind the server. Remote 8080 → 127.0.0.1:8080 lets the server reach a web server on this computer.' }),
      h('h3', { text: 'Outbound proxy', style: { marginTop: '18px' } }),
      proxyFields(() => s.proxy, v => (s.proxy = v), n.parentId));
    return out;
  }

  function automation() {
    const out = [h('h3', { text: 'Automation' }),
      h('p', { class: 'lead' }, 'After connecting, each active rule waits for its pattern (a regular expression) and then sends its text; a rule with nothing to wait for sends at once. Use ', h('kbd', { text: '<USER>' }), ' ', h('kbd', { text: '<PASS>' }), ' ', h('kbd', { text: '<HOST>' }), ' ', h('kbd', { text: '<PORT>' }), ' ', h('kbd', { text: '<NAME>' }), ' in the text. Hidden texts are kept in the vault.'),
      check('Press Enter once right after connecting', h('input', { type: 'checkbox', checked: !!s.initialReturn, on: { change: e => (s.initialReturn = e.target.checked) } }), 'Wakes up consoles and terminal servers that show nothing until a key is pressed (SecureCRT: Send initial carriage return).')];
    const table = h('table', { class: 'rules' }, h('tr', {}, h('th', {}), h('th', { text: 'On' }), h('th', { text: 'Wait for' }), h('th', { text: 'Then send' }), h('th', { text: 'Hidden' }), h('th', { text: 'Enter' }), h('th', { text: 'Timeout' }), h('th', { text: 'If not seen' }), h('th')));
    s.expect.forEach((r, i) => {
      const stored = flags.expect[r.id];
      const send = h('input', { type: r.hidden ? 'password' : 'text', value: r.hidden ? '' : r.send || '', spellcheck: false, placeholder: r.hidden && stored ? 'stored' : '', on: { input: e => { if (r.hidden) sec.expect[r.id] = e.target.value === '' ? undefined : e.target.value; else r.send = e.target.value; } } });
      const onFail = h('select', { on: { change: e => (r.onFail = e.target.value) } }, h('option', { value: 'stop', text: 'stop' }), h('option', { value: 'continue', text: 'skip rule' }));
      onFail.value = r.onFail || 'stop';
      table.append(h('tr', {},
        h('td', { class: 'n', text: i + 1 }),
        h('td', {}, h('input', { type: 'checkbox', checked: r.active, on: { change: e => (r.active = e.target.checked) } })),
        h('td', {}, h('input', { type: 'text', value: r.expect, spellcheck: false, placeholder: i === 0 ? '[Pp]assword:' : 'empty = send at once', on: { input: e => (r.expect = e.target.value) } })),
        h('td', {}, send),
        h('td', {}, h('input', { type: 'checkbox', checked: r.hidden, disabled: !vaultOK, title: vaultOK ? '' : 'Unlock the vault to hide values', on: { change: e => { r.hidden = e.target.checked; if (r.hidden && r.send) { sec.expect[r.id] = r.send; } r.send = ''; show(current); } } })),
        h('td', {}, h('input', { type: 'checkbox', checked: r.return, on: { change: e => (r.return = e.target.checked) } })),
        h('td', { style: { width: '70px' } }, h('input', { type: 'number', min: 0, value: r.timeout || '', placeholder: String(s.expectTimeout || 20), title: 'Seconds; empty uses the default below', on: { input: e => (r.timeout = +e.target.value || 0) } })),
        h('td', { style: { width: '100px' } }, onFail),
        h('td', { style: { whiteSpace: 'nowrap' } },
          h('button', { type: 'button', class: 'mv', title: 'Move up', text: '↑', disabled: i === 0, on: { click: () => { s.expect.splice(i - 1, 0, s.expect.splice(i, 1)[0]); show(current); } } }),
          h('button', { type: 'button', class: 'del', title: 'Remove rule', text: '×', on: { click: () => { s.expect.splice(i, 1); show(current); } } }))));
    });
    const et = h('input', { type: 'number', min: 1, max: 3600, value: s.expectTimeout || 20, on: { input: e => (s.expectTimeout = +e.target.value) } });
    out.push(table,
      h('div', { style: { display: 'flex', gap: '8px', marginBottom: '14px', flexWrap: 'wrap' } },
        h('button', { type: 'button', class: 'btn small', text: 'Add rule', on: { click: () => { s.expect.push({ id: uuid(), active: true, expect: '', send: '', hidden: false, return: true, timeout: 0, onFail: 'stop' }); show(current); } } }),
        h('button', { type: 'button', class: 'btn small', text: 'Add Cisco enable login', title: 'Waits for > , sends enable, then the stored password', on: { click: () => {
          s.expect.push({ id: uuid(), active: true, expect: '>\\s*$', send: 'enable', hidden: false, return: true, timeout: 0, onFail: 'stop' },
            { id: uuid(), active: true, expect: '[Pp]assword:\\s*$', send: '<PASS>', hidden: false, return: true, timeout: 0, onFail: 'stop' },
            { id: uuid(), active: true, expect: '#\\s*$', send: 'terminal length 0', hidden: false, return: true, timeout: 0, onFail: 'continue' });
          show(current);
        } } })),
      h('div', { style: { maxWidth: '260px' } }, field('Default timeout per rule (seconds)', et)));

    const cmdList = (title, lead, list) => {
      const tbl = h('table', { class: 'rules' }, h('tr', {}, h('th', { text: 'Command' }), h('th', { text: 'Ask first' }), h('th')));
      list.forEach((c, i) => tbl.append(h('tr', {},
        h('td', {}, h('input', { type: 'text', value: c.command, spellcheck: false, on: { input: e => (c.command = e.target.value) } })),
        h('td', { style: { width: '80px' } }, h('input', { type: 'checkbox', checked: c.confirm, on: { change: e => (c.confirm = e.target.checked) } })),
        h('td', { style: { width: '24px' } }, h('button', { type: 'button', class: 'del', text: '×', title: 'Remove', on: { click: () => { list.splice(i, 1); show(current); } } })))));
      return [h('h3', { text: title, style: { marginTop: '18px' } }), h('p', { class: 'lead', text: lead }), tbl,
        h('button', { type: 'button', class: 'btn small', text: 'Add command', on: { click: () => { list.push({ command: '', confirm: true }); show(current); } } })];
    };
    out.push(...cmdList('Before connecting', 'Runs on this computer. YACM_HOST, YACM_PORT, YACM_USER and YACM_NAME are set.', s.before),
      ...cmdList('After disconnecting', 'Runs on this computer when the session ends.', s.after));
    return out;
  }

  function macros() {
    const out = [h('h3', { text: 'Saved commands' }),
      h('p', { class: 'lead', text: `Commands for this device that are sent only when you pick them: Commands (${KEY.palette}) lists them first, under “This session”. Multi-line text is sent line by line. Commands that run by themselves, on login or when the device prints something, are under Automation.` })];
    const tbl = h('table', { class: 'rules' }, h('tr', {}, h('th', { text: 'Name' }), h('th', { text: 'Text' }), h('th', { text: 'Enter' }), h('th')));
    s.macros.forEach((m, i) => {
      const txt = h('textarea', { rows: Math.min(6, Math.max(1, (m.text || '').split('\n').length)), spellcheck: false, style: { width: '100%', fontFamily: 'var(--mono)', fontSize: '12.5px', background: 'var(--bg-input)', border: '1px solid var(--line)', borderRadius: '4px', padding: '4px 7px' }, on: { input: e => (m.text = e.target.value) } });
      txt.value = m.text || '';
      tbl.append(h('tr', {},
        h('td', { style: { width: '160px' } }, h('input', { type: 'text', value: m.name || '', on: { input: e => (m.name = e.target.value) } })),
        h('td', {}, txt),
        h('td', { style: { width: '50px' } }, h('input', { type: 'checkbox', checked: m.return, title: 'Press Enter after the text', on: { change: e => (m.return = e.target.checked) } })),
        h('td', { style: { width: '24px' } }, h('button', { type: 'button', class: 'del', text: '×', title: 'Remove', on: { click: () => { s.macros.splice(i, 1); show(current); } } }))));
    });
    out.push(tbl, h('button', { type: 'button', class: 'btn small', text: 'Add command', on: { click: () => { s.macros.push({ name: '', text: '', return: true }); show(current); } } }));
    return out;
  }

  function terminal() {
    const log = h('input', { type: 'checkbox', checked: s.logToFile, on: { change: e => (s.logToFile = e.target.checked) } });
    const dir = h('input', { type: 'text', value: s.logDir, spellcheck: false, placeholder: 'Default log folder from Settings', on: { input: e => (s.logDir = e.target.value.trim()) } });
    const fs = h('input', { type: 'number', min: 0, max: 40, value: s.fontSize || '', placeholder: String(state.settings.fontSize), on: { input: e => { s.fontSize = +e.target.value || 0; tPrev.update(); } } });
    const sb = h('input', { type: 'number', min: 0, step: 1000, value: s.scrollback || '', placeholder: String(state.settings.scrollback), on: { input: e => (s.scrollback = +e.target.value || 0) } });
    const stamp = h('input', { type: 'checkbox', checked: s.logTimestamps, on: { change: e => (s.logTimestamps = e.target.checked) } });
    const pd = h('input', { type: 'number', min: 0, max: 10000, step: 50, value: s.pasteDelay > 0 ? s.pasteDelay : s.pasteDelay < 0 ? 0 : '', placeholder: `App default (${state.settings.pasteDelay || 0} ms)`,
      on: { input: e => { const v = e.target.value; s.pasteDelay = v === '' ? 0 : +v === 0 ? -1 : Math.max(0, +v); } } });
    const hl = h('input', { type: 'checkbox', checked: !s.noHighlight, on: { change: e => (s.noHighlight = !e.target.checked) } });
    const al = h('input', { type: 'checkbox', checked: !s.noAlerts, on: { change: e => (s.noAlerts = !e.target.checked) } });
    const keep = h('input', { type: 'checkbox', checked: !!s.keepName, on: { change: e => (s.keepName = e.target.checked) } });
    const color = colorPicker(n.color, v => (n.color = v), { colors: COLORS, labels: COLOR_LABELS });
    const inh = key => `Default (${sourceText(termResolve({ isGroup: false, session: {} }, key, n.parentId))})`;
    const tText = termPicker(s.termText, v => { s.termText = v; tPrev.update(); }, { kind: 'text', inheritText: inh('termText') });
    const tBg = termPicker(s.termBackground, v => { s.termBackground = v; tPrev.update(); }, { kind: 'bg', inheritText: inh('termBackground') });
    // The preview shows this session's font size (or the default) in the
    // weight from Settings, like the terminal will.
    const tPrev = termPreview(() => ({ ...colorsFor({ ...n, session: s }), size: s.fontSize || state.settings.fontSize || 14, weight: { light: 300, medium: 500 }[state.settings.fontWeight] || 400 }));
    return [h('h3', { text: 'Session log' }),
      check('Save everything this session prints to a text file', log, 'A new file per connection, named after the session and the time (or the file name template in Settings > Session logs). Escape codes are removed. Log files are plain, unencrypted text; passwords and keys in configurations are written as ***** unless that is turned off in Settings > Session logs.'),
      check('Start every line with the date and time', stamp, 'Like [2026-10-22 13:35:01] show ip interface brief. Only in the log file, not on screen.'),
      h('label', { class: 'field mono' }, h('span', { text: 'Log folder' }), h('div', { class: 'with-btn' }, dir, h('button', { type: 'button', class: 'btn small', text: 'Browse', on: { click: async () => { const p = await api.ChooseDirectory('Log folder'); if (p) { dir.value = p; s.logDir = p; } } } }))),
      state.settings.logNameTemplateOn && state.settings.logNameOwn
        ? field('Log file name template', h('input', { type: 'text', value: s.logName || '', spellcheck: false, class: 'mono-input', placeholder: 'Empty: the group’s or the one in Settings', on: { input: e => (s.logName = e.target.value) } }), 'Tokens: {name} {host} {protocol} {user} {group} {path} {date} {time} {stamp} {ticket}; {stamp} is 20261113-103743; “/” starts a subfolder.')
        : null,
      h('h3', { text: 'Terminal', style: { marginTop: '18px' } }),
      h('div', { class: 'field' }, h('span', { text: 'Tab colour' }), color, h('span', { class: 'hint', text: 'For example red for production, so a reload never goes to the wrong device. “From group” uses the group’s colour.' })),
      h('div', { class: 'grid2' }, field('Font size (empty = default)', fs), field('Scrollback lines (empty = default)', sb)),
      h('div', { class: 'field' }, h('span', { text: 'Text colour' }), tText),
      h('div', { class: 'field' }, h('span', { text: 'Background colour' }), tBg),
      tPrev.el,
      h('p', { class: 'hint', style: { color: 'var(--faint)', margin: '-4px 0 10px' }, text: 'For example a dark red background for production. Colours the device sends and keyword highlighting stay as they are.' }),
      field('Pause between pasted lines, in ms (0 = none)', pd, 'For slow serial consoles and old devices that lose characters when a long configuration is pasted.'),
      check('Highlight keywords (down, errors, IP addresses…)', hl, 'The colours are set in Settings.'),
      check('Alerts from this session', al, 'Neighbours going down, err-disabled ports and the like mark the tab when you are not looking at it (Settings > Highlighting).'),
      check('Keep this session name', keep, 'Never offer to rename it after the hostname the device shows in its prompt.')];
  }

  // ---- dialog ----
  const body = h('div', { class: 'editor' }, nav, pane);
  refreshNav();
  show(Math.max(0, sections.findIndex(x => x[0] === startSection)));
  openDialog({
    title: isNew ? 'New session' : `Edit ${n.name}`,
    wide: true,
    body,
    buttons: [
      { label: 'Cancel' },
      { label: isNew ? 'Create session' : 'Save session', kind: 'primary', default: true, onClick: async () => {
        const out = structuredClone(n);
        const o = out.session;
        if (o.protocol !== 'serial') delete o.serial; else { o.host = ''; o.port = 0; }
        if (o.protocol !== 'ssh') o.auth = '';
        if (o.protocol === 'serial') { o.jump = []; o.jumpVia = ''; }
        if (o.jumpVia) { o.jump = []; }
        if (o.algorithms === '') o.algorithms = 'secure';
        o.expect = o.expect.filter(r => r.expect.trim() || r.send || sec.expect[r.id] || (r.hidden && flags.expect[r.id]));
        o.before = o.before.filter(c => c.command.trim());
        o.after = o.after.filter(c => c.command.trim());
        o.macros = o.macros.filter(m => m.text.trim());
        if (o.protocol !== 'ssh') { o.tunnels = []; o.proxy = null; }
        o.tunnels = (o.tunnels || []).filter(t => t.port || t.host || t.hostPort);
        if (!o.tunnels.length) delete o.tunnels;
        if (!o.proxy) delete o.proxy;
        const secrets = {
          password: sec.password ?? null,
          passphrase: sec.passphrase ?? null,
          jump: Object.fromEntries(Object.entries(sec.jump).filter(([, v]) => v !== undefined)),
          expect: Object.fromEntries(Object.entries(sec.expect).filter(([, v]) => v !== undefined)),
          jumpOrigin: o.protocol === 'ssh' && !o.jumpVia ? sec.jumpOrigin : [],
        };
        try {
          const saved = await api.SaveNode(out, secrets);
          await reload();
          state.selected = saved.id;
          window.dispatchEvent(new Event('yacm:tree'));
        } catch (e) {
          // Point the user to the section that needs fixing.
          const m = errMsg(e);
          if (/tunnel|proxy/i.test(m)) show(sections.findIndex(x => x[0] === 'Tunnels and proxy'));
          else if (/host|device|name|port/i.test(m)) show(0);
          throw e;
        }
      } },
    ],
  });
}
