package splash

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/opentype"
)

func TestFormatPack(t *testing.T) {
	cases := []struct {
		name string
		f    Format
		want []byte // pixel (0x12, 0x34, 0x56) in memory
	}{
		// tegra_fb at 32 bpp: whatever FBIOGET_VSCREENINFO reads rules; these are the two usual orders
		{"BGRA", Format{32, Channel{16, 8}, Channel{8, 8}, Channel{0, 8}, Channel{24, 8}}, []byte{0x56, 0x34, 0x12, 0xff}},
		{"RGBA", Format{32, Channel{0, 8}, Channel{8, 8}, Channel{16, 8}, Channel{24, 8}}, []byte{0x12, 0x34, 0x56, 0xff}},
		{"XRGB", Format{32, Channel{16, 8}, Channel{8, 8}, Channel{0, 8}, Channel{}}, []byte{0x56, 0x34, 0x12, 0x00}},
		{"RGB565", Format{16, Channel{11, 5}, Channel{5, 6}, Channel{0, 5}, Channel{}}, []byte{0xaa, 0x11}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.f.Validate(); err != nil {
				t.Fatal(err)
			}
			img := image.NewRGBA(image.Rect(0, 0, 2, 1))
			img.SetRGBA(1, 0, color.RGBA{0x12, 0x34, 0x56, 0xff})
			bpp := c.f.BytesPerPixel()
			dst := make([]byte, bpp)
			c.f.Encode(dst, bpp, img, image.Rect(1, 0, 2, 1))
			if string(dst) != string(c.want) {
				t.Fatalf("% x, want % x", dst, c.want)
			}
		})
	}
}

func TestFormatValidate(t *testing.T) {
	bad := []Format{
		{8, Channel{0, 3}, Channel{3, 3}, Channel{6, 2}, Channel{}},       // palette
		{32, Channel{0, 10}, Channel{10, 10}, Channel{20, 10}, Channel{}}, // 10 bits
		{32, Channel{30, 8}, Channel{8, 8}, Channel{0, 8}, Channel{}},     // outside the pixel
	}
	for _, f := range bad {
		if f.Validate() == nil {
			t.Errorf("%+v should be rejected", f)
		}
	}
}

func TestGeometryValidate(t *testing.T) {
	f := Format{32, Channel{16, 8}, Channel{8, 8}, Channel{0, 8}, Channel{24, 8}}
	ok := Geometry{Width: 1024, Height: 600, Stride: 4096, MemLen: 4096 * 600, Format: f}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, g := range []Geometry{
		{Width: 0, Height: 600, Stride: 4096, MemLen: 1 << 24, Format: f},
		{Width: 1024, Height: 600, Stride: 2048, MemLen: 1 << 24, Format: f},
		{Width: 1024, Height: 600, Stride: 4096, Offset: 4096, MemLen: 4096 * 600, Format: f},
	} {
		if g.Validate() == nil {
			t.Errorf("%+v should be rejected", g)
		}
	}
}

func TestLayout(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}, {640, 480}, {800, 480}, {3840, 2160}} {
		w, h := s[0], s[1]
		r.Layout(w, h)
		screen := image.Rect(0, 0, w, h)
		logo, bar := r.LogoBounds(), r.BarBounds()
		if !logo.In(screen) || !bar.In(screen) || logo.Empty() || bar.Empty() {
			t.Fatalf("%dx%d: logo %v bar %v off screen", w, h, logo, bar)
		}
		if bar.Min.Y <= logo.Max.Y {
			t.Errorf("%dx%d: bar %v overlaps logo %v", w, h, bar, logo)
		}
		if lw := logo.Dx(); lw > w*6/10+1 || lw < w/4 {
			t.Errorf("%dx%d: logo width %d", w, h, lw)
		}
		if r.textBase >= h {
			t.Errorf("%dx%d: text off screen (baseline %d)", w, h, r.textBase)
		}
	}
}

