package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/hw"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/modem"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/srtstats"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

type ServiceView struct {
	State    string    `json:"state"`
	Phase    string    `json:"phase"` // streaming | rehearsal | waiting_signal | waiting_device | restarting | stopped | failed
	Since    time.Time `json:"since,omitzero"`
	Restarts int       `json:"restarts"`
}

type Status struct {
	Service        ServiceView     `json:"service"`
	Output         string          `json:"output"` // on | off (rehearsal mode)
	PendingRestart bool            `json:"pendingRestart"`
	Signal         SignalView      `json:"signal"`
	Audio          hw.Audio        `json:"audio"`
	Input          InputView       `json:"input"`
	SRT            *srtstats.Stats `json:"srt"`
	System         hw.System       `json:"system"`
	// Transport: srt | srtla, that of the RUNNING broadcast (or the saved one when stopped)
	Transport string `json:"transport"`
	// Uplinks: internet links (SRTLA). Absent without the NetworkManager dispatcher.
	Uplinks []uplinks.Uplink `json:"uplinks,omitempty"`
	// Modems: the cellular modems, also those without a usable link (no SIM, PIN…); MODEMS.md §4
	Modems []modem.Modem `json:"modems,omitempty"`
	// Bitrate: the current video bitrate while adaptive bitrate runs; absent without it
	Bitrate *BitrateView `json:"bitrate,omitempty"`
	// Mixer: scene and sources of the running mixer (MIXER=1); absent without it (MIXER.md §5)
	Mixer *mixer.State `json:"mixer,omitempty"`
	// OutputState: how the output is really doing. Output is what was requested (on/off); with the
	// mixer it hooks up hot and this says whether it connects, streams or fails (ENCODER_CLOUD.md §13.3)
	OutputState OutputStateView `json:"outputState"`
	// Network: network cameras and their access point, WITHOUT secrets (NETWORK_SOURCES.md §6.6);
	// absent without eirl-netsetup
	Network *network.Status `json:"network,omitempty"`
	// WiFi: the board's Wi-Fi, without secrets: same as GET /network/wifi (WIFI.md §5); absent
	// without the Wi-Fi client
	WiFi *network.WiFiStatus `json:"wifi,omitempty"`
	// Version: the installed release, app and engine; Update: the update state in short (UPDATES.md §8)
	Version *update.Versions `json:"version,omitempty"`
	Update  *UpdateSummary   `json:"update,omitempty"`
}

type OutputStateView struct {
	State  string `json:"state"` // off | connecting | on | error
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type BitrateView struct {
	Kbps    int `json:"kbps"`
	MinKbps int `json:"minKbps"`
	MaxKbps int `json:"maxKbps"`
}

func phase(state string, signal bool, output string) string {
	switch state {
	case "failed":
		return "failed"
	case "activating", "deactivating", "reloading":
		return "restarting"
	case "active":
		switch {
		case !signal:
			return "waiting_signal"
		case output == "off":
			return "ready" // running with preview, not streaming (formerly “rehearsal”, the rehearsal mode)
		default:
			return "streaming"
		}
	}
	return "stopped"
}

// InputView: the saved input (source, device, format, mode, audio, EDID) and, with the broadcast
// running, what it is capturing with (EDIDActive and Active; empty when stopped).
type InputView struct {
	EDIDPreferred string       `json:"edidPreferred"`
	EDIDActive    string       `json:"edidActive,omitempty"`
	Source        string       `json:"source"`
	Device        string       `json:"device"`
	Format        string       `json:"format"`
	Mode          string       `json:"mode"`
	Audio         string       `json:"audio"`
	Active        *ActiveInput `json:"active,omitempty"`
}

// runState: what the RUNNING broadcast started with (the engine writes /run/eirl-engine/state).
type runState struct {
	Output, Source, ConfSHA, EDID, Transport, ABR, Mixer string
	// Input (SOURCES.md); an older script does not write them
	Input, Device, DeviceName, Format, Mode, Rate, Audio string
}

func (s *Server) runState() (runState, bool) {
	b, err := os.ReadFile(s.RunStatePath)
	if err != nil {
		return runState{}, false
	}
	var rs runState
	for _, line := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "OUTPUT":
			rs.Output = v
		case "SOURCE":
			rs.Source = v
		case "CONF_SHA":
			rs.ConfSHA = v
		case "TRANSPORT":
			rs.Transport = v
		case "ABR":
			rs.ABR = v
		case "EDID":
			rs.EDID = v
		case "INPUT":
			rs.Input = v
		case "DEVICE":
			rs.Device = v
		case "DEVICE_NAME":
			rs.DeviceName = v
		case "FORMAT":
			rs.Format = v
		case "MODE":
			rs.Mode = v
		case "RATE":
			rs.Rate = v
		case "AUDIO":
			rs.Audio = v
		case "MIXER":
			rs.Mixer = v
		}
	}
	return rs, true
}

