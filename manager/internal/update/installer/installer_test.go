package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
)

// --- fakes ---------------------------------------------------------------------------------------------

type fakeSystemd struct {
	mu      sync.Mutex
	calls   []string
	active  map[string]bool
	inv     map[string]int
	runDir  string // the engine "publishes" its state here when restarted
	onRun   func(args []string)
	flapApp bool // the app restarts by itself (crash loop) after every check
}

func newSystemd(runDir string) *fakeSystemd {
	return &fakeSystemd{active: map[string]bool{AppUnit: true, EngineUnit: true}, inv: map[string]int{}, runDir: runDir}
}

func (f *fakeSystemd) Run(_ context.Context, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) == 2 && args[0] == "restart" {
		f.inv[args[1]]++
		f.active[args[1]] = true
		if args[1] == EngineUnit && f.runDir != "" {
			os.MkdirAll(f.runDir, 0o755)
			os.WriteFile(filepath.Join(f.runDir, "state"), []byte("OUTPUT=off\nMIXER=1\n"), 0o644)
		}
	}
	cb := f.onRun
	f.mu.Unlock()
	if cb != nil {
		cb(args)
	}
	return nil
}

func (f *fakeSystemd) Show(_ context.Context, unit, prop string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch prop {
	case "ActiveState":
		if f.active[unit] {
			return "active", nil
		}
		return "inactive", nil
	case "InvocationID":
		if unit == AppUnit && f.flapApp {
			f.inv[unit]++
		}
		return fmt.Sprintf("%s-%d", unit, f.inv[unit]), nil
	}
	return "", nil
}

func (f *fakeSystemd) called(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

// --- a board under a temporary root ---------------------------------------------------------------------

type board struct {
	t      *testing.T
	root   string
	in     *Installer
	sd     *fakeSystemd
	key    ed25519.PrivateKey
	appDir string
}

const (
	appBin    = "/usr/local/bin/eirl-encoderd"
	ctlLink   = "/usr/local/bin/eirl-enginectl"
	engineSh  = "/usr/lib/eirl-encoder/engine/eirl-engine.sh"
	sudoers   = "/etc/sudoers.d/eirl-encoder"
	helperBin = "/usr/local/sbin/eirl-update"
)

func newBoard(t *testing.T, installed string) *board {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	root := t.TempDir()
	b := &board{t: t, root: root, key: priv}
	b.sd = newSystemd(filepath.Join(root, "/run/eirl-engine"))
	b.appDir = "/var/lib/eirl-encoder/update"
	os.MkdirAll(filepath.Join(root, b.appDir), 0o750)
	b.in = &Installer{
		Root: root, AppDir: b.appDir, StateDir: "/var/lib/eirl-update", RunDir: "/run/eirl-engine", AppUID: -1,
		Keys: []ed25519.PublicKey{pub}, Systemd: b.sd, RunScript: b.runScript,
		// The "app" answers as the version its binary says (the content of the file)
		AppHealth: func(_ context.Context, expect string) error {
			got, err := os.ReadFile(filepath.Join(root, appBin))
			if err != nil {
				return err
			}
			if v := strings.TrimPrefix(string(got), "app "); v != expect {
				return fmt.Errorf("the app runs %q", v)
			}
			return nil
		},
		HealthTimeout: 300 * time.Millisecond, Settle: 10 * time.Millisecond, Poll: 10 * time.Millisecond,
	}
	// What is installed now
	if installed != "" {
		b.write(appBin, "app "+installed, 0o755)
		b.write(engineSh, "#!/bin/bash\n# engine "+installed+"\n", 0o755)
		b.write(sudoers, "eirl-encoder ALL=… "+installed+"\n", 0o440)
		os.Symlink("/usr/lib/eirl-encoder/engine/eirl-enginectl", filepath.Join(root, ctlLink))
		b.write(update.VersionFile, installed+"\n", 0o644)
		b.write(update.FilesFile, strings.Join([]string{appBin, ctlLink, engineSh, sudoers, update.VersionFile, update.FilesFile}, "\n")+"\n", 0o644)
	} else {
		// Pre-OTA install: the files exist, without VERSION or files.txt
		b.write(appBin, "app 0.0.0", 0o755)
		b.write(sudoers, "eirl-encoder ALL=… old\n", 0o440)
	}
	os.MkdirAll(filepath.Join(root, "/run/eirl-engine"), 0o755)
	return b
}

func (b *board) write(p, s string, mode os.FileMode) {
	b.t.Helper()
	full := filepath.Join(b.root, p)
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(s), mode); err != nil {
		b.t.Fatal(err)
	}
	os.Chmod(full, mode)
}

