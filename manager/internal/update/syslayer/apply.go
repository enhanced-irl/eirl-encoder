package syslayer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Exec runs a command of the system (apt-get, dpkg, depmod, systemctl…). Tests use a fake.
type Exec interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// OSExec runs commands for real, non-interactive, with a fixed PATH and a minimal environment.
type OSExec struct{}

func (OSExec) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C",
		"HOME=/root", "DEBIAN_FRONTEND=noninteractive", "DEBCONF_NONINTERACTIVE_SEEN=true"}
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, &out, &out
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, tail(out.String(), 1500))
	}
	return out.String(), err
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}

// Failure is an apply that did not happen (nothing changed) or that was undone, with a stable code:
// bad_layer, no_space, dpkg_busy, dpkg_broken, bad_package, bad_file, unmet_dependencies, backup_failed,
// apply_failed, verify_failed, rollback_failed, interrupted.
type Failure struct {
	Code   string
	Reason string
}

func (f *Failure) Error() string { return f.Code + ": " + f.Reason }

func fail(code, format string, a ...any) *Failure {
	return &Failure{Code: code, Reason: fmt.Sprintf(format, a...)}
}

// Applier applies a layer to the root filesystem under Root.
type Applier struct {
	Root     string // "/" (the board, or the image's chroot)
	StateDir string // DefaultStateDir, absolute on the board
	// Image: the root is an SD card image being built (a chroot, no systemd running): no reloads, no restart
	// to wait for, and nothing is "loaded"
	Image    bool
	Exec     Exec
	Proc     string        // "/proc": this boot's id and running kernel ("" with Image)
	Sys      string        // "/sys": the loaded modules ("" with Image)
	LockWait time.Duration // how long dpkg's lock may be held by someone else (60 s)
	// Budget: the longest an apply may take before it is undone (7 min: the helpers up to 0.9.0 give install.sh 10,
	// and a stopped install.sh must not leave a layer half applied behind it)
	Budget  time.Duration
	MinFree int64 // free space left after the apply, on top of twice what it needs (300 MB)
	Log     *slog.Logger
	Now     func() time.Time
}

// Result of an apply.
type Result struct {
	Plan    *Plan
	Changed bool
	Skipped string
	Pending []Pending
}

func (a *Applier) defaults() {
	if a.Root == "" {
		a.Root = "/"
	}
	if a.StateDir == "" {
		a.StateDir = DefaultStateDir
	}
	if a.Exec == nil {
		a.Exec = OSExec{}
	}
	if a.LockWait == 0 {
		a.LockWait = 60 * time.Second
	}
	if a.MinFree == 0 {
		a.MinFree = 300 << 20
	}
	if a.Budget == 0 {
		a.Budget = 7 * time.Minute
	}
	if a.Log == nil {
		a.Log = slog.New(slog.DiscardHandler)
	}
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.Image {
		a.Proc, a.Sys = "", ""
	}
}

func (a *Applier) p(abs string) string { return filepath.Join(a.Root, abs) }
func (a *Applier) sd() string          { return a.p(a.StateDir) }
func (a *Applier) markerPath() string  { return filepath.Join(a.sd(), markerFile) }
func (a *Applier) txn() string         { return filepath.Join(a.sd(), txnDir) }

// Interrupted reports whether an apply was cut (its marker is there).
func (a *Applier) Interrupted() bool {
	a.defaults()
	_, err := os.Stat(a.markerPath())
	return err == nil
}

