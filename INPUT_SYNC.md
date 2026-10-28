> **Status:** PLAN — design agreed with the maintainer (Oct 23-24): optional, dynamic, with an optional calibration; D1-D5 decided (§6). S0 on the board (§7): buffers, detector, MediaMTX, the RTSP timecode and the Logi against an HDMI camera done; the USB capture card, the screen's delay and HDMI timecode pending
> **Updated:** 2026-10-24

# Lining the inputs up to the slowest one

Goal: the same moment looks and sounds the same on every input of the mixer. If someone claps in front of an HDMI
camera and of a phone sending over SRT, the clap lands on the same program frame on both, so a cut between them does
not jump back or forward in time. The engine does it by delaying every input to the slowest one it waits for.

It is **optional and off by default**: off, the engine times the sources exactly as today (MIXER.md §12, §13, §22)
and costs nothing more. On, each input's latency keeps itself up to date (§3.1); a **calibration** in front of the
touch screen is available, but never required (§3.2).

## 1. What the engine does today

- Each source's bridge (`gstc.c`, `on_sample` → `next_pts`) stamps a frame with its **capture time on the board**
  plus `MIXER_SYNC` (120 ms), on the program's timeline. `CaptureJudge` falls back to the **arrival** time when the
  capture stamps are not believable (age outside [−5 ms, 150 ms]; 400 ms for network sources): the MS2109 capture
  card and the Logi always end up there.
- Network sources add `MIXER_NET_SYNC` (150 ms) to their video and audio.
- Each audio input can be delayed by hand (`audio_delay`, 0-500 ms, MIXER.md §13).
- The screen's tiles share the program's timestamps (MIXER.md §22).

None of that sees **what happens before the board**: the camera's own processing, the phone's encoder, the Wi-Fi,
SRT's buffer. Measured (NETWORK_SOURCES.md, "Latency and jitter with a real phone"; an iPhone at 1080p30, glass to
glass):

| Path | Latency |
|---|---|
| HDMI over CSI | 66-80 ms from capture to arrival, plus the camera's own (unknown) |
| Phone, SRT latency 200 ms | 0.55 s |
| Phone, SRT latency 500 ms | 0.81 s |
| Phone, Moblin's default (3000 ms) | 3.35 s |
| Phone, RTMP | 0.35-0.38 s, with an arrival − PTS spread of 120-520 ms |

So a clap seen by an HDMI camera and by a phone over SRT comes out about 0.4-0.8 s apart in today's program (the
network sources' extra 150 ms covers only part of it), and the sources with arrival stamps are behind their own
audio by their capture time.

## 2. The model

For each input *i*:

- `L_i`, its **latency**: from the scene to the frame's mark on the board (the capture stamp, or the arrival one
  when `CaptureJudge` rejects the capture stamps).
- `pts_i = mark_i − L_i + D`: each frame goes at the moment it shows, plus a common wait `D`.
- `D = max(MIXER_SYNC, max(L_i + a_i) + margin)` over the inputs that are waited for, capped at `MaxSyncWait`
  (1 s, §3.9). `a_i` is the p99 of the age of the input's frames on arrival (the bridge measures min/avg/max today;
  S1 adds the p99); the margin is one program frame.
- An input whose `L_i + a_i` does not fit under the cap, or that the user marked **don't wait**, keeps today's timing
  and is shown as not in sync.

With the function off, nothing changes: `D = MIXER_SYNC` and the network sources keep `MIXER_NET_SYNC`.

## 3. Design

### 3.1 Per input: mode and origin

Each input has a **mode**, saved with the scene by source id (it survives reconnecting the device):

| Mode | `L_i` |
|---|---|
| **Automatic** (the default with the function on) | the best origin available, below |
| **Manual** | a value in ms typed by the user |
| **Don't wait** | today's timing; it does not count for `D` |

In automatic mode the origin is, best first (the state says which one is in use):

1. **The sender's timecode**: continuous, frame by frame (§3.3).
2. **Calibration**: that device's last measurement in front of the screen (§3.2). For an SRT source it fixes the
   device's own part, and the SRT buffer stays live (§3.4).
3. **Live estimate**: SRT's negotiated latency plus the device's base; otherwise a value per source type, measured
   in S0 (§3.4).

A calibration is dropped by itself when the device's mode changes (resolution, frame rate), since its internal
latency may change with it. Values outside [0, `MaxSyncWait`], or unstable (a spread of more than two frames over
2 s), are discarded and the next origin is used, as `CaptureJudge` does with the stamps.

