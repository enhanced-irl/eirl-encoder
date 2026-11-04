package ui

import (
	"net/url"
	"strings"
	"sync"

	"rsc.io/qr"
)

// Links to EnhancedIRL's site and documentation (OUTPUTS.md §11): the only place the screen writes their addresses
// (the web's are EIRL_LINKS in app.js). Nothing is fetched and nothing is counted on the device: they are QR codes
// and text for the user to open on a phone, if they want.
const (
	// SiteURL is EnhancedIRL's site; it serves English without a language prefix (/es/…, /fr/… for the others).
	SiteURL = "https://enhancedirl.com"
	// DocsHost is the documentation's host, written for people (the About page).
	DocsHost = "docs.enhancedirl.com"
	// encoderPath is the encoder's page on the site.
	encoderPath = "/encoder"
	// utmSource and utmCampaign tag the links (utm_medium says which screen: destinations, wizard).
	utmSource   = "encoder"
	utmCampaign = "eirl-encoder"
)

// encoderPagePath is the encoder's page in a language: English without a prefix.
func encoderPagePath(lang string) string {
	if lang == "es" || lang == "fr" {
		return "/" + lang + encoderPath
	}
	return encoderPath
}

// EncoderPageURL is the encoder's page on the site in a language, tagged with where it was shown (medium).
func EncoderPageURL(lang, medium string) string {
	return SiteURL + encoderPagePath(lang) + "?utm_source=" + utmSource + "&utm_medium=" + url.QueryEscape(medium) +
		"&utm_campaign=" + utmCampaign
}

// EncoderPageShown is the encoder's page as it is written for people: no scheme and no tags.
func EncoderPageShown(lang string) string {
	return strings.TrimPrefix(SiteURL, "https://") + encoderPagePath(lang)
}

// linkQRs caches the QR rows of each link (a handful: one per language and screen), made once.
var linkQRs sync.Map

// linkQR is a link's QR in rows of 0 and 1 (correction M, like the app's), nil if it cannot be made.
func linkQR(text string) []string {
	if v, ok := linkQRs.Load(text); ok {
		return v.([]string)
	}
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil
	}
	rows := make([]string, c.Size)
	b := make([]byte, c.Size)
	for y := range rows {
		for x := range b {
			b[x] = '0'
			if c.Black(x, y) {
				b[x] = '1'
			}
		}
		rows[y] = string(b)
	}
	linkQRs.Store(text, rows)
	return rows
}
