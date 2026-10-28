// Settings cards of the dashboard that came with the device API (setup batch, 2026-10): source slots
// (GET/PUT /sources/slots), updates (GET /update, POST /update/check|install|reboot) and the device (GET/PUT
// /device: language, name, time zone and clock); later, Bluetooth audio (GET/PUT /bluetooth) and local recording
// (GET/PUT /recording, PUT /record, POST /storage/eject|format). Loaded after app.js: it uses its helpers (api, t,
// toast, option, state, hooks, syncRows, askInline).
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

// --- Bluetooth audio (BLUETOOTH.md §3.5) -----------------------------------------------------
// Headphones or a speaker to listen to the program, a headset's microphone or a phone as an audio input. GET
// /bluetooth while the dashboard is live: every 2 s with the card on screen, every 30 s off it, and every second
// while something is going on (a search, the visible window, an order on a device, switching on or off). An order
// on a device answers at once and runs on: the device shows it (pending), then why it failed (error). Only one
// runs at a time, so meanwhile the other devices' buttons wait. The rows stay in place (an open select would
// close under the finger) and their texts are redone with every state, so a language change only renders again.

const bt = { data: null, timer: 0, gen: 0, onScreen: false, gone: false, busy: 0 };
const BT_KINDS = ['headphones', 'headset', 'speaker', 'phone', 'computer', 'audio'];
const BT_USES = ['listen', 'mic', 'input'];
const BT_ACTS = ['pair', 'connect', 'disconnect', 'forget'];
const BT_FAILS = ['rejected', 'timeout', 'not_reachable', 'gone', 'failed'];
const BT_REASONS = ['no_stack', 'no_adapter', 'no_audio'];
const btKind = (k) => t('bt.kind.' + (BT_KINDS.includes(k) ? k : 'audio'));
const btUse = (u) => (BT_USES.includes(u) ? t('bt.use.' + u) : u);
const btName = (d) => d.name || d.address;
const btDevice = (addr) => ((bt.data && bt.data.devices) || []).find((d) => d.address === addr) || null;
// The visible window's time left, m:ss
const btLeft = (s) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
// Signal of a device found, in words: the dBm mean nothing to most people
const btSignal = (rssi) => t(rssi >= -60 ? 'bt.signal.strong' : rssi >= -75 ? 'bt.signal.medium' : 'bt.signal.weak');

// Something is going on that the card should follow closely
function btActive(s) {
  // on ≠ powered is a switch on its way, unless the adapter refuses it (powerFailed: the app retries every minute)
  const switching = s && s.on !== s.powered && !s.powerFailed;
  return !!s && s.available === true && (s.scanning || s.visible || switching || (s.devices || []).some((d) => d.pending));
}

// gen: a newer call (an order's answer, the card coming on screen) takes over, so only one timer chain lives
async function loadBluetooth() {
  clearTimeout(bt.timer);
  if (bt.gone) return;
  const gen = ++bt.gen;
  let s = null;
  try {
    s = await api('GET', '/bluetooth', undefined, { quiet: true });
  } catch (e) {
    // An older app without Bluetooth (404): no card, and it is not asked again
    if (e.status === 404) { bt.gone = true; $('bt-card').hidden = true; return; }
  }
  if (gen !== bt.gen) return;
  if (s) renderBluetooth(s);
  if (state.live) bt.timer = setTimeout(loadBluetooth, btActive(bt.data) ? 1000 : bt.onScreen ? 2000 : 30000);
}

