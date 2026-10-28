> **Status:** IN MAIN (Oct 13) — steps 1 (spike), 2 (contract, §6) and 3 (implementation) done and signed off: network, receiver, API, web, engine and screen, plus **cameras over cable** (§6.11). Installed and tested on the board with a test phone and while streaming (§7). **RTMP/SRT reception switch** (§6.12): in main and tested on the board on Oct 13. Pending: the board's power supply, before more tests under load, and support for more than one camera
> **Updated:** 2026-10-14

# Network sources: cameras and phones over the encoder's Wi-Fi (F7)

The mixer's F7 ([`MIXER.md`](MIXER.md)): the encoder **receives RTMP and SRT** from wireless cameras
and phones (Moblin, Larix, cameras with RTMP output) connected to an **access point created by the
board itself**, and each stream is one more mixer source, on the touch screen and in the web, just like the
HDMI and the USB ones.

Steps, each one signed off: **1** measurement (this document, §1-§4) · **2** design: the contract before
the code (§5 is the starting proposal) · **3** implementation and test on the board, with
code review and security review.

## 1. Spike setup (without touching eirl-engine or the engine)

```
 phone / camera ──wifi 2.4 GHz──▶ wlan0 (AP, 10.42.0.1/24) ──▶ MediaMTX v1.21.1 (login user)
   RTMP :1935 · SRT :8890                                        │  RTSP only on 127.0.0.1:8554
                                                                 ▼
                       f7probe.py: rtspsrc (TCP) ▶ depay ▶ h264parse/h265parse ▶ nvv4l2decoder (NVDEC)
                                   ▶ nvvidconv ▶ NV12 NVMM 1920×1080 ▶ fakesink  (+ AAC ▶ avdec_aac)
```

