package appapi

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The Bluetooth page's reads and actions (BLUETOOTH.md §3.4) against the fake app.

const btAddr = "AC:80:0A:12:34:56"

func btBody() map[string]any {
	return map[string]any{"available": true, "on": true, "powered": true, "name": "Estudio 2", "visible": true,
		"visibleLeft": 107, "scanning": false, "devices": []any{
			map[string]any{"address": btAddr, "name": "WH-1000XM4", "kind": "headphones", "paired": true, "connected": true,
				"ready": true, "use": "listen", "uses": []any{"listen", "mic"}, "output": true},
			map[string]any{"address": "F8:DF:15:A1:B2:C3", "name": "JBL Flip 5", "kind": "speaker", "paired": false,
				"connected": false, "ready": false, "uses": []any{"listen"}, "rssi": -58, "pending": "pair"},
		}}
}

func TestBluetoothRead(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /bluetooth", 200, btBody())
	d.PollBluetooth()
	s := d.AppSnapshot()
	b := s.Bluetooth
	if s.BluetoothAPI != ui.Yes || b == nil || !b.Available || !b.On || !b.Powered || b.Name != "Estudio 2" ||
		!b.Visible || b.VisibleLeft != 107 || b.Scanning || len(b.Devices) != 2 {
		t.Fatalf("%+v", b)
	}
	want := ui.BTDevice{Address: btAddr, Name: "WH-1000XM4", Kind: "headphones", Paired: true, Connected: true, Ready: true,
		Use: "listen", Uses: []string{"listen", "mic"}, Output: true}
	if !reflect.DeepEqual(b.Devices[0], want) {
		t.Errorf("%+v", b.Devices[0])
	}
	if f := b.Devices[1]; f.RSSI == nil || *f.RSSI != -58 || f.Pending != "pair" || f.Paired || !b.Busy() || !btFast(b) {
		t.Errorf("%+v", f)
	}
	// An app without the route (403 over the socket): the page says so, asked again only rarely
	a.drop("GET /bluetooth")
	d.PollBluetooth()
	if s := d.AppSnapshot(); s.BluetoothAPI != ui.No || s.Bluetooth != nil {
		t.Fatalf("%+v", s.Bluetooth)
	}
}

// Whatever does not fit is left out: the devices' names are third-party data.
func TestBluetoothValidated(t *testing.T) {
	long := strings.Repeat("é", BTNameMax+1)
	devs := []any{
		map[string]any{"address": "ac:80:0a:12:34:56", "name": "lower case"},
		map[string]any{"address": "AC:80:0A:12:34:56/../x", "name": "path"},
		map[string]any{"address": btAddr, "name": long, "kind": "toaster", "use": "spy", "uses": []any{"listen", "spy", "listen", "input"},
			"pending": "rm", "error": "<b>", "rssi": 900, "paired": "yes"},
		map[string]any{"address": btAddr, "name": "duplicate"},
		map[string]any{"address": "11:22:33:44:55:66", "name": "bad\x07name", "kind": "phone", "error": "not_reachable"},
	}
	for i := range 40 {
		devs = append(devs, map[string]any{"address": "00:11:22:33:44:" + strings.ToUpper(string("0123456789abcdef"[i/16])+string("0123456789abcdef"[i%16])),
			"name": "n"})
	}
	b := btState(js(t, mustJSON(t, map[string]any{"available": true, "on": true, "powered": true,
		"name": long, "visible": true, "visibleLeft": 100000, "devices": devs})))
	cut := strings.Repeat("é", BTNameMax) // cut at BTNameMax runes, not dropped
	if b == nil || b.Name != cut || b.VisibleLeft != 0 || len(b.Devices) != MaxBTDevices {
		t.Fatalf("%+v", b)
	}
	d0 := b.Devices[0]
	if d0.Address != btAddr || d0.Name != cut || d0.Kind != "audio" || d0.Use != "" || d0.Pending != "" || d0.Error != "" ||
		d0.RSSI != nil || d0.Paired || !reflect.DeepEqual(d0.Uses, []string{"listen", "input"}) {
		t.Errorf("%+v", d0)
	}
	if d1 := b.Devices[1]; d1.Address != "11:22:33:44:55:66" || d1.Name != "" || d1.Kind != "phone" || d1.Error != "not_reachable" {
		t.Errorf("%+v", d1)
	}
	// Unavailable: the reason, nothing else; an unknown reason is left out
	b = btState(js(t, `{"available": false, "reason": "no_adapter", "devices": [{"address": "`+btAddr+`"}]}`))
	if b == nil || b.Available || b.Reason != "no_adapter" || b.Devices != nil {
		t.Fatalf("%+v", b)
	}
	if b = btState(js(t, `{"available": false, "reason": "BlueZ exploded"}`)); b == nil || b.Reason != "" {
		t.Fatalf("%+v", b)
	}
	// Not a state
	for _, s := range []string{`{}`, `{"available": "yes"}`, `[]`, `"x"`} {
		if btState(js(t, s)) != nil {
			t.Errorf("%s: a state", s)
		}
	}
}

