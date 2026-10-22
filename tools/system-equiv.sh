#!/usr/bin/env bash
# tools/system-equiv.sh — does a board flashed with an older image reach, over the air, the same system as a board
# flashed with the image of the new release? (UPDATES.md §12.10). Never touches a board.
#
#   tools/system-equiv.sh --release DIR (--image NEW.img.xz | --report DIR) [--from OLD.img.xz]… [--pubkey B64]
#
#   --release DIR   the new release (manifest.json, .sig and its bundle: tools/release.sh build [--local])
#   --image F       the new release's image (tools/image/build.sh): its root filesystem's fingerprint, or
#   --report DIR    the report tools/image/build.sh wrote next to it (its fingerprint.json)
#   --from F        an image a board may still run (default: every published one, downloaded from GitHub Releases and
#                   verified with the release key): its root filesystem, in a container, gets the new release's bundle
#                   exactly as the update helper runs it (install.sh, EIRL_OTA=1; the helper's own path is
#                   tools/system-sim.sh's), twice (the second must change nothing), and its fingerprint must be the new
#                   image's but for tools/system-equiv.expected
#   --pubkey B64    the key the release is signed with (a test release); the images are always checked with the
#                   production key
#
# The fingerprint (eirl-update system fingerprint): packages and versions, our modules and modules.dep, firmware,
# enabled and masked units, the system layer's files, groups and users, /boot. Needs Docker (arm64) and Go. ~5 min
# per image. Leaves its work in a temporary directory and prints it.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
die() { echo "equiv: $*" >&2; exit 1; }
REPO=enhanced-irl/eirl-encoder
PUBLISHED="0.7.0 0.8.0"   # every release with an SD card image (IMAGE.md §0)

RELEASE="" IMAGE="" REPORT="" PUBKEY="" FROM=()
while [ $# -gt 0 ]; do
  case "$1" in
    --release) RELEASE=$(cd "${2:?}" && pwd); shift ;;
    --image) IMAGE=$(cd "$(dirname "${2:?}")" && pwd)/$(basename "$2"); shift ;;
    --report) REPORT=$(cd "${2:?}" && pwd); shift ;;
    --from) FROM+=("$(cd "$(dirname "${2:?}")" && pwd)/$(basename "$2")"); shift ;;
    --pubkey) PUBKEY=${2:?}; shift ;;
    *) die "unknown option $1" ;;
  esac
  shift
done
[ -n "$RELEASE" ] || die "--release DIR is required"
[ -n "$IMAGE$REPORT" ] || die "--image or --report is required"
work=$(mktemp -d "${TMPDIR:-/tmp}/eirl-equiv.XXXXXX")
tool=$work/eirl-release
go -C manager build -o "$tool" ./cmd/eirl-release
VERSION=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$RELEASE/manifest.json")
BUNDLE=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["bundle"]["name"])' "$RELEASE/manifest.json")
"$tool" verify -manifest "$RELEASE/manifest.json" -sig "$RELEASE/manifest.json.sig" -bundle "$RELEASE/$BUNDLE" \
  ${PUBKEY:+-pubkey "$PUBKEY"} >/dev/null || die "the release does not verify"

