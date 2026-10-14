// Package linkhealth decides the colour of each internet link (MODEMS.md §5): good (green), weak (amber),
// bad (red) or off (grey, switched off by the user). It is the ONE place that judges a link: the web, the
// touch screen and the cloud only paint the level and translate the code.
//
// The level is the worst of two judgements: the radio (signal of a modem or of the Wi-Fi client) and the
// connection (no IP, no internet, a repeated IP, a SIM that asks for its PIN…). A modem with full bars but
// no data is not green, and a cable has no radio at all.
//
// Tracker adds the hysteresis, so that a dot does not blink while walking with the backpack.
package linkhealth

import (
	"sync"
	"time"
)

// Level of a link, worst last (Off is apart: the user's choice, not a fault).
type Level string

const (
	Good Level = "good"
	Weak Level = "weak"
	Bad  Level = "bad"
	Off  Level = "off"
)

func (l Level) rank() int {
	switch l {
	case Good:
		return 0
	case Weak:
		return 1
	case Bad:
		return 2
	}
	return 3 // Off
}

// Codes: why a link has its level. Stable: the web and the screen translate them (es/en/fr).
const (
	CodeOK            = "ok"
	CodeOff           = "off"            // switched off for streaming
	CodeConflict      = "ip_conflict"    // same IP as another link
	CodeDisconnected  = "disconnected"   // no IP or no route
	CodeSearching     = "searching"      // the modem looks for a network
	CodeSIMMissing    = "sim_missing"    // no SIM
	CodeSIMPIN        = "sim_pin"        // the SIM asks for its PIN
	CodeSIMPUK        = "sim_puk"        // the SIM is blocked (PUK)
	CodeSIMError      = "sim_error"      // the SIM does not answer
	CodeModemFailed   = "modem_failed"   // the modem itself is in error
	CodeRegDenied     = "denied"         // the network refuses the SIM
	CodeRoamingOff    = "roaming_off"    // roaming, and data roaming is not allowed
	CodeNoInternet    = "no_internet"    // connected, but HTTPS to the relay fails
	CodeSignalMinimal = "signal_minimal" // radio below the red threshold
	CodeSignalWeak    = "signal_weak"    // radio in the amber band
	CodeSlowTech      = "slow_tech"      // 3G (amber at best) or 2G (red)
	CodeConnecting    = "connecting"     // streaming, enabled, and SRTLA does not use it yet
)

// Radio: what is known of a radio link. Every field optional; nil = not reported.
type Radio struct {
	Tech string   // 2g | 3g | 4g | 5g-nsa | 5g (modems); empty = unknown
	RSRP *float64 // dBm (4G/5G)
	RSRQ *float64 // dB
	SINR *float64 // dB
	RSSI *float64 // dBm
	Bars *int     // 0-5, when the modem only gives bars (HiLink SignalIcon)
	// Quality: 0-100 when the modem gives nothing better (ModemManager's SignalQuality, from RSSI)
	Quality *int
}

// Modem: the state of a modem relevant to its colour.
type Modem struct {
	// State: failed | locked | disabled | searching | registered | connecting | connected | disconnected | unknown
	State string
	// SIM: ready | missing | pin | puk | error | unknown
	SIM string
	// Registration: home | roaming | searching | denied | idle | unknown
	Registration string
	Radio        Radio
}

// Input: everything known about one link.
type Input struct {
	Kind string // ethernet | usb | wifi | modem
	// Available: in the dispatcher's list (it has an IPv4 and a default route)
	Available bool
	Disabled  bool // switched off by the user
	Conflict  bool
	// NoInternet: the HTTPS check through it fails (WIFI.md §13)
	NoInternet bool
	// Streaming: the stream runs over SRTLA right now; InUse: and this link carries part of it
	Streaming, InUse bool
	// WiFiPercent: the Wi-Fi client's signal, 0-100 (NetworkManager's Strength); only for kind wifi
	WiFiPercent *int
	Modem       *Modem
}

// Result: a level and its code.
type Result struct {
	Level Level  `json:"health"`
	Code  string `json:"healthCode"`
}