### 3.2 Calibration in front of the screen (optional)

Nothing requires it: without it, every input works with its estimate. It is there for the HDMI and USB cameras,
whose own latency the engine cannot see, and to check the result by eye.

- **The touch screen's "Sync" page** shows a timecode pattern: big vertical bars with the screen's frame number in
  Gray code, between guard bars (the idea of `latency/clock.html`, sized to be read from a few metres). The engine
  knows the moment each screen frame is shown, so there are no clocks to synchronise.
- While the page is open, a **detector** looks for the pattern in every input, on a small grey copy of each source
  (≈320×180 at 10 fps) read by the CPU. When an input sees it steadily for ~2 s, its latency is saved
  (`origin: calibrated`) and the page says so. Only while the page is open: closed, the detector's branch does not
  run.
- Three ways in: **Calibrate** on a source (the page, for that one), **Calibrate all** (a guided pass, one source
  after another, or all at once if they all see the screen) and just opening the page and pointing cameras at it.
  Any time, as often as wanted. **Forget calibration** goes back to the estimate.
- Gray code: between two consecutive values only one bar changes, so a frame caught mid-change reads one of the two
  (±1 screen frame); averaging over 2 s brings the error under one program frame.
- The screen's own delay (from the compositor's timestamp to light on the panel) is the same for every input, so it
  cancels out between them. It is measured once in S0 to combine calibrations with absolute timecodes (§3.3).

### 3.3 The sender's timecode (automatic)

Only some senders carry the real capture time, and only some protocols take it to the engine:

- **RTSP and WebRTC (WHIP)**: yes. The sender's RTCP sender reports map its RTP timestamps to NTP time, and
  MediaMTX passes them on to its readers with `useAbsoluteTimestamp: true` (off by default; the template is netcfg's,
  NETWORK_SOURCES.md §6). `rtspsrc ntp-sync` does **not** work with it in GStreamer 1.14: `rtpbin` only associates
  a sender report that comes with an SDES CNAME, and MediaMTX sends none (§7). So the engine reads the reports itself,
  from the jitterbuffer's `handle-sync` signal (in 1.14), and maps each frame's RTP timestamp to the sender's time.
  `L_i` = arrival − capture time, frame by frame, provided the board's clock and the sender's are synchronised by NTP
  (the board runs timesyncd).
- The sender's clock and its reports have to be right: in S0 a Mac was 79 ms behind NTP, and ffmpeg's reports carry
  a constant offset of about half a second (§7). A constant offset is fixed by calibrating that device once (the
  timecode then follows the changes); a negative or unstable value is discarded (§3.1).
- **SRT and RTMP**: no absolute time. **Moblin over SRT, today's usual phone, does not have it**; neither do the HDMI
  and USB sources.
- **WebRTC is off** on the board (`webrtc: false`); opening a WHIP input would be a separate initiative.
- **HDMI**: some cameras embed timecode in the signal. Whether the TC358743 lets us read it is a question for S0,
  not a promise.
