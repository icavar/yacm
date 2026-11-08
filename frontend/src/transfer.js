// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Import and export of sessions: .yacm files, other programs, CSV/Excel
// device lists and NetBox.
import { api, on, state, reload } from './api.js';
import { h, openDialog, field, check, seg, toast, errMsg, contextMenu, plural, busyDialog } from './ui.js';

function countSessions(id) {
  let c = 0;
  for (const n of state.children.get(id) || []) c += n.isGroup ? countSessions(n.id) : 1;
  return c;
}

function slug(s) {
  return s.normalize('NFKD').replace(/[̀-ͯ]/g, '').replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'sessions';
}

// ---------- export ----------

export function exportDialog(nodeIds = null, after) {
  const ids = (Array.isArray(nodeIds) ? nodeIds : nodeIds ? [nodeIds] : []).filter(id => state.byId.has(id));
  const node = ids.length === 1 ? state.byId.get(ids[0]) : null;
  const total = state.nodes.filter(n => !n.isGroup).length;
  let scope = ids.length ? 'node' : 'all';
  const selCount = ids.reduce((a, id) => a + (state.byId.get(id).isGroup ? countSessions(id) : 1), 0);
  const scopeSeg = seg([
    { value: 'all', label: `All sessions (${total})` },
    ...(node ? [{ value: 'node', label: node.isGroup ? `Group ${node.name} (${countSessions(node.id)})` : `Only ${node.name}` }]
      : ids.length ? [{ value: 'node', label: `The ${ids.length} selected (${selCount})` }] : []),
  ], scope, v => (scope = v));

  const withPw = h('input', { type: 'checkbox', disabled: !state.vault.unlocked });
  const pw = h('input', { type: 'password', autocomplete: 'new-password' });
  const pw2 = h('input', { type: 'password', autocomplete: 'new-password' });
  const master = h('input', { type: 'password', autocomplete: 'current-password' });
  const pwBox = h('div', { hidden: true },
    field('Your master password', master, 'Exporting passwords needs it, like showing a saved password.'),
    h('div', { class: 'grid2' }, field('Export password (at least 12 characters)', pw), field('Repeat export password', pw2)),
    h('p', { class: 'hint', style: { color: 'var(--faint)', marginTop: 0 }, text: 'Use a different password than your master password. Whoever imports the file needs it; without it, the sessions still import but the passwords do not.' }));
  withPw.addEventListener('change', () => { pwBox.hidden = !withPw.checked; if (withPw.checked) master.focus(); });
  const hosts = h('input', { type: 'checkbox', checked: true });
  const settings = h('input', { type: 'checkbox' });

  openDialog({
    title: 'Export sessions',
    sub: 'Creates a .yacm file you can import on another computer or keep as a backup.',
    medium: true,
    body: h('div', {},
      ids.length ? h('div', { class: 'field' }, h('span', { text: 'What to export' }), scopeSeg) : null,
      check('Include passwords, encrypted', withPw, state.vault.unlocked ? 'Saved passwords, key passphrases, hidden automation values and the passwords of the credential profiles the sessions use.' : 'Unlock the vault to export passwords.'),
      pwBox,
      check('Include trusted host keys', hosts, 'Public keys only, so the other computer recognises your devices without asking again.'),
      check('Include app settings', settings, 'Theme, font, scrollback, auto-lock and log folder.')),
    buttons: [
      { label: 'Cancel' },
      { label: 'Choose file and export', kind: 'primary', default: true, onClick: async d => {
        if (withPw.checked) {
          if (!master.value) throw new Error('Enter your master password to export passwords.');
          if (pw.value.length < 12) throw new Error('The export password must be at least 12 characters.');
          if (pw.value !== pw2.value) throw new Error('The export passwords do not match.');
        }
        const name = scope === 'node' ? slug(node ? node.name : 'selection') : 'all';
        const path = await api.ChooseSaveFile('Export sessions', `YACM-${name}-${new Date().toISOString().slice(0, 10)}.yacm`);
        if (!path) return false;
        const sum = await api.ExportSessions(path, {
          nodeIds: scope === 'node' ? ids : [],
          includeSecrets: withPw.checked, password: withPw.checked ? pw.value : '', master: withPw.checked ? master.value : '',
          includeSettings: settings.checked, includeKnownHosts: hosts.checked,
        });
        const extra = [sum.secrets ? plural(sum.secrets, 'password', 'passwords') : null, sum.knownHosts ? plural(sum.knownHosts, 'host key', 'host keys') : null].filter(Boolean);
        toast(`Exported ${plural(sum.sessions, 'session', 'sessions')}${extra.length ? ' with ' + extra.join(' and ') : ''} to ${sum.path.split(/[\\/]/).pop()}.`);
        after && after();
      } },
    ],
  });
}

