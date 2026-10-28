# Multi-camera: composition, multiview and touch screen

> **Status:** IN PROGRESS (F0, F1 —single engine— and F2 —web ↔ screen— in main; F3 —no rehearsal mode— on the board; next F4, per-source audio)
> **Updated:** 2026-10-15 (Oct 15: the HDMI output stays lit when the screen closes, §3 trap 27; USB cameras plugged and unplugged while running, "Sources and slots" in §4, and a camera's audio follows its video slot, §10; the output protocols and encoding settings in the state and `output_config`, §5, [`OUTPUTS.md`](OUTPUTS.md); translated to English; the Python engine retired, the Go engine is the only one: [`ENGINE_GO.md`](ENGINE_GO.md); last content change Oct 13: header and Settings, §18; Wi-Fi in Settings, §19; Oct 14: renaming "Create my Wi-Fi", §19; everything the web configures, on the screen, §21; fades one step per program frame and the screen's tiles in step with the program, §22)

**The mixer is the encoder.** `eirl-engine` captures all the sources it detects at the same time, and what
comes out is always the composed **program** (cut, PiP, side by side, fades). That includes SRT/SRTLA
with adaptive bitrate, the web preview, the cloud thumbnail and the level meter. With the touch
screen on the board's HDMI output you choose what goes on air, and the encoder's web does the same
on the same state (F2). The full plan (architecture, budget, risks, phases and decisions) was kept in a
separate internal planning document ("Encoder: multi-camera, touch screen and PiP"). This document
collects what lives in the repo: what was
measured, the traps, how the engine works and its contract.

## 1. How it fits together (decided on Oct 12)

- **A single engine, and it is the default mode.** Since Oct 14 it is the Go engine, `eirl-engine-go`
  (`manager/cmd/eirl-engine`, [`ENGINE_GO.md`](ENGINE_GO.md)); until then it was a bash launcher plus a
  Python engine, retired that day. It validates the configuration and builds the pipeline. With `MIXER=1`
  (the value if nothing is said), the video head of that pipeline is the program compositor.
  `MIXER=0` goes back to a single source, the one in `SOURCE`.
- **The output is always the program**, and what you do is "put on air", live and without
  restarting. `SOURCE` and `VIDEO_DEVICE` become the **main** source: the one that goes on air at
  startup and the one the audio comes from (D7).
- **All detected sources, up to 4** (D11): the board's HDMI, the USB capture cards and cameras
  from the catalogue (SOURCES.md) and, with `SOURCE=test`, the bars.
- **A single state, the engine's.** The screen and the web send the same commands through the same
  socket (§5) and see the same state: whatever is touched in one place shows up in the other instantly (§8).
- **The screen's interface is drawn by the engine** (Cairo, D6). `eirl-encoderd` is left with the web,
  the configuration and the cloud.
- **No rehearsal mode (F3, §9).** The preview, the screen and the level meter are always running. Go live
  hooks up the output live and Stop streaming releases it; if it fails, it retries by itself.

```
 Board HDMI       ─▶ [own pipeline] ─▶ appsink ┐                 ┌─▶ NVENC ─▶ SRT/SRTLA (+ ABR)
 Cam Link (USB)   ─▶ [own pipeline] ─▶ appsink ┼─▶ appsrc m0…m3 ─▶ nvcompositor pgm ─┼─▶ WebRTC preview, thumbnail, level meter
 Logi MJPEG (USB) ─▶ [own pipeline] ─▶ appsink ┘  (program)       └─▶ pgmtap ─┐
                                 └────────── same buffers ────────────────────┴─▶ [multiview pipeline]
                                                                                    nvcompositor + UI layer ─▶ screen
 touch screen (evdev) ─┐
 web (eirl-encoderd)  ─┴─▶ /run/eirl-engine/mixer.sock ─▶ scene (on air, PiP, transition) ─▶ pgm pads
```

## 2. F0: result (Oct 12, on the board)

**G0 approved: carry on with `nvcompositor` on the Nano**, without our own compositor and without waiting for the
RK3588.

| Test | Result |
|---|---|
| Three real cameras (Cam Link 1080p60, webcam NV12 1080p30, board HDMI 1080p30) | Program, multiview and NVENC at **60 fps**; each input reaches both compositors complete |
| VIC headroom (clock on automatic, `f0/vic_load.sh`) | 3 sources ≈ **415 of 627 MHz (66 %)**; 4 sources ≈ 520 (83 %); always 60 fps, no freezes |
| Long test (`f0/soak.sh`, 2 h, scene every 5 s) | 1439 scene changes, **0 freezes, 0 errors**; 60 fps in 1437/1439 reports; 4.1 W average (5.3 max), ≥ 5.22 V, max 36.5 °C; RAM 751 → 842 MB (to be watched) |
| Unplugging a source while running (`f0/bridge_test.py`) | The engine stays at 60 fps (worst gap 38 ms) with "No signal"; when plugged back in it returns by itself in 2-3 s |
| Waveshare 7" touch screen (`f0/edid.py`, `f0/touch.py`) | 1024×600 physical panel; HID touch 0..1024 × 0..600 with no inverted axes and no calibration |
| `nvdrmvideosink` | Works without a desktop and without `tegra-udrm` |
| Boot screen (SPLASH.md) | The splash withdraws at ~8.2 s and the demo starts right after it |

## 3. Traps found (and their fix)

1. **`nvcompositor` only blends with alpha if all inputs are RGBA**
   ("pixel_format must be RGBA for blending operation"). Each input goes through an `nvvidconv` to RGBA.
2. **It does not accept rectangles outside the canvas** ("DstComp rect's left must not be greater than width").
   Hiding a layer = alpha 0. A PiP cannot slide in from outside.
3. **An `appsrc` with `max-latency` 0 prevents configuring the latency.** The compositor repeats frames
   (stutter) and gives ~57 "clock problem" warnings per second. The UI layer uses
   `min-latency=0 max-latency=500 ms` and a queue.
4. **Glitches with motion, only on the screen.** Under load, the compositor wrote into the buffer
   the screen was still showing. Fixed by a copy with 8 buffers of its own before `nvdrmvideosink`,
   with `sync=true`, and 10 buffers in the conversions towards the compositors. Pinning the VIC to its maximum
   was not enough.
5. **The screen's mode changes.** The same panel announces itself sometimes as 1280×720 and sometimes as
   1024×600. The multiview is drawn at the real mode (`/sys/class/graphics/fb0/virtual_size`). Otherwise,
   it comes out cropped.
6. **The board's HDMI "in jumps".** The compositor discarded its frames as late: its path
   carries two 4 MB copies by CPU and takes longer than the latency the source declares. The program
   only changed picture 5-6 times per second. Fixed with **`latency=60 ms` on both
   compositors** and **queues of 6** in front of them, because with 2 Cam Link frames were discarded.
   Measured with `--cadence 1`: repeated frame = byte-for-byte identical. With the camera at 30 and the program
   at 60 it must come out `1010…`.
7. **Program cadence.** At 50 fps with sources at 30 it gives irregular repeats. The launcher uses
   the Cam Link's cadence and, if it is not there, the board HDMI's doubled (30 → 60).
8. **MJPEG in the single pipeline.** The webcam in MJPEG stops or runs at 3-4 fps inside the demo. Neither
   more decoder surfaces, nor a forced copy, nor removing the latency fix it, and separately every piece
   works fine. **Through `appsink` → `appsrc` (isolated source) it gives a stable 29 fps.** The F1
   design solves it; meanwhile, the demo uses uncompressed NV12.
9. **A source can stop without an error.** The Cam Link stopped delivering frames 8 min into the long
   test (`uvcvideo: Non-zero status (-71)`), and the demo kept its last frame. Then it hung
   (`Failed to initialize the device`, -110). The USB reset (`authorized` 0/1) only recovered it
   right after reconnecting its HDMI. The engine needs a **per-source watchdog** and a retry with a
   cap, and a broken source must not prevent the others from starting.
10. **Uncapped retries exhaust the VIC.** Scheduling a retry for every error message created
    hundreds of pipelines in seconds: "Couldn't create ddkvic Session: Cannot allocate memory", the
    process ignored SIGTERM and had to be killed by force. Rule: **a single pending retry per
    source**, ignoring messages from pipelines already withdrawn.
