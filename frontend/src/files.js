// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// File transfer: the Files tab (this computer on the left, the device on
// the right), Send / Get file and command output to a file from a terminal tab, and
// the list of transfers. The backend uses SFTP when the device has it, else
// SCP with exec commands (dir, verify /md5, delete) as on Cisco IOS.
import { api, on, state, isNewerType, newerTypeText } from './api.js';
import { h, toast, errMsg, confirmDialog, openDialog, field, check, contextMenu, icon, plural } from './ui.js';
import { addPaneTab, paneShell, paintPaneLabel, refreshTab, tabById, terminalTabFor, activate } from './terms.js';

// ---- helpers ------------------------------------------------------------------

const LS = {
  get(k, d = '') { try { return localStorage.getItem(`yacm.files.${k}`) ?? d; } catch { return d; } },
  set(k, v) { try { localStorage.setItem(`yacm.files.${k}`, v); } catch { /* storage off */ } },
};

export function fmtBytes(n) {
  if (n === undefined || n === null || n < 0) return '';
  if (n < 1024) return `${n} B`;
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}

function fmtDate(iso) {
  if (!iso || iso.startsWith('0001')) return '';
  const d = new Date(iso);
  if (isNaN(+d)) return '';
  const p = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

const isDevicePath = p => { const i = p.indexOf(':'); return i > 0 && !p.slice(0, i).includes('/') && !/^[A-Za-z]$/.test(p.slice(0, i)); };

// rjoin and rparent work on device paths (flash:dir/file) and Unix paths.
// deviceNames asks before files whose names have spaces go to a network
// device, which would end the name at the first space (scp -t flash:a b.txt
// saves "a"). Resolves to the names to use, or null when cancelled.
/** @param {string} dir @param {string[]} names @returns {Promise<string[] | null>} */
export async function deviceNames(dir, names) {
  if (!isDevicePath(dir) || !names.some(n => /\s/.test(n))) return names;
  const fixed = names.map(n => n.replace(/\s/g, '_'));
  const list = names.map((n, i) => (n === fixed[i] ? null : `${n} → ${fixed[i]}`)).filter(Boolean).join('\n');
  const ok = await confirmDialog({
    title: 'Spaces in file names',
    message: h('div', {}, h('p', { text: 'Network devices end a file name at the first space. Send with underscores instead?' }), h('pre', { class: 'hostkey', text: list })),
    ok: 'Send with underscores',
  });
  return ok ? fixed : null;
}

export function rjoin(dir, name) {
  if (!dir) return name;
  if (isDevicePath(dir)) return /[:/]$/.test(dir) ? dir + name : `${dir}/${name}`;
  return `${dir.replace(/\/+$/, '')}/${name}`;
}

export function rparent(p) {
  if (isDevicePath(p)) {
    const i = p.indexOf(':');
    const rest = p.slice(i + 1).replace(/^\/+|\/+$/g, '');
    const j = rest.lastIndexOf('/');
    return p.slice(0, i + 1) + (j >= 0 ? rest.slice(0, j) : '');
  }
  if (!p || p === '/') return '/';
  const q = p.replace(/\/+$/, '');
  const j = q.lastIndexOf('/');
  return j <= 0 ? '/' : q.slice(0, j);
}

const baseName = p => p.split(/[\\/:]/).filter(Boolean).pop() || p;
const dirName = p => { const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\')); return i > 0 ? p.slice(0, i) : p; };
const today = () => { const d = new Date(); const p = n => String(n).padStart(2, '0'); return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`; };
const safeName = s => s.replace(/[^\p{L}\p{N}._-]+/gu, '_').replace(/^_+|_+$/g, '').slice(0, 80) || 'device';

// ---- transfers ----------------------------------------------------------------

/** @type {Map<string, any>} */
const transfers = new Map();
const listeners = new Set();
let panelIds = new Set(); // transfers started from terminal tabs, shown in the floating panel

export function onTransfer(fn) { listeners.add(fn); return () => listeners.delete(fn); }

export function initFiles() {
  on('xfer:progress', ev => {
    const prev = transfers.get(ev.id);
    const now = Date.now();
    const x = { ...ev, started: prev?.started || now, at: now };
    // Speed over the last few seconds.
    if (prev && ev.done > prev.done) x.rate = ((ev.done - prev.done) / Math.max(1, now - prev.at)) * 1000 * 0.5 + (prev.rate || 0) * 0.5;
    else x.rate = prev?.rate || 0;
    transfers.set(ev.id, x);
    listeners.forEach(f => f(x));
    if (panelIds.has(ev.id)) renderPanel();
    if (prev?.state !== ev.state && !active(ev)) finished(x);
  });
  on('tab:status', st => {
    if (st.state !== 'connected') dropTabConn(st.tabId);
  });
}

// finished: transfers in the floating panel show their result there; the
// window asks for attention when YACM is in the background.
function finished(x) {
  if (panelIds.has(x.id) && !document.hasFocus()) api.RequestAttention().catch(() => {});
}

// active: a transfer that is not finished (queued ones wait for a device
// that takes one channel at a time).
const active = x => x.state === 'running' || x.state === 'verifying' || x.state === 'queued';

function stateText(x) {
  switch (x.state) {
    case 'queued': return 'queued';
    case 'running': return x.total ? `${Math.floor((x.done / x.total) * 100)} %` : fmtBytes(x.done);
    case 'verifying': return 'checking MD5…';
    case 'done': return x.verified === 'ok' ? 'done, MD5 OK' : x.verified === 'unavailable' ? 'done (MD5 not available)' : 'done';
    case 'cancelled': return 'cancelled';
    default: return 'failed';
  }
}

// transferRow draws one transfer (in a Files tab or the floating panel).
function transferRow(x) {
  const pct = x.total ? Math.min(100, (x.done / x.total) * 100) : x.state === 'done' ? 100 : 0;
  const running = active(x);
  const queued = x.state === 'queued';
  const arrow = x.dir === 'up' ? '↑' : '↓';
  const detail = queued ? 'waits for the transfer before it (the device takes one at a time)' : running
    ? `${fmtBytes(x.done)}${x.total ? ` of ${fmtBytes(x.total)}` : ''}${x.rate > 0 && x.state === 'running' ? ` · ${fmtBytes(x.rate)}/s` : ''}`
    : x.state === 'error' ? x.error : x.dir === 'up' ? `to ${x.remote}` : `to ${x.local}`;
  return h('div', { class: `xfer ${x.state}`, title: `${x.dir === 'up' ? `${x.local} → ${x.remote}` : x.dir === 'down' ? `${x.remote} → ${x.local}` : `${x.remote} → ${x.local}`}${x.md5 ? `\nMD5 ${x.md5}` : ''}` },
    h('span', { class: 'dir', text: x.dir === 'cmd' ? '⌘' : arrow, 'aria-label': x.dir === 'up' ? 'Upload' : 'Download' }),
    h('div', { class: 'what' },
      h('div', { class: 'line' }, h('span', { class: 'name', text: x.name }), h('span', { class: 'st', text: stateText(x) })),
      h('div', { class: `progress${x.state === 'verifying' ? ' indeterminate' : ''}` }, h('div', { class: 'fill', style: { width: `${pct}%` } })),
      h('div', { class: 'detail', text: detail })),
    running
      ? h('button', { class: 'text-btn', text: 'Cancel', on: { click: () => api.TransferCancel(x.id) } })
      : x.state === 'done' && x.dir !== 'up'
        ? h('button', { class: 'text-btn', text: 'Show', title: 'Show the file in the file manager', on: { click: () => api.RevealFile(x.local).catch(e => toast(errMsg(e), 'error')) } })
        : null);
}

// The floating panel lists transfers started from terminal tabs. Hide folds
// it into a small bar (also while transfers run); Clear finished drops the
// ones that are done.
let panel = null;
let panelFolded = false;
function renderPanel() {
  const list = [...panelIds].map(id => transfers.get(id)).filter(Boolean);
  if (!list.length) { panel?.remove(); panel = null; panelFolded = false; return; }
  if (!panel) {
    panel = h('div', { class: 'xfer-panel', role: 'region', 'aria-label': 'File transfers' });
    document.body.append(panel);
  }
  const running = list.filter(x => active(x)).length;
  const finished = list.length - running;
  const title = running ? `Transfers: ${running} running` : 'Transfers: done';
  panel.classList.toggle('folded', panelFolded);
  if (panelFolded) {
    panel.replaceChildren(h('button', { class: 'xfer-fold', title: 'Show the transfers', on: { click: () => { panelFolded = false; renderPanel(); } } }, h('b', { text: title }), h('span', { text: '▴' })));
    return;
  }
  panel.replaceChildren(
    h('div', { class: 'head' }, h('b', { text: running ? title : 'Transfers' }),
      finished && running ? h('button', { class: 'text-btn', text: 'Clear finished', on: { click: () => {
        panelIds = new Set([...panelIds].filter(id => { const x = transfers.get(id); return x && active(x); }));
        renderPanel();
      } } }) : null,
      running
        ? h('button', { class: 'text-btn', text: 'Hide', title: 'Fold the list into a small bar; the transfers go on', on: { click: () => { panelFolded = true; renderPanel(); } } })
        : h('button', { class: 'text-btn', text: 'Close', on: { click: () => { panelIds = new Set(); renderPanel(); } } })),
    h('div', { class: 'rows' }, ...list.slice(-8).reverse().map(transferRow)));
}

function track(id) { panelIds.add(id); renderPanel(); }

// ---- connections of terminal tabs ------------------------------------------

/** @type {Map<string, Promise<any>>} */
const tabConns = new Map();

function dropTabConn(tabId) {
  const p = tabConns.get(tabId);
  if (!p) return;
  tabConns.delete(tabId);
  p.then(info => api.FilesClose(info.id)).catch(() => {});
}

// tabConn opens the file system of a terminal tab over its own SSH
// connection (no second login).
function tabConn(tabId) {
  let p = tabConns.get(tabId);
  if (!p) {
    p = api.FilesOpen({ nodeId: '', tabId, ownerId: tabId, scp: false });
    tabConns.set(tabId, p);
    p.catch(() => tabConns.delete(tabId));
  }
  return p;
}

function termTab(tabId) {
  const t = tabById(tabId);
  if (!t || t.kind !== 'terminal') throw new Error('The session tab is closed.');
  if (t.state !== 'connected') throw new Error(`${t.node.name} is not connected.`);
  return t;
}

const remoteDirKey = node => `remoteDir.${node.id}`;

// ---- Send file ----------------------------------------------------------------

export async function sendFile(tabId) {
  const t = termTab(tabId);
  const paths = await api.ChooseFiles(`Send files to ${t.node.name}`, LS.get('localDir'));
  if (!paths?.length) return;
  LS.set('localDir', dirName(paths[0]));
  const info = await tabConn(tabId);
  const dir = h('input', { type: 'text', value: LS.get(remoteDirKey(t.node), info.home), spellcheck: false });
  const free = h('span', { class: 'hint' });
  const verify = h('input', { type: 'checkbox', checked: LS.get('verify', '1') === '1' });
  const showFree = async () => {
    free.textContent = '';
    const n = await api.FilesFree(info.id, dir.value.trim()).catch(() => -1);
    if (n >= 0) free.textContent = `${fmtBytes(n)} free`;
  };
  showFree();
  dir.addEventListener('change', showFree);
  const roots = (info.roots || []).length > 1 ? h('div', { class: 'chips' }, ...info.roots.map(r => h('button', { type: 'button', class: 'chip', text: r.path, on: { click: () => { dir.value = r.path; showFree(); } } }))) : null;
  openDialog({
    title: `Send ${paths.length === 1 ? baseName(paths[0]) : plural(paths.length, 'file', 'files')} to ${t.node.name}`,
    sub: `${info.kind.toUpperCase()} · ${info.summary}`,
    body: h('div', {},
      h('pre', { class: 'hostkey', style: { maxHeight: '120px', overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }, text: paths.join('\n') }),
      field('Folder on the device', dir, info.kind === 'scp' ? 'For example flash: or bootflash:images' : 'For example /tmp or your home folder'),
      roots, free,
      check('Check the MD5 checksum on the device afterwards', verify, info.kind === 'scp' ? 'verify /md5 (Cisco), md5sum, file checksum md5 (Junos)' : 'md5sum on the device')),
    buttons: [{ label: 'Cancel' }, { label: 'Send', kind: 'primary', default: true, onClick: async () => {
      const d = dir.value.trim() || info.home;
      LS.set(remoteDirKey(t.node), d);
      LS.set('verify', verify.checked ? '1' : '0');
      const names = await deviceNames(d, paths.map(baseName));
      if (!names) return;
      paths.forEach((p, i) => {
        api.FilesUpload(info.id, p, rjoin(d, names[i]), verify.checked).then(track).catch(e => toast(errMsg(e), 'error'));
      });
    } }],
    initialFocus: dir,
  });
}

// ---- Get file -----------------------------------------------------------------

export async function getFile(tabId) {
  const t = termTab(tabId);
  const info = await tabConn(tabId);
  const pane = new Pane(remoteAdapter(info), { title: t.node.name, roots: info.roots, onOpen: () => getBtn() });
  let getBtn = () => {};
  const verify = h('input', { type: 'checkbox', checked: LS.get('verifyDown', '0') === '1' });
  const dlg = openDialog({
    title: `Get a file from ${t.node.name}`,
    sub: `${info.kind.toUpperCase()} · ${info.summary}`,
    wide: true,
    body: h('div', { class: 'files-dialog' }, pane.el,
      check('Compare MD5 checksums afterwards', verify, 'Asks the device for the checksum (verify /md5 on Cisco); takes a while for large images')),
    buttons: [{ label: 'Cancel' }, { label: 'Save to this computer…', kind: 'primary', onClick: () => { getBtn(); return false; } }],
  });
  getBtn = async () => {
    const files = pane.selected().filter(e => !e.dir);
    if (!files.length) { dlg.setError('Choose a file (double-click a folder to open it).'); return; }
    LS.set('verifyDown', verify.checked ? '1' : '0');
    LS.set(remoteDirKey(t.node), pane.dir);
    let targets;
    if (files.length === 1) {
      const dest = await api.ChooseSaveAs(`Save ${files[0].name}`, LS.get('localDir'), files[0].name);
      if (!dest) return;
      targets = [[files[0], dest]];
      LS.set('localDir', dirName(dest));
    } else {
      const folder = await api.ChooseFolder(`Save ${files.length} files into`, LS.get('localDir'));
      if (!folder) return;
      LS.set('localDir', folder);
      try {
        targets = await Promise.all(files.map(async f => [f, await api.LocalJoin(folder, f.name)]));
      } catch (e) { toast(errMsg(e), 'error'); return; }
      const existing = [];
      for (const [, d] of targets) if (await api.LocalExists(d)) existing.push(baseName(d));
      if (existing.length && !(await confirmDialog({ title: 'Replace files?', message: `${existing.join(', ')} already ${existing.length === 1 ? 'exists' : 'exist'} in ${folder}.`, ok: 'Replace', danger: true }))) return;
    }
    dlg.close();
    for (const [f, dest] of targets) api.FilesDownload(info.id, f.path, dest, f.size, verify.checked).then(track).catch(e => toast(errMsg(e), 'error'));
  };
  pane.go(LS.get(remoteDirKey(t.node), info.home));
}

// pickDeviceFile lets the user choose a file on the device of a terminal tab
// (its file systems over SSH, as in Get file) and returns its path, such as
// flash:c8000v.bin, or '' when cancelled.
/** @param {string} tabId @returns {Promise<string>} */
export async function pickDeviceFile(tabId) {
  const t = termTab(tabId);
  const info = await tabConn(tabId);
  return new Promise(resolve => {
    let done = false;
    const choose = () => {
      const f = pane.selected().find(e => !e.dir);
      if (!f) { dlg.setError('Choose a file (double-click a folder to open it).'); return false; }
      LS.set(remoteDirKey(t.node), pane.dir);
      done = true;
      resolve(f.path);
      dlg.close();
      return false;
    };
    const pane = new Pane(remoteAdapter(info), { title: t.node.name, roots: info.roots, onOpen: () => choose() });
    const dlg = openDialog({
      title: `Choose a file on ${t.node.name}`,
      sub: `${info.kind.toUpperCase()} · ${info.summary}`,
      wide: true,
      body: h('div', { class: 'files-dialog' }, pane.el),
      buttons: [{ label: 'Cancel' }, { label: 'Choose', kind: 'primary', onClick: choose }],
      onClose: () => { if (!done) resolve(''); },
    });
    pane.go(LS.get(remoteDirKey(t.node), info.home));
  });
}

// ---- command output to a file -------------------------------------------------

function hostOf(t) { return safeName(t.hostname || t.node.name); }

async function runToFile(t, command, name) {
  const dest = await api.ChooseSaveAs(`Save the output of “${command}”`, LS.get('outputDir', LS.get('localDir')), name);
  if (!dest) return;
  LS.set('outputDir', dirName(dest));
  track(await api.SessionCommandToFile(t.id, command, dest));
}

// Commands people save often, by platform. Tech support first.
/** @type {[string, string[]][]} */
const COMMON_COMMANDS = [
  ['Cisco IOS, IOS XE, NX-OS, ASA', ['show tech-support', 'show running-config', 'show version', 'show logging', 'show interfaces', 'show ip route']],
  ['Cisco IOS XR', ['show running-config', 'show version', 'show logging', 'show interfaces', 'show route']],
  ['Arista EOS', ['show tech-support', 'show running-config', 'show version', 'show logging', 'show interfaces']],
  ['Juniper Junos', ['request support information | no-more', 'show configuration | display set | no-more', 'show version | no-more', 'show log messages | no-more', 'show interfaces | no-more']],
  ['Huawei VRP', ['display diagnostic-information', 'display current-configuration', 'display version', 'display logbuffer', 'display interface']],
  ['Linux', ['ip addr', 'ip route', 'journalctl -n 1000 --no-pager', 'dmesg']],
];

// commandToFile types one command into the session and saves what the
// device prints until its prompt is back: with the rights the session has
// (enable), any vendor, SSH, telnet or serial; --More-- is answered.
export async function commandToFile(tabId) {
  const t = termTab(tabId);
  const inp = h('input', { type: 'text', value: LS.get('lastCommand', 'show tech-support'), spellcheck: false, class: 'mono-input', 'aria-label': 'Command' });
  const common = h('select', { 'aria-label': 'Common commands' }, h('option', { value: '', text: 'Common commands…' }),
    ...COMMON_COMMANDS.map(([label, cmds]) => h('optgroup', { label }, ...cmds.map(c => h('option', { value: c, text: c })))));
  common.addEventListener('change', () => { if (common.value) { inp.value = common.value; common.value = ''; inp.focus(); } });
  openDialog({
    title: `Save command output from ${t.node.name}`,
    body: h('div', {},
      field('Command', h('div', { class: 'with-btn cmd-row' }, inp, common),
        'Typed into this session, with the rights it has (enable mode). YACM answers --More-- and saves everything up to the next prompt; the terminal shows a line saying where it went. The session must be at its prompt.')),
    buttons: [{ label: 'Cancel' }, { label: 'Save to file…', kind: 'primary', default: true, onClick: () => {
      const c = inp.value.trim();
      if (!c) return false;
      LS.set('lastCommand', c);
      runToFile(t, c, `${safeName(c.replace(/^(show|display|request)\s+/, '').replace(/\s*\|.*$/, ''))}_${hostOf(t)}_${today()}.txt`).catch(e => toast(errMsg(e), 'error'));
    } }],
    initialFocus: inp,
  });
  requestAnimationFrame(() => inp.select());
}

// ---- file list pane -----------------------------------------------------------

function remoteAdapter(info) {
  return {
    kind: 'remote',
    list: dir => api.FilesList(info.id, dir),
    parent: async p => rparent(p),
    join: async (d, n) => rjoin(d, n),
    mkdir: info.kind === 'sftp' ? p => api.FilesMkdir(info.id, p) : null,
    rename: info.kind === 'sftp' ? (a, b) => api.FilesRename(info.id, a, b) : null,
    remove: p => api.FilesRemove(info.id, p),
    free: p => api.FilesFree(info.id, p),
  };
}

const localAdapter = {
  kind: 'local',
  list: dir => api.LocalList(dir),
  parent: p => api.LocalParent(p),
  join: (d, n) => api.LocalJoin(d, n),
  mkdir: p => api.LocalMkdir(p),
  rename: null,
  remove: null,
  free: null,
};

// Pane is one file list with its path bar.
class Pane {
  /** @param {any} ad @param {{ title: string, roots?: any[], onOpen?: (e: any) => void, onMenu?: (e: MouseEvent, entries: any[]) => void, onDrop?: (data: any) => void }} opts */
  constructor(ad, opts) {
    this.ad = ad;
    this.opts = opts;
    this.dir = '';
    this.entries = [];
    this.sel = new Set();
    this.anchor = -1;
    this.path = h('input', { class: 'path', type: 'text', spellcheck: false, 'aria-label': `${opts.title} folder` });
    this.path.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); this.go(this.path.value.trim()); } });
    this.freeEl = h('span', { class: 'free' });
    this.rootSel = h('select', { class: 'roots', title: 'Drive or file system', 'aria-label': 'Drive or file system' });
    this.rootSel.addEventListener('change', () => this.go(this.rootSel.value));
    this.setRoots(opts.roots || []);
    this.body = h('div', { class: 'rows', tabindex: '0', role: 'listbox', 'aria-multiselectable': 'true', 'aria-label': opts.title });
    this.body.addEventListener('keydown', e => this.key(e));
    this.body.addEventListener('contextmenu', e => { if (e.target === this.body) { e.preventDefault(); this.sel.clear(); this.paint(); opts.onMenu?.(e, []); } });
    this.body.addEventListener('dragover', e => { if ([...(e.dataTransfer?.types || [])].includes('application/x-yacm-files')) { e.preventDefault(); this.el.classList.add('drop'); } });
    this.body.addEventListener('dragleave', e => { if (!this.body.contains(/** @type {Node} */ (e.relatedTarget))) this.el.classList.remove('drop'); });
    this.body.addEventListener('drop', e => {
      this.el.classList.remove('drop');
      const raw = e.dataTransfer?.getData('application/x-yacm-files');
      if (!raw) return;
      e.preventDefault();
      const data = JSON.parse(raw);
      if (data.side !== this.ad.kind) opts.onDrop?.(data);
    });
    this.msg = h('div', { class: 'files-msg' });
    this.el = h('div', { class: `files-pane ${ad.kind}` },
      h('div', { class: 'files-head' },
        h('div', { class: 'top' },
          h('b', { class: 'side', text: opts.title, title: opts.title }), this.rootSel,
          h('button', { class: 'icon-btn', title: 'Up one folder (Backspace)', 'aria-label': 'Up one folder', text: '↑', on: { click: () => this.up() } }),
          h('button', { class: 'icon-btn', title: 'Refresh (F5)', 'aria-label': 'Refresh', text: '⟳', on: { click: () => this.refresh() } }),
          this.mkBtn = h('button', { class: 'icon-btn', title: 'New folder', 'aria-label': 'New folder', text: '+', hidden: !ad.mkdir, on: { click: () => this.newFolder() } })),
        this.path),
      h('div', { class: 'cols' }, h('span', { text: 'Name' }), h('span', { text: 'Size' }), h('span', { text: 'Modified' })),
      this.body, this.msg, h('div', { class: 'files-foot' }, this.freeEl));
  }

  setAdapter(ad) {
    this.ad = ad;
    this.mkBtn.hidden = !ad.mkdir;
  }

  setRoots(roots) {
    this.roots = roots;
    const label = p => (p.length > 22 ? `…${p.slice(-20)}` : p);
    this.rootSel.replaceChildren(...roots.map(r => h('option', { value: r.path, title: r.path, text: label(r.path) + (r.free >= 0 ? `  (${fmtBytes(r.free)} free)` : '') })));
    this.rootSel.hidden = roots.length < 2;
  }

  async go(dir) {
    if (!dir) return;
    this.msg.textContent = 'Loading…';
    this.msg.className = 'files-msg';
    try {
      const es = await this.ad.list(dir);
      this.dir = dir;
      this.entries = es || [];
      this.sel.clear();
      this.anchor = -1;
      this.msg.textContent = this.entries.length ? '' : 'This folder is empty.';
      const root = [...this.roots].sort((a, b) => b.path.length - a.path.length).find(r => dir.startsWith(r.path));
      if (root) this.rootSel.value = root.path;
      this.paint();
      this.showFree();
    } catch (e) {
      this.msg.textContent = errMsg(e);
      this.msg.className = 'files-msg error';
    }
    this.path.value = this.dir || dir;
  }

  async showFree() {
    this.freeEl.textContent = `${plural(this.entries.length, 'item', 'items')}`;
    if (!this.ad.free) return;
    const n = await this.ad.free(this.dir).catch(() => -1);
    if (n >= 0) this.freeEl.textContent = `${plural(this.entries.length, 'item', 'items')} · ${fmtBytes(n)} free`;
  }

  refresh() { return this.go(this.dir); }

  async up() {
    const p = await this.ad.parent(this.dir);
    if (p && p !== this.dir) this.go(p);
  }

  selected() { return this.entries.filter((_, i) => this.sel.has(i)); }

  paint() {
    this.body.replaceChildren(...this.entries.map((e, i) => {
      const row = h('div', { class: `row${this.sel.has(i) ? ' sel' : ''}${e.dir ? ' dir' : ''}`, role: 'option', 'aria-selected': String(this.sel.has(i)), draggable: true, title: e.path },
        h('span', { class: 'n' }, e.dir ? icon('files', 'folder') : h('span', { class: 'file-dot' }), h('span', { text: e.name })),
        h('span', { class: 's', text: e.dir ? '' : fmtBytes(e.size) }),
        h('span', { class: 'm', text: fmtDate(e.mod) }));
      row.addEventListener('mousedown', ev => { if (ev.button === 0 || !this.sel.has(i)) this.click(i, ev); });
      row.addEventListener('dblclick', () => (e.dir ? this.go(e.path) : this.opts.onOpen?.(e)));
      row.addEventListener('contextmenu', ev => { ev.preventDefault(); this.opts.onMenu?.(ev, this.selected()); });
      row.addEventListener('dragstart', ev => {
        if (!this.sel.has(i)) { this.sel = new Set([i]); this.markSel(); }
        ev.dataTransfer.setData('application/x-yacm-files', JSON.stringify({ side: this.ad.kind, entries: this.selected() }));
        ev.dataTransfer.effectAllowed = 'copy';
      });
      return row;
    }));
  }

  click(i, ev) {
    if (ev.shiftKey && this.anchor >= 0) {
      const [a, b] = [Math.min(this.anchor, i), Math.max(this.anchor, i)];
      if (!(ev.ctrlKey || ev.metaKey)) this.sel.clear();
      for (let k = a; k <= b; k++) this.sel.add(k);
    } else if (ev.ctrlKey || ev.metaKey) {
      this.sel.has(i) ? this.sel.delete(i) : this.sel.add(i);
      this.anchor = i;
    } else {
      this.sel = new Set([i]);
      this.anchor = i;
    }
    this.markSel();
    this.body.focus();
  }

  // markSel shows the selection without redrawing the rows (a redraw
  // between the two clicks of a double-click would swallow it).
  markSel() {
    [...this.body.children].forEach((row, i) => {
      row.classList.toggle('sel', this.sel.has(i));
      row.setAttribute('aria-selected', String(this.sel.has(i)));
    });
  }

  key(e) {
    const n = this.entries.length;
    const cur = this.anchor;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      const next = Math.max(0, Math.min(n - 1, cur + (e.key === 'ArrowDown' ? 1 : -1)));
      this.click(next, { shiftKey: e.shiftKey, ctrlKey: false, metaKey: false });
      this.body.children[next]?.scrollIntoView({ block: 'nearest' });
    } else if (e.key === 'Enter' && cur >= 0) {
      e.preventDefault();
      const en = this.entries[cur];
      en.dir ? this.go(en.path) : this.opts.onOpen?.(en);
    } else if (e.key === 'Backspace') {
      e.preventDefault();
      this.up();
    } else if (e.key === 'F5') {
      e.preventDefault();
      this.refresh();
    } else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
      e.preventDefault();
      this.sel = new Set(this.entries.map((_, i) => i));
      this.markSel();
    } else if (e.key === 'Delete' && this.ad.remove && this.sel.size) {
      e.preventDefault();
      this.removeSelected();
    }
  }

  async newFolder() {
    const inp = h('input', { type: 'text', spellcheck: false });
    openDialog({
      title: 'New folder', body: field(`Name (in ${this.dir})`, inp),
      buttons: [{ label: 'Cancel' }, { label: 'Create', kind: 'primary', default: true, onClick: async dlg => {
        const name = inp.value.trim();
        if (!name || /[\\/]/.test(name)) { dlg.setError('Enter a name without / or \\.'); return false; }
        try { await this.ad.mkdir(await this.ad.join(this.dir, name)); this.refresh(); } catch (e) { dlg.setError(errMsg(e)); return false; }
      } }],
      initialFocus: inp,
    });
  }

  async rename(e) {
    const inp = h('input', { type: 'text', value: e.name, spellcheck: false });
    openDialog({
      title: `Rename ${e.name}`, body: field('New name', inp),
      buttons: [{ label: 'Cancel' }, { label: 'Rename', kind: 'primary', default: true, onClick: async dlg => {
        const name = inp.value.trim();
        if (!name || name === e.name) return;
        try { await this.ad.rename(e.path, await this.ad.join(this.dir, name)); this.refresh(); } catch (err) { dlg.setError(errMsg(err)); return false; }
      } }],
      initialFocus: inp,
    });
    requestAnimationFrame(() => inp.select());
  }

  async removeSelected() {
    const list = this.selected();
    if (!list.length) return;
    const ok = await confirmDialog({
      title: list.length === 1 ? `Delete ${list[0].name}?` : `Delete ${list.length} items?`,
      message: h('div', {}, h('p', { text: 'They are deleted on the device and cannot be restored.' }), h('pre', { class: 'hostkey', style: { maxHeight: '160px', overflow: 'auto' }, text: list.map(e => e.path).join('\n') })),
      ok: 'Delete', danger: true,
    });
    if (!ok) return;
    for (const e of list) {
      try { await this.ad.remove(e.path); } catch (err) { toast(`${e.name}: ${errMsg(err)}`, 'error', 8000); }
    }
    this.refresh();
  }
}

// ---- the Files tab ------------------------------------------------------------

class FilesTab {
  constructor(node, fromTab) {
    this.id = crypto.randomUUID();
    this.kind = 'files';
    this.node = node;
    this.fromTab = fromTab || '';
    this.state = 'connecting';
    this.message = '';
    this.alert = null;
    this.waiting = null;
    this.info = null;
    this.verify = LS.get('verify', '1') === '1';
    /** @type {any} */ this.el = null;
    /** @type {any} */ this.label = null;
    paneShell(this, 'files-tab');
    this.local = new Pane(localAdapter, {
      title: 'This computer',
      onOpen: e => this.send([e]),
      onMenu: (ev, es) => this.localMenu(ev, es),
      onDrop: d => this.get(d.entries),
    });
    this.remote = new Pane({ kind: 'remote', list: async () => [], parent: async p => rparent(p), join: async (d, n) => rjoin(d, n), free: null }, {
      title: node.name,
      onOpen: e => this.get([e]),
      onMenu: (ev, es) => this.remoteMenu(ev, es),
      onDrop: d => this.send(d.entries),
    });
    const mid = h('div', { class: 'files-mid' },
      h('button', { class: 'btn small', title: 'Send the selected files to the device', text: '→', 'aria-label': 'Send to the device', on: { click: () => this.send(this.local.selected()) } }),
      h('button', { class: 'btn small', title: 'Copy the selected files to this computer', text: '←', 'aria-label': 'Copy to this computer', on: { click: () => this.get(this.remote.selected()) } }));
    this.queue = h('div', { class: 'xfer-queue', hidden: true });
    this.overlay = h('div', { class: 'files-overlay', hidden: true });
    this.el.append(h('div', { class: 'files-body' }, h('div', { class: 'files-panes' }, this.local.el, mid, this.remote.el), this.queue), this.overlay);
    this.unlisten = onTransfer(x => this.onTransfer(x));
    this.offStatus = on('tab:status', st => {
      if (st.tabId === this.fromTab && st.state !== 'connected' && this.info?.shared && this.state === 'connected') {
        this.setState('closed', `${this.node.name} disconnected in its terminal tab`);
      }
    });
    api.LocalHome().then(home => this.local.go(LS.get('localDir', home)));
    api.LocalRoots().then(r => this.local.setRoots(r)).catch(() => {});
  }

  focus() { (this.remote.body.isConnected ? this.remote.body : this.el).focus(); }
  fitAndResize() {}
  applyColors() {}
  paintLabel() { paintPaneLabel(this); }

  setState(st, msg = '') {
    this.state = st;
    this.message = msg;
    this.overlay.hidden = st === 'connected';
    if (st !== 'connected') {
      this.overlay.replaceChildren(...[
        st === 'connecting' ? h('span', { class: 'spinner', 'aria-hidden': 'true' }) : null,
        h('span', { class: `msg${st === 'error' ? ' error' : ''}`, text: st === 'connecting' ? `Connecting to ${this.node.name}…` : msg }),
        st === 'connecting' ? null : h('button', { class: 'btn small primary', text: 'Reconnect', on: { click: () => this.reconnect() } }),
      ].filter(Boolean));
    }
    refreshTab(this);
  }

  async connect() {
    if (this.info) { api.FilesClose(this.info.id); this.info = null; }
    this.setState('connecting');
    const from = this.fromTab && tabById(this.fromTab);
    const borrow = from && from.kind === 'terminal' && from.state === 'connected';
    if (!borrow && !state.byId.has(this.node.id)) {
      this.setState('error', 'Reconnect the quick connect session in its terminal tab first.');
      return;
    }
    try {
      this.info = await api.FilesOpen({ nodeId: this.node.id, tabId: borrow ? this.fromTab : '', ownerId: this.id, scp: false });
    } catch (e) {
      this.setState('error', errMsg(e));
      return;
    }
    if (!this.el.isConnected) { api.FilesClose(this.info.id); return; }
    this.remote.setAdapter(remoteAdapter(this.info));
    this.remote.setRoots(this.info.roots || []);
    this.setState('connected');
    this.remote.go(LS.get(remoteDirKey(this.node), this.info.home)).then(() => {
      if (this.remote.msg.classList.contains('error') && this.remote.dir !== this.info.home) this.remote.go(this.info.home);
    });
  }

  reconnect() { if (this.state !== 'connecting') this.connect(); }

  busy() {
    const n = [...transfers.values()].filter(x => x.filesId === this.info?.id && (active(x))).length;
    return n ? plural(n, 'transfer', 'transfers') : '';
  }

  dispose() {
    this.unlisten();
    this.offStatus?.();
    if (this.info) api.FilesClose(this.info.id);
    if (this.local.dir) LS.set('localDir', this.local.dir);
    this.el.remove();
  }

  menuItems() {
    return [
      { label: 'Reconnect', action: () => this.reconnect(), disabled: this.state === 'connecting' },
      { label: 'Refresh both sides', action: () => { this.local.refresh(); this.remote.refresh(); }, disabled: this.state !== 'connected' },
    ];
  }

  renderStatus(bar) {
    const f = h('div', { class: 'facts' });
    const fact = (b, v, title) => f.append(h('span', { class: 'fact', title: title || v }, h('b', { text: b }), h('span', { text: v })));
    if (this.state !== 'connected') fact('Status', this.state === 'connecting' ? 'connecting…' : this.message || this.state);
    else {
      fact('Protocol', this.info.kind === 'sftp' ? 'SFTP' : 'SCP + device commands', this.info.kind === 'sftp' ? 'SSH File Transfer Protocol' : 'The device has no SFTP: files are copied with SCP and listed with dir');
      fact('Connection', this.info.shared ? 'shared with the terminal tab' : 'own SSH connection', this.info.summary);
    }
    const verify = h('input', { type: 'checkbox', checked: this.verify });
    verify.addEventListener('change', () => { this.verify = verify.checked; LS.set('verify', verify.checked ? '1' : '0'); });
    bar.append(f, h('div', { class: 'tools' },
      h('label', { class: 'check inline', title: 'After sending: verify /md5 (Cisco), md5sum (Linux), file checksum md5 (Junos); after getting: compares with the device' }, verify, h('span', { text: 'Check MD5' })),
      h('button', { class: 'text-btn', text: 'Refresh', on: { click: () => { this.local.refresh(); this.remote.refresh(); } } }),
      h('button', { class: 'text-btn', text: 'Reconnect', on: { click: () => this.reconnect() } })));
  }

  onTransfer(x) {
    if (!this.info || x.filesId !== this.info.id) return;
    this.renderQueue();
    if (x.state === 'done') {
      if (x.dir === 'up' && rparent(x.remote) === rparent(rjoin(this.remote.dir, 'x'))) this.remote.refresh();
      if (x.dir === 'down') this.local.refresh();
    }
    if (x.state === 'error' && x.error) toast(`${x.name}: ${x.error}`, 'error', 9000);
  }

  renderQueue() {
    const list = [...transfers.values()].filter(x => x.filesId === this.info?.id);
    this.queue.hidden = !list.length;
    const anyDone = list.some(x => !active(x));
    this.queue.replaceChildren(
      h('div', { class: 'head' }, h('b', { text: 'Transfers' }),
        anyDone ? h('button', { class: 'text-btn', text: 'Clear finished', on: { click: () => {
          for (const x of list) if (!active(x)) transfers.delete(x.id);
          this.renderQueue();
        } } }) : null),
      h('div', { class: 'rows' }, ...list.slice(-30).reverse().map(transferRow)));
  }

  async send(entries) {
    if (this.state !== 'connected') return;
    const files = entries.filter(e => !e.dir);
    if (entries.some(e => e.dir)) toast('Folders are not copied yet: open the folder and select its files.', 'info', 6000);
    if (!files.length) return;
    const targets = await deviceNames(this.remote.dir, files.map(f => f.name));
    if (!targets) return;
    const names = new Set(this.remote.entries.map(e => e.name));
    const clash = targets.filter(n => names.has(n));
    if (clash.length && !(await confirmDialog({ title: 'Replace files on the device?', message: `${clash.join(', ')} already ${clash.length === 1 ? 'exists' : 'exist'} in ${this.remote.dir}.`, ok: 'Replace', danger: true }))) return;
    LS.set(remoteDirKey(this.node), this.remote.dir);
    files.forEach((f, i) => {
      api.FilesUpload(this.info.id, f.path, rjoin(this.remote.dir, targets[i]), this.verify).catch(e => toast(errMsg(e), 'error'));
    });
  }

  async get(entries) {
    if (this.state !== 'connected' || !this.local.dir) return;
    const files = entries.filter(e => !e.dir);
    if (entries.some(e => e.dir)) toast('Folders are not copied yet: open the folder and select its files.', 'info', 6000);
    if (!files.length) return;
    let targets;
    try {
      targets = await Promise.all(files.map(async f => [f, await api.LocalJoin(this.local.dir, f.name)]));
    } catch (e) { toast(errMsg(e), 'error'); return; }
    const clash = [];
    for (const [f, d] of targets) if (await api.LocalExists(d)) clash.push(f.name);
    if (clash.length && !(await confirmDialog({ title: 'Replace files on this computer?', message: `${clash.join(', ')} already ${clash.length === 1 ? 'exists' : 'exist'} in ${this.local.dir}.`, ok: 'Replace', danger: true }))) return;
    LS.set('localDir', this.local.dir);
    for (const [f, d] of targets) {
      api.FilesDownload(this.info.id, f.path, d, f.size, this.verify).catch(e => toast(errMsg(e), 'error'));
    }
  }

  localMenu(ev, es) {
    const files = es.filter(e => !e.dir);
    contextMenu(ev.clientX, ev.clientY, [
      { label: files.length > 1 ? `Send ${files.length} files to the device` : 'Send to the device', action: () => this.send(es), disabled: !files.length || this.state !== 'connected' },
      { label: 'Show in the file manager', action: () => api.RevealFile(es[0]?.path || this.local.dir) },
      '-',
      { label: 'New folder…', action: () => this.local.newFolder() },
      { label: 'Refresh', action: () => this.local.refresh() },
    ]);
  }

  remoteMenu(ev, es) {
    const files = es.filter(e => !e.dir);
    const one = es.length === 1 ? es[0] : null;
    contextMenu(ev.clientX, ev.clientY, [
      { label: files.length > 1 ? `Copy ${files.length} files to this computer` : 'Copy to this computer', action: () => this.get(es), disabled: !files.length },
      { label: 'Save as…', action: async () => {
        const dest = await api.ChooseSaveAs(`Save ${one.name}`, this.local.dir, one.name);
        if (dest) api.FilesDownload(this.info.id, one.path, dest, one.size, this.verify).catch(e => toast(errMsg(e), 'error'));
      }, disabled: !one || one.dir },
      { label: 'MD5 checksum', action: () => this.md5(one), disabled: !one || one.dir, title: 'Asks the device (verify /md5, md5sum …)' },
      { label: 'Copy path', action: () => navigator.clipboard?.writeText(es.map(e => e.path).join('\n')).catch(() => {}), disabled: !es.length },
      '-',
      { label: 'Rename…', action: () => this.remote.rename(one), disabled: !one || !this.remote.ad.rename, title: this.remote.ad.rename ? undefined : 'Not with SCP' },
      { label: 'New folder…', action: () => this.remote.newFolder(), disabled: !this.remote.ad.mkdir, title: this.remote.ad.mkdir ? undefined : 'Not with SCP' },
      { label: es.length > 1 ? `Delete ${es.length} items…` : 'Delete…', action: () => this.remote.removeSelected(), disabled: !es.length, danger: true },
      '-',
      { label: 'Refresh', action: () => this.remote.refresh() },
    ]);
  }

  async md5(e) {
    const out = h('pre', { class: 'hostkey', text: 'Asking the device…' });
    openDialog({ title: `MD5 of ${e.name}`, sub: e.path, body: out, buttons: [{ label: 'Close', default: true }] });
    try {
      const sum = await api.FilesMD5(this.info.id, e.path);
      out.textContent = sum;
    } catch (err) { out.textContent = errMsg(err); }
  }
}

// openFiles opens the Files tab of a session: with the connection of an
// open terminal tab of it when there is one (no second login).
export function openFiles(nodeId, fromTab) {
  const node = state.byId.get(nodeId) || tabById(fromTab)?.node;
  if (!node) return;
  if (isNewerType(node)) { toast(newerTypeText(node), 'error', 8000); return; }
  if (node.session?.protocol !== 'ssh') { toast('The Files tab needs an SSH session. For telnet and serial sessions, let the device copy from the file servers (File servers, the server icon on the left edge).', 'info', 8000); return; }
  const t = new FilesTab(node, fromTab || terminalTabFor(node.id)?.id);
  addPaneTab(t, fromTab);
  t.connect();
  activate(t.id);
  return t.id;
}

export function openFilesFromTab(tabId) {
  const t = tabById(tabId);
  if (!t) return;
  return openFiles(t.node.id, tabId);
}
