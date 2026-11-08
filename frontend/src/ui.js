// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Small DOM helpers, dialogs, menus and toasts.

/**
 * h creates an element. Typed loosely (any): call sites read .value,
 * .checked and the like on inputs.
 * @param {string} tag
 * @param {Record<string, any>} [attrs]
 * @param {...any} children
 * @returns {any}
 */
export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k === 'html') el.innerHTML = v; // only used with static markup
    else if (k === 'on') for (const [ev, fn] of Object.entries(v)) el.addEventListener(ev, fn);
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (k in el && typeof v !== 'string') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

export function errMsg(e) {
  let m = !e ? 'Unknown error' : typeof e === 'string' ? e : e.message || String(e);
  return m.charAt(0).toUpperCase() + m.slice(1);
}

/** @param {any} msg @param {string} [kind] @param {number} [ms] */
export function toast(msg, kind = 'info', ms = 4500) {
  const t = h('div', { class: `toast ${kind}`, role: kind === 'error' ? 'alert' : 'status' }, msg);
  document.getElementById('toasts').append(t);
  setTimeout(() => t.remove(), ms);
}

let openDialogs = [];

/**
 * @typedef {object} DialogButton
 * @property {string} label
 * @property {string} [kind] primary, danger
 * @property {boolean} [default] pressed by Enter
 * @property {any} [value] passed to onClose
 * @property {(dlg: any) => any} [onClick] return false to keep the dialog open
 */
/**
 * @typedef {object} DialogOptions
 * @property {string} title
 * @property {any} [sub]
 * @property {any} [body]
 * @property {DialogButton[]} [buttons]
 * @property {boolean} [wide]
 * @property {boolean} [medium]
 * @property {(result?: any) => void} [onClose]
 * @property {any} [left]
 * @property {any} [initialFocus]
 */
// openDialog returns { el, body, close, setError, setBusy }.
/** @param {DialogOptions} opts */
export function openDialog({ title, sub, body, buttons = [], wide = false, medium = false, onClose, left, initialFocus }) {
  const errorLine = h('div', { class: 'error-line', role: 'alert' });
  const footer = h('footer', {}, left ? h('div', { class: 'left' }, left) : null);
  const dlg = h('div', { class: `dialog${wide ? ' wide' : medium ? ' medium' : ''}`, role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
    h('header', {}, h('h2', { text: title }), sub ? h('p', { class: 'sub' }, sub) : null),
    wide ? body : h('div', { class: 'body' }, body, errorLine),
    footer);
  if (wide) footer.prepend(errorLine), errorLine.style.marginRight = 'auto';
  const scrim = h('div', { class: 'scrim' }, dlg);
  let closed = false;
  const api = {
    el: dlg,
    close(result) {
      if (closed) return;
      closed = true;
      scrim.remove();
      openDialogs = openDialogs.filter(d => d !== api);
      onClose && onClose(result);
    },
    setError(msg) { errorLine.textContent = msg || ''; },
    setBusy(b) { footer.querySelectorAll('button').forEach(x => (x.disabled = b)); },
    isOpen: () => !closed,
  };
  for (const b of buttons) {
    const btn = h('button', { class: `btn ${b.kind || ''}`, type: 'button', text: b.label });
    btn.addEventListener('click', async () => {
      if (!b.onClick) return api.close(b.value);
      api.setError('');
      try {
        api.setBusy(true);
        const r = await b.onClick(api);
        if (r !== false) api.close(b.value);
      } catch (e) {
        api.setError(errMsg(e));
      } finally {
        api.setBusy(false);
      }
    });
    if (b.default) btn.dataset.default = '1';
    footer.append(btn);
  }
  dlg.addEventListener('keydown', e => {
    if (e.key === 'Escape') { e.stopPropagation(); api.close(); }
    if (e.key === 'Enter' && !e.shiftKey && e.target.tagName !== 'TEXTAREA' && e.target.tagName !== 'BUTTON') {
      const d = footer.querySelector('button[data-default]');
      if (d && !d.disabled) { e.preventDefault(); d.click(); }
    }
  });
  document.getElementById('layer').append(scrim);
  openDialogs.push(api);
  // Focus at once, so keys typed right after opening (Escape, a search
  // word) reach the dialog; again after layout in case it was not ready.
  const focusFirst = () => {
    if (closed || dlg.contains(document.activeElement)) return;
    const f = initialFocus || dlg.querySelector('input:not([type=checkbox]):not([disabled]), select, textarea, button[data-default]');
    // Without a field to focus, the dialog itself takes focus so Escape works.
    if (f) f.focus(); else { dlg.tabIndex = -1; dlg.focus(); }
  };
  focusFirst();
  requestAnimationFrame(focusFirst);
  return api;
}

// Escape closes the top dialog even when the keyboard focus is outside it.
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape' || !openDialogs.length) return;
  if (/** @type {Element} */ (e.target).closest?.('.dialog, .menu')) return;
  e.preventDefault();
  openDialogs[openDialogs.length - 1].close();
});

