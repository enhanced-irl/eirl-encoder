// Package mixer talks to the eirl-engine mixer (MIXER=1; the Go engine or the frozen Python one): it reads
// its state and sends it commands over the local socket /run/eirl-engine/mixer.sock (MIXER.md §5). The
// board's touch screen and this app send the same commands and see the same state.
//
// The engine is another process (another user): what comes from it is read as data, with types and
// limits, and only what is in these structures is forwarded to the web.
//
// Validation errors of commands are *i18n.Error: the API renders them in the request language.
package mixer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
)

// ErrOff: no mixer running (eirl-engine stopped, restarting or without MIXER=1).
var ErrOff = errors.New("mixer: no mixer running")

// CommandError: the engine rejected the command (unknown_source, on_air, hidden_source…).
type CommandError struct{ Code string }

func (e *CommandError) Error() string { return "mixer: " + e.Code }

type Program struct {
	Main        *string  `json:"main"`
	Secondary   *string  `json:"secondary"`
	Layout      string   `json:"layout"`
	Transition  string   `json:"transition"`
	Hidden      []string `json:"hidden"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	FPS         int      `json:"fps"`
	MeasuredFPS *float64 `json:"measuredFps"`
	// PiP and Pause: absent with an older engine (the web hides their controls)
	PiP   *PiP   `json:"pip,omitempty"`
	Pause *Pause `json:"pause,omitempty"`
}

// PiP: corner of the small window and its width in % of the canvas
type PiP struct {
	Corner string `json:"corner"` // tl | tr | bl | br
	Size   int    `json:"size"`   // 25 | 33
}

// Pause: a still image with Text full screen over everything else; Mute = no audio while it lasts
type Pause struct {
	On   bool   `json:"on"`
	Text string `json:"text"`
	Mute bool   `json:"mute"`
}

type Source struct {
	ID   string `json:"id"`
	Slot int    `json:"slot"` // 1…4
	Kind string `json:"kind"` // hdmi-csi | usb | network | test
	Name string `json:"name"`
	// Alias: the user's name for the slot (slots.json); "" = none, the UIs show Name. An older engine
	// does not send it
	Alias  string `json:"alias"`
	State  string `json:"state"` // starting | live | no_signal | unsupported | gone | error | hidden
	Detail string `json:"detail"`
	// DetailCode is the stable code of Detail (the engine's internal/engine/detail, ENGINE_GO.md §13), for
	// the interfaces to translate; DetailArgs its arguments. Absent with an old engine (use Detail)
	DetailCode string            `json:"detailCode,omitempty"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
	Format     string            `json:"format"`
	Mode       string            `json:"mode"`
	FPS        float64           `json:"fps"`
}

type Screen struct {
	State  string `json:"state"` // off | waiting | on | error
	Width  *int   `json:"width"`
	Height *int   `json:"height"`
	Touch  bool   `json:"touch"`
	// Locked: the screen ignores touches except the unlock one. Absent with an older engine
	Locked *bool `json:"locked,omitempty"`
	// Power: restart or power off asked for from the screen's Power menu (the app does it: the engine has
	// no permission). Absent if not asked for
	Power *PowerRequest `json:"power,omitempty"`
}

// PowerRequest: Want = reboot | poweroff; Seq goes up with each request; At = time (Unix, s) of the request
type PowerRequest struct {
	Want string  `json:"want"`
	Seq  int     `json:"seq"`
	At   float64 `json:"at"`
}

// Output: the live output (F3). Mode = what was asked (OUTPUT); State = how it really is.
type Output struct {
	Mode   string `json:"mode"`             // on | off
	State  string `json:"state"`            // off | connecting | on | error
	Reason string `json:"reason,omitempty"` // no_destination, no_uplinks, relay_unreachable, rejected…
	Detail string `json:"detail,omitempty"` // free text from the engine, ≤ 200
	Kbps   *int   `json:"kbps,omitempty"`
	RTTMs  *int   `json:"rttMs,omitempty"`
	// By: who changed Mode last (start | socket | screen); Seq goes up with each change (0 at start).
	// Absent with an older engine
	By  string `json:"by,omitempty"`
	Seq int    `json:"seq,omitempty"`
}

