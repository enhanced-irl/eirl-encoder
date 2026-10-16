> **Status:** IN PROGRESS (I1-I3 done on Oct 14-15: the test board runs the image; I4, the image of 0.7.0 with its notices and sources, Oct 16)
> **Updated:** 2026-10-16

# A minimal microSD image for the Jetson Nano

A ready-to-flash microSD image of the encoder for the **Jetson Nano Developer Kit**. You write it to a
card, put the card in a Nano and it boots straight into the encoder: boot splash, then the touch screen
(or the web) with the first-boot wizard. From then on it updates itself over the air (UPDATES.md).

It is phase **B6** of [`BASE.md`](BASE.md) (the last one, decided on Oct 13): the board's slimming
recipe (B0-B3), applied from scratch to NVIDIA's own base instead of by hand on a board.

## 0. Status

| Phase | Status | What |
|---|---|---|
| F0 Research and proposal | ✅ Oct 14 | This document; the maintainer's decisions are in §12 |
| I1 Recipe and first image | ✅ Oct 14 | `tools/image/`: test and debug images of release 0.1.1, every offline check passes (§10.1) |
| I2 First boot and uniqueness | ✅ Oct 14-16 | Two boots from scratch on the test board, with different identities, and later boots (§10.4, §10.6) |
| I3 Test on a real card | ✅ Oct 14-15 | Wizard, sources, a stream, a real OTA (0.1.1 → 0.3.0) and reboots (§10.4). The maintainer chose to back up the board's own card and use it; the board **keeps the image** |
| I4 Publish | 🟡 Oct 16 | The image of 0.7.0, with its notices, its sources and the written offer (§9.3); flashing and verification in the README; booted from scratch on the test board (§10.6); signed and published as assets of release v0.7.0 with the maintainer's OK (§10.5) |

## 1. Scope

**What it is.** One image per release of the encoder software (or less often, §8.3), for:

| | |
|---|---|
| Board | Jetson Nano Developer Kit **4 GB, carrier B01** (module P3448-0000 with a microSD slot, carrier P3449-0000 rev B01). The device tree is the B01's (`…-p3449-0000-b00-tc358743.dtb`) with the TC358743 on **CAM1** |
| Not supported | The A02 carrier (one camera port), the 2 GB Nano (P3448-0003, another carrier and DT), the production module with eMMC (P3448-0002). They would need their own DT and their own image |
| BSP | NVIDIA **L4T R32.7.6** (the last R32 release; Ubuntu 18.04, kernel 4.9.337-tegra), the same as the test board |
| What the user does | Flash the card (balenaEtcher or `dd`, §8.4), insert it, power on. Nothing else: no NVIDIA setup screens, no account, no password typed into a console |

**What it is not.** It does not update L4T, the kernel or Ubuntu afterwards (UPDATES.md: only our software
is updated over the air). A new L4T or kernel means a new image and reflashing.

## 2. What goes in

| Layer | Source | Pinned by |
|---|---|---|
| Boot partitions on the card (p2-p14: TBC, RP1, EBT, WB0, BPF, BPF-DTB, FX, TOS, DTB, LNX, EKS, BMP, RP4) | NVIDIA's signed boot images, as `jetson-disk-image-creator.sh -b jetson-nano -r 300` lays them out. They let the card boot on **any** QSPI version (§4) | BSP sha256 + sha256 of each partition image |
| Root filesystem (p1 `APP`, physically the last partition, grown at first boot) | Ubuntu 18.04 arm64: `ubuntu-base-18.04.5` + the packages of `tools/image/packages.txt` from `ports.ubuntu.com` (§3.2) | Ubuntu base sha256; the exact package list with versions, checked on every build (bionic no longer changes in its public pockets) |
| NVIDIA BSP | The `nvidia-l4t-*` packages of the Driver Package, installed with `apply_binaries.sh` (§3.3), minus the ones the encoder does not use | BSP sha256 `865717ee…0212` (279 MB) |
| Kernel | NVIDIA's stock `Image` and `initrd` (package `nvidia-l4t-kernel`), **not rebuilt** | BSP |
| Our kernel bits | `tc358743.ko` (patched, `driver/`) in `/lib/modules/4.9.337-tegra/extra/`; the cellular modem modules (`qmi_wwan`, `cdc_mbim`, `huawei_cdc_ncm`, `option` with the Quectel IDs: MODEMS.md §4) in `updates/eirl-modems/`, which wins over the kernel's own; `btusb` and `btrtl` with the Realtek RTL8761B in `updates/eirl-bluetooth/` and its firmware in `/lib/firmware/rtl_bt/` (BLUETOOTH.md §3.8); the B01 DTB with the TC358743 (`dt/`) in `/boot/`; an extlinux entry with that `FDT` (README, "Installing on the Jetson") | `build/build.sh` (kernel sources by sha256) |
| Our software | The **latest signed release** (the OTA bundle of UPDATES.md §4): downloaded, verified with `release/eirl-release.pub`, installed with its own `install.sh` in image mode (§3.4) | The release's signed manifest |
| Fan | Our own controller, `eirl-fan`, in the release since 0.6.0 ([`FAN.md`](FAN.md); §12, D-I7) | The release |
| Notices and sources | `/usr/share/doc/eirl-encoder-image/`: `NOTICE` and `SOURCES.md` with the written offer, made from what is installed (§9.3) | The build |

Until Oct 14 the bundle did not carry everything a **new** device needs: `srt-live-transmit` and `libsrt` (hand-built
in `/usr/local` on the test board), the EDID files and `/etc/default/eirl-engine` (without which the engine unit
does not start). They are in the bundle since then, from the next release on: `srt-live-transmit` with `libsrt`
1.5.7 in `/usr/lib/eirl-encoder/lib` (RUNPATH), the EDIDs in `/usr/lib/eirl-encoder/engine`, a default
`/etc/default/eirl-engine`, and a new device starts with the output **off**. So the OTA keeps them current, and the
**first image has to carry a release after 0.1.0** (§12, D-I9).

What the bundle needs from the base system: the groups `i2c` and `input`, and the packages `v4l-utils`,
`i2c-tools`, `python3`, `libssl1.1`, `sudo` and `systemd`. What it does **not** carry, and the image does: the
TC358743 module, the DTB and the `tc358743` extlinux entry.

## 3. How it is built

`tools/image/build.sh` runs every step in containers, from downloads pinned by sha256, and writes
`build/out/image/`. Nothing is installed on the build machine.

```bash
# A test image (the board's firmware is only read) of the release in DIR (manifest.json, .sig and bundle):
tools/image/build.sh --release DIR --test
# A debug image for a board test: as --test, plus a key-only eirl-debug user and ssh on (never published):
tools/image/build.sh --release DIR --debug-ssh-key ~/.ssh/id_ed25519.pub
# A product image (the release must be signed with the production key; it can update the QSPI once, §4):
tools/image/build.sh --release DIR
# The same, but its first boot only reads the QSPI, as a test image does:
tools/image/build.sh --release DIR --firmware-check-only
```

