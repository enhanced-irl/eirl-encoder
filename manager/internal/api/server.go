// Package api is the local API of eirl-encoderd (/api/v1). See MANAGER.md §3 for the contract and
// SETUP.md for the device settings, the setup wizard, the source slots and the message languages.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/audiolevel"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/hw"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/links"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/storage"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// LiveRelay is live.Relay (an interface for the tests).
type LiveRelay interface {
	Answer(key, offer string, alive func() bool) (string, error)
}

// Unit is eirl-engine.service (an interface to test without systemd).
type Unit interface {
	Status(ctx context.Context) (service.Status, error)
	Control(ctx context.Context, action string) error
	Tail(ctx context.Context, n int) ([]service.Entry, error)
	Follow(ctx context.Context, fn func(service.Entry)) error
}

// Power: the touch screen's Power menu (service.Unit: sudo -n with the exact commands).
type Power interface {
	Reboot(ctx context.Context) error
	Poweroff(ctx context.Context) error
}

type Board interface {
	Signal() hw.Signal
	Audio() hw.Audio
	System() hw.System
}

type Server struct {
	Unit      Unit
	Board     Board
	Auth      *auth.Auth
	Config    *config.Store
	StatsPath string // /run/eirl-engine/srt-stats.json
	// RunStatePath: /run/eirl-engine/state, what the running stream started with (mode, source, fingerprint)
	RunStatePath string
	// ABRPath: /run/eirl-engine/abr, the current video bitrate with the adaptive bitrate on
	ABRPath string
	// Uplinks: internet uplinks for SRTLA (nil = no uplinks card)
	Uplinks *uplinks.Reader
	// Links: the 2-second round that judges every link and watches the modems (MODEMS.md §4); with it,
	// /status serves its cached snapshot. nil = /status reads the uplinks itself, and no modems
	Links *links.Service
	// Sources: detection of video and audio sources (nil = GET /sources answers 503)
	Sources *sources.Scanner
	// VideoPath: /run/eirl-engine/video, the real capture fps (written by the engine)
	VideoPath string
	// Hostnames: names allowed in the Host header besides literal IPs, the device hostname (the one at
	// start and the current one), <hostname>.local and localhost (defence against DNS rebinding)
	Hostnames []string
	Meter     *audiolevel.Meter
	// Live: the WebRTC live preview (nil = off: POST /live answers 503)
	Live LiveRelay
	Info Info
	Web  fs.FS // web interface; nil = none
	Log  *slog.Logger

	// Network: network cameras and their access point (F7; nil = no eirl-netsetup: /network answers 503)
	Network *network.Manager

	// Bluetooth: the board's Bluetooth audio (BLUETOOTH.md); nil = none (GET answers no_stack)
	Bluetooth BluetoothControl

	// Cloud: link with an EnhancedIRL account (nil = off: /cloud answers 503)
	Cloud CloudControl
	// Mixer: the eirl-engine mixer (MIXER=1; nil = /mixer answers 503)
	Mixer *mixer.Client
	// Power: restart or power off the board from the screen's Power menu (nil = ignored)
	Power Power

	// Local recording to a USB disk (RECORDING.md §6). StoragePath: the storage helper's state file
	// (/run/eirl-storage/state.json; "" = no helper: the disk is always "none"); StorageRunner runs its eject and
	// format (nil = for real: sudo -n with the exact commands of the sudoers rule)
	StoragePath   string
	StorageRunner storage.Runner

	// Device settings and setup wizard (SETUP.md §2). Device: device.json (nil = /device and /setup
	// answer 503); System: time zone, NTP, clock and hostname (nil = the OS hostname, no clock
	// settings); Zones: the cached time zone list; SetupCodePath: the setup code ("" = none: the web
	// cannot set the first password, only the screen can); Versions: the version files of GET /device
	Device        *device.Store
	System        device.System
	Zones         *device.Zones
	SetupCodePath string
	Versions      Versions
	// SlotsPath: /etc/eirl-encoder/slots.json, the source slots (SETUP.md §5; "" = /sources/slots answers 503)
	SlotsPath string
	// Update: over-the-air updates (UPDATES.md §8; nil = /update answers 503)
	Update UpdateControl
	// SystemStateDir: the system layer's state (/var/lib/eirl-system, UPDATES.md §12; "" = GET /update has no
	// "system"); BootID: this boot's id (its pending restarts are for the boot they were recorded in)
	SystemStateDir string
	BootID         func() string

	hub   *hub
	hosts map[string]bool
	lan   lanNets
	// mux: the route table (Handler); the local socket forwards its whitelist to it
	mux *http.ServeMux
	// setupMu serialises the first password and the reset; codeLimit: attempts at the setup code (the wizard's
	// first password and the reset share it, like the login's but its own); passwordLimit: attempts at the
	// current password of PUT /password and of the re-authentication (the web's IP, or the screen)
	setupMu       sync.Mutex
	codeLimit     attemptLimiter
	passwordLimit attemptLimiter
}

