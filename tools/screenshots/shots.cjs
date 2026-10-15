// The browser half of tools/screenshots/shots.sh (Playwright, Chromium):
//
//   node shots.cjs frames <work>                         the synthetic camera pictures (scenes.html) → <work>/frames
//   node shots.cjs web <work> <base-url> <password> <out> the web page of eirl-encoder-sim (local mode) → <out>
//
// The simulator has no WebRTC preview, shows some sources failing on purpose (to exercise the detail codes) and
// does not really stream, so before the page draws its state it is tidied up into a healthy encoder on air.
const { chromium } = require('playwright');
const path = require('path');
const fs = require('fs');

const [cmd, work, base, password, outDir] = process.argv.slice(2);
const frames = path.join(work, 'frames');
const fileURL = (p) => 'file://' + p;

async function renderFrames(browser) {
  fs.mkdirSync(frames, { recursive: true });
  const scenes = fileURL(path.join(__dirname, 'scenes.html'));
  const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } });
  for (const s of ['city', 'mountain', 'sea', 'bars']) {
    await page.goto(`${scenes}?scene=${s}&w=1920&h=1080`);
    await page.screenshot({ path: path.join(frames, `${s}.png`) });
  }
  await page.setViewportSize({ width: 1080, height: 1920 });
  await page.goto(`${scenes}?scene=phone&w=1080&h=1920`);
  await page.screenshot({ path: path.join(frames, 'phone-portrait.png') });

  // What the engine would make of them: a portrait phone pillarboxed into 16:9, and the program with a
  // picture-in-picture where scene.PipRect(1920, 1080, "br", 33) puts it
  await page.setViewportSize({ width: 1920, height: 1080 });
  const compose = async (name, html) => {
    const f = path.join(frames, name + '.html');
    fs.writeFileSync(f, `<html><body style="margin:0;background:#000;width:1920px;height:1080px;position:relative;overflow:hidden">${html}</body></html>`);
    await page.goto(fileURL(f));
    await page.evaluate(() => Promise.all([...document.images].map((i) => i.decode())));
    await page.screenshot({ path: path.join(frames, name) });
    fs.rmSync(f);
  };
  const img = (n, style) => `<img src="${fileURL(path.join(frames, n))}" style="position:absolute;${style}">`;
  await compose('phone.png', img('phone-portrait.png', `left:${(1920 - 608) / 2}px;top:0;height:1080px`));
  await compose('pip.png', img('city.png', 'inset:0;width:1920px') + img('mountain.png', 'right:58px;bottom:32px;width:640px;height:360px'));
}

// Destinations, slot names and the output on, through the page's own API helper (its session)
async function setup(page) {
  await page.evaluate(async () => {
    const list = await api('GET', '/destinations');
    const add = (d) => api('POST', '/destinations', d);
    const relay = await add({ name: 'My relay', template: 'custom', protocol: 'srtla', url: 'srtla://relay.example.com:5001',
      streamId: 'publish:demo-key', videoCodec: 'h265', videoBitrateKbps: 8000 });
    await add({ name: 'Twitch', template: 'twitch', protocol: 'rtmp', url: 'rtmp://live.twitch.tv/app', key: 'live_000000000_demoDemoDemoDemo' });
    await add({ name: 'YouTube', template: 'youtube', protocol: 'rtmps', url: 'rtmps://a.rtmps.youtube.com:443/live2',
      key: 'abcd-efgh-ijkl-mnop-qrst', videoCodec: 'h265', videoBitrateKbps: 9000 });
    await add({ name: 'Studio (RIST)', template: 'custom', protocol: 'rist', url: 'rist://203.0.113.40:8200', videoCodec: 'h264' });
    await api('POST', `/destinations/${relay.id}/activate`, { apply: true });
    for (const d of list.destinations) if (!d.managed) await api('DELETE', `/destinations/${d.id}`).catch(() => {});
    // The simulator's slots: board HDMI, a Logi webcam, a phone, the test pattern
    await api('PUT', '/sources/slots', { slots: [
      { slot: 1, assign: 'auto', alias: 'Main camera' }, { slot: 2, assign: 'auto', alias: 'Wide shot' },
      { slot: 3, assign: 'auto', alias: 'Phone' }, { slot: 4, assign: 'auto', alias: 'Seafront' }] });
    await api('PUT', '/device', { language: 'en', name: 'Encoder' }).catch(() => {});
    await api('PUT', '/output', { output: 'on', apply: true });
  });
}

