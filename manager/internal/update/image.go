package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The SD card image's signed manifest (IMAGE.md §8.1): a small JSON next to the image, signed with the same
// release key as the OTA manifest. Its own product keeps the two apart: the board's verifier (VerifyManifest)
// requires Product, so an image manifest can never pass for an OTA release, and VerifyImageManifest requires
// ImageProduct, so an OTA manifest can never pass for an image. Only the release tool (cmd/eirl-release) uses
// this file; the board never reads an image manifest.

// ImageProduct is the "product" of an image manifest.
const ImageProduct = "eirl-encoder-image"

// DefaultImageBoard is the board of the image (IMAGE.md §1).
const DefaultImageBoard = "jetson-nano-devkit-b01"

// DefaultMinCardBytes is the smallest card an image is published for (IMAGE.md §12, D-I5: 16 GB).
const DefaultMinCardBytes = 16_000_000_000

const (
	sectorSize   = 512
	maxCardBytes = 1 << 41 // 2 TiB, the largest SDXC card
)

// imageBoard is a board an image may be made for: its L4T release (a new L4T is a new image, IMAGE.md §1) and
// the tag in the image's file name.
type imageBoard struct {
	l4t, fileTag string
}

var imageBoards = map[string]imageBoard{
	"jetson-nano-devkit-b01": {l4t: "32.7.6", fileTag: "jetson-nano"},
}

// ImageBoardL4T is the L4T release of a known board ("" for an unknown one).
func ImageBoardL4T(board string) string { return imageBoards[board].l4t }

// ImageName is the only accepted file name of the compressed image of a version for a board ("" for an
// unknown board).
func ImageName(version, board string) string {
	b, ok := imageBoards[board]
	if !ok {
		return ""
	}
	return "eirl-encoder-" + version + "-" + b.fileTag + ".img.xz"
}

// ImageManifest is the image's manifest. The field order is the document's (IMAGE.md §8.1): json.MarshalIndent
// of this struct is the canonical form, the only one sign-image signs.
type ImageManifest struct {
	V            int           `json:"v"`
	Product      string        `json:"product"`
	Board        string        `json:"board"`
	L4T          string        `json:"l4t"`
	Version      string        `json:"version"`
	Software     ImageSoftware `json:"software"`
	Built        time.Time     `json:"built"`
	Commit       string        `json:"commit"`
	Image        ImageFile     `json:"image"`
	MinCardBytes int64         `json:"minCardBytes"`
}

// ImageSoftware is the release installed in the image: its version and the sha256 of its OTA manifest.json.
type ImageSoftware struct {
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifestSha256"`
}

// ImageFile is the compressed image (what is downloaded) and the raw image (what is written to the card).
type ImageFile struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	RawSize   int64  `json:"rawSize"`
	RawSHA256 string `json:"rawSha256"`
}

var (
	commitHex  = regexp.MustCompile(`^[0-9a-f]{12}$`)
	rebuildNum = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
)

// CheckImageVersion says whether version may be the version of an image that carries the release software:
// the same version, or "<software>-N" for the N-th rebuild of an image of that release (IMAGE.md §8.1).
func CheckImageVersion(version, software string) error {
	sv, err := ParseVersion(software)
	if err != nil {
		return fmt.Errorf("software version: %w", err)
	}
	if sv.Build != "" {
		return fmt.Errorf("software version %q: a release has no build metadata", software)
	}
	if _, err := ParseVersion(version); err != nil {
		return err
	}
	if version == software {
		return nil
	}
	if n, ok := strings.CutPrefix(version, software+"-"); ok && rebuildNum.MatchString(n) {
		return nil
	}
	return fmt.Errorf("version %q: must be the software's (%s), or %s-N for a rebuild", version, software, software)
}

