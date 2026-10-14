> **Status:** DONE — in main and installed on the board (Oct 13; A3 with the operator: "Now it works perfectly", clap in sync); code-reviewed
> **Updated:** 2026-10-14
>
> Since Oct 14 the engine is the Go one: the monitor lives in `manager/internal/engine/phones`, with the same
> behaviour. The file names in §3-§5 (`eirl-engine.sh`, `engine_phones.py`, `engine_mixer.py`, `tools/ui_render.py`)
> are those of the former Python engine, kept as history.

# Headphones on the screen: program audio over HDMI

The request (Oct 13): send the **program audio** over the board's HDMI to the touch screen, to listen to it
with headphones plugged into the **jack the screen already has**.

In short: the board already has the HDMI audio output and the screen accepts 48 kHz stereo audio, the same
format as the program. **Tested with a pair of test headphones (A0)**: tones and the real program sound clean
through the jack, in step with the picture. What is missing is the code: one more branch of the program audio
into its own isolated pipeline, with its switch and its volume in the screen's audio view and in the web.

## 0. Status by phase

| Phase | What | Status |
|---|---|---|
| A0 | Does it play? A tone over HDMI with test headphones, without touching the engine | **Done** Oct 13 19:50-19:53: passes (§1.1) |
| A1 | Engine: branch, monitor pipeline, `phones` command, state and scene | **Done** on the branch (Oct 13): tests, and a smoke test on the board with real GStreamer (§5.1) |
| A2 | Screen (audio view) and web | **Done** on the branch (Oct 13): web and app; screen with a PNG for review (§5.1) |
| A3 | Test on the board, measurement and review | **Done** Oct 13 20:20-20:41 with the operator (§5.2) |

## 1. What was checked on the board (Oct 13, 19:35-19:40, read-only)

- **There is an HDMI audio output**: card `tegrahda`, device 3 (`hw:tegrahda,3`, "HDMI 0"), codec "Nvidia
  Tegra210 HDMI/DP". Nobody uses it today. `IEC958 Playback Switch` = on. The card number is 0 right now, but
  it changes with the USB cards: it has to be opened **by name**.
- **The screen announces audio.** Its EDID ("LEN L1950wD", 1024×600) carries a CEA extension with *basic audio*,
  an LPCM audio block of 2 channels at 32/44.1/48 kHz and 16/20/24 bits, and the HDMI VSDB (it is not DVI). The ELD
  the codec sees says the same: `sad0 LPCM, 2 channels, 32000 44100 48000, 16 20 24`.
- **The program is already in that format**: `S16LE`, 48 kHz, stereo (`tee at` in the program pipeline, after
  `level pgmlevel`). No resampling needed.
- 🪤 **Lazy ELD.** Right after a reconnection of the screen (the engine switches it off and on when it
  restarts), `/proc/asound/card0/eld#3.0` said `monitor_present 1` and **`eld_valid 0`**. After reading the
  card's controls with `amixer`, it changed to `eld_valid 1` with the data above. Most likely: the HDA codec
  was suspended when the connection notice arrived and did not re-read the ELD until something woke it up.
  Consequence: **do not trust the ELD while the device is closed**; open it first (that wakes it up) and look
  afterwards.
- `/etc/asound.conf` (from NVIDIA) points the ALSA `default` to `hw:tegrahda,3`, and PulseAudio is still installed.
  The engine already runs with `PULSE_SERVER=unix:/nonexistent`; the monitor will open the explicit `hw:` device,
  never `default`.
- What **cannot** be known by reading: whether the screen's jack outputs the HDMI audio, at what level, whether the
  screen has its own volume or speaker, and whether the audio matches its pixel clock (1024×600 at 49 MHz, which
  is not a standard TV mode). That is A0.

### 1.1 Result of A0 (Oct 13, 19:50-19:53, with the operator and test headphones; the engine untouched)

