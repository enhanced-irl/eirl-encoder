#!/usr/bin/env bash
# bluealsa.sh — builds BlueALSA (bluez-alsa, MIT) for the Jetson: aarch64, Ubuntu 18.04 (glibc 2.27), in the same
# container as srtla_send, SRT and RIST (build/Dockerfile) plus the -dev packages it needs. BLUETOOTH.md §3.7.
# Pinned release and tarball verified by sha256; the source is not modified.
#
#   ./build/bluealsa.sh   → build/out/bluealsa/{bluealsa,libasound_module_pcm_bluealsa.so,
#                           libasound_module_ctl_bluealsa.so,20-bluealsa.conf,bluealsa.version,LICENSE,
#                           LICENSE.sbc}
#
# Why 4.1.0: the newest release whose configure takes Ubuntu 18.04's libraries (BlueZ 5.48, glib 2.56, sbc 1.3);
# from 4.2.0 it wants BlueZ ≥ 5.51 and glib ≥ 2.58.2. Since 4.1.0 the PCMs are listed through the D-Bus
# ObjectManager on /org/bluealsa, which is what the app reads (internal/bluetooth).
# What it is built with: A2DP with SBC only and hands-free with CVSD only (no AAC, aptX, LDAC, mSBC: kernel 4.9's
# btusb does not carry mSBC's USB alternate setting), no tools (bluealsa-aplay, -cli, -rfcomm, hcitop), no man pages,
# no systemd unit (ours is manager/deploy/eirl-bluealsa.service). It links the image's own libraries (libasound2,
# libbluetooth3, libdbus-1-3, libglib2.0-0), never libbsd: the build checks it.
# Except libsbc: the daemon calls sbc_reinit_a2dp, which libsbc only has from 1.4 (18.04 ships 1.3, although
# BlueALSA's configure asks for ≥ 1.2). So the daemon links a static libsbc 2.0 (LGPL-2.1+, kernel.org, pinned and
# verified like BlueALSA) and does not need the system's; the ALSA plugins do not use it.
# To update it: change BLUEALSA_VERSION and BLUEALSA_SHA256, read its NEWS for the D-Bus API (PCM1, ObjectManager)
# and run the Bluetooth tests on the board (BLUETOOTH.md §5, B5).
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
OUT="${OUT:-$HERE/build/out}/bluealsa"
IMAGE=l4t-r32.7.6-build
BA_IMAGE=l4t-r32.7.6-build-bluealsa
BLUEALSA_VERSION=v4.1.0   # 2023-05 (github.com/arkq/bluez-alsa, tag v4.1.0)
BLUEALSA_SHA256=91ecd35d841abe702260088ebe4adf86c4e38fd21635e1b5aa47873a1cc10421
TARBALL="$CACHE/bluez-alsa-$BLUEALSA_VERSION.tar.gz"
SBC_VERSION=2.0           # 2022-07 (kernel.org/pub/linux/bluetooth; its sha256 is the one in sha256sums.asc there)
SBC_SHA256=8f12368e1dbbf55e14536520473cfb338c84b392939cc9b64298360fd4a07992
SBC_TARBALL="$CACHE/sbc-$SBC_VERSION.tar.xz"

mkdir -p "$CACHE" "$OUT"
if [ ! -f "$TARBALL" ]; then
  curl -fsSL -o "$TARBALL.part" "https://github.com/arkq/bluez-alsa/archive/refs/tags/$BLUEALSA_VERSION.tar.gz"
  mv "$TARBALL.part" "$TARBALL"
fi
echo "$BLUEALSA_SHA256  $TARBALL" | shasum -a 256 -c - >/dev/null || { echo "bluealsa: sha256 mismatch ($TARBALL)" >&2; exit 1; }
if [ ! -f "$SBC_TARBALL" ]; then
  curl -fsSL -o "$SBC_TARBALL.part" "https://www.kernel.org/pub/linux/bluetooth/sbc-$SBC_VERSION.tar.xz"
  mv "$SBC_TARBALL.part" "$SBC_TARBALL"
