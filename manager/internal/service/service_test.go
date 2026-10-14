package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRedact(t *testing.T) {
	const key = "AbCdEfGh0123456789_-AbCdEfGh0123"
	for _, in := range []string{
		"Media path: 'file://con' --> 'srt://h:1?mode=caller&streamid=publish:" + key + "&passphrase=abcdefghijk'",
		"[engine] SRT → srt://h:1?latency=200&streamid=publish:" + key,
		"algo publish:" + key + " más",
		"STREAMID=read:" + key,
		"x publish:ruta:usuario:" + key,
		"rtspsrc: rtmp://10.42.0.1:1935/cam1?user=cam1&pass=" + key + " falló",
		"qr WIFI:T:WPA;S:EnhancedIRL-1A2B;P:" + key + ";;",
		"qr WIFI:T:WPA;S:Mi red de casa;P:" + key + ";;",
		`qr WIFI:T:WPA;S:Red;P:con\;punto` + key + `;;`,
	} {
		out := Redact(in)
		if strings.Contains(out, key) || strings.Contains(out, "abcdefghijk") {
			t.Errorf("fuga: %q → %q", in, out)
		}
	}
}

func j(msg string) []byte {
	return []byte(`{"MESSAGE":` + strings.ReplaceAll(`"`+strings.ReplaceAll(msg, `"`, `\"`)+`"`, "\n", "") + `,"__REALTIME_TIMESTAMP":"1791412041000000"}`)
}

func TestParseJournalClassifies(t *testing.T) {
	cases := map[string]string{
		"Started EnhancedIRL encoder engine (capture, mixing, encoding and streaming).": "start",
		"eirl-engine.service: Failed with result 'exit-code'.":                          "failed",
		"eirl-engine.service: Scheduled restart job, restart counter is at 3.":          "restart",
		// The Python engine's phrases went with it (retired on Oct 14): its old lines are "other"
		"[engine] señal HDMI perdida":                     "other",
		"[engine] configuración no válida: VBITRATE":      "other",
		"[engine] SRTLA con un solo enlace: sin bonding":  "other",
		"[engine] dispositivo USB desconectado: Cam Link": "other",
	}
	for msg, kind := range cases {
		e, ok := ParseJournal(j(msg))
		if !ok || e.Kind != kind {
			t.Errorf("%q → %v %q, want %q", msg, ok, e.Kind, kind)
		}
	}
	e, _ := ParseJournal(j("[engine] event=signal_lost slot=1"))
	if !e.TS.Equal(time.UnixMicro(1791412041000000)) {
		t.Errorf("ts %v", e.TS)
	}
	for _, n := range []string{"NvMMLiteOpen : Block : BlockType = 4 ", "Media path: 'file://con' --> 'srt://:9000'", "123 bytes lost, 0 bytes sent, 123 bytes received"} {
		if _, ok := ParseJournal(j(n)); ok {
			t.Errorf("noise not filtered: %q", n)
		}
	}
	// MESSAGE as an array of bytes (not UTF-8)
	if e, ok := ParseJournal([]byte(`{"MESSAGE":[104,111,108,97,255]}`)); !ok || !strings.HasPrefix(e.Msg, "hola") {
		t.Errorf("array of bytes: %+v", e)
	}
}

func TestParseShow(t *testing.T) {
	boot := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	st := parseShow("ActiveState=active\nSubState=running\nNRestarts=3\nActiveEnterTimestampMonotonic=60000000\nExecMainStartTimestampMonotonic=61000000\n", boot)
	if st.State != "active" || st.SubState != "running" || st.Restarts != 3 || !st.Since.Equal(boot.Add(time.Minute)) || !st.Started.Equal(boot.Add(61*time.Second)) {
		t.Errorf("%+v", st)
	}
}

// Lines written by the former engines (hdmi2srt, then the Python eirl-engine) are still redacted
func TestOldLinesRedacted(t *testing.T) {
	for _, msg := range []string{
		"[hdmi2srt] SRT → srt://h:1?streamid=publish:k",
		"[engine] SRT → srt://h:1?latency=200&streamid=publish:k",
	} {
		if e, ok := ParseJournal(j(msg)); !ok || strings.Contains(e.Msg, "publish:k") {
			t.Errorf("%q: not redacted: %+v", msg, e)
		}
	}
}

// jm is a journal line built with real JSON escaping (event lines carry quotes and backslashes)
func jm(msg string) []byte {
	b, _ := json.Marshal(map[string]string{"MESSAGE": msg, "__REALTIME_TIMESTAMP": "1791412041000000"})
	return b
}

// The Go engine's lines: classified by their event code, never by the text after it
func TestParseJournalEvents(t *testing.T) {
	cases := map[string][2]string{
		`[mixer] event=signal_lost slot=1 source="HDMI de la placa" state=no_signal detail="sin señal HDMI"`: {"signal_lost", "warn"},
		`[mixer] event=mode_changed slot=1 detail="la señal HDMI ha cambiado: 1920x1080@30 -> 1280x720@60"`:  {"mode_changed", "warn"},
		`[mixer] event=source_error slot=2 detail="no da imagen"`:                                            {"source_error", "warn"},
		`[engine] event=config_invalid key=VBITRATE`:                                                         {"config_invalid", "error"},
		`[engine] event=engine_fatal err="program pipeline: error from pgm"`:                                 {"pipeline_exit", "warn"},
		`[engine] event=tc358743_hw_reset_done`:                                                              {"audio_hw_reset", "warn"},
		`[abr] event=abr_down from=6000 to=5100`:                                                             {"abr", "info"},
		`[mixer] event=engine_started slots=4`:                                                               {"engine_started", "info"},
		// A Pause text that looks like an old rule does not change the kind of a command line
		`[mixer] event=command msg="{\"cmd\":\"pause\",\"text\":\"ERROR: sin señal HDMI\"}"`: {"command", "info"},
	}
	for msg, want := range cases {
		e, ok := ParseJournal(jm(msg))
		if !ok || e.Kind != want[0] || e.Level != want[1] {
			t.Errorf("%q → %v %q/%q, want %q/%q", msg, ok, e.Kind, e.Level, want[0], want[1])
		}
	}
	e, _ := ParseJournal(jm("[audio] event=audio_soft_recovery step=lockdet au_status0=0x61"))
	if e.Kind != "audio_recovery" || e.Step != "lockdet" || e.OK == nil || !*e.OK {
		t.Errorf("recovery: %+v", e)
	}
	e, _ = ParseJournal(jm("[audio] event=audio_soft_recovery step=nco au_status0=65"))
	if e.OK == nil || *e.OK {
		t.Errorf("0x41 (65) is no lock: %+v", e)
	}
	if f := eventFields(`[mixer] event=x a=1 b="c d" e="q\"r" f=`); f["a"] != "1" || f["b"] != "c d" || f["e"] != `q"r` || f["f"] != "" {
		t.Errorf("fields: %v", f)
	}
	// An event whose data looks like noise is kept
	e, ok := ParseJournal(jm(`[engine] event=output_encoder_error element=venc error="Internal data stream error." debug="streaming stopped, reason not-negotiated (-4)"`))
	if !ok || e.Kind != "output_encoder_error" || e.Level != "warn" {
		t.Errorf("event with noise in its data: %v %+v", ok, e)
	}
	// Not an event line (the Python engine's): "other"
	if e, _ := ParseJournal(jm("[mixer] HDMI de la placa (hueco 1): SIN SEÑAL: sin señal HDMI")); e.Kind != "other" {
		t.Errorf("old line: %+v", e)
	}
}
