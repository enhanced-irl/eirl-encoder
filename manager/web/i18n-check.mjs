#!/usr/bin/env node
// Development check of the web UI translations (static/i18n.js + static/lang-*.js). Not embedded in
// the binary (only static/ is). Run it after touching any text of the web:
//
//   node manager/web/i18n-check.mjs
//
// Fails (exit 1) when:
//   - a key is missing in some language, or a language has an extra key;
//   - the {params} of a key differ between languages, or a plural key is not plural everywhere;
//   - index.html or the JS use a key (data-i18n, data-i18n-attr, t('…'), tNodes('…')) that does not exist;
//   - index.html or the JS (outside the dictionaries) still hold Spanish-looking text: accented letters,
//     ¿ ¡ ñ, or string literals / HTML text with Spanish words; or index.html has visible text outside a
//     data-i18n element. A line with the marker `i18n-ok` is skipped (e.g. a list of allowed characters).
// Warns (no failure) about keys no code seems to use.
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const STATIC = join(dirname(fileURLToPath(import.meta.url)), 'static');
const LANGS = ['es', 'en', 'fr'];
const errors = [];
const warnings = [];
const err = (m) => errors.push(m);

// --- Dictionaries --------------------------------------------------------------------------------

const dicts = {};
for (const l of LANGS) {
  const file = `lang-${l}.js`;
  const sandbox = { window: {} };
  try {
    vm.runInNewContext(readFileSync(join(STATIC, file), 'utf8'), sandbox, { filename: file });
  } catch (e) {
    err(`${file}: does not load: ${e.message}`);
    continue;
  }
  const d = sandbox.window.I18N && sandbox.window.I18N[l];
  if (!d || typeof d !== 'object') err(`${file}: does not define window.I18N.${l}`);
  else dicts[l] = d;
}
if (errors.length) finish();

const PLURAL_FORMS = ['zero', 'one', 'two', 'few', 'many', 'other'];
const paramsOf = (v) => {
  const texts = typeof v === 'string' ? [v] : Object.values(v);
  const set = new Set();
  for (const s of texts) for (const m of String(s).matchAll(/\{(\w+)\}/g)) set.add(m[1]);
  return [...set].sort().join(',');
};

