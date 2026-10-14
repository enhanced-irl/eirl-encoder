package tcaudio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// fakeChip is the TC358743 behind i2ctransfer: successive AU_STATUS0 answers ("!" = the read fails; the
// last one repeats), the registers the recovery saves, and what is written.
type fakeChip struct {
	mu     sync.Mutex
	au     []string
	auto2  string
	nco    string
	writes []string // "0x86 0x04 0x00"
	bad    []string // calls that were not i2ctransfer on bus 8 at 0x0f
}

func (c *fakeChip) run(_ time.Duration, name string, args ...string) sysx.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name != "i2ctransfer" || len(args) < 6 || strings.Join(args[:3], " ") != "-f -y 8" {
		c.bad = append(c.bad, name+" "+strings.Join(args, " "))
		return sysx.Result{RC: 1}
	}
	switch {
	case args[3] == "w2@0x0f" && len(args) == 7 && args[6] == "r1":
		v := ""
		switch args[4] + " " + args[5] {
		case "0x85 0x23":
			v = c.au[0]
			if len(c.au) > 1 {
				c.au = c.au[1:]
			}
		case "0x86 0x04":
			v = c.auto2
		case "0x86 0x70":
			v = c.nco
		}
		if v == "!" {
			return sysx.Result{RC: 1, Err: "Error: Sending messages failed: Remote I/O error"}
		}
		return sysx.Result{Out: v + "\n"}
	case args[3] == "w3@0x0f" && len(args) == 7:
		c.writes = append(c.writes, strings.Join(args[4:], " "))
		return sysx.Result{}
	}
	c.bad = append(c.bad, strings.Join(args, " "))
	return sysx.Result{RC: 1}
}

type rig struct {
	w     *Watcher
	chip  *fakeChip
	root  string
	state string
	slept time.Duration
	now   time.Time
	logs  *strings.Builder
}

