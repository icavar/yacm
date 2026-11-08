// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Team libraries: Settings > Team libraries, publishing a group, editing
// and copying library sessions.
import { api, state, reload, groupsFlat, on, effectiveLogin } from './api.js';
import { h, openDialog, field, toast, errMsg, confirmDialog, plural } from './ui.js';
import { addSettingsSection, openSettings } from './settings.js';
import { editProfile } from './profiles.js';

let afterChange = () => {};
export function initTeam(onChange) {
  afterChange = onChange;
  on('team:diff', () => showTeamChanges());
  showTeamChanges();
}

// showTeamChanges lists what changed in team libraries since you last saw
// them, so a redirected address does not go unnoticed.
let showingChanges = false;
async function showTeamChanges() {
  if (showingChanges) return;
  const diffs = (await api.TakeTeamChanges().catch(() => [])) || [];
  if (!diffs.length) return;
  showingChanges = true;
  const row = (name, text, cls) => h('div', { class: 'change-row' }, h('span', { class: `pill ${cls}`, text: name }), h('span', { class: 'mono-small', text }));
  const body = h('div', {}, ...diffs.map(d => h('div', { style: { marginBottom: '12px' } },
    h('h3', { text: `${d.library} · revision ${d.revision}` }),
    h('p', { class: 'hint', style: { margin: '0 0 6px' }, text: [d.updatedBy && `Published by ${d.updatedBy}`, d.updated && `on ${d.updated}`].filter(Boolean).join(' ') }),
    ...(d.moved || []).map(c => row(c.name, `${c.before}  →  ${c.now}`, 'warn')),
    ...(d.added || []).map(c => row(c.name, `new: ${c.now}`, 'ok')),
    ...(d.removed || []).map(c => row(c.name, `removed (was ${c.before})`, '')),
  )));
  const moved = diffs.reduce((n, d) => n + (d.moved || []).length, 0);
  openDialog({
    title: diffs.length === 1 ? `Team library ${diffs[0].library} was updated` : `${diffs.length} team libraries were updated`,
    sub: moved ? `${plural(moved, 'session connects', 'sessions connect')} elsewhere now or changed its proxy, agent forwarding or automation. YACM asks before your next login to ${moved === 1 ? 'it' : 'them'}.` : null,
    body,
    medium: true,
    buttons: [{ label: 'OK', kind: 'primary', default: true }],
    onClose: () => { showingChanges = false; },
  });
}

const changed = async () => { await reload(); afterChange(); };
const day = t => (t && !String(t).startsWith('0001') ? new Date(t).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '');

// ---- Settings > Team libraries ----

function teamSection(ctx) {
  const box = h('div');
  const render = async () => {
    let libs = [];
    try { libs = (await api.TeamLibraries()) || []; } catch (e) { box.replaceChildren(h('p', { class: 'error-line', text: errMsg(e) })); return; }
    const list = h('div', { class: 'team-list' });
    if (!libs.length) {
      list.append(h('div', { class: 'empty-note' },
        h('p', { style: { margin: '0 0 6px' }, text: 'You follow no team library yet.' }),
        h('p', { style: { margin: 0 }, text: 'A team library is one file on a share, in a synced folder or in git. Everyone who adds it sees the same sessions, read-only, and logs in with their own profiles. Add one a colleague published, or publish one of your groups.' })));
    }
    for (const l of libs) list.append(libraryCard(l, render));
    box.replaceChildren(list,
      h('div', { class: 'data-actions', style: { marginTop: '10px' } },
        h('button', { type: 'button', class: 'btn small', text: 'Add a library…', on: { click: async () => {
          const p = await api.ChooseFile('Choose the team library file', 'team');
          if (!p) return;
          try {
            const l = await api.AddTeamLibrary(p);
            const unlinked = l.logins.filter(x => !x.linked).length;
            toast(`Added ${l.name}: ${plural(l.sessions, 'session', 'sessions')}.${unlinked ? ` Link ${plural(unlinked, 'login', 'logins')} to your profiles below.` : ''}`);
            await changed(); render();
          } catch (e) { toast(errMsg(e), 'error', 8000); }
        } } }),
        h('button', { type: 'button', class: 'btn small', text: 'Publish one of my groups…', on: { click: () => publishDialog(null, render) } }),
        libs.length ? h('button', { type: 'button', class: 'btn small', text: 'Reload all', on: { click: async () => { await api.ReloadTeamLibraries(); await changed(); render(); } } }) : null));
  };
  render();
  return [h('h3', { text: 'Team libraries' }),
    ctx.hint('Shared, read-only sessions for the whole team. Passwords never go into a library: each login in it is a name such as “AD account” that you link to one of your own credential profiles.'),
    box];
}

