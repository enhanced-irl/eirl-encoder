package ui

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/detail"
)

// detailArgs are the arguments each code's text uses (ENGINE_GO.md §13; hung_on_air's ids become {names} on the
// screen, no_format's optional warnings go after it).
var detailArgs = map[string][]string{"hdmi_mode_changed": {"from", "to"}, "hdmi_interlaced": {"mode"},
	"hdmi_too_fast": {"mode"}, "hdmi_set_mode_failed": {"error"}, "unsupported": {"error"}, "probe_failed": {"error", "rc"},
	"hung_on_air": {"names"}, "net_no_video": {"tracks"}, "net_behind": {"seconds"}, "frames_stopped": {"seconds"},
	"no_samples": {"seconds"}, "stream_error": {"error"}, "build_failed": {"error"}, "internal_error": {"error"}}

// Every language has a text for every code of the engine's closed list, and no other; each with the contract's
// arguments.
func TestDetailsMatchEngine(t *testing.T) {
	codes := detail.Codes()
	if len(codes) != 40 {
		t.Fatalf("the engine has %d codes: ENGINE_GO.md §13 says 40", len(codes))
	}
	for _, lang := range Languages {
		tx := Lang(lang)
		got := slices.Sorted(maps.Keys(tx.Details))
		if !slices.Equal(got, codes) {
			t.Errorf("%s: %v, want %v", lang, got, codes)
		}
		for code, text := range tx.Details {
			var used []string
			for _, m := range reArg.FindAllStringSubmatch(text, -1) {
				used = append(used, m[1])
			}
			sort.Strings(used)
			if want := detailArgs[code]; !slices.Equal(used, want) {
				t.Errorf("%s %s: %q uses %v, want %v", lang, code, text, used, want)
			}
		}
	}
}

func TestDetailText(t *testing.T) {
	h := newHarness(t)
	h.u.SetLanguage("fr")
	u := h.u
	check := func(code string, args map[string]string, det, want string) {
		t.Helper()
		if got := u.detailText(code, args, det); got != want {
			t.Errorf("%s %v: %q, want %q", code, args, got, want)
		}
	}
	check("hdmi_no_signal", nil, "sin señal HDMI", "pas de signal HDMI")
	check("hdmi_mode_changed", map[string]string{"from": "1080p60", "to": "1280x720@60"}, "x",
		"HDMI modifié : 1080p60 → 1280x720@60")
	check("net_behind", map[string]string{"seconds": "2.4"}, "va 2.4 s atrasada", "2,4 s de retard")
	check("no_format", nil, "x", "aucun format capturable")
	check("no_format", map[string]string{"warnings": "MJPG 4K"}, "x", "aucun format capturable (MJPG 4K)")
	check("stream_error", map[string]string{"error": "Internal data stream error."}, "x", "Internal data stream error.")
	// Unknown code (a newer engine), or an argument missing: the engine's own text
	check("brand_new_code", nil, "texto nuevo", "texto nuevo")
	check("hdmi_interlaced", nil, "1080i60 entrelazado: el VI del Nano no lo captura",
		"1080i60 entrelazado: el VI del Nano no lo captura")
	check("", nil, "", "")
	// hung_on_air: the ids on air, named as the screen names them (alias, the engine's own names translated)
	st := *u.st
	st.Sources = append([]SourceStatus(nil), st.Sources...)
	st.Sources = append(st.Sources, SourceStatus{ID: "hdmi", Slot: 4, Name: "HDMI de la placa", Alias: "Plateau"})
	st.Audio = &AudioStatus{Sources: []AudioInput{{ID: "i2s", Name: "HDMI de la placa"}, {ID: "alsa:x", Name: "Logi"}}}
	u.SetStatus(&st)
	check("hung_on_air", map[string]string{"video": "hdmi,usb1", "audio": "i2s,alsa:x"}, "x",
		"bloquée : la relancer couperait Plateau, usb1, HDMI de la carte, Logi")
	check("hung_on_air", nil, "colgada: …", "bloquée : pas relancée en direct")
	// Phones: by code, else as before the codes
	if _, w := PhonesText(&PhonesStatus{On: true, State: "waiting", DetailCode: "phones_no_device"}, French); w != "pas de sortie audio HDMI" {
		t.Error(w)
	}
	if _, w := PhonesText(&PhonesStatus{On: true, State: "waiting", Detail: phonesNoAudioDetail}, French); w != French.PhonesNoAudio {
		t.Error(w)
	}
	if _, w := PhonesText(&PhonesStatus{On: true, State: "error"}, French); w != French.PhonesRetrying {
		t.Error(w)
	}
}