func newRig(t *testing.T, cfg Config, au ...string) *rig {
	t.Helper()
	r := &rig{chip: &fakeChip{au: au, auto2: "0x09", nco: "0x02"}, root: t.TempDir(), now: time.Unix(1_760_000_000, 0), logs: &strings.Builder{}}
	r.state = filepath.Join(r.root, "var/lib/eirl-engine")
	if err := os.MkdirAll(filepath.Join(r.root, "proc/asound/card1/pcm1c/sub0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("card1", filepath.Join(r.root, "proc/asound/tegrasndt210ref")); err != nil {
		t.Fatal(err)
	}
	cfg.I2CBus, cfg.StateDir, cfg.ALSACard, cfg.ADMAIF = 8, r.state, "tegrasndt210ref", 2
	if cfg.ResetInterval == 0 {
		cfg.ResetInterval = 300
	}
	if err := os.MkdirAll(r.state, 0o755); err != nil {
		t.Fatal(err)
	}
	r.w = New(cfg, Sys{Root: r.root, Run: r.chip.run, Sleep: func(d time.Duration) { r.slept += d }, Now: func() time.Time { return r.now }})
	elog.SetOutput(r.logs)
	t.Cleanup(func() { elog.SetOutput(nil) })
	return r
}

// ptr writes the PCM status: a running capture with that hw_ptr, or closed ("").
func (r *rig) ptr(t *testing.T, hw string) {
	t.Helper()
	s := "closed\n"
	if hw != "" {
		s = "state: RUNNING\nowner_pid   : 1234\ntrigger_time: 1.0\ntstamp      : 0.0\ndelay       : 512\navail       : 0\navail_max   : 1024\n-----\nhw_ptr      : " + hw + "\nappl_ptr    : " + hw + "\n"
	}
	if err := os.WriteFile(filepath.Join(r.root, "proc/asound/card1/pcm1c/sub0/status"), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ticks runs n ticks with these hw_ptr values (the last one repeats) and returns the first error and
// at which tick (1-based; 0 = none).
func (r *rig) ticks(t *testing.T, n int, ptrs ...string) (error, int) {
	t.Helper()
	for i := range n {
		r.ptr(t, ptrs[min(i, len(ptrs)-1)])
		if err := r.w.tick(context.Background()); err != nil {
			return err, i + 1
		}
		r.now = r.now.Add(Every)
	}
	return nil, 0
}

func (r *rig) lines() []string {
	s := strings.TrimSuffix(r.logs.String(), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

var (
	bufinit = []string{"0x86 0x04 0x00", "0x86 0x01 0x01", "0x86 0x01 0x05", "0x86 0x01 0x00", "0x86 0x04 0x09"}
	lockdet = []string{"0x86 0x04 0x00", "0x86 0x01 0x01", "0x86 0x01 0x03", "0x86 0x01 0x05", "0x86 0x01 0x00", "0x86 0x04 0x09"}
	nco     = []string{"0x86 0x70 0x02", "0x86 0x70 0x02"}
)

func cat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// requested says whether hw-reset.request exists.
func (r *rig) requested() bool { return sysx.Exists(filepath.Join(r.state, "hw-reset.request")) }

func TestWatcher(t *testing.T) {
	unlocked5 := []string{"0x41", "0x41", "0x41", "0x41", "0x41"} // 10 s without lock: 5 ticks
	for _, c := range []struct {
		name    string
		cfg     Config
		auto2   string // AUTO_CMD2 and NCO as the chip answers ("" = 0x09 and 0x02; "!" = unreadable)
		nco     string
		last    string // hw-reset.last ("" = missing)
		au      []string
		n       int
		ptrs    []string
		err     error
		at      int
		lines   []string
		writes  []string
		slept   time.Duration // in the recovery (without the ticks' own pauses, which tick does not do)
		request bool
	}{
		{name: "sounding and moving", au: []string{"0x61"}, n: 10, ptrs: []string{"100", "200", "300", "400", "500", "600", "700", "800", "900", "1000"}},
		{name: "sounding, capture closed", au: []string{"0x61"}, n: 10, ptrs: []string{""}},
		{name: "no hdmi audio", au: []string{"0x40"}, n: 20, ptrs: []string{"5"}},
		{name: "i2c fails", cfg: Config{LockTimeout: 10}, au: []string{"!"}, n: 20, ptrs: []string{"5"}},
		{name: "unparsable value", cfg: Config{LockTimeout: 10}, au: []string{"oops"}, n: 20, ptrs: []string{"5"}},
		{
			name: "capture stalled", au: []string{"0x61"}, n: 10, ptrs: []string{"100"},
			err: ErrCaptureStalled, at: 4, lines: []string{"[audio] event=audio_capture_stalled seconds=6"},
		},
		{
			name: "stall counter resets when it moves", au: []string{"0x61"}, n: 10, ptrs: []string{"1", "1", "1", "2", "2", "2", "3", "3", "3", "4"},
		},
		{
			name: "soft recovery: lockdet works", cfg: Config{LockTimeout: 10}, au: append(append([]string{}, unlocked5...), "0x41", "0x61", "0x61"), n: 9, ptrs: []string{""},
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=10",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=0x41",
				"[audio] event=audio_soft_recovery step=lockdet au_status0=0x61",
			},
			writes: cat(bufinit, lockdet),
			slept:  100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second,
		},
		{
			name: "soft fails, hardware reset", cfg: Config{LockTimeout: 10, ResetPin: true}, au: []string{"0x41"}, n: 9, ptrs: []string{""},
			err: ErrHWReset, at: 5, request: true,
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=10",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=0x41",
				"[audio] event=audio_soft_recovery step=lockdet au_status0=0x41",
				"[audio] event=audio_soft_recovery step=nco au_status0=0x41",
				"[audio] event=audio_hw_reset_requested au_status0=0x41 seconds=10",
			},
			writes: cat(bufinit, lockdet, nco),
			slept: 100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second +
				300*time.Millisecond + 3*time.Second,
		},
		{
			name: "soft fails, no reset pin: said once, soft not retried", cfg: Config{LockTimeout: 10}, au: []string{"0x41"}, n: 20, ptrs: []string{""},
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=10",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=0x41",
				"[audio] event=audio_soft_recovery step=lockdet au_status0=0x41",
				"[audio] event=audio_soft_recovery step=nco au_status0=0x41",
				"[audio] event=audio_pll_unlocked_no_reset_pin au_status0=0x41",
			},
			writes: cat(bufinit, lockdet, nco),
			slept: 100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second +
				300*time.Millisecond + 3*time.Second,
		},
		{
			name: "recent reset: deferred, then requested once the interval passes", cfg: Config{LockTimeout: 10, ResetPin: true, ResetInterval: 30},
			last: "1760000000\n", au: []string{"0x41"}, n: 30, ptrs: []string{""},
			err: ErrHWReset, at: 16, request: true, // the clock starts at hw-reset.last: 30 s at the 16th look
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=10",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=0x41",
				"[audio] event=audio_soft_recovery step=lockdet au_status0=0x41",
				"[audio] event=audio_soft_recovery step=nco au_status0=0x41",
				"[audio] event=audio_hw_reset_deferred au_status0=0x41 interval=30",
				"[audio] event=audio_hw_reset_requested au_status0=0x41 seconds=32",
			},
			writes: cat(bufinit, lockdet, nco),
			slept: 100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second +
				300*time.Millisecond + 3*time.Second,
		},
		{
			name: "hw-reset.last not digits counts as never", cfg: Config{LockTimeout: 4, ResetPin: true},
			last: "1760000000; reboot\n", au: []string{"0x41", "0x41", "0x41"}, n: 5, ptrs: []string{""},
			err: ErrHWReset, at: 2, request: true,
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=4",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=0x41",
				"[audio] event=audio_soft_recovery step=lockdet au_status0=0x41",
				"[audio] event=audio_soft_recovery step=nco au_status0=0x41",
				"[audio] event=audio_hw_reset_requested au_status0=0x41 seconds=4",
			},
			writes: cat(bufinit, lockdet, nco),
			slept: 100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second +
				300*time.Millisecond + 3*time.Second,
		},
		{
			name: "read fails during the recovery: au_status0=0", cfg: Config{LockTimeout: 2}, au: []string{"0x41", "!", "!", "!", "0x40"}, n: 3, ptrs: []string{""},
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=2",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=0",
				"[audio] event=audio_soft_recovery step=lockdet au_status0=0",
				"[audio] event=audio_soft_recovery step=nco au_status0=0",
				"[audio] event=audio_pll_unlocked_no_reset_pin au_status0=0x41",
			},
			writes: cat(bufinit, lockdet, nco),
			slept: 100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second +
				300*time.Millisecond + 3*time.Second,
		},
		{
			name: "AUTO_CMD2 unreadable: only the nco step", cfg: Config{LockTimeout: 2}, auto2: "!", au: []string{"0x41", "0x41", "0x61"}, n: 2, ptrs: []string{""},
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=2",
				"[audio] event=audio_soft_recovery_skipped step=bufinit reason=register_unreadable",
				"[audio] event=audio_soft_recovery_skipped step=lockdet reason=register_unreadable",
				"[audio] event=audio_soft_recovery step=nco au_status0=0x41",
				"[audio] event=audio_pll_unlocked_no_reset_pin au_status0=0x41",
			},
			writes: nco,
			slept:  300*time.Millisecond + 3*time.Second,
		},
		{
			name: "NCO unreadable: no nco step", cfg: Config{LockTimeout: 2}, nco: "!", au: []string{"0x41"}, n: 2, ptrs: []string{""},
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=2",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=0x41",
				"[audio] event=audio_soft_recovery step=lockdet au_status0=0x41",
				"[audio] event=audio_soft_recovery_skipped step=nco reason=register_unreadable",
				"[audio] event=audio_pll_unlocked_no_reset_pin au_status0=0x41",
			},
			writes: cat(bufinit, lockdet),
			slept:  100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second,
		},
		{name: "lock timeout 0: never", cfg: Config{LockTimeout: 0, ResetPin: true}, au: []string{"0x41"}, n: 50, ptrs: []string{""}},
		{
			name: "decimal value", cfg: Config{LockTimeout: 2}, au: []string{"65", "97"}, n: 3, ptrs: []string{""},
			lines: []string{
				"[audio] event=audio_pll_unlocked au_status0=65 seconds=2",
				"[audio] event=audio_soft_recovery step=bufinit au_status0=97",
			},
			writes: bufinit,
			slept:  100*time.Millisecond + time.Second + 3*time.Second,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.cfg, c.au...)
			if c.auto2 != "" {
				r.chip.auto2 = c.auto2
			}
			if c.nco != "" {
				r.chip.nco = c.nco
			}
			if c.last != "" {
				if err := os.WriteFile(filepath.Join(r.state, "hw-reset.last"), []byte(c.last), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err, at := r.ticks(t, c.n, c.ptrs...)
			if !errors.Is(err, c.err) || at != c.at {
				t.Errorf("err %v at tick %d, want %v at %d", err, at, c.err, c.at)
			}
			if got := r.lines(); !reflect.DeepEqual(got, c.lines) {
				t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(c.lines, "\n"))
			}
			if !reflect.DeepEqual(r.chip.writes, c.writes) {
				t.Errorf("writes %v, want %v", r.chip.writes, c.writes)
			}
			if r.slept != c.slept {
				t.Errorf("slept %v, want %v", r.slept, c.slept)
			}
			if r.requested() != c.request {
				t.Errorf("hw-reset.request %v", r.requested())
			}
			if len(r.chip.bad) > 0 {
				t.Errorf("unexpected calls %v", r.chip.bad)
			}
		})
	}
}

