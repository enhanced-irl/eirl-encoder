package api

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// --- device settings (SETUP.md §2): GET/PUT /device, GET /device/timezones ----------------------------

// Versions: the version files GET /device reports (the app's own version is Info.Version).
type Versions struct {
	ReleaseFile string // /usr/lib/eirl-encoder/VERSION: the release (one line, e.g. 0.1.0)
	EngineFile  string // /usr/lib/eirl-encoder/engine/VERSION: the installed engine
}

// readVersion: the first whitespace-separated field of a version file ("0.1.0"; the engine's file may carry
// more text after it), "" if missing; at most 64 printable characters.
func readVersion(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return ""
	}
	f2 := strings.Fields(sc.Text())
	if len(f2) == 0 {
		return ""
	}
	v := f2[0]
	if len(v) > 64 || strings.IndexFunc(v, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return ""
	}
	return v
}

type deviceView struct {
	Language  string      `json:"language"`
	Languages []string    `json:"languages"`
	Name      string      `json:"name"`
	Hostname  string      `json:"hostname"`
	Timezone  string      `json:"timezone"`
	Time      timeView    `json:"time"`
	Version   versionView `json:"version"`
}

type timeView struct {
	Now    string `json:"now"`    // RFC 3339, in the device time zone
	NTP    bool   `json:"ntp"`    // automatic time on
	Synced bool   `json:"synced"` // the clock is synchronised
	RTC    *bool  `json:"rtc,omitempty"`
}

type versionView struct {
	Release string `json:"release"`
	App     string `json:"app"`
	Engine  string `json:"engine"`
}

// deviceOff: true (and answers 503) without a device store.
func (s *Server) deviceOff(w http.ResponseWriter, r *http.Request) bool {
	if s.Device == nil {
		fail(w, r, 503, "device_unavailable", "device.unavailable")
		return true
	}
	return false
}

// now: the system clock (System, which the simulator can move).
func (s *Server) now() time.Time {
	if s.System != nil {
		return s.System.Now()
	}
	return time.Now()
}

// timeInfo: the clock settings, or a zero value (logged) if the system does not answer.
func (s *Server) timeInfo(ctx context.Context) device.TimeInfo {
	if s.System == nil {
		return device.TimeInfo{}
	}
	ti, err := s.System.TimeInfo(ctx)
	if err != nil {
		s.Log.Warn("read the clock settings", "err", err)
	}
	return ti
}

func (s *Server) deviceView(ctx context.Context) deviceView {
	d := s.Device.Get()
	ti := s.timeInfo(ctx)
	now := s.now()
	if loc, err := time.LoadLocation(ti.Timezone); err == nil && ti.Timezone != "" {
		now = now.In(loc)
	}
	return deviceView{
		Language: d.Language, Languages: i18n.Languages, Name: d.Name, Hostname: s.hostname(),
		Timezone: ti.Timezone,
		Time:     timeView{Now: now.Format(time.RFC3339), NTP: ti.NTP, Synced: ti.Synced, RTC: ti.RTC},
		Version: versionView{Release: readVersion(s.Versions.ReleaseFile), App: s.Info.Version,
			Engine: readVersion(s.Versions.EngineFile)},
	}
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	writeJSON(w, 200, s.deviceView(r.Context()))
}

// Range of a manual date: a board without a battery in its RTC wakes up in 1970 or 2018.
const (
	minYear = 2020
	maxYear = 2100
)

