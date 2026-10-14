#!/usr/bin/env bash
# public-check.sh: looks for things that must never reach the public repository: personal names, home
# paths and addresses, internal vault paths, internal links and session ids, and secrets (private keys,
# API tokens). Exits 1 if it finds anything, with one `file:line: kind` per hit.
#
#   tools/public-check.sh                 the tracked files that would be published (git ls-files minus
#                                         the paths in tools/public-export.exclude)
#   tools/public-check.sh --all           every tracked file, the excluded ones too
#   tools/public-check.sh --dir <tree>    every file under <tree> (what tools/public-export.sh runs on
#                                         the exported tree)
#   --summary                             also print the hit count per top-level directory
#
# Justified exceptions go in tools/public-check.allow (of the scanned tree, or of this repository), one
# `path:regex` per line: a hit is ignored when `path` (a shell glob, relative to the tree) matches the
# file and `regex` (Python syntax) matches the line. Keep that file tiny and comment every entry.
#
# For secrets it prints only where they are and their kind, never the value. Binary files are skipped.
set -euo pipefail

usage() { sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

repo=$(cd "$(dirname "$0")/.." && pwd)
mode=published dir="" summary=0
while [ $# -gt 0 ]; do
  case $1 in
    --all) mode=all ;;
    --dir) [ $# -ge 2 ] || usage; mode=dir dir=$2; shift ;;
    --summary) summary=1 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
  shift
done

if [ "$mode" = dir ]; then
  root=$(cd "$dir" && pwd)
else
  root=$(git -C "$repo" rev-parse --show-toplevel)
fi
allow=$root/tools/public-check.allow
[ -f "$allow" ] || allow=$repo/tools/public-check.allow
exclude=$root/tools/public-export.exclude
[ -f "$exclude" ] || exclude=$repo/tools/public-export.exclude

list=$(mktemp)
trap 'rm -f "$list"' EXIT
case $mode in
  dir) (cd "$root" && find . -type f -not -path './.git/*' -print0) > "$list" ;;
  *) git -C "$root" ls-files -z > "$list" ;;
esac

export PC_ROOT=$root PC_LIST=$list PC_ALLOW=$allow PC_SUMMARY=$summary
export PC_EXCLUDE=$([ "$mode" = published ] && echo "$exclude" || echo "")
exec python3 -I -c '
import fnmatch, os, re, sys

# (kind, regex, secret). Literal words are written so that this file does not match itself.
PATTERNS = [
    ("personal-name", r"(?i)pmor[i]llas|paumneg[r]e", False),
    ("personal-name", r"\bPau\b", False),
    ("personal-email", r"(?i)@gmail\.com\b", False),
    ("home-path", r"/User[s]/", False),
    ("home-network", r"\b192\.168\.(?:21|28)\.\d{1,3}", False),
    ("vault-path", r"\benhancedirl/(?:common|dev|prod)/", False),
    ("vault-command", r"\bvault[ ]kv\b", False),
    ("board-turn-log", r"\bf0-tur[n]o\b", False),
    ("session-id", r"\benhancedirl-[0-9a-f]{2}\b", False),
    ("internal-link", r"\bclaude\.ai/", False),
    ("private-key", r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY", True),
    ("gitlab-token", r"\bglpat-[A-Za-z0-9_-]{20,}", True),
    ("github-token", r"\bgh[pousr]_[A-Za-z0-9]{30,}|\bgithub_pat_[A-Za-z0-9_]{22,}", True),
    ("aws-key", r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b", True),
    ("stripe-key", r"\b[sr]k_(?:live|test)_[A-Za-z0-9]{10,}", True),
    ("vault-token", r"\bhv[sbr]\.[A-Za-z0-9_-]{20,}", True),
    ("slack-token", r"\bxox[abposr]-[A-Za-z0-9-]{10,}", True),
    ("google-key", r"\bAIza[0-9A-Za-z_-]{35}", True),
    ("resend-key", r"\bre_[A-Za-z0-9]{8,}_[A-Za-z0-9]{8,}", True),
    ("jwt", r"\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.", True),
    ("private-key", r"(?i)private[_-]?key\W{1,4}[A-Za-z0-9+/]{43}=", True),
]
PATTERNS = [(k, re.compile(r), s) for k, r, s in PATTERNS]

def lines(path):
    """Non-blank lines that are not comments (a comment starts the line with #)."""
    if not path or not os.path.isfile(path):
        return []
    out = []
    for n, line in enumerate(open(path, encoding="utf-8"), 1):
        line = line.rstrip("\n")
        if line.strip() and not line.lstrip().startswith("#"):
            out.append((n, line))
    return out

allow = []
for n, line in lines(os.environ["PC_ALLOW"]):
    if ":" not in line:
        sys.exit("%s:%d: expected path:regex" % (os.environ["PC_ALLOW"], n))
    glob, rx = line.split(":", 1)
    allow.append((glob.strip(), re.compile(rx)))
# Exclude entries: a path (a directory ends with /), optionally followed by blanks and a # comment
excluded = [line.split()[0] for n, line in lines(os.environ["PC_EXCLUDE"])]

def is_excluded(rel):
    for e in excluded:
        if rel == e.rstrip("/") or (e.endswith("/") and rel.startswith(e)):
            return True
    return False

root = os.environ["PC_ROOT"]
files = [f[2:] if f.startswith("./") else f
         for f in open(os.environ["PC_LIST"], "rb").read().decode("utf-8", "replace").split("\0") if f]
hits, per_dir = [], {}
for rel in sorted(files):
    if is_excluded(rel):
        continue
    path = os.path.join(root, rel)
    try:
        data = open(path, "rb").read()
    except (IsADirectoryError, FileNotFoundError):
        continue
    if b"\0" in data[:8192]:
        continue
    for n, line in enumerate(data.decode("utf-8", "replace").splitlines(), 1):
        for kind, rx, secret in PATTERNS:
            m = rx.search(line)
            if not m:
                continue
            if any(fnmatch.fnmatch(rel, g) and r.search(line) for g, r in allow):
                continue
            hits.append("%s:%d: %s" % (rel, n, kind if secret else "%s (%s)" % (kind, m.group(0))))
            top = rel.split("/", 1)[0] if "/" in rel else "(root)"
            per_dir[top] = per_dir.get(top, 0) + 1

for h in hits:
    print(h)
if os.environ["PC_SUMMARY"] == "1" and per_dir:
    print("-- hits per top-level directory")
    for d in sorted(per_dir):
        print("%6d  %s" % (per_dir[d], d))
nfiles = len(set(h.split(":", 1)[0] for h in hits))
print("public-check: %d hit(s) in %d file(s)" % (len(hits), nfiles), file=sys.stderr)
sys.exit(1 if hits else 0)
'
