// eirl-encoder-sim: a simulated encoder, in two modes.
//
// Cloud mode (default): to develop the "Encoders" section of the EnhancedIRL app without the Jetson and for the
// level-A E2E tests. It uses the SAME cloud client as eirl-encoderd (internal/cloud): pairing, channel, reconnection,
// config.set validation (D9 included) and commands; only the device is fake (phases, signal, SRT, uplinks, thumbnail).
//
//	eirl-encoder-sim -api https://api.dev.enhancedirl.com -app https://app.dev.enhancedirl.com
//	eirl-encoder-sim -api http://localhost:3000 -state /tmp/sim1.json -json   # E2E: one JSON line per event
//	eirl-encoder-sim -state /tmp/sim1.json -unlink                             # DELETE /encoders/self and remove the state
//
// The device secret (identity) goes in <state>.identity and survives -unlink, like on the Jetson: to simulate a
// reinstalled device, delete it. Without a link saved in -state, it asks for a code and prints it
// (`pairing code=XXXX-XXXX link=…`); once confirmed in the app, it opens the channel. With a link, it connects.
//
// Local mode (-http): the device's own web and API (internal/api, the real handlers) over fakes, to develop the web
// and the screen without the board (local.go): device settings, setup wizard (-fresh: an unconfigured device),
// source slots with a catalogue, EnhancedIRL linking (approved after 30 s) and a fake updater.
//
//	eirl-encoder-sim -http 127.0.0.1:8080 -fresh              # wizard from scratch (the setup code is in the log)
//	eirl-encoder-sim -http 127.0.0.1:8080 -web web/static     # configured (password eirl-sim-1234), web from disk
//	eirl-encoder-sim -http :8080 -local-socket /tmp/eirl-api.sock -data /tmp/eirl-sim
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
)

var version = "dev"

func main() {
	apiBase := flag.String("api", "https://api.dev.enhancedirl.com", "EnhancedIRL backend (http:// allowed: it is a simulator)")
	appBase := flag.String("app", "https://app.dev.enhancedirl.com", "EnhancedIRL app (pairing link)")
	statePath := flag.String("state", "eirl-encoder-sim.json", "saved link (like /etc/eirl-encoder/cloud.json; 0600)")
	identityPath := flag.String("identity", "", "device secret (like /etc/eirl-encoder/identity; 0600, kept when unlinking); default <state>.identity")
	hwID := flag.String("hardware-id", "", "hardwareId (16-64 hex); default, a stable one derived from -state (local mode: from -data)")
	name := flag.String("name", "", "name proposed when linking (optional)")
	model := flag.String("model", "eirl-encoder-sim", "model announced")
	signalFlap := flag.Duration("signal-flap", 0, "how often the HDMI signal is lost for 10 s (0 = never)")
	links := flag.Int("uplinks", 1, "simulated internet uplinks (1-4)")
	jsonLogs := flag.Bool("json", false, "JSON logs on stdout (for E2E)")
	unlink := flag.Bool("unlink", false, "unlink (DELETE /encoders/self), remove -state and exit")
	pairOnly := flag.Bool("pair-and-exit", false, "exit as soon as it is linked (E2E: link and go on with another process)")
	legacy := flag.Bool("legacy", false, "imitate a device older than mixer F3: rehearsal phase, Start/Rehearsal restart and stop stops the service")
	// Local mode (local.go)
	httpAddr := flag.String("http", "", "serve the device's local web and API on this address (local mode; e.g. 127.0.0.1:8080)")
	fresh := flag.Bool("fresh", false, "local mode: an unconfigured device (no admin password, wizard to run)")
	localSock := flag.String("local-socket", "", "local mode: also serve the screen's local socket at this path")
	dataDir := flag.String("data", "", "local mode: directory for device.json, slots.json, admin.hash… (default a new temporary one)")
	updScenario := flag.String("update", "", "local mode: how the fake updater starts: metered (0.2.0 waits for Wi-Fi to download), restart (on 0.2.0, waiting for a restart), sysfailed (its system layer failed)")
	webDir := flag.String("web", "", "local mode: serve the web from this directory instead of the embedded one (e.g. web/static)")
	flag.Parse()

	var h slog.Handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	if *jsonLogs {
		h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	log := slog.New(h)
	if *httpAddr != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := runLocal(ctx, localOpts{addr: *httpAddr, socket: *localSock, dataDir: *dataDir, webDir: *webDir, hwID: *hwID, fresh: *fresh,
			update: *updScenario}, log); err != nil {
			fail(log, "local mode", err)
		}
		return
	}
	if err := cloud.ValidateBase(*apiBase, true); err != nil {
		fail(log, "-api", err)
	}
	if *hwID == "" {
		abs, _ := filepath.Abs(*statePath)
		host, _ := os.Hostname()
		sum := sha256.Sum256([]byte("eirl-encoder-sim:" + host + ":" + abs))
		*hwID = hex.EncodeToString(sum[:8])
	}
	if *identityPath == "" {
		*identityPath = *statePath + ".identity"
	}
	if *links < 1 || *links > 4 {
		*links = 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dev := newSimDevice(log, *signalFlap, *links, *legacy)
	dev.info = cloud.DeviceInfo{
		HardwareID: *hwID, Model: *model, FirmwareVersion: "sim-" + version, Name: *name,
		Capabilities: []string{cloud.CapSRTLA, cloud.CapABR, cloud.CapEDID, cloud.CapThumbnail, cloud.CapRTMP, cloud.CapRTMPS,
			cloud.CapRIST, cloud.CapUDP, cloud.CapH265, cloud.CapDestinations},
	}
	cl := &cloud.Client{
		APIBase: *apiBase, AppBase: *appBase, State: &cloud.StateFile{Path: *statePath, AllowHTTP: true},
		Identity: &cloud.IdentityFile{Path: *identityPath},
		Device:   dev, Log: log, UserAgent: "eirl-encoder-sim/" + version,
	}
	dev.client = cl
	if err := cl.Init(ctx); err != nil {
		fail(log, "state", err)
	}

	if *unlink {
		notified := cl.Unlink(ctx)
		log.Info("unlinked", "cloudNotified", notified)
		return
	}

	go dev.run(ctx)
	go cl.Run(ctx)

	if cl.View().State != "linked" {
		v, err := cl.Pair(ctx)
		if err != nil {
			fail(log, "pairing", err)
		}
		log.Info("pairing", "code", v.Code, "link", v.LinkURL, "expiresAt", v.ExpiresAt, "hardwareId", *hwID)
	} else {
		log.Info("linked", "account", cl.View().AccountLabel, "hardwareId", *hwID)
	}

	// Watch the link state and report it (the E2E app waits for these lines)
	last := ""
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		v := cl.View()
		cur := v.State
		if v.State == "linked" && v.Online != nil && *v.Online {
			cur = "online"
		}
		if cur == last {
			continue
		}
		last = cur
		switch cur {
		case "linked":
			log.Info("linked", "account", v.AccountLabel, "halted", v.Halted)
			if *pairOnly {
				return
			}
		case "online":
			log.Info("online", "account", v.AccountLabel)
		case "unlinked":
			log.Info("unlinked", "reason", v.LastError)
			if v.LastError != "" { // denied, expired or unlinked from the app: nothing else to do
				return
			}
		}
	}
}

func fail(log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	fmt.Fprintln(os.Stderr, what+":", err)
	os.Exit(1)
}
