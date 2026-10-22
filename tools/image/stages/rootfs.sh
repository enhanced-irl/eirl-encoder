#!/bin/bash
# stages/rootfs.sh — runs as root in the linux/arm64 work container (tools/image/Dockerfile): the image's root
# filesystem in /work/rootfs, in a native arm64 chroot (IMAGE.md §3.2-§3.6). It is the frozen base (the packages of
# tools/image/base.lock exactly, the BSP, the DTB and extlinux, the configuration done once), then the release's
# bundle with its system layer (UPDATES.md §12: our modules, firmware, the packages added since, system files and the
# units' state), applied by the same code as on a board. Inputs, read only:
#   /in/dl/<BSP> /in/dl/<ubuntu-base>   pinned downloads          /in/repo     this repository
#   /in/kernel/<DTB>                    build/build.sh's output   /in/release  a verified release (bundle + manifest)
#   /in/base-debs/*.deb                 optional: the base's .deb files (the maintainers' copies, IMAGE.md §3.2)
#   /in/debug.pub                       only for a debug image (--debug-ssh-key)
# Environment: BSP_FILE UBUNTU_BASE_FILE KERNEL_RELEASE IMAGE_VERSION FIRMWARE_MODE (check|update) BUNDLE DEBUG (0|1)
#              IMAGE_NAME IMAGE_COMMIT (the recipe's commit, for SOURCES.md)
set -euo pipefail
: "${BSP_FILE:?}" "${UBUNTU_BASE_FILE:?}" "${KERNEL_RELEASE:?}" "${IMAGE_VERSION:?}" "${FIRMWARE_MODE:?}" "${BUNDLE:?}"
: "${IMAGE_NAME:?}" "${IMAGE_COMMIT:?}"
DEBUG=${DEBUG:-0}
R=/work/rootfs
L=/work/bsp/Linux_for_Tegra
IMG=/in/repo/tools/image
DTB=tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb
step() { echo "== rootfs: $*"; }

mounted=""
umount_all() { for m in $mounted; do umount -l "$m" 2>/dev/null || true; done; mounted=""; }
trap umount_all EXIT
mount_all() {
  mount -t proc proc "$R/proc"; mounted="$R/proc $mounted"
  mount --bind /sys "$R/sys"; mounted="$R/sys $mounted"
  mount --bind /dev "$R/dev"; mounted="$R/dev $mounted"
  mount --bind /dev/pts "$R/dev/pts"; mounted="$R/dev/pts $mounted"
}
in_root() { chroot "$R" /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/root LC_ALL=C DEBIAN_FRONTEND=noninteractive "$@"; }

step "Ubuntu base and the BSP"
rm -rf "$R" /work/bsp && mkdir -p "$R" /work/bsp
tar --numeric-owner -xpzf "/in/dl/$UBUNTU_BASE_FILE" -C "$R"
tar -C /work/bsp -xjf "/in/dl/$BSP_FILE"
cp /etc/resolv.conf "$R/etc/resolv.conf"                      # build only: removed at the end
printf '#!/bin/sh\n# image build: no service starts in the chroot\nexit 101\n' > "$R/usr/sbin/policy-rc.d"
chmod 0755 "$R/usr/sbin/policy-rc.d"
cat > "$R/etc/apt/sources.list" <<'EOF'
deb http://ports.ubuntu.com/ubuntu-ports bionic main universe
deb http://ports.ubuntu.com/ubuntu-ports bionic-updates main universe
deb http://ports.ubuntu.com/ubuntu-ports bionic-security main universe
EOF
mount_all

