#!/usr/bin/env bash
# Installs on the board the cellular modem modules built by build/build.sh (build/out/modules/: qmi_wwan,
# cdc_mbim, huawei_cdc_ncm, option with the Quectel IDs L4T lacks, and ipheth for an iPhone; MODEMS.md §4).
#   tools/modem-modules.sh            → copies them to /lib/modules/<release>/updates/eirl-modems/ and runs depmod
#   tools/modem-modules.sh --check    → what is installed and which of them are loaded
#   tools/modem-modules.sh --undo     → removes them and runs depmod (the stock option.ko is used again)
#   JETSON=<host> for another board (default: the `jetson` alias of ~/.ssh/config)
#
# It loads nothing and does not touch the engine or the USB bus: udev loads a module when a modem that needs
# it is plugged in. An option.ko already loaded (a modem plugged in) keeps the old one until it is replugged
# or the board restarts. Ubuntu's depmod looks in updates/ first (/etc/depmod.d/ubuntu.conf), so these
# win over the kernel's own. It refuses modules built for another kernel.
set -euo pipefail
HOST=${JETSON:-jetson}
cd "$(dirname "$0")/.."
SRC=build/out/modules
MODS="qmi_wwan cdc_mbim huawei_cdc_ncm option ipheth"

case "${1:-}" in
--check)
  ssh "$HOST" 'd=/lib/modules/$(uname -r)/updates/eirl-modems; ls -l $d 2>/dev/null || echo "not installed ($d)";
    for m in '"$MODS"'; do printf "%-15s %s\n" $m "$(modinfo -F filename $m 2>/dev/null || echo -)"; done;
    echo "loaded:"; lsmod | grep -E "^(qmi_wwan|cdc_mbim|huawei_cdc_ncm|option|ipheth|usb_wwan|cdc_wdm) " || echo "  none"'
  exit 0
  ;;
--undo)
  ssh "$HOST" 'set -e; d=/lib/modules/$(uname -r)/updates/eirl-modems; sudo rm -rf "$d"; sudo depmod -a; echo "removed $d"'
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
release=$(ssh "$HOST" uname -r)
for m in $MODS; do
  vm=$(docker run --rm -v "$PWD/$SRC":/m l4t-r32.7.6-build modinfo -F vermagic "/m/$m.ko" | awk '{print $1}')
  [ "$vm" = "$release" ] || { echo "$m.ko is built for $vm, the board runs $release" >&2; exit 1; }
done
tmp=$(ssh "$HOST" mktemp -d)
scp -q $(for m in $MODS; do echo "$SRC/$m.ko"; done) "$HOST:$tmp/"
ssh "$HOST" "set -e; d=/lib/modules/\$(uname -r)/updates/eirl-modems; sudo install -d -m 0755 \$d;
  for m in $MODS; do sudo install -m 0644 $tmp/\$m.ko \$d/\$m.ko; done; rm -rf $tmp; sudo depmod -a;
  for m in $MODS; do printf '%-15s %s\n' \$m \"\$(modinfo -F filename \$m)\"; done"