function libraryCard(l, rerender) {
  const meta = [plural(l.sessions, 'session', 'sessions'), l.snippets ? plural(l.snippets, 'command', 'commands') : '', l.revision ? `revision ${l.revision}` : '', l.updatedBy ? `published by ${l.updatedBy}${day(l.updated) ? ' on ' + day(l.updated) : ''}` : ''].filter(Boolean).join(' · ');
  const pills = [];
  if (l.stale) pills.push(h('span', { class: 'pill warn', text: 'offline: showing the last copy' }));
  if (l.editing) pills.push(h('span', { class: `pill ${l.newer ? 'warn' : 'ok'}`, text: l.newer ? 'you edit it · newer version published' : 'you edit it' }));
  if (l.needsNewer) pills.push(h('span', { class: 'pill warn', title: `Update YACM to edit it: ${l.needsNewer}.`, text: 'needs a newer YACM to edit' }));
  const logins = h('div', { class: 'team-logins' });
  if (l.logins.length) {
    logins.append(h('div', { class: 'hint', style: { color: 'var(--muted)', margin: '8px 0 4px' }, text: 'Logins in this library, and which of your profiles each one uses:' }));
    for (const lg of l.logins) {
      const sel = h('select', {}, h('option', { value: '', text: 'Not linked: ask when connecting' }),
        ...state.credentials.map(c => h('option', { value: c.id, text: `${c.name}${c.user ? ` (${c.user})` : ''}` })),
        h('option', { value: '__new', text: 'New profile with my account…' }));
      sel.value = lg.linked && state.credById.has(lg.linked) ? lg.linked : '';
      const link = async id => {
        try { await api.LinkTeamLogin(l.id, lg.id, id); await changed(); toast(id ? `${lg.name} now uses ${state.credById.get(id)?.name}.` : `${lg.name} is no longer linked.`); } catch (e) { toast(errMsg(e), 'error'); }
      };
      sel.addEventListener('change', () => {
        if (sel.value !== '__new') { link(sel.value); return; }
        // A profile of one's own for this login (your AD user and password),
        // linked as soon as it is saved.
        sel.value = lg.linked || '';
        editProfile(null, async saved => { await reload(); if (saved?.id) await link(saved.id); rerender && rerender(); }, { name: lg.name, description: `My login for “${lg.name}” in ${l.name}` });
      });
      if (!lg.linked) sel.classList.add('unlinked');
      logins.append(h('div', { class: 'team-login' }, h('div', {}, h('b', { text: lg.name }), lg.description ? h('div', { class: 'hint', text: lg.description }) : null), sel));
    }
  }
  const actions = h('div', { class: 'data-actions', style: { marginTop: '8px' } });
  if (l.editing) {
    actions.append(
      h('button', { type: 'button', class: 'btn small primary', text: 'Publish changes', on: { click: () => publishDialog(l.sourceGroup, rerender) } }),
      h('button', { type: 'button', class: 'btn small', text: 'Stop editing…', on: { click: () => stopEditing(l.id, rerender) } }));
  } else {
    actions.append(h('button', { type: 'button', class: 'btn small', text: 'Edit…', disabled: l.stale || !l.writable || !!l.needsNewer, title: l.needsNewer ? `Update YACM to edit this library: ${l.needsNewer}. You can still use its sessions.` : l.writable ? 'Make your own copy to change it, then publish' : 'You cannot write to the folder of this file', on: { click: () => editLibrary(l.id, rerender) } }));
  }
  actions.append(h('button', { type: 'button', class: 'btn small danger', text: 'Remove', on: { click: async () => {
    const ok = await confirmDialog({ title: `Stop following “${l.name}”?`, message: `Its sessions disappear from your list. The file itself is not touched${l.editing ? '; your editing copy stays in your sessions' : ''}.`, ok: 'Remove', danger: true });
    if (!ok) return;
    try { await api.RemoveTeamLibrary(l.id); await changed(); rerender(); } catch (e) { toast(errMsg(e), 'error'); }
  } } }));
  return h('div', { class: 'team-card' },
    h('div', { class: 'team-head' }, h('b', { text: l.name }), ...pills),
    h('div', { class: 'meta', text: meta }),
    h('code', { class: 'team-path', text: l.path }),
    l.error ? h('p', { class: 'warning', text: l.stale ? `The file cannot be read right now (${l.error}). Changes appear once it can be reached again.` : l.error }) : null,
    l.description ? h('p', { class: 'hint', text: l.description }) : null,
    logins, actions);
}

