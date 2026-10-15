#!/bin/bash
# stages/rootfs.sh — runs as root in the linux/arm64 work container (tools/image/Dockerfile): the image's root
# filesystem in /work/rootfs, in a native arm64 chroot (IMAGE.md §3.2-§3.6). Inputs, read only:
#   /in/dl/<BSP> /in/dl/<ubuntu-base>   pinned downloads          /in/repo     this repository
#   /in/kernel/{tc358743.ko,<DTB>}      build/build.sh's output   /in/release  a verified release (bundle + manifest)
#   /in/debug.pub                       only for a debug image (--debug-ssh-key)
# Environment: BSP_FILE UBUNTU_BASE_FILE KERNEL_RELEASE IMAGE_VERSION FIRMWARE_MODE (check|update) BUNDLE DEBUG (0|1)
set -euo pipefail
: "${BSP_FILE:?}" "${UBUNTU_BASE_FILE:?}" "${KERNEL_RELEASE:?}" "${IMAGE_VERSION:?}" "${FIRMWARE_MODE:?}" "${BUNDLE:?}"
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

step "Ubuntu packages ($(grep -cvE '^[[:space:]]*(#|$)' "$IMG/packages.txt") requested)"
in_root apt-get update -q
in_root apt-get -y -q upgrade
# shellcheck disable=SC2046
in_root apt-get -y -q install --no-install-recommends $(sed -E 's/#.*//' "$IMG/packages.txt" | awk 'NF {print $1}')

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

step "our kernel bits: tc358743.ko, the B01 DTB with the TC358743, extlinux"
install -D -m 0644 /in/kernel/tc358743.ko "$R/lib/modules/$KERNEL_RELEASE/extra/tc358743.ko"
# The cellular modem modules (MODEMS.md §4): updates/ is searched before the kernel's own, so our option.ko
# (with the Quectel IDs) wins over the stock one
for m in /in/kernel/modules/*.ko; do
  install -D -m 0644 "$m" "$R/lib/modules/$KERNEL_RELEASE/updates/eirl-modems/$(basename "$m")"
done
# Bluetooth (BLUETOOTH.md §3.8): btusb and btrtl with the Realtek RTL8761B(U) (TP-Link UB500 and most current dongles),
# also from updates/, and its firmware, which Ubuntu 18.04's linux-firmware lacks (with its licence)
for m in /in/kernel/modules-bt/*.ko; do
  install -D -m 0644 "$m" "$R/lib/modules/$KERNEL_RELEASE/updates/eirl-bluetooth/$(basename "$m")"
done
for f in /in/kernel/firmware/rtl_bt/*; do
  install -D -m 0644 "$f" "$R/lib/firmware/rtl_bt/$(basename "$f")"
done
in_root depmod -a "$KERNEL_RELEASE"
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

step "the encoder software: $BUNDLE (EIRL_IMAGE=1 install.sh)"
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
# NVIDIA enables nvpmodel (MAXN at boot) only with a display manager
nvp=$(cd "$R" && ls etc/systemd/system/nvpmodel.service lib/systemd/system/nvpmodel.service 2>/dev/null | head -1 || true)
[ -n "$nvp" ] || { echo "rootfs: no nvpmodel.service" >&2; exit 1; }
mkdir -p "$R/etc/systemd/system/multi-user.target.wants"
ln -sfn "/$nvp" "$R/etc/systemd/system/multi-user.target.wants/nvpmodel.service"
# Journal: persistent, 200 MB, nothing forwarded to syslog (B2)
# The image's own files: only what is tracked there (no editor or Finder leftovers from the build machine)
(cd "$IMG/rootfs" && find . -type f ! -name '.DS_Store' ! -name '*~' ! -name '*.swp' ! -name '._*') | while read -r f; do
  m=0644; [ -x "$IMG/rootfs/$f" ] && m=0755
  install -D -m "$m" -o root -g root "$IMG/rootfs/$f" "$R/${f#./}"
done
install -m 0644 /in/repo/base/files/90-eirl-nosyslog.conf "$R/etc/systemd/journald.conf.d/90-eirl-nosyslog.conf"
mkdir -p "$R/var/log/journal"
# What flash.sh writes for this board; the firmware check reads it (IMAGE.md §4)
install -m 0644 /work/out/bootparts/nv_boot_control.conf "$R/etc/nv_boot_control.conf"
echo "$FIRMWARE_MODE" > "$R/usr/lib/eirl-image/firmware-mode"
in_root systemctl enable eirl-firstboot.service eirl-firmware.service

# Units the image does not run: base/units-*.txt where the unit exists (BASE.md §6), USB device mode (D5), sshd
mask_unit() {   # also the units NVIDIA only links (their files are in /opt/nvidia) and template instances
  local u=$1
  if find "$R/etc/systemd/system" "$R/lib/systemd/system" -name "$u" 2>/dev/null | grep -q .; then
    find "$R/etc/systemd/system" -name "$u" -path '*.wants/*' -delete
    rm -f "$R/etc/systemd/system/$u"
    ln -s /dev/null "$R/etc/systemd/system/$u"
    echo "   masked $u"
  fi
}
units() { sed -E 's/#.*//' "$1" | awk 'NF {print $1}'; }
for f in units-b1a.txt units-b2.txt units-b1b-alsa.txt units-qspi.txt; do
  for u in $(units "/in/repo/base/$f"); do
    case "$u" in
      linger:*) ;;                                              # a login user's linger: the image has none
      file:*) ;;                                                # B2's journald drop-in: installed above
      mask:*) mask_unit "${u#mask:}" ;;
      nvweston.service|nvmemwarning.service) find "$R/etc/systemd/system" -name "$u" -path '*.wants/*' -delete ;;
      *) mask_unit "$u" ;;
    esac
  done
