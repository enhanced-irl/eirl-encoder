#!/usr/bin/env bash
# send-test.sh: eirl-send against MediaMTX in Docker (OUTPUTS.md §5 and §12). It publishes ~20 s test
# streams (1920x1080@60 + AAC, MPEG-TS fed in real time by ffmpeg) over RTMP and RTMPS (a self-signed
# certificate made here), in H.264 and H.265, reads each one back with ffprobe (RTSP) while it is on air,
# and checks the failure reasons: a wrong key (rejected), an untrusted certificate (tls_failed), a
# certificate not valid yet (clock_not_synced) and a closed port (relay_unreachable). It also checks the
# stats lines of fd 3.
#
#   tools/send-test.sh            needs docker, ffmpeg/ffprobe (libx264, libx265), openssl, go, python3
#
# It uses the host ports 19350 (RTMP), 19360 (RTMPS), 18554 (RTSP) and 19399 (closed), on 127.0.0.1.
# Everything it makes goes to a temporary directory; the container and the directory are removed on exit.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
image=bluenviron/mediamtx:1.21.2
rtmp_port=19350 rtmps_port=19360 rtsp_port=18554 closed_port=19399
key=testkey-$RANDOM
tmp=$(mktemp -d)
name=eirl-send-test-$$
fails=0

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

say() { printf '\n== %s\n' "$*"; }
ok() { printf '   ok    %s\n' "$*"; }
bad() { printf '   FAIL  %s\n' "$*"; fails=$((fails + 1)); }

say "build eirl-send"
(cd "$repo/manager" && go build -o "$tmp/eirl-send" ./cmd/eirl-send)
bin=$tmp/eirl-send

say "certificates and MediaMTX ($image)"
mkdir -p "$tmp/certs"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=localhost \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
  -keyout "$tmp/certs/server.key" -out "$tmp/certs/server.crt" 2>/dev/null
future=0
if openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=localhost \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" -not_before "$(date -u -v+2d +%Y%m%d%H%M%SZ 2>/dev/null ||
    date -u -d '+2 days' +%Y%m%d%H%M%SZ)" -not_after "$(date -u -v+30d +%Y%m%d%H%M%SZ 2>/dev/null ||
    date -u -d '+30 days' +%Y%m%d%H%M%SZ)" \
  -keyout "$tmp/certs/future.key" -out "$tmp/certs/future.crt" 2>/dev/null; then
  future=1
fi
cat >"$tmp/mediamtx.yml" <<EOF
logLevel: info
api: no
hls: no
webrtc: no
srt: no
rtsp: yes
rtspTransports: [tcp]
rtspAddress: :8554
rtmp: yes
rtmpEncryption: optional
rtmpAddress: :1935
rtmpsAddress: :1936
rtmpServerKey: /certs/server.key
rtmpServerCert: /certs/server.crt
paths:
  live/$key:
EOF
start_mediamtx() {
  docker logs "$name" >>"$tmp/mediamtx.log" 2>&1 || true
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" -p "127.0.0.1:$rtmp_port:1935" -p "127.0.0.1:$rtmps_port:1936" \
    -p "127.0.0.1:$rtsp_port:8554" -v "$tmp/certs:/certs:ro" -v "$tmp/mediamtx.yml:/mediamtx.yml:ro" \
    "$image" >/dev/null
  for _ in $(seq 50); do
    if docker logs "$name" 2>&1 | grep -q '\[RTMPS\] started'; then
      return 0
    fi
    sleep 0.2
  done
  docker logs "$name" >&2
  return 1
}
start_mediamtx
ok "MediaMTX up: rtmp :$rtmp_port, rtmps :$rtmps_port, rtsp :$rtsp_port"

say "test streams (20 s, 1920x1080@60, AAC 48 kHz)"
lavfi=(-f lavfi -i testsrc2=size=1920x1080:rate=60 -f lavfi -i sine=frequency=1000:sample_rate=48000)
ffmpeg -nostdin -hide_banner -loglevel error -y "${lavfi[@]}" -t 20 -pix_fmt yuv420p \
  -c:v libx264 -preset ultrafast -profile:v high -bf 0 -g 120 -keyint_min 120 -sc_threshold 0 \
  -x264-params repeat-headers=1 -b:v 4M -c:a aac -b:a 128k -ac 2 -f mpegts "$tmp/h264.ts"
ffmpeg -nostdin -hide_banner -loglevel error -y "${lavfi[@]}" -t 20 -pix_fmt yuv420p \
  -c:v libx265 -preset ultrafast -b:v 4M \
  -x265-params bframes=0:keyint=120:min-keyint=120:scenecut=0:repeat-headers=1:log-level=error \
  -c:a aac -b:a 128k -ac 2 -f mpegts "$tmp/h265.ts"
ok "$(du -h "$tmp/h264.ts" | cut -f1) h264.ts, $(du -h "$tmp/h265.ts" | cut -f1) h265.ts"

# publish <case> <file.ts> <url> <key> [eirl-send flags…]: feeds the file in real time into eirl-send in
# the background; its stderr, stats and exit code go to $tmp/<case>/.
publish() {
  local out=$tmp/$1 ts=$2 url=$3 k=$4
  shift 4
  mkdir -p "$out"
  (
    set +e
    ffmpeg -nostdin -hide_banner -loglevel error -re -i "$ts" -c copy -f mpegts - 2>/dev/null |
      EIRL_SEND_KEY=$k "$bin" rtmp -url "$url" -stats-fd 3 -connect-timeout 5s "$@" \
        3>"$out/stats.jsonl" 2>"$out/events.log"
    echo "${PIPESTATUS[1]}" >"$out/exit"
  ) &
  echo $! >"$out/pid"
}

