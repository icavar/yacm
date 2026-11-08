// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { SearchAddon } from '@xterm/addon-search';
import { Unicode11Addon } from '@xterm/addon-unicode11';
import { api, on, state, runtime, colorOf, reload, isNewerType, newerTypeText, saveSettings } from './api.js';
import { h, toast, errMsg, confirmDialog, openDialog, field, portGlyph, icon, contextMenu, anyDialogOpen, plural, KEY, shortcut, zoomWheel, IS_MAC } from './ui.js';
import { Highlighter, compileAlerts, stripAnsi } from './highlight.js';
import { openPalette, editLibrary } from './snippets.js';
import { xtermTheme, colorsFor } from './termcolors.js';

const tabs = new Map(); // tabId -> Tab
let order = [];
let active = null;
const broadcast = { on: false, ids: new Set() };

// Split view: the layout and which tab each pane shows.
const LAYOUTS = {
  '1': [[0, 0, 1, 1]],
  '2v': [[0, 0, .5, 1], [.5, 0, .5, 1]],
  '2h': [[0, 0, 1, .5], [0, .5, 1, .5]],
  '4': [[0, 0, .5, .5], [.5, 0, .5, .5], [0, .5, .5, .5], [.5, .5, .5, .5]],
};
const view = { layout: '1', panes: [null], focus: 0 };

// Pane names per layout, for "Move to" and the keyboard.
const PANE_NAMES = {
  '1': ['main'],
  '2v': ['left', 'right'],
  '2h': ['top', 'bottom'],
  '4': ['top left', 'top right', 'bottom left', 'bottom right'],
};
// NEIGHBOURS[layout][pane][direction] is the pane an arrow key moves to.
const NEIGHBOURS = {
  '2v': [{ ArrowRight: 1 }, { ArrowLeft: 0 }],
  '2h': [{ ArrowDown: 1 }, { ArrowUp: 0 }],
  '4': [{ ArrowRight: 1, ArrowDown: 2 }, { ArrowLeft: 0, ArrowDown: 3 }, { ArrowRight: 3, ArrowUp: 0 }, { ArrowLeft: 2, ArrowUp: 1 }],
};
const DRAG_TYPE = 'application/x-yacm-tab';

