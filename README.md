> **Status:** IN PROGRESS — video + audio from the board's HDMI or from a USB capture card/camera → SRT/SRTLA as a systemd service, stable; latency ~1.06 s (HDMI)
> **Updated:** 2026-10-14

# eirl-encoder — EnhancedIRL's HDMI → SRT encoder (Jetson Nano + TC358743)

**eirl-encoder** is an open hardware and software encoder and live mixer for IRL ("in real life")
streaming. It is built on an NVIDIA **Jetson Nano** with a **Toshiba TC358743** HDMI-to-CSI-2 bridge:
it takes the HDMI input, USB capture cards and cameras, and network cameras or phones, mixes them
from a **touch screen**, and streams the program as H.264/AAC over **SRT** or **SRTLA** (several
internet uplinks bonded together). A **local web app** runs it from a phone or a laptop, and the
encoder can optionally be linked to an [EnhancedIRL](https://enhancedirl.com) account to control it
from the cloud.

## Features

- **Inputs**: the board's HDMI through the TC358743 over CSI (up to 1080p50 or 720p60, with HDMI
  audio over I2S); USB capture cards (Elgato Cam Link 4K up to 1080p60, MS2130, MS2109) and UVC
  cameras; network cameras and phones over RTMP or SRT, through the encoder's own Wi-Fi access point
  or by cable; a test source.
- **Live mixer** (the mixer is the encoder): cut and fade, picture-in-picture (4 corners, 2 sizes),
  side by side, a full-screen pause slate with text, letterboxing for sources that are not 16:9.
  Multiview on a 1024×600 touch screen on the board's HDMI output: put a source on air with a touch,
  hold to go live, lock the screen.
- **Audio independent from video**: one input or a mix, with per-input gain and delay, "audio follows
  video", and headphones on the screen's jack (the program audio over HDMI).
- **Output**: hardware H.264 (NVENC) and AAC over SRT (caller or listener) or SRTLA bonding across
  Ethernet, USB modems and Wi-Fi, with adaptive bitrate; going live or stopping is done hot, without
  interrupting the preview.
- **Local web app** (`eirl-encoderd`, a static Go binary): low-latency WebRTC preview with audio,
  level meters, destination, sources, network and Wi-Fi settings, logs. LAN only, password
  protected, strict input validation, least privilege (no root, exact sudo rules).
- **Networking**: the board's own Wi-Fi access point for cameras, Wi-Fi client, cameras by cable.
- **Optional link to EnhancedIRL**: status, thumbnails and remote control from the EnhancedIRL app.
- Boot splash, a slimmed-down base OS and, in progress, interface translations (English, Spanish and
  French), signed over-the-air updates and a first-boot setup wizard.

## Repository layout

| Path | What | Docs |
|---|---|---|
| `driver/` | Patch for L4T R32.7.6's TC358743 driver, built as an out-of-tree module | This README: "Building", "Bugs found and fixed" |
| `dt/` | Device-tree fragments: the TC358743 on the CSI port and I2S4 audio | This README: "Installing on the Jetson" |
| `edid/` | EDID generator (modes up to 1080p50, LPCM stereo 48 kHz only) | This README: "HDMI signal and EDID" |
| `build/` | Docker build (arm64) of the module, the DTB, the EDIDs, `srtla_send` and MediaMTX | This README: "Building" |
| `manager/cmd/eirl-engine`, `manager/internal/engine/` | The engine (Go, `eirl-engine-go`): capture, mixer, screen, audio, output. It replaced the original bash + Python engine, retired on Oct 14 | [`MIXER.md`](MIXER.md), [`ENGINE_GO.md`](ENGINE_GO.md), [`SOURCES.md`](SOURCES.md), [`HEADPHONES.md`](HEADPHONES.md), this README: "Operating" |
| `scripts/`, `systemd/` | The engine's launcher (`eirl-engine-start`), its socket client (`eirl-enginectl`), the boot HDMI init and the systemd units | This README: "Operating" |
| `manager/` | The management app `eirl-encoderd` (Go: local web and API, cloud link), the network helper `eirl-netsetup`, the boot splash `eirl-splash`, a simulated encoder and the deploy files | [`MANAGER.md`](MANAGER.md), [`NETWORK_SOURCES.md`](NETWORK_SOURCES.md), [`WIFI.md`](WIFI.md), [`SRTLA.md`](SRTLA.md), [`SPLASH.md`](SPLASH.md), [`SETUP.md`](SETUP.md) |
| `release/`, `manager/internal/update/`, `manager/cmd/eirl-update/` | Signed releases and over-the-air updates | [`UPDATES.md`](UPDATES.md) |
| `base/` | Recipe to slim down the Jetson's base OS | [`BASE.md`](BASE.md) |
| `tools/`, `mixer/`, `network/`, `latency/`, `monitor/` | Deployment, test and measurement tools | The doc of each piece |

The rest of this README is the operating manual: hardware, building, installing, operating, the bugs
found and the state of the test board.

## About this encoder

EnhancedIRL's own hardware encoder. Until 2026-10-07 it lived in the `devops` repo
(`encoders/jetson-nano-tc358743/`, MRs !336 and !343); it was moved here keeping its history.

A **Jetson Nano** captures HDMI (video and audio) with a
**Toshiba TC358743** bridge connected to the CSI camera port. It encodes **H.264 with the Jetson's
NVENC** and **AAC**, and streams over **SRT**. The destination can be any SRT ingest, including
EnhancedIRL's (MediaMTX). It is useful, for example, to bring a camera or a console into the Cloud OBS.

```
HDMI source ─▶ TC358743 ─CSI-2 (2 lanes, 972 Mbps/lane, UYVY)─▶ Jetson VI/NVCSI ─▶ /dev/video0
                  │ I2S (phase 2b)                                                        │
                  ▼                                                                       ▼
            I2S4 (pins 12/35/38, Jetson as slave)    v4l2src ─▶ nvvidconv ─▶ nvv4l2h264enc ─▶ h264parse ─┐
                  │                                                                                      │
                  ▼  ADMAIF2 (hw:tegrasndt210ref,1)                                                      │
            alsasrc ─┐                                                                                   │
  audiotestsrc ──────┴▶ audiomixer (background silence) ─▶ voaacenc ─▶ aacparse ────────────────────────┤
                                                                                                         ▼
                              srt-live-transmit file://con ◀─ FIFO /var/lib/eirl-engine.*.ts ◀─ mpegtsmux (1316 B)
                                            │
                                            ▼  SRT (caller or listener)
```

