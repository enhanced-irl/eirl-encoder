#!/usr/bin/env python3
"""stages/sources.py — the image's SOURCES.md (IMAGE.md §9.3): where the source code of everything in the image is,
with our written offer for it. Written into the root filesystem (/usr/share/doc/eirl-encoder-image/SOURCES.md) by
stages/rootfs.sh once every package is installed; check.py checks it against the installed packages and copies it
next to the image (published as eirl-encoder-<version>-jetson-nano-sources.md). Runs in the linux/arm64 work
container (Python 3.6).

  sources.py --rootfs DIR --repo DIR --release DIR --version V --name IMAGE_NAME --commit SHA12 --out FILE
"""
import argparse
import json
import os
import re
import sys

OFFER_CONTACT = "support@enhancedirl.com"
PUBLIC_REPO = "https://github.com/enhanced-irl/eirl-encoder"
NVIDIA_LICENSE_URL = ("https://developer.download.nvidia.com/embedded/L4T/r32_Release_v7.6/T210/"
                      "Tegra_Software_License_Agreement-Tegra-Linux.txt")

# NVIDIA's initrd (nvidia-l4t-initrd) holds programs from Ubuntu 18.04 arm64 packages and records no versions.
# Established on Oct 16-17 2026 for the version below: of its 75 files, 42 are byte for byte the same as the image's own
# packages (§4), 32 byte for byte the same as Ubuntu's arm64 packages of these versions (the ones published on the day
# NVIDIA put the initrd together, Nov 18 2020, compared with the .deb files from Launchpad), and one is NVIDIA's xusb
# firmware. A new BSP fails the build here until this is established again.
INITRD_VERSION = "32.7.6-20241104234540"
INITRD_SOURCES = [  # (source package, version, what of the initrd)
    ("bash", "4.4.18-2ubuntu1.2", "bash"),
    ("bind9", "1:9.11.3+dfsg-1ubuntu1.13", "libdns-export, libisc-export (for dhclient)"),
    ("e2fsprogs", "1.44.1-1ubuntu1.3", "mkfs.ext4, libext2fs, libe2p, libcom_err"),
    ("glibc", "2.27-3ubuntu1.3", "the C library"),
    ("gzip", "1.6-5ubuntu1", "gzip"),
    ("isc-dhcp", "4.3.5-3ubuntu7.1", "dhclient"),
    ("keyutils", "1.5.9-9.2ubuntu2", "libkeyutils"),
    ("krb5", "1.16-2ubuntu0.2", "libgssapi_krb5, libk5crypto, libkrb5, libkrb5support"),
    ("libtirpc", "0.2.5-1.2ubuntu0.1", "libtirpc"),
    ("ncurses", "6.1-1ubuntu1.18.04", "libtinfo"),
    ("net-tools", "1.60+git20161116.90da8a0-1ubuntu1", "ifconfig"),
    ("nfs-utils", "1:1.3.4-2.1ubuntu5.3", "mount.nfs"),
    ("openssl", "1.1.1-1ubuntu2.1~18.04.6", "libcrypto"),
    ("pcre3", "2:8.39-9", "libpcre"),
    ("tar", "1.29b-2ubuntu0.1", "tar"),
    ("zlib", "1:1.2.11.dfsg-0ubuntu2", "libz"),
]
INITRD_SAME = ["acl", "attr", "coreutils", "debianutils", "gcc-8", "grep", "libselinux", "mawk", "sed", "util-linux"]


def shell_vars(path, names):
    """NAME=value lines of a shell file (pins.sh, build/build.sh): the values this image was built with."""
    found = {}
    for line in open(path, encoding="utf-8"):
        m = re.match(r"^([A-Z0-9_]+)=(\S+)\s*$", line.strip())
        if m and m.group(1) in names:
            found[m.group(1)] = m.group(2).strip("'\"")
    missing = [n for n in names if n not in found]
    if missing:
        sys.exit("sources.py: %s has no %s" % (path, ", ".join(missing)))
    return found


def dpkg_installed(root):
    """[(package, version, source, source version, maintainer)] of the installed packages."""
    out, cur = [], {}

    def flush():
        if cur.get("Package") and cur.get("Status", "").endswith(" installed"):
            src = cur.get("Source", cur["Package"]).split(" ")
            srcv = src[1].strip("()") if len(src) > 1 else cur["Version"]
            out.append((cur["Package"], cur["Version"], src[0], srcv, cur.get("Maintainer", "")))

    for line in open(os.path.join(root, "var/lib/dpkg/status"), encoding="utf-8", errors="replace"):
        line = line.rstrip("\n")
        if not line:
            flush()
            cur = {}
            continue
        if line[0] in " \t":
            continue
        k, _, v = line.partition(":")
        cur[k] = v.strip()
    flush()
    return sorted(out)


def is_nvidia(pkg, maintainer):
    return pkg.startswith("nvidia-l4t-") or "nvidia" in maintainer.lower()


