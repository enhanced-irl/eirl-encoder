> **Status:** IN PROGRESS — v1 complete and deployed (phases 0-4, Oct 8); phase 5 (WebRTC preview with audio) deployed on Oct 8; in `main` (MR !1); pending: testing it on a real phone and the items in §12
> **Updated:** 2026-10-14
>
> SRTLA and adaptive bitrate (branch `feat/srtla`, not deployed): design in [`SRTLA.md`](SRTLA.md); here, what changes in the API.
>
> Linking to an EnhancedIRL account (cloud client, thumbnail and simulator; branch `feat/cloud-link`, not deployed):
> contract in `devops/docs/active/ENCODER_CLOUD.md`; here, §10.

# The encoder's management app (`eirl-encoderd`)

Local web interface to operate the encoder from a phone: SRT destination, start/stop the
stream, preview of the HDMI and live status (signal, audio, SRT, temperature and power). The
stream is still `eirl-engine.service`; the app **does not stream**, it only configures, starts and
watches it. If the app goes down, the stream goes on.

## 1. What is on the board today and shapes the design (inventory of 2026-10-08, read-only)

| Fact | Consequence |
|---|---|
| `eirl-engine` active, listener :9000. `gst-launch` ~28 % of a core. The graphical session (gdm) took ~400 MB of RAM (its 11 % CPU was from boot) | **Done (Oct 8)**: boot into `multi-user.target` and surplus services disabled (see the README, "Board state") |
| `nvjpegenc` present, but it **doubles the host1x interrupts** (measured in phase 1) | v1's JPEG preview encoded it on the CPU. Since phase 5 the preview is a second hardware H.264 encode, which does **not** raise those interrupts |
| polkit **0.105** (Ubuntu 18.04): `.pkla` files only, no JavaScript rules | With polkit the permission **cannot** be limited to a specific unit: either all or none. Discarded for starting and stopping the service |
| `ina3221` (power): `in_power*_input` **is only readable by root**; `tegrastats` without root gives no `POM_*` | Reading those sysfs files has to be opened up (they are telemetry, read-only) |
| Temperatures (`thermal_zone*`), GPU load (`/sys/devices/gpu.0/load`) and `/proc/stat`: readable without root | Read directly, without launching `tegrastats` |
| `/dev/video0`, `/dev/v4l-subdev1` → group `video`; `/dev/i2c-8` → group `i2c` | The app's user joins those two groups (queries only) |
| `avahi-daemon` active, hostname `jetson` | `http://jetson.local` already resolves on the LAN (recent iOS and Android) |
| `/run` is a 2 GB tmpfs; `/tmp` is on the SD | Everything ephemeral (frames, statistics) goes to `/run`, never to the SD |
| **The key leaks into the journal**: the script masks `passphrase=` but prints `streamid=publish:<key>` in clear | It has to be fixed in the script (phase 1), with or without the app |
| As a listener without a client, `srt-live-transmit` writes `N bytes lost, 0 bytes sent…` **every 5 s** (41 lines in 11 min ≈ 17,000/day) | Noise in the journal that would hide what matters; review `-q` in phase 1 |
| `VBITRATE`/`ABITRATE` end up in `$(( … ))` inside a string passed to `eval` | A value like `a[$(cmd)]` **executes code** (bash arithmetic evaluates subscripts). Strict validation in the app **and** in the script |
| `SRT_PASSPHRASE`/`SRT_STREAMID` are concatenated unescaped into the SRT URL's query | A `&` in the key would inject SRT parameters (`&mode=listener`…). Character whitelist |
| EnhancedIRL keys: `randomBytes(24).toString('base64url')` → `[A-Za-z0-9_-]{32}`; streamid `publish:<key>` | Strict validation leaves out no real key |
| Go 1.26.4 on the development Mac (same as `eirl-failover`). Go requires kernel ≥ 3.2; the board has 4.9 | Static binary `CGO_ENABLED=0 GOOS=linux GOARCH=arm64`, not depending on glibc 2.27 |

## 2. Architecture

```
 Phone / laptop (LAN)                       Jetson
 ───────────────────────          ─────────────────────────────────────────────────────────────────
  browser ───HTTP :80──▶ eirl-encoderd (Go, user eirl-encoder, Nice 10, CPUQuota 20 %)
     ▲   ▲                  │  ├─ JSON API /api/v1 + SSE /api/v1/events + embedded web (embed.FS)
     │   └── SSE (status    │  ├─ config:  /etc/eirl-encoder/engine.env  (atomic write)
     │       2 s, logs,     │  ├─ control: sudo -n /bin/systemctl {start,stop,restart} eirl-engine.service
     │       level meter)   │  ├─ status:  ioctl QUERY_DV_TIMINGS (/dev/video0) · I2C AU_STATUS0 (/dev/i2c-8)
     │                      │  │           systemctl show · journalctl -u eirl-engine -o json -f
     │                      │  │           sysfs: thermal, ina3221, gpu load · /proc/stat
     │                      │  ├─ SRT:     /run/eirl-engine/srt-stats.json (latest sample, written by the script)
     └── WebRTC (UDP) ──────│  ├─ preview: RTP 127.0.0.1:5006 (H.264) and :5008 (Opus) → pion → browsers
                            │  └─ meter:   UDP 127.0.0.1:5005 (48 kHz PCM from the mixer) → peak/RMS
                            ▼
                   eirl-engine.service (owner unchanged: the engine's user today)
                   v4l2src → nvvidconv → tee ─┬─ nvv4l2h264enc → … → mpegtsmux → FIFO → srt-live-transmit → SRT
                                              └─ [preview] nvvidconv 640x360 (VIC) → nvv4l2h264enc Baseline 800k → RTP → udpsink :5006
                   audiomixer → tee ─┬─ voaacenc → mux
                                     ├─ [preview] opusenc 96k → RTP → udpsink :5008
                                     └─ [meter] udpsink 127.0.0.1:5005
```

Principles:
- **Decoupled**: the app and the stream share no process nor blocking sockets. The
  preview and level-meter branches write over local UDP, which never makes the pipeline wait even if the app is
  stopped.
- **Zero cost if nobody is watching**: the app polls nothing (neither I2C, nor v4l2, nor the journal) while there
  is no client subscribed to `/api/v1/events`. With someone watching: one cycle every 2 s.
- All the hardware is read with **ioctl from Go** (`golang.org/x/sys/unix`), without launching `v4l2-ctl`
  or `i2ctransfer` every 2 s. Processes are only launched for `systemctl` and `journalctl`.
- **A single source of configuration**, shared with the script and in systemd's
  `EnvironmentFile` format.

### Code

Separate Go module in `manager/` (`github.com/enhanced-irl/eirl-encoder/manager`), same pattern as
`eirl-failover`. Dependencies: `golang.org/x/sys` (ioctl), `golang.org/x/crypto/bcrypt` and, since
phase 5, `github.com/pion/webrtc/v4` (pure Go: the binary stays static; it grows from 6.5 to
10.6 MB).

```
manager/
├─ cmd/eirl-encoderd/main.go   ← flags, startup, `passwd` subcommand
├─ internal/config/            ← schema, strict validation, atomic read and write of the .env
├─ internal/service/           ← systemctl show / sudo start|stop|restart, journal (redacted and classified)
├─ internal/hw/                ← dv-timings (ioctl), AU_STATUS0 (I2C_RDWR), sysfs (temperature, power, CPU)
├─ internal/srtstats/          ← latest SRT statistics sample
├─ internal/live/              ← WebRTC preview: forwarding of the local RTP, Sender Reports, signalling
├─ internal/audiolevel/        ← UDP PCM → per-channel peak/RMS in dBFS at 10 Hz
├─ internal/auth/              ← password (bcrypt), in-memory sessions, attempt limit
├─ internal/api/               ← handlers, SSE, middlewares (LAN only, session, CSRF); cloud.go = the device for the cloud
├─ internal/cloud/             ← linking with EnhancedIRL and outgoing WebSocket channel (§10)
├─ cmd/eirl-encoder-sim/       ← simulated encoder against any backend (§10)
├─ web/                        ← HTML + CSS + JS without a build step (embed.FS), translated es/en/fr; i18n-check.mjs (dev only)
└─ deploy/                     ← systemd unit, eirl-engine drop-in, sudoers, tmpfiles, install.sh
```

The web is **minimal**: no framework and no build step (`manager/web/static/`: `index.html`, `app.js`, `settings.js`
(source slots, updates and device cards), `setup.js` (first-start wizard), `app.css`, and the translations `i18n.js` +
`lang-es.js`, `lang-en.js`, `lang-fr.js`; ~345 KB), embedded in the binary. Dark theme with the
`eirl` theme tokens and the system font (no CDN nor Google Fonts: the device works on a
LAN without internet and the CSP is `default-src 'self'`). A single screen, responsive and full
width, with four zones by what the user does: **Live** (program, Go live and the mixer's
Sources), **Status** (signal, audio, SRT, board and uplinks), **Settings** (destination, input, source slots,
Wi-Fi, network cameras and account) and **System** (updates, device, log, password and version). A grid with named areas (`app.css`,
"Layout") places them by width: one column on a phone (≤ 720 px), the live zone full
width with two columns below on a tablet, live | status and settings on a laptop (≥ 1100 px) and three
columns on a wide monitor (≥ 1700 px). What is inside each zone adapts to its column's width
(container queries), not to the window's. On the screen:

- header with the status (`Live` in red **only** with an SRT client connected; otherwise, `No
  SRT connection`), warning of unapplied changes with an "Apply" button;
- live preview over WebRTC (video and audio, starts muted with an "Unmute" button) and
  L/R level meter (−60…0 dBFS, peak + RMS);
- Go live ↔ Stop streaming and Restart buttons (no rehearsal mode since the mixer's F3: the
  preview is always visible), with confirmation only if someone is receiving;
- signal, audio, SRT and board cards (temperature, power, low-supply warning);
- destination: you paste the key's URL; the **mode is deduced from the URL** (with a host, caller; `srt://:port`,
  listener) and secrets are never displayed (empty fields = "no changes", a checkbox to remove them);
- source slots: what each mixer slot holds (Automatic, None or a source) and its alias (`GET/PUT /sources/slots`,
  [`SETUP.md`](SETUP.md) §5); the mixer tiles show the alias;
- updates: version, state, «Check now», «Update to X» with the release notes, disabled while streaming
  (`/update`, `UPDATES.md`), and a banner when a new version is available;
- device: language, name (and its `.local` name), time zone, NTP or manual clock, and «Repeat the initial setup»
  (`/device`, `/setup/restart`);
- live log and password change.

With the tab hidden (phone locked, another app) it closes the SSE and the preview.

