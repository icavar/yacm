// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Settings, import, lock screen.
import { api, state, reload, runtime, KNOWN_PROTOCOLS } from './api.js';
import { h, openDialog, field, check, seg, errMsg, toast, uuid, confirmDialog, plural, IS_MAC } from './ui.js';
import { Highlighter, compileRules, HL_COLORS } from './highlight.js';
import { picker as termPicker, preview as termPreview, resolve as termResolve, THEME_COLORS } from './termcolors.js';
import { showLegal, PROJECT_URL, SUPPORT_URL } from './legal.js';
import { check as checkUpdates } from './updates.js';
import { whatsNewFor } from './whatsnew.js';

const SAMPLE = `R1#show ip interface brief
GigabitEthernet1       10.0.0.1        YES NVRAM  up                    up
GigabitEthernet2       192.0.2.1       YES NVRAM  administratively down down
*Oct 22 13:35:01: %LINK-3-UPDOWN: Interface Gi1/0/24, changed state to down
Te1/1/1  connected  trunk   aabb.cc00.0100   err-disabled`;

const ANSI_CSS = { 31: 'var(--c-red)', 32: 'var(--c-green)', 33: 'var(--c-yellow)', 34: 'var(--c-blue)', 35: 'var(--c-purple)', 36: 'var(--c-teal)', 37: 'var(--text)' };

