> **Status:** IN PROGRESS — plan approved on Oct 14 (§10). M1-M3 merged (the app's modem service and the dots, the web's card, the screen's header and tab). M4 (configuration, a modem's page on the screen and the web) written from the documentation of ModemManager, NetworkManager and the HiLink API, and tested against fakes and the simulator: §11 is what M0 checks with real modems. M0 waits for the test modems and hub. An iPhone sharing its data over USB works since Oct 19 (§12), in the image from 0.8.0
> **Updated:** 2026-10-19

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
- `srtla_send` gives no per-link statistics: per link there is only the traffic (sysfs). *(Since Oct 19 the
  default SRTLA sender is irlserver's, in Rust, and it does: RTT, what it sent, NAKs per second and whether it holds
  the link back, in `/status` `uplinks[]`, the web and the screen; SRTLA.md §9.3-§9.4. They are data for the dot's
  `link_weak` in M5: the dot itself does not use them yet.)*
- The screen only knew how many links there were (`output.LinkInfo()`), plus the client Wi-Fi's antenna.

## 2. The board (read-only inventory, Oct 14)

| Piece | State | Meaning |
|---|---|---|
| `cdc_ether`, `cdc_ncm` | built in | HiLink, USB routers, modems in ECM/NCM mode |
| `rndis_host` | module | Android tethering, modems in RNDIS mode |
| `option`, `usb_wwan`, `cdc-wdm`, PPP | yes | the modems' AT ports |
| `qmi_wwan`, `cdc_mbim`, `huawei_cdc_ncm`, `ipheth` | **not set** | a QMI or MBIM modem (Quectel, Sierra, Telit… in their normal mode) gets no network interface, nor does an iPhone. We build them since (§4.1, §12) |
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
| Phone sharing data | iPhone over USB (§12) | `eth…` (`ipheth`, once `usbmuxd` opens its session) | that it is there, internet, traffic | on/off |
| Router or CPE | a 5G router by cable | `eth…` | the same as a cable | on/off |

An iPhone needs `ipheth` and `usbmuxd`: both in the image from 0.8.0 (§12).

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
`qmi_wwan.ko`, `cdc_mbim.ko`, `huawei_cdc_ncm.ko`, `option.ko` and `ipheth.ko` into `build/out/modules/`:

- `driver/modems/qmi_wwan-ids.patch`: EM05-G v2, EG12/EM12, EM160R, RM500Q, RM520N on the QMI interface of
  their default composition (`AT+QCFG="usbnet",0`, interface 4). Upstream matches them by class and
  protocol, which 4.9 lacks.
- `driver/modems/option-ids.patch`: their AT/NMEA/DM ports, from upstream v6.6 (with the QMI interface reserved).
- `cdc_mbim` binds by class: any MBIM-mode modem works without IDs.
- `ipheth` (an iPhone's tethering, §12), unpatched: 4.9's table already has `05ac:12a8`, the product id of
  every current iPhone.

`tools/modem-modules.sh` installs them in `/lib/modules/<release>/updates/eirl-modems/` and runs `depmod`
(Ubuntu looks in `updates/` first, so the patched `option.ko` wins over the stock one); `--check` and
`--undo`. It loads nothing: udev loads them when a modem is plugged in. Since the system layer (UPDATES.md §12) the
OTA installs them too: they are in `system/built.txt`, so every board gets them with the release, whatever image it was
flashed with; the script is for trying a module that is not in a release yet.

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
| SRTLA says it is struggling (the sender's figures per link, M5) | weak | `link_weak` |

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
through `appapi.Links`) and the dot and reason in every row of the "Connections" tab (`screen/ui/uplinks.go`). With
M4, a modem's row has a «›» to its page (`screen/ui/modempage.go`), and a modem without a usable link (a SIM asking
for its PIN) has a row of its own that opens it. Previews: `eirl-ui-render live+srtla+ant3+dots|dots6
settings-uplinks-health settings-uplinks-modems settings-uplinks-modem-page settings-uplinks-modem-pin`.

