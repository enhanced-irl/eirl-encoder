package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// simUpdate: a fake updater with the contract's shape (UPDATES.md; CONTRACT §4): GET /update, POST /update/check and
// POST /update/install. A check finds 0.2.0 (checking 1.5 s → available 3 s → downloading 8 s → ready); installing
// takes 6 s and ends idle on 0.2.0. Installing while streaming: 409 streaming; before ready: 409 not_ready.
type simUpdate struct {
	eng *simEngine

	mu        sync.Mutex
	current   string
	state     string // idle | checking | available | downloading | ready | installing
	since     time.Time
	lastCheck *time.Time
}

const simNewVersion = "0.2.0"

var simNotes = map[string]string{
	"es": "Asistente de primera configuración, fuentes con alias y la interfaz en español, inglés y francés.",
	"en": "First-boot setup wizard, sources with aliases and the interface in Spanish, English and French.",
	"fr": "Assistant de première configuration, sources avec alias et l’interface en espagnol, anglais et français.",
}

func newSimUpdate(eng *simEngine) *simUpdate {
	return &simUpdate{eng: eng, current: "0.1.0", state: "idle", since: time.Now()}
}

type simUpdateView struct {
	Current   string          `json:"current"`
	State     string          `json:"state"`
	Available *simUpdateAvail `json:"available"`
	Progress  float64         `json:"progress"`
	LastCheck *time.Time      `json:"lastCheck"`
	Error     string          `json:"error,omitempty"`
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
		case u.state == "available" && age > 3*time.Second:
			u.state = "downloading"
		case u.state == "downloading" && age > 8*time.Second:
			u.state = "ready"
		case u.state == "installing" && age > 6*time.Second:
			u.state, u.current = "idle", simNewVersion
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
	v := simUpdateView{Current: u.current, State: u.state, LastCheck: u.lastCheck}
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
				u.state, u.since, u.lastCheck = "checking", time.Now(), &now
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
		default:
			fail(404, "not_found", "common.unknown_route")
		}
	})
}
