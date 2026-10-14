package ui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Times and limits of the touch logic (engine_mixer.py).
const (
	TapLong     = 600 * time.Millisecond // long press on a tile = to the PiP; on erase = all
	Hold        = time.Second            // held: Emit / Stop, Unlock
	HoldPower   = 2 * time.Second        // held: Reboot / Power off in the Power menu
	PowerWait   = 20 * time.Second       // «Reiniciando…» before giving the request up
	SecretGrace = 2 * time.Second        // after closing Settings, screen.jpg stays frozen
	PressMin    = 150 * time.Millisecond // a press highlight is seen at least this long
	ConfirmFor  = 3 * time.Second        // the second tap of what is confirmed by tapping again
	// ConfirmUpdate: the same for an update or running the setup again (a longer text to read)
	ConfirmUpdate = 5 * time.Second
	PressStale    = 30 * time.Second      // a press without "up" is dropped after this
	MeterEvery    = 50 * time.Millisecond // VU meters: 20 images a second
	MeterDecay    = 24.0                  // dB/s the bar falls (it rises at once)
	MeterHold     = 1.5                   // s the peak mark stays
	PSKMin        = 8
	PSKMax        = 64
)

// Audio and headphones gains (engine_mixer.py, engine_phones.py).
const (
	GainMin, GainMax, GainStep = -40.0, 12.0, 2.0
	PhonesStep                 = 2
	PhonesGainMin              = -40 // silence
	PhonesGainMax              = 0
	PhonesDefaultGain          = -12
)

// SSIDMax is the longest name of «Crear mi wifi» (the AP's SSID).
const SSIDMax = 32

// reSSID is the app's rule for the AP's name (netcfg.ValidSSID; a test checks they agree): 1-32 letters without
// accents, digits, space, dot, hyphen and underscore, with no space at either end.
var reSSID = regexp.MustCompile(`^[A-Za-z0-9_.-]([A-Za-z0-9 _.-]{0,30}[A-Za-z0-9_.-])?$`)

// SSIDOK: a valid name for «Crear mi wifi» (the screen checks it before sending; the app checks it again).
func SSIDOK(name string) bool { return reSSID.MatchString(name) }

// SSIDEditable: «Crear mi wifi» can be renamed from here: the app answers with the AP's policy (an app with the
// board's Wi-Fi) and its name is known (the Wi-Fi to join, read with Settings open; the app writes it at its first
// start, so it is there with the AP off too).
func SSIDEditable(n *NetSnapshot) bool {
	return APSwitch(n) && n.Wifi != nil && n.Wifi.SSID != ""
}

// PSKOK is psk_ok: 8-63 characters, or exactly 64 hexadecimal ones.
func PSKOK(chars []byte) bool {
	n := len(chars)
	if PSKMin <= n && n <= 63 {
		return true
	}
	if n != 64 {
		return false
	}
	for _, c := range chars {
		if !strings.ContainsRune("0123456789abcdefABCDEF", rune(c)) {
			return false
		}
	}
	return true
}

// CornerNext goes clockwise; CornerArrow is the PiP button's arrow.
var (
	CornerNext  = map[string]string{"tl": "tr", "tr": "br", "br": "bl", "bl": "tl"}
	CornerArrow = map[string]string{"tl": "↖", "tr": "↗", "bl": "↙", "br": "↘"}
)

// TouchCommand is touch_command: the command of a tap, the same as the web's. ids: the source id of each
// slot ("" = empty). The PiP is handled from its button: with the PiP on, a tap moves its corner and a long
// press changes its size. Emit and Unlock are not taps: they are held (HoldKey).
func TouchCommand(h Hit, long bool, st *Status, ids []string) map[string]any {
	switch h.Kind {
	case HitTile:
		if h.Slot >= len(ids) || ids[h.Slot] == "" {
			return nil
		}
		if long {
			return map[string]any{"cmd": "secondary", "source": ids[h.Slot]}
		}
		return map[string]any{"cmd": "take", "source": ids[h.Slot]}
	case HitProgram:
		return map[string]any{"cmd": "swap"}
	case HitButton:
		p := st.Program
		switch h.Key {
		case "cut", "fade":
			return map[string]any{"cmd": "transition", "transition": h.Key}
		case "pause":
			return map[string]any{"cmd": "pause", "on": !st.pauseOn()}
		case "lock":
			return map[string]any{"cmd": "lock", "on": true}
		case "pip":
			if p.Layout == "pip" {
				size, corner := 33, "br"
				if p.PiP != nil {
					size, corner = p.PiP.Size, p.PiP.Corner
				}
				if long {
					if size == 33 {
						return map[string]any{"cmd": "pip", "size": 25}
					}
					return map[string]any{"cmd": "pip", "size": 33}
				}
				return map[string]any{"cmd": "pip", "corner": CornerNext[corner]}
			}
			return map[string]any{"cmd": "layout", "layout": "pip"}
		case "full", "side":
			return map[string]any{"cmd": "layout", "layout": h.Key}
		}
	}
	return nil
}