- **Tones** (440 Hz left, 660 Hz right, 550 Hz both, −26 dBFS, `audiotestsrc ! audiopanorama !
  alsasink device=hw:tegrahda,3`): **all three are heard, each on its side**. Good level. The screen's picture
  **does not flicker** when starting or stopping. ALSA opened it at `S16_LE`, 2 channels, 48000 Hz (period 480,
  buffer 9600 = 200 ms, the `alsasink` defaults). `dmesg` with nothing new: the 49 MHz pixel clock does not
  bother the audio.
- **Lazy ELD, confirmed**: after reinstalling the engine at 19:42, `eld_valid 0` again; on opening the
  device for the first tone, `eld_valid 1`.
- **Real program** (no code: the copy the engine already sends to `127.0.0.1:5005` for the web's level meter,
  `udpsrc ! volume ! alsasink sync=false`), audio from the board's HDMI: **clean** for 30 s, **in step with the
  picture** on the screen. Of the two levels, 0 dB and −12 dB, the operator preferred **−12 dB** to start with.
- **The screen has no speaker and no volume of its own**: only the jack. The only volume is ours (§3.4), and there
  is no risk of feedback with the monitor switched on at startup.

### 1.2 The screen reconnects with the audio open (Oct 13, 19:59-20:01, with the operator; an eirl-engine restart without streaming)

Continuous tone through an `alsasink` outside the engine, and `systemctl restart eirl-engine` (the screen goes off and comes back):

- **`alsasink` does not fail**: the PCM stays in `RUNNING` the whole time, and `gst-launch` ends because of its
  `timeout`, not because of an error. The operator heard that **the tone cuts out while the screen is black and
  comes back on its own** when the screen comes back (about 10 s in total).
- The ELD goes to `eld_valid 0` on disconnection and **goes back to 1 on its own** after ~7 s, with the PCM open:
  with the codec awake, the connection notice does arrive (it confirms the cause of the trap in §1).
- Opening again with the screen already back (3 s beep): fine.

Consequence for the design (§3.3): **a reconnection of the screen does not require closing or reopening**. It is
opened once, with the screen stable, and it is only closed and retried on a real `alsasink` error.

### 1.3 🪤 The ALSA buffer reads ahead (A3, Oct 13)

Installed in the real engine, the monitor **played nothing at all**: no errors or warnings, PCM in `RUNNING`, and
the `audiobasesink` log said "wrote 1920 of 1920". Two causes, one behind the other:

- **The program audio reaches the monitor late: ~165 ms behind its timestamp** (p90; up to 219 at some point),
  against ~40 ms with a fake program. That is what the program's `audiomixer` waits (`latency=100 ms` plus its
  40 ms chunk). The screen shows the picture at 193 ms from the timestamp: with the monitor at that latency,
  everything arrived late. Fix: measure the arrival 1 s before opening (and keep following it afterwards, upwards
  only).
- **Even arriving in time, what is written close to its time does not play.** With a `buffer-time` of 100 ms, ALSA
  has already read up to 100 ms ahead of the point being played: whatever `audiobasesink` writes less than that
  before its time lands in a chunk already read and is lost **without any warning** (in the log it shows when
  comparing "rendering at N" with "processed samples: raw M": N < M). Fix: a `buffer-time` of 40 ms and
  latency = arrival + ALSA buffer + 30 ms of headroom (`target_delay`).

Result: it plays at ~234 ms from the timestamp, about 40 ms behind the screen's picture (193). The operator: "Now
it works perfectly".

## 2. Decisions (Oct 13)

| # | Question | Decision |
|---|---|---|
| D1 | On at startup? | **On**, at **−12 dB** the first time (chosen during A0). After that, the last choice is remembered. The screen has no speaker (A0): no risk of feedback |
| D2 | What is heard? | **Only the program**, what goes on air (with mix, gains and Pause with mute). Listening to a single input (PFL) is out of scope (§6) |
| D3 | Where is it controlled from? | **Screen and web.** Not the cloud: it is local monitoring and does not touch the ENCODER_CLOUD contract |
| D4 | Volume | **−40…0 dB, in steps of 2 on the screen; −40 is silence** (−∞, not −40 dB), besides "off". Nothing above 0: the program already reaches close to 0 dBFS at its peaks and would clip |
| D5 | Delay | **In step with the screen's picture** (§3.5). It is for checking what goes out, not for hearing your own voice while you talk (it arrives ~0.15 s late or more, because of the mixer's sync) |

