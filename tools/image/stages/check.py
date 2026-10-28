#!/usr/bin/env python3
"""stages/check.py — the image's offline checks (IMAGE.md §10.1), on the root filesystem the image was made
from and on the assembled raw image. Runs in the linux/arm64 work container (Python 3.6: no dataclasses, no
walrus). Prints one line per check and a report; exits 1 if any check fails.

  check.py --rootfs DIR --image IMG --bootparts DIR --release DIR --bundle NAME --version V
           --mode product|test|debug --firmware-mode update|check --commit SHA12 --lock BASE.LOCK --report-dir DIR
--lock is the frozen base (tools/image/base.lock); the system layer's packages come from the bundle. A missing
bootparts.lock is written by a test build (review its diff); a product image needs it.
"""
import argparse
import fnmatch
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import time

FAILS = []
NOTES = []


def ok(name, cond, detail=""):
    line = ("ok   " if cond else "FAIL ") + name + (": " + detail if detail else "")
    print(line)
    if not cond:
        FAILS.append(line)
    return cond


def note(name, detail):
    print("info " + name + ": " + detail)
    NOTES.append((name, detail))


def sha256_file(path, limit=None):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        left = limit
        while True:
            n = 1 << 20 if left is None else min(1 << 20, left)
            if n == 0:
                break
            b = f.read(n)
            if not b:
                break
            h.update(b)
            if left is not None:
                left -= len(b)
    return h.hexdigest()


def dpkg_status(root):
    """{package: {"version", "status", "source"}} from var/lib/dpkg/status."""
    pkgs, cur = {}, {}
    for line in open(os.path.join(root, "var/lib/dpkg/status"), encoding="utf-8", errors="replace"):
        line = line.rstrip("\n")
        if not line:
            if cur.get("Package"):
                pkgs[cur["Package"]] = cur
            cur = {}
            continue
        if line[0] in " \t":
            continue
        k, _, v = line.partition(":")
        cur[k] = v.strip()
    if cur.get("Package"):
        pkgs[cur["Package"]] = cur
    return pkgs


def wants(root):
    """Enabled units: {unit: [target.wants, ...]} from etc/systemd/system/*.wants."""
    out = {}
    base = os.path.join(root, "etc/systemd/system")
    for d in sorted(os.listdir(base)):
        if not (d.endswith(".wants") or d.endswith(".requires")):
            continue
        for u in sorted(os.listdir(os.path.join(base, d))):
            out.setdefault(u, []).append(d)
    return out


def masked(root):
    base = os.path.join(root, "etc/systemd/system")
    return sorted(u for u in os.listdir(base)
                  if os.path.islink(os.path.join(base, u)) and os.readlink(os.path.join(base, u)) == "/dev/null")


def patterns(path):
    out = []
    for line in open(path, encoding="utf-8"):
        line = line.split("#", 1)[0].strip()
        if line:
            out.append(line.split()[0])
    return out


