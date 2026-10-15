package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/hw"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

const mixerState = `{"v":1,"program":{"main":"hdmi","secondary":null,"layout":"full","transition":"cut","hidden":[],` +
	`"width":1920,"height":1080,"fps":60,"measuredFps":60},"slots":4,"sources":[{"id":"hdmi","slot":1,"kind":"hdmi-csi",` +
	`"name":"HDMI de la placa","state":"live","detail":"","format":"UYVY","mode":"1920x1080@30","fps":30}],` +
	`"screen":{"state":"on","width":1024,"height":600,"touch":true}}`

type noSignalBoard struct{ fakeBoard }

func (noSignalBoard) Signal() hw.Signal { return hw.Signal{} }

// Fake engine: answers ok to take/layout and unknown_source to the rest; unknown_command to the
// alsa:Viejo card (like an engine older than the command)
func fakeMixer(t *testing.T, runDir string) (*mixer.Client, *[]string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mxa") // short path (Unix socket limit on macOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var cmds []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				var req map[string]any
				json.Unmarshal(sc.Bytes(), &req)
				cmds = append(cmds, sc.Text())
				code := "unknown_source"
				switch req["cmd"] {
				case "layout", "output", "pip", "pause", "lock", "audio_follow", "audio_mode":
					req["ok"] = true
				case "take":
					req["ok"] = req["source"] == "hdmi"
				case "audio_delay", "audio_link", "audio_mix":
					req["ok"] = req["source"] == "i2s" || req["source"] == "none"
				}
				if req["source"] == "alsa:Viejo" {
					code = "unknown_command"
				}
				if req["ok"] == true {
					c.Write([]byte(`{"id":1,"ok":true}` + "\n"))
				} else {
					c.Write([]byte(`{"id":1,"ok":false,"error":"` + code + `"}` + "\n"))
				}
			}
			c.Close()
		}
	}()
	return &mixer.Client{Sock: filepath.Join(dir, "s"), StatePath: filepath.Join(runDir, "mixer.json"), RunDir: runDir}, &cmds
}

