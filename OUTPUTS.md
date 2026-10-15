> **Status:** IN PROGRESS — P1-P5 released in v0.5.0 (Oct 15) and tested on the board (a test SD card, §12). Pending: platforms with real keys, a Cloud OBS reading H.265, the SRT listener's firewall, the cloud's side (P6)
> **Updated:** 2026-10-15

# Output protocols and encoding settings

Goal: the encoder streams, besides SRT and SRTLA, over **RTMP**, **RTMPS** and **RIST**, and to the local
network over **SRT in listener mode** and **UDP/RTP**; and the user picks the **video codec (H.264 or H.265)**
and a few encoding settings. Destinations are **saved** (several per device, one active) and each one keeps its
own encoding settings, filled in by **templates** (EnhancedIRL, Twitch, YouTube, Kick, Facebook, custom).

The encoding pipeline does not change shape (NVENC → MPEG-TS): only the last stretch changes, one **sender** per
protocol with the same contract `srt-live-transmit` already has. The output keeps its retries, reasons, secret
masking, adaptive bitrate (ABR) and clean stop.

## 1. Decisions (Oct 15)

| # | Decision |
|---|---|
| D-P1 | First version: RTMP, RTMPS and RIST; SRT listener and UDP for the local network. WHIP is out. |
| D-P2 | RTMP/RTMPS with our own Go sender, `eirl-send`, a separate process, built on MediaMTX's RTMP library (`gortmplib`, MIT) and `mediacommon` (MIT). No ffmpeg, no `rtmpsink` (librtmp). |
| D-P3 | RIST with `ristsender` from librist (BSD-2-Clause), built and pinned like `srtla_send`. |
| D-P4 | One active destination at a time; sending to several platforms is the cloud's job. |
| D-P5 | Saved destinations, one active, switched from the screen without restarting the engine. |
| D-P6 | Templates: EnhancedIRL, Twitch, YouTube, Kick, Facebook and Custom. |
| D-P7 | RTMP/RTMPS (single TCP connection) leaves through the healthiest link and reconnects through the next one; the screen says it does not bond. |
| D-P8 | No RIST receiver on our relays for now: towards EnhancedIRL, SRTLA. |
| D-P9 | The cloud manages only its own destination (`eirl`); the others live on the device. |
| D-C1 | H.265 by default towards EnhancedIRL; H.264 in the platform templates that do not take H.265. |
| D-C2 | Encoding settings are per destination: codec, video/audio bitrate, ABR, output size and frame rate. |
| D-C3 | Minimal settings: codec, bitrates and ABR, output size (program · 720p · 540p) and frame rate (program · half). Profile (H.264 High, H.265 Main), preset, CBR with two passes and the 2 s keyframe interval stay fixed (device defaults in `/etc/default/eirl-engine`). |

## 2. Architecture

```
program ─ tee ─▶ appsink outv/outa ─(C bridge)─▶ output pipeline
  appsrc ov ─▶ [videorate half] ─▶ nvvidconv (scale) ─▶ nvv4l2h264enc | nvv4l2h265enc ─▶ h264parse | h265parse ┐
  appsrc oa ─▶ audioconvert ─▶ AAC ─▶ aacparse ─────────────────────────────────────────────────────────────┴▶ mpegtsmux
        ├─▶ FIFO ─▶ srt-live-transmit ─────────────────────────────▶ SRT (caller or listener)
        ├─▶ FIFO ─▶ srt-live-transmit ─▶ srtla_send ─▶ links ──────▶ SRTLA
        ├─▶ FIFO ─▶ eirl-send rtmp ─────────────────────────────────▶ RTMP / RTMPS (H.265: Enhanced RTMP)
        ├─▶ udpsink 127.0.0.1 ─▶ ristsender ──────────────────────▶ RIST (simple or main profile)
        └─▶ [rtpmp2tpay] ─▶ udpsink host:port ──────────────────────▶ UDP / RTP (unicast or multicast)
```

The pipeline is built **per output session** from the configuration of that moment (it used to be built once per
engine run), so a new destination or codec only needs the next session, never an engine restart.

## 3. Engine configuration (`engine.env`, written by the app)

New whitelisted keys (`manager/internal/engine/config`), all optional. The app always writes all of them (empty
when unused), like the existing ones.

