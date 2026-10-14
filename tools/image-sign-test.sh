#!/usr/bin/env bash
# tools/image-sign-test.sh — signing, verifying and publishing the SD card image (IMAGE.md §8), end to end, with a
# THROWAWAY key. No Docker, no board, never GitHub (about a minute). It checks that:
#   - tools/release.sh image --local … --key-stdin --pubkey … signs a fake 64 MiB image, with the raw image hashed
#     from the xz stream or given as the recipe's flags, and eirl-release verify-image accepts it (the signature, the
#     .img.xz and the raw stream), as do sha256sum -c and the grep of IMAGE.md §8.4;
#   - one byte changed in the image, the manifest or the raw stream is refused (exit 1), as are wrong recipe values;
#   - OpenSSL 3 (and later) verifies the same manifest with the PEM form of the key, and release/eirl-release.pem holds
#     the key of release/eirl-release.pub (skipped, with a note, without OpenSSL 3);
#   - tools/release.sh publish-image, with a stubbed gh on PATH, refuses a missing release, a release of another
#     version or with another manifest, an image of another release, an existing file without --clobber, a file over
#     2 GiB, missing licence files and a test key for the real repository, and otherwise only uploads: it never
#     creates, edits or deletes a release.
#
#   tools/image-sign-test.sh          (needs Go and xz; leaves nothing behind)
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/eirl-image-sign-test.XXXXXX")
ver=0.0.1-imgsign.$$      # test releases: their build/out/release/<version> directories are removed at the end
other=0.0.2-imgsign.$$
trap 'rm -rf "$work" "build/out/release/$ver" "build/out/release/$other"' EXIT
command -v xz >/dev/null || { echo "xz is needed" >&2; exit 2; }

failed=0
ok() { echo "ok    $*"; }
bad() { echo "FAIL  $*"; failed=1; }
# passes WHAT COMMAND…: the command succeeds
passes() {
  local what=$1 out; shift
  if out=$("$@" 2>&1); then ok "$what"; else bad "$what: $(tail -3 <<<"$out")"; fi
}
# refused WHAT TEXT COMMAND…: the command fails and says TEXT
refused() {
  local what=$1 want=$2 out; shift 2
  if out=$("$@" 2>&1); then
    bad "$what: accepted"
  elif grep -qF -- "$want" <<<"$out"; then
    ok "$what: refused"
  else
    bad "$what: refused without \"$want\": $(tail -3 <<<"$out")"
  fi
}
# exits CODE WHAT COMMAND…: the command exits with CODE
exits() {
  local code=$1 what=$2 rc=0; shift 2
  "$@" >"$work/out" 2>&1 || rc=$?
  if [ "$rc" = "$code" ]; then ok "$what: exit $rc"; else bad "$what: exit $rc, want $code: $(tail -3 "$work/out")"; fi
}
sha256_of() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }
sha256_check() { if command -v sha256sum >/dev/null; then sha256sum -c "$1"; else shasum -a 256 -c "$1"; fi; }
# flip FILE OFFSET: changes one bit of one byte, in place
flip() {
  local b
  b=$(dd if="$1" bs=1 skip="$2" count=1 2>/dev/null | od -An -tu1 | tr -d ' ')
  printf "$(printf '\\%03o' $((b ^ 1)))" | dd of="$1" bs=1 seek="$2" count=1 conv=notrunc 2>/dev/null
}

REL=$work/eirl-release
go -C manager build -o "$REL" ./cmd/eirl-release
"$REL" keygen > "$work/key"
sed -n 's/^seed=//p' "$work/key" > "$work/seed"
PUB=$(sed -n 's/^pubkey=//p' "$work/key")
rm "$work/key"

# A release signed with the test key (only its manifest matters to the image), and another one
release() {   # release VERSION DIR [NOTE]
  mkdir -p "$2/notes" && echo "${3:-Test release.}" > "$2/notes/en.md"
  printf 'bundle %s\n' "$1" > "$2/eirl-encoder-$1-linux-arm64.tar.gz"
  "$REL" manifest -version "$1" -bundle "$2/eirl-encoder-$1-linux-arm64.tar.gz" -notes "$2/notes" -out "$2/manifest.json"
  "$REL" sign -in "$2/manifest.json" -pubkey "$PUB" < "$work/seed" 2>/dev/null
}
release "$ver" "$work/rel"
release "$other" "$work/rel-other"
release "$ver" "$work/rel-again" "The same version, built again."