wait_case() { wait "$(cat "$tmp/$1/pid")" 2>/dev/null || true; }

# probe <path>: codec,width,height of each stream read back over RTSP.
probe() {
  ffprobe -v error -rtsp_transport tcp -timeout 5000000 -show_entries stream=codec_name,width,height \
    -of csv=p=0 "rtsp://127.0.0.1:$rtsp_port/$1" 2>&1 | sort | tr '\n' ' '
}

check_events() { # <case> <expected exit> <grep -E patterns…>
  local c=$1 want=$2
  shift 2
  local got
  got=$(cat "$tmp/$c/exit" 2>/dev/null || echo none)
  [ "$got" = "$want" ] && ok "$c: exit $got" || bad "$c: exit $got, want $want"
  for p in "$@"; do
    if grep -qE "$p" "$tmp/$c/events.log"; then ok "$c: $p"; else bad "$c: no line matching $p"; fi
  done
  if grep -q "$key" "$tmp/$c/events.log"; then bad "$c: the key was printed"; fi
  sed 's/^/          | /' "$tmp/$c/events.log"
}

check_stats() { # <case>
  python3 -I - "$tmp/$1/stats.jsonl" <<'EOF' && ok "$1: stats on fd 3" || bad "$1: stats on fd 3"
import json, sys
lines = open(sys.argv[1]).read().splitlines()
assert len(lines) >= 40, f"only {len(lines)} lines"
total = 0
for line in lines:
    d = json.loads(line)
    assert set(d) == {"link", "send"}, d
    assert set(d["send"]) == {"bytes", "msBuf", "packetsDropped", "mbitRate"}, d
    assert isinstance(d["link"]["rtt"], (int, float)) and isinstance(d["send"]["msBuf"], int), d
    total += d["send"]["bytes"]
mid = [json.loads(l) for l in lines[8:-8]]
rate = sum(d["send"]["mbitRate"] for d in mid) / len(mid)
bufs = sorted(d["send"]["msBuf"] for d in mid)
drops = sum(d["send"]["packetsDropped"] for d in mid)
print(f"          | {len(lines)} lines, {total/1e6:.1f} MB, ~{rate:.2f} Mbit/s, msBuf median "
      f"{bufs[len(bufs)//2]} ms max {bufs[-1]} ms, dropped {drops}; last: {lines[-1]}")
EOF
}

run_ok() { # <case> <file.ts> <url> <codec> [flags…]
  local c=$1 ts=$2 url=$3 codec=$4
  shift 4
  say "$c"
  publish "$c" "$ts" "$url" "$key" "$@"
  sleep 7
  local got name=$codec
  [ "$codec" = h265 ] && name=hevc
  got=$(probe "live/$key")
  if [[ $got == *"$name,1920,1080"* && $got == *aac* ]]; then ok "$c: read back: $got"; else bad "$c: read back: $got"; fi
  wait_case "$c"
  check_events "$c" 0 '^event=connecting host=127\.0\.0\.1 ' '^event=connected$' "^event=publishing codec=$codec$" '^event=eof$'
  check_stats "$c"
}

run_ok rtmp-h264 "$tmp/h264.ts" "rtmp://127.0.0.1:$rtmp_port/live" h264 -bind 127.0.0.1
run_ok rtmps-h264 "$tmp/h264.ts" "rtmps://127.0.0.1:$rtmps_port/live" h264 -ca-file "$tmp/certs/server.crt"
run_ok rtmp-h265 "$tmp/h265.ts" "rtmp://127.0.0.1:$rtmp_port/live" h265
run_ok rtmps-h265 "$tmp/h265.ts" "rtmps://127.0.0.1:$rtmps_port/live/" h265 -ca-file "$tmp/certs/server.crt"

run_fail() { # <case> <url> <key> <reason> [flags…]
  local c=$1 url=$2 k=$3 reason=$4
  shift 4
  say "$c"
  publish "$c" "$tmp/h264.ts" "$url" "$k" "$@"
  wait_case "$c"
  check_events "$c" 1 "^event=failed reason=$reason detail=\""
}

run_fail wrong-key "rtmp://127.0.0.1:$rtmp_port/live" wrong-key rejected
run_fail untrusted-cert "rtmps://127.0.0.1:$rtmps_port/live" "$key" tls_failed
run_fail closed-port "rtmp://127.0.0.1:$closed_port/live" "$key" relay_unreachable
run_fail bad-url "srt://127.0.0.1:$rtmp_port/live" "$key" internal

if [ "$future" = 1 ]; then
  cp "$tmp/certs/future.crt" "$tmp/certs/server.crt"
  cp "$tmp/certs/future.key" "$tmp/certs/server.key"
  start_mediamtx
  run_fail clock-behind "rtmps://127.0.0.1:$rtmps_port/live" "$key" clock_not_synced -ca-file "$tmp/certs/future.crt"
else
  say "clock-behind: skipped (this openssl has no -not_before)"
fi

say "summary"
# What MediaMTX complained about (the refused publishes are expected; the MoQ certificate is its own).
docker logs "$name" >>"$tmp/mediamtx.log" 2>&1 || true
grep -E ' (WAR|ERR) ' "$tmp/mediamtx.log" | grep -v MoQ | sed 's/^/   mediamtx | /' || true
if [ "$fails" -eq 0 ]; then
  echo "   all checks passed"
else
  echo "   $fails check(s) failed"
  exit 1
fi
