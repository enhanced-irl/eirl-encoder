package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// simUpdate: a fake updater with the contract's shape (UPDATES.md; CONTRACT §4): GET /update, POST /update/check,
// POST /update/install and POST /update/reboot. A check finds 0.2.0 (checking 1.5 s → available 3 s → downloading 8 s →
// ready); installing takes 6 s and ends idle on 0.2.0, its system layer waiting for a restart (HDMI input, modems).
// Installing while streaming: 409 streaming; before ready: 409 not_ready. Restarting: 409 streaming | nothing_pending;
// it "restarts" at once (the pending areas are cleared). -update metered: 0.2.0 is known and waits for Wi-Fi (a check
// downloads it); restart: on 0.2.0 with the restart pending; sysfailed: its system layer did not install.
type simUpdate struct {
	eng *simEngine

	mu        sync.Mutex
	current   string
	state     string // idle | checking | available | downloading | ready | installing
	since     time.Time
	lastCheck *time.Time
	metered   bool     // the available release waits for Wi-Fi (until a check)
	sysState  string   // "" (no system layer state) | ok | failed
	pending   []string // what waits for a restart
}

const simNewVersion = "0.2.0"

var simNotes = map[string]string{
	"es": "Asistente de primera configuración, fuentes con alias y la interfaz en español, inglés y francés.",
	"en": "First-boot setup wizard, sources with aliases and the interface in Spanish, English and French.",
	"fr": "Assistant de première configuration, sources avec alias et l’interface en espagnol, anglais et français.",
}

func newSimUpdate(eng *simEngine, scenario string) *simUpdate {
	u := &simUpdate{eng: eng, current: "0.1.0", state: "idle", since: time.Now()}
	switch scenario {
	case "metered":
		u.state, u.metered = "available", true
	case "restart":
		u.current, u.sysState, u.pending = simNewVersion, "ok", []string{"hdmi", "uplinks"}
	case "sysfailed":
		u.current, u.sysState = simNewVersion, "failed"
	}
	return u
}

type simUpdateView struct {
	Current          string          `json:"current"`
	State            string          `json:"state"`
	Available        *simUpdateAvail `json:"available"`
	Progress         float64         `json:"progress"`
	LastCheck        *time.Time      `json:"lastCheck"`
	Error            string          `json:"error,omitempty"`
	Streaming        bool            `json:"streaming"`
	DownloadWaits    string          `json:"downloadWaits,omitempty"`
	DownloadWaitsMsg string          `json:"downloadWaitsMessage,omitempty"`
	System           *simSystemView  `json:"system,omitempty"`
}

// simSystemView is GET /update's "system" (internal/api UpdateSystemView).
type simSystemView struct {
	State           string   `json:"state"`
	Error           string   `json:"error,omitempty"`
	Message         string   `json:"message,omitempty"`
	RestartRequired bool     `json:"restartRequired"`
	Pending         []string `json:"pending,omitempty"`
	RestartMessage  string   `json:"restartMessage,omitempty"`
}

type simUpdateAvail struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
	Size    int64  `json:"size"`
}

// advance moves the state machine along the clock (called with the lock held).
func (u *simUpdate) advance(now time.Time) {
	for {
		age := now.Sub(u.since)
		switch {
		case u.state == "checking" && age > 1500*time.Millisecond:
			if u.current == simNewVersion {
				u.state = "idle"
			} else {
				u.state = "available"
			}
		case u.state == "available" && age > 3*time.Second && !u.metered:
			u.state = "downloading"
		case u.state == "downloading" && age > 8*time.Second:
			u.state = "ready"
		case u.state == "installing" && age > 6*time.Second:
			u.state, u.current = "idle", simNewVersion
			u.sysState, u.pending = "ok", []string{"hdmi", "uplinks"}
		default:
			return
		}
		u.since = u.since.Add(map[string]time.Duration{"available": 1500 * time.Millisecond, "downloading": 3 * time.Second,
			"ready": 8 * time.Second, "idle": 6 * time.Second}[u.state])
	}
}

func (u *simUpdate) view(lang string) simUpdateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	u.advance(now)
	v := simUpdateView{Current: u.current, State: u.state, LastCheck: u.lastCheck, Streaming: u.eng.streaming()}
	if u.metered && u.state == "available" {
		v.DownloadWaits, v.DownloadWaitsMsg = "metered", i18n.Msg(lang, "update.waits.metered")
	}
	if u.sysState != "" {
		s := &simSystemView{State: u.sysState, Pending: u.pending, RestartRequired: len(u.pending) > 0}
		if u.sysState == "failed" {
			s.Error, s.Message = "apt", i18n.Msg(lang, "update.system.failed")
		}
		if s.RestartRequired {
			names := make([]string, len(u.pending))
			for i, a := range u.pending {
				names[i] = i18n.Msg(lang, "update.area."+a)
			}
			s.RestartMessage = i18n.Msg(lang, "update.restart_required", strings.Join(names, ", "))
		}
		v.System = s
	}
	switch u.state {
	case "available", "downloading", "ready", "installing":
		v.Available = &simUpdateAvail{Version: simNewVersion, Notes: simNotes[lang], Size: 12_345_678}
	}
	switch u.state {
	case "downloading":
		v.Progress = min(now.Sub(u.since).Seconds()/8, 0.99)
	case "ready":
		v.Progress = 1
	case "installing":
		v.Progress = min(now.Sub(u.since).Seconds()/6, 0.99)
	}
	return v
}

func (u *simUpdate) handler(dev *device.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang, ok := i18n.LangFrom(r.Context())
		if !ok {
			lang = i18n.Negotiate(r.Header.Get("Accept-Language"), dev.Language())
		}
		reply := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		fail := func(code int, kind, key string) {
			reply(code, map[string]string{"error": kind, "message": i18n.Msg(lang, key)})
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/update":
			reply(200, u.view(lang))
		case "POST /api/v1/update/check":
			u.mu.Lock()
			u.advance(time.Now())
			if u.state == "idle" || u.state == "available" {
				now := time.Now().UTC().Truncate(time.Second)
				u.state, u.since, u.lastCheck, u.metered = "checking", time.Now(), &now, false // a check downloads at once
			}
			u.mu.Unlock()
			reply(202, u.view(lang))
		case "POST /api/v1/update/install":
			if u.eng.streaming() {
				fail(409, "streaming", "update.streaming")
				return
			}
			u.mu.Lock()
			u.advance(time.Now())
			ready := u.state == "ready"
			if ready {
				u.state, u.since = "installing", time.Now()
			}
			u.mu.Unlock()
			if !ready {
				fail(409, "not_ready", "update.not_ready")
				return
			}
			reply(202, u.view(lang))
		case "POST /api/v1/update/reboot":
			u.mu.Lock()
			pending := len(u.pending) > 0
			u.mu.Unlock()
			switch {
			case !pending:
				fail(409, "nothing_pending", "update.nothing_pending")
			case u.eng.streaming():
				fail(409, "streaming", "update.reboot_streaming")
			default:
				u.mu.Lock()
				u.pending = nil // the simulator "restarts" at once
				u.mu.Unlock()
				reply(202, map[string]bool{"rebooting": true})
			}
		default:
			fail(404, "not_found", "common.unknown_route")
		}
	})
}