func (b *board) read(p string) string {
	got, err := os.ReadFile(filepath.Join(b.root, p))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(got)
}

func (b *board) exists(p string) bool {
	_, err := os.Lstat(filepath.Join(b.root, p))
	return err == nil
}

// runScript stands in for install.sh: copies payload/<path> of the bundle to the root (as the real
// one installs to /), with a temporary name and a rename. A file "FAIL" in the bundle makes it fail.
func (b *board) runScript(_ context.Context, dir string, out io.Writer) error {
	if _, err := os.Stat(filepath.Join(dir, "FAIL")); err == nil {
		fmt.Fprintln(out, "install.sh: something broke")
		return errors.New("exit status 1")
	}
	payload := filepath.Join(dir, "payload")
	return filepath.Walk(payload, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(payload, p)
		dst := filepath.Join(b.root, "/"+rel)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		data, _ := os.ReadFile(p)
		tmp := dst + ".new"
		if err := os.WriteFile(tmp, data, fi.Mode()); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	})
}

// release is a bundle (tar.gz) plus its signed manifest, placed in the app's directory with a request.
type release struct {
	version string
	minFrom string
	files   map[string]string // payload path (absolute on the board) → content
	extra   map[string]string // other entries of the bundle (relative)
	tamper  bool              // change the bundle after signing
	inside  string            // VERSION inside the bundle, if it must differ from version
}

func (b *board) offer(r release) {
	b.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, content string, mode int64) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg, ModTime: time.Now()})
		tw.Write([]byte(content))
	}
	var list []string
	for p := range r.files {
		list = append(list, p)
	}
	list = append(list, update.VersionFile, update.FilesFile)
	sort.Strings(list)
	inside := r.version
	if r.inside != "" {
		inside = r.inside
	}
	add("VERSION", inside+"\n", 0o644)
	add("install.sh", "#!/bin/bash\n", 0o755)
	add("files.txt", strings.Join(list, "\n")+"\n", 0o644)
	for p, c := range r.files {
		add("payload"+p, c, 0o755)
	}
	add("payload"+update.VersionFile, r.version+"\n", 0o644)
	add("payload"+update.FilesFile, strings.Join(list, "\n")+"\n", 0o644)
	for n, c := range r.extra {
		add(n, c, 0o644)
	}
	tw.Close()
	gz.Close()
	bundle := buf.Bytes()
	sum := sha256.Sum256(bundle)
	m := update.Manifest{V: 1, Product: update.Product, Version: r.version, MinFrom: r.minFrom, Released: time.Now().UTC(),
		Bundle: update.Bundle{Name: update.BundleName(r.version), Size: int64(len(bundle)), SHA256: hex.EncodeToString(sum[:])}}
	raw, _ := json.Marshal(m)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(b.key, raw))
	if r.tamper {
		bundle = append([]byte{}, bundle...)
		bundle[len(bundle)/2] ^= 0xff
	}
	req, _ := json.Marshal(update.Request{Version: r.version, RequestedAt: time.Now()})
	dir := filepath.Join(b.root, b.appDir)
	os.WriteFile(filepath.Join(dir, update.ManifestFile), raw, 0o640)
	os.WriteFile(filepath.Join(dir, update.SigFile), []byte(sig+"\n"), 0o640)
	os.WriteFile(filepath.Join(dir, update.BundleFile), bundle, 0o640)
	os.WriteFile(filepath.Join(dir, update.RequestFile), req, 0o640)
}

func (b *board) status() update.HelperStatus {
	var st update.HelperStatus
	data, err := os.ReadFile(b.in.StatusPath())
	if err != nil {
		b.t.Fatalf("no status: %v", err)
	}
	json.Unmarshal(data, &st)
	return st
}

