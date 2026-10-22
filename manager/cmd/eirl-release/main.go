// eirl-release: the release tool's Go half (tools/release.sh drives it; UPDATES.md §6). Runs on the
// maintainer's machine, never on the board.
//
//	eirl-release pack -dir PKG -out BUNDLE.tar.gz [-mtime RFC3339]
//	eirl-release manifest -version V -bundle BUNDLE [-min-from V] [-notes DIR] [-released RFC3339] -out manifest.json
//	eirl-release sign -in manifest.json -out manifest.json.sig [-pubkey B64]   (seed: base64, on stdin)
//	eirl-release verify -manifest F [-sig F] [-bundle F] [-pubkey B64] [-print FIELD]
//	eirl-release keygen                       a THROWAWAY key pair for tests (seed and public key, base64)
//	eirl-release check -url URL -current V -dir DIR [-pubkey B64]   what a board does: check, download, verify
//	eirl-release semver V                    validates a version (no leading "v")
//	eirl-release check-files -files files.txt   every path is one the board's update helper accepts (UPDATES.md §3)
//	eirl-release layer -repo DIR -out DIR -release V -debs DIR -build-out DIR [-commit C] [-sources F] [-update-modules-lock]
//	                                          the system layer (UPDATES.md §12) a bundle carries as system/
//
// The SD card image (IMAGE.md §8):
//
//	eirl-release image-manifest -image IMG.img.xz -software manifest.json -commit SHA
//	    (-raw-size N -raw-sha256 H | -raw-stdin) [-version V] [-built RFC3339] [-min-card-bytes N]
//	    [-out IMG.img.json] [-sha256-out IMG.img.xz.sha256]
//	eirl-release sign-image -in IMG.img.json [-out IMG.img.json.sig] [-pubkey B64]   (seed: base64, on stdin)
//	eirl-release verify-image -manifest IMG.img.json [-sig F] [-image IMG.img.xz] [-raw-stdin] [-pubkey B64] [-print FIELD]
//	    exit 0 OK, 1 the download does not match (do not flash it), 2 could not check
//	eirl-release pem [-pubkey B64]           the public key in PEM form, for OpenSSL (release/eirl-release.pem)
//
// The signing seed is only ever read from stdin (a pipe from the secret store); it is never written,
// printed or put in an error message.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update/syslayer"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: eirl-release pack|manifest|sign|verify|keygen|check|semver|image-manifest|sign-image|verify-image|pem …")
		os.Exit(64)
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "pack":
		err = pack(args)
	case "manifest":
		err = manifest(args)
	case "sign":
		err = sign(args, os.Stdin)
	case "verify":
		err = verify(args)
	case "image-manifest":
		err = imageManifest(args, os.Stdin)
	case "sign-image":
		err = signImage(args, os.Stdin)
	case "verify-image":
		if err = verifyImage(args, os.Stdin, os.Stdout); err != nil {
			code := verifyImageExit(err)
			if code == 1 {
				fmt.Fprintf(os.Stderr, "eirl-release: FAILED: %v\n"+
					"Do not flash this image: it is not the one EnhancedIRL published, or it was damaged on the way. Download it again.\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "eirl-release: could not check: %v\n", err)
			}
			os.Exit(code)
		}
	case "pem":
		err = pemKey(args, os.Stdout)
	case "keygen":
		err = keygen()
	case "check":
		err = check(args)
	case "layer":
		err = layer(args)
	case "check-files":
		err = checkFiles(args)
	case "semver":
		if len(args) != 1 {
			err = errors.New("usage: eirl-release semver VERSION")
		} else if v, perr := update.ParseVersion(args[0]); perr != nil {
			err = perr
		} else {
			fmt.Println(v)
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "eirl-release:", err)
		os.Exit(1)
	}
}

// checkFiles: a bundle whose files.txt names a path outside the helpers' allow-list is refused whole by every board
// (bad_bundle), as the bundles of 0.7.0 to 0.10.0 were. The allow-list has not changed since 0.1.0, so this one is
// every helper's.
func checkFiles(args []string) error {
	fs := flag.NewFlagSet("check-files", flag.ContinueOnError)
	files := fs.String("files", "", "files.txt")
	if err := fs.Parse(args); err != nil || *files == "" {
		return errors.New("usage: eirl-release check-files -files files.txt")
	}
	b, err := os.ReadFile(*files)
	if err != nil {
		return err
	}
	paths, err := update.ParseFiles(b)
	if err != nil {
		return fmt.Errorf("%v: every board would refuse this bundle (bad_bundle); a system file goes in the system layer (UPDATES.md §12.2)", err)
	}
	fmt.Printf("files.txt: %d paths, all accepted by the update helper\n", len(paths))
	return nil
}

