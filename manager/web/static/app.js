// eirl-encoderd — minimal web UI (no build, no dependencies). API: /api/v1 (MANAGER.md §3).
// It only keeps the live connection (SSE) and the preview while the tab is visible: a phone with the
// page in the background costs the board nothing. Every visible text goes through t() (i18n.js).
'use strict';

const $ = (id) => document.getElementById(id);
// The other cards (settings.js, setup.js) hook into the app's life: start (dashboard shown), live (the live
// connection opens: also when the tab is shown again), stop (it closes), status (every status) and mixer (a
// new mixer state, or null)
const hooks = { start: [], live: [], stop: [], status: [], mixer: [] };
const state = { status: null, dest: null, catalog: null, catalogAt: 0, inputDirty: false, inputSaved: '', cloud: null, cloudTimer: 0, es: null, previewTimer: 0, previewGen: 0, levelTimer: 0, live: false, pc: null, frames: 0, frameAt: 0, statsTimer: 0, rtcError: '', mixer: null, mixerImgTimer: 0, pauseDirty: false, audioRows: 0, device: null };

// --- API -----------------------------------------------------------------------------------

class ApiError extends Error {
  constructor(status, data) {
    super((data && data.message) || t('common.httpError', { status }));
    this.status = status;
    this.data = data;
  }
}

// opts.quiet: no screen change on 401/503 (probing routes before the app knows where it is)
async function api(method, path, body, opts) {
  const opt = { method, credentials: 'same-origin', headers: { 'Accept-Language': getLang() } };
  if (method !== 'GET') {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body === undefined ? {} : body);
  }
  let r;
  try {
    r = await fetch('/api/v1' + path, opt);
  } catch (_) {
    throw new ApiError(0, { error: 'network', message: t('common.network') });
  }
  let data = null;
  if (r.status !== 204 && r.status !== 304) {
    try { data = await r.json(); } catch (_) { /* no JSON body */ }
  }
  if (!(opts && opts.quiet)) {
    if (r.status === 401 && path !== '/login') { showLogin(); }
    if (r.status === 503 && data && data.error === 'no_password') { show('nopass'); }
    // Setup was restarted (or never finished) while the dashboard was open: back to the wizard
    if (r.status === 503 && data && data.error === 'setup_required' && $('setup').hidden && !state.rebooting) {
      state.rebooting = true;
      stopLive();
      boot().finally(() => { state.rebooting = false; });
    }
  }
  if (!r.ok) throw new ApiError(r.status, data);
  return data;
}

// --- Screens -------------------------------------------------------------------------------

function show(name) {
  for (const id of ['login', 'nopass', 'setup', 'app']) $(id).hidden = id !== name;
}

function showLogin() {
  show('login');
  stopLive();
  $('login-pw').value = '';
  $('login-pw').focus();
}

// The language before anything is shown: this browser's choice, else the device's (GET /setup carries it
// without a session; GET /device needs one once a password exists), else the browser's
async function initLang() {
  let dev = '';
  state.setup = await fetchSetup();
  if (state.setup) dev = normLang(state.setup.language);
  if (!dev) {
    try {
      state.device = await api('GET', '/device', undefined, { quiet: true });
      dev = normLang(state.device && state.device.language);
    } catch (_) { /* older app, or a session is needed: the browser's language until login */ }
  }
  setLang(langOverride() || dev || browserLang() || 'en');
}

// After login: the device language, unless this browser chose another one
async function syncDeviceLang() {
  try {
    state.device = await api('GET', '/device', undefined, { quiet: true });
  } catch (_) { return; }
  const dev = normLang(state.device && state.device.language);
  if (dev && !langOverride()) setLang(dev);
}

async function boot() {
  await initLang();
  // Setup not finished: the wizard (setup.js). Without a password yet, straight away; with one, after login
  const setup = state.setup;
  if (setup && !setup.done) {
    if (setup.needsPassword) { showSetup(setup); return; }
    try {
      await api('GET', '/session', undefined, { quiet: true });
      showSetup(setup);
    } catch (e) {
      if (e.status === 401) showLogin();
    }
    return;
  }
  try {
    await api('GET', '/session', undefined, { quiet: true });
    startApp();
  } catch (e) {
    if (e.status === 401) showLogin();
    else if (e.status === 503 && e.data && e.data.error === 'no_password') show('nopass'); // an app without the wizard
  }
}

// GET /setup (works without a session). null with an app that does not have it: no wizard
async function fetchSetup() {
  try {
    return await api('GET', '/setup', undefined, { quiet: true });
  } catch (_) {
    return null;
  }
}

// After signing in: the wizard if setup is not finished, else the dashboard
async function afterLogin() {
  const setup = await fetchSetup();
  if (setup && !setup.done) showSetup(setup);
  else startApp();
}

$('login-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const err = $('login-err');
  err.hidden = true;
  try {
    await api('POST', '/login', { password: $('login-pw').value });
    $('login-pw').value = '';
    afterLogin();
  } catch (e) {
    err.textContent = e.status === 401 ? t('login.wrong') : e.message;
    err.hidden = false;
  }
});

$('logout').addEventListener('click', async () => {
  try { await api('POST', '/logout'); } catch (_) { /* does not matter */ }
  showLogin();
});

function startApp() {
  show('app');
  syncDeviceLang();
  loadDestination();
  loadSources();
  loadLogs();
  api('GET', '/info').then((i) => {
    if (staleWeb(i)) return;
    $('info').textContent = `eirl-encoderd ${i.version} · ${i.hostname}` + (i.l4t ? ` · L4T ${(i.l4t.match(/R\d+.*?REVISION: [\d.]+/) || [''])[0].replace(' (release), REVISION: ', '.')}` : '');
  }).catch(() => {});
  if (!document.hidden) startLive();
  for (const fn of hooks.start) fn();
}

// The UI lives inside the binary. A tab left open during an update would keep the old JS (neither the
// login nor the SSE reconnection reload the page): if the fingerprint changes, reload.
function staleWeb(i) {
  if (state.web && i.web && i.web !== state.web) {
    location.reload();
    return true;
  }
  state.web = state.web || i.web;
  return false;
}

// --- Live: SSE + preview ---------------------------------------------------------------------

function startLive() {
  if (state.live) return;
  state.live = true;
  const es = new EventSource('/api/v1/events?levels=1');
  es.addEventListener('status', (e) => renderStatus(JSON.parse(e.data)));
  es.addEventListener('log', (e) => addLog(JSON.parse(e.data), true));
  es.addEventListener('level', (e) => renderLevel(JSON.parse(e.data)));
  // Mixer: as soon as it changes (a tap on the encoder's screen, another tab). Also Go live: with the
  // mixer, what is streamed is what the engine says
  es.addEventListener('mixer', (e) => {
    renderMixer(JSON.parse(e.data));
    renderLiveBtn();
  });
  // After a cut (e.g. the app restarted after an update)
  es.onopen = () => api('GET', '/info').then(staleWeb).catch(() => {});
  es.onerror = () => {
    // EventSource retries by itself; if the session expired, back to the login
    setTimeout(() => api('GET', '/session').catch(() => {}), 1000);
  };
  state.es = es;
  startRTC(++state.previewGen);
  loadCloud();
  for (const fn of hooks.live) fn();
}

function stopLive() {
  state.live = false;
  state.previewGen++; // cancels a pending preview retry
  if (state.es) { state.es.close(); state.es = null; }
  clearTimeout(state.previewTimer);
  clearTimeout(state.cloudTimer);
  stopMixerImages();
  closeRTC();
  renderLevel(null);
  for (const fn of hooks.stop) fn();
}

document.addEventListener('visibilitychange', () => {
  if ($('app').hidden) return;
  if (document.hidden) stopLive(); else startLive();
});

// Live preview over WebRTC (~0.2–0.3 s behind, with audio). A single signalling request: the app is
// ICE lite and answers with its candidates. It starts muted: browsers do not play sound without a
// tap, and nobody gets a fright with the volume.
// gen: attempt number. Hiding and showing the tab again with a negotiation in flight would leave two
// alive; the old one sees that its gen is no longer the current one and stops.
async function startRTC(gen) {
  if (!state.live || gen !== state.previewGen) return;
  state.rtcError = '';
  if (!window.RTCPeerConnection) { state.rtcError = 'live.noRTC'; showRTC(false); return; }
  const pc = new RTCPeerConnection();
  state.pc = pc;
  state.frames = 0;
  state.frameAt = 0;
  pc.addTransceiver('video', { direction: 'recvonly' });
  pc.addTransceiver('audio', { direction: 'recvonly' });
  const video = $('live');
  pc.ontrack = (e) => { if (e.streams[0] && video.srcObject !== e.streams[0]) video.srcObject = e.streams[0]; };
  pc.onconnectionstatechange = () => {
    if (state.pc !== pc || !['failed', 'disconnected', 'closed'].includes(pc.connectionState)) return;
    closeRTC();
    if (gen === state.previewGen) state.previewTimer = setTimeout(() => startRTC(gen), 2000);
  };
  try {
    await pc.setLocalDescription(await pc.createOffer());
    const r = await fetch('/api/v1/live', {
      method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'Accept-Language': getLang() },
      body: JSON.stringify({ sdp: pc.localDescription.sdp }),
    });
    if (state.pc !== pc) return; // closed meanwhile (hidden tab)
    if (r.status === 401) { closeRTC(); showLogin(); return; }
    if (!r.ok) { // 429: already 3 open (from other sessions); 503: the app does not have it. Retried
      closeRTC();
      state.rtcError = r.status === 429 ? 'live.tooMany' : 'live.unavailable';
      showRTC(false);
      if (gen === state.previewGen) state.previewTimer = setTimeout(() => startRTC(gen), r.status === 429 ? 5000 : 30000);
      return;
    }
    await pc.setRemoteDescription({ type: 'answer', sdp: (await r.json()).sdp });
    state.statsTimer = setInterval(() => watchFrames(pc), 1000);
  } catch (_) {
    if (state.pc !== pc) return;
    closeRTC();
    if (gen === state.previewGen) state.previewTimer = setTimeout(() => startRTC(gen), 3000);
  }
}

function closeRTC() {
  clearInterval(state.statsTimer);
  if (state.pc) { state.pc.onconnectionstatechange = null; state.pc.close(); state.pc = null; }
  $('live').srcObject = null;
  showRTC(false);
}

// With the connection open, video may not arrive (no signal, stream stopped): check whether decoded
// frames advance and, if not, show the notice instead.
async function watchFrames(pc) {
  if (state.pc !== pc) return;
  let n = 0;
  try {
    (await pc.getStats()).forEach((s) => { if (s.type === 'inbound-rtp' && s.kind === 'video') n = s.framesDecoded || 0; });
  } catch (_) { return; }
  if (n > state.frames) { state.frames = n; state.frameAt = Date.now(); }
  showRTC(Date.now() - state.frameAt < 2000);
}

function showRTC(on) {
  $('live').hidden = !on;
  $('live-sound').hidden = !on;
  $('preview-empty').hidden = on;
  if (state.rtcError) $('preview-empty').textContent = t(state.rtcError);
}

function renderSoundBtn() {
  $('live-sound').textContent = $('live').muted ? t('live.soundOn') : t('live.mute');
}

$('live-sound').addEventListener('click', () => {
  const v = $('live');
  v.muted = !v.muted;
  if (!v.muted) v.play().catch(() => {});
  renderSoundBtn();
});

// --- Status ----------------------------------------------------------------------------------

const PHASES = {
  streaming: ['phase.streaming', 'live'],
  ready: ['phase.ready', 'ready'],
  waiting_signal: ['phase.waitingSignal', 'warn'],
  waiting_device: ['phase.waitingDevice', 'warn'],
  restarting: ['phase.restarting', 'warn'],
  stopped: ['phase.stopped', ''],
  failed: ['phase.failed', 'bad'],
};

function setStat(id, text, sub, cls, subCls) {
  const big = $(id);
  big.textContent = text;
  big.className = 'big' + (cls ? ' ' + cls : '');
  const s = $(id + '-sub');
  s.textContent = sub || '';
  s.className = 'sub' + (subCls ? ' ' + subCls : '');
}

const num = (v, d) => fmtNum(v, d);
// The engine names its own sources in Spanish («HDMI de la placa», «Barras»/«Test pattern»). They have fixed
// ids: video hdmi and test, and the board HDMI's audio input i2s; drawn here by id, in the page's language.
// USB and network sources keep their device's or the user's name (GET /sources is already translated by the app)
const FIXED_SOURCES = { hdmi: 'src.hdmi', i2s: 'src.hdmi', test: 'src.test' };
const fixedName = (id, name) => (FIXED_SOURCES[id] ? t(FIXED_SOURCES[id]) : name);
// A source's name as the user sees it: its slot alias (slots.json), else its name (fixedName)
const srcName = (s) => (s && s.alias) || (s && fixedName(s.id, s.name)) || '';

// Why a source, an audio input or the headphones are not live: the engine's stable code (ENGINE_GO.md §13) with its
// arguments, translated here (detail.<code>). Without a code (an older engine), with one this page does not know or
// with an argument missing, the engine's own text (detail, Spanish). The output has no code: it goes by its reason.
// For each code, the arguments it needs; seconds are numbers in the page's format
const DETAIL_ARGS = {
  hdmi_no_signal: [], hdmi_signal_lost: [], hdmi_locking: [], hdmi_mode_changed: ['from', 'to'], hdmi_interlaced: ['mode'],
  hdmi_too_fast: ['mode'], hdmi_set_mode_failed: ['error'], hdmi_hung: [], hdmi_no_audio: [],
  gone: [], no_bus: [], no_format: [], unsupported: ['error'], probe_failed: ['rc', 'error'], hung_given_up: [],
  hung_on_air_unknown: [], hung_on_air: [], port_reset: [],
  net_receiver_down: [], net_waiting: [], net_no_video: ['tracks'], net_no_audio: [], net_disconnected: [], net_behind: ['seconds'],
  phones_waiting_screen: [], phones_screen_no_audio: [], phones_no_device: [], phones_not_opening: [],
  no_image: [], frames_stopped: ['seconds'], no_samples: ['seconds'], stream_ended: [], audio_ended: [], stream_error: ['error'],
  start_failed: [], build_failed: ['error'], no_sink: [], prepare_failed: [], internal_error: ['error'],
};

function detailText(x) {
  if (!x) return '';
  const code = x.detailCode;
  const a = x.detailArgs || {};
  const need = code && Object.prototype.hasOwnProperty.call(DETAIL_ARGS, code) ? DETAIL_ARGS[code] : null;
  if (!need || need.some((k) => typeof a[k] !== 'string' || !a[k])) return x.detail || '';
  const args = { ...a };
  if (args.seconds) args.seconds = secondsLabel(args.seconds);
  if (code === 'no_format') return a.warnings ? t('detail.no_format_warnings', { warnings: a.warnings }) : t('detail.no_format');
  if (code === 'hung_on_air') {
    const names = onAirNames(a.video, a.audio);
    return names.length ? t('detail.hung_on_air', { names: names.join(', '), n: names.length }) : t('detail.hung_on_air_unknown');
  }
  return t('detail.' + code, args);
}

// "2.4" → «2,4» in Spanish and French; what is not a number stays as it came
function secondsLabel(v) {
  const n = Number(v);
  if (!Number.isFinite(n)) return v;
  const dot = v.indexOf('.');
  return num(n, dot < 0 ? 0 : Math.min(1, v.length - dot - 1));
}

// hung_on_air: the ids on air (video: sources[].id, audio: audio inputs' ids), named as this page shows them
function onAirNames(video, audio) {
  const m = state.mixer || {};
  const names = [];
  for (const id of (video || '').split(',').filter(Boolean)) {
    const s = (m.sources || []).find((x) => x.id === id);
    names.push(s ? srcName(s) : fixedName(id, id));
  }
  for (const id of (audio || '').split(',').filter(Boolean)) {
    const x = ((m.audio && m.audio.sources) || []).find((i) => i.id === id);
    names.push(x ? fixedName(x.id, x.name) : fixedName(id, id));
  }
  return [...new Set(names)];
}

// A detail inside a sentence: lower-case first letter, unless it is an acronym («HDMI», «USB»)
const inSentence = (s) => (s && /^\p{Lu}\p{Ll}/u.test(s) ? s[0].toLowerCase() + s.slice(1) : s);

