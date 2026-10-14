package ui

import (
	"slices"
	"strings"
	"testing"
)

// The Python engine's tests (scripts/test_engine_mixer.py), one per test, named after it.

func overlaps(a, b Rect) bool { return a.overlaps(b) }

// LayoutTest.test_fits_screens
func TestLayoutFitsScreens(t *testing.T) {
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		dw, dh := sz[0], sz[1]
		lay := NewLayout(dw, dh, 4)
		in := func(r Rect) bool { return r.X >= 0 && r.Y >= 0 && r.X+r.W <= dw && r.Y+r.H <= dh }
		all := append(append([]Rect{}, lay.Tiles...), lay.PGM)
		for _, r := range lay.Buttons {
			all = append(all, r)
		}
		for _, r := range all {
			if !in(r) {
				t.Errorf("%v: %v off the screen", sz, r)
			}
		}
		if h := lay.Hit(lay.Tiles[3].X+5, lay.Tiles[3].Y+5, false, "video", false, nil); h != (Hit{Kind: HitTile, Slot: 3}) {
			t.Errorf("%v: tile 3: %+v", sz, h)
		}
		if h := lay.Hit(lay.PGM.X+5, lay.PGM.Y+5, false, "video", false, nil); h.Kind != HitProgram {
			t.Errorf("%v: program: %+v", sz, h)
		}
		// Settings is always there, also in the audio view
		sb := lay.Buttons["settings"]
		if h := lay.Hit(sb.X+3, sb.Y+3, false, "audio", false, nil); h != (Hit{Kind: HitButton, Key: "settings"}) {
			t.Errorf("%v: settings in the audio view: %+v", sz, h)
		}
		for k, b := range lay.Buttons {
			if strings.HasPrefix(k, "k_") { // the keyboard (KBHit)
				if got := lay.KBHit(b.X+3, b.Y+3, "lower"); got != k {
					t.Errorf("%v: %s: KBHit %q", sz, k, got)
				}
				continue
			}
			view := "video"
			if strings.HasPrefix(k, "a_") {
				view = "audio"
			}
			var keys []string
			s := strings.HasPrefix(k, "s_") || strings.HasPrefix(k, "w_") // Settings' and the wizard's: given
			if s {
				keys = []string{k}
			}
			if h := lay.Hit(b.X+3, b.Y+3, strings.HasPrefix(k, "m_"), view, s, keys); h != (Hit{Kind: HitButton, Key: k}) {
				t.Errorf("%v: %s: %+v", sz, k, h)
			}
			if b.H < 44 {
				t.Errorf("%v: %s is %d px high (a finger needs 44)", sz, k, b.H)
			}
		}
		// Settings open: only its buttons; what is below (program, header) not
		if h := lay.Hit(lay.PGM.X+5, lay.PGM.Y+5, false, "video", true, nil); h != (Hit{}) {
			t.Errorf("%v: program under Settings: %+v", sz, h)
		}
		if h := lay.Hit(sb.X+3, sb.Y+3, false, "video", true, nil); h != (Hit{}) {
			t.Errorf("%v: header under Settings: %+v", sz, h)
		}
		sn := lay.Buttons["s_net"]
		if h := lay.Hit(sn.X+3, sn.Y+3, false, "video", false, nil); h.Key == "s_net" {
			t.Errorf("%v: s_net answers with Settings closed", sz)
		}
		// The keyboard: all printable ASCII between its layers, keys of 44 px or more, inside and not overlapping
		chars := map[string]bool{}
		for _, rows := range KBLayers {
			for _, row := range rows {
				for _, k := range row {
					if len(k.name) == 1 {
						chars[k.name] = true
					}
				}
			}
		}
		for c := 32; c < 127; c++ {
			if !chars[string(rune(c))] {
				t.Errorf("the keyboard has no %q", rune(c))
			}
		}
		if len(chars) != 95 {
			t.Errorf("keyboard characters: %d", len(chars))
		}
		for layer, keys := range lay.KB {
			for i, k := range keys {
				a := k.R
				if a.W < 44 || a.H < 44 {
					t.Errorf("%v %s %q: %v too small", sz, layer, k.Name, a)
				}
				if !(a.X >= 0 && a.Y > lay.KBNote && a.X+a.W <= dw && a.Y+a.H <= dh) {
					t.Errorf("%v %s %q: %v off", sz, layer, k.Name, a)
				}
				if got := lay.KBHit(a.X+a.W/2, a.Y+a.H/2, layer); got != k.Name {
					t.Errorf("%v %s %q: KBHit %q", sz, layer, k.Name, got)
				}
				for _, b := range keys[i+1:] {
					if overlaps(a, b.R) {
						t.Errorf("%v %s: %v overlaps %v", sz, layer, a, b.R)
					}
				}
			}
		}
		// Settings: in each tab nothing overlaps (Redes goes inside the client's row: tested apart), all inside
		// the panel; without the client row, «Crear mi wifi» moves up
		for _, client := range []bool{true, false} {
			lay.SettingsRows(client)
			for _, tab := range [][]string{{"s_page_wifi", "s_page_cams", "s_close", "s_client", "s_ap"},
				{"s_page_wifi", "s_page_cams", "s_close", "s_net"}} {
				var rs []Rect
				for _, k := range tab {
					if client || k != "s_client" {
						rs = append(rs, lay.Buttons[k])
					}
				}
				if slices.Contains(tab, "s_ap") {
					rs = append(rs, lay.SettingsQR)
				} else {
					rs = append(rs, lay.SettingsCams)
				}
				for i, a := range rs {
					if !lay.SettingsPanel.Inside(a.X, a.Y, 0) || !lay.SettingsPanel.Inside(a.X+a.W-1, a.Y+a.H-1, 0) {
						t.Errorf("%v: %v outside the panel", sz, a)
					}
					for _, b := range rs[i+1:] {
						if overlaps(a, b) {
							t.Errorf("%v client=%v: %v overlaps %v", sz, client, a, b)
						}
					}
				}
			}
			if (lay.Buttons["s_ap"].Y == lay.Buttons["s_client"].Y) != !client {
				t.Errorf("%v client=%v: s_ap at %d", sz, client, lay.Buttons["s_ap"].Y)
			}
			nb := lay.Buttons["s_networks"]
			if !lay.Buttons["s_client"].Inside(nb.X, nb.Y, 0) {
				t.Errorf("%v: Redes outside the client row", sz)
			}
		}
		// The same margin above and below as at the sides, and the header's line not touching its buttons
		m := lay.M
		for _, k := range []string{"lock", "power", "settings"} {
			b := lay.Buttons[k]
			if b.Y != m || lay.Status.H-(b.Y+b.H) != m {
				t.Errorf("%v: %s margins %d/%d", sz, k, b.Y, lay.Status.H-(b.Y+b.H))
			}
		}
		// Right to left: Power, Settings and the padlock
		if p := lay.Buttons["power"]; p.X+p.W != dw-m {
			t.Errorf("%v: power at %d", sz, p.X)
		}
		if !(lay.Buttons["settings"].X < lay.Buttons["power"].X && lay.Buttons["lock"].X < lay.Buttons["settings"].X) {
			t.Errorf("%v: header order", sz)
		}
		for _, r := range lay.Tiles {
			if r.Y+r.H != dh-m {
				t.Errorf("%v: tile bottom %d", sz, r.Y+r.H)
			}
			if r.H+1 < r.W*9/16*3/4 { // tiles not squashed
				t.Errorf("%v: tile %v squashed", sz, r)
			}
		}
		if lay.PGM.Y != lay.Status.H+m || lay.PGM.W > dw*5/8 {
			t.Errorf("%v: program %v", sz, lay.PGM)
		}
		// The Power menu covers the rest: open, only its buttons answer
		if h := lay.Hit(lay.PGM.X+5, lay.PGM.Y+5, true, "video", false, nil); h != (Hit{}) {
			t.Errorf("%v: program under the menu: %+v", sz, h)
		}
		mc := lay.Buttons["m_cancel"]
		if h := lay.Hit(mc.X+3, mc.Y+3, false, "video", false, nil); strings.HasPrefix(h.Key, "m_") {
			t.Errorf("%v: m_cancel answers with the menu closed", sz)
		}
		for _, r := range lay.TileMeters { // the meters, inside their tile
			if !in(r) {
				t.Errorf("%v: meter %v", sz, r)
			}
		}
		var rs, ra []Rect
		rs = append(append(rs, lay.Tiles...), lay.PGM, lay.PGMMeter)
		for k, v := range lay.Buttons {
			if !hasAnyPrefix(k, "m_", "a_", "s_", "k_", "w_") {
				rs = append(rs, v)
			}
			if strings.HasPrefix(k, "a_") && !strings.HasPrefix(k, "a_fader") {
				ra = append(ra, v)
			}
		}
		for _, set := range [][]Rect{ra, rs} { // in the audio view nothing overlaps either
			for i, a := range set {
				for _, b := range set[i+1:] {
					if overlaps(a, b) {
						t.Errorf("%v: %v overlaps %v", sz, a, b)
					}
				}
			}
		}
	}
}

