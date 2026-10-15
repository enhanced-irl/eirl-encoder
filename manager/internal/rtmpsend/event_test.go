package rtmpsend

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFormatEvent(t *testing.T) {
	cases := []struct {
		code string
		kv   []string
		want string
	}{
		{"connected", nil, "event=connected"},
		{"connecting", []string{"host", "live.example.com", "port", "443", "tls", "true", "bind", "10.0.0.2"},
			"event=connecting host=live.example.com port=443 tls=true bind=10.0.0.2"},
		{"connecting", []string{"host", "::1", "port", "1935", "tls", "false", "bind", ""},
			"event=connecting host=::1 port=1935 tls=false bind="},
		{"publishing", []string{"codec", "h265"}, "event=publishing codec=h265"},
		{"dropped", []string{"frames", "42", "queue_ms", "3016"}, "event=dropped frames=42 queue_ms=3016"},
		{"failed", []string{"reason", "rejected", "detail", "NetStream.Publish.BadName"},
			`event=failed reason=rejected detail="NetStream.Publish.BadName"`},
		{"failed", []string{"reason", "internal", "detail", `say "hi"` + "\n"},
			`event=failed reason=internal detail="say \"hi\"\n"`},
		{"x", []string{"a", "b c", "d", "e=f"}, `event=x a="b c" d="e=f"`},
	}
	for _, c := range cases {
		if got := formatEvent(c.code, c.kv...); got != c.want {
			t.Errorf("formatEvent(%s, %q) = %s, want %s", c.code, c.kv, got, c.want)
		}
	}
}

func TestEventsMaskKey(t *testing.T) {
	var b bytes.Buffer
	ev := &events{w: &b, key: "live_123/x y"}
	ev.failed(failure{ReasonRejected, "publish live_123/x y refused; live_123%2Fx%20y; live_123%2Fx+y"})
	if strings.Contains(b.String(), "live_123") {
		t.Fatalf("key leaked: %s", b.String())
	}
	if !strings.HasSuffix(b.String(), "\n") || strings.Count(b.String(), "\n") != 1 {
		t.Errorf("not one line: %q", b.String())
	}
	if maskKey("anything", "") != "anything" {
		t.Error("an empty key must leave the text alone")
	}
}

func TestClassifySetup(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	future := &x509.Certificate{NotBefore: now.Add(48 * time.Hour), NotAfter: now.Add(90 * 24 * time.Hour)}
	past := &x509.Certificate{NotBefore: now.Add(-90 * 24 * time.Hour), NotAfter: now.Add(-time.Hour)}
	tlsErr := func(err error) error {
		return &stageError{stageTLS, &tls.CertificateVerificationError{Err: err}}
	}
	cases := []struct {
		name     string
		err      error
		timedOut bool
		reason   string
		detail   string // a part of it
	}{
		{"dns", &stageError{stageDial, &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x"}}},
			false, ReasonRelayUnreachable, "dns: no such host"},
		{"refused", &stageError{stageDial, &net.OpError{Op: "dial", Net: "tcp",
			Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}}, false, ReasonRelayUnreachable, "refused"},
		{"dial timeout", &stageError{stageDial, context.DeadlineExceeded}, false, ReasonRelayUnreachable, "timeout connecting"},
		{"rtmp timeout", net.ErrClosed, true, ReasonRelayUnreachable, "timeout waiting"},
		{"tls timeout", &stageError{stageTLS, context.DeadlineExceeded}, false, ReasonRelayUnreachable, "TLS"},
		{"not yet valid", tlsErr(x509.CertificateInvalidError{Cert: future, Reason: x509.Expired}), false,
			ReasonClockNotSynced, "valid from 2026-10-17"},
		{"expired", tlsErr(x509.CertificateInvalidError{Cert: past, Reason: x509.Expired}), false, ReasonTLSFailed, ""},
		{"unknown authority", tlsErr(x509.UnknownAuthorityError{}), false, ReasonTLSFailed, "unknown authority"},
		{"hostname", tlsErr(x509.HostnameError{Certificate: past, Host: "a"}), false, ReasonTLSFailed, ""},
		{"not tls", &stageError{stageTLS, tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}},
			false, ReasonTLSFailed, "TLS handshake"},
		{"bad name", errors.New("bad result: &{4 16777216 onStatus 5 [<nil> [{level error} {code NetStream.Publish.BadName} {description Bad stream name}]]}"),
			false, ReasonRejected, "NetStream.Publish.BadName: Bad stream name"},
		{"connect rejected", errors.New("bad result: &{3 0 _error 1 [<nil> [{level error} {code NetConnection.Connect.Rejected} {description }]]}"),
			false, ReasonRejected, "NetConnection.Connect.Rejected"},
		{"auth", errors.New("credentials are required"), false, ReasonRejected, "credentials"},
		{"closed during publish", fmt.Errorf("read: %w", io.EOF), false, ReasonRejected, "closed the connection"},
		{"reset during publish", &net.OpError{Op: "read", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}},
			false, ReasonRejected, "closed the connection"},
	}
	for _, c := range cases {
		f := classifySetup(c.err, now, c.timedOut)
		if f.reason != c.reason || !strings.Contains(f.detail, c.detail) {
			t.Errorf("%s: got %s %q, want %s with %q", c.name, f.reason, f.detail, c.reason, c.detail)
		}
	}
}

func TestClassifyLive(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		since time.Duration
		want  string
	}{
		{"status error", &statusError{"NetStream.Publish.Rejected", "codec"}, time.Minute, ReasonRejected},
		{"closed right after publish", io.EOF, time.Second, ReasonRejected},
		{"closed later", io.EOF, time.Minute, ReasonConnectionLost},
		{"reset later", &os.SyscallError{Syscall: "write", Err: syscall.ECONNRESET}, time.Minute, ReasonConnectionLost},
		{"write timeout", os.ErrDeadlineExceeded, time.Second, ReasonConnectionLost},
		{"broken pipe", &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}, time.Second, ReasonConnectionLost},
	}
	for _, c := range cases {
		if f := classifyLive(c.err, c.since); f.reason != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, f.reason, f.detail, c.want)
		}
	}
}
