package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/bluetooth"
)

// --- Bluetooth audio (BLUETOOTH.md §3.4) -------------------------------------------------------------------------

// BluetoothControl: the board's Bluetooth (bluetooth.Manager; a fake one in the tests).
type BluetoothControl interface {
	State() bluetooth.State
	SetOn(ctx context.Context, on bool) error
	Scan(ctx context.Context) error
	SetVisible(ctx context.Context, on bool) error
	Act(addr, action string) error
	SetUse(addr, use string) error
}

// bluetoothState: the state, also without a manager (an app built without it: no stack).
func (s *Server) bluetoothState() bluetooth.State {
	if s.Bluetooth == nil {
		return bluetooth.State{Reason: bluetooth.ReasonNoStack, Devices: []bluetooth.Device{}}
	}
	return s.Bluetooth.State()
}

// getBluetooth: whether there is Bluetooth, how it is and its devices (paired, and the ones a search finds). No
// secrets: devices have no keys here (BlueZ keeps them, root only).
func (s *Server) getBluetooth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.bluetoothState())
}

// bluetoothDone: an order taken; the state as it is now (an order on a device runs on: its Pending).
func (s *Server) bluetoothDone(w http.ResponseWriter) {
	writeJSON(w, 200, map[string]any{"ok": true, "bluetooth": s.bluetoothState()})
}

// bluetoothFail answers an order's error.
func (s *Server) bluetoothFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, bluetooth.ErrUnavailable):
		fail(w, r, 409, "bluetooth_unavailable", "bluetooth.unavailable")
	case errors.Is(err, bluetooth.ErrOff):
		fail(w, r, 409, "bluetooth_off", "bluetooth.off")
	case errors.Is(err, bluetooth.ErrUnknown):
		fail(w, r, 404, "bluetooth_unknown_device", "bluetooth.unknown_device")
	case errors.Is(err, bluetooth.ErrNotPaired):
		fail(w, r, 409, "bluetooth_not_paired", "bluetooth.not_paired")
	case errors.Is(err, bluetooth.ErrBusy):
		fail(w, r, 409, "busy", "bluetooth.busy")
	case errors.Is(err, bluetooth.ErrInvalid):
		fail(w, r, 400, "validation", "bluetooth.invalid")
	default:
		s.Log.Warn("bluetooth", "err", err)
		fail(w, r, 502, "bluetooth_failed", "bluetooth.failed")
	}
}

func (s *Server) bluetoothOff(w http.ResponseWriter, r *http.Request) bool {
	if s.Bluetooth == nil {
		fail(w, r, 409, "bluetooth_unavailable", "bluetooth.unavailable")
		return true
	}
	return false
}

// putBluetooth: {"on": bool}, Bluetooth on or off (saved). Off disconnects every device; the pairings stay.
func (s *Server) putBluetooth(w http.ResponseWriter, r *http.Request) {
	if s.bluetoothOff(w, r) {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if err := decode(r, &body); err != nil || body.On == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"on": true|false}`)
		return
	}
	if err := s.Bluetooth.SetOn(r.Context(), *body.On); err != nil {
		s.bluetoothFail(w, r, err)
		return
	}
	s.bluetoothDone(w)
}

// postBluetoothScan: search for devices for 30 s (bluetooth.ScanFor). No body.
func (s *Server) postBluetoothScan(w http.ResponseWriter, r *http.Request) {
	if s.bluetoothOff(w, r) {
		return
	}
	if err := s.Bluetooth.Scan(r.Context()); err != nil {
		s.bluetoothFail(w, r, err)
		return
	}
	s.bluetoothDone(w)
}

// postBluetoothVisible: {"on": bool}, open (2 min) or close the window in which a device (a phone) can find the
// encoder and pair with it (D4).
func (s *Server) postBluetoothVisible(w http.ResponseWriter, r *http.Request) {
	if s.bluetoothOff(w, r) {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if err := decode(r, &body); err != nil || body.On == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"on": true|false}`)
		return
	}
	if err := s.Bluetooth.SetVisible(r.Context(), *body.On); err != nil {
		s.bluetoothFail(w, r, err)
		return
	}
	s.bluetoothDone(w)
}

// btAddr: the {address} of the path, as BlueZ writes it (upper case; it is validated by the manager).
func btAddr(r *http.Request) string { return strings.ToUpper(r.PathValue("address")) }

// postBluetoothDevice: pair, connect, disconnect or forget a device. It answers at once: the order runs on (pairing
// takes up to ~45 s) and GET shows it (devices[].pending, then devices[].error if it failed).
func (s *Server) postBluetoothDevice(w http.ResponseWriter, r *http.Request) {
	if s.bluetoothOff(w, r) {
		return
	}
	action := r.PathValue("action")
	switch action {
	case bluetooth.ActPair, bluetooth.ActConnect, bluetooth.ActDisconnect, bluetooth.ActForget:
	default:
		fail(w, r, 404, "unknown_action", "common.unknown_action")
		return
	}
	if err := s.Bluetooth.Act(btAddr(r), action); err != nil {
		s.bluetoothFail(w, r, err)
		return
	}
	s.bluetoothDone(w)
}

// putBluetoothDevice: {"use": "listen"|"mic"|"input"}, what a paired device is used for (one of its uses).
func (s *Server) putBluetoothDevice(w http.ResponseWriter, r *http.Request) {
	if s.bluetoothOff(w, r) {
		return
	}
	var body struct {
		Use *string `json:"use"`
	}
	if err := decode(r, &body); err != nil || body.Use == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"use": "listen"|"mic"|"input"}`)
		return
	}
	if err := s.Bluetooth.SetUse(btAddr(r), *body.Use); err != nil {
		s.bluetoothFail(w, r, err)
		return
	}
	s.bluetoothDone(w)
}
