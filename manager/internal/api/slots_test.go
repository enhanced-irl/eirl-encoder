package api

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

const cam = "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0"

// slotsEngine: a fake engine that answers the slots command with reply (ok, unknown_command, on_air…).
func slotsEngine(t *testing.T, run string) (*mixer.Client, func(string), func() []string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mxs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	reply := "ok"
	var got []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				mu.Lock()
				got = append(got, sc.Text())
				r := reply
				mu.Unlock()
				if r == "ok" {
					c.Write([]byte(`{"id":1,"ok":true}` + "\n"))
				} else {
					c.Write([]byte(`{"id":1,"ok":false,"error":"` + r + `"}` + "\n"))
				}
			}
			c.Close()
		}
	}()
	return &mixer.Client{Sock: filepath.Join(dir, "s"), StatePath: filepath.Join(run, "mixer.json"), RunDir: run},
		func(r string) { mu.Lock(); reply = r; mu.Unlock() },
		func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) }
}

func TestSlotsAPI(t *testing.T) {
	e := setup(t, true)
	withDevice(t, e)
	tok := e.login(t)
	run := t.TempDir()
	cl, setReply, sent := slotsEngine(t, run)
	e.srv.Mixer = cl

	// An older engine (no catalog, no slotConfig): candidates = the sources now; engineSupports false
	os.WriteFile(filepath.Join(run, "mixer.json"), []byte(`{"v":1,"program":{"main":"hdmi","layout":"full"},"slots":4,`+
		`"sources":[{"id":"hdmi","slot":1,"kind":"hdmi-csi","name":"HDMI","state":"live"}],"screen":{"state":"off"}}`), 0o644)
	w := e.do("GET", "/api/v1/sources/slots", "", tok)
	var v slotsView
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || len(v.Slots) != 4 || v.Slots[0].Current == nil || v.Slots[0].Current.ID != "hdmi" || v.EngineSupports ||
		len(v.Candidates) != 1 || v.Candidates[0].ID != "hdmi" || !v.Candidates[0].Connected {
		t.Fatalf("older engine: %d %s", w.Code, w.Body)
	}
	// It answers unknown_command: saved all the same, engineSupports false
	setReply("unknown_command")
	w = e.do("PUT", "/api/v1/sources/slots", `{"slots":[{"slot":1,"assign":"hdmi","alias":" Main "},{"slot":2,"assign":"`+cam+`","alias":""}]}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"engineSupports":false`) || !strings.Contains(w.Body.String(), `"applied":false`) {
		t.Fatalf("unknown_command: %d %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(e.srv.SlotsPath)
	if !strings.Contains(string(b), `"alias": "Main"`) || !strings.Contains(string(b), `"slot": 4`) {
		t.Errorf("slots.json: %s", b)
	}
	if s := sent(); len(s) != 1 || !strings.Contains(s[0], `"cmd":"slots"`) || !strings.Contains(s[0], `"alias":"Main"`) {
		t.Errorf("command sent: %v", s)
	}

	// A new engine: catalog + slotConfig; the pinned USB camera is absent (connected false, readable name)
	os.WriteFile(filepath.Join(run, "mixer.json"), []byte(`{"v":1,"program":{"main":"hdmi","layout":"full"},"slots":4,`+
		`"slotConfig":[{"slot":1,"assign":"hdmi","alias":"Main","present":true},{"slot":2,"assign":"`+cam+`","alias":"","present":false},`+
		`{"slot":3,"assign":"auto","alias":"","present":true},{"slot":4,"assign":"auto","alias":"","present":false}],`+
		`"catalog":[{"id":"hdmi","name":"HDMI","kind":"hdmi-csi"},{"id":"net:cam1","name":"Phone","kind":"network"},{"id":"test","name":"Bars","kind":"test"}],`+
		`"sources":[{"id":"hdmi","slot":1,"kind":"hdmi-csi","name":"HDMI","alias":"Main","state":"live"},`+
		`{"id":"net:cam1","slot":3,"kind":"network","name":"Phone","alias":"","state":"live"}],"screen":{"state":"off"}}`), 0o644)
	w = e.do("GET", "/api/v1/sources/slots", "", tok)
	v = slotsView{}
	json.Unmarshal(w.Body.Bytes(), &v)
	if !v.EngineSupports || len(v.Candidates) != 4 || v.Candidates[3].ID != cam || v.Candidates[3].Connected ||
		v.Candidates[3].Name != "Elgato Cam Link 4K 0005AB12" || v.Candidates[3].Kind != "usb" || v.Slots[2].Current == nil ||
		v.Slots[2].Current.ID != "net:cam1" || v.Slots[1].Current != nil {
		t.Errorf("new engine: %s", w.Body)
	}
	// Validation: duplicate with its code, a prefixed USB id, a long alias
	w = e.do("PUT", "/api/v1/sources/slots", `{"slots":[{"slot":1,"assign":"hdmi","alias":""},{"slot":3,"assign":"hdmi","alias":""},`+
		`{"slot":2,"assign":"usb:`+cam+`","alias":"`+strings.Repeat("x", 25)+`"}]}`, tok, "Accept-Language", "fr")
	body := w.Body.String()
	for _, want := range []string{`"slots.3.assign":"cette source est déjà attribuée à la source 1"`, `"slots.3.assign":"duplicate"`,
		`"slots.2.assign":"invalid"`, `"slots.2.alias":"invalid"`} {
		if w.Code != 400 || !strings.Contains(body, want) {
			t.Errorf("validation: %d %s, want %s", w.Code, body, want)
		}
	}
	// on_air: 409, and the previous file is back
	before, _ := os.ReadFile(e.srv.SlotsPath)
	setReply("on_air")
	w = e.do("PUT", "/api/v1/sources/slots", `{"slots":[{"slot":1,"assign":"off","alias":""}]}`, tok, "Accept-Language", "en-GB")
	after, _ := os.ReadFile(e.srv.SlotsPath)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"error":"on_air"`) || !strings.Contains(w.Body.String(), "stop streaming to move") ||
		string(before) != string(after) {
		t.Errorf("on_air: %d %s\n%s", w.Code, w.Body, after)
	}
	// ok: applied
	setReply("ok")
	w = e.do("PUT", "/api/v1/sources/slots", `{"slots":[{"slot":1,"assign":"auto","alias":"Cam A"},{"slot":2,"assign":"off","alias":""}]}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":true`) || !strings.Contains(w.Body.String(), `"engineSupports":true`) {
		t.Errorf("ok: %d %s", w.Code, w.Body)
	}
	// slots through POST /mixer: no (the app owns the file)
	if w := e.do("POST", "/api/v1/mixer", `{"cmd":"slots","slots":[{"slot":1,"assign":"auto","alias":""}]}`, tok); w.Code != 400 {
		t.Errorf("slots via POST /mixer: %d %s", w.Code, w.Body)
	}
	// CSRF
	if w := e.do("PUT", "/api/v1/sources/slots", `{"slots":[]}`, tok, "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("cross-origin PUT: %d", w.Code)
	}
	// Engine stopped: saved, applied false
	os.Remove(filepath.Join(run, "mixer.json"))
	e.srv.Mixer = &mixer.Client{Sock: filepath.Join(run, "none"), StatePath: filepath.Join(run, "mixer.json")}
	if w := e.do("PUT", "/api/v1/sources/slots", `{"slots":[{"slot":4,"assign":"net:cam2","alias":"Drone"}]}`, tok); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"applied":false`) || !strings.Contains(w.Body.String(), `{"id":"net:cam2","name":"cam2","kind":"network","connected":false}`) {
		t.Errorf("engine stopped: %d %s", w.Code, w.Body)
	}
}