// Ad hoc highlighting: up to five words or expressions per tab, each with a
// background colour of its own. /…/ is a regular expression, anything else
// plain text (case is ignored).
const ADHOC_COLORS = ['yellow', 'cyan', 'magenta', 'green', 'blue'];
function adhocPattern(text) {
  const m = text.match(/^\/(.+)\/$/);
  return m ? m[1] : text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

const termTheme = {
  dark: {
    background: '#14181c', foreground: '#d9dfe5', cursor: '#93b4ff', cursorAccent: '#14181c',
    selectionBackground: '#93b4ff55',
    black: '#20262d', red: '#ec7d7d', green: '#7cc59a', yellow: '#e2c25b', blue: '#79a8f0', magenta: '#c49ae6', cyan: '#7fc6e4', white: '#cfd6dd',
    brightBlack: '#5f6a76', brightRed: '#f59a9a', brightGreen: '#9ad9b3', brightYellow: '#f0d67e', brightBlue: '#9cc0ff', brightMagenta: '#d8b8f2', brightCyan: '#a5dbf0', brightWhite: '#f4f7fa',
  },
  light: {
    background: '#fbfcfd', foreground: '#1f262d', cursor: '#3459c7', cursorAccent: '#fbfcfd',
    selectionBackground: '#3459c740',
    black: '#1f262d', red: '#c43c3c', green: '#2f8a57', yellow: '#9a7300', blue: '#3459c7', magenta: '#8a3fb5', cyan: '#1f7fa6', white: '#a7b0b9',
    brightBlack: '#5b6672', brightRed: '#d65a5a', brightGreen: '#3f9e68', brightYellow: '#b58a10', brightBlue: '#4a6fe0', brightMagenta: '#a258cc', brightCyan: '#2c97c0', brightWhite: '#c6cdd4',
  },
};

function b64ToBytes(b64) {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const sleep = ms => new Promise(r => setTimeout(r, ms));

function nodeNow(t) { return state.byId.get(t.node.id) || t.node; }

// Tabs come in kinds: "terminal" (this class) and others such as the Files
// browser (files.js), later remote desktops. Every kind has id, node, kind,
// state, el, label, alert, waiting and the methods focus(), fitAndResize(),
// paintLabel(), applyColors(), dispose(); other kinds add renderStatus(bar),
// menuItems(), reconnect() and busy() (see PaneTab in types.d.ts).
class Tab {
  constructor(id, node) {
    this.id = id;
    this.kind = 'terminal';
    this.node = node;
    this.state = 'connecting';
    this.summary = '';
    this.expect = null;
    this.message = '';
    this.pasting = null;
    /** @type {any} */ this.waiting = null; // waiting for the device after a reload
    /** @type {any} */ this.alert = null; // last output alert while not watched
    this.alertsMuted = false; // alerts turned off for this tab from an alert message
    /** @type {Record<string, number>} */ this.alertLast = {};
    /** @type {any[]} */ this.tunnels = []; // tunnel statuses (tab:tunnels)
    /** @type {string} */ this.quick = ''; // quick connect spec
    const s = state.settings;
    const ss = node.session || {};
    this.baseFont = ss.fontSize || s.fontSize || 14;
    this.term = new Terminal({
      fontFamily: `"JetBrains Mono", ${s.fontFamily || 'monospace'}`,
      fontSize: this.baseFont,
      ...fontWeights(),
      scrollback: ss.scrollback || s.scrollback || 10000,
      cursorBlink: true,
      allowProposedApi: true,
      theme: xtermTheme(termTheme[state.settings.theme === 'light' ? 'light' : 'dark'], node),
      rightClickSelectsWord: false,
      macOptionIsMeta: true,
      convertEol: false,
      // Links a device sends (OSC 8) open like the addresses in the text:
      // only http(s), only with Ctrl/Cmd-click, in the browser. The link
      // text can differ from the address, so the address is shown first.
      linkHandler: {
        activate: (ev, uri) => {
          if (!(ev.ctrlKey || ev.metaKey) || !/^https?:\/\//i.test(uri)) return;
          confirmDialog({ title: 'Open this link?', message: `The device sent a link to ${uri}`, ok: 'Open in the browser' }).then(ok => { if (ok) runtime.openURL(uri); });
        },
        allowNonHttpProtocols: false,
      },
    });
    this.decoder = new TextDecoder('utf-8');
    this.setupHighlight();
    this.fit = new FitAddon();
    this.search = new SearchAddon();
    this.term.loadAddon(this.fit);
    this.term.loadAddon(this.search);
    this.term.loadAddon(new WebLinksAddon((ev, uri) => { if (ev.ctrlKey || ev.metaKey) runtime.openURL(uri); }));
    const u = new Unicode11Addon();
    this.term.loadAddon(u);
    this.term.unicode.activeVersion = '11';
    this.label = h('div', { class: 'pane-label' });
    // xterm's fit measures its parent's full box, so the padding lives on
    // the outer element and the terminal gets an unpadded box of its own.
    this.body = h('div', { class: 'term-body' });
    this.el = h('div', { class: 'term', hidden: true }, this.label, this.body);
    document.getElementById('terms').append(this.el);
    dropTarget(this.el, () => view.panes.indexOf(this.id));
    this.term.open(this.body);
    this.el.style.background = colorsFor(node).bg;
    this.overlay = null;
    this.bind();
  }

  focus() { this.term.focus(); }

  // applyColors follows the session's terminal colours (after an edit or a
  // change of theme or Settings).
  applyColors() {
    const node = nodeNow(this);
    this.term.options.theme = xtermTheme(termTheme[state.settings.theme === 'light' ? 'light' : 'dark'], node);
    this.el.style.background = colorsFor(node).bg;
  }

  setupHighlight() {
    const ss = nodeNow(this).session || {};
    // Ad hoc words come first (their colour wins) and work even when the
    // keyword colours are switched off.
    const adhoc = (this.adhoc || []).map(a => ({ enabled: true, pattern: a.pattern, ignoreCase: true, color: a.color, background: true, bold: true }));
    const rules = [...adhoc, ...(state.settings.highlight && !ss.noHighlight ? state.settings.highlightRules || [] : [])];
    const hl = rules.length ? new Highlighter(rules) : null;
    this.hl = hl?.active ? hl : null;
    // Output alerts work without colours too.
    this.alertRules = !state.settings.noAlerts && !ss.noAlerts && !this.alertsMuted ? compileAlerts(state.settings.highlightRules) : [];
    this.alertLine = '';
    this.alertLast ||= {};
  }

  // ---- ad hoc highlighting: words typed in the status bar ----

  addAdhoc(text) {
    text = (text || '').trim();
    this.adhoc ||= [];
    if (!text || this.adhoc.some(a => a.text.toLowerCase() === text.toLowerCase())) return;
    if (this.adhoc.length >= ADHOC_COLORS.length) { toast(`At most ${ADHOC_COLORS.length} words at once; remove one first.`); return; }
    const pattern = adhocPattern(text);
    try { new RegExp(pattern, 'i'); } catch (e) { toast(`Not a valid regular expression: ${e.message}`, 'error'); return; }
    const used = new Set(this.adhoc.map(a => a.color));
    const item = { text, pattern, color: ADHOC_COLORS.find(c => !used.has(c)), decos: [] };
    this.adhoc.push(item);
    this.setupHighlight();
    this.decorateAdhoc(item);
    if (this.id === active) renderStatus();
  }

  removeAdhoc(item) {
    item.decos.forEach(d => d.dispose());
    this.adhoc = (this.adhoc || []).filter(a => a !== item);
    this.setupHighlight();
    if (this.id === active) renderStatus();
  }

  // decorateAdhoc marks the matches already on screen and in the scrollback
  // (new output is coloured as it arrives).
  decorateAdhoc(item) {
    const buf = this.term.buffer.active;
    const re = new RegExp(item.pattern, 'gi');
    const bg = (termTheme[state.settings.theme === 'light' ? 'light' : 'dark'])[item.color] || '#e2c25b';
    const cursorAbs = buf.baseY + buf.cursorY;
    const first = Math.max(0, buf.length - 10000);
    for (let y = buf.length - 1; y >= first && item.decos.length < 2000; y--) {
      const line = buf.getLine(y)?.translateToString(true);
      if (!line) continue;
      re.lastIndex = 0;
      let m;
      while ((m = re.exec(line)) && item.decos.length < 2000) {
        if (!m[0]) { re.lastIndex++; continue; }
        const marker = this.term.registerMarker(y - cursorAbs);
        if (!marker) continue;
        const deco = this.term.registerDecoration({ marker, x: m.index, width: m[0].length, backgroundColor: bg, foregroundColor: '#14181c', layer: 'top' });
        if (deco) item.decos.push(deco); else marker.dispose();
      }
    }
  }

  write(bytes) {
    const text = this.decoder.decode(bytes, { stream: true });
    this.term.write(this.hl ? this.hl.process(text) : text);
    if (this.alertRules.length) this.scanAlerts(text);
  }

  // scanAlerts checks finished lines against the alert rules while nobody
  // looks at this session; one alert per rule and session every 30 s.
  scanAlerts(text) {
    if (isWatched(this) || this.pasting) { this.alertLine = ''; return; }
    const parts = (this.alertLine + stripAnsi(text)).split(/\r\n|\n|\r/);
    this.alertLine = parts.pop().slice(-2000);
    for (const line of parts) {
      const r = this.alertRules.find(x => x.re.test(line));
      if (!r) continue;
      const now = Date.now();
      if (now - (this.alertLast[r.id] || 0) < 30000) continue;
      this.alertLast[r.id] = now;
      raiseAlert(this, r, line.trim());
    }
  }

  bind() {
    const t = this.term;
    t.onData(d => this.input(d));
    t.onBinary(d => this.input(d));
    t.onSelectionChange(() => {
      if (state.settings.copyOnSelect && t.hasSelection()) runtime.copy(t.getSelection());
    });
    t.attachCustomKeyEventHandler(e => {
      if (e.type !== 'keydown') return true;
      if (isBreakKey(e)) return false; // globalKeys sends it
      const sk = shortcut(e);
      if (sk) {
        if (sk === 'c') { if (t.hasSelection()) runtime.copy(t.getSelection()); return false; }
        if (sk === 'v') { this.nativePasteUntil = Date.now() + 500; this.pasteFromClipboard(); return false; }
        if (['f', 'w', 'n', 'b', 'r', 'o', 'palette', '+', '-', '0', 'prevtab', 'nexttab'].includes(sk)) return false; // app shortcuts
        if (e.metaKey) return false; // other Cmd keys never go to the device
      }
      if (paneMoveKey(e)) return false; // handled by globalKeys
      if (e.shiftKey && e.key === 'Insert') { this.pasteFromClipboard(); return false; }
      if (e.ctrlKey && e.key === 'Tab') return false;
      if (pageScroll(t, e)) return false;
      return true;
    });
    // Right click opens a menu, so nothing is sent by accident. Settings can
    // switch to the PuTTY habit: right click without a selection pastes.
    this.el.addEventListener('contextmenu', e => {
      e.preventDefault();
      const sel = t.hasSelection() ? t.getSelection() : '';
      if (!sel && state.settings.rightClick === 'paste') { this.pasteFromClipboard(); return; }
      contextMenu(e.clientX, e.clientY, [
        { label: 'Copy', key: KEY.copy, action: () => runtime.copy(sel), disabled: !sel },
        { label: 'Paste', key: KEY.paste, action: () => this.pasteFromClipboard(), disabled: this.state !== 'connected' },
        { label: 'Copy and paste', action: () => { runtime.copy(sel); this.paste(sel); }, disabled: !sel || this.state !== 'connected' },
        '-',
        { label: 'Send saved command…', key: KEY.palette, action: () => { focusTab(this.id); openPalette(); }, disabled: this.state !== 'connected' },
        { label: 'Save as command…', action: () => editLibrary(null, { add: sel }), disabled: !sel.trim() },
        { label: 'Highlight this', action: () => this.addAdhoc(sel.split('\n')[0].slice(0, 200)), disabled: !sel.trim() },
        '-',
        breakItem(this),
        '-',
        { label: 'Select all', action: () => t.selectAll() },
        { label: 'Clear scrollback', action: () => t.clear() },
      ]);
    });
    // Native paste (Ctrl+V inside the webview) goes through the same check.
    this.el.addEventListener('paste', e => {
      e.preventDefault();
      e.stopImmediatePropagation();
      if (this.nativePasteUntil && Date.now() < this.nativePasteUntil) return; // already pasted by the shortcut
      this.paste(e.clipboardData?.getData('text') || '');
    }, true);
    // Ctrl + mouse wheel changes the font size of this tab only.
    this.el.addEventListener('wheel', e => {
      if (!zoomWheel(e)) return;
      e.preventDefault();
      e.stopPropagation();
      this.zoom(e.deltaY < 0 ? 1 : -1);
    }, { passive: false, capture: true });
    // Clicking or typing in a pane makes it the active one.
    this.el.addEventListener('mousedown', () => focusTab(this.id), true);
    this.el.addEventListener('focusin', () => focusTab(this.id));
  }

  zoom(delta) {
    const cur = this.term.options.fontSize;
    const next = delta === 0 ? this.baseFont : Math.max(6, Math.min(40, cur + delta));
    if (next === cur) return;
    this.term.options.fontSize = next;
    this.fitAndResize();
    if (this.id === active) renderStatus();
  }

  async keepFontSize() {
    const n = state.byId.get(this.node.id);
    if (!n) { toast('Quick connect sessions are not saved.'); return; }
    const copy = structuredClone(n);
    copy.session.fontSize = this.term.options.fontSize;
    try {
      await api.SaveNode(copy, null);
      n.session.fontSize = copy.session.fontSize;
      this.baseFont = copy.session.fontSize;
      toast(`${n.name} now opens with font size ${this.baseFont}.`);
      renderStatus();
    } catch (e) { toast(errMsg(e), 'error'); }
  }

  async pasteFromClipboard() {
    try { this.paste(await runtime.paste()); } catch (e) { toast(errMsg(e), 'error'); }
  }

  pasteDelay() {
    const d = nodeNow(this).session?.pasteDelay || 0;
    if (d < 0) return 0;
    return d || state.settings.pasteDelay || 0;
  }

  async paste(text) {
    if (!text) return;
    const lines = text.replace(/\r\n/g, '\n').split('\n');
    const multi = lines.filter(l => l.trim() !== '').length;
    const delay = this.pasteDelay();
    if (multi > 1) {
      const targets = broadcast.on && broadcast.ids.has(this.id) ? broadcast.ids.size : 1;
      const ok = await confirmDialog({
        title: `Paste ${multi} lines?`,
        message: h('div', {},
          h('p', { text: (targets > 1 ? `Each line will run on ${targets} sessions.` : `Each line will run on ${this.node.name}.`) + (delay ? ` Lines are sent ${delay} ms apart.` : '') }),
          h('pre', { class: 'hostkey', style: { maxHeight: '220px', overflow: 'auto', whiteSpace: 'pre-wrap' }, text: lines.slice(0, 40).join('\n') + (lines.length > 40 ? `\n… ${lines.length - 40} more` : '') })),
        ok: 'Paste',
      });
      if (!ok) return;
    }
    if (multi > 1 && delay > 0) {
      await this.pacedPaste(lines, delay);
    } else {
      this.term.paste(text);
    }
    this.term.focus();
  }

  // pacedPaste sends one line at a time, for consoles that drop characters.
  async pacedPaste(lines, delay) {
    if (this.pasting) return;
    const endsWithNewline = lines[lines.length - 1] === '';
    if (endsWithNewline) lines = lines.slice(0, -1);
    const job = { i: 0, n: lines.length, stop: false };
    this.pasting = job;
    renderStatus();
    try {
      for (; job.i < lines.length && !job.stop; job.i++) {
        if (this.state !== 'connected') break;
        const last = job.i === lines.length - 1;
        this.input(lines[job.i] + (last && !endsWithNewline ? '' : '\r'));
        if (this.id === active) renderStatus();
        if (!last) await sleep(delay);
      }
    } finally {
      this.pasting = null;
      if (this.id === active) renderStatus();
    }
  }

  input(d) {
    if (this.state !== 'connected') {
      if ((this.state === 'closed' || this.state === 'error') && d === '\r') reconnect(this.id);
      return;
    }
    if (broadcast.on && broadcast.ids.has(this.id)) {
      api.Broadcast([...broadcast.ids].filter(id => tabs.get(id)?.state === 'connected'), d);
    } else {
      api.Write(this.id, d).catch(() => {});
    }
  }

  log(text) {
    const nl = this.term.buffer.active.cursorX > 0 ? '\r\n' : '';
    this.term.write(`${nl}\x1b[2m${text.replace(/\r?\n/g, '\r\n')}\x1b[0m\r\n`);
  }

  showOverlay(kind, msg) {
    this.hideOverlay();
    const o = h('div', { class: `term-overlay ${kind}` },
      h('span', { class: 'msg', text: msg }),
      h('button', { class: 'btn small primary', text: 'Reconnect', on: { click: () => reconnect(this.id) } }),
      h('button', { class: 'btn small', text: 'Close tab', on: { click: () => closeTab(this.id) } }));
    this.el.append(o);
    this.overlay = o;
  }

  hideOverlay() { this.overlay?.remove(); this.overlay = null; clearInterval(this.waitTimer); this.waitTimer = 0; }

  // showWaiting: the device is being watched until it answers again.
  showWaiting() {
    this.hideOverlay();
    const w = this.waiting;
    const msg = h('span', { class: 'msg' });
    const tick = () => {
      const s = Math.max(0, Math.floor((Date.now() - w.since) / 1000));
      msg.textContent = `Waiting for ${this.node.name} to answer again… ${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')} (up to ${Math.round(w.max / 60)} min), then it reconnects.`;
    };
    tick();
    const o = h('div', { class: 'term-overlay waiting' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }), msg,
      h('button', { class: 'btn small', text: 'Reconnect now', on: { click: () => { api.StopWaiting(this.id); reconnect(this.id); } } }),
      h('button', { class: 'btn small', text: 'Stop', on: { click: () => api.StopWaiting(this.id) } }));
    this.el.append(o);
    this.overlay = o;
    this.waitTimer = setInterval(() => { tick(); if (this.id === active) renderStatus(); }, 1000);
  }

  // The device prompt shows another hostname than the session name.
  offerHostname(o) {
    this.notice?.remove();
    const done = () => { this.notice?.remove(); this.notice = null; this.term.focus(); };
    const n = h('div', { class: 'term-overlay notice', role: 'status' },
      h('span', { class: 'msg' }, 'The device calls itself ', h('b', { text: o.hostname }), '. Rename the session “', o.name, '”?'),
      h('button', { class: 'btn small primary', text: `Rename to ${o.hostname}`, on: { click: async () => {
        try { await api.RenameSession(o.nodeId, o.hostname); await renamed(); toast(`Renamed to ${o.hostname}.`); } catch (e) { toast(errMsg(e), 'error'); }
        done();
      } } }),
      h('button', { class: 'btn small', text: 'Not now', on: { click: done } }),
      h('button', { class: 'btn small', text: 'Keep this name', title: 'Never ask again for this session', on: { click: async () => {
        try { await api.KeepSessionName(o.nodeId); await reload(); } catch (e) { toast(errMsg(e), 'error'); }
        done();
      } } }));
    this.el.append(n);
    this.notice = n;
  }

  fitAndResize() {
    if (this.el.hidden) return;
    try { this.fit.fit(); } catch { return; }
    const { cols, rows } = this.term;
    if (cols !== this.cols || rows !== this.rows) {
      this.cols = cols; this.rows = rows;
      api.Resize(this.id, cols, rows).catch(() => {});
    }
  }

  paintLabel() { paintPaneLabel(this); }

  dispose() {
    this.pasting && (this.pasting.stop = true);
    this.term.dispose();
    this.el.remove();
  }
}

// paintPaneLabel draws the name bar a tab shows in split view.
export function paintPaneLabel(t) {
  const color = colorOf(nodeNow(t));
  if (color) { t.el.dataset.color = color; } else { delete t.el.dataset.color; }
  if (!t.label.draggable) {
    t.label.draggable = true;
    t.label.title = 'Drag onto another pane to swap; right click to move';
    t.label.addEventListener('dragstart', e => startDrag(e, t.id));
    t.label.addEventListener('dragend', endDrag);
    t.label.addEventListener('contextmenu', e => { e.preventDefault(); e.stopPropagation(); contextMenu(e.clientX, e.clientY, [...moveItems(t.id), '-', { label: 'Remove from this pane', action: () => unpane(t.id) }]); });
  }
  // replaceChildren would write a null as the text "null": leave it out.
  t.label.replaceChildren(...[
    h('span', { class: `led ${t.waiting ? 'waiting' : t.state}` }),
    t.kind === 'terminal' ? null : icon(t.kind, 'label-kind'),
    h('span', { class: 'name', text: t.kind === 'files' ? `${t.node.name}: files` : t.node.name }),
    h('button', { title: 'Remove from this pane', 'aria-label': 'Remove from this pane', text: '×', on: { click: e => { e.stopPropagation(); unpane(t.id); } } }),
  ].filter(Boolean));
}

// paneShell makes the element of a tab of another kind: a pane with a
// name bar that tabs can be dropped on. The caller fills el.
export function paneShell(t, cls) {
  t.label = h('div', { class: 'pane-label' });
  t.el = h('div', { class: `term ${cls}`, hidden: true }, t.label);
  document.getElementById('terms').append(t.el);
  dropTarget(t.el, () => view.panes.indexOf(t.id));
  t.el.addEventListener('mousedown', () => focusTab(t.id), true);
  t.el.addEventListener('focusin', () => focusTab(t.id));
}

// addPaneTab adds a tab of another kind (after the tab "after").
export function addPaneTab(t, after) { addTab(t.id, t.node, after, t); return t; }

// terminalTabFor finds a connected terminal tab of a session.
export function terminalTabFor(nodeId) {
  for (const id of order) { const t = tabs.get(id); if (t.kind === 'terminal' && t.node.id === nodeId && t.state === 'connected') return t; }
  return null;
}

// activeTerminal is the terminal tab in front (null for a Files tab or none).
// pageScroll lets Page Up / Page Down scroll the terminal like in other
// terminal programs, except while a full-screen program on the device (less,
// vi, top: the alternate screen) wants them. Shift+Page Up/Down is handled
// by the terminal itself.
function pageScroll(t, e) {
  if (e.key !== 'PageUp' && e.key !== 'PageDown') return false;
  if (e.shiftKey || e.ctrlKey || e.altKey || e.metaKey) return false;
  if (state.settings.pageKeys === 'device' || t.buffer.active.type === 'alternate') return false;
  t.scrollPages(e.key === 'PageUp' ? -1 : 1);
  return true;
}

export function activeTerminal() { const t = tabs.get(active); return isTerm(t) ? t : null; }

// terminalTabs are the terminal tabs in tab bar order.
export function terminalTabs() { return order.map(id => tabs.get(id)).filter(isTerm); }

// tabById gives other modules a tab (terminal tabs: for Send / Get file).
export function tabById(id) { return tabs.get(id); }

// refreshTab repaints a tab after its state changed.
export function refreshTab(t) {
  t.paintLabel();
  renderTabbar();
  if (t.id === active) renderStatus();
}

const isTerm = t => t?.kind === 'terminal';

// ---------- public API ----------

export function initTerms() {
  on('tab:data', ({ tabId, data }) => tabs.get(tabId)?.write(b64ToBytes(data)));
  on('tab:log', ({ tabId, text }) => tabs.get(tabId)?.log(text));
  on('tab:status', st => {
    const t = tabs.get(st.tabId);
    if (!t) return;
    t.state = st.state;
    if (st.summary !== undefined && st.summary !== '') t.summary = st.summary;
    t.message = st.message || '';
    if (st.state === 'connected') { t.hideOverlay(); t.expect = null; }
    if (st.state === 'error') { t.log(`\x1b[0;31m${st.message}`); t.showOverlay('error', st.message); }
    if (st.state === 'closed') { const m = st.message || 'connection closed'; t.log(m); t.showOverlay('', `${m[0].toUpperCase()}${m.slice(1)}. Press Enter to reconnect.`); }
    t.paintLabel();
    renderTabbar();
    if (t.id === active) renderStatus();
  });
  on('tab:expect', ({ tabId, event }) => {
    const t = tabs.get(tabId);
    if (!t) return;
    t.expect = event;
    if (event.state === 'timeout') t.log(`automation: timed out waiting for “${event.pattern}”`);
    if (event.state === 'stopped' && event.index < event.total) t.log('automation stopped');
    if (t.id === active) renderStatus();
  });
  on('tab:wait', st => {
    const t = tabs.get(st.tabId);
    if (!t) return;
    if (st.state === 'waiting') {
      t.waiting = st;
      t.log(`waiting for ${t.node.name} to answer again`);
      t.showWaiting();
    } else {
      t.waiting = null;
      t.hideOverlay();
      if (st.state === 'up') {
        t.log(`${t.node.name} answers again`);
        toast(`${t.node.name} is back. Reconnecting.`);
        reconnect(t.id);
      } else if (st.state === 'stopped') {
        t.showOverlay('', 'Stopped waiting. Press Enter to reconnect.');
      } else {
        t.log(st.message || 'stopped waiting');
        t.showOverlay(st.state === 'timeout' ? 'error' : '', `${st.message}.`);
      }
    }
    t.paintLabel();
    renderTabbar();
    if (t.id === active) renderStatus();
  });
  on('tab:hostname', async o => {
    const t = tabs.get(o.tabId);
    if (!t) return;
    if (o.renamed) {
      await renamed();
      toast(`Renamed “${o.name}” to ${o.hostname}, the hostname the device reports.`);
      return;
    }
    t.offerHostname(o);
  });
  on('tab:tunnels', ({ tabId, tunnels }) => {
    const t = tabs.get(tabId);
    if (!t) return;
    t.tunnels = tunnels || [];
    if (t.id === active) renderStatus();
  });
  on('tab:after', async ({ tabId, commands }) => {
    const t = tabs.get(tabId);
    const ok = await confirmDialog({
      title: 'Run local commands?',
      message: h('div', {}, h('p', { text: `${t?.node.name || 'The session'} disconnected. These commands are set to run afterwards:` }),
        h('pre', { class: 'hostkey', text: commands.join('\n') })),
      ok: 'Run', cancel: 'Skip',
    });
    if (ok) api.RunAfterCommands(tabId).catch(e => toast(errMsg(e), 'error'));
  });
  new ResizeObserver(() => fitVisible()).observe(document.getElementById('terms'));
  // Coming back to the window clears the alerts of the sessions on screen.
  window.addEventListener('focus', () => {
    let changed = false;
    for (const id of view.panes) { const t = tabs.get(id); if (t?.alert) { t.alert = null; changed = true; } }
    if (changed) renderTabbar();
  });
  document.addEventListener('keydown', globalKeys, true);
  /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('#layout-tools .layout-btn')).forEach(b => b.addEventListener('click', () => setLayout(b.dataset.layout)));
}

function fitVisible() {
  for (const id of view.panes) tabs.get(id)?.fitAndResize();
}

// addTab creates a tab at the end of the tab bar, or right after the tab
// "after" (Clone session).
function addTab(id, node, after, custom) {
  const t = custom || new Tab(id, node);
  tabs.set(id, t);
  const at = after ? order.indexOf(after) : -1;
  if (at >= 0) order.splice(at + 1, 0, id); else order.push(id);
  t.paintLabel();
  // In split view a new session fills an empty pane first.
  const empty = view.panes.indexOf(null);
  if (view.layout !== '1' && empty >= 0) view.focus = empty;
  activate(id);
  if (broadcast.on) renderBroadcast();
  return t;
}

// ---- change/ticket number for the log file name ----------------------------

let lastTicket = '';

// needsTicket: the session writes a log and Settings ask for a number for it.
function needsTicket(node) {
  if (!node?.session?.logToFile) return false;
  const mode = state.settings.askTicket;
  if (mode === 'all') return true;
  if (mode !== 'groups') return false;
  for (let g = state.byId.get(node.parentId), i = 0; g && i < 64; g = state.byId.get(g.parentId), i++) {
    if (g.defaults?.askTicket) return true;
  }
  return false;
}

// askTicket resolves the number ('' = none) or null when the user cancels.
function askTicket(nodes) {
  return new Promise(resolve => {
    const inp = h('input', { type: 'text', value: lastTicket, spellcheck: false, maxLength: 64, placeholder: 'CHG0012345', 'aria-label': 'Change or ticket number' });
    let result = null;
    const who = nodes.length === 1 ? nodes[0].name : plural(nodes.length, 'session', 'sessions');
    openDialog({
      title: 'Change or ticket number',
      body: h('div', {},
        h('p', { class: 'lead', text: `For the log file name of ${who}. Leave it empty to connect without one.` }),
        field('Change or ticket number', inp)),
      buttons: [{ label: 'Cancel' }, { label: 'Connect', kind: 'primary', default: true, onClick: () => { result = inp.value.trim(); lastTicket = result; } }],
      onClose: () => resolve(result),
      initialFocus: inp,
    });
    requestAnimationFrame(() => inp.select());
  });
}

async function confirmBefore(node) {
  const cmds = (node.session?.before || []).filter(c => c.confirm);
  if (!cmds.length) return true;
  return confirmDialog({
    title: 'Run local commands first?',
    message: h('div', {}, h('p', { text: `${node.name} runs these commands on this computer before connecting:` }), h('pre', { class: 'hostkey', text: cmds.map(c => c.command).join('\n') })),
    ok: 'Run and connect', cancel: 'Connect without them',
  });
}

/** @param {string} nodeId @param {{ after?: string, ticket?: string }} [opts] */
export async function openSession(nodeId, { after, ticket } = {}) {
  const node = state.byId.get(nodeId);
  if (!node || node.isGroup) return;
  if (isNewerType(node)) { toast(newerTypeText(node), 'error', 8000); return; }
  if (ticket === undefined && needsTicket(node)) {
    ticket = await askTicket([node]);
    if (ticket === null) return;
  }
  const approve = await confirmBefore(node);
  // Create the terminal first so we know its size; the tab ID is ours so no
  // backend event can arrive before the tab exists.
  const t = addTab(crypto.randomUUID(), node, after);
  t.fitAndResize();
  try {
    await api.Connect({ nodeId, tabId: t.id, cols: t.term.cols, rows: t.term.rows, approveBefore: approve, quick: '', ticket: ticket || '' });
  } catch (e) {
    t.state = 'error';
    t.log(errMsg(e));
    t.showOverlay('error', errMsg(e));
    renderTabbar();
  }
  return t.id;
}

// ---- open sessions from last time ------------------------------------------

let persistTimer = 0;
let restoring = false;
// rememberTabs saves the open sessions (debounced), for "reopen at start".
function rememberTabs() {
  if (restoring) return;
  clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    const ids = order.filter(id => isTerm(tabs.get(id)) && tabs.get(id).node.id && !String(tabs.get(id).node.id).startsWith('quick'));
    const index = id => ids.indexOf(id);
    api.SaveOpenTabs({
      sessions: ids.map(id => tabs.get(id).node.id),
      layout: view.layout,
      panes: view.panes.map(id => (id ? index(id) : -1)),
      active: active ? index(active) : -1,
      clean: false,
    }).catch(() => {});
  }, 400);
}

