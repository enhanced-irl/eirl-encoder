package api

import (
	"context"
	"net"
	"net/http"
	"time"
)

// --- local socket: the API for the engine's screen (MANAGER.md §3, "Local socket"; SETUP.md §8) ------------------
//
// The touch screen is drawn by the engine (eirl-engine, another user, without privileges). For its settings it calls
// this same API over a Unix socket (/run/eirl-encoder/api.sock, 0660, group eirl-encoder: the engine has it through
// its unit). No session or CSRF: the socket's permissions are the authentication. Only the whitelisted routes, with
// the same handlers, validation and errors as the web; the rest, 403 local_forbidden. Restart and power off do NOT go
// through here (they stay in screen.power): they must work even if the API hangs.

// localRoutes: the whitelist, as method and path patterns of the route table (Handler), where the request is
// forwarded. Every route was reviewed for secrets before being added (SETUP.md §8): the Wi-Fi credentials have their
// own handler below (without the cameras' keys); the rest carry nothing secret.
var localRoutes = []string{
	// Board network and Wi-Fi (WIFI.md §5; the password typed on the screen goes straight to NetworkManager)
	"GET /api/v1/network", "PUT /api/v1/network", "PUT /api/v1/network/ap", "PUT /api/v1/network/lan",
	"GET /api/v1/network/wifi", "PUT /api/v1/network/wifi", "GET /api/v1/network/wifi/scan",
	"POST /api/v1/network/wifi/connect", "POST /api/v1/network/wifi/forget",
	// Device settings and first-boot wizard (SETUP.md §2-4). GET /setup shows the setup code only here
	"GET /api/v1/device", "PUT /api/v1/device", "GET /api/v1/device/timezones",
	"GET /api/v1/setup", "POST /api/v1/setup/password", "POST /api/v1/setup/step", "POST /api/v1/setup/complete",
	// Run the wizard again (Settings → About on the screen): the password is kept and its step is skipped, and
	// every other step is already reachable from the screen's Settings, so it gives the screen no new power
	"POST /api/v1/setup/restart",
	// Source slots and aliases (SETUP.md §5)
	"GET /api/v1/sources/slots", "PUT /api/v1/sources/slots",
	// Updates (UPDATES.md; the handlers are the updater's). Until they exist, the route table answers 404
	"GET /api/v1/update", "POST /api/v1/update/check", "POST /api/v1/update/install",
	// Linking with EnhancedIRL from the wizard: the state and the pairing code (?qr=rows: its QR in rows). Never the
	// token or the pollSecret (cloud.View does not carry them). Unlinking stays in the web
	"GET /api/v1/cloud", "POST /api/v1/cloud/pair",
}

// LocalHandler: the handler of the local socket. Handler must have been called (it builds the route table).
func (s *Server) LocalHandler() http.Handler {
	if s.mux == nil {
		s.Handler()
	}
	routes := s.mux
	mux := http.NewServeMux()
	for _, p := range localRoutes {
		mux.Handle(p, routes)
	}
	// Only the Wi-Fi: the cameras' keys are not shown on the screen (they go to the web, with a session)
	mux.HandleFunc("GET /api/v1/network/credentials", s.getWiFiCredentials)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, r, 403, "local_forbidden", "access.local_forbidden")
	})
	return s.logLocal(securityHeaders(s.withLang(markLocal(mux))))
}

type localKey struct{}

// markLocal: the request came over the local socket (isLocal).
func markLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), localKey{}, true)))
	})
}

// isLocal: the request came over the local socket (the touch screen), not from the web.
func isLocal(r *http.Request) bool {
	v, _ := r.Context().Value(localKey{}).(bool)
	return v
}

// logLocal: like logRequests, with who calls (uid and pid of the other end of the socket) instead of the IP.
func (s *Server) logLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		p, _ := r.Context().Value(peerKey{}).(Peer)
		s.Log.Info("http local", "method", r.Method, "path", r.URL.Path, "code", rec.code, "ms", time.Since(start).Milliseconds(), "uid", p.UID, "pid", p.PID)
	})
}

// Peer: the process at the other end of the socket (SO_PEERCRED; -1 if unknown).
type Peer struct{ UID, PID int }

type peerKey struct{}

// LocalConnContext: for http.Server.ConnContext of the local socket; leaves the Peer in the context.
func LocalConnContext(ctx context.Context, c net.Conn) context.Context {
	p := Peer{UID: -1, PID: -1}
	if uc := unixConn(c); uc != nil {
		p = peerCred(uc)
	}
	return context.WithValue(ctx, peerKey{}, p)
}

// unixConn: the Unix socket underneath, removing the wrappers that expose it with NetConn() (the connection limit of
// eirl-encoderd, like tls.Conn). nil if it is not a Unix socket.
func unixConn(c net.Conn) *net.UnixConn {
	for i := 0; i < 8 && c != nil; i++ {
		if uc, ok := c.(*net.UnixConn); ok {
			return uc
		}
		w, ok := c.(interface{ NetConn() net.Conn })
		if !ok {
			return nil
		}
		c = w.NetConn()
	}
	return nil
}