- **Header**: the middle area ("5,8 Mbps · SRTLA · 2 links · RTT 57 ms" today) becomes two lines: a dot
  and a short name per connection (alias, operator, "Cable", "Wi-Fi"), and below it the rate, RTT,
  temperature and voltage. The whole area is a button (≥ 44 px) to Settings › Streaming › Connections. With
  6 or more connections the names become the technology ("5G", "4G"), then dots only. The client Wi-Fi's
  antenna becomes its dot. `screen.jpg` shows the header (no secrets).
- **Settings**: Settings has 7 groups with tabs (decided Oct 14). The "Links" tab of the Streaming group
  (one row per link with its switch, the web's parity) becomes **"Connections"**: a row per connection
  with its dot, kind, reason, upload and switch (5 rows: cable, Wi-Fi, three modems), and a page per modem: RSRP,
  SINR and RSRQ on their scales (§5's thresholds), SIM, network, operator, model and the IMEI's last digits, and its
  settings (§7.4). Data used: not yet.
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

### 7.1 API (M4)

- `PUT /api/v1/modems/{id}` `{"alias"?, "apn"? ("" = automatic), "roaming"?, "mode"? (auto | 4g | 5g), "force"?}` →
  the modem as `GET /modems` shows it. Applied on the modem first, then kept in `/etc/eirl-encoder/modems.json`
  (0640; alias, APN, roaming and mode by stable id; never the PIN). What the order names is applied even if
  `modems.json` already says it (the modem may not: a stick's own page). A change of APN, roaming or mode
  reconnects the modem: `409 last_link` when it is the only link carrying an SRTLA stream, unless `force` (the web
  and the screen ask first). `mode: "auto"` is kept as such; a modem nobody set (`""`) keeps its own mode.
- `POST /api/v1/modems/{id}/pin` `{"pin", "remove"?}` → `204`: enter the PIN of a SIM that asks for it and keep it
  (D-M6), or turn the SIM's PIN off (`remove`). `400 pin_wrong`, `409 pin_puk`, `409 pin_not_needed` (a PIN for a
  SIM that is not asking: it could not be checked). The PIN is never logged nor answered.
- `POST /api/v1/modems/{id}/reset` `{"force"?}` → `202` (the same `last_link` rule); `POST
  /api/v1/modems/{id}/subnet` `{"force"?}` → `202`: separates two HiLink sticks with the same IP (§7.3). The `last_link`
  rule is checked on the stick the move really cuts (the routed twin).
- Errors: `404 modem_unknown`, `400 validation` (with the field), `409 not_supported` (the modem's `can` does not
  list it), `409 busy` (one order at a time, background profile writes included), `409 modem_unavailable`, `502
  modem_failed`, `503 modems_disabled`.
- `GET /modems` and `/status` `modems[]` add `alias`, `apn`, `apnAuto` (the operator's, shown as "automatic
  (…)"), `roaming`, `mode`, `modes` (what this modem can be set to) and `can` (`apn | roaming | mode | pin | reset |
  subnet`). The label of a link (§4.2) is the alias when there is one.
- The screen's socket allows the same routes (SETUP.md §8). The PIN goes from the screen's secret keyboard to the
  app straight away, never through the engine's state.

### 7.2 ModemManager modems

A ModemManager modem only connects through a NetworkManager `gsm` profile. The app keeps one per modem
(`internal/modem/nmgsm.go`), system-wide, with the same `.pkla` as the Wi-Fi client (`settings.modify.system`,
`network-control`):

- id `eirl-modem-<id>`, a fixed uuid derived from the id, `autoconnect`; `gsm.device-id` = ModemManager's
  `DeviceIdentifier` (it binds the profile to that modem, so four modems do not share one); `gsm.sim-id` = the SIM's
  ICCID (it binds the profile, and so its PIN, to that SIM); `gsm.home-only` = not roaming; IPv6 `ignore` (SRTLA and
  the dispatcher are IPv4).
- **Automatic APN**: the SIM's home operator (`Sim.OperatorIdentifier`, from the IMSI; the network's code only if the
  SIM does not say) looked up in `mobile-broadband-provider-info`. Roaming, the APN is still the home operator's.
  Virtual operators share their host's code: when the providers listed for a code have different APNs, the one named
  as the SIM's operator (`Sim.OperatorName`) wins, and without a match there is none. Without the database, for an
  unknown operator or an ambiguous code, no APN: the network's default bearer, which works for most LTE operators.
  The SIM's last reading is kept, so a failed one does not flip the APN (and reconnect the modem).
- **PIN**: `Sim.SendPin`, then kept in the profile as a system-owned secret (NetworkManager's root keyfile) **for that
  SIM only**: another SIM in the same modem gets a profile without it (offering a stale PIN three times would block
  it), and without the SIM's ICCID no PIN is kept at all. It is read back with `GetSecrets` only when the profile is
  rewritten for the same SIM, and if that read fails the profile is not rewritten (it would lose the PIN). After
  unlocking, the profile is activated again (NetworkManager gave up on the locked modem). `remove`: `SendPin` if the
  SIM asks for it, then `Sim.EnablePin(pin, false)`, and the stored PIN is forgotten.
- **Network mode**: `Modem.SetCurrentModes` with one of the modem's `SupportedModes` (auto = the widest; 4G = 4G
  only; 5G = 4G+5G, only offered when the modem lists it).
- **Reset**: `Modem.Reset`.
- New modems: on the first round that sees one (with a SIM) the app writes its profile with the stored settings or
  the defaults (automatic APN, roaming off, its own mode). Once per modem, SIM and configuration; retried a minute
  later if it failed. Never for a modem whose link carries the stream (that would reconnect it: it waits), and never
  while `modems.json` cannot be read (its defaults would overwrite the user's APN). A change rewrites the profile and
  activates it again on the modem's NetworkManager device (its `Udi` is the ModemManager object path).

### 7.3 HiLink sticks

The stick's own web API (`internal/modem/hilinkctl.go`), with a fresh session per order (`SesTokInfo`):

- **APN**: a profile of its own called "EnhancedIRL" (`dialup/profiles`, created or edited and made the default);
  automatic = that profile deleted and, if it was the default, the stick's own first profile made the default again.
  An `<error>` answer to the profile list is an error, never an empty list.
- **Roaming**: `dialup/connection` `RoamAutoConnectEnable`.
- **Network mode**: `net/net-mode` `NetworkMode` `00` (auto) / `03` (4G only), keeping its bands.
- **PIN**: `pin/operate` (`OperateType` 0 = enter, 2 = turn off; a SIM asking for it gets 0 then 2), the PIN
  encrypted with the stick's RSA key (`webserver/publickey`; PKCS#1 v1.5 or OAEP as `user/state-login` says). The
  stick keeps an entered PIN only until it powers off and the app has nowhere to keep it, so on a HiLink D-M6 is
  «Unlock and remove the PIN»; the screen and the web say so.
- **Reset**: `device/control` `Control` 1 (reboot). Never 2 (factory reset).
- **Two identical sticks** hand out the same address (`192.168.8.100`). The dispatcher routes the first by
  interface name and marks the other `conflict`; anything asked from that address reaches the first. So the twin is
  never asked (its readings would be its twin's): it shows as a HiLink that can only «Separate the networks», which
  moves the **routed** stick's LAN (`dhcp/settings`, the new `192.168.X.1` and its DHCP range). That stick drops for a
  few seconds, which is why `last_link` is checked on it. Afterwards both are routed.
- **PIN errors**: `103002`/`103003` = the SIM did not take the PIN (`pin_wrong`, or `pin_puk` if `pin/status` says
  so), `103004` = PUK; anything else (an expired session, a busy SIM) is the modem's failure, not a wrong PIN.

### 7.4 Screen and web

- **Web** (the "Connections" card, shown whenever there is a modem: one asking for its PIN may be the board's only way
  out): a «›» per modem opens its panel: signal, facts, the PIN form (with "Remove the PIN" and what happens next
  time), alias, APN, network mode and roaming with «Save» (only what changed is sent, and only what the modem allows:
  a new name alone never reconnects it), «Restart» and «Separate the networks» (only for a stick in conflict; both
  ask first). `409 last_link` asks, then sends again with `force`.
- **Screen** (Settings › Streaming › Connections › «›»): three tiles (RSRP, SINR, RSRQ) with their scales, the SIM and
  network line, and six buttons: Network (a picker), APN and Name (keyboards), Roaming (a tap), Restart (a second
  tap) and, when it applies, «Unlock the SIM» (a choice, as the web's checkbox: unlock, or unlock and remove the PIN,
  each saying what happens next time; then a secret numeric keyboard, 4-8 digits) or «Separate the networks» (a
  second tap). What would cut the only connection on air asks first, saying so: a second tap on the same button, and
  only then the picker or the keyboard (nothing typed is thrown away); it goes with `force`. What the modem cannot do
  says so (`can`). Actions `modem_save | modem_pin | modem_reset | modem_subnet` (MIXER.md §21.2).

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
| M2 | ✅ Web: the "Links" card becomes "Connections" (dots, reason) | — |
| M3 | ✅ code: header dots and the "Connections" tab (dot and reason per row) | engine install: slot |
| M4 | ✅ code, from documentation: configuration (§7) on the app, the web and the screen (a modem's page); the security review; **adjusted in M0 (§11)** | engine install: slot |
| M5 | Robustness: staged recovery of a hung modem without cutting SRTLA; in-use links protected from the hub cut; per-link stats from `srtla_send` (`link_weak`); thresholds calibrated with 3+ real modems for 30 min, walking | streams to the test relay |
| M6 | Cloud: health and signal in `status.network` (ENCODER_CLOUD.md §5) and the dashboard; a newer ModemManager if the 5G modems need it; modules in the SD image and, if decided, in updates | — |

Enabling ModemManager at boot (`systemctl enable ModemManager`, today disabled by the base) and shipping
the modules are decided after M0, with real modems. The SD image needs `mobile-broadband-provider-info` for the
automatic APN (without it, §7.2's fallback).

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

## 11. M0: what to check with real modems

M4 was written from documentation, without a modem. On the bench (one slot without streaming, ~2 h), each item
says what was assumed and how to see it. What does not hold is fixed in the code, and the table says so.

**Setup**: `tools/modem-modules.sh` (then `--check`), `sudo systemctl start ModemManager` (not `enable`), the app
restarted so that it sees the bus; `mmcli -L`, `mmcli -m 0`, `nmcli -f all device`.

| # | Assumed | How to check |
|---|---|---|
| 1 | The patched `qmi_wwan`/`option` bind the EG25 and the RM520N; the RM520N goes as QMI over USB, not MBIM | `lsusb -t` (driver per interface), `dmesg`, `mmcli -m N` (`primary port`, `ports`) |
| 2 | ModemManager 1.10 reports `DeviceIdentifier`, `Sim.OperatorIdentifier`, `SupportedModes`/`CurrentModes` and the LTE signal (`Signal.Setup`) for both Quectels | `mmcli -m N`, `mmcli -m N --signal-get`, `mmcli -i N`; `GET /api/v1/modems` |
| 3 | The RM520N in 5G NSA shows as `5g-nsa` with the LTE anchor's RSRP (D-M2); if 1.10 cannot set 4G+5G, the mode offers auto and 4G only | `mmcli -m N` (access tech, modes); the screen's Network picker |
| 4 | NetworkManager 1.10 accepts the `gsm` profile as written (`device-id`, `sim-id`, `home-only`, no APN, `pin-flags` 0) and connects with it; the profile binds to its modem and its SIM and not another | `nmcli -s connection show eirl-modem-<id>`, `nmcli device`, two modems at once; another SIM in the same modem |
| 5 | `GetSecrets` returns the stored PIN to the app's user (with `settings.modify.system`), so a change of APN keeps it; a profile without secrets answers `NoSecrets` (or nothing), not a failure; the SIM unlocks itself after a reboot of the board | change the APN, then `nmcli -s -g gsm.pin connection show …` as root; reboot with a PIN SIM |
| 5b | ModemManager gives the SIM's ICCID (`SimIdentifier`) once unlocked, at least; if it also does while locked, nothing changes | `mmcli -i N` before and after the PIN |
| 6 | `SendPin` with a wrong PIN answers `IncorrectPassword` (→ `pin_wrong`) and the third wrong one `SimPuk` (→ `pin_puk`); «unlock and remove» (`SendPin`, then `EnablePin false`) leaves the SIM without a PIN | **a SIM you can lose**, or stop at two wrong tries; `mmcli -i N` |
| 7 | `SetCurrentModes` takes effect without a reset, and the activation after a profile change reconnects in under 30 s | the screen's Network and APN; `journalctl -u NetworkManager` |
| 8 | The automatic APN of each test SIM's operator is in `mobile-broadband-provider-info` and works; a wrong APN gives `no_internet` (§5) | the APN field "automatic (…)"; a wrong APN on purpose |
| 9 | Data roaming off = `home-only` stops data in roaming without breaking the registration (a foreign SIM, or a home SIM where the operator roams) | `roaming_off` on the dot |
| 10 | The E3372h-320's firmware answers `pin/operate`, `net/net-mode`, `dialup/profiles`, `dialup/connection`, `device/control` and `dhcp/settings` as huawei-lte-api documents; its RSA padding (`rsapadingtype`) | each order from the web, then the stick's own page |
| 10b | A HiLink forgets an entered PIN when it powers off (so D-M6 there is removing it); if its firmware has an automatic PIN of its own (`pin/save-pin`, which huawei-lte-api only reads), it can be used instead | unplug and plug the stick after «Unlock»; `GET /api/pin/save-pin` |
| 11 | Two E3372h on `192.168.8.1`: the second shows `ip_conflict` and is not asked; «Separate the networks» moves the first one's LAN and then both are used | two sticks at once |
| 11b | Deleting our APN profile when it was the default: the stick's own becomes the default again and it connects | the APN back to automatic, then `dialup/profiles` |
| 12 | `Modem.Reset` and the HiLink reboot come back by themselves, with the same id, in under a minute | «Restart» while watching `GET /modems` |
| 13 | Power, CPU and RAM: ModemManager's and the app's with four modems; the HDMI capture with every modem uploading (§8) | `tegrastats`, `top`, the engine's capture counters |

Results: a line per item here (✅, or what changed and the commit).

## 12. iPhone over USB (Oct 19)

An iPhone with **Personal Hotspot** on, plugged into the encoder by cable, is one more internet link: the
same as an Android sharing its data, also for SRTLA. It needs two things L4T lacks: in the SD card image from
**0.8.0**, and over the air for every board (the system layer, UPDATES.md §12) from the first release that carries it:

- **`ipheth`**, the kernel's driver for the iPhone's tethering interface (§4.1).
- **`usbmuxd`** (Ubuntu's package, 1.1.0 with libimobiledevice 1.2.1). An iPhone shows up in its photos
  configuration (1 of 4); its tethering interface (`ff/fd/01`) is in configuration 4. usbmuxd's udev rule starts
  it when an iPhone is plugged in, and usbmuxd switches the phone to that configuration (`ipheth` binds: `eth1`)
  and opens a session with it. **Without that session there is no link**, even with the phone already trusting
  the encoder (checked: `usbmuxd` stopped and the configuration set by hand, 25 s without carrier).

What the owner does, once: unlock the iPhone when plugging it in, answer **Trust** to "Trust this computer?" (the
phone's passcode), and have Personal Hotspot on ("Allow Others to Join"). usbmuxd asks for the trust by itself;
nothing has to run on the encoder. From then on the iPhone comes back without asking.

### 12.1 Tested on the test board (Oct 19)

A test iPhone 15 (`iPhone15,4`, iOS 26.7.1) on the board's USB hub, with the software of 0.7.0:

| | Result |
|---|---|
| Not paired | "Trust this computer?" on the phone at once; trusted → link up and an address by DHCP (`172.20.10.x/28`) in under 1 s |
| Paired, plugged in again | Link and address in 2.7 s, no question |
| The encoder | `90-eirl-uplinks` gives it its own table like any link; the app lists it as `kind: usb`, green; `eth0` keeps the main default route (metric 100, the iPhone 101) |
| Board ↔ iPhone over the cable | ~95 Mbps each way at once (flood ping of 1,428-byte packets to the phone's gateway, 0.5 ms) |
| To the internet | ~3 Mbps down and up: the phone's mobile data where the test was, not the cable |
| Power | The phone charges from the board: 5.12-5.18 V on the board's 5 V rail |

`idevicepair validate` (libimobiledevice-utils, not in the image) answers error -8 with this old libimobiledevice;
it does not affect the link.

### 12.2 The phone's trust is root's only

The pairing record usbmuxd keeps reaches much more of the phone than its tethering (its files, its backups). As
Ubuntu ships it, any user of the board could use it: usbmuxd's socket is `0666` and its records are world-readable
(`/var/lib/lockdown`, `2775`). The image adds `/etc/systemd/system/usbmuxd.service.d/90-eirl.conf`:

- `UMask=0077`: the records are written `0600` (usbmuxd sets its folder back to `2775` on every start, so the
  files are what is protected).
- The socket made `0600`: usbmuxd chmods it to `0666` right after creating it (hard-coded, no option), so the
  drop-in waits for that `0666` and then changes it. If the socket does not show up in 3 s the unit fails:
  closed, not open. In between there are a few milliseconds while it is `0666`, right after an iPhone is plugged in.

None of the encoder's services talks to usbmuxd. The image's check refuses an image with records or a host
identity in `/var/lib/lockdown`: the host identity (`SystemConfiguration.plist`) is made on each device the first
time an iPhone is plugged in.

### 12.3 Traps

- usbmuxd is started by its udev rule (`SYSTEMD_WANTS`) only when the iPhone's device **appears**. Re-authorising
  the USB device (`authorized` 0/1) or `udevadm trigger` with the device already there does not start it again:
  `systemctl start usbmuxd`.
- `ipheth` binds as soon as the phone is in configuration 4 (`eth1` with no carrier): the interface existing
  does not mean the link is there.

### 12.4 Pending

- The link says "USB" on the screen and the web: it should say "iPhone" (from the USB device's product name).
- No hint when an iPhone is plugged in but has no link (not trusted, or Personal Hotspot off): the screen could say
  what to do on the phone.
- The board's hub cut (§8) drops it, as it does the modems.
- On the test board (Oct 19) it was installed by hand: `ipheth.ko` with `tools/modem-modules.sh`'s path
  (`updates/eirl-modems/`), `usbmuxd` and `libimobiledevice-utils` with apt, and the drop-in of §12.2. To undo it:
  `sudo apt-get purge usbmuxd libimobiledevice-utils`, `sudo rm -r /etc/systemd/system/usbmuxd.service.d
  /lib/modules/$(uname -r)/updates/eirl-modems/ipheth.ko && sudo depmod -a`.
