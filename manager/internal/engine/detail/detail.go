// Package detail is why a source (video) or an audio input is not live: a stable code with its
// arguments, which the web and the screen translate, plus the Spanish text the state has always carried
// in `detail` (kept byte for byte, for whoever already reads it). The codes are a closed list (Codes):
// adding one is a contract change the interfaces must learn.
package detail

import (
	"slices"
	"strings"
)

// D is a detail. The zero D says nothing (live, opening, hidden).
type D struct {
	Code string
	Args map[string]string // nil without arguments
	Text string
}

// Same: equal for the state machine (the arguments derive the text).
func (d D) Same(o D) bool { return d.Code == o.Code && d.Text == o.Text }

// New is the detail of a code with its arguments as key, value pairs. An unknown code is a programming
// error (the tests build every one).
func New(code string, kv ...string) D {
	f, ok := texts[code]
	if !ok {
		panic("detail: unknown code " + code)
	}
	var args map[string]string
	if len(kv) > 0 {
		args = make(map[string]string, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			args[kv[i]] = kv[i+1]
		}
	}
	return D{Code: code, Args: args, Text: f(args)}
}

func fixed(s string) func(map[string]string) string {
	return func(map[string]string) string { return s }
}

func arg(k string) func(map[string]string) string {
	return func(a map[string]string) string { return a[k] }
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// texts: code → its Spanish text, exactly as the engine wrote it before the codes (and the Python engine).
var texts = map[string]func(map[string]string) string{
	// HDMI (the board's TC358743)
	"hdmi_no_signal":   fixed("sin señal HDMI"),
	"hdmi_signal_lost": fixed("señal HDMI perdida"),
	"hdmi_locking":     fixed("la señal HDMI se está enganchando"),
	"hdmi_mode_changed": func(a map[string]string) string { // from: "1080p60"; to: "1280x720@60"
		return "la señal HDMI ha cambiado: " + a["from"] + " -> " + a["to"]
	},
	"hdmi_interlaced":      func(a map[string]string) string { return a["mode"] + " entrelazado: el VI del Nano no lo captura" },
	"hdmi_too_fast":        func(a map[string]string) string { return a["mode"] + " no cabe en 2 lanes CSI: máx. 1080p50 o 720p60" },
	"hdmi_set_mode_failed": func(a map[string]string) string { return "no se puede fijar el modo HDMI: " + a["error"] },
	"hdmi_hung":            fixed("la captura HDMI se ha colgado: vuelve al reiniciar el codificador"),
	"hdmi_no_audio":        fixed("sin audio HDMI"),

	// USB
	"gone":   fixed("desconectada"), // a USB camera or audio input unplugged
	"no_bus": fixed("no cabe en el bus USB con las demás"),
	"no_format": func(a map[string]string) string { // warnings: the probe's, "" if none
		if w := a["warnings"]; w != "" {
			return "no ofrece un formato que se pueda capturar (" + w + ")"
		}
		return "no ofrece un formato que se pueda capturar"
	},
	"unsupported": func(a map[string]string) string { return clip(a["error"], 160) }, // what the probe said
	"probe_failed": func(a map[string]string) string {
		return "eirl-encoderd probe ha fallado (" + a["rc"] + "): " + clip(a["error"], 120)
	},
	"hung_given_up":       fixed("colgada: desenchúfala y vuelve a enchufarla"),
	"hung_on_air_unknown": fixed("colgada: emitiendo no la reinicio (no sé qué hay al aire)"),
	"hung_recording":      fixed("colgada: grabando no la reinicio (cortaría el disco)"),
	"hung_on_air": func(a map[string]string) string { // only through HungOnAir: the text needs the names
		return "colgada: reiniciarla apagaría " + a["shown"] + " (al aire)"
	},
	"port_reset": fixed("reiniciando su puerto USB"),

	// Network cameras
	"net_receiver_down": fixed("el receptor de cámaras no responde"),
	"net_waiting":       fixed("Esperando a la cámara"),
	"net_no_video": func(a map[string]string) string { // tracks: what it sends, comma separated
		return clip("la cámara no manda H.264 ni H.265 ("+a["tracks"]+")", 120)
	},
	"net_no_audio": fixed("la cámara no manda audio"),
	// Bluetooth (BLUETOOTH.md): a device used as an input that is connected but sends nothing (a phone not playing)
	"bt_no_audio":      fixed("el dispositivo Bluetooth no manda audio"),
	"net_disconnected": fixed("la cámara se ha desconectado"),
	"net_behind":       func(a map[string]string) string { return "va " + a["seconds"] + " s atrasada" }, // seconds: "2.4"

	// The headphones (audio.phones)
	"phones_waiting_screen":  fixed("Esperando a la pantalla"),
	"phones_screen_no_audio": fixed("La pantalla no admite audio"),
	"phones_no_device":       fixed("La placa no tiene salida de audio HDMI"),
	"phones_not_opening":     fixed("la salida HDMI no se abre"),

	// Any source or input
	"no_image":       fixed("no da imagen"),
	"frames_stopped": func(a map[string]string) string { return a["seconds"] + " s sin imagen" },
	"no_samples":     func(a map[string]string) string { return "sin muestras " + a["seconds"] + " s" },
	"stream_ended":   fixed("fin de la fuente"),
	"audio_ended":    fixed("fin del audio"),
	"stream_error":   arg("error"), // GStreamer's error text
	"start_failed":   fixed("no arranca"),
	"build_failed":   func(a map[string]string) string { return "no se puede construir: " + a["error"] },
	"no_sink":        fixed("no se puede construir: sin appsink"),
	"prepare_failed": fixed("fallo interno al prepararla"),
	"internal_error": func(a map[string]string) string { return "fallo interno: " + a["error"] },
}

// HungOnAir is hung_on_air: a hung USB source waiting, while emitting, because resetting its port would
// power off what is on air. The arguments are ids (video: sources[].id, audio: audio.inputs[].id, each
// comma separated, absent when empty) so the interfaces name them in their language; the Spanish text
// names them as the user sees them (shown).
func HungOnAir(video, audio, shown []string) D {
	a := map[string]string{}
	if len(video) > 0 {
		a["video"] = strings.Join(video, ",")
	}
	if len(audio) > 0 {
		a["audio"] = strings.Join(audio, ",")
	}
	text := texts["hung_on_air"](map[string]string{"shown": Join(shown)})
	return D{Code: "hung_on_air", Args: a, Text: text}
}

// StreamError is the detail of a GStreamer error. An error without text is the zero D, as the text was
// before the codes (the loops take it as asked to stop, without a failure).
func StreamError(text string) D {
	if text == "" {
		return D{}
	}
	return New("stream_error", "error", text)
}

// Codes is the closed list, sorted.
func Codes() []string {
	out := make([]string, 0, len(texts))
	for c := range texts {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// Join is how a list goes into an argument.
func Join(items []string) string { return strings.Join(items, ", ") }
