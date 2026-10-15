// Settings cards of the dashboard that came with the device API (setup batch, 2026-10): source slots
// (GET/PUT /sources/slots), updates (GET /update, POST /update/check|install) and the device (GET/PUT
// /device: language, name, time zone and clock). Loaded after app.js: it uses its helpers (api, t,
// toast, option, state, hooks).
'use strict';

// --- Source slots (slots.json) ---------------------------------------------------------------
// Each mixer slot is «auto» (the next connected source, HDMI first), «off» or pinned to a source id, and
// has an alias (0-24 characters; empty = the device's name). The form is not repainted while edited.

// A typed name as the device stores it: NFC, trimmed and with typographic apostrophes as ' (phone keyboards
// type ’, and the alias / device name rules only allow ')
const cleanName = (v) => v.normalize('NFC').replace(/[\u2018\u2019]/g, "'").trim();

const slots = { data: null, dirty: false, key: '', timer: 0, busy: false };
// The kind next to a candidate's name: only where the name does not say it (the board HDMI and the test pattern do)
const slotKind = (k) => (k === 'usb' ? 'USB' : k === 'network' ? t('slots.kindNetwork') : '');

async function loadSlots() {
  clearTimeout(slots.timer);
  let d;
  try {
    d = await api('GET', '/sources/slots', undefined, { quiet: true });
  } catch (e) {
    // An app without the route (404) or without the engine's catalogue yet: no card
    if (e.status === 404 || e.status === 503) $('slots-card').hidden = true;
    return;
  }
  slots.data = d;
  $('slots-card').hidden = false;
  $('slots-old').hidden = d.engineSupports !== false;
  if (!slots.dirty && !$('slots-form').contains(document.activeElement)) renderSlots();
}

// The engine changed something (a source plugged in, the screen saved the slots): reload, a bit later
// and only once for a burst of mixer events
function slotsFromMixer(m) {
  const key = m ? JSON.stringify([m.slotConfig || null, m.sources.map((s) => [s.slot, s.id, s.state, s.alias])]) : '';
  if (key === slots.key) return;
  slots.key = key;
  clearTimeout(slots.timer);
  slots.timer = setTimeout(loadSlots, 800);
}

function slotLabel(c) {
  const kind = slotKind(c.kind);
  const base = fixedName(c.id, c.name) || c.id;
  const name = kind ? `${base} · ${kind}` : base;
  return c.connected === false ? t('common.notConnected', { name }) : name;
}

function renderSlots() {
  const d = slots.data;
  if (!d) return;
  const box = $('slots-rows');
  box.replaceChildren(...d.slots.map((s) => slotRow(s, d.candidates)));
  for (const p of $('slots-form').querySelectorAll('.err')) p.hidden = true;
  $('slots-cancel').hidden = true;
  slots.dirty = false;
}

function slotRow(s, candidates) {
  const n = s.slot;
  const row = document.createElement('div');
  row.className = 'slot-row';
  row.dataset.slot = n;
  row.setAttribute('role', 'group');
  row.setAttribute('aria-labelledby', `slot-${n}-title`);
  const legend = document.createElement('h4');
  legend.id = `slot-${n}-title`;
  legend.className = 'slot-title';
  legend.textContent = t('slots.row', { n });
  const fields = document.createElement('div');
  fields.className = 'slot-fields';

  const selWrap = document.createElement('div');
  const selL = document.createElement('label');
  selL.htmlFor = `slot-${n}-assign`;
  selL.textContent = t('slots.assign');
  const sel = document.createElement('select');
  sel.id = `slot-${n}-assign`;
  sel.name = `slots.${n}.assign`;
  const opts = [option('auto', t('slots.auto'), s.assign === 'auto'), option('off', t('slots.off'), s.assign === 'off')];
  const known = candidates.some((c) => c.id === s.assign);
  for (const c of candidates) opts.push(option(c.id, slotLabel(c), c.id === s.assign));
  // Pinned to something the app no longer lists: keep it, marked
  if (!known && s.assign !== 'auto' && s.assign !== 'off') opts.push(option(s.assign, t('common.notConnected', { name: fixedName(s.assign, s.assign) }), true));
  sel.replaceChildren(...opts);
  const selErr = document.createElement('p');
  selErr.className = 'err';
  selErr.dataset.for = `slots.${n}.assign`;
  selErr.hidden = true;
  selWrap.append(selL, sel, selErr);

  const aliasWrap = document.createElement('div');
  const aliasL = document.createElement('label');
  aliasL.htmlFor = `slot-${n}-alias`;
  aliasL.textContent = t('slots.alias');
  const alias = document.createElement('input');
  alias.type = 'text';
  alias.id = `slot-${n}-alias`;
  alias.name = `slots.${n}.alias`;
  alias.maxLength = 24;
  alias.autocomplete = 'off';
  alias.value = s.alias || '';
  const aliasErr = document.createElement('p');
  aliasErr.className = 'err';
  aliasErr.dataset.for = `slots.${n}.alias`;
  aliasErr.hidden = true;
  aliasWrap.append(aliasL, alias, aliasErr);
  fields.append(selWrap, aliasWrap);

  const now = document.createElement('p');
  now.className = 'hint slot-now';
  row.append(legend, fields, now);

  const sync = () => {
    // The alias placeholder is the name that shows without one: the pinned source's, or the current one's
    const pick = candidates.find((c) => c.id === sel.value);
    alias.placeholder = pick ? fixedName(pick.id, pick.name) : sel.value === 'auto' && s.current ? fixedName(s.current.id, s.current.name) : t('slots.noAlias');
    now.textContent = slotNow(s, sel.value);
  };
  sync();
  sel.addEventListener('change', sync);
  return row;
}