export function anyDialogOpen() { return openDialogs.length > 0; }

/** @param {{ title: string, message: any, ok?: string, danger?: boolean, cancel?: string }} opts @returns {Promise<boolean>} */
export function confirmDialog({ title, message, ok = 'OK', danger = false, cancel = 'Cancel' }) {
  return new Promise(resolve => {
    openDialog({
      title, body: typeof message === 'string' ? h('p', { text: message }) : message,
      buttons: [{ label: cancel, value: false }, { label: ok, kind: danger ? 'danger' : 'primary', value: true, default: true }],
      onClose: v => resolve(!!v),
    });
  });
}

let menuEl = null;
let subEl = null;
export function closeMenu() { closeSub(); if (menuEl) { menuEl.remove(); menuEl = null; } }
function closeSub() { if (subEl) { subEl.remove(); subEl = null; } }

// buildMenu fills a menu element. An item with items (instead of action)
// opens a submenu to its right: on hover, click or the right arrow key.
function buildMenu(m, items, sub) {
  for (const it of items) {
    if (it === '-') { m.append(h('hr')); continue; }
    if (!it) continue;
    const b = h('button', { role: 'menuitem', class: [it.danger ? 'danger' : '', it.items ? 'has-sub' : ''].filter(Boolean).join(' '), disabled: it.disabled, title: it.title, 'aria-haspopup': it.items ? 'menu' : undefined },
      h('span', { text: it.label }), it.items ? h('span', { class: 'sub-caret', text: '▸' }) : it.key ? h('kbd', { text: it.key }) : null);
    if (it.items) {
      const open = focus => {
        if (subEl?.dataset.owner === it.label) { if (focus) subEl.querySelector('button:not([disabled])')?.focus(); return; }
        closeSub();
        const s = h('div', { class: 'menu submenu', role: 'menu' });
        s.dataset.owner = it.label;
        buildMenu(s, it.items, true);
        document.body.append(s);
        const r = b.getBoundingClientRect();
        const sr = s.getBoundingClientRect();
        const left = r.right + 2 + sr.width > innerWidth - 4 ? r.left - sr.width - 2 : r.right + 2;
        s.style.left = Math.max(4, left) + 'px';
        s.style.top = Math.max(4, Math.min(r.top - 4, innerHeight - sr.height - 4)) + 'px';
        subEl = s;
        s.addEventListener('keydown', e => {
          const btns = [...s.querySelectorAll('button:not([disabled])')];
          const i = btns.indexOf(document.activeElement);
          if (e.key === 'ArrowDown') { e.preventDefault(); btns[(i + 1) % btns.length]?.focus(); }
          if (e.key === 'ArrowUp') { e.preventDefault(); btns[(i - 1 + btns.length) % btns.length]?.focus(); }
          if (e.key === 'ArrowLeft' || e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeSub(); b.focus(); }
        });
        if (focus) s.querySelector('button:not([disabled])')?.focus();
      };
      b.addEventListener('mouseenter', () => { if (!b.disabled) open(false); });
      b.addEventListener('click', () => open(true));
      b.addEventListener('keydown', e => { if (e.key === 'ArrowRight' || e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); open(true); } });
    } else {
      if (!sub) b.addEventListener('mouseenter', closeSub);
      b.addEventListener('click', () => { closeMenu(); it.action(); });
    }
    m.append(b);
  }
}

/** @param {number} x @param {number} y @param {any[]} items @returns {any} */
export function contextMenu(x, y, items) {
  closeMenu();
  const m = h('div', { class: 'menu', role: 'menu' });
  buildMenu(m, items, false);
  document.body.append(m);
  const r = m.getBoundingClientRect();
  m.style.left = Math.max(4, Math.min(x, innerWidth - r.width - 4)) + 'px';
  m.style.top = Math.max(4, Math.min(y, innerHeight - r.height - 4)) + 'px';
  menuEl = m;
  m.querySelector('button:not([disabled])')?.focus();
  m.addEventListener('keydown', e => {
    const btns = [...m.querySelectorAll('button:not([disabled])')];
    const i = btns.indexOf(document.activeElement);
    if (e.key === 'ArrowDown') { e.preventDefault(); btns[(i + 1) % btns.length]?.focus(); }
    if (e.key === 'ArrowUp') { e.preventDefault(); btns[(i - 1 + btns.length) % btns.length]?.focus(); }
    if (e.key === 'Escape') closeMenu();
  });
  return m;
}
document.addEventListener('mousedown', e => { if (menuEl && !menuEl.contains(e.target) && !subEl?.contains(e.target)) closeMenu(); });
window.addEventListener('blur', closeMenu);

