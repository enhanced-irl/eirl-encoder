package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"
)

func hexOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func imageManifestFor(version, software string) ImageManifest {
	return ImageManifest{V: 1, Product: ImageProduct, Board: DefaultImageBoard, L4T: "32.7.6", Version: version,
		Software: ImageSoftware{Version: software, ManifestSHA256: hexOf("manifest")},
		Built:    time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), Commit: "0123456789ab",
		Image: ImageFile{Name: ImageName(version, DefaultImageBoard), Size: 1 << 30, SHA256: hexOf("xz"),
			RawSize: 4 << 30, RawSHA256: hexOf("raw")},
		MinCardBytes: DefaultMinCardBytes}
}

func mustMarshal(t *testing.T, m ImageManifest) []byte {
	t.Helper()
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// keysOf lists the keys of the JSON object at the start of b, in order.
func keysOf(t *testing.T, b []byte) []string {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %v", err)
	}
	var keys []string
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := d.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestImageManifestFormat(t *testing.T) {
	b := mustMarshal(t, imageManifestFor("0.1.1", "0.1.1"))
	if got, want := strings.Join(keysOf(t, b), ","), "v,product,board,l4t,version,software,built,commit,image,minCardBytes"; got != want {
		t.Errorf("field order %s, want %s (IMAGE.md §8.1)", got, want)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(b, &m)
	if got := strings.Join(keysOf(t, m["software"]), ","); got != "version,manifestSha256" {
		t.Errorf("software fields %s", got)
	}
	if got := strings.Join(keysOf(t, m["image"]), ","); got != "name,size,sha256,rawSize,rawSha256" {
		t.Errorf("image fields %s", got)
	}
	for _, want := range []string{`"product": "eirl-encoder-image"`, `"board": "jetson-nano-devkit-b01"`, `"l4t": "32.7.6"`,
		`"built": "2026-10-20T10:00:00Z"`, `"name": "eirl-encoder-0.1.1-jetson-nano.img.xz"`, `"minCardBytes": 16000000000`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("missing %s in\n%s", want, b)
		}
	}
	if !bytes.HasSuffix(b, []byte("}\n")) {
		t.Error("no final newline")
	}
	if err := CanonicalImageManifest(b); err != nil {
		t.Errorf("canonical: %v", err)
	}
	// Anything else is not what sign-image signs
	for name, r := range map[string][]byte{
		"compact":       func() []byte { c, _ := json.Marshal(imageManifestFor("0.1.1", "0.1.1")); return c }(),
		"no newline":    bytes.TrimSuffix(b, []byte("\n")),
		"unknown field": bytes.Replace(b, []byte(`"v": 1,`), []byte(`"v": 1, "extra": 1,`), 1),
		"duplicate":     bytes.Replace(b, []byte(`"v": 1,`), []byte(`"v": 1, "product": "eirl-encoder-image",`), 1),
	} {
		if err := CanonicalImageManifest(r); Code(err) != CodeBadManifest {
			t.Errorf("%s: %v, want bad_manifest", name, err)
		}
	}
}

func TestImageVersion(t *testing.T) {
	for _, c := range []struct {
		version, software string
		ok                bool
	}{
		{"0.1.1", "0.1.1", true},
		{"0.1.1-1", "0.1.1", true},
		{"0.1.1-12", "0.1.1", true},
		{"0.1.1-test.1", "0.1.1-test.1", true},
		{"0.1.1-test.1-2", "0.1.1-test.1", true},
		{"0.1.2", "0.1.1", false},
		{"0.1.1-0", "0.1.1", false},
		{"0.1.1-01", "0.1.1", false},
		{"0.1.1-r2", "0.1.1", false},
		{"0.1.1-12345", "0.1.1", false},
		{"0.1.1-debug", "0.1.1", false},
		{"v0.1.1", "0.1.1", false},
		{"0.1.1", "0.1", false},
		{"0.0.0-dev+abc", "0.0.0-dev+abc", false},
	} {
		if err := CheckImageVersion(c.version, c.software); (err == nil) != c.ok {
			t.Errorf("%s of %s: %v, want ok=%v", c.version, c.software, err, c.ok)
		}
	}
	if ImageName("0.1.1-2", DefaultImageBoard) != "eirl-encoder-0.1.1-2-jetson-nano.img.xz" || ImageName("0.1.1", "other") != "" {
		t.Error("ImageName")
	}
}

func TestVerifyImageManifest(t *testing.T) {
	k, other := newKey(t), newKey(t)
	keys := []ed25519.PublicKey{k.pub}
	raw := mustMarshal(t, imageManifestFor("0.1.1-2", "0.1.1"))
	m, err := VerifyImageManifest(raw, k.sign(raw), keys)
	if err != nil {
		t.Fatalf("good manifest: %v", err)
	}
	if m.Version != "0.1.1-2" || m.Software.Version != "0.1.1" || m.Image.RawSize != 4<<30 || m.Commit != "0123456789ab" {
		t.Errorf("parsed %+v", m)
	}
	if _, err := VerifyImageManifest(raw, other.sign(raw), keys); Code(err) != CodeBadSignature {
		t.Errorf("wrong key: %v", err)
	}
	// Any byte changed, anywhere in the manifest: refused
	sig := k.sign(raw)
	for i := range raw {
		for _, x := range []byte{0x01, 0x20, 0x80} {
			r := bytes.Clone(raw)
			r[i] ^= x
			if _, err := VerifyImageManifest(r, sig, keys); err == nil {
				t.Fatalf("byte %d ^ %#x accepted", i, x)
			}
		}
	}
	// Any bit of the signature changed: refused
	s := ed25519.Sign(k.priv, raw)
	for i := range s {
		bad := bytes.Clone(s)
		bad[i] ^= 1
		if _, err := VerifyImageManifest(raw, []byte(b64(bad)), keys); Code(err) != CodeBadSignature {
			t.Fatalf("signature byte %d: %v", i, err)
		}
	}

	// Signed but invalid: the signature holds, the content does not
	bad := map[string]func(*ImageManifest){
		"v2":               func(m *ImageManifest) { m.V = 2 },
		"OTA product":      func(m *ImageManifest) { m.Product = Product },
		"other product":    func(m *ImageManifest) { m.Product = "other" },
		"board":            func(m *ImageManifest) { m.Board = "jetson-nano-devkit-a02" },
		"l4t":              func(m *ImageManifest) { m.L4T = "32.7.5" },
		"version":          func(m *ImageManifest) { m.Version = "0.1.2"; m.Image.Name = ImageName("0.1.2", m.Board) },
		"rebuild suffix":   func(m *ImageManifest) { m.Version = "0.1.1-x"; m.Image.Name = ImageName("0.1.1-x", m.Board) },
		"software":         func(m *ImageManifest) { m.Software.Version = "one" },
		"manifestSha256":   func(m *ImageManifest) { m.Software.ManifestSHA256 = m.Software.ManifestSHA256[:63] },
		"uppercase sha":    func(m *ImageManifest) { m.Image.SHA256 = strings.ToUpper(m.Image.SHA256) },
		"short sha":        func(m *ImageManifest) { m.Image.SHA256 = "abcd" },
		"raw sha":          func(m *ImageManifest) { m.Image.RawSHA256 = m.Image.RawSHA256 + "00" },
		"no built":         func(m *ImageManifest) { m.Built = time.Time{} },
		"short commit":     func(m *ImageManifest) { m.Commit = "0123456" },
		"long commit":      func(m *ImageManifest) { m.Commit = strings.Repeat("a", 40) },
		"commit not hex":   func(m *ImageManifest) { m.Commit = "0123456789ag" },
		"name":             func(m *ImageManifest) { m.Image.Name = "eirl-encoder-0.1.1-jetson-nano-debug.img.xz" },
		"name of another":  func(m *ImageManifest) { m.Image.Name = ImageName("0.1.1", m.Board) },
		"size":             func(m *ImageManifest) { m.Image.Size = 0 },
		"negative size":    func(m *ImageManifest) { m.Image.Size = -1 },
		"raw size":         func(m *ImageManifest) { m.Image.RawSize = 0 },
		"raw not sectors":  func(m *ImageManifest) { m.Image.RawSize = 4<<30 + 1 },
		"min card":         func(m *ImageManifest) { m.MinCardBytes = 0 },
		"does not fit":     func(m *ImageManifest) { m.MinCardBytes = 2 << 30 },
		"absurd card size": func(m *ImageManifest) { m.MinCardBytes = 1 << 50 },
	}
	for name, mut := range bad {
		mm := imageManifestFor("0.1.1-2", "0.1.1")
		mut(&mm)
		if _, err := mm.Marshal(); Code(err) != CodeBadManifest {
			t.Errorf("%s: Marshal accepted it (%v)", name, err)
		}
		r, err := json.MarshalIndent(mm, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyImageManifest(r, k.sign(r), keys); Code(err) != CodeBadManifest {
			t.Errorf("%s: code %q (%v), want bad_manifest", name, Code(err), err)
		}
	}
}

// The two manifests are signed with the same key: neither may ever pass for the other.
func TestImageAndOTAManifestsNeverMix(t *testing.T) {
	k := newKey(t)
	keys := []ed25519.PublicKey{k.pub}
	img := mustMarshal(t, imageManifestFor("0.1.1", "0.1.1"))
	if _, err := VerifyManifest(img, k.sign(img), keys); Code(err) != CodeBadManifest || !strings.Contains(err.Error(), "product") {
		t.Errorf("the board's verifier on a signed image manifest: %v, want bad_manifest (product)", err)
	}
	if _, err := ParseManifest(img); Code(err) != CodeBadManifest {
		t.Errorf("ParseManifest on an image manifest: %v", err)
	}
	ota := mustJSON(t, manifestFor("0.1.1", []byte("bundle")))
	if _, err := VerifyImageManifest(ota, k.sign(ota), keys); Code(err) != CodeBadManifest || !strings.Contains(err.Error(), "product") {
		t.Errorf("verify-image on a signed OTA manifest: %v, want bad_manifest (product)", err)
	}
	if _, err := ParseImageManifest(ota); Code(err) != CodeBadManifest {
		t.Errorf("ParseImageManifest on an OTA manifest: %v", err)
	}
	// A manifest with both products (JSON keeps the last one, and both parsers agree on it) is one of the two at most
	both := bytes.Replace(img, []byte(`"v": 1,`), []byte(`"v": 1, "product": "eirl-encoder",`), 1)
	_, e1 := VerifyManifest(both, k.sign(both), keys)
	_, e2 := VerifyImageManifest(both, k.sign(both), keys)
	if e1 == nil && e2 == nil {
		t.Error("a manifest passed as both")
	}
}

func TestProductionKeyPEM(t *testing.T) {
	b, err := os.ReadFile("../../../release/eirl-release.pem")
	if err != nil {
		t.Fatalf("release/eirl-release.pem: %v", err)
	}
	pk, _ := ParsePublicKey(ProductionKey)
	want, err := PublicKeyPEM(pk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, want) {
		t.Errorf("release/eirl-release.pem is not the PEM of release/eirl-release.pub: regenerate it with\n"+
			"  go -C manager run ./cmd/eirl-release pem -pubkey \"$(cat release/eirl-release.pub)\" > release/eirl-release.pem\nwant:\n%s", want)
	}
	// Read back with the standard library: an ed25519 SubjectPublicKeyInfo of the same 32 bytes
	blk, rest := pem.Decode(b)
	if blk == nil || blk.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("not a single PUBLIC KEY block")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if ek, ok := k.(ed25519.PublicKey); !ok || !ek.Equal(pk) {
		t.Errorf("the PEM holds %T %x", k, k)
	}
	if _, err := PublicKeyPEM(pk[:31]); err == nil {
		t.Error("a short key")
	}
}
