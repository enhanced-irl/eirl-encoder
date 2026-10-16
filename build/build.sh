#!/usr/bin/env bash
# build.sh — the patched tc358743 module and the Nano B01's DTB with the TC358743 on CAM1, for
# L4T R32.7.6 (kernel 4.9.337-tegra). Builds in an ubuntu:18.04 arm64 container: native on an Apple
# Silicon Mac (no cross-compiling). The kernel Image is NOT installed: it is only built to have
# Module.symvers and the DT tree ready.
#
#   ./build/build.sh        → artifacts in build/out/
#
# It also builds the cellular modem modules into build/out/modules/ (MODEMS.md §4) and the Bluetooth ones (btusb
# and btrtl with the Realtek RTL8761B) into build/out/modules-bt/ (BLUETOOTH.md §3.8).
#
# Variables: CACHE (downloads), OUT (output). The Docker volume "l4t-src" keeps the sources and the built
# kernel (~3 GB).
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
OUT="${OUT:-$HERE/build/out}"
IMAGE=l4t-r32.7.6-build
VOLUME=l4t-src
L4T_URL=https://developer.download.nvidia.com/embedded/L4T/r32_Release_v7.6/sources/T210/public_sources.tbz2
L4T_SHA256=8e2d10fb5a582ab9802d4014740a54140bc204fcdcc29794bfa020a856d807c7
# NVIDIA's own kernel config for the Nano: extract-ikconfig of the R32.7.6 BSP's kernel/Image, which is also the
# /proc/config.gz of every board on that kernel (not tegra_defconfig blindly)
KERNEL_CONFIG=build/kernel-4.9.337-tegra.config

mkdir -p "$CACHE" "$OUT"
# The Realtek RTL8761B(U) Bluetooth firmware the patched btrtl loads (BLUETOOTH.md §3.8): linux-firmware 1.173 of
# Ubuntu 18.04 does not have it. Pinned to a linux-firmware tag and verified; redistributable (its licence goes with it)
RTLBT_TAG=20240909
mkdir -p "$OUT/firmware/rtl_bt"
for f in "rtl_bt/rtl8761bu_fw.bin 1d7a9597349ad89344fa16c1913d3e39e9a12e966e417ca16871bc79bbe59edb" \
         "rtl_bt/rtl8761bu_config.bin 6c28a3f07c6a30ed208c4b64862a23f02b7d93543ea980edd24df16bab45095f" \
         "LICENCE.rtlwifi_firmware.txt a61351665b4f264f6c631364f85b907d8f8f41f8b369533ef4021765f9f3b62e"; do
  set -- $f
  c="$CACHE/linux-firmware-$RTLBT_TAG-$(basename "$1")"
  [ -f "$c" ] || { curl -fsSL -o "$c.part" "https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git/plain/$1?h=$RTLBT_TAG" && mv "$c.part" "$c"; }
  echo "$2  $c" | shasum -a 256 -c - >/dev/null || { echo "sha256 mismatch: $c" >&2; exit 1; }
  cp "$c" "$OUT/firmware/rtl_bt/$(basename "$1")"
done
docker build -q -t "$IMAGE" "$HERE/build" >/dev/null
docker volume inspect "$VOLUME" >/dev/null 2>&1 || docker volume create "$VOLUME" >/dev/null

[ -f "$CACHE/public_sources.tbz2" ] || curl -fL -o "$CACHE/public_sources.tbz2" "$L4T_URL"
echo "$L4T_SHA256  $CACHE/public_sources.tbz2" | shasum -a 256 -c - >/dev/null

docker run --rm -v "$VOLUME":/src -v "$CACHE":/cache:ro -v "$HERE":/proj:ro -v "$OUT":/out -e KERNEL_CONFIG="$KERNEL_CONFIG" \
  "$IMAGE" bash -euo pipefail -c '
K=/src/kernel/kernel-4.9; O=/src/out
mk() { make -s -C $K ARCH=arm64 O=$O LOCALVERSION=-tegra "$@"; }