// ---------- import ----------

// importMenu shows where sessions can come from, anchored to a button.
export async function importMenu(anchor, after, { withExport = false } = {}) {
  let sources = [];
  try { sources = (await api.ImportSources()) || []; } catch { /* optional */ }
  const r = anchor.getBoundingClientRect();
  const short = p => (p.length > 46 ? '…' + p.slice(-45) : p);
  const menu = contextMenu(r.left, r.top, [
    { label: 'Import from a file…', action: () => importFile(after) },
    { label: 'Import a sessions folder…', action: () => importFolder(after) },
    { label: 'Import a CSV or Excel list…', action: () => importTableFile(after) },
    { label: 'Import from NetBox…', action: () => netboxDialog(after) },
    { label: 'Team libraries…', title: 'Follow a library a colleague published, or see the ones you follow (Settings > Team libraries)', action: () => import('./teamui.js').then(m => m.openTeamSettings()).catch(e => toast(errMsg(e), 'error')) },
    ...(sources.length ? ['-', ...sources.map(s => ({ label: `${s.label}: ${short(s.display || s.path)}`, action: () => importPath(s.path, s.kind, after) }))] : []),
    ...(withExport ? ['-', { label: 'Export sessions…', action: () => (state.nodes.length ? exportDialog(null, after) : toast('There are no sessions to export yet.')) }] : []),
  ]);
  // Open upwards from the button when there is not enough room below it.
  if (menu) {
    const hgt = menu.getBoundingClientRect().height;
    if (r.bottom + hgt > window.innerHeight - 4) menu.style.top = `${Math.max(4, r.top - hgt - 6)}px`;
    else menu.style.top = `${r.bottom + 6}px`;
  }
}

export async function importFlow(after) { return importFile(after); }

async function importFile(after) {
  let path;
  try { path = await api.ChooseFile('Import sessions', 'import'); } catch (e) { toast(errMsg(e), 'error'); return; }
  if (path) importPath(path, null, after);
}

async function importFolder(after) {
  let path;
  try { path = await api.ChooseDirectory('Choose the SecureCRT, Xshell or PuTTY sessions folder'); } catch (e) { toast(errMsg(e), 'error'); return; }
  if (path) importPath(path, null, after);
}

async function importTableFile(after) {
  let path;
  try { path = await api.ChooseFile('Choose a CSV or Excel device list', 'table'); } catch (e) { toast(errMsg(e), 'error'); return; }
  if (path) tableDialog(path, after);
}

// withProgress runs a slow import step behind a progress dialog with Cancel.
async function withProgress(title, fn) {
  const b = busyDialog(title, { cancel: () => api.CancelImport(), text: 'Reading the sessions…' });
  const off = on('import:progress', p => b.update(p.done, p.total, p.step));
  try { return await fn(); } finally { if (typeof off === 'function') off(); b.close(); }
}

const cancelled = e => /import cancelled/i.test(errMsg(e));

// importSource imports a file or folder: SecureCRT, a .yacm file, a sheet …
export function importSource(path, kind, after) { return importPath(path, kind, after); }

async function importPath(path, kind, after) {
  let info;
  try {
    info = await withProgress('Reading sessions', () => (kind ? api.PreviewForeign(kind, path, '') : api.InspectImport(path)));
  } catch (e) { cancelled(e) ? toast('Import cancelled.') : toast(errMsg(e), 'error', 9000); return; }
  if (info.kind === 'table') return tableDialog(path, after);
  if (info.kind === 'yacm') return importYacm(path, info, after);
  foreignDialog(info, after);
}