// ansiToNodes renders the highlighter's own SGR codes for the preview.
function ansiToNodes(text) {
  const out = [];
  const re = /\x1b\[([\d;]*)m/g;
  let pos = 0;
  let style = {};
  let m;
  const push = t => { if (t) out.push(h('span', { style: { ...style } }, t)); };
  while ((m = re.exec(text))) {
    push(text.slice(pos, m.index));
    pos = re.lastIndex;
    for (const code of m[1].split(';').map(Number)) {
      if (code === 1) style.fontWeight = '700';
      else if (code === 22) delete style.fontWeight;
      else if (code === 39) delete style.color;
      else if (code === 49) delete style.background;
      else if (code === 30) style.color = 'var(--bg-term)';
      else if (ANSI_CSS[code]) style.color = ANSI_CSS[code];
      else if (ANSI_CSS[code - 60]) style.color = ANSI_CSS[code - 60];
      else if (ANSI_CSS[code - 10]) style.background = ANSI_CSS[code - 10];
      else if (ANSI_CSS[code - 70]) style.background = ANSI_CSS[code - 70];
    }
  }
  push(text.slice(pos));
  return out;
}

function highlightEditor(st) {
  const rules = st.highlightRules;
  const box = h('div');
  const preview = h('pre', { class: 'hostkey', style: { whiteSpace: 'pre', overflow: 'auto', background: 'var(--bg-term)', fontSize: '12.5px' } });
  const render = () => {
    const { errors } = compileRules(rules);
    const t = h('table', { class: 'rules' }, h('tr', {}, h('th', { text: 'On' }), h('th', { text: 'Pattern (regular expression)' }), h('th', { text: 'Colour' }), h('th', { text: 'Bold' }), h('th', { text: 'Background' }), h('th', { text: 'Aa', title: 'Ignore upper/lower case' }), h('th', { text: 'Alert', title: 'Mark the tab and show a message when this appears in a session you are not looking at' }), h('th')));
    rules.forEach((r, i) => {
      const color = h('select', { on: { change: e => { r.color = e.target.value; renderPreview(); } } }, ...HL_COLORS.map(c => h('option', { value: c, text: c.replace('bright-', 'bright ') })));
      color.value = r.color || 'yellow';
      const pat = h('input', { type: 'text', value: r.pattern, spellcheck: false, title: errors[i] || '', style: errors[i] ? { borderColor: 'var(--danger)' } : {}, on: { input: e => { r.pattern = e.target.value; renderPreview(); } , change: render } });
      const cb = (key, title) => h('input', { type: 'checkbox', checked: !!r[key], title, on: { change: e => { r[key] = e.target.checked; renderPreview(); } } });
      t.append(h('tr', {},
        h('td', { style: { width: '34px' } }, cb('enabled')),
        h('td', {}, pat, errors[i] ? h('div', { class: 'hint', style: { color: 'var(--danger)' }, text: errors[i] }) : null),
        h('td', { style: { width: '130px' } }, color),
        h('td', { style: { width: '40px' } }, cb('bold')),
        h('td', { style: { width: '70px' } }, cb('background', 'Colour the background instead of the text')),
        h('td', { style: { width: '30px' } }, cb('ignoreCase', 'Ignore upper/lower case')),
        h('td', { style: { width: '44px' } }, cb('alert', 'Alert when this appears in a session you are not looking at')),
        h('td', { style: { width: '24px' } }, h('button', { type: 'button', class: 'del', text: '×', title: 'Remove', on: { click: () => { rules.splice(i, 1); render(); } } }))));
    });
    box.replaceChildren(t,
      h('div', { style: { display: 'flex', gap: '8px', marginBottom: '10px' } },
        h('button', { type: 'button', class: 'btn small', text: 'Add rule', on: { click: () => { rules.push({ id: uuid(), enabled: true, pattern: '', color: 'yellow', bold: false, background: false, ignoreCase: true }); render(); } } }),
        h('button', { type: 'button', class: 'btn small', text: 'Restore the standard rules', on: { click: async () => { const d = await api.DefaultHighlightRules(); rules.splice(0, rules.length, ...d); render(); } } })));
    renderPreview();
  };
  const renderPreview = () => {
    const hl = new Highlighter(st.highlight ? rules : []);
    preview.replaceChildren(...ansiToNodes(hl.process(SAMPLE)));
  };
  render();
  return { el: h('div', {}, box, h('span', { class: 'hint', style: { color: 'var(--faint)', fontSize: '12.5px' }, text: 'Preview' }), preview), renderPreview };
}

// The tab shown when Settings opens again (per viewer, best effort).
const TAB_KEY = 'yacm.settingsTab';
function lastTab() { try { return localStorage.getItem(TAB_KEY) || ''; } catch { return ''; } }
function rememberTab(id) { try { localStorage.setItem(TAB_KEY, id); } catch { /* storage off */ } }

// Extra sections other modules add (data folder, team libraries).
const extraSections = [];
export function addSettingsSection(section) { extraSections.push(section); }

export async function openSettings(onApply, startTab) {
  const st = structuredClone(state.settings);
  st.rightClick = st.rightClick === 'paste' ? 'paste' : 'menu';
  st.pageKeys = st.pageKeys === 'device' ? 'device' : 'scroll';
  st.hostnameRename = ['always', 'off'].includes(st.hostnameRename) ? st.hostnameRename : 'ask';
  /** @type {Record<string, any>} */
  const about = await api.About().catch(() => ({}));
  let kh = await api.KnownHostEntries().catch(() => []);
  const netbox = await api.NetBoxDefaults().catch(() => ({}));
  const hint = text => h('p', { class: 'lead', text });

  // ---- Appearance ----
  const theme = seg([{ value: 'dark', label: 'Dark' }, { value: 'light', label: 'Light' }], st.theme || 'dark', v => (st.theme = v));
  const fs = h('input', { type: 'number', min: 8, max: 32, value: st.fontSize, on: { input: e => { st.fontSize = +e.target.value; tPrev.update(); } } });
  const fw = seg([{ value: 'light', label: 'Light' }, { value: '', label: 'Regular' }, { value: 'medium', label: 'Medium' }], st.fontWeight || '', v => { st.fontWeight = v; tPrev.update(); });
  const sb = h('input', { type: 'number', min: 1000, step: 1000, value: st.scrollback, on: { input: e => (st.scrollback = +e.target.value) } });
  const themeOf = () => THEME_COLORS[st.theme === 'light' ? 'light' : 'dark'];
  const hex = v => (/^#[0-9a-f]{6}$/i.test(v || '') ? v : '');
  const weightOf = () => ({ light: 300, medium: 500 })[st.fontWeight] || 400;
  const tPrev = termPreview(() => ({ text: hex(st.termText) || themeOf().text, bg: hex(st.termBackground) || themeOf().bg, weight: weightOf(), size: st.fontSize >= 8 && st.fontSize <= 32 ? st.fontSize : 0 }));
  theme.addEventListener('click', () => tPrev.update());
  const tText = termPicker(st.termText || 'theme', v => { st.termText = v; tPrev.update(); }, { kind: 'text', allowInherit: false });
  const tBg = termPicker(st.termBackground || 'theme', v => { st.termBackground = v; tPrev.update(); }, { kind: 'bg', allowInherit: false });
  const appearance = () => [
    h('h3', { text: 'Appearance' }),
    h('div', { class: 'field' }, h('span', { text: 'Theme' }), theme),
    field('Font size', fs, 'Each session and tab can use its own size (Ctrl + mouse wheel, or Aa in the status bar).'),
    h('div', { class: 'field' }, h('span', { text: 'Font weight' }), fw, h('span', { class: 'hint', text: 'Bold text from the device is always heavier (see the preview below).' })),
    h('h3', { text: 'Terminal colours', style: { marginTop: '16px' } }),
    hint('The text and background colour of every session that sets none itself (Edit > Terminal and logs) and whose group sets none. “Theme” uses the colours of the Dark or Light theme.'),
    h('div', { class: 'field' }, h('span', { text: 'Terminal text colour' }), tText),
    h('div', { class: 'field' }, h('span', { text: 'Terminal background' }), tBg),
    tPrev.el,
  ];

  // ---- Terminal ----
  const cos = h('input', { type: 'checkbox', checked: st.copyOnSelect, on: { change: e => (st.copyOnSelect = e.target.checked) } });
  const rc = seg([{ value: 'menu', label: 'Open a menu' }, { value: 'paste', label: 'Paste at once (PuTTY)' }], st.rightClick, v => (st.rightClick = v));
  const pk = seg([{ value: 'scroll', label: 'Scroll the terminal' }, { value: 'device', label: 'Send to the device' }], st.pageKeys, v => (st.pageKeys = v));
  const pd = h('input', { type: 'number', min: 0, max: 10000, step: 50, value: st.pasteDelay || 0, on: { input: e => (st.pasteDelay = Math.max(0, +e.target.value || 0)) } });
  const ren = seg([{ value: 'ask', label: 'Ask' }, { value: 'always', label: 'Always rename' }, { value: 'off', label: 'Off' }], st.hostnameRename, v => (st.hostnameRename = v));
  const reopen = h('input', { type: 'checkbox', checked: !!st.reopenSessions, on: { change: e => (st.reopenSessions = e.target.checked) } });
  const upd = h('input', { type: 'checkbox', checked: st.updateCheck === 'daily', on: { change: e => (st.updateCheck = e.target.checked ? 'daily' : 'off') } });
  const reloadOn = h('input', { type: 'checkbox', checked: !st.noReloadReconnect, on: { change: e => (st.noReloadReconnect = !e.target.checked) } });
  const reloadMax = h('input', { type: 'number', min: 1, max: 240, value: st.reloadWaitMinutes || 30, on: { input: e => (st.reloadWaitMinutes = Math.max(1, Math.min(240, +e.target.value || 30))) } });
  const general = () => [
    h('h3', { text: 'Starting YACM' }),
    check('Reopen the sessions that were open when YACM closed', reopen, 'Tabs, their order and the split view come back; the sessions connect once the vault is unlocked. After a crash YACM offers this even when it is off.'),
    about.store
      ? hint('Updates come from the Microsoft Store.')
      : check('Check for new versions once a day', upd, 'Off unless you switch it on. YACM asks GitHub (api.github.com) whether a newer release is out; only that question is sent, nothing about you or your sessions. YACM never updates itself: a store or package manager (Microsoft Store, winget, Homebrew, apt, dnf) updates it for you, otherwise download the new version. Settings > About checks by hand.'),
    h('h3', { text: 'Session names', style: { marginTop: '16px' } }),
    h('div', { class: 'field' }, h('span', { text: 'When the device prompt shows a different hostname' }), ren,
      h('span', { class: 'hint', text: 'After login YACM reads the hostname from the prompt (for example core-sw1# ) and offers to use it as the session name, handy after importing devices by IP address.' })),
  ];
  const terminal = () => [
    h('h3', { text: 'Terminal' }),
    field('Scrollback lines', sb, 'Lines each tab keeps to scroll back and search. Each session can set its own value.'),
    h('div', { class: 'field' }, h('span', { text: 'Page Up and Page Down' }), pk,
      h('span', { class: 'hint', text: 'Scrolling stops while a full-screen program runs on the device (less, vi, top): then the keys go to it. Shift+Page Up and Shift+Page Down always scroll.' })),
    h('h3', { text: 'Copy and paste', style: { marginTop: '16px' } }),
    check('Copy selected text automatically', cos),
    h('div', { class: 'field' }, h('span', { text: 'Right click in the terminal' }), rc,
      h('span', { class: 'hint', text: `The menu has Copy, Paste and Copy and paste, so nothing is sent by accident. With “Paste at once” a right click without a selection pastes straight away. ${IS_MAC ? '⌘V always pastes.' : 'Ctrl+Shift+V, Shift+Insert and Ctrl+V always paste.'}` })),
    field('Pause between pasted lines, in ms (0 = none)', pd, 'For slow consoles. Each session can set its own value. Pasting more than one line always asks first.'),
  ];
  const connections = () => [
    h('h3', { text: 'After a reload' }),
    check('Reconnect automatically after a reload', reloadOn, 'When a session drops after you typed reload, reboot, request system reboot, install … activate and the like, YACM tries the device every few seconds (through the jump host if there is one) and reconnects once it answers. Any session can also wait by hand: right click its tab > Wait for the device and reconnect.'),
    field('Stop waiting after this many minutes', reloadMax),
    h('h3', { text: 'Keepalive and anti-idle', style: { marginTop: '16px' } }),
    hint('Set per session (Edit > Connection) or for a whole group (Edit group > Defaults): SSH keepalives, and typing “!” while you are idle so the device does not log the session out.'),
  ];

  // ---- Highlighting ----
  st.highlightRules = structuredClone(st.highlightRules || []);
  const hlEd = highlightEditor(st);
  const hlOn = h('input', { type: 'checkbox', checked: st.highlight, on: { change: e => { st.highlight = e.target.checked; hlEd.renderPreview(); } } });
  const alertsOn = h('input', { type: 'checkbox', checked: !st.noAlerts, on: { change: e => (st.noAlerts = !e.target.checked) } });
  const alertSound = h('input', { type: 'checkbox', checked: !!st.alertSound, on: { change: e => (st.alertSound = e.target.checked) } });
  const highlighting = () => [
    h('h3', { text: 'Keyword highlighting' }),
    check('Colour keywords in terminal output', hlOn, 'down, err-disabled, syslog messages, IP and MAC addresses, interfaces. Can be switched off per session.'),
    check('Alert me when a rule marked “Alert” matches in a session I am not looking at', alertsOn, 'The tab gets a dot and a message names the session and the line (Show jumps to it); on Windows the taskbar button flashes while YACM is in the background. At most one alert per rule and session every 30 seconds, none while pasting. Alerts that arrive together share one message, which can also turn them off. The standard alert rules cover neighbours going down, err-disabled ports and reloads; links going down are only coloured. Can be switched off per session.'),
    check('Play a sound with an alert', alertSound),
    hlEd.el,
  ];

  // ---- Host keys ----
  const sys = h('input', { type: 'checkbox', checked: st.useSystemKnownHosts, on: { change: e => (st.useSystemKnownHosts = e.target.checked) } });
  const khBox = h('div', { class: 'kh-box' });
  const khFilter = h('input', { type: 'search', class: 'kh-filter', placeholder: 'Filter by host or fingerprint', spellcheck: false, on: { input: () => renderKH() } });
  const renderKH = () => {
    khFilter.hidden = !kh?.length || kh.length < 8;
    if (!kh?.length) { khBox.replaceChildren(h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'No host keys trusted yet. They are added the first time you accept a server’s key.' })); return; }
    const q = khFilter.value.trim().toLowerCase();
    const shown = kh.filter(e => !q || `${e.hosts} ${e.keyType} ${e.fingerprint}`.toLowerCase().includes(q));
    if (!shown.length) { khBox.replaceChildren(h('p', { class: 'hint', style: { color: 'var(--faint)' }, text: 'No trusted key matches.' })); return; }
    const t = h('table', { class: 'kh-table' });
    for (const e of shown) {
      t.append(h('tr', {},
        h('td', { text: e.marker ? `${e.marker} ${e.hosts}` : e.hosts }), h('td', { text: e.keyType }), h('td', { class: 'fp', text: e.fingerprint }),
        h('td', {}, e.marker ? null : h('button', { type: 'button', class: 'btn small', text: 'Forget', on: { click: async () => { await api.RemoveKnownHost(e.hosts.split(',')[0]); kh = await api.KnownHostEntries(); renderKH(); } } }))));
    }
    khBox.replaceChildren(t);
  };
  renderKH();
  const hostKeys = () => [
    h('h3', { id: 'settings-hostkeys', text: 'Trusted host keys', style: { marginTop: '16px' } }),
    hint('Keys you accepted when connecting. Forget a key when a device was replaced, so the next connection asks again.'),
    check('Also trust keys in ~/.ssh/known_hosts', sys, 'Read only. Keys you accept in YACM are stored in its own known_hosts file.'),
    khFilter, khBox,
  ];

  // ---- Security ----
  const lock = h('input', { type: 'number', min: 0, max: 600, value: st.autoLockMinutes || 0, on: { input: e => (st.autoLockMinutes = +e.target.value) } });
  const oldPw = h('input', { type: 'password', autocomplete: 'off' });
  const newPw = h('input', { type: 'password', autocomplete: 'new-password' });
  const newPw2 = h('input', { type: 'password', autocomplete: 'new-password' });
  const pwMsg = h('span', { class: 'hint' });
  const security = () => [
    h('h3', { text: 'Vault' }),
    field('Lock the vault after this many idle minutes (0 = never)', lock),
    h('h3', { text: 'Change master password', style: { marginTop: '16px' } }),
    field('Current master password', oldPw), h('div', { class: 'grid2' }, field('New master password', newPw), field('Repeat new password', newPw2)),
    h('div', { style: { display: 'flex', gap: '10px', alignItems: 'center' } },
      h('button', { type: 'button', class: 'btn small', text: 'Change password', on: { click: async () => {
        if (newPw.value !== newPw2.value) { pwMsg.textContent = 'The new passwords do not match.'; return; }
        try { await api.ChangeMasterPassword(oldPw.value, newPw.value); pwMsg.textContent = 'Master password changed.'; oldPw.value = newPw.value = newPw2.value = ''; } catch (e) { pwMsg.textContent = errMsg(e); }
      } } }), pwMsg),
    ...hostKeys(),
  ];

  // ---- Data and backups, session logs ----
  const logdir = h('input', { type: 'text', value: st.defaultLogDir || '', placeholder: `${about.dataDir || ''}/logs`, spellcheck: false, on: { input: e => (st.defaultLogDir = e.target.value.trim()) } });
  // ---- Backups ----
  const backupList = h('div', { class: 'list' });
  const fmtTime = s => new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  const renderBackups = async () => {
    const list = (await api.ListBackups().catch(() => [])) || [];
    backupList.replaceChildren();
    if (!list.length) { backupList.append(h('div', { class: 'empty-note', text: 'No backups yet. The first one is made the next time something is saved.' })); return; }
    for (const b of list) {
      backupList.append(h('div', { class: 'item' },
        h('div', { class: 'main' },
          h('div', { class: 'title', text: fmtTime(b.created) }),
          h('div', { class: 'meta', text: [b.reason === 'auto' ? 'automatic' : b.reason, plural(b.sessions, 'session', 'sessions'), b.groups ? plural(b.groups, 'group', 'groups') : ''].filter(Boolean).join(' · ') })),
        h('button', { type: 'button', class: 'btn small', text: 'Restore…', on: { click: () => restoreBackup(b) } })));
    }
  };
  const restoreBackup = async b => {
    const ok = await confirmDialog({
      title: `Restore the backup from ${fmtTime(b.created)}?`,
      message: `Sessions, groups, credential profiles, settings, saved passwords and trusted host keys go back to that moment (${plural(b.sessions, 'session', 'sessions')}). The current state is saved as a backup first, so this can be undone. YACM restarts; the vault then opens with the master password that was valid at that time.`,
      ok: 'Restore and restart', danger: true,
    });
    if (!ok) return;
    try { await api.RestoreBackup(b.id); } catch (e) { toast(errMsg(e), 'error'); }
  };
  const backups = () => {
    renderBackups();
    return [
      h('h3', { text: 'Backups', style: { marginTop: '18px' } }),
      hint('YACM copies sessions, profiles, settings, the vault and trusted host keys into the backups folder inside the data folder: automatically while you work (at most every 10 minutes) and before imports, deleting, editing many sessions at once and changing the master password. The newest 10 are kept, plus one from each of the last 14 days. The vault copies stay encrypted.'),
      backupList,
      h('div', { style: { display: 'flex', gap: '8px', marginTop: '8px' } },
        h('button', { type: 'button', class: 'btn small', text: 'Back up now', on: { click: async () => { try { await api.BackupNow(); toast('Backup made.'); renderBackups(); } catch (e) { toast(errMsg(e), 'error'); } } } }),
        h('button', { type: 'button', class: 'btn small', text: 'Open backups folder', on: { click: () => api.OpenBackupFolder().catch(e => toast(errMsg(e), 'error')) } })),
    ];
  };

  const maskLogs = h('input', { type: 'checkbox', checked: !st.logSecretsInClear, on: { change: e => (st.logSecretsInClear = !e.target.checked) } });
  const logNew = h('input', { type: 'checkbox', checked: !st.logNewOff, on: { change: e => (st.logNewOff = !e.target.checked) } });
  // Log file names
  const tplPreview = h('code', { class: 'log-preview' });
  const updatePreview = async () => { tplPreview.textContent = await api.LogNamePreview(st.logNameTemplate || '').catch(() => ''); };
  const tplInput = h('input', { type: 'text', value: st.logNameTemplate || '', placeholder: '{group}/{name}_{date}_{time}.log', spellcheck: false, class: 'mono-input', on: { input: e => { st.logNameTemplate = e.target.value; updatePreview(); } } });
  const tplBox = h('div', { class: 'sub-options' },
    field('File name template', tplInput, h('span', {}, 'Tokens: {name} {host} {protocol} {user} {group} {path} {date} {time} {ticket}. A “/” starts a subfolder; {path} makes one folder per group. Example session core-sw1 in DC / Zagreb: ', tplPreview)),
    check('Allow groups and sessions to use their own template', h('input', { type: 'checkbox', checked: !!st.logNameOwn, on: { change: e => (st.logNameOwn = e.target.checked) } }), 'Set under Edit group and Edit > Terminal and logs; empty there means this template.'));
  const tplOn = h('input', { type: 'checkbox', checked: !!st.logNameTemplateOn, on: { change: e => { st.logNameTemplateOn = e.target.checked; tplBox.hidden = !e.target.checked; } } });
  tplBox.hidden = !st.logNameTemplateOn;
  updatePreview();
  const ticket = seg([{ value: '', label: 'Never' }, { value: 'all', label: 'Every session with logging' }, { value: 'groups', label: 'Only in groups that ask' }], st.askTicket || '', v => (st.askTicket = v));
  const logs = () => [
    h('h3', { text: 'Session logs' }),
    h('label', { class: 'field mono' }, h('span', { text: 'Default folder for session logs' }), h('div', { class: 'with-btn' }, logdir, h('button', { type: 'button', class: 'btn small', text: 'Browse', on: { click: async () => { const p = await api.ChooseDirectory('Log folder'); if (p) { logdir.value = p; st.defaultLogDir = p; } } } })),
      h('span', { class: 'hint', text: 'Empty: the logs folder inside the data folder. Sessions can use their own folder.' })),
    check('Name log files with a template', tplOn, 'Off: core-sw1_20261028-133501.log in the log folder, as before. A file is never overwritten: the same name gets -2, -3 …'),
    tplBox,
    h('div', { class: 'field' }, h('span', { text: 'Ask for a change or ticket number when connecting' }), ticket,
      h('span', { class: 'hint', text: 'Only for sessions that write a log. The number goes into {ticket}, for example CHG0012345_core-sw1_2026-10-28.log; leave it empty and {ticket} is left out. “Only in groups that ask” is switched on per group under Edit group. Opening several sessions at once asks once.' })),
    check('Log new and imported sessions', logNew, 'New sessions and every import start with “Log to file” on; each session can turn it off (Edit > Terminal and logs). Existing sessions stay as they are.'),
    check('Hide passwords and keys in session logs', maskLogs, 'enable secret, username … password, SNMP communities, TACACS+/RADIUS keys, pre-shared keys, routing protocol keys, Junos $9$ values and private keys are written as ***** in the log file (the terminal shows them as they are). Log files are not encrypted.'),
  ];

  // ---- Integrations ----
  const nbURL = h('input', { type: 'text', value: st.netboxUrl || '', spellcheck: false, placeholder: 'https://netbox.example.com', on: { input: e => (st.netboxUrl = e.target.value.trim()) } });
  const nbToken = h('div');
  const renderToken = () => nbToken.replaceChildren(netbox.hasToken
    ? h('div', { style: { display: 'flex', gap: '10px', alignItems: 'center' } }, h('span', { class: 'pill ok', text: 'API token stored in the vault' }),
      h('button', { type: 'button', class: 'btn small', text: 'Forget token', on: { click: async () => { try { await api.ForgetNetBoxToken(); netbox.hasToken = false; renderToken(); toast('NetBox token removed.'); } catch (e) { toast(errMsg(e), 'error'); } } } }))
    : h('span', { class: 'hint', text: 'No API token stored. It is asked for (and can be saved) when you import from NetBox.' }));
  renderToken();
  const integrations = () => [
    h('h3', { text: 'NetBox' }),
    hint('Used by Import > From NetBox to create sessions from the devices and virtual machines in NetBox.'),
    field('NetBox address', nbURL), nbToken,
  ];

  // ---- About ----
  const aboutPane = () => [
    h('h3', { text: 'About YACM' }),
    h('div', { class: 'about-notice' },
      h('p', {}, h('strong', { text: 'YACM – Yet Another Connection Manager' })),
      h('p', { text: 'Copyright © 2026 The YACM Authors' }),
      h('p', {}, h('a', { href: '#', text: PROJECT_URL, on: { click: e => { e.preventDefault(); runtime.openURL(PROJECT_URL); } } })),
      h('p', { text: 'Free software under the GNU GPL version 3 or later, with additional terms (attribution, modified versions, name and logo). You may share and change it under these terms.' }),
      h('p', { text: 'YACM is free and stays free. If you would like it to keep improving and getting new features, you can support its development.' }),
      h('p', { text: 'This program comes with ABSOLUTELY NO WARRANTY. You use it at your own risk: it sends to your devices what you, your saved commands and your rules tell it to. The authors are not liable for any damage, as far as the law allows.' })),
    h('table', { class: 'kh-table' },
      ...[['YACM', about.app], ['Installed from', about.store ? 'Microsoft Store' : ''], ['Data folder', about.dataDir], ['Go', about.go], ['golang.org/x/crypto', about.xcrypto], ['Wails', about.wails], ['WebView2 loader', about.webview2]]
        .filter(([, v]) => v).map(([k, v]) => h('tr', {}, h('td', { text: k }), h('td', { class: 'fp', text: v })))),
    h('div', { style: { display: 'flex', gap: '8px', marginTop: '10px' } },
      h('button', { type: 'button', class: 'btn small', text: 'Licence and terms', on: { click: showLegal } }),
      h('button', { type: 'button', class: 'btn small', text: 'Support YACM', title: 'Ways to support YACM (GitHub Sponsors, Ko-fi); YACM stays free either way', on: { click: () => runtime.openURL(SUPPORT_URL) } }),
      about.store ? null : h('button', { type: 'button', class: 'btn small', text: 'Check for updates', on: { click: () => checkUpdates(true) } }),
      whatsNewFor(about.app).length ? h('button', { type: 'button', class: 'btn small', text: 'What’s new', on: { click: async () => { const { showWhatsNew } = await import('./guide.js'); showWhatsNew(about.app); } } }) : null,
      h('button', { type: 'button', class: 'btn small', text: 'Getting started', on: { click: async () => { const { startGuide } = await import('./guide.js'); const info = await api.GetStartInfo(); startGuide(info, async () => { await reload(); window.dispatchEvent(new Event('yacm:tree')); }); } } })),
    h('h3', { text: 'Reporting a problem', style: { marginTop: '16px' } }),
    hint('YACM keeps its own log (yacm.log): starts, failed connections and imports, errors. It never contains passwords or what the sessions show. Copy diagnostics puts the versions, a few settings and the end of the log on the clipboard, ready to paste into a message.'),
    h('div', { style: { display: 'flex', gap: '8px' } },
      h('button', { type: 'button', class: 'btn small', text: 'Copy diagnostics', on: { click: async () => {
        try {
          const text = await api.Diagnostics(navigator.userAgent);
          await runtime.copy(text);
          toast('Diagnostics copied to the clipboard.');
        } catch (e) { toast(errMsg(e), 'error'); }
      } } }),
      h('button', { type: 'button', class: 'btn small', text: 'Open log folder', on: { click: () => api.OpenLogFolder().catch(e => toast(errMsg(e), 'error')) } })),
  ];

  const ctx = { st, about, hint, toast };
  const sections = [
    { id: 'general', label: 'General', render: general },
    { id: 'appearance', label: 'Appearance', render: appearance },
    { id: 'terminal', label: 'Terminal', render: terminal },
    { id: 'connections', label: 'Connections', render: connections },
    { id: 'highlight', label: 'Highlighting', render: highlighting },
    { id: 'logs', label: 'Session logs', render: logs },
    { id: 'security', label: 'Security', render: security },
    { id: 'data', label: 'Data and backups', render: () => [...(extraSections.find(x => x.id === 'data')?.render(ctx) || []), ...backups()] },
    ...extraSections.filter(x => x.id !== 'data').map(x => ({ id: x.id, label: x.label, render: () => x.render(ctx) })),
    { id: 'integrations', label: 'Integrations', render: integrations },
    { id: 'about', label: 'About', render: aboutPane },
  ];

  // Every pane is built once, so values survive switching tabs.
  const nav = h('nav');
  const panes = h('div', { class: 'pane' });
  const built = sections.map(sec => {
    const el = h('div', { class: 'settings-pane', hidden: true }, ...[sec.render()].flat());
    panes.append(el);
    const btn = h('button', { type: 'button', text: sec.label, on: { click: () => show(sec.id) } });
    nav.append(btn);
    return { ...sec, el, btn };
  });
  const show = id => {
    const cur = built.find(b => b.id === (id === 'hostkeys' ? 'security' : id)) || built[0];
    for (const b of built) { b.el.hidden = b !== cur; b.btn.classList.toggle('on', b === cur); b.btn.classList.remove('has-error'); }
    panes.scrollTop = 0;
    rememberTab(cur.id);
  };
  show(startTab || lastTab());
  if (startTab === 'hostkeys') requestAnimationFrame(() => panes.querySelector('#settings-hostkeys')?.scrollIntoView());

  openDialog({
    title: 'Settings', body: h('div', { class: 'editor' }, nav, panes), wide: true,
    buttons: [{ label: 'Cancel' }, { label: 'Save settings', kind: 'primary', default: true, onClick: async () => {
      st.highlightRules = st.highlightRules.filter(r => r.pattern.trim());
      const { errors } = compileRules(st.highlightRules);
      const bad = Object.keys(errors).map(Number);
      if (bad.length) {
        show('highlight');
        built.find(b => b.id === 'highlight').btn.classList.add('has-error');
        throw new Error(`Highlight rule ${bad[0] + 1}: ${errors[bad[0]]}`);
      }
      // Team libraries change through their own buttons while this is open.
      st.teamLibraries = structuredClone(state.settings.teamLibraries || []);
      if (!(await askTerminalColours(st))) return false;
      await api.SaveSettings(st);
      await reload();
      onApply && onApply();
    } }],
  });
}