11. **Startup.** After each exit of the demo, the HDMI turns off and the panel "unplugs" for a few
    seconds: an immediate attempt fails with "Failed to set plane". Also, at startup, the TC358743
    announces 640×480 until it locks. The launcher waits for the screen to be connected and stable for
    3 s, and for the board's HDMI to give the same mode 3 times in a row. The unit retries every 10 s,
    5 times at most. (Oct 15: the HDMI turning off was the screen's sink itself; cause and fix in trap 27.)
12. **The VIC clock can get stuck at its minimum.** Writing `performance` when it already is does not
    reapply the frequency, and the suspended VIC wakes up at 140 MHz: everything drops to ~27 fps. The launcher
    sets `power/control=on` on the VIC and changes the governor in two steps (`wmark_active` → `performance`).
13. **Limiting the VIC devfreq's `max_freq` does not lower its clock** (it stayed at 627 MHz). The headroom is measured
    with the clock on automatic, looking at the frequency it works at (`f0/vic_load.sh`).
14. **The TC358743's log-status says "Chip ID: 0x00": that is its normal identifier**, not a hang.
    "TMDS signal detected: no" with "Cable detected: yes" is a connected camera that is not outputting.

15. **`LC_ALL=C` and the output of other programs.** The former bash launcher exported `LC_ALL=C` and the
    board's Python 3.6 then decoded in ASCII. The messages of `eirl-encoderd probe` carried accents and the
    thread of each USB died with `UnicodeDecodeError`. The fix: read in bytes and decode in UTF-8, and an
    unexpected failure does not kill a source's thread: it stays in "error" and is retried.
16. **The scene names sources, not slots.** When a source appears the scene has to be rearranged: otherwise,
    the program comes out black even though the source is live.
17. **"No signal" from the engine and not from the source.** Opening a hung device ("failed during
    initialization") blocks its thread for several seconds, and its slot stayed black. A
    timer in the engine paints it every 250 ms for every slot that has had no picture for more than 0.5 s. It also
    covers the source that stops without an error, before its watchdog reopens it after 3 s. The picture
    is the bars slate with "NO SIGNAL" (see §4, "Scene").
18. **A hung capture card lengthens the boot.** With the Cam Link not enumerating (`device descriptor
    read/8, error -110`), `systemd-udev-settle` waited 34 s and the board took 40 s to boot. The
    splash withdrew at 39.9 s instead of at ~8.4 s. It does not depend on the mixer: it has to be reconnected.

19. **Closing I2S audio without a clock takes ~5 s.** With the HDMI camera off, the TC358743 gives no BCLK and
    `alsasrc` waits for the driver's timeout when going to NULL. With the output active, the shutdown went beyond
    the unit's 10 s. The launcher gives the final NULL 3 s and, if it does not finish, exits anyway (the kernel
    closes the devices with the process).
20. **`voaacenc` asks for `channel-mask` in stereo.** The fixed caps of an audio `appsrc` do not carry it
    (`not-negotiated`): an `audioconvert` is needed in front, like the one in the single pipeline.

21. **Timestamps do not cross from one pipeline to another.** Each pipeline has its own `base_time`.
    A buffer that reaches an `appsrc` with the source's timestamp (its running time) does not lose it:
    `do-timestamp` only stamps the ones that do **not** carry a timestamp. The source started after the program,
    so its frames arrived seconds late and the compositor discarded them all as
    late. The program was **frozen since F1** (at 60 fps of buffers, with the last accepted
    frame), and the screen, which starts later, lagged behind. The encoder filled ~2 Mbit/s with
    the keyframes of a still picture; the thumbnails (without `sync`) looked fine and
    hid it. Fix (Oct 12): `unstamp()` strips PTS/DTS from every buffer that crosses (sources, "No
    signal", program → screen) and each `appsrc` stamps it on arrival. The output (F3) does not need it:
    it shares `base_time` with the program. So that it does not happen again, the engine measures the **new
    frames per second** of the program (`program.newFps`, with a warning in the log if they drop below 30 %).
22. **The VIC does not downscale more than 16 times.** The layer of the Pause when off was a 64×36 rectangle:
    going from 1920×1080 to that is 30 times smaller and `nvbuffer_composite Failed` brought down the program in
    a loop ("Internal data stream error" on `m0`). A small invisible layer is at least 1/8 of the
    canvas (`pause_off`). The same goes for any rectangle: the PiP at 25 % with a vertical
    source ends up at ~1/13.
23. **The arrival timestamp stutters and desynchronises.** Each buffer was stamped on reaching the program.
    The former Python engine passed every frame, every audio chunk, the program towards the screen and the output
    through Python, and with the GIL the arrivals came in bursts, with gaps of up to 100-130 ms. Several frames fell into the
    same compositor slot and others did not arrive: the program gave 13 new frames out of 60 while streaming.
    Also, the video and the audio of a source take different times to arrive, and with the arrival timestamp the
    sound ran ahead. Fix (Oct 12): each buffer enters with the **time of its capture + `MIXER_SYNC`**
    (120 ms), on the common clock. The screen shares clock and base_time with the program: that way a frame
    carries ONE timestamp, set on the buffer itself. Copying it does not work: the copy shares the NVMM memory, the
    original goes back to its pool and is reused with the copy still queued, with errors in a loop from the
    slot's appsrc. Each appsrc's timestamp never goes backwards. If a source's timestamps are not
    reliable, it uses the arrival one: `CaptureJudge` decides it with the minimum age of each window. The Logi,
    half hung, gave 47 s and rising by 2 %; a USB capture card, a fixed 1.85 s.
24. **Tegra's CSI stamps with `CLOCK_MONOTONIC_RAW`.** v4l2src treats the timestamp as if it were from
    `MONOTONIC`. NTP corrects that one and not RAW, so they drift apart with hours of uptime (419 ms at
    4 h). That is why the board's HDMI seemed to arrive half a second late; the real delay is 66-80
    ms. Corrected with the difference between the two clocks (`raw_offset()`). The first frames after opening
    carry odd timestamps, and `CaptureJudge` waits for the first window.
25. **An appsink nobody reads keeps the old stuff.** When not streaming, the output branches are not
    read (it saved Python CPU in the former engine), but `outa` kept its last buffers. When streaming, one was read for each new
    one: the audio was always behind, the mux waited and the encoder produced 5-10 fps. Now they are flushed
    when they are hooked up.
26. **A `continue` for every level message left the audio in "opening".** The loop that watches each
    audio input jumped to the next iteration with every `level` message (every 200 ms). With chunks
    of 40 ms (§12) those messages arrive before the loop's wait deadline, so it never
    checked whether the input was live, nor the I2S "no signal", nor the watchdog that reopens a
    USB without samples. The audio reached the program anyway, but the web did not show the levels.
    Fixed in F4b (§13).
27. **Closing the screen switched the HDMI output off.** When the screen's pipeline closes, `nvdrmvideosink`
    takes its plane off (SetPlane with no framebuffer). NVIDIA's libdrm treats the last plane leaving the head
    as switching the head off: `libnvdc`'s `nvdcDPMS` sends `FBIOBLANK` with power-down to `/dev/fb0` and
    tegradc turns the HDMI off (`tegradc.0: blank - powerdown` in the kernel log, ~0.5 s after the engine
    starts stopping). It happened on every engine restart (an update, saving a destination) and on every
    retry of the screen. The panel shows "no signal", ~4.3 s later drops its hot plug (`hdmi: unplugged`) and
    comes back with a re-plug; if the new engine had painted just before, its screen failed ("Internal data
    stream error", the "Failed to set plane" of trap 11) and the HDMI went off a second time. Measured on Oct
    15: 5 s without signal and 5-12 s until the multiview. The sink runs with the default `set-mode=false`,
    which is already the SetPlane path, and `libnvdc` refuses to open the display without `/dev/fb0` (any
    error but "does not exist"), so neither a property nor hiding the device avoids it. **Fix**: the engine exports its
    own `ioctl` (`internal/engine/displayhold`, C), which the display libraries the engine loads resolve
    before libc's. It holds back those blanks (suspend and power-down levels) and lets everything else
    through. A mode set on the same fd gets the held blank just before (`nvdcSetMode` sends power-down,
    mode, unblank), and an unblank cancels it. With the head on and no plane, tegradc shows its background,
    black: the screen stays black **with signal** until the next screen paints. The engine logs
    `display_hold active=true` when it starts (`false`: libc's `ioctl` won, the fix is not working) and, when it
    stops, how many blanks it held. Since the sink leaves the framebuffer's window switched off, `eirl-splash`
    brings it back with a pan when it paints "Restarting…" (SPLASH.md §3). Checked on the board on Oct 15
    (0.6.0 plus the fix): before, stopping the engine gave `blank - powerdown` at 0.7 s and `unplugged` 4.3 s
    later; with it, three restarts without either, the new screen at the first attempt (`no_display=0`) and
    the multiview back 8 s after the restart began (4 s of that is the old engine stopping and 3 s the wait
    for a stable display, which can now be revisited). With the engine stopped, window A of the head is off
    (black, with signal) and `eirl-splash -scene reboot` turns it back on with `fb0`.

## 4. The engine (F1)

| Piece | What it does |
|---|---|
The engine was Python until Oct 14; the Go engine replaced it with the same behaviour and contract. Its
pieces today (`manager/internal/engine/…`):

| Piece | What it does |
|---|---|
| `scripts/eirl-engine-start` | The service's `ExecStart`: runs `eirl-engine-go` with `MALLOC_ARENA_MAX=2` and `GOGC=50` |
| `manager/cmd/eirl-engine`, `pipeline`, `hdmi` | With `MIXER=1` it does not wait for any source. It loads the EDID, chooses the program cadence and sets as video head one `appsrc` per slot → RGBA → `nvcompositor name=pgm` (plus the `pgmtap` branch for the screen). The HDMI mode and USB device watchers do not kill the stream; the I2S audio one (`tcaudio`) stays |
| `manager/internal/engine` (+ `scene`, `screen`, `server`) | The engine: sources, scene, screen, touch and socket |
| `output` | The live output (F3, §9): NVENC + MPEG-TS pipeline, `srt-live-transmit`, `srtla_send` with its links, statistics and adaptive bitrate |
| `scripts/eirl-enginectl` | Socket client for testing: `state`, `watch`, `take 2`, `secondary 1`, `swap`, `layout pip`, `transition fade`, `pip tl 25`, `pause on "text"`, `lock on`, `audio Logi`, `audio-follow on`, `audio-delay Logi 80`, `audio-link hdmi Logi`, `auto 5 [min]` |
| Tests (`build/engine.sh`) | Go tests: scene, commands, geometry (PiP, letterboxing, Pause), fades, parsing of `v4l2-ctl` and `probe`, multiview, held touches, socket, and parity with the former engine |

**Sources and slots.**
- The source catalogue (`eirl-encoderd probe -list`) is read at startup and every time
  `/dev/v4l/by-id` or `/dev/v4l/by-path` changes (checked every 2 s; by-path too because two capture cards
  with the same serial share one by-id link), and once more 3 s after each change, for what was still
  binding the first time (a camera's sound card). The board's HDMI goes in slot 1 and the USB ones in
  catalogue order.
- Each source's id is the catalogue's: `hdmi`, the USB's by-id path or `test`.
- Each source has its own thread and pipeline (`… ! nvvidconv output-buffers=16 ! NV12 NVMM 1920×1080
  ! appsink`). Its buffers go to its slot's `appsrc` in the program and in the multiview.
- States: `starting` · `live` · `no_signal` (HDMI without a signal, or the mode is not stable yet: 3 equal
  readings) · `unsupported` (interlaced, 1080p60 over CSI, or it does not fit in the bus) · `gone` (USB
  unplugged) · `error`.
- Watchdog: 3 s without frames → that source is reopened, and only that one. With the source live, every 2 s it
  checks that the HDMI is still in the same mode and that the USB is still plugged in. Retries after 1, 2, 4 and 8 s.
- **Plugging and unplugging while running** (Oct 15; [`SOURCES.md` §8](SOURCES.md#8-plugging-and-unplugging-while-running)):
  a USB camera that is plugged in appears in a free slot within ~2 s; one that is unplugged shows
  "disconnected" for ~3 s and then **leaves its slot** (black and empty, gone from the state, and its audio input
  too), like a network camera removed from the web. It comes back to the **same slot** if that one is still free (the
  ones coming back are placed before a new camera). Exceptions: a camera **on air** (the main one or, in PiP and side by side,
  the secondary) keeps its slot with the slate until it is taken off air, because the engine never changes what goes out
  by itself; one that only **dropped off the bus but is still on its port** (hung) stays for the USB recovery (§16);
  and for 20 s after a hub's power cut nothing leaves (everything under the hub drops and comes back by itself). A
  pinned slot stays reserved and black while its camera is unplugged.
- A slot whose USB camera has been gone for over 10 s without leaving it (a hung one, not on air) is reused only if
  another one arrives and there is no free one left. A camera that arrives with every slot taken gets one as soon as
  another camera leaves.
- **USB.** With `eirl-encoderd probe -all` (SOURCES.md §1) the bus is shared among all the USB ones and
  what is already live is kept (`-keep`): a camera on air does not change mode because another one is plugged
  in. With a binary without `-all`, each one is chosen on its own as if it had the whole bus.
  `exposure_auto_priority=0` on each camera, as the former bash launcher did.

**Scene** (`take`, `secondary`, `swap`, `layout`, `transition`; §5).
- **On air** changes the main one. The secondary (PiP, side by side) does not change, unless the new
  main one was it: then they swap.
- **Fade** of 400 ms (0.6 s with a layer that moves afterwards; §11), one step per program frame (§22). PiP in the bottom-right corner, at ⅓ of
  the size.
- The scene is saved in `STATE_DIR/mixer-scene.json` (`/var/lib/eirl-engine`), so it **survives Go live,
  Stop streaming and Apply**. If the configuration's main source changes, it starts with that one.
- A source on air without a signal leaves the **bars slate** in the program. The engine never switches
  source by itself. The slate (`still_desc('slate')`, Oct 13) is `videotestsrc`'s SMPTE with "NO SIGNAL"
  (`SLATE_TEXT`) in a black box. It is generated once at startup (340 ms at 1080p on the board) and
  `slate_tick` re-pushes the same buffer. Two tweaks over GStreamer's:
  - The bottom-right corner is **black**, as in the original test card. `videotestsrc` puts noise
    there, which does not compress and would "breathe" at each keyframe of the stream.
  - The text is painted in **BGRx**. In YUV, `shaded-background` only lowers the luma and the box comes out tinted
    with the colour of each bar.

  `textoverlay` reads the text as Pango markup, and with `<` or `&` it paints nothing: `SLATE_TEXT` follows
  the same rule as the Pause text (`pause_text`, with a test). It is in English by the maintainers' decision;
  **pending:** translate it according to the encoder's language. A hidden source, or one that has been opening
  for less than 2 s, stays black. In the web, a thumbnail without a picture shows the same bars,
  dimmed.
- A USB capture card that streams without a signal (the MS2109 sends black) gets the slate too: the engine reads
  what the card says (the MS2109's "no signal" flag, over HID) or, for any other card, sees its picture still and
  nearly uniform for 5 s; it holds its frames in the bridge and puts the source in `no_signal` (Oct 21,
  [`SOURCES.md`](SOURCES.md) §9).

**Screen.**
- It runs in its own pipeline: if it fails or is unplugged, the stream carries on.
- It waits for the splash to release the screen (`ActiveState` of `eirl-splash.service` in
  `inactive`/`failed`, SPLASH.md §3 variant b). eirl-engine is **not** `After=` the splash: the stream
  does not wait.
- Then it waits for the screen to be connected (`/sys/class/switch/hdmi/state`) with the same mode for
  3 s, and draws at its real mode.
- If it fails, it retries after 3, 6… 30 s. If the mode changes or it is unplugged, it is rebuilt.
- Between two screens (a retry, an engine restart) the HDMI output stays lit and black: the engine holds
  back the power-down of NVIDIA's display libraries (trap 27).
- 1 fps snapshot in `/run/eirl-engine/screen.jpg`.
- Touch through evdev; if it is unplugged, it looks for it again:
  - tap on a thumbnail: that source on air;
  - long press (≥ 0.6 s): to the PiP;
  - tap on the program: swaps main and secondary;
  - buttons: Full, PiP, Side by side and Cut/Fade.
- The panel also shows "LIVE · Mbps · RTT" or "REHEARSAL".

**Quality of the main encoder** (in `/etc/default/eirl-engine`):
- `H264_PRESET`: 1 UltraFast (the old one), **2 Fast (default)**, 3 Medium, 4 Slow;
- `H264_TWOPASS`: two-pass CBR, **1 by default**;
- `H264_QP_RANGE` (`minP,maxP:minI,maxI:minB,maxB`): with bitrate control active, NVENC ignores it in practice.

Fast + two passes, measured on the board at 1080p60: 60 fps without losing frames. Every 10 s the output measures the
frames entering the encoder, the encoded ones and the Mbit/s (`output.inFps`, `encFps`, `encMbps`).

**Program cadence (D10).** 50 fps if all the sources known at startup are 25 or 50; otherwise,
60 (the 30 ones come out twice per frame). `FPS` in `/etc/default/eirl-engine` pins it.

**Configuration** in `/etc/default/eirl-engine`:
- `MIXER`: 1 by default; 0 = a single source;
- `MIXER_SLOTS` (1-4, 4 by default);
- `MIXER_LATENCY` (ms, 60);
- `MIXER_SYNC` (ms, 120): from each source's capture to the program; A/V sync and stutter cushion (§12). 0 = arrival timestamp;
- `MIXER_SCREEN` (1);
- `MIXER_HDMI_CAPTURE` (`nvcam`): the board's HDMI with `nvv4l2camerasrc`; `v4l2` = with `v4l2src`, as before (§17).

They are not in the app (§8).

**Measured on the board (Oct 12, rehearsal, board HDMI 1080p30 + Logi MJPEG 1080p60 + screen):**
- Program at 60 fps. The Logi in MJPEG gives 58.5 fps: isolated, it no longer stops (trap 8).
- Board CPU at ~30 % per core; the process, at 86 % of one core adding up its threads.
- 1.3 GB of RAM out of 4. VIC at 627 MHz with its stock governor (`wmark_active`); pinning it is no longer
  needed. 34 °C.
- SRTLA stream to eu1: connects, RTT 59 ms and 0 packets lost. The bitrate depends on the picture
  (0.9 Mbit/s with the Logi facing a wall, 1.5-2.7 with the HDMI camera): NVENC does not pad.
- Startup (with the Cam Link hung, trap 18): eirl-engine at 39.7 s, sources live at 45 s and
  multiview at 46.7 s.

**Known limitations (for the next phases):**
- The audio of a USB source goes inside the streaming pipeline: if that source is unplugged,
  everything restarts. F4 isolates it, with "the audio follows the video".
- The slots are assigned by the engine (the board's HDMI first, the USB ones in catalogue order):
  they are not reordered by hand.
- A capture card that USB no longer enumerates (Cam Link `-71` and then `-110`) does not come back without
  reconnecting it (F5: port reset).

## 5. Command and state socket (contract)

`/run/eirl-engine/mixer.sock`, Unix, 0660, group `eirl-encoder`. It speaks JSON, one object per line
(max. 4 KB per line, 8 clients). A client that does not read (more than 256 KB pending) is disconnected. The
state is also copied to `/run/eirl-engine/mixer.json` (0644) on every change.

| Command | Fields | Errors |
|---|---|---|
| `take` | `source`: id | `unknown_source`, `hidden_source` |
| `secondary` | `source`: id (the main one: swap) | `unknown_source`, `hidden_source` |
| `swap` | — | `no_secondary` |
| `audio` | `source`: `i2s` · `alsa:<card>` · `none`. The audio input that plays (F4, §10), with a 250 ms fade | `unknown_source`, `unsupported` |
| `audio_follow` | `on`: bool. "The audio follows the video": putting a source on air plays its linked audio (§13) | `bad_request`, `unsupported` |
| `audio_delay` | `source`: `i2s` · `alsa:<card>`, `ms`: integer from 0 to 500. Delay of that input relative to the video | `bad_request`, `unknown_source` |
| `audio_mode` | `mode`: `single` (`main` plays) · `mix` (the ones in the mix play at the same time) (§15) | `bad_request`, `unsupported` |
| `audio_mix` | `source`: `i2s` · `alsa:<card>`, and `on` (in the mix) and/or `gain` (dB, −40…+12). With a fade | `bad_request`, `unknown_source` |
| `audio_link` | `video`: video source id, `source`: `i2s` · `alsa:<card>` · `none`. The audio of that source with `audio_follow` (`none` = no audio of its own: whatever was playing keeps playing) | `unknown_source` |
| `output` | `on`: bool. Start or stop streaming live (F3); the app also saves `OUTPUT` in its configuration | `unsupported` (no live output), `bad_request` |
| `hide` | `source`: id, `hidden`: bool. Hidden = it stops being captured (frees bus and VIC) and cannot be put on air; if it was the secondary, it stops being so | `unknown_source`, `on_air` (the one on air is not hidden), `bad_request` |
| `layout` | `layout`: `full` · `pip` · `side` | `bad_layout` |
| `transition` | `transition`: `cut` · `fade` | `bad_transition` |
| `pip` | `corner`: `tl` · `tr` · `bl` · `br` and/or `size`: `25` · `33` (PiP width, % of the canvas) | `bad_request` |
| `pause` | `on`: bool, `text`: 1-60 characters (letters, digits, space and `.,:;!?¡¿'"()-–—…·/+#@%€$*`; no `< > &`, line breaks or emoji), `mute`: bool. At least one | `bad_request`, `unsupported` (pipeline without the `mp` layer) |
| `lock` | `on`: bool. Lock the touch screen | `bad_request` |
| `preview` | `on`: bool. The web's WebRTC preview: the app stops it while nobody is watching and repeats it every 30 s (§17). The state reports it in `preview.on` | `bad_request`, `unsupported` (no preview, `LIVE=0`) |
| `phones` | `on`: bool and/or `gain`: integer dB from −40 (silence) to 0. The screen headphones: the program audio over the board's HDMI ([`HEADPHONES.md`](HEADPHONES.md)). At least one | `bad_request`, `unsupported` (pipeline without `phvalve`/`phtap`) |
| `camera_control` | `source`: a USB source id, `control`: a control id, `value`: integer. A camera's image setting ([`SOURCES.md`](SOURCES.md) §7): answered once the camera took it, with the state already carrying it | `unknown_source`, `unsupported` (the source has no `controls`), `unknown_control`, `bad_request`, `inactive` (its automatic is on), `busy`, `camera_error` |
| `camera_reset` | `source`. Every image setting of that camera back to its default, and its saved values forgotten | `unknown_source`, `unsupported`, `busy`, `camera_error` |
| `get` | — | — |
| `subscribe` | — (the state now and on every change, as `{"event": "state", "state": {…}}`) | — |

Request: `{"id": 7, "cmd": "take", "source": "hdmi"}`. Response: `{"id": 7, "ok": true}`, or
`{"id": 7, "ok": false, "error": "unknown_source"}`. `get` and `subscribe` answer with `state`. A
valid command that changes nothing gives `ok: true`. Other errors: `bad_json`, `bad_request` (no `cmd`, or
an `id` of more than 64 characters), `unknown_command` and `timeout`.

```json
{"v": 1,
 "program": {"main": "hdmi", "secondary": "/dev/v4l/by-id/usb-046d_Logi_…-video-index0", "layout": "pip",
             "transition": "cut", "hidden": [], "width": 1920, "height": 1080, "fps": 60, "measuredFps": 60.0},
 "slots": 4,
 "sources": [{"id": "hdmi", "slot": 1, "kind": "hdmi-csi", "name": "Board HDMI", "state": "live",
              "detail": "", "format": "UYVY", "mode": "1920x1080@30", "fps": 30.0}, …],
 "screen": {"state": "on", "width": 1024, "height": 600, "touch": true},
 "output": {"mode": "on", "kbps": 6000, "rttMs": 59}}
```

`audio` (F4): `{"main": "alsa:Edition" | null, "sources": [{"id": "i2s", "slot": 1, "kind": "i2s",
"name": "Board HDMI", "state": "no_signal", "detail": "…"}, {"id": "alsa:Edition", …, "state":
"live", "peakDb": -48.0}]}`. States of an input: `starting` · `live` · `no_signal` (I2S without a camera
or without sound) · `gone` · `error`. Since §13, also: `follow` (bool), `links` (`{video id: audio id
or null}`, already resolved: if nobody has changed it, the audio of the same device) and `delayMs` on each
input, and `videoSlot` (the slot of the camera of the same device; the inputs are sorted by it).
Since §15: `mode` (`single` · `mix`) and, on each input, `inMix` and `gainDb`. Since [`HEADPHONES.md`](HEADPHONES.md):
`phones` `{"on": true, "gainDb": -12, "state": "on"}`, with `state` `off` · `waiting` (no screen, or one that does not
support audio: `detail` says which) · `on` · `error` (retrying; `detail` = the reason). It is saved with the scene
(`audioSettings.phones`).

**Bluetooth audio ([`BLUETOOTH.md`](BLUETOOTH.md) §3.4)**, additive:
- `audio.phones` gains `target` (`screen` · `bluetooth`: where the monitor plays, or would play while off) and, with
  `bluetooth`, `device` (the headphones' name). With Bluetooth headphones ready (the app's file
  `/run/eirl-encoder/bluetooth.json`), the monitor plays through them; when they go, through the screen's jack again.
  Same `phones` command, same switch and volume.
- `audio.sources[].kind` gains `bluetooth`: a headset's microphone (hands-free) or a phone (A2DP), with id
  `bt:<address without colons>` (`bt:38184C12A001`), valid wherever an audio id is (`audio`, `audio_link`, `audio_delay`,
  `audio_mix`). It takes an audio slot like a loose USB microphone. Its `no_signal` is `bt_no_audio` (connected, sending
  nothing: a phone that is not playing), and it is not reopened; disconnected, `gone`, and its slot is freed after the
  same grace as an unplugged card.

Camera image settings ([`SOURCES.md`](SOURCES.md) §7.3): a USB source whose camera has some of the standard
controls carries `controls` (`id`, `group`, `type` `int` · `bool` · `menu`, `min`, `max`, `step`, `default`, `value`,
`inactive`, `options`); without any, the key is absent.

Composition (§11): `program.pip` `{"corner": "br", "size": 33}`, `program.pause` `{"on": false,
"text": "Be right back", "mute": true}`, `screen.locked` (bool) and, in `output`, `by` (`start` ·
`socket` · `screen`: who changed `mode` last) and `seq` (goes up on every `mode` change). With
`by: "screen"` and a new `seq`, the app saves `OUTPUT`: that way what is streamed from the screen
survives a restart and the cloud sees it.

**Output protocols and encoding ([`OUTPUTS.md`](OUTPUTS.md) §8)**, additive: `output.protocol` (`srt` · `srtla` · `rtmp` ·
`rtmps` · `rist` · `udp`) and `output.codec` (`h264` · `h265`), also while off; `output.size` (`"1280x720"`) and
`output.fps` while on. Reasons gain `tls_failed` and `clock_not_synced`. A new command, `{"cmd": "output_config"}`: the
engine rereads the app's file and takes the output's keys live (destination, bitrates, codec, size, rate), restarting the
output session if it is on air; it answers `restart_required` when anything else changed, `invalid_config` when the file
is invalid, and an older engine `unknown_command` (the app then restarts it).

`output` is used by the screen's panel ("LIVE · Mbps · RTT") and `eirl-encoderd` forwards it,
bounded, in `GET /api/v1/mixer` and in the `mixer` SSE (F5 review: before, this document said the
opposite). Its `detail`, the libsrt line without the time, also goes through the app's redactor
(`service.Redact`) before reaching the web and the cloud. Source ids are at most 217 characters long (`/dev/v4l/by-path/` + 200).
`screen.state`: `off` · `waiting` (splash) · `on` · `error`. Free slots do not appear in
`sources`. `fps` of each source: measured every 2 s. State of a hidden source: `hidden`.

Everything else the screen asks of the app (Settings: the network cameras, §18) goes through the app's local API, over its
Unix socket, and not through this socket or the state: here only `screen.power` (§14) remains, which stays like this so that
a restart is possible even if the API hangs.

**Network cameras (F7, [`NETWORK_SOURCES.md`](NETWORK_SOURCES.md) §6.4-§6.5)**, additive:
- `sources[].kind` and `audio.sources[].kind` gain `network`. Ids `net:cam1` … `net:cam8` (today, at most one
  camera), the same for the video and the audio of one camera; they are valid in `take`, `secondary`, `hide`, `audio`,
  `audio_link`, `audio_delay` and `audio_mix`.
- `format`: `H264` · `H265` (what the camera sends); `mode`: the decoded size (a phone in portrait gives
  `1080x1920@30`).
- No sender: `state` `no_signal`, `detail` "Waiting for the camera"; if MediaMTX does not respond, "the camera
  receiver is not responding".
- The network side (protocol, kbps, RTT, losses, sender IP, over Wi-Fi or over cable) does not go through the socket: the app
  gets it from the MediaMTX API (`GET /api/v1/network`, MANAGER.md §3).

Images in `/run/eirl-engine` (with rename, never half-written): `source-<slot>.jpg` (320×180, one per
second while the source is live) and `screen.jpg` (what the screen shows, 1 fps; deleted
when it switches off). The web requests them through `GET /api/v1/mixer/…` (MANAGER.md §3).

## 6. Install and undo

`tools/deploy-engine.sh` (`--unit` also installs the units): it runs `build/engine.sh` (tests and the
arm64 build), copies `eirl-engine-go`, `eirl-engine-start` and `eirl-enginectl`, does not install while
streaming, removes the former Python engine's files from the board, leaves
`/usr/lib/eirl-encoder/engine/VERSION` and checks that the new run starts. Then:

```bash
ssh <board> 'eirl-enginectl watch'        # live state
ssh <board> 'eirl-enginectl take 2'       # slot 2 on air
# A single source, as before: MIXER=0 in /etc/default/eirl-engine and restart eirl-engine
```

The F0 demo (`eirl-mixer-demo.service`) is no longer installed: the screen belongs to eirl-engine. If it were still on
some board, `sudo systemctl disable --now eirl-mixer-demo && sudo rm -r /etc/systemd/system/eirl-mixer-demo.service /usr/local/lib/eirl-mixer-demo`.

## 7. Measurement harness (`mixer/f0/`)

| File | What for |
|---|---|
| `scene_test.py` | Complete graph with automatic scene changes and a freeze detector |
| `bridge_test.py` | Isolated source (`appsink` → `appsrc`, NVMM) with "No signal", a 3 s watchdog and a single retry: unplugging while running |
| `soak.sh` | Long test of the engine (since F1): scene changes through the socket (`eirl-enginectl auto`), tegrastats and overcurrent (`./soak.sh <minutes> [s]`) |
| `demo.py`, `demo_start.sh` | The F0 touch demo in a single pipeline (no longer installed). Used by `vic_load.sh` and `mjpeg_test.sh`, with eirl-engine stopped |
| `vic_load.sh` | VIC headroom with 3 and 4 sources, with its clock on automatic |
| `mjpeg_test.sh` | MJPEG variants inside the demo (trap 8) |
| `tear_test.sh` | Direct pattern, through the compositor and direct Cam Link: where does a tear come from? |
| `edid.py`, `touch.py` | The screen's EDID (`/sys/kernel/debug/tegradc.N/edid`) and touches, without `edid-decode` or `evtest` |

They assume the test board (`by-id` paths of the Cam Link and the Logitech, HDMI on `/dev/video0`).
They write to `mixer/f0/logs/` unless `LOGS=` is passed.

## 8. F2: the web and the screen, a single control (Oct 12)

- **`eirl-encoderd`** (`manager/internal/mixer`) talks to the §5 socket. It exposes:
  - `GET`/`POST /api/v1/mixer`;
  - the images;
  - the state in `GET /status` (`mixer`);
  - an SSE event `mixer` on every change (MANAGER.md §3).

  Commands are validated in the app **and** in the engine. What comes from the engine is only forwarded if it is
  in its structures.
- **Web.** The preview carries the "PROGRAM" label and the **"Sources"** card is added:
  - one thumbnail per slot, with "ON AIR", "PiP"/"2nd", "No signal" or "Free";
  - tap = on air; "PiP" = secondary; "Hide"/"Show";
  - Full, PiP, Side by side, Cut/Fade and Swap;
  - "View the encoder screen" (its picture, at 1 fps).

  The "Signal" card shows the program and how many sources are live. "Input" becomes "Main
  source": it is the one that goes on air at startup and the one for audio.
- **Measured on the board (Oct 12, over VPN):**
  - a command from the web changes the program and the screen;
  - a command through the socket, like the screen's, reaches the web through its `mixer` event without waiting
    for the 2 s poll;
  - on the phone (390 px), no horizontal scroll and no console errors.
- **`MIXER` does not go to the app.** It is the script's default mode, and switching it off (`MIXER=0`) is a matter of
  the installation, in `/etc/default/eirl-engine`. The app always writes all its keys and its file
  overrides that one: with `MIXER` in it, an app that is not updated would pin it forever.

## 9. F3: no rehearsal mode, hot-switched output (Oct 12)

The request: "there should be no rehearsal mode; the preview must always be visible and, when streaming
starts, connect to the output channel".

- **Two pipelines with the same clock.** The program's never changes: sources, preview, screen,
  level meter and thumbnail. It leaves two branches (`outv`, `outa`).
  The **output** is another pipeline: `appsrc` → copy to its pool (`nvvidconv` to I420) → NVENC + AAC →
  `mpegtsmux` → FIFO. It is created and destroyed by the output (`manager/internal/engine/output`) on each stream, with the program's clock and
  `base_time`: the buffers arrive with their timestamps and audio and video stay in
  step.
- **The output processes go with the stream, not with the service.** `srt-live-transmit`,
  `srtla_send` with its links (and the SIGHUP on change), the statistics for the app and the adaptive
  bitrate are run by the engine's output; before F3, the bash launcher launched them. The destination, which carries
  streamid and passphrase, arrives through the environment and not through arguments.
- **State and reasons.** `off`, `connecting`, `on`, `error`, and the reason from a closed list
  (MANAGER.md §3). A cut with the relay does not bring down the output: `srt-live-transmit` reconnects by itself and,
  meanwhile, `connecting` carries the reason. Not connected within 10 s, `relay_unreachable`. If a
  process dies, it is retried after 2, 4, 8, 15 and 30 s.
- **Configuration.** With the mixer, the configuration fingerprint does not count `OUTPUT`, so
  Go live does not leave "unapplied changes". The app, the script (`CONF_SHA`) and `config.Store.Hashes` use
  the same rule. Changing the destination or the bitrate does restart (since [`OUTPUTS.md`](OUTPUTS.md), only the
  output: `output_config`).
- **Cloud.** `start` = start streaming and `stop` = stop streaming, live; `rehearsal` = `stop`. The phase
  `ready` replaces `rehearsal` and `outputState` arrives (ENCODER_CLOUD.md §13.3). The EnhancedIRL cloud
  already accepts it.
- **Web and screen.** The Go live ↔ Stop streaming button replaces Go live / Rehearsal / Stop. The
  pill says Not streaming, Connecting…, Live or No connection, and the SRT card gives the reason. The
  screen's panel says NOT STREAMING, CONNECTING, LIVE or NO CONNECTION.

**Measured on the board (Oct 12, SRTLA to eu1 over one link):**
- **Go live from the web**: connects in ~2 s, without restarting (same eirl-engine PID), with the program at
  60 fps and the screen not re-hooking.
- **Stop streaming**: `srt-live-transmit` and `srtla_send` go away.
- **Preview**: the same WebRTC connection, without cuts, during the whole cycle.
- **Removing the only SRTLA link while streaming**: goes through `connection_lost` and then `no_uplinks`
  ("waiting for an internet link"). When it is put back, it streams again in ~3 s. The program does not
  drop below 60 fps.
- **Service shutdown**: with the output active it hung until the SIGKILL at 10 s. It was ~5 s of
  `alsasrc` closing the I2S of the board's HDMI without a clock, with the camera off (trap 19). Now
  it takes 4.2 s.

## 10. F4a: audio, independent of video (Oct 12)

Decided (Oct 12): by default **a single source** plays, and the audio can come from an input other than
the video's, for example a USB microphone.

- **Each audio input, in its own pipeline** (`manager/internal/engine/audio`), like video: the I2S
  of the board's HDMI and each USB sound card in the catalogue (`probe -list` → `audio`), that is,
  capture cards, webcams, microphones and interfaces. They reach the program through one `appsrc` per slot
  (`aa0…aa3`, `MIXER_AUDIO_SLOTS`) towards the `audiomixer`, with a background silence that keeps the track.
  **Since Oct 15 the audio follows the video slots** ([`SOURCES.md` §8.3](SOURCES.md#83-detected-cameras-and-the-four-slots-the-audio-follows)):
  a camera's own sound card is opened only while that camera has a video slot (or a camera with a slot was given
  it by hand, `audio_link`); the I2S and a USB sound card that belongs to no camera, always. They take the audio
  slots in the order of their video slots, the loose cards last (one gives its slot up to a camera's); the audio on
  air is never closed by itself.
- **One plays.** The one on air goes at volume 1 and the rest muted but capturing, to switch
  instantly with a 250 ms fade. At startup `AUDIO_SOURCE` plays (auto = the main source's:
  I2S with HDMI, the USB's card). The choice is saved with the scene.
- **Isolated.** A card that is unplugged or stops only takes itself down (new cards are detected when
  `/proc/asound/cards` changes; since Oct 15 an unplugged one leaves its slot after 3 s and goes back to it when it
  comes again, SOURCES.md §8). I2S without a camera stays in `no_signal` and is not
  reopened: closing it takes ~5 s (trap 19).
- **Web**: "Audio" row in "Sources" (each input, its level and which one plays; "No audio"). The Audio
  card shows the one that plays. **Screen**: "Audio: <name>".
- **Measured on the board**: with the Logi's mic on air, the program level meter reads ~−49 dB
  (the room); with I2S without a camera, −90; switching from the web and through the socket, live.

For later: "the audio follows the video", mixing several, volume and delay per input, and choosing the
audio from the touch screen.

## 11. Composition and screen (Oct 12)

What in the plan was the composition F4, plus Go live and the lock on the touch screen, which were
needed for testing out on the street (G1).

- **Configurable PiP**: four corners and two sizes (25 % and 33 % of the width), with a 3 % margin.
- **Pause**: one more layer of the program (`appsrc mp` → `pgm.sink_<slots>`, on top of everything) with a
  still image and the user's text (by default "Be right back"). What is underneath stays the same:
  removing it brings it back as it was. With `mute`, the audio fades to silence while it lasts, without
  changing the chosen input. When off, the layer is small and invisible (trap 22). The text goes to
  `textoverlay` as a property, never inside the pipeline description.
- **Letterboxing**: a source that is not 16:9 (4:3 webcam, phone in portrait; SD over HDMI counts as 4:3)
  is fitted centred and uncropped, in the program and in the screen's thumbnails. In side by side,
  each source uses the full height of its half. The web's thumbnails are still stretched to 320×180.
- **Fades in every composition** (`fade_plan`/`fade_rects`): with a single pad per source, a
  layer cannot be in two places. What comes in does so fading over the top; what goes out and gets
  covered stays underneath, opaque, until the end (crossfade); what goes out without anything covering it
  (a PiP leaving, the Pause) goes out fading; and what leaves full screen for a smaller place
  (swap in PiP) acts as the base and then moves into its place, in a 0.6 s fade instead of 0.4.
  A change mid-fade starts from the scene the interrupted fade was going to, not from the picture half way:
  the layers still fading out go at once (a visible jump; §22).
- **Touch screen**: new row with Corner and Size (greyed out without PiP) and, at the bottom, **Go live** (it is
  held for 1 s, both to start and to stop streaming, with a bar that fills up), **Pause** and
  **Lock**. When locked, everything goes dark except the program and it only responds to "Unlock" held for
  1 s (it can also be unlocked from the web). The status also shows the transport and the links, the
  CPU temperature and the input voltage (amber below 4.8 V). The lock, the PiP and the
  Pause are saved with the scene and survive restarts.
- **Measured on the board**: 64 scene changes with fade in one minute (takes, swaps, corners
  and sizes, side by side and Pause) without a single VIC failure or a restart; afterwards, 59 new frames per
  second and CPU at ~30 %. With the Pause and `mute`, the program at −90 dB; on removing it, the mic comes back.

## 12. Timing: stutter while streaming and sync (Oct 12)

During a test at home, the operator saw that on starting to stream everything stuttered (above all the program on the
screen) and that the audio was not in sync with the video. Both things had the same cause (trap
23), with two aggravating factors: the CSI timestamps (24) and Python saturated with calls.

- **Capture timestamps** with `MIXER_SYNC` (120 ms by default; 0 goes back to the arrival timestamp): the video
  and the audio of each source enter in step, and an engine hiccup is not visible as long as it does not exceed the margin.
  It costs those 120 ms of delay in the program and on the screen.
- **Less Python**: when not streaming, the output branches are not read. Audio goes in chunks of 40
  ms instead of 10, in the capture and in the mixer. New frames are measured 2 s out of every 10. Each source
  also carries a queue that drops the old stuff behind the capture.
- **`tiempos` (timings) log line** every minute: minimum/average/maximum age of each source's buffers on arrival,
  the longest gap between arrivals, the program towards the screen and the longest time each
  engine task has taken.
- **Measured on the board**, streaming and alternating the sources on air: the program gives all the frames of
  the source on air (HDMI 30.5/30.5, Logi 59/59, USB capture card 30/30.5); before, it was 13 of 57. The output
  runs at 60/60 fps and 6 Mbit/s. Maximum arrival gaps of 50-78 ms (before 100-134); 0 errors in 5 min.
- **Pending**: the sources with unreliable timestamps (the Logi, the USB capture card) use the arrival one,
  so their video arrives late relative to their own audio by however long their capture takes (about 50-100 ms,
  not measured). It is fixed with the per-input audio delay (F4).

The hung Cam Link and Logi (USB `-71` in control transfers, even on their own) are down to the
device or its power supply, not the engine. Hot detection worked: on reconnecting them they were
reopened in their slot without touching the program, and a new USB capture card came in by itself. They have to be
reconnected by hand, preferably with a powered USB 3 hub.

## 13. F4b: per-input delay and "the audio follows the video" (Oct 12)

The rest of F4. By default nothing changes: one fixed input plays and without delay (D7).

- **Per-input delay** (`audio_delay`, 0-500 ms): it is added to that input's capture timestamp
  (§12). It is for lining up the sources whose video arrives later than their sound, especially the ones that
  have unreliable timestamps and use the arrival one (the Logi, the MS2109 capture card).
- **"The audio follows the video"** (`audio_follow`): when putting a source on air, from the web or from the
  screen, its linked audio plays, with the 250 ms fade. If it has none, whatever was playing continues. The
  audio can still be chosen by hand.
- **Which audio goes with each camera** (`audio_link`): by default, the one of its own device (the board's
  HDMI → I2S; the Cam Link → its HDMI audio; the Logi → its microphone). It can be changed, for example,
  the board's HDMI with a USB microphone. It is saved by source id, so it survives reconnecting it.
- The audio inputs come out **in the order of the cameras** of their own device, with their number
  (`videoSlot`: "1 · Board HDMI", "2 · Logi…"), and the ones that belong to no camera (a standalone
  mic), at the end. Before, they came out by their audio slot (I2S first and the USB ones as they were
  detected), and did not match the source list.
- Everything is saved with the scene (`audioSettings`). The screen says "Audio: X · follows video".
- **Tested on the board** through the socket: with `audio_follow`, each take changes the audio to its
  device's; a manual link (the HDMI with the Logi's mic) wins; out-of-range delays and
  inputs that do not exist are rejected.

For later: mixing several inputs and per-input volume.

## 14. The screen, like the plan's sketch (Oct 12)

The request: "make the screen match what you prepared in the mock-up"; plus a
Power menu to restart and shut down, and the level meters of the sources and the overall one.

- **Status bar** at the top: LIVE with the time streaming (or CONNECTING / NOT STREAMING), Mbps,
  transport and RTT, temperature and voltage (amber below 4.8 V), **Power** and **Lock** (since
  Oct 13, header with logo and icons and Settings: §18).
- **Program** large on the left: red frame when streaming, amber in Pause; label, source on air
  and, with PiP, where the small window is (the same calculation as the program).
- On the right, **Composition** (Full, PiP, Side by side, Pause) and **Transition** (Cut, Fade),
  as in the sketch. The PiP is handled from its button: with PiP on, a tap changes the corner and a
  long press, the size. Below, which audio is playing with the **program level meter** (L/R, from the
  `level` `pgmlevel` before the audio output) and, if there is a standalone microphone, its own. At the bottom,
  **Go live** (held for 1 s).
- **Thumbnails** with the name and the state centred over a band, red border when on air and green in the
  PiP, and the **level meter of the audio of its own device** (bright if it is the one playing).
- **Power menu**: Restart and Shut down are held for 2 s; Cancel or a tap outside closes it. The engine does not
  have permission: it publishes `screen.power {want, seq, at}` and the app, with the exact sudoers commands
  (`/bin/systemctl reboot` and `poweroff`), does it once per `seq` and only if the request is less than
  15 s old. The screen says "Restarting…" or "Shutting down…" until the splash comes in.
- Colours of the eirl theme in dark mode, larger font for 7" and touch targets of ≥ 44 px.

## 15. Audio mixing and its view on the screen (Oct 12)

The request: "a second view on the screen that lets us select the audio tracks and mix them";
and the overall level meter, vertical next to the preview, without saying which source is playing (now there is a mixer).

- **Two modes** (`audio_mode`): `single`, as until now (`main` plays, with "follows video"), and `mix`,
  where the marked inputs play at the same time (`audio_mix` with `on`). The mix starts with what was
  playing. **Per-input gain** (`gain`, −40…+12 dB, 0 = no change), in both modes. Each change
  goes from the current volume to the new one with a 250 ms fade (`targets`/`blend`): no clicks. The
  Pause with mute still switches everything off. It is saved with the scene.
- **Audio view on the screen**: entered through "Audio mixer ›" (where it used to say which source
  was playing) and left with "‹ Video". At the top, "Single source | Mix" and "Follows video". One strip per
  input (up to 4), in the order of the cameras, with its number: "Put on air"/"Playing" or "In the
  mix"/"Out", its level meter, a fader (tap wherever you want: coarse adjustment, without dragging, as
  the plan asks for use on the move), the gain and − / + in steps of 2 dB. And the "Program" strip with the
  L/R level meter.
- **Video view**: the program level meter, vertical between the preview and the buttons.
- **Measured on the board**: mix mode adds and removes inputs (only the Logi's mic: the program at
  −49 dB; with the capture card, at −14), and out-of-range gain is rejected.

## 16. F5: robustness and review (Oct 12-13)

> The go-ahead: "start around 2 a.m. and you can take 6 h"; "you can do F5 now".

**CPU, measured before touching anything (Oct 12, 23:00, three sources live, not streaming).** The engine
process uses **~155 % of one core** (out of 400 %), but **it is not Python**: 72 % is user time and
85 % system, spread over the GStreamer threads. What uses the most:
- **The board's HDMI, 31-36 %** (almost all system). It arrives in UYVY in system memory and
  `nvvidconv` copies it to NVMM with the CPU, 4 MB per frame. The review confirmed it: the thread's name
  changed when that source was reopened.
- The two MJPEG captures (`v4l2src` + `jpegparse` + the decoder input), 12-18 % each.
- The main Python thread (GLib loop, screen at 20 fps, state): 13 %.
- Each fixed branch of the program, measured separately with `gst-launch`: 3-4 points of one core. They are the
  cadence, the thumbnail, the WebRTC preview and the program picture for the screen.

Not touched tonight: the headroom is ample and the long test must measure the engine as it is. In
order of savings, for later:
1. `nvv4l2camerasrc` (it is installed) for the HDMI, which delivers NVMM directly: 25-35 points. The
   timestamps (`raw_offset`, `CaptureJudge`) and the behaviour without a signal need reviewing.
2. Valves before converting in the ≤1 fps branches (cadence, thumbnail, screen
   snapshot) and in the WebRTC preview without viewers: ~7-9 and ~6 points.
3. Screen: level meters in small layers or only when they change, without the `bytes()` copy per picture
   (5-10), and the multiview at 30 fps (3-4).
4. No `jpegparse` on the MJPEG ones (5-9 each) and `VIDIOC_QUERY_DV_TIMINGS` by ioctl instead of launching
   `v4l2-ctl` every 2 s (~1).

**Code review (Oct 12): 10 findings.** Fixed before the long test:
- **Socket**: a subscriber that does not read made its buffer grow without limit, because the cap was only
  checked when it could be written to. Now it is checked when broadcasting. The client list now only changes under the
  lock; before, `dictionary changed size during iteration` could pop up inside a timer.
- **Exception-proof GLib timers** (`guarded`). Before, PyGObject 3.26 removed them
  forever at the first failure: without `periodic` the state stopped refreshing and without `tick` the
  screen froze. Fades that fail end in their final state, instead of leaving the
  next one unable to start.
- **Mix**: when going back to "Mix" whatever was playing always goes in. Before, it could end up
  silent if that input had ever been taken out.
- **Output**:
  - the detail of an SRT error goes without libsrt's time (each line redrew the screen: dozens
    per second with a rejection);
  - with the connection made and 10 s without the encoder producing, the output re-hooks instead of
    still saying "LIVE".
- **Others**:
  - `LimitNOFILE=8192`: the engine already has ~730 descriptors when not streaming and the limit was 1024.
  - Audio queue of 1 s, so that the maximum delay of 500 ms fits.
  - `newFps` without the false 0.5 from outside the measurement window; the long test records it.
  - `pip` with a size that is not a number gives `bad_request` (before, `TypeError` and `timeout`).
  - A race when releasing the screen.

**Security review (Oct 12).** Nothing exploitable from the network. Power can only be triggered by the touch
screen: the socket has no power command, the app only acts on its subscription, once per `seq`
and with ≤ 15 s, and nobody else can fake touches. Fixed:
- **The engine had root in practice**: it runs as the board's login user, with passwordless sudo on the test
  board. The drop-in sets `NoNewPrivileges=yes` (it uses no sudo or anything setuid).
- **The hub, only for the engine**: its own group `eirl-usbhub`, which the web app does not have. The rule, only
  for the RTS5411 (`0bda:5411|0411`), without the root hubs or others.
- **Power that never gets done**: after 20 s the screen comes back and says "Could not…" (before
  it stayed dead on "Restarting…"). The app starts requests from scratch with each engine.
- Locking from the web closes the Power menu, and with the screen locked it does not restart or shut down.
- USB recovery only cuts with the source in `error` or `gone`: never one that does not fit in the bus.
- Hardening: `service.Redact` on the output's detail; `hw-reset.last` only with digits
  before the arithmetic expression; regex with `\Z`.

Remaining:
- Move `STATE_DIR` from `/tmp` to `StateDirectory`. Today it is not exploitable: there is no other local user.
- ~~`discover()` launches `probe -list` inside the GLib loop~~ (done Oct 13): the `probe -list` runs in
  a thread and what it finds is applied in the loop (`discovered`), one at a time. Before, with a USB that
  comes and goes, the screen froze for up to 10 s and commands gave `timeout`.

**Recovering a hung USB capture card** (`manager/internal/engine/usbhub`). The board's hub (Realtek
RTS5411: `1-2` USB 2 and `2-1` USB 3) declares **per-port power switching**
(`wHubCharacteristics` 0x00a9 and 0x0009, checked with `lsusb -v` and with the module itself). So the
engine does the same as `uhubctl`, without installing anything: ClearPortFeature(PORT_POWER) on the port and on
its twin on the other hub, 2 s, and SetPortFeature.
- When: a USB source that has had **no picture for 20 s and is still plugged in**. That it is still plugged in is
  said by sysfs, or by the hub's connection bit if it has already dropped off the bus. At most, one cut every 2 min and 3
  in a row; after 1 min live, the counters go back to zero. If it does not come back, its state says "hung:
  unplug it and plug it back in".
- What it never cuts: for a device without a video interface (the touch screen, on `1-2.3`), nor on a
  hub that does not declare per-port switching.
- Without root: the capture cards' udev rule gives the group `eirl-usbhub` (only the engine's service)
  write access to the hub's `/dev/bus/usb` nodes, and `install.sh` applies it to the ones already there.
- The former Python engine had a command line for it: `port <node>` said what it would cut and what else would
  switch off; `cycle <node>` did it. `usbhub.Main` keeps that command line in Go. As the login user, without the group,
  it answers "its hub does not switch": sudo is needed.

**Tested on the board (Oct 13, 02:06, not streaming; `mixer/f5/usbtest.sh`).** The MS2109 is held by another
process (`v4l2-ctl --stream-mmap`) so that the engine cannot open it. After 20 s the engine cuts and it
recovers by itself: live 7 s after the cut. The process that held it loses the device and dies. But
**the cut switches off the whole hub**, even though the descriptor says "per port": the touch screen (`1-2.3`)
drops after a quarter of a second and the Logi (`2-1.4`, which was on air) after 0.6 s. Both re-enumerate
by themselves: the touch screen after ~2 s (the engine opens it again) and the Logi live after ~5 s. The
screen rebuilds itself. In other words: VBUS really is cut (it cures a `-71`), but on all the
connectors at once. Therefore:
- `Plan.affected` counts as switched off **everything hanging from the hub and its twin**, on any hub
  (the descriptors lie). The "twin with something else" rule is removed: the cut switches off the touch screen
  anyway, and it comes back by itself.
- **While streaming, it does not cut if it would switch off something on air**: the video being shown (the main and, in
  PiP or side by side, the secondary) or a USB audio input that is playing. The source waits, without using up
  attempts, and says "hung: restarting it would switch off … (on air)". As soon as that is taken off air or
  streaming stops, it cuts. Not streaming, it always cuts. If the engine does not know what is on air, it does not cut.
- On the board all the USB devices hang from the same hub. Streaming with a USB on air, the automatic
  recovery only acts if another source that is not on that hub is put on air: the HDMI or, with F7, a network one.
- That message and the "unplug it" one are no longer overwritten by the error of each retry.
- Review of these changes (Oct 13, 02:30), fixed:
  - **One turn per hub** (a claim with a lock). If another source cut it less than
    20 s ago, it is not cut again: that cut also restarted it, and the count starts there. Without this,
    two hung ones on the same hub cut it one after the other.
  - A USB on air that gave a picture less than 30 s ago counts as on air, even if it is being reopened.
  - Only the time in `error` or `gone` counts as hung. Before, after 5 min in "does not fit in the bus",
    the first failure cut on the spot.
  - The device is known even if the source has never been live.
  - `recover()` runs inside a `try`: a failure there does not leave the slot dead.
  - Unplugged or hidden, the wait is forgotten.
  - The `cycle` CLI does not cut if the engine is streaming (or does not answer), except with `--force`. It accepts the
    sysfs name (`1-2.1`) of a device that has already dropped off the bus.
  - `deploy-engine.sh` and the night harness checked the output again right before restarting. Copying the
    unit and the `daemon-reload` take a few seconds: if someone is streaming, they leave it as it was. They required
    the engine's startup line in its journal (empty is not "clean"). The night harness stopped deleting the
    USB module it had just restored when rolling back.
  - `newFps` knows whether the measurement window was open from the tick, not from how many frames arrive.
    With the program stuck it kept the last good value.
- `mixer/f5/usbtest-hold.sh` tests the wait on the board: Logi on air while streaming → does not cut; HDMI on
  air → cuts. It leaves the scene and the audio as they were even if it is cut halfway (`trap`), and it is launched
  with `systemd-run` so that it does not depend on ssh.

**False alarms during the night (Oct 13), fixed:**
- The night harness (since removed) took the traceback of the **old engine stopping** for a failure of the new engine: it already had the
  new files on disk, and the traceback shows the line of the new file. It was the `mv_pgm` race
  that F5 fixes. It rolled back and streamed with the old engine. Now it only looks at the journal of the new
  run (`_SYSTEMD_INVOCATION_ID`) and requires that it has not restarted by itself. It was relaunched at 02:05.
- The "solo N frames nuevos/s" (only N new frames/s) warning always fired at startup: the measurement window started before
  the source went live. Now it only counts if it was already on air and live when the window started.
- Seen in the old engine (eea0ee7), once: `fallo interno: AttributeError("type object
  'MessageType' has no attribute 'ERROR'")` (internal failure) in the MS2109's audio at startup. It recovers in
  1.5 s. It is PyGObject 3.26's lazy loading of enums (`gi/module.py`: it publishes the class at
  line 146 and sets its values at 168), with several source threads starting at once.
  The former launcher touched them all after `Gst.init`, before starting any thread. It has not been
  reproduced on purpose: it shows up once in many starts.

**Seen during cloud testing (Oct 13, Go live/Stop streaming from the app with SRTLA to dev), fixed:**
- With the relay down, the SRTLA output said `no_uplinks` ("You have no internet connection")
  with eth0 in use. When `srtla_send` gave up, that was the default reason, and nothing it printed
  was read. Now `classify_srtla` reads its last lines:
  - "Failed to establish/re-establish any connections" or "Failed to resolve" → `relay_unreachable`.
  - Not being able to open or bind the links' IPs → `no_uplinks`.
  - If it says nothing → `connection_lost`. Without links it never gets launched (`start_srtla` waits).
- Stopping during the wait for a retry left `{state: error}` with the output off. Now it stays
  `off`.
- With SRTLA and the relay down, the app alternated "Connecting…" and "No connection" every few seconds:
  `srtla_send` gives up after ~11 s, and `srt-live-transmit` does not. New rule, agreed with the cloud
  (ENCODER_CLOUD §13.3, `retry_plan`):
  - **Transient reasons**, which the output retries by itself: `relay_unreachable`, `connection_lost` and
    `no_uplinks`. During the wait it stays in `connecting` with the reason, with waits of 2, 4, 8 and 10 s.
    When the relay comes back it hooks up in seconds, not in 30. The screen says "CONNECTING · the relay is not
    responding".
  - `error`, with the 2 to 30 s ladder, only for what needs an action or is not fixed by
    retrying: `rejected`, `no_destination`, `encoder_error` and `internal`.
  - After a session of more than 1 min, the failure counts as the first one. Before it counted as number 0, which
    gave the longest wait (30 s).

**Long test**: `mixer/f5/soak-night.py` (a sample every 30 s and a scene change every 20 s; every 15
min, 2 min of audio mixing) and `soak-summary.py`.

**Result (Oct 13, 02:07-07:44: 5 h 37 min streaming to eu1; `~/f5-soak/20261013-0207`).** It ran
the F5 engine from 00:15, without the 02:30 fixes. There were two USB sources live all night: the Logi at
60 fps and the MS2109 at 30 fps. The CSI HDMI had no signal.
- The output gave "on" in all 670 samples, without re-hooks: a single process, 0 restarts and 0 tracebacks.
- **CPU**: 153 % of one core (148-159).
- **Memory**: the RSS goes from 133 to 138.5 MB. It rises in the first hour and from 03:00 stays flat at
  ~137 MB: it is not a leak. Threads (104), descriptors (~779) and free memory, flat.
- **Picture**: the program at 60.0 fps (minimum 55). New frames: 53 on average, 30 with the MS2109 on air.
- **Network**: average send of 6513 kbps, RTT of 58 ms. At 03:18 the relay counted 0.041 % of packets
  lost in the network, all retransmitted: none unrecoverable.
  - The final summary does not include the relay: the connection disappears from its API when streaming stops. Fixed:
    the night harness recorded it beforehand (`soak-summary.py --relay`).
- **Low send rate**: 9 of 670 samples below 4000 kbps (minimum 449), all with the MS2109 on air and
  without losses or RTT change. It is below the adaptive bitrate's floor (1000 kbps), so
  ABR is not what lowers it. Not investigated.
- 1041 scene and audio changes, 0 with an error.
- 50 °C, 5.13 V at the 5 V input and 6.1 W.
- Kernel: 0 `-71` errors, 0 "OC ALARM" and 0 USB disconnections.

**Installed with the 02:30 fixes and tested (Oct 13, 09:31, `usbtest-hold.sh`).** Streaming to eu1
with the Logi on air and the MS2109 held by another process:
- After 20 s, the MS2109 says "hung: restarting it would switch off Logi 4K Stream Edition (on air)". The engine
  does not cut, and the touch screen and the Logi carry on.
- When the HDMI is put on air, it cuts in ~3 s. The touch screen and the Logi drop for a few seconds, and after ~10 s
  the capture card, the Logi and the touch screen are live again. The multiview rebuilds itself.
- The test leaves the scene and the audio as they were, and stops streaming. 0 tracebacks and 0 restarts.
- After the cut the numbering of `/dev/video*` changes: the engine goes by by-id and by-path.

## 17. CPU savings (Oct 13, in progress)

> Go-ahead: "Let's do it". Starting point: the engine uses ~153 % of one core (long test of Oct 13, §16), and
> whatever is saved is headroom for the network cameras (F7). Branch `perf/cpu`.

Three steps, from least to most risky. Each one is measured on the board with the same method (CPU per thread) and at
the end a long test is done while streaming.

Meters (read-only; copied to the board, `tools/engine-cpu.sh` together with `tools/thrcpu.py`):
- `engine-cpu.sh [s]`: CPU of the engine per thread and of the system for N s (60 by default), with the state
  of the sources, the preview and the program's new frames/s. They are the tables in this section. 🪤 Until
  Oct 13 at 17:00, its "system" did not count IRQs or softirq (~20 points on the board: host1x and
  USB 3, on CPU0; spotted during the base-system work, BASE.md). The "System" column of the tables in this section
  was measured that way: it is valid for comparing among themselves, not as a total. `engine-watch.py` always counted them.
- `engine-watch.py [min]`: every 30 s, the engine's RAM with its 5 min slope, the CPU, the output, what
  is on air, the new frames/s and each source's fps. At the end it adds from the journal the engine's
  "tiempos" (timings) (drawing, program→screen) and its warnings. It writes to `~/eirl-watch-2d/`. It is
  launched with `setsid nohup` so that it does not depend on the session.

**Measured on the board (Oct 13, 10:45-11:15)**, with steps 1 and 3 and the F7 engine on top. The
conditions were those of the long test: two USB sources live (Logi at 60 and MS2109 at 30), the CSI HDMI
and the network camera without a signal, and the screen on. CPU in % of one core, 60 s average:

| | Engine | System (4 cores) |
|---|---|---|
| Before (F7 engine from 09:48), not streaming | 130 | 164 |
| Step 1 + level meters, preview on | 108 | 128 |
| The same with the preview stopped (nobody watching) | 93 | 108 |
| And without `jpegparse`, preview on | 90 | 124 |
| And without `jpegparse`, preview stopped | **75** | **88** |
| Streaming to eu1, preview on (before: 153 in the long test) | **124** | 160 |

- The USB captures go from 17 and 12 % to ~6 % each without `jpegparse`. The Python thread, from 13 to 10 %.
- Stopping the preview saves 15 points, twice the estimate: the rescaling and its queues are also
  saved. When it is switched back on, the first packet goes out after 0.04 s and the first IDR after 0.36 s.
- With all of it, the screen, the thumbnails, the snapshot and the new-frames measurement keep
  working. 10 min without `jpegparse` without a single decoder error.

**With the board's HDMI with a signal (Oct 13, 13:51-14:00)**: 1080p30 from the test HDMI source, the two USB
sources live, the network camera without a sender and the screen on, not streaming. Preview on in both
cases: the installed app does not stop it yet.

| | Engine | System (4 cores) |
|---|---|---|
| Before (F7 engine from 11:17, `v4l2src`) | 162 | 196 |
| Steps 1, 2 and 3 (+ F7) | **96** | **108** |

The thread that copied the HDMI (`queue23:src`, 32.5 %) disappears. Its two extra frames per second
("newFps" 58.5, before 27.7) are not a failure: with step 1, the cadence no longer drops frames before counting.

**1. Not converting frames that are going to be dropped** (done on the branch):
- The cloud thumbnail and the screen snapshot: first 1 per second
  (`videorate max-rate=1` in NVMM) and then convert. Before, all 60 per second were converted and 59 dropped.
- The screen's multiview, at 30 per second (`MV_FPS`). The program picture it carries
  (`pgmtap`) too, and each source sends it one frame every `MV_GAP` (25 ms) at most: from a 60 one
  one in two passes, and from a 30 one, all of them.
- The new frames (`cadence`) are counted 2 s out of every 10. Now a valve (`cadvalve`) drops the other 8 before
  converting them.
- The HDMI mode is read with `VIDIOC_QUERY_DV_TIMINGS` from Python (`query_dv_ioctl`), without launching
  `v4l2-ctl` every 1-2 s. If the driver does not accept it, it falls back to `v4l2-ctl`. Tested on the board without
  a signal: it gives the same as `v4l2-ctl` ("Link has been severed"). With a signal, still to be tested.

**3. What works while nobody is watching** (done on the branch):
- **Screen level meters**: only the base under each level meter is restored, not the whole picture. If no
  level meter moves a pixel, there is no new picture. Checked on the board: 0 pixels different from
  repainting everything. It goes from 3.6 to 2.4 ms of CPU per picture (20 per second), and in silence to 0.
  The `Gst.Buffer.new_wrapped` copy remains: 1.1 ms per 1024×600 picture.
- **WebRTC preview**: valves `livevalve` and `liveavalve` before rescaling and before Opus. The app
  (`FollowPreview`) sends `preview` with `on: false` if nobody has been watching for 15 s, and `on: true` as soon
  as someone arrives. An engine without the command leaves it on, as before. The app's part is not installed
  yet: on the board the command was tested by hand through the socket.
- **USB MJPEG without `jpegparse`**: `v4l2src` already gives a whole JPEG per buffer. It is reopened with it if
  the error comes from the decoder or if the camera goes two openings in a row without a frame (there are webcams
  that send the JPEG without an end marker). A single opening is not enough: the MS2109 sometimes
  gives no picture on the first one after restarting the engine, also with `jpegparse` (Oct 12 21:41, Oct 13
  08:22 and 11:11). The single-source mode (`MIXER=0`) still uses it.

**2. Board HDMI with `nvv4l2camerasrc`** (done on the branch, Oct 13): it delivers the UYVY already in NVMM. It
removes the 4 MB per frame copy that `nvvidconv` did with the CPU from `v4l2src`. By default
`MIXER_HDMI_CAPTURE=nvcam`; with `v4l2` (in `/etc/default/eirl-engine`), or if the element does not exist, it
falls back to `v4l2src`. Tested on the board, first separately and then in the engine:
- **CPU**: in the separate test, at 1080p30, the capture goes from 21.4 to 1.2 % of one core and the system from
  32.5 to 10.1. In the engine, the table above.
- **Timestamps**: `nvv4l2camerasrc` stamps each frame with the ARRIVAL time. With `do-timestamp=false` it does not
  stamp it. The driver timestamp that `v4l2src` carries is always 2 frames ahead of the arrival (66.8 ms at
  30 fps, from 66.7 to 68.1), so the engine subtracts 2 frames from it (`csi_latency`) and the capture lands where
  it did before. In the engine, the minimum age is 69 ms (with `v4l2src`, 76, which included the copy). With the normal
  clock `raw_offset` is not needed. Still to be checked at 50/60 fps: the test source only gave 30, not even
  with the 720p60 EDID.
- **It does not look at the caps' framerate; it does look at the size.** With a size that is not the signal's it keeps
  waiting without an error, and `set_state(NULL)` never returns (the process had to be killed). The
  device recovers when the process dies. That is why, on close (`Source.close`), it waits 2 s and, if
  it has not returned, does `VIDIOC_STREAMOFF` on the descriptor the process itself has open
  (`streamoff`). Tested: it unblocks it in 2.0 s. If even that did not bring it back, the slot stays in "error" until
  the engine restarts and is then reopened with `v4l2src`.
- **No signal** (EDID erased with the capture open: the source sees the hotplug go down): it gives "Internal data
  stream error" and EOS, like `v4l2src`, and closes in 0.05 s. In the engine, "NO SIGNAL" and, when the
  EDID comes back, "live" with valid timestamps. A hotplug with the 720p60 EDID behaves the same.
- On the closes after an error, `nvbuf_utils: dmabuf_fd 0 mapped entry NOT found` on stderr. No effect.
- 🪤 With `fakesink` instead of the engine's `appsink`, `nvv4l2camerasrc` stops delivering frames after a few
  (3 in 8 s), with the same message. With the engine's queue (`sink_tail`), it does not happen. Cause unknown: it has
  to be kept in mind in standalone tests.
- 🪤 After the last test, the test HDMI source stopped sending a signal (cable with +5 V and hotplug, but
  no TMDS), and a hotplug pulse did not bring it back. It may be unrelated: still to be checked on site with that source.
- Still missing: the long test while streaming and measuring A/V sync with `avsync_ball.py`.

## 18. Header with logo, icons and Settings (Oct 13)

Requested: the logo at the top left and, separately, the streaming status; Wi-Fi, Power and Lock as icons;
the top and bottom margins the same as the side ones; space between the header buttons and its line;
and the yellow touch dot, "prettier or gone". Of the two proposals (antenna in the header, or a
gear grouping the Wi-Fi and the network cameras switch of NETWORK_SOURCES.md §6.12) the
**gear** was chosen, and for the touch, **highlight what is pressed**.

- **Header** (`Layout`): icon buttons of `hb = max(44, height/13)` with the margin `m` above **and below**
  (the header measures `hb + 2m`; before, buttons 2 px from the edge and stuck to the line). From left to right:
  the logo (`scripts/eirl-logo.png`, the boot screen's one scaled down; if missing, "EnhancedIRL" as text),
  a thin rule, the dot and the streaming status, the network in the remaining space, temperature and voltage, and
  lock · ⚙ Settings · ⏻ Power (feedback after seeing it installed: Power at the far right). The lock is shown open;
  locked, closed and in amber, and it is held to unlock.
- **The header's network** is measured in the free space between the status and the board readings; if it does not fit, whole parts
  are removed (first the RTT, then the link), not half a word. Before, fixed width and cut off when streaming over SRTLA.
- **The connections' dots** (Oct 14, [`MODEMS.md`](MODEMS.md) §6): with the app's `links.json` fresh, the middle has two
  lines: a dot per internet connection (green, amber, red; a grey ring when switched off) with its short name (the
  modem's operator or alias, else Cable / Wi-Fi / Modem / USB), and below, smaller, the network ("SRTLA" without the
  count: the dots say it) with the temperature and voltage, which leave the right side to make room. If the names do
  not fit: a smaller size, then the modem's technology ("5G", "4G"), then dots only. The client Wi-Fi's antenna becomes
  its dot. The whole area is a button to Settings › Streaming › Connections (not while locked). Without the file, or
  with one older than 15 s (the app is not running), the header is as before. The engine reads the file at each
  drawing only if it changed (`appapi.Links`, validated: the app runs as another user).
- **Margins**: the program (16:9, at most 5/8 of the width) and the thumbnails share the height the
  header leaves with `m` also at the bottom; the thumbnails take what is left over (their video is letterboxed, `fit_rect`).
  At 1024×600: header 70, program 640×360, thumbnails 240×134, 12 px on all four edges.
- **Icons** in Cairo strokes (`draw_icon`: wifi, power, lock/unlock, gear, close), without fonts, drawn once
  per type, size and colour and then pasted at whole pixels (`Screen.icon`). The logo is read and scaled **once**
  per height (`Screen.logo`) and, in RGBA, with R and B already swapped (as `rgba_context` does with colours):
  `draw()` runs 1-10 times per second and while streaming takes 0.4-0.9 s.
- **Cost measured on the board** (Oct 13 18:02-18:10, `draw()` in /tmp with the real `mixer.json`, RGBA, with the engine
  running; median of 57 rounds, two alternating batches): main 14.1-17.8 ms not streaming and 14.6-16.5 streaming;
  the new header 14.9-15.5 and 14.6-14.8: **the same as main** within the noise. Without the icon cache, ~0.5 ms
  more while streaming. With Settings open, ~40 ms (darkening the whole screen and the panel, like the old Wi-Fi panel and
  Power menu; painting the ready-made QR did not bring it down: it stayed module by module); with a panel open it is drawn
  once per second, without level meters.
- **Touch**: the yellow dot is gone. The button, the thumbnail or the program under the finger lighten while
  touched and at least `PRESS_MIN` (0.15 s) after release (a quick one did not get drawn); when locked, only the
  lock. It is redrawn on press and on release, not 10 times per second for 0.6 s like the dot.
- **Settings** (the gear, always visible): a panel with **Network cameras**. The whole row is the
  switch of the RTMP/SRT **reception** (not of the Wi-Fi); below, with the Wi-Fi running, the QR, the network and the
  password (what the Wi-Fi button used to open) and each camera; with the reception off, the Wi-Fi stays and a notice
  says so. It is closed with the X or by tapping outside; it closes by itself on locking. With no cameras registered, it says so.
  An amber dot on the gear if the cameras' Wi-Fi should be on and is not. **Since §19**, with
  two tabs (Wi-Fi | Network cameras) and the board's Wi-Fi separate from the cameras.
- **Where the data comes from** (decided Oct 13): from the **app's local API** over its Unix socket (`/run/eirl-encoder/api.sock`,
  HTTP/1.1 with the web's routes, JSON and errors; MANAGER.md "local socket"), with its own worker
  (`manager/internal/engine/appapi`) that never blocks the engine's loop and never logs a request or its body. Details
  in NETWORK_SOURCES.md §6.4. Without the socket (an older app), the Wi-Fi from `wifi.json` and no switch.
- **`screen.jpg` without secrets**: the 1 fps snapshot carries the interface layer and the web sees it. With Settings
  open (Wi-Fi password and QR) and for `SECRET_GRACE` (2 s) after closing it, it is not updated: the previous one
  stays. Before, the Wi-Fi panel showed up in it (only the local web with a session, which can already see that password; it does not
  go to the cloud). The same will apply to the client Wi-Fi keyboard.
- **Audio view** (requested the same afternoon): going back to the video one is a button with only the arrow (←); "Single
  source" and "Mix" are separated (`m`); "Follows video" only exists with "Single source" (in the mix it does not apply:
  it is neither shown nor takes touches).
- **Previews** (in the former Python engine's `ui_render` tool; today `manager/cmd/eirl-ui-render`): it gained `settings`, `settingsoff`, `settingscable`, `settingsnone`,
  `settingsbusy`, `settingsonair`, `settingsold`, `settingsload` (with the app's fake state) and the
  modifiers `+press` / `+presstile`.

## 19. Wi-Fi in Settings: "Create my Wi-Fi", networks, keyboard and antenna (Oct 13, phase W-C of WIFI.md)

The screen part of [`WIFI.md`](WIFI.md) (§6 and §12): the app sets up the board's Wi-Fi (W-A, W-B) and the screen
handles it through the app's local API, like the cameras' reception (§18). Everything with data from the app; the engine
never calls NetworkManager or `eirl-netsetup`.

- **Settings, with two tabs** in the title (chosen over "everything visible", comparing the PNGs):
  - **Wi-Fi**: "Connect to a Wi-Fi" (only if the app has the client: `client` in `GET /network/wifi`; otherwise, the
    row does not appear and the one below moves up) with "Networks ›" inside the row; "Create my Wi-Fi" (`PUT /network/wifi {"ap":
    "always"|"rescue"}`); and under them the QR of the board's Wi-Fi, the network and the password (before, in the cameras:
    now the Wi-Fi belongs to the board, whether there are cameras or not). Without the Wi-Fi up, its place says why (off,
    turning on, error). Since Oct 14, "Rename" under the QR (with the AP off, next to its name) opens the keyboard on the
    AP's name and sends it with `PUT /network/ap {"ssid"}` (WIFI.md §14); the page's notes wrap instead of being cut.
  - **Network cameras**: the reception switch (§18), each camera with its state and how they connect; it warns
    if they come in over the Wi-Fi and "Create my Wi-Fi" is off.
  - Each whole row is its switch (the app requests it; meanwhile, "Turning on…"/"Turning off…" and the switch
    halfway). A `202` or an error show the app's `message` as is (`409 on_air` when turning off the Wi-Fi with one of its
    cameras on air; `409 uplink_in_use` when turning off the client or changing network if it carries the stream). The last
    tab is remembered. The amber dot (gear and Wi-Fi tab): "Create my Wi-Fi" requested and down.
- **Network list** (on tapping "Networks ›"; only requested while open: `GET /network/wifi/scan` every 2.5 s, the app answers
  at once with the latest, and `503 scan_busy` is "Searching for networks…"): inside the panel, with ← and X, in the
  app's order (the connected one, the known ones, by signal), 6 per page. Each row: the signal (1-3 arcs), the name, the security
  and the band, and its state (connected, connecting, wrong password, saved). Open or known: connects right away (the
  saved profile); with a password: the keyboard; WEP, WPA3 and Enterprise: greyed out, "not supported". "Forget" on the
  known ones, with a second tap within 3 s (`CONFIRM_FOR`). The network is fixed when the finger goes down (`_pick`): the list
  reorders itself and on release it could be a different one.
- **Keyboard** (`KB_LAYERS`): full-screen QWERTY, with layers for lower case, upper case (⇧, sticky until it is
  tapped again), `123` and `#+=`: between the four, all of printable ASCII (a test checks it). At 1024×600, keys of
  92×101 px. The field: **dots only**, the cursor and the counter `12/63`; "Connect" is enabled at 8. Holding delete
  clears it. With `wrong_password` for that network (NetworkManager takes ~25 s), the keyboard opens again empty with
  "Wrong password" if Settings is still open (`watch_connect`: only an error seen after "connecting" counts; the
  one from the previous attempt stays in the state until the next one). Tapping a known network whose password has just failed
  opens the keyboard instead of retrying with the saved one.
- **The typed password** (requirements of WIFI.md §6): it lives in `Screen.kb['chars']` and nowhere else on the
  screen. On connecting it goes to `NetState.connect`, which keeps it apart from what the screen reads (`snapshot` does not
  carry it) until sending it in the body of `POST /network/wifi/connect`, and lets it go. It never goes through the engine's socket,
  nor its state, nor the journal, nor an error (`ApiError` only carries the code and the app's `message`). It is
  cleared on connecting, on cancelling, on closing Settings and on locking (locking closes Settings). Python does not allow
  erasing it from memory with certainty: it is released and not written anywhere. The keyboard is **inside Settings**,
  so `screen.jpg` stays still while it is open (§18). A test draws the real keyboard and checks that
  no painted text contains what was typed.
- **Header antenna** (decided after comparing the PNGs): to the left of "°C · V", it is not a button and it only belongs to the
  client (what matters while streaming: it is an SRTLA link). Connected: the arcs of its signal (`signal_level`: ≥67 %
  three, ≥34 % two), in amber if it has no internet; on without a connection: grey; error: amber; client off or no
  `wlan0`: nothing. The AP does not show here (the gear's amber dot covers it). To have it with Settings closed,
  `NetState` requests `GET /network/wifi` also while closed, every 15 s (8-12 ms in the app).
- **`NetState`**: one change at a time, whether a PUT (`PUTS`: reception, "Create my Wi-Fi", client) or connect or forget
  (`POSTS`); `pending = (what, value)`. What `PUT /network/wifi` and `connect` return (the same as `GET
  /network/wifi`) goes straight to the client and to the state's `accessPoint`. A `403`/`404` on `/network/wifi` (an older
  app) removes the client row and it is not asked again until Settings is reopened. What arrives from the app is
  validated (`client_state`, `scan_list`: SSID of 1-32 bytes without control characters, at most 40 networks).
- **Cost** (board, `draw()` in /tmp with the engine running, median of 40): normal screen 17 ms; Settings/Wi-Fi 43-47 ms
  and Settings/Cameras 43-48 (before, Settings ~40 ms); network list 51 ms; **keyboard 9.5 ms**: it covers the whole screen,
  so what is underneath is not drawn, and its base (background and keys) is painted once per layer and pasted (`_kb_cache`, an idea
  from the mixer work): on each draw only the pressed key, "Connect", the title and the field. With a panel
  open it is drawn once per second and on every touch.
- **Previews** (former `ui_render` tool): it gained `settingsapoff`, `settingsapbusy`, `settingswrong`, `settingsnoinet`,
  the list (`settingsnets`, `settingsnets2`, `settingsnetsbusy`, `settingsnetswrong`, `settingsnetsforget`), the
  keyboard (`settingskb`, `settingskbupper`, `settingskbsym`, `settingskbsym2`, `settingskbwrong`) and the
  modifiers `+noclient`, `+camstab` and `+ant3`/`+ant1`/`+antnoinet`/`+antsearch`/`+anterror` (antenna). All with
  fake data.

## 20. Next

- **F4 · Audio**: done (§10, §13, §15). What remains, if needed, is giving the sources custom names.
- **F5 · Robustness and review:** §16. **CPU savings:** §17.
- **F6 · Cloud:** scene and sources from the app (ENCODER_CLOUD.md §13.2).
- **F7 · Network sources:** the encoder as an RTMP/SRT receiver for wireless cameras (local
  MediaMTX + NVDEC). In main on Oct 13 (MR !27): [`NETWORK_SOURCES.md`](NETWORK_SOURCES.md).
- **Settings parity:** everything the web configures, on the screen (Oct 14): §21.
- **Fades and the screen's tiles** (Oct 14): §22. Open: a change mid-fade still jumps (§22.3).
- **Lining the inputs up to the slowest one** (optional; plan, Oct 24): [`INPUT_SYNC.md`](INPUT_SYNC.md).

## 21. Settings: everything the web configures, on the screen (Oct 14)

Decided by the owner on Oct 14: **every setting of the local web can be changed from the touch screen**, from
Settings or from the audio view; **secret values show as dots, with an eye button to reveal them**. Before, the
screen had the Wi-Fi, the cameras' reception, the source slots, the language, the time, the updates and About; the
destination, the uplinks, the input, the cameras' management, the AP's channel and password, the pause card, the
audio delays and links, the EnhancedIRL link and the web password were only on the web.

### 21.1 Navigation: seven groups with tabs

At 1024×600 the list on the left takes seven entries a finger can hit (≥ 48 px), and there are fourteen pages now.
Chosen over a list with pages (▲▼): **seven groups**, and a group of several pages shows them as **tabs** in the page's
title row (`SettingsGroups`, settings_layout.go; a list entry is `s_page_<group>`, a tab `s_tab_<page>`):

| Group | Tabs (pages) |
|---|---|
| Stream | Destination (`dest`) · Uplinks (`uplinks`) · Pause (`pause`) |
| Sources | Slots (`sources`) · Input (`input`) · Image (`camera`) |
| Wireless (since Oct 15; Wi-Fi before) | Wi-Fi (`wifi`) · Bluetooth (`bluetooth`, [`BLUETOOTH.md`](BLUETOOTH.md) §3.6). The group's id stays `wifi` |
| Network cameras | — (`cams`) |
| EnhancedIRL | — (`cloud`) |
| Device | Language (`language`) · Date & time (`time`) · Name & password (`account`) |
| System | Updates (`updates`) · About (`about`) |

A group's entry goes back to the tab last seen. The dots (Wi-Fi down, an update or a restart to finish one, the setup
pending) show on the group's entry and on the tab. The Updates page also shows the system layer (UPDATES.md §12): a
restart that finishes the update (action `update_reboot` → `POST /update/reboot`, two taps, refused while streaming),
a download that waits for Wi-Fi or a cable, and a system part that failed or was skipped. The wizard keeps using the pages by id, without tabs.

### 21.2 How a page is made

- **One file per page**, registered with `registerPage(id, &pageDef{layout, draw, title, keys, touch, watch, leave})`
  (pagedef.go): its buttons' rectangles, its drawing, the buttons on view, its taps, what it needs from the app and
  what it drops when it goes. The first seven pages keep their wiring in ui.go/settings.go.
- **Shared pieces** (widgets.go): a field (title above, value below, › when a tap opens something), a secret's value
  (dots, or the text with its eye open), a stepper (− value +, the unit in the title), the generic picker (a choice
  in the page area, with pages; the option under the finger when it went down), `confirmTap` (a second tap within
  `ConfirmFor` for what cuts or deletes) and a page's own notice.
- **Texts by area**: each area's table in its own file (`strings_<area>.go`: stream, input, net, cloud, account,
  audio, camera) reached as `Tx.<Area>`; the completeness and verbs tests walk into them.
- **The keyboard** takes a spec (`kbSpec`: title, help, secret, max, min, starting layer, allowed keys, what OK and X
  do). Every secret keyboard (the Wi-Fi password, the web password, the stream key…) has an **eye** (`k_eye`) that
  shows what is typed in monospaced type; closed, only dots, as before.
- **The app's data and actions**: the device client (appapi) asks for `GET /destination`, `GET /status` (uplinks and
  the saved input) and `GET /sources` only while their page is on the screen (`AppWatch.Dest|Status|Sources`, every
  2 s, 2 s and 10 s), and runs this section's actions through the same queue as the device's
  (`ui.AppActions`: `dest_save`, `dest_reveal`, `restart`, `uplink`, `input_save`, `sources_scan`, `cloud_unlink`,
  `password`, `cam_add|rename|rotate|remove`, `cam_creds`, `ap_enabled|channel|rotate`, `lan_enabled`,
  `modem_save|pin|reset|subnet`, and since Oct 15 Bluetooth's `bt_power`, `bt_scan`, `bt_visible`, `bt_device` (the order
  in the action's `Value`: `POST /bluetooth/devices/{address}/{action}`) and `bt_use`, with `AppWatch.Bluetooth`
  (`GET /bluetooth` every 2 s, 1 s while searching, visible or with an order running);
  appapi/parity.go), each with its route's `{id}`, its JSON body and its **secret fields**, which `Do` copies, the
  body is built from without turning them into a string, and are wiped once sent. The app's side (the socket's
  whitelist and its two local-only routes): SETUP.md §8, MANAGER.md §3.
- The engine's own settings (the pause card, a source hidden, an audio delay or link, a camera's image settings) go
  straight to the engine as commands, like the mixer's; a refusal shows on the page (origins `page`, `audioset` and
  `camera`). A page's long press reads how long the finger was down (`UI.tapHeld`): `pageDef.touch` gets no duration.

### 21.3 Secrets

- **Shown only behind an eye.** The saved stream key and passphrase come from `GET /destination/secrets` (local
  socket only; the web never sees them), a camera's URLs from `GET /network/credentials?cameras=1`; both are asked
  for when an eye is opened, and dropped (`DropSecrets`) when their page goes. The board Wi-Fi's password, drawn in
  clear next to its QR before, is now dots with an eye (the QR still carries it: it is how cameras and phones join).
- **Typed ones** live in the keyboard's buffer, then in the page's draft (the destination's key and passphrase until
  the app saves them, so a refused save does not make the user type a long key again; the web passwords between
  their keyboards) until they are saved or sent, cancelled, the page goes or the screen locks; then they are wiped.
  They never go to a log, a command, the state or a drawn text with the eye closed.
- **A reveal does not outlive its page**: the client keeps a generation of the secrets that `DropSecrets` moves on; a
  reveal queued before it is not kept when it answers. A new key, or a camera added or removed, drops the cameras'
  URLs too (a reveal in flight would bring the old ones), and every eye opened asks for them again.
- `screen.jpg` is not updated while Settings is open (§18), so a revealed secret does not reach the web's snapshot.

### 21.4 The pages

- **Destination**: the URL (SRT/SRTLA, its end shown when long), the key and the passphrase (secret keyboards; empty
  removes them), latency, audio, video and minimum video (steppers inside the app's bounds; the minimum only with
  adaptive bitrate and never above the video), adaptive bitrate. A draft until «Save» or «Save and apply» (a second
  tap while streaming); a saved change not applied yet offers «Apply» (`POST /service/restart`). Set by the
  EnhancedIRL account: read-only, as on the web. Applying restarts the engine, which draws this screen: it comes back
  by itself, and the app finishes the restart even though the screen's request is cut (MANAGER.md §3).
- **Connections** (formerly Uplinks; renamed Oct 14, MODEMS.md D-M5): one row per uplink (kind, IP, what it sends, or
  why not); the row is its switch in or out of the SRTLA share. With the app's judgement (MODEMS.md §5) each row has
  its dot before the icon, a modem's operator as its title (its interface goes to the second line), and the reason
  of an amber or red dot, translated (`Stream.UplinkHealth`). Taking out the last usable one while streaming over SRTLA needs a second tap. A
  modem's row has a «›» to **its page** (`modempage.go`, MODEMS.md §7.4: signal on its scales, SIM and network, and
  Network, APN, Roaming, Name, Restart and «Unlock the SIM» or «Separate the networks»); a modem without a usable link
  has a row of its own that opens it. The rates come from the
  app's shared sample (one baseline of at least 1.5 s for every caller: the web's SSE and the screen no longer
  disturb each other's rates).
