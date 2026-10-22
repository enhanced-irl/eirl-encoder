#!/usr/bin/env bash
# tools/image/import-rootfs.sh IMG.img.xz TAG — the root filesystem (APP, p1, physically the last partition) of an
# encoder SD card image as a linux/arm64 Docker image TAG, to run it as a container: a board's system without the
# board (tools/system-equiv.sh, tools/system-sim.sh). No loop devices and no privileges: the partition is read with
# debugfs (e2fsprogs), in the image's work container. Ownership and modes are kept; setuid bits, file capabilities and
# device nodes are not (nothing the tests use needs them).
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=pins.sh
. tools/image/pins.sh
[ $# -eq 2 ] && [ -f "$1" ] || { echo "usage: tools/image/import-rootfs.sh IMG.img.xz TAG" >&2; exit 64; }
img=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
docker build -q --platform linux/arm64 --build-arg TOOLS_BASE="$TOOLS_BASE" -t eirl-image-tools:arm64 -f tools/image/Dockerfile tools/image >/dev/null
vol=eirl-rootfs-$$
docker volume create "$vol" >/dev/null
trap 'docker volume rm -f "$vol" >/dev/null 2>&1' EXIT
docker run --rm --platform linux/arm64 -v "$vol:/w" -v "$(dirname "$img"):/img:ro" eirl-image-tools:arm64 bash -euo pipefail -c '
  xz -dc "/img/$1" > /w/raw.img
  read -r first last < <(sgdisk -i 1 /w/raw.img | awk "/First sector/ {f=\$3} /Last sector/ {l=\$3} END {print f, l}")
  dd if=/w/raw.img of=/w/app.fs bs=512 skip="$first" count=$((last - first + 1)) status=none
  rm -f /w/raw.img && mkdir -p /w/root
  debugfs -R "rdump / /w/root" /w/app.fs >/dev/null 2>&1
  rm -f /w/app.fs
  [ -x /w/root/usr/local/sbin/eirl-update ] || { echo "no encoder root filesystem in $1" >&2; exit 1; }' _ "$(basename "$img")"
docker run --rm -v "$vol:/w" eirl-image-tools:arm64 tar -C /w/root --numeric-owner -cf - . |
  docker import --platform linux/arm64 - "$2" >/dev/null
echo "$2: the root filesystem of $(basename "$img")"
