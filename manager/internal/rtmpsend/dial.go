package rtmpsend

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/x509roots/fallback/bundle"
)

// link is the connection to the server once dialled, in layers: the TCP socket (for TCP_INFO), the bytes
// written to it (the stats), and what the RTMP library and the media writer share (TLS on top when
// rtmps).
type link struct {
	tcp  *net.TCPConn
	wire *countConn
	conn *sharedConn
}

// dial resolves, connects from bind (when given) and, for rtmps, does the TLS handshake with SNI. Its
// errors are *stageError.
func dial(ctx context.Context, t Target, bind net.IP, roots *x509.CertPool) (*link, error) {
	d := net.Dialer{}
	network := "tcp"
	if bind != nil {
		d.LocalAddr = &net.TCPAddr{IP: bind}
		network = "tcp4" // the source is IPv4: resolve to IPv4 only
	}
	c, err := d.DialContext(ctx, network, t.Addr())
	if err != nil {
		return nil, &stageError{stageDial, err}
	}
	tcp := c.(*net.TCPConn)
	tuneSocket(tcp)
	l := &link{tcp: tcp, wire: &countConn{Conn: c}}
	var nc net.Conn = l.wire
	if t.TLS {
		tc := tls.Client(l.wire, &tls.Config{ServerName: t.Host, RootCAs: roots, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, &stageError{stageTLS, err}
		}
		nc = tc
	}
	l.conn = &sharedConn{Conn: nc}
	return l, nil
}

// loadRoots is the system's roots plus Mozilla's embedded in the binary (the board's store is old),
// plus the PEM file caFile when given (tests).
func loadRoots(caFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	for r := range bundle.Roots() {
		cert, err := x509.ParseCertificate(r.Certificate)
		if err != nil {
			continue
		}
		if r.Constraint == nil {
			pool.AddCert(cert)
		} else {
			pool.AddCertWithConstraint(cert, r.Constraint)
		}
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("no certificate in the CA file")
		}
	}
	return pool, nil
}

// countConn counts the bytes written to the socket (TLS records included).
type countConn struct {
	net.Conn
	written atomic.Int64
}

func (c *countConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

// sharedConn serializes the writers of the connection: the RTMP library answers pings and acks from its
// reader goroutine while the media writer sends; each one writes whole messages under the lock, so
// their chunks never interleave.
type sharedConn struct {
	net.Conn
	mu sync.Mutex
}

func (c *sharedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.Write(p)
}

// writePiece: a message goes in pieces this big, so that the bytes written are counted as they leave
// and not when a whole keyframe has.
const writePiece = 16 << 10

// writeMessage writes one whole message, failing if it does not leave within timeout.
func (c *sharedConn) writeMessage(p []byte, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Conn.SetWriteDeadline(time.Now().Add(timeout))
	defer c.Conn.SetWriteDeadline(time.Time{})
	for len(p) > 0 {
		n, err := c.Conn.Write(p[:min(len(p), writePiece)])
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}