func TestFrames(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	r.Layout(1024, 600)
	ready := r.Frame(SceneReady)
	if got := ready.RGBAAt(0, 0); got != colBackground {
		t.Fatalf("corner %v, want the background", got)
	}
	l := r.LogoBounds()
	if countNot(ready, l, colBackground) < l.Dx()*l.Dy()/10 {
		t.Fatal("logo not visible")
	}
	// Scenes with text differ from "ready" only below the logo
	reboot := r.Frame(SceneReboot)
	below := image.Rect(0, l.Max.Y, 1024, 600)
	if countDiff(ready, reboot, below) == 0 {
		t.Fatal("reboot text does not appear")
	}
	if countDiff(ready, reboot, image.Rect(0, 0, 1024, l.Max.Y)) != 0 {
		t.Fatal("the text overlaps the logo")
	}
	if countDiff(r.Frame(SceneReboot), r.Frame(ScenePoweroff), below) == 0 {
		t.Fatal("reboot and poweroff look the same")
	}
}

func TestEllipsisGlyph(t *testing.T) {
	r, _ := NewRenderer()
	face, err := opentype.NewFace(r.font, &opentype.FaceOptions{Size: 20, DPI: 72})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := face.GlyphAdvance('…'); !ok {
		t.Fatal("the font has no \"…\"")
	}
}

func TestDrawBar(t *testing.T) {
	r, _ := NewRenderer()
	r.Layout(1024, 600)
	b := r.BarBounds()
	dst := image.NewRGBA(image.Rect(0, 0, 1024, 600))
	for _, c := range []struct {
		at     time.Duration
		accent bool
	}{{0, false}, {BarPeriod / 2, true}, {BarPeriod + BarPeriod/2, true}} {
		r.DrawBar(dst, c.at)
		if got := countColor(dst, b, colAccent) > 0; got != c.accent {
			t.Errorf("t=%v: bright segment %v, want %v", c.at, got, c.accent)
		}
		if countColor(dst, b, colTrack) == 0 {
			t.Errorf("t=%v: no track", c.at)
		}
		// only touches its rectangle
		if countNot(dst, image.Rect(0, 0, 1024, b.Min.Y), color.RGBA{}) != 0 {
			t.Fatal("the bar paints outside its rectangle")
		}
	}
}

func TestConsoleHasFB(t *testing.T) {
	dir := t.TempDir()
	mk := func(n, name, bind string) {
		d := filepath.Join(dir, n)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "name"), []byte(name+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "bind"), []byte(bind+"\n"), 0o644)
	}
	mk("vtcon0", "(S) dummy device", "0")
	if ConsoleHasFB(dir) {
		t.Fatal("dummy only: no console on the framebuffer")
	}
	mk("vtcon1", "(M) frame buffer device", "0")
	if ConsoleHasFB(dir) {
		t.Fatal("fbcon not bound")
	}
	mk("vtcon1", "(M) frame buffer device", "1") // what the board has today
	if !ConsoleHasFB(dir) {
		t.Fatal("fbcon bound but not detected")
	}
}

