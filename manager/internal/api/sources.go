package api

import (
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/hw"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
)

// --- Sources (SOURCES.md) -------------------------------------------------------------------

// sourceView: a source of the catalogue and, if USB, what it would be captured with on "auto" (the same
// rule the engine applies with `eirl-encoderd probe`) and everything that can be asked of it.
type sourceView struct {
	sources.Source
	Auto    *choiceView  `json:"auto,omitempty"`
	Options []choiceView `json:"options,omitempty"`
}

type choiceView struct {
	Format    string  `json:"format"`
	Mode      string  `json:"mode"` // WxH@fps, the VIDEO_MODE value that asks for it
	FPS       float64 `json:"fps"`
	OutWidth  int     `json:"outWidth"` // what is streamed (above 1080p, scaled down)
	OutHeight int     `json:"outHeight"`
	BusShare  float64 `json:"busShare,omitempty"` // share of USB 3 it takes uncompressed
}

// getSources: GET /api/v1/sources. What is connected now: the board's HDMI, the USB capture devices and
// cameras, the test pattern and the audio inputs.
func (s *Server) getSources(w http.ResponseWriter, r *http.Request) {
	if s.Sources == nil {
		fail(w, r, 503, "sources_disabled", "sources.disabled")
		return
	}
	cat := s.Sources.Scan()
	out := struct {
		Sources []sourceView         `json:"sources"`
		Audio   []sources.AudioInput `json:"audio"`
	}{Sources: []sourceView{}, Audio: cat.Audio}
	lang := reqLang(r)
	for i := range cat.Audio {
		cat.Audio[i].Name = sourceName(lang, cat.Audio[i].ID, cat.Audio[i].Name)
	}
	for _, src := range cat.Sources {
		src.Name = sourceName(lang, src.ID, src.Name)
		v := sourceView{Source: src}
		if src.Kind == sources.KindUSB {
			for _, c := range sources.Options(src) {
				v.Options = append(v.Options, newChoiceView(c))
			}
			if len(v.Options) > 0 {
				v.Auto = &v.Options[0] // Options are in the order of the "auto" rule
			}
		}
		out.Sources = append(out.Sources, v)
	}
	out.Sources = append(out.Sources, sourceView{Source: sources.Source{ID: "test", Kind: sources.KindTest, Name: i18n.Msg(reqLang(r), "sources.test_pattern")}})
	writeJSON(w, 200, out)
}

// sourceName: the catalogue's default names (sources.HDMIName…, data in Spanish that the engine also shows) in the
// request language; any other name (a device's own, a network camera's) as is.
func sourceName(lang, id, name string) string {
	switch {
	case id == "hdmi" && name == sources.HDMIName:
		return i18n.Msg(lang, "sources.hdmi")
	case id == "i2s" && name == sources.HDMIAudioName:
		return i18n.Msg(lang, "sources.hdmi_audio")
	case strings.HasPrefix(name, sources.USBNamePrefix):
		return i18n.Msg(lang, "sources.usb_camera", strings.TrimPrefix(name, sources.USBNamePrefix))
	}
	return name
}

func newChoiceView(c sources.Choice) choiceView {
	ow, oh := sources.OutputSize(c.Width, c.Height)
	return choiceView{Format: c.Format, Mode: c.Mode(), FPS: round2(c.Rate.FPS()), OutWidth: ow, OutHeight: oh, BusShare: round2(sources.RawBusShare(c))}
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// putInput: PUT /api/v1/input. Source, device, format, mode, audio and EDID. It belongs to the device
// (the connected camera), not to the destination: it can be changed also with the destination managed
// by EnhancedIRL. A device that is not connected can be saved: the stream waits for it.
func (s *Server) putInput(w http.ResponseWriter, r *http.Request) {
	var p config.InputPatch
	if decode(r, &p) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	if p.Source == nil && p.Device == nil && p.Format == nil && p.Mode == nil && p.Audio == nil && p.EDIDPreferred == nil {
		fieldError(w, r, "source", "common.nothing_to_change")
		return
	}
	s.saveAndApply(w, r, p.Apply, p.ApplyTo)
}

// --- Status per source kind ------------------------------------------------------------------

// SignalView: the signal of the RUNNING source. With HDMI, the TC358743's (even with the stream
// stopped); with USB, the mode it is captured with and whether frames arrive.
type SignalView struct {
	hw.Signal
	Kind        string   `json:"kind"`                  // hdmi-csi | usb | test
	MeasuredFPS *float64 `json:"measuredFps,omitempty"` // real capture fps (the engine)
}

// ActiveInput: what the running stream is capturing with (the engine leaves it in the state).
type ActiveInput struct {
	State  string `json:"state"` // starting | waiting_device | ready
	Source string `json:"source"`
	Device string `json:"device,omitempty"`
	Name   string `json:"name,omitempty"`
	Format string `json:"format,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Rate   string `json:"rate,omitempty"`  // 60000/1001
	Audio  string `json:"audio,omitempty"` // i2s | silence | test | alsa:<card>
}

// videoStale: without an fps sample in this time, the capture is stopped (the launcher writes every second).
const videoStale = 4 * time.Second

// readVideo: real fps from /run/eirl-engine/video. known=false if it does not exist (old launcher or no
// stream): then it is unknown, which is not the same as 0.
func readVideo(path string, now time.Time) (fps float64, known bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	if now.Sub(fi.ModTime()) > videoStale {
		return 0, true
	}
	env, err := config.ReadEnv(path)
	if err != nil {
		return 0, true
	}
	fps, _ = strconv.ParseFloat(env["FPS"], 64)
	return fps, true
}

// usbSignal: with USB, "there is signal" = the capture is ready and delivers frames.
func usbSignal(rs runState, fps float64, known bool) SignalView {
	v := SignalView{Kind: string(sources.KindUSB)}
	if rs.Input != "ready" {
		return v
	}
	if mode, _, ok := strings.Cut(rs.Mode, "@"); ok {
		ws, hs, _ := strings.Cut(mode, "x")
		v.Width, _ = strconv.Atoi(ws)
		v.Height, _ = strconv.Atoi(hs)
	}
	if n, d, ok := strings.Cut(rs.Rate, "/"); ok {
		num, _ := strconv.Atoi(n)
		den, _ := strconv.Atoi(d)
		if den > 0 {
			v.FPS = round2(float64(num) / float64(den))
		}
	}
	v.Supported = true
	v.Present = !known || fps > 0
	return v
}

// audioFor: the audio state of the running stream when it is not the HDMI's I2S.
func audioFor(active string) (hw.Audio, bool) {
	switch {
	case strings.HasPrefix(active, "alsa"):
		return hw.Audio{State: "usb"}, true
	case active == "silence":
		return hw.Audio{State: "silence"}, true
	case active == "test":
		return hw.Audio{State: "test"}, true
	}
	return hw.Audio{}, false
}