function renderBluetooth(s) {
  bt.data = s;
  $('bt-card').hidden = false;
  const avail = s.available === true;
  const up = avail && s.on && s.powered;
  // Without Bluetooth, one line that says why (no software names: the reason, in the user's words). Off, the
  // switch and one line; turning on or off, the switch says so. An adapter that refuses to switch on (powerFailed)
  // is not «turning on» for ever: the switch shows what was asked and a warning says why nothing happens
  $('bt-on-row').hidden = !avail;
  const cb = $('bt-on');
  if (!cb.disabled) cb.checked = s.on;
  const settled = s.on === s.powered || s.powerFailed === true;
  $('bt-on-txt').textContent = settled ? t(s.on ? 'bt.on' : 'bt.off') : t(s.on ? 'bt.turningOn' : 'bt.turningOff');
  // Warnings above the controls, which still work: Bluetooth audio down (no_audio: devices can be managed, not
  // heard) and an adapter that does not switch on
  const warn = [];
  if (avail && s.reason === 'no_audio') warn.push(t('bt.reason.no_audio'));
  if (avail && s.on && s.powerFailed === true) warn.push(t('bt.powerFailed'));
  $('bt-warn').textContent = warn.join(' ');
  $('bt-warn').hidden = !warn.length;
  const why = !avail ? t('bt.reason.' + (BT_REASONS.includes(s.reason) ? s.reason : 'no_stack')) : !s.on ? t('bt.offNote') : '';
  $('bt-why').textContent = why;
  $('bt-why').hidden = !why;
  $('bt-body').hidden = !up;
  if (!up) return;

  const devs = s.devices || [];
  const paired = devs.filter((d) => d.paired);
  const found = devs.filter((d) => !d.paired);
  // One order at a time (the app answers busy): while one runs, or is being sent, the others wait
  const lock = bt.busy > 0 || devs.some((d) => d.pending);
  syncRows($('bt-paired'), paired.map((d) => d.address), btPairedRow);
  for (const li of $('bt-paired').children) updatePairedRow(li, paired.find((d) => d.address === li.dataset.id), lock);
  $('bt-paired-none').hidden = paired.length > 0;
  syncRows($('bt-found'), found.map((d) => d.address), btFoundRow);
  for (const li of $('bt-found').children) updateFoundRow(li, found.find((d) => d.address === li.dataset.id), lock);
  const none = $('bt-found-none');
  none.textContent = s.scanning ? t('bt.searchingHint') : found.length ? '' : t('bt.foundNone');
  none.hidden = !none.textContent;

  const scan = $('bt-scan');
  scan.textContent = t(s.scanning ? 'bt.searching' : 'bt.search');
  scan.disabled = s.scanning || lock;
  const vis = $('bt-visible');
  if (!vis.disabled) vis.checked = s.visible;
  const name = s.name || t('common.encoder');
  $('bt-visible-txt').textContent = s.visible ? t('bt.visibleLeft', { left: btLeft(s.visibleLeft || 0) }) : t('bt.hidden');
  $('bt-visible-hint').textContent = t(s.visible ? 'bt.visibleHintOn' : 'bt.visibleHint', { name });
}

// A device's row: its name and a line under it (kind, state), and its buttons on the right
function btRow(addr) {
  const li = document.createElement('li');
  li.dataset.id = addr;
  li.innerHTML = '<div class="bt-info"><span class="name"></span><span class="meta"></span></div><div class="acts"></div>';
  return li;
}

function btButton(cls, fn) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn small' + (cls ? ' ' + cls : '');
  b.addEventListener('click', () => fn(b));
  return b;
}

// Paired: Connect or Disconnect, Forget, and what it is used for (a select only when it can do more than one thing)
function btPairedRow(addr) {
  const li = btRow(addr);
  li.querySelector('.acts').append(btButton('', () => btToggle(addr)), btButton('ghost', () => btForget(addr)));
  const use = document.createElement('div');
  use.className = 'bt-use';
  use.innerHTML = '<span class="bt-use-k"></span><select></select><span class="bt-use-one"></span>';
  use.querySelector('select').addEventListener('change', (ev) => btSetUse(addr, ev.target));
  li.append(use);
  return li;
}

// Found nearby, not paired: Pair
function btFoundRow(addr) {
  const li = btRow(addr);
  li.querySelector('.acts').append(btButton('primary', () => btOrder(addr, 'pair')));
  return li;
}

// The state line: an order running, else how it is; a failed order adds why (until the next one)
function btMeta(li, d, parts) {
  if (d.pending) parts.push(t('bt.pending.' + (BT_ACTS.includes(d.pending) ? d.pending : 'connect')));
  else if (d.error) parts.push(t('bt.fail.' + (BT_FAILS.includes(d.error) ? d.error : 'failed')));
  li.querySelector('.name').textContent = btName(d);
  const meta = li.querySelector('.meta');
  meta.textContent = parts.filter(Boolean).join(' · ');
  meta.className = 'meta' + (d.pending ? '' : d.error ? ' warn' : d.output || d.connected ? ' ok' : '');
}

function updatePairedRow(li, d, lock) {
  // The monitor plays through it: «Playing the program», unless the headphones are switched off in Audio
  const phones = state.mixer && state.mixer.audio && state.mixer.audio.phones;
  const now = d.pending ? '' : d.output ? t(phones && phones.on === false ? 'bt.outputOff' : 'bt.output')
    : t(d.connected ? 'bt.connected' : 'bt.notConnected');
  btMeta(li, d, [btKind(d.kind), now]);
  const [toggle, forget] = li.querySelector('.acts').children;
  toggle.textContent = t(d.connected ? 'bt.disconnect' : 'bt.connect');
  forget.textContent = t('bt.forget');
  toggle.disabled = forget.disabled = lock;

  const uses = d.uses || [];
  const box = li.querySelector('.bt-use');
  const sel = box.querySelector('select');
  const many = uses.length > 1;
  box.hidden = !many && !d.use && !uses.length;
  box.querySelector('.bt-use-k').textContent = t('bt.useLabel');
  sel.hidden = !many;
  box.querySelector('.bt-use-one').textContent = many ? '' : btUse(d.use || uses[0] || '');
  if (!many) return;
  const key = uses.join(',') + '|' + getLang();
  if (sel.dataset.key !== key) { // only if they change: redoing them closes the open dropdown
    sel.dataset.key = key;
    sel.replaceChildren(...uses.map((u) => option(u, btUse(u))));
  }
  sel.setAttribute('aria-label', t('bt.useAria', { name: btName(d) }));
  if (!sel.dataset.busy) sel.value = d.use || uses[0];
  sel.disabled = !!sel.dataset.busy || !!d.pending;
}

