> **Status:** IN PROGRESS — implemented (phases 2-4), tested over loopback and **deployed on the board** (Oct 9: a real stream over SRTLA to eu1 with one link). Pending: tests with several real links (they need the 5 V 4 A power supply and modems), §7
> **Updated:** 2026-10-14

# Streaming over SRTLA (bonding several links) from the encoder

Goal: have the encoder stream over several internet connections at once (Ethernet, USB 4G/5G
modems, phones sharing their data) and have the relay join them back into a single SRT stream, just
as Moblin does. If one link drops or degrades, the stream carries on over the others.

> **Since the mixer's F3 (Oct 12, [`MIXER.md` §9](MIXER.md#9-f3-no-rehearsal-mode-hot-switched-output-oct-12)), and with the mixer, which is the default mode:**
> `srtla_send`, its links (with the SIGHUP on change), `srt-live-transmit`, the statistics and the
> adaptive bitrate are started by the engine (`manager/internal/engine/output`) when **going live** and stopped when
> going off air, without restarting the service. The logic is the same as described here.

## 1. What there is today and what shapes the design (inventory of 2026-10-09, read-only)

| Fact | Consequence |
|---|---|
| Streaming: `gst-launch` → FIFO → `srt-live-transmit` (libsrt 1.5.7) as caller or listener. **Fixed** bitrate (CBR, 6 Mbps by default) | SRTLA can be slotted in **without touching the pipeline**: `srt-live-transmit` calls a local `srtla_send` instead of the relay |
| The app rejects `srtla://` on purpose (`config.ParseURL`: "SRTLA is not supported") and the script only accepts `srt://` | Both whitelists have to be opened, in a bounded way |
| **Server side already done**: the 3 production relays (eu1/us1/as1) run `srtla_rec` (irlserver fork) on **5001/udp**, which forwards to MediaMTX :5000 keeping the streamid. The key is protocol-agnostic (`mediamtx-path.ts`: SRTLA → path `<key>`) and the dashboard already gives the URL `srtla://host:5001?streamid=publish:<key>` | **Zero changes in the backend, dashboard and relays.** All the work is on the board |
| The board's network: only `eth0` (1 Gbps, LAN). `rndis0`/`usb0`/`l4tbr0` are the micro-USB's *gadget* mode (they are not links). No Wi-Fi | The extra links have to come through the 4 USB-A ports (one shared USB 3.0 hub; the modems are USB 2.0, plenty of bandwidth) |
| Kernel 4.9: `cdc_ether` and `cdc_ncm` **built in**, `rndis_host` as a module. **No** `ipheth`, `cdc_mbim` or `qmi_wwan`. `option`/`usb_wwan` yes (serial modems). `usb-modeswitch` installed, but with data from **2017** | These work: **HiLink** modems (they show up as Ethernet: `cdc_ether`), **Android** phones with USB tethering (RNDIS or NCM) and 4G routers over cable. **They do not work without building modules**: iPhone (`ipheth`) and "stick" modems in QMI/MBIM mode (they would also need ModemManager, disabled when slimming down the board) |
| `CONFIG_IP_MULTIPLE_TABLES=y`, iproute2 4.15 | There is *policy routing* |
| NetworkManager **1.10.6** active and managing new USB-Ethernet devices (DHCP only); `networkd-dispatcher` also active | NM 1.10 does **not** know how to set source-based routing rules (`ipv4.routing-rules` arrived in 1.18): it has to be done with a *dispatcher* script |
| `rp_filter`: `all=1` (strict), `eth0=2` | With `from <ip>` rules the strict check should pass, but **it has to be verified** in the spike; if not, `rp_filter=2` on the link interfaces |
| Power: 5 V 2 A supply, already at `OC ALARM` and 4.65 V | 🔴 **Blocking**: a 4G modem peaks at 0.5–1 A. Without the **5 V 4 A** supply (barrel + jumper J48) the board will reboot while streaming (see README) |
| gcc 7.5, cmake 3.10, Python 3.6 with `python3-gi` and GStreamer 1.14 through introspection | `srtla_send` (C, just `make`) builds on the board itself or in `build/Dockerfile`. Python+gi allows an encoder controller without new dependencies |
| No passwordless sudo; the app runs as `eirl-encoder` with 3 exact sudo commands | Routing (root) goes into pieces that `install.sh` installs, **not** into the app |