// What the slot holds now (from the engine), or what will happen with the new choice
function slotNow(s, assign) {
  if (assign !== s.assign) return t('slots.pending');
  const c = s.current;
  if (c) {
    const st = c.state === 'live' ? t('slots.live') : MX_STATES.includes(c.state) ? t('mxstate.' + c.state) : c.state;
    return t('slots.now', { what: `${fixedName(c.id, c.name)} · ${st}` });
  }
  if (assign === 'off') return t('slots.nowOff');
  if (assign === 'auto') return t('slots.nowFree');
  return t('slots.nowReserved');
}

$('slots-form').addEventListener('input', () => {
  slots.dirty = true;
  $('slots-cancel').hidden = false;
  $('slots-err').hidden = true;
});
$('slots-form').addEventListener('change', () => {
  slots.dirty = true;
  $('slots-cancel').hidden = false;
});
$('slots-cancel').addEventListener('click', () => {
  renderSlots();
  loadSlots();
});

function slotFieldError(key, text) {
  // slots.<n>.<field>, n = the slot number (1-based)
  const p = $('slots-form').querySelector(`.err[data-for="${CSS.escape(key)}"]`);
  if (!p) return false;
  p.textContent = text;
  p.hidden = false;
  const input = $('slots-form').querySelector(`[name="${CSS.escape(key)}"]`);
  if (input) input.classList.add('invalid');
  return true;
}

$('slots-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  if (slots.busy || !slots.data) return;
  const form = $('slots-form');
  for (const p of form.querySelectorAll('.err')) p.hidden = true;
  for (const i of form.querySelectorAll('.invalid')) i.classList.remove('invalid');
  const body = { slots: slots.data.slots.map((s) => ({
    slot: s.slot,
    assign: $(`slot-${s.slot}-assign`).value,
    alias: cleanName($(`slot-${s.slot}-alias`).value),
  })) };
  // The same source pinned twice: say it here (the app answers the same with code «duplicate»)
  const seen = new Map();
  let bad = false;
  for (const s of body.slots) {
    if (s.assign === 'auto' || s.assign === 'off') continue;
    if (seen.has(s.assign)) bad = slotFieldError(`slots.${s.slot}.assign`, t('slots.duplicate', { n: seen.get(s.assign) })) || bad;
    else seen.set(s.assign, s.slot);
  }
  if (bad) return;
  slots.busy = true;
  const btn = form.querySelector('button[type=submit]');
  btn.disabled = true;
  try {
    const d = await api('PUT', '/sources/slots', body);
    slots.data = d;
    $('slots-old').hidden = d.engineSupports !== false;
    renderSlots();
    // applied: false = saved, but the engine is stopped or did not answer: it reads the file when it starts
    toast(d.engineSupports === false ? t('slots.savedLater') : d.applied === false ? t('slots.savedNotApplied') : t('slots.saved'));
  } catch (e) {
    const fields = e.data && e.data.fields;
    let shown = false;
    if (e.status === 400 && fields) {
      for (const [k, msg] of Object.entries(fields)) shown = slotFieldError(k, msg) || shown;
    }
    if (!shown) {
      // on_air (moving the source on air while streaming), validation without fields, network…
      $('slots-err').textContent = e.message;
      $('slots-err').hidden = false;
    }
  } finally {
    slots.busy = false;
    btn.disabled = false;
  }
});

hooks.start.push(loadSlots);
hooks.mixer.push(slotsFromMixer);
onLangChange(() => { if (!slots.dirty) renderSlots(); });

// --- Camera image (SOURCES.md §7) -----------------------------------------------------------
// The settings each USB camera has (sources[].controls of the mixer state), changed hot with camera_control and
// camera_reset (POST /mixer). The controls are built again only when the camera or its list changes (the state comes
// several times a second); otherwise only their values, never one being dragged or with its command in flight.