// LayoutTest.test_touch_commands
func TestTouchCommands(t *testing.T) {
	a := "a"
	st := &Status{}
	st.Program.Main = &a
	st.Program.Layout = "full"
	ids := []string{"a", "", "b", ""}
	tile := func(i int) Hit { return Hit{Kind: HitTile, Slot: i} }
	button := func(k string) Hit { return Hit{Kind: HitButton, Key: k} }
	check := func(got, want map[string]any) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("%v, want %v", got, want)
			}
		}
	}
	check(TouchCommand(tile(2), false, st, ids), map[string]any{"cmd": "take", "source": "b"})
	check(TouchCommand(tile(2), true, st, ids), map[string]any{"cmd": "secondary", "source": "b"})
	if TouchCommand(tile(1), false, st, ids) != nil {
		t.Error("an empty tile does nothing")
	}
	check(TouchCommand(Hit{Kind: HitProgram}, false, st, ids), map[string]any{"cmd": "swap"})
	check(TouchCommand(button("fade"), false, st, ids), map[string]any{"cmd": "transition", "transition": "fade"})
	check(TouchCommand(button("pip"), false, st, ids), map[string]any{"cmd": "layout", "layout": "pip"})
	check(TouchCommand(button("cut"), false, st, ids), map[string]any{"cmd": "transition", "transition": "cut"})
	st.Program.Layout = "pip" // with the PiP on, its button: tap = corner, long press = size
	st.Program.PiP = &struct {
		Corner string `json:"corner"`
		Size   int    `json:"size"`
	}{"br", 33}
	check(TouchCommand(button("pip"), false, st, ids), map[string]any{"cmd": "pip", "corner": "bl"})
	check(TouchCommand(button("pip"), true, st, ids), map[string]any{"cmd": "pip", "size": 25})
	check(TouchCommand(button("pause"), false, st, ids), map[string]any{"cmd": "pause", "on": true})
	check(TouchCommand(button("lock"), false, st, ids), map[string]any{"cmd": "lock", "on": true})
	if TouchCommand(button("emit"), false, st, ids) != nil { // held, not tapped
		t.Error("emit is not a tap")
	}
}

