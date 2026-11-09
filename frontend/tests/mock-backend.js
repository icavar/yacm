// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// A stand-in for the Go backend, so the user interface can be tested in a
// browser without Wails. It keeps sessions in memory and records every call
// in window.__calls; tests send backend events with window.__emit(name, data).
// Loaded with page.addInitScript before the page's own scripts.
(() => {
  const cfg = window.__mockConfig || {};
  const listeners = {};
  const calls = [];
  window.__calls = calls;
  window.__emit = (name, data) => (listeners[name] || []).forEach(cb => cb(data));

  const settings = Object.assign({
    fontFamily: '', fontSize: 14, scrollback: 10000, theme: 'dark', copyOnSelect: false, rightClick: 'menu',
    hostnameRename: 'ask', useSystemKnownHosts: false, defaultLogDir: '', autoLockMinutes: 0, sidebarWidth: 320,
    pasteDelay: 0, highlight: true, highlightRules: [], snippets: [], teamLibraries: [],
    lastSeenVersion: '0.6.0', reopenSessions: false, updateCheck: 'off',
  }, cfg.settings || {});
  let nodes = cfg.nodes || [
    { id: 'g1', parentId: '', name: 'DC Zagreb', isGroup: true, expanded: true, order: 0 },
    { id: 's1', parentId: 'g1', name: 'core-sw1', order: 0, session: { protocol: 'ssh', host: '10.0.0.1', port: 22, user: 'admin' } },
    { id: 's2', parentId: 'g1', name: 'core-sw2', order: 1, session: { protocol: 'ssh', host: '10.0.0.2', port: 22, user: 'admin' } },
    { id: 's3', parentId: '', name: 'old-router', order: 1, session: { protocol: 'telnet', host: '10.9.9.9', port: 23 } },
  ];
  const backups = cfg.backups || [];
  let favorites = cfg.favorites || [];
  const servers = {};
  let lowPortAllowed = 1024;
  const clusters = () => {
    const c = {};
    for (const n of nodes) for (const name of n.session?.clusters || []) c[name] = (c[name] || 0) + 1;
    return c;
  };
  const impl = {
    GetState: () => ({ nodes: structuredClone(nodes), settings: structuredClone(settings), clusters: clusters(), credentials: [], teamSnippets: cfg.teamSnippets || [], teamLibraries: [] }),
    GetVaultStatus: () => ({ exists: true, unlocked: true, secrets: 0, dataDir: '/data' }),
    DataFolderStatus: () => ({ path: '/data', source: 'default', readOnly: false, unavailable: false }),
    About: () => ({ app: cfg.version || '0.6.0', dataDir: '/data', go: 'go1.27', wails: 'v2.12.0', xcrypto: 'v0.58.0' }),
    GetStartInfo: () => ({ version: cfg.version || '0.6.0', lastSeen: settings.lastSeenVersion, fresh: !!cfg.fresh, demoFile: cfg.demoFile || '' }),
    MarkVersionSeen: () => { settings.lastSeenVersion = cfg.version || '0.6.0'; return null; },
    GetOpenTabs: () => cfg.openTabs || { sessions: [], active: -1 },
    SaveOpenTabs: () => null,
    RecentSessions: () => cfg.recent || [],
    Favorites: () => favorites.slice(),
    SetFavorite: (id, on) => { favorites = favorites.filter(x => x !== id); if (on) favorites.push(id); return favorites.slice(); },
    LogNamePreview: t => (t || '{group}/{name}_{date}_{time}.log').replace('{group}', 'Zagreb').replace('{name}', 'core-sw1').replace('{date}', '2026-10-28').replace('{time}', '13-35-01').replace('{ticket}', 'CHG0012345'),
    TakeTeamChanges: () => cfg.teamChanges || [],
    TeamLibraries: () => [],
    ImportSources: () => [],
    ListBackups: () => backups,
    KnownHostEntries: () => [],
    NetBoxDefaults: () => ({ url: '', hasToken: false }),
    DefaultHighlightRules: () => [],
    SessionsUnder: ids => {
      const out = new Set();
      const add = id => { const n = nodes.find(x => x.id === id); if (!n) return; if (n.isGroup) nodes.filter(c => c.parentId === id).forEach(c => add(c.id)); else out.add(id); };
      ids.forEach(add);
      return [...out];
    },
    SaveSettings: s => { Object.assign(settings, s); return null; },
    SetExpanded: (id, open) => { const n = nodes.find(x => x.id === id); if (n) n.expanded = open; return null; },
    DeleteNode: id => { nodes = nodes.filter(n => n.id !== id && n.parentId !== id); return null; },
    BulkUpdate: req => req.ids.length,
    SaveNode: n => {
      const copy = structuredClone(n);
      if (!copy.id) copy.id = `n${nodes.length + 1}`;
      nodes = nodes.filter(x => x.id !== copy.id).concat(copy);
      return copy;
    },
    LogUIError: msg => { (window.__uiErrors ||= []).push(msg); return null; },
    AnswerPrompt: () => null,
    RestoreBackup: () => null,
    // File transfer: a device with flash: and this computer's Downloads.
    FilesOpen: req => ({ id: 'f1', name: 'core-sw1', kind: 'scp', home: 'flash:', roots: [{ path: 'flash:', size: 7897796608, free: 6000000000 }, { path: 'usb0:', size: 8e9, free: 7e9 }], summary: 'curve25519-sha256 · ssh-ed25519 · aes128-gcm', shared: !!req.tabId }),
    FilesClose: () => null,
    FilesList: (id, dir) => (dir === 'flash:' ? [
      { name: 'tracelogs', path: 'flash:tracelogs', size: 4096, dir: true, mod: '2026-10-20T09:12:33Z' },
      { name: 'c8000v-universalk9.17.12.04.SPA.bin', path: 'flash:c8000v-universalk9.17.12.04.SPA.bin', size: 3145728, dir: false, mod: '2026-10-20T09:12:33Z' },
      { name: 'vlan.dat', path: 'flash:vlan.dat', size: 3384, dir: false, mod: '2026-10-20T09:12:33Z' },
    ] : []),
    FilesFree: () => 6000000000,
    FilesUpload: (id, local, remote) => 'x-up',
    FilesDownload: () => 'x-down',
    LocalHome: () => '/home/ivan/Downloads',
    FileServerFolder: () => '/home/ivan/Downloads/YACM file servers',
    LocalRoots: () => [{ path: '/home/ivan', size: -1, free: -1 }, { path: '/', size: -1, free: -1 }],
    LocalList: dir => (dir === '/home/ivan/Downloads' ? cfg.local || [{ name: 'new-image.bin', path: '/home/ivan/Downloads/new-image.bin', size: 2048000, dir: false, mod: '2026-10-28T10:00:00Z' }] : []),
    LocalParent: p => p.replace(/\/[^/]*$/, '') || '/',
    LocalJoin: (d, n) => `${d}/${n}`,
    LocalExists: () => false,
    ChooseFiles: () => cfg.chooseFiles || [],
    FileServerStatuses: () => ['tftp', 'http', 'scp'].map(k => servers[k] || { kind: k, running: false }),
    LocalAddresses: () => [{ ip: '10.1.1.5', interface: 'eth0' }],
    ChooseSaveAs: (title, dir, name) => `/home/ivan/Desktop/${name}`,
    SessionCommandToFile: () => 'x-cmd',
    SessionAddresses: ids => ids.map(id => nodes.find(n => n.id === id)).filter(n => n?.session).map(n => (n.session.protocol === 'ssh' && n.session.user ? `${n.session.user}@${n.session.host}` : n.session.host)),
    TunnelStatuses: () => cfg.tunnels || [],
    TunnelStop: () => null,
    SendBreak: () => null,
    FileServerAllowPort: port => { lowPortAllowed = port; return null; },
    CheckForUpdate: () => cfg.update || { current: '0.9.0-rc.1', newer: false, latest: '0.9.0-rc.1' },
    FileServerViaTunnel: (tabId, kind) => ({ address: '127.0.0.1', port: servers[kind]?.port || 0, via: 'core-sw1', reachable: true }),
    FileServerStart: cfg => (cfg.port < 1024 && window.__mockConfig?.lowPorts && lowPortAllowed > cfg.port
      ? (() => { throw new Error(`port ${cfg.port} needs administrator rights on this system: allow it once (YACM asks for the password), or choose a port above 1024 (for example 6969) and give it in the device command`); })()
      : servers[cfg.kind] = { kind: cfg.kind, running: true, port: cfg.port, bind: '', folder: cfg.folder, allowUpload: cfg.allowUpload, user: cfg.kind === 'scp' ? 'yacm' : '', password: cfg.kind === 'scp' ? 'Secret123abcd' : '', hostKey: 'SHA256:x' }),
  };
  const App = new Proxy({}, {
    get: (_, method) => (...args) => {
      calls.push({ method, args: structuredClone(args) });
      const fn = impl[method];
      try {
        return Promise.resolve(fn ? fn(...args) : null);
      } catch (e) {
        return Promise.reject(e);
      }
    },
  });
  window.go = { core: { App } };
  window.runtime = {
    EventsOn: (name, cb) => { (listeners[name] ||= []).push(cb); return () => { listeners[name] = listeners[name].filter(x => x !== cb); }; },
    EventsOff: () => {},
    ClipboardSetText: t => { window.__clipboard = t; return Promise.resolve(true); },
    ClipboardGetText: () => Promise.resolve(window.__clipboard || ''),
    BrowserOpenURL: url => { (window.__opened ||= []).push(url); },
  };
})();
