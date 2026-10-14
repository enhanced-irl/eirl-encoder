// First-start wizard of the web (when the device has no touch screen, or from any browser meanwhile):
// language → password (only while there is none) → time → network → name → EnhancedIRL → done. The
// order is the web's: without a password the app only answers the setup routes and the device language,
// so the password goes right after the language. Each step is marked with POST /setup/step; the touch
// screen may run its own wizard at the same time, so GET /setup is polled and the web leaves as soon as
// setup is done there. Loaded after app.js and settings.js (uses api, t, show, startApp, showLogin,
// refreshStatus, renderCloud, fillZones, loadZones, deviceChanges, deviceErrors, hostMoved, cleanName).
'use strict';

const wiz = { setup: null, order: [], cur: '', busy: false, authed: false, pollTimer: 0, stepTimer: 0, clockTimer: 0, borrowed: [] };
const WIZ_SKIPPABLE = ['network', 'cloud'];

function wizOrder(setup) {
  const o = ['language'];
  if (setup.needsPassword) o.push('password');
  o.push('time', 'network', 'name', 'cloud', 'done');
  return o;
}

const stepState = (id) => {
  const s = wiz.setup && (wiz.setup.steps || []).find((x) => x.id === id);
  return s ? s.state : 'todo';
};

function showSetup(setup) {
  stopLive();
  wiz.setup = setup;
  wiz.order = wizOrder(setup);
  // With a password already set we only get here signed in (boot / afterLogin)
  wiz.authed = !setup.needsPassword;
  show('setup');
  const first = wiz.order.find((id) => id !== 'done' && stepState(id) === 'todo') || 'done';
  goStep(first);
  clearTimeout(wiz.pollTimer);
  wiz.pollTimer = setTimeout(pollSetup, 5000);
}

function leaveWizard() {
  leaveStep();
  clearTimeout(wiz.pollTimer);
  wiz.cur = '';
}

// The touch screen (or another browser) may finish setup, or set the password, meanwhile
async function pollSetup() {
  clearTimeout(wiz.pollTimer);
  if ($('setup').hidden) return;
  const s = await fetchSetup();
  if ($('setup').hidden) return;
  if (s) {
    if (s.done) {
      leaveWizard();
      if (wiz.authed) startApp(); else showLogin();
      toast(t('setup.doneElsewhere'));
      return;
    }
    if (!s.needsPassword && !wiz.authed) {
      leaveWizard();
      showLogin();
      toast(t('setup.passwordElsewhere'));
      return;
    }
    wiz.setup = { ...wiz.setup, ...s, needsPassword: wiz.setup.needsPassword && s.needsPassword };
    renderStepper();
  }
  wiz.pollTimer = setTimeout(pollSetup, 5000);
}

// --- Steps -----------------------------------------------------------------------------------

function renderStepper() {
  const ol = $('setup-steps');
  const idx = wiz.order.indexOf(wiz.cur);
  ol.replaceChildren(...wiz.order.map((id, i) => {
    const li = document.createElement('li');
    const st = id === 'done' ? (wiz.cur === 'done' ? 'current' : 'todo') : id === wiz.cur ? 'current' : stepState(id);
    li.className = 'setup-step s-' + st;
    if (id === wiz.cur) li.setAttribute('aria-current', 'step');
    const n = document.createElement('span');
    n.className = 'setup-n';
    n.textContent = st === 'done' ? '✓' : String(i + 1);
    n.setAttribute('aria-hidden', 'true');
    const label = document.createElement('span');
    label.textContent = t('setup.step.' + id);
    li.append(n, label);
    if (st === 'skipped') li.title = t('setup.skipped');
    return li;
  }));
  $('setup-count').textContent = t('setup.count', { n: idx + 1, total: wiz.order.length });
  $('setup-bar').style.width = `${Math.round((idx / Math.max(1, wiz.order.length - 1)) * 100)}%`;
}