// ModeLabelTest.test_mode_label
func TestModeLabel(t *testing.T) {
	for _, c := range []struct {
		mode string
		fps  float64
		want string
	}{{"1920x1080@30", 30, "1080p30"}, {"1280x720", 0, "720p"}, {"1080x1920@30", 29.97, "vertical 1080p30"}} {
		if got := ModeLabel(c.mode, c.fps, Spanish); got != c.want {
			t.Errorf("ModeLabel(%q, %v) = %q, want %q", c.mode, c.fps, got, c.want)
		}
	}
}

// LayoutTest.test_audio_view_commands
func TestAudioViewCommands(t *testing.T) {
	lay := NewLayout(1024, 600, 4)
	main := "i2s"
	g0, g1 := 0.0, -6.0
	au := &AudioStatus{Mode: "single", Main: &main, Sources: []AudioInput{{ID: "i2s", InMix: true, GainDb: &g0},
		{ID: "alsa:C4K", GainDb: &g1}}}
	cmd := func(w string, y int) map[string]any { return AudioCommand(w, y, au, lay) }
	eq := func(got, want map[string]any) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("%v, want %v", got, want)
			}
		}
	}
	eq(cmd("a_mix", 0), map[string]any{"cmd": "audio_mode", "mode": "mix"})
	eq(cmd("a_follow", 0), map[string]any{"cmd": "audio_follow", "on": true})
	eq(cmd("a_on_1", 0), map[string]any{"cmd": "audio", "source": "alsa:C4K"}) // one source: on air
	eq(cmd("a_plus_1", 0), map[string]any{"cmd": "audio_mix", "source": "alsa:C4K", "gain": -4.0})
	eq(cmd("a_minus_0", 0), map[string]any{"cmd": "audio_mix", "source": "i2s", "gain": -2.0})
	if cmd("a_on_3", 0) != nil { // empty strip
		t.Error("an empty strip does nothing")
	}
	au.Mode = "mix"
	eq(cmd("a_on_1", 0), map[string]any{"cmd": "audio_mix", "source": "alsa:C4K", "on": true}) // mix: in
	f := lay.Buttons["a_fader_0"]
	eq(cmd("a_fader_0", f.Y), map[string]any{"cmd": "audio_mix", "source": "i2s", "gain": 12.0})
	eq(cmd("a_fader_0", f.Y+f.H), map[string]any{"cmd": "audio_mix", "source": "i2s", "gain": -40.0})
	if g := FaderGain(f, int(FaderY(f, 0))); g != 0 { // there and back, 0 dB
		t.Errorf("0 dB round trip: %v", g)
	}
	g11 := 11.0
	au.Sources[0].GainDb = &g11
	if g := cmd("a_plus_0", 0)["gain"]; g != 12.0 { // capped
		t.Errorf("capped gain: %v", g)
	}
}

