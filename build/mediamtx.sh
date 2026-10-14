#!/usr/bin/env bash
# mediamtx.sh — trae el binario oficial de MediaMTX para la Jetson (linux/arm64, estático, MIT) con
# versión y sha256 fijados. Recibe las cámaras de red por el punto de acceso (NETWORK_SOURCES.md §6.3).
#
#   ./build/mediamtx.sh     → build/out/mediamtx   (lo empaqueta manager/build.sh)
#
# Para actualizarlo: cambiar MEDIAMTX_VERSION y MEDIAMTX_SHA256 (el del tarball, de su
# checksums.sha256), revisar las claves nuevas de su mediamtx.yml (la 1.21 trajo MoQ encendido por
# defecto: netcfg.MediaMTXConfig lo apaga) y probar de nuevo con el móvil.
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/eirl-encoder}"
OUT="${OUT:-$HERE/build/out}"
MEDIAMTX_VERSION=v1.21.1   # 2026-09-20; la de los relays de dev es la 1.20.1
MEDIAMTX_SHA256=6a3aa635fb60ea9b8d566ec306f0a42ff1b6b52a3942bc2baffbe55880d4c3dd
TARBALL="$CACHE/mediamtx_${MEDIAMTX_VERSION}_linux_arm64.tar.gz"

mkdir -p "$CACHE" "$OUT"
[ -f "$TARBALL" ] || curl -fsSL -o "$TARBALL" \
  "https://github.com/bluenviron/mediamtx/releases/download/$MEDIAMTX_VERSION/mediamtx_${MEDIAMTX_VERSION}_linux_arm64.tar.gz"
echo "$MEDIAMTX_SHA256  $TARBALL" | shasum -a 256 -c - >/dev/null || { echo "mediamtx: sha256 no coincide ($TARBALL)" >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tar xzf "$TARBALL" -C "$tmp" mediamtx LICENSE
install -m 0755 "$tmp/mediamtx" "$OUT/mediamtx"
install -m 0644 "$tmp/LICENSE" "$OUT/mediamtx.LICENSE"
echo "$MEDIAMTX_VERSION" > "$OUT/mediamtx.version"
echo "build/out/mediamtx $MEDIAMTX_VERSION"
