> **Status:** CLOSED (Oct 14) — the Go engine is the only engine: M0-M6 in main, the Python engine retired (§16); open outside C1: the in-place HDMI reset (MR !72, draft)
> **Updated:** 2026-10-15

# The Go engine (`eirl-engine`)

The owner (Oct 13): "The hdmi2srt engine was meant to be a test, but it looks like it has stuck around.
I want you to plan migrating it to Go and tuning it properly so it is optimal."

**Proposal:** a new Go binary, `eirl-engine` (the service name since Oct 13), that replaces
`eirl-engine.sh`, `engine-run.py` and the `engine_*.py` modules.
- **Contract:** the same as today (socket, `mixer.json`, files in `/run/eirl-engine`, configuration), so
  neither the app, nor the web, nor the cloud notice the change, and the screen looks the same.
- **Division of work:** control goes in Go. Whatever is done to each frame goes in C (cgo) and never
  enters Go.
- **Rollout:** in phases, with the Python engine as the fallback until a clean night of
  broadcasting.
- **Timeline:** about 3-4 weeks. The bottleneck is board slots and the board's power supply.

## 1. Why

| Problem | Data | Where it was seen |
|---|---|---|
| Python's GIL slows down the screen | Drawing takes 0.4-0.9 s per frame while broadcasting with the CPU at ~70%; program→screen of 2-3 s with a slow source. The screen does not respond during that time | Oct 13, F7 test (engine without the CPU savings) |
| Every frame goes through Python | `on_sample` → timestamp → `push` in each source's thread: 4 sources × 60 fps, plus the multiview and the screen, competing for the GIL | MIXER.md §4, §17 |
| Fragile PyGObject 3.26 | Timers that are lost on the first failure (they had to be wrapped in `guarded`); `TypeError: Must be Gst.Message, not Message` at startup (Oct 13 19:42); races while iterating dictionaries | MIXER.md §16 |
| Tied to Python 3.6 | Unsupported since 2021 and pinned by L4T R32 | — |
| Memory and startup | 115-175 MB RSS; ~4.5 s from the service to the multiview (Oct 12: the engine, then hdmi2srt, at 6.9 s from board boot, multiview at 11.4 s) | Oct 12 and 13; memory `encoder_mixer_plan` |
| Code | 8,621 lines: one file of 4,596 and a 1,068-line bash script (`eirl-engine.sh`) that builds the pipeline; the configuration is validated twice (bash and Python) | `scripts/` at `895f90d` |

**What Go does not fix on its own.** Most of the CPU goes into the GStreamer and NVIDIA elements, the
VIC interrupts (host1x, ~10% of CPU0) and USB. The language change saves what Python spends today.
Exactly how much is measured in M0: the estimate is 10-25 points of one core. Its main value is that
the screen and commands always respond, and that the engine is robust and maintainable. The big savings
come from tuning (§5), which is easier on Go.

## 2. What does not change (contract)

- The socket `/run/eirl-engine/mixer.sock`, with line-delimited JSON and the same commands and errors (MIXER.md §5). The
  state in `mixer.json` and in subscriptions, with the same fields.
- `/run/eirl-engine/{state,video,abr,srt-stats.json,screen.jpg,source-N.jpg,thumb.jpg}`, and the WebRTC preview
  over the same UDP ports to the app.
- The configuration: `/etc/default/eirl-engine`, `EIRL_CONF` (`/etc/eirl-encoder/engine.env`) and the same keys, validated only once.
- `eirl-engine.service` (and `eirl-hdmi-init.service`): the same name, the ordering with the splash, `NoNewPrivileges`, the groups `eirl-encoder`
  and `eirl-usbhub`, and the bounded stop. Only `ExecStart` changes. State lives in `/var/lib/eirl-engine` (`StateDirectory`).
- What the engine reads from others:
  - from `eirl-encoderd`: `eirl-encoderd probe -list/-all`;
  - from the app: `cameras-public.json` and `wifi.json`, its local API (what `engine_app.py` does today), the
    file of disabled uplinks and the file of uplinks without internet (`uplinks-nointernet`, WIFI.md §11);
  - the rest: the `SIGHUP` to `srtla_send` and `hw-reset.request`.
- The tools `tools/deploy-engine.sh`, `eirl-enginectl`, `engine-cpu.sh` and `engine-watch.py`.
- What went into the Python engine after this plan was written, and is part of parity: uplinks without internet (c9, MR !42, contract in WIFI.md §13) and
  headphone monitoring on the screen (c1, MR !41, HEADPHONES.md: command `phones`, `audio.phones`).
- The first addition that only the Go engine has (the Python one is frozen, E4): slots 1-4 with an alias (§12). It is
  additive, and the old engine answers `unknown_command`, which the app tolerates.
- The rules already settled: the 26 pitfalls in MIXER.md §3, the F5 robustness work (§16), the CPU savings (§17)
  and keeping secrets out of the socket, the state and the log (WIFI.md §6).

## 3. Architecture

```
eirl-engine (Go)                                   C (cgo), per frame
├─ config, scene, socket commands, state ───────┐   ├─ source → program bridge: appsink → timestamp → appsrc
├─ one goroutine per source (open, watch,       │   ├─ multiview limit (MV_GAP) and thumbnails
│  reopen, USB recovery)                        │   ├─ cadence (new frames/s) and level meters
├─ audio bus, output (srt/srtla, ABR)           │   └─ counters that Go reads every second
├─ screen: drawing and touches in its goroutine │
└─ own gst layer (~30 calls into GStreamer) ────┘   GStreamer 1.14 + NVIDIA elements (unchanged)
```

- **Separate process** (`manager/cmd/eirl-engine`, in the same Go module as the app): if the engine dies, the app
  keeps running, as today. It shares the contract types with `manager/internal/mixer`.
- **Own minimal GStreamer layer.** About 30 calls: `parse_launch`, states, bus, properties, pads,
  clock and `base_time`, appsrc/appsink and probes. Not go-gst: its bindings are generated from GIR and tested
  against recent GStreamer releases, and the board has 1.14.5 with GLib 2.56. With our own layer, we control
  the hot path.
- **No frame enters Go.** The bridge from each source to the program is a C callback:
  - the capture timestamp, the sync margin and the timestamp judge are given by Go as parameters;
  - the same goes for the multiview limit, the thumbnails and the cadence;
  - Go changes them on the fly and reads counters.

  In M0 it is compared with `proxysink`/`proxysrc` (available in GStreamer 1.14), which would do it without custom code.
- **Isolated platform.** A `platform` interface with the NVIDIA elements and caps (`nvvidconv`,
  `nvv4l2*`, `nvcompositor`, `nvdrmvideosink`, `nvv4l2camerasrc`) and their pitfalls. It is the "platform
  profile" left over from F5, and it prepares for the RK3588 without solving it.
- **Screen in pure Go** (like `eirl-splash`): rasterized with `golang.org/x/image`, and DejaVu loaded from
  `/usr/share/fonts`, so it looks the same. It is drawn in its own goroutine, by regions and with caching. Touches,
  via evdev. Nothing slows it down: neither the GIL nor a slow command.