**First-start wizard.** While `GET /setup` says `done: false` the web shows a full-page wizard instead of the
dashboard (`setup.js`, [`SETUP.md`](SETUP.md) §4): language → admin password with the setup code (only while there is
none; it opens the session) → time → network (the Wi-Fi card) → name → EnhancedIRL (the account card, with a QR of the
pairing link) → done. It marks each step (`POST /setup/step`), resumes after a reload and polls `GET /setup`, so it
leaves as soon as the touch screen finishes setup. With a password but setup unfinished, the login comes first.

**Languages.** Every visible text comes from the dictionaries (`lang-*.js`) through `t(key, params)` and the
`data-i18n` / `data-i18n-attr` attributes of `index.html` (`i18n.js`: plurals with `Intl.PluralRules`, numbers and
dates with `Intl` in the active language, French typography). The language is this browser's choice (the header
picker, `localStorage` `eirl.lang`), else the device's (`language` of `GET /setup`, which needs no session, or
`GET /device`), else the browser's, else English; changing it in the Device card or the wizard changes the device
language and drops the browser's choice. Every request sends `Accept-Language`, so the API's `message` and field
errors come in the same language. Journal lines are not translated. After touching any text, run
`node manager/web/i18n-check.mjs`: it fails on a key missing in a language, `{params}` that differ, a key the page
uses that no dictionary has, or Spanish-looking text left outside the dictionaries.

## 3. Local API contract (`/api/v1`)

All JSON. Everything except `login` requires a session. Mutations also require `Origin`/`Sec-Fetch-Site`
from the device itself (CSRF) and `Content-Type: application/json`.

### Session

| Method and route | Body | Response |
|---|---|---|
| `POST /login` | `{"password": "…"}` | `204` + cookie `eirl_sess` (HttpOnly, SameSite=Strict, 30 sliding days). `401` if it fails, `429` if the limit is exceeded (5 attempts/min per IP, with an increasing wait) |
| `POST /logout` | — | `204` |
| `GET /session` | — | `200 {"authenticated": true}` · `401` |
| `PUT /password` | `{"current": "…", "new": "…"}` (≥ 10 characters) | `204`. Invalidates the other sessions (from the screen, over the local socket: all of them). `403 bad_password` if the current one is wrong, `400 validation` if the new one does not fit, `429` after 5 wrong current passwords in a minute per IP (Oct 14) |

### Status