// pending: there is saved configuration different from what the running broadcast read. By
// fingerprint, not by date: the Jetson has no RTC and the clock jumps when NTP syncs.
func (s *Server) pending(svcState string) bool {
	if svcState != "active" {
		return false
	}
	rs, ok := s.runState()
	if !ok || rs.ConfSHA == "" {
		return false
	}
	// With the mixer, the script writes the fingerprint without OUTPUT (streaming is hot, no restart)
	if h := s.Config.Hashes(); rs.Mixer == "1" {
		return rs.ConfSHA != h.NoOutput
	} else {
		return rs.ConfSHA != h.Full
	}
}

func (s *Server) collect(ctx context.Context) Status {
	now := time.Now()
	var st Status
	cfg, _ := s.Config.Load()
	svc, err := s.Unit.Status(ctx)
	if err != nil {
		svc.State = "unknown"
	}
	// Mode and source of the running execution, not the saved ones: they differ until it restarts
	st.Output = cfg.Output
	st.Transport = config.Transport(cfg.URL)
	source := cfg.Source
	st.Input = InputView{EDIDPreferred: cfg.EDIDPreferred, Source: cfg.Source, Device: cfg.VideoDevice,
		Format: cfg.VideoFormat, Mode: cfg.VideoMode, Audio: cfg.AudioSource}
	rs, running := s.runState()
	running = running && svc.State == "active"
	if running {
		st.Output, source = rs.Output, rs.Source
		st.Input.EDIDActive = rs.EDID
		if rs.Transport != "" { // a script older than SRTLA does not write it
			st.Transport = rs.Transport
		}
		if rs.Input != "" {
			st.Input.Active = &ActiveInput{State: rs.Input, Source: rs.Source, Device: rs.Device, Name: rs.DeviceName,
				Format: rs.Format, Mode: rs.Mode, Rate: rs.Rate, Audio: rs.Audio}
		}
	}
	fps, fpsKnown := 0.0, false
	if running {
		fps, fpsKnown = readVideo(s.VideoPath, now)
	}
	switch source {
	case "usb":
		st.Signal = usbSignal(rs, fps, fpsKnown)
	case "test": // test pattern: there is always "signal"
		st.Signal = SignalView{Kind: string(sources.KindTest), Signal: hw.Signal{Present: true, Supported: true}}
	default:
		st.Signal = SignalView{Kind: string(sources.KindHDMI), Signal: s.Board.Signal()}
	}
	if fpsKnown {
		f := round2(fps)
		st.Signal.MeasuredFPS = &f
	}
	if a, ok := audioFor(rs.Audio); ok && running && rs.Input == "ready" {
		st.Audio = a // USB audio, silence or test tone: not the HDMI I2S
	} else {
		switch {
		case source != "hdmi" && running && rs.Audio == "i2s":
			// video from another source (USB or test pattern) with the board's HDMI audio
			st.Audio = hw.Audio{State: "no_samples"}
			if s.Board.Signal().Present {
				st.Audio = s.Board.Audio()
			}
		case source == "test":
			st.Audio = hw.Audio{State: "test"}
		case source == "hdmi" && st.Signal.Present:
			st.Audio = s.Board.Audio()
		default:
			st.Audio = hw.Audio{State: "no_samples"}
		}
	}
	// With the mixer the program always has a picture (a source without signal shows “No signal”
	// in its slot): the phase does not depend on the main source's signal
	mixing := running && rs.Mixer == "1"
	if mixing && s.Mixer != nil {
		if m, err := s.Mixer.Read(); err == nil {
			st.Mixer = m
		}
	}
	if st.Mixer != nil && st.Mixer.Output != nil {
		o := st.Mixer.Output
		st.Output = o.Mode // the mode in force is the engine's (it changes hot)
		st.OutputState = OutputStateView{State: o.State, Reason: o.Reason, Detail: o.Detail}
	}
	ph := phase(svc.State, st.Signal.Present || mixing, st.Output)
	if running && rs.Input == "waiting_device" && ph != "failed" && ph != "restarting" {
		ph = "waiting_device"
	}
	st.Service = ServiceView{State: svc.State, Phase: ph, Since: svc.Since, Restarts: svc.Restarts}
	st.PendingRestart = s.pending(svc.State)
	st.SRT = srtstats.Read(s.StatsPath, now)
	if st.OutputState.State == "" { // without the mixer: inferred (no SRT stats yet = still connecting)
		switch {
		case !running || st.Output != "on":
			st.OutputState.State = "off"
		case st.SRT != nil:
			st.OutputState.State = "on"
		default:
			st.OutputState.State = "connecting"
		}
	}
	st.System = s.Board.System()
	// What is really streamed: st.Output, which with the mixer is the engine's mode. The startup OUTPUT does not
	// change with the hot output switch (screen, web, socket): with it, links showed “unused” and no bitrate while
	// streaming
	emitting := running && st.Output == "on"
	if snap := s.linksSnapshot(); !snap.At.IsZero() {
		st.Uplinks, st.Modems = snap.Uplinks, snap.Modems
	} else if s.Uplinks != nil {
		st.Uplinks = s.Uplinks.Read(now, overSRTLA(running, st.Output, rs))
	}
	if emitting && rs.ABR == "1" {
		st.Bitrate = readBitrate(s.ABRPath)
	}
	if s.Network != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ns := netStatus(s.ctxLang(ctx), s.Network.Status(ctx))
		cancel()
		st.Network = &ns
		if s.Network.Client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			ws := wifiStatus(s.ctxLang(ctx), s.Network.WiFi(ctx))
			st.WiFi = &ws
			cancel()
		}
	}
	s.addVersionAndUpdate(&st)
	return st
}

