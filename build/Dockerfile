# Native aarch64 build environment for the L4T R32.7.6 kernel (4.9.337-tegra) and the board's C/C++ tools
# (srtla_send, SRT: build/srtla.sh, build/srt.sh). Ubuntu 18.04 like the board: glibc 2.27, OpenSSL 1.1.
FROM ubuntu:18.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential bc flex bison libssl-dev kmod cpio python3 xxd device-tree-compiler \
      ca-certificates rsync file patch cmake pkg-config && rm -rf /var/lib/apt/lists/*
WORKDIR /work
