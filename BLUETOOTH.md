> **Status:** IN PROGRESS — B0-B4 done; B5 in part on the board (Oct 15-16): a TP-Link UB500 (RTL8761B, §3.8) and
> headphones playing the program and taking the voice; the rest of B5 pending
> **Updated:** 2026-10-15

# Bluetooth audio: headphones to listen, microphones and phones as inputs

The request (Oct 15): connect Bluetooth input and output devices to the encoder **when the board has a
Bluetooth module**. Scoped to audio (D1):

- **Output**: headphones or a speaker to **listen to the program**. It is the same monitor as the screen's
  headphones (HEADPHONES.md): when Bluetooth headphones connect, the monitor plays through them; when they go,
  back to the screen's jack (D2).
- **Input**: the **microphone of a headset** (hands-free profile, phone quality) and **a phone or a player**
  sending its audio (A2DP), each one an audio input of the mixer, like a loose USB microphone.

Without an adapter (or without the Bluetooth stack), nothing changes: the web and the screen say so in one line
and offer nothing else.

## 0. Status by phase

| Phase | What | Status |
|---|---|---|
| B0 | Hardware: an adapter the board sees (the M.2 card's own, or a USB dongle) | **Done** with a TP-Link UB500 (Realtek RTL8761B), which needs the patched modules and firmware of §3.8. The M.2 card's Bluetooth still does not enumerate (§1) |
| B1 | Platform: BlueZ in the image, BlueALSA built and shipped in the bundle, units, D-Bus policy | Written: `build/bluealsa.sh` builds and runs in the arm64 container; the image installs `bluez` (not enabled: `eirl-bluealsa.service` pulls it in), with the image session's OK |
| B2 | App: BlueZ over D-Bus (power, scan, pairing, agent, connect, forget), what each device is used for, the engine's file, API and socket | Written, tested on the Mac with a fake BlueZ (also in `eirl-encoder-sim`) |
| B3 | Engine: the monitor over Bluetooth, Bluetooth audio inputs | Written, tested on the Mac |
| B4 | Web (Settings → Bluetooth) and screen (Settings → Wireless → Bluetooth, the monitor's label) | Written: web tested on the simulator at 390 and 1280 px in es/en/fr; screen previews in es/en/fr for review |
| B5 | Test on the board with real devices (scheduled board slot) | **In part** (§5.1): pairing from the encoder (also from the web), the monitor over A2DP, the headset's microphone over hands-free, the pairing kept over the adapter coming back. Pending: a phone as an input, disconnections and engine restarts with them connected, 30 min streaming, the AP at the same time |

## 1. What was checked on the board (Oct 15, read-only)

- **The M.2 card is an Intel Wireless-AC 8265** (PCI `8086:24fd`, subsystem `8086:1010`). Its Wi-Fi works over
  PCIe. Its Bluetooth half is a USB device (`8087:0a2b`) on the M.2 Key E USB 2.0 lane, which the device tree has
  in host mode (`usb2-2`, okay). **It is not on the bus**: the root hub's port 3 has been empty since boot, there is
  no `hci0`, and `/sys/class/bluetooth` is empty. The firmware it would load (`intel/ibt-12-16.sfi`/`.ddc`) **is**
  in the image (`linux-firmware` 1.173.21): this is not a missing firmware but a device that does not enumerate
  (§4, risk 1).
- **The kernel is ready**: `CONFIG_BT=y`, `BT_RFCOMM=y`, `BT_HIDP=y`, `UHID=y`; `btusb`, `btintel`, `btrtl`,
  `btbcm` as modules (loaded by udev when an adapter appears). SCO (the hands-free audio) is in the core.
- **The user space is not**: no `bluez` (left out of the image, IMAGE.md D-I6, because nothing used it), no
  BlueALSA, no D-Bus policy for `org.bluez`. Already in the image: `libbluetooth3`, `libsbc1`, `libdbus-1-3`,
  `libglib2.0-0`, `libasound2`.
- No `lsusb` or `rfkill` in the image: the USB devices are read from `/sys/bus/usb/devices/*/idVendor`.
- 🪤 **NVIDIA starts `bluetoothd` without audio.** The package `nvidia-l4t-firmware` ships
  `/lib/systemd/system/bluetooth.service.d/nv-bluetooth-service.conf`: `bluetoothd -d --noplugin=audio,a2dp,avrcp`
  (debug logging, and without the plugins BlueALSA needs to register A2DP). Ours, with the same name in
  `/etc/systemd/system/bluetooth.service.d/`, replaces it.
- ALSA loads its add-on configuration from `/usr/share/alsa/alsa.conf.d/*.conf`, and its plugins from
  `/usr/lib/aarch64-linux-gnu/alsa-lib/` (not there yet: no plugin is installed).

What else was found before writing code (BlueALSA, BlueZ 5.48 and kernel 4.9, from their sources and changelogs):

- **BlueALSA 4.1.0** is the newest release whose configure takes 18.04's libraries (from 4.2.0 it wants BlueZ ≥ 5.51
  and glib ≥ 2.58.2). Its PCMs are listed through the D-Bus ObjectManager on `/org/bluealsa` (`org.bluealsa.PCM1`:
  `Device`, `Transport`, `Mode`, `Delay` in 1/10 ms…). 🪤 Its configure asks for sbc ≥ 1.2 but the daemon calls
  `sbc_reinit_a2dp`, which libsbc has from 1.4 (18.04 ships 1.3): the build links libsbc 2.0 statically.
- 🪤 **alsa-lib 1.1.2-1.1.3** (the board's is 1.1.3) can deadlock a software plugin such as BlueALSA's with its
  thread-safe API: whoever opens `bluealsa:` PCMs must run with `LIBASOUND_THREAD_SAFE=0` (the engine's unit).
- 🪤 **One client per PCM** (a second one gets `EBUSY`), and the **capture of a phone that is not playing waits
  forever**: no data and no poll events until the phone starts the stream. Opening a hands-free PCM can answer `EAGAIN`
  for ~2 s while the codec is agreed.
- **Hands-free is CVSD (8 kHz) only** on kernel 4.9: `btusb` picks mSBC's USB alternate setting only from 5.8.
- BlueZ 5.48 has what the app uses (`Adapter1`, `Device1`, `AgentManager1`, `SetDiscoveryFilter` with `Transport`); its
  D-Bus policy on 18.04 only lets root, the console and group `lp` call `org.bluez`: the app needs its own (§3.7). Its
  `AutoEnable` is off: the app powers the adapter. Found devices are kept 3 min after a search.
- **Adapters for kernel 4.9 without newer firmware**: Intel (the 8265's `8087:0a2b`, firmware already in the image) or a
  genuine CSR8510 (`0a12:0001`; clones are flaky). Not a Realtek RTL8761B (TP-Link UB500): supported from 5.10/5.16.

So "Bluetooth available" means two things: **an HCI adapter** and **the stack running**. Today the board has
neither.

## 2. Decisions

| # | Question | Decision |
|---|---|---|
| D1 | Which devices | **Audio only** in this version: headphones and speakers to listen; a headset's microphone and a phone or player as inputs. Keyboards, remotes and pedals to control the mixer, later (§7) (owner, Oct 15) |
| D2 | What the monitor does with Bluetooth headphones | **One monitor**: with headphones connected it plays through them, and back to the screen's jack when they disconnect. Same switch and volume (owner, Oct 15) |
| D3 | Without an adapter, and by default | Proposed: `available: false` with the reason (`no_adapter`, `no_stack`); the web card and the screen page say it in one line, nothing else. With an adapter, Bluetooth is **off until the operator switches it on** (security review): the radio and the stack are not exposed to everyone around without being asked |
| D4 | Pairing safety (the encoder is used in public places) | Proposed: **never visible nor pairable by default**. Pairing started from the encoder (search → tap the device) is always allowed; a device that starts it (a phone) only inside a **2-minute "Visible" window** opened from the screen or the web, and **one device per window** (it closes as soon as one pairs). A device paired from its side gets a use by itself only if it *sends* audio (`input`): never the monitor nor a microphone, which the operator chooses, so a stranger who pairs first cannot take the monitor. The agent authorises **audio services only**; outside the window it refuses. Paired devices are trusted (they reconnect by themselves). Only audio devices are listed |
| D5 | A headset with a microphone | Proposed: each device has a **use**: `listen` (stereo, A2DP), `mic` (its microphone as an input, and the monitor through its earpiece, both over the hands-free profile, phone quality) or `input` (a phone or a player sending audio). The default: `listen` for headphones and speakers, `input` for phones |
| D6 | Where on the screen | Proposed: a **Bluetooth** tab in the Wi-Fi group (renamed "Wireless"), so the list keeps its seven entries (MIXER.md §21.1) |
| D7 | Cloud | Proposed: not in this version (local, like the headphones' D3) |
| D8 | Stack | **BlueZ 5.48** from Ubuntu (the image) + **BlueALSA** built from a pinned release (the bundle, like `ristsender`) + its ALSA plugin, which the engine opens. No PulseAudio: the engine keeps opening ALSA devices directly |

## 3. Design

### 3.1 The stack

```
adapter (USB / M.2) ─ btusb ─ bluetoothd (BlueZ, root) ─ D-Bus ─┬─ eirl-encoderd: power, search, pairing (agent),
                                                                │  connect, forget, the use of each device
                                                                └─ eirl-bluealsa: A2DP source + sink, HFP/HSP AG
                                                                       │ D-Bus + a pipe per stream
                                          engine: alsasink / alsasrc device="bluealsa:DEV=<addr>,PROFILE=a2dp|sco"
```

- `bluetoothd` keeps the pairings (`/var/lib/bluetooth`, root). The app talks to it over the system bus.
- BlueALSA registers the audio profiles with BlueZ and gives each connected device its PCMs. The engine opens
  them through the ALSA plugin, like any other ALSA device, from its own pipelines.
- The app is the only one that knows what each device is **used for**; the engine only learns, through a file,
  which Bluetooth devices are ready for audio and for what (§3.3).

### 3.2 App: `manager/internal/bluetooth`

- **BlueZ backend** (godbus, like the Wi-Fi's NetworkManager one): the adapter (`Adapter1`: `Powered`,
  `Discoverable`, `Pairable`, `Discovering`, `Alias`), the devices (`Device1`: `Address`, `Alias`, `Icon`, `Class`,
  `UUIDs`, `Paired`, `Trusted`, `Connected`, `RSSI`), read with `ObjectManager.GetManagedObjects` and followed by
  its signals. A fake backend for the tests.
- **BlueALSA backend**: its PCMs (device, profile, direction, delay). A device is **ready** for a use when its
  PCM is there, not just when BlueZ says "connected" (BlueZ does not say which profiles are up).
- **Agent** (`org.bluez.Agent1`, capability `NoInputNoOutput`, the default agent): confirms the pairings the app
  starts, and the ones a device starts only inside the visible window (D4); authorises services of trusted devices
  only. It never asks the user for a PIN (audio devices pair with "just works"; a legacy one with a fixed PIN
  `0000` only inside the window).
- **Search**: `SetDiscoveryFilter{Transport: bredr}` (audio is classic Bluetooth) and `StartDiscovery` for 30 s.
  Only audio devices are listed: class of device Audio/Video or Phone, or an audio UUID.
- **What each device can be used for**, from its UUIDs: A2DP sink (`110b`) → `listen`; hands-free (`111e`) or
  headset (`1108`) → `mic`; A2DP source (`110a`) → `input`.
- **Saved** in `/etc/eirl-encoder/bluetooth.json`: whether Bluetooth is on, and the use of each device by
  address. The pairings themselves are BlueZ's.
- **At start and on power on**: one connection attempt to each paired device with a use (headphones usually
  reconnect by themselves, phones do), shown as an order running on it.
- **Robustness** (code review): the visible window opens for the agent only once the adapter took it (a failure
  leaves it closed and hidden); switching on or off is saved only if the adapter took it; an adapter that refuses to
  power (rfkill) is said once (`powerFailed`) and tried again every minute, not every refresh.
- **The engine's file** `/run/eirl-encoder/bluetooth.json` (0640, group `eirl-encoder`: the engine is in it),
  rewritten atomically on every change, with only what is **ready**:

  ```json
  {"v": 1,
   "out": {"addr": "AA:BB:CC:DD:EE:FF", "name": "WH-1000XM4", "profile": "a2dp", "delayMs": 180},
   "in": [{"addr": "11:22:33:44:55:66", "name": "Phone", "profile": "a2dp"}]}
  ```

  `out` is the device the monitor plays through (`null`: the screen's jack): the last one connected with use
  `listen` or `mic`. `profile` is `a2dp` or `sco`. `delayMs` is what BlueALSA reports for that PCM (0-1000, 0 when
  it does not say). The engine checks everything: the address against `^([0-9A-F]{2}:){5}[0-9A-F]{2}$`, the profile
  against the two, the name cleaned like any other third-party name; it builds the ALSA device itself, never takes
  one from the file. 🪤 **A missing file keeps the last one** (`btaudio.Reader`): when the app restarts, systemd
  empties its RuntimeDirectory, and that must not cut a Bluetooth input or the headphones on air. A device really
  gone, BlueALSA closes and the engine's pipeline errs on its own; the app never empties the file when it stops. The
  engine reads it again only when it changed, and probes again only when its inputs changed.
- **Names**: letters and digits of any script, spaces and a few signs, ≤ 48 characters (`btaudio.CleanName`, also
  the adapter's alias that phones show): no control or format characters.

### 3.3 Engine

- **The monitor** (`internal/engine/phones`): its target is the file's `out` when there is one, and the HDMI
  otherwise. If the target changes while it plays, it ramps down, closes and opens on the new one. With a
  Bluetooth target there is no screen to wait for, no ELD and no `amixer`. Its delay keeps the rule of
  HEADPHONES.md §3.5 minus what Bluetooth adds: `max(screen − delayMs, arrival + buffer + margin)`, so it sounds
  close to the screen's picture without ever going below what the program audio needs to arrive. The sink's buffer on
  Bluetooth is 100 ms (`BTSinkBuffer`; 40 on the HDMI), to be tuned in B5. Its state gains `target` (`screen` |
  `bluetooth`, also while off: where it would play) and, with Bluetooth, `device` (the name). The target is looked at
  every second (`TargetPoll`).
- **Inputs** (`internal/engine/audio`): a new kind `bluetooth`, id `bt:<12 hex digits>`, its pipeline
  `alsasrc device="bluealsa:DEV=…,PROFILE=a2dp|sco" provide-clock=false ! audioconvert ! audioresample ! <caps>`
  (hands-free is mono at 8 kHz; the mixer takes 48 kHz stereo; `buffer-time` 200 ms: the air brings bursts). A phone
  that is not playing sends nothing: like the board's HDMI without sound, that is `no_signal` (`bt_no_audio`) and the
  input is **not** reopened (a reopening would only queue behind the read that waits, §1).
  A change of profile (a device's use from `input` to `mic`) reopens it on its new device, in its slot. A
  device that disconnects is `gone` and frees its slot after the same grace as an unplugged USB card. They take an
  audio slot like a loose USB microphone: after the cameras.
- **Discovery**: `probe -list` adds the file's `in` to its audio list (like the network cameras' file), and the
  engine probes again when the file changes (its stamp, like the cameras').

### 3.4 Contracts (additive)

- **Socket** (MIXER.md §5): no new command. State: `audio.phones.target` and `audio.phones.device`; inputs with
  `kind: "bluetooth"`. An old app ignores them; an old engine has neither and the app shows the screen's jack.
- **Audio ids**: `bt:[0-9A-F]{12}` joins `i2s`, `alsa:<card>` and `net:camN` in every validation (engine, app,
  appapi).
- **API** (MANAGER.md §3):

| Route | Body | What |
|---|---|---|
| `GET /api/v1/bluetooth` | — | `{available, reason, on, powered, powerFailed, visible, visibleLeft, scanning, devices: [{address, name, kind, paired, connected, ready, use, uses, rssi, pending, error, output}]}`. `reason` `no_audio` comes with `available: true` (devices can be managed, not heard); `powerFailed`: the adapter refused to follow `on` (rfkill, firmware), retried every minute |
| `PUT /api/v1/bluetooth` | `{on}` | Switch Bluetooth on or off (saved) |
| `POST /api/v1/bluetooth/scan` | — | Search for 30 s |
| `POST /api/v1/bluetooth/visible` | `{on}` | Open (2 min) or close the visible window |
| `POST /api/v1/bluetooth/devices/{address}/{action}` | — | `pair`, `connect`, `disconnect`, `forget` |
| `PUT /api/v1/bluetooth/devices/{address}` | `{use}` | `listen`, `mic` or `input` (one of its `uses`) |

  Errors: `409 bluetooth_unavailable`, `409 bluetooth_off`, `404 bluetooth_unknown_device`, `409 busy`,
  `400 validation`, `422 bluetooth_failed` with the reason (`rejected`, `timeout`, `not_reachable`…). All of them
  are also on the screen's socket.

### 3.5 Web

Settings → **Bluetooth** card, after the Wi-Fi ones: the switch; the paired devices (name, kind, state: playing the
program, connected, not connected, the order running or why it failed; its use as a choice when it has more than
one, Connect / Disconnect, Forget with a confirmation); **Make visible (2 min)** with the time left and how the
encoder appears on a phone; **Devices nearby** with **Search** and Pair. While any order runs, the other device
buttons and Search are greyed out (one at a time). Disconnecting, forgetting or switching off a device whose audio
is on air while streaming asks first. The card reads `GET /bluetooth` only while the page is visible: every 2 s with
the card on screen, 30 s off it, 1 s while searching, visible or with an order running; an app without the route
(`404`) hides the card. The headphones' line in the Audio card names the Bluetooth headphones when they play there,
and a Bluetooth input reads "Name · Bluetooth". es / en / fr.

### 3.6 Screen

- Settings → **Wireless** (the former Wi-Fi group: the list keeps its seven entries) → **Bluetooth** tab
  (`btpage.go`): the switch (and the name it shows to phones), Search, Make visible (the time left; a second tap closes
  it), the paired devices then the found ones (≥ 48 px rows, in pages), each with its kind and state; the device under
  the finger is fixed when it goes down, so a list that reorders during a search cannot swap it. A paired device opens
  its view: Connect / Disconnect, its uses as choices, Forget (a second tap, `confirmTap`). Disconnecting the
  monitor's headphones asks nothing; disconnecting an input on air while streaming asks for a second tap. Without
  Bluetooth, one line with the reason.
- Audio view: the headphones button shows a small `BT` mark and the device's name (shortened) when the monitor plays
  over Bluetooth, and a Bluetooth input's strip carries the same mark before its name (its `no_signal` reads "the
  device sends no audio": the mark already says it is Bluetooth).
- Previews: `eirl-ui-render settings-bluetooth…` (MIXER.md §21.5) and `audio+phbt`, `audio+btin`.

### 3.8 Realtek RTL8761B on kernel 4.9 (the TP-Link UB500 and most current dongles)

The RTL8761B(U) answers with its ROM firmware and BlueZ powers it, but **its radio receives nothing** without its
firmware patch: 0 inquiry results with headphones in pairing mode, 0 LE advertisements in 8 s (btmon). Linux loads
that patch from 5.10/5.16; L4T's 4.9 `btrtl` already has the patch format the 8761B uses ("Realtech" epatch, the
same extension signature) and lacks only three things, taken from upstream:

- `btrtl`: project id 14 (the 8761B) and, when `lmp_subver` is 0x8761 and `hci_rev` 0x000b, the firmware
  `rtl_bt/rtl8761bu_fw.bin` and its config `rtl_bt/rtl8761bu_config.bin` (`driver/bluetooth/btrtl-8761b.patch`);
- `btusb`: the USB ids of 8761BU dongles of other vendors (TP-Link UB500 `2357:0604`, ASUS USB-BT500 `0b05:190e`,
  `2550:8761`, Edimax BT-8500 `7392:c611`); Realtek's own vendor id already matched (`btusb-8761bu-ids.patch`);
- the firmware itself, which Ubuntu 18.04's linux-firmware (1.173) does not have: `rtl8761bu_fw.bin` and
  `rtl8761bu_config.bin` of linux-firmware `20240909`, verified by sha256, with their licence (redistributable).

`build/build.sh` builds both modules from L4T's sources (`build/out/modules-bt/`) and fetches the firmware
(`build/out/firmware/rtl_bt/`). They live where the modems' modules do: `/lib/modules/<release>/updates/eirl-bluetooth/`
(searched before the kernel's own) and `/lib/firmware/rtl_bt/`. New boards get them from the SD card image
(`tools/image/stages/rootfs.sh`, checked by `check.py`); a board already in use, from `tools/bt-modules.sh`
(`--check`, `--undo`). The OTA never touches the kernel. Our `btusb` is the stock one plus those ids: every other
adapter works as before. Measured: with the patch the controller reports LMP subversion 0xd922 and receives (206 LE
advertisements in 6 s); reloading `btusb` keeps the patch (the dongle is not powered off), a cold boot loads it again.

### 3.7 Platform

- **BlueZ** comes from Ubuntu, so it goes **in the image** (`tools/image/packages.txt`, reverting D-I6 for
  `bluez` only): the OTA updates never install Ubuntu packages (UPDATES.md). On a board without it, the app
  answers `no_stack`. `bluetooth.service` is pulled in by our unit, with a drop-in of ours that loads the audio
  plugins and **not** the others (`--noplugin=input,hog,network,sap,midi,health`: no HID "keyboard" typing into the
  encoder, no networking, no SIM access). The app keeps the adapter not pairable and not discoverable outside the
  window (also before powering it: BlueZ starts pairable) and names it like the encoder (`Alias`).
- **BlueALSA** is built like `ristsender` (`build/bluealsa.sh`: release 4.1.0 and libsbc 2.0 verified by sha256, in
  the L4T 18.04 arm64 container; A2DP with SBC, hands-free with CVSD, no tools) and travels in the bundle: the daemon
  (`/usr/local/bin/bluealsa`, which links only the image's libraries; libsbc inside), the ALSA plugins
  (`/usr/lib/aarch64-linux-gnu/alsa-lib/`) and their configuration (`/usr/share/alsa/alsa.conf.d/20-bluealsa.conf`),
  and the unit `eirl-bluealsa.service`: its **own system user `bluealsa`** (created by `install.sh`), not root (it
  decodes what paired devices send: as root a flaw there would be the whole board), sandboxed (`ProtectSystem=strict`,
  `AF_UNIX AF_BLUETOOTH`, `MemoryDenyWriteExecute`, `RestrictNamespaces`, ambient `CAP_NET_RAW CAP_NET_ADMIN` only),
  `ConditionPathExists=/usr/lib/bluetooth/bluetoothd`, and `Wants=bluetooth.service`: in the image `bluetooth.service`
  is **not** enabled (its postinst enables it; `tools/image/stages/rootfs.sh` undoes it and `check.py` checks it), our
  unit pulls it in. The licences go to `/usr/share/doc/eirl-encoder/licenses/bluealsa/`, with libsbc's source
  tarball (LGPL, linked statically).
- **D-Bus policy** (`/etc/dbus-1/system.d/eirl-encoder-bluetooth.conf`, from `install.sh`), each one to the interfaces
  it uses: `bluealsa` owns `org.bluealsa` and reaches BlueZ's `Media1`, `ProfileManager1` and `MediaTransport1`; the
  app reaches `Adapter1`, `Device1`, `AgentManager1` and reads BlueALSA's PCMs; the engine (group `audio`) opens the
  PCMs (`PCM1`), never BlueALSA's RFCOMM.
- `install.sh` (OTA): when any of these files changes and BlueZ is installed, it reloads the bus's policies and
  restarts `bluetoothd` and BlueALSA (the connected devices drop for a few seconds). The engine's unit runs with
  `LIBASOUND_THREAD_SAFE=0` (§1).

## 4. Risks

| Risk | What we would do |
|---|---|
| 1. The M.2 card's Bluetooth does not enumerate (§1) | B0: a USB dongle that kernel 4.9 supports without new firmware, or a look at the card (its `W_DISABLE2#`, the model). The feature does not depend on which adapter it is |
| 2. A dongle on the board's hub: cutting the hub's power switches everything off (MIXER.md §16), and the power supply is tight | A dongle draws ~20-50 mA; the M.2 card would be better (no hub) |
| 3. Bluetooth and Wi-Fi share the 2.4 GHz radio of the 8265 ("Create my Wi-Fi" for the cameras is on 2.4 GHz) | Measure in B5 a camera over the AP with A2DP playing |
| 4. A2DP latency (~150-250 ms): the monitor would sound behind the screen | Subtract the PCM's reported delay (§3.3); clap test in B5 |
| 5. A Bluetooth input arrives late against the video (A2DP ~200 ms, hands-free ~30-60 ms) and the per-input delay only delays the audio | Say it in the docs: for commentary and music, not for lip sync |
| 6. Hands-free audio is 8 kHz (CVSD) on kernel 4.9 | Phone quality is what it is; wideband (mSBC, 16 kHz) where the kernel and BlueALSA can |
| 7. Pairing from strangers in a public place | D4: never visible by default, a 2-minute window, only audio devices |
| 8. CPU (SBC encoding, the daemon) on a board near its limit | Measure with `engine-cpu.sh` and `tegrastats` in B5, with and without Bluetooth |
| 9. Device names are third-party data | Cleaned (letters, digits and a few signs, ≤ 48) in the app and again in the engine |
| 10. A phone used as an input that stops playing: its capture waits forever (§1) | `no_signal`, not reopened. Closing it (its slot given to a camera's audio, the engine stopping) waits until audio comes or the phone disconnects: the input's goroutine is left behind, never the actor; the engine's stop is bounded. Check in B5 |
| 11. `LIBASOUND_THREAD_SAFE=0` is process-wide: it also applies to the HDMI monitor and the I2S/USB inputs | As alsa-lib was before 1.1.2; GStreamer's ALSA elements lock on their own. Check in B5 that they all keep working |
| 12. Someone nearby pairs inside the window before the operator's phone (security review) | One device per window, it gets no use unless it sends audio, it never takes the monitor; the operator sees it in the list and forgets it |
| 13. A paired device's address can be spoofed (Bluetooth's own limit) | Audio services only, no HID or networking plugins; uses whose pairing BlueZ no longer has are dropped after a minute |
| 14. Our `btusb`/`btrtl` replace the kernel's for every adapter (§3.8) | They are the stock 4.9 sources plus upstream's ids and the 8761B case; `tools/bt-modules.sh --undo` goes back. Built from the board's exact config, refused for another kernel |
| 15. The Bluetooth sink without clock slaving drifts against the program | Corrected by the sink when it passes 40 ms (a tiny jump); measure how often in the 30-minute test |

## 5. Phases

- **B0 · An adapter.** Done with a TP-Link UB500 (§3.8). Read-only check: `ls /sys/class/bluetooth` shows `hci0`,
  `tools/bt-modules.sh --check` says what the adapter runs.
- **B1 · Platform.** `build/bluealsa.sh`, the bundle's files and `install.sh`; BlueZ in the image (the image's
  session). On today's board, BlueZ installed by hand for B5 (with the owner's OK).
- **B2 · App.** `internal/bluetooth`, API, socket, the engine's file, `probe -list`. Tests with the fake backend.
- **B3 · Engine.** The monitor's target and the Bluetooth inputs. Tests on the Mac.
- **B4 · Web and screen.** PNGs of the screen for review before installing.
- **B5 · Board** (scheduled slot): BlueZ installed by hand on today's board (or a test image), the bundle with
  `manager/build.sh`, the engine with `tools/deploy-engine.sh`. Pairing from both sides (and refused outside the window),
  headphones (A2DP) with the clap and `BTSinkBuffer`, a headset in `mic`, a phone as input (and paused, and its slot
  taken by a camera's audio: risk 10), disconnections in the middle, restarting the engine with them connected, the HDMI
  monitor and the I2S/USB inputs with `LIBASOUND_THREAD_SAFE=0` (risk 11), BlueALSA as its own user and the narrow
  D-Bus policy (`journalctl -u dbus` without "Rejected send message"; hands-free needs its capabilities), pairing from
  a phone closing the window, 30 min streaming with Bluetooth on (output
  without losses, CPU, flat RAM), Wi-Fi AP at the same time. `/code-review` and `/security-review` (pairing and D-Bus).
  Commit and MR once approved.

### 5.1 B5 on the board (Oct 15-16, with the owner)

- **Setup**: `bluez` 5.48 installed by hand (only that package; not enabled, as in the image), the app and the engine of
  this branch, the modules and firmware of §3.8. Not streaming the whole time.
- **Platform**: `bluetoothd` with the audio plugins only, BlueALSA as `bluealsa` (its D-Bus name, BlueZ's endpoints:
  the policy works), the agent registered; no "Rejected send message" on the bus. The adapter announces Rendering,
  Capturing and Telephony.
- **Pairing**: the owner's AirPods Pro found and paired from the encoder in ~3 s (use `listen`, ready, the monitor's
  target), and again from the web card. 🪤 The first pairing had no key (see below).
- **The monitor over A2DP**: plays the program continuously after the fix below; with the 40 ms buffer, 232 ms of
  our own delay (arrival 162 + 40 + 30) plus what the headphones add: "less delay, continuous" (the owner).
- **The headset's microphone (use `mic`)**: switching the use moved the monitor to hands-free and opened the input
  `bt:<its address>` in audio slot 4 (no signal while the SCO link opened, then live); on air, "it sounds good".
- 🪤 **Clock slaving**: with the default `slave-method` (skew) the sink corrected ~20 ms again and again against
  BlueALSA's clock and ended up dropping everything (1-2 s of sound, then silence; the radio kept sending 100 packets/s
  of silence). Tones outside the engine told it apart: only `slave-method=none` played the whole 12 s. The Bluetooth
  sink runs without slaving (a small jump when the drift passes 40 ms: to be measured in the 30-minute test).
- 🪤 **Bonding**: with the adapter not pairable, the kernel turns even a pairing the encoder starts into a non-bonding
  one, and BlueZ stores no key: after the adapter came back the headphones were unpaired. The adapter is bondable now
  while the app's own pairing runs (the agent still accepts only that device) and not pairable again right after;
  checked: the key is stored (`[LinkKey]`).
- Kernel noise after leaving hands-free: "SCO packet for unknown connection handle" while the headset still sends on
  the closed link. Harmless.

## 6. Tests on the Mac

`cd manager && go test -race ./internal/bluetooth/ ./internal/btaudio/ ./internal/engine/... ./internal/api/ ./internal/sources/`:
the fake BlueZ (power, search, pairing in and out of the window, refused and unreachable devices, one order at a time,
the uses, forgetting, reconnecting at power on, the engine's file), the file's checks, the routes and the socket, the
monitor's target (to the headphones and back to the screen while playing, the delay), the Bluetooth inputs (no signal
without reopening, gone and its slot, a profile change). By hand, with the web and the API over the fake BlueZ:
`eirl-encoder-sim -http 127.0.0.1:8080 -web web/static` (devices around: one refuses to pair, one is off).

## 7. Out of scope

- **Keyboards, remotes, pedals** (HID) to type in Settings or to control the mixer: a later phase (D1). The kernel
  already has HIDP and UHID.
- Two monitors at once (D2), the AAC / aptX / LDAC codecs, LE Audio.
- Control from the cloud (D7) and customer documentation (the encoder is still in testing).
