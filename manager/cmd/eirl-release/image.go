package main

// The SD card image's manifest, signature and verification (IMAGE.md §8): image-manifest, sign-image,
// verify-image and pem. tools/release.sh image and publish-image drive them.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
)

var commitArg = regexp.MustCompile(`^[0-9a-f]{12,40}$`)

// imageManifest writes the image manifest of IMAGE.md §8.1. It hashes the compressed image itself, takes
// software.version and its sha256 from the OTA manifest installed in the image, and the raw image's size and
// sha256 either from -raw-size/-raw-sha256 (measured by the recipe) or from the decompressed image piped in
// (-raw-stdin: xz -dc IMG | eirl-release image-manifest … -raw-stdin).
func imageManifest(args []string, stdin io.Reader) error {
	fs := flag.NewFlagSet("image-manifest", flag.ContinueOnError)
	img := fs.String("image", "", "the compressed image, named eirl-encoder-<version>-jetson-nano.img.xz")
	software := fs.String("software", "", "the OTA manifest.json of the release installed in the image")
	ver := fs.String("version", "", "the image's version (default: the software's; <software>-N for a rebuild)")
	board := fs.String("board", update.DefaultImageBoard, "the board")
	rawSize := fs.Int64("raw-size", 0, "size of the raw (decompressed) image, in bytes")
	rawSHA := fs.String("raw-sha256", "", "sha256 of the raw image")
	rawStdin := fs.Bool("raw-stdin", false, "hash the raw image piped on stdin instead (xz -dc IMG | …)")
	built := fs.String("built", "", "build time (RFC3339; default now)")
	commit := fs.String("commit", "", "the image recipe's commit (12 to 40 hex characters; the first 12 are written)")
	minCard := fs.Int64("min-card-bytes", update.DefaultMinCardBytes, "smallest card the image is for, in bytes")
	out := fs.String("out", "", "manifest to write (default: the image's name with .json instead of .xz)")
	shaOut := fs.String("sha256-out", "", "also write the image's sha256 here, in sha256sum format (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *img == "" || *software == "" || *commit == "" {
		return errors.New("image-manifest: -image, -software and -commit are required")
	}
	if *rawStdin == (*rawSize != 0 || *rawSHA != "") {
		return errors.New("image-manifest: give either -raw-size and -raw-sha256, or -raw-stdin")
	}
	if !commitArg.MatchString(*commit) {
		return fmt.Errorf("image-manifest: -commit %q is not a git commit (12 to 40 lowercase hex characters)", *commit)
	}
	if update.ImageBoardL4T(*board) == "" {
		return fmt.Errorf("image-manifest: unknown board %q", *board)
	}
	if *out == "" {
		if !strings.HasSuffix(*img, ".xz") {
			return errors.New("image-manifest: -image must end in .img.xz")
		}
		*out = strings.TrimSuffix(*img, ".xz") + ".json"
	}

	// The release installed in the image
	sraw, err := os.ReadFile(*software)
	if err != nil {
		return err
	}
	sm, err := update.ParseManifest(sraw)
	if err != nil {
		return fmt.Errorf("-software %s: %w", *software, err)
	}
	ssum := sha256.Sum256(sraw)
	if *ver == "" {
		*ver = sm.Version
	}
	if err := update.CheckImageVersion(*ver, sm.Version); err != nil {
		return err
	}
	if want := update.ImageName(*ver, *board); filepath.Base(*img) != want {
		return fmt.Errorf("the image must be named %s", want)
	}

	// The compressed image (hashed here) and the raw one
	fi, err := os.Stat(*img)
	if err != nil {
		return err
	}
	sum, err := update.FileSHA256(*img, fi.Size())
	if err != nil {
		return err
	}
	rs, rsum := *rawSize, *rawSHA
	if *rawStdin {
		if err := notATerminal(stdin); err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, stdin)
		if err != nil {
			return fmt.Errorf("reading the raw image on stdin: %w", err)
		}
		rs, rsum = n, hex.EncodeToString(h.Sum(nil))
	}

	t := time.Now().UTC().Truncate(time.Second)
	if *built != "" {
		if t, err = time.Parse(time.RFC3339, *built); err != nil {
			return fmt.Errorf("-built: %w", err)
		}
	}
	m := update.ImageManifest{V: 1, Product: update.ImageProduct, Board: *board, L4T: update.ImageBoardL4T(*board),
		Version:  *ver,
		Software: update.ImageSoftware{Version: sm.Version, ManifestSHA256: hex.EncodeToString(ssum[:])},
		Built:    t.UTC().Truncate(time.Second), Commit: (*commit)[:12],
		Image: update.ImageFile{Name: filepath.Base(*img), Size: fi.Size(), SHA256: sum,
			RawSize: rs, RawSHA256: rsum},
		MinCardBytes: *minCard}
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return err
	}
	if *shaOut != "" {
		if err := os.WriteFile(*shaOut, []byte(sum+"  "+m.Image.Name+"\n"), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "wrote %s: image %s %s (%d bytes, raw %d bytes), software %s\n", *out, m.Image.Name, m.Version,
		m.Image.Size, m.Image.RawSize, m.Software.Version)
	return nil
}

