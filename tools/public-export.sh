#!/usr/bin/env bash
# public-export.sh: builds the public repository (GitHub, enhanced-irl/eirl-encoder) from this one, with a
# fresh history of a single commit. The internal repository and its history are never published.
#
#   tools/public-export.sh [--ref <ref>] <target-dir>
#
# 1. `git archive <ref>` (default HEAD: committed content only) into <target-dir>, which must not exist
#    or be empty.
# 2. Drops the paths listed in tools/public-export.exclude (as of <ref>).
# 3. Runs tools/public-check.sh on the result and stops on any hit (the tree stays there to look at).
# 4. `git init -b main` and one commit, "Initial public release", by EnhancedIRL <support@enhancedirl.com>.
# 5. Prints the commands to publish it. It never adds a remote and never pushes.
#
#   --ignore-hits   for testing this script only: commits even with hits, and then refuses to print the
#                   publishing steps. Such a tree must never be pushed.
#   --tree-only     stops after step 3: the checked tree, without git (tools/public-sync.sh uses it).
set -euo pipefail

usage() { sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

ref=HEAD target="" ignore_hits=0 tree_only=0
while [ $# -gt 0 ]; do
  case $1 in
    --ref) [ $# -ge 2 ] || usage; ref=$2; shift ;;
    --ignore-hits) ignore_hits=1 ;;
    --tree-only) tree_only=1 ;;
    -h|--help) usage ;;
    -*) usage ;;
    *) [ -z "$target" ] || usage; target=$1 ;;
  esac
  shift
done
[ -n "$target" ] || usage

repo=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
commit=$(git -C "$repo" rev-parse --verify --quiet "$ref^{commit}") || { echo "public-export: unknown ref: $ref" >&2; exit 2; }

if [ -e "$target" ] && [ -n "$(ls -A "$target" 2>/dev/null)" ]; then
  echo "public-export: $target exists and is not empty" >&2; exit 2
fi
mkdir -p "$target"
target=$(cd "$target" && pwd)

echo "== 1/4 export ${commit:0:12} ($ref)"
git -C "$repo" archive --format=tar "$commit" | tar -x -C "$target"

echo "== 2/4 drop internal-only paths (tools/public-export.exclude)"
exclude=$(git -C "$repo" show "$commit:tools/public-export.exclude" 2>/dev/null) || {
  echo "public-export: $ref has no tools/public-export.exclude" >&2; exit 2; }
while read -r path _; do
  case $path in ''|'#'*) continue ;; esac
  case $path in /*|*..*) echo "public-export: bad path in the exclude file: $path" >&2; exit 2 ;; esac
  if [ -e "$target/$path" ]; then
    rm -rf "${target:?}/${path%/}"
    echo "   - $path"
  else
    echo "   (not in $ref: $path)"
  fi
done <<< "$exclude"
find "$target" -mindepth 1 -type d -empty -delete   # parents left empty (git does not keep them)

echo "== 3/4 public-check"
hits=0
"$repo/tools/public-check.sh" --dir "$target" --summary || hits=$?
if [ "$hits" != 0 ]; then
  if [ "$ignore_hits" = 0 ]; then
    echo "public-export: public-check found problems (above): nothing committed. Fix them in the internal" >&2
    echo "repository (or justify them in tools/public-check.allow) and export again." >&2
    exit 1
  fi
  echo "public-export: --ignore-hits: committing anyway, FOR TESTING ONLY" >&2
fi

if [ "$tree_only" = 1 ]; then
  [ "$hits" = 0 ] || { echo "public-export: --tree-only with hits: not usable" >&2; exit 1; }
  echo "tree only: $target (internal commit ${commit:0:12})"
  exit 0
fi

echo "== 4/4 git init + commit"
export GIT_AUTHOR_NAME=EnhancedIRL GIT_AUTHOR_EMAIL=support@enhancedirl.com
export GIT_COMMITTER_NAME=EnhancedIRL GIT_COMMITTER_EMAIL=support@enhancedirl.com
g() { git -C "$target" -c commit.gpgsign=false -c core.hooksPath=/dev/null "$@"; }
g init -q -b main
g add -A
g commit -q -m "Initial public release" -m "Exported from internal commit ${commit:0:12}."
g log --format='%h %an <%ae> %s' -1
echo "$(g ls-files | wc -l | tr -d ' ') files"

if [ "$ignore_hits" = 1 ] && [ "$hits" != 0 ]; then
  echo "NOT PUBLISHABLE: exported with --ignore-hits and public-check found problems. Do not push it." >&2
  exit 1
fi
cat <<EOF

Ready in $target. Review it, then publish by hand (this script never pushes):
  cd $target
  git remote add origin git@github.com:enhanced-irl/eirl-encoder.git
  git push -u origin main
EOF