- Without NTP on the board, the timecoded sources stay in step with each other; to place them against the local
  ones, one of them has to see the pattern once (the offset is kept until the board's clock jumps).

### 3.4 Live estimate (automatic, no action)

- **SRT**: `L_i = base + TSBPD`, where TSBPD is the receive latency negotiated for the connection, read from
  MediaMTX's API (`api: true` on 127.0.0.1 in the template): `msReceiveTsbPdDelay` in `GET /v3/srtconns/list`,
  which follows the negotiated latency (the larger of the sender's and the receiver's 120 ms; checked in S0, §7). The
  base is the device's calibration if there is one, otherwise ~0.35 s (what the phone
  measurements give: camera, encoder, Wi-Fi, MediaMTX, `rtspsrc`'s 100 ms jitterbuffer, NVDEC). TSBPD is fixed for
  a connection, so this only changes when the phone reconnects with another latency.
- **RTMP**: the type's base; its large arrival spread goes into `a_i`.
- **HDMI, USB and the rest**: a value per type, measured in S0 with `latency/measure.py`.

### 3.5 Applying changes without jumps

Changing an input's offset makes it jump: a freeze (more delay) or a skipped stretch (less). So:

- **An input that is not in the program** (not on air, not in a PiP, not sounding) takes its new `L_i` at once:
  nobody sees it.
- **An input in the program** takes it at the **next cut** to or away from it, unless its frames would arrive late
  with the old value: then at once (losing frames is worse than one jump).
- Hysteresis of one program frame: smaller changes are ignored.
- The bridge applies a smaller offset by **dropping** frames until the new timestamps pass the last one sent (today's
  `next_pts` would squeeze them 1 ms apart).
- **`D` is common to everything**, video and audio: moving it freezes or skips the whole program and its sound. So:
  - **not streaming**, `D` follows the slowest input by itself;
  - **streaming**, `D` does not move by itself. An input that stops fitting goes to *don't wait* with a warning, and
    the screen offers **Resync** (one jump, accepted by the operator). `D` goes down only when the stream stops;
  - to avoid surprises on air, `D` is computed with **every source in the scene**, also the ones not connected yet
    whose latency is known (a calibrated phone that will join later).

### 3.6 Optional

- **Off (the default)**: today's timing, today's queues and pools, no detector, no polling of MediaMTX. The switch is
  saved with the scene.
- **On**: the costs of §3.9, and **don't wait** per input.
- The web and the screen show, per input: mode, origin, `L_i`, whether it waits and whether a change is pending for
  the next cut; at the top, `D`.

### 3.7 Audio (D5)

- The audio that travels **in the same signal** as a camera's video (the board's HDMI over I2S, a capture card's HDMI
  audio, a network stream's audio) **inherits its video's `L_i`**: what the camera does to the picture before the
  board, it does to the sound.
- **Microphones are synced on their own** (I2S without HDMI, USB sound cards, a camera's own mic such as the Logi's,
  Bluetooth). Their `L` is 0 by default; a Bluetooth input's is the delay BlueALSA reports (`delayMs`,
  BLUETOOTH.md §3), which today makes it arrive late with no way to fix it (BLUETOOTH.md §4, risk 5).
- **Every audio input can be aligned by hand**, on top of that: with the function on, `audio_delay` also takes
  negative values (bring the audio forward). An audio brought forward counts for `D` like a slow input, so the limit
  is what the cap leaves. Manual changes apply **at once** (they are made by ear, with the screen's headphones,
  HEADPHONES.md); automatic ones follow §3.5.
- The audio queues (`aq%d`, 1 s today) are sized from `D`; audio is ~0.2 MB/s per input.
- Later and optional, if asked: **calibrate a mic with a clap**, cross-correlating it with the audio of a camera whose
  latency is known.

### 3.8 The screen's tiles (D1)

**Aligned with the program**: the tiles keep sharing its timestamps (MIXER.md §22), so the whole screen shows what
goes out, `D` behind real life, also when cutting. Their frames wait already converted to their small size (RGBA,
~130 KB at 240×134), so `tileQueue` is sized with `D` instead of `MIXER_SYNC`: ~5 MB per tile at 1 s.

### 3.9 Buffers and memory (D2)

Where the frames wait: each program slot is `appsrc → queue max-size-buffers=10 leaky=downstream → nvvidconv
output-buffers=10 → RGBA → pgm`. The compositor's pad keeps about its `latency` (60 ms), and the rest waits in that
queue, which drops the oldest when it is full. With `D` up to 1 s at 60 fps it would drop, so with the function on it
becomes time-based (`max-size-time` = `D` + margin), before the conversion: the frames wait in NV12 (≈3.1 MB at
1080p), not in RGBA (≈8.3 MB). Those frames are the source's (its `SinkTail` pool, 20 buffers today), so the pool has
to hold them too, or the source stalls.

Extra frames per input ≈ (`D` − `MIXER_SYNC`) × fps. At 1080p60 (half at 30 fps), with up to 4 slots (`MaxSlots`):

| Case | `D` | Per fast input | 3 fast inputs |
|---|---|---|---|
| HDMI and USB only | ~0.2-0.25 s | ~8 frames, ~25 MB | ~75 MB |
| Plus a phone, SRT 200 ms | ~0.6 s | ~30 frames, ~95 MB | ~280 MB |
| Plus a phone, SRT 500 ms | ~0.9 s | ~47 frames, ~145 MB | ~440 MB |
| The cap | 1 s | ~53 frames, ~165 MB | ~500 MB |
| Plus a phone at Moblin's default (3.35 s) | does not fit: *don't wait* | — | (it would be ~0.6 GB per input) |

Available RAM with the engine running and not streaming: ~2.6 GB (IMAGE.md §11.2).

