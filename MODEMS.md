> **Status:** IN PROGRESS — plan approved on Oct 14 (§10); phase M1 (the app's modem service, the health of every link, `links.json`) and the kernel modules written and tested off the board; M0 waits for the test modems and hub
> **Updated:** 2026-10-14

# Cellular modems and the health of every connection

Goal: several USB 4G/5G modems with their SIMs plugged into the encoder, watched (signal, network,
technology, SIM) and configured (PIN, APN, roaming, network mode, reset) from the touch screen and the
web, and every internet connection shown with a dot: **green** (fine), **amber** (weak signal or a
struggling link), **red** (minimal signal, no internet or disconnected), **grey** (switched off by the user).

The SRTLA transport, its routing and its share between links do not change ([`SRTLA.md`](SRTLA.md)): a
connected modem is one more interface that the NetworkManager dispatcher turns into a link.

## 1. What there was (Oct 14)

- A modem only worked if it showed up as an Ethernet card (HiLink, an Android phone sharing its data, a
  router by cable). The app knew links as `ethernet | usb | wifi` with an on/off switch, and nothing about
  their signal, operator, SIM or technology.
- `srtla_send` gives no per-link statistics: per link there is only the traffic (sysfs).
- The screen only knew how many links there were (`output.LinkInfo()`), plus the client Wi-Fi's antenna.

## 2. The board (read-only inventory, Oct 14)

| Piece | State | Meaning |
|---|---|---|
| `cdc_ether`, `cdc_ncm` | built in | HiLink, USB routers, modems in ECM/NCM mode |
| `rndis_host` | module | Android tethering, modems in RNDIS mode |
| `option`, `usb_wwan`, `cdc-wdm`, PPP | yes | the modems' AT ports |
| `qmi_wwan`, `cdc_mbim`, `huawei_cdc_ncm`, `ipheth` | **not set** | a QMI or MBIM modem (Quectel, Sierra, Telit… in their normal mode) gets no network interface |
| Quectel IDs in `qmi_wwan.c` / `option.c` (L4T sources) | 4G only | EC25/EG25, EC21, EG91/95, BG96, EP06; **not** RM500Q/RM520N (5G), EM05-G, EM12, EM160R |
| ModemManager | 1.10.0, disabled | runs with `--filter-policy=strict`; knows 2G/3G/4G. 5G modes and access technology arrived in 1.14, NR signal in 1.20 |
| libqmi / libmbim | 1.22 / 1.18 | enough for 4G |
| NetworkManager | 1.10.6, WWAN plugin | can bring ModemManager modems up with `gsm` profiles (APN, PIN) |
| `usb-modeswitch-data` | 2017 | a new modem may stay in CD-ROM mode |
| `mobile-broadband-provider-info` | not installed | the APN database (automatic APN, M4) |
| USB | one internal hub | the 4 ports share the RTS5411; cutting one port powers off all four (MIXER.md §16) |

## 3. Which modems

| Family | Examples | How it shows up | What is read | What is configured |
|---|---|---|---|---|
| **ModemManager** (QMI/MBIM) | Quectel EG25/EC25, EM05-G, RM520N (5G) on a USB adapter; Sierra EM7455; Telit FN990 | `wwan0` (qmi_wwan/cdc_mbim) + AT ports | everything: RSRP, RSRQ, SINR, RSSI, operator, technology, SIM, registration, IMEI | PIN, APN, roaming, network mode, enable, reset |
| **HiLink** | Huawei E3372h-320, E8372 | `enx…` (cdc_ether/RNDIS); a router inside, web API on its gateway | its API: RSRP, RSRQ, SINR, RSSI, bars, operator, technology, SIM | PIN, APN profiles, network mode, reboot, its subnet |
| Phone sharing data | Android over USB | `usb0`/`enx…` (RNDIS/NCM) | that it is there, internet, traffic | on/off |
| Router or CPE | a 5G router by cable | `eth…` | the same as a cable | on/off |

iPhone tethering would need `ipheth` + `usbmuxd`: not planned.

## 4. Design

```
modems ── kernel (qmi_wwan/cdc_mbim + option, §4.1) ── ModemManager (root, D-Bus) ── NM gsm profile ── wwan0 ─┐
HiLink ── cdc_ether ── enx… ───────────────────────────────────────────────────────────────────────────────┤
                                                                         90-eirl-uplinks (unchanged) ─┘
eirl-encoderd
  internal/modem      every 5 s: ModemManager over D-Bus (mm.go) + the HiLink web API (hilink.go)
  internal/netcheck   the internet probe and its hysteresis (shared with the Wi-Fi client)
  internal/linkhealth the ONE judgement of a link: good | weak | bad | off + a code, with hysteresis
  internal/links      every 2 s: uplinks + modems + Wi-Fi signal → health
      ├─ GET /status  uplinks[] (health, healthCode, label, modemId, kind modem) and modems[]   (cached snapshot)
      ├─ GET /modems  the modems
      └─ /run/eirl-encoder/links.json → the engine → the dots of the screen's header (M3)
```

- **Nothing new runs as root** except ModemManager, which is the system's. The app reads it over D-Bus
  (reading needs no permission) and turns on its extended signal readings with `Modem.Signal.Setup`, the
  only call that needs polkit (`deploy/eirl-encoder-mm.pkla`: `Device.Control` only). It **never starts
  ModemManager**: if `org.freedesktop.ModemManager1` has no owner there are no modems (D-Bus activation
  would otherwise start a disabled service).
- **HiLink**: for every available link behind `cdc_ether`, `rndis_host` or `cdc_ncm`, the app asks
  `/api/webserver/SesTokInfo` on its gateway (from `/proc/net/route`), leaving from the link's own IP so that
  the dispatcher's source routing takes it through that interface. An interface that does not answer like
  a HiLink (an Android phone) is not asked again for a minute. Then `/api/monitoring/status`,
  `/api/device/signal`, `/api/net/current-plmn` and `/api/pin/status` at the same time (1.5 s each), all
  optional: fields differ between firmware versions. All the HiLink modems are read at once; a HiLink that
  stops answering keeps its last reading for 30 s, so its dot does not flicker. A round that runs out of
  time is an error, and the previous list stays.
- **Stable identity**: a modem's id is an HMAC of its IMEI (`m-…`) or of the HiLink's MAC (`h-…`) with a key
  that never leaves the board (`/etc/eirl-encoder/modem-id.key`, 0600, created at random the first time). A
  plain hash would not do: an IMEI is the model's 8 digits, 6 of serial and a check digit, and `imeiTail`
  shows 4 of them, so ~1,000 candidates would undo it. Settings (from M4) are keyed by the id, not by the
  interface name, which changes when replugged.
- **One judgement**: `linkhealth.Judge` decides the level; the web, the screen and the cloud only paint it
  and translate the code (the workspace rule: shared domain logic lives in one helper).
- **To the screen, a file**: the app writes `links.json` atomically when something changes and at least
  every 5 s; the engine reads it while drawing only if its date changed, as it already does with the link
  files. Cheaper than asking the socket every 2 s while streaming. Orders (switch a link, PIN, APN) go
  through the socket's allow list.
- **Modems without a usable link** (no SIM, PIN, searching) are not in the dispatcher's list (no IP): they
  are in `modems[]` and in `links.json`, red, with the reason.
- **To the cloud**, a modem's uplink is still `usb` until its contract has modems (M6).

### 4.1 Kernel modules

`build/build.sh` also builds, from the same L4T R32.7.6 sources and the board's exact config,
`qmi_wwan.ko`, `cdc_mbim.ko`, `huawei_cdc_ncm.ko` and `option.ko` into `build/out/modules/`:

- `driver/modems/qmi_wwan-ids.patch`: EM05-G v2, EG12/EM12, EM160R, RM500Q, RM520N on the QMI interface of
  their default composition (`AT+QCFG="usbnet",0`, interface 4). Upstream matches them by class and
  protocol, which 4.9 lacks.
- `driver/modems/option-ids.patch`: their AT/NMEA/DM ports, from upstream v6.6 (with the QMI interface reserved).
- `cdc_mbim` binds by class: any MBIM-mode modem works without IDs.

`tools/modem-modules.sh` installs them in `/lib/modules/<release>/updates/eirl-modems/` and runs `depmod`
(Ubuntu looks in `updates/` first, so the patched `option.ko` wins over the stock one); `--check` and
`--undo`. It loads nothing: udev loads them when a modem is plugged in. The OTA installs only our
software, never L4T: new boards get the modules from the SD card image (IMAGE.md), the test board from this
script.

### 4.2 Contract

`GET /api/v1/status` (additive; an older web ignores the new fields):

```jsonc
"uplinks": [{ "name": "wwan0", "ip": "…", "kind": "modem",       // ethernet | usb | wifi | modem
              "inUse": true, "disabled": false, "txKbps": 700, "rxKbps": 40,
              "label": "Vodafone",                               // operator (or alias, M4); empty = name it by kind
              "health": "weak", "healthCode": "signal_weak",     // §5
              "modemId": "m-3f9a1c2b4d5e" }],
"modems": [{ "id": "m-3f9a1c2b4d5e", "backend": "mm",            // mm | hilink
             "iface": "wwan0", "manufacturer": "Quectel", "model": "EG25",
             "state": "connected",       // failed|locked|disabled|enabling|searching|registered|connecting|connected|disconnected|unknown
             "sim": "ready",             // ready | missing | pin | puk | error | unknown
             "registration": "home",     // home | roaming | searching | denied | idle | unknown
             "operator": "Vodafone ES", "tech": "4g",             // 2g | 3g | 4g | 5g-nsa | 5g
             "signal": { "rsrp": -106, "rsrq": -13, "sinr": 3.2, "rssi": -77, "quality": 54 },  // "bars": 0-5 (HiLink)
             "imeiTail": "5531", "iccidTail": "4821" }]
```

`GET /api/v1/modems` → `{"modems": [...]}`, the same list. Never the whole IMEI, ICCID or IMSI.

`/run/eirl-encoder/links.json` (app → engine; 0640, group `eirl-encoder`; no IPs, no identifiers):

```json
{"v":1,"at":"2026-10-14T17:00:02Z","streaming":true,"links":[
  {"name":"eth0","kind":"ethernet","health":"good","code":"ok","available":true,"inUse":true,"disabled":false,"txKbps":2100},
  {"name":"wwan0","kind":"modem","label":"Vodafone","tech":"4g","bars":2,"health":"weak","code":"signal_weak","available":true,"inUse":true,"disabled":false,"txKbps":700},
  {"name":"m-0c1d2e3f4a5b","kind":"modem","health":"bad","code":"sim_pin","available":false,"inUse":false,"disabled":false,"txKbps":0}]}
```

Order: cable, Wi-Fi, modems, other USB; by name within each. Older than 15 s = the app is not running: the
screen shows the dots as unknown.

## 5. The dot

The level is the **worst of two judgements**: the radio and the connection. A modem with full bars but no
data is not green, and a cable has no radio. Thresholds are a starting point, calibrated in M5 against
what a real stream holds.

| Link | Green | Amber | Red |
|---|---|---|---|
| Modem 4G/5G | RSRP ≥ −100 dBm and SINR ≥ 5 dB | RSRP −100…−110, SINR 0…5, or RSRQ < −15 | RSRP < −110 or SINR < 0 |
| Modem with RSSI only / bars / quality | ≥ −75 dBm / 4-5 / ≥ 60 | −75…−85 / 2-3 / 45-59 | below |
| Modem on 3G / 2G | never | 3G with a good signal | 2G |
| Client Wi-Fi | ≥ 67 % | 34-66 % | < 34 % (the antenna's thresholds) |
| Cable, phone, router | with IP and internet | — | no internet or disconnected |

5G NSA is judged by its LTE anchor (what ModemManager 1.10 reports, and it carries the uplink).

| Connection (every kind) | Level | `healthCode` |
|---|---|---|
| Switched off by the user | off | `off` |
| Same IP as another link | bad | `ip_conflict` |
| No SIM, PIN, PUK, SIM error, modem failed | bad | `sim_missing`, `sim_pin`, `sim_puk`, `sim_error`, `modem_failed` |
| Network refuses the SIM; roaming without data; searching | bad | `denied`, `roaming_off`, `searching` |
| No IP or no route | bad | `disconnected` |
| HTTPS to the relay fails | bad | `no_internet` |
| Radio below/within the thresholds above | bad / weak | `signal_minimal`, `signal_weak`, `slow_tech` |
| Streaming, switched on, and SRTLA does not use it yet | weak | `connecting` |
| SRTLA says it is struggling (patched `srtla_send`, M5) | weak | `link_weak` |

**No internet** (`links.Internet`): every 30 s, HTTPS to the stream's relay from each modem's or phone's own
IP, with the Wi-Fi client's hysteresis (two results in a row and 60 s since the last change; WIFI.md §13). A
SIM without credit or a wrong APN gives an IP and a route but no data: the link goes red and leaves the SRTLA
share. The probe and its hysteresis are the Wi-Fi client's, shared (`internal/netcheck`); with nowhere to
check against (a custom destination without the cloud) what was known is forgotten. It is the one writer of
`/run/eirl-encoder/uplinks-nointernet`: the Wi-Fi client's monitor reports
to it (`network.Manager.OnNoInternet`) instead of writing the file itself, so that neither erases the
other's lines. The cable is not checked (a LAN for cameras is not judged by the relay). ~15 MB a day per modem.

**Hysteresis** (`linkhealth.Tracker`): a worse result shows after two rounds in a row (~4 s from the last
good one), a better one after 10 s in a row; facts (off, disconnected, SIM, conflict) show at once, and so
does their end. A link that disappears starts afresh when it comes back.

## 6. Screen (M3)

Written Oct 14 (MIXER.md §18 and §21): the header's dots (`screen/ui/linkdots.go`, the engine reading `links.json`
through `appapi.Links`) and the dot and reason in every row of the "Connections" tab (`screen/ui/uplinks.go`). The
modem's own page (signal on its scales, settings) comes with the configuration, M4. Previews:
`eirl-ui-render live+srtla+ant3+dots|dots6 settings-uplinks-health`.

- **Header**: the middle area ("5,8 Mbps · SRTLA · 2 links · RTT 57 ms" today) becomes two lines: a dot
  and a short name per connection (alias, operator, "Cable", "Wi-Fi"), and below it the rate, RTT,
  temperature and voltage. The whole area is a button (≥ 44 px) to Settings › Streaming › Connections. With
  6 or more connections the names become the technology ("5G", "4G"), then dots only. The client Wi-Fi's
  antenna becomes its dot. `screen.jpg` shows the header (no secrets).
- **Settings**: Settings has 7 groups with tabs (decided Oct 14). The "Links" tab of the Streaming group
  (one row per link with its switch, the web's parity) becomes **"Connections"**: a row per connection
  with its dot, kind, reason, upload and switch (6 rows per page: cable, Wi-Fi, four modems), and a page per
  modem with RSRP/SINR/RSRQ on their scales, operator, technology, SIM, data used and its settings.
- A capture of the screen goes to the owner before installing; `draw()` stays under 50 ms on the board.

## 7. Configuration and security (M4)

| Setting | ModemManager | HiLink | Kept in |
|---|---|---|---|
| Use for streaming | the existing switch (`uplinks-disabled`), by stable id | same | `/etc/eirl-encoder` |
| Name (alias) | the app, ≤ 12 characters | same | `/etc/eirl-encoder/modems.json` |
| SIM PIN | NetworkManager uses it when connecting | its API | only NetworkManager's profile (root), like the Wi-Fi password |
| APN | `gsm` profile; automatic by operator (`mobile-broadband-provider-info`), editable | its profiles | NetworkManager / the modem |
| Data roaming | **off by default**; `gsm` profile | its API | NetworkManager / the modem |
| Network mode | `SetCurrentModes` (5G with a newer ModemManager) | its API | the modem |
| Reset | `Reset`; if it does not come back, USB unbind/bind; then cut its port (only with a hub that really cuts per port) | its API | — |
| Repeated subnet (two HiLink) | — | change it through its API, after confirmation | the modem |

- The PIN and an APN password follow the Wi-Fi password's rules (WIFI.md §12): never in arguments, logs,
  GET answers, the engine's state or the cloud; on the screen only in memory, as dots.
- IMSI, ICCID, phone number and IMEI are personal data: the cloud gets state, health, signal, operator and
  technology only; the web and the screen show the last 4 digits.

## 8. Hardware

- **Power**: four modems use more than the whole board (4G ≈ 2-4 W with ~1 A peaks at 5 V; 5G ≈ 5-8 W).
  They do not fit on the board's 5 V 4 A supply: a **USB 2.0 hub with its own supply** (5 V, 6 A or more),
  that does **not back-feed** the board, and with **real per-port power switching** checked with `uhubctl`.
- **The board's hub**: the engine cuts it to recover a hung capture card and only protects the video and
  audio sources on air: with modems behind it, that cut drops every link at once. In-use links must become
  blockers too (`engine.onAirFor`, `usbhub`; M5).
- **Interference**: USB 3 radiates at 2.4-5 GHz, and modems close to the HDMI cable or the board can break
  the capture. Modems on USB 2.0, on extension cables, antennas away; measured in M0.

## 9. Phases

| Phase | What | Board |
|---|---|---|
| **M0** | Test bench: the hub and modems (§10, D-M9/D-M11); modules installed (`tools/modem-modules.sh`); ModemManager started by hand; read each family; power, CPU and RAM of ModemManager; HDMI capture with every modem uploading | slot without streaming, ~2 h |
| **M1** | ✅ code: `internal/modem` (ModemManager, HiLink), `internal/linkhealth`, `internal/links` (with the internet check of modems and phones, §5), `/status` fields, `GET /modems`, `links.json`, polkit rule, kind `modem` in the web | installing the app does not cut a stream |
| M2 | Web: the "Links" card becomes "Connections" (dots, reason, modem detail) | — |
| M3 | ✅ code: header dots and the "Connections" tab (dot and reason per row); the modem page goes with M4 | engine install: slot |
| M4 | Configuration (§7), security review | — |
| M5 | Robustness: staged recovery of a hung modem without cutting SRTLA; in-use links protected from the hub cut; per-link stats from `srtla_send` (`link_weak`); thresholds calibrated with 3+ real modems for 30 min, walking | streams to the test relay |
| M6 | Cloud: health and signal in `status.network` (ENCODER_CLOUD.md §5) and the dashboard; a newer ModemManager if the 5G modems need it; modules in the SD image and, if decided, in updates | — |

Enabling ModemManager at boot (`systemctl enable ModemManager`, today disabled by the base) and shipping
the modules are decided after M0, with real modems.

## 10. Decisions (Oct 14)

- D-M1 ModemManager (QMI/MBIM) and HiLink; the rest as generic links.
- D-M2 5G with Ubuntu's ModemManager 1.10 (connects, measured as 4G); a newer one only if M0 asks for it.
- D-M3 the dots in the header.
- D-M4 the colour is the worst of radio and connection.
- D-M5 the "Connections" tab of the Streaming group, growing from the parity's "Links" tab.
- D-M6 the PIN is kept on the board (NetworkManager's profile) and the SIM unlocks itself at boot.
- D-M7 automatic APN by operator, editable.
- D-M8 data roaming off by default, with the reason on the red dot.
- D-M9 a powered USB 2.0 hub, no back-feed, real per-port switching (`uhubctl`).
- D-M10 to the cloud: state, health, signal, operator and technology; never IMSI, ICCID, IMEI or number.
- D-M11 test bench: 2 × Huawei E3372h-320 (HiLink), 1 × Quectel EG25/EC25 on a USB adapter (QMI),
  1 × Quectel RM520N on a USB 3 adapter (5G), SIMs from different operators.