function renderStatus(st) {
  state.status = st;
  const svc = st.service;
  let [label, cls] = PHASES[svc.phase] ? [t(PHASES[svc.phase][0]), PHASES[svc.phase][1]] : [svc.phase, ''];
  // «Live» in red only with someone receiving: without an SRT connection there is nobody on the other side
  if (svc.phase === 'streaming' && !st.srt && !st.outputState) [label, cls] = [t('phase.noSrt'), 'warn'];
  // Streaming: how the output really is (with the mixer it is hooked up live and retried)
  const os = st.outputState || {};
  if (svc.phase === 'streaming' && os.state === 'connecting') [label, cls] = [t('common.connecting'), 'warn'];
  if (svc.phase === 'streaming' && os.state === 'error') [label, cls] = [t('phase.noConnection'), 'warn'];
  const pill = $('phase');
  pill.textContent = label;
  pill.className = 'pill' + (cls ? ' p-' + cls : ''); // p-*: bare .warn/.bad are text colours

  $('pending').hidden = !st.pendingRestart;

  renderInputStatus(st);
  renderMixer(st.mixer || null);

  // Buttons
  const running = svc.state === 'active' || svc.state === 'activating';
  renderLiveBtn();
  $('btn-restart').disabled = svc.state === 'inactive';

  // Signal: the running source's (the board's HDMI, USB or the test pattern)
  const sg = st.signal;
  const act = (st.input || {}).active;
  // Fewer fps than promised (a webcam in low light, a capture card on USB 2): warn
  const low = sg.measuredFps != null && sg.fps > 0 && sg.measuredFps < sg.fps * 0.9;
  const lowTxt = low ? ' · ' + t('signal.arriving', { fps: num(sg.measuredFps, 1) }) : '';
  if (st.mixer) {
    // Mixer: the output is the program (always with a picture); each source's signal, in «Sources»
    const pg = st.mixer.program;
    const srcs = st.mixer.sources;
    const airSrc = srcs.find((s) => s.id === pg.main);
    const dark = !airSrc || airSrc.state !== 'live';
    setStat('st-signal', t('signal.program', { w: pg.width, h: pg.height, fps: pg.fps }),
      t('signal.liveSources', { live: srcs.filter((s) => s.state === 'live').length, n: srcs.length }) +
      (airSrc ? ' · ' + t(dark ? 'signal.onAirDark' : 'signal.onAir', { name: srcName(airSrc) }) : ''), 'ok', dark ? 'warn' : '');
  } else if (sg.kind === 'test') {
    setStat('st-signal', t('signal.test'), t('signal.testSub'), 'ok');
  } else if (sg.present && sg.kind === 'usb') {
    const what = act ? [act.name, formatLabel(act.format)].filter(Boolean).join(' · ') : 'USB';
    setStat('st-signal', `${sg.width}×${sg.height} @ ${fpsLabel(sg.fps)}`, what + lowTxt, 'ok', low ? 'warn' : '');
  } else if (sg.present) {
    setStat('st-signal', `${sg.width}×${sg.height} @ ${fpsLabel(sg.fps)}`,
      (sg.supported ? t('signal.supported') : t('signal.unsupported')) + lowTxt,
      sg.supported ? 'ok' : 'bad', low ? 'warn' : '');
  } else if (svc.phase === 'waiting_device') {
    setStat('st-signal', t('signal.noDevice'), t('signal.noDeviceSub'), 'bad');
  } else if (sg.kind === 'usb') {
    setStat('st-signal', t('signal.none'), t('signal.noneUsb'), 'bad');
  } else {
    setStat('st-signal', t('signal.none'), t('signal.noneSub'), 'bad');
  }

  // Audio
  const au = {
    playing: [t('audio.playing'), t('audio.playingSub'), 'ok'],
    pll_unlocked: [t('audio.pll'), t('audio.pllSub'), 'warn'],
    no_samples: [t('audio.none'), sg.present ? t('audio.noSamplesSub') : '', ''],
    test: [t('audio.test'), t('audio.testSub'), ''],
    usb: [t('audio.usb'), act && act.audio ? t('audio.usbSub', { card: act.audio.replace(/^alsa:/, '') }) : '', 'ok'],
    silence: [t('audio.none'), t('audio.silenceSub'), ''],
  }[st.audio.state] || ['—', '', ''];
  const mau = st.mixer && st.mixer.audio;
  if (mau && mau.mode === 'mix') {
    // Mix: the checked inputs, together
    const on = mau.sources.filter((x) => x.inMix);
    const live = on.some((x) => x.state === 'live');
    if (!on.length) setStat('st-audio', t('audio.none'), t('audio.mixEmpty'), '');
    else setStat('st-audio', live ? t('audio.mixing') : t('signal.none'), on.map((x) => fixedName(x.id, x.name)).join(' + '), live ? 'ok' : 'warn');
  } else if (mau) {
    // Mixer: the audio input on air, independent of the video
    const on = mau.sources.find((x) => x.id === mau.main);
    if (!on) setStat('st-audio', t('audio.none'), t('audio.pickOne'), '');
    else setStat('st-audio', on.state === 'live' ? t('audio.playing') : t('signal.none'), fixedName(on.id, on.name), on.state === 'live' ? 'ok' : 'warn');
  } else {
    setStat('st-audio', au[0], au[1], au[2]);
  }

  // Connection: the output's protocol and codec (OUTPUTS.md §8-9) and the sender's statistics when it has them (every
  // protocol but UDP). An older app has neither field: its transport (srt or srtla) is the protocol
  const mode = state.dest ? state.dest.mode : '';
  const reason = (r) => (REASONS.includes(r) ? t('reason.' + r) : r);
  const mo = (st.mixer && st.mixer.output) || {};
  const proto = st.protocol || mo.protocol || st.transport || '';
  const codec = codecLabel(st.codec || mo.codec);
  if (st.output === 'off' && running) {
    setStat('st-srt', t('srt.off'), t('srt.offSub'), '');
  } else if (os.state === 'error' || (os.state === 'connecting' && os.reason)) {
    setStat('st-srt', os.state === 'error' ? t('phase.noConnection') : t('srt.reconnecting'), t('srt.retrying', { reason: reason(os.reason) }), 'warn');
  } else if (st.srt) {
    const s = st.srt;
    const loss = s.lossPct > 0 ? ' · ' + t('srt.loss', { pct: num(s.lossPct, 1) }) : '';
    // With the adaptive bitrate, the encoder's right now (below the maximum = the network gives no more)
    const br = st.bitrate ? ' · ' + t('srt.video', { cur: num(st.bitrate.kbps / 1000, 1), max: num(st.bitrate.maxKbps / 1000, 1) }) : '';
    const low = st.bitrate && st.bitrate.kbps < st.bitrate.maxKbps;
    const re = s.pktRetrans > 0 ? ' · ' + t('conn.retrans', { n: s.pktRetrans }) : '';
    setStat('st-srt', proto ? t('conn.connected', { proto: protoLabel(proto) }) : t('srt.connected'),
      (codec ? codec + ' · ' : '') + t('srt.stats', { rtt: num(s.rttMs, 0), mbps: num(s.sendMbps, 1) }) + re + loss + br, 'ok', s.lossPct > 1 || low ? 'warn' : '');
  } else if (os.state === 'on' && proto === 'udp') {
    // UDP: no connection and no statistics, only what the encoder sends
    const kbps = mo.kbps > 0 ? t('unit.mbps', { v: num(mo.kbps / 1000, 1) }) : '';
    setStat('st-srt', t('conn.sending', { proto: protoLabel(proto) }), [codec, kbps].filter(Boolean).join(' · '), 'ok');
  } else if (svc.phase === 'streaming' && st.pendingRestart) {
    // the saved mode is not the running one yet: cannot tell whether it waits for a client or the relay
    setStat('st-srt', t('phase.noConnection'), t('srt.pending'), '');
  } else if (svc.phase === 'streaming') {
    setStat('st-srt', mode === 'listener' ? t('srt.waitingClient') : t('common.connecting'),
      mode === 'listener' ? t('srt.nobodyReading') : t('srt.noRelay'), mode === 'listener' ? '' : 'warn');
  } else {
    setStat('st-srt', '—', '', '');
  }

  // Board
  const sys = st.system;
  const temp = sys.tempC && sys.tempC.cpu;
  const cpu = sys.cpuPct && sys.cpuPct.length ? Math.round(sys.cpuPct.reduce((a, b) => a + b, 0) / sys.cpuPct.length) : null;
  const parts = [];
  if (sys.power) parts.push(t('board.watts', { w: num(sys.power.inMw / 1000, 1) }));
  if (cpu !== null) parts.push(t('board.cpu', { pct: cpu }));
  let sub = parts.join(' · ');
  let subCls = '';
  if (sys.undervoltage && sys.power) {
    sub = t('board.lowPower', { v: num(sys.power.inMv / 1000, 2) });
    subCls = 'warn';
  }
  setStat('st-sys', temp ? t('board.temp', { c: num(temp, 0) }) : '—', sub, temp >= 80 ? 'bad' : temp >= 70 ? 'warn' : '', subCls);

  renderUplinks(st, running);
  maybeReloadDests();
  renderWiFi(st.network || null, st.wifi || null);
  renderCameras(st.network || null);
  for (const fn of hooks.status) fn(st);

  // Only visible when there is no picture
  if (!state.rtcError) {
    $('preview-empty').textContent = svc.state === 'inactive' ? t('live.stopped')
      : svc.state === 'failed' ? t('live.failed')
      : svc.phase === 'waiting_device' ? t('signal.noDevice')
      : sg.present || st.audio.state === 'test' ? t('common.connecting') : t('signal.none');
  }
}

const REASONS = ['no_destination', 'no_uplinks', 'relay_unreachable', 'rejected', 'tls_failed', 'clock_not_synced', 'connection_lost', 'encoder_error', 'internal'];

// One button: Go live ↔ Stop streaming (the preview always goes on). With the mixer the engine's mode
// counts (output.mode, the same as status.output): Go live from the encoder's screen shows at once,
// without waiting for the 2 s poll
function renderLiveBtn() {
  if (!state.status) return;
  const emitting = outputOn();
  const live = $('btn-live');
  live.textContent = emitting ? t('live.stop') : t('live.start');
  live.dataset.action = emitting ? 'off' : 'on';
  live.classList.toggle('primary', !emitting);
  live.classList.toggle('danger', emitting);
}

// Internet uplinks (SRTLA). Only shown if the device has the uplinks dispatcher and the stream goes
// (or will go) over SRTLA, or if there is more than one uplink: that is how one finds out it can.
const KINDS = ['ethernet', 'usb', 'wifi', 'modem'];
// The levels of a link's dot (MODEMS.md §5); its codes are translated as health.<code>
const HEALTH = ['good', 'weak', 'bad', 'off'];
const kindLabel = (k) => (KINDS.includes(k) ? t('kind.' + k) : k);

// --- Network cameras (F7, NETWORK_SOURCES.md §6.7 and §6.11) ---------------------------------
// They come in through the encoder's Wi-Fi and/or by cable (the eth0 network). The state comes without
// secrets with every /status. The URLs with their key and the Wi-Fi password are only requested when
// «How to connect» or «Wi-Fi network» is opened (GET /network/credentials) and wiped when closed.

// After a change, without waiting for the next SSE status (every 2 s)
async function refreshStatus() {
  try { renderStatus(await api('GET', '/status')); } catch (_) { /* it will come through the SSE */ }
}

const ON_STATES = ['on', 'starting', 'off', 'error'];
const apState = (s) => (ON_STATES.includes(s) ? t('ap.' + s) : s);
const lanState = (s) => (ON_STATES.includes(s) ? t('lan.' + s) : s);

function camMeta(c) {
  if (!c.connected) return [t('cams.waiting'), ''];
  const via = c.via === 'wifi' ? t('cams.byWifi') : c.via === 'lan' ? t('cams.byLan') : '';
  const p = [(c.protocol || '').toUpperCase(), via, c.codec === 'h265' ? 'H.265' : c.codec === 'h264' ? 'H.264' : ''];
  if (c.kbps != null) p.push(t('unit.mbps', { v: num(c.kbps / 1000, 1) }));
  if (c.rttMs != null) p.push(t('unit.rtt', { ms: Math.round(c.rttMs) }));
  if (c.lossPct) p.push(t('cams.lost', { pct: num(c.lossPct, 1) }));
  return [t('cams.connected', { details: p.filter(Boolean).join(' · ') }), 'ok'];
}

function renderCameras(net) {
  const card = $('cams-card');
  card.hidden = !net;
  if (!net) return;
  // RTMP/SRT reception (§6.12). Off, what only serves to receive is hidden (how they come in, the cameras,
  // adding and «How to connect»); the board's Wi-Fi has its own card and does not change. A /status from an
  // older app lacks the field: on
  const on = net.enabled !== false;
  if (!$('cams-on').disabled) $('cams-on').checked = on;
  for (const id of ['cams-via', 'cams-list', 'cams-add', 'cams-tip']) $(id).hidden = !on;
  const off = $('cams-offnote');
  off.hidden = on;
  if (!on) {
    const items = net.cameras.items;
    const saved = items.map((c) => t('common.quoted', { s: c.name })).join(', ');
    off.textContent = t('cams.offNote') + (!saved ? '' : ' ' + t('cams.offSaved', { n: items.length, names: saved }));
    closeConnect();
  }
  const ap = net.accessPoint;
  const lan = net.lan || { enabled: false, state: 'off', iface: 'eth0', ip: null };
  const items = net.cameras.items;
  // Through the encoder's Wi-Fi: they come in with «Create my Wi-Fi» on (the «Wi-Fi» card); cameras do not
  // turn it on (WIFI.md W3). A /status from an older app lacks policy: this switch drove the Wi-Fi
  const apAsked = ap.policy ? ap.policy === 'always' : ap.enabled;
  const apTxt = !ap.enabled ? t('cams.viaOff')
    : !apAsked ? t('cams.apAskedOff')
      : ap.state === 'on' ? t('cams.apReceiving', { ssid: ap.ssid || '' }) : apState(ap.state);
  $('cams-ap').textContent = apTxt;
  $('cams-ap').className = 'sub' + (ap.enabled && (!apAsked || ap.state === 'error') ? ' warn' : '');
  const where = `${lan.iface} · ${lan.ip || t('cams.noIp')}`;
  let lanTxt = !lan.enabled ? t('cams.lanOff', { where }) : lan.ip ? `${lanState(lan.state)} · ${where}` : t('cams.lanNoIp', { iface: lan.iface });
  if (lan.enabled && lan.state === 'off' && !items.length) lanTxt = t('cams.lanOnAdd', { where });
  $('cams-lan').textContent = lanTxt;
  $('cams-lan').className = 'sub' + (lan.state === 'error' || (lan.enabled && !lan.ip) ? ' warn' : '');
  const hint = $('cams-lan-hint');
  hint.hidden = !lan.enabled || !on;
  hint.textContent = lan.ip ? t('cams.lanHint', { ip: lan.ip }) : t('cams.lanNoIpHint', { iface: lan.iface });
  // While a change is applied, the switch is not repainted (it would jump under the finger)
  for (const [id, on] of [['cams-via-wifi', ap.enabled], ['cams-via-lan', lan.enabled]]) {
    if (!$(id).disabled) $(id).checked = on;
  }
  const errs = [];
  if (on && lan.state === 'error') errs.push(t('cams.lanError', { error: lan.error || t('common.unknownError') }));
  // If turning it off was not applied, the app retries every 2 min: show it
  if (!on && lan.state === 'on') errs.push(t('cams.lanStopPending'));
  $('cams-err').hidden = !errs.length;
  $('cams-err').textContent = errs.join(' ');
  if (on) $('cams-add').hidden = items.length >= net.cameras.max;
  // Rows IN PLACE (like the uplinks): repainting every 2 s would move the buttons under the finger
  const ul = $('cams-list');
  const ids = items.map((c) => c.id).join(' ');
  if (ul.dataset.ids !== ids) {
    ul.dataset.ids = ids;
    ul.replaceChildren(...items.map((c) => {
      const li = document.createElement('li');
      const info = document.createElement('div');
      const name = document.createElement('span');
      name.className = 'name';
      const meta = document.createElement('span');
      meta.className = 'meta';
      info.append(name, meta);
      const acts = document.createElement('div');
      acts.className = 'acts';
      const mk = (label, cls, fn) => {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'btn small ' + cls;
        b.textContent = label;
        b.addEventListener('click', () => fn(b));
        return b;
      };
      acts.append(mk(t('cams.howTo'), '', () => showConnect(c.id)),
        mk(t('cams.newKey'), 'ghost', (b) => rotateCamera(c.id, b)),
        mk(t('cams.remove'), 'danger', (b) => deleteCamera(c.id, b)));
      li.append(info, acts);
      return li;
    }));
  }
  items.forEach((c, i) => {
    const [info] = ul.children[i].children;
    info.firstChild.textContent = c.name;
    const [txt, cls] = camMeta(c);
    info.lastChild.textContent = txt;
    info.lastChild.className = 'meta' + (cls ? ' ' + cls : '');
  });
  if (!items.length) closeConnect();
}

// The board's Wi-Fi (WIFI.md): «Create my Wi-Fi» and its network (name, channel, password and QR). The
// state comes with every /status (network.accessPoint); the client Wi-Fi, with phase W-B
function renderWiFi(net, wf) {
  $('wifi-card').hidden = !net;
  if (!net) return;
  renderWiFiClient(net.accessPoint, wf && wf.client, wf && wf.warning);
  const ap = net.accessPoint;
  // A /status from an older app lacks policy: the cameras' switch drove the Wi-Fi
  const asked = ap.policy ? ap.policy === 'always' : ap.enabled;
  if (!$('wifi-ap').disabled) $('wifi-ap').checked = asked;
  $('wifi-ap').disabled = $('wifi-ap').disabled || !ap.policy;
  let txt = apState(ap.state);
  if (ap.state === 'on') txt += ' · ' + t('wifi.apDetails', { ssid: ap.ssid || '', ch: ap.channel || '?', n: ap.clients });
  $('wifi-ap-txt').textContent = txt;
  $('wifi-ap-txt').className = 'sub' + (ap.state === 'error' ? ' warn' : '');
  const errs = [];
  if (ap.state === 'error') errs.push(t('wifi.apError', { error: ap.error || t('common.unknownError') }));
  // If turning it off was not applied, the app retries every 2 min: show it
  if (!asked && ap.state === 'on') errs.push(t('wifi.apStopPending'));
  $('wifi-err').hidden = !errs.length;
  $('wifi-err').textContent = errs.join(' ');
  $('cams-wifi').hidden = !asked;
  $('cams-ssid').textContent = ap.ssid ? `· ${ap.ssid}` : '';
  if (!asked && $('cams-wifi').open) $('cams-wifi').open = false; // and its 'toggle' wipes the password from the page
  renderWiFiName(ap, asked);
}

// The Wi-Fi's name (the access point's SSID, accessPoint.name): generated by the app at its first start
// (EnhancedIRL-XXXX), shown with «Create my Wi-Fi» on or off and changed here with PUT /network/ap {ssid}. A new
// name restarts the Wi-Fi and whoever is on it has to join the new name; with a camera on air over it the app
// refuses (409 on_air, its message). The field is a <div>, not a <form>: in the wizard the card lives inside
// #setup-form, and Enter must rename, not move on to the next step
const SSID_RE = /^[A-Za-z0-9_.-]([A-Za-z0-9 _.-]{0,30}[A-Za-z0-9_.-])?$/; // netcfg.ValidSSID
let wifiRenaming = false;

function renderWiFiName(ap, asked) {
  const box = $('wifi-name');
  box.hidden = !ap.name;
  if (!ap.name) return;
  const inp = $('wifi-name-in');
  const prev = inp.dataset.saved;
  inp.dataset.saved = ap.name;
  // The refresh every few seconds does not touch it while it is being edited
  if (prev === undefined || (document.activeElement !== inp && inp.value === prev)) inp.value = ap.name;
  $('wifi-name-hint').textContent = t(asked && ap.state !== 'off' ? 'wifi.nameHintOn' : 'wifi.nameHintOff');
  $('wifi-name-save').disabled = wifiRenaming || inp.value.trim() === ap.name;
}

// The field's problem now ('' if none), shown under it
function wifiNameCheck() {
  const inp = $('wifi-name-in');
  const v = inp.value.trim();
  const msg = !v ? t('wifi.nameEmpty') : SSID_RE.test(v) ? '' : t('wifi.nameBad');
  $('wifi-name-err').textContent = msg;
  $('wifi-name-err').hidden = !msg;
  inp.setAttribute('aria-invalid', msg ? 'true' : 'false');
  $('wifi-name-save').disabled = wifiRenaming || v === inp.dataset.saved;
  return msg;
}

async function renameWiFi() {
  const inp = $('wifi-name-in');
  const ssid = inp.value.trim();
  if (wifiRenaming || ssid === inp.dataset.saved) return;
  if (wifiNameCheck()) { inp.focus(); return; }
  const ap = ((state.status || {}).network || {}).accessPoint || {};
  const up = ap.state === 'on' || ap.state === 'starting';
  if (up && ap.clients && !confirm(t('wifi.renameConfirm', { ssid, n: ap.clients }))) return;
  wifiRenaming = true;
  $('wifi-name-save').disabled = true;
  toast(t('wifi.renaming'));
  try {
    const r = await api('PUT', '/network/ap', { ssid });
    inp.value = ssid;
    inp.dataset.saved = ssid;
    const a = r.accessPoint || {};
    if (a.state === 'error') toast(t('common.savedNotApplied', { error: a.error || t('common.unknownError') }), true);
    else toast(up ? t('wifi.renamed', { ssid }) : t('wifi.renamedOff', { ssid }));
  } catch (e) {
    const msg = e.data && e.data.fields ? (e.data.fields.ssid || Object.values(e.data.fields).join(' · ')) : e.message;
    $('wifi-name-err').textContent = msg;
    $('wifi-name-err').hidden = false;
    toast(msg, true);
  } finally {
    wifiRenaming = false;
    await refreshStatus();
    if ($('cams-wifi').open) showWifi(); // its password and QR, with the new name
  }
}

$('wifi-name-in').addEventListener('input', wifiNameCheck);
$('wifi-name-in').addEventListener('keydown', (ev) => {
  if (ev.key !== 'Enter') return;
  ev.preventDefault(); // inside the wizard's form, Enter would submit the step
  renameWiFi();
});
$('wifi-name-save').addEventListener('click', renameWiFi);

// «Create my Wi-Fi». Turning it off warns who gets disconnected (the Wi-Fi's devices, cameras included)
async function setWiFiAP(cb) {
  const on = cb.checked;
  const ap = ((state.status || {}).network || {}).accessPoint || {};
  if (!on && ap.clients && !confirm(t('wifi.apOffConfirm', { n: ap.clients }))) {
    cb.checked = true;
    return;
  }
  cb.disabled = true;
  toast(on ? t('wifi.turningOn') : t('wifi.turningOff'));
  try {
    const r = await api('PUT', '/network/wifi', { ap: on ? 'always' : 'rescue' });
    if (r.ap && r.ap.state === 'error') toast(t('common.savedNotApplied', { error: r.ap.error || t('common.unknownError') }), true);
    else toast(on ? t('wifi.apOnDone') : t('wifi.apOffDone'));
  } catch (e) {
    cb.checked = !on;
    toast(e.message, true);
  } finally {
    cb.disabled = false;
    await refreshStatus();
  }
}

$('wifi-ap').addEventListener('change', (ev) => setWiFiAP(ev.target));

// --- The client Wi-Fi (WIFI.md §4, W-B): «Connect to a Wi-Fi», its state and the list of networks. The
// password only lives in the form field while it is typed: it is wiped when sent or when the list closes,
// and goes straight to the app (which hands it to NetworkManager)
const CLIENT_ERRORS = ['wrong_password', 'not_found', 'connect_failed', 'unavailable'];