# The published images (default), verified as a user would (IMAGE.md §8.4)
if [ ${#FROM[@]} -eq 0 ]; then
  for v in $PUBLISHED; do
    b=eirl-encoder-$v-jetson-nano
    c=${CACHE:-$HOME/.cache/eirl-image}/published
    mkdir -p "$c"
    for f in $b.img.xz $b.img.json $b.img.json.sig; do
      [ -f "$c/$f" ] || { curl -fsSL -o "$c/$f.part.$$" "https://github.com/$REPO/releases/download/v$v/$f" && mv "$c/$f.part.$$" "$c/$f"; } ||
        die "cannot download $f"
    done
    "$tool" verify-image -manifest "$c/$b.img.json" -image "$c/$b.img.xz" >/dev/null || die "the published image $v does not verify"
    FROM+=("$c/$b.img.xz")
  done
fi

TAGS=()
cleanup() { for t in ${TAGS[@]+"${TAGS[@]}"}; do docker image rm -f "$t" >/dev/null 2>&1 || true; done; }
trap cleanup EXIT
# The bundle's helper and layer (for the fingerprints' file list and the comparison)
mkdir -p "$work/bundle" && tar -xzf "$RELEASE/$BUNDLE" -C "$work/bundle" system/layer.json eirl-update
[ -f "$work/bundle/system/layer.json" ] || die "the release has no system layer"
run() { docker run --rm --platform linux/arm64 -v "$work:/out" "$@"; }

if [ -n "$IMAGE" ]; then
  echo "== the new image: $(basename "$IMAGE")"
  t=eirl-equiv-$$-new && TAGS+=("$t")
  tools/image/import-rootfs.sh "$IMAGE" "$t" >/dev/null
  run "$t" /out/bundle/eirl-update system fingerprint -layer /out/bundle/system -out /out/fp-new.json
else
  cp "$REPORT/fingerprint.json" "$work/fp-new.json"
fi

rc=0
for img in "${FROM[@]}"; do
  n=$(basename "$img" .img.xz)
  echo "== $n → $VERSION over the air"
  t=eirl-equiv-$$-$(tr -dc a-z0-9 <<<"$n" | tail -c 20) && TAGS+=("$t")
  tools/image/import-rootfs.sh "$img" "$t" >/dev/null
  # The bundle as the helper runs it: install.sh with EIRL_OTA=1, the fake systemctl, sudo and udevadm of
  # tools/ota-sim (systemd does not run in a container; unit files change through systemd's systemctl, offline), twice
  set +e
  run -v "$RELEASE:/rel:ro" -v "$ROOT/tools/ota-sim/shims:/shims:ro" "$t" /bin/bash -c '
      set -e
      [ -x /bin/systemctl ] && mv /bin/systemctl /bin/systemctl.real
      cp /shims/* /bin/
      mkdir -p /var/log /tmp/b && tar -xzf "/rel/$1" -C /tmp/b
      cd /tmp/b
      export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
      EIRL_OTA=1 ./install.sh > "/out/install-$2.log" 2>&1 || { echo "install.sh failed"; tail -30 "/out/install-$2.log"; exit 1; }
      EIRL_OTA=1 ./install.sh > "/out/install-$2-again.log" 2>&1 || { echo "install.sh failed the second time"; exit 1; }
      grep -q "system layer: up to date" "/out/install-$2-again.log" || { echo "the second run changed the system"; exit 1; }
      cp /var/lib/eirl-system/state.json "/out/state-$2.json"
      /usr/local/sbin/eirl-update system fingerprint -layer /tmp/b/system -out "/out/fp-$2.json"
    ' _ "$BUNDLE" "$n"
  r=$?
  set -e
  if [ $r -ne 0 ]; then
    echo "FAIL: $n: the update did not go through (logs in $work)"
    rc=1
    continue
  fi
  grep -E 'system layer (applied|skipped)|nothing to change' "$work/install-$n.log" | sed 's/^/   /' | head -3
  echo "   drift: $(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("drift") or "none")' "$work/state-$n.json")"
  if run -v "$ROOT/tools:/tools:ro" "$t" /out/bundle/eirl-update system compare -a /out/fp-new.json -b "/out/fp-$n.json" \
      -expected /tools/system-equiv.expected > "$work/compare-$n.txt"; then
    echo "   OK: $(head -1 "$work/compare-$n.txt")"
  else
    echo "FAIL: $n differs from the new image:"
    sed 's/^/   /' "$work/compare-$n.txt"
    rc=1
  fi
done
echo "work: $work"
[ $rc -eq 0 ] && echo "ALL OK: every image reaches the system of $VERSION over the air"
exit $rc
