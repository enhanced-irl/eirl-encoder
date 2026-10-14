#!/usr/bin/env bash
# srtla.sh — compila srtla_send (BELABOX/srtla, C, AGPL-3.0) para la Jetson: aarch64, Ubuntu 18.04
# (glibc 2.27), en el mismo contenedor que el driver (build/Dockerfile). En un Mac con Apple Silicon
# es nativo. Commit fijado y tarball verificado por sha256.
#
#   ./build/srtla.sh        → build/out/srtla_send   (lo empaqueta manager/build.sh)
#
# Un único cambio sobre el original (licencia AGPL: el cambio es este script, ver SRTLA.md): el
# socket donde srtla_send recibe el SRT local escucha en 127.0.0.1 y no en INADDR_ANY. srtla_send
# toma como destino de las respuestas del relay al último que le escribe en ese puerto: abierto a
# todas las interfaces, cualquiera de la LAN o de la red de un módem que le mande un datagrama se
# quedaría con las respuestas y cortaría la emisión.
# Para actualizarlo: cambiar SRTLA_COMMIT y SRTLA_SHA256, comprobar que el parche sigue aplicando y
# probar de nuevo con el arnés de SRTLA.md.
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
OUT="${OUT:-$HERE/build/out}"
IMAGE=l4t-r32.7.6-build
SRTLA_COMMIT=37862da3d0c13b46956efd3f88877053293d97d6   # 2025-04-05, "make the SRTLA socks non-blocking…"
SRTLA_SHA256=12103e82a24a8a92a817bc18dc78b641cf10a75a5e085bbba38897f903e795c0
TARBALL="$CACHE/srtla-$SRTLA_COMMIT.tar.gz"

mkdir -p "$CACHE" "$OUT"
[ -f "$TARBALL" ] || curl -fsSL -o "$TARBALL" "https://codeload.github.com/BELABOX/srtla/tar.gz/$SRTLA_COMMIT"
echo "$SRTLA_SHA256  $TARBALL" | shasum -a 256 -c - >/dev/null || { echo "srtla: sha256 no coincide ($TARBALL)" >&2; exit 1; }
docker build -q -t "$IMAGE" "$HERE/build" >/dev/null

docker run --rm --platform linux/arm64 -v "$CACHE":/cache:ro -v "$OUT":/out "$IMAGE" bash -euo pipefail -c "
  mkdir /tmp/s && tar xzf /cache/$(basename "$TARBALL") -C /tmp/s --strip-components=1
  sed -i 's/listen_addr.sin_addr.s_addr = INADDR_ANY;/listen_addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);/' /tmp/s/srtla_send.c
  [ \$(grep -c 'listen_addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);' /tmp/s/srtla_send.c) = 1 ] || { echo 'srtla: el parche de 127.0.0.1 no aplica' >&2; exit 1; }
  make -s -C /tmp/s srtla_send VERSION=${SRTLA_COMMIT:0:7}
  strip /tmp/s/srtla_send && install -m 0755 /tmp/s/srtla_send /out/srtla_send
  file /out/srtla_send
"