// render* are the page's global functions: the SSE listeners look them up by name on every event
async function tidy(page) {
  await page.evaluate(() => {
    const mixer = window.renderMixer;
    window.renderMixer = (m) => {
      for (const s of m.sources || []) {
        if (s.kind === 'network') Object.assign(s, { state: 'live', format: 'H265', mode: '1080x1920@30', fps: 30, detail: '', detailCode: '', detailArgs: undefined });
        if (s.kind === 'test') Object.assign(s, { kind: 'usb', name: 'Cam Link 4K', format: 'NV12', mode: '1920x1080@60', fps: 60 });
      }
      for (const a of (m.audio && m.audio.sources) || []) if (a.state !== 'live') Object.assign(a, { state: 'live', detail: '', detailCode: '', detailArgs: undefined });
      return mixer(m);
    };
    const tx = { eth0: 3100, wlan0: 900, wwan0: 2700, wwan1: 600 };
    const status = window.renderStatus;
    window.renderStatus = (st) => {
      if (st.output === 'on') {
        st.outputState = { state: 'on' };
        st.srt = { rttMs: 52, sendMbps: 7.3, lossPct: 0, pktRetrans: 0 };
        st.bitrate = { kbps: 8000, maxKbps: 8000 };
      }
      st.system = Object.assign({}, st.system, { tempC: { cpu: 41 }, power: { inMw: 7400, inMv: 5180 }, undervoltage: false });
      st.modems = (st.modems || []).filter((x) => x.sim !== 'pin');
      st.links = (st.links || []).filter((x) => x.code !== 'sim_pin');
      for (const u of [...(st.uplinks || []), ...(st.links || [])]) {
        if (u.health === 'bad') continue;
        u.inUse = st.output === 'on';
        u.txKbps = u.inUse ? tx[u.name] || 0 : 0;
        if (u.healthCode === 'connecting' || u.code === 'connecting') {
          u.health = 'good';
          u.healthCode = u.code = 'ok';
        }
      }
      return status(st);
    };
  });
}

async function webShots(browser) {
  fs.mkdirSync(outDir, { recursive: true });
  const slotFrame = { 1: 'city.png', 2: 'mountain.png', 3: 'phone.png', 4: 'sea.png' };
  // 1440 px of layout, saved 1920 px wide
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1060 }, deviceScaleFactor: 4 / 3, colorScheme: 'dark', locale: 'en-US' });
  await ctx.route('**/api/v1/mixer/source-*.jpg*', (route) => {
    const n = route.request().url().match(/source-(\d)\.jpg/)[1];
    route.fulfill({ path: path.join(frames, slotFrame[n] || 'bars.png'), contentType: 'image/png' });
  });
  await ctx.route('**/api/v1/live', (route) => route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"no_preview"}' }));
  const page = await ctx.newPage();
  await page.goto(base);
  await page.selectOption('select', 'en').catch(() => {});
  await page.fill('input[type=password]', password);
  await page.keyboard.press('Enter');
  await page.waitForSelector('#app:not([hidden])');
  await setup(page);
  await page.reload();
  await page.waitForSelector('#app:not([hidden])');
  await tidy(page);
  // The WebRTC preview's place: the program picture
  const program = 'data:image/png;base64,' + fs.readFileSync(path.join(frames, 'city.png')).toString('base64');
  await page.evaluate((src) => {
    const pv = document.querySelector('.card.program .preview');
    const img = document.createElement('img');
    img.src = src;
    img.style.cssText = 'position:absolute;inset:0;width:100%;height:100%;object-fit:cover;z-index:0';
    pv.style.position = 'relative';
    pv.prepend(img);
    for (const el of pv.children) if (el !== img && el.id !== 'live' && !el.className.includes('preview-empty')) el.style.zIndex = 1;
    document.getElementById('preview-empty').style.display = 'none';
    document.getElementById('live').style.display = 'none';
  }, program);
  // Draw the tidied state now (the first events arrived before tidy), then let the thumbnails load (every 1.5 s)
  await page.evaluate(() => { if (state.mixer) renderMixer(state.mixer); });
  await page.waitForTimeout(4000);
  // The meters: the simulator sends no levels
  await page.evaluate(() => {
    const level = window.renderLevel;
    window.renderLevel = () => level({ rmsDb: [-14, -16], peakDb: [-8, -9] });
    window.renderLevel();
  });
  await page.screenshot({ path: path.join(outDir, 'web-dashboard.jpg'), type: 'jpeg', quality: 88 });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.waitForTimeout(800);
  await page.screenshot({ path: path.join(outDir, 'web-phone.png') });
}

(async () => {
  const browser = await chromium.launch();
  try {
    if (cmd === 'frames') await renderFrames(browser);
    else if (cmd === 'web') await webShots(browser);
    else throw new Error('usage: node shots.cjs frames <work> | web <work> <base-url> <password> <out>');
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e.message || e);
  process.exit(1);
});