// Text fields get Cut, Copy, Paste and Select all on right click (the
// webview's own menu is off). Elements with a menu of their own (the
// terminal) prevent the default first and are left alone.
const TEXT_TYPES = new Set(['text', 'search', 'password', 'url', 'email', 'number', 'tel', '']);
/** @param {{ copy: (t: string) => any, paste: () => Promise<string> }} clip */
export function textFieldMenus(clip) {
  document.addEventListener('contextmenu', e => {
    if (e.defaultPrevented) return;
    const el = /** @type {HTMLInputElement | HTMLTextAreaElement} */ (/** @type {Element} */ (e.target).closest?.('input, textarea'));
    if (!el || el.closest('.xterm') || (el instanceof HTMLInputElement && !TEXT_TYPES.has(el.type))) return;
    e.preventDefault();
    let start = null, end = null;
    try { start = el.selectionStart; end = el.selectionEnd; } catch { /* number fields have no selection */ }
    const sel = start !== null && end !== null ? el.value.slice(start, end) : '';
    const secret = el instanceof HTMLInputElement && el.type === 'password';
    const editable = !el.readOnly && !el.disabled;
    const changed = () => el.dispatchEvent(new Event('input', { bubbles: true }));
    const put = text => {
      el.focus();
      if (start !== null && end !== null) { try { el.setRangeText(text, start, end, 'end'); } catch { el.value = text; } } else el.value = text;
      changed();
    };
    contextMenu(e.clientX, e.clientY, [
      { label: 'Cut', action: () => { clip.copy(sel); put(''); }, disabled: !sel || secret || !editable },
      { label: 'Copy', action: () => clip.copy(sel), disabled: !sel || secret },
      { label: 'Paste', action: async () => { const t = await clip.paste().catch(() => ''); if (t) put(el instanceof HTMLInputElement ? t.replace(/\r?\n/g, ' ').trim() : t); }, disabled: !editable },
      '-',
      { label: 'Select all', action: () => { el.focus(); el.select(); }, disabled: !el.value },
    ]);
  });
}

export function field(label, input, hint) {
  return h('label', { class: 'field' }, h('span', { text: label }), input, hint ? h('span', { class: 'hint' }, hint) : null);
}

export function check(label, input, small) {
  return h('label', { class: 'check' }, input, h('span', {}, label, small ? h('small', { text: small }) : null));
}

export function seg(options, value, onChange) {
  const wrap = h('div', { class: 'seg', role: 'radiogroup' });
  const set = v => {
    value = v;
    wrap.querySelectorAll('button').forEach(b => {
      const on = b.dataset.v === v;
      b.classList.toggle('on', on);
      b.setAttribute('aria-checked', on);
    });
  };
  for (const o of options) {
    const b = h('button', { type: 'button', role: 'radio', dataset: { v: o.value } }, o.icon || null, o.label);
    b.addEventListener('click', () => { set(o.value); onChange(o.value); });
    wrap.append(b);
  }
  set(value);
  wrap.setValue = set;
  return wrap;
}