// The hotplug pulse: the last software step, only with the engine's leave; it waits for the lock.
func TestWatcherHotplug(t *testing.T) {
	soft := []string{
		"[audio] event=audio_pll_unlocked au_status0=0x41 seconds=2",
		"[audio] event=audio_soft_recovery step=bufinit au_status0=0x41",
		"[audio] event=audio_soft_recovery step=lockdet au_status0=0x41",
		"[audio] event=audio_soft_recovery step=nco au_status0=0x41",
	}
	softSlept := 100*time.Millisecond + time.Second + 3*time.Second + 100*time.Millisecond + 1500*time.Millisecond + time.Second + 3*time.Second +
		300*time.Millisecond + 3*time.Second
	for _, c := range []struct {
		name     string
		may, ok  bool
		pin      bool
		au       []string
		err      error
		lines    []string
		hotplugs int
		slept    time.Duration
	}{
		{
			name: "allowed: locks 2 s later", may: true, ok: true, au: []string{"0x41", "0x41", "0x41", "0x41", "0x41", "0x61"},
			lines:    append(append([]string{}, soft...), "[audio] event=audio_soft_recovery step=hotplug au_status0=0x61"),
			hotplugs: 1, slept: softSlept + 2*time.Second,
		},
		{
			name: "on air: skipped", may: false, ok: true, au: []string{"0x41"},
			lines: append(append([]string{}, soft...), "[audio] event=audio_soft_recovery_skipped step=hotplug reason=on_air",
				"[audio] event=audio_pll_unlocked_no_reset_pin au_status0=0x41"),
			slept: softSlept,
		},
		{
			name: "EDID cannot be written", may: true, ok: false, au: []string{"0x41"},
			lines: append(append([]string{}, soft...), "[audio] event=audio_soft_recovery_skipped step=hotplug reason=edid_failed",
				"[audio] event=audio_pll_unlocked_no_reset_pin au_status0=0x41"),
			hotplugs: 1, slept: softSlept,
		},
		{
			name: "no lock after it: hardware reset", may: true, ok: true, pin: true, au: []string{"0x41"}, err: ErrHWReset,
			lines: append(append([]string{}, soft...), "[audio] event=audio_soft_recovery step=hotplug au_status0=0x41",
				"[audio] event=audio_hw_reset_requested au_status0=0x41 seconds=2"),
			hotplugs: 1, slept: softSlept + hotplugSettle,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, Config{LockTimeout: 2, ResetPin: c.pin}, c.au...)
			hotplugs := 0
			r.w.sys.Hotplug = func() bool { hotplugs++; return c.ok }
			r.w.sys.MayHotplug = func() bool { return c.may }
			if err, _ := r.ticks(t, 1, ""); !errors.Is(err, c.err) {
				t.Errorf("err %v, want %v", err, c.err)
			}
			if got := r.lines(); !reflect.DeepEqual(got, c.lines) {
				t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(c.lines, "\n"))
			}
			if hotplugs != c.hotplugs || r.slept != c.slept {
				t.Errorf("hotplugs %d, slept %v; want %d, %v", hotplugs, r.slept, c.hotplugs, c.slept)
			}
			if !reflect.DeepEqual(r.chip.writes, cat(bufinit, lockdet, nco)) {
				t.Errorf("writes %v", r.chip.writes)
			}
		})
	}
}

