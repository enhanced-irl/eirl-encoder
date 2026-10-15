package camera

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fakeCam is a camera as uvcvideo shows it: its controls' info and values, the menu entries it has, and the
// automatics that make a manual control inactive (the driver's own flag).
type fakeCam struct {
	info    map[uint32]Info
	val     map[uint32]int32
	menus   map[uint32][]int32
	masters map[uint32]struct {
		cid    uint32
		manual int32
	}
	failGet map[uint32]bool
	failSet map[uint32]bool
	broken  bool // every request fails (a camera that stopped answering)
	sets    []string
	gets    int
}

var errIO = errors.New("input/output error")

func (f *fakeCam) Query(cid uint32) (Info, error) {
	if f.broken {
		return Info{}, errIO
	}
	in, ok := f.info[cid]
	if !ok {
		return Info{}, ErrAbsent
	}
	if m, ok := f.masters[cid]; ok && f.val[m.cid] != m.manual {
		in.Flags |= flagInactive
	}
	return in, nil
}

func (f *fakeCam) HasMenu(cid uint32, i int32) bool { return slices.Contains(f.menus[cid], i) }

func (f *fakeCam) Get(cid uint32) (int32, error) {
	f.gets++
	if f.broken || f.failGet[cid] {
		return 0, errIO
	}
	return f.val[cid], nil
}

func (f *fakeCam) Set(cid uint32, v int32) error {
	if f.broken || f.failSet[cid] {
		return errIO
	}
	f.sets = append(f.sets, name(cid)+"="+itoa(int(v)))
	f.val[cid] = v
	return nil
}

func (f *fakeCam) Close() error { return nil }

func name(cid uint32) string {
	for _, sp := range specs {
		if sp.cid == cid {
			return sp.id
		}
	}
	if cid == cidExposureAutoPriority {
		return "exposure_auto_priority"
	}
	return "?"
}

func itoa(v int) string { b, _ := json.Marshal(v); return string(b) }

// logi is the Logi 4K Stream Edition as read on the board on Oct 14 (SOURCES.md §7.1).
func logi() *fakeCam {
	i := func(lo, hi, step, def int32) Info {
		return Info{Type: ctrlTypeInteger, Min: lo, Max: hi, Step: step, Default: def}
	}
	f := &fakeCam{
		info: map[uint32]Info{
			0x980900: i(0, 255, 1, 128), 0x980901: i(0, 255, 1, 128), 0x980902: i(0, 255, 1, 128),
			0x98090c: {Type: ctrlTypeBoolean, Min: 0, Max: 1, Step: 1, Default: 1},
			0x980913: i(0, 255, 1, 0),
			0x980918: {Type: ctrlTypeMenu, Min: 0, Max: 2, Step: 1, Default: 2},
			0x98091a: i(2000, 7500, 10, 4000), 0x98091b: i(0, 255, 1, 128), 0x98091c: i(0, 1, 1, 1),
			0x9a0901: {Type: ctrlTypeMenu, Min: 0, Max: 3, Step: 1, Default: 3},
			0x9a0902: i(3, 2047, 1, 250),
			0x9a0903: {Type: ctrlTypeBoolean, Min: 0, Max: 1, Step: 1, Default: 0},
			0x9a0908: i(-36000, 36000, 3600, 0), 0x9a0909: i(-36000, 36000, 3600, 0),
			0x9a090a: i(0, 255, 5, 0),
			0x9a090c: {Type: ctrlTypeBoolean, Min: 0, Max: 1, Step: 1, Default: 1},
			0x9a090d: i(100, 500, 1, 100),
		},
		val: map[uint32]int32{0x980900: 128, 0x980901: 128, 0x980902: 128, 0x98090c: 1, 0x980913: 0, 0x980918: 1,
			0x98091a: 3320, 0x98091b: 128, 0x98091c: 1, 0x9a0901: 3, 0x9a0902: 78, 0x9a0903: 1, 0x9a0908: 0,
			0x9a0909: 0, 0x9a090a: 10, 0x9a090c: 1, 0x9a090d: 100},
		menus: map[uint32][]int32{0x980918: {0, 1, 2}, 0x9a0901: {1, 3}},
		masters: map[uint32]struct {
			cid    uint32
			manual int32
		}{0x9a0902: {0x9a0901, 1}, 0x98091a: {0x98090c, 0}, 0x9a090a: {0x9a090c, 0}},
	}
	return f
}

