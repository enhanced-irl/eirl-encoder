package config

import (
	"fmt"
	"io"
)

// masked is what every formatting of a Secret prints.
const masked = "***"

// Secret holds a value that must never reach a log, an error or an API response: the SRT stream id,
// the passphrase and the SRT target that carries both. Every fmt verb, String, GoString, JSON and
// text marshalling print "***"; only Reveal gives the value, for the one place that needs it (the
// environment of the output process).
//
// The value sits behind a pointer on purpose: when fmt cannot use the methods (a bad verb on a
// struct that contains a Config, e.g. %s), it falls back to reflection, and a nested pointer to a
// string prints as an address, never as the string.
type Secret struct{ v *string }

// NewSecret wraps a value.
func NewSecret(s string) Secret {
	if s == "" {
		return Secret{}
	}
	return Secret{v: &s}
}

// Reveal returns the secret in clear. Never pass the result to a logger.
func (s Secret) Reveal() string {
	if s.v == nil {
		return ""
	}
	return *s.v
}

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s.Reveal() == "" }

func (s Secret) String() string   { return masked }
func (s Secret) GoString() string { return masked }

// Format implements fmt.Formatter so that no verb (%x, %q, %d…) prints the value.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, masked) }

// MarshalText makes encoding/json and friends print the mask too.
func (s Secret) MarshalText() ([]byte, error) { return []byte(masked), nil }
