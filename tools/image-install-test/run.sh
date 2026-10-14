#!/bin/bash
# tools/image-install-test/run.sh — runs INSIDE the (privileged, offline) container of tools/image-install-test.sh,
# set up the way the SD card image is built: the container is the build machine (with /run/systemd/system, as a
# systemd host has), and a copy of its root at /img is the image, an arm64 chroot with /proc, /sys (read-only)
# and /dev mounted and no systemd running in it. In the chroot ("inner"), the release bundle's real install.sh
# runs in image mode twice (EIRL_IMAGE=1, then --image). Everything that needs a running system is replaced by a
# guard that logs it as forbidden; systemctl is the real one for `enable` and `is-enabled` only.
set -uo pipefail
cd /
fail() { echo "FAIL: $*"; exit 1; }
LIB=/usr/lib/eirl-encoder

if [ "${1:-}" != inner ]; then
  # The build machine: copy its root as the image's (its own mounts, /proc /sys /dev and the bind mounts, stay out)
  mkdir /img && tar -C / --one-file-system --exclude=./img -cf - . | tar -C /img -xpf - || fail "cannot copy the root"
  mkdir -p /img/proc /img/sys /img/dev /img/s /img/t
  mount -t proc proc /img/proc && mount --bind /sys /img/sys && mount -o remount,bind,ro /img/sys &&
    mount --bind /dev /img/dev || fail "cannot mount /proc, /sys and /dev in the chroot"
  cp /s/bundle.tar.gz /img/s/ && cp /s/sim/run.sh /img/t/run.sh
  # A systemd host, and its /run as if it were bound into the chroot: neither may make --image refuse there
  mkdir -p /run/systemd/system /img/run/systemd/system
  chroot /img /bin/bash /t/run.sh inner
  rc=$?
  umount /img/proc /img/sys /img/dev
  [ $rc = 0 ] || exit $rc
  echo "=== 3. --image where systemd runs this very root (not a chroot) is refused"
  mkdir -p /tmp/b && tar -xzf /s/bundle.tar.gz -C /tmp/b
  /tmp/b/install.sh --image > /tmp/install-3.log 2>&1; rc=$?
  [ $rc = 64 ] || { cat /tmp/install-3.log; fail "--image on a running system: exit $rc, want 64"; }
  [ ! -e /usr/local/bin/eirl-encoderd ] || fail "the refused run installed something"
  echo "refused (exit 64), nothing installed: $(cat /tmp/install-3.log)"
  echo "ALL OK"
  exit 0
fi

# Inner: the image's root, in the chroot
[ "$(stat -c %d:%i /)" != "$(stat -c %d:%i /proc/1/root/.)" ] || fail "not in a chroot"
[ -d /run/systemd/system ] && [ -r /proc/1/root ] || fail "the chroot is not set up as expected"
echo "=== in a chroot with /proc, /sys (ro) and /dev mounted, /run/systemd/system present, no systemd running here"

# What the image already has before the bundle: L4T's i2c and input groups (the latter from udev, not installed
# here), its boot entry, NetworkManager's directory and an empty machine-id (systemd writes it at the first boot)
for g in i2c input; do groupadd -f "$g"; done
mkdir -p /boot/extlinux /etc/NetworkManager/dispatcher.d
cat > /boot/extlinux/extlinux.conf <<'EXT'
TIMEOUT 30
DEFAULT primary

MENU TITLE L4T boot options

LABEL primary
      MENU LABEL primary kernel
      LINUX /boot/Image
      INITRD /boot/initrd
      APPEND ${cbootargs} quiet root=/dev/mmcblk0p1 rw rootwait
EXT
: > /etc/machine-id

# The guards, first in PATH
mkdir -p /opt/guard
for c in udevadm systemd-tmpfiles nmcli ldconfig; do
  printf '#!/bin/sh\necho "%s $*" >> /var/log/forbidden.log\nexit 1\n' "$c" > "/opt/guard/$c"
