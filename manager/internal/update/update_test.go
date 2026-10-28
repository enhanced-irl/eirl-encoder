package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestParseVersion(t *testing.T) {
	good := []string{"0.0.0", "0.1.0", "1.2.3", "10.20.30", "0.0.0-dev+abc1234", "0.1.1-test.1", "1.0.0-alpha.beta+exp.sha.5114f85", "0.0.0-dev+abc.dirty"}
	for _, s := range good {
		v, err := ParseVersion(s)
		if err != nil {
			t.Errorf("%q: %v", s, err)
			continue
		}
		if v.String() != s {
			t.Errorf("%q round-trips as %q", s, v.String())
		}
	}
	bad := []string{"", "v1.0.0", "1.0", "1.0.0.0", "01.0.0", "1.00.0", "1.0.0-", "1.0.0-01", "1.0.0+", "1.0.0-a..b", "1.0.0-a_b", "x.y.z", "1.0.0 "}
	for _, s := range bad {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("%q: want error", s)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	// Ascending, per semver §11
	order := []string{"0.0.0-dev+zzz", "0.0.0", "0.1.0-test.1", "0.1.0-test.2", "0.1.0-test.10", "0.1.0", "0.1.1",
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "2.0.0", "10.0.0"}
	for i := range order {
		for j := range order {
			a, b := MustVersion(order[i]), MustVersion(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	if MustVersion("1.0.0+a").Compare(MustVersion("1.0.0+b")) != 0 {
		t.Error("build metadata must not count")
	}
}

// --- signing helpers --------------------------------------------------------------------------------

type testKey struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{pub, priv}
}

func (k testKey) sign(b []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, b)) + "\n")
}

func manifestFor(version string, bundle []byte) Manifest {
	sum := sha256.Sum256(bundle)
	return Manifest{V: 1, Product: Product, Version: version, Released: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		Notes:  map[string]string{"en": "Fixes.", "es": "Arreglos."},
		Bundle: Bundle{Name: BundleName(version), Size: int64(len(bundle)), SHA256: hex.EncodeToString(sum[:])}}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyManifest(t *testing.T) {
	k, other := newKey(t), newKey(t)
	m := manifestFor("0.2.0", []byte("bundle"))
	raw := mustJSON(t, m)
	keys := []ed25519.PublicKey{k.pub}

	got, err := VerifyManifest(raw, k.sign(raw), keys)
	if err != nil {
		t.Fatalf("good manifest: %v", err)
	}
	if got.Version != "0.2.0" || got.Bundle.Size != 6 || got.Note("fr") != "Fixes." || got.Note("es") != "Arreglos." {
		t.Errorf("parsed %+v", got)
	}

	cases := []struct {
		name string
		raw  []byte
		sig  []byte
		keys []ed25519.PublicKey
		code string
	}{
		{"wrong key", raw, other.sign(raw), keys, CodeBadSignature},
		{"no key", raw, k.sign(raw), nil, CodeBadSignature},
		{"tampered byte", append([]byte{}, raw...), k.sign(raw), keys, CodeBadSignature},
		{"extra whitespace", append(append([]byte{}, raw...), ' '), k.sign(raw), keys, CodeBadSignature},
		{"garbage signature", raw, []byte("not base64!"), keys, CodeBadSignature},
		{"short signature", raw, []byte(base64.StdEncoding.EncodeToString([]byte("short"))), keys, CodeBadSignature},
		{"empty manifest", nil, k.sign(nil), keys, CodeBadManifest},
	}
	cases[2].raw[10] ^= 1
	for _, c := range cases {
		_, err := VerifyManifest(c.raw, c.sig, c.keys)
		if Code(err) != c.code {
			t.Errorf("%s: code %q (%v), want %q", c.name, Code(err), err, c.code)
		}
	}

	// Signed but invalid: the signature holds, the content does not
	bad := map[string]func(*Manifest){
		"v2":           func(m *Manifest) { m.V = 2 },
		"product":      func(m *Manifest) { m.Product = "other" },
		"version":      func(m *Manifest) { m.Version = "v0.2" },
		"bundle name":  func(m *Manifest) { m.Bundle.Name = "../../etc/passwd" },
		"name/version": func(m *Manifest) { m.Bundle.Name = BundleName("0.3.0") },
		"size":         func(m *Manifest) { m.Bundle.Size = 0 },
		"huge":         func(m *Manifest) { m.Bundle.Size = MaxBundleSize + 1 },
		"sha":          func(m *Manifest) { m.Bundle.SHA256 = "XYZ" },
		"minFrom":      func(m *Manifest) { m.MinFrom = "one" },
		"notes lang":   func(m *Manifest) { m.Notes = map[string]string{"english": "x"} },
	}
	for name, mut := range bad {
		mm := manifestFor("0.2.0", []byte("bundle"))
		mut(&mm)
		r := mustJSON(t, mm)
		if _, err := VerifyManifest(r, k.sign(r), keys); Code(err) != CodeBadManifest {
			t.Errorf("%s: code %q (%v), want bad_manifest", name, Code(err), err)
		}
	}
	// Unknown fields of a newer manifest are accepted
	r := []byte(strings.Replace(string(raw), `"v": 1,`, `"v": 1, "future": {"x": 1},`, 1))
	if _, err := VerifyManifest(r, k.sign(r), keys); err != nil {
		t.Errorf("unknown field: %v", err)
	}
}

func TestProductionKey(t *testing.T) {
	keys, err := TrustedKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("TrustedKeys: %v", err)
	}
	if TestBuild() {
		t.Error("tests run with the production key")
	}
	b, err := os.ReadFile("../../../release/eirl-release.pub")
	if err != nil {
		t.Fatalf("release/eirl-release.pub: %v", err)
	}
	if strings.TrimSpace(string(b)) != ProductionKey {
		t.Error("release/eirl-release.pub differs from the compiled-in key")
	}
}