func ids(list []Control) []string {
	var out []string
	for _, c := range list {
		out = append(out, c.ID)
	}
	return out
}

func TestListLogi(t *testing.T) {
	list, err := List(logi())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"brightness", "contrast", "saturation", "sharpness", "exposure_auto", "exposure", "gain",
		"backlight", "power_line", "wb_auto", "wb_temperature", "focus_auto", "focus", "zoom", "pan", "tilt"}
	if !slices.Equal(ids(list), want) {
		t.Fatalf("ids = %v", ids(list))
	}
	ea := find(list, "exposure_auto")
	if ea.Type != TypeBool || ea.Value != 1 || ea.Default != 1 || ea.auto != 3 {
		t.Errorf("exposure_auto = %+v", *ea)
	}
	if e := find(list, "exposure"); !e.Inactive || e.Value != 78 || e.Min != 3 || e.Max != 2047 {
		t.Errorf("exposure = %+v", *e)
	}
	if b := find(list, "backlight"); b.Type != TypeBool || b.Value != 1 {
		t.Errorf("backlight 0-1 should be a bool: %+v", *b)
	}
	if p := find(list, "power_line"); p.Type != TypeMenu || !slices.Equal(p.Options, []int{0, 1, 2}) || p.Value != 1 {
		t.Errorf("power_line = %+v", *p)
	}
	if w := find(list, "wb_temperature"); !w.Inactive || w.Step != 10 {
		t.Errorf("wb_temperature = %+v", *w)
	}
	if b := find(list, "brightness"); b.Inactive || b.Group != "image" {
		t.Errorf("brightness = %+v", *b)
	}
	b, _ := json.Marshal(find(list, "power_line"))
	if string(b) != `{"id":"power_line","group":"exposure","type":"menu","min":0,"max":2,"step":1,"default":2,"value":1,"options":[0,1,2]}` {
		t.Errorf("json = %s", b)
	}
}

func TestListSkips(t *testing.T) {
	f := logi()
	in := f.info[0x980900]
	in.Flags = flagReadOnly
	f.info[0x980900] = in
	f.menus[0x9a0901] = []int32{3}              // no manual: nothing to switch
	f.failGet = map[uint32]bool{0x980901: true} // one control that stalls: left out, the rest stays
	list, err := List(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"brightness", "exposure_auto", "contrast"} {
		if find(list, id) != nil {
			t.Errorf("%s should be left out", id)
		}
	}
	if find(list, "saturation") == nil {
		t.Error("saturation missing")
	}
}

func TestListNotAnswering(t *testing.T) {
	f := logi()
	f.broken = true
	if _, err := List(f); !errors.Is(err, ErrNotAnswering) {
		t.Fatalf("err = %v", err)
	}
	f = logi()
	f.failGet = map[uint32]bool{0x980900: true, 0x980901: true}
	if _, err := List(f); !errors.Is(err, ErrNotAnswering) {
		t.Fatalf("two failures: err = %v", err)
	}
}

func TestExposureAutoFallbacks(t *testing.T) {
	f := logi()
	f.menus[0x9a0901] = []int32{0, 1, 2} // no aperture priority: auto (0)
	list, _ := List(f)
	if c := find(list, "exposure_auto"); c == nil || c.auto != 0 || c.Value != 1 {
		t.Fatalf("exposure_auto = %+v", c)
	}
}