func (a *Applier) lock() (func(), error) {
	if err := os.MkdirAll(a.sd(), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(a.sd(), lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another system layer run holds the lock")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// Plan only computes what an apply would change (eirl-update system plan).
func (a *Applier) Plan(layerDir string) (*Plan, string, error) {
	a.defaults()
	l, err := LoadLayer(layerDir)
	if err != nil {
		return nil, "", err
	}
	if why := a.baseMismatch(l); why != "" {
		return nil, why, nil
	}
	dpkg, err := readDpkg(a.Root)
	if err != nil {
		return nil, "", err
	}
	pl, err := makePlan(a.Root, l, ReadState(a.sd()), dpkg)
	return pl, "", err
}

// Apply applies the layer in layerDir (the bundle's system/): see the package documentation.
func (a *Applier) Apply(ctx context.Context, layerDir string) (*Result, error) {
	a.defaults()
	// apt takes a .deb only by a path it cannot mistake for a package name
	if abs, err := filepath.Abs(layerDir); err == nil {
		layerDir = abs
	}
	l, err := LoadLayer(layerDir)
	if err != nil {
		return nil, fail("bad_layer", "%v", err)
	}
	unlock, err := a.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err := os.Stat(a.markerPath()); err == nil {
		a.Log.Warn("system layer: an earlier apply was interrupted: undoing it first")
		if err := a.recoverLocked(ctx); err != nil {
			return nil, fail("rollback_failed", "undoing the interrupted apply: %v", err)
		}
	}
	st := ReadState(a.sd())
	if why := a.baseMismatch(l); why != "" {
		a.Log.Warn("system layer skipped: this is not the encoder's base system", "why", why)
		st.Skipped = why
		if err := st.write(a.sd()); err != nil {
			return nil, err
		}
		return &Result{Skipped: why}, nil
	}
	// Once it starts changing the board it goes to the end or undoes itself: a signal does not stop it halfway (the
	// apply has its own deadline, Budget)
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.Budget)
	defer cancel()
	// dpkg free and with nothing left half done before the plan: what an earlier cut left unpacked is configured
	// first, so it is not mistaken for something this apply adds
	if f := a.dpkgReady(actx, l); f != nil {
		a.recordFailure(st, l, f)
		return nil, f
	}
	dpkg, err := readDpkg(a.Root)
	if err != nil {
		return nil, fail("dpkg_broken", "dpkg's database: %v", err)
	}
	plan, err := makePlan(a.Root, l, st, dpkg)
	if err != nil {
		return nil, fail("bad_file", "%v", err)
	}
	res := &Result{Plan: plan}
	cur := bootID(a.Proc)
	if plan.Empty() {
		a.Log.Info("system layer: nothing to change", "release", l.Release)
		a.commitState(st, l, plan.Drift, st.PendingNow(cur))
		return res, st.write(a.sd())
	}
	a.Log.Info("system layer: plan", "release", l.Release, "plan", strings.TrimSpace(plan.String()))
	if f := a.preflight(actx, l, layerDir, plan); f != nil {
		a.recordFailure(st, l, f)
		return res, f
	}
	pending := a.effects(actx, l, layerDir, plan)
	mk, err := a.save(l, layerDir, plan)
	if err != nil {
		os.RemoveAll(a.txn())
		f := fail("backup_failed", "%v", err)
		a.recordFailure(st, l, f)
		return res, f
	}
	if err := a.do(actx, l, layerDir, plan, mk); err != nil {
		return res, a.undo(ctx, st, l, mk, fail("apply_failed", "%v", err))
	}
	// Verify: the board must now be exactly what the layer says
	dpkg2, err := readDpkg(a.Root)
	if err != nil {
		return res, a.undo(ctx, st, l, mk, fail("verify_failed", "dpkg's database: %v", err))
	}
	after, err := makePlan(a.Root, l, st, dpkg2)
	if err != nil {
		return res, a.undo(ctx, st, l, mk, fail("verify_failed", "%v", err))
	}
	if after.changes() {
		return res, a.undo(ctx, st, l, mk, fail("verify_failed", "still different after the apply: %s", strings.TrimSpace(after.String())))
	}
	// Commit
	pend := append(st.PendingNow(cur), pending...)
	a.commitState(st, l, after.Drift, dedupe(pend))
	if err := st.write(a.sd()); err != nil {
		return res, a.undo(ctx, st, l, mk, fail("apply_failed", "state.json: %v", err))
	}
	os.Remove(a.markerPath())
	syncDir(a.sd())
	os.RemoveAll(a.txn())
	res.Changed, res.Pending = true, st.Pending
	a.Log.Info("system layer applied", "release", l.Release, "pending", len(st.Pending))
	// Retiring packages comes after the commit: if it fails they stay listed and the next apply tries again
	if len(after.Retire) > 0 {
		if err := a.purge(actx, after.Retire); err != nil {
			a.Log.Warn("system layer: could not retire packages", "packages", after.Retire, "err", err)
			for _, n := range after.Retire {
				if v, ok := dpkg2[n]; ok {
					st.Packages[n] = v.Version
				}
			}
		}
		if dp, err := readDpkg(a.Root); err == nil {
			if pl, err := makePlan(a.Root, l, st, dp); err == nil {
				st.Drift = driftPtr(pl.Drift)
			}
		}
		st.write(a.sd())
	}
	return res, nil
}

// commitState records what the layer l now owns.
func (a *Applier) commitState(st *State, l *Layer, d Drift, pending []Pending) {
	st.Release, st.AppliedAt, st.Image = l.Release, nowUTC(a.Now), a.Image
	st.BootID = bootID(a.Proc)
	st.Files, st.Packages, st.Units = map[string]string{}, map[string]string{}, map[string]string{}
	for _, f := range l.Files {
		st.Files[f.Path] = f.SHA256
	}
	for _, x := range l.Packages {
		st.Packages[x.Name] = x.Version
	}
	for _, u := range l.Units {
		st.Units[u.Unit] = u.State
	}
	st.Pending = pending
	st.Drift = driftPtr(d)
	st.Skipped, st.Error, st.Failed, st.FailedAt = "", "", "", time.Time{}
}

func driftPtr(d Drift) *Drift {
	if d.empty() {
		return nil
	}
	return &d
}

func (a *Applier) recordFailure(st *State, l *Layer, f *Failure) {
	st.Error, st.Failed, st.FailedAt = f.Error(), l.Release, nowUTC(a.Now)
	if err := st.write(a.sd()); err != nil {
		a.Log.Error("system layer: cannot write state.json", "err", err)
	}
	a.Log.Error("system layer not applied", "code", f.Code, "reason", f.Reason)
}

// undo undoes the apply: never with a cancelled context (whatever stopped the apply, the undo runs to the end).
func (a *Applier) undo(ctx context.Context, st *State, l *Layer, mk *marker, f *Failure) error {
	a.Log.Error("system layer: undoing the apply", "code", f.Code, "reason", f.Reason)
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	pkgErr, err := a.rollback(uctx, mk)
	switch {
	case err != nil:
		f = fail("rollback_failed", "%s; undoing it failed too: %v", f.Error(), err)
	case pkgErr != nil:
		f = fail(f.Code, "%s; some packages stay: %v", f.Reason, pkgErr)
	}
	st = ReadState(a.sd()) // the state as it was before the apply (rollback put it back)
	a.recordFailure(st, l, f)
	return f
}

// --- before changing anything --------------------------------------------------------------------------

// baseMismatch: why the layer does not apply to this root ("" = it does): another L4T, no modules directory of its
// kernel or another architecture means the root filesystem does not come from the encoder's image (UPDATES.md
// §12.6). The kernel that runs is not looked at: the layer only writes files for its kernel.
func (a *Applier) baseMismatch(l *Layer) string {
	r := l.Requires
	if v := l4tRelease(a.Root); v != r.L4T {
		return fmt.Sprintf("L4T %q, the layer is for %s", v, r.L4T)
	}
	if fi, err := os.Stat(a.p("/lib/modules/" + r.Kernel)); err != nil || !fi.IsDir() {
		return "no /lib/modules/" + r.Kernel
	}
	dpkg, err := readDpkg(a.Root)
	if err != nil {
		return "no dpkg database"
	}
	if d, ok := dpkg["dpkg"]; !ok || d.Arch != r.Arch {
		return fmt.Sprintf("architecture %q, the layer is for %s", d.Arch, r.Arch)
	}
	return ""
}

func (a *Applier) preflight(ctx context.Context, l *Layer, dir string, plan *Plan) *Failure {
	var need int64
	// The layer's own files, checked again (the bundle's signature covered them) and, for a module, built
	// for this kernel
	for _, op := range plan.Files {
		dst := a.p(op.File.Path)
		if fi, err := os.Lstat(dst); err == nil {
			if !fi.Mode().IsRegular() {
				return fail("bad_file", "%s is not a regular file", op.File.Path)
			}
			need += fi.Size() // its copy
		}
		if op.Op == "remove" {
			continue
		}
		src := filepath.Join(dir, op.File.Src)
		fi, err := os.Lstat(src)
		if err != nil || !fi.Mode().IsRegular() {
			return fail("bad_file", "the layer has no %s", op.File.Src)
		}
		need += fi.Size()
		if sum, err := sha256File(src); err != nil || sum != op.File.SHA256 {
			return fail("bad_file", "%s does not match layer.json", op.File.Src)
		}
		if op.File.Module != "" {
			if vm, err := moduleVermagic(src); err != nil || vm != l.Requires.Kernel {
				return fail("bad_file", "%s is built for %q (%v), not %s", op.File.Src, vm, err, l.Requires.Kernel)
			}
		}
	}
	// The packages: what layer.json says, byte for byte and in their control
	var debs []string
	for _, x := range plan.Install {
		deb := filepath.Join(dir, x.File)
		if fi, err := os.Lstat(deb); err != nil || !fi.Mode().IsRegular() || fi.Size() != x.Size {
			return fail("bad_package", "%s is missing or not %d bytes", x.File, x.Size)
		}
		if sum, err := sha256File(deb); err != nil || sum != x.SHA256 {
			return fail("bad_package", "%s does not match layer.json", x.File)
		}
		out, err := a.Exec.Run(ctx, "dpkg-deb", "-f", deb, "Package", "Version", "Architecture")
		if err != nil {
			return fail("bad_package", "%v", err)
		}
		c := parseControl(out)
		if c["Package"] != x.Name || c["Version"] != x.Version || c["Architecture"] != x.Arch {
			return fail("bad_package", "%s is %s %s %s, layer.json says %s %s %s", x.File, c["Package"], c["Version"],
				c["Architecture"], x.Name, x.Version, x.Arch)
		}
		need += x.InstalledSize*1024 + x.Size
		debs = append(debs, deb)
	}
	// Space: twice what it needs and a margin (UPDATES.md §12.7)
	var sfs unix.Statfs_t
	if err := unix.Statfs(a.sd(), &sfs); err == nil {
		free := int64(sfs.Bavail) * int64(sfs.Bsize)
		if free < 2*need+a.MinFree {
			return fail("no_space", "%d MB free, the system layer needs %d MB", free>>20, (2*need+a.MinFree)>>20)
		}
	}
	if len(debs) > 0 {
		out, err := a.apt(ctx, true, debs)
		if err != nil {
			return fail("unmet_dependencies", "%v", err)
		}
		a.Log.Info("system layer: apt simulation", "out", tail(out, 800))
	}
	return nil
}

// dpkgReady: dpkg's lock is free (it waits up to LockWait) and nothing is left half done (an install cut by a power
// loss, or by hand): if dpkg --audit lists something, dpkg --configure -a first. Only when the layer has packages.
func (a *Applier) dpkgReady(ctx context.Context, l *Layer) *Failure {
	if len(l.Packages)+len(l.Remove) == 0 {
		return nil
	}
	if err := a.waitDpkgLock(ctx); err != nil {
		return fail("dpkg_busy", "%v", err)
	}
	if out, _ := a.Exec.Run(ctx, "dpkg", "--audit"); strings.TrimSpace(out) != "" {
		a.Log.Warn("system layer: dpkg has unfinished work: dpkg --configure -a", "audit", tail(out, 500))
		if err := a.withPolicy(func() { a.Exec.Run(ctx, "dpkg", "--configure", "-a") }); err != nil {
			return fail("dpkg_broken", "%v", err)
		}
		if out, _ := a.Exec.Run(ctx, "dpkg", "--audit"); strings.TrimSpace(out) != "" {
			return fail("dpkg_broken", "dpkg --audit: %s", tail(out, 500))
		}
	}
	return nil
}

func parseControl(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// waitDpkgLock waits (up to LockWait) until nobody holds dpkg's locks. apt and dpkg then take them
// themselves; the moment in between is too short to matter on an appliance with no apt timers.
func (a *Applier) waitDpkgLock(ctx context.Context) error {
	deadline := a.Now().Add(a.LockWait)
	for {
		busy := ""
		for _, name := range []string{"lock-frontend", "lock"} {
			p := a.p("/var/lib/dpkg/" + name)
			f, err := os.OpenFile(p, os.O_RDWR, 0)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0}
			if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk); err != nil {
				busy = name
			} else {
				lk.Type = unix.F_UNLCK
				unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk)
			}
			f.Close()
		}
		if busy == "" {
			return nil
		}
		if !a.Now().Before(deadline) {
			return fmt.Errorf("dpkg's %s is held by another program", busy)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// apt installs (or with simulate only resolves) local .deb files: no source at all (so nothing can be downloaded:
// a dependency the board and the layer lack is an error), its own empty lists and cache (the board's apt state is not
// used or changed), never removing anything, keeping configuration files that were changed (UPDATES.md §12.3).
// Not --no-download: apt 1.6 then loses the path of a local .deb ("Pathname to install is not absolute").
func (a *Applier) apt(ctx context.Context, simulate bool, debs []string) (string, error) {
	work := filepath.Join(a.sd(), "apt")
	os.RemoveAll(work)
	defer os.RemoveAll(work)
	for _, d := range []string{"lists/partial", "cache/archives/partial", "sources.d"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o700); err != nil {
			return "", err
		}
	}
	args := []string{"-q", "-y", "--no-remove",
		"-o", "Dir::Etc::SourceList=/dev/null", "-o", "Dir::Etc::SourceParts=" + filepath.Join(work, "sources.d"),
		"-o", "Dir::State::Lists=" + filepath.Join(work, "lists"), "-o", "Dir::Cache=" + filepath.Join(work, "cache"),
		"-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=",
		"-o", "APT::Install-Recommends=false", "-o", "APT::Install-Suggests=false",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "-o", "Dpkg::Use-Pty=0"}
	if simulate {
		args = append(args, "-s")
	}
	args = append(append(args, "install"), debs...)
	var out string
	var err error
	if simulate {
		out, err = a.Exec.Run(ctx, "apt-get", args...)
	} else if perr := a.withPolicy(func() { out, err = a.Exec.Run(ctx, "apt-get", args...) }); perr != nil {
		return "", perr
	}
	return out, err
}

// policy-rc.d: while the layer installs packages no maintainer script starts a service (the layer sets the
// units' state itself, and nothing is restarted under a running encoder).
const policyRcD = "#!/bin/sh\n# eirl-update system: no service starts while the system layer installs packages (UPDATES.md §12.3)\nexit 101\n"

// withPolicy runs fn with a policy-rc.d that refuses every service start: one already there (an image being built
// has its own) is kept; without one, nothing runs (a maintainer script could restart the encoder's network).
func (a *Applier) withPolicy(fn func()) error {
	p := a.p("/usr/sbin/policy-rc.d")
	ours := false
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := writeFileAtomic(p, []byte(policyRcD), 0o755); err != nil {
			return fmt.Errorf("policy-rc.d: %w", err)
		}
		ours = true
	case err != nil:
		return fmt.Errorf("policy-rc.d: %w", err)
	case bytes.Contains(b, []byte("eirl-update system")):
		ours = true // a leftover of ours (a power cut): it goes too
	}
	fn()
	if ours {
		os.Remove(p)
	}
	return nil
}

// --- the transaction ---------------------------------------------------------------------------------------

// save copies everything the apply changes into txn/ and writes the marker last.
func (a *Applier) save(l *Layer, dir string, plan *Plan) (*marker, error) {
	txn := a.txn()
	if err := os.RemoveAll(txn); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(txn, "files"), 0o700); err != nil {
		return nil, err
	}
	os.Chmod(txn, 0o700)
	mk := &marker{V: Format, Release: l.Release, StartedAt: nowUTC(a.Now), Image: a.Image, Depmod: plan.Depmod,
		Kernel: l.Requires.Kernel, Units: plan.Units, New: plan.New}
	for i, op := range plan.Files {
		sf := savedFile{Path: op.File.Path, Module: strings.HasSuffix(op.File.Path, ".ko")}
		dst := a.p(op.File.Path)
		if fi, err := os.Lstat(dst); err == nil {
			sf.Copy, sf.Mode = filepath.Join("files", strconv.Itoa(i)), uint32(fi.Mode().Perm())
			if err := copyFileAtomic(dst, filepath.Join(txn, sf.Copy), 0o600); err != nil {
				return nil, err
			}
		}
		mk.Files = append(mk.Files, sf)
	}
	if len(plan.Upgrade) > 0 {
		if err := os.MkdirAll(filepath.Join(txn, "debs"), 0o700); err != nil {
			return nil, err
		}
	}
	for _, name := range plan.Upgrade {
		for _, x := range plan.Install {
			if x.Name != name {
				continue
			}
			c := filepath.Join("debs", filepath.Base(x.File))
			if err := copyFileAtomic(filepath.Join(dir, x.File), filepath.Join(txn, c), 0o600); err != nil {
				return nil, err
			}
			mk.Upgrade = append(mk.Upgrade, upgrade{Name: x.Name, Version: x.Version, Deb: c})
		}
	}
	// state.json as it is: a cut after the commit wrote the new one but before the marker went must not leave the
	// new one describing a board that recovery put back
	if _, err := os.Stat(filepath.Join(a.sd(), StateFile)); err == nil {
		if err := copyFileAtomic(filepath.Join(a.sd(), StateFile), filepath.Join(txn, StateFile), 0o600); err != nil {
			return nil, err
		}
		mk.State = true
	}
	_, err := os.Stat(a.p("/usr/sbin/policy-rc.d"))
	mk.PolicyRcD = len(plan.Install) > 0 && errors.Is(err, fs.ErrNotExist)
	b, _ := json.MarshalIndent(mk, "", "  ")
	syncDir(filepath.Join(txn, "files"))
	if err := writeFileAtomic(a.markerPath(), b, 0o600); err != nil {
		return nil, err
	}
	return mk, nil
}