// readBitrate: what the engine (internal/engine/output) writes to /run/eirl-engine/abr (KBPS, MIN, MAX) on every change.
func readBitrate(path string) *BitrateView {
	env, err := config.ReadEnv(path)
	if err != nil {
		return nil
	}
	n := func(k string) int { v, _ := strconv.Atoi(env[k]); return v }
	v := BitrateView{Kbps: n("KBPS"), MinKbps: n("MIN"), MaxKbps: n("MAX")}
	if v.Kbps == 0 {
		return nil
	}
	return &v
}

// Info about the device. deviceId will be used to pair it with an account (v2): a hash of
// machine-id, which is not exposed as is.
type Info struct {
	Version string
	Web     string // fingerprint of the web UI (web.Hash)
}

func (i Info) read() map[string]any {
	host, _ := os.Hostname()
	out := map[string]any{"version": i.Version, "web": i.Web, "hostname": host}
	if b, err := os.ReadFile("/etc/nv_tegra_release"); err == nil {
		line, _, _ := strings.Cut(string(b), "\n")
		out["l4t"] = strings.TrimSpace(strings.TrimPrefix(line, "#"))
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			out["uptime"] = f[0]
		}
	}
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		h := sha256.Sum256([]byte("eirl-encoder:" + strings.TrimSpace(string(b))))
		out["deviceId"] = hex.EncodeToString(h[:8])
	}
	return out
}