// restoreTabs opens last time's sessions: always with the setting, after a
// crash only when the user agrees.
export async function restoreTabs() {
  const last = await api.GetOpenTabs().catch(() => null);
  const ids = (last?.sessions || []);
  if (!ids.length) return;
  const known = ids.filter(id => state.byId.get(id) && !state.byId.get(id).isGroup);
  if (!known.length) return;
  if (!state.settings.reopenSessions) {
    if (!last.crashed) return;
    const ok = await confirmDialog({
      title: 'Reopen the sessions from last time?',
      message: `YACM did not close normally. ${plural(known.length, 'session was', 'sessions were')} open.`,
      ok: 'Reopen', cancel: 'Not now',
    });
    if (!ok) { rememberTabs(); return; }
  }
  const ticket = await ticketFor(known.map(id => state.byId.get(id)));
  if (ticket === null) { rememberTabs(); return; }
  restoring = true;
  const newIds = new Map();
  try {
    for (const id of known) if (!isNewerType(state.byId.get(id))) newIds.set(id, await openSession(id, { ticket }));
  } finally {
    restoring = false;
  }
  const tabFor = i => (i >= 0 && i < ids.length ? newIds.get(ids[i]) || null : null);
  if (LAYOUTS[last.layout] && last.layout !== '1') {
    setLayout(last.layout, true);
    const panes = (last.panes || []).map(tabFor).slice(0, LAYOUTS[last.layout].length);
    while (panes.length < LAYOUTS[last.layout].length) panes.push(null);
    if (panes.some(Boolean)) view.panes = panes;
  }
  const act = tabFor(last.active);
  if (act) activate(act); else applyView();
  if (known.length < ids.length) toast(`${plural(ids.length - known.length, 'session no longer exists', 'sessions no longer exist')} and was not reopened.`);
  rememberTabs();
}

