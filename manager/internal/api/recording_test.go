package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/storage"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
)

// recEngine: a fake engine that answers each command with answer(cmd) ("" = ok, else its error code) and keeps the
// lines it got. Its state is mixer.json in run (writeMixer).
type recEngine struct {
	mu     sync.Mutex
	cmds   []string
	answer func(cmd string) string
}

func (f *recEngine) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds)
}

func startRecEngine(t *testing.T, run string) (*mixer.Client, *recEngine) {
	t.Helper()
	dir, err := os.MkdirTemp("", "rec") // short path (Unix socket limit on macOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &recEngine{answer: func(string) string { return "" }}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				var req struct{ Cmd string }
				json.Unmarshal(sc.Bytes(), &req)
				f.mu.Lock()
				f.cmds = append(f.cmds, sc.Text())
				code := f.answer(req.Cmd)
				f.mu.Unlock()
				if code == "" {
					c.Write([]byte(`{"id":1,"ok":true}` + "\n"))
				} else {
					c.Write([]byte(`{"id":1,"ok":false,"error":"` + code + `"}` + "\n"))
				}
			}
			c.Close()
		}
	}()
	return &mixer.Client{Sock: filepath.Join(dir, "s"), StatePath: filepath.Join(run, "mixer.json"), RunDir: run}, f
}

