package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Files of the app's update directory (-update-dir, /var/lib/eirl-encoder/update). The root helper reads
// them as DATA and re-verifies everything (cmd/eirl-update): their names are fixed, nothing else is passed.
const (
	ManifestFile = "manifest.json"
	SigFile      = "manifest.json.sig"
	BundleFile   = "bundle.tar.gz"
	RequestFile  = "request.json"
	stateFile    = "state.json"
)

// States of the app's update state machine (GET /update, CONTRACT §4).
const (
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateAvailable   = "available"
	StateDownloading = "downloading"
	StateReady       = "ready"
	StateInstalling  = "installing"
	StateRolledBack  = "rolled_back"
	StateError       = "error"
)

// Timing of the automatic check: 2 min after start, then every 6 h ± 30 min, only when online.
var (
	FirstCheck    = 2 * time.Minute
	CheckInterval = 6 * time.Hour
	CheckJitter   = 30 * time.Minute
	RetryInterval = 30 * time.Minute // after a failed check or download
	OfflinePoll   = 5 * time.Minute
	tick          = 30 * time.Second
	helperPoll    = 2 * time.Second
	helperTimeout = 20 * time.Minute
)

// Errors of Install (the API maps them to 409).
var (
	ErrStreaming = errors.New("streaming")
	ErrNotReady  = errors.New("not_ready")
	ErrBusy      = errors.New("busy")
)

// Request is request.json: what the app asks the helper to install. Data only: the helper takes the
// manifest, signature and bundle from their fixed names and checks that the version matches.
type Request struct {
	Version     string    `json:"version"`
	RequestedAt time.Time `json:"requestedAt"`
}

