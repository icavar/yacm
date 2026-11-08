// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Command library: saved commands with {placeholders}, sent to the active
// session or to every session in "Type to many".
import { api, state, saveSettings } from './api.js';
import { h, openDialog, field, check, toast, errMsg, plural, uuid, KEY } from './ui.js';
import { commandTargets, pasteDelayFor } from './terms.js';
import { editSession } from './editor.js';

const PLACEHOLDER = /\{([A-Za-z][\w.-]{0,30})\}/g;
const lastValues = new Map(); // placeholder -> last value, for this run of the app

export function placeholders(text) {
  return [...new Set([...(text || '').matchAll(PLACEHOLDER)].map(m => m[1]))];
}

function fill(text, values) {
  return text.replace(PLACEHOLDER, (m, name) => (name in values ? values[name] : m));
}

const sleep = ms => new Promise(r => setTimeout(r, ms));

// EXAMPLES show how the library works when it is still empty.
const EXAMPLES = [
  { folder: 'Examples', name: 'Interfaces', text: 'show ip interface brief', return: true },
  { folder: 'Examples', name: 'One interface', text: 'show running-config interface {interface}\nshow interfaces {interface}', return: true },
  { folder: 'Examples', name: 'Search the log', text: 'show logging | include {text}', return: true },
  { folder: 'Examples', name: 'Route to', text: 'show ip route {address}', return: true },
  { folder: 'Examples', name: 'Back up the configuration', text: 'copy running-config tftp://{server}/<NAME>.cfg', return: true },
];

async function addExamples() {
  const items = [...(state.settings.snippets || []), ...EXAMPLES.map(e => ({ ...e, id: uuid() }))];
  await saveSettings({ snippets: items });
  toast(`Added ${plural(EXAMPLES.length, 'example command', 'example commands')} to the library.`);
}

async function send(item, targets) {
  const names = placeholders(item.text);
  let text = item.text;
  if (names.length) {
    const values = await askValues(item, names);
    if (!values) return;
    text = fill(text, values);
  }
  const ids = targets.ids;
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  const delay = Math.max(...ids.map(pasteDelayFor));
  try {
    if (lines.length > 1 && delay > 0) {
      for (const [i, line] of lines.entries()) {
        const last = i === lines.length - 1;
        await api.SendText(ids, line, last ? item.return : true, !!item.fromTeam);
        if (!last) await sleep(delay);
      }
    } else {
      await api.SendText(ids, text, item.return, !!item.fromTeam);
    }
  } catch (e) { toast(errMsg(e), 'error'); }
}