// PhonesText is phones_text: the label under the headphones button and the warning inside it ("" = none).
func PhonesText(ph *PhonesStatus, tx *Strings) (label, warn string) {
	if !ph.On {
		return tx.PhonesOff, ""
	}
	g := 0
	if ph.GainDb != nil {
		g = *ph.GainDb
	}
	vol := tx.PhonesMute
	if g > PhonesGainMin {
		vol = fmt.Sprintf(tx.PhonesDbF, g)
	}
	switch ph.State {
	case "error", "waiting":
		if t, ok := tx.Details[ph.DetailCode]; ok { // why, in the screen's language (ENGINE_GO.md §13)
			return vol, t
		}
		switch {
		case ph.State == "error":
			return vol, tx.PhonesRetrying
		case ph.Detail == phonesNoAudioDetail: // an engine from before the codes
			return vol, tx.PhonesNoAudio
		}
		return vol, tx.PhonesWaiting
	}
	return vol, ""
}

// FaderGain is the dB of a touch on fader r (−40 at the bottom to +12 at the top), in 1 dB steps.
func FaderGain(r Rect, y int) float64 {
	f := 1.0 - clamp01(float64(y-r.Y)/float64(max(1, r.H)))
	return math.RoundToEven(GainMin + f*(GainMax-GainMin)) // Python's round()
}

// FaderY is where a gain sits on fader r.
func FaderY(r Rect, db float64) float64 {
	return float64(r.Y) + float64(r.H)*(1.0-(max(GainMin, min(GainMax, db))-GainMin)/(GainMax-GainMin))
}

var reAudioKey = regexp.MustCompile(`^a_(on|fader|minus|plus)_([0-3])$`)

func round1(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 1, 64), 64)
	return f
}

// AudioCommand is audio_command: the command of a tap in the audio view (the web's), nil if it does nothing
// (an empty strip).
func AudioCommand(what string, y int, au *AudioStatus, lay *Layout) map[string]any {
	if au == nil {
		au = &AudioStatus{}
	}
	switch what {
	case "a_single":
		return map[string]any{"cmd": "audio_mode", "mode": "single"}
	case "a_mix":
		return map[string]any{"cmd": "audio_mode", "mode": "mix"}
	case "a_follow":
		return map[string]any{"cmd": "audio_follow", "on": !au.Follow}
	case "a_phones", "a_ph_minus", "a_ph_plus":
		ph := au.Phones
		if ph == nil {
			return nil // an engine without headphones: the strip does not show them
		}
		if what == "a_phones" {
			return map[string]any{"cmd": "phones", "on": !ph.On}
		}
		g := PhonesDefaultGain
		if ph.GainDb != nil {
			g = *ph.GainDb
		}
		if what == "a_ph_plus" {
			g += PhonesStep
		} else {
			g -= PhonesStep
		}
		return map[string]any{"cmd": "phones", "gain": max(PhonesGainMin, min(PhonesGainMax, g))}
	}
	m := reAudioKey.FindStringSubmatch(what)
	if m == nil {
		return nil
	}
	i, _ := strconv.Atoi(m[2])
	if i >= len(au.Sources) {
		return nil
	}
	a := au.Sources[i]
	switch m[1] {
	case "on":
		if au.Mode == "mix" {
			return map[string]any{"cmd": "audio_mix", "source": a.ID, "on": !a.InMix}
		}
		return map[string]any{"cmd": "audio", "source": a.ID}
	case "fader":
		return map[string]any{"cmd": "audio_mix", "source": a.ID, "gain": FaderGain(lay.Buttons[what], y)}
	}
	g := 0.0
	if a.GainDb != nil {
		g = *a.GainDb
	}
	if m[1] == "plus" {
		g += GainStep
	} else {
		g -= GainStep
	}
	return map[string]any{"cmd": "audio_mix", "source": a.ID, "gain": max(GainMin, min(GainMax, round1(g)))}
}

// HoldKey is the button to hold under the finger, or "": Emit/Stop (not by a brush), Unlock when locked
// (the only thing it answers) and, in the Power menu, Reboot and Power off.
func HoldKey(h Hit, locked, menu bool) string {
	if h.Kind != HitButton {
		return ""
	}
	if menu && !locked {
		switch h.Key {
		case "m_reboot":
			return "reboot"
		case "m_poweroff":
			return "poweroff"
		}
		return ""
	}
	if locked {
		if h.Key == "lock" {
			return "unlock"
		}
		return ""
	}
	if h.Key == "emit" {
		return "emit"
	}
	return ""
}