func TestCheckUpgrade(t *testing.T) {
	cases := []struct {
		installed, release, minFrom string
		code                        string
	}{
		{"0.1.0", "0.2.0", "", ""},
		{"", "0.1.0", "", ""},              // pre-OTA install
		{"garbage", "0.1.0", "", ""},       // unreadable = 0.0.0
		{"0.0.0-dev+abc", "0.1.0", "", ""}, // dev build
		{"0.2.0", "0.2.0", "", CodeNotNewer},
		{"0.2.0", "0.1.9", "", CodeNotNewer}, // downgrade / replay of an old manifest
		{"0.2.0", "0.2.0-rc.1", "", CodeNotNewer},
		{"0.1.0", "0.3.0", "0.2.0", CodeMinFrom},
		{"0.2.0", "0.3.0", "0.2.0", ""},
		{"", "0.3.0", "0.2.0", CodeMinFrom},
		{"0.0.0-dev+abc", "0.3.0", "0.1.0", CodeMinFrom},
	}
	for _, c := range cases {
		m := manifestFor(c.release, []byte("x"))
		m.MinFrom = c.minFrom
		if got := Code(CheckUpgrade(&m, c.installed)); got != c.code {
			t.Errorf("installed %q → %s (minFrom %q): %q, want %q", c.installed, c.release, c.minFrom, got, c.code)
		}
	}
}

func TestParseFiles(t *testing.T) {
	ok := "# comment\n/usr/local/bin/eirl-encoderd\n\n/usr/lib/eirl-encoder/engine/VERSION\n/usr/local/bin/eirl-encoderd\n/boot/extlinux/extlinux.conf\n"
	got, err := ParseFiles([]byte(ok))
	if err != nil || len(got) != 3 || got[0] != "/boot/extlinux/extlinux.conf" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"/etc/passwd", "relative/path", "/usr/local/bin/../../etc/shadow", "/etc/eirl-encoder/admin.hash",
		"/usr/local/bin/", "/boot/Image", "/etc/systemd/system//x"} {
		if _, err := ParseFiles([]byte(bad + "\n")); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestStreaming(t *testing.T) {
	dir := t.TempDir()
	if on, _ := Streaming(dir); on {
		t.Error("empty run dir: streaming")
	}
	w := func(name, s string) { os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644) }
	w("state", "OUTPUT=on\nSOURCE=hdmi\n")
	if on, _ := Streaming(dir); !on {
		t.Error("state OUTPUT=on without mixer.json: not streaming")
	}
	w("mixer.json", `{"output":{"mode":"off","state":"off"}}`)
	if on, why := Streaming(dir); on {
		t.Errorf("mixer output off (stopped live after starting on): streaming (%s)", why)
	}
	w("mixer.json", `{"output":{"mode":"on","state":"error"}}`)
	if on, _ := Streaming(dir); !on {
		t.Error("mixer mode on: not streaming")
	}
	w("mixer.json", `{"output":{"mode":"off","state":"connecting"}}`)
	if on, _ := Streaming(dir); !on {
		t.Error("mixer connecting: not streaming")
	}
	w("mixer.json", `{"output":{"mode":"off","state":"off"},"record":{"mode":"on","state":"on"}}`)
	if on, why := Streaming(dir); !on || why != "mixer.json record.mode=on" {
		t.Errorf("recording to a USB disk: not busy (%s)", why)
	}
	w("mixer.json", `{"scene":{}}`) // an engine without output in its state: the state file decides
	if on, _ := Streaming(dir); !on {
		t.Error("mixer.json without output + OUTPUT=on: not streaming")
	}
}

