# tools/image/pins.sh — every input of the image, pinned (IMAGE.md §2). Sourced by build.sh.
# To move one: change its URL and sha256 together, rebuild, and review the diff of packages.lock.

# NVIDIA L4T R32.7.6 Driver Package for T210 (NVIDIA publishes no checksums: sha256 computed on Oct 14 2026)
BSP_URL=https://developer.download.nvidia.com/embedded/L4T/r32_Release_v7.6/T210/Jetson-210_Linux_R32.7.6_aarch64.tbz2
BSP_SHA256=865717eed7076babd3c193fc39bc7df41cc457b554059d374c624c42bcde0212
BSP_FILE=Jetson-210_Linux_R32.7.6_aarch64.tbz2

# Ubuntu 18.04.5 base for arm64 (sha256 from the release's SHA256SUMS)
UBUNTU_BASE_URL=http://cdimage.ubuntu.com/ubuntu-base/releases/18.04/release/ubuntu-base-18.04.5-base-arm64.tar.gz
UBUNTU_BASE_SHA256=9327cf905e818c38ba04605e40fbe11ac6548537786dc12936ca5819f8a563ad
UBUNTU_BASE_FILE=ubuntu-base-18.04.5-base-arm64.tar.gz

# The containers that do the work: ubuntu:18.04 (multi-arch index), so the filesystem tools are 18.04's
# (e2fsprogs 1.44.1: a newer one enables ext4 features kernel 4.9 does not know)
TOOLS_BASE=ubuntu:18.04@sha256:152dc042452c496007f07ca9127571cb9c29697f42acbfad72324b2bb2e43c98

# The kernel the image boots (nvidia-l4t-kernel of the BSP) and our module must agree
KERNEL_RELEASE=4.9.337-tegra
L4T_VERSION=32.7.6
