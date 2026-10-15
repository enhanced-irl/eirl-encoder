> **Status:** IN PROGRESS · **Updated:** 2026-10-15
>
> Device settings, first-boot wizard, source slots and the languages of the API messages, app side
> (`eirl-encoderd`). The screen and the web build on this API; the engine side of the slots is in
> [`ENGINE_GO.md`](ENGINE_GO.md) §12; updates are in `UPDATES.md`.

# Device settings, setup wizard and source slots

## 1. What there is

| Piece | Code |
|---|---|
| Message catalog (es/en/fr) and language negotiation | `manager/internal/i18n` |
| `device.json`, migration, hostname derivation, setup code, time zone/NTP/clock/hostname | `manager/internal/device` |
| `slots.json`: validation, load, atomic save | `manager/internal/slots` |
| Slot command and the engine state fields (`alias`, `slotConfig`, `catalog`) | `manager/internal/mixer` |
| HTTP API (`/device*`, `/setup*`, `/sources/slots`) and the local-socket whitelist | `manager/internal/api/{device,setup,slots,local,lang}.go` |
| Wiring and flags | `manager/cmd/eirl-encoderd/main.go` (`setupDevice`) |
| Privileges | `manager/deploy/eirl-encoder-time.pkla` (installed by `install.sh`) |
| Simulator (local mode) | `manager/cmd/eirl-encoder-sim/{local,engine,update}.go` |

## 2. Device settings

`/etc/eirl-encoder/device.json`, owned by the app, written atomically (temporary file in the same directory +
fsync + rename), mode **0640** like the rest of the setgid `/etc/eirl-encoder` (the group reads it, only the app
writes it; the contract said 0660, but group write adds nothing the app needs):

```jsonc
{ "v": 1, "language": "en", "name": "EIRL Encoder",
  "setup": { "done": false, "completedAt": null, "skipped": [], "completed": [] } }
```

- `language`: `es` | `en` | `fr`. The touch screen always uses it; the web uses it unless the browser overrides it.
- `name`: display name, 1-40 runes after trimming: Unicode letters and digits, space and `-_.'`. Typographic
  apostrophes from phone keyboards (`’ ‘ ʼ ‛`) are turned into `'` before validating and stored that way
  (`device.NormalizeText`; the same for slot aliases). Typographic double quotes are not touched: no rule allows `"`.
- `setup.completed` is additive to the contract: the steps marked done, so the wizard resumes after a restart.
- Time zone, NTP and the clock are **not** stored here: they are read from and written to the system.

**Migration** (first start of an app with this code): `device.json` missing **and** `admin.hash` present → a board
set up before the wizard existed: `{"language":"es","name":<hostname>,"setup":{"done":true,…}}`. Both missing → a
fresh device: English, `setup.done=false`. The file is written at once in both cases, so a password set later by
the wizard never turns the next start into a migration. An unreadable or invalid `device.json` is logged and left
alone; the migration rule applies in memory until the next save.

**Hostname** is derived from the name when the name is set (wizard or settings): lowercase ASCII `[a-z0-9-]`,
1-40 characters, the usual Latin diacritics transliterated (`á→a`, `ñ→n`, `ß→ss`…), other characters → `-`,
`eirl-` in front if it does not start with a letter: "Estudio 2" → `estudio-2`, "2º plató" → `eirl-2-plato`, "日本"
→ `eirl-encoder`. Set with `hostnamectl --static --transient set-hostname` (not `--pretty`: that needs another
polkit action). The web's Host check reads the current hostname on each request, and keeps accepting the hostname
the app started with (mDNS may keep announcing it until a restart, see §7).

### API

