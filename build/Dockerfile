# Entorno de compilación nativo aarch64 para el kernel L4T R32.7.6 (4.9.337-tegra)
FROM ubuntu:18.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential bc flex bison libssl-dev kmod cpio python3 xxd device-tree-compiler \
      ca-certificates rsync file patch && rm -rf /var/lib/apt/lists/*
WORKDIR /work
