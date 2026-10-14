package device

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// System is what the device settings read and write in the operating system: time zone, automatic
// time, the clock and the hostname. On the board it is Systemd (timedated/hostnamed through polkit,
// SETUP.md §7); the tests and the simulator use MemSystem.
type System interface {
	Now() time.Time
	TimeInfo(ctx context.Context) (TimeInfo, error)
	SetTimezone(ctx context.Context, tz string) error
	SetNTP(ctx context.Context, on bool) error
	// SetTime sets the clock (only with NTP off: timedated refuses otherwise)
	SetTime(ctx context.Context, t time.Time) error
	Hostname() string
	// SetHostname sets the static and the transient hostname (not the pretty one)
	SetHostname(ctx context.Context, name string) error
	ListTimezones(ctx context.Context) ([]string, error)
}

// TimeInfo: the clock settings timedated reports.
type TimeInfo struct {
	Timezone string
	NTP      bool  // automatic time on
	Synced   bool  // the clock is synchronised
	RTC      *bool // the board has a readable RTC (nil = unknown)
}

// reTimezone: the shape of a zone id (the list decides which exist).
var reTimezone = regexp.MustCompile(`^[A-Za-z0-9_+-]{1,64}(/[A-Za-z0-9_+-]{1,64}){0,3}$`)

// reHostname: a hostname as Hostname derives it.
var reHostname = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Systemd: the real system through timedatectl and hostnamectl (writes) and busctl (reads; systemd 237
// on the board has no `timedatectl show`). Each call is a short process; nothing polls.
//
// The app (user eirl-encoder, no root) is allowed by the polkit file eirl-encoder-time.pkla, for these
// exact actions: org.freedesktop.timedate1.set-timezone, set-ntp, set-time and
// org.freedesktop.hostname1.set-hostname, set-static-hostname.
type Systemd struct {
	// Run executes a command and returns its standard output (nil = os/exec, LC_ALL=C, 15 s)
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (s Systemd) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return out, fmt.Errorf("%s %s: %w: %s", name, firstArg(args), err, msg)
	}
	return out, nil
}

func firstArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func (Systemd) Now() time.Time { return time.Now() }

// TimeInfo reads the timedate1 properties: `busctl get-property` prints one per line, e.g. `s "UTC"`,
// `b true`, `t 1760400000000000`.
func (s Systemd) TimeInfo(ctx context.Context) (TimeInfo, error) {
	out, err := s.run(ctx, "busctl", "get-property", "org.freedesktop.timedate1", "/org/freedesktop/timedate1",
		"org.freedesktop.timedate1", "Timezone", "NTP", "NTPSynchronized", "RTCTimeUSec")
	if err != nil {
		return TimeInfo{}, err
	}
	return parseTimeProps(out)
}

func parseTimeProps(out []byte) (TimeInfo, error) {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 4 {
		return TimeInfo{}, fmt.Errorf("busctl: %d properties, want 4", len(lines))
	}
	var ti TimeInfo
	tz, ok := strings.CutPrefix(lines[0], "s ")
	if !ok {
		return TimeInfo{}, errors.New("busctl: Timezone is not a string")
	}
	if uq, err := strconv.Unquote(tz); err == nil {
		tz = uq
	}
	if tz != "" && !reTimezone.MatchString(tz) {
		return TimeInfo{}, fmt.Errorf("busctl: odd time zone %q", tz)
	}
	ti.Timezone = tz
	ti.NTP = lines[1] == "b true"
	ti.Synced = lines[2] == "b true"
	if v, ok := strings.CutPrefix(lines[3], "t "); ok {
		rtc := v != "0"
		ti.RTC = &rtc
	}
	return ti, nil
}

func (s Systemd) SetTimezone(ctx context.Context, tz string) error {
	if !reTimezone.MatchString(tz) {
		return fmt.Errorf("invalid time zone %q", tz)
	}
	_, err := s.run(ctx, "timedatectl", "--no-ask-password", "set-timezone", tz)
	return err
}

func (s Systemd) SetNTP(ctx context.Context, on bool) error {
	_, err := s.run(ctx, "timedatectl", "--no-ask-password", "set-ntp", strconv.FormatBool(on))
	return err
}

// SetTime: timedatectl reads the time in the system time zone, so it is written in that zone (after a
// time zone change in the same request, timedatectl is a new process and reads the new one).
func (s Systemd) SetTime(ctx context.Context, t time.Time) error {
	ti, err := s.TimeInfo(ctx)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(ti.Timezone)
	if err != nil || ti.Timezone == "" {
		loc = time.UTC
	}
	_, err = s.run(ctx, "timedatectl", "--no-ask-password", "set-time", t.In(loc).Format("2006-01-02 15:04:05"))
	return err
}

func (Systemd) Hostname() string {
	h, _ := os.Hostname()
	return strings.ToLower(h)
}

// SetHostname: static and transient (not --pretty: that would need set-machine-info, which the polkit
// file does not grant).
func (s Systemd) SetHostname(ctx context.Context, name string) error {
	if !reHostname.MatchString(name) {
		return fmt.Errorf("invalid hostname %q", name)
	}
	_, err := s.run(ctx, "hostnamectl", "--no-ask-password", "--static", "--transient", "set-hostname", name)
	return err
}

