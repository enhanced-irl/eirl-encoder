package bluetooth

import (
	"log/slog"
	"strings"

	"github.com/godbus/dbus/v5"
)

// agent is our org.bluez.Agent1 (BLUETOOTH.md §3.2, D4): BlueZ asks it whether a pairing or a service may go ahead.
// It never asks the user (capability NoInputNoOutput: audio devices pair with "just works") and it says yes only to
// a pairing the app started itself or to one inside the visible window. Everything else is refused.
type agent struct {
	// allowed: may a pairing or a service of the device at that address go ahead now?
	allowed func(addr string) bool
	log     *slog.Logger
}

// audioServices: the only services the agent authorises (D1, security review): A2DP, AVRCP, hands-free and headset.
// Never HID (a "keyboard" typing into the encoder), networking or SIM access; bluetoothd's drop-in does not even load
// those plugins.
var audioServices = map[string]bool{
	"00001108-0000-1000-8000-00805f9b34fb": true, // HSP (headset)
	"0000110a-0000-1000-8000-00805f9b34fb": true, // A2DP source
	"0000110b-0000-1000-8000-00805f9b34fb": true, // A2DP sink
	"0000110c-0000-1000-8000-00805f9b34fb": true, // AVRCP target
	"0000110d-0000-1000-8000-00805f9b34fb": true, // A2DP (Advanced Audio)
	"0000110e-0000-1000-8000-00805f9b34fb": true, // AVRCP
	"0000110f-0000-1000-8000-00805f9b34fb": true, // AVRCP controller
	"00001112-0000-1000-8000-00805f9b34fb": true, // HSP audio gateway
	"0000111e-0000-1000-8000-00805f9b34fb": true, // HFP (hands-free)
	"0000111f-0000-1000-8000-00805f9b34fb": true, // HFP audio gateway
}

var errRejected = dbus.NewError("org.bluez.Error.Rejected", nil)

// legacyPIN: the fixed PIN of the old audio devices (before Bluetooth 2.1, no "just works"), only inside the window
const legacyPIN = "0000"

func (a *agent) decide(what string, dev dbus.ObjectPath, ok bool) *dbus.Error {
	addr := addrOfPath(string(dev))
	a.log.Info("bluetooth agent", "event", "bt_agent", "request", what, "device", addr, "allowed", ok)
	if ok {
		return nil
	}
	return errRejected
}

// Release: BlueZ no longer uses the agent (bluetoothd stopping). The app registers it again when BlueZ comes back.
func (a *agent) Release() *dbus.Error { return nil }

func (a *agent) RequestPinCode(dev dbus.ObjectPath) (string, *dbus.Error) {
	if err := a.decide("pin_code", dev, a.allowed(addrOfPath(string(dev)))); err != nil {
		return "", err
	}
	return legacyPIN, nil
}

func (a *agent) DisplayPinCode(dev dbus.ObjectPath, pin string) *dbus.Error {
	return a.decide("display_pin", dev, a.allowed(addrOfPath(string(dev))))
}

// RequestPasskey: a device that needs a passkey typed (a keyboard). Not in this version (D1).
func (a *agent) RequestPasskey(dev dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, a.decide("passkey", dev, false)
}

func (a *agent) DisplayPasskey(dev dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return a.decide("display_passkey", dev, false)
}

// RequestConfirmation: numeric comparison. Nobody can compare the number on the encoder, so it is accepted like "just
// works", and only when the pairing is allowed now.
func (a *agent) RequestConfirmation(dev dbus.ObjectPath, passkey uint32) *dbus.Error {
	return a.decide("confirmation", dev, a.allowed(addrOfPath(string(dev))))
}

// RequestAuthorization: an incoming "just works" pairing.
func (a *agent) RequestAuthorization(dev dbus.ObjectPath) *dbus.Error {
	return a.decide("authorization", dev, a.allowed(addrOfPath(string(dev))))
}

// AuthorizeService: a device that is not trusted wants a service (BlueZ does not ask for trusted ones): only an audio
// one, and only while its pairing is allowed (the window, or the app's own pairing). What the app pairs it trusts.
func (a *agent) AuthorizeService(dev dbus.ObjectPath, uuid string) *dbus.Error {
	addr := addrOfPath(string(dev))
	return a.decide("service", dev, audioServices[strings.ToLower(uuid)] && a.allowed(addr))
}

func (a *agent) Cancel() *dbus.Error { return nil }
