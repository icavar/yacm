// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.

// Keyword highlighting for terminal output.
//
// Text between escape sequences is matched against the rules and the
// matches are wrapped in ANSI colour codes before the data reaches xterm.
// The basic ANSI colours are used, so highlights follow the terminal theme.
// Escape sequences (colours the device sends, cursor movement, OSC titles)
// pass through untouched, also when they are split across chunks.

const FG = { red: 31, green: 32, yellow: 33, blue: 34, magenta: 35, cyan: 36, white: 37 };

export const HL_COLORS = ['red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'bright-red', 'bright-green', 'bright-yellow', 'bright-blue', 'bright-magenta', 'bright-cyan'];

function sgr(rule) {
  const bright = rule.color?.startsWith('bright-');
  const base = FG[(rule.color || 'yellow').replace('bright-', '')] || 33;
  const open = [];
  const close = [];
  if (rule.background) {
    open.push(base + (bright ? 70 : 10)); // 41..46 or 101..106
    open.push(30); // dark text on the coloured background
    close.push(49, 39);
  } else {
    open.push(bright ? base + 60 : base);
    close.push(39);
  }
  if (rule.bold) { open.push(1); close.push(22); }
  return { open: `\x1b[${open.join(';')}m`, close: `\x1b[${close.join(';')}m` };
}

// compileRules returns the usable rules; invalid patterns are reported in
// errors (index -> message) and skipped.
export function compileRules(rules) {
  const out = [];
  const errors = {};
  (rules || []).forEach((r, i) => {
    if (!r.enabled || !r.pattern) return;
    try {
      const re = new RegExp(r.pattern, 'g' + (r.ignoreCase ? 'i' : ''));
      if (re.test('')) { errors[i] = 'matches empty text'; return; }
      out.push({ re, ...sgr(r) });
    } catch (e) {
      errors[i] = e.message;
    }
  });
  return { rules: out, errors };
}

// compileAlerts returns the enabled rules marked "Alert" (case as set).
export function compileAlerts(rules) {
  const out = [];
  for (const r of rules || []) {
    if (!r.enabled || !r.alert || !r.pattern) continue;
    try {
      const re = new RegExp(r.pattern, r.ignoreCase ? 'i' : '');
      if (!re.test('')) out.push({ id: r.id, re, color: r.color || 'red' });
    } catch { /* reported in Settings */ }
  }
  return out;
}

// stripAnsi removes escape sequences, for matching alert rules on lines.
const ANSI = /\x1b\[[0-9;?<=>]*[ -\/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]|\x1b[()][0-9A-Za-z]/g;
export function stripAnsi(s) { return s.replace(ANSI, ''); }

const NORMAL = 0, ESC = 1, CSI = 2, OSC = 3, STR = 4, ESC_INTER = 5, OSC_ESC = 6, STR_ESC = 7;

export class Highlighter {
  constructor(rules) {
    this.rules = compileRules(rules).rules;
    this.state = NORMAL;
  }

  get active() { return this.rules.length > 0; }

  // colour one run of plain text
  paint(text) {
    if (!text || !this.rules.length) return text;
    const hits = [];
    this.rules.forEach((r, prio) => {
      r.re.lastIndex = 0;
      let m;
      let guard = 0;
      while ((m = r.re.exec(text)) && guard++ < 5000) {
        if (m[0] === '') { r.re.lastIndex++; continue; }
        hits.push([m.index, m.index + m[0].length, prio]);
      }
    });
    if (!hits.length) return text;
    hits.sort((a, b) => a[0] - b[0] || a[2] - b[2]);
    let out = '';
    let pos = 0;
    for (const [s, e, prio] of hits) {
      if (s < pos) continue; // overlaps an earlier, higher-priority match
      const r = this.rules[prio];
      out += text.slice(pos, s) + r.open + text.slice(s, e) + r.close;
      pos = e;
    }
    return out + text.slice(pos);
  }

  process(data) {
    if (!this.rules.length) return data;
    let out = '';
    let textStart = -1;
    const flush = i => {
      if (textStart >= 0) { out += this.paint(data.slice(textStart, i)); textStart = -1; }
    };
    for (let i = 0; i < data.length; i++) {
      const c = data.charCodeAt(i);
      switch (this.state) {
        case NORMAL:
          if (c === 0x1b) { flush(i); this.state = ESC; out += data[i]; }
          else if (c === 0x9b) { flush(i); this.state = CSI; out += data[i]; }
          else if (textStart < 0) textStart = i;
          break;
        case ESC:
          out += data[i];
          if (c === 0x5b) this.state = CSI; // [
          else if (c === 0x5d) this.state = OSC; // ]
          else if (c === 0x50 || c === 0x58 || c === 0x5e || c === 0x5f) this.state = STR; // P X ^ _
          else if (c >= 0x20 && c <= 0x2f) this.state = ESC_INTER; // ESC ( B and friends
          else this.state = NORMAL;
          break;
        case ESC_INTER:
          out += data[i];
          if (c >= 0x30 && c <= 0x7e) this.state = NORMAL;
          break;
        case CSI:
          out += data[i];
          if (c >= 0x40 && c <= 0x7e) this.state = NORMAL;
          break;
        case OSC:
          out += data[i];
          if (c === 0x07) this.state = NORMAL;
          else if (c === 0x1b) this.state = OSC_ESC;
          break;
        case OSC_ESC:
          out += data[i];
          this.state = c === 0x5c ? NORMAL : OSC;
          break;
        case STR:
          out += data[i];
          if (c === 0x1b) this.state = STR_ESC;
          break;
        case STR_ESC:
          out += data[i];
          this.state = c === 0x5c ? NORMAL : STR;
          break;
      }
    }
    flush(data.length);
    return out;
  }
}
