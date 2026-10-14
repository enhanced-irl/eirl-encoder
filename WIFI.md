> **Status:** IN PROGRESS — contract reviewed by the mixer, cloud and screen-header workstreams and **approved on Oct 13**; W-A and W-B in main; W-C installed on the board (§12); W-D and W-E under way
> **Updated:** 2026-10-14

# The board's Wi-Fi: client and access point

What was requested (Oct 13), for the settings menu (the screen's ⚙ "Settings" and the web):

1. The **access point** can be switched on **even if no cameras** are registered.
2. Choose between **client** and **access point**.
3. As a client, **see the list of networks** and **connect by entering the password**.

Today the AP (`ap0`) only comes up if there is some network camera (NETWORK_SOURCES.md §6.2), and the client (`wlan0`) is not
touched from anywhere: NetworkManager leaves it disconnected and without profiles. RTMP/SRT reception can already
be switched off without touching the Wi-Fi (NETWORK_SOURCES.md §6.12).

## 1. Decisions

Taken by the mixer workstream and the F7 workstream on Oct 13, as requested, with the review of the
cloud and screen-header workstreams. **They were approved on Oct 13**, W3 and W5 included.

| | Decision | Why |
|---|---|---|
| W1 | **Two independent switches**: "Join a Wi-Fi network" (client) and "Create my Wi-Fi" (access point). Not a selector | They are two needs that happen at the same time out in the street: the cameras on the AP and, as an output, the venue's Wi-Fi or a phone's hotspot (one more SRTLA link). The radio supports it (`managed ≤ 1, AP ≤ 1, #channels ≤ 2`). Two switches are easier to understand than a selector with an "advanced" option |
| W2 | **Both at once, allowed in v1**, with the channel rule `eirl-netsetup` already has: with the client on 2.4 GHz, the AP on its channel (one channel, without splitting the radio); with the client on 5 GHz, the AP on 2.4 GHz and the radio is split between two channels, with a visible warning ("Wi-Fi will be slower while both are on") | The useful case exists. **It is measured before closing v1** (§8). If it goes badly, that combination is blocked and the warning becomes an error |
| W3 | **The AP belongs to the Wi-Fi, not to the cameras**: it comes up even if there are no cameras. Reception's "Over Wi-Fi" comes to mean "accept cameras through the AP" and only has an effect with the AP up | Requested (1). With the AP up and no cameras, only the AP and its DHCP: not MediaMTX |
| W4 | **By default, today's behaviour**: the AP up if there were cameras registered with "Over Wi-Fi"; the client, without profiles | So that nobody notices the change when updating |
| W5 | **The web is reachable from `ap0`**, as today (the `EIRL-AP` firewall opens port 80): "Create my Wi-Fi" serves to get in from a phone to configure | The risk of NETWORK_SOURCES.md §6.9 remains (the HTTP session can be sniffed on a shared WPA2-PSK). **Accepted on Oct 13** |
| W6 | **The client Wi-Fi password only lives in the NetworkManager profile** (`/etc/NetworkManager/system-connections`, root, 0600). Not in `/etc/eirl-encoder`, nor in logs, nor in the state, nor in any GET, nor on a process's command line (`/proc/<pid>/cmdline` is visible to anyone) | Requested by the cloud workstream (ENCODER_CLOUD.md §10) |
| W7 | **A single AP policy, shared with the cloud**: `always` (= "Create my Wi-Fi" on) or `rescue` (= off; the AP only comes up to rescue the board, once the cloud implements it). It is changed locally and in the app, and the device reports the local change, which the cloud adopts as a new version (same as the EDID and the output) | The cloud already sends `accessPoint: 'rescue' \| 'always'` in every `config.set` (`manager/internal/cloud/state.go`). With two settings, one would overwrite the other |
| W8 | **The client never prevents rescue**: even if it is on, if the board is not linked or has been without internet for 60 s, the rescue AP must be able to come up | A board without a cable and with the wrong Wi-Fi password cannot be left unreachable. Rescue lives in the device (`eirl-netsetup` and the app): the cloud workstream implements it in its F3. This layer leaves room for it |

**Confirmed on Oct 13** (the three workstreams agreed): the cameras **do not force** the AP (W3). With "Create
my Wi-Fi" off and cameras "Over Wi-Fi", the web and the screen warn that those cameras cannot get in. Forcing it
would contradict the switch: you turn it off and it stays on.

## 2. Configuration (`/etc/eirl-encoder/network.json`, additive)

```json
{"v": 1, "wifi": {"client": true, "ap": "always"}, "ap": {"ssid": "…", "channel": "auto", "enabled": true}, "lan": {"enabled": false}}
```

- `wifi.client`: "Join a Wi-Fi network". Off: `wlan0` disconnected and not connecting on its own (the profiles are
  kept).
- `wifi.ap`: `always` (on) or `rescue` (off except for rescue). Without the `wifi` block (older files),
  W4: `always` if there are cameras and `ap.enabled`, `rescue` otherwise; and `client` on (it changes nothing: without profiles
  it connects to nothing).
- `ap.ssid`, `ap.channel` and `ap.psk` are still the AP's. Since Oct 14 the app writes `ap.ssid` (and `ap.psk`) at
  its first start, and the name is changed in Settings and in the wizard (§14). `ap.enabled` is still reception's "Over Wi-Fi"
  (W3). The name stays for compatibility (`netcfg` reads strictly).
- The client's known networks do **not** go here: they are NetworkManager profiles (W6), with the fixed name
  `eirl-wifi-<n>` to tell them apart from those someone else may have set up.

## 3. `eirl-netsetup apply` (root, a single helper)

- **AP**: up if `wifi.ap = always` (or in rescue, once it exists), whether there are cameras or not. With the AP and no
  cameras: profile, DHCP and the `EIRL-AP` firewall; not MediaMTX.
- **Channel**: today's rule (§6.2 of NETWORK_SOURCES.md): with the client on 2.4 GHz, its channel; otherwise, the one
  the AP has while on; otherwise, the least busy of 1/6/11. 🔴 **The AP's channel is chosen when it is switched on, or when
  someone connects the client by hand while not streaming. It never changes on its own with the output `on` or `connecting`**:
  moving the AP drops the cameras, and a camera on air would go black. If the client changes channel while streaming
  (another network, or another BSSID of the same one on another channel), both channels are accepted (the W2 warning) and the AP is
  relocated when streaming stops. When connecting the client by hand while not streaming, the app warns that the AP's cameras will
  reconnect.
- **MediaMTX**: with cameras, reception on and a way in (`ap.enabled` with the AP up, or the
  cable).
- **AP mode**: with `always`, today's mode with cameras (its own password, no router nor captive DNS), whether there are
  cameras or not. It is renamed **`normal`** in `state.json` (`mode`); the app also accepts `cameras` (the
  old one) while versions coexist. D2 of ENCODER_CLOUD.md is rewritten by the cloud workstream.
- `state.json` gains `ap: "always" | "rescue"`. The `setup` and `rescue` modes (the cloud's F3) will be decided by an
  input that `eirl-netsetup` reads as data, just like `network.json`: no other helper is needed.
- For F3 (only noted): with the AP on `always` and cameras streaming, rescue does **not** switch it to captive DNS
  nor announce a router (the trap of ENCODER_CLOUD.md §8): it is enough for the setup code to open a session over
  that same AP.
- `eirl-netsetup` does **not** touch the client: the app handles it through NetworkManager with polkit permissions (§4).
  That way the root helper still receives nothing but the files in `/etc/eirl-encoder`.

## 4. Client (`wlan0`), in the app

- **Permissions**: a `.pkla` for the `eirl-encoder` user with
  `org.freedesktop.NetworkManager.{network-control,settings.modify.system,wifi.scan}` (D10 of
  ENCODER_CLOUD.md). No `CAP_NET_ADMIN`.
- **Scan**: requests a rescan and reads the list. NM 1.10 rejects two in a row ("Scanning not allowed
  immediately"): it is retried and the latest result is served (≤ 10 s). Each network: SSID, signal (0-100), security (`open`,
  `wpa`, `wpa2`, `wpa3`, `enterprise`), band and channel, whether it is known and whether it is the connected one. `enterprise`
  (802.1X) and `wpa3` (SAE only: Ubuntu 18.04's wpa_supplicant 2.6 has no SAE) appear in the list but cannot
  be connected to in v1. Mixed WPA2/WPA3 ones get in through WPA2.
- **v1 limitation**: hidden networks (no SSID in the beacons) do not appear in the scan and cannot be
  added.
- **Connect**: creates or updates the `eirl-wifi-<n>` profile (system-wide, `autoconnect` yes) and activates it. **The
  password goes over D-Bus** (`AddAndActivateConnection`/`Update`), not as `nmcli` arguments (W6); if `nmcli` has to be
  used, through its standard input, checked on the board first. Validation: SSID of 1-32 bytes; password
  of 8-63 printable ASCII characters or 64 hexadecimal ones; open network without a password.
- **Forget**: deletes the profile.
- The client network with a gateway is one more SRTLA link (dispatcher `90-eirl-uplinks`, `kind: wifi`), and
  it can be disabled like any link. **Captive portals** (hotel, bar): the client connects and has
  a gateway, but no internet. The state says so (`internet: false`, "connected, no internet"): the app
  checks the way out to the internet before accepting it as a link.
- **Scanning takes the radio off the AP's channel** for a moment: with cameras on the AP it can cause stutters. The app only
  scans when asked, but NetworkManager scans on its own in two cases we do not fully control: (a) with the
  client on and **no known network in range**, every few seconds, with a back-off that grows up to
  ~2 min; (b) already connected, wpa_supplicant's `bgscan` (NM sets `simple:30:-65:300`: every 30 s with the signal
  below −65 dBm, every 300 s otherwise). All three are measured (§8). If they cause stutters, for v1: warn in the web
  and on the screen ("while the Wi-Fi client is searching, Wi-Fi cameras may stutter") or remove
  autoconnect while there is a network camera on air. The `SCAN-FAILED` messages on `ap0` could be this same periodic
  scan.

## 5. Local API (`/api/v1/network/wifi…`, additive)

Behind the web session (the cloud will add the rescue credential of its §10 on top) and in the whitelist
of the screen's local socket (MANAGER.md §3, "Local socket").

| Method and route | What |
|---|---|
| `GET /network/wifi` | `{"client": {"enabled", "state": "off"\|"disconnected"\|"connecting"\|"connected"\|"error", "ssid", "signal", "band": "2.4"\|"5", "channel", "ip", "internet": bool\|null, "error"?}, "ap": {"policy": "always"\|"rescue", "state", "ssid", "channel", "clients"}, "warning": null\|"two_channels"}`. No secrets |
| `PUT /network/wifi` | `{"client"?: bool, "ap"?: "always"\|"rescue"}` → the same as the GET (`202` if it was saved but not applied) |
| `GET /network/wifi/scan` | `{"networks": [{"ssid", "signal", "security", "band", "channel", "known", "connected"}], "at"}`. **Answers immediately** with the latest scan (and its `at`) and starts a new scan in the background if what it has is older than 10 s: whoever shows the list requests it again every 2-3 s. `503 scan_busy` only if there has been no scan yet (the screen shows it as "Searching for networks…") |
| `POST /network/wifi/connect` | `{"ssid", "password"?}` → `202` (connecting: the state shows up in the GET) · `400 validation` (also without a password on a secured network that is not known) · `404 not_found` (not in the scan) · `409 unsupported` (`enterprise` or `wpa3`). A **known** network without `password` uses the saved profile; with `password`, it changes it. **The body is never logged**: the warnings only carry the error code |
| `POST /network/wifi/forget` | `{"ssid"}` → `204` · `404` |

- The AP's name: `accessPoint.name` in `GET /network`, changed with `PUT /network/ap {"ssid"}`, with the on-air rule
  of switching it off (§14).
- `PUT /network/wifi` also works with the board linked (`managedBy = eirl`): it does not return `409` like the destination does.
- `GET /network` and `/status`: `accessPoint` gains `policy`, with the real value from `network.json` (W4 rule
  included). For the cloud, `status.network.wifi {ssid, signal}` (today always `null`) and the `wifi` capability in the
  `hello`. The cloud workstream integrates it in ENCODER_CLOUD.md §5.
- **The scan and the known networks never go to the cloud**: the surrounding SSIDs reveal where the board is. Only
  the connected network goes to the cloud (`ssid`, `signal`).
- v1 limitation: a hidden network does not appear in the scan, so `connect` returns `404 not_found`.
- Wrong password: NM considers it failed after its timeout (~25 s). The state becomes `error` with
  `"error": "wrong_password"`, and the profile stays so it can be corrected.

**For the security review**: the polkit permission `settings.modify.system` lets the app touch **any**
NetworkManager profile, not only the `eirl-wifi-*` ones (also the cable one or the AP's). The app only creates, changes and
deletes its own, but the permission does not limit it.

## 6. Screen (screen-header workstream; done in W-C, §12)

- In ⚙ "Settings", "Wi-Fi" (tabs were chosen: "Wi-Fi | Network cameras"): the two switches, the list of networks (when opened, not in the background) and a touch keyboard
  for the password: full-screen QWERTY over Settings, with uppercase and numbers-and-symbols layers
  (all printable ASCII), keys of 44 px or more, dots only and a "12/63" counter. With `wrong_password`, "Wrong
  password" and the keyboard opens again, empty.
- **What is typed lives only in the screen's memory**: masked, it is erased on sending, cancelling, closing Settings or
  locking the screen (locking it closes Settings). It goes straight to the app's socket, from the thread that logs
  neither the request nor the body. It **never** goes through the engine's socket, nor its state (`mixer.json` goes to all the
  web's subscribers), nor the journal (the engine logs every command on its socket).
- **The screen snapshot** (`/run/eirl-engine/screen.jpg`, one per second, visible in the web) includes the
  interface layer: dots only (never "the last character for a moment"), and **with Settings open, and for 2 s after
  closing it, it is not updated** (MIXER.md §18). That covers the keyboard and the AP's QR, which carries its password.
- In the header, a small antenna icon (it is not a button) only for the **client**, which is what matters
  while streaming (an SRTLA link): arcs according to the signal if connected, grey if searching, amber on error, and no
  icon with the client off. The AP does not appear in the header: the gear's amber dot covers it if it has been
  requested and is not up. **Approved with the PNG mock-ups on Oct 13**, with one addition: connected without internet, the
  arcs in amber.
- The engine never calls NetworkManager nor `eirl-netsetup`: only the app does.

## 7. Cloud (cloud workstream)

🔴 **Safe order.** The backend sends `accessPoint` in **every** `config.set`. When linking, it adopts what the device
says in the `hello`, and today the device sends there the value from `cloud.json`, which defaults to `rescue`, not the
real policy. If the device started obeying it, the first `config.set` of anything (a bitrate)
would put in `rescue`, the AP would switch off and the cameras would drop live. That is why:

- **In W-A and W-B, the device still does not obey the cloud's `accessPoint`**, as today: it only stores it in
  `cloud.json`.
- Since W-A, `/status` carries `network.accessPoint.policy` with the real value. It breaks nothing: the backend discards what
  it does not validate yet.
- The app offers a read-only function with the **effective policy** (W4 included), so that the cloud does not
  duplicate the rule.
- In W-E, the cloud workstream wires it up in this order: (1) the backend adopts the policy from the state, just
  like the EDID and the output; (2) the `hello` and the state take it from `network.json`, not from `cloud.json`; (3)
  last, the device obeys `config.set`.

Also: fill in `status.network.wifi`, announce the `wifi` capability and, in F3, the setup/rescue mode
on top of this layer (setup code, W8). It goes in ENCODER_CLOUD.md §5 and §8, with a link here.

## 8. Measurements before closing v1

With the mixer workstream's meters (`tools/engine-watch.py`, `tools/engine-cpu.sh`), the engine's
output (`sentKbps`, `rttMs`) and the relay's API:

1. One camera through the AP and SRTLA going out over the Wi-Fi client, on the **same 2.4 GHz channel**: losses, RTT,
   the camera's fps.
2. The same with the client on **5 GHz** (two channels): if it is much worse, W2 gets stricter.
3. Scans with a camera coming in through the AP: the one requested by hand; the one of the client switched on **with no known
   network in range**; and the `bgscan` once connected with a **weak signal** (< −65 dBm, every 30 s). Stutters and
   losses in each case.
4. That the `SCAN-FAILED` messages on `ap0` do not come back (fix from another workstream, NETWORK_SOURCES.md §3).

## 9. Phases (each one with the maintainers' OK)

| Phase | What | Workstream |
|---|---|---|
| W-A | AP independent of the cameras and its policy (`network.json`, `eirl-netsetup`, API, web) | F7 · **in main and installed** (§10) |
| W-B | Client: NM permissions, scan, connect and forget, API and web | F7 · **in main and installed** (§11) |
| W-C | Screen: switches, list and keyboard | screen header · **installed on the board** (§12) |
| W-D | Measurements (§8) | F7, with the mixer workstream's meters |
| W-E | Cloud, in the order of §7: adopt the policy, take it from `network.json`, obey `config.set`; `status.network.wifi`; then, the setup/rescue mode (F3) | cloud |

## 10. W-A: in main (Oct 13), installed and tested on the board

- `netcfg`: a `wifi` block (`ap`, `client`) in `network.json`, only written if something is chosen, and
  **`Network.APPolicy(hasCameras bool) string`**: the effective policy with the W4 rule, a single one for
  `eirl-netsetup`, the app and the cloud (§7). In the app, `network.Manager.APPolicy()` computes it with the real
  cameras.
- `eirl-netsetup`: the AP follows its policy, whether there are cameras or not, in `normal` mode. `state.json` gains `ap`. RTMP and
  SRT are only opened on `ap0` if the cameras come in over the Wi-Fi, in both chains (`EIRL-AP` and `EIRL-MEDIA`); with
  the AP only for configuring, `ap0` only lets through DHCP, the web, the preview and ping.
- App: `GET` and `PUT /network/wifi` (also over the local socket), `accessPoint.policy` in `GET /network` and
  `/status`, and `accessPoint.policy` in the state that goes to the cloud (**the device still does not obey the
  `accessPoint` of `config.set`**). The screen's file (`wifi.json`) follows "Create my Wi-Fi", not the
  cameras.
- Web: a "Wi-Fi" card with "Create my Wi-Fi" and the "Wi-Fi network" panel (name, channel, password and QR, which move out of
  "Network cameras"). In "Network cameras", "Through the encoder's Wi-Fi" warns if "Create my Wi-Fi" is off.

After code review (Oct 13):
- `PUT /network/wifi {"ap": "rescue"}` performs the reception check: `409 on_air` if streaming with a
  camera on air that comes in **over that Wi-Fi** (over cable, no) and `503 mixer_unreadable`.
- Switching off reception's "Over Wi-Fi" never switches off the AP: on a board where "Create my Wi-Fi" was never chosen, it first sets
  `wifi.ap` to the policy at that moment (W4) and then changes "Over Wi-Fi".
- With "Over Wi-Fi" off and the AP up, RTMP and SRT are closed on `ap0` **before** letting established traffic through:
  a camera that was already streaming over the Wi-Fi is cut off immediately.
- Retry brings up again a requested AP that stays in error even if there are no cameras.
- Left this way on purpose: `409 no_input` still looks at the reception switches ("Over Wi-Fi" and "Over
  cable"), not at whether the AP is up. With "Create my Wi-Fi" off, the web warns instead of blocking (W3).
- 🪤 **Going back to an older app** with the `wifi` block in `network.json`: its parsing is strict, so that
  `eirl-netsetup` leaves everything down with an error until the versions match again. Before rolling back,
  remove the `wifi` block from the file.

## 11. W-B: the Wi-Fi client (Oct 13, in main, installed and tested on the board)

- **`internal/wifi`**: the client on top of NetworkManager over **D-Bus** (`github.com/godbus/dbus/v5`, pure Go). The
  password only goes in the settings of `AddAndActivateConnection`/`Update` (NM stores it in its root keyfile): not
  in a process's arguments, nor logs, nor state, nor app files. Profiles `eirl-wifi-<n>`, system-wide,
  bound to `wlan0`, IPv4 by DHCP and IPv6 `ignore` (SRTLA goes over IPv4). Other profiles are neither touched nor shown.
- **Permissions**: `deploy/eirl-encoder-nm.pkla` (polkit 0.105) gives `eirl-encoder` `network-control` and
  `settings.modify.system`. `install.sh` installs it.
- **Scan**: whatever NetworkManager knows, immediately, and a `RequestScan` in the background at most every 10 s
  (the "not allowed immediately" does not matter). Left out: hidden ones, our own AP and names that cannot be displayed.
  One row per SSID (the one with the strongest signal), with the connected one first and then the known ones. Security from NM's
  flags: `wep` also shows and is not supported.
- **Connect**: with the network in the scan. Known and without a password, the saved profile; with a password, it changes it.
  Unknown, secured and without a password: `400`. The attempt is watched for up to 45 s. With
  `NO_SECRETS`, `wrong_password`; with `SSID_NOT_FOUND`, `not_found`; with anything else, `connect_failed`. The state
  stays at `error` and the profile stays. "Join a Wi-Fi network" must be on (`409 client_off`).
- **"Join a Wi-Fi network"**: `wifi.client` in `network.json`. Off is `Device.Disconnect` + `Autoconnect=false`;
  on, `Autoconnect=true`. NetworkManager does not remember it, so the app sets it again at startup (there are a few
  seconds of boot in which NM may connect on its own before the app arrives).
- **Internet**: with the client connected, an HTTPS check from `wlan0`'s IP (the links dispatcher
  gives it its routing table, so it goes out over the Wi-Fi client), with hysteresis. `null` until it is known. A captive
  portal cannot fake the certificate: `internet: false`, and the Wi-Fi **leaves SRTLA's distribution**. Where to, how
  often and how: §13.
- **AP channel**: with the AP up, its own, **always**: it does not follow the client (§3, requested by the mixer
  workstream). If the client ends up on another channel, `warning: two_channels` and the web explains it. To make them share,
  switch "Create my Wi-Fi" off and on while not streaming.
- **API**: `PUT /network/wifi {"client"}`, `GET /network/wifi/scan`, `POST /network/wifi/connect` and
  `POST /network/wifi/forget`, also over the screen's socket. `GET /network/wifi` fills in `client` (`null` without
  `wlan0`) and `warning`. `/status` gains `wifi` (the client). To the cloud, `status.network.wifi {ssid, signal}` and the
  `wifi` capability.
- **Web**: in the "Wi-Fi" card, "Join a Wi-Fi network", its state (network, signal, channel, "no internet"), the
  two-channel warning and the "Networks" list (refreshed every 3 s while open). The password, in a password-type
  field that is emptied on sending or cancelling.

After the code review of W-B (Oct 13):
- The connection attempt is followed through the `Device.StateChanged` signal (a bus connection just for that), subscribed
  before activating. Reading the state at intervals lost the reason: with a bad password, NetworkManager 1.10 goes
  through FAILED (`NO_SECRETS`) and right away to DISCONNECTED with reason 0. Without the signal, at intervals (`connect_failed`).
  Only the requested network counts as connected: when switching from one to another, the previous one stays activated for a moment.
- On error, `client.ssid` is the network that failed (the screen uses it for "Wrong password for X").
- `409 uplink_in_use`: with the Wi-Fi client carrying the stream (streaming over SRTLA and `wlan0` in use), you cannot
  switch off "Join a Wi-Fi network", change network or forget the connected one.
- Connect, forget and the switch go one at a time (otherwise, two simultaneous additions duplicated the profile).
- `/status` carries in `wifi` the same as `GET /network/wifi`, `two_channels` warning included: the web does not repeat the rule.
- **Decided this way**: "Join a Wi-Fi network" rules over the board's whole Wi-Fi client. Off, `wlan0` does not
  connect on its own to anything, not even a profile made by hand with `nmcli`. The board is administered over cable.
- **Risk accepted for v1 (Oct 13)** (the same thing the `.pkla` says): `settings.modify.system` and `network-control`
  let the app (user `eirl-encoder`) change, delete or read the secrets of **any**
  NetworkManager profile, not only the `eirl-wifi-*` ones. If someone took control of the app, they could touch the cable or the AP.
  polkit 0.105 does not allow restricting it per profile. The alternative would be a root helper just for the Wi-Fi client (like
  `eirl-netsetup`), with the password coming in through its standard input: more pieces, for the next round.

**Tested on the board (Oct 13, 19:15-19:20)**, app only:
- Scan over the socket, like the engine: 3 networks in about 80 ms, without ours.
- polkit: `Autoconnect` yes/no over D-Bus.
- Errors: `409 client_off`, `404 not_found` and `400 validation`.
- The journal carries neither passwords nor SSIDs.
- Web at 390 px.
- The board was connected to a test iPhone's hotspot from the web: connect, forget and connect again. It stayed
  connected on 5 GHz (channel 44) with `internet: true` (the check went out over the phone's Wi-Fi), and the dispatcher
  counts it as an SRTLA link (`wlan0` in `/run/eirl-uplinks/available`).
- Pending for W-D: measure with a camera through the AP and SRTLA over the client (§8).

## 12. W-C: the screen (Oct 13, installed on the board)

Details in MIXER.md §19. What matters for this contract:

- **Settings with tabs**: "Wi-Fi" = "Join a Wi-Fi network" (with "Networks ›") and "Create my Wi-Fi", and below them
  the AP's QR, network and password; "Network cameras" = reception and the cameras (NETWORK_SOURCES.md §6.12).
  Each whole row is its switch. The app's errors are shown with their `message` as is (`409 on_air`,
  `409 uplink_in_use`, `409 client_off`, `400 validation`…).
- **List**: only while open, `GET /network/wifi/scan` every 2.5 s (`503 scan_busy` = "Searching for networks…"). Open or
  known, `connect` without a password; secured and unknown, the keyboard; `wep`, `wpa3` and `enterprise`, greyed out.
  "Forget" with a second tap.
- **Keyboard**: as §6 asks (QWERTY with layers, all printable ASCII, keys of ≥ 44 px —92×101 at 1024×600—, dots
  only, `n/63` counter, "Connect" from 8). With `wrong_password` for that network, it opens again empty.
- **The typed password** lives only in the screen and goes in the body of `POST /network/wifi/connect`, through the
  thread that logs nothing; it is emptied on connecting, cancelling, closing Settings and locking. It never goes through the
  engine's socket, nor `mixer.json`, nor the journal. `screen.jpg` is not updated with Settings open (the
  keyboard is inside it).
- **Antenna** in the header (§6), client only. To have it with Settings closed, the screen requests
  `GET /network/wifi` while closed too, every 15 s (8-12 ms in the app); while open, every 2 s.
- **With an older app**: a `403`/`404` on `/network/wifi` removes the client row (and the screen does not ask
  again until Settings is reopened); without `accessPoint.policy`, "Create my Wi-Fi" shows without a switch; without the socket, the
  Wi-Fi from `wifi.json` (NETWORK_SOURCES.md §6.4).
- **Tested on the board** (Oct 13, 19:07 step 1 and 19:25 the rest): the engine requests over the socket `GET /network/wifi`
  (200 in 8-12 ms), the scan (45 ms) and the credentials; the antenna shows all three arcs with the board on a
  test iPhone's hotspot. `draw()`: keyboard 9.5 ms, list 51 ms, Settings 43-47 ms.

## 13. The Wi-Fi client without internet, out of SRTLA's distribution (Oct 13, with the mixer workstream)

A captive portal (hotel, bar) gives a connection and a gateway, but no internet. The dispatcher counted that network as
one more link, so the stream said "in use" and, if it was the only one, it stayed connecting until SRT's timeout.

- **App** (`network.Manager.MonitorWiFi`, `wifi.Client.Monitor`): every 30 s, even with nobody watching, it checks
  the way out to the internet from `wlan0`'s IP. It does so over HTTPS **with the certificate verified**: a portal that
  intercepts HTTPS also counts as "no internet". The check goes against **the destination's `https://<relay>/ping`**
  if the destination is `*.relay.enhancedirl.com` (the Caddy on the production relays answers 204), and otherwise
  against the cloud API; that way, an API outage does not knock down a link that works.
- **Hysteresis in both directions** (each change is a SIGHUP to `srtla_send` and a new distribution): it takes
  2 consecutive checks saying the opposite and, except the first time, 60 s since the last change. Transient
  issues do not count (NetworkManager not answering, having nowhere to check against, a new DHCP IP on the
  same network): what is known is kept. Only a disconnection forgets it. If the file cannot be written, it is retried
  on the next round.
- While it is "no internet", the app puts `wlan0` in **`/run/eirl-encoder/uplinks-nointernet`** (WriteAtomic,
  0640, group `eirl-encoder`; the format of `uplinks-disabled`). It empties it when internet comes back or on disconnecting. It does not touch the
  user's list of disabled links.
- **Engine**: `selectUplinks(available, excluded...)` (excluded = disabled ∪ no internet), in `internal/engine/output`,
  with `UPLINKS_NOINTERNET`. **If the last link goes away** while streaming,
  the uplinks watcher stops `srtla_send` and the session ends in `no_uplinks`, transient: "connecting · no
  links", with retries at 2/4/8/10 s that wait for one to come back. Before, `srtla_send` stayed alive without
  links and the output ended in `connection_lost`. An older engine ignores the file and an older app does not
  write it.
- **State**: `/status` → `uplinks[].noInternet`. In the web, "No internet" in amber on the "Links" card, and the
  link does not count as usable. The screen counts the links with the same rule as the output ("SRTLA · N
  links").
- **Cloud**: `status.uplinks[].noInternet` (omitempty). The backend rebuilds each link with the fields it knows,
  so it breaks nothing; the cloud workstream adds its reading, the app's "No internet" label and §4.1 of the
  contract.

**Tested on the board (Oct 13, 20:46-21:01, in a reserved board slot)**, streaming over SRTLA to eu1 with `eth0` and a
test iPhone's hotspot. iOS switches off the hotspot when mobile data is turned off, so the captive portal was simulated
with a temporary `iptables` rule (OUTPUT on `wlan0`, except DHCP), since removed:
- If the Wi-Fi disappears, the stream continues over the cable only, without dropping.
- With the simulated portal, "no internet" after ~56 s (two checks) and out of the distribution, without dropping.
- If the cable is disabled as well (no link left), `srtla_send` stops and the output stays in
  `connecting · no_uplinks`, with retries at 2 and 4 s. When the cable comes back, it streams again in ~4 s.
- With the rule removed, the Wi-Fi returns to the distribution in ~35 s.

## 14. The name of "Create my Wi-Fi" (Oct 14)

The access point's name (its SSID) is the one the cameras and phones join. It is no longer a fixed value nobody
sees until the first camera: it exists from the first boot and can be changed in Settings and in the setup wizard.

- **Where it comes from.** At its first start, `eirl-encoderd` writes `network.json` with
  `ap.ssid = "EnhancedIRL-" + the last 4 characters of the hardwareId` (`netcfg.DefaultSSID`, like the cloud's mDNS;
  `EnhancedIRL-Cam` without a usable hardwareId), channel `auto`, and `ap.psk` (`network.Manager.EnsureNetwork`).
  Before Oct 14 both appeared with the first camera or the first "Create my Wi-Fi". Nothing is applied: with no
  cameras and "Create my Wi-Fi" off, `eirl-netsetup` reconciles to the same "off" as with no file.
- **Never overwritten.** `EnsureNetwork` is create-only: a file that is there (valid or not) is never rewritten at
  start, so a new hardwareId, a reinstall or an update keep the name. When a later network change has to rebuild a
  `network.json` this version cannot parse (another version's fields), it keeps that file's name if it is valid
  (`netcfg.LooseSSID`) instead of going back to the default.
- **Rule** (`netcfg.ValidSSID`, the same in the web and on the screen): 1-32 characters, letters without accents,
  digits, space, dot, hyphen and underscore, no space at either end (the NetworkManager keyfile would trim it).
- **API.** `GET /network` (and `/status`, `network`) carries `accessPoint.name`: the saved name, also with the AP off
  (additive; `accessPoint.ssid` stays the name `eirl-netsetup` brought it up with, `null` off). It is changed with
  `PUT /network/ap {"ssid"}` (the route the web already used): `400 validation` with `fields.ssid`; saved and, if
  the AP is up or requested, applied (`202` if not applied, as before).
- **What a rename does.** The AP restarts with the new name: every device on it drops and has to **join the new name**
  (the old network disappears; they do not follow on their own). The QR, the screen's `wifi.json` and
  `GET /network/credentials` carry the new name as soon as it is saved. The web and the screen say so in es/en/fr: a
  note under the field and a question first if devices are connected (web), the keyboard's help line and a notice
  afterwards (screen).
- **On air.** The same rule as switching "Create my Wi-Fi" off (§10): while streaming with a camera on air (main,
  secondary in PiP or side by side, or its audio alone) that comes in **over this Wi-Fi**, the rename is refused:
  `409 on_air` with its own reason (`network.ap_rename_on_air`: put another source on air or stop streaming first),
  and `503 mixer_unreadable` if the mixer state cannot be read. The check runs with the network lock held and only
  when the name changes; over cable, or with the AP off, nothing is checked. The channel and "New password" keep
  their behaviour (no check).
- **Web.** In the "Wi-Fi" card, "Name of the encoder's Wi-Fi" with "Rename", shown with "Create my Wi-Fi" on or off;
  the web wizard's network step borrows that card, so it shows the generated name and lets it be changed. The name
  left the "Wi-Fi network" panel (channel, password and QR). The field is not a `<form>`: inside the wizard's form,
  Enter renames instead of going to the next step.
- **Screen.** Settings → Wi-Fi (the page the wizard's network step shows): "Rename" under the QR; with the AP off,
  the name next to it. The existing keyboard (visible text, `n/32`); the screen checks the rule, sends nothing for
  the same name and calls `PUT /network/ap {"ssid"}` over the local socket (already on the whitelist: no new route).
  The app's answer becomes the state and its refusal is shown as is.
- **Simulator.** `eirl-encoder-sim -http … [-fresh] [-hardware-id <hex>]` runs the real network manager with a fake
  `eirl-netsetup` (SETUP.md §9): the name, its card and the wizard step can be tried without the board.