const allKeys = new Set(LANGS.flatMap((l) => Object.keys(dicts[l])));
for (const k of [...allKeys].sort()) {
  const vals = {};
  for (const l of LANGS) {
    if (!(k in dicts[l])) { err(`missing key '${k}' in ${l}`); continue; }
    const v = dicts[l][k];
    if (typeof v === 'object' && v !== null) {
      for (const f of Object.keys(v)) if (!PLURAL_FORMS.includes(f)) err(`${l} '${k}': unknown plural form '${f}'`);
      if (!('one' in v) || !('other' in v)) err(`${l} '${k}': a plural needs at least 'one' and 'other'`);
      for (const [f, s] of Object.entries(v)) if (typeof s !== 'string' || !s) err(`${l} '${k}.${f}': empty or not a string`);
    } else if (typeof v !== 'string' || !v) {
      err(`${l} '${k}': empty or not a string`);
    }
    vals[l] = v;
  }
  const present = LANGS.filter((l) => l in vals);
  const kinds = new Set(present.map((l) => typeof vals[l]));
  if (kinds.size > 1) err(`'${k}': plural in some languages and plain in others (${present.map((l) => `${l}=${typeof vals[l] === 'object' ? 'plural' : 'text'}`).join(' ')})`);
  const ps = new Set(present.map((l) => paramsOf(vals[l])));
  if (ps.size > 1) err(`'${k}': different {params}: ${present.map((l) => `${l}=[${paramsOf(vals[l])}]`).join(' ')}`);
  // Backticks mark <code>: an odd number would leave a span open
  for (const l of present) {
    for (const s of typeof vals[l] === 'string' ? [vals[l]] : Object.values(vals[l])) {
      if ((s.match(/`/g) || []).length % 2) err(`${l} '${k}': unbalanced backtick`);
    }
  }
}

// --- Keys used by the page -------------------------------------------------------------------------

const html = readFileSync(join(STATIC, 'index.html'), 'utf8');
const jsFiles = readdirSync(STATIC).filter((f) => f.endsWith('.js') && !/^lang-\w+\.js$/.test(f)).sort();
const js = Object.fromEntries(jsFiles.map((f) => [f, readFileSync(join(STATIC, f), 'utf8')]));

const used = new Set();
const prefixes = new Set();
const refs = []; // [file, key]
const addAttrKeys = (file, src) => {
  for (const m of src.matchAll(/data-i18n="([^"]*)"/g)) refs.push([file, m[1]]);
  for (const m of src.matchAll(/data-i18n-attr="([^"]*)"/g)) {
    for (const pair of m[1].split(';')) {
      const i = pair.indexOf(':');
      if (i <= 0) { err(`${file}: bad data-i18n-attr "${m[1]}"`); continue; }
      refs.push([file, pair.slice(i + 1).trim()]);
    }
  }
};
addAttrKeys('index.html', html);
for (const [f, src] of Object.entries(js)) {
  // data-i18n in JS: only inside string literals (markup built in code), never in comments
  addAttrKeys(f, jsLiterals(src).filter((l) => l.kind === 'string').map((l) => l.text).join('\n'));
  for (const m of src.matchAll(/(?<![\w$.])(?:t|tNodes)\(\s*'([^']+)'\s*[,)]/g)) refs.push([f, m[1]]);
  for (const m of src.matchAll(/(?<![\w$.])(?:t|tNodes)\(\s*'([\w.]+\.)'\s*\+/g)) prefixes.add(m[1]);
  // t(`setup.${id}.title`): every key of that shape is used
  for (const m of src.matchAll(/(?<![\w$.])(?:t|tNodes)\(\s*`([\w.]*\$\{[^}]+\}[\w.]*)`/g)) {
    const re = new RegExp('^' + m[1].split(/\$\{[^}]+\}/).map((p) => p.replace(/\./g, '\\.')).join('\\w+') + '$');
    for (const k of allKeys) if (re.test(k)) used.add(k);
  }
  // A literal equal to a key is a key passed around (state.rtcError = 'live.noRTC', tables of keys)
  for (const m of src.matchAll(/'([a-z][\w]*(?:\.[\w]+)+)'/g)) if (allKeys.has(m[1])) used.add(m[1]);
}
for (const [file, key] of refs) {
  used.add(key);
  if (!allKeys.has(key)) err(`${file}: uses key '${key}', which no dictionary has`);
}
const unused = [...allKeys].filter((k) => !used.has(k) && ![...prefixes].some((p) => k.startsWith(p)));
for (const k of unused.sort()) warnings.push(`key '${k}' seems unused`);

// --- Spanish left outside the dictionaries ---------------------------------------------------------

const ACCENTS = /[áéíóúüñÁÉÍÓÚÜÑ¿¡]/;
// Words that do not occur in English UI text or code: two function words, or one content word, in the
// same literal flag it
const ES_FUNCTION = new Set(['de', 'del', 'la', 'las', 'los', 'el', 'que', 'con', 'sin', 'para', 'por', 'una', 'unos', 'unas', 'y', 'hay', 'al', 'se', 'su', 'sus', 'lo', 'ya', 'cuando', 'como', 'pero', 'mientras', 'cada', 'desde', 'hasta', 'otra', 'otro', 'esta', 'este', 'aqui', 'solo', 'tu', 'tus']);
const ES_CONTENT = new Set(['guardar', 'guardado', 'cancelar', 'contrasena', 'codificador', 'conectar', 'conectado', 'conectada', 'aplicar', 'apagar', 'apagado', 'apagada', 'encender', 'encendida', 'emitir', 'emision', 'fuente', 'fuentes', 'senal', 'camara', 'camaras', 'salir', 'entrar', 'enlace', 'enlaces', 'destino', 'ajustes', 'red', 'redes', 'vincular', 'cuenta', 'buscar', 'quitar', 'mostrar', 'ocultar', 'pantalla', 'reiniciar', 'parado', 'esperando', 'nueva', 'nuevo', 'cambiar', 'seguir']);
const fold = (s) => s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();
const spanishWords = (text) => {
  const words = fold(text).split(/[^a-z]+/).filter(Boolean);
  const fn = new Set(words.filter((w) => ES_FUNCTION.has(w)));
  const content = words.filter((w) => ES_CONTENT.has(w));
  return fn.size >= 2 || content.length ? [...fn, ...content].join(', ') : '';
};

function checkLines(file, src) {
  src.split('\n').forEach((line, i) => {
    if (line.includes('i18n-ok')) return;
    const m = line.match(ACCENTS);
    if (m) err(`${file}:${i + 1}: Spanish-looking character '${m[0]}': ${line.trim().slice(0, 100)}`);
  });
}

// JS: every string literal, template literal and comment (a light tokenizer, good enough for our code)
function jsLiterals(src) {
  const out = [];
  let i = 0;
  let line = 1;
  let prev = ''; // last significant character, to tell a regex from a division
  while (i < src.length) {
    const c = src[i];
    if (c === '\n') { line++; i++; continue; }
    if (c === '/' && src[i + 1] === '/') { const e = src.indexOf('\n', i); const end = e < 0 ? src.length : e; out.push({ kind: 'comment', text: src.slice(i + 2, end), line }); i = end; continue; }
    if (c === '/' && src[i + 1] === '*') { const e = src.indexOf('*/', i + 2); const text = src.slice(i + 2, e); out.push({ kind: 'comment', text, line }); line += (text.match(/\n/g) || []).length; i = e + 2; continue; }
    if (c === '\'' || c === '"' || c === '`') {
      let j = i + 1;
      let text = '';
      const startLine = line;
      while (j < src.length && src[j] !== c) {
        if (src[j] === '\\') { text += src[j + 1]; j += 2; continue; }
        if (c === '`' && src[j] === '$' && src[j + 1] === '{') { // skip ${…}, roughly
          let depth = 1; j += 2;
          while (j < src.length && depth) { if (src[j] === '{') depth++; else if (src[j] === '}') depth--; if (src[j] === '\n') line++; j++; }
          text += ' ';
          continue;
        }
        if (src[j] === '\n') line++;
        text += src[j];
        j++;
      }
      out.push({ kind: 'string', text, line: startLine });
      i = j + 1;
      prev = 'a';
      continue;
    }
    if (c === '/' && (prev === '' || '(,=:[!&|?{};+-*%<>~^'.includes(prev) || /\b(return|typeof|case)\s*$/.test(src.slice(Math.max(0, i - 8), i)))) {
      let j = i + 1;
      let cls = false;
      while (j < src.length && (cls || src[j] !== '/') && src[j] !== '\n') {
        if (src[j] === '\\') j++;
        else if (src[j] === '[') cls = true;
        else if (src[j] === ']') cls = false;
        j++;
      }
      i = j + 1;
      while (/[a-z]/.test(src[i] || '')) i++;
      prev = 'a';
      continue;
    }
    if (!/\s/.test(c)) prev = c;
    i++;
  }
  return out;
}

for (const [f, src] of Object.entries(js)) {
  checkLines(f, src);
  const lines = src.split('\n');
  for (const lit of jsLiterals(src)) {
    if (lit.kind !== 'string' || (lines[lit.line - 1] || '').includes('i18n-ok')) continue;
    const words = spanishWords(lit.text);
    if (words) err(`${f}:${lit.line}: string literal looks Spanish (${words}): ${JSON.stringify(lit.text.slice(0, 80))}`);
  }
}

// HTML: accents anywhere; text and user-facing attributes with Spanish words; visible text outside
// data-i18n elements (it would not change with the language)
checkLines('index.html', html);
const lineAt = (pos) => html.slice(0, pos).split('\n').length;
for (const m of html.matchAll(/\s(alt|title|placeholder|aria-label|value)="([^"]*)"/g)) {
  const words = spanishWords(m[2]);
  if (words) err(`index.html:${lineAt(m.index)}: attribute ${m[1]} looks Spanish (${words}): "${m[2]}"`);
}
const ALLOWED_TEXT = new Set(['SRT', 'RTMP']);
const body = html.replace(/<!--[\s\S]*?-->/g, (c) => c.replace(/[^\n]/g, ' '));
const tagRe = /<(\/?)([a-zA-Z0-9]+)([^>]*)>/g;
let last = null;
let pre = 0;
let m;
while ((m = tagRe.exec(body))) {
  if (last) {
    const text = body.slice(last.end, m.index);
    const trimmed = text.trim();
    if (trimmed && !pre && !last.i18n && /[A-Za-zÀ-ÿ]{3,}/.test(trimmed) && !ALLOWED_TEXT.has(trimmed)) {
      err(`index.html:${lineAt(last.end)}: visible text outside a data-i18n element: "${trimmed.slice(0, 60)}"`);
    }
    const words = spanishWords(trimmed);
    if (trimmed && words) err(`index.html:${lineAt(last.end)}: text looks Spanish (${words}): "${trimmed.slice(0, 60)}"`);
  }
  const [, close, tag, attrs] = m;
  if (tag.toLowerCase() === 'pre') pre += close ? -1 : 1;
  last = { end: m.index + m[0].length, i18n: !close && /\sdata-i18n="/.test(attrs) };
}

finish();

function finish() {
  for (const w of warnings) console.log('warning: ' + w);
  for (const e of errors) console.log('ERROR: ' + e);
  const keys = dicts.en ? Object.keys(dicts.en).length : 0;
  console.log(errors.length ? `i18n-check: ${errors.length} error(s)` : `i18n-check: OK (${keys} keys × ${LANGS.length} languages, ${warnings.length} warning(s))`);
  process.exit(errors.length ? 1 : 0);
}