// The engine's user writes these files and root reads them: links, FIFOs and huge files are not followed,
// waited on or read whole
func TestStreamingReadsSafely(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	os.WriteFile(outside, []byte(`{"output":{"mode":"on"}}`), 0o644)
	os.Symlink(outside, filepath.Join(dir, "mixer.json"))
	if on, why := Streaming(dir); on {
		t.Errorf("followed a symlink: %s", why)
	}
	os.Remove(filepath.Join(dir, "mixer.json"))
	if err := unix.Mkfifo(filepath.Join(dir, "mixer.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { on, _ := Streaming(dir); done <- on }()
	select {
	case on := <-done:
		if on {
			t.Error("a FIFO said streaming")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
	os.Remove(filepath.Join(dir, "mixer.json"))
	big := `{"output":{"mode":"on"},"pad":"` + strings.Repeat("x", maxEngineFile) + `"}`
	os.WriteFile(filepath.Join(dir, "mixer.json"), []byte(big), 0o644)
	if on, _ := Streaming(dir); on {
		t.Error("read a file larger than the limit")
	}
	// The state file has the same rules
	os.Remove(filepath.Join(dir, "mixer.json"))
	os.WriteFile(outside, []byte("OUTPUT=on\n"), 0o644)
	os.Symlink(outside, filepath.Join(dir, "state"))
	if on, _ := Streaming(dir); on {
		t.Error("followed a symlinked state file")
	}
}

func TestInstalledVersions(t *testing.T) {
	root := t.TempDir()
	if v := InstalledVersions(root, "x"); v.Release != "" || v.App != "x" {
		t.Errorf("%+v", v)
	}
	os.MkdirAll(filepath.Join(root, LibDir, "engine"), 0o755)
	os.WriteFile(filepath.Join(root, VersionFile), []byte("0.1.0\n"), 0o644)
	os.WriteFile(filepath.Join(root, BuildFile), []byte("commit=abc1234 date=2026-10-14T00:00:00Z\n"), 0o644)
	os.WriteFile(filepath.Join(root, EngineVerTxt), []byte("0.1.0 abc1234 release · 2026-10-14\n"), 0o644)
	v := InstalledVersions(root, "0.1.0")
	if v.Release != "0.1.0" || v.Engine != "0.1.0" || v.Commit != "abc1234" {
		t.Errorf("%+v", v)
	}
	os.WriteFile(filepath.Join(root, VersionFile), []byte("not a version\n"), 0o644)
	if InstalledRelease(root) != "" {
		t.Error("invalid VERSION accepted")
	}
}

func TestHasDefaultRoute(t *testing.T) {
	dir := t.TempDir()
	r4, r6 := filepath.Join(dir, "route"), filepath.Join(dir, "ipv6_route")
	hdr := "Iface\tDestination\tGateway \tFlags\n"
	os.WriteFile(r4, []byte(hdr+"eth0\t0015A8C0\t00000000\t0001\n"), 0o644)
	if HasDefaultRoute(r4, r6) {
		t.Error("no default route")
	}
	os.WriteFile(r4, []byte(hdr+"eth0\t00000000\t0115A8C0\t0003\n"), 0o644)
	if !HasDefaultRoute(r4, r6) {
		t.Error("default route not seen")
	}
}

// --- release server -----------------------------------------------------------------------------------

type release struct {
	srv      *httptest.Server
	manifest []byte
	sig      []byte
	bundle   []byte
	hits     atomic.Int32
	slow     chan struct{} // non-nil: the bundle handler blocks until closed
}

func serveRelease(t *testing.T, k testKey, version string, bundle []byte) *release {
	t.Helper()
	r := &release{bundle: bundle}
	m := manifestFor(version, bundle)
	r.manifest = mustJSON(t, m)
	r.sig = k.sign(r.manifest)
	mux := http.NewServeMux()
	// Like GitHub: latest/download → the release's tag
	mux.HandleFunc("/releases/latest/download/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/releases/download/v"+version+"/"+filepath.Base(req.URL.Path), http.StatusFound)
	})
	mux.HandleFunc("/releases/download/v"+version+"/", func(w http.ResponseWriter, req *http.Request) {
		switch filepath.Base(req.URL.Path) {
		case "manifest.json":
			w.Write(r.manifest)
		case "manifest.json.sig":
			w.Write(r.sig)
		case m.Bundle.Name:
			r.hits.Add(1)
			// Storage host: GitHub redirects assets to another host
			http.Redirect(w, req, "/storage/"+m.Bundle.Name+"?token=secret", http.StatusFound)
		default:
			http.NotFound(w, req)
		}
	})
	mux.HandleFunc("/storage/", func(w http.ResponseWriter, req *http.Request) {
		if r.slow != nil {
			w.Header().Set("Content-Length", "1000000")
			w.WriteHeader(200)
			w.Write(r.bundle[:1])
			w.(http.Flusher).Flush()
			<-r.slow
			return
		}
		w.Write(r.bundle)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *release) url() string { return r.srv.URL + "/releases/latest/download/manifest.json" }

func TestFetchAndDownload(t *testing.T) {
	k := newKey(t)
	bundle := []byte(strings.Repeat("bundle-bytes ", 10000))
	r := serveRelease(t, k, "0.2.0", bundle)
	f := &Fetcher{}
	fd, err := f.FetchManifest(context.Background(), r.url())
	if err != nil {
		t.Fatal(err)
	}
	m, err := VerifyManifest(fd.Raw, fd.Sig, []ed25519.PublicKey{k.pub})
	if err != nil {
		t.Fatal(err)
	}
	if want := r.srv.URL + "/releases/download/v0.2.0/" + m.Bundle.Name; fd.BundleURL(m) != want {
		t.Errorf("bundle URL %s, want %s (the tag the redirect named)", fd.BundleURL(m), want)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, BundleFile)
	var last int64
	if err := f.Download(context.Background(), fd.BundleURL(m), dst, m.Bundle.Size, m.Bundle.SHA256, func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if last != int64(len(bundle)) {
		t.Errorf("progress %d", last)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(bundle) {
		t.Error("bundle content")
	}

	// Size and checksum mismatches leave nothing behind
	os.Remove(dst)
	err = f.Download(context.Background(), fd.BundleURL(m), dst, m.Bundle.Size-1, m.Bundle.SHA256, nil)
	if Code(err) != CodeChecksum {
		t.Errorf("short manifest size: %v", err)
	}
	err = f.Download(context.Background(), fd.BundleURL(m), dst, m.Bundle.Size+5, m.Bundle.SHA256, nil)
	if Code(err) != CodeChecksum {
		t.Errorf("long manifest size: %v", err)
	}
	err = f.Download(context.Background(), fd.BundleURL(m), dst, m.Bundle.Size, strings.Repeat("0", 64), nil)
	if Code(err) != CodeChecksum {
		t.Errorf("wrong sha: %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("left files: %v", ents)
	}
	// A server that sends nothing more: the stall watchdog stops it
	r.slow = make(chan struct{})
	defer close(r.slow)
	f2 := &Fetcher{Stall: 200 * time.Millisecond}
	err = f2.Download(context.Background(), fd.BundleURL(m), dst, 1000000, m.Bundle.SHA256, nil)
	if Code(err) != CodeNetwork || !strings.Contains(err.Error(), "no data") {
		t.Errorf("stall: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Error("the storage URL's token leaked into an error")
	}
}

// The client's errors name the URL of the last redirect: its signed query never reaches a log
func TestErrorsDoNotCarryTheSignedURL(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close() // nothing listens there any more: connection refused
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+dead+"/asset?X-Signature=secret-token", http.StatusFound)
	}))
	defer srv.Close()
	f := &Fetcher{}
	_, err := f.FetchManifest(context.Background(), srv.URL+"/manifest.json")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("manifest error: %v", err)
	}
	err = f.Download(context.Background(), srv.URL+"/bundle", filepath.Join(t.TempDir(), "b"), 10, strings.Repeat("0", 64), nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("download error: %v", err)
	}
}

func TestChecksumBackoff(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("good bundle"))
	r.bundle = []byte("evil bundle") // the asset does not match its manifest
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	u, _ := newUpdater(t, k, r.url(), &fakeEnv{})
	u.cfg.Now = func() time.Time { return now }
	ctx := context.Background()
	wait := func() time.Duration { u.mu.Lock(); defer u.mu.Unlock(); return u.nextDL.Sub(now) }

	u.checkAndDownload(ctx, false)
	if r.hits.Load() != 1 || wait() != 30*time.Minute {
		t.Fatalf("first failure: hits %d, wait %v", r.hits.Load(), wait())
	}
	u.checkAndDownload(ctx, false) // an automatic check meanwhile does not download again
	if r.hits.Load() != 1 {
		t.Errorf("automatic check ignored the backoff: hits %d", r.hits.Load())
	}
	for i, want := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour} {
		now = now.Add(wait() + time.Minute)
		u.periodic(ctx)
		if r.hits.Load() != int32(i+2) || wait() != want {
			t.Fatalf("failure %d: hits %d, wait %v (want %v)", i+2, r.hits.Load(), wait(), want)
		}
	}
	u.checkAndDownload(ctx, true) // a manual check retries now
	if r.hits.Load() != 5 {
		t.Errorf("manual check: hits %d", r.hits.Load())
	}
	if checksumBackoff(20) != 24*time.Hour {
		t.Error("no cap")
	}
}

func TestRedirectToHTTPRefused(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.invalid/x", nil)
	via, _ := http.NewRequest("GET", "https://example.invalid/y", nil)
	if checkRedirect(req, []*http.Request{via}) == nil {
		t.Error("https → http redirect accepted")
	}
}

// --- state machine ----------------------------------------------------------------------------------------

type fakeEnv struct {
	streaming atomic.Bool
	started   atomic.Int32
	current   atomic.Value
}

func newUpdater(t *testing.T, k testKey, url string, env *fakeEnv) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	env.current.Store("0.1.0")
	u := New(Config{
		ManifestURL: url, Dir: dir, Keys: []ed25519.PublicKey{k.pub},
		Current:          func() string { return env.current.Load().(string) },
		Streaming:        env.streaming.Load,
		StartHelper:      func(context.Context) error { env.started.Add(1); return nil },
		HelperStatusPath: filepath.Join(dir, "helper-status.json"),
		AutoCheck:        true,
	})
	return u, dir
}

