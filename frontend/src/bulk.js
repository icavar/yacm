// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Edit many sessions at once: tick the settings to change; everything else
// stays as it is in each session.
import { api, state, reload, groupsFlat, COLORS, COLOR_LABELS } from './api.js';
import { h, openDialog, toast, colorPicker, plural } from './ui.js';
import { openProfiles } from './profiles.js';
import { sessionPicker } from './picker.js';
import { picker } from './termcolors.js';

export async function editMany(ids) {
  const sessionIds = (await api.SessionsUnder(ids)) || [];
  const groups = ids.filter(id => state.byId.get(id)?.isGroup);
  if (!sessionIds.length && !groups.length) { toast('Nothing to edit.'); return; }
  const names = ids.map(id => state.byId.get(id)?.name).filter(Boolean);
  const vaultOK = state.vault.unlocked;

  const rows = [];
  const row = (label, control, opts = {}) => {
    const cb = h('input', { type: 'checkbox', 'aria-label': `Change ${label}` });
    const name = h('span', { text: label });
    const r = h('div', { class: 'bulk-row off' }, cb, name, h('div', { class: 'ctl' }, control, opts.hint ? h('div', { class: 'inherit-note', text: opts.hint }) : null));
    cb.addEventListener('change', () => r.classList.toggle('off', !cb.checked));
    name.addEventListener('click', () => { cb.checked = !cb.checked; r.classList.toggle('off', !cb.checked); });
    // Touching a control ticks its box.
    const tick = () => { cb.checked = true; r.classList.remove('off'); };
    const ctl = r.querySelector('.ctl');
    ctl.addEventListener('input', tick, true);
    ctl.addEventListener('change', tick, true);
    ctl.addEventListener('click', e => { if (e.target.closest('.swatches button')) tick(); }, true);
    rows.push({ cb, ...opts });
    return r;
  };
  const sel = (options, value = '') => {
    const s = h('select', {}, ...options.map(([v, t]) => (v === '--' ? h('option', { disabled: true, text: t }) : h('option', { value: v, text: t }))));
    s.value = value;
    return s;
  };

  const cred = sel([['', 'Each session’s own login'], ['inherit', 'From group'], ...(state.credentials.length ? [['--', '── Credential profiles ──']] : []), ...state.credentials.map(c => [c.id, `${c.name}${c.user ? ` (${c.user})` : ''}`])], state.credentials[0]?.id || 'inherit');
  const user = h('input', { type: 'text', spellcheck: false, placeholder: 'User name for sessions with their own login' });
  const pw = h('input', { type: 'password', autocomplete: 'off', placeholder: vaultOK ? 'Stored in each session' : 'Unlock the vault to set passwords', disabled: !vaultOK });
  const jump = sessionPicker({ value: 'inherit', nodes: () => state.nodes.filter(n => n.session?.protocol === 'ssh'), specials: [['', 'Connect directly'], ['inherit', 'From group']], label: 'Jump host' });
  const algo = sel([['inherit', 'From group'], ['secure', 'Secure'], ['compatible', 'Compatible with older devices'], ['legacy', 'Legacy']], 'compatible');
  const port = h('input', { type: 'number', min: 1, max: 65535, placeholder: '22' });
  let color = '';
  const colorCtl = colorPicker('', v => (color = v), { colors: COLORS, labels: COLOR_LABELS });
  const parent = sel([['', 'Top level'], ...groupsFlat().filter(g => !ids.includes(g.id)).map(g => [g.id, '  '.repeat(g.depth + 1) + g.name])], '');
  const addCl = h('input', { type: 'text', spellcheck: false, placeholder: 'CORE, EDGE' });
  const rmCl = h('input', { type: 'text', spellcheck: false, placeholder: 'OLD' });
  const yesNo = v => sel([['yes', 'On'], ['no', 'Off']], v);
  const log = yesNo('yes');
  const stamp = yesNo('yes');
  const hl = yesNo('yes');
  const font = h('input', { type: 'number', min: 0, max: 40, placeholder: `Default (${state.settings.fontSize})` });
  const delay = h('input', { type: 'number', min: 0, max: 10000, step: 50, placeholder: `Default (${state.settings.pasteDelay || 0} ms)` });
  let termText = '';
  let termBg = '';
  const textCtl = picker('', v => (termText = v), { kind: 'text', inheritText: 'Default' });
  const bgCtl = picker('', v => (termBg = v), { kind: 'bg', inheritText: 'Default' });
  const idleMode = sel([['inherit', 'From group'], ['off', 'Off'], ['on', 'Send text when idle']], 'on');
  const idleSecs = h('input', { type: 'number', min: 10, max: 3600, step: 10, value: 240, style: { width: '90px' } });
  const idleText = h('input', { type: 'text', maxLength: 64, spellcheck: false, placeholder: '!', class: 'mono-input', style: { width: '120px' } });
  const idleCtl = h('div', { style: { display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap' } }, idleMode, h('span', { text: 'after' }), idleSecs, h('span', { text: 's, text' }), idleText);

  const manage = h('button', { type: 'button', class: 'btn small', text: 'Profiles…', style: { width: 'auto', marginTop: '6px' }, on: { click: () => openProfiles(() => {
    const v = cred.value;
    cred.replaceChildren(...sel([['', 'Each session’s own login'], ['inherit', 'From group'], ...state.credentials.map(c => [c.id, `${c.name}${c.user ? ` (${c.user})` : ''}`])]).childNodes);
    cred.value = v;
  }) } });

  const R = {
    cred: row('Login', h('div', {}, cred, manage), { hint: 'Pick a profile so a password change later is done in one place.' }),
    user: row('User name', user, { hint: 'Only used by sessions with their own login.' }),
    pw: row('Password', pw, { hint: 'Stored in each session separately. A credential profile is easier to change later.' }),
    jump: row('Jump host', jump, { hint: 'SSH and telnet sessions.' }),
    algo: row('SSH algorithms', algo),
    port: row('Port', port, { hint: 'SSH and telnet sessions.' }),
    color: row('Colour', colorCtl, { hint: 'Set on the selected groups and sessions; sessions inside a group take its colour.' }),
    parent: row('Move to group', parent),
    addCl: row('Add tags', addCl),
    rmCl: row('Remove tags', rmCl),
    log: row('Log to file', log),
    stamp: row('Time in log lines', stamp),
    hl: row('Highlight keywords', hl),
    font: row('Font size', font),
    termText: row('Text colour', textCtl, { hint: '“Default” takes it from the group or Settings.' }),
    termBg: row('Background colour', bgCtl),
    delay: row('Pause between pasted lines (ms)', delay),
    idle: row('Anti-idle', idleCtl, { hint: 'Types the text (no Enter; {enter} adds one) every so many seconds without typing, only at a CLI prompt, and erases it before your next key. Empty text: “!” (or the group’s).' }),
  };
  const ticked = key => R[key].querySelector('input[type=checkbox]').checked;

  openDialog({
    title: `Edit ${plural(sessionIds.length, 'session', 'sessions')} together`,
    sub: names.length <= 4 ? names.join(', ') : `${names.slice(0, 3).join(', ')} and ${names.length - 3} more`,
    medium: true,
    body: h('div', {},
      h('p', { class: 'hint', style: { color: 'var(--muted)', margin: '0 0 8px' }, text: 'Tick what to change. Everything else stays as it is in each session.' }),
      ...Object.values(R)),
    buttons: [
      { label: 'Cancel' },
      { label: 'Apply', kind: 'primary', default: true, onClick: async api_ => {
        const change = {};
        let password = null;
        if (ticked('cred')) change.credentialId = cred.value;
        if (ticked('user')) change.user = user.value.trim();
        if (ticked('pw')) password = pw.value;
        if (ticked('jump')) { change.jumpVia = jump.value; change.clearJumpHosts = jump.value === ''; }
        if (ticked('algo')) change.algorithms = algo.value;
        if (ticked('port')) { if (!+port.value) throw new Error('Enter a port number.'); change.port = +port.value; }
        if (ticked('color')) change.color = color;
        if (ticked('parent')) change.parentId = parent.value;
        if (ticked('addCl')) change.addClusters = addCl.value.split(',').map(x => x.trim()).filter(Boolean);
        if (ticked('rmCl')) change.removeClusters = rmCl.value.split(',').map(x => x.trim()).filter(Boolean);
        if (ticked('log')) change.logToFile = log.value === 'yes';
        if (ticked('stamp')) change.logTimestamps = stamp.value === 'yes';
        if (ticked('hl')) change.noHighlight = hl.value !== 'yes';
        if (ticked('font')) change.fontSize = +font.value || 0;
        if (ticked('termText')) change.termText = termText;
        if (ticked('termBg')) change.termBackground = termBg;
        if (ticked('delay')) change.pasteDelay = delay.value === '' ? 0 : +delay.value === 0 ? -1 : +delay.value;
        if (ticked('idle')) {
          change.antiIdle = idleMode.value === 'on' ? (+idleSecs.value || 0) : idleMode.value === 'off' ? -1 : 0;
          change.antiIdleText = idleMode.value === 'on' ? idleText.value.trim() : '';
        }
        if (!Object.keys(change).length && password === null) throw new Error('Tick at least one setting to change.');
        const n = await api.BulkUpdate({ ids, change, password });
        await reload();
        window.dispatchEvent(new Event('yacm:tree'));
        toast(`Changed ${plural(n, 'session', 'sessions')}.`);
      } },
    ],
  });
}