function foreignDialog(info, after) {
  let f = info.foreign;
  const date = new Date().toISOString().slice(0, 10);
  const groupName = h('input', { type: 'text', value: `Imported from ${f.label} ${date}`, spellcheck: false });
  const summary = h('p');
  const pw = h('input', { type: 'password', autocomplete: 'off' });
  const pwStatus = h('span', { class: 'hint' });
  const check = h('button', { type: 'button', class: 'btn small', text: 'Check' });
  const pwField = h('label', { class: 'field' }, h('span', { text: f.passwordLabel || 'Passphrase' }), h('div', { class: 'with-btn' }, pw, check), pwStatus);
  const render = () => {
    const parts = [`Found ${plural(f.sessions, 'session', 'sessions')}${f.groups ? ` in ${plural(f.groups, 'group', 'groups')}` : ''}`];
    if (f.secrets && !f.passwordLabel) parts.push(`${plural(f.secrets, 'saved password', 'saved passwords')} can be moved into the vault`);
    if (f.automated) parts.push(`${plural(f.automated, 'session logs', 'sessions log')} in automatically (logon actions become automation rules)`);
    if (f.profiles) parts.push(plural(f.profiles, 'saved login becomes a credential profile', 'saved logins become credential profiles'));
    if (f.skipped) parts.push(`${f.skipped} other ${f.skipped === 1 ? 'entry' : 'entries'} (RDP, VNC, SFTP…) will be listed as skipped`);
    summary.textContent = parts.join('. ') + '.';
    pwField.hidden = !f.passwordLabel;
    pwStatus.textContent = f.secretsFailed
      ? `${plural(f.secretsFailed, 'saved password is', 'saved passwords are')} protected. Enter the ${f.passwordLabel || 'passphrase'} and press Check, or import without them.`
      : f.secrets ? `${plural(f.secrets, 'saved password', 'saved passwords')} can be decrypted.` : '';
    pwStatus.style.color = f.secretsFailed ? 'var(--warn)' : 'var(--ok)';
  };
  check.addEventListener('click', async () => {
    check.disabled = true;
    try { f = (await withProgress('Checking the passphrase', () => api.PreviewForeign(info.kind, info.path, pw.value))).foreign; render(); } catch (e) { pwStatus.textContent = cancelled(e) ? 'Cancelled.' : errMsg(e); }
    check.disabled = false;
  });
  render();
  openDialog({
    title: `Import from ${f.label}`,
    sub: info.name,
    medium: true,
    body: h('div', {},
      summary,
      pwField,
      f.secrets && !state.vault.unlocked ? h('p', { class: 'warning', text: 'The vault is locked, so saved passwords will be skipped. Unlock it first to import them.' }) : null,
      field('Put the sessions into a new group named', groupName),
      h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'Nothing in the other program is changed. Settings YACM does not take over (RDP, file transfer, port forwards, some proxies) are listed after the import.' })),
    buttons: [
      { label: 'Cancel' },
      { label: 'Import', kind: 'primary', default: true, onClick: async () => {
        const rep = await withProgress(`Importing from ${f.label}`, () => api.ImportForeign({ kind: info.kind, path: info.path, password: pw.value, groupName: groupName.value }));
        await reload();
        after && after();
        const extra = [rep.secrets ? `${plural(rep.secrets, 'saved password', 'saved passwords')} moved into the vault.` : f.passwordLabel ? 'No passwords were imported.' : '',
          rep.automated ? `Login automation imported for ${plural(rep.automated, 'session', 'sessions')}.` : '',
          rep.profiles ? `${plural(rep.profiles, 'credential profile', 'credential profiles')} added.` : '',
          rep.profilesKept ? `${plural(rep.profilesKept, 'credential profile', 'credential profiles')} you already had ${rep.profilesKept === 1 ? 'is' : 'are'} used.` : ''].filter(Boolean).join(' ');
        reportDialog(rep.sessions ? `Imported ${plural(rep.sessions, 'session', 'sessions')} from ${f.label}` : 'Nothing imported', extra, rep,
          info.kind === 'ssh-config' ? '' : 'SSH sessions imported from other programs use the Compatible algorithm set, because these configurations often point at older devices. Switch them to Secure where the device allows it.');
      } },
    ],
  });
}