// Reasons: the output reasons that are forwarded (closed list, the cloud contract's)
var Reasons = map[string]bool{"no_destination": true, "no_uplinks": true, "relay_unreachable": true,
	"rejected": true, "connection_lost": true, "encoder_error": true, "internal": true}

// Audio: the audio inputs (independent of video, F4) and which one plays. Main nil = no audio.
type Audio struct {
	Main    *string      `json:"main"`
	Sources []AudioInput `json:"sources"`
	// Mode: single (Main plays) | mix (those with InMix play together; Main is kept to go back to
	// single). Absent with an older engine (the web hides the selector and the mix controls)
	Mode string `json:"mode,omitempty"`
	// Follow and Links: absent with an older engine (the web hides their controls). With Follow,
	// putting a video source on air plays the audio linked to it in Links (video id → audio id; nil =
	// no audio of its own: the one playing keeps playing)
	Follow *bool              `json:"follow,omitempty"`
	Links  map[string]*string `json:"links,omitempty"`
	// Phones: the screen's headphones (the program audio through the board's HDMI, HEADPHONES.md).
	// Absent with an older engine (the web hides their controls)
	Phones *Phones `json:"phones,omitempty"`
}

// Phones: On = on (what was asked); State = how they really are: off | waiting (no screen, or one without
// audio: Detail says so) | on | error (retrying; Detail = the reason). GainDb: the volume,
// MinPhonesGain…MaxPhonesGain, where MinPhonesGain is silence
type Phones struct {
	On     bool   `json:"on"`
	GainDb int    `json:"gainDb"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// DetailCode is the stable code of Detail (the engine's internal/engine/detail), for the interfaces
	// to translate; DetailArgs its arguments. Absent with an old engine (use Detail).
	DetailCode string            `json:"detailCode,omitempty"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
}

// MinPhonesGain and MaxPhonesGain: the headphones volume, in dB. The minimum is silence (−∞)
const (
	MinPhonesGain = -40
	MaxPhonesGain = 0
)

type AudioInput struct {
	ID     string `json:"id"`   // i2s | alsa:<tarjeta>
	Slot   int    `json:"slot"` // 1…4
	Kind   string `json:"kind"` // i2s | usb
	Name   string `json:"name"`
	State  string `json:"state"` // starting | live | no_signal | gone | error
	Detail string `json:"detail"`
	// DetailCode and DetailArgs: as in Source. Absent with an old engine
	DetailCode string            `json:"detailCode,omitempty"`
	DetailArgs map[string]string `json:"detailArgs,omitempty"`
	PeakDb     *float64          `json:"peakDb,omitempty"` // recent peak (−90…0), with the input live
	// DelayMs: delay against the video, 0…MaxAudioDelay. Absent with an older engine
	DelayMs *int `json:"delayMs,omitempty"`
	// VideoSlot: the slot (1…4) of the video source of the same device; the engine sorts by it so they
	// show in the same row as the cameras. Absent if it belongs to none (a standalone microphone)
	VideoSlot *int `json:"videoSlot,omitempty"`
	// InMix: whether it plays in mix mode. GainDb: its gain, MinAudioGain…MaxAudioGain with one decimal
	// (0 = unchanged), in both modes. Absent with an older engine
	InMix  *bool    `json:"inMix,omitempty"`
	GainDb *float64 `json:"gainDb,omitempty"`
}

// MaxAudioDelay: the maximum delay of an audio input, in ms
const MaxAudioDelay = 500

// MinAudioGain and MaxAudioGain: the gain of an audio input, in dB
const (
	MinAudioGain = -40
	MaxAudioGain = 12
)

type State struct {
	V       int     `json:"v"`
	Program Program `json:"program"`
	Slots   int     `json:"slots"`
	// SlotConfig: every slot as the user arranged it (slots.json, SETUP.md §5); a slot reserved for a
	// pinned source that is missing appears only here. Absent with an older engine
	SlotConfig []SlotState `json:"slotConfig,omitempty"`
	// Catalog: every source connected, also those without a slot (the slot editor's candidates).
	// Absent with an older engine
	Catalog []CatalogEntry `json:"catalog,omitempty"`
	Sources []Source       `json:"sources"`
	Screen  Screen         `json:"screen"`
	Output  *Output        `json:"output,omitempty"`
	Audio   *Audio         `json:"audio,omitempty"`
}

