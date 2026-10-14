// eirl-encoderd: the management app of the EnhancedIRL HDMI → SRT encoder (see MANAGER.md and SETUP.md).
//
//	eirl-encoderd [flags]                 serves the API (and the web) on the LAN
//	eirl-encoderd passwd [-hash path]     sets the admin password (read from stdin)
//	eirl-encoderd probe [-device …]       what to capture a USB source with (used by the engine; see probe.go)
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/api"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/audiolevel"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/auth"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/hw"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/live"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/wifi"
)

var version = "dev" // -ldflags "-X main.version=…"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "passwd" {
		os.Exit(passwd(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "probe" {
		os.Exit(probe(os.Args[2:]))
	}
	listen := flag.String("listen", ":80", "HTTP address")
	confDir := flag.String("config-dir", "/etc/eirl-encoder", "the app's configuration (engine.env, admin.hash, device.json, slots.json)")
	defEnv := flag.String("default-env", "/etc/default/eirl-engine", "base configuration of eirl-engine (read only)")
	runDir := flag.String("run-dir", "/run/eirl-engine", "eirl-engine tmpfs: state and SRT statistics")
	uplinksFile := flag.String("uplinks", "/run/eirl-uplinks/available", "internet uplinks for SRTLA (left by the NetworkManager dispatcher)")
	unit := flag.String("unit", "eirl-engine.service", "systemd unit of the stream")
	levels := flag.String("levels", "127.0.0.1:5005", "UDP of the level meter (the script's LEVELS_PORT)")
	liveVideo := flag.String("live-video", "127.0.0.1:5006", "RTP H.264 of the live preview (the script's LIVE_VPORT)")
	liveAudio := flag.String("live-audio", "127.0.0.1:5008", "RTP Opus of the live preview (the script's LIVE_APORT)")
	liveMax := flag.Int("live-max", 3, "live previews at once (0 = off)")
	i2c := flag.String("i2c", "/dev/i2c-8", "I2C bus of the TC358743 (CAM1)")
	// Real hosts: prod api.enhancedirl.com / app.enhancedirl.com · dev api.dev.enhancedirl.com / app.dev.enhancedirl.com
	cloudAPI := flag.String("cloud-api", "https://api.enhancedirl.com", "EnhancedIRL backend to link the device with (empty = no linking)")
	cloudApp := flag.String("cloud-app", "https://app.enhancedirl.com", "EnhancedIRL app (link to confirm the pairing)")
	thumb := flag.String("thumb", "", "JPEG thumbnail left by the engine (default <run-dir>/thumb.jpg)")
	netsetup := flag.String("netsetup", "/usr/local/sbin/eirl-netsetup", "root helper of the network cameras (without it, no network cameras)")
	camerasMax := flag.Int("cameras-max", netcfg.DefaultMax, "network cameras at most (1-8)")
	apState := flag.String("ap-state", "/run/eirl-ap/state.json", "access point state (left by eirl-netsetup)")
	localSock := flag.String("local-socket", "/run/eirl-encoder/api.sock", "Unix socket of the API for the engine's screen (MANAGER.md §3, \"Local socket\"; empty = none)")
	// GET /device version (SETUP.md §2): the release and the installed engine
	releaseFile := flag.String("device-version-file", "/usr/lib/eirl-encoder/VERSION", "release version file (GET /device)")
	engineFile := flag.String("engine-version-file", "/usr/lib/eirl-encoder/engine/VERSION", "installed engine version file (GET /device)")
	// Over-the-air updates (UPDATES.md §8)
	updateURL := flag.String("update-url", update.DefaultManifestURL, "manifest of the latest release (empty = no updates)")
	updateDir := flag.String("update-dir", "/var/lib/eirl-encoder/update", "where the downloaded update is kept (the unit's StateDirectory)")
	updateCheck := flag.Bool("update-check", true, "check for updates automatically (every 6 h, only when online)")
	flag.Parse()
	if *thumb == "" {
		*thumb = filepath.Join(*runDir, "thumb.jpg")
	}

	// journald adds the time: none in the text
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))

	// A configuration file someone else created under the app's name is ignored (safefile): say so
	safefile.OnUntrusted = func(path, reason string) {
		log.Warn("ignoring a configuration file that is not the app's", "path", path, "reason", reason)
	}
	srv := &api.Server{
		Unit:         service.Unit{Name: *unit},
		Board:        &hw.Board{Chip: "tc358743", I2CBus: *i2c, Addr: 0x0f},
		Auth:         auth.New(filepath.Join(*confDir, "admin.hash")),
		Config:       &config.Store{DefaultPath: *defEnv, AppPath: filepath.Join(*confDir, "engine.env")},
		StatsPath:    filepath.Join(*runDir, "srt-stats.json"),
		RunStatePath: filepath.Join(*runDir, "state"),
		ABRPath:      filepath.Join(*runDir, "abr"),
		Uplinks: &uplinks.Reader{
			Available: *uplinksFile,
			InUse:     filepath.Join(*runDir, "uplinks"),
			Disabled:  filepath.Join(*confDir, "uplinks-disabled"),
			// The Wi-Fi client without internet (the app writes it: MonitorWiFi; WIFI.md §13)
			NoInternet: noInternetFile,
			SysNet:     "/sys/class/net",
		},
		Sources: sources.New(),
		Mixer: &mixer.Client{Sock: filepath.Join(*runDir, "mixer.sock"), StatePath: filepath.Join(*runDir, "mixer.json"),
			RunDir: *runDir},
		VideoPath: filepath.Join(*runDir, "video"),
		Meter:     &audiolevel.Meter{Addr: *levels},
		Info:      api.Info{Version: version, Web: webHash()},
		Web:       webFS(),
		Log:       log,
	}
	setupDevice(srv, *confDir, *releaseFile, *engineFile, log)
	var previewWatch api.PreviewWatch
	if *liveMax > 0 {
		relay, err := live.New(*liveVideo, *liveAudio, *liveMax, log)
		if err != nil {
			log.Error("live preview off", "err", err)
		} else {
			srv.Live = relay
			defer relay.Close()
			previewWatch = relay
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	setupUpdates(ctx, srv, *updateURL, *updateDir, *updateCheck, *runDir, log)
	// Network cameras (F7, NETWORK_SOURCES.md §6): only with the root helper installed
	if _, err := os.Stat(*netsetup); err == nil {
		srv.Network = &network.Manager{
			Dir: *confDir, StatePath: *apState, MediaAPI: fmt.Sprintf("http://127.0.0.1:%d", netcfg.APIPort),
			HardwareID: cloud.DeviceID("/etc/machine-id"), Max: *camerasMax,
			Apply:      func(ctx context.Context) error { return network.SystemdApply(ctx, *apState) },
			Write:      func(p string, b []byte, mode os.FileMode) error { return config.WriteAtomic(p, b, mode) },
			ScreenFile: "/run/eirl-encoder/wifi.json",
		}
		// The access point's name and password from the first start (the wizard shows the name and lets it be
		// changed); never rewritten if they exist
		if created, err := srv.Network.EnsureNetwork(); err != nil {
			log.Warn("access point: could not write its name and password", "err", err)
		} else if created {
			log.Info("access point: name and password generated")
		}
		// The Wi-Fi client (WIFI.md §4): wlan0 through NetworkManager. Internet is checked against the cloud API
		if _, err := os.Stat("/sys/class/net/wlan0"); err == nil {
			wc := wifi.New("wlan0")
			wc.OwnSSID, wc.InternetURL = srv.Network.SSID, internetURL(srv.Config, *cloudAPI)
			srv.Network.Client = wc
			// Without internet (a captive portal), out of the SRTLA bonding: the engine reads this file (WIFI.md §13)
			srv.Network.NoInternetFile = noInternetFile
			go srv.Network.MonitorWiFi(ctx, log.Info)
			// "Connect to a Wi-Fi" as network.json says: NetworkManager does not remember it after a restart
			go func() {
				if err := srv.Network.InitWiFi(ctx); err != nil {
					log.Warn("Wi-Fi client", "err", err)
				}
			}()
		}
		if srv.Network.Wanted() {
			// In case eirl-ap.service failed at boot (NetworkManager without wlan0 yet): idempotent
			go func() {
				if err := srv.Network.Reapply(ctx); err != nil {
					log.Warn("cameras' network", "err", err)
				}
			}()
		}
		// And if it stays in error (or up without being asked), again every 2 min
		go srv.Network.Retry(ctx, 2*time.Minute, func(err error) { log.Warn("cameras' network: retry", "err", err) })
	}
	if *cloudAPI != "" {
		cl, err := newCloud(ctx, srv, *cloudAPI, *cloudApp, *confDir, *thumb, *defEnv, *listen, *unit, log)
		if err != nil {
			log.Error("linking with EnhancedIRL off", "err", err)
		} else {
			srv.Cloud = cl
			srv.Config.ManagedBy = cl.ManagedBy
			go cl.Run(ctx)
		}
	}
	// Streaming from the touch screen is saved in the configuration, with or without open tabs; the
	// screen's Power menu restarts or powers off the board (exact sudoers commands)
	srv.Power = service.Unit{Name: *unit}
	go srv.FollowMixer(ctx)
	if previewWatch != nil {
		go srv.FollowPreview(ctx, previewWatch) // with nobody watching, the engine does not encode the preview
	}
	switch {
	case srv.Auth.LoadErr != nil:
		log.Error("cannot read the admin password: the API answers 503", "err", srv.Auth.LoadErr)
	case !srv.Auth.Configured():
		log.Warn("no admin password: the web serves only the setup wizard (SETUP.md §4) until the screen or the web sets one, or `sudo -u eirl-encoder eirl-encoderd passwd`")
	}
	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout: a slow client does not hold the connection. /events removes it
		// (SetReadDeadline) because they are long streams; for the same reason there is no WriteTimeout.
		ReadTimeout:    20 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	if *localSock != "" {
		// Without it, the screen keeps the old way (wifi.json, no settings): no reason not to start
		if err := serveLocal(ctx, srv, *localSock, log); err != nil {
			log.Warn("local socket off", "path", *localSock, "err", err)
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("server", "err", err)
		os.Exit(1)
	}
	log.Info("listening", "addr", *listen, "version", version)
	// Maximum connections at once: opening thousands would exhaust the 64 MB MemoryLimit
	if err := hs.Serve(limitListener(ln, 64)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}

// noInternetFile: the Wi-Fi client without internet, out of the SRTLA bonding (WIFI.md §13). The app writes it
// (network.MonitorWiFi), the engine reads it (UPLINKS_NOINTERNET, the same path) and /status shows it
const noInternetFile = "/run/eirl-encoder/uplinks-nointernet"

// internetURL: where the Wi-Fi client checks its way out to the internet (WIFI.md §13): the /ping of the relay the
// stream goes to if it is a production one (HTTPS with a real certificate: "do I reach where I stream?"; an API outage
// does not drop an uplink that works), else the cloud API (or nothing, without it). The dev relay has no /ping: the API.
func internetURL(cfg *config.Store, cloudAPI string) func() string {
	return func() string {
		if st, err := cfg.Load(); err == nil {
			if h := config.HostOf(st.URL); strings.HasSuffix(h, ".relay.enhancedirl.com") {
				return "https://" + h + "/ping"
			}
		}
		return cloudAPI
	}
}

// serveLocal: the API for the engine's screen over a Unix socket (api.LocalHandler). 0660: the eirl-encoder group
// opens it (the engine has it through its unit); the directory (RuntimeDirectory, 0750) already keeps the rest out.
// A socket from a previous run is removed; anything else at that path is not.
func serveLocal(ctx context.Context, srv *api.Server, path string, log *slog.Logger) error {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return err
	}
	// No WriteTimeout: a network PUT waits until it is applied (up to 2 min, applyCtx)
	hs := &http.Server{Handler: srv.LocalHandler(), ConnContext: api.LocalConnContext,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		hs.Close()
	}()
	go func() {
		if err := hs.Serve(limitListener(ln, 8)); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("local socket", "err", err)
		}
	}()
	log.Info("local socket", "path", path)
	return nil
}

// newCloud: the cloud client with the real Jetson behind it (api.CloudDevice).
func newCloud(ctx context.Context, srv *api.Server, apiBase, appBase, confDir, thumb, defEnv, listen, unit string, log *slog.Logger) (*cloud.Client, error) {
	if err := cloud.ValidateBase(apiBase, false); err != nil {
		return nil, fmt.Errorf("-cloud-api: %w", err)
	}
	if appBase != "" {
		if err := cloud.ValidateBase(appBase, false); err != nil {
			return nil, fmt.Errorf("-cloud-app: %w", err)
		}
	}
	caps := []string{cloud.CapABR, cloud.CapEDID}
	if _, err := os.Stat("/usr/local/bin/srtla_send"); err == nil {
		caps = append(caps, cloud.CapSRTLA)
	}
	if srv.Network != nil {
		caps = append(caps, cloud.CapAP) // access point of the network cameras (F7)
		if srv.Network.Client != nil {
			caps = append(caps, cloud.CapWiFi) // the Wi-Fi client (WIFI.md §4)
		}
	}
	// Thumbnail (the engine: THUMB, THUMB_EVERY in /etc/default/eirl-engine; the app's configuration does
	// not carry them). CloudDevice announces the thumbnail capability only if there really are thumbnails.
	thumbOn, thumbEvery := true, 5
	if env, err := config.ReadEnv(defEnv); err == nil {
		thumbOn = env["THUMB"] != "0"
		if n, err := strconv.Atoi(env["THUMB_EVERY"]); err == nil && n >= 1 && n <= 3600 {
			thumbEvery = n
		}
	}
	port := 80
	if _, p, err := net.SplitHostPort(listen); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	dev := &api.CloudDevice{
		Srv: srv, Thumb: thumb, ThumbEnabled: thumbOn && thumb != "", ThumbMaxAge: 3 * time.Duration(thumbEvery) * time.Second,
		Port: port, Rebooter: service.Unit{Name: unit},
		Ident: cloud.DeviceInfo{
			HardwareID: cloud.DeviceID("/etc/machine-id"), Model: "Jetson Nano + TC358743",
			FirmwareVersion: version, Capabilities: caps,
		},
	}
	cl := &cloud.Client{
		APIBase: apiBase, AppBase: appBase, State: &cloud.StateFile{Path: filepath.Join(confDir, "cloud.json")},
		Identity: &cloud.IdentityFile{Path: filepath.Join(confDir, "identity")},
		Device:   dev, Log: log.With("component", "cloud"), UserAgent: "eirl-encoderd/" + version + " (+https://enhancedirl.com)",
		Lang: srv.DeviceLanguage, // command results in the device language
	}
	if err := cl.Init(ctx); err != nil {
		return nil, err
	}
	dev.ThumbActive() // if there is a thumbnail at start, it is announced from the first hello
	return cl, nil
}

func passwd(args []string) int {
	fs := flag.NewFlagSet("passwd", flag.ExitOnError)
	path := fs.String("hash", "/etc/eirl-encoder/admin.hash", "password file")
	fs.Parse(args)
	fmt.Fprintf(os.Stderr, "New password (min. %d characters) on stdin: ", auth.MinPasswordLen)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(os.Stderr, "\nno password")
		return 1
	}
	h, err := auth.HashPassword(strings.TrimRight(line, "\r\n"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		return 1
	}
	if err := auth.WriteHash(*path, h); err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		return 1
	}
	fmt.Fprintln(os.Stderr, "\npassword saved in", *path, "(restart eirl-encoderd so that it reads it)")
	return 0
}

