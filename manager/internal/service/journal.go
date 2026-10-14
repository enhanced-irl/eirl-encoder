package service

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Entry struct {
	TS    time.Time `json:"ts"`
	Level string    `json:"level"` // info | warn | error
	Kind  string    `json:"kind"`
	Step  string    `json:"step,omitempty"` // audio_recovery: bufinit | lockdet | nco
	OK    *bool     `json:"ok,omitempty"`   // audio_recovery: si el PLL enganchó
	Msg   string    `json:"msg"`
}

var (
	// pass=: la clave de una cámara de red en su URL RTMP (?user=camN&pass=…, F7)
	reSecretParam = regexp.MustCompile(`(?i)(streamid|passphrase|pass)=[^&'"\s]*`)
	// La contraseña de la wifi de las cámaras en el texto de su QR (WIFI:T:WPA;S:…;P:…;;)
	reWifiPSK = regexp.MustCompile(`(?i)(WIFI:[^\n]*?;P:)(?:\\.|[^;\\\s])*`)
	// Con ":" dentro: un streamid tipo MediaMTX (publish:ruta:usuario:contraseña) se tapa entero
	reStreamKey = regexp.MustCompile(`\b(publish|read):[A-Za-z0-9_:./-]+`)
)

// Redact quita streamid, passphrase, pass (cámaras de red), claves publish:/read: y la contraseña de
// la wifi de un QR de una línea. El script y su filtro ya no los imprimen; esto es la segunda barrera
// (líneas antiguas del journal, otros procesos).
func Redact(s string) string {
	s = reSecretParam.ReplaceAllString(s, "$1=***")
	s = reWifiPSK.ReplaceAllString(s, "${1}***")
	return reStreamKey.ReplaceAllString(s, "$1:***")
}

type rule struct {
	sub   string
	kind  string
	level string
}

// Lines that are not the engine's events: systemd's about the unit, and an "ERROR:" from a library. The
// Python engine's phrases went with it (retired on Oct 14): its old journal lines are "other".
var rules = []rule{
	{"ERROR:", "pipeline_error", "error"},
	{"Started ", "start", "info"},
	{"Stopped ", "stop", "info"},
	{"Failed with result", "failed", "error"},
	{"Scheduled restart job", "restart", "warn"},
}

// Lines of the engine (ENGINE_GO.md): `[prefix] event=<code> key=value …`. The code says what happened
// and never changes once released; what follows is data.
var reEvent = regexp.MustCompile(`^\[[a-z]+\] event=([a-z0-9_]+)(?:\s|$)`)

// reField is one key=value of an event line: a bare token or a Go-quoted string.
var reField = regexp.MustCompile(`(?:^|\s)([a-z0-9_]+)=("(?:[^"\\]|\\.)*"|\S*)`)

type eventRule struct{ kind, level string }

// eventKinds maps the Go engine's event codes to the kinds the app and the cloud already know
// (cloud.journalEvents forwards signal_lost, mode_changed, audio_*, pipeline_exit, usb2…). A code
// that is not here keeps its own name as kind, at level info.
var eventKinds = map[string]eventRule{
	"config_invalid":                  {"config_invalid", "error"},
	"signal_lost":                     {"signal_lost", "warn"},
	"mode_changed":                    {"mode_changed", "warn"},
	"signal_unsupported":              {"signal_unsupported", "error"},
	"source_error":                    {"source_error", "warn"},
	"capture_stuck":                   {"capture_stuck", "warn"},
	"usb2":                            {"usb2", "warn"},
	"edid_loaded":                     {"edid_loaded", "info"},
	"edid_missing":                    {"edid_missing", "warn"},
	"tc358743_hw_reset_done":          {"audio_hw_reset", "warn"},
	"tc358743_hw_reset_failed":        {"audio_hw_reset", "warn"},
	"audio_soft_recovery":             {"audio_recovery", "warn"},
	"audio_soft_recovery_skipped":     {"audio_recovery", "warn"},
	"audio_pll_unlocked":              {"audio_pll_unlocked", "warn"},
	"audio_pll_unlocked_no_reset_pin": {"audio_pll_unlocked", "warn"},
	"audio_hw_reset_requested":        {"audio_hw_reset", "warn"},
	"audio_hw_reset_deferred":         {"audio_pll_unlocked", "warn"},
	"audio_capture_stalled":           {"audio_capture_stalled", "warn"},
	"audio_missing":                   {"audio_missing", "warn"},
	"pipeline_error":                  {"pipeline_error", "error"},
	"start_failed":                    {"pipeline_error", "error"},
	"engine_fatal":                    {"pipeline_exit", "warn"},
	"srtla_no_uplinks":                {"srtla_no_uplinks", "warn"},
	"srtla_single_uplink":             {"srtla_single_uplink", "warn"},
	"srtla_uplinks":                   {"srtla_uplinks", "info"},
	"srtla_link_down":                 {"srtla_link_down", "warn"},
	"abr_down":                        {"abr", "info"},
	"abr_max":                         {"abr", "info"},
	"srt_connected":                   {"srt_connected", "info"},
	"srt_disconnected":                {"srt_disconnected", "warn"},
	"audio_hw_reset_request_failed":   {"audio_hw_reset", "warn"},
	"usb_recovery_failed":             {"usb_recovery_failed", "warn"},
	"usb_recovery_held_unknown":       {"usb_recovery_held", "warn"},
	"usb_recovery_held":               {"usb_recovery_held", "warn"},
	"usb_recovery_impossible":         {"usb_recovery_impossible", "warn"},
	"usb_power_cycle":                 {"usb_power_cycle", "warn"},
	"usb_power_cycle_error":           {"usb_power_cycle", "warn"},
	"phones_error":                    {"phones_error", "warn"},
	"phones_close_timeout":            {"phones_error", "warn"},
	"app_api_panic":                   {"app_api_panic", "warn"},
	"single_source_unsupported":       {"single_source_unsupported", "warn"},
	"socket_unavailable":              {"socket_unavailable", "warn"},
	"state_write_failed":              {"state_write_failed", "warn"},
	"scene_save_failed":               {"scene_save_failed", "warn"},
	"output_encoder_error":            {"output_encoder_error", "warn"},
	"output_process_killed":           {"output_process_killed", "warn"},
	"output_close_stuck":              {"output_close_stuck", "warn"},
	"output_close_timeout":            {"output_close_stuck", "warn"},
	"output_readers_stuck":            {"output_close_stuck", "warn"},
	"output_unavailable":              {"output_unavailable", "error"},
	"audio_input_error":               {"audio_input_error", "warn"},
	"audio_input_panic":               {"audio_input_error", "error"},
	"audio_bus_unavailable":           {"audio_missing", "warn"},
	"i2s_mux_failed":                  {"audio_missing", "warn"},
	"capture_judge":                   {"capture_timestamps", "info"},
}

