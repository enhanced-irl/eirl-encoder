package ui

import "testing"

// The encoder's page on EnhancedIRL's site: English without a prefix, the UTM tags in a fixed order, and a QR for
// each one the screen shows.
func TestEncoderPageLinks(t *testing.T) {
	for _, c := range []struct{ lang, medium, url, shown string }{
		{"en", "destinations", "https://enhancedirl.com/encoder?utm_source=encoder&utm_medium=destinations&utm_campaign=eirl-encoder",
			"enhancedirl.com/encoder"},
		{"es", "wizard", "https://enhancedirl.com/es/encoder?utm_source=encoder&utm_medium=wizard&utm_campaign=eirl-encoder",
			"enhancedirl.com/es/encoder"},
		{"fr", "destinations", "https://enhancedirl.com/fr/encoder?utm_source=encoder&utm_medium=destinations&utm_campaign=eirl-encoder",
			"enhancedirl.com/fr/encoder"},
		{"de", "wizard", "https://enhancedirl.com/encoder?utm_source=encoder&utm_medium=wizard&utm_campaign=eirl-encoder",
			"enhancedirl.com/encoder"},
	} {
		if got := EncoderPageURL(c.lang, c.medium); got != c.url {
			t.Errorf("%s %s: %s", c.lang, c.medium, got)
		}
		if got := EncoderPageShown(c.lang); got != c.shown {
			t.Errorf("%s shown: %s", c.lang, got)
		}
		rows := linkQR(c.url)
		if len(rows) < 21 || len(rows[0]) != len(rows) {
			t.Errorf("%s: QR %d rows", c.lang, len(rows))
		}
		if again := linkQR(c.url); &again[0] != &rows[0] {
			t.Errorf("%s: the QR is made again", c.lang)
		}
	}
}
