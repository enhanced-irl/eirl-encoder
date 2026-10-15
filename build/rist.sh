#!/usr/bin/env bash
# rist.sh — builds ristsender from librist (VideoLAN, C, BSD-2-Clause) for the Jetson: aarch64, Ubuntu 18.04
# (glibc 2.27), in the same container as srtla_send and SRT (build/Dockerfile) plus meson from pip (18.04's
# meson 0.45 is older than librist's minimum, 0.51). Also ristreceiver and ristsrppasswd, for tests only.
# Pinned release and tarball verified by sha256; the source is not modified.
#
#   ./build/rist.sh      → build/out/rist/{ristsender,ristreceiver,ristsrppasswd,rist.version,LICENSE*}
#
# Self-contained binaries: librist is linked statically, with the library's bundled copies of mbedtls
# (Apache-2.0: SRP authentication for the main profile, and the AES of the pre-shared key), cJSON (MIT) and
# LZ4 (BSD-2-Clause). So the package carries one file, /usr/local/bin/ristsender, that needs nothing but
# glibc (2.17-2.25 symbols): no librist.so, no RUNPATH, no ldconfig. The licences go next to it.
# Source: the release's archive on code.videolan.org (its SHA512 is the one in the release notes); the
# tarball is cached under ~/.cache like the others. To update it: change RIST_VERSION and RIST_SHA256,
# capture ristsender's lines again (manager/internal/engine/output/rist_test.go says how) and run its tests.
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
OUT="${OUT:-$HERE/build/out}/rist"
IMAGE=l4t-r32.7.6-build
MESON_IMAGE=l4t-r32.7.6-build-meson
MESON_VERSION=0.61.5   # the last meson that runs on 18.04's Python 3.6
RIST_VERSION=v0.2.20   # 2026-07-30 (code.videolan.org/rist/librist, tag v0.2.20)
RIST_SHA256=9e40eeb87f014790531ad41326cc271b930a65962e4b15231b301fc59b29fe31
TARBALL="$CACHE/librist-$RIST_VERSION.tar.gz"

mkdir -p "$CACHE" "$OUT"
if [ ! -f "$TARBALL" ]; then
  curl -fsSL -o "$TARBALL.part" "https://code.videolan.org/rist/librist/-/archive/$RIST_VERSION/librist-$RIST_VERSION.tar.gz"
  mv "$TARBALL.part" "$TARBALL"
fi
echo "$RIST_SHA256  $TARBALL" | shasum -a 256 -c - >/dev/null || { echo "rist: sha256 mismatch ($TARBALL)" >&2; exit 1; }
docker build -q -t "$IMAGE" "$HERE/build" >/dev/null
docker build -q -t "$MESON_IMAGE" - >/dev/null <<DOCKERFILE
FROM $IMAGE
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      python3-pip python3-setuptools ninja-build && rm -rf /var/lib/apt/lists/* && \
    pip3 install --no-cache-dir meson==$MESON_VERSION
DOCKERFILE

docker run --rm --platform linux/arm64 -v "$CACHE":/cache:ro -v "$OUT":/out "$MESON_IMAGE" bash -euo pipefail -c "
  mkdir /tmp/s && tar xzf /cache/$(basename "$TARBALL") -C /tmp/s --strip-components=1
  cd /tmp/s
  meson setup /tmp/b --buildtype=release --default-library=static -Dtest=false -Dbuilt_tools=true \
    -Duse_mbedtls=true -Dbuiltin_mbedtls=true -Dbuiltin_cjson=true -Dbuiltin_lz4=true \
    -Dfallback_builtin=true >/tmp/meson.log 2>&1 || { tail -40 /tmp/meson.log; exit 1; }
  ninja -C /tmp/b tools/ristsender tools/ristreceiver tools/ristsrppasswd >/tmp/ninja.log 2>&1 || { tail -40 /tmp/ninja.log; exit 1; }
  for b in ristsender ristreceiver ristsrppasswd; do
    strip /tmp/b/tools/\$b
    # Self-contained: glibc only, never librist.so or a crypto library from the system
    if readelf -d /tmp/b/tools/\$b | grep 'Shared library' | grep -v -E '\[(libc|libpthread|librt|libm|libdl)\.so|\[ld-linux-aarch64\.so'; then
      echo \"rist: \$b needs a library beyond glibc\" >&2; exit 1
    fi
    install -m 0755 /tmp/b/tools/\$b /out/\$b
  done
  # SRP (main profile) is only built with mbedtls: without it ristsender has no --srpfile
  h=\$(/out/ristsender -h 2>&1 || true)
  echo \"\$h\" | grep -q -- '--srpfile' || { echo 'rist: ristsender built without SRP' >&2; exit 1; }
  install -m 0644 COPYING /out/LICENSE
  install -m 0644 contrib/mbedtls/LICENSE /out/LICENSE.mbedtls
  sed -n '1,/\\*\\//p' contrib/contrib_cJSON/cjson/cJSON.h > /out/LICENSE.cJSON
  sed -n '1,/\\*\\//p' contrib/lz4/lz4.c > /out/LICENSE.lz4
  echo '$RIST_VERSION' > /out/rist.version
  echo \"\$h\" | grep -q 'libRIST library: ${RIST_VERSION#v} ' || { echo \"rist: unexpected version: \$h\" >&2; exit 1; }
  file /out/ristsender && ls -l /out/ristsender
"
echo "build/out/rist/ristsender, build/out/rist/ristreceiver: librist $RIST_VERSION"