func TestCheck(t *testing.T) {
	list, _ := List(logi())
	cases := []struct {
		id   string
		v    int
		want int
		code string
	}{
		{"brightness", 300, 255, ""},
		{"brightness", -5, 0, ""},
		{"pan", 5000, 3600, ""},
		{"pan", 36001, 36000, ""},
		{"focus", 12, 0, CodeInactive},
		{"exposure_auto", 2, 0, CodeBadRequest},
		{"exposure_auto", 0, 0, ""},
		{"power_line", 3, 0, CodeBadRequest},
		{"power_line", 2, 2, ""},
		{"iris", 1, 0, CodeUnknownControl},
		{"hue", 1, 0, CodeUnknownControl}, // the Logi has no hue
	}
	for _, c := range cases {
		_, v, code := Check(list, c.id, c.v)
		if v != c.want || code != c.code {
			t.Errorf("%s=%d → %d %q, want %d %q", c.id, c.v, v, code, c.want, c.code)
		}
	}
	// A step that does not divide the range never goes past the maximum
	_, v, _ := Check([]Control{{ID: "zoom", Type: TypeInt, Min: 0, Max: 10, Step: 4}}, "zoom", 10)
	if v != 8 {
		t.Errorf("zoom = %d", v)
	}
}

func TestSet(t *testing.T) {
	f := logi()
	cur, _ := List(f)
	// As the engine runs them: each command against the controls the previous one left
	set := func(id string, v int) Result {
		r := Set(f, cur, id, v)
		if r.List != nil {
			cur = r.List
		}
		return r
	}
	r := set("exposure_auto", 0)
	if r.Code != "" || !slices.Equal(f.sets, []string{"exposure_auto=1"}) {
		t.Fatalf("r = %+v sets %v", r, f.sets)
	}
	if e := find(r.List, "exposure"); e.Inactive {
		t.Error("exposure should be active once the automatic is off")
	}
	r = set("exposure", 200)
	if r.Code != "" || r.Value != 200 || f.val[0x9a0902] != 200 {
		t.Fatalf("r = %+v", r)
	}
	r = set("exposure_auto", 1)
	if f.val[0x9a0901] != 3 || !find(r.List, "exposure").Inactive {
		t.Fatalf("back to automatic: %v", f.sets)
	}
	n := len(f.sets)
	if r = set("brightness", 128); r.Code != "" || len(f.sets) != n {
		t.Errorf("same value: no write (%v)", f.sets)
	}
	if r = set("focus", 50); r.Code != CodeInactive || len(f.sets) != n {
		t.Errorf("inactive: %+v", r)
	}
	f.failSet = map[uint32]bool{0x980900: true}
	if r = set("brightness", 10); r.Code != CodeCameraError || r.List == nil {
		t.Errorf("refused: %+v", r)
	}
	f.broken = true
	if r = set("brightness", 10); r.Code != CodeCameraError || !errors.Is(r.Err, errIO) {
		t.Errorf("not answering: %+v", r)
	}
}

func TestReset(t *testing.T) {
	f := logi()
	f.val[0x9a0901] = 1 // manual exposure, 200
	f.val[0x9a0902] = 200
	f.val[0x980900] = 40
	f.val[0x98090c] = 0 // white balance manual
	list, _ := List(f)
	r := Reset(f, list)
	if r.Code != "" {
		t.Fatal(r.Code, r.Err)
	}
	// The automatics first; then the exposure stays as it is (inactive), the rest to the defaults
	want := []string{"exposure_auto=3", "wb_auto=1", "brightness=128", "power_line=2"}
	if !slices.Equal(f.sets, want) {
		t.Fatalf("sets = %v, want %v", f.sets, want)
	}
	for _, c := range r.List {
		if !c.Inactive && c.Value != c.Default {
			t.Errorf("%s = %d, default %d", c.ID, c.Value, c.Default)
		}
	}
	// One default refused: the rest still goes, and it says so
	f = logi()
	f.val[0x980900], f.val[0x980901] = 40, 41
	f.failSet = map[uint32]bool{0x980900: true}
	list, _ = List(f)
	if r = Reset(f, list); r.Code != CodeCameraError || f.val[0x980901] != 128 {
		t.Errorf("partial reset: %+v contrast %d", r, f.val[0x980901])
	}
}