func TestUpdaterCheckDownloadInstall(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("the bundle"))
	env := &fakeEnv{}
	u, dir := newUpdater(t, k, r.url(), env)
	ctx := context.Background()

	if err := u.Install(ctx); !errors.Is(err, ErrNotReady) {
		t.Fatalf("install before check: %v", err)
	}
	u.checkAndDownload(ctx, true)
	st := u.Status()
	if st.State != StateReady || st.Available == nil || st.Available.Version != "0.2.0" || st.Progress != 1 || st.LastCheck.IsZero() {
		t.Fatalf("after check: %+v", st)
	}
	for _, f := range []string{ManifestFile, SigFile, BundleFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not stored: %v", f, err)
		}
	}
	// A second check of the same release does not download again
	u.checkAndDownload(ctx, false)
	if r.hits.Load() != 1 {
		t.Errorf("bundle fetched %d times", r.hits.Load())
	}

	env.streaming.Store(true)
	if err := u.Install(ctx); !errors.Is(err, ErrStreaming) {
		t.Fatalf("install while streaming: %v", err)
	}
	if u.Status().CanInstallNow() {
		t.Error("can install while streaming")
	}
	env.streaming.Store(false)
	if err := u.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if env.started.Load() != 1 || u.Status().State != StateInstalling {
		t.Fatalf("helper started %d, state %s", env.started.Load(), u.Status().State)
	}
	var req Request
	b, _ := os.ReadFile(filepath.Join(dir, RequestFile))
	if json.Unmarshal(b, &req) != nil || req.Version != "0.2.0" {
		t.Errorf("request.json %s", b)
	}
	if err := u.Install(ctx); !errors.Is(err, ErrBusy) {
		t.Errorf("second install: %v", err)
	}
	if err := u.Check(); !errors.Is(err, ErrBusy) {
		t.Errorf("check while installing: %v", err)
	}

	// The helper finishes: installed. The app sees the new version and cleans up.
	env.current.Store("0.2.0")
	u.applyResult(&HelperStatus{State: HelperInstalled, From: "0.1.0", To: "0.2.0", StartedAt: time.Now(), FinishedAt: time.Now()})
	st = u.Status()
	if st.State != StateIdle || st.Available != nil || st.LastResult == nil || st.LastResult.State != HelperInstalled {
		t.Errorf("after install: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, BundleFile)); err == nil {
		t.Error("bundle kept after install")
	}
	// Up to date now
	u.checkAndDownload(ctx, false)
	if st := u.Status(); st.State != StateIdle || st.Available != nil {
		t.Errorf("up to date: %+v", st)
	}
}

