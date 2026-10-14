package scene

import "slices"

// Error codes of the scene commands, as the socket returns them (MIXER.md §5). Stable: the app and
// the screen match on them.
const (
	CodeUnknownSource  = "unknown_source"
	CodeHiddenSource   = "hidden_source"
	CodeOnAir          = "on_air"
	CodeNoSecondary    = "no_secondary"
	CodeBadLayout      = "bad_layout"
	CodeBadTransition  = "bad_transition"
	CodeBadRequest     = "bad_request"
	CodeUnknownCommand = "unknown_command"
)

// ApplyCommand applies an already decoded command to the scene (Python apply_command). known holds
// the ids of the sources that have a slot. It returns whether the scene changed and an error code,
// "" when there is none (a command that changes nothing is not an error). The same for the screen
// and for the web.
//
// msg is decoded JSON: numbers are float64 (or json.Number with UseNumber), and the type checks
// mirror Python's: a non-string source is unknown_source, hidden/on/mute must be bools.
func ApplyCommand(s *Scene, msg map[string]any, known map[string]bool) (bool, string) {
	cmd, _ := msg["cmd"].(string)
	switch cmd {
	case "take", "secondary", "hide":
		src, ok := msg["source"].(string)
		if !ok || !known[src] {
			return false, CodeUnknownSource
		}
		if cmd == "hide" {
			hidden, ok := msg["hidden"].(bool)
			if !ok {
				return false, CodeBadRequest
			}
			if hidden && src == s.Main {
				return false, CodeOnAir // another one on air first: the program is never left without picture this way
			}
			return s.SetHidden(src, hidden), ""
		}
		if s.IsHidden(src) {
			return false, CodeHiddenSource
		}
		if cmd == "take" {
			return s.Take(src), ""
		}
		return s.SetSecondary(src), ""
	case "swap":
		if s.Secondary == "" || s.Secondary == s.Main {
			return false, CodeNoSecondary
		}
		return s.Swap(), ""
	case "layout":
		l, ok := msg["layout"].(string)
		if !ok || !slices.Contains(Layouts, l) {
			return false, CodeBadLayout
		}
		changed := s.Layout != l
		s.Layout = l
		return changed, ""
	case "transition":
		t, ok := msg["transition"].(string)
		if !ok || !slices.Contains(Transitions, t) {
			return false, CodeBadTransition
		}
		changed := s.Transition != t
		s.Transition = t
		return changed, ""
	case "pip":
		return applyPip(s, msg)
	case "pause":
		return applyPause(s, msg)
	}
	return false, CodeUnknownCommand
}

func applyPip(s *Scene, msg map[string]any) (bool, string) {
	cv, sv := msg["corner"], msg["size"]
	if cv == nil && sv == nil {
		return false, CodeBadRequest
	}
	next := s.Pip
	if cv != nil {
		c, ok := cv.(string)
		if !ok || !slices.Contains(Corners, c) {
			return false, CodeBadRequest
		}
		next.Corner = c
	}
	if sv != nil {
		n, ok := pyInt(sv)
		if !ok || (n != 25 && n != 33) {
			return false, CodeBadRequest
		}
		next.Size = int(n)
	}
	changed := next != s.Pip
	s.Pip = next
	return changed, ""
}

func applyPause(s *Scene, msg map[string]any) (bool, string) {
	ov, tv, mv := msg["on"], msg["text"], msg["mute"]
	if ov == nil && tv == nil && mv == nil {
		return false, CodeBadRequest
	}
	next := s.Pause
	if ov != nil {
		on, ok := ov.(bool)
		if !ok {
			return false, CodeBadRequest
		}
		next.On = on
	}
	if mv != nil {
		mute, ok := mv.(bool)
		if !ok {
			return false, CodeBadRequest
		}
		next.Mute = mute
	}
	if tv != nil {
		text, ok := pauseTextOf(tv)
		if !ok {
			return false, CodeBadRequest
		}
		next.Text = text
	}
	changed := next != s.Pause
	s.Pause = next
	return changed, ""
}
