// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Tunnels of a connected SSH session: what runs, its traffic and errors,
// starting and stopping them, and adding one for this connection.
import { api, state, reload } from './api.js';
import { h, toast, errMsg, openDialog, field, check, uuid } from './ui.js';
import { tabById } from './terms.js';
import { fmtBytes } from './files.js';

export const TYPE = { local: 'Local', remote: 'Remote', dynamic: 'SOCKS/HTTP proxy' };

function describe(s) {
  const bind = s.bind && s.bind !== '127.0.0.1' ? s.bind : '127.0.0.1';
  if (s.type === 'dynamic') return `${bind}:${s.port || 'any'} → anywhere behind the server`;
  if (s.type === 'remote') return `server ${bind}:${s.port || 'any'} → ${s.host}:${s.hostPort}`;
  return `${bind}:${s.port || 'any'} → ${s.host}:${s.hostPort}`;
}

export async function openTunnels(tabId) {
  const t = tabById(tabId);
  if (!t) return;
  const list = h('div', { class: 'tun-list' });
  const paint = sts => {
    sts = sts || [];
    list.replaceChildren(...(sts.length ? sts.map(s => {
      const running = s.state === 'running';
      return h('div', { class: `tun-row ${s.state}` },
        h('span', { class: `led ${running ? 'connected' : s.state === 'error' ? 'error' : ''}` }),
        h('div', { class: 'what' },
          h('div', {}, h('b', { text: s.name || TYPE[s.type] || 'Tunnel' }), ' ', h('span', { class: 'mono', text: running && s.listen ? describe({ ...s, ...splitListen(s.listen) }) : describe(s) })),
          s.error ? h('div', { class: 'err', text: s.error }) : running ? h('div', { class: 'hint', text: `${s.conns} open, ${s.total} so far · ↓ ${fmtBytes(s.in)} ↑ ${fmtBytes(s.out)}${s.last ? ` · last ${s.last}` : ''}` }) : h('div', { class: 'hint', text: s.id?.startsWith('adhoc-') ? 'for this connection only' : 'saved with the session, not started' })),
        running
          ? h('button', { class: 'btn small', text: 'Stop', on: { click: () => api.TunnelStop(tabId, s.id).then(refresh).catch(e => toast(errMsg(e), 'error')) } })
          : h('button', { class: 'btn small', text: 'Start', disabled: t.state !== 'connected', on: { click: () => api.TunnelStart(tabId, s).then(refresh).catch(e => toast(errMsg(e), 'error', 9000)) } }));
    }) : [h('p', { class: 'hint', text: 'No tunnels yet. Add one below, or save tunnels with the session (Edit > Tunnels and proxy).' })]));
  };
  const refresh = () => api.TunnelStatuses(tabId).then(paint).catch(() => {});
  await refresh();
  const timer = setInterval(refresh, 2000);

  // Add a tunnel for this connection (and optionally save it).
  const type = h('select', {}, h('option', { value: 'local', text: 'Local (-L): a port here reaches a host behind the server' }), h('option', { value: 'dynamic', text: 'SOCKS/HTTP proxy (-D): a browser reaches everything behind the server' }), h('option', { value: 'remote', text: 'Remote (-R): a port on the server reaches this computer' }));
  const port = h('input', { type: 'number', min: 0, max: 65535, placeholder: '8443' });
  const host = h('input', { type: 'text', spellcheck: false, placeholder: '10.0.0.1' });
  const hostPort = h('input', { type: 'number', min: 1, max: 65535, placeholder: '443' });
  const save = h('input', { type: 'checkbox' });
  const dest = h('div', { class: 'grid2' }, field('Destination host', host), field('Destination port', hostPort));
  type.addEventListener('change', () => {
    dest.hidden = type.value === 'dynamic';
    host.placeholder = type.value === 'remote' ? '127.0.0.1 (this computer)' : '10.0.0.1';
    if (type.value === 'dynamic' && !port.value) port.value = '1080';
  });
  const canSave = state.byId.has(t.node.id) && !t.node.library;
  const add = async () => {
    const spec = { id: '', type: type.value, port: +port.value || 0, host: host.value.trim(), hostPort: +hostPort.value || 0, bind: '', name: '' };
    if (type.value === 'remote' && !spec.host) spec.host = '127.0.0.1';
    try {
      if (save.checked && canSave) {
        spec.id = uuid();
        const n = structuredClone(state.byId.get(t.node.id));
        n.session.tunnels = [...(n.session.tunnels || []), { ...spec, off: false }];
        await api.SaveNode(n, null);
        await reload();
        t.node = state.byId.get(t.node.id) || t.node;
      }
      await api.TunnelStart(tabId, spec);
      port.value = host.value = hostPort.value = '';
      refresh();
    } catch (e) { toast(errMsg(e), 'error', 9000); }
  };
  openDialog({
    title: `Tunnels of ${t.node.name}`,
    sub: 'Port forwards through this SSH connection. They stop when it disconnects and start again when it reconnects.',
    medium: true,
    body: h('div', {}, list,
      h('h3', { text: 'Add a tunnel', style: { margin: '16px 0 6px', fontSize: '15px' } }),
      field('Type', type), field('Listen port (0 = any free port)', port), dest,
      canSave ? check('Also save it with the session', save, 'Then it starts every time the session connects.') : null,
      h('button', { type: 'button', class: 'btn small primary', text: 'Start tunnel', on: { click: add } })),
    buttons: [{ label: 'Close', default: true }],
    onClose: () => clearInterval(timer),
  });
}

// tunnelText describes a tunnel status: the actual address once it runs.
export function tunnelText(s) {
  return s.state === 'running' && s.listen ? describe({ ...s, ...splitListen(s.listen) }) : describe(s);
}

function splitListen(listen) {
  const i = listen.lastIndexOf(':');
  return i > 0 ? { bind: listen.slice(0, i).replace(/^\[|\]$/g, ''), port: +listen.slice(i + 1) } : {};
}
