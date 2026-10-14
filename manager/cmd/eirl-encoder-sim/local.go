package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/api"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/audiolevel"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/hw"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
	"github.com/enhanced-irl/eirl-encoder/manager/web"
)

// --- local web and API (-http): the real api.Server over a simulated device -----------------------------------
//
// For developing the web (and the screen, with -local-socket) without the board: the same handlers, validation,
// security checks and messages as eirl-encoderd, with fakes underneath: the stream unit, the HDMI signal, an
// engine on a Unix socket (mixer state, slots with a catalogue, live commands), the clock and hostname
// (device.MemSystem), the EnhancedIRL linking (a code that is approved after 30 s), the updater (/update, a state
// machine idle → available 0.2.0 → downloading → ready → installing → idle) and the board's access point (network.go).

// simPassword: the admin password of the simulator outside -fresh (it is a simulator: not a secret).
const simPassword = "eirl-sim-1234"

type localOpts struct {
	addr, socket, dataDir, webDir, hwID string
	fresh                               bool
}

func runLocal(ctx context.Context, o localOpts, log *slog.Logger) error {
	if o.dataDir == "" {
		d, err := os.MkdirTemp("", "eirl-sim-data")
		if err != nil {
			return err
		}
		o.dataDir = d
	}
	if err := os.MkdirAll(o.dataDir, 0o750); err != nil {
		return err
	}
	hashPath := filepath.Join(o.dataDir, "admin.hash")
	if o.fresh {
		for _, f := range []string{"admin.hash", "device.json", "slots.json", "setup-code", "network.json", "ap.psk",
			"cameras.json", "cameras-public.json", "media-api.key", "ap-state.json"} {
			os.Remove(filepath.Join(o.dataDir, f))
		}
	} else if _, err := os.Stat(hashPath); err != nil {
		h, err := auth.HashPassword(simPassword)
		if err != nil {
			return err
		}
		if err := auth.WriteHash(hashPath, h); err != nil {
			return err
		}
	}
	sys := device.NewMemSystem("eirl-sim")
	sys.ZoneIDs = simZones
	_, err := os.Stat(hashPath)
	st, origin, err := device.Open(filepath.Join(o.dataDir, "device.json"), err == nil, sys.Hostname(), time.Now())
	if err != nil {
		log.Warn("device.json", "origin", origin, "err", err)
	}
	codePath := filepath.Join(o.dataDir, "setup-code")
	if _, err := device.EnsureCode(codePath); err != nil {
		return err
	}
	code, _ := device.ReadCode(codePath)
	// A test destination, so that Start streaming works (and the on_air rule of the slots can be tried)
	os.WriteFile(filepath.Join(o.dataDir, "default.env"), []byte("SRT_URL=srt://relay.example.com:5000\nSRT_MODE=caller\nOUTPUT=off\n"), 0o644)
	os.WriteFile(filepath.Join(o.dataDir, "VERSION"), []byte("0.1.0\n"), 0o644)
	os.WriteFile(filepath.Join(o.dataDir, "ENGINE"), []byte("0.1.0 go-sim\n"), 0o644)

	// The engine: a Unix socket in a short path (sun_path is ~104 bytes on macOS)
	sockDir, err := os.MkdirTemp("/tmp", "eirlsim")
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); os.RemoveAll(sockDir) }()
	eng, err := startSimEngine(ctx, sockDir, log)
	if err != nil {
		return err
	}

	var webFS fs.FS = web.FS()
	if o.webDir != "" {
		webFS = os.DirFS(o.webDir) // edits show up on reload, no rebuild
	}
	unit := &simUnit{state: "active", since: time.Now()}
	srv := &api.Server{
		Unit: unit, Board: simBoard{}, Auth: auth.New(hashPath),
		Config:       &config.Store{DefaultPath: filepath.Join(o.dataDir, "default.env"), AppPath: filepath.Join(o.dataDir, "engine.env")},
		StatsPath:    filepath.Join(o.dataDir, "srt-stats.json"),
		RunStatePath: filepath.Join(sockDir, "state"),
		Mixer:        eng.client(),
		Meter:        &audiolevel.Meter{Addr: "127.0.0.1:0"},
		Info:         api.Info{Version: "0.1.0-sim", Web: web.Hash()},
		Web:          webFS,
		Log:          log,
		Hostnames:    []string{"eirl-sim", "eirl-sim.local"},
		Cloud:        &simCloud{},
		Device:       st, System: sys, Zones: &device.Zones{Sys: sys}, SetupCodePath: codePath,
		Versions:  api.Versions{ReleaseFile: filepath.Join(o.dataDir, "VERSION"), EngineFile: filepath.Join(o.dataDir, "ENGINE")},
		SlotsPath: filepath.Join(o.dataDir, "slots.json"),
	}
	// The board's access point (a fake eirl-netsetup), with its name and password from the first start like the app
	if o.hwID == "" {
		o.hwID = simHardwareID(o.dataDir)
	}
	srv.Network = simNetwork(o.dataDir, o.hwID)
	if _, err := srv.Network.EnsureNetwork(); err != nil {
		log.Warn("access point: could not write its name and password", "err", err)
	}
	// The run state the API reads: the mixer is on (live output, slots)
	os.WriteFile(srv.RunStatePath, []byte("OUTPUT=off\nSOURCE=hdmi\nMIXER=1\nTRANSPORT=srt\n"), 0o644)
	// A restart of the "stream" makes the engine take OUTPUT from what is saved, like the real one at start
	unit.onStart = func() {
		if cfg, err := srv.Config.Load(); err == nil {
			eng.setOutput(cfg.Output == "on")
		}
	}
	unit.onStart()
	upd := newSimUpdate(eng)
	apiHandler := srv.Handler()
	mux := http.NewServeMux()
	// The fake updater, behind the same checks as the API (session, CSRF, LAN, language)
	mux.Handle("/api/v1/update", srv.Guard(upd.handler(st)))
	mux.Handle("/api/v1/update/", srv.Guard(upd.handler(st)))
	mux.Handle("/", apiHandler)
	hs := &http.Server{Addr: o.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); hs.Close() }()
	if o.socket != "" {
		lmux := http.NewServeMux()
		lmux.Handle("/api/v1/update", upd.handler(st))
		lmux.Handle("/api/v1/update/", upd.handler(st))
		lmux.Handle("/", srv.LocalHandler())
		os.Remove(o.socket)
		ln, err := net.Listen("unix", o.socket)
		if err != nil {
			return err
		}
		ls := &http.Server{Handler: lmux, ConnContext: api.LocalConnContext, ReadHeaderTimeout: 10 * time.Second}
		go func() { <-ctx.Done(); ls.Close(); os.Remove(o.socket) }()
		go ls.Serve(ln)
		log.Info("local socket", "path", o.socket)
	}
	pw := simPassword
	if !srv.Auth.Configured() {
		pw = "(none: run the wizard)"
	}
	log.Info("local web", "url", "http://"+displayAddr(o.addr), "data", o.dataDir, "password", pw, "setupCode", code,
		"fresh", o.fresh, "language", st.Language())
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func displayAddr(a string) string {
	if h, p, err := net.SplitHostPort(a); err == nil && (h == "" || h == "0.0.0.0") {
		return "127.0.0.1:" + p
	}
	return a
}

