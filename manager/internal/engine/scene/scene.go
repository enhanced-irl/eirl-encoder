// Package scene is what goes out on the program: which source is main and which is secondary, the
// layout, the transition, hidden sources, the PiP and the Pause. It also holds the geometry that
// turns a scene into compositor rectangles, the socket commands that change it and its persistence
// in mixer-scene.json. No GStreamer here, so all of it is tested on any machine.
//
// Port of the scene part of scripts/engine_mixer.py (Scene, apply_command, scene_rects, fade_plan…).
// The Python engine is the reference: same rules, same numbers, same error codes.
package scene

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Layouts and transitions, in the order the Python engine lists them.
const (
	LayoutFull = "full"
	LayoutPip  = "pip"
	LayoutSide = "side"

	TransitionCut  = "cut"
	TransitionFade = "fade"
)

var (
	Layouts     = []string{LayoutFull, LayoutPip, LayoutSide}
	Transitions = []string{TransitionCut, TransitionFade}
	// Corners of the PiP: top/bottom, left/right.
	Corners = []string{"tl", "tr", "bl", "br"}
	// PipSizes: PiP width as % of the canvas → exact fraction (num, den).
	PipSizes = map[int][2]int{25: {1, 4}, 33: {1, 3}}
)

// PauseTextDefault is the Pause text until someone writes another.
const PauseTextDefault = "Volvemos enseguida"

// SlateText is the "no signal" card. It goes on air, so viewers see it; in English (decided on
// Oct 13). It must pass PauseText: textoverlay reads Pango markup and draws nothing with < or &.
const SlateText = "NO SIGNAL"

// pausePunct: punctuation allowed in the Pause text besides letters, digits and space.
const pausePunct = ".,:;!?¡¿'\"()-–—…·/+#@%€$*"

// Limits of mixer-scene.json, in runes (Python counts code points).
const (
	maxIDLen  = 256
	maxHidden = 8
	maxPause  = 60
)

// PauseText returns the Pause text trimmed and ok, or ok=false when it is not valid. Same rule as the
// app (mixer.CheckPauseText): 1-60 runes after trimming; letters, digits, space and some
// punctuation. No < > & (textoverlay markup), line breaks or emoji (the font lacks them).
func PauseText(text string) (string, bool) {
	t := strings.TrimFunc(text, pyIsSpace)
	if n := utf8.RuneCountInString(t); n < 1 || n > maxPause {
		return "", false
	}
	for _, r := range t {
		if !(unicode.IsLetter(r) || unicode.Is(unicode.Nd, r) || r == ' ' || strings.ContainsRune(pausePunct, r)) {
			return "", false
		}
	}
	return t, true
}

// pauseTextOf is pause_text on a decoded JSON value: anything but a string is invalid.
func pauseTextOf(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return PauseText(s)
}

// pyIsSpace is Python's str.isspace, which str.strip() uses: unicode.IsSpace plus the ASCII
// separators 0x1c-0x1f, which Python counts as whitespace and Go does not.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// Pip is the picture-in-picture corner and size (25 or 33).
type Pip struct {
	Corner string `json:"corner"`
	Size   int    `json:"size"`
}

// Pause is a still image over everything else. Mute silences the program while it is on.
type Pause struct {
	On   bool   `json:"on"`
	Text string `json:"text"`
	Mute bool   `json:"mute"`
}

// Scene is what goes out on the program. Sources are named by their catalogue id ("hdmi", the by-id
// path of a USB one, "test"), not by their slot: a camera that is unplugged and comes back is still
// the same. An empty Main or Secondary means none (Python's None).
type Scene struct {
	Main       string
	Secondary  string
	Layout     string
	Transition string
	Pip        Pip
	Pause      Pause
	// hidden: sources that are not captured (D11). Replaced whole, never mutated, so copies of a
	// Scene can share it.
	hidden map[string]struct{}
}

// New returns the default scene with main on air.
func New(main string) Scene {
	return Scene{
		Main:       main,
		Layout:     LayoutFull,
		Transition: TransitionCut,
		Pip:        Pip{Corner: "br", Size: 33},
		Pause:      Pause{On: false, Text: PauseTextDefault, Mute: true},
	}
}

// Take puts sid on air. The secondary (PiP, side by side) stays, unless the new main was the
// secondary: then they swap. With no secondary, the previous main becomes it.
func (s *Scene) Take(sid string) bool {
	if sid == s.Main {
		return false
	}
	if sid == s.Secondary || s.Secondary == "" {
		s.Secondary = s.Main
	}
	s.Main = sid
	return true
}

// SetSecondary makes sid the secondary; the main as secondary is a swap.
func (s *Scene) SetSecondary(sid string) bool {
	if sid == s.Secondary {
		return false
	}
	if sid == s.Main {
		return s.Swap()
	}
	s.Secondary = sid
	return true
}

