#!/usr/bin/env bash
# tools/release.sh — builds and signs a release of the whole encoder software (UPDATES.md §6). Runs on
# the maintainer's machine (macOS or Linux, with Go and Docker), never on the board.
#
#   tools/release.sh build [options]      → build/out/release/<version>/{bundle, manifest.json, manifest.json.sig}
#   tools/release.sh publish [--yes]      → GitHub release v<VERSION> with those three files (explicit step)
#
# build options:
#   --min-from X.Y.Z    oldest installed version that may update directly (manifest "minFrom")
#   --notes DIR         release notes DIR/{en,es,fr}.md (default release/notes/<version>/; en.md required)
#   --engine-bin PATH   a prebuilt Go engine (linux/arm64) instead of building it (build/engine.sh, Docker)
#   --local DIR         also lay the release out in DIR, to serve it to a test board over HTTP:
#                         python3 -m http.server --directory DIR 8000
#                         (board: EIRL_ENCODERD_OPTS=-update-url http://<this machine>:8000/manifest.json)
#   Only with --local (test releases; refused otherwise):
#   --version V         instead of the VERSION file (e.g. 0.1.0-test.2: a prerelease sorts below 0.1.0)
#   --key-stdin         sign with a THROWAWAY seed (base64) read from stdin; needs --pubkey
#   --pubkey B64        the throwaway public key: the binaries are a TEST build that trusts only it
#   --skip-engine       an app-only bundle (the engine on the board is left as it is)
#   --inject-failure W  a broken bundle to test the rollback: W=app (the new app never starts) or
#                       W=install (install.sh fails)
#   --allow-dirty       build from a tree with uncommitted changes
#
# The production signing seed is read ONLY through a pipe from the maintainers' secret store (never written
# to disk, never printed): EIRL_RELEASE_KEY_CMD is the command that prints it (base64) on stdout. A
# production build refuses to start without it. The signature is checked against the compiled-in production key.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
REPO=enhanced-irl/eirl-encoder
die() { echo "release: $*" >&2; exit 1; }

cmd=${1:-}
[ $# -gt 0 ] && shift
case "$cmd" in
  build|publish) ;;
  *) sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 64 ;;
esac

LOCAL="" VERSION_OPT="" KEY_STDIN="" PUBKEY="" SKIP_ENGINE="" ENGINE_BIN="" INJECT="" ALLOW_DIRTY="" MIN_FROM="" NOTES="" YES=""
while [ $# -gt 0 ]; do
  case "$1" in
    --local) LOCAL=${2:?}; shift ;;
    --version) VERSION_OPT=${2:?}; shift ;;
    --key-stdin) KEY_STDIN=1 ;;
    --pubkey) PUBKEY=${2:?}; shift ;;
    --skip-engine) SKIP_ENGINE=1 ;;
    --engine-bin) ENGINE_BIN=${2:?}; shift ;;
    --inject-failure) INJECT=${2:?}; shift ;;
    --allow-dirty) ALLOW_DIRTY=1 ;;
    --min-from) MIN_FROM=${2:?}; shift ;;
    --notes) NOTES=${2:?}; shift ;;
    --yes) YES=1 ;;
    *) die "unknown option $1" ;;
  esac
  shift
done

# The Go half of the tool, for this machine
tooldir=$(mktemp -d)
trap 'rm -rf "$tooldir"' EXIT
(cd manager && go build -o "$tooldir/eirl-release" ./cmd/eirl-release)
REL=$tooldir/eirl-release

file_version() { tr -d ' \n' < VERSION; }

if [ "$cmd" = publish ]; then
  [ -z "$LOCAL$VERSION_OPT$KEY_STDIN$PUBKEY$SKIP_ENGINE$INJECT" ] || die "publish takes only --yes"
  ver=$(file_version)
  out=build/out/release/$ver
  bundle=$out/eirl-encoder-$ver-linux-arm64.tar.gz
  for f in "$bundle" "$out/manifest.json" "$out/manifest.json.sig"; do [ -f "$f" ] || die "missing $f: run tools/release.sh build first"; done
  # Only a release signed with the PRODUCTION key is ever published
  "$REL" verify -manifest "$out/manifest.json" -sig "$out/manifest.json.sig" -bundle "$bundle"
  notes=release/notes/$ver/en.md
  [ -f "$notes" ] || die "missing $notes"
  command -v gh >/dev/null || die "gh (GitHub CLI) is needed"
  if gh release view "v$ver" --repo "$REPO" >/dev/null 2>&1; then die "v$ver is already released on GitHub"; fi
  echo "About to publish v$ver to https://github.com/$REPO/releases (bundle $(du -h "$bundle" | cut -f1)) as the LATEST release:"
  echo "every encoder will be offered it within ~6 h."
  if [ -z "$YES" ]; then
    read -r -p "Type the version to confirm: " answer
    [ "$answer" = "$ver" ] || die "not confirmed"
  fi
  gh release create "v$ver" --repo "$REPO" --target main --title "eirl-encoder $ver" --notes-file "$notes" --latest \
    "$bundle" "$out/manifest.json" "$out/manifest.json.sig"
  echo "published v$ver. Tag the working repo too: git tag -a v$ver -m 'eirl-encoder $ver' && git push origin v$ver"
  echo "check it as a board would: $REL check -url https://github.com/$REPO/releases/latest/download/manifest.json -current <installed>"
  exit 0