addSettingsSection({ id: 'team', label: 'Team libraries', render: teamSection });

export function openTeamSettings() { openSettings(afterChange, 'team'); }

// ---- editing ----

export async function editLibrary(id, after) {
  const lib = state.teamLibs.find(l => l.id === id);
  const ok = await confirmDialog({
    title: `Edit “${lib?.name || 'the library'}”?`,
    message: 'YACM makes a copy of the library in your own sessions. Change it like any other group, then use Publish changes (right-click the group). While you edit, you see your copy instead of the library. Logins that are not linked yet get a new profile of yours, so you can fill in your password.',
    ok: 'Make my copy',
  });
  if (!ok) return;
  try {
    const gid = await api.EditTeamLibrary(id);
    await changed();
    state.selected = gid; state.selection = new Set([gid]);
    afterChange();
    toast('Your copy is ready. Right-click it and choose Publish changes when you are done.');
    after && after();
  } catch (e) { toast(errMsg(e), 'error', 8000); }
}

export async function stopEditing(id, after) {
  const lib = state.teamLibs.find(l => l.id === id);
  let del = false;
  const box = h('input', { type: 'checkbox', checked: true, on: { change: e => (del = e.target.checked) } });
  del = true;
  const ok = await new Promise(resolve => openDialog({
    title: `Stop editing “${lib?.name || 'the library'}”?`,
    body: h('div', {}, h('p', { text: 'You see the published library again. Changes you did not publish are not shared.' }),
      h('label', { class: 'check' }, box, h('span', {}, 'Delete my editing copy', h('small', { text: 'Otherwise it stays in your sessions as a normal group.' })))),
    buttons: [{ label: 'Cancel', value: false }, { label: 'Stop editing', kind: 'primary', value: true, default: true }],
    onClose: v => resolve(!!v),
  }));
  if (!ok) return;
  try { await api.StopEditingTeamLibrary(id, del); await changed(); after && after(); } catch (e) { toast(errMsg(e), 'error'); }
}

export async function copyToMine(ids) {
  try {
    const top = await api.CopyToMySessions(ids);
    await changed();
    state.selected = top; state.selection = new Set([top]);
    afterChange();
    toast('Copied to your sessions. The copy is yours to change; the library is not affected.');
  } catch (e) { toast(errMsg(e), 'error'); }
}

// ---- publishing ----

