// Package installer is the root side of the over-the-air update (UPDATES.md §7): it takes what the app
// downloaded as DATA, verifies it again on its own (signature with the compiled-in key, sha256, no
// downgrade, minFrom), refuses while streaming, backs up what it is going to replace, runs the bundle's
// install.sh, restarts the services, checks that the new version comes up healthy and otherwise
// restores the backup. cmd/eirl-update is the binary; everything here runs under a root prefix so the
// whole install and rollback can be tested on a temporary directory.
package installer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update/syslayer"
)

// Units the helper restarts.
const (
	AppUnit    = "eirl-encoderd.service"
	EngineUnit = "eirl-engine.service"
)

// Systemd is systemctl (an interface: tests use a fake).
type Systemd interface {
	// Run runs systemctl with args (e.g. "restart", "eirl-engine.service").
	Run(ctx context.Context, args ...string) error
	// Show returns one property of a unit (systemctl show -p <prop> --value <unit>).
	Show(ctx context.Context, unit, prop string) (string, error)
}

// Installer holds the configuration of one helper run. Paths are absolute paths ON THE BOARD; Root is
// prepended to all of them ("/" on the board, a temporary directory in tests).
type Installer struct {
	Root     string
	AppDir   string // the app's update directory (/var/lib/eirl-encoder/update): read as data only
	StateDir string // the helper's own directory (/var/lib/eirl-update)
	RunDir   string // the engine's run directory (/run/eirl-engine)
	AppUID   int    // the files in AppDir must belong to this uid (-1 = not checked: tests)
	Keys     []ed25519.PublicKey
	Systemd  Systemd
	// RunScript runs the bundle's install.sh (dir = the extracted bundle); output goes to out.
	RunScript func(ctx context.Context, dir string, out io.Writer) error
	// AppHealth says whether the app answers and runs version expect (CheckApp on the board).
	AppHealth     func(ctx context.Context, expect string) error
	HealthTimeout time.Duration // 90 s
	Settle        time.Duration // the units must stay up this long after the check (10 s)
	Poll          time.Duration // 1 s
	ScriptTimeout time.Duration // install.sh, with the system layer it applies (20 min)
	// System undoes a system layer apply that was cut (UPDATES.md §12.4): install.sh applies the layer, and a
	// power cut in the middle is undone here, at boot or before the next install. nil = none (tests)
	System *syslayer.Applier
	// MinFree: the free space left after the backup and the extracted bundle (200 MB)
	MinFree int64
	Log     *slog.Logger
	Now     func() time.Time
}

// Defaults fills what is not set.
func (in *Installer) defaults() {
	if in.Root == "" {
		in.Root = "/"
	}
	if in.HealthTimeout == 0 {
		in.HealthTimeout = 90 * time.Second
	}
	if in.Settle == 0 {
		in.Settle = 10 * time.Second
	}
	if in.Poll == 0 {
		in.Poll = time.Second
	}
	if in.ScriptTimeout == 0 {
		in.ScriptTimeout = 20 * time.Minute
	}
	if in.MinFree == 0 {
		in.MinFree = 200 << 20
	}
	if in.Log == nil {
		in.Log = slog.New(slog.DiscardHandler)
	}
	if in.Now == nil {
		in.Now = time.Now
	}
}

func (in *Installer) p(abs string) string { return filepath.Join(in.Root, abs) }

func (in *Installer) state(name string) string { return filepath.Join(in.p(in.StateDir), name) }

func (in *Installer) backupDir() string  { return in.state("backup") }
func (in *Installer) stagingDir() string { return in.state("staging") }

// StatusPath is status.json (the app reads it).
func (in *Installer) StatusPath() string { return in.state("status.json") }
func (in *Installer) markerPath() string { return in.state("in-progress.json") }

