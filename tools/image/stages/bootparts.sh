#!/bin/bash
# stages/bootparts.sh — runs in the linux/amd64 boot container (boot.Dockerfile): NVIDIA's own
# flash.sh --no-flash --sign makes the card's signed boot partitions p2-p14 (IMAGE.md §3.6), exactly as
# jetson-disk-image-creator.sh -b jetson-nano -r 300 does, but without building a system image (-r with a
# placeholder): the root filesystem is ours. They depend only on the BSP, the board (3448, FAB 300) and the
# kernel command line U-Boot's LNX image carries, so the stock DTB stays in kernel/dtb (ours only ever
# reaches the kernel, through extlinux's FDT).
#   in:  /in/dl/<BSP tarball>        out: /work/out/bootparts/<n>-<NAME>.bin + layout.tsv + nv_boot_control.conf
set -euo pipefail
: "${BSP_FILE:?}"
B=/work/bootbsp
rm -rf "$B" /work/out/bootparts && mkdir -p "$B" /work/out/bootparts
tar -C "$B" -xjf "/in/dl/$BSP_FILE"
L=$B/Linux_for_Tegra
cd "$L"
truncate -s 1M bootloader/system.img       # placeholder for -r: the APP partition is not taken from here
mkdir -p rootfs/etc rootfs/boot/extlinux   # flash.sh writes nv_boot_control.conf there (the BSP ships rootfs/ empty)
# flash.sh checks $USER, not the uid
USER=root BOARDID=3448 BOARDSKU="" FAB=300 BUILD_SD_IMAGE=1 \
  ./flash.sh --no-flash --sign -r -S 64MiB jetson-nano-qspi-sd mmcblk0p1 > /work/out/bootparts/flash.log 2>&1 ||
  { tail -30 /work/out/bootparts/flash.log >&2; echo "bootparts: flash.sh failed" >&2; exit 1; }

# The partition list of the card, in the order NVIDIA creates them (APP numbered 1, physically last)
python3 tools/nvptparser.py bootloader/signed/flash.xml sdcard > /work/out/bootparts/nvptparser.txt
printf 'num\tname\tsize\tfile\tsha256\n' > /work/out/bootparts/layout.tsv
while IFS=';' read -r n name size file; do
  n=${n#part_num=}; name=${name#part_name=}; size=${size#part_size=}; file=${file#part_file=}
  [ "$name" = master_boot_record ] && { echo "bootparts: unexpected master_boot_record" >&2; exit 1; }
  if [ "$name" = APP ]; then
    printf '%s\t%s\t%s\t%s\t%s\n' "$n" "$name" "$size" "-" "-" >> /work/out/bootparts/layout.tsv
    continue
  fi
  out=-; sum=-
  if [ -n "$file" ]; then
    src=""
    [ -e "bootloader/signed/$file" ] && src="bootloader/signed/$file"
    [ -z "$src" ] && [ -e "bootloader/$file" ] && src="bootloader/$file"
    [ -n "$src" ] || { echo "bootparts: $name: $file not found" >&2; exit 1; }
    [ "$(stat -c %s "$src")" -le "$size" ] || { echo "bootparts: $name: $file is larger than the partition" >&2; exit 1; }
    out=$(printf '%02d-%s.bin' "$n" "$name")
    cp "$src" "/work/out/bootparts/$out"
    sum=$(sha256sum "/work/out/bootparts/$out" | cut -d' ' -f1)
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' "$n" "$name" "$size" "$out" "$sum" >> /work/out/bootparts/layout.tsv
done < /work/out/bootparts/nvptparser.txt
# What flash.sh writes into a root filesystem for this board: the firmware check (IMAGE.md §4) needs it
cp rootfs/etc/nv_boot_control.conf /work/out/bootparts/nv_boot_control.conf
rm -rf "$B"
cat /work/out/bootparts/layout.tsv
