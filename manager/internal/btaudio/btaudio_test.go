package btaudio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksEverything(t *testing.T) {
	in := `{"v":1,
	 "out":{"addr":"38:18:4C:12:A0:01","name":"WH-1000XM4 <b>","profile":"a2dp","delayMs":5000},
	 "in":[{"addr":"A4:C3:F0:55:21:02","name":"Pixel 8","profile":"a2dp"},
	       {"addr":"A4:C3:F0:55:21:02","name":"twice","profile":"sco"},
	       {"addr":"a4:c3:f0:55:21:03","name":"lower case","profile":"a2dp"},
	       {"addr":"50:C2:ED:7B:10:03","name":"","profile":"sco","delayMs":-4},
	       {"addr":"50:C2:ED:7B:10:04","name":"x","profile":"hw:0,0"}]}`
	f, ok := Parse([]byte(in))
	if !ok {
		t.Fatal("not parsed")
	}
	if f.Out == nil || f.Out.Name != "WH-1000XM4 b" || f.Out.DelayMs != MaxDelayMs {
		t.Errorf("out %+v", f.Out)
	}
	if len(f.In) != 2 || f.In[0].Addr != "A4:C3:F0:55:21:02" || f.In[1].Name != "50:C2:ED:7B:10:03" || f.In[1].DelayMs != 0 {
		t.Errorf("in %+v", f.In)
	}
	if ALSADevice(*f.Out) != "bluealsa:DEV=38:18:4C:12:A0:01,PROFILE=a2dp" {
		t.Error(ALSADevice(*f.Out))
	}
	for _, bad := range []string{``, `{}`, `{"v":2,"out":null,"in":[]}`, `[1]`, strings.Repeat(" ", MaxSize+1)} {
		if f, ok := Parse([]byte(bad)); ok || f.Out != nil || len(f.In) != 0 || f.V != Version {
			t.Errorf("%q: %+v %v", bad, f, ok)
		}
	}
	// An output that does not check out is none; the device string can never be smuggled in
	f, _ = Parse([]byte(`{"v":1,"out":{"addr":"38:18:4C:12:A0:01\",PROFILE=x","profile":"a2dp"},"in":[]}`))
	if f.Out != nil {
		t.Error(f.Out)
	}
}

func TestIDsAndRoundTrip(t *testing.T) {
	if ID("38:18:4C:12:A0:01") != "bt:38184C12A001" || AddrOf("bt:38184C12A001") != "38:18:4C:12:A0:01" {
		t.Error("ids")
	}
	for _, bad := range []string{"bt:38184c12a001", "bt:38184C12A0", "alsa:38184C12A001", "bt:38184C12A0011", "bt:XX184C12A001"} {
		if AddrOf(bad) != "" {
			t.Error(bad)
		}
	}
	p := filepath.Join(t.TempDir(), "bluetooth.json")
	if Read(p).Out != nil || InputsKey(Read(p)) != "" {
		t.Error("missing file")
	}
	want := File{Out: &Device{Addr: "38:18:4C:12:A0:01", Name: "WH", Profile: ProfileSCO}}
	b := Marshal(want)
	if string(b) != `{"v":1,"out":{"addr":"38:18:4C:12:A0:01","name":"WH","profile":"sco"},"in":[]}`+"\n" {
		t.Error(string(b))
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Read(p); got.Out == nil || *got.Out != *want.Out {
		t.Errorf("%+v", got)
	}
}

func TestCleanNameKeepsScripts(t *testing.T) {
	for in, want := range map[string]string{
		"Écouteurs de Zoé":           "Écouteurs de Zoé",
		"Cámara  Sur\n":              "Cámara Sur",
		"ヘッドホン":                      "ヘッドホン",
		"AirPods\u202e evil\x00 <x>": "AirPods evil x",
		"WH-1000XM4 (Sony)":          "WH-1000XM4 (Sony)",
		"\xff\xfe":                   "",
	} {
		if got := CleanName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if n := CleanName(strings.Repeat("é", 60)); len([]rune(n)) != 48 {
		t.Error(len([]rune(n)))
	}
}

// The engine's reader: re-read when the file changes; a missing or broken one keeps the last good one (the app
// restarting must not cut what is on air).
func TestReaderHoldsThroughTheAppsRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bluetooth.json")
	r := NewReader(p)
	if f := r.Get(); f.Out != nil || len(f.In) != 0 {
		t.Error("nothing read yet", f)
	}
	in := Device{Addr: "A4:C3:F0:55:21:02", Name: "Pixel 8", Profile: ProfileA2DP}
	write := func(f File) {
		t.Helper()
		if err := os.WriteFile(p+".tmp", Marshal(f), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(p+".tmp", p); err != nil {
			t.Fatal(err)
		}
	}
	write(File{In: []Device{in}})
	if f := r.Get(); len(f.In) != 1 || InputsKey(f) != "A4:C3:F0:55:21:02/a2dp;" {
		t.Error(f)
	}
	f := r.Get()
	f.In[0].Name = "changed" // a copy
	if r.Get().In[0].Name != "Pixel 8" {
		t.Error("Get shares its slice")
	}
	os.Remove(p)
	if f := r.Get(); len(f.In) != 1 {
		t.Error("a missing file must keep the last one", f)
	}
	if err := os.WriteFile(p, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := r.Get(); len(f.In) != 1 {
		t.Error("a broken file must keep the last one", f)
	}
	write(File{})
	if f := r.Get(); len(f.In) != 0 || InputsKey(f) != "" {
		t.Error("an empty file is nothing ready", f)
	}
}