// notATerminal refuses to read a raw image from a terminal (nothing was piped in).
func notATerminal(r io.Reader) error {
	if f, ok := r.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return errors.New("-raw-stdin: nothing is piped in (xz -dc IMAGE.img.xz | eirl-release …)")
		}
	}
	return nil
}

// productOf reads only "product" (to point at the right command; never trusted for anything else).
func productOf(raw []byte) string {
	var p struct {
		Product string `json:"product"`
	}
	json.Unmarshal(raw, &p)
	return p.Product
}

// signImage signs the exact bytes of an image manifest, after checking that it IS one, in canonical form. The
// seed comes only on stdin and must match -pubkey (default: the production key), as for sign.
func signImage(args []string, stdin io.Reader) error {
	fs := flag.NewFlagSet("sign-image", flag.ContinueOnError)
	in := fs.String("in", "", "image manifest to sign (eirl-encoder-<version>-jetson-nano.img.json)")
	out := fs.String("out", "", "signature file (default <in>.sig)")
	pub := fs.String("pubkey", update.ProductionKey, "public key the seed must match (base64)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("sign-image: -in is required")
	}
	if *out == "" {
		*out = *in + ".sig"
	}
	want, err := update.ParsePublicKey(*pub)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	// Checked before the seed is read: an OTA manifest (or anything else) is never signed as an image
	if p := productOf(raw); p == update.Product {
		return fmt.Errorf("%s is an OTA manifest (product %q), not an image manifest: sign it with eirl-release sign", *in, p)
	}
	if err := update.CanonicalImageManifest(raw); err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	priv, err := readSeed(stdin)
	if err != nil {
		return err
	}
	defer wipe(priv)
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), want) {
		return fmt.Errorf("the seed does not match the public key %s", update.KeyID(want))
	}
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)))
	if _, err := update.VerifyImageManifest(raw, sig, []ed25519.PublicKey{want}); err != nil {
		return fmt.Errorf("the signed image manifest does not verify: %w", err)
	}
	if err := os.WriteFile(*out, append(sig, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "signed %s with key %s\n", *in, update.KeyID(want))
	return nil
}

// errNotChecked marks verify-image failures that are not a verdict on the download (usage, a missing file).
type errNotChecked struct{ error }

func (e errNotChecked) Unwrap() error { return e.error }