const cam = { sel: '', pickKey: '', key: '' };

// The value as it is shown: exposure in 100 µs (UVC) as ms, the white balance in K, pan and tilt in arc seconds as degrees
function cameraValue(c, v) {
  if (c.inactive) return t('camera.auto');
  switch (c.id) {
    case 'exposure': return t('camera.ms', { v: fmtNum(v / 10, 1) });
    case 'wb_temperature': return t('camera.kelvin', { v: fmtNum(v, 0) });
    case 'pan': case 'tilt': {
      const d = v / 3600;
      return t('camera.degrees', { v: fmtNum(d, Number.isInteger(d) ? 0 : 1) });
    }
  }
  return fmtNum(v, 0);
}

// The sources with settings now (the engine decides: also one that is not live)
const cameraSources = (m) => (m ? m.sources.filter((s) => s.controls && s.controls.length) : []);

function cameraFromMixer(m) {
  const list = cameraSources(m);
  $('camera-card').hidden = !list.length;
  if (!list.length) {
    // No state (the tab was hidden, the engine restarts) or no camera now: built again when they come back
    cam.key = cam.pickKey = '';
    return;
  }
  // The chosen camera while it is there (a gap without state does not forget it); otherwise the first one
  if (!list.some((s) => s.id === cam.sel)) cam.sel = list[0].id;
  const src = list.find((s) => s.id === cam.sel);
  renderCameraPick(list);
  $('camera-src').textContent = list.length === 1 ? srcName(src) : '';
  const key = JSON.stringify([src.id, src.controls.map((c) => [c.id, c.group, c.type, c.min, c.max, c.step, c.default, c.options || null])]);
  if (key !== cam.key) {
    cam.key = key;
    buildCamera(src);
  }
  for (const c of src.controls) updateCameraRow(c);
}

function renderCameraPick(list) {
  const box = $('camera-pick');
  box.hidden = list.length < 2;
  const key = JSON.stringify(list.map((s) => [s.id, s.slot, srcName(s)]));
  if (key !== cam.pickKey) {
    cam.pickKey = key;
    box.replaceChildren(...list.map((s) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'btn small';
      b.dataset.id = s.id;
      const span = document.createElement('span');
      span.textContent = `${s.slot} · ${srcName(s)}`;
      b.append(span);
      b.title = span.textContent;
      b.addEventListener('click', () => {
        if (cam.sel === s.id) return;
        cam.sel = s.id;
        $('camera-err').hidden = true;
        cameraFromMixer(state.mixer);
      });
      return b;
    }));
  }
  for (const b of box.children) {
    const on = b.dataset.id === cam.sel;
    b.classList.toggle('active', on);
    b.setAttribute('aria-pressed', String(on));
  }
}

// One group per kind of setting, in the order they come
function buildCamera(src) {
  const groups = new Map();
  for (const c of src.controls) {
    let g = groups.get(c.group);
    if (!g) {
      g = document.createElement('div');
      g.className = 'cam-group';
      g.setAttribute('role', 'group');
      g.setAttribute('aria-labelledby', `cam-g-${c.group}`);
      const h = document.createElement('h4');
      h.id = `cam-g-${c.group}`;
      h.className = 'sub-title';
      h.textContent = t('camera.group.' + c.group);
      g.append(h);
      groups.set(c.group, g);
    }
    g.append(cameraRow(src.id, c));
  }
  $('camera-ctls').replaceChildren(...groups.values());
}