func TestMixerAPI(t *testing.T) {
	e := setup(t, true)
	cookie := e.login(t)
	if w := e.do("GET", "/api/v1/mixer", "", cookie); w.Code != 503 {
		t.Errorf("no mixer in the app: %d", w.Code)
	}
	run := t.TempDir()
	cl, cmds := fakeMixer(t, run)
	e.srv.Mixer = cl
	if w := e.do("GET", "/api/v1/mixer", "", cookie); w.Code != 404 || !strings.Contains(w.Body.String(), "mixer_off") {
		t.Errorf("stopped: %d %s", w.Code, w.Body)
	}
	os.WriteFile(cl.StatePath, []byte(mixerState), 0o644)
	if w := e.do("GET", "/api/v1/mixer", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"main":"hdmi"`) {
		t.Errorf("state: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/mixer", `{"cmd":"take","source":"hdmi"}`, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("take: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/mixer", `{"cmd":"take","source":"test"}`, cookie); w.Code != 422 || !strings.Contains(w.Body.String(), "unknown_source") {
		t.Errorf("engine rejection: %d %s", w.Code, w.Body)
	}
	// Layout, pause and screen lock: reach the engine as is (the text, trimmed)
	for body, want := range map[string]string{
		`{"cmd":"pip","corner":"tl","size":25}`:                            `"cmd":"pip","corner":"tl","size":25`,
		`{"cmd":"pause","on":true}`:                                        `"cmd":"pause","on":true`,
		`{"cmd":"pause","text":"  ¡Volvemos a las 21:30!  ","mute":false}`: `"cmd":"pause","text":"¡Volvemos a las 21:30!","mute":false`,
		`{"cmd":"lock","on":false}`:                                        `"cmd":"lock","on":false`,
		// Audio (F4): follows video, delay of an input (0 too) and the audio of a video source
		`{"cmd":"audio_follow","on":true}`:                    `"cmd":"audio_follow","on":true`,
		`{"cmd":"audio_delay","source":"i2s","ms":0}`:         `"cmd":"audio_delay","source":"i2s","ms":0`,
		`{"cmd":"audio_delay","source":"i2s","ms":500}`:       `"cmd":"audio_delay","source":"i2s","ms":500`,
		`{"cmd":"audio_link","video":"hdmi","source":"i2s"}`:  `"cmd":"audio_link","source":"i2s","video":"hdmi"`,
		`{"cmd":"audio_link","video":"test","source":"none"}`: `"cmd":"audio_link","source":"none","video":"test"`,
		// Mix: the mode, and adding or removing an input and its gain (0 too, the limits and with a decimal)
		`{"cmd":"audio_mode","mode":"mix"}`:                       `"cmd":"audio_mode","mode":"mix"`,
		`{"cmd":"audio_mode","mode":"single"}`:                    `"cmd":"audio_mode","mode":"single"`,
		`{"cmd":"audio_mix","source":"i2s","on":true}`:            `"cmd":"audio_mix","source":"i2s","on":true}`,
		`{"cmd":"audio_mix","source":"i2s","on":false,"gain":-6}`: `"cmd":"audio_mix","source":"i2s","on":false,"gain":-6}`,
		`{"cmd":"audio_mix","source":"i2s","gain":0}`:             `"cmd":"audio_mix","source":"i2s","gain":0}`,
		`{"cmd":"audio_mix","source":"i2s","gain":-40}`:           `"cmd":"audio_mix","source":"i2s","gain":-40}`,
		`{"cmd":"audio_mix","source":"i2s","gain":12}`:            `"cmd":"audio_mix","source":"i2s","gain":12}`,
		`{"cmd":"audio_mix","source":"i2s","gain":2.5}`:           `"cmd":"audio_mix","source":"i2s","gain":2.5}`,
	} {
		w := e.do("POST", "/api/v1/mixer", body, cookie)
		if n := len(*cmds); w.Code != 200 || n == 0 || !strings.Contains((*cmds)[n-1], want) {
			t.Errorf("%s: %d %s, to the engine %v", body, w.Code, w.Body, *cmds)
		}
	}
	// What does not validate does not reach the engine: unknown commands, extra fields (also those of
	// another command), odd sources, corners, sizes or texts outside the rule
	n := len(*cmds)
	for _, body := range []string{`{"cmd":"reboot"}`, `{"cmd":"take","source":"hdmi","x":1}`, `{"cmd":"take","source":"/etc/passwd"}`, `{"cmd":"layout","layout":"zoom"}`,
		`{"cmd":"swap","on":true}`, `{"cmd":"output","on":true,"text":"x"}`, `{"cmd":"layout","layout":"pip","corner":"tl"}`,
		`{"cmd":"pip"}`, `{"cmd":"pip","corner":"c"}`, `{"cmd":"pip","size":50}`, `{"cmd":"pip","size":"33"}`,
		`{"cmd":"pause"}`, `{"cmd":"pause","text":"<script>"}`, `{"cmd":"pause","text":"a\nb"}`, `{"cmd":"pause","text":"` + strings.Repeat("a", 61) + `"}`,
		`{"cmd":"lock"}`, `{"cmd":"lock","on":true,"mute":true}`,
		// Audio: integer delay from 0 to 500 of a real input; link with valid ids
		`{"cmd":"audio_follow"}`, `{"cmd":"audio_follow","on":"yes"}`, `{"cmd":"audio_follow","on":true,"ms":0}`,
		`{"cmd":"audio_delay","source":"i2s"}`, `{"cmd":"audio_delay","source":"i2s","ms":1.5}`, `{"cmd":"audio_delay","source":"i2s","ms":"100"}`,
		`{"cmd":"audio_delay","source":"i2s","ms":501}`, `{"cmd":"audio_delay","source":"i2s","ms":-10}`, `{"cmd":"audio_delay","source":"none","ms":0}`,
		`{"cmd":"audio_delay","ms":100}`, `{"cmd":"audio_link","video":"hdmi"}`, `{"cmd":"audio_link","source":"i2s"}`,
		`{"cmd":"audio_link","video":"/etc/passwd","source":"i2s"}`, `{"cmd":"audio_link","video":"hdmi","source":"alsa:x;rm"}`,
		`{"cmd":"audio","source":"i2s","video":"hdmi"}`, `{"cmd":"take","source":"hdmi","ms":10}`,
		// Mix: the mode, from its list; a real input with on and/or numeric gain from −40 to +12
		`{"cmd":"audio_mode"}`, `{"cmd":"audio_mode","mode":"stereo"}`, `{"cmd":"audio_mode","mode":1}`, `{"cmd":"audio_mode","mode":"mix","on":true}`,
		`{"cmd":"audio_mix","source":"i2s"}`, `{"cmd":"audio_mix","on":true}`, `{"cmd":"audio_mix","source":"none","on":true}`,
		`{"cmd":"audio_mix","source":"hdmi","gain":0}`, `{"cmd":"audio_mix","source":"i2s","gain":-41}`, `{"cmd":"audio_mix","source":"i2s","gain":12.1}`,
		`{"cmd":"audio_mix","source":"i2s","gain":"-6"}`, `{"cmd":"audio_mix","source":"i2s","gain":null}`, `{"cmd":"audio_mix","source":"i2s","gain":1e400}`,
		`{"cmd":"audio_mix","source":"i2s","on":"yes"}`, `{"cmd":"audio_mix","source":"i2s","gain":0,"ms":0}`, `{"cmd":"audio_mix","source":"i2s","on":true,"mode":"mix"}`,
		`{"cmd":"audio","source":"i2s","gain":0}`, `{"cmd":"audio_follow","on":true,"mode":"mix"}`,
		`{"cmd":"output","on":true}`} { // streaming goes through PUT /output: otherwise OUTPUT is not saved
		if w := e.do("POST", "/api/v1/mixer", body, cookie); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if len(*cmds) != n {
		t.Errorf("invalid commands reached the engine: %v", (*cmds)[n:])
	}
	// Engine rejections of the audio commands: input or source without a slot, and an older engine
	// (without audio_mix it answers unknown_command)
	for body, want := range map[string]string{
		`{"cmd":"audio_delay","source":"alsa:Edition","ms":100}`:    `"error":"unknown_source"`,
		`{"cmd":"audio_link","video":"hdmi","source":"alsa:Viejo"}`: `"esta versión del codificador no lo admite"`,
		`{"cmd":"audio_mix","source":"alsa:Edition","on":true}`:     `"error":"unknown_source"`,
		`{"cmd":"audio_mix","source":"alsa:Viejo","gain":-3}`:       `"esta versión del codificador no lo admite"`,
	} {
		// The message in the request language (Accept-Language), the code unchanged
		if w := e.do("POST", "/api/v1/mixer", body, cookie, "Accept-Language", "es-ES,es;q=0.9"); w.Code != 422 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	// No session, nothing
	if w := e.do("POST", "/api/v1/mixer", `{"cmd":"swap"}`, ""); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
	// Images: only those on the list
	os.WriteFile(filepath.Join(run, "source-1.jpg"), []byte("\xff\xd8jpeg"), 0o644)
	if w := e.do("GET", "/api/v1/mixer/source-1.jpg", "", cookie); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Body.String() != "\xff\xd8jpeg" {
		t.Errorf("thumbnail: %d %q", w.Code, w.Body)
	}
	for _, p := range []string{"/api/v1/mixer/mixer.json", "/api/v1/mixer/source-9.jpg", "/api/v1/mixer/screen.jpg"} {
		if w := e.do("GET", p, "", cookie); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	// The general status carries it while the broadcast runs as the mixer
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=off\nSOURCE=hdmi\nINPUT=ready\nMIXER=1\n"), 0o644)
	if w := e.do("GET", "/api/v1/status", "", cookie); !strings.Contains(w.Body.String(), `"mixer":{"v":1`) {
		t.Errorf("status without mixer: %s", w.Body)
	}
	// With the mixer the phase does not wait for the main source's signal
	e.srv.Board = noSignalBoard{}
	if w := e.do("GET", "/api/v1/status", "", cookie); !strings.Contains(w.Body.String(), `"phase":"ready"`) {
		t.Errorf("phase with the mixer and the main source without signal: %s", w.Body)
	}
	e.srv.Board = fakeBoard{}
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=off\nSOURCE=hdmi\nINPUT=ready\n"), 0o644)
	if w := e.do("GET", "/api/v1/status", "", cookie); strings.Contains(w.Body.String(), `"mixer"`) {
		t.Errorf("status with mixer without MIXER=1: %s", w.Body)
	}
}

// The cameras' image settings (SOURCES.md §7.3): they reach the engine as is (the value 0 too), what does not validate
// does not, and the engine's codes come back with their message in the request language
func TestMixerCamera(t *testing.T) {
	e := setup(t, true)
	cookie := e.login(t)
	run := t.TempDir()
	dir, err := os.MkdirTemp("", "mxc") // short path (Unix socket limit on macOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	// The engine's answer by control (camera_control) or by source (camera_reset)
	codes := map[string]string{"focus": "inactive", "zoom": "busy", "pan": "camera_error", "tilt": "unknown_control",
		"hue": "unsupported", "hdmi": "unsupported", "test": "unknown_source", "net:cam1": "unknown_command", "gamma": "unknown_command"}
	var mu sync.Mutex
	var cmds []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				var req map[string]any
				json.Unmarshal(sc.Bytes(), &req)
				mu.Lock()
				cmds = append(cmds, sc.Text())
				mu.Unlock()
				key, _ := req["control"].(string)
				if req["cmd"] == "camera_reset" {
					key, _ = req["source"].(string)
				}
				if code := codes[key]; code != "" {
					c.Write([]byte(`{"id":1,"ok":false,"error":"` + code + `"}` + "\n"))
				} else {
					c.Write([]byte(`{"id":1,"ok":true}` + "\n"))
				}
			}
			c.Close()
		}
	}()
	e.srv.Mixer = &mixer.Client{Sock: filepath.Join(dir, "s"), StatePath: filepath.Join(run, "mixer.json"), RunDir: run}
	os.WriteFile(filepath.Join(run, "mixer.json"), []byte(mixerState), 0o644)
	sent := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, cmds...) }
	cam := "/dev/v4l/by-id/usb-046d_Logi_4K_Stream_Edition_0000ABCD-video-index0"
	for body, want := range map[string]string{
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":140}`: `"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":140}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"gain","value":0}`:         `"control":"gain","value":0}`,
		`{"cmd":"camera_reset","source":"` + cam + `"}`:                                      `"cmd":"camera_reset","source":"` + cam + `"}`,
	} {
		w := e.do("POST", "/api/v1/mixer", body, cookie)
		if c := sent(); w.Code != 200 || len(c) == 0 || !strings.Contains(c[len(c)-1], want) {
			t.Errorf("%s: %d %s, to the engine %v", body, w.Code, w.Body, c)
		}
	}
	n := len(sent())
	for _, body := range []string{
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness"}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":null}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":1.5}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":"1"}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":true}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":4294967296}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"exposure_auto_priority","value":0}`,
		`{"cmd":"camera_control","source":"` + cam + `","value":1}`,
		`{"cmd":"camera_control","source":"/dev/video0","control":"brightness","value":1}`,
		`{"cmd":"camera_control","source":"` + cam + `","control":"brightness","value":1,"on":true}`,
		`{"cmd":"camera_reset"}`, `{"cmd":"camera_reset","source":"` + cam + `","control":"zoom"}`,
		`{"cmd":"take","source":"hdmi","value":1}`,
	} {
		if w := e.do("POST", "/api/v1/mixer", body, cookie); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if c := sent(); len(c) != n {
		t.Errorf("invalid commands reached the engine: %v", c[n:])
	}
	// The engine's codes: the code as is, the message in the request language. unsupported, for these commands, is a
	// source without image settings now; unknown_command, an engine older than them
	ctl := func(id string) string {
		return `{"cmd":"camera_control","source":"` + cam + `","control":"` + id + `","value":1}`
	}
	for _, c := range []struct{ body, code, msg string }{
		{ctl("focus"), "inactive", "ese ajuste está en automático"},
		{ctl("zoom"), "busy", "ocupado ahora mismo"},
		{ctl("pan"), "camera_error", "la cámara no lo ha aceptado o no responde"},
		{ctl("tilt"), "unknown_control", "esa cámara no tiene ese ajuste"},
		{ctl("hue"), "unsupported", "esa fuente no tiene ajustes de imagen ahora"},
		{ctl("gamma"), "unknown_command", "esta versión del codificador no lo admite"},
		{`{"cmd":"camera_reset","source":"hdmi"}`, "unsupported", "esa fuente no tiene ajustes de imagen ahora"},
		{`{"cmd":"camera_reset","source":"test"}`, "unknown_source", "esa fuente no está en el mezclador"},
		{`{"cmd":"camera_reset","source":"net:cam1"}`, "unknown_command", "esta versión del codificador no lo admite"},
	} {
		w := e.do("POST", "/api/v1/mixer", c.body, cookie, "Accept-Language", "es-ES,es;q=0.9")
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"error":"`+c.code+`"`) || !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	if w := e.do("POST", "/api/v1/mixer", ctl("focus"), cookie, "Accept-Language", "fr"); !strings.Contains(w.Body.String(), "ce réglage est en automatique") {
		t.Errorf("fr: %s", w.Body)
	}
}