func TestAppOnlyBundleLeavesTheEngine(t *testing.T) {
	b := newBoard(t, "0.1.0")
	// The installed release's files.txt without the engine either (an app-only install)
	b.write(update.FilesFile, strings.Join([]string{appBin, sudoers, update.VersionFile, update.FilesFile}, "\n")+"\n", 0o644)
	r := newRelease("0.2.0", "0.2.0")
	delete(r.files, engineSh)
	b.offer(r)
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.sd.called("restart " + EngineUnit) {
		t.Error("an app-only bundle restarted the engine")
	}
	// Nor does its rollback: automatic (a broken app-only release) or by hand
	b.offer(func() release { r := newRelease("0.3.0", "broken"); delete(r.files, engineSh); return r }())
	if err := b.in.Install(context.Background()); err == nil {
		t.Fatal("broken release installed")
	}
	if err := b.in.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.sd.called("restart " + EngineUnit) {
		t.Errorf("an app-only rollback restarted the engine: %v", b.sd.calls)
	}
	if !b.sd.called("restart " + AppUnit) {
		t.Error("the app was not restarted")
	}
}

// On a board where the engine came with a release, an app-only update's backup holds the engine's files too
// (old files.txt): restoring them unchanged must not restart the engine
func TestAppOnlyRollbackOverEngineInstall(t *testing.T) {
	b := newBoard(t, "0.1.0") // its files.txt lists the engine
	r := newRelease("0.2.0", "broken")
	delete(r.files, engineSh)
	b.offer(r)
	if err := b.in.Install(context.Background()); err == nil {
		t.Fatal("broken release installed")
	}
	if b.sd.called("restart " + EngineUnit) {
		t.Errorf("restoring unchanged engine files restarted the engine: %v", b.sd.calls)
	}
}

// A rollback that puts the engine's code back restarts the engine (it runs the new files otherwise)
func TestEngineRollbackRestartsTheEngine(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.offer(newRelease("0.2.0", "broken")) // carries engineSh
	if err := b.in.Install(context.Background()); err == nil {
		t.Fatal("broken release installed")
	}
	n := 0
	for _, c := range b.sd.calls {
		if c == "restart "+EngineUnit {
			n++
		}
	}
	if n != 2 {
		t.Errorf("engine restarts %d, want 2 (new release, then the restored one): %v", n, b.sd.calls)
	}
}

func newRelease(version, app string) release {
	return release{version: version, files: map[string]string{
		appBin:    "app " + app,
		engineSh:  "#!/bin/bash\n# engine " + version + "\n",
		sudoers:   "eirl-encoder ALL=… " + version + "\n",
		helperBin: "helper " + version, // new in this release
	}}
}

// --- tests ---------------------------------------------------------------------------------------------

func TestInstallHealthy(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.offer(newRelease("0.2.0", "0.2.0"))
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	st := b.status()
	if st.State != update.HelperInstalled || st.From != "0.1.0" || st.To != "0.2.0" || st.FinishedAt.IsZero() {
		t.Fatalf("status %+v", st)
	}
	if b.read(appBin) != "app 0.2.0" || b.read(update.VersionFile) != "0.2.0\n" || b.read(helperBin) != "helper 0.2.0" {
		t.Error("new release not installed")
	}
	if !b.sd.called("restart " + EngineUnit) {
		t.Error("engine not restarted")
	}
	if b.exists("/var/lib/eirl-update/in-progress.json") {
		t.Error("marker left")
	}
	ents, _ := os.ReadDir(filepath.Join(b.root, "/var/lib/eirl-update/backup"))
	if len(ents) != 1 || ents[0].Name() != "backup-0.1.0.tar" {
		t.Errorf("backups %v", ents)
	}
	if ents, _ := os.ReadDir(filepath.Join(b.root, "/var/lib/eirl-update/staging")); len(ents) != 0 {
		t.Errorf("staging not cleaned: %v", ents)
	}
	fi, _ := os.Stat(filepath.Join(b.root, "/var/lib/eirl-update/backup"))
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode %v", fi.Mode().Perm())
	}

	// The operator rolls back by hand to the kept release
	if err := b.in.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if b.read(appBin) != "app 0.1.0" || b.read(update.VersionFile) != "0.1.0\n" || b.exists(helperBin) {
		t.Errorf("manual rollback: app %q version %q helper %v", b.read(appBin), b.read(update.VersionFile), b.exists(helperBin))
	}
	if st := b.status(); st.State != update.HelperRolledBack || st.Error != "manual" {
		t.Errorf("status %+v", st)
	}
}

