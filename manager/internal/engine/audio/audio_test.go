package audio

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Ports of scripts/test_engine_audio.py (AudioTest, FollowTest), one Go test per Python test.

const level = "level, endtime=(guint64)1200000000, timestamp=(guint64)1000000000, stream-time=(guint64)1000000000, " +
	"running-time=(guint64)1000000000, duration=(guint64)200000000, rms=(GValueArray)< -31.5, -30.2 >, " +
	"peak=(GValueArray)< -12.25, -9.5 >, decay=(GValueArray)< -14.0, -11.0 >;"

func TestAudioDesc(t *testing.T) {
	d := Desc(KindI2S, "hw:tegrasndt210ref,1", "")
	if !strings.Contains(d, "alsasrc device=hw:tegrasndt210ref,1 provide-clock=false") || strings.Contains(d, "audioresample") {
		t.Errorf("i2s: %s", d)
	}
	u := Desc(KindUSB, "hw:Edition,0", "")
	if !strings.Contains(u, "audioconvert ! audioresample") {
		t.Errorf("usb: %s", u)
	}
	for _, x := range []string{d, u} {
		if !strings.Contains(x, "format=S16LE,rate=48000,channels=2") || !strings.Contains(x, "appsink name=snk") {
			t.Errorf("caps/appsink: %s", x)
		}
	}
	// New: a network camera's audio track (engine_net.audio_head + the usual tail)
	n := Desc(KindNetwork, "rtsp://127.0.0.1:8554/cam1", "aac")
	want := "rtspsrc name=cap location=rtsp://127.0.0.1:8554/cam1 protocols=tcp latency=100 drop-on-latency=true " +
		"! rtpmp4gdepay ! aacparse ! avdec_aac ! audioconvert ! audioresample ! " + Caps
	if !strings.HasPrefix(n, want) || !strings.Contains(Desc(KindNetwork, "x", "opus"), "rtpopusdepay ! opusdec ! audioconvert") {
		t.Errorf("network: %s", n)
	}
}

func TestAudioPeak(t *testing.T) {
	if p, ok := ParsePeak(level); !ok || p != -9.5 {
		t.Errorf("level: %v %v", p, ok)
	}
	if _, ok := ParsePeak("level, rms=(GValueArray)< -1 >;"); ok {
		t.Error("no peak field")
	}
	if p, ok := ParsePeak("peak=(GValueArray)< -inf, -60 >"); !ok || p != -60 {
		t.Errorf("-inf: %v %v", p, ok)
	}
	// New: every channel, floored at -90 (NaN too, like Python's max); junk gives nothing
	if got := ParsePeaks("peak=(GValueArray)< -inf, nan, -3 >"); !reflect.DeepEqual(got, []float64{-90, -90, -3}) {
		t.Errorf("peaks: %v", got)
	}
	for _, bad := range []string{"peak=(GValueArray)< >", "peak=(GValueArray)< -1, x >", ""} {
		if got := ParsePeaks(bad); got != nil {
			t.Errorf("%q: %v", bad, got)
		}
	}
}

func TestAudioFade(t *testing.T) {
	if got := Blend(map[int]float64{0: 1}, map[int]float64{2: 1}, 0.25); !reflect.DeepEqual(got, map[int]float64{0: 0.75, 2: 0.25}) {
		t.Errorf("blend: %v", got)
	}
	if got := Blend(map[int]float64{1: 0.5}, map[int]float64{1: 1}, 2); !reflect.DeepEqual(got, map[int]float64{1: 1}) {
		t.Errorf("clamped: %v", got)
	}
}

