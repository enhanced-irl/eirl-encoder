package scene

import (
	"reflect"
	"strings"
	"testing"
)

func pair(s *Scene) [2]string { return [2]string{s.Main, s.Secondary} }

func TestSceneTakeKeepsSecondaryAndSwapsWhenNeeded(t *testing.T) {
	s := New("hdmi")
	if !s.Take("cam") || pair(&s) != [2]string{"cam", "hdmi"} { // no secondary: the previous main becomes it
		t.Fatalf("take cam: %v", pair(&s))
	}
	if !s.Take("logi") || pair(&s) != [2]string{"logi", "hdmi"} { // the secondary stays
		t.Fatalf("take logi: %v", pair(&s))
	}
	if !s.Take("hdmi") || pair(&s) != [2]string{"hdmi", "logi"} { // the secondary on air: they swap
		t.Fatalf("take hdmi: %v", pair(&s))
	}
	if s.Take("hdmi") {
		t.Fatal("taking the main again changed the scene")
	}
}

func TestSceneSecondaryAndSwap(t *testing.T) {
	s := New("a")
	if !s.SetSecondary("b") {
		t.Fatal("set secondary b")
	}
	if !s.SetSecondary("a") || pair(&s) != [2]string{"b", "a"} { // the main as secondary = swap
		t.Fatalf("set secondary a: %v", pair(&s))
	}
	if !s.Swap() || pair(&s) != [2]string{"a", "b"} {
		t.Fatalf("swap: %v", pair(&s))
	}
}

func strp(s string) *string { return &s }

func TestScenePersistRoundtripIgnoresJunk(t *testing.T) {
	s := New("a")
	s.LoadMap(map[string]any{"main": "b", "secondary": "c", "layout": "pip", "transition": "fade",
		"hidden": []any{"d"}, "pip": map[string]any{"corner": "tl", "size": 25.0},
		"pause": map[string]any{"on": true, "text": " Ya vuelvo ", "mute": false}})
	want := Snapshot{Main: strp("b"), Secondary: strp("c"), Layout: "pip", Transition: "fade",
		Hidden: []string{"d"}, Pip: Pip{"tl", 25}, Pause: Pause{true, "Ya vuelvo", false}}
	if got := s.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	s.LoadMap(map[string]any{"main": 7.0, "layout": "zoom", "transition": nil, "hidden": "x",
		"pip": map[string]any{"corner": "x", "size": true}, "pause": map[string]any{"on": 1.0, "text": "<b>", "mute": "no"}})
	if got := s.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("junk changed the scene: %+v", got)
	}
}

func TestScenePauseText(t *testing.T) {
	good := map[string]string{
		"  Volvemos en 5 min. ¡Ahora! ": "Volvemos en 5 min. ¡Ahora!",
		"Café, ñu — (vuelvo) 100 %":     "Café, ñu — (vuelvo) 100 %",
		strings.Repeat("x", 60):         strings.Repeat("x", 60),
	}
	for in, want := range good {
		if got, ok := PauseText(in); !ok || got != want {
			t.Errorf("PauseText(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	bad := []string{"  Volvemos en 5\u2019 ", "Café, ñu — «no»", // \u2019 « » are not in the list
		"", "   ", strings.Repeat("a", 61), "a<b", "a&b", "dos\nlíneas", "tab\tx", "hola 🙂", "²"}
	for _, in := range bad {
		if got, ok := PauseText(in); ok {
			t.Errorf("PauseText(%q) = %q, want invalid", in, got)
		}
	}
	for _, in := range []any{7.0, nil} { // not a string at all
		if _, ok := pauseTextOf(in); ok {
			t.Errorf("pauseTextOf(%v) valid", in)
		}
	}
}

func TestPauseTextStripsLikePython(t *testing.T) {
	// str.strip() also drops 0x1c-0x1f, which unicode.IsSpace does not count
	if got, ok := PauseText("\x1c\u3000Hola\u00a0\x1f"); !ok || got != "Hola" {
		t.Fatalf("got %q, %v", got, ok)
	}
	if _, ok := PauseText("Ho\x1cla"); ok {
		t.Fatal("a separator inside the text was accepted")
	}
}

func TestStillDescSlateTextIsSafeForTextoverlay(t *testing.T) {
	// textoverlay reads Pango markup: the text (and its future translations) follows the Pause rule
	if got, ok := PauseText(SlateText); !ok || got != SlateText {
		t.Fatalf("PauseText(SlateText) = %q, %v", got, ok)
	}
}

func TestStillDescTextNeverInDescription(t *testing.T) {
	// Only the text-rule part: the Pause text is written by the user and the rule lets quotes
	// through, so it can never be spliced into a pipeline description; it goes by property. The
	// description checks belong to the pipeline package.
	if _, ok := PauseText(`Back "soon" 'ok'`); !ok {
		t.Fatal("quotes are part of the rule; if this changes, revisit how the text reaches textoverlay")
	}
}

func TestSnapshotNullsAndEmptyHidden(t *testing.T) {
	s := New("")
	snap := s.Snapshot()
	if snap.Main != nil || snap.Secondary != nil || snap.Hidden == nil || len(snap.Hidden) != 0 {
		t.Fatalf("got %+v", snap)
	}
	s.SetHidden("b", true)
	s.SetHidden("a", true)
	if got := s.Snapshot().Hidden; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("hidden not sorted: %v", got)
	}
}

func TestHiddenIsCopyOnWrite(t *testing.T) {
	a := New("x")
	a.SetHidden("h", true)
	b := a
	b.SetHidden("h", false)
	if !a.IsHidden("h") || b.IsHidden("h") {
		t.Fatal("a copy of the scene shares its hidden set")
	}
}