func TestInstallUnhealthyRollsBack(t *testing.T) {
	b := newBoard(t, "0.1.0")
	r := newRelease("0.2.0", "broken") // the new app never answers as 0.2.0
	b.offer(r)
	err := b.in.Install(context.Background())
	var f *Failure
	if !errors.As(err, &f) || f.Code != "unhealthy" {
		t.Fatalf("install: %v", err)
	}
	st := b.status()
	if st.State != update.HelperRolledBack || st.Error != "unhealthy" || !strings.Contains(st.Reason, "app") {
		t.Fatalf("status %+v", st)
	}
	// Everything as before, the new file removed, the symlink back
	if b.read(appBin) != "app 0.1.0" || b.read(update.VersionFile) != "0.1.0\n" || !strings.Contains(b.read(sudoers), "0.1.0") {
		t.Errorf("not restored: app %q version %q sudoers %q", b.read(appBin), b.read(update.VersionFile), b.read(sudoers))
	}
	if b.exists(helperBin) {
		t.Error("file added by the failed release not removed")
	}
	if l, err := os.Readlink(filepath.Join(b.root, ctlLink)); err != nil || l != "/usr/lib/eirl-encoder/engine/eirl-enginectl" {
		t.Errorf("symlink %q %v", l, err)
	}
	fi, _ := os.Stat(filepath.Join(b.root, sudoers))
	if fi.Mode().Perm() != 0o440 {
		t.Errorf("sudoers mode %v", fi.Mode().Perm())
	}
	if !b.sd.called("daemon-reload") || !b.sd.called("restart "+AppUnit) {
		t.Errorf("services not restarted after the rollback: %v", b.sd.calls)
	}
	if b.exists("/var/lib/eirl-update/in-progress.json") {
		t.Error("marker left")
	}
}

func TestInstallUnstableAppRollsBack(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.sd.flapApp = true // answers, but systemd keeps restarting it
	b.offer(newRelease("0.2.0", "0.2.0"))
	err := b.in.Install(context.Background())
	if st := b.status(); err == nil || st.State != update.HelperRolledBack || !strings.Contains(st.Reason, "restarted") {
		t.Fatalf("flapping app: %v %+v", err, st)
	}
}

func TestInstallScriptFailureRollsBack(t *testing.T) {
	b := newBoard(t, "0.1.0")
	r := newRelease("0.2.0", "0.2.0")
	r.extra = map[string]string{"FAIL": "1"}
	b.offer(r)
	err := b.in.Install(context.Background())
	if st := b.status(); err == nil || st.State != update.HelperRolledBack || st.Error != "install_failed" || !strings.Contains(st.Reason, "something broke") {
		t.Fatalf("%v %+v", err, b.status())
	}
	if b.read(appBin) != "app 0.1.0" {
		t.Error("not restored")
	}
}

func TestFirstUpdateFromPreOTAInstall(t *testing.T) {
	b := newBoard(t, "") // no VERSION, no files.txt
	b.offer(newRelease("0.1.0", "broken"))
	if err := b.in.Install(context.Background()); err == nil {
		t.Fatal("broken release installed")
	}
	// The paths of the new bundle that existed are back; those that did not are gone
	if b.read(appBin) != "app 0.0.0" || !strings.Contains(b.read(sudoers), "old") {
		t.Errorf("not restored: %q %q", b.read(appBin), b.read(sudoers))
	}
	for _, p := range []string{update.VersionFile, update.FilesFile, helperBin, engineSh} {
		if b.exists(p) {
			t.Errorf("%s left behind", p)
		}
	}
	// The restored backup is gone (its content is installed)
	if ents, _ := os.ReadDir(filepath.Join(b.root, "/var/lib/eirl-update/backup")); len(ents) != 0 {
		t.Errorf("backups after the rollback: %v", ents)
	}
	// And a good one installs, keeping the pre-OTA install as the backup
	b.offer(newRelease("0.1.0", "0.1.0"))
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.read(update.VersionFile) != "0.1.0\n" {
		t.Error("not installed")
	}
	if _, err := os.Stat(filepath.Join(b.root, "/var/lib/eirl-update/backup/backup-0.0.0.tar")); err != nil {
		t.Error(err)
	}
}