// askTerminalColours runs before Settings are saved. When the default
// terminal colours change and sessions use them, it asks whether those
// sessions follow the change or keep looking as before (their current colour
// is then written into them). Resolves false to cancel saving.
async function askTerminalColours(st) {
  const norm = v => (/^#[0-9a-f]{6}$/i.test(v || '') ? v.toLowerCase() : '');
  const keys = ['termText', 'termBackground'].filter(k => norm(st[k]) !== norm(state.settings[k]));
  if (!keys.length) return true;
  const own = state.nodes.filter(n => !n.isGroup && !n.library && KNOWN_PROTOCOLS.includes(n.session?.protocol));
  const using = Object.fromEntries(keys.map(k => [k, own.filter(n => termResolve(n, k).from === 'settings').map(n => n.id)]));
  const count = new Set(keys.flatMap(k => using[k])).size;
  if (!count) return true;
  const lib = state.nodes.filter(n => !n.isGroup && n.library && keys.some(k => termResolve(n, k).from === 'settings')).length;
  const choice = await new Promise(resolve => {
    openDialog({
      title: 'Apply the new colours to existing sessions?',
      body: h('div', {},
        h('p', { text: `${plural(count, 'existing session uses', 'existing sessions use')} the default terminal colours. They can switch to the new colours now, or keep the colours they have and leave the new ones to sessions you add from now on.` }),
        lib ? h('p', { class: 'hint', text: `${plural(lib, 'team library session follows', 'team library sessions follow')} the default either way (they are read-only here).` }) : null),
      buttons: [
        { label: 'Cancel', value: null },
        { label: 'Only new sessions', value: 'new' },
        { label: 'Apply to existing sessions', kind: 'primary', default: true, value: 'all' },
      ],
      onClose: v => resolve(v || null),
    });
  });
  if (!choice) return false;
  if (choice === 'new') {
    for (const k of keys) {
      if (!using[k].length) continue;
      await api.BulkUpdate({ ids: using[k], change: { [k]: norm(state.settings[k]) || 'theme' }, password: null });
    }
  }
  return true;
}

// The YACM mark: a network jack whose contacts use the protocol colours.
const logoSVG = () => {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '40 56 176 144');
  svg.setAttribute('class', 'logo');
  svg.setAttribute('aria-hidden', 'true');
  const add = (d, attrs) => {
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', d);
    for (const [k, v] of Object.entries(attrs)) p.setAttribute(k, v);
    svg.append(p);
  };
  add('M52 68 H204 V164 H160 V188 H96 V164 H52 Z', { class: 'jack' });
  add('M92 88 V130', { class: 'pin', stroke: 'var(--ssh)' });
  add('M128 88 V130', { class: 'pin', stroke: 'var(--telnet)' });
  add('M164 88 V130', { class: 'pin', stroke: 'var(--serial)' });
  return svg;
};