**A precedent that fits almost to the millimetre**: BELABOX was born on a **Jetson Nano with a TC358743
board** (the C779) — the same hardware. Its chain is `belacoder` (GStreamer + libsrt with dynamic
bitrate) → `srtla_send` → modems. We take `srtla_send` and the dynamic bitrate ideas from it,
not the whole distribution.

## 2. Proposed architecture

```
                                     Jetson                                                   Relay (DO)
 ─────────────────────────────────────────────────────────────────────────────────────    ───────────────────
 gst-launch ─FIFO─▶ srt-live-transmit ─SRT caller─▶ srtla_send :<SRTLA_LOCAL_PORT>          srtla_rec :5001
   (unchanged)       srt://127.0.0.1:PORT            (local listener)   │                     │  un-bonds
                     ?streamid=publish:<key>                            ├─ bind eth0  ─────▶  │
                     &latency=2000                                      ├─ bind usb-a ─────▶  ├─▶ MediaMTX :5000
                                                                        └─ bind usb-b ─────▶  │   (usual auth)
                     IP list file: /run/eirl-engine/srtla-ips  ◀── script's watcher (SIGHUP on change)
                                                                ◀── /run/eirl-uplinks/available  ◀── NM dispatcher (root)
```

1. **Transport**: `srtla_send` is one more process of the stream, a child of the engine like
   `srt-live-transmit`. It listens for SRT on a localhost port, opens one socket per source IP
   (`bind()`), spreads the packets across links by window/ACK and forwards what comes back. SRT goes
   **end to end** (handshake, streamid, passphrase, retransmissions and statistics):
   `srt-live-transmit` does not notice there is bonding and its statistics are still valid for the app.
2. **Source-based routing (root, outside the app)**: a NetworkManager *dispatcher* script
   (`/etc/NetworkManager/dispatcher.d/90-eirl-uplinks`), on `up`/`down`/`dhcp4-change` of each
   link interface, creates one table per interface (`default via <gw> dev <if> table <10000+ifindex>`
   + the connected network) and a `from <ip> lookup <table>` rule, and rewrites with an atomic `rename()`
   `/run/eirl-uplinks/available` (`<interface> <ip> <type>` per line). It excludes `lo`, the
   *gadget* mode (`l4tbr0`, `rndis0`, `usb0`, `dummy0`) and any interface without a gateway.
   It is idempotent and does not touch the `main` table (the LAN and access to the app stay the same).
