package appapi

import (
	"encoding/json"
	"fmt"
	"io"
)

const (
	masked    = "***"
	hexDigits = "0123456789abcdef"
)

// secret holds the typed wifi password of a queued connect (WIFI.md §6): its own copy, wiped (zeroed) when it is
// sent or dropped. It never prints: %v, %s, %#v and text marshalling give ***. Go cannot promise there is no other
// copy (the transport's write buffer has one until it is collected), but nothing here keeps or writes one.
type secret struct{ b []byte }

// newSecret copies p (nil or empty: no secret).
func newSecret(p []byte) secret {
	if len(p) == 0 {
		return secret{}
	}
	return secret{b: append([]byte(nil), p...)}
}

func (s secret) empty() bool { return len(s.b) == 0 }

// wipe zeroes the bytes (the copy is shared by value: wiping any copy wipes them all).
func (s *secret) wipe() {
	clear(s.b)
	s.b = nil
}

func (s secret) String() string               { return masked }
func (s secret) GoString() string             { return masked }
func (s secret) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, masked) }
func (s secret) MarshalText() ([]byte, error) { return []byte(masked), nil }
func (s secret) MarshalJSON() ([]byte, error) { return []byte(`"` + masked + `"`), nil }

// connectBody is {"ssid": …, "password": …} (without the password if there is none), built without turning the
// password into a string (which could not be wiped). The caller's call() wipes the result.
func connectBody(ssid string, s secret) []byte {
	ss, _ := json.Marshal(ssid)
	b := make([]byte, 0, len(ss)+2*len(s.b)+32)
	b = append(b, `{"ssid":`...)
	b = append(b, ss...)
	if !s.empty() {
		b = append(b, `,"password":"`...)
		for _, c := range s.b {
			switch {
			case c == '"' || c == '\\':
				b = append(b, '\\', c)
			case c < 0x20:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&15])
			default:
				b = append(b, c) // printable ASCII from the keyboard (UTF-8 passes as is)
			}
		}
		b = append(b, '"')
	}
	return append(b, '}')
}