function cameraRow(source, c) {
  const id = `cam-${c.id}`;
  const label = t('camera.ctl.' + c.id);
  const row = document.createElement('div');
  row.dataset.ctl = c.id;
  if (c.type === 'bool') {
    // The web's switch: the label wraps the box
    row.className = 'cam-row';
    row.innerHTML = '<label class="mx-follow"><input type="checkbox" role="switch"> <span></span></label>';
    const cb = row.querySelector('input');
    cb.id = id;
    row.querySelector('span').textContent = label;
    cb.addEventListener('change', async () => {
      cb.dataset.busy = '1';
      await cameraCmd({ cmd: 'camera_control', source, control: c.id, value: cb.checked ? 1 : 0 });
      delete cb.dataset.busy;
      rerenderCamera(); // if the camera refused it, back to what it says
    });
  } else if (c.type === 'menu') {
    row.className = 'cam-row cam-menu';
    const name = document.createElement('span');
    name.id = `${id}-label`;
    name.className = 'cam-name';
    name.textContent = label;
    const seg = document.createElement('span');
    seg.className = 'seg';
    seg.setAttribute('role', 'group');
    seg.setAttribute('aria-labelledby', name.id);
    for (const v of c.options) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'btn small';
      b.dataset.value = v;
      b.textContent = c.id === 'power_line' && v >= 0 && v <= 3 ? t('camera.powerLine.' + v) : String(v);
      b.addEventListener('click', async () => {
        if (seg.dataset.busy || b.classList.contains('active')) return;
        seg.dataset.busy = '1';
        await cameraCmd({ cmd: 'camera_control', source, control: c.id, value: v });
        delete seg.dataset.busy;
        rerenderCamera();
      });
      seg.append(b);
    }
    row.append(name, seg);
  } else {
    // A slider: the value shows while dragging and goes on release (change). The mark on the track is the factory value
    row.className = 'cam-row cam-int';
    row.innerHTML = '<label></label><output aria-hidden="true"></output><input type="range">';
    const lab = row.querySelector('label');
    lab.htmlFor = id;
    lab.textContent = label;
    const inp = row.querySelector('input');
    Object.assign(inp, { id, min: c.min, max: c.max, step: c.step });
    const span = c.max - c.min;
    if (span > 0) inp.style.setProperty('--zero', String((c.default - c.min) / span));
    else inp.style.background = 'none';
    inp.addEventListener('input', () => {
      inp.dataset.held = '1';
      inp.dataset.dirty = '1';
      // c is the control as the row was built: its automatic may have been turned off since (a slider that moves is not inactive)
      showCameraValue(row, { ...c, inactive: false }, Number(inp.value));
    });
    // change does not come if released on the starting value: pointerup and blur end the drag too
    const release = () => {
      if (!inp.dataset.held) return;
      delete inp.dataset.held;
      sendCameraValue(inp, source, c);
    };
    for (const ev of ['change', 'pointerup', 'pointercancel', 'blur']) inp.addEventListener(ev, release);
  }
  return row;
}

function showCameraValue(row, c, v) {
  const inp = row.querySelector('input');
  const text = cameraValue(c, v);
  row.querySelector('output').textContent = text;
  inp.setAttribute('aria-valuetext', text);
  inp.style.setProperty('--p', String(c.max > c.min ? (v - c.min) / (c.max - c.min) : 0));
}

async function sendCameraValue(inp, source, c) {
  if (!inp.dataset.dirty || inp.dataset.busy) return;
  delete inp.dataset.dirty;
  const value = Number(inp.value);
  const cur = cameraControlNow(source, c.id);
  if (!cur || cur.value !== value) {
    inp.dataset.busy = '1';
    await cameraCmd({ cmd: 'camera_control', source, control: c.id, value });
    delete inp.dataset.busy;
    if (inp.dataset.dirty && !inp.dataset.held) { sendCameraValue(inp, source, c); return; } // moved again meanwhile
  }
  rerenderCamera();
}

function cameraControlNow(source, id) {
  const s = cameraSources(state.mixer).find((x) => x.id === source);
  return s && s.controls.find((x) => x.id === id);
}

// The values and the automatics from the state, except on a control the user holds or whose command is going
function updateCameraRow(c) {
  const row = $('camera-ctls').querySelector(`[data-ctl="${CSS.escape(c.id)}"]`);
  if (!row) return;
  if (c.type === 'bool') {
    const cb = row.querySelector('input');
    if (!cb.dataset.busy) cb.checked = c.value === 1;
    cb.disabled = !!c.inactive;
  } else if (c.type === 'menu') {
    const seg = row.querySelector('.seg');
    if (seg.dataset.busy) return;
    for (const b of seg.children) {
      const on = Number(b.dataset.value) === c.value;
      b.classList.toggle('active', on);
      b.setAttribute('aria-pressed', String(on));
      b.disabled = !!c.inactive;
    }
  } else {
    const inp = row.querySelector('input');
    inp.disabled = !!c.inactive;
    row.classList.toggle('inactive', !!c.inactive);
    if (inp.dataset.held || inp.dataset.dirty || inp.dataset.busy) return;
    inp.value = String(c.value);
    showCameraValue(row, c, c.value);
  }
}

const rerenderCamera = () => cameraFromMixer(state.mixer);

// Errors go under the controls (not in a toast): what the camera refused stays next to it
async function cameraCmd(body) {
  const err = $('camera-err');
  err.hidden = true;
  try {
    const r = await api('POST', '/mixer', body);
    if (r && r.state) renderMixer(r.state);
    return true;
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
    return false;
  }
}

$('camera-reset').addEventListener('click', async () => {
  const src = cameraSources(state.mixer).find((s) => s.id === cam.sel);
  if (!src || !confirm(t('camera.resetConfirm', { name: srcName(src) }))) return;
  const btn = $('camera-reset');
  btn.disabled = true;
  if (await cameraCmd({ cmd: 'camera_reset', source: src.id })) toast(t('camera.resetDone'));
  btn.disabled = false;
});

