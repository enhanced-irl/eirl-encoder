#!/usr/bin/env bash
# public-sync.sh: brings the public repository (GitHub, enhanced-irl/eirl-encoder) up to an internal commit, as
# ONE new commit on top of its history. It never rewrites that history and never pushes.
#
#   tools/public-sync.sh [--ref <ref>] <clone-dir>
#
# <clone-dir> is a clone of the public repository (git clone https://github.com/enhanced-irl/eirl-encoder.git),
# clean, on main and up to date with its origin/main.
# 1. Exports <ref> (default HEAD) with tools/public-export.sh --tree-only: the same excluded paths and the same
#    public-check, which must find nothing.
# 2. Makes the clone's files exactly that tree (what was deleted internally is deleted there too).
# 3. One commit, "Sync with internal commit <sha12>", by EnhancedIRL <support@enhancedirl.com>. Its body says
#    "Exported from internal commit <sha12>.", which tools/release.sh publish checks before a release.
#    With nothing to change, it says so and commits nothing.
# 4. Prints what changed and the push command. Pushing publishes: only with the owner's OK.
set -euo pipefail

usage() { sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

ref=HEAD clone=""
while [ $# -gt 0 ]; do
  case $1 in
    --ref) [ $# -ge 2 ] || usage; ref=$2; shift ;;
    -h|--help|-*) usage ;;
    *) [ -z "$clone" ] || usage; clone=$1 ;;
  esac
  shift
done
[ -n "$clone" ] || usage

repo=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
commit=$(git -C "$repo" rev-parse --verify --quiet "$ref^{commit}") || { echo "public-sync: unknown ref: $ref" >&2; exit 2; }
short=${commit:0:12}

c() { git -C "$clone" -c commit.gpgsign=false -c core.hooksPath=/dev/null "$@"; }
[ -d "$clone/.git" ] || { echo "public-sync: $clone is not a git clone" >&2; exit 2; }
case "$(c remote get-url origin 2>/dev/null)" in
  *github.com[:/]enhanced-irl/eirl-encoder|*github.com[:/]enhanced-irl/eirl-encoder.git) ;;
  *) echo "public-sync: $clone's origin is not github.com/enhanced-irl/eirl-encoder" >&2; exit 2 ;;
esac
[ "$(c rev-parse --abbrev-ref HEAD)" = main ] || { echo "public-sync: $clone is not on main" >&2; exit 2; }
[ -z "$(c status --porcelain)" ] || { echo "public-sync: $clone has local changes" >&2; exit 2; }
c fetch -q origin
[ "$(c rev-parse HEAD)" = "$(c rev-parse origin/main)" ] ||
  { echo "public-sync: $clone's main is not origin/main (pull, or push what is pending, first)" >&2; exit 2; }
if c log -1 --format=%B | grep -q "Exported from internal commit $short\."; then
  echo "public-sync: the public main is already the export of $short"; exit 0
fi

echo "== 1/3 export $short ($ref)"
tree=$(mktemp -d)
trap 'rm -rf "$tree"' EXIT
"$repo/tools/public-export.sh" --ref "$commit" --tree-only "$tree/t" | sed 's/^/   /'

echo "== 2/3 replace the clone's files"
c ls-files -z > "$tree/files"
(cd "$clone" && xargs -0 rm -f -- < "$tree/files")
# The directories those files leave empty (never anything under .git: only tracked paths' parents)
(cd "$clone" && tr '\0' '\n' < "$tree/files" | while IFS= read -r f; do
  d=$(dirname "$f"); [ "$d" = . ] || rmdir -p "$d" 2>/dev/null || true
done)
(cd "$tree/t" && tar -cf - .) | (cd "$clone" && tar -xf -)
c add -A
if c diff --cached --quiet; then
  echo "public-sync: no change in the public tree for $short (nothing committed)"; exit 0
fi

echo "== 3/3 commit"
export GIT_AUTHOR_NAME=EnhancedIRL GIT_AUTHOR_EMAIL=support@enhancedirl.com
export GIT_COMMITTER_NAME=EnhancedIRL GIT_COMMITTER_EMAIL=support@enhancedirl.com
c commit -q -m "Sync with internal commit $short" -m "Exported from internal commit $short."
c log --format='%h %an <%ae> %s' -1
c diff --stat HEAD~1 HEAD | tail -1
cat <<EOF

Committed in $clone (not pushed). Review it (git -C $clone show --stat), then, with the owner's OK:
  git -C $clone push origin main
EOF