const cookieName = "eirl_sess"

// maxBody: the body limit of a mutation (web and local socket), except /live.
const maxBody = 16 << 10

func (s *Server) Handler() http.Handler {
	s.hub = newHub(s)
	s.hosts = map[string]bool{"localhost": true}
	// The hostname at start stays allowed after a rename: mDNS may keep announcing it until a restart
	if h := s.hostname(); h != "" {
		s.hosts[h], s.hosts[h+".local"] = true, true
	}
	for _, h := range s.Hostnames {
		s.hosts[strings.ToLower(h)] = true
	}
	mux := http.NewServeMux()
	s.mux = mux
	mux.HandleFunc("POST /api/v1/login", s.login)
	mux.HandleFunc("POST /api/v1/logout", s.logout)
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"authenticated": true})
	})
	mux.HandleFunc("PUT /api/v1/password", s.changePassword)
	mux.HandleFunc("POST /api/v1/password/reset", s.resetPassword)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("GET /api/v1/logs", s.logs)
	mux.HandleFunc("GET /api/v1/destination", s.getDestination)
	mux.HandleFunc("PUT /api/v1/destination", s.putDestination)
	mux.HandleFunc("GET /api/v1/destinations", s.getDestinations)
	mux.HandleFunc("POST /api/v1/destinations", s.postDestination)
	mux.HandleFunc("PUT /api/v1/destinations/{id}", s.putDestinationByID)
	mux.HandleFunc("DELETE /api/v1/destinations/{id}", s.deleteDestination)
	mux.HandleFunc("POST /api/v1/destinations/{id}/activate", s.activateDestination)
	mux.HandleFunc("PUT /api/v1/output", s.putOutput)
	mux.HandleFunc("GET /api/v1/recording", s.getRecording)
	mux.HandleFunc("PUT /api/v1/recording", s.putRecording)
	mux.HandleFunc("PUT /api/v1/record", s.putRecord)
	mux.HandleFunc("GET /api/v1/storage", s.getStorage)
	mux.HandleFunc("POST /api/v1/storage/eject", s.postStorageEject)
	mux.HandleFunc("POST /api/v1/storage/format", s.postStorageFormat)
	mux.HandleFunc("PUT /api/v1/uplinks/{name}", s.putUplink)
	mux.HandleFunc("GET /api/v1/modems", s.getModems)
	mux.HandleFunc("PUT /api/v1/modems/{id}", s.putModem)
	mux.HandleFunc("POST /api/v1/modems/{id}/{action}", s.postModem)
	mux.HandleFunc("PUT /api/v1/input", s.putInput)
	mux.HandleFunc("GET /api/v1/sources", s.getSources)
	mux.HandleFunc("POST /api/v1/service/{action}", s.serviceAction)
	mux.HandleFunc("POST /api/v1/live", s.liveOffer)
	mux.HandleFunc("GET /api/v1/mixer", s.getMixer)
	mux.HandleFunc("POST /api/v1/mixer", s.postMixer)
	mux.HandleFunc("GET /api/v1/mixer/{image}", s.mixerImage)
	mux.HandleFunc("GET /api/v1/network", s.getNetwork)
	mux.HandleFunc("GET /api/v1/network/credentials", s.getNetworkCredentials)
	mux.HandleFunc("GET /api/v1/network/qr/{what}", s.networkQR)
	mux.HandleFunc("PUT /api/v1/network", s.putNetwork)
	mux.HandleFunc("PUT /api/v1/network/ap", s.putNetworkAP)
	mux.HandleFunc("GET /api/v1/network/wifi", s.getNetworkWiFi)
	mux.HandleFunc("PUT /api/v1/network/wifi", s.putNetworkWiFi)
	mux.HandleFunc("GET /api/v1/network/wifi/scan", s.getNetworkWiFiScan)
	mux.HandleFunc("POST /api/v1/network/wifi/connect", s.postNetworkWiFiConnect)
	mux.HandleFunc("POST /api/v1/network/wifi/forget", s.postNetworkWiFiForget)
	mux.HandleFunc("GET /api/v1/bluetooth", s.getBluetooth)
	mux.HandleFunc("PUT /api/v1/bluetooth", s.putBluetooth)
	mux.HandleFunc("POST /api/v1/bluetooth/scan", s.postBluetoothScan)
	mux.HandleFunc("POST /api/v1/bluetooth/visible", s.postBluetoothVisible)
	mux.HandleFunc("POST /api/v1/bluetooth/devices/{address}/{action}", s.postBluetoothDevice)
	mux.HandleFunc("PUT /api/v1/bluetooth/devices/{address}", s.putBluetoothDevice)
	mux.HandleFunc("PUT /api/v1/network/lan", s.putNetworkLAN)
	mux.HandleFunc("POST /api/v1/cameras", s.postCamera)
	mux.HandleFunc("PATCH /api/v1/cameras/{id}", s.patchCamera)
	mux.HandleFunc("DELETE /api/v1/cameras/{id}", s.deleteCamera)
	mux.HandleFunc("GET /api/v1/cameras/{id}/qr.svg", s.cameraQR)
	mux.HandleFunc("GET /api/v1/cloud", s.getCloud)
	mux.HandleFunc("POST /api/v1/cloud/pair", s.pairCloud)
	mux.HandleFunc("DELETE /api/v1/cloud", s.unlinkCloud)
	s.registerUpdate(mux)
	mux.HandleFunc("GET /api/v1/info", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Info.read()) })
	// Device settings, setup wizard and source slots (SETUP.md)
	mux.HandleFunc("GET /api/v1/device", s.getDevice)
	mux.HandleFunc("PUT /api/v1/device", s.putDevice)
	mux.HandleFunc("GET /api/v1/device/timezones", s.getTimezones)
	mux.HandleFunc("GET /api/v1/setup", s.getSetup)
	mux.HandleFunc("POST /api/v1/setup/password", s.postSetupPassword)
	mux.HandleFunc("POST /api/v1/setup/step", s.postSetupStep)
	mux.HandleFunc("POST /api/v1/setup/complete", s.postSetupComplete)
	mux.HandleFunc("POST /api/v1/setup/restart", s.postSetupRestart)
	mux.HandleFunc("GET /api/v1/sources/slots", s.getSlots)
	mux.HandleFunc("PUT /api/v1/sources/slots", s.putSlots)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, r, 404, "not_found", "common.unknown_route") })
	if s.Web != nil {
		static := http.FileServerFS(s.Web)
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// embed.FS has no dates: without this, a phone could keep an old version
			w.Header().Set("Cache-Control", "no-cache")
			static.ServeHTTP(w, r)
		}))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("eirl-encoderd: API at /api/v1 (built without the web interface)\n"))
		})
	}
	return s.logRequests(securityHeaders(s.withLang(s.guard(mux))))
}