export function publishDialog(groupId, after) {
  const groups = groupsFlat();
  if (!groups.length) { toast('Make a group with the sessions to share first.'); return; }
  const sel = h('select', {}, ...groups.map(g => h('option', { value: g.id, text: '  '.repeat(g.depth) + g.name })));
  sel.value = groupId || state.selected && groups.some(g => g.id === state.selected) ? (groupId || state.selected) : groups[0].id;
  const path = h('input', { type: 'text', spellcheck: false, placeholder: '\\\\server\\noc\\yacm-team.json' });
  const name = h('input', { type: 'text', spellcheck: false });
  const desc = h('input', { type: 'text', placeholder: 'What it is for, who maintains it' });
  const folders = [...new Set((state.settings.snippets || []).map(s => s.folder).filter(Boolean))].sort();
  const snip = h('select', {}, h('option', { value: '', text: 'None' }), ...folders.map(f => h('option', { value: f, text: f })));
  const info = h('div');
  const pathField = h('label', { class: 'field mono' }, h('span', { text: 'Library file (on a share, in a synced folder or in git)' }),
    h('div', { class: 'with-btn' }, path, h('button', { type: 'button', class: 'btn small', text: 'Browse', on: { click: async () => {
      const g = state.byId.get(sel.value);
      const p = await api.ChooseTeamFile(`${(name.value || g?.name || 'team').replace(/[\\/:*?"<>|]+/g, '-')}.json`);
      if (p) path.value = p;
    } } })));
  const loginNote = h('div');
  // Sessions that log in with their own user name instead of a credential
  // profile: colleagues would get that user name and be asked for a
  // password, instead of linking their own account.
  const paintLogins = () => {
    const own = [];
    const walk = pid => (state.children.get(pid) || []).forEach(n => {
      if (n.isGroup) walk(n.id);
      else if (n.session && n.session.protocol !== 'serial' && !effectiveLogin(n).cred) own.push(n);
    });
    walk(sel.value);
    const users = [...new Set(own.map(n => n.session.user).filter(Boolean))];
    loginNote.replaceChildren(...(own.length ? [h('p', { class: 'warning', text:
      `${plural(own.length, 'session logs', 'sessions log')} in without a credential profile${users.length ? ` (user ${users.slice(0, 3).join(', ')}${users.length > 3 ? ' …' : ''})` : ''}. Colleagues would get ${users.length ? 'that user name' : 'no login'} and be asked for a password. To let each colleague link their own account (an AD login, say): make a credential profile, choose it under Edit group and defaults > Login, and set the sessions to "From group" (Edit all sessions in it).` })] : []));
  };
  const sync = () => {
    paintLogins();
    const lib = state.teamLibs.find(l => l.editing && l.sourceGroup === sel.value);
    const g = state.byId.get(sel.value);
    if (lib) {
      path.value = lib.path; path.disabled = true;
      if (!name.value || name.dataset.auto) { name.value = lib.name; name.dataset.auto = '1'; }
      if (!desc.value) desc.value = lib.description || '';
      info.replaceChildren(h('p', { class: lib.newer ? 'warning' : 'hint', text: lib.newer
        ? `${lib.updatedBy} published revision ${lib.revision} after your copy was made (revision ${lib.sourceRevision}). Publishing would overwrite their changes; you will be asked.`
        : `Updates “${lib.name}” (now revision ${lib.revision}) for everyone who follows it.` }));
    } else {
      path.disabled = false;
      if (!name.value || name.dataset.auto) { name.value = g?.name || ''; name.dataset.auto = '1'; }
      info.replaceChildren();
    }
  };
  name.addEventListener('input', () => delete name.dataset.auto);
  sel.addEventListener('change', sync);
  sync();
  openDialog({
    title: 'Publish as team library',
    sub: 'Everything inside the group, for colleagues to follow read-only.',
    medium: true,
    body: h('div', {},
      field('Group', sel), pathField,
      h('div', { class: 'grid2' }, field('Library name', name), field('Commands to include (a folder of your library)', snip)),
      field('Description', desc),
      info, loginNote,
      h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Never published: passwords and passphrases, key files, local commands, log folders and hidden automation values. Credential profiles become named logins that every colleague links to a profile of their own. The previous version of the file is kept in a .yacm-backup folder next to it.' })),
    buttons: [
      { label: 'Cancel' },
      { label: 'Publish', kind: 'primary', default: true, onClick: async () => {
        if (!path.value.trim()) throw new Error('Choose where to save the library file.');
        const req = { groupId: sel.value, path: path.value.trim(), name: name.value.trim(), description: desc.value.trim(), snippetFolder: snip.value, force: false };
        let res = await api.PublishTeamLibrary(req);
        if (res.conflict) {
          const ok = await confirmDialog({ title: 'Replace the library?', message: res.conflict, ok: 'Publish anyway', danger: true });
          if (!ok) return false;
          res = await api.PublishTeamLibrary({ ...req, force: true });
        }
        await changed();
        after && after();
        publishedReport(res, req.name);
      } },
    ],
  });
}

function publishedReport(res, name) {
  openDialog({
    title: `Published ${name || 'the library'}`,
    sub: `Revision ${res.revision}: ${plural(res.sessions, 'session', 'sessions')}, ${plural(res.logins, 'login', 'logins')}. Colleagues who follow it see the change within a few seconds.`,
    medium: true,
    body: h('div', { class: 'report' },
      res.warnings?.length ? h('div', {}, h('h3', { text: `Left out (${res.warnings.length})` }), h('ul', {}, ...res.warnings.map(w => h('li', { text: w })))) : h('p', { text: 'Everything was published.' }),
      h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'To let colleagues follow it, send them the path of the file: Settings > Team libraries > Add a library.' })),
    buttons: [{ label: 'Done', kind: 'primary', default: true }],
  });
}