| Route | Body | Answer |
|---|---|---|
| `GET /device` | — | `{"language","languages":["es","en","fr"],"name","hostname","timezone","time":{"now":RFC3339 in the device zone,"ntp","synced","rtc"?},"version":{"release","app","engine"}}` |
| `PUT /device` | any of `{"language","name","timezone","ntp":bool,"time":RFC3339}` | `200` + the same as GET · `400 validation` (`fields`: `language`, `name`, `timezone`, `time`) · `502 system_failed` (the system refused: the message says which part) · `503 setup_required` (web, no password, anything but `language`) |
| `GET /device/timezones` | — | `{"zones":[{"id":"Europe/Madrid","offset":"+02:00"},…]}`, sorted by id; the list is cached, the offsets recomputed every 10 min |

- `time` only with automatic time off (`ntp:false` in the same request or already off): otherwise `400` with
  `fields.time`. Years 2020-2100. timedatectl reads the time in the system zone, so the app writes it in that zone.
- Order of a `PUT`: hostname, time zone, NTP, clock, then `device.json` (language, name). A system failure stops
  there (the earlier parts stay applied) and answers `502`.
- `version.release` = first field of `/usr/lib/eirl-encoder/VERSION` (`-device-version-file`), `version.app` = the
  binary's `main.version`, `version.engine` = first field of `/usr/lib/eirl-encoder/engine/VERSION`
  (`-engine-version-file`); `""` if a file is missing.

## 3. Languages and the API messages

- Codes `es`, `en`, `fr`. The language of a request: the first supported primary tag of `Accept-Language` (by
  q-value, then order), else the device language, else English (`i18n.Negotiate`). The web sends its language on
  every fetch; the screen sends the device language.
- Every error keeps its stable `error` code and JSON shape (`{"error","message"}`, or `{"error":"validation","fields":{…}}`);
  only `message` and the field texts are translated, server-side, from the catalog (`internal/i18n/catalog.go`;
  the Spanish text the API used before is the `es` entry, verbatim).
- `config.FieldErrors` stays `map[string]string` (internal/cloud builds and reads it): its values are **i18n refs**
  (`i18n.Ref`: an opaque string with key and arguments) rendered per request (`FieldErrors.Render(lang)`); a plain
  string is shown as is. `FieldErrors.Error()` is English, for logs.
- Validation errors of packages (`config.ParseURL`, `mixer.Command.Validate`, `mixer.CheckPauseText`) are
  `*i18n.Error`; errors of packages without a catalog (auth, cloud pairing, network, wifi) are mapped **by error**
  in `internal/api` to a catalog key — their own text is never forwarded.
- **The cloud** keeps receiving the same codes; the messages of command results (those of `api.CloudDevice` and
  those the cloud client produces itself: `busy`, `bad_request`, `unknown_action`, `unsupported`, `failed`,
  `validation`) are in the device language (`cloud.Client.Lang`).
- **eirl-netsetup** (the root helper of the access point) writes stable codes in its state (`error`/`lanError`:
  `no_wifi`, `subnet_in_use:wwan0`, `ap_dhcp`…, never free text); the API renders them (`netsetup.*`) wherever the
  network state is served, the SSE status in the device language. An older helper's text passes through.
- The catalogue's default source names (`HDMI de la placa`, its audio, `Cámara USB v:p`) are data (the engine shows
  them, `probe -list` carries them); `GET /sources` and the slots editor render them in the request language. The
  mixer state (`GET /mixer`, SSE) carries the engine's names as they are: the UIs may localise `hdmi` by id.
- The boot splash draws "Restarting…" / "Shutting down…" in the language of `device.json` (`eirl-splash
  -device-json`).
- Logs are English and never translated. `GET /sources` names the test pattern in the request language.
- The catalog test checks that every key has the three languages with the same format verbs.

## 4. First-boot wizard

Steps (ids): `language` → `time` (time zone + date/time: NTP when online, manual otherwise) → `network` (Wi-Fi
client or cable; skippable) → `password` (web admin password) → `name` → `cloud` (link to EnhancedIRL; skippable)
→ done. The wizard drives the order; the API only stores how each step ended. Boards already configured are
treated as done (migration).