// marker: written once the backup is complete and before anything is changed; removed when the new
// release is healthy or the old one is back. Present at boot = the install was interrupted (power loss,
// crash): eirl-update-recover.service restores the backup before the encoder starts.
type marker struct {
	From         string    `json:"from"`
	To           string    `json:"to"`
	Backup       string    `json:"backup"`
	StartedAt    time.Time `json:"startedAt"`
	EngineActive bool      `json:"engineActive"`
}

// Failure is a refused or failed run with its stable code.
type Failure struct {
	Code   string
	Reason string
}

func (f *Failure) Error() string { return f.Code + ": " + f.Reason }

func fail(code, format string, a ...any) *Failure {
	return &Failure{Code: code, Reason: fmt.Sprintf(format, a...)}
}

// prepare creates the helper's directories: the state directory readable (status.json is for the
// app), backups and staging root-only.
func (in *Installer) prepare() error {
	if err := os.MkdirAll(in.p(in.StateDir), 0o755); err != nil {
		return err
	}
	for _, d := range []string{in.backupDir(), in.stagingDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// cleanStaging empties the staging directory (root-only, 0700).
func (in *Installer) cleanStaging() error {
	if err := os.RemoveAll(in.stagingDir()); err != nil {
		return err
	}
	return os.MkdirAll(in.stagingDir(), 0o700)
}

// lock takes the helper's lock (one run at a time). The returned function releases it.
func (in *Installer) lock() (func(), error) {
	f, err := os.OpenFile(in.state("lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another update run holds the lock")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func (in *Installer) writeStatus(st *update.HelperStatus) {
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := writeFileAtomic(in.StatusPath(), append(b, '\n'), 0o644); err != nil {
		in.Log.Error("cannot write status", "err", err)
	}
}

// Install is `eirl-update install`: the run started by the app through eirl-update.service.
func (in *Installer) Install(ctx context.Context) error {
	in.defaults()
	if err := in.prepare(); err != nil {
		return err
	}
	unlock, err := in.lock()
	if err != nil {
		return err
	}
	defer unlock()
	// What a power cut left in staging (no defer ran) would make the extraction fail (O_EXCL)
	if err := in.cleanStaging(); err != nil {
		return err
	}
	installed := update.InstalledRelease(in.Root)
	st := &update.HelperStatus{State: update.HelperInstalling, Phase: "verify", From: installed, StartedAt: in.Now().UTC()}
	in.writeStatus(st)
	in.Log.Info("update requested", "installed", orNone(installed))
	in.recoverSystem(ctx)
	if _, err := os.Stat(in.markerPath()); err == nil {
		// A previous run was interrupted and not recovered at boot: put the old release back first
		in.Log.Warn("a previous update was interrupted: restoring its backup first")
		if rerr := in.rollback(ctx, "interrupted", "a previous update was interrupted", true); rerr != nil {
			return rerr
		}
		return in.refuse(st, fail("interrupted", "a previous update was interrupted and has been undone; try again"))
	}
	defer os.RemoveAll(in.stagingDir())
	m, bundle, f := in.stage(installed)
	if f != nil {
		return in.refuse(st, f)
	}
	st.To = m.Version
	if on, why := update.Streaming(in.p(in.RunDir)); on {
		return in.refuse(st, fail(update.CodeStreaming, "the encoder is streaming (%s)", why))
	}
	dir := filepath.Join(in.stagingDir(), "bundle")
	if err := extractBundle(bundle, dir); err != nil {
		return in.refuse(st, fail("bad_bundle", "%v", err))
	}
	newFiles, f := checkBundle(dir, m.Version)
	if f != nil {
		return in.refuse(st, f)
	}
	oldFiles := in.oldFiles()
	if f := in.checkSpace(dir, union(oldFiles, newFiles)); f != nil {
		return in.refuse(st, f)
	}

	// Backup of every path of the old and the new release, then the marker: from here on, any failure
	// (or a power cut) brings the old release back
	st.Phase = "backup"
	in.writeStatus(st)
	from := installed
	if from == "" {
		from = "0.0.0"
	}
	bpath := filepath.Join(in.backupDir(), "backup-"+from+".tar")
	newSet := map[string]bool{}
	for _, p := range newFiles {
		newSet[p] = true
	}
	all := union(oldFiles, newFiles)
	meta, err := writeBackup(in.Root, bpath, all, newSet, backupMeta{From: from, To: m.Version, CreatedAt: in.Now().UTC()},
		func(f string, a ...any) { in.Log.Warn(fmt.Sprintf(f, a...)) })
	if err != nil {
		return in.refuse(st, fail("backup_failed", "%v", err))
	}
	in.Log.Info("backup written", "path", bpath, "files", len(all)-len(meta.Absent), "new", len(meta.Absent))
	// The engine is restarted only if it runs and the bundle carries it (an app-only test bundle leaves the
	// running engine alone; a new drop-in then applies at its next restart, as with install.sh by hand)
	engineActive := in.active(ctx, EngineUnit) && carriesEngine(newFiles)
	mk := marker{From: from, To: m.Version, Backup: bpath, StartedAt: st.StartedAt, EngineActive: engineActive}
	mb, _ := json.Marshal(mk)
	if err := writeFileAtomic(in.markerPath(), mb, 0o600); err != nil {
		return in.refuse(st, fail("backup_failed", "marker: %v", err))
	}

	// install.sh of the new release
	st.Phase = "install"
	in.writeStatus(st)
	in.Log.Info("running install.sh", "version", m.Version)
	tail := &tailBuffer{max: 2048}
	sctx, cancel := context.WithTimeout(ctx, in.ScriptTimeout)
	err = in.RunScript(sctx, dir, io.MultiWriter(os.Stderr, tail))
	cancel()
	if err != nil {
		return in.rollbackAndReport(ctx, st, "install_failed", fmt.Sprintf("install.sh failed: %v; last output: %s", err, tail.String()))
	}

	// The engine is restarted only if it was running, and never if a stream started meanwhile: then the
	// update is undone as a whole instead of leaving new files under an old running engine
	st.Phase = "restart"
	in.writeStatus(st)
	since := in.Now()
	if engineActive {
		if on, why := update.Streaming(in.p(in.RunDir)); on {
			return in.rollbackAndReport(ctx, st, update.CodeStreaming, "streaming started during the install ("+why+")")
		}
		// The state directory's owner is systemd's job: StateDirectory= re-owns it (fd-based) when User=
		// changes. Root never walks a tree the engine's user can write (UPDATES.md §7)
		if err := in.Systemd.Run(ctx, "restart", EngineUnit); err != nil {
			return in.rollbackAndReport(ctx, st, "unhealthy", fmt.Sprintf("restart %s: %v", EngineUnit, err))
		}
	}
	st.Phase = "health"
	in.writeStatus(st)
	if err := in.health(ctx, m.Version, engineActive, since); err != nil {
		return in.rollbackAndReport(ctx, st, "unhealthy", err.Error())
	}
	os.Remove(in.markerPath())
	// The backup of the release just replaced is the one kept (for `eirl-update rollback`)
	in.keepOnlyBackup(bpath)
	syncDir(in.p(in.StateDir))
	st.State, st.Phase, st.FinishedAt = update.HelperInstalled, "done", in.Now().UTC()
	in.writeStatus(st)
	in.Log.Info("update installed", "from", orNone(installed), "to", m.Version)
	return nil
}

func orNone(v string) string {
	if v == "" {
		return "none (pre-OTA install)"
	}
	return v
}

func (in *Installer) refuse(st *update.HelperStatus, f *Failure) error {
	st.State, st.Error, st.Reason, st.FinishedAt = update.HelperRefused, f.Code, f.Reason, in.Now().UTC()
	in.writeStatus(st)
	in.Log.Warn("update refused: nothing was changed", "code", f.Code, "reason", f.Reason)
	return f
}

func (in *Installer) rollbackAndReport(ctx context.Context, st *update.HelperStatus, code, reason string) error {
	in.Log.Error("update failed: rolling back", "code", code, "reason", reason)
	st.Phase = "rollback"
	in.writeStatus(st)
	if err := in.rollback(ctx, code, reason, true); err != nil {
		st.State, st.Error, st.Reason, st.FinishedAt = update.HelperFailed, code, reason+"; rollback failed: "+err.Error(), in.Now().UTC()
		in.writeStatus(st)
		return err
	}
	st.State, st.Error, st.Reason, st.FinishedAt = update.HelperRolledBack, code, reason, in.Now().UTC()
	in.writeStatus(st)
	return &Failure{Code: code, Reason: reason}
}

// stage copies the app's files into the helper's own directory and verifies the copies (never the
// app's files, which the app could change in between): signature with the compiled-in key, product,
// version newer than installed, minFrom, the request's version, and the bundle's size and sha256.
func (in *Installer) stage(installed string) (*update.Manifest, string, *Failure) {
	reqB, err := in.readApp(update.RequestFile, 4<<10)
	if err != nil {
		return nil, "", fail("bad_request", "request: %v", err)
	}
	var req update.Request
	if err := json.Unmarshal(reqB, &req); err != nil || req.Version == "" {
		return nil, "", fail("bad_request", "request.json is not valid")
	}
	raw, err := in.readApp(update.ManifestFile, update.MaxManifestSize)
	if err != nil {
		return nil, "", fail(update.CodeBadManifest, "manifest: %v", err)
	}
	sig, err := in.readApp(update.SigFile, update.MaxSigSize)
	if err != nil {
		return nil, "", fail(update.CodeBadSignature, "signature: %v", err)
	}
	m, err := update.VerifyManifest(raw, sig, in.Keys)
	if err != nil {
		return nil, "", fail(update.Code(err), "%v", err)
	}
	if m.Version != req.Version {
		return nil, "", fail("bad_request", "requested %s but the manifest is %s", req.Version, m.Version)
	}
	if err := update.CheckUpgrade(m, installed); err != nil {
		return nil, "", fail(update.Code(err), "%v", err)
	}
	dst := filepath.Join(in.stagingDir(), update.BundleFile)
	sum, n, err := in.copyApp(update.BundleFile, dst, m.Bundle.Size)
	if err != nil {
		return nil, "", fail(update.CodeChecksum, "bundle: %v", err)
	}
	if n != m.Bundle.Size || sum != m.Bundle.SHA256 {
		return nil, "", fail(update.CodeChecksum, "bundle does not match the manifest (%d bytes, sha256 %s)", n, sum)
	}
	in.Log.Info("release verified", "version", m.Version, "key", update.KeyID(in.Keys[0]), "bundle", m.Bundle.Name)
	return m, dst, nil
}

// openApp opens a file of the app's directory as data: no symlinks, regular files only, owned by the
// app's user, at most max bytes.
func (in *Installer) openApp(name string, max int64) (*os.File, int64, error) {
	p := filepath.Join(in.p(in.AppDir), name)
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	f := os.NewFile(uintptr(fd), p)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, 0, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		f.Close()
		return nil, 0, errors.New("not a regular file")
	}
	if in.AppUID >= 0 && int(st.Uid) != in.AppUID {
		f.Close()
		return nil, 0, fmt.Errorf("owned by uid %d, not the app's", st.Uid)
	}
	if st.Size > max {
		f.Close()
		return nil, 0, fmt.Errorf("%d bytes, more than %d", st.Size, max)
	}
	return f, st.Size, nil
}

func (in *Installer) readApp(name string, max int64) ([]byte, error) {
	f, _, err := in.openApp(name, max)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		err = errors.New("too large")
	}
	return b, err
}

func (in *Installer) copyApp(name, dst string, max int64) (string, int64, error) {
	f, _, err := in.openApp(name, max)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(f, max+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// checkBundle: the extracted bundle is the release the manifest describes (its VERSION), and has an
// install.sh and a valid files.txt.
func checkBundle(dir, version string) ([]string, *Failure) {
	vb, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil || strings.TrimSpace(string(vb)) != version {
		return nil, fail("bad_bundle", "bundle VERSION %q does not match the manifest %s", strings.TrimSpace(string(vb)), version)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "install.sh")); err != nil || !fi.Mode().IsRegular() {
		return nil, fail("bad_bundle", "bundle has no install.sh")
	}
	fb, err := os.ReadFile(filepath.Join(dir, "files.txt"))
	if err != nil {
		return nil, fail("bad_bundle", "bundle has no files.txt")
	}
	files, err := update.ParseFiles(fb)
	if err != nil {
		return nil, fail("bad_bundle", "%v", err)
	}
	if len(files) == 0 {
		return nil, fail("bad_bundle", "files.txt is empty")
	}
	return files, nil
}

// oldFiles: files.txt of the installed release (none before the first OTA install). Lines that are not
// allowed are skipped (they are never backed up or touched).
func (in *Installer) oldFiles() []string {
	b, err := os.ReadFile(in.p(update.FilesFile))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !update.AllowedPath(line) {
			in.Log.Warn("installed files.txt: skipping a path that is not allowed", "path", line)
			continue
		}
		out = append(out, line)
	}
	return out
}

// carriesEngine: the bundle installs the engine's code.
func carriesEngine(files []string) bool {
	for _, p := range files {
		if isEngineCode(p) {
			return true
		}
	}
	return false
}

func union(a, b []string) []string {
	set := map[string]bool{}
	for _, p := range append(append([]string{}, a...), b...) {
		set[p] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// keepOnlyBackup removes every other backup. Backups are kept like this: after a successful update, only
// the backup of the release it replaced; a backup that has been restored is deleted (its content is the
// installed release). So after a failed attempt the kept backup is still the previous release's.
func (in *Installer) keepOnlyBackup(keep string) {
	ents, _ := os.ReadDir(in.backupDir())
	for _, e := range ents {
		p := filepath.Join(in.backupDir(), e.Name())
		if p != keep {
			os.Remove(p)
		}
	}
}

func (in *Installer) active(ctx context.Context, unit string) bool {
	s, err := in.Systemd.Show(ctx, unit, "ActiveState")
	return err == nil && strings.TrimSpace(s) == "active"
}

// health waits (up to HealthTimeout) for the app to answer as the new version and, if the engine was
// running, for the engine to be active and publish its state again; then both units must stay up
// (same invocation) for Settle.
func (in *Installer) health(ctx context.Context, version string, engine bool, since time.Time) error {
	deadline := in.Now().Add(in.HealthTimeout)
	var appErr error
	appOK, engOK := false, !engine
	for {
		if !appOK {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			appErr = in.AppHealth(cctx, version)
			cancel()
			appOK = appErr == nil
		}
		if !engOK {
			engOK = in.active(ctx, EngineUnit) && in.enginePublished(since)
		}
		if appOK && engOK {
			break
		}
		if !in.Now().Before(deadline) {
			var miss []string
			if !appOK {
				miss = append(miss, fmt.Sprintf("the app does not answer as %s (%v)", version, appErr))
			}
			if !engOK {
				miss = append(miss, "the engine did not publish its state")
			}
			return fmt.Errorf("not healthy after %s: %s", in.HealthTimeout, strings.Join(miss, "; "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(in.Poll):
		}
	}
	units := []string{AppUnit}
	if engine {
		units = append(units, EngineUnit)
	}
	before := map[string]string{}
	for _, u := range units {
		before[u], _ = in.Systemd.Show(ctx, u, "InvocationID")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(in.Settle):
	}
	for _, u := range units {
		id, _ := in.Systemd.Show(ctx, u, "InvocationID")
		if id != before[u] || !in.active(ctx, u) {
			return fmt.Errorf("%s restarted or stopped during the %s after the check", u, in.Settle)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := in.AppHealth(cctx, version); err != nil {
		return fmt.Errorf("the app stopped answering: %v", err)
	}
	in.Log.Info("healthy", "version", version, "engine", engine)
	return nil
}

// enginePublished: the engine wrote its state (state or mixer.json) after since.
func (in *Installer) enginePublished(since time.Time) bool {
	for _, f := range []string{"mixer.json", "state"} {
		if fi, err := os.Stat(filepath.Join(in.p(in.RunDir), f)); err == nil && !fi.ModTime().Before(since.Add(-time.Second)) {
			return true
		}
	}
	return false
}

// rollback restores the backup named in the marker (or the only one kept), removes the marker and, with
// restart, restarts the app and (if it was running and nothing streams) the engine.
func (in *Installer) rollback(ctx context.Context, code, reason string, restart bool) error {
	var mk marker
	if b, err := os.ReadFile(in.markerPath()); err == nil {
		json.Unmarshal(b, &mk)
	}
	bpath := mk.Backup
	if bpath == "" || !strings.HasPrefix(filepath.Clean(bpath), in.backupDir()+string(filepath.Separator)) {
		var err error
		if bpath, err = in.lastBackup(); err != nil {
			return err
		}
	}
	in.Log.Info("restoring backup", "path", bpath, "code", code)
	meta, engineFiles, err := restoreBackup(in.Root, bpath, func(f string, a ...any) { in.Log.Info(fmt.Sprintf(f, a...)) })
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if err := in.Systemd.Run(ctx, "daemon-reload"); err != nil {
		in.Log.Warn("daemon-reload", "err", err)
	}
	if restart {
		if err := in.Systemd.Run(ctx, "restart", AppUnit); err != nil {
			in.Log.Warn("restart app", "err", err)
		}
		// The engine if the install restarted it (the marker: it ran and the bundle carries it; also when the
		// new engine then failed, or when an earlier rollback was interrupted after the restore), or if the
		// restore changed the engine's code, units or drop-in while it runs. Never for an app-only bundle
		if mk.EngineActive || (engineFiles && in.active(ctx, EngineUnit)) {
			if on, why := update.Streaming(in.p(in.RunDir)); on {
				in.Log.Warn("not restarting the engine: it is streaming (it runs the old files it loaded)", "why", why)
			} else if err := in.Systemd.Run(ctx, "restart", EngineUnit); err != nil {
				in.Log.Warn("restart engine", "err", err)
			}
		}
	}
	os.Remove(in.markerPath())
	os.Remove(bpath) // restored: its content is what is installed now
	syncDir(in.p(in.StateDir))
	in.Log.Info("rolled back", "to", meta.From, "undone", meta.To)
	return nil
}

func (in *Installer) lastBackup() (string, error) {
	ents, err := os.ReadDir(in.backupDir())
	if err != nil {
		return "", err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "backup-") && strings.HasSuffix(e.Name(), ".tar") {
			return filepath.Join(in.backupDir(), e.Name()), nil
		}
	}
	return "", errors.New("no backup kept")
}

// Recover is `eirl-update recover` (eirl-update-recover.service, at boot before the encoder's units):
// if an install was interrupted, the old release is restored. restart=false at boot (the units have
// not started yet: only daemon-reload).
func (in *Installer) Recover(ctx context.Context, restart bool) error {
	in.defaults()
	in.recoverSystem(ctx)
	if _, err := os.Stat(in.markerPath()); err != nil {
		in.Log.Info("no interrupted update")
		return nil
	}
	if err := in.prepare(); err != nil {
		return err
	}
	unlock, err := in.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := in.cleanStaging(); err != nil {
		in.Log.Warn("cannot empty staging", "err", err)
	}
	var mk marker
	if b, err := os.ReadFile(in.markerPath()); err == nil {
		json.Unmarshal(b, &mk)
	}
	in.Log.Warn("an update was interrupted: restoring the previous release", "from", mk.From, "to", mk.To)
	reason := "the update was interrupted (power loss or crash) and has been undone"
	st := &update.HelperStatus{State: update.HelperRolledBack, Phase: "rollback", From: mk.From, To: mk.To,
		StartedAt: mk.StartedAt, Error: "interrupted", Reason: reason}
	if err := in.rollback(ctx, "interrupted", reason, restart); err != nil {
		st.State, st.Reason = update.HelperFailed, reason+"; rollback failed: "+err.Error()
		st.FinishedAt = in.Now().UTC()
		in.writeStatus(st)
		return err
	}
	st.FinishedAt = in.Now().UTC()
	in.writeStatus(st)
	return nil
}

// recoverSystem undoes a system layer apply that a power cut (or a crash) left half done: before the release's
// files are restored, so the old release starts on the system it had (UPDATES.md §12.4).
func (in *Installer) recoverSystem(ctx context.Context) {
	if in.System == nil || !in.System.Interrupted() {
		return
	}
	in.Log.Warn("a system layer apply was interrupted: undoing it")
	if _, err := in.System.Recover(ctx); err != nil {
		in.Log.Error("undoing the interrupted system layer failed (tried again at the next boot)", "err", err)
	}
}

// checkSpace refuses an update that would fill the card (UPDATES.md §11): the backup of every path and the
// extracted bundle, twice, plus a margin. The system layer checks its own needs when it applies.
func (in *Installer) checkSpace(bundleDir string, paths []string) *Failure {
	var need int64
	for _, p := range paths {
		if fi, err := os.Lstat(in.p(p)); err == nil && fi.Mode().IsRegular() {
			need += fi.Size()
		}
	}
	filepath.WalkDir(bundleDir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				need += fi.Size()
			}
		}
		return nil
	})
	var sfs unix.Statfs_t
	if err := unix.Statfs(in.p(in.StateDir), &sfs); err != nil {
		return nil
	}
	free := int64(sfs.Bavail) * int64(sfs.Bsize)
	if free < 2*need+in.MinFree {
		return fail("no_space", "%d MB free, the update needs %d MB", free>>20, (2*need+in.MinFree)>>20)
	}
	return nil
}

// Rollback is `eirl-update rollback`: the operator puts back the release kept in the backup (the only
// downgrade allowed). Refused while streaming.
func (in *Installer) Rollback(ctx context.Context) error {
	in.defaults()
	if err := in.prepare(); err != nil {
		return err
	}
	unlock, err := in.lock()
	if err != nil {
		return err
	}
	defer unlock()
	bpath, err := in.lastBackup()
	if err != nil {
		return err
	}
	meta, err := readBackupMeta(bpath)
	if err != nil {
		return err
	}
	installed := update.InstalledRelease(in.Root)
	if meta.To != installed {
		return fail("no_backup", "the kept backup (%s, replaced by %s) does not belong to the installed release %s", meta.From, meta.To, orNone(installed))
	}
	if on, why := update.Streaming(in.p(in.RunDir)); on {
		return fail(update.CodeStreaming, "the encoder is streaming (%s)", why)
	}
	st := &update.HelperStatus{State: update.HelperInstalling, Phase: "rollback", From: installed,
		To: meta.From, StartedAt: in.Now().UTC()}
	in.writeStatus(st)
	reason := "rolled back by the operator to " + meta.From
	if err := in.rollback(ctx, "manual", reason, true); err != nil {
		st.State, st.Error, st.Reason, st.FinishedAt = update.HelperFailed, "manual", "rollback failed: "+err.Error(), in.Now().UTC()
		in.writeStatus(st)
		return err
	}
	st.State, st.Error, st.Reason, st.FinishedAt = update.HelperRolledBack, "manual", reason, in.Now().UTC()
	in.writeStatus(st)
	return nil
}

// tailBuffer keeps the last max bytes written (the end of install.sh's output for the status).
type tailBuffer struct {
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	return strings.TrimSpace(string(bytes.ToValidUTF8(t.b, nil)))
}

func writeFileAtomic(p string, b []byte, mode os.FileMode) error {
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(p))
	return nil
}
