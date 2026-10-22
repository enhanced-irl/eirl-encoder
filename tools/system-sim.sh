#!/usr/bin/env bash
# tools/system-sim.sh — the system layer through the real update helper, on a SIMULATED board: the root filesystem of the
# published SD card image of 0.7.0 in a container (UPDATES.md §12.13). Never touches a board. Scenarios (tools/ota-sim/
# system.sh): a power cut with the layer's packages half installed and its recovery at boot; the first release with a
# layer installed by an OLDER helper; an app that never starts after the layer was committed (the layer stays); a layer
# whose file does not match it (refused before any change); streaming.
#
#   tools/system-sim.sh [--old-ref REF] [--image OLD.img.xz]
#
#   --old-ref REF   the code of the older release installed first, whose helper runs the first layer (default v0.8.0)
#   --image F       the image whose root filesystem is the board (default: the published image of 0.7.0, verified)
#
# Builds four test releases with a THROWAWAY key (A from REF in a temporary worktree, B, C and E from this tree). Needs
# Go and Docker; ~15 min the first time. Leaves its work directory (releases, logs) and prints it.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
die() { echo "system-sim: $*" >&2; exit 1; }
OLD_REF=v0.8.0 IMAGE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --old-ref) OLD_REF=${2:?}; shift ;;
    --image) IMAGE=$(cd "$(dirname "${2:?}")" && pwd)/$(basename "$2"); shift ;;
    *) die "unknown option $1" ;;
  esac
  shift
done
work=$(mktemp -d "${TMPDIR:-/tmp}/eirl-system-sim.XXXXXX")
go -C manager run ./cmd/eirl-release keygen > "$work/key"
sed -n 's/^seed=//p' "$work/key" > "$work/seed"
pub=$(sed -n 's/^pubkey=//p' "$work/key")
tag=eirl-system-sim-$$
old=$ROOT/../encoder-system-sim-$$
cleanup() {
  docker image rm -f "$tag" >/dev/null 2>&1 || true
  [ -d "$old" ] && git -C "$ROOT" worktree remove --force "$old" >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [ -z "$IMAGE" ]; then
  c=${CACHE:-$HOME/.cache/eirl-image}/published && mkdir -p "$c" && b=eirl-encoder-0.7.0-jetson-nano
  for f in $b.img.xz $b.img.json $b.img.json.sig; do
    [ -f "$c/$f" ] || { curl -fsSL -o "$c/$f.part.$$" "https://github.com/enhanced-irl/eirl-encoder/releases/download/v0.7.0/$f" &&
      mv "$c/$f.part.$$" "$c/$f"; } || die "cannot download $f"
  done
  go -C manager run ./cmd/eirl-release verify-image -manifest "$c/$b.img.json" -image "$c/$b.img.xz" >/dev/null ||
    die "the published image of 0.7.0 does not verify"
  IMAGE=$c/$b.img.xz
fi

echo "== A: $OLD_REF (an older helper, without the system layer), app only"
git -C "$ROOT" worktree add --detach "$old" "$OLD_REF" >/dev/null 2>&1
# The third-party programs (pinned the same) are not built again
mkdir -p "$ROOT/build/out" && rm -rf "$old/build/out" && ln -s "$ROOT/build/out" "$old/build/out"
mkdir -p "$work/rel/A"
"$old/tools/release.sh" build --local "$work/rel/A" --skip-engine --allow-dirty --key-stdin --pubkey "$pub" \
  --version 0.9.90-test.1 < "$work/seed" | tail -1
echo "== B, C (its app never starts), E (its layer has a file that does not match)"
[ -x build/out/eirl-engine ] || EIRL_VERSION=0.10.0-sim build/engine.sh | tail -1
for spec in "B 0.10.0-sim.1 -" "C 0.10.0-sim.2 app" "E 0.10.0-sim.3 system"; do
  set -- $spec
  inj=(); [ "$3" != - ] && inj=(--inject-failure "$3")
  mkdir -p "$work/rel/$1"
  tools/release.sh build --local "$work/rel/$1" --engine-bin build/out/eirl-engine --allow-dirty --key-stdin --pubkey "$pub" \
    --version "$2" ${inj[@]+"${inj[@]}"} < "$work/seed" | tail -1
done
echo "== the board: $(basename "$IMAGE")"
tools/image/import-rootfs.sh "$IMAGE" "$tag"
set +e
docker run --rm --platform linux/arm64 -v "$work:/s" -v "$ROOT/tools/ota-sim:/s/sim:ro" "$tag" /bin/bash /s/sim/system.sh > "$work/run.log" 2>&1
rc=$?
set -e
grep -E '^===|^base:|^A =|helper:|after recover|^cut|^OK|FAIL|ALL OK' "$work/run.log"
echo "log: $work/run.log"
exit $rc