fi
# A production release needs the command that prints the signing seed; checked before building anything
[ -n "$KEY_STDIN" ] || [ -n "${EIRL_RELEASE_KEY_CMD:-}" ] ||
  die "set EIRL_RELEASE_KEY_CMD to the command that prints the production signing seed (maintainers' secret store)"

# --- build ------------------------------------------------------------------------------------------------
if [ -z "$LOCAL" ]; then
  [ -z "$VERSION_OPT$KEY_STDIN$PUBKEY$SKIP_ENGINE$INJECT$ALLOW_DIRTY" ] ||
    die "--version, --key-stdin, --pubkey, --skip-engine, --inject-failure and --allow-dirty are for --local test releases only"
fi
if [ -n "$KEY_STDIN" ] || [ -n "$PUBKEY" ]; then
  [ -n "$KEY_STDIN" ] && [ -n "$PUBKEY" ] || die "--key-stdin and --pubkey go together"
fi
case "$INJECT" in ""|app|install) ;; *) die "--inject-failure app|install" ;; esac
if [ -z "$SKIP_ENGINE" ]; then
  # The engine runs as its own system user (no login, no sudo): a unit with any other User= would make
  # every board run the engine as that account (security review). Until the eirl-engine user is in main,
  # only app-only bundles (--skip-engine) can be built
  # systemd takes the last User= and allows blanks around it; a drop-in can override it
  engine_user=$(sed -nE 's/^[[:space:]]*User[[:space:]]*=[[:space:]]*([^[:space:]]*)[[:space:]]*$/\1/p' systemd/eirl-engine.service | tail -1)
  [ "$engine_user" = eirl-engine ] ||
    die "systemd/eirl-engine.service runs as '${engine_user:-?}', not eirl-engine: no engine in a release before that (or --local --skip-engine)"
  for f in manager/deploy/eirl-engine-*.conf; do
    [ -f "$f" ] || continue
    if grep -qE '^[[:space:]]*(User|DynamicUser)[[:space:]]*=' "$f"; then
      die "$f (eirl-engine's drop-in) sets User= or DynamicUser=: the engine must run as eirl-engine"
    fi
  done
fi
ver=${VERSION_OPT:-$(file_version)}
"$REL" semver "$ver" >/dev/null || die "not a semantic version: $ver"
if [ -z "$ALLOW_DIRTY" ] && [ -n "$(git status --porcelain)" ]; then die "uncommitted changes (commit them, or --local --allow-dirty)"; fi
if [ -z "$LOCAL" ] && git rev-parse -q --verify "refs/tags/v$ver" >/dev/null; then die "tag v$ver already exists: bump VERSION"; fi
commit=$(git rev-parse --short=12 HEAD)
released=$(date -u +%Y-%m-%dT%H:%M:%SZ)
out=build/out/release/$ver
rm -rf "$out" && mkdir -p "$out"
# Nothing but the signing step reads stdin (it may carry the throwaway seed)
exec 3<&0 0</dev/null

echo "== eirl-encoder $ver ($commit)${LOCAL:+ — LOCAL TEST RELEASE}"
echo "== tests"
(cd manager && go test ./... >/dev/null) || die "Go tests failed"

echo "== app package (manager/build.sh)"
EIRL_VERSION=$ver EIRL_PUBKEY=$PUBKEY EIRL_SKIP_TESTS=1 manager/build.sh >/dev/null
PKG=manager/dist/pkg