// Swap exchanges main and secondary.
func (s *Scene) Swap() bool {
	if s.Secondary == "" || s.Secondary == s.Main {
		return false
	}
	s.Main, s.Secondary = s.Secondary, s.Main
	return true
}

// SetHidden hides or shows sid. A hidden secondary stops being the secondary.
func (s *Scene) SetHidden(sid string, hidden bool) bool {
	if hidden == s.IsHidden(sid) {
		return false
	}
	if hidden && sid == s.Secondary {
		s.Secondary = ""
	}
	next := make(map[string]struct{}, len(s.hidden)+1)
	for k := range s.hidden {
		if k != sid {
			next[k] = struct{}{}
		}
	}
	if hidden {
		next[sid] = struct{}{}
	}
	s.hidden = next
	return true
}

// IsHidden reports whether sid is hidden.
func (s *Scene) IsHidden(sid string) bool {
	_, ok := s.hidden[sid]
	return ok
}

// Hidden returns the hidden ids, sorted and never nil.
func (s *Scene) Hidden() []string {
	out := make([]string, 0, len(s.hidden))
	for k := range s.hidden {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Snapshot is the scene part of the state's `program` object and of mixer-scene.json (Python
// Scene.to_dict), with the keys in the Python order. The engine embeds it in its own struct and
// adds width, height, fps, measuredFps and newFps after it.
type Snapshot struct {
	Main       *string  `json:"main"`
	Secondary  *string  `json:"secondary"`
	Layout     string   `json:"layout"`
	Transition string   `json:"transition"`
	Hidden     []string `json:"hidden"`
	Pip        Pip      `json:"pip"`
	Pause      Pause    `json:"pause"`
}

// Snapshot returns the scene as published: null for no main/secondary, hidden sorted and [] when empty.
func (s *Scene) Snapshot() Snapshot {
	return Snapshot{
		Main:       optional(s.Main),
		Secondary:  optional(s.Secondary),
		Layout:     s.Layout,
		Transition: s.Transition,
		Hidden:     s.Hidden(),
		Pip:        s.Pip,
		Pause:      s.Pause,
	}
}

func optional(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// LoadMap applies a decoded scene dict (Python Scene.load): every field is checked on its own and
// anything invalid is ignored, keeping the current value.
func (s *Scene) LoadMap(d map[string]any) {
	if d == nil {
		return
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"main", &s.Main}, {"secondary", &s.Secondary}} {
		v, present := d[f.key]
		if !present {
			continue
		}
		if v == nil {
			*f.dst = ""
		} else if id, ok := v.(string); ok && utf8.RuneCountInString(id) <= maxIDLen {
			*f.dst = id
		}
	}
	if l, ok := d["layout"].(string); ok && slices.Contains(Layouts, l) {
		s.Layout = l
	}
	if t, ok := d["transition"].(string); ok && slices.Contains(Transitions, t) {
		s.Transition = t
	}
	if h, ok := d["hidden"].([]any); ok && len(h) <= maxHidden {
		next := make(map[string]struct{}, len(h))
		valid := true
		for _, x := range h {
			id, ok := x.(string)
			if !ok || utf8.RuneCountInString(id) > maxIDLen {
				valid = false
				break
			}
			next[id] = struct{}{}
		}
		if valid {
			s.hidden = next
		}
	}
	if p, ok := d["pip"].(map[string]any); ok {
		if c, ok := p["corner"].(string); ok && slices.Contains(Corners, c) {
			s.Pip.Corner = c
		}
		// Python checks membership by value (`size in PIP_SIZES`), so 25.0 counts as 25; bools never
		// reach here as numbers in Go
		if n, ok := pyNumber(p["size"]); ok {
			if _, valid := PipSizes[int(n)]; valid && float64(int(n)) == n {
				s.Pip.Size = int(n)
			}
		}
	}
	if p, ok := d["pause"].(map[string]any); ok {
		if v, ok := p["on"].(bool); ok {
			s.Pause.On = v
		}
		if v, ok := p["mute"].(bool); ok {
			s.Pause.Mute = v
		}
		if t, ok := pauseTextOf(p["text"]); ok {
			s.Pause.Text = t
		}
	}
}

// pyNumber is the value of a decoded JSON number (float64, json.Number or a Go int from an
// in-process caller). Bools are not numbers here, unlike in Python.
func pyNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n)
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// pyInt is a decoded JSON value that Python would see as an int. A json.Number (decoder with
// UseNumber) keeps the literal, so "25.0" is a float as in Python; a float64 cannot tell 25 from
// 25.0 and is accepted when integral.
func pyInt(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.ParseInt(string(n), 10, 64)
		return i, err == nil
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, false
		}
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}
