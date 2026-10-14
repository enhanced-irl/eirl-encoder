package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/api"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
)

// Over-the-air updates (UPDATES.md §8): the app checks, downloads and verifies; the root helper
// (eirl-update.service) installs. Without the helper installed, /update answers 503.
const (
	updateHelper     = "/usr/local/sbin/eirl-update"
	updateHelperStat = "/var/lib/eirl-update/status.json"
)

func setupUpdates(ctx context.Context, srv *api.Server, manifestURL, dir string, auto bool, runDir string, log *slog.Logger) {
	log = log.With("component", "update")
	if manifestURL == "" {
		log.Info("updates disabled (-update-url is empty)")
		return
	}
	if _, err := os.Stat(updateHelper); err != nil {
		log.Info("updates disabled: the update helper is not installed", "path", updateHelper)
		return
	}
	keys, err := update.TrustedKeys()
	if err != nil {
		log.Error("updates disabled", "err", err)
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		log.Error("updates disabled: no update directory", "dir", dir, "err", err)
		return
	}
	if update.TestBuild() {
		log.Warn("TEST BUILD: updates are verified with a test key, not the production key", "key", update.KeyID(keys[0]))
	}
	u := update.New(update.Config{
		ManifestURL: manifestURL, Dir: dir, Keys: keys,
		Fetcher:   &update.Fetcher{UserAgent: "eirl-encoderd/" + version + " (+https://enhancedirl.com)"},
		Current:   func() string { return update.InstalledRelease("/") },
		Online:    func() bool { return update.HasDefaultRoute("/proc/net/route", "/proc/net/ipv6_route") },
		Streaming: func() bool { on, _ := update.Streaming(runDir); return on },
		// Exact sudoers rule: the helper is a systemd unit, never a child of this sandboxed process
		StartHelper: func(ctx context.Context) error {
			out, err := exec.CommandContext(ctx, "sudo", "-n", "/bin/systemctl", "--no-block", "start", "eirl-update.service").CombinedOutput()
			if err != nil {
				return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		HelperStatusPath: updateHelperStat,
		AutoCheck:        auto,
		Log:              log,
	})
	srv.Update = u
	log.Info("updates", "manifest", manifestURL, "auto", auto, "installed", update.InstalledRelease("/"), "key", update.KeyID(keys[0]))
	go u.Run(ctx)
}
