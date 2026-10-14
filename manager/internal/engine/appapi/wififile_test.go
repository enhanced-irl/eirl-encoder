package appapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Ported from the former Python engine's WifiInfoTest.test_lee_y_valida
func TestLeeYValida(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wifi.json")
	write := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(path, raw, 0o640); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		mt := st.ModTime().Add(1000 * time.Nanosecond) // so the change shows
		os.Chtimes(path, mt, mt)
	}
	writeJSON := func(v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		write(raw)
	}
	if WiFiInfo(path) != nil {
		t.Fatal("no file")
	}
	writeJSON(map[string]any{"v": 1, "ssid": "EnhancedIRL-A1B2", "password": "abcdEFGH1234abcdEFGH", "qr": qrRows, "on": true})
	w := WiFiInfo(path)
	if w == nil || w.SSID != "EnhancedIRL-A1B2" || w.Password != "abcdEFGH1234abcdEFGH" || len(w.QR) != 21 || !w.On {
		t.Fatalf("%+v", w)
	}
	if WiFiInfo(path) != w {
		t.Error("read again without a change")
	}
	// Without «on» (an older app): on. With on=false: off
	writeJSON(map[string]any{"v": 1, "ssid": "Red", "password": "abcdEFGH1234abcdEFGH", "qr": qrRows})
	if w := WiFiInfo(path); w == nil || !w.On {
		t.Errorf("without on: %+v", w)
	}
	writeJSON(map[string]any{"v": 1, "ssid": "Red", "password": "abcdEFGH1234abcdEFGH", "qr": qrRows, "on": false})
	if w := WiFiInfo(path); w == nil || w.On {
		t.Errorf("on=false: %+v", w)
	}
	twos := make([]string, 21)
	for i := range twos {
		twos[i] = strings.Repeat("2", 21)
	}
	for _, bad := range []any{
		map[string]any{"ssid": "Red<b>", "password": "abcdEFGH1234abcdEFGH", "qr": qrRows},
		map[string]any{"ssid": "Red", "password": "corta", "qr": qrRows},
		map[string]any{"ssid": "Red", "password": "abcdEFGH1234abcdEFGH", "qr": qrRows[:20]},
		map[string]any{"ssid": "Red", "password": "abcdEFGH1234abcdEFGH", "qr": twos},
		map[string]any{"ssid": "Red", "password": "abcdEFGH1234abcdEFGH", "qr": "no"},
		[]any{"lista"},
	} {
		writeJSON(bad)
		if w := WiFiInfo(path); w != nil {
			t.Errorf("%v → %+v", bad, w)
		}
	}
	write([]byte("{roto"))
	if WiFiInfo(path) != nil {
		t.Error("broken JSON")
	}
	os.Remove(path)
	if WiFiInfo(path) != nil {
		t.Error("removed file")
	}
}
