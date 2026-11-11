// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Credential profiles: one login (user, password or key) shared by many
// sessions. Changing the password here changes it for all of them.
import { api, state, reload } from './api.js';
import { h, openDialog, field, seg, errMsg, toast, confirmDialog, plural } from './ui.js';
import { secretField } from './editor.js';

const METHOD = { password: 'Password', publickey: 'Key file', agent: 'SSH agent', ask: 'Asked when connecting' };

export function openProfiles(onChange) {
  const list = h('div', { class: 'list' });
  const render = () => {
    list.replaceChildren();
    if (!state.credentials.length) {
      list.append(h('div', { class: 'empty-note' },
        h('p', { style: { margin: '0 0 6px' }, text: 'No profiles yet.' }),
        h('p', { style: { margin: 0 }, text: 'Create one for each login you use on many devices, for example your AD account or the local admin account. Sessions then pick the profile instead of storing their own password.' })));
      return;
    }
    for (const c of state.credentials) {
      const used = [c.sessions ? plural(c.sessions, 'session', 'sessions') : '', c.groups ? `the sessions in ${plural(c.groups, 'group', 'groups')}` : ''].filter(Boolean).join(' and ');
      const secret = c.auth === 'password' ? (c.hasPassword ? h('span', { class: 'pill ok', text: 'password stored' }) : h('span', { class: 'pill', text: 'no password yet' }))
        : c.auth === 'publickey' ? h('span', { class: 'pill', text: 'key file' }) : h('span', { class: 'pill', text: METHOD[c.auth] || c.auth });
      list.append(h('div', { class: 'item' },
        h('div', { class: 'main' },
          h('div', { class: 'title', text: c.name }),
          h('div', { class: 'meta', text: [c.user ? `User ${c.user}` : 'No user name', used ? `used by ${used}` : 'not used yet', c.description].filter(Boolean).join(' · ') })),
        secret,
        h('button', { class: 'btn small', text: 'Edit', on: { click: () => editProfile(c, done) } }),
        h('button', { class: 'btn small danger', text: 'Delete', on: { click: () => deleteProfile(c, done) } })));
    }
  };
  const done = async () => { await reload(); render(); window.dispatchEvent(new Event('yacm:tree')); onChange && onChange(); };
  render();
  openDialog({
    title: 'Credential profiles',
    sub: 'Logins shared by many sessions. Change a password once, every session follows.',
    medium: true,
    body: h('div', {}, list,
      h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Use a profile in a session (Edit > Authentication > Login) or give it to a whole group (Edit group > Defaults). Passwords are kept in the encrypted vault.' })),
    left: h('button', { class: 'btn primary', text: 'New profile', on: { click: () => editProfile(null, done) } }),
    buttons: [{ label: 'Close', default: true }],
  });
}

/** @param {any} c @param {(saved?: any) => any} [after] @param {{ name?: string, description?: string }} [preset] for a new profile */
export function editProfile(c, after, preset = {}) {
  const p = c ? structuredClone(c) : { id: '', name: '', description: '', user: '', auth: 'password', keyPath: '', certPath: '', ...preset };
  const sec = { password: undefined, passphrase: undefined };
  const vaultOK = state.vault.unlocked;
  const name = h('input', { type: 'text', value: p.name, spellcheck: false, placeholder: 'AD account' });
  const desc = h('input', { type: 'text', value: p.description || '' });
  const user = h('input', { type: 'text', value: p.user || '', spellcheck: false, placeholder: 'j.smith' });
  const methodBox = h('div');
  const reveal = kind => master => api.RevealSecret('', kind, p.id, master);
  const renderMethod = () => {
    methodBox.replaceChildren();
    if (!vaultOK && (p.auth === 'password' || p.auth === 'publickey')) methodBox.append(h('p', { class: 'warning', text: 'The vault is locked, so the password cannot be stored. Unlock it first.' }));
    if (p.auth === 'password' && vaultOK) {
      methodBox.append(secretField('Password', !!c?.hasPassword, v => (sec.password = v), c ? reveal('cred-password') : null),
        h('p', { class: 'hint', style: { color: 'var(--faint)', marginTop: '-6px' }, text: 'Used for SSH logins (also keyboard-interactive prompts) and by <PASS> in automation rules of telnet and serial sessions.' }));
    }
    if (p.auth === 'publickey') {
      const key = h('input', { type: 'text', value: p.keyPath || '', spellcheck: false, placeholder: '~/.ssh/id_ed25519', on: { input: e => (p.keyPath = e.target.value.trim()) } });
      const cert = h('input', { type: 'text', value: p.certPath || '', spellcheck: false, placeholder: 'Automatic: <key>-cert.pub when present', on: { input: e => (p.certPath = e.target.value.trim()) } });
      const browse = (inp, set) => h('button', { type: 'button', class: 'btn small', text: 'Browse', on: { click: async () => { const f = await api.ChooseFile('Choose file', 'key'); if (f) { inp.value = f; set(f); } } } });
      methodBox.append(h('label', { class: 'field mono' }, h('span', { text: 'Private key (OpenSSH or PuTTY .ppk)' }), h('div', { class: 'with-btn' }, key, browse(key, v => (p.keyPath = v)))));
      if (vaultOK) methodBox.append(secretField('Key passphrase', !!c?.hasPassphrase, v => (sec.passphrase = v), c ? reveal('cred-passphrase') : null));
      methodBox.append(h('label', { class: 'field mono' }, h('span', { text: 'OpenSSH certificate' }), h('div', { class: 'with-btn' }, cert, browse(cert, v => (p.certPath = v)))));
    }
    if (p.auth === 'agent') methodBox.append(h('p', { class: 'lead', style: { color: 'var(--muted)' }, text: 'Keys come from the Windows OpenSSH agent service or ssh-agent.' }));
    if (p.auth === 'ask') methodBox.append(h('p', { class: 'lead', style: { color: 'var(--muted)' }, text: 'Only the user name is stored; the password is asked for when connecting. If you tick “save” at that prompt, it is stored in this profile.' }));
  };
  const method = seg(Object.entries(METHOD).map(([value, label]) => ({ value, label: label === 'Asked when connecting' ? 'Ask' : label })), p.auth, v => { p.auth = v; renderMethod(); });
  renderMethod();
  openDialog({
    title: c ? `Edit profile ${c.name}` : 'New credential profile',
    sub: c && (c.sessions || c.groups) ? `Changes apply at once to ${[c.sessions ? plural(c.sessions, 'session', 'sessions') : '', c.groups ? `the sessions in ${plural(c.groups, 'group', 'groups')}` : ''].filter(Boolean).join(' and ')}.` : null,
    medium: true,
    body: h('div', {},
      h('div', { class: 'grid2' }, field('Name', name), field('User name', user)),
      field('Description', desc),
      h('div', { class: 'field' }, h('span', { text: 'Login method (SSH)' }), method),
      methodBox),
    buttons: [
      { label: 'Cancel' },
      { label: c ? 'Save profile' : 'Create profile', kind: 'primary', default: true, onClick: async () => {
        p.name = name.value.trim(); p.description = desc.value.trim(); p.user = user.value.trim();
        const out = { id: p.id, name: p.name, description: p.description, user: p.user, auth: p.auth, keyPath: p.keyPath, certPath: p.certPath };
        const saved = await api.SaveCredential(out, { password: sec.password ?? null, passphrase: sec.passphrase ?? null });
        toast(c ? `Profile ${p.name} saved.` : `Profile ${p.name} created.`);
        after && (await after(saved));
      } },
    ],
  });
}

async function deleteProfile(c, after) {
  const used = c.sessions + c.groups;
  const ok = await confirmDialog({
    title: `Delete profile “${c.name}”?`,
    message: used
      ? `${plural(c.sessions, 'session uses', 'sessions use')} it directly and ${plural(c.groups, 'group gives', 'groups give')} it to their sessions. Those sessions keep the user name ${c.user || ''} and ask for the password from now on. The stored password is deleted.`
      : 'The profile and its stored password are deleted.',
    ok: 'Delete profile', danger: true,
  });
  if (!ok) return;
  try { await api.DeleteCredential(c.id); await after(); } catch (e) { toast(errMsg(e), 'error'); }
}
