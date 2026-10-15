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
//
// Since Oct 14 everything the web configures can be configured on the screen (the owner's decision), secrets
// included: the screen shows them as dots and reveals them with an eye button, through the two local-only routes
// below. The screen is physical access: it already shows the Wi-Fi password and the setup code, and the engine
// already holds the stream key to stream it.

// localRoutes: the whitelist, as method and path patterns of the route table (Handler), where the request is
// forwarded. Every route was reviewed before being added (SETUP.md §8): what secret it returns and what it can break.
// The secrets in clear only come from the local-only routes registered in LocalHandler.
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
	// Linking with EnhancedIRL: the state and the pairing code (?qr=rows: its QR in rows), and unlinking or cancelling a
	// code. Never the token or the pollSecret (cloud.View does not carry them)
	"GET /api/v1/cloud", "POST /api/v1/cloud/pair", "DELETE /api/v1/cloud",
	// The stream destination. GET masks the key as on the web; its value in clear is /destination/secrets (below)
	"GET /api/v1/destination", "PUT /api/v1/destination",
	// The saved destinations (OUTPUTS.md §9.3): the list (masked), add, change, delete and activate one (the screen's
	// Settings › Stream › Destination). Their secrets in clear only through /destinations/{id}/secrets (below)
	"GET /api/v1/destinations", "POST /api/v1/destinations", "PUT /api/v1/destinations/{id}",
	"DELETE /api/v1/destinations/{id}", "POST /api/v1/destinations/{id}/activate",
	// The status snapshot (uplinks, the saved input, pendingRestart, transport): the same as the web's SSE, without
	// secrets (collect: no destination, no key, no Wi-Fi password)
	"GET /api/v1/status",
	// SRTLA uplinks, the input and the sources it can be
	"PUT /api/v1/uplinks/{name}", "PUT /api/v1/input", "GET /api/v1/sources",
	// "Apply" of saved changes (the web's pending banner). Only restart: starting and stopping the engine stay in
	// the web. It restarts the engine, so the screen that asks for it does not see the answer
	"POST /api/v1/service/restart",
	// Network cameras: add, rename or new key, remove. Their answers carry the camera's URLs with its key, as on the web
	"POST /api/v1/cameras", "PATCH /api/v1/cameras/{id}", "DELETE /api/v1/cameras/{id}",
	// The admin password, with the current one (no session to keep: every web session is closed)
	"PUT /api/v1/password",
	// Cellular modems (MODEMS.md §7): the list (only the last digits of IMEI and ICCID), their configuration, the SIM's
	// PIN (typed on the screen, straight to the modem or NetworkManager; never in a GET or a log), a reset and a HiLink's
	// subnet
	"GET /api/v1/modems", "PUT /api/v1/modems/{id}", "POST /api/v1/modems/{id}/{action}",
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
	// Local-only (never on the web): what the screen's eye buttons reveal
	mux.HandleFunc("GET /api/v1/network/credentials", s.getLocalCredentials)
	mux.HandleFunc("GET /api/v1/destination/secrets", s.getDestinationSecrets)
	mux.HandleFunc("GET /api/v1/destinations/{id}/secrets", s.getDestinationSecretsByID)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, r, 403, "local_forbidden", "access.local_forbidden")
	})
	return s.logLocal(securityHeaders(s.withLang(markLocal(limitBody(mux)))))
}

// limitBody: the web's body limit for mutations (csrf), over the socket too: what comes from the engine is bounded.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

// getLocalCredentials: GET /network/credentials over the socket. Only the Wi-Fi ({"wifi": …}, the screen's Wi-Fi
// panel); with ?cameras=1, the same as the web (the Wi-Fi and every camera's URLs with their keys), for a camera's
// eye button. ?qr=rows works in both (wifi.qrRows); the screen draws a camera's QR from its URL.
func (s *Server) getLocalCredentials(w http.ResponseWriter, r *http.Request) {
	cameras := false
	switch r.URL.Query().Get("cameras") {
	case "":
	case "1":
		cameras = true
	default:
		fail(w, r, 400, "bad_request", "network.bad_cameras")
		return
	}
	cr, ok := s.credentials(w, r)
	switch {
	case !ok:
	case cameras:
		writeJSON(w, 200, cr)
	default:
		writeJSON(w, 200, map[string]any{"wifi": cr.WiFi})
	}
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
