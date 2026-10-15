package mixer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const stateJSON = `{"v":1,"program":{"main":"hdmi","secondary":null,"layout":"full","transition":"cut","hidden":[],` +
	`"width":1920,"height":1080,"fps":60,"measuredFps":59.9},"slots":4,"sources":[{"id":"hdmi","slot":1,` +
	`"kind":"hdmi-csi","name":"HDMI de la placa","state":"live","detail":"","format":"UYVY","mode":"1920x1080@30","fps":30}],` +
	`"screen":{"state":"on","width":1024,"height":600,"touch":true},"output":{"mode":"off","state":"off"},"secret":"x"}`

// fakeEngine: a fake engine on a Unix socket (like internal/engine/server)
func fakeEngine(t *testing.T, handle func(req map[string]any) []string) string {
	t.Helper()
	// Short path: a Unix socket allows no more than ~104 characters on macOS
	dir, err := os.MkdirTemp("", "mx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req map[string]any
					json.Unmarshal(sc.Bytes(), &req)
					for _, line := range handle(req) {
						c.Write([]byte(line + "\n"))
					}
				}
			}(conn)
		}
	}()
	return sock
}

func TestValidate(t *testing.T) {
	yes, no := true, false
	n25, n33, n50 := 25, 33, 50
	n0, n500, n501, neg := 0, 500, 501, -10
	cam := "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0004C523E9000-video-index0"
	str := func(s string) *string { return &s }
	db := func(g float64) *float64 { return &g }
	ok := []Command{
		{Cmd: "take", Source: "hdmi"}, {Cmd: "secondary", Source: "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0004C523E9000-video-index0"},
		{Cmd: "swap"}, {Cmd: "layout", Layout: "pip"}, {Cmd: "transition", Transition: "fade"},
		{Cmd: "hide", Source: "test", Hidden: &yes}, {Cmd: "hide", Source: "hdmi", Hidden: &no},
		{Cmd: "audio", Source: "i2s"}, {Cmd: "audio", Source: "alsa:Edition"}, {Cmd: "audio", Source: "none"},
		// Network cameras (F7): video and audio with the same id
		{Cmd: "take", Source: "net:cam1"}, {Cmd: "hide", Source: "net:cam8", Hidden: &yes}, {Cmd: "audio", Source: "net:cam1"},
		{Cmd: "output", On: &yes},
		{Cmd: "pip", Corner: "tl"}, {Cmd: "pip", Size: &n25}, {Cmd: "pip", Corner: "br", Size: &n33},
		{Cmd: "pause", On: &yes}, {Cmd: "pause", Mute: &no}, {Cmd: "pause", On: &no, Text: str("Volvemos enseguida"), Mute: &yes},
		{Cmd: "pause", Text: str("¿Pausa técnica? Volvemos a las 21:30 — 100 % (o casi)…")},
		{Cmd: "pause", Text: str("#IRL @canal €5/$5 * 'x' \"y\" ·+– ¡Ya; vale, sí: 1-0!")},
		{Cmd: "pause", Text: str(strings.Repeat("ñ", 60))}, {Cmd: "pause", Text: str("  Ya vuelvo  ")}, {Cmd: "pause", Text: str("Пауза 中断 ٣")},
		{Cmd: "lock", On: &yes}, {Cmd: "lock", On: &no},
		{Cmd: "audio_follow", On: &yes}, {Cmd: "audio_follow", On: &no},
		{Cmd: "audio_delay", Source: "i2s", Ms: &n0}, {Cmd: "audio_delay", Source: "alsa:Edition", Ms: &n500},
		{Cmd: "audio_link", Video: "hdmi", Source: "i2s"}, {Cmd: "audio_link", Video: cam, Source: "alsa:Edition"},
		{Cmd: "audio_link", Video: "test", Source: "none"},
		{Cmd: "audio_mode", Mode: "single"}, {Cmd: "audio_mode", Mode: "mix"},
		{Cmd: "audio_mix", Source: "i2s", On: &yes}, {Cmd: "audio_mix", Source: "alsa:Edition", On: &no},
		{Cmd: "audio_mix", Source: "i2s", Gain: db(0)}, {Cmd: "audio_mix", Source: "i2s", Gain: db(-40)},
		{Cmd: "audio_mix", Source: "i2s", Gain: db(12)}, {Cmd: "audio_mix", Source: "alsa:Edition", On: &yes, Gain: db(-6.5)},
		// Screen headphones: on and/or gain, integer dB from −40 (mute) to 0
		{Cmd: "phones", On: &yes}, {Cmd: "phones", On: &no}, {Cmd: "phones", Gain: db(-12)},
		{Cmd: "phones", Gain: db(-40)}, {Cmd: "phones", Gain: db(0)}, {Cmd: "phones", On: &yes, Gain: db(-6)},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []Command{
		{Cmd: "take", Source: "net:cam9"}, {Cmd: "take", Source: "net:cam1/../x"}, {Cmd: "audio", Source: "net:"},
		{Cmd: "reboot"}, {Cmd: "take"}, {Cmd: "take", Source: "/dev/video0"}, {Cmd: "take", Source: "/dev/v4l/by-id/../../etc"},
		{Cmd: "take", Source: "hdmi\n{\"cmd\":\"x\"}"}, {Cmd: "layout", Layout: "zoom"}, {Cmd: "transition"},
		{Cmd: "hide", Source: "hdmi"},
		{Cmd: "audio", Source: "hdmi"}, {Cmd: "audio", Source: "alsa:x;rm"}, {Cmd: "audio"},
		{Cmd: "output"}, {Cmd: "lock"},
		// Fields of another command: they do not reach the engine
		{Cmd: "swap", Source: "hdmi"}, {Cmd: "take", Source: "hdmi", On: &yes}, {Cmd: "output", On: &yes, Mute: &yes},
		{Cmd: "layout", Layout: "pip", Corner: "tl"}, {Cmd: "lock", On: &yes, Text: str("x")}, {Cmd: "pip", Corner: "tl", On: &yes},
		// PiP: corner and size from their lists, at least one
		{Cmd: "pip"}, {Cmd: "pip", Corner: "center"}, {Cmd: "pip", Size: &n50}, {Cmd: "pip", Corner: "tl", Size: &n50},
		// Pause: at least one field; text of 1 to 60 runes without markup, line breaks or emoji
		{Cmd: "pause"}, {Cmd: "pause", Text: str("")}, {Cmd: "pause", Text: str("   ")}, {Cmd: "pause", Text: str(strings.Repeat("ñ", 61))},
		{Cmd: "pause", Text: str("<b>hola</b>")}, {Cmd: "pause", Text: str("a & b")}, {Cmd: "pause", Text: str("una\nlínea")},
		{Cmd: "pause", Text: str("Vuelvo 🙂")}, {Cmd: "pause", Text: str("tab\tx")}, {Cmd: "pause", Text: str("a_b")},
		{Cmd: "pause", Text: str("e\u0301")}, // combining accent: not a letter (the web normalizes to NFC)
		// Audio: follow video with on; delay of a real input (not none), integer from 0 to 500;
		// link with a valid video source and a valid audio input (or none)
		{Cmd: "audio_follow"}, {Cmd: "audio_follow", On: &yes, Source: "i2s"},
		{Cmd: "audio_delay", Source: "i2s"}, {Cmd: "audio_delay", Ms: &n0}, {Cmd: "audio_delay", Source: "none", Ms: &n0},
		{Cmd: "audio_delay", Source: "hdmi", Ms: &n0}, {Cmd: "audio_delay", Source: "i2s", Ms: &n501}, {Cmd: "audio_delay", Source: "i2s", Ms: &neg},
		{Cmd: "audio_delay", Source: "i2s", Ms: &n0, On: &yes},
		{Cmd: "audio_link", Video: "hdmi"}, {Cmd: "audio_link", Source: "i2s"}, {Cmd: "audio_link", Video: "/dev/video0", Source: "i2s"},
		{Cmd: "audio_link", Video: "hdmi", Source: "hdmi"}, {Cmd: "audio_link", Video: "i2s", Source: "i2s"},
		{Cmd: "audio_link", Video: "hdmi", Source: "alsa:x;rm"}, {Cmd: "audio_link", Video: "hdmi", Source: "i2s", Ms: &n0},
		// The new fields are not valid in other commands either
		{Cmd: "take", Source: "hdmi", Video: "hdmi"}, {Cmd: "audio", Source: "i2s", Ms: &n0},
		// Mix: the mode, one of the two words; a real input (not none) with on and/or gain, the gain
		// from −40 to +12
		{Cmd: "audio_mode"}, {Cmd: "audio_mode", Mode: "stereo"}, {Cmd: "audio_mode", Mode: "MIX"}, {Cmd: "audio_mode", Mode: "mix", On: &yes},
		{Cmd: "audio_mix", Source: "i2s"}, {Cmd: "audio_mix", On: &yes}, {Cmd: "audio_mix", Source: "none", On: &yes},
		{Cmd: "audio_mix", Source: "hdmi", On: &yes}, {Cmd: "audio_mix", Source: "alsa:x;rm", Gain: db(0)},
		{Cmd: "audio_mix", Source: "i2s", Gain: db(-40.1)}, {Cmd: "audio_mix", Source: "i2s", Gain: db(12.5)}, {Cmd: "audio_mix", Source: "i2s", Gain: db(-90)},
		{Cmd: "audio_mix", Source: "i2s", On: &yes, Ms: &n0}, {Cmd: "audio_mix", Source: "i2s", Gain: db(0), Mode: "mix"},
		{Cmd: "audio", Source: "i2s", Gain: db(0)}, {Cmd: "audio_delay", Source: "i2s", Ms: &n0, Gain: db(0)}, {Cmd: "layout", Layout: "pip", Mode: "mix"},
		// Headphones: at least one; not above 0 nor below −40, in integer dB; nothing from other commands
		{Cmd: "phones"}, {Cmd: "phones", Gain: db(1)}, {Cmd: "phones", Gain: db(-41)}, {Cmd: "phones", Gain: db(-6.5)},
		{Cmd: "phones", On: &yes, Source: "i2s"}, {Cmd: "phones", Gain: db(-6), Mode: "mix"},
	}
	for _, c := range bad {
		if c.Validate() == nil {
			t.Errorf("%+v: should be rejected", c)
		}
	}
}

func TestDo(t *testing.T) {
	var got map[string]any
	sock := fakeEngine(t, func(req map[string]any) []string {
		got = req
		if req["source"] == "zz" || req["source"] == "test" {
			return []string{`{"id":1,"ok":false,"error":"unknown_source"}`}
		}
		// a stray event before the response does not confuse the client
		return []string{`{"event":"state","state":{}}`, `{"id":1,"ok":true}`}
	})
	c := &Client{Sock: sock}
	if err := c.Do(context.Background(), Command{Cmd: "take", Source: "hdmi"}); err != nil {
		t.Fatal(err)
	}
	if got["cmd"] != "take" || got["source"] != "hdmi" || got["id"] != 1.0 {
		t.Errorf("request %v", got)
	}
	var ce *CommandError
	if err := c.Do(context.Background(), Command{Cmd: "take", Source: "test"}); !errors.As(err, &ce) || ce.Code != "unknown_source" {
		t.Errorf("engine error: %v", err)
	}
	if err := c.Do(context.Background(), Command{Cmd: "nope"}); err == nil || errors.As(err, &ce) {
		t.Errorf("an invalid command does not reach the engine: %v", err)
	}
	text := "  Volvemos enseguida  "
	if err := c.Do(context.Background(), Command{Cmd: "pause", Text: &text}); err != nil || got["text"] != "Volvemos enseguida" || text != "  Volvemos enseguida  " {
		t.Errorf("pause: %v %v (original command %q)", err, got, text)
	}
	zero := 0
	if err := c.Do(context.Background(), Command{Cmd: "audio_delay", Source: "i2s", Ms: &zero}); err != nil || got["ms"] != 0.0 || got["source"] != "i2s" {
		t.Errorf("delay 0: %v %v", err, got)
	}
	// Gain 0 (back to unchanged) is sent too
	gain := 0.0
	if err := c.Do(context.Background(), Command{Cmd: "audio_mix", Source: "i2s", Gain: &gain}); err != nil || got["gain"] != 0.0 || got["on"] != nil {
		t.Errorf("gain 0: %v %v", err, got)
	}
	off := &Client{Sock: filepath.Join(t.TempDir(), "no")}
	if err := off.Do(context.Background(), Command{Cmd: "swap"}); !errors.Is(err, ErrOff) {
		t.Errorf("no socket: %v", err)
	}
}

func TestWatch(t *testing.T) {
	sock := fakeEngine(t, func(req map[string]any) []string {
		if req["cmd"] != "subscribe" {
			return nil
		}
		return []string{`{"id":1,"ok":true,"state":` + stateJSON + `}`, `{"event":"state","state":` + stateJSON + `}`}
	})
	c := &Client{Sock: sock}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n := 0
	err := c.Watch(ctx, func(st *State) {
		n++
		if st.Program.Main == nil || *st.Program.Main != "hdmi" || len(st.Sources) != 1 || st.Screen.State != "on" {
			t.Errorf("state %+v", st)
		}
		if n == 2 {
			cancel()
		}
	})
	if n != 2 || !errors.Is(err, context.Canceled) {
		t.Errorf("n=%d err=%v", n, err)
	}
}

func TestReadAndImages(t *testing.T) {
	dir := t.TempDir()
	c := &Client{StatePath: filepath.Join(dir, "mixer.json"), RunDir: dir}
	if _, err := c.Read(); !errors.Is(err, ErrOff) {
		t.Errorf("no file: %v", err)
	}
	os.WriteFile(c.StatePath, []byte(stateJSON), 0o644)
	st, err := c.Read()
	if err != nil || st.Program.FPS != 60 || *st.Program.MeasuredFPS != 59.9 || st.Sources[0].Mode != "1920x1080@30" {
		t.Fatalf("%+v %v", st, err)
	}
	// Only what is in the structs is forwarded: anything else the engine writes is left out
	b, _ := json.Marshal(st)
	var back map[string]any
	json.Unmarshal(b, &back)
	if _, ok := back["secret"]; ok {
		t.Error("a field not in State is forwarded")
	}
	if st.Output == nil || st.Output.State != "off" {
		t.Errorf("output: %+v", st.Output)
	}
	// What comes from the engine is bounded: state and reason from closed lists, detail ≤ 200
	bad := `{"v":1,"program":{},"slots":1,"sources":[],"screen":{},"output":{"mode":"x","state":"rm -rf","reason":"<b>","detail":"` + strings.Repeat("é", 300) + `"}}`
	os.WriteFile(c.StatePath, []byte(bad), 0o644)
	st, _ = c.Read()
	if o := st.Output; o.Mode != "off" || o.State != "off" || o.Reason != "" || len([]rune(o.Detail)) != 200 {
		t.Errorf("not bounded: %+v", o)
	}
	// Layout, pause, lock and who changed the output: pass through as is if valid…
	full := strings.Replace(stateJSON, `"measuredFps":59.9}`, `"measuredFps":59.9,"pip":{"corner":"tl","size":25},`+
		`"pause":{"on":true,"text":"Volvemos enseguida","mute":true}}`, 1)
	full = strings.Replace(full, `"touch":true}`, `"touch":true,"locked":true}`, 1)
	full = strings.Replace(full, `"output":{"mode":"off","state":"off"}`, `"output":{"mode":"on","state":"on","by":"screen","seq":3}`, 1)
	os.WriteFile(c.StatePath, []byte(full), 0o644)
	st, _ = c.Read()
	if p := st.Program.PiP; p == nil || p.Corner != "tl" || p.Size != 25 {
		t.Errorf("pip: %+v", p)
	}
	if p := st.Program.Pause; p == nil || !p.On || p.Text != "Volvemos enseguida" || !p.Mute {
		t.Errorf("pause: %+v", p)
	}
	if l := st.Screen.Locked; l == nil || !*l {
		t.Errorf("lock: %v", l)
	}
	if o := st.Output; o.By != "screen" || o.Seq != 3 {
		t.Errorf("output: %+v", o)
	}
	// … and bounded if not: PiP outside its list = no controls, text ≤ 60, by from its list, seq ≥ 0
	full = strings.Replace(full, `"corner":"tl"`, `"corner":"middle"`, 1)
	full = strings.Replace(full, `"text":"Volvemos enseguida"`, `"text":"`+strings.Repeat("ñ", 100)+`"`, 1)
	full = strings.Replace(full, `"by":"screen","seq":3`, `"by":"cloud","seq":-4`, 1)
	os.WriteFile(c.StatePath, []byte(full), 0o644)
	st, _ = c.Read()
	if st.Program.PiP != nil || len([]rune(st.Program.Pause.Text)) != 60 || st.Output.By != "" || st.Output.Seq != 0 {
		t.Errorf("not bounded: pip %+v pause %q output %+v", st.Program.PiP, st.Program.Pause.Text, st.Output)
	}
	// An older engine does not send them: they are absent (the web hides their controls)
	os.WriteFile(c.StatePath, []byte(stateJSON), 0o644)
	st, _ = c.Read()
	b, _ = json.Marshal(st)
	for _, k := range []string{`"pip"`, `"pause"`, `"locked"`, `"by"`, `"seq"`} {
		if strings.Contains(string(b), k) {
			t.Errorf("older engine: %s present in %s", k, b)
		}
	}
	if c.Image("source-2.jpg") != filepath.Join(dir, "source-2.jpg") || c.Image("../mixer.json") != "" || c.Image("source-5.jpg") != "" {
		t.Error("image list")
	}
}

// Audio: follow video, per-input delay, the audio of each video source (F4) and the mix
func TestReadAudio(t *testing.T) {
	dir := t.TempDir()
	c := &Client{StatePath: filepath.Join(dir, "mixer.json"), RunDir: dir}
	cam := "/dev/v4l/by-id/usb-046d_Logi-video-index0"
	// Three video sources with a slot: hdmi, the Logi and the bars
	sources := `"sources":[{"id":"hdmi","slot":1,"kind":"hdmi-csi","name":"HDMI","state":"live"},` +
		`{"id":"` + cam + `","slot":2,"kind":"usb","name":"Logi","state":"live"},{"id":"test","slot":3,"kind":"test","name":"Barras","state":"live"}]`
	base := stateJSON[:strings.Index(stateJSON, `"sources":`)] + sources + stateJSON[strings.Index(stateJSON, `,"screen":`):]
	read := func(audio string) *Audio {
		t.Helper()
		os.WriteFile(c.StatePath, []byte(strings.Replace(base, `"secret":"x"`, `"audio":`+audio, 1)), 0o644)
		st, err := c.Read()
		if err != nil || st.Audio == nil {
			t.Fatalf("%+v %v", st, err)
		}
		return st.Audio
	}
	// They pass through as is if valid
	a := read(`{"main":"i2s","follow":true,"links":{"hdmi":"i2s","` + cam + `":null,"test":"alsa:Edition"},` +
		`"sources":[{"id":"i2s","slot":1,"kind":"i2s","name":"HDMI","state":"live","delayMs":0},` +
		`{"id":"alsa:Edition","slot":2,"kind":"usb","name":"Edition","state":"live","delayMs":120}]}`)
	if a.Follow == nil || !*a.Follow || len(a.Links) != 3 || *a.Links["hdmi"] != "i2s" || a.Links[cam] != nil || *a.Links["test"] != "alsa:Edition" {
		t.Errorf("audio: %+v", a)
	}
	if d := a.Sources[0].DelayMs; d == nil || *d != 0 {
		t.Errorf("delay 0: %v", d)
	}
	if d := a.Sources[1].DelayMs; d == nil || *d != 120 {
		t.Errorf("delay: %v", d)
	}
	b, _ := json.Marshal(a)
	if !strings.Contains(string(b), `"delayMs":0`) || !strings.Contains(string(b), `"`+cam+`":null`) {
		t.Errorf("delay 0 and the link without audio are present: %s", b)
	}
	// … and bounded if not: delay from 0 to 500, valid values, none = no own audio, and only those
	// of sources with a slot (at most 8, like sources)
	links := `"hdmi":"none","test":"hdmi","` + cam + `":"alsa:x;rm","/etc/passwd":"i2s"`
	for i := range 10 {
		links += fmt.Sprintf(`,"/dev/v4l/by-path/usb-%d":"i2s"`, i)
	}
	a = read(`{"main":null,"links":{` + links + `},"sources":[{"id":"i2s","slot":1,"kind":"i2s","name":"HDMI","state":"live","delayMs":-5,"videoSlot":1},` +
		`{"id":"alsa:Edition","slot":2,"kind":"usb","name":"Edition","state":"live","delayMs":9000,"videoSlot":9}]}`)
	if *a.Sources[0].DelayMs != 0 || *a.Sources[1].DelayMs != MaxAudioDelay {
		t.Errorf("delay not bounded: %d %d", *a.Sources[0].DelayMs, *a.Sources[1].DelayMs)
	}
	// The number of its camera: 1…8 or nothing
	if a.Sources[0].VideoSlot == nil || *a.Sources[0].VideoSlot != 1 || a.Sources[1].VideoSlot != nil {
		t.Errorf("videoSlot not bounded: %v %v", a.Sources[0].VideoSlot, a.Sources[1].VideoSlot)
	}
	if v, ok := a.Links["hdmi"]; !ok || v != nil || len(a.Links) != 1 {
		t.Errorf("links not bounded: %v", a.Links)
	}
	// Mix: the mode and, on each input, whether it is in the mix and its gain (also 0 and false), as is
	// if valid…
	a = read(`{"main":"i2s","mode":"mix","sources":[{"id":"i2s","slot":1,"kind":"i2s","name":"HDMI","state":"live","inMix":true,"gainDb":0},` +
		`{"id":"alsa:Edition","slot":2,"kind":"usb","name":"Edition","state":"live","inMix":false,"gainDb":-6.5}]}`)
	if a.Mode != "mix" || a.Sources[0].InMix == nil || !*a.Sources[0].InMix || a.Sources[1].InMix == nil || *a.Sources[1].InMix ||
		a.Sources[0].GainDb == nil || *a.Sources[0].GainDb != 0 || *a.Sources[1].GainDb != -6.5 {
		t.Errorf("mix: %+v", a)
	}
	b, _ = json.Marshal(a)
	if !strings.Contains(string(b), `"inMix":false,"gainDb":-6.5`) || !strings.Contains(string(b), `"inMix":true,"gainDb":0`) {
		t.Errorf("false and 0 are present: %s", b)
	}
	// … and bounded if not: the mode, from its list (otherwise dropped); the gain, −40 to +12 with one decimal
	a = read(`{"main":"i2s","mode":"surround","sources":[{"id":"i2s","slot":1,"kind":"i2s","name":"HDMI","state":"live","gainDb":-300},` +
		`{"id":"alsa:Edition","slot":2,"kind":"usb","name":"Edition","state":"live","gainDb":99},` +
		`{"id":"alsa:Mic","slot":3,"kind":"usb","name":"Mic","state":"live","gainDb":-3.14159}]}`)
	if a.Mode != "" || *a.Sources[0].GainDb != MinAudioGain || *a.Sources[1].GainDb != MaxAudioGain || *a.Sources[2].GainDb != -3.1 {
		t.Errorf("mix not bounded: %q %v %v %v", a.Mode, *a.Sources[0].GainDb, *a.Sources[1].GainDb, *a.Sources[2].GainDb)
	}
	if b, _ = json.Marshal(a); strings.Contains(string(b), `"mode"`) {
		t.Errorf("unknown mode: present in %s", b)
	}
	// Headphones: as is if valid (also off and 0)…
	a = read(`{"main":"i2s","sources":[],"phones":{"on":false,"gainDb":0,"state":"off"}}`)
	if p := a.Phones; p == nil || p.On || p.GainDb != 0 || p.State != "off" || p.Detail != "" {
		t.Errorf("headphones: %+v", a.Phones)
	}
	if b, _ = json.Marshal(a); !strings.Contains(string(b), `"phones":{"on":false,"gainDb":0,"state":"off"}`) {
		t.Errorf("headphones: %s", b)
	}
	a = read(`{"main":"i2s","sources":[],"phones":{"on":true,"gainDb":-12,"state":"waiting","detail":"Esperando a la pantalla"}}`)
	if p := a.Phones; p.State != "waiting" || p.Detail != "Esperando a la pantalla" {
		t.Errorf("headphones waiting: %+v", p)
	}
	// … and bounded if not: state from its list, volume from −40 to 0, detail of 120 runes
	a = read(`{"main":"i2s","sources":[],"phones":{"on":true,"gainDb":9,"state":"raro","detail":"` + strings.Repeat("ñ", 300) + `"}}`)
	if p := a.Phones; p.State != "off" || p.GainDb != MaxPhonesGain || len([]rune(p.Detail)) != 120 {
		t.Errorf("headphones not bounded: %+v", p)
	}
	if a = read(`{"main":"i2s","sources":[],"phones":{"on":true,"gainDb":-90,"state":"on"}}`); a.Phones.GainDb != MinPhonesGain {
		t.Errorf("volume not bounded: %d", a.Phones.GainDb)
	}
	// An older engine does not send them: they are absent (the web hides their controls)
	a = read(`{"main":"i2s","sources":[{"id":"i2s","slot":1,"kind":"i2s","name":"HDMI","state":"live"}]}`)
	b, _ = json.Marshal(a)
	for _, k := range []string{`"follow"`, `"links"`, `"delayMs"`, `"mode"`, `"inMix"`, `"gainDb"`, `"phones"`} {
		if strings.Contains(string(b), k) {
			t.Errorf("older engine: %s present in %s", k, b)
		}
	}
}

// screen.power: only reboot or poweroff with a valid seq; anything else is discarded
func TestReadPower(t *testing.T) {
	for in, ok := range map[string]bool{
		`{"want":"reboot","seq":1,"at":1.5}`: true, `{"want":"poweroff","seq":3,"at":1}`: true,
		`{"want":"halt","seq":1,"at":1}`: false, `{"want":"reboot","seq":0,"at":1}`: false,
	} {
		var s State
		if err := json.Unmarshal([]byte(`{"v":1,"screen":{"state":"on","power":`+in+`}}`), &s); err != nil {
			t.Fatal(err)
		}
		got := clean(&s).Screen.Power != nil
		if got != ok {
			t.Errorf("%s: %v", in, got)
		}
	}
}

func TestCleanRedactsOutputDetail(t *testing.T) {
	st := clean(&State{Output: &Output{State: "connecting", Detail: "rejecting: streamid=publish:clave:pau&Passphrase=abc publish:otra"}})
	for _, secret := range []string{"clave", "abc", "otra"} {
		if strings.Contains(st.Output.Detail, secret) {
			t.Fatalf("the detail carries a secret (%q): %q", secret, st.Output.Detail)
		}
	}
}

// Detail codes and their arguments reach the web bounded: a code of the list's shape, plain argument names, one
// line of ≤ 200 runes per value, secrets redacted; no arguments without a code
func TestCleanDetailCodes(t *testing.T) {
	long := strings.Repeat("x", 300)
	st := clean(&State{
		Sources: []Source{
			{ID: "a", DetailCode: "frames_stopped", DetailArgs: map[string]string{"seconds": "3"}},
			{ID: "b", DetailCode: "Bad Code!", DetailArgs: map[string]string{"x": "y"}},
			{ID: "c", DetailCode: "stream_error", DetailArgs: map[string]string{"error": "line1\nline2 streamid=publish:secret " + long, "bad key": "z"}},
			{ID: "d", DetailCode: "", DetailArgs: map[string]string{"seconds": "3"}},
		},
		Audio: &Audio{Sources: []AudioInput{{ID: "i2s", DetailCode: "no_samples", DetailArgs: map[string]string{"seconds": "3"}}},
			Phones: &Phones{State: "waiting", DetailCode: "phones_waiting_screen"}},
	})
	if s := st.Sources[0]; s.DetailCode != "frames_stopped" || s.DetailArgs["seconds"] != "3" {
		t.Errorf("a valid code changed: %+v", s)
	}
	if s := st.Sources[1]; s.DetailCode != "" || s.DetailArgs != nil {
		t.Errorf("an invalid code passed: %+v", s)
	}
	c := st.Sources[2]
	e := c.DetailArgs["error"]
	if strings.Contains(e, "\n") || strings.Contains(e, "secret") || len([]rune(e)) > 200 {
		t.Errorf("the error argument is not bounded: %q", e)
	}
	if _, ok := c.DetailArgs["bad key"]; ok {
		t.Errorf("an argument with a bad name passed: %+v", c.DetailArgs)
	}
	if s := st.Sources[3]; s.DetailArgs != nil {
		t.Errorf("arguments without a code: %+v", s)
	}
	if in := st.Audio.Sources[0]; in.DetailCode != "no_samples" || in.DetailArgs["seconds"] != "3" {
		t.Errorf("audio input: %+v", in)
	}
	if p := st.Audio.Phones; p.DetailCode != "phones_waiting_screen" || p.DetailArgs != nil {
		t.Errorf("phones: %+v", p)
	}
}

// The slots command: every slot once within 1…4, a valid assign and alias, no source pinned twice
func TestValidateSlots(t *testing.T) {
	cam := "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0004C523E9000-video-index0"
	ok := [][]SlotAssign{
		{{Slot: 1, Assign: "auto"}},
		{{Slot: 1, Assign: "hdmi", Alias: "Main camera"}, {Slot: 2, Assign: cam}, {Slot: 3, Assign: "net:cam1", Alias: "Móvil de Ana"},
			{Slot: 4, Assign: "off"}},
		{{Slot: 2, Assign: "test", Alias: "Rock & Roll (#2) +1/x"}},
		{{Slot: 1, Assign: "auto", Alias: strings.Repeat("ñ", 24)}},
	}
	for _, list := range ok {
		if err := (Command{Cmd: "slots", Slots: list}).Validate(); err != nil {
			t.Errorf("%+v: %v", list, err)
		}
	}
	bad := [][]SlotAssign{
		nil, {},
		{{Slot: 0, Assign: "auto"}}, {{Slot: 5, Assign: "auto"}},
		{{Slot: 1, Assign: "auto"}, {Slot: 1, Assign: "off"}},
		{{Slot: 1, Assign: "hdmi"}, {Slot: 2, Assign: "hdmi"}},
		{{Slot: 1, Assign: "usb:" + cam}}, {{Slot: 1, Assign: "/dev/video0"}}, {{Slot: 1, Assign: "net:cam9"}},
		{{Slot: 1, Assign: "auto", Alias: strings.Repeat("a", 25)}}, {{Slot: 1, Assign: "auto", Alias: "a<b"}},
		{{Slot: 1, Assign: "auto", Alias: "line\nbreak"}}, {{Slot: 1, Assign: "auto", Alias: "🙂"}},
		{{Slot: 1, Assign: "auto"}, {Slot: 2, Assign: "auto"}, {Slot: 3, Assign: "auto"}, {Slot: 4, Assign: "auto"}, {Slot: 4, Assign: "off"}},
	}
	for _, list := range bad {
		if (Command{Cmd: "slots", Slots: list}).Validate() == nil {
			t.Errorf("%+v: should be rejected", list)
		}
	}
	// slots only in the slots command
	if (Command{Cmd: "take", Source: "hdmi", Slots: []SlotAssign{{Slot: 1, Assign: "auto"}}}).Validate() == nil {
		t.Error("slots in take should be rejected")
	}
}

// The new state fields are bounded like everything else from the engine
func TestCleanSlotsAndCatalog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixer.json")
	os.WriteFile(path, []byte(`{"v":1,"program":{"main":"hdmi","layout":"full"},"slots":4,`+
		`"sources":[{"id":"hdmi","slot":1,"kind":"hdmi-csi","name":"HDMI","alias":"Main"},{"id":"test","slot":2,"name":"Bars","alias":"<script>"}],`+
		`"slotConfig":[{"slot":1,"assign":"hdmi","alias":"Main","present":true},{"slot":1,"assign":"off","alias":""},`+
		`{"slot":2,"assign":"/dev/video0","alias":""},{"slot":3,"assign":"net:cam1","alias":"bad\u0007","present":false},{"slot":9,"assign":"auto"}],`+
		`"catalog":[{"id":"hdmi","name":"HDMI\u0000 board","kind":"hdmi-csi"},{"id":"hdmi","name":"dup","kind":"hdmi-csi"},`+
		`{"id":"/etc/passwd","name":"x","kind":"usb"},{"id":"net:cam1","name":"Phone","kind":"weird"}],`+
		`"screen":{"state":"off"}}`), 0o644)
	st, err := (&Client{StatePath: path}).Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Sources[0].Alias != "Main" || st.Sources[1].Alias != "" {
		t.Errorf("aliases: %+v", st.Sources)
	}
	if len(st.SlotConfig) != 2 || st.SlotConfig[0].Assign != "hdmi" || !st.SlotConfig[0].Present ||
		st.SlotConfig[1].Slot != 3 || st.SlotConfig[1].Alias != "" {
		t.Errorf("slotConfig: %+v", st.SlotConfig)
	}
	if len(st.Catalog) != 2 || st.Catalog[0].Name != "HDMI board" || st.Catalog[1].ID != "net:cam1" || st.Catalog[1].Kind != "" {
		t.Errorf("catalog: %+v", st.Catalog)
	}
	// An older engine: no slotConfig/catalog at all (nil, so the API knows)
	os.WriteFile(path, []byte(stateJSON), 0o644)
	if st, err := (&Client{StatePath: path}).Read(); err != nil || st.SlotConfig != nil || st.Catalog != nil {
		t.Errorf("older engine: %+v %v", st, err)
	}
}