func TestUpdaterRejectsBadReleases(t *testing.T) {
	k, other := newKey(t), newKey(t)
	ctx := context.Background()

	// Signed with another key
	r := serveRelease(t, other, "0.2.0", []byte("x"))
	u, dir := newUpdater(t, k, r.url(), &fakeEnv{})
	u.checkAndDownload(ctx, true)
	if st := u.Status(); st.State != StateError || st.Error != CodeBadSignature || st.Available != nil {
		t.Errorf("wrong key: %+v", st)
	}
	if r.hits.Load() != 0 {
		t.Error("bundle downloaded for an unsigned manifest")
	}

	// A tampered bundle: the manifest is good, the bytes are not
	r = serveRelease(t, k, "0.2.0", []byte("good bundle"))
	r.bundle = []byte("evil bundle")
	u, dir = newUpdater(t, k, r.url(), &fakeEnv{})
	u.checkAndDownload(ctx, true)
	if st := u.Status(); st.State != StateError || st.Error != CodeChecksum || st.Ready {
		t.Errorf("tampered bundle: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, BundleFile)); err == nil {
		t.Error("tampered bundle kept")
	}
	if err := u.Install(ctx); !errors.Is(err, ErrNotReady) {
		t.Errorf("install of a tampered bundle: %v", err)
	}

	// An older release (replay): nothing offered
	r = serveRelease(t, k, "0.0.9", []byte("x"))
	u, _ = newUpdater(t, k, r.url(), &fakeEnv{})
	u.checkAndDownload(ctx, true)
	if st := u.Status(); st.State != StateIdle || st.Available != nil {
		t.Errorf("older release: %+v", st)
	}
}

func TestUpdaterNoDownloadWhileStreaming(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("x"))
	env := &fakeEnv{}
	env.streaming.Store(true)
	u, _ := newUpdater(t, k, r.url(), env)
	u.checkAndDownload(context.Background(), false)
	if st := u.Status(); st.State != StateAvailable || st.Ready || r.hits.Load() != 0 {
		t.Fatalf("streaming: %+v, hits %d", st, r.hits.Load())
	}
	env.streaming.Store(false)
	u.mu.Lock()
	u.nextDL = time.Time{}
	u.mu.Unlock()
	u.periodic(context.Background())
	if st := u.Status(); st.State != StateReady {
		t.Errorf("after streaming: %+v", st)
	}
}