// HoldTime is how long a held key takes.
func HoldTime(key string) time.Duration {
	if key == "reboot" || key == "poweroff" {
		return HoldPower
	}
	return Hold
}

// ModeLabel is mode_label: '1920x1080@30' → '1080p30'; a portrait phone ('1080x1920') → 'vertical 1080p30'.
func ModeLabel(mode string, fps float64, tx *Strings) string {
	wh := strings.Split(strings.SplitN(mode, "@", 2)[0], "x")
	tail := ""
	if fps != 0 {
		tail = strconv.FormatFloat(fps, 'f', 0, 64)
	}
	if len(wh) == 2 && isDigits(wh[0]) && isDigits(wh[1]) {
		w, _ := strconv.Atoi(wh[0])
		h, _ := strconv.Atoi(wh[1])
		if w < h {
			return fmt.Sprintf(tx.ModeVerticalF, wh[0], tail)
		}
	}
	return wh[len(wh)-1] + "p" + tail
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// MeterFrac is dB (−60…0) to the meter's fraction (0…1); ok false (no level) is 0.
func MeterFrac(db float64, ok bool) float64 {
	if !ok {
		return 0
	}
	return max(0, min(1, (db+60)/60))
}

// MeterState is a meter's ballistics: what it shows, the peak mark, and when each was last updated (s).
type MeterState struct {
	Shown, T, Peak, TPeak float64
	Set                   bool
}

// MeterStep is meter_step: up at once, down MeterDecay dB/s, the peak stays MeterHold s. ok false (no level)
// is silence.
func MeterStep(prev MeterState, raw float64, ok bool, now float64) MeterState {
	if !ok {
		raw = -90
	}
	if !prev.Set {
		return MeterState{raw, now, raw, now, true}
	}
	shown := max(raw, prev.Shown-MeterDecay*max(0, now-prev.T))
	peak, tp := prev.Peak, prev.TPeak
	if raw >= peak || now-tp > MeterHold {
		peak, tp = max(raw, shown), now
	}
	return MeterState{shown, now, peak, tp, true}
}

// SignalLevel is the antenna's arcs (1-3) for NetworkManager's signal (0-100 %).
func SignalLevel(signal int) int {
	switch {
	case signal >= 67:
		return 3
	case signal >= 34:
		return 2
	}
	return 1
}

// --- the app's network state: which switches exist and what their rows say ---

// NetSwitch: the reception switch exists (the app answers and some network camera is registered).
func NetSwitch(n *NetSnapshot) bool { return n.Status != nil && n.Status.Cameras > 0 }

// APSwitch: «Crear mi wifi» has a switch (the app answers and has accessPoint.policy, WIFI.md §5).
func APSwitch(n *NetSnapshot) bool {
	return n.Status != nil && (n.Status.AccessPoint.Policy == "always" || n.Status.AccessPoint.Policy == "rescue")
}

// ClientSwitch: there is a «Conectarse a una wifi» row (the app has the client wifi).
func ClientSwitch(n *NetSnapshot) bool { return n.Status != nil && n.Client != nil }

// WifiDown: the board's wifi asked for and down, the gear's amber dot. With «Crear mi wifi», when it is on
// always and does not come up; with an older app, the cameras' one; without the API, wifi.json.
func WifiDown(n *NetSnapshot, legacy *WifiCreds) bool {
	if st := n.Status; st != nil {
		ap := st.AccessPoint
		if ap.Policy == "always" || ap.Policy == "rescue" {
			return ap.Policy == "always" && (ap.State == "off" || ap.State == "error")
		}
		return st.Cameras > 0 && ap.Enabled != nil && *ap.Enabled && (ap.State == "off" || ap.State == "error")
	}
	if n.API == No {
		return legacy != nil && !legacy.On
	}
	return false
}

// Antenna is the header's antenna for the client wifi: arcs 1-3 (0 = none) and its tone (ink, quiet, warn);
// ok false = none (no client, off, or the app does not answer).
func Antenna(n *NetSnapshot) (level int, tone string, ok bool) {
	cl := n.Client
	if n.Status == nil || cl == nil || !cl.Enabled || cl.State == "" || cl.State == "off" {
		return 0, "", false
	}
	if cl.State == "connected" {
		sig := 0
		if cl.Signal != nil {
			sig = *cl.Signal
		}
		if cl.Internet != nil && !*cl.Internet {
			return SignalLevel(sig), "warn", true
		}
		return SignalLevel(sig), "ink", true
	}
	if cl.State == "error" {
		return 0, "warn", true
	}
	return 0, "quiet", true
}

// Knob is a row's switch: none, off or on.
type Knob int

const (
	KnobNone Knob = iota
	KnobOff
	KnobOn
)

func knob(on bool) Knob {
	if on {
		return KnobOn
	}
	return KnobOff
}

// RowState is what a Settings row shows.
type RowState struct {
	Knob Knob
	Busy bool
	Sub  string
	Tone string // good | warn | quiet
}

// APRow is ap_row: the «Crear mi wifi» row.
func APRow(n *NetSnapshot, legacy *WifiCreds, tx *Strings) RowState {
	st, pend := n.Status, n.Pending
	if st == nil { // an older app (wifi.json): no switch
		w := legacy
		if n.API != No {
			w = nil
		}
		if w == nil {
			return RowState{KnobNone, false, tx.Off, "quiet"}
		}
		if w.On {
			return RowState{KnobNone, false, fmt.Sprintf(tx.LegacyOnF, w.SSID), "good"}
		}
		return RowState{KnobNone, false, tx.LegacyOff, "warn"}
	}
	ap := st.AccessPoint
	policy, state := ap.Policy, ap.State
	busy := pend != nil && pend.What == "ap"
	var on bool
	switch {
	case busy:
		on = pend.On
	case policy != "":
		on = policy == "always"
	default:
		on = state == "on" || state == "starting"
	}
	if busy {
		sub := tx.TurningOff
		if on {
			sub = tx.TurningOn
		}
		return RowState{knob(on), true, sub, "quiet"}
	}
	withPolicy := func(v bool) Knob {
		if policy != "" {
			return knob(v)
		}
		return KnobNone
	}
	if policy == "rescue" || (policy == "" && (state == "" || state == "off")) {
		return RowState{withPolicy(false), false, tx.Off, "quiet"}
	}
	switch state {
	case "on":
		who := tx.NobodyConnected
		if ap.Clients != nil && *ap.Clients == 1 {
			who = tx.OneDevice
		} else if ap.Clients != nil && *ap.Clients > 1 {
			who = fmt.Sprintf(tx.DevicesF, *ap.Clients)
		}
		parts := []string{ap.SSID}
		if ap.Channel != nil {
			parts = append(parts, fmt.Sprintf(tx.ChannelF, *ap.Channel))
		}
		parts = append(parts, who)
		return RowState{withPolicy(true), false, joinNonEmpty(parts, tx.Sep), "good"}
	case "starting":
		return RowState{withPolicy(true), false, tx.TurningOn, "quiet"}
	}
	sub := tx.APFailed
	if ap.Error != "" {
		sub += fmt.Sprintf(tx.ErrSuffixF, ap.Error)
	}
	return RowState{withPolicy(true), false, sub, "warn"}
}

func joinNonEmpty(parts []string, sep string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// ClientRow is client_row: the «Conectarse a una wifi» row (n.Client must be set).
func ClientRow(n *NetSnapshot, tx *Strings) RowState {
	cl, pend := n.Client, n.Pending
	busy := pend != nil && pend.What == "client"
	on := cl.Enabled
	if busy {
		on = pend.On
		sub := tx.TurningOff
		if on {
			sub = tx.TurningOn
		}
		return RowState{knob(on), true, sub, "quiet"}
	}
	state, ssid := cl.State, cl.SSID
	if !on || state == "off" {
		return RowState{KnobOff, false, tx.Off, "quiet"}
	}
	switch state {
	case "connecting":
		return RowState{KnobOn, false, fmt.Sprintf(tx.ConnectingToF, ssid), "quiet"}
	case "connected":
		parts := []string{ssid}
		if cl.Signal != nil {
			sig := *cl.Signal
			q := tx.SignalWeak
			if sig >= 67 {
				q = tx.SignalGood
			} else if sig >= 34 {
				q = tx.SignalMid
			}
			parts = append(parts, fmt.Sprintf(tx.SignalF, q))
		}
		if cl.Band == "2.4" || cl.Band == "5" {
			parts = append(parts, fmt.Sprintf(tx.GHzF, strings.ReplaceAll(cl.Band, ".", tx.Decimal)))
		}
		if cl.Internet != nil && !*cl.Internet {
			return RowState{KnobOn, false, strings.Join(append(parts, tx.NoInternet), tx.Sep), "warn"}
		}
		return RowState{KnobOn, false, strings.Join(parts, tx.Sep), "good"}
	case "error":
		if cl.Error == "wrong_password" {
			return RowState{KnobOn, false, fmt.Sprintf(tx.WrongPasswordForF, ssid), "warn"}
		}
		sub := tx.ConnectFailed
		if ssid != "" {
			sub += fmt.Sprintf(tx.ConnectFailedToSuffixF, ssid)
		}
		return RowState{KnobOn, false, sub, "warn"}
	}
	return RowState{KnobOn, false, tx.NotConnected, "quiet"}
}