// Lock / first-run screen. Resolves when the user is through. idle: shown by
// the auto-lock, which locks saved passwords, not the open sessions.
export function lockScreen({ idle = false } = {}) {
  return new Promise(resolve => {
    const creating = !state.vault.exists;
    const pw = h('input', { type: 'password', autocomplete: creating ? 'new-password' : 'current-password' });
    const pw2 = h('input', { type: 'password', autocomplete: 'new-password' });
    const err = h('div', { class: 'error-line', role: 'alert' });
    const go = h('button', { class: 'btn primary', text: creating ? 'Create vault' : 'Unlock' });
    const skip = h('button', { class: 'text-btn skip', text: creating ? 'Set up later' : 'Continue without passwords' });
    const el = h('div', { class: 'lock' },
      h('h1', {}, logoSVG(), h('span', {}, 'YACM', h('small', { text: 'Yet Another Connection Manager' }))),
      h('p', { text: creating
        ? 'Choose a master password. It encrypts every saved password and passphrase (Argon2id and AES-256-GCM) and is never stored anywhere. If you lose it, the saved passwords cannot be recovered.'
        : idle
          ? 'Saved passwords were locked after inactivity. Open sessions stay connected: lock your computer\'s screen when you leave it. Enter your master password to unlock saved passwords.'
          : 'Enter your master password to unlock saved passwords.' }),
      field(creating ? 'Master password (at least 12 characters)' : 'Master password', pw),
      creating ? field('Repeat master password', pw2) : null,
      err,
      h('div', { class: 'actions' }, go, skip));
    const submit = async () => {
      err.textContent = '';
      go.disabled = true;
      try {
        if (creating) {
          if (pw.value !== pw2.value) throw new Error('The passwords do not match.');
          await api.CreateVault(pw.value);
        } else {
          await api.UnlockVault(pw.value);
        }
        state.vault = await api.GetVaultStatus();
        await reload().catch(() => {}); // profiles now show which passwords are stored
        el.remove();
        resolve(true);
      } catch (e) {
        err.textContent = errMsg(e);
        pw.select();
      } finally { go.disabled = false; }
    };
    go.addEventListener('click', submit);
    el.addEventListener('keydown', e => { if (e.key === 'Enter') submit(); });
    skip.addEventListener('click', () => { el.remove(); resolve(false); });
    document.body.append(el);
    pw.focus();
  });
}
