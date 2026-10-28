> **Status:** IN PROGRESS — implemented (phases 2-4), tested over loopback and **deployed on the board** (Oct 9: a real stream over SRTLA to eu1 with one link). Pending: tests with several real links, §7. **Hardening of the link (Oct 19, §9): the Rust sender, 1,128-byte payload, 2 s latency, cached relay address — in progress**
> **Updated:** 2026-10-19

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
| Kernel 4.9: `cdc_ether` and `cdc_ncm` **built in**, `rndis_host` as a module. **No** `ipheth`, `cdc_mbim` or `qmi_wwan`. `option`/`usb_wwan` yes (serial modems). `usb-modeswitch` installed, but with data from **2017** | These work: **HiLink** modems (they show up as Ethernet: `cdc_ether`), **Android** phones with USB tethering (RNDIS or NCM) and 4G routers over cable. **They do not work without building modules**: iPhone (`ipheth`) and "stick" modems in QMI/MBIM mode (they would also need ModemManager, disabled when slimming down the board). Both are built since (MODEMS.md §4.1; the iPhone, MODEMS.md §12, from the 0.8.0 image) |
| `CONFIG_IP_MULTIPLE_TABLES=y`, iproute2 4.15 | There is *policy routing* |
| NetworkManager **1.10.6** active and managing new USB-Ethernet devices (DHCP only); `networkd-dispatcher` also active | NM 1.10 does **not** know how to set source-based routing rules (`ipv4.routing-rules` arrived in 1.18): it has to be done with a *dispatcher* script |
| `rp_filter`: `all=1` (strict), `eth0=2` | With `from <ip>` rules the strict check should pass, but **it has to be verified** in the spike; if not, `rp_filter=2` on the link interfaces |
| Power: 5 V 2 A supply, already at `OC ALARM` and 4.65 V | 🔴 **Blocking**: a 4G modem peaks at 0.5–1 A. Without the **5 V 4 A** supply (barrel + jumper J48) the board will reboot while streaming (see DEVELOPMENT.md) |
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
| **0** | **Hardware requirements**: 5 V 4 A supply (blocking). Get at least **2 real links**: `eth0` + an Android with USB tethering is the minimum; the realistic option is 2 **HiLink** modems (e.g. Huawei E3372h-320 HiLink) with SIMs from different carriers. Decide whether the iPhone is in (it requires building `ipheth` in `build/Dockerfile` and `usbmuxd`): in since Oct 19, MODEMS.md §12 | The maintainer. No interruption |
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
- **Decisions for the maintainers**: (1) which links to buy/use for the tests; (2) iPhone, yes or no? (yes: MODEMS.md §12);
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
`JETSON=<board> manager/build.sh`. `install.sh` puts in place `srtla_send` and `srtla_send_rs` (§9), the dispatcher
and the tmpfiles entry for `/run/eirl-uplinks`, and runs the dispatcher once. **It does not restart the stream.**
To go back to the C sender without reinstalling: `SRTLA_SENDER=belabox` in `/etc/default/eirl-engine` (§9.2) and a
restart of `eirl-engine` (it reads that file when it starts: a slot with the owner, never while streaming).