function renderWiFiClient(ap, cl, warning) {
  $('wifi-client-row').hidden = !cl;
  if (!cl) { $('wifi-nets').hidden = true; $('wifi-warn').hidden = true; return; }
  if (!$('wifi-client').disabled) $('wifi-client').checked = cl.enabled;
  let txt = t('wifi.clientOff');
  if (cl.enabled) {
    switch (cl.state) {
      case 'connected': {
        const p = [t('common.quoted', { s: cl.ssid }), t('wifi.signal', { pct: cl.signal })];
        if (cl.channel) p.push(t('wifi.channelBand', { ch: cl.channel, band: cl.band }));
        if (cl.internet === false) p.push(t('wifi.noInternet'));
        txt = t('wifi.clientConnected', { details: p.join(' · ') });
        break;
      }
      case 'connecting': txt = cl.ssid ? t('wifi.connectingTo', { ssid: cl.ssid }) : t('common.connecting'); break;
      case 'error': txt = CLIENT_ERRORS.includes(cl.error) ? t('wifi.err.' + cl.error) : t('common.withError'); break;
      default: txt = t('wifi.notConnected');
    }
  }
  $('wifi-client-txt').textContent = txt;
  $('wifi-client-txt').className = 'sub' + (cl.state === 'error' || cl.internet === false ? ' warn' : '');
  // Both at once on different channels: the radio is shared (WIFI.md W2). The rule is the app's (warning)
  const two = warning === 'two_channels';
  $('wifi-warn').hidden = !two;
  $('wifi-warn').textContent = two ? t('wifi.twoChannels', { ap: ap.channel, cl: cl.channel }) : '';
  $('wifi-nets').hidden = !cl.enabled;
  if (!cl.enabled && $('wifi-nets').open) $('wifi-nets').open = false;
}

async function setWiFiClient(cb) {
  const on = cb.checked;
  const cl = ((state.status || {}).wifi || {}).client || {};
  if (!on && cl.state === 'connected' && !confirm(t('wifi.clientOffConfirm', { ssid: cl.ssid }))) {
    cb.checked = true;
    return;
  }
  cb.disabled = true;
  try {
    await api('PUT', '/network/wifi', { client: on });
    toast(on ? t('wifi.clientOnDone') : t('wifi.clientOffDone'));
  } catch (e) {
    cb.checked = !on;
    toast(e.message, true);
  } finally {
    cb.disabled = false;
    await refreshStatus();
  }
}

$('wifi-client').addEventListener('change', (ev) => setWiFiClient(ev.target));

const SECURITY = ['open', 'wep', 'wpa', 'wpa2', 'wpa3', 'enterprise'];
const secLabel = (s) => (SECURITY.includes(s) ? t('wifi.sec.' + s) : s);
let netsTimer = 0;

// The list: when opened and every 3 s while open (the app scans in the background); the row with the
// open form is not repainted (what was typed would be lost)
async function loadNets() {
  clearTimeout(netsTimer);
  if (!$('wifi-nets').open) return;
  const body = $('wifi-nets-body');
  try {
    const r = await api('GET', '/network/wifi/scan');
    if (!body.querySelector('form')) renderNets(r.networks);
    $('wifi-nets-sub').textContent = '· ' + t('wifi.netCount', { n: r.networks.length });
  } catch (e) {
    // With the form open, nothing: the half-typed password would be lost
    if (!body.querySelector('form')) body.textContent = e.data && e.data.error === 'scan_busy' ? t('wifi.scanning') : e.message;
  }
  netsTimer = setTimeout(loadNets, 3000);
}

function renderNets(nets) {
  const ul = document.createElement('ul');
  ul.className = 'wifi-nets';
  for (const n of nets) {
    const li = document.createElement('li');
    const info = document.createElement('div');
    const name = document.createElement('span');
    name.className = 'name';
    name.textContent = n.ssid;
    const meta = document.createElement('span');
    meta.className = 'meta' + (n.connected ? ' ok' : '');
    meta.textContent = [n.connected ? t('wifi.netConnected') : n.known ? t('wifi.netKnown') : '', secLabel(n.security),
      t('wifi.signal', { pct: n.signal }), t('wifi.bandChannel', { band: n.band, ch: n.channel })].filter(Boolean).join(' · ');
    info.append(name, meta);
    const acts = document.createElement('div');
    acts.className = 'acts';
    const ok = ['open', 'wpa', 'wpa2'].includes(n.security);
    if (ok && !n.connected) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'btn small';
      b.textContent = t('wifi.connect');
      // Known or open: straight away (with its saved profile); otherwise ask for the password
      b.addEventListener('click', () => (n.known || n.security === 'open') ? connectNet(n.ssid, null, b) : askPassword(li, n.ssid));
      acts.append(b);
    }
    if (n.known) {
      const f = document.createElement('button');
      f.type = 'button';
      f.className = 'btn small ghost';
      f.textContent = t('wifi.forget');
      f.addEventListener('click', () => forgetNet(n.ssid, f));
      acts.append(f);
    }
    li.append(info, acts);
    ul.append(li);
  }
  $('wifi-nets-body').replaceChildren(nets.length ? ul : document.createTextNode(t('wifi.noNets')));
}

function askPassword(li, ssid) {
  const old = $('wifi-nets-body').querySelector('form');
  if (old) old.remove();
  const f = document.createElement('form');
  const inp = document.createElement('input');
  inp.type = 'password';
  inp.autocomplete = 'off';
  inp.maxLength = 64;
  inp.placeholder = t('wifi.pwPlaceholder', { ssid });
  inp.setAttribute('aria-label', t('wifi.pwAria', { ssid }));
  const go = document.createElement('button');
  go.type = 'submit';
  go.className = 'btn small primary';
  go.textContent = t('wifi.connect');
  const no = document.createElement('button');
  no.type = 'button';
  no.className = 'btn small ghost';
  no.textContent = t('common.cancel');
  no.addEventListener('click', () => { inp.value = ''; f.remove(); });
  f.addEventListener('submit', (ev) => {
    ev.preventDefault();
    const pw = inp.value;
    inp.value = '';
    f.remove();
    connectNet(ssid, pw, go);
  });
  f.append(inp, go, no);
  li.append(f);
  inp.focus();
}

async function connectNet(ssid, password, b) {
  b.disabled = true;
  toast(t('wifi.connectingTo', { ssid }));
  try {
    await api('POST', '/network/wifi/connect', password == null ? { ssid } : { ssid, password });
  } catch (e) {
    toast(e.data && e.data.fields ? Object.values(e.data.fields).join(' · ') : e.message, true);
  } finally {
    b.disabled = false;
    await refreshStatus();
  }
}

async function forgetNet(ssid, b) {
  if (!confirm(t('wifi.forgetConfirm', { ssid }))) return;
  b.disabled = true;
  try {
    await api('POST', '/network/wifi/forget', { ssid });
    toast(t('wifi.forgotten', { ssid }));
  } catch (e) {
    toast(e.message, true);
  } finally {
    b.disabled = false;
    loadNets();
    await refreshStatus();
  }
}

$('wifi-nets').addEventListener('toggle', () => {
  if ($('wifi-nets').open) loadNets();
  else { clearTimeout(netsTimer); $('wifi-nets-body').replaceChildren(); }
});

function copyRow(label, value) {
  const wrap = document.createElement('div');
  const l = document.createElement('span');
  l.className = 'copylabel';
  l.textContent = label;
  const row = document.createElement('div');
  row.className = 'copyrow';
  const inp = document.createElement('input');
  inp.type = 'text';
  inp.readOnly = true;
  inp.value = value;
  inp.setAttribute('aria-label', label);
  inp.addEventListener('focus', () => inp.select());
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn small';
  b.textContent = t('common.copy');
  b.addEventListener('click', () => copyText(inp));
  row.append(inp, b);
  wrap.append(l, row);
  return wrap;
}

// Copy: the modern clipboard needs HTTPS (the device's web is plain http on the LAN): without it, the
// text is selected and the old method is used
async function copyText(inp) {
  try {
    if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(inp.value);
    else { inp.focus(); inp.select(); if (!document.execCommand('copy')) throw new Error(); }
    toast(t('common.copied'));
  } catch (_) {
    inp.focus();
    inp.select();
    toast(t('common.copyManual'), true);
  }
}

function qrImg(src, alt) {
  const img = document.createElement('img');
  img.className = 'qr';
  img.alt = alt;
  img.src = src + (src.includes('?') ? '&' : '?') + 't=' + Date.now();
  return img;
}

function closeConnect() {
  const box = $('cams-connect');
  box.hidden = true;
  box.replaceChildren();
  delete box.dataset.id;
}

async function showConnect(id) {
  const box = $('cams-connect');
  if (box.dataset.id === id && !box.hidden) { closeConnect(); return; }
  try {
    const cr = await api('GET', '/network/credentials');
    const c = cr.cameras.find((x) => x.id === id);
    if (!c) return;
    const net = state.status.network || {};
    const apOn = !net.accessPoint || net.accessPoint.enabled !== false;
    const lan = net.lan || {};
    const para = (text) => {
      const p = document.createElement('p');
      p.className = 'cloud-text';
      p.textContent = text;
      return p;
    };
    const head = (text) => {
      const h = document.createElement('h4');
      h.textContent = text;
      return h;
    };
    // One way's URLs (Wi-Fi or cable) and their QR
    const urls = (u, via) => [
      copyRow(t('cams.urlSrt'), u.srt),
      qrImg(`/api/v1/cameras/${encodeURIComponent(id)}/qr.svg?proto=srt&via=${via}`, t('cams.qrSrt')),
      copyRow('RTMP', u.rtmp),
      copyRow(t('cams.rtmpServer'), u.rtmpServer),
      copyRow(t('cams.rtmpKey'), u.rtmpKey)];
    const parts = [];
    const both = apOn && lan.enabled;
    if (apOn) {
      if (both) parts.push(head(t('cams.headWifi')));
      parts.push(para(cr.wifi ? t('cams.stepsWifi', { ssid: cr.wifi.ssid }) : t('cams.putUrl')), ...urls(c, 'wifi'));
    }
    if (lan.enabled) {
      if (both) parts.push(head(t('cams.headLan')));
      if (c.lan) parts.push(para(t('cams.stepsLan')), ...urls(c.lan, 'lan'));
      else parts.push(para(t('cams.lanNoIpReopen', { iface: lan.iface || 'eth0' })));
    }
    const close = document.createElement('div');
    close.className = 'form-actions';
    const cb = document.createElement('button');
    cb.type = 'button';
    cb.className = 'btn ghost';
    cb.textContent = t('common.close');
    cb.addEventListener('click', closeConnect);
    close.append(cb);
    box.replaceChildren(...parts, close);
    box.dataset.id = id;
    box.hidden = false;
  } catch (e) {
    toast(e.message, true);
  }
}

async function addCamera(ev) {
  ev.preventDefault();
  const inp = $('cams-name');
  const name = inp.value.trim();
  if (!name) return;
  const btn = ev.submitter || $('cams-add').querySelector('button');
  btn.disabled = true;
  toast(t('cams.adding'));
  try {
    const r = await api('POST', '/cameras', { name });
    inp.value = '';
    const apErr = r.accessPoint && r.accessPoint.state === 'error';
    toast(apErr ? t('cams.addedNoWifi') : t('cams.added', { name }), apErr);
    await refreshStatus();
    showConnect(r.camera.id);
  } catch (e) {
    toast(e.data && e.data.fields ? e.data.fields.name : e.message, true);
  } finally {
    btn.disabled = false;
  }
}

async function rotateCamera(id, b) {
  if (!confirm(t('cams.rotateConfirm'))) return;
  b.disabled = true;
  try {
    await api('PATCH', '/cameras/' + encodeURIComponent(id), { rotateKey: true });
    toast(t('cams.rotated'));
    closeConnect();
    showConnect(id);
  } catch (e) {
    toast(e.message, true);
  } finally {
    b.disabled = false;
  }
}

async function deleteCamera(id, b) {
  const one = (state.status.network || { cameras: { items: [] } }).cameras.items.length <= 1;
  if (!confirm(one ? t('cams.removeLastConfirm') : t('cams.removeConfirm'))) return;
  b.disabled = true;
  try {
    await api('DELETE', '/cameras/' + encodeURIComponent(id));
    closeConnect();
    toast(t('cams.removed'));
    await refreshStatus();
  } catch (e) {
    toast(e.message, true);
    b.disabled = false;
  }
}

async function showWifi() {
  const body = $('cams-wifi-body');
  if (!$('cams-wifi').open) { body.replaceChildren(); return; }
  try {
    const cr = await api('GET', '/network/credentials');
    if (!cr.wifi) { body.textContent = t('wifi.apNotConfigured'); return; }
    const ap = (state.status.network || {}).accessPoint || {};
    const row = document.createElement('div');
    row.className = 'row';
    // The name is changed in its own field of the card (renameWiFi): here, the channel
    const chL = document.createElement('label');
    chL.textContent = t('wifi.channel24');
    const ch = document.createElement('select');
    ch.append(option('auto', t('common.auto'), false), ...[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11].map((n) => option(String(n), String(n), false)));
    ch.value = cr.wifi.channel || 'auto';
    chL.append(ch);
    const save = document.createElement('button');
    save.type = 'button';
    save.className = 'btn';
    save.textContent = t('common.save');
    save.addEventListener('click', () => putAP({ channel: ch.value }, save));
    row.append(chL, save);
    const rot = document.createElement('button');
    rot.type = 'button';
    rot.className = 'btn ghost';
    rot.textContent = t('wifi.newPassword');
    rot.addEventListener('click', () => {
      if (confirm(t('wifi.rotateConfirm'))) putAP({ rotatePassword: true }, rot);
    });
    const acts = document.createElement('div');
    acts.className = 'form-actions';
    acts.append(rot);
    const note = document.createElement('p');
    note.className = 'hint';
    note.textContent = t('wifi.channelInUse', { ch: ap.channel || '—' });
    body.replaceChildren(copyRow(t('wifi.password'), cr.wifi.password),
      qrImg('/api/v1/network/qr/wifi.svg', t('wifi.qrAlt')), row, note, acts);
  } catch (e) {
    toast(e.message, true);
  }
}

async function putAP(body, b) {
  b.disabled = true;
  toast(t('wifi.applying'));
  try {
    await api('PUT', '/network/ap', body);
    toast(t('wifi.saved'));
    await refreshStatus();
    showWifi();
  } catch (e) {
    toast(e.data && e.data.fields ? Object.values(e.data.fields).join(' · ') : e.message, true);
  } finally {
    b.disabled = false;
  }
}

// The «Through Wi-Fi» and «By cable» switches. Turning one off with cameras cuts those coming in that way;
// both at once is not allowed (409 no_input)
async function setVia(which, cb) {
  const on = cb.checked;
  const items = ((state.status.network || {}).cameras || { items: [] }).items.length;
  const ask = !on && items && (which === 'wifi' ? t('cams.viaWifiOffConfirm') : t('cams.viaLanOffConfirm'));
  if (ask && !confirm(ask)) { cb.checked = !on; return; }
  cb.disabled = true;
  toast(on ? t('common.turningOn') : t('common.turningOff'));
  try {
    const r = await api('PUT', which === 'wifi' ? '/network/ap' : '/network/lan', { enabled: on });
    const st = (which === 'wifi' ? r.accessPoint : r.lan) || {};
    if (st.state === 'error') toast(t('common.savedNotApplied', { error: st.error || t('common.unknownError') }), true);
    else toast(which === 'wifi' ? (on ? t('cams.viaWifiOn') : t('cams.viaWifiOff')) : (on ? t('cams.viaLanOn') : t('cams.viaLanOff')));
  } catch (e) {
    cb.checked = !on;
    toast(e.message, true);
  } finally {
    cb.disabled = false;
    closeConnect();
    await refreshStatus();
  }
}

// RTMP/SRT reception. Turning it off warns about what gets cut; streaming with a network camera on air,
// the app refuses (409 on_air) and says so
async function setCamerasOn(cb) {
  const on = cb.checked;
  const net = state.status.network || { cameras: { items: [] } };
  if (!on) {
    const p = state.mixer && state.mixer.program;
    const live = net.cameras.items.filter((c) => c.connected).map((c) => t('common.quoted', { s: c.name }));
    // The same the app checks for 409 on_air (netOnAir): here, only to warn beforehand
    const isNet = (id) => !!id && id.startsWith('net:');
    const a = state.mixer && state.mixer.audio;
    const air = p && (isNet(p.main) || (['pip', 'side'].includes(p.layout) && isNet(p.secondary)));
    const airAudio = a && a.mode !== 'mix' && isNet(a.main);
    const emitting = state.mixer && state.mixer.output && state.mixer.output.mode === 'on';
    const ask = [];
    if (live.length) ask.push(t('cams.offLive', { n: live.length, names: live.join(', ') }));
    if (air) ask.push(t('cams.offOnAir'));
    if (airAudio) ask.push(t('cams.offAudio'));
    // Nothing stops in the audio mix (the other inputs keep playing), but its own input goes away
    if (a && a.mode === 'mix' && a.sources.some((x) => isNet(x.id) && x.inMix)) ask.push(t('cams.offMix'));
    // Streaming with it on air nothing is asked: the app refuses and says what to do
    if (ask.length && !((air || airAudio) && emitting) && !confirm(ask.join(' ') + ' ' + t('cams.offAsk'))) { cb.checked = true; return; }
  }
  cb.disabled = true;
  toast(on ? t('cams.receptionTurningOn') : t('cams.receptionTurningOff'));
  try {
    const r = await api('PUT', '/network', { enabled: on });
    const errs = [r.accessPoint, r.lan].filter((x) => x && x.state === 'error');
    if (errs.length) toast(t('common.savedNotApplied', { error: errs[0].error || t('common.unknownError') }), true);
    else toast(on ? t('cams.receptionOn') : t('cams.receptionOff'));
  } catch (e) {
    cb.checked = !on;
    toast(e.message, true);
  } finally {
    cb.disabled = false;
    await refreshStatus();
  }
}

$('cams-on').addEventListener('change', (ev) => setCamerasOn(ev.target));
$('cams-add').addEventListener('submit', addCamera);
$('cams-wifi').addEventListener('toggle', showWifi);
$('cams-via-wifi').addEventListener('change', (ev) => setVia('wifi', ev.target));
$('cams-via-lan').addEventListener('change', (ev) => setVia('lan', ev.target));

