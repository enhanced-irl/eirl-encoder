package ui

import (
	"bytes"
	"image"
	"image/color"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

var testFonts *Fonts

func fonts(t testing.TB) *Fonts {
	t.Helper()
	if testFonts == nil {
		f, err := LoadFonts("testdata/fonts")
		if err != nil {
			t.Fatal(err)
		}
		testFonts = f
	}
	return testFonts
}

type levels struct{ pgm, in float64 }

func (l *levels) ProgramPeak(ch int) (float64, bool) { return l.pgm - float64(ch)*3, true }
func (l *levels) InputPeak(id string) (float64, bool) {
	if id == "" {
		return 0, false
	}
	return l.in, true
}

// drawHarness is a harness that draws.
func drawHarness(t testing.TB) (*harness, *levels) {
	h := newHarness(&testing.T{})
	h.t = nil
	lv := &levels{pgm: -9, in: -12}
	h.u.Fonts, h.u.Levels, h.u.Logo, h.u.PauseSupported = fonts(t), lv, DefaultLogo(), true
	h.u.LinkInfo = func() (string, int) { return "srt", 0 }
	h.u.SetLayout(h.lay)
	return h, lv
}

// ScreenDrawTest.test_logo_rgba: the logo goes in the screen's byte order (RGBA), scaled once per height.
func TestLogoRGBA(t *testing.T) {
	h, _ := drawHarness(t)
	src := h.u.Logo
	a := h.u.logoImage(src.Bounds().Dy())
	if h.u.logoImage(src.Bounds().Dy()) != a { // only once per height
		t.Fatal("scaled twice")
	}
	found := false
	for y := 0; y < a.Rect.Dy() && !found; y++ {
		for x := 0; x < a.Rect.Dx(); x++ {
			want := color.RGBAModel.Convert(src.At(src.Bounds().Min.X+x, src.Bounds().Min.Y+y)).(color.RGBA)
			if want.R == want.B || want.A != 255 {
				continue
			}
			got := a.RGBAAt(x, y)
			if got != want {
				t.Fatalf("pixel (%d,%d) %v, want %v (RGBA, not BGRA)", x, y, got, want)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no pixel with R ≠ B")
	}
	if b := h.u.logoImage(30); b == a || b.Rect.Dy() != 30 {
		t.Fatal("another height")
	}
}

// ScreenDrawTest.test_keyboard_draws_only_dots: what is drawn as text with the keyboard and the list open:
// never anything of the typed password.
func TestKeyboardDrawsOnlyDots(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	net := h.net
	net.snap.API = Yes
	net.snap.Status = &NetStatus{Enabled: true, AccessPoint: AccessPoint{Policy: "always", State: "on"}}
	net.snap.Client = &WifiClient{Enabled: true, State: "disconnected"}
	net.snap.Scan = []ScanNet{{SSID: "Red", Signal: 70, Security: "wpa2", Band: "5", Known: true}}
	h.u.settingsOpen = true
	for _, layer := range KBLayerNames {
		h.u.openKeyboard("Red", "")
		for _, c := range []byte("SECRETOx9") {
			h.u.kbKey(string(c), 0)
		}
		h.u.kb.layer = layer
		h.u.press = &press{hit: Hit{Kind: HitKey, Key: "k"}, since: h.now()}
		h.u.Dirty = true
		if !h.u.Tick() {
			t.Fatal("no image")
		}
	}
	h.u.openNetworks()
	h.u.Dirty = true
	h.u.Tick()
	if !reflect.DeepEqual(h.u.listShown, []string{"Red"}) {
		t.Fatal(h.u.listShown)
	}
	if len(shown) == 0 {
		t.Fatal("nothing drawn")
	}
	for _, s := range shown {
		if strings.Contains(s, "SECRETO") || strings.Contains(s, "x9") {
			t.Fatalf("the password drawn as text: %q", s)
		}
	}
}

// ScreenDrawTest.test_icons
func TestIcons(t *testing.T) {
	var p Path
	for _, kind := range []string{"wifi", "power", "lock", "unlock", "gear", "close", "hotspot", "camera", "wifi1",
		"wifi2", "wifi3", "shift", "backspace", "back", "globe", "clock", "update", "info", "check", "grid"} {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		drawIcon(NewCanvas(img, nil), &p, kind, 32, 32, 18, 3, Color{1, 1, 1, 1})
		if bytes.Count(img.Pix, []byte{0}) == len(img.Pix) {
			t.Errorf("%s draws nothing", kind)
		}
	}
}

// Every text of every table is there (an empty field would be a hole on the screen), with the same keys as
// Spanish in the maps.
func TestStringsComplete(t *testing.T) {
	es := reflect.ValueOf(Spanish).Elem()
	for _, code := range Languages {
		tx := Lang(code)
		if tx.Code != code {
			t.Errorf("%s: Code %q", code, tx.Code)
		}
		v := reflect.ValueOf(tx).Elem()
		for i := range v.NumField() {
			f, name := v.Field(i), v.Type().Field(i).Name
			switch f.Kind() {
			case reflect.String:
				if f.String() == "" {
					t.Errorf("%s: %s is empty", code, name)
				}
			case reflect.Array:
				for j := range f.Len() {
					if f.Index(j).String() == "" {
						t.Errorf("%s: %s[%d] is empty", code, name, j)
					}
				}
			case reflect.Map:
				ref := es.Field(i)
				if f.Len() == 0 || f.Len() != ref.Len() {
					t.Errorf("%s: %s has %d keys (es: %d)", code, name, f.Len(), ref.Len())
				}
				for _, k := range ref.MapKeys() {
					if s := f.MapIndex(k); !s.IsValid() || s.String() == "" {
						t.Errorf("%s: %s[%v] is missing", code, name, k)
					}
				}
			case reflect.Slice:
				if f.Len() == 0 || f.Len() != es.Field(i).Len() {
					t.Errorf("%s: %s has %d items (es: %d)", code, name, f.Len(), es.Field(i).Len())
				}
			}
		}
	}
	if Lang("de") != English || Lang("") != English || New(nil, nil, nil).Tx != English {
		t.Error("the default is not English")
	}
}

var reVerb = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z%]`)

// The fmt verbs of every text are the same, in the same order, in every language (a missing or swapped one
// would print %!d(MISSING) on the screen).
func TestStringsVerbs(t *testing.T) {
	verbs := func(s string) string { return strings.Join(reVerb.FindAllString(s, -1), " ") }
	es := reflect.ValueOf(Spanish).Elem()
	for _, code := range Languages {
		v := reflect.ValueOf(Lang(code)).Elem()
		for i := range v.NumField() {
			f, ref, name := v.Field(i), es.Field(i), v.Type().Field(i).Name
			var pairs [][2]string
			switch f.Kind() {
			case reflect.String:
				pairs = append(pairs, [2]string{f.String(), ref.String()})
			case reflect.Array, reflect.Slice:
				for j := range min(f.Len(), ref.Len()) {
					pairs = append(pairs, [2]string{f.Index(j).String(), ref.Index(j).String()})
				}
			case reflect.Map:
				for _, k := range ref.MapKeys() {
					if s := f.MapIndex(k); s.IsValid() {
						pairs = append(pairs, [2]string{s.String(), ref.MapIndex(k).String()})
					}
				}
			}
			for _, p := range pairs {
				if verbs(p[0]) != verbs(p[1]) {
					t.Errorf("%s: %s %q has verbs %q, es %q", code, name, p[0], verbs(p[0]), verbs(p[1]))
				}
			}
			if strings.HasSuffix(name, "F") && f.Kind() == reflect.String && verbs(f.String()) == "" {
				t.Errorf("%s: %s is a format without verbs", code, name)
			}
		}
	}
}

// Nothing moves = no new image; the meters repaint only their base (MIXER.md §17); a change draws.
func TestRegionRepaintAndNoImage(t *testing.T) {
	h, lv := drawHarness(t)
	if !h.u.Tick() {
		t.Fatal("the first image")
	}
	prev := append([]byte(nil), h.u.Image().Pix...)
	h.advance(60 * time.Millisecond)
	if h.u.Tick() {
		t.Fatal("an image with nothing moving")
	}
	h.advance(time.Second) // the redraw of every second, same content
	if h.u.Tick() {
		t.Fatal("an image after the 1 s redraw with the same content")
	}
	if !bytes.Equal(prev, h.u.Image().Pix) {
		t.Fatal("the image changed")
	}
	// A meter moves: only the meters' rectangles change
	lv.pgm = -3
	h.advance(60 * time.Millisecond)
	if !h.u.Tick() {
		t.Fatal("the meter moved and no image")
	}
	img := h.u.Image()
	m := h.lay.PGMMeter
	allowed := image.Rect(m.X-1, m.Y-1, m.X+m.W+2, m.Y+m.H+2)
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			i := img.PixOffset(x, y)
			if !bytes.Equal(img.Pix[i:i+4], prev[i:i+4]) && !image.Pt(x, y).In(allowed) {
				t.Fatalf("(%d,%d) changed outside the meter", x, y)
			}
		}
	}
	// A touch draws everything (the highlight)
	x, y := h.at("full")
	h.u.OnTouch("down", x, y, 0)
	if !h.u.Tick() {
		t.Fatal("a touch without an image")
	}
}

// The drawing's speed (ENGINE_GO.md §5: ≤ 50 ms on the board; the Python one takes 14-17 ms, 40-50 with
// Settings). Logged; fails only far beyond the goal.
func TestDrawSpeed(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(h *harness)
	}{
		{"video", func(h *harness) {}},
		{"audio", func(h *harness) {
			st := *h.u.st
			g := -6.0
			st.Audio = &AudioStatus{Mode: "single", Sources: []AudioInput{{ID: "i2s", Name: "HDMI", State: "live", GainDb: &g, VideoSlot: 1}},
				Phones: &PhonesStatus{On: true, GainDb: ip(-12), State: "on"}}
			h.u.SetStatus(&st)
			h.u.view = "audio"
		}},
		{"settings", func(h *harness) {
			h.wifiState()
			h.net.snap.Wifi = &WifiCreds{SSID: "EnhancedIRL-0000", Password: "Ejempl0lI1NoEsLaReal", QR: testQR()}
			h.u.settingsOpen = true
		}},
		{"networks", func(h *harness) { h.wifiState(); h.u.settingsOpen = true; h.u.openNetworks() }},
		{"keyboard", func(h *harness) { h.wifiState(); h.u.settingsOpen = true; h.u.openKeyboard("Nueva", "") }},
	} {
		h, _ := drawHarness(t)
		c.set(h)
		h.u.Tick()
		const n = 20
		var worst time.Duration
		t0 := time.Now()
		for range n {
			h.u.Dirty = true
			t1 := time.Now()
			h.u.Tick()
			worst = max(worst, time.Since(t1))
		}
		avg := time.Since(t0) / n
		t.Logf("%-9s full drawing: average %v, worst %v", c.name, avg.Round(10*time.Microsecond), worst.Round(10*time.Microsecond))
		if avg > 50*time.Millisecond && !raceEnabled {
			t.Errorf("%s: %v per drawing", c.name, avg)
		}
	}
}

func testQR() []string {
	rows := make([]string, 29)
	for i := range rows {
		b := make([]byte, 29)
		for j := range b {
			b[j] = "01"[(i*7+j*3)%2]
		}
		rows[i] = string(b)
	}
	return rows
}

func BenchmarkDrawVideo(b *testing.B) {
	h, _ := drawHarness(b)
	for b.Loop() {
		h.u.Dirty = true
		h.u.Tick()
	}
}

func BenchmarkMeters(b *testing.B) {
	h, lv := drawHarness(b)
	h.u.Tick()
	for i := 0; b.Loop(); i++ {
		lv.pgm = -float64(i % 40)
		h.advance(60 * time.Millisecond)
		h.u.Tick()
	}
}

// The header's link, in the screen's language: SRT; SRTLA with one uplink (singular) or more (plural).
func TestLinkText(t *testing.T) {
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	h, _ := drawHarness(t)
	want := map[string][3]string{"es": {"SRT", "SRTLA · 1 enlace", "SRTLA · 2 enlaces"},
		"en": {"SRT", "SRTLA · 1 link", "SRTLA · 2 links"}, "fr": {"SRT", "SRTLA · 1 lien", "SRTLA · 2 liens"}}
	for _, lang := range Languages {
		h.u.SetLanguage(lang)
		for i, info := range []struct {
			tr string
			n  int
		}{{"srt", 0}, {"srtla", 1}, {"srtla", 2}} {
			if got := h.u.linkText(info.tr, info.n); got != want[lang][i] {
				t.Errorf("%s %v: %q, want %q", lang, info, got, want[lang][i])
			}
		}
		if got := h.u.linkText("srtla", 0); got != sprintf(Lang(lang).LinkSRTLAF, 0) {
			t.Errorf("%s: no uplinks: %q", lang, got)
		}
	}
	h.u.LinkInfo = func() (string, int) { return "srtla", 2 }
	shown = shown[:0]
	h.u.Dirty = true
	h.u.Tick()
	found := false
	for _, s := range shown {
		found = found || s == want[h.u.Tx.Code][2]
	}
	if !found {
		t.Fatalf("the header: %q", shown)
	}
}