Undo (as root):
```bash
rm /etc/NetworkManager/dispatcher.d/90-eirl-uplinks /usr/local/bin/srtla_send /usr/local/bin/srtla_send_rs
rm -f /var/lib/eirl-engine/relay-addr.json
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

## 9. Hardening the link (Oct 19): the Rust sender, smaller packets, more latency, a cached relay address

> **Status:** IN PROGRESS — implemented, bench done (§9.9: the default is Rust classic) and tested on the board with one link (§9.10); waiting for its release.

Four changes, decided on Oct 19 after a review of SRT, SRTLA and RIST for IRL use:

1. **The sender.** BELABOX's `srtla_send` (C, AGPL-3.0, frozen since April 2025, no statistics per link) is
   replaced, as an interim step, by irlserver's Rust `srtla_send` (MIT): per-link congestion control, exclusion
   of stalled links, statistics per link over a control socket. The C sender stays in the bundle for one more
   release as the way back, then leaves it (and with it the AGPL notice).
2. **1,128-byte SRT payload** (6 TS packets instead of 7): below the MTU of the APNs and phone hotspots that
   fragment 1,316-byte packets (a lost fragment is a lost packet). What Moblin sends with "Big packets" off.
3. **2,000 ms SRT latency by default**, for SRT as for SRTLA (§9.6).
4. **The last good address of the relay is cached**, so that a session can start while name resolution
   fails (§9.7): in the night test of the Go engine a recovery waited two minutes only for DNS.

### 9.1 The Rust sender, pinned

`irlserver/srtla_send` at **59a7d8b** (2026-10-17), nine commits after v4.1.0: it carries the fix that makes
the RTT floor a window in time and lets a stall-latched link rejoin, which is what the bufferbloat report
(upstream issue #25: a bloated leg capturing the stream) needed. Built by `build/srtla-rs.sh` (§9.8) with one
change: the local socket listens on 127.0.0.1, not `[::]`. Like the C sender, it answers whoever wrote to it
last, so a datagram from the LAN or from a modem's network would take the relay's answers (§7, the same
change in `build/srtla.sh`).

What it keeps from the C sender, so the engine's side barely changes:

| | BELABOX (C) | irlserver (Rust) |
|---|---|---|
| Arguments | `PORT HOST PORT IPS_FILE` | the same, plus options |
| IPs file | one IPv4 per line | the same (`<ip> [weight]` accepted, not used) |
| New uplinks | SIGHUP rereads the file | the same |
| Giving up | prints `Failed to establish any initial connections` / `Failed to re-establish any connections` and exits | prints `Failed to re-establish any connections` (ERROR) every second while every uplink is lost, but **never exits**, and never gives up its first registration either: it keeps trying, which saves rebuilding the session after a short cut. The engine stops it after 60 s of that (`event=srtla_gave_up`), and ends a session that has not registered in 15 s (below), so that the next one resolves the relay's name again |
| No uplink answers | `warning: no available connections` | the same |
| Name of the relay | resolved once at start (`Failed to resolve`) | resolved for every uplink it adds; with an address (§9.7) nothing to resolve |
| Log | `printf` to stdout (hence `stdbuf -oL`) | `tracing` to stdout, line by line; `RUST_LOG=warn`, `NO_COLOR=1`. It warns once per NAK, and once per packet while it has no uplink: the engine classifies every line but writes each kind (its numbers aside) to the journal at most once every 30 s, with how many it left out |
| Statistics | none | JSON-RPC over a Unix socket (`--control-socket`), `subscribe` to `stats`: one snapshot per second |

How the engine starts it: `env RUST_LOG=warn NO_COLOR=1 TOKIO_WORKER_THREADS=2 <SRTLA_RS_BIN> --mode <SRTLA_MODE>
[--no-rehome] --control-socket <RUN_DIR>/srtla.sock <SRTLA_LOCAL_PORT> <relay> <port> <RUN_DIR>/srtla-ips`, where
the relay is its address (§9.7) and `--no-rehome` goes with it; when the name could not be resolved at all the
sender gets the name, and keeps its re-home.
srt-live-transmit starts only once the sender has registered its uplinks with the relay (the C sender's
`connection established`; for the Rust sender, the first connected uplink in its statistics, asked with `get_stats`
every 100 ms until then, since its pushes come once a second; at most 15 s, then it starts anyway,
`event=srtla_register_slow`; with the Rust sender, whose statistics say it has not registered, the session
ends with `relay_unreachable` instead, as the C sender does by exiting, and the retry resolves the relay's name
again: otherwise a cached address that is no longer the relay's would hold the session for ever). 🪤 The relays' SRT stack (gosrt, in MediaMTX) starts its delivery clock when
the handshake arrives and does not discount how long the caller had been connecting (libsrt does, from the
handshake's timestamp): every second `srt-live-transmit` spent waiting for the bond was a second more of latency for
the whole session. Measured on the bench with the old order: +1.8 s end to end with the Rust sender, which takes
~2 s to register (an RTT probe of 1 s, then REG1 and REG2), against ~0.4 s for the C one.
`--no-rehome`: the relay is given as an address, so there is nothing to follow; when every uplink is dead for
10 s the sender exits, and the engine's next session resolves the name again (§9.7), which is the same
"re-home" one level up. Lines the engine classifies besides the C ones: `no uplinks available` and
`no IPs in list` → `no_uplinks`; `bind local SRT UDP listener` → `internal`. ANSI escapes are stripped anyway.

### 9.2 Board settings (the way back)

In `/etc/default/eirl-engine` (root; the app cannot set them):

| Key | Values | Default | |
|---|---|---|---|
| `SRTLA_SENDER` | `irlserver` \| `belabox` | `irlserver` | which sender; `belabox` is the C one of §3 |
| `SRTLA_MODE` | `classic` \| `enhanced` | chosen with the bench (§9.9) | the Rust sender's scheduler; `classic` is the C one's |
| `SRTLA_RS_BIN` | path | `/usr/local/bin/srtla_send_rs` | the Rust sender |
| `SRTLA_BIN` | path | `/usr/local/bin/srtla_send` | the C sender (unchanged) |
| `SRT_PAYLOAD` | `1128` \| `1316` | `1128` | SRT payload for `srt` and `srtla` (§9.5) |

If the chosen sender is not installed and the other one is, the engine uses the other one and says so
(`event=srtla_sender_fallback`): an update that lands half-way never leaves the encoder without SRTLA. With
neither, the configuration is invalid as before (`SRTLA_BIN`).

### 9.3 Statistics per link: `srtla-links.json` (engine → app)

While a session runs with the Rust sender, the engine subscribes to its `stats` and writes, atomically and at most
once a second, `<RUN_DIR>/srtla-links.json` (`/run/eirl-engine/srtla-links.json`, 0644). It is removed when the
session ends; the C sender never writes it. The app reads it as data: a file older than 5 s, or one that does not
validate, counts as absent.

```json
{"v": 1, "sender": "irlserver", "mode": "classic",
 "links": [{"iface": "eth0", "ip": "192.168.1.20", "bond": "active",
            "rttMs": 57, "sendKbps": 5400, "nakPerSec": 0.2}]}