GStreamer 1.14, the one in L4T 32.7, **has no `srtsink`**. That is why the jump to SRT is made by
`srt-live-transmit` (Haivision SRT 1.5.7, built by `build/srt.sh` and shipped in the app package and the release
bundle; its `libsrt` in `/usr/lib/eirl-encoder/lib`, found through the binary's RUNPATH), which reads the MPEG-TS from a **named
FIFO** on its standard input. At first it was a local UDP socket, and the kernel dropped
datagrams in the bursts of each IDR, which produced artefacts (see [Bugs](#bugs-found-and-fixed)).

With an **`srtla://`** destination the stream is split across several internet uplinks (Ethernet, USB
modems, phones) and the EnhancedIRL relay puts it back together; with **`ABR=1`** the video bitrate
adapts to the network. Design, pieces and tests in [`SRTLA.md`](SRTLA.md).

Besides the board's HDMI, the stream can come from a **USB capture card** (Elgato Cam Link 4K,
MS2130, MS2109) or from a **UVC camera**, chosen from the app. With the Cam Link over USB 3 it reaches
**1080p60**, which does not fit over CSI. See [`SOURCES.md`](SOURCES.md).

With **several sources at once**, the encoder composes the program (cut, PiP, side by side,
fades), which is what always goes out. Its HDMI output shows a multiview with a touch screen to
put a source on air live, and the web does the same on the same state. It is the default
mode (`MIXER=0`, a single source). See
[`MIXER.md`](MIXER.md).

## Status by phase

| Phase | What | Status |
|---|---|---|
| 0 | Board inventory (read-only) | ✅ |
| 1 | Encoding chain + SRT with `videotestsrc`/`audiotestsrc` | ✅ NVENC verified (`tegrastats` → `NVENC 716`) |
| 2 | TC358743 video driver + DTB + EDID | ✅ 1080p30, 100/100 complete frames, clean `dmesg` |
| 2b | HDMI audio over I2S4 | ✅ `arecord` 10 s, 48 kHz stereo, no errors; listened to and correct |
| 2c | Modes up to 1080p50 with autonegotiation (EDID with a mode list selectable from the app, FIFO per mode, rejecting what does not fit) | ✅ **1080p50 in a real stream** (Oct 9): 50 fps, complete frames, SRT without losses, ~55 % of a core. 720p60/50 untested. See [Video modes](#video-modes-and-autonegotiation) |
| 3 | Integration (`v4l2src` + `alsasrc`), A/V sync, latency, systemd | ✅ **sync −31 ms ±33 ms** (to the frame), **latency ~1.06 s** (500 ms are the SRT buffer), `eirl-engine` service with automatic restart and an audio watchdog |
| 4 | Management app (`eirl-encoderd`, see [`MANAGER.md`](MANAGER.md)) | ✅ v1 on the board (`http://jetson.local`), reviewed (code and security) and without passwordless sudo. WebRTC preview with audio (phase 5) deployed on Oct 8; still to be tested on a real phone |
| 5 | SRTLA + adaptive bitrate ([`SRTLA.md`](SRTLA.md)) | 🟡 Tested over loopback on the board (bonding of 2 "uplinks", adding and removing them live, ABR) and **streaming over SRTLA to the eu1 relay with one uplink** (Oct 9). The tests with several real uplinks are missing; they need the 4 A supply |
| 6 | USB sources: capture cards and UVC cameras ([`SOURCES.md`](SOURCES.md)) | ✅ In `main` (MR !5) and installed on the board (Oct 12). **Cam Link 4K at 1080p60 streaming: 60.9 fps and 91 % of a core**; MJPEG webcam 1080p60, 63 %. The board's HDMI unchanged, hot unplug with automatic recovery. Per-source A/V sync and a long test are missing |
| 7 | Multicamera: composition and multiview with a touch screen ([`MIXER.md`](MIXER.md)) | 🟡 F0 and G0 (Oct 12): 3 cameras at 60 fps, 2 h without freezes, `nvcompositor` on the Nano. **F1 and F2 on the board (Oct 12):** eirl-engine is the mixer (isolated sources, the output is the program, stable MJPEG) and the web and the touch screen are a single control. **F3 on the board (Oct 12):** no rehearsal mode, the preview always on and Go live attaches the output hot, with retries. **F4a on the board (Oct 12):** the audio, independent from the video (one input plays: the HDMI's I2S, capture card, webcam or USB microphone). **Composition and screen (Oct 12):** PiP in 4 corners and 2 sizes, Pause with text, bars for sources that are not 16:9, fades in all compositions; on the screen, Go live by holding and lock. Next: more audio, F5 (robustness) and a street test |

**Open: the TC358743's audio PLL** sometimes does not lock after an HDMI renegotiation and stops
outputting audio. See [Audio that does not come back](#audio-that-does-not-come-back-tc358743-audio-pll).

**Open: the DSLR.** With a MacBook's HDMI output, with the same module and the
same cable, everything works. With the DSLR, the chip receives clock and vertical sync, but does not
decode the video. See [DSLR diagnosis](#dslr-diagnosis).

## Hardware

| Part | Detail |
|---|---|
| Board | Jetson Nano Developer Kit 4 GB, carrier **B01** (`p3449-0000-b00`), module P3448-0000 (SD) |
| OS | Ubuntu 18.04.6, **L4T R32.7.6**, kernel **4.9.337-tegra**. The OS is not upgraded |
| HDMI bridge | Chip `441473 358743G 2143HNL JAPAN` = **TC358743XBG** (week 43 of 2021). Unbranded module; it looks like a Geekworm C780A or Waveshare clone. **27 MHz crystal** (confirmed: `FV_CNT = 333` → exactly 30.0 fps) |
| Video connection | 15-pin flat cable to **CAM1** = I2C `cam_i2cmux` channel 1 (bus 8 in Linux), address **0x0f**, CSI port E (`port-index 4`, `serial_e`), 2 lanes |
| Audio connection | I2S from the TC358743 (master) to the **I2S4** of the 40-pin header (the Jetson is the slave): SCK→**12**, WS/LRCK→**35**, SD→**38**, GND→39. **Do not connect pin 40**, which is the I2S4's output. Verified with jetson-io: `i2s4 (12,35,38,40)` |
| I2S level | The chip has it in the VDDIO2 domain (1.65–3.6 V), so the board decides it. On boards for the RPi it is 3.3 V. This module works with the Nano's 3.3 V I2C pull-ups, which is the same domain → 3.3 V (inferred, not measured) |
| Bandwidth limit | 2 lanes × 972 Mbps in UYVY → **up to 1080p50** (the CSI takes 89 % of each line) **or 720p60**. 1080p60 does not fit (107 %), and the devkit's 15-pin connector has no more lanes (verified Oct 9). The Nano's VI **does not capture interlaced** |

| Power | 5 V jack with jumper **J48**. With today's **5 V 2 A** supply, the input drops to 4.68–4.76 V while streaming (4.86 V stopped; `POM_5V_IN` of the ina3221 6-0040) and `soctherm OC ALARM` warnings appear. **Use 5 V 4 A** |
| Reset pin | **RST** pad of the TC358743 board (3.3 V, the board's pull-up). **Not connected**: see [Hardware reset](#hardware-reset-of-the-tc358743-rst-pin) |

Access: over SSH with a key (`ssh <board>`). The commands in this README use `jetson` as the SSH host
for the board (for example `Host jetson` in `~/.ssh/config` pointing at `<board-ip>`); use your own
alias or `<user>@<board-ip>`.

## Repository structure

```
eirl-encoder/
├─ README.md                 ← this document
├─ MANAGER.md                ← management app (`eirl-encoderd`): design, local API, preview, privileges, installation
├─ SRTLA.md                  ← SRTLA streaming (uplink bonding) and adaptive bitrate: design, pieces, tests
├─ SOURCES.md                ← USB sources (capture cards and UVC cameras): how it works, configuration, tests
├─ SPLASH.md                 ← boot splash with the EnhancedIRL logo on the HDMI (boot, shutdown, rescue)
├─ MIXER.md                  ← multicamera: composition, multiview and touch screen (measurements, traps, engine, socket contract)
├─ NETWORK_SOURCES.md        ← cameras and phones over the encoder's Wi-Fi (F7): spike, contract, access point, MediaMTX
├─ mixer/f0/                 ← measurement harness (scenes, isolated source, long engine test, VIC, MJPEG, EDID, touch, F0 demo)
├─ network/spike/            ← F7 spike harness (RTSP → NVDEC probe, meters, publishers, the spike's MediaMTX)
├─ manager/                  ← code of `eirl-encoderd` (Go, static binary) + deploy/ (unit, drop-in, sudoers,
│                               uplinks dispatcher 90-eirl-uplinks, install.sh) + `eirl-splash` and deploy/splash/ +
│                               `eirl-netsetup` (the cameras' network: AP, DHCP, firewall, MediaMTX) and its units
├─ build/
│  ├─ Dockerfile             ← ubuntu:18.04 arm64 + toolchain (native gcc 7.5)
│  ├─ build.sh               ← R32.7.6 sources → module + DTB + EDID in build/out/ (ignored by git)
│  ├─ edid.sh                ← edid/*.hex → build/out/edid-*.bin (checked against make_edid.py)
│  ├─ srtla.sh               ← srtla_send (BELABOX, pinned commit) → build/out/srtla_send
│  ├─ srt.sh                 ← srt-live-transmit + libsrt (Haivision SRT, pinned tag) → build/out/
│  └─ mediamtx.sh            ← official MediaMTX arm64 (pinned version and sha256) → build/out/mediamtx
├─ driver/
│  ├─ tc358743-jetson.patch  ← patch on drivers/media/i2c/tc358743.c from L4T 32.7.6
│  └─ Makefile               ← obj-m (out-of-tree build, M=)
├─ dt/tegra210-porg-camera-tc358743.dtsi   ← replaces the B01 DT's imx219/imx477 includes
├─ dt/tegra210-porg-tc358743-i2s4.dtsi     ← audio: I2S4 as slave (dai-link-1) + i2s4b pinmux; included at the end
│                                             (build.sh also generates …-gpio.dtsi: the B01's gpio-input without J4-J7; not enough, see eirl-hdmi-init)
├─ edid/make_edid.py         ← EDID generator: modes up to 1080p50, LPCM stereo 48 kHz only
├─ edid/edid-<mode>-lpcm48k.hex ← the EDIDs' source of truth (1080p30, 1080p50 and 720p60 preferred), from make_edid.py
├─ manager/cmd/eirl-engine     ← the engine (Go, eirl-engine-go; ENGINE_GO.md); its packages in manager/internal/engine/:
│                               the core actor (sources, scene), output (NVENC + SRT/SRTLA, ABR), audio, phones
│                               (headphones), usbhub (hung USB card recovery), netcam (F7), screen (touch screen),
│                               tcaudio (TC358743 audio watchdog), hdmi, server (control socket)
├─ build/engine.sh             ← engine tests (Mac + linux/arm64 image) and build → build/out/eirl-engine
├─ scripts/eirl-engine-start   ← the service's ExecStart: runs eirl-engine-go (MALLOC_ARENA_MAX=2, GOGC=50)
├─ scripts/eirl-enginectl ← client of the mixer's socket (state, commands, automatic changes)
├─ scripts/avsync_ball.py    ← measures the A/V offset with YouTube's "Audio Signal Suite" test (ball + beep)
├─ scripts/eirl-hdmi-init.sh      ← at boot: DAP4 pins out of GPIO mode + loading the EDID
├─ systemd/eirl-hdmi-init.service ← oneshot that runs eirl-hdmi-init.sh
├─ systemd/eirl-engine.service  ← the stream as a service (Restart=always)
├─ manager/deploy/eirl-engine.default ← the board settings → /etc/default/eirl-engine (install.sh creates it if missing)
├─ tools/                    ← deploy-engine.sh (installs the engine without cutting a live stream),
│                               tap.py (touches on the touch panel), webproxy.py (the web with a session, for tests),
│                               engine-cpu.sh + thrcpu.py and engine-watch.py (engine CPU and RAM, MIXER.md §17)
├─ latency/                  ← end-to-end latency measurement (on-screen clock, see Measurements)
└─ monitor/mediamtx.yml      ← local viewer on the Mac (SRT pull → HLS/WebRTC/RTSP on 127.0.0.1)
```

## Building

It is built on the Mac, in native arm64 Docker: NVIDIA's x86 toolchain is not needed.
On kernel 4.9 the vermagic does not include the gcc version, so gcc 7.5 will do.

```bash
JETSON=jetson ./build/build.sh      # 1st time: downloads 162 MB and builds the base kernel (~11 min)
ls build/out/                       # tc358743.ko · tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb · edid-*.bin
```

- The sources are kept in `~/.cache/l4t-r32.7.6/public_sources.tbz2` and verified with sha256.
  The kernel config comes from the board's `/proc/config.gz`, and the base build gives an
  **identical** config and `kernelrelease = 4.9.337-tegra`.
- The tree is kept in the Docker volume `l4t-src`, not on the Mac's disk: APFS is not
  case-sensitive and the kernel has files that differ only in that.
- The built `Image` **is not installed**: it only serves to have `Module.symvers` and the DT.
- It is reproducible: the patched source is identical to the installed one and the DTB only differs in
  `nvidia,dtbbuildtime`.

## Installing on the Jetson

> Rules: **never** flash the QSPI or the SD card, never `apt dist-upgrade`, never touch the bootloader. Before
> touching `/boot`, make a copy. Every new boot configuration goes in a **new** entry of
> `extlinux.conf`, with the original as the alternative, and an OK is requested before rebooting.

```bash
scp build/out/{tc358743.ko,tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb} jetson:/tmp/
ssh jetson '
  sudo install -D -m0644 /tmp/tc358743.ko /lib/modules/4.9.337-tegra/extra/tc358743.ko && sudo depmod -a
  sudo cp /boot/extlinux/extlinux.conf /boot/extlinux/extlinux.conf.bak-$(date +%Y%m%d)
  sudo install -m0644 /tmp/tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb /boot/'
# The engine itself, with the EDIDs: tools/deploy-engine.sh --unit (see "Operating"), or a release bundle
```

Boot service (I2S4 pins + EDID): `eirl-hdmi-init.service` runs `/usr/lib/eirl-encoder/engine/eirl-hdmi-init.sh`.
Both come with the engine (`tools/deploy-engine.sh --unit`, or a release bundle, whose `install.sh` also enables
`eirl-hdmi-init` and `eirl-engine`):

```bash
# journalctl -u eirl-hdmi-init → "GPIO_CNF puerto J 0xf0 -> 0x00" and "EDID … cargado en /dev/v4l-subdev1" (the script's messages, in Spanish)
```

**A new device from a release bundle**: `sudo ./install.sh` in the extracted bundle installs the app, the engine
with its EDIDs, the SRT output and the units, and leaves the output off with no destination (an SD card image
runs it with `--image`, UPDATES.md §9). The bundle does not carry the TC358743 module, the DTB and its extlinux
entry (above), nor what L4T itself provides: GStreamer with NVIDIA's plugins, `v4l-utils`, `i2c-tools`, `python3`
and OpenSSL 1.1 (`libsrt` links it).

Entry added to `/boot/extlinux/extlinux.conf`. The `LABEL primary` one stays untouched:

```
LABEL tc358743
      MENU LABEL TC358743 HDMI-in (CAM1)
      LINUX /boot/Image
      FDT /boot/tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb
      INITRD /boot/initrd
      APPEND ${cbootargs} quiet root=/dev/mmcblk0p1 rw rootwait rootfstype=ext4 console=ttyS0,115200n8 console=tty0 fbcon=map:0 net.ifnames=0
```

The `LABEL tc358743-rst` entry (DTB `…-b00-tc358743-rst.dtb`, with the reset pin declared) was
**removed on Oct 8** because there is no transistor fitted; the DTB is still in `/boot` and the entry is in
`extlinux.conf.bak-20261008`. If one is ever fitted: see
[Hardware reset](#hardware-reset-of-the-tc358743-rst-pin).

Today it is on `DEFAULT tc358743`. To go back: `sudo sed -i 's/^DEFAULT tc358743$/DEFAULT primary/' /boot/extlinux/extlinux.conf`.
If it did not boot, choose `primary kernel` in the U-Boot menu (3 s, with a monitor and keyboard on the Jetson).

Check after booting:
`dmesg | grep tc358743` → `tc358743 8-000f: tc358743 found @ 0x1e (i2c-6-mux (chan_id 1))`, with no traces.
`/dev/video0` (`vi-output, tc358743 8-000f`) and `/dev/v4l-subdev1` must exist; the latter is the TC358743
(`/dev/v4l-subdev0` is `nvcsi`).

> A module that is loaded and in use by the VI cannot be unloaded (`rmmod`): **every change to the module
> requires a reboot**.

## Operating

The stream runs as a **systemd service** (`eirl-engine.service`, enabled): it starts with the board,
waits for an HDMI signal with no time limit and, if the pipeline fails (signal lost, mode
change, video watchdog, SRT), it exits and systemd relaunches it after 2 s. The board settings are in
`/etc/default/eirl-engine` (root only; `install.sh` creates a commented default on a new device and never
overwrites it); what the app manages (destination, bitrates, output, input) is in `/etc/eirl-encoder/engine.env`.

```bash
# Install or update: tests, builds eirl-engine-go, installs it with eirl-engine-start and eirl-enginectl
# (--unit: also the units) and restarts; refuses to do anything while streaming
# (a board without the app's install.sh: scp manager/deploy/eirl-engine.default and install it as /etc/default/eirl-engine, 0644 root)
JETSON=jetson tools/deploy-engine.sh --unit

# Change the destination: from the app (http://jetson.local, see MANAGER.md) or, by hand, in
# /etc/eirl-encoder/engine.env (loaded by a drop-in AFTER /etc/default/eirl-engine) and
ssh jetson 'sudo systemctl restart eirl-engine'
ssh jetson 'journalctl -u eirl-engine -f'        # log
```

> Files are replaced with `mv` (a new inode), never overwritten in place. (The former bash engine
> needed it because bash reads a script in chunks while it runs.)

Service details:
- `PULSE_SERVER=unix:/nonexistent`: outside a graphical session, some library started its own
  PulseAudio which grabbed the card. The audio is captured from ALSA directly.
- **`gst-launch` runs without `-e`**: with it, after an error (signal lost, watchdog) it hung
  waiting for an EOS that never arrives, without streaming and without exiting.
- A `watchdog` element after `v4l2src` (`WATCHDOG=3000` ms): if frames stop arriving, the pipeline
  fails and is relaunched.
- Watchdog every 2 s: HDMI signal present and with the same mode as at start (the caps are fixed
  to the start mode) and, with I2S audio, the two audio failures of
  [Audio that does not come back](#audio-that-does-not-come-back-tc358743-audio-pll).

Tested (2026-10-07): **signal cut while live** (EDID deleted) → the stream comes back by itself in
~11 s; **cold boot** → `eirl-hdmi-init` and the stream start automatically, streaming ~8 s after boot.

The engine (Python until Oct 14, Go since) reads its configuration from the environment: the keys
below, in `/etc/default/eirl-engine` and `/etc/eirl-encoder/engine.env`. Stream on and off with
`eirl-enginectl output on|off`. Main keys:

| Key | Use |
|---|---|
| `SOURCE=test\|hdmi\|usb` | Source (with `MIXER=0`; the mixer takes its sources from the slots) |
| `RES=WxH FPS=fps` | Resolution and fps. With `hdmi`, by default the signal's |
| `VBITRATE=kbps GOP=frames` | CBR bitrate and GOP. By default 6000 and 2 s |
| `ABITRATE=kbps AENC=voaacenc\|avenc_aac` | Audio |
| `SRT_URL=srt://… SRT_MODE=caller\|listener SRT_LATENCY=ms SRT_PASSPHRASE SRT_STREAMID` | SRT destination |
| `AUDIO=test\|silence\|i2s` | Audio. By default `test` with the `test` source and `i2s` (real HDMI) with `hdmi` |
| `OUTPUT=on\|off` | `off` = **rehearsal mode**: capture with preview and level meter, without the main encoder or SRT (`SRT_URL` is not needed) |
| `LIVE=1 LIVE_SIZE=640x360 LIVE_VBITRATE=800 LIVE_ABITRATE=96 LIVE_VPORT=5006 LIVE_APORT=5008` | Live preview for the management app (WebRTC): a second, small H.264 encode + Opus, over RTP to localhost (branches of the `tee`, see [`MANAGER.md`](MANAGER.md) §4). `0` = without them |
| `LEVELS=1 LEVELS_PORT=5005` | Level meter: the mixer's PCM over UDP to `127.0.0.1:LEVELS_PORT` (if nobody listens, it is lost) |
| `RUN_DIR=/run/eirl-engine` | tmpfs for the state and the SRT statistics; created by the service (`RuntimeDirectory`). By hand, if it cannot be written, the statistics are disabled |
| `SRT_STATS=<f.json>` | **Latest** SRT statistics sample in JSON, ~1 per second and with an atomic rename (by default `RUN_DIR/srt-stats.json`; empty = no statistics). Previously it was appended to the file endlessly (~86 MB/day) |
| `RECORD=<f.ts>` | Diagnostics: local copy of what comes out of the multiplexer (before SRT) |
| `SIGNAL_TIMEOUT=s` | Wait for an HDMI signal at start (60; `0` = no limit, the service's) |
| `WATCHDOG=ms` | No video frames for that long → the pipeline fails (3000; `0` = disabled) |
| `AUDIO_LOCK_TIMEOUT=s` | HDMI audio without the PLL locking for that long → recovery (10; `0` = never) |
| `HW_RESET_INTERVAL=s` | Minimum between two hardware resets (300) |

With the `hdmi` source the engine does four things before capturing:
1. **Loads the EDID only if it has changed.** Writing it drops the hotplug and the source renegotiates.
2. Waits for a signal (`--query-dv-timings`, `SIGNAL_TIMEOUT`).
3. Sets the detected timings and exits with an error if the signal is interlaced.
4. Sets the **UYVY** format.

If the capture fails for good, the engine exits and systemd relaunches it.

**Validation**: before building anything, every variable goes through a whitelist (numbers, paths,
enumerations; `SRT_URL` = `srt://host:port` without a query; streamid `[A-Za-z0-9_:./-]`; passphrase
`[A-Za-z0-9_.~-]{10,79}`). What does not fit exits with **64** and `event=config_invalid key=<VAR>`.
The rule comes from the former bash engine, where almost everything ended up in `gst-launch`'s `eval`
or in the SRT query, and a `VBITRATE=a[$(cmd)]` executed code.
The log never carries the streamid or the passphrase: `srt-live-transmit`'s stderr goes through a filter
that masks them (it printed them in `Media path: …`) and removes the `N bytes lost, …` it repeated every 5 s
without a client.

**Viewer on the Mac**: `brew install mediamtx && mediamtx monitor/mediamtx.yml`, and in the browser
<http://127.0.0.1:8888/jetson/> (HLS, video and audio) or <http://127.0.0.1:8889/jetson/> (WebRTC,
**video only**: AAC does not travel over WebRTC). MediaMTX *pulls* from the Jetson's listener. It also
serves RTSP at `rtsp://127.0.0.1:8554/jetson` (the latency measurement uses it).

### Measurements

| | Test source 1080p30 | Real HDMI 1080p30 |
|---|---|---|
| Output | H.264 High 1920x1080 30/1, IDR every 2.0 s, AAC-LC 48 kHz stereo, ~6.3 Mbps | Same |
| CPU | One core at 85–95 % (`videotestsrc` and overlays drawn on the CPU) | ~37 % + ~20 % on two cores |
| NVENC / power | 716 MHz / 3.0 W | 716 MHz / 2.7 W |
| Viewer (HLS) | — | ~1.1 s behind the live edge, 0 frames lost |

**Stability test** (2026-10-07, 3.6 min of HDMI from the tower, Mac over WiFi at −70 dBm):
0 VI errors, 0 datagrams lost on the Jetson, SRT with **0 packets lost, 0
retransmitted and 0 dropped** (average RTT 6 ms, maximum 78 ms), MediaMTX without drift warnings, and
in the player 0 freezes and 2 frames lost out of 6867.

**A/V sync** ("Audio Signal Suite" test on YouTube: a bouncing ball with a beep on each bounce,
measured in software on MediaMTX's recording): **−31 ms on all 7 bounces** (the audio is
slightly ahead), with a resolution of ±33 ms → **in sync to the frame**. To measure:
record with MediaMTX (`record: yes`, `recordFormat: mpegts`), cut the test section with
`ffmpeg -ss <s> -t 26 -i rec.ts -c copy test.ts` and run `scripts/avsync_ball.py test.ts`.

**End-to-end latency** (2026-10-07; from the tower drawing a frame on the Jetson's
screen to it coming out decoded on the Mac, through TC358743, NVENC, SRT and MediaMTX): **median
1047–1069 ms** in two 30 s measurements (~890 frames, none discarded), stable between 1045 and
1080 ms, ±20 ms from one frame to the next. **500 ms are the SRT buffer** (`SRT_LATENCY=500`, chosen for
the Mac's WiFi); the remaining ~550 ms are the tower drawing, capture, encoder, multiplexer with the
AAC, MediaMTX and decoding. Not broken down yet.

How it is measured (`latency/`): the Jetson serves `clock.html` (`python3 clock_server.py 8080`, in
`/usr/lib/eirl-encoder/engine/latency/`). The source opens it full screen on the output that goes to the TC358743 and
draws 32 stripes: `[1,0]`, 24 bits with the Jetson's ms (mod. 2²⁴), 4 check bits
(v mod. 13) and `[0,1]`, with the time synchronised with `/now` compensating for the RTT. On the Mac,
`python3 latency/measure.py rtsp://127.0.0.1:8554/jetson http://<board-ip>:8080 30` reads the
stream, decodes row 810 of each frame and compares it with the arrival time converted to the
Jetson's time. It does not depend on the source's or the Mac's clocks. It discards the first second: it is the
burst ffmpeg accumulates when connecting.

### HDMI audio (I2S4)

- The test tower is **Linux** (RTX 4060 AD107, GNOME, PipeWire 1.6 + WirePlumber). It sends
  audio over HDMI only if **that monitor is the sound output**: two "HDMI / DisplayPort -
  AD107" appear; the Jetson's is the sink `…hdmi-stereo-extra1` ("HDMI 2"), with ELD `JetsonHDMI-IN`
  (in `/proc/asound/card*/eld#0.4`). Every time the tower detects the display again (Jetson
  reboot, new EDID, reset) the default output may change.
- Chip status: `v4l2-ctl -d /dev/v4l-subdev1 -C audio_present,audio_sampling_rate`, or better
  `AU_STATUS0` (0x8523: `i2ctransfer -f -y 8 w2@0x0f 0x85 0x23 r1`). Bits: 0x80 mute · 0x40 PLL
  phase-locked (P_Lock) · 0x20 **frequency-locked (F_Lock)** · 0x08 DSD · 0x04 HBR ·
  0x02 non-LPCM · 0x01 samples arriving. **0x61 = playing**; 0x40/0xE0 = no samples; **0x41 = samples
  arriving but the PLL does not lock in frequency → no I2S clock**. Even with no sound, the chip already
  measures 48 kHz: those are the ACR packets, which the graphics card always sends. `Audio_Counter` (0x862F/0x862E)
  = samples in 100 ms (0x12C0 = 4800 at 48 kHz).
- **If the I2S capture starts without BCLK, it stays dead**: the I2S4 reset fails (`Failed at I2S4
  DAP RX sw reset`) and `hw_ptr` in `/proc/asound/tegrasndt210ref/pcm1c/sub0/status` does not advance even if
  audio arrives later. It is fixed by relaunching the pipeline (the watchdog does it).
- **The TC358743 stops the I2S clock (BCLK and LRCK) when it receives no samples.** That is why the script mixes
  `alsasrc` with background silence in an `audiomixer`, and uses `provide-clock=false`: if the audio
  is cut (video paused), the stream carries on at 30 fps. Verified.
- Manual capture: `amixer -c tegrasndt210ref cset name="ADMAIF2 Mux" I2S4` and
  `arecord -D hw:tegrasndt210ref,1 -f S16_LE -r 48000 -c 2 -d 10 x.wav`. **ADMAIF1 is held by
  PulseAudio** of the graphical session (`Device or resource busy`).
- Checking the I2S clock without instruments: port J's `GPIO_IN` register (`0x6000d234`) reflects
  the pin even in I2S mode. Sampling it in a loop, BCLK and LRCK come out at ~50 % if there is a clock
  (LRCK gave 50.01 %).

### Audio that does not come back (TC358743 audio PLL)

**Symptom**: `AU_STATUS0 = 0x41` instead of 0x61: samples arrive and the PLL locks in phase, but
**not in frequency** (S_A_F_Lock is missing). Without F_Lock the chip does not fill its audio FIFO and leaves the
I2S clock stopped: nothing reaches the Jetson.

**When** (2026-10-07, ~3 times): after an **HDMI renegotiation** with the source (Jetson reboot
with the EDID reloaded; the tower detecting the display again), and once after a cut of
~2 s of the sound. **It is intermittent**: it is not reproduced by pausing the source (short pauses or 27 s ones,
the PLL comes back by itself) nor by unplugging and plugging the HDMI cable.

**What does NOT fix it**: the chip's configuration, which is correct (`LOCKDET_REF`, `NCO_F0_MOD`,
`SYS_FREQ`, `ACR_MODE/MDF`, `FS_IMODE`, `AUTO_CMD0-2`, all as per the driver); the power supply; the
cables; the ACR mode; clearing interrupts; renegotiating the HDMI; the chip's software resets
(`SYSCTL`); **rebooting the Jetson** (the chip does not lose power). The host's audio commands
(`CMD_AUD` 0x8601) are **ignored** by the chip while it is in automatic mode (`AUTO_CMD2[2:1] ≠ 0`, 0x0c).

**What DOES fix it**: removing the power (shutting down and unplugging the Jetson) and the hardware reset
through the RST pad (tested once, see below). In both cases the chip was configured with the source already
sending audio and it locked.

**Automatic recovery** (the engine's audio watchdog, `manager/internal/engine/tcaudio`, with `AUDIO=i2s`):
1. **Samples + F_Lock but `hw_ptr` stopped for 6 s** (dead capture): it exits and systemd relaunches the
   pipeline. A few seconds of cut, without touching the HDMI.
2. **Samples without F_Lock for `AUDIO_LOCK_TIMEOUT` (10 s)**: without cutting the video and in
   this order, it tries three software recoveries, switching the chip's sequencer to manual (`AUTO_CMD2
   = 0`, `CMD_MUTE` first) and back to automatic: reinitialising the buffer (`CMD_BUFINIT`),
   relaunching the frequency detection (`CMD_LOCKDET`) and restarting the NCO (`NCO_F0_MOD`). After each one
   it waits 3 s and leaves the result in the journal: `journalctl -u eirl-engine | grep audio_soft_recovery`.
   Since Oct 14 there is a last step, a hotplug pulse (the EDID rewritten), only when not on air (ENGINE_GO.md §15.2).
   **None has been tested against the real failure** (it could not be reproduced at will): the log will say which one
   works.
3. If none works and the DT declares the reset pin, it requests a **hardware reset**
   (`$STATE_DIR/hw-reset.request`), exits, and the next start of the engine does it with the pipeline
   stopped, at most once every `HW_RESET_INTERVAL` (300 s). Without the pin, it warns once and carries on without audio.

### Hardware reset of the TC358743 (RST pin)

The driver supports `reset-gpios` in the DT (upstream) and the patch adds:
- **Reset at probe**, with an active wait until the chip answers over I2C (up to 3 s, 3 attempts;
  with this wiring it has taken from 0.1 s to more than 3 s, versus upstream's 20 ms).
- **`hardware_reset` control** (a button, `v4l2-ctl -d /dev/v4l-subdev1 -c hardware_reset=1`): a pulse on
  RESETN and a full reprogramming of the chip (`initial_setup`, PLL, CSI, format and the **EDID**, of which
  the driver keeps a copy because the reset erases it), without re-probing the driver. It runs in a work item because
  `s_ctrl` runs with the ctrl_handler's lock, which the interrupt polling also takes.
  Do it with the pipeline stopped: the CSI comes back with the next `s_stream`. The hotplug drops and rises,
  so the source renegotiates.
- If the pin is not in the DT or cannot be requested, the driver carries on without it (a warning, not an error).

**The 2026-10-07 experiment and why it is disabled**: a **direct** wire from the RST pad to
**pin 31** of the 40-pin header (GPIO11 = PZ0, `TEGRA_GPIO(Z, 0)`).
- It worked once: the live reset fixed the 0x41 without removing power.
- But the header's pins go through a **TXB0108 level shifter** (weak output, ~4 kΩ),
  which latches in whatever state it is in and fights the board's pull-up. With a pulse of
  1-2 ms the chip took between 0.1 and more than 3 s to come back. With a 10 ms one, RST stayed at
  **0.6 V** (chip permanently in reset), **even after a power cycle**, and forcing the pin
  high the Jetson read it oscillating. It only recovered **by removing the wire**.
- That is why `dt/tegra210-porg-camera-tc358743.dtsi` has `reset-gpios` **commented out**. To enable it:
  fit an **open-drain transistor**, which only pulls to GND and never fights the pull-up:
  2N7000/BS170 with the gate to pin 31, source to GND, drain to RST and 100 kΩ gate-GND (or an NPN with 1 kΩ
  on the base and 10 kΩ base-emitter). **Careful: the transistor inverts**: GPIO high = RST low; in the DT it
  would be `GPIO_ACTIVE_HIGH`. Uncomment, rebuild the DTB and test the pulse before leaving it in
  `DEFAULT`.

### Video modes and autonegotiation

The source chooses the mode among the EDID's (almost always the preferred one, unless it is forced to another)
and the script streams whatever arrives: resolution and fps come from the signal's timings, and if the source
changes mode the watchdog detects it and the service is relaunched with the new one. What each piece does:

- **EDID**: it announces only modes that fit (table). There are three files with the same list and a different
  **preferred** mode (`edid-{1080p30,1080p50,720p60}-lpcm48k.bin`, generated by `build.sh`). It is
  chosen from the app ("HDMI input" card, `PUT /api/v1/input` → `EDID_PREFERRED` in its
  configuration); 1080p30 by default. The engine loads the chosen one at start if the chip has
  another (the source renegotiates) and leaves it in `/run/eirl-engine/state` (`EDID=`); `eirl-hdmi-init.sh` already loads
  the chosen one when the board powers on. The source is not obliged to use the preferred one: the test
  DSLR stays at 1080p30 even when asked for 1080p50 (Oct 9; probably because it is set to NTSC).
- **Driver**: the FIFO level (`FIFOCTL`) is no longer the fixed 374: it is computed for each mode in
  `tc358743_set_csi()` and left in `dmesg` (`…: FIFO N (pico ~M), CSI al X% de la línea`, i.e. peak ~M, CSI at X % of the line).
  Diagnostics: `fifo_level=N` as a module parameter forces it (0 = computed).
- **Script and app**: they reject the modes that do not fit (`no cabe en 2 lanes CSI` — "does not fit in 2 CSI lanes" —, exit 2 and
  retry; the app marks them "Not supported"). Rule: the CSI has to send each line
  (width × 16 bits) in ≤ 95 % of the time of one HDMI line (total ÷ pixel clock).

| Mode | CSI load per line | Computed FIFO | Status |
|---|---|---|---|
| 1080p60 | 107 % | — | ❌ does not fit, rejected |
| **1080p50** | **89 %** | 16 (peak ~190 words) | ✅ tested Oct 9 |
| 1080p30 / 25 / 24 | 53 / 44 / 43 % | 374 (the usual one) | ✅ 1080p30 in use |
| 720p60 / 50 | 47 / 40 % | 249 (374 before) | 🟡 untested |
| 576p50 · 480p60 · VGA | 17-19 % | 280 · 280 · 254 | 🟡 untested |

**The FIFO**, in 32-bit words (2 UYVY pixels). The CSI starts emptying a line when the
FIFO reaches the level:
- If the line comes in more slowly than it goes out (everything except 1080p50), you have to wait for
  `words × (1 − input/CSI)` so the CSI does not catch up with the input in the middle of the line. It is the
  formula that reproduces mainline's 374: 1080p30 at 2 lanes and 1080p60 at 4 lanes give 373.3.
- If it comes in faster (1080p50: 2.376 Gbps of input versus 1.944), it is better to start as soon
  as possible. The FIFO accumulates `words × (1 − CSI/input)` ≈ 175 words on top of the level,
  below the 374 we already know fit.

**Tested on Oct 9** with the source choosing 1080p50 from the app's "HDMI input" card:
- `dmesg`: `1920x1080@50: FIFO 16 (pico ~190), CSI al 88% de la línea`; 1080p30 still at 374.
- Direct capture from the VI (`v4l2-ctl`, service stopped): **50.00 fps**, 500 frames in 10.1 s, 0 frames
  with zeroed or repeated rows (the same in 1080p30: 30.00 fps).
- Capture + NVENC 10 Mbps + preview, 30 s to a file: **1500/1500 frames** in the main output and in the
  preview, High 1920x1080 50/1, gst at 33-44 % of a core, NVENC at 716 MHz.
- **Real stream** to the eu1 relay (6 Mbps): 50 fps, SRT 5.4-5.9 Mbps, RTT 56 ms, 0 dropped (the
  few losses are retransmitted), gst ~55 % of a core, board ~3.3 W, no errors in the journal.
- The board showed **undervoltage** (4.63 V) during the whole test: see "5 V 4 A supply".
- **Hotplug**: the DSLR stayed at 1080p30 with 1080p50 preferred (NTSC?); with an EDID change
  a source may not renegotiate if it ignores the hotplug pulse (~100 ms from the driver).

**Original test plan** (done except 720p and the `fifo_level` sweep):
1. Install `tc358743.ko`, the EDID and the script (the DTB does not change: it comes out identical except for the date) and
   reboot. Check that 1080p30 is still the same: `dmesg | grep FIFO` → `FIFO 374`.
2. On the tower, choose 1920x1080 at 50 Hz. Expected: `dmesg` → `1920x1080@50: FIFO 16 (pico ~190),
   CSI al 88% de la línea` and the stream at 50 fps. Criteria: complete frames (`v4l2-ctl
   --stream-mmap --stream-count=300` without zeroed buffers or truncated lines), 50.00 fps at the
   output, no VI drops in `dmesg` and CPU/NVENC with headroom (`tegrastats`).
3. The same at 720p60 (the FIFO goes down from 374 to 249) and 720p50.
4. If 1080p50 fails: try `fifo_level` by hand (with `sudo`, in
   `/sys/module/tc358743/parameters/fifo_level`, and change mode on the source so it is applied).
5. Bitrate: 1080p50 asks for more than 1080p30's 6 Mbps for the same quality (~9-10 Mbps).

### HDMI signal and EDID

- Own EDID (`edid/make_edid.py <output.bin> [1080p30|1080p50|720p60|480p]`; the argument is the
  **preferred** mode, the list is the same). What a device gets is `edid/edid-<mode>-lpcm48k.hex`
  (1080p30, 1080p50, 720p60): `build/edid.sh` decodes them to `build/out/edid-*.bin` and fails if
  `make_edid.py` no longer produces them, and the engine's install (`tools/deploy-engine.sh`, the release
  bundle) puts them in `/usr/lib/eirl-encoder/engine/`. To change one, change `make_edid.py` and regenerate its `.hex`
  (the hex of `make_edid.py out.bin <mode>`, on one line). What they announce:
  - **Video**: 1080p50, 1080p30, 1080p25, 1080p24, 720p60, 720p50, 576p50, 480p and VGA; preferred
    1080p30. No interlaced mode and no 1080p60. Clock cap (VSDB and limits) 150 MHz.
  - **Audio**: **LPCM stereo at 48 kHz and 16 bits only**.
  - HDMI block (VSDB) included, so that the source sends audio.
- Manual load: `v4l2-ctl -d /dev/v4l-subdev1 --set-edid=pad=0,file=<bin>,format=raw`. Read back, it matches byte for byte.
- With the MacBook as the source, **1920x1080p30** is received, 74.25 MHz, VIC 34, **YCbCr 4:4:4 BT.709 limited range**,
  8 bits per channel and no HDCP. It comes out of the CSI in UYVY and the picture shows the right colours.
- Raw chip status: `i2ctransfer -f -y 8 w2@0x0f 0x85 0x20 r1` (`SYS_STATUS`). With
  `0x9f` there is sync and HDMI mode. Bits: 0x01 5V · 0x02 TMDS · 0x04 PHY PLL · 0x08 SCDT ·
  0x10 HDMI · 0x80 stable sync.

## Bugs found and fixed

They are all in `driver/tc358743-jetson.patch` and `dt/…dtsi`. The first three are **bugs in
4.9's upstream `tc358743.c`** that are already fixed or rewritten in mainline.

1. **The subdev was registered before the probe finished.** Tegra's VI hooks in
   synchronously inside `v4l2_async_register_subdev()` and calls `get_fmt` while `mbus_fmt_code`
   is still 0. Result: a NULL *oops* in `tegra_channel_fmts_bitmap_init` and a half-set-up VI
   channel. → Registration moves to the end of the probe, as NVIDIA's `tc358840` does, and also
   `entity.function = MEDIA_ENT_F_CAM_SENSOR`.
2. **`i2c_wr16_and_or()` read and wrote 1 byte instead of 2.** The chip ignores partial
   writes to 16-bit registers, so these three were never applied:
   - `SYSCTL` (sleep): the chip stayed asleep, without a TMDS signal.
   - `PLLCTL1` (`PLL_EN`): the PLL stayed off.
   - `CONFCTL` (`AUTOINDEX`): the HDMI register block (0x8000 onwards) does not auto-increment
     the I2C address, and **the EDID was written corrupted**, with all the bytes in the first position.
   → 2 bytes are written, as in mainline. Also, multi-byte accesses to 0x8000 and
   above are split into 1-byte accesses, because `AUTOINDEX` is 0 after the reset and the
   driver does not enable it until the end of `initial_setup`.
3. **FIFO overflow.** With 594 Mbps per lane and `fifo_level = 16`, the VI received ~595 of
   1080 lines, and only in 1 out of every 4 buffers; the rest arrived as zeros. → Backport from mainline and
   RPi: **972 Mbps per lane** (`link-frequencies` 486 MHz, `pll_fbd = 144`) with its CSI
   timings, and **`fifo_level = 374`**. Since Oct 9 it is computed for each mode (see
   [Video modes](#video-modes-and-autonegotiation)); at 1080p30 it still gives 374.
4. Upstream drops to 1 lane when that is enough (for example, at 720p30), but the VI is programmed
   with the DT's lanes and then it does not sync. → **The DT's lanes are always used**.
5. The VI was missing `enum_mbus_code`, `enum_frame_size` and `enum_frame_interval`, and the default
   format was RGB888, which VI2 does not support. → The three are implemented and **UYVY becomes the
   default format**.
6. **No INT pin** on the 15-pin connector. → Backport of mainline's polling mode: a timer
   and a work item every 500 ms run the ISR.
7. **No `of_match` table.** The module did not bind or autoload from the DT. → 
   `toshiba,tc358743` is added.
8. DT: with a continuous clock you have to set `discontinuous_clk = "no"` in `nvcsi/channel@0`, and the DT
   must not carry `modeX` nodes. That way the VI takes the "HDMI-IN" path, the one with `dv_timings` without
   `s_data`, and NVCSI reads `cil_settletime` and `discontinuous_clk` from its node. The refclk is declared
   as a 27 MHz `fixed-clock`. The warning `of_fixed_clk: probe … failed with error -17` that shows up at
   boot is harmless, because the clock is already registered by `CLK_OF_DECLARE`.
9. Encoder: **`poc-type=2`** is needed. Without it, MediaMTX's HLS muxer crashes with *"unable to
   extract DTS: too many reordered frames"*. We do not use B-frames, so the presentation order
   is the same as the decoding order.
10. **The local UDP hop lost data → artefacts.** `udpsink` → `srt-live-transmit udp://` lost
    **147 datagrams in 3.6 min** (`RcvbufErrors` in `/proc/net/snmp`) in the bursts of each IDR. Each
    one is 1316 B of MPEG-TS lost *before* SRT, which cannot recover them, and MediaMTX
    discarded those broken PES. → A **named FIFO** is used, which loses no data (with a slow
    reader, the writer waits), coming in through stdin: `srt-live-transmit` only accepts `file://con`
    as an input file ("Unsupported source type"). gst's `stdout` will not do, because NVIDIA's libraries
    print there.
11. **The script left GStreamer orphaned.** With `eval gst-launch … &`, `$!` was the PID of a subshell.
    → `eval "exec gst-launch-1.0 …" &` is used.
12. **DAP4 pins (J4-J7) in GPIO mode.** Their `CNF` bit comes out as 1, so the signal never reaches the
    I2S4 even though the pinmux says `i2s4b`. Symptom: `Failed at I2S4 DAP RX sw reset` / `PRE_PMU … -22`,
    with the clock present on the pin. jetson-io does not touch it. There are two culprits:
    - the B01's `gpio_default` `gpio-input`, which `build.sh` resolves by generating the list
      without J4-J7;
    - **the bootloader**, which leaves them as GPIO with the QSPI's pin configuration, before the kernel.
      With the DT already fixed, `CNF` stays at `0xf0`, and on this kernel releasing the GPIO
      (export/unexport) does not set it to 0 either. The QSPI is not flashed. → **`eirl-hdmi-init.service`**
      clears bits 4-7 on every boot: it writes `0xF000` into port J's `MSK_CNF`
      (`0x6000d284`), and only touches those four bits.
13. **I2S4 as master by default.** The machine driver uses `snd_soc_of_parse_daifmt`, which only reads
    `bitclock-master` and `frame-master` (from the codec's side). Nobody reads NVIDIA's DT
    `bitclock-slave` and `frame-slave`. → In `dai-link-1`: `bitclock-master; frame-master;`. As it is
    a codec↔codec link, ASoC inverts the role for the I2S4, which ends up as the slave. Link in
    `s32_le`, because the TC358743 uses 32-bit slots.
14. **Audio ~600 ms late** (spotted by eye). `alsasrc do-timestamp=true` stamped each buffer
    with the *read* time and not the capture time. → Without `do-timestamp`, `alsasrc` computes the
    capture instant itself by subtracting its latency. Measured afterwards: −31 ms.

### Other traps

- **The Mac does not receive incoming UDP**, because of the firewall or macOS's Local Network permission. If the
  Mac acts as the SRT listener, it records 0 bytes. That is why the Jetson is the one that listens and the Mac the one that pulls.
- The Jetson's locale is `es_ES`, with a decimal comma: `printf "%.0f" 30.00` fails.
  `LC_ALL=C` has to be used.
- `tegrastats` only shows `NVENC` if it runs as root.
- `srt-live-transmit` in listener mode counts as `bytes lost` what it reads while nobody is
  connected. It reads it and discards it, so GStreamer does not block. It is normal.
- **With CBR, NVENC does not pad**: with an almost static picture (a still desktop or a paused video) it
  drops to ~3 Mbps even though the target is 6. It does not mean frames are lost: checked, exactly
  30.00 fps come out. Careful when reading the SRT throughput as an fps indicator.
- **The test source with overlaid clocks does not reach a real 30 fps** (~23.6 fps, drawn on the
  CPU at 1080p) and marks them as 30. The stream runs slower than the real clock, MediaMTX warns of
  *"detected drift"* and the player freezes every ~16 s. For long tests, use `-n`.
- **The Mac's WiFi** (−70 dBm, RTT up to 160 ms): the viewer uses `latency=500`. SRT's rule is
  latency ≥ 4×RTT.
- `soctherm: OC ALARM` in `dmesg`: overcurrent alarm of the power supply. The board is powered through the
  jack with J48, but with a 5 V **2 A** supply, which falls short (see Hardware). Use 5 V 4 A.
- **Python 3.6** on the Jetson: there is no `http.server.ThreadingHTTPServer` (`ThreadingMixIn` is used) nor
  `subprocess.run(capture_output=…)`. There is no `curl` either.
- To touch a GPIO or the pinmux without the driver: Tegra's GPIO registers through `/dev/mem`
  (`0x6000d000` + bank·0x100 + port·4; `CNF` +0x00, `OE` +0x10, `OUT` +0x20, `IN` +0x30; PZ0 =
  bank 6, port 1 → `0x6000d604/614/624/634`, bit 0).
- Each `MW_ACK_DONE syncpoint time out` warning when **stopping** a capture corresponds to the last buffer.
  It has not shown up in continuous streaming.

## DSLR diagnosis

The same happens with two different HDMI cables and with the 1080p30 and 480p-only (27 MHz) EDIDs:
- `SYS_STATUS = 0x0f`: there are +5 V, TMDS, PHY PLL and SCDT. **Stable sync (0x80) and HDMI mode (0x10) are missing**.
- The vertical measurement is correct: `V_SIZE = 1125` and `FV_CNT = 333`, that is, the camera sends
  1080p30, the EDID's preferred mode.
- The horizontal one is fixed garbage: `DE_WIDTH_H = 2` and `H_SIZE = 40`. `DE_WIDTH_V` changes on every
  read, and the ISR warns *"DE size or position has changed"* every 500 ms.
- The camera goes into auto power-off after a few minutes and it cannot be disabled.

**With the MacBook's HDMI output, the same module and the same cable, everything works**, so the
problem is in the camera's HDMI output or in its compatibility with this receiver. Possible next
steps:
- Look at the camera's HDMI menu: output resolution, "HDMI info display" and video mode.
- Try the camera on a monitor.
- Put an HDMI splitter or converter that regenerates the signal in front.

## Board state: applied changes and how to undo them

| Where | Change | Undo |
|---|---|---|
| Jetson engine names (Oct 13, `refactor/eirl-engine`) | `hdmi2srt` becomes `eirl-engine`: `eirl-engine.service` and `eirl-hdmi-init.service` (enabled), code in `/usr/lib/eirl-encoder/engine/` (with the EDIDs) and `/usr/local/bin/eirl-enginectl`, `/etc/default/eirl-engine`, `/etc/eirl-encoder/engine.env`, `/run/eirl-engine/` and `/var/lib/eirl-engine/`. The old ones stay: `hdmi2srt.service` and `hdmi-init.service` (disabled), `~/hdmi2srt/`, `/etc/default/hdmi2srt`, `/etc/eirl-encoder/hdmi2srt.env`. The rows below from before Oct 13 already use the new names | No longer supported: the migration script was removed with the Python engine on Oct 14 |
| Jetson engine (Oct 14) | The Python engine is retired: `tools/deploy-engine.sh` installs only `eirl-engine-go`, `eirl-engine-start` and `eirl-enginectl`, and removes the former Python engine's files from `/usr/lib/eirl-encoder/engine/` together with their `.bak-*` copies. The rows below that mention those files and copies are history | Install an older release |
| Mac `~/.ssh/config` | Created, with `Host jetson` | `rm ~/.ssh/config` |
| Jetson `/etc/sudoers` | The temporary `<user> ALL=(ALL:ALL) NOPASSWD: ALL` was removed on Oct 8 (copy `/etc/sudoers.bak-20261008`) and **the maintainer restored it on Oct 9** in a separate file, `/etc/sudoers.d/90-<user>-nopasswd`, so that deployments do not depend on it. Separately, `eirl-encoderd`'s narrow rule (`/etc/sudoers.d/eirl-encoder`) | `sudo rm /etc/sudoers.d/90-<user>-nopasswd` |
| Jetson apt | `libssl-dev`, `tcl` (+`tcl8.6`), `v4l-utils` (+`libv4l2rds0`) | `sudo apt-get remove …` |
| Jetson `/usr/local` | SRT 1.5.7 built by hand (sources in `~/build/srt`). Since the app package carries its own SRT, its `/usr/local/bin/srt-live-transmit` is the package's (RUNPATH to `/usr/lib/eirl-encoder/lib`); the hand-built `libsrt` in `/usr/local/lib` and the other SRT tools stay, unused by the engine | `cd ~/build/srt/_build && grep -v '^/usr/local/bin/srt-live-transmit$' install_manifest.txt \| sudo xargs rm -f && sudo ldconfig` (keeps the package's `srt-live-transmit`) |
| Jetson `/boot` | DTB `…-b00-tc358743.dtb` (video at 972 Mbps + I2S4 audio; copies: `….bak-594mbps` and `….bak-video-only`), `LABEL tc358743`, `DEFAULT tc358743`. Also DTB `…-b00-tc358743-rst.dtb` (with the reset pin), without an entry: `LABEL tc358743-rst` removed on Oct 8 | `sudo cp /boot/extlinux/extlinux.conf.bak-20261005 /boot/extlinux/extlinux.conf` and delete the `*tc358743*` DTBs (intermediate copies: `extlinux.conf.bak-20261007`, `…-20261007-rst` and `…-20261008`, the latter with the reset entry) |
| Jetson modules | `/lib/modules/4.9.337-tegra/extra/tc358743.ko` (with hardware reset) | `sudo rm …/extra/tc358743.ko && sudo depmod -a` |
| Jetson `/usr/lib/eirl-encoder/engine/` | Script, EDID, `latency/` and `backup-20261007/` (module, DTB, extlinux and script from before the hardware reset) | `rm -rf /usr/lib/eirl-encoder/engine` |
| Jetson systemd | `/usr/lib/eirl-encoder/engine/eirl-hdmi-init.sh` + `/etc/systemd/system/eirl-hdmi-init.service`, and `/etc/systemd/system/eirl-engine.service` + `/etc/default/eirl-engine` (both enabled) | `sudo systemctl disable --now eirl-engine eirl-hdmi-init && sudo rm /usr/lib/eirl-encoder/engine/eirl-hdmi-init.sh /etc/systemd/system/{eirl-hdmi-init,eirl-engine}.service /etc/default/eirl-engine` |
| Jetson boot and services (Oct 8, to slim it down) | `systemctl set-default multi-user.target` (no desktop: −200 MB of RAM) and `disable --now` of `docker`, `docker.socket`, `containerd`, `bluetooth`, `ModemManager`, `whoopsie`, `kerneloops`, `bolt`, `nvargus-daemon` (Argus/ISP cameras; v4l2 is used here), `rpcbind`(+`.socket`), `gpsd.socket`, `snapd.socket`, `udisks2`, `accounts-daemon` and the `apt-daily`, `apt-daily-upgrade`, `motd-news` timers. Kept: ssh, serial/USB consoles, USB device mode, avahi, NetworkManager and the fan | `sudo systemctl set-default graphical.target` and `sudo systemctl enable --now <unit>` |
| Jetson `/usr/lib/eirl-encoder/engine/`, systemd (Oct 8) | Script with WebRTC preview (`LIVE`, since 16:45), level meter, rehearsal mode and validation; `RuntimeDirectory=eirl-engine` in the unit. Copies: `/usr/lib/eirl-encoder/engine/eirl-engine.sh.bak-20261008` (before the app), `/usr/lib/eirl-encoder/engine/eirl-engine.sh.bak-20261008-jpeg` (with the JPEG preview), `/etc/systemd/system/eirl-engine.service.bak-20261008` | Restore the copies (`mv`) and `daemon-reload` |
| Jetson `eirl-encoderd` (Oct 8) | User `eirl-encoder`, `/etc/eirl-encoder/` (destination migrated from `/etc/default/eirl-engine`, where those keys are left commented out; copy `/etc/default/eirl-engine.bak-20261008-012355`), `/usr/local/bin/eirl-encoderd`, unit `eirl-encoderd.service` (port 80), drop-in `eirl-engine.service.d/eirl-encoder.conf`, `/etc/sudoers.d/eirl-encoder`, `/etc/tmpfiles.d/eirl-encoder.conf` | See [`MANAGER.md` §11](MANAGER.md#11-install-operate-and-undo) |
| Jetson SRTLA (Oct 9) | `/usr/lib/eirl-encoder/engine/engine-run.py` (copy of the previous script: `/usr/lib/eirl-encoder/engine/eirl-engine.sh.bak-20261009-presrtla`); `install.sh` puts `/usr/local/bin/srtla_send`, `/etc/NetworkManager/dispatcher.d/90-eirl-uplinks` (tables 10000+ifindex, rules 999 and 1000+ifindex, `rp_filter=2` on the uplinks) and `/run/eirl-uplinks` (tmpfiles) | See [`SRTLA.md` §8](SRTLA.md#8-install-and-undo) |
| Jetson boot splash (Oct 12) | `/usr/local/bin/eirl-splash`, 5 `eirl-splash*.service` units (enabled), `LABEL eirl-splash` as `DEFAULT` and `TIMEOUT 10` in extlinux (copy `extlinux.conf.bak-20261012-042546-splash`; previous DEFAULT and TIMEOUT in `/var/lib/eirl-splash/extlinux.prev`). The `tc358743` entry is not touched | `sudo ~/eirl-splash-20261012/install.sh --undo` (or the `splash/install.sh` of any package); see [`SPLASH.md` §4](SPLASH.md#4-build-install-and-undo) |
| Jetson `~/srtla-spike/` (Oct 9) | SRTLA tests over loopback: srtla built, harness and a test app on :8088 (stopped) | `rm -rf ~/srtla-spike` |
| Jetson USB sources (Oct 12) | `/usr/lib/eirl-encoder/engine/eirl-engine.sh` and `engine-run.py` from MR !5 (copies `…bak-20261012-sources`; the one from before cloud-link, `eirl-engine.sh.bak-20261012-cloud`); app `eirl-encoderd` with sources and cloud-link; `/etc/udev/rules.d/70-eirl-capture.rules` (no autosuspend and `PULSE_IGNORE` on the capture cards' audio); keys `SOURCE`, `VIDEO_*` and `AUDIO_SOURCE` in `/etc/eirl-encoder/engine.env` | HDMI source from the app; `sudo rm /etc/udev/rules.d/70-eirl-capture.rules && sudo udevadm control --reload`; restore the copies with `mv` |
| Jetson mixer (Oct 12) | **eirl-engine is the mixer** (the script's default mode; `/etc/default/eirl-engine` without `MIXER`, copy `…bak-20261012-mixer`); `/usr/lib/eirl-encoder/engine/` with `engine_mixer.py` and `eirl-enginectl` (copies of `eirl-engine.sh` and `engine-run.py` in `…bak-20261012-mixer`); `eirl-engine.service` **enabled** again and with the `input` group (touch; copy of the unit in `…bak-20261012-mixer`); `eirl-hdmi-init.service` without `systemd-udev-settle` (a hung USB device delayed the whole boot: 42 s → 9.9 s; copy `…bak-20261012-mixer`); `eirl-encoderd` from the mixer branch (API `/mixer`). The F0 demo (`eirl-mixer-demo`) is removed. **F3 (Oct 12):** `/usr/lib/eirl-encoder/engine/engine_output.py` and the hot-output scripts (F2 copies in `…bak-20261012-f3`), and `eirl-encoderd` with `ready`/`outputState` | `MIXER=0` in `/etc/default/eirl-engine` and `sudo systemctl restart eirl-engine` (a single source); the units, by restoring their copies and `daemon-reload` (see [`MIXER.md` §6](MIXER.md#6-install-and-undo)) |
| Jetson journal (Oct 12) | Persistent: `/var/log/journal` and `/etc/systemd/journald.conf.d/90-eirl-persistent.conf` (`SystemMaxUse=200M`, applied when journald or the board restarts). Before, it was volatile and after the Oct 12 hang no trace was left | `sudo rm /etc/systemd/journald.conf.d/90-eirl-persistent.conf && sudo rm -rf /var/log/journal` and reboot |
| Jetson USB hub (Oct 13, F5) | The rule `/etc/udev/rules.d/70-eirl-capture.rules` gives the board's hub (Realtek RTS5411, `0bda:5411` and `0bda:0411`) the `eirl-usbhub` group and mode 0664. `install.sh` creates the group and applies it to the already enumerated nodes, and the `eirl-engine` drop-in gives it only to the engine, with `NoNewPrivileges=yes`. It is used to cut the power of a hung capture card's port, which switches the whole hub off for a few seconds (MIXER.md §16) | Remove the last rule from the file, `sudo udevadm control --reload` and `sudo chgrp root` on the hub's two nodes in `/dev/bus/usb` |
| Jetson CPU saving (Oct 13 16:02, `perf/cpu`) | App and engine from `main` with the CPU saving (MIXER.md §17): the board's HDMI with `nvv4l2camerasrc` and the web preview stopped while nobody is watching. Copies of the previous engine in `/usr/lib/eirl-encoder/engine/*.bak-20261013-1602`. Meters and their logs in an `~/eirl-watch-*` folder | `MIXER_HDMI_CAPTURE=v4l2` in `/etc/default/eirl-engine` goes back to `v4l2src` (restarts eirl-engine: needs an agreed slot). `rm -rf ~/eirl-watch-*` |
| Jetson base system (Oct 13, [`BASE.md`](BASE.md)) | B1 group A: 18 services removed (`base/units-b1a.txt`: masked, except `nvweston` and `nvmemwarning`, which are disabled) and `loginctl enable-linger <engine-user>`. Previous state in `/var/lib/eirl-base/b1a.prev`. Recipe and meter in `~/eirl-base/` | `sudo ~/eirl-base/base/slim.sh --undo b1a` |
| Jetson logging (Oct 13, B2) | `rsyslog` masked and `/etc/systemd/journald.conf.d/90-eirl-nosyslog.conf` (`ForwardToSyslog=no`): the persistent journal is the only log | `sudo ~/eirl-base/base/slim.sh --undo b2` |
| Jetson `alsa-restore` (Oct 13 21:15, B1 group B) | Masked (`base/units-b1b-alsa.txt`): the engine sets the capture routing and the headphones' `IEC958` itself. The mixers start the same as with it | `sudo ~/eirl-base/base/slim.sh --undo b1b-alsa` |
| Jetson QSPI (Oct 13, decided on Oct 13) | `nv-l4t-bootloader-config` **masked** (`base/units-qspi.txt`): it failed on every boot because of a race with udev and, if it stopped failing, it could flash the QSPI | `sudo ~/eirl-base/base/slim.sh --undo qspi` |
| Jetson purged packages (Oct 13, B3, [`BASE.md`](BASE.md) §10) | NVIDIA SDK (CUDA, cuDNN, TensorRT, VPI, VisionWorks, OpenCV, `nvidia-l4t-graphics-demos` and `-jetson-multimedia-api`), docker/containerd/snapd, desktop applications and fonts/debugging (544 packages), and the desktop (`desktop`, 21:11: 413 packages, with PulseAudio, gvfs, packagekit, Xorg and anacron). In total, from 2267 to 1310 packages and from 14 to 4.1 GB. Lists in `/var/lib/eirl-base/<group>.purged` | `sudo ~/eirl-base/base/purge.sh --undo <group>` (sdk, docker, apps, extra, desktop; needs network) |
| Jetson apt (Oct 13, B3) | **All `nvidia-l4t-*` on hold** and `base/keep.txt` marked as manual (`purge.sh` sets them when simulating). Previous copy of the package selection, manual, hold and `/etc` in `/var/lib/eirl-base/backup/` | `sudo apt-mark unhold $(apt-mark showhold)`; the ones marked manual, with `apt-mark auto` according to `backup/manual.txt` |
| Jetson `/usr/lib/eirl-encoder/engine/VERSION` (Oct 12) | Which engine is installed (written by `tools/deploy-engine.sh`) | — |
| Jetson `~/mixer-spike/` (Oct 12) | F0 harness as a flat copy and its logs (long test in `logs/soak-20261012-0508/`) | `rm -rf ~/mixer-spike` |
| Jetson network cameras and the board's Wi-Fi (F7 and [`NETWORK_SOURCES.md`](NETWORK_SOURCES.md), Oct 13; [`WIFI.md`](WIFI.md), phases W-A and W-B, Oct 13; updated at 19:50) | App `eirl-encoderd` from `main` with W-B (MR !37; installed at 19:15 from the branch, same code: `version=5e3de5f-dirty`) and engine from `main` 895f90d (`/usr/lib/eirl-encoder/engine/VERSION`; the previous ones, in `/usr/lib/eirl-encoder/engine/*.bak-20261013-*`). **Network cameras (F7)**: `/usr/local/sbin/eirl-netsetup` (with the fix for the `ap0: CTRL-EVENT-SCAN-FAILED` loop, trap 17 of NETWORK_SOURCES.md §3; the previous one, in `eirl-netsetup.bak-20261013-1822`), `/usr/local/bin/mediamtx` (v1.21.1), units `eirl-ap` (enabled: applies the network at boot), `eirl-ap-dhcp` and `eirl-media`; user `eirl-media`; `/etc/eirl-encoder/media-api.key` (0640); sudoers with `systemctl start eirl-ap.service`; `90-eirl-uplinks` excluding the AP; tmpfiles for `/run/eirl-ap`; `iptables` chains `EIRL-AP`, `EIRL-AP-X` and `EIRL-MEDIA`, and no IPv6 on `ap0`. Camera "Test phone" registered (`cam1`); the AP's password in `/etc/eirl-encoder/ap.psk` (0600; a copy in the maintainers' secret store). **The screen's socket**: `/run/eirl-encoder/api.sock` (MANAGER.md §3, "Local socket"). **Wi-Fi client (W-B)**: `/etc/polkit-1/localauthority/50-local.d/eirl-encoder-nm.pkla` (risk accepted on Oct 13, WIFI.md §11) and the NetworkManager profile `eirl-wifi-1` (a test iPhone's hotspot; its password, only in NM's root keyfile). **`network.json` at 19:50** (changed by the web and the screen): reception on, "Create my Wi-Fi" `always` (AP `EnhancedIRL-XXXX` up, channel 1), "Connect to a Wi-Fi" off (`wlan0` disconnected), "over Wi-Fi" on and "by cable" off. `~/f7-test/` was deleted when F7 was closed | Wi-Fi client: switch off "Connect to a Wi-Fi" and forget the network from the web (or `sudo nmcli connection delete eirl-wifi-1`); `sudo rm /etc/polkit-1/localauthority/50-local.d/eirl-encoder-nm.pkla`. AP: switch off "Create my Wi-Fi" from the web or the screen. Only the loop fix: `sudo mv /usr/local/sbin/eirl-netsetup.bak-20261013-1822 /usr/local/sbin/eirl-netsetup`. Everything from F7: remove the camera and switch off "Create my Wi-Fi" (with no cameras and no AP, everything goes down, `EIRL-MEDIA` included) or `sudo systemctl disable --now eirl-ap eirl-media eirl-ap-dhcp` and `sudo iptables -D INPUT -j EIRL-MEDIA; sudo iptables -F EIRL-MEDIA; sudo iptables -X EIRL-MEDIA`; `sudo rm /usr/local/sbin/eirl-netsetup /usr/local/bin/mediamtx /etc/systemd/system/eirl-{ap,ap-dhcp,media}.service && sudo systemctl daemon-reload`; reinstall the app from `main` (`build.sh`) for the sudoers, the dispatcher and tmpfiles; `sudo userdel eirl-media`. Engine: `tools/deploy-engine.sh` from `main` (restarts eirl-engine: needs an agreed slot) |
| Jetson `~/f7-spike/` (Oct 13, F7) | Network sources spike ([`NETWORK_SOURCES.md`](NETWORK_SOURCES.md)): MediaMTX v1.21.1, probe and meters, test clips (~900 MB), logs and the spike AP's password (`ap.psk`; a copy in the maintainers' secret store). The AP (NM profile `eirl-f7-spike`), its `dnsmasq` and MediaMTX were removed when it finished (00:32): nothing is left running or in `/etc` | `rm -rf ~/f7-spike` |
| Jetson power supply (Oct 12) | 5 V 4 A supply with a short cable: 5.3 V at idle and 5.27 V minimum while streaming. With the long cable it dropped to 4.3 V and the board rebooted when waking the capture cards up | — (hardware) |
| Mac | `brew install mediamtx`, Docker image `l4t-r32.7.6-build`, volume `l4t-src` (~3 GB), `~/.cache/l4t-r32.7.6` | `brew uninstall mediamtx; docker rmi l4t-r32.7.6-build; docker volume rm l4t-src; rm -rf ~/.cache/l4t-r32.7.6` |

Already done before the project and not touched by it: on October 4 an `apt upgrade` from 32.7.1 to 32.7.6 was done that **rewrote the QSPI**
(`/opt/ota_package/bl_update_payload.log`: "Update successful"). The board boots fine with it.

## Pending

1. **Audio that does not come back**: wait for it to happen again and see in the journal whether any software
   recovery is enough. If not, fit the hardware reset with a transistor.
2. **Management app** (`eirl-encoderd`, [`MANAGER.md`](MANAGER.md)): v1 done; pending items in its §12
   (real phone, real audio, latency, a dedicated user for the stream, TLS, pairing).
3. Break down the ~550 ms of latency that are not SRT; try a lower `SRT_LATENCY` on the LAN.
4. **DSLR**: see the diagnosis above.
5. ~~5 V 4 A supply~~: fitted on Oct 12 with a short cable (5.3 V at idle). Unblocks the SRTLA tests with modems.
8. **SRTLA** ([`SRTLA.md`](SRTLA.md)): deploy and test with real uplinks.
6. When closing: decide whether `DEFAULT` stays on `tc358743`. Already done: `tc358743-rst` entry removed and
   passwordless sudo removed (Oct 8).
7. **Reboot without a clean shutdown on Oct 8 at 14:56** (a power cut?): without a persistent journal
   the cause is unknown. That afternoon the app showed low supply voltage (4.65 V under load):
   prime suspect, see item 5. Keep an eye on it; if it happens again, enable `Storage=persistent` in journald.
9. **USB sources** ([`SOURCES.md`](SOURCES.md) §5): per-source A/V sync (`avsync_ball.py`) and a
   long test (2 h).
10. **Boot splash** ([`SPLASH.md`](SPLASH.md)): on the board and validated (Oct 12). Two
    minor tests remain (§6): plugging the screen in with the splash running and a real failed boot.
8. **Modes**: 1080p50 tested in a real stream (Oct 9). 720p60/50 are missing (their FIFO goes down from 374 to 249)
   and, for 1080p50, raising the bitrate to ~9-10 Mbps.

## License

Apache License 2.0 ([`LICENSE`](LICENSE)), except `driver/` and `dt/`, which derive from the Linux
kernel and are GPL-2.0-only ([`driver/LICENSE`](driver/LICENSE), [`dt/LICENSE`](dt/LICENSE)).