| Key | Values | Meaning |
|---|---|---|
| `OUTPUT_PROTOCOL` | `""` · `srt` · `srtla` · `rtmp` · `rtmps` · `rist` · `udp` | `""` = legacy: derived from `SRT_URL` (srt or srtla) |
| `OUTPUT_URL` | rtmp(s), rist, udp/rtp URL (below) | Destination for every protocol but srt/srtla, which keep `SRT_URL` |
| `OUTPUT_KEY` | `^[A-Za-z0-9._~!$()*+,;=:@%/?&-]{1,512}$` or empty | RTMP stream key (secret) |
| `OUTPUT_PASSPHRASE` | `^[A-Za-z0-9_.~-]{10,79}$` or empty | RIST pre-shared key (secret; AES-128) |
| `OUTPUT_USER` / `OUTPUT_PASSWORD` | `^[A-Za-z0-9_.-]{1,64}$` / `^[A-Za-z0-9_.~-]{1,128}$` or empty | RIST main profile SRP (password secret); both or neither |
| `RIST_PROFILE` | `""` · `simple` · `main` | `""` = main |
| `OUTPUT_LATENCY` | `^[0-9]{0,5}$`, 50-8000 | RIST buffer in ms (`""` = 1000) |
| `OUTPUT_VCODEC` | `""` · `h264` · `h265` | `""` = h264 |
| `OUTPUT_SIZE` | `""` · `1280x720` · `960x540` | `""` = the program's size; never larger than the program |
| `OUTPUT_FPS` | `""` · `full` · `half` | `""` = full (the program's 50/60); half = 25/30 |

URL forms (validated the same way by the app and the engine):

- `rtmp://host[:port]/path`, `rtmps://host[:port]/path` — host is a DNS name, IPv4 or `[IPv6]`; path
  `[A-Za-z0-9._~!$'()*+,;=:@%/-]{1,400}` (the application, e.g. `app`, `live2`, `rtmp/`). The key goes in
  `OUTPUT_KEY`, never in the URL. Default ports 1935 and 443.
- `rist://host:port`.
- `udp://host:port` (MPEG-TS over UDP) and `rtp://host:port` (RTP/MPEG-TS); a multicast address is allowed.

Rules:

- For `srt`/`srtla` the app writes `SRT_URL`… as before and an empty `OUTPUT_URL`. For the others it writes an
  **empty `SRT_URL`**, so an older engine answers `no_destination` instead of streaming somewhere else.
- `OUTPUT_PROTOCOL` must agree with the URL's scheme (`srtla` ↔ `srtla://`, `rtmps` ↔ `rtmps://`…).
- The new keys are read verbatim (no `A=b=` quirk of the old launcher parser): a key may end in `=`.
- The video bitrate keys (`VBITRATE`, `VBITRATE_MIN`, `ABR`, `ABITRATE`) are the active destination's.

## 4. The sender contract

Every protocol has a sender the output session starts and watches:

| Protocol | Sender | Input | Stats for the ABR |
|---|---|---|---|
| srt | `srt-live-transmit` (as before) | FIFO on stdin | its `-pf:json` on fd 3 |
| srtla | `srtla_send` + `srt-live-transmit` (as before) | FIFO | the same |
| rtmp, rtmps | `eirl-send rtmp` (§5) | FIFO on stdin | its JSON on fd 3, same shape |
| rist | `ristsender` (§6) | UDP datagrams to `127.0.0.1:<RIST_LOCAL_PORT>` (6100) | its own JSON, converted by the engine |
| udp | none: `udpsink` at the end of the pipeline | — | none (the encoder's own counter for `sentKbps`) |

- The stats the ABR reads are srt-live-transmit's shape: one JSON object per line, **counters of the last
  interval**: `{"link":{"rtt":<ms>},"send":{"bytes":<n>,"msBuf":<ms>,"packetsDropped":<n>,"mbitRate":<Mbps>}}`.
  The engine writes the last one to `srt-stats.json` (legacy name) for the app, for every protocol that has stats
  (RIST's are converted to this shape first).
- The ABR's latency budget is `SRT_LATENCY` for srt/srtla, `OUTPUT_LATENCY` for rist and 2000 ms for rtmp.
- A sender that exits ends the session; the output retries with its retry plan (§8). Senders get secrets through
  the environment, never argv — except `srt-live-transmit` and `ristsender`, which have no other way (an accepted
  risk, MANAGER.md §12).
- The rtmp sender binds to the source IP of the healthiest available link (D-P7): available links from
  `UPLINKS_FILE` minus the disabled ones and the Wi-Fi without internet (as SRTLA picks them), ranked by
  `links.json`'s health (good > weak > bad) and then cable > Wi-Fi > modem > other USB. With no ranking, no bind
  (the default route).

## 5. `eirl-send` (RTMP and RTMPS)

`manager/cmd/eirl-send`, a Go binary of the app's module (no cgo), installed next to the engine.

```
eirl-send rtmp -url rtmps://host:443/app [-bind <ipv4>] [-stats-fd 3] [-stats-ms 250]
               [-max-queue-ms 3000] [-connect-timeout 10s] [-ca-file <pem>]
env EIRL_SEND_KEY   the stream key (may be empty); never printed
stdin               MPEG-TS (H.264 or H.265 video, AAC audio)
```

- **stderr**: one line per event, `event=<code>` plus `key=value` pairs (values with spaces quoted, Go `%q`):
  `event=connecting host=… port=… tls=true bind=…`, `event=connected`, `event=publishing codec=h265`,
  `event=dropped frames=<n> queue_ms=<n>` (at most every 10 s), `event=failed reason=<reason> detail="…"` (then
  exit 1), `event=eof` (stdin closed: exit 0). It never prints the key, nor a URL with it.
- **Reasons**: `relay_unreachable` (DNS, TCP connect, timeout), `rejected` (the server refused `connect` or
  `publish`: a wrong key, application or codec), `tls_failed`, `clock_not_synced` (the server's certificate is
  not valid yet: the device's clock is behind), `connection_lost` (a write or read failed after connecting),
  `internal`.
- **Stats** on `-stats-fd` every `-stats-ms`: the shape of §4. `rtt` from `TCP_INFO` (`tcpi_rtt`); `msBuf` = media
  time waiting in its queue plus the kernel's unsent bytes at the current rate; `packetsDropped` = frames dropped
  in the interval; `bytes` = bytes written in the interval.
- **Behaviour**: resolve, connect from `-bind` (when given), TLS with SNI against the system roots plus Mozilla's
  roots embedded in the binary (`golang.org/x/crypto/x509roots/fallback`; Ubuntu 18.04's store is old), RTMP
  handshake, `connect`, `publish(key)`. Sends nothing until the first keyframe with its parameter sets (SPS/PPS,
  and VPS for H.265). H.265 goes as Enhanced RTMP (`hvc1`). Timestamps from the TS (90 kHz → ms), from 0. If the
  queue goes over `-max-queue-ms` it drops up to the next keyframe (what OBS does) and reports it. Any write
  error ends it.

As built (`manager/internal/rtmpsend`; tested by `tools/send-test.sh` against MediaMTX in Docker: RTMP and RTMPS, H.264
and H.265, a wrong key, TLS without the CA, a closed port, a certificate valid from two days ahead, and a 3 Mbit link
under a 4 Mbit stream):

- `event=connected` comes once the server accepted `connect` **and** `publish`; `event=publishing` when the first
  keyframe with its headers is written (immediately: the newest group of frames is kept while connecting). The
  engine puts the output on air at `publishing`.
- gortmplib does the handshake, `connect`, `publish` and reads the server; the media messages are built by
  `rtmpsend` itself (gortmplib's H.265 writer sets no keyframe flag, which FFmpeg-based ingests rely on).
- `bytes` on Linux are the bytes the server **acknowledged** in the interval (`TCP_INFO`), smoother than the
  writes; unsent data in the kernel is capped at 256 KB (`TCP_NOTSENT_LOWAT`) so that a backlog builds in its own
  queue, where it is measured and dropped.
- A server that closes within 5 s of accepting the publish, or answers it with an error status, is `rejected`.
  A trailing `/` of the application is dropped (`…/rtmp/` is the application `rtmp`, as OBS does).
- SIGTERM ends it with exit 0 and no event; on stdin's end what is queued gets 2 s to leave.

## 6. RIST (`ristsender`)

`build/rist.sh` builds librist **v0.2.20** (pinned release and sha256, from code.videolan.org) for the board:
**one static `ristsender`** (~0.9 MB, librist with its bundled mbedtls, cJSON and LZ4; only glibc ≤ 2.25), which
the package installs as `/usr/local/bin/ristsender`. `ristreceiver` and `ristsrppasswd` are built for tests only.

```
ristsender -i udp://127.0.0.1:6100 -o 'rist://host:port?buffer=<OUTPUT_LATENCY>[&secret=…&aes-type=128][&username=…&password=…][&miface=<ip>&weight=5][,…]'
           -p <0 simple | 1 main> -S <stats ms> -v 6
```

- The output pipeline sends the TS to `127.0.0.1:6100` in 1316-byte datagrams (`ristsender` reads only
  `udp://`/`rtp://`).
- **Everything comes on stderr**: `<unix time>|<ctx>|[LEVEL] text`, the statistics included, as INFO lines with a
  JSON `sender-stats` object (counters of the last interval). `-v 6` is the lowest level that prints them.
  `manager/internal/engine/output/rist.go` reads them (the real captures are in its `testdata/rist/`): connected =
  «new status is 0» (after «EAP Authentication succeeded» with SRP); a wrong key = «Malformed packet…» or
  «[EAP-SRP] Authentication failed» → `rejected` (an unknown SRP user only repeats «Waiting for EAP
  authentication», counted: 5 in a row → `rejected`); the receiver gone = «timed out» / «new status is 1» →
  `connection_lost` (it reconnects by itself); a host that does not resolve → `relay_unreachable`. A receiver that
  never answers prints nothing: the 10 s timer gives `relay_unreachable`. It exits with status 0 only when it cannot
  start (DNS, a bad URL).
- **ABR sample**: `rtt` = the highest `avg_rtt` of the peers that received; `msBuf` = 1000 × retransmitted bytes /
  sent bytes (0 clean, ~55 at 5 % loss, ~220 at 25 %); `packetsDropped` = `retransmit_skipped + bandwidth_skipped`;
  `bytes` = `sent_bytes`. Converted to §4's shape for `srt-stats.json`.
- **Bonding**: with two links or more, one leg per link (`miface=<source IP>`: a plain `bind()`, no privileges; the
  board's per-source routing takes each leg out through its link) with equal weights, which balances the packets
  (weight 0, librist's default, would duplicate them). A leg that goes away prints the same «timed out» while the
  others carry on: bonded, the session believes the statistics (none for 5 s = lost). Proven in containers; to
  validate on the board.
- **Simple profile**: no encryption nor SRP (refused), and an even port (its RTCP goes to the next one).
- **SRP and older receivers**: librist 0.2.16 changed SRP's wire format; a receiver on 0.2.15 or older needs
  `srp-compat=1` at both ends (not offered yet).

## 7. Encoding settings

| Setting | Values | Default | Fixed |
|---|---|---|---|
| Codec | H.264 · H.265 | per template (§9.2) | — |
| Video bitrate, minimum, ABR | 500-20000 kbps; 200-max; on/off | per template | — |
| Audio bitrate | 64 · 96 · 128 · 160 · 192 · 256 kbps | 128 | AAC-LC 48 kHz stereo |
| Output size | program · 1280x720 · 960x540 | program | scaled by the VIC in the existing `nvvidconv` |
| Frame rate | program (50/60) · half (25/30) | program | the output's C bridge passes one frame of two (`gst.Bridge.SetEvery`) and the appsrc declares the half rate. Not `videorate`: the frames keep the program's timestamps, minutes past the output's segment start, and in drop-only mode it never catches up with them, so it let every frame through (seen on the board: 60 frames into an encoder told 30, twice the bitrate) |
| Profile | — | — | H.264 High (`PROFILE`), H.265 Main |
| Preset, rate control | — | — | `H264_PRESET` (Fast) for both codecs, CBR, two passes (`H264_TWOPASS`) |
| Keyframe | — | — | every 2 s of the *output* frame rate (`GOP`, when set, wins) |

`nvv4l2h265enc` (L4T R32.7.6 source) has `control-rate`, `bitrate` (also live, the same V4L2 control as H.264, so
the ABR works), `iframeinterval`, `idrinterval`, `preset-level`, `maxperf-enable`, `insert-sps-pps`,
`insert-vui`, `EnableTwopassCBR`, `qp-range`; no `poc-type`; B-frames only on Xavier.

## 8. Engine: socket, state and reasons

- **`{"cmd":"output_config"}`** (app → engine): the engine reloads `EIRL_CONF`. If anything but the output keys
  changed (§3 and `SRT_*`, `VBITRATE`, `VBITRATE_MIN`, `ABR`, `ABITRATE`, `OUTPUT`) it answers
  `restart_required` and keeps running as it is; if the file is invalid, `invalid_config`; otherwise it takes the
  new output configuration, rewrites the state file (`CONF_SHA`, `TRANSPORT`), restarts the output session if it
  is on air, publishes and answers `ok`. An older engine answers `unknown_command`: the app restarts it instead.
- **State** `output` gains (optional): `protocol` (`srt|srtla|rtmp|rtmps|rist|udp`), `codec` (`h264|h265`),
  `size` (`"1280x720"`), `fps` (the output's).
- **Reasons** gain `tls_failed` (error: the retry plan's long waits) and `clock_not_synced` (transient: it fixes
  itself once the clock syncs). The cloud maps reasons it does not know to `internal` until it learns them.
- **State file** `TRANSPORT=` carries the protocol.
- **Header**: `LinkInfo()` returns the protocol and, with srtla, the links in use; the screen words it
  («RTMPS», «SRTLA · 3 links»).

## 9. App: saved destinations

### 9.1 Store

`<dir of engine.env>/destinations.json` (0600, the app's only; the engine never reads it):

```json
{"v":1, "active":"d-3f9a1c2b",
 "destinations":[
  {"id":"eirl", "name":"EnhancedIRL", "template":"eirl", "managed":true,
   "protocol":"srtla", "url":"srtla://eu1.relay.enhancedirl.com:5001", "mode":"caller", "latencyMs":2000,
   "streamId":"publish:…", "passphrase":"", "key":"", "ristProfile":"", "username":"", "password":"",
   "videoCodec":"h265", "videoBitrateKbps":6000, "videoMinBitrateKbps":1000, "adaptiveBitrate":true,
   "audioBitrateKbps":128, "outputSize":"", "outputFps":"full"},
  {"id":"d-3f9a1c2b", "name":"Twitch", "template":"twitch", "managed":false, "protocol":"rtmp",
   "url":"rtmp://live.twitch.tv/app", "key":"live_…", "videoCodec":"h264", "videoBitrateKbps":6000, …}]}
```

- At most 16 destinations; names 1-32 characters. Ids: `eirl` for the cloud's, `d-` + 8 hex for the rest.
- **The active destination is what `engine.env` holds**: activating or editing it renders `engine.env` (§3) and
  applies it (§9.4). `Output` (on/off), `EDID_PREFERRED` and the input keys stay global.
- **Migration** (no file yet): linked → an `eirl` destination from the current settings, active, codec H.265;
  otherwise a current `SRT_URL` becomes a `custom` destination named after its protocol, active, codec H.264
  (what it streamed until now); nothing → an empty list.
- **The cloud's destination** (`eirl`, `managed`) is created and updated only by the cloud (`config.set`), with its
  bitrates and, when it says so, `videoCodec`; locally it can be activated but not edited or deleted. The first
  time the cloud sets it, it becomes active; later updates do not change which one is active. Unlinking removes it;
  if it was active, nothing is active (empty destination, output off, as before).

### 9.2 Templates (`manager/internal/config/templates.go`, served by the API)

| Template | Protocols | Server | Codecs | Max video |
|---|---|---|---|---|
| `eirl` | srtla, srt | from the cloud | h265, h264 (default h265) | — |
| `twitch` | rtmp, rtmps | `rtmp://live.twitch.tv/app` | h264 | 6000 |
| `youtube` | rtmps, rtmp | `rtmps://a.rtmps.youtube.com:443/live2` | h264, h265 | 20000 |
| `kick` | rtmps | (per account; the user pastes it) | h264 | 8000 |
| `facebook` | rtmps | `rtmps://live-api-s.facebook.com:443/rtmp/` | h264 | 6000 |
| `custom` | all | — | h264, h265 (default h264) | 20000 |

A template that lists codecs refuses the others (`config.codec_unsupported`), and its maximum bounds the video
bitrate (`config.kbps_range`). Defaults: ABR on, minimum 1000 kbps, bitrate min(6000, max).

### 9.3 API (`/api/v1`, additive)

Public view of a destination (secrets never in clear; `streamId` and `key` masked like the stream ID today):

```jsonc
{ "id": "d-3f9a1c2b", "name": "Twitch", "template": "twitch", "managed": false, "active": true,
  "protocol": "rtmp", "url": "rtmp://live.twitch.tv/app", "urlInvalid": false,
  "mode": "caller", "latencyMs": 2000,                  // srt/srtla; latencyMs is RIST's buffer too
  "streamId": {"set": false}, "passphrase": {"set": false},
  "key": {"set": true, "masked": "live…wXyZ"},
  "ristProfile": "main", "username": "", "password": {"set": false},
  "videoCodec": "h264", "videoBitrateKbps": 6000, "videoMinBitrateKbps": 1000, "adaptiveBitrate": true,
  "audioBitrateKbps": 128, "outputSize": "", "outputFps": "full" }
```

| Route | Body → answer |
|---|---|
| `GET /destinations` | → `{"active": id or null, "managedBy": "local"\|"eirl", "destinations": [view…], "templates": [template…], "pendingRestart": bool}` |
| `POST /destinations` | patch (below) with `template` and `protocol` → 201 view; 409 `too_many` |
| `PUT /destinations/{id}` | patch + `"apply": bool` → view (+ `"restarted"`); 409 `managed` |
| `DELETE /destinations/{id}` | → 204; 409 `active` or `managed` |
| `POST /destinations/{id}/activate` | `{"apply": true}` → the list as in GET (+ `"restarted"`) |
| `GET /destinations/{id}/secrets` | local socket only → `{"streamId","passphrase","key","password"}` in clear, no-store |
| `GET`/`PUT /destination` | as before, on the active destination, plus the view's new fields |

Patch: `name`, `template`, `protocol`, `url`, `mode`, `latencyMs`, `streamId`, `passphrase`, `key`, `password`
(these four with the absent/null/value semantics of today's `Optional`), `ristProfile`, `username`, `videoCodec`,
`videoBitrateKbps`, `videoMinBitrateKbps`, `adaptiveBitrate`, `audioBitrateKbps`, `outputSize`, `outputFps`.
Changing the server keeps the rule of today: the secrets must be given again. A pasted srt/srtla URL with its
query fills the SRT fields as before. The socket's allow list gets the new routes (the screen uses them).

`templates[]`: `{"id","protocols":[…],"url","codecs":[…],"maxVideoKbps","keyRequired":bool}`; the web and the
screen translate the name by its id.

### 9.4 Applying

After saving `engine.env`: if the engine is running the mixer and **only output keys** changed, the app sends
`output_config` (§8) — no restart; the output restarts only if it was on air (a cut of 1-2 s, which the screen and
the web confirm first). `unknown_command` or `restart_required` → the restart of today. `/status` gains
`protocol` and `codec`; `transport` carries the protocol.

## 10. Cloud (device side, additive; the backend follows in its own phase)

- Capabilities gain `rtmp`, `rtmps`, `rist`, `udp`, `h265` and `destinations`.
- The wire configuration accepts an optional `videoCodec` (`h264|h265`) for the `eirl` destination.
- The reported `destination` stays the cloud's, in today's shape, and only when it is the active one (`null`
  otherwise: today's backend refuses the whole report for a destination that is not srt/srtla with its host, port and
  latency). A new `activeDestination` (`kind` `eirl|local`, `protocol`, `codec`) says which one is active; of a local
  one nothing else. New capabilities go after the known ones (the backend keeps the first 16 before filtering).
- Start/stop from the app stream to the **active** destination, whichever it is.
- `ENCODER_CLOUD.md` §5 gets these fields with the cloud session.

## 11. Screen and web

- **Settings › Stream › Destination** becomes a list: each row with name, protocol and server, the active one
  marked; a touch activates (a second touch while on air: the output restarts); «›» opens it. «Add» picks a
  template, then its fields. The cloud's destination is read-only (it can be activated).
- **A destination's page**: protocol (only the template's), server, key or stream ID, passphrase (and RIST
  profile, user and password), latency, then **Video**: codec (only the template's), bitrate, minimum, ABR, size
  and frame rate; audio bitrate. RTMP/RTMPS with more than one link: a note that it does not bond.
- **Header**: the protocol («RTMPS», «RIST», «SRTLA · 3 links»); the status shows the codec («H.265 · 4.2 Mbps»).
- **Web**: the «Destination» card becomes the same list plus a form per protocol; the «SRT» status card becomes
  «Connection». Everything in es/en/fr.

## 12. Tests

- Go tests with fake senders (as `fakeproc_test.go`), the pipeline goldens for each codec, size, rate and sink,
  the config validation (app and engine), the store's migration, the API.
- `eirl-send` against MediaMTX in Docker (RTMP and RTMPS with a test certificate, H.264 and H.265) fed by
  ffmpeg; `ristsender` against `ristreceiver`.
- On the board: each protocol to a test receiver (our relay over SRT/SRTLA/RTMP, a receiver on the LAN for RIST and
  UDP), H.265 through the relay, CPU and fps of each codec at 1080p60.

### 12.1 Measured on the board (Oct 15, a test SD card, program 1920x1080@60 with two USB cameras)

| Destination | Codec, output | Encoder | Engine CPU (one core) | Sender | Read back |
|---|---|---|---|---|---|
| RTMP to MediaMTX (LAN) | H.264 1080p60, 6000 kbps | 60 fps, 6.0 Mbps, RTT 7-14 ms | 91 % | eirl-send 12.5 %, 13 MB | H.264 High 1920x1080@60 + AAC |
| RTMP to MediaMTX (LAN) | H.265 1080p60, 6000 kbps | 60 fps, 6.0 Mbps | 89 % | eirl-send 13 %, 14 MB | HEVC Main 1920x1080@60 (Enhanced RTMP) + AAC |
| RTMP to MediaMTX (LAN) | H.265 720p, half rate, 4000 kbps ABR | 30 fps, on target | 80 % | — | HEVC Main 1280x720@30 |
| SRTLA to eu1 (the cloud's destination) | H.265 1080p60, 5872 kbps | 60 fps, 5.9 Mbps, RTT 58 ms | 86-94 % | — | HEVC Main 1920x1080@60 + AAC read back from the relay (`read:` the same key) |
| RIST to ristreceiver (LAN) | H.265 1080p60, ABR | 60 fps | 102 % | ristsender 7 %, 2 MB | HEVC Main 1920x1080@60 + AAC |
| UDP (LAN) | H.264 720p60 | 60 fps, 6.0 Mbps | 91 % | — | H.264 High 1280x720@60 + AAC |

- **H.265 costs the engine the same as H.264** at 1080p60 (the NVENC does the work): no preset limit was needed.
- **A live destination switch** (RTMP H.264 → UDP H.265 while on air): back on air in 0.93 s, the engine not
  restarted (same PID).
- Found and fixed on the board: half the rate (above, §7), and a hot destination switch that stopped the stream
  when the saved OUTPUT lagged behind what was on air (the app switched the output after `output_config`; now only
  a save that changes OUTPUT does).
- Not tested yet: Twitch, YouTube, Kick and Facebook with real keys; a Cloud OBS reading H.265; RIST bonding over two
  real links; RTMP to our relays (their keys are SRT/SRTLA).

## 13. Phases

| Phase | What | State |
|---|---|---|
| P0 | Measurements on the board | done with the implementation (§12.1) |
| P1 | Contract and pluggable output: config keys, senders, per-session pipeline, `output_config`, destinations store and API | done; tested on the board |
| P2 | Encoding settings: codec, size, frame rate per destination; screen and web | done; tested on the board |
| P3 | RTMP and RTMPS (`eirl-send`) | done; RTMP tested on the board, RTMPS on the Mac; platforms pending |
| P4 | RIST: ristsender built, its log and statistics read from real captures, bonding by source IP | done; one link tested on the board, bonding pending |
| P5 | Local network: SRT listener (already supported) and UDP/RTP | UDP done and tested; the listener's firewall pending |
| P6 | Cloud: backend and the app's panel | after the device |