function updateFoundRow(li, d, lock) {
  btMeta(li, d, [btKind(d.kind), typeof d.rssi === 'number' && !d.pending ? btSignal(d.rssi) : '']);
  const [pair] = li.querySelector('.acts').children;
  pair.textContent = t('bt.pair');
  pair.disabled = lock;
}

// Its audio input plays in the program (alone or in the mix) while streaming: cutting it is heard on air
function btOnAir(d) {
  const a = state.mixer && state.mixer.audio;
  if (!a || !emittingNow()) return false;
  const id = 'bt:' + d.address.replace(/:/g, '');
  return a.mode === 'mix' ? a.sources.some((x) => x.id === id && x.inMix) : a.main === id;
}

// An order on a device: the answer carries the state (the order running); then the fast polling follows it
async function btOrder(addr, action) {
  bt.busy++;
  if (bt.data) renderBluetooth(bt.data);
  try {
    const r = await api('POST', `/bluetooth/devices/${encodeURIComponent(addr)}/${action}`);
    if (r && r.bluetooth) bt.data = r.bluetooth;
  } catch (e) {
    toast(e.message, true);
  } finally {
    bt.busy--;
    if (bt.data) renderBluetooth(bt.data);
    loadBluetooth();
  }
}

function btToggle(addr) {
  const d = btDevice(addr);
  if (!d) return;
  if (d.connected && btOnAir(d) && !confirm(t('bt.disconnectOnAir', { name: btName(d) }))) return;
  btOrder(addr, d.connected ? 'disconnect' : 'connect');
}

function btForget(addr) {
  const d = btDevice(addr);
  if (!d) return;
  if (confirm(t(d.connected && btOnAir(d) ? 'bt.forgetConfirmOnAir' : 'bt.forgetConfirm', { name: btName(d) }))) btOrder(addr, 'forget');
}

async function btSetUse(addr, sel) {
  const use = sel.value;
  sel.dataset.busy = '1';
  sel.disabled = true;
  try {
    const r = await api('PUT', '/bluetooth/devices/' + encodeURIComponent(addr), { use });
    if (r && r.bluetooth) bt.data = r.bluetooth;
  } catch (e) {
    toast(e.message, true);
  } finally {
    delete sel.dataset.busy;
    if (bt.data) renderBluetooth(bt.data); // if the app refused it, back to what it says
  }
}

// The switch, the search and the visible window: the answer carries the new state
async function btSend(method, path, body, el) {
  if (el) el.disabled = true;
  try {
    const r = await api(method, '/bluetooth' + path, body);
    if (r && r.bluetooth) bt.data = r.bluetooth;
    return true;
  } catch (e) {
    toast(e.message, true);
    return false;
  } finally {
    if (el) el.disabled = false;
    if (bt.data) renderBluetooth(bt.data);
    loadBluetooth();
  }
}

$('bt-on').addEventListener('change', async (ev) => {
  const cb = ev.target;
  const on = cb.checked;
  // Off disconnects every device (the pairings stay): say who, and above all what is on air
  const conn = on ? [] : ((bt.data && bt.data.devices) || []).filter((d) => d.connected);
  if (conn.length) {
    const names = conn.map((d) => t('common.quoted', { s: btName(d) })).join(', ');
    if (!confirm(t(conn.some(btOnAir) ? 'bt.offConfirmOnAir' : 'bt.offConfirm', { n: conn.length, names }))) { cb.checked = true; return; }
  }
  if (!(await btSend('PUT', '', { on }, cb))) cb.checked = !on;
});

$('bt-scan').addEventListener('click', (ev) => btSend('POST', '/scan', undefined, ev.currentTarget));

$('bt-visible').addEventListener('change', async (ev) => {
  const cb = ev.target;
  const on = cb.checked;
  if (!(await btSend('POST', '/visible', { on }, cb))) cb.checked = !on;
});

// Every 2 s only with the card on screen: further down the page, it waits (and is asked as soon as it shows)
if (window.IntersectionObserver) {
  new IntersectionObserver((entries) => {
    const seen = entries.some((e) => e.isIntersecting);
    const was = bt.onScreen;
    bt.onScreen = seen;
    if (seen && !was && state.live) loadBluetooth();
  }).observe($('bt-card'));
} else {
  bt.onScreen = true;
}

hooks.live.push(loadBluetooth);
hooks.stop.push(() => clearTimeout(bt.timer));
onLangChange(() => { if (bt.data) renderBluetooth(bt.data); });