function askValues(item, names) {
  return new Promise(resolve => {
    const inputs = names.map(n => h('input', { type: 'text', spellcheck: false, value: lastValues.get(n) || '', placeholder: n }));
    const preview = h('pre', { class: 'hostkey', style: { whiteSpace: 'pre-wrap' } });
    const update = () => {
      const v = Object.fromEntries(names.map((n, i) => [n, inputs[i].value]));
      preview.textContent = fill(item.text, Object.fromEntries(Object.entries(v).map(([k, x]) => [k, x || `{${k}}`])));
    };
    inputs.forEach(i => i.addEventListener('input', update));
    update();
    let result = null;
    openDialog({
      title: item.name || 'Send command',
      body: h('div', {}, ...names.map((n, i) => field(n, inputs[i])), h('span', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Will send:' }), preview),
      buttons: [{ label: 'Cancel' }, { label: 'Send', kind: 'primary', default: true, onClick: () => {
        result = Object.fromEntries(names.map((n, i) => [n, inputs[i].value]));
        names.forEach((n, i) => lastValues.set(n, inputs[i].value));
      } }],
      onClose: () => resolve(result),
    });
  });
}

// openPalette shows the library and the active session's macros.
export function openPalette() {
  const targets = commandTargets();
  if (!targets.ids.length) { toast('Open and connect a session first.'); return; }
  let items = [];
  const collect = () => (items = [
    // Commands from a team library (its own or a library session's) may use
    // <USER> and <PASS> only as lines of their own (the backend checks).
    ...(targets.macros || []).map(m => ({ name: m.name || m.text.split('\n')[0], text: m.text, return: m.return, folder: 'This session', fromTeam: !!targets.library })),
    ...(state.settings.snippets || []).map(s => ({ ...s })),
    ...(state.teamSnippets || []).map(s => ({ ...s, fromTeam: true })),
  ]);
  collect();
  const search = h('input', { class: 'search', type: 'text', spellcheck: false, placeholder: 'Type to filter, Enter to send' });
  const list = h('ul', { role: 'listbox' });
  let shown = [];
  let cur = 0;
  let dlg = null;
  const render = () => {
    const q = search.value.trim().toLowerCase();
    shown = items.filter(i => !q || `${i.name} ${i.text} ${i.folder || ''}`.toLowerCase().includes(q));
    cur = Math.min(cur, Math.max(0, shown.length - 1));
    list.replaceChildren();
    if (!shown.length) {
      if (items.length) { list.append(h('li', { class: 'empty-note', text: 'Nothing matches.' })); return; }
      list.append(h('li', { class: 'empty-note' },
        h('p', { text: 'No saved commands yet. The library holds commands for every session (Edit library); a session’s own commands are under Edit > Saved commands.' }),
        h('button', { class: 'btn small', text: 'Add examples', on: { click: async () => { try { await addExamples(); collect(); render(); search.focus(); } catch (e) { toast(errMsg(e), 'error'); } } } })));
      return;
    }
    shown.forEach((it, i) => {
      const li = h('li', { role: 'option', class: i === cur ? 'on' : '', 'aria-selected': String(i === cur) },
        h('span', { class: 't', text: it.name || '(no name)' }), h('span', { class: 'f', text: it.folder || '' }),
        h('code', { text: it.text.split('\n').slice(0, 2).join(' ⏎ ') + (it.text.includes('\n') && it.text.split('\n').length > 2 ? ' …' : '') }));
      li.addEventListener('click', () => { cur = i; go(); });
      li.addEventListener('mousemove', () => { if (cur !== i) { cur = i; paint(); } });
      list.append(li);
    });
  };
  const paint = () => list.querySelectorAll('li').forEach((li, i) => { li.classList.toggle('on', i === cur); li.setAttribute('aria-selected', String(i === cur)); });
  const go = () => {
    const it = shown[cur];
    if (!it) return;
    dlg.close();
    send(it, targets);
  };
  search.addEventListener('input', () => { cur = 0; render(); });
  search.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown') { e.preventDefault(); cur = Math.min(shown.length - 1, cur + 1); paint(); list.children[cur]?.scrollIntoView({ block: 'nearest' }); }
    if (e.key === 'ArrowUp') { e.preventDefault(); cur = Math.max(0, cur - 1); paint(); list.children[cur]?.scrollIntoView({ block: 'nearest' }); }
    if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); go(); }
  });
  render();
  dlg = openDialog({
    title: 'Send a command',
    medium: true,
    body: h('div', { class: 'palette' }, search, list,
      h('span', { class: 'target', text: targets.ids.length > 1 ? `Sends to ${plural(targets.ids.length, 'session', 'sessions')} (Type to many is on).` : `Sends to ${targets.label}. {name} parts are asked for first; <HOST>, <USER> and <NAME> are filled in per session.` })),
    left: h('div', { style: { display: 'flex', gap: '8px' } },
      h('button', { class: 'btn', text: 'Edit library…', on: { click: () => { dlg.close(); editLibrary(); } } }),
      targets.nodeId && state.byId.get(targets.nodeId) && !state.byId.get(targets.nodeId).library
        ? h('button', { class: 'btn', text: 'Edit this session’s commands…', on: { click: () => { dlg.close(); editSession(state.byId.get(targets.nodeId), '', 'Saved commands'); } } })
        : null),
    buttons: [{ label: 'Close' }],
    initialFocus: search,
  });
}