// eventFields reads the key=value pairs of an event line (quoted values unquoted).
func eventFields(msg string) map[string]string {
	f := map[string]string{}
	for _, m := range reField.FindAllStringSubmatch(msg, -1) {
		v := m[2]
		if strings.HasPrefix(v, `"`) {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
		}
		f[m[1]] = v
	}
	return f
}

// Ruido de NVIDIA/GStreamer/libsrt que no le dice nada a quien opera el aparato.
var noise = []string{
	"NvMMLite", "===== NVMEDIA", "H264: Profile", "Opening in BLOCKING MODE", "Redistribute latency",
	"New clock", "Pipeline is live", "Setting pipeline", "Freeing pipeline", "IPE: update struck",
	"remove_usock", "Additional debug info", "gstbasesrc.c", "streaming stopped, reason", "Media path:",
	"Main process exited", "Stopping ", "Service hold-off", "bytes lost, ", "vendor.tegra.display-size",
}

// ParseJournal convierte una línea de `journalctl -o json`. Devuelve ok=false para el ruido.
func ParseJournal(line []byte) (Entry, bool) {
	var raw struct {
		Message  json.RawMessage `json:"MESSAGE"`
		Realtime string          `json:"__REALTIME_TIMESTAMP"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return Entry{}, false
	}
	msg := decodeMessage(raw.Message)
	if msg == "" {
		return Entry{}, false
	}
	// A Go engine event is never noise, even if a value it carries looks like it (a GStreamer debug line
	// such as "streaming stopped, reason not-negotiated (-4)" in output_encoder_error)
	isEvent := reEvent.MatchString(strings.TrimSpace(msg))
	for _, n := range noise {
		if isEvent {
			break
		}
		if strings.Contains(msg, n) {
			return Entry{}, false
		}
	}
	e := Entry{Msg: Redact(strings.TrimSpace(msg)), Kind: "other", Level: "info"}
	if us, err := strconv.ParseInt(raw.Realtime, 10, 64); err == nil {
		e.TS = time.UnixMicro(us).UTC()
	}
	if m := reEvent.FindStringSubmatch(e.Msg); m != nil {
		r, ok := eventKinds[m[1]]
		if !ok {
			r = eventRule{m[1], "info"}
		}
		e.Kind, e.Level = r.kind, r.level
		if e.Kind == "audio_recovery" {
			f := eventFields(e.Msg)
			if step := f["step"]; step != "" {
				e.Step = step
				v, err := strconv.ParseUint(f["au_status0"], 0, 8)
				ok := err == nil && v&0x21 == 0x21
				e.OK = &ok
			}
		}
		return e, true
	}
	for _, r := range rules {
		if strings.Contains(msg, r.sub) {
			e.Kind, e.Level = r.kind, r.level
			break
		}
	}
	return e, true
}

// journald guarda MESSAGE como cadena o, si no es UTF-8 válido, como array de bytes.
func decodeMessage(m json.RawMessage) string {
	var s string
	if json.Unmarshal(m, &s) == nil {
		return s
	}
	var b []byte
	var ints []int
	if json.Unmarshal(m, &ints) == nil {
		b = make([]byte, len(ints))
		for i, v := range ints {
			b[i] = byte(v)
		}
		return strings.ToValidUTF8(string(b), "?")
	}
	return ""
}