| File | What |
|---|---|
| `tools/image/build.sh` | The steps below, on the build machine: verifies the release (`eirl-release verify`), runs `build/build.sh` if the kernel bits are missing, downloads and checks the pinned inputs, runs the stages in a Docker volume |
| `tools/image/pins.sh` | Every input: BSP and Ubuntu base (URL, sha256), the tool containers' base (by digest), kernel release |
| `tools/image/Dockerfile`, `boot.Dockerfile` | The work containers: `linux/arm64` (root filesystem, assembly, checks) and `linux/amd64` (NVIDIA's i386 tools) |
| `tools/image/stages/bootparts.sh` | The card's signed boot partitions with NVIDIA's `flash.sh --no-flash --sign` (§3.6) |
| `tools/image/stages/rootfs.sh` | The root filesystem (§3.2-§3.6) |
| `tools/image/stages/assemble.sh` | The card image and its `xz` (§3.6) |
| `tools/image/stages/sources.py` | The image's `SOURCES.md` (§9.3), from the installed packages, the pins and the release's own notices |
| `tools/image/stages/check.py` | The offline checks (§10.1); writes a report next to the image, with the `packages.txt` and `sources.md` published next to it |
| `tools/image/packages.txt`, `packages.lock` | What is installed, and the exact result with versions (a change fails the build until `--update-lock` and a review) |
| `tools/image/rootfs/` | Files copied into the image: the first boot (`eirl-firstboot`, `eirl-firmware` and their unit), the persistent journal, NetworkManager's cable override, the image's `NOTICE` |

A product image is built only from a committed recipe (its commit goes into `SOURCES.md` and the signed manifest);
test and debug images of an uncommitted tree say `<commit>-dirty`. How a product image is signed and published:
§10.5.

### 3.1 Build host

Tested on Oct 14 on the maintainer's machine (Apple Silicon, Docker Desktop, Linux VM aarch64), with a small
rootfs and image, not with a real build (§12, D-I3):

| Step | Where | Result |
|---|---|---|
| Root filesystem: extract `ubuntu-base`, chroot, apt | arm64 container, `--cap-add SYS_ADMIN --security-opt apparmor=unconfined` | **Native**, no qemu: `apt-get update` 16 s, 308 packages in 162 s |
| `systemctl` in the chroot (systemd 237) | same | `enable`, `disable`, `mask`, `set-default` work. `daemon-reload`, `restart`, `start`, `is-active`, `show` print "Running in chroot, ignoring request" and **return 0 with empty output**. `udevadm control --reload` exits 2. `systemd-tmpfiles`, `useradd --system`, `runuser`, `visudo -cf` work |
| NVIDIA's host tools (`tegrabct`, `tegrasign`, `mkgpt`… 32-bit i386 static ELF), for the signed boot partitions | amd64 container (`--platform linux/amd64`) | They run (`tegrabct`, `mkgpt` answer). `flash.sh --no-flash` itself is tried in I1 |
| Assembling the image | arm64 container, **no loop devices and no privileges** | `truncate` + `sgdisk -o -n …` + `mkfs.ext4 -d <rootfs> part.fs` + `dd conv=notrunc,sparse seek=<start>`. fsck clean; owners, setuid, capabilities, xattrs, device nodes, hard links and sparse files kept |
| Loop devices (`losetup -P`) | privileged container | They work, but the container never gets `/dev/loopNp*` nodes, only 8 loops exist and they are shared by every container in the VM (a leaked one stays until the VM restarts): avoided |
| Compression (927 MiB of real rootfs data) | arm64 | `xz -T0 -6` 60 s, ratio 0.32; `zstd -19 --long=27` 89 s, 0.32; `xz -9e` > 5 min |

So the recipe runs on a Mac with Docker, and on any Linux machine with Docker. Traps already found:
- `sgdisk -n` on a blank file does nothing and returns 0: always `-o` first.
- e2fsprogs ≥ 1.47 adds `orphan_file`, which kernel 4.9 does not know: the filesystem is made in an Ubuntu 18.04 container
  (e2fsprogs 1.44.1).
- Installing `openssh-server` creates host keys and installing `systemd` fills `/etc/machine-id`: §3.6 removes them.
- Other sessions prune Docker images on the same machine: the recipe pulls its base images by digest and rebuilds its tool
  images at every run.
- Disk: a build needs ~10 GB free in the Docker VM (BSP 0.3 GB, rootfs ~2.5 GB, raw image ~4 GB sparse, compressed ~1 GB);
  the script checks it before starting.

**Later (D-I3): a GitHub Actions workflow** on the public repository. GitHub has native arm64 Linux runners, and
x86-64 ones for the one i386 step, so the same recipe runs there. CI would build the image and attach it unsigned;
the signature stays with the maintainer (the seed never reaches CI), over the manifest of the image CI built. The
recipe is written for that from the start: nothing in it depends on macOS.

### 3.2 Root filesystem

NVIDIA's `nv_build_samplefs.sh` in R32.7.6 has **no minimal flavour** (flavours arrived with R35): it builds
the 2,178-package desktop and only runs on an x86-64 Ubuntu host. The sample rootfs tarball (1.5 GB) is that
same desktop. Starting from it means purging ~1,000 packages again, as B3 did on the board. Instead:

1. `ubuntu-base-18.04.5-base-arm64.tar.gz` (the official Ubuntu base, ~25 MB), extracted as root.
2. In a native arm64 chroot: `apt-get install --no-install-recommends` of `tools/image/packages.txt`, which
   is `base/keep.txt` without the development group (D4) and without what the Python engine needed (§11),
   plus the Ubuntu libraries the kept NVIDIA packages depend on (§3.3).
3. The exact result (`dpkg-query -W` with versions) is compared with `tools/image/packages.lock`: any
   difference fails the build until it is reviewed and the lock is updated.

### 3.3 NVIDIA BSP

- `apply_binaries.sh -r <rootfs>` from the Driver Package. Its `nv-apply-debs.sh` installs **every** `.deb` in
  `nv_tegra/l4t_deb_packages/` with `dpkg -i` (not apt, under `set -e`), so their Ubuntu dependencies must be in
  the rootfs beforehand (§3.2), and it refuses to run unless `/usr/bin/qemu-aarch64-static` exists, even on an
  arm64 host where it never runs it (a stub file is enough).
- `apply_binaries.sh` installs NVIDIA's `extlinux.conf` **before** the packages (the kernel package's postinst
  needs it), then `nv_customize_rootfs.sh` (system groups `gpio`, `crypto`, `trusty`; `ondemand` off).