- **Pause**: the card's text (the engine's rule: 1-60 characters, letters, digits and some punctuation) and whether
  the pause silences the program.
- **Slots**: besides what each slot is and its alias, a Hide/Show button for the source in it (live, outside the
  draft; the source on air cannot be hidden).
- **Input**: the main source, its capture mode (USB), the EDID (HDMI) and the start audio, as the web's card; a
  draft until «Save» / «Save and apply»; «Scan».
- **Image (camera settings)** (`camera.go`, [`SOURCES.md`](SOURCES.md) §7): the USB cameras whose source carries
  `controls`, by slot (chips with more than one; the chosen camera and group last while the page is on view), the
  groups it has (chips: Image, Exposure, White balance, Focus & framing), and that group's settings as rows: an
  integer is − value + (a tap moves ~2 % of its range, held ≥ `TapLong` ~10 %, whole steps; the exposure in ms, the
  colour temperature in K, pan and tilt in degrees), a switch is the whole row, the anti-flicker is its choices side
  by side. Each row says its factory value; an inactive one (its automatic on) is dimmed, «Auto», says which switch
  frees it and sends nothing. «Factory settings» (a second tap) sends `camera_reset`. Live, also while streaming:
  `camera_control` and `camera_reset` go straight to the engine; a burst of taps steps from the value just sent until
  the state carries it (as the audio delays), and a refusal brings the state's value back. At least five rows fit (the
  Logi's Exposure); a group with more is paged (‹ ›). Without a camera with settings (or with an older engine), a
  message says which sources have none.