func readJetsonConf(t *testing.T) string {
	b, err := os.ReadFile("testdata/extlinux-jetson.conf") // copy of the board's (12-oct)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallEntry(t *testing.T) {
	orig := readJetsonConf(t)
	out, prev, err := InstallEntry(orig, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prev != (BootPrev{Default: "tc358743", Timeout: "30"}) {
		t.Fatalf("previous state %+v", prev)
	}
	lines := splitLines(out)
	if d, _ := globalKey(lines, "DEFAULT"); d != EntryLabel {
		t.Fatalf("DEFAULT %q", d)
	}
	if to, _ := globalKey(lines, "TIMEOUT"); to != "10" {
		t.Fatalf("TIMEOUT %q", to)
	}
	body, err := labelBody(lines, EntryLabel)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := labelBody(splitLines(orig), "tc358743")
	if len(body) != len(base) {
		t.Fatalf("body %v, base %v", body, base)
	}
	for i := range base {
		want := base[i]
		if strings.HasPrefix(want, "APPEND") {
			want += " fbcon=map:9"
		}
		if body[i] != want {
			t.Errorf("line %d: %q, want %q", i, body[i], want)
		}
	}
	// The entry that boots today is not touched
	origTC, _ := labelBody(splitLines(orig), "tc358743")
	nowTC, _ := labelBody(lines, "tc358743")
	if strings.Join(origTC, "|") != strings.Join(nowTC, "|") {
		t.Fatal("the tc358743 entry was touched")
	}

	// Idempotent: reinstalling (DEFAULT already ours) with the saved state gives the same result
	again, prev2, err := InstallEntry(out, &prev)
	if err != nil || again != out || prev2 != prev {
		t.Fatalf("reinstalling changes the file (err %v)", err)
	}
	if _, _, err := InstallEntry(out, nil); err == nil {
		t.Fatal("reinstalling without previous state should fail")
	}

	// Undo gives the file back as it was
	back, err := UninstallEntry(out, &prev)
	if err != nil {
		t.Fatal(err)
	}
	if back != orig {
		t.Fatalf("undo does not restore the original:\n%s", back)
	}
	if _, err := UninstallEntry(out, nil); err == nil {
		t.Fatal("undo without previous state and DEFAULT ours should fail")
	}
}

func TestUninstallRespectsNewDefault(t *testing.T) {
	out, prev, err := InstallEntry(readJetsonConf(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Someone changed DEFAULT to primary after installing
	out = strings.Replace(out, "DEFAULT "+EntryLabel, "DEFAULT primary", 1)
	back, err := UninstallEntry(out, &prev)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := globalKey(splitLines(back), "DEFAULT"); d != "primary" {
		t.Fatalf("DEFAULT %q, want primary", d)
	}
	if strings.Contains(back, EntryLabel) {
		t.Fatal("the entry is still there")
	}
}

func TestInstallWithoutTimeout(t *testing.T) {
	conf := "DEFAULT a\n\nLABEL a\n  LINUX /boot/Image\n  APPEND ${cbootargs} quiet fbcon=map:9\n"
	out, prev, err := InstallEntry(conf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Timeout != "" || !strings.HasPrefix(out, "TIMEOUT 10\nDEFAULT eirl-splash\n") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Count(out, "fbcon=map:9") != 2 { // the base's and ours, not duplicated
		t.Fatalf("fbcon=map:9 duplicated:\n%s", out)
	}
	back, err := UninstallEntry(out, &prev)
	if err != nil || back != conf {
		t.Fatalf("undo (err %v):\n%s", err, back)
	}
}

func TestInstallErrors(t *testing.T) {
	for name, conf := range map[string]string{
		"no DEFAULT":     "LABEL a\n  LINUX /x\n  APPEND y\n",
		"broken DEFAULT": "DEFAULT b\nLABEL a\n  LINUX /x\n  APPEND y\n",
		"no APPEND":      "DEFAULT a\nLABEL a\n  LINUX /x\n",
		"broken block":   "DEFAULT a\nLABEL a\n  LINUX /x\n  APPEND y\n" + entryBegin + "\nLABEL eirl-splash\n",
	} {
		if _, _, err := InstallEntry(conf, nil); err == nil {
			t.Errorf("%s: should fail", name)
		}
	}
}

func countNot(img *image.RGBA, r image.Rectangle, c color.RGBA) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y) != c {
				n++
			}
		}
	}
	return n
}

func countColor(img *image.RGBA, r image.Rectangle, c color.RGBA) int {
	return r.Dx()*r.Dy() - countNot(img, r, c)
}

func countDiff(a, b *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				n++
			}
		}
	}
	return n
}

// The reboot/poweroff texts follow the language of the app's device.json; anything odd falls back to English
func TestDeviceLanguage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "device.json")
	if DeviceLanguage(p) != "" {
		t.Error("missing file")
	}
	os.WriteFile(p, []byte(`{"v":1,"language":"fr","name":"x"}`), 0o640)
	if got := DeviceLanguage(p); got != "fr" {
		t.Errorf("fr: %q", got)
	}
	os.WriteFile(p, []byte(`{"language":"de"}`), 0o640)
	if got := DeviceLanguage(p); got != "" {
		t.Errorf("unsupported language: %q", got)
	}
	if DeviceLanguage(dir) != "" {
		t.Error("a directory")
	}
}
