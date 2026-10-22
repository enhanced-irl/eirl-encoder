#!/usr/bin/env bash
# tools/release.sh — builds and signs a release of the whole encoder software (UPDATES.md §5), and signs and
# publishes the SD card image of a release (IMAGE.md §8). Runs on the maintainer's machine (macOS or Linux, with
# Go and Docker), never on the board.
#
#   tools/release.sh build [options]      → build/out/release/<version>/{bundle, manifest.json, manifest.json.sig}
#   tools/release.sh publish [--yes]      → GitHub release v<VERSION> with those three files (explicit step)
#   tools/release.sh image --image IMG [options]
#                                         → build/out/release/<release>/image/: the image and its files (IMAGE.md §8.2)
#   tools/release.sh publish-image <release> [--yes] [--clobber]
#                                         → those files, added to the EXISTING GitHub release v<release>
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
#   --skip-system       a bundle without the system layer (UPDATES.md §12; the board's system is left as it is)
#   --inject-failure W  a broken bundle to test the rollback: W=app (the new app never starts),
#                       W=install (install.sh fails) or W=system (a file of the system layer that every apply
#                       needs, its os-release, does not match its layer.json: refused before changing anything)
#   --allow-dirty       build from a tree with uncommitted changes
#
# image options. IMG is the image recipe's eirl-encoder-<version>-jetson-nano.img.xz, where <version> is the
# release it carries, or <release>-N for the N-th rebuild of its image:
#   --software-manifest F   the OTA manifest.json of the release installed in the image, with its .sig next to it
#                           (default build/out/release/<VERSION>/manifest.json)
#   --raw-size N --raw-sha256 H
#                       the raw image as the recipe measured it, or
#   --raw-from-xz       hash the decompressed image here (the default). Either way it is checked with xz -dc
#   --packages F        the package list, published as eirl-encoder-<version>-jetson-nano-packages.txt (IMAGE.md §9.3)
#   --sources F         where the sources are, published as eirl-encoder-<version>-jetson-nano-sources.md
#   --min-card-bytes N  the smallest card (default 16000000000)
#   --commit C          the image recipe's commit (default HEAD, which must then be clean)
#   --built T           when the recipe built the image (RFC3339; default now)
#   --local DIR, --key-stdin, --pubkey B64, --allow-dirty: the test mode of build (a THROWAWAY key)
#
# publish-image adds the image's files to the existing GitHub release v<release> (the OTA bundle's): it never
# creates a release, never changes which release is latest, and replaces an asset only with --clobber. The
# release's manifest.json must be the one in the image, and GitHub takes files under 2 GiB each.
# Its test mode, for a stubbed gh: --repo OWNER/NAME (never enhanced-irl/eirl-encoder) --pubkey B64.
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
  build|publish|image|publish-image) ;;
  *) awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; exit 64 ;;
esac

LOCAL="" VERSION_OPT="" KEY_STDIN="" PUBKEY="" SKIP_ENGINE="" SKIP_SYSTEM="" ENGINE_BIN="" INJECT="" ALLOW_DIRTY="" MIN_FROM="" NOTES="" YES=""
IMAGE="" SOFTWARE="" RAW_SIZE="" RAW_SHA="" RAW_FROM_XZ="" PACKAGES="" SOURCES="" MIN_CARD="" COMMIT="" BUILT="" CLOBBER="" REPO_OPT="" ARG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --local) LOCAL=${2:?}; shift ;;
    --version) VERSION_OPT=${2:?}; shift ;;
    --key-stdin) KEY_STDIN=1 ;;
    --pubkey) PUBKEY=${2:?}; shift ;;
    --skip-engine) SKIP_ENGINE=1 ;;
    --skip-system) SKIP_SYSTEM=1 ;;
    --engine-bin) ENGINE_BIN=${2:?}; shift ;;
    --inject-failure) INJECT=${2:?}; shift ;;
    --allow-dirty) ALLOW_DIRTY=1 ;;
    --min-from) MIN_FROM=${2:?}; shift ;;
    --notes) NOTES=${2:?}; shift ;;
    --yes) YES=1 ;;
    --image) IMAGE=${2:?}; shift ;;
    --software-manifest) SOFTWARE=${2:?}; shift ;;
    --raw-size) RAW_SIZE=${2:?}; shift ;;
    --raw-sha256) RAW_SHA=${2:?}; shift ;;
    --raw-from-xz) RAW_FROM_XZ=1 ;;
    --packages) PACKAGES=${2:?}; shift ;;
    --sources) SOURCES=${2:?}; shift ;;
    --min-card-bytes) MIN_CARD=${2:?}; shift ;;
    --commit) COMMIT=${2:?}; shift ;;
    --built) BUILT=${2:?}; shift ;;
    --clobber) CLOBBER=1 ;;
    --repo) REPO_OPT=${2:?}; shift ;;
    -*) die "unknown option $1" ;;
    *) [ "$cmd" = publish-image ] && [ -z "$ARG" ] || die "unexpected argument $1"; ARG=$1 ;;
  esac
  shift
