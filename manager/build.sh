#!/usr/bin/env bash
# build.sh — builds the app's package for the Jetson (static, linux/arm64) and, with JETSON=<ssh host>,
# installs it there.
#   ./build.sh                    → dist/pkg/ (binaries, srtla_send, MediaMTX, deploy/, splash/, install.sh,
#                                   VERSION, BUILD, files.txt, doc/ with the licences)
#   JETSON=jetson ./build.sh      → also copies it and runs `sudo ./install.sh` on the board
#   JETSON=jetson ./build.sh --restart-engine   (passes the option to install.sh)
# Environment (tools/release.sh sets them; UPDATES.md §6):
#   EIRL_VERSION=X.Y.Z   the release version stamped into every binary (default 0.0.0-dev+<commit>)
#   EIRL_PUBKEY=<b64>    a TEST build that trusts this release key instead of the production one
#   EIRL_SKIP_TESTS=1    skip `go test` (the caller already ran it)
# The engine is not in this package: tools/deploy-engine.sh installs it by hand, a release bundle
# (tools/release.sh) adds it.
set -euo pipefail
cd "$(dirname "$0")"
commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
dirty=""
[ -z "$(git status --porcelain 2>/dev/null)" ] || dirty=.dirty
ver=${EIRL_VERSION:-0.0.0-dev+$commit$dirty}
ldflags="-s -w -X main.version=$ver"
if [ -n "${EIRL_PUBKEY:-}" ]; then
  ldflags="$ldflags -X github.com/enhanced-irl/eirl-encoder/manager/internal/update.trustedKey=$EIRL_PUBKEY"
  echo "TEST BUILD: the binaries trust the release key $EIRL_PUBKEY, not the production key" >&2
fi
[ -n "${EIRL_SKIP_TESTS:-}" ] || go test ./...
rm -rf dist && mkdir -p dist/pkg/deploy
build() { CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$ldflags" -o "$1" "$2"; }
build dist/pkg/eirl-encoderd ./cmd/eirl-encoderd
find deploy -maxdepth 1 -type f -exec cp {} dist/pkg/deploy/ \; && cp deploy/install.sh dist/pkg/ && chmod +x dist/pkg/install.sh
# Boot splash (SPLASH.md): binary, units and its installer in dist/pkg/splash. install.sh runs it; on its
# own it undoes the splash (splash/install.sh --undo).
mkdir -p dist/pkg/splash
build dist/pkg/splash/eirl-splash ./cmd/eirl-splash
cp deploy/splash/* dist/pkg/splash/ && chmod +x dist/pkg/splash/install.sh
# Network cameras (NETWORK_SOURCES.md §6): the root helper and MediaMTX (official binary, version and
# sha256 pinned in build/mediamtx.sh)
build dist/pkg/eirl-netsetup ./cmd/eirl-netsetup
[ -x ../build/out/mediamtx ] || ../build/mediamtx.sh
cp ../build/out/mediamtx dist/pkg/
# srtla_send (SRTLA): built separately in build/out (arm64 container, pinned commit); reused
[ -x ../build/out/srtla_send ] || ../build/srtla.sh
cp ../build/out/srtla_send dist/pkg/
# Update helper (UPDATES.md §7)
build dist/pkg/eirl-update ./cmd/eirl-update

# Licences of what the package ships (→ /usr/share/doc/eirl-encoder): ours, the Go toolchain and every
# Go module linked into the binaries, MediaMTX (MIT) and srtla_send (AGPL-3.0, with where its source is)
doc=dist/pkg/doc
mkdir -p "$doc/licenses/go" "$doc/licenses/mediamtx" "$doc/licenses/srtla"
for f in LICENSE NOTICE; do [ -f "../$f" ] && cp "../$f" "$doc/$f"; done
# The Go toolchain's licence (Homebrew keeps it next to GOROOT, not inside)
goroot=$(go env GOROOT)
golic=""
for l in "$goroot/LICENSE" "$goroot/../LICENSE"; do [ -f "$l" ] && { golic=$l; break; }; done
[ -n "$golic" ] || { echo "Go's LICENSE not found near $goroot" >&2; exit 1; }
cp "$golic" "$doc/licenses/go/LICENSE.golang"
[ -f "$goroot/PATENTS" ] && cp "$goroot/PATENTS" "$doc/licenses/go/PATENTS.golang"
GOOS=linux GOARCH=arm64 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' \
    ./cmd/eirl-encoderd ./cmd/eirl-splash ./cmd/eirl-netsetup ./cmd/eirl-update ./cmd/eirl-engine | sort -u |
  while read -r mod modver dir; do
    [ -n "$dir" ] || continue
    d="$doc/licenses/go/$mod"
    mkdir -p "$d"
    echo "$mod $modver" > "$d/VERSION"
    found=""
    for l in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE* "$dir"/PATENTS*; do
      [ -f "$l" ] && cp "$l" "$d/" && found=1
    done
    [ -n "$found" ] || { echo "no licence file found for Go module $mod" >&2; exit 1; }
  done
cp ../build/out/mediamtx.LICENSE "$doc/licenses/mediamtx/LICENSE"
srtla_commit=$(sed -n 's/^SRTLA_COMMIT=\([0-9a-f]*\).*/\1/p' ../build/srtla.sh)
srtla_tgz="${CACHE:-$HOME/.cache/l4t-r32.7.6}/srtla-$srtla_commit.tar.gz"
tar -xzOf "$srtla_tgz" "srtla-$srtla_commit/LICENSE" > "$doc/licenses/srtla/LICENSE"
cat > "$doc/licenses/srtla/SOURCE.txt" <<SRC
srtla_send is BELABOX/srtla, licensed under the GNU Affero General Public License v3.0 (LICENSE here).
Source: https://github.com/BELABOX/srtla/tree/$srtla_commit
It is built from that commit with one change, applied by build/srtla.sh of eirl-encoder
(https://github.com/enhanced-irl/eirl-encoder): the socket that receives the local SRT listens on
127.0.0.1 instead of INADDR_ANY.
SRC

# What is installed (UPDATES.md §2) and every path install.sh installs (files.txt, for the OTA helper)
echo "$ver" > dist/pkg/VERSION
echo "commit=$commit$dirty date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" > dist/pkg/BUILD
: > dist/pkg/files.txt
bash dist/pkg/install.sh --list-files > dist/pkg/files.txt.new && mv dist/pkg/files.txt.new dist/pkg/files.txt
echo "dist/pkg: $ver, eirl-encoderd $(du -h dist/pkg/eirl-encoderd | cut -f1), $(wc -l < dist/pkg/files.txt | tr -d ' ') files"
if [ -n "${JETSON:-}" ]; then
  # A NON-predictable temporary directory: what is inside runs with sudo
  rdir=$(ssh "$JETSON" 'mktemp -d')
  COPYFILE_DISABLE=1 tar --no-xattrs -C dist/pkg -cf - . | ssh "$JETSON" "tar -C $rdir -xf -"
  # -t: sudo asks for the password (the board no longer has NOPASSWD)
  ssh -t "$JETSON" "sudo $rdir/install.sh $*; rc=\$?; rm -rf $rdir; exit \$rc"
fi