/** @param {string} spec @param {{ after?: string }} [opts] */
export async function openQuick(spec, { after } = {}) {
  const proto = spec.startsWith('telnet://') ? 'telnet' : spec.startsWith('serial://') ? 'serial' : 'ssh';
  const node = { id: 'quick', name: spec.replace(/^(ssh|telnet|serial):\/\//, ''), session: { protocol: proto } };
  const t = addTab(crypto.randomUUID(), node, after);
  t.quick = spec;
  t.fitAndResize();
  try {
    await api.Connect({ nodeId: '', tabId: t.id, cols: t.term.cols, rows: t.term.rows, approveBefore: false, quick: spec });
  } catch (e) {
    closeTab(t.id);
    toast(errMsg(e), 'error');
  }
}

// cloneSession opens a second connection to the device of a tab, right
// after it. It logs in again (a password that is asked for is asked again).
export async function cloneSession(id) {
  const t = tabs.get(id);
  if (!t) return;
  if (t.node.session?.protocol === 'serial') { toast('A serial port can be open in one tab only.'); return; }
  if (t.quick) return openQuick(t.quick, { after: id });
  if (!state.byId.has(t.node.id)) { toast('This session no longer exists.'); return; }
  return openSession(t.node.id, { after: id });
}

// openBeside opens a session next to the current tab: in split view in a
// pane other than the active one, otherwise it switches to two side by side.
export async function openBeside(nodeId) {
  if (view.layout === '1' && active) setLayout('2v', true);
  if (view.layout !== '1') {
    const free = view.panes.indexOf(null);
    const other = view.panes.findIndex(id => id !== active);
    view.focus = free >= 0 ? free : other >= 0 ? other : view.focus;
  }
  return openSession(nodeId);
}

// ticketFor asks once for a number when any of the sessions needs one:
// '' when none is needed, null when the user cancels.
async function ticketFor(nodes) {
  const need = [...new Map(nodes.filter(n => n && !isNewerType(n) && needsTicket(n)).map(n => [n.id, n])).values()];
  return need.length ? askTicket(need) : '';
}

export async function openMany(nodeIds) {
  const newer = nodeIds.filter(id => isNewerType(state.byId.get(id)));
  const ticket = await ticketFor(nodeIds.map(id => state.byId.get(id)));
  if (ticket === null) return;
  for (const id of nodeIds) if (!newer.includes(id)) await openSession(id, { ticket });
  if (newer.length) toast(`${plural(newer.length, 'session uses', 'sessions use')} a connection type from a newer version of YACM and ${newer.length === 1 ? 'was' : 'were'} not opened.`, 'info', 6000);
}

export async function reconnect(id) {
  const t = tabs.get(id);
  if (t && !isTerm(t)) { t.reconnect?.(); return; }
  if (!t || t.state === 'connected' || t.state === 'connecting') return;
  t.hideOverlay();
  t.waiting = null;
  t.state = 'connecting';
  t.summary = '';
  t.term.write('\r\n');
  renderTabbar();
  try {
    // Saved sessions may have been edited since: refresh the node.
    t.node = state.byId.get(t.node.id) || t.node;
    t.setupHighlight();
    t.paintLabel();
    const approve = await confirmBefore(t.node);
    await api.Connect({ nodeId: '', tabId: id, cols: t.term.cols, rows: t.term.rows, approveBefore: approve, quick: '' });
  } catch (e) {
    t.state = 'error';
    t.log(errMsg(e));
    renderTabbar();
  }
}

export function closeTab(id) {
  const t = tabs.get(id);
  if (!t) return;
  if (isTerm(t)) api.CloseTab(id).catch(() => {});
  t.dispose();
  tabs.delete(id);
  broadcast.ids.delete(id);
  const idx = order.indexOf(id);
  order = order.filter(x => x !== id);
  const pane = view.panes.indexOf(id);
  if (pane >= 0) view.panes[pane] = null;
  if (active === id) {
    active = null;
    if (view.layout === '1') {
      const next = order[Math.min(idx, order.length - 1)];
      if (next) { activate(next); return finishClose(); }
    }
  }
  if (!tabs.size) setLayout('1', true);
  applyView();
  finishClose();
}

function finishClose() {
  renderTabbar();
  renderStatus();
  renderBroadcast();
}

// ---- output alerts ------------------------------------------------------------

// isWatched: the session is on screen and YACM has the focus.
function isWatched(t) {
  return document.hasFocus() && !document.hidden && view.panes.includes(t.id);
}

const ALERT_CSS = { red: 'var(--c-red)', green: 'var(--c-green)', yellow: 'var(--c-yellow)', blue: 'var(--c-blue)', magenta: 'var(--c-purple)', cyan: 'var(--c-teal)' };
const alertColor = c => ALERT_CSS[(c || 'red').replace('bright-', '')] || 'var(--danger)';

// Alerts that arrive while an alert message is up join that message, so a
// reload that takes down neighbours in many sessions shows one message
// instead of a pile of them. The message stays 12 s after the last alert.
/** @type {{ el: HTMLElement, list: { t: any, rule: any, line: string }[], timer: any } | null} */
let alertBox = null;
const ALERT_ROWS = 4;

function raiseAlert(t, rule, line) {
  t.alert = { color: rule.color, line };
  renderTabbar();
  const first = !alertBox;
  if (!alertBox) {
    const el = h('div', { class: 'toast alert', role: 'status' });
    document.getElementById('toasts').append(el);
    alertBox = { el, list: [], timer: 0 };
  }
  alertBox.list.push({ t, rule, line });
  renderAlertBox();
  clearTimeout(alertBox.timer);
  alertBox.timer = setTimeout(closeAlertBox, 12000);
  if (first) {
    if (!document.hasFocus()) api.RequestAttention().catch(() => {});
    if (state.settings.alertSound) beep();
  }
}

function closeAlertBox() {
  if (!alertBox) return;
  clearTimeout(alertBox.timer);
  alertBox.el.remove();
  alertBox = null;
}

function renderAlertBox() {
  if (!alertBox) return;
  const { el, list } = alertBox;
  const show = a => { closeAlertBox(); activate(a.t.id); setTimeout(() => a.t.search.findPrevious(a.line.slice(0, 120)), 50); };
  const off = h('button', { class: 'btn small', text: 'Turn off…', title: 'Stop these alerts', on: { click: e => alertOffMenu(e, list) } });
  const close = h('button', { class: 'close', text: '×', title: 'Close', 'aria-label': 'Close the alert', on: { click: closeAlertBox } });
  const row = (a, last) => h('div', { class: 'alert-toast' },
    h('span', { class: 'alert-dot', style: { background: alertColor(a.rule.color) } }),
    h('span', { class: 'alert-text' }, h('b', { text: a.t.node.name }), ': ', a.line.length > 160 ? a.line.slice(0, 159) + '…' : a.line),
    h('button', { class: 'btn small', text: 'Show', on: { click: () => show(a) } }),
    ...(last ? [off, close] : []));
  if (list.length === 1) { el.replaceChildren(row(list[0], true)); return; }
  const shown = list.slice(-ALERT_ROWS);
  const sessions = new Set(list.map(a => a.t.id)).size;
  el.replaceChildren(...[
    h('div', { class: 'alert-head' }, h('b', { text: `${plural(list.length, 'alert', 'alerts')} from ${plural(sessions, 'session', 'sessions')}` }), off, close),
    ...shown.map(a => row(a, false)),
    list.length > shown.length ? h('div', { class: 'hint', text: `and ${list.length - shown.length} more; their tabs have a dot` }) : null,
  ].filter(Boolean));
}

// alertOffMenu turns alerts off for the tabs in the message, for its kinds
// of message (their rules) or altogether, right from the message.
function alertOffMenu(e, list) {
  const r = e.currentTarget.getBoundingClientRect();
  const tabsIn = [...new Set(list.map(a => a.t))];
  const ruleIds = new Set(list.map(a => a.rule.id));
  const done = msg => { closeAlertBox(); toast(msg); };
  const save = async (patch, msg) => {
    try {
      await saveSettings(patch);
      applySettings();
      done(msg);
    } catch (err) { toast(errMsg(err), 'error'); }
  };
  contextMenu(r.left, r.bottom + 2, [
    { label: tabsIn.length === 1 ? `For ${tabsIn[0].node.name} until its tab closes` : `For these ${tabsIn.length} sessions until their tabs close`, action: () => {
      for (const t of tabsIn) { t.alertsMuted = true; t.alertRules = []; }
      done(tabsIn.length === 1 ? `No more alerts from ${tabsIn[0].node.name} while its tab is open.` : `No more alerts from these ${tabsIn.length} sessions while their tabs are open.`);
    } },
    { label: ruleIds.size === 1 ? 'For messages like this one' : 'For messages like these', title: 'Takes Alert off the rules in Settings > Highlighting; the colours stay',
      action: () => save({ highlightRules: (state.settings.highlightRules || []).map(x => (ruleIds.has(x.id) ? { ...x, alert: false } : x)) },
        `${ruleIds.size === 1 ? 'That rule no longer alerts' : 'Those rules no longer alert'}. Settings > Highlighting turns alerts on again.`) },
    { label: 'All alerts', action: () => save({ noAlerts: true }, 'Alerts are off. Settings > Highlighting turns them on again.') },
  ]);
}

let audio = null;
function beep() {
  try {
    audio ||= new AudioContext();
    const o = audio.createOscillator();
    const g = audio.createGain();
    o.frequency.value = 880;
    g.gain.setValueAtTime(0.08, audio.currentTime);
    g.gain.exponentialRampToValueAtTime(0.001, audio.currentTime + 0.25);
    o.connect(g).connect(audio.destination);
    o.start();
    o.stop(audio.currentTime + 0.25);
  } catch { /* no sound available */ }
}

// focusTab makes a visible tab active without moving it between panes.
function focusTab(id) {
  const i = view.panes.indexOf(id);
  if (i < 0 || (active === id && view.focus === i)) return;
  view.focus = i;
  active = id;
  const ft = tabs.get(id);
  if (ft) ft.alert = null;
  for (const t of tabs.values()) t.el.classList.toggle('focused', t.id === id);
  document.querySelectorAll('.pane-empty').forEach(p => p.classList.remove('focused'));
  renderTabbar();
  renderStatus();
}

// activate shows a tab: in the pane where it already is, or in the focused pane.
export function activate(id) {
  if (!tabs.has(id)) return;
  const at = view.panes.indexOf(id);
  if (at >= 0) view.focus = at;
  else view.panes[view.focus] = id;
  active = id;
  const at2 = tabs.get(id);
  if (at2) at2.alert = null;
  applyView();
  const t = tabs.get(id);
  requestAnimationFrame(() => { fitVisible(); if (!anyDialogOpen()) t.focus(); });
}

// placeInPane shows a tab in pane "target". A tab already visible elsewhere
// swaps places with what the target pane showed.
function placeInPane(id, target) {
  if (!tabs.has(id) || target < 0 || target >= view.panes.length) return;
  const from = view.panes.indexOf(id);
  if (from === target) return;
  const other = view.panes[target];
  view.panes[target] = id;
  if (from >= 0) view.panes[from] = other;
  view.focus = target;
  active = id;
  applyView();
  const t = tabs.get(id);
  requestAnimationFrame(() => { fitVisible(); if (!anyDialogOpen()) t.focus(); });
}

// moveItems are the "Move to … pane" menu entries for a tab in split view.
function moveItems(id) {
  if (view.layout === '1') return [];
  const from = view.panes.indexOf(id);
  return PANE_NAMES[view.layout].map((name, i) => {
    const other = view.panes[i] && view.panes[i] !== id ? tabs.get(view.panes[i]) : null;
    return {
      label: from >= 0 && other ? `Swap with ${other.node.name} (${name} pane)` : `Move to the ${name} pane`,
      key: i === view.panes.indexOf(id) || from < 0 || NEIGHBOURS[view.layout]?.[from] === undefined ? undefined : arrowKeyFor(from, i),
      disabled: i === from,
      action: () => placeInPane(id, i),
    };
  });
}

let dragId = null;
function startDrag(e, id) {
  dragId = id;
  e.dataTransfer.setData(DRAG_TYPE, id);
  e.dataTransfer.effectAllowed = 'move';
  document.getElementById('terms').classList.add('dragging-tab');
}
function endDrag() {
  dragId = null;
  document.getElementById('terms').classList.remove('dragging-tab');
  document.querySelectorAll('.drop-target').forEach(x => x.classList.remove('drop-target'));
}

// dropTarget lets a tab (from the tab bar or a pane label) be dropped on a
// pane; paneOf returns the pane index of the element.
function dropTarget(el, paneOf) {
  const ok = e => dragId && view.layout !== '1' && paneOf() >= 0 && [...(e.dataTransfer?.types || [])].includes(DRAG_TYPE);
  el.addEventListener('dragover', e => { if (!ok(e)) return; e.preventDefault(); e.dataTransfer.dropEffect = 'move'; el.classList.add('drop-target'); });
  el.addEventListener('dragleave', e => { if (!el.contains(e.relatedTarget)) el.classList.remove('drop-target'); });
  el.addEventListener('drop', e => {
    if (!ok(e)) return;
    e.preventDefault();
    e.stopPropagation();
    const id = e.dataTransfer.getData(DRAG_TYPE) || dragId;
    const pane = paneOf();
    endDrag();
    placeInPane(id, pane);
  });
}

// arrowKeyFor shows the shortcut that moves from pane "from" to pane "to".
function arrowKeyFor(from, to) {
  const dir = Object.entries(NEIGHBOURS[view.layout]?.[from] || {}).find(([, p]) => p === to)?.[0];
  if (!dir) return undefined;
  const arrow = { ArrowLeft: '←', ArrowRight: '→', ArrowUp: '↑', ArrowDown: '↓' }[dir];
  return IS_MAC ? `⌘⌥${arrow}` : `Ctrl+Shift+Alt+${arrow}`;
}

// paneMoveKey: Ctrl+Shift+Alt+arrow (Cmd+Alt+arrow on a Mac) moves the
// focused session to the next pane in that direction.
function paneMoveKey(e) {
  if (!e.key.startsWith('Arrow')) return false;
  return IS_MAC ? (e.metaKey && e.altKey && !e.ctrlKey) : (e.ctrlKey && e.shiftKey && e.altKey && !e.metaKey);
}

function unpane(id) {
  const i = view.panes.indexOf(id);
  if (i < 0) return;
  view.panes[i] = null;
  view.focus = i;
  if (active === id) active = null;
  applyView();
}

export function setLayout(layout, quiet) {
  if (!LAYOUTS[layout]) return;
  const count = LAYOUTS[layout].length;
  const keep = view.panes.filter(Boolean);
  if (active && !keep.includes(active)) keep.unshift(active);
  // Fill the new panes with what was visible, then the next tabs in order.
  const panes = keep.slice(0, count);
  for (const id of order) {
    if (panes.length >= count) break;
    if (!panes.includes(id)) panes.push(id);
  }
  while (panes.length < count) panes.push(null);
  view.layout = layout;
  view.panes = panes;
  view.focus = Math.max(0, panes.indexOf(active));
  if (!panes.includes(active)) active = panes[view.focus] || null;
  if (!quiet) applyView();
  requestAnimationFrame(fitVisible);
}

function applyView() {
  const box = document.getElementById('terms');
  const split = view.layout !== '1';
  box.classList.toggle('split', split);
  box.querySelectorAll('.pane-empty').forEach(p => p.remove());
  const rects = LAYOUTS[view.layout];
  const gap = split ? 3 : 0;
  const place = (el, [x, y, w, hgt]) => {
    if (!split) { el.style.left = el.style.top = el.style.width = el.style.height = ''; return; }
    el.style.left = `calc(${x * 100}% + ${x ? gap : 0}px)`;
    el.style.top = `calc(${y * 100}% + ${y ? gap : 0}px)`;
    el.style.width = `calc(${w * 100}% - ${x ? gap : 0}px - ${x + w < 1 ? gap : 0}px)`;
    el.style.height = `calc(${hgt * 100}% - ${y ? gap : 0}px - ${y + hgt < 1 ? gap : 0}px)`;
  };
  for (const [tid, t] of tabs) {
    const i = view.panes.indexOf(tid);
    t.el.hidden = i < 0;
    t.el.classList.toggle('focused', split && tid === active);
    if (i >= 0) place(t.el, rects[i]);
  }
  if (split) {
    view.panes.forEach((id, i) => {
      if (id) return;
      const ph = h('div', { class: `pane-empty${i === view.focus ? ' focused' : ''}`, text: 'Click a tab above or drag it here' });
      ph.addEventListener('click', () => { view.focus = i; active = null; applyView(); });
      dropTarget(ph, () => i);
      place(ph, rects[i]);
      ph.style.inset = 'auto';
      box.append(ph);
    });
  }
  document.getElementById('empty').hidden = tabs.size > 0;
  const tools = document.getElementById('layout-tools');
  tools.hidden = tabs.size < 2 && view.layout === '1';
  /** @type {NodeListOf<HTMLElement>} */ (tools.querySelectorAll('.layout-btn')).forEach(b => b.classList.toggle('on', b.dataset.layout === view.layout));
  renderTabbar();
  renderStatus();
  requestAnimationFrame(fitVisible);
}

export function hasTabs() { return tabs.size > 0; }

// fontWeights: Settings > Appearance > Font weight; device bold is three
// steps heavier.
export function fontWeights() {
  const w = { light: 300, medium: 500 }[state.settings?.fontWeight] || 400;
  return { fontWeight: w, fontWeightBold: Math.min(w + 300, 800) };
}

export function applySettings() {
  const weights = fontWeights();
  for (const t of tabs.values()) {
    t.applyColors();
    if (!isTerm(t)) { t.paintLabel(); continue; }
    if (t.term.options.fontWeight !== weights.fontWeight) Object.assign(t.term.options, weights);
    const base = nodeNow(t).session?.fontSize || state.settings.fontSize || 14;
    if (t.term.options.fontSize === t.baseFont) t.term.options.fontSize = base;
    t.baseFont = base;
    t.setupHighlight();
    t.paintLabel();
    t.fitAndResize();
  }
  renderTabbar();
}

// commandTargets is used by the command library.
export function commandTargets() {
  const t = tabs.get(active);
  if (!isTerm(t) || t.state !== 'connected') return { ids: [], label: '' };
  if (broadcast.on && broadcast.ids.has(t.id)) {
    const ids = [...broadcast.ids].filter(id => tabs.get(id)?.state === 'connected');
    return { ids, label: plural(ids.length, 'session', 'sessions'), macros: t.node.session?.macros || [], library: !!t.node.library };
  }
  return { ids: [t.id], label: t.node.name, macros: t.node.session?.macros || [], nodeId: t.node.id, library: !!t.node.library };
}

export function pasteDelayFor(id) { return tabs.get(id)?.pasteDelay() || 0; }

// ---------- tab bar ----------

// renamed refreshes the tree, tab labels and panes after a session rename.
async function renamed() {
  await reload();
  for (const t of tabs.values()) {
    const fresh = state.byId.get(t.node.id);
    if (fresh) { t.node = fresh; t.paintLabel(); }
  }
  renderTabbar();
  renderStatus();
  window.dispatchEvent(new Event('yacm:tree'));
}

function renderTabbar() {
  rememberTabs();
  const bar = document.getElementById('tabbar');
  bar.replaceChildren();
  const split = view.layout !== '1';
  for (const id of order) {
    const t = tabs.get(id);
    const proto = t.node.session?.protocol || 'ssh';
    const color = colorOf(nodeNow(t));
    const el = h('div', {
      class: `tab${id === active ? ' active' : ''}${split && view.panes.includes(id) ? ' visible' : ''}${broadcast.on && broadcast.ids.has(id) ? ' in-broadcast' : ''}`,
      role: 'tab', 'aria-selected': String(id === active), title: `${t.node.name} (${t.waiting ? 'waiting for the device' : t.state})`,
      style: { '--proto': `var(--${proto})` },
    },
      h('span', { class: `led ${t.waiting ? 'waiting' : t.state}` }),
      isTerm(t) ? portGlyph(proto) : icon(t.kind, `port ${t.kind}`),
      h('span', { class: 'title', text: t.node.name }),
      t.alert ? h('span', { class: 'alert-dot', title: `Alert: ${t.alert.line}`, style: { background: alertColor(t.alert.color) } }) : null,
      h('button', { class: 'close', title: `Close tab (${KEY.close})`, 'aria-label': 'Close tab', text: '×', on: { click: e => { e.stopPropagation(); requestClose(id); } } }));
    if (color) el.dataset.color = color;
    el.draggable = true;
    el.addEventListener('dragstart', e => startDrag(e, id));
    el.addEventListener('dragend', endDrag);
    // Dropping a tab on another tab moves it there in the tab bar.
    el.addEventListener('dragover', e => { if (dragId && dragId !== id) { e.preventDefault(); el.classList.add('drop-before'); } });
    el.addEventListener('dragleave', () => el.classList.remove('drop-before'));
    el.addEventListener('drop', e => {
      if (!dragId || dragId === id) return;
      e.preventDefault();
      const moving = dragId;
      endDrag();
      order = order.filter(x => x !== moving);
      order.splice(order.indexOf(id), 0, moving);
      renderTabbar();
      if (broadcast.on) renderBroadcast();
    });
    el.addEventListener('mousedown', e => { if (e.button === 1) { e.preventDefault(); requestClose(id); } });
    el.addEventListener('click', () => activate(id));
    el.addEventListener('contextmenu', e => {
      e.preventDefault();
      const panes = LAYOUTS[view.layout].length;
      if (!isTerm(t)) {
        contextMenu(e.clientX, e.clientY, [
          ...t.menuItems(),
          '-',
          view.layout === '1'
            ? { label: 'Show next to the current tab', action: () => { setLayout('2v'); activate(id); }, disabled: order.length < 2 || id === active }
            : { label: panes > 1 && view.panes.includes(id) ? 'Remove from split view' : 'Show in the selected pane', action: () => (view.panes.includes(id) ? unpane(id) : activate(id)) },
          ...moveItems(id),
          '-',
          { label: 'Close other tabs', action: () => order.filter(x => x !== id).forEach(closeTab) },
          { label: 'Close tab', key: KEY.close, action: () => requestClose(id) },
        ]);
        return;
      }
      const serial = t.node.session?.protocol === 'serial';
      contextMenu(e.clientX, e.clientY, [
        { label: 'Clone session', action: () => cloneSession(id), disabled: serial || isNewerType(t.node), title: serial ? 'A serial port can be open in one tab only' : 'A second connection to the same device' },
        { label: 'Files and tunnels', items: fileItems(t) },
        breakItem(t),
        '-',
        { label: 'Reconnect', action: () => reconnect(id), disabled: t.state === 'connected' || t.state === 'connecting' },
        { label: 'Disconnect', action: () => api.Disconnect(id), disabled: t.state !== 'connected' && t.state !== 'connecting' },
        t.waiting
          ? { label: 'Stop waiting for the device', action: () => api.StopWaiting(id) }
          : { label: 'Wait for the device and reconnect', action: () => api.WaitForDevice(id).catch(err => toast(errMsg(err), 'error')), disabled: t.state === 'connected' || t.state === 'connecting' || serial || !!t.quick, title: 'After a reload: tries the device every few seconds and reconnects when it answers' },
        '-',
        view.layout === '1'
          ? { label: 'Show next to the current tab', action: () => { setLayout('2v'); activate(id); }, disabled: order.length < 2 || id === active }
          : { label: panes > 1 && view.panes.includes(id) ? 'Remove from split view' : 'Show in the selected pane', action: () => (view.panes.includes(id) ? unpane(id) : activate(id)) },
        ...moveItems(id),
        '-',
        { label: 'Close other tabs', action: () => order.filter(x => x !== id).forEach(closeTab) },
        { label: 'Close tab', key: KEY.close, action: () => requestClose(id) },
      ]);
    });
    bar.append(el);
  }
}

// fileItems are the file transfer entries of a terminal tab (menus, the
// Files button). files.js is loaded on first use.
function fileItems(t) {
  const ssh = t.node.session?.protocol === 'ssh';
  const live = ssh && t.state === 'connected';
  const why = !ssh ? 'Needs an SSH session. For telnet and serial, let the device copy from the file servers (next entry)' : live ? undefined : 'Connect first';
  const run = fn => () => import('./files.js').then(m => m[fn](t.id)).catch(e => toast(errMsg(e), 'error'));
  return [
    { label: 'Send file…', action: run('sendFile'), disabled: !live, title: why || 'Copy files from this computer to the device (SCP or SFTP, through the jump hosts)' },
    { label: 'Get file…', action: run('getFile'), disabled: !live, title: why || 'Copy a file from the device to this computer' },
    { label: 'Save command output to file…', action: run('commandToFile'), disabled: t.state !== 'connected', title: t.state === 'connected' ? 'Types one command (show tech-support, show running-config …) into the session and saves what it prints, --More-- answered' : 'Connect first' },
    { label: 'Browse files', action: run('openFilesFromTab'), disabled: !ssh || isNewerType(t.node), title: !ssh ? why : 'Opens a Files tab: this computer and the device side by side' },
    { label: 'Tunnels…', action: () => import('./tunnels.js').then(m => m.openTunnels(t.id)).catch(e => toast(errMsg(e), 'error')), disabled: !ssh, title: ssh ? 'Port forwards through this connection (-L, -R, SOCKS/HTTP proxy)' : 'Needs an SSH session' },
    '-',
    { label: 'File servers…', action: () => import('./servers.js').then(m => m.openServers({ tabId: t.id })).catch(e => toast(errMsg(e), 'error')), title: 'TFTP, HTTP and SCP servers on this computer the device copies from and to, with the command to type' },
  ];
}

// sendBreak sends a break to the device: serial line break, telnet BRK or
// SSH break request (console servers pass it on to the console port).
function sendBreak(t) {
  if (!t || t.state !== 'connected') return;
  api.SendBreak(t.id).then(() => toast('Break sent')).catch(e => toast(errMsg(e), 'error'));
}

function breakItem(t) {
  return { label: 'Send break', key: KEY.brk, action: () => sendBreak(t), disabled: t.state !== 'connected', title: 'Serial line break, telnet BRK or SSH break request: ROMMON and password recovery on the console' };
}

// isBreakKey: Ctrl+Break (Windows, Linux) or Cmd+. (macOS).
function isBreakKey(e) {
  if (IS_MAC) return e.metaKey && !e.ctrlKey && !e.altKey && e.key === '.';
  return e.ctrlKey && !e.altKey && !e.metaKey && (e.code === 'Pause' || e.key === 'Cancel' || e.key === 'Pause');
}

async function requestClose(id) {
  const t = tabs.get(id);
  if (!t) return;
  if (!isTerm(t)) {
    const busy = t.busy?.();
    if (busy) {
      const ok = await confirmDialog({ title: `Stop ${busy}?`, message: 'Closing the tab cancels the running transfers.', ok: 'Stop and close', danger: true });
      if (!ok) return;
    }
    closeTab(id);
    return;
  }
  if (t.state === 'connected') {
    const ok = await confirmDialog({ title: `Disconnect ${t.node.name}?`, message: 'The session is still connected.', ok: 'Disconnect and close', danger: true });
    if (!ok) return;
  }
  closeTab(id);
}

// ---------- status bar ----------

function facts(t) {
  const proto = t.node.session?.protocol;
  const out = [];
  if (t.waiting) return [['Status', 'waiting for the device to answer again']];
  if (t.state === 'connecting') return [['Status', 'connecting…']];
  if (t.state !== 'connected') return [['Status', t.message || t.state]];
  if (!t.summary) return t.message ? [['Status', t.message]] : [];
  if (proto === 'ssh') {
    // One short fact; the details (rarely looked at) are in its tooltip.
    const [kex, hostKey, cipher, server] = t.summary.split(' · ');
    const detail = [`Key exchange: ${kex}`, `Host key: ${hostKey}`, `Cipher: ${cipher}`, server ? `Server: ${server}` : ''].filter(Boolean).join('\n');
    out.push(['SSH', server || cipher || 'encrypted', false, detail]);
  } else if (proto === 'telnet') {
    const via = t.summary.match(/through jump host (\S+)/);
    out.push(['Encryption', via ? `SSH up to the jump host ${via[1]}, clear text from there to the device` : 'none, telnet sends everything in clear text', true]);
  } else {
    out.push(['Line', t.summary.replace(/^serial /, '').replace(' · ', ', ')]);
  }
  return out;
}

// menuAbove opens a menu above the button that was clicked (the status bar
// is at the bottom of the window).
function menuAbove(btn, items) {
  const r = btn.getBoundingClientRect();
  const m = contextMenu(r.left, r.top, items);
  m.style.top = `${Math.max(4, r.top - m.getBoundingClientRect().height - 4)}px`;
}

function renderStatus() {
  const bar = document.getElementById('statusbar');
  const t = tabs.get(active);
  bar.hidden = !t;
  if (!t) return;
  bar.replaceChildren();
  if (!isTerm(t)) { t.renderStatus(bar); return; }
  const f = h('div', { class: 'facts' });
  for (const [label, value, clear, detail] of facts(t)) {
    f.append(h('span', { class: `fact${clear ? ' cleartext' : ''}`, title: detail || value }, h('b', { text: label }), h('span', { text: value })));
  }
  bar.append(f);
  const ex = t.expect;
  if (ex && ex.state !== 'done' && ex.state !== 'stopped') {
    bar.append(h('span', { class: 'expect', title: ex.pattern }, `Automation ${ex.index + 1} of ${ex.total}: waiting for ${ex.pattern}`),
      h('button', { class: 'text-btn', text: 'Stop', on: { click: () => api.StopExpect(t.id) } }));
  }
  if (t.pasting) {
    const job = t.pasting;
    bar.append(h('span', { class: 'paste-progress', text: `Pasting line ${Math.min(job.i + 1, job.n)} of ${job.n}` }),
      h('button', { class: 'text-btn', text: 'Stop', on: { click: () => { job.stop = true; } } }));
  }
  const tools = h('div', { class: 'tools' });
  const proto = t.node.session?.protocol;
  const sep = () => h('span', { class: 'tool-sep', 'aria-hidden': 'true' });
  if (t.state === 'connected') {
    tools.append(h('button', { class: 'text-btn', text: 'Commands…', title: `Send a command from the library or this session’s saved commands (${KEY.palette})`, on: { click: openPalette } }));
    tools.append(h('button', { class: 'text-btn', text: 'Files ▾', title: 'Send or get files, show tech to a file, the Files tab, tunnels and file servers', on: { click: e => menuAbove(e.currentTarget, fileItems(t)) } }));
    if (proto === 'ssh' && ((t.tunnels || []).length || (nodeNow(t).session?.tunnels || []).length)) {
      const run = (t.tunnels || []).filter(x => x.state === 'running').length;
      const bad = (t.tunnels || []).some(x => x.state === 'error');
      tools.append(h('button', { class: `text-btn${bad ? ' warn-btn' : ''}`, text: run ? `Tunnels (${run})` : 'Tunnels', title: bad ? 'A tunnel failed: click for details' : 'Port forwards through this connection (-L, -R, SOCKS/HTTP proxy)', on: { click: () => import('./tunnels.js').then(m => m.openTunnels(t.id)).catch(e => toast(errMsg(e), 'error')) } }));
    }
    // Break on the status bar of a serial console, where it is used most;
    // for SSH and telnet (console servers) it is in the tab and terminal
    // menus and on Ctrl+Break.
    if (proto === 'serial') {
      tools.append(h('button', { class: 'text-btn', text: 'Break', title: `Send a break (${KEY.brk}): ROMMON and password recovery`, on: { click: () => sendBreak(t) } }));
    }
  } else if (t.state !== 'connecting') {
    tools.append(h('button', { class: 'text-btn', text: 'Reconnect', title: KEY.reconnect, on: { click: () => reconnect(t.id) } }));
  }
  tools.append(sep());
  for (const a of t.adhoc || []) {
    tools.append(h('span', { class: `adhoc-chip c-${a.color}`, title: 'Highlighted in this session. Click for options.' },
      h('button', { class: 'word', text: a.text.length > 24 ? a.text.slice(0, 23) + '…' : a.text, on: { click: e => menuAbove(e.currentTarget, [
        { label: 'Keep as a highlighting rule', action: () => keepAdhoc(t, a) },
        { label: 'Remove', action: () => t.removeAdhoc(a) },
      ]) } }),
      h('button', { class: 'x', text: '×', title: 'Remove', 'aria-label': `Stop highlighting ${a.text}`, on: { click: () => t.removeAdhoc(a) } })));
  }
  tools.append(h('button', { class: 'text-btn', text: 'Find', title: KEY.find, on: { click: () => find(t) } }));
  tools.append(h('button', { class: 'text-btn', text: 'Highlight', title: 'Highlight a word in this session (/…/ for a regular expression)', on: { click: () => adhocInput(t) } }));
  tools.append(sep());
  tools.append(h('button', { class: 'text-btn', text: broadcast.on ? 'Stop typing to many' : 'Type to many', title: KEY.broadcast, on: { click: toggleBroadcast } }));
  // Font size of this tab: one control; the wheel over it changes the size.
  const fs = t.term.options.fontSize;
  const changed = fs !== t.baseFont;
  const aa = h('button', { class: 'text-btn font-size-btn', 'aria-label': `Font size ${fs}`, title: `Font size of this tab${changed ? ` (the session opens with ${t.baseFont})` : ''}: click for options, ${KEY.wheel} or ${KEY.zoomIn} / ${KEY.zoomOut}`, style: { color: changed ? 'var(--accent)' : '' } },
    h('span', { class: 'aa', text: 'Aa' }), h('span', { text: String(fs) }));
  aa.addEventListener('click', () => menuAbove(aa, [
    { label: 'Bigger', key: KEY.zoomIn, action: () => t.zoom(1) },
    { label: 'Smaller', key: KEY.zoomOut, action: () => t.zoom(-1) },
    '-',
    { label: `Reset to ${t.baseFont}`, key: KEY.zoomReset, action: () => t.zoom(0), disabled: !changed },
    { label: `Keep ${fs} for this session`, action: () => t.keepFontSize(), disabled: !changed || !state.byId.has(t.node.id) || !!t.node.library },
  ]));
  aa.addEventListener('wheel', e => { e.preventDefault(); t.zoom(e.deltaY < 0 ? 1 : -1); }, { passive: false });
  tools.append(aa);
  bar.append(tools);
}

// adhocInput asks for a word to highlight, in the status bar like Find.
function adhocInput(t) {
  const bar = document.getElementById('statusbar');
  const existing = /** @type {HTMLInputElement} */ (bar.querySelector('.adhoc-input'));
  if (existing) { existing.focus(); return; }
  const inp = h('input', { class: 'inline-input adhoc-input', placeholder: 'Word to highlight, Enter adds it (/…/ = regular expression)', style: { maxWidth: '340px' }, spellcheck: false });
  inp.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); t.addAdhoc(inp.value); inp.value = ''; if (t.id === active) { renderStatus(); adhocInput(t); } }
    if (e.key === 'Escape') { renderStatus(); t.term.focus(); }
  });
  bar.querySelector('.facts').replaceChildren(inp);
  inp.focus();
}