done
cat > /opt/guard/systemctl <<'G'
#!/bin/sh
echo "systemctl $*" >> /var/log/systemctl.log
case "$1" in enable|is-enabled) exec /bin/systemctl "$@" ;; esac
echo "systemctl $*" >> /var/log/forbidden.log
exit 1
G
chmod 0755 /opt/guard/*
GUARDED=/opt/guard:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# snapshot: every file, link and directory the install may touch, with type, mode, owner and content (not
# times). The shadow-utils backups (/etc/passwd- …) are left out: they hold the previous state by design.
snapshot() {
  python3 - /etc /usr/local /usr/lib/eirl-encoder /usr/share/doc/eirl-encoder /boot /var/lib <<'PY'
import hashlib, os, stat, sys
for top in sys.argv[1:]:
    for root, dirs, files in os.walk(top):
        dirs.sort()
        for name in sorted(dirs + files):
            p = os.path.join(root, name)
            if p in ("/etc/passwd-", "/etc/group-", "/etc/shadow-", "/etc/gshadow-") or p.startswith("/var/lib/apt") or p.startswith("/var/lib/dpkg"):
                continue
            st = os.lstat(p)
            if stat.S_ISLNK(st.st_mode):
                what = "-> " + os.readlink(p)
            elif stat.S_ISREG(st.st_mode):
                with open(p, "rb") as f:
                    what = hashlib.sha256(f.read()).hexdigest()
            else:
                what = ""
            print(p, oct(st.st_mode), st.st_uid, st.st_gid, what)
PY
}

mkdir -p /tmp/b && tar -xzf /s/bundle.tar.gz -C /tmp/b || fail "cannot extract the bundle"
echo "=== bundle $(cat /tmp/b/VERSION): $(wc -l < /tmp/b/files.txt) paths in files.txt"
snapshot > /tmp/snap-0

echo "=== 1. install.sh in image mode (EIRL_IMAGE=1)"
env PATH=$GUARDED EIRL_IMAGE=1 /tmp/b/install.sh > /var/log/install-1.log 2>&1 || { tail -30 /var/log/install-1.log; fail "install.sh (1st run)"; }
grep -E '^(user|created|new device|image:|engine:)' /var/log/install-1.log
[ ! -s /var/log/forbidden.log ] || { cat /var/log/forbidden.log; fail "image mode ran commands that need a running system"; }
for d in /run/eirl-uplinks /run/eirl-ap /run/eirl-engine /run/eirl-encoder; do
  [ ! -e "$d" ] || fail "image mode created $d (a running system's)"
done
echo "forbidden commands: none · systemctl calls: $(cut -d' ' -f2 /var/log/systemctl.log | sort | uniq -c | tr -s ' ' | tr '\n' ' ')"

# Every path of files.txt is there, except the retired engine files it lists so that a rollback brings them back
retired=" $(tr '\n' ' ' < /tmp/b/engine/retired.txt 2>/dev/null) "
n=0
for p in $(cat $LIB/files.txt); do
  case "$p" in $LIB/engine/*) case "$retired" in *" ${p#$LIB/engine/} "*) [ ! -e "$p" ] || fail "retired but installed: $p"; continue ;; esac ;; esac
  [ -e "$p" ] || fail "listed but not installed: $p"
  n=$((n + 1))
done
echo "files.txt: $(wc -l < $LIB/files.txt) paths: $n installed, the rest retired engine files (absent)"
for p in /usr/local/bin/srt-live-transmit $LIB/lib/libsrt.so.1.5 /usr/local/bin/srtla_send /usr/local/bin/mediamtx \
  $LIB/engine/eirl-engine-go $LIB/engine/eirl-hdmi-init.sh /usr/local/bin/eirl-enginectl \
  $LIB/engine/edid-1080p30-lpcm48k.bin $LIB/engine/edid-1080p50-lpcm48k.bin $LIB/engine/edid-720p60-lpcm48k.bin \
  /usr/share/doc/eirl-encoder/licenses/srt/LICENSE /usr/share/doc/eirl-encoder/licenses/srt/SOURCE.txt; do
  grep -qx "$p" $LIB/files.txt || fail "not in files.txt: $p"
done
echo "SRT, EDIDs and licences: listed and installed"
srt=$(/usr/local/bin/srt-live-transmit -version 2>&1 || true)
echo "$srt" | grep -q 'SRT Library version: 1.5.7' || fail "srt-live-transmit does not run: $srt"
ldd /usr/local/bin/srt-live-transmit | grep -q "libsrt.so.1.5 => $LIB/lib/libsrt.so.1.5" || fail "srt-live-transmit does not load $LIB/lib/libsrt.so.1.5"
echo "srt-live-transmit: $(echo "$srt" | tail -1) · libsrt from $LIB/lib"

# Board settings and the app's file of a new device
[ "$(stat -c '%U:%G %a' /etc/default/eirl-engine)" = "root:root 644" ] || fail "/etc/default/eirl-engine: $(stat -c '%U:%G %a' /etc/default/eirl-engine)"
grep -qx 'SOURCE=hdmi' /etc/default/eirl-engine || fail "/etc/default/eirl-engine without SOURCE=hdmi"
[ "$(stat -c '%U:%G %a' /etc/eirl-encoder/engine.env)" = "eirl-encoder:eirl-encoder 640" ] || fail "engine.env: $(stat -c '%U:%G %a' /etc/eirl-encoder/engine.env)"
grep -qx 'OUTPUT=off' /etc/eirl-encoder/engine.env || fail "a new device's engine.env without OUTPUT=off"
grep -qx 'SRT_URL=' /etc/eirl-encoder/engine.env || fail "a new device with a destination"
[ "$(stat -c '%U:%G %a' /etc/default/eirl-encoderd)" = "root:root 644" ] || fail "/etc/default/eirl-encoderd"
echo "new device: /etc/default/eirl-engine (root 644, SOURCE=hdmi), engine.env (OUTPUT=off, no destination)"

# Users and groups
for u in eirl-encoder eirl-engine eirl-media; do getent passwd $u >/dev/null || fail "no user $u"; done
getent group eirl-usbhub >/dev/null || fail "no group eirl-usbhub"
for g in video audio input i2c; do id -nG eirl-engine | grep -qw $g || fail "eirl-engine not in $g"; done
for g in video i2c systemd-journal; do id -nG eirl-encoder | grep -qw $g || fail "eirl-encoder not in $g"; done
echo "users: eirl-encoder ($(id -nG eirl-encoder)), eirl-engine ($(id -nG eirl-engine)), eirl-media"

# Units enabled for the first boot
for u in eirl-encoderd eirl-engine eirl-hdmi-init eirl-update-recover eirl-ap \
  eirl-splash eirl-splash-quit eirl-splash-console eirl-splash-reboot eirl-splash-poweroff; do
  s=$(/bin/systemctl is-enabled $u.service 2>&1)
  [ "$s" = enabled ] || fail "$u.service is $s"
done
echo "enabled: $(ls /etc/systemd/system/multi-user.target.wants | tr '\n' ' ')"
grep -q '^LABEL eirl-splash' /boot/extlinux/extlinux.conf || fail "no splash boot entry"

# The hardware id the first boot can use to name things (eirl-encoderd -print-hardware-id), once there is a
# machine-id; without one it says so and fails
if /usr/local/bin/eirl-encoderd -print-hardware-id >/tmp/hwid 2>/tmp/hwid.err; then fail "a hardware id without a machine-id: $(cat /tmp/hwid)"; fi
echo 0123456789abcdef0123456789abcdef > /etc/machine-id
want=$(python3 -c 'import hashlib; print(hashlib.sha256(b"eirl-encoder:0123456789abcdef0123456789abcdef").hexdigest()[:16])')
got=$(/usr/local/bin/eirl-encoderd -print-hardware-id) || fail "-print-hardware-id"
[ "$got" = "$want" ] || fail "hardware id $got, want $want"
: > /etc/machine-id
echo "hardware id: $got (as cloud.DeviceID), and an error without a machine-id"

echo "=== 2. install.sh --image again: idempotent"
snapshot > /tmp/snap-1
: > /var/log/systemctl.log
env PATH=$GUARDED /tmp/b/install.sh --image > /var/log/install-2.log 2>&1 || { tail -30 /var/log/install-2.log; fail "install.sh (2nd run)"; }
[ ! -s /var/log/forbidden.log ] || { cat /var/log/forbidden.log; fail "2nd run: forbidden commands"; }
! grep -E '^(user .* created|created |new device|destination migrated)' /var/log/install-2.log || fail "the 2nd run created or migrated something"
snapshot > /tmp/snap-2
diff /tmp/snap-1 /tmp/snap-2 > /tmp/snap.diff && echo "2nd run: no change (type, mode, owner and content of $(wc -l < /tmp/snap-2) paths)" ||
  { cat /tmp/snap.diff; fail "the 2nd run changed something"; }
echo "1st run added or changed $(diff /tmp/snap-0 /tmp/snap-1 | grep -c '^>') paths"