// editLibrary edits the command library; add starts a new command with that
// text (Save as command in the terminal menu).
/** @param {(() => void) | null} [after] @param {{ add?: string }} [opts] */
export function editLibrary(after, { add } = {}) {
  const items = structuredClone(state.settings.snippets || []);
  if (add) items.push({ id: uuid(), folder: items[items.length - 1]?.folder || '', name: '', text: add.replace(/\r\n/g, '\n').replace(/\n+$/, ''), return: true });
  const table = h('table', { class: 'rules' });
  const render = () => {
    table.replaceChildren(h('tr', {}, h('th', { text: 'Folder' }), h('th', { text: 'Name' }), h('th', { text: 'Command (one per line; {name} is asked for)' }), h('th', { text: 'Enter' }), h('th')));
    items.forEach((it, i) => {
      const txt = h('textarea', { rows: Math.min(6, Math.max(1, (it.text || '').split('\n').length)), spellcheck: false, style: { width: '100%', fontFamily: 'var(--mono)', fontSize: '12.5px', background: 'var(--bg-input)', border: '1px solid var(--line)', borderRadius: '4px', padding: '4px 7px', resize: 'vertical' }, on: { input: e => (it.text = e.target.value) } });
      txt.value = it.text || '';
      table.append(h('tr', {},
        h('td', { style: { width: '120px' } }, h('input', { type: 'text', value: it.folder || '', list: 'snippet-folders', on: { input: e => (it.folder = e.target.value) } })),
        h('td', { style: { width: '170px' } }, h('input', { type: 'text', value: it.name || '', on: { input: e => (it.name = e.target.value) } })),
        h('td', {}, txt),
        h('td', { style: { width: '50px' } }, h('input', { type: 'checkbox', checked: it.return, on: { change: e => (it.return = e.target.checked) } })),
        h('td', { style: { width: '44px', whiteSpace: 'nowrap' } },
          h('button', { type: 'button', class: 'mv', title: 'Move up', text: '↑', disabled: i === 0, on: { click: () => { items.splice(i - 1, 0, items.splice(i, 1)[0]); render(); } } }),
          h('button', { type: 'button', class: 'del', title: 'Remove', text: '×', on: { click: () => { items.splice(i, 1); render(); } } }))));
    });
  };
  render();
  if (add) requestAnimationFrame(() => table.querySelector('tr:last-child td:nth-child(2) input')?.focus());
  const folders = [...new Set(items.map(i => i.folder).filter(Boolean))];
  openDialog({
    title: 'Command library',
    sub: `Send them from a connected session with ${KEY.palette}, the Commands button or the terminal’s right-click menu.`,
    wide: true,
    body: h('div', { class: 'pane', style: { padding: '14px 20px', overflow: 'auto', flex: '1' } },
      table,
      h('div', { style: { display: 'flex', gap: '8px' } },
        h('button', { type: 'button', class: 'btn small', text: 'Add command', on: { click: () => { items.push({ id: uuid(), folder: items[items.length - 1]?.folder || '', name: '', text: '', return: true }); render(); table.querySelector('tr:last-child input')?.focus(); } } }),
        h('button', { type: 'button', class: 'btn small', text: 'Add examples', title: 'A few show commands with {placeholders} and <NAME>', on: { click: () => { items.push(...EXAMPLES.map(e => ({ ...e, id: uuid() }))); render(); } } })),
      h('datalist', { id: 'snippet-folders' }, ...folders.map(f => h('option', { value: f }))),
      h('p', { class: 'hint', style: { color: 'var(--faint)', marginTop: '14px' } }, 'Example: show running-config interface {interface}. Tokens ', h('kbd', { text: '<HOST>' }), ' ', h('kbd', { text: '<USER>' }), ' ', h('kbd', { text: '<NAME>' }), ' are filled in for each session. Commands for one session only are under Edit > Saved commands.')),
    buttons: [{ label: 'Cancel' }, { label: 'Save library', kind: 'primary', onClick: async () => {
      const clean = items.filter(i => (i.text || '').trim()).map(i => ({ ...i, name: (i.name || '').trim() || i.text.split('\n')[0].slice(0, 40) }));
      await saveSettings({ snippets: clean });
      toast(`Saved ${plural(clean.length, 'command', 'commands')}.`);
      after && after();
    } }],
  });
}