// --- Local recording (RECORDING.md §6) -------------------------------------------------------
// The program to a USB disk, with its own encode: REC, the mode (off · manual · with_output), the quality and the disk
// (eject, format). The recording's state comes with the mixer's (status.mixer.record, with every status and mixer
// event); the view (mode, quality, the qualities, the storage helper's state) from GET /recording, asked when the card
// first shows, after each action and when the disk's state changes (a disk plugged in or out). An older engine sends
// no record and an older app answers 404: no card. Errors are the app's message, already in the page's language.

const rec = { view: null, viewAt: 0, gone: false, shown: false, disk: '', gen: 0, loadAt: 0, busy: '', timer: 0,
  seq: null, base: 0, at: 0, shownS: 0, warnKey: '' };
const REC_MODES = ['off', 'manual', 'with_output'];
const REC_QUALITIES = ['standard', 'high', 'max', 'efficient'];
const REC_REASONS = ['no_disk', 'disk_removed', 'disk_full', 'disk_slow', 'write_error', 'encoder_error', 'power_lost', 'internal'];
const DISK_STATES = ['none', 'checking', 'ready', 'unsupported', 'unformatted', 'error', 'ejected'];
const FS_NAMES = { exfat: 'exFAT', fat32: 'FAT32', fat16: 'FAT16', fat12: 'FAT12', ext4: 'ext4', ntfs: 'NTFS' };
// GB in an hour at a bitrate in kbps: 3600 s / 8 bits / 10^6 kB per GB
const GB_PER_KBPS_HOUR = 3600 / 8 / 1e6;

// Recording, or asked to: the button stops, the quality and the disk wait
const recOn = (r) => !!r && (r.mode === 'on' || r.state === 'starting' || r.state === 'on');
const recQualityName = (id) => (REC_QUALITIES.includes(id) ? t('rec.q.' + id) : id || '');
const fsName = (fs) => FS_NAMES[fs] || fs || t('rec.fsUnknown');
const diskName = (d) => d.label || d.model || t('rec.unnamed');
// 4521 → «01:15:21»
const recClock = (s) => [Math.floor(s / 3600), Math.floor(s / 60) % 60, s % 60].map((n) => String(n).padStart(2, '0')).join(':');

// Minutes → «3 h 20 min», «45 min», «12 h»
function recSpan(min) {
  const m = Math.max(0, Math.floor(min));
  const h = Math.floor(m / 60);
  if (!h) return t('rec.min', { m });
  return m % 60 ? t('rec.hm', { h, m: m % 60 }) : t('rec.h', { h });
}

// Bytes → «62 GB», «1,2 TB», «740 MB»: decimal units, as disks are sold
function diskSize(b) {
  if (typeof b !== 'number' || !(b >= 0)) return '';
  if (b >= 1e12) return t('unit.tb', { v: fmtNum(b / 1e12, 1) });
  if (b >= 1e9) return t('unit.gb', { v: fmtNum(b / 1e9, b >= 1e10 ? 0 : 1) });
  return t('unit.mb', { v: fmtNum(b / 1e6, 0) });
}

// «Alta · 25 Mbps · 11,4 GB/h»; the codec only when it is not H.264
function qualityLabel(q) {
  const mbps = t('unit.mbps', { v: fmtNum((q.videoKbps || 0) / 1000, 0) });
  const rate = q.codec && q.codec !== 'h264' ? `${codecLabel(q.codec) || q.codec.toUpperCase()} ${mbps}` : mbps;
  const gb = fmtNum(((q.videoKbps || 0) + (q.audioKbps || 0)) * GB_PER_KBPS_HOUR, 1);
  return t('rec.qOption', { name: recQualityName(q.id), rate, gb });
}

// The disk in the format question: «EIRL-REC» (DataTraveler 3.0 · 62 GB)
function diskWho(d) {
  const name = t('common.quoted', { s: diskName(d) });
  const more = [d.label ? d.model : '', diskSize(d.sizeBytes)].filter(Boolean).join(' · ');
  return more ? `${name} (${more})` : name;
}

// The recording's state: the mixer's, unless an action's answer (the view's record) is newer by a moment
function curRecord() {
  const r = state.mixer && state.mixer.record;
  const v = rec.view && rec.view.record;
  if (r && v && Date.now() - rec.viewAt < 3000 && typeof v.seq === 'number' && typeof r.seq === 'number' && v.seq > r.seq) {
    return { ...v, disk: r.disk || v.disk };
  }
  return r || null;
}

// The disk as the card shows it: the helper's view (model, write speed, read-only, another disk, seq) under the
// engine's live figures (state, free space, time left). While the view is behind (another state: it is being asked
// again), only the engine's
function recDisk(r) {
  const s = (rec.view && rec.view.storage) || { state: 'none', seq: 0 };
  const live = r && r.disk;
  if (!live) return s;
  if (live.state !== s.state) return { ...live, seq: s.seq };
  const d = { ...s };
  for (const [k, val] of Object.entries(live)) if (val !== undefined && val !== null) d[k] = val;
  return d;
}