function renderUplinks(st, running) {
  const mods = st.modems || [];
  const list = st.uplinks || [];
  const srtla = st.transport === 'srtla';
  const card = $('uplinks-card');
  // A modem is always shown: its SIM may be asking for the PIN while it is the board's only way out (no uplinks)
  card.hidden = !mods.length && (!st.uplinks || (!srtla && list.length < 2));
  if (card.hidden) return;
  const live = srtla && running && st.output === 'on';
  // RIST bonds the checked links like SRTLA; RTMP leaves through the healthiest one (OUTPUTS.md §4, §6)
  const tr = st.transport;
  $('uplinks-mode').textContent = srtla ? 'SRTLA' : tr === 'rist' ? 'RIST'
    : tr === 'rtmp' || tr === 'rtmps' ? t('uplinks.single1', { proto: tr.toUpperCase() }) : t('uplinks.noSrtla');
  const used = list.filter((u) => (live ? u.inUse : !u.disabled && !u.conflict && !u.noInternet)).length;
  const conflict = list.some((u) => u.conflict);
  const warn = $('uplinks-warn');
  warn.hidden = !conflict && (!srtla || used >= 2);
  warn.textContent = conflict ? t('uplinks.conflict') : used === 0 ? t('uplinks.none') : t('uplinks.single');
  // Rows updated IN PLACE: repainting the list every 2 s would change the checkbox under the finger
  // (a tap would land on an element that no longer exists). Only rebuilt if the uplinks change.
  const ul = $('uplinks');
  const names = list.map((u) => u.name).join(' ');
  if (ul.dataset.names !== names) {
    ul.dataset.names = names;
    ul.replaceChildren(...list.map((u) => {
      const li = document.createElement('li');
      li.dataset.name = u.name;
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.setAttribute('aria-label', t('uplinks.useAria', { name: u.name }));
      cb.addEventListener('change', () => toggleUplink(u.name, cb));
      const info = document.createElement('div');
      const name = document.createElement('span');
      name.className = 'name';
      const dot = document.createElement('i'); // the app's judgement of the link (MODEMS.md §5): its colour
      dot.className = 'hdot';
      dot.hidden = true;
      name.append(dot, document.createElement('span'));
      const meta = document.createElement('span');
      meta.className = 'meta';
      info.append(name, meta);
      const rate = document.createElement('span');
      rate.className = 'rate';
      const more = moreButton();
      li.append(cb, info, rate, more);
      return li;
    }));
  }
  list.forEach((u, i) => {
    const li = ul.children[i];
    const [cb, info, rate, more] = li.children;
    more.hidden = !u.modemId;
    more.dataset.id = u.modemId || '';
    more.setAttribute('aria-label', t('modem.details', { name: u.label || u.name }));
    more.classList.toggle('open', !!u.modemId && state.modemOpen === u.modemId);
    li.className = u.disabled ? 'off' : '';
    if (!cb.disabled) cb.checked = !u.disabled; // disabled = being saved: what was tapped wins
    // A modem: its operator (or alias) as the title, its interface on the second line (MODEMS.md §4.2)
    const [dot, title] = info.firstChild.children;
    title.textContent = u.label || u.name;
    info.lastChild.textContent = u.label ? `${kindLabel(u.kind)} · ${u.name}` : `${kindLabel(u.kind)} · ${u.ip}`;
    const health = HEALTH.includes(u.health) ? u.health : '';
    dot.hidden = !health;
    dot.className = `hdot ${health}`;
    dot.title = health ? t('health.' + (u.healthCode || 'ok')) : '';
    // With the app's judgement, the reason of an amber or red dot; else (an older app) as before. No internet (the
    // client Wi-Fi behind a captive portal): out of the share until it comes back (WIFI.md §13)
    const why = (health === 'weak' || health === 'bad') && u.healthCode && u.healthCode !== 'ok' ? t('health.' + u.healthCode) : '';
    rate.textContent = why || (u.conflict ? t('uplinks.dupIp') : u.disabled ? t('uplinks.disabled') : u.noInternet ? t('uplinks.noInternet')
      : live && !u.inUse ? t('common.connecting') : t('uplinks.rate', { v: num(u.txKbps / 1000, 1) }));
    rate.classList.toggle('warn', why ? health === 'weak' : !!u.conflict || (!u.disabled && !!u.noInternet));
    rate.classList.toggle('bad', !!why && health === 'bad');
  });
  renderModemsOff(st);
  renderModemPanel(st);
}

// --- Cellular modems (MODEMS.md §7) -------------------------------------------------------------------
// A modem row's «›» opens its panel under the list: its signal, SIM and settings. The data come with every /status
// (modems[] and links[]): nothing is asked for while it is open.

function moreButton() {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn small ghost more';
  b.textContent = '›';
  b.hidden = true;
  b.addEventListener('click', () => {
    state.modemOpen = state.modemOpen === b.dataset.id ? '' : b.dataset.id;
    renderUplinks(state.status, true);
  });
  return b;
}

// renderModemsOff: modems without a usable link (no SIM, PIN, searching): not in the share, but they say why.
function renderModemsOff(st) {
  const ul = $('modems-off');
  const mods = st.modems || [];
  const list = (st.links || []).filter((l) => l.kind === 'modem' && !l.available)
    .map((l) => ({ l, m: mods.find((m) => m.iface === l.name || m.id === l.name) })).filter((x) => x.m);
  ul.hidden = list.length === 0;
  const ids = list.map((x) => x.m.id).join(' ');
  if (ul.dataset.ids !== ids) {
    ul.dataset.ids = ids;
    ul.replaceChildren(...list.map(() => {
      const li = document.createElement('li');
      const info = document.createElement('div');
      const name = document.createElement('span');
      name.className = 'name';
      const dot = document.createElement('i');
      dot.className = 'hdot';
      name.append(dot, document.createElement('span'));
      const meta = document.createElement('span');
      meta.className = 'meta';
      info.append(name, meta);
      const rate = document.createElement('span');
      rate.className = 'rate';
      li.append(document.createElement('span'), info, rate, moreButton());
      return li;
    }));
  }
  list.forEach(({ l, m }, i) => {
    const [, info, rate, more] = ul.children[i].children;
    const [dot, title] = info.firstChild.children;
    const health = HEALTH.includes(l.health) ? l.health : 'bad';
    dot.className = `hdot ${health}`;
    title.textContent = m.alias || m.operator || [m.manufacturer, m.model].filter(Boolean).join(' ') || t('kind.modem');
    info.lastChild.textContent = m.imeiTail ? `${t('kind.modem')} · IMEI …${m.imeiTail}` : t('kind.modem');
    rate.textContent = t('health.' + (l.code || 'disconnected'));
    rate.className = 'rate ' + (health === 'off' ? '' : health === 'weak' ? 'warn' : 'bad');
    more.hidden = false;
    more.dataset.id = m.id;
    more.setAttribute('aria-label', t('modem.details', { name: title.textContent }));
    more.classList.toggle('open', state.modemOpen === m.id);
  });
}

// renderModemPanel: the open modem's panel; built when another modem opens, its values refreshed in place (what is
// being typed is kept).
function renderModemPanel(st) {
  const box = $('modem-panel');
  const m = (st.modems || []).find((x) => x.id === state.modemOpen);
  if (!m) {
    box.hidden = true;
    box.replaceChildren();
    delete box.dataset.id;
    return;
  }
  box.hidden = false;
  if (box.dataset.id !== m.id) {
    box.dataset.id = m.id;
    box.replaceChildren(buildModemPanel(m));
  }
  fillModemPanel(box, m, st);
}

function field(labelKey, input, hintKey) {
  const wrap = document.createElement('div');
  wrap.className = 'mp-field';
  const lab = document.createElement('label');
  lab.textContent = t(labelKey);
  lab.htmlFor = input.id;
  wrap.append(lab, input);
  if (hintKey) {
    const h = document.createElement('p');
    h.className = 'hint';
    h.textContent = t(hintKey);
    wrap.append(h);
  }
  return wrap;
}

function buildModemPanel(m) {
  const root = document.createElement('div');
  const head = document.createElement('div');
  head.className = 'mp-head';
  const title = document.createElement('b');
  title.className = 'mp-title';
  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'btn small ghost';
  close.textContent = t('modem.close');
  close.addEventListener('click', () => { state.modemOpen = ''; renderUplinks(state.status, true); });
  head.append(title, close);
  const sig = document.createElement('p');
  sig.className = 'mp-signal';
  const facts = document.createElement('dl');
  facts.className = 'mp-facts';

  // The settings: one form, saved together
  const form = document.createElement('form');
  form.className = 'mp-form';
  const alias = document.createElement('input');
  alias.id = 'mp-alias'; alias.type = 'text'; alias.maxLength = 12; alias.autocomplete = 'off';
  const apn = document.createElement('input');
  apn.id = 'mp-apn'; apn.type = 'text'; apn.maxLength = 63; apn.autocomplete = 'off'; apn.spellcheck = false;
  const mode = document.createElement('select');
  mode.id = 'mp-mode';
  const roam = document.createElement('input');
  roam.id = 'mp-roam'; roam.type = 'checkbox';
  const roamWrap = document.createElement('label');
  roamWrap.className = 'mp-check';
  roamWrap.append(roam, document.createTextNode(' ' + t('modem.roaming')));
  const save = document.createElement('button');
  save.type = 'submit';
  save.className = 'btn small primary';
  save.textContent = t('modem.save');
  form.append(field('modem.alias', alias, 'modem.aliasHint'), field('modem.apn', apn), field('modem.mode', mode), roamWrap, save);
  for (const el of [alias, apn, mode, roam]) el.addEventListener('input', () => { form.dataset.dirty = '1'; });
  form.addEventListener('submit', (ev) => { ev.preventDefault(); saveModem(m.id, form); });

  // The PIN, only while the SIM asks for it
  const pin = document.createElement('form');
  pin.className = 'mp-pin';
  const pinIn = document.createElement('input');
  pinIn.id = 'mp-pinv'; pinIn.type = 'password'; pinIn.inputMode = 'numeric'; pinIn.autocomplete = 'off';
  pinIn.pattern = '[0-9]{4,8}'; pinIn.maxLength = 8; pinIn.required = true;
  const rm = document.createElement('input');
  rm.id = 'mp-pinrm'; rm.type = 'checkbox';
  const rmWrap = document.createElement('label');
  rmWrap.className = 'mp-check';
  rmWrap.append(rm, document.createTextNode(' ' + t('modem.pinRemove')));
  const unlock = document.createElement('button');
  unlock.type = 'submit';
  unlock.className = 'btn small primary';
  unlock.textContent = t('modem.unlock');
  const pinNote = document.createElement('p');
  pinNote.className = 'hint mp-pinnote'; // what happens next time: kept (ModemManager) or asked again (HiLink)
  pin.append(field('modem.pin', pinIn), pinNote, rmWrap, unlock);
  pin.addEventListener('submit', (ev) => { ev.preventDefault(); unlockModem(m.id, pinIn, rm); });

  const acts = document.createElement('div');
  acts.className = 'mp-actions';
  const reset = document.createElement('button');
  reset.type = 'button';
  reset.className = 'btn small';
  reset.textContent = t('modem.reset');
  reset.addEventListener('click', () => modemAction(m.id, 'reset'));
  const subnet = document.createElement('button');
  subnet.type = 'button';
  subnet.className = 'btn small';
  subnet.textContent = t('modem.subnet');
  subnet.title = t('modem.subnetHint');
  subnet.addEventListener('click', () => modemAction(m.id, 'subnet'));
  acts.append(reset, subnet);
  root.append(head, sig, facts, pin, form, acts);
  return root;
}

function fillModemPanel(box, m, st) {
  const can = (x) => (m.can || []).includes(x);
  box.querySelector('.mp-title').textContent = m.alias || m.operator || [m.manufacturer, m.model].filter(Boolean).join(' ');
  const s = m.signal || {};
  const parts = [];
  if (s.rsrp != null) parts.push(`RSRP ${t('modem.dbm', { v: num(s.rsrp, 0) })}`);
  if (s.sinr != null) parts.push(`SINR ${t('modem.db', { v: num(s.sinr, 1) })}`);
  if (s.rsrq != null) parts.push(`RSRQ ${t('modem.db', { v: num(s.rsrq, 0) })}`);
  if (!parts.length && s.rssi != null) parts.push(`RSSI ${t('modem.dbm', { v: num(s.rssi, 0) })}`);
  if (m.tech) parts.push(m.tech.replace('-nsa', ' NSA').toUpperCase());
  box.querySelector('.mp-signal').textContent = parts.length ? parts.join(' · ') : t('modem.noSignal');
  const link = (st.links || []).find((l) => l.name === m.iface || l.name === m.id);
  box.querySelector('.mp-signal').className = 'mp-signal ' + (link ? link.health : '');
  const facts = [
    ['modem.operator', m.operator || '—'],
    ['modem.sim', [t('modem.simState.' + (m.sim || 'unknown')), m.iccidTail ? '…' + m.iccidTail : ''].filter(Boolean).join(' · ')],
    ['modem.registration', t('modem.reg.' + (m.registration || 'unknown'))],
    ['modem.device', [m.manufacturer, m.model, m.imeiTail ? 'IMEI …' + m.imeiTail : '', m.iface].filter(Boolean).join(' · ')],
  ];
  const dl = box.querySelector('.mp-facts');
  dl.replaceChildren(...facts.flatMap(([k, v]) => {
    const dt = document.createElement('dt');
    dt.textContent = t(k);
    const dd = document.createElement('dd');
    dd.textContent = v;
    return [dt, dd];
  }));
  box.querySelector('.mp-pin').hidden = !(can('pin') && (m.sim === 'pin'));
  box.querySelector('.mp-pinnote').textContent = t(m.backend === 'hilink' ? 'modem.pinOnce' : 'modem.pinKeep');
  const form = box.querySelector('.mp-form');
  const mode = box.querySelector('#mp-mode');
  if (!form.dataset.dirty && !form.contains(document.activeElement)) {
    box.querySelector('#mp-alias').value = m.alias || '';
    box.querySelector('#mp-alias').placeholder = m.operator || '';
    box.querySelector('#mp-apn').value = m.apn || '';
    box.querySelector('#mp-apn').placeholder = m.apnAuto ? t('modem.apnAutoKnown', { apn: m.apnAuto }) : t('modem.apnAutoNet');
    box.querySelector('#mp-roam').checked = !!m.roaming;
    const modes = (m.modes && m.modes.length ? m.modes : ['auto']);
    if (mode.dataset.modes !== modes.join(',')) {
      mode.dataset.modes = modes.join(',');
      mode.replaceChildren(...modes.map((x) => {
        const o = document.createElement('option');
        o.value = x;
        o.textContent = t('modem.mode.' + x);
        return o;
      }));
    }
    mode.value = m.mode || 'auto';
    // What the form showed: «Save» sends only what changed (a new name alone must not reconnect the modem)
    form.dataset.orig = JSON.stringify({ alias: m.alias || '', apn: m.apn || '', roaming: !!m.roaming, mode: mode.value });
  }
  box.querySelector('#mp-apn').disabled = !can('apn');
  box.querySelector('#mp-roam').disabled = !can('roaming');
  mode.disabled = !can('mode');
  const [reset, subnet] = box.querySelector('.mp-actions').children;
  reset.hidden = !can('reset');
  subnet.hidden = !can('subnet') || !(st.uplinks || []).some((u) => u.modemId === m.id && u.conflict); // its own link
}

// lastLinkRetry: a 409 last_link asks before cutting the stream, then sends it again with force.
async function lastLinkRetry(fn) {
  try {
    return await fn(false);
  } catch (e) {
    if (e.status === 409 && e.data && e.data.error === 'last_link' && confirm(t('modem.lastConfirm'))) return fn(true);
    throw e;
  }
}

async function saveModem(id, form) {
  let orig = {};
  try { orig = JSON.parse(form.dataset.orig || '{}'); } catch { /* none: everything counts as changed */ }
  const body = {};
  const take = (key, input, value) => {
    if (!input.disabled && value !== orig[key]) body[key] = value; // what the modem cannot change is not sent
  };
  const alias = form.querySelector('#mp-alias');
  const apn = form.querySelector('#mp-apn');
  const roam = form.querySelector('#mp-roam');
  const mode = form.querySelector('#mp-mode');
  take('alias', alias, alias.value.trim());
  take('apn', apn, apn.value.trim());
  take('roaming', roam, roam.checked);
  take('mode', mode, mode.value);
  if (!Object.keys(body).length) {
    delete form.dataset.dirty;
    toast(t('modem.saved'));
    return;
  }
  try {
    await lastLinkRetry((force) => api('PUT', '/modems/' + encodeURIComponent(id), force ? { ...body, force } : body));
    delete form.dataset.dirty;
    toast(t('modem.saved'));
  } catch (e) {
    toast(e.data && e.data.fields ? Object.values(e.data.fields).join(' · ') : e.message, true);
  }
}

async function unlockModem(id, pinIn, rm) {
  const pin = pinIn.value;
  pinIn.value = ''; // out of the page at once: it only goes to the modem
  try {
    await api('POST', '/modems/' + encodeURIComponent(id) + '/pin', { pin, remove: rm.checked });
    toast(rm.checked ? t('modem.pinRemoved') : t('modem.unlocked'));
  } catch (e) {
    toast(e.data && e.data.fields ? Object.values(e.data.fields).join(' · ') : e.message, true);
  }
}

async function modemAction(id, action) {
  if (action === 'reset' && !confirm(t('modem.resetConfirm'))) return;
  if (action === 'subnet' && !confirm(t('modem.subnetConfirm'))) return;
  try {
    await lastLinkRetry((force) => api('POST', '/modems/' + encodeURIComponent(id) + '/' + action, force ? { force } : {}));
    toast(t(action === 'reset' ? 'modem.resetDone' : 'modem.subnetDone'));
  } catch (e) {
    toast(e.message, true);
  }
}

// One uplink at a time: the server changes it on top of what the file already has (an uplink disabled
// and now disconnected stays disabled; two taps in a row do not overwrite each other).
async function toggleUplink(name, cb) {
  const st = state.status;
  const off = !cb.checked;
  // The ones really left: neither disabled, nor with a repeated IP, nor without internet (the tapped one too)
  const left = (st.uplinks || []).filter((u) => (u.name === name ? !off : !u.disabled) && !u.conflict && !u.noInternet).length;
  if (off && left === 0 && st.transport === 'srtla' && onAir() &&
      !confirm(t('uplinks.lastConfirm'))) { cb.checked = true; return; }
  cb.disabled = true;
  try {
    await api('PUT', '/uplinks/' + encodeURIComponent(name), { disabled: off });
    const u = (state.status.uplinks || []).find((x) => x.name === name);
    if (u) u.disabled = off;
    toast(off ? t('uplinks.out', { name }) : t('uplinks.in', { name }));
  } catch (e) {
    cb.checked = !cb.checked;
    toast(e.message, true);
  } finally {
    cb.disabled = false;
    const cur = state.status;
    renderUplinks(cur, cur.service.state === 'active' || cur.service.state === 'activating');
  }
}

// Level meter: −60…0 dBFS to 0…100 %. If no levels arrive (audio stopped), it drops to zero.
const pct = (db) => Math.max(0, Math.min(100, (db + 60) / 60 * 100));

function renderLevel(l) {
  clearTimeout(state.levelTimer);
  for (const [i, ch] of [[0, 'l'], [1, 'r']]) {
    $(`lvl-${ch}-rms`).style.width = (100 - (l ? pct(l.rmsDb[i]) : 0)) + '%';
    $(`lvl-${ch}-peak`).style.left = (l ? pct(l.peakDb[i]) : 0) + '%';
  }
  if (l) state.levelTimer = setTimeout(() => renderLevel(null), 1000);
}

// --- Actions ---------------------------------------------------------------------------------

function toast(msg, isError) {
  const el = $('toast');
  el.textContent = msg;
  el.className = 'toast' + (isError ? ' error' : '');
  el.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { el.hidden = true; }, isError ? 8000 : 4000);
}

// Someone is receiving: only then a confirmation is asked before cutting. UDP has no statistics: its output's state
const onAir = () => !!(state.status && state.status.service.phase === 'streaming' &&
  (state.status.srt || (state.status.outputState && state.status.outputState.state === 'on')));

// The output is on, or trying to be (Go live pressed): switching or applying a destination restarts it
function outputOn() {
  const st = state.status;
  if (!st) return false;
  const running = st.service.state === 'active' || st.service.state === 'activating';
  const mo = state.mixer && state.mixer.output;
  return running && (mo ? mo.mode : st.output) === 'on';
}

async function setOutput(output) {
  if (output === 'off' && onAir() && !confirm(t('live.stopConfirm'))) return;
  try {
    const d = await api('PUT', '/output', { output, apply: true });
    renderDest(d);
    if (!d.restarted) await api('POST', '/service/start'); // it was stopped: start with what is saved
    toast(output === 'on' ? t('live.connecting') : t('live.stoppedToast'));
  } catch (e) {
    if (e.status === 400 && e.data && e.data.fields && e.data.fields.url) {
      toast(t('live.noDestination'), true);
    } else {
      toast(e.message, true);
    }
  }
}

async function serviceAction(action) {
  if (onAir() && !confirm(action === 'stop' ? t('live.stopServiceConfirm') : t('live.restartConfirm'))) return;
  try {
    await api('POST', '/service/' + action);
    toast(action === 'stop' ? t('live.serviceStopped') : t('phase.restarting'));
  } catch (e) { toast(e.message, true); }
}

$('btn-live').addEventListener('click', () => setOutput($('btn-live').dataset.action === 'off' ? 'off' : 'on'));
$('btn-restart').addEventListener('click', () => serviceAction('restart'));
$('pending-apply').addEventListener('click', () => serviceAction('restart'));

// --- EnhancedIRL account (linking) -----------------------------------------------------------
// Only polled with the tab visible: every 3 s while a code waits for confirmation (to switch to «Linked»
// as soon as it is confirmed in the app) and every 15 s otherwise.