// putDevice: any of {"language", "name", "timezone", "ntp", "time"}. Before an admin password exists, the
// web may only change the language (the local socket, the screen, everything). The system changes go
// first (hostname, time zone, NTP, clock), then device.json.
func (s *Server) putDevice(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	var body struct {
		Language *string `json:"language"`
		Name     *string `json:"name"`
		Timezone *string `json:"timezone"`
		NTP      *bool   `json:"ntp"`
		Time     *string `json:"time"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	system := body.Name != nil || body.Timezone != nil || body.NTP != nil || body.Time != nil
	if body.Language == nil && !system {
		fail(w, r, 400, "bad_request", "common.nothing_to_change")
		return
	}
	if system && !s.Auth.Configured() && !isLocal(r) {
		fail(w, r, 503, "setup_required", "device.language_only")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	ti := s.timeInfo(ctx)

	errs := config.FieldErrors{}
	if body.Language != nil && !i18n.Supported(*body.Language) {
		errs["language"] = i18n.Ref("device.bad_language", strings.Join(i18n.Languages, ", "))
	}
	var name string
	if body.Name != nil {
		name = device.NormalizeText(*body.Name)
		if !device.ValidName(name) {
			errs["name"] = i18n.Ref("device.bad_name", device.NameMax)
		}
	}
	if body.Timezone != nil && !s.validZone(ctx, *body.Timezone) {
		errs["timezone"] = i18n.Ref("device.bad_timezone")
	}
	var at time.Time
	if body.Time != nil {
		t, err := time.Parse(time.RFC3339, *body.Time)
		ntp := ti.NTP
		if body.NTP != nil {
			ntp = *body.NTP
		}
		switch {
		case err != nil || t.Year() < minYear || t.Year() > maxYear:
			errs["time"] = i18n.Ref("device.bad_time", minYear, maxYear)
		case ntp:
			errs["time"] = i18n.Ref("device.time_needs_manual")
		}
		at = t
	}
	if len(errs) > 0 {
		fieldErrors(w, r, errs)
		return
	}
	if system && s.System == nil {
		fail(w, r, 503, "system_unavailable", "device.unavailable")
		return
	}

	var changed []any
	sysFail := func(what string, err error) {
		s.Log.Error("device settings: "+what, "err", err)
		fail(w, r, 502, "system_failed", "device.system_failed", what)
	}
	if body.Name != nil {
		if h := device.Hostname(name); h != s.hostname() {
			if err := s.System.SetHostname(ctx, h); err != nil {
				sysFail("hostname", err)
				return
			}
			changed = append(changed, "hostname", h)
		}
	}
	if body.Timezone != nil && *body.Timezone != ti.Timezone {
		if err := s.System.SetTimezone(ctx, *body.Timezone); err != nil {
			sysFail("timezone", err)
			return
		}
		changed = append(changed, "timezone", *body.Timezone)
	}
	if body.NTP != nil && *body.NTP != ti.NTP {
		if err := s.System.SetNTP(ctx, *body.NTP); err != nil {
			sysFail("ntp", err)
			return
		}
		changed = append(changed, "ntp", *body.NTP)
	}
	if body.Time != nil {
		if err := s.System.SetTime(ctx, at); err != nil {
			sysFail("time", err)
			return
		}
		changed = append(changed, "time", at.UTC().Format(time.RFC3339))
	}
	if body.Language != nil || body.Name != nil {
		_, err := s.Device.Update(func(d *device.Settings) error {
			if body.Language != nil {
				d.Language = *body.Language
			}
			if body.Name != nil {
				d.Name = name
			}
			return nil
		})
		if err != nil {
			s.Log.Error("save device.json", "err", err)
			fail(w, r, 500, "internal", "common.save_failed")
			return
		}
		if body.Language != nil {
			changed = append(changed, "language", *body.Language)
		}
		if body.Name != nil {
			changed = append(changed, "name", name)
		}
	}
	if len(changed) > 0 {
		s.Log.Info("device settings changed", append(changed, "via", via(r))...)
	}
	writeJSON(w, 200, s.deviceView(ctx))
}

// validZone: one of the system's zones (the cached list); without the list, a zone Go can load.
func (s *Server) validZone(ctx context.Context, id string) bool {
	if s.Zones != nil {
		if ok, err := s.Zones.Valid(ctx, id); err == nil {
			return ok
		}
	}
	_, err := time.LoadLocation(id)
	return err == nil && id != "" && id != "Local"
}

func (s *Server) getTimezones(w http.ResponseWriter, r *http.Request) {
	if s.deviceOff(w, r) {
		return
	}
	if s.Zones == nil {
		fail(w, r, 503, "timezones_unavailable", "device.timezones_failed")
		return
	}
	zones, err := s.Zones.List(r.Context(), s.now())
	if err != nil {
		s.Log.Error("time zone list", "err", err)
		fail(w, r, 503, "timezones_unavailable", "device.timezones_failed")
		return
	}
	writeJSON(w, 200, map[string]any{"zones": zones})
}

// via: who made a change, for the logs: the screen (local socket) or the web.
func via(r *http.Request) string {
	if isLocal(r) {
		return "screen"
	}
	return "web"
}