// setupDevice: device settings, setup code, clock and hostname, and the source slots (SETUP.md). device.json is
// created on the first start with the migration rule: a board that already has admin.hash was set up before the
// wizard (Spanish, setup done, name = hostname); otherwise the wizard runs. A problem here is logged, never fatal.
func setupDevice(srv *api.Server, confDir, releaseFile, engineFile string, log *slog.Logger) {
	sys := device.Systemd{}
	srv.System = sys
	srv.Zones = &device.Zones{Sys: sys}
	srv.Versions = api.Versions{ReleaseFile: releaseFile, EngineFile: engineFile}
	srv.SlotsPath = filepath.Join(confDir, "slots.json")
	_, err := os.Stat(filepath.Join(confDir, "admin.hash"))
	st, origin, err := device.Open(filepath.Join(confDir, "device.json"), err == nil, sys.Hostname(), time.Now())
	switch {
	case origin == device.Invalid:
		log.Error("device.json invalid: using the migration rule until the next save", "err", err)
	case err != nil:
		log.Error("device.json", "origin", origin, "err", err)
	case origin != device.Loaded:
		log.Info("device.json created", "origin", origin, "language", st.Language())
	}
	srv.Device = st
	codePath := filepath.Join(confDir, "setup-code")
	if created, err := device.EnsureCode(codePath); err != nil {
		// Without a code the web cannot set the first password; the screen still can
		log.Error("setup code unavailable", "err", err)
	} else {
		srv.SetupCodePath = codePath
		if created {
			log.Info("setup code created", "path", codePath) // never the code itself
		}
	}
}
