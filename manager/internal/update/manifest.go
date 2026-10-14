// Package update is the over-the-air update of the encoder software (UPDATES.md): the signed release
// manifest, its verification, the download of the bundle and the app's update state machine. The
// root helper that installs a bundle (cmd/eirl-update) re-verifies everything with the same code.
package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Product is the manifest's "product": a manifest of anything else is never installed.
const Product = "eirl-encoder"

// Limits of what is read from the network or from the app's directory.
const (
	MaxManifestSize = 64 << 10
	MaxSigSize      = 1 << 10
	MaxBundleSize   = 512 << 20
	maxNoteLen      = 8 << 10
)

// ProductionKey is the release public key (ed25519, base64 of the 32 raw bytes). Its private seed lives
// only in the maintainers' secret store (UPDATES.md §3); it is also in the repo as release/eirl-release.pub.
const ProductionKey = "u/seIYCVoGlGJzGtsQ2jaOV7ryiTLUAy/+YGH1FHaRo="

// trustedKey is the key compiled into this build. A test build overrides it at link time
// (-X github.com/enhanced-irl/eirl-encoder/manager/internal/update.trustedKey=<base64>, tools/release.sh
// --key-stdin --pubkey): such a build trusts only the test key, never both.
var trustedKey = ProductionKey

// TrustedKeys returns the public key(s) this build verifies releases with.
func TrustedKeys() ([]ed25519.PublicKey, error) {
	k, err := ParsePublicKey(trustedKey)
	if err != nil {
		return nil, fmt.Errorf("compiled-in release key: %w", err)
	}
	return []ed25519.PublicKey{k}, nil
}

// TestBuild reports whether this build trusts a key other than the production one.
func TestBuild() bool { return trustedKey != ProductionKey }

// KeyID is a short, public fingerprint of a key (for logs and the API): hex of the first 8 bytes.
func KeyID(k ed25519.PublicKey) string {
	if len(k) < 8 {
		return ""
	}
	return hex.EncodeToString(k[:8])
}

// ParsePublicKey decodes a base64 ed25519 public key (32 bytes).
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key: %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// Manifest is manifest.json of a release (UPDATES.md §5). Unknown fields are ignored: a newer
// release may add some and older devices must still read it.
type Manifest struct {
	V        int               `json:"v"`
	Product  string            `json:"product"`
	Version  string            `json:"version"`
	Released time.Time         `json:"released"`
	MinFrom  string            `json:"minFrom,omitempty"`
	Notes    map[string]string `json:"notes,omitempty"`
	Bundle   Bundle            `json:"bundle"`
}

// Bundle is the release archive the manifest points to.
type Bundle struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BundleName is the only accepted name of the bundle of a version.
func BundleName(version string) string { return "eirl-encoder-" + version + "-linux-arm64.tar.gz" }

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	langCode  = regexp.MustCompile(`^[a-z]{2}$`)
)

// Error carries a stable code for the API and the helper's status (the message is for logs).
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func errf(code, format string, a ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, a...)}
}

// Code returns the stable code of err ("" for nil, "internal" when it has none).
func Code(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}

// Error codes (stable: the API returns them and the UIs translate them).
const (
	CodeBadSignature = "bad_signature" // the manifest is not signed by the trusted key
	CodeBadManifest  = "bad_manifest"  // signed, but not a valid manifest
	CodeChecksum     = "checksum"      // the bundle does not match the manifest (size or sha256)
	CodeNotNewer     = "not_newer"     // the release is not newer than what is installed (downgrade/replay)
	CodeMinFrom      = "min_from"      // the installed version is too old to update directly
	CodeNetwork      = "network"       // could not fetch the manifest or the bundle
	CodeStreaming    = "streaming"     // the encoder is streaming: never installed then
)

// VerifyManifest checks the detached signature (base64 ed25519 over the exact bytes of raw) with any of
// keys, then parses and validates the manifest. Nothing in raw is looked at before the signature holds.
func VerifyManifest(raw, sig []byte, keys []ed25519.PublicKey) (*Manifest, error) {
	if err := checkSignature(raw, sig, keys); err != nil {
		return nil, err
	}
	return ParseManifest(raw)
}