# A fake 64 MiB raw image (8 MiB of noise, the rest zeros), compressed as the recipe does
{ head -c 8388608 /dev/urandom; head -c 58720256 /dev/zero; } > "$work/raw.img"
raw_size=$(wc -c < "$work/raw.img" | tr -d ' ') && raw_sha=$(sha256_of "$work/raw.img")
mkdir -p "$work/recipe"
img_in=$work/recipe/eirl-encoder-$ver-jetson-nano.img.xz
xz -T0 -1 -c "$work/raw.img" > "$img_in"
printf 'base-files 10.1ubuntu2.12 base-files\n' > "$work/packages.txt"
printf '# Sources\n\nTest image.\n' > "$work/sources.md"

image() {   # image [release.sh image options…]: the test mode, with the test key on stdin
  tools/release.sh image --software-manifest "$work/rel/manifest.json" --packages "$work/packages.txt" \
    --sources "$work/sources.md" --local "$work/local" --key-stdin --pubkey "$PUB" --allow-dirty "$@" < "$work/seed"
}
out=build/out/release/$ver/image
base=eirl-encoder-$ver-jetson-nano
json=$out/$base.img.json img=$out/$base.img.xz

echo "== tools/release.sh image"
passes "image, raw hashed from xz -dc" image --image "$img_in"
want="$base-packages.txt $base-sources.md $base.img.json $base.img.json.sig $base.img.xz $base.img.xz.sha256"
[ "$(ls "$out" | sort | tr '\n' ' ')" = "$want " ] && ok "the files of IMAGE.md §8.2" || bad "files: $(ls "$out" | tr '\n' ' ')"
[ "$(ls "$work/local" | sort | tr '\n' ' ')" = "$want " ] && ok "the --local copy" || bad "local: $(ls "$work/local" | tr '\n' ' ')"
[ "$("$REL" verify-image -manifest "$json" -pubkey "$PUB" -print image.rawSize)" = "$raw_size" ] &&
  [ "$("$REL" verify-image -manifest "$json" -pubkey "$PUB" -print image.rawSha256)" = "$raw_sha" ] &&
  ok "raw size and sha256 of the xz stream" || bad "raw size or sha256"
passes "image, raw from the recipe's flags" image --image "$img_in" --raw-size "$raw_size" --raw-sha256 "$raw_sha" \
  --min-card-bytes 32000000000 --built 2026-10-20T12:00:00+02:00 --commit 0123456789abcdef
[ "$("$REL" verify-image -manifest "$json" -pubkey "$PUB" -print minCardBytes)" = 32000000000 ] &&
  [ "$("$REL" verify-image -manifest "$json" -pubkey "$PUB" -print built)" = 2026-10-20T10:00:00Z ] &&
  [ "$("$REL" verify-image -manifest "$json" -pubkey "$PUB" -print commit)" = 0123456789ab ] &&
  ok "--min-card-bytes, --built and --commit" || bad "--min-card-bytes, --built or --commit"
cp "$json" "$work/good.img.json"
refused "a wrong raw sha256 from the recipe" "raw image: sha256" image --image "$img_in" --raw-size "$raw_size" --raw-sha256 "$(sha256_of "$work/packages.txt")"
refused "a wrong raw size from the recipe" "raw image:" image --image "$img_in" --raw-size $((raw_size + 512)) --raw-sha256 "$raw_sha"
refused "a raw size that is not whole sectors" "multiple of 512" image --image "$img_in" --raw-size $((raw_size + 1)) --raw-sha256 "$raw_sha"
cmp -s "$json" "$work/good.img.json" && ok "a refused run leaves the previous image as it was" || bad "a refused run changed $json"
refused "an image named otherwise" "must be named" image --image "$work/raw.img"
cp "$img_in" "$work/recipe/eirl-encoder-$other-jetson-nano.img.xz"
refused "an image of another version than the release" "must be the software's" image --image "$work/recipe/eirl-encoder-$other-jetson-nano.img.xz"
refused "an OTA manifest signed with another key" "does not verify" \
  tools/release.sh image --image "$img_in" --software-manifest "$work/rel/manifest.json" --local "$work/x" --key-stdin \
  --pubkey "$("$REL" keygen | sed -n 's/^pubkey=//p')" --allow-dirty < "$work/seed"
