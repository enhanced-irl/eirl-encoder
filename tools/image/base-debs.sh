#!/usr/bin/env bash
# tools/image/base-debs.sh DIR — the maintainers' copies of the frozen base (IMAGE.md §3.2): every Ubuntu package of
# tools/image/base.lock, at its version, as Ubuntu's archive serves it today, into DIR (with SHA256SUMS). apt fetches
# them in an Ubuntu 18.04 arm64 container and checks each one against the archive's signed index. Kept outside the
# repository (backups/eirl-encoder-image-sources/base-debs/, next to the source packages), they let
# `tools/image/build.sh --base-debs DIR` rebuild the base the day Ubuntu no longer serves one of those versions.
# Run it once (and again if base.lock ever changed, which it should not); ~200 MB.
set -euo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=pins.sh
. tools/image/pins.sh
[ $# -eq 1 ] || { echo "usage: tools/image/base-debs.sh DIR" >&2; exit 64; }
mkdir -p "$1" && out=$(cd "$1" && pwd)
sed -E 's/#.*//' tools/image/base.lock | awk 'NF == 2 && $1 !~ /^(nvidia-l4t-|jetson-gpio)/ {print $1 "=" $2}' > "$out/.want"
docker run --rm --platform linux/arm64 -v "$out:/out" "$TOOLS_BASE" bash -euo pipefail -c '
  cat > /etc/apt/sources.list <<EOF
deb http://ports.ubuntu.com/ubuntu-ports bionic main universe
deb http://ports.ubuntu.com/ubuntu-ports bionic-updates main universe
deb http://ports.ubuntu.com/ubuntu-ports bionic-security main universe
EOF
  apt-get update -qq
  cd /out && apt-get download -qq $(cat /out/.want)
  sha256sum *.deb > SHA256SUMS'
n=$(ls "$out"/*.deb | wc -l | tr -d ' ')
want=$(wc -l < "$out/.want" | tr -d ' ')
rm -f "$out/.want"
[ "$n" = "$want" ] || { echo "base-debs: $n of $want packages" >&2; exit 1; }
echo "base-debs: $n packages in $out ($(du -sh "$out" | cut -f1))"