if [ ! -d $K ]; then
  echo "== extracting the R32.7.6 sources"
  cd /src && tar xjf /cache/public_sources.tbz2 Linux_for_Tegra/source/public/kernel_src.tbz2 \
    && tar xjf Linux_for_Tegra/source/public/kernel_src.tbz2
fi
if [ ! -f $O/Module.symvers ]; then
  echo "== base kernel (only the first time, ~11 min on an M-series)"
  mkdir -p $O && cp /proj/$KERNEL_CONFIG $O/.config && mk olddefconfig
  mk -j"$(nproc)" Image modules
fi
[ "$(mk kernelrelease)" = 4.9.337-tegra ] || { echo "unexpected kernelrelease"; exit 1; }

echo "== tc358743 module"
rm -rf /tmp/mod && mkdir /tmp/mod
cp $K/drivers/media/i2c/tc358743.c $K/drivers/media/i2c/tc358743_regs.h /proj/driver/Makefile /tmp/mod/
(cd /tmp/mod && patch -s tc358743.c < /proj/driver/tc358743-jetson.patch)
mk M=/tmp/mod modules 2>&1 | grep -vi "clock skew\|modification time" || true
cp /tmp/mod/tc358743.ko /out/

echo "== modem modules (MODEMS.md §4)"
# qmi_wwan, cdc_mbim and huawei_cdc_ncm are not set in the board config, and option lacks the 5G
# Quectel IDs: built from the same sources, installed under /lib/modules/<release>/updates/ so they
# take precedence over the stock option.ko.
rm -rf /tmp/modem && mkdir /tmp/modem
cp $K/drivers/net/usb/qmi_wwan.c $K/drivers/net/usb/cdc_mbim.c $K/drivers/net/usb/huawei_cdc_ncm.c \
   $K/drivers/usb/serial/option.c $K/drivers/usb/serial/usb-wwan.h /proj/driver/modems/Makefile /tmp/modem/
(cd /tmp/modem && patch -s qmi_wwan.c < /proj/driver/modems/qmi_wwan-ids.patch \
  && patch -s option.c < /proj/driver/modems/option-ids.patch)
mk M=/tmp/modem modules 2>&1 | grep -vi "clock skew\|modification time" || true
# The pipe hides the status of make: check every module was built, and never leave an older one behind
rm -rf /out/modules && mkdir -p /out/modules
for m in qmi_wwan cdc_mbim huawei_cdc_ncm option; do
  [ -f /tmp/modem/$m.ko ] || { echo "modem module $m.ko was not built"; exit 1; }
  cp /tmp/modem/$m.ko /out/modules/
