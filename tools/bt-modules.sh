#!/usr/bin/env bash
# Installs on the board the Bluetooth modules built by build/build.sh (build/out/modules-bt/: btusb and btrtl with the
# Realtek RTL8761B(U), the chip of the TP-Link UB500 and most current dongles, which the 4.9 kernel lacks) and the
# firmware they load (build/out/firmware/rtl_bt/: rtl8761bu, not in Ubuntu 18.04's linux-firmware). BLUETOOTH.md §3.8.
#   tools/bt-modules.sh            → copies them to /lib/modules/<release>/updates/eirl-bluetooth/ and /lib/firmware/rtl_bt/,
#                                    runs depmod and reloads btusb (only the Bluetooth adapter notices)
#   tools/bt-modules.sh --check    → what is installed and loaded, and what the adapter runs
#   tools/bt-modules.sh --undo     → removes them, runs depmod and reloads btusb (the stock modules again)
#   JETSON=<host> for another board (default: the `jetson` alias of ~/.ssh/config)
#
# It does not touch the engine, the capture cards or the stream: reloading btusb only drops the Bluetooth adapter
# for a few seconds (connected headphones reconnect). New boards get all of this from the SD card image (IMAGE.md);
# the OTA updates never touch the kernel or its modules (UPDATES.md). It refuses modules built for another kernel.
set -euo pipefail
HOST=${JETSON:-jetson}
cd "$(dirname "$0")/.."
SRC=build/out/modules-bt
FW=build/out/firmware/rtl_bt
MODS="btusb btrtl"
FILES="rtl8761bu_fw.bin rtl8761bu_config.bin LICENCE.rtlwifi_firmware.txt"
RELOAD='sudo rmmod btusb 2>/dev/null || true; sudo rmmod btrtl 2>/dev/null || true; sudo modprobe btusb'

case "${1:-}" in
--check)
  ssh "$HOST" 'd=/lib/modules/$(uname -r)/updates/eirl-bluetooth; ls -l $d 2>/dev/null || echo "not installed ($d)";
    ls -l /lib/firmware/rtl_bt/rtl8761bu_* 2>/dev/null || echo "no rtl8761bu firmware";
    for m in '"$MODS"'; do printf "%-6s %s\n" $m "$(modinfo -F filename $m 2>/dev/null || echo -)"; done;
    echo "loaded:"; lsmod | grep -E "^bt(usb|rtl) " || echo "  none";
    hciconfig -a 2>/dev/null | grep -E "^hci|Manufacturer|LMP" || echo "no adapter"'
  exit 0
  ;;
--undo)
  ssh "$HOST" "set -e; d=/lib/modules/\$(uname -r)/updates/eirl-bluetooth; sudo rm -rf \"\$d\";
    for f in $FILES; do sudo rm -f /lib/firmware/rtl_bt/\$f; done; sudo depmod -a; $RELOAD; echo \"removed \$d and the firmware\""
  exit 0
  ;;
"") ;;
*)
  echo "usage: $0 [--check|--undo]" >&2
  exit 2
  ;;
esac

for m in $MODS; do
  [ -f "$SRC/$m.ko" ] || { echo "missing $SRC/$m.ko: run build/build.sh first" >&2; exit 1; }
done
for f in $FILES; do
  [ -f "$FW/$f" ] || { echo "missing $FW/$f: run build/build.sh first" >&2; exit 1; }
done
release=$(ssh "$HOST" uname -r)
for m in $MODS; do
  vm=$(docker run --rm -v "$PWD/$SRC":/m l4t-r32.7.6-build modinfo -F vermagic "/m/$m.ko" | awk '{print $1}')
  [ "$vm" = "$release" ] || { echo "$m.ko is built for $vm, the board runs $release" >&2; exit 1; }
done
tmp=$(ssh "$HOST" mktemp -d)
scp -q $(for m in $MODS; do echo "$SRC/$m.ko"; done) $(for f in $FILES; do echo "$FW/$f"; done) "$HOST:$tmp/"
ssh "$HOST" "set -e; d=/lib/modules/\$(uname -r)/updates/eirl-bluetooth; sudo install -d -m 0755 \$d /lib/firmware/rtl_bt;
  for m in $MODS; do sudo install -m 0644 $tmp/\$m.ko \$d/\$m.ko; done
  for f in $FILES; do sudo install -m 0644 $tmp/\$f /lib/firmware/rtl_bt/\$f; done
  rm -rf $tmp; sudo depmod -a; $RELOAD
  for m in $MODS; do printf '%-6s %s\n' \$m \"\$(modinfo -F filename \$m)\"; done"