// writeMixer: the engine's state with an output and a record object ("" = none, an older engine).
func writeMixer(t *testing.T, cl *mixer.Client, output, record string) {
	t.Helper()
	st := strings.TrimSuffix(mixerState, "}") + `,"output":` + output
	if record != "" {
		st += `,"record":` + record
	}
	if err := os.WriteFile(cl.StatePath, []byte(st+"}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	outputOff    = `{"mode":"off","state":"off"}`
	outputOn     = `{"mode":"on","state":"on"}`
	recordOff    = `{"mode":"off","state":"off","disk":{"state":"ready","fs":"exfat","label":"EIRL-REC","freeBytes":1000,"minutesLeft":200}}`
	recordingOn  = `{"mode":"on","state":"on","by":"socket","seq":1,"elapsedS":75,"kbps":25000,"droppedS":0,"file":"2026-10-28_1712.mp4","disk":{"state":"ready"}}`
	storageReady = `{"state":"ready","reason":"","seq":7,"device":"sda","partition":"sda1","model":"DataTraveler 3.0","fs":"exfat",` +
		`"label":"EIRL-REC","uuid":"1234-ABCD","sizeBytes":62008590336,"mountpoint":"/media/eirl-rec","dir":"/media/eirl-rec/EnhancedIRL",` +
		`"readOnly":false,"dirty":true,"writeMBps":31.2,"anotherDisk":"sdb","at":"2026-10-24T12:41:03+02:00"}`
)

func decodeView(t *testing.T, w *httptest.ResponseRecorder) recordingView {
	t.Helper()
	var v recordingView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return v
}

func errorOf(w *httptest.ResponseRecorder) (string, string) {
	var b map[string]any
	json.Unmarshal(w.Body.Bytes(), &b)
	kind, _ := b["error"].(string)
	msg, _ := b["message"].(string)
	return kind, msg
}

// The settings (RECORDING.md §6.1): closed lists, saved in engine.env, taken live with output_config and never with a
// restart; the quality cannot change while recording, the mode can
func TestRecordingSettingsAPI(t *testing.T) {
	e := setup(t, true)
	ck := e.login(t)
	v := decodeView(t, e.do("GET", "/api/v1/recording", "", ck))
	if v.Mode != "off" || v.Quality != "high" || len(v.Qualities) != 4 || v.Record != nil || v.Storage.State != "none" || v.Storage.Seq != 0 {
		t.Errorf("defaults %+v", v)
	}
	if w := e.do("GET", "/api/v1/recording", "", ck); !strings.Contains(w.Body.String(), `"record":null`) ||
		!strings.Contains(w.Body.String(), `{"id":"efficient","codec":"h265","videoKbps":20000,"audioKbps":256}`) {
		t.Errorf("shape %s", w.Body)
	}
	for body, field := range map[string]string{`{}`: "", `{"mode":"manual","x":1}`: "", `{"mode":true}`: "", `[]`: "",
		`{"mode":"on"}`: "mode", `{"mode":"manual;reboot"}`: "mode", `{"quality":"ultra"}`: "quality", `{"quality":""}`: "quality"} {
		w := e.do("PUT", "/api/v1/recording", body, ck)
		var b struct {
			Error  string
			Fields map[string]string
		}
		json.Unmarshal(w.Body.Bytes(), &b)
		if w.Code != 400 || field == "" && b.Error != "bad_request" || field != "" && (b.Error != "validation" || b.Fields[field] == "") {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := e.do("PUT", "/api/v1/recording", `{"mode":"manual"}`, ck, "Sec-Fetch-Site", "cross-site"); w.Code != 403 {
		t.Errorf("cross-site: %d", w.Code)
	}

	// Without the mixer: saved, no restart (the engine takes it at its next start)
	if v := decodeView(t, e.do("PUT", "/api/v1/recording", `{"mode":"manual"}`, ck)); v.Mode != "manual" || v.Quality != "high" {
		t.Errorf("saved %+v", v)
	}
	env, _ := config.ReadEnv(e.srv.Config.AppPath)
	if env["REC_MODE"] != "manual" || env["REC_QUALITY"] != "high" || len(e.unit.actions) != 0 {
		t.Errorf("engine.env %v, actions %v", env, e.unit.actions)
	}

	// With the mixer running: output_config, and still no restart when the engine cannot take it
	run := t.TempDir()
	cl, eng := startRecEngine(t, run)
	e.srv.Mixer = cl
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=off\nMIXER=1\n"), 0o644)
	writeMixer(t, cl, outputOff, recordOff)
	if v := decodeView(t, e.do("PUT", "/api/v1/recording", `{"mode":"with_output","quality":"max"}`, ck)); v.Mode != "with_output" ||
		v.Quality != "max" || v.Record == nil || v.Record.Disk.Label != "EIRL-REC" {
		t.Errorf("view %+v", v)
	}
	if got := eng.got(); len(got) != 1 || !strings.Contains(got[0], `"cmd":"output_config"`) {
		t.Errorf("to the engine %v", got)
	}
	eng.answer = func(string) string { return "restart_required" }
	if w := e.do("PUT", "/api/v1/recording", `{"quality":"efficient"}`, ck); w.Code != 200 || len(e.unit.actions) != 0 {
		t.Errorf("restart_required: %d, actions %v", w.Code, e.unit.actions)
	}
	n := len(eng.got())
	if w := e.do("PUT", "/api/v1/recording", `{"quality":"efficient"}`, ck); w.Code != 200 || len(eng.got()) != n {
		t.Errorf("nothing changed: %d, %d commands", w.Code, len(eng.got())-n)
	}

	// Recording: not the quality (the same one is fine); the mode, yes
	writeMixer(t, cl, outputOff, recordingOn)
	w := e.do("PUT", "/api/v1/recording", `{"quality":"standard"}`, ck, "Accept-Language", "es")
	if kind, msg := errorOf(w); w.Code != 409 || kind != "recording" || !strings.Contains(msg, "detén la grabación") {
		t.Errorf("quality while recording: %d %s", w.Code, w.Body)
	}
	if env, _ := config.ReadEnv(e.srv.Config.AppPath); env["REC_QUALITY"] != "efficient" {
		t.Errorf("saved anyway: %v", env["REC_QUALITY"])
	}
	writeMixer(t, cl, outputOff, `{"mode":"off","state":"starting","disk":{"state":"ready"}}`)
	if w := e.do("PUT", "/api/v1/recording", `{"quality":"standard"}`, ck); w.Code != 409 {
		t.Errorf("quality while starting: %d", w.Code)
	}
	writeMixer(t, cl, outputOff, recordingOn)
	if v := decodeView(t, e.do("PUT", "/api/v1/recording", `{"mode":"off","quality":"efficient"}`, ck)); v.Mode != "off" ||
		v.Record == nil || v.Record.ElapsedS == nil || *v.Record.ElapsedS != 75 {
		t.Errorf("mode while recording %+v", v)
	}
	if len(e.unit.actions) != 0 {
		t.Errorf("restarted: %v", e.unit.actions)
	}
}

// REC (RECORDING.md §6.2): the engine's record command, its refusals as 409 with their messages, 503 when the engine
// does not answer; also through POST /mixer
func TestRecordAPI(t *testing.T) {
	e := setup(t, true)
	ck := e.login(t)
	if w := e.do("PUT", "/api/v1/record", `{"on":true}`, ck); w.Code != 503 {
		t.Errorf("no mixer in the app: %d", w.Code)
	}
	run := t.TempDir()
	cl, eng := startRecEngine(t, run)
	e.srv.Mixer = cl
	for _, body := range []string{`{}`, `{"on":"yes"}`, `{"on":true,"quality":"max"}`, `null`} {
		if w := e.do("PUT", "/api/v1/record", body, ck); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if len(eng.got()) != 0 {
		t.Errorf("an invalid body reached the engine: %v", eng.got())
	}
	writeMixer(t, cl, outputOff, recordingOn)
	v := decodeView(t, e.do("PUT", "/api/v1/record", `{"on":true}`, ck))
	if got := eng.got(); len(got) != 1 || got[0] != `{"id":1,"cmd":"record","on":true}` {
		t.Errorf("to the engine %v", got)
	}
	if v.Record == nil || v.Record.State != "on" || v.Record.File != "2026-10-28_1712.mp4" {
		t.Errorf("view %+v", v.Record)
	}
	if w := e.do("PUT", "/api/v1/record", `{"on":false}`, ck); w.Code != 200 || !strings.HasSuffix(eng.got()[1], `"on":false}`) {
		t.Errorf("stop: %d %v", w.Code, eng.got())
	}
	for _, c := range []struct {
		engine, kind string
		code         int
		msg          string
	}{
		{"disabled", "disabled", 409, "local recording is off"},
		{"no_disk", "no_disk", 409, "connect a USB disk"},
		{"disk_full", "disk_full", 409, "the disk is full"},
		{"unsupported", "unsupported", 409, "cannot record"},
		{"unknown_command", "unsupported", 409, "cannot record"}, // an engine older than the recording
		{"timeout", "timeout", 503, "did not answer"},
		{"bad_request", "bad_request", 422, "invalid command"},
	} {
		eng.answer = func(string) string { return c.engine }
		w := e.do("PUT", "/api/v1/record", `{"on":true}`, ck, "Accept-Language", "en")
		if kind, msg := errorOf(w); w.Code != c.code || kind != c.kind || !strings.Contains(msg, c.msg) {
			t.Errorf("%s: %d %s", c.engine, w.Code, w.Body)
		}
	}
	// The same command through POST /mixer, with the engine's code as 422 and the recording's messages
	eng.answer = func(string) string { return "" }
	if w := e.do("POST", "/api/v1/mixer", `{"cmd":"record","on":true}`, ck); w.Code != 200 || !strings.Contains(w.Body.String(), `"record":{`) {
		t.Errorf("POST /mixer record: %d %s", w.Code, w.Body)
	}
	eng.answer = func(string) string { return "no_disk" }
	if w := e.do("POST", "/api/v1/mixer", `{"cmd":"record","on":true}`, ck, "Accept-Language", "fr"); w.Code != 422 ||
		!strings.Contains(w.Body.String(), "branchez un disque USB") {
		t.Errorf("POST /mixer record no_disk: %d %s", w.Code, w.Body)
	}
	// The engine stopped: 503
	e.srv.Mixer = &mixer.Client{Sock: filepath.Join(run, "none.sock"), StatePath: filepath.Join(run, "none.json")}
	if kind, _ := errorOf(e.do("PUT", "/api/v1/record", `{"on":true}`, ck)); kind != "mixer_off" {
		t.Errorf("engine stopped: %s", kind)
	}
	if w := e.do("PUT", "/api/v1/record", `{"on":true}`, ck); w.Code != 503 {
		t.Errorf("engine stopped: %d", w.Code)
	}
}

// fakeStorage: eirl-storage through sudo, with its exit status; it keeps every command line and the time it was
// given.
type fakeStorage struct {
	mu     sync.Mutex
	lines  []string
	stdins []string
	waits  []time.Duration
	code   int
	err    error
	after  func() // what the helper changes before it exits
}

func (f *fakeStorage) Run(ctx context.Context, stdin, name string, args ...string) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, strings.Join(append([]string{name}, args...), " "))
	f.stdins = append(f.stdins, stdin)
	dl, _ := ctx.Deadline()
	f.waits = append(f.waits, time.Until(dl))
	if f.after != nil {
		f.after()
	}
	return "event=refused action=x reason=y", f.code, f.err
}

// The disk (RECORDING.md §6.2): the helper's state with a closed set of fields and no paths; eject and format through
// the exact sudo commands, their exit status as codes, and format only on the disk the confirmation named
func TestStorageAPI(t *testing.T) {
	e := setup(t, true)
	ck := e.login(t)
	get := func() storageView {
		t.Helper()
		w := e.do("GET", "/api/v1/storage", "", ck)
		var v storageView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("GET %d %s", w.Code, w.Body)
		}
		return v
	}
	// No helper, or no file yet: none, seq 0
	if w := e.do("GET", "/api/v1/storage", "", ck); w.Body.String() != `{"state":"none","seq":0}`+"\n" {
		t.Errorf("no helper: %s", w.Body)
	}
	state := filepath.Join(e.dir, "storage", "state.json")
	e.srv.StoragePath = state
	if v := get(); v.State != "none" || v.Seq != 0 {
		t.Errorf("no file: %+v", v)
	}
	os.MkdirAll(filepath.Dir(state), 0o755)
	os.WriteFile(state, []byte(storageReady), 0o644)
	w := e.do("GET", "/api/v1/storage", "", ck)
	var raw map[string]any
	json.Unmarshal(w.Body.Bytes(), &raw)
	allowed := []string{"state", "reason", "seq", "device", "model", "fs", "label", "sizeBytes", "readOnly", "dirty", "writeMBps", "anotherDisk"}
	for k := range raw {
		if !slices.Contains(allowed, k) {
			t.Errorf("field %q forwarded", k)
		}
	}
	if strings.Contains(w.Body.String(), "/media") || strings.Contains(w.Body.String(), "1234-ABCD") {
		t.Errorf("a path or the uuid: %s", w.Body)
	}
	if v := get(); v.State != "ready" || v.Seq != 7 || v.Device != "sda" || v.Model != "DataTraveler 3.0" || v.FS != "exfat" ||
		v.Label != "EIRL-REC" || v.SizeBytes != 62008590336 || v.ReadOnly == nil || *v.ReadOnly || v.Dirty == nil || !*v.Dirty ||
		v.WriteMBps != 31.2 || v.AnotherDisk != "sdb" {
		t.Errorf("ready: %+v", v)
	}
	// Outside the lists: a state reads as none; a reason, a disk or a filesystem that do not fit are dropped; the
	// label and the model, as a device's name
	os.WriteFile(state, []byte(`{"state":"mounted","seq":9,"device":"sda","model":"x"}`), 0o644)
	if w := e.do("GET", "/api/v1/storage", "", ck); w.Body.String() != `{"state":"none","seq":9}`+"\n" {
		t.Errorf("unknown state: %s", w.Body)
	}
	os.WriteFile(state, []byte(`{"state":"unsupported","reason":"fs_zfs","seq":3,"device":"/dev/sda","fs":"ntfs;rm",`+
		`"model":"Disk\u001b[2J<script>","label":"`+strings.Repeat("L", 80)+`","anotherDisk":"../sdb","sizeBytes":-5,"writeMBps":-1}`), 0o644)
	if v := get(); v.State != "unsupported" || v.Reason != "" || v.Device != "" || v.FS != "" || v.AnotherDisk != "" ||
		v.Model != "Disk 2J script" || len([]rune(v.Label)) > 48 || v.SizeBytes != 0 || v.WriteMBps != 0 {
		t.Errorf("bounded: %+v", v)
	}
	os.WriteFile(state, []byte(`{"state":"unsupported","reason":"fs_ntfs","seq":3,"fs":"ntfs"}`), 0o644)
	if v := get(); v.Reason != "fs_ntfs" || v.FS != "ntfs" {
		t.Errorf("ntfs: %+v", v)
	}
	os.WriteFile(state, []byte("{not json"), 0o644)
	if v := get(); v.State != "none" {
		t.Errorf("unreadable: %+v", v)
	}

	// Eject: the exact command of the sudoers rule, and its exit status
	f := &fakeStorage{}
	e.srv.StorageRunner = f
	os.WriteFile(state, []byte(storageReady), 0o644)
	f.after = func() {
		os.WriteFile(state, []byte(`{"state":"ejected","reason":"","seq":8,"device":"sda","fs":"exfat"}`), 0o644)
	}
	if w := e.do("POST", "/api/v1/storage/eject", "", ck); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"ejected","seq":8`) {
		t.Errorf("eject: %d %s", w.Code, w.Body)
	}
	if f.lines[0] != "/usr/bin/sudo -n /usr/local/sbin/eirl-storage eject" || f.waits[0] > ejectTimeout || f.waits[0] < ejectTimeout-time.Second {
		t.Errorf("command %q, %v", f.lines[0], f.waits[0])
	}
	f.after = nil
	if w := e.do("POST", "/api/v1/storage/eject", "{}", ck, "Sec-Fetch-Site", "cross-site"); w.Code != 403 || len(f.lines) != 1 {
		t.Errorf("cross-site eject: %d", w.Code)
	}
	exits := []struct {
		code   int
		err    error
		status int
		kind   string
	}{
		{storage.ExitNoDisk, nil, 409, "no_disk"}, {storage.ExitRecording, nil, 409, "recording"}, {storage.ExitBusy, nil, 409, "busy"},
		{storage.ExitFailed, nil, 500, "%s_failed"}, {storage.ExitUsage, nil, 500, "%s_failed"},
		{-1, context.DeadlineExceeded, 500, "%s_failed"}, {0, errors.New("sudo: not found"), 500, "%s_failed"},
	}
	for _, c := range exits {
		f.code, f.err = c.code, c.err
		w := e.do("POST", "/api/v1/storage/eject", "", ck)
		if kind, msg := errorOf(w); w.Code != c.status || kind != strings.ReplaceAll(c.kind, "%s", "eject") || msg == "" {
			t.Errorf("eject exit %d %v: %d %s", c.code, c.err, w.Code, w.Body)
		}
	}
	if w := e.do("POST", "/api/v1/storage/eject", "", ck, "Accept-Language", "es"); !strings.Contains(w.Body.String(), "no se pudo expulsar") {
		t.Errorf("message: %s", w.Body)
	}

	// Format: only with the seq the confirmation named, a disk there, then the exact command
	f.lines, f.stdins, f.waits, f.code, f.err = nil, nil, nil, 0, nil
	for _, body := range []string{"", `{}`, `{"seq":"7"}`, `{"seq":7,"force":true}`} {
		if w := e.do("POST", "/api/v1/storage/format", body, ck); w.Code != 400 {
			t.Errorf("format %q: %d", body, w.Code)
		}
	}
	for _, st := range []string{storageReady, `{"state":"none","reason":"","seq":7}`, `{"state":"checking","reason":"","seq":7,"device":"sda"}`} {
		os.WriteFile(state, []byte(st), 0o644)
		seq := "6"
		if !strings.Contains(st, `"ready"`) {
			seq = "7"
		}
		w := e.do("POST", "/api/v1/storage/format", `{"seq":`+seq+`}`, ck)
		if kind, _ := errorOf(w); w.Code != 409 || kind != "disk_changed" {
			t.Errorf("format %s with seq %s: %d %s", st, seq, w.Code, w.Body)
		}
	}
	if len(f.lines) != 0 {
		t.Fatalf("formatted a disk that changed: %v", f.lines)
	}
	for _, st := range []string{storageReady, `{"state":"unformatted","reason":"","seq":7,"device":"sda"}`,
		`{"state":"unsupported","reason":"fs_ntfs","seq":7,"device":"sda","fs":"ntfs"}`, `{"state":"ejected","reason":"","seq":7,"device":"sda"}`} {
		os.WriteFile(state, []byte(st), 0o644)
		f.after = func() {
			os.WriteFile(state, []byte(`{"state":"ready","reason":"","seq":9,"fs":"exfat","label":"EIRL-REC"}`), 0o644)
		}
		w := e.do("POST", "/api/v1/storage/format", `{"seq":7}`, ck)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"label":"EIRL-REC"`) {
			t.Errorf("format %s: %d %s", st, w.Code, w.Body)
		}
	}
	// The sudoers rule's exact command; the confirmed seq on stdin, which the helper checks again under its lock
	if f.lines[0] != "/usr/bin/sudo -n /usr/local/sbin/eirl-storage format" || f.stdins[0] != "7\n" ||
		f.waits[0] > formatTimeout || f.waits[0] < formatTimeout-time.Second {
		t.Errorf("command %q stdin %q, %v", f.lines[0], f.stdins[0], f.waits[0])
	}
	f.after = nil
	for _, c := range append(exits, struct {
		code   int
		err    error
		status int
		kind   string
	}{storage.ExitChanged, nil, 409, "disk_changed"}) {
		os.WriteFile(state, []byte(storageReady), 0o644)
		f.code, f.err = c.code, c.err
		w := e.do("POST", "/api/v1/storage/format", `{"seq":7}`, ck)
		if kind, msg := errorOf(w); w.Code != c.status || kind != strings.ReplaceAll(c.kind, "%s", "format") || msg == "" {
			t.Errorf("format exit %d %v: %d %s", c.code, c.err, w.Code, w.Body)
		}
	}
}