// do changes the board: files, depmod, packages, units, groups, reloads.
func (a *Applier) do(ctx context.Context, l *Layer, dir string, plan *Plan, mk *marker) error {
	// Packages first: a package that also ships one of the layer's paths must not overwrite the layer's file
	if len(plan.Install) > 0 {
		debs := make([]string, 0, len(plan.Install))
		for _, x := range plan.Install {
			debs = append(debs, filepath.Join(dir, x.File))
		}
		out, err := a.apt(ctx, false, debs)
		a.Log.Info("system layer: packages", "out", tail(out, 1500))
		if err != nil {
			return err
		}
	}
	for _, op := range plan.Files {
		dst := a.p(op.File.Path)
		if op.Op == "remove" {
			if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			a.Log.Info("system layer: removed", "path", op.File.Path)
			continue
		}
		if err := copyFileAtomic(filepath.Join(dir, op.File.Src), dst, parseMode(op.File.Mode)); err != nil {
			return err
		}
		os.Lchown(dst, 0, 0)
		a.Log.Info("system layer: "+op.Op, "path", op.File.Path)
	}
	if plan.Depmod {
		if _, err := a.depmod(ctx, l.Requires.Kernel); err != nil {
			return err
		}
	}
	for _, u := range plan.Units {
		if err := a.setUnit(ctx, u.Unit, u.From, u.To); err != nil {
			return err
		}
	}
	for _, g := range plan.Groups {
		if _, err := a.Exec.Run(ctx, "groupadd", "--system", g); err != nil {
			return err
		}
	}
	a.reload(ctx)
	return nil
}