// ---- connection type icons -------------------------------------------------
// One SVG sprite: every connection type has its own shape, so types differ
// without colour too (colour-blind users, print). Types of later releases are
// drawn already; an unknown type gets the dashed "newer YACM" icon.
const ICON_SHAPES = {
  ssh: '<rect x="1.5" y="2.5" width="13" height="10" rx="1.5"/><path d="M4 6l2 1.5L4 9M7.5 9.5h2"/><rect x="10.2" y="9.6" width="4.6" height="3.9" rx=".8" fill="currentColor" stroke="none"/><path d="M11.3 9.6V8.7a1.2 1.2 0 0 1 2.4 0v.9" stroke-width="1.2"/>',
  telnet: '<rect x="1.5" y="2.5" width="13" height="10" rx="1.5" stroke-dasharray="2.2 1.6"/><path d="M4 6l2 1.5L4 9M7.5 9.5h3"/>',
  serial: '<path d="M2 4.5h12l-1.6 6H3.6z"/><circle cx="5.2" cy="6.8" r=".55" fill="currentColor" stroke="none"/><circle cx="8" cy="6.8" r=".55" fill="currentColor" stroke="none"/><circle cx="10.8" cy="6.8" r=".55" fill="currentColor" stroke="none"/><circle cx="6.6" cy="8.7" r=".55" fill="currentColor" stroke="none"/><circle cx="9.4" cy="8.7" r=".55" fill="currentColor" stroke="none"/><path d="M8 10.5v3"/>',
  rdp: '<rect x="1.5" y="2" width="13" height="9" rx="1.2"/><path d="M5.5 14h5M8 11v3"/><path d="M4.5 5h4M4.5 7.5h6" stroke-width="1.2"/>',
  vnc: '<rect x="1.5" y="2" width="13" height="9" rx="1.2"/><path d="M5.5 14h5M8 11v3"/><path d="M7 4.2l3.8 2.4-1.7.4.9 1.8-.9.4-.9-1.8-1.2 1.1z" fill="currentColor" stroke-width=".6"/>',
  web: '<circle cx="8" cy="8" r="6"/><path d="M2 8h12M8 2c1.8 1.7 2.6 3.7 2.6 6S9.8 12.3 8 14C6.2 12.3 5.4 10.3 5.4 8S6.2 3.7 8 2z"/>',
  local: '<rect x="2.5" y="3" width="11" height="7.5" rx="1"/><path d="M1 13h14"/><path d="M5 5.5l1.5 1.2L5 8M7.5 8h2" stroke-width="1.2"/>',
  container: '<path d="M8 1.8l5.5 3.1v6.2L8 14.2l-5.5-3.1V4.9z"/><path d="M2.5 4.9L8 8l5.5-3.1M8 8v6.2"/>',
  ipmi: '<rect x="1.5" y="2.5" width="13" height="4.5" rx="1"/><rect x="1.5" y="9" width="13" height="4.5" rx="1"/><path d="M4 4.75h.01M4 11.25h.01" stroke-width="2"/><path d="M8 11.25h4M8 4.75h4" stroke-width="1.2"/>',
  newer: '<rect x="1.5" y="2.5" width="13" height="11" rx="1.5" stroke-dasharray="2 2"/><path d="M6.5 6.4a1.6 1.6 0 1 1 2.2 1.5c-.5.2-.7.6-.7 1.1M8 11h.01"/>',
  files: '<path d="M2 4.5V12a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3.5H3a1 1 0 0 0-1 1z"/>',
};
const SVG_NS = 'http://www.w3.org/2000/svg';
function ensureIconSprite() {
  if (document.getElementById('yacm-icons')) return;
  const sprite = document.createElementNS(SVG_NS, 'svg');
  sprite.id = 'yacm-icons';
  sprite.setAttribute('aria-hidden', 'true');
  sprite.style.position = 'absolute';
  sprite.style.width = sprite.style.height = '0';
  sprite.innerHTML = '<defs>' + Object.entries(ICON_SHAPES).map(([k, v]) => `<symbol id="icon-${k}" viewBox="0 0 16 16">${v}</symbol>`).join('') + '</defs>';
  document.body.prepend(sprite);
}

// icon returns an SVG icon by name (see ICON_SHAPES).
export function icon(name, cls = '') {
  ensureIconSprite();
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('class', `icon ${cls}`.trim());
  svg.setAttribute('viewBox', '0 0 16 16');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS(SVG_NS, 'use');
  use.setAttribute('href', `#icon-${ICON_SHAPES[name] ? name : 'newer'}`);
  svg.append(use);
  return svg;
}

// portGlyph is the icon of a connection type (coloured by type).
export function portGlyph(proto) {
  // Only the types this version can open; a type from a newer YACM gets the
  // dashed question mark even when its shape is drawn already.
  const known = ['ssh', 'telnet', 'serial'].includes(proto);
  const svg = icon(known ? proto : 'newer', `port ${known ? proto : 'unknown'}`);
  return svg;
}

export const protoLabel = { ssh: 'SSH', telnet: 'Telnet', serial: 'Serial' };

export function uuid() {
  return crypto.randomUUID();
}

// colorPicker: swatches for the node colours plus "inherit"/"none".
/** @param {string} value @param {(v: string) => void} onChange @param {{ colors: string[], labels: Record<string, string>, allowInherit?: boolean, inheritText?: string }} opts */
export function colorPicker(value, onChange, { colors, labels, allowInherit = true, inheritText = 'From group' }) {
  const wrap = h('div', { class: 'swatches', role: 'radiogroup' });
  const opts = [...(allowInherit ? [['', inheritText]] : []), ['none', 'None'], ...colors.map(c => [c, labels[c]])];
  const set = v => wrap.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.v === v));
  for (const [v, label] of opts) {
    const b = h('button', { type: 'button', role: 'radio', title: label, 'aria-label': label, class: v === '' || v === 'none' ? 'none' : '', dataset: { v } }, v === '' || v === 'none' ? label : null);
    if (v && v !== 'none') b.dataset.color = v;
    b.addEventListener('click', () => { set(v); onChange(v); });
    wrap.append(b);
  }
  set(value || '');
  return wrap;
}

