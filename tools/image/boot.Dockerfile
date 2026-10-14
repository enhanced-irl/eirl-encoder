# The container that runs NVIDIA's flash.sh --no-flash --sign to make the card's boot partitions
# (linux/amd64: NVIDIA's host tools are 32-bit i386 static binaries). IMAGE.md §3.6.
ARG TOOLS_BASE
FROM ${TOOLS_BASE}
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      bzip2 python3 xxd cpio file binutils device-tree-compiler libxml2-utils openssl uuid-runtime \
      && rm -rf /var/lib/apt/lists/*
WORKDIR /work