func TestUpdaterRestartAndRollbackResult(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("the bundle"))
	env := &fakeEnv{}
	u, dir := newUpdater(t, k, r.url(), env)
	ctx := context.Background()
	u.checkAndDownload(ctx, true)
	if err := u.Install(ctx); err != nil {
		t.Fatal(err)
	}
	// The install restarts the app: a new Updater on the same directory sees the helper still working
	hsPath := filepath.Join(dir, "helper-status.json")
	writeHS := func(hs HelperStatus) { b, _ := json.Marshal(hs); os.WriteFile(hsPath, b, 0o644) }
	start := time.Now().UTC()
	writeHS(HelperStatus{State: HelperInstalling, Phase: "health", From: "0.1.0", To: "0.2.0", StartedAt: start})
	u2 := New(u.cfg)
	u2.load(ctx)
	if st := u2.Status(); st.State != StateInstalling || !st.Ready {
		t.Fatalf("restarted app: %+v", st)
	}
	// The new version was not healthy: rolled back
	writeHS(HelperStatus{State: HelperRolledBack, From: "0.1.0", To: "0.2.0", StartedAt: start, FinishedAt: time.Now().UTC(),
		Error: "unhealthy", Reason: "the app does not answer"})
	hs := u2.readHelper()
	u2.applyResult(hs)
	st := u2.Status()
	if st.State != StateRolledBack || st.Error != "unhealthy" || !st.Ready || st.Available == nil {
		t.Fatalf("rolled back: %+v", st)
	}
	// Shown again after another restart, until dismissed by a manual check
	u3 := New(u.cfg)
	u3.load(ctx)
	if st := u3.Status(); st.State != StateRolledBack {
		t.Fatalf("rolled back after restart: %+v", st)
	}
	if err := u3.Check(); err != nil {
		t.Fatal(err)
	}
	u3.checkAndDownload(ctx, true)
	if st := u3.Status(); st.State != StateReady {
		t.Errorf("after a manual check: %+v", st)
	}
	u4 := New(u.cfg)
	u4.load(ctx)
	if st := u4.Status(); st.State != StateReady {
		t.Errorf("dismissed result came back: %+v", st)
	}
	// Retrying the install is possible
	if err := u4.Install(ctx); err != nil {
		t.Errorf("retry: %v", err)
	}
}