// verifyImage checks a downloaded image: the manifest's signature (the compiled-in production key, or -pubkey),
// then optionally the compressed image (-image: size and sha256) and the raw image (-raw-stdin: the decompressed
// stream's size and sha256). See verifyImageExit for the exit codes.
func verifyImage(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("verify-image", flag.ContinueOnError)
	man := fs.String("manifest", "", "the image manifest (eirl-encoder-<version>-jetson-nano.img.json)")
	sig := fs.String("sig", "", "its signature (default <manifest>.sig)")
	img := fs.String("image", "", "the compressed image to check (.img.xz; optional)")
	rawStdin := fs.Bool("raw-stdin", false, "check the raw image piped on stdin (xz -dc IMG | …; optional)")
	printField := fs.String("print", "", "print only this field of the verified manifest (e.g. software.version)")
	pub := keyFlag(fs)
	if err := fs.Parse(args); err != nil {
		return errNotChecked{err}
	}
	if *man == "" {
		return errNotChecked{errors.New("-manifest is required")}
	}
	if *sig == "" {
		*sig = *man + ".sig"
	}
	k, err := update.ParsePublicKey(*pub)
	if err != nil {
		return errNotChecked{err}
	}
	raw, err := os.ReadFile(*man)
	if err != nil {
		return errNotChecked{err}
	}
	s, err := os.ReadFile(*sig)
	if err != nil {
		return errNotChecked{err}
	}
	if *rawStdin {
		if err := notATerminal(stdin); err != nil {
			return errNotChecked{err}
		}
	}
	m, err := update.VerifyImageManifest(raw, s, []ed25519.PublicKey{k})
	if err != nil {
		if update.Code(err) == update.CodeBadManifest && productOf(raw) == update.Product {
			return fmt.Errorf("%s is an OTA manifest, not an image manifest (use eirl-release verify): %w", *man, err)
		}
		return fmt.Errorf("image manifest: %w", err)
	}
	say := stdout
	if *printField != "" {
		say = io.Discard
	}
	fmt.Fprintf(say, "manifest OK: %s %s for %s (L4T %s), software %s, built %s from %s, key %s\n", m.Product, m.Version,
		m.Board, m.L4T, m.Software.Version, m.Built.Format(time.RFC3339), m.Commit, update.KeyID(k))
	if *img != "" {
		sum, err := update.FileSHA256(*img, m.Image.Size)
		if err != nil {
			if update.Code(err) != update.CodeChecksum {
				return errNotChecked{err}
			}
			return fmt.Errorf("image: %w", err)
		}
		if sum != m.Image.SHA256 {
			return &update.Error{Code: update.CodeChecksum,
				Err: fmt.Errorf("image: %s has sha256 %s, the manifest says %s", filepath.Base(*img), sum, m.Image.SHA256)}
		}
		fmt.Fprintf(say, "image OK: %s (%d bytes, sha256 %s)\n", filepath.Base(*img), m.Image.Size, m.Image.SHA256)
		if filepath.Base(*img) != m.Image.Name {
			fmt.Fprintf(os.Stderr, "note: the file is named %s; the manifest names it %s\n", filepath.Base(*img), m.Image.Name)
		}
	}
	if *rawStdin {
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(stdin, m.Image.RawSize+1))
		if err != nil {
			return errNotChecked{fmt.Errorf("reading the raw image on stdin: %w", err)}
		}
		if n != m.Image.RawSize {
			more := ""
			if n > m.Image.RawSize {
				more = " or more"
			}
			return &update.Error{Code: update.CodeChecksum,
				Err: fmt.Errorf("raw image: %d%s bytes on stdin, the manifest says %d", n, more, m.Image.RawSize)}
		}
		if sum := hex.EncodeToString(h.Sum(nil)); sum != m.Image.RawSHA256 {
			return &update.Error{Code: update.CodeChecksum,
				Err: fmt.Errorf("raw image: sha256 %s, the manifest says %s", sum, m.Image.RawSHA256)}
		}
		fmt.Fprintf(say, "raw image OK: %d bytes, sha256 %s (what is written to the card)\n", m.Image.RawSize, m.Image.RawSHA256)
	}
	if *printField != "" {
		v, err := field(m, *printField)
		if err != nil {
			return errNotChecked{err}
		}
		fmt.Fprintln(stdout, v)
		return nil
	}
	var skipped []string
	if *img == "" {
		skipped = append(skipped, "the image (-image FILE.img.xz)")
	}
	if !*rawStdin {
		skipped = append(skipped, "the raw image (xz -dc FILE.img.xz | eirl-release verify-image … -raw-stdin)")
	}
	if len(skipped) > 0 {
		fmt.Fprintf(stdout, "not checked: %s\n", strings.Join(skipped, " and "))
	}
	return nil
}

// verifyImageExit is verify-image's exit code: 0 everything checked is good; 1 the download does NOT match
// (bad signature, not an image manifest, wrong size or sha256): do not flash it; 2 it could not be checked
// (usage, a missing or unreadable file).
func verifyImageExit(err error) int {
	var nc errNotChecked
	switch {
	case err == nil:
		return 0
	case errors.As(err, &nc):
		return 2
	}
	switch update.Code(err) {
	case update.CodeBadSignature, update.CodeBadManifest, update.CodeChecksum:
		return 1
	}
	return 2
}

// field prints one field of a verified manifest by its JSON path ("version", "software.version", "image.size").
func field(v any, path string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var cur any
	if err := d.Decode(&cur); err != nil {
		return "", err
	}
	for _, k := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", fmt.Errorf("-print %s: no such field", path)
		}
		if cur, ok = obj[k]; !ok {
			return "", fmt.Errorf("-print %s: no such field", path)
		}
	}
	switch x := cur.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	}
	return "", fmt.Errorf("-print %s: not a single value", path)
}

// pemKey prints the PEM (SubjectPublicKeyInfo) form of a public key, for OpenSSL. release/eirl-release.pem is
// `eirl-release pem -pubkey "$(cat release/eirl-release.pub)"` (a test checks it).
func pemKey(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("pem", flag.ContinueOnError)
	pub := keyFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	k, err := update.ParsePublicKey(*pub)
	if err != nil {
		return err
	}
	b, err := update.PublicKeyPEM(k)
	if err != nil {
		return err
	}
	_, err = stdout.Write(b)
	return err
}
