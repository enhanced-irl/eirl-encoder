#!/usr/bin/env bash
# srtla-rs.sh — builds irlserver's SRTLA sender (irlserver/srtla_send, Rust, MIT) for the Jetson: aarch64, Ubuntu
# 18.04 (glibc 2.27), in the same container as the driver, srtla_send and SRT (build/Dockerfile). On a Mac with Apple
# Silicon it is native. Pinned commit, tarball and rustup-init verified by sha256, the Rust toolchain of the project
# (its rust-toolchain.toml) and the crates of its Cargo.lock (--locked: cargo checks each crate's sha256).
#
#   ./build/srtla-rs.sh  → build/out/srtla_send_rs, srtla_send_rs.version, srtla_send_rs.LICENSE and
#                          srtla_send_rs-licenses/ (the licences of the crates it links)   (manager/build.sh packages them)
#
# One change to the original, applied here and checked to apply exactly once: the local socket where the sender
# takes the SRT of srt-live-transmit listens on 127.0.0.1 instead of [::]. Like BELABOX's sender (build/srtla.sh), it
# sends the relay's answers to whoever wrote to that port last: open on every interface, a datagram from the LAN or
# from a modem's network would take the answers and cut the stream (SRTLA.md §7 and §9.1).
#
# The toolchain and the crates are kept under $CACHE/rust, so a rebuild needs no network. To update: change
# SRTLA_RS_COMMIT, SRTLA_RS_SHA256 and RUST_TOOLCHAIN (upstream's rust-toolchain.toml), check that the patch still
# applies, and run the bench of SRTLA.md §9.9 again.
set -euo pipefail

HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
OUT="${OUT:-$HERE/build/out}"
IMAGE=l4t-r32.7.6-build
SRTLA_RS_COMMIT=59a7d8bd632730ad75f5d4b973c329ee92235c45   # 2026-10-17, v4.1.0 + 9 (the RTT floor windowed in time)
SRTLA_RS_SHA256=a8cb4b2c8f6e895342d48a00aca40abaa5ac53129bf8d3dfa34408e3cc599a96
RUST_TOOLCHAIN=1.97.1
RUSTUP_VERSION=1.28.2
RUSTUP_SHA256=e3853c5a252fca15252d07cb23a1bdd9377a8c6f3efa01531109281ae47f841c   # aarch64-unknown-linux-gnu
GLIBC_MAX=2.27
TARBALL="$CACHE/srtla_send-rs-$SRTLA_RS_COMMIT.tar.gz"
RUSTUP_INIT="$CACHE/rustup-init-$RUSTUP_VERSION-aarch64"

mkdir -p "$CACHE/rust" "$OUT"
[ -f "$TARBALL" ] || curl -fsSL -o "$TARBALL" "https://codeload.github.com/irlserver/srtla_send/tar.gz/$SRTLA_RS_COMMIT"
echo "$SRTLA_RS_SHA256  $TARBALL" | shasum -a 256 -c - >/dev/null || { echo "srtla-rs: sha256 mismatch ($TARBALL)" >&2; exit 1; }
[ -f "$RUSTUP_INIT" ] || curl -fsSL -o "$RUSTUP_INIT" \
  "https://static.rust-lang.org/rustup/archive/$RUSTUP_VERSION/aarch64-unknown-linux-gnu/rustup-init"
echo "$RUSTUP_SHA256  $RUSTUP_INIT" | shasum -a 256 -c - >/dev/null || { echo "srtla-rs: rustup-init sha256 mismatch" >&2; exit 1; }
docker build -q -t "$IMAGE" "$HERE/build" >/dev/null
rm -rf "$OUT/srtla_send_rs-licenses"