// Thresholds (MODEMS.md §5). A starting point, to be calibrated against real streams (phase M5).
const (
	rsrpGood = -100.0 // dBm: from here up, green (with SINR)
	rsrpBad  = -110.0 // below, red
	sinrGood = 5.0    // dB
	sinrBad  = 0.0
	rsrqWeak = -15.0 // dB: below, amber
	rssiGood = -75.0 // dBm, only without LTE metrics
	rssiBad  = -85.0
	// Wi-Fi: the same thresholds as the header's antenna (3 arcs from 67 %, 2 from 34 %)
	wifiGood = 67
	wifiBad  = 34
	// ModemManager's 0-100 quality, consistent with the RSSI thresholds above
	qualityGood = 60
	qualityBad  = 45
)

// Judge: the level of a link, without hysteresis.
func Judge(in Input) Result {
	if in.Disabled {
		return Result{Off, CodeOff}
	}
	if in.Conflict {
		return Result{Bad, CodeConflict}
	}
	if m := in.Modem; m != nil {
		if r, bad := modemFault(*m, in.Available); bad {
			return r
		}
	}
	if !in.Available {
		return Result{Bad, CodeDisconnected}
	}
	if in.NoInternet {
		return Result{Bad, CodeNoInternet}
	}
	res := Result{Good, CodeOK}
	switch {
	case in.Modem != nil:
		res = radio(in.Modem.Radio)
	case in.Kind == "wifi" && in.WiFiPercent != nil:
		res = wifi(*in.WiFiPercent)
	}
	if in.Streaming && !in.InUse && res.Level == Good {
		res = Result{Weak, CodeConnecting}
	}
	return res
}

// modemFault: what makes a modem red whatever its signal.
func modemFault(m Modem, available bool) (Result, bool) {
	switch m.SIM {
	case "missing":
		return Result{Bad, CodeSIMMissing}, true
	case "pin":
		return Result{Bad, CodeSIMPIN}, true
	case "puk":
		return Result{Bad, CodeSIMPUK}, true
	case "error":
		return Result{Bad, CodeSIMError}, true
	}
	if m.State == "failed" {
		return Result{Bad, CodeModemFailed}, true
	}
	if available {
		return Result{}, false
	}
	switch {
	case m.Registration == "denied":
		return Result{Bad, CodeRegDenied}, true
	case m.Registration == "roaming":
		// Registered abroad but no data: data roaming is off (D-M8: off by default)
		return Result{Bad, CodeRoamingOff}, true
	case m.Registration == "searching" || m.State == "searching" || m.State == "enabling":
		return Result{Bad, CodeSearching}, true
	}
	return Result{}, false
}

// radio: the colour of a modem's signal. The worst of RSRP, SINR and RSRQ when there are 4G/5G metrics;
// otherwise RSSI, bars or quality, in that order. 3G is amber at best and 2G red. Nothing reported: green
// (it is connected; the connection checks decide).
func radio(r Radio) Result {
	lvl := Good
	worse := func(l Level) {
		if l.rank() > lvl.rank() {
			lvl = l
		}
	}
	if r.RSRP != nil || r.SINR != nil {
		if v := r.RSRP; v != nil {
			worse(band(*v, rsrpGood, rsrpBad))
		}
		if v := r.SINR; v != nil {
			worse(band(*v, sinrGood, sinrBad))
		}
		if v := r.RSRQ; v != nil && *v < rsrqWeak {
			worse(Weak)
		}
	} else {
		switch {
		case r.RSSI != nil:
			worse(band(*r.RSSI, rssiGood, rssiBad))
		case r.Bars != nil:
			worse(band(float64(*r.Bars), 4, 2))
		case r.Quality != nil:
			worse(band(float64(*r.Quality), qualityGood, qualityBad))
		}
	}
	res := Result{lvl, CodeOK}
	switch lvl {
	case Weak:
		res.Code = CodeSignalWeak
	case Bad:
		res.Code = CodeSignalMinimal
	}
	switch r.Tech {
	case "2g":
		return Result{Bad, CodeSlowTech}
	case "3g":
		if lvl == Good {
			return Result{Weak, CodeSlowTech}
		}
	}
	return res
}

// band: good at or above good, bad below bad, weak in between.
func band(v, good, bad float64) Level {
	switch {
	case v >= good:
		return Good
	case v < bad:
		return Bad
	}
	return Weak
}

