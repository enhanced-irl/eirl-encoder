package device

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// The setup code (SETUP.md §2.1): 8 characters of CodeAlphabet in /etc/eirl-encoder/setup-code
// (0640), generated at the first start. The touch-screen wizard shows it (later, the product label
// too); the web needs it to set the first admin password. It is the pending "rescue credential" of
// MANAGER.md §6, but tonight only the first-password use exists.

const (
	// CodeAlphabet: no 0/O, 1/I/L: it is read off a screen or a label
	CodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	CodeLen      = 8
)

// ErrNoCode: the setup code file is missing or invalid.
var ErrNoCode = errors.New("setup code unavailable")

// NewCode returns a random code (crypto/rand, uniform over CodeAlphabet).
func NewCode() (string, error) {
	b := make([]byte, CodeLen)
	max := big.NewInt(int64(len(CodeAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = CodeAlphabet[n.Int64()]
	}
	return string(b), nil
}

// validCode: exactly CodeLen characters of CodeAlphabet.
func validCode(c string) bool {
	if len(c) != CodeLen {
		return false
	}
	for i := 0; i < len(c); i++ {
		if !strings.ContainsRune(CodeAlphabet, rune(c[i])) {
			return false
		}
	}
	return true
}

// EnsureCode creates the code file if it is missing. An existing file that does not hold a valid code
// is an error, not replaced: the code may be printed on a label.
func EnsureCode(path string) (created bool, err error) {
	if _, err := ReadCode(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	code, err := NewCode()
	if err != nil {
		return false, err
	}
	return true, config.WriteAtomic(path, []byte(code+"\n"), 0o640)
}

// ReadCode reads the code (os.ErrNotExist if there is none).
func ReadCode(path string) (string, error) {
	b, err := safefile.ReadOwned(path, 1<<10)
	if err != nil {
		return "", err
	}
	c := strings.TrimSpace(string(b))
	if !validCode(c) {
		return "", fmt.Errorf("%w: %s holds no valid code", ErrNoCode, path)
	}
	return c, nil
}

// NormalizeCode: what a person types (lowercase, spaces, a dash in the middle as on a label) → the code.
func NormalizeCode(in string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		if r >= 'a' && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}, in)
}

// CheckCode compares a typed code with the file in constant time. err != nil: no code to compare with.
func CheckCode(path, given string) (bool, error) {
	code, err := ReadCode(path)
	if err != nil {
		return false, err
	}
	g := NormalizeCode(given)
	return len(g) == len(code) && subtle.ConstantTimeCompare([]byte(g), []byte(code)) == 1, nil
}