// Where the screen draws them: a tile, an audio strip, the network cameras page; without a code, the state as before.
func TestDetailsDrawn(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	h.u.SetLanguage("en")
	st := &Status{}
	st.Program.Layout = "full"
	st.Sources = []SourceStatus{
		{ID: "hdmi", Slot: 1, Name: "HDMI de la placa", State: "no_signal", Detail: "sin señal HDMI", DetailCode: "hdmi_no_signal"},
		{ID: "usb1", Slot: 2, Name: "Cam Link", State: "gone", Detail: "desconectada"}, // an engine before the codes
		{ID: "usb2", Slot: 3, Name: "Logi", State: "error", Detail: "texto nuevo", DetailCode: "brand_new"},
		{ID: "net:cam1", Slot: 4, Kind: "network", Name: "Phone", State: "no_signal", Detail: "va 2.4 s atrasada",
			DetailCode: "net_behind", DetailArgs: map[string]string{"seconds": "2.4"}}}
	st.Audio = &AudioStatus{Mode: "single", Sources: []AudioInput{{ID: "i2s", Name: "HDMI de la placa", State: "no_signal",
		Detail: "sin audio HDMI", DetailCode: "hdmi_no_audio", VideoSlot: 1}}}
	h.u.SetStatus(st)
	draw := func() {
		shown = shown[:0]
		h.u.Dirty = true
		h.u.Tick()
	}
	draw()
	for _, want := range []string{"no HDMI signal", "disconnected", "texto nuevo", "2.4 s behind"} {
		if !slices.Contains(shown, want) {
			t.Errorf("tiles: no %q in %q", want, shown)
		}
	}
	h.u.view = "audio"
	draw()
	if !slices.Contains(shown, "no HDMI audio") {
		t.Errorf("audio strip: %q", shown)
	}
	h.u.view = "video"
	h.wifiState()
	h.net.snap.Status.Cameras = 1
	h.u.settingsOpen, h.u.settingsPage = true, "cams"
	draw()
	if !slices.Contains(shown, "2.4 s behind") {
		t.Errorf("cameras page: %q", shown)
	}
}

// Every detail without arguments fits on a tile and on an audio strip in every language (with arguments, the
// data may be long: it is shortened with «…»).
func TestDetailsFit(t *testing.T) {
	for _, lang := range Languages {
		for _, code := range detail.Codes() {
			if strings.Contains(Lang(lang).Details[code], "{") {
				continue
			}
			var cut []string
			fitCut = func(s string) { cut = append(cut, s) }
			h, _ := drawHarness(t)
			h.u.SetLanguage(lang)
			st := &Status{}
			st.Program.Layout = "full"
			st.Sources = []SourceStatus{{ID: "usb1", Slot: 1, Name: "Cam", State: "error", DetailCode: code}}
			st.Audio = &AudioStatus{Mode: "single", Sources: []AudioInput{{ID: "alsa:1", Name: "Cam", State: "error",
				DetailCode: code, VideoSlot: 1}}}
			h.u.SetStatus(st)
			h.u.Dirty = true
			h.u.Tick()
			h.u.view = "audio"
			h.u.Dirty = true
			h.u.Tick()
			fitCut = nil
			for _, c := range cut {
				if c == Lang(lang).Details[code] {
					t.Errorf("%s %s: %q is cut", lang, code, c)
				}
			}
		}
	}
}