refused "a production image without EIRL_RELEASE_KEY_CMD" "EIRL_RELEASE_KEY_CMD" env -u EIRL_RELEASE_KEY_CMD tools/release.sh image --image "$img_in"
refused "a test key without --local" "for --local test images only" \
  env EIRL_RELEASE_KEY_CMD=false tools/release.sh image --image "$img_in" --key-stdin --pubkey "$PUB"
cp "$img_in" "$work/recipe/eirl-encoder-$ver-2-jetson-nano.img.xz"
passes "a rebuild (<release>-2)" image --image "$work/recipe/eirl-encoder-$ver-2-jetson-nano.img.xz"
[ -f "$out/eirl-encoder-$ver-2-jetson-nano.img.json" ] && [ "$(ls "$out" | wc -l | tr -d ' ')" = 6 ] &&
  ok "a rebuild replaces the release's image" || bad "rebuild files: $(ls "$out" | tr '\n' ' ')"
passes "image again" image --image "$img_in"

echo "== eirl-release verify-image"
v() { "$REL" verify-image -manifest "$json" -pubkey "$PUB" "$@"; }
exits 0 "the signature, the image and the raw stream" sh -c 'xz -dc "$1" | "$2" verify-image -manifest "$3" -image "$1" -raw-stdin -pubkey "$4"' \
  sh "$img" "$REL" "$json" "$PUB"
exits 1 "the same image with the production key (a test signature)" "$REL" verify-image -manifest "$json" -image "$img"
passes "sha256sum -c of the .img.xz.sha256" sh -c "cd '$out' && $(declare -f sha256_check); sha256_check '$base.img.xz.sha256'"
passes "the sha256 grep of IMAGE.md §8.4" grep -q "\"sha256\": \"$(sha256_of "$img")\"" "$json"
cp "$img" "$work/flipped.img.xz" && flip "$work/flipped.img.xz" 1000000
exits 1 "one byte changed in the image" v -image "$work/flipped.img.xz"
exits 1 "one byte changed at the end of the image" sh -c 'cp "$1" "$2"; printf x >> "$2"; "$3" verify-image -manifest "$4" -pubkey "$5" -image "$2"' \
  sh "$img" "$work/longer.img.xz" "$REL" "$json" "$PUB"
mkdir -p "$work/t" && cp "$json" "$json.sig" "$work/t/"
flip "$work/t/$base.img.json" 200
exits 1 "one byte changed in the manifest" "$REL" verify-image -manifest "$work/t/$base.img.json" -pubkey "$PUB"
cp "$work/raw.img" "$work/flipped.raw" && flip "$work/flipped.raw" 33554432
exits 1 "one byte changed in the raw stream" sh -c '"$1" verify-image -manifest "$2" -pubkey "$3" -raw-stdin < "$4"' sh "$REL" "$json" "$PUB" "$work/flipped.raw"
exits 1 "an OTA manifest given as an image manifest" "$REL" verify-image -manifest "$work/rel/manifest.json" -pubkey "$PUB"
exits 2 "a missing image file" v -image "$work/nothing.img.xz"
refused "the OTA sign on an image manifest" "sign it with eirl-release sign-image" "$REL" sign -in "$json" -out "$work/x.sig" -pubkey "$PUB"
refused "sign-image on an OTA manifest" "sign it with eirl-release sign" "$REL" sign-image -in "$work/rel/manifest.json" -out "$work/x.sig" -pubkey "$PUB"

echo "== OpenSSL"
ossl=""
for o in "$(brew --prefix openssl@3 2>/dev/null)/bin/openssl" "$(command -v openssl || true)"; do
  [ -x "$o" ] && "$o" version 2>/dev/null | grep -qE '^OpenSSL ([3-9]|[1-9][0-9])\.' || continue
  case " $ossl " in *" $o "*) ;; *) ossl="$ossl $o" ;; esac
done
if [ -z "$ossl" ]; then
  echo "note: no OpenSSL 3 here (LibreSSL cannot verify ed25519 with pkeyutl): the OpenSSL checks are skipped"
