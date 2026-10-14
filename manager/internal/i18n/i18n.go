// Package i18n is the server-side catalog of user-visible messages of eirl-encoderd (SETUP.md §3).
//
// Every API error keeps its stable `error` code; only `message` is translated, server-side, in the
// language of the request: the first supported primary tag of Accept-Language, else the device
// language, else English. Log messages are not translated (they are for operators, in English).
//
// A message is a catalog key plus format arguments. Where only a string fits (config.FieldErrors,
// which other packages build and read as map[string]string), a message travels as a Ref: an opaque
// string that Text renders later in the request language. Any other string is shown verbatim.
package i18n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Language codes of the user interface.
const (
	ES = "es"
	EN = "en"
	FR = "fr"
)

// Languages in the order the API lists them.
var Languages = []string{ES, EN, FR}

// Fallback is the last language of the chain (request → device → Fallback).
const Fallback = EN

// Supported reports whether lang is one of Languages (exact, lowercase code).
func Supported(lang string) bool { return slices.Contains(Languages, lang) }

// Negotiate picks the response language: the first supported primary tag of an Accept-Language
// header (by q-value, then by order), else deviceLang if supported, else Fallback.
func Negotiate(acceptLanguage, deviceLang string) string {
	type tag struct {
		lang string
		q    float64
		pos  int
	}
	var tags []tag
	for i, part := range strings.Split(acceptLanguage, ",") {
		if i >= 32 { // a header is never this long on purpose
			break
		}
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.TrimSpace(k) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f >= 0 && f <= 1 {
					q = f
				}
			}
		}
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(name)), "-")
		if q > 0 && Supported(primary) {
			tags = append(tags, tag{primary, q, i})
		}
	}
	sort.SliceStable(tags, func(a, b int) bool { return tags[a].q > tags[b].q })
	if len(tags) > 0 {
		return tags[0].lang
	}
	if Supported(deviceLang) {
		return deviceLang
	}
	return Fallback
}

// Msg renders key in lang with fmt-style args. A language missing for a key falls back to English
// and then Spanish (the test forbids both cases); an unknown key renders as the key itself, so a
// mistake is visible but never breaks a response.
func Msg(lang, key string, args ...any) string {
	entry, ok := catalog[key]
	if !ok {
		return key
	}
	format, ok := entry[lang]
	if !ok {
		if format, ok = entry[Fallback]; !ok {
			format = entry[ES]
		}
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// Has reports whether key is in the catalog.
func Has(key string) bool { _, ok := catalog[key]; return ok }

// Error is an error whose user-visible text is a catalog message. Error() renders it in English
// (logs, and callers that do not know the request language).
type Error struct {
	Key  string
	Args []any
}

// E builds an *Error.
func E(key string, args ...any) *Error { return &Error{Key: key, Args: args} }

func (e *Error) Error() string { return Msg(EN, e.Key, e.Args...) }

// Ref encodes the message as a Ref string.
func (e *Error) Ref() string { return Ref(e.Key, e.Args...) }

// refMark starts every Ref. A record separator never appears in a message typed by a person.
const refMark = "\x1e"

// Ref encodes a catalog message as an opaque string, for places that only hold strings (the
// values of config.FieldErrors). Arguments must be strings, booleans or numbers.
func Ref(key string, args ...any) string {
	b, err := json.Marshal(append([]any{key}, args...))
	if err != nil {
		return key
	}
	return refMark + string(b)
}

// RefOf: the Ref of an *Error (wrapped or not), else err.Error() as is.
func RefOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Ref()
	}
	return err.Error()
}

// Text renders s in lang if it is a Ref; any other string is returned unchanged.
func Text(lang, s string) string {
	if !strings.HasPrefix(s, refMark) {
		return s
	}
	d := json.NewDecoder(strings.NewReader(s[len(refMark):]))
	d.UseNumber()
	var parts []any
	if d.Decode(&parts) != nil || len(parts) == 0 {
		return s
	}
	key, ok := parts[0].(string)
	if !ok {
		return s
	}
	args := parts[1:]
	for i, a := range args {
		if n, ok := a.(json.Number); ok {
			if v, err := n.Int64(); err == nil {
				args[i] = v
			} else if f, err := n.Float64(); err == nil {
				args[i] = f
			}
		}
	}
	return Msg(lang, key, args...)
}

// Render: the user-visible text of err in lang (an *Error or a Ref inside err.Error(); else the
// error text as is).
func Render(lang string, err error) string {
	var e *Error
	if errors.As(err, &e) {
		return Msg(lang, e.Key, e.Args...)
	}
	return Text(lang, err.Error())
}

type ctxKey struct{}

// WithLang stores the request language in ctx.
func WithLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, ctxKey{}, lang)
}

// LangFrom: the language stored by WithLang, if any.
func LangFrom(ctx context.Context) (string, bool) {
	l, ok := ctx.Value(ctxKey{}).(string)
	return l, ok
}