```

| Field | | |
|---|---|---|
| `iface`, `ip` | the uplink (the IP the sender reports, matched to the interface of `uplinks`) | required |
| `bond` | `active` \| `stalled` \| `weak` \| `down` | `down`: not connected or timed out; `stalled`: excluded by the stall guard; `weak`: held back by the enhanced scheduler; else `active` |
| `rttMs` | integer, 0-60000 | the sender's smoothed RTT on that uplink |
| `sendKbps` | integer, 0-1000000 | what the sender put on that uplink, kbit/s |
| `nakPerSec` | number, 0-100000, one decimal | NAKs attributed to that uplink per second, from the difference of its cumulative count |

At most 16 links. Unknown fields are ignored (additive contract).

### 9.4 Where the statistics go (all optional)

- **App, `GET /api/v1/status`, `uplinks[]`**: `rttMs`, `sendKbps`, `nakPerSec` and `bond`, only for a link in a
  fresh `srtla-links.json`. Absent otherwise: with the C sender, with SRT or while not streaming.
- **Screen** (`ui.UplinkInfo`, read by `appapi` from `/status`) and **web** ("Connections"): a second line under a
  link in use, "RTT 57 ms · 0.2 NAK/s", and `bond` other than `active` as the reason (es/en/fr).
- **Cloud** (`status.uplinks[]`): the same four fields. Today's backend keeps only the fields it knows (it ignores
  these); showing them in the dashboard is a later change there.
- The health of a link (the dot, `MODEMS.md` §5) does not change: these figures are data for it, for a later phase.

### 9.5 1,128-byte payload

For `srt` and `srtla` with `SRT_PAYLOAD=1128`: `mpegtsmux alignment=6` (each write of the unbuffered filesink is
one SRT packet), `srt-live-transmit -chunk:1128` and `payloadsize=1128` in the SRT URL; the statistics interval
counts packets of that size. RIST, UDP and RTMP keep 7 TS packets. Cost: ~17 % more packets per second and
~0.6 % more headers.

🪤 The chunk and the mux's alignment must change together, and they come from the same `SRT_PAYLOAD`
(`config.TSPerPacket`, `config.SRTPayloadBytes`, and a test that ties them). `srt-live-transmit`'s `file://con` reads
stdin non-blocking and, after a read shorter than `-chunk`, still sends a whole chunk with the tail of the previous
packet in it (SRT 1.5.7, `apps/transmitmedia.cpp`, `ConsoleSource::Read`): corrupt TS that SRT carries without a
complaint. With every write of the unbuffered filesink exactly one chunk (atomic on a pipe, under `PIPE_BUF`), every
read is whole.