**D2: `MaxSyncWait` = 1 s.** It covers phones over SRT up to a 500 ms latency, and RTMP; the reserve below is
~0.45-0.75 GB, a fifth to a third of the free RAM. A slower source (Moblin's default, 3 s) is not waited for, and the
screen suggests lowering its SRT latency to 500 ms or less.

Measured in S0 (§7): 200 more 1080p NVMM buffers (615 MB) allocate and run cleanly next to the engine, and **the pool
does not grow**: `nvvidconv output-buffers` is a fixed size, and a pool of 20 holding 60 frames stalls the source. So
with the function on, each local source's pool is sized for the cap when the function is turned on: 20 +
`MaxSyncWait` × the source's fps (+60 buffers, ~185 MB, at 60 fps; half at 30), which rebuilds the sources' tails
once (it shows on their tiles: better before going live). Network sources are the slow ones and keep theirs. Worst
case, four local sources at 60 fps: ~0.75 GB; a usual mix (HDMI at 30, two USB at 60): ~0.45 GB.

### 3.10 Contract (additive)

Socket (MIXER.md §5); a new command gets `unknown_command` from an old engine, and the web copes with it:

| Command | Arguments | Errors |
|---|---|---|
| `input_sync` | `on`: bool | `bad_request` |
| `input_latency` | `source`: video source id; `mode`: `auto` · `manual` · `nowait`; `ms`: 0…`MaxSyncWait`, with `manual` | `bad_request`, `unknown_source`, `sync_off` |
| `sync_calibrate` | `source`: video source id or `all`; `on`: bool (the page open or closed) | `unknown_source`, `sync_off` |
| `sync_forget` | `source`: video source id | `unknown_source` |
| `sync_resync` | none: apply the pending `D` now (one jump) | `sync_off` |
| `audio_delay` | as today; with the function on, `ms` may also be negative | an old engine: `bad_request` |

State: at the top, `inputSync: {on, delayMs, capMs, pendingDelayMs}`; per video source,
`sync: {mode, origin, latencyMs, waits, pending, calibratedAt}`, where `origin` is `timecode` · `calibrated` ·
`estimate` · `manual`; per audio input, its effective `latencyMs` and `origin`. Saved in `mixer-scene.json` by source
id. The app validates and bounds everything (`manager/internal/mixer`) and the local API repeats it (MANAGER.md §3).

## 4. Risks

- The pools are fixed (§3.9): turning the function on rebuilds the local sources' tails once. How long that takes per
  source type (the CSI one above all) is measured in S1.
- The sender reports' quality varies per device (§3.3): plausibility, and a calibration for a constant offset.
- Rolling shutter, exposure and the screen's refresh blur the pattern: Gray code, guard bars and discarding unstable
  readings.
- With `D` up to 1 s the touch screen feels slower (D1): accepted.
- A timecode depends on NTP at both ends: a sender with a wrong clock gives nonsense, hence the plausibility check
  (§3.1).

## 5. Phases

| Phase | What | Needs |
|---|---|---|
| S0 | Measurements and spikes (§7). Done on Oct 24: NVMM headroom and pool growth, `nvvidconv` to grey for the detector, `rtspsrc`'s NTP in 1.14 with MediaMTX and an RTSP sender, the SRT field of MediaMTX's API. Also the Logi against an HDMI camera (~195 ms apart). Pending: the USB capture card, the screen's delay, timecode over HDMI (a camera that sends it) | a board slot agreed with the maintainer; someone to point the cameras |
| S1 | Engine, off by default: the switch, `L_i` with mode and origin, `D`, the bridge's p99, applying at the cut, queues and pools, audio with the same `D` and a signed `audio_delay`, contract and tests | S0 |
| S2 | Automatic latencies: SRT from MediaMTX's API, timecode over RTSP (the netcfg template), plausibility | S1 |
| S3 | The calibration: the "Sync" page and the detector | S1 |
| S4 | Web and screen (es/en/fr): the switch; per input mode, origin and value; calibrate; don't wait; Resync; warnings; audio alignment | S1-S3 |
| S5 | Validation on the board: every camera pointing at the pattern within ±1 program frame, on the program and on the tiles, also while streaming; a 2 h soak watching RAM; docs; release | the maintainer's OK to publish |

## 6. Decisions

| # | Question | Maintainer |
|---|---|---|
| D1 | The tiles: aligned with the program, or live | **Aligned** (Oct 24) |
| D2 | The cap of the common wait | **1 s, the recommendation** (Oct 24; §3.9) |
| D3 | Optional? | **Yes, off by default** (Oct 23) |
| D4 | Calibration | **Available and optional, never required**; the latencies keep themselves up to date (Oct 23-24) |
| D5 | Audio | **It inherits its video's latency only in the same signal; microphones on their own; every audio input can be aligned by hand** (Oct 24) |

## 7. S0: measured on the board (Oct 24)

Image 0.11.1, output off, the engine running and untouched (same start, no errors, its timings unchanged); every test
in separate processes. MemAvailable at the start: 2.54 GB; `NvMapMemUsed` 805 MB.

| What | How | Result |
|---|---|---|
| NVMM pool, does it grow? | `videotestsrc` 320×180 → `nvvidconv output-buffers=N` → NV12 1080p60 NVMM → a queue holding H frames (`min-threshold-buffers`) | N=20, H=60: **stalls** (0 frames). N=80, H=60: 60 fps. The pool is fixed |
| NVMM headroom | N=200, H=180 | 60 fps, no errors; `NvMapMemUsed` +615 MB (**3.08 MB per buffer**), MemAvailable 2.54 → 1.92 GB, all of it back at the end |
| Detector branch | the same source plus a `tee` → `nvvidconv` → GRAY8 320×180 in system memory | **+1.1 points of a core per source at 10 fps** (+3.5 at 60); `nvvidconv` outputs GRAY8 |
| `rtspsrc ntp-sync` (1.14.5) | a test MediaMTX 1.21.1 (other ports, separate from `eirl-media`), an RTSP sender from a Mac (ffmpeg), paths with `useAbsoluteTimestamp` on and off, read with `ntp-sync=true` and a sink that reports late buffers | No effect: `rtpbin` gets the sender reports but never associates them, because they come **without an SDES CNAME** (0 SDES in the reports MediaMTX sends) |
| The sender's time through MediaMTX | a small RTSP client: arrival − sender time of each frame from the RTCP sender reports | Off: 12 ms (MediaMTX's own time). On: −417 ms, steady (p10-p90 16 ms): **MediaMTX passes the sender's time on**; this sender's reports are ~0.5 s off (ffmpeg's mapping, and the Mac 79 ms behind NTP) |
| SRT's negotiated latency in the API | `srt-live-transmit` from the Mac at 200, 500 and 60 ms | `msReceiveTsbPdDelay` in `/v3/srtconns/list`: 200, 500 and **120** (the receiver's default minimum); also `msRTT` and `msReceiveBuf` |
| The board's clock | NTP queries from the board | −2 to −5 ms against pool.ntp.org and time.apple.com: good |