function renderStepTexts() {
  const id = wiz.cur;
  $('setup-h').textContent = t(`setup.${id}.title`);
  $('setup-intro').textContent = t(`setup.${id}.intro`);
  const i = wiz.order.indexOf(id);
  $('setup-back').hidden = i <= 0;
  $('setup-skip').hidden = !WIZ_SKIPPABLE.includes(id);
  $('setup-next').textContent = id === 'done' ? t('setup.finish') : t('setup.next');
  renderStepper();
}

async function goStep(id) {
  leaveStep();
  wiz.cur = id;
  for (const p of document.querySelectorAll('#setup-form .setup-panel')) p.hidden = p.dataset.step !== id;
  for (const p of $('setup-form').querySelectorAll('.err')) p.hidden = true;
  renderStepTexts();
  await enterStep(id);
  // The text fields get the focus; elsewhere the heading (a screen reader reads the new step)
  const first = { password: 'sp-pw', name: 'sp-name' }[id];
  const el = first && $(first);
  if (el && !el.closest('[hidden]')) el.focus();
  else $('setup-h').focus();
}

function leaveStep() {
  clearTimeout(wiz.stepTimer);
  clearTimeout(wiz.clockTimer);
  giveBack();
}

// The dashboard's Wi-Fi and account cards are moved into their step and back
function borrow(id, slotId) {
  const el = $(id);
  if (!el || el.dataset.borrowed) return el;
  const mark = document.createComment('slot:' + id);
  el.before(mark);
  el.dataset.borrowed = '1';
  wiz.borrowed.push([el, mark]);
  $(slotId).append(el);
  return el;
}

function giveBack() {
  for (const [el, mark] of wiz.borrowed) {
    mark.replaceWith(el);
    delete el.dataset.borrowed;
  }
  wiz.borrowed = [];
}

async function enterStep(id) {
  if (id === 'language') {
    const box = $('sp-langs');
    if (!box.children.length) {
      box.replaceChildren(...LANGS.map((l) => {
        const lab = document.createElement('label');
        lab.className = 'setup-choice';
        lab.lang = l;
        const r = document.createElement('input');
        r.type = 'radio';
        r.name = 'setup-lang';
        r.value = l;
        r.addEventListener('change', () => { if (r.checked) setLang(l); }); // a preview; saved on Continue
        const name = document.createElement('span');
        name.textContent = LANG_NAMES[l];
        lab.append(r, name);
        return lab;
      }));
    }
    for (const r of box.querySelectorAll('input')) r.checked = r.value === getLang();
  } else if (id === 'password') {
    const need = !!wiz.setup.needsPassword;
    $('sp-pw-fields').hidden = !need;
    $('sp-pw-set').hidden = need;
    $('sp-code-row').hidden = wiz.setup.codeRequired === false;
  } else if (id === 'time') {
    await wizDevice();
    if (!dev.zones) await loadZones();
    const d = state.device || {};
    fillZones($('sp-tz'), suggestZone(d));
    const ntp = !d.time || d.time.ntp !== false;
    $('sp-ntp').checked = ntp;
    $('sp-time-row').hidden = ntp;
    $('sp-time').value = localInputValue(new Date());
    delete $('sp-time').dataset.touched;
    wizClock();
  } else if (id === 'network') {
    borrow('wifi-card', 'sp-net-slot');
    await wizNetTick();
  } else if (id === 'name') {
    await wizDevice();
    $('sp-name').value = (state.device && state.device.name) || '';
    $('sp-moved').hidden = true;
    renderNameHint();
  } else if (id === 'cloud') {
    borrow('cloud-card', 'sp-cloud-slot');
    await wizCloudTick();
  } else if (id === 'done') {
    await wizDevice();
    renderSummary();
  }
}

async function wizDevice() {
  try { state.device = await api('GET', '/device', undefined, { quiet: true }); } catch (_) { /* keep the last one */ }
}