func TestBluetoothActions(t *testing.T) {
	a, d := newDevice(t)
	answer := map[string]any{"ok": true, "bluetooth": btBody()}
	for _, k := range []string{"PUT /bluetooth", "POST /bluetooth/scan", "POST /bluetooth/visible",
		"POST /bluetooth/devices/" + btAddr + "/connect", "PUT /bluetooth/devices/" + btAddr} {
		a.route(k, 200, answer)
	}
	cases := []struct {
		act          ui.AppAction
		method, path string
		body         string
	}{
		{ui.AppAction{What: "bt_power", Body: map[string]any{"on": false}}, "PUT", "/api/v1/bluetooth", `{"on":false}`},
		{ui.AppAction{What: "bt_scan"}, "POST", "/api/v1/bluetooth/scan", `{}`},
		{ui.AppAction{What: "bt_visible", Body: map[string]any{"on": true}}, "POST", "/api/v1/bluetooth/visible", `{"on":true}`},
		{ui.AppAction{What: "bt_device", ID: btAddr, Value: "connect"}, "POST", "/api/v1/bluetooth/devices/" + btAddr + "/connect", `{}`},
		{ui.AppAction{What: "bt_use", ID: btAddr, Body: map[string]any{"use": "mic"}}, "PUT", "/api/v1/bluetooth/devices/" + btAddr,
			`{"use":"mic"}`},
	}
	for _, c := range cases {
		res := run(t, d, c.act)
		last := a.last()
		a.mu.Lock()
		raw := string(a.raws[len(a.raws)-1])
		a.mu.Unlock()
		if res == nil || !res.OK || last.method != c.method || last.uri != c.path || raw != c.body {
			t.Errorf("%s: %+v %s %s %s", c.act.What, res, last.method, last.uri, raw)
		}
		// The answer's state is the page's at once
		if s := d.AppSnapshot(); s.BluetoothAPI != ui.Yes || s.Bluetooth == nil || len(s.Bluetooth.Devices) != 2 {
			t.Errorf("%s: %+v", c.act.What, s.Bluetooth)
		}
	}
	// A refusal: the app's code and message (in the device language)
	a.route("POST /bluetooth/devices/"+btAddr+"/forget", 409, map[string]any{"error": "busy",
		"message": "Espera a que termine lo anterior"})
	res := run(t, d, ui.AppAction{What: "bt_device", ID: btAddr, Value: "forget"})
	if res.OK || res.Status != 409 || res.Code != "busy" || res.Message != "Espera a que termine lo anterior" {
		t.Fatalf("%+v", res)
	}
}