- **Pipelines.** The same graphs, generated from Go with per-platform templates and tested as they are now
  (`csi_desc` and friends). Single-source mode (`MIXER=0`) goes away (E5).
- **Build.** cgo for arm64 in the Mac's `l4t-r32.7.6-build` Docker image, with the board's GStreamer 1.14
  headers. The binary links the system libraries, and `tools/deploy-engine.sh` installs it.

**Names and language.** Everything in English: the binary and the service (`eirl-engine`), the packages, the
identifiers, the comments and the log messages (CLAUDE.md, "Names and language"). Log lines
carry a stable event code, for example `[engine] event=signal_lost …`, so that the app and the cloud do not
depend on the text: today `manager/internal/service/journal.go` recognizes events by the Python engine's Spanish
phrases, and the change to those rules goes in the same MR as the engine that emits them (M1).

## 4. Phases

| Phase | What | Gate | Days |
|---|---|---|---|
| **M0 · Measure and prototype** | (a) `py-spy` on the board (standalone binary, read-only, with permission): how much Python spends in each thread, broadcasting and not broadcasting. (b) A Go prototype with cgo: 4 test sources in NVMM, C bridge at 60 fps, compositor and NVENC; CPU and latency versus Python. (c) `proxysink`/`proxysrc` versus the C bridge | The owner decides from the numbers whether to continue | 2 |
| **M1 · Skeleton and sources** | `eirl-engine` with configuration, program pipeline and output to the FIFO (what `engine-run.py` does). HDMI sources (`nvv4l2camerasrc`), USB (`probe -all`), network and test. Scene, composition, fades, Pause. Socket and `mixer.json` identical | Works with the real web and app, with `MIXER_SCREEN=0` | 4-5 |
| **M2 · Audio and output** | Audio bus: I2S, ALSA and network, «sigue al vídeo» (follow video), mixing, gains, delays and uplinks. Hot output: SRT/SRTLA, ABR, statistics and retries. USB recovery via VBUS. Watchdogs | Broadcasts to eu1 without drops | 3-4 |
| **M3 · Screen** | Multiview and interface: header, Settings, wifi and keyboard, Power, lock, touches. A render to PNG (what `ui_render.py` does) to compare it with Python's | The owner approves the PNGs | 4-5 |
| **M4 · Parity** (in parallel) | The 135 Python tests, ported to Go. A checklist with the 26 pitfalls and the F5 and §17 items | All green | — |
| **M5 · Shadow run and switchover** | Installed alongside Python: `MIXER_ENGINE=go\|python` in `/etc/default/eirl-engine`. Long overnight test with the three sources broadcasting. If it comes out clean, Go becomes the default, and Python stays 2 weeks as the fallback before it is removed | Clean night | 2-3 + 1 night |
| **M6 · Tuning** | §5, with before and after under the same conditions | Targets in §5 | 3-5 |

M2 and M3 can run in parallel (two sessions), once the M1 contract is closed. What needs the board: M0 (b)
and the M1-M3 tests in short board slots without broadcasting, M5 with the night run, and M6.

## 5. Tuning

Each change is measured with `tools/engine-cpu.sh` and `tools/engine-watch.py`, under the conditions of MIXER.md §17.

- **No GIL and no frame in Go**: comes from M1.
- **Fewer VIC jobs.** Each `nvvidconv` is a VIC job plus its host1x interrupts.
  Candidates:
  - have the compositor scale when the size already matches, instead of one `nvvidconv` per source;
  - a single conversion per source for the multiview and the thumbnails;
  - do not convert what is going to be dropped, as was already done in step 1 of §17.
- **Affinity and priority**, together with the base session (BASE.md, B5):
  - host1x and USB 3 interrupts, off the capture and compositor cores;
  - slightly higher priority only for those threads, never real time for the whole process.
- **Screen**:
  - repaint only the regions that change;
  - no per-frame copies (today `Gst.Buffer.new_wrapped` costs 1.1 ms);
  - no frame if nothing moves.
- **Memory**: custom buffer pools, no copies.
- **Startup**: GStreamer registry cache and no interpreter.

Targets, to be confirmed with the M0 numbers:

| Measurement (MIXER.md §17) | Today | Target |
|---|---|---|
| Engine, HDMI 1080p30 + 2 USB, not broadcasting, preview stopped | 96% of one core (Python); Go: 63% (HDMI 1080p50 + 2 USB, Oct 14, §15.1) | ≤ 75% |
| Slowest screen draw, broadcasting | 0.4-0.9 s (F7 engine); Go, M5 night: p50 37 ms, p95 47 ms, max 89 ms (`draw_max_ms` of the `timings` event) | ≤ 50 ms |
| From touch to seeing it on the screen | not measured; Go: 35 ms to the UI frame + about one frame of the screen's compositor (§15.4) | ≤ 100 ms |
| Engine RAM | 105 MB not broadcasting (Go, 2 arenas + malloc_trim + GOGC=50, §15.1); the M5 night grew to 254 MB with the reconnections | ≤ 80 MB |
| From service start to multiview | ~4.5 s (Python); Go after M6: on an engine restart the engine is up in 0.6-0.7 s and multiview takes ~4-5 s, held by the panel; at boot, 6.1 s (cold GStreamer, §15.4) | ≤ 2 s |
| Night broadcasting with 3 sources | Oct 14: 7 h 25 min, 0 restarts, 0 panics (§14) | 0 restarts, 0 tracebacks |

## 6. Risks

- **cgo with GStreamer 1.14** (references, threads, callbacks): minimal layer, stress tests on the board and
  no frames crossing into Go.
- **Parity**: the Python engine carries many solved pitfalls that are not visible. The M4 checklist and the
  M5 shadow night cover this.
- **Other sessions touch the Python engine** (screen, wifi, base): freeze it for the duration (E4).
- **Board power supply**: without a stable supply there is no overnight test (Oct 13: four power cuts).
- **RK3588**: Go and the platform interface make it easier, but its multimedia layer is different and would be
  outside this plan.

## 7. The owner's decisions (Oct 13, 22:05)

| # | Decision | Chosen |
|---|---|---|
| E1 | How to talk to GStreamer | Own minimal layer with cgo, not go-gst (§3) |
| E2 | Where the engine lives | Separate process `eirl-engine` (`manager/cmd/eirl-engine`), in the same Go module as the app |
| E3 | How the screen is drawn | Pure Go with DejaVu. The PNGs, side by side with Python's before installing (M3 gate) |
| E4 | The Python engine in the meantime | **Frozen now, from M0** (not from M1): fixes only. Anything new in the engine goes in Go |
| E5 | Single-source mode (`MIXER=0`) | Dropped: the Go engine does not have it |
| E6 | When | All of it, now. The owner: "The power supply is fixed now. Do all of it." |

## 8. M0: measurements (Oct 13, 22:07-22:51)

A board slot from 22:06, agreed with the owner. The engine was stopped from 22:27:51 to 22:51:02, not broadcasting. Before loading, the
input read 5.18 V and never dropped below 5.22 V during the tests.

### 8.1 How much Python spends in the real engine (py-spy)

`py-spy` 0.4.2 (standalone binary in `~/py-spy/` on the board, sha256 from PyPI), always with `--nonblocking`:
without it, py-spy pauses the process at every sample. Samples of who holds the GIL (`--gil`, 100 per second).
The `main` engine (58c494d), with HDMI at 1080p50, the capture card at 30, the Logi at 60, the screen on
and the preview stopped:

| | Python holding the GIL | Engine (CPU of one core, `engine-cpu.sh`) |
|---|---|---|
| Not broadcasting (10 min) | **24.3%** of the time | 78% |
| Broadcasting to eu1 (4 min) | **33.5%** | 108% |

Where it comes from (broadcasting, in % of one core):
- **Screen, 9.6%** (main thread): `tick` and `compose`, and the `Gst.Buffer.new_wrapped` copy, 3.6% on its own.
- **Source bridge, 9.9%**: `on_sample` → `Timing.add`, the judge, `stamp` and `push_buffer`.
- **Audio, 5%**: the `on_sample` callbacks and loops in `engine_audio`.
- **Output bridge, 2.2%**, and `on_program` (program → screen), 1.3%.
- Headphones, 1%; SRT statistics, 0.6%; level meters, 0.5%; cadence, 0.3%.

This is a ceiling on what can be saved: it includes calls into C that do not release the GIL, such as the
`new_wrapped` copy. 🪤 `py-spy record --threads` at 100 Hz cannot keep up with the engine's 99 threads: it falls behind, its
process uses a whole core and it ignores SIGINT. Measure with `--gil` or at 20 Hz, and stop it with SIGTERM. The
"system" CPU in those measurements (213% and 267%) is inflated by py-spy itself; the engine's is not.

### 8.2 Prototype: C bridge versus Python and proxysink (`mixer/m0/`)

The same graph in all three variants. `eirl-engine-m0 -describe` exports it and `proto_py.py` loads it:
- **Sources:** 4 test sources at 1080p60 in NVMM (`videotestsrc` 320×180 → `nvvidconv` and the engine's `sink_tail`),
  each in its own pipeline.
- **Program:** `nvcompositor` in PiP with two hidden layers, cadence for 2 s out of every 10, and the branch to the
  screen at 30 fps.
- **Screen:** the multiview pipeline with `fakesink sync=true`.
- **Output:** appsink → appsrc → NVENC 1080p60 at 6 Mbit/s → MPEG-TS.

The variants:
- **py:** the Python engine's per-frame code, copied: `on_sample`, `Timing`, `CaptureJudge`, `stamp`/`push_buffer`,
  `on_program`, `on_cadence`, and the output bridge and probe.
- **c:** the same, in a C callback (`proto/bridge.c`). Go only builds the pipelines and reads counters: no frame
  enters Go.
- **proxy:** `proxysink`/`proxysrc`, with a C probe that only changes the timestamp.
- **Screen load:** in py, Cairo draws 1024×600 twenty times per second (median 10.5-10.7 ms) and pushes it with
  `new_wrapped`. In c, a goroutine spends those same ms and pushes the layer.
- **How it is measured:** 15 s of warm-up and 120 s of measurement. Two alternating rounds (`run-m0.sh`; summary with `m0_sum.py`).

| Variant (mean of 2 rounds) | Process (% of one core) | System (4 cores, with IRQ) | Max arrival gap | Mean/max age | New program frames | NVENC | RSS | 5 V input |
|---|---|---|---|---|---|---|---|---|
| py, with screen | 118.2 | 152 | 28-29 ms | 2.4/14 ms | 60 | 60 | 82-88 MB | 4.97 W |
| **c, with screen** | **102.2** | **130** | 28-29 ms | 2.4/15 ms | 60 | 60 | 97-105 MB | 4.87 W |
| proxy, with screen | 113.1 | 143 | **154-190 ms** | **14-18/150-183 ms** | 60 | 60 | 87-108 MB | 4.88 W |
| py, without screen | 92.0 | 118 | 26-45 ms | 2.5/14-48 ms | 60 | 60 | 69 MB | 4.77 W |
| **c, without screen** | **75.3** | **100** | 33-37 ms | 3.0/18-24 ms | 60 | 60 | 68-75 MB | 4.67 W |

Reading:
- **The C bridge saves 16-17 points of one core** compared with Python's, with about 400 crossings per second (4×60
  sources, 4×30 to the screen, 30 from the program to the screen, 60 to the output, the encoder probe and cadence), and
  19-22 points of the board's CPU: about 0.4 ms of CPU per crossing in Python. The cost of the C bridge
  cannot be distinguished in the per-thread table.
- **proxysink/proxysrc is not usable**:
  - it uses 11 points more than the C bridge, because of the threads and queues it adds;
  - it has 10 times more delay and gaps of 150-190 ms;
  - at startup it gives `NvDdkVicExecute Failed` and `not negotiated` on some source;
  - and it does not allow the timestamp judge or the multiview limit without per-frame code.
  
  Ruled out: the bridge goes in C.
- **Gaps and delay, the same in py and c.** The Python prototype has 3 Python threads, not 12 like the engine:
  there is hardly any GIL contention. The real engine logs gaps of 44-62 ms per source and 81-95 ms from the program
  to the screen (journal, «tiempos» (timings), Oct 13 21:56-22:06), with ages of 22-42 ms minimum and 62-81 maximum. That
  is what the GIL adds and the C bridge removes, and it gets measured for real in M5, with the whole engine.
- **RAM:** without the screen, the same (68-75 MB: almost all of it is the GStreamer and NVIDIA libraries). With the screen, the
  Go prototype rises to ~100 MB because it allocates a new 2.4 MB layer on every draw. The Go engine will use a pool
  of 2-3 layers (M3). The ≤ 80 MB target in §5 is for the whole engine, with more pipelines: it is tight.
- Left unmeasured in M0: audio, real captures, RTSP and startup. The test source
  (`videotestsrc`) uses ~11 points per source in both variants.

**Estimate for the whole engine** (bridge from §8.2 and what was measured in §8.1):
- not broadcasting, from 78% to ~55-60%;
- broadcasting, from 108% to ~80-85%, plus whatever the Go screen costs (M3).

This is in line with the ≤ 75% target in §5, before tuning. The big win is still what does not show in the CPU:
that the screen and commands always respond, and that a crossing never waits for the GIL.

### 8.3 M0 tools (in `mixer/m0/`)

| File | Purpose |
|---|---|
| `build/engine.Dockerfile` | Build image with cgo: Ubuntu 18.04 + GStreamer 1.14.5 + GLib 2.56 (what the board has) + Go 1.26.4 (sha256) |
| `proto/` | Prototype `eirl-engine-m0` (Go + `bridge.c`, `-mode c\|proxy`, `-describe`, `-check`): a separate module so as not to bring cgo into `manager/` before M1 |
| `proto_py.py` | The same test with the Python engine's per-frame code (Python 3.6, PyGObject 3.26) |
| `run-m0.sh` | On the board: checks that nothing is broadcasting and stops `eirl-engine` in the same script, runs the variants and starts it again no matter what (`trap`) |
| `m0_sum.py` | Table from a results folder |

Build: `docker build --platform linux/arm64 -f build/engine.Dockerfile -t eirl-engine-build build` and
`docker run --rm --platform linux/arm64 -v "$PWD/mixer/m0/proto":/src -v "$PWD/mixer/m0/out":/out eirl-engine-build go build -o /out/eirl-engine-m0 .`