fi
echo "$SBC_SHA256  $SBC_TARBALL" | shasum -a 256 -c - >/dev/null || { echo "bluealsa: sha256 mismatch ($SBC_TARBALL)" >&2; exit 1; }
docker build -q -t "$IMAGE" "$HERE/build" >/dev/null
docker build -q -t "$BA_IMAGE" - >/dev/null <<DOCKERFILE
FROM $IMAGE
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      autoconf automake libtool xz-utils libasound2-dev libbluetooth-dev libdbus-1-dev libglib2.0-dev && \
    rm -rf /var/lib/apt/lists/*
DOCKERFILE

docker run --rm --platform linux/arm64 -v "$CACHE":/cache:ro -v "$OUT":/out "$BA_IMAGE" bash -euo pipefail -c "
  # libsbc, static (see above): only the library, into /tmp/sbc, found through its pkg-config file
  mkdir /tmp/sbc-src && tar xJf /cache/$(basename "$SBC_TARBALL") -C /tmp/sbc-src --strip-components=1
  cd /tmp/sbc-src
  ./configure --prefix=/tmp/sbc --enable-static --disable-shared --disable-tools --disable-tester --with-pic \
    >/tmp/sbc-configure.log 2>&1 || { tail -40 /tmp/sbc-configure.log; exit 1; }
  make -j\$(nproc) install >/tmp/sbc-make.log 2>&1 || { tail -40 /tmp/sbc-make.log; exit 1; }
  export PKG_CONFIG_PATH=/tmp/sbc/lib/pkgconfig
  mkdir /tmp/s && tar xzf /cache/$(basename "$TARBALL") -C /tmp/s --strip-components=1
  cd /tmp/s
  autoreconf --install >/tmp/autoreconf.log 2>&1 || { tail -40 /tmp/autoreconf.log; exit 1; }
  mkdir /tmp/b && cd /tmp/b
  /tmp/s/configure --prefix=/usr --sysconfdir=/etc --localstatedir=/var \
    --disable-aplay --disable-cli --disable-rfcomm --disable-hcitop --disable-manpages --disable-test \
    --disable-aac --disable-aptx --disable-aptx_hd --disable-ldac --disable-msbc --disable-ofono --disable-upower \
    --with-bluealsauser=root >/tmp/configure.log 2>&1 || { tail -40 /tmp/configure.log; exit 1; }
  make -j\$(nproc) >/tmp/make.log 2>&1 || { tail -40 /tmp/make.log; exit 1; }
  strip src/bluealsa
  install -m 0755 src/bluealsa /out/bluealsa
  for k in pcm ctl; do
    so=src/asound/.libs/libasound_module_\${k}_bluealsa.so
    strip --strip-unneeded \$so
    install -m 0644 \$so /out/libasound_module_\${k}_bluealsa.so
  done
  install -m 0644 src/asound/20-bluealsa.conf /out/20-bluealsa.conf
  # Only libraries the image has (never libbsd, which it does not have, nor libsbc: the daemon carries 2.0 inside)
  for f in /out/bluealsa /out/libasound_module_pcm_bluealsa.so /out/libasound_module_ctl_bluealsa.so; do
    if readelf -d \$f | grep 'Shared library' | grep -v -E '\[(libc|libpthread|librt|libm|libdl|libasound|libbluetooth|libdbus-1|libgio-2\.0|libgobject-2\.0|libglib-2\.0)\.so|\[ld-linux-aarch64\.so'; then
      echo \"bluealsa: \$f needs a library the image does not have\" >&2; exit 1
    fi
  done
  # The plugin and the daemon must agree on the D-Bus service (the device strings the engine builds use the default)
  grep -q 'defaults.bluealsa.service \"org.bluealsa\"' /out/20-bluealsa.conf || { echo 'bluealsa: unexpected 20-bluealsa.conf' >&2; exit 1; }
  install -m 0644 /tmp/s/LICENSE /out/LICENSE
  install -m 0644 /tmp/sbc-src/COPYING.LIB /out/LICENSE.sbc
"
printf '%s\nsbc %s\n' "$BLUEALSA_VERSION" "$SBC_VERSION" > "$OUT/bluealsa.version"
echo "bluealsa $BLUEALSA_VERSION → $OUT"