func near(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestAudioTargets(t *testing.T) {
	ins := []SlotID{{0, "i2s"}, {1, "alsa:C4K"}, {2, "alsa:Mic"}}
	// Single: only the one on air, with its gain (−6 dB ≈ 0.5)
	v := TargetVolumes(ins, ModeSingle, "alsa:C4K", map[string]bool{"i2s": true}, map[string]float64{"alsa:C4K": -6}, false)
	if v[0] != 0 || v[2] != 0 || !near(v[1], 0.501, 0.005) {
		t.Errorf("single: %v", v)
	}
	// Mix: the marked ones, each with its own; the one on air in unless taken out
	v = TargetVolumes(ins, ModeMix, "alsa:C4K", map[string]bool{"alsa:Mic": true}, map[string]float64{"alsa:Mic": 6}, false)
	if v[0] != 0 || v[1] != 1 || !near(v[2], 1.995, 0.005) {
		t.Errorf("mix: %v", v)
	}
	v = TargetVolumes(ins, ModeMix, "alsa:C4K", map[string]bool{"alsa:C4K": false}, nil, false)
	if !reflect.DeepEqual(v, map[int]float64{0: 0, 1: 0, 2: 0}) {
		t.Errorf("mix without main: %v", v)
	}
	// The Pause with silence: all to 0
	for _, x := range TargetVolumes(ins, ModeMix, "i2s", map[string]bool{"alsa:Mic": true}, nil, true) {
		if x != 0 {
			t.Errorf("silenced: %v", x)
		}
	}
}

func TestAudioGain(t *testing.T) {
	for _, c := range []struct {
		in   any
		want float64
	}{{0, 0}, {-40, -40}, {12, 12}, {-3.25, -3.2}, {json.Number("-3.25"), -3.2}, {json.Number("0.35"), 0.3}, {json.Number("6"), 6}} {
		if g, ok := GainDB(c.in); !ok || g != c.want {
			t.Errorf("%v: %v %v", c.in, g, ok)
		}
	}
	for _, bad := range []any{-41, 12.5, "3", true, nil, math.NaN(), json.Number("1e999"), json.Number("x")} {
		if g, ok := GainDB(bad); ok {
			t.Errorf("%v accepted: %v", bad, g)
		}
	}
}

func TestAudioIDs(t *testing.T) {
	for _, ok := range []string{"none", "i2s", "alsa:Edition", "alsa:C4K", "net:cam1"} {
		if !ValidID(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"alsa:", "alsa:x;rm", "hw:0", "/dev/snd", "i2s\n", "net:cam9"} {
		if ValidID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

var followVideos = []Video{{ID: "hdmi", Slot: 1, Kind: "hdmi-csi"},
	{ID: "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K-video-index0", Slot: 2, Kind: "usb", Card: "C4K"},
	{ID: "/dev/v4l/by-id/usb-bars", Slot: 3, Kind: "usb"}, {ID: "test", Slot: 4, Kind: "test"}}

func TestFollowDefaultLinksAreTheSameDevice(t *testing.T) {
	links := ResolveLinks(followVideos, nil)
	want := map[string]string{"hdmi": "i2s", "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K-video-index0": "alsa:C4K",
		"/dev/v4l/by-id/usb-bars": "", "test": ""} // USB without a sound card; the bars
	if !reflect.DeepEqual(links, want) {
		t.Errorf("links: %v", links)
	}
	if got := DefaultLink("usb", "x;rm"); got != "" { // a card with an odd name: nothing
		t.Errorf("odd card: %q", got)
	}
	if got := DefaultLink("network", "net:cam2"); got != "net:cam2" { // new: its audio has its id
		t.Errorf("network: %q", got)
	}
}

func TestFollowOverrides(t *testing.T) {
	links := ResolveLinks(followVideos, map[string]string{"hdmi": "alsa:Edition", "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K-video-index0": ""})
	if links["hdmi"] != "alsa:Edition" { // a separate microphone for the HDMI camera
		t.Errorf("hdmi: %q", links["hdmi"])
	}
	if links["/dev/v4l/by-id/usb-Elgato_Cam_Link_4K-video-index0"] != "" {
		t.Error("override to none")
	}
	if FollowTarget(links, "hdmi") != "alsa:Edition" || FollowTarget(links, "test") != "" {
		t.Error("follow target") // without its own audio: the one sounding keeps sounding
	}
}

func TestFollowInputsInCameraOrder(t *testing.T) {
	// Audio slots: the I2S first, then the USB ones as detected; cameras: HDMI 1, Logi 2, Cam Link 3
	same := SameDevice([]Video{{Slot: 1, Kind: "hdmi-csi"}, {Slot: 2, Kind: "usb", Card: "Edition"},
		{Slot: 3, Kind: "usb", Card: "C4K"}, {Slot: 4, Kind: "test"}})
	if !reflect.DeepEqual(same, map[string]int{"i2s": 1, "alsa:Edition": 2, "alsa:C4K": 3}) {
		t.Errorf("same: %v", same)
	}
	infos := []InputState{{ID: "i2s", Slot: 1}, {ID: "alsa:C4K", Slot: 2}, {ID: "alsa:Mic", Slot: 3}, {ID: "alsa:Edition", Slot: 4}}
	got := OrderInputs(infos, same)
	var ids []string
	var vs []any
	for _, d := range got {
		ids = append(ids, d.ID)
		if d.VideoSlot != nil {
			vs = append(vs, *d.VideoSlot)
		} else {
			vs = append(vs, nil)
		}
	}
	if !reflect.DeepEqual(ids, []string{"i2s", "alsa:Edition", "alsa:C4K", "alsa:Mic"}) { // the loose mic, last
		t.Errorf("order: %v", ids)
	}
	if !reflect.DeepEqual(vs, []any{1, 2, 3, nil}) {
		t.Errorf("videoSlot: %v", vs)
	}
	if infos[0].VideoSlot != nil {
		t.Error("OrderInputs changed its input")
	}
}

func TestFollowDelay(t *testing.T) {
	for _, ok := range []any{0, 10, 500, json.Number("80")} {
		if _, good := DelayMs(ok); !good {
			t.Errorf("%v rejected", ok)
		}
	}
	for _, bad := range []any{-1, 501, 12.5, "100", true, nil, json.Number("80.0"), json.Number("1e2"), float64(80)} {
		if ms, good := DelayMs(bad); good {
			t.Errorf("%v accepted: %d", bad, ms)
		}
	}
}

func TestPyRound1(t *testing.T) {
	for in, want := range map[float64]float64{-3.25: -3.2, 0.35: 0.3, 0.25: 0.2, 2.675: 2.7, 1.45: 1.4, -12.25: -12.2} {
		if got := pyRound1(in); got != want {
			t.Errorf("round(%v, 1) = %v, Python says %v", in, got, want)
		}
	}
}