done
for u in nv-l4t-usb-device-mode.service nv-l4t-usb-device-mode-runtime.service serial-getty@ttyGS0.service; do mask_unit "$u"; done
# The apt and motd timers, off on the test board since Oct 8 (BASE.md §4.1): an appliance does not refresh apt's lists
# every day, and motd-news has nothing left to run (update-motd.d is emptied below): it failed at every boot
for u in apt-daily.timer apt-daily.service apt-daily-upgrade.timer apt-daily-upgrade.service motd-news.timer motd-news.service; do
  mask_unit "$u"
done
# ModemManager stays installed and disabled, as on the test board (base/keep.txt): its postinst enables it on a new
# install. Whether it runs at boot is decided with the modems' work
find "$R/etc/systemd/system" -name ModemManager.service -path '*.wants/*' -delete
rm -f "$R/etc/systemd/system/dbus-org.freedesktop.ModemManager1.service"
# bluetoothd (Bluetooth audio, BLUETOOTH.md §3.7) is installed but not enabled: its postinst enables it, and it only has
# to run when the release's eirl-bluealsa.service (enabled by install.sh) pulls it in. One switch, ours
find "$R/etc/systemd/system" -name bluetooth.service -path '*.wants/*' -delete
rm -f "$R/etc/systemd/system/dbus-org.bluez.service"
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
# NVIDIA's udev rules (nvidia-l4t-init, 99-tegra-devices.rules) give the GPU's debug nodes to the group debug, which
# L4T's own image creates with its first user: without it udev warns at every boot
in_root getent group debug >/dev/null || in_root groupadd --system debug
# Every nvidia-l4t-* on hold, as on the board: never an apt upgrade of L4T (it rewrote a QSPI on Oct 4)
in_root /bin/sh -c "dpkg-query -W -f '\${db:Status-Abbrev} \${Package}\n' 'nvidia-l4t-*' | awk '\$1 ~ /^.i/ {print \$2}' | xargs apt-mark hold" >/dev/null

step "branding (Canonical's IP policy: a modified Ubuntu without its trademarks, IMAGE.md §9.2)"
rm -f "$R/etc/os-release"
cat > "$R/usr/lib/os-release" <<EOF
NAME="EnhancedIRL Encoder"
VERSION="$IMAGE_VERSION"
ID=ubuntu
ID_LIKE=debian
PRETTY_NAME="EnhancedIRL Encoder $IMAGE_VERSION (Jetson Nano, L4T R32.7.6)"
VERSION_ID="18.04"
HOME_URL="https://github.com/enhanced-irl/eirl-encoder"
VERSION_CODENAME=bionic
UBUNTU_CODENAME=bionic
EOF
ln -s ../usr/lib/os-release "$R/etc/os-release"
sed -i -E 's/^DISTRIB_DESCRIPTION=.*/DISTRIB_DESCRIPTION="EnhancedIRL Encoder '"$IMAGE_VERSION"'"/' "$R/etc/lsb-release"
printf 'EnhancedIRL Encoder \\n \\l\n\n' > "$R/etc/issue"
printf 'EnhancedIRL Encoder\n' > "$R/etc/issue.net"
rm -f "$R"/etc/update-motd.d/* "$R/etc/legal"

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
