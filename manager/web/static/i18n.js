// Translations of the web UI (es / en / fr). No build and no dependencies: the dictionaries live in
// lang-<code>.js (plain objects, loaded after this file and before app.js) and this file gives
// t(key, params), the data-i18n attributes of index.html and Intl formatting in the active language.
// CSP-safe: no eval, no inline code, text only through textContent.
//
// Dictionary values: a string with {param} placeholders, or plural forms {one, other, many?} chosen
// with Intl.PluralRules on params.n. A `code` span (between backticks) becomes a <code> element in
// data-i18n elements and in tNodes().
'use strict';

const LANGS = ['es', 'en', 'fr'];
// Each language is offered in its own name, whatever the active language
const LANG_NAMES = { es: 'Español', en: 'English', fr: 'Français' }; // i18n-ok: endonyms
const LOCALES = { es: 'es-ES', en: 'en-GB', fr: 'fr-FR' };
const LANG_KEY = 'eirl.lang'; // per-browser override (localStorage)

window.I18N = window.I18N || {};

const i18n = { lang: 'en', listeners: [], missing: new Set(), plural: {} };

// 'es-ES', 'fr', 'EN' → 'es' / 'fr' / 'en'; anything else → ''
function normLang(v) {
  if (typeof v !== 'string') return '';
  const l = v.trim().toLowerCase().slice(0, 2);
  return LANGS.includes(l) ? l : '';
}

function getLang() { return i18n.lang; }
function getLocale() { return LOCALES[i18n.lang]; }

function lookup(lang, key) {
  const d = window.I18N[lang];
  return d ? d[key] : undefined;
}

function pluralForm(n) {
  const lang = i18n.lang;
  if (!i18n.plural[lang]) i18n.plural[lang] = new Intl.PluralRules(LOCALES[lang]);
  return i18n.plural[lang].select(n);
}

// French typography: a narrow no-break space before ; : ! ? » and after «, so they never start a line.
// Applied to the template, never to the parameters (an SSID or a name stays as it is).
function typo(s) {
  if (i18n.lang !== 'fr') return s;
  return s.replace(/ ([;:!?»])/g, ' $1').replace(/« /g, '« ');
}

function t(key, params) {
  let v = lookup(i18n.lang, key);
  if (v === undefined) v = lookup('en', key);
  if (v === undefined) {
    if (!i18n.missing.has(key)) { i18n.missing.add(key); console.warn('i18n: missing key', key); }
    return key;
  }
  if (typeof v === 'object') {
    const n = params && typeof params.n === 'number' ? params.n : 0;
    v = v[pluralForm(n)] !== undefined ? v[pluralForm(n)] : v.other;
  }
  v = typo(v);
  if (!params) return v;
  return v.replace(/\{(\w+)\}/g, (m, k) => (params[k] === undefined || params[k] === null ? m : String(params[k])));
}

// The text with its `code` spans as nodes (for an element that mixes text and <code>)
function tNodes(key, params) {
  return t(key, params).split('`').map((part, i) => {
    if (!(i % 2)) return document.createTextNode(part);
    const c = document.createElement('code');
    c.textContent = part;
    return c;
  });
}

function setI18nText(el, key) {
  const text = t(key);
  if (text.includes('`')) el.replaceChildren(...tNodes(key));
  else el.textContent = text;
}

// data-i18n="key": the element's text. data-i18n-attr="aria-label:key; placeholder:key2": attributes.
function applyI18n(root) {
  const scope = root || document;
  const all = (sel) => {
    const list = [...scope.querySelectorAll(sel)];
    if (scope.matches && scope.matches(sel)) list.unshift(scope);
    return list;
  };
  for (const el of all('[data-i18n]')) setI18nText(el, el.dataset.i18n);
  for (const el of all('[data-i18n-attr]')) {
    for (const pair of el.dataset.i18nAttr.split(';')) {
      const i = pair.indexOf(':');
      if (i > 0) el.setAttribute(pair.slice(0, i).trim(), t(pair.slice(i + 1).trim()));
    }
  }
}

// --- Choosing the language --------------------------------------------------------------------
// Device language (GET /device) unless this browser chose another one (the header picker); without
// the device's (unreachable, older app), the browser's; then English.

function langOverride() {
  try { return normLang(localStorage.getItem(LANG_KEY)); } catch (_) { return ''; }
}

function setLangOverride(lang) {
  try {
    if (lang) localStorage.setItem(LANG_KEY, lang);
    else localStorage.removeItem(LANG_KEY);
  } catch (_) { /* private mode, blocked storage: the choice lasts until reload */ }
}

function browserLang() {
  for (const l of navigator.languages || [navigator.language]) {
    const n = normLang(l);
    if (n) return n;
  }
  return '';
}

function onLangChange(fn) { i18n.listeners.push(fn); }

function setLang(lang) {
  const l = normLang(lang) || 'en';
  const changed = l !== i18n.lang;
  i18n.lang = l;
  document.documentElement.lang = l;
  applyI18n(document);
  syncLangPickers();
  if (changed) for (const fn of i18n.listeners) fn(l);
}

// <select> with the three languages. short: the code only (the compact header picker)
function fillLangSelect(sel, short) {
  if (sel.options.length !== LANGS.length) {
    sel.replaceChildren(...LANGS.map((l) => {
      const o = document.createElement('option');
      o.value = l;
      o.textContent = short ? l.toUpperCase() : LANG_NAMES[l];
      o.lang = l;
      if (short) o.title = LANG_NAMES[l];
      return o;
    }));
  }
}

function syncLangPickers() {
  for (const sel of document.querySelectorAll('select.lang-pick')) {
    fillLangSelect(sel, true);
    sel.value = i18n.lang;
  }
}

// The per-browser pickers (header, login): they override the device language in this browser only
document.addEventListener('change', (ev) => {
  const sel = ev.target;
  if (!(sel instanceof HTMLSelectElement) || !sel.classList.contains('lang-pick')) return;
  const l = normLang(sel.value);
  if (!l) return;
  setLangOverride(l);
  setLang(l);
});

// --- Formatting -------------------------------------------------------------------------------

function fmtNum(v, digits) {
  return Number(v).toLocaleString(getLocale(), { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

function fmtTime(d, opts) {
  return d.toLocaleTimeString(getLocale(), opts || { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
}

function fmtDateTime(d, opts) {
  return d.toLocaleString(getLocale(), opts || { dateStyle: 'medium', timeStyle: 'short', hourCycle: 'h23' });
}

// «hace 5 minutos» / «5 minutes ago» / «il y a 5 minutes»
function fmtRelative(d) {
  const s = (d.getTime() - Date.now()) / 1000;
  const rtf = new Intl.RelativeTimeFormat(getLocale(), { numeric: 'auto' });
  const abs = Math.abs(s);
  if (abs < 60) return rtf.format(Math.round(s), 'second');
  if (abs < 3600) return rtf.format(Math.round(s / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(s / 3600), 'hour');
  return rtf.format(Math.round(s / 86400), 'day');
}
