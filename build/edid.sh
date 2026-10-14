#!/usr/bin/env bash
# edid.sh — the EDID files the board loads into the TC358743 (eirl-hdmi-init.sh at boot, the engine when the
# preferred mode changes), built from their source of truth: edid/edid-<mode>-lpcm48k.hex, 256 bytes in hex.
#
#   build/edid.sh        → build/out/edid-<mode>-lpcm48k.bin, one per edid/*.hex
#
# Reproducible: the .bin is the .hex decoded, byte for byte. It also checks that each file is a valid EDID
# (two 128-byte blocks with their checksums) and that edid/make_edid.py, the generator, still produces it:
# to change an EDID, change make_edid.py and regenerate the .hex (python3 edid/make_edid.py out.bin <mode>,
# then the hex of out.bin on one line). tools/deploy-engine.sh (EDIDS) and the release bundle install them
# next to the engine, in /usr/lib/eirl-encoder/engine.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
OUT="${OUT:-$HERE/build/out}"
mkdir -p "$OUT"
python3 - "$HERE/edid" "$OUT" <<'PY'
import glob, os, re, subprocess, sys, tempfile
src, out = sys.argv[1], sys.argv[2]
files = sorted(glob.glob(os.path.join(src, "edid-*-lpcm48k.hex")))
if not files:
    sys.exit("edid.sh: no edid/edid-*-lpcm48k.hex")
for hexfile in files:
    name = os.path.basename(hexfile)
    mode = re.fullmatch(r"edid-(.+)-lpcm48k\.hex", name).group(1)
    with open(hexfile) as f:
        edid = bytes.fromhex(f.read().strip())
    if len(edid) != 256 or edid[:8] != b"\x00\xff\xff\xff\xff\xff\xff\x00" or sum(edid[:128]) % 256 or sum(edid[128:]) % 256:
        sys.exit("edid.sh: %s is not a valid 256-byte EDID" % name)
    with tempfile.TemporaryDirectory() as d:
        gen = os.path.join(d, "gen.bin")
        subprocess.check_call([sys.executable, os.path.join(src, "make_edid.py"), gen, mode])
        with open(gen, "rb") as f:
            if f.read() != edid:
                sys.exit("edid.sh: %s differs from what make_edid.py generates for %s: regenerate it" % (name, mode))
    dst = os.path.join(out, name[:-len(".hex")] + ".bin")
    tmp = dst + ".tmp"
    with open(tmp, "wb") as f:
        f.write(edid)
    os.chmod(tmp, 0o644)
    os.replace(tmp, dst)
    print("build/out/" + os.path.basename(dst))
PY