// reportItems lists import notes grouped by their reason: a reason shared
// by a few entries is one line with the entries folded under it, so
// thousands of sessions with the same note stay readable.
function reportItems(items, cls) {
  const groups = new Map();
  for (const it of items) {
    const g = groups.get(it.reason) || [];
    g.push(it.name);
    groups.set(it.reason, g);
  }
  return h('ul', {}, ...[...groups].map(([reason, names]) => {
    if (names.length < 3) return names.map(n => h('li', { class: cls, text: n ? `${n}: ${reason}` : reason }));
    const shown = names.slice(0, 200);
    return h('li', { class: cls }, h('details', {},
      h('summary', { text: `${reason} (${names.length})` }),
      h('ul', {}, ...shown.map(n => h('li', { text: n })), names.length > shown.length ? h('li', { text: `and ${names.length - shown.length} more` }) : null)));
  }).flat());
}

// splitWarning separates "name: note" as the importers write warnings.
function splitWarning(w) {
  const i = w.indexOf(': ');
  return i > 0 ? { name: w.slice(0, i), reason: w.slice(i + 2) } : { name: '', reason: w };
}

function reportDialog(title, sub, rep, note) {
  openDialog({
    title, sub, medium: true,
    body: h('div', { class: 'report' },
      rep.skipped?.length ? h('div', {}, h('h3', { text: `Not imported (${rep.skipped.length})` }),
        reportItems(rep.skipped.map(s => ({ name: `${s.name}${s.method ? ` (${s.method})` : ''}`, reason: s.reason })), 'skip')) : null,
      rep.warnings?.length ? h('div', {}, h('h3', { text: `Imported, but check (${rep.warnings.length})` }),
        reportItems(rep.warnings.map(splitWarning), '')) : null,
      !rep.skipped?.length && !rep.warnings?.length ? h('p', { text: 'Everything was imported.' }) : null,
      note ? h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: note }) : null),
    buttons: [{ label: 'Done', kind: 'primary', default: true }],
  });
}

