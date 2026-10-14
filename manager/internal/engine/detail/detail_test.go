package detail

import (
	"strings"
	"testing"
)

// Every code with its texts as the engine wrote them before the codes (and the Python engine): the
// `detail` of the state must not change for whoever already reads it.
func TestTexts(t *testing.T) {
	long := strings.Repeat("x", 200)
	cases := []struct {
		code string
		kv   []string
		want string
	}{
		{"hdmi_no_signal", nil, "sin señal HDMI"},
		{"hdmi_signal_lost", nil, "señal HDMI perdida"},
		{"hdmi_locking", nil, "la señal HDMI se está enganchando"},
		{"hdmi_mode_changed", []string{"from", "1080p60", "to", "1280x720@60"}, "la señal HDMI ha cambiado: 1080p60 -> 1280x720@60"},
		{"hdmi_interlaced", []string{"mode", "1080i50"}, "1080i50 entrelazado: el VI del Nano no lo captura"},
		{"hdmi_too_fast", []string{"mode", "1080p60"}, "1080p60 no cabe en 2 lanes CSI: máx. 1080p50 o 720p60"},
		{"hdmi_set_mode_failed", []string{"error", "VIDIOC_S_DV_TIMINGS: busy"}, "no se puede fijar el modo HDMI: VIDIOC_S_DV_TIMINGS: busy"},
		{"hdmi_hung", nil, "la captura HDMI se ha colgado: vuelve al reiniciar el codificador"},
		{"hdmi_no_audio", nil, "sin audio HDMI"},
		{"gone", nil, "desconectada"},
		{"no_bus", nil, "no cabe en el bus USB con las demás"},
		{"no_format", []string{"warnings", "no_format"}, "no ofrece un formato que se pueda capturar (no_format)"},
		{"no_format", nil, "no ofrece un formato que se pueda capturar"},
		{"unsupported", []string{"error", "only 4:2:0"}, "only 4:2:0"},
		{"unsupported", []string{"error", long}, long[:160]},
		{"probe_failed", []string{"rc", "2", "error", "boom"}, "eirl-encoderd probe ha fallado (2): boom"},
		{"hung_given_up", nil, "colgada: desenchúfala y vuelve a enchufarla"},
		{"hung_on_air_unknown", nil, "colgada: emitiendo no la reinicio (no sé qué hay al aire)"},
		{"port_reset", nil, "reiniciando su puerto USB"},
		{"net_receiver_down", nil, "el receptor de cámaras no responde"},
		{"net_waiting", nil, "Esperando a la cámara"},
		{"net_no_video", []string{"tracks", "MPEG-4 Audio"}, "la cámara no manda H.264 ni H.265 (MPEG-4 Audio)"},
		{"net_no_video", []string{"tracks", long}, ("la cámara no manda H.264 ni H.265 (" + long)[:120]},
		{"net_no_audio", nil, "la cámara no manda audio"},
		{"net_disconnected", nil, "la cámara se ha desconectado"},
		{"net_behind", []string{"seconds", "2.4"}, "va 2.4 s atrasada"},
		{"phones_waiting_screen", nil, "Esperando a la pantalla"},
		{"phones_screen_no_audio", nil, "La pantalla no admite audio"},
		{"phones_no_device", nil, "La placa no tiene salida de audio HDMI"},
		{"phones_not_opening", nil, "la salida HDMI no se abre"},
		{"no_image", nil, "no da imagen"},
		{"frames_stopped", []string{"seconds", "3"}, "3 s sin imagen"},
		{"no_samples", []string{"seconds", "6"}, "sin muestras 6 s"},
		{"stream_ended", nil, "fin de la fuente"},
		{"audio_ended", nil, "fin del audio"},
		{"stream_error", []string{"error", "Internal data stream error."}, "Internal data stream error."},
		{"start_failed", nil, "no arranca"},
		{"build_failed", []string{"error", "no element \"x\""}, "no se puede construir: no element \"x\""},
		{"no_sink", nil, "no se puede construir: sin appsink"},
		{"prepare_failed", nil, "fallo interno al prepararla"},
		{"internal_error", []string{"error", "nil map"}, "fallo interno: nil map"},
	}
	seen := map[string]bool{"hung_on_air": true} // TestHungOnAir
	for _, c := range cases {
		d := New(c.code, c.kv...)
		seen[c.code] = true
		if d.Code != c.code || d.Text != c.want {
			t.Errorf("%s %v: %q, want %q", c.code, c.kv, d.Text, c.want)
		}
		if (len(c.kv) == 0) != (d.Args == nil) {
			t.Errorf("%s: args %v", c.code, d.Args)
		}
	}
	for _, c := range Codes() {
		if !seen[c] {
			t.Errorf("code %s has no test", c)
		}
	}
	if len(Codes()) != 39 {
		t.Errorf("%d codes: the list is a contract (ENGINE_GO.md §13), update both", len(Codes()))
	}
}

func TestHungOnAir(t *testing.T) {
	d := HungOnAir([]string{"hdmi", "/dev/v4l/by-id/usb-Cap-video-index0"}, []string{"alsa:Logi"}, []string{"HDMI", "CAP", "el audio de Logi"})
	if d.Code != "hung_on_air" || d.Text != "colgada: reiniciarla apagaría HDMI, CAP, el audio de Logi (al aire)" ||
		len(d.Args) != 2 || d.Args["video"] != "hdmi,/dev/v4l/by-id/usb-Cap-video-index0" || d.Args["audio"] != "alsa:Logi" {
		t.Errorf("%+v", d)
	}
	if d := HungOnAir(nil, []string{"alsa:Logi"}, []string{"el audio de Logi"}); len(d.Args) != 1 || d.Args["audio"] != "alsa:Logi" {
		t.Errorf("only audio: %+v", d)
	}
}

func TestStreamError(t *testing.T) {
	if d := StreamError(""); !d.Same(D{}) || d.Args != nil {
		t.Errorf("empty: %+v", d)
	}
	if d := StreamError("Internal data stream error."); d.Code != "stream_error" || d.Text != "Internal data stream error." {
		t.Errorf("%+v", d)
	}
}

func TestSameAndZero(t *testing.T) {
	if !(D{}).Same(D{}) || New("gone").Same(D{}) || !New("net_behind", "seconds", "2.0").Same(New("net_behind", "seconds", "2.0")) ||
		New("net_behind", "seconds", "2.0").Same(New("net_behind", "seconds", "2.5")) {
		t.Error("Same")
	}
	defer func() {
		if recover() == nil {
			t.Error("an unknown code must panic")
		}
	}()
	New("nope")
}