hooks.mixer.push(cameraFromMixer);
onLangChange(() => {
  cam.key = cam.pickKey = '';
  rerenderCamera();
});

// --- Updates (OTA) ---------------------------------------------------------------------------
// The app checks by itself (every ~6 h) and downloads; installing is only on a button press, never while
// streaming, and a version that does not come up healthy is rolled back. Polled every 2 s while something
// is going on, every minute otherwise (only with the dashboard live). While installing, the app restarts:
// failed requests are expected, and the page reloads itself when the new UI answers (staleWeb).

const upd = { data: null, timer: 0, installing: false, dismissed: '', asked: false };
const UPD_BUSY = ['checking', 'downloading', 'installing'];
const UPD_KEY = 'eirl.updating'; // sessionStorage: the version being installed, to say how it went after the reload

const verOf = (v) => (v && typeof v === 'object' ? v.release || v.app || '' : v || '');
// Release notes: the active language, else English, else whatever there is
function pickNotes(n) {
  if (!n) return '';
  if (typeof n === 'string') return n;
  return n[getLang()] || n.en || Object.values(n).find((x) => typeof x === 'string') || '';
}
const fmtSize = (b) => (b > 0 ? t('unit.mb', { v: fmtNum(b / 1e6, 1) }) : '');
const sessGet = (k) => { try { return sessionStorage.getItem(k) || ''; } catch (_) { return ''; } };
const sessSet = (k, v) => { try { if (v) sessionStorage.setItem(k, v); else sessionStorage.removeItem(k); } catch (_) { /* no storage */ } };

// Streaming now: the same rule as the Go live button (with the mixer, its output mode)
function emittingNow() {
  const st = state.status;
  if (!st) return false;
  const running = st.service.state === 'active' || st.service.state === 'activating';
  const mo = state.mixer && state.mixer.output;
  return running && (mo ? mo.mode : st.output) === 'on';
}

async function loadUpdate() {
  clearTimeout(upd.timer);
  try {
    renderUpdate(await api('GET', '/update', undefined, { quiet: true }));
  } catch (e) {
    // An app without updates (404) or a build without the updater (503 update_unavailable): no card
    if ((e.status === 404 || e.status === 503) && !upd.installing) { $('update-card').hidden = true; $('update-banner').hidden = true; return; }
    // Installing: the app is restarting; keep the card as it is and keep asking
  }
  if (!state.live && !upd.installing) return;
  const busy = upd.installing || (upd.data && UPD_BUSY.includes(upd.data.state));
  upd.timer = setTimeout(loadUpdate, busy ? 2000 : 60000);
}

function renderUpdate(u) {
  const prev = upd.data;
  upd.data = u;
  $('update-card').hidden = false;
  const cur = verOf(u.current) || verOf(state.device && state.device.version);
  const av = u.available;
  const avVer = av ? verOf(av.version) : '';
  // After an install (and the reload that comes with it): how it went. lastResult is the root helper's last
  // run (state installed | rolled_back | failed | refused); without it, the versions say it
  const target = sessGet(UPD_KEY);
  if (target && !UPD_BUSY.includes(u.state)) {
    sessSet(UPD_KEY, '');
    const lr = u.lastResult;
    if (u.state === 'rolled_back' || (lr && ['rolled_back', 'failed', 'refused'].includes(lr.state))) {
      toast(t('update.rolledBackToast') + (lr && lr.message ? ' ' + lr.message : ''), true);
    } else if (cur === target || (lr && lr.state === 'installed')) {
      toast(t('update.doneToast', { v: cur }));
    }
  }
  if (u.state !== 'installing') upd.installing = false;
  // An install just ended (normally the page reloads first): the versions of /device changed too
  if (prev && prev.state === 'installing' && u.state !== 'installing') loadDevice();

  $('update-state').textContent = t('update.state.' + (UPDATE_STATES.includes(u.state) ? u.state : 'idle'));
  $('update-state').className = 'sub' + (u.state === 'error' || u.state === 'rolled_back' ? ' warn' : ['available', 'ready'].includes(u.state) ? ' ok' : '');
  renderVersions(cur, u.versions);
  $('update-test').hidden = !u.testBuild;

  const showAv = !!av && ['available', 'downloading', 'ready', 'installing'].includes(u.state);
  $('update-avail').hidden = !showAv;
  if (showAv) {
    const size = fmtSize(av.size);
    $('update-avail-title').textContent = size ? t('update.availableSize', { v: avVer, size }) : t('update.available', { v: avVer });
    const notes = pickNotes(av.notes);
    $('update-notes').textContent = notes;
    $('update-notes').hidden = !notes;
  }

  const bar = $('update-progress');
  bar.hidden = !['checking', 'downloading', 'installing'].includes(u.state);
  if (u.state === 'downloading' && typeof u.progress === 'number') bar.value = Math.max(0, Math.min(1, u.progress));
  else bar.removeAttribute('value'); // indeterminate

  const last = u.lastCheck ? new Date(u.lastCheck) : null;
  const when = last && !isNaN(last) ? fmtRelative(last) : '';
  const msgs = {
    idle: when ? t('update.upToDate', { when }) : t('update.never'),
    checking: t('update.checking'),
    available: t('update.availableMsg'),
    downloading: t('update.downloading', { pct: Math.round((u.progress || 0) * 100) }),
    ready: t('update.ready'),
    installing: t('update.installing'),
    rolled_back: when ? t('update.lastCheck', { when }) : '',
    error: when ? t('update.lastCheck', { when }) : '',
  };
  $('update-msg').textContent = msgs[u.state] || '';
  // message: the server's text for the error code, in the request's language; error: the stable code
  const detail = u.message || u.error;
  const err = $('update-err');
  err.textContent = u.state === 'rolled_back' ? t('update.rolledBack') + (detail ? ' ' + t('update.detail', { detail }) : '')
    : u.state === 'error' ? t('update.error', { detail: detail || t('common.unknownError') }) : '';
  err.hidden = !err.textContent;

  // Install: with a version ready (or available: the app answers 409 not_ready if it is not downloaded yet)
  const inst = $('update-install');
  const canInstall = !!av && ['available', 'ready'].includes(u.state);
  inst.hidden = !canInstall;
  inst.textContent = t('update.install', { v: avVer });
  renderUpdateGate();
  $('update-check').disabled = UPD_BUSY.includes(u.state);

  // Banner: a new version, once per version and session unless dismissed
  const show = canInstall || u.state === 'downloading';
  $('update-banner').hidden = !show || upd.dismissed === avVer;
  if (show) $('update-banner-txt').textContent = t('update.banner', { v: avVer });
  // «Check now» found nothing new: say so (an automatic check, silently)
  if (upd.asked && prev && UPD_BUSY.includes(prev.state) && u.state === 'idle') toast(t('update.upToDateToast'));
  if (!UPD_BUSY.includes(u.state)) upd.asked = false;
}