function importYacm(path, info, after) {
  const i = info.info;
  const fileName = info.name.replace(/\.yacm$/i, '');
  const treeEmpty = state.nodes.length === 0;
  let target = treeEmpty && !i.existing ? 'root' : 'group';
  let conflict = 'copy';
  const groupName = h('input', { type: 'text', value: `Imported ${fileName}`, spellcheck: false });
  const groupField = field('Group name', groupName);
  groupField.hidden = target !== 'group';
  const targetSeg = seg([
    { value: 'group', label: 'Into a new group' },
    { value: 'root', label: 'Keep the file’s structure at the top level' },
  ], target, v => { target = v; groupField.hidden = v !== 'group'; });

  const conflictBox = i.existing ? h('div', { class: 'field' },
    h('span', { text: `${plural(i.existing, 'item from this file is', 'items from this file are')} already here, for example from an earlier import. What should happen to them?` }),
    seg([
      { value: 'copy', label: 'Keep both' },
      { value: 'replace', label: 'Replace mine' },
      { value: 'skip', label: 'Skip them' },
    ], conflict, v => (conflict = v))) : null;

  const pw = h('input', { type: 'password', autocomplete: 'off' });
  const settings = h('input', { type: 'checkbox' });
  // Off by default: trusted keys from someone else's file decide which
  // servers you accept without a question.
  const hosts = h('input', { type: 'checkbox' });
  const exported = i.exportedAt ? new Date(i.exportedAt).toLocaleString() : 'unknown date';

  openDialog({
    title: `Import ${info.name}`,
    sub: `${i.app || 'YACM'} export from ${exported}: ${plural(i.sessions, 'session', 'sessions')} in ${plural(i.groups, 'group', 'groups')}${i.profiles ? `, ${plural(i.profiles, 'credential profile', 'credential profiles')}` : ''}${i.scope ? ` (${i.scope})` : ''}.`,
    medium: true,
    body: h('div', {},
      h('div', { class: 'field' }, h('span', { text: 'Where to put them' }), targetSeg),
      groupField,
      conflictBox,
      i.hasSecrets ? (state.vault.unlocked
        ? field('Export password', pw, 'The file contains encrypted passwords. Leave this empty to import the sessions without them.')
        : h('p', { class: 'warning', text: 'The file contains encrypted passwords, but your vault is locked, so they will be skipped. Unlock the vault first to import them.' })) : null,
      i.knownHosts ? check(`Add ${plural(i.knownHosts, 'trusted host key', 'trusted host keys')}`, hosts, 'Only if you trust whoever made the file: YACM then connects to these hosts without asking. Only keys for the sessions in the file are added, and never one for a host you already trust.') : null,
      i.settings ? check('Use the app settings from the file', settings, 'Takes its theme, fonts, colours and highlighting. Your security settings, saved commands and team libraries stay as they are.') : null,
      i.localCommands ? h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: `${plural(i.localCommands, 'session command runs', 'session commands run')} on this computer before or after connecting. They will ask before running, because they come from a file.` }) : null),
    buttons: [
      { label: 'Cancel' },
      { label: 'Import', kind: 'primary', default: true, onClick: async () => {
        const rep = await api.ImportSessions(path, {
          target, groupName: groupName.value, onConflict: conflict,
          password: state.vault.unlocked ? pw.value : '',
          applySettings: settings.checked, addKnownHosts: hosts.checked,
        });
        await reload();
        after && after();
        const parts = [];
        if (rep.sessions) parts.push(plural(rep.sessions, 'new session', 'new sessions'));
        if (rep.replaced) parts.push(`${rep.replaced} replaced`);
        if (rep.profiles) parts.push(plural(rep.profiles, 'credential profile', 'credential profiles'));
        if (rep.skippedExisting) parts.push(`${rep.skippedExisting} skipped`);
        const extra = [];
        if (rep.secrets) extra.push(`${plural(rep.secrets, 'password', 'passwords')} stored in the vault`);
        if (rep.knownHosts) extra.push(plural(rep.knownHosts, 'host key added', 'host keys added'));
        if (rep.settingsApplied) extra.push('settings applied');
        reportDialog(parts.length ? `Imported ${parts.join(', ')}` : 'Nothing imported', extra.length ? extra.join(', ') + '.' : '', rep);
      } },
    ],
  });
}

// ---------- defaults for imported groups ----------

function targetFields(defaultName) {
  const name = h('input', { type: 'text', value: defaultName, spellcheck: false });
  const cred = h('select', {}, h('option', { value: '', text: 'None (each device as in the source)' }), ...state.credentials.map(c => h('option', { value: c.id, text: `${c.name}${c.user ? ` (${c.user})` : ''}` })));
  const ssh = state.nodes.filter(n => n.session?.protocol === 'ssh').sort((a, b) => a.name.localeCompare(b.name));
  const jump = h('select', {}, h('option', { value: '', text: 'None, connect directly' }), ...ssh.map(n => h('option', { value: n.id, text: `${n.name} (${n.session.host})` })));
  const user = h('input', { type: 'text', spellcheck: false, placeholder: 'Only when no profile is chosen' });
  const el = h('div', {},
    field('Put the sessions into a new group named', name),
    h('div', { class: 'grid2' },
      field('Login for the group (credential profile)', cred, state.credentials.length ? null : 'Create profiles under Profiles to log in automatically.'),
      field('Jump host for the group', jump)),
    field('User name', user, 'For devices without a user name of their own when no profile is chosen.'));
  return { el, value: () => ({ groupName: name.value.trim(), credentialId: cred.value, jumpVia: jump.value, user: user.value.trim() }) };
}

// ---------- CSV / Excel ----------

const FIELD_LABELS = {
  name: 'Name', host: 'IP address or host', port: 'Port', protocol: 'Protocol', user: 'User name', password: 'Password',
  group: 'Group (A/B for subgroups)', description: 'Description', clusters: 'Tags', credential: 'Credential profile name',
  jump: 'Jump host (session name)', serial: 'Serial port', baud: 'Baud rate', color: 'Colour',
};

