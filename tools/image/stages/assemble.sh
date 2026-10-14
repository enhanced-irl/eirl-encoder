#!/bin/bash
# stages/assemble.sh — runs in the linux/arm64 work container: the card image from /work/rootfs and the boot
# partitions of stages/bootparts.sh, without loop devices or privileges (IMAGE.md §3.6):
#   GPT with NVIDIA's 14 partitions in NVIDIA's order (APP numbered 1, physically last) · each boot partition
#   written with dd at its offset · APP = an ext4 made with mke2fs -d (e2fsprogs 1.44.1, Ubuntu 18.04's) ·
#   xz. Output: /out/<IMAGE_NAME>.img.xz (+ the raw image's size and sha256 in /work/out/raw.txt).
# Environment: IMAGE_NAME, APP_HEADROOM_MB (free space left in APP before it grows at first boot; default 1024)
set -euo pipefail
: "${IMAGE_NAME:?}"
HEADROOM=${APP_HEADROOM_MB:-1024}
R=/work/rootfs
P=/work/out/bootparts
W=/work/out/assemble
rm -rf "$W" && mkdir -p "$W"
step() { echo "== assemble: $*"; }

step "APP: ext4 from the root filesystem"
used=$(du -sm "$R" | cut -f1)
app_mb=$(( used + used / 8 + HEADROOM ))                   # +12 % for ext4's own metadata, plus the headroom
mke2fs -q -F -t ext4 -b 4096 -L APP -E root_owner=0:0 -d "$R" "$W/app.ext4" "${app_mb}M"
e2fsck -fn "$W/app.ext4" >/dev/null
echo "   rootfs ${used} MiB, APP ${app_mb} MiB"

step "partition table (NVIDIA's order and sizes; APP last)"
# The image size as jetson-disk-image-creator.sh computes it: every partition rounded up to 1 MiB plus 1 MiB,
# plus 2 MiB for the two GPTs
total_mb=2
while IFS=$'\t' read -r n name size file sum; do
  [ "$n" = num ] && continue
  [ "$name" = APP ] && size=$(( app_mb * 1024 * 1024 ))
  total_mb=$(( total_mb + size / (1024 * 1024) + 1 ))
done < "$P/layout.tsv"
img=$W/image.img
truncate -s "${total_mb}M" "$img"
sgdisk -og "$img" >/dev/null
while IFS=$'\t' read -r n name size file sum; do
  [ "$n" = num ] && continue
  [ "$name" = APP ] && size=$(( app_mb * 1024 * 1024 ))
  sgdisk -n "$n:0:+$(( size / 512 ))" -c "$n:$name" -t "$n:8300" "$img" >/dev/null
done < "$P/layout.tsv"
sgdisk -v "$img" | grep -q 'No problems found' || { sgdisk -v "$img" >&2; exit 1; }

step "writing the partitions"
first() { sgdisk -i "$1" "$img" | sed -n 's/^First sector: \([0-9]*\).*/\1/p'; }
while IFS=$'\t' read -r n name size file sum; do
  [ "$n" = num ] && continue
  if [ "$name" = APP ]; then
    dd if="$W/app.ext4" of="$img" bs=4M seek="$(( $(first "$n") * 512 ))" oflag=seek_bytes conv=notrunc,sparse status=none
  elif [ "$file" != - ]; then
    [ "$(sha256sum "$P/$file" | cut -d' ' -f1)" = "$sum" ] || { echo "assemble: $file changed since bootparts" >&2; exit 1; }
    dd if="$P/$file" of="$img" bs=64K seek="$(( $(first "$n") * 512 ))" oflag=seek_bytes conv=notrunc status=none
  fi
done < "$P/layout.tsv"
rm -f "$W/app.ext4"
sgdisk -p "$img" | sed 's/^/   /'
printf 'rawSize=%s\nrawSha256=%s\n' "$(stat -c %s "$img")" "$(sha256sum "$img" | cut -d' ' -f1)" > /work/out/raw.txt

step "xz"
xz -T0 -6 -c "$img" > "/out/$IMAGE_NAME.img.xz.part"
mv -f "/out/$IMAGE_NAME.img.xz.part" "/out/$IMAGE_NAME.img.xz"
ls -l "/out/$IMAGE_NAME.img.xz" | awk '{print "   " $5 " bytes"}'