- Kept (20): `core`, `init`, `tools`, `firmware`, `xusb-firmware`, `initrd`, `configs`, `apt-source`,
  `bootloader`, `kernel`, `kernel-dtbs`, `libvulkan`, `wayland`, `x11`, `3d-core`, `cuda`, `multimedia-utils`,
  `multimedia`, `camera`, `gstreamer`. Left out (removed from the package folders before applying): `weston`,
  `graphics-demos`, `oem-config`, `kernel-headers` (its package is in the BSP's `kernel/` folder), `jetson-io`,
  `gputools` and the two `jetson-gpio` Python packages (one needs python2). `jetson-gpio-common` (udev rules and the
  `gpio` group, no dependencies) stays: `nv-apply-debs.sh` copies `tools/*.deb` and fails if there is none. Nothing
  kept depends on what is left out. The hardware encode chain (gstreamer → camera → multimedia →
  cuda → 3d-core) pulls X11, EGL, cairo and GTK **libraries** (36 direct Ubuntu dependencies, `libgtk-3-0` the
  biggest), not an X server.
- `nvidia-l4t-cuda` is NVIDIA's driver library (`libcuda`, 16 MB), part of the Driver Package, and it cannot go:
  the multimedia, camera and GStreamer libraries link it. The CUDA toolkit, cuDNN, TensorRT and VisionWorks are
  **not** installed (other licences, and the encoder does not use them).
- 🪤 The `nvidia-l4t-init` postinst rewrites `/etc/fstab`, `/etc/hosts` and `/etc/hostname`: the system
  configuration (§3.5) goes **after** `apply_binaries.sh`.
- `/etc/nv/nvfirstboot` stays in the image: at first boot NVIDIA's `nvfb-early` sets up the EGL/GL library
  paths with it, rewriting NVIDIA's `/etc/ld.so.conf.d/nvidia-tegra.conf` and running `ldconfig`. Until then
  `libcuda.so.1` and NVIDIA's other libraries would not resolve, so the image adds its own
  `/etc/ld.so.conf.d/00-eirl-tegra.conf` with the same paths and runs `ldconfig` at build time. NVIDIA's file is not
  touched (it carries NVIDIA's notice, §9.1).
- The apt sources (`nvidia-l4t-apt-source`) carry a `<SOC>` placeholder that NVIDIA's `nvfb.sh` replaces with
  `t210` at first boot. The build does not need NVIDIA's repository after `apply_binaries.sh`, so it is left to
  NVIDIA's script.
- 🪤 The `nvidia-l4t-kernel` postinst runs `nv-update-extlinux generic`, which forces `DEFAULT primary`. Our
  `FDT` therefore goes into `LABEL primary` too, so a stray reinstall cannot boot the stock DT (no HDMI
  capture, no splash). And every `nvidia-l4t-*` is put on hold, as on the board.
- 🪤 `nvpmodel.service` is only enabled by NVIDIA when a display manager is installed: the build enables it
  (it sets MAXN at boot).

### 3.4 Our software

1. Download the release's `manifest.json`, `manifest.json.sig` and bundle (or take a local `tools/release.sh`
   build), and verify them with `eirl-release verify` against the public key in the repo. The build aborts on any
   mismatch.
2. Before installing, write the `tc358743` extlinux entry (our `FDT`) as `DEFAULT`: the bundle's splash copies the
   default entry into its own `eirl-splash` entry and makes that one the `DEFAULT`.
3. Extract the bundle into the chroot and run `EIRL_IMAGE=1 ./install.sh` (or `--image`): files, users, groups
   and an offline `systemctl enable` (`eirl-encoderd`, `eirl-engine`, `eirl-hdmi-init`, `eirl-update-recover`,
   `eirl-ap`, the splash). It never reloads, starts or stops anything, never runs udevadm, `systemd-tmpfiles` or the
   dispatcher, never touches `/sys` or `/dev`, and refuses if systemd manages that same root. It is tested by
   `tools/image-install-test.sh` (a real chroot in an Ubuntu 18.04 arm64 container).
4. Check that `/usr/lib/eirl-encoder/VERSION` is the manifest's version and that every file of the bundle's
   `files.txt` is installed with the bundle's content.

### 3.5 System configuration

Applied as files and `systemctl` commands in the chroot, each one from the repo (`tools/image/files/`):

- `/etc/nv_boot_control.conf`, as NVIDIA's `flash.sh` writes it for this board (`TNSPEC 3448-300-…-jetson-nano-qspi-sd-mmcblk0p1`,
  `TEGRA_CHIPID 0x21`, `/dev/mtdblock0`): the firmware check (§4) needs it.
- `default.target` = `multi-user.target`. **No oem-config**: NVIDIA links `default.target` to
  `nv-oem-config.target` only when `/etc/gdm3` exists; the build also does what `l4t_create_default_user.sh`
  does to skip it (`rm etc/systemd/system/default.target`, `touch etc/nv/nvautoconfig`), **without creating a
  user**.
- The `base/` units (§11): B1 group A, B2, `alsa-restore`, the QSPI service (§4), D5 out (USB device mode and
  `serial-getty@ttyGS0` masked).
- Persistent journal, 200 MB (`90-eirl-persistent.conf`, as on the board) and no syslog (B2).
- The apt and motd timers masked (`apt-daily`, `apt-daily-upgrade`, `motd-news`), as on the board since Oct 8
  (BASE.md §4.1): `fstrim` is the only timer left.
- The system group `debug`, which NVIDIA's udev rules (`99-tegra-devices.rules`) give the GPU's debug nodes to and
  L4T's own image creates with its first user.
- A clock floor: the Nano has no RTC battery, so a first boot would start at systemd's build date (2023) until NTP
  answers, and TLS (the OTA, the cloud) would reject today's certificates. The image ships timesyncd's clock file
  (`/var/lib/private/systemd/timesync/clock`, linked from `/var/lib/systemd/timesync`: the unit has `DynamicUser=`)
  with the build time, and timesyncd moves the clock forward to it when it starts.
- Hostname `eirl-encoder` (renamed at first boot, §5), `C.UTF-8`, time zone UTC until the wizard.
- No login users; `root` locked; the only sudo rule is the app's (`/etc/sudoers.d/eirl-encoder`).
- Ubuntu's branding replaced (§9.2): `/etc/os-release` `NAME`/`PRETTY_NAME`, `/etc/issue`, `/etc/lsb-release`
  `DISTRIB_DESCRIPTION` and the login messages. `ID=ubuntu`, `VERSION_ID` and `bionic` stay, because apt and
  NVIDIA's scripts read them.

### 3.6 Cleaning and assembly

- Removed: apt lists and caches, logs, `/tmp`, shell histories, `/etc/machine-id` content (left **empty**),
  `/var/lib/dbus/machine-id`, `/var/lib/systemd/random-seed`, SSH host keys, `/var/lib/NetworkManager/*`,
  `/etc/NetworkManager/system-connections/*`, `/etc/eirl-encoder/*` (the app creates it all at first start,
  §6), `/var/log/journal/*`, the qemu stub and the build's `resolv.conf`.
- **Kept**: every `/usr/share/doc/*/copyright` and `/usr/share/doc/nvidia-tegra/` (licences, §9).
- The `APP` partition is made only as big as needed (used + ~1 GB) and grown to the whole card at first boot.
- The boot partition images (p2-p14) do not depend on the root filesystem: only on the BSP, the board
  (3448, FAB 300) and the kernel command line that U-Boot's `LNX` image carries. They are generated **once per BSP**
  with NVIDIA's own `flash.sh --no-flash --sign jetson-nano-qspi-sd mmcblk0p1` (its tools are 32-bit i386, so in a
  `linux/amd64` container), with the **stock** DTB (ours only ever goes to the kernel, through `FDT`), and their
  sha256 are pinned in `tools/image/`. Every build reuses them.
- The image file: the 14-partition GPT of the B01 layout (`sgdisk`, APP numbered 1 but physically last), the boot
  partition images written with `dd`, and the root filesystem made with `mke2fs -d` in an Ubuntu 18.04 container
  (e2fsprogs ≥ 1.47 enables `orphan_file`, which kernel 4.9 does not know).
- `xz -T0` compression; sha256; the image manifest and its signature (§8).

## 4. The board's firmware (QSPI)

The Nano devkit keeps its boot firmware in a QSPI-NOR chip on the module and the system on the card. A Nano
bought today may carry firmware from any L4T release. What NVIDIA does, read in the R32.7.6 BSP:

- **Every boot**, `nv-l4t-bootloader-config.service` compares the installed `nvidia-l4t-bootloader` version with
  the QSPI's (`l4t_payload_updater_t210 -v`) and writes the QSPI only if the package is **newer**. It never
  downgrades and never rewrites an equal version. It needs `/etc/nv_boot_control.conf`, the board EEPROM
  (`i2cget`, `xxd`) and python2 (the updater's shebang is `/usr/bin/python`): without them it does nothing.
- **A QSPI from JetPack ≤ 4.4** (the old "Min-SPI" layout) still boots the card, because it loads the rest of
  the boot chain from the card's partitions p2-p14, which is why the SD layout keeps them.
- **A QSPI from 4.5 to 4.6.5** boots with its own, older bootloader and our 32.7.6 kernel. NVIDIA updates it on
  that first boot; the new firmware is used from the next reboot.
- The same service also migrates a JetPack ≤ 4.4 QSPI to the new layout (no oem-config needed): the updater has
  no layout check and writes only the QSPI, never the card's partitions.
- **Writing the QSPI** takes up to about 2 minutes (partitions whose content is already the same are skipped). It writes `VER_b` first and `VER` last; the BCT has several copies and
  TegraBoot a backup, but CBoot, U-Boot, the DTB and TOS exist once. A power cut in the middle leaves a board that
  does not boot until it is reflashed over USB recovery from an x86-64 Linux PC (`flash.sh jetson-nano-qspi
  mmcblk0p1`): not permanently bricked, but not something a streamer can fix alone.
- The last official Nano SD image is JetPack 4.6.1 (R32.7.1); NVIDIA has no 32.7.6 SD image (the test board got
  32.7.6 through `apt upgrade`, which rewrote its QSPI on Oct 4).

**Proposal** (§12, D-I2): the image boots on any QSPI (boot partitions on the card), NVIDIA's every-boot service
is **masked** (as on the test board), and a one-shot step of ours does the update in the open:

1. At first boot, before the wizard, `eirl-firmware check` reads `VER` and `VER_b` (read only:
   `l4t_payload_updater_t210 -v`, run with python3, or the raw `VER` block of `/dev/mtd0`). If both say 32.7.6,
   nothing happens: that is the test board's case, and every board's after its first boot with our image.
2. Otherwise the screen (and the web) says that the Jetson's firmware has to be updated once, that it takes
   about 2 minutes and that the power must stay connected, and NVIDIA's own updater writes NVIDIA's own payloads
   from the BSP (`bl_update_payload`, then `xusb_only_payload`). Then `VER` = `VER_b` = 32.7.6 is checked and the
   board reboots by itself. An update that fails or is cut (`VER_b` is written first, `VER` last) is tried
   again at the next boots, at most 3 times; after that it stays `failed` and needs a person (USB recovery).
3. The result is written to the journal and to `/var/lib/eirl-image/firmware.json` (the app can show it).
4. **Test images** (I1-I3) are built with the write disabled at build time (`check` only): the test board's QSPI
   is never written, whatever the check reads.

## 5. First boot

Two oneshot units, each running until its work is done (markers in `/var/lib/eirl-image/`: a step that fails or is
cut by a power loss is retried at the next boot), with the splash on:

- `eirl-firstboot.service`, early (no default dependencies; before the network, avahi, NVIDIA's `nvfb` and the
  app): steps 1, 2 and 4 below, each with its own marker (`grow.done`, `hostname.done`).
- `eirl-firmware.service`, after `sysinit` (udev, modules: NVIDIA's updater needs the partition links and the board
  EEPROM) and after NVIDIA's `nvfb.service` (both use debconf), before the app and the engine: step 3
  (`firmware.done`; an update that fails is tried again at most 3 times, then it stays `failed` for a person).

The steps:

1. `machine-id`: systemd generates it (empty `/etc/machine-id` in the image) and commits it. The app's hardware
   id, and from it the access point's name, come from it.
2. Grow `APP` to the whole card and `resize2fs` online. NVIDIA's `nvresizefs.sh` comes with the oem-config package,
   which the image leaves out, so ours does the same steps: check that p1 is the last partition, move the backup GPT
   header, recreate p1 with the same start (no realignment), name, type, UUID and attributes, tell the kernel
   (`partx -u`), `resize2fs`. `resize2fs` also runs whenever the filesystem is smaller than its partition, so a power
   cut between the table and the filesystem is finished at the next boot.
3. The firmware check (§4).
4. Hostname `eirl-encoder-<xxxx>`, with the same four characters as the Wi-Fi name `EnhancedIRL-XXXX`
   (`eirl-encoderd -print-hardware-id`), so that each new device on a LAN has its own `.local` name
   (§12, D-I5). The wizard's device name renames it later, as today (SETUP.md §2).
5. SSH host keys: NVIDIA's own first-boot script (`nvfb.sh`, `dpkg-reconfigure openssh-server`) makes them per
   device. With `ssh.service` masked nothing listens; the debug image uses them.
6. Marker; the splash goes on until the app is up and the screen shows the wizard (SETUP.md §4). Without a
   screen, the web on `http://eirl-encoder-<xxxx>.local` (or the IP) shows it, with the setup code.

The app generates the rest of what is per device at its own first start, as it already does: `setup-code`,
the access point's password and name (`network.json`), `device.json` (a fresh device, English, wizard to run).

## 6. Per-device values and nothing from the test board

| Value | In the image | Made at |
|---|---|---|
| `/etc/machine-id` (→ hardware id → Wi-Fi name, cloud identity) | empty | systemd, first boot |
| `/var/lib/systemd/random-seed` | absent | systemd, first boot (haveged feeds the pool) |
| SSH host keys | absent | first boot, only with SSH (§7) |
| Hostname | `eirl-encoder` | first boot (§5) |
| `setup-code`, `ap.psk`, `network.json`, `device.json`, `admin.hash`, `cloud.json`, identity | absent | the app (first start, wizard, linking) |
| NetworkManager state, secret key, connections | absent | NetworkManager |
| Journal | empty | — |

Checked on every image (§10.1): no file differs between two builds except where expected, and the mounted
image contains none of the test board's names, networks, addresses, keys or accounts (the patterns of
`tools/public-check.sh`, plus SSH keys, Wi-Fi profiles, `authorized_keys`, `/home/*`, the maintainers' secret
store paths).

**Note** for the cloud: the hardware id derives from `/etc/machine-id`, so **reflashing a card makes a new
device** for EnhancedIRL (it has to be linked again). A hardware id from the module's serial number would
survive a reflash; that is the app's and the cloud's call (§12, D-I8).

## 7. Maintenance access

The product image has no login user and no default password. Options (§12, D-I4):

| | What | For | Against |
|---|---|---|---|
| A | **No SSH** (`openssh-server` not installed) | Smallest surface; nothing to secure; nothing unique to generate | Diagnosing a customer's device = the web, the journal the app shows, or reflashing |
| B | SSH off; switched on from the web (admin session) by pasting a public key, for a dedicated key-only user | Real maintenance when needed, opt-in, no password | App work (an endpoint and a root helper) and a new privileged path to review |
| C | A user with a password chosen in the wizard | Familiar | One more password to choose and lose; password SSH on venue LANs; with sudo it is root |

**Decided (D-I4): A**, with one change that keeps the door open for §7.1: `openssh-server` **is installed but
`ssh.service` is masked**. Nothing listens on port 22, the image has no user, no key and no password, and its host
keys are made per device, never in the image. The reason is that the OTA cannot install Ubuntu packages: without
the package in the image, a future remote support would need an SSH server of our own.

**Test images** (I1-I3) can be built with `--debug-ssh-key <file>`: a key-only `eirl-debug` user and `ssh.service`
unmasked. Such an image is named `…-debug`, never signed and never published; the checks refuse it.

### 7.1 Remote support for linked devices (requested on Oct 14)

The maintainer wants maintenance access to encoders that are linked to EnhancedIRL, always with the user told. It
is not part of the image: the app and the cloud would build it, as a separate initiative. The proposal:

- **Never a permanent key or password** baked into the image or the bundle: one leak would open every device, the
  repository is public, and it is exactly what a customer calls a back door.
- **Level 1, diagnostics over the cloud link, no shell.** While linked, support can ask for a diagnostics bundle
  (state, the journal of our units, versions, the checks) and run a closed list of actions (restart the engine or
  the app, run the checks again). It travels over the link the encoder already opens to the cloud, so nothing
  listens and it works behind NAT and on 4G. Settings and the linking screen say that support can do this; every
  request is logged and listed for the user. It covers most tickets.