// keepAdhoc turns an ad hoc word into a highlighting rule in Settings.
async function keepAdhoc(t, a) {
  const rules = [{ id: crypto.randomUUID(), enabled: true, pattern: a.pattern, color: `bright-${a.color}`, bold: true, background: false, ignoreCase: true }, ...(state.settings.highlightRules || [])];
  try {
    await saveSettings({ highlightRules: rules, highlight: true });
    t.removeAdhoc(a);
    applySettings();
    toast(`“${a.text}” is now a highlighting rule (Settings > Highlighting).`);
  } catch (e) { toast(errMsg(e), 'error'); }
}

function find(t) {
  const bar = document.getElementById('statusbar');
  const existing = /** @type {HTMLInputElement} */ (bar.querySelector('.find'));
  if (existing) { existing.focus(); existing.select(); return; }
  const inp = h('input', { class: 'inline-input find', placeholder: 'Find in scrollback (Enter = next, Shift+Enter = previous)', style: { maxWidth: '340px' } });
  inp.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); e.shiftKey ? t.search.findPrevious(inp.value) : t.search.findNext(inp.value); }
    if (e.key === 'Escape') { inp.remove(); t.search.clearDecorations?.(); t.term.focus(); }
  });
  bar.querySelector('.facts').replaceChildren(inp);
  inp.focus();
}