// guard: the checks of every web request, in order (LAN only, Host, authentication, CSRF).
func (s *Server) guard(next http.Handler) http.Handler {
	return s.lanOnly(s.checkHost(s.requireAuth(csrf(next))))
}

// Guard wraps h with the same checks and language negotiation as the web API, for routes served
// outside the route table (the simulator's fake /update).
func (s *Server) Guard(h http.Handler) http.Handler { return s.withLang(s.guard(h)) }

// hostname: the device hostname now, lowercase (System, else the OS).
func (s *Server) hostname() string {
	if s.System != nil {
		return strings.ToLower(s.System.Hostname())
	}
	h, _ := os.Hostname()
	return strings.ToLower(h)
}

// --- middlewares ---------------------------------------------------------------

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// form-action 'none': if the JS failed, a native form submission would not put the secrets in
		// a URL
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'none'; base-uri 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// lanOnly: defence in depth in case someone opens the port on the router. "Local network" = private
// ranges, loopback, link-local and also the subnets of the device's own interfaces: on an IPv6 network
// the phone comes in with its GLOBAL address of the same /64.
func (s *Server) lanOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)
		if ip == nil || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || s.lan.contains(ip)) {
			fail(w, r, 403, "lan_only", "access.lan_only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// lanNets: subnets of the interfaces, refreshed every 30 s (DHCP/SLAAC change them).
type lanNets struct {
	mu   sync.Mutex
	nets []*net.IPNet
	at   time.Time
}

func (l *lanNets) contains(ip net.IP) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.at) > 30*time.Second {
		l.nets, l.at = nil, time.Now()
		addrs, _ := net.InterfaceAddrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
				l.nets = append(l.nets, n)
			}
		}
	}
	for _, n := range l.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// checkHost: only literal IPs or the device's own names. A foreign site that re-resolves its domain to