func TestRefusals(t *testing.T) {
	cases := map[string]struct {
		setup func(b *board)
		code  string
	}{
		"streaming (state file)": {func(b *board) {
			b.offer(newRelease("0.2.0", "0.2.0"))
			b.write("/run/eirl-engine/state", "OUTPUT=on\n", 0o644)
		}, update.CodeStreaming},
		"streaming (mixer)": {func(b *board) {
			b.offer(newRelease("0.2.0", "0.2.0"))
			b.write("/run/eirl-engine/state", "OUTPUT=off\n", 0o644)
			b.write("/run/eirl-engine/mixer.json", `{"output":{"mode":"on","state":"connecting"}}`, 0o644)
		}, update.CodeStreaming},
		"same version": {func(b *board) { b.offer(newRelease("0.1.0", "0.1.0")) }, update.CodeNotNewer},
		"downgrade":    {func(b *board) { b.offer(newRelease("0.0.9", "0.0.9")) }, update.CodeNotNewer},
		"minFrom": {func(b *board) {
			r := newRelease("0.3.0", "0.3.0")
			r.minFrom = "0.2.0"
			b.offer(r)
		}, update.CodeMinFrom},
		"tampered bundle": {func(b *board) {
			r := newRelease("0.2.0", "0.2.0")
			r.tamper = true
			b.offer(r)
		}, update.CodeChecksum},
		"wrong key": {func(b *board) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			b.key = other
			b.offer(newRelease("0.2.0", "0.2.0"))
		}, update.CodeBadSignature},
		"tampered manifest": {func(b *board) {
			b.offer(newRelease("0.2.0", "0.2.0"))
			p := filepath.Join(b.root, b.appDir, update.ManifestFile)
			raw, _ := os.ReadFile(p)
			os.WriteFile(p, bytes.Replace(raw, []byte(`"v":1`), []byte(`"v":1 `), 1), 0o640)
		}, update.CodeBadSignature},
		"request for another version": {func(b *board) {
			b.offer(newRelease("0.2.0", "0.2.0"))
			req, _ := json.Marshal(update.Request{Version: "0.3.0"})
			os.WriteFile(filepath.Join(b.root, b.appDir, update.RequestFile), req, 0o640)
		}, "bad_request"},
		"no request": {func(b *board) {
			b.offer(newRelease("0.2.0", "0.2.0"))
			os.Remove(filepath.Join(b.root, b.appDir, update.RequestFile))
		}, "bad_request"},
		"symlinked bundle": {func(b *board) {
			b.offer(newRelease("0.2.0", "0.2.0"))
			p := filepath.Join(b.root, b.appDir, update.BundleFile)
			os.Rename(p, p+".real")
			os.Symlink(p+".real", p)
		}, update.CodeChecksum},
		"files of another owner": {func(b *board) {
			b.offer(newRelease("0.2.0", "0.2.0"))
			b.in.AppUID = os.Getuid() + 1
		}, "bad_request"},
		"bundle of another version": {func(b *board) {
			r := newRelease("0.2.0", "0.2.0")
			r.inside = "0.9.0"
			b.offer(r)
		}, "bad_bundle"},
		"files.txt outside the allowed paths": {func(b *board) {
			r := newRelease("0.2.0", "0.2.0")
			r.files["/etc/passwd"] = "root::0:0"
			b.offer(r)
		}, "bad_bundle"},
		"bundle with path traversal": {func(b *board) {
			r := newRelease("0.2.0", "0.2.0")
			r.extra = map[string]string{"../../escape": "x"}
			b.offer(r)
		}, "bad_bundle"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := newBoard(t, "0.1.0")
			c.setup(b)
			err := b.in.Install(context.Background())
			var f *Failure
			if !errors.As(err, &f) || f.Code != c.code {
				t.Fatalf("got %v, want code %s", err, c.code)
			}
			st := b.status()
			if st.State != update.HelperRefused || st.Error != c.code {
				t.Errorf("status %+v", st)
			}
			// Nothing was touched: no backup, no marker, same files, no restarts
			if b.read(appBin) != "app 0.1.0" || b.read(update.VersionFile) != "0.1.0\n" || b.exists(helperBin) || b.exists("/etc/passwd") {
				t.Error("files changed by a refused update")
			}
			if b.exists("/var/lib/eirl-update/in-progress.json") || len(b.sd.calls) != 0 {
				t.Errorf("marker or systemctl calls: %v", b.sd.calls)
			}
			if ents, _ := os.ReadDir(filepath.Join(b.root, "/var/lib/eirl-update/backup")); len(ents) != 0 {
				t.Errorf("backup made: %v", ents)
			}
		})
	}
}

