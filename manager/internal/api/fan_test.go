package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/fan"
)

// withFan2: the fan daemon's state (with a second fan, or none), a control file the app writes and a boot id.
func withFan2(t *testing.T, e *env, second bool) {
	t.Helper()
	e.srv.Fan = FanFiles{State: filepath.Join(e.dir, "fan-state.json"), Control: filepath.Join(e.dir, "fan.json"),
		BootID: filepath.Join(e.dir, "boot_id")}
	st := `{"pwm":120,"tempC":61.5}`
	if second {
		st = `{"pwm":120,"tempC":61.5,"fan2":{"on":true,"reason":"temp","mode":"auto","onC":55,"offC":50,"hotC":80}}`
	}
	must(t, os.WriteFile(e.srv.Fan.State, []byte(st), 0o644))
	must(t, os.WriteFile(e.srv.Fan.BootID, []byte("boot-1\n"), 0o644))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func getFan(t *testing.T, e *env, tok string) *fanView {
	t.Helper()
	w := e.do("GET", "/api/v1/device", "", tok)
	var v deviceView
	must(t, json.Unmarshal(w.Body.Bytes(), &v))
	return v.Fan
}

func getFan2(t *testing.T, e *env, tok string) *fan2View {
	t.Helper()
	if v := getFan(t, e, tok); v != nil {
		return v.Second
	}
	return nil
}

func TestDeviceFan2(t *testing.T) {
	e := setup(t, true)
	withDevice(t, e)
	tok := e.login(t)
	if f := getFan(t, e, tok); f != nil {
		t.Fatalf("no fan daemon: no fans (%+v)", f)
	}
	withFan2(t, e, false)
	if f := getFan(t, e, tok); f == nil || f.TempC != 61.5 || f.PWM != 120 || f.Second != nil {
		t.Fatalf("a board without the second fan: the main fan only (%+v)", f)
	}
	if w := e.do("PUT", "/api/v1/device", `{"fan":{"second":"off"}}`, tok); w.Code != 400 || !strings.Contains(w.Body.String(), `"fan"`) {
		t.Fatalf("switching off a fan the board does not have: %d %s", w.Code, w.Body)
	}
	withFan2(t, e, true)
	f := getFan2(t, e, tok)
	if f == nil || f.Mode != fan.ModeAuto || !f.On || f.Reason != "temp" || f.OnC != 55 || f.HotC != 80 {
		t.Fatalf("the second fan: %+v", f)
	}
	for _, bad := range []string{`{"fan":{}}`, `{"fan":{"second":"quiet"}}`} {
		if w := e.do("PUT", "/api/v1/device", bad, tok); w.Code != 400 {
			t.Fatalf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	w := e.do("PUT", "/api/v1/device", `{"fan":{"second":"off"}}`, tok)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"off"`) {
		t.Fatalf("switch off: %d %s", w.Code, w.Body)
	}
	b, err := os.ReadFile(e.srv.Fan.Control)
	must(t, err)
	if c, err := fan.ParseControl(b); err != nil || c.Fan2 != fan.ModeOff || c.BootID != "boot-1" {
		t.Fatalf("control file %s: %v", b, err)
	}
	if fi, _ := os.Stat(e.srv.Fan.Control); fi.Mode().Perm() != 0o644 {
		t.Fatalf("control file mode %v: the fan daemon reads it as other", fi.Mode().Perm())
	}
	// The next boot: automatic again
	must(t, os.WriteFile(e.srv.Fan.BootID, []byte("boot-2\n"), 0o644))
	if f := getFan2(t, e, tok); f == nil || f.Mode != fan.ModeAuto {
		t.Fatalf("another boot: automatic (%+v)", f)
	}
	// Off again in this boot, then back to automatic
	if w := e.do("PUT", "/api/v1/device", `{"fan":{"second":"off"}}`, tok); w.Code != 200 || getFan2(t, e, tok).Mode != fan.ModeOff {
		t.Fatalf("switch off in the new boot: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/v1/device", `{"fan":{"second":"auto"}}`, tok); w.Code != 200 || getFan2(t, e, tok).Mode != fan.ModeAuto {
		t.Fatalf("back to automatic: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(e.srv.Fan.Control); !strings.Contains(string(b), `"fan2":"auto"`) {
		t.Fatalf("control file %s", b)
	}
}

// Before the admin password exists, the web cannot switch it off (only the language); the screen can.
func TestDeviceFan2NeedsThePassword(t *testing.T) {
	e := setup(t, false)
	withDevice(t, e)
	withFan2(t, e, true)
	if w := e.do("PUT", "/api/v1/device", `{"fan":{"second":"off"}}`, ""); w.Code != 503 {
		t.Fatalf("web without a password: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(e.srv.Fan.Control); !os.IsNotExist(err) {
		t.Fatal("nothing written")
	}
}