// HelperStatus is the helper's status.json (/var/lib/eirl-update/status.json, world-readable).
type HelperStatus struct {
	State      string    `json:"state"` // installing | installed | rolled_back | failed | refused
	Phase      string    `json:"phase,omitempty"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Error      string    `json:"error,omitempty"`  // stable code
	Reason     string    `json:"reason,omitempty"` // English, for the operator
}

// Final reports whether the helper is done with this run.
func (h *HelperStatus) Final() bool { return h != nil && h.State != "" && h.State != "installing" }

// Helper status states.
const (
	HelperInstalling = "installing"
	HelperInstalled  = "installed"
	HelperRolledBack = "rolled_back"
	HelperFailed     = "failed"
	HelperRefused    = "refused"
)

// Config of the Updater. Every function is injectable for tests.
type Config struct {
	ManifestURL string
	Dir         string              // the app's update directory
	Keys        []ed25519.PublicKey // trusted release keys (TrustedKeys)
	Fetcher     *Fetcher
	// Current: the installed release (re-read: it changes after an install). "" = pre-OTA install.
	Current func() string
	// Online: whether there is a way to the internet (HasDefaultRoute); nil = always.
	Online func() bool
	// Streaming: whether the engine streams (Streaming(runDir)); nil = never.
	Streaming func() bool
	// Metered: whether the way to the internet is paid by the megabyte (a modem, a phone: MeteredDefaultRoute).
	// An automatic check then does not download the release: it waits for Wi-Fi or a cable, and a manual check
	// downloads it at once (UPDATES.md §12.7). nil = never.
	Metered func() bool
	// StartHelper starts the root helper (sudo -n /bin/systemctl --no-block start eirl-update.service).
	StartHelper func(ctx context.Context) error
	// HelperStatusPath: the helper's status.json.
	HelperStatusPath string
	// AutoCheck: periodic checks (and the download that follows); manual checks always work.
	AutoCheck bool
	Log       *slog.Logger
	Now       func() time.Time
}

// Status is the state for the API (it localizes notes and messages).
type Status struct {
	Current    string
	State      string
	Available  *Manifest // nil = nothing newer known
	Progress   float64   // 0-1 while downloading (1 when ready)
	LastCheck  time.Time // zero = never
	Error      string    // stable code of the last error, "" = none
	Detail     string    // English detail of that error (logs, operators)
	Ready      bool      // a verified bundle is waiting to be installed
	Streaming  bool
	LastResult *HelperStatus // the helper's last run, if any
	AutoCheck  bool
	// DownloadWaits: why a newer release is not being downloaded by itself ("metered": it waits for Wi-Fi or a
	// cable; a manual check downloads it). "" = it is not waiting
	DownloadWaits string
}

// CanInstallNow: a verified bundle is waiting, nothing streams and no install runs.
func (s Status) CanInstallNow() bool {
	return s.Ready && !s.Streaming && s.State != StateInstalling && s.Available != nil
}

// Updater is the app side of the update (UPDATES.md §6): it checks, downloads and verifies, and asks
// the root helper to install. It never installs anything itself.
type Updater struct {
	cfg Config

	mu         sync.Mutex
	state      string
	avail      *Manifest
	ready      bool
	progress   float64
	lastCheck  time.Time
	dismissed  time.Time // results older than this no longer set the state
	errCode    string
	errDetail  string
	lastResult *HelperStatus
	requested  time.Time // when the running install was requested
	nextCheck  time.Time
	nextDL     time.Time
	cancelDL   context.CancelFunc
	busy       bool             // a check or download is running
	pending    *fetchedManifest // the last verified manifest, with what the download needs
	// badSums: downloads of this version in a row whose bytes did not match the manifest. Each one doubles
	// the wait before the next (a mispublished asset is not fetched in full every 30 min)
	badSums    int
	badSumsVer string
	waits      string // Status.DownloadWaits

	trigger chan struct{}
}

// New creates an Updater; call Run to start it.
func New(cfg Config) *Updater {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Fetcher == nil {
		cfg.Fetcher = &Fetcher{}
	}
	if cfg.Current == nil {
		cfg.Current = func() string { return "" }
	}
	return &Updater{cfg: cfg, state: StateIdle, trigger: make(chan struct{}, 1)}
}

func (u *Updater) current() string {
	if c := u.cfg.Current(); c != "" {
		return c
	}
	return "0.0.0"
}

func (u *Updater) streaming() bool { return u.cfg.Streaming != nil && u.cfg.Streaming() }

func (u *Updater) metered() bool { return u.cfg.Metered != nil && u.cfg.Metered() }

// Status returns a snapshot.
func (u *Updater) Status() Status {
	streaming := u.streaming()
	u.mu.Lock()
	defer u.mu.Unlock()
	st := Status{Current: u.cfg.Current(), State: u.state, Available: u.avail, Progress: u.progress,
		LastCheck: u.lastCheck, Error: u.errCode, Detail: u.errDetail, Ready: u.ready, Streaming: streaming,
		AutoCheck: u.cfg.AutoCheck}
	if u.avail != nil && !u.ready && u.state == StateAvailable {
		st.DownloadWaits = u.waits
	}
	if u.lastResult != nil {
		r := *u.lastResult
		st.LastResult = &r
	}
	return st
}

// Check asks for a check now (and the download after it). It never blocks; a check already running absorbs it.
func (u *Updater) Check() error {
	u.mu.Lock()
	if u.state == StateInstalling {
		u.mu.Unlock()
		return ErrBusy
	}
	u.mu.Unlock()
	select {
	case u.trigger <- struct{}{}:
	default:
	}
	return nil
}

// Install asks the helper to install the ready bundle. ErrStreaming, ErrNotReady, ErrBusy, or the
// error of starting the helper.
func (u *Updater) Install(ctx context.Context) error {
	u.mu.Lock()
	if u.state == StateInstalling {
		u.mu.Unlock()
		return ErrBusy
	}
	if !u.ready || u.avail == nil || u.busy {
		u.mu.Unlock()
		return ErrNotReady
	}
	m := u.avail
	u.mu.Unlock()
	if u.streaming() {
		return ErrStreaming
	}
	now := u.cfg.Now()
	b, _ := json.Marshal(Request{Version: m.Version, RequestedAt: now.UTC()})
	if err := writeAtomic(filepath.Join(u.cfg.Dir, RequestFile), b, 0o640); err != nil {
		return err
	}
	u.mu.Lock()
	prev := u.state
	u.state, u.requested, u.errCode, u.errDetail = StateInstalling, now, "", ""
	u.dismissed = now
	u.saveLocked()
	u.mu.Unlock()
	u.cfg.Log.Info("update: install requested", "version", m.Version, "from", u.cfg.Current())
	if u.cfg.StartHelper == nil {
		u.setError(prev, "helper_failed", "no update helper")
		return errors.New("no update helper")
	}
	if err := u.cfg.StartHelper(ctx); err != nil {
		u.setError(prev, "helper_failed", err.Error())
		return err
	}
	go u.watchHelper(context.WithoutCancel(ctx))
	return nil
}

func (u *Updater) setError(state, code, detail string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if state == "" || state == StateInstalling {
		state = StateError
	}
	if u.ready && state != StateRolledBack {
		state = StateReady // a verified bundle is still there: installing again is possible
	}
	u.state, u.errCode, u.errDetail = state, code, detail
}

// Run loads the state left on disk and runs the periodic check until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	u.loadState()
	u.mu.Lock()
	u.nextCheck = u.cfg.Now().Add(FirstCheck)
	installing := u.state == StateInstalling
	u.mu.Unlock()
	if installing {
		go u.watchHelper(ctx)
	}
	u.loadDownload()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-u.trigger:
			u.checkAndDownload(ctx, true)
		case <-t.C:
			u.periodic(ctx)
		}
	}
}

func (u *Updater) periodic(ctx context.Context) {
	now := u.cfg.Now()
	u.mu.Lock()
	dueCheck := u.cfg.AutoCheck && !now.Before(u.nextCheck) && u.state != StateInstalling
	dueDL := u.cfg.AutoCheck && u.avail != nil && !u.ready && !now.Before(u.nextDL) &&
		(u.state == StateAvailable || u.state == StateError) && u.errCode != CodeMinFrom
	u.mu.Unlock()
	if !dueCheck && !dueDL {
		return
	}
	if u.cfg.Online != nil && !u.cfg.Online() {
		u.mu.Lock()
		u.nextCheck = now.Add(OfflinePoll)
		u.mu.Unlock()
		return
	}
	if dueCheck {
		u.checkAndDownload(ctx, false)
	} else if !u.waitForUnmetered(now) {
		u.download(ctx)
	}
}

// checksumBackoff: the wait after the n-th download in a row that did not match its manifest: 30 min, 1 h,
// 2 h… up to a day.
func checksumBackoff(n int) time.Duration {
	d := RetryInterval
	for i := 1; i < n && d < 24*time.Hour; i++ {
		d *= 2
	}
	return min(d, 24*time.Hour)
}

func jitter(d time.Duration) time.Duration {
	return time.Duration((rand.Float64()*2 - 1) * float64(d))
}

// checkAndDownload fetches and verifies the manifest and, if it is newer, downloads the bundle.
func (u *Updater) checkAndDownload(ctx context.Context, manual bool) {
	u.mu.Lock()
	if u.state == StateInstalling || u.busy {
		u.mu.Unlock()
		return
	}
	// A manual check dismisses the result of the last install (rolled_back stays visible until then). Here
	// and not in Check: a check asked for while the restarted app still loads its state is not lost
	if manual && u.lastResult != nil && u.lastResult.Final() {
		u.dismissed = u.cfg.Now()
		if u.state == StateRolledBack {
			u.state, u.errCode, u.errDetail = StateIdle, "", ""
			if u.ready {
				u.state = StateReady
			}
		}
	}
	u.busy = true
	prev := u.state
	if !u.showingResult() {
		u.state = StateChecking
	}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()

	m, err := u.fetch(ctx)
	now := u.cfg.Now()
	u.mu.Lock()
	u.lastCheck = now
	u.nextCheck = now.Add(CheckInterval + jitter(CheckJitter))
	if errors.Is(err, ErrNoRelease) {
		// Nothing published on the channel yet: up to date, at the normal pace
		u.cfg.Log.Info("update: no release published yet", "current", u.cfg.Current())
		u.avail, u.ready, u.progress = nil, false, 0
		u.errCode, u.errDetail = "", ""
		if !u.showingResult() {
			u.state = StateIdle
		}
		u.saveLocked()
		u.mu.Unlock()
		u.clean()
		return
	}
	if err != nil {
		u.nextCheck = now.Add(RetryInterval + jitter(RetryInterval/6))
		u.cfg.Log.Warn("update: check failed", "code", Code(err), "err", err)
		if u.showingResult() {
			u.state = prev
		} else if u.ready {
			u.state = StateReady
		} else {
			u.state = StateError
		}
		u.errCode, u.errDetail = Code(err), err.Error()
		u.saveLocked()
		u.mu.Unlock()
		return
	}
	u.errCode, u.errDetail = "", ""
	switch err := CheckUpgrade(m, u.current()); Code(err) {
	case CodeNotNewer:
		u.cfg.Log.Info("update: up to date", "current", u.cfg.Current(), "latest", m.Version)
		u.avail, u.ready, u.progress = nil, false, 0
		if !u.showingResult() {
			u.state = StateIdle
		}
		u.saveLocked()
		u.mu.Unlock()
		u.clean()
		return
	case CodeMinFrom:
		u.cfg.Log.Warn("update: cannot update directly", "err", err)
		u.avail, u.ready = m, false
		u.state, u.errCode, u.errDetail = StateError, CodeMinFrom, err.Error()
		u.saveLocked()
		u.mu.Unlock()
		return
	case "":
	default:
		u.state, u.errCode, u.errDetail = StateError, Code(err), err.Error()
		u.saveLocked()
		u.mu.Unlock()
		return
	}
	same := u.avail != nil && u.avail.Version == m.Version && u.avail.Bundle == m.Bundle
	if !same {
		u.ready, u.progress = false, 0
	}
	u.avail = m
	if u.ready {
		if !u.showingResult() {
			u.state = StateReady
		}
		u.saveLocked()
		u.mu.Unlock()
		return
	}
	// After downloads that did not match this manifest, an automatic check waits for the backoff (a manual
	// one retries now)
	if !manual && same && u.badSums > 0 && u.badSumsVer == m.Version && now.Before(u.nextDL) {
		if !u.showingResult() {
			u.state = StateError
		}
		u.errCode = CodeChecksum
		u.errDetail = fmt.Sprintf("the last %d download(s) did not match the manifest; next try at %s", u.badSums, u.nextDL.UTC().Format(time.RFC3339))
		u.saveLocked()
		u.mu.Unlock()
		return
	}
	if !u.showingResult() {
		u.state = StateAvailable
	}
	u.nextDL = time.Time{}
	u.saveLocked()
	u.mu.Unlock()
	u.cfg.Log.Info("update: new version available", "version", m.Version, "current", u.cfg.Current(), "manual", manual)
	if !same {
		u.clean()
	}
	if manual {
		u.mu.Lock()
		u.waits = ""
		u.mu.Unlock()
	} else if u.waitForUnmetered(now) {
		return
	}
	u.mu.Lock()
	u.busy = false // download takes the flag itself
	u.mu.Unlock()
	u.download(ctx)
}

// waitForUnmetered: an automatic download waits while the way to the internet is metered (a modem, a phone): it
// says so in the status and looks again later. True = it waits.
func (u *Updater) waitForUnmetered(now time.Time) bool {
	if !u.metered() {
		u.mu.Lock()
		u.waits = ""
		u.mu.Unlock()
		return false
	}
	u.mu.Lock()
	if u.waits == "" {
		u.cfg.Log.Info("update: the download waits for Wi-Fi or a cable (the link is metered)")
	}
	u.waits, u.nextDL = "metered", now.Add(OfflinePoll)
	u.mu.Unlock()
	return true
}

// showingResult: the state shows the result of the last install (rolled_back) until it is dismissed.
func (u *Updater) showingResult() bool { return u.state == StateRolledBack }

// fetchedManifest: the last verified manifest, its exact bytes and signature (stored next to the
// bundle for the helper) and the bundle's URL.
type fetchedManifest struct {
	m   *Manifest
	raw []byte
	sig []byte
	url string
}

func (u *Updater) fetch(ctx context.Context) (*Manifest, error) {
	fd, err := u.cfg.Fetcher.FetchManifest(ctx, u.cfg.ManifestURL)
	if err != nil {
		return nil, err
	}
	m, err := VerifyManifest(fd.Raw, fd.Sig, u.cfg.Keys)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.pending = &fetchedManifest{m: m, raw: fd.Raw, sig: fd.Sig, url: fd.BundleURL(m)}
	u.mu.Unlock()
	return m, nil
}

// download fetches the bundle of the available version (never while streaming: it would take
// bandwidth from the stream; a stream that starts cancels it), then keeps manifest, signature and
// bundle together in the update directory.
func (u *Updater) download(ctx context.Context) {
	u.mu.Lock()
	p := u.pending
	if u.busy || u.avail == nil || u.ready || p == nil || p.m.Version != u.avail.Version || u.state == StateInstalling {
		u.mu.Unlock()
		return
	}
	if u.streaming() {
		u.nextDL = u.cfg.Now().Add(OfflinePoll)
		u.mu.Unlock()
		u.cfg.Log.Info("update: download postponed while streaming", "version", p.m.Version)
		return
	}
	u.busy = true
	dctx, cancel := context.WithCancel(ctx)
	u.cancelDL = cancel
	if !u.showingResult() {
		u.state = StateDownloading
	}
	u.progress = 0
	u.mu.Unlock()
	defer func() {
		cancel()
		u.mu.Lock()
		u.busy, u.cancelDL = false, nil
		u.mu.Unlock()
	}()
	// A stream that starts during the download cancels it
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-dctx.Done():
				return
			case <-t.C:
				if u.streaming() {
					cancel()
					return
				}
			}
		}
	}()
	u.cfg.Log.Info("update: downloading", "version", p.m.Version, "bytes", p.m.Bundle.Size)
	size := p.m.Bundle.Size
	err := u.cfg.Fetcher.Download(dctx, p.url, filepath.Join(u.cfg.Dir, BundleFile), size, p.m.Bundle.SHA256, func(n int64) {
		u.mu.Lock()
		u.progress = float64(n) / float64(size)
		u.mu.Unlock()
	})
	if err == nil {
		err = writeAtomic(filepath.Join(u.cfg.Dir, ManifestFile), p.raw, 0o640)
	}
	if err == nil {
		err = writeAtomic(filepath.Join(u.cfg.Dir, SigFile), p.sig, 0o640)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case err == nil:
		u.badSums = 0
		u.ready, u.progress, u.errCode, u.errDetail = true, 1, "", ""
		if !u.showingResult() {
			u.state = StateReady
		}
		u.cfg.Log.Info("update: ready to install", "version", p.m.Version)
	case errors.Is(err, context.Canceled) && ctx.Err() == nil:
		u.progress = 0
		u.nextDL = u.cfg.Now().Add(OfflinePoll)
		if !u.showingResult() {
			u.state = StateAvailable
		}
		u.cfg.Log.Info("update: download cancelled (streaming started)", "version", p.m.Version)
	default:
		u.progress = 0
		u.nextDL = u.cfg.Now().Add(RetryInterval)
		if Code(err) == CodeChecksum {
			if u.badSumsVer != p.m.Version {
				u.badSums, u.badSumsVer = 0, p.m.Version
			}
			u.badSums++
			u.nextDL = u.cfg.Now().Add(checksumBackoff(u.badSums))
		}
		if !u.showingResult() {
			u.state = StateError
		}
		u.errCode, u.errDetail = Code(err), err.Error()
		if ctx.Err() == nil {
			u.cfg.Log.Warn("update: download failed", "version", p.m.Version, "code", Code(err), "err", err)
		}
	}
	u.saveLocked()
}

// clean removes what was downloaded (a version that is no longer offered, or already installed).
func (u *Updater) clean() {
	for _, f := range []string{BundleFile, BundleFile + ".part", ManifestFile, SigFile, RequestFile} {
		os.Remove(filepath.Join(u.cfg.Dir, f))
	}
}

// watchHelper follows the helper's status.json until its run ends. The app itself is usually restarted
// by the install in the middle: the new app picks the watch up in Run (load).
func (u *Updater) watchHelper(ctx context.Context) {
	deadline := u.cfg.Now().Add(helperTimeout)
	t := time.NewTicker(helperPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		hs := u.readHelper()
		u.mu.Lock()
		req := u.requested
		u.mu.Unlock()
		if hs != nil && hs.Final() && !hs.StartedAt.Before(req.Add(-5*time.Second)) {
			u.applyResult(hs)
			return
		}
		if u.cfg.Now().After(deadline) {
			u.setError(StateError, "helper_timeout", "the update helper did not finish")
			return
		}
	}
}

// applyResult turns the helper's final status into the app's state.
func (u *Updater) applyResult(hs *HelperStatus) {
	u.mu.Lock()
	u.lastResult = hs
	switch hs.State {
	case HelperInstalled:
		u.cfg.Log.Info("update: installed", "from", hs.From, "to", hs.To)
		u.state, u.errCode, u.errDetail = StateIdle, "", ""
		if u.avail != nil && u.avail.Version == hs.To {
			u.avail, u.ready, u.progress = nil, false, 0
			u.mu.Unlock()
			u.clean()
			u.mu.Lock()
		}
	case HelperRolledBack:
		u.cfg.Log.Warn("update: rolled back", "to", hs.To, "code", hs.Error, "reason", hs.Reason)
		u.state, u.errCode, u.errDetail = StateRolledBack, hs.Error, hs.Reason
	case HelperRefused:
		u.cfg.Log.Warn("update: refused by the helper", "code", hs.Error, "reason", hs.Reason)
		u.state, u.errCode, u.errDetail = StateError, hs.Error, hs.Reason
		if u.ready {
			u.state = StateReady
		}
	default: // failed: the rollback itself failed
		u.cfg.Log.Error("update: install failed", "code", hs.Error, "reason", hs.Reason)
		u.state, u.errCode, u.errDetail = StateError, "rollback_failed", hs.Reason
	}
	u.saveLocked()
	u.mu.Unlock()
}

func (u *Updater) readHelper() *HelperStatus {
	if u.cfg.HelperStatusPath == "" {
		return nil
	}
	b, err := os.ReadFile(u.cfg.HelperStatusPath)
	if err != nil || len(b) > 64<<10 {
		return nil
	}
	var hs HelperStatus
	if json.Unmarshal(b, &hs) != nil {
		return nil
	}
	return &hs
}

type savedState struct {
	LastCheck time.Time `json:"lastCheck,omitzero"`
	Dismissed time.Time `json:"dismissed,omitzero"`
	Requested time.Time `json:"requested,omitzero"`
}

func (u *Updater) saveLocked() {
	b, _ := json.Marshal(savedState{LastCheck: u.lastCheck, Dismissed: u.dismissed, Requested: u.requested})
	if err := writeAtomic(filepath.Join(u.cfg.Dir, stateFile), b, 0o640); err != nil {
		u.cfg.Log.Warn("update: cannot save state", "err", err)
	}
}

// load restores what survives a restart of the app: last check, the helper's last result (installing: the
// install restarted the app; rolled_back, also by the operator: shown until dismissed) and a verified
// download.
func (u *Updater) load(ctx context.Context) {
	u.loadState()
	u.loadDownload()
}

// loadState is the cheap part of load: the saved state and the helper's status. It runs first, so the API
// tells the result of the last install (or rollback) at once, while the stored bundle is still being hashed.
func (u *Updater) loadState() {
	if err := os.MkdirAll(u.cfg.Dir, 0o750); err != nil {
		u.cfg.Log.Warn("update: no update directory", "dir", u.cfg.Dir, "err", err)
	}
	var ss savedState
	if b, err := os.ReadFile(filepath.Join(u.cfg.Dir, stateFile)); err == nil {
		json.Unmarshal(b, &ss)
	}
	hs := u.readHelper()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.lastCheck, u.dismissed, u.requested = ss.LastCheck, ss.Dismissed, ss.Requested
	if hs == nil {
		return
	}
	u.lastResult = hs
	switch {
	case hs.State == HelperInstalling && u.cfg.Now().Sub(hs.StartedAt) < helperTimeout:
		u.state = StateInstalling
		if u.requested.IsZero() || u.requested.After(hs.StartedAt) {
			u.requested = hs.StartedAt
		}
	case hs.State == HelperRolledBack && hs.FinishedAt.After(u.dismissed):
		u.state, u.errCode, u.errDetail = StateRolledBack, hs.Error, hs.Reason
	case (hs.State == HelperFailed) && hs.FinishedAt.After(u.dismissed):
		u.state, u.errCode, u.errDetail = StateError, "rollback_failed", hs.Reason
	}
}

// loadDownload: a download that is complete is verified again (the signature with the compiled-in key, the
// bundle's sha256: a few seconds on the board) and is ready to install.
func (u *Updater) loadDownload() {
	m := u.loadDownloaded()
	if m == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.avail != nil && u.avail.Version != m.Version {
		return // something newer was found meanwhile
	}
	u.avail, u.ready, u.progress = m, true, 1
	if u.state == StateIdle {
		u.state = StateReady
	}
}

func (u *Updater) loadDownloaded() *Manifest {
	raw, err1 := os.ReadFile(filepath.Join(u.cfg.Dir, ManifestFile))
	sig, err2 := os.ReadFile(filepath.Join(u.cfg.Dir, SigFile))
	if err1 != nil || err2 != nil {
		return nil
	}
	m, err := VerifyManifest(raw, sig, u.cfg.Keys)
	if err != nil {
		u.cfg.Log.Warn("update: discarding a stored manifest", "err", err)
		u.clean()
		return nil
	}
	if CheckUpgrade(m, u.current()) != nil {
		u.clean() // installed meanwhile (or no longer installable)
		return nil
	}
	sum, err := FileSHA256(filepath.Join(u.cfg.Dir, BundleFile), m.Bundle.Size)
	if err != nil || sum != m.Bundle.SHA256 {
		os.Remove(filepath.Join(u.cfg.Dir, BundleFile))
		return nil
	}
	return m
}

// writeAtomic writes p via a temporary file in the same directory, fsync and rename.
func writeAtomic(p string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(p)
	f, err := os.CreateTemp(dir, "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
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
	syncDir(dir)
	return nil
}

// HasDefaultRoute reports whether the kernel has a default route (IPv4 or IPv6): "online" for the
// automatic check. Paths are /proc/net/route and /proc/net/ipv6_route on Linux.
func HasDefaultRoute(route4, route6 string) bool {
	if b, err := os.ReadFile(route4); err == nil {
		for _, line := range strings.Split(string(b), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) > 2 && f[1] == "00000000" && f[0] != "lo" {
				return true
			}
		}
	}
	if b, err := os.ReadFile(route6); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			// destination ::/0 with a non-loopback device
			if len(f) >= 10 && f[0] == strings.Repeat("0", 32) && f[1] == "00" && f[9] != "lo" {
				return true
			}
		}
	}
	return false
}
