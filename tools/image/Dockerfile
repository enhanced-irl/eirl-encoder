# The image's work container (linux/arm64, native on Apple Silicon): builds the root filesystem in a chroot
# and assembles the card image without loop devices (IMAGE.md §3). Base pinned by digest in pins.sh.
ARG TOOLS_BASE
FROM ${TOOLS_BASE}
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates bzip2 xz-utils e2fsprogs gdisk python3 kmod file && rm -rf /var/lib/apt/lists/*
WORKDIR /work
