// Package netcheck: whether a link reaches the internet (WIFI.md §13, MODEMS.md §5). One probe and one
// hysteresis for every link: the Wi-Fi client (internal/wifi) and the modems and phones (internal/links).
package netcheck

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// HTTPS: HEAD over HTTPS to url from ip, with the certificate verified (a portal that intercepts HTTPS is
// "no internet" too). With ctx: when the app stops it does not wait for the timeout.
func HTTPS(ctx context.Context, url, ip string) bool {
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}, Timeout: 5 * time.Second}
	cl := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{DialContext: d.DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// State: what is known of one link's internet access, with hysteresis. Each change takes the link out of or
// puts it into the SRTLA share (a SIGHUP to srtla_send and a new share): a hotel Wi-Fi or a modem that comes
// and goes must not shake the stream every 30 s. The zero value is "not checked yet".
type State struct {
	OK      *bool     // nil = not checked yet
	Streak  int       // consecutive checks that say the opposite of OK
	Changed time.Time // last change of OK
}

// Record one check: it takes `streak` consecutive checks saying the opposite and, except the first time,
// `dwell` since the last change. A first good check is accepted at once (it does not change the share).
func (s *State) Record(ok bool, now time.Time, streak int, dwell time.Duration) {
	switch {
	case s.OK == nil && ok:
		s.OK, s.Streak, s.Changed = &ok, 0, now
	case s.OK != nil && *s.OK == ok:
		s.Streak = 0
	default:
		s.Streak++
		if s.Streak >= streak && (s.OK == nil || now.Sub(s.Changed) >= dwell) {
			v := ok
			s.OK, s.Streak, s.Changed = &v, 0, now
		}
	}
}

// Off: the link is known to be without internet.
func (s *State) Off() bool { return s.OK != nil && !*s.OK }