async function tableDialog(path, after) {
  let p;
  try { p = await api.PreviewTable(path); } catch (e) { toast(errMsg(e), 'error', 9000); return; }
  const mapping = { ...p.mapping };
  const preview = h('div', { class: 'sheet-preview' });
  const renderPreview = () => {
    const used = {};
    for (const [f, i] of Object.entries(mapping)) if (i >= 0) used[i] = FIELD_LABELS[f];
    const t = h('table');
    t.append(h('tr', {}, ...p.headers.map((hd, i) => h('th', {}, hd, used[i] ? h('span', { class: 'used', text: `→ ${used[i]}` }) : null))));
    for (const r of p.rows) t.append(h('tr', {}, ...p.headers.map((_, i) => h('td', { text: r[i] || '' }))));
    preview.replaceChildren(t);
  };
  const selects = p.fields.map(f => {
    const s = h('select', {}, h('option', { value: '-1', text: '(not used)' }), ...p.headers.map((hd, i) => h('option', { value: String(i), text: hd })));
    s.value = String(mapping[f] ?? -1);
    s.addEventListener('change', () => { mapping[f] = +s.value; renderPreview(); });
    return field(FIELD_LABELS[f] || f, s);
  });
  renderPreview();
  let proto = 'ssh';
  const protoSeg = seg([{ value: 'ssh', label: 'SSH' }, { value: 'telnet', label: 'Telnet' }], proto, v => (proto = v));
  const target = targetFields(`Imported from ${p.name}`);
  openDialog({
    title: `Import ${plural(p.total, 'device', 'devices')}`,
    sub: p.sheet ? `${p.name}, sheet ${p.sheet}` : p.name,
    wide: true,
    body: h('div', { class: 'pane', style: { padding: '14px 20px', overflow: 'auto', flex: '1' } },
      preview,
      h('h3', { text: 'Which column is what', style: { margin: '4px 0 8px', fontSize: '16px', fontWeight: 600 } }),
      h('p', { class: 'hint', style: { color: 'var(--muted)', margin: '0 0 10px' }, text: 'Guessed from the column names. Only the address is required; rows without a name are named after their address.' }),
      h('div', { class: 'map-grid' }, ...selects),
      h('div', { class: 'field' }, h('span', { text: 'Protocol when there is no protocol column' }), protoSeg),
      target.el),
    buttons: [
      { label: 'Cancel' },
      { label: 'Import', kind: 'primary', default: true, onClick: async () => {
        if ((mapping.host ?? -1) < 0 && (mapping.serial ?? -1) < 0) throw new Error('Choose which column holds the IP address or host name.');
        const rep = await api.ImportTable({ path: p.path, mapping, protocol: proto, ...target.value() });
        await reload();
        after && after();
        reportDialog(rep.sessions ? `Imported ${plural(rep.sessions, 'session', 'sessions')}` : 'Nothing imported',
          rep.secrets ? `${plural(rep.secrets, 'password', 'passwords')} moved into the vault.` : '', rep, '');
      } },
    ],
  });
}

// ---------- NetBox ----------