// checkSignature checks a detached signature (base64 ed25519 over the exact bytes of raw) with any of keys.
// It is the same for the OTA manifest and the image manifest (image.go): their "product" tells them apart.
func checkSignature(raw, sig []byte, keys []ed25519.PublicKey) error {
	if len(raw) == 0 || len(raw) > MaxManifestSize {
		return errf(CodeBadManifest, "manifest size %d", len(raw))
	}
	if len(sig) > MaxSigSize {
		return errf(CodeBadSignature, "signature file too large")
	}
	s, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil || len(s) != ed25519.SignatureSize {
		return errf(CodeBadSignature, "signature is not base64 of %d bytes", ed25519.SignatureSize)
	}
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, raw, s) {
			return nil
		}
	}
	return errf(CodeBadSignature, "signature does not verify with the trusted key")
}

// ParseManifest parses and validates a manifest WITHOUT checking its signature: only for the release tool,
// which checks what it is about to sign. Everything that installs a release calls VerifyManifest.
func ParseManifest(raw []byte) (*Manifest, error) {
	if len(raw) == 0 || len(raw) > MaxManifestSize {
		return nil, errf(CodeBadManifest, "manifest size %d", len(raw))
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errf(CodeBadManifest, "manifest JSON: %v", err)
	}
	if err := m.validate(); err != nil {
		return nil, errf(CodeBadManifest, "%v", err)
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.V != 1 {
		return fmt.Errorf("manifest v=%d, want 1", m.V)
	}
	if m.Product != Product {
		return fmt.Errorf("product %q", m.Product)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return err
	}
	if m.MinFrom != "" {
		if _, err := ParseVersion(m.MinFrom); err != nil {
			return fmt.Errorf("minFrom: %w", err)
		}
	}
	if m.Bundle.Name != BundleName(m.Version) {
		return fmt.Errorf("bundle name %q, want %q", m.Bundle.Name, BundleName(m.Version))
	}
	if m.Bundle.Size <= 0 || m.Bundle.Size > MaxBundleSize {
		return fmt.Errorf("bundle size %d", m.Bundle.Size)
	}
	if !sha256Hex.MatchString(m.Bundle.SHA256) {
		return fmt.Errorf("bundle sha256 %q", m.Bundle.SHA256)
	}
	for k, v := range m.Notes {
		if !langCode.MatchString(k) || len(v) > maxNoteLen {
			return fmt.Errorf("notes[%q]", k)
		}
	}
	return nil
}

// CheckUpgrade says whether m may be installed over the installed version: strictly newer (no downgrade,
// no reinstall of the same version: a replayed old manifest is useless) and installed >= minFrom.
// An unparsable installed version counts as 0.0.0 (a pre-OTA install).
func CheckUpgrade(m *Manifest, installed string) error {
	nv, err := ParseVersion(m.Version)
	if err != nil {
		return errf(CodeBadManifest, "%v", err)
	}
	cur, err := ParseVersion(installed)
	if err != nil {
		cur = Version{}
	}
	if nv.Compare(cur) <= 0 {
		return errf(CodeNotNewer, "release %s is not newer than installed %s", nv, cur)
	}
	if m.MinFrom != "" {
		min, _ := ParseVersion(m.MinFrom)
		if cur.Less(min) {
			return errf(CodeMinFrom, "installed %s is older than %s, the oldest that can update to %s", cur, min, nv)
		}
	}
	return nil
}

// Note picks the release notes for lang: lang, then English, then any (sorted for stability).
func (m *Manifest) Note(lang string) string {
	if n, ok := m.Notes[lang]; ok {
		return n
	}
	if n, ok := m.Notes["en"]; ok {
		return n
	}
	best := ""
	for k := range m.Notes {
		if best == "" || k < best {
			best = k
		}
	}
	return m.Notes[best]
}