// layer builds the system layer from the repository's system/ (syslayer.Build) and prints what it holds.
func layer(args []string) error {
	fs := flag.NewFlagSet("layer", flag.ContinueOnError)
	var in syslayer.BuildInput
	fs.StringVar(&in.Repo, "repo", ".", "the repository's root")
	fs.StringVar(&in.Out, "out", "", "the layer's directory (made from scratch)")
	fs.StringVar(&in.Release, "release", "", "the release that carries it")
	fs.StringVar(&in.DebCache, "debs", "", "the .deb files of system/packages.lock (build/system.sh)")
	fs.StringVar(&in.BuildOut, "build-out", "", "build/out: the modules and firmware of build/build.sh")
	fs.StringVar(&in.Commit, "commit", "", "the repository's commit (SOURCES.md)")
	fs.StringVar(&in.SourcesOut, "sources", "", "write the layer's SOURCES.md here")
	fs.BoolVar(&in.UpdateModulesLock, "update-modules-lock", false, "rewrite system/modules.lock with what build/build.sh built")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if in.Out == "" || in.Release == "" || in.DebCache == "" || in.BuildOut == "" {
		return errors.New("usage: eirl-release layer -repo DIR -out DIR -release V -debs DIR -build-out DIR [-commit C] [-sources F]")
	}
	if _, err := update.ParseVersion(in.Release); err != nil {
		return err
	}
	l, err := syslayer.Build(in)
	if err != nil {
		return err
	}
	var debs int64
	for _, p := range l.Packages {
		debs += p.Size
	}
	fmt.Printf("system layer %s: %d packages (%d KB), %d files, %d units, base of %d packages\n", l.Release, len(l.Packages),
		debs>>10, len(l.Files), len(l.Units), len(l.Base))
	return nil
}

