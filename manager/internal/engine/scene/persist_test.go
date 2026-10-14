package scene

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// pythonFile was written by the Python engine (json.dumps): ASCII-escaped, ", " and ": ", audio keys
// from a later phase, and a gains float that must survive untouched.
const pythonFile = `{"main": "usb-C\u00e1m_\ud83d\ude00", "secondary": "hdmi", "layout": "pip", "transition": "fade", "hidden": ["a\u007fb\b\f\t", "test", "zz"], "pip": {"corner": "tl", "size": 25}, "pause": {"on": true, "text": "Caf\u00e9, \u00f1u \u2014 100 %", "mute": false}, "configMain": "hdmi", "locked": true, "audio": "alsa:Logi", "configAudio": null, "audioSettings": {"follow": true, "delays": {"i2s": 120}, "links": {"hdmi": null}, "mode": "mix", "mix": {"i2s": true}, "gains": {"i2s": -3.5, "alsa:X": 1e-05}, "phones": {"on": true, "gainDb": -12}}}`

// pythonDefaults is json.dumps of a default scene with configMain and locked, no audio bus.
const pythonDefaults = `{"main": "x", "secondary": null, "layout": "full", "transition": "cut", "hidden": [], "pip": {"corner": "br", "size": 33}, "pause": {"on": false, "text": "Volvemos enseguida", "mute": true}, "configMain": "x", "locked": false}`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPersistPythonFileRoundTrip(t *testing.T) {
	sv := Load(writeFile(t, pythonFile), "hdmi")
	if !sv.Restored || !sv.Locked {
		t.Fatalf("restored %v locked %v", sv.Restored, sv.Locked)
	}
	want := Snapshot{Main: strp("usb-Cám_😀"), Secondary: strp("hdmi"), Layout: "pip", Transition: "fade",
		Hidden: []string{"a\x7fb\b\f\t", "test", "zz"}, Pip: Pip{"tl", 25}, Pause: Pause{true, "Café, ñu — 100 %", false}}
	if got := sv.Scene.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	out := filepath.Join(t.TempDir(), "sub", "dir", FileName) // mkdir -p
	if err := Save(out, "hdmi", &sv.Scene, sv.Locked, sv.Extra); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pythonFile { // byte for byte what Python writes, audio included
		t.Fatalf("Go wrote\n%s\nwant\n%s", got, pythonFile)
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind: %v", err)
	}
}

func TestPersistDefaultsLikePython(t *testing.T) {
	s := New("x")
	got, err := Marshal("x", &s, false, Extra{})
	if err != nil || string(got) != pythonDefaults {
		t.Fatalf("got %s (%v)", got, err)
	}
}

func TestPersistConfigMainChanged(t *testing.T) {
	// Another configured main source: only pip, pause and locked survive; audio is passed through
	sv := Load(writeFile(t, pythonFile), "test")
	if sv.Restored || !sv.Locked {
		t.Fatalf("restored %v locked %v", sv.Restored, sv.Locked)
	}
	want := New("test")
	want.Pip = Pip{"tl", 25}
	want.Pause = Pause{true, "Café, ñu — 100 %", false}
	if !reflect.DeepEqual(sv.Scene.Snapshot(), want.Snapshot()) {
		t.Fatalf("got %+v", sv.Scene.Snapshot())
	}
	if string(sv.Extra.ConfigAudio) != "null" || string(sv.Extra.Audio) != `"alsa:Logi"` || sv.Extra.AudioSettings == nil {
		t.Fatalf("extra %+v", sv.Extra)
	}
}

func TestPersistJunkRejected(t *testing.T) {
	defaults := Saved{Scene: New("hdmi")}
	for name, content := range map[string]string{
		"top-level list": `[{"configMain": "hdmi", "main": "b", "locked": true}]`, // Python crashes here
		"null":           `null`,
		"string":         `"hdmi"`,
		"not json":       `{"main": `,
		"empty":          ``,
		"bom":            "\xef\xbb\xbf{\"configMain\": \"hdmi\", \"locked\": true}",
		"invalid utf-8":  "{\"configMain\": \"hdmi\", \"locked\": true, \"main\": \"\xff\"}",
		"nan":            `{"configMain": "hdmi", "locked": true, "x": NaN}`,
	} {
		if got := Load(writeFile(t, content), "hdmi"); !reflect.DeepEqual(got, defaults) {
			t.Errorf("%s: got %+v", name, got)
		}
	}
	if got := Load(filepath.Join(t.TempDir(), "missing.json"), "hdmi"); !reflect.DeepEqual(got, defaults) {
		t.Errorf("missing file: %+v", got)
	}
	if got := Load("", "hdmi"); !reflect.DeepEqual(got, defaults) {
		t.Errorf("no path: %+v", got)
	}
}

func TestPersistJunkFieldsIgnored(t *testing.T) {
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'x'
	}
	d := map[string]any{
		"configMain": "hdmi", "main": string(long), "secondary": 5, "layout": "zoom", "transition": "wipe",
		"hidden":  []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"},
		"pip":     map[string]any{"corner": "middle", "size": []int{1}}, // a list size crashes Python
		"pause":   map[string]any{"on": "yes", "text": "a<b", "mute": 0},
		"locked":  "yes",
		"unknown": 1,
	}
	data, _ := json.Marshal(d)
	sv := Load(writeFile(t, string(data)), "hdmi")
	if !reflect.DeepEqual(sv.Scene.Snapshot(), defaultSnapshot("hdmi")) || sv.Locked || !sv.Restored {
		t.Fatalf("got %+v", sv)
	}
	out, _ := Marshal("hdmi", &sv.Scene, sv.Locked, sv.Extra)
	if string(out) != `{"main": "hdmi", "secondary": null, "layout": "full", "transition": "cut", "hidden": [], "pip": {"corner": "br", "size": 33}, "pause": {"on": false, "text": "Volvemos enseguida", "mute": true}, "configMain": "hdmi", "locked": false}` {
		t.Fatalf("unknown keys or junk written back: %s", out)
	}
}

func TestPersistLimits(t *testing.T) {
	id256 := make([]rune, 256)
	for i := range id256 {
		id256[i] = 'é' // runes, not bytes: Python counts code points
	}
	d := map[string]any{"configMain": "hdmi", "main": string(id256), "secondary": nil,
		"hidden": []string{"1", "2", "3", "4", "5", "6", "7", "7"}, "pip": map[string]any{"size": 33.0}}
	data, _ := json.Marshal(d)
	sv := Load(writeFile(t, string(data)), "hdmi")
	if sv.Scene.Main != string(id256) || sv.Scene.Secondary != "" || len(sv.Scene.Hidden()) != 7 {
		t.Fatalf("main %d runes, secondary %q, hidden %v", len([]rune(sv.Scene.Main)), sv.Scene.Secondary, sv.Scene.Hidden())
	}
}

func TestPythonStyleEscapes(t *testing.T) {
	s := New(`q"\` + "\u2028<&>\x01")
	got, err := Marshal("c", &s, false, Extra{})
	if err != nil {
		t.Fatal(err)
	}
	// json.dumps(...) of the same main: \" \\ \u2028 kept, < & > raw, control as \u0001
	want := `{"main": "q\"\\\u2028<&>\u0001", `
	if string(got[:len(want)]) != want {
		t.Fatalf("got %s", got)
	}
}

func defaultSnapshot(main string) Snapshot {
	s := New(main)
	return s.Snapshot()
}
