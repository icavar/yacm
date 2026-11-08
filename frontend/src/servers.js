// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// File servers: TFTP, HTTP and SCP on this computer, for devices that copy
// files themselves (copy tftp: flash:, copy flash: scp:). One shared folder,
// a list of what devices did, and ready commands for the device.
import { api, on, state } from './api.js';
import { h, toast, errMsg, openDialog, field, check, seg, confirmDialog } from './ui.js';
import { fmtBytes } from './files.js';
import { tabById, terminalTabs, activate, activeTerminal } from './terms.js';
import { TYPE, tunnelText } from './tunnels.js';

const LS = {
  get(k, d = '') { try { return localStorage.getItem(`yacm.servers.${k}`) ?? d; } catch { return d; } },
  set(k, v) { try { localStorage.setItem(`yacm.servers.${k}`, v); } catch { /* storage off */ } },
};

const KINDS = [
  { kind: 'tftp', label: 'TFTP', port: 69, note: 'UDP, no login: for lab and console networks. Anyone who reaches this computer can use it while it is on.' },
  { kind: 'http', label: 'HTTP', port: 8080, note: 'TCP, no login. Devices download with GET and upload with PUT.' },
  { kind: 'scp', label: 'SCP', port: 2022, note: 'SSH with a generated password. Encrypted; also works through a jump host tunnel.' },
];

let statuses = [];
const activity = [];
let renderOpen = null; // refreshes the open dialog

export function initServers() {
  on('fileserver:idle', kind => toast(`The ${kind.toUpperCase()} server switched itself off after ${LS.get('idle', '1') === '1' ? '15 minutes' : 'a while'} without transfers.`, 'info', 8000));
  on('fileserver:status', st => { statuses = st || []; paintButton(); renderOpen?.(); });
  on('fileserver:event', ev => {
    const key = `${ev.kind}|${ev.remote}|${ev.file}|${ev.upload}`;
    const i = activity.findIndex(a => a.key === key && (a.state === 'start' || a.state === 'progress'));
    const row = { ...ev, key, at: Date.now() };
    if (i >= 0) activity[i] = row; else activity.unshift(row);
    activity.length = Math.min(activity.length, 60);
    if (!renderOpen && (ev.state === 'done' || ev.state === 'error')) {
      const what = ev.upload ? `${ev.remote} sent ${ev.file}` : `${ev.remote} got ${ev.file}`;
      toast(ev.state === 'done' ? `${what} (${ev.kind.toUpperCase()}, ${fmtBytes(ev.done)}).` : `${ev.kind.toUpperCase()} ${ev.file} from ${ev.remote}: ${ev.error}`, ev.state === 'done' ? 'info' : 'error', 7000);
    }
    renderOpen?.();
  });
  on('tab:tunnels', () => setTimeout(paintButton));
  on('tab:status', () => setTimeout(paintButton));
  api.FileServerStatuses().then(st => { statuses = st || []; paintButton(); }).catch(() => {});
  document.getElementById('btn-servers')?.addEventListener('click', () => openServers());
}

function running() { return statuses.filter(s => s.running); }

// runningTunnels counts the tunnels running in the open sessions.
function runningTunnels() {
  return terminalTabs().reduce((n, t) => n + (t.state === 'connected' ? (t.tunnels || []).filter(x => x.state === 'running').length : 0), 0);
}

function paintButton() {
  const b = document.getElementById('btn-servers');
  if (!b) return;
  const n = running().length;
  const tun = runningTunnels();
  b.classList.toggle('live', n > 0 || tun > 0);
  const parts = ['File servers and tunnels'];
  if (n) parts.push(`Running: ${running().map(s => `${s.kind.toUpperCase()} on port ${s.port}`).join(', ')}`);
  if (tun) parts.push(`${tun} tunnel${tun === 1 ? '' : 's'} running in your sessions`);
  b.dataset.tip = parts.join('\n');
}

// ---- commands for the device ----------------------------------------------

const PLATFORMS = [
  { value: 'ios', label: 'Cisco IOS / IOS XE' },
  { value: 'nxos', label: 'Cisco NX-OS' },
  { value: 'iosxr', label: 'Cisco IOS XR' },
  { value: 'eos', label: 'Arista EOS' },
  { value: 'junos', label: 'Juniper Junos' },
  { value: 'linux', label: 'Linux' },
];