if [ -z "$SKIP_ENGINE" ]; then
  echo "== engine"
  if [ -z "$ENGINE_BIN" ]; then
    EIRL_VERSION=$ver build/engine.sh >/dev/null
    ENGINE_BIN=build/out/eirl-engine
  fi
  file "$ENGINE_BIN" | grep -q 'ARM aarch64' || die "$ENGINE_BIN is not a linux/arm64 binary"
  # The same files tools/deploy-engine.sh installs, and the retired ones it removes (read from it: one list)
  FILES755="" FILES644="" RETIRED="" UNITS=""
  eval "$(grep -E '^(FILES755|FILES644|RETIRED|UNITS)=' tools/deploy-engine.sh)"
  [ -n "$FILES755" ] && [ -n "$UNITS" ] || die "tools/deploy-engine.sh has no FILES755/UNITS"
  mkdir -p "$PKG/engine/lib" "$PKG/engine/units"
  for f in $FILES755; do
    [ "$f" = eirl-engine-go ] && continue
    install -m 0755 "scripts/$f" "$PKG/engine/lib/$f"
  done
  for f in $FILES644; do install -m 0644 "scripts/$f" "$PKG/engine/lib/$f"; done
  install -m 0755 "$ENGINE_BIN" "$PKG/engine/lib/eirl-engine-go"
  for u in $UNITS; do install -m 0644 "systemd/$u" "$PKG/engine/units/$u"; done
  echo "$ver $commit release · $released" > "$PKG/engine/lib/VERSION"
  # What install.sh removes from /usr/lib/eirl-encoder/engine (the retired Python engine): one name per line
  printf '%s\n' $RETIRED > "$PKG/engine/retired.txt"
fi

# files.txt with everything the bundle installs (install.sh --list-files, with the engine now in the package)
bash "$PKG/install.sh" --list-files > "$PKG/files.txt.new" && mv "$PKG/files.txt.new" "$PKG/files.txt"
grep -qx /usr/lib/eirl-encoder/VERSION "$PKG/files.txt" || die "files.txt without VERSION"

case "$INJECT" in
  app)
    printf '#!/bin/sh\necho "eirl-encoderd: injected failure (tools/release.sh --inject-failure app)" >&2\nexit 1\n' > "$PKG/eirl-encoderd"
    chmod 0755 "$PKG/eirl-encoderd" ;;
  install)
    printf '#!/bin/sh\necho "install.sh: injected failure (tools/release.sh --inject-failure install)" >&2\nexit 1\n' > "$PKG/install.sh"
    chmod 0755 "$PKG/install.sh" ;;
esac

echo "== bundle"
bundle=$out/eirl-encoder-$ver-linux-arm64.tar.gz
COPYFILE_DISABLE=1 "$REL" pack -dir "$PKG" -out "$bundle" -mtime "$released"
if [ -z "$NOTES" ]; then
  NOTES=release/notes/$ver
  if [ ! -f "$NOTES/en.md" ] && [ -n "$LOCAL" ]; then
    NOTES=$tooldir/notes && mkdir -p "$NOTES"
    echo "Test release $ver ($commit)${INJECT:+, with an injected failure ($INJECT) to test the rollback}." > "$NOTES/en.md"
  fi
fi
[ -f "$NOTES/en.md" ] || die "no release notes: write $NOTES/en.md (and es.md, fr.md)"
"$REL" manifest -version "$ver" -bundle "$bundle" ${MIN_FROM:+-min-from "$MIN_FROM"} -notes "$NOTES" -released "$released" -out "$out/manifest.json"

echo "== signature"
if [ -n "$KEY_STDIN" ]; then
  "$REL" sign -in "$out/manifest.json" -out "$out/manifest.json.sig" -pubkey "$PUBKEY" <&3
else
  # The seed only goes through this pipe
  sh -c "$EIRL_RELEASE_KEY_CMD" |
    "$REL" sign -in "$out/manifest.json" -out "$out/manifest.json.sig"
fi
exec 3<&-
"$REL" verify -manifest "$out/manifest.json" -sig "$out/manifest.json.sig" -bundle "$bundle" ${PUBKEY:+-pubkey "$PUBKEY"}

if [ -n "$LOCAL" ]; then
  mkdir -p "$LOCAL"
  cp "$bundle" "$out/manifest.json" "$out/manifest.json.sig" "$LOCAL/"
  echo "== local release in $LOCAL (its manifest.json is now the \"latest\" there)"
  echo "   serve: python3 -m http.server --directory $LOCAL 8000"
fi
echo "release $ver: $out ($(du -h "$bundle" | cut -f1))${PUBKEY:+ — TEST BUILD trusting key $PUBKEY}"