// pack writes a deterministic .tar.gz of dir: sorted entries, root:root, fixed time, modes 0755/0644,
// no symlinks or special files (the board's helper rejects them).
func pack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	dir := fs.String("dir", "", "package directory")
	out := fs.String("out", "", "bundle to write")
	mtime := fs.String("mtime", "", "time of every entry (RFC3339; default now, truncated to the second)")
	fs.Parse(args)
	if *dir == "" || *out == "" {
		return errors.New("pack: -dir and -out are required")
	}
	t := time.Now().UTC().Truncate(time.Second)
	if *mtime != "" {
		var err error
		if t, err = time.Parse(time.RFC3339, *mtime); err != nil {
			return err
		}
	}
	var entries []string
	err := filepath.WalkDir(*dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(*dir, p)
		if rel == "." {
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("pack: %s is not a regular file", rel)
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(entries)
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, rel := range entries {
		full := filepath.Join(*dir, rel)
		fi, err := os.Lstat(full)
		if err != nil {
			return err
		}
		h := &tar.Header{Name: filepath.ToSlash(rel), ModTime: t, Uid: 0, Gid: 0, Uname: "root", Gname: "root", Format: tar.FormatPAX}
		if fi.IsDir() {
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, h.Name+"/", 0o755
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			continue
		}
		h.Typeflag, h.Size, h.Mode = tar.TypeReg, fi.Size(), 0o644
		if fi.Mode()&0o111 != 0 {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		src, err := os.Open(full)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

// manifest writes manifest.json for a bundle (size and sha256 computed here).
func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	ver := fs.String("version", "", "release version (semver)")
	bundle := fs.String("bundle", "", "the bundle")
	minFrom := fs.String("min-from", "", "oldest version that may update directly (optional)")
	notes := fs.String("notes", "", "directory with en.md, es.md, fr.md (optional)")
	released := fs.String("released", "", "release time (RFC3339; default now)")
	out := fs.String("out", "manifest.json", "manifest to write")
	fs.Parse(args)
	if _, err := update.ParseVersion(*ver); err != nil {
		return err
	}
	if *minFrom != "" {
		if _, err := update.ParseVersion(*minFrom); err != nil {
			return fmt.Errorf("-min-from: %w", err)
		}
	}
	if filepath.Base(*bundle) != update.BundleName(*ver) {
		return fmt.Errorf("bundle must be named %s", update.BundleName(*ver))
	}
	fi, err := os.Stat(*bundle)
	if err != nil {
		return err
	}
	sum, err := update.FileSHA256(*bundle, fi.Size())
	if err != nil {
		return err
	}
	rel := time.Now().UTC().Truncate(time.Second)
	if *released != "" {
		if rel, err = time.Parse(time.RFC3339, *released); err != nil {
			return err
		}
	}
	m := update.Manifest{V: 1, Product: update.Product, Version: *ver, Released: rel.UTC(), MinFrom: *minFrom,
		Bundle: update.Bundle{Name: filepath.Base(*bundle), Size: fi.Size(), SHA256: sum}}
	if *notes != "" {
		m.Notes = map[string]string{}
		for _, lang := range []string{"en", "es", "fr"} {
			b, err := os.ReadFile(filepath.Join(*notes, lang+".md"))
			if errors.Is(err, iofs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			m.Notes[lang] = strings.TrimSpace(string(b))
		}
		if m.Notes["en"] == "" {
			return fmt.Errorf("notes: %s/en.md is required", *notes)
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(b, '\n'), 0o644)
}

// readSeed reads the base64 seed from stdin (first line, at most 1 KiB) and zeroes the decoded seed. The line
// itself (a string) cannot be zeroed and lives until the process exits (UPDATES.md §3).
func readSeed(stdin io.Reader) (ed25519.PrivateKey, error) {
	r := bufio.NewReader(io.LimitReader(stdin, 1024))
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, errors.New("cannot read the seed from stdin")
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("stdin is not a base64 seed of %d bytes", ed25519.SeedSize)
	}
	defer wipe(seed)
	return ed25519.NewKeyFromSeed(seed), nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// sign signs the exact bytes of the manifest. The key must match -pubkey (default: the production
// key), so a wrong key never produces a release no board accepts. It signs only a valid OTA manifest
// (never an image manifest: that is sign-image), checked before the seed is read.
func sign(args []string, stdin io.Reader) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	in := fs.String("in", "manifest.json", "manifest to sign")
	out := fs.String("out", "", "signature file (default <in>.sig)")
	pub := fs.String("pubkey", update.ProductionKey, "public key the seed must match (base64)")
	fs.Parse(args)
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
	if p := productOf(raw); p == update.ImageProduct {
		return fmt.Errorf("%s is an image manifest (product %q), not an OTA manifest: sign it with eirl-release sign-image", *in, p)
	}
	if _, err := update.ParseManifest(raw); err != nil {
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
	sig := ed25519.Sign(priv, raw)
	if _, err := update.VerifyManifest(raw, []byte(base64.StdEncoding.EncodeToString(sig)), []ed25519.PublicKey{want}); err != nil {
		return fmt.Errorf("the signed manifest does not verify: %w", err)
	}
	if err := os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "signed %s with key %s\n", *in, update.KeyID(want))
	return nil
}

func keyFlag(fs *flag.FlagSet) *string {
	return fs.String("pubkey", update.ProductionKey, "trusted public key (base64)")
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	man := fs.String("manifest", "manifest.json", "manifest")
	sig := fs.String("sig", "", "signature (default <manifest>.sig)")
	bundle := fs.String("bundle", "", "bundle to check (optional)")
	printField := fs.String("print", "", "print only this field of the verified manifest (e.g. version)")
	pub := keyFlag(fs)
	fs.Parse(args)
	if *sig == "" {
		*sig = *man + ".sig"
	}
	k, err := update.ParsePublicKey(*pub)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*man)
	if err != nil {
		return err
	}
	s, err := os.ReadFile(*sig)
	if err != nil {
		return err
	}
	m, err := update.VerifyManifest(raw, s, []ed25519.PublicKey{k})
	if err != nil {
		return err
	}
	say := io.Writer(os.Stdout)
	if *printField != "" {
		say = io.Discard
	}
	fmt.Fprintf(say, "manifest OK: %s %s, key %s\n", m.Product, m.Version, update.KeyID(k))
	if *bundle != "" {
		sum, err := update.FileSHA256(*bundle, m.Bundle.Size)
		if err != nil {
			return err
		}
		if sum != m.Bundle.SHA256 {
			return fmt.Errorf("bundle sha256 %s, manifest says %s", sum, m.Bundle.SHA256)
		}
		fmt.Fprintf(say, "bundle OK: %s (%d bytes)\n", m.Bundle.Name, m.Bundle.Size)
	}
	if *printField != "" {
		v, err := field(m, *printField)
		if err != nil {
			return err
		}
		fmt.Println(v)
	}
	return nil
}

// keygen prints a THROWAWAY key pair (tests and --local board tests). Production keys are never made here.
func keygen() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Printf("seed=%s\npubkey=%s\n", base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(pub))
	return nil
}

// check runs what a board does against a release URL: fetch + verify the manifest, compare with
// -current, download and verify the bundle into -dir.
func check(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	url := fs.String("url", update.DefaultManifestURL, "manifest URL")
	current := fs.String("current", "0.0.0", "installed version to compare with")
	dir := fs.String("dir", "", "where to download the bundle (default: no download)")
	pub := keyFlag(fs)
	fs.Parse(args)
	k, err := update.ParsePublicKey(*pub)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	f := &update.Fetcher{UserAgent: "eirl-release-check"}
	ctx := context.Background()
	fd, err := f.FetchManifest(ctx, *url)
	if err != nil {
		return err
	}
	m, err := update.VerifyManifest(fd.Raw, fd.Sig, []ed25519.PublicKey{k})
	if err != nil {
		return err
	}
	log.Info("manifest verified", "version", m.Version, "minFrom", m.MinFrom, "bundle", fd.BundleURL(m), "key", update.KeyID(k))
	if err := update.CheckUpgrade(m, *current); err != nil {
		return err
	}
	log.Info("update available", "from", *current, "to", m.Version)
	if *dir == "" {
		return nil
	}
	dst := filepath.Join(*dir, update.BundleFile)
	if err := f.Download(ctx, fd.BundleURL(m), dst, m.Bundle.Size, m.Bundle.SHA256, nil); err != nil {
		return err
	}
	log.Info("bundle downloaded and verified", "path", dst, "bytes", m.Bundle.Size)
	return nil
}
