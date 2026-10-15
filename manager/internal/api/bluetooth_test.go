package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/bluetooth"
)

// fakeBT: the Bluetooth manager's orders, recorded; err answers the next one.
type fakeBT struct {
	mu    sync.Mutex
	calls []string
	err   error
	state bluetooth.State
}

func (f *fakeBT) rec(c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	err := f.err
	f.err = nil
	return err
}

func (f *fakeBT) State() bluetooth.State { f.mu.Lock(); defer f.mu.Unlock(); return f.state }
func (f *fakeBT) SetOn(_ context.Context, on bool) error {
	return f.rec(map[bool]string{true: "on", false: "off"}[on])
}
func (f *fakeBT) Scan(context.Context) error { return f.rec("scan") }
func (f *fakeBT) SetVisible(_ context.Context, on bool) error {
	return f.rec(map[bool]string{true: "visible", false: "hidden"}[on])
}
func (f *fakeBT) Act(addr, action string) error { return f.rec(action + " " + addr) }
func (f *fakeBT) SetUse(addr, use string) error { return f.rec("use " + addr + " " + use) }

func TestBluetoothRoutes(t *testing.T) {
	e := setup(t, true)
	tok := e.login(t)
	// Without a manager: no stack, and every order 409
	if w := e.do("GET", "/api/v1/bluetooth", "", tok); w.Code != 200 ||
		strings.TrimSpace(w.Body.String()) != `{"available":false,"reason":"no_stack","on":false,"powered":false,"visible":false,"visibleLeft":0,"scanning":false,"devices":[]}` {
		t.Errorf("GET without Bluetooth: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", "/api/v1/bluetooth/scan", "", tok); w.Code != 409 || !strings.Contains(w.Body.String(), `"bluetooth_unavailable"`) {
		t.Errorf("scan without Bluetooth: %d %s", w.Code, w.Body)
	}
	f := &fakeBT{state: bluetooth.State{Available: true, On: true, Powered: true, Devices: []bluetooth.Device{
		{Address: "38:18:4C:12:A0:01", Name: "WH-1000XM4", Kind: "headphones", Paired: true, Uses: []string{"listen"}}}}}
	e.srv.Bluetooth = f
	if w := e.do("GET", "/api/v1/bluetooth", "", ""); w.Code != 401 {
		t.Errorf("GET without a session: %d", w.Code)
	}
	for _, c := range []struct {
		method, path, body string
		code               int
		call, errCode      string
	}{
		{"PUT", "/api/v1/bluetooth", `{"on":false}`, 200, "off", ""},
		{"PUT", "/api/v1/bluetooth", `{}`, 400, "", "bad_request"},
		{"PUT", "/api/v1/bluetooth", `{"on":"yes"}`, 400, "", "bad_request"},
		{"POST", "/api/v1/bluetooth/scan", ``, 200, "scan", ""},
		{"POST", "/api/v1/bluetooth/visible", `{"on":true}`, 200, "visible", ""},
		{"POST", "/api/v1/bluetooth/visible", `{"on":1}`, 400, "", "bad_request"},
		{"POST", "/api/v1/bluetooth/devices/38:18:4c:12:a0:01/pair", ``, 200, "pair 38:18:4C:12:A0:01", ""},
		{"POST", "/api/v1/bluetooth/devices/38:18:4C:12:A0:01/forget", ``, 200, "forget 38:18:4C:12:A0:01", ""},
		{"POST", "/api/v1/bluetooth/devices/38:18:4C:12:A0:01/dance", ``, 404, "", "unknown_action"},
		{"PUT", "/api/v1/bluetooth/devices/38:18:4C:12:A0:01", `{"use":"mic"}`, 200, "use 38:18:4C:12:A0:01 mic", ""},
		{"PUT", "/api/v1/bluetooth/devices/38:18:4C:12:A0:01", `{"use":3}`, 400, "", "bad_request"},
	} {
		f.mu.Lock()
		f.calls = nil
		f.mu.Unlock()
		w := e.do(c.method, c.path, c.body, tok)
		f.mu.Lock()
		calls := f.calls
		f.mu.Unlock()
		if w.Code != c.code || c.call != "" && (len(calls) != 1 || calls[0] != c.call) || c.call == "" && len(calls) != 0 ||
			c.errCode != "" && !strings.Contains(w.Body.String(), `"error":"`+c.errCode+`"`) {
			t.Errorf("%s %s %s: %d %s %v", c.method, c.path, c.body, w.Code, w.Body, calls)
		}
		if c.code == 200 {
			var m struct {
				OK        bool            `json:"ok"`
				Bluetooth bluetooth.State `json:"bluetooth"`
			}
			if json.Unmarshal(w.Body.Bytes(), &m) != nil || !m.OK || len(m.Bluetooth.Devices) != 1 {
				t.Errorf("%s %s: answer %s", c.method, c.path, w.Body)
			}
		}
	}
	// The manager's errors, each with its code
	for err, want := range map[error]string{
		bluetooth.ErrUnavailable: `409 "bluetooth_unavailable"`, bluetooth.ErrOff: `409 "bluetooth_off"`,
		bluetooth.ErrUnknown: `404 "bluetooth_unknown_device"`, bluetooth.ErrNotPaired: `409 "bluetooth_not_paired"`,
		bluetooth.ErrBusy: `409 "busy"`, bluetooth.ErrInvalid: `400 "validation"`, context.DeadlineExceeded: `502 "bluetooth_failed"`,
	} {
		f.mu.Lock()
		f.err = err
		f.mu.Unlock()
		w := e.do("POST", "/api/v1/bluetooth/devices/38:18:4C:12:A0:01/connect", ``, tok)
		code, kind, _ := strings.Cut(want, " ")
		if want := code + " "; !strings.HasPrefix(w.Result().Status, want) || !strings.Contains(w.Body.String(), `"error":`+kind) ||
			!strings.Contains(w.Body.String(), `"message":"`) {
			t.Errorf("%v: %d %s", err, w.Code, w.Body)
		}
	}
	// The CSRF rule of every mutation: JSON only
	if w := e.do("POST", "/api/v1/bluetooth/scan", ``, tok, "Content-Type", "text/plain"); w.Code != 415 {
		t.Errorf("scan as a form: %d", w.Code)
	}
}

func TestBluetoothOverTheSocket(t *testing.T) {
	e := setup(t, true)
	f := &fakeBT{state: bluetooth.State{Available: true, On: true, Powered: true, Devices: []bluetooth.Device{}}}
	e.srv.Bluetooth = f
	do := localClient(t, e.srv)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/bluetooth", ""}, {"PUT", "/api/v1/bluetooth", `{"on":true}`},
		{"POST", "/api/v1/bluetooth/scan", ""}, {"POST", "/api/v1/bluetooth/visible", `{"on":false}`},
		{"POST", "/api/v1/bluetooth/devices/38:18:4C:12:A0:01/connect", ""},
		{"PUT", "/api/v1/bluetooth/devices/38:18:4C:12:A0:01", `{"use":"listen"}`},
	} {
		if code, body := do(c.method, c.path, c.body); code != 200 {
			t.Errorf("%s %s over the socket: %d %s", c.method, c.path, code, body)
		}
	}
	if len(f.calls) != 5 {
		t.Error(f.calls)
	}
}