// Platforms that take no port in an scp:// address (the SCP server then
// needs port 22).
const SCP_NO_PORT = new Set(['ios', 'iosxr', 'eos']);

// deviceCommand builds the command a device types: dir "get" copies from
// this computer to the device, "put" from the device to this computer.
/** @param {{ platform: string, kind: string, dir: string, ip: string, port: number, file?: string, user?: string, dest?: string }} o */
export function deviceCommand({ platform, kind, dir, ip, port, file, user, dest }) {
  const host = ip.includes(':') ? `[${ip}]` : ip;
  const def = { tftp: 69, http: 80, scp: 22 }[kind];
  const hp = port && port !== def ? `${host}:${port}` : host;
  const f = file || 'FILE';
  const enc = encodeURIComponent(f).replace(/%2F/g, '/');
  if (platform === 'linux') {
    if (kind === 'http') return dir === 'get' ? `curl -fO http://${hp}/${enc}` : `curl -fT ${f} http://${hp}/`;
    if (kind === 'tftp') return dir === 'get' ? `tftp ${host}${port !== 69 ? ` ${port}` : ''} -m binary -c get ${f}` : `tftp ${host}${port !== 69 ? ` ${port}` : ''} -m binary -c put ${f}`;
    return dir === 'get' ? `scp -P ${port} ${user}@${host}:${f} .` : `scp -P ${port} ${f} ${user}@${host}:`;
  }
  if (platform === 'junos') {
    const local = dest || '/var/tmp/';
    const url = kind === 'scp' ? `scp://${user}@${hp}/${f}` : `${kind}://${hp}/${enc}`;
    return dir === 'get' ? `file copy ${url} ${local}` : `file copy ${local.replace(/\/?$/, '/')}${f} ${url}`;
  }
  const fs = dest || { nxos: 'bootflash:', iosxr: 'harddisk:' }[platform] || 'flash:';
  if (platform === 'iosxr' && kind === 'scp') {
    // IOS XR copies over SCP with its own scp command.
    return dir === 'get' ? `scp ${user}@${host}:/${f} ${fs}/${f}` : `scp ${fs}/${f} ${user}@${host}:/${f}`;
  }
  const vrf = platform === 'nxos' ? ' vrf management' : '';
  const url = kind === 'scp' ? `scp://${user}@${SCP_NO_PORT.has(platform) ? host : hp}/${f}` : `${kind}://${hp}/${enc}`;
  return dir === 'get' ? `copy ${url} ${fs}${vrf}` : `copy ${fs}${f} ${url}${vrf}`;
}

// deviceNotes are hints for a platform under the command.
/** @param {string} platform @param {string} kind @param {number} port */
export function deviceNotes(platform, kind, port) {
  const notes = [];
  const name = PLATFORMS.find(p => p.value === platform)?.label || platform;
  if (kind === 'scp' && SCP_NO_PORT.has(platform) && port !== 22) notes.push(`${name} cannot give a port for SCP: set the SCP server to port 22 (if nothing else uses it) or use HTTP.`);
  if (platform === 'ios') {
    const cmd = { tftp: 'ip tftp source-interface', http: 'ip http client source-interface', scp: 'ip ssh source-interface' }[kind];
    if (cmd) notes.push(`Through the management port (VRF Mgmt-intf on ISR and ASR routers, Mgmt-vrf on Catalyst 9000): configure ${cmd} GigabitEthernet0 first (Catalyst 9000: GigabitEthernet0/0).`);
  }
  if (platform === 'iosxr') notes.push('Through a management VRF: add the VRF at the end, for example … harddisk: vrf MGMT.');
  if (platform === 'eos') notes.push('Through a management VRF: type cli vrf MGMT first.');
  if (platform === 'nxos') notes.push('The command copies through the management VRF; leave out "vrf management" for the default VRF.');
  return notes;
}