// SlotAssign: one slot of the `slots` command and of slots.json (SETUP.md §5, ENGINE_GO.md §12).
type SlotAssign struct {
	Slot   int    `json:"slot"`   // 1…MaxSlots
	Assign string `json:"assign"` // auto | off | a source id exactly as in sources[].id
	Alias  string `json:"alias"`  // 0…AliasMax runes; "" = none
}

// SlotState: a slot in the engine state, with whether a source sits in it now.
type SlotState struct {
	SlotAssign
	Present bool `json:"present"`
}

// CatalogEntry: a connected source, with or without a slot.
type CatalogEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // hdmi-csi | usb | network | test
}

const (
	// MaxSlots: the slots the engine has at most (MIXER_SLOTS ≤ 4)
	MaxSlots = 4
	// AliasMax: runes of a slot alias
	AliasMax = 24
	// AssignAuto and AssignOff: a slot that takes any source, or none
	AssignAuto = "auto"
	AssignOff  = "off"
)

// aliasPunct: what an alias allows besides letters, digits and spaces (the engine's rule)
const aliasPunct = " -_.'&()#+/"

// ValidAlias: 0…AliasMax runes, each a Unicode letter or digit, a space or one of -_.'&()#+/ (the same
// rule as the engine). No trimming here: the app trims before saving.
func ValidAlias(a string) bool {
	if !utf8.ValidString(a) || utf8.RuneCountInString(a) > AliasMax {
		return false
	}
	for _, r := range a {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(aliasPunct, r) {
			return false
		}
	}
	return true
}

// ValidSourceID: a video source id of the catalogue (hdmi, test, a USB by-id/by-path link, net:camN).
func ValidSourceID(id string) bool { return reSource.MatchString(id) }

// ValidAssign: auto, off or a source id.
func ValidAssign(a string) bool { return a == AssignAuto || a == AssignOff || ValidSourceID(a) }

// Command: one command, the same the screen sends. Validate before sending it.
type Command struct {
	Cmd        string   `json:"cmd"`
	Source     string   `json:"source,omitempty"`
	Layout     string   `json:"layout,omitempty"`
	Transition string   `json:"transition,omitempty"`
	Hidden     *bool    `json:"hidden,omitempty"`
	On         *bool    `json:"on,omitempty"` // output: start or stop streaming; pause and lock: turn on or off
	Corner     string   `json:"corner,omitempty"`
	Size       *int     `json:"size,omitempty"`
	Text       *string  `json:"text,omitempty"`
	Mute       *bool    `json:"mute,omitempty"`
	Video      string   `json:"video,omitempty"` // audio_link: the video source; source, the audio that goes with it
	Ms         *int     `json:"ms,omitempty"`    // audio_delay
	Mode       string   `json:"mode,omitempty"`  // audio_mode: single | mix
	Gain       *float64 `json:"gain,omitempty"`  // audio_mix and phones, in dB; on = in or out of the mix, or headphones on
	// Slots: the slots command (every slot, as in slots.json). The API sends it after saving the file
	Slots []SlotAssign `json:"slots,omitempty"`
}

// Ids of the source catalogue (SOURCES.md): hdmi, the bars, the by-id/by-path links of USB devices and
// the network cameras (net:cam1…net:cam8, NETWORK_SOURCES.md §6.5)
var reSource = regexp.MustCompile(`^(hdmi|test|net:cam[1-8]|/dev/v4l/by-(id|path)/[A-Za-z0-9_:.-]{1,200})$`)

// Ids of the audio inputs (source catalogue: i2s, alsa:<card> and the audio of each network camera,
// net:camN); none = no audio
var reAudio = regexp.MustCompile(`^(none|i2s|alsa:[A-Za-z0-9_-]{1,32}|net:cam[1-8])$`)