The `network` step also shows the name of the encoder's own Wi-Fi ("Create my Wi-Fi", the one cameras and phones
join), with the option to change it (since Oct 14). The app generates it at its first start (`EnhancedIRL-` + the end
of the hardwareId, `network.json`), so it is there before anything is set up; changing it is `PUT /network/ap
{"ssid"}`, the route Settings uses, with its validation and its on-air rule (WIFI.md §14). It adds nothing to this
API: the step ids, the routes below and how a step ends are unchanged, and renaming does not mark the step.

- **Touch screen** (local socket): any order, the one above. No session and no setup code (the socket is the
  physical screen); it shows the setup code (from `GET /setup`) so the web can be used too.
- **Web** (only when there is no screen): before an admin password exists the web may change **nothing but the
  language**, so its order is `language` → `password` (with the setup code; this opens the session) → `time` →
  `network` → `name` → `cloud`. This is on purpose: on a fresh device anybody on the LAN reaches the web, and the
  setup code is what proves physical access.

| Route | Body | Answer |
|---|---|---|
| `GET /setup` | — | `{"done","steps":[{"id","state":"todo"\|"done"\|"skipped"}],"needsPassword","codeRequired","language"}` + `"code"` over the local socket while there is no password. No session needed; `Cache-Control: no-store` |
| `POST /setup/password` | `{"password","code"?}` | `204` (web: + the session cookie, like a login) · `400 validation` (`fields.password`, `fields.code`) · `403 bad_code` · `429 rate_limited` · `409 password_exists` · `503 setup_code_unavailable` |
| `POST /setup/step` | `{"id","state":"done"\|"skipped"}` | `200` + the same as `GET /setup` · `400 validation` · `409 password_required` (marking `password` done without one). `password` cannot be skipped |
| `POST /setup/complete` | `{}` | `200` + view · `409 password_required` |
| `POST /setup/restart` | `{}` | (session, or the local socket) `200` + view: the wizard again; the password is kept |

The `password` step is `done` whenever a password exists; with `setup.done`, every step not skipped is `done`.
`language` (device language) and `code` in `GET /setup` are additive to the contract: the web needs the language
to draw the sign-in form or the wizard before a session.

**Access without a password (web).** Only `GET /setup`, `POST /setup/password|step|complete`, `GET /device`,
`PUT /device` (language only), `GET /device/timezones` and the static files. `GET /session` answers
`401 setup_required`; everything else `503 setup_required` (the old `503 no_password` is gone: never an open
device). With a password, `GET /setup` and `POST /setup/password` (→ `409`) work without a session, the rest as
before. An `admin.hash` that exists but cannot be read answers `503 password_unreadable` to the whole API (the
wizard must not replace a password it cannot read).

## 5. Source slots and aliases

`/etc/eirl-encoder/slots.json` (0640), owned by the app; the engine reads it at start and on the `slots` command
(it does not watch it). Engine side and placement rules: [`ENGINE_GO.md`](ENGINE_GO.md) §12.

```jsonc
{ "v": 1, "slots": [
  { "slot": 1, "assign": "auto", "alias": "Main camera" },
  { "slot": 2, "assign": "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0", "alias": "" },
  { "slot": 3, "assign": "net:cam1", "alias": "Phone" },
  { "slot": 4, "assign": "off", "alias": "" } ] }
```

- `assign`: `auto` | `off` | a source id exactly as the engine reports it (`hdmi`, `test`,
  `/dev/v4l/by-(id|path)/…`, `net:cam1`…`net:cam8`). No prefixes.
- `alias`: trimmed, 0-24 runes of Unicode letters and digits, space and `-_.'&()#+/`; `""` = the UI shows the
  device name. The engine never draws it.