// LayoutTest.test_hold
func TestHold(t *testing.T) {
	b := func(k string) Hit { return Hit{Kind: HitButton, Key: k} }
	if HoldKey(b("m_reboot"), false, true) != "reboot" {
		t.Error("m_reboot")
	}
	if HoldKey(b("m_poweroff"), true, true) != "" { // locked: not even the Power menu
		t.Error("locked m_poweroff")
	}
	if HoldKey(b("emit"), false, true) != "" { // with the menu open, only it
		t.Error("emit with the menu")
	}
	if HoldTime("poweroff") != HoldPower || HoldTime("emit") != Hold {
		t.Error("hold times")
	}
	if MeterFrac(-60, true) != 0 || MeterFrac(-30, true) != 0.5 || MeterFrac(3, true) != 1 || MeterFrac(0, false) != 0 {
		t.Error("MeterFrac")
	}
}

// LayoutTest.test_meter_ballistics
func TestMeterBallistics(t *testing.T) {
	st := MeterStep(MeterState{}, -10, true, 0)
	if st.Shown != -10 {
		t.Fatal(st)
	}
	st = MeterStep(st, -40, true, 0.5) // falls MeterDecay dB/s, not at once
	if st.Shown != -10-MeterDecay*0.5 || st.Peak != -10 {
		t.Fatal(st)
	}
	st = MeterStep(st, -5, true, 0.55) // rises at once
	if st.Shown != -5 || st.Peak != -5 {
		t.Fatal(st)
	}
	st = MeterStep(st, 0, false, 0.55+MeterHold+0.1)
	if st.Peak >= -5 { // after the hold, the peak falls with the bar
		t.Fatal(st)
	}
	b := func(k string) Hit { return Hit{Kind: HitButton, Key: k} }
	if HoldKey(b("emit"), false, false) != "emit" || HoldKey(b("emit"), true, false) != "" ||
		HoldKey(b("lock"), true, false) != "unlock" || HoldKey(b("lock"), false, false) != "" ||
		HoldKey(Hit{Kind: HitTile}, false, false) != "" {
		t.Error("hold keys")
	}
}

// test_review_fixes (its psk_ok part) and the keyboard's limits.
func TestPSKOK(t *testing.T) {
	ok := func(s string) bool { return PSKOK([]byte(s)) }
	if !ok(strings.Repeat("a", 8)) || !ok(strings.Repeat("A1", 32)) {
		t.Error("valid passwords refused")
	}
	if ok(strings.Repeat("a", 7)) || ok(strings.Repeat("g", 64)) || ok(strings.Repeat("a", 65)) {
		t.Error("invalid passwords accepted")
	}
}