### 9.6 Latency: 2,000 ms by default, one value

SRT's latency is fixed by the handshake for the whole session, but the links change during it (the cable is
unplugged and a modem carries on), so the default cannot depend on the kind of link. Too much latency only adds
delay; too little cuts the stream on a mobile network (5-8 × RTT). So: new SRT destinations get 2,000 ms, as SRTLA
already did, and the cloud's "automatic" latency is 2,000 ms for both (the backend, `ENCODER_CLOUD.md` §5). A value
someone set is kept.

### 9.7 The relay's address, cached

At the start of every `srt`/`srtla` session the engine resolves the relay's name itself (IPv4, 5 s budget):

- resolved: the first address (or the cached one, if it is among the answers: round-robin does not move the bond),
  saved in `<STATE_DIR>/relay-addr.json` (`/var/lib/eirl-engine/`, kept across reboots), `event=relay_resolved`;
- not resolved: the cached address of that name, whatever its age, `event=relay_dns_cached` with its age; without one,
  the sender gets the name and resolves it itself, as before (`event=relay_dns_failed`): the cache only ever adds a
  way to connect (an IPv6-only name, for plain SRT, still works).

The senders and `srt-live-transmit` are given the address, never the name (SRT carries no name: no TLS, the stream
id is in the handshake). The file holds at most 8 names: `{"v": 1, "hosts": {"<name>": {"ip": "<ipv4>", "at": <unix s>}}}`.

### 9.8 Build

`build/srtla-rs.sh`: the same Ubuntu 18.04 arm64 container as the other tools (glibc 2.27), the pinned Rust
toolchain of the project (`rust-toolchain.toml`) installed by a rustup-init verified by sha256, the codeload tarball
of the pinned commit verified by sha256, `cargo build --release --locked`. It checks that the binary needs no glibc
symbol newer than 2.27, and leaves `build/out/srtla_send_rs`, its version, its MIT licence and those of the crates it
links. Installed as `/usr/local/bin/srtla_send_rs`, next to the C one.

### 9.9 Bench before choosing the default mode

C sender against Rust classic against Rust enhanced, on a Linux bench with network namespaces and `netem`: two or
three links with 50/150/800 ms RTT, deep queues (the bufferbloat case), loss bursts, a link that dies and comes
back, a marginal link swinging between 100 and 1,200 ms. Measured: cuts, NAKs, retransmissions, sustained
bitrate, the share per link and the sender's CPU and memory.

**Results (Oct 19-20).** A Linux bench in Docker on an Apple Silicon Mac (arm64): `srt-live-transmit` → the sender →
three links in network namespaces (`netem` + `tbf`) → `srtla_rec` (irlserver 39e324a, as the relays) → MediaMTX
1.21.0 (gosrt, as the relays) → an SRT reader. 300 s per run, latency 2000 ms, srt-live-transmit started once the
sender registered (as the engine does now). Means of 3 seeds (enhanced: 1); *frozen* is the seconds with the
reader's video stopped for more than 250 ms.