// The cameras' image settings (SOURCES.md §7.3): a control of the table with an integer value, or a reset; a source
// id of the catalogue and nothing from other commands
func TestValidateCamera(t *testing.T) {
	cam := "/dev/v4l/by-id/usb-046d_Logi_4K_Stream_Edition_0000ABCD-video-index0"
	v := func(n int) *int { return &n }
	yes := true
	ok := []Command{
		{Cmd: "camera_control", Source: cam, Control: "brightness", Value: v(140)},
		{Cmd: "camera_control", Source: cam, Control: "exposure_auto", Value: v(0)},
		{Cmd: "camera_control", Source: cam, Control: "pan", Value: v(-36000)},
		{Cmd: "camera_control", Source: cam, Control: "wb_temperature", Value: v(9999)}, // the engine brings it into range
		{Cmd: "camera_control", Source: "net:cam1", Control: "zoom", Value: v(100)},     // the engine answers unsupported
		{Cmd: "camera_reset", Source: cam}, {Cmd: "camera_reset", Source: "hdmi"},
	}
	for id := range ControlIDs {
		ok = append(ok, Command{Cmd: "camera_control", Source: cam, Control: id, Value: v(1)})
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []Command{
		{Cmd: "camera_control", Source: cam, Control: "brightness"}, // no value
		{Cmd: "camera_control", Source: cam, Value: v(1)},           // no control
		{Cmd: "camera_control", Control: "brightness", Value: v(1)}, // no source
		{Cmd: "camera_control", Source: "/dev/video0", Control: "brightness", Value: v(1)},
		{Cmd: "camera_control", Source: cam, Control: "exposure_auto_priority", Value: v(1)}, // kept by the engine, not shown
		{Cmd: "camera_control", Source: cam, Control: "Brightness", Value: v(1)},
		{Cmd: "camera_control", Source: cam, Control: "brightness;rm", Value: v(1)},
		{Cmd: "camera_control", Source: cam, Control: "brightness", Value: v(1 << 31)},
		{Cmd: "camera_control", Source: cam, Control: "brightness", Value: v(-1<<31 - 1)},
		{Cmd: "camera_control", Source: cam, Control: "brightness", Value: v(1), On: &yes},
		{Cmd: "camera_reset"}, {Cmd: "camera_reset", Source: "/etc/passwd"},
		{Cmd: "camera_reset", Source: cam, Control: "brightness"}, {Cmd: "camera_reset", Source: cam, Value: v(0)},
		// control and value only in camera_control
		{Cmd: "take", Source: cam, Control: "brightness"}, {Cmd: "audio_delay", Source: "i2s", Ms: v(0), Value: v(0)},
	}
	for _, c := range bad {
		if c.Validate() == nil {
			t.Errorf("%+v: should be rejected", c)
		}
	}
	// The value 0 reaches the engine (it is not dropped as empty)
	var got map[string]any
	sock := fakeEngine(t, func(req map[string]any) []string { got = req; return []string{`{"id":1,"ok":true}`} })
	if err := (&Client{Sock: sock}).Do(context.Background(), Command{Cmd: "camera_control", Source: cam, Control: "gain", Value: v(0)}); err != nil ||
		got["control"] != "gain" || got["value"] != 0.0 || got["source"] != cam {
		t.Errorf("value 0: %v %v", err, got)
	}
}

// The controls of each source reach the web bounded: ids of the table once, groups and types from their lists, a
// sound range with the value and the default inside it, bools 0…1 and menus with their options
func TestCleanControls(t *testing.T) {
	ctl := func(id, group, typ string, lo, hi, step, def, val int, opts ...int) Control {
		return Control{ID: id, Group: group, Type: typ, Min: lo, Max: hi, Step: step, Default: def, Value: val, Options: opts}
	}
	in := []Control{
		ctl("brightness", "image", "int", 0, 255, 1, 128, 140),
		ctl("brightness", "image", "int", 0, 255, 1, 128, 10),                   // twice: the first one stays
		ctl("exposure_auto_priority", "exposure", "bool", 0, 1, 1, 0, 0),        // not in the table
		ctl("contrast", "colour", "int", 0, 255, 1, 128, 128),                   // unknown group
		ctl("saturation", "image", "float", 0, 255, 1, 128, 128),                // unknown type
		ctl("hue", "image", "int", 10, -10, 1, 0, 0),                            // min > max
		ctl("sharpness", "image", "int", 0, 255, 0, 999, -5),                    // step 0, default and value out of range
		ctl("exposure_auto", "exposure", "bool", 0, 1, 1, 1, 7),                 // bool value out of range
		ctl("backlight", "exposure", "bool", 0, 2, 1, 1, 1),                     // a bool that is not 0…1
		ctl("gain", "exposure", "int", 0, 255, 1, 0, 0, 1, 2),                   // options only on a menu
		ctl("power_line", "exposure", "menu", 0, 3, 1, 2, 1, 0, 1, 1, 2, 9, -1), // duplicates and out of range dropped
		ctl("wb_auto", "white_balance", "menu", 0, 3, 1, 0, 0),                  // a menu without options
		ctl("focus", "framing", "int", 0, 255, 5, 0, 30),
	}
	in[len(in)-1].Inactive = true
	st := clean(&State{Sources: []Source{{ID: "cam", Controls: in}, {ID: "hdmi"}}})
	got := map[string]Control{}
	var ids []string
	for _, c := range st.Sources[0].Controls {
		got[c.ID] = c
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "brightness,sharpness,exposure_auto,gain,power_line,focus" {
		t.Fatalf("controls kept: %v", ids)
	}
	if c := got["brightness"]; c.Value != 140 {
		t.Errorf("brightness: %+v", c)
	}
	if c := got["sharpness"]; c.Step != 1 || c.Default != 255 || c.Value != 0 {
		t.Errorf("sharpness not bounded: %+v", c)
	}
	if c := got["exposure_auto"]; c.Value != 1 {
		t.Errorf("bool not bounded: %+v", c)
	}
	if c := got["gain"]; c.Options != nil {
		t.Errorf("options on an int: %+v", c)
	}
	if c := got["power_line"]; fmt.Sprint(c.Options) != "[0 1 2]" {
		t.Errorf("menu options: %v", c.Options)
	}
	if c := got["focus"]; !c.Inactive || c.Step != 5 {
		t.Errorf("focus: %+v", c)
	}
	// At most 8 options and 24 controls
	many := ctl("power_line", "exposure", "menu", 0, 100, 1, 0, 0)
	for i := range 20 {
		many.Options = append(many.Options, i)
	}
	long := make([]Control, 30)
	for i := range long {
		long[i] = ctl("zoom", "framing", "int", 100, 500, 1, 100, 100)
	}
	long[25] = ctl("tilt", "framing", "int", -36000, 36000, 3600, 0, 0) // beyond the 24th: not read
	st = clean(&State{Sources: []Source{{ID: "a", Controls: []Control{many}}, {ID: "b", Controls: long}}})
	if n := len(st.Sources[0].Controls[0].Options); n != MaxMenuOptions {
		t.Errorf("menu options: %d", n)
	}
	if c := st.Sources[1].Controls; len(c) != 1 || c[0].ID != "zoom" {
		t.Errorf("long list: %+v", c)
	}
	// Nothing valid, or nothing sent: the key is absent (the web hides the card)
	st = clean(&State{Sources: []Source{{ID: "a", Controls: []Control{ctl("x", "image", "int", 0, 1, 1, 0, 0)}}, {ID: "b"}}})
	b, _ := json.Marshal(st.Sources)
	if strings.Contains(string(b), `"controls"`) {
		t.Errorf("controls present: %s", b)
	}
	// Through the state file, as the engine writes it: inactive only when true, options only on the menu
	dir := t.TempDir()
	path := filepath.Join(dir, "mixer.json")
	os.WriteFile(path, []byte(`{"v":1,"program":{},"slots":1,"screen":{},"sources":[{"id":"/dev/v4l/by-id/usb-x-video-index0","slot":1,`+
		`"kind":"usb","name":"Logi","state":"live","controls":[{"id":"power_line","group":"exposure","type":"menu","min":0,"max":2,`+
		`"step":1,"default":2,"value":1,"options":[0,1,2]},{"id":"wb_temperature","group":"white_balance","type":"int","min":2000,`+
		`"max":7500,"step":10,"default":4000,"value":3320,"inactive":true},{"id":"gain","group":"exposure","type":"int","min":0,`+
		`"max":255,"step":1,"default":0,"value":0}]}]}`), 0o644)
	rd, err := (&Client{StatePath: path}).Read()
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(rd.Sources[0].Controls)
	want := `[{"id":"power_line","group":"exposure","type":"menu","min":0,"max":2,"step":1,"default":2,"value":1,"options":[0,1,2]},` +
		`{"id":"wb_temperature","group":"white_balance","type":"int","min":2000,"max":7500,"step":10,"default":4000,"value":3320,"inactive":true},` +
		`{"id":"gain","group":"exposure","type":"int","min":0,"max":255,"step":1,"default":0,"value":0}]`
	if string(b) != want {
		t.Errorf("controls:\n%s\nwant\n%s", b, want)
	}
}