// The clock: the engine's elapsedS, counted on here between states (they come every ~2 s), never backwards within one
// recording and never more than 10 s past the last figure (a frozen engine stops it)
function recElapsed(r) {
  const now = Date.now();
  const s = Number(r.elapsedS) || 0;
  // Another recording: a new seq, or the same one going back (a retry in a new file)
  if (r.seq !== rec.seq || s + 2 < rec.base) { rec.seq = r.seq; rec.shownS = 0; rec.base = -1; }
  if (s !== rec.base) { rec.base = s; rec.at = now; }
  rec.shownS = Math.max(rec.shownS, rec.base + Math.min(10, Math.floor((now - rec.at) / 1000)));
  return rec.shownS;
}

function recTick(on) {
  if (on && !rec.timer) {
    rec.timer = setInterval(() => {
      const r = curRecord();
      if (r && r.state === 'on') renderRecLine(r);
    }, 1000);
  } else if (!on && rec.timer) {
    clearInterval(rec.timer);
    rec.timer = 0;
  }
}

async function loadRecording() {
  if (rec.gone) return;
  const gen = ++rec.gen;
  rec.loadAt = Date.now();
  let v;
  try {
    v = await api('GET', '/recording', undefined, { quiet: true });
  } catch (e) {
    // An app without the route (404): no card, and it is not asked again
    if (e.status === 404) {
      rec.gone = true;
      $('recording-card').hidden = true;
      recTick(false);
      state.recView = null;
      renderLiveBtn();
    }
    return;
  }
  if (gen === rec.gen) applyRecView(v);
}

// A view (GET /recording, PUT /recording, PUT /record): it replaces the one in flight. Go live's label depends on it
function applyRecView(v) {
  if (!v || typeof v !== 'object' || !v.storage) return;
  rec.gen++;
  rec.view = v;
  rec.viewAt = Date.now();
  state.recView = v;
  renderRecording();
  renderLiveBtn();
}

// A storage object (eject, format): the view's disk until GET /recording answers
function applyStorage(s) {
  if (rec.view && s && typeof s === 'object' && s.state) applyRecView({ ...rec.view, storage: s });
}

// Every mixer state (or null): the card shows with the engine's record, and the view is asked again when the disk's
// state changes. A first answer that failed is asked again, at most every 10 s
function recFromMixer(m) {
  const r = m && m.record;
  if (!r || rec.gone) {
    $('recording-card').hidden = true;
    rec.shown = false;
    recTick(false);
    return;
  }
  const disk = (r.disk && r.disk.state) || '';
  // The mode or the quality changed elsewhere (the screen, another browser): the engine's state says so at once,
  // and the view is asked once per change
  const set = (r.setting || '') + '/' + (r.quality || '');
  const moved = rec.shown && set !== rec.set;
  if (!rec.shown || disk !== rec.disk || moved || (!rec.view && Date.now() - rec.loadAt > 10000)) loadRecording();
  rec.shown = true;
  rec.disk = disk;
  rec.set = set;
  $('recording-card').hidden = false;
  renderRecording();
}

// The state line: off, «REC 01:12:34 · 3 h 20 min left» with the file under it, or why it stopped
function renderRecLine(r) {
  const v = rec.view;
  const d = recDisk(r);
  const reason = REC_REASONS.includes(r.reason) ? r.reason : '';
  let text;
  let cls = '';
  let sub = '';
  let subCls = '';
  if (r.state !== 'on') rec.seq = null; // the next one starts its clock from the engine's figure
  if (r.state === 'on') {
    text = t('rec.onAir', { time: recClock(recElapsed(r)) });
    // «3 h 20 min left» never split over two lines
    if (typeof d.minutesLeft === 'number') text += ' · ' + t('rec.left', { time: recSpan(d.minutesLeft) }).replace(/ /g, '\u00a0');
    cls = 'on';
    const parts = [r.file || '', r.kbps > 0 ? t('unit.mbps', { v: fmtNum(r.kbps / 1000, 1) }) : ''];
    if (r.droppedS > 0) {
      const n = Math.max(1, Math.round(r.droppedS));
      parts.push(t('rec.dropped', { n }));
      subCls = 'warn';
    }
    sub = parts.filter(Boolean).join(' · ');
  } else if (r.state === 'starting') {
    text = t('rec.starting');
  } else if (reason || r.state === 'error') {
    text = t('rec.reason.' + (reason || 'internal'));
    cls = reason === 'disk_removed' ? 'bad' : 'warn';
    // Still asked to record: it goes on in a new file when it can (the disk back, after a retry)
    if (r.mode === 'on') sub = t(reason === 'disk_removed' || reason === 'no_disk' ? 'rec.waitDisk' : 'rec.retrying');
  } else if (v && v.mode === 'off') {
    text = t('rec.offMode');
  } else {
    text = t('rec.idle');
    if (v && v.mode === 'with_output' && d.state === 'ready' && !emittingNow()) sub = t('rec.withOutputIdle');
  }
  const line = $('rec-line');
  line.textContent = text;
  line.className = 'rec-line' + (cls ? ' ' + cls : '');
  const s = $('rec-sub');
  s.textContent = sub;
  s.className = 'sub' + (subCls ? ' ' + subCls : '');
  s.hidden = !sub;
}

