// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Terminal text and background colours: per session, as a group default
// and as the default in Settings. Values are "#rrggbb", "theme" (the
// theme's own colour) or "" (take it from the group, then Settings).
import { state } from './api.js';
import { h } from './ui.js';

export const THEME_COLORS = {
  dark: { text: '#d9dfe5', bg: '#14181c' },
  light: { text: '#1f262d', bg: '#fbfcfd' },
};

export const TEXT_PRESETS = [
  ['#ffffff', 'White'], ['#d9dfe5', 'Light grey'], ['#9ad9b3', 'Green'], ['#33ff66', 'Phosphor green'],
  ['#ffb000', 'Amber'], ['#9cc0ff', 'Light blue'], ['#1f262d', 'Near black'], ['#000000', 'Black'],
];

// Dark backgrounds that work with both themes, plus white for light work.
export const BG_PRESETS = [
  ['#3a1214', 'Dark red (production)'], ['#3a2410', 'Dark orange'], ['#0f2a1a', 'Dark green (lab)'], ['#0f1d33', 'Dark blue'],
  ['#2a1736', 'Dark purple'], ['#000000', 'Black'], ['#ffffff', 'White'],
];

const isHex = v => /^#[0-9a-f]{6}$/i.test(v || '');

function themeName() { return state.settings.theme === 'light' ? 'light' : 'dark'; }

// resolve returns the colour a node uses for one part ("termText" or
// "termBackground"): { value: hex or '' (theme), from: 'session' | group
// node | 'settings' | 'theme' }. parentId lets the editor resolve a node
// that is not saved yet.
export function resolve(node, key, parentId = node?.parentId) {
  const own = node && !node.isGroup ? node.session?.[key] : node?.isGroup ? node.defaults?.[key] : '';
  if (own) return { value: isHex(own) ? own : '', from: own === 'theme' ? 'theme' : 'session' };
  for (let g = state.byId.get(parentId), i = 0; g && i < 64; g = state.byId.get(g.parentId), i++) {
    const v = g.defaults?.[key];
    if (v) return { value: isHex(v) ? v : '', from: g };
  }
  const st = state.settings[key];
  return { value: isHex(st) ? st : '', from: 'settings' };
}

// colorsFor returns the actual text and background colours of a node.
export function colorsFor(node) {
  const t = THEME_COLORS[themeName()];
  return { text: resolve(node, 'termText').value || t.text, bg: resolve(node, 'termBackground').value || t.bg };
}

// xtermTheme merges a node's colours into an xterm theme.
export function xtermTheme(base, node) {
  const { text, bg } = colorsFor(node);
  return { ...base, foreground: text, background: bg, cursorAccent: bg };
}

function luminance(hex) {
  const c = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(v => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}

export function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

// picker edits one colour value. inheritText names what "" means here.
/** @param {string} value @param {(v: string) => void} onChange @param {{ kind: 'text' | 'bg', inheritText?: string, allowInherit?: boolean }} opts */
export function picker(value, onChange, { kind, inheritText = 'Default', allowInherit = true }) {
  const presets = kind === 'text' ? TEXT_PRESETS : BG_PRESETS;
  const wrap = h('div', { class: 'swatches term-swatches', role: 'radiogroup' });
  const custom = h('input', { type: 'color', title: 'Choose any colour', 'aria-label': 'Choose any colour' });
  const set = v => {
    value = v;
    wrap.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.v === v));
    const isCustom = isHex(v) && !presets.some(([c]) => c.toLowerCase() === v.toLowerCase());
    custom.classList.toggle('on', isCustom);
    if (isHex(v)) custom.value = v.toLowerCase();
  };
  const opts = [...(allowInherit ? [['', inheritText]] : []), ['theme', 'Theme']];
  for (const [v, label] of opts) {
    const b = h('button', { type: 'button', role: 'radio', class: 'none', dataset: { v }, text: label, title: v === 'theme' ? 'The colour of the Dark or Light theme' : label });
    b.addEventListener('click', () => { set(v); onChange(v); });
    wrap.append(b);
  }
  for (const [c, label] of presets) {
    const b = h('button', { type: 'button', role: 'radio', title: label, 'aria-label': label, dataset: { v: c }, style: { background: c, borderColor: c.toLowerCase() === '#ffffff' || c === '#000000' ? 'var(--line)' : '' } });
    b.addEventListener('click', () => { set(c); onChange(c); });
    wrap.append(b);
  }
  custom.addEventListener('input', () => { set(custom.value); onChange(custom.value); });
  wrap.append(h('label', { class: 'custom-color', title: 'Choose any colour' }, custom));
  set(value || (allowInherit ? '' : 'theme'));
  wrap.setValue = set;
  return wrap;
}

// preview shows a few lines in the chosen colours and warns about weak
// contrast. get() returns { text, bg } as hex and may also give the font
// weight (300, 400, 500), with a bold line showing how device bold looks,
// and the font size in px.
export function preview(get) {
  const box = h('pre', { class: 'term-preview' });
  const warn = h('span', { class: 'hint warn-text' });
  const sample = 'core-sw1#show ip interface brief\nVlan10     10.0.10.1   YES NVRAM  up    up\n';
  const update = () => {
    const { text, bg, weight, size } = get();
    const bold = h('span', { text: '%LINK-3-UPDOWN: Interface Gi1/0/1, changed state to up' });
    box.replaceChildren(sample, ...(weight ? [bold, '\n'] : []), 'core-sw1#');
    box.style.fontWeight = weight ? String(weight) : '';
    if (weight) bold.style.fontWeight = String(Math.min(weight + 300, 800));
    // A cleared size goes back to the default instead of keeping the last one.
    box.style.fontSize = size ? `${Math.min(Math.max(size, 6), 40)}px` : '';
    box.style.color = text;
    box.style.background = bg;
    const r = contrast(text, bg);
    warn.textContent = r < 4.5 ? `The text is hard to read on this background (contrast ${r.toFixed(1)}:1; 4.5:1 or more is recommended).` : '';
  };
  update();
  return { el: h('div', { class: 'term-preview-wrap' }, box, warn), update };
}

// sourceText describes where an inherited colour comes from.
export function sourceText(r) {
  if (r.from === 'settings') return r.value ? 'Settings' : 'theme';
  if (r.from === 'theme') return 'theme';
  if (r.from && r.from.name) return `group ${r.from.name}`;
  return '';
}
