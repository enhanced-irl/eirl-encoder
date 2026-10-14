#!/usr/bin/env bash
# tools/ota-sim.sh — the over-the-air update on a SIMULATED board (UPDATES.md §9): an Ubuntu 18.04 arm64
# container (the image of build/Dockerfile) runs the real install.sh, eirl-encoderd and eirl-update with a
# fake systemctl and sudo (tools/ota-sim/shims). Scenarios (tools/ota-sim/run.sh): first install by hand,
# a healthy update through the web API, rollback when the new app never starts, rollback when install.sh
# fails, refusal while streaming (app and helper), manual rollback, recovery of an interrupted install; then,
# with two releases that carry the engine, an engine install, its rollback and a manual rollback of the
# engine, checking every time how many times the (emulated) engine was restarted.
#   tools/ota-sim.sh               → builds the test releases with a THROWAWAY key and runs it (~6 min: the
#                                    Go engine is built once in Docker, build/engine.sh)
#   tools/ota-sim.sh --no-engine   → only the app-only releases and scenarios (~2 min)
# Needs Go and Docker. Never touches a board. Leaves its work directory (releases, logs) and prints it.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/eirl-ota-sim.XXXXXX")
go -C manager run ./cmd/eirl-release keygen > "$work/key"
sed -n 's/^seed=//p' "$work/key" > "$work/seed"
pub=$(sed -n 's/^pubkey=//p' "$work/key")
for spec in "A 0.1.1-test.1 -" "B 0.1.1-test.2 -" "C 0.1.1-test.3 app" "D 0.1.1-test.4 install"; do
  set -- $spec
  inj=(); [ "$3" != - ] && inj=(--inject-failure "$3")
  mkdir -p "$work/rel/$1"
  tools/release.sh build --local "$work/rel/$1" --skip-engine --allow-dirty --key-stdin --pubkey "$pub" --version "$2" \
    ${inj[@]+"${inj[@]}"} < "$work/seed" | tail -1
done
if [ "${1:-}" != --no-engine ]; then
  # Releases that carry the engine: the Go engine is built once (Docker) and reused
  EIRL_VERSION=0.1.1-test.5 build/engine.sh | tail -1
  cp build/out/eirl-engine "$work/eirl-engine"
  for spec in "E 0.1.1-test.5 -" "F 0.1.1-test.6 app"; do
    set -- $spec
    inj=(); [ "$3" != - ] && inj=(--inject-failure "$3")
    mkdir -p "$work/rel/$1"
    tools/release.sh build --local "$work/rel/$1" --engine-bin "$work/eirl-engine" --allow-dirty --key-stdin --pubkey "$pub" \
      --version "$2" ${inj[@]+"${inj[@]}"} < "$work/seed" | tail -1
  done
fi
docker image inspect l4t-r32.7.6-build >/dev/null 2>&1 || docker build -q -t l4t-r32.7.6-build build >/dev/null
set +e
docker run --rm --platform linux/arm64 -v "$work:/s" -v "$PWD/tools/ota-sim:/s/sim:ro" l4t-r32.7.6-build bash /s/sim/run.sh > "$work/run.log" 2>&1
rc=$?
set -e
grep -E '^===|helper:|FAIL|ALL OK|after recover|install while|helper exit|files.txt|config dir|PUT /device|engine restarts|^engine:|retired Python' "$work/run.log"
echo "log: $work/run.log"
exit $rc