3. **Selection and reload (unprivileged)**: the watcher the script already has (the 2 s HDMI
   loop) computes `available − disabled` (§5, phase 4), and if it changes it rewrites
   `/run/eirl-engine/srtla-ips` and sends `SIGHUP` to **its own child** `srtla_send` (same user: there is
   no need for the dispatcher, which is root, to signal another user's processes). `srtla_send` adds the new
   IPs and closes the ones that are gone without cutting the stream.
4. **Failures**: a link that stops answering for 4 s is left out of the distribution and `srtla_send`
   retries it on its own; if **none** is left for 10 s, `srtla_send` exits → the script's `wait -n` → systemd
   restarts everything (today's behaviour on any failure).

### Why this way and not another

- **BELABOX's `srtla_send`** (C, AGPL-3.0, no dependencies, `make`), with a pinned commit. It is the
  reference sender of the protocol and the one irlserver's `srtla_rec` uses as its counterpart (as does
  Moblin, which implements it on its own). The irlserver fork only documents the receiver. The
  BELABOX README marks it *experimental*: it is measured in the spike, not assumed.
- **No `irl-srt-server` nor anything on the server**: already ruled out and solved on the EnhancedIRL
  ingest side.
- **Not the whole `belacoder`**: it would replace the `gst-launch`+FIFO+`srt-live-transmit` that already works,
  with no documented passphrase, without the statistics JSON the app reads, and it requires building libsrt
  from BELABOX's fork on 18.04. What is worth taking from it is the dynamic bitrate idea (§4).
- **The dispatcher runs nothing the app writes** (lesson from phase 4 of MANAGER.md): it only
  reads what NetworkManager gives it. The script applies the disabled links, as data.

## 3. Changes per piece

### `scripts/eirl-engine.sh` (the former launcher; today `internal/engine/config` and `internal/engine/output`)
- `SRT_URL` accepts `srtla://host:port` (same host/port regex, another scheme). With `srtla`:
  `SRT_MODE` must be `caller` (otherwise it exits with 64), it starts
  `srtla_send ${SRTLA_LOCAL_PORT} <host> <port> ${RUN_DIR}/srtla-ips` and the destination of
  `srt-live-transmit` becomes `srt://127.0.0.1:${SRTLA_LOCAL_PORT}?…` (streamid, passphrase and
  latency are kept).
- New variables **only from `/etc/default`** (not from the app): `SRTLA_LOCAL_PORT` (6000),
  `SRTLA_BIN` (`/usr/local/bin/srtla_send`), validated by whitelist like the rest.
- Startup: waits up to N s to have at least one IP in `available`; with zero it exits with 2 (systemd
  restarts it) instead of starting an empty `srtla_send` (which exits with `EXIT_FAILURE`).
- Watcher: selection + `SIGHUP` (§2.3); leaves `/run/eirl-engine/srtla-state` (IPs in use).
- Log filter: whatever `srtla_send` prints goes through `srt_log_filter` (masks the streamid) and the
  periodic noise is silenced, same as with `bytes lost`.
- `state` adds `TRANSPORT=srt|srtla`.

### Dispatcher + installation (root)
- `deploy/90-eirl-uplinks` (NM dispatcher) and `deploy/eirl-uplinks.conf` (tmpfiles:
  `/run/eirl-uplinks` `0755 root`). `install.sh` installs them; with `rp_filter=2` in
  `/etc/sysctl.d/` only if the spike requires it.
- The `srtla_send` binary: built in `build/Dockerfile` (aarch64, pinned commit) and deployed by
  `build.sh` to `/usr/local/bin`. AGPL licence: unmodified binary as a separate process; if the
  device is sold, its source code has to be offered (note it in the README).

### `eirl-encoderd` (Go)
- `config.ParseURL`/`splitSRT`: accept `srtla://host:port` (caller only; listener → 400), without
  breaking the rule of §6 of MANAGER.md (changing host requires giving the secrets again; going from
  `srt` to `srtla` on the same host is only a port/scheme change and does not require it).
- Latency: if the pasted `srtla://` URL has no `latency`, **2000 ms** (what we use with Moblin;
  with mobile links 200–500 ms falls short). The field is still editable (20–8000).
- `GET /status` → an `uplinks` block: per interface, name, type (`ethernet`/`usb`), IP, in use or not,
  and **upload Mbps** computed from `/sys/class/net/<if>/statistics/tx_bytes` (without touching
  `srtla_send`). `transport: "srt"|"srtla"`.
- Web: a "Links" card with a distribution bar per interface; a **"Single link: no bonding"** warning
  when there is only one (the Oct 7 cancellation by a customer using SRTLA with a single link shows it is the
  typical failure). The usual SRT state (RTT, losses) is still the end-to-end one.
- Tests: `ParseURL` (srtla, listener rejected, default latency), validation and reading of
  `available`.

### Nothing in the backend, dashboard, relays or `eirl-docs`
The encoder is not a product yet: `docs: n/a` until it is. When it is, the SRTLA page
of `eirl-docs` will have to talk about the encoder (without naming BELABOX/srtla_send: "zero
stack" rule).

## 4. Dynamic bitrate (needed for SRTLA to be really useful)

With modems the total bandwidth goes up and down minute by minute. With fixed CBR, when the sum of the links
drops below the bitrate, SRT's buffer fills up and SRT **drops** packets once the latency is exceeded: cuts.
Moblin and BELABOX lower the encoder's bitrate live; without that, SRTLA only protects against a link
dropping, not against congestion.

Proposal: **our own controller**, which is also useful for plain SRT over 4G:
- Replace `gst-launch-1.0` with a minimal launcher in **Python 3.6 + gi** (already installed) that does
  `Gst.parse_launch()` of the **same** pipeline text, exits on bus error/EOS (same
  behaviour as today for systemd) and adjusts the `bitrate` of the main (named) `nvv4l2h264enc`
  on the fly.
- Input: the statistics that `srt-live-transmit` already writes to `/run/eirl-engine/srt-stats.json`,
  raising the rate from ~1 Hz to ~5 Hz (`-s` in packets). Signals: send buffer
  (`msSndBuf`/`pktSndBuf`), RTT compared with its recent minimum, and losses/retransmissions.
- Rule (belacoder style): go down fast (−20–30% per step) when the buffer or the RTT grows;
  go up slowly (+0.5 Mbps every few s) when stable; between `VBITRATE_MIN` and `VBITRATE`.
  `ABITRATE` is not touched.
- The app shows the current bitrate (the controller leaves it in `/run/eirl-engine/state`).

Alternatives evaluated: **belacoder** as is (GPL-3, see §2); **no dynamic bitrate** (phase 2
alone: acceptable only for testing the transport or with a very comfortable link margin).

To check in the spike before committing: that L4T 32.7's `nvv4l2h264enc` accepts changing
`bitrate` in PLAYING without a forced IDR or artefacts (BELABOX does it on this SoC, but with its own
build), and the cost of the Python process (target: < 1% of a core, < 30 MB).

## 5. Phases (with a stop and sign-off at each one)

| Phase | What | Who / interruption |
|---|---|---|
| **0** | **Hardware requirements**: 5 V 4 A supply (blocking). Get at least **2 real links**: `eth0` + an Android with USB tethering is the minimum; the realistic option is 2 **HiLink** modems (e.g. Huawei E3372h-320 HiLink) with SIMs from different carriers. Decide whether the iPhone is in (it requires building `ipheth` in `build/Dockerfile` and `usbmuxd`) | The maintainer. No interruption |
| **1** ✅ | **Manual spike** (done on Oct 9 over loopback, without modems: see §7), without touching the service: build `srtla_send` in `~/srtla`; the operator sets the `ip rule`/`ip route` rules by hand for `eth0` + 1 USB link (script in `~`, with sudo); a test stream in parallel (`SOURCE=test`, another FIFO) → `srt-live-transmit` → `srtla_send` → **relay eu1:5001** with our own production key; reading from a workstation with `srt-live-transmit` (`read:` on eu1:5000, the workstation always as caller). Measure: CPU/RAM of `srtla_send`, distribution per link, what happens when one is unplugged (cut or not, how long it takes to come back), `rp_filter`. Test in a separate pipeline the on-the-fly `bitrate` change of `nvv4l2h264enc` | The maintainer, for sudo and cables. No interruption of `eirl-engine` |
| **2** ✅ code | **Integrated SRTLA transport**: script, dispatcher, `install.sh`, binary in `build/`, app (URL, latency, `uplinks`, "Links" card). Bitrate still fixed | Deployment with `build.sh` (the maintainer) + script with `mv`. One interruption |
| **3** ✅ code | **Dynamic bitrate** (§4), enabled with `ABR=1` and also tested with plain SRT | One interruption |
| **4** ✅ code | **Disable links from the web** (e.g. so as not to spend the phone's data): the app writes `/etc/eirl-encoder/uplinks-disabled` (interface names, whitelist `^[A-Za-z0-9_.-]{1,15}$`), the watcher applies it as data. Wrap-up: code review + security review (root dispatcher and the new file reading), README and MANAGER.md up to date, MR | No interruption (reload by SIGHUP) |

Acceptance tests (phases 2–3): 30 min with 2 links without visible losses; unplug a
link live without a cut on the reading side; plug it back in and have it rejoin on its own; with
`tc netem` (the maintainer, sudo) limit a link to 1 Mbps and see the bitrate go down instead of cuts;
end-to-end latency with `latency/` (expected ~2.5 s with a 2000 ms buffer, compared with today's 1.06 s
on the LAN).

## 6. Risks and open decisions

- 🔴 **Power** (§1). Without the right supply, phase 1 with modems does not start.
- **`srtla_send` is experimental**: if it hangs or distributes badly in the spike, plan B = the
  OpenIRL/irlserver sender if they publish it, or maintaining a small fork of our own.
- **Modem compatibility**: without `cdc_mbim`/`qmi_wwan` and with `usb-modeswitch-data` from 2017, a
  new modem may get stuck in CD-ROM mode. Purchase recommendation: HiLink (already Ethernet).
- **Latency**: SRTLA requires a large buffer (1.5–2 s). It is the price of bonding; with a single reliable
  link plain SRT is still better, and the web must make that clear.
- **Personal data/licences**: no new customer data. The AGPL of `srtla_send` (§3) must be reviewed
  before selling the device.
- **Decisions for the maintainers**: (1) which links to buy/use for the tests; (2) iPhone, yes or no?;
  (3) dynamic bitrate with our own controller (recommended) or belacoder; (4) H.265? It saves ~30%
  of bitrate at the same quality (Moblin uses it for that reason and MediaMTX accepts it), but reading it
  in the Cloud OBS has to be validated: outside this plan.

## 7. Implementation (Oct 9) and tests

What changed compared with the plan when implementing it:

- **Dispatcher** (`manager/deploy/90-eirl-uplinks`): besides one table and one `from <ip>` rule per
  link, it sets **a rule 999 `lookup main suppress_prefixlength 0`**: `main` decides everything except the
  default route. Without it, traffic with a link's IP towards a local network of another interface
  (the USB gadget `192.168.55.x`, the LAN) would go out through that link's gateway. It always recomputes
  everything (idempotent) and has a dry-run mode (`EIRL_UPLINKS_DRYRUN=1`, without root).
- **Adaptive bitrate** (the former `scripts/engine-run.py`; today `internal/engine/output`): only with `ABR=1`; with `ABR=0`, plain
  `gst-launch-1.0` as before. Same pipeline (`Gst.parse_launchv` with the same arguments), same
  outputs and the same error format the app classifies. SRT statistics reach it through
  a FIFO at ~4 samples/s with the bitrate at its minimum (more with a high bitrate) and it leaves the latest one in
  `srt-stats.json` as before. The base RTT is the minimum over **10 s** (with 30 s, a change in the
  distribution between links that raised the RTT was penalised for half a minute; a test caught it).
- **Disabled links**: `/etc/eirl-encoder/uplinks-disabled`, outside `engine.env` so that
  changing them does not flag "unapplied changes" (they are applied on the fly).
- **The Wi-Fi client without internet** (Oct 13, [`WIFI.md`](WIFI.md) §13): `/run/eirl-encoder/uplinks-nointernet` (written by
  the app, same format) is also excluded (`UPLINKS_NOINTERNET`). If the **last** link goes away while
  streaming, `srtla_send` stops and the output stays in `no_uplinks` (transient) until one comes back.
- **App**: `transport`, `uplinks` and `bitrate` in `/status`; `PUT /uplinks/{interface}`; a form with
  `srtla://`, 2000 ms latency and adaptive bitrate when an SRTLA URL is pasted. The list of links
  is updated row by row (redrawing it every 2 s changed the checkbox under your finger).
- `srtla_send`: BELABOX `37862da` (2025-04-05), built by `build/srtla.sh` (18 KB, glibc 2.27)
  with **one patch**: it listens for the local SRT on `127.0.0.1` and not on all interfaces (see below).

### Code review (Oct 9, high effort): 10 findings, all fixed

- 🔴 **Tables `100+ifindex`**: a modem that reconnects bumps its ifindex; at 154/155 the table would be
  `main`/`local` (253-255 are reserved) and when the link went away **`main` was flushed** (board without network).
  Now tables `10000+ifindex` and priorities bounded to 1000-30999.
- 🔴 **`srtla_send` listened on `INADDR_ANY:6000`** and answers whoever wrote to it last: anyone
  on the LAN (or on a modem's network) sending a datagram took over the relay's replies and cut
  the stream. Patch in `build/srtla.sh` → `127.0.0.1` (checked with `ss -lunp`).
- 🟠 **Two identical modems = the same IP** (factory HiLink: both `192.168.8.100`): they cannot be
  told apart by source. The dispatcher routes only the first and marks the rest `conflict`; the script does not
  use it and the web warns. **When fitting two identical modems, change the network of one of them in its web UI** (e.g.
  to 192.168.9.1).
- 🟠 Disabling links sent the whole list from the browser: a disabled link
  that was disconnected got lost, and two taps in a row overwrote each other → `PUT /uplinks/{interface}`.
- 🟡 ABR: with a `VBITRATE` that is not a multiple of 50 it never reached the maximum (rounding); if the statistics FIFO
  failed, `srt-live-transmit` stayed blocked with the log saying "bitrate fijo" (fixed bitrate) → now
  it exits with an error and systemd restarts it.
- 🟡 `usb0` excluded by name left out an RNDIS Android (the gadget is already excluded for being
  a slave of the `l4tbr0` bridge); a `PUT` that collected the whole state; duplicated logic.

### Tested on the board (Oct 9, over loopback, without touching `eirl-engine.service`)

Without modems or the 4 A supply: two "links" with loopback IPs (127.0.0.2 and .3, which do not
need source-based routing) → local `srtla_rec` → SRT listener.

| What | Result |
|---|---|
| `srtla_send` and `srtla_rec` build with gcc 7.5 | ✅ ~0% CPU, 0.5 MB of RAM each |
| Bonding with 2 links, remove one and add it back with `SIGHUP` | ✅ data keeps arriving without a cut (~0.5 MB/s constant) |
| `nvv4l2h264enc` accepts changing `bitrate` in PLAYING (L4T 32.7.6) | ✅ 6000 → 2000 → 4000 → 1000 → 6000 kbps, measured 6031/2144/3984/1119/5807, **no extra IDR** |
| Full script with `ABR=1` and SRTLA | ✅ connects; disabling a link from the API takes it out of the distribution in ≤ 2 s with SRT connected; receiver frozen for 3 s → the ABR drops to the minimum (1000) and comes back up slowly (1800 after 10 s); `SIGTERM` cleans up processes and files |
| Cost of the former Python launcher (`engine-run.py`) compared with `gst-launch-1.0` | ✅ the same CPU (the pipeline's) and +10 MB of RAM |
| Dispatcher in dry-run mode | ✅ `eth0` → table 10003 and rule 1003; ignores the USB gadget (`usb0`, `rndis0`, `l4tbr0`) |
| Test app on :8088 with the web at iPhone size | ✅ "Links" card, single-link warning, checkboxes, current bitrate in SRT, `srtla://` form (no trace of the key in the DOM) |
| Automated tests | ✅ Go (`go test ./...`, also `-race`) and Python (the former engine's `test_engine_run.py`) |

### Pending (needs hardware)

1. 🔴 5 V 4 A power supply.
2. ✅ Deployed on Oct 9 (see §8). Real dispatcher with `eth0`: table 10003, rules 999 and 1003, the
   board still reaches the internet. A real stream over SRTLA to **eu1:5001** with a single link: it connects
   (RTT ~58 ms) and the web shows the "Links" card and "Connected (SRTLA)".
3. **Dispatcher with a second link** (USB): rules, `rp_filter` and that each IP's traffic goes out
   through its interface (`tcpdump -i <if>`).
4. Reading from a workstation (`read:` on eu1:5000) with 2 links and with `ABR=1`.
5. The acceptance tests of §5: 30 min with 2 links, unplugging and plugging back in live,
   `tc netem` on a link to see the bitrate go down, end-to-end latency.
6. Tune the ABR thresholds with a real mobile network (today they come from synthetic tests).

## 8. Install and undo

Install: the engine with `tools/deploy-engine.sh` (it refuses while streaming), and then
`JETSON=<board> manager/build.sh`. `install.sh` puts in place `srtla_send`, the dispatcher and the tmpfiles entry for
`/run/eirl-uplinks`, and runs the dispatcher once. **It does not restart the stream.**

Undo (as root):
```bash
rm /etc/NetworkManager/dispatcher.d/90-eirl-uplinks /usr/local/bin/srtla_send
ip -4 rule del priority 999
for p in $(ip -4 rule show | awk -F: '$1 >= 1000 && $1 <= 30999 {print $1}'); do ip -4 rule del priority "$p"; done
for t in $(ip -4 route show table all | grep -o 'table 1[0-9]\{4\}' | sort -u | cut -d' ' -f2); do ip -4 route flush table "$t"; done
rm -rf /run/eirl-uplinks /etc/eirl-encoder/uplinks-disabled
sed -i '/eirl-uplinks/d' /etc/tmpfiles.d/eirl-encoder.conf
```
`rp_filter=2` on the links goes back to its default only on reboot.

**The cameras' access point (F7, [`NETWORK_SOURCES.md`](NETWORK_SOURCES.md)).** `ap0` is never a
link: the dispatcher excludes it by name (`ap*`) and by type (`iw dev <if> info` → `type AP`), even if
it ever had a gateway. A Wi-Fi client with a gateway is one (that is the point: a phone
sharing its data). In `/etc/eirl-encoder/engine.env`, set
`SRT_URL` to `srt://…` and `ABR=0` (or from the app).