// After the soft recovery worked, the counters start again; locked again, a new loss of lock tries
// the software recovery again (the launcher resets soft_tried and warned only when locked).
func TestWatcherRelock(t *testing.T) {
	r := newRig(t, Config{LockTimeout: 4}, "0x41", "0x41", "0x41", "0x41", "0x41", "0x61", "0x41", "0x41", "0x41", "0x41", "0x41", "0x41")
	if err, _ := r.ticks(t, 8, ""); err != nil {
		t.Fatal(err)
	}
	soft := 0
	for _, l := range r.lines() {
		if strings.Contains(l, "event=audio_pll_unlocked ") {
			soft++
		}
	}
	if soft != 2 {
		t.Errorf("software recovery tried %d times:\n%s", soft, r.logs)
	}
	// Without samples the counters reset but the "said once" does not (like the launcher)
	r2 := newRig(t, Config{LockTimeout: 2}, "0x41", "0x41", "0x41", "0x41", "0x41", "0x40", "0x41")
	if err, _ := r2.ticks(t, 3, ""); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(r2.logs.String(), "no_reset_pin"); n != 1 {
		t.Errorf("no_reset_pin %d times", n)
	}
}

// Run: the period, the restart error, and ctx.
func TestWatcherRun(t *testing.T) {
	r := newRig(t, Config{LockTimeout: 10}, "0x61")
	r.ptr(t, "7")
	if err := r.w.Run(context.Background()); !errors.Is(err, ErrCaptureStalled) {
		t.Fatalf("run: %v", err)
	}
	if r.slept != 4*Every { // stalled at the 4th look (6 s)
		t.Errorf("slept %v", r.slept)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r2 := newRig(t, Config{}, "0x61")
	r2.w.sys.Sleep = nil
	done := make(chan error, 1)
	go func() { done <- r2.w.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("cancelled: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run does not end with its context")
	}
}

// A stop during the recovery ends it between steps, never inside one (the chip is left in automatic).
func TestWatcherStopDuringRecovery(t *testing.T) {
	r := newRig(t, Config{LockTimeout: 2}, "0x41")
	ctx, cancel := context.WithCancel(context.Background())
	r.w.sys.Sleep = func(d time.Duration) {
		r.slept += d
		if d == settle {
			cancel()
		}
	}
	r.ptr(t, "")
	if err := r.w.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.chip.writes, bufinit) || r.chip.writes[len(r.chip.writes)-1] != "0x86 0x04 0x09" {
		t.Errorf("writes %v", r.chip.writes)
	}
	if r.requested() {
		t.Error("reset requested on stop")
	}
}

func TestEnabled(t *testing.T) {
	r := newRig(t, Config{}, "0x61")
	s := Sys{Root: r.root}
	if !Enabled(s, true, "tegrasndt210ref") || Enabled(s, false, "tegrasndt210ref") || Enabled(s, true, "other") || Enabled(s, true, "") {
		t.Error("Enabled")
	}
}

func TestValue(t *testing.T) {
	for in, want := range map[string]uint64{"0x61": 0x61, "0x41": 0x41, "0": 0, "97": 97, "": 0, "0xzz": 0, "oops": 0} {
		if got := Value(in); got != want {
			t.Errorf("%q: %d", in, got)
		}
	}
}