docker run --rm --platform linux/arm64 -v "$CACHE":/cache:ro -v "$CACHE/rust":/rust -v "$OUT":/out \
  -e RUSTUP_HOME=/rust/rustup -e CARGO_HOME=/rust/cargo -e RUSTUP_TOOLCHAIN=$RUST_TOOLCHAIN "$IMAGE" bash -euo pipefail -c "
  export PATH=/rust/cargo/bin:\$PATH
  if ! rustup toolchain list 2>/dev/null | grep -q '^$RUST_TOOLCHAIN'; then
    install -m 0755 /cache/$(basename "$RUSTUP_INIT") /tmp/rustup-init
    /tmp/rustup-init -y --no-modify-path --profile minimal --default-toolchain $RUST_TOOLCHAIN >/tmp/rustup.log 2>&1 \
      || { tail -20 /tmp/rustup.log; exit 1; }
  fi
  mkdir /tmp/s && tar xzf /cache/$(basename "$TARBALL") -C /tmp/s --strip-components=1
  cd /tmp/s
  f=src/sender/mod.rs
  sed -i 's/UdpSocket::bind(SocketAddr::from((Ipv6Addr::UNSPECIFIED, local_srt_port)))/UdpSocket::bind(SocketAddr::from((Ipv4Addr::LOCALHOST, local_srt_port)))/' \$f
  sed -i 's/info!(\"listening for SRT on \[::\]:{}\", local_srt_port);/info!(\"listening for SRT on 127.0.0.1:{}\", local_srt_port);/' \$f
  sed -i 's/^use std::net::{IpAddr, Ipv6Addr, SocketAddr};/use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};/' \$f
  [ \$(grep -c 'SocketAddr::from((Ipv4Addr::LOCALHOST, local_srt_port))' \$f) = 1 ] &&
    [ \$(grep -c 'Ipv6Addr::UNSPECIFIED, local_srt_port' \$f) = 0 ] &&
    [ \$(grep -c '^use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};' \$f) = 1 ] ||
    { echo 'srtla-rs: the 127.0.0.1 patch does not apply' >&2; exit 1; }
  # Same paths in every build: the binary does not depend on where it was built
  export RUSTFLAGS='--remap-path-prefix=/tmp/s=srtla_send --remap-path-prefix=/rust/cargo=cargo'
  cargo build --release --locked --quiet 2>/tmp/cargo.log || { tail -40 /tmp/cargo.log; exit 1; }
  bin=target/release/srtla_send
  strip \$bin
  # Nothing newer than the board's glibc
  max=\$(objdump -T \$bin | grep -o 'GLIBC_[0-9.]*' | sed 's/GLIBC_//' | sort -V | tail -1)
  [ \"\$(printf '%s\n' \"\$max\" $GLIBC_MAX | sort -V | tail -1)\" = $GLIBC_MAX ] ||
    { echo \"srtla-rs: needs glibc \$max, the board has $GLIBC_MAX\" >&2; exit 1; }
  v=\$(\$bin -v)
  echo \"\$v\" | grep -q '^4\.1\.0 ' || { echo \"srtla-rs: unexpected version: \$v\" >&2; exit 1; }
  install -m 0755 \$bin /out/srtla_send_rs
  install -m 0644 LICENSE /out/srtla_send_rs.LICENSE
  echo '$SRTLA_RS_COMMIT '\"\$v\" > /out/srtla_send_rs.version
  # The licences of every crate linked into it (normal dependencies for this target; build tools are not shipped)
  mkdir -p /out/srtla_send_rs-licenses
  : > /out/srtla_send_rs-licenses/THIRD-PARTY.txt
  cargo tree -e normal --target aarch64-unknown-linux-gnu --prefix none --locked --format '{p} {l}' 2>/dev/null |
    sed 's/ (\*)\$//; s/ (proc-macro)//' | sort -u | while read -r name ver rest; do
      [ \"\$name\" = srtla_send ] && continue
      case \"\$rest\" in \"(/tmp/s\"*) continue ;; esac   # the workspace's own crates: upstream's MIT, LICENSE above
      lic=\$(echo \"\$rest\" | sed 's/^(.*) //')
      echo \"\$name \${ver#v} \$lic\" >> /out/srtla_send_rs-licenses/THIRD-PARTY.txt
      src=\$(ls -d /rust/cargo/registry/src/*/\"\$name-\${ver#v}\" 2>/dev/null | head -1)
      [ -n \"\$src\" ] || { echo \"srtla-rs: no source for \$name \$ver\" >&2; exit 1; }
      mkdir -p \"/out/srtla_send_rs-licenses/\$name-\${ver#v}\"
      n=0
      for f in \"\$src\"/LICENSE* \"\$src\"/LICENCE* \"\$src\"/COPYING* \"\$src\"/NOTICE* \"\$src\"/UNLICENSE*; do
        [ -f \"\$f\" ] && cp \"\$f\" \"/out/srtla_send_rs-licenses/\$name-\${ver#v}/\" && n=\$((n+1))
      done
      [ \$n -gt 0 ] || echo \"srtla-rs: warning: no licence file in \$name \$ver (\$lic)\" >&2
    done
  file /out/srtla_send_rs
  echo \"glibc max: \$max\"
"
echo "build/out/srtla_send_rs: $(cat "$OUT/srtla_send_rs.version"), sha256 $(shasum -a 256 "$OUT/srtla_send_rs" | cut -d' ' -f1)"