- **Wi-Fi**: «Create my Wi-Fi»'s password with its eye, its 2.4 GHz channel (Auto, 1-11) and «New password» (a second
  tap: every camera has to join again). The access point is changed by two clients (NetState: its name and «Create my
  Wi-Fi»; the device client: its channel, password and the cameras' way over it): one change at a time across both.
- **Network cameras**: the reception switch plus «Through the encoder's Wi-Fi» and «By cable»; the cameras, each one
  opening its own view (rename, its URLs and QR behind the eye, «New key», «Remove», with a second tap), and «Add
  camera».
- **EnhancedIRL**: unlinked, «Link» shows the code and its QR; pairing, «New code» or «Cancel»; linked, to whom,
  whether it is connected and «Unlink» (a second tap, saying what it does).
- **Name & password**: the encoder's name, and the web password (the current one, then the new one twice, like the
  wizard: a mistyped one would leave nobody knowing it with every web session closed). With no password yet, only the
  new one, twice (the setup's). The app limits wrong current passwords like the login (`429`).
- **Audio settings** (in the audio view, «Audio settings ›» in its header): each input's delay (0-500 ms, 10 ms a tap,
  100 ms held) and each camera's audio (a choice list: an input or «No audio of its own»).

### 21.5 Limits

- The keyboard types printable ASCII only: no accents in a pause card, a camera's or the encoder's name typed on the
  screen (a name set on the web with accents stays and shows).