// fields: the fields each command allows besides cmd. One from another command is rejected here and
// never reaches the engine.
var fields = map[string][]string{
	"take": {"source"}, "secondary": {"source"}, "swap": nil, "hide": {"source", "hidden"}, "audio": {"source"},
	"output": {"on"}, "layout": {"layout"}, "transition": {"transition"},
	"pip": {"corner", "size"}, "pause": {"on", "text", "mute"}, "lock": {"on"},
	"audio_follow": {"on"}, "audio_delay": {"source", "ms"}, "audio_link": {"video", "source"},
	"audio_mode": {"mode"}, "audio_mix": {"source", "on", "gain"},
	"preview": {"on"}, // the WebRTC preview: the app stops it while nobody watches (FollowPreview)
	"phones":  {"on", "gain"},
	"slots":   {"slots"}, // the source slots (PUT /sources/slots writes slots.json and then sends it)
}

// present: the fields the command carries
func (c Command) present() []string {
	var out []string
	for name, set := range map[string]bool{"source": c.Source != "", "layout": c.Layout != "",
		"transition": c.Transition != "", "hidden": c.Hidden != nil, "on": c.On != nil, "corner": c.Corner != "",
		"size": c.Size != nil, "text": c.Text != nil, "mute": c.Mute != nil, "video": c.Video != "", "ms": c.Ms != nil,
		"mode": c.Mode != "", "gain": c.Gain != nil, "slots": c.Slots != nil} {
		if set {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Validate checks the command before it is sent. Errors are *i18n.Error.
func (c Command) Validate() error {
	allowed, ok := fields[c.Cmd]
	if !ok {
		return i18n.E("mixer.cmd_unknown")
	}
	for _, f := range c.present() {
		if !slices.Contains(allowed, f) {
			return i18n.E("mixer.cmd_field", f, c.Cmd)
		}
	}
	needSource := false
	switch c.Cmd {
	case "take", "secondary":
		needSource = true
	case "hide":
		needSource = true
		if c.Hidden == nil {
			return i18n.E("mixer.cmd_missing", "hidden")
		}
	case "audio":
		if !reAudio.MatchString(c.Source) {
			return i18n.E("mixer.bad_audio_input")
		}
	case "audio_delay":
		if c.Source == "none" || !reAudio.MatchString(c.Source) {
			return i18n.E("mixer.bad_audio_input")
		}
		if c.Ms == nil {
			return i18n.E("mixer.cmd_missing", "ms")
		}
		if *c.Ms < 0 || *c.Ms > MaxAudioDelay {
			return i18n.E("mixer.bad_delay", MaxAudioDelay)
		}
	case "audio_mode":
		if c.Mode != "single" && c.Mode != "mix" {
			return i18n.E("mixer.bad_audio_mode")
		}
	case "audio_mix":
		if c.Source == "none" || !reAudio.MatchString(c.Source) {
			return i18n.E("mixer.bad_audio_input")
		}
		if c.On == nil && c.Gain == nil {
			return i18n.E("mixer.cmd_missing_or", "on", "gain")
		}
		if c.Gain != nil && (*c.Gain < MinAudioGain || *c.Gain > MaxAudioGain) {
			return i18n.E("mixer.bad_gain", MinAudioGain, MaxAudioGain)
		}
	case "audio_link":
		if !reSource.MatchString(c.Video) {
			return i18n.E("mixer.bad_video_source")
		}
		if !reAudio.MatchString(c.Source) {
			return i18n.E("mixer.bad_audio_input")
		}
	case "phones":
		if c.On == nil && c.Gain == nil {
			return i18n.E("mixer.cmd_missing_or", "on", "gain")
		}
		if c.Gain != nil && (*c.Gain < MinPhonesGain || *c.Gain > MaxPhonesGain || *c.Gain != math.Trunc(*c.Gain)) {
			return i18n.E("mixer.bad_phones_gain", MinPhonesGain, MaxPhonesGain)
		}
	case "output", "lock", "audio_follow", "preview":
		if c.On == nil {
			return i18n.E("mixer.cmd_missing", "on")
		}
	case "layout":
		if c.Layout != "full" && c.Layout != "pip" && c.Layout != "side" {
			return i18n.E("mixer.bad_layout_value")
		}
	case "transition":
		if c.Transition != "cut" && c.Transition != "fade" {
			return i18n.E("mixer.bad_transition_value")
		}
	case "pip":
		if c.Corner == "" && c.Size == nil {
			return i18n.E("mixer.cmd_missing_or", "corner", "size")
		}
		if c.Corner != "" && !Corners[c.Corner] {
			return i18n.E("mixer.bad_corner")
		}
		if c.Size != nil && !Sizes[*c.Size] {
			return i18n.E("mixer.bad_size")
		}
	case "pause":
		if c.On == nil && c.Text == nil && c.Mute == nil {
			return i18n.E("mixer.cmd_missing_pause")
		}
		if c.Text != nil {
			if err := CheckPauseText(*c.Text); err != nil {
				return err
			}
		}
	case "slots":
		if !validSlots(c.Slots) {
			return i18n.E("mixer.bad_slots")
		}
	}
	if needSource && !reSource.MatchString(c.Source) {
		return i18n.E("mixer.bad_source")
	}
	return nil
}

// validSlots: the engine's rule for a slots list: 1…MaxSlots entries, each slot once and within
// 1…MaxSlots, a valid assign and alias, and no source pinned twice.
func validSlots(list []SlotAssign) bool {
	if len(list) == 0 || len(list) > MaxSlots {
		return false
	}
	slots, ids := map[int]bool{}, map[string]bool{}
	for _, s := range list {
		if s.Slot < 1 || s.Slot > MaxSlots || slots[s.Slot] || !ValidAssign(s.Assign) || !ValidAlias(s.Alias) {
			return false
		}
		slots[s.Slot] = true
		if s.Assign != AssignAuto && s.Assign != AssignOff {
			if ids[s.Assign] {
				return false
			}
			ids[s.Assign] = true
		}
	}
	return true
}

// Corners and Sizes: the corners and widths (in % of the canvas) of the PiP
var (
	Corners = map[string]bool{"tl": true, "tr": true, "bl": true, "br": true}
	Sizes   = map[int]bool{25: true, 33: true}
)

// MaxPauseText: runes of the pause text (without the spaces at the ends)
const MaxPauseText = 60

// pausePunct: what the pause text allows besides letters, digits and spaces. Closed list, the same as
// the engine's: no <, >, &, line breaks or emoji.
const pausePunct = ".,:;!?¡¿'\"()-–—…·/+#@%€$*"

// CheckPauseText: the pause text rule (the same in the engine and the web): trimmed of spaces, 1 to 60
// runes remain, each a letter or digit, a space or one of pausePunct. Errors are *i18n.Error.
func CheckPauseText(text string) error {
	t := strings.TrimSpace(text)
	n := 0
	for _, r := range t {
		n++
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || strings.ContainsRune(pausePunct, r) {
			continue
		}
		if !unicode.IsGraphic(r) {
			return i18n.E("mixer.pause_control")
		}
		return i18n.E("mixer.pause_char", r, pausePunct)
	}
	switch {
	case n == 0:
		return i18n.E("mixer.pause_empty")
	case n > MaxPauseText:
		return i18n.E("mixer.pause_long", MaxPauseText)
	}
	return nil
}

// Client of the engine. The engine leaves Sock and StatePath in the eirl-engine tmpfs (RuntimeDirectory):
// they disappear when the stream stops, so a stale state is never read.
type Client struct {
	Sock      string // /run/eirl-engine/mixer.sock
	StatePath string // /run/eirl-engine/mixer.json (copy of the state on each change)
	RunDir    string // thumbnails: source-<slot>.jpg and screen.jpg
	Timeout   time.Duration
}

const maxState = 256 << 10

// Read: the state from mixer.json (without talking to the engine: this is what the 2 s status uses).
func (c *Client) Read() (*State, error) {
	f, err := os.Open(c.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrOff
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var st State
	if err := json.NewDecoder(io.LimitReader(f, maxState)).Decode(&st); err != nil {
		return nil, fmt.Errorf("mixer: unreadable state: %w", err)
	}
	return clean(&st), nil
}

// clean bounds what comes from the engine before forwarding it
func clean(st *State) *State {
	if len(st.Sources) > 8 {
		st.Sources = st.Sources[:8]
	}
	if len(st.Program.Hidden) > 8 {
		st.Program.Hidden = st.Program.Hidden[:8]
	}
	if st.Sources == nil {
		st.Sources = []Source{}
	}
	if st.Program.Hidden == nil {
		st.Program.Hidden = []string{}
	}
	for i := range st.Sources {
		if !ValidAlias(st.Sources[i].Alias) {
			st.Sources[i].Alias = ""
		}
		cleanDetail(&st.Sources[i].DetailCode, &st.Sources[i].DetailArgs)
	}
	st.SlotConfig = cleanSlotConfig(st.SlotConfig)
	st.Catalog = cleanCatalog(st.Catalog)
	if p := st.Program.PiP; p != nil && (!Corners[p.Corner] || !Sizes[p.Size]) {
		st.Program.PiP = nil // the web does not show controls it could not mark
	}
	if p := st.Program.Pause; p != nil {
		if r := []rune(p.Text); len(r) > MaxPauseText {
			p.Text = string(r[:MaxPauseText])
		}
	}
	if o := st.Output; o != nil {
		if o.Mode != "on" {
			o.Mode = "off"
		}
		switch o.State {
		case "off", "connecting", "on", "error":
		default:
			o.State = "off"
		}
		if !Reasons[o.Reason] {
			o.Reason = ""
		}
		// The libsrt line reaches the web and the cloud: a second barrier for keys and passphrases (the
		// engine already hides streamid= and passphrase=, but not publish:/read: or other casings)
		o.Detail = service.Redact(o.Detail)
		if r := []rune(o.Detail); len(r) > 200 {
			o.Detail = string(r[:200])
		}
		switch o.By {
		case "start", "socket", "screen":
		default:
			o.By = ""
		}
		o.Seq = max(o.Seq, 0)
	}
	if p := st.Screen.Power; p != nil && (p.Want != "reboot" && p.Want != "poweroff" || p.Seq < 1) {
		st.Screen.Power = nil
	}
	if a := st.Audio; a != nil {
		if len(a.Sources) > 8 {
			a.Sources = a.Sources[:8]
		}
		if a.Sources == nil {
			a.Sources = []AudioInput{}
		}
		if a.Mode != "single" && a.Mode != "mix" {
			a.Mode = "" // the web does not show a selector it could not mark
		}
		for i := range a.Sources {
			in := &a.Sources[i]
			if in.DelayMs != nil {
				*in.DelayMs = min(max(*in.DelayMs, 0), MaxAudioDelay)
			}
			if in.VideoSlot != nil && (*in.VideoSlot < 1 || *in.VideoSlot > 8) {
				in.VideoSlot = nil
			}
			if in.GainDb != nil {
				*in.GainDb = math.Round(min(max(*in.GainDb, MinAudioGain), MaxAudioGain)*10) / 10
			}
			cleanDetail(&in.DetailCode, &in.DetailArgs)
		}
		a.Links = cleanLinks(a.Links, st.Sources)
		if p := a.Phones; p != nil {
			switch p.State {
			case "off", "waiting", "on", "error":
			default:
				p.State = "off"
			}
			p.GainDb = min(max(p.GainDb, MinPhonesGain), MaxPhonesGain)
			if r := []rune(p.Detail); len(r) > 120 {
				p.Detail = string(r[:120])
			}
			cleanDetail(&p.DetailCode, &p.DetailArgs)
		}
	}
	return st
}

var (
	detailCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	detailArgRe  = regexp.MustCompile(`^[a-z][A-Za-z0-9_]{0,15}$`)
)

// cleanDetail bounds a detail code and its arguments (ENGINE_GO.md §13) before they reach the web and the screen:
// a code of the closed list's shape (the UIs translate it, or fall back to Detail), at most 8 arguments with plain
// names, each value one line of ≤ 200 runes, through the same redactor as the output's detail (an argument can
// carry a tool's error text).
func cleanDetail(code *string, args *map[string]string) {
	if !detailCodeRe.MatchString(*code) {
		*code = ""
	}
	if *code == "" || len(*args) == 0 {
		*args = nil
		return
	}
	out := make(map[string]string, len(*args))
	for k, v := range *args {
		if len(out) == 8 || !detailArgRe.MatchString(k) {
			continue
		}
		v = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, service.Redact(v))
		if r := []rune(v); len(r) > 200 {
			v = string(r[:200])
		}
		out[k] = v
	}
	*args = out
}

// cleanSlotConfig: at most 8 slots, each once, within 1…8, with a valid assign; a bad alias becomes "".
func cleanSlotConfig(in []SlotState) []SlotState {
	if in == nil {
		return nil
	}
	out := []SlotState{}
	seen := map[int]bool{}
	for _, s := range in {
		if len(out) == 8 {
			break
		}
		if s.Slot < 1 || s.Slot > 8 || seen[s.Slot] || !ValidAssign(s.Assign) {
			continue
		}
		seen[s.Slot] = true
		if !ValidAlias(s.Alias) {
			s.Alias = ""
		}
		out = append(out, s)
	}
	return out
}

// catalogKinds: the source kinds the catalogue may carry
var catalogKinds = map[string]bool{"hdmi-csi": true, "usb": true, "network": true, "test": true}

// cleanCatalog: at most 32 entries with valid, unique ids; names without control characters and at
// most 80 runes; an unknown kind becomes "".
func cleanCatalog(in []CatalogEntry) []CatalogEntry {
	if in == nil {
		return nil
	}
	out := []CatalogEntry{}
	seen := map[string]bool{}
	for _, c := range in {
		if len(out) == 32 {
			break
		}
		if !ValidSourceID(c.ID) || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		c.Name = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, c.Name)
		if r := []rune(c.Name); len(r) > 80 {
			c.Name = string(r[:80])
		}
		if !catalogKinds[c.Kind] {
			c.Kind = ""
		}
		out = append(out, c)
	}
	return out
}

// cleanLinks: only the links of video sources with a slot (those in sources, already bounded to 8: one
// each), with valid video and audio ids; an invalid one is dropped. none is the same as nil: no audio of
// its own.
func cleanLinks(in map[string]*string, sources []Source) map[string]*string {
	if in == nil {
		return nil
	}
	out := map[string]*string{}
	for _, s := range sources {
		a, ok := in[s.ID]
		if !ok || !reSource.MatchString(s.ID) || a != nil && !reAudio.MatchString(*a) {
			continue
		}
		if a != nil && *a == "none" {
			a = nil
		}
		out[s.ID] = a
	}
	return out
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Sock)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrOff
		}
		return nil, err
	}
	return conn, nil
}