// allowPort asks once whether programs may listen on low ports (Linux), so
// the TFTP server gets port 69, which most network devices insist on.
/** @param {number} port @returns {Promise<boolean>} */
async function allowPort(port) {
  const ok = await confirmDialog({
    title: `Allow port ${port}?`,
    message: h('div', {},
      h('p', { text: `On Linux only programs with administrator rights may use ports below 1024, and most network devices copy over TFTP only from port 69.` }),
      h('p', { text: `YACM can allow programs on this computer to use ports from ${port} up. The system asks for your password once; the setting stays after a restart and after YACM updates (it is kept in /etc/sysctl.d/60-yacm-file-servers.conf).` }),
      h('p', { class: 'hint', text: 'On a computer other people log in to as well, they could then also use these ports. Instead you can choose a port above 1024 if the device can give a port.' })),
    ok: `Allow port ${port}…`,
  });
  if (!ok) return false;
  try {
    await api.FileServerAllowPort(port);
    toast(`Port ${port} is allowed.`);
    return true;
  } catch (e) {
    toast(errMsg(e), 'error', 12000);
    return false;
  }
}

// ---- dialog ---------------------------------------------------------------

/** @param {{ tabId?: string }} [opts] */
export async function openServers({ tabId } = {}) {
  statuses = (await api.FileServerStatuses().catch(() => statuses)) || [];
  const addrs = await api.LocalAddresses().catch(() => []);
  // The session the command is typed into: the one it was opened from, else
  // the one in front, else the first connected one; the list changes it.
  const connected = () => terminalTabs().filter(t => t.state === 'connected');
  const front = activeTerminal();
  let tab = tabId ? tabById(tabId) : front?.state === 'connected' ? front : connected()[0] || null;
  let route = null;
  const routeFor = async t => (t && state.byId.has(t.node.id) ? api.RouteToDevice(t.node.id).catch(() => null) : null);
  route = await routeFor(tab);

  const folder = h('input', { type: 'text', value: LS.get('folder', ''), spellcheck: false, placeholder: 'Folder the devices copy from and to' });
  // Its own folder by default: never Downloads or the home folder, which the
  // servers would share to everyone who can reach this computer. Earlier
  // versions remembered Downloads or home: that is replaced once.
  if (!LS.get('folderChecked', '')) {
    LS.set('folderChecked', '1');
    const home = await api.LocalHome().catch(() => '');
    const trim = p => String(p || '').replace(/[\\/]+$/, '');
    if (folder.value && home && (trim(folder.value) === trim(home) || trim(home).startsWith(trim(folder.value) + '/') || trim(home).startsWith(trim(folder.value) + '\\'))) {
      folder.value = '';
      LS.set('folder', '');
    }
  }
  if (!folder.value) api.FileServerFolder().then(d => { if (!folder.value) folder.value = d; }).catch(() => {});
  const pick = h('button', { type: 'button', class: 'btn small', text: 'Choose…', on: { click: async () => { const d = await api.ChooseFolder('Folder for the file servers', folder.value); if (d) { folder.value = d; LS.set('folder', d); } } } });
  const upload = h('input', { type: 'checkbox', checked: LS.get('upload', '0') === '1' }); // off until switched on (secure default)
  const overwrite = h('input', { type: 'checkbox', checked: LS.get('overwrite', '0') === '1' });
  const listing = h('input', { type: 'checkbox', checked: LS.get('listing', '0') === '1' }); // off: nobody can browse the folder
  const allow = h('input', { type: 'text', spellcheck: false, value: LS.get('allow', ''), placeholder: 'All addresses (or for example 10.1.0.0/16, 192.0.2.7)' });
  const idle = h('input', { type: 'checkbox', checked: LS.get('idle', '1') === '1' });
  const bind = h('select', {}, h('option', { value: '', text: 'All addresses of this computer' }), ...addrs.map(a => h('option', { value: a.ip, text: `${a.ip} (${a.interface})` })));
  bind.value = LS.get('bind', '');
  const rows = h('div', { class: 'srv-rows' });
  const log = h('div', { class: 'srv-log' });
  const helper = h('div', { class: 'srv-helper' });

  const ports = {};
  for (const k of KINDS) ports[k.kind] = h('input', { type: 'number', min: 1, max: 65535, value: LS.get(`port.${k.kind}`, String(k.port)), class: 'port-input', 'aria-label': `${k.label} port` });

  const cfgFor = kind => ({ kind, folder: folder.value.trim(), bind: bind.value, port: Number(ports[kind].value) || 0, allowUpload: upload.checked, overwrite: overwrite.checked, listing: listing.checked, user: '', password: '',
    allow: allow.value.split(/[\s,;]+/).filter(Boolean), idleStop: idle.checked ? 15 : 0 });
  const remember = () => {
    LS.set('folder', folder.value.trim());
    LS.set('upload', upload.checked ? '1' : '0');
    LS.set('overwrite', overwrite.checked ? '1' : '0');
    LS.set('listing', listing.checked ? '1' : '0');
    LS.set('bind', bind.value);
    LS.set('allow', allow.value.trim());
    LS.set('idle', idle.checked ? '1' : '0');
    for (const k of KINDS) LS.set(`port.${k.kind}`, ports[k.kind].value);
  };

  const toggle = async (kind, on) => {
    remember();
    try {
      if (on) await api.FileServerStart(cfgFor(kind));
      else await api.FileServerStop(kind);
    } catch (e) {
      // Linux: ports below 1024 can be allowed once, with the password.
      const low = on && /^port (\d+) needs administrator rights.*allow it once/i.exec(errMsg(e));
      if (low && (await allowPort(+low[1]))) {
        try { await api.FileServerStart(cfgFor(kind)); } catch (e2) { toast(errMsg(e2), 'error', 9000); }
      } else if (!low) toast(errMsg(e), 'error', 9000);
    }
    statuses = (await api.FileServerStatuses()) || [];
    paintButton();
    paint();
  };
  // Settings changed while servers run: restart them with the new ones.
  const restart = async () => {
    remember();
    for (const s of running()) {
      try { await api.FileServerStart({ ...cfgFor(s.kind), user: s.user || '', password: s.password || '' }); } catch (e) { toast(errMsg(e), 'error', 9000); }
    }
    statuses = (await api.FileServerStatuses()) || [];
    paint();
  };
  [upload, overwrite, bind, allow, idle].forEach(el => el.addEventListener('change', restart));
  folder.addEventListener('change', restart);

  // Commands for the device.
  let platform = LS.get('platform', 'ios');
  let kindSel = LS.get('helperKind', 'scp');
  let dirSel = 'get';
  const fileInp = h('input', { type: 'text', spellcheck: false, placeholder: 'file name', value: LS.get('helperFile', '') });
  // Choose… picks the file instead of typing its name: on this computer when
  // the device copies to itself (a file outside the shared folder makes its
  // folder the shared folder), on the device when it copies from itself
  // (its file systems over SSH).
  let deviceFs = ''; // file system of a file chosen on the device (flash:, bootflash: …)
  const pickFile = h('button', { type: 'button', class: 'btn small', text: 'Choose…', on: { click: async () => {
    if (dirSel === 'put') {
      if (!tab) return;
      const p = await import('./files.js').then(m => m.pickDeviceFile(tab.id)).catch(e => { toast(errMsg(e), 'error'); return ''; });
      if (!p) return;
      const m = p.match(/^([A-Za-z0-9_-]+:)\/?(.*)$/);
      if (m) { deviceFs = m[1]; fileInp.value = m[2]; } else if (platform === 'junos' && p.includes('/')) { deviceFs = p.slice(0, p.lastIndexOf('/') + 1); fileInp.value = p.slice(p.lastIndexOf('/') + 1); } else { deviceFs = ''; fileInp.value = p; }
      LS.set('helperFile', fileInp.value);
      paintHelper();
      return;
    }
    const p = await api.ChooseFile('File for the device', folder.value).catch(() => '');
    if (!p) return;
    const root = folder.value.replace(/[\\/]+$/, '');
    const inside = root && (p.startsWith(root + '/') || p.startsWith(root + '\\'));
    if (inside) {
      fileInp.value = p.slice(root.length + 1).replace(/\\/g, '/');
    } else {
      const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
      folder.value = p.slice(0, i) || p;
      fileInp.value = p.slice(i + 1);
      folder.dispatchEvent(new Event('change'));
      toast(`The shared folder is now ${folder.value}.`);
    }
    LS.set('helperFile', fileInp.value);
    paintHelper();
  } } });
  const ipSel = h('select', {});
  const fillAddresses = () => {
    ipSel.replaceChildren(...addrs.map(a => h('option', { value: a.ip, text: a.ip })));
    if (route?.address && !addrs.some(a => a.ip === route.address)) ipSel.prepend(h('option', { value: route.address, text: route.address }));
    if (route?.address) {
      ipSel.value = route.address;
      // Say which address the session's device reaches, in the list itself.
      const o = [...ipSel.options].find(x => x.value === route.address);
      if (o && tab) o.text = `${route.address} (${tab.node.name} reaches it)`;
    }
  };
  fillAddresses();
  const target = h('select', { 'aria-label': 'Session to type into' });
  const fillTargets = () => {
    const list = connected();
    target.replaceChildren(...(list.length ? list.map(t => h('option', { value: t.id, text: t.node.name })) : [h('option', { value: '', text: 'No connected session' })]));
    target.value = tab?.id || '';
    target.disabled = !list.length;
  };
  fillTargets();
  target.addEventListener('change', async () => {
    tab = tabById(target.value) || null;
    route = await routeFor(tab);
    for (const k of Object.keys(via)) delete via[k];
    fillAddresses();
    paintHelper();
  });
  const out = h('pre', { class: 'hostkey srv-cmd' });
  const via = {}; // kind -> tunnel through the jump host { address, port, reachable, note }
  const tunBtn = h('button', { type: 'button', class: 'btn small', on: { click: async () => {
    try {
      via[kindSel] = await api.FileServerViaTunnel(tab.id, kindSel);
      if (via[kindSel].note) toast(via[kindSel].note, 'error', 12000);
    } catch (e) { toast(errMsg(e), 'error', 9000); }
    paintHelper();
  } } });
  const paintHelper = () => {
    const st = statuses.find(s => s.kind === kindSel);
    // A directly connected SSH session can carry the tunnel itself: for a
    // server behind VPN or NAT that cannot connect to this computer.
    const direct = !route?.jump && tab?.node.session?.protocol === 'ssh' && tab.state === 'connected';
    const tun = st?.running && (route?.jump || direct) ? via[kindSel] : null;
    const port = tun ? tun.port : st?.running ? st.port : Number(ports[kindSel].value);
    const ip = tun ? tun.address : ipSel.value || '<this computer>';
    // Devices end a file name at a space: the command asks for the name with
    // underscores, and YACM's servers send the file with spaces for it.
    const raw = fileInp.value.trim();
    const base = raw.split('/').pop() || '';
    const spaced = dirSel === 'get' && /\s/.test(base);
    const file = spaced ? raw.slice(0, raw.length - base.length) + base.replace(/\s/g, '_') : raw;
    const cmd = deviceCommand({ platform, kind: kindSel, dir: dirSel, ip, port, file, user: st?.user || 'yacm', dest: dirSel === 'put' && deviceFs ? deviceFs : undefined });
    out.textContent = cmd;
    const notes = [];
    if (spaced) notes.push(`Devices end a file name at a space, so the command asks for ${base.replace(/\s/g, '_')}; YACM's server sends “${base}” for it.`);
    if (!st?.running) notes.push(`Start the ${kindSel.toUpperCase()} server above first.`);
    notes.push(...deviceNotes(platform, kindSel, port));
    if (kindSel === 'scp' && st?.running) notes.push(`The device asks for the password: ${st.password}`);
    if (route?.jump && !tun) notes.push(`${tab.node.name} is behind the jump host ${route.jump} and cannot reach this computer directly. Open a tunnel through the jump host (HTTP or SCP), or copy with Send file / Get file instead.`);
    if (direct && !tun && st?.running && kindSel !== 'tftp') notes.push(`If ${tab.node.name} cannot connect to this computer (VPN, NAT, a firewall: “Connection refused” or a timeout), open a tunnel through this session: the command then copies from 127.0.0.1 on ${tab.node.name}. Works with Linux servers; Cisco IOS and most network devices do not offer such tunnels.`);
    if (tun) notes.push(tun.reachable ? `Through the tunnel on ${tun.via}: the device copies from ${tun.address}:${tun.port}.` : `The tunnel on ${tun.via} is open, but the device probably cannot reach it: ${tun.note || 'its SSH server does not allow tunnels (AllowTcpForwarding).'}`);
    helperNote.textContent = notes.join(' ');
    tunBtn.hidden = !(route?.jump || direct) || kindSel === 'tftp' || !st?.running || !!tun;
    tunBtn.textContent = route?.jump ? `Open a tunnel through ${route.jump}` : `Open a tunnel through ${tab?.node.name || 'this session'}`;
    typeBtn.disabled = !tab || tab.state !== 'connected';
    typeBtn.textContent = tab ? `Type into ${tab.node.name}` : 'Type into the session';
    const ssh = tab?.node.session?.protocol === 'ssh' && tab.state === 'connected';
    pickFile.disabled = dirSel === 'put' && !ssh;
    pickFile.title = dirSel === 'get' ? 'Pick the file on this computer' : ssh ? `Pick the file on ${tab.node.name}` : 'The files of the device can be listed over SSH only: type the name';
  };
  const helperNote = h('p', { class: 'hint' });
  const typeBtn = h('button', { type: 'button', class: 'btn small primary', text: 'Type into the session', title: 'Types the command into the session without pressing Enter', on: { click: () => {
    if (!tab) return;
    api.SendText([tab.id], out.textContent, false, false).then(() => { dlg.close(); tab.focus(); }).catch(e => toast(errMsg(e), 'error'));
  } } });
  const copyBtn = h('button', { type: 'button', class: 'btn small', text: 'Copy', on: { click: () => navigator.clipboard?.writeText(out.textContent).then(() => toast('Copied.')).catch(() => {}) } });
  fileInp.addEventListener('input', () => { deviceFs = ''; LS.set('helperFile', fileInp.value); paintHelper(); });
  ipSel.addEventListener('input', paintHelper);
  helper.append(
    h('div', { class: 'srv-helper-opts' },
      seg([{ value: 'get', label: 'To the device' }, { value: 'put', label: 'From the device' }], dirSel, v => { dirSel = v; deviceFs = ''; paintHelper(); }),
      seg(KINDS.map(k => ({ value: k.kind, label: k.label })), kindSel, v => { kindSel = v; LS.set('helperKind', v); paintHelper(); }),
      h('select', { on: { change: e => { platform = e.target.value; LS.set('platform', platform); paintHelper(); } } }, ...PLATFORMS.map(p => h('option', { value: p.value, text: p.label, selected: p.value === platform })))),
    h('div', { class: 'srv-helper-opts' }, h('div', { class: 'field' }, h('span', { text: 'File' }), h('div', { class: 'with-btn' }, fileInp, pickFile)), field('Address of this computer', ipSel), field('Session', target)),
    out, helperNote, h('div', { class: 'row-btns' }, typeBtn, copyBtn, tunBtn));

  const paint = () => {
    rows.replaceChildren(...KINDS.map(k => {
      const st = statuses.find(s => s.kind === k.kind) || {};
      const sw = h('input', { type: 'checkbox', class: 'switch', checked: !!st.running, 'aria-label': `${k.label} server` });
      sw.addEventListener('change', () => toggle(k.kind, sw.checked));
      return h('div', { class: `srv-row${st.running ? ' on' : ''}` },
        h('label', { class: 'srv-name' }, sw, h('b', { text: k.label })),
        h('label', { class: 'srv-port' }, 'Port ', ports[k.kind]),
        h('div', { class: 'srv-info' },
          st.running ? h('span', { class: 'ok-text', text: `Running on ${st.bind || 'all addresses'}, port ${st.port}${st.allow?.length ? `, only for ${st.allow.join(', ')}` : ''}${st.stopsAt ? `; switches off at ${new Date(st.stopsAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} unless used` : ''}` }) : h('span', { class: 'hint', text: k.note }),
          st.running && k.kind === 'scp' ? h('div', { class: 'srv-login' }, 'Login ', h('code', { text: st.user }), ' password ', h('code', { text: st.password }),
            h('button', { type: 'button', class: 'text-btn', text: 'Copy password', on: { click: () => navigator.clipboard?.writeText(st.password).catch(() => {}) } }),
            h('div', { class: 'hint', text: `Host key ${st.hostKey}` })) : null));
    }));
    log.replaceChildren(...(activity.length ? activity.slice(0, 30).map(a => h('div', { class: `srv-ev ${a.state}` },
      h('span', { class: 'k', text: a.kind.toUpperCase() }),
      h('span', { class: 'f', text: `${a.upload ? '↓' : '↑'} ${a.file}` }),
      h('span', { class: 'r', text: a.remote }),
      h('span', { class: 's', text: a.state === 'error' ? a.error : a.state === 'checked' ? `read ${fmtBytes(a.done)} and stopped by the device (its check before the copy, or a cancelled copy)` : a.state === 'done' ? fmtBytes(a.done) : `${fmtBytes(a.done)}${a.total > 0 ? ` of ${fmtBytes(a.total)}` : ''}…` }))) : [h('div', { class: 'hint', text: 'Nothing copied yet. Transfers by devices show here.' })]));
    paintHelper();
  };
  // Tunnels running in all open sessions, to see and stop them in one place.
  const tunList = h('div', { class: 'tun-list' });
  const paintTunnels = async () => {
    const found = [];
    for (const t of terminalTabs()) {
      if (t.node.session?.protocol !== 'ssh' || t.state !== 'connected') continue;
      const sts = (await api.TunnelStatuses(t.id).catch(() => [])) || [];
      for (const s of sts) if (s.state === 'running' || s.state === 'error') found.push({ t, s });
    }
    tunList.replaceChildren(...(found.length ? found.map(({ t, s }) => h('div', { class: `tun-row ${s.state}` },
      h('span', { class: `led ${s.state === 'running' ? 'connected' : 'error'}` }),
      h('div', { class: 'what' },
        h('div', {}, h('b', { text: t.node.name }), ' · ', h('span', { text: s.name || TYPE[s.type] || 'Tunnel' }), ' ', h('span', { class: 'mono', text: tunnelText(s) })),
        s.error ? h('div', { class: 'err', text: s.error }) : h('div', { class: 'hint', text: `${s.conns} open, ${s.total} so far · ↓ ${fmtBytes(s.in)} ↑ ${fmtBytes(s.out)}${s.last ? ` · last ${s.last}` : ''}` })),
      h('button', { class: 'btn small', text: 'Go to tab', on: { click: () => { dlg.close(); activate(t.id); } } }),
      s.state === 'running' ? h('button', { class: 'btn small', text: 'Stop', on: { click: () => api.TunnelStop(t.id, s.id).then(paintTunnels).catch(e => toast(errMsg(e), 'error')) } }) : null))
      : [h('p', { class: 'hint', text: 'No tunnels running. Start one in an SSH session (Files ▾ > Tunnels…) or save tunnels with the session (Edit > Tunnels and proxy).' })]));
  };
  const tunTimer = setInterval(paintTunnels, 2000);
  paintTunnels();

  renderOpen = paint;
  paint();
  const dlg = openDialog({
    title: 'File servers and tunnels',
    sub: 'Servers for devices that copy files themselves; they run until you switch them off or quit YACM. Below them, the tunnels of all sessions.',
    wide: true,
    body: h('div', { class: 'servers' },
      h('div', { class: 'srv-folder' }, field('Shared folder', h('div', { class: 'inline' }, folder, pick))),
      h('div', { class: 'srv-opts' }, check('Devices may upload files', upload), check('Replace existing files', overwrite), check('HTTP lists the folder', listing), check('Switch off after 15 minutes without transfers', idle), field('Listen on', bind)),
      field('Devices allowed', allow, 'Addresses or networks, separated by commas. Others are refused.'),
      rows,
      h('p', { class: 'hint', text: addrs.length ? `Addresses of this computer: ${addrs.map(a => a.ip).join(', ')}` : 'This computer has no network address right now.' }),
      h('h3', { text: 'Command for the device' }), helper,
      h('h3', { text: 'Tunnels in your sessions' }), tunList,
      h('h3', { text: 'Activity' }), log),
    buttons: [{ label: 'Close', default: true }],
    onClose: () => { renderOpen = null; clearInterval(tunTimer); remember(); },
  });
}

export function runningServers() { return running(); }