// simZones: a short but representative list of time zones.
var simZones = []string{"UTC", "Europe/Madrid", "Europe/Paris", "Europe/London", "Europe/Lisbon", "Europe/Berlin",
	"Europe/Brussels", "Europe/Zurich", "Atlantic/Canary", "Africa/Casablanca", "America/New_York", "America/Chicago",
	"America/Denver", "America/Los_Angeles", "America/Mexico_City", "America/Bogota", "America/Lima",
	"America/Santiago", "America/Argentina/Buenos_Aires", "America/Sao_Paulo", "America/Montreal", "Asia/Tokyo",
	"Asia/Seoul", "Asia/Singapore", "Asia/Dubai", "Australia/Sydney", "Pacific/Auckland"}

// --- fakes of the board -----------------------------------------------------------------------------------------

// simUnit: eirl-engine.service always running (start/stop/restart change its state for a moment).
type simUnit struct {
	mu      sync.Mutex
	state   string
	since   time.Time
	onStart func()
}

func (u *simUnit) Status(context.Context) (service.Status, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return service.Status{State: u.state, Started: u.since}, nil
}

func (u *simUnit) Control(_ context.Context, action string) error {
	u.mu.Lock()
	switch action {
	case "stop":
		u.state = "inactive"
	default:
		u.state, u.since = "active", time.Now()
	}
	start := u.onStart
	u.mu.Unlock()
	if action != "stop" && start != nil {
		start()
	}
	return nil
}

func (u *simUnit) Tail(context.Context, int) ([]service.Entry, error) {
	e, _ := service.ParseJournal([]byte(`{"MESSAGE":"[sim] engine started"}`))
	return []service.Entry{e}, nil
}

func (u *simUnit) Follow(ctx context.Context, _ func(service.Entry)) error {
	<-ctx.Done()
	return nil
}

// simBoard: HDMI 1080p30 with audio.
type simBoard struct{}

func (simBoard) Signal() hw.Signal {
	return hw.Signal{Present: true, Width: 1920, Height: 1080, FPS: 30, Supported: true}
}
func (simBoard) Audio() hw.Audio   { return hw.Audio{State: "playing", AuStatus0: "0x61"} }
func (simBoard) System() hw.System { return hw.System{CPUPct: []int{20, 10, 5, 5}} }

// simCloud: linking with EnhancedIRL without a backend: a code, approved 30 s later.
type simCloud struct {
	mu      sync.Mutex
	view    cloud.View
	started time.Time
}

func (c *simCloud) View() cloud.View {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.view.State == "pairing" && time.Since(c.started) > 30*time.Second {
		online := true
		c.view = cloud.View{State: "linked", AccountLabel: "sim@example.com", Online: &online}
	}
	if c.view.State == "" {
		c.view.State = "unlinked"
	}
	return c.view
}

func (c *simCloud) Pair(context.Context) (cloud.View, error) {
	if v := c.View(); v.State == "linked" {
		return v, cloud.ErrAlreadyLinked
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := time.Now().Add(10 * time.Minute)
	c.started = time.Now()
	c.view = cloud.View{State: "pairing", Code: "SIMX-2345", ExpiresAt: &exp,
		LinkURL: "https://app.dev.enhancedirl.com/encoders/link?pairing=SIMX-2345"}
	return c.view, nil
}

func (c *simCloud) Unlink(context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.view = cloud.View{State: "unlinked"}
	return true
}