const CLOUD_ERRORS = ['limit_reached', 'email_not_verified', 'subscription_required', 'feature_disabled', 'hardware_claimed', 'denied', 'expired', 'revoked', 'unlinked', 'save_failed'];
const CLOUD_HALTED = ['replaced', 'upgrade_required'];

async function loadCloud() {
  clearTimeout(state.cloudTimer);
  try {
    renderCloud(await api('GET', '/cloud?qr=rows'));
  } catch (e) {
    if (e.status === 503) { $('cloud-card').hidden = true; return; } // linking disabled
  }
  if (state.live) state.cloudTimer = setTimeout(loadCloud, state.cloud && state.cloud.state === 'pairing' ? 3000 : 15000);
}

function renderCloud(v) {
  const prev = state.cloud;
  state.cloud = v;
  $('cloud-card').hidden = false;
  $('cloud-unlinked').hidden = v.state !== 'unlinked';
  $('cloud-pairing').hidden = v.state !== 'pairing';
  $('cloud-linked').hidden = v.state !== 'linked';
  const status = $('cloud-status');
  status.className = 'sub';
  if (v.state !== 'pairing') renderCloudQR(null);
  if (v.state === 'unlinked') {
    status.textContent = t('cloud.unlinked');
    const err = $('cloud-error');
    err.textContent = CLOUD_ERRORS.includes(v.lastError) ? t('cloud.err.' + v.lastError) : '';
    err.hidden = !err.textContent;
  } else if (v.state === 'pairing') {
    status.textContent = t('cloud.waiting');
    $('cloud-code').textContent = v.code;
    const link = $('cloud-link');
    link.hidden = !v.linkUrl;
    link.href = v.linkUrl || '#';
    link.textContent = v.linkUrl || '';
    renderCloudQR(v.linkQrRows);
    const exp = new Date(v.expiresAt);
    $('cloud-expires').textContent = isNaN(exp) ? '' : t('cloud.expires', { time: fmtTime(exp) });
  } else {
    $('cloud-account').textContent = v.accountLabel || t('cloud.yourAccount');
    const on = $('cloud-online');
    if (v.halted) {
      on.textContent = CLOUD_HALTED.includes(v.halted) ? t('cloud.halted.' + v.halted) : t('cloud.haltedDefault');
      on.className = 'sub bad';
    } else {
      on.textContent = v.online ? t('cloud.online') : t('cloud.offline');
      on.className = 'sub ' + (v.online ? 'ok' : 'warn');
    }
    const warn = $('cloud-warn');
    warn.textContent = v.lastError === 'save_failed' ? t('cloud.err.save_failed') : '';
    warn.hidden = !warn.textContent;
    status.textContent = v.online ? t('cloud.statusOnline') : t('cloud.statusOffline');
    status.className = 'sub ' + (v.online ? 'ok' : 'warn');
  }
  // Linking or unlinking changes who manages the destination
  if (prev && (prev.state === 'linked') !== (v.state === 'linked')) loadDestination();
}

// The link as a QR, to open it on a phone signed in to EnhancedIRL (linkQrRows: rows of 1 and 0 from the app,
// with ?qr=rows). Drawn as one SVG path with DOM calls; anything that is not a square of 0/1 is ignored
const SVG_NS = 'http://www.w3.org/2000/svg';
function renderCloudQR(rows) {
  const box = $('cloud-qr');
  const n = Array.isArray(rows) ? rows.length : 0;
  const ok = n >= 21 && n <= 177 && rows.every((r) => typeof r === 'string' && r.length === n && /^[01]+$/.test(r));
  box.hidden = !ok;
  if (!ok) { box.replaceChildren(); return; }
  const key = rows.join('');
  if (box.dataset.key === key) return;
  box.dataset.key = key;
  let d = '';
  rows.forEach((r, y) => { for (let x = 0; x < n; x++) if (r[x] === '1') d += `M${x} ${y}h1v1h-1z`; });
  const q = 4; // quiet zone, in modules
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', `${-q} ${-q} ${n + 2 * q} ${n + 2 * q}`);
  svg.setAttribute('shape-rendering', 'crispEdges');
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', t('cloud.qrAria'));
  const bg = document.createElementNS(SVG_NS, 'rect');
  for (const [k, v] of [['x', -q], ['y', -q], ['width', n + 2 * q], ['height', n + 2 * q], ['fill', '#fff']]) bg.setAttribute(k, String(v));
  const path = document.createElementNS(SVG_NS, 'path');
  path.setAttribute('d', d);
  path.setAttribute('fill', '#000');
  svg.append(bg, path);
  box.replaceChildren(svg);
}

async function cloudPair() {
  for (const id of ['cloud-pair', 'cloud-renew']) $(id).disabled = true;
  try {
    renderCloud(await api('POST', '/cloud/pair?qr=rows'));
    loadCloud(); // reschedules the fast poll
  } catch (e) {
    toast(e.message, true);
  } finally {
    for (const id of ['cloud-pair', 'cloud-renew']) $(id).disabled = false;
  }
}

async function cloudUnlink(ask) {
  if (ask) {
    const who = (state.cloud && state.cloud.accountLabel) || t('cloud.yourAccount');
    if (!confirm(t('cloud.unlinkConfirm', { who }))) return;
  }
  try {
    const r = await api('DELETE', '/cloud');
    renderCloud(r);
    if (ask) toast(r.cloudNotified ? t('cloud.unlinkedToast') : t('cloud.unlinkedLocal'), !r.cloudNotified);
    loadDestination();
  } catch (e) {
    toast(e.message, true);
  }
}

$('cloud-pair').addEventListener('click', cloudPair);
$('cloud-renew').addEventListener('click', cloudPair);
$('cloud-cancel').addEventListener('click', () => cloudUnlink(false));
$('cloud-unlink').addEventListener('click', () => cloudUnlink(true));

// --- Destination -----------------------------------------------------------------------------

// The active destination (GET /destination: also the EDID and the output saved) and, with an app that has them, the
// saved destinations
async function loadDestination() {
  loadDestinations();
  loadActiveDest();
}

async function loadActiveDest() {
  try { renderDest(await api('GET', '/destination')); } catch (_) { /* retried when saving */ }
}

function renderDest(d) {
  state.dest = d;
  if (d.edidPreferred) $('f-edid').value = d.edidPreferred;
  const bitrate = (d.adaptiveBitrate ? t('dest.bitrateAdaptive', { min: d.videoMinBitrateKbps, max: d.videoBitrateKbps })
    : t('dest.bitrateFixed', { v: d.videoBitrateKbps })) + ' · ' + t('dest.bitrateAudio', { v: d.audioBitrateKbps });
  const rows = [
    [t('dest.k.url'), d.urlInvalid ? t('dest.urlInvalid') : d.url || t('dest.none')],
    [t('dest.k.mode'), d.transport === 'srtla' ? t('dest.modeSrtla') : d.mode === 'listener' ? t('dest.modeListener') : t('dest.modeCaller')],
    [t('dest.k.latency'), t('unit.ms', { v: d.latencyMs })],
    ['Stream ID', d.streamId.set ? d.streamId.masked : '—'],
    ['Passphrase', d.passphrase.set ? t('dest.passSet') : '—'],
    [t('dest.k.bitrate'), bitrate],
  ];
  if (d.managedBy === 'eirl') rows.unshift([t('dest.k.managed'), t('dest.managedValue')]);
  const dl = $('dest-view');
  dl.replaceChildren(...rows.flatMap(([k, v]) => {
    const dt = document.createElement('dt'); dt.textContent = k;
    const dd = document.createElement('dd'); dd.textContent = v;
    return [dt, dd];
  }));
  $('dest-edit').hidden = d.managedBy === 'eirl' || !$('dest-form').hidden || !!state.dl;
}

function destPlaceholders() {
  const d = state.dest;
  if (!d) return;
  $('f-streamid').placeholder = d.streamId.set ? t('dest.unchanged', { v: d.streamId.masked }) : t('dest.streamIdPlaceholder');
  $('f-pass').placeholder = d.passphrase.set ? t('dest.unchanged', { v: '••••••' }) : t('common.optional');
}

function openForm() {
  const d = state.dest;
  if (!d) return;
  const f = $('dest-form');
  f.reset();
  $('f-url').value = d.url;
  $('f-latency').value = d.latencyMs;
  $('f-vbr').value = d.videoBitrateKbps;
  $('f-abr').value = String(d.audioBitrateKbps);
  $('f-adaptive').checked = d.adaptiveBitrate;
  $('f-vmin').value = d.videoMinBitrateKbps;
  $('f-vmin-row').hidden = !d.adaptiveBitrate;
  destPlaceholders();
  clearErrors();
  f.hidden = false;
  $('dest-view').hidden = true;
  $('dest-edit').hidden = true;
}

function closeForm() {
  const f = $('dest-form');
  f.reset(); // also wipes what was typed in the secret fields
  f.hidden = true;
  $('dest-view').hidden = false;
  $('dest-edit').hidden = (state.dest && state.dest.managedBy === 'eirl') || !!state.dl;
}

function clearErrors() {
  for (const p of document.querySelectorAll('#dest-form .err')) p.hidden = true;
  for (const i of document.querySelectorAll('#dest-form .invalid')) i.classList.remove('invalid');
}

$('dest-edit').addEventListener('click', openForm);
$('f-adaptive').addEventListener('change', () => { $('f-vmin-row').hidden = !$('f-adaptive').checked; });
// When an srtla:// URL is pasted (the dashboard's has no latency): mobile-network latency and adaptive
// bitrate, in sight so they can be changed before saving
$('f-url').addEventListener('input', () => {
  const url = $('f-url').value.trim().toLowerCase();
  if (!url.startsWith('srtla://') || (state.dest && state.dest.transport === 'srtla')) return;
  if (!/[?&]latency=/.test(url) && Number($('f-latency').value) < 2000) $('f-latency').value = 2000;
  if (!$('f-adaptive').checked) { $('f-adaptive').checked = true; $('f-vmin-row').hidden = false; }
});
$('dest-cancel').addEventListener('click', closeForm);

// The toast after saving the destination or the input (same three cases)
function savedToast(d, apply) {
  toast(d.restarted ? t('common.savedRestarting') : apply ? t('common.savedStopped') : t('common.savedLater'));
}

$('dest-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const apply = ev.submitter && ev.submitter.value === 'apply';
  if (apply && onAir() && !confirm(t('common.applyConfirm'))) return;
  clearErrors();
  const body = {
    url: $('f-url').value.trim(),
    latencyMs: Number($('f-latency').value),
    videoBitrateKbps: Number($('f-vbr').value),
    audioBitrateKbps: Number($('f-abr').value),
    adaptiveBitrate: $('f-adaptive').checked,
    videoMinBitrateKbps: Number($('f-vmin').value),
    apply,
  };
  // Secrets: empty = leave as is; checkbox = delete
  if ($('f-streamid-clear').checked) body.streamId = null; else if ($('f-streamid').value) body.streamId = $('f-streamid').value.trim();
  if ($('f-pass-clear').checked) body.passphrase = null; else if ($('f-pass').value) body.passphrase = $('f-pass').value;
  try {
    const d = await api('PUT', '/destination', body);
    renderDest(d);
    closeForm();
    savedToast(d, apply);
  } catch (e) {
    if (e.status === 400 && e.data && e.data.fields) {
      for (const [k, msg] of Object.entries(e.data.fields)) {
        const p = document.querySelector(`#dest-form .err[data-for="${k}"]`);
        if (p) { p.textContent = msg; p.hidden = false; }
        const input = document.querySelector(`#dest-form [name="${k}"]`);
        if (input) input.classList.add('invalid');
      }
    } else {
      $('dest-err').textContent = e.message;
      $('dest-err').hidden = false;
    }
  }
});

// --- Saved destinations (OUTPUTS.md §9.3, §11) ---------------------------------------------------
// Several destinations, one active (what engine.env holds). The list replaces the single destination's card when
// the app has GET /destinations; an older app answers 404 and the card stays as it was (state.dl = null). Secrets
// never come in clear: a set one shows masked, an empty field leaves it as it is and «Remove» clears it.

const PROTOS = ['srt', 'srtla', 'rtmp', 'rtmps', 'rist', 'udp'];
const PROTO_LABELS = { srt: 'SRT', srtla: 'SRTLA', rtmp: 'RTMP', rtmps: 'RTMPS', rist: 'RIST', udp: 'UDP/RTP' };
const protoLabel = (p) => PROTO_LABELS[p] || String(p || '').toUpperCase();
const CODEC_LABELS = { h264: 'H.264', h265: 'H.265' };
const codecLabel = (c) => CODEC_LABELS[c] || '';
// The platforms' names are brands, the same in every language; «Custom» is translated
const TPL_NAMES = { eirl: 'EnhancedIRL', twitch: 'Twitch', youtube: 'YouTube', kick: 'Kick', facebook: 'Facebook' };
const tplName = (id) => (id === 'custom' ? t('dests.tplCustom') : TPL_NAMES[id] || id);
const DESTS_MAX = 16;
// A new destination's latency by protocol (the app's defaults: SRT 200 ms, SRTLA 2000, the RIST buffer 1000)
const LATENCY_DEFAULT = { srt: 200, srtla: 2000, rist: 1000 };
// Protocols that share a server and differ in the scheme (srt ↔ srtla, rtmp ↔ rtmps)
const family = (p) => (p === 'srtla' ? 'srt' : p === 'rtmps' ? 'rtmp' : p);
const enc = encodeURIComponent;

async function loadDestinations() {
  state.dlAt = Date.now();
  let r;
  try {
    r = await api('GET', '/destinations', undefined, { quiet: true });
  } catch (e) {
    if (e.status === 404) destsMode(false); // an older app: the single destination
    else if (e.status === 401) showLogin();
    return;
  }
  renderDests(r);
}

// List mode (true) or the single destination of an older app (false)
function destsMode(on) {
  if (!on) state.dl = null;
  $('dests').hidden = !on;
  if (on) {
    $('dest-view').hidden = true;
    $('dest-form').hidden = true;
    $('dest-edit').hidden = true;
  } else {
    $('dests-add').hidden = true;
    if ($('dest-form').hidden) {
      $('dest-view').hidden = false;
      $('dest-edit').hidden = !!(state.dest && state.dest.managedBy === 'eirl');
    }
  }
}

// force: repaint even if nothing changed (a language change). Without it, the rows are only rebuilt when the list
// changes: a refresh must not move a button under the finger
function renderDests(r, force) {
  const key = JSON.stringify(r);
  const same = !force && state.dl && state.dlKey === key;
  state.dl = r;
  state.dlKey = key;
  destsMode(true);
  const list = r.destinations || [];
  const busy = !$('dd-form').hidden || !$('dests-pick').hidden;
  $('dests-add').hidden = busy || list.length >= DESTS_MAX;
  $('dests-list').hidden = !$('dd-form').hidden;
  $('dests-empty').hidden = list.length > 0 || !$('dd-form').hidden;
  if (same) return;
  $('dests-list').replaceChildren(...list.map(destRow));
}

function smallBtn(label, cls, fn) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn small' + (cls ? ' ' + cls : '');
  b.textContent = label;
  b.addEventListener('click', () => fn(b));
  return b;
}

// The server as a row shows it: without the scheme (the protocol is beside it)
function destServer(d) {
  if (d.urlInvalid) return t('dest.urlInvalid');
  if (!d.url) return t('dests.noServer');
  if (d.mode === 'listener') return t('dests.listening', { port: d.url.replace(/^.*:/, '') });
  return d.url.replace(/^[a-z]+:\/\//i, '');
}

function destRow(d) {
  const li = document.createElement('li');
  li.className = d.active ? 'on' : '';
  const mark = document.createElement('i');
  mark.className = 'dmark';
  const info = document.createElement('div');
  const head = document.createElement('div');
  head.className = 'dname';
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = d.name;
  head.append(name);
  if (d.active) {
    const b = document.createElement('span');
    b.className = 'dbadge';
    b.textContent = t('dests.active');
    head.append(b);
  }
  const meta = document.createElement('span');
  meta.className = 'meta';
  meta.textContent = [protoLabel(d.protocol), codecLabel(d.videoCodec), destServer(d)].filter(Boolean).join(' · ');
  info.append(head, meta);
  if (d.managed) {
    const m = document.createElement('span');
    m.className = 'dmanaged';
    m.textContent = t('dests.managed');
    info.append(m);
  }
  const acts = document.createElement('div');
  acts.className = 'acts';
  if (!d.active) acts.append(smallBtn(t('dests.use'), '', (b) => activateDest(d, b)));
  if (!d.managed) acts.append(smallBtn(t('common.edit'), 'ghost', () => openDestForm(d)));
  li.append(mark, info, acts);
  return li;
}

// A question in the page instead of confirm(): the box's text and its two buttons, true on «yes». A new question
// answers the one before with false
function askInline(box, text, yesLabel, danger) {
  if (box.done) box.done(false);
  box.querySelector('p').textContent = text;
  const yes = box.querySelector('[data-ask="yes"]');
  yes.textContent = yesLabel;
  yes.className = 'btn small ' + (danger ? 'danger' : 'primary');
  box.hidden = false;
  box.scrollIntoView({ block: 'nearest' });
  yes.focus();
  return new Promise((resolve) => {
    box.done = (v) => {
      box.hidden = true;
      box.done = null;
      box.onclick = null;
      resolve(v);
    };
    box.onclick = (ev) => {
      const a = ev.target.closest('[data-ask]');
      if (a) box.done(a.dataset.ask === 'yes');
    };
  });
}

function closeAsk(box) {
  if (box.done) box.done(false);
}

// Activating while on air restarts the output (1-2 s): asked first, in the page. asked: the form already asked
async function activateDest(d, b, asked) {
  if (!asked && outputOn() && !(await askInline($('dests-ask'), t('dests.switchConfirm', { name: d.name }), t('dests.switch')))) return;
  if (b) b.disabled = true;
  try {
    const r = await api('POST', `/destinations/${enc(d.id)}/activate`, { apply: true });
    renderDests(r);
    toast(r.restarted ? t('dests.switchedRestart', { name: d.name }) : t('dests.switched', { name: d.name }));
    loadActiveDest();
    refreshStatus();
  } catch (e) {
    toast(e.message, true);
    loadDestinations();
  } finally {
    if (b) b.disabled = false;
  }
}

// «Add»: first the template (its protocols, server, codecs and maximum), then its form. The cloud's own destination
// is not added by hand while there is one
function openPicker() {
  const r = state.dl;
  if (!r) return;
  const managed = (r.destinations || []).some((d) => d.managed);
  const tpls = (r.templates || []).filter((x) => !(managed && x.id === 'eirl'));
  $('dests-tpls').replaceChildren(...tpls.map((x) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'btn';
    const n = document.createElement('span');
    n.textContent = tplName(x.id);
    const s = document.createElement('small');
    s.textContent = (x.protocols || []).map(protoLabel).join(' · ');
    b.append(n, s);
    b.addEventListener('click', () => openDestForm(null, x.id));
    return b;
  }));
  closeAsk($('dests-ask'));
  $('dests-pick').hidden = false;
  $('dests-add').hidden = true;
  const first = $('dests-tpls').querySelector('button');
  if (first) first.focus();
}

function closePicker() {
  $('dests-pick').hidden = true;
  if (state.dl) renderDests(state.dl);
}

// A template by id; an unknown one (a newer app) allows everything, like «custom»
function tplOf(id) {
  const x = ((state.dl && state.dl.templates) || []).find((v) => v.id === id);
  return x || { id: id || 'custom', protocols: PROTOS, url: '', codecs: ['h264', 'h265'], maxVideoKbps: 20000, keyRequired: false };
}

