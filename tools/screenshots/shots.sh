#!/usr/bin/env bash
# shots.sh: redraws the README's screenshots (docs/screenshots/) without a board. They are synthetic on purpose:
# the touch screen is drawn by eirl-ui-render with mixer.json (an English state) and illustrated pictures in
# place of the cameras (scenes.html, -frames), and the web page is the real one, served by eirl-encoder-sim in
# local mode, with the same pictures. Nothing here talks to a device or to the cloud.
#
#   tools/screenshots/shots.sh            # needs Go, Node and Playwright with Chromium
#
# Playwright: `require('playwright')` must resolve, e.g. `npm install --no-save playwright` in an empty folder
# and NODE_PATH=<that folder>/node_modules, plus `npx playwright install chromium` once.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
out=$repo/docs/screenshots
work=$(mktemp -d)
sim=
cleanup() {
  [ -n "$sim" ] && kill "$sim" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

node -e "require('playwright')" 2>/dev/null || {
  echo "shots: Playwright not found (see the header of $0)" >&2; exit 2; }

echo "== build eirl-ui-render and eirl-encoder-sim"
(cd "$repo/manager" && go build -o "$work/eirl-ui-render" ./cmd/eirl-ui-render &&
  go build -o "$work/eirl-encoder-sim" ./cmd/eirl-encoder-sim)

echo "== the camera pictures"
node "$here/shots.cjs" frames "$work"
# -frames: program.png in the program, tile-N.png in the tiles (slot N)
for v in full pip; do
  mkdir -p "$work/$v"
  cp "$work/frames/city.png" "$work/$v/tile-1.png"
  cp "$work/frames/mountain.png" "$work/$v/tile-2.png"
  cp "$work/frames/phone.png" "$work/$v/tile-3.png"
  cp "$work/frames/sea.png" "$work/$v/tile-4.png"
done
cp "$work/frames/city.png" "$work/full/program.png"
cp "$work/frames/pip.png" "$work/pip/program.png"

echo "== the touch screen"
mkdir -p "$out" "$work/screen"
render() { "$work/eirl-ui-render" -state "$here/mixer.json" -fonts "$repo/manager/internal/engine/screen/ui/testdata/fonts" \
  -out "$work/screen" -lang en "$@"; }
render -frames "$work/pip" pip+srtla+h265+dots
render -frames "$work/full" audio+srtla+h265+dots settings-uplinks-health+srtla+h265+dots \
  settings-camera+srtla+h265+dots settings-dests+rtmp settings-dests-video+rtmp
cp "$work/screen/pip+srtla+h265+dots-en.png" "$out/screen-mixer.png"
cp "$work/screen/audio+srtla+h265+dots-en.png" "$out/screen-audio.png"
cp "$work/screen/settings-uplinks-health+srtla+h265+dots-en.png" "$out/screen-connections.png"
cp "$work/screen/settings-camera+srtla+h265+dots-en.png" "$out/screen-camera-image.png"
cp "$work/screen/settings-dests+rtmp-en.png" "$out/screen-destinations.png"
cp "$work/screen/settings-dests-video+rtmp-en.png" "$out/screen-destination-video.png"

echo "== the web page"
mkdir -p "$work/sim"
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
(cd "$repo/manager" && exec "$work/eirl-encoder-sim" -http "127.0.0.1:$port" -web web/static -data "$work/sim" \
  -uplinks 3 >"$work/sim.log" 2>&1) &
sim=$!
for _ in $(seq 50); do
  grep -q 'local web' "$work/sim.log" 2>/dev/null && break
  sleep 0.2
done
password=$(sed -n 's/.* password=\([^ ]*\).*/\1/p' "$work/sim.log" | head -1)
[ -n "$password" ] || { echo "shots: the simulator did not start:" >&2; cat "$work/sim.log" >&2; exit 1; }
node "$here/shots.cjs" web "$work" "http://127.0.0.1:$port" "$password" "$out"

ls -l "$out"