- The screen's previews: `eirl-ui-render settings-dest|dest-reveal|dest-edited|dest-managed|dest-pending|dest-error,
  settings-uplinks|uplinks-single|uplinks-none|uplinks-health, settings-pause, settings-input…, settings-sources-hidden,
  settings-camera|camera-exposure|camera-framing|camera-reset|camera-two|camera-empty,
  settings-bluetooth|bluetooth-unavailable|bluetooth-nostack|bluetooth-noaudio|bluetooth-off|bluetooth-scanning|
  bluetooth-visible|bluetooth-busy|bluetooth-many|bluetooth-page2|bluetooth-device|bluetooth-forget, audio+phbt, audio+btin,
  settings-cloud|cloud-pairing|cloud-linked|cloud-unlink, settings-account|account-kb, audioset, audiosetpick` and the
  Wi-Fi and cameras ones (its file's header lists them all).

## 22. Fades one step per program frame, and the screen's tiles in step with the program (Oct 14)

Two things seen on the touch screen: the tiles of the sources changed a moment **before** the program, and a
fade between cameras was **not smooth**. Measured frame by frame on the board with a diagnostic build (not in
main): a trace of every program frame and every multiview frame (pts, when it left the compositor, luma) and,
per output frame of each compositor, the pts of the buffer each of its pads used; analysed off the board.
Sources: the board's HDMI at 1080p50, a USB camera at 1080p60 (MJPEG) and a USB capture card at 30 fps;
program at 60, screen at 30.

### 22.1 The fade

**Cause.** A Go ticker every 20 ms set the six properties of each layer, one by one, from the actor. The
program compositor does not give its frames out at a steady pace: now and then it stalls for ~110 ms and
catches up composing several frames 6-10 ms apart (they leave between 25 ms before and 115 ms after their pts;
none is lost, the pts stay continuous). The alpha a frame got depended on **when** the VIC composed it, not on
its time: two or three frames of a burst with the same alpha, then a jump of five ticks after each stall. A
frame could also come out with half a change.

**Fix.** The actor computes the whole fade at the program's rate (`FadeRects` at k/n, n = length × fps, then
the new scene) and an animator in C (`EgAnim`, `internal/gst`) plays it: a probe on the compositor's src pad
sets, after each output frame and from the compositor's own thread, every pad property for the next frame, by
its timestamp. A cut is a table of one step; before the program plays, the layers go straight onto the pads.
At the end of a fade the scene is placed again, for what changed meanwhile (a source found, another aspect).
`GstControlSource` does not fit: GStreamer 1.14's `videoaggregator` (nvcompositor's base class) syncs each pad
to the timestamp of **its own** current buffer, not the output's, so a 30 fps source or a still (the Pause, the
slate: one buffer every 250 ms) would fade in a handful of steps. Tested against the real 1.14.5 aggregator
(`TestAnimatorOneStepPerFrame`, with the software `compositor` where gst-plugins-bad is installed).

| Pause fade over the slate (400 ms, 9 of each) | Before | After |
|---|---|---|
| Program (60 fps): frames in the ramp | 24-28 | 23 (ideal 23) |
| Repeated alpha / jumps per fade | 12-15 / 2-3 (up to +0.31) | 0 / 0 |
| Step per frame (ideal 0.042) | 0 … 0.31 | 0.036 … 0.047 |
| Off a straight ramp (max) | 0.13-0.20 | 0.006 |
| Screen (30 fps): repeats / jumps per fade | 0-2 / 1-2 (up to +0.36) | 0 / 0 |

Fades between sources: the biggest change between two frames, against the median, went from 3-158× to
1.1-1.2× in full-screen crossfades, and from 200-1100× to 3-4× in a swap in PiP (what is left is its planned
move at ⅔). Unchanged, by design: the cut halfway in full → side by side (`later-cut`), and a change mid-fade
(§22.3). The fade no longer depends on the sources, their rates or the load: only on each frame's timestamp.

### 22.2 The tiles ahead of the program

**Cause.** A source's frame goes to the program and to its tile with the same pts (capture + `MIXER_SYNC`); the
program reaches the multiview through `pgmtap`, `ProgramDelay` (= `MIXER_LATENCY` + `MVMargin`, 100 ms) later.
Before, the tiles were **66 ms (HDMI at 50) and 75-80 ms (USB at 60) ahead** of the program on the screen:
two frames of the screen (correlation of the pictures of each tile and of the program tile, sharp peak).

**Fix.** Each tile waits after its conversion (small RGBA from its own pool, never the source's buffers, which
the program shares) in a queue `mvd<i>` whose src pad is offset (`gst_pad_set_offset`) by
`TileDelay` = `ProgramDelay` − one program frame: the program tile shows a program frame `ProgramDelay` after
its pts, and a program frame holds the source frames that start before its end, up to one program frame newer
than its pts. Both values come from the configuration (`MIXER_LATENCY`, the program's rate), not from the
sources. The queue and the pool are sized from the time a tile frame can wait (`MIXER_SYNC` + `TileDelay`, one
frame every `MVGap`): 10 frames and 16 buffers with the defaults (+6 buffers of 240×134 RGBA per tile, ~3 MB in
all). A tile without its queue is logged (`screen_tile_delay_missing`).

Fitting the delay to the pictures did not work: with the tiles delayed, the screen's compositor also shows the
program at another moment (the tiles were still a frame ahead at 67 ms, and 10-38 ms behind at 100 ms). The
values below come from the pads' own pts, per screen frame (source frame on the tile minus source frame inside
the program on the program tile; positive = tile ahead):

| Tile against the program on the screen | Before | After (mean; spread) |
|---|---|---|
| Board HDMI, 1080p50 | 66 ms ahead | +17 ms (0, +20, +40) |
| USB camera, 1080p60 MJPEG | 75-80 ms ahead | −9 ms (mostly ±16) |
| USB capture card, 30 fps | — | +7 ms (mostly 0 and ±32) |

What is left is where each source frame falls in a program frame and in a frame of the screen (a 60 fps
source also gives its tile one frame in two, `MVGap`): within a frame of the screen, which no fixed or
per-source delay removes.

### 22.3 Left open

- **A change mid-fade** starts from the scene the interrupted fade was going to: the layers still fading out go
  at once (a jump; it was so in the Python engine too). Starting from the picture half way needs `FadePlan` to
  handle layers at a partial alpha.
- The program compositor's stalls of ~110 ms do not lose frames, but they bunch them (also in the timings line:
  `program_to_screen … gap=146ms`): worth a look on their own; the stills pushed every 250 ms are a suspect.
- CPU and memory (60 s per thread, three sources live, screen on, preview off, not streaming): engine 61 → 64 %
  of one core and system 90 → 94 %, the difference in the capture threads (the compositor's thread, where the
  animator runs, 7.3 → 7.5 %); RAM ~105 MB in both. The two builds also differ in the screen work merged in
  between (Settings and the connections' dots).