// The device's zone, unless it is still the factory UTC and this browser's zone is in the list
function suggestZone(d) {
  let browser = '';
  try { browser = Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch (_) { /* old browser */ }
  const zones = dev.zones || [];
  const cur = d && d.timezone;
  if ((!cur || /^(Etc\/)?(UTC|GMT)$/.test(cur)) && zones.some((z) => z.id === browser)) return browser;
  return cur || browser;
}

function wizClock() {
  clearTimeout(wiz.clockTimer);
  const d = state.device;
  if (!d || !d.time || wiz.cur !== 'time') { $('sp-clock').textContent = ''; return; }
  const base = new Date(d.time.now);
  if (isNaN(base)) return;
  if (wiz.clockFrom !== d.time.now) { wiz.clockFrom = d.time.now; wiz.clockAt = Date.now(); }
  const now = new Date(base.getTime() + (Date.now() - wiz.clockAt));
  let txt;
  try {
    txt = now.toLocaleString(getLocale(), { dateStyle: 'medium', timeStyle: 'medium', hourCycle: 'h23', timeZone: $('sp-tz').value || d.timezone || undefined });
  } catch (_) {
    txt = fmtDateTime(now);
  }
  const how = !d.time.ntp ? t('device.clockManual') : d.time.synced ? t('device.clockSynced') : t('device.clockNotSynced');
  $('sp-clock').textContent = t('device.clock', { time: txt, how });
  wiz.clockTimer = setTimeout(wizClock, 1000);
}

$('sp-tz').addEventListener('change', wizClock);
$('sp-ntp').addEventListener('change', () => { $('sp-time-row').hidden = $('sp-ntp').checked; });
$('sp-time').addEventListener('input', () => { $('sp-time').dataset.touched = '1'; });
$('sp-time-now').addEventListener('click', () => {
  $('sp-time').value = localInputValue(new Date());
  $('sp-time').dataset.touched = '1';
});

// Network: the dashboard's Wi-Fi card, fed by /status every 3 s, and what goes out to the internet now
async function wizNetTick() {
  clearTimeout(wiz.stepTimer);
  if (wiz.cur !== 'network') return;
  await refreshStatus();
  const st = state.status;
  const ups = (st && st.uplinks) || [];
  const usable = ups.filter((u) => !u.disabled && !u.conflict && !u.noInternet);
  $('sp-net-status').textContent = usable.length
    ? t('setup.netUplinks', { list: usable.map((u) => `${kindLabel(u.kind)} (${u.name} · ${u.ip})`).join(', ') })
    : t('setup.netNone');
  $('sp-net-status').className = 'setup-status ' + (usable.length ? 'ok' : 'warn');
  $('sp-net-none').hidden = !$('wifi-card').hidden;
  wiz.stepTimer = setTimeout(wizNetTick, 3000);
}

// EnhancedIRL: the dashboard's account card; polled every 3 s (to see «Linked» as soon as it is confirmed)
async function wizCloudTick() {
  clearTimeout(wiz.stepTimer);
  if (wiz.cur !== 'cloud') return;
  try {
    renderCloud(await api('GET', '/cloud?qr=rows'));
    $('sp-cloud-none').hidden = true;
  } catch (e) {
    if (e.status === 503) { $('cloud-card').hidden = true; $('sp-cloud-none').hidden = false; return; }
  }
  wiz.stepTimer = setTimeout(wizCloudTick, 3000);
}

// The network name the device will take: the same rule as the app (lowercase ASCII, digits and -)
function hostPreview(name) {
  let h = name.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  if (!h || !/^[a-z]/.test(h)) h = ('eirl-' + h).replace(/-+$/, '').slice(0, 40);
  return h;
}

function renderNameHint() {
  const name = cleanName($('sp-name').value);
  $('sp-name-hint').textContent = name ? t('device.nameHint', { host: `${hostPreview(name)}.local` }) : '';
}
$('sp-name').addEventListener('input', renderNameHint);

function renderSummary() {
  const d = state.device || {};
  const items = [
    [t('setup.step.language'), LANG_NAMES[getLang()]],
    [t('device.timezone'), d.timezone || '—'],
    [t('setup.step.name'), d.name ? `${d.name} · ${d.hostname}.local` : '—'],
  ];
  const ups = ((state.status && state.status.uplinks) || []).filter((u) => !u.disabled && !u.conflict && !u.noInternet);
  items.push([t('setup.step.network'), stepState('network') === 'skipped' && !ups.length ? t('setup.skipped')
    : ups.length ? ups.map((u) => kindLabel(u.kind)).join(', ') : t('setup.netNoneShort')]);
  const c = state.cloud;
  items.push([t('setup.step.cloud'), c && c.state === 'linked' ? t('setup.linkedTo', { who: c.accountLabel || t('cloud.yourAccount') })
    : stepState('cloud') === 'skipped' ? t('setup.skipped') : t('cloud.unlinked')]);
  $('sp-summary').replaceChildren(...items.map(([k, v]) => {
    const li = document.createElement('li');
    const a = document.createElement('span');
    a.className = 'k';
    a.textContent = k;
    const b = document.createElement('span');
    b.textContent = v;
    li.append(a, b);
    return li;
  }));
}

async function markStep(id, st) {
  try {
    const r = await api('POST', '/setup/step', { id, state: st }, { quiet: true });
    if (r && r.steps) wiz.setup = { ...wiz.setup, steps: r.steps };
    else wiz.setup.steps = (wiz.setup.steps || []).map((x) => (x.id === id ? { ...x, state: st } : x));
  } catch (_) {
    wiz.setup.steps = (wiz.setup.steps || []).map((x) => (x.id === id ? { ...x, state: st } : x));
  }
}

function wizError(text) {
  $('setup-err').textContent = text;
  $('setup-err').hidden = !text;
}

function fieldErr(panel, key, text) {
  const p = panel.querySelector(`.err[data-for="${CSS.escape(key)}"]`);
  if (!p) return false;
  p.textContent = text;
  p.hidden = false;
  return true;
}

// Saves the current step. true: go on; false: stay (an error is shown)
async function doStep(id) {
  const panel = document.querySelector(`#setup-form .setup-panel[data-step="${id}"]`);
  if (id === 'language') {
    const r = $('sp-langs').querySelector('input:checked');
    const lang = (r && r.value) || getLang();
    try {
      state.device = await api('PUT', '/device', { language: lang }, { quiet: true });
    } catch (e) {
      if (e.status !== 404) { wizError(e.message); return false; }
    }
    setLangOverride('');
    setLang(lang);
    return true;
  }
  if (id === 'password') {
    if (!wiz.setup.needsPassword) return true;
    const pw = $('sp-pw').value;
    const pw2 = $('sp-pw2').value;
    const code = $('sp-code').value.toUpperCase().replace(/[\s-]/g, '');
    let bad = false;
    if ([...pw].length < 10) bad = fieldErr(panel, 'password', t('setup.pwShort')) || true;
    else if (pw !== pw2) bad = fieldErr(panel, 'password2', t('setup.pwMismatch')) || true;
    if (wiz.setup.codeRequired !== false && !code) bad = fieldErr(panel, 'code', t('setup.codeEmpty')) || true;
    if (bad) return false;
    const body = { password: pw };
    if (wiz.setup.codeRequired !== false) body.code = code;
    try {
      await api('POST', '/setup/password', body, { quiet: true });
    } catch (e) {
      if (e.status === 409) { // set on the screen meanwhile: sign in with it
        leaveWizard();
        showLogin();
        toast(t('setup.passwordElsewhere'));
        return false;
      }
      const fields = e.data && e.data.fields;
      let shown = false;
      if (e.status === 400 && fields) for (const [k, msg] of Object.entries(fields)) shown = fieldErr(panel, k, msg) || shown;
      if (e.data && e.data.error === 'bad_code') shown = fieldErr(panel, 'code', e.message); // 403
      if (!shown) wizError(e.message); // 429 rate_limited, 503 setup_code_unavailable…
      return false;
    }
    // Never kept in the page once used
    $('sp-pw').value = '';
    $('sp-pw2').value = '';
    $('sp-code').value = '';
    wiz.authed = true;
    wiz.setup.needsPassword = false;
    return true;
  }
  if (id === 'time') {
    const d = state.device;
    if (!d) return true;
    const body = deviceChanges(d, d.name || '', $('sp-tz').value, $('sp-ntp').checked, $('sp-time').value, !!$('sp-time').dataset.touched);
    delete body.name;
    if (!Object.keys(body).length) return true;
    try {
      state.device = await api('PUT', '/device', body);
    } catch (e) {
      deviceErrors(e, panel, 'setup-err');
      return false;
    }
    return true;
  }
  if (id === 'name') {
    const d = state.device || {};
    const name = cleanName($('sp-name').value);
    if (!name) { fieldErr(panel, 'name', t('device.nameEmpty')); return false; }
    if (name === d.name) return true;
    let nd;
    try {
      nd = await api('PUT', '/device', { name });
    } catch (e) {
      deviceErrors(e, panel, 'setup-err');
      return false;
    }
    state.device = nd;
    hostMoved(d.hostname, nd.hostname, $('sp-moved'));
    // Opened by the old network name: that address goes away. Stay here with the link to the new one
    if (!$('sp-moved').hidden) {
      await markStep('name', 'done');
      return false;
    }
    return true;
  }
  return true; // network, cloud: nothing to save
}

$('setup-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  // A form of a borrowed card (the Wi-Fi password of the network step) submits itself: it is not this step's Next
  if (ev.target !== ev.currentTarget) return;
  if (wiz.busy || !wiz.cur) return;
  wiz.busy = true;
  wizError('');
  for (const p of $('setup-form').querySelectorAll('.err[data-for]')) p.hidden = true;
  const btns = ['setup-next', 'setup-back', 'setup-skip'].map($);
  for (const b of btns) b.disabled = true;
  const id = wiz.cur;
  try {
    if (id === 'done') {
      try {
        await api('POST', '/setup/complete');
      } catch (e) {
        wizError(e.message);
        return;
      }
      leaveWizard();
      startApp();
      toast(t('setup.completed'));
      return;
    }
    if (!(await doStep(id))) return;
    if (!wiz.cur) return; // left the wizard (password set elsewhere)
    await markStep(id, 'done');
    goStep(wiz.order[wiz.order.indexOf(id) + 1]);
  } finally {
    wiz.busy = false;
    for (const b of btns) b.disabled = false;
  }
});

$('setup-skip').addEventListener('click', async () => {
  const id = wiz.cur;
  if (wiz.busy || !WIZ_SKIPPABLE.includes(id)) return;
  wiz.busy = true;
  try {
    await markStep(id, 'skipped');
    goStep(wiz.order[wiz.order.indexOf(id) + 1]);
  } finally {
    wiz.busy = false;
  }
});

$('setup-back').addEventListener('click', () => {
  const i = wiz.order.indexOf(wiz.cur);
  if (i > 0 && !wiz.busy) goStep(wiz.order[i - 1]);
});

// Signed out meanwhile (a 401 sends to the login, through stopLive): the borrowed cards go back home
hooks.stop.push(() => { if (wiz.cur && $('setup').hidden) leaveWizard(); });

onLangChange(() => {
  if (!wiz.cur) return;
  renderStepTexts();
  if (wiz.cur === 'name') renderNameHint();
  if (wiz.cur === 'done') renderSummary();
});