fi
"$REL" pem -pubkey "$PUB" > "$work/test.pem"
for o in $ossl; do
  n=$("$o" version | cut -d' ' -f1-2)
  passes "$n: release/eirl-release.pem holds the key of release/eirl-release.pub" \
    test "$("$o" pkey -pubin -in release/eirl-release.pem -outform DER | tail -c 32 | "$o" base64 -A)" = "$(cat release/eirl-release.pub)"
  # The commands of IMAGE.md §8.4, with the test key's PEM instead of release/eirl-release.pem
  "$o" base64 -d -A -in "$json.sig" -out "$work/sig.bin"
  passes "$n: pkeyutl -verify of the image manifest" "$o" pkeyutl -verify -pubin -inkey "$work/test.pem" -rawin -in "$json" -sigfile "$work/sig.bin"
  refused "$n: pkeyutl -verify of a changed manifest" "Signature Verification Failure" \
    "$o" pkeyutl -verify -pubin -inkey "$work/test.pem" -rawin -in "$work/t/$base.img.json" -sigfile "$work/sig.bin"
  refused "$n: pkeyutl -verify with the production key" "Signature Verification Failure" \
    "$o" pkeyutl -verify -pubin -inkey release/eirl-release.pem -rawin -in "$json" -sigfile "$work/sig.bin"
done

echo "== tools/release.sh publish-image (stubbed gh)"
mkdir -p "$work/bin" "$work/gh"
cat > "$work/bin/gh" <<'STUB'
#!/usr/bin/env bash
# A stub of gh: a release is the directory $GH_STUB/<tag> and its files are its assets. Every call is logged.
set -euo pipefail
echo "gh $*" >> "$GH_STUB/calls.log"
[ "${1:-}" = release ] || { echo "stub gh: not expected: $*" >&2; exit 3; }
sub=$2 tag=$3
shift 3
json="" dir="" clobber="" patterns="" files=""
while [ $# -gt 0 ]; do
  case $1 in
    --repo|--jq) shift ;;
    --json) json=$2; shift ;;
    --pattern) patterns="$patterns $2"; shift ;;
    --dir) dir=$2; shift ;;
    --clobber) clobber=1 ;;
    -*) echo "stub gh: unknown option $1" >&2; exit 3 ;;
    *) files="$files $1" ;;
  esac
  shift
done
rel=$GH_STUB/$tag
case $sub in view|download|upload) ;; *) echo "stub gh: never expected: release $sub" >&2; exit 3 ;; esac
[ -d "$rel" ] || { echo "release not found" >&2; exit 1; }
case $sub in
  view) case $json in isDraft) echo false ;; assets) ls "$rel" ;; *) exit 3 ;; esac ;;
  download)
    for p in $patterns; do
      [ -f "$rel/$p" ] || { echo "no assets match the file pattern" >&2; exit 1; }
      cp "$rel/$p" "$dir/"
    done ;;
  upload)
    for f in $files; do
      n=$(basename "$f")
      if [ -e "$rel/$n" ] && [ -z "$clobber" ]; then echo "asset under the same name already exists: $n" >&2; exit 1; fi
      cp "$f" "$rel/$n"
    done ;;
esac
STUB
chmod +x "$work/bin/gh"
export GH_STUB=$work/gh
pub() { PATH="$work/bin:$PATH" tools/release.sh publish-image "$@"; }
tpub() { pub "$@" --repo test/eirl-encoder-stub --pubkey "$PUB"; }
gh_release() {   # gh_release VERSION MANIFEST_DIR [ASSET…]: a published release with that manifest
  rm -rf "$GH_STUB/v$1" && mkdir -p "$GH_STUB/v$1"
  cp "$2/manifest.json" "$2/manifest.json.sig" "$GH_STUB/v$1/"
  local a
  for a in "${@:3}"; do echo old > "$GH_STUB/v$1/$a"; done
}
uploads() { grep -c '^gh release upload' "$GH_STUB/calls.log" 2>/dev/null || true; }

