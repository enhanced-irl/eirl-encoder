#!/usr/bin/env bash
# build.sh — módulo tc358743 parcheado + DTB de la Nano B01 con el TC358743 en CAM1, para
# L4T R32.7.6 (kernel 4.9.337-tegra). Compila en un contenedor ubuntu:18.04 arm64: en un Mac
# con Apple Silicon es nativo (sin compilación cruzada). El Image del kernel NO se instala:
# solo hace falta para tener Module.symvers y el árbol de DT preparados.
#
#   JETSON=jetson ./build/build.sh        → artefactos en build/out/
#
# It also builds the cellular modem modules into build/out/modules/ (MODEMS.md §4).
#
# Variables: JETSON (host ssh, para leer /proc/config.gz la primera vez), CACHE (descargas),
# OUT (salida). El volumen Docker "l4t-src" guarda fuentes y kernel compilado (~3 GB).
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
JETSON="${JETSON:-jetson}"
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
OUT="${OUT:-$HERE/build/out}"
IMAGE=l4t-r32.7.6-build
VOLUME=l4t-src
L4T_URL=https://developer.download.nvidia.com/embedded/L4T/r32_Release_v7.6/sources/T210/public_sources.tbz2
L4T_SHA256=8e2d10fb5a582ab9802d4014740a54140bc204fcdcc29794bfa020a856d807c7

mkdir -p "$CACHE" "$OUT"
docker build -q -t "$IMAGE" "$HERE/build" >/dev/null
docker volume inspect "$VOLUME" >/dev/null 2>&1 || docker volume create "$VOLUME" >/dev/null

[ -f "$CACHE/public_sources.tbz2" ] || curl -fL -o "$CACHE/public_sources.tbz2" "$L4T_URL"
echo "$L4T_SHA256  $CACHE/public_sources.tbz2" | shasum -a 256 -c - >/dev/null
# Config EXACTA del kernel de la placa (la de NVIDIA para la Nano, no tegra_defconfig a ciegas)
[ -s "$CACHE/running.config" ] || ssh "$JETSON" 'zcat /proc/config.gz' > "$CACHE/running.config"

docker run --rm -v "$VOLUME":/src -v "$CACHE":/cache:ro -v "$HERE":/proj:ro -v "$OUT":/out "$IMAGE" \
  bash -euo pipefail -c '
K=/src/kernel/kernel-4.9; O=/src/out
mk() { make -s -C $K ARCH=arm64 O=$O LOCALVERSION=-tegra "$@"; }

if [ ! -d $K ]; then
  echo "== extrayendo fuentes R32.7.6"
  cd /src && tar xjf /cache/public_sources.tbz2 Linux_for_Tegra/source/public/kernel_src.tbz2 \
    && tar xjf Linux_for_Tegra/source/public/kernel_src.tbz2
fi
if [ ! -f $O/Module.symvers ]; then
  echo "== kernel base (solo la primera vez, ~11 min en un M-series)"
  mkdir -p $O && cp /cache/running.config $O/.config && mk olddefconfig
  mk -j"$(nproc)" Image modules
fi
[ "$(mk kernelrelease)" = 4.9.337-tegra ] || { echo "kernelrelease inesperado"; exit 1; }

echo "== módulo tc358743"
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

echo "== DTB b00 + TC358743 en CAM1"
P=/src/hardware/nvidia/platform/t210/porg/kernel-dts
cp /proj/dt/tegra210-porg-camera-tc358743.dtsi /proj/dt/tegra210-porg-tc358743-i2s4.dtsi $P/porg-platforms/
# cámaras imx219/imx477 → TC358743; el audio I2S4 se incluye AL FINAL (tras el pinmux base)
sed -e "s#porg-platforms/tegra210-porg-camera-rbpcv3-dual-imx477.dtsi#porg-platforms/tegra210-porg-camera-tc358743.dtsi#" \
    -e "/tegra210-porg-camera-rbpcv2-dual-imx219.dtsi/d" \
    -e "s#^\(\#include \"porg-platforms/tegra210-porg-p3448-emc-a00.dtsi\"\)#\1\n\#include \"porg-platforms/tegra210-porg-tc358743-i2s4.dtsi\"#" \
    $P/tegra210-p3448-0000-p3449-0000-b00.dts > $P/tegra210-p3448-0000-p3449-0000-b00-tc358743.dts
# DAP4 (J4-J7) está en la lista gpio-input del gpio_default de la B01: al arrancar el driver GPIO
# pone su bit CNF (modo GPIO) y la señal I2S nunca llega al I2S4 aunque el pinmux diga i2s4b
# ("Failed at I2S4 DAP RX sw reset"). Un DT no puede quitar un elemento de una lista → se genera
# la lista de la B01 SIN J4-J7, sacada de las propias fuentes.
G=$P/porg-platforms/tegra210-porg-gpio-p3448-0000-b00.dtsi
{
  echo "/* GENERADO por build.sh: gpio-input de la B01 sin J4-J7 (DAP4/I2S4). No editar. */"
  echo "&gpio_default {"
  echo "	gpio-input = <"
  awk "/gpio_default: default/ {f=1} f && /gpio-input = </ {g=1; next} g && />;/ {exit} g {print}" $G \
    | grep -v "TEGRA_GPIO(J, [4-7])"
  echo "	>;"
  echo "};"
} > $P/porg-platforms/tegra210-porg-tc358743-gpio.dtsi
n_orig=$(awk "/gpio_default: default/ {f=1} f && /gpio-input = </ {g=1; next} g && />;/ {exit} g {print}" $G | grep -c TEGRA_GPIO)
n_new=$(grep -c TEGRA_GPIO $P/porg-platforms/tegra210-porg-tc358743-gpio.dtsi)
[ $((n_orig - n_new)) -eq 4 ] || { echo "gpio-input: esperaba quitar 4 pines (J4-J7), quito $((n_orig - n_new))"; exit 1; }
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