// Never while streaming: the button is off and says why (follows every status and mixer change)
function renderUpdateGate() {
  const u = upd.data;
  if (!u) return;
  const shown = !!u.available && ['available', 'ready'].includes(u.state);
  // The app says whether it would install now (canInstall, blockedMessage); what this page sees streaming
  // counts too, as it is fresher than the last poll
  const streaming = emittingNow() || u.streaming === true;
  const serverOk = typeof u.canInstall === 'boolean' ? u.canInstall : true;
  $('update-install').disabled = !shown || streaming || !serverOk;
  const why = !shown ? '' : streaming ? t('update.streaming') : !serverOk ? (u.blockedMessage || '') : '';
  $('update-why').hidden = !why;
  $('update-why').textContent = why;
}

const UPDATE_STATES = ['idle', 'checking', 'available', 'downloading', 'ready', 'installing', 'rolled_back', 'error'];

// versions: GET /update's (release, app, engine, commit), else GET /device's
function renderVersions(cur, versions) {
  const d = state.device || {};
  const v = versions || (upd.data && upd.data.versions) || d.version || {};
  const rows = [[t('update.k.version'), cur || v.release || '—']];
  if (v.app && v.app !== cur) rows.push([t('update.k.app'), v.app]);
  if (v.engine && v.engine !== cur) rows.push([t('update.k.engine'), v.engine]);
  if (v.commit) rows.push([t('update.k.commit'), v.commit.slice(0, 10)]);
  if (d.name) rows.push([t('update.k.name'), d.name]);
  if (d.hostname) rows.push([t('update.k.hostname'), `${d.hostname}.local`]);
  const dl = $('update-versions');
  dl.replaceChildren(...rows.flatMap(([k, val]) => {
    const dt = document.createElement('dt'); dt.textContent = k;
    const dd = document.createElement('dd'); dd.textContent = val;
    return [dt, dd];
  }));
}

$('update-check').addEventListener('click', async () => {
  $('update-check').disabled = true;
  try {
    await api('POST', '/update/check');
    upd.asked = true;
    if (upd.data) renderUpdate({ ...upd.data, state: 'checking' });
  } catch (e) {
    toast(e.message, true);
  }
  loadUpdate();
});

$('update-install').addEventListener('click', async () => {
  const u = upd.data;
  if (!u || !u.available) return;
  const v = verOf(u.available.version);
  if (emittingNow()) { toast(t('update.streaming'), true); return; }
  if (!confirm(t('update.confirm', { v }))) return;
  $('update-install').disabled = true;
  try {
    await api('POST', '/update/install');
    upd.installing = true;
    sessSet(UPD_KEY, v);
    renderUpdate({ ...u, state: 'installing' });
  } catch (e) {
    $('update-err').textContent = e.message; // 409 streaming / not_ready, in the request's language
    $('update-err').hidden = false;
    $('update-install').disabled = false;
  }
  loadUpdate();
});

