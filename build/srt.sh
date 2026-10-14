#!/usr/bin/env bash
# srt.sh — builds srt-live-transmit and libsrt (Haivision SRT, C++, MPL-2.0) for the Jetson: aarch64, Ubuntu
# 18.04 (glibc 2.27, OpenSSL 1.1), in the same container as the driver and srtla_send (build/Dockerfile).
# The engine's SRT output pipes the program into srt-live-transmit (README, "Operating"). Pinned tag and
# tarball verified by sha256; the source is not modified.
#
#   ./build/srt.sh       → build/out/srt-live-transmit, build/out/libsrt.so.1.5   (manager/build.sh packages them)
#
# The same version and options as the test board's hand-built copy (/usr/local, v1.5.7: Release, shared,
# encryption with OpenSSL EVP, no bonding). One difference: it is installed as the encoder's own copy,
# /usr/local/bin/srt-live-transmit with its library in /usr/lib/eirl-encoder/lib/libsrt.so.1.5 (the file
# carries its soname), found through the binary's RUNPATH. So no ldconfig and no /usr/local/lib: the OTA
# helper backs up, installs and rolls back both files, and a libsrt someone built by hand in /usr/local/lib
# is never picked up by our binary (nor ours by theirs).
# To update it: change SRT_VERSION, SRT_COMMIT and SRT_SHA256 (the codeload tarball of that commit), keep
# SRT_SONAME in step with it and test a stream on the board (SRT and SRTLA).
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
OUT="${OUT:-$HERE/build/out}"
IMAGE=l4t-r32.7.6-build
SRT_VERSION=v1.5.7
SRT_COMMIT=899348d8318eb9a3c5a5b6ec43c4a1114288773a   # tag v1.5.7, "Change to version 1.5.7"
SRT_SHA256=82050d96a1a55d54b423fed4ad80ddb6f30b31bf33e317ffd6726c91ee9a6170
SRT_SONAME=libsrt.so.1.5
RUNPATH=/usr/lib/eirl-encoder/lib
TARBALL="$CACHE/srt-$SRT_COMMIT.tar.gz"

mkdir -p "$CACHE" "$OUT"
[ -f "$TARBALL" ] || curl -fsSL -o "$TARBALL" "https://codeload.github.com/Haivision/srt/tar.gz/$SRT_COMMIT"
echo "$SRT_SHA256  $TARBALL" | shasum -a 256 -c - >/dev/null || { echo "srt: sha256 mismatch ($TARBALL)" >&2; exit 1; }
docker build -q -t "$IMAGE" "$HERE/build" >/dev/null

docker run --rm --platform linux/arm64 -v "$CACHE":/cache:ro -v "$OUT":/out "$IMAGE" bash -euo pipefail -c "
  mkdir /tmp/s /tmp/b && tar xzf /cache/$(basename "$TARBALL") -C /tmp/s --strip-components=1
  cd /tmp/b
  cmake /tmp/s -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local \
    -DENABLE_SHARED=ON -DENABLE_STATIC=OFF -DENABLE_APPS=ON -DENABLE_EXAMPLES=OFF -DENABLE_UNITTESTS=OFF \
    -DENABLE_ENCRYPTION=ON -DUSE_ENCLIB=openssl-evp -DENABLE_BONDING=OFF -DENABLE_LOGGING=ON \
    -DCMAKE_INSTALL_RPATH=$RUNPATH >/tmp/cmake.log 2>&1 || { tail -30 /tmp/cmake.log; exit 1; }
  make -s -j\$(nproc) >/tmp/make.log 2>&1 || { tail -30 /tmp/make.log; exit 1; }
  make -s install DESTDIR=/tmp/i >/dev/null
  lib=\$(readlink -f /tmp/i/usr/local/lib/$SRT_SONAME)
  bin=/tmp/i/usr/local/bin/srt-live-transmit
  strip --strip-unneeded \"\$lib\" && strip \"\$bin\"
  # The binary must look for exactly this library, there; the library must carry that soname
  readelf -d \"\$bin\" | grep -q 'Library runpath: \[$RUNPATH\]' || { echo 'srt: srt-live-transmit has no RUNPATH $RUNPATH' >&2; readelf -d \"\$bin\" >&2; exit 1; }
  readelf -d \"\$bin\" | grep -q 'Shared library: \[$SRT_SONAME\]' || { echo 'srt: srt-live-transmit does not need $SRT_SONAME' >&2; exit 1; }
  readelf -d \"\$lib\" | grep -q 'Library soname: \[$SRT_SONAME\]' || { echo 'srt: the library is not $SRT_SONAME' >&2; exit 1; }
  install -m 0755 \"\$bin\" /out/srt-live-transmit
  install -m 0644 \"\$lib\" /out/$SRT_SONAME
  install -m 0644 /tmp/s/LICENSE /out/srt.LICENSE
  echo '$SRT_VERSION $SRT_COMMIT' > /out/srt.version
  v=\$(LD_LIBRARY_PATH=/out /out/srt-live-transmit -version 2>&1 || true)
  echo \"\$v\" | grep -q 'SRT Library version: ${SRT_VERSION#v}' || { echo \"srt: unexpected version: \$v\" >&2; exit 1; }
"
echo "build/out/srt-live-transmit, build/out/$SRT_SONAME: SRT $SRT_VERSION"
