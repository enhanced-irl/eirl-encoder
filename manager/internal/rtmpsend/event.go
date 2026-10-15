package rtmpsend

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The reasons of `event=failed`: a closed list, part of the sender contract (OUTPUTS.md §5). The engine
// maps them to the output's reasons.
const (
	ReasonRelayUnreachable = "relay_unreachable"
	ReasonRejected         = "rejected"
	ReasonTLSFailed        = "tls_failed"
	ReasonClockNotSynced   = "clock_not_synced"
	ReasonConnectionLost   = "connection_lost"
	ReasonInternal         = "internal"
)

// rejectWindow: the server closing the connection this soon after accepting the publish is a refusal
// (some servers check the key or the codec only once the media arrives), not a lost connection.
const rejectWindow = 5 * time.Second

// maxDetail bounds the detail of a failure: it is a hint for the log, not a dump.
const maxDetail = 300

// failure is what ends the sender: one of the reasons and a human detail.
type failure struct {
	reason, detail string
}

// formatEvent is one stderr line: `event=<code>` plus key=value pairs (kv alternates keys and values).
// A value with a space, a quote, a backslash, an `=` or a control character goes quoted (Go %q), and
// so does `detail` always.
func formatEvent(code string, kv ...string) string {
	var b strings.Builder
	b.WriteString("event=")
	b.WriteString(code)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteByte(' ')
		b.WriteString(kv[i])
		b.WriteByte('=')
		if kv[i] == "detail" || needsQuote(kv[i+1]) {
			b.WriteString(fmt.Sprintf("%q", kv[i+1]))
		} else {
			b.WriteString(kv[i+1])
		}
	}
	return b.String()
}

func needsQuote(v string) bool {
	for _, r := range v {
		if r <= ' ' || r == '"' || r == '\\' || r == '=' || r == 0x7f || r > 0x7e {
			return true
		}
	}
	return false
}

// events writes the stderr lines, one at a time, with the stream key masked in every value.
type events struct {
	mu  sync.Mutex
	w   io.Writer
	key string
}

func (e *events) emit(code string, kv ...string) {
	for i := 1; i < len(kv); i += 2 {
		kv[i] = maskKey(kv[i], e.key)
	}
	line := formatEvent(code, kv...) + "\n"
	e.mu.Lock()
	defer e.mu.Unlock()
	io.WriteString(e.w, line)
}

func (e *events) failed(f failure) {
	e.emit("failed", "reason", f.reason, "detail", f.detail)
}

// maskKey hides the stream key (as is and URL-escaped) wherever a message could have echoed it.
func maskKey(s, key string) string {
	if key == "" {
		return s
	}
	for _, k := range []string{key, url.PathEscape(key), url.QueryEscape(key)} {
		s = strings.ReplaceAll(s, k, "***")
	}
	return s
}

func clip(s string) string {
	if len(s) > maxDetail {
		return s[:maxDetail] + "…"
	}
	return s
}

// stage is where the connection setup was when it failed.
type stage int

const (
	stageDial stage = iota // DNS and TCP connect
	stageTLS               // the TLS handshake
	stageRTMP              // the RTMP handshake, connect and publish
)

// stageError marks an error of the dial (the RTMP library returns it unchanged).
type stageError struct {
	stage stage
	err   error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

// classifySetup gives the reason of an error before the publish was accepted. timedOut: the connect
// timeout expired (the RTMP library then closes the socket, so the error itself says little).
func classifySetup(err error, now time.Time, timedOut bool) failure {
	st := stageRTMP
	var se *stageError
	if errors.As(err, &se) {
		st = se.stage
	}
	if timedOut || isTimeout(err) {
		return failure{ReasonRelayUnreachable, "timeout " + [...]string{"connecting", "in the TLS handshake",
			"waiting for the server's answer"}[st]}
	}
	switch st {
	case stageDial:
		var dns *net.DNSError
		if errors.As(err, &dns) {
			return failure{ReasonRelayUnreachable, clip("dns: " + dns.Err)}
		}
		return failure{ReasonRelayUnreachable, clip(err.Error())}
	case stageTLS:
		var inv x509.CertificateInvalidError
		if errors.As(err, &inv) && inv.Reason == x509.Expired && inv.Cert != nil && now.Before(inv.Cert.NotBefore) {
			return failure{ReasonClockNotSynced, fmt.Sprintf("the server's certificate is valid from %s; the device clock says %s",
				inv.Cert.NotBefore.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))}
		}
		return failure{ReasonTLSFailed, clip(err.Error())}
	}
	return failure{ReasonRejected, rtmpDetail(err)}
}

// classifyLive gives the reason of a read or write error once publishing. sincePublish is the time since
// the server accepted the publish.
func classifyLive(err error, sincePublish time.Duration) failure {
	var st *statusError
	if errors.As(err, &st) {
		return failure{ReasonRejected, clip(st.Error())}
	}
	if closedByServer(err) && sincePublish < rejectWindow {
		return failure{ReasonRejected, "the server closed the connection right after the publish"}
	}
	if closedByServer(err) {
		return failure{ReasonConnectionLost, "the server closed the connection"}
	}
	return failure{ReasonConnectionLost, clip(err.Error())}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}

// closedByServer: the peer ended the connection (FIN or RST).
func closedByServer(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

var (
	reCode = regexp.MustCompile(`\{code ([^{}]*)\}`)
	reDesc = regexp.MustCompile(`\{description ([^{}]*)\}`)
)

// rtmpDetail turns an error of the RTMP library's connect/publish into a short detail. Its refusals are
// «bad result: <the server's answer dumped with %v>»: keep the code and the description of the answer.
func rtmpDetail(err error) string {
	if closedByServer(err) {
		return "the server closed the connection during connect/publish"
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "bad result:") {
		var parts []string
		if m := reCode.FindStringSubmatch(msg); m != nil {
			parts = append(parts, m[1])
		}
		if m := reDesc.FindStringSubmatch(msg); m != nil && m[1] != "" {
			parts = append(parts, m[1])
		}
		if len(parts) > 0 {
			return clip(strings.Join(parts, ": "))
		}
	}
	return clip(msg)
}

// statusError is an `onStatus` or `_error` of level «error» received while publishing.
type statusError struct {
	code, description string
}

func (e *statusError) Error() string {
	if e.description == "" {
		return e.code
	}
	return e.code + ": " + e.description
}
