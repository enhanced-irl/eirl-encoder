package appapi

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The Recording page's reads and actions (RECORDING.md §6.2) against the fake app.

func storageBody() map[string]any {
	return map[string]any{"state": "ready", "seq": 7, "device": "sda", "model": "DataTraveler 3.0", "fs": "exfat",
		"label": "EIRL-REC", "sizeBytes": 62008590336, "readOnly": false, "dirty": false, "writeMBps": 31.2}
}

func recordingBody() map[string]any {
	return map[string]any{"mode": "manual", "quality": "high",
		"qualities": []any{
			map[string]any{"id": "standard", "codec": "h264", "videoKbps": 15000, "audioKbps": 192},
			map[string]any{"id": "high", "codec": "h264", "videoKbps": 25000, "audioKbps": 256},
			map[string]any{"id": "max", "codec": "h264", "videoKbps": 40000, "audioKbps": 256},
			map[string]any{"id": "efficient", "codec": "h265", "videoKbps": 20000, "audioKbps": 256},
		},
		"record":  map[string]any{"mode": "off", "state": "off"},
		"storage": storageBody()}
}

func TestRecordingRead(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /recording", 200, recordingBody())
	d.PollRecording()
	s := d.AppSnapshot()
	r := s.Recording
	if s.RecordingAPI != ui.Yes || r == nil || r.Mode != "manual" || r.Quality != "high" || len(r.Qualities) != 4 {
		t.Fatalf("%+v", r)
	}
	if q := r.QualityOf("efficient"); q == nil || *q != (ui.RecQuality{ID: "efficient", Codec: "h265", VideoKbps: 20000,
		AudioKbps: 256}) {
		t.Errorf("%+v", q)
	}
	want := ui.StorageInfo{State: "ready", Seq: 7, Device: "sda", Model: "DataTraveler 3.0", FS: "exfat", Label: "EIRL-REC",
		SizeBytes: 62008590336, WriteMBps: 31.2}
	if r.Storage != want {
		t.Errorf("%+v", r.Storage)
	}
	// An app without the route (403 over the socket, 404): the page says so, asked again only rarely
	a.drop("GET /recording")
	d.PollRecording()
	if s := d.AppSnapshot(); s.RecordingAPI != ui.No || s.Recording != nil {
		t.Fatalf("%+v", s.Recording)
	}
}

// Whatever does not fit is left out: closed lists, the disk's names by the rule of a device's name.
func TestRecordingValidated(t *testing.T) {
	long := strings.Repeat("é", BTNameMax+1)
	s, ok := storageInfo(js(t, mustJSON(t, map[string]any{"state": "unsupported", "reason": "fs_zfs", "seq": -1,
		"device": "mmcblk0", "anotherDisk": "sdb/../x", "model": long, "label": "bad\x07label", "fs": "Weird FS",
		"sizeBytes": 1.5, "readOnly": "yes", "writeMBps": -3, "dir": "/media/eirl-rec", "uuid": "1234-ABCD"})))
	if !ok || !reflect.DeepEqual(s, ui.StorageInfo{State: "unsupported", Model: strings.Repeat("é", BTNameMax)}) {
		t.Fatalf("%+v", s)
	}
	for _, x := range []string{`{}`, `{"state": "mounted"}`, `[]`, `"ready"`} {
		if _, ok := storageInfo(js(t, x)); ok {
			t.Errorf("%s: a storage object", x)
		}
	}
	body := recordingBody()
	body["quality"] = "ultra"
	body["qualities"] = []any{map[string]any{"id": "high", "codec": "vp9", "videoKbps": "25000"},
		map[string]any{"id": "high", "codec": "h264", "videoKbps": 1}, map[string]any{"id": "8k"}}
	r := recordingInfo(js(t, mustJSON(t, body)))
	if r == nil || r.Quality != "" || len(r.Qualities) != 1 || r.Qualities[0] != (ui.RecQuality{ID: "high"}) {
		t.Fatalf("%+v", r)
	}
	for _, x := range []map[string]any{{"mode": "always", "storage": storageBody()}, {"mode": "manual"},
		{"mode": "manual", "storage": map[string]any{"state": "x"}}} {
		if recordingInfo(js(t, mustJSON(t, x))) != nil {
			t.Errorf("%v: a view", x)
		}
	}
}