// Updates while recording (RECORDING.md §6.4): their own code and message; streaming keeps its own
func TestUpdateWhileRecording(t *testing.T) {
	e := setup(t, true)
	f := &fakeUpdate{st: update.Status{Current: "0.1.0", State: update.StateReady, Ready: true, Streaming: true,
		Available: &update.Manifest{Version: "0.2.0"}}}
	e.srv.Update = f
	pw := &fakePower{}
	e.srv.Power = pw
	sysDir := filepath.Join(e.dir, "eirl-system")
	os.MkdirAll(sysDir, 0o755)
	os.WriteFile(filepath.Join(sysDir, "state.json"), []byte(`{"v":1,"release":"0.1.0","bootId":"b","pending":[{"area":"hdmi","what":"x","effect":"reboot"}]}`), 0o644)
	e.srv.SystemStateDir, e.srv.BootID = sysDir, func() string { return "b" }
	run := t.TempDir()
	cl, _ := startRecEngine(t, run)
	e.srv.Mixer = cl
	e.h = e.srv.Handler()
	ck := e.login(t)
	view := func() UpdateView {
		var v UpdateView
		json.Unmarshal(e.do("GET", "/api/v1/update", "", ck, "Accept-Language", "en").Body.Bytes(), &v)
		return v
	}

	// Recording only
	writeMixer(t, cl, outputOff, recordingOn)
	if v := view(); v.CanInstall || v.Streaming || v.Blocked != "recording" || v.BlockedMsg != "stop the recording to update the encoder" {
		t.Errorf("recording view %+v", v)
	}
	f.installEr = update.ErrStreaming
	if kind, msg := errorOf(e.do("POST", "/api/v1/update/install", "{}", ck, "Accept-Language", "fr")); kind != "recording" ||
		msg != "arrêtez l’enregistrement pour mettre à jour l’encodeur" {
		t.Errorf("install while recording: %s %s", kind, msg)
	}
	w := e.do("POST", "/api/v1/update/reboot", "{}", ck, "Accept-Language", "es")
	if kind, msg := errorOf(w); w.Code != 409 || kind != "recording" || msg != "detén la grabación para reiniciar el codificador" || len(pw.calls) != 0 {
		t.Errorf("reboot while recording: %d %s", w.Code, w.Body)
	}
	writeMixer(t, cl, outputOff, `{"mode":"off","state":"starting","disk":{"state":"ready"}}`)
	if v := view(); v.Blocked != "recording" {
		t.Errorf("starting: %+v", v)
	}

	// Streaming, with or without a recording: streaming (Stop also stops a recording Go live started)
	for _, rec := range []string{recordingOn, recordOff, ""} {
		writeMixer(t, cl, outputOn, rec)
		if v := view(); !v.Streaming || v.Blocked != "streaming" {
			t.Errorf("streaming with %s: %+v", rec, v)
		}
		if kind, _ := errorOf(e.do("POST", "/api/v1/update/install", "{}", ck)); kind != "streaming" {
			t.Errorf("install while streaming: %s", kind)
		}
		if kind, _ := errorOf(e.do("POST", "/api/v1/update/reboot", "{}", ck)); kind != "streaming" {
			t.Errorf("reboot while streaming: %s", kind)
		}
	}
	// Without the mixer's state, what the updater says: streaming
	os.Remove(cl.StatePath)
	if v := view(); v.Blocked != "streaming" {
		t.Errorf("no mixer state: %+v", v)
	}
	// Neither: nothing blocks
	f.st.Streaming, f.installEr = false, nil
	writeMixer(t, cl, outputOff, recordOff)
	if v := view(); !v.CanInstall || v.Streaming || v.Blocked != "" {
		t.Errorf("idle: %+v", v)
	}
}

// The screen (R4) reaches the recording over the local socket, without session or CSRF
func TestRecordingOverLocalSocket(t *testing.T) {
	e := setup(t, true)
	state := filepath.Join(e.dir, "state.json")
	os.WriteFile(state, []byte(storageReady), 0o644)
	f := &fakeStorage{}
	e.srv.StoragePath, e.srv.StorageRunner = state, f
	lh := e.srv.LocalHandler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		lh.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/api/v1/recording", "", 200},
		{"PUT", "/api/v1/recording", `{"mode":"manual"}`, 200},
		{"PUT", "/api/v1/record", `{"on":true}`, 503}, // reached: no engine in this app
		{"GET", "/api/v1/storage", "", 200},
		{"POST", "/api/v1/storage/eject", "", 200},
		{"POST", "/api/v1/storage/format", `{"seq":7}`, 200},
	} {
		if w := do(c.method, c.path, c.body); w.Code != c.code {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if len(f.lines) != 2 || !strings.HasSuffix(f.lines[1], "eirl-storage format") {
		t.Errorf("helper %v", f.lines)
	}
	if env, _ := config.ReadEnv(e.srv.Config.AppPath); env["REC_MODE"] != "manual" {
		t.Errorf("not saved: %v", env)
	}
}