async function netboxDialog(after) {
  const def = await api.NetBoxDefaults().catch(() => ({ url: '', hasToken: false }));
  const url = h('input', { type: 'text', value: def.url || '', spellcheck: false, placeholder: 'https://netbox.example.net' });
  const token = h('input', { type: 'password', autocomplete: 'off', placeholder: def.hasToken ? 'Saved in the vault. Type to replace.' : 'API token (read access is enough)' });
  const saveTok = h('input', { type: 'checkbox', checked: state.vault.unlocked, disabled: !state.vault.unlocked });
  const insecure = h('input', { type: 'checkbox' });
  const site = h('input', { type: 'text', spellcheck: false, placeholder: 'zagreb-dc1' });
  const role = h('input', { type: 'text', spellcheck: false, placeholder: 'core-switch' });
  const tag = h('input', { type: 'text', spellcheck: false, placeholder: 'managed' });
  const status = h('select', {}, ...[['active', 'Active only'], ['planned', 'Planned'], ['staged', 'Staged'], ['any', 'Any status']].map(([v, t]) => h('option', { value: v, text: t })));
  const vms = h('input', { type: 'checkbox' });
  const groupBy = h('select', {}, h('option', { value: 'site', text: 'Site' }), h('option', { value: 'site-role', text: 'Site, then role' }), h('option', { value: 'none', text: 'No subgroups' }));
  let proto = 'ssh';
  const protoSeg = seg([{ value: 'ssh', label: 'SSH' }, { value: 'telnet', label: 'Telnet' }], proto, v => (proto = v));
  const result = h('div', { class: 'list', hidden: true });
  const target = targetFields(`NetBox ${new Date().toISOString().slice(0, 10)}`);
  const query = () => ({
    url: url.value.trim(), token: token.value.trim(), site: site.value.trim(), role: role.value.trim(), tag: tag.value.trim(),
    status: status.value, includeVms: vms.checked, insecureTls: insecure.checked, saveToken: saveTok.checked,
    groupBy: groupBy.value, protocol: proto, port: 0, ...target.value(),
  });
  const check_ = h('button', { type: 'button', class: 'btn', text: 'Check connection', on: { click: async () => {
    check_.disabled = true;
    result.hidden = false;
    result.replaceChildren(h('div', { class: 'empty-note', text: 'Asking NetBox…' }));
    try {
      const pv = await api.NetBoxPreview(query());
      result.replaceChildren(
        h('div', { class: 'item' }, h('div', { class: 'main' },
          h('div', { class: 'title', text: `${plural(pv.devices, 'device', 'devices')}${pv.vms ? ` and ${plural(pv.vms, 'virtual machine', 'virtual machines')}` : ''} with a primary IP` }),
          h('div', { class: 'meta', text: pv.sites?.length ? `Sites: ${pv.sites.slice(0, 8).join(', ')}${pv.sites.length > 8 ? ` and ${pv.sites.length - 8} more` : ''}` : 'No sites' }))),
        ...(pv.sample || []).map(d => h('div', { class: 'item' }, h('div', { class: 'main' },
          h('div', { class: 'title', text: d.name }), h('div', { class: 'meta', text: [d.host, d.site, d.role, d.platform].filter(Boolean).join(' · ') })))));
    } catch (e) {
      result.replaceChildren(h('div', { class: 'empty-note', style: { color: 'var(--danger)' }, text: errMsg(e) }));
    } finally { check_.disabled = false; }
  } } });
  openDialog({
    title: 'Import from NetBox',
    sub: 'Reads devices that have a primary IP address. Nothing in NetBox is changed.',
    wide: true,
    body: h('div', { class: 'pane', style: { padding: '14px 20px', overflow: 'auto', flex: '1' } },
      h('div', { class: 'grid2' }, field('NetBox address', url), field('API token', token)),
      check('Remember the token in the vault', saveTok, state.vault.unlocked ? null : 'Unlock the vault to remember it.'),
      check('Accept a self-signed certificate', insecure, 'Only for your own lab server. The connection is still encrypted, but the server is not verified.'),
      h('h3', { text: 'Which devices', style: { margin: '12px 0 8px', fontSize: '16px', fontWeight: 600 } }),
      h('div', { class: 'grid2' }, field('Site (slug, empty = all)', site), field('Role (slug, empty = all)', role), field('Tag (slug, empty = all)', tag), field('Status', status)),
      check('Also virtual machines', vms),
      h('div', { style: { display: 'flex', gap: '10px', alignItems: 'center', margin: '6px 0 12px' } }, check_),
      result,
      h('h3', { text: 'How to file them', style: { margin: '12px 0 8px', fontSize: '16px', fontWeight: 600 } }),
      h('div', { class: 'grid2' }, field('Subgroups by', groupBy), h('div', { class: 'field' }, h('span', { text: 'Protocol' }), protoSeg)),
      target.el),
    buttons: [
      { label: 'Cancel' },
      { label: 'Import', kind: 'primary', default: true, onClick: async () => {
        const rep = await api.NetBoxImport(query());
        await reload();
        after && after();
        reportDialog(`Imported ${plural(rep.sessions, 'session', 'sessions')} from NetBox`, rep.groups ? `In ${plural(rep.groups, 'group', 'groups')}.` : '', rep,
          'The sessions take their login and jump host from the new group. Change them there (Edit group > Defaults) and every device follows.');
      } },
    ],
  });
}
