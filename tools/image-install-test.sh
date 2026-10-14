#!/usr/bin/env bash
# tools/image-install-test.sh — a release bundle's install.sh in IMAGE mode (install.sh --image / EIRL_IMAGE=1),
# as the SD card image build runs it: in an arm64 chroot with /proc, /sys and /dev mounted, no systemd running in it
# and no network. An Ubuntu 18.04 arm64 container (privileged, for the mounts; --network none) is the build machine
# and a copy of its root is the image (tools/image-install-test/run.sh). It checks that:
#   - nothing that needs a running system runs (daemon-reload, start/restart/stop, udevadm, systemd-tmpfiles, the
#     NetworkManager dispatcher): guards log them as forbidden;
#   - every path of files.txt is there, with srt-live-transmit (it runs, with the package's libsrt), the EDIDs and
#     the licences; the users and groups; a new device's settings (output off); the units enabled for the
#     first boot; the splash boot entry; eirl-encoderd -print-hardware-id;
#   - a second run changes nothing (type, mode, owner and content of every path), and --image is refused where
#     systemd runs that root.
#
#   tools/image-install-test.sh BUNDLE.tar.gz   → that bundle (e.g. build/out/release/<version>/eirl-encoder-…tar.gz)
#   tools/image-install-test.sh                 → first builds a local test release with the engine (THROWAWAY key)
# Needs Docker (and Go for the build). Never touches a board. Leaves its work directory and prints it.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/eirl-image-test.XXXXXX")
if [ $# -gt 0 ]; then
  cp "$1" "$work/bundle.tar.gz"
else
  go -C manager run ./cmd/eirl-release keygen > "$work/key"
  sed -n 's/^seed=//p' "$work/key" > "$work/seed"
  pub=$(sed -n 's/^pubkey=//p' "$work/key")
  tools/release.sh build --local "$work/rel" --allow-dirty --key-stdin --pubkey "$pub" --version 0.1.1-image.1 < "$work/seed" | tail -1
  rm -f "$work/seed" "$work/key"
  cp "$work"/rel/eirl-encoder-*-linux-arm64.tar.gz "$work/bundle.tar.gz"
fi
# The image's root before the bundle: what install.sh uses from the operating system (L4T has all of it)
docker build -q --platform linux/arm64 -t eirl-image-install-test - >/dev/null <<'DOCKER'
FROM ubuntu:18.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd sudo python3 libssl1.1 && rm -rf /var/lib/apt/lists/*
DOCKER
set +e
docker run --rm --privileged --platform linux/arm64 --network none -v "$work:/s" -v "$PWD/tools/image-install-test:/s/sim:ro" \
  eirl-image-install-test bash /s/sim/run.sh > "$work/run.log" 2>&1
rc=$?
set -e
cat "$work/run.log"
echo "log: $work/run.log"
exit $rc