function renderRecording() {
  const r = curRecord();
  if (!r) return;
  const v = rec.view;
  const on = recOn(r);
  const d = recDisk(r);
  const busy = !!rec.busy;
  renderRecLine(r);
  recTick(r.state === 'on' && state.live && !$('recording-card').hidden);

  // REC: disabled with the reason (the mode is off, no disk ready, the disk full)
  const btn = $('rec-btn');
  btn.textContent = t(on ? 'rec.stop' : 'rec.start');
  btn.classList.toggle('danger', on);
  const st = DISK_STATES.includes(d.state) ? d.state : 'none';
  const full = st === 'ready' && typeof d.minutesLeft === 'number' && d.minutesLeft < 1;
  const why = on || !v ? ''
    : v.mode === 'off' ? t('rec.why.off')
      : st === 'checking' ? t('rec.why.checking')
        : st === 'none' ? t('rec.why.noDisk') : st === 'ejected' ? t('rec.why.ejected')
          : st !== 'ready' ? t('rec.why.unusable')
            : full ? t('rec.why.full') : '';
  btn.disabled = busy || !v || !!why;
  $('rec-why').textContent = why;
  $('rec-why').hidden = !why;

  // The mode and the quality: their options only when they change (redoing them closes an open dropdown); the value
  // is not repainted while a change is being sent or asked
  const ms = $('rec-mode');
  if (ms.dataset.key !== getLang()) {
    ms.dataset.key = getLang();
    ms.replaceChildren(...REC_MODES.map((m) => option(m, t('rec.mode.' + m))));
  }
  if (v && !ms.dataset.busy) ms.value = v.mode;
  ms.disabled = !v || busy || !!ms.dataset.busy;
  $('rec-mode-hint').textContent = REC_MODES.includes(ms.value) ? t('rec.modeHint.' + ms.value) : '';
  const qs = $('rec-quality');
  const list = ((v && v.qualities) || []).filter((q) => q && typeof q.id === 'string');
  const qk = getLang() + JSON.stringify(list);
  if (qs.dataset.key !== qk) {
    qs.dataset.key = qk;
    qs.replaceChildren(...list.map((q) => option(q.id, qualityLabel(q))));
  }
  if (v && !qs.dataset.busy) qs.value = v.quality;
  qs.disabled = !v || on || busy || !!qs.dataset.busy;
  $('rec-quality-hint').textContent = t(on ? 'rec.qLocked' : 'rec.qHint');

  renderRecDisk(v, d, st, on, busy);
}