| Scenario | Sender | Frozen s | Loss % | Retransmissions | Lost in the links' queues |
|---|---|---|---|---|---|
| Bufferbloat (upstream #25: 4.2/1.5/2.0 Mbit/s, queues 150/800/600 ms) | C | 44.6 (one seed: 134) | 9.1 | 285k | 175k |
| | Rust classic | 7.2 | 0.9 | 74k | 7.6k |
| | Rust enhanced | 0 | 0 | 143k | 50k |
| A link goes down 30 s, another is removed 20 s, short outages | C | 4.1 | 1.3 | 212k | 69k |
| | Rust classic | 1.0 | 0.3 | 104k | 33k |
| | Rust enhanced | 202 | 50 | 384k | 364k |
| Loss bursts on two of three links | C | 0 | 0 | 250k | 132k |
| | Rust classic | 0 | 0 | 124k | 64k |
| | Rust enhanced | 212 | 54 | 489k | 463k |
| A link swinging 100 ↔ 1,200 ms (upstream PR #17) | C | 0 | 0 | 136k | 16k |
| | Rust classic | 0.1 | 0 | 80k | 12k |
| | Rust enhanced | 189 | 44 | 264k | 228k |
| RTT 50/150/800 ms, 5 Mbit/s that needs the three | C | 294 | 62 | 1,673k | 1,502k |
| | Rust classic | 190 | 47 | 457k | 358k |
| | Rust enhanced | 296 | 78 | 1,317k | 1,044k |

- **The default is Rust classic.** It is never clearly worse than the C sender, sends half the retransmissions or
  fewer, and loses far less in the links' queues; the C sender had one bufferbloat run that froze for 134 s.
- **Enhanced is not a default**: it collapsed in 4 of 5 scenarios (loss 44-78 %, the SRT sender dropping packets too
  late), likely its NAK penalties and loss-driven caps against the spurious NAKs of the relays' SRT stack (gosrt sends
  a NAK for every gap, and with several links of different delay most gaps are only reordering).
- No sender carries a link of 800 ms RTT that the stream needs: it is beyond SRTLA with today's relays.
- **1,128 against 1,316 bytes** (the link-down scenario with both): no systematic difference, only noise between seeds,
  as expected with a 1,500-byte MTU. 1,128 is for carriers whose MTU fragments 1,316-byte packets (a lost fragment is a
  lost packet); the bench shows it costs nothing measurable.
- **Cost on the Mac**: Rust classic 7.5-8.5 % of a core and ~8 MB, the C sender ~4 % and 1.6 MB. On the board: §9.10.
- Same latency with every sender (~2.4 s end to end) once srt-live-transmit waits for the registration (before: +1.8 s
  with the Rust sender, §9.1).

### 9.10 On the board (Oct 21)

The test board (Jetson Nano, the 1080p60 program, H.265 at 6 Mbit/s over SRTLA to the test relay, **Ethernet only**),
10 minutes with the C sender and 30 with the Rust one (classic), then a cut of the relay's UDP of 20 s and of 75 s
(an iptables rule on the board: the link "dies" and comes back).

| | C sender | Rust sender (classic) |
|---|---|---|
| Sender's CPU (of one core) | 3.7 % | 7.9 % |
| Sender's memory | 0.5 MB | 6.9 MB |
| Engine's CPU | 82 % | 82.5 % |
| Whole board (of 400 %) | 138 % | 146 % |
| CPU / GPU temperature | 42.8 / 40.7 °C | 43.0 / 41.0 °C |
| Registration | 0.2 s | 3.0 s (srt-live-transmit waits for it, §9.1: no latency added) |
| Streaming | no cut, RTT 57-59 ms | no cut, RTT median 67 ms, 6.5 Mbit/s sent |

- `srtla-links.json` and `/status` `uplinks[]` carry the link's figures (`rttMs` 64, `sendKbps` 6515, `nakPerSec` 0,
  `bond` `active`); the stream reads back from the relay; the sender listens on 127.0.0.1 only; the relay's address
  is cached in `/var/lib/eirl-engine/relay-addr.json`.
- **Cut of 20 s**: the Rust sender kept trying without exiting and was back on air 9 s after the cut ended, without
  rebuilding the session. Found: a warning per packet into the journal (761 lines) → each kind of line at most once
  every 30 s (§9.1). **Cut of 75 s** (with that fix): 119 journal lines in 3 minutes, the engine stopped the sender
  after 60 s (`srtla_gave_up`), the next session ended because it could not register (15 s), and once the cut ended
  it registered in 4 s and was on air 5 s later.
- Not tested on the board: several real links (Wi-Fi, an iPhone, modems), unplugging them by hand. The bench covers
  bonding (§9.9); real links go with the modems' M0.
