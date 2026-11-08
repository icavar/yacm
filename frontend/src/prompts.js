// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Dialogs the backend asks for while connecting: host keys, passwords,
// keyboard-interactive questions.
import { api, on, state } from './api.js';
import { h, openDialog, check } from './ui.js';
import { activate } from './terms.js';

const open = new Map(); // prompt id -> dialog

export function initPrompts() {
  on('prompt', p => {
    if (p.tabId) activate(p.tabId);
    const dlg = p.kind === 'hostkey' ? hostKey(p) : p.kind === 'secret' ? secret(p) : p.kind === 'teamchange' ? teamChange(p) : questions(p);
    open.set(p.id, dlg);
  });
  on('prompt:closed', id => {
    const d = open.get(id);
    open.delete(id);
    if (d && d.isOpen()) d.close('__backend');
  });
}

function answer(id, ans) {
  open.delete(id);
  api.AnswerPrompt(id, { accept: false, value: '', save: false, answers: [], ...ans });
}

function hostKey(p) {
  const k = p.hostKey;
  const body = h('div', {},
    k.changed ? h('p', { class: 'warning' },
      'The key this server presents is different from the one on record. That happens after a reinstall or an RMA swap, but it is also exactly what an interception attack looks like. Check the fingerprint with whoever manages the device before you continue.') :
      h('p', { text: `This is the first connection to ${k.host}. Compare the fingerprint with the one the device or its administrator shows you before trusting it.` }),
    h('div', { class: `hostkey${k.changed ? ' changed' : ''}` },
      h('div', {}, h('span', { style: { color: 'var(--muted)' }, text: 'Host  ' }), k.host),
      h('div', {}, h('span', { style: { color: 'var(--muted)' }, text: 'Type  ' }), k.keyType),
      h('div', {}, h('span', { style: { color: 'var(--muted)' }, text: 'Key   ' }), h('strong', { text: k.fingerprint }))),
    k.changed && k.knownKeys?.length ? h('div', {},
      h('p', { style: { margin: '0 0 4px', color: 'var(--muted)' }, text: 'Previously trusted:' }),
      h('div', { class: 'hostkey' }, ...k.knownKeys.map((kk, i) => h('div', {}, kk, h('span', { style: { color: 'var(--faint)' }, text: `  (${k.knownSources[i]})` }))))) : null);
  let answered = false;
  return openDialog({
    title: k.changed ? `Host key for ${k.host} has changed` : `Trust ${k.host}?`,
    sub: p.session,
    body,
    buttons: [
      { label: 'Cancel', onClick: () => { answered = true; answer(p.id, { accept: false }); } },
      { label: k.changed ? 'Replace key and connect' : 'Trust and connect', kind: k.changed ? 'danger' : 'primary', default: !k.changed, onClick: () => { answered = true; answer(p.id, { accept: true }); } },
    ],
    onClose: r => { if (!answered && r !== '__backend') answer(p.id, { accept: false }); },
  });
}