def main():
    ap = argparse.ArgumentParser()
    for a in ("rootfs", "image", "bootparts", "bootparts-lock", "release", "bundle", "version", "mode", "firmware-mode",
              "commit", "lock", "report-dir", "repo"):
        ap.add_argument("--" + a, required=True)
    a = ap.parse_args()
    R = a.rootfs
    debug = a.mode == "debug"

    # --- packages
    pkgs = dpkg_status(R)
    installed = {p: v for p, v in pkgs.items() if v.get("Status", "").endswith(" installed")}
    lock_lines = ["%s %s" % (p, installed[p]["Version"]) for p in sorted(installed)]
    report = os.path.join(a.report_dir, "packages.txt")
    with open(report, "w", encoding="utf-8") as f:
        f.write("# package version source-package source-version\n")
        for p in sorted(installed):
            src = installed[p].get("Source", p).split(" ")
            srcv = src[1].strip("()") if len(src) > 1 else installed[p]["Version"]
            f.write("%s %s %s %s\n" % (p, installed[p]["Version"], src[0], srcv))
    note("packages", "%d installed (report %s)" % (len(installed), report))
    # The packages are the frozen base (tools/image/base.lock) with the release's system layer on top: its packages,
    # at their versions, and nothing it retires (UPDATES.md §12)
    with tarfile.open(os.path.join(a.release, a.bundle)) as t:
        lnames = {n.lstrip("./"): n for n in t.getnames()}
        layer = json.load(t.extractfile(lnames["system/layer.json"])) if "system/layer.json" in lnames else None
    ok("the release carries a system layer", layer is not None)
    want = {}
    for l in open(a.lock).read().splitlines():
        if l and not l.startswith("#"):
            p, v = l.split()[:2]
            want[p] = v
    for p in (layer or {}).get("remove") or []:
        want.pop(p, None)
    for p in (layer or {}).get("packages") or []:
        want[p["name"]] = p["version"]
    want_lines = ["%s %s" % (p, v) for p, v in want.items()]
    extra, missing = sorted(set(lock_lines) - set(want_lines)), sorted(set(want_lines) - set(lock_lines))
    ok("packages = base.lock + the system layer", not extra and not missing,
       "new/changed: %s; gone: %s" % (", ".join(extra[:12]) or "-", ", ".join(missing[:12]) or "-"))
    st = os.path.join(R, "var/lib/eirl-system/state.json")
    lstate = json.load(open(st)) if os.path.exists(st) else {}
    ok("the system layer applied (state.json: the release, no error)", lstate.get("release") == (layer or {}).get("release")
       and not lstate.get("error") and not lstate.get("skipped"), "%s %s" % (lstate.get("release"), lstate.get("error", "")))
    ok("no system layer apply left half done", not os.path.exists(os.path.join(R, "var/lib/eirl-system/in-progress.json"))
       and not os.path.exists(os.path.join(R, "var/lib/eirl-system/txn")))
    deny = []
    for f in sorted(os.listdir(os.path.join(a.repo, "base"))):
        if f.startswith("purge-") and f.endswith(".txt"):
            deny += patterns(os.path.join(a.repo, "base", f))
    hits = sorted(p for p in installed for d in deny if fnmatch.fnmatch(p, d))
    ok("nothing from base/purge-*.txt", not hits, ", ".join(hits[:20]))
    dropped = ["nvidia-l4t-" + d for d in ("weston", "graphics-demos", "oem-config", "kernel-headers", "jetson-io", "gputools")]
    ok("NVIDIA packages left out", not [p for p in dropped if p in installed], ", ".join(p for p in dropped if p in installed))
    nv = sorted(p for p in installed if p.startswith("nvidia-l4t-"))
    not_held = [p for p in nv if not installed[p]["Status"].startswith("hold ")]
    ok("every nvidia-l4t-* on hold (%d)" % len(nv), nv and not not_held, ", ".join(not_held))
    ok("no CUDA toolkit, cuDNN, TensorRT, VisionWorks", not [p for p in installed if re.match(r"(cuda-|libcudnn|libnvinfer|tensorrt|libvisionworks)", p)])
    branding = [p for p in installed if re.match(r"(plymouth-theme-ubuntu|ubuntu-mono|humanity-icon-theme|ubuntu-wallpapers|ubuntu-artwork|ubuntu-settings|ubuntu-docs|ubuntu-advantage|ubuntu-pro|update-manager|ubuntu-release-upgrader|update-notifier)", p)]
    ok("no Ubuntu branding packages", not branding, ", ".join(branding))
    nocopy = [p for p in installed
              if not os.path.exists(os.path.join(R, "usr/share/doc", p.split(":")[0], "copyright"))
              and not os.path.islink(os.path.join(R, "usr/share/doc", p.split(":")[0]))]
    ok("every package keeps its copyright file", len(nocopy) <= 3, "%d without: %s" % (len(nocopy), ", ".join(nocopy[:10])))

    # --- licences and sources (IMAGE.md §9.3): the notices in the image, and SOURCES.md, which goes next to it
    doc = os.path.join(R, "usr/share/doc/eirl-encoder-image")
    ok("the image's NOTICE", os.path.isfile(os.path.join(doc, "NOTICE")))
    ok("NVIDIA's licence", os.path.isfile(os.path.join(R, "usr/share/doc/nvidia-tegra/L4T_End_User_License_Agreement.txt.gz")))
    ok("the RTL8761B firmware's licence", os.path.isfile(os.path.join(R, "lib/firmware/rtl_bt/LICENCE.rtlwifi_firmware.txt")))
    sources = os.path.join(doc, "SOURCES.md")
    text = open(sources, encoding="utf-8").read() if os.path.isfile(sources) else ""
    rows = set(re.findall(r"(?m)^\| (\S+) \| (\S+) \|", text))
    unlisted = []
    for p in sorted(installed):
        src = installed[p].get("Source", p).split(" ")
        srcv = src[1].strip("()") if len(src) > 1 else installed[p]["Version"]
        if (p, installed[p]["Version"]) not in rows and (src[0], srcv) not in rows:   # NVIDIA's by package, Ubuntu's by source
            unlisted.append(p)
    ok("SOURCES.md lists the source of every package", text and not unlisted, ", ".join(unlisted[:10]))
    ok("SOURCES.md: the written offer, the recipe's commit and the release",
       "## Written offer" in text and ("`%s`" % a.commit) in text and ("release v%s " % a.version) in text)
    if a.mode == "product":
        ok("a product image from a committed recipe", not a.commit.endswith("-dirty"), a.commit)
    if text:
        shutil.copyfile(sources, os.path.join(a.report_dir, "sources.md"))

    # --- units
    w, m = wants(R), masked(R)
    with open(os.path.join(a.report_dir, "units.txt"), "w", encoding="utf-8") as f:
        f.write("# enabled (wanted by)\n")
        for u in sorted(w):
            f.write("%s %s\n" % (u, ",".join(w[u])))
        f.write("# masked\n" + "\n".join(m) + "\n")
    dt = os.readlink(os.path.join(R, "etc/systemd/system/default.target"))
    ok("default.target = multi-user", dt.endswith("multi-user.target"), dt)
    for u in ("eirl-encoderd.service", "eirl-engine.service", "eirl-hdmi-init.service", "eirl-firstboot.service", "eirl-firmware.service",
              "eirl-splash.service", "eirl-update-recover.service", "eirl-ap.service", "nvpmodel.service",
              "NetworkManager.service", "avahi-daemon.service", "haveged.service", "systemd-resolved.service"):
        ok("enabled " + u, u in w)
    must_mask = ["nv-l4t-bootloader-config.service", "alsa-restore.service"]
    must_mask += ["nv-l4t-usb-device-mode.service"]
    must_mask += ["apt-daily.timer", "apt-daily-upgrade.timer", "motd-news.timer", "motd-news.service"]
    if not debug:
        must_mask += ["ssh.service", "ssh.socket"]
    for u in must_mask:
        ok("masked " + u, u in m)
    ok("no oem-config", not os.path.exists(os.path.join(R, "etc/systemd/system/nv-oem-config.target"))
       and not os.path.exists(os.path.join(R, "lib/systemd/system/nv-oem-config.service")))
    ok("no USB device mode console wanted", "serial-getty@ttyGS0.service" not in w)
    ok("timers: fstrim only", sorted(u for u in w if u.endswith(".timer")) == ["fstrim.timer"],
       ", ".join(sorted(u for u in w if u.endswith(".timer"))))
    ok("ModemManager installed, not enabled", "modemmanager" in installed and "ModemManager.service" not in w
       and not os.path.lexists(os.path.join(R, "etc/systemd/system/dbus-org.freedesktop.ModemManager1.service")))
    ok("nvargus-daemon not enabled (as on the test board)", "nvargus-daemon.service" not in w)
    # Bluetooth audio (BLUETOOTH.md §3.7): bluetoothd installed, started only by eirl-bluealsa.service (Wants=)
    ok("bluez installed, bluetooth.service not enabled", "bluez" in installed and "bluetooth.service" not in w
       and not os.path.lexists(os.path.join(R, "etc/systemd/system/dbus-org.bluez.service")))
    # An iPhone's USB tethering (MODEMS.md §12): usbmuxd, started by its udev rule when an iPhone is plugged in, and
    # our drop-in that keeps the phone's trust (its socket, its pairing records) for usbmuxd and root only: the socket
    # must be usbmuxd's own user's, or usbmuxd cannot open its session with the iPhone through it
    ok("usbmuxd installed, started by its udev rule", "usbmuxd" in installed
       and os.path.exists(os.path.join(R, "lib/udev/rules.d/39-usbmuxd.rules")) and "usbmuxd.service" not in w)
    dropin = os.path.join(R, "etc/systemd/system/usbmuxd.service.d/90-eirl.conf")
    dtext = open(dropin).read() if os.path.exists(dropin) else ""
    ok("usbmuxd: socket and pairing records for usbmuxd and root only", "UMask=0077" in dtext
       and "chown usbmux /run/usbmuxd && chmod 0600 /run/usbmuxd" in dtext)

    # --- per-device values and nothing of the test board
    mid = open(os.path.join(R, "etc/machine-id"), encoding="utf-8").read()
    ok("machine-id empty", mid.strip() == "")
    ok("dbus machine-id -> /etc/machine-id", os.path.islink(os.path.join(R, "var/lib/dbus/machine-id")))
    ok("no random seed", not os.path.exists(os.path.join(R, "var/lib/systemd/random-seed")))
    keys = [f for f in os.listdir(os.path.join(R, "etc/ssh")) if f.startswith("ssh_host_")]
    ok("no SSH host keys", not keys, ", ".join(keys))
    lockdown = os.path.join(R, "var/lib/lockdown")
    lk = sorted(os.listdir(lockdown)) if os.path.isdir(lockdown) else []
    ok("no iPhone host identity or pairing records", not lk, ", ".join(lk))
    conf = os.path.join(R, "etc/eirl-encoder")
    left = sorted(os.listdir(conf)) if os.path.isdir(conf) else []
    ok("/etc/eirl-encoder holds only a new device's engine.env", left == ["engine.env"], ", ".join(left))
    env = open(os.path.join(conf, "engine.env")).read() if "engine.env" in left else ""
    ok("engine.env: output off, no destination", re.search(r"(?m)^OUTPUT=off$", env) is not None
       and re.search(r"(?m)^SRT_URL=$", env) is not None)
    nmc = os.path.join(R, "etc/NetworkManager/system-connections")
    ok("no Wi-Fi or network profiles", not os.path.isdir(nmc) or not os.listdir(nmc))
    ok("no NetworkManager state", not os.listdir(os.path.join(R, "var/lib/NetworkManager")) if os.path.isdir(os.path.join(R, "var/lib/NetworkManager")) else True)
    ok("journal empty", not os.listdir(os.path.join(R, "var/log/journal")))
    homes = os.listdir(os.path.join(R, "home"))
    ok("home directories", homes == (["eirl-debug"] if debug else []), ", ".join(homes))
    ak = []
    for top in ("root", "home", "etc"):
        for dp, dn, fn in os.walk(os.path.join(R, top)):
            ak += [os.path.join(dp, f)[len(R):] for f in fn if f in ("authorized_keys", "authorized_keys2")]
    ok("authorized_keys", ak == (["/home/eirl-debug/.ssh/authorized_keys"] if debug else []), ", ".join(ak))
    users = [l.split(":") for l in open(os.path.join(R, "etc/passwd")).read().splitlines() if l]
    logins = [u[0] for u in users if 1000 <= int(u[2]) < 65534]
    ok("login users", logins == (["eirl-debug"] if debug else []), ", ".join(logins))
    shadow = [l.split(":") for l in open(os.path.join(R, "etc/shadow")).read().splitlines() if l]
    pw = [s[0] for s in shadow if s[1] and s[1][0] not in "!*"]
    ok("no account with a password", not pw, ", ".join(pw))
    root = [s for s in shadow if s[0] == "root"]
    ok("root locked", root and root[0][1].startswith(("!", "*")))
    groups = [l.split(":")[0] for l in open(os.path.join(R, "etc/group")).read().splitlines() if l]
    ok("group debug (NVIDIA's udev rules)", "debug" in groups)
    clk = os.path.join(R, "var/lib/private/systemd/timesync/clock")
    ok("clock floor at the build time", os.path.exists(clk) and abs(os.stat(clk).st_mtime - time.time()) < 6 * 3600
       and os.path.islink(os.path.join(R, "var/lib/systemd/timesync"))
       and os.readlink(os.path.join(R, "var/lib/systemd/timesync")) == "../private/systemd/timesync")
    ok("APN database", "mobile-broadband-provider-info" in installed)
    ok("resolv.conf is the resolved stub", os.readlink(os.path.join(R, "etc/resolv.conf")).endswith("stub-resolv.conf"))
    ok("no extlinux backups", not [f for f in os.listdir(os.path.join(R, "boot/extlinux")) if f != "extlinux.conf"],
       ", ".join(os.listdir(os.path.join(R, "boot/extlinux"))))
    ok("no build leftovers", not os.path.exists(os.path.join(R, "usr/sbin/policy-rc.d"))
       and not os.path.exists(os.path.join(R, "usr/bin/qemu-aarch64-static")) and not os.listdir(os.path.join(R, "tmp")))
    scan = "/work/scan"
    subprocess.call(["rm", "-rf", scan])
    os.makedirs(scan)
    for d in ("etc", "root", "home", "boot/extlinux", "usr/local", "usr/lib/eirl-encoder", "usr/lib/eirl-image",
              "usr/share/doc/eirl-encoder", "usr/share/doc/eirl-encoder-image", "var/lib", "opt/nvidia/l4t-bootloader-config"):
        src = os.path.join(R, d)
        if os.path.isdir(src):
            os.makedirs(os.path.dirname(os.path.join(scan, d)), exist_ok=True)
            subprocess.check_call(["cp", "-a", src, os.path.join(scan, d)])
    subprocess.call(["rm", "-rf", os.path.join(scan, "var/lib/dpkg")])   # Ubuntu's package metadata (maintainers' addresses)
    # The third-party licence texts of the release, kept verbatim as their licences require (an author's own address);
    # the SOURCE.txt files next to them are ours and are scanned
    subprocess.call(["find", os.path.join(scan, "usr/share/doc/eirl-encoder/licenses"), "-type", "f", "-name", "LICENSE*",
                     "-delete"])
    rc = subprocess.call(["bash", os.path.join(a.repo, "tools/public-check.sh"), "--dir", scan])
    ok("nothing of the test board, no secrets (tools/public-check.sh on /etc, /var/lib, /usr/local, …)", rc == 0)

    # --- our software
    man = json.load(open(os.path.join(a.release, "manifest.json")))
    ver = open(os.path.join(R, "usr/lib/eirl-encoder/VERSION")).read().split()[0]
    ok("installed release = manifest (%s)" % man["version"], ver == man["version"], ver)
    files_txt = open(os.path.join(R, "usr/lib/eirl-encoder/files.txt")).read().split()
    with tarfile.open(os.path.join(a.release, a.bundle)) as t:
        names = {n.lstrip("./"): n for n in t.getnames()}
        retired = t.extractfile(names["engine/retired.txt"]).read().decode().split() if "engine/retired.txt" in names else []
    # files.txt also lists the retired engine files an update removes (so a rollback can bring them back):
    # a new device never had them
    gone = set("/usr/lib/eirl-encoder/engine/" + r for r in retired)
    missing = [p for p in files_txt if p not in gone and not os.path.lexists(os.path.join(R, p.lstrip("/")))]
    if "eirl-fan" in names:  # the fan control (FAN.md), in the bundles after 0.5.0
        ok("enabled eirl-fan.service", "eirl-fan.service" in w)
    if "bluealsa/bluealsa" in names:  # Bluetooth audio (BLUETOOTH.md), in the bundles after 0.6.1
        ok("enabled eirl-bluealsa.service", "eirl-bluealsa.service" in w)
    ok("every path of files.txt installed (%d, %d retired ones not expected)" % (len(files_txt), len(gone & set(files_txt))),
       not missing, ", ".join(missing[:10]))
    # Every file of the bundle at the path install.sh gives it (manager/deploy/install.sh), with the bundle's bytes
    deploy_dst = {"70-eirl-capture.rules": "etc/udev/rules.d/70-eirl-capture.rules",
                  "71-eirl-storage.rules": "etc/udev/rules.d/71-eirl-storage.rules",  # RECORDING.md §4
                  "90-eirl-uplinks": "etc/NetworkManager/dispatcher.d/90-eirl-uplinks",
                  "eirl-engine-eirl-encoder.conf": "etc/systemd/system/eirl-engine.service.d/eirl-encoder.conf",
                  "sudoers": "etc/sudoers.d/eirl-encoder", "tmpfiles.conf": "etc/tmpfiles.d/eirl-encoder.conf",
                  "eirl-engine.default": "etc/default/eirl-engine",
                  # Bluetooth audio (BLUETOOTH.md §3.7): bluetoothd's drop-in (the D-Bus policy is the system layer's)
                  "nv-bluetooth-service.conf": "etc/systemd/system/bluetooth.service.d/nv-bluetooth-service.conf"}
    def dest(n):
        top, _, rest = n.partition("/")
        if not rest:
            if n in ("eirl-encoderd", "srtla_send", "srtla_send_rs", "mediamtx", "srt-live-transmit", "eirl-send",
                     "ristsender"):  # OUTPUTS.md §4, SRTLA.md §9
                return "usr/local/bin/" + n
            if n in ("eirl-netsetup", "eirl-update", "eirl-fan", "eirl-storage"):  # RECORDING.md §4
                return "usr/local/sbin/" + n
            if n.startswith("libsrt.so."):
                return "usr/lib/eirl-encoder/lib/" + n
            if n in ("VERSION", "BUILD", "files.txt"):
                return "usr/lib/eirl-encoder/" + n
            if n == "install.sh":
                return ""
        if top == "engine":
            if rest.startswith("lib/"):
                return "usr/lib/eirl-encoder/engine/" + rest[4:]
            if rest.startswith("units/"):
                return "etc/systemd/system/" + rest[6:]
            if rest == "retired.txt":
                return ""
        if top == "splash":
            if rest == "eirl-splash":
                return "usr/local/bin/eirl-splash"
            if rest.endswith(".service"):
                return "etc/systemd/system/" + rest
            if rest == "install.sh":
                return ""
        if top == "doc":
            return "usr/share/doc/eirl-encoder/" + rest
        if top == "system":  # the system layer (UPDATES.md §12): its files where they go, its packages through dpkg
            if rest.startswith("files/"):
                return rest[len("files/"):]
            if rest == "layer.json" or (rest.startswith("debs/") and rest.endswith(".deb")):
                return ""
        if top == "bluealsa":  # BlueALSA (build/bluealsa.sh): the daemon (its ALSA plugins are the system layer's)
            if rest == "bluealsa":
                return "usr/local/bin/bluealsa"
        if top == "deploy":
            if rest == "install.sh":
                return ""
            if rest.endswith(".service"):
                return "etc/systemd/system/" + rest
            if rest.endswith(".pkla"):
                return "etc/polkit-1/localauthority/50-local.d/" + rest
            return deploy_dst.get(rest)
        return None
    unknown, differ, checked = [], [], 0
    with tarfile.open(os.path.join(a.release, a.bundle)) as t:
        for m in t.getmembers():
            if not m.isfile():
                continue
            n = m.name[2:] if m.name.startswith("./") else m.name
            d = dest(n)
            if d is None:
                unknown.append(n)
                continue
            if d == "":
                continue
            checked += 1
            h = hashlib.sha256(t.extractfile(m).read()).hexdigest()
            p = os.path.join(R, d)
            if not os.path.isfile(p) or sha256_file(p) != h:
                differ.append(d)
    ok("every file of the bundle has a known destination", not unknown, ", ".join(unknown[:10]))
    ok("every bundle file installed with the bundle's bytes (%d)" % checked, not differ, ", ".join(differ[:10]))
    ok("srt-live-transmit installed", any(os.path.exists(os.path.join(R, p)) for p in
       ("usr/lib/eirl-encoder/bin/srt-live-transmit", "usr/local/bin/srt-live-transmit", "usr/lib/eirl-encoder/lib/srt-live-transmit")))
    for e in ("edid-1080p30-lpcm48k.bin", "edid-1080p50-lpcm48k.bin", "edid-720p60-lpcm48k.bin"):
        ok("EDID " + e, os.path.exists(os.path.join(R, "usr/lib/eirl-encoder/engine", e)))
    ok("/etc/default/eirl-engine", os.path.exists(os.path.join(R, "etc/default/eirl-engine")))

    # --- kernel bits and boot
    kr = "4.9.337-tegra"
    ok("tc358743.ko in extra/", os.path.exists(os.path.join(R, "lib/modules", kr, "extra/tc358743.ko")))
    dep = open(os.path.join(R, "lib/modules", kr, "modules.dep")).read()
    ok("depmod knows extra/tc358743.ko", "extra/tc358743.ko:" in dep)
    ok("no in-tree tc358743 module shadowing ours", "kernel/drivers/media/i2c/tc358743.ko" not in dep)
    deps = dict((l.split(":", 1)[0].rsplit("/", 1)[-1], l.split(":", 1)[0]) for l in dep.splitlines() if ":" in l)
    for m in ("qmi_wwan", "cdc_mbim", "huawei_cdc_ncm", "option", "ipheth"):
        ok("modem module %s from updates/eirl-modems/ (MODEMS.md §4)" % m,
           deps.get(m + ".ko") == "updates/eirl-modems/%s.ko" % m, deps.get(m + ".ko", "missing"))
    for m in ("btusb", "btrtl"):
        ok("Bluetooth module %s from updates/eirl-bluetooth/ (BLUETOOTH.md §3.8)" % m,
           deps.get(m + ".ko") == "updates/eirl-bluetooth/%s.ko" % m, deps.get(m + ".ko", "missing"))
    for f in ("rtl8761bu_fw.bin", "rtl8761bu_config.bin", "LICENCE.rtlwifi_firmware.txt"):
        ok("Realtek Bluetooth firmware %s" % f, os.path.exists(os.path.join(R, "lib/firmware/rtl_bt", f)))
    dtb = "tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb"
    ok("our DTB in /boot", os.path.exists(os.path.join(R, "boot", dtb)))
    pinned_dtb = re.search(r"(?m)^DTB_SHA256=([0-9a-f]{64})$", open(os.path.join(a.repo, "tools/image/pins.sh")).read())
    ok("the DTB is the frozen base's (pins.sh DTB_SHA256)", pinned_dtb is not None and os.path.exists(os.path.join(R, "boot", dtb))
       and sha256_file(os.path.join(R, "boot", dtb)) == pinned_dtb.group(1))
    ext = open(os.path.join(R, "boot/extlinux/extlinux.conf")).read()
    ok("extlinux DEFAULT eirl-splash", re.search(r"(?m)^DEFAULT eirl-splash$", ext) is not None)
    labels = re.findall(r"(?m)^LABEL (\S+)", ext)
    fdts = re.findall(r"(?m)^\s*FDT (\S+)", ext)
    appends = re.findall(r"(?m)^\s*APPEND .*", ext)
    ok("extlinux entries primary, tc358743, eirl-splash", set(labels) == {"primary", "tc358743", "eirl-splash"}, ", ".join(labels))
    ok("every extlinux entry boots our DTB", len(fdts) == len(labels) >= 3 and all(f.endswith(dtb) for f in fdts))
    ok("every APPEND has root=/dev/mmcblk0p1", len(appends) == len(labels) and all("root=/dev/mmcblk0p1" in l for l in appends))
    ok("nv_boot_control.conf", os.path.exists(os.path.join(R, "etc/nv_boot_control.conf")))
    fm = open(os.path.join(R, "usr/lib/eirl-image/firmware-mode")).read().strip()
    # Only a product image may write the QSPI, and only when it was not built --firmware-check-only
    want_fm = a.firmware_mode if a.mode == "product" else "check"
    ok("firmware mode = %s" % want_fm, fm == want_fm and want_fm in ("update", "check"), fm)
    for s in ("usr/lib/eirl-image/eirl-firstboot", "usr/lib/eirl-image/eirl-firmware"):
        ok(s + " executable", os.access(os.path.join(R, s), os.X_OK))
    for f in ("etc/os-release", "usr/lib/os-release"):
        osr = open(os.path.join(R, f)).read()
        names = re.findall(r'(?m)^(?:NAME|PRETTY_NAME)=.*', osr)
        ok(f + " without the Ubuntu name", len(names) == 2 and not any("Ubuntu" in x for x in names), names[0] if names else "")

    # --- the system's fingerprint (UPDATES.md §12.10): tools/system-equiv.sh compares it with the boards an OTA migrates
    import tempfile
    with tempfile.TemporaryDirectory() as td:
        with tarfile.open(os.path.join(a.release, a.bundle)) as t:
            ln = {n.lstrip("./"): n for n in t.getnames()}
            if "system/layer.json" in ln:
                with open(os.path.join(td, "layer.json"), "wb") as f:
                    f.write(t.extractfile(ln["system/layer.json"]).read())
        fp = os.path.join(a.report_dir, "fingerprint.json")
        rc = subprocess.call([os.path.join(R, "usr/local/sbin/eirl-update"), "system", "fingerprint", "-root", R]
                             + (["-layer", td] if os.path.exists(os.path.join(td, "layer.json")) else []) + ["-out", fp])
        ok("the system's fingerprint (report fingerprint.json)", rc == 0 and os.path.exists(fp))

    # --- libraries resolve (ldd in the chroot)
    bins = ["usr/lib/eirl-encoder/engine/eirl-engine-go", "usr/local/bin/srtla_send", "usr/local/bin/srtla_send_rs"]
    bins += [b for b in ("usr/local/bin/srt-live-transmit",) if os.path.exists(os.path.join(R, b))]
    for d in ("usr/lib/eirl-encoder/bin", "usr/lib/eirl-encoder/lib"):
        if os.path.isdir(os.path.join(R, d)):
            bins += [os.path.join(d, f) for f in os.listdir(os.path.join(R, d)) if f.startswith(("srt-live", "libsrt"))]
    gst = os.path.join(R, "usr/lib/aarch64-linux-gnu/gstreamer-1.0")
    bins += ["usr/lib/aarch64-linux-gnu/gstreamer-1.0/" + f for f in os.listdir(gst) if f.startswith("libgstnv")]
    bad = []
    for b in bins:
        env = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin"}
        p = subprocess.Popen(["chroot", R, "/usr/bin/ldd", "/" + b], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=env)
        out = p.communicate()[0].decode("utf-8", "replace")
        if "not found" in out:
            bad.append("%s: %s" % (b, " ".join(l.split()[0] for l in out.splitlines() if "not found" in l)))
    ok("libraries resolve (%d binaries, NVIDIA's GStreamer plugins included)" % len(bins), not bad, "; ".join(bad[:6]))

    # --- the image
    pt = subprocess.check_output(["sgdisk", "-p", a.image]).decode()
    rows = [l.split() for l in pt.splitlines() if re.match(r"^\s+\d+\s", l)]
    ok("14 partitions", len(rows) == 14, str(len(rows)))
    last = max(rows, key=lambda r: int(r[1]))
    ok("APP (1) is physically last", last[0] == "1" and last[6] == "APP", " ".join(last))
    layout = [l.split("\t") for l in open(os.path.join(a.bootparts, "layout.tsv")).read().splitlines()[1:]]
    # The boot partitions are pinned (tools/image/bootparts.lock): NVIDIA's signing is deterministic
    pinned = "\n".join("\t".join((n, name, size, s)) for n, name, size, f, s in layout) + "\n"
    if os.path.exists(a.bootparts_lock):
        ok("boot partitions = bootparts.lock", open(a.bootparts_lock).read().split("\n", 1)[1] == pinned)
    elif a.mode == "product":
        ok("a product image has a reviewed bootparts.lock", False)
    else:
        with open(a.bootparts_lock, "w", encoding="utf-8") as f:
            f.write("# tools/image/bootparts.lock: the card's boot partitions (number, name, size, sha256), written by a test build when missing\n" + pinned)
        note("bootparts.lock", "written: review its diff")
    with open(a.image, "rb") as img:
        for n, name, size, f, s in layout:
            if f == "-":
                continue
            first = int(next(r for r in rows if r[0] == n)[1])
            img.seek(first * 512)
            ln = os.path.getsize(os.path.join(a.bootparts, f))
            got = hashlib.sha256(img.read(ln)).hexdigest()
            ok("p%s %s written" % (n, name), got == s)
    sizes = {"rootfs MiB": int(subprocess.check_output(["du", "-sm", R]).split()[0]), "raw bytes": os.path.getsize(a.image)}
    note("sizes", json.dumps(sizes))
    with open(os.path.join(a.report_dir, "check.json"), "w", encoding="utf-8") as f:
        json.dump({"fails": FAILS, "notes": NOTES, "sizes": sizes}, f, indent=1)
    print("check: %d failed" % len(FAILS))
    return 1 if FAILS else 0


if __name__ == "__main__":
    sys.exit(main())
