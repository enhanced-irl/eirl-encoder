> **Status:** IN PROGRESS — **in `main` (MR !5)**, phase 0 measured (1080p60 over USB fits), installed and tested on the board on Oct 12 (hot unplugging too). Still missing: per-source A/V sync and a long test. Camera image settings (§7): in a branch, Oct 14
> **Updated:** 2026-10-14

# Encoder video sources (board HDMI, capture cards and USB cameras)

The encoder no longer captures only the TC358743's HDMI: also a **USB capture card** (Elgato Cam
Link 4K, MS2130, MS2109) or a **UVC camera**, with preview and streaming just like HDMI. It is chosen from
the app ("Input" card). A single active source; changing it restarts the stream.

What brings the most is the Cam Link 4K: **1080p60 over USB 3**, which the 2-lane CSI cannot deliver (capped at
1080p50, see README).

The full plan, the research (devices, BELABOX, risks) and the decisions were kept in a separate internal
planning document ("Encoder video sources plan").

## 1. How it works

```
HDMI over CSI (UYVY) ──┐
Uncompressed USB ──────┼─ v4l2src ─▶ nvvidconv (copy to NVMM by CPU) ──┐
(NV12, YUY2, I420)     │                                               ├─▶ NV12 in NVMM ─▶ the usual:
USB MJPEG ─────────────┴─ v4l2src ─▶ jpegparse ─▶ nvv4l2decoder mjpeg=1 ┘   encoder, preview, level meter,
Test pattern ───────────── videotestsrc ─▶ nvvidconv ──────────────────────  ABR, SRT/SRTLA
```

- **Format and mode chosen in a single place**: `eirl-encoderd probe` (package `internal/sources`).
  Used by the app (what it shows) and by the engine (what it captures). "auto" rule: up to 1080p and 60 fps;
  higher resolution before more fps; uncompressed before MJPEG with the same mode (NV12 > YUY2 > I420 >
  UYVY > MJPG); below 24 fps only if there is nothing else; uncompressed only if it fits in the bus
  (USB 3 ≈ 300 MB/s, USB 2 ≈ 24 MB/s). With the camera in 4K, the Cam Link only offers 2160p30, which does not
  fit uncompressed: the camera has to be set to 1080p (warning `only_4k`). Anything above 1080p is
  streamed rescaled.
- **Several sources at once** (the mixer, `MIXER.md`): the bus is shared. The Nano's 4 USB 3 ports
  go through a single link, and a Cam Link in NV12 1080p60 already takes 62 %: two uncompressed do not fit.
  `eirl-encoderd probe -all [-max N] [-keep ID=FORMAT:WIDTHxHEIGHT@NUM/DEN]… [-want ID]…` (`sources.ChooseAll`):
  - It chooses for up to N USB sources, 3 by default (the HDMI does not count: it goes over CSI). The ones in
    `-keep` go first with what they have, if they still offer it, so that a camera on air does not change because
    another one is plugged in.
  - After them come the `-want` ones: the one about to be opened, so that the limit does not leave it out. Then,
    the rest in catalogue order.
  - Each one gets the best mode that leaves room on its bus (same `busnum`) for the cheapest mode of
    the following ones. That way a webcam drops to MJPEG so that a Cam Link fits, which only delivers
    uncompressed. If it does not fit in any way, it comes out without a format and with warning `no_bus`.
  - Output: `COUNT=n` and, with indices from 0, `SRC<i>_ID` plus the `probe` keys with the prefix
    `SRC<i>_` and `SRC<i>_BUS_SHARE` (% of the bus). It always exits with 0, also with `COUNT=0`; with
    bad arguments, 64.
  - It is read line by line, never with `eval`: `NAME` contains spaces.
  - MJPEG counts as 0 on the bus. It is not free: webcams use isochronous transfers and uvcvideo
    reserves an alt setting for them according to the mode (table below). But the Cam Link uses bulk, and with the
    largest reservation measured both run at full fps.
  - If with several at once the kernel cannot reserve (`No space left on device`), it will have to be accounted for.

  Measured on Oct 12 with fakesink. "Reservation" = the alt setting uvcvideo chose, read from sysfs:

  | Device | Transfer | Mode → reservation | Next to Cam Link NV12 1080p60 |
  |---|---|---|---|
  | Cam Link 4K | bulk (1024 B × burst 15) | NV12 and YUY2 1080p60 at 60 fps | — |
  | Logi 4K Stream | isochronous | MJPEG 2160p30 172 MB/s · 1080p60 98 · 1080p30 and 720p60 49 · 720p30 25 · NV12 1080p30 123 | all at full fps, Cam Link at 60 |
  | MS2109 ("USB3. 0 capture", it is USB 2) | isochronous | MJPEG 1080p60 24.6 MB/s (all of USB 2) at a real 60 fps · 720p60 16 | another bus |

  A hung Logi (`-71` in the probe control) fails to open even when there is nothing else on the bus:
  do not mistake it for a lack of bandwidth.