func (s Systemd) ListTimezones(ctx context.Context) ([]string, error) {
	out, err := s.run(ctx, "timedatectl", "--no-pager", "list-timezones")
	if err != nil {
		return nil, err
	}
	var zones []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); reTimezone.MatchString(l) {
			zones = append(zones, l)
		}
	}
	if len(zones) == 0 {
		return nil, errors.New("timedatectl list-timezones: empty")
	}
	return zones, nil
}

// --- time zones with their offset ------------------------------------------------------------------

// Zone is a time zone and its UTC offset now ("+02:00").
type Zone struct {
	ID     string `json:"id"`
	Offset string `json:"offset"`
}

// Zones caches the list of time zones (it only changes with tzdata) and their offsets (recomputed every
// 10 min: DST changes them).
type Zones struct {
	Sys System

	mu    sync.Mutex
	ids   []string
	locs  map[string]*time.Location
	zones []Zone
	at    time.Time
}

const zonesRefresh = 10 * time.Minute

// List returns the zones sorted by id, with the offset at now.
func (z *Zones) List(ctx context.Context, now time.Time) ([]Zone, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if err := z.load(ctx); err != nil {
		return nil, err
	}
	if z.zones == nil || now.Sub(z.at) > zonesRefresh || now.Before(z.at) {
		z.zones = make([]Zone, 0, len(z.ids))
		for _, id := range z.ids {
			zone := Zone{ID: id}
			if loc := z.locs[id]; loc != nil {
				zone.Offset = now.In(loc).Format("-07:00")
			}
			z.zones = append(z.zones, zone)
		}
		z.at = now
	}
	return slices.Clone(z.zones), nil
}

// Valid: id is one of the system's zones.
func (z *Zones) Valid(ctx context.Context, id string) (bool, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if err := z.load(ctx); err != nil {
		return false, err
	}
	_, found := slices.BinarySearch(z.ids, id)
	return found, nil
}

func (z *Zones) load(ctx context.Context) error {
	if z.ids != nil {
		return nil
	}
	ids, err := z.Sys.ListTimezones(ctx)
	if err != nil {
		return err
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	z.locs = map[string]*time.Location{}
	for _, id := range ids {
		if loc, err := time.LoadLocation(id); err == nil {
			z.locs[id] = loc
		}
	}
	z.ids = ids
	return nil
}

// --- in-memory system (simulator and tests) -----------------------------------------------------------

// MemSystem is a System in memory: a clock with an offset, a time zone, NTP and a hostname. Calls
// records every change ("timezone Europe/Madrid", "ntp false", "time …", "hostname …"); Fail, if set,
// makes every change fail.
type MemSystem struct {
	mu       sync.Mutex
	TZ       string
	NTP      bool
	Synced   bool
	Host     string
	ZoneIDs  []string
	Offset   time.Duration
	Fail     error
	Calls    []string
	NoRTC    bool
	ListFail error
}

// NewMemSystem: UTC, NTP on and synchronised, hostname host, a short list of zones.
func NewMemSystem(host string) *MemSystem {
	return &MemSystem{TZ: "UTC", NTP: true, Synced: true, Host: host,
		ZoneIDs: []string{"UTC", "Europe/Madrid", "Europe/Paris", "Europe/London", "America/New_York",
			"America/Mexico_City", "America/Argentina/Buenos_Aires", "Asia/Tokyo", "Atlantic/Canary"}}
}

func (m *MemSystem) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Add(m.Offset)
}

func (m *MemSystem) TimeInfo(context.Context) (TimeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rtc := !m.NoRTC
	return TimeInfo{Timezone: m.TZ, NTP: m.NTP, Synced: m.NTP && m.Synced, RTC: &rtc}, nil
}

func (m *MemSystem) change(call string, fn func()) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	m.Calls = append(m.Calls, call)
	fn()
	return nil
}

func (m *MemSystem) SetTimezone(_ context.Context, tz string) error {
	return m.change("timezone "+tz, func() { m.TZ = tz })
}

func (m *MemSystem) SetNTP(_ context.Context, on bool) error {
	return m.change("ntp "+strconv.FormatBool(on), func() {
		m.NTP = on
		if on {
			m.Offset = 0 // NTP brings the clock back
		}
	})
}

func (m *MemSystem) SetTime(_ context.Context, t time.Time) error {
	m.mu.Lock()
	ntp := m.NTP
	m.mu.Unlock()
	if ntp {
		return errors.New("automatic time synchronization is enabled")
	}
	return m.change("time "+t.UTC().Format(time.RFC3339), func() { m.Offset = time.Until(t) })
}

func (m *MemSystem) Hostname() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Host
}

func (m *MemSystem) SetHostname(_ context.Context, name string) error {
	return m.change("hostname "+name, func() { m.Host = name })
}

func (m *MemSystem) ListTimezones(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListFail != nil {
		return nil, m.ListFail
	}
	return slices.Clone(m.ZoneIDs), nil
}

// Changes returns the recorded changes.
func (m *MemSystem) Changes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.Calls)
}
