// Package elog writes the engine's journal lines: one line per event, with a subsystem prefix and a
// stable event code, then key=value pairs.
//
//	[mixer] event=source_state slot=1 source=hdmi state=no_signal detail="sin señal HDMI"
//
// The app classifies lines by their event code (manager/internal/service/journal.go), never by the
// text, so codes must not change once released. Values are quoted when they contain spaces, quotes,
// '=' or control characters. Secrets never go through here: callers pass masked values.
package elog

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

var (
	mu  sync.Mutex
	out io.Writer = os.Stderr
)

// SetOutput redirects the log (tests); nil restores stderr.
func SetOutput(w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	mu.Lock()
	out = w
	mu.Unlock()
}

// Event logs `[prefix] event=code k=v …`. kv alternates keys (string) and values (any).
func Event(prefix, code string, kv ...any) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprint(out, Format(prefix, code, kv...))
}

// Format builds the line Event writes, with its trailing newline.
func Format(prefix, code string, kv ...any) string {
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(prefix)
	b.WriteString("] event=")
	b.WriteString(code)
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok || k == "" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(Value(kv[i+1]))
	}
	if len(kv)%2 == 1 {
		b.WriteString(" extra=")
		b.WriteString(Value(kv[len(kv)-1]))
	}
	b.WriteByte('\n')
	return b.String()
}

// Value renders one value: bare when it is a simple token, Go-quoted otherwise.
func Value(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return `""`
	case string:
		s = x
	case error:
		s = x.Error()
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(x), 'f', -1, 32)
	case fmt.Stringer:
		s = x.String()
	default:
		s = fmt.Sprint(x)
	}
	if s == "" || needsQuote(s) {
		return strconv.Quote(s)
	}
	return s
}

func needsQuote(s string) bool {
	for _, r := range s {
		if r == '"' || r == '=' || r == '\\' || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}
