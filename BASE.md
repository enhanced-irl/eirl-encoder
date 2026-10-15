# The Jetson's base system: cutting it down to the minimum

> **Status:** IN PROGRESS: B0, B1 (group A and `alsa-restore`), B2, QSPI and the full B3 (with `desktop`) applied on Oct 13 on the test board and validated with a reboot. The rest of B1 group B, B4, B5 and B6 remain
> **Updated:** 2026-10-14

Plan to review everything installed in the Jetson's image and remove what the
encoder does not need. The goal is for the base system to use as little as possible and leave as much
CPU, RAM, I/O and boot time as possible for the engine. The scope is **the OS**: the engine (`engine_*`) and its
CPU optimisation (branch `perf/cpu`) belong to the mixer session. Where they overlap, phase B5 is
done together with it.

## 0. Status (Oct 13, 21:25)

| Phase | Status | Detail |
|---|---|---|
| B0 Baseline | ✅ 16:51 and 16:55 | §10. System 122-130 of 400: engine 82, IRQs and kernel ~40 (all on CPU0), services < 2 |
| B1 Services (group A) | ✅ 16:56 | 18 units removed + linger for the engine's user. `getty@tty1` stays. Group B: `alsa-restore` masked (21:15, with the maintainer's OK); nvphs, nvs-service, polkit and nvfb not done |
| B2 Logging | ✅ 17:08 | rsyslog removed; journald without forwarding (applied on reboot) |
| QSPI | ✅ 17:18 | `nv-l4t-bootloader-config` masked (decided on Oct 13) |
| B3 Packages | ✅ 17:19-17:47 and `desktop` 21:11 | 957 packages in total (from 2267 to 1310), from 14 to 4.1 GB. CPU the same as in B0; +58 MB of RAM |
| Validation reboot | ✅ 21:15 | B1, B2, QSPI and B3 are validated by the reboots at 18:23 (another session), 19:03 (the screen's ⏻ menu) and the one at 21:15, after `desktop` (§10) |
| B4 Boot | — | Not started |
| B5 IRQs and priority | — | Not started; with the mixer session |
| B6 Minimal image | In progress (Oct 14) | [`IMAGE.md`](IMAGE.md): phase 0 decided; I1 (recipe and first image) next |

## 1. What to expect (being honest about the goal)

The board was already slimmed down on Oct 8 (booting without a desktop and a dozen services removed, see the
README, "Board state"). What is left of the base system **uses almost no CPU**: all its
processes together do not reach 1 % of a core. The CPU goes to the engine (`python3` at 178 %,
load 8.5). Therefore:

| Resource | Now | Expected | Where it comes from |
|---|---|---|---|
| Base system CPU | Services: < 1 % of a core. Everything outside the engine: 14-50 points of 400 | To be measured in B0 | Removing services leaves little to scrape. What may be noticeable is the **engine's priority and the IRQ distribution** (all on CPU0), in B5 |
| RAM | 2.36 GB available of 3.96 (engine streaming) | +80-120 MB | snapd 45 MB, networkd-dispatcher 17, gvfs and the D-Bus session 16, polkit 10, rsyslog 6… |
| Disk (SD) | 14 GB used | ≈ 4-5 GB | Unused NVIDIA SDK 7.2 GB, desktop and its apps ~1.5-2 GB, docker and snap 0.5 GB |
| Writes to the SD | ~6 KB/s steady state | −10-20 % | rsyslog duplicates what the journal already stores |
| Boot | 9.2 s to `multi-user` (kernel 2.4 + user space 6.8) | −1-2 s, to be measured | snapd, speech-dispatcher, grub-common, pppd-dns and networkd-dispatcher are in the boot doing nothing |
| Attack surface | snapd, docker and polkit installed, full desktop | much smaller | — |

The big CPU lever is the engine itself, and it has already been pulled: with the CPU saving (MR !29,
95d3630, installed on Oct 13 at 16:15) the engine uses 70-72 % of a core when not streaming, where
it used 162 % before. With the engine this low, **what is outside the engine starts to matter**: the whole
system shows 84-122 % of 400, so 14-50 points remain outside the engine, and that is what varies most.
Almost all of it is IRQs and kernel threads (host1x, display, USB), the app, MediaMTX and whatever tests
are running, rather than the system services. B0 has to break those 14-50 points down,
because how much B1 (services) and B5 (IRQs and priority) can deliver depends on that split.
This plan does not replace the work on the engine: it frees what is left around it and keeps the system from
competing with it at critical moments (boot, I/O bursts, IRQs).

## 2. Starting snapshot (Oct 13, 15:07, read-only, with F7 streaming)

- L4T R32.7.6, Ubuntu 18.04, `multi-user.target`, nvpmodel MAXN, governor `schedutil` (the 4 cores
  at 1479 MHz), no `jetson_clocks`, zram 4 × 494 MB (0 B in use), `swappiness` 60.
- **2267 packages**, 14 GB of 30 on the SD. `/usr/local/cuda-10.2` takes 2.9 GB and `/usr/src` 786 MB.
- RAM: 1642 MB used, `NvMapMemUsed` 915 MB (the engine's NVMM buffers), 2360 MB available. The
  journald shows 122 MB of RSS, but those are mapped files: Pss 77 MB and anonymous 4 MB, so it is not
  lost RAM.
- IRQs: **all device IRQs on CPU0**: host1x `host_status` (394 M), `xhci` (131 M),
  `host_syncpt` (67 M), PCIe/eth0 (43 M). `irq/70-host_sta` uses 9.8 % of a core.
- The engine runs as **the engine's user** (today a regular login user; a dedicated `eirl-engine` system user is planned).
  GIO loads `gvfs` and `dconf` into its process, and
  that starts `systemd --user`, a D-Bus session, `gvfsd` and `gvfsd-fuse`.
- `/tmp` is on the SD, not on tmpfs, and the engine has its `--state-dir` in `/var/lib/eirl-engine`.
- Writes to the SD (30 s): journald 3.6 KB/s, jbd2 2.1, rsyslog 0.1. A minute earlier,
  76 KB/s were measured (a burst from some test).
- Boot (`systemd-analyze blame`): snapd 9.0 s · automagic-fan 5.3 · networkd-dispatcher 3.4 ·
  nv-l4t-usb-device-mode 2.6 · avahi 2.1 · NetworkManager 2.0 · speech-dispatcher 2.0 ·
  alsa-restore 1.9 · eirl-hdmi-init 1.8 · pppd-dns 1.5 · grub-common 1.5.
- Failed: `snap-nvtop-346.mount` (a half-removed snap).

## 3. What the encoder needs (keep list)

Checked by the mixer session in the code (main + F7) and in the `.so` files the engine has
loaded (`/proc/<pid>/maps`, 253 libraries).

- **NVIDIA**: `nvidia-l4t-*` **in full, untouchable** (kernel, bootloader, core, 3d-core,
  multimedia, multimedia-utils, gstreamer, wayland, firmware…). That is where nvvidconv,
  nvv4l2h264enc, nvv4l2decoder, nvv4l2camerasrc, nvcompositor, nvjpeg and nvdrmvideosink come from.
- **GStreamer 1.14**: base, good, bad, ugly, libav (`avenc_aac`/`avdec_aac`), alsa and
  `gstreamer1.0-tools`. gl, gtk3, x, clutter, pulseaudio and packagekit are not needed.
- **Python 3.6**: `python3-gi`, `gir1.2-gstreamer-1.0`, `gir1.2-gst-plugins-base-1.0` and
  `python3-cairo`. The rest is the standard library. (The former Python engine needed them; the Go engine
  does not. `scripts/eirl-hdmi-init.sh` still runs `python3` with the standard library only.)
- **Fonts**: the screen uses the default sans (`fc-match sans` → DejaVu Sans). We need to
  keep `fonts-dejavu-core` and confirm whether any other is needed.
- **Tools**: `v4l-utils`, `alsa-utils`, `vim-common` (xxd), `iproute2`, `iw`, `iptables`
  (F7), `ca-certificates` (TLS with the cloud), `ffmpeg`/`ffprobe` (A/V meter and F7 tests:
  ask before removing them).
- **Services**: `eirl-hdmi-init`, `eirl-engine`, `eirl-encoderd`, `eirl-splash*`, `eirl-ap` +
  `eirl-ap-dhcp` (dnsmasq) + `eirl-media` (MediaMTX) (F7: confirm with its session),
  NetworkManager **and NetworkManager-dispatcher** (`90-eirl-uplinks`, SRTLA), `wpa_supplicant` (AP),
  avahi (`jetson.local`), persistent journald (the app reads the journal), `systemd-resolved`,
  `timesyncd` (TLS and SRT need a correct clock), ssh, `serial-getty@ttyS0` (serial rescue),
  the fan, `nvpmodel` (sets MAXN at boot), `nvzramconfig`, `nv.service` and
  `nv-late-init` (L4T boot), apparmor, cron, `fstrim.timer` and
  `systemd-tmpfiles-clean.timer`.
- 🔴 **haveged stays**. Kernel 4.9 has no jitter entropy and `getrandom` blocks until
  the CRNG is initialised. Without haveged, `eirl-encoderd`'s `crypto/rand` (TLS and WebRTC's DTLS) and
  SRT encryption could be left waiting after boot.
- In `/usr/local/bin`: `srtla_send`, `mediamtx`, `eirl-encoderd` and `eirl-splash`. `srt-live-transmit` and
  `libsrt` 1.5.7 come with the release bundle since Oct 14, in `/usr/lib/eirl-encoder/lib` (until then they were
  hand-built in `/usr/local` on the test board).

## 4. Inventory and classification

**Remove** = disable and mask (phase B1) and, if it is a package, purge it (phase B3).
**Verify** = measure or check first. The maintainer's decisions are in §8.

### 4.1 Services, timers and sockets

| Unit | What it is | Decision | Phase |
|---|---|---|---|
| `snapd` (+ `snapd.*`, `snap-*` mounts, `snap-repair.timer`) | Ubuntu snaps: only core22 and snapd, nothing of ours | Remove (and purge `snapd`) | B1 · B3 |
| `networkd-dispatcher` | systemd-networkd's dispatcher (NetworkManager is in charge here), Python, 17 MB | Remove | B1 |
| `speech-dispatcher` | The desktop's speech synthesis; 2 s at boot | Remove | B1 |
| `pppd-dns`, `dns-clean` | Leftovers of PPP or resolvconf | Remove | B1 |
| `grub-common` | GRUB's successful-boot mark; the Jetson does not use GRUB. 1.5 s | Remove | B1 |
| `ubuntu-fan` | Ubuntu's container networking | Remove | B1 |
| `spice-vdagent`, `spice-vdagentd` | Virtual machine agent | Remove | B1 |
| `ubuntu-advantage`, `ua-reboot-cmds`, `ua-timer.timer` | Ubuntu Pro | Remove | B1 |
| `apport` (+ `apport-autoreport.path`, `apport-forward.socket`) | Crash reports | Remove | B1 |
| `rsync.service`, `nfs-client.target`, `remote-fs.target` | rsync server and network mounts | Remove | B1 |
| `nvweston` | NVIDIA's Wayland (only with a desktop) | Remove | B1 |
| `nvmemwarning` | The desktop's low-memory warning | Remove | B1 |
| `getty@tty1` | Login on the screen. 🔴 **Stays**: if the boot fails, the splash hands the console back to the HDMI and the login that appears is this one (SPLASH.md, D4). It costs nothing | Stays | — |
| `anacron` (+ `.timer`) and the desktop's daily jobs (`update-notifier-common`, `apt-compat`, `man-db`, `cracklib`, `apport`) | Periodic tasks | Remove the jobs. 🪤 **anacron**: with the package installed, cron does not run `cron.daily` (it leaves it to anacron). If it is only disabled, `logrotate` stops rotating. It has to be **purged**, not just disabled | B1 · B3 |
| `rsyslog` | Copies the journal into `/var/log/syslog`. Nothing of ours reads it | Remove (D6) | B2 |
| `nvphs` | NVIDIA power hinting | Verify: does it touch the multimedia DVFS? Measure before and after | B1 |
| `nvs-service` | NVIDIA service of unclear purpose | Verify what it does and measure | B1 |
| `polkit` | D-Bus authorisation | **Stays**: the Wi-Fi client (W-B) talks to NetworkManager over D-Bus with a `.pkla` (in `keep.txt` since Oct 13) | — |
| `nvfb`, `nvfb-early` | L4T framebuffer | Verify: the splash draws on `/dev/fb0`. They probably stay | B1 |
| `alsa-restore` | Restores the ALSA mixer (1.9 s) | Remove. The capture does not need it: the engine sets `ADMAIF<n> Mux` = I2S4 itself with `amixer`. On the output HDMI (`tegrahda`, the headphones) only `IEC958 Playback Switch` = on matters, which is the default value, and the engine will keep it on itself (headphones work, Oct 13). The validation reboot can be used for it | B1 |
| `nv-l4t-usb-device-mode`, `serial-getty@ttyGS0` | Network 192.168.55.1 and console over the micro-USB, active: rescue path. The encoder does not use them (SRTLA only excludes them from the uplinks) | They stay on the development board (D5); removed in the image (B6) | — |
| `automagic-fan` | Fan: Python reading every 2 s; 5.3 s at boot | Measure before replacing it with the kernel's control (D7) | B0 · B1 |
| `haveged`, `avahi`, NetworkManager (+ dispatcher), `wpa_supplicant`, `eirl-*`, `hdmi-*`, ssh, journald, resolved, timesyncd, `nvpmodel`, `nvzramconfig`, `nv*`, cron, `fstrim`, tmpfiles | See §3 | They stay | — |

Already disabled on Oct 8 (no need to touch them, except purging them in phase B3): docker,
containerd, bluetooth, ModemManager, whoopsie, kerneloops, bolt, nvargus-daemon, rpcbind,
gpsd.socket, snapd.socket, udisks2, accounts-daemon and the apt and motd timers.

### 4.2 Packages (installed size according to dpkg)

| Group | Packages | Size | Decision |
|---|---|---|---|
| Unused NVIDIA SDK: CUDA 10.2, cuDNN, TensorRT, VPI, VisionWorks, OpenCV, samples, `nvidia-l4t-graphics-demos`, `nvidia-l4t-jetson-multimedia-api`, `nvidia-container-*`, and the local repo `cuda-repo-l4t-10-2-local` (1 GB of .deb) | 94 | **7.2 GB** | Purge (D2) |
| Desktop: `ubuntu-desktop`, gdm3, lightdm, unity, gnome-shell, lxde, xorg, pulseaudio (+ `gstreamer1.0-pulseaudio`), gvfs, dconf, packagekit, `gstreamer1.0-{gl,gtk3,x,clutter}`… | ~400, to be measured | ~1-1.5 GB | Purge (D3) |
| Desktop applications: chromium, thunderbird, libreoffice… | 45 | 0.8 GB | Purge (D3) |
| docker.io, containerd, runc, snapd (+ `/var/lib/snapd` 108 MB) | 5 | 0.5 GB | Purge |
| `-dev`, `-dbg` and `-doc` (includes `libc6-dbg` 58 MB) | 82 | 0.26 GB | Purge except what is used to compile on the board (D4) |
| Fonts (`fonts-noto-cjk` 90 MB and others) | 97 | 0.16 GB | Keep DejaVu (§3); purge the rest |
| `linux-firmware` | 1 | 0.32 GB | **Stays** (firmware for the Intel 8265 and for USB adapters) |
| `nvidia-l4t-kernel-headers` and `/usr/src` | — | 0.8 GB | They stay on the development board (D4); removed in the image |
| `~/` (spikes from the mixer's F0, SRTLA and F7, `~/build`) | — | 1.1 GB | Outside this plan: the session that owns each spike decides |

### 4.3 Other things that are not packages

- **The engine running as a login user**: user session, session D-Bus and `gvfsd` only because GIO
  loads `gvfs`. The quick fix is `GIO_USE_VFS=local` in the `eirl-engine` drop-in, or it
  goes away when gvfs is purged. The real one, a system user for the engine (also better
  for security). It belongs to the engine: B5, with the mixer session.
- **`/tmp` on the SD**: the `--state-dir /var/lib/eirl-engine` writes to the SD. It can be moved to `/run`, or
  `/tmp` can be mounted on tmpfs (B2).
- **IRQs and priority**: the engine runs with `Nice=0` and no I/O class, and all IRQs land on
  CPU0 (B5).
- **`noatime`** on the root filesystem, currently `relatime` (B2, optional: it changes `/etc/fstab` and requires
  a reboot).
- **Not touched**: `jetson_clocks` nor the `performance` governor (more power and no measurement showing it
  helps; besides, changing frequencies invalidates the engine CPU measurements the
  mixer session is taking, so never without telling it), zram (costs nothing until it is used), the
  kernel, the DT, the bootloader and the QSPI.

## 5. Phases

They are called **B0-B6** (for "base") so they are not confused with the mixer's F1-F7, neither in the
docs nor in the board's booking notes.

Each phase is applied in small groups and each group can be undone. From phase B1 on, every
change goes into the recipe in the repo (`base/`, §6), never by hand. Before and after each group,
measure with the phase B0 harness and run the validation in §7.

### B0 — Baseline measurement (changing nothing)

- **CPU: the mixer session's meters**, so the figures can be compared with
  theirs. They are in the `perf/cpu` branch (`tools/`, MIXER.md §17 "Meters") and all three only read:
  - `engine-cpu.sh [s]` + `thrcpu.py`: engine CPU per thread and system CPU, with sources, preview and
    new frames/s. `python3 thrcpu.py <pid> <s>` works for any process.
  - `engine-watch.py [min]`: engine RAM with its slope, CPU, output and fps every 30 s.
  - Same conditions as their baselines: 60 s, output off, noting the live sources, whether the
    preview is on and whether the HDMI has a signal. The meters are already on the board (a copy in
    the engine user's home): `engine-cpu.sh 60` and `engine-watch.py`.
  - Engine baseline with the CPU saving (main 95d3630, Oct 13 16:15): not streaming, HDMI without
    signal, both USB sources live, `net:cam1` without a sender, screen on and preview stopped →
    **engine 70-72 %, system 84-122 % of 400**, engine RAM 115 MB, drawing max. 30-62 ms.
  - 🪤 If someone opens the web, the preview turns on and adds ~15 points to the engine, plus the
    app's share. Check "preview" on the first line of `engine-cpu.sh` and discard the measurement if
    it is on.
- The baseline is taken with whatever engine is on the board at the start, and it is noted which one
  (`/usr/lib/eirl-encoder/engine/VERSION`). If the engine changes afterwards (e.g. when `perf/cpu` is installed), the
  baseline has to be measured again before comparing.
- **What those meters do not cover**, in a complementary `tools/base-measure.sh`, read-only
  and with JSON output:
  - CPU per core (user, sys, irq, softirq, idle) and CPU grouped into engine / app / F7 / base.
  - Context switches and interrupts per second, per IRQ and core.
  - `MemAvailable`, `NvMapMemUsed` and Pss per service.
  - Writes to the SD (`diskstats` and `/proc/<pid>/io`).
  - Temperatures, and power if the INA3221 exposes it.
  - `--compare a.json b.json` to compare two measurements.
- Fixed scenarios:
  - **S1**: engine running without streaming, with a fixed scene (HDMI on air + USB capture card). Read-only,
    it can be measured at any time.
  - **S2**: streaming for 10 min to the test destination. Needs a slot agreed with the maintainer.
  - **S3**: cold boot: `systemd-analyze`, `critical-chain eirl-engine.service` and the time
    until the web answers, the engine's screen is drawn and the output is ready. Needs a
    slot and a reboot warning.
- Deliverable: `base/baseline-<date>.json` (or in the scratchpad, if it is heavy) and a table in this doc.

### B1 — Services removed (without removing packages)

- **Group A**, unrelated to the engine (table 4.1, "Remove", phase B1): `disable --now` + `mask`.
  It is applied live without stopping the engine. The mixer session is told beforehand (it is
  measuring) and it is written down in the board's booking notes.
- **Group B**, to be verified (nvphs, nvs-service, polkit, nvfb, alsa-restore): one at a time, with
  an S1 measurement before and after. polkit, only after checking how `eirl-encoderd` talks to
  NetworkManager.
- Closing: reboot (S3, with a slot and a warning) to check that the boot is clean
  (`systemctl --failed` empty).

### B2 — Logging and writes to the SD

- rsyslog removed (D6). The journal stays persistent with 200 MB (Oct 12, to diagnose hangs):
  enough for weeks at the measured rate (~0.6 lines/s).
- The engine's `--state-dir` to `/run`, or `/tmp` on tmpfs (with the mixer session).
- `noatime` (optional, requires a reboot).
- Measure the writes before and after (S1 and S2).

### B3 — Package purge, by groups

It is the only thing that is hard to undo, so it comes with a safety net:

1. Backup: `dpkg --get-selections`, `apt-mark showmanual` and a tar of `/etc` in
   `~/base-backup-<date>/`. Going back = reinstalling from that list (NVIDIA's r32.7 repo
   is still published).
2. `sudo apt-mark hold 'nvidia-l4t-*'`. 🔴 The `apt upgrade` of Oct 4 rewrote the QSPI from the
   postinst of `nvidia-l4t-bootloader`: **no** operation in this plan may install,
   upgrade or remove an `nvidia-l4t-*`. Never `apt upgrade` nor `dist-upgrade`.
3. `sudo apt-mark manual <list from §3>`, so that `autoremove` does not take anything that is needed
   (python3-gi, alsa-utils, network-manager, avahi, ca-certificates… may be installed only
   as a dependency of the desktop).
4. By group, in this order: NVIDIA SDK → docker and snap → desktop applications →
   desktop → `-dev`/`-dbg`/`-doc` and fonts.
   - `apt-get -s purge … && apt-get -s autoremove --purge`, saving the list.
   - Abort if an `nvidia-l4t-*`, a `gstreamer1.0-*` from §3, `python3-gi`/`python3-cairo`,
     `network-manager`, `avahi-daemon`, `wpasupplicant`, `alsa-utils`, `v4l-utils`,
     `ca-certificates`, `haveged`, `openssh-server` or `linux-firmware` shows up.
   - Review the list, apply, and then `apt-get clean`.
5. The groups that may affect the engine (desktop: gvfs, dconf, pulseaudio and GStreamer
   plugins) are applied **in a slot with the output off**, restarting the engine afterwards. When
   the plugins change, GStreamer regenerates its registry and the first start is slower.

### B4 — Boot

- With B1 and B3 done, `critical-chain` up to `eirl-engine` and up to the screen being ready. Remove waits
  that add nothing.
- Any ordering change is coordinated with the splash session. The contract is in SPLASH.md §3:
  `eirl-engine` waits for the splash to finish.
- Goal: measure where the 6.8 s of user space come from and trim them without touching the kernel
  or the bootloader.

### B5 — The system at the engine's service (with the mixer session)

Measured with `engine-cpu.sh` and with the engine's "timings" (drawing, program→screen, new
frames/s), before and after and in the same conditions as B0.

- **Priority**. Today there is no `Nice`, `CPUSchedulingPolicy` nor `IOSchedulingClass`: everything runs with the
  defaults. Start with a moderate negative `Nice` (−5, then −10) and the default CPU policy,
  for `eirl-engine` and its children (`srt-live-transmit`, `srtla_send`).
  🔴 **No real time for the whole process**: it is about 120 GStreamer threads plus Python, and
  an RT thread that runs away starves the app and ssh of CPU. RT, at most, for one specific,
  measured thread.
- **IRQs**. Today they all land on CPU0. The ones that weigh the most with the engine running (top during the
  F7 test on Oct 13):
  - `irq/70 host_status` (host1x): 7-10 %.
  - `irq/69 host_syncpt`: 3-5 %.
  - `irq/74 tegradc` (the display).
  - The xusb one (USB 3).

  Try spreading or pinning them with `/proc/irq/*/smp_affinity` (on the Tegra210 some
  may not allow being moved) and, optionally, moving the engine's hot threads away from CPU0.
- **A dedicated system user** instead of the current login user: no user session, no gvfs nor session
  D-Bus, and better isolated. It is a big move, because today everything is tied to
  `/usr/lib/eirl-encoder/engine`, and all of this has to change at the same time:
  - the unit: `User=<engine-user>`, `SupplementaryGroups=video audio input` and the `ExecStart`;
  - the app's drop-in (`eirl-engine-eirl-encoder.conf`), which adds `eirl-encoder` (socket),
    `eirl-usbhub` (VBUS cut), `NoNewPrivileges=yes` and `EIRL_CONF`;
  - `scripts/eirl-hdmi-init.sh`;
  - `tools/deploy-engine.sh`, which installs into the `/usr/lib/eirl-encoder/engine` of whoever connects over ssh;
  - the meters, which look for the process by `^/usr/lib/eirl-encoder/engine/eirl-engine-go`;
  - the EDIDs, which live next to the engine's binary;
  - the scene's `STATE_DIR`, whose path is pending a decision by the maintainer. It fits with B2
    (`/tmp` on the SD).
- **Ordering with other branches**: the unit and `scripts/eirl-engine-start` may also be touched by other workstreams.
  Any change to the unit or to the script is made on top of whatever those two have merged into
  main.
- All of this lives in the engine's unit, drop-in or script: it is done with the mixer
  session, as an agreed change.

### B6 — Minimal image to flash (decided on Oct 13, D1: the last phase)

There will be a version of the encoder for the Jetson Nano, so this phase will be done, at the end,
when the recipe has been tested on the board. **Its plan, decisions and results are in [`IMAGE.md`](IMAGE.md).**
What was foreseen here on Oct 13:

- BSP R32.7.6 with `nv_build_samplefs.sh --flavor minimal` (minimal Ubuntu 18.04) and
  `apply_binaries.sh`. (R32.7.6's `nv_build_samplefs.sh` turned out to have no minimal flavour: the image starts
  from Ubuntu's own base instead, IMAGE.md §3.2.)
- On top, the `base/` recipe (list from §3, `/usr/local` binaries, units, driver, DT and EDID).
- The product image ships without USB device mode or the development tools (D4 and
  D5 are only for the development board).
- It is written to **another** card, never the board's own, and the current board stays as it is.
- The RK3588 version would be a different image: there the multimedia layer changes completely.

## 6. How it is applied (recipe in the repo)

Everything is in `base/` and in `tools/base-measure.py`. On the board it is copied to `~/eirl-base/`
(`scp -r base tools/base-measure.py jetson:eirl-base/`) and run with sudo. The state (what
there was before each group, the plans and what was purged) is kept in `/var/lib/eirl-base/`.

| File | What it does |
|---|---|
| `base/slim.sh --check\|--apply\|--undo <group>` | Unit groups (`base/units-<group>.txt`): disable + stop + mask, `linger:`, `file:` (installs `base/files/<name>` with a backup) and `mask:` (mask only). Saves the previous state the first time; `--undo` restores it. Aborts if streaming |
| `base/units-b1a.txt`, `units-b1b-alsa.txt`, `units-b2.txt`, `units-qspi.txt` | B1 group A, `alsa-restore` (B1 group B), B2 (rsyslog + `files/90-eirl-nosyslog.conf`) and the QSPI service |
| `base/purge.sh --plan\|--apply\|--undo <group>` | Purge of `base/purge-<group>.txt` (patterns). Safety net: backup in `/var/lib/eirl-base/backup`, hold of `nvidia-l4t-*` (except the named ones), `apt-mark manual` of `keep.txt`. The plan aborts if it removes something in `keep.txt`, an unnamed `nvidia-l4t-*` or a package with libraries loaded in a process (except `inuse-ok.txt`), or if it installs anything. `--apply` simulates again and aborts if it does not match the plan |
| `base/keep.txt`, `base/inuse-ok.txt` | What stays (§3, plus what was learnt in B3) and what may be removed even though it is loaded, with its reason |
| `base/run-b3.sh <groups…>` | Several groups in a row, detached from ssh (`setsid nohup`). If a plan changes, it simulates it again and only continues if it brings no unreviewed packages |
| `base/check-engine.sh` | After each group: GStreamer elements, Python, tools, `ldd` of NVIDIA and tools, DejaVu fonts |
| `base/check-nv.sh` | After each group, with the output off: NVENC, NVDEC, nvjpeg and nvcompositor for real (they load what NVIDIA opens with dlopen) |
| `tools/base-measure.py` | B0 measurement (§5) and `--compare` |

- Applying twice changes nothing.
- Each applied group is recorded in the "Board state" table of the README (what and how to undo it),
  with a line in the board's booking notes and a prior heads-up to the mixer session.
- Commit, push and MR only when the maintainer asks (the repo's contributor rules). In the MR: `docs: n/a (encoder
  in testing, D8)`.

## 7. Validation after each group

1. `systemctl --failed` empty and `systemctl is-system-running` = `running`.
2. Engine: a scene with HDMI and USB on air. `eirl-enginectl state` with the expected new fps
   per source and in the program, and live level meters.
3. Web `jetson.local`: login, WebRTC preview with audio, scene change from the web and from the screen.
4. Stream for 10 min to the test destination over SRT. Over SRTLA, if there are uplinks: the dispatcher creates its
   tables.
5. F7: the AP is visible, hands out DHCP, and a network camera publishes and enters the mixer.
6. Cloud: linking and `eirl-encoderd`'s heartbeat with the backend (TLS), and the clock synchronised.
7. Reboot (slot and warning): splash, the engine's screen, the app answers, S3 timings.
8. Power off and reboot from the screen's Power menu.
9. Compare with the B0 baseline, and write the group's result down here.

## 8. Decisions (taken by the maintainer on Oct 13)

| # | Decision | Recommendation | Maintainer (Oct 13) |
|---|---|---|---|
| D1 | Scope: slim this board with a reproducible recipe, or also a minimal image to flash | **Recipe on this board.** The image, only if the beta runs on Jetson | Yes to slimming; the image, last, with a version for the Jetson Nano |
| D2 | Purge the NVIDIA SDK (CUDA, cuDNN, TensorRT, VPI, VisionWorks, OpenCV: 7.2 GB) | **Yes**: nothing uses it or plans to, and it can be reinstalled through apt | Purge |
| D3 | Purge the desktop and its applications (gdm, unity, lxde, xorg, pulseaudio, gvfs, chromium, libreoffice…) | **Yes**. Going back = `apt install ubuntu-desktop` + `set-default graphical.target` | Purge |
| D4 | Development tools on the board (ffmpeg, compilers, kernel headers, `libssl-dev`, `tcl`) | **Keep** while it is the development board | Keep |
| D5 | USB device mode (192.168.55.1 + `ttyGS0` console), rescue path | **Keep** in development; removed in a product image | Keep |
| D6 | rsyslog removed, with the 200 MB persistent journal | **Yes** | Yes |
| D7 | Fan: `automagic-fan` (Python every 2 s) or the kernel's thermal control | Measure first; small impact | Measure first. **Oct 15: neither: our own daemon (`eirl-fan`, [`FAN.md`](FAN.md))** |
| D8 | B5: engine priority, IRQs and a dedicated user | **Yes**, after B0 and with the mixer session | Yes |

## 9. Coordination

| Who | What |
|---|---|
| Base session (owner of this plan) | B0-B4, the recipe, this doc |
| Mixer session | Keep list (§3), measurement harness, B5, `--state-dir`. Tell it before every change of services or frequencies: it is measuring `perf/cpu` |
| F7 session | Confirm that `eirl-ap`, `eirl-ap-dhcp`/dnsmasq, `eirl-media`, `wpa_supplicant` and `iptables` stay |
| Splash session | Any change in the boot order (B4) |
| Cloud session | `eirl-encoderd` needs `ca-certificates`, a synchronised clock and entropy (haveged) |

## 10. Results

### B0 — Baseline (Oct 13, 16:51 and 16:55; engine from main 95d3630)

Conditions: output off, preview off, screen on. HDMI with signal (it came back with the
16:41 reboot), both USB sources live and `net:cam1` without signal. Measured for 60 s with
`tools/base-measure.py`, twice.

| Measurement | 16:51 | 16:55 |
|---|---|---|
| System CPU (everything but idle and iowait, of 400) | 129.9 | 122.5 |
| · engine (`eirl-engine` cgroup) | 82.2 | 82.3 |
| · kernel threads (`irq/70 host_status` 9.2-9.6; `irq/69` 2.1; kworker 1.5; nvmap 1.0; tegradc 1.6) | 17.1 | 16.4 |
| · IRQ + softirq (counted separately in `/proc/stat`) | 22.9 | 23.0 |
| · all system services together | < 2 | < 1 |
| CPU0 versus the other cores | 39 / 30-31 | 38 / 28 |
| IRQs/s of `host_status` and `xhci` (100 % on CPU0) | 6583 · 2498 | 6585 · 2507 |
| Available RAM | 2529 MB | 2524 MB |
| Pss: snapd · MediaMTX · user session · networkd-dispatcher · fan | 42 · 42 · 16 · 10 · 6 MB | same |
| Writes to the SD | 32 KB/s | 24 KB/s |
| Power (5V_IN · CPU) | 4.9 W · 1.0 W | 5.3 W · 1.1 W |

Conclusions:
- **Services do not weigh on the CPU**. What there is outside the engine (~40 points) is IRQs and kernel
  threads of the multimedia block (host1x), of USB and of the display, **all on CPU0**. That is B5.
- The noise between measurements is ±5 points in the system total. Below that, an improvement cannot
  be seen in a 60 s window.
- 🪤 The mixer's `engine-cpu.sh` did not count IRQ or softirq (~23 points). Fixed in
  `fix/engine-cpu-irq`: its old "system" figures are good for comparing with each other, not as a
  total.
- **Boot** (the 16:41 reboot, from the Power menu): kernel 2.2 s + user space
  11.6 s. `multi-user` is delayed by snapd (8.7 s).
  - `eirl-engine` starts at 5.7 s, after `network-online.target`, that is,
    NetworkManager (3.2 s).
  - The splash is drawn at 4.0 s, the app listens at 8.7 s and the multiview appears on the screen
    at 14.9 s.
  - Possible gain for B4: the engine not waiting for the network to draw the screen. It belongs to the
    engine's unit: decided with the mixer.
- **The journal**: what writes the most are the agents' ssh connections. Each one started and
  stopped `user@1000` (29 times in 10 min, ~1300 lines). Besides, `wpa_supplicant` writes
  `ap0: CTRL-EVENT-SCAN-FAILED ret=-95` once per second: something asks for a scan on the
  AP's interface (F7, with no owning session right now).

### B1 group A — applied Oct 13 16:56 (`base/units-b1a.txt`)

- **Masked**: networkd-dispatcher, speech-dispatcher, pppd-dns, dns-clean, grub-common,
  ubuntu-fan, spice-vdagent(d), ubuntu-advantage, ua-reboot-cmds, ua-timer, apport (+ `.path` and
  `.socket`), rsync and nfs-client.target.
- **Only disabled**: `nvweston` and `nvmemwarning`. Their file is in `/etc/systemd/system` and
  they cannot be masked.
- **Linger** for the engine's user: the user manager stays up.
- `getty@tty1` stays (§4.1).
- Measurement after 30 s (b1a-1 versus b0-ref-2):
  - CPU 127.6 versus 122.5: within the noise, as expected.
  - Available RAM: +7 MB (networkd-dispatcher).
  - No failed services and `systemctl is-system-running` = `running`.
- The validation reboot (§7.7) is still missing; it will be done together with B2.
- Undo: `sudo ~/eirl-base/base/slim.sh --undo b1a`.

### Power cut — Oct 13 16:59:22

- The board switched off abruptly: the journal stops with no shutdown message. It happened while another session
  was restarting the engine and the app (with the USB capture cards starting up).
- The maintainer switched it on at ~17:03. It booted in 12.9 s, clean with B1: no unit of the recipe
  failed and linger active.
- The input reads **5.08 V at idle**; on Oct 12 it read 5.3 V. There were two other cuts the same day
  (15:24 and 15:35, according to the F7 session). It has nothing to do with the recipe, which touches nothing that draws
  current.
- 🔴 Failed unit on **every** boot of Oct 13, since before B1:
  `nv-l4t-bootloader-config`. Its script does not find `/dev/disk/by-partlabel/`, a race with
  udev at boot (the boot of Oct 12 at 21:40 went fine).
  - **Not touched**: that service updates the QSPI if its version does not match that of the
    `nvidia-l4t-bootloader` package. While it fails, it flashes nothing; "fixing it" could flash the QSPI
    on the next boot.
  - The maintainer decides: leave it like this or mask it, which would be the safe option.

### B2 — applied Oct 13 17:08 (`base/units-b2.txt`)

- `rsyslog` masked, plus `/etc/systemd/journald.conf.d/90-eirl-nosyslog.conf`
  (`ForwardToSyslog=no`). The drop-in applies on the next restart of journald or of the board.
- Checked: `logger` still reaches the journal.
- Undo: `sudo ~/eirl-base/base/slim.sh --undo b2`.

### B3 — simulated plans Oct 13 (`base/purge.sh --plan`, nothing removed)

| Group | Packages requested → plan with orphans | Size |
|---|---|---|
| `sdk` (D2) | 80 → 93 | 7237 MB |
| `docker` (containers and snap) | 16 → 19 | 384 MB |
| `apps` (D3) | 76 → 326 | 1104 MB |
| `desktop` (D3) | 126 → 680 | 690 MB |
| `extra` (debugging and fonts) | 8 → 16 | 176 MB |

The plans overlap, because each one is simulated on the current state. They are applied in that order and
each `--apply` simulates again: if the plan no longer matches, it aborts and the `--plan` has to be repeated.

What `purge.sh`'s protections stopped while simulating, and how it ended up:
- Without marking `keep.txt` as manual, the desktop took **`network-manager`** with it, which had
  come in as one of its dependencies. Now the safety net (backup in `/var/lib/eirl-base/backup`,
  hold of `nvidia-l4t-*` and `apt-mark manual` of `keep.txt`) is applied already when simulating.
- `nvidia-l4t-camera` is a dependency of `nvidia-l4t-gstreamer` and needs `libgtk-3-0`. That is why
  `dconf-gsettings-backend` and `dconf-service` stay.
- The multimedia samples need `xorg-sgml-doctools`, which also stays (until the `sdk` group
  is purged).
- **`gstreamer1.0-x` stays**: it brings `libgstpango` (the engine's textoverlay). The "in
  use" protection (libraries currently loaded in some process) would have stopped it even if it were not in
  `keep.txt`.
- `alsa-base` (card ordering), `debhelper`, `dh-autoreconf` and the
  GStreamer headers (D4) move to `keep.txt`.

### QSPI — Oct 13 17:18 (`base/units-qspi.txt`, decided on Oct 13)

- `nv-l4t-bootloader-config` masked (only `mask`: the unit is linked from `/opt/nvidia`
  and a `disable` would leave it with no way to be enabled again). It does not run again even if it stops
  failing. The board is `running` again.
- Undo: `sudo ~/eirl-base/base/slim.sh --undo qspi`.

### B3 — applied Oct 13 17:19-17:47 (`base/run-b3.sh`, with the maintainer's OK)

| Group | Purged | After the group |
|---|---|---|
| `sdk` | 93 packages, 7.2 GB | `check-engine.sh` OK |
| `docker` (containers and snap) | 15 | — |
| `apps` | 327 (plus `squashfs-tools`, an orphan of snapd: reviewed) | — |
| `extra` | 109 (with 93 desktop leftovers orphaned when the applications were removed, 67 MB: reviewed against the `desktop` plan) | `check-engine.sh` and `check-nv.sh` OK |

- Disk: from **14 GB to 4.4 GB** used. Packages: from **2267 to 1723**. `dpkg --audit` clean,
  no failed units and the board `running`.
- Lists of what was purged in `/var/lib/eirl-base/<group>.purged`. Undo:
  `sudo ~/eirl-base/base/purge.sh --undo <group>` (reinstalls through apt; needs network).
- What was fixed along the way:
  - 🪤 When applying, the simulation was done with the plan's list and not with the group's. Apt then found
    second-level orphans (476 versus 327) and it never matched. Now `--apply`
    simulates the same as `--plan`.
  - 🪤 `dpkg` marks packages on hold as "hi", and they were not counted as installed.
  - `run-b3.sh` sets `LC_ALL=C`, because `comm` needs the same ordering as `sort`.
- `check-nv.sh`: NVENC to a file, NVDEC from a file, nvjpeg and nvcompositor work fine. 🪤 NVENC and
  NVDEC in the SAME pipeline hang and `gst-launch -e` does not die with SIGINT: they run separately and
  with `timeout -k`.
- **Measurement b3-1 (17:53)**: not useful to see the effect of the purge.
  - Since the 16:59 cut, the Logi and the MS2109 capture card ("USB3. 0 capture", `534d:2109`)
    are not on the USB bus (and no errors in `dmesg`: unplugged or unpowered; the Cam Link has not
    been connected since Oct 12), and the engine only sees 2 sources (HDMI live + one without
    signal) instead of 3 live. That is why everything goes down: system 64 of 400, engine 36, power 3.8 W.
  - What is due to the purge: snapd is gone (−42 MB of Pss) and the available RAM rises to
    2739 MB (+215, part of it because there are fewer sources).
  - The measurement has to be repeated with the same sources as the baseline.
- `packagekit` and `rtkit-daemon` are still running: they go with the `desktop` group.

### Two reboots by other sessions validate B1, B2, QSPI and B3 (Oct 13 18:23 and 19:03)

They were not power cuts: both leave a record of an orderly shutdown.
- **18:23**: another session, with the maintainer's OK, to test its fix for the `SCAN-FAILED` noise of `ap0`.
- **19:03**: reboot requested from the screen's ⏻ menu (`eirl-encoderd` → `sudo systemctl reboot`).

Checked at 20:05, read-only:
- `running` and **no failed units** (before, `nv-l4t-bootloader-config` failed on every boot).
- `slim.sh --check` of b1a, b2 and qspi: everything as it was left. rsyslog is not running and journald already started with
  the `ForwardToSyslog=no` drop-in.
- Boot: **2.2 s of kernel + 7.7 s of user space** (11.6 s before). The credit cannot be split
  among B1, B3 and the rest: this afternoon the network and the app also changed (Wi-Fi client
  connected instead of the AP). B4 will measure it with `systemd-analyze blame`.
- Engine (restarted by the headphones session at 19:59): HDMI, MS2109 and Logi live, "valid capture timestamps"
  on all three, `screen.state` = `on`. **The Logi and the MS2109 are back on the bus**, so the
  measurement can now be repeated with the same sources as the baseline.

### Measurement b3-2 — Oct 13 20:13, with the same sources as the baseline

Conditions: output and preview off; HDMI, MS2109 and Logi live. The engine is no longer 95d3630
but main 895f90d (header, settings and Wi-Fi on the screen), so its share is not comparable.
File: `~/eirl-base/b3-2.json`.

| | B0 (16:51 · 16:55) | b3-2 | Reading |
|---|---|---|---|
| System CPU (of 400) | 129.9 · 122.5 | 136.1 | Without `user.slice`: 128.4, the same as in B0 |
| Engine (`eirl-engine`) | 82.2 · 82.3 | 84.8 | Another engine version; within the noise |
| Kernel threads + IRQ | 17.1 + 20.5 | 16.6 + 21.7 | The same: still on CPU0 (host_status 6782/s, xhci 1955/s) |
| `user.slice` | 1.2 · 0.4 | 7.7 | Processes of another session over ssh during the measurement, not from the base |
| System services | < 2 | < 1 | |
| Available RAM | 2524 MB | 2563 MB | +39 MB (snapd is gone) |
| Writes to the SD | 24 KB/s | 62 KB/s | journald 27 + init 13: logs of the agents' ssh sessions (sshd, logind and "Started Session", 290 lines in 10 min). Not from the base |
| Power 5V_IN | 5294 mW | 5012 mW | −0.3 W, with no clear explanation: not taken as valid |

**Conclusion: as expected (§1), removing services and packages frees no visible CPU.** What
B1 to B3 gain is disk (from 14 to 4.4 GB), a shorter and cleaner boot (from 11.6 to 7.7 s of user
space, with no failed units), less surface (snapd, docker, the desktop) and some RAM.
The spare CPU in the system is in CPU0's IRQs, which is B5.

### Group `desktop` + `alsa-restore` + reboot — Oct 13 21:10-21:23 (slot and OK from the maintainer)

- **Plan** redone at 21:08: 413 packages and 344 MB, versus the afternoon's 680, because `extra` already
  took the leftovers. What is new is orphans: Qt5, zenity, libunity and MTP. PulseAudio, gvfs,
  packagekit, rtkit, anacron, Xorg, gdm, lightdm, gnome-keyring and its PAM module go. These stay:
  `libpulse0` (ffmpeg and GStreamer use it), `alsa-utils` and **`policykit-1`**. The latter is added to
  `keep.txt`, because the Wi-Fi client (W-B) talks to NetworkManager over D-Bus with a `.pkla`.
- **Purge** (21:11, `rc=0`): `dpkg --audit` clean and nothing of the desktop installed. `ldd` of the 123
  `tegra/*.so*`, `libgstnv*.so` and `tegra-egl` without a single "not found" (requested by the mixer session: `libnvbuf_utils`
  links libEGL and libX11). `check-engine.sh` and `check-nv.sh` OK. ssh logs in without problems after removing
  the keyring's PAM module.
- **`alsa-restore` masked** (`base/units-b1b-alsa.txt`). The mixers of the four cards
  (HDA, APE, MS2109 and Logi) are **identical** to the ones `/var/lib/alsa/asound.state` saved: zero
  different controls. The Logi keeps its mic at 44 (40 dB).
- **Reboot** at 21:15:10; the board comes back in 35 s. `running`, no failed units and a boot
  of 2.2 + 8.9 s. All the `slim.sh --check` remain as they were left.
- **Engine** (the mixer session's criterion): 0 `Traceback`; "HDMI de la placa (hueco 1): en vivo" (board HDMI, slot 1: live) in `[mixer]`
  and in `[audio]`; valid capture timestamps on the 5 inputs; `screen` = on; the splash goes away by itself.
  Headphones: `pcm3p` in `RUNNING` and "suena por hw:tegrahda,3" (playing on hw:tegrahda,3). Before the reboot they had been switched off
  by hand and they start switched on. It is not a bug: the whole scene is saved in
  `/var/lib/eirl-engine/mixer-scene.json`, which is emptied at boot, and the monitor starts switched on (D1).
  Moving it to `/var/lib/eirl-engine` is pending the maintainer's decision (the mixer session owns it).
- ⚠️ **The HDMI audio flips** between "live" for 1 s and "no HDMI audio" every 20 s. **It is not due to this
  purge**: in the boot from 18:23 to 19:03, with the desktop and `alsa-restore` still in place, it already
  flipped about 60 times every 10 minutes; in the one from 19:03 to 21:15 it was stable. According to the mixer session, it is the TC358743's
  audio PLL, which does not lock (`AU_STATUS0=0x41`). The software recovery of
  the engine's watchdog does not fix it, and without a reset pin in the DT it stays like that. The bursts are
  the I2S starting and stopping. It depends on the audio negotiation with the source at each boot.
  The mixer session owns it, with the maintainer.
- **Measurement** `desktop-1` (21:21; output and preview off; HDMI, MS2109 and Logi live;
  headphones playing):

| | B0 (16:55) | desktop-1 |
|---|---|---|
| System CPU (of 400) | 122.5 | 124.5 |
| Engine | 82.3 | 79.2 (with the headphones) |
| IRQ + kernel threads | 20.6 + 16.4 | 19.2 + 18.0 |
| Interrupts/s | 19.4 thousand | 22.9 thousand: mostly rescheduling IPIs (4.7 thousand/s, wake-ups between cores because of more threads, like the monitor's) and some host1x; from the HDA, nothing noticeable (mixer session) |
| Available RAM | 2524 MB | 2582 MB (+58) |
| Writes to the SD | 24 KB/s | 17 KB/s |
| Disk used · packages | 14 GB · 2267 | 4.1 GB · 1310 |
| Running services | — | 23 |

  With the mixer session's meter (`engine-cpu.sh 60`): engine 77 % and system 136 % of a core.
- `systemd-analyze blame`: the slowest is `automagic-fan` (5.0 s), then mounting the SD, the
  journal flush, `eirl-hdmi-init` and NetworkManager. It is the starting point of B4 and of D7.

### What is left

1. **B1 group B**, one at a time and with a measurement before and after: `nvphs`, `nvs-service` and `nvfb`.
   `polkit` **stays**: the Wi-Fi client (W-B) needs it.
2. **B4, boot**: `automagic-fan` takes 5 s (with D7: measure before replacing it with the kernel's
   control), and `eirl-engine` waits for `network-online`. The latter is coordinated with the mixer session and with the
   splash session.
3. **B5, interrupts and priority**, with the mixer session. That is where the CPU that can be
   recovered is: all device IRQs land on CPU0.
4. **B6, minimal image** for the Jetson Nano: the last phase, on another card.

To purge another group or repeat this, the procedure is the one in §6 and §7. A slot and a warning are needed,
and the engine is validated with the mixer session's criterion (above).