- **Level 2, a remote shell, only inside a support window the user opens** on the screen or in the web ("Allow
  EnhancedIRL support for 24 h"), with a visible indicator while it is open and an "End now" button:
  - a tunnel over the same outbound link, like the Cloud OBS desktop tunnel (the backend mints a single-use token),
    to `sshd` listening on localhost only;
  - a dedicated `eirl-support` user, authenticated with **short-lived SSH certificates** signed by a support CA kept
    in the maintainers' secret store (the device trusts the CA's public key; a certificate lasts hours and names the
    device), so no static key ever sits on a device and closing the window or letting the certificate expire is the
    revocation;
  - sudo for that user only while the window is open.
- **Audit and notice**: every session (who, when, how long) in the device's journal and in the web, and explained in
  the privacy policy and the terms before it ships.

## 8. Signing, files and publishing

### 8.1 Image manifest (signed)

A small JSON next to the image, signed with the **same ed25519 release key** as the OTA (one key to trust,
already compiled into every board), with its own `product` so that it can never be mistaken for an OTA
manifest (the board's verifier requires `product: "eirl-encoder"`):

```jsonc
// eirl-encoder-<version>-jetson-nano.img.json (the signature covers its exact bytes)
{ "v": 1, "product": "eirl-encoder-image",
  "board": "jetson-nano-devkit-b01", "l4t": "32.7.6",
  "version": "0.1.1",                       // the image's version = the release it carries (+ "-N" for a rebuild)
  "software": { "version": "0.1.1", "manifestSha256": "…" },   // the OTA manifest installed in the image
  "built": "2026-10-20T10:00:00Z", "commit": "<sha12>",          // the recipe's commit
  "image": { "name": "eirl-encoder-0.1.1-jetson-nano.img.xz", "size": 0, "sha256": "…",
             "rawSize": 0, "rawSha256": "…" },                   // the compressed file and the raw image (what Etcher writes)
  "minCardBytes": 16000000000 }
```

The real manifest is that, pretty-printed in the same field order, with a final newline. The tools, all in the repo
(the same key custody as the OTA: the seed only through `EIRL_RELEASE_KEY_CMD`, never on disk, UPDATES.md §3):

| Command | What |
|---|---|
| `tools/release.sh image --image IMG.img.xz [--raw-size N --raw-sha256 H \| --raw-from-xz] [--packages F] [--sources F] [--min-card-bytes N] [--commit C] [--built T] [--software-manifest F]` | Checks the signed OTA manifest the image carries (default `build/out/release/<VERSION>/manifest.json`), writes the files of §8.2 into `build/out/release/<release>/image/`, signs with the production key and verifies everything, the raw image included (`xz -dc`). The raw values come from the recipe's flags or from the xz stream (the default). `--commit` is the recipe's (default `HEAD`, clean). Test mode: `--local DIR --key-stdin --pubkey B64 --allow-dirty`, a throwaway key |
| `tools/release.sh publish-image <release> [--yes] [--clobber]` | Uploads those files to the **existing** release `v<release>` (§8.3). It never creates a release, never changes which one is latest, and replaces a file only with `--clobber`. It refuses a missing or draft release, a release whose `manifest.json` is another version or is not the one in the image, an image of another release, missing `packages.txt`/`sources.md` (§9.3), and a file over GitHub's 2 GiB. It asks to type the version unless `--yes` |
| `eirl-release image-manifest -image IMG.img.xz -software manifest.json -commit SHA (-raw-size N -raw-sha256 H \| -raw-stdin) [-version V] [-built RFC3339] [-min-card-bytes N] [-out F] [-sha256-out F]` | Writes the manifest. It hashes the `.img.xz` itself and takes `software` from the OTA manifest; `-raw-stdin` hashes `xz -dc IMG \|`. `version` defaults to the software's (`<software>-N` for a rebuild) and the file must be named `eirl-encoder-<version>-jetson-nano.img.xz`. Strict: a known board and its L4T, semver, lowercase sha256, a 12-character commit (a longer one is cut), positive sizes, and a raw size in whole 512-byte sectors that fits `minCardBytes` |
| `eirl-release sign-image -in F.img.json` (seed on stdin) | Signs the exact bytes of a canonical image manifest (as `image-manifest` wrote it). It refuses an OTA manifest, and `eirl-release sign` refuses an image manifest, both before reading the seed |
| `eirl-release verify-image -manifest F.img.json [-image F.img.xz] [-raw-stdin] [-pubkey B64] [-print FIELD]` | The signature with the compiled-in key, then the `.img.xz` (size, sha256) and the raw stream. Exit 0: good; 1: the download does not match, do not flash it; 2: it could not be checked |
| `eirl-release pem` | The release key in PEM form, `release/eirl-release.pem` (a test checks it against `release/eirl-release.pub`) |
| `tools/image-sign-test.sh` | All of the above end to end, with a throwaway key and a stubbed `gh` (about a minute) |

### 8.2 Files of a published image

| File | What |
|---|---|
| `eirl-encoder-<v>-jetson-nano.img.xz` | The image: a raw disk image of the whole card (the card's equivalent of an ISO), cloned to the card as it is by Etcher or `dd`. Compressed: the raw image is ~2.5 GB, the `.img.xz` ~300 MB (GitHub allows 2 GiB per file) |
| `eirl-encoder-<v>-jetson-nano.img.json` + `.sig` | Signed manifest (§8.1) |
| `eirl-encoder-<v>-jetson-nano.img.xz.sha256` | For people who only check a hash |
| `eirl-encoder-<v>-jetson-nano-packages.txt` | Every package with its source package and version (§9.3) |
| `eirl-encoder-<v>-jetson-nano-sources.md` | Where the corresponding source of everything is (§9.3) |

### 8.3 Where

As **assets of the same GitHub release** `v<version>` as the OTA bundle. Never as a separate release marked
*latest*: boards fetch `releases/latest/download/manifest.json`, and a latest release without one answers 404,
which a board reads as "nothing published" — every board would stop updating without a sign.

An image is only rebuilt when something below the bundle changes (L4T-level files, the recipe) or every few
releases; a board flashed with an older image updates itself on its first check.

### 8.4 Flashing and verifying (for the README)

- balenaEtcher: pick the `.img.xz` (it decompresses and verifies the write itself).
- `dd`: `xz -dc eirl-encoder-<v>-jetson-nano.img.xz | sudo dd of=/dev/<card> bs=4M conv=fsync status=progress`.
- Verifying with the repository (Go):

  ```bash
  git clone https://github.com/enhanced-irl/eirl-encoder.git && cd eirl-encoder
  go -C manager build -o "$PWD/eirl-release" ./cmd/eirl-release
  F=~/Downloads/eirl-encoder-<v>-jetson-nano
  ./eirl-release verify-image -manifest $F.img.json -image $F.img.xz
  xz -dc $F.img.xz | ./eirl-release verify-image -manifest $F.img.json -raw-stdin   # optional: the raw image too
  ```

  Exit 0: good. 1: the download does not match; do not flash it. 2: it could not be checked (a missing file).
- Verifying with OpenSSL 3 or later and `release/eirl-release.pem`. Not with LibreSSL, the `openssl` macOS ships: it
  cannot load the key (`brew install openssl@3`, then `$(brew --prefix openssl@3)/bin/openssl`).

  ```bash
  openssl base64 -d -A -in $F.img.json.sig -out $F.img.json.sig.bin
  openssl pkeyutl -verify -pubin -inkey release/eirl-release.pem -rawin -in $F.img.json -sigfile $F.img.json.sig.bin
  # "Signature Verified Successfully"; then the image must have the signed manifest's sha256 (macOS: shasum -a 256):
  grep -q "\"sha256\": \"$(sha256sum $F.img.xz | cut -d' ' -f1)\"" $F.img.json && echo "image OK"
  ```

- `sha256sum -c $F.img.xz.sha256` checks the download only against that unsigned file: it catches a damaged
  download, not a replaced one.
- They are in the README ("Getting started"). `tools/image-sign-test.sh` runs the verifications above.

## 9. Licences

Summary of the research (sources quoted in §9.4). **Verdict: the binary image can be published**, with the list
below.

### 9.1 NVIDIA

R32.7.6 ships under the **NVIDIA Driver License Agreement** (Nov 23, 2023), the same text as in every
`nvidia-l4t-*` package. §1.1(d) allows distributing the software "provided for use with operating systems
distributed under the terms of an OSI-approved open source license … provided that (i) the binary files thereof
are not modified in any way (except for uncompressing of compressed files) and (ii) this Agreement is provided to
each SOFTWARE recipient". Also: NVIDIA hardware only (§2.1), no modified binaries (§2.3), notices kept and any
modified file marked as changed (§2.5), and NVIDIA libraries never linked into GPL/AGPL binaries (§2.9).
The licence has to be **provided**, not accepted; skipping oem-config does not bypass anything (NVIDIA's staff
recommend `l4t_create_default_user.sh` for that). Precedents: NVIDIA's own Nano image, balenaOS for the Nano on
R32.7.6, Qengineering's image.

### 9.2 Ubuntu (Canonical)

Canonical's IP rights policy: "Any redistribution of modified versions of Ubuntu must be approved, certified or
provided by Canonical if you are going to associate it with the Trademarks. Otherwise you must remove and
replace the Trademarks and will need to recompile the source code to create your own binaries. This does not
affect your rights under any open source licence…". Our image is a modified Ubuntu, so:
- no Ubuntu name or logo in the product (§3.5), and the docs say "built from Ubuntu 18.04 packages; not affiliated
  with or endorsed by Canonical";
- the recompile clause does not override the GPL's permission to redistribute binaries with their source (FSF and
  SFC, 2015). For packages under permissive licences the policy's wording remains a residual risk, the same one
  NVIDIA's and other published images carry. Only Canonical can remove it, in writing.

### 9.3 Copyleft obligations

- **GPLv2** (the kernel, U-Boot, our modules, many Ubuntu packages): the corresponding source of the exact
  binaries, or a written offer valid at least 3 years. The "pass on the offer you received" option (§3(c)) is only
  for non-commercial distribution, so not for us.
- **GPLv3, LGPLv3, AGPLv3** (bash, coreutils, tar, gzip and other GNU tools, in the root filesystem and in NVIDIA's
  initrd; `srtla_send` in the release): for a download, a written offer is not one of the options (GPLv3 §6(b) is for
  physical products). The source has to be reachable by directions next to the image, and whoever distributes stays
  responsible for it remaining available (§6(d)), even when it sits on someone else's server.
- What the research listed per release (Oct 14): NVIDIA's licence; `packages.txt`; `sources.md`; NVIDIA's
  `public_sources.tbz2`; our module and DT patches, the kernel config and `build/build.sh`; `srtla` at its pinned
  commit with our patch; SRT 1.5.7; the GPL/LGPL Ubuntu source packages; the recipe commit. Inside the image: the
  `copyright` files, `/usr/share/doc/nvidia-tegra/`, our `NOTICE` and licences, and the sources file with the offer.

**What the image does (Oct 16).**
- In the image, `/usr/share/doc/eirl-encoder-image/`: `NOTICE` (what the image is made of and under which licences;
  built from Ubuntu packages and not Ubuntu; NVIDIA's proprietary parts under NVIDIA's licence, its open-source parts
  under theirs) and `SOURCES.md`, written by `stages/sources.py` from what is installed.
- `SOURCES.md` has the **written offer** (at least 3 years after we last distribute the image; every GPL, LGPL and AGPL
  program; at no more than the cost of providing it; support@enhancedirl.com), and the directions to every source
  (the GPLv3 ones too): the recipe's commit on the public repository; NVIDIA's BSP and `public_sources.tbz2` (URLs and
  sha256) with every `nvidia-l4t-*` package; the programs of NVIDIA's initrd with the Ubuntu source package and version each one
  comes from (NVIDIA records no versions: 42 of its 75 files are byte for byte the image's own; the others are the
  versions Ubuntu had published on the day NVIDIA put it together, Nov 18 2020, which glibc's own version string
  confirms); our modules, DT and the
  RTL8761B firmware; every Ubuntu source package at its exact version; and the release's own notices (BlueALSA with
  libsbc's source in the bundle, librist, SRT, srtla).
- Next to the image: the same `SOURCES.md` as `-sources.md`, and the package list as `-packages.txt`.
- The kernel configuration is in the repository (`build/kernel-4.9.337-tegra.config`, extracted from the BSP's own
  kernel; `build/build.sh` uses it and no longer reads it from a board), and a product image always builds the modules
  and the DT again from its own commit.
- `check.py` checks that the notices and the licences of NVIDIA and of the firmware are there, that `SOURCES.md` names
  the source of every installed package, and the offer, the commit and the release.
- **Availability**: the maintainers keep copies of everything the directions point to (the pinned downloads and every
  Ubuntu source package of each published image, initrd included), outside the repository.

Not done: NVIDIA's `public_sources.tbz2` and the Ubuntu source packages as assets next to the image, which would make
the source available "from the same place" in the plainest sense. It would need `publish-image` to upload more files
(and about 1 GB per image). Until then the directions point to NVIDIA, Launchpad and this repository, and the copies
above cover the obligation to keep it available.

### 9.4 Sources

- NVIDIA licence: `developer.download.nvidia.com/embedded/L4T/r32_Release_v7.6/T210/Tegra_Software_License_Agreement-Tegra-Linux.txt`; release page `developer.nvidia.com/embedded/linux-tegra-r3276`; NVIDIA forum threads 51156, 227095, 233265, 306285.
- Canonical: `ubuntu.com/legal/intellectual-property-policy`; FSF `fsf.org/news/canonical-updated-licensing-terms`; SFC `sfconservancy.org/news/2015/jul/15/ubuntu-ip-policy/`.
- GPL: `gnu.org/licenses/gpl-faq.html`; the Copyleft Guide (`copyleft.org/guide`).

## 10. Tests

### 10.1 I1, on the mounted image (no board)

- Packages = `packages.lock`; none of the `base/purge-*.txt` patterns installed; every `nvidia-l4t-*` on hold.
- Enabled and masked units = the expected lists; `default.target` = multi-user; no oem-config.
- Size of the raw and compressed image; free space in `APP`.
- No secret and nothing of the test board (§6).
- The installed release verifies its signature, and its files match the bundle.
- `base/check-engine.sh`-like checks that can run offline (libraries resolve with `ldd` in the chroot).
- Two builds from the same inputs give the same package list and the same files except the expected ones.

`stages/check.py` runs them on every build (about 80 checks) and writes a report next to the image
(`<name>.report/`: `check.json`, `packages.txt`, `units.txt`, the partition layout and the raw image's sha256).
A failing check fails the build.

**Results (Oct 14)**: release 0.1.1 (production-signed, from GitHub), a `-test` and a `-debug` image, **0 failed**.

| | Image | Test board |
|---|---|---|
| Packages | 493 | 1310 |
| Root filesystem | 1.18 GB | 4.4 GB |
| Raw image · `.img.xz` | 2.48 GB · 297 MB | — |
| Build | ~10 min on the Mac (apt in the chroot is most of it) | — |

What the first builds taught (each one fixed in the recipe):
- The BSP ships `rootfs/` empty, and `flash.sh` then writes `nv_boot_control.conf` as a *file* called `etc`.
- `nv-apply-debs.sh` fails if `tools/` has no `.deb` left (hence `jetson-gpio-common` stays, §3.3).
- `dpkg-query -W 'nvidia-l4t-*'` also lists packages dpkg knows but did not install: hold only the installed ones.
- `libcuda.so.1` is a link `ldconfig` makes; without the tegra paths at build time it does not exist (§3.3).
- `adwaita-icon-theme` pulls `ubuntu-mono` (Ubuntu-branded icons) unless `adwaita-icon-theme-full` is asked for.
- The checks caught the comment of the debug key (a person's name) in `authorized_keys`: only the key goes in now.
- Python 3.6 in the containers writes ASCII unless told `LC_ALL=C.UTF-8`.

Not yet exercised: the signature of the image (§8.1, the app session's tool, tested on its own), the reproducibility
of two builds (the second item above), and everything that only runs on a board (§10.2, §10.3).

### 10.2 I2, first boot and uniqueness

Boot the image from scratch twice (two flashes of the same image): different machine-id, hardware id, Wi-Fi name,
setup code, AP password, hostname and (with SSH) host keys; `APP` grown; the firmware check reports 32.7.6 and
writes nothing; no failed units.

### 10.3 I3, with the maintainer, on another microSD (agreed slot)

The test board's card is kept untouched; the maintainer swaps the cards by hand.
1. Boot: splash, touch screen, the wizard on the screen and on the web.
2. Sources: board HDMI and a USB capture card.
3. A short stream to the test destination (the key never in the clear: `streamid=***`).
4. OTA to the next release (or a test release), a reboot, and back to the original card.
5. Measurements against the test board (§11.2).

### 10.4 Results of I2 and I3 (Oct 14-15, debug image of 0.1.1)

The maintainer chose to use the test board's own card: it was backed up whole first (raw, compressed, checked
against the card's hash), and a restore that writes only the card's used ranges (GPT, NVIDIA's partitions and the
used blocks of `APP`, 6.1 of 31.9 GB) was rehearsed on a dirtied copy. 🪤 On macOS, not even root can open
`/dev/rdiskN` from an app without Full Disk Access: write the card from Terminal.

| | Result |
|---|---|
| First boot | 16 s to `multi-user` (the grow 9 s: `APP` and its ext4 grown online to the whole 32 GB card); splash, then the wizard on the touch screen |
| Per-device values | New `machine-id`, host name `eirl-encoder-<xxxx>` and the matching Wi-Fi name, SSH host keys (NVIDIA's `nvfb`), setup code and AP password |
| Firmware | QSPI read, VER and VER_b = 32.7.6; nothing written (mode `check`) |
| Nothing from the test board | No login user but `eirl-debug`, `root` locked, no NVIDIA questions, the QSPI service masked, ModemManager off |
| Sources and stream | Board HDMI at 1080p50, a USB capture card and a UVC camera; a stream over SRTLA to the test relay |
| OTA | 0.1.1 → 0.3.0 from the touch screen in 35 s (signature, backup, `install.sh`, health check) |
| Later boots | 9 s; the first-boot units are skipped (markers), the identity stays |

Found and fixed for the next image (§3.5): `motd-news` failed at every boot (the system was `degraded`) and the apt
timers ran; the group `debug` was missing; the first boot's clock started in 2023. Found and **not** the image's (the
board's own system does the same): saving a destination restarts the engine and with it the touch screen, which is
black for about 12 s (`Failed to set plane` until the engine takes the screen again). The fan stays at PWM 0 until
about 51 °C under the kernel's control (D-I7): since Oct 15 our own daemon drives it ([`FAN.md`](FAN.md)). The test
board's old system also ran `jetson_clocks` at boot (its `automagic-fan` had `MAX_PERF=1`: CPU, GPU and memory at
their highest clocks); the image keeps NVIDIA's default frequency scaling, and the test went well with it.

At a cold start the touch screen went off twice before the splash: neither CBoot nor the kernel could read its EDID
(640×480, then the kernel's built-in 720p EDID at 1.43 s), and at 2.15-2.92 s the panel dropped its hot plug and came
back at 1024×600. Both come from the panel, not from the image: nothing of ours runs that early ([`SPLASH.md`](SPLASH.md) §1).

### 10.5 I4, publishing (with the maintainer's OK)

Signed image and its files (§8.2) on the release; the recipe and this document already on the public repository
(the public sync); flashing and verification instructions in the README. In order:

1. The recipe in `main`; the release it carries already published (its `manifest.json`, `.sig` and bundle, downloaded
   from GitHub Releases).
2. `tools/image/build.sh --release DIR` on a clean `main`: the product image and its report. A debug image of the
   same commit for the board test (`--debug-ssh-key`).
3. A boot from scratch on a card that is then written again.
4. `tools/release.sh image …`: the command `build.sh` prints at the end, with `EIRL_RELEASE_KEY_CMD` (the seed only
   through that pipe, UPDATES.md §3). It writes `build/out/release/<release>/image/`.
5. The public sync of exactly that commit (`tools/public-sync.sh --ref <commit>`, UPDATES.md §5), with the
   maintainer's OK: `SOURCES.md` names the public commit "Sync with internal commit <commit>".
6. `tools/release.sh publish-image <release>`, with the maintainer's OK.

### 10.6 Results of I4 on the test board (Oct 16, debug image of 0.7.0)

The debug image of `main` at the commit of MR !113 (the same recipe as the product image, with the debug user, ssh and
a firmware check that never writes), written to the test board's card (the maintainer chose not to back it up: it ran
the image already). It is the second boot from scratch of §10.2.

| | Result |
|---|---|
| Write | 2.5 GB in 4 min 48 s (8.7 MB/s), read back: the raw sha256 of the build |
| First boot | 16.2 s to `multi-user` (the grow 8.5 s, ext4 grown online to 30 GB); the clock started at the build time and NTP set it ~45 s later |
| Per-device values | All new and different from the first card's (Oct 14): host name `eirl-encoder-74de`, machine-id, hardware id, SSH host keys, setup code, AP password |
| Firmware | VER and VER_b = 32.7.6, nothing written |
| Software | Release 0.7.0; `eirl-fan` running (fan stopped at 33 °C, below the curve); the TC358743 at 1080p50; a USB capture card and a UVC camera; the maintainer did the wizard on the touch screen (without the cloud) |
| Bluetooth, cold | `btrtl` loads `rtl8761bu_config.bin` and `rtl8761bu_fw.bin` for the UB500 at 4 s and the controller reports LMP subversion 0xd922 (patched); `bluetooth.service` active and not enabled (pulled in by `eirl-bluealsa`, which runs as `bluealsa`); Bluetooth off in the app by default, switched on from the screen; the radio receives (inquiry and LE adverts in a 7 s scan) |
| Second boot | 5.6 s; the same identity; the first-boot and firmware steps do not run again; `running` |

Found and fixed (in the recipe from the next image): NVIDIA's Argus camera service `nvargus-daemon` was enabled (off
on the test board since Oct 8): it probed every video device at boot, with a kernel warning (`v4l_enum_fmt`: a USB
capture card's empty format) and SCF errors, and kept 40 MB. Now off; checked on the board by hand, with a reboot.

## 11. From `base/` to the image

### 11.1 What is applied as it is and what changes

| `base/` | In the image |
|---|---|
| `keep.txt` | The install list, **minus** the development group (D4: ffmpeg, build-essential, git, pkg-config, cmake, `-dev`, `tcl`, debhelper), **minus** `python3-gi`, `python3-cairo` and the `gir1.2-*` (only the retired Python engine used them; `eirl-enginectl` and `eirl-hdmi-init.sh` use the standard library), **minus** `rsyslog` and `ubuntu-minimal` if nothing else needs them, `bluez` out (unused; **back in on Oct 15** for Bluetooth audio, BLUETOOTH.md §3.7: installed, not enabled, pulled in by `eirl-bluealsa.service`). `modemmanager` stays (4G modems for SRTLA) |
| `purge-*.txt` | Nothing to purge (never installed). Used as a **deny list** in the checks |
| `units-b1a.txt` | Masked where the unit exists (most of them do not exist in this rootfs) |
| `units-b2.txt` | rsyslog absent or masked; `90-eirl-nosyslog.conf` |
| `units-b1b-alsa.txt` | `alsa-restore` masked |
| `units-qspi.txt` | `nv-l4t-bootloader-config` masked; the update is ours (§4) |
| B1 group B (nvphs, nvs-service, nvfb) | As on the board (not decided yet) |
| B4, B5 | Whatever is in main when the image is built |
| D5 | USB device mode and `serial-getty@ttyGS0` masked |
| `check-engine.sh` | Its offline part in the image checks; the Python checks go (Python engine retired) |

### 11.2 What to measure against the test board

Same sources and conditions as B0 (BASE.md §10): output off, preview off.

| | Test board (Oct 14, read only) | Image |
|---|---|---|
| Packages | 1310 | 493 (measured in the image, I1) |
| Disk used | 4.4 GB (with the development tools and kernel headers) | 1.18 GB (I1) |
| Boot | 2.2 s kernel + 8.2 s user space; multiview at ~13 s | `systemd-analyze`, `critical-chain eirl-engine`, splash, app and multiview times |
| Available RAM (engine running, no stream) | ~2.6 GB | `MemAvailable` with the same sources |
| Running services | 25 | count and list |
| SD writes | 17-24 KB/s | `tools/base-measure.py` |

## 12. Decisions for the maintainer (phase 0)

Each with the recommendation first. The maintainer's answer goes in the last column.

| # | Question | Options | Recommendation and why | Maintainer |
|---|---|---|---|---|
| D-I1 | Can we publish a binary image? | (a) publish it with the compliance list of §9; (b) publish only the recipe; (c) (a), but only after a written answer from Canonical (and NVIDIA) | **(a)**, with the recipe public too. NVIDIA's licence allows exactly this (unmodified binaries + the licence provided), and there are precedents; the Ubuntu name and logo come out; the GPL work is mechanical. (b) would make the product unusable for a streamer. (c) can run in parallel if the residual Canonical risk worries the maintainer | **(a)**: a ready-to-clone card image |
| D-I2 | The board's firmware (QSPI) | (A) NVIDIA's every-boot service masked; our one-shot update at first boot, shown on screen, only if the QSPI is not 32.7.6; (B) keep NVIDIA's service (the same write, silent, checked at every boot); (C) never write: boot on whatever firmware is there and warn | **(A)**. The same write NVIDIA's own image does, but once, visible ("keep the power connected"), logged, and with the test images unable to write at all. (B) hides a 2-minute risky write behind a black screen; (C) leaves boards on an unsupported bootloader/kernel combination | **(A)**. Oct 16: kept for the first published image (0.7.0), knowing that the write has not run on a board with older firmware yet and that the screen does not say it is updating |
| D-I3 | Build host | (a) the maintainer's Mac with Docker (arm64 native + one `linux/amd64` step); (b) an x86-64 Linux machine; (c) CI | **(a)**: tested on Oct 14 (§3.1), fast, no new machine, and the signing key stays where it already is (through a pipe). The recipe also runs on Linux with Docker; CI later if wanted (it could never sign) | **(a)**; later, built by a GitHub workflow (§3.1) |
| D-I4 | Maintenance access | (A) no SSH; (B) SSH off, switched on from the web with a key; (C) a user with a password from the wizard | **(A)** in the product image, with `--debug-ssh-key` for test images only (§7). Nothing to secure and nothing to leak. (B) as a later app feature if support needs it | **(A)**, with `openssh-server` installed and masked; remote support for linked devices analysed in §7.1 |
| D-I5 | Card, host name, files, signature | Card: minimum 8 / 16 / 32 GB. Host name: `eirl-encoder` / `eirl-encoder-<xxxx>`. Files: §8.2. Signature: the release key with a separate image manifest (§8.1), or another key | Card **16 GB minimum, 32 GB high-endurance recommended** (the image fits in 8 GB, but the persistent journal, the OTA backups and future releases need room, and 8 GB cards are slow and rare). Host name **`eirl-encoder-<xxxx>`** (same suffix as the Wi-Fi name: two new devices on one LAN never clash). Files as in §8.2, **as assets of the release they carry** (never a separate *latest* release). **Same release key**, separate `product`: one key to trust and to guard | **As recommended**: 16 GB, `eirl-encoder-<xxxx>`, same release key, assets of the same release |
| D-I6 | What of `base/` goes in | §11.1 | **As in §11.1**: `keep.txt` without the development tools (D4), without `python3-gi`/`python3-cairo`/`gir1.2-*` (only the retired Python engine used them) and without `bluez`; `modemmanager` stays; the `units-*.txt` masks as on the board; USB device mode out (D5). Measured against the board as in §11.2 | **As in §11.1**. Oct 15: `bluez` back in, for Bluetooth audio (BLUETOOTH.md §3.7) |
| D-I7 | Fan | (a) the same fan daemon as the test board (`automagic-fan`, installed by hand there; licence to check); (b) the kernel's own thermal control, nothing installed; (c) our own small controller later | **(a)** for I1-I3, so the image behaves like the validated board; D7 of BASE.md (measure first) decides (b) or (c) with the I3 measurements | **(c)**: our own controller, with thermal profiles the user picks in the interface, and a mode like a professional camera's (the fan well above what is needed while not streaming, quieter on air). Until it exists, (b) |
| D-I8 | Hardware id after a reflash | (a) keep it from `/etc/machine-id`: a reflashed card is a new device for EnhancedIRL; (b) derive it from the module's serial number: it survives a reflash | **(a)** for the first image (no change; relinking takes a minute). (b) is the app's and the cloud's decision, not the image's | **(a)** |
| D-I9 | Software in the first image | 0.1.0, or the next release | **The next release** (0.1.1): 0.1.0 cannot bring up a new device (§2). I1 works with local test builds meanwhile | **The next release** |

## 13. Coordination

| Who | What |
|---|---|
| Image session (owner) | This document, `tools/image/`, the kernel layer, first boot, the firmware step, the checks |
| App session | Done on Oct 14: a fresh device's needs in the bundle (`/etc/default/eirl-engine`, `srt-live-transmit` + `libsrt`, the EDIDs, `OUTPUT=off` by default), `install.sh` image mode, `eirl-encoderd -print-hardware-id`, the public repository sync (`tools/public-sync.sh`), the image's signature and publication (§8.1), release 0.1.1. That session is closed: changes to the app, the installer or the release tools now go through the maintainer |
| Cloud session | D-I8 (hardware id after a reflash); with the app session, remote support (§7.1) when the maintainer starts it |
| Image session (step 1, Oct 15) | The fan controller (D-I7, [`FAN.md`](FAN.md)): the root daemon `eirl-fan` with one curve, in the package (it arrives by OTA). Its profiles and the "camera" mode in Settings and the web: later |
| Maintainer | The decisions in §12, the card swaps and the slots of I3, every publication |

## 14. Risks

- **Power cut while the QSPI is written** (§4): only on boards with older firmware, once; recovery needs an x86-64
  PC and a USB cable.
- **Canonical's policy** for permissively licensed packages (§9.2): residual; only removable in writing.
- **No A/B partitions**: as with the OTA (UPDATES.md §10). A card that dies is reflashed.
- **L4T is frozen** at R32.7.6 (the last R32): no security updates of the base system come with the OTA.