$('update-banner-go').addEventListener('click', () => {
  $('update-card').scrollIntoView({ behavior: 'smooth', block: 'start' });
  $('update-install').focus({ preventScroll: true });
});
$('update-banner-x').addEventListener('click', () => {
  upd.dismissed = upd.data && upd.data.available ? verOf(upd.data.available.version) : '';
  $('update-banner').hidden = true;
});

// --- Device: language, name, time zone and clock --------------------------------------------
// The language applies at once (it is the device's: the touch screen follows it, and this browser drops its
// own choice); the rest is saved with «Save». The form is not repainted while being edited.

const dev = { zones: null, dirty: false, clockTimer: 0 };

async function loadDevice() {
  let d;
  try {
    d = await api('GET', '/device', undefined, { quiet: true });
  } catch (e) {
    if (e.status === 404) $('device-card').hidden = true;
    return;
  }
  state.device = d;
  $('device-card').hidden = false;
  if (!dev.zones) loadZones();
  if (!dev.dirty) renderDevice();
  if (upd.data) renderVersions(verOf(upd.data.current) || verOf(d.version), upd.data.versions);
}

async function loadZones() {
  try {
    const r = await api('GET', '/device/timezones', undefined, { quiet: true });
    dev.zones = r.zones || [];
  } catch (_) {
    dev.zones = null;
    return;
  }
  if (!dev.dirty) renderDevice();
}

const zoneLabel = (z) => `${z.id.replace(/_/g, ' ')} (UTC${z.offset || ''})`;

function fillZones(sel, current) {
  const zones = dev.zones || [];
  const opts = zones.map((z) => option(z.id, zoneLabel(z), z.id === current));
  if (current && !zones.some((z) => z.id === current)) opts.unshift(option(current, current, true));
  sel.replaceChildren(...opts);
  sel.value = current || '';
}