// The disk: its state, what it is, and why it cannot be used (or what to know before using it); Eject with a disk ready
// and Format with any disk, never while recording
function renderRecDisk(v, d, st, on, busy) {
  const ds = $('rec-disk-state');
  ds.textContent = t('rec.disk.' + st);
  ds.className = 'sub' + (st === 'ready' ? ' ok' : ['unsupported', 'unformatted', 'error'].includes(st) ? ' warn' : '');
  const hint = st === 'none' ? t('rec.noneHint') : st === 'ejected' ? t('rec.ejectedHint') : '';
  $('rec-disk-hint').textContent = hint;
  $('rec-disk-hint').hidden = !hint;

  // What it is: its name, then «exFAT · 41 GB free of 62 GB · writes at 31 MB/s» and how long it can record
  $('rec-disk-info').hidden = st === 'none';
  if (st !== 'none') {
    const size = diskSize(d.sizeBytes);
    const free = st === 'ready' ? diskSize(d.freeBytes) : '';
    const space = free && size ? t('rec.freeOf', { free, size }) : free ? t('rec.free', { free }) : size;
    const speed = d.writeMBps > 0 ? t('rec.writes', { v: fmtNum(d.writeMBps, d.writeMBps >= 10 ? 0 : 1) }) : '';
    $('rec-disk-name').textContent = diskName(d);
    const fs = d.fs ? fsName(d.fs) : st === 'unsupported' && d.reason === 'fs_unknown' ? t('rec.fsUnknown') : '';
    $('rec-disk-meta').textContent = [fs, space, speed].filter(Boolean).join(' · ');
    const time = st === 'ready' && d.minutesLeft >= 1 && v
      ? t('rec.timeAt', { time: recSpan(d.minutesLeft), quality: inSentence(recQualityName(v.quality)) }) : '';
    $('rec-disk-time').textContent = time;
    $('rec-disk-time').hidden = !time;
  }

  // A disk under 1 GB is not worth formatting (it would still be too small), and a write-protected one cannot be
  const tiny = st === 'unsupported' && d.reason === 'too_small' && d.sizeBytes > 0 && d.sizeBytes < 1e9;
  const locked = d.readOnly === true || (st === 'error' && d.reason === 'readonly');
  const warns = [];
  const warn = (key, p) => warns.push(t('rec.warn.' + key, p));
  if (st === 'unsupported') {
    if (d.reason === 'fs_ntfs') warn('ntfs');
    else if (d.reason === 'too_small') warn(tiny ? 'tooSmall' : 'tooSmallPart');
    else warn('unknownFs');
  } else if (st === 'unformatted') {
    warn('unformatted');
  } else if (st === 'error') {
    warn(d.reason === 'readonly' ? 'readonly' : d.reason === 'check_failed' ? 'checkFailed' : 'mountFailed');
  }
  if (locked && !(st === 'error' && d.reason === 'readonly')) warn('readonly');
  if (st === 'ready' && /^fat/.test(d.fs || '')) warn('fat', { fs: fsName(d.fs) });
  if (d.dirty === true) warn('dirty');
  if (st === 'ready' && typeof d.minutesLeft === 'number' && d.minutesLeft >= 1 && d.minutesLeft < 10) warn('low');
  if (d.anotherDisk) warn('another');
  const wk = warns.join('\n');
  if (rec.warnKey !== wk) {
    rec.warnKey = wk;
    $('rec-warns').replaceChildren(...warns.map((w) => {
      const p = document.createElement('p');
      p.textContent = w;
      return p;
    }));
  }
  $('rec-warns').hidden = !warns.length;

  // A recording, or a disk gone, answers an open format question with no
  const fask = $('rec-format-ask');
  if (on || st === 'none' || st === 'checking' || tiny || locked) closeAsk(fask);
  const asking = !fask.hidden;
  const ej = $('rec-eject');
  const fm = $('rec-format');
  ej.hidden = st !== 'ready' || asking;
  fm.hidden = !v || st === 'none' || st === 'checking' || tiny || locked || asking;
  ej.disabled = fm.disabled = on || busy;
  const dwhy = on && (!ej.hidden || !fm.hidden) ? t('rec.warn.busyRec') : '';
  $('rec-disk-why').textContent = dwhy;
  $('rec-disk-why').hidden = !dwhy;
  const doing = rec.busy === 'format' ? t('rec.formatting') : rec.busy === 'eject' ? t('rec.ejecting') : '';
  $('rec-busy').textContent = doing;
  $('rec-busy').hidden = !doing;
}

// An action: the card waits for it (busy) and shows its answer; GET /recording follows anyway. true when it went well
async function recAction(kind, method, path, body, apply) {
  rec.busy = kind;
  renderRecording();
  let ok = false;
  try {
    const res = await api(method, path, body);
    apply(res);
    ok = true;
  } catch (e) {
    toast(e.message, true); // 409 disabled / no_disk / disk_full / recording / disk_changed…, in the page's language
  } finally {
    rec.busy = '';
    renderRecording();
    loadRecording();
  }
  return ok;
}

$('rec-btn').addEventListener('click', () => {
  const r = curRecord();
  if (r) recAction('rec', 'PUT', '/record', { on: !recOn(r) }, applyRecView);
});

$('rec-mode').addEventListener('change', async (ev) => {
  const sel = ev.target;
  const mode = sel.value;
  sel.dataset.busy = '1';
  // Off stops the recording that is on: asked first, in the page
  if (mode === 'off' && recOn(curRecord())) {
    renderRecording();
    if (!(await askInline($('rec-mode-ask'), t('rec.modeOffAsk'), t('rec.modeOffYes'), true))) {
      delete sel.dataset.busy;
      renderRecording();
      return;
    }
  }
  await recAction('mode', 'PUT', '/recording', { mode }, applyRecView);
  delete sel.dataset.busy;
  renderRecording();
});

$('rec-quality').addEventListener('change', async (ev) => {
  const sel = ev.target;
  sel.dataset.busy = '1';
  await recAction('quality', 'PUT', '/recording', { quality: sel.value }, applyRecView);
  delete sel.dataset.busy;
  renderRecording();
});

$('rec-eject').addEventListener('click', async () => {
  if (await recAction('eject', 'POST', '/storage/eject', undefined, applyStorage)) toast(t('rec.ejected'));
});

// Format: the question names the disk; seq is the disk the page saw, so a disk swapped after the question is not the
// one formatted (409 disk_changed)
$('rec-format').addEventListener('click', async () => {
  const v = rec.view;
  if (!v) return;
  const seq = v.storage.seq;
  const ask = askInline($('rec-format-ask'), t('rec.formatAsk', { disk: diskWho(recDisk(curRecord())) }), t('rec.formatYes'), true);
  renderRecording(); // the buttons hide while it asks
  const yes = await ask;
  renderRecording();
  if (yes && await recAction('format', 'POST', '/storage/format', { seq }, applyStorage)) toast(t('rec.formatted'));
});