| Route | Body | Answer |
|---|---|---|
| `GET /sources/slots` | — | `{"slots":[{"slot","assign","alias","current":{"id","name","kind","state"}\|null}],"candidates":[{"id","name","kind","connected"}],"engineSupports"}` |
| `PUT /sources/slots` | `{"slots":[…]}` (missing slots = `auto`) | `200` + the same + `"applied"` · `400 validation` · `409 on_air` · `422 <engine code>` |

- N = the engine's `slots` (MIXER_SLOTS), or 4 without a running engine. A slot outside 1..N or repeated:
  `fields.slots`. Per slot: `fields["slots.<n>.assign"]`, `fields["slots.<n>.alias"]`. Next to `fields`, the
  validation answer carries **`codes`** with the same keys: `invalid` or `duplicate` (the same id pinned twice; the
  later slot gets it and the message names the first). `codes` and `applied` are additive to the contract.
- The app writes the file, then sends `{"cmd":"slots","slots":[…]}`:
  - `ok` → `applied: true`, `engineSupports: true`;
  - `unknown_command` (the frozen Python engine) → kept, `engineSupports: false` (a new engine uses it at start);
  - engine stopped or not answering → kept, `applied: false`;
  - `on_air` (streaming, and it would move or switch off the main source, or the secondary in PiP/side by side) →
    the previous file is put back and the answer is `409 on_air` ("Stop streaming to move the source that is on
    air"); an alias-only change never moves anything;
  - any other refusal → the previous file is put back, `422`.
- `candidates`: the engine's `catalog` when present (every connected source, also those without a slot), else the
  sources in the slots now; plus every pinned id that is not connected (`connected:false`, with a readable name:
  the network camera's name, or the USB link without its decoration). `current` comes from `sources[]` by slot.
- `engineSupports`: the running engine reports `slotConfig` (and did not answer `unknown_command`).
- `POST /mixer` refuses `cmd: "slots"` (`400`): the file is the app's.
- `internal/mixer` reads the new state fields with the same bounding as the rest: `sources[].alias` (invalid → `""`),
  `slotConfig` (≤ 8, valid slot and assign), `catalog` (≤ 32, valid unique ids, names without control characters,
  ≤ 80 runes, known kinds).
- **Cloud:** the app does not forward the mixer's sources to the cloud today (`CloudStatus` has no sources), so
  there is no alias to add there.

## 6. Setup code and its threat model

`/etc/eirl-encoder/setup-code`: 8 characters of `ABCDEFGHJKMNPQRSTUVWXYZ23456789` (no 0/O/1/I/L), generated with
`crypto/rand` at the first start if missing, 0640 (owner `eirl-encoder`: the app creates it; the contract said
root, which an unprivileged app cannot do). A file that exists but holds no valid code is an error and is never
replaced (the code may be printed on a label). The touch screen shows it (from `GET /setup` over the socket), later
the label too.

- Needed only by `POST /setup/password` from the **web**, and only while there is no admin password. Compared in
  constant time after normalising what a person types (case, spaces, a dash in the middle).
- Attempts limited like the login: 5 per minute per IP (IPv6: per /64) and 20 per minute in total, counted before
  comparing; a right code clears that IP's count. The password rules are checked first, so a short password does
  not cost an attempt. 31^8 ≈ 8.5·10^11 codes at ≤ 20 tries/min: not guessable.
- **Threat**: a fresh device on a LAN with other people (venue Wi-Fi, a shared office). Without the code, the first
  person to open the web would own the device. With it, setting the password needs to see the screen or the label.
  Before the password exists the web may only read the device settings and change the language (harmless).
- **Not** done tonight (do not widen): the code as a rescue credential once a password exists (MANAGER.md §6,
  the counterweight to the cloud's `set_local_password`). That needs its own design (reset flow, label printing).

## 7. Privileges

`deploy/eirl-encoder-time.pkla` (polkit 0.105 `.pkla`, like `eirl-encoder-nm.pkla`), installed by `install.sh` to
`/etc/polkit-1/localauthority/50-local.d/`: user `eirl-encoder` may run exactly
`org.freedesktop.timedate1.set-timezone`, `.set-ntp`, `.set-time`, `org.freedesktop.hostname1.set-hostname` and
`.set-static-hostname`, with `ResultAny/Inactive/Active=yes` (a system service has no session). Not
`set-machine-info` (pretty hostname, icon, location) nor `set-local-rtc`. No sudo rules. The app calls
`timedatectl`/`hostnamectl --no-ask-password` and reads with `busctl get-property` (systemd 237 has no
`timedatectl show`), with `LC_ALL=C`, validated arguments only (zone and hostname by regular expression, never a
shell). A compromised app could set a wrong clock or hostname: visible and reversible.

**Unit sandbox** (`eirl-encoderd.service`), checked: it has no `RestrictAddressFamilies`, `PrivateNetwork`,
`ProtectHostname` or `ProtectClock` (the last two do not exist in systemd 237), and connecting to
`/run/dbus/system_bus_socket` works under `ProtectSystem=strict` (a read-only mount does not block `connect()` on a
socket; the app already talks D-Bus to NetworkManager from this unit). timedated and hostnamed do the writing as
root. **No unit change needed.**

**Hostname caveats** (to check on the board):
- `/etc/hosts` keeps `127.0.1.1 <old name>`; the app cannot edit it. `sudo` warns "unable to resolve host" but works
  (nss `myhostname` resolves it). A later root step (the updater or install.sh) could rewrite that line.
- avahi 0.7 may keep announcing `<old>.local` until it restarts; that is why the web keeps accepting the hostname
  it started with. The wizard's "done" screen can suggest a restart.

## 8. Local socket additions (MANAGER.md §3, "Local socket")

The whitelist (`api/local.go`) now forwards these method+path patterns to the web route table (so a route
registered there, like the updater's, needs no second registration); the rest stays `403 local_forbidden`:

| Route | Why it is safe over the socket |
|---|---|
| `GET /device`, `GET /device/timezones` | Name, hostname, clock, versions: nothing secret |
| `PUT /device` | The screen is physical access; everything is validated as on the web; worst case a wrong clock or name |
| `GET /setup` | Carries the setup code while there is no password: the screen exists to show it; never on the web |
| `POST /setup/password` | Only while there is no password (`409` after): whoever holds the screen sets the first one, as with the code |
| `POST /setup/step`, `POST /setup/complete` | Wizard progress only |
| `GET/PUT /sources/slots` | Source arrangement and aliases; the on_air rule protects the program |
| `GET /update`, `POST /update/check`, `POST /update/install` | The updater's (UPDATES.md): it refuses to install while streaming and re-verifies signatures as root |
| `GET /cloud`, `POST /cloud/pair` (`?qr=rows` → `linkQrRows`) | The link state and the pairing code with its QR; `cloud.View` never carries the token or the `pollSecret` |
| `DELETE /cloud` | Unlink, or cancel a code in progress (Oct 14). Whoever holds the screen holds the board. With a destination the cloud set, unlinking restarts the engine (the caller): the app does not let the closing socket cut the cloud's notification |
| `GET /destination`, `PUT /destination` | The stream destination (Oct 14). `GET` masks the key, as on the web; `PUT` has the web's validation, `409 managed` and the new-server rule (a new host needs the secrets again) |
| `GET /status` | The web's SSE snapshot (Oct 14), for the screen's Stream and Sources pages: uplinks, the saved input, `pendingRestart`, `transport`. Reviewed (`collect`): no destination URL, key or passphrase, `network` and `wifi` without secrets (as in `GET /network`), the engine's own mixer state with its output detail redacted |
| `PUT /uplinks/{name}`, `PUT /input`, `GET /sources` | What the web's Stream and Sources pages change (Oct 14): nothing secret |
| `GET /modems`, `PUT /modems/{id}`, `POST /modems/{id}/{pin,reset,subnet}` | The modems' page (MODEMS.md §7): the screen is physical access, as with the Wi-Fi password. The SIM's PIN goes as a secret field and is never logged or answered; what would cut the only link on air needs `force`, which the screen sends only after a second tap |
| `POST /service/restart` | "Apply" of saved changes, like the web's pending banner (Oct 14). Only `restart`: `start` and `stop` stay `403`. It restarts the engine, which is the caller: the screen never sees the `202`, and the restart (like `"apply": true` in a `PUT`) is not cut when the socket closes |
| `POST /cameras`, `PATCH /cameras/{id}`, `DELETE /cameras/{id}` | Network cameras (Oct 14). The answers carry the camera's URLs with its key, as on the web |
| `GET/PUT /bluetooth`, `POST /bluetooth/{scan,visible}`, `POST /bluetooth/devices/{address}/{action}`, `PUT /bluetooth/devices/{address}` | Bluetooth audio (Oct 15, BLUETOOTH.md §3.4): nothing secret in them (BlueZ keeps the keys, root only). Opening the 2-minute visible window from the screen is physical access, as on the web with a session |
| `PUT /password` | The admin password with the current one (Oct 14). The same validation and errors as the web (`403 bad_password`, `400 validation`); `429` after 5 wrong current passwords in a minute (the screen counts as one caller, the web per IP); `503 setup_required` without a password. No session to keep: **every web session is closed** |

Two routes have **their own handlers on the socket** (not forwarded): `GET /destination/secrets` does not exist on the web
(`404` there), and the web's `GET /network/credentials` is still its own (everything, with a session):

| Route | What |
|---|---|
| `GET /destination/secrets` | `200 {"streamId": "…", "passphrase": "…"}` in clear, as saved (`""` when unset), for the screen's eye button. Also with `managedBy: eirl`: the web never reveals them, the screen does (the owner's decision). `Cache-Control: no-store`; the request is logged (method, path, code, caller), never the body. `500 internal` if the configuration cannot be read |
| `GET /network/credentials` | Without `?cameras=1`, only the Wi-Fi, as before: `{"wifi": {"ssid", "channel", "password", "qr"} \| null}`. With `?cameras=1`, the same as the web: `{"wifi", "cameras": [{"id", "srt", "rtmp", "rtmpServer", "rtmpKey", "lan"}]}`, for a camera's eye button (the screen draws its QR from the URL). `?qr=rows` adds `wifi.qrRows` in both. Another value of `cameras` or `qr`: `400 bad_request`; `503 network_unavailable` without `eirl-netsetup` |

**Why, since Oct 14.** The owner decided that everything the web configures can be configured on the touch screen,
secrets included: the screen shows them as dots and reveals them with an eye button. Until then the destination with
its key, the cameras with theirs, `PUT /password` and `DELETE /cloud` were left out on purpose; that decision is
reversed. It is acceptable because the screen is physical access (it already shows the Wi-Fi password and the setup
code, and whoever holds it can reset the board), the engine already holds the stream key to stream it, and
`screen.jpg` (what the web and the cloud can see of the screen) is not updated while Settings is open. What stays out:
`start`/`stop` of the service, reboot and power off of the board (those go through `screen.power`), the logs, the
mixer and `PUT /output` (the engine is the mixer), the live preview, the QR images and the web session.

`POST /setup/restart` is on the socket too (Settings → About on the screen): the password is kept and its step
skipped, and every other step is already reachable from the screen's Settings. Mutations over the socket have the
web's body limit (16 KB). Requests over the socket are marked in their context (`isLocal`): the setup code, the
language-only rule and the password change (no session to keep, its own attempt count) depend on it.

## 9. How to test

```bash
cd manager && gofmt -l . && go vet ./... && go test ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/eirl-encoderd ./cmd/eirl-encoder-sim
# The web and the screen against the simulator (real handlers, fake board):
go run ./cmd/eirl-encoder-sim -http 127.0.0.1:8080 -fresh -web web/static          # wizard; setup code in the log
go run ./cmd/eirl-encoder-sim -http 127.0.0.1:8080 -local-socket /tmp/eirl-api.sock # configured: eirl-sim-1234
curl --unix-socket /tmp/eirl-api.sock http://x/api/v1/setup
```

Local mode also runs the board's access point (`cmd/eirl-encoder-sim/network.go`): the real network manager over the
data directory with a fake `eirl-netsetup` that writes the state the real one would. "Create my Wi-Fi", its name
(generated at start from `-hardware-id`, otherwise from `-data`; `-fresh` clears it), channel and password work in the
web, the wizard and over the socket; no camera ever connects (no MediaMTX), and there is no Wi-Fi client.

The simulator's engine has a catalogue of five sources (HDMI, a USB camera, a network camera, the test pattern and,
without a slot until one is given to it, a USB capture card); the two USB ones carry image settings
(SOURCES.md §7) with `camera_control`/`camera_reset` and the engine's rules. It hides and shows sources and
applies slots with the on_air rule and starts streaming with `PUT /output {"output":"on","apply":true}` (a test
destination is preset). Linking gives code `SIMX-2345`, approved after 30 s. `/update`: check → `available` 0.2.0
(1.5 s) → `downloading` (3 s) → `ready` (8 s); install → `installing` (6 s) → `idle` on 0.2.0; `409 streaming` /
`409 not_ready`.

Tests: `internal/i18n` (catalog completeness, negotiation, refs), `internal/device` (migration, hostname, steps,
setup code, commands passed to timedatectl/hostnamectl, zones), `internal/slots` (validation, duplicates, file),
`internal/mixer` (slots command, state bounding), `internal/api` (`setup_test.go`: gating with and without a
password, the code over the web vs the socket, rate limit, CSRF, device validation and system calls, Host after a
rename, cloud results in the device language, pairing over the socket; `slots_test.go`: unknown_command, catalog
candidates, duplicate/invalid codes, on_air rollback, engine stopped; `local_test.go`: the whitelist, the routes of
§8 over a real socket, the secrets in clear only there, `start`/`stop` refused, the password change from the screen
closing the web sessions, its attempt limit, the body limit).

On the board (not done tonight): `PUT /device` time zone/NTP/time/name through polkit; that `sudo` still works after
a rename; mDNS of the new name; the Python engine answering `unknown_command` to `slots`.

## 10. Decisions and deviations from the batch contract

1. `device.json` and `setup-code` are 0640 (contract: 0660 / root-owned): the app cannot chown, and group write
   adds nothing.
2. `setup.completed` in `device.json`; `language` and (socket only) `code` in `GET /setup`; `codes` in the slots
   validation answer; `applied` in the `PUT /sources/slots` answer; `linkQrRows` with `?qr=rows` in `/cloud`.
3. The web wizard puts `password` second (§4): the contract's "only language before the password" makes the
   screen's order impossible on the web.
4. `GET /session` without a password: `401 setup_required` (not 503), so the web can tell "set up" from "sign in".
5. The local socket gets `GET /cloud` and `POST /cloud/pair`, not `DELETE /cloud`.
6. `PUT /device` system errors: `502 system_failed` with the failing part in the message.
7. `version.engine`/`version.release`: the first whitespace-separated field of their files.

## 11. Pending

- On the board: the checks at the end of §9.
- `/etc/hosts` after a rename (a root step), and the avahi name.
- The setup code as a rescue credential (MANAGER.md §6).
- User-visible texts outside the app that still arrive in Spanish: the names and not-ready details the engine puts
  in its state (`HDMI de la placa`, "no ofrece un formato…": `internal/engine`), and the `probe` errors
  (`sources.ErrNoMode`…) that the frozen Python engine copies to its screen as they are.