# The frozen base (IMAGE.md §3.2), exactly: every Ubuntu package of base.lock at its version (NVIDIA's come with the
# BSP below). Nothing newer: a newer version goes through the system layer, so that the boards in the field get it too
base_pkgs() { sed -E 's/#.*//' "$IMG/base.lock" | awk 'NF == 2 && $1 !~ /^(nvidia-l4t-|jetson-gpio)/ {print $1 "=" $2}'; }
if ls /in/base-debs/*.deb >/dev/null 2>&1; then
  step "Ubuntu packages of the base: $(base_pkgs | wc -l), from the maintainers' copies (/in/base-debs)"
  rm -rf "$R/tmp/base-debs" && mkdir -p "$R/tmp/base-debs" && cp /in/base-debs/*.deb "$R/tmp/base-debs/"
  in_root /bin/sh -c 'apt-get -y -q install --no-install-recommends /tmp/base-debs/*.deb'
  rm -rf "$R/tmp/base-debs"
else
  step "Ubuntu packages of the base: $(base_pkgs | wc -l), at their versions in base.lock"
  in_root apt-get update -q
  # shellcheck disable=SC2046
  in_root apt-get -y -q install --no-install-recommends $(base_pkgs)
fi
# What was asked for is "manual", the rest "auto", as before the base was frozen (apt's own bookkeeping)
# shellcheck disable=SC2046
in_root apt-mark auto $(base_pkgs | cut -d= -f1) >/dev/null
# shellcheck disable=SC2046
in_root apt-mark manual $(sed -E 's/#.*//' "$IMG/packages.txt" | awk 'NF {print $1}') >/dev/null

step "NVIDIA BSP: apply_binaries.sh, without the packages the encoder does not use"
for d in weston graphics-demos oem-config kernel-headers jetson-io gputools; do
  rm -f "$L"/nv_tegra/l4t_deb_packages/nvidia-l4t-"$d"_*.deb "$L"/kernel/nvidia-l4t-"$d"_*.deb   # the kernel debs live in kernel/
done
# The Python GPIO libraries (one needs python2) go; jetson-gpio-common (udev rules, the gpio group) stays:
# nv-apply-debs.sh copies tools/*.deb and fails if there is none
rm -f "$L"/tools/python-jetson-gpio_*.deb "$L"/tools/python3-jetson-gpio_*.deb
# nv-apply-debs.sh refuses to run without qemu-aarch64-static on the build machine, even on arm64, where it is never run
[ -e /usr/bin/qemu-aarch64-static ] || { printf '#!/bin/sh\nexit 1\n' > /usr/bin/qemu-aarch64-static; chmod 0755 /usr/bin/qemu-aarch64-static; }
umount_all                                                      # apply_binaries mounts what it needs itself
(cd "$L" && ./apply_binaries.sh -r "$R")
rm -f "$R/usr/bin/qemu-aarch64-static"
mount_all
# NVIDIA's libraries (libcuda, libnvbuf_utils…) are found through /etc/ld.so.conf.d/nvidia-tegra.conf, which
# NVIDIA's nvfb-early.sh rewrites at the first boot and then runs ldconfig. Our own file (NVIDIA's keeps its
# notice: the licence, §2.5) gives the same paths now, so they resolve from the first process on
printf '# eirl-encoder image: the NVIDIA library paths (as nvfb-early.sh sets them at the first boot)\n/usr/lib/aarch64-linux-gnu/tegra\n/usr/lib/aarch64-linux-gnu/tegra-egl\n' > "$R/etc/ld.so.conf.d/00-eirl-tegra.conf"
in_root ldconfig

step "the B01 DTB with the TC358743, extlinux (frozen with the base: the system layer never writes /boot)"
# Our modules and firmware are the system layer's (UPDATES.md §12), installed with the bundle below
install -m 0644 "/in/kernel/$DTB" "$R/boot/$DTB"
# Our FDT in "primary" too: the nvidia-l4t-kernel postinst forces DEFAULT primary (IMAGE.md §3.3).
# tc358743 is the DEFAULT the bundle's splash copies into its own entry.
APPEND='${cbootargs} quiet root=/dev/mmcblk0p1 rw rootwait rootfstype=ext4 console=ttyS0,115200n8 console=tty0 fbcon=map:0 net.ifnames=0'
cat > "$R/boot/extlinux/extlinux.conf" <<EOF
TIMEOUT 10
DEFAULT tc358743
MENU TITLE L4T boot options

LABEL primary
      MENU LABEL primary kernel (B01 DT with the TC358743)
      LINUX /boot/Image
      FDT /boot/$DTB
      INITRD /boot/initrd
      APPEND $APPEND

LABEL tc358743
      MENU LABEL TC358743 HDMI-in (CAM1)
      LINUX /boot/Image
      FDT /boot/$DTB
      INITRD /boot/initrd
      APPEND $APPEND
EOF

step "the encoder software and its system layer: $BUNDLE (EIRL_IMAGE=1 install.sh)"
rm -rf "$R/tmp/eirl-bundle" && mkdir -p "$R/tmp/eirl-bundle"
tar -xzf "/in/release/$BUNDLE" -C "$R/tmp/eirl-bundle"
in_root /bin/bash -c 'cd /tmp/eirl-bundle && EIRL_IMAGE=1 ./install.sh'
rm -rf "$R/tmp/eirl-bundle"