func TestRecordingActions(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /recording", 200, recordingBody())
	d.PollRecording()
	view := recordingBody()
	view["mode"] = "with_output"
	a.route("PUT /recording", 200, view)
	ejected := map[string]any{"state": "ejected", "seq": 8}
	a.route("POST /storage/eject", 200, ejected)
	formatted := storageBody()
	formatted["seq"], formatted["fs"] = 9, "exfat"
	a.route("POST /storage/format", 200, formatted)
	cases := []struct {
		act          ui.AppAction
		method, path string
		body         string
		check        func(r *ui.RecordingInfo) bool
	}{
		{ui.AppAction{What: "rec_settings", Body: map[string]any{"mode": "with_output"}}, "PUT", "/api/v1/recording",
			`{"mode":"with_output"}`, func(r *ui.RecordingInfo) bool { return r.Mode == "with_output" }},
		{ui.AppAction{What: "rec_settings", Body: map[string]any{"quality": "max", "mode": "manual"}}, "PUT",
			"/api/v1/recording", `{"mode":"manual","quality":"max"}`, nil},
		{ui.AppAction{What: "rec_eject"}, "POST", "/api/v1/storage/eject", `{}`,
			func(r *ui.RecordingInfo) bool { return r.Storage.State == "ejected" && r.Mode == "with_output" }},
		{ui.AppAction{What: "rec_format", Body: map[string]any{"seq": int64(8)}}, "POST", "/api/v1/storage/format",
			`{"seq":8}`, func(r *ui.RecordingInfo) bool { return r.Storage.State == "ready" && r.Storage.Seq == 9 }},
	}
	for _, c := range cases {
		res := run(t, d, c.act)
		last := a.last()
		a.mu.Lock()
		raw := string(a.raws[len(a.raws)-1])
		a.mu.Unlock()
		if res == nil || !res.OK || last.method != c.method || last.uri != c.path || raw != c.body {
			t.Errorf("%s: %+v %s %s %s", c.act.What, res, last.method, last.uri, raw)
		}
		// The answer is the page's at once
		if r := d.AppSnapshot().Recording; c.check != nil && (r == nil || !c.check(r)) {
			t.Errorf("%s: %+v", c.act.What, r)
		}
	}
	// A refusal: the app's code and message (in the device language)
	a.route("POST /storage/format", 409, map[string]any{"error": "disk_changed",
		"message": "El disco ha cambiado: vuelve a mirarlo antes de formatear"})
	res := run(t, d, ui.AppAction{What: "rec_format", Body: map[string]any{"seq": int64(9)}})
	if res.OK || res.Status != 409 || res.Code != "disk_changed" || !strings.HasPrefix(res.Message, "El disco ha cambiado") {
		t.Fatalf("%+v", res)
	}
}

// What the screen asks is checked before it goes: anything else is not sent.
func TestRecordingActionsChecked(t *testing.T) {
	a, d := newDevice(t)
	bad := []ui.AppAction{
		{What: "rec_settings"},
		{What: "rec_settings", Body: map[string]any{"mode": "always"}},
		{What: "rec_settings", Body: map[string]any{"quality": "ultra"}},
		{What: "rec_settings", Body: map[string]any{"mode": "manual", "dir": "/tmp"}},
		{What: "rec_settings", ID: "x", Body: map[string]any{"mode": "manual"}},
		{What: "rec_eject", Body: map[string]any{"force": true}},
		{What: "rec_eject", Value: "sdb"},
		{What: "rec_format"},
		{What: "rec_format", Body: map[string]any{"seq": 7}}, // an int, not the seq the client keeps (int64)
		{What: "rec_format", Body: map[string]any{"seq": int64(-1)}},
		{What: "rec_format", Body: map[string]any{"seq": int64(7), "fs": "ntfs"}},
		{What: "rec_format", Body: map[string]any{"seq": int64(7)}, Secrets: map[string][]byte{"x": []byte("y")}},
	}
	for _, act := range bad {
		res := run(t, d, act)
		if res == nil || res.OK || res.Status != 400 || res.Code != "bad_request" {
			t.Errorf("%+v: %+v", act, res)
		}
	}
	if seen := a.allSeen(); len(seen) != 0 {
		t.Fatalf("sent: %+v", seen)
	}
	if recTimeout("rec_format", ActionTimeout) < 2*time.Minute || recTimeout("rec_eject", ActionTimeout) < 90*time.Second ||
		recTimeout("rec_settings", ActionTimeout) != ActionTimeout {
		t.Error("the timeouts")
	}
}

// Asked only while the page is on the screen.
func TestRecordingCadence(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /device", 200, deviceBody("es"))
	a.route("GET /recording", 200, recordingBody())
	d.Start()
	defer d.Stop()
	asked := func() int {
		n := 0
		for _, r := range a.allSeen() {
			if r.uri == "/api/v1/recording" {
				n++
			}
		}
		return n
	}
	time.Sleep(200 * time.Millisecond)
	if asked() != 0 {
		t.Fatal("asked with the page not on the screen")
	}
	d.Watch(ui.AppWatch{Recording: true})
	for t0 := time.Now(); d.AppSnapshot().Recording == nil; time.Sleep(10 * time.Millisecond) {
		if time.Since(t0) > 3*time.Second {
			t.Fatal("not asked when the page opened")
		}
	}
}