done
for m in /out/modules/*.ko; do printf "%s: " "$(basename $m)"; modinfo -F vermagic $m; done

echo "== Bluetooth modules (BLUETOOTH.md §3.8)"
# btusb and btrtl with the Realtek RTL8761B(U) (TP-Link UB500 and most current dongles): its project id, its
# rtl8761bu firmware and the USB ids of its vendors, from upstream. Installed under /lib/modules/<release>/updates/
# like the modem modules, so they take precedence over the stock btusb/btrtl
rm -rf /tmp/bt && mkdir /tmp/bt
cp $K/drivers/bluetooth/btusb.c $K/drivers/bluetooth/btrtl.c $K/drivers/bluetooth/btrtl.h \
   $K/drivers/bluetooth/btintel.h $K/drivers/bluetooth/btbcm.h /proj/driver/bluetooth/Makefile /tmp/bt/
(cd /tmp/bt && patch -s btrtl.c < /proj/driver/bluetooth/btrtl-8761b.patch \
  && patch -s btusb.c < /proj/driver/bluetooth/btusb-8761bu-ids.patch)
mk M=/tmp/bt modules 2>&1 | grep -vi "clock skew\|modification time" || true
rm -rf /out/modules-bt && mkdir -p /out/modules-bt
for m in btusb btrtl; do
  [ -f /tmp/bt/$m.ko ] || { echo "Bluetooth module $m.ko was not built"; exit 1; }
  cp /tmp/bt/$m.ko /out/modules-bt/
done
for m in /out/modules-bt/*.ko; do printf "%s: " "$(basename $m)"; modinfo -F vermagic $m; done

echo "== B00 DTB + TC358743 on CAM1"
P=/src/hardware/nvidia/platform/t210/porg/kernel-dts
cp /proj/dt/tegra210-porg-camera-tc358743.dtsi /proj/dt/tegra210-porg-tc358743-i2s4.dtsi $P/porg-platforms/
# imx219/imx477 cameras → TC358743; the I2S4 audio is included LAST (after the base pinmux)
sed -e "s#porg-platforms/tegra210-porg-camera-rbpcv3-dual-imx477.dtsi#porg-platforms/tegra210-porg-camera-tc358743.dtsi#" \
    -e "/tegra210-porg-camera-rbpcv2-dual-imx219.dtsi/d" \
    -e "s#^\(\#include \"porg-platforms/tegra210-porg-p3448-emc-a00.dtsi\"\)#\1\n\#include \"porg-platforms/tegra210-porg-tc358743-i2s4.dtsi\"#" \
    $P/tegra210-p3448-0000-p3449-0000-b00.dts > $P/tegra210-p3448-0000-p3449-0000-b00-tc358743.dts
# DAP4 (J4-J7) is in the gpio-input list of gpio_default on the B01: at boot the GPIO driver sets its
# CNF bit (GPIO mode) and the I2S signal never reaches I2S4 even though the pinmux says i2s4b
# ("Failed at I2S4 DAP RX sw reset"). A DT cannot remove one element from a list → the list of the B01 is
# generated WITHOUT J4-J7, taken from the sources themselves.
G=$P/porg-platforms/tegra210-porg-gpio-p3448-0000-b00.dtsi
{
  echo "/* GENERATED by build.sh: gpio-input of the B01 without J4-J7 (DAP4/I2S4). Do not edit. */"
  echo "&gpio_default {"
  echo "	gpio-input = <"
  awk "/gpio_default: default/ {f=1} f && /gpio-input = </ {g=1; next} g && />;/ {exit} g {print}" $G \
    | grep -v "TEGRA_GPIO(J, [4-7])"
  echo "	>;"
  echo "};"
} > $P/porg-platforms/tegra210-porg-tc358743-gpio.dtsi
n_orig=$(awk "/gpio_default: default/ {f=1} f && /gpio-input = </ {g=1; next} g && />;/ {exit} g {print}" $G | grep -c TEGRA_GPIO)
n_new=$(grep -c TEGRA_GPIO $P/porg-platforms/tegra210-porg-tc358743-gpio.dtsi)
[ $((n_orig - n_new)) -eq 4 ] || { echo "gpio-input: expected to remove 4 pins (J4-J7), removed $((n_orig - n_new))"; exit 1; }
sed -i "s#^\(\#include \"porg-platforms/tegra210-porg-tc358743-i2s4.dtsi\"\)#\1\n\#include \"porg-platforms/tegra210-porg-tc358743-gpio.dtsi\"#" \
  $P/tegra210-p3448-0000-p3449-0000-b00-tc358743.dts
grep -n "#include" $P/tegra210-p3448-0000-p3449-0000-b00-tc358743.dts
grep -q b00-tc358743.dtb $P/Makefile || \
  sed -i "s#tegra210-p3448-0000-p3449-0000-b00.dtb#& tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb#" $P/Makefile
mk dtbs 2>&1 | grep -i "b00-tc358743" || true
cp $O/arch/arm64/boot/dts/tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb /out/
modinfo /out/tc358743.ko | grep vermagic
'
# The EDIDs (same modes, a different preferred one: the app picks one, EDID_PREFERRED), from edid/*.hex
OUT="$OUT" "$HERE/build/edid.sh" >/dev/null
ls -l "$OUT"
