package i18n

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"testing"
)

var reVerb = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z%]`)

// Every key in the three languages, non-empty, with the same format verbs in the same order.
func TestCatalogComplete(t *testing.T) {
	for key, entry := range catalog {
		for _, lang := range Languages {
			if entry[lang] == "" {
				t.Errorf("%s: missing %s", key, lang)
			}
		}
		if len(entry) != len(Languages) {
			t.Errorf("%s: %d languages, want %d", key, len(entry), len(Languages))
		}
		want := reVerb.FindAllString(entry[ES], -1)
		for _, lang := range Languages {
			if got := reVerb.FindAllString(entry[lang], -1); !slices.Equal(got, want) {
				t.Errorf("%s: verbs in %s %v, in es %v", key, lang, got, want)
			}
		}
	}
}

func TestNegotiate(t *testing.T) {
	for _, c := range []struct{ header, device, want string }{
		{"", "es", "es"},
		{"", "", "en"},
		{"", "de", "en"},
		{"fr-FR,fr;q=0.9,en;q=0.8", "es", "fr"},
		{"de-DE,de;q=0.9,es;q=0.5", "en", "es"},
		{"de", "fr", "fr"},
		{"en;q=0.2, es;q=0.9", "fr", "es"},
		{"ES", "en", "es"},
		{"fr;q=0, en", "es", "en"},
		{"*", "es", "es"},
		{"pt-BR, en-US;q=0.7", "es", "en"},
	} {
		if got := Negotiate(c.header, c.device); got != c.want {
			t.Errorf("Negotiate(%q, %q) = %q, want %q", c.header, c.device, got, c.want)
		}
	}
}

func TestMsgAndRefs(t *testing.T) {
	if got := Msg(ES, "config.latency", 20, 8000); got != "entre 20 y 8000 ms" {
		t.Errorf("es: %q", got)
	}
	if got := Msg(FR, "config.latency", 20, 8000); got != "entre 20 et 8000 ms" {
		t.Errorf("fr: %q", got)
	}
	if got := Msg("de", "config.latency", 20, 8000); got != "between 20 and 8000 ms" {
		t.Errorf("unsupported language falls back to English: %q", got)
	}
	if got := Msg(EN, "no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key: %q", got)
	}
	// A Ref keeps its arguments (numbers too) until it is rendered in the request language
	ref := Ref("config.latency", 20, 8000)
	if got := Text(ES, ref); got != "entre 20 y 8000 ms" {
		t.Errorf("Text(es, ref) = %q", got)
	}
	if got := Text(EN, Ref("config.param_repeated", "foo")); got != `parameter "foo" repeated` {
		t.Errorf("string arg: %q", got)
	}
	if got := Text(EN, "plain text"); got != "plain text" {
		t.Errorf("a plain string is shown verbatim: %q", got)
	}
	// *Error: English in Error(), the request language in Render, also wrapped
	err := fmt.Errorf("wrapped: %w", E("auth.too_short", 10))
	if got := Render(FR, err); got != "le mot de passe doit comporter au moins 10 caractères" {
		t.Errorf("Render(fr) = %q", got)
	}
	if got := E("auth.too_short", 10).Error(); got != "the password must be at least 10 characters long" {
		t.Errorf("Error() = %q", got)
	}
	if got := Text(ES, RefOf(err)); got != "la contraseña debe tener al menos 10 caracteres" {
		t.Errorf("RefOf: %q", got)
	}
	if got := RefOf(errors.New("other")); got != "other" {
		t.Errorf("RefOf of a plain error: %q", got)
	}
	if got := Render(ES, errors.New(Ref("auth.bad_password"))); got != "contraseña incorrecta" {
		t.Errorf("a Ref inside a plain error: %q", got)
	}
}