`GET /status` returns a snapshot. `GET /events` (SSE) sends:
- `event: status`: the same snapshot, every 2 s;
- `event: log`: the new journal lines;
- with `?levels=1`, `event: level`: the level meter, at 10 Hz (`{"peakDb":[L,R],"rmsDb":[L,R]}`, −90 = silence);
- with the mixer running, `event: mixer`: its state as soon as it changes (a touch on the
  encoder's screen, another tab), with the same shape as `GET /mixer`.

The polling, `journalctl -f`, the level-meter socket and the mixer subscription only run
while some client is connected. Separately, the app always follows the mixer, even when there is
none, to save the output chosen on the touch screen (see `PUT /output`).

```jsonc
{
  "service": {
    "state": "active",            // active | activating | deactivating | inactive | failed
    "phase": "streaming",         // streaming | ready | waiting_signal | waiting_device | restarting | stopped | failed
    "since": "2026-10-08T00:01:00Z",
    "restarts": 0                 // systemd's NRestarts since the last manual start
  },
  "output": "on",                 // mode of the RUNNING stream (left by the script in /run/eirl-engine/output)
  "pendingRestart": false,        // there is saved configuration newer than the current start
  "input": {                      // what is SAVED (PUT /input) and, with the stream running, what is running (SOURCES.md)
    "edidPreferred": "1080p50", "edidActive": "1080p30",   // saved EDID · loaded by the running stream (state: EDID=)
    "source": "usb", "device": "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0",
    "format": "auto", "mode": "auto", "audio": "auto",
    "active": { "state": "ready",                          // starting | waiting_device | ready
                "source": "usb", "device": "/dev/v4l/by-id/…", "name": "Cam Link 4K",
                "format": "NV12", "mode": "1920x1080@60", "rate": "60000/1001", "audio": "alsa:C4K" }
  },
  "signal": {
    "present": true, "width": 1920, "height": 1080, "fps": 30.0,
    "interlaced": false,
    "supported": true,            // false if interlaced or if it does not fit in 2 lanes (csiLoad > 0.95)
    "kind": "hdmi-csi",           // hdmi-csi | usb | test: the running source. With usb, the captured mode and present = frames arriving
    "measuredFps": 29.97,         // real fps of the capture (the engine → /run/eirl-engine/video); absent without a stream
    "pixelClockHz": 74250000,
    "csiLoad": 0.533              // fraction of each HDMI line the CSI takes (1080p50 0.89; 1080p60 1.07)
  },
  "audio": {
    "state": "playing",           // playing (0x61) | pll_unlocked (0x41) | no_samples | unknown
    "auStatus0": "0x61"
  },
  "srt": {                        // null if there is no sample in the last 5 s (= no SRT connection)
    "connected": true,
    "rttMs": 2.3, "sendMbps": 7.61, "bufferMs": 1,
    "pktSent": 583, "pktLost": 0, "pktRetrans": 0, "pktDropped": 0,   // of the LAST interval (~1 s)
    "lossPct": 0.0,
    "updatedAt": "2026-10-08T00:05:12Z"
  },
  "system": {
    "cpuPct": [37, 20, 3, 4], "gpuPct": 0,
    "tempC": { "cpu": 45.5, "gpu": 42.0, "pmic": 50.0 },
    "power": { "inMw": 4210, "inMv": 4760, "cpuMw": 900, "gpuMw": 300 },
    "undervoltage": true          // POM_5V_IN < 4.8 V (the 2 A supply, see the README)
  },
  "transport": "srtla",           // srt | srtla: that of the running stream (or the saved one if stopped)
  "uplinks": [                    // internet uplinks (SRTLA.md); absent without the uplinks dispatcher
    { "name": "enx0c5b8f279a64", "ip": "192.168.8.100", "kind": "modem", // ethernet | usb | wifi | modem
      "inUse": true,              // srtla_send is using it now (only when streaming over SRTLA, also with the hot output)
      "disabled": false,          // disabled from the app (PUT /uplinks/{interface})
      "conflict": false,          // same IP as another uplink (two identical HiLink modems): not used
      "noInternet": true,         // only if: the Wi-Fi client without internet (captive portal), out of the split (WIFI.md §13)
      "txKbps": 2410, "rxKbps": 85,    // traffic over the last sample (sysfs), at least 1.5 s long: every caller sees the same
      // With the links service (MODEMS.md §4): the dot of every link, judged in one place (internal/linkhealth)
      "label": "Movistar",        // the modem's operator (or its alias); absent = name it by its kind
      "health": "good",           // good | weak | bad | off
      "healthCode": "ok",         // why: closed list in MODEMS.md §5, translated by the web and the screen
      "modemId": "h-9b2c41d07e5a" } // the modem behind it (modems[])
  ],
  "modems": [ … ],                // cellular modems, also those without a usable link (no SIM, PIN…): GET /modems
  "bitrate": { "kbps": 3200, "minKbps": 1000, "maxKbps": 6000 },  // only with ABR running
  "mixer": { … },                 // only with the mixer running (MIXER=1): the same as GET /mixer
  "outputState": { "state": "on" } // off | connecting | on | error, + reason (closed list) and detail (≤ 200)
}
```

**No rehearsal mode (the mixer's F3, Oct 12).**
- `ready` (formerly `rehearsal`): running, with preview, screen and level meter, not streaming.
- `output` is what was requested (`on`/`off`) and `outputState`, how it is really going.
- With the mixer, the output is attached hot and retries by itself without cutting the preview.
  While it retries after a failure, `connecting` carries the reason.
- Reasons: `no_destination`, `no_uplinks`, `relay_unreachable`, `rejected`, `connection_lost`,
  `encoder_error`, `internal`.
- Without the mixer it is deduced: `on` if there are SRT statistics and `connecting` if there are none.

With the mixer, `service.phase` does not wait for the main source's signal: the program always
has a picture, and a source without signal shows up as "No signal" in its slot.

| Method and route | What |
|---|---|
| `GET /logs?lines=100` | `[{"ts": "…", "level": "info|warn|error", "kind": "…", "msg": "…"}]`. `kind` classifies the watchdog's lines: `signal_lost`, `mode_changed`, `audio_capture_stalled`, `audio_pll_unlocked`, `audio_recovery` (+ `step` and `ok`), `audio_hw_reset`, `pipeline_exit`, `start`, `other`; with a USB source, also `device_missing`, `device_lost`, `format_unsupported`, `usb2`, `audio_missing` and `device`. Always **redacted** (see §6) |

### Destination

`GET /destination`:

```jsonc
{
  "managedBy": "local",                     // local | eirl (v2, paired: read-only here)
  "url": "srt://eu1.relay.example:8890",    // listener: "srt://:9000"
  "mode": "caller",                         // caller | listener
  "latencyMs": 500,
  "streamId":  { "set": true, "masked": "publish:AbCd…wXyZ" },   // never the value
  "passphrase": { "set": false },                                 // never the value
  "videoBitrateKbps": 6000,
  "audioBitrateKbps": 128,
  "output": "on",                           // on | off (not streaming), what is SAVED
  "transport": "srt",                       // srt | srtla, deduced from the URL
  "adaptiveBitrate": false,                 // ABR: adaptive video bitrate
  "videoMinBitrateKbps": 1000,              // floor of the adaptive bitrate
  "edidPreferred": "1080p30",               // EDID announced to the source (PUT /input)
  "pendingRestart": false                   // saved but not yet applied (the service was not restarted)
}
```

`PUT /destination`: fields that do not come are kept; `streamId`/`passphrase`: a string to
change it, `null` to delete it, absent to leave it alone (so the key does not have to be rewritten to change
the latency). Unknown fields → 400. `"apply": true` restarts the stream unless it is stopped on
purpose (`inactive`): if it is running or down (`failed`), it starts with what is saved. It accepts the URL **exactly as given by the
EnhancedIRL panel** (`srt://host:port?streamid=publish:<key>&latency=…`): the server
breaks it down, and if it carries `streamid` or `passphrase` they take precedence over the separate fields.

- `200` → the same object as `GET` + `"restarted": true|false`.
- `400 {"error": "validation", "fields": {"url": "…", "latencyMs": "…"}}`.
- `409` if `managedBy = eirl`.

Validation (whitelist; what does not fit is rejected, not "cleaned"):

| Field | Rule |
|---|---|
| `url` | `srt://` + host (DNS name `[A-Za-z0-9.-]{1,253}`, IPv4 or IPv6 in brackets) + `:` port 1–65535. Listener: `srt://:port`. `srtla://host:port` (SRTLA): host required (name or IPv4, not IPv6) and always caller; when switching to SRTLA without `latency` in the URL, the latency goes up to 2000 ms unless the same request carries another one. No user, path or fragment. Query only with `streamid`, `passphrase`, `latency`, `mode` (they are extracted); any other key → 400 |
| `mode` | `caller` \| `listener`. Listener ⇒ empty host |
| `latencyMs` | integer 20–8000 |
| `streamId` | `[A-Za-z0-9_:.\-/]{1,512}` (the real EnhancedIRL key fits; `#`, `&`, `=`, `?`, spaces, quotes, `$` and `` ` `` do not) |
| `passphrase` | `[A-Za-z0-9_.~\-]{10,79}` (10–79 is SRT's range) |
| `videoBitrateKbps` | integer 500–20000 |
| `audioBitrateKbps` | one of 64, 96, 128, 160, 192, 256 |
| `adaptiveBitrate` | boolean |
| `videoMinBitrateKbps` | integer 200–20000; with `adaptiveBitrate`, ≤ `videoBitrateKbps` |

### Service and preview

| Method and route | What |
|---|---|
| `PUT /output` | `{"output": "on"\|"off", "apply": bool}`: Go live or Stop streaming. With the mixer, **hot**: it is saved and sent to the engine (`{"cmd": "output"}`), without restarting or leaving "unapplied changes" (neither the script's fingerprint nor the app's count `OUTPUT`). Without the mixer, it restarts as before, with the preview always running. Same response and `apply` semantics as `PUT /destination`; `on` requires a destination. Go live or Stop streaming **from the touch screen** (`output.by: "screen"` in the engine's state) is also saved into `OUTPUT`, with the same validation and the same log, once per change (`output.seq`) and even when no tab is open: it survives a restart and the cloud sees it in its configuration. It does not restart or apply other pending changes. With `by` `start` or `socket` it is not touched: what is saved is already the truth. Likewise, the screen's **Power menu** (`screen.power`: `reboot` or `poweroff`) is carried out by the app, once per request (`seq`) and only if it is recent |
| `PUT /uplinks/{interface}` | `{"disabled": true\|false}`: takes **one** uplink out of or into the SRTLA split (name `[A-Za-z0-9_.-]{1,15}`, max. 16 disabled). The server changes it on top of what is already in `/etc/eirl-encoder/uplinks-disabled` (a disabled uplink that is now disconnected stays disabled; two changes in a row do not overwrite each other) and the stream applies it in ≤ 2 s **without cutting** (reloads `srtla_send` with SIGHUP). `200 {"name", "disabled"}`; the state arrives through the SSE. `400` if the name is not valid; `503` without the dispatcher |
| `GET /modems` | The cellular modems ([`MODEMS.md`](MODEMS.md) §4.2): `{"modems": [{"id", "backend": "mm"\|"hilink", "iface", "manufacturer", "model", "state", "sim", "registration", "operator", "tech", "signal": {"rsrp", "rsrq", "sinr", "rssi", "bars", "quality"}, "imeiTail", "iccidTail"}]}`, every field but `id`, `backend`, `state`, `sim` and `signal` optional. Never the whole IMEI, ICCID or IMSI. Read-only (configuration: MODEMS.md phase M4). `[]` without modems or without ModemManager |
| `GET /sources` | Sources connected now ([`SOURCES.md`](SOURCES.md)): `{"sources": [{"id", "kind": "hdmi-csi"\|"usb"\|"test", "name", "node", "usb": {"vendorId", "productId", "speedMbps", "bus", "serial"}, "formats", "audio": {"id", "kind", "name", "card"}, "warnings": ["usb2"\|"only_4k"\|"no_format"\|"unreadable"], "error": "<if it could not be queried>", "auto": {"format", "mode", "fps", "outWidth", "outHeight", "busShare"}, "options": [same as auto]}], "audio": [{"id": "i2s"\|"alsa:<id>", "kind", "name"}]}`. `id` is what is sent in `device`; `auto` is what the stream would choose and `options`, everything that can be asked of it (the same rule: `sources.Choose`/`Options`). `503` without a detector |
| `PUT /input` | With the mixer, the **main** source: the one that goes on air at start and the audio one (what is streamed is changed with `POST /mixer`, without restarting). `{"source": "hdmi"\|"usb"\|"test", "device": "auto"\|"/dev/v4l/by-id/…"\|"/dev/v4l/by-path/…", "format": "auto"\|"NV12"\|"YUY2"\|"I420"\|"UYVY"\|"MJPG", "mode": "auto"\|"<W>x<H>@<fps>", "audio": "auto"\|"none"\|"i2s"\|"alsa:<id>", "edidPreferred": "1080p30"\|"1080p50"\|"720p60", "apply": bool}`. Absent fields are not touched (a body with only `edidPreferred` is still valid); with none → `400`. A device that is not connected can be saved: the stream waits for it (phase `waiting_device`). Validation errors with the keys `source`, `device`, `videoFormat`, `videoMode`, `audio`, `edidPreferred` (`mode` is the destination's: the whole configuration is validated). Same response and `apply` semantics as `PUT /destination`. It belongs to the device, so it can be changed even if the destination is managed by EnhancedIRL. With HDMI, applying = the source renegotiates (a few seconds without signal) |
| `GET /mixer` | The mixer's state (scene, sources per slot, screen; [`MIXER.md` §5](MIXER.md#5-command-and-state-socket-contract)). With an older engine, `program.pip`, `program.pause`, `screen.locked`, `output.by`/`seq`, `audio.follow`, `audio.links`, `audio.mode` and `audio.sources[].delayMs`/`inMix`/`gainDb` are missing (the web hides their controls). `audio.links` carries only the sources in `sources` (`null` = no audio of its own), `delayMs` goes from 0 to 500, `audio.mode` is `single` (`main` plays) or `mix` (the ones with `inMix: true` play at the same time; no other value comes out) and `gainDb`, from −40 to +12 with one decimal (0 = no change). `404 mixer_off` if it is not running (stream stopped, restarting or without `MIXER=1`) |
| `POST /mixer` | A command, the same as the touch screen's: `{"cmd": "take"\|"secondary"\|"swap"\|"layout"\|"transition"\|"hide"\|"audio"\|"audio_follow"\|"audio_delay"\|"audio_link"\|"audio_mode"\|"audio_mix"\|"pip"\|"pause"\|"lock", "source": "<id from GET /mixer>", "layout": "full"\|"pip"\|"side", "transition": "cut"\|"fade", "hidden": bool}` (only each command's fields: one from another command gives `400`; `audio` carries the audio input in `source`: `i2s`, `alsa:<card>` or `none`). **`pip`** `{"corner": "tl"\|"tr"\|"bl"\|"br", "size": 25\|33}` (at least one): corner and width, in % of the canvas, of the small window. **`pause`** `{"on": bool, "text": "…", "mute": bool}` (at least one): a full-screen slate over the program and, with `mute`, no audio while it lasts; the text is trimmed of spaces and must be 1 to 60 characters, each a letter, digit, space or one of ``.,:;!?¡¿'"()-–—…·/+#@%€$*`` (the same rule in the app, the web and the engine). **`lock`** `{"on": bool}`: locks or unlocks the touch screen (ignores touches except the unlock one). **`audio_follow`** `{"on": bool}`: audio follows video (putting a source on air plays the audio linked to it; if it has none, the one playing continues). **`audio_delay`** `{"source": "i2s"\|"alsa:<card>", "ms": 0…500}` (integer; one with decimals or out of range gives `400`): delay of that input relative to the video. **`audio_link`** `{"video": "<source id from GET /mixer>", "source": "i2s"\|"alsa:<card>"\|"none"}`: the audio of that video source (`none` = no audio of its own). **`audio_mode`** `{"mode": "single"\|"mix"}`: one input plays (`main`, with "audio follows video") or the mix of the ones with `inMix`. **`audio_mix`** `{"source": "i2s"\|"alsa:<card>", "on": bool, "gain": −40…12}` (at least one of `on` and `gain`; a gain out of range or that is not a number gives `400`): `on` puts the input into or takes it out of the mix and `gain` is its volume in dB, in both modes (0 = no change). The engine applies them with a short fade. **`phones`** `{"on": bool, "gain": −40…0}` (at least one; integer dB, −40 is silence; out of range or with decimals gives `400`): the screen's headphones, the program audio over the board's HDMI ([`HEADPHONES.md`](HEADPHONES.md)); their state, in `audio.phones` of `GET /mixer`. `output` is not valid here (`400`): streaming is done with `PUT /output`, which also saves `OUTPUT`. Hot, without restarting the stream. `200 {"ok": true, "state": {…}}`. Errors: `400 bad_request` (validated here before sending it), `422` with the engine's code (`unknown_source`, `hidden_source`, `on_air`, `no_secondary`…; `unsupported` or `unknown_command` with an engine older than the command), `409 mixer_off` |
| `GET /mixer/source-<slot>.jpg` · `GET /mixer/screen.jpg` | Thumbnail of a source (320×180, one per second) and what the encoder's screen shows (1 fps). The engine writes them with rename. `404` without an image right now |
| `POST /service/start` · `/service/stop` · `/service/restart` | `202 {"state": "…"}`. The UI asks for confirmation before stopping or restarting if streaming |
| `POST /live` | Preview signalling: `{"sdp": "<offer>"}` → `200 {"sdp": "<answer>"}` (a single request, no trickle: the app is ICE lite). One preview per web session: a new offer from the same session replaces the previous one. `429` if there are already `-live-max` (3) from other sessions (counting the ones being negotiated, which drop after 15 s if they do not connect), `400` if the offer is not valid, `503` if the app started with `-live-max 0`. Body ≤ 64 KB |
| `GET /info` | `{"version", "web", "hostname", "l4t", "uptime", "deviceId"}` (`web` = fingerprint of the embedded interface: the page reloads by itself if it changes on reconnecting; `deviceId` = hash of `/etc/machine-id`; it is the linking's `hardwareId`) |
| `GET /cloud` | Link with EnhancedIRL: `{"state": "unlinked"\|"pairing"\|"linked", "code"?, "expiresAt"?, "linkUrl"?, "accountLabel"?, "online"?, "halted"?, "lastError"?}` (§10). `503 cloud_disabled` if the app started with `-cloud-api=` |
| `POST /cloud/pair` | `{}` → starts linking or renews the code: `200` + the same as `GET /cloud`. `409 already_linked`, `429 rate_limited`, `502 cloud_unreachable`/`cloud_error` |
| `DELETE /cloud` | Unlinks (or cancels a code in progress): deletes the link even if the cloud does not answer and notifies it with `DELETE /encoders/self`. `200` + the view + `"cloudNotified": bool` |

### Network cameras and their access point (F7, [`NETWORK_SOURCES.md`](NETWORK_SOURCES.md) §6)

Only with `eirl-netsetup` installed (otherwise `503 network_unavailable` and `/status` without `network`). A phone or
a camera joins the encoder's Wi-Fi and streams over SRT or RTMP; the engine reads it as one more source
(`net:camN`). At most `-cameras-max` cameras (1 in v1).

| Method and route | What |
|---|---|
| `GET /network` | `{"enabled", "accessPoint": {"enabled", "policy": "always"\|"rescue", "state": "off"\|"starting"\|"on"\|"error", "mode": "normal"\|null, "ssid", "channel", "band": "2.4", "ip": "10.42.0.1", "clients", "name", "error"?}, "lan": {"enabled", "state": "off"\|"starting"\|"on"\|"error", "iface": "eth0", "ip"\|null, "error"?}, "cameras": {"max", "items": [{"id": "cam1", "name", "createdAt", "connected", "protocol": "srt"\|"rtmp"\|null, "codec": "h264"\|"h265"\|null, "remote", "via": "wifi"\|"lan"\|null, "kbps", "rttMs", "lossPct"}]}}`. `accessPoint.name`: the AP's saved name (`network.json`, written at the app's first start), also with it off; `ssid`, the one it is up with (`null` off) (WIFI.md §14). **No secrets**: the same goes into `GET /status` (`network`) and, the access point, to the cloud. Each camera's state comes from MediaMTX's local API; the AP's, from what `eirl-netsetup` leaves in `/run/eirl-ap/state.json` (`starting` while it applies). `accessPoint.enabled` is the reception's "over Wi-Fi"; `policy`, "Create my Wi-Fi" (WIFI.md §2); `mode` `cameras` = that of an older `eirl-netsetup` |
| `GET /network/credentials` | `{"wifi": {"ssid", "channel", "password", "qr"} \| null, "cameras": [{"id", "srt", "rtmp", "rtmpServer", "rtmpKey", "lan": {"srt", "rtmp", "rtmpServer", "rtmpKey"} \| null}]}` with `Cache-Control: no-store` (`lan`: by cable, with `eth0`'s IP; `null` with the cable switched off or without an IP). The web only asks for it when opening "How to connect" or "Wi-Fi network". With `?qr=rows`, `wifi.qrRows`: the QR in rows of `1` and `0` (for the screen) |
| `GET /network/qr/wifi.svg` · `GET /cameras/{id}/qr.svg?proto=srt\|rtmp&via=wifi\|lan` | QR as SVG (generated in the app; the web loads nothing from outside) |
| `POST /cameras` | `{"name": "…"}` (1-32: letters, digits, space and `. _ ( ) ' -`) → `201` `{"camera", "connect", "accessPoint"}`; with the first one the network and its password are generated and the AP comes up. `202` if it was saved but the AP did not come up (the reason, in `accessPoint`). `409 limit` |
| `PATCH /cameras/{id}` | `{"name"?, "rotateKey"?: true}` → `200` `{"camera", "connect", "accessPoint"}` (`202` if the network was not applied). A new key kicks out the camera that streams with the old one (the app kicks it through MediaMTX's API ~1.5 s after applying it) |
| `DELETE /cameras/{id}` | `204`. With the last one, the AP goes down |
| `PUT /network` | `{"enabled": bool}` → `200` with the same as `GET /network` (`202` if it was not applied): the network cameras' RTMP/SRT reception (NETWORK_SOURCES.md §6.12). Off means neither receiver (MediaMTX) nor cameras in the mixer; the board's Wi-Fi (client and AP) stays as it is, and cameras, keys and network stay saved. Streaming with a network camera on air (main, secondary in PiP or side by side, or its audio playing alone): `409 on_air`; with `mixer.json` unreadable, `503 mixer_unreadable`; switching it on without Wi-Fi or cable, `409 no_input`. Off, `POST /cameras` gives `409 disabled`. Without cameras, it is only saved |
| `PUT /network/ap` | `{"ssid"?, "channel"?: "auto"\|"1"…"11", "enabled"?: bool, "rotatePassword"?: true}` → `200` with the same as `GET /network`, which carries `accessPoint` (`202` if it was not applied). With cameras it is applied: all of them reconnect (with a new `ssid`, they have to join the new name). `auto` with the AP on keeps its channel. Switching the Wi-Fi off with the cable off (and the reception on): `409 no_input`. A new `ssid` while streaming with a camera on air that comes in over that Wi-Fi: `409 on_air` (`503 mixer_unreadable` without the mixer state), the rule of `PUT /network/wifi {"ap":"rescue"}` (WIFI.md §14) |
| `GET /network/wifi` | The board's Wi-Fi (WIFI.md §5): `{"client": {"enabled", "state": "off"\|"disconnected"\|"connecting"\|"connected"\|"error", "ssid", "signal", "band", "channel", "ip", "internet", "error"?}\|null, "ap": {"policy": "always"\|"rescue", "state", "ssid", "channel", "clients", "error"?}, "warning": null\|"two_channels"}`. `client` is `null` without `wlan0`. No secrets. `GET /status` carries the client in `wifi` |
| `PUT /network/wifi` | `{"ap"?: "always"\|"rescue", "client"?: bool}` ("Create my Wi-Fi" and "Connect to a Wi-Fi"): → `200` with the same as `GET /network/wifi` (`202` if it was not applied). `always` brings the AP up even if there are no cameras (the first time it generates the network and its password); cameras do not force it; `rescue` while streaming with a camera on air that comes in over that Wi-Fi, `409 on_air`. `client` without a Wi-Fi client: `503 wifi_unavailable`; switching it off with the Wi-Fi client carrying the stream (SRTLA): `409 uplink_in_use`. Also with the board linked: the cloud does not manage it, it adopts it (WIFI.md §7) |
| `GET /network/wifi/scan` | `{"networks": [{"ssid", "signal", "security": "open"\|"wep"\|"wpa"\|"wpa2"\|"wpa3"\|"enterprise", "band", "channel", "known", "connected"}], "at"}`, at that moment (WIFI.md §11). `503 scan_busy` with nothing yet; `503 wifi_unavailable` |
| `POST /network/wifi/connect` | `{"ssid", "password"?}` → `202` with the same as `GET /network/wifi` (the result, in `client.state`/`client.error`). `400 validation` · `404 not_found` · `409 unsupported` · `409 client_off` · `409 uplink_in_use` (switching networks while the current one carries the stream). **The body is never logged** (not even the SSID) |
| `POST /network/wifi/forget` | `{"ssid"}` → `204` · `404 not_found` · `409 uplink_in_use` (the connected one, while it carries the stream) |
| `PUT /network/lan` | `{"enabled": bool}` → `200` with the same as `GET /network`, which carries `lan` (`202` if it was not applied): cameras by cable, over `eth0`'s network (NETWORK_SOURCES.md §6.11). Switching it off with the Wi-Fi off (and the reception on): `409 no_input`. Without cameras, it is only saved |

Errors: always `{"error": "<type>", "message": "<text for the user>"}` (or `fields` in `validation`).
Mutations: `Content-Type: application/json` required (`415` otherwise), body ≤ 16 KB (64 KB in `/live`).

### Local socket: the API for the engine's screen (Oct 13)

The touch screen is drawn by the engine (`eirl-engine`, running as the engine's user — today a regular login user;
a dedicated `eirl-engine` system user is planned — without privileges or `sudo`). For its
settings it uses **this same API** over a Unix socket, instead of a file or a state field per function.
That way there is a single contract, with the same validations and the same errors as the web. **Reboot and power
off do not go through here**: they still go through `screen.power` in the engine's state, so it can reboot even if the API hangs.

- **`/run/eirl-encoder/api.sock`**, 0660, group `eirl-encoder`, inside `/run/eirl-encoder` (0750, the app's
  `RuntimeDirectory`). The engine opens it through the group, which its drop-in gives it
  (`SupplementaryGroups=eirl-encoder`). Flag `-local-socket` (empty = no socket). Without it, the screen carries on with
  `wifi.json`, without settings.
- HTTP/1.1 over the socket, the same `/api/v1/…` routes, the same JSON and the same errors
  (`{"error", "message"}`; `message` in the request's language — the screen sends the device language — to be shown as
is, [`SETUP.md`](SETUP.md) §3). **No session or CSRF**: the
  authentication is the socket's permissions. Each request is logged with the caller's uid and pid
  (`SO_PEERCRED`), never with the body.
- **Whitelist** (the rest, `403 local_forbidden`). Since Oct 14 **everything the web configures can be configured on
  the screen**, secrets included (the owner's decision): the screen shows them as dots and reveals them with an eye
  button. Each route and why it is acceptable, in [`SETUP.md`](SETUP.md) §8:

  | Route | Over the socket |
  |---|---|
  | `GET /network` | Same as in the web |
  | `GET /network/credentials` | **The socket's own handler**: only the Wi-Fi (`{"wifi": …}`); with `?cameras=1`, the same as the web (`{"wifi", "cameras"}`, every camera's URLs with its key) for a camera's eye button. `?qr=rows` adds `wifi.qrRows` in both; another value of `cameras`, `400` |
  | `PUT /network`, `PUT /network/ap`, `PUT /network/lan` | Same as in the web; they answer with the same as `GET /network`. The screen renames "Create my Wi-Fi" with `PUT /network/ap {"ssid"}` (WIFI.md §14) |
  | `GET /network/wifi`, `PUT /network/wifi`, `GET /network/wifi/scan`, `POST /network/wifi/connect`, `POST /network/wifi/forget` | Same as in the web (WIFI.md §5). The password typed on the screen goes through here, directly: never through the engine's socket nor its state |
  | `POST /cameras`, `PATCH /cameras/{id}`, `DELETE /cameras/{id}` | Same as in the web (Oct 14); the answers carry the camera's URLs with its key |
  | `GET /destination`, `PUT /destination` | Same as in the web (Oct 14): `GET` masks the key |
  | `GET /destination/secrets` | **Only on the socket** (the web answers `404`): `200 {"streamId": "…", "passphrase": "…"}` in clear, as saved (`""` when unset), also with `managedBy: eirl`. `Cache-Control: no-store`; never logged |
  | `GET /status` | Same as in the web (Oct 14): the SSE snapshot, without secrets |
  | `PUT /uplinks/{interface}`, `PUT /input`, `GET /sources` | Same as in the web (Oct 14) |
  | `POST /service/restart` | "Apply" (Oct 14); `start` and `stop`, `403`. It restarts the engine, the caller: the screen never sees the `202` (nor the answer of a `PUT` with `"apply": true` that restarts), and the app does not cut the restart when the socket closes |
  | `PUT /password` | Same as in the web (Oct 14), with no session to keep: **every web session is closed**. The screen's wrong attempts are counted as one caller (5 per minute, then `429`); `503 setup_required` without a password |
  | `GET/PUT /device`, `GET /device/timezones`, `GET /setup`, `POST /setup/{password,step,complete,restart}`, `GET/PUT /sources/slots`, `GET /update`, `POST /update/{check,install}` | The device settings, the wizard (the setup code only here), the slots and the updates ([`SETUP.md`](SETUP.md) §8) |
  | `GET /cloud`, `POST /cloud/pair`, `DELETE /cloud` | The link, the pairing code (`?qr=rows` → `linkQrRows`) and unlinking (Oct 14). With a destination the cloud set, unlinking restarts the engine, and the cloud is still notified when the socket closes |

  Left out on purpose: `start`/`stop` of the service, reboot and power off (they go through `screen.power`), the logs,
  the mixer and `PUT /output` (the engine already is the mixer), the live preview, the QR images and the web session.
  Mutations have the web's body limit (16 KB). Widening it is a contract change: each new route is reviewed (what
  secret it returns, what it can break).
- The client (the screen) makes each call in a thread, never in the GLib loop, with one connection per
  request. Timeout of 3 s on GETs (8 s on `GET /status`, which can wait for the network and the Wi-Fi) and 150 s on PUTs: a network PUT waits until it is applied (0.5-1 s the
  reception, ~10 s if `ap0` is created, at most 2 min). Whatever secret it reads (the Wi-Fi password, the one
  typed, the stream key and the cameras' URLs an eye reveals) lives only in the screen's memory: never in the engine's state (`mixer.json` goes to all the
  web's subscribers) nor in the journal.

## 4. Live preview (WebRTC, phase 5)

v1 had a 640x360 JPEG preview at 2 fps, without audio (summary at the end of this section). Since
phase 5 it is **smooth video with audio over WebRTC, ~0.2–0.3 s behind the HDMI**.

**Why WebRTC and not HLS**: HLS cuts into 1–2 s segments and the player waits until it has 2–3;
it runs 3–6 s behind by design. LL-HLS goes down to ~1 s but GStreamer 1.14 does not include it, and desktop
Chrome would need a ~500 KB JS library. WebRTC works in every browser without
libraries (iPhone Safari too) and, on the LAN, without STUN/TURN servers.

**Mechanism**: the script encodes a small copy and sends it over RTP to localhost; the app
forwards it untouched.

```
… ! nvvidconv ! video/x-raw(memory:NVMM),format=NV12 ! tee name=vt ! nvv4l2h264enc …   (stream, unchanged)
  vt. ! queue leaky=downstream max-size-buffers=1
      ! nvvidconv ! video/x-raw(memory:NVMM),width=640,height=360,format=NV12,framerate=30/1   (VIC)
      ! nvv4l2h264enc control-rate=1 bitrate=800000 iframeinterval=30 idrinterval=30 profile=0 …
      ! h264parse ! rtph264pay config-interval=-1 mtu=1200 pt=96 timestamp-offset=0 ! udpsink 127.0.0.1:5006
audiomixer … ! tee name=at
  at. ! queue leaky=downstream ! opusenc bitrate=96000 frame-size=20 complexity=5
      ! rtpopuspay pt=111 timestamp-offset=0 ! udpsink 127.0.0.1:5008
```

1. **A second hardware encode, not the stream's.** Reusing the H.264 that goes over SRT
   does not work: in rehearsal mode it does not exist (no encoder: −470 mW), it is 6 Mbps of 1080p (a lot for a phone
   over Wi-Fi) in High profile with an IDR every 2 s. The copy is 640x360, Baseline, 800 kbps and an IDR every
   second: whoever opens the preview waits at most 1 s for the first picture.
2. **Opus in the script, not in the app**: WebRTC requires Opus, and encoding it in Go would need cgo (goodbye
   static binary). `complexity=5`: for listening to the preview it is not noticeable and it saves almost a point.
3. **It never slows the stream down**: `leaky` queues and `udpsink` without `sync`. With nobody watching, the app does not open
   the ports and the kernel discards the datagrams (same as the level meter).
4. **The app (`internal/live`, pion)**: ICE lite with host candidates (the browser connects to the
   Jetson's IP), one video track and one audio track shared by everyone watching, NACK to
   resend what gets lost over Wi-Fi. At most 3 previews at a time and one per web session (hiding and
   showing the tab again replaces its own instead of taking another slot); the session is revalidated
   every 20 s (a logout cuts it). Signalling in a single request (`POST /api/v1/live`).
5. **Audio/video sync**: the browser aligns them with the *Sender Reports* (RTP ↔ time pairs).
   pion's use the **arrival** time of each packet, and with HDMI the audio arrives ~100 ms later
   relative to its capture (the script's `audiomixer` has 100 ms of latency): it would desynchronise.
   The app generates them from the RTP timestamps, which come from the same pipeline clock
   (`timestamp-offset=0`): the same sync as the SRT output. Since the video timestamp wraps
   every 13.25 h and the audio one every 24.9 h, at startup it looks for the combination of wraps that
   puts both tracks at the same time (unique up to 8.3 days of continuous streaming). After a restart
   of the script, one track arrives before the other: it is realigned when both are new (or after
   3 s), with no reports in the meantime (found in the review: aligning a new one with an old one
   sent a report with the time off by minutes).
6. **Script restarts** (new SSRC, sequence and timestamps): the app rewrites sequence and
   timestamps so the browser sees a single continuous track, with a timestamp jump equal to the
   real time stopped (otherwise the RTP ↔ time line breaks and desynchronises for several seconds). The
   preview carries on by itself, without reconnecting.
7. **It works the same in caller mode and in rehearsal mode**: it reads from the pipeline itself, not from SRT.

Parameters (script): `LIVE=1`, `LIVE_SIZE=640x360`, `LIVE_VBITRATE=800`, `LIVE_ABITRATE=96`,
`LIVE_VPORT=5006`, `LIVE_APORT=5008`. App: `-live-video`, `-live-audio`, `-live-max 3` (0 = no
preview). With `LIVE=0` both branches disappear.

**Measurements** (2026-10-08). Cost on the board, each branch launched in a separate process alongside the real
stream and compared with a baseline without it (40 s, two rounds):

| Branch | CPU (% of a core) | `host_status` interrupts | `POM_5V_IN` |
|---|---|---|---|
| H.264 640x360 Baseline 800 kbps + RTP | +2.3 | no change (~5,800/s) | ~+60 mW |
| Opus 96 kbps, `complexity=10` (default) | +4.1 | — | ~+40 mW |
| Opus 96 kbps, `complexity=5` (chosen) | +3.4 | — | — |
| Opus 96 kbps, `complexity=3` | +2.6 | — | — |

App with one person watching (status, level meter and preview): **4.4 % of a core** (1.7 % without
WebRTC) and 20 MB of RSS; with 3 previews, ~10 %, within `CPUQuota=20%`.

In Chrome (Mac over the LAN, test source): 30 fps, 800 + 96 kbps, **0 losses, 0 frames
dropped or frozen** in steady state, decoding ~3 ms per frame, browser buffer ~110–150 ms,
**sync between −9 and +1 ms** (it is noticeable from ~45 ms). Delay estimated by parts:
~0.2–0.25 s (not measured end to end: the Mac's and the Jetson's clocks do not match to the millisecond).
Restarting the script with the preview open: same connection, the picture comes back by itself and the sync
stays within ±25 ms.

**v1's JPEG preview (replaced)**: `nvvidconv` 640x360 (VIC) branch → `videorate` 2 fps →
`jpegenc` (CPU) → `multifilesink` in `/run/eirl-engine/preview/`, served as `/preview.jpg` and
`/preview.mjpeg`. It cost +1 point. Lessons that still hold: the `tee` goes after the first
`nvvidconv` (the copy to NVMM is CPU, 4 MB per frame, and it must not be duplicated); the `framerate` goes
fixed in the scaler's caps (without it, with `v4l2src` in UYVY, `videorate`'s caps query
reached `v4l2src` empty and the pipeline did not negotiate: about 5 min without a stream in phase 1); and
`nvjpegenc` is discarded: at just 2 fps it raised the `host_status` interrupts from ~5,900 to
~12,500/s and its IRQ thread from ~8 % to ~17 % of a core. Its phase 1 stability test is still
the stream's reference: 10 min, 17,985 frames at 30.001 fps, 0 video or audio
gaps, SRT with 0 lost and a clean `dmesg`.

### Level meter (the extra)

Same idea for the audio, at the `audiomixer`'s output (what really goes on air):
`tee → queue leaky → udpsink host=127.0.0.1 port=5005 sync=false async=false` with PCM S16LE 48 kHz
stereo (~192 KB/s over loopback, about 100 small datagrams per second). The app computes peak and
RMS per channel in dBFS at 10 Hz and sends it over SSE. **Verified in phase 1**: with the receiver
closed, `udpsink` carries on without error (≈47 datagrams/s of 4 KB, 192 KB/s over loopback).

## 5. Privileges (without `NOPASSWD: ALL`)

System user **`eirl-encoder`** (no shell or home), groups `video`, `i2c` and `systemd-journal`.
The app only uses them to query, but `video` and `i2c` also allow writing (EDID, the
TC358743's registers): it matters if the app were compromised.

**What a compromised app could do** (after the phase 4 security review): stop,
start and restart the stream, and change the destination, including sending the stream with the
saved key to another server. It **cannot** execute code as the stream's user or as root:
the file it writes is read as data with a key whitelist (see below).

| Need | How |
|---|---|
| Start, stop and restart `eirl-engine` (and reboot or power off the board) | `/etc/sudoers.d/eirl-encoder`, **exact commands without wildcards** (validated with `visudo -cf` before installing it):<br>`eirl-encoder ALL=(root) NOPASSWD: /bin/systemctl start eirl-engine.service, /bin/systemctl stop eirl-engine.service, /bin/systemctl restart eirl-engine.service, /bin/systemctl reboot, /bin/systemctl poweroff`<br>The app calls `sudo -n` with those literal arguments. `/bin/systemctl reboot` (with no more arguments: not even `--force`) is EnhancedIRL's `reboot` command with the device linked (§10) and the Reboot of the screen's Power menu; `/bin/systemctl poweroff`, its Power off (MIXER.md §14: the engine requests it in `screen.power` and the app does it once per request, if it is < 15 s old). A compromised app could reboot or power off the board, which is the same as what it can already do by stopping the stream, and it gains nothing more. Since Oct 8 it is the **only** passwordless thing on the board: the temporary `<user> … NOPASSWD: ALL` was removed (copy in `/etc/sudoers.bak-20261008`) |
| Writing the configuration | **Its own file, not `/etc/default/eirl-engine`** (see below): `/etc/eirl-encoder/` (`root:eirl-encoder 3770`: setgid, and sticky so the group, which the engine has, cannot unlink or rename the app's files; the app only reads its files there if they are regular files it or root owns, never through a link: `internal/safefile`, so a name the engine created first is ignored) with `engine.env` and `admin.hash` (`0640`, group `eirl-encoder` through the setgid), `cloud.json` (`0600`: it carries the cloud token; the streaming service, which comes in through the group, does not read it) and `identity` (`0600`, the device's secret for the cloud, §10) |
| `eirl-engine` using that file | Drop-in `/etc/systemd/system/eirl-engine.service.d/eirl-encoder.conf`: `Environment=EIRL_CONF=…`, `SupplementaryGroups=eirl-encoder eirl-usbhub` (only the service gets those groups: the first, to read it; the second, to cut the power of a hung capture card's port, MIXER.md §16; the app does not have it) and `NoNewPrivileges=yes` (the engine runs as the engine's user, which on the test board has passwordless sudo for deploying: without this, a bug in the engine would give root; F5 security review). It is **not** an `EnvironmentFile`: the script reads it as **data** and only accepts `SRT_URL SRT_MODE SRT_LATENCY SRT_STREAMID SRT_PASSPHRASE VBITRATE ABITRATE OUTPUT ABR VBITRATE_MIN EDID_PREFERRED`. Next to it, `uplinks-disabled` (disabled uplinks), which the script rereads every 2 s, also as data with a whitelist. As an environment, whoever could write it would slip in `BASH_ENV`, `LD_PRELOAD`, `GST_PLUGIN_PATH` or paths (`RECORD`) and execute code as the engine's user (review finding; tested that they are now ignored) |
| Service state | `systemctl show` needs no privileges |
| `eirl-engine`'s journal | Group `systemd-journal` |
| HDMI signal and `AU_STATUS0` | Groups `video` and `i2c` (only `QUERY_DV_TIMINGS` and a 1-byte I2C read) |
| Power (ina3221) | `/etc/tmpfiles.d/eirl-encoder.conf` with `z …/ina3221x/6-0040/iio:device0/in_*_input 0444`: opens **reading** of those files (telemetry) on every boot |
| Port 80 | `AmbientCapabilities=CAP_NET_BIND_SERVICE` in the unit, without root |

**Network cameras (F7, [`NETWORK_SOURCES.md`](NETWORK_SOURCES.md) §6.2-6.3).** The app does not touch the network: it writes
into `/etc/eirl-encoder` `cameras.json` (the keys, **0600**), `ap.psk` (the Wi-Fi password, **0600**),
`network.json` (SSID and channel, 0640) and `cameras-public.json` (id and name, without keys, 0640: it is read by the
source catalogue that the engine launches), and starts `eirl-ap.service` (`sudo -n /bin/systemctl start
eirl-ap.service`, same sudoers rule, without extra arguments): a oneshot with its own hardening
that launches `eirl-netsetup apply` as root. Not as a child of the app, which would pass its sandbox
(`ProtectSystem=strict`) and its limits on to it. `eirl-netsetup` reads those files as data:
without following links, regular files only, with a size cap, **only if their owner is `eirl-encoder`**
(the directory is 3770 and the engine is in the group) and with the same whitelists. It sets up `ap0`, the
NetworkManager profile (keyfile 0600: the password does not go through arguments), the DHCP
(`eirl-ap-dhcp.service`), `ap0`'s firewall and MediaMTX's configuration; MediaMTX runs as its
own unprivileged user (`eirl-media.service`) with the keys hashed. For the screen, the app
leaves `/run/eirl-encoder/wifi.json` (0640, the unit's `RuntimeDirectory`) with the Wi-Fi's SSID, password and
QR; not the cameras' keys. MediaMTX's local API asks for the key in
`/etc/eirl-encoder/media-api.key` (0640, generated by the app: the app and the engine read it through the group; MediaMTX
only stores its hash). No IPv6 on `ap0` and with the firewall in place before the AP comes up. With "cameras by
cable" (NETWORK_SOURCES.md §6.11), RTMP and SRT listen on `0.0.0.0` and the `EIRL-MEDIA` chain (set up earlier)
only lets them in through `lo`, `ap0` and `eth0`.

**Why its own file and not `/etc/default/eirl-engine`**: the atomic write is a temporary file
+ `fsync` + `rename()` in the **same directory**, and for that the app would need write
permission on the whole of `/etc/default`. With `/etc/eirl-encoder/` the permission is limited to its own.
`/etc/default/eirl-engine` keeps what does not belong to the user (`SOURCE`, `SIGNAL_TIMEOUT`,
watchdog thresholds) and the destination and bitrates are migrated to the new file once, at
installation. The app only writes the keys it manages; the rest it neither reads nor touches.

`install.sh` runs as root, but **does not operate inside `/etc/eirl-encoder`**, which is writable by the
app: a symbolic link placed in time would make it `chown` or `chmod` another file (e.g.
`/etc/passwd`). What is inside it writes as the app's user (`runuser`).

**Discarded**: polkit (0.105 cannot limit to one unit); `.path` units watching a
command file (they avoid `sudo` and would allow `NoNewPrivileges`, but they are asynchronous, with no exit
code, and add three units and a parser running as root: more pieces for the same thing); the app as root.

Unit `eirl-encoderd.service`: `User=eirl-encoder`, `Nice=10`, `IOSchedulingClass=idle`,
`CPUQuota=20%`, `MemoryLimit=64M` (cgroup v1 on 18.04), `ProtectSystem=strict`,
`ReadWritePaths=/etc/eirl-encoder`, `ProtectHome=yes`, `PrivateTmp=yes`, `ProtectControlGroups=yes`,
`Restart=on-failure`, `GOMAXPROCS=1`, `AmbientCapabilities=CAP_NET_BIND_SERVICE`.

**No `NoNewPrivileges` nor anything that enables it underneath**, because it cancels `sudo`'s setuid
("effective uid is not 0"). Tested one by one on the board with `systemd-run` (2026-10-08): on systemd
237, `ProtectKernelModules=yes` and `ProtectKernelTunables=yes` break it, just as
`PrivateDevices`, `SystemCallFilter`, `RestrictAddressFamilies` or `MemoryDenyWriteExecute` would. It is the
price of the sudoers rule; the user has no `CAP_SYS_MODULE` anyway.

Checked that sudo **denies** (`rc=1`) anything else: another unit, `stop eirl-engine` without
`.service`, extra arguments, a shell.

The `eirl-engine` drop-in adds `RestartPreventExitStatus=64`: with an invalid configuration the
script exits with 64 and the service stays `failed` with the reason in the journal, instead of being relaunched
every 2 s. Saving a valid configuration with `apply` starts it again.

## 6. Interface security

> **With the device linked to EnhancedIRL (§10)**, the cloud can do the same as the local web **plus two
> powers the local web does not have without the password**: `reboot` and `set_local_password` (changes the
> local password without knowing the current one, for whoever forgot it). A compromised EnhancedIRL account
> could therefore lock the owner out of the local portal. Counterweight **pending for phase 3**: the
> setup code (`/etc/eirl-encoder/setup-code`) will ALWAYS get into the local portal as a
> rescue credential (contract `ENCODER_CLOUD.md` §10). None of this allows executing code or taking
> the stream out of EnhancedIRL's relays (D9).

- **LAN only**: listens on `:80` and rejects (`403`) any source IP that is not RFC 1918,
  link-local, ULA, loopback **or from the subnets of the device's own interfaces** (on a network with IPv6 the
  phone comes in with its global address from the same /64). Defence in depth in case someone opens
  the port on the router.
- **Host**: only literal IPs, `<hostname>`, `<hostname>.local` and `localhost` (`403` otherwise): it closes
  DNS rebinding (a foreign website that re-resolves its domain to the Jetson's IP).
- **Limits**: `ReadTimeout` 20 s (the SSE removes it), headers ≤ 16 KB, 64 connections at a time,
  bodies ≤ 16 KB (64 KB the WebRTC offer). The SSE and the preview revalidate the session every 20 s: a
  logout or a password change cuts them.
- **WebRTC preview**: the signalling goes through the API (session, CSRF, LAN only); the media goes over UDP to
  an ephemeral port of the app, protected by the ICE credentials and the DTLS fingerprint that only the
  response to that request carries (encrypted SRTP). No STUN/TURN: nothing leaves the LAN.
- Administrator **password**, bcrypt (cost 10, ~0.1 s at a time on one core) in
  `/etc/eirl-encoder/admin.hash`. In v1 it is set at installation
  (`sudo -u eirl-encoder eirl-encoderd passwd`, read from stdin). With no password set, the API
  answers `503` to everything: there is never an open device.
- **Login attempts**: 5 per minute per IP (IPv6: per /64) and 20 per minute in total, recorded
  **before** bcrypt (concurrent ones count too) and one bcrypt at a time. The global limit lets
  someone on the LAN block the login for a minute: accepted.
- **Session**: random 32-byte token in memory, cookie `HttpOnly; SameSite=Strict`. A
  restart of the app closes the sessions (acceptable).
- **CSRF**: `SameSite=Strict` + a check of `Origin`/`Sec-Fetch-Site` on every mutation.
- **Changing server requires giving the secrets again**: a `PUT` that changes the destination's host
  without sending `streamId`/`passphrase` (or `null` to remove them) gives `400`. Otherwise, a stolen session
  would send the saved key to its own server by changing only the URL. Switching to listener or changing
  only the port does not require it.
- **Secrets never in clear**: the API does not return the streamid or the passphrase (only
  `publish:AbCd…wXyZ`, or `publish:••••` if it has more `:`, and `set: true`); an `SRT_URL` saved by
  hand with `?streamid=…` is not returned (`url: ""`, `urlInvalid: true`); the forms send them once
  and do not display them again; the app does not log them; **every journal line goes through a redactor**
  (`streamid=`, `passphrase=`, `publish:<…>`/`read:<…>` with `:` inside), even though the script no longer
  prints them. Checked on Oct 8 that the old journal keeps no key in clear.
- **The cameras' Wi-Fi** (F7): whoever joins it reaches the portal (80, with password), RTMP 1935 and SRT 8890
  (with each camera's key) and the WebRTC preview (UDP 50000-50099, fixed ports since F7); the
  rest, SSH included, is cut by `ap0`'s firewall. No forwarding or NAT: a camera does not reach the
  LAN nor go out through the board's uplinks.
- **HTTP without TLS in v1** (see §9): on the LAN, the passphrase travels in clear once, when it is saved.
- Headers: `Content-Security-Policy: default-src 'self'; …; form-action 'none'; base-uri 'none'`
  (no CDN, everything embedded; if the JS failed, a native form submission would not put the secrets in
  a URL), `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`. The web only uses `textContent`.

Remaining risk the app does not solve: `srt-live-transmit` receives the URL with streamid and
passphrase **in its arguments**, so any local user sees the key with `ps`. It has no option
to read it from a file. Today the only local user is the maintainer; if there are ever more, it will have to be
replaced.

## 7. Changes to `eirl-engine.sh` (✅ done in phase 1)

> Historical: these were made to the former bash launcher. The Go engine (`manager/internal/engine`)
> keeps the same behaviour.

All with the usual procedure (replace with `mv`) and **a single cut** of the stream, announced:

1. **Preview branch** (§4) and **level-meter branch**, with `LIVE=0`/`LEVELS=0` to disable them
   (the preview was JPEG until phase 5).
2. **SRT statistics to tmpfs, only the latest sample**: today `SRT_STATS=<file>` makes
   `srt-live-transmit` **append** JSON endlessly (~1 KB/s ≈ 86 MB/day: unworkable 24/7, and in `/tmp` it goes
   to the SD). It is sent through a pipe to a one-line reader that rewrites
   `/run/eirl-engine/srt-stats.json` with an atomic `rename()`. The exact format of SRT 1.5.7's
   JSON is checked on the board before writing the parser.
3. **Mask the streamid** in the startup log, same as the passphrase.
4. **Defensive validation** of the numeric variables (`[[ $VBITRATE =~ ^[0-9]+$ ]]`, etc.) and of
   the streamid and passphrase whitelist before building anything. If something does not add up, it exits with
   an error in the journal instead of executing.
5. Silence the `bytes lost` every 5 s **without** losing `Accepted SRT target connection` / `SRT target
   disconnected`: `-q` swallows everything, so stderr goes through a filter (`sed -u`) that removes that
   line and **masks streamid and passphrase**, because `srt-live-transmit` prints them in clear in
   `Media path: …`.
6. **Rehearsal mode** (`OUTPUT=off`): capture with preview and level meter, without the main encoder, without mux and without SRT.
7. (phase 4) Reads `EIRL_CONF` **as data with a whitelist** (§5); leaves in `/run/eirl-engine/state` the mode,
   the source and the **fingerprint** (`CONF_SHA`) of the configuration it started with (the app compares it with
   its own: "unapplied changes" without depending on the clock, which on the Jetson jumps with NTP); without a destination
   with `OUTPUT=on` it exits with 64 (not 1, which relaunched in a loop); `LC_ALL=C`; FIFO in `/run/eirl-engine`.
8. (F2 of `ENCODER_CLOUD.md`, not deployed) **Thumbnail** for the EnhancedIRL app: `THUMB=1`
   (`THUMB_SIZE=320x180`, `THUMB_EVERY=5`, `THUMB_FILE=/run/eirl-engine/thumb.jpg`). A single new block
   after the level meter's (so that merging with `feat/sources` is trivial), hanging off the preview's
   `tee` (`vt.`, NV12 in NVMM; if `LIVE=0` the block creates the `tee`): a 1-buffer `queue leaky` →
   `nvvidconv` (VIC) to 320×180 I420 **with the framerate in the caps** (`${FPS_FRAC}`, which comes with
   `feat/sources`; if it is not there, `${FPS}/1`) → `videorate drop-only` to 1/5 → `jpegenc quality=75` (CPU;
   never `nvjpegenc`) → `multifilesink`. Same order as v1's JPEG preview (tested with
   `v4l2src`): `videorate` before the scaler, in NVMM, would hold a 1080p buffer from the
   stream's pool. Also in rehearsal mode; each start deletes the previous one (on stopping it stays: the app does not send
   one older than 15 s). **Measured on Oct 11** (separate pipelines with `videotestsrc` 1080p30 UYVY → NVMM →
   `tee`, with and without the branch, 40 s, three rounds, with no other pipelines on the board; the real stream
   was waiting for a signal and was not touched): **+1.9 points of a core** (+1.4, +1.9 and +2.3), `host_status` with no
   change (~5,900/s), `host_syncpt` +30/s (one VIC operation per frame) and ~+55 mW. JPEG of ~7 KB.
   Also tested inside the full script (test source; in rehearsal without preview and streaming as a
   listener without a client with preview and level meter): it negotiates and writes every 5 s. **Still to do**: seeing it with the
   real HDMI capture (`v4l2src`) when deploying.

## 8. Phases (with a stop and an OK at each one)

| Phase | What | Stream cut |
|---|---|---|
| **0** | This plan | No |
| **1** ✅ | Script changes (§7) and measuring the preview against the criteria in §4. Done on Oct 8 (validation, rehearsal, statistics and level meter tested separately, with `SOURCE=test` on another port) | Yes: ~5 min because of the negotiation failure and about 6 short cuts to measure |
| **2** ✅ | `eirl-encoderd` without the web, deployed on Oct 8 (§11). Tested with `curl` against the board: login, 401/403/415, status, SSE with level meter, redacted logs, preview, restart/stop/start through sudo, rehearsal mode, broken configuration → `failed` → repaired from the app. App: **0.0 % CPU at idle, 1.6 % of a core with one client watching (status + level meter), 14 MB of RSS** | Several restarts to test the control (with nobody connected) |
| **3** ✅ | Minimal mobile web, deployed on Oct 8. Tested in Chrome with an iPhone viewport (390×844): login, per-field errors, pasting a key's URL (caller + mask, no trace in the DOM), unapplied-changes warning, save and apply, rehearsal ↔ stream with a real SRT client connected, stop and start, level meter. App with the page open: **1.7 % of a core, 15 MB**. **Still to test on a real phone** | The ones from the tests (with nobody connected) |
| **4** ✅ | Wrap-up (Oct 8). Code review (10 findings) and security review (2 medium, 7 low, 6 hardening): no injection exploitable from the API; **all fixed** except the accepted ones in §6 and the items in §12. The big ones: the app's file is no longer the service's environment (§5), migration without root inside the app's directory, login limit without a race, Host and LAN IPv6, "unapplied changes" by fingerprint and not by clock, `SOURCE=test`. **`NOPASSWD: ALL` removed** and checked that the app still controls the stream with its rule. 27 test functions (with subtests), also with `-race` | Two restarts (new drop-in and sudo test) |
| **5** ✅ | **WebRTC preview with audio** (Oct 8, §4), requested after testing v1: it replaces the JPEG one. Measured without cutting the stream (branches in a separate process), tested end to end with a second app on another port and the test source, reviewed (1 sync bug after restarts and 5 minor ones, fixed; no security findings) and deployed: app installed by the maintainer with `build.sh`, script replaced with `mv` and restart from the app; it starts fine with the real HDMI (1080p30, small encoder in Baseline) and the RTP reaches the app without drops. Pending: testing it on the phone | One (~10 s; the board was in rehearsal mode) |

The app's budget (met in phase 2): at idle **~0 % CPU and < 20 MB of RSS**; with a
phone watching, **< 3 % of a core** (4.4 % since phase 5, with the WebRTC preview: see §4). Rehearsal mode: −470 mW versus streaming (no encoder or SRT).

## 9. Decisions (taken on 2026-10-08)

1. **Configuration in `/etc/eirl-encoder/engine.env`**, separate, through a drop-in. Yes.
2. **HTTP** on the LAN (internal test).
3. **Initial password through the CLI** at installation; for the product, one per device.
4. **Rehearsal mode**: yes, also for the final product. It is already in the script (`OUTPUT=off`: capture,
   preview and level meter, without the main encoder or SRT). *Superseded on Oct 12 by the mixer's F3:
   there is no rehearsal mode; the preview is always on and the output is attached hot.*
5. **Slim down the board**: everything that is surplus. Done in phase 1 (see the README).
6. **Minimal** web, without translations. Free choice of language: **Go** is kept (static binary without glibc,
   v4l2 and I2C ioctl without cgo, same pattern as `eirl-failover`). *(Translations superseded on Oct 14: the
   interface is translated es/en/fr, still without a build step: `i18n.js` and `lang-*.js`, see §2.)*
7. **Real-time preview with audio** (requested after testing v1): WebRTC and not HLS (§4).

## 10. Linking with EnhancedIRL (phase F2 of `ENCODER_CLOUD.md`)

The contract (routes, messages, close codes, configuration) is `devops/docs/active/ENCODER_CLOUD.md`
(§3.1, §4, §5 and §8). Here, how the device implements it.

- **`internal/cloud`**: generic with respect to the device (interface `Device`). eirl-encoderd uses it with the
  Jetson (`api.CloudDevice`: everything goes through `config.Store`, the unit with its sudo commands and `auth`,
  same as the local web) and the simulator with a fake one.
- **State**: `/etc/eirl-encoder/cloud.json` (`0600`): `encoderId`, `token`, `apiBase`, `accountLabel` and,
  beyond the contract, `appliedVersion` (reported in `hello`), `accessPoint` (phase 3 will use it) and
  `cloudDestination` (the destination was set by the cloud). The token never goes to the logs (nor the linking
  code, nor the `pollSecret`, nor `set_local_password`'s password).
- **Identity**: `/etc/eirl-encoder/identity` (`0600`), 32 random bytes in base64url generated when
  asking for the first code and sent in `POST /encoders/pairing`. They prove to the cloud that it is the SAME
  device: re-linking a `hardwareId` that is still active in another account is only allowed with it (otherwise,
  `hardware_claimed`: the web says "This encoder is still linked to another account. Unlink it from that
  account or write to support"). **It is not deleted on unlinking** (neither from the web, nor with `4401`, nor
  with `unlink`); it only changes if it is reinstalled (deleting `/etc/eirl-encoder`). A file that exists and is not
  valid is an error (it is not regenerated by itself). Never to the logs or the web.
- **Linking**: `POST /encoders/pairing` → the web shows `XXXX-XXXX` and `<-cloud-app>/encoders/link?pairing=XXXX-XXXX`
  → polling `/token` every `interval` until approved, denied (reason in `lastError`), `410` or 10 min.
  A `429` while polling doubles the interval (max. 30 s).
- **Who manages the destination** (`managedBy`): the link decides it (`cloud.json`), not the
  `EIRL_MANAGED_BY` key of the `.env` (`config.Store.ManagedBy`). That way linking or unlinking does not rewrite the
  `.env` and the web does not warn of "unapplied changes" that do not exist. Linked: `PUT /destination` → `409`
  (HDMI input, rehearsal mode and uplinks can still be changed locally).
- **Channel**: `wss://<apiBase>/encoders/connect`, `Authorization: Bearer`, subprotocol `eirl-encoder.v1`,
  library `github.com/coder/websocket` (pure Go, no dependencies: the binary stays static,
  10.6 → 11.5 MB). Reconnection 1 → 60 s with ±30 % jitter, from 1 s again after 60 s connected; after
  `1013`, never before 30 s. With nothing from the cloud in 90 s (it sends `ping` every 25 s), the connection is considered
  dead. `4401` (or **two** consecutive `401`s on the upgrade) → deletes the link; `4409`/`4426` → stopped
  until the app restarts (the web says so).
- **Messages**: `hello` with the effective configuration without secrets; `status` (the `/status` snapshot
  mapped to `EncoderStatus`, without destination, key or IPs) every 60 s, every 2 s with `watch` and immediately if
  the phase changes (checked 1.5 s after a journal line about start, stop, signal, source or
  failure, or after a command; never more than one snapshot per line in a burst). The phase, the audio state and the
  `input` fields the local API adds (multiple sources: `waiting_device`, `usb`, `source`…) go
  through as is; of `signal`, only `kind` and `measuredFps` go up in addition. `event`: the journal's `kind`s from
  §4.1 (also the multiple-source ones: `device_missing`, `device_lost`, `format_unsupported`, `usb2`,
  `audio_missing`, `device`) with a flat `detail` (redacted `message`, `step`, `ok`) +
  `stream_started`/`stream_stopped` on entering or leaving `streaming` + `uplink_up`/`uplink_down`
  (`detail: {uplink}`) by checking `/run/eirl-uplinks/available` every 5 s. `thumbnail` every 5 s with `watch`
  (only if there is a new one);
  its own limit of 10 messages/s for `status`/`event`/`thumbnail` (the critical ones always go through).
  `watch` expires after 60 s without renewal.
- **`config.set`**: strict whitelist (unknown or missing fields → error), the SAME rules
  as the local API (`config.Settings.Validate`) and **D9**: a host that does not end in `.enhancedirl.com` (nor an
  IP, nor a listener) → `errors.destination = "destination_not_allowed"`. `errors` carries per-field CODES
  from the contract (`video.bitrateKbps: "invalid"`…); saved but unable to apply (failed
  restart) → `config: "apply_failed"` and it does not count as applied. The URL arrives without a query (the key only
  in `streamId`); if it brought a different `?streamid=`, error. It writes the `.env` and the disabled
  uplinks; with `apply` (false if absent), it restarts **only if something changed** or there were unapplied
  changes (the cloud may resend the same one on reconnecting and must not cut the stream); stopped on
  purpose is respected. On linking, `appliedVersion` = 0; on reconnecting, the last one applied successfully.
- **Commands**: `start` and `stop` = the web's Go live / Stop streaming buttons (they save `OUTPUT`;
  with the mixer, hot; if the service is stopped, they start it; `start` without a destination →
  `no_destination`). `stop` **no longer** stops eirl-engine: the preview carries on. `rehearsal` = `stop`, for
  compatibility. `restart` restarts the whole of eirl-engine. The client sends `status` immediately when
  the phase or `outputState.state` changes (ENCODER_CLOUD.md §13.3);
  `reboot` (answers `result` and reboots 1 s later with `sudo -n /bin/systemctl reboot`);
  `set_local_password` (bcrypt to `admin.hash` and closes **all** local sessions).
- **Unlinking** (from the web, `unlink` or `4401`): if the destination was set by the cloud, it is deleted (the key
  belongs to the account) and it stops streaming (the preview carries on); a destination set locally before linking is respected.
- **Thumbnail**: a branch of the engine's pipeline (§7, item 8) that leaves `/run/eirl-engine/thumb.jpg` (320×180, every 5 s). The
  app only reads it with `watch`, and discards a half-written one (without an end marker) or an old one (> 15 s).
- **Web**: "EnhancedIRL account" card (Link → big code and link; Linked to … · connected or
  not · Unlink with confirmation). It is queried every 3 s with a pending code and every 15 s otherwise,
  only with the tab visible.

**Backends** (checked in `devops/kubernetes/apps/<env>/enhancedirl/enhancedirl/eirl-{backend,app}/values.yaml`):

| Environment | `-cloud-api` | `-cloud-app` |
|---|---|---|
| prod (default) | `https://api.enhancedirl.com` | `https://app.enhancedirl.com` |
| dev (test Jetson) | `https://api.dev.enhancedirl.com` | `https://app.dev.enhancedirl.com` |

The options go in `/etc/default/eirl-encoderd` (`EIRL_ENCODERD_OPTS=…`, owned by root: the app cannot
switch backends by itself); `install.sh` creates it empty (= prod) if it does not exist. `-cloud-api=` disables
linking. An existing link uses the `apiBase` it was made with, not the flag's.

**Simulator** (`cmd/eirl-encoder-sim`, same `internal/cloud`): to develop the app without the Jetson and
for level A E2E tests.

```bash
cd manager && go run ./cmd/eirl-encoder-sim -api https://api.dev.enhancedirl.com -app https://app.dev.enhancedirl.com
#   pairing code=XXXX-XXXX link=https://app.dev.enhancedirl.com/encoders/link?pairing=XXXX-XXXX
go run ./cmd/eirl-encoder-sim -api http://localhost:3000 -state /tmp/sim1.json -json   # E2E: one JSON line per event
go run ./cmd/eirl-encoder-sim -state /tmp/sim1.json -unlink                             # DELETE /encoders/self and deletes the state
```

The simulated device's identity goes in `<state>.identity` (`-identity`) and survives `-unlink`, as on the
Jetson; deleting it = a reinstalled device. It starts without streaming (`ready`) with a 1080p30 signal; it obeys `config.set` (with D9) and the commands; streaming with a destination
it gives SRT, adaptive bitrate and consistent uplinks; `-signal-flap 2m` loses the signal for 10 s every 2 min;
`reboot` cuts the channel for 15 s; a test-card thumbnail with the phase's colour. It imitates the mixer's F3
(`ENCODER_CLOUD.md` §13.3): Go live and Stop streaming (`stop` and `rehearsal`) change the output
hot, without restarting, and `outputState` goes through `connecting` (2 s) → `on`; `error` with
`no_destination` without a destination or `no_uplinks` with SRTLA and all uplinks off. `-legacy` imitates an
older device: phase `rehearsal`, Go live/Rehearsal restart and `stop` stops the service.

**Deviations from the contract** (the most conservative ones; to be confirmed in `ENCODER_CLOUD.md`):
1. The script's thumbnail branch works **always** (0.2 fps), not only with `watch`: `gst-launch` cannot
   be switched on and off from outside without restarting the stream. Measured: +1.9 points of a core (§7).
2. `set_local_password` accepts **10-72 bytes**, not 10-128: bcrypt does not look beyond 72 (and `x/crypto`
   rejects them). The backend should limit to 72.
3. `cloud.json` also carries `appliedVersion`, `accessPoint` and `cloudDestination`.
4. A `401` on the upgrade (the contract does not cover it) counts as `4401` only if it repeats twice in a row.
5. `GET /api/v1/cloud` adds `linkUrl`, `halted` and `lastError`; `DELETE` returns `200` with `cloudNotified`.
6. `start` = Go live (sets `OUTPUT=on`), like the local web's button; without it there would be no command to come back
   from rehearsal.
7. Unlinking deletes the destination if the cloud set it and switches to rehearsal (the contract only says "goes back to the
   wizard").
8. Capabilities announced today: `abr`, `edid`, `thumbnail` and `srtla` (if `srtla_send` is there); `wifi` and `ap`
   come with phase 3. `network` in `status` is off until then.

> **G1 finding (Oct 12):** a `config.set` without a key chosen in the app travelled with `destination: null` and
> the device deleted the destination the owner had set locally. Now `destination` **absent** = the device's
> destination is not touched (if it has none and streaming is requested, it stays in rehearsal); `null` = remove it
> (only when the cloud manages it and the key is no longer valid). Contract `ENCODER_CLOUD.md` §5.

## 11. Install, operate and undo

Code in `manager/` (Go, without cgo). Tests: `cd manager && go test ./...` (they run on the Mac; what
touches hardware has a Linux build tag).

```bash
JETSON=jetson manager/build.sh                      # tests + linux/arm64 binary + install.sh on the board
JETSON=jetson manager/build.sh --restart-engine   # and restarts the stream if the drop-in changed
```

`install.sh` (idempotent, as root) creates the `eirl-encoder` user (groups `video`, `i2c`,
`systemd-journal`), `/etc/eirl-encoder` (`root:eirl-encoder 3770`), the **first time** migrates the
destination from `/etc/default/eirl-engine` to `/etc/eirl-encoder/engine.env` (and comments those keys out there,
with a `.bak-<date>` copy), and installs the binary, unit, drop-in, sudoers (validated with `visudo -cf`) and
tmpfiles, the SRT output (`srt-live-transmit` and its `libsrt` in `/usr/lib/eirl-encoder/lib`), and the boot splash (`splash/install.sh`, see [`SPLASH.md`](SPLASH.md)).
On a **new device** (no `/etc/default/eirl-engine` to migrate) it writes `engine.env` with the output **off** and no
destination, and creates a commented default `/etc/default/eirl-engine` (root, 0644; `SOURCE=hdmi`), never
overwritten afterwards. With the engine in the package (a release bundle) it also enables `eirl-engine` and
`eirl-hdmi-init`. `install.sh --image` is for an SD card image's chroot ([`UPDATES.md` §9](UPDATES.md#9-testing)). Without `--restart-engine` it **does not cut the stream**. The board no longer has passwordless sudo:
`build.sh` uses `ssh -t` and sudo asks for it (it cannot be deployed without someone in front). It has to be
launched from a real terminal: a shell without a TTY (such as an AI coding assistant's `!` command) makes sudo fail with `no tty present`.

The drop-in sets `EIRL_CONF`, which the engine reads (`internal/engine/config`); it takes effect with
`build.sh --restart-engine`.

- **Administrator password**: `sudo -u eirl-encoder eirl-encoderd passwd` (through stdin) and
  `sudo systemctl restart eirl-encoderd`. The test device's password is in the maintainers' secret store.
- **Access**: `http://jetson.local` or `http://<board-ip>` (port 80, LAN only).
- **Logs**: `journalctl -u eirl-encoderd` (method, route and code; never bodies or secrets).

Undo: for SRTLA, see [`SRTLA.md` §8](SRTLA.md#8-install-and-undo); the boot splash, [`SPLASH.md` §4](SPLASH.md#4-build-install-and-undo). The rest: `sudo systemctl disable --now eirl-encoderd`, delete `/usr/local/bin/eirl-encoderd`,
`/etc/systemd/system/eirl-encoderd.service`, `/etc/systemd/system/eirl-engine.service.d/eirl-encoder.conf`,
`/etc/sudoers.d/eirl-encoder`, `/etc/tmpfiles.d/eirl-encoder.conf`, `/etc/default/eirl-encoderd` and `/etc/eirl-encoder`
(with `cloud.json` and `identity`: unlink first from the web to revoke the token in the cloud; without
`identity`, to the cloud it is a new device); restore
`/etc/default/eirl-engine.bak-<date>`; `sudo userdel eirl-encoder`; `daemon-reload` and restart `eirl-engine`.

## 12. Pending (after v1)

- **Testing it on a real phone**, above all the WebRTC preview in iPhone Safari and over Wi-Fi
  (only tested with Chrome on the Mac and emulating an iPhone).
- **Real audio**: the test source sends no audio (`AU_STATUS0=0x80`); level meter and preview audio
  only tested with the test source.
- **Latency and A/V sync of the stream** (SRT): not measured end to end (they need the tower with
  `clock.html` or the ball test). The preview's, measured with the browser's statistics (§4).
- For a product: streaming with a **dedicated user** (today the engine's login user, with the script in its home),
  `STATE_DIR` outside `/tmp`, **TLS** and a per-device password, and **pairing** (§10).
- Accepted risks (§6): HTTP without TLS on the LAN and the key visible in `srt-live-transmit`'s `cmdline`
  to local users.