// After `eirl-update rollback` the restarted app shows it like an automatic rollback, and at once: the
// helper's result is read before the stored bundle is hashed again
func TestManualRollbackShownAtOnce(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("the bundle"))
	u, dir := newUpdater(t, k, r.url(), &fakeEnv{})
	u.checkAndDownload(context.Background(), true) // a verified download is stored
	hs := HelperStatus{State: HelperRolledBack, Phase: "rollback", From: "0.1.1", To: "0.1.0", Error: "manual",
		Reason: "rolled back by the operator to 0.1.0", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	b, _ := json.Marshal(hs)
	os.WriteFile(filepath.Join(dir, "helper-status.json"), b, 0o644)

	u2 := New(u.cfg)
	u2.loadState() // the cheap part only: the bundle is not verified yet
	st := u2.Status()
	if st.State != StateRolledBack || st.Error != "manual" || st.LastResult == nil || st.LastResult.Error != "manual" {
		t.Fatalf("before the bundle check: %+v", st)
	}
	u2.loadDownload()
	if st := u2.Status(); st.State != StateRolledBack || !st.Ready || st.Available == nil {
		t.Errorf("after the bundle check: %+v", st)
	}
}

func TestUpdaterRefusedByHelper(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("the bundle"))
	u, _ := newUpdater(t, k, r.url(), &fakeEnv{})
	u.checkAndDownload(context.Background(), true)
	u.Install(context.Background())
	u.applyResult(&HelperStatus{State: HelperRefused, Error: CodeStreaming, Reason: "streaming", StartedAt: time.Now()})
	if st := u.Status(); st.State != StateReady || st.Error != CodeStreaming {
		t.Errorf("refused: %+v", st)
	}
}

func TestStoredDownloadReverified(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("the bundle"))
	u, dir := newUpdater(t, k, r.url(), &fakeEnv{})
	u.checkAndDownload(context.Background(), true)
	// Someone changes the stored bundle: after a restart it is not ready any more
	os.WriteFile(filepath.Join(dir, BundleFile), []byte("the bundlX"), 0o640)
	u2 := New(u.cfg)
	u2.load(context.Background())
	if st := u2.Status(); st.Ready || st.State != StateIdle {
		t.Errorf("tampered stored bundle: %+v", st)
	}
}

// A channel with nothing published yet (the manifest is a 404): up to date, not a network failure. A missing
// signature next to a manifest is still an error (a broken release)
func TestNoReleasePublishedYet(t *testing.T) {
	k := newKey(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	u, _ := newUpdater(t, k, srv.URL+"/latest/download/manifest.json", &fakeEnv{})
	u.checkAndDownload(context.Background(), true)
	if st := u.Status(); st.State != StateIdle || st.Error != "" || st.LastCheck.IsZero() || st.Available != nil {
		t.Fatalf("no release: %+v", st)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) })
	half := httptest.NewServer(mux)
	defer half.Close()
	if _, err := (&Fetcher{}).FetchManifest(context.Background(), half.URL+"/manifest.json"); errors.Is(err, ErrNoRelease) || Code(err) != CodeNetwork {
		t.Errorf("manifest without its signature: %v (%s)", err, Code(err))
	}
}

