// eirl-update: the root helper of the over-the-air update (UPDATES.md §7). It never runs as a child
// of the app: systemd starts it (eirl-update.service, which the app may only `start` through an exact
// sudoers rule) and it takes nothing from the app but the files of the app's update directory, which
// it reads as data and verifies again on its own.
//
//	eirl-update install    verify and install the release the app downloaded (eirl-update.service)
//	eirl-update recover    at boot: undo an install that was interrupted (eirl-update-recover.service)
//	eirl-update rollback   the operator puts back the release kept in the backup
//	eirl-update status     print the status of the last run
//	eirl-update verify -manifest F -sig F [-bundle F]   check a release with the compiled-in key
//	eirl-update system apply -layer DIR [-image]          apply a release's system layer (UPDATES.md §12;
//	                                                      the bundle's install.sh runs it)
//	eirl-update system plan -layer DIR                    what an apply would change, without changing it
//	eirl-update system recover                            undo a system layer apply that was cut
//	eirl-update system status                             print /var/lib/eirl-system/state.json
//	eirl-update system fingerprint [-root R] [-layer DIR] what makes a root filesystem the system it is (§12.10)
//	eirl-update system compare -a F -b F [-expected F]    the differences of two fingerprints (exit 1: unexpected ones)
//
// Every path can be moved under -root for tests.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update/installer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update/syslayer"
)

var version = "dev" // -ldflags "-X main.version=…"

func usage() {
	fmt.Fprintf(os.Stderr, `eirl-update %s
usage: eirl-update [flags] install|recover|rollback|status
       eirl-update verify -manifest FILE -sig FILE [-bundle FILE] [-installed VERSION]
`, version)
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		os.Exit(verify(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "system" {
		os.Exit(systemCmd(os.Args[2:]))
	}
	fs := flag.NewFlagSet("eirl-update", flag.ContinueOnError)
	fs.Usage = usage
	root := fs.String("root", "/", "prefix of every path (tests)")
	appDir := fs.String("app-dir", "/var/lib/eirl-encoder/update", "the app's update directory (read as data)")
	stateDir := fs.String("state-dir", "/var/lib/eirl-update", "the helper's directory (status, backup)")
	runDir := fs.String("run-dir", "/run/eirl-engine", "the engine's run directory (is it streaming?)")
	appSock := fs.String("app-socket", "/run/eirl-encoder/api.sock", "the app's local socket (health check)")
	appUser := fs.String("app-user", "eirl-encoder", "owner of the files in -app-dir")
	healthTimeout := fs.Duration("health-timeout", 90*time.Second, "time for the new release to come up")
	settle := fs.Duration("settle", 10*time.Second, "the services must stay up this long after the check")
	restart := fs.Bool("restart", false, "recover: also restart the services (not at boot)")
	if len(os.Args) < 2 {
		usage()
		os.Exit(64)
	}
	cmd := os.Args[len(os.Args)-1]
	if err := fs.Parse(os.Args[1 : len(os.Args)-1]); err != nil || fs.NArg() > 0 {
		os.Exit(64)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{} // journald adds the time
		}
		return a
	}}))
	keys, err := update.TrustedKeys()
	if err != nil {
		log.Error("no trusted key", "err", err)
		os.Exit(1)
	}
	uid := -1
	if u, err := user.Lookup(*appUser); err == nil {
		uid, _ = strconv.Atoi(u.Uid)
	} else if *root == "/" {
		log.Error("unknown app user: nothing in its directory is trusted", "user", *appUser)
		uid = -2
	}
	in := &installer.Installer{
		Root: *root, AppDir: *appDir, StateDir: *stateDir, RunDir: *runDir, AppUID: uid, Keys: keys,
		Systemd: installer.Systemctl{}, RunScript: installer.RunInstallScript,
		AppHealth:     installer.CheckApp(rooted(*root, *appSock)),
		HealthTimeout: *healthTimeout, Settle: *settle, Log: log,
		System: &syslayer.Applier{Root: *root, Log: log, Proc: "/proc", Sys: "/sys"},
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if update.TestBuild() {
		log.Warn("TEST BUILD: releases are verified with a test key, not the production key", "key", update.KeyID(keys[0]))
	}
	log.Info("eirl-update", "version", version, "command", cmd)
	switch cmd {
	case "install":
		err = in.Install(ctx)
	case "recover":
		err = in.Recover(ctx, *restart)
	case "rollback":
		err = in.Rollback(ctx)
	case "status":
		b, rerr := os.ReadFile(in.StatusPath())
		if rerr != nil {
			fmt.Println(`{"state":""}`)
			return
		}
		os.Stdout.Write(b)
		return
	default:
		usage()
		os.Exit(64)
	}
	if err != nil {
		var f *installer.Failure
		if errors.As(err, &f) {
			log.Error("done: not installed", "code", f.Code, "reason", f.Reason)
			os.Exit(2)
		}
		log.Error("done: error", "err", err)
		os.Exit(1)
	}
	log.Info("done")
}

func rooted(root, p string) string {
	if root == "/" || root == "" {
		return p
	}
	return root + p
}

// verify checks a release the way the board does (operators, the release tool, tests).
func verify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	manifest := fs.String("manifest", "manifest.json", "manifest file")
	sig := fs.String("sig", "", "signature file (default <manifest>.sig)")
	bundle := fs.String("bundle", "", "bundle to check against the manifest (optional)")
	installed := fs.String("installed", "", "check that the release may update this version (optional)")
	fs.Parse(args)
	if *sig == "" {
		*sig = *manifest + ".sig"
	}
	keys, err := update.TrustedKeys()
	if err == nil {
		err = verifyFiles(keys, *manifest, *sig, *bundle, *installed)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 1
	}
	return 0
}

func verifyFiles(keys []ed25519.PublicKey, manifest, sig, bundle, installed string) error {
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	s, err := os.ReadFile(sig)
	if err != nil {
		return err
	}
	m, err := update.VerifyManifest(raw, s, keys)
	if err != nil {
		return err
	}
	fmt.Printf("manifest OK: %s %s (key %s)\n", m.Product, m.Version, update.KeyID(keys[0]))
	if bundle != "" {
		sum, err := update.FileSHA256(bundle, m.Bundle.Size)
		if err != nil {
			return err
		}
		if sum != m.Bundle.SHA256 {
			return fmt.Errorf("bundle sha256 %s, manifest says %s", sum, m.Bundle.SHA256)
		}
		fmt.Println("bundle OK:", m.Bundle.Name)
	}
	if installed != "" {
		if err := update.CheckUpgrade(m, installed); err != nil {
			return err
		}
		fmt.Println("may update from", installed)
	}
	return nil
}