def main():
    ap = argparse.ArgumentParser()
    for a in ("rootfs", "repo", "release", "version", "name", "commit", "out"):
        ap.add_argument("--" + a, required=True)
    a = ap.parse_args()
    pins = shell_vars(os.path.join(a.repo, "tools/image/pins.sh"),
                      ["BSP_URL", "BSP_SHA256", "UBUNTU_BASE_URL", "UBUNTU_BASE_SHA256", "KERNEL_RELEASE", "L4T_VERSION"])
    kb = shell_vars(os.path.join(a.repo, "build/build.sh"), ["L4T_URL", "L4T_SHA256", "RTLBT_TAG", "KERNEL_CONFIG"])
    manifest = json.load(open(os.path.join(a.release, "manifest.json"), encoding="utf-8"))
    bundle = manifest["bundle"]
    pkgs = dpkg_installed(a.rootfs)
    nvidia = [p for p in pkgs if is_nvidia(p[0], p[4])]
    initrd = [p[1] for p in nvidia if p[0] == "nvidia-l4t-initrd"]
    if initrd != [INITRD_VERSION]:
        sys.exit("sources.py: nvidia-l4t-initrd is %s, INITRD_SOURCES was established for %s: establish it again"
                 % (initrd, INITRD_VERSION))
    ubuntu = {}
    for p in pkgs:
        if not is_nvidia(p[0], p[4]):
            ubuntu.setdefault((p[2], p[3]), []).append(p[0])
    third = []
    lic = os.path.join(a.rootfs, "usr/share/doc/eirl-encoder/licenses")
    for d in sorted(os.listdir(lic)) if os.path.isdir(lic) else []:
        f = os.path.join(lic, d, "SOURCE.txt")
        if os.path.isfile(f):
            third.append((d, open(f, encoding="utf-8").read().strip()))

    L = []
    w = L.append
    w("# Sources of the EnhancedIRL Encoder image %s" % a.version)
    w("")
    w("Image `%s.img.xz` for the NVIDIA Jetson Nano Developer Kit, built from the recipe at commit `%s` of the"
      % (a.name, a.commit))
    w("EnhancedIRL Encoder repository (on GitHub, %s: the commit \"Sync with internal commit %s\")."
      % (PUBLIC_REPO, a.commit))
    w("This file says where the source code of everything in the image is, and carries our written offer for it.")
    w("")
    w("## Written offer")
    w("")
    w("For at least three years after we last distribute this image, EnhancedIRL will give anyone who asks a complete")
    w("machine-readable copy of the corresponding source code of every program in this image licensed under the GNU")
    w("General Public License, the GNU Lesser General Public License or the GNU Affero General Public License (any")
    w("version), on a medium customarily used for software interchange or as a download, for a charge no more than our")
    w("cost of physically performing that source distribution (nothing, for a download). Write to %s, naming"
      % OFFER_CONTACT)
    w("the image (`%s.img.xz`)." % a.name)
    w("")
    w("The sections below say where that source is, and they are also the directions to it that version 3 of those")
    w("licences asks for. We keep copies of what they point to, so that it stays available whatever happens to those")
    w("places.")
    w("")
    w("## 1. The recipe")
    w("")
    w("`tools/image/` of the repository at the commit above builds this image from the inputs below: the downloads")
    w("pinned by sha256 (`tools/image/pins.sh`, `build/build.sh`), the Ubuntu packages by their exact versions")
    w("(`tools/image/base.lock`, and `system/packages.lock` for the system layer). `build/build.sh` builds the kernel")
    w("modules and the device tree (§3). How: `IMAGE.md` and `UPDATES.md` §12")
    w("in the repository.")
    w("")
    w("## 2. NVIDIA Jetson Linux (L4T) R%s" % pins["L4T_VERSION"])
    w("")
    w("The `nvidia-l4t-*` packages below (the kernel `%s` with its modules and initrd among them) and the boot"
      % pins["KERNEL_RELEASE"])
    w("firmware on the card's boot partitions (made from them by NVIDIA's `flash.sh`) come as NVIDIA ships them in")
    w("its L4T R%s Driver Package for T210: %s (sha256 `%s`)." % (pins["L4T_VERSION"], pins["BSP_URL"],
                                                                     pins["BSP_SHA256"]))
    w("NVIDIA's own proprietary software is under the NVIDIA Driver License Agreement, which is in the image at")
    w("`/usr/share/doc/nvidia-tegra/L4T_End_User_License_Agreement.txt.gz` (the licences of its parts are next to it)")
    w("and at %s. The open-source parts keep their own licences." % NVIDIA_LICENSE_URL)
    w("")
    w("Their source (the Linux kernel, U-Boot, the device trees, NVIDIA's GStreamer plugins and the other GPL and LGPL")
    w("parts) is NVIDIA's `public_sources.tbz2` for R%s: %s (sha256 `%s`)."
      % (pins["L4T_VERSION"], kb["L4T_URL"], kb["L4T_SHA256"]))
    w("The kernel's configuration is NVIDIA's own, as in the kernel's `/proc/config.gz`; the repository has a copy")
    w("(`%s`)." % kb["KERNEL_CONFIG"])
    w("")
    w("| Package | Version |")
    w("|---|---|")
    for p in nvidia:
        w("| %s | %s |" % (p[0], p[1]))
    w("")
    w("NVIDIA's initrd (`nvidia-l4t-initrd`, `/boot/initrd`) holds programs from Ubuntu 18.04 arm64 packages, and NVIDIA")
    w("records no versions. Some of its files are byte for byte those of the image's own packages (%s: their"
      % ", ".join(INITRD_SAME))
    w("source is in §4). The others are byte for byte those of Ubuntu's packages built from these source packages,")
    w("the versions Ubuntu had published when NVIDIA put the initrd together (November 2020):")
    w("")
    w("| Source package (initrd) | Version | What |")
    w("|---|---|---|")
    for src, v, what in INITRD_SOURCES:
        w("| %s | %s | %s |" % (src, v, what))
    w("")
    w("## 3. Our kernel modules, device tree and firmware")
    w("")
    w("GNU General Public License version 2, built by `build/build.sh` of the repository at the commit above from the")
    w("kernel source of `public_sources.tbz2` (§2) with NVIDIA's kernel configuration:")
    w("")
    w("- `/lib/modules/%s/extra/tc358743.ko`: the TC358743 HDMI-to-CSI-2 bridge driver with `driver/tc358743-jetson.patch`."
      % pins["KERNEL_RELEASE"])
    w("- `/lib/modules/%s/updates/eirl-modems/`: `qmi_wwan`, `cdc_mbim`, `huawei_cdc_ncm`, `option` and `ipheth`,"
      % pins["KERNEL_RELEASE"])
    w("  with `driver/modems/*.patch`.")
    w("- `/lib/modules/%s/updates/eirl-bluetooth/`: `btusb` and `btrtl`, with `driver/bluetooth/*.patch`."
      % pins["KERNEL_RELEASE"])
    w("- `/boot/tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb`: NVIDIA's device tree of the B01 board with `dt/*.dtsi`.")
    w("")
    w("The Realtek RTL8761B(U) Bluetooth firmware in `/lib/firmware/rtl_bt/rtl8761bu_*.bin` is from linux-firmware,")
    w("tag `%s` (https://git.kernel.org/pub/scm/linux/kernel/git/firmware/linux-firmware.git), unmodified and"
      % kb["RTLBT_TAG"])
    w("redistributable under its licence, `/lib/firmware/rtl_bt/LICENCE.rtlwifi_firmware.txt`.")
    w("")
    w("## 4. Ubuntu 18.04 packages")
    w("")
    w("The rest of the system is Ubuntu 18.04 (\"bionic\", arm64): %s (sha256 `%s`)," % (pins["UBUNTU_BASE_URL"],
                                                                                   pins["UBUNTU_BASE_SHA256"]))
    w("then packages from Ubuntu's archive (ports.ubuntu.com: bionic, bionic-updates and bionic-security, main and")
    w("universe), installed as Ubuntu publishes them; the recipe changes some of their configuration files (the")
    w("system's name in `os-release`, `lsb-release` and `issue`, the login messages, the services that run). The image")
    w("is built from Ubuntu packages and is not Ubuntu: it is not affiliated with or endorsed by Canonical. Each")
    w("package's licence is in the image at `/usr/share/doc/<package>/copyright`.")
    w("")
    w("The source packages, at the exact versions installed: `apt-get source <package>=<version>` (with Ubuntu's")
    w("`deb-src` lines), or https://launchpad.net/ubuntu/+source/<package>/<version>.")
    w("")
    w("| Source package | Version | Installed packages |")
    w("|---|---|---|")
    for (src, srcv), bins in sorted(ubuntu.items()):
        w("| %s | %s | %s |" % (src, srcv, ", ".join(sorted(bins))))
    w("")
    w("## 5. The EnhancedIRL Encoder software %s" % manifest["version"])
    w("")
    w("Installed from the signed release v%s of the repository (`%s`, sha256 `%s`): Apache License 2.0, with the"
      % (manifest["version"], bundle["name"], bundle["sha256"]))
    w("third-party programs below. Its notices and licences are in the image at `/usr/share/doc/eirl-encoder/`.")
    for d, text in third:
        w("")
        w("**%s** (`/usr/share/doc/eirl-encoder/licenses/%s/`):" % (d, d))
        w("")
        for line in text.splitlines():
            w(line)
    w("")
    os.makedirs(os.path.dirname(a.out), exist_ok=True)
    with open(a.out, "w", encoding="utf-8") as f:
        f.write("\n".join(L))
    print("   %s: %d NVIDIA packages, %d Ubuntu source packages, %d third-party programs of the release"
          % (a.out, len(nvidia), len(ubuntu), len(third)))


if __name__ == "__main__":
    main()