step "system configuration"
# After apply_binaries: nvidia-l4t-init's postinst rewrites fstab, hosts and hostname
echo eirl-encoder > "$R/etc/hostname"
printf '127.0.0.1\tlocalhost\n127.0.1.1\teirl-encoder\n\n::1\tip6-localhost ip6-loopback\nff02::1\tip6-allnodes\nff02::2\tip6-allrouters\n' > "$R/etc/hosts"
printf '# <file system> <mount point> <type> <options> <dump> <pass>\n/dev/root / ext4 defaults 0 1\n' > "$R/etc/fstab"
echo 'LANG=C.UTF-8' > "$R/etc/default/locale"
ln -sfn /usr/share/zoneinfo/Etc/UTC "$R/etc/localtime"; echo Etc/UTC > "$R/etc/timezone"
# No oem-config and no user: what l4t_create_default_user.sh does to skip it, without its useradd
ln -sfn /lib/systemd/system/multi-user.target "$R/etc/systemd/system/default.target"
mkdir -p "$R/etc/nv" && touch "$R/etc/nv/nvautoconfig"
# The image's own files (its NOTICE): only what is tracked there (no editor or Finder leftovers from the build machine).
# The system files a board needs too (the first boot, the journal, NetworkManager…) are the system layer's
(cd "$IMG/rootfs" && find . -type f ! -name '.DS_Store' ! -name '*~' ! -name '*.swp' ! -name '._*' ! -path '*/__pycache__/*') | while read -r f; do
  m=0644; [ -x "$IMG/rootfs/$f" ] && m=0755
  install -D -m "$m" -o root -g root "$IMG/rootfs/$f" "$R/${f#./}"
done
mkdir -p "$R/var/log/journal"
# What flash.sh writes for this board; the firmware check reads it (IMAGE.md §4)
install -m 0644 /work/out/bootparts/nv_boot_control.conf "$R/etc/nv_boot_control.conf"
echo "$FIRMWARE_MODE" > "$R/usr/lib/eirl-image/firmware-mode"
# What the card was flashed with (the system says the installed release since UPDATES.md §12.12: /etc/os-release)
echo "$IMAGE_VERSION" > "$R/usr/lib/eirl-image/flashed"

# The units' state is the system layer's (system/units.txt), except sshd: masked in a product image, on in a debug one
mask_unit() {
  local u=$1
  if find "$R/etc/systemd/system" "$R/lib/systemd/system" -name "$u" 2>/dev/null | grep -q .; then
    find "$R/etc/systemd/system" -name "$u" -path '*.wants/*' -delete
    rm -f "$R/etc/systemd/system/$u"
    ln -s /dev/null "$R/etc/systemd/system/$u"
    echo "   masked $u"
  fi
}
if [ "$DEBUG" = 1 ]; then
  step "DEBUG image: key-only eirl-debug user, ssh on"
  in_root useradd --create-home --shell /bin/bash --groups sudo,video,audio,systemd-journal eirl-debug
  in_root passwd -l eirl-debug
  install -d -m 0700 -o "$(chroot "$R" id -u eirl-debug)" -g "$(chroot "$R" id -g eirl-debug)" "$R/home/eirl-debug/.ssh"
  # The key without its comment (a comment usually names a person or a machine)
  awk 'NF >= 2 && $1 ~ /^(ssh-|ecdsa-|sk-)/ {print $1, $2}' /in/debug.pub > "$R/home/eirl-debug/.ssh/authorized_keys"
  [ -s "$R/home/eirl-debug/.ssh/authorized_keys" ] || { echo "rootfs: no usable key in the debug key file" >&2; exit 1; }
  chown "$(chroot "$R" id -u eirl-debug):$(chroot "$R" id -g eirl-debug)" "$R/home/eirl-debug/.ssh/authorized_keys"
  chmod 0600 "$R/home/eirl-debug/.ssh/authorized_keys"
  echo 'eirl-debug ALL=(ALL) NOPASSWD: ALL' > "$R/etc/sudoers.d/90-eirl-debug"; chmod 0440 "$R/etc/sudoers.d/90-eirl-debug"
  printf '\n# eirl-encoder debug image: keys only\nPasswordAuthentication no\nPermitRootLogin no\n' >> "$R/etc/ssh/sshd_config"