func TestApply(t *testing.T) {
	f := logi()
	saved := map[string]int{"exposure": 9999, "exposure_auto": 0, "brightness": 140, "focus": 30, "hue": 5,
		"power_line": 7}
	var fails []string
	list, err := Apply(f, saved, func(id string, _ error) { fails = append(fails, id) })
	if err != nil {
		t.Fatal(err)
	}
	// exposure_auto_priority off first; the automatic before its manual value (brought inside the range); the focus
	// stays automatic (not written); the camera has no hue; a menu value it lacks is skipped
	want := []string{"exposure_auto_priority=0", "exposure_auto=1", "brightness=140", "exposure=2047"}
	if !slices.Equal(f.sets, want) {
		t.Fatalf("sets = %v, want %v", f.sets, want)
	}
	if find(list, "exposure").Value != 2047 || find(list, "focus").Value != 10 || len(fails) != 0 {
		t.Errorf("list or fails wrong: %v", fails)
	}
	// Nothing saved: only the fps rule
	f = logi()
	if _, err := Apply(f, nil, func(string, error) {}); err != nil || !slices.Equal(f.sets, []string{"exposure_auto_priority=0"}) {
		t.Errorf("sets = %v err %v", f.sets, err)
	}
	// A capture card without the control: nothing written
	f = &fakeCam{info: map[uint32]Info{0x980900: {Type: ctrlTypeInteger, Min: -128, Max: 127, Step: 1, Default: -11}},
		val: map[uint32]int32{0x980900: -11}}
	list, err = Apply(f, nil, func(string, error) {})
	if err != nil || len(f.sets) != 0 || len(list) != 1 || list[0].Default != -11 {
		t.Errorf("ms2109: %v %v %v", list, f.sets, err)
	}
}

func TestIntArg(t *testing.T) {
	ok := []any{json.Number("12"), json.Number("-3"), float64(4), 5, int64(6), int32(7)}
	for _, v := range ok {
		if _, good := IntArg(v); !good {
			t.Errorf("%#v should be an integer", v)
		}
	}
	bad := []any{json.Number("1.5"), json.Number("1e40"), 2.5, true, "3", nil, float64(1 << 40)}
	for _, v := range bad {
		if _, good := IntArg(v); good {
			t.Errorf("%#v should not be an integer", v)
		}
	}
}

func TestStore(t *testing.T) {
	saveDelay = 10 * time.Millisecond
	defer func() { saveDelay = time.Second }()
	path := filepath.Join(t.TempDir(), FileName)
	s := NewStore(path)
	cam := "/dev/v4l/by-id/usb-046d_Logi_4K_Stream_Edition_0B2CFE13-video-index0"
	br := Control{ID: "brightness", Default: 128}
	s.Set(cam, br, 140)
	s.Set(cam, Control{ID: "exposure_auto", Default: 1}, 0)
	s.Set("hdmi", br, 1)                           // not a camera id: ignored
	s.Set(cam, Control{ID: "iris", Default: 0}, 3) // not in the table
	if v := s.Values(cam); v["brightness"] != 140 || v["exposure_auto"] != 0 || len(v) != 2 {
		t.Fatalf("values = %v", v)
	}
	s.Set(cam, br, 128) // back to its default: forgotten
	time.Sleep(50 * time.Millisecond)
	got := NewStore(path).Values(cam)
	if len(got) != 1 || got["exposure_auto"] != 0 {
		t.Fatalf("reloaded = %v", got)
	}
	s.Forget(cam)
	s.Flush()
	if v := NewStore(path).Values(cam); v != nil {
		t.Fatalf("after forget = %v", v)
	}
	// At most MaxCameras: the one changed longest ago goes
	for i := range MaxCameras + 2 {
		id := "/dev/v4l/by-id/cam" + itoa(i)
		s.Set(id, br, 1)
		s.mu.Lock()
		s.cams[id].At = int64(i)
		s.mu.Unlock()
	}
	s.Flush()
	s2 := NewStore(path)
	if len(s2.cams) != MaxCameras || s2.Values("/dev/v4l/by-id/cam0") != nil || s2.Values("/dev/v4l/by-id/cam17") == nil {
		t.Errorf("pruned wrong: %d cameras", len(s2.cams))
	}
	// A broken file: nothing, and no panic
	os.WriteFile(path, []byte("{nope"), 0o644)
	if len(NewStore(path).cams) != 0 {
		t.Error("broken file should load empty")
	}
	// In memory only
	m := NewStore("")
	m.Set(cam, br, 1)
	m.Flush()
	if m.Values(cam)["brightness"] != 1 {
		t.Error("in-memory store")
	}
}