## 10. M1: skeleton and sources (Oct 13, in main)

**Gate passed on the board** (Oct 13, 23:27-23:47, in an agreed board slot). The Go engine replaced the Python one with a drop-in in
`/run` (`tools/engine-go-try.sh`), with the 3 sources, without the screen and without broadcasting:
- **Startup:**
  - scene recovered from Python;
  - «sin señal» (no signal) slate in 254 ms (Python: 340);
  - the 3 sources live (HDMI 1080p50, MS2109 at 30 and Logi at 59);
  - program at 60 fps, with new frames equal to the on-air source.
- **Real app and web:** they read the state; `take`, `layout`, `pause`, `hide` and `preview` via `POST /api/v1/mixer` work
  (the program thumbnail shows the PiP and the Pause); the engine reads the app's `preview off`.
- **Measurements:** engine at 35-42% of one core and the board at 76% with IRQ, versus 78% and 119% for Python, which
  also has the screen and audio. RAM 96 MB, 79 threads, 549 descriptors, 5.36 W.

**How it is built** (`manager/`, the app's module):

| Package | What | Tests |
|---|---|---|
| `internal/gst` | Own cgo layer (`gstc.c`: bridge, cadence, counters, `select-stream`, STREAMOFF) behind interfaces (`types.go`); `gst/fake` for testing without GStreamer | with real GStreamer 1.14.5 in the image (`go test` inside `eirl-engine-build`) |
| `internal/engine/config` | The launcher: `engine.env` allowlist, byte-by-byte validation, `CONF_SHA` (same as the app), `state` file, secrets behind a type that does not print | 27, checked against bash 4.4 |
| `internal/engine/scene` | Scene, geometry, fades, commands and `mixer-scene.json` (audio settings pass through untouched) | those from SceneTest/RectsTest/CommandTest + persistence |
| `internal/engine/server` | Socket and publishing of `mixer.json` (actor: commands are executed by the engine goroutine) | ServerTest + round trip with `mixer.Client` |
| `internal/engine/pipeline` | Descriptions of all the pipelines; NVIDIA elements in `Platform` (Tegra) | golden files: Python and the real program on the board, token by token |
| `internal/engine/{hdmi,probe,netcam,sysx,elog}` | DV timings via ioctl, EDID and reset; `probe`; MediaMTX; `event=` logging | DvTimingsTest, ParseTest, NetTest |
| `internal/engine` | The core: actor, sources (one goroutine each), slate, cadence, `video`, thumbnails, discovery | with `gst/fake`, `-race` |
| `cmd/eirl-engine` | `--version`, `--check-config`; exit 64 with an invalid configuration | — |

The app's `journal.go` recognizes `[x] event=<code>`, which maps to the usual classes (`signal_lost`, `mode_changed`,
`pipeline_exit`…), and keeps the Python phrases. Build: `build/engine.sh` (tests + binary in `build/out/`).