## 3. Design

### 3.1 Where it comes from

One more branch of `tee at`, just like `outa` (the output) and `liveavalve` (the WebRTC preview). Like them, it
**can never hold back the program** (which goes to the encoder): a short `leaky=downstream` queue, `appsink
sync=false max-buffers=… drop=true`, and with the monitor off the valve closed (`drop=true`): not a single push to Python.

```
amix ─ audioconvert ─ level pgmlevel ─ tee at ─┬─ … outa (output), liveavalve (preview)
                                               └─ queue leaky ─ valve phvalve ─ appsink phtap
                                                                                     │
       [monitor pipeline]  appsrc pha ─ queue ─ volume phvol ─ alsasink hw:tegrahda,3 ◀┘
```

With the monitor off, `phvalve` closes and Python receives nothing. What is heard is exactly the program,
before the AAC.

### 3.2 Its own isolated pipeline (`scripts/engine_phones.py`, new)

Like `engine_output.py`: another pipeline with **the same clock and `base_time` as the program**, and
`alsasink provide-clock=false sync=true`. That way the ALSA ring adjusts by itself to the drift between the HDA
clock and the program's, without the buffer growing or emptying.

It is separate because an `alsasink` without a screen, without audio in the EDID or with the screen unplugged
**fails on open**. In the program pipeline, that would bring down the program and the stream. Separately, only
the monitor falls.