**Latency per camera** (the same day, the maintainer at the board): both cameras filming `latency/clock.html` full
screen on a Mac, each one on air in turn, the program streamed to the test destination (SRTLA to eu1) and read back
on the Mac; the bars found in the picture by themselves, the guards and the mod-13 bits checked, 1 frame in 3 decoded.
The absolute values carry the whole chain (SRTLA and the read-back's buffers, the relay, decoding), the same for both;
**the difference is what counts**:

| Camera | Path, stamp | Age on arrival | Glass to glass, p10 / p50 / p90 |
|---|---|---|---|
| Logi 4K Stream Edition, 1080p60 | USB, arrival stamp | 21-25 ms | 1302 / **1324** / 1346 ms |
| A camera's HDMI output, 1080p30 | CSI, capture stamp | 68-76 ms | 1401 / **1519** / 1546 ms |

So **the HDMI camera comes out ~195 ms (6 frames at 30 fps) behind the Logi** in today's program: a cut between them
jumps back that much. It is the camera's own processing (sensor to HDMI out), which the capture stamp cannot see,
exactly what `L_i` is for. Lining up local sources only needs these differences; the absolute values matter only
against a timecode (§3.3).

Found on the way: the TC358743 had lost its configuration (sleep mode on, no DDC, no hotplug: a known hang after a
voltage dip), so a camera plugged into the HDMI saw no sink. Rewriting the EDID raises the hotplug but does not wake
the chip, and the driver's `hardware_reset` needs the reset pin this board does not have: a clean power-off and
unplugging the board fixed it.

Still pending: the USB capture card (the same HDMI camera into it, to separate the capture path from the camera), the
screen's own delay, and timecode over HDMI (needs a camera that embeds it; whether the TC358743 exposes it is
untested).