export function plural(n, one, many) { return `${n} ${n === 1 ? one : many}`; }

// busyDialog shows a progress bar for slow work. It appears only after a
// moment, so quick steps do not flash a dialog. update(done, total, step)
// switches from "working" to a percentage.
/** @param {string} title @param {{ cancel?: () => void, text?: string }} [opts] */
export function busyDialog(title, { cancel, text = 'Working…' } = {}) {
  const fill = h('div', { class: 'fill' });
  const bar = h('div', { class: 'progress indeterminate', role: 'progressbar', 'aria-valuemin': '0', 'aria-valuemax': '100' }, fill);
  const msg = h('p', { class: 'busy-text', text });
  let dlg = null;
  let finished = false;
  const timer = setTimeout(() => {
    dlg = openDialog({
      title, body: h('div', { class: 'busy' }, msg, bar),
      buttons: cancel ? [{ label: 'Cancel', onClick: () => { cancel(); msg.textContent = 'Stopping…'; return false; } }] : [],
      onClose: () => { if (!finished && cancel) cancel(); },
    });
  }, 300);
  return {
    update(done, total, step) {
      if (!total) return;
      const pct = Math.min(100, Math.round((done / total) * 100));
      bar.classList.remove('indeterminate');
      bar.setAttribute('aria-valuenow', String(pct));
      fill.style.width = `${pct}%`;
      msg.textContent = `${step || 'Working'}… ${done} / ${total} (${pct} %)`;
    },
    close() { finished = true; clearTimeout(timer); dlg?.close(); },
  };
}

// ---- keyboard shortcuts ----------------------------------------------------
// App shortcuts are Ctrl+Shift+<key> on Windows and Linux (plain Ctrl+<key>
// belongs to the device: Ctrl+C, Ctrl+W, …) and Cmd+<key> on macOS, where
// Ctrl stays with the device as well.
export const IS_MAC = /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent || '');

export const KEY = IS_MAC ? {
  copy: '⌘C', paste: '⌘V', close: '⌘W', find: '⌘F', broadcast: '⌘B', reconnect: '⌘R',
  palette: '⌘K', switcher: '⌘O', brk: '⌘.', zoomIn: '⌘+', zoomOut: '⌘−', zoomReset: '⌘0', wheel: '⌘+wheel', newSession: '⌘N',
} : {
  copy: 'Ctrl+Shift+C', paste: 'Ctrl+Shift+V', close: 'Ctrl+Shift+W', find: 'Ctrl+Shift+F', broadcast: 'Ctrl+Shift+B', reconnect: 'Ctrl+Shift+R',
  palette: 'Ctrl+Shift+Space', switcher: 'Ctrl+Shift+O', brk: 'Ctrl+Break', zoomIn: 'Ctrl+Shift++', zoomOut: 'Ctrl+Shift+−', zoomReset: 'Ctrl+Shift+0', wheel: 'Ctrl+wheel', newSession: 'Ctrl+Shift+N',
};

// shortcut returns the key of an app shortcut ('c', 'v', 'w', '+', '-', '0',
// 'palette', …) or null when the keys belong to the terminal.
export function shortcut(e) {
  const mod = IS_MAC ? (e.metaKey && !e.ctrlKey && !e.altKey) : (e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey);
  if (!mod) return null;
  if (e.code === 'Space' || e.key === ' ') return IS_MAC ? null : 'palette';
  if (IS_MAC && e.key.toLowerCase() === 'k') return 'palette';
  if (e.code === 'Equal' || e.key === '+' || e.key === '=') return '+';
  if (e.code === 'Minus' || e.key === '-' || e.key === '_') return '-';
  if (e.code === 'Digit0' || e.key === '0' || e.key === ')') return '0';
  if (IS_MAC && e.shiftKey && (e.code === 'BracketLeft' || e.code === 'BracketRight')) return e.code === 'BracketLeft' ? 'prevtab' : 'nexttab';
  return e.key.toLowerCase();
}

// zoomWheel tells whether a wheel event should change the font size.
export function zoomWheel(e) { return e.ctrlKey || (IS_MAC && e.metaKey); }
