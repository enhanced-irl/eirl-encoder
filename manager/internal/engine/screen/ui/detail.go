package ui

import (
	"regexp"
	"strings"
)

// Details (ENGINE_GO.md §13): the engine says why a source, an audio input or the headphones are not live with a
// stable code and its arguments, next to its Spanish `detail`. The screen draws the code in its language; an
// unknown code (a newer engine) draws `detail` as it comes.

var reArg = regexp.MustCompile(`\{([a-z]+)\}`)

// detailText is a detail in the screen's language: the code's text with its arguments, else detail ("" if the
// code says nothing and there is no detail).
func (u *UI) detailText(code string, args map[string]string, detail string) string {
	tx := u.Tx
	if code == "hung_on_air" {
		names := u.onAirNames(args)
		if names == "" {
			code = "hung_on_air_unknown" // which ones, the engine did not say: the same, without the names
		} else {
			args = map[string]string{"names": names}
		}
	}
	t, ok := tx.Details[code]
	if !ok {
		return detail
	}
	missing := false
	t = reArg.ReplaceAllStringFunc(t, func(m string) string {
		k := m[1 : len(m)-1]
		v, ok := args[k]
		if !ok || v == "" {
			missing = true
		}
		if k == "seconds" { // «2.4» → «2,4» where the comma is the decimal separator
			v = strings.ReplaceAll(v, ".", tx.Decimal)
		}
		return v
	})
	if missing && detail != "" {
		return detail // an argument it needs is not there: the engine's own text says more
	}
	if code == "no_format" && args["warnings"] != "" {
		t += " (" + args["warnings"] + ")"
	}
	return t
}

// onAirNames are the sources on air of hung_on_air (video and audio: ids, comma separated) as the screen names
// them, without repeating one.
func (u *UI) onAirNames(args map[string]string) string {
	var names []string
	add := func(n string) {
		if n != "" && !contains(names, n) {
			names = append(names, n)
		}
	}
	for _, id := range splitIDs(args["video"]) {
		name := u.sourceName(id, id)
		for i := range u.st.Sources {
			if x := &u.st.Sources[i]; x.ID == id {
				name = u.srcLabel(x)
			}
		}
		add(name)
	}
	if au := u.st.Audio; au != nil {
		for _, id := range splitIDs(args["audio"]) {
			name := u.sourceName(id, id)
			for _, a := range au.Sources {
				if a.ID == id {
					name = u.sourceName(a.ID, a.Name)
				}
			}
			add(name)
		}
	}
	return strings.Join(names, ", ")
}

func splitIDs(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// notLiveText is the last line of a source that is not live: its detail in the screen's language; without a code
// (an engine from before the codes), the state word as the screen always drew it, or detail if asked.
func (u *UI) notLiveText(state, code string, args map[string]string, detail string, orDetail bool) string {
	if code != "" {
		if t := u.detailText(code, args, detail); t != "" {
			return t
		}
	}
	if orDetail && detail != "" {
		return detail
	}
	t, ok := u.Tx.StateText[state]
	if !ok {
		t = state
	}
	return strings.ToLower(t)
}