hooks.mixer.push(recFromMixer);
hooks.live.push(() => { if (rec.shown) loadRecording(); });
hooks.stop.push(() => recTick(false));
onLangChange(() => { rec.warnKey = ''; renderRecording(); });

// --- Updates (OTA) ---------------------------------------------------------------------------
// The app checks by itself (every ~6 h) and downloads; installing is only on a button press, never while
// streaming, and a version that does not come up healthy is rolled back. Polled every 2 s while something
// is going on, every minute otherwise (only with the dashboard live). While installing, the app restarts:
// failed requests are expected, and the page reloads itself when the new UI answers (staleWeb).

const upd = { data: null, timer: 0, installing: false, dismissed: '', asked: false, rebootAt: 0 };
const REBOOT_WAIT = 3 * 60 * 1000; // «Restarting…» stays this long after the app took it (the board goes off before)
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
  renderUpdateSystem(u);
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
  const recording = recOn(state.mixer && state.mixer.record); // busy like streaming (RECORDING.md §5.5)
  const serverOk = typeof u.canInstall === 'boolean' ? u.canInstall : true;
  $('update-install').disabled = !shown || streaming || recording || !serverOk;
  const why = !shown ? '' : streaming ? t('update.streaming') : recording ? t('update.recording') : !serverOk ? (u.blockedMessage || '') : '';
  $('update-why').hidden = !why;
  $('update-why').textContent = why;
}

// Below the app (the system layer, UPDATES.md §12): a download that waits for Wi-Fi or a cable («Check now»
// downloads it), a system part that did not install, and the restart that finishes an update. The app's words
// come in the request's language, as fragments
const capFirst = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : '');
function renderUpdateSystem(u) {
  // With a release available, it replaces «it downloads by itself» (update-msg); otherwise its own line
  const waits = u.downloadWaits === 'metered' ? capFirst(u.downloadWaitsMessage || '') : '';
  if (waits && u.state === 'available') $('update-msg').textContent = waits;
  $('update-waits').textContent = waits;
  $('update-waits').hidden = !waits || u.state === 'available';
  const sys = u.system || null;
  const sysMsg = sys && (sys.state === 'failed' || sys.state === 'skipped') ? capFirst(sys.message || '') : '';
  $('update-system').textContent = sysMsg;
  $('update-system').className = 'hint' + (sys && sys.state === 'failed' ? ' warn' : '');
  $('update-system').hidden = !sysMsg;
  const restart = !!(sys && sys.restartRequired === true);
  if (!restart) { upd.rebootAt = 0; closeAsk($('update-reboot-ask')); }
  $('update-restart').hidden = !restart;
  if (restart) $('update-restart-msg').textContent = capFirst(sys.restartMessage || '') || t('update.restartNeeded');
  renderRebootGate();
}

const updRebooting = () => upd.rebootAt > 0 && Date.now() - upd.rebootAt < REBOOT_WAIT;

// «Restart now»: never while streaming nor while an update installs (follows every status and mixer change)
function renderRebootGate() {
  const u = upd.data;
  if (!u || $('update-restart').hidden) return;
  const streaming = emittingNow() || u.streaming === true;
  const recording = recOn(state.mixer && state.mixer.record);
  const going = updRebooting();
  const btn = $('update-reboot');
  btn.disabled = going || streaming || recording || UPD_BUSY.includes(u.state);
  btn.hidden = !$('update-reboot-ask').hidden;
  const why = going ? t('update.rebooting') : streaming ? t('update.rebootStreaming') : recording ? t('update.rebootRecording') : '';
  $('update-restart-why').textContent = why;
  $('update-restart-why').hidden = !why;
  if (streaming || recording) closeAsk($('update-reboot-ask'));
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
  if (recOn(state.mixer && state.mixer.record)) { toast(t('update.recording'), true); return; }
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

$('update-reboot').addEventListener('click', async () => {
  const u = upd.data;
  if (!u || !u.system || !u.system.restartRequired) return;
  if (emittingNow() || recOn(state.mixer && state.mixer.record)) { renderRebootGate(); return; }
  const ask = $('update-reboot-ask');
  $('update-reboot').hidden = true;
  const yes = await askInline(ask, t('update.rebootAsk'), t('update.rebootYes'), true);
  $('update-reboot').hidden = false;
  if (!yes) { renderRebootGate(); return; }
  $('update-reboot').disabled = true;
  try {
    await api('POST', '/update/reboot');
    upd.rebootAt = Date.now();
    toast(t('update.rebootingToast'));
  } catch (e) {
    toast(e.message, true); // 409 streaming / nothing_pending, 503 power_unavailable: in the request's language
    loadUpdate();
  }
  renderRebootGate();
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
hooks.status.push(renderUpdateGate, renderRebootGate);
hooks.mixer.push(renderUpdateGate, renderRebootGate);
onLangChange(() => {
  if (upd.data) renderUpdate(upd.data);
  if (state.device) {
    fillLangSelect($('dev-lang'), false);
    renderHostHint(state.device.hostname);
    renderClock();
  }
});