function secret(p) {
  const s = p.secret;
  if (s.kind === 'username') return username(p);
  const inp = h('input', { type: 'password', autocomplete: 'off', spellcheck: false });
  const save = h('input', { type: 'checkbox' });
  let answered = false;
  const d = openDialog({
    title: s.title,
    sub: p.session,
    body: h('div', {},
      s.retry ? h('p', { class: 'warning', text: s.kind === 'passphrase' ? 'That passphrase did not unlock the key.' : 'The server rejected that password.' }) : null,
      h('label', { class: 'field' }, h('span', { text: s.message }), inp),
      s.canSave ? check('Save in the vault', save, 'Stored encrypted with your master password.') :
        (state.vault.exists && !state.vault.unlocked ? h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Unlock the vault to save passwords.' }) : null)),
    buttons: [
      { label: 'Cancel', onClick: () => { answered = true; answer(p.id, { accept: false }); } },
      { label: s.kind === 'passphrase' ? 'Unlock key' : 'Log in', kind: 'primary', default: true, onClick: () => { answered = true; answer(p.id, { accept: true, value: inp.value, save: save.checked }); } },
    ],
    onClose: r => { if (!answered && r !== '__backend') answer(p.id, { accept: false }); },
    initialFocus: inp,
  });
  return d;
}

// username: the session has no username (nor its group or credential
// profile); asked before the login, like PuTTY's "login as".
function username(p) {
  const s = p.secret;
  const inp = h('input', { type: 'text', autocomplete: 'off', spellcheck: false, autocapitalize: 'off' });
  const save = h('input', { type: 'checkbox', checked: true });
  let answered = false;
  const ok = () => { if (!inp.value.trim()) { inp.focus(); return false; } answered = true; answer(p.id, { accept: true, value: inp.value.trim(), save: s.canSave && save.checked }); return true; };
  return openDialog({
    title: s.title,
    sub: p.session,
    body: h('div', {},
      h('p', { class: 'hint', text: s.canSave ? 'This session has no username, and neither do its group and credential profile.' : 'No username was given for this connection.' }),
      h('label', { class: 'field' }, h('span', { text: s.message }), inp),
      s.canSave ? check('Save with the session', save, 'Edit > Connection shows it afterwards.') : null),
    buttons: [
      { label: 'Cancel', onClick: () => { answered = true; answer(p.id, { accept: false }); } },
      { label: 'Continue', kind: 'primary', default: true, onClick: ok },
    ],
    onClose: r => { if (!answered && r !== '__backend') answer(p.id, { accept: false }); },
    initialFocus: inp,
  });
}

function questions(p) {
  const inputs = p.questions.map((q, i) => h('input', { type: p.echos[i] ? 'text' : 'password', autocomplete: 'off', spellcheck: false }));
  let answered = false;
  return openDialog({
    title: p.title || 'The server asks',
    sub: p.session,
    body: h('div', {},
      p.instruction ? h('p', { text: p.instruction }) : null,
      ...p.questions.map((q, i) => h('label', { class: 'field' }, h('span', { text: q.trim() }), inputs[i]))),
    buttons: [
      { label: 'Cancel', onClick: () => { answered = true; answer(p.id, { accept: false }); } },
      { label: 'Send', kind: 'primary', default: true, onClick: () => { answered = true; answer(p.id, { accept: true, answers: inputs.map(x => x.value) }); } },
    ],
    onClose: r => { if (!answered && r !== '__backend') answer(p.id, { accept: false }); },
  });
}

// A team library session now points somewhere else, or changed its proxy,
// agent forwarding or automation, since your last connection: whoever can
// edit the library could have done that.
function teamChange(p) {
  const c = p.change;
  const who = [c.updatedBy && `by ${c.updatedBy}`, c.updated && `on ${c.updated}`].filter(Boolean).join(' ');
  let answered = false;
  return openDialog({
    title: `${p.session} has changed since your last connection`,
    sub: `Team library ${c.library}${who ? `, changed ${who}` : ''}`,
    body: h('div', {},
      h('div', { class: 'hostkey changed' },
        h('div', {}, h('span', { style: { color: 'var(--muted)' }, text: 'Last time  ' }), c.before),
        h('div', {}, h('span', { style: { color: 'var(--muted)' }, text: 'Now        ' }), h('strong', { text: c.now }))),
      h('p', { class: 'warning', text: 'If you did not expect this change, do not connect: anyone who can edit the library could point a session at a server that collects your password, or add automation that sends commands to your devices. Check with the library editor first.' })),
    buttons: [
      { label: 'Cancel', default: true, onClick: () => { answered = true; answer(p.id, { accept: false }); } },
      { label: 'Connect anyway', kind: 'danger', onClick: () => { answered = true; answer(p.id, { accept: true }); } },
    ],
    onClose: r => { if (!answered && r !== '__backend') answer(p.id, { accept: false }); },
  });
}