func wifi(pct int) Result {
	switch {
	case pct >= wifiGood:
		return Result{Good, CodeOK}
	case pct >= wifiBad:
		return Result{Weak, CodeSignalWeak}
	}
	return Result{Bad, CodeSignalMinimal}
}

// Bars: 0-4 for drawing a signal icon, from the same data (0 = nothing known or no signal).
func Bars(r Radio) int {
	switch {
	case r.RSRP != nil:
		v := *r.RSRP
		switch {
		case v >= -90:
			return 4
		case v >= rsrpGood:
			return 3
		case v >= rsrpBad:
			return 2
		case v >= -120:
			return 1
		}
		return 0
	case r.RSSI != nil:
		v := *r.RSSI
		switch {
		case v >= -65:
			return 4
		case v >= rssiGood:
			return 3
		case v >= rssiBad:
			return 2
		case v >= -100:
			return 1
		}
		return 0
	case r.Bars != nil:
		b := *r.Bars
		switch {
		case b >= 5:
			return 4
		case b <= 0:
			return 0
		}
		return b
	case r.Quality != nil:
		return min(4, max(0, (*r.Quality+12)/25))
	}
	return 0
}

// WiFiBars: 0-4 for the Wi-Fi client's signal icon, with the same thresholds as its colour: 4 green, 3 or 2
// amber, 1 red, 0 nothing.
func WiFiBars(pct int) int {
	switch {
	case pct >= wifiGood:
		return 4
	case pct >= (wifiGood+wifiBad)/2:
		return 3
	case pct >= wifiBad:
		return 2
	case pct > 0:
		return 1
	}
	return 0
}

// --- hysteresis -------------------------------------------------------------------------------------------

// Tracker: the shown result of each link, with hysteresis. A worse result is shown once it has been seen
// WorsenCount times in a row over at least WorsenAfter; a better one after ImproveAfter in a row. "Hard"
// results (off, disconnected, SIM, conflict…) are shown at once: they are facts, not a fluctuating signal.
type Tracker struct {
	WorsenAfter  time.Duration // default 1.5 s: two rounds of 2 s (~4 s after the last good sample)
	WorsenCount  int           // default 2
	ImproveAfter time.Duration // default 10 s

	mu    sync.Mutex
	links map[string]*track
}

type track struct {
	shown Result
	dir   int // +1: results worse than the shown one keep coming; -1: better ones; 0: none
	since time.Time
	seen  int
}

func (t *Tracker) params() (time.Duration, int, time.Duration) {
	wa, wc, ia := t.WorsenAfter, t.WorsenCount, t.ImproveAfter
	if wa == 0 {
		wa = 1500 * time.Millisecond
	}
	if wc == 0 {
		wc = 2
	}
	if ia == 0 {
		ia = 10 * time.Second
	}
	return wa, wc, ia
}

// hard: results shown without waiting.
func hard(r Result) bool {
	switch r.Code {
	case CodeOff, CodeDisconnected, CodeConflict, CodeSIMMissing, CodeSIMPIN, CodeSIMPUK, CodeSIMError, CodeModemFailed:
		return true
	}
	return false
}

// Update records the latest judgement of a link and returns what to show.
func (t *Tracker) Update(key string, r Result, now time.Time) Result {
	wa, wc, ia := t.params()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.links == nil {
		t.links = map[string]*track{}
	}
	tr, ok := t.links[key]
	if !ok || hard(r) || hard(tr.shown) && r != tr.shown {
		// First sight, a hard fact, or a hard fact that is over (plugged in, SIM unlocked): no waiting
		t.links[key] = &track{shown: r}
		return r
	}
	if r.Level == tr.shown.Level {
		tr.shown, tr.dir = r, 0 // same colour (perhaps another reason, which updates at once)
		return r
	}
	d := -1
	if r.Level.rank() > tr.shown.Level.rank() {
		d = 1
	}
	if tr.dir != d {
		tr.dir, tr.since, tr.seen = d, now, 0
	}
	tr.seen++
	held := now.Sub(tr.since)
	if d > 0 && tr.seen >= wc && held >= wa || d < 0 && held >= ia {
		tr.shown, tr.dir = r, 0
	}
	return tr.shown
}

// Keep forgets the links not in keys (unplugged): coming back, they start afresh.
func (t *Tracker) Keep(keys map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.links {
		if !keys[k] {
			delete(t.links, k)
		}
	}
}
