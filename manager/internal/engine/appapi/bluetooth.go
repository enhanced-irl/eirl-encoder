package appapi

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The board's Bluetooth for the screen's Bluetooth page (BLUETOOTH.md §3.4, §3.6): GET /bluetooth only while the
// page is on the screen, every second while something moves (a search, the visible window, an order on a device),
// and the page's five actions (bt_power, bt_scan, bt_visible, bt_device, bt_use). What the app says is third-party
// data in part (the devices' names): every field is checked against a closed list, a regex or the text rule, and
// whatever does not fit is left out. What the screen asks is checked too before it goes: only the fields of its
// route, each of the right shape.

const (
	BluetoothEvery = 2 * time.Second // the Bluetooth page
	BluetoothFast  = time.Second     // while it searches, is visible or an order on a device runs
	// MaxBTDevices: the most devices kept (the app lists at most 32)
	MaxBTDevices = 32
	// BTNameMax: a device's (and the encoder's) longest name, in runes (the app's cleaning rule)
	BTNameMax = 48
)

var (
	reBTAddr   = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)
	btReasons  = set("no_stack", "no_adapter", "no_audio")
	btKinds    = set("headphones", "headset", "speaker", "phone", "computer", "audio")
	btUses     = set("listen", "mic", "input")
	btOrders   = set("pair", "connect", "disconnect", "forget")
	btFailures = set("rejected", "timeout", "not_reachable", "gone", "failed")
)

// btName is m[k] if it is a drawable name: any script (accents, CJK), bounded in bytes by what BTNameMax runes can
// take (4 each) and cut at BTNameMax runes ("" otherwise: the screen shows the address).
func btName(m map[string]any, k string) string {
	s := textField(m, k, 4*BTNameMax)
	if r := []rune(s); len(r) > BTNameMax {
		s = string(r[:BTNameMax])
	}
	return s
}

// btState is GET /bluetooth (or the state an action answers with); nil if it is not one (no boolean available).
func btState(v any) *ui.BTState {
	m := obj(v)
	avail, ok := boolOf(m["available"])
	if m == nil || !ok {
		return nil
	}
	b := &ui.BTState{Available: avail, Reason: oneOf(m, "reason", btReasons)}
	b.On, _ = boolOf(m["on"])
	if !avail {
		return b // nothing else applies (and nothing else is drawn)
	}
	if b.Reason != "no_audio" { // available: only «its audio does not work» (the devices can still be managed)
		b.Reason = ""
	}
	b.Powered, _ = boolOf(m["powered"])
	b.PowerFailed, _ = boolOf(m["powerFailed"])
	b.Name = btName(m, "name")
	b.Visible, _ = boolOf(m["visible"])
	if b.Visible {
		b.VisibleLeft, _ = intIn(m["visibleLeft"], 0, 600)
	}
	b.Scanning, _ = boolOf(m["scanning"])
	raw, _ := m["devices"].([]any)
	seen := map[string]bool{}
	for _, x := range raw {
		if len(b.Devices) >= MaxBTDevices {
			break
		}
		dv, ok := btDevice(obj(x))
		if !ok || seen[dv.Address] {
			continue
		}
		seen[dv.Address] = true
		b.Devices = append(b.Devices, dv)
	}
	return b
}

// btDevice is one device of GET /bluetooth; ok false without a valid address.
func btDevice(m map[string]any) (ui.BTDevice, bool) {
	addr, _ := m["address"].(string)
	if m == nil || !reBTAddr.MatchString(addr) {
		return ui.BTDevice{}, false
	}
	dv := ui.BTDevice{Address: addr, Name: btName(m, "name"), Kind: oneOf(m, "kind", btKinds),
		Use: oneOf(m, "use", btUses), Uses: closedList(m["uses"], btUses), Pending: oneOf(m, "pending", btOrders),
		Error: oneOf(m, "error", btFailures)}
	if dv.Kind == "" {
		dv.Kind = "audio"
	}
	dv.Paired, _ = boolOf(m["paired"])
	dv.Connected, _ = boolOf(m["connected"])
	dv.Ready, _ = boolOf(m["ready"])
	dv.Output, _ = boolOf(m["output"])
	if r, ok := intIn(m["rssi"], -127, 20); ok {
		dv.RSSI = &r
	}
	return dv, true
}

// btFast: something on the page moves by the second (the visible window's countdown, a search, an order).
func btFast(b *ui.BTState) bool {
	return b != nil && (b.Scanning || b.Visible || b.Busy())
}

// btRequest checks one of the page's actions and builds its body: only its route's fields, each of the right shape
// (the address of {id}, the order of {action}, a boolean on, a use of the three). ok false: it is not sent.
func btRequest(a ui.AppAction) (body []byte, ok bool) {
	only := func(keys ...string) bool {
		if len(a.Body) != len(keys) || len(a.Secrets) > 0 {
			return false
		}
		for _, k := range keys {
			if _, has := a.Body[k]; !has {
				return false
			}
		}
		return true
	}
	addr := reBTAddr.MatchString(a.ID)
	switch a.What {
	case "bt_power", "bt_visible":
		on, isBool := a.Body["on"].(bool)
		if !only("on") || !isBool || a.ID != "" || a.Value != "" {
			return nil, false
		}
		b, _ := json.Marshal(map[string]bool{"on": on})
		return b, true
	case "bt_scan":
		return []byte("{}"), only() && a.ID == "" && a.Value == ""
	case "bt_device":
		return []byte("{}"), only() && addr && btOrders[a.Value]
	case "bt_use":
		use, _ := a.Body["use"].(string)
		if !only("use") || !addr || !btUses[use] || a.Value != "" {
			return nil, false
		}
		b, _ := json.Marshal(map[string]string{"use": use})
		return b, true
	}
	return nil, false
}

// isBT: one of the Bluetooth page's actions.
func isBT(what string) bool { return strings.HasPrefix(what, "bt_") }

// pollBluetooth asks GET /bluetooth.
func (d *DeviceState) pollBluetooth(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/bluetooth", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if b := btState(v); b != nil {
			d.snap.Bluetooth, d.snap.BluetoothAPI = b, ui.Yes
		}
	case d.gone(e):
		d.snap.Bluetooth, d.snap.BluetoothAPI = nil, ui.No
	}
	// Passing (no answer, a 5xx): the last one stays
}

// PollBluetooth is pollBluetooth for tests and tools.
func (d *DeviceState) PollBluetooth() { d.pollBluetooth(ui.AppWatch{}) }
