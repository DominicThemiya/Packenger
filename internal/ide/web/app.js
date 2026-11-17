'use strict';
// pkide: the Packenger IDE. One editor (a textarea over a highlighted
// <pre>), a completion popup driven by the server's grammar-aware engine,
// a console for runs, and a manager for Packenger builds.

let TOKEN = document.querySelector('meta[name="pkide-token"]').content;
const $ = (sel, el = document) => el.querySelector(sel);

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'html') el.innerHTML = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

const esc = s => s.replace(/[&<>]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]);

// renewToken asks the server for the page again and takes its token: the
// server was restarted, and unsaved work stays where it is.
async function renewToken() {
  const page = await (await fetch('/', { cache: 'no-store' })).text();
  const m = page.match(/name="pkide-token" content="(\w+)"/);
  if (m) TOKEN = m[1];
  return !!m;
}

async function api(path, body, retried) {
  let res;
  try {
    res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Pkide-Token': TOKEN },
      body: JSON.stringify(body || {}),
    });
  } catch {
    throw new Error('pkide isn’t answering: start it with pkide, then try again');
  }
  if (res.status === 403 && !retried && await renewToken().catch(() => false)) return api(path, body, true);
  let data = null;
  try { data = await res.json(); } catch { /* not json */ }
  if (!res.ok) throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  return data;
}

const store = {
  get(k, d) { try { const v = localStorage.getItem(k); return v == null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* private mode */ } },
};

// ------------------------------------------------------------------ state

const S = {
  ws: '',
  state: null,        // server state: builds, repo, recent
  profile: null,      // this workspace's build, target, registry
  tabs: [],
  active: null,
  dirs: new Map(),    // dir -> entries
  open: new Set(['']),
  comp: null,         // the last completion response, with where it was asked
  compSeq: 0,
  sel: 0,             // selected completion item
  shown: [],          // completion items shown
  snippet: null,
  proc: null,         // the process the console follows
  procDir: '',
  installPrompt: null,
};

function status(msg, cls) {
  const el = $('#statusMsg');
  el.textContent = msg || '';
  el.className = cls || '';
  clearTimeout(status.t);
  if (msg && cls !== 'bad') status.t = setTimeout(() => { el.textContent = ''; }, 5000);
}

function rel(path) { return path; }
function baseName(p) { return p.split('/').pop(); }
function dirName(p) { const i = p.lastIndexOf('/'); return i < 0 ? '' : p.slice(0, i); }
function absPath(p) { return p.startsWith('/') ? p : S.ws + '/' + p; }

// ------------------------------------------------------------------ editor

const ED = {};

function buildEditor() {
  ED.root = h('div', { class: 'ed', id: 'ed' });
  ED.paper = h('div', { class: 'paper' });
  ED.gutter = h('div', { class: 'gutter', 'aria-hidden': 'true' });
  ED.layer = h('div', { class: 'layer' });
  ED.cur = h('div', { class: 'curline' });
  ED.pre = h('pre', { 'aria-hidden': 'true' });
  ED.ta = h('textarea', { spellcheck: 'false', autocapitalize: 'off', autocomplete: 'off', wrap: 'off', 'aria-label': 'Editor' });
  ED.layer.append(ED.cur, ED.pre, ED.ta);
  ED.paper.append(ED.gutter, ED.layer);
  ED.root.append(ED.paper);
  $('#editorHost').append(ED.root);
  measure();

  const ta = ED.ta;
  ta.addEventListener('input', onInput);
  ta.addEventListener('keydown', onKey);
  ta.addEventListener('keyup', e => { if (/Arrow|Home|End|Page/.test(e.key)) caretMoved(); });
  ta.addEventListener('mouseup', caretMoved);
  ta.addEventListener('scroll', () => { ta.scrollTop = 0; ta.scrollLeft = 0; });
  ta.addEventListener('blur', () => setTimeout(() => { if (document.activeElement !== ta) hideComplete(); }, 150));
  ta.addEventListener('mousemove', onHoverMove);
  ta.addEventListener('mouseleave', e => {
    clearTimeout(ED.hoverT);
    if (!(e.relatedTarget && e.relatedTarget.closest && e.relatedTarget.closest('#hover'))) hideHover();
  });
  $('#hover').addEventListener('mouseleave', hideHover);
  ta.addEventListener('click', e => { if (e.ctrlKey || e.metaKey) goToDefinition(); });
  ED.root.addEventListener('scroll', () => { hideHover(); positionComplete(); });
}

function measure() {
  const c = document.createElement('canvas').getContext('2d');
  const cs = getComputedStyle(ED.root);
  c.font = `${cs.fontSize} ${cs.fontFamily}`;
  ED.cw = c.measureText('MMMMMMMMMM').width / 10;
  ED.lh = parseFloat(cs.lineHeight) || 20;
  ED.padL = 12;
}

function tab() { return S.active; }

function lineStarts(text) {
  const out = [0];
  for (let i = 0; i < text.length; i++) if (text.charCodeAt(i) === 10) out.push(i + 1);
  return out;
}

function posOf(text, off) {
  const line = text.slice(0, off).split('\n').length - 1;
  const col = off - (text.lastIndexOf('\n', off - 1) + 1);
  return { line, col };
}

function offsetOf(text, line, col) {
  const starts = lineStarts(text);
  line = Math.max(0, Math.min(line, starts.length - 1));
  const end = line + 1 < starts.length ? starts[line + 1] - 1 : text.length;
  return Math.min(starts[line] + Math.max(0, col), end);
}

// ---- highlighting

function wordSet(list) { return new Set(list || []); }

function grammarOf(t) {
  if (!t.grammarSets) {
    const g = t.grammar || {};
    t.grammarSets = {
      kw: wordSet(g.keywords), types: wordSet(g.types), openers: wordSet(g.openers),
      closers: wordSet(g.closers), reopen: wordSet(g.reopen), args: wordSet(g.args),
    };
  }
  return t.grammarSets;
}

const SECTIONS = new Set(['doc', 'needs', 'type', 'truck', 'syntax', 'expand', 'code', 'emit', 'data', 'support', 'resolve']);

// codeSpans splits a line of Packenger code into [text, class] spans.
function codeSpans(line, g, holes) {
  const out = [];
  let i = 0;
  const n = line.length;
  while (i < n) {
    const c = line[i];
    if (c === '/' && line[i + 1] === '/') { out.push([line.slice(i), 't-cmt']); break; }
    if (holes && c === '{') {
      const j = line.indexOf('}', i);
      const end = j < 0 ? n : j + 1;
      out.push([line.slice(i, end), 't-hole']); i = end; continue;
    }
    if (c === '"' || c === "'") {
      let j = i + 1;
      while (j < n && line[j] !== c) { if (line[j] === '\\') j++; j++; }
      out.push([line.slice(i, Math.min(j + 1, n)), 't-str']); i = Math.min(j + 1, n); continue;
    }
    if (/[0-9]/.test(c)) {
      let j = i; while (j < n && /[0-9_]/.test(line[j])) j++;
      out.push([line.slice(i, j), 't-num']); i = j; continue;
    }
    if (/[A-Za-z_]/.test(c)) {
      let j = i; while (j < n && /\w/.test(line[j])) j++;
      const w = line.slice(i, j);
      let k = j; while (k < n && line[k] === ' ') k++;
      let cls = '';
      if (g.kw.has(w)) cls = 't-kw';
      else if (g.types.has(w)) cls = 't-ty';
      else if (line[k] === '(') cls = 't-fn';
      out.push([w, cls]); i = j; continue;
    }
    if (c === ' ' || c === '\t') {
      let j = i; while (j < n && (line[j] === ' ' || line[j] === '\t')) j++;
      out.push([line.slice(i, j), '']); i = j; continue;
    }
    let j = i + 1;
    while (j < n && /[^\w\s"'{}]/.test(line[j]) && !(line[j] === '/' && line[j + 1] === '/')) j++;
    out.push([line.slice(i, j), 't-sym']); i = j;
  }
  return out;
}

function patternSpans(line) {
  const out = [];
  const re = /(\{[^}]*\}?)|(\]\*|\[|\]|->)|(\S+)|(\s+)/g;
  let m;
  while ((m = re.exec(line))) {
    if (m[1]) out.push([m[1], 't-slot']);
    else if (m[2]) out.push([m[2], 't-sym']);
    else if (m[3]) out.push([m[3], m[3].startsWith('//') ? 't-cmt' : 't-kw']);
    else out.push([m[4], '']);
    if (m[3] && m[3].startsWith('//')) { out[out.length - 1] = [line.slice(m.index), 't-cmt']; break; }
  }
  return out;
}