refused "the test key for the real repository" "only for a repository other" pub "$ver" --repo enhanced-irl/eirl-encoder --pubkey "$PUB" --yes
refused "the test key for the real repository, in capitals" "only for a repository other" pub "$ver" --repo Enhanced-IRL/EIRL-Encoder --pubkey "$PUB" --yes
refused "the test key for the real repository's URL" "only for a repository other" pub "$ver" --repo https://github.com/enhanced-irl/eirl-encoder.git --pubkey "$PUB" --yes
refused "--pubkey without --repo" "go together" pub "$ver" --pubkey "$PUB" --yes
refused "an image signed with a test key, for real" "does not verify" pub "$ver" --yes
[ ! -s "$GH_STUB/calls.log" ] && ok "nothing asked of GitHub before the local checks pass" || bad "gh was called: $(cat "$GH_STUB/calls.log")"
refused "no GitHub release v$ver" "there is no GitHub release v$ver" tpub "$ver" --yes
gh_release "$ver" "$work/rel-other"
refused "a release whose manifest.json is another version" "is version $other, not $ver" tpub "$ver" --yes
gh_release "$ver" "$work/rel-again"
refused "a release with another manifest.json of the same version" "another manifest.json" tpub "$ver" --yes
mkdir -p "build/out/release/$other" && cp -R "$out" "build/out/release/$other/image"
gh_release "$other" "$work/rel-other"
refused "an image of another release" "the image carries release $ver, not $other" tpub "$other" --yes
mv "$out/$base-sources.md" "$work/sources.keep"
refused "no sources.md" "IMAGE.md §9.3" tpub "$ver" --yes
mv "$work/sources.keep" "$out/$base-sources.md"
mv "$img" "$work/img.keep"
dd if=/dev/zero of="$img" bs=1 count=0 seek=2147483648 2>/dev/null   # 2 GiB, sparse
refused "an image of 2 GiB" "under 2 GiB" tpub "$ver" --yes
mv "$work/img.keep" "$img"
gh_release "$ver" "$work/rel" "$base.img.xz"
refused "a file already in the release, without --clobber" "already has $base.img.xz: pass --clobber" tpub "$ver" --yes
gh_release "$ver" "$work/rel"
n=$(uploads)
refused "a wrong confirmation" "not confirmed" sh -c 'echo nope | "$@"' sh env PATH="$work/bin:$PATH" tools/release.sh publish-image "$ver" \
  --repo test/eirl-encoder-stub --pubkey "$PUB"
[ "$(uploads)" = "$n" ] && ok "nothing uploaded without the confirmation" || bad "uploaded without the confirmation"
passes "publish-image, confirmed by typing the version" sh -c 'echo "$1" | "${@:2}"' sh "$ver" env PATH="$work/bin:$PATH" \
  tools/release.sh publish-image "$ver" --repo test/eirl-encoder-stub --pubkey "$PUB"
for f in $want; do cmp -s "$out/$f" "$GH_STUB/v$ver/$f" || bad "$f was not uploaded"; done
[ "$(ls "$GH_STUB/v$ver" | wc -l | tr -d ' ')" = 8 ] && ok "the release has its manifest, its signature and the 6 image files" ||
  bad "release files: $(ls "$GH_STUB/v$ver" | tr '\n' ' ')"
grep -q "^gh release upload v$ver --repo test/eirl-encoder-stub $out/$base.img.xz" "$GH_STUB/calls.log" &&
  ! grep -q -- "--clobber" "$GH_STUB/calls.log" && ok "uploaded without --clobber" || bad "upload: $(grep upload "$GH_STUB/calls.log")"
refused "publishing the same image again" "already has" tpub "$ver" --yes
passes "publishing it again with --clobber" tpub "$ver" --yes --clobber
grep -q "^gh release upload v$ver .*--clobber" "$GH_STUB/calls.log" && ok "--clobber reaches gh" || bad "--clobber not passed"
if grep -E '^gh release (create|edit|delete)|--latest|--draft' "$GH_STUB/calls.log"; then
  bad "publish-image created, edited or deleted a release, or changed latest"
else
  ok "only view, download and upload: no release created or edited, latest untouched"
fi
refused "publish with image options" "publish takes only --yes" tools/release.sh publish --clobber

echo
if [ "$failed" = 0 ]; then echo "image-sign-test: all passed"; else echo "image-sign-test: FAILED"; exit 1; fi