func (a *Applier) depmod(ctx context.Context, kernel string) (string, error) {
	if a.Root != "/" {
		return a.Exec.Run(ctx, "depmod", "-b", a.Root, "-a", kernel)
	}
	return a.Exec.Run(ctx, "depmod", "-a", kernel)
}

func (a *Applier) systemctl(ctx context.Context, args ...string) (string, error) {
	if a.Root != "/" {
		args = append([]string{"--root=" + a.Root}, args...)
	}
	return a.Exec.Run(ctx, "systemctl", args...)
}

func (a *Applier) setUnit(ctx context.Context, unit, from, to string) error {
	var err error
	switch to {
	case Masked:
		a.systemctl(ctx, "disable", unit) // its links go first; a unit that is not installed has none
		_, err = a.systemctl(ctx, "mask", unit)
	case Disabled:
		if from == Masked {
			if _, err = a.systemctl(ctx, "unmask", unit); err != nil {
				return err
			}
		}
		_, err = a.systemctl(ctx, "disable", unit)
	case Enabled:
		if from == Masked {
			if _, err = a.systemctl(ctx, "unmask", unit); err != nil {
				return err
			}
		}
		_, err = a.systemctl(ctx, "enable", unit)
	}
	if err == nil {
		a.Log.Info("system layer: unit", "unit", unit, "from", from, "to", to)
	}
	return err
}