// A date for <input type=datetime-local> in this browser's time
function localInputValue(d) {
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function renderDevice() {
  const d = state.device;
  if (!d) return;
  fillLangSelect($('dev-lang'), false);
  $('dev-lang').value = normLang(d.language) || 'en';
  $('dev-name').value = d.name || '';
  renderHostHint(d.hostname);
  fillZones($('dev-tz'), d.timezone);
  const ntp = !!(d.time && d.time.ntp);
  $('dev-ntp').checked = ntp;
  $('dev-time-row').hidden = ntp;
  if (!ntp) $('dev-time').value = localInputValue(new Date());
  for (const p of $('device-form').querySelectorAll('.err')) p.hidden = true;
  $('dev-cancel').hidden = true;
  dev.dirty = false;
  renderClock();
}

function renderHostHint(host) {
  $('dev-name-hint').textContent = host ? t('device.nameHint', { host: `${host}.local` }) : '';
}

// The device's clock, in its own time zone, ticking from the last reading
function renderClock() {
  clearTimeout(dev.clockTimer);
  const d = state.device;
  if (!d || !d.time || $('device-card').hidden) return;
  const base = new Date(d.time.now);
  if (isNaN(base)) return;
  if (!dev.readAt || dev.readFrom !== d.time.now) { dev.readAt = Date.now(); dev.readFrom = d.time.now; }
  const now = new Date(base.getTime() + (Date.now() - dev.readAt));
  let txt;
  try {
    txt = now.toLocaleString(getLocale(), { dateStyle: 'medium', timeStyle: 'medium', hourCycle: 'h23', timeZone: d.timezone || undefined });
  } catch (_) {
    txt = fmtDateTime(now);
  }
  const how = !d.time.ntp ? t('device.clockManual') : d.time.synced ? t('device.clockSynced') : t('device.clockNotSynced');
  $('dev-clock').textContent = t('device.clock', { time: txt, how });
  if (state.live) dev.clockTimer = setTimeout(renderClock, 1000);
}

function markDeviceDirty() {
  dev.dirty = true;
  $('dev-cancel').hidden = false;
  $('device-err').hidden = true;
}

$('dev-name').addEventListener('input', markDeviceDirty);
$('dev-tz').addEventListener('change', markDeviceDirty);
$('dev-time').addEventListener('input', () => { markDeviceDirty(); $('dev-time').dataset.touched = '1'; });
$('dev-ntp').addEventListener('change', () => {
  markDeviceDirty();
  $('dev-time-row').hidden = $('dev-ntp').checked;
  if (!$('dev-ntp').checked && !$('dev-time').dataset.touched) $('dev-time').value = localInputValue(new Date());
});
$('dev-time-now').addEventListener('click', () => {
  $('dev-time').value = localInputValue(new Date());
  $('dev-time').dataset.touched = '1';
  markDeviceDirty();
});
$('dev-cancel').addEventListener('click', () => { delete $('dev-time').dataset.touched; renderDevice(); });

// Language: the device's, at once. This browser follows it again (its own choice is dropped)
$('dev-lang').addEventListener('change', async () => {
  const sel = $('dev-lang');
  const lang = normLang(sel.value);
  const before = state.device && state.device.language;
  sel.disabled = true;
  try {
    state.device = await api('PUT', '/device', { language: lang });
    setLangOverride('');
    setLang(lang);
    toast(t('device.languageSaved'));
  } catch (e) {
    sel.value = normLang(before) || getLang();
    toast(e.message, true);
  } finally {
    sel.disabled = false;
  }
});

// A field error from PUT /device under its field (language, name, timezone, ntp, time)
function deviceErrors(e, form, generalId) {
  const fields = e.data && e.data.fields;
  let shown = false;
  if (e.status === 400 && fields) {
    for (const [k, msg] of Object.entries(fields)) {
      const p = form.querySelector(`.err[data-for="${CSS.escape(k)}"]`);
      if (!p) continue;
      p.textContent = msg;
      p.hidden = false;
      shown = true;
    }
  }
  if (!shown) {
    $(generalId).textContent = e.message;
    $(generalId).hidden = false;
  }
}

// The body of PUT /device with what changed (time only with ntp off, as the API wants)
function deviceChanges(d, name, tz, ntp, timeVal, timeTouched) {
  const body = {};
  if (name !== (d.name || '')) body.name = name;
  if (tz && tz !== d.timezone) body.timezone = tz;
  const wasNtp = !!(d.time && d.time.ntp);
  if (ntp !== wasNtp) body.ntp = ntp;
  if (!ntp && timeVal && (timeTouched || wasNtp)) {
    const when = new Date(timeVal); // this browser's time zone
    if (!isNaN(when)) { body.ntp = false; body.time = when.toISOString(); }
  }
  return body;
}

// The page was opened by the device's name (<host>.local): after renaming it, that address goes away
function hostMoved(oldHost, newHost, box) {
  const here = location.hostname.toLowerCase();
  box.hidden = true;
  if (!oldHost || !newHost || oldHost === newHost) return;
  if (here !== oldHost && here !== `${oldHost}.local`) return;
  const url = `${location.protocol}//${newHost}.local${location.port ? ':' + location.port : ''}/`;
  const a = document.createElement('a');
  a.href = url;
  a.textContent = url;
  a.className = 'cloud-link';
  box.replaceChildren(document.createTextNode(t('device.hostMoved') + ' '), a);
  box.hidden = false;
}

$('device-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const d = state.device;
  if (!d) return;
  const form = $('device-form');
  for (const p of form.querySelectorAll('.err')) p.hidden = true;
  const name = cleanName($('dev-name').value);
  if (!name) {
    const p = form.querySelector('.err[data-for="name"]');
    p.textContent = t('device.nameEmpty');
    p.hidden = false;
    return;
  }
  const body = deviceChanges(d, name, $('dev-tz').value, $('dev-ntp').checked, $('dev-time').value, !!$('dev-time').dataset.touched);
  if (!Object.keys(body).length) { renderDevice(); return; }
  const btn = form.querySelector('button[type=submit]');
  btn.disabled = true;
  try {
    const nd = await api('PUT', '/device', body);
    const oldHost = d.hostname;
    state.device = nd;
    delete $('dev-time').dataset.touched;
    renderDevice();
    hostMoved(oldHost, nd.hostname, $('dev-moved'));
    if (upd.data) renderVersions(verOf(upd.data.current) || verOf(nd.version));
    toast(t('device.saved'));
  } catch (e) {
    deviceErrors(e, form, 'device-err');
  } finally {
    btn.disabled = false;
  }
});

// Run the setup wizard again (the password is kept)
$('dev-setup-again').addEventListener('click', async () => {
  if (!confirm(t('device.setupAgainConfirm'))) return;
  try {
    await api('POST', '/setup/restart');
    location.reload();
  } catch (e) {
    toast(e.message, true);
  }
});

hooks.start.push(loadDevice);
hooks.live.push(loadUpdate, renderClock);
hooks.stop.push(() => { clearTimeout(upd.timer); clearTimeout(dev.clockTimer); });
hooks.status.push(renderUpdateGate);
hooks.mixer.push(renderUpdateGate);
onLangChange(() => {
  if (upd.data) renderUpdate(upd.data);
  if (state.device) {
    fillLangSelect($('dev-lang'), false);
    renderHostHint(state.device.hostname);
    renderClock();
  }
});