else
  mask_unit ssh.service; mask_unit ssh.socket
fi
in_root passwd -l root >/dev/null
# Every nvidia-l4t-* on hold, as on the board: never an apt upgrade of L4T (it rewrote a QSPI on Oct 4)
in_root /bin/sh -c "dpkg-query -W -f '\${db:Status-Abbrev} \${Package}\n' 'nvidia-l4t-*' | awk '\$1 ~ /^.i/ {print \$2}' | xargs apt-mark hold" >/dev/null

step "branding (Canonical's IP policy: a modified Ubuntu without its trademarks, IMAGE.md §9.2)"
# The system's name and version (os-release, lsb-release, issue) are the system layer's: here only what is done once
[ -L "$R/etc/os-release" ] || { rm -f "$R/etc/os-release"; ln -s ../usr/lib/os-release "$R/etc/os-release"; }
rm -f "$R"/etc/update-motd.d/* "$R/etc/legal"

step "notices and sources (IMAGE.md §9.3)"
# NOTICE comes with the image's own files (tools/image/rootfs); SOURCES.md is made from what is installed now, and
# check.py publishes it next to the image
python3 -I "$IMG/stages/sources.py" --rootfs "$R" --repo /in/repo --release /in/release --version "$IMAGE_VERSION" \
  --name "$IMAGE_NAME" --commit "$IMAGE_COMMIT" --out "$R/usr/share/doc/eirl-encoder-image/SOURCES.md"

step "cleaning (IMAGE.md §3.6)"
in_root apt-get clean
rm -rf "$R"/var/lib/apt/lists/* "$R"/var/cache/apt/*.bin "$R"/var/cache/debconf/*-old
rm -rf "$R"/tmp/* "$R"/var/tmp/* "$R"/var/log/*.log "$R"/var/log/apt/* "$R"/var/log/journal/* "$R"/root/.bash_history
find "$R/var/log" -type f \( -name '*.gz' -o -name '*.[0-9]' \) -delete
: > "$R/var/log/wtmp"; : > "$R/var/log/btmp" 2>/dev/null || true; : > "$R/var/log/lastlog"
: > "$R/etc/machine-id"; rm -f "$R/var/lib/dbus/machine-id"; ln -s /etc/machine-id "$R/var/lib/dbus/machine-id"
rm -f "$R/var/lib/systemd/random-seed" "$R"/etc/ssh/ssh_host_*
rm -rf "$R"/var/lib/NetworkManager/* "$R"/etc/NetworkManager/system-connections/*
# /etc/eirl-encoder keeps only install.sh's engine.env of a new device (the output off): everything per device
# (setup code, the access point's name and password, device.json, the password, the cloud link) is the app's,
# at its first start, the wizard and linking
for f in setup-code ap.psk network.json device.json admin.hash cloud.json identity media-api.key cameras.json slots.json; do
  rm -f "$R/etc/eirl-encoder/$f"
done
rm -f "$R"/boot/extlinux/extlinux.conf.bak-* "$R/boot/extlinux/extlinux.conf.nv-update-extlinux-backup"
rm -f "$R/usr/sbin/policy-rc.d"
# No RTC battery: the first boot's clock would start at systemd's build date (2023) until NTP answers, and TLS (the OTA,
# the cloud) would reject today's certificates. timesyncd moves the clock forward to this file's time when it starts:
# the image's build time. After the first sync, timesyncd keeps it at the last known time itself. timesyncd runs with
# DynamicUser=: its state is in /var/lib/private (0700), linked from /var/lib/systemd/timesync, and systemd gives the
# tree to the unit's user when it starts
rm -rf "$R/var/lib/systemd/timesync"
install -d -m 0700 "$R/var/lib/private"
install -d -m 0755 "$R/var/lib/private/systemd" "$R/var/lib/private/systemd/timesync"
ln -s ../private/systemd/timesync "$R/var/lib/systemd/timesync"
touch "$R/var/lib/private/systemd/timesync/clock"
umount_all
rm -f "$R/etc/resolv.conf" && ln -s ../run/systemd/resolve/stub-resolv.conf "$R/etc/resolv.conf"
du -sh --apparent-size "$R" | sed 's/^/== rootfs: size /'