function emitSpans(line) {
  const out = [];
  const re = /(\{[^}]*\}?)|(@[\w/]+)|([;#].*$)|([^{@;#]+)/g;
  let m;
  while ((m = re.exec(line))) {
    if (m[1]) out.push([m[1], 't-hole']);
    else if (m[2]) out.push([m[2], 't-kw']);
    else if (m[3]) out.push([m[3], 't-cmt']);
    else out.push([m[4], '']);
  }
  return out;
}

function lineSpans(line, ctx, g) {
  if (!ctx.program) {
    if (line.length && line[0] !== ' ' && line[0] !== '\t') {
      const w = line.split(/\s+/)[0];
      if (w === 'end' && ctx.section === 'resolve') { ctx.section = ''; return [[line, 't-sec']]; }
      ctx.section = SECTIONS.has(w) ? w : ctx.section;
      ctx.fence = false;
      const rest = line.slice(w.length);
      const cls = { doc: 't-doc', needs: 't-fn', syntax: 't-kw', emit: 't-kw', type: 't-ty' }[w] || '';
      return [[w, SECTIONS.has(w) ? 't-sec' : ''], [rest, cls]];
    }
    switch (ctx.section) {
      case 'doc':
        // code in the doc's fenced blocks is highlighted as code
        if (/^\s*```/.test(line)) { ctx.fence = !ctx.fence; return [[line, 't-doc']]; }
        return ctx.fence ? codeSpans(line, g, false) : [[line, 't-doc']];
      case 'syntax': return patternSpans(line);
      case 'expand': return codeSpans(line, g, true);
      case 'code': return codeSpans(line, g, false);
      case 'emit': case 'support': case 'data': return emitSpans(line);
      case 'truck': return [[line, 't-fn']];
      case 'resolve': {
        return line.split(/('[^']*'?)/).map(p => p.startsWith("'") ? [p, 't-str'] : [p.replace(/\b(rename|prefix|name|keyword|symbol|in|to)\b/g, '\u0001$1\u0002'), 'resolve']);
      }
    }
    return [[line, '']];
  }
  if (/^\s*use\b/.test(line) && !ctx.body) {
    const m = line.match(/^(\s*)(use)(.*)$/);
    return [[m[1], ''], [m[2], 't-kw'], [m[3], 't-fn']];
  }
  if (line.trim() && !line.trim().startsWith('//')) ctx.body = true;
  return codeSpans(line, g, false);
}

function spansHTML(spans, errCol) {
  let html = '';
  let at = 0;
  let marked = errCol == null;
  for (const [text, cls] of spans) {
    let c = cls;
    if (!marked && errCol < at + text.length && text.trim()) { c = (c ? c + ' ' : '') + 't-err'; marked = true; }
    if (cls === 'resolve') html += esc(text).replace(/\u0001(\w+)\u0002/g, '<span class="t-kw">$1</span>');
    else html += c ? `<span class="${c}">${esc(text)}</span>` : esc(text);
    at += text.length;
  }
  return html;
}

function render() {
  const t = tab();
  if (!t) return;
  const text = ED.ta.value;
  const lines = text.split('\n');
  const g = grammarOf(t);
  const ctx = { program: t.kind === 'program', section: '', body: false };
  const errs = new Map();
  for (const d of t.diags || []) if (!d.file) errs.set(d.line - 1, Math.max(0, (d.col || 1) - 1));
  let html = '';
  for (let i = 0; i < lines.length; i++) {
    html += spansHTML(lineSpans(lines[i], ctx, g), errs.has(i) ? errs.get(i) : null) + '\n';
  }
  ED.pre.innerHTML = html + ' ';
  const width = String(lines.length).length;
  const gw = 34 + Math.max(2, width) * 8;
  ED.root.style.setProperty('--gutter-w', gw + 'px');
  if (ED.gutterLines !== lines.length || ED.gutterErrs !== [...errs.keys()].join()) {
    let gh = '';
    for (let i = 0; i < lines.length; i++) gh += `<div${errs.has(i) ? ' class="bad"' : ''}>${i + 1}</div>`;
    ED.gutter.innerHTML = gh;
    ED.gutterLines = lines.length;
    ED.gutterErrs = [...errs.keys()].join();
  }
  markCurrentLine();
}

let renderQueued = false;
function queueRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => { renderQueued = false; render(); });
}

function caretLine() {
  return posOf(ED.ta.value, ED.ta.selectionStart).line;
}

function markCurrentLine() {
  const { line, col } = posOf(ED.ta.value, ED.ta.selectionStart);
  ED.cur.style.top = (line * ED.lh) + 'px';
  const prev = ED.gutter.querySelector('.cur');
  if (prev) prev.classList.remove('cur');
  const g = ED.gutter.children[line];
  if (g) g.classList.add('cur');
  $('#statusPos').textContent = `${line + 1}:${col + 1}`;
}

function ensureVisible() {
  const { line, col } = posOf(ED.ta.value, ED.ta.selectionEnd);
  const top = 8 + line * ED.lh;
  const r = ED.root;
  if (top < r.scrollTop + ED.lh) r.scrollTop = Math.max(0, top - ED.lh * 2);
  else if (top + ED.lh * 2 > r.scrollTop + r.clientHeight) r.scrollTop = top + ED.lh * 3 - r.clientHeight;
  const gw = parseFloat(getComputedStyle(r).getPropertyValue('--gutter-w')) || 64;
  const x = gw + ED.padL + col * ED.cw;
  if (x > r.scrollLeft + r.clientWidth - 40) r.scrollLeft = x - r.clientWidth + 80;
  else if (x < r.scrollLeft + gw + 20) r.scrollLeft = Math.max(0, x - gw - 60);
}

function caretXY() {
  const { line, col } = posOf(ED.ta.value, ED.ta.selectionStart);
  const rect = ED.layer.getBoundingClientRect();
  return { x: rect.left + ED.padL + col * ED.cw, y: rect.top + line * ED.lh, line, col };
}

// insert replaces [from, to) with text, through the browser's editing so
// that undo works.
function insert(from, to, text) {
  const ta = ED.ta;
  ta.focus();
  ta.setSelectionRange(from, to);
  if (!document.execCommand('insertText', false, text)) {
    ta.setRangeText(text, from, to, 'end');
    onInput({ inputType: 'insertReplacementText' });
  }
}

// ---- tabs

function tabKind(text) {
  for (const l of text.split('\n')) {
    const t = l.trim();
    if (!t || t.startsWith('//')) continue;
    return /^use(\s|$)/.test(t) ? 'program' : 'package';
  }
  return 'program';
}

function headerSig(text) {
  return text.split('\n').filter(l => /^\s*(use|needs)\b/.test(l)).join('|');
}

async function loadGrammar(t) {
  if (t.ro && t.pkg) {
    // a package from the registry: its own needs
  }
  try {
    t.grammar = await api('/api/grammar', { ws: S.ws, path: t.ro ? '' : t.path, text: t.text });
  } catch (e) {
    t.grammar = null;
  }
  t.grammarSets = null;
  t.sig = headerSig(t.text);
  if (t === tab()) { queueRender(); renderOutline(); }
}

async function openFile(path, line) {
  let t = S.tabs.find(x => x.path === path);
  if (!t) {
    let data;
    try { data = await api('/api/fs/read', { ws: S.ws, path }); }
    catch (e) { status(e.message, 'bad'); return null; }
    t = { path, title: baseName(path), text: data.text, saved: data.text, kind: tabKind(data.text), diags: [], scroll: 0, sel: [0, 0] };
    S.tabs.push(t);
    loadGrammar(t);
    check(t);
  }
  activate(t);
  if (line) goToLine(line);
  saveSession();
  return t;
}

async function openPackage(name, line) {
  const key = 'pkg:' + name;
  let t = S.tabs.find(x => x.path === key);
  if (!t) {
    const from = tab() && !tab().ro ? tab().path : '';
    let data;
    try { data = await api('/api/package', { ws: S.ws, path: from, name }); }
    catch (e) { status(e.message, 'bad'); return; }
    if (data.local) return openFile(data.local, line);
    t = { path: key, title: name + '.pkg', text: data.text, saved: data.text, kind: tabKind(data.text), ro: true, pkg: name, origin: data.origin, diags: [], scroll: 0, sel: [0, 0] };
    S.tabs.push(t);
    loadGrammar(t);
  }
  activate(t);
  if (line) goToLine(line);
}

function stash() {
  const t = tab();
  if (!t) return;
  t.text = ED.ta.value;
  t.scroll = [ED.root.scrollTop, ED.root.scrollLeft];
  t.sel = [ED.ta.selectionStart, ED.ta.selectionEnd];
}

function activate(t) {
  if (S.active === t) { renderTabs(); return; }
  stash();
  hideComplete();
  hideHover();
  S.snippet = null;
  S.comp = null;
  S.active = t;
  renderSuggest();
  $('#welcome').hidden = !!t;
  ED.root.hidden = !t;
  if (!t) { renderTabs(); renderWelcome(); renderOutline(); return; }
  ED.ta.value = t.text;
  ED.ta.readOnly = !!t.ro;
  render();
  ED.ta.setSelectionRange(t.sel[0], t.sel[1]);
  ED.ta.focus({ preventScroll: true });
  const [st, sl] = Array.isArray(t.scroll) ? t.scroll : [0, 0];
  ED.root.scrollTop = st; ED.root.scrollLeft = sl;
  markCurrentLine();
  renderTabs();
  renderFiles();
  renderOutline();
  if ($('#sidePackages').hidden === false) renderPackages();
  $('#statusKind').textContent = t.ro ? `${t.kind} · read-only from ${t.origin || 'the registry'}` : t.kind;
  $('#statusHint').textContent = '';
  document.title = `${t.title} — pkide`;
}

function closeTab(t) {
  t = t || tab();
  if (!t) return;
  if (dirty(t) && !confirm(`${t.title} has unsaved changes. Close it and lose them?`)) return;
  const i = S.tabs.indexOf(t);
  S.tabs.splice(i, 1);
  if (S.active === t) {
    S.active = null;
    activate(S.tabs[Math.min(i, S.tabs.length - 1)] || null);
  } else renderTabs();
  saveSession();
}

function dirty(t) {
  const text = t === tab() ? ED.ta.value : t.text;
  return !t.ro && text !== t.saved;
}

function renderTabs() {
  const bar = $('#tabs');
  bar.replaceChildren(...S.tabs.map(t => h('button', {
    class: [t === tab() ? 'on' : '', dirty(t) ? 'dirty' : '', t.ro ? 'ro' : ''].join(' '),
    title: t.ro ? `${t.pkg} (read-only, ${t.origin})` : absPath(t.path),
    onclick: () => activate(t),
    onauxclick: e => { if (e.button === 1) closeTab(t); },
  }, t.title, h('span', { class: 'x', title: 'Close', onclick: e => { e.stopPropagation(); closeTab(t); } }, '×'))));
  const on = bar.querySelector('.on');
  if (on) on.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

async function save(t) {
  t = t || tab();
  if (!t || t.ro) return;
  if (t === tab()) t.text = ED.ta.value;
  try {
    await api('/api/fs/write', { ws: S.ws, path: t.path, text: t.text });
    t.saved = t.text;
    renderTabs();
    renderFiles();
    status(`saved ${t.title}`, 'good');
  } catch (e) { status(e.message, 'bad'); }
}

async function saveAll() {
  stash();
  for (const t of S.tabs) if (dirty(t)) await save(t);
}

function goToLine(line, col) {
  const off = offsetOf(ED.ta.value, line - 1, (col || 1) - 1);
  ED.ta.focus();
  ED.ta.setSelectionRange(off, off);
  markCurrentLine();
  const r = ED.root;
  r.scrollTop = Math.max(0, 8 + (line - 1) * ED.lh - r.clientHeight / 3);
}

// ---- input

let checkT, compT;
function onInput(e) {
  const t = tab();
  if (!t) return;
  t.text = ED.ta.value;
  queueRender();
  if (!t.wasDirty) { renderTabs(); t.wasDirty = true; }
  if (!dirty(t)) { t.wasDirty = false; renderTabs(); }
  clearTimeout(checkT);
  checkT = setTimeout(() => {
    check(t);
    if (headerSig(t.text) !== t.sig) {
      t.kind = tabKind(t.text);
      loadGrammar(t);
    }
  }, 650);
  ensureVisible();
  renderSuggest();
  const typed = e && e.inputType === 'insertText' ? e.data : null;
  if (typed == null) { if (e && /delete/.test(e.inputType || '') && !$('#complete').hidden) filterComplete(); return; }
  if (!$('#complete').hidden && /^[\w/-]$/.test(typed) && sameContext()) { filterComplete(); scheduleComplete(false, 140); return; }
  const prefix = currentPrefix();
  const open = /[\w]$/.test(prefix) || /\.$/.test(prefix) || /\{$/.test(prefix) || /^\s*(use|needs)\s+[\w/-]*$/.test(prefix) || /\{\w+:\s*$/.test(prefix);
  scheduleComplete(open, open ? 40 : 160, !open);
}

function currentPrefix() {
  const ta = ED.ta;
  const off = ta.selectionStart;
  return ta.value.slice(ta.value.lastIndexOf('\n', off - 1) + 1, off);
}

function trailingWord(prefix, extra) {
  const re = extra ? /[\w/-]*$/ : /\w*$/;
  return prefix.match(re)[0];
}

function sameContext() {
  const c = S.comp;
  if (!c) return false;
  const p = currentPrefix();
  return caretLine() === c.line && p.startsWith(c.before) && /^[\w/-]*$/.test(p.slice(c.before.length));
}

function scheduleComplete(open, delay, silent) {
  clearTimeout(compT);
  compT = setTimeout(() => requestComplete(open, silent), delay);
}

async function requestComplete(open, silent) {
  const t = tab();
  if (!t) return;
  const seq = ++S.compSeq;
  const line = caretLine();
  const prefix = currentPrefix();
  let res;
  try {
    res = await api('/api/complete', { ws: S.ws, path: t.ro ? '' : t.path, text: ED.ta.value, line, prefix });
  } catch (e) { if (!silent) status(e.message, 'bad'); return; }
  if (seq !== S.compSeq || t !== tab()) return;
  const word = res.word || '';
  S.comp = { items: res.items || [], line, before: prefix.slice(0, prefix.length - word.length), hint: res.hint || '' };
  $('#statusHint').textContent = (res.hint || '').split('\n')[0];
  $('#statusHint').title = res.hint || '';
  S.panelSel = null;
  if (open && sameContext()) filterComplete();
  else if (!open && !$('#complete').hidden) filterComplete();
  renderSuggest();
}

function score(label, q) {
  if (!q) return 3;
  const l = label.toLowerCase();
  const ql = q.toLowerCase();
  if (l.startsWith(ql)) return label.startsWith(q) ? 0 : 1;
  if (l.includes(ql)) return 2;
  let i = 0;
  for (const c of l) if (c === ql[i]) i++;
  return i === ql.length ? 4 : -1;
}

function highlightMatch(label, q) {
  if (!q) return esc(label);
  const l = label.toLowerCase(), ql = q.toLowerCase();
  const at = l.indexOf(ql);
  if (at >= 0) return esc(label.slice(0, at)) + '<b>' + esc(label.slice(at, at + q.length)) + '</b>' + esc(label.slice(at + q.length));
  let out = '', i = 0;
  for (const c of label) {
    if (i < ql.length && c.toLowerCase() === ql[i]) { out += '<b>' + esc(c) + '</b>'; i++; } else out += esc(c);
  }
  return out;
}

function filterComplete() {
  const c = S.comp;
  if (!c || !sameContext()) { hideComplete(); return; }
  const q = currentPrefix().slice(c.before.length);
  const items = [];
  for (const it of c.items) {
    const s = score(it.label, q);
    if (s >= 0) items.push([s, it]);
  }
  items.sort((a, b) => a[0] - b[0] || a[1].rank - b[1].rank || a[1].label.length - b[1].label.length || a[1].label.localeCompare(b[1].label));
  S.shown = items.slice(0, 200).map(x => x[1]);
  if (!S.shown.length || S.shown.length === 1 && S.shown[0].label === q && !S.shown[0].insert) { hideComplete(); return; }
  S.sel = 0;
  S.query = q;
  const list = $('#completeList');
  list.replaceChildren(...S.shown.map((it, i) => {
    const li = h('li', { role: 'option', onmousedown: e => { e.preventDefault(); accept(i); }, onmousemove: () => select(i) },
      h('span', { class: 'k ' + it.kind, title: it.kind }, kindLetter(it.kind)),
      h('span', { class: 'l', html: highlightMatch(it.label, q) }),
      h('span', { class: 'd' }, it.detail || ''));
    return li;
  }));
  $('#complete').hidden = false;
  select(0);
  positionComplete();
}

function kindLetter(k) {
  return { keyword: 'k', operator: 'o', var: 'v', param: 'p', func: 'f', global: 'g', const: 'c', type: 't', struct: 's', field: '.', package: 'P', section: '§', slot: '{', hole: '{', snippet: '…' }[k] || '·';
}

function select(i) {
  const list = $('#completeList');
  if (!S.shown.length) return;
  S.sel = (i + S.shown.length) % S.shown.length;
  [...list.children].forEach((li, k) => li.classList.toggle('on', k === S.sel));
  const li = list.children[S.sel];
  if (li) li.scrollIntoView({ block: 'nearest' });
  const it = S.shown[S.sel];
  renderSuggest();
  const doc = $('#completeDoc');
  doc.replaceChildren();
  if (it.detail || it.doc) {
    if (it.detail) doc.append(h('div', { class: 'sig' }, it.detail));
    if (it.doc) doc.append(h('div', { class: 'body' }, it.doc));
  }
}

function positionComplete() {
  const box = $('#complete');
  if (box.hidden || !tab()) return;
  const c = caretXY();
  const q = (S.query || '').length;
  let x = c.x - q * ED.cw - 22;
  let y = c.y + ED.lh + 2;
  const w = 380 + ($('#completeDoc').childNodes.length ? 340 : 0);
  const hgt = Math.min(S.shown.length, 11) * ED.lh + 4;
  if (x + w > innerWidth - 8) x = Math.max(8, innerWidth - 8 - w);
  if (y + hgt > innerHeight - 30) y = c.y - hgt - 2;
  box.style.left = x + 'px';
  box.style.top = y + 'px';
}

function hideComplete() {
  if ($('#complete').hidden) return;
  $('#complete').hidden = true;
  S.shown = [];
  renderSuggest();
}

// accept inserts a completion item in place of the word being typed.
function accept(i) {
  acceptItem(S.shown[i == null ? S.sel : i]);
}

function acceptItem(it) {
  hideComplete();
  if (!it || !S.comp || !sameContext()) return;
  const ta = ED.ta;
  const end = ta.selectionStart;
  const q = currentPrefix().slice(S.comp.before.length);
  const from = end - q.length;
  let text = it.insert || it.label;
  // the rest of the word after the cursor is replaced too
  const after = ta.value.slice(end).match(/^\w*/)[0];
  const phs = [];
  let out = '';
  let last = 0;
  const re = /\$\{([^}]*)\}/g;
  let m;
  while ((m = re.exec(text))) {
    out += text.slice(last, m.index);
    phs.push({ start: out.length, end: out.length + m[1].length });
    out += m[1];
    last = re.lastIndex;
  }
  out += text.slice(last);
  insert(from, end + after.length, out);
  if (phs.length) {
    const len = ta.value.length;
    S.snippet = { phs: phs.map(p => ({ start: from + p.start, end: from + p.end })), i: 0, len };
    const p = S.snippet.phs[0];
    ta.setSelectionRange(p.start, p.end);
    if (phs.length === 1 && it.kind === 'func') S.snippet.exit = from + out.length;
  } else {
    S.snippet = null;
  }
  markCurrentLine();
  scheduleComplete(false, 60, true);
}

function nextPlaceholder(back) {
  const sn = S.snippet;
  if (!sn) return false;
  const ta = ED.ta;
  const shift = ta.value.length - sn.len;
  for (let k = sn.i + 1; k < sn.phs.length; k++) { sn.phs[k].start += shift; sn.phs[k].end += shift; }
  if (sn.exit != null) sn.exit += shift;
  sn.len = ta.value.length;
  sn.i += back ? -1 : 1;
  if (sn.i < 0) sn.i = 0;
  if (sn.i >= sn.phs.length) {
    if (sn.exit != null) ta.setSelectionRange(sn.exit, sn.exit);
    S.snippet = null;
    return true;
  }
  const p = sn.phs[sn.i];
  ta.setSelectionRange(p.start, p.end);
  markCurrentLine();
  return true;
}

// ---- keys

function lineAt(text, off) {
  const start = text.lastIndexOf('\n', off - 1) + 1;
  let end = text.indexOf('\n', off);
  if (end < 0) end = text.length;
  return { start, end, text: text.slice(start, end) };
}

function indentOf(s) { return s.match(/^[ \t]*/)[0]; }

// sectionOf finds the package-file section of a line ('' for programs).
function sectionOf(text, line) {
  const t = tab();
  if (!t || t.kind === 'program') return 'code';
  const lines = text.split('\n');
  let sec = '';
  for (let i = 0; i <= line && i < lines.length; i++) {
    const l = lines[i];
    if (!l || l[0] === ' ' || l[0] === '\t') continue;
    const w = l.split(/\s+/)[0];
    sec = w === 'end' && sec === 'resolve' ? '' : w;
  }
  return sec;
}

function firstWords(s) {
  return s.split('//')[0].trim().split(/\s+/).filter(Boolean);
}

function isOpener(g, line) {
  const w = firstWords(line);
  return w.length > 0 && g.openers.has(w[0]) && w[1] !== '=';
}

function isCloserLine(g, line) {
  const w = firstWords(line);
  return w.length > 0 && g.closers.has(w[0]) && (w.length === 1 || g.args.has(w[0]));
}

// openerIndent finds the indentation of the line that opened the block a
// closer on line ends.
function openerIndent(g, lines, line) {
  let depth = 0;
  for (let i = line - 1; i >= 0; i--) {
    if (!lines[i].trim()) continue;
    const w = firstWords(lines[i]);
    if (!w.length) continue;
    if (isCloserLine(g, lines[i]) && !g.reopen.has(w[0])) depth++;
    else if (isOpener(g, lines[i]) || isCloserLine(g, lines[i]) && g.reopen.has(w[0])) {
      if (depth === 0) return indentOf(lines[i]);
      if (isOpener(g, lines[i])) depth--;
    }
  }
  return null;
}

// fixCloserIndent puts a closer (end, else) at the indentation of its block.
function fixCloserIndent(g) {
  const ta = ED.ta;
  const L = lineAt(ta.value, ta.selectionStart);
  if (!isCloserLine(g, L.text)) return;
  const lines = ta.value.split('\n');
  const n = posOf(ta.value, ta.selectionStart).line;
  const want = openerIndent(g, lines, n);
  const have = indentOf(L.text);
  if (want == null || want === have) return;
  const caret = ta.selectionStart - have.length + want.length;
  insert(L.start, L.start + have.length, want);
  ta.setSelectionRange(caret, caret);
}

function onEnter(e) {
  const t = tab();
  const ta = ED.ta;
  const g = grammarOf(t);
  const lineNo = caretLine();
  const sec = sectionOf(ta.value, lineNo);
  const code = sec === 'code' || sec === 'expand';
  if (code) fixCloserIndent(g);
  const L = lineAt(ta.value, ta.selectionStart);
  const before = ta.value.slice(L.start, ta.selectionStart);
  const after = ta.value.slice(ta.selectionStart, L.end);
  let ind = indentOf(L.text);
  if (!t.kind || t.kind === 'package') {
    if (L.text && L.text[0] !== ' ' && L.text[0] !== '\t' && SECTIONS.has(firstWords(L.text)[0]) && sec !== 'resolve' || sec === 'resolve' && /^resolve\b/.test(L.text)) ind = '  ';
  }
  let extra = '';
  let close = '';
  if (code && !after.trim()) {
    if (isOpener(g, before) || isCloserLine(g, before) && g.reopen.has(firstWords(before)[0])) {
      extra = '  ';
      if (isOpener(g, before) && g.closers.has('end')) {
        // close the block, unless the next line already does
        const lines = ta.value.split('\n');
        let k = lineNo + 1;
        while (k < lines.length && !lines[k].trim()) k++;
        const next = lines[k];
        const closed = next != null && (indentOf(next).length > ind.length || indentOf(next).length === ind.length && isCloserLine(g, next));
        if (!closed) close = '\n' + ind + 'end';
      }
    }
  }
  e.preventDefault();
  const pos = ta.selectionStart;
  insert(pos, ta.selectionEnd, '\n' + ind + extra + close);
  const caret = pos + 1 + ind.length + extra.length;
  ta.setSelectionRange(caret, caret);
  ensureVisible();
  markCurrentLine();
}

function indentLines(back) {
  const ta = ED.ta;
  const v = ta.value;
  const s = v.lastIndexOf('\n', ta.selectionStart - 1) + 1;
  let eEnd = ta.selectionEnd;
  if (eEnd > ta.selectionStart && v[eEnd - 1] === '\n') eEnd--;
  let end = v.indexOf('\n', eEnd);
  if (end < 0) end = v.length;
  const block = v.slice(s, end);
  const out = block.split('\n').map(l => back ? l.replace(/^( {1,2}|\t)/, '') : (l ? '  ' + l : l)).join('\n');
  insert(s, end, out);
  ta.setSelectionRange(s, s + out.length);
}

function toggleComment() {
  const ta = ED.ta;
  const v = ta.value;
  const s = v.lastIndexOf('\n', ta.selectionStart - 1) + 1;
  let end = v.indexOf('\n', Math.max(ta.selectionEnd - (ta.selectionEnd > ta.selectionStart && v[ta.selectionEnd - 1] === '\n' ? 1 : 0), s));
  if (end < 0) end = v.length;
  const lines = v.slice(s, end).split('\n');
  const all = lines.filter(l => l.trim()).every(l => l.trim().startsWith('//'));
  const ind = Math.min(...lines.filter(l => l.trim()).map(l => indentOf(l).length));
  const out = lines.map(l => {
    if (!l.trim()) return l;
    return all ? l.replace(/^(\s*)\/\/ ?/, '$1') : l.slice(0, ind) + '// ' + l.slice(ind);
  }).join('\n');
  insert(s, end, out);
  ta.setSelectionRange(s, s + out.length);
}

function onKey(e) {
  const ta = ED.ta;
  hideHover();
  const popup = !$('#complete').hidden;
  const mod = e.ctrlKey || e.metaKey;
  if (popup) {
    if (e.key === 'ArrowDown') { e.preventDefault(); select(S.sel + 1); return; }
    if (e.key === 'ArrowUp') { e.preventDefault(); select(S.sel - 1); return; }
    if (e.key === 'PageDown') { e.preventDefault(); select(Math.min(S.sel + 10, S.shown.length - 1)); return; }
    if (e.key === 'PageUp') { e.preventDefault(); select(Math.max(S.sel - 10, 0)); return; }
    if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); accept(); return; }
    if (e.key === 'Escape') { e.preventDefault(); hideComplete(); return; }
  }
  if (e.key === 'Escape') { hideHover(); S.snippet = null; return; }
  if (mod && e.key === ' ') { e.preventDefault(); requestComplete(true); return; }
  if (tab() && tab().ro && !mod && (e.key.length === 1 || e.key === 'Enter' || e.key === 'Backspace' || e.key === 'Delete' || e.key === 'Tab')) {
    if (e.key !== 'Tab') status('this package is read-only: it comes from the registry', 'bad');
    if (e.key === 'Tab') e.preventDefault();
    return;
  }
  if (e.key === 'Tab') {
    e.preventDefault();
    if (!e.shiftKey && S.snippet && nextPlaceholder(false)) return;
    if (e.shiftKey && S.snippet && nextPlaceholder(true)) return;
    if (ta.selectionStart !== ta.selectionEnd || e.shiftKey) indentLines(e.shiftKey);
    else insert(ta.selectionStart, ta.selectionEnd, '  ');
    return;
  }
  if (e.key === 'Enter' && !mod && !e.shiftKey && !e.altKey) { S.snippet = null; onEnter(e); return; }
  if (e.key === ' ' && !mod && tab()) {
    const sec = sectionOf(ta.value, caretLine());
    if (sec === 'code' || sec === 'expand') setTimeout(() => {
      const L = lineAt(ta.value, ta.selectionStart);
      const w = firstWords(L.text);
      if (w.length === 1 && grammarOf(tab()).args.has(w[0])) fixCloserIndent(grammarOf(tab()));
    });
  }
  if (e.key === 'Backspace' && ta.selectionStart === ta.selectionEnd && !mod) {
    const L = lineAt(ta.value, ta.selectionStart);
    const before = ta.value.slice(L.start, ta.selectionStart);
    if (before.length >= 2 && /^ +$/.test(before)) {
      e.preventDefault();
      const n = before.length % 2 === 0 ? 2 : 1;
      insert(ta.selectionStart - n, ta.selectionStart, '');
      return;
    }
  }
  if (mod && e.key === '/') { e.preventDefault(); toggleComment(); return; }
  if (e.key === 'F12') { e.preventDefault(); goToDefinition(); return; }
  if ((e.key === 'Home') && !e.shiftKey && !mod) {
    const L = lineAt(ta.value, ta.selectionStart);
    const first = L.start + indentOf(L.text).length;
    e.preventDefault();
    const to = ta.selectionStart === first ? L.start : first;
    ta.setSelectionRange(to, to);
    markCurrentLine();
  }
}

function caretMoved() {
  markCurrentLine();
  hideHover();
  if (!$('#complete').hidden && !sameContext()) hideComplete();
  if (S.snippet) {
    const p = S.snippet.phs[S.snippet.i];
    const shift = ED.ta.value.length - S.snippet.len;
    if (!p || ED.ta.selectionStart < p.start || ED.ta.selectionStart > p.end + shift) S.snippet = null;
  }
  scheduleComplete(false, 250, true);
}

// ---- hover and definition

function hideHover() { $('#hover').hidden = true; clearTimeout(ED.hoverT); }

function mouseOffset(e) {
  const rect = ED.layer.getBoundingClientRect();
  const line = Math.floor((e.clientY - rect.top) / ED.lh);
  const col = Math.floor((e.clientX - rect.left - ED.padL) / ED.cw + 0.2);
  return { line, col };
}

function byteCol(line, col) { return new TextEncoder().encode(line.slice(0, col)).length; }

function onHoverMove(e) {
  clearTimeout(ED.hoverT);
  if (!$('#hover').hidden) {
    const r = $('#hover').getBoundingClientRect();
    if (e.clientY < r.top - 30 || e.clientY > r.bottom + 30 || e.clientX < r.left - 30 || e.clientX > r.right + 30) hideHover();
  }
  if (!$('#complete').hidden) return;
  const { line, col } = mouseOffset(e);
  const x = e.clientX, y = e.clientY;
  ED.hoverT = setTimeout(() => showHover(line, col, x, y), 500);
}

async function showHover(line, col, x, y) {
  const t = tab();
  const lines = ED.ta.value.split('\n');
  if (!t || line < 0 || line >= lines.length || col < 0 || col >= lines[line].length || !/\w/.test(lines[line][col] || '')) return;
  let info;
  try { info = await api('/api/hover', { ws: S.ws, path: t.ro ? '' : t.path, text: ED.ta.value, line, col: byteCol(lines[line], col) }); }
  catch { return; }
  if (!info || !info.title || t !== tab()) return;
  const box = $('#hover');
  box.replaceChildren(h('div', { class: 'sig' }, info.title));
  if (info.doc) box.append(h('div', { class: 'body' }, info.doc));
  if (info.def) box.append(h('div', { class: 'go' }, 'Ctrl+click or F12 to go to its definition'));
  box.hidden = false;
  const r = box.getBoundingClientRect();
  box.style.left = Math.min(x, innerWidth - r.width - 8) + 'px';
  box.style.top = (y + 18 + r.height > innerHeight - 30 ? y - r.height - 10 : y + 18) + 'px';
}

async function goToDefinition() {
  const t = tab();
  if (!t) return;
  const { line, col } = posOf(ED.ta.value, ED.ta.selectionStart);
  const text = ED.ta.value.split('\n')[line];
  let info;
  try { info = await api('/api/hover', { ws: S.ws, path: t.ro ? '' : t.path, text: ED.ta.value, line, col: byteCol(text, col) }); }
  catch (e) { status(e.message, 'bad'); return; }
  if (!info || !info.def) { status('no definition found here'); return; }
  const d = info.def;
  if (d.package) openPackage(d.package, d.line);
  else if (d.file) {
    const p = d.file.startsWith(S.ws + '/') ? d.file.slice(S.ws.length + 1) : d.file;
    if (t.path === p) goToLine(d.line); else openFile(p, d.line);
  }
}

// ---- diagnostics

async function check(t) {
  if (!t || t.ro) return;
  let diags;
  try { diags = await api('/api/check', { ws: S.ws, path: t.path, text: t === tab() ? ED.ta.value : t.text }); }
  catch (e) { diags = [{ line: 1, col: 0, msg: e.message }]; }
  t.diags = diags;
  if (t === tab()) { queueRender(); renderOutline(); }
  renderProblems();
}

function renderProblems() {
  const all = [];
  for (const t of S.tabs) for (const d of t.diags || []) all.push([t, d]);
  $('#problemCount').textContent = all.length || '';
  const pane = $('#problemsPane');
  if (!all.length) { pane.replaceChildren(h('div', { class: 'empty-note' }, 'No problems in the open files.')); return; }
  pane.replaceChildren(...all.map(([t, d]) => h('button', {
    class: 'problem',
    onclick: () => { activate(t); goToLine(d.line, d.col); },
  }, h('span', { class: 'dot' }), h('span', { class: 'where' }, `${t.title}:${d.line}${d.col ? ':' + d.col : ''}`), h('span', {}, d.file ? `${d.msg}` : d.msg))));
}

// ------------------------------------------------------------------ side panels

async function listDir(dir, force) {
  if (!force && S.dirs.has(dir)) return S.dirs.get(dir);
  try {
    const entries = await api('/api/fs/list', { ws: S.ws, path: dir });
    S.dirs.set(dir, entries);
    return entries;
  } catch (e) { status(e.message, 'bad'); return []; }
}

async function renderFiles() {
  const host = $('#sideFiles');
  if (!S.ws) {
    host.replaceChildren(h('div', { class: 'side-empty' }, 'No folder is open. ', h('button', { onclick: openFolderDialog }, 'Open a folder'), ' to see its files.'));
    return;
  }
  const rows = [];
  const walk = async (dir, depth) => {
    for (const e of await listDir(dir)) {
      const p = dir ? dir + '/' + e.name : e.name;
      if (e.dir) {
        const open = S.open.has(p);
        rows.push(h('button', {
          class: 'tree-row dir', style: `--depth:${depth}`, title: p,
          onclick: () => { open ? S.open.delete(p) : S.open.add(p); saveSession(); renderFiles(); },
          oncontextmenu: ev => fileMenu(ev, p, true),
        }, h('span', { class: 'mark' }, open ? '▾' : '▸'), e.name + '/'));
        if (open) await walk(p, depth + 1);
      } else {
        const t = S.tabs.find(x => x.path === p);
        rows.push(h('button', {
          class: 'tree-row' + (tab() && tab().path === p ? ' on' : ''), style: `--depth:${depth}`, title: p,
          onclick: () => openFile(p),
          oncontextmenu: ev => fileMenu(ev, p, false),
        }, h('span', { class: 'mark' }, ''), h('span', { class: (e.name.endsWith('.pkg') ? 'pkg' : '') + (t && dirty(t) ? ' dirty' : '') }, e.name)));
      }
    }
  };
  await walk('', 0);
  host.replaceChildren(...rows);
}

function fileMenu(ev, path, isDir) {
  ev.preventDefault();
  modal(isDir ? path + '/' : path, body => {
    const dir = isDir ? path : dirName(path);
    body.append(
      h('div', { class: 'row' },
        h('button', { class: 'btn quiet', onclick: () => { closeModal(); newFile(dir); } }, 'New file here'),
        h('button', { class: 'btn quiet', onclick: () => { closeModal(); renamePath(path); } }, 'Rename'),
        h('button', { class: 'btn quiet', onclick: () => { closeModal(); deletePath(path, isDir); } }, 'Delete')));
  });
}

async function newFile(dir, folder) {
  const name = await ask(folder ? 'New folder' : 'New file', dir ? dir + '/' : '', folder ? 'path of the folder' : 'path of the file, e.g. examples/hello.pkg');
  if (!name) return;
  const program = 'use std\n\nprint "hello"\n';
  try {
    await api('/api/fs/create', { ws: S.ws, path: name, dir: !!folder, text: folder ? '' : (name.endsWith('.pkg') ? program : '') });
    S.dirs.clear();
    let d = dirName(name);
    while (d) { S.open.add(d); d = dirName(d); }
    await renderFiles();
    if (!folder) openFile(name);
  } catch (e) { status(e.message, 'bad'); }
}

async function renamePath(path) {
  const to = await ask('Rename', path, 'the new path');
  if (!to || to === path) return;
  try {
    await api('/api/fs/rename', { ws: S.ws, path, to });
    for (const t of S.tabs) if (t.path === path || t.path.startsWith(path + '/')) { t.path = to + t.path.slice(path.length); t.title = baseName(t.path); }
    S.dirs.clear();
    renderTabs(); renderFiles(); saveSession();
  } catch (e) { status(e.message, 'bad'); }
}

async function deletePath(path, isDir) {
  if (!confirm(`Delete ${path}${isDir ? ' and everything in it' : ''}? This can't be undone.`)) return;
  try {
    await api('/api/fs/delete', { ws: S.ws, path });
    for (const t of [...S.tabs]) if (t.path === path || t.path.startsWith(path + '/')) { t.saved = t.text = ED.ta.value; closeTab(t); }
    S.dirs.clear();
    renderFiles();
    status('deleted ' + path, 'good');
  } catch (e) { status(e.message, 'bad'); }
}

async function renderOutline() {
  const host = $('#sideOutline');
  if (host.hidden) return;
  const t = tab();
  if (!t) { host.replaceChildren(h('div', { class: 'side-empty' }, 'Open a file to see what it defines.')); return; }
  let syms = [];
  try { syms = await api('/api/outline', { ws: S.ws, path: t.ro ? '' : t.path, text: ED.ta.value }); } catch { /* keep empty */ }
  if (t !== tab()) return;
  if (!syms.length) { host.replaceChildren(h('div', { class: 'side-empty' }, 'Nothing defined yet (or the file doesn’t parse).')); return; }
  host.replaceChildren(...syms.map(s => h('button', { class: 'tree-row', title: s.detail || '', onclick: () => goToLine(s.line) },
    h('span', { class: 'out-kind' }, s.kind), s.name, s.detail ? h('small', {}, s.detail) : null)));
}

async function renderPackages() {
  const host = $('#sidePackages');
  const t = tab();
  if (!t) { host.replaceChildren(h('div', { class: 'side-empty' }, 'Open a file to see the packages it loads.')); return; }
  let pkgs = [];
  try { pkgs = await api('/api/loaded', { ws: S.ws, path: t.ro ? '' : t.path, text: ED.ta.value }); } catch (e) { host.replaceChildren(h('div', { class: 'side-empty' }, e.message)); return; }
  host.replaceChildren(
    h('div', { class: 'side-empty' }, `${pkgs.length} packages define this file’s language. Open one to read it.`),
    ...pkgs.map(p => h('button', { class: 'tree-row', title: p.doc, onclick: () => openPackage(p.name) },
      h('span', { class: 'mark' }, p.truck ? '▪' : ''), h('span', { class: 'pkg' }, p.name), h('small', {}, p.doc))));
}

function showSide(which) {
  for (const b of $('#sideTabs').children) b.classList.toggle('on', b.dataset.side === which);
  $('#sideFiles').hidden = which !== 'files';
  $('#sideOutline').hidden = which !== 'outline';
  $('#sidePackages').hidden = which !== 'packages';
  $('#fileCmds').style.visibility = which === 'files' ? '' : 'hidden';
  if (which === 'outline') renderOutline();
  if (which === 'packages') renderPackages();
}

// ------------------------------------------------------------------ console

function showBottom(which) {
  for (const b of $('#bottomTabs').children) b.classList.toggle('on', b.dataset.bottom === which);
  $('#consolePane').hidden = which !== 'console';
  $('#problemsPane').hidden = which !== 'problems';
  $('#consoleCmds').style.visibility = which === 'console' ? '' : 'hidden';
  if (which === 'problems') renderProblems();
}

const consoleState = { partial: '' };

function linkify(line) {
  const frag = document.createDocumentFragment();
  const re = /((?:\/|[\w.-]+\/)?[\w./-]*\.pkg):(\d+)(?::(\d+))?/g;
  let last = 0, m;
  while ((m = re.exec(line))) {
    frag.append(line.slice(last, m.index));
    const [, file, ln, col] = m;
    frag.append(h('a', { onclick: () => openLocation(file, +ln, +(col || 1)), title: 'Open' }, m[0]));
    last = re.lastIndex;
  }
  frag.append(line.slice(last));
  return frag;
}

async function openLocation(file, line, col) {
  const candidates = [];
  if (file.startsWith('/')) {
    if (file.startsWith(S.ws + '/')) candidates.push(file.slice(S.ws.length + 1));
  } else {
    if (S.procDir) candidates.push((S.procDir ? S.procDir + '/' : '') + file);
    candidates.push(file);
    if (tab() && !tab().ro) candidates.push((dirName(tab().path) ? dirName(tab().path) + '/' : '') + file);
  }
  for (const c of candidates) {
    const norm = c.replace(/^\.\//, '');
    if (S.tabs.find(t => t.path === norm)) { await openFile(norm, line); goToLine(line, col); return; }
    try { await api('/api/fs/read', { ws: S.ws, path: norm }); await openFile(norm, line); goToLine(line, col); return; } catch { /* next */ }
  }
  if (file.includes('/') && !file.startsWith('/')) openPackage(file.replace(/\.pkg$/, ''), line);
  else status(`can't find ${file} in the workspace`, 'bad');
}

function consoleWrite(text, cls) {
  const out = $('#console');
  const atBottom = out.scrollTop + out.clientHeight >= out.scrollHeight - 4;
  if (cls) {
    out.append(h('span', { class: cls }, text));
  } else {
    const all = consoleState.partial + text;
    const parts = all.split('\n');
    consoleState.partial = parts.pop();
    const tail = out.querySelector('.partial');
    if (tail) tail.remove();
    for (const p of parts) {
      const el = h('span', p.startsWith('$ ') ? { class: 'cmd' } : {});
      el.append(linkify(p), '\n');
      out.append(el);
    }
    if (consoleState.partial) out.append(h('span', { class: 'partial' }, consoleState.partial));
  }
  while (out.childNodes.length > 6000) out.firstChild.remove();
  if (atBottom) out.scrollTop = out.scrollHeight;
}

function follow(id, dir, onExit) {
  if (S.proc && S.proc.es) S.proc.es.close();
  showBottom('console');
  const es = new EventSource(`/api/events/${id}?token=${TOKEN}`);
  S.proc = { id, es, running: true };
  S.procDir = dir || '';
  consoleState.partial = '';
  $('#stdin').disabled = false;
  const t0 = performance.now();
  es.addEventListener('out', ev => consoleWrite(JSON.parse(ev.data)));
  es.addEventListener('exit', ev => {
    const code = +ev.data;
    if (consoleState.partial) { consoleWrite('\n'); }
    const secs = ((performance.now() - t0) / 1000).toFixed(2);
    consoleWrite(`exit ${code} · ${secs} s\n`, 'exit ' + (code === 0 ? 'good' : 'bad'));
    es.close();
    S.proc.running = false;
    $('#stdin').disabled = true;
    if (onExit) onExit(code);
  });
  es.onerror = () => { if (S.proc && S.proc.running) { es.close(); S.proc.running = false; $('#stdin').disabled = true; } };
}

async function runAction(action, arg) {
  const t = tab();
  if (!t || t.ro) { status('open a file of the workspace first', 'bad'); return; }
  await saveAll();
  try {
    const r = await api('/api/run', { ws: S.ws, path: t.path, action, arg: arg || '' });
    if ($('#console').childNodes.length) consoleWrite('\n');
    follow(r.id, action === 'test' ? '' : dirName(t.path), code => {
      if (action === 'build' && code === 0) { S.dirs.clear(); renderFiles(); }
    });
  } catch (e) { status(e.message, 'bad'); }
}

function testTarget(t) {
  const p = t.path;
  const m = p.match(/^(.*?\btests)(\/|$)/);
  if (m) return m[1];
  const top = S.dirs.get('') || [];
  if (top.some(e => e.dir && e.name === 'tests')) return 'tests';
  return p;
}

async function formatTab() {
  const t = tab();
  if (!t || t.ro) return;
  status('formatting…');
  try {
    const r = await api('/api/fmt', { ws: S.ws, path: t.path, text: ED.ta.value });
    if (r.text === ED.ta.value) { status('already formatted', 'good'); return; }
    const sel = ED.ta.selectionStart;
    const top = ED.root.scrollTop;
    insert(0, ED.ta.value.length, r.text);
    const off = Math.min(sel, r.text.length);
    ED.ta.setSelectionRange(off, off);
    ED.root.scrollTop = top;
    status('formatted with pkfmt', 'good');
  } catch (e) {
    status('pkfmt failed: see the console', 'bad');
    consoleWrite(e.message + '\n', 'exit bad');
    showBottom('console');
  }
}

async function whyDialog() {
  const t = tab();
  if (!t || t.ro) { status('open a program first', 'bad'); return; }
  let pkgs = [];
  try { pkgs = await api('/api/loaded', { ws: S.ws, path: t.path, text: ED.ta.value }); } catch { /* none */ }
  pick('Why is this package in the build?', pkgs.filter(p => !p.truck).map(p => ({ label: p.name, detail: p.doc })), it => runAction('why', it.label), 'a package name, e.g. std/add or bintables/add', true);
}

// ------------------------------------------------------------------ build settings (right sidebar)

function buildOf(id) { return ((S.state && S.state.builds) || []).find(b => b.id === id); }

const TARGETS = [
  ['bintables', 'NASM → a native x86-64 executable'],
  ['ctables', 'C, built with cc -O2: faster code'],
  ['webtables', 'WebAssembly, run with wasmtime'],
];

// renderBuildSelect updates the profile chip and the commands it enables.
function renderBuildSelect() {
  const chip = $('#profileChip');
  const b = S.profile && buildOf(S.profile.build);
  chip.querySelector('.dot').className = 'dot' + (!b ? ' none' : b.kind === 'wasm' ? ' wasm' : '');
  chip.querySelector('.name').textContent = b ? b.name : (S.ws ? 'choose a build' : 'no folder open');
  chip.querySelector('.tgt').textContent = b && S.profile ? S.profile.target || 'bintables' : '';
  for (const btn of document.querySelectorAll('#topCmds button')) {
    const runs = ['run', 'build', 'test'].includes(btn.dataset.cmd);
    btn.disabled = !S.ws || !b || (runs && b.kind === 'wasm');
  }
  if (!$('#rightBuild').hidden) renderBuilds();
}

async function setProfile(change) {
  if (!S.ws) { status('open a folder first', 'bad'); return; }
  S.profile = Object.assign({}, S.profile, change);
  try { S.profile = await api('/api/profile', { ws: S.ws, profile: S.profile }); } catch (e) { status(e.message, 'bad'); }
  renderBuildSelect();
  if (change.registry !== undefined) { await api('/api/reload'); for (const t of S.tabs) { loadGrammar(t); check(t); } }
}

function buildCard(b, prof, st) {
  const on = b.id === prof.build;
  const ver = b.version ? `${b.version.slice(0, 7)}${b.subject ? ' · ' + b.subject : ''}` : '';
  return h('div', {
    class: 'card' + (on ? ' on' : ''), role: 'radio', 'aria-checked': on ? 'true' : 'false', tabindex: '0', title: b.path,
    onclick: e => { if (!e.target.closest('.acts')) setProfile({ build: b.id }); },
    onkeydown: e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setProfile({ build: b.id }); } },
  },
    h('span', { class: 'radio' }),
    h('span', { class: 'title' }, b.name),
    h('span', { class: 'badge ' + b.kind }, b.kind),
    ver ? h('span', { class: 'meta ver' }, ver) : null,
    h('span', { class: 'meta' }, b.path.replace(st.home || '\u0000', '~')),
    b.kind === 'wasm' ? h('span', { class: 'meta' }, 'checks and prints asm only (runs in wasmtime)') : null,
    h('span', { class: 'acts' },
      h('span', { onclick: async () => { const n = await ask('Rename build', b.name, 'a name for this build'); if (n) { S.state = await api('/api/builds/rename', { id: b.id, name: n }); renderBuilds(); renderBuildSelect(); } } }, 'Rename'),
      b.auto ? null : h('span', { class: 'danger', onclick: async () => {
        if (!confirm(`Remove ${b.name}?${b.source ? ' Its files in ' + st.dataDir + ' are deleted.' : ''}`)) return;
        try { S.state = await api('/api/builds/remove', { id: b.id }); await refreshProfile(); renderBuilds(); } catch (e) { status(e.message, 'bad'); }
      } }, 'Remove')));
}

function option(on, title, note, onclick, disabled) {
  return h('button', { class: 'opt' + (on ? ' on' : ''), role: 'radio', 'aria-checked': on ? 'true' : 'false', disabled, onclick },
    h('span', { class: 'radio' }), h('b', {}, title), note ? h('small', {}, note) : null);
}

function renderBuilds() {
  const pane = $('#rightBuild');
  const st = S.state || {};
  const builds = st.builds || [];
  const prof = S.profile || {};
  const groups = [
    ['Found in the repository and on PATH', builds.filter(b => b.auto)],
    ['Built from git', builds.filter(b => !b.auto && b.version)],
    ['Added', builds.filter(b => !b.auto && !b.version)],
  ].filter(g => g[1].length);
  const custom = prof.registry && !['', 'repo', 'build'].includes(prof.registry);
  const cur = buildOf(prof.build) || {};

  const path = h('input', { type: 'text', placeholder: '/path/to/packenger, pkc or pkc.wasm', 'aria-label': 'Path of a compiler' });
  const rev = h('input', { type: 'text', placeholder: 'main, a tag or a commit', list: 'gitRefs', 'aria-label': 'Git revision' });
  const refs = h('datalist', { id: 'gitRefs' });
  const withPkc = h('input', { type: 'checkbox', checked: true });
  api('/api/git/refs').then(rs => refs.replaceChildren(...rs.map(r => h('option', { value: r.split(' ')[0] }, r)))).catch(() => {});

  pane.replaceChildren(h('div', { class: 'bs' },
    h('section', {},
      h('h3', {}, 'Compiler'),
      h('p', { class: 'note' }, S.ws ? `The build that runs, builds and tests ${baseName(S.ws)}.` : 'Open a folder to choose its build.'),
      builds.length ? groups.flatMap(([name, list]) => [h('div', { class: 'group-name' }, name), ...list.map(b => buildCard(b, prof, st))])
        : h('p', { class: 'note' }, 'No builds yet: add a compiler or build one from git below.')),
    h('section', {},
      h('h3', {}, 'Target'),
      h('p', { class: 'note' }, 'The backend truck that translates the program.'),
      h('div', { class: 'seg', role: 'radiogroup' }, ...TARGETS.map(([t, note]) =>
        h('button', { class: (prof.target || 'bintables') === t ? 'on' : '', role: 'radio', 'aria-checked': (prof.target || 'bintables') === t ? 'true' : 'false', onclick: () => setProfile({ target: t }) }, h('b', {}, t), h('small', {}, note))))),
    h('section', {},
      h('h3', {}, 'Packages from'),
      h('p', { class: 'note' }, 'Where builds, completion and checking download packages.'),
      option(!prof.registry, 'Default', `the program’s lock file or pkg_tree.pkg, else ${st.defaultRegistry || 'the universal tree'}`, () => setProfile({ registry: '' })),
      option(prof.registry === 'repo', 'The repository', st.repo ? `${st.repo.replace(st.home || '\u0000', '~')}/registry` : 'set the repository in the settings', () => setProfile({ registry: 'repo' }), !st.repo),
      option(prof.registry === 'build', 'The build’s own', cur.source ? 'the registry of the commit the build came from' : 'only for builds from git', () => setProfile({ registry: 'build' }), !cur.source),
      option(custom, 'A URL', custom ? prof.registry : 'an http(s):// or file:// registry, e.g. your own server', async () => {
        const u = await ask('Registry URL', custom ? prof.registry : 'http://localhost:6464', 'an http(s):// or file:// registry');
        if (u) setProfile({ registry: u });
      })),
    h('section', {},
      h('h3', {}, 'Build from git'),
      h('p', { class: 'note' }, st.repo ? `Packenger at any commit of the repository, kept side by side in ${st.dataDir.replace(st.home || '\u0000', '~')}/builds.` : 'Set the Packenger repository in the settings to build versions from git.'),
      h('form', { onsubmit: async e => {
        e.preventDefault();
        try {
          const r = await api('/api/builds/git', { rev: rev.value, pkc: withPkc.checked });
          if ($('#console').childNodes.length) consoleWrite('\n');
          follow(r.id, '', async () => { S.state = await api('/api/state'); await refreshProfile(); renderBuilds(); });
        } catch (err) { status(err.message, 'bad'); }
      } },
        h('div', { class: 'field' }, rev, refs),
        h('div', { class: 'field', style: 'margin-top:8px' }, h('label', { class: 'check' }, withPkc, 'build pkc too'), h('span', { class: 'fill' }), h('button', { class: 'btn', disabled: !st.repo }, 'Build')))),
    h('section', {},
      h('h3', {}, 'Add a compiler'),
      h('p', { class: 'note' }, 'Any Packenger compiler on disk: the Go compiler, pkc, pkc-c or pkc.wasm.'),
      h('form', { onsubmit: async e => { e.preventDefault(); try { S.state = await api('/api/builds/add', { path: path.value }); path.value = ''; await refreshProfile(); renderBuilds(); } catch (err) { status(err.message, 'bad'); } } },
        h('div', { class: 'field' }, path, h('button', { class: 'btn' }, 'Add'))),
      h('div', { class: 'field', style: 'margin-top:10px' },
        h('button', { class: 'btn quiet', onclick: async () => { S.state = await api('/api/builds/detect'); await refreshProfile(); renderBuilds(); status('builds refreshed', 'good'); } }, 'Look for builds again'),
        h('button', { class: 'btn quiet', onclick: settingsDialog }, 'Settings')))));
}

async function refreshProfile() {
  if (!S.ws) { renderBuildSelect(); return; }
  try {
    const r = await api('/api/workspace', { ws: S.ws });
    S.profile = r.profile;
    S.state = r.state;
  } catch { /* keep */ }
  renderBuildSelect();
}

// ------------------------------------------------------------------ right sidebar

function showRight(which) {
  const app = $('#app');
  app.classList.toggle('no-right', !which);
  for (const b of document.querySelectorAll('[data-right]')) b.classList.toggle('on', !!which && b.dataset.right === which);
  $('#profileChip').classList.toggle('on', which === 'build');
  $('#rightSuggest').hidden = which !== 'suggest';
  $('#rightBuild').hidden = which !== 'build';
  $('#complete').classList.toggle('docked', which === 'suggest');
  store.set('right', which || '');
  if (which === 'build') renderBuilds();
  if (which === 'suggest') renderSuggest();
  positionComplete();
}

function toggleRight(which) {
  const open = !$('#app').classList.contains('no-right') && !$(which === 'build' ? '#rightBuild' : '#rightSuggest').hidden;
  showRight(open ? '' : which);
}

// ---- the suggestions panel: every suggestion, in full

const GROUPS = [
  ['next', 'Next in this statement'],
  ['scope', 'In scope'],
  ['fields', 'Fields'],
  ['file', 'This file'],
  ['forms', 'Statements and expressions'],
  ['lib', 'Libraries'],
  ['types', 'Types'],
  ['packages', 'Packages'],
  ['pkgfile', 'Package file'],
];

function groupOf(it) {
  if (it.rank === 15 || it.kind === 'operator') return 'next';
  switch (it.kind) {
    case 'var': case 'param': return 'scope';
    case 'field': return 'fields';
    case 'type': case 'struct': return 'types';
    case 'package': return 'packages';
    case 'section': case 'slot': case 'hole': case 'snippet': return 'pkgfile';
    case 'func': case 'global': case 'const': return it.source === 'this file' ? 'file' : 'lib';
  }
  return 'forms';
}

// panelItems are the suggestions for the word at the cursor, best first.
function panelItems() {
  const c = S.comp;
  if (!c || !tab() || !sameContext()) return [];
  const q = currentPrefix().slice(c.before.length);
  const out = [];
  for (const it of c.items) {
    const s = score(it.label, q);
    if (s >= 0) out.push([s, it]);
  }
  out.sort((a, b) => a[0] - b[0] || a[1].rank - b[1].rank || a[1].label.length - b[1].label.length || a[1].label.localeCompare(b[1].label));
  return out.map(x => x[1]);
}

function insertPreview(text) {
  const frag = document.createDocumentFragment();
  let last = 0;
  const re = /\$\{([^}]*)\}/g;
  let m;
  while ((m = re.exec(text))) {
    frag.append(text.slice(last, m.index), h('span', { class: 'ph' }, m[1]));
    last = re.lastIndex;
  }
  frag.append(text.slice(last));
  return frag;
}

function hintNode(hint) {
  const pre = h('pre', { class: 'sg-hint' });
  for (const [i, line] of hint.split('\n').entries()) {
    if (i) pre.append('\n');
    const [pat, pkg] = line.split('   — ');
    const parts = pat.split(/(‹[^›]*›)/);
    for (const p of parts) pre.append(p.startsWith('‹') ? h('span', { class: 'arg' }, p.slice(1, -1)) : p);
    if (pkg) pre.append(h('span', { class: 'pkg' }, '  ' + pkg));
  }
  return pre;
}

let suggestQueued = false;
function renderSuggest() {
  if (suggestQueued) return;
  suggestQueued = true;
  requestAnimationFrame(() => { suggestQueued = false; drawSuggest(); });
}

function drawSuggest() {
  const pane = $('#rightSuggest');
  if (pane.hidden) return;
  const t = tab();
  if (!t) { pane.replaceChildren(h('div', { class: 'sg-empty' }, 'Open a file: what you can write at the cursor shows up here, in detail.')); return; }
  const items = panelItems();
  const c = S.comp;
  const q = c && sameContext() ? currentPrefix().slice(c.before.length) : '';
  const top = h('div', { class: 'sg-top' },
    h('div', { class: 'sg-ctx' }, c && sameContext()
      ? [`${items.length} suggestion${items.length === 1 ? '' : 's'}`, q ? [' for ', h('b', {}, q)] : ' here', ' · line ', String(c.line + 1)]
      : 'Move the cursor or start typing to see suggestions.'),
    c && c.hint && sameContext() ? hintNode(c.hint) : null);
  if (!items.length) {
    pane.replaceChildren(top, h('div', { class: 'sg-empty' }, c && sameContext() ? 'Nothing to suggest here. Ctrl+Space asks again.' : ''));
    return;
  }
  const selected = !$('#complete').hidden ? S.shown[S.sel] : null;
  const byGroup = new Map();
  for (const it of items.slice(0, 160)) {
    const g = groupOf(it);
    if (!byGroup.has(g)) byGroup.set(g, []);
    byGroup.get(g).push(it);
  }
  const groups = [...byGroup.entries()].sort((a, b) => {
    const ia = items.indexOf(a[1][0]), ib = items.indexOf(b[1][0]);
    return ia - ib;
  });
  pane.replaceChildren(top, ...groups.map(([g, list]) => h('div', { class: 'sg-group' },
    h('h4', {}, h('span', {}, (GROUPS.find(x => x[0] === g) || [g, g])[1]), h('span', {}, String(list.length))),
    ...list.map(it => {
      const on = it === selected || (!selected && it === S.panelSel);
      const el = h('div', {
        class: 'sg-item' + (on ? ' on' : ''), role: 'option', tabindex: '-1',
        onmousedown: e => e.preventDefault(),
        onclick: () => { S.panelSel = it; const i = S.shown.indexOf(it); if (i >= 0) select(i); else drawSuggest(); },
        ondblclick: () => acceptItem(it),
      },
        h('div', { class: 'sg-head' },
          h('span', { class: 'k ' + it.kind, title: it.kind }, kindLetter(it.kind)),
          h('span', { class: 'sg-label', html: highlightMatch(it.label, q) }),
          it.source ? h('span', { class: 'sg-src', title: it.source }, it.source) : null),
        it.detail ? h('div', { class: 'sg-detail' }, it.detail) : null,
        it.doc ? h('div', { class: 'sg-doc' }, it.doc) : null,
        on && it.insert ? h('pre', { class: 'sg-insert' }, insertPreview(it.insert)) : null,
        on ? h('div', { class: 'sg-actions' }, h('button', { onclick: e => { e.stopPropagation(); acceptItem(it); } }, 'Insert'), 'or Enter / Tab in the editor') : null);
      return el;
    }))));
  const on = pane.querySelector('.sg-item.on');
  if (on) on.scrollIntoView({ block: 'nearest' });
}

// ------------------------------------------------------------------ dialogs

function modal(title, fill, onClose) {
  const m = $('#modal');
  const body = h('div', { class: 'modal-body' });
  m.replaceChildren(h('div', { class: 'tag' }, h('strong', {}, title), h('span', { class: 'fill' }), h('nav', { class: 'cmds' }, h('button', { onclick: closeModal }, 'Close'))), body);
  fill(body);
  $('#modalBack').hidden = false;
  S.modalClose = onClose;
  const f = m.querySelector('input, select, button.btn');
  if (f) f.focus();
}

function closeModal() {
  $('#modalBack').hidden = true;
  if (S.modalClose) { const c = S.modalClose; S.modalClose = null; c(); }
  if (tab()) ED.ta.focus();
}

function ask(title, value, placeholder) {
  return new Promise(resolve => {
    let done = false;
    modal(title, body => {
      const input = h('input', { type: 'text', value: value || '', placeholder: placeholder || '', 'aria-label': title });
      body.append(h('form', { onsubmit: e => { e.preventDefault(); done = true; resolve(input.value.trim()); closeModal(); } },
        h('div', { class: 'field' }, input, h('button', { class: 'btn' }, 'OK'))));
      setTimeout(() => { input.focus(); input.setSelectionRange(input.value.length, input.value.length); });
    }, () => { if (!done) resolve(null); });
  });
}

// pick shows a filterable list; free allows any typed value.
function pick(title, items, onPick, placeholder, free) {
  modal(title, body => {
    const input = h('input', { class: 'search', placeholder: placeholder || 'type to filter', 'aria-label': title });
    const list = h('ul', { class: 'list' });
    let shown = [], sel = 0;
    const draw = () => {
      const q = input.value.trim();
      shown = items.map(it => [score(it.label, q), it]).filter(x => x[0] >= 0).sort((a, b) => a[0] - b[0] || a[1].label.localeCompare(b[1].label)).slice(0, 300).map(x => x[1]);
      sel = Math.min(sel, Math.max(0, shown.length - 1));
      list.replaceChildren(...shown.map((it, i) => h('li', {}, h('button', { class: i === sel ? 'on' : '', onclick: () => { closeModal(); onPick(it); } },
        h('span', { html: highlightMatch(it.label, q) }), it.detail ? h('small', {}, it.detail) : null))));
      const on = list.querySelector('.on');
      if (on) on.scrollIntoView({ block: 'nearest' });
    };
    input.addEventListener('input', () => { sel = 0; draw(); });
    input.addEventListener('keydown', e => {
      if (e.key === 'ArrowDown') { e.preventDefault(); sel = Math.min(sel + 1, shown.length - 1); draw(); }
      if (e.key === 'ArrowUp') { e.preventDefault(); sel = Math.max(sel - 1, 0); draw(); }
      if (e.key === 'Enter') {
        e.preventDefault();
        const it = shown[sel] || (free && input.value.trim() ? { label: input.value.trim() } : null);
        if (it) { closeModal(); onPick(it); }
      }
    });
    body.append(input, list);
    draw();
    setTimeout(() => input.focus());
  });
}

async function openFolderDialog() {
  const st = S.state || {};
  let dir = S.ws || st.home || '/';
  modal('Open a folder', body => {
    const input = h('input', { type: 'text', value: dir, 'aria-label': 'Folder path' });
    const list = h('ul', { class: 'list' });
    const load = async d => {
      try {
        const r = await api('/api/dirs', { path: d });
        dir = r.dir;
        input.value = dir;
        const up = dir === '/' ? [] : [h('li', {}, h('button', { onclick: () => load(dir.replace(/\/[^/]+$/, '') || '/') }, '../'))];
        list.replaceChildren(...up, ...r.dirs.map(n => h('li', {}, h('button', { onclick: () => load((dir === '/' ? '' : dir) + '/' + n), ondblclick: () => openWorkspace((dir === '/' ? '' : dir) + '/' + n) }, n + '/'))));
      } catch (e) { status(e.message, 'bad'); }
    };
    body.append(
      h('form', { onsubmit: e => { e.preventDefault(); load(input.value.trim()); } }, h('div', { class: 'field' }, input, h('button', { class: 'btn quiet', type: 'submit' }, 'Go'),
        h('button', { class: 'btn', type: 'button', onclick: () => openWorkspace(input.value.trim()) }, 'Open this folder'))),
      list);
    if (st.recent && st.recent.length) {
      body.append(h('h3', {}, 'Recent'), h('ul', { class: 'list' }, ...st.recent.map(r => h('li', {}, h('button', { onclick: () => openWorkspace(r) }, r)))));
    }
    load(dir);
  });
}

function settingsDialog() {
  const st = S.state || {};
  modal('pkide', body => {
    const repo = h('input', { type: 'text', value: st.repo || '', placeholder: '/path/to/Packenger', 'aria-label': 'Packenger repository' });
    const theme = h('select', { 'aria-label': 'Theme', onchange: async () => { applyTheme(theme.value); S.state = await api('/api/settings', { theme: theme.value }); } },
      h('option', { value: 'mocha' }, 'Catppuccin Mocha'), h('option', { value: 'latte' }, 'Catppuccin Latte'));
    theme.value = st.theme === 'latte' ? 'latte' : 'mocha';
    body.append(
      h('p', {}, 'The Packenger IDE. Completion and checking use the compiler’s own grammar; running and building use the build you choose.'),
      h('h3', {}, 'Packenger repository'),
      h('p', {}, 'A checkout of the Packenger source: for building versions from git, for formatting with tools/pkfmt.pkg, and as a registry.'),
      h('form', { onsubmit: async e => { e.preventDefault(); try { S.state = await api('/api/settings', { repo: repo.value.trim() }); await refreshProfile(); status('saved', 'good'); } catch (err) { status(err.message, 'bad'); } } },
        h('div', { class: 'field' }, repo, h('button', { class: 'btn' }, 'Save'))),
      h('h3', {}, 'Theme'), h('div', { class: 'field' }, theme),
      h('h3', {}, 'Install'),
      S.installPrompt
        ? h('div', { class: 'row' }, h('button', { class: 'btn', onclick: async () => { S.installPrompt.prompt(); await S.installPrompt.userChoice; S.installPrompt = null; closeModal(); } }, 'Install pkide as an app'))
        : h('p', {}, matchMedia('(display-mode: standalone)').matches ? 'pkide is running as an installed app.' : 'In Chrome, Edge or Brave, choose “Install pkide” in the address bar.'),
      h('p', {}, 'To add pkide to your app menu, and start it at login:'),
      h('pre', { class: 'code' }, 'pkide install --service'),
      h('h3', {}, 'Keys'),
      h('div', { class: 'shortcuts' },
        ...[['Ctrl+Space', 'suggest'], ['Tab', 'accept, or next placeholder'], ['F12, Ctrl+click', 'go to definition'], ['Ctrl+S', 'save'], ['Ctrl+Enter', 'run'], ['Ctrl+B', 'build'], ['Ctrl+Shift+T', 'test'], ['Alt+Shift+F', 'format'], ['Ctrl+P', 'open a file'], ['Ctrl+Shift+P', 'all commands'], ['Ctrl+/', 'comment lines'], ['Ctrl+O', 'open a folder'], ['Ctrl+W', 'close the tab']]
          .flatMap(([k, d]) => [h('span', {}, h('kbd', {}, k)), h('span', {}, d)])),
      h('p', { style: 'margin-top:12px' }, `Settings: ${st.configDir} · builds: ${st.dataDir}`));
  });
}

async function quickOpen() {
  if (!S.ws) { openFolderDialog(); return; }
  const files = [];
  const walk = async (dir, depth) => {
    if (depth > 8 || files.length > 4000) return;
    for (const e of await listDir(dir)) {
      const p = dir ? dir + '/' + e.name : e.name;
      if (e.dir) await walk(p, depth + 1); else files.push(p);
    }
  };
  status('listing files…');
  await walk('', 0);
  status('');
  pick('Open a file', files.map(f => ({ label: f })), it => openFile(it.label), 'file name');
}

const COMMANDS = [
  ['Run the file', 'run', 'Ctrl+Enter'], ['Build an executable', 'build', 'Ctrl+B'], ['Run tests', 'test', 'Ctrl+Shift+T'],
  ['Format with pkfmt', 'fmt', 'Alt+Shift+F'], ['Why is a package in the build?', 'why'], ['Show the package tree', 'tree'],
  ['Show the generated code (asm)', 'asm'], ['Lock package versions', 'lock'], ['Save', 'save', 'Ctrl+S'], ['Save all', 'saveAll'],
  ['Close the tab', 'close', 'Ctrl+W'], ['Open a file', 'quickOpen', 'Ctrl+P'], ['Open a folder', 'openFolder', 'Ctrl+O'],
  ['New file', 'newFile'], ['New folder', 'newFolder'], ['Go to definition', 'definition', 'F12'], ['Suggest', 'suggest', 'Ctrl+Space'],
  ['Build settings', 'builds', 'Ctrl+Shift+B'], ['Suggestions in detail', 'suggestions', 'Ctrl+I'], ['Show problems', 'problems'], ['Reload packages from the registry', 'reload'], ['Settings and install', 'settings'],
  ['Switch theme', 'theme'],
];

function palette() {
  pick('Commands', COMMANDS.map(([label, cmd, key]) => ({ label, cmd, detail: key || '' })), it => command(it.cmd));
}

async function command(cmd) {
  const t = tab();
  switch (cmd) {
    case 'run': case 'build': case 'tree': case 'asm': case 'lock': return runAction(cmd);
    case 'test': return t && !t.ro ? runAction('test', testTarget(t)) : status('open a file of the workspace first', 'bad');
    case 'why': return whyDialog();
    case 'fmt': return formatTab();
    case 'save': return save();
    case 'saveAll': return saveAll();
    case 'close': return closeTab();
    case 'quickOpen': return quickOpen();
    case 'openFolder': return openFolderDialog();
    case 'newFile': return newFile(t && !t.ro ? dirName(t.path) : '');
    case 'newFolder': return newFile('', true);
    case 'refreshFiles': S.dirs.clear(); return renderFiles();
    case 'definition': return goToDefinition();
    case 'suggest': ED.ta.focus(); return requestComplete(true);
    case 'builds': return showRight('build');
    case 'suggestions': return toggleRight('suggest');
    case 'problems': return showBottom('problems');
    case 'reload': await api('/api/reload'); for (const x of S.tabs) { loadGrammar(x); check(x); } return status('packages will be downloaded again', 'good');
    case 'settings': return settingsDialog();
    case 'theme': {
      const next = document.documentElement.dataset.theme === 'latte' ? 'mocha' : 'latte';
      applyTheme(next);
      S.state = await api('/api/settings', { theme: next });
      return;
    }
    case 'stop': if (S.proc && S.proc.running) await api('/api/proc/kill', { id: S.proc.id }); return;
    case 'clear': $('#console').replaceChildren(); consoleState.partial = ''; return;
  }
}

function applyTheme(t) {
  document.documentElement.dataset.theme = t === 'latte' ? 'latte' : 'mocha';
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = t === 'latte' ? '#dce0e8' : '#11111b';
  requestAnimationFrame(() => { if (ED.root) measure(); });
}

// ------------------------------------------------------------------ welcome and workspace

function renderWelcome() {
  const w = $('#welcome');
  const st = S.state || {};
  w.replaceChildren(
    h('h1', {}, 'pkide'),
    h('p', {}, S.ws ? `Working in ${S.ws}. Pick a file on the left, or start a new one.` : 'An editor for Packenger programs and packages. Open a folder to start.'),
    h('div', { class: 'cols' },
      h('div', {}, h('h2', {}, 'Start'), h('ul', {},
        h('li', {}, h('button', { onclick: openFolderDialog }, 'Open a folder…')),
        S.ws ? h('li', {}, h('button', { onclick: () => newFile('') }, 'New file…')) : null,
        S.ws ? h('li', {}, h('button', { onclick: quickOpen }, 'Open a file…')) : null,
        h('li', {}, h('button', { onclick: () => showRight('build') }, 'Build settings')),
        h('li', {}, h('button', { onclick: settingsDialog }, 'Settings and install')))),
      st.recent && st.recent.length ? h('div', {}, h('h2', {}, 'Recent folders'), h('ul', {}, ...st.recent.slice(0, 8).map(r => h('li', {}, h('button', { onclick: () => openWorkspace(r), title: r }, r.replace(st.home || '\u0000', '~')))))) : null,
      h('div', {}, h('h2', {}, 'Keys'), h('dl', {},
        ...[['Ctrl+Space', 'suggest'], ['Tab', 'accept, next placeholder'], ['F12', 'go to definition'], ['Ctrl+Enter', 'run'], ['Ctrl+P', 'open a file'], ['Ctrl+Shift+P', 'all commands']]
          .flatMap(([k, d]) => [h('dt', {}, h('kbd', {}, k)), h('dd', {}, d)])))));
}

async function openWorkspace(ws) {
  if (!ws) return;
  if (S.tabs.some(dirty) && !confirm('Some files have unsaved changes. Switch folders and lose them?')) return;
  try {
    const r = await api('/api/workspace', { ws });
    closeModal();
    S.ws = r.ws;
    S.state = r.state;
    S.profile = r.profile;
    S.tabs = [];
    S.active = null;
    S.dirs.clear();
    S.open = new Set(['', ...store.get('open:' + S.ws, [])]);
    history.replaceState(null, '', '#ws=' + encodeURIComponent(S.ws));
    $('#wsButton').textContent = S.ws.replace(S.state.home || '\u0000', '~');
    $('#wsButton').title = S.ws + ' (Ctrl+O to open another folder)';
    renderBuildSelect();
    activate(null);
    renderWelcome();
    renderFiles();
    const sess = store.get('session:' + S.ws, null);
    if (sess && sess.tabs) {
      for (const p of sess.tabs) await openFile(p).catch(() => {});
      const t = S.tabs.find(x => x.path === sess.active);
      if (t) activate(t);
    }
  } catch (e) { status(e.message, 'bad'); }
}

function saveSession() {
  if (!S.ws) return;
  store.set('session:' + S.ws, { tabs: S.tabs.filter(t => !t.ro).map(t => t.path), active: tab() && tab().path });
  store.set('open:' + S.ws, [...S.open].filter(Boolean));
}

// ------------------------------------------------------------------ layout

function splitter(el, prop, axis, min, max, invert) {
  el.addEventListener('pointerdown', e => {
    e.preventDefault();
    el.setPointerCapture(e.pointerId);
    const start = axis === 'x' ? e.clientX : e.clientY;
    const host = prop === '--bottom-h' ? $('#main') : $('#app');
    const cur = parseFloat(getComputedStyle(host).getPropertyValue(prop)) || ({ '--side-w': 280, '--right-w': 380 }[prop] || 220);
    const move = ev => {
      const d = (axis === 'x' ? ev.clientX : ev.clientY) - start;
      const v = Math.max(min, Math.min(max(), cur + (invert ? -d : d)));
      host.style.setProperty(prop, v + 'px');
      store.set(prop, v);
    };
    const up = () => { el.removeEventListener('pointermove', move); el.removeEventListener('pointerup', up); positionComplete(); };
    el.addEventListener('pointermove', move);
    el.addEventListener('pointerup', up);
  });
  const saved = store.get(prop, null);
  if (saved) (prop === '--bottom-h' ? $('#main') : $('#app')).style.setProperty(prop, saved + 'px');
}

// ------------------------------------------------------------------ start

function wire() {
  document.addEventListener('click', e => {
    const b = e.target.closest('[data-cmd]');
    if (b && !b.disabled) command(b.dataset.cmd);
    const s = e.target.closest('[data-side]');
    if (s) showSide(s.dataset.side);
    const bt = e.target.closest('[data-bottom]');
    if (bt) showBottom(bt.dataset.bottom);
    const rt = e.target.closest('[data-right]');
    if (rt) { if (rt.closest('.toggles')) toggleRight(rt.dataset.right); else showRight(rt.dataset.right); }
  });
  $('#brand').onclick = settingsDialog;
  $('#wsButton').onclick = openFolderDialog;
  $('#profileChip').onclick = () => toggleRight('build');
  $('#modalBack').addEventListener('mousedown', e => { if (e.target.id === 'modalBack') closeModal(); });
  $('#stdinForm').onsubmit = async e => {
    e.preventDefault();
    const input = $('#stdin');
    if (!S.proc || !S.proc.running) return;
    consoleWrite(input.value + '\n', 'cmd');
    try { await api('/api/proc/input', { id: S.proc.id, text: input.value + '\n' }); } catch (err) { status(err.message, 'bad'); }
    input.value = '';
  };
  document.addEventListener('keydown', e => {
    const mod = e.ctrlKey || e.metaKey;
    if (e.key === 'Escape' && !$('#modalBack').hidden) { e.preventDefault(); closeModal(); return; }
    if (mod && e.key.toLowerCase() === 's') { e.preventDefault(); if (e.shiftKey) saveAll(); else save(); return; }
    if (mod && e.key === 'Enter') { e.preventDefault(); command('run'); return; }
    if (mod && e.key.toLowerCase() === 'b' && !e.shiftKey) { e.preventDefault(); command('build'); return; }
    if (mod && e.shiftKey && e.key.toLowerCase() === 'b') { e.preventDefault(); toggleRight('build'); return; }
    if (mod && !e.shiftKey && e.key.toLowerCase() === 'i') { e.preventDefault(); toggleRight('suggest'); return; }
    if (mod && e.shiftKey && e.key.toLowerCase() === 't') { e.preventDefault(); command('test'); return; }
    if (mod && e.shiftKey && e.key.toLowerCase() === 'p') { e.preventDefault(); palette(); return; }
    if (mod && !e.shiftKey && e.key.toLowerCase() === 'p') { e.preventDefault(); quickOpen(); return; }
    if (mod && e.key.toLowerCase() === 'o') { e.preventDefault(); openFolderDialog(); return; }
    if (mod && e.key.toLowerCase() === 'w') { e.preventDefault(); closeTab(); return; }
    if (e.altKey && e.shiftKey && e.key.toLowerCase() === 'f') { e.preventDefault(); formatTab(); return; }
  });
  window.addEventListener('beforeunload', e => { stash(); if (S.tabs.some(dirty)) { e.preventDefault(); e.returnValue = ''; } });
  window.addEventListener('resize', () => { positionComplete(); hideHover(); });
  window.addEventListener('beforeinstallprompt', e => { e.preventDefault(); S.installPrompt = e; });
  splitter($('#splitSide'), '--side-w', 'x', 160, () => innerWidth * 0.5);
  splitter($('#splitBottom'), '--bottom-h', 'y', 80, () => innerHeight * 0.7, true);
  splitter($('#splitRight'), '--right-w', 'x', 260, () => innerWidth * 0.5, true);
}

async function start() {
  buildEditor();
  ED.root.hidden = true;
  wire();
  try { S.state = await api('/api/state'); } catch (e) { status(e.message, 'bad'); }
  applyTheme((S.state && S.state.theme) || '');
  document.fonts && document.fonts.ready.then(() => { measure(); queueRender(); });
  const m = location.hash.match(/ws=([^&]+)/);
  const ws = m ? decodeURIComponent(m[1]) : (S.state && S.state.recent && S.state.recent[0]);
  renderWelcome();
  renderFiles();
  renderBuildSelect();
  renderProblems();
  showBottom('console');
  showRight(store.get('right', 'suggest'));
  if (ws) await openWorkspace(ws);
  if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
}

start();