func TestStreamingStartsDuringInstall(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.offer(newRelease("0.2.0", "0.2.0"))
	// The user starts streaming from the touch screen while install.sh runs
	orig := b.in.RunScript
	b.in.RunScript = func(ctx context.Context, dir string, out io.Writer) error {
		err := orig(ctx, dir, out)
		b.write("/run/eirl-engine/mixer.json", `{"output":{"mode":"on","state":"connecting"}}`, 0o644)
		return err
	}
	err := b.in.Install(context.Background())
	st := b.status()
	if err == nil || st.State != update.HelperRolledBack || st.Error != update.CodeStreaming {
		t.Fatalf("%v %+v", err, st)
	}
	if b.read(appBin) != "app 0.1.0" {
		t.Error("not restored")
	}
	if b.sd.called("restart " + EngineUnit) {
		t.Error("the engine was restarted while streaming")
	}
}

func TestEngineStoppedIsNotStarted(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.sd.active[EngineUnit] = false // stopped on purpose by the user
	b.offer(newRelease("0.2.0", "0.2.0"))
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.sd.called("restart " + EngineUnit) {
		t.Error("a stopped engine was started")
	}
}

func TestEngineNotPublishingRollsBack(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.sd.runDir = "" // the restarted engine never writes its state
	b.offer(newRelease("0.2.0", "0.2.0"))
	err := b.in.Install(context.Background())
	if st := b.status(); err == nil || st.State != update.HelperRolledBack || !strings.Contains(st.Reason, "engine") {
		t.Fatalf("%v %+v", err, b.status())
	}
}