// the Jetson's IP (DNS rebinding) arrives with its own Host and is refused. The current hostname is
// read on each request: a rename from the settings or the wizard works without a restart.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Host
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		h = strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
		if net.ParseIP(h) == nil && !s.hosts[h] {
			if cur := s.hostname(); cur == "" || (h != cur && h != cur+".local") {
				fail(w, r, 403, "bad_host", "access.bad_host")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// limitKey: login attempts are limited per IP; with IPv6, per /64 (a computer changes its address
// within its prefix whenever it likes).
func limitKey(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return ip.String()
}

func (s *Server) sessionValid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && s.Auth.Valid(c.Value, time.Now())
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// openWithoutPassword: what the web serves before an admin password exists (SETUP.md §4): the wizard
// and the language. The rest answers 503 setup_required: never an open device.
var openWithoutPassword = map[string]bool{
	"GET /api/v1/setup": true, "POST /api/v1/setup/password": true, "POST /api/v1/setup/step": true,
	"POST /api/v1/setup/complete": true,
	// PUT /device: only the language (putDevice checks it)
	"GET /api/v1/device": true, "PUT /api/v1/device": true, "GET /api/v1/device/timezones": true,
}

// openWithPassword: what works without a session once the password exists.
var openWithPassword = map[string]bool{
	"POST /api/v1/login": true, "GET /api/v1/setup": true,
	// the rescue credential: a new password with the setup code (MANAGER.md §6)
	"POST /api/v1/password/reset": true,
	// answers 409 password_exists: the web learns that it has to sign in
	"POST /api/v1/setup/password": true,
}

// requireAuth: without an admin password only the wizard is served (never an open device); with it,
// the whole API except the login and the public part of the wizard needs a session. The static web is
// public: without a session it can only draw the sign-in form or the wizard.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		route := r.Method + " " + r.URL.Path
		if s.Auth.LoadErr != nil {
			// admin.hash exists but cannot be read: not "no password" (the wizard would replace it)
			fail(w, r, 503, "password_unreadable", "access.password_unreadable")
			return
		}
		if !s.Auth.Configured() {
			switch {
			case openWithoutPassword[route]:
				next.ServeHTTP(w, r)
			case route == "GET /api/v1/session":
				fail(w, r, 401, "setup_required", "access.setup_required")
			default:
				fail(w, r, 503, "setup_required", "access.setup_required")
			}
			return
		}
		if openWithPassword[route] {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || !s.Auth.Valid(c.Value, time.Now()) {
			fail(w, r, 401, "unauthorized", "access.unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrf: besides SameSite=Strict, every mutation needs JSON (a form on another site cannot send it
// without CORS) and, if the browser sends them, Sec-Fetch-Site/Origin of the device itself.
func csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			fail(w, r, 403, "csrf", "access.csrf")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if o, err := urlHost(origin); err != nil || o != r.Host {
				fail(w, r, 403, "csrf", "access.csrf")
				return
			}
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			fail(w, r, 415, "content_type", "access.content_type")
			return
		}
		limit := int64(maxBody)
		if r.URL.Path == "/api/v1/live" {
			limit = 64 << 10 // a Chrome SDP offer with audio and video is about 6 KB
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

func urlHost(origin string) (string, error) {
	_, rest, ok := strings.Cut(origin, "://")
	if !ok {
		return "", errors.New("origin")
	}
	return rest, nil
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests: method, path and code. Never bodies or queries (they could carry secrets).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "code", rec.code, "ms", time.Since(start).Milliseconds(), "ip", remoteIP(r).String())
	})
}

// --- helpers ----------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// apiError answers {"error": kind, "message": msg} with msg as is (already in the right language; fail
// renders a catalog key).
func apiError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]string{"error": kind, "message": msg})
}

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