**Deliberate deviations from Python** (detailed in each piece's report; reviewed in M4):
- **Config:**
  - `MIXER=0` is accepted, but the mixer runs (`event=single_source_unsupported`);
  - an invalid `AENC` exits with 64, not 1;
  - `CONF_SHA` follows the app in its two odd cases.
- **Socket:** line >4096 bytes = disconnected; boolean `id` = `bad_request`; `NaN` = `bad_json`; the 256 KiB cap also
  counts responses; a slow client no longer blocks the others.
- **Scene:** a `mixer-scene.json` that is not an object gives the default scene (Python crashed).
- **Logs** in English and with a code; the state's `detail` stays the same (it is contract, and C5 translates it).

**Left for M2-M3:** hot output, audio, headphones, USB recovery, the TC358743 audio watchdog and ADMAIF
(M2); screen and touch (M3). In the meantime, the state says `output off` and `audio` does not appear.

## 11. M2 and M3: full implementation (Oct 14, branch `feat/engine-go-m2m3`)

The owner (Oct 13, 23:40): "finish the full implementation and then we'll test everything." Six pieces in
parallel (subagents) and the integration into the core:

| Package | What it ports | Python tests ported |
|---|---|---|
| `engine/audio` | `engine_audio.py`: inputs (I2S, USB, network) with their C bridge, mixing, gains, delays, «sigue al vídeo», level meters, `audioSettings`, ADMAIF | 14 + part of `test_usb_on_air` |
| `engine/output` | `engine_output.py` and the ABR from `engine-run.py`: sessions, FIFO, srt-live-transmit, srtla_send, uplinks and SIGHUP, retries, measurements | 14 + 9 (the VideoMeter was for `MIXER=0`) |
| `engine/phones` | `engine_phones.py`: headphones via the HDMI, with a C "tap" that measures the audio delay (HEADPHONES.md §1.3) | 18 |
| `engine/usbhub`, `engine/tcaudio` | `engine_usb.py` (port power cut) and the launcher's HDMI audio watchdog (PLL, stalled I2S, reset) | 10 + 2 from the mixer; watchdog with 16 cases |
| `engine/screen` (+ `ui`), `cmd/eirl-ui-render` | Screen: Layout, drawing in Go with DejaVu, touches, Settings, keyboard, networks, Power, `screen.jpg`, evdev touch | LayoutTest, ScreenTouchTest, ScreenDrawTest, ModeLabelTest… |
| `engine/appapi` | `engine_app.py` (NetState) and `wifi.json` | 7 + `test_lee_y_valida` |

What was added to `internal/gst` for them:
- `Feeder`: the interface layer, with a pool of 3 buffers, without allocating memory on every draw;
- `Tap`: the audio to the headphones, with measurement of its delay;
- `Bridge.SetDst`, `Counter.Free`;
- `Pipeline.SetLatency` and `Latency`.

**Screen PNGs (M3 gate), Go versus Python in 57 variants:** the differences are antialiasing only. Only
0.00-0.02% of the pixels change by more than 96 levels; there are no placement or text changes. On the Mac, the full
draw takes 1.2 ms and Settings 3.5 ms. Side by side in the session scratchpad (`screen-compare/side-*.png`; they
are regenerated with its `run.sh`).

**Engine switch (M5):** `scripts/eirl-engine-start` is the new `ExecStart`. With `MIXER_ENGINE=go` in
`/etc/default/eirl-engine`, which only root can write, it starts `eirl-engine-go`; otherwise, or if the binary is missing, Python.
`tools/deploy-engine.sh` installs both (`--unit` for the unit).

**Deliberate deviations in M2 and M3** (from each piece's report; reviewed in M4):
- **Output:**
  - stops the processes before closing the pipeline (Python's order hung with the FIFO full);
  - the FIFO stays open until PLAYING;
  - SIGPIPE ignored and descriptors with close-on-exec.
- **Screen:**
  - commands are asynchronous;
  - it does not push a frame if nothing has changed (Python pushes at least one per second);
  - the keyboard buffer is wiped with zeros;
  - the drawing approximates Cairo.
- **Headphones:** a notification that arrives just before waiting is no longer lost.
- **App:** «La app no responde» (the app is not responding) instead of the exception name; the client wipes the wifi password with zeros.

**Pending, with the owner's OK** (known debt):
- if the AUTO_CMD2/NCO reads fail, the PLL recovery writes "0" (copied from the bash script);
- the hotplug pulse for the PLL (§9).

**M3 gate passed (Oct 14, 00:38).** The owner tested the Go engine's screen on the board: "everything works perfectly, we can
call it good."

### 11.1 Testing everything (next step, on the board)

1. **Install:** `tools/deploy-engine.sh --unit` with `MIXER_ENGINE` unset, which keeps Python. Short board slot: the
   service has to be restarted for the new `ExecStart`.
2. **Agreed board slot, with the Go engine:**
   - the 3 sources;
   - the screen: what is shown, touches, Settings with the wifi and the keyboard, Power, lock, audio view;
   - audio: mixing, «sigue al vídeo», delays and headphones;
   - the web and the app;
   - broadcast to eu1 over SRT and over SRTLA, with ABR;
   - USB recovery, if the chance comes up.
   
   Measure with `engine-cpu.sh` and `engine-watch.py` (MIXER.md §17).
3. **Night (M5):** 3 sources broadcasting to eu1 with the Go engine and `engine-watch.py`. If it comes out clean, `MIXER_ENGINE=go`
   by default.

## 9. Parity: checklist (M4)

Filled in phase by phase. Each item is ticked when it has a Go test or has been checked on the board.


### 9.1 Status (Oct 14, M4)

**Tests:** 154 of 156 ported, with the same name or an equivalent. The two that do not apply in Go:
- `test_guarded_timer_survives`: there are no GLib timers;
- `VideoMeterTest`: it belongs to `MIXER=0` mode.

The timestamp judge and the capture timestamp are tested on their C functions (`eg_judge_step`, `eg_capture_time`).
The pipeline descriptions, with golden files from Python and from the real program on the board.

**The 26 pitfalls of MIXER.md §3, in Go:**

| Pitfalls | Where | How it is checked |
|---|---|---|
| 1, 3, 6, 20 (RGBA, max-latency, latency and queues, audioconvert) | `pipeline` and `screen` descriptions | golden files (Python and board) |
| 2, 22 (nothing outside the canvas, ≤16× on the VIC) | `scene.SceneRects`, `PauseOff` | scene tests |
| 4, 5, 11 (copy of 8 buffers, real screen mode, stable screen) | `screen` | golden file of `Screen.describe`, lifecycle tests |
| 7 (cadence 50/60) | `engine.Prepare` | board: 60 with HDMI at 50 and USB at 60 |
| 8, 10 (isolated MJPEG, a single retry) | one goroutine per source, with increasing backoff | design |
| 9 (source that stops without an error) | 3 s watchdog in `Source.stream` | code; on the board, in the night run |
| 16, 17 (scene by sources, slate from the engine) | `discovered`, `slateTick` | `TestDiscoveryAssignsSlots`, `TestSlateTick` |
| 19, 26 (5 s I2S close, the audio loop) | `audio` (close with deadline), `phones` | audio tests |
| 21, 23, 24 (timestamps across pipelines, capture timestamp, RAW clock) | C bridge | `TestBridgeMovesFramesAcrossPipelines`, `TestCaptureJudge`, `TestCaptureTimeEarly` |
| 25 (unread appsink) | the output bridges, always attached (with no destination, they drop in C) | output tests |
| 12-15, 18 | do not apply: measurements, VIC governor (not in the launcher), Python 3.6, udev | — |

**2d's pitfalls (§9):**
- covered by tests:
  - `nvv4l2camerasrc` (`TestCloseStuck`, `TestCloseUnstuckByStreamoff`, `Early`);
  - the headphone tap;
  - the screen (repaint by regions, frozen `screen.jpg`, the keyboard with dots);
  - `jpegparse` (`TestNeedsJpegparse`);
  - network camera lag (`TestNetworkLag`);
  - `uplinks-nointernet` and `no_uplinks` (output tests);
- the PLL debt stays as it was.

**Code review (/code-review high, Oct 14, of all of M1-M3):** 9 findings, 8 of them fixed:
1. **Reused slot:** the old source, on closing, detached the new source's bridge. Now the new one waits for the
   old one to finish, and a source only releases the bridge if it is still its own (`Bridge.DetachIf`).
2. **`journal.go`:** it discarded as noise the `event=` lines whose detail carried a GStreamer phrase. Now an
   event is never noise.
3. **`screen.jpg` and the password:** false positive. Every close of Settings keeps the snapshot frozen for `SecretGrace` (2 s).
4. **The stop:** it could exceed the unit's 10 s. Now it has a global deadline of 9.5 s; the screen and the output
   close at the same time as the sources, and the program goes last.
5. **Races in the cgo layer with `Close`:** pipelines and elements with `RWMutex`. `Close` waits for in-flight
   calls and none of them touches a freed element.
6. **The new Pause text** generated its image in the actor. Now it is done separately, and the latest text wins.
7. **Hiding a source counted as a failure** and lengthened the backoff. Not anymore.
8. **The `WaitGroup` without `Wait`:** it is now waited on, with a deadline, before closing the thumbnails.
9. **Three copies of the bounded close:** unified into `gst.CloseWithin`.

- **Tests:** 156 methods in `scripts/test_engine_*.py`: mixer 72, phones 18, audio 14, output 14, net 11, run 10,
  usb 10 and app 7. They are ported to Go in their piece's phase.
- **Pitfalls:** the 26 in MIXER.md §3, F5 (§16), the savings (§17), the screen (§18-§19), the wifi (WIFI.md §6 and §13)
  and the headphones (HEADPHONES.md).
- **Pitfalls that are not in MIXER.md §3** (2d passed them on, Oct 13):
  - `nvv4l2camerasrc`:
    - with caps of a different size it keeps waiting and `set_state(NULL)` does not return: `VIDIOC_STREAMOFF` on its
      descriptor after 2 s and, if even that fails, «error» and `v4l2src`;
    - it timestamps on arrival: 2 frames have to be subtracted;
    - with `fakesink` it stops delivering frames;
    - the "dmabuf_fd 0 mapped entry NOT found" on close has no effect.
  - CSI `v4l2src`: the pts comes out clamped to 0 for the first ~1.6 s, because `raw_offset` is larger than the running
    time. A «NO fiables» (not reliable) from the judge at startup is normal.
  - `nvv4l2h264enc ! h264parse ! nvv4l2decoder` in a single pipeline hangs and does not die with SIGINT. Test it
    separately and with `timeout -k`.
  - Program audio:
    - it reaches the `at` tee ~165 ms after its pts;
    - with a `buffer-time` of 100 ms, ALSA goes silent without warning. That is why it runs at 40 ms (HEADPHONES.md §1.3).
  - TC358743: the audio PLL sometimes does not lock (`AU_STATUS0=0x41`). Neither software recovery nor rewriting the
    EDID fixes it. Known debt, with the owner's OK for each test.
  - Screen:
    - the dirty/compose contract: nothing that changes may sit under a level meter;
    - `screen.jpg` freezes while Settings is open and for `SECRET_GRACE` afterwards;
    - the keyboard only shows dots;
    - `NetState` uses 3 s timeouts and polls quickly only while Settings is open.
  - What is sent from outside and has to be kept:
    - `preview` (FollowPreview, 15 s) with `livevalve`/`liveavalve`, and `phones`;
    - `uplinks-nointernet`, and `no_uplinks` when the last uplink goes away;
    - `cadvalve`;
    - `MV_FPS`/`MV_GAP`, with still images always passing;
    - MJPEG without `jpegparse`, with its fallback.
  - Network sources:
    - `rtspsrc` gives neither an error nor EOS when the sender goes away (3 s watchdog);
    - `drop-on-latency` and `MAX_LAG`;
    - H.265 over RTSP, passed through MPEG-TS.
  - `journal.go` recognizes Spanish phrases and the `[engine]`/`[hdmi2srt]` prefixes. The cloud depends on those classes
    (`start/stop/failed/restart` and the lines `[mixer] … (hueco N): …` with «sin señal HDMI» (no HDMI signal), «señal HDMI perdida» (HDMI signal lost)…).
    The old rules stay for old lines, and the new ones (`event=`) go in the same MR as the engine
    that emits them.
  - Measurements: `engine-cpu.sh` only counts IRQs since 17:00 on Oct 13. Reference for that night: engine 78% and
    system 119% (HDMI 1080p50, capture card at 30, Logi at 59, preview stopped, headphones on).
  - Power: before a test under load, check `in_voltage0_input` on the ina3221 (6-0040). On Oct 13 there were
    4 cuts with 4.70-5.18 V.
- **From the contract inventory** (session f1, Oct 13), things in the Python engine that are not in MIXER.md §5:
  - four audio commands also respond `unsupported` without audio;
  - `bad_json` and `bad_request` do not return the `id`;
  - `"id": true` is accepted;
  - a line of slightly more than 4096 bytes is processed if it completes on the next read;
  - the 0644 of `mixer.json` comes from the umask;
  - a `mixer-scene.json` that is a JSON list crashes `load_audio` at startup;
  - `thumb.jpg` is not written atomically.
  
  When porting, each case is decided — copy or fix — and noted here.

## 12. Slots 1-4 with an alias (Oct 14, contract agreed with 2d)

The user chooses what each slot takes, and gives it an alias. The app, the web and the screen are 2d's; the engine part is
`manager/internal/engine/slots.go`.

- **File** `<dir of EIRL_CONF>/slots.json` (normally `/etc/eirl-encoder/slots.json`). Only the app writes it, atomically;
  the engine reads it as data, at start and on the `slots` command. It does not watch it.
  `{"v":1,"slots":[{"slot":1,"assign":"auto","alias":"Main camera"},{"slot":3,"assign":"net:cam1","alias":"Phone"}]}`
- **`assign`:**
  - `auto`, `off`, or a source id exactly as in `sources[].id`: `hdmi`, `test`, `/dev/v4l/by-(id|path)/…` or
    `net:cam1`…`net:cam8`;
  - one id is pinned to one slot at most.
- **`alias`:** 0-24 characters, Unicode letters and digits, space and `-_.'&()#+/`; `""` = the device's name. The engine
  does not draw it: it is data for the app, the web and the screen.
- **Validation:**
  - slots beyond `MIXER_SLOTS` are ignored, and missing ones are `auto`;
  - a repeated or out-of-range slot, `v` other than 1, a bad `assign` or a bad `alias` make the whole list invalid;
  - an invalid file means all `auto` plus `event=slots_config_invalid`; an invalid command gets `bad_request`.
- **Placement:**
  - a pinned source goes to its slot as soon as it appears, and while it is missing the slot stays reserved and black;
  - `off` never takes a source;
  - `auto` takes the rest in today's order (the HDMI, first in the catalogue, takes the first `auto` slot), and a USB
    camera gone for over 10 s still gives up its `auto` slot.
- **`{"cmd":"slots","slots":[…]}` → `{"ok":true}`, applied live.**
  - A source that changes slot is stopped and starts again in its new slot. It waits until the old one has ended
    (`Source.prev`: a device is opened only once, and a bridge has one owner).
  - While emitting, it answers `on_air` if it would move or switch off the main source, or the secondary in PiP or side
    by side (~2 s of slate on the program). An alias change alone moves nothing.
- **State (additive):**
  - `sources[].alias`;
  - `slotConfig: [{slot, assign, alias, present}]` (a reserved slot without its source appears only here);
  - `catalog: [{id, name, kind}]`, the last good `probe -list` including the sources without a slot.
- **Tests:** `slots_test.go` (validation, file at start, invalid file, reserved slot, live command, `on_air`).

## 13. Detail codes (Oct 14, agreed with 2d)

The web and the screen are translated (es/en/fr), but the engine's `detail` is Spanish text: the reason a source or an
audio input is not live. Each one now also carries a stable code, so the interfaces can translate it. The change is
additive: `detail` keeps its Spanish text byte for byte for whoever already reads it.

- **State:**
  - `sources[].detailCode` and `audio.inputs[].detailCode`: always present, `""` when there is nothing to say;
  - `audio.phones.detailCode`: absent when there is nothing to say, like its `detail`;
  - `detailArgs`: an object of strings, absent when the code has no arguments;
  - `output` already had its stable code, `output.reason` (`no_destination`, `no_uplinks`, `relay_unreachable`,
    `rejected`, `connection_lost`, `encoder_error`, `internal`). Its `detail` stays technical text, much of it lines
    from the tools after masking the streamid, so it gets no arguments.
- **The closed list** (`manager/internal/engine/detail`, 40 codes): adding a code is a contract change, and
  `detail_test.go` fails until the list here and the test are updated.

| Code | Arguments | Spanish `detail` today |
|---|---|---|
| `hdmi_no_signal` | | sin señal HDMI |
| `hdmi_signal_lost` | | señal HDMI perdida |
| `hdmi_locking` | | la señal HDMI se está enganchando |
| `hdmi_mode_changed` | `from` (`1080p60`), `to` (`1280x720@60`) | la señal HDMI ha cambiado: {from} -> {to} |
| `hdmi_interlaced` | `mode` | {mode} entrelazado: el VI del Nano no lo captura |
| `hdmi_too_fast` | `mode` | {mode} no cabe en 2 lanes CSI: máx. 1080p50 o 720p60 |
| `hdmi_set_mode_failed` | `error` | no se puede fijar el modo HDMI: {error} |
| `hdmi_hung` | | la captura HDMI se ha colgado: vuelve al reiniciar el codificador |
| `hdmi_no_audio` | | sin audio HDMI |
| `gone` | | desconectada |
| `no_bus` | | no cabe en el bus USB con las demás |
| `no_format` | `warnings` (optional) | no ofrece un formato que se pueda capturar[ ({warnings})] |
| `unsupported` | `error` (what the probe said) | {error} |
| `probe_failed` | `rc`, `error` | eirl-encoderd probe ha fallado ({rc}): {error} |
| `hung_given_up` | | colgada: desenchúfala y vuelve a enchufarla |
| `hung_on_air_unknown` | | colgada: emitiendo no la reinicio (no sé qué hay al aire) |
| `hung_on_air` | `video`, `audio`: the ids on air (`sources[].id`, `audio.inputs[].id`), comma separated, each absent when empty | colgada: reiniciarla apagaría {names as shown} (al aire) |
| `port_reset` | | reiniciando su puerto USB |
| `net_receiver_down` | | el receptor de cámaras no responde |
| `net_waiting` | | Esperando a la cámara |
| `net_no_video` | `tracks` (comma separated) | la cámara no manda H.264 ni H.265 ({tracks}) |
| `net_no_audio` | | la cámara no manda audio |
| `bt_no_audio` | | el dispositivo Bluetooth no manda audio (BLUETOOTH.md) |
| `net_disconnected` | | la cámara se ha desconectado |
| `net_behind` | `seconds` (`2.4`) | va {seconds} s atrasada |
| `phones_waiting_screen` | | Esperando a la pantalla |
| `phones_screen_no_audio` | | La pantalla no admite audio |
| `phones_no_device` | | La placa no tiene salida de audio HDMI |
| `phones_not_opening` | | la salida HDMI no se abre |
| `no_image` | | no da imagen |
| `frames_stopped` | `seconds` | {seconds} s sin imagen |
| `no_samples` | `seconds` | sin muestras {seconds} s |
| `stream_ended` | | fin de la fuente |
| `audio_ended` | | fin del audio |
| `stream_error` | `error` (GStreamer's text, English) | {error} |
| `start_failed` | | no arranca |
| `build_failed` | `error` | no se puede construir: {error} |
| `no_sink` | | no se puede construir: sin appsink |
| `prepare_failed` | | fallo interno al prepararla |
| `internal_error` | `error` | fallo interno: {error} |

- **App:** `internal/mixer` (`Source`, `AudioInput`, `Phones`) carries `detailCode` and `detailArgs` (omitempty: absent
  with the Python engine), so the web gets them through the app's state as they come from the engine.
- **A GStreamer error without text** stays the zero detail (no code, no failure), as the text was before the codes.
- **Log:** the `source_*` and `audio_input_*` events carry `code=` next to `detail=`.
- **Cloud events:** `sourceEvent` now classifies by code (`signal_lost`, `mode_changed`, `signal_unsupported`), with
  the same result as before.
- **What is still Spanish and without a code:** the names the engine makes up (`hdmi` and `test` have fixed ids; a
  network camera without a name is «Cámara wifi»).


## 14. M5: the night (Oct 14, 00:34-08:00)

The Go engine broadcast to eu1 (a test key forwarded to no channel) over SRTLA with ABR for 7 h 25 min, while
`soak-night.py` changed the scene every 20 s (layout, take, PiP corner, swap, transition, audio mix and single)
and a root guard (`eirl-m5-night`) stood ready to go back to Python on any failure. It never had to: at 08:00 it
switched the output off and put the Python engine back, as planned. Sources: the board's HDMI (1080p50), the
MacroSilicon USB capture card (30 fps) and the Logitech 4K Stream Edition (60 fps); a network camera (a phone)
left slot 4 at 00:35. Data on the board: `~/m5/20261014-0034/` (samples every 30 s, the 1380 commands) and the
guard's `~/m5/night-1014-0034/guard.log`.

| | Result |
|---|---|
| Engine restarts, panics | 0, 0 (one PID all night) |
| Scene commands | 1380, all answered `ok` |
| Program | 59.9 fps on average (59.2-60.6) |
| Sources 1-3 | live the whole night |
| Engine CPU | 85% of one core on average (64-113%); `srt-live-transmit` 4% |
| Engine RSS | 139 MB at start, 175 MB a minute later, 254 MB at the end: see below |
| Threads, descriptors | 123 and 835, flat |
| Broadcast | 5.4 Mbps sent on average (6 Mbps target), RTT 59 ms; 5 cuts, all of them the home internet |
| Board | 5.14-5.21 V, 4.7-7.0 W, 50-51 °C, no power cut |

**The cuts** (01:37-01:40, 02:45-02:46, 03:18-03:23, 03:52-03:59, 04:10-04:11; about 16 min in all) were the home
line: srtla lost its only uplink, and then the board could not even resolve `eu1.relay.enhancedirl.com`. The
router's gateway monitor raised loss alarms (21-22%) at 01:37:51, 03:18:49 and 03:52:55 and cleared them at
01:40:32, 03:23:54 and 03:57:11. ABR stepped down before each cut (6000 → 2550 kbps). The engine kept the output in
`connecting` with `reason=relay_unreachable`, retried every 2-10 s, and was back on air 1-4 s after the line came
back (at 03:59, two minutes after the alarm cleared, because name resolution took that long to return).

**Memory grows with the reconnections.** Each retry rebuilds the whole output session (`srtla_send`,
`srt-live-transmit` and the encoder pipeline: 78 encoder opens over the night) and about 0.9 MB stays behind: the
RSS went from 178 to 245 MB between 01:37 and 04:11, in steps at the cuts, and only +9 MB in the last 3.5 h without
cuts. The Python engine's night of Oct 13 (no cuts) went from 128 to 139 MB in 5.6 h. Threads and descriptors do
not grow, so the sessions do close; what stays is memory, in the engine or in the NVIDIA encoder library. It is
the first task of M6: open and close the output pipeline in a loop on the board, with Go and with Python, and fix
it. An IRL broadcast over mobile networks reconnects far more often than a home line.

**`program_cadence_low`** (118 warnings): all but two with the USB capture card on air, which shows a still image
(byte-identical frames). The Python engine gives the same warning with that card (Oct 13): not a fault.

**Verdict:** the night meets the M5 bar (0 restarts, 0 panics, every command answered). The reconnection growth
turned out not to be a leak (§15.1). `MIXER_ENGINE=go` became the default on Oct 14, and the owner retired the Python
engine the same day instead of keeping it two weeks as the fallback (§16).

## 15. M6: tuning (Oct 14)

### 15.1 Memory after reconnections: glibc's arenas, not a leak

Measured on the board without the engine (test binary `go test -c -tags board ./internal/engine/output/`,
`leak_board_test.go`), opening and closing N times, not broadcasting:

| Loop (RSS of the test process) | Start | After 10 | After 60 |
|---|---|---|---|
| Encoder pipeline alone, Go | 22 MB | 71 MB | 85 MB (flat from round 20) |
| Encoder pipeline alone, Python (PyGObject) | 17 MB | 49 MB | 50 MB |
| Encoder alone, Go, `MALLOC_ARENA_MAX=2` | 22 MB | 66 MB | 66 MB |
| Output sessions (the engine's `output` package, fake srt-live-transmit) | 34 MB | 133 MB | 164 MB (flat from round 30) |
| Output sessions, `MALLOC_ARENA_MAX=2` | 32 MB | 65 MB | 87 MB |
| Output sessions, `malloc_trim` every 10 (40 rounds) | 37 MB | 54 MB | 69 MB |
| Output sessions, both (40 rounds) | 35 MB | 43 MB | 46 MB |

The Go heap stays at 1.2-1.8 MB in every loop: the growth is C memory, and it levels off. glibc gives each
thread that allocates its own arena (up to 8 per core), GStreamer's streaming threads change with every
pipeline, and what a closed pipeline freed stays in its arena. Python, with one thread calling GStreamer, levels
off lower. The fix (`fix(engine): give back what glibc's arenas keep…`):

- `eirl-engine-start` runs the Go engine with `MALLOC_ARENA_MAX=2`.
- `malloc_trim(0)` after every output session, and every 10 min with the new `memory` event, off the actor
  (`event=memory rss_mb trimmed_mb trim_ms go_heap_mb go_sys_mb goroutines threads`; `trim_ms` 0-2 on the board).

The real engine, not broadcasting, 3 sources, screen on, preview off, 6 min each after a restart:

| | CPU (one core) | RSS |
|---|---|---|
| `MALLOC_ARENA_MAX=2` | 63.1% | 110 MB |
| `MALLOC_ARENA_MAX=32` (glibc's default on the Nano) | 62.9% | 118 MB |
| `MALLOC_ARENA_MAX=2` and `GOGC=50` | 62.3% | 105 MB |

Two arenas cost no CPU, nor does `GOGC=50` (`go_sys` 34 → 30 MB); `eirl-engine-start` sets both (each can be
overridden in `/etc/default/eirl-engine`). Of the ~110 MB: 34 MB are mapped files (the binary, libnvidia-eglcore, GStreamer…), the
Go heap is 15 MB (`go_sys` 34 MB), and NVIDIA's own allocations account for several 4 MB blocks.

### 15.2 The HDMI hotplug pulse (stuck audio PLL)

New last step of the audio watcher's software recovery, before the hardware reset (which restarts the engine):
the EDID is written again, so the driver drops the hotplug (`HPD_CTL` 0x8544: 0x01 → 0x00 → 0x01) and the source
should renegotiate HDMI, audio included. It is only done when not broadcasting and with the HDMI off the program;
otherwise it is skipped with `audio_soft_recovery_skipped step=hotplug reason=on_air`. It waits up to 10 s for
the lock.

On the board with today's HDMI source, by hand (`v4l2-ctl --set-edid`, and also `--clear-edid` with 1 s
before writing it back): the hotplug drops and rises, but the source keeps sending (`SYS_STATUS` 0x8520 stays
0x9f, no signal lost, the engine sees nothing). So with this source the pulse does not renegotiate: it is
harmless (10 s more before the hardware reset) and may help with sources that do follow the hotplug. What would
fix the PLL for any source without cutting the broadcast is a hardware reset of the TC358743 with only the HDMI
source and its I2S input closed, instead of restarting the whole engine; it is not done (needs its own test
slot).

### 15.3 Other fixes in the same branch

- `tcaudio`: a failed i2c read of AUTO_CMD2 or the NCO no longer writes "0" back (the step is skipped).
- `TestCommands` waits for the Pause still, made off the actor since M4 (it failed now and then under load).

### 15.4 Start and touch (Oct 14)

- **`start_timings`** (one event per start, ms since `Start`: `program_ms`, `stills_ms`, `thumbs_ms`, `tcaudio_ms`,
  `screen_ms`, `phones_ms`, `discover_ms`, `total_ms`). The thumbnails pipeline (four VIC converters and JPEG
  encoders) took ~3 s to build and held up the whole start. It now builds in its own goroutine: `engine_started`
  went from 3.1-3.7 s to 0.6-0.7 s after `starting`. The web's thumbnails arrive a moment later.
- **The screen** (`screen_on` now carries `display_wait_ms`, `need`, `no_display`, `show_ms`). After an engine
  restart the panel unplugs for a few seconds when the previous engine's screen closes (MIXER.md §3, trap 11).
  The first reading usually finds no display, and then the 3 equal readings add up to ~4 s. Skipping the wait
  after a clean restart (a marker in `STATE_DIR`) was tried and discarded: when the first reading still saw the
  display, painting at once failed (`screen_error`, "Internal data stream error") because the panel unplugged a
  moment later. At boot, the splash path already takes one reading. So multiview takes ~4-5 s on a restart, held
  by the panel, and should be ~1 s after the engine at boot (to be measured at the next reboot).
  Oct 15: the panel unplugged because the closing screen switched the HDMI output off (MIXER.md §3, trap 27).
  With `displayhold` it stays lit and black between engines, so the wait can be revisited once measured on
  the board.
- **Touch** (`touch_max_ms` in the `timings` line): from the kernel's touch event to the UI frame pushed into the
  screen's pipeline. Six taps injected into the touch device: 35 ms at most. The screen's compositor adds about
  one frame, so the total stays under the 100 ms target.
- **Boot** (reboot on Oct 14, 12:17, monotonic times): the splash at 3.1 s, released at 6.8 s (the logo stays);
  the unit starts at 7.0 s, `starting` 7.3 s, `mixer_config` 9.1 s (configuration, `Prepare` and GStreamer's
  init, cold), `program_ms=3327` (loading the NVIDIA plugins cold; 180 ms on a restart), `screen_on` 13.1 s
  (`need=1`, no display wait: the splash path). So from the unit's start to the multiview: 6.1 s, of which
  ~5 s are cold loading before the first pipeline plays; the logo covers it. Below 2 s would take loading
  GStreamer and the plugins before the unit starts, which is not planned. Every source was live at 17.6 s.

### 15.5 Left open

- **The in-place HDMI reset** (MR !72, draft, §15.2): it needs the open-drain transistor on the TC358743's RST
  and `reset-gpios` enabled in the DT, then its board test with the HDMI source connected. The owner will take
  it up in another session.
- A night broadcasting with real reconnections, to confirm the memory plateau of §15.1 in the engine itself: not
  done, by the owner's decision (Oct 14).
- The targets not met, accepted: RAM ~105 MB (≤ 80), multiview 6.1 s at boot and ~4-5 s on a restart (≤ 2 s).

## 16. Closing (Oct 14)

The owner retired the Python engine on Oct 14 ("we are not going to maintain it") and closed C1:

- **Removed:** `scripts/eirl-engine.sh`, `engine-run.py`, `engine_*.py` and their 156 tests, the `MIXER_ENGINE`
  selector (`eirl-engine-start` runs `eirl-engine-go` only, with `MALLOC_ARENA_MAX=2` and `GOGC=50`), the tools
  that only served it (`tools/ui_render.py`, `tools/migrate-eirl-engine.sh`, `tools/engine-go-try.sh`,
  `scripts/measure-source.sh`, `mixer/f5/night.sh`) and `scripts/eirl-logo.png` (the screen embeds its own copy).
- **The board:** `tools/deploy-engine.sh` installs the Go engine only and deletes what the Python engine left in
  `/usr/lib/eirl-encoder/engine` (and their `.bak` copies). `tools/engine-cpu.sh` and `tools/engine-watch.py` look
  for the Go process.
- **The app:** `internal/service/journal.go` no longer classifies the Python engine's Spanish phrases (its old
  journal lines are "other", still redacted); the engine's lines are `event=` codes.
- **The docs** describe the Go engine; what they tell about the Python engine is history.
- **Kept:** `scripts/eirl-enginectl` (a Python client of the control socket, for operators), the `mixer/` research
  scripts and `mixer/m0/` (the M0 measurements), and comments in the Go code that cite the Python engine as the
  reference a behaviour was ported from.
- **After C1:** C2 (package and install) and C3 (OTA) are being done by the screen/web session with its signed
  release packages; C4 (base image) is BASE.md's B6.