func (m *ImageManifest) validate() error {
	if m.V != 1 {
		return fmt.Errorf("image manifest v=%d, want 1", m.V)
	}
	if m.Product != ImageProduct {
		return fmt.Errorf("product %q, want %q", m.Product, ImageProduct)
	}
	b, ok := imageBoards[m.Board]
	if !ok {
		return fmt.Errorf("unknown board %q", m.Board)
	}
	if m.L4T != b.l4t {
		return fmt.Errorf("l4t %q, want %q for %s", m.L4T, b.l4t, m.Board)
	}
	if err := CheckImageVersion(m.Version, m.Software.Version); err != nil {
		return err
	}
	if !sha256Hex.MatchString(m.Software.ManifestSHA256) {
		return fmt.Errorf("software.manifestSha256 %q", m.Software.ManifestSHA256)
	}
	if m.Built.IsZero() {
		return fmt.Errorf("built is missing")
	}
	if !commitHex.MatchString(m.Commit) {
		return fmt.Errorf("commit %q: want 12 lowercase hex characters", m.Commit)
	}
	if want := ImageName(m.Version, m.Board); m.Image.Name != want {
		return fmt.Errorf("image name %q, want %q", m.Image.Name, want)
	}
	if m.Image.Size <= 0 {
		return fmt.Errorf("image size %d", m.Image.Size)
	}
	if !sha256Hex.MatchString(m.Image.SHA256) {
		return fmt.Errorf("image sha256 %q", m.Image.SHA256)
	}
	if m.Image.RawSize <= 0 || m.Image.RawSize%sectorSize != 0 {
		return fmt.Errorf("image rawSize %d: want a positive multiple of %d (a disk image)", m.Image.RawSize, sectorSize)
	}
	if !sha256Hex.MatchString(m.Image.RawSHA256) {
		return fmt.Errorf("image rawSha256 %q", m.Image.RawSHA256)
	}
	if m.MinCardBytes <= 0 || m.MinCardBytes > maxCardBytes {
		return fmt.Errorf("minCardBytes %d", m.MinCardBytes)
	}
	if m.Image.RawSize > m.MinCardBytes {
		return fmt.Errorf("the raw image (%d bytes) does not fit the smallest card (minCardBytes %d)", m.Image.RawSize, m.MinCardBytes)
	}
	return nil
}

// ParseImageManifest parses and validates an image manifest WITHOUT checking its signature (the release tool,
// before signing). Unknown fields are ignored, as in the OTA manifest; CanonicalImageManifest tells whether raw
// is exactly what the tool writes.
func ParseImageManifest(raw []byte) (*ImageManifest, error) {
	if len(raw) == 0 || len(raw) > MaxManifestSize {
		return nil, errf(CodeBadManifest, "image manifest size %d", len(raw))
	}
	var m ImageManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errf(CodeBadManifest, "image manifest JSON: %v", err)
	}
	if err := m.validate(); err != nil {
		return nil, errf(CodeBadManifest, "%v", err)
	}
	return &m, nil
}

// VerifyImageManifest checks the detached signature with any of keys, then parses and validates the image
// manifest. Nothing in raw is looked at before the signature holds.
func VerifyImageManifest(raw, sig []byte, keys []ed25519.PublicKey) (*ImageManifest, error) {
	if err := checkSignature(raw, sig, keys); err != nil {
		return nil, err
	}
	return ParseImageManifest(raw)
}

// Marshal is the canonical form of the manifest: indented JSON in the document's field order, with a final
// newline. It is what eirl-release image-manifest writes and the only form sign-image signs.
func (m *ImageManifest) Marshal() ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, errf(CodeBadManifest, "%v", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// CanonicalImageManifest reports whether raw is a valid image manifest in its canonical form (no unknown or
// duplicated fields, no other spacing or order): what sign-image requires before it signs.
func CanonicalImageManifest(raw []byte) error {
	m, err := ParseImageManifest(raw)
	if err != nil {
		return err
	}
	c, err := m.Marshal()
	if err != nil {
		return err
	}
	if !bytes.Equal(c, raw) {
		return errf(CodeBadManifest, "not in the canonical form eirl-release image-manifest writes (edited by hand?)")
	}
	return nil
}

// PublicKeyPEM is the PEM (SubjectPublicKeyInfo, "PUBLIC KEY") form of an ed25519 public key, for OpenSSL:
// release/eirl-release.pem is PublicKeyPEM(ProductionKey).
func PublicKeyPEM(k ed25519.PublicKey) ([]byte, error) {
	if len(k) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key: %d bytes, want %d", len(k), ed25519.PublicKeySize)
	}
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}