type reply struct {
	ID    *int            `json:"id"`
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Event string          `json:"event"`
	State json.RawMessage `json:"state"`
}

// Do sends a command and waits for its answer (by id; the engine sends no events without a subscription).
func (c *Client) Do(ctx context.Context, cmd Command) error {
	if err := cmd.Validate(); err != nil {
		return err
	}
	if cmd.Text != nil { // the text rule trims it: the engine gets it clean
		t := strings.TrimSpace(*cmd.Text)
		cmd.Text = &t
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	req := struct {
		ID int `json:"id"`
		Command
	}{1, cmd}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), maxState)
	for sc.Scan() {
		var r reply
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == nil || *r.ID != 1 {
			continue
		}
		if !r.OK {
			code := r.Error
			if code == "" || len(code) > 32 {
				code = "rejected"
			}
			return &CommandError{Code: code}
		}
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("mixer: the engine closed without answering")
}

// Watch subscribes to the state and calls fn with the current one and on each change, until the engine
// closes (eirl-engine stops or restarts) or ctx is cancelled.
func (c *Client) Watch(ctx context.Context, fn func(*State)) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := conn.Write([]byte(`{"id":1,"cmd":"subscribe"}` + "\n")); err != nil {
		return err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), maxState)
	for sc.Scan() {
		var r reply
		if json.Unmarshal(sc.Bytes(), &r) != nil || len(r.State) == 0 {
			continue
		}
		var st State
		if json.Unmarshal(r.State, &st) != nil {
			continue
		}
		fn(clean(&st))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return sc.Err()
}

// Image: path of an engine image for the web, only from the allowed list (screen or source-1…4). ""
// if the name is not valid.
func (c *Client) Image(name string) string {
	switch name {
	case "screen.jpg", "source-1.jpg", "source-2.jpg", "source-3.jpg", "source-4.jpg":
		return filepath.Join(c.RunDir, name)
	}
	return ""
}