// ---------- broadcast ("type to many") ----------

export function toggleBroadcast() {
  broadcast.on = !broadcast.on;
  if (broadcast.on && broadcast.ids.size === 0) {
    const cur = tabs.get(active);
    const cl = new Set(cur?.node.session?.clusters || []);
    for (const [id, t] of tabs) {
      if (!isTerm(t) || t.state !== 'connected') continue;
      if (!cl.size || (t.node.session?.clusters || []).some(c => cl.has(c))) broadcast.ids.add(id);
    }
    if (cur) broadcast.ids.add(cur.id);
  }
  renderBroadcast();
  renderTabbar();
  renderStatus();
  tabs.get(active)?.focus();
}

function renderBroadcast() {
  const box = document.getElementById('broadcast');
  box.hidden = !broadcast.on;
  if (!broadcast.on) return;
  box.replaceChildren(h('strong', { text: 'Typing goes to:' }));
  // "All" checks or clears every open tab. Tabs opened later never join by
  // themselves: "All" then shows as partly checked, so a command never goes
  // to a device the user has not seen ticked.
  const all = h('input', { type: 'checkbox', 'aria-label': 'All sessions' });
  const terms = order.filter(id => isTerm(tabs.get(id)));
  const syncAll = () => {
    const n = terms.filter(id => broadcast.ids.has(id)).length;
    all.checked = n > 0 && n === terms.length;
    all.indeterminate = n > 0 && n < terms.length;
  };
  // After a click on a check box the keys go back to the terminal: a space
  // would otherwise tick or clear the box instead of reaching the devices.
  const back = () => tabs.get(active)?.focus();
  all.addEventListener('change', () => {
    if (all.checked) terms.forEach(id => broadcast.ids.add(id)); else broadcast.ids.clear();
    renderBroadcast();
    renderTabbar();
    back();
  });
  box.append(h('label', { class: 'all', title: 'Check or clear every open session' }, all, 'All'));
  for (const id of terms) {
    const t = tabs.get(id);
    const cb = h('input', { type: 'checkbox', checked: broadcast.ids.has(id) });
    cb.addEventListener('change', () => { cb.checked ? broadcast.ids.add(id) : broadcast.ids.delete(id); syncAll(); renderTabbar(); back(); });
    box.append(h('label', { title: t.state === 'connected' ? t.node.name : `${t.node.name} (${t.state}): receives nothing until it is connected` }, cb, t.node.name));
  }
  syncAll();
  const line = h('input', { class: 'inline-input', placeholder: 'Send one command to the checked sessions', spellcheck: false,
    title: 'Enter sends the command. While the field is empty, Space, Enter and Ctrl+C go to the sessions too, so --More-- pages on.' });
  const send = d => api.Broadcast([...broadcast.ids].filter(id => tabs.get(id)?.state === 'connected'), d);
  line.addEventListener('keydown', e => {
    if (e.isComposing) return;
    if (e.key === 'Enter') {
      e.preventDefault();
      send(line.value.trim() ? line.value + '\r' : '\r');
      line.value = '';
      return;
    }
    // An empty field passes the keys a pager waits for (--More--) to the
    // sessions; a command never starts with a space.
    if (line.value) return;
    if (e.key === ' ' && !e.ctrlKey && !e.metaKey && !e.altKey) { e.preventDefault(); send(' '); }
    else if (e.ctrlKey && !e.metaKey && !e.altKey && e.key.toLowerCase() === 'c') { e.preventDefault(); send('\x03'); }
  });
  box.append(line, h('button', { class: 'btn small', text: 'Done', on: { click: toggleBroadcast } }));
}