No new keys in the env: the state lives in the scene (§3.6). That way the two whitelists do not have to be
touched (the engine's, `internal/engine/config`, and `manager/internal/config/config.go`). The device is looked up in `/proc/asound/pcm`
("HDMI 0") and `/proc/asound/cards`, and is kept in the platform profile, for when there is another board (RK3588).

### 3.3 Lifecycle

1. On and with `screen.state = on` (Screen already knows it from `SCREEN_STABLE`) → open the pipeline. Opening
   wakes the codec (§1): **always open and then look at the ELD, never the other way round**.
2. Once open, read the ELD: if the screen does not announce LPCM, state `waiting` with "the screen does not support
   audio", close and try again after 30 s.
3. If the screen disconnects and comes back, **nothing is done** (§1.2: `alsasink` keeps running, goes quiet and
   plays again on its own). The state reports it (`waiting` with "Waiting for the screen" while the ELD is not
   valid), checking the ELD every few seconds, which with the PCM open is up to date.
4. `alsasink` error → close and retry after 2/4/8…30 s. **A single pending retry** (lesson from F0: several at
   once exhausted the VIC).
5. Bounded close: `set_state(NULL)` in a thread with a 3 s cap, like `Source.close` and the shutdown of
   `engine-run.py` (trap 19). It never blocks the GLib loop. **Also when the engine stops**: it must not go beyond
   the 10 s the unit allows.
6. When the engine restarts, the monitor stops with it and opens again once the screen is stable.

### 3.4 Volume and switching on without clicks

`volume phvol` with a 100 ms ramp in the monitor's thread (10 ms steps), like the 250 ms fade of the inputs. −40 dB = `volume 0`
(true silence, D4); the rest, `10^(dB/20)`. Switching off = ramp to 0, then close the valve and the device
(zero cost): A0 showed that opening and closing the audio does not make the screen flicker.

### 3.5 Delay

With the program's clock and `base_time` (`share_time`) and `sync=true`, it plays with the program's timestamps
(capture + `MIXER_SYNC`), the same way the screen shows them.

The program already runs `MIXER_SYNC` (120 ms) behind the capture, for the picture and for the audio. The screen
shows each program picture at its timestamp **plus** the latency of the compositors and `MV_MARGIN` (`on_program`),
**plus** the latency of its own pipeline. The monitor forces that same sum as its pipeline's latency
(`Engine.screen_delay_ns()`: `latency_ns` + `MV_MARGIN` + the latency query to the screen's pipeline), so that
**what is heard is in step with what is seen on the screen**, **unless the audio arrives later** (in the engine it
arrives at ~165 ms and the screen runs at 193: with the ALSA buffer that is not enough, §1.3). Then the arrival
rules: p90 measured 1 s before opening + 40 ms of ALSA buffer + 30 ms of headroom (`target_delay`), and if, while
running, it arrives later, it goes up (never down: every change is a cut). `TRIM_MS` (0) is the fine adjustment
after the A3 clap test (in front of the camera, looking at the screen and listening). The program audio reaches
the monitor about 40 ms after its timestamp (the `audiomixer` output; measured in the smoke test): the latency has
to exceed that or whatever arrives late does not play (the engine warns in the journal, at most once a minute). If
the screen is rebuilt with another latency (mode change, another EDID), the monitor follows it while running: every
3 s it recomputes the delay, with the screen on, and if it changes by more than 5 ms it calls `set_latency` again
(code review). There is no way to go below what the program already carries without listening
before the sync, and that is a different feature (§6).

### 3.6 Contract (MIXER.md §5), additive

| Command | Fields | Errors |
|---|---|---|
| `phones` | `on`: bool and/or `gain`: integer dB, −40…0 (−40 = silence). At least one | `bad_request`, `unsupported` (engine without the branch, e.g. `MIXER=0`) |

State: `audio.phones` = `{"on": true, "gainDb": -12, "state": "on"}` (by default, D1; `detail` only when there is something to say). States: `off` (off) ·
`waiting` (no screen, or the screen does not support audio: `detail` says which) · `on` · `error` (retrying;
`detail` = short reason, no paths). It is saved in `mixer-scene.json` → `audioSettings.phones {on, gainDb}`.
An old engine answers `unknown_command`, and an old app ignores the field.

`eirl-enginectl`: `phones on|off` and `phones-gain <dB>`.

### 3.7 App and web

- `manager/internal/mixer`: bounds `audio.phones` (state from a closed list, `gainDb` in −40…0, short `detail`)
  and lets the `phones` command through `POST /api/v1/mixer` with its validation. MANAGER.md §3.
- Web, Audio card: "Screen headphones" with a switch, a slider from −40 to 0 in steps of 2 (at the very
  bottom, "Mute"; disabled while the headphones are off) and a status line ("Waiting for the screen.",
  "The screen does not support audio.", "No sound: … Retrying automatically."). The volume, like the inputs': one
  command every 200 ms while dragging and the last one on release.

### 3.8 Screen

In the **audio view**, in the "Program" strip (the one with the L/R level meter), in the gaps it had free: at the
top the "Headphones" button (green when on; amber with "waiting", "screen without audio" or "retrying" below it), and
at the bottom the volume ("-12 dB", "mute" or "off") with − / + in steps of 2 dB, like the inputs. The L/R level
meters stay where they were. The audio view was changed in MR !34 and !38 (MIXER.md §18-§19): the strip is agreed with that work,
and it is marked `dirty` only outside the level meters (partial drawing contract, §17). Nothing new in the video view; if an
indicator is wanted, a small headphones icon next to the
program's vertical level meter. Decided with a PNG from `tools/ui_render.py` before installing. Measure `draw()`
against today's (audio view ~40 ms on the board).

### 3.9 Expected cost

About 25 calls per second from appsink to appsrc (40 ms chunks), one `volume` and one `alsasink` at 48 kHz
stereo: 1-2 points of one core (estimate). Measured in A3 with `engine-cpu.sh 60` (from `tools/`, copied to the board), with the
monitor on and off, as in MIXER.md §17.

## 4. Risks and expected traps

| Risk | What we would do |
|---|---|
| ~~The screen's jack does not output the HDMI audio, or it sounds very quiet or very loud~~ | Ruled out in A0: it plays, good level |
| ~~The 49 MHz pixel clock does not match the HDMI audio clock (N/CTS)~~ | Ruled out in A0: tones and program clean |
| Lazy ELD after reconnecting the screen (§1) | Open before looking at the ELD (§3.3) |
| Sometimes the screen gives NVIDIA's fallback EDID (1280×720, seen in F0) instead of its own, maybe without audio | `waiting` with "the screen does not support audio" and a retry every 30 s. Affects nothing else |
| ~~The screen flickers when the audio starts or stops~~ | Ruled out in A0 |
| Screen with a speaker (a different one from this) + monitor on at startup = feedback with the mic | This one has none. If the screen is ever changed, revisit D1 |
| PulseAudio starts by itself and grabs the card | `PULSE_SERVER=unix:/nonexistent` (already in the unit) and an explicit `hw:tegrahda,3`. Tell whoever is slimming down the base system (BASE.md): purging PulseAudio helps, and removing `alsa-restore` does no harm if the engine leaves `IEC958 Playback Switch` on |
| Closing `alsasink` hangs (like I2S without a clock) | Bounded close in a thread (§3.3) |
| Board power supply is tight (cuts on Oct 13) | The monitor barely consumes anything. A0 is light; the tests under load, once the power supply is approved |
| Clash with the work in progress in `engine_mixer.py` | A1/A2 on their own branch from `origin/main` (W-C already in main, MR !38). Checked on Oct 13: no other work in progress touched the audio tee or the audio view; this work does it and it gets reviewed |
| Plan to move the engine to Go (ENGINE_GO.md, branch `docs/motor-go`, not approved) | If it goes ahead, the monitor goes into its parity list (to be noted in its §2) |

## 5. Phases

**A0 · Does it play? DONE (Oct 13, §1.1).** It does not stop the engine or the stream: nobody uses the HDMI audio output.
What was run (to repeat it with another screen):

```bash
# 1. Wake the codec and look at the ELD
ssh <board> 'amixer -c tegrahda contents >/dev/null; head -3 /proc/asound/card0/eld#3.0'
# 2. 440 Hz tone at −20 dBFS for 10 s (without -e; with a cap)
ssh <board> 'PULSE_SERVER=unix:/nonexistent timeout -k 2 12 gst-launch-1.0 audiotestsrc wave=sine freq=440 \
  volume=0.1 is-live=true ! audio/x-raw,format=S16LE,rate=48000,channels=2 ! alsasink device=hw:tegrahda,3'
# 3. Display driver messages during the test
ssh <board> 'sudo dmesg | tail -20'
```

And the real program, without code, from the level meter's copy (only if nobody is looking at the web, which uses that port):

```bash
ssh <board> 'PULSE_SERVER=unix:/nonexistent timeout -k 2 15 gst-launch-1.0 -q udpsrc address=127.0.0.1 port=5005 \
  caps="audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved" ! queue leaky=downstream \
  ! volume volume=0.25 ! alsasink device=hw:tegrahda,3 sync=false'
```

**A1 · Engine.** `eirl-engine.sh` (branch with `phvalve`/`phtap`), `scripts/engine_phones.py` (pipeline,
lifecycle, volume), command and state in `engine_mixer.py`, scene, mixerctl, unit tests (states,
retries, `gain` range, reading the ELD and `/proc/asound`) that run on a Mac and on Python 3.6.
MIXER.md §5 and a new section.

**A2 · Screen and web.** "Program" strip of the audio view (PNG reviewed before installing), app (`internal/mixer`
and API) and web. MANAGER.md §3.

**A3 · Test on the board (scheduled board slot).** `tools/deploy-engine.sh` and `manager/build.sh`. Check:
it plays, in step with the screen (clap), it comes back on its own after restarting the engine and after unplugging
and replugging the screen, Pause with mute, mix mode, and 30 min streaming with the monitor on (output without
losses, CPU with and without the monitor, flat RAM). `/code-review`. Commit and MR once approved (`docs: n/a (encoder
in testing, D8)`).

### 5.1 A1 and A2 done (Oct 13, branch `feat/auriculares` on main b8f7b4d, uncommitted)

- **Engine**: `scripts/engine_phones.py` (new; in `FILES644` of `tools/deploy-engine.sh`), branch
  `phvalve`/`phtap` in `eirl-engine.sh`, and in `engine_mixer.py` the `phones` command, `audio.phones`,
  `audioSettings.phones`, `screen_delay_ns()`, `Screen.latency_ns()`, the shutdown within the engine's 4 s and the
  "Program" strip. `eirl-enginectl phones on|off` and `phones-gain <dB>`. `tools/ui_render.py` with
  `+phoff`, `+phwait`, `+pherr`, `+phmute` and `+nophones`.
- **App**: `internal/mixer` (type `Phones`, validated command, bounded state) and the web (Audio card).
- **Tests**: 151 Python tests (16 new, `test_engine_phones.py`, with the board's real `/proc` files) on a Mac
  and on the board's Python 3.6.9; Go (`gofmt`, `go vet`, `go test ./...`) and `node --check`.
- **Smoke test on the board** (Oct 13 ~20:20, `/tmp`, without touching eirl-engine): `Phones` with GStreamer 1.14 and a
  fake program (`audiotestsrc` → `audiomixer` → `tee`). Opens `hw:tegrahda,3`, state `on` (ELD valid on
  open), ramps −12 → −6 → mute, switching off closes the PCM and switching on reopens it, shutdown in 0.18 s. With
  `GST_DEBUG=audiobasesink:5`: no sample dropped as late; the drift between the HDMI clock and the
  program's, −0.3/−0.4 ms and stable. The only "Dropping contents" come from the stretch at −40 dB (`volume` at 0 marks the
  buffers as a gap: it is silence anyway). Second test (~20:45), after the code review: changing the delay from
  160 to 200 ms with the monitor playing adjusts it without cutting and without late samples.
- **Code review (Oct 13 ~20:40): good for the MR.** It checked the latency arithmetic against `on_program`, the
  shutdown (within the 4 s, the valve closes before releasing the appsrc) and that `volume` does not touch the audio being
  streamed (it copies the shared buffer). It asked to follow the screen's latency changes (done) and to measure in A3 the
  CPU with the monitor on and off and the "motor máx: dibujo" (engine max: drawing) of `engine-watch.py` while streaming: every
  program audio buffer goes through Python in the streaming thread (GIL).

### 5.2 A3 on the board (Oct 13, 20:20-20:41, scheduled board slot)

- **Installed**: engine from `feat/auriculares` with `tools/deploy-engine.sh` (backups `/usr/lib/eirl-encoder/engine/*.bak-20261013-2020` =
  main 895f90d, the W-C) and the app with `manager/build.sh` (`b8f7b4d-dirty`). At first it did not play (§1.3); fixed and
  installed again at 20:32 and without diagnostics at 20:34.
- **The operator**: it plays, "Now it works perfectly", and **the clap is in sync** with the screen's picture (~40 ms from it:
  not noticeable). The screen's button and − / + work (used: −2 … −12 and off/on).
- **CPU** (`engine-cpu.sh 60`, not streaming, preview stopped, 3 sources live): engine **86 %** with the monitor
  on and **85 %** off (1 point; with the previous engine, 84 %).
- **3 min streaming** to eu1 with the monitor on (`engine-watch.py 3`): engine 113-115 %, system 155-172 of 400,
  flat RAM (~130 MB), program at 50 new/s, output 3.4 Mbps and RTT 59 ms, "motor máx: dibujo" 59-69 ms, no warnings.
  The monitor stayed at 234 ms (no readjustments).

## 6. Out of scope

- **Direct monitoring** (hearing your voice without delay): the audio would have to be taken before the mixer's sync,
  from a specific input. It is a different feature; to be considered if requested.
- **PFL** (listening to an input that is not on air): likewise, a different feature.
- Control from the cloud and customer documentation (the encoder is still in testing, D8).

## 7. Plan B: USB sound card with a headphone output

If HDMI does not work (A0), the same monitor pipeline with another `device` (a cheap USB card with a jack). Drawbacks:
one more device on the hub, which on a power cut switches off all the ports (MIXER.md §16), and more consumption on a board
whose power supply is already tight. That is why it is an alternative, not the first option.