done
IMAGE_OPTS=$IMAGE$SOFTWARE$RAW_SIZE$RAW_SHA$RAW_FROM_XZ$PACKAGES$SOURCES$MIN_CARD$COMMIT$BUILT

# The Go half of the tool, for this machine
tooldir=$(mktemp -d)
trap 'rm -rf "$tooldir"' EXIT
(cd manager && go build -o "$tooldir/eirl-release" ./cmd/eirl-release)
REL=$tooldir/eirl-release

file_version() { tr -d ' \n' < VERSION; }
filesize() { stat -c %s "$1" 2>/dev/null || stat -f %z "$1"; }
sha256_of() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }
sha256_check() { if command -v sha256sum >/dev/null; then sha256sum -c "$1"; else shasum -a 256 -c "$1"; fi; }
# A clone where the filesystem can make one (APFS, btrfs, XFS): an image is a GB that need not be copied
clone() { cp -c "$1" "$2" 2>/dev/null || cp --reflink=auto "$1" "$2" 2>/dev/null || cp "$1" "$2"; }
GITHUB_MAX_ASSET=2147483648   # GitHub: every file of a release must be under 2 GiB

# --- image: sign the SD card image of a release (IMAGE.md §8) ----------------------------------------------
if [ "$cmd" = image ]; then
  [ -z "$VERSION_OPT$SKIP_ENGINE$SKIP_SYSTEM$ENGINE_BIN$INJECT$MIN_FROM$NOTES$YES$CLOBBER$REPO_OPT" ] ||
    die "image takes --image, --software-manifest, --raw-size and --raw-sha256 or --raw-from-xz, --packages, --sources, --min-card-bytes, --commit, --built, and --local, --key-stdin, --pubkey, --allow-dirty to test"
  [ -n "$IMAGE" ] || die "--image eirl-encoder-<version>-jetson-nano.img.xz is required"
  if [ -z "$LOCAL" ]; then
    [ -z "$KEY_STDIN$PUBKEY$ALLOW_DIRTY" ] || die "--key-stdin, --pubkey and --allow-dirty are for --local test images only"
  fi
  if [ -n "$KEY_STDIN" ] || [ -n "$PUBKEY" ]; then
    [ -n "$KEY_STDIN" ] && [ -n "$PUBKEY" ] || die "--key-stdin and --pubkey go together"
  fi
  [ -n "$KEY_STDIN" ] || [ -n "${EIRL_RELEASE_KEY_CMD:-}" ] ||
    die "set EIRL_RELEASE_KEY_CMD to the command that prints the production signing seed (maintainers' secret store)"
  if [ -n "$RAW_SIZE$RAW_SHA" ]; then
    [ -n "$RAW_SIZE" ] && [ -n "$RAW_SHA" ] || die "--raw-size and --raw-sha256 go together"
    [ -z "$RAW_FROM_XZ" ] || die "either --raw-size and --raw-sha256, or --raw-from-xz"
  fi
  command -v xz >/dev/null || die "xz is needed (it checks the raw image)"
  [ -f "$IMAGE" ] || die "no image $IMAGE"
  name=$(basename "$IMAGE")
  case "$name" in
    eirl-encoder-?*-jetson-nano.img.xz) ;;
    *) die "$name: the image must be named eirl-encoder-<version>-jetson-nano.img.xz" ;;
  esac
  imgver=${name#eirl-encoder-} && imgver=${imgver%-jetson-nano.img.xz}
  for f in "$PACKAGES" "$SOURCES"; do [ -z "$f" ] || [ -s "$f" ] || die "$f is missing or empty"; done
  if [ -z "$COMMIT" ]; then
    if [ -z "$ALLOW_DIRTY" ] && [ -n "$(git status --porcelain)" ]; then
      die "uncommitted changes: pass --commit <the image recipe's commit> (or --local --allow-dirty)"
    fi
    COMMIT=$(git rev-parse --short=12 HEAD)
  fi
  SOFTWARE=${SOFTWARE:-build/out/release/$(file_version)/manifest.json}
  [ -f "$SOFTWARE" ] && [ -f "$SOFTWARE.sig" ] ||
    die "no $SOFTWARE and its .sig: the release in the image (tools/release.sh build, or --software-manifest)"
  # Nothing but the signing step reads stdin (it may carry the throwaway seed)
  exec 3<&0 0</dev/null

  echo "== the release in the image"
  swver=$("$REL" verify -manifest "$SOFTWARE" -sig "$SOFTWARE.sig" ${PUBKEY:+-pubkey "$PUBKEY"} -print version) ||
    die "$SOFTWARE does not verify${PUBKEY:+ with the test key}"
  echo "   eirl-encoder $swver ($SOFTWARE)"
  out=build/out/release/$swver/image
  base=eirl-encoder-$imgver-jetson-nano
  new=$out.new
  rm -rf "$new" && mkdir -p "$new"
  echo "== image $imgver ($(du -h "$IMAGE" | cut -f1))"
  clone "$IMAGE" "$new/$base.img.xz"
  [ -z "$PACKAGES" ] || cp "$PACKAGES" "$new/$base-packages.txt"
  [ -z "$SOURCES" ] || cp "$SOURCES" "$new/$base-sources.md"
  set -- -image "$new/$base.img.xz" -software "$SOFTWARE" -version "$imgver" -commit "$COMMIT" \
    ${MIN_CARD:+-min-card-bytes "$MIN_CARD"} ${BUILT:+-built "$BUILT"} -out "$new/$base.img.json" -sha256-out "$new/$base.img.xz.sha256"
  if [ -n "$RAW_SIZE" ]; then
    "$REL" image-manifest "$@" -raw-size "$RAW_SIZE" -raw-sha256 "$RAW_SHA"
  else
    echo "   hashing the raw image (xz -dc)"
    xz -dc "$new/$base.img.xz" | "$REL" image-manifest "$@" -raw-stdin
  fi

  echo "== signature"
  if [ -n "$KEY_STDIN" ]; then
    "$REL" sign-image -in "$new/$base.img.json" -pubkey "$PUBKEY" <&3
  else
    # The seed only goes through this pipe
    sh -c "$EIRL_RELEASE_KEY_CMD" | "$REL" sign-image -in "$new/$base.img.json"
  fi
  exec 3<&-

  echo "== verify: signature, image, raw image (xz -dc), .sha256 file"
  xz -dc "$new/$base.img.xz" |
    "$REL" verify-image -manifest "$new/$base.img.json" -image "$new/$base.img.xz" -raw-stdin ${PUBKEY:+-pubkey "$PUBKEY"}
  (cd "$new" && sha256_check "$base.img.xz.sha256" >/dev/null) || die "$base.img.xz.sha256 does not check"
  rm -rf "$out" && mv "$new" "$out"
  size=$(filesize "$out/$base.img.xz")
  if [ "$size" -ge "$GITHUB_MAX_ASSET" ]; then
    echo "release: WARNING: $base.img.xz is $size bytes, over GitHub's 2 GiB per-file limit: publish-image will refuse it" >&2
  fi
  if [ -n "$LOCAL" ]; then
    mkdir -p "$LOCAL"
    for f in "$out"/*; do clone "$f" "$LOCAL/$(basename "$f")"; done
    echo "== local copy in $LOCAL"
  fi
  if [ -z "$PACKAGES" ] || [ -z "$SOURCES" ]; then
    echo "note: without --packages and --sources (IMAGE.md §9.3), publish-image refuses this image" >&2
  fi
  echo "image $imgver of release $swver: $out${PUBKEY:+ — TEST signature, key $PUBKEY}"
  exit 0
fi

# --- publish-image: add the image to the existing GitHub release (IMAGE.md §8.3) ---------------------------
if [ "$cmd" = publish-image ]; then
  [ -z "$LOCAL$VERSION_OPT$KEY_STDIN$SKIP_ENGINE$SKIP_SYSTEM$ENGINE_BIN$INJECT$ALLOW_DIRTY$MIN_FROM$NOTES$IMAGE_OPTS" ] ||
    die "publish-image takes <version>, --yes and --clobber (and --repo with --pubkey, to test it)"
  ver=$ARG
  [ -n "$ver" ] || die "usage: tools/release.sh publish-image <version> [--yes] [--clobber]"
  "$REL" semver "$ver" >/dev/null || die "not a semantic version: $ver"
  if [ -n "$PUBKEY$REPO_OPT" ]; then
    [ -n "$PUBKEY" ] && [ -n "$REPO_OPT" ] || die "--repo and --pubkey go together (a test of publish-image)"
    # GitHub names are case-insensitive and gh also takes HOST/OWNER/REPO and URLs
    case $(tr '[:upper:]' '[:lower:]' <<<"$REPO_OPT") in
      *"$REPO"*) die "--repo and --pubkey are only for a repository other than $REPO (a test of publish-image)" ;;
    esac
    REPO=$REPO_OPT
  fi
  dir=build/out/release/$ver/image
  set -- "$dir"/*.img.json
  [ $# -eq 1 ] && [ -f "$1" ] || die "$dir must hold exactly one image (*.img.json): run tools/release.sh image first"
  json=$1 && base=${json%.img.json}
  img=$base.img.xz
  files=("$img" "$json" "$json.sig" "$img.sha256" "$base-packages.txt" "$base-sources.md")
  for f in "$img" "$json" "$json.sig" "$img.sha256"; do [ -s "$f" ] || die "missing $f: run tools/release.sh image again"; done
  for f in "$base-packages.txt" "$base-sources.md"; do
    [ -s "$f" ] || die "missing $f: the package list and the sources go with the image (IMAGE.md §9.3): tools/release.sh image --packages … --sources …"
  done
  # GitHub takes release assets under 2 GiB each: checked first, it is the cheapest
  size=$(filesize "$img")
  [ "$size" -lt "$GITHUB_MAX_ASSET" ] ||
    die "$(basename "$img") is $size bytes: GitHub takes release files under 2 GiB ($GITHUB_MAX_ASSET bytes) each"
  # Only an image signed with the PRODUCTION key is ever published (--pubkey only goes with another --repo)
  vimg() { "$REL" verify-image -manifest "$json" ${PUBKEY:+-pubkey "$PUBKEY"} "$@"; }
  vimg -image "$img" >/dev/null || die "the image does not verify: run tools/release.sh image again"
  imgsw=$(vimg -print software.version) && name=$(vimg -print image.name) && sha=$(vimg -print image.sha256) &&
    msha=$(vimg -print software.manifestSha256) || die "cannot read the image manifest"
  [ "$imgsw" = "$ver" ] || die "the image carries release $imgsw, not $ver"
  [ "$name" = "$(basename "$img")" ] || die "the manifest names the image $name, not $(basename "$img")"
  [ "$(cat "$img.sha256")" = "$sha  $name" ] || die "$img.sha256 is not the manifest's sha256"

  command -v gh >/dev/null || die "gh (GitHub CLI) is needed"
  draft=$(gh release view "v$ver" --repo "$REPO" --json isDraft --jq .isDraft 2>/dev/null) ||
    die "there is no GitHub release v$ver in $REPO: publish-image adds the image to an existing release (tools/release.sh publish first) and never creates one"
  [ "$draft" = false ] || die "v$ver is a draft on GitHub: publish it first"
  rel=$tooldir/v$ver && mkdir -p "$rel"
  gh release download "v$ver" --repo "$REPO" --pattern manifest.json --pattern manifest.json.sig --dir "$rel" >/dev/null 2>&1 ||
    die "cannot download manifest.json and manifest.json.sig of v$ver"
  relver=$("$REL" verify -manifest "$rel/manifest.json" -sig "$rel/manifest.json.sig" ${PUBKEY:+-pubkey "$PUBKEY"} -print version) ||
    die "the manifest.json of GitHub release v$ver does not verify"
  [ "$relver" = "$ver" ] || die "the manifest.json of GitHub release v$ver is version $relver, not $ver"
  [ "$(sha256_of "$rel/manifest.json")" = "$msha" ] ||
    die "the image carries another manifest.json of $ver than the one in GitHub release v$ver: build the image again from the published release"
  assets=$(gh release view "v$ver" --repo "$REPO" --json assets --jq '.assets[].name') || die "cannot list the files of v$ver"
  for f in "${files[@]}"; do
    if grep -qxF "$(basename "$f")" <<<"$assets"; then
      [ -n "$CLOBBER" ] || die "v$ver already has $(basename "$f"): pass --clobber to replace it"
      echo "replacing $(basename "$f") (--clobber)"
    fi
  done
  echo "About to add the SD card image $(basename "$img") ($(du -h "$img" | cut -f1), under GitHub's 2 GiB per file) and its"
  echo "files to the existing release https://github.com/$REPO/releases/tag/v$ver. Which release is latest does not change."
  if [ -z "$YES" ]; then
    read -r -p "Type the version to confirm: " answer
    [ "$answer" = "$ver" ] || die "not confirmed"
  fi
  gh release upload "v$ver" --repo "$REPO" ${CLOBBER:+--clobber} "${files[@]}"
  echo "added the image to v$ver: https://github.com/$REPO/releases/tag/v$ver"
  echo "check a download as a user would (IMAGE.md §8.4): eirl-release verify-image -manifest $(basename "$json") -image $(basename "$img")"
  exit 0
fi

if [ "$cmd" = publish ]; then
  [ -z "$IMAGE_OPTS$CLOBBER$REPO_OPT" ] || die "publish takes only --yes"
  [ -z "$LOCAL$VERSION_OPT$KEY_STDIN$PUBKEY$SKIP_ENGINE$SKIP_SYSTEM$INJECT" ] || die "publish takes only --yes"
  ver=$(file_version)
  out=build/out/release/$ver
  bundle=$out/eirl-encoder-$ver-linux-arm64.tar.gz
  for f in "$bundle" "$out/manifest.json" "$out/manifest.json.sig"; do [ -f "$f" ] || die "missing $f: run tools/release.sh build first"; done
  # Only a release signed with the PRODUCTION key is ever published
  "$REL" verify -manifest "$out/manifest.json" -sig "$out/manifest.json.sig" -bundle "$bundle"
  notes=release/notes/$ver/en.md
  [ -f "$notes" ] || die "missing $notes"
  command -v gh >/dev/null || die "gh (GitHub CLI) is needed"
  # The public repository's main must be the export of the commit the bundle was built from (its source is
  # what the release points at, GPL included): tools/public-sync.sh, pushed with the owner's OK, comes first
  built=$(tar -xzOf "$bundle" ./BUILD 2>/dev/null | grep -o 'commit=[0-9a-f]*' | cut -d= -f2)
  [ -n "$built" ] || die "no commit in the bundle's BUILD"
  public=$(gh api "repos/$REPO/commits/main" --jq .commit.message) || die "cannot read $REPO's main"
  grep -q "Exported from internal commit ${built:0:12}\." <<<"$public" ||
    die "github.com/$REPO main is not the export of ${built:0:12}: run tools/public-sync.sh and push it first"
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
[ -z "$IMAGE_OPTS$CLOBBER$REPO_OPT" ] || die "--image, --software-manifest, --raw-*, --packages, --sources, --min-card-bytes, --commit, --built, --clobber and --repo are not for build"
if [ -z "$LOCAL" ]; then
  [ -z "$VERSION_OPT$KEY_STDIN$PUBKEY$SKIP_ENGINE$SKIP_SYSTEM$INJECT$ALLOW_DIRTY" ] ||
    die "--version, --key-stdin, --pubkey, --skip-engine, --skip-system, --inject-failure and --allow-dirty are for --local test releases only"
fi
if [ -n "$KEY_STDIN" ] || [ -n "$PUBKEY" ]; then
  [ -n "$KEY_STDIN" ] && [ -n "$PUBKEY" ] || die "--key-stdin and --pubkey go together"
fi
case "$INJECT" in ""|app|install|system) ;; *) die "--inject-failure app|install|system" ;; esac
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
  FILES755="" FILES644="" RETIRED="" UNITS="" EDIDS=""
  eval "$(grep -E '^(FILES755|FILES644|RETIRED|UNITS|EDIDS)=' tools/deploy-engine.sh)"
  [ -n "$FILES755" ] && [ -n "$UNITS" ] && [ -n "$EDIDS" ] || die "tools/deploy-engine.sh has no FILES755/UNITS/EDIDS"
  build/edid.sh >/dev/null   # the EDIDs, from edid/*.hex
  mkdir -p "$PKG/engine/lib" "$PKG/engine/units"
  for f in $FILES755; do
    [ "$f" = eirl-engine-go ] && continue
    install -m 0755 "scripts/$f" "$PKG/engine/lib/$f"
  done
  for f in $FILES644; do install -m 0644 "scripts/$f" "$PKG/engine/lib/$f"; done
  for f in $EDIDS; do install -m 0644 "build/out/$f" "$PKG/engine/lib/$f"; done
  install -m 0755 "$ENGINE_BIN" "$PKG/engine/lib/eirl-engine-go"
  for u in $UNITS; do install -m 0644 "systemd/$u" "$PKG/engine/units/$u"; done
  echo "$ver $commit release · $released" > "$PKG/engine/lib/VERSION"
  # What install.sh removes from /usr/lib/eirl-encoder/engine (the retired Python engine): one name per line
  printf '%s\n' $RETIRED > "$PKG/engine/retired.txt"
fi

if [ -z "$SKIP_SYSTEM" ]; then
  # The system layer (UPDATES.md §12): our modules and firmware (build/build.sh, reproducible: their sha256 are pinned
  # in system/modules.lock), the Ubuntu packages of system/packages.lock (build/system.sh fetches and checks them),
  # system/files/ and the units' state. In the bundle as system/, applied by install.sh; its sources in doc/
  echo "== system layer"
  build/build.sh >/dev/null
  build/system.sh >/dev/null
  "$REL" layer -repo "$ROOT" -out "$PKG/system" -release "$ver" -debs "${CACHE:-$HOME/.cache/l4t-r32.7.6}/debs" \
    -build-out build/out -commit "$commit" -sources "$PKG/doc/system-SOURCES.md"
fi

# files.txt with everything the bundle installs (install.sh --list-files, with the engine now in the package)
bash "$PKG/install.sh" --list-files > "$PKG/files.txt.new" && mv "$PKG/files.txt.new" "$PKG/files.txt"
grep -qx /usr/lib/eirl-encoder/VERSION "$PKG/files.txt" || die "files.txt without VERSION"
# Every path must be one the boards' update helper accepts, or every board refuses the whole bundle (UPDATES.md §3)
"$REL" check-files -files "$PKG/files.txt" >/dev/null || die "files.txt names a path the update helper refuses"

case "$INJECT" in
  app)
    printf '#!/bin/sh\necho "eirl-encoderd: injected failure (tools/release.sh --inject-failure app)" >&2\nexit 1\n' > "$PKG/eirl-encoderd"
    chmod 0755 "$PKG/eirl-encoderd" ;;
  install)
    printf '#!/bin/sh\necho "install.sh: injected failure (tools/release.sh --inject-failure install)" >&2\nexit 1\n' > "$PKG/install.sh"
    chmod 0755 "$PKG/install.sh" ;;
  system)
    f=$PKG/system/files/usr/lib/os-release
    [ -f "$f" ] || die "--inject-failure system: the bundle has no system layer"
    echo "# injected failure (tools/release.sh --inject-failure system)" >> "$f" ;;
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
