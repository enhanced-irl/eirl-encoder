package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
)

// A THROWAWAY key: the production seed is never used in tests.
type key struct {
	seed string // base64, as EIRL_RELEASE_KEY_CMD prints it
	pub  string
}

func newKey(t *testing.T) key {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key{base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(pub)}
}

func (k key) stdin() io.Reader { return strings.NewReader(k.seed + "\n") }

// noSeed fails the test if the seed is read: a refusal must come before it.
type noSeed struct{ t *testing.T }

func (n noSeed) Read([]byte) (int, error) {
	n.t.Error("the seed was read")
	return 0, io.EOF
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func write(t *testing.T, p string, b []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// otaRelease writes the OTA manifest.json of a release of version v (and signs it with k when k.seed is set).
func otaRelease(t *testing.T, dir, v string, k key) string {
	t.Helper()
	bundle := write(t, filepath.Join(dir, update.BundleName(v)), []byte("bundle "+v))
	notes := filepath.Join(dir, "notes")
	write(t, filepath.Join(notes, "en.md"), []byte("Fixes."))
	m := filepath.Join(dir, "manifest.json")
	if err := manifest([]string{"-version", v, "-bundle", bundle, "-notes", notes, "-released", "2026-10-20T10:00:00Z", "-out", m}); err != nil {
		t.Fatal(err)
	}
	if k.seed != "" {
		if err := sign([]string{"-in", m, "-pubkey", k.pub}, k.stdin()); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// rawImage is a fake raw disk image: n sectors, some random data and a lot of zeros (it compresses like one).
func rawImage(t *testing.T, sectors int) []byte {
	t.Helper()
	b := make([]byte, sectors*512)
	for i := 0; i < len(b); i += 64 << 10 {
		rand.Read(b[i : i+min(4096, len(b)-i)])
	}
	return b
}

func readImageManifest(t *testing.T, p string) update.ImageManifest {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m update.ImageManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestImageManifestFromFlags(t *testing.T) {
	dir := t.TempDir()
	sm := otaRelease(t, dir, "0.1.1", key{})
	raw := rawImage(t, 64)
	img := write(t, filepath.Join(dir, "eirl-encoder-0.1.1-jetson-nano.img.xz"), []byte("compressed image"))
	commit := strings.Repeat("abcdef0123", 4)
	err := imageManifest([]string{"-image", img, "-software", sm, "-commit", commit, "-built", "2026-10-20T12:00:00+02:00",
		"-raw-size", "32768", "-raw-sha256", sum(raw), "-sha256-out", img + ".sha256"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "eirl-encoder-0.1.1-jetson-nano.img.json") // the default -out
	m := readImageManifest(t, out)
	smRaw, _ := os.ReadFile(sm)
	want := update.ImageManifest{V: 1, Product: "eirl-encoder-image", Board: "jetson-nano-devkit-b01", L4T: "32.7.6", Version: "0.1.1",
		Software: update.ImageSoftware{Version: "0.1.1", ManifestSHA256: sum(smRaw)},
		Built:    time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), Commit: commit[:12],
		Image: update.ImageFile{Name: "eirl-encoder-0.1.1-jetson-nano.img.xz", Size: 16, SHA256: sum([]byte("compressed image")),
			RawSize: 32768, RawSHA256: sum(raw)},
		MinCardBytes: 16000000000}
	if m != want {
		t.Errorf("manifest\n%+v\nwant\n%+v", m, want)
	}
	b, _ := os.ReadFile(out)
	if err := update.CanonicalImageManifest(b); err != nil {
		t.Errorf("not canonical: %v", err)
	}
	if s, _ := os.ReadFile(img + ".sha256"); string(s) != sum([]byte("compressed image"))+"  eirl-encoder-0.1.1-jetson-nano.img.xz\n" {
		t.Errorf("sha256 file %q", s)
	}

	// A rebuild of the image of the same release
	img2 := write(t, filepath.Join(dir, "eirl-encoder-0.1.1-2-jetson-nano.img.xz"), []byte("rebuilt"))
	if err := imageManifest([]string{"-image", img2, "-software", sm, "-version", "0.1.1-2", "-commit", commit[:12],
		"-raw-size", "512", "-raw-sha256", sum(raw), "-min-card-bytes", "32000000000"}, nil); err != nil {
		t.Fatal(err)
	}
	if m := readImageManifest(t, strings.TrimSuffix(img2, ".xz")+".json"); m.Version != "0.1.1-2" || m.Software.Version != "0.1.1" ||
		m.MinCardBytes != 32000000000 {
		t.Errorf("rebuild %+v", m)
	}

	// Refusals
	other := write(t, filepath.Join(dir, "image.img.xz"), []byte("x"))
	imgOf := func(v string) string {
		return write(t, filepath.Join(dir, update.ImageName(v, update.DefaultImageBoard)), []byte("x"))
	}
	good := []string{"-software", sm, "-commit", commit, "-raw-size", "512", "-raw-sha256", sum(raw)}
	for name, args := range map[string][]string{
		"wrong file name":   append([]string{"-image", other}, good...),
		"another version":   append([]string{"-image", imgOf("0.1.2"), "-version", "0.1.2"}, good...),
		"name of a rebuild": append([]string{"-image", imgOf("0.1.1-3")}, good...),
		"bad rebuild":       append([]string{"-image", imgOf("0.1.1-x"), "-version", "0.1.1-x"}, good...),
		"both raw modes":    append([]string{"-image", img, "-raw-stdin"}, good...),
		"no raw":            {"-image", img, "-software", sm, "-commit", commit},
		"raw size only":     {"-image", img, "-software", sm, "-commit", commit, "-raw-size", "512"},
		"short commit":      {"-image", img, "-software", sm, "-commit", "abc123", "-raw-size", "512", "-raw-sha256", sum(raw)},
		"commit not hex":    {"-image", img, "-software", sm, "-commit", strings.Repeat("g", 12), "-raw-size", "512", "-raw-sha256", sum(raw)},
		"raw not sectors":   {"-image", img, "-software", sm, "-commit", commit, "-raw-size", "513", "-raw-sha256", sum(raw)},
		"raw sha":           {"-image", img, "-software", sm, "-commit", commit, "-raw-size", "512", "-raw-sha256", "abc"},
		"raw over the card": {"-image", img, "-software", sm, "-commit", commit, "-raw-size", "16000000512", "-raw-sha256", sum(raw)},
		"unknown board":     append([]string{"-image", img, "-board", "jetson-orin"}, good...),
		"software is image": {"-image", img, "-software", out, "-commit", commit, "-raw-size", "512", "-raw-sha256", sum(raw)},
		"no software":       {"-image", img, "-commit", commit, "-raw-size", "512", "-raw-sha256", sum(raw)},
		"bad built":         append([]string{"-image", img, "-built", "yesterday"}, good...),
	} {
		o := filepath.Join(dir, "refused.json")
		os.Remove(o)
		if err := imageManifest(append(args, "-out", o), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(o); err == nil {
			t.Errorf("%s: wrote a manifest", name)
		}
	}
}

func xzAvailable(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz is not installed")
	}
}

// xzDC is `xz -dc file` as a stream.
func xzDC(t *testing.T, p string) io.Reader {
	t.Helper()
	cmd := exec.Command("xz", "-dc", p)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Wait() })
	return out
}

// A real image: raw → xz → image-manifest -raw-stdin (xz -dc) → sign-image → verify-image (xz -dc again).
func TestImageRawStdinFromXZ(t *testing.T) {
	xzAvailable(t)
	dir := t.TempDir()
	k := newKey(t)
	sm := otaRelease(t, dir, "0.1.1-test.1", k)
	raw := rawImage(t, 2048+7) // 1 MiB and a few sectors
	rawPath := write(t, filepath.Join(dir, "raw.img"), raw)
	img := filepath.Join(dir, "eirl-encoder-0.1.1-test.1-jetson-nano.img.xz")
	if out, err := exec.Command("sh", "-c", `xz -T0 -6 -c "$1" > "$2"`, "sh", rawPath, img).CombinedOutput(); err != nil {
		t.Fatalf("xz: %v %s", err, out)
	}
	if err := imageManifest([]string{"-image", img, "-software", sm, "-commit", "0123456789ab", "-raw-stdin"}, xzDC(t, img)); err != nil {
		t.Fatal(err)
	}
	man := strings.TrimSuffix(img, ".xz") + ".json"
	m := readImageManifest(t, man)
	if m.Image.RawSize != int64(len(raw)) || m.Image.RawSHA256 != sum(raw) {
		t.Fatalf("raw %d %s, want %d %s", m.Image.RawSize, m.Image.RawSHA256, len(raw), sum(raw))
	}
	if err := signImage([]string{"-in", man, "-pubkey", k.pub}, k.stdin()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := verifyImage([]string{"-manifest", man, "-image", img, "-raw-stdin", "-pubkey", k.pub}, xzDC(t, img), &out)
	if err != nil || verifyImageExit(err) != 0 {
		t.Fatalf("verify-image: %v", err)
	}
	for _, want := range []string{"manifest OK: eirl-encoder-image 0.1.1-test.1 for jetson-nano-devkit-b01 (L4T 32.7.6)", "image OK: ", "raw image OK: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "not checked") {
		t.Errorf("everything was checked:\n%s", out.String())
	}

	// The raw stream: one byte changed, one byte short, one byte more
	flipped := bytes.Clone(raw)
	flipped[len(raw)/2] ^= 1
	for name, stream := range map[string][]byte{"flipped": flipped, "short": raw[:len(raw)-1], "long": append(bytes.Clone(raw), 0)} {
		err := verifyImage([]string{"-manifest", man, "-raw-stdin", "-pubkey", k.pub}, bytes.NewReader(stream), io.Discard)
		if verifyImageExit(err) != 1 || !strings.Contains(err.Error(), "raw image") {
			t.Errorf("raw %s: %v (exit %d), want a raw image mismatch (exit 1)", name, err, verifyImageExit(err))
		}
	}
	// A corrupted .xz: xz -dc fails or gives another stream; the compressed image's sha256 says so first
	b := mustRead(t, img)
	b[len(b)/2] ^= 1
	bad := write(t, filepath.Join(dir, "bad", filepath.Base(img)), b)
	if err := verifyImage([]string{"-manifest", man, "-image", bad, "-pubkey", k.pub}, nil, io.Discard); verifyImageExit(err) != 1 {
		t.Errorf("corrupted image: %v", err)
	}
}

func TestSignAndVerifyImage(t *testing.T) {
	dir := t.TempDir()
	k, other := newKey(t), newKey(t)
	sm := otaRelease(t, dir, "0.1.1", k)
	img := write(t, filepath.Join(dir, "eirl-encoder-0.1.1-jetson-nano.img.xz"), []byte("compressed image"))
	if err := imageManifest([]string{"-image", img, "-software", sm, "-commit", "0123456789ab", "-raw-size", "4096",
		"-raw-sha256", sum([]byte("raw"))}, nil); err != nil {
		t.Fatal(err)
	}
	man := strings.TrimSuffix(img, ".xz") + ".json"

	// sign-image: the seed must match -pubkey; nothing is written otherwise
	if err := signImage([]string{"-in", man, "-pubkey", k.pub}, other.stdin()); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("wrong seed: %v", err)
	}
	if err := signImage([]string{"-in", man, "-pubkey", k.pub}, strings.NewReader("not a seed\n")); err == nil {
		t.Error("garbage seed accepted")
	}
	if _, err := os.Stat(man + ".sig"); err == nil {
		t.Fatal("a signature was written by a refused signing")
	}
	if err := signImage([]string{"-in", man, "-pubkey", k.pub}, k.stdin()); err != nil {
		t.Fatal(err)
	}

	// The wrong product, both ways, refused before the seed is read
	if err := signImage([]string{"-in", sm, "-out", filepath.Join(dir, "x.sig"), "-pubkey", k.pub}, noSeed{t}); err == nil ||
		!strings.Contains(err.Error(), "OTA manifest") {
		t.Errorf("sign-image of an OTA manifest: %v", err)
	}
	if err := sign([]string{"-in", man, "-out", filepath.Join(dir, "y.sig"), "-pubkey", k.pub}, noSeed{t}); err == nil ||
		!strings.Contains(err.Error(), "image manifest") {
		t.Errorf("sign of an image manifest: %v", err)
	}
	for _, p := range []string{"x.sig", "y.sig"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("%s written", p)
		}
	}
	// Not canonical (edited by hand), or not a manifest at all: refused before the seed is read
	b, _ := os.ReadFile(man)
	edited := write(t, filepath.Join(dir, "edited.img.json"), bytes.Replace(b, []byte(`"v": 1,`), []byte(`"v":1,`), 1))
	junk := write(t, filepath.Join(dir, "junk.img.json"), []byte("{}"))
	for _, p := range []string{edited, junk} {
		if err := signImage([]string{"-in", p, "-pubkey", k.pub}, noSeed{t}); err == nil {
			t.Errorf("%s signed", p)
		}
	}

	verify := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := verifyImage(append([]string{"-pubkey", k.pub}, args...), nil, &out) // a later -pubkey wins
		return out.String(), err
	}
	out, err := verify("-manifest", man, "-image", img)
	if err != nil {
		t.Fatalf("verify-image: %v", err)
	}
	if !strings.Contains(out, "image OK: eirl-encoder-0.1.1-jetson-nano.img.xz (16 bytes") || !strings.Contains(out, "not checked: the raw image") {
		t.Errorf("output:\n%s", out)
	}
	if out, err := verify("-manifest", man); err != nil || !strings.Contains(out, "not checked: the image (-image FILE.img.xz) and the raw image") {
		t.Errorf("manifest only: %v\n%s", err, out)
	}
	if out, err := verify("-manifest", man, "-print", "software.version"); err != nil || out != "0.1.1\n" {
		t.Errorf("-print software.version: %q %v", out, err)
	}
	if out, err := verify("-manifest", man, "-print", "image.rawSize"); err != nil || out != "4096\n" {
		t.Errorf("-print image.rawSize: %q %v", out, err)
	}

	// Exit 1: the download does not match. Exit 2: it could not be checked.
	tb := bytes.Clone(b)
	tb[len(tb)/3] ^= 1
	tampered := write(t, filepath.Join(dir, "t", filepath.Base(man)), tb)
	write(t, tampered+".sig", mustRead(t, man+".sig"))
	longer := write(t, filepath.Join(dir, "l", filepath.Base(img)), []byte("compressed image!"))
	changed := write(t, filepath.Join(dir, "c", filepath.Base(img)), []byte("compressed imagE"))
	smSig := sm + ".sig"
	for _, c := range []struct {
		name string
		args []string
		exit int
		msg  string
	}{
		{"tampered manifest", []string{"-manifest", tampered}, 1, "signature"},
		{"another key", []string{"-manifest", man, "-pubkey", other.pub}, 1, "signature"},
		{"bigger image", []string{"-manifest", man, "-image", longer}, 1, "bytes"},
		{"changed image", []string{"-manifest", man, "-image", changed}, 1, "sha256"},
		{"an OTA manifest", []string{"-manifest", sm, "-sig", smSig}, 1, "OTA manifest"},
		{"no image file", []string{"-manifest", man, "-image", filepath.Join(dir, "missing.img.xz")}, 2, "no such file"},
		{"no signature", []string{"-manifest", sm + ".nothing"}, 2, "no such file"},
		{"no -manifest", nil, 2, "-manifest"},
		{"bad -print", []string{"-manifest", man, "-print", "image.nothing"}, 2, "no such field"},
		{"bad flag", []string{"-manifest", man, "-frobnicate"}, 2, "frobnicate"},
	} {
		_, err := verify(c.args...)
		if got := verifyImageExit(err); got != c.exit || err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: exit %d (%v), want %d with %q", c.name, got, err, c.exit, c.msg)
		}
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPEMOfTheProductionKey(t *testing.T) {
	var out bytes.Buffer
	if err := pemKey(nil, &out); err != nil {
		t.Fatal(err)
	}
	if want := mustRead(t, "../../../release/eirl-release.pem"); !bytes.Equal(out.Bytes(), want) {
		t.Errorf("eirl-release pem:\n%s\nrelease/eirl-release.pem:\n%s", out.Bytes(), want)
	}
	pub := strings.TrimSpace(string(mustRead(t, "../../../release/eirl-release.pub")))
	out.Reset()
	if err := pemKey([]string{"-pubkey", pub}, &out); err != nil || out.String() != string(mustRead(t, "../../../release/eirl-release.pem")) {
		t.Errorf("pem of release/eirl-release.pub: %v", err)
	}
}

func TestFieldOfTheOTAManifest(t *testing.T) {
	m := &update.Manifest{V: 1, Product: update.Product, Version: "0.1.1", Bundle: update.Bundle{Name: "b", Size: 1 << 40}}
	for path, want := range map[string]string{"version": "0.1.1", "bundle.size": "1099511627776", "product": "eirl-encoder"} {
		if got, err := field(m, path); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", path, got, err, want)
		}
	}
	for _, path := range []string{"bundle", "nothing", "version.x"} {
		if _, err := field(m, path); err == nil {
			t.Errorf("%s accepted", path)
		}
	}
}