func TestUpdaterWaitsForUnmeteredLink(t *testing.T) {
	k := newKey(t)
	r := serveRelease(t, k, "0.2.0", []byte("x"))
	u, _ := newUpdater(t, k, r.url(), &fakeEnv{})
	var metered atomic.Bool
	metered.Store(true)
	u.cfg.Metered = metered.Load
	// An automatic check on a modem or a phone: known, not downloaded, and it says why
	u.checkAndDownload(context.Background(), false)
	if st := u.Status(); st.State != StateAvailable || st.Ready || st.DownloadWaits != "metered" || r.hits.Load() != 0 {
		t.Fatalf("metered, automatic: %+v, hits %d", st, r.hits.Load())
	}
	// Later, still metered: it keeps waiting
	u.mu.Lock()
	u.nextDL = time.Time{}
	u.mu.Unlock()
	u.periodic(context.Background())
	if r.hits.Load() != 0 {
		t.Fatal("downloaded on a metered link by itself")
	}
	// On Wi-Fi or a cable it downloads by itself
	metered.Store(false)
	u.mu.Lock()
	u.nextDL = time.Time{}
	u.mu.Unlock()
	u.periodic(context.Background())
	if st := u.Status(); st.State != StateReady || st.DownloadWaits != "" {
		t.Errorf("unmetered: %+v", st)
	}
	// A manual check downloads at once, metered or not
	r2 := serveRelease(t, k, "0.2.0", []byte("y"))
	u2, _ := newUpdater(t, k, r2.url(), &fakeEnv{})
	u2.cfg.Metered = func() bool { return true }
	u2.checkAndDownload(context.Background(), true)
	if st := u2.Status(); st.State != StateReady || st.DownloadWaits != "" {
		t.Errorf("manual on a metered link: %+v", st)
	}
}

func TestMeteredDefaultRoute(t *testing.T) {
	dir := t.TempDir()
	r4 := filepath.Join(dir, "route")
	sys := filepath.Join(dir, "net")
	dev := filepath.Join(dir, "devices")
	link := func(iface, devPath, driver string, wireless bool) {
		os.MkdirAll(filepath.Join(dev, devPath, "drv"), 0o755)
		os.MkdirAll(filepath.Join(sys, iface), 0o755)
		os.Symlink(filepath.Join(dev, devPath), filepath.Join(sys, iface, "device"))
		os.Symlink("/sys/bus/usb/drivers/"+driver, filepath.Join(dev, devPath, "driver"))
		if wireless {
			os.MkdirAll(filepath.Join(sys, iface, "wireless"), 0o755)
		}
	}
	link("eth0", "platform/eth", "eqos", false)
	link("wlan0", "platform/wlan", "iwlwifi", true)
	link("eth1", "platform/usb1/1-2/1-2:1.0", "ipheth", false)
	link("eth2", "platform/usb1/1-3/1-3:1.0", "r8152", false)
	link("usb0", "platform/usb1/1-4/1-4:1.0", "cdc_ether", false)
	hdr := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n"
	route := func(rows ...string) {
		os.WriteFile(r4, []byte(hdr+strings.Join(rows, "\n")+"\n"), 0o644)
	}
	for _, c := range []struct {
		rows  []string
		want  bool
		iface string
	}{
		{[]string{"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000"}, false, "eth0"},
		{[]string{"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000"}, false, "wlan0"},
		{[]string{"eth1\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000"}, true, "eth1"},  // an iPhone
		{[]string{"usb0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000"}, true, "usb0"},  // a HiLink modem
		{[]string{"eth2\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000"}, false, "eth2"}, // a USB Ethernet adapter
		{[]string{"wwan0\t00000000\t0101A8C0\t0003\t0\t0\t700\t00000000"}, true, "wwan0"},
		// The lowest metric wins: the cable before the iPhone
		{[]string{"eth1\t00000000\t0101A8C0\t0003\t0\t0\t300\t00000000", "eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000"}, false, "eth0"},
		{nil, false, ""},
	} {
		route(c.rows...)
		got, iface := MeteredDefaultRoute(r4, sys)
		if got != c.want || iface != c.iface {
			t.Errorf("%v: metered %v via %q, want %v via %q", c.rows, got, iface, c.want, c.iface)
		}
	}
}
