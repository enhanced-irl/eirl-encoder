#!/usr/bin/env bash
# build.sh — builds the app's package for the Jetson (static, linux/arm64) and, with JETSON=<ssh host>,
# installs it there.
#   ./build.sh                    → dist/pkg/ (binaries, srt-live-transmit + libsrt, srtla_send + srtla_send_rs, MediaMTX, deploy/,
#                                   splash/, install.sh, VERSION, BUILD, files.txt, doc/ with the licences)
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
# The fan control (FAN.md): a root daemon and its unit (deploy/eirl-fan.service, copied above)
build dist/pkg/eirl-fan ./cmd/eirl-fan
# Local recordings (RECORDING.md §4): the root helper of the USB disk; its units and udev rule are in deploy/ (copied above)
build dist/pkg/eirl-storage ./cmd/eirl-storage
[ -x ../build/out/mediamtx ] || ../build/mediamtx.sh
cp ../build/out/mediamtx dist/pkg/
# srtla_send (SRTLA): built separately in build/out (arm64 container, pinned commit); reused
[ -x ../build/out/srtla_send ] || ../build/srtla.sh
cp ../build/out/srtla_send dist/pkg/
# srtla_send_rs, irlserver's SRTLA sender (SRTLA.md §9): the default one, the C one stays as the way back. Built
# separately in build/out (arm64 container, pinned commit); reused while build/out has the pinned one
srtla_rs_commit=$(sed -n 's/^SRTLA_RS_COMMIT=\([0-9a-f]*\).*/\1/p' ../build/srtla-rs.sh)
[ -n "$srtla_rs_commit" ] || { echo "build/srtla-rs.sh without SRTLA_RS_COMMIT" >&2; exit 1; }
if ! [ -x ../build/out/srtla_send_rs ] || [ "$(cut -d' ' -f1 ../build/out/srtla_send_rs.version 2>/dev/null)" != "$srtla_rs_commit" ]; then
  ../build/srtla-rs.sh
fi
cp ../build/out/srtla_send_rs dist/pkg/
# The SRT output: srt-live-transmit and its libsrt (Haivision SRT, pinned tag in build/srt.sh, arm64 container);
# reused while build/out has the pinned one
srt_version=$(sed -n 's/^SRT_VERSION=\([^ ]*\).*/\1/p' ../build/srt.sh)
srt_commit=$(sed -n 's/^SRT_COMMIT=\([0-9a-f]*\).*/\1/p' ../build/srt.sh)
srt_soname=$(sed -n 's/^SRT_SONAME=\([^ ]*\).*/\1/p' ../build/srt.sh)
[ -n "$srt_version" ] && [ -n "$srt_commit" ] && [ -n "$srt_soname" ] || { echo "build/srt.sh without SRT_VERSION/SRT_COMMIT/SRT_SONAME" >&2; exit 1; }
if ! [ -x ../build/out/srt-live-transmit ] || ! [ -f "../build/out/$srt_soname" ] ||
  [ "$(cat ../build/out/srt.version 2>/dev/null)" != "$srt_version $srt_commit" ]; then
  ../build/srt.sh
fi
cp ../build/out/srt-live-transmit "../build/out/$srt_soname" dist/pkg/
# RTMP and RTMPS (OUTPUTS.md §5): eirl-send, the engine's RTMP sender (pure Go, this module)
build dist/pkg/eirl-send ./cmd/eirl-send
# RIST (OUTPUTS.md §6): ristsender of librist, one static binary built separately in build/out/rist (arm64
# container, pinned release in build/rist.sh); reused while build/out has the pinned one
rist_version=$(sed -n 's/^RIST_VERSION=\([^ ]*\).*/\1/p' ../build/rist.sh)
[ -n "$rist_version" ] || { echo "build/rist.sh without RIST_VERSION" >&2; exit 1; }
if ! [ -x ../build/out/rist/ristsender ] || [ "$(head -1 ../build/out/rist/rist.version 2>/dev/null)" != "$rist_version" ]; then
  ../build/rist.sh
fi
cp ../build/out/rist/ristsender dist/pkg/
# Bluetooth audio (BLUETOOTH.md §3.7): BlueALSA's daemon and its ALSA plugins, built separately in build/out/bluealsa
# (arm64 container, pinned release in build/bluealsa.sh); reused while build/out has the pinned one
bluealsa_version=$(sed -n 's/^BLUEALSA_VERSION=\([^ ]*\).*/\1/p' ../build/bluealsa.sh)
[ -n "$bluealsa_version" ] || { echo "build/bluealsa.sh without BLUEALSA_VERSION" >&2; exit 1; }
if ! [ -x ../build/out/bluealsa/bluealsa ] || [ "$(head -1 ../build/out/bluealsa/bluealsa.version 2>/dev/null)" != "$bluealsa_version" ]; then
  ../build/bluealsa.sh
fi
# Only the daemon: its ALSA plugins and their configuration are the system layer's (system/built.txt, UPDATES.md §12)
mkdir -p dist/pkg/bluealsa
cp ../build/out/bluealsa/bluealsa dist/pkg/bluealsa/
# Update helper (UPDATES.md §7)
build dist/pkg/eirl-update ./cmd/eirl-update