// reload: systemd and udev read their files again (a running board only; nothing is restarted).
func (a *Applier) reload(ctx context.Context) {
	if a.Image {
		return
	}
	if _, err := a.systemctl(ctx, "daemon-reload"); err != nil {
		a.Log.Warn("system layer: daemon-reload", "err", err)
	}
	if _, err := a.Exec.Run(ctx, "udevadm", "control", "--reload"); err != nil {
		a.Log.Warn("system layer: udevadm control --reload", "err", err)
	}
}

func (a *Applier) purge(ctx context.Context, names []string) error {
	if err := a.waitDpkgLock(ctx); err != nil {
		return err
	}
	var err error
	if perr := a.withPolicy(func() { _, err = a.Exec.Run(ctx, "dpkg", append([]string{"--purge"}, names...)...) }); perr != nil {
		return perr
	}
	if err == nil {
		a.Log.Info("system layer: retired", "packages", names)
	}
	return err
}

// rollback puts back what the marker says: packages first (the new ones purged, the upgraded ones completed:
// there is no older .deb to go back to), then files, depmod, units and state.json. Idempotent: a recovery cut by
// another power loss runs it again. What it cannot do with the packages (a new one an upgraded one now depends on,
// a maintainer script that fails) is returned as pkgErr: the packages stay, and the marker goes anyway, since doing it
// again would fail the same way at every boot and block every later apply. A file or a unit it cannot put back is
// err: the marker stays and the next recovery tries again.
func (a *Applier) rollback(ctx context.Context, mk *marker) (pkgErr, err error) {
	var pkgErrs, errs []error
	txn := a.txn()
	if len(mk.New)+len(mk.Upgrade) > 0 {
		if err := a.waitDpkgLock(ctx); err != nil {
			pkgErrs = append(pkgErrs, err)
		}
		dpkg, _ := readDpkg(a.Root)
		perr := a.withPolicy(func() {
			for _, up := range mk.Upgrade {
				if have, ok := dpkg[up.Name]; ok && have.installed() {
					continue
				}
				if _, err := a.Exec.Run(ctx, "dpkg", "--force-confdef", "--force-confold", "-i", filepath.Join(txn, up.Deb)); err != nil {
					pkgErrs = append(pkgErrs, err)
				}
			}
			var purge []string
			for _, n := range mk.New {
				if have, ok := dpkg[n]; ok && have.Status != "not-installed" {
					purge = append(purge, n)
				}
			}
			if len(purge) > 0 {
				// All at once (dpkg orders them); if something still depends on one, each on its own
				if _, err := a.Exec.Run(ctx, "dpkg", append([]string{"--purge"}, purge...)...); err != nil {
					for _, n := range purge {
						if _, err := a.Exec.Run(ctx, "dpkg", "--purge", n); err != nil {
							pkgErrs = append(pkgErrs, fmt.Errorf("%s stays: %w", n, err))
						}
					}
				}
			}
			if _, err := a.Exec.Run(ctx, "dpkg", "--configure", "-a"); err != nil {
				pkgErrs = append(pkgErrs, err)
			}
		})
		if perr != nil {
			pkgErrs = append(pkgErrs, perr)
		}
	}
	for i := len(mk.Files) - 1; i >= 0; i-- {
		f := mk.Files[i]
		dst := a.p(f.Path)
		if f.Copy == "" {
			if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := copyFileAtomic(filepath.Join(txn, f.Copy), dst, os.FileMode(f.Mode)); err != nil {
			errs = append(errs, err)
		}
	}
	if mk.Depmod {
		if _, err := a.depmod(ctx, mk.Kernel); err != nil {
			errs = append(errs, err)
		}
	}
	for _, u := range mk.Units {
		if cur := unitState(a.Root, u.Unit); cur != u.From {
			if err := a.setUnit(ctx, u.Unit, cur, u.From); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if mk.State {
		if err := copyFileAtomic(filepath.Join(txn, StateFile), filepath.Join(a.sd(), StateFile), 0o644); err != nil {
			errs = append(errs, err)
		}
	} else if err := os.Remove(filepath.Join(a.sd(), StateFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if b, err := os.ReadFile(a.p("/usr/sbin/policy-rc.d")); err == nil && bytes.Contains(b, []byte("eirl-update system")) {
		os.Remove(a.p("/usr/sbin/policy-rc.d"))
	}
	if !mk.Image {
		a.reload(ctx)
	}
	pkgErr = errors.Join(pkgErrs...)
	if pkgErr != nil {
		a.Log.Error("system layer: some packages could not be put back (they stay)", "err", pkgErr)
	}
	if err := errors.Join(errs...); err != nil {
		return pkgErr, err // the marker stays: the next recovery tries again
	}
	os.Remove(a.markerPath())
	syncDir(a.sd())
	os.RemoveAll(txn)
	a.Log.Info("system layer: apply undone", "release", mk.Release)
	return pkgErr, nil
}

// Recover undoes an apply that was cut (eirl-update recover at boot, or before the next apply). It reports
// whether there was one.
func (a *Applier) Recover(ctx context.Context) (bool, error) {
	a.defaults()
	if _, err := os.Stat(a.markerPath()); err != nil {
		return false, nil
	}
	unlock, err := a.lock()
	if err != nil {
		return true, err
	}
	defer unlock()
	return true, a.recoverLocked(ctx)
}

func (a *Applier) recoverLocked(ctx context.Context) error {
	b, err := os.ReadFile(a.markerPath())
	if err != nil {
		return err
	}
	var mk marker
	if err := json.Unmarshal(b, &mk); err != nil {
		return fmt.Errorf("in-progress.json: %w", err)
	}
	a.Log.Warn("system layer: an apply was interrupted: undoing it", "release", mk.Release, "started", mk.StartedAt)
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	pkgErr, err := a.rollback(uctx, &mk)
	st := ReadState(a.sd())
	f := fail("interrupted", "the system layer of %s was interrupted and has been undone", mk.Release)
	switch {
	case err != nil:
		f = fail("rollback_failed", "undoing the interrupted system layer of %s: %v", mk.Release, err)
	case pkgErr != nil:
		f = fail("interrupted", "the system layer of %s was interrupted and has been undone; some packages stay: %v", mk.Release, pkgErr)
	}
	st.Error, st.Failed, st.FailedAt = f.Error(), mk.Release, nowUTC(a.Now)
	if werr := st.write(a.sd()); werr != nil {
		a.Log.Error("system layer: cannot write state.json", "err", werr)
	}
	return err
}

// --- what waits for a restart ------------------------------------------------------------------------------

var reUnitName = regexp.MustCompile(`\.(service|socket|timer|target|path|mount)$`)

// effects: the changes that only act after a restart, because what uses them is loaded or running now
// (UPDATES.md §12.5). Nothing is reloaded or restarted: the user restarts from the screen or the web.
func (a *Applier) effects(ctx context.Context, l *Layer, dir string, plan *Plan) []Pending {
	if a.Image {
		return nil
	}
	var out []Pending
	for _, op := range plan.Files {
		f := op.File
		area := f.Area
		if area == "" {
			area = "system"
		}
		switch {
		case f.Reboot:
			out = append(out, Pending{Area: area, What: firstNonEmpty(f.Module, filepath.Base(f.Path)), Effect: "reboot"})
		case f.Module != "" && a.loaded(f.Module):
			out = append(out, Pending{Area: area, What: f.Module, Effect: "reboot"})
		case f.UsedBy != "" && reUnitName.MatchString(f.UsedBy) && a.active(ctx, f.UsedBy):
			if op.Op == "replace" && a.onlyComments(f, dir) {
				continue // the running unit reads the same settings: nothing to wait for
			}
			out = append(out, Pending{Area: area, What: f.UsedBy, Effect: "reboot"})
		case f.UsedBy != "" && !reUnitName.MatchString(f.UsedBy) && a.loaded(f.UsedBy):
			out = append(out, Pending{Area: area, What: f.UsedBy, Effect: "reboot"})
		}
	}
	for _, name := range plan.Upgrade {
		out = append(out, Pending{Area: "system", What: name, Effect: "reboot"})
	}
	return dedupe(out)
}

// onlyComments: the new file f differs from the one on the board only in comments and blank lines (a translated
// comment must not ask every board to restart).
func (a *Applier) onlyComments(f File, dir string) bool {
	if dir == "" {
		return false
	}
	old, err1 := os.ReadFile(a.p(f.Path))
	cur, err2 := os.ReadFile(filepath.Join(dir, f.Src))
	return err1 == nil && err2 == nil && settings(old) == settings(cur)
}

// settings: a configuration file without its comments (# or ;) and blank lines.
func settings(b []byte) string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}

func (a *Applier) loaded(module string) bool {
	if a.Sys == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(a.Sys, "module", module))
	return err == nil
}

func (a *Applier) active(ctx context.Context, unit string) bool {
	out, _ := a.systemctl(ctx, "is-active", unit)
	return strings.TrimSpace(out) == "active"
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func dedupe(p []Pending) []Pending {
	seen := map[Pending]bool{}
	var out []Pending
	for _, x := range p {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