func TestRecoverAfterPowerLoss(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.offer(newRelease("0.2.0", "0.2.0"))
	// The power goes off in the middle of install.sh: the new files are half in place
	orig := b.in.RunScript
	b.in.RunScript = func(ctx context.Context, dir string, out io.Writer) error {
		orig(ctx, dir, out)
		panic("power loss")
	}
	func() {
		defer func() { recover() }()
		b.in.Install(context.Background())
	}()
	if !b.exists("/var/lib/eirl-update/in-progress.json") || b.read(appBin) != "app 0.2.0" {
		t.Fatal("the simulated interruption did not leave the marker and the new files")
	}
	// Next boot: eirl-update-recover.service, before the encoder's units (no restarts)
	b.sd.calls = nil
	in2 := *b.in
	in2.RunScript = orig
	if err := in2.Recover(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if b.read(appBin) != "app 0.1.0" || b.read(update.VersionFile) != "0.1.0\n" || b.exists(helperBin) {
		t.Error("not restored at boot")
	}
	if b.exists("/var/lib/eirl-update/in-progress.json") {
		t.Error("marker left")
	}
	if st := b.status(); st.State != update.HelperRolledBack || st.Error != "interrupted" {
		t.Errorf("status %+v", st)
	}
	if b.sd.called("restart "+AppUnit) || !b.sd.called("daemon-reload") {
		t.Errorf("boot recovery calls: %v", b.sd.calls)
	}
	// Nothing to do on the next boot
	if err := in2.Recover(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

// A power cut runs no defers: what it left in staging must not make the next install fail
func TestLeftoverStagingAfterPowerLoss(t *testing.T) {
	b := newBoard(t, "0.1.0")
	stale := filepath.Join(b.root, "/var/lib/eirl-update/staging/bundle")
	os.MkdirAll(stale, 0o700)
	os.WriteFile(filepath.Join(stale, "VERSION"), []byte("0.2.0\n"), 0o644)
	b.offer(newRelease("0.2.0", "0.2.0"))
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatalf("install over a stale staging: %v", err)
	}
	// Recover empties it too
	os.MkdirAll(stale, 0o700)
	os.WriteFile(filepath.Join(stale, "VERSION"), []byte("x"), 0o644)
	b.write("/var/lib/eirl-update/in-progress.json", `{"from":"0.1.0","to":"0.2.0"}`, 0o600)
	b.in.Recover(context.Background(), false)
	if _, err := os.Stat(filepath.Join(stale, "VERSION")); err == nil {
		t.Error("Recover left the staging")
	}
}

func TestExtractRejects(t *testing.T) {
	mk := func(h *tar.Header, content string) string {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		h.Size = int64(len(content))
		tw.WriteHeader(h)
		tw.Write([]byte(content))
		tw.Close()
		gz.Close()
		p := filepath.Join(t.TempDir(), "b.tar.gz")
		os.WriteFile(p, buf.Bytes(), 0o600)
		return p
	}
	bad := []*tar.Header{
		{Name: "/etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "../x", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "a/../../x", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"},
		{Name: "hard", Typeflag: tar.TypeLink, Linkname: "x"},
		{Name: "dev", Typeflag: tar.TypeChar},
	}
	for _, h := range bad {
		dst := t.TempDir()
		if err := extractBundle(mk(h, ""), dst); err == nil {
			t.Errorf("%s (%c): accepted", h.Name, h.Typeflag)
		}
	}
	dst := t.TempDir()
	if err := extractBundle(mk(&tar.Header{Name: "./bin/x", Typeflag: tar.TypeReg, Mode: 0o4755}, "#!"), dst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "bin/x")); err != nil || fi.Mode().Perm() != 0o755 || fi.Mode()&os.ModeSetuid != 0 {
		t.Errorf("mode %v %v", fi.Mode(), err)
	}
}

func TestCheckApp(t *testing.T) {
	// A short path: unix sockets have a ~104-byte limit on macOS
	dir, err := os.MkdirTemp("/tmp", "eu")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	check := CheckApp(sock)
	if err := check(context.Background(), "0.2.0"); err == nil {
		t.Error("no socket: healthy")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	answer := `{"current":"0.2.0","app":"0.2.0"}`
	code := 200
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/update" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(code)
		w.Write([]byte(answer))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	ctx := context.Background()
	if err := check(ctx, "0.2.0"); err != nil {
		t.Errorf("healthy app: %v", err)
	}
	answer = `{"current":"0.2.0","app":"0.1.0"}` // VERSION written but the old binary runs
	if err := check(ctx, "0.2.0"); err == nil {
		t.Error("old binary accepted")
	}
	code = 403 // an app without /update on its socket: up, version not checked
	if err := check(ctx, "0.2.0"); err != nil {
		t.Errorf("403: %v", err)
	}
}

// After a failed attempt, the kept backup is still the previous release's: a manual rollback goes back
// one release, not to the release that is already installed.
func TestBackupRetention(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.offer(newRelease("0.2.0", "0.2.0"))
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.offer(newRelease("0.3.0", "broken"))
	if err := b.in.Install(context.Background()); err == nil {
		t.Fatal("broken release installed")
	}
	if b.read(update.VersionFile) != "0.2.0\n" {
		t.Fatal("not rolled back to 0.2.0")
	}
	ents, _ := os.ReadDir(filepath.Join(b.root, "/var/lib/eirl-update/backup"))
	if len(ents) != 1 || ents[0].Name() != "backup-0.1.0.tar" {
		t.Fatalf("backups after a failed attempt: %v", ents)
	}
	if err := b.in.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.read(update.VersionFile) != "0.1.0\n" || b.read(appBin) != "app 0.1.0" {
		t.Errorf("manual rollback: %q", b.read(update.VersionFile))
	}
	// Nothing left to roll back to
	err := b.in.Rollback(context.Background())
	if err == nil {
		t.Error("second manual rollback accepted")
	}
}

func TestManualRollbackRefusesForeignBackup(t *testing.T) {
	b := newBoard(t, "0.1.0")
	b.offer(newRelease("0.2.0", "0.2.0"))
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Someone installed 0.2.1 by hand since: the kept backup (0.1.0 → 0.2.0) is not its predecessor
	b.write(update.VersionFile, "0.2.1\n", 0o644)
	var f *Failure
	if err := b.in.Rollback(context.Background()); !errors.As(err, &f) || f.Code != "no_backup" {
		t.Errorf("foreign backup: %v", err)
	}
	if b.read(update.VersionFile) != "0.2.1\n" {
		t.Error("files changed")
	}
}

func engineRestarts(sd *fakeSystemd) int {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	n := 0
	for _, c := range sd.calls {
		if c == "restart "+EngineUnit {
			n++
		}
	}
	return n
}

// The new engine fails at once (exit 64, RestartPreventExitStatus: "failed", not restarted by systemd). The
// rollback restores the old engine and must start it: the marker says the install restarted it
func TestRollbackRestartsAFailedNewEngine(t *testing.T) {
	b := newBoard(t, "0.1.0")
	first := true
	b.sd.onRun = func(args []string) {
		if len(args) == 2 && args[0] == "restart" && args[1] == EngineUnit && first {
			first = false
			b.sd.mu.Lock()
			b.sd.active[EngineUnit] = false
			b.sd.mu.Unlock()
			os.Remove(filepath.Join(b.root, "/run/eirl-engine/state"))
		}
	}
	b.offer(newRelease("0.2.0", "0.2.0")) // healthy app, failing engine
	if err := b.in.Install(context.Background()); err == nil {
		t.Fatal("installed with a failed engine")
	}
	if !strings.Contains(b.read(engineSh), "engine 0.1.0") {
		t.Fatalf("engine not restored: %q", b.read(engineSh))
	}
	if n := engineRestarts(b.sd); n != 2 {
		t.Errorf("engine restarts = %d, want 2 (the restored engine would stay down): %v", n, b.sd.calls)
	}
}

// A rollback killed after the restore and before the engine restart (the marker stays): the next run restores
// identical files, and must still restart the engine, which runs the failed release's code
func TestInterruptedRollbackStillRestartsTheEngine(t *testing.T) {
	b := newBoard(t, "0.1.0")
	crash := false
	b.sd.onRun = func(args []string) {
		if crash && len(args) == 1 && args[0] == "daemon-reload" {
			crash = false
			runtime.Goexit() // the helper dies right after the restore (SIGKILL or timeout)
		}
	}
	orig := b.in.AppHealth
	b.in.AppHealth = func(ctx context.Context, v string) error {
		if engineRestarts(b.sd) == 1 {
			crash = true
		}
		return orig(ctx, v)
	}
	b.offer(newRelease("0.2.0", "broken")) // carries the engine; its app is unhealthy
	done := make(chan struct{})
	go func() { defer close(done); b.in.Install(context.Background()) }()
	<-done
	if !b.exists("/var/lib/eirl-update/in-progress.json") {
		t.Fatal("the marker did not survive the interrupted rollback")
	}
	if n := engineRestarts(b.sd); n != 1 {
		t.Fatalf("engine restarts before the second run = %d", n)
	}
	b.in.AppHealth = orig
	b.offer(newRelease("0.2.0", "broken"))
	b.in.Install(context.Background()) // sees the marker: rolls back again
	if n := engineRestarts(b.sd); n != 2 {
		t.Errorf("engine restarts = %d, want 2 (the engine would keep 0.2.0's code): %v", n, b.sd.calls)
	}
}

// A release whose engine code is identical but whose drop-in changed: the install restarted the engine under
// the new drop-in, the rollback restores the old one and restarts it again
func TestDropinOnlyChangeRestartsOnRollback(t *testing.T) {
	b := newBoard(t, "0.1.0")
	dropin := "/etc/systemd/system/eirl-engine.service.d/eirl-encoder.conf"
	b.write(dropin, "[Service]\nSupplementaryGroups=eirl-encoder\n", 0o644)
	b.write(update.FilesFile, strings.Join([]string{appBin, ctlLink, engineSh, sudoers, dropin,
		update.VersionFile, update.FilesFile}, "\n")+"\n", 0o644)
	r := newRelease("0.2.0", "broken")
	r.files[engineSh] = "#!/bin/bash\n# engine 0.1.0\n" // the same engine code
	r.files[dropin] = "[Service]\nSupplementaryGroups=eirl-encoder eirl-usbhub\n"
	b.offer(r)
	if err := b.in.Install(context.Background()); err == nil {
		t.Fatal("broken release installed")
	}
	if !strings.Contains(b.read(dropin), "SupplementaryGroups=eirl-encoder\n") {
		t.Fatalf("drop-in not restored: %q", b.read(dropin))
	}
	if n := engineRestarts(b.sd); n != 2 {
		t.Errorf("engine restarts = %d, want 2 (the engine would keep the failed release's drop-in): %v", n, b.sd.calls)
	}
}

// The restore counts a changed engine unit or drop-in as an engine change (a manual rollback has no marker)
func TestManualRollbackOfADropinChange(t *testing.T) {
	b := newBoard(t, "0.1.0")
	dropin := "/etc/systemd/system/eirl-engine.service.d/eirl-encoder.conf"
	b.write(dropin, "[Service]\nSupplementaryGroups=eirl-encoder\n", 0o644)
	b.write(update.FilesFile, strings.Join([]string{appBin, sudoers, dropin, update.VersionFile, update.FilesFile}, "\n")+"\n", 0o644)
	r := newRelease("0.2.0", "0.2.0")
	delete(r.files, engineSh) // app-only: the install does not restart the engine
	r.files[dropin] = "[Service]\nSupplementaryGroups=eirl-encoder eirl-usbhub\n"
	b.offer(r)
	if err := b.in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := engineRestarts(b.sd); n != 0 {
		t.Fatalf("an app-only install restarted the engine")
	}
	if err := b.in.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := engineRestarts(b.sd); n != 1 {
		t.Errorf("engine restarts after rolling back a drop-in change = %d, want 1: %v", n, b.sd.calls)
	}
}