# Licences of what the package ships (→ /usr/share/doc/eirl-encoder): ours, the Go toolchain and every
# Go module linked into the binaries, MediaMTX (MIT), srtla_send (AGPL-3.0), srtla_send_rs (MIT, with the crates it
# links) and SRT (MPL-2.0), with where their source is
doc=dist/pkg/doc
mkdir -p "$doc/licenses/go" "$doc/licenses/mediamtx" "$doc/licenses/srtla" "$doc/licenses/srtla-rs" "$doc/licenses/srt" \
  "$doc/licenses/rist" "$doc/licenses/bluealsa"
for f in LICENSE NOTICE; do [ -f "../$f" ] && cp "../$f" "$doc/$f"; done
# The Go toolchain's licence (Homebrew keeps it next to GOROOT, not inside)
goroot=$(go env GOROOT)
golic=""
for l in "$goroot/LICENSE" "$goroot/../LICENSE"; do [ -f "$l" ] && { golic=$l; break; }; done
[ -n "$golic" ] || { echo "Go's LICENSE not found near $goroot" >&2; exit 1; }
cp "$golic" "$doc/licenses/go/LICENSE.golang"
[ -f "$goroot/PATENTS" ] && cp "$goroot/PATENTS" "$doc/licenses/go/PATENTS.golang"
GOOS=linux GOARCH=arm64 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' \
    ./cmd/eirl-encoderd ./cmd/eirl-splash ./cmd/eirl-netsetup ./cmd/eirl-update ./cmd/eirl-engine ./cmd/eirl-send ./cmd/eirl-fan ./cmd/eirl-storage | sort -u |
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
cp ../build/out/srtla_send_rs.LICENSE "$doc/licenses/srtla-rs/LICENSE"
cp -R ../build/out/srtla_send_rs-licenses "$doc/licenses/srtla-rs/crates"
cat > "$doc/licenses/srtla-rs/SOURCE.txt" <<SRC
srtla_send_rs is irlserver/srtla_send, licensed under the MIT License (LICENSE here), statically linked with the
Rust crates listed in crates/THIRD-PARTY.txt, whose licences are in crates/.
Source: https://github.com/irlserver/srtla_send/tree/$srtla_rs_commit
It is built from that commit with one change, applied by build/srtla-rs.sh of eirl-encoder
(https://github.com/enhanced-irl/eirl-encoder): the socket that receives the local SRT listens on
127.0.0.1 instead of all interfaces.
SRC
cp ../build/out/srt.LICENSE "$doc/licenses/srt/LICENSE"
cat > "$doc/licenses/srt/SOURCE.txt" <<SRC
srt-live-transmit and $srt_soname are Haivision SRT (Secure Reliable Transport) $srt_version, licensed under
the Mozilla Public License 2.0 (LICENSE here).
Source: https://github.com/Haivision/srt/tree/$srt_commit
They are built from that commit without changes by build/srt.sh of eirl-encoder
(https://github.com/enhanced-irl/eirl-encoder), which also gives the build options.
SRC
cp ../build/out/rist/LICENSE* "$doc/licenses/rist/"
cat > "$doc/licenses/rist/SOURCE.txt" <<SRC
ristsender is librist $rist_version (BSD-2-Clause, LICENSE here), statically linked with the copies of
mbedtls (Apache-2.0, LICENSE.mbedtls), cJSON (MIT, LICENSE.cJSON) and LZ4 (BSD-2-Clause, LICENSE.lz4) it
bundles. Source: https://code.videolan.org/rist/librist/-/tree/$rist_version
It is built from that release without changes by build/rist.sh of eirl-encoder
(https://github.com/enhanced-irl/eirl-encoder), which also gives the build options.
SRC
cp ../build/out/bluealsa/LICENSE ../build/out/bluealsa/LICENSE.sbc "$doc/licenses/bluealsa/"
# libsbc is LGPL and linked statically: its source goes with the binary (the exact tarball the build verified)
sbc_tarball="${CACHE:-$HOME/.cache/l4t-r32.7.6}/sbc-$(sed -n 's/^SBC_VERSION=\([^ ]*\).*/\1/p' ../build/bluealsa.sh).tar.xz"
[ -f "$sbc_tarball" ] || { echo "no $sbc_tarball: run build/bluealsa.sh" >&2; exit 1; }
cp "$sbc_tarball" "$doc/licenses/bluealsa/"
sbc_version=$(sed -n 's/^SBC_VERSION=\([^ ]*\).*/\1/p' ../build/bluealsa.sh)
cat > "$doc/licenses/bluealsa/SOURCE.txt" <<SRC
bluealsa and its ALSA plugins (libasound_module_pcm_bluealsa.so, libasound_module_ctl_bluealsa.so) are BlueALSA
(bluez-alsa) $bluealsa_version, MIT licence (LICENSE here). Source: https://github.com/arkq/bluez-alsa/tree/$bluealsa_version
The daemon is statically linked with libsbc $sbc_version, GNU Lesser General Public License 2.1 or later
(LICENSE.sbc here). Its source is next to this file (sbc-$sbc_version.tar.xz, as published at
https://www.kernel.org/pub/linux/bluetooth/sbc-$sbc_version.tar.xz).
Both are built from those releases without changes by build/bluealsa.sh of eirl-encoder
(https://github.com/enhanced-irl/eirl-encoder), which also gives the build options: with it and those sources the
daemon can be rebuilt, and relinked with another libsbc.
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
