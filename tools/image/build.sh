#!/usr/bin/env bash
# tools/image/build.sh — the encoder's microSD image for the Jetson Nano Developer Kit (B01), from NVIDIA's
# L4T R32.7.6 BSP (IMAGE.md). Runs on a machine with Docker (macOS on Apple Silicon or Linux); every step runs
# in containers, from inputs pinned in tools/image/pins.sh. Nothing is installed on the machine.
#
#   tools/image/build.sh --release DIR [--pubkey B64] [--test | --debug-ssh-key FILE] [--out DIR] [--keep]
#
#   --release DIR        a release: DIR/manifest.json, manifest.json.sig and the bundle they name
#                        (tools/release.sh build, or build --local for a test release). Verified before use.
#   --pubkey B64         the key that release is signed with (default: the production key, release/eirl-release.pub)
#   --test               a test image: the board's firmware (QSPI) is only checked, never written. Named …-test
#   --debug-ssh-key F    a test image with a key-only eirl-debug user and ssh on (F = a public key). Named …-debug.
#                        Never signed, never published
#   --out DIR            where the image goes (default build/out/image)
#   --keep               keep the work volume (the root filesystem, the raw image) for a look afterwards
#   --update-lock        accept the packages the build installs as the new tools/image/packages.lock
#
# Needs ~12 GB free in Docker's disk. The work happens in a Docker volume (eirl-image-<pid>): macOS's
# filesystem is not case-sensitive and cannot hold a Linux root filesystem.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
# shellcheck source=pins.sh
. "$HERE/pins.sh"
die() { echo "image: $*" >&2; exit 1; }

RELEASE="" PUBKEY="" MODE=product DEBUG_KEY="" OUT="$ROOT/build/out/image" KEEP="" UPDATE_LOCK=""
CACHE=${CACHE:-$HOME/.cache/eirl-image}
while [ $# -gt 0 ]; do
  case "$1" in
    --release) RELEASE=$(cd "${2:?}" && pwd); shift ;;
    --pubkey) PUBKEY=${2:?}; shift ;;
    --test) [ "$MODE" = debug ] || MODE=test ;;
    --debug-ssh-key) DEBUG_KEY=$(cd "$(dirname "${2:?}")" && pwd)/$(basename "$2"); MODE=debug; shift ;;
    --out) mkdir -p "${2:?}"; OUT=$(cd "$2" && pwd); shift ;;
    --keep) KEEP=1 ;;
    --update-lock) UPDATE_LOCK=1 ;;
    -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option $1" ;;
  esac
  shift
done
[ -n "$RELEASE" ] || die "--release DIR is required"
[ "$MODE" != product ] || [ -z "$UPDATE_LOCK" ] || die "--update-lock is for test builds: a product image uses the reviewed locks"
[ -z "$DEBUG_KEY" ] || [ -f "$DEBUG_KEY" ] || die "no such key: $DEBUG_KEY"
command -v docker >/dev/null || die "Docker is needed"
command -v go >/dev/null || die "Go is needed (to verify the release with eirl-release)"

# --- 1. The release, verified like a board does
tool=$(mktemp -d); trap 'rm -rf "$tool"' EXIT
(cd "$ROOT/manager" && go build -o "$tool/eirl-release" ./cmd/eirl-release)
VERSION=$(python3 -c 'import json,sys; m=json.load(open(sys.argv[1])); print(m["version"])' "$RELEASE/manifest.json")
BUNDLE=$(python3 -c 'import json,sys; m=json.load(open(sys.argv[1])); print(m["bundle"]["name"])' "$RELEASE/manifest.json")
"$tool/eirl-release" verify -manifest "$RELEASE/manifest.json" -sig "$RELEASE/manifest.json.sig" \
  -bundle "$RELEASE/$BUNDLE" ${PUBKEY:+-pubkey "$PUBKEY"}
if [ "$MODE" = product ]; then
  [ -z "$PUBKEY" ] || [ "$PUBKEY" = "$(tr -d ' \n' < "$ROOT/release/eirl-release.pub")" ] ||
    die "a product image carries a release signed with the production key: use --test with a test release"
fi
case "$MODE" in
  product) NAME="eirl-encoder-$VERSION-jetson-nano"; FIRMWARE_MODE=update ;;
  test) NAME="eirl-encoder-$VERSION-jetson-nano-test"; FIRMWARE_MODE=check ;;
  debug) NAME="eirl-encoder-$VERSION-jetson-nano-debug"; FIRMWARE_MODE=check ;;
esac
echo "== image $NAME ($MODE) with release $VERSION"