- **Device saved under a name that does not change**: `/dev/v4l/by-id/…` if the device has a serial
  number (the Cam Link has one) and `/dev/v4l/by-path/…` (the port) otherwise, because cheap capture
  cards repeat serial numbers. `auto` = the first capture card or camera that can be captured.
- **Without the device** (or without the requested mode, because the Cam Link only offers the camera's), the
  stream waits like HDMI without a signal: phase `waiting_device`, `DEVICE_TIMEOUT` (by default, the one of
  `SIGNAL_TIMEOUT`; 0 = forever).
- **Watchdog on every capture**: the Cam Link stops WITHOUT an error when the camera changes mode. And a
  watcher ends the stream if the node disappears (unplugged) so that systemd relaunches it and it waits.
- **Audio**: `AUDIO_SOURCE` = `auto` (the HDMI's with `hdmi`; the USB device's own with `usb`, which
  on the Cam Link is the HDMI audio) | `none` | `i2s` | `alsa:<id>` (any USB card, e.g. a
  microphone). USB uses the same scheme as I2S (mixer with background silence): if the
  card stops sending, the video does not freeze and the track does not disappear. If the chosen card is not
  there, it streams silence and warns.
- **Real fps**: the engine always runs the pipeline (with or without ABR) and leaves the capture's
  fps in `/run/eirl-engine/video`. The app shows them and warns if fewer than promised arrive
  (webcam in low light, capture card on USB 2).

## 2. Configuration

New keys in `/etc/eirl-encoder/engine.env` (written by the app; the same whitelists in
`internal/config/input.go` and in the script, because they end up inside the pipeline's `eval`):

| Key | Values |
|---|---|
| `SOURCE` | `hdmi` \| `usb` \| `test` (before, only in `/etc/default/eirl-engine`) |
| `VIDEO_DEVICE` | `auto` \| `/dev/v4l/by-id/<name>` \| `/dev/v4l/by-path/<name>` |
| `VIDEO_FORMAT` | `auto` \| `NV12` \| `YUY2` \| `I420` \| `UYVY` \| `MJPG` |
| `VIDEO_MODE` | `auto` \| `<width>x<height>@<fps>` (60 covers 59.94) |
| `AUDIO_SOURCE` | `auto` \| `none` \| `i2s` \| `alsa:<card id>` |

API: `GET /api/v1/sources` (catalogue, with `auto` and `options` per source) and an extended `PUT /api/v1/input`;
the state gains `input.active`, `signal.kind`, `signal.measuredFps`, `audio.state` `usb` and
`silence`, and the phase `waiting_device`. Details in [`MANAGER.md` §3](MANAGER.md#3-local-api-contract-apiv1).

## 3. Tested (Oct 11, on the board, without a capture card)

- `probe -list` against the real sysfs: the board's HDMI (`/dev/video0`) and its I2S audio; without USB,
  exit code 3 ("wait"); a `VIDEO_DEVICE` with `..` → exit code 2.
- Script with `SOURCE=usb` and no device: waits, `state` with `INPUT=waiting_device`, exits after
  `DEVICE_TIMEOUT` s.
- Script with the test pattern (rehearsal and streaming to a local listener, with preview): the launcher measures
  29.95 fps, correct `state`, the preview negotiates with `framerate=${FPS_FRAC}`.
- Test app on :8089 (temporary config, nonexistent unit) with Chrome in an iPhone viewport: the
  "Input" card lists HDMI, automatic USB and test pattern; saving a USB source writes the
  keys and the form redraws with what was saved.
- Tests: `go test ./...` (catalogue with a fake sysfs: Cam Link on USB 3 and 2, MS2109 without a serial
  number, two devices with the same serial, read error, "auto" rule, bus budget, API and
  journal) and the former Python engine's `test_engine_run.py`.
- `/code-review high` (Oct 11): 10 findings, all fixed. The big ones: `probe` exited with 2 on
  incorrect usage, which is also the exit code of a Go *panic*, and the script took it as an
  invalid configuration (64 → the unit in `failed` forever): now 64 on usage and any
  other code is retried. With the camera in 4K, `auto` said "none is connected" instead of
  "set it to 1080p". Two capture cards with the same serial shared the by-id link (the stream could
  switch cameras by itself): now they go by port. The udev rule arrived before uvcvideo
  enabled autosuspend.

## 4. Phase 0 with hardware (Oct 12)

Devices: **Elgato Cam Link 4K** (`0fd9:0066`, USB 3, audio `C4K`) with the tower PC at 1080p60 over HDMI, and
**Logi 4K Stream Edition** (`046d:086b`, USB 3, microphone `Edition`; MJPEG up to 1080p60, NV12 and YUY2
up to 1080p30; a second infrared node that is discarded). Measured with
`scripts/measure-source.sh` (since removed; the branch's script with its own `RUN_DIR`,
without touching `eirl-engine.service`): launcher fps, CPU of the WHOLE board for 10 s (includes ~5-10
points of noise: ssh, top) and consumption from the ina3221.

| Source and mode | Test | Real fps | CPU (of one core) | Consumption |
|---|---|---|---|---|
| Cam Link NV12 1080p60 | SRT streaming + preview | 60.9 | 91 % | 3.9 W |
| Cam Link NV12 1080p60 | Capture only | 59.9 | 78 % | 2.9 W |
| Cam Link YUY2 1080p60 | SRT streaming + preview | 60.0 | 122 % | 4.1 W |
| Logi MJPEG 1080p60 | SRT streaming + preview | 57.9 | 63 % | 4.0 W |
| Logi MJPEG 1080p60 | Capture only | 56.9 | 24 % | 3.4 W |
| Logi NV12 1080p30 | SRT streaming + preview | 29.0 | 44 % | 3.7 W |
| Logi YUY2 1080p30 | Capture only | 29.0 | 34 % | 3.0 W |
| (HDMI over CSI UYVY 1080p50, README) | SRT streaming | 50 | ~55 % | — |

- **Risk no. 1 is ruled out**: uncompressed 1080p60 over USB fits comfortably (91 % of one
  core = 23 % of the four), without losing frames. The Cam Link delivers 1080p60 in NV12, YUY2 and I420.
- **NV12 before YUY2** confirmed: YUY2 moves 33 % more data and costs ~30 more points.
- **MJPEG is the cheapest in CPU** (NVJPG decodes it), but it compresses twice: the "auto" rule
  still prefers uncompressed when there is an equal mode.
- **With the camera in 4K, the Cam Link only offers 2160p30** and "auto" discards it (373 MB/s do not fit;
  a separate test measured ~25 real fps at 4K): the `only_4k` warning and the `probe` message ("the camera
  sends more than 1080p, set it to 1080p") come out as planned.
- **Power supply**: with a long cable the board dropped to 4.3 V and rebooted when waking up both
  cameras. With the good cable, 5.3 V at rest and 5.27 V minimum while streaming, without `OC ALARM`.
- **Intermittent failure**: sometimes, especially on the first start after being stopped for a while, the
  pipeline does not start ("pipeline doesn't want to play"; with the real stream, systemd relaunches it after
  2 s and it carries on). Once it was another test using the same webcam. Suspect: PulseAudio (launched by
  any process on the board without `PULSE_SERVER`) opening the USB audio card at the same time. Now
  the launcher prints the real bus error and the udev rule sets `PULSE_IGNORE` on the capture cards'
  audio; still to be confirmed.
- The webcam drops fps in low light (a separate test saw ~17 fps in MJPEG 1080p30): the script sets
  `exposure_auto_priority=0` on the device (capture cards do not have that control).

### Installed in `eirl-engine.service` (Oct 12, 03:45)

Build of `feat/sources` with `main` (includes cloud-link, MR !6) on the board: app `a3ba599`, script and
launcher with `.bak-20261012-sources` backups and the udev rule.

- **Cam Link** chosen through `PUT /input` (like the web): rehearsal at 1080p60 NV12, 59.99 fps measured,
  audio from the Cam Link itself (`alsa:C4K`), state with `input.active`, `signal.kind=usb`,
  `measuredFps` and `audio.state=usb`.
- **Web tested by the operator** switching between webcam, test pattern, no audio and back to the webcam:
  each change restarts the stream and recovers it in 2-3 s.
- **Cold start with everything installed**: the udev rule leaves `power/control=on` on both
  capture devices and `PULSE_IGNORE=1` only on their audio (not on the board's cards).
- **Board HDMI without behaviour changes**: after a power cycle, 1080p50 at 49.95 real fps
  with I2S audio (the tower PC sends no audio: `AU_STATUS0=0x80`, as before).
- **Hot unplugging of the active source** (the webcam, 04:11): after 3 s the *watchdog* sees
  that no frames arrive and the stream exits; systemd relaunches it after 2 s and it stays in
  `waiting_device` ("waiting for the USB device … it is not connected"); when plugged back in,
  the stream carries on by itself ~3 s after the kernel re-enumerates it. A single restart.
  Unplugging a source that is NOT the active one (the Cam Link, 03:49) does not cut anything.
- From stopped to picture with the webcam: 2.6 s.
- 🪤 The TC358743 got stuck after the voltage drops and the reboots of the night (cable
  detected, but hotplug and DDC off and "Sleep mode: on"): only removing power fixed it.
  Reading register 0x0000 with `i2ctransfer` gives `0x00 0x00` also with the chip healthy: it is useless for
  diagnosis.
- 🪤 Unplugging a camera that a process has open leaves `WARNING … sysfs_remove_group …
  uvc_status_cleanup` traces in `dmesg` (uvcvideo of kernel 4.9). They are warnings, not an *oops*.
- Cause of the intermittent start failure: **PulseAudio**. Ubuntu's ALSA configuration starts it
  (autospawn) when any device is opened, even `hw:`, and it takes all the cards for
  a few seconds ("Device 'hw:C4K,0' is busy"). Fixed in the script (`PULSE_SERVER=unix:/nonexistent`,
  as the unit already did) and in the udev rule (`PULSE_IGNORE` on the capture cards' audio):
  2 failures in 8 starts before, 0 in 6 after.

## 5. Pending

- **A/V sync**
  with `avsync_ball.py` per source, **long test** (2 h, MJPEG through NVJPG included) and temperature.
- The Cam Link shows up fine if it is already plugged in at boot (several boots on Oct 12, also
  cold).
- MS2130: untested (none available). The MS2109 works in the mixer (MJPEG 1080p60 at 60 fps over USB 2).
- **Capture card connected but without a picture** (camera off): if it does not deliver frames, the watchdog stops
  the stream after 3 s and systemd relaunches it, in a loop (like BELABOX), with a log line every few
  seconds and SRT reconnecting. See in F0 what each device does without a signal (some send a
  "no signal" picture) and, if needed, wait with increasing pauses.
- Hung Cam Link (L4T does not ship the upstream reset). It happened on Oct 12: after a `-71`, it did not even enumerate
  (`device descriptor read/8, error -110`) and that is why `udev-settle` took 34 s at boot.
  - Switching off and on only its hub port (`pm_qos_no_power_off` 0→1 on `2-1-portN` and on its USB 2
    pair) does not recover it, and it is not known whether that cut removes the physical VBUS.
  - A warm reboot does not recover it either.
  - It has to be unplugged by hand.
- Audio delay and PTS correction: only if the sync measurements (`avsync_ball.py`) call for it.
- The "auto" rule (raw before MJPEG) is to be reviewed with the F0 measurements.

## 6. Install and undo

As always: script and launcher with `mv` into `/usr/lib/eirl-encoder/engine/` and then `JETSON=<board>
manager/build.sh` (which now also installs `/etc/udev/rules.d/70-eirl-capture.rules`: no autosuspend
on capture cards: it hooks onto the appearance of the video node and waits 1 s, because uvcvideo enables
autosuspend at the end of its probe and kernel 4.9 does not emit `bind` events). Back to HDMI: `SOURCE=hdmi` from the app. Undo the rule:
`sudo rm /etc/udev/rules.d/70-eirl-capture.rules && sudo udevadm control --reload`.

## 7. Camera image settings (UVC controls)

Asked for on Oct 14: the settings a USB camera offers (brightness, exposure, white balance, focus, zoom…),
**detected from the camera** and changed from Settings on the screen and on the web, while streaming too.

### 7.1 What a camera offers

- **Standard controls.** UVC defines a fixed list (its Camera Terminal and Processing Unit) that `uvcvideo`
  turns into standard V4L2 controls, with their minimum, maximum, step, default and current value. Any
  camera can be asked which ones it has without knowing its model; each one has only some of them.
- **Proprietary controls** live in *Extension Units*: a vendor GUID and opaque bytes, undocumented, that
  Linux only exposes if someone maps them by hand. **They are not exposed**: some are firmware or factory
  controls, and writing them blind can break the camera.
- **Only the controls in the table below are shown** (a whitelist of standard ones, with their meaning known and
  their names translated). A camera that brings another standard control gets it shown by adding it to the table.
- Read on the board on Oct 14 (read only, output off):

  | Source | Controls |
  |---|---|
  | Logi 4K Stream Edition (046d:086b) | brightness, contrast, saturation, sharpness, gain, backlight compensation (0-1), power line (50/60 Hz), white balance auto + 2000-7500 K, exposure auto (manual / aperture priority) + 3-2047, `exposure_auto_priority`, focus auto + 0-255, zoom 100-500, pan/tilt ±36000 (digital). Plus 7 Logitech extension units (~63 controls) |
  | MS2109 capture card (534d:2109) | brightness (−128…127, default −11), contrast (default 148), saturation (default 180), hue. Its defaults are not neutral |
  | Board HDMI (TC358743) | none (the bridge passes the signal through) |
  | Cam Link 4K | not read yet (unplugged); a capture card takes the camera's picture as it is |
  | Network cameras | none: they are set on the phone or the camera |

- `uvcvideo` 1.1.1 (L4T 4.9) answers `VIDIOC_QUERYCTRL`, `VIDIOC_QUERY_EXT_CTRL`, `VIDIOC_QUERYMENU`,
  `VIDIOC_G_CTRL` and `VIDIOC_S_CTRL`. Querying a control's range is cached by the driver after the first time;
  reading its value and writing it are USB control requests to the camera.

### 7.2 The controls (ids, groups, order)

The engine queries each one by its V4L2 id and reports those the camera has, in this order. Read-only or disabled
ones are left out.

| Group (`group`) | Control (`id`) | V4L2 | Type | Notes |
|---|---|---|---|---|
| `image` | `brightness` · `contrast` · `saturation` · `hue` · `gamma` · `sharpness` | 0x980900-03, 0x980910, 0x98091b | int | |
| `exposure` | `exposure_auto` | 0x9a0901 | bool | The V4L2 menu as a switch: 1 = the camera's automatic mode (aperture priority, else auto, else shutter priority), 0 = manual. Left out if the camera lacks manual or every automatic mode |
| `exposure` | `exposure` | 0x9a0902 | int | Units of 100 µs (UVC): the UIs show ms. Inactive while `exposure_auto` is 1 |
| `exposure` | `gain` · `backlight` | 0x980913, 0x98091c | int | |
| `exposure` | `power_line` | 0x980918 | menu | `options`: the values the camera has: 0 off, 1 50 Hz, 2 60 Hz, 3 auto |
| `white_balance` | `wb_auto` | 0x98090c | bool | |
| `white_balance` | `wb_temperature` | 0x98091a | int | Kelvin. Inactive while `wb_auto` is 1 |
| `framing` | `focus_auto` | 0x9a090c | bool | |
| `framing` | `focus` | 0x9a090a | int | Inactive while `focus_auto` is 1 |
| `framing` | `zoom` · `pan` · `tilt` | 0x9a090d, 0x9a0908, 0x9a0909 | int | `pan`/`tilt` in arc seconds (UVC): the UIs show degrees (÷ 3600) |

- An integer control whose range is exactly 0-1 (the Logi's backlight compensation) is reported as `bool`.
- `exposure_auto_priority` is **not** shown: the engine keeps it at 0 on every camera (with little light the camera
  would lower its fps), as it did before.

### 7.3 Contract (MIXER.md §5, additive)

State: each USB source of `sources[]` whose camera has at least one of those controls carries `controls`; other
sources, a hidden source and one that is gone carry none (the key is absent).

```json
"controls": [
  {"id": "brightness", "group": "image", "type": "int", "min": 0, "max": 255, "step": 1, "default": 128, "value": 140},
  {"id": "exposure_auto", "group": "exposure", "type": "bool", "min": 0, "max": 1, "step": 1, "default": 1, "value": 0},
  {"id": "exposure", "group": "exposure", "type": "int", "min": 3, "max": 2047, "step": 1, "default": 250, "value": 78},
  {"id": "power_line", "group": "exposure", "type": "menu", "min": 0, "max": 2, "step": 1, "default": 2, "value": 1, "options": [0, 1, 2]},
  {"id": "wb_temperature", "group": "white_balance", "type": "int", "min": 2000, "max": 7500, "step": 10, "default": 4000, "value": 3320, "inactive": true}
]
```

- `value` and `default` are always integers (a `bool` is 0 or 1). `inactive`: its automatic is on, the camera
  ignores a manual value now (the UIs show it as automatic and do not let it change). `options` only on `menu`.
- The values are read when the camera opens (after putting the saved ones back) and after each command: the engine
  does not poll the camera. A value the camera moves by itself (the exposure while it is automatic) is not followed.

| Command | Fields | Errors |
|---|---|---|
| `camera_control` | `source`: a USB source id; `control`: an `id` of the table; `value`: integer. An `int` is brought inside `min…max` and to its `step`; a `bool` takes 0 or 1; a `menu`, one of its `options` | `unknown_source`, `unsupported` (the source has no `controls` now), `unknown_control`, `bad_request` (no value, not an integer, outside a bool's or a menu's values), `inactive` (its automatic is on), `busy`, `camera_error` (the camera refused it or did not answer) |
| `camera_reset` | `source`. Every control back to its default (the automatics first) and the camera's saved values forgotten | `unknown_source`, `unsupported`, `busy`, `camera_error` |

- `ok: true` once the camera took it; the state already carries the new values when the reply arrives. A command
  that changes nothing is `ok: true` too.
- Allowed while streaming: that is what it is for (the exposure or the focus mid-stream). The UIs ask for a second tap
  before `camera_reset`.
- An older engine answers `unknown_command`; the UIs only show the settings of a source that has `controls`.

### 7.4 Saved per camera

- `/var/lib/eirl-engine/cameras.json` (the engine's): per source id, the values set by the user that differ from
  the camera's default, and when they were last changed. At most 16 cameras: the one changed longest ago goes.
  Written at most once a second, and when the engine stops.
- **Put back every time the engine opens the camera**: at start, after unplugging and plugging it back, after a
  `-71` reset, after showing a hidden source. The automatics first, then the values (a manual value whose automatic
  is on is not written). A saved value outside the camera's range now is brought inside it; a control the camera no
  longer has is ignored (and kept).
- `camera_reset` forgets them, also when the camera refused some default (it answers `camera_error`): what was
  saved would come back at the next opening.

### 7.5 Where it lives and how to try it

- `manager/internal/engine/camera`: the table, the reading and writing over the V4L2 ioctls (`List`, `Set`,
  `Reset`, `Apply`), and `Store` (`cameras.json`). Its tests use a fake camera with the Logi's controls.
- `manager/internal/engine/camctl.go`: the two commands. The actor only checks a command against the controls last
  read and queues it (at most 8 per camera, then `busy`); each USB source has its own goroutine for the camera, so a
  camera that stops answering (the driver's timeout on each request) never holds the actor. The reply comes from
  there, after the new state is published. A command is checked against the controls the previous one left (no
  full read before writing) and reads them back after it. A camera that fails twice while being read, or takes
  more than 1.5 s, is left without settings in the state (capturing goes on); the screen waits for a camera command
  at most 2 s, like the socket, so its next command is never left behind a camera that hardly answers.
- A source waits for its camera command in flight before it ends: a source that takes its place (another slot, a
  restart) never opens the camera in the middle of a write, and the engine saves `cameras.json` after it.
- The source puts the saved values back in `prepareUSB`, where it used to run `v4l2-ctl -c exposure_auto_priority=0`
  (now done with the same ioctl, in `Apply`). Hidden or gone: its `controls` leave the state.
- The screen: Settings › Sources › Image (MIXER.md §21). The web: the «Camera image» card, through `POST /mixer`
  (MANAGER.md §3).
- On the board: `eirl-enginectl camera 2` (the controls of the source in slot 2, as JSON),
  `eirl-enginectl camera 2 brightness 140`, `eirl-enginectl camera-reset 2`.
- Undo: `sudo rm /var/lib/eirl-engine/cameras.json` and restart the engine (the cameras keep the values they have
  until they are unplugged or reset).