// What the screen asks is checked before it goes: anything else is not sent.
func TestBluetoothActionsChecked(t *testing.T) {
	a, d := newDevice(t)
	bad := []ui.AppAction{
		{What: "bt_power"},
		{What: "bt_power", Body: map[string]any{"on": "true"}},
		{What: "bt_power", Body: map[string]any{"on": true, "name": "x"}},
		{What: "bt_power", ID: btAddr, Body: map[string]any{"on": true}},
		{What: "bt_scan", Body: map[string]any{"seconds": 600}},
		{What: "bt_visible", Body: map[string]any{"on": 1}},
		{What: "bt_device", ID: btAddr, Value: "unpair"},
		{What: "bt_device", ID: btAddr, Value: "../forget"},
		{What: "bt_device", ID: "ac:80:0a:12:34:56", Value: "pair"},
		{What: "bt_device", ID: btAddr + "/x", Value: "pair"},
		{What: "bt_device", ID: btAddr, Value: "pair", Body: map[string]any{"pin": "0000"}},
		{What: "bt_device", ID: btAddr, Value: "pair", Secrets: map[string][]byte{"pin": []byte("0000")}},
		{What: "bt_use", ID: btAddr, Body: map[string]any{"use": "spy"}},
		{What: "bt_use", ID: btAddr, Body: map[string]any{"use": "mic", "on": true}},
		{What: "bt_use", Body: map[string]any{"use": "mic"}},
	}
	for _, act := range bad {
		res := run(t, d, act)
		if res == nil || res.OK || res.Status != 400 || res.Code != "bad_request" {
			t.Errorf("%+v: %+v", act, res)
		}
	}
	if seen := a.allSeen(); len(seen) != 0 {
		t.Fatalf("sent: %+v", seen)
	}
}

// The page asks every 2 s, every second while something moves; nothing while it is not on the screen.
func TestBluetoothCadence(t *testing.T) {
	if btFast(nil) || btFast(&ui.BTState{Available: true}) {
		t.Fatal("fast with nothing moving")
	}
	for _, b := range []*ui.BTState{{Scanning: true}, {Visible: true},
		{Devices: []ui.BTDevice{{Address: btAddr, Pending: "connect"}}}} {
		if !btFast(b) {
			t.Errorf("not fast: %+v", b)
		}
	}
	if BluetoothEvery != 2*BluetoothFast {
		t.Fatal("the cadences")
	}
	a, d := newDevice(t)
	a.route("GET /device", 200, deviceBody("es"))
	a.route("GET /bluetooth", 200, btBody())
	d.Start()
	defer d.Stop()
	asked := func() int {
		n := 0
		for _, r := range a.allSeen() {
			if r.uri == "/api/v1/bluetooth" {
				n++
			}
		}
		return n
	}
	time.Sleep(200 * time.Millisecond)
	if asked() != 0 {
		t.Fatal("asked with the page not on the screen")
	}
	d.Watch(ui.AppWatch{Bluetooth: true})
	for t0 := time.Now(); d.AppSnapshot().Bluetooth == nil; time.Sleep(10 * time.Millisecond) {
		if time.Since(t0) > 3*time.Second {
			t.Fatal("not asked when the page opened")
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	return string(jsonBody(v))
}

// Names in any script are kept (their bytes are bounded by what 48 runes can take, not 48 bytes), as the app cleans
// them (btaudio.CleanName); the reason no_audio comes with available (devices managed, not heard); powerFailed.
func TestBluetoothNamesAndReasons(t *testing.T) {
	cjk := strings.Repeat("音", BTNameMax) // 144 bytes
	b := btState(js(t, mustJSON(t, map[string]any{"available": true, "reason": "no_audio", "on": true, "powered": false,
		"powerFailed": true, "name": "Écouteurs de Zoé", "devices": []any{
			map[string]any{"address": btAddr, "name": "Écouteurs de Zoé", "kind": "headphones", "paired": true},
			map[string]any{"address": "11:22:33:44:55:66", "name": cjk, "kind": "phone"},
			map[string]any{"address": "11:22:33:44:55:77", "name": strings.Repeat("😀", BTNameMax+1), "kind": "phone"},
		}})))
	if b == nil || !b.Available || b.Reason != "no_audio" || !b.PowerFailed || b.Name != "Écouteurs de Zoé" {
		t.Fatalf("%+v", b)
	}
	if b.Devices[0].Name != "Écouteurs de Zoé" || b.Devices[1].Name != cjk || b.Devices[2].Name != "" {
		t.Errorf("names %q %q %q", b.Devices[0].Name, b.Devices[1].Name, b.Devices[2].Name)
	}
	// powerFailed alone does not ask every second (only a search, the visible window or an order do)
	if btFast(b) {
		t.Error("fast because the adapter does not turn on")
	}
	// Available with another reason: none
	if b := btState(js(t, `{"available": true, "reason": "no_adapter"}`)); b == nil || b.Reason != "" || b.PowerFailed {
		t.Fatalf("%+v", b)
	}
}