# --- 2. Our kernel bits (build/build.sh: the patched tc358743 module and the B01 DTB with the TC358743)
DTB=tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb
KOUT="$ROOT/build/out"
if [ ! -f "$KOUT/tc358743.ko" ] || [ ! -f "$KOUT/$DTB" ] || ! ls "$KOUT"/modules/*.ko >/dev/null 2>&1; then
  "$ROOT/build/build.sh"
fi

# --- 3. Downloads, pinned by sha256
mkdir -p "$CACHE"
fetch() { # url file sha256
  [ -f "$CACHE/$2" ] || { echo "== downloading $2"; curl -fL --retry 3 -o "$CACHE/$2.part" "$1" && mv "$CACHE/$2.part" "$CACHE/$2"; }
  echo "$3  $CACHE/$2" | shasum -a 256 -c - >/dev/null || die "sha256 of $2 does not match pins.sh"
}
fetch "$BSP_URL" "$BSP_FILE" "$BSP_SHA256"
fetch "$UBUNTU_BASE_URL" "$UBUNTU_BASE_FILE" "$UBUNTU_BASE_SHA256"

# --- 4. Tool containers (rebuilt every run: other work on this machine may prune Docker images)
docker build -q --platform linux/arm64 --build-arg TOOLS_BASE="$TOOLS_BASE" -t eirl-image-tools:arm64 -f "$HERE/Dockerfile" "$HERE" >/dev/null
docker build -q --platform linux/amd64 --build-arg TOOLS_BASE="$TOOLS_BASE" -t eirl-image-tools:amd64 -f "$HERE/boot.Dockerfile" "$HERE" >/dev/null
VOL=eirl-image-$$
docker volume create "$VOL" >/dev/null
cleanup() { rm -rf "$tool"; [ -n "$KEEP" ] && echo "== work volume kept: $VOL (docker volume rm $VOL)" || docker volume rm -f "$VOL" >/dev/null; }
trap cleanup EXIT
free=$(docker run --rm -v "$VOL:/work" eirl-image-tools:arm64 df -BG --output=avail /work | tail -1 | tr -dc 0-9)
[ "${free:-0}" -ge 12 ] || die "only ${free} GB free in Docker's disk (12 needed)"
mkdir -p "$OUT"
ENVS=(-e BSP_FILE="$BSP_FILE" -e UBUNTU_BASE_FILE="$UBUNTU_BASE_FILE" -e KERNEL_RELEASE="$KERNEL_RELEASE"
      -e IMAGE_VERSION="$VERSION" -e FIRMWARE_MODE="$FIRMWARE_MODE" -e BUNDLE="$BUNDLE" -e IMAGE_NAME="$NAME"
      -e DEBUG="$([ "$MODE" = debug ] && echo 1 || echo 0)")
MOUNTS=(-v "$VOL:/work" -v "$CACHE:/in/dl:ro" -v "$ROOT:/in/repo:ro" -v "$KOUT:/in/kernel:ro" -v "$RELEASE:/in/release:ro")
[ "$MODE" = debug ] && MOUNTS+=(-v "$DEBUG_KEY:/in/debug.pub:ro")

# --- 5. The card's boot partitions: NVIDIA's flash.sh --no-flash --sign, in linux/amd64
docker run --rm --platform linux/amd64 "${ENVS[@]}" "${MOUNTS[@]}" eirl-image-tools:amd64 bash /in/repo/tools/image/stages/bootparts.sh

# --- 6. The root filesystem, in a native arm64 chroot (mounts /proc, /sys, /dev into it; mknod for NVIDIA's debs)
docker run --rm --platform linux/arm64 --cap-add SYS_ADMIN --cap-add MKNOD --security-opt apparmor=unconfined \
  "${ENVS[@]}" "${MOUNTS[@]}" eirl-image-tools:arm64 bash /in/repo/tools/image/stages/rootfs.sh

# --- 7. Assembly and checks (no privileges)
docker run --rm --platform linux/arm64 "${ENVS[@]}" "${MOUNTS[@]}" -v "$OUT:/out" eirl-image-tools:arm64 \
  bash /in/repo/tools/image/stages/assemble.sh
mkdir -p "$OUT/$NAME.report"
docker run --rm --platform linux/arm64 --cap-add SYS_CHROOT -e LC_ALL=C.UTF-8 -e PYTHONIOENCODING=utf-8 "${ENVS[@]}" "${MOUNTS[@]}" -v "$OUT:/out" \
  -v "$HERE:/in/lockdir" eirl-image-tools:arm64 python3 -I /in/repo/tools/image/stages/check.py \
  --rootfs /work/rootfs --image /work/out/assemble/image.img --bootparts /work/out/bootparts \
  --release /in/release --bundle "$BUNDLE" --version "$VERSION" --mode "$MODE" --repo /in/repo \
  --lock /in/lockdir/packages.lock --bootparts-lock /in/lockdir/bootparts.lock \
  --report-dir "/out/$NAME.report" ${UPDATE_LOCK:+--update-lock} || CHECK_FAILED=1
docker run --rm -v "$VOL:/work" -v "$OUT:/out" eirl-image-tools:arm64 sh -c \
  "cp /work/out/raw.txt /work/out/bootparts/layout.tsv /out/$NAME.report/ && cp /work/out/bootparts/flash.log /out/$NAME.report/bootparts-flash.log"
if [ -n "${CHECK_FAILED:-}" ]; then
  # A failed image must never look publishable: no hash, and a name that says so
  mv -f "$OUT/$NAME.img.xz" "$OUT/$NAME.img.xz.FAILED"; rm -f "$OUT/$NAME.img.xz.sha256"
  die "checks FAILED: $OUT/$NAME.img.xz.FAILED; see $OUT/$NAME.report/check.json"
fi
( cd "$OUT" && shasum -a 256 "$NAME.img.xz" > "$NAME.img.xz.sha256" )
echo "== $OUT/$NAME.img.xz ($(du -h "$OUT/$NAME.img.xz" | cut -f1)), report in $OUT/$NAME.report/"