- **AP**: NetworkManager profile `eirl-f7-spike` on `wlan0`, `mode ap`, band `bg`, channel 1, WPA2
  (`rsn`/`ccmp`, a random 20-character password, kept in the team's secret store, never in the repo),
  **`ipv4.method manual 10.42.0.1/24` + `never-default`**, `autoconnect no`. Like the recipe
  in `ENCODER_CLOUD.md` §8 (the cloud spike of Oct 11), and not `shared`: `shared` leaves `ip_forward=1` and
  sets up NAT towards the LAN and the modems.
- **DHCP**: a separate `dnsmasq` (`--port=0`, no DNS) bound to `wlan0`, which **announces neither a
  gateway nor DNS** (`--dhcp-option=3 --dhcp-option=6`). The phone stays on the Wi-Fi ("No Internet
  Connection") and keeps reaching the internet over its mobile data: **checked with a test iPhone** (Moblin
  streaming to the board and the phone with internet over LTE at the same time).
- **MediaMTX**: official arm64 binary (static, sha256 checked), without root. RTMP and SRT listen
  **only on 10.42.0.1**; RTSP and the API, **only on 127.0.0.1**; HLS, WebRTC, MoQ, metrics and pprof,
  off (MoQ comes on by default in 1.21). Publishing, only from 10.42.0.0/24; reading, only
  from the board itself.
- **Probe** (`f7probe.py`, Python 3.6 + gi, like the engine at the time): one pipeline per stream and, for each one,
  fps, gaps between frames (p50/p95/max), frames with a gap of more than 2 periods and the spread of
  "arrival − PTS" (the cushion that would be needed to absorb the jitter), measured at the network output
  (after `h264parse`), at NVDEC and after `nvvidconv`. A 3 s watchdog. JPEG snapshots with the decoding
  time for glass-to-glass latency against `latency/clock.html`.
- **Load** (`f7measure.sh`): the board's CPU and per process (all its threads), `actmon` of NVDEC and of the
  VIC, `tegrastats` as root (EMC, VIC, NVDEC/NVENC), power draw and what the mixer reports.
- **Harness** in [`network/spike/`](network/spike/) (like `mixer/f0/`): `f7probe.py`, `f7measure.sh`,
  `f7soak.sh` (the run while streaming: aborts if already streaming and always leaves `output off`), `pub.sh` and
  `mediamtx-spike.yml`. On the board it lives in `~/f7-spike/`.
- **Synthetic senders**: 5-min "phone-like" clips (H.264 High without B-frames, 2 s GOP,
  1080p30 at 6 Mbit/s and 1080p60 at 8 Mbit/s, AAC) published in a loop without re-encoding with
  `ffmpeg -re -c copy` over RTMP from the board itself (they cost ~2-3% of a core each, separately).

## 2. Results

With the mixer running and its three local sources (the board's HDMI 1080p30, Logi MJPEG 1080p60,
MS2109 capture card 1080p30). The local sources changed during the night (the HDMI camera lost
signal for a while), so each case is compared with a baseline taken right before or after.

### Load without streaming

| Case | Board CPU (4 cores) | Receive + decode | MediaMTX | NVDEC | Mixer |
|---|---|---|---|---|---|
| Baseline | 51-57% | — | — | stopped | program 60 fps |
| 1 H.264 1080p30 stream | +~5 points | 15% of a core | 5% | 192 MHz, actmon 37 | same |
| 2 streams (30 + 60 fps) | 63% (baseline 51%) | 37% | 25% | 716 MHz, actmon 186 | program 60 |
| 3 streams (30 + 60 + 30 fps) | **77%** (baseline 57%) | 53% | 20% | 716 MHz, actmon 233 | program 61, but the local sources drop (HDMI 25.5, Logi 54) |
| Real phone (RTMP H.264 1080p30), alone | 60% | 14% | 7% | actmon 33 | same |
| Phone (SRT HEVC) + 3 synthetic | **90%** | 75% | 52% | actmon 168 | **saturated**: program 54 fps, HDMI 21, Logi 50 |

### Load while streaming (Oct 13, 00:22-00:30, to the eu1 test destination over SRTLA)

60 s per case, with the senders restarted in each window. MediaMTX always carries the input of the 3
senders (~10%); `srt-live-transmit` (~6%) is the engine's output.

| Network cameras | Board CPU | Engine | Receive + decode | MediaMTX | NVDEC | Local sources (HDMI 30 / Logi 58 / MS2109 30) | Output |
|---|---|---|---|---|---|---|---|
| 0 | 69% | 206% | — | 10% | stopped | 29.1 / 56.6 / 29.6 | 60/60 fps |
| 1 (1080p30) | 76% | 216% | 15% | 12% | actmon 30 | 30 / 57.5 / 30 ✅ | 60/60 |
| 2 (30 + 60 fps) | 84% | 218% | 40% | 17% | actmon 143 | 28 / 54.5 / 30.5 ⚠️ (in another run, 24 / 47 / 29.7 and the program at 57.8) | 60/60 |
| 3 (30 + 60 + 30) | 85% | 208% | 54% | 20% | actmon 209 | **25 / 49.6** / 29.4 ❌ | 60/60 |

- The network cameras always arrive complete (30/60 fps, p95 between frames of 37-45 ms).
- **NVENC does not suffer** (output at 60/60 in every case) and NVDEC has plenty of headroom.
- What gets lost are frames from the **local sources**: the CPU cannot keep up with everything.

### Latency and jitter with a real phone

iPhone with Moblin, 1080p30, filming `latency/clock.html` on a Mac's screen, which shows the
Jetson's time. Latency = the Jetson's time when decoding the frame − the time read in it (includes ~1-2 frames
of the Mac's screen).

| Mode | Codec | Glass to glass | Arrival − PTS spread | SRT losses |
|---|---|---|---|---|
| SRT, 3000 ms latency (Moblin's default) | H.265 | 3.35 s | 50-130 ms | — |
| SRT 500 ms | H.265 | 0.81 s | 40-200 ms | 7 of 152,000, 1 dropped |
| **SRT 200 ms** | H.265 | **0.55 s** | **~20 ms** | **0** |
| RTMP | H.264 (Moblin does not offer HEVC over RTMP) | 0.35-0.38 s | 120-520 ms | — |

- Without SRT's buffer, ~0.35 s remain: the phone's camera and encoding, Wi-Fi, MediaMTX, the 100 ms of
  `rtspsrc`'s jitterbuffer and NVDEC.
- SRT at 200 ms over this Wi-Fi is clean (RTT 6-7 ms), **but with Moblin's adaptive bitrate it does not go above ~0.5
  Mbps** (trap 14 of §3, seen on Oct 13 with a test phone): the recommendation becomes **500 ms**. RTMP is the fastest but, over TCP, a
  Wi-Fi retransmission blocks what comes behind it: gaps of up to 216 ms. The engine would have to absorb it
  with more cushion, and the advantage is lost.
- **Signal**: the phone one metre away arrives at −75/−83 dBm (24-58 Mbit/s PHY). **The board has no
  antennas** (noted by the cloud workstream, Oct 11): with MHF4 antennas on the Intel 8265 it should improve a lot.
  Without them, more than one camera a few metres away is not realistic.

## 3. Traps found

1. **B-frames → bursts at the NVDEC output.** With a stream with B-frames, NVDEC delivers frames in
   bursts (median of 3 ms between frames, p95 of 103-153 ms) even if the network arrives smoothly. Phones do not
   use B-frames; some RTMP cameras do. The engine cannot timestamp these sources by arrival: it has
   to use the PTS (like `CaptureJudge`) or a bigger cushion.
2. **`rtspsrc` does not report when the sender goes away**: only a *warning* ("Could not read from resource") and
   it just stops, with no error or EOS. The engine's 3 s no-frames watchdog is needed (MIXER.md §4).
3. **`rtspsrc` against a path without a sender** gives "No supported authentication protocol was found": it is
   MediaMTX's 404 ("no stream is available"), not a credentials problem.
4. **A source that never arrives blocks the whole pipeline**: an `appsink` behind a closed `valve`
   does not *preroll* and the other sinks are left waiting with one frame. Sinks with `async=false`.
5. **Synthetic senders** (they do not affect the design, but they mislead measurements): `ffmpeg -re -stream_loop` from
   3.4 stops when the clip loops and MediaMTX cuts it after 10 s (`readTimeout`); a TS with long audio
   PES makes `-re` send the video in bursts; and the synthetic MPEG-TS over SRT arrived broken
   (through a pipe and over UDP). The synthetic HEVC only decoded the keyframes, but **the HEVC from a
   real phone over SRT decodes at a clean 30 fps**. Measure with RTMP and long clips, and validate with a phone.
6. **Moblin**: by default, HEVC and an SRT latency of 3000 ms (3.35 s glass to glass). The SRT URL
   carries `?streamid=publish:<path>`; two attempts arrived with an empty streamid and MediaMTX rejects them
   ("invalid stream ID ''"). The web will have to give the full URL and recommend 200 ms.
7. **5 GHz is not possible**: with iwlwifi, all of 5 GHz is *No-IR* (the AP runs ONLY on 2.4 GHz, at 20 MHz). If
   the board also uses the Wi-Fi as a client (ENCODER_CLOUD.md §8), the AP runs on the client's channel and
   they share the radio.
8. **MediaMTX costs more with SRT** than with RTMP (Go; ~5-7% of a core per input stream, more
   for each reader).

On the board with the app (step 3, Oct 13):

9. **`ap0` does not come up if NetworkManager takes it in client mode.** NM switches every new Wi-Fi interface to *managed*,
   and also the AP's when its profile is deactivated (also when reactivating it with another channel or password). With `wlan0`
   also *managed*, the kernel does not let it come up (`#{managed} ≤ 1`: "Device or resource busy"), and
   wpa_supplicant, after two retries, leaves it *unavailable* forever. Fix: `nmcli device set ap0
   managed no`, wait 1 s, `ip link set ap0 down`, `iw dev ap0 set type __ap`, `managed yes`. NM has it
   *disconnected* in ~1 s, and then `nmcli connection up eirl-ap`. Without the 1 s wait, the first attempt
   fails.
10. **A root helper started by the app inherits its sandbox**: `ProtectSystem=strict` left it `/run` and
   `/etc/NetworkManager` read-only (§6.2). It goes through `systemctl start eirl-ap.service`.
11. **MediaMTX skips reloads** that arrive less than 1 s after the previous one, and **does not kick whoever is already publishing**
   when their key changes (§6.3).
12. **The `auto` channel kept moving**: the scan without `ifname wlan0` includes our own AP, which "occupies" its channel,
   and recomputing it on every `apply` reactivated the profile (from 6 to 1 when rotating a key, and the AP did not come back
   up because of trap 9).
13. **`iptables -C` and `-D` do not find a rule with a match** (`-p tcp --dport 1935`, with or without `-m tcp`) on the
   board (iptables 1.6.1, L4T): "No chain/target/match by that name" even though the rule is there. The jumps were
   duplicated on every `apply` and could not be removed. The jumps to our chains go **without a match** (`-i`, `-d` and
   `-o` do work) and the ports, inside the chain, which is written with `iptables-restore` (§6.11; there is a
   test that guards it).
14. **Moblin with SRT at 200 ms does not raise the bitrate** (test phone, Oct 13, over cable and over Wi-Fi). With adaptive
   bitrate, it stays at 0.4-0.5 Mbps even if the network is perfect: RTT 4-13 ms and 0 losses, retransmissions and
   dropped packets, with 270-1,300 Mbps of estimated capacity. At **500 ms** it goes up to 6-7 Mbps in ~15 s, also with 0
   losses. In the spike the latency was measured with 200 ms but not the bitrate. The web's recommendation becomes 500 ms
   (~0.8 s glass to glass, §2).
15. **The board's NVDEC only outputs the IRAP pictures of some H.265 streams arriving over RTSP** ("NVPARSER: HEVC: Seeking is
   not performed on IRAP picture"): 0.5 fps with a 2 s GOP (Oct 13, long test with an x265/ffmpeg clip,
   IDR keyframes and no B-frames). HEVC from an iPhone (VideoToolbox) does not suffer from it: the phone ran at 30 fps in the spike,
   and so did a VideoToolbox stream from a Mac. From the file, the clip decodes at 30 fps; in software
   (`avdec_h265`), too. Setting DTS = PTS does not fix it. Passing it through MPEG-TS and back
   (`h265parse ! mpegtsmux ! tsdemux ! h265parse`) does: 30 fps on both streams, ~9% CPU for the whole path. It goes in
   `engine_net.VIDEO` only for H.265.
16. **A lagging network camera accumulates without limit.** Long test on Oct 13: with streaming, the three local
   sources, the network camera and also the web's WebRTC preview open (almost one more core), the CPU could not keep up. The
   network camera fell further and further behind: the age of its frames went from ~0 to 11 s and then to 50-65 s, with +45 MB of the
   engine's RAM that is not given back, and the screen got to be 3 s behind. When the preview was closed it recovered on its own.
   The source's output queue already drops old frames, so the delay stays in `rtspsrc`'s jitterbuffer, which
   by default drops nothing. Fix: `drop-on-latency=true` on the video and audio `rtspsrc` (what is dropped
   spoils the image until the next keyframe) and, as a safety net, reopening the source if the MINIMUM
   age of its frames in one check (2 s) exceeds `MAX_LAG_NS` (2 s); healthy is -300…50 ms.
   Without streaming and without preview, a reconnection gets back to 30 fps in 5 s and the RAM stays flat.
17. **`ap0: CTRL-EVENT-SCAN-FAILED ret=-95 retry=1`, once a second and forever** (Oct 13): ~3,200 lines
   per hour, the biggest filler of the persistent journal. `eirl-netsetup` does not scan, and NetworkManager does not request it
   every second either: it is wpa_supplicant 2.6's retry. When `ap0` goes to *disconnected* (the recipe of trap 9),
   NM 1.10 requests a scan, as for every Wi-Fi device that enters that state. `wlan0` and `ap0` share a radio:
   if it is busy, `ap0`'s scan is queued and runs when the AP is already sending beacons. At boot it always
   happens, because NM scans with `wlan0` as soon as it sees it. The kernel does not let an AP with beacons scan
   (`-EOPNOTSUPP`), and wpa_supplicant retries every second the "normal" scan that fails while the interface is not
   disconnected: until `ap0` is removed. In the two boots the journal keeps, the first failure came
   4.1-4.2 s after `wlan0`'s scan started, which is how long a full scan of the 8265 takes. When the
   AP is recreated with the radio free, `ap0`'s scan goes first, the activation waits for it to finish (~3 s) and there is no loop: that is
   why it disappeared when switching the cameras off and on. With the AP already up, NM does not scan on `ap0` again
   (`scanning_prohibited`). The scan on entering *disconnected* cannot be turned off in NM 1.10.
   **Fix** (`endScanRetry` in `eirl-netsetup`): with the AP up, a **manual** scan on `ap0` over D-Bus
   (`Scan {"Type": "passive"}`, no channels). wpa_supplicant does not retry a manual scan, and requesting one replaces the
   pending retry (and if NM's is still queued, when it fails it is no longer retried). It fails once, without leaving the channel
   or cutting off the cameras (wpa_supplicant 2.6 never asks to scan "as an AP", `NL80211_SCAN_FLAG_AP`), and that is the end of it.
   `"active"` does not work: it marks the scan as manual, but leaves the retry pending. It runs on every `apply` with the AP
   up, so installing the fix also stops a loop that was already running. Measured on the board on Oct 13
   (`journalctl -u wpa_supplicant --since -10min -o cat | grep -c SCAN-FAILED`): before, 540 every 10 min since
   boot; with the fix, after a reboot, 3 lines without `retry` at 11.5 s (NM's queued one and one for each
   `apply` at boot) and 0 in the following 10 min. The race can be reproduced without rebooting: `sudo rm
   /run/eirl-ap/keyfile.sha256; sudo nmcli device wifi rescan ifname wlan0; sudo systemctl start eirl-ap.service`
   (recreates the AP: cuts its Wi-Fi for ~4 s). If wpa_supplicant or NetworkManager are ever
   updated, check again (today, `wpasupplicant 2:2.6-15ubuntu2.8+esm1` and
   `network-manager 1.10.6-2ubuntu1.4`).

## 4. Spike conclusions

- **The limit is the CPU, not NVDEC.** NVDEC decodes 3 1080p streams (120 fps) with headroom. But the
  engine already uses 1.7-2.1 cores with 3 local sources, and more while streaming. Each network camera costs
  ~20-25% of a core (MediaMTX + RTSP + NVDEC + AAC), and a bit more in the engine, because each frame goes
  through Python (appsink → appsrc) like the USB ones.
- **While streaming**, **one network camera with the three local ones** fits without losing anything (76%). With two, the
  local ones start losing frames (84%), and with three, clearly. Two network cameras may fit if
  one USB source is removed (the mixer has 4 slots) or when F5 lowers the engine's CPU.
- The path works as is for the engine: local MediaMTX → **RTSP over TCP from 127.0.0.1** →
  `h264parse`/`h265parse` → `nvv4l2decoder` → NV12 NVMM, with H.264 and H.265 and with RTMP and SRT as input.
- **SRT with 200 ms** is the recommended mode for phones (0.55 s, stable). **Corrected on Oct 13: 500 ms** (~0.8 s),
  because with 200 Moblin's adaptive bitrate does not go above ~0.5 Mbps (trap 14 of §3). RTMP, for cameras that
  have nothing else.
- **Antennas**: essential before testing seriously.

## 5. Starting proposal for the design (step 2)

Step 2 will fix the contract (socket, API, configuration) before writing code. This is the starting
point that came out of the spike.

**Network: a single access point, the cloud's.** `ENCODER_CLOUD.md` §8 already designs the device's network
(phase 3 "network and wizard", not implemented): `wlan0` client + `ap0` AP, fixed IP 10.42.0.1/24 with its own
`dnsmasq`, a root helper `eirl-netsetup ap-on|ap-off` with exact sudoers and a `.pkla` for
NetworkManager. F7 uses that AP, not another one. What F7 adds or changes:
- **Cameras mode** for DHCP: no gateway or DNS (checked: the phone streams and keeps its
  data). The cloud's setup/rescue mode keeps its captive DNS.
- **Policy**: with some network camera registered, the AP is on (today D2 keeps it only for
  setup and rescue, unless `accessPoint: "always"`).
- **Its own Wi-Fi password** (generated, in `/etc/eirl-encoder/`), different from the setup code.
  With the cloud's recipe they are the same, and the code is also the portal's password after linking
  (D7): whoever had the Wi-Fi (a camera operator) would have the portal.
- 2.4 GHz, 20 MHz; automatic channel (the Wi-Fi client's if there is one; otherwise, 1/6/11, the least busy).
- **MHF4 antennas** on the Intel 8265 before anything else.
- Firewall on `ap0`: only DHCP, the portal (80), RTMP 1935, SRT 8890 and the WebRTC preview. No SSH.
- `90-eirl-uplinks` excludes the AP explicitly (by AP type), not just because it has no gateway.

**Receiver**: MediaMTX (pinned version and sha256) with its own system user, without root, started and
stopped with the AP. RTMP and SRT only on the AP's IP; RTSP and API only on 127.0.0.1; everything else, off.
One path per camera (`cam1`…) with its key as the credential, **hashed with SHA-256 in its configuration**
(MediaMTX supports it). SRT: `streamid=publish:cam1:cam1:<key>`; RTMP: `rtmp://10.42.0.1/cam1?user=cam1&pass=<key>`
(to be checked with cameras that ask for "server + key" separately). The plain-text keys, only in
`/etc/eirl-encoder/` (to show them in the web), never in logs.

**Engine**: a new `kind` `network` in the catalogue; id `net:<camera>`. An isolated pipeline like the USB ones:
`rtspsrc` TCP to 127.0.0.1 → depay/parse according to the codec (H.264 or H.265) → `nvv4l2decoder` → NV12 NVMM →
appsink. The AAC, as one more audio input (`net:<camera>`, linked to its video by default).
Timestamps by PTS (like `CaptureJudge`), not by arrival (B-frames), with its own cushion of
~200 ms for the Wi-Fi. Without a sender: `no_signal` with "waiting for the camera". A 3 s watchdog **after the
first frame**. Initial limit: **1 network camera** with the local ones (2 if a USB one is hidden or when
F5 lowers the CPU).

**Web and screen**: register cameras (name, recommended protocol), and for each one the full URL
to copy (SRT recommended, with 200 ms; RTMP for those that only have that) and a QR. Network: SSID and
password with a Wi-Fi QR. On the screen: each camera's state (like one more source) and the Wi-Fi
QR if it fits.

**Contracts (always additive)**: socket (`sources[].kind = "network"` and a `network` block per source:
protocol, codec, kbps, SRT RTT/losses); API (`/api/v1/cameras` and `/api/v1/network/ap`); cloud
(`status.network.accessPoint` already exists; the cameras appear as mixer sources; registering them
from the app, later). To be coordinated with the cloud workstream and noted in `ENCODER_CLOUD.md` §5.

**Decided (Oct 13, 00:40)**:
1. **F7 implements the AP layer** of the cloud's phase 3 (`eirl-netsetup`, `ap0`, `dnsmasq`), with its
   recipe; the cloud workstream adds on top the Wi-Fi client, scanning, captive DNS and the wizard.
2. **AP on if any network camera is registered**, in cameras mode. Without cameras, D2 applies.
3. **Its own Wi-Fi password**, generated, different from the setup code.
4. **At most 1 network camera** in the first version.

Pending: buy the MHF4 antennas.

## 6. Contract (step 2, signed off on Oct 13)

Everything is additive: an older engine, app or cloud keep working, just without network cameras.
The new pieces are not activated until the first camera is registered.

```
 web, cloud ──▶ eirl-encoderd (eirl-encoder) ── writes ──▶ /etc/eirl-encoder/{network.json, ap.psk, cameras.json}
                    │ sudo -n systemctl start eirl-ap.service  (oneshot: eirl-netsetup apply; reads those files as DATA)
                    ▼
               eirl-netsetup (root, idempotent) ─┬─ ap0 + NM profile eirl-ap (2.4 GHz, WPA2, 10.42.0.1/24)
                                                ├─ eirl-ap-dhcp.service (dnsmasq: cameras mode, no router or DNS)
                                                ├─ ap0 firewall (EIRL-AP chain)
                                                └─ /run/eirl-media/mediamtx.yml (hashes) + eirl-media.service
 camera ──wifi──▶ ap0 ──RTMP :1935 / SRT :8890──▶ MediaMTX (as user eirl-media) ──RTSP 127.0.0.1:8554──▶ engine
                                                                                  API 127.0.0.1:9997 ──▶ app (state)
```

### 6.1 Configuration (in `/etc/eirl-encoder`, written by the app with `WriteAtomic`: `cameras.json` and `ap.psk` at 0600, `network.json` at 0640)

| File | Content | Rules |
|---|---|---|
| `cameras.json` | `{"v":1,"cameras":[{"id":"cam1","name":"Ana's phone","key":"…","createdAt":"…"}]}` | `id` `^cam[1-8]$`, fixed; `name` 1-32 characters (the same list as the Pause text, without `< > &`); `key` `^[A-Za-z0-9]{20}$`, random (≈119 bits); at most `max` cameras (1 in v1) |
| `network.json` | `{"v":1,"ap":{"ssid":"EnhancedIRL-1A2B","channel":"auto"}}` | `ssid` 1-32 of `[A-Za-z0-9 _.-]`, no space at either end (by default `EnhancedIRL-` + the last 4 of the `hardwareId`, like the cloud's mDNS; written at the app's first start and changed in Settings and the wizard since Oct 14, WIFI.md §14); `channel` `auto` or 1-11 |
| `ap.psk` | The Wi-Fi password | `^[A-Za-z0-9]{20}$`, generated by the app at its first start (before Oct 14, when the first camera was registered); can be changed. Different from the setup code (decision 3 of §5) |

`eirl-netsetup` opens them as root **without following links** (`O_NOFOLLOW`), only if they are regular files of the
`eirl-encoder` user (the directory is 3770 and the engine is in its group: a file the engine plants is not the app's and does not count, and the sticky bit keeps the engine from replacing the app's), with a size cap, and
validates them with the same rules: whatever does not fit is ignored and the AP is not brought up. It never executes or interpolates anything from them
outside those whitelists (the rule of MANAGER.md §5).

### 6.2 `eirl-netsetup` (root, Go binary in `manager/cmd/eirl-netsetup`)

- **A single command, `apply`**, idempotent: compares what the files ask for with what there is and makes them match.
  It leaves its state in `/run/eirl-ap/state.json` (and on stdout):
  `{"state":"on|off|error","mode"?:"cameras","ssid"?,"channel"?,"error"?:"…","at"}`.
- **The app does not run it: it starts `eirl-ap.service`** (oneshot with `ExecStart=eirl-netsetup apply`). Exact
  sudoers: `eirl-encoder ALL=(root) NOPASSWD: /bin/systemctl start eirl-ap.service`. Changed on the board on
  Oct 13: as the app's child it inherited its sandbox (`ProtectSystem=strict`: `/run` and
  `/etc/NetworkManager` read-only, it could not even write its `state.json`) and its CPU and memory limits. The
  unit has its own hardening (`ProtectSystem=full` + writing only to
  `/etc/NetworkManager/system-connections`, `ProtectHome`, `PrivateTmp`, `NoNewPrivileges`). The app sends its
  changes one at a time; even so, a `start` can merge with another one in progress (the one at boot or the one from
  `install.sh`) and return that one's result, which read the files earlier: if the `at` in `state.json` is
  older than the request, the app asks again. If it fails, the reason comes from `state.json`; with the AP in error and
  cameras registered, the app retries every 2 min.
- 🔄 **Since Oct 13 (WIFI.md, phase W-A) the AP no longer depends on the cameras**: it comes up with "Create my Wi-Fi"
  (`wifi.ap = always` in `network.json`), whether there are cameras or not, and in `normal` mode. If it was never chosen, the old behaviour: up
  with some camera and "Over Wi-Fi". What follows describes v1; the new details, in WIFI.md §2 and §3.
- **With some camera** (decision 2 of §5), in cameras mode, in this order:
  1. creates `ap0` (`iw dev wlan0 interface add ap0 type __ap`). At boot, it waits for `wlan0` for up to 30 s. The MAC
     derives from `wlan0`'s permanent one (`ethtool -P`) with the local bit, and never equal to `wlan0`'s current one;
  2. removes its IPv6 (`disable_ipv6`) and sets up the firewall: if that is not possible, it does not continue (and brings the AP down if it was
     up);
  3. prepares it (trap 9 of §3: release it from NetworkManager, bring it down, `__ap`, take it back; before every
     activation, not only when creating it) and brings up the `eirl-ap` profile (`mode ap`, band `bg`, channel, WPA2
     `rsn`/`ccmp`, `manual 10.42.0.1/24`, `never-default`, `ipv6 ignore`, `autoconnect no`);
  4. writes MediaMTX's configuration and starts `eirl-ap-dhcp.service` (dnsmasq as in §1: only `ap0`, no
     router or DNS, 10.42.0.10-99) and `eirl-media.service`.

  **Without cameras**: it removes everything (and deletes `ap0`).
- **`auto` channel**: the Wi-Fi client's if `wlan0` is connected on 2.4 GHz (they share the radio); otherwise, with the AP
  already on, **the one it has** (recomputing it on every `apply` moved it, and reactivating the profile cuts off all the
  cameras); otherwise, the least busy of 1, 6 and 11 in `wlan0`'s scan (`ifname wlan0`: without it our own
  AP is included).
- **Firewall** (chain `EIRL-AP`, `-A INPUT -i ap0 -j EIRL-AP`): DHCP 67/udp, the web 80/tcp, RTMP
  1935/tcp, SRT 8890/udp and the WebRTC preview's UDP range (the app fixes its port range); the rest,
  out (SSH included). `FORWARD` from and to `ap0`, out. `ip_forward` is not touched. The chain is rebuilt in one
  go with `iptables-restore --noflush` (atomic) and the jumps with `iptables -w`. In IPv6 (`ip6tables`), everything on
  `ap0`, out.
- **Board boot**: `eirl-ap.service` (oneshot) runs `apply` at power-on: if there are cameras, AP up.
- **For the cloud (phase 3)**: the same `apply` will decide the setup/rescue mode (captive DNS, D2) when
  the cloud workstream adds it; F7 leaves room for it (`mode` in the state) and does not implement it.
- `90-eirl-uplinks` explicitly excludes `ap*` and any interface in AP mode (`iw dev <if> info` → `type AP`).
- Dry-run mode `EIRL_NETSETUP_DRYRUN=1`, like the dispatcher: prints what it would do (tests without root).

### 6.3 MediaMTX (`eirl-media.service`)

- Official arm64 binary with pinned version and sha256 (today v1.21.1), installed by `install.sh` in
  `/usr/local/bin/mediamtx`. System user `eirl-media`, no shell, no groups; `NoNewPrivileges`,
  `ProtectSystem=strict`, `PrivateTmp`, no capabilities (ports > 1024). `eirl-netsetup` starts and stops it.
- Configuration `/run/eirl-media/mediamtx.yml` (`0640 root:eirl-media`), written by `eirl-netsetup` from
  a fixed template: RTMP `10.42.0.1:1935`, SRT `10.42.0.1:8890`, RTSP and API on `127.0.0.1`; HLS, WebRTC,
  MoQ, metrics, pprof and recording, off; `logLevel: warn`; no `runOn*`.
- **One path per camera** (`cam1`): the user `cam1` publishes with password = its key, **stored as
  `sha256:`** (MediaMTX supports it), only from `10.42.0.0/24`; reading the video, only from `127.0.0.1`; the API,
  only from `127.0.0.1` and with the key in `media-api.key` (user `eirl-app`, hashed). No other path. When the cameras change, `apply` rewrites the file and MediaMTX reloads it on the fly: changing the
  name does not touch the file and does not cut anything (tested on the board on Oct 13).
- 🪤 **MediaMTX skips reloads that arrive less than 1 s after the previous one** (`confwatcher`, `minInterval`
  in v1.21): a registration followed by a new key left the old one in force. `eirl-netsetup` spaces its
  writes at least 1.2 s apart.
- 🪤 **A new key does not kick whoever is already streaming with the old one** (MediaMTX only checks on connect; tested on the
  board): after applying it, the app waits 1.5 s and kicks through the API (`/v3/{rtmp,srt}conns/kick/<id>`) whoever
  is publishing on that path. When they come back, the old key no longer gets in.
- **How a camera connects** (what the web shows):
  - SRT (recommended; on the sender, latency **500 ms**: with 200, Moblin's adaptive bitrate does not go above ~0.5
    Mbps, trap 14 of §3):
    `srt://10.42.0.1:8890?streamid=publish:cam1:cam1:<key>`
  - RTMP: `rtmp://10.42.0.1:1935/cam1?user=cam1&pass=<key>`; for those that ask for server and key
    separately, server `rtmp://10.42.0.1:1935/` and key `cam1?user=cam1&pass=<key>` (the full URL, tested
    on the board with ffmpeg on Oct 13; the two fields, to be checked with a sender that asks for them).

### 6.4 Engine (then `engine_mixer.py`, `engine_audio.py`; today `internal/engine/netcam` and `internal/engine/audio`)

- **Catalogue**: `eirl-encoderd probe -list` adds each camera in `cameras.json` as
  `{"id":"net:cam1","kind":"network","name":"…","node":"rtsp://127.0.0.1:8554/cam1"}` and its audio
  `{"id":"net:cam1","kind":"network","name":"…"}`. The engine accepts it with `^net:cam[1-8]$` and that exact `node`,
  and reads the catalogue again when `cameras.json` changes (as today with `/dev/v4l/by-id`). It goes
  after the USB ones in the slots; with all 4 taken it does not get in (v1 limit). A new name reaches the
  source and its audio without restarting. If `probe -list` fails, nothing is removed and it is retried in 30 s.
- **Without a sender** (the path is not ready in MediaMTX's API, which the engine queries with the key in
  `media-api.key`): it opens nothing; state `no_signal`, detail
  "Esperando a la cámara" (waiting for the camera). Once it is ready, it opens according to the codec (H.264 or H.265):
  `rtspsrc location=… protocols=tcp latency=100` → depay → parse → `nvv4l2decoder` → `nvvidconv` → NV12 NVMM
  1920×1080 → the usual queue (`sink_tail`). Watchdog: the usual one (6 s to the first frame, 3 s afterwards).
- **Audio**: one more input, `net:cam1` (AAC → `avdec_aac`; Opus → `opusdec`), in its own RTSP session
  (only that track), linked by default to its video (`default_link`). It cannot be `AUDIO_SOURCE` in v1.
- **Timing**: the PTS from `rtspsrc` (after its jitterbuffer) if `CaptureJudge` accepts it, and arrival
  otherwise. Plus its own cushion, `MIXER_NET_SYNC` (0-1000 ms, 150 by default; in `/etc/default/eirl-engine`, which the
  unit loads as its environment), which is added to `MIXER_SYNC` on its video (`Engine.net_extra`) **and on its audio**
  (`AudioBus.push`: otherwise, the audio ran 150 ms ahead of its own video): it absorbs the Wi-Fi's stutters
  (§2: up to ~200 ms). It is tuned with the phone.
- The screen treats the camera like any other source (thumbnail, on air, PiP, audio). Since Oct 13 (MIXER.md
  §18) the Wi-Fi is in **Settings** (the header's gear, always visible): the "Network cameras" row with
  the **reception** switch (§6.12: RTMP/SRT and the network source; the board's Wi-Fi, as client and as
  AP, does not follow it), and below it the network (SSID, password and QR to join) and each camera (live or waiting). With
  reception off, the Wi-Fi and its QR remain and a warning says the cameras do not get into the mixer. Before, it was a
  "Wi-Fi" button that only appeared with cameras registered. Since phase W-C of WIFI.md (MIXER.md §19), Settings has two
  tabs: "Network cameras" (reception and each camera) and "Wi-Fi" (the board's Wi-Fi with its QR, whether there are cameras or
  not).
- **Where the screen gets it from** (decided on Oct 13: the screen talks to the **app's local API** over its
  Unix socket, MANAGER.md "local socket" and §6.12; except Power, which still goes through `screen.power`). The engine has a thread,
  `engine_app.NetState`, that never blocks the GLib loop:
  - `GET /network` every 2 s with Settings open and every 10 s closed (the gear's amber dot:
    `accessPoint.enabled` with cameras and `state` `off`/`error`).
  - `GET /network/credentials?qr=rows` only with the panel open (when opening it and every 10 s): the Wi-Fi (`ssid`,
    `password`, `qrRows`), **without the cameras' keys**. When the panel closes, the password leaves memory. Since
    Oct 14 (MIXER.md §21) a camera's URLs with its key are asked for only when its eye is opened on the screen
    (`GET /network/credentials?cameras=1`) and dropped when its page goes.
  - `PUT /network {"enabled"}` when the switch is tapped, one at a time and with a 150 s margin. While it lasts,
    "Switching off…"/"Switching on…"; a `202` says "Saved, but it could not be applied: <reason>"; an error shows
    its `message` as is (e.g. the `409 on_air`).
  - A request or its body is never logged (the future Wi-Fi keyboard will send passwords this way).
- **Fallback**: without `api.sock` (an older app), the panel shows the Wi-Fi from **`/run/eirl-encoder/wifi.json`**
  (`ssid`, `password`, `qr` in rows, `on`; the app still writes it, 0640 `eirl-encoder`) and shows no switch.
  The file bridge agreed on earlier (`enabled`/`seq`/`lastError` in `wifi.json` and `screen.network`
  in the engine's state) was dropped without ever being written in the app.

- **On the screen, everything the web does** (Oct 14, MIXER.md §21): the Network cameras page has, besides the
  reception switch, «Through the encoder's Wi-Fi» (`PUT /network/ap {"enabled"}`) and «By cable» (`PUT /network/lan
  {"enabled"}`), the list of cameras and «Add camera» (`POST /cameras`); each camera opens its own view: rename
  (`PATCH {"name"}`), «New key» (`PATCH {"rotateKey"}`) and «Remove» (`DELETE`), both with a second tap, and its SRT and
  RTMP URLs and the SRT QR behind an eye (`GET /network/credentials?cameras=1`, asked when the eye opens and dropped
  when the view goes). Without cameras and with the reception off, the reception switch shows only to turn it on (the
  app adds a camera only with it on).

### 6.5 Socket (MIXER.md §5) — additive

- `sources[].kind` gains `network`; `format` = `H264`/`H265`; `mode` that of the stream (`1920x1080@30`).
- `audio.sources[].kind` gains `network`.
- The ids `net:cam1`…`net:cam8` are valid in `take`, `secondary`, `hide`, `audio`, `audio_link`, `audio_delay` and
  `audio_mix` (`reSource` and `reAudio` in the app and `RE_AUDIO_ID` in the engine are widened).
- No new commands. The network details (protocol, kbps, RTT, losses, sender IP) do not go over the socket:
  the app gets them from MediaMTX's API.

### 6.6 Local API (MANAGER.md §3) — additive

| Method and route | What |
|---|---|
| `GET /network` | `{"accessPoint": {"state": "off"\|"starting"\|"on"\|"error", "mode": "cameras"\|null, "ssid", "channel", "band": "2.4", "ip": "10.42.0.1", "clients", "error"?}, "cameras": {"max": 1, "items": [{"id", "name", "createdAt", "connected", "protocol": "srt"\|"rtmp"\|null, "codec": "h264"\|"h265"\|null, "remote": "10.42.0.74"\|null, "kbps", "rttMs", "lossPct"}]}}`. **No secrets**. `503 network_unavailable` without `eirl-netsetup` installed |
| `GET /network/credentials` | `{"wifi": {"ssid", "password", "qr": "WIFI:T:WPA;S:…;P:…;;"}, "cameras": [{"id", "srt", "rtmp", "rtmpServer", "rtmpKey"}]}`. `Cache-Control: no-store`. Only when the user presses "Show" |
| `GET /network/qr/wifi.svg` · `GET /cameras/{id}/qr.svg?proto=srt\|rtmp` | The QR (SVG generated in the app, no CDN) |
| `POST /cameras` | `{"name"}` → `201` with the camera and its credentials; applies (`eirl-netsetup apply`: with the first one, AP up). `409 limit` if there are already `max` |
| `PATCH /cameras/{id}` | `{"name"?, "rotateKey"?: true}`. Changing the key disconnects the camera that is streaming with the old one |
| `DELETE /cameras/{id}` | `204`. With the last one, AP down |
| `PUT /network/ap` | `{"ssid"?, "channel"?, "enabled"?, "rotatePassword"?: true}`: applies (the cameras reconnect; with a new name they have to join it). `enabled: false` with the cable off: `409 no_input` (§6.11). A new `ssid` with a camera on air over the Wi-Fi: `409 on_air` (since Oct 14, WIFI.md §14) |
| `PUT /network/lan` | `{"enabled": bool}`: cameras over cable (§6.11). Switching it off with the Wi-Fi off: `409 no_input` |
| `GET /status` | Gains `network` (the same as `GET /network`) |

Mutations with session and CSRF, like everything else. The app's journal redacts `pass=`, `:cam[1-8]:<key>` and the QR's `P:`.
The keys never appear in logs or in `/status`.

### 6.7 Web

A **"Wi-Fi cameras"** card in Configuration: the network's state (SSID, channel, clients), "Show password and
QR", and the camera (at most one): name, state ("Connected · SRT · H.265 · 4.6 Mbit/s · RTT 7 ms" or
"Waiting for the camera"), "How to connect" (recommended SRT URL with "latency 500 ms", RTMP, server and key
separately, and QR), "Change key" and "Remove". In "Sources", the camera is one more thumbnail.

### 6.8 Cloud (ENCODER_CLOUD.md, with the cloud workstream) — additive

- `status.network.accessPoint {on, ssid, clients}` starts being filled in; the device announces the `ap` capability.
- §13.1: `sources[].kind` and `audio.sources[].kind` gain `network`; ids `net:camN` (the backend has to
  accept them instead of discarding them).
- D2 and §8: the AP is also switched on with cameras (cameras mode, without captive DNS) and the Wi-Fi password
  is its own, not the setup code. `eirl-netsetup apply` instead of `ap-on`/`ap-off`.
- Registering cameras from the app: later (not in v1).
- **Done (Oct 13)**: integrated into `ENCODER_CLOUD.md` by the cloud workstream (D2, §5, §8, §13.1 and
  §13.2). The cloud backend already accepts the `ap` capability and validates `status.network.accessPoint`, and discards
  the whole `status.mixer` until F6: with this app the cloud does not need to be touched.

### 6.9 Security (summary for the security review)

- Nothing new listens outside `ap0` and `lo`, except with "Cameras over cable" on (§6.11): then RTMP and SRT
  also on `eth0`, and the `EIRL-MEDIA` firewall drops them on any other interface. The `ap0` firewall
  lets through only what is needed. No forwarding or NAT: a camera does not reach the LAN nor go out through the modems.
- The firewall is set up BEFORE bringing up the AP (if it fails, the AP does not come up or is brought down), and the chain is rebuilt in one
  go (`iptables-restore --noflush`; with separate `-F` and `-A`, `ap0` was left open in between). Jumps with
  `iptables -w`.
- No IPv6 on `ap0` (`disable_ipv6`) and `ip6tables` drops everything on `ap0`: the `fe80::` the kernel sets let
  SSH be reached from the Wi-Fi (sshd listens on `::`; seen in the Oct 13 review).
- Root only in `eirl-ap.service` (`eirl-netsetup apply`, hardened): it reads validated data, no links, fixed templates.
- MediaMTX without privileges, without `runOn*`, only hashes of the keys. Its API, only with the key in
  `media-api.key` (0640: the app and the engine); without it, from localhost only the video can be read (with the API open,
  any process on the board could change addresses, users or `runOn*` on the fly).
- The log redactor (`service.Redact`) also masks `pass=` and the `P:` of the Wi-Fi QR.
- Secrets (key per camera, Wi-Fi password) only in `/etc/eirl-encoder` (0600: not even the engine reads them) and in
  `GET /network/credentials`. The test board's copies are kept in the team's secret store.
- `10.42.0.1` can only be reached from `ap0` or the board itself (chain `EIRL-AP-X`): without it, from the `eth0` LAN
  MediaMTX could be reached with a route to `10.42.0.1` via the Jetson (Linux accepts packets to any of its IPs on
  any interface). When the AP goes down, the firewall is only removed if `ap0` no longer exists.
- dnsmasq (`eirl-ap-dhcp.service`, reachable from the Wi-Fi): as `nobody`, with `ProtectSystem=strict`, it writes
  only to `/run/eirl-ap`, and its capability set is limited to DHCP's.
- When a key is rotated the sender is kicked even if the apply fails halfway.
- The AP does not come up if the `10.42.0.0/24` network is already used by another link (streaming wins). With `wlan0` on channels 12-13,
  the `auto` channel chooses between 1, 6 and 11 (the radio supports two channels at once).

**Remaining risks (security review of Oct 13). Accepted for v1 on Oct 13: the Wi-Fi one, documented and to be
revisited with client isolation (hostapd) when there are several cameras; the engine one, on the test board, and
for a customer device, with a user without sudo:**

- **The Wi-Fi is shared (WPA2-PSK).** Whoever knows its password (it is shown on the screen and in the QR) can see the
  traffic of the other clients: NetworkManager 1.10 does not isolate the AP's clients (that arrived in 1.28) and there is
  no PMF. With a spoofed ARP, or by decrypting the WPA2 handshake, they get a camera's key, which travels in plain text in
  SRT's `streamid` and in RTMP's `pass=`. With it they can publish on that path and, with `overridePublisher`, replace
  the image. They also get the web session, which goes over HTTP, if the web is opened from a phone connected to that
  Wi-Fi. Today: the Wi-Fi password only for your own cameras, change it if it has been shared, and the web from the
  LAN. Options: close the web on `ap0`; client isolation with hostapd instead of NetworkManager; an SRT passphrase
  per path, so that the key does not travel in the `streamid`.
- **The engine decodes third-party video.** Before, it only decoded what came in over HDMI or USB, with physical
  access. It runs as the test board's login user, who has sudo there. A bug in GStreamer 1.14 or libav with a
  malformed stream (from whoever has a camera's key) could end up as root: by writing to `~/.ssh` or to
  `/usr/lib/eirl-encoder/engine`, or with a `systemctl --user` unit. On a customer device, the engine has to run as a
  system user without sudo, with the code in a root-owned directory.
- **Cameras over cable (§6.11, accepted on Oct 13 when choosing the design)**: with the switch on,
  RTMP and SRT are open to the whole `eth0` network, which at a venue may be shared with many more people than
  our WPA2 Wi-Fi. The camera's key travels in plain text (as on the Wi-Fi) and whoever has it can put video into
  that camera's slot (if it is on air, it goes out live). MediaMTX parses that traffic before authenticating
  (it runs without privileges). Mitigations: off by default, the key is changed with a button, and the firewall is
  set up before opening the listener.

### 6.10 Implementation (step 3), in this order

1. **Network**: `eirl-netsetup` (+ dry-run mode and tests), `eirl-ap.service`, `eirl-ap-dhcp.service`, firewall,
   `90-eirl-uplinks`, `install.sh`/sudoers/tmpfiles; `GET/PUT /network`.
2. **Receiver**: MediaMTX (`build/` with version and sha256), `eirl-media.service`, `cameras.json`, cameras
   API, credentials and QR, web card.
3. **Engine**: `kind` `network` (catalogue in Go, `discover`/`net_desc`/audio in the then Python engine), ids, `MIXER_NET_SYNC`,
   screen (thumbnail and Wi-Fi panel). Rebased on the mixer's F5.
4. **Board**: install in an agreed board slot (the engine restarts), tests with the phone (SRT and RTMP,
   reconnection, cutting the Wi-Fi, changing the key on the fly), a 30-min test while streaming, code review and
   security review, documentation (MIXER §5, MANAGER §3/§5/§6, SRTLA, README), MR.

### 6.11 Cameras over cable (`eth0`) — signed off on Oct 13

A camera (or a phone) **on the same network as the Jetson's `eth0`** (same switch or router, at home or at the
venue) streams to `eth0`'s IP. The engine does not change: it reads from MediaMTX over `127.0.0.1` and does not care how
the camera got in. Decisions (Oct 13): listen on `0.0.0.0` with a per-interface firewall, **only with
the switch on**; two switches, **over Wi-Fi** (on by default) and **over cable** (off
by default); a **direct cable** camera–Jetson without a router is **out** (the Jetson would have to serve DHCP on
`eth0`, and plugged into a venue's network it would hand out addresses there; if needed, a separate port with a
USB-Ethernet adapter, like the Wi-Fi).

- **Configuration**: `network.json` gains `ap.enabled` (default `true`: a file without the field means the Wi-Fi
  on, as until now) and `lan.enabled` (default `false`):
  `{"v":1,"ap":{"ssid":"…","channel":"auto","enabled":true},"lan":{"enabled":false}}`. Both off is not
  allowed (the cameras would have no way in); the API answers `409 no_input`.
- **Only `eth0`, by name.** A USB-Ethernet adapter is usually a phone sharing its data (an SRTLA
  link): it never gets in. `eth0`'s IPs are set by whoever runs that network (the router's DHCP); the board does not touch
  routes, the default route, `eth0`'s DHCP nor SRTLA.
- **MediaMTX** only supports one address per protocol: RTMP and SRT listen on `10.42.0.1` (Wi-Fi only, as
  until now) or on **`0.0.0.0`** (with the cable; IPv4: it opens nothing on IPv6). RTSP and the API stay on
  `127.0.0.1`; reading the video, only from `127.0.0.1`. With the cable, publishing is no longer limited to `10.42.0.0/24`:
  the network barrier is the per-interface firewall, and the access barrier, the camera's key (a camera may
  be on another, routed subnet at the venue).
- **`EIRL-MEDIA` firewall** (whenever MediaMTX runs, also with Wi-Fi only): `INPUT -j EIRL-MEDIA` (no
  match: trap 13 of §3); the chain lets through what arrives on `lo`, `ap0` and, with the cable, `eth0`, and from
  anything else **drops 1935/tcp and 8890/udp** (the Wi-Fi client `wlan0`, SRTLA's USB modems and phones, docker). It only
  filters: what arrives on `ap0` is still decided by `EIRL-AP`, and `10.42.0.1` can only be reached from `ap0`
  (`EIRL-AP-X`). It is
  rebuilt in one go (`iptables-restore --noflush`) **before** changing MediaMTX's listener: when switching the
  cable on, first the chain with `eth0` and then `0.0.0.0`; when switching it off, first the chain without `eth0` (it immediately cuts
  whoever is streaming over cable) and then `10.42.0.1`. If the chain cannot be set up, MediaMTX does not move to
  `0.0.0.0` (and the cable stays in `error`). It is only removed when MediaMTX stops.
- **`eirl-netsetup apply`** with cameras: the Wi-Fi comes up or goes down according to `ap.enabled` (as until now); MediaMTX runs
  if the Wi-Fi is up **or** the cable is on (if the Wi-Fi fails, the cable ones carry on); `eirl-ap-dhcp` goes with
  the Wi-Fi. `state.json` gains `lan`: `"on"`, `"off"` or `"error"`, and `lanError`; it exits with an error if
  either of the two fails (the API answers `202`). Without cameras, everything down as before. If the Wi-Fi does not come up and MediaMTX
  was listening on `0.0.0.0` (the previous cable state), `eth0` leaves the chain and MediaMTX stops (Oct 13 review:
  switching the cable off with the Wi-Fi failing left it open). The chain is only removed if MediaMTX has really stopped.
  `eirl-media.service` has no `[Install]`: it never starts on its own, without the firewall.
- 🪤 **If the Jetson's Wi-Fi client (`wlan0`) is on the same network as `eth0`**, Linux answers ARP for `eth0`'s
  IP also on `wlan0` (`arp_ignore=0`): a camera may end up sending over `wlan0`, and the firewall
  drops it (it fails intermittently). Do not connect `wlan0` to the same network as `eth0`; if needed, `arp_ignore=1` and
  `arp_announce=2`, testing it first with SRTLA (not measured on the board; Oct 13 review).
- **API** (additive): `GET /network` gains `accessPoint.enabled` and
  `lan {"enabled", "state": "off"|"starting"|"on"|"error", "iface": "eth0", "ip": "<board-ip>"|null, "error"?}`
  (`ip`: `eth0`'s first IPv4, or `null` if it has none), and each camera, `via`: `"wifi"` (the Wi-Fi up and the
  sender in `10.42.0.0/24`), `"lan"` or `null` (no sender). `PUT /network/ap` accepts `enabled`.
  New `PUT /network/lan {"enabled": bool}` (without cameras, it is only saved). `GET /network/credentials`: each
  camera gains `lan: {srt, rtmp, rtmpServer, rtmpKey}` with `eth0`'s IP, or `null` (cable off or `eth0` without an
  IP). QR: `/cameras/{id}/qr.svg?proto=srt|rtmp&via=wifi|lan`.
- **Web**: the card becomes **"Network cameras"**, with the two switches ("Over Wi-Fi" and "Over cable"), and
  "How to connect" shows the URL for each path that is on. With the cable, a warning: `eth0`'s IP may change if the
  router hands out another one (reserve it in the router; cameras almost never resolve `jetson.local`).
- **Screen** unchanged: with the Wi-Fi off, the app does not write `wifi.json` and the "Wi-Fi" button does not appear; the cable
  camera is one more source. Only one engine text changes (it ships with its next installation): without MediaMTX, the
  camera says "el receptor de cámaras no responde" (the camera receiver is not responding) instead of "la wifi de las cámaras está apagada" (the cameras' Wi-Fi is off).
- **Cloud**: nothing new (with the Wi-Fi off, `accessPoint.on` is `false`; the cameras are still `net:camN`).

### 6.12 RTMP/SRT reception switch — requested on Oct 13, reviewed with the mixer workstream

It switches off **reception** (MediaMTX and the mixer's network source) and **leaves the board's Wi-Fi as it is**: as
a client (`wlan0`, which may be an SRTLA link) and as an access point (`ap0`, according to "Over Wi-Fi"). The
configuration is kept. With a camera registered, even if nobody is streaming, MediaMTX is listening (~0.2% of a
core at idle) and the camera takes up a mixer slot, whose source keeps retrying to open the RTSP. Before, there was no
way to switch that off without removing the camera and losing its key. (The first version, from the same day, also switched off
the AP: that was not what had been asked for.)

- **Configuration**: `network.json` gains `enabled` at the root, which **is only written when off**:
  `{"v":1,"enabled":false,"ap":{…},"lan":{…}}`. Without the field, on, so older files stay
  the same and one with reception on is also valid for an older app. "Over Wi-Fi" and "over cable" are kept as they are.
- **`eirl-netsetup apply`** with reception off: first MediaMTX and its `EIRL-MEDIA` firewall down (if
  it was listening on `0.0.0.0`, `eth0` is closed immediately); then the Wi-Fi as "Over Wi-Fi" says: the AP and its DHCP
  up, or everything down. The cable stays `off`. It survives a board reboot. **`wlan0` is not touched.** The
  AP's firewall does not change: it opens 1935/8890 on `ap0`, but nobody is listening.
- **Engine, unchanged**: with reception off, the app leaves `cameras-public.json` empty, and the engine (which re-reads
  the catalogue when that file changes) releases each camera's slot: it neither shows it nor queries
  MediaMTX. When switched on, the list comes back and the camera gets into the first free slot (it may be another number, and with
  it another place in the multiview). Everything else is kept by id and comes back on its own: the scene (if it was still the
  main one, it goes out again) and the audio settings (delay, link, mix, gain). Order: the list first and
  then the network. When switching off, the mixer releases the camera before the receiver stops. When switching on, the camera
  waits a few seconds for MediaMTX, as when it is registered. If the list cannot be written, the switch goes back
  to how it was.
- **While streaming with a network camera on air, the app does not switch off**: `409 on_air`. On air means the main source, the
  secondary with PiP or side by side, or its audio playing alone (`single` mode). When it is removed from the catalogue, the engine
  leaves its place **black** (without the no-signal slate, because it no longer has the source) and its audio
  **silent**. Warning in the web is not enough: streams are also started from the screen and from the cloud. The app checks it in
  `mixer.json` (if it cannot be read, `503 mixer_unreadable`; if it does not exist, with the engine stopped, it switches off), with the
  network lock held and right before switching off: if another network change is being applied, the wait can
  last minutes, and meanwhile the camera could be put on air from the screen. When not streaming, the web asks for
  confirmation if the camera is connected, in the program or playing. It also warns if its audio is in the mix
  (`mix` mode): it leaves the mix and the other inputs keep playing, so no 409 is needed.
- **API** (additive): `PUT /network {"enabled": bool}` → `200` with the same as `GET /network` (`202` if it was
  saved but not applied). Switching it on without "Over Wi-Fi" or "Over cable": `409 no_input`. `GET /network` and
  `/status` gain `enabled`. With reception off, MediaMTX is not queried (the cameras show without a sender);
  `accessPoint` keeps its state and its devices, and `lan` shows `off`. If something is still up without being requested (an
  apply that never ran: sudo or systemd failing do not touch its state), the app retries every 2 min,
  just like an error, and the web says so. With reception off, "Over Wi-Fi" and "Over cable" can both be switched
  off (the `409 no_input` of §6.11 only applies with it on); cameras can be renamed, have their key changed or be
  removed; and registering one returns `409 disabled`, because the camera would never get in.
- **Web**: the "Reception" switch goes in the header of the "Network cameras" card. When off, "Over
  Wi-Fi" and "Wi-Fi network" remain; "Over cable", the cameras, "Add" and "How to connect" are hidden, and a note says what
  stays saved.
- **Screen**: `wifi.json` does not change (it stays while the Wi-Fi is up): the "Wi-Fi" button and its QR remain.
  The switch reaches the screen with the screen-header workstream's ⚙ "Settings", which calls this same API
  (`PUT /network`, with the same `409 on_air`) over the app's local socket (MANAGER.md §3, "Local socket").
  `wifi.json` stays as a fallback for an older engine or app.
- **Cloud**: nothing new.
- 🪤 **Going back to an older app with reception off**: it reads `network.json` strictly, so the
  `enabled` field is not valid for it: its `eirl-netsetup` leaves the network down with an error, and the app rebuilds the file with the
  default values on the next network or camera change (the SSID and the channel are lost). Before
  rolling back, switch it on: that way the file does not carry the field.
- **If it is wanted later**: have the camera go to "off" without releasing the slot (the "NO SIGNAL" slate
  instead of black, if it was on air). It needs a new state in the engine, the screen and the web, and with the
  `409 on_air` it is not needed for v1.
- Seen on Oct 13 (base-system workstream): with the AP up, the journal says
  `ap0: CTRL-EVENT-SCAN-FAILED ret=-95` once a second (something scans on the AP's interface). When the
  AP was recreated it disappeared (0 in 2 min). Cause and fix: trap 17 of §3 (an NM scan queued after `wlan0`'s, which
  wpa_supplicant retries every second; it has nothing to do with the switch).

**The first version, the one that also switched off the AP, tested on the board (Oct 13, 17:54-18:01, only the app with
`build.sh`, without touching eirl-engine or streaming)**:
- **Switch off** (API, 0.6 s): no `ap0`, no profile, no DHCP or MediaMTX, no listeners on 1935/8890/8554/9997, no
  chains (neither `iptables` nor `ip6tables`) and no `wifi.json`. `cameras-public.json` empty; `cameras.json`, the key and
  the network, intact. The engine: "<camera> dada de baja: el hueco 2 queda libre" (<camera> removed: slot 2 is free), in video and in audio.
- **Switch on** (5.5 s, because `ap0` is created): everything comes back. MediaMTX, only on `10.42.0.1` and on localhost; the 21+3
  rules; `wifi.json` with `on: true`. `net:cam1` returns to its slot 2 ("el receptor no responde" (the receiver is not responding) for a moment and
  then "Esperando a la cámara" (waiting for the camera)), and the scene keeps it as secondary. The `auto` channel went from 1 to 6: without the AP
  up, it is chosen again.
- **Measurement at idle** (60 s each, no camera connected, output off; engine and system CPU with
  `engine-cpu.sh`, power from the INA3221's `in_power0_input`):

  | | On | Off | On again |
  |---|---|---|---|
  | `POM_5V_IN` (mean / median) | 3782 / 3785 mW | 3736 / 3709 mW | 3622 / 3587 mW |
  | System (% of a core) | 59 | 57 | 62 |
  | Engine | 35% | 34% | 30% |
  | MediaMTX · dnsmasq | 0.2% · 0 | not running | 0.2% · 0 |
  | `ap0` SCAN-FAILED per minute | 55 | 0 | 0 |

  **At idle, the CPU and energy savings are lost in the noise** (±150 mW between identical measurements; the board's
  power supply is not reliable either, see §7): MediaMTX uses 0.2% of a core and the AP's radio without clients,
  less than can be measured this way. The real savings come when a camera streams: decoding it costs ~20-25%
  of a core (§2), and with the switch off it does not get in even if the phone is still configured. It also frees the
  mixer slot and closes the Wi-Fi and the RTMP/SRT ports while they are not in use.

## 7. Implementation (step 3)

### Done (Oct 13, tested on a workstation)

| Piece | Where | Tests |
|---|---|---|
| Files, rules, MediaMTX template (keys as `sha256:`), the AP's profile (keyfile), Wi-Fi URLs and QR | `manager/internal/netcfg` | `go test` (strict: extra fields, ids, names, SSID, channel, that the configuration carries no keys nor `runOn*`) |
| `eirl-netsetup apply` (root) | `manager/cmd/eirl-netsetup` | dry-run mode: without cameras everything down; with one, `ap0` with a local MAC, profile, firewall without SSH, DHCP and MediaMTX; does not follow links nor FIFOs, size cap, **only files owned by `eirl-encoder`**; the password never in the output |
| Units `eirl-ap`, `eirl-ap-dhcp` (dnsmasq only on `ap0`, `--bind-dynamic`, no router or DNS), `eirl-media` (unprivileged MediaMTX); sudoers, tmpfiles, `install.sh`, `build.sh` and `build/mediamtx.sh` (v1.21.1 with sha256) | `manager/deploy`, `build/` | `bash -n`; on the board, in step 3.4 |
| `90-eirl-uplinks` excludes `ap*` and any interface in AP mode | `manager/deploy/90-eirl-uplinks` | `bash -n`; on the board |
| The app's network manager: cameras (add, name, new key, remove), network (SSID, channel, new password), state from MediaMTX's API, `sudo systemctl start eirl-ap.service`, kicking the sender after a new key, the screen's file | `manager/internal/network` | `go test` (permissions 0600/0640, limit, the public list without keys, state of an SRT camera from a fake API) |
| API: `/network`, `/network/credentials`, QR, `/cameras`, `/network/ap`; `network` in `/status`; `accessPoint` and the `ap` capability to the cloud | `manager/internal/api` | `go test` (session, CSRF, 400/404/409, `no-store`, neither `/network` nor `/status` carry keys or password) |
| Catalogue: each camera as a `network` source (`net:camN`, its local RTSP) and its audio, from `cameras-public.json`; `net:camN` ids in the mixer's commands | `manager/internal/sources`, `manager/internal/mixer` | `go test` |
| WebRTC preview with fixed UDP ports (50000-50099) for the AP's firewall | `manager/internal/live` | `go test` |
| "Wi-Fi cameras" card | `manager/web/static` | in Chrome at 390 and 1280 px with the app on a workstation and a fake MediaMTX API: add, "Connected · SRT · H.265 · 4.8 Mbps · RTT 7 ms", "How to connect" with QR, "Wi-Fi network" with password, QR and channel |
| The engine parts that do not need GStreamer: ids, name with accents, the path's state in MediaMTX, pipeline headers, track filter (`select-stream`), decoded size | `manager/internal/engine/netcam` (before Oct 14, `scripts/engine_net.py`) | `go test ./internal/engine/netcam/` |

Changes to the contract of §6 when implementing it (from the cloud workstream's review):
- `cameras.json` and `ap.psk` go at **0600** (not even the engine, which is in the group, reads the keys); for the
  catalogue there is a list without keys, `cameras-public.json` (0640).
- `eirl-netsetup` only accepts files owned by `eirl-encoder`.
- The screen receives the network in `/run/eirl-encoder/wifi.json` (SSID, password and QR in rows), written by
  the app (its unit's `RuntimeDirectory`): the engine neither reads `/etc/eirl-encoder` nor generates QRs.
- `POST /cameras`, `PATCH /cameras/{id}` and `PUT /network/ap` answer `202` if the change was saved but the network
  did not end up as requested (the reason, in `accessPoint.error`).
- New app flags: `-netsetup`, `-cameras-max`, `-ap-state`.

### Tested on the board (Oct 13, 08:27-08:45, without touching eirl-engine)

App installed with `build.sh` from a test tree = `main` + the `manager/` diff of F5 (which was already
installed: so it is not undone) + F7. What came up and was fixed on the spot: traps 9-12 of §3.

- **Without cameras nothing changes**: routes, rules, `ip_forward`, firewall, SRTLA links and listeners,
  the same as before installing (snapshot of the network before and after).
- **Add** (web and API): AP `EnhancedIRL-XXXX` up in 4.5 s from cold (creating `ap0`) and in 0.5-6 s if it already
  exists. The default route still goes through `eth0`, `ip rule` and `/run/eirl-uplinks/available` (only `eth0`)
  unchanged, `ip_forward=0`. MediaMTX as `eirl-media`: RTMP/SRT only on `10.42.0.1`, RTSP/API only on
  `127.0.0.1`. The camera's key is not in plain text either in `mediamtx.yml` or in the NM profile.
  `wifi.json` 0640 `eirl-encoder`.
- **RTMP** with `?user=cam1&pass=<key>` (ffmpeg from the board): connected, H.264, 5.5 Mbps. Wrong key:
  `authentication failed`.
- **SRT** with `streamid=publish:cam1:cam1:<key>` (srt-live-transmit): connected, 5.6 Mbps, RTT 0.6 ms,
  0% losses. Wrong key: rejected.
- **Engine read**: `rtspsrc` to `rtsp://127.0.0.1:8554/cam1` reads; through `10.42.0.1:8554`, it does not.
- **Changing the name** with the camera streaming: same connection, no cut. **New key**: kicks the sender in
  ~2 s and the old one no longer gets in; with the new one it comes back. **Channel** 11 from the API: reactivates in 8 s; going back to
  `auto` leaves it on 11.
- **Catalogue**: `probe -list` (with the engine's group) gives `net:cam1` as a source and as audio; the old engine
  ignores it without errors.
- **Removing the last one** (from the web): everything goes back to how it was (no `ap0`, profile, firewall, MediaMTX,
  DHCP nor `wifi.json`).
- Real web (card with real data), credentials and QR (`200`, `no-store`), `/status` without secrets.
- `iw dev ap0 station dump` as `eirl-encoder`: works without root (still to be seen with a device connected).

### Engine installed and tested on the board (Oct 13 09:48, in a reserved board slot)

F7 app and engine on top of `main` 0419383 (with F5), with `build.sh` and `tools/deploy-engine.sh` (output off).
- Startup without tracebacks. The camera "Test phone" gets into slot 4 as `network`, "Esperando a la cámara" (waiting for the camera), and its
  audio as the `net:cam1` input.
- With a 1080p30 H.264 SRT clip published from the board: live at 30.2 fps (NVDEC), audio live. On air (not
  streaming): program 59 fps, the camera 29.9, Logi 59.3, capture card 30.4; board CPU ~60%.
- The sender goes away: "Esperando a la cámara" in ~2 s (video and audio). It comes back: live in ~7 s.
- Real screen (`screen.jpg`): "Wi-Fi" button with the green dot and the camera in its thumbnail.

### Cameras over cable installed and tested on the board (Oct 13 12:58-13:05, without touching eirl-engine)

App and `eirl-netsetup` with §6.11 (`build.sh`; only `eirl-encoderd` restarts). From a workstation that reaches the
board routed from another subnet (that is why publishing is not limited to `eth0`'s network):
- **On installing, with the cable off, nothing changes** except the `EIRL-MEDIA` chain and its jump (snapshot of the network before
  and after); MediaMTX stays on `10.42.0.1` without restarting; from the workstation, `1935` over `eth0` closed.
- **Switching the cable on** (API, 0.45 s): the chain with `eth0`, and MediaMTX moves on the fly to `0.0.0.0` (without
  restarting). **RTMP** and **SRT** from the workstation to `<board-ip>`: connected (SRT with RTT 12.7 ms) and **live in
  the mixer** (`net:cam1`, 1920x1080@30, H.264). The `eth0` traffic goes through the chain (counters); nothing falls into the
  `DROP`.
- **Switching it off with the camera streaming**: cut off in < 4 s, MediaMTX goes back to `10.42.0.1`, `1935` closed again.
- **Cable only** (Wi-Fi off): no `ap0`, no `EIRL-AP`/`EIRL-AP-X` nor `ip6tables` rules for `ap0`, DHCP stopped,
  no `wifi.json` (the screen does not show the "Wi-Fi" button); MediaMTX on `0.0.0.0`. Wrong key over cable:
  `authentication failed`; the right one, live. Switching the cable off then: `409 no_input`.
- **Switching the Wi-Fi back on** with the camera streaming over cable: AP up in 4 s (channel 1) and the camera **is not
  cut off**. A second `apply` does not duplicate anything (trap 13, found and fixed here).
- Real web: the two switches, "Receiving · eth0 · <board-ip>", "How to connect" with "Over Wi-Fi" and "Over
  cable" and their QRs; switching the cable off from the switch (with its warning).
- MediaMTX with the cable on: `0.0.0.0:1935` and `0.0.0.0:8890`, **IPv4 only** (nothing on `[::]`).
- Adversarial review of the delta (Oct 13): one HIGH (switching the cable off with the Wi-Fi failing left `eth0` open) and
  five lows, fixed with tests (`TestCableOffWithWifiFailing`, `TestMediaMTXQueNoPara`, `via`); the
  ARP trap, documented above (§6.11).
- Left as: Wi-Fi on, **cable off**.

### With a test phone and long tests (Oct 13, 14:21-15:40, in a reserved board slot)

- **Phone (Moblin, H.265)**:
  - Over cable: with the phone on the local Wi-Fi network → `<board-ip>`. Over the Jetson's Wi-Fi: 6.2 Mbps at −74 dBm
    without antennas, 3 packets lost out of 13,000 and recovered, and the web counts "1 device".
  - Moblin with SRT at 200 ms does not go above ~0.5 Mbps; at 500 ms it goes up to 6-7 Mbps in ~15 s (trap 14). The web
    now recommends **500 ms**.
  - Sync fine according to a hand-clap test: the cushion stays at 150 ms.
  - Reconnection: the phone gets back on the Wi-Fi and is live in the mixer in 4 s.
- **x265 H.265 clip** (published over SRT from the board): NVDEC only gave 0.5 fps (trap 15). Fixed with
  the pass through MPEG-TS: 30 fps.
- **15 min streaming** to the test destination, with the network camera on air at 30 fps:
  - Stable output (6 Mbps, RTT ~58 ms), program at ~60 fps, board at ~70%, engine ~196%, RAM almost flat.
  - With the web's WebRTC preview open (almost one more core), the network camera accumulated up to 65 s of delay and
    +45 MB of RAM (trap 16).
  - Without that load, restarting the publisher (reconnection) gets back to 30 fps in 5 s and the RAM stays flat.
- **Fix for trap 16** (`drop-on-latency` and reopening if it is more than 2 s behind), tested under overload.
  HDMI, USB3 and Logi were live, plus the network camera on air, streaming and one core kept busy on purpose:
  network camera ages with a mean of 63-275 ms and a maximum of 1.3 s, flat RAM (171→175 MB), 8-20 fps. After
  ~2.5 min, **the board lost power** (`TEGRA_POWER_ON_RESET`, board at 89-90%; see below). It did not
  happen again with this supply.
- **Board reboot** (15:24): the cameras' Wi-Fi comes up on its own (`eirl-ap.service`); "Over cable" stays off;
  MediaMTX only on `10.42.0.1`; a single `EIRL-MEDIA` chain; the default route and SRTLA still go through `eth0`.
  🔴 But the software reboot did not come back: two boots died ~1 s after Linux started, right when
  waking up the GPU, the HDMI and the USB (reset by the PMIC's watchdog). It came back with a manual cold boot.
  Precedent: on Oct 12 a reboot took ~4 min. Together with the power loss under load and the 5.07-5.09 V at idle (on
  Oct 12 it was 5.3 V with the 4 A supply), **the board's power supply is not enough**. The supply and the
  cable have to be checked before more tests under load.

### Pending

1. The board's **power supply** (above): 5 V 4 A supply and a short cable before more tests under load.
2. With more than one camera (when the maximum goes up): hot reload without cuts, and client isolation
   (hostapd) for the Wi-Fi.
3. MHF4 antennas for the Intel 8265.

## 8. Board state

The spike was cleared away on Oct 13 at 00:32, before the long F5 test at 02:00: AP down and
profile `eirl-f7-spike` deleted, no processes (MediaMTX, `dnsmasq`, probe, senders, clock), `wlan0`
disconnected, SRTLA links, rules and `iptables` as before, `ip_forward=0`, `output off`.

| What remains | Undo |
|---|---|
| `~/f7-spike/` on the board: MediaMTX v1.21.1, `f7probe.py`, `f7measure.sh`, `f7soak.sh`, `pub.sh`, test clips (~900 MB), logs and `ap.psk` (the spike AP's password) | `rm -rf ~/f7-spike` |
| The team's secret store, field `ap_psk`: since version 3 (Oct 13 08:50), the **real** AP's password (`/etc/eirl-encoder/ap.psk`, network `EnhancedIRL-XXXX`); v2 was the spike's | If F7 does not continue, roll the secret back to version 1. If the password is changed from the web, copy it again (`sudo cat /etc/eirl-encoder/ap.psk` piped straight into the secret store, never printed) |
| **F7 app installed** (Oct 13 08:27, with the app's F5 on top): the cameras' AP on with a test camera, "Test phone" (`cam1`). Details and how to undo it, in the "Board state" table of DEVELOPMENT.md | Remove the camera from the web: with the last one, everything goes down |

To set up the spike again, in a reserved board slot (the password comes from the file and is not
printed):

```bash
cd ~/f7-spike
sudo nmcli con add type wifi ifname wlan0 con-name eirl-f7-spike autoconnect no ssid EIRL-F7-SPIKE \
  802-11-wireless.mode ap 802-11-wireless.band bg 802-11-wireless.channel 1 \
  wifi-sec.key-mgmt wpa-psk wifi-sec.proto rsn wifi-sec.pairwise ccmp wifi-sec.group ccmp \
  wifi-sec.psk "$(cat ap.psk)" ipv4.method manual ipv4.addresses 10.42.0.1/24 ipv4.never-default yes \
  ipv6.method ignore
sudo nmcli con up eirl-f7-spike
sudo dnsmasq --conf-file=/dev/null --port=0 --interface=wlan0 --bind-interfaces --except-interface=lo \
  --listen-address=10.42.0.1 --dhcp-range=10.42.0.10,10.42.0.99,255.255.255.0,2h --dhcp-option=3 \
  --dhcp-option=6 --dhcp-authoritative --dhcp-leasefile=$PWD/dnsmasq.leases --pid-file=$PWD/dnsmasq.pid \
  -u <user> -g <user> --log-facility=$PWD/dnsmasq.log --log-dhcp
setsid nohup ./mediamtx mediamtx-spike.yml > mediamtx.log 2>&1 < /dev/null &
./pub.sh cam1 rtmp p264_30.flv &                       # synthetic senders (or a phone)
python3 -u f7probe.py --latency 100 cam1 phone:h265    # probe; snapshot: echo phone > snap
./f7measure.sh 30 label                                # load
```