// The other protocol of the same server: the scheme changes, and an explicit default port goes (RTMP 1935, RTMPS 443)
function swapScheme(url, p) {
  const m = /^([a-z]+):\/\/(.*)$/i.exec(url || '');
  if (!m || m[1].toLowerCase() === p) return url;
  let rest = m[2];
  if (family(p) === 'rtmp') rest = rest.replace(/^([^/?#]*?):(443|1935)(?=[/?#]|$)/, '$1');
  return `${p}://${rest}`;
}

// The template's server for a protocol: its own (urls, e.g. YouTube's RTMP and RTMPS hosts differ), else its
// default one with the protocol's scheme
function tplURL(tpl, p) {
  if (tpl.urls && tpl.urls[p]) return tpl.urls[p];
  return tpl.url ? swapScheme(tpl.url, p) : '';
}

// A name not used yet: «Twitch», «Twitch 2»…
function freeName(base) {
  const used = new Set(((state.dl && state.dl.destinations) || []).map((d) => d.name));
  if (!used.has(base)) return base;
  for (let i = 2; ; i++) if (!used.has(`${base} ${i}`)) return `${base} ${i}`.slice(0, 32);
}

// d: the destination to edit, or null with tplId for a new one
function openDestForm(d, tplId) {
  const tpl = tplOf(d ? d.template : tplId);
  const protos = tpl.protocols && tpl.protocols.length ? tpl.protocols.filter((p) => PROTOS.includes(p)) : PROTOS.slice();
  if (d && d.protocol && !protos.includes(d.protocol)) protos.push(d.protocol);
  const codecs = tpl.codecs && tpl.codecs.length ? tpl.codecs.slice() : ['h264', 'h265'];
  if (d && d.videoCodec && !codecs.includes(d.videoCodec)) codecs.push(d.videoCodec);
  const max = tpl.maxVideoKbps || 20000;
  state.dd = { d, tpl, protos, codecs, max, proto: '', locked: false };
  const f = $('dd-form');
  f.reset();
  clearDestErrors();
  $('dd-proto').replaceChildren(...protos.map((p) => option(p, protoLabel(p))));
  $('dd-codec').replaceChildren(...codecs.map((c) => option(c, codecLabel(c) || c)));
  $('dd-vbr').max = max;
  $('dd-vmin').max = max;
  const p = d ? d.protocol : protos[0];
  $('dd-proto').value = p;
  $('dd-name').value = d ? d.name : freeName(tplName(tpl.id));
  $('dd-url').value = d ? (d.urlInvalid ? '' : d.url || '') : tplURL(tpl, p);
  $('dd-mode').value = d && d.mode === 'listener' ? 'listener' : 'caller';
  $('dd-lat').value = (d && d.latencyMs) || LATENCY_DEFAULT[p] || '';
  $('dd-rprof').value = d && d.ristProfile === 'simple' ? 'simple' : 'main';
  $('dd-user').value = (d && d.username) || '';
  $('dd-codec').value = d && d.videoCodec ? d.videoCodec : codecs[0];
  $('dd-vbr').value = d ? d.videoBitrateKbps : Math.min(6000, max);
  $('dd-vmin').value = d ? d.videoMinBitrateKbps : 1000;
  $('dd-adaptive').checked = d ? !!d.adaptiveBitrate : true;
  $('dd-abr').value = String((d && d.audioBitrateKbps) || 128);
  $('dd-size').value = (d && d.outputSize) || '';
  $('dd-fps').value = d && d.outputFps === 'half' ? 'half' : 'full';
  state.dd.proto = p;
  // The template's server is kept as it is unless the user asks to change it (Kick and custom have none: free)
  state.dd.locked = !!tplURL(tpl, p) && $('dd-url').value === tplURL(tpl, p);
  closeAsk($('dests-ask'));
  $('dests-pick').hidden = true;
  f.hidden = false;
  destLayout();
  renderDests(state.dl);
  f.scrollIntoView({ block: 'nearest' });
  $('dd-name').focus();
}

function closeDestForm() {
  const f = $('dd-form');
  closeAsk($('dd-ask'));
  f.reset(); // also wipes what was typed in the secret fields
  f.hidden = true;
  state.dd = null;
  if (state.dl) renderDests(state.dl);
}

// The secret fields: a set one is shown masked as the placeholder and can be removed; an empty field is not sent
function secretField(input, clearRow, view, placeholder, canClear) {
  const set = !!(view && view.set);
  $(input).placeholder = set ? t('dest.unchanged', { v: view.masked || '••••••' }) : placeholder;
  $(clearRow).hidden = !set || !canClear;
}

// Everything that depends on the protocol (and the language): which fields, their labels, hints and notes
function destLayout() {
  const dd = state.dd;
  if (!dd) return;
  const p = $('dd-proto').value;
  const fam = family(p);
  const d = dd.d;
  const name = tplName(dd.tpl.id);
  for (const g of document.querySelectorAll('#dd-form .dd-g')) g.hidden = !g.dataset.protos.split(' ').includes(p);
  // The passphrase and SRP (user and password) are RIST's main profile only (OUTPUTS.md §6)
  const ristSimple = p === 'rist' && $('dd-rprof').value !== 'main';
  $('dd-srp').hidden = p !== 'rist' || ristSimple;
  if (ristSimple) $('dd-pass-g').hidden = true;
  $('dd-head').textContent = d ? t('dests.editTitle', { name: d.name }) : t('dests.newTitle', { name });
  // The server
  const url = $('dd-url');
  const label = { srt: 'dest.url', rtmp: 'dests.server', rist: 'dests.server', udp: 'dests.address' }[fam] || 'dests.server';
  $('dd-url-label').textContent = t(label);
  url.placeholder = { srt: t('dest.urlPlaceholder'), rtmp: `${p}://live.example.com/app`, rist: 'rist://192.168.1.50:5000', udp: 'udp://192.168.1.50:5000' }[fam] || '';
  url.readOnly = dd.locked;
  $('dd-url-unlock').hidden = !dd.locked;
  const hint = { srt: 'dest.urlHint', rtmp: 'dests.urlRtmpHint', rist: 'dests.urlRistHint', udp: 'dests.urlUdpHint' }[fam];
  const urlHint = $('dd-url-hint');
  if (dd.locked) urlHint.textContent = t('dests.urlLocked', { name });
  else urlHint.replaceChildren(...(hint ? tNodes(hint) : []));
  // Latency (SRT) or buffer (RIST)
  const rist = p === 'rist';
  $('dd-lat-label').textContent = rist ? t('dests.buffer') : t('dest.latency');
  $('dd-lat-hint').textContent = rist ? t('dests.bufferHint') : t('dest.latencyHint');
  $('dd-lat').min = rist ? 50 : 20;
  // Secrets
  secretField('dd-sid', 'dd-sid-clear-row', d && d.streamId, t('dest.streamIdPlaceholder'), true);
  secretField('dd-key', 'dd-key-clear-row', d && d.key, dd.tpl.keyRequired ? t('dests.keyPlaceholder') : t('common.optional'), !dd.tpl.keyRequired);
  secretField('dd-pass', 'dd-pass-clear-row', d && d.passphrase, t('common.optional'), true);
  secretField('dd-pw', 'dd-pw-clear-row', d && d.password, t('common.optional'), true);
  // Video: what the template takes
  const only = dd.codecs.length === 1;
  $('dd-codec').disabled = only;
  $('dd-codec-hint').hidden = !only || dd.tpl.id === 'custom';
  $('dd-codec-hint').textContent = only ? t('dests.codecOnly', { name, codec: codecLabel(dd.codecs[0]) }) : '';
  $('dd-max-hint').hidden = dd.max >= 20000;
  $('dd-max-hint').textContent = t('dests.maxKbps', { name, max: dd.max });
  $('dd-vmin-row').hidden = !$('dd-adaptive').checked;
  // Buttons: the active one saves and applies; another one saves and becomes the active one. The cloud's and the
  // active one are not deleted
  const active = !!(d && d.active);
  $('dd-delete').hidden = !d || active || d.managed;
  $('dd-apply').textContent = active ? t('common.saveApply') : t('dests.saveUse');
  $('dd-apply-hint').hidden = !active;
  destSingleNote();
}

// RTMP, RTMPS and RIST go over one connection: with more than one internet link, say that they do not bond
function destSingleNote() {
  if (!state.dd) return;
  const p = $('dd-proto').value;
  const links = ((state.status && state.status.uplinks) || []).filter((u) => !u.disabled && !u.conflict && !u.noInternet).length;
  const note = $('dd-single');
  note.hidden = !['rtmp', 'rtmps'].includes(p) || links < 2; // RIST bonds the links (OUTPUTS.md §6)
  if (!note.hidden) note.textContent = t('dests.singleLink', { proto: protoLabel(p) });
}

// A protocol picked (or implied by a pasted URL). Same family: the server stays with the new scheme; another one:
// the template's server, or empty. SRTLA (mobile networks) brings its latency and the adaptive bitrate in sight
function switchProto(p, fromUrl) {
  const dd = state.dd;
  const prev = dd.proto;
  if (p === prev) return;
  dd.proto = p;
  $('dd-proto').value = p;
  const url = $('dd-url');
  if (!fromUrl) {
    const own = url.value.trim() && url.value.trim() !== tplURL(dd.tpl, prev);
    if (family(p) === family(prev) && own) url.value = swapScheme(url.value.trim(), p);
    else url.value = tplURL(dd.tpl, p);
    dd.locked = !!tplURL(dd.tpl, p) && url.value === tplURL(dd.tpl, p);
  }
  const lat = $('dd-lat');
  const n = Number(lat.value);
  if (p === 'srtla') {
    if (!(fromUrl && /[?&]latency=/i.test(url.value)) && n < LATENCY_DEFAULT.srtla) lat.value = LATENCY_DEFAULT.srtla;
    $('dd-adaptive').checked = true;
  } else if (LATENCY_DEFAULT[p] && (!n || family(p) !== family(prev))) {
    lat.value = LATENCY_DEFAULT[p];
  }
  destLayout();
}

// A pasted URL: its scheme says the protocol (when the template has it); an SRT one carries its latency and mode
// in the query (the app also takes its stream ID and passphrase from it)
function destUrlInput() {
  const dd = state.dd;
  if (!dd || dd.locked) return;
  const v = $('dd-url').value.trim();
  const m = /^([a-z]+):\/\/([^/?#]*)/i.exec(v);
  if (!m) return;
  const scheme = m[1].toLowerCase();
  const p = scheme === 'rtp' ? 'udp' : scheme;
  if (dd.protos.includes(p)) switchProto(p, true);
  if (family(dd.proto) !== 'srt') return;
  const q = new URLSearchParams(v.includes('?') ? v.slice(v.indexOf('?') + 1) : '');
  const lat = Number(q.get('latency'));
  if (lat > 0) $('dd-lat').value = lat;
  const mode = q.get('mode');
  if (mode === 'caller' || mode === 'listener') $('dd-mode').value = mode;
  else if (dd.proto === 'srt' && /^:\d+$/.test(m[2])) $('dd-mode').value = 'listener';
  else if (dd.proto === 'srt' && m[2] && !m[2].startsWith(':')) $('dd-mode').value = 'caller';
}

// The patch (OUTPUTS.md §9.3): the protocol's fields only. Secrets: absent = unchanged, null = removed
function destBody() {
  const p = $('dd-proto').value;
  const b = {
    name: $('dd-name').value.trim(), protocol: p, url: $('dd-url').value.trim(),
    videoCodec: $('dd-codec').value, videoBitrateKbps: Number($('dd-vbr').value), adaptiveBitrate: $('dd-adaptive').checked,
    videoMinBitrateKbps: Number($('dd-vmin').value), audioBitrateKbps: Number($('dd-abr').value),
    outputSize: $('dd-size').value, outputFps: $('dd-fps').value,
  };
  // UDP has no statistics for the adaptive bitrate to read: a fixed bitrate
  if (p === 'udp') b.adaptiveBitrate = false;
  const secret = (input, clear, key) => {
    if ($(clear).checked) b[key] = null;
    else if ($(input).value.trim()) b[key] = $(input).value.trim();
  };
  const fam = family(p);
  if (fam === 'srt') {
    b.latencyMs = Number($('dd-lat').value);
    b.mode = p === 'srtla' ? 'caller' : $('dd-mode').value;
    secret('dd-sid', 'dd-sid-clear', 'streamId');
    secret('dd-pass', 'dd-pass-clear', 'passphrase');
  } else if (fam === 'rtmp') {
    secret('dd-key', 'dd-key-clear', 'key');
  } else if (p === 'rist') {
    b.latencyMs = Number($('dd-lat').value);
    b.ristProfile = $('dd-rprof').value;
    if (b.ristProfile === 'main') {
      secret('dd-pass', 'dd-pass-clear', 'passphrase');
      b.username = $('dd-user').value.trim();
      secret('dd-pw', 'dd-pw-clear', 'password');
    } else {
      // The simple profile has no passphrase nor user: the saved ones go (the app refuses them with it)
      b.username = '';
      const d = state.dd.d;
      if (d && d.password && d.password.set) b.password = null;
      if (d && d.passphrase && d.passphrase.set) b.passphrase = null;
    }
  }
  return b;
}

function clearDestErrors() {
  for (const p of document.querySelectorAll('#dd-form .err')) p.hidden = true;
  for (const i of document.querySelectorAll('#dd-form .invalid')) i.classList.remove('invalid');
}

// 400 with fields: each message next to its input (the ones without an input, together below)
function showDestErrors(e) {
  const rest = [];
  if (e.status === 400 && e.data && e.data.fields) {
    for (const [k, msg] of Object.entries(e.data.fields)) {
      const p = document.querySelector(`#dd-form .err[data-for="${k}"]`);
      const input = document.querySelector(`#dd-form [name="${k}"]`);
      if (input) input.classList.add('invalid');
      if (p && !p.parentElement.closest('[hidden]')) { p.textContent = msg; p.hidden = false; } else rest.push(msg);
    }
  } else {
    rest.push(e.message);
  }
  $('dd-err').textContent = rest.join(' ');
  $('dd-err').hidden = !rest.length;
  const first = document.querySelector('#dd-form .err:not([hidden])');
  if (first) first.scrollIntoView({ block: 'nearest' });
}

async function saveDest(ev) {
  ev.preventDefault();
  const dd = state.dd;
  if (!dd) return;
  const d = dd.d;
  const go = !!(ev.submitter && ev.submitter.value === 'apply');
  const active = !!(d && d.active);
  const name = $('dd-name').value.trim() || (d && d.name) || tplName(dd.tpl.id);
  // On air, applying the active one or switching to this one restarts the output (1-2 s)
  if (go && outputOn() && !(await askInline($('dd-ask'), active ? t('dests.applyConfirm') : t('dests.switchConfirm', { name }),
    active ? t('dests.apply') : t('dests.switch')))) return;
  clearDestErrors();
  const body = destBody();
  let v;
  try {
    if (d) v = await api('PUT', `/destinations/${enc(d.id)}`, { ...body, apply: go && active });
    else v = await api('POST', '/destinations', { ...body, template: dd.tpl.id });
  } catch (e) {
    showDestErrors(e);
    return;
  }
  closeDestForm();
  if (go && !active) {
    activateDest(v, null, true);
    return;
  }
  if (active) {
    savedToast(v, go);
    loadActiveDest();
  } else {
    toast(t('dests.saved', { name: v.name || name }));
  }
  loadDestinations();
}

async function deleteDest() {
  const d = state.dd && state.dd.d;
  if (!d) return;
  if (!(await askInline($('dd-ask'), t('dests.deleteConfirm', { name: d.name }), t('dests.delete'), true))) return;
  try {
    await api('DELETE', `/destinations/${enc(d.id)}`);
  } catch (e) {
    showDestErrors(e);
    return;
  }
  closeDestForm();
  toast(t('dests.deleted', { name: d.name }));
  loadDestinations();
}

$('dests-add').addEventListener('click', openPicker);
$('dests-pick-cancel').addEventListener('click', closePicker);
$('dd-cancel').addEventListener('click', closeDestForm);
$('dd-form').addEventListener('submit', saveDest);
$('dd-delete').addEventListener('click', deleteDest);
$('dd-proto').addEventListener('change', () => switchProto($('dd-proto').value, false));
$('dd-rprof').addEventListener('change', destLayout);
$('dd-adaptive').addEventListener('change', () => { $('dd-vmin-row').hidden = !$('dd-adaptive').checked; });
$('dd-url').addEventListener('input', destUrlInput);
$('dd-url-unlock').addEventListener('click', () => {
  if (!state.dd) return;
  state.dd.locked = false;
  destLayout();
  $('dd-url').focus();
});
// A secret being typed and «Remove» at once: typing wins
for (const [input, clear] of [['dd-sid', 'dd-sid-clear'], ['dd-key', 'dd-key-clear'], ['dd-pass', 'dd-pass-clear'], ['dd-pw', 'dd-pw-clear']]) {
  $(input).addEventListener('input', () => { if ($(input).value) $(clear).checked = false; });
}

// The active destination can change from the encoder's screen: the list is read again every 20 s while the page
// is live (on each status) and the form is closed
function maybeReloadDests() {
  if (state.dl && $('dd-form').hidden && Date.now() - (state.dlAt || 0) > 20000) loadDestinations();
  destSingleNote();
}

// --- Input (sources) -------------------------------------------------------------------------
// The catalogue (GET /sources) says what is connected and which modes each source supports: the same
// rule the stream applies, so nothing is computed here. The selects are filled with what is saved
// (status.input) and not repainted while someone is changing them.

const RAW_FORMATS = ['NV12', 'YUY2', 'I420', 'UYVY'];
const formatLabel = (f) => (RAW_FORMATS.includes(f) ? t('format.raw') : f === 'MJPG' ? 'MJPEG' : f);
const SOURCE_WARN = ['usb2', 'only_4k', 'no_format', 'unreadable'];

const fpsLabel = (f) => (Number.isInteger(f) ? String(f) : num(f, 2));
const usbSpeed = (s) => (s.usb && s.usb.speedMbps ? (s.usb.speedMbps >= 5000 ? 'USB 3' : 'USB 2') : 'USB');
const choiceLabel = (c) => {
  const [w, h] = c.mode.split('@')[0].split('x');
  return `${w}×${h} @ ${fpsLabel(c.fps)} · ${formatLabel(c.format)}` + (c.outHeight < Number(h) ? ` → ${c.outWidth}×${c.outHeight}` : '');
};

function option(value, label, selected) {
  const o = document.createElement('option');
  o.value = value;
  o.textContent = label;
  if (selected) o.selected = true;
  return o;
}

// force: the «Scan» button (repaints even without changes). Without it, only if the catalogue changes.
async function loadSources(force) {
  let cat;
  try { cat = await api('GET', '/sources'); } catch (_) { return; }
  state.catalogAt = Date.now();
  const changed = JSON.stringify(cat) !== JSON.stringify(state.catalog);
  state.catalog = cat;
  if (!changed && !force) return;
  if (!state.inputDirty) fillInputForm(); else { renderModeOptions(); renderSourceInfo(); }
}

// A camera just plugged in shows up by itself: every 10 s with the page in sight, except while someone
// edits the card (repainting an open select closes it or scrambles it on a phone).
setInterval(() => {
  if (document.hidden || $('app').hidden || state.inputDirty) return;
  if ($('input-form').contains(document.activeElement)) return;
  loadSources();
}, 10000);

function savedInput() {
  return (state.status && state.status.input) || { source: 'hdmi', device: 'auto', format: 'auto', mode: 'auto', audio: 'auto' };
}

function catalogSource(value) {
  const [kind, id] = splitSource(value);
  if (!state.catalog) return null;
  if (kind !== 'usb') return state.catalog.sources.find((s) => s.kind === (kind === 'hdmi' ? 'hdmi-csi' : 'test')) || null;
  if (id === 'auto') return state.catalog.sources.find((s) => s.kind === 'usb' && s.auto) || null;
  return state.catalog.sources.find((s) => s.id === id) || null;
}

function splitSource(v) {
  const i = v.indexOf('|');
  return i < 0 ? [v, ''] : [v.slice(0, i), v.slice(i + 1)];
}

// Fills the three selects with the catalogue and what is saved
function fillInputForm() {
  if (!state.catalog) return;
  const sv = savedInput();
  const cur = sv.source === 'usb' ? `usb|${sv.device}` : sv.source;
  const opts = [];
  for (const s of state.catalog.sources) {
    if (s.kind === 'hdmi-csi') opts.push(option('hdmi', s.name, cur === 'hdmi'));
    if (s.kind === 'usb') opts.push(option(`usb|${s.id}`, `${s.name} · ${usbSpeed(s)}`, cur === `usb|${s.id}`));
  }
  opts.push(option('usb|auto', t('input.usbAuto'), cur === 'usb|auto'));
  if (sv.source === 'usb' && sv.device !== 'auto' && !state.catalog.sources.some((s) => s.id === sv.device)) {
    opts.push(option(cur, t('input.usbSaved'), true));
  }
  opts.push(option('test', t('signal.test'), cur === 'test'));
  if (!opts.some((o) => o.selected) && sv.source === 'hdmi') opts.unshift(option('hdmi', t('input.hdmiMissing'), true));
  $('f-source').replaceChildren(...opts);

  const aopts = [option('auto', t('input.audioAuto'), sv.audio === 'auto'), option('none', t('audio.none'), sv.audio === 'none')];
  // A network camera's audio cannot be the startup one (AUDIO_SOURCE): only in the mixer
  for (const a of state.catalog.audio) if (a.kind !== 'network') aopts.push(option(a.id, a.name, sv.audio === a.id));
  if (!aopts.some((o) => o.selected)) aopts.push(option(sv.audio, t('common.notConnected', { name: sv.audio.replace(/^alsa:/, '') }), true));
  $('f-audio').replaceChildren(...aopts);

  if (sv.edidPreferred) $('f-edid').value = sv.edidPreferred;
  state.inputSaved = JSON.stringify(sv);
  renderModeOptions(sv);
  renderSourceInfo();
}

// Modes of the chosen source. With the saved one, its mode is selected; with another, «Automatic».
function renderModeOptions(sv) {
  const value = $('f-source').value;
  const [kind] = splitSource(value);
  $('usb-fields').hidden = kind !== 'usb';
  $('hdmi-fields').hidden = kind !== 'hdmi';
  if (kind !== 'usb') return;
  const src = catalogSource(value);
  const saved = sv || savedInput();
  const isSaved = saved.source === 'usb' && `usb|${saved.device}` === value;
  const want = isSaved && !(saved.format === 'auto' && saved.mode === 'auto') ? `${saved.format}|${saved.mode}` : 'auto';
  const opts = [option('auto', src && src.auto ? t('input.modeAutoWith', { mode: choiceLabel(src.auto) }) : t('input.modeAuto'), want === 'auto')];
  if (src && src.options && !splitSource(value)[1].startsWith('auto')) {
    for (const c of src.options) opts.push(option(`${c.format}|${c.mode}`, choiceLabel(c), want === `${c.format}|${c.mode}`));
  }
  if (!opts.some((o) => o.selected)) opts.push(option(want, t('common.savedSuffix', { v: want.replace('|', ' · ') }), true));
  $('f-mode').replaceChildren(...opts);
}

function renderSourceInfo() {
  const src = catalogSource($('f-source').value);
  const info = $('source-info');
  const warn = $('source-warn');
  info.hidden = true;
  warn.hidden = true;
  if (!src || src.kind !== 'usb') return;
  const parts = [usbSpeed(src)];
  if (src.audio) parts.push(t('input.hasAudio', { name: src.audio.name }));
  if (src.auto && src.auto.busShare > 0.5) parts.push(t('input.busShare', { pct: Math.round(src.auto.busShare * 100) }));
  info.textContent = parts.join(' · ');
  info.hidden = false;
  const w = (src.warnings || []).filter((k) => SOURCE_WARN.includes(k)).map((k) => t('srcwarn.' + k));
  if (w.length) {
    warn.textContent = w.join(' ');
    warn.hidden = false;
  }
}

// What runs now, if it is not what is saved; and the form up to date if nobody is touching it
function renderInputStatus(st) {
  const inp = st.input || {};
  const el = $('input-active');
  const a = inp.active;
  let txt = '';
  if (a && a.state === 'ready' && a.source === 'usb') {
    txt = t('input.inUse', { name: a.name || 'USB', format: a.format, mode: a.mode.split('@')[0].replace('x', '×') });
  } else if (a && a.state === 'waiting_device') {
    txt = t('input.waitingUsb');
  } else if (inp.edidActive && inp.edidActive !== inp.edidPreferred && inp.source === 'hdmi') {
    txt = t('input.edidActive', { edid: inp.edidActive });
  }
  el.textContent = txt;
  el.hidden = !txt;
  if (!state.inputDirty && state.catalog && JSON.stringify(savedInput()) !== state.inputSaved) fillInputForm();
}

for (const id of ['f-mode', 'f-audio', 'f-edid']) $(id).addEventListener('change', () => { state.inputDirty = true; });
$('f-source').addEventListener('change', () => {
  state.inputDirty = true;
  renderModeOptions();
  renderSourceInfo();
});
$('input-rescan').addEventListener('click', () => loadSources(true).then(() => toast(t('input.rescanned'))));

$('input-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const apply = ev.submitter && ev.submitter.value === 'apply';
  if (apply && onAir() && !confirm(t('common.applyConfirm'))) return;
  for (const p of document.querySelectorAll('#input-form .err')) p.hidden = true;
  const [kind, device] = splitSource($('f-source').value);
  const body = { source: kind, audio: $('f-audio').value, apply };
  if (kind === 'hdmi') body.edidPreferred = $('f-edid').value;
  if (kind === 'usb') {
    const [format, mode] = $('f-mode').value === 'auto' ? ['auto', 'auto'] : splitSource($('f-mode').value);
    Object.assign(body, { device, format, mode });
  }
  try {
    const d = await api('PUT', '/input', body);
    renderDest(d);
    state.inputDirty = false;
    state.inputSaved = ''; // the next status (2 s) brings what is saved and repaints
    savedToast(d, apply);
  } catch (e) {
    if (e.status === 400 && e.data && e.data.fields) {
      for (const [k, msg] of Object.entries(e.data.fields)) {
        const p = document.querySelector(`#input-form .err[data-for="${k}"]`);
        if (p) { p.textContent = msg; p.hidden = false; continue; }
        // The whole configuration is saved: a destination problem also prevents saving the input
        $('input-err').textContent = k === 'url' ? t('input.needDest') : t('input.checkDest', { msg });
        $('input-err').hidden = false;
      }
    } else {
      $('input-err').textContent = e.message;
      $('input-err').hidden = false;
    }
  }
});

// --- Mixer (MIXER.md §5) ---------------------------------------------------------------------
// The same commands as the encoder's touch screen, over the same state: what is touched there arrives
// here through the SSE («mixer» event) and the other way round. Source names come from the device:
// always with textContent, never as HTML.

const MX_STATES = ['starting', 'no_signal', 'unsupported', 'gone', 'error', 'hidden'];
const MX_SCREENS = ['waiting', 'off', 'error'];

function renderMixer(m) {
  state.mixer = m;
  $('mixer-card').hidden = !m;
  $('preview-label').hidden = !m;
  $('input-title').textContent = m ? t('input.titleMain') : t('input.title');
  $('input-mixer').hidden = !m;
  if (!m) { stopMixerImages(); for (const fn of hooks.mixer) fn(null); return; }
  const p = m.program;
  for (const b of document.querySelectorAll('#mixer-card [data-layout]')) b.classList.toggle('active', b.dataset.layout === p.layout);
  $('mixer-fade').textContent = p.transition === 'fade' ? t('mixer.fade') : t('mixer.cut');
  $('mixer-fade').classList.toggle('active', p.transition === 'fade');
  $('mixer-swap').disabled = !p.secondary;
  renderMixerPiP(p);
  renderMixerPause(p.pause);
  const sc = m.screen || {};
  const locked = sc.locked === true;
  $('mixer-screen').textContent = sc.state === 'on'
    ? t('mxscreen.on', { w: sc.width, h: sc.height }) + (sc.touch ? ' · ' + t('mxscreen.touch') : '') + (locked ? ' · ' + t('mxscreen.locked') : '')
    : (MX_SCREENS.includes(sc.state) ? t('mxscreen.' + sc.state) : '');
  $('mixer-screen-box').hidden = sc.state !== 'on';
  // Lock: the screen ignores taps except the unlock one (an older engine does not send it)
  const lock = $('mixer-lock');
  lock.hidden = sc.state !== 'on' || sc.locked == null;
  lock.textContent = locked ? t('mixer.unlock') : t('mixer.lock');
  lock.setAttribute('aria-label', locked ? t('mixer.unlockAria') : t('mixer.lockAria'));
  const grid = $('mixer-sources');
  if (grid.children.length !== m.slots) {
    grid.textContent = '';
    for (let i = 1; i <= m.slots; i++) grid.appendChild(mixerTile(i));
  }
  const bySlot = new Map(m.sources.map((s) => [s.slot, s]));
  const cfgBySlot = new Map((m.slotConfig || []).map((c) => [c.slot, c]));
  for (const tile of grid.children) updateTile(tile, bySlot.get(Number(tile.dataset.slot)), p, cfgBySlot.get(Number(tile.dataset.slot)));
  renderMixerAudio(m.audio, m.sources);
  startMixerImages();
  for (const fn of hooks.mixer) fn(m);
}

// PiP: corner and width of the small window, only with that layout (an older engine does not send it)
function renderMixerPiP(p) {
  const pip = p.pip;
  $('mixer-pip').hidden = !pip || p.layout !== 'pip';
  if (!pip) return;
  for (const b of document.querySelectorAll('#mixer-pip [data-corner], #mixer-pip [data-size]')) {
    const on = b.dataset.corner ? b.dataset.corner === pip.corner : Number(b.dataset.size) === pip.size;
    b.classList.toggle('active', on);
    b.setAttribute('aria-pressed', on ? 'true' : 'false');
  }
}

// Pause: a full-screen card over the program. The preview label turns «PAUSE» (amber). The settings
// are not overwritten while being edited (pauseDirty)
function renderMixerPause(pz) {
  const btn = $('mixer-pause');
  btn.hidden = !pz;
  $('mixer-pause-box').hidden = !pz;
  const label = $('preview-label');
  label.textContent = pz && pz.on ? t('live.paused') : t('live.program');
  label.classList.toggle('paused', !!(pz && pz.on));
  if (!pz) return;
  btn.textContent = pz.on ? t('mixer.unpause') : t('mixer.pause');
  btn.classList.toggle('active', pz.on);
  btn.setAttribute('aria-pressed', pz.on ? 'true' : 'false');
  if (!state.pauseDirty) {
    $('f-pause-text').value = pz.text;
    $('f-pause-mute').checked = pz.mute;
  }
}

// The pause text rule, the same as the app's and the engine's (manager/internal/mixer): without the
// spaces at the ends, 1 to 60 characters, each a letter, a digit, a space or one of PAUSE_PUNCT.
const PAUSE_PUNCT = '.,:;!?¡¿\'"()-–—…·/+#@%€$*'; // i18n-ok: the engine's list of allowed signs
const PAUSE_MAX = 60;

function pauseTextError(text) {
  const chars = [...text];
  if (!chars.length) return t('pause.empty');
  if (chars.length > PAUSE_MAX) return t('pause.tooLong', { max: PAUSE_MAX, n: chars.length });
  const bad = chars.find((c) => !/^[\p{L}\p{Nd}]$/u.test(c) && c !== ' ' && !PAUSE_PUNCT.includes(c));
  if (bad === undefined) return '';
  if (/^\p{C}$/u.test(bad)) return t('pause.control');
  return t('pause.badChar', { c: bad });
}

$('mixer-pause').addEventListener('click', () => {
  const pz = state.mixer && state.mixer.program.pause;
  if (pz) mixerCmd({ cmd: 'pause', on: !pz.on });
});
$('mixer-pause-form').addEventListener('input', () => {
  state.pauseDirty = true;
  $('pause-err').hidden = true;
});
$('mixer-pause-box').addEventListener('toggle', () => {
  if ($('mixer-pause-box').open) return;
  state.pauseDirty = false; // closing without saving discards what was typed
  $('pause-err').hidden = true;
  if (state.mixer) renderMixerPause(state.mixer.program.pause);
});
$('mixer-pause-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  // NFC: an accent typed as letter + combining mark counts as one letter, as in the engine
  const text = $('f-pause-text').value.normalize('NFC').trim();
  const msg = pauseTextError(text);
  const err = $('pause-err');
  err.textContent = msg;
  err.hidden = !msg;
  if (msg) return;
  // Without changing on: saving the settings neither sets nor clears the pause
  if (await mixerCmd({ cmd: 'pause', text, mute: $('f-pause-mute').checked })) {
    state.pauseDirty = false;
    if (state.mixer) renderMixerPause(state.mixer.program.pause);
    toast(t('pause.saved'));
  }
});

// An audio input's name with its camera's number (the same as in «Sources»), if it belongs to one
function audioName(x) {
  const name = fixedName(x.id, x.name);
  return x.videoSlot ? `${x.videoSlot} · ${name}` : name;
}

// Audio on air, independent of the camera (F4). With «One source» (mode single) one input plays: the
// buttons choose which, with «No audio», and with «Follow video», taking a camera on air plays the input
// linked to it (links). With «Mix» (mix) the checked ones play together: each button adds or removes it.
// Both, with their level. An older engine does not send the mode: one plays, as always.
const AUDIO_STATES = ['starting', 'no_signal', 'gone', 'error'];
const AUDIO_DELAY_MAX = 500;
const AUDIO_GAIN_MIN = -40;
const AUDIO_GAIN_MAX = 12;

// Gain in dB, with a real minus sign: «−6 dB», «+3 dB», «0 dB», «−6,5 dB»
function dbLabel(g) {
  const v = num(Math.abs(g), Number.isInteger(g) ? 0 : 1);
  return `${g > 0 ? '+' : g < 0 ? '−' : ''}${v} dB`;
}

function renderMixerAudio(a, sources) {
  $('mixer-audio-box').hidden = !a;
  if (!a) return;
  const mix = a.mode === 'mix';
  $('mixer-amode').hidden = !a.mode;
  for (const b of $('mixer-amode').children) {
    const on = b.dataset.amode === a.mode;
    b.classList.toggle('active', on);
    b.setAttribute('aria-pressed', on ? 'true' : 'false');
  }
  const bar = $('mixer-audio');
  bar.classList.toggle('mix', mix);
  bar.setAttribute('aria-label', mix ? t('mixer.mixAria') : t('mixer.audioAria'));
  // «No audio», only with one source: in the mix it is checking none
  const items = mix ? a.sources : [...a.sources, { id: 'none', name: t('audio.none'), state: 'live' }];
  const key = items.map((x) => x.id).join('|');
  if (bar.dataset.key !== key) {
    bar.dataset.key = key;
    bar.textContent = '';
    for (const x of items) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'btn small mx-abtn';
      b.dataset.id = x.id;
      b.innerHTML = '<span class="mx-aname"></span><span class="mx-asub"></span><span class="mx-lvl"><i></i></span>';
      b.addEventListener('click', async () => {
        if (b.dataset.busy) return; // a second tap with the command on its way would undo it halfway
        b.dataset.busy = '1';
        await audioPick(b.dataset.id);
        delete b.dataset.busy;
      });
      bar.appendChild(b);
    }
  }
  for (const b of bar.children) {
    const x = items.find((i) => i.id === b.dataset.id);
    const on = mix ? x.inMix === true : (a.main || 'none') === x.id;
    b.classList.toggle('active', on);
    b.setAttribute('aria-pressed', on ? 'true' : 'false');
    b.querySelector('.mx-aname').textContent = audioName(x);
    // Not live: the engine's reason when there is one, else the state
    const why = x.id === 'none' || x.state === 'live' ? '' : detailText(x);
    const sub = x.id === 'none' ? [] : [on && (mix ? t('mixer.inMix') : t('mixer.playing')), why || (AUDIO_STATES.includes(x.state) && t('astate.' + x.state)),
      typeof x.gainDb === 'number' && x.gainDb !== 0 && dbLabel(x.gainDb), x.delayMs > 0 && t('mixer.delayPlus', { ms: x.delayMs })];
    b.querySelector('.mx-asub').textContent = sub.filter(Boolean).join(' · ');
    b.title = why;
    const lvl = b.querySelector('.mx-lvl');
    lvl.hidden = x.id === 'none';
    lvl.firstChild.style.width = x.peakDb != null ? `${Math.max(0, Math.min(100, (x.peakDb + 60) / 60 * 100))}%` : '0';
  }
  // Follow video (an older engine does not send it; does nothing with the mix). With a command on its
  // way the checkbox is not touched
  const follow = typeof a.follow === 'boolean' && a.follow && !mix;
  $('mixer-follow-row').hidden = typeof a.follow !== 'boolean' || mix;
  $('mixer-follow-note').hidden = !follow;
  $('mixer-audio-hint').hidden = follow || mix;
  const note = $('mixer-mix-note');
  note.hidden = !mix;
  note.textContent = a.sources.some((x) => x.inMix) ? t('mixer.mixNote') : t('mixer.mixNone');
  const cb = $('mixer-follow');
  if (!cb.dataset.busy) cb.checked = typeof a.follow === 'boolean' && a.follow;
  renderAudioSettings(a, sources);
  renderPhones(a.phones);
}

const rerenderAudio = () => { if (state.mixer) renderMixerAudio(state.mixer.audio, state.mixer.sources); };

// An input button: with one source, the one that plays; with the mix, add or remove it
function audioPick(id) {
  const a = state.mixer && state.mixer.audio;
  if (!a) return null;
  if (a.mode !== 'mix') return mixerCmd({ cmd: 'audio', source: id });
  const x = a.sources.find((i) => i.id === id);
  return x ? mixerCmd({ cmd: 'audio_mix', source: id, on: x.inMix !== true }) : null;
}

for (const b of $('mixer-amode').children) {
  b.addEventListener('click', () => {
    const a = state.mixer && state.mixer.audio;
    if (a && a.mode !== b.dataset.amode) mixerCmd({ cmd: 'audio_mode', mode: b.dataset.amode });
  });
}

$('mixer-follow').addEventListener('change', async () => {
  const cb = $('mixer-follow');
  cb.dataset.busy = '1';
  await mixerCmd({ cmd: 'audio_follow', on: cb.checked });
  delete cb.dataset.busy;
  rerenderAudio(); // if the engine refused it, back to what it says
});

// Audio settings: each input's volume and delay and each camera's audio. What the engine does not send
// (an older one: without gainDb, delayMs or links) does not show. What is being edited is not
// overwritten: a slider being moved (dataset.held), an unsent value (dataset.dirty) nor a control with
// its command on the way (dataset.busy)
function renderAudioSettings(a, sources) {
  const gains = a.sources.filter((x) => typeof x.gainDb === 'number');
  const delays = a.sources.filter((x) => typeof x.delayMs === 'number');
  const links = a.links ? sources.filter((s) => a.links[s.id] !== undefined) : [];
  $('mixer-gain-box').hidden = !gains.length;
  $('mixer-delay-box').hidden = !delays.length;
  $('mixer-links-box').hidden = !links.length;
  $('mixer-audio-set').hidden = !gains.length && !delays.length && !links.length;

  const gl = $('mixer-gains');
  syncRows(gl, gains.map((x) => x.id), gainRow);
  for (const row of gl.children) {
    const x = gains.find((i) => i.id === row.dataset.id);
    row.querySelector('label').textContent = audioName(x);
    const inp = row.querySelector('input');
    if (inp.dataset.held || inp.dataset.dirty || inp.dataset.busy) continue;
    inp.value = String(x.gainDb);
    showGain(row, x.gainDb);
  }

  const dl = $('mixer-delays');
  syncRows(dl, delays.map((x) => x.id), delayRow);
  for (const row of dl.children) {
    const x = delays.find((i) => i.id === row.dataset.id);
    row.querySelector('label').textContent = audioName(x);
    const inp = row.querySelector('input');
    if (inp.dataset.dirty || inp.dataset.busy) continue;
    inp.value = String(x.delayMs);
    inp.classList.remove('invalid');
  }

  // The present audio inputs and «No own audio»; the linked one, even if it has been disconnected
  const opts = [...a.sources.map((x) => [x.id, audioName(x)]), ['none', t('mixer.noOwnAudio')]];
  const ll = $('mixer-links');
  syncRows(ll, links.map((s) => s.id), linkRow);
  for (const row of ll.children) {
    const s = links.find((i) => i.id === row.dataset.id);
    row.querySelector('label').textContent = `${s.slot} · ${srcName(s)}`;
    const sel = row.querySelector('select');
    if (sel.dataset.busy) continue;
    const v = a.links[s.id] || 'none';
    const want = opts.some(([id]) => id === v) ? opts : [...opts, [v, t('common.notConnected', { name: v.replace(/^alsa:/, '') })]];
    const key = want.map((o) => o.join('=')).join('|');
    if (sel.dataset.key !== key) { // only if they change: redoing them closes the open dropdown
      sel.dataset.key = key;
      sel.replaceChildren(...want.map(([id, name]) => option(id, name, id === v)));
    }
    sel.value = v;
  }
}

// One row per id, in order. The list is only redone if the ids change (something is plugged in or
// removed), reusing the rows that stay: what was typed in them is not lost
function syncRows(list, ids, make) {
  const key = ids.join('|');
  if (list.dataset.key === key) return;
  list.dataset.key = key;
  const rows = new Map([...list.children].map((r) => [r.dataset.id, r]));
  list.replaceChildren(...ids.map((id) => rows.get(id) || make(id)));
}

function audioRow(id, cls, control) {
  const row = document.createElement('div');
  row.className = cls;
  row.dataset.id = id;
  const n = ++state.audioRows;
  // Fixed markup only: the names go afterwards with textContent
  row.innerHTML = `<label for="f-audio-row-${n}"></label>${control.replace('%id', `f-audio-row-${n}`)}`;
  applyI18n(row);
  return row;
}

// Delay: sent on release (Enter, leaving the field or the arrows) or after 1 s without typing, not on
// every key. A single command on its way per input; what is typed meanwhile goes afterwards
function delayRow(id) {
  const row = audioRow(id, 'mx-arow', `<span class="mx-ms"><input id="%id" type="number" inputmode="numeric" min="0" max="${AUDIO_DELAY_MAX}" step="10"> ms</span>`);
  const inp = row.querySelector('input');
  inp.addEventListener('input', () => {
    inp.dataset.dirty = '1';
    delayLater(inp, id);
  });
  inp.addEventListener('change', () => sendDelay(inp, id));
  inp.addEventListener('blur', () => sendDelay(inp, id));
  return row;
}

function delayLater(inp, id) {
  clearTimeout(inp.timer);
  inp.timer = setTimeout(() => sendDelay(inp, id), 1000);
}

async function sendDelay(inp, id) {
  clearTimeout(inp.timer);
  if (!inp.dataset.dirty || inp.dataset.busy) return;
  const ms = inp.value === '' ? NaN : Number(inp.value);
  const bad = !Number.isInteger(ms) || ms < 0 || ms > AUDIO_DELAY_MAX;
  inp.classList.toggle('invalid', bad);
  const err = $('delay-err');
  err.textContent = bad ? t('mixer.delayError', { max: AUDIO_DELAY_MAX }) : '';
  err.hidden = !bad;
  if (bad) return;
  delete inp.dataset.dirty;
  const cur = state.mixer && state.mixer.audio && state.mixer.audio.sources.find((x) => x.id === id);
  if (!cur || cur.delayMs !== ms) {
    inp.dataset.busy = '1';
    await mixerCmd({ cmd: 'audio_delay', source: id, ms });
    delete inp.dataset.busy;
    if (inp.dataset.dirty) { delayLater(inp, id); return; } // typed while the command was going
  }
  rerenderAudio();
}

// Volume: heard while dragging (one command every GAIN_EVERY ms at most, never two at once) and the
// last value goes on release. «0 dB» leaves it as it comes
const GAIN_EVERY = 200;

function gainRow(id) {
  const row = audioRow(id, 'mx-arow mx-gain', `<input id="%id" type="range" min="${AUDIO_GAIN_MIN}" max="${AUDIO_GAIN_MAX}" step="1">` +
    '<output aria-hidden="true"></output><button class="btn small ghost mx-db0" type="button" data-i18n-attr="aria-label:mixer.gainResetAria">0 dB</button>');
  const inp = row.querySelector('input');
  inp.addEventListener('input', () => {
    inp.dataset.held = '1';
    inp.dataset.dirty = '1';
    showGain(row, Number(inp.value));
    gainLater(inp, id);
  });
  // On release: change does not come if released on the starting value; pointerup and blur do
  const release = () => {
    if (!inp.dataset.held) return;
    delete inp.dataset.held;
    sendGain(inp, id);
  };
  for (const ev of ['change', 'pointerup', 'pointercancel', 'blur']) inp.addEventListener(ev, release);
  row.querySelector('.mx-db0').addEventListener('click', () => {
    inp.value = '0';
    inp.dataset.dirty = '1';
    showGain(row, 0);
    inp.focus(); // the button hides at 0 dB: the focus is not lost
    sendGain(inp, id);
  });
  return row;
}

// The value (the engine's may carry a decimal), the fill up to the thumb and «0 dB», unless already there
function showGain(row, g) {
  const inp = row.querySelector('input');
  row.querySelector('output').textContent = dbLabel(g);
  inp.setAttribute('aria-valuetext', dbLabel(g));
  inp.style.setProperty('--p', String((Number(inp.value) - AUDIO_GAIN_MIN) / (AUDIO_GAIN_MAX - AUDIO_GAIN_MIN)));
  row.querySelector('.mx-db0').classList.toggle('zero', g === 0);
}

function gainLater(inp, id) {
  if (!inp.timer) inp.timer = setTimeout(() => sendGain(inp, id), GAIN_EVERY);
}

async function sendGain(inp, id) {
  clearTimeout(inp.timer);
  inp.timer = 0;
  if (!inp.dataset.dirty || inp.dataset.busy) return;
  delete inp.dataset.dirty;
  const gain = Number(inp.value);
  const cur = state.mixer && state.mixer.audio && state.mixer.audio.sources.find((x) => x.id === id);
  if (!cur || cur.gainDb !== gain) {
    inp.dataset.busy = '1';
    await mixerCmd({ cmd: 'audio_mix', source: id, gain });
    delete inp.dataset.busy;
    if (inp.dataset.dirty) { // moved while the command was going: goes afterwards (now, if released)
      if (inp.dataset.held) gainLater(inp, id);
      else sendGain(inp, id);
      return;
    }
  }
  rerenderAudio();
}

// Screen headphones (HEADPHONES.md): the program audio through the board's HDMI, for the screen's jack.
// An older engine does not send them. The volume, like the inputs' (one command every GAIN_EVERY ms
// while dragging, the last one on release); −40 dB is silence
const PHONES_MIN = -40;
const PHONES_MAX = 0;
const phonesLabel = (g) => (g <= PHONES_MIN ? t('mixer.silence') : dbLabel(g));
const rerenderPhones = () => renderPhones(state.mixer && state.mixer.audio && state.mixer.audio.phones);

function renderPhones(p) {
  $('mixer-phones').hidden = !p;
  if (!p) return;
  const cb = $('mixer-phones-on');
  if (!cb.dataset.busy) cb.checked = p.on;
  const inp = $('mixer-phones-gain');
  inp.disabled = !p.on;
  if (!inp.dataset.held && !inp.dataset.dirty && !inp.dataset.busy) {
    inp.value = String(p.gainDb);
    showPhonesGain(p.gainDb);
  }
  const note = $('mixer-phones-note');
  const err = p.on && p.state === 'error';
  note.classList.toggle('err', err);
  note.textContent = !p.on ? t('phones.off')
    : err ? t('phones.error', { detail: inSentence(detailText(p)) || t('phones.errorDefault') })
    : p.state === 'waiting' ? t('phones.waiting', { detail: detailText(p) || t('phones.waitingDefault') })
    : t('phones.on');
}

function showPhonesGain(g) {
  const inp = $('mixer-phones-gain');
  $('mixer-phones-val').textContent = phonesLabel(g);
  inp.setAttribute('aria-valuetext', phonesLabel(g));
  inp.style.setProperty('--p', String((Number(inp.value) - PHONES_MIN) / (PHONES_MAX - PHONES_MIN)));
}

$('mixer-phones-on').addEventListener('change', async () => {
  const cb = $('mixer-phones-on');
  cb.dataset.busy = '1';
  await mixerCmd({ cmd: 'phones', on: cb.checked });
  delete cb.dataset.busy;
  rerenderPhones(); // if the engine refused it, back to what it says
});

{
  const inp = $('mixer-phones-gain');
  inp.addEventListener('input', () => {
    inp.dataset.held = '1';
    inp.dataset.dirty = '1';
    showPhonesGain(Number(inp.value));
    if (!inp.timer) inp.timer = setTimeout(sendPhonesGain, GAIN_EVERY);
  });
  const release = () => {
    if (!inp.dataset.held) return;
    delete inp.dataset.held;
    sendPhonesGain();
  };
  for (const ev of ['change', 'pointerup', 'pointercancel', 'blur']) inp.addEventListener(ev, release);
}

async function sendPhonesGain() {
  const inp = $('mixer-phones-gain');
  clearTimeout(inp.timer);
  inp.timer = 0;
  if (!inp.dataset.dirty || inp.dataset.busy) return;
  delete inp.dataset.dirty;
  const gain = Number(inp.value);
  const cur = state.mixer && state.mixer.audio && state.mixer.audio.phones;
  if (!cur || cur.gainDb !== gain) {
    inp.dataset.busy = '1';
    await mixerCmd({ cmd: 'phones', gain });
    delete inp.dataset.busy;
    if (inp.dataset.dirty) { // moved while the command was going: goes afterwards (now, if released)
      if (!inp.dataset.held) sendPhonesGain();
      else if (!inp.timer) inp.timer = setTimeout(sendPhonesGain, GAIN_EVERY);
      return;
    }
  }
  rerenderPhones();
}

function linkRow(video) {
  const row = audioRow(video, 'mx-arow mx-link', '<select id="%id"></select>');
  const sel = row.querySelector('select');
  sel.addEventListener('change', async () => {
    sel.dataset.busy = '1';
    await mixerCmd({ cmd: 'audio_link', video, source: sel.value });
    delete sel.dataset.busy;
    rerenderAudio();
  });
  return row;
}

$('mixer-audio-set').addEventListener('toggle', () => {
  if ($('mixer-audio-set').open) return;
  // Closing discards what is not valid (what is valid was already sent on leaving the field)
  for (const inp of $('mixer-delays').querySelectorAll('input')) {
    clearTimeout(inp.timer);
    delete inp.dataset.dirty;
  }
  $('delay-err').hidden = true;
  rerenderAudio();
});

function mixerTile(slot) {
  const tile = document.createElement('div');
  tile.className = 'mx-tile';
  tile.dataset.slot = slot;
  // Fixed markup only: the data goes afterwards with textContent
  tile.innerHTML = '<button class="mx-view" type="button"><img alt="" hidden><span class="mx-empty"></span><span class="mx-badge" hidden></span></button>' +
    '<div class="mx-meta"><span class="mx-name"></span><span class="mx-sub"></span></div>' +
    '<div class="mx-acts"><button class="btn small mx-pip" type="button" data-i18n="mixer.layoutPip"></button><button class="btn small ghost mx-hide" type="button"></button></div>';
  applyI18n(tile);
  tile.querySelector('.mx-view').addEventListener('click', () => {
    if (tile.dataset.id) mixerCmd({ cmd: 'take', source: tile.dataset.id });
  });
  tile.querySelector('.mx-pip').addEventListener('click', () => mixerCmd({ cmd: 'secondary', source: tile.dataset.id }));
  tile.querySelector('.mx-hide').addEventListener('click', () => mixerCmd({ cmd: 'hide', source: tile.dataset.id, hidden: tile.dataset.state !== 'hidden' }));
  return tile;
}

// cfg: the slot as the user arranged it (slotConfig; an older engine does not send it). A slot pinned to
// a source that is not connected stays reserved (black in the program); an «off» slot never gets one
function updateTile(tile, s, p, cfg) {
  const live = !!s && s.state === 'live';
  const onAir = !!s && s.id === p.main;
  const sec = !!s && s.id === p.secondary && p.layout !== 'full';
  const name = srcName(s);
  tile.dataset.id = s ? s.id : '';
  tile.dataset.state = s ? s.state : 'free';
  tile.classList.toggle('onair', onAir);
  tile.classList.toggle('sec', sec);
  tile.classList.toggle('free', !s);
  tile.classList.toggle('hidden-src', !!s && s.state === 'hidden');
  tile.querySelector('img').hidden = !live;
  const empty = tile.querySelector('.mx-empty');
  empty.hidden = live;
  const reserved = !s && cfg && cfg.assign !== 'auto' && cfg.assign !== 'off';
  const off = !s && cfg && cfg.assign === 'off';
  empty.textContent = s ? (MX_STATES.includes(s.state) ? t('mxstate.' + s.state) : s.state)
    : reserved ? t('mixer.reserved') : off ? t('mixer.slotOff') : t('mixer.free');
  const badge = tile.querySelector('.mx-badge');
  badge.hidden = !(onAir || sec);
  badge.textContent = onAir ? t('mixer.onAirBadge') : p.layout === 'pip' ? t('mixer.layoutPip') : t('mixer.second');
  tile.querySelector('.mx-name').textContent = s ? `${s.slot} · ${name}`
    : cfg && cfg.alias ? `${tile.dataset.slot} · ${cfg.alias}`
    : reserved || off ? t('slots.row', { n: tile.dataset.slot }) : t('mixer.emptySlot', { slot: tile.dataset.slot });
  // With an alias, the device's name goes underneath (when there is no live detail to show)
  tile.querySelector('.mx-sub').textContent = !s ? (reserved ? t('mixer.reservedSub') : '') : live
    ? [t('unit.fps', { v: num(s.fps, 0) }), s.mode.replace('x', '×'), formatLabel(s.format)].filter(Boolean).join(' · ')
    : detailText(s) || (s.alias ? fixedName(s.id, s.name) : '');
  // The whole reason in the tooltip (the line under the tile is cut to its width), and the device's name under an alias
  tile.title = !s ? '' : [s.alias ? fixedName(s.id, s.name) : '', live ? '' : detailText(s)].filter(Boolean).join(' · ');
  const view = tile.querySelector('.mx-view');
  view.disabled = !s || s.state === 'hidden';
  view.setAttribute('aria-label', s ? (onAir ? t('mixer.tileOnAirAria', { name }) : t('mixer.tileTakeAria', { name })) : t('mixer.tileFreeAria'));
  const pip = tile.querySelector('.mx-pip');
  pip.hidden = !s;
  pip.disabled = !s || onAir || s.state === 'hidden' || s.id === p.secondary;
  const hide = tile.querySelector('.mx-hide');
  hide.hidden = !s;
  hide.textContent = s && s.state === 'hidden' ? t('mixer.show') : t('mixer.hide');
  hide.disabled = onAir;
}

async function mixerCmd(body) {
  try {
    const r = await api('POST', '/mixer', body);
    if (r && r.state) renderMixer(r.state);
    return true;
  } catch (e) {
    toast(e.message, true);
    return false;
  }
}

for (const b of document.querySelectorAll('#mixer-card [data-layout]')) {
  b.addEventListener('click', () => mixerCmd({ cmd: 'layout', layout: b.dataset.layout }));
}
$('mixer-fade').addEventListener('click', () => {
  const p = state.mixer && state.mixer.program;
  mixerCmd({ cmd: 'transition', transition: p && p.transition === 'fade' ? 'cut' : 'fade' });
});
$('mixer-swap').addEventListener('click', () => mixerCmd({ cmd: 'swap' }));
for (const b of document.querySelectorAll('#mixer-pip [data-corner]')) {
  b.addEventListener('click', () => mixerCmd({ cmd: 'pip', corner: b.dataset.corner }));
}
for (const b of document.querySelectorAll('#mixer-pip [data-size]')) {
  b.addEventListener('click', () => mixerCmd({ cmd: 'pip', size: Number(b.dataset.size) }));
}
$('mixer-lock').addEventListener('click', () => {
  const sc = state.mixer && state.mixer.screen;
  if (sc) mixerCmd({ cmd: 'lock', on: sc.locked !== true });
});
$('mixer-screen-box').addEventListener('toggle', () => { stopMixerImages(); startMixerImages(); });

// Source thumbnails (and the screen's, if unfolded): the device remakes them once per second; here they
// are requested every 1.5 s, only with the tab visible and without overlapping requests.
function refreshImg(img, url) {
  if (img.dataset.busy) return;
  img.dataset.busy = '1';
  const done = () => { delete img.dataset.busy; };
  img.onload = done;
  img.onerror = done;
  img.src = `${url}?t=${Date.now()}`;
}

function startMixerImages() {
  if (state.mixerImgTimer || !state.live) return;
  const tick = () => {
    state.mixerImgTimer = 0;
    if (!state.live || !state.mixer || $('mixer-card').hidden) return;
    for (const tile of $('mixer-sources').children) {
      if (tile.dataset.state === 'live') refreshImg(tile.querySelector('img'), `/api/v1/mixer/source-${tile.dataset.slot}.jpg`);
    }
    if ($('mixer-screen-box').open && !$('mixer-screen-box').hidden) refreshImg($('mixer-screen-img'), '/api/v1/mixer/screen.jpg');
    state.mixerImgTimer = setTimeout(tick, 1500);
  };
  tick();
}

function stopMixerImages() {
  clearTimeout(state.mixerImgTimer);
  state.mixerImgTimer = 0;
}

// --- Log -------------------------------------------------------------------------------------
// Journal lines are for operators: they are not translated (only the time follows the language)

const MAX_LOGS = 200;
const LOG_TIME = { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' };

function addLog(e, live) {
  const li = document.createElement('li');
  li.className = e.level;
  const time = document.createElement('time');
  const d = new Date(e.ts);
  time.dateTime = isNaN(d) ? '' : d.toISOString();
  time.textContent = isNaN(d) ? '' : fmtTime(d, LOG_TIME);
  const msg = document.createElement('span');
  msg.className = 'msg';
  msg.textContent = e.msg.replace(/^\[(engine|hdmi2srt)\]\s*/, '');
  li.append(time, msg);
  const ol = $('logs');
  if (live) ol.prepend(li); else ol.append(li);
  while (ol.children.length > MAX_LOGS) ol.lastChild.remove();
}

async function loadLogs() {
  try {
    const entries = await api('GET', '/logs?lines=60');
    $('logs').replaceChildren();
    for (const e of entries.reverse()) addLog(e, false); // most recent on top
  } catch (_) { /* filled live */ }
}

// --- Password --------------------------------------------------------------------------------

$('pw-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const msg = $('pw-msg');
  try {
    await api('PUT', '/password', { current: $('pw-cur').value, new: $('pw-new').value });
    ev.target.reset();
    msg.textContent = t('pw.changed');
    msg.className = 'ok';
  } catch (e) {
    msg.textContent = e.status === 403 ? t('pw.wrongCurrent') : e.message;
    msg.className = 'err';
  }
  msg.hidden = false;
});

// --- Language change --------------------------------------------------------------------------
// The static texts change by themselves (data-i18n). What app.js wrote is redone from the last state;
// rows built once (uplinks, cameras) are rebuilt because their labels were set when created.

onLangChange(() => {
  delete $('uplinks').dataset.names;
  delete $('cams-list').dataset.ids;
  delete $('mixer-audio').dataset.key;
  for (const time of document.querySelectorAll('#logs time')) {
    const d = new Date(time.dateTime);
    if (!isNaN(d)) time.textContent = fmtTime(d, LOG_TIME);
  }
  renderSoundBtn();
  if (state.status) renderStatus(state.status);
  if (state.mixer) renderMixer(state.mixer);
  renderLiveBtn();
  if (state.dest) { renderDest(state.dest); destPlaceholders(); }
  if (state.dl) renderDests(state.dl, true);
  if (!$('dests-pick').hidden) openPicker();
  destLayout();
  if (state.cloud) renderCloud(state.cloud);
  if (state.catalog) { if (!state.inputDirty) fillInputForm(); else { renderModeOptions(); renderSourceInfo(); } }
  if ($('wifi-nets').open && !$('wifi-nets-body').querySelector('form')) loadNets();
  if ($('cams-wifi').open) showWifi();
  const conn = $('cams-connect');
  if (!conn.hidden && conn.dataset.id) { const id = conn.dataset.id; closeConnect(); showConnect(id); }
  if (state.rtcError || !$('preview-empty').hidden) showRTC(!$('live').hidden);
});

// After every script of the page (settings.js, setup.js) has run
document.addEventListener('DOMContentLoaded', boot);
