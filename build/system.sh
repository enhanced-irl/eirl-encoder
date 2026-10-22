#!/usr/bin/env bash
# build/system.sh — the Ubuntu packages of the system layer (UPDATES.md §12), on the release machine.
#
#   build/system.sh                → fetches the .deb files of system/packages.lock into $CACHE/debs, each checked
#                                    against its sha256 (tools/release.sh runs it; eirl-release layer takes them)
#   build/system.sh --update-lock  → resolves system/packages.txt with its dependencies over the frozen base
#                                    (tools/image/base.lock) in an Ubuntu 18.04 arm64 container, against Ubuntu's
#                                    signed archive, and rewrites system/packages.lock. Review its diff: every line is a
#                                    package every board installs
#
# The archive is ports.ubuntu.com (bionic, bionic-updates and bionic-security, main and universe). A version Ubuntu no
# longer serves has to come from the maintainers' copies (backups/eirl-encoder-image-sources/): put it in $CACHE/debs.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
CACHE="${CACHE:-$HOME/.cache/l4t-r32.7.6}"
LOCK=$HERE/system/packages.lock
ARCHIVE=http://ports.ubuntu.com/ubuntu-ports
# shellcheck source=../tools/image/pins.sh
. "$HERE/tools/image/pins.sh"
die() { echo "system: $*" >&2; exit 1; }
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

if [ "${1:-}" = --update-lock ]; then
  command -v docker >/dev/null || die "Docker is needed"
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  grep -vE '^[[:space:]]*(#|$)' "$HERE/system/packages.txt" | awk '{print $1}' > "$work/want"
  grep -vE '^[[:space:]]*(#|$)' "$HERE/tools/image/base.lock" > "$work/base"
  docker run --rm --platform linux/arm64 -v "$work:/w" "$TOOLS_BASE" bash -euo pipefail -c '
    cat > /etc/apt/sources.list <<EOF
deb '"$ARCHIVE"' bionic main universe
deb '"$ARCHIVE"' bionic-updates main universe
deb '"$ARCHIVE"' bionic-security main universe
EOF
    rm -f /etc/apt/apt.conf.d/docker-gzip-indexes   # the lists uncompressed: the script below reads them
    apt-get update -qq
    apt-get install -y -qq --no-install-recommends python3 >/dev/null
    # The base as dpkg would see it on a board: each base package with its record from the archive (NVIDIA'"'"'s,
    # which are not in it, with a minimal one). apt then resolves the layer over exactly that
    python3 - <<"PY"
import glob, re
base = dict(l.split() for l in open("/w/base"))
recs = {}
for f in glob.glob("/var/lib/apt/lists/*_Packages"):
    for r in open(f, encoding="utf-8", errors="replace").read().split("\n\n"):
        m = re.search(r"^Package: (\S+)$", r, re.M); v = re.search(r"^Version: (\S+)$", r, re.M)
        if m and v and base.get(m.group(1)) == v.group(1):
            recs[m.group(1)] = r
drop = ("Filename", "Size", "MD5sum", "SHA1", "SHA256", "SHA512", "Description-md5", "Task", "Supported", "Origin", "Bugs")
out = []
for name, ver in sorted(base.items()):
    r = recs.get(name)
    if r is None:
        out.append("Package: %s\nStatus: install ok installed\nArchitecture: arm64\nVersion: %s\n" % (name, ver))
        continue
    lines, skip = [], False
    for line in r.splitlines():
        if line and line[0] not in " \t":
            skip = line.split(":", 1)[0] in drop
        if not skip:
            lines.append(line)
    lines.insert(1, "Status: install ok installed")
    out.append("\n".join(lines) + "\n")
open("/var/lib/dpkg/status", "w", encoding="utf-8").write("\n".join(out))
missing = [n for n in base if n not in recs and not n.startswith(("nvidia-l4t-", "jetson-gpio"))]
if missing:
    raise SystemExit("base packages Ubuntu no longer serves at their version: %s" % ", ".join(missing))
PY
    # What installing the wanted packages over that base needs, exactly
    apt-get install -s --no-install-recommends $(cat /w/want) | sed -n "s/^Inst \([^ ]*\) .*(\([^ ]*\) .*\[\([^]]*\)\].*/\1 \2 \3/p" > /w/inst
    [ -s /w/inst ] || { echo "nothing to install: already in the base?"; exit 1; }
    while read -r name ver arch; do
      apt-cache show "$name=$ver" | awk -v n="$name" -v v="$ver" -v a="$arch" "
        /^Filename:/ {f=\$2} /^SHA256:/ {s=\$2} /^Size:/ {z=\$2} /^Installed-Size:/ {i=\$2}
        /^Source:/ {src=\$2; sv=\$3; gsub(/[()]/, \"\", sv)}
        /^\$/ {exit}
        END {if (src == \"\") src = n; if (sv == \"\") sv = v; print n, v, a, s, z, i, src, sv, f}"
    done < /w/inst > /w/lock
  '
  {
    echo "# system/packages.lock — the system layer's Ubuntu packages: system/packages.txt with its dependencies, resolved"
    echo "# over the frozen base (tools/image/base.lock) by build/system.sh --update-lock. Every line is installed on every"
    echo "# board and image, at this version: review a change. Columns:"
    echo "# name version arch sha256 size installed-size(KiB) source source-version path-in-archive"
    sort "$work/lock"
  } > "$LOCK.new"
  mv "$LOCK.new" "$LOCK"
  echo "system/packages.lock: $(grep -vc '^#' "$LOCK") packages"
  diff <(git -C "$HERE" show HEAD:system/packages.lock 2>/dev/null | grep -v '^#') <(grep -v '^#' "$LOCK") || true
  exit 0
fi
[ $# -eq 0 ] || die "usage: build/system.sh [--update-lock]"

# Fetch and check
mkdir -p "$CACHE/debs"
grep -vE '^[[:space:]]*(#|$)' "$LOCK" | while read -r name ver arch sum size isize src srcver path; do
  f=$CACHE/debs/$(basename "$path")
  if [ ! -f "$f" ] || [ "$(sha256 "$f")" != "$sum" ]; then
    curl -fsSL -o "$f.part" "$ARCHIVE/$path" || die "cannot fetch $name $ver ($ARCHIVE/$path): take it from the maintainers' copies"
    mv "$f.part" "$f"
  fi
  [ "$(sha256 "$f")" = "$sum" ] || die "$f: sha256 does not match system/packages.lock"
  echo "$name $ver"
done