// ---------- keyboard ----------

function globalKeys(e) {
  if (anyDialogOpen()) return;
  if (paneMoveKey(e) && view.layout !== '1' && active) {
    e.preventDefault();
    e.stopPropagation();
    const from = view.panes.indexOf(active);
    const to = NEIGHBOURS[view.layout]?.[from]?.[e.key];
    if (to !== undefined) placeInPane(active, to);
    return;
  }
  if (isBreakKey(e)) {
    const cur = tabs.get(active);
    if (isTerm(cur)) { e.preventDefault(); sendBreak(cur); }
    return;
  }
  const k = e.key.toLowerCase();
  if (e.ctrlKey && e.key === 'Tab' && order.length) {
    e.preventDefault();
    const i = order.indexOf(active);
    activate(order[(i + (e.shiftKey ? -1 : 1) + order.length) % order.length]);
    return;
  }
  const sk = shortcut(e);
  if (sk) {
    const cur = tabs.get(active);
    const t = isTerm(cur) ? cur : null;
    if ((sk === 'prevtab' || sk === 'nexttab') && order.length) {
      e.preventDefault();
      const i = order.indexOf(active);
      activate(order[(i + (sk === 'prevtab' ? -1 : 1) + order.length) % order.length]);
    }
    if (sk === 'w' && cur) { e.preventDefault(); requestClose(cur.id); }
    if (sk === 'f' && t) { e.preventDefault(); find(t); }
    if (sk === 'b' && t) { e.preventDefault(); toggleBroadcast(); }
    if (sk === 'r' && cur) { e.preventDefault(); reconnect(cur.id); }
    if (sk === 'palette' && t) { e.preventDefault(); openPalette(); }
    if (sk === '+' && t) { e.preventDefault(); t.zoom(1); }
    if (sk === '-' && t) { e.preventDefault(); t.zoom(-1); }
    if (sk === '0' && t) { e.preventDefault(); t.zoom(0); }
  }
}