// Every engine code of a command has its message: unsupported keeps its old meaning outside the camera commands
func TestMixerErrorKey(t *testing.T) {
	for _, c := range []struct{ cmd, code, key string }{
		{"take", "unsupported", "mixer.unsupported"}, {"camera_control", "unsupported", "mixer.camera_unsupported"},
		{"camera_reset", "unsupported", "mixer.camera_unsupported"}, {"camera_reset", "unknown_command", "mixer.unsupported"},
		{"camera_control", "inactive", "mixer.control_inactive"}, {"camera_control", "busy", "mixer.busy"},
		{"camera_control", "camera_error", "mixer.camera_error"}, {"camera_control", "unknown_control", "mixer.unknown_control"},
		{"take", "on_air", "mixer.on_air"}, {"take", "whatever", "mixer.rejected"},
	} {
		if got := mixerErrorKey(c.cmd, c.code); got != c.key {
			t.Errorf("%s/%s: %s, want %s", c.cmd, c.code, got, c.key)
		}
	}
}

// F3: with the mixer, Stream and Stop streaming are hot: OUTPUT is saved, the command is sent to the
// engine and eirl-engine is NOT restarted; nor does it show as an “unapplied change”
func TestHotOutput(t *testing.T) {
	e := setup(t, true)
	cookie := e.login(t)
	run := t.TempDir()
	cl, cmds := fakeMixer(t, run)
	e.srv.Mixer = cl
	os.WriteFile(cl.StatePath, []byte(strings.Replace(mixerState, `"touch":true}}`, `"touch":true},"output":{"mode":"off","state":"off"}}`, 1)), 0o644)
	e.do("PUT", "/api/v1/destination", `{"latencyMs":300}`, cookie) // the app file exists
	// The fingerprint the script writes with the mixer: without OUTPUT
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=off\nSOURCE=hdmi\nINPUT=ready\nMIXER=1\nCONF_SHA="+e.srv.Config.Hashes().NoOutput+"\n"), 0o644)
	w := e.do("PUT", "/api/v1/output", `{"output":"on","apply":true}`, cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"restarted":true`) {
		t.Fatalf("stream: %d %s", w.Code, w.Body)
	}
	if len(e.unit.actions) != 0 {
		t.Errorf("streaming with the mixer does not restart: %v", e.unit.actions)
	}
	if n := len(*cmds); n == 0 || !strings.Contains((*cmds)[n-1], `"cmd":"output","on":true`) {
		t.Errorf("command to the engine: %v", *cmds)
	}
	if w := e.do("GET", "/api/v1/status", "", cookie); strings.Contains(w.Body.String(), `"pendingRestart":true`) {
		t.Errorf("hot OUTPUT is not an unapplied change: %s", w.Body)
	}
	// A real change (destination) does restart
	if w := e.do("PUT", "/api/v1/destination", `{"latencyMs":900,"apply":true}`, cookie); !strings.Contains(w.Body.String(), `"restarted":true`) {
		t.Errorf("changing the destination restarts: %s", w.Body)
	}
	// The output state comes from the engine
	os.WriteFile(cl.StatePath, []byte(strings.Replace(mixerState, `"touch":true}}`, `"touch":true},"output":{"mode":"on","state":"connecting","reason":"relay_unreachable","detail":"el relay no responde"}}`, 1)), 0o644)
	b := e.do("GET", "/api/v1/status", "", cookie).Body.String()
	if !strings.Contains(b, `"output":"on"`) || !strings.Contains(b, `"outputState":{"state":"connecting","reason":"relay_unreachable","detail":"el relay no responde"}`) || !strings.Contains(b, `"phase":"streaming"`) {
		t.Errorf("output state: %s", b)
	}
}

// Fake engine with a subscription: sends every state that arrives on states as an event
func fakeMixerWatch(t *testing.T) (*mixer.Client, chan<- string, func() []string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mxw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	states := make(chan string, 8)
	var mu sync.Mutex
	var cmds []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					mu.Lock()
					cmds = append(cmds, sc.Text())
					mu.Unlock()
					if !strings.Contains(sc.Text(), `"subscribe"`) {
						c.Write([]byte(`{"id":1,"ok":true}` + "\n"))
						continue
					}
					for st := range states {
						if _, err := c.Write([]byte(`{"event":"state","state":` + st + "}\n")); err != nil {
							return
						}
					}
				}
			}()
		}
	}()
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, cmds...)
	}
	return &mixer.Client{Sock: filepath.Join(dir, "s"), StatePath: filepath.Join(dir, "mixer.json"), RunDir: dir}, states, got
}

func withOutput(mode, by string, seq int) string {
	return strings.Replace(mixerState, `"touch":true}}`,
		fmt.Sprintf(`"touch":true},"output":{"mode":%q,"state":"connecting","by":%q,"seq":%d}}`, mode, by, seq), 1)
}

// Stream or Stop streaming from the touchscreen is saved in OUTPUT (it survives a restart and the
// cloud sees it), without restarting or sending anything to the engine, even with no tab open
func TestScreenOutput(t *testing.T) {
	e := setup(t, true)
	cookie := e.login(t)
	cl, states, cmds := fakeMixerWatch(t)
	e.srv.Mixer = cl
	e.do("PUT", "/api/v1/output", `{"output":"off"}`, cookie)
	os.WriteFile(e.srv.RunStatePath, []byte("OUTPUT=off\nSOURCE=hdmi\nINPUT=ready\nMIXER=1\nCONF_SHA="+e.srv.Config.Hashes().NoOutput+"\n"), 0o644)
	output := func() string { cfg, _ := e.srv.Config.Load(); return cfg.Output }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.srv.FollowMixer(ctx)
	states <- withOutput("off", "start", 0)
	states <- withOutput("on", "screen", 1)
	for deadline := time.Now().Add(3 * time.Second); output() != "on"; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the output chosen on the screen was not saved")
		}
	}
	if len(e.unit.actions) != 0 {
		t.Errorf("saving the screen's output does not restart: %v", e.unit.actions)
	}
	if w := e.do("GET", "/api/v1/status", "", cookie); strings.Contains(w.Body.String(), `"pendingRestart":true`) {
		t.Errorf("the screen's output is not an unapplied change: %s", w.Body)
	}
	cancel()

	// Once per change and only from the screen. Anything else does not touch what is saved
	last := -1
	set := func(out string) { e.do("PUT", "/api/v1/output", `{"output":"`+out+`"}`, cookie) }
	step := func(st, want, why string) {
		t.Helper()
		var m mixer.State
		json.Unmarshal([]byte(st), &m)
		e.srv.screenOutput(&last, &m)
		if got := output(); got != want {
			t.Errorf("%s: OUTPUT=%s, want %s", why, got, want)
		}
	}
	set("off")
	step(withOutput("on", "screen", 1), "on", "first state after the app starts: it catches up")
	set("off") // the web stops streaming; a stale screen state does not undo it
	step(withOutput("on", "screen", 1), "off", "same seq repeated")
	step(withOutput("on", "socket", 2), "off", "by socket: the saved value wins")
	step(withOutput("on", "start", 0), "off", "by start (engine restarted)")
	step(withOutput("on", "screen", 1), "on", "seq 1 of a new engine")
	step(withOutput("off", "screen", 2), "off", "stop streaming from the screen")
	step(strings.Replace(withOutput("on", "screen", 3), `,"by":"screen","seq":3`, "", 1), "off", "older engine, no by or seq")
	step(mixerState, "off", "no output")
	if len(e.unit.actions) != 0 {
		t.Errorf("none of this restarts: %v", e.unit.actions)
	}
	for _, c := range cmds() {
		if !strings.Contains(c, `"subscribe"`) {
			t.Errorf("nothing is sent to the engine: %s", c)
		}
	}
}

type fakePower struct{ calls []string }

func (p *fakePower) Reboot(context.Context) error { p.calls = append(p.calls, "reboot"); return nil }
func (p *fakePower) Poweroff(context.Context) error {
	p.calls = append(p.calls, "poweroff")
	return nil
}

// The screen's Power menu: the app reboots or powers off once per request, and only if it is recent
func TestScreenPower(t *testing.T) {
	e := setup(t, true)
	pw := &fakePower{}
	e.srv.Power = pw
	now := time.Unix(1800000000, 0)
	at := func(d time.Duration) float64 { return float64(now.Add(d).UnixNano()) / 1e9 }
	st := func(want string, seq int, ago time.Duration) *mixer.State {
		return &mixer.State{Screen: mixer.Screen{Power: &mixer.PowerRequest{Want: want, Seq: seq, At: at(-ago)}}}
	}
	last := 0
	e.srv.screenPower(context.Background(), &last, &mixer.State{}, now) // no request: nothing
	e.srv.screenPower(context.Background(), &last, st("reboot", 1, time.Second), now)
	e.srv.screenPower(context.Background(), &last, st("reboot", 1, time.Second), now) // the same one: not repeated
	e.srv.screenPower(context.Background(), &last, st("poweroff", 2, 2*time.Second), now)
	e.srv.screenPower(context.Background(), &last, st("poweroff", 3, time.Minute), now) // stale (the app reconnected)
	if strings.Join(pw.calls, ",") != "reboot,poweroff" {
		t.Errorf("commands: %v", pw.calls)
	}
}
