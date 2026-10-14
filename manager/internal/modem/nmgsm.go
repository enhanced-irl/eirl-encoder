package modem

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// A ModemManager modem only connects through NetworkManager with a "gsm" profile (APN, roaming, PIN). The app keeps
// one per modem, system-wide (stored in NetworkManager's root keyfile: the PIN never touches the app's files), bound
// to the modem by ModemManager's DeviceIdentifier (gsm.device-id), with a fixed uuid derived from the modem's id.
// Permission: settings.modify.system, the same .pkla as the Wi-Fi client (deploy/eirl-encoder-nm.pkla).
//
// Written from NetworkManager 1.10's D-Bus API and settings documentation; to be checked with real modems (M0).

const (
	nmDest      = "org.freedesktop.NetworkManager"
	nmPath      = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmSettings  = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	nmConn      = nmDest + ".Settings.Connection"
	nmDevice    = nmDest + ".Device"
	profilePref = "eirl-modem-"
)

type nmMap = map[string]map[string]dbus.Variant

// nmBus: what the profiles need from NetworkManager (a fake in the tests).
type nmBus interface {
	connectionByUUID(ctx context.Context, uuid string) (dbus.ObjectPath, error) // "" if there is none
	settings(ctx context.Context, path dbus.ObjectPath) (nmMap, error)
	secrets(ctx context.Context, path dbus.ObjectPath, setting string) (nmMap, error)
	add(ctx context.Context, s nmMap) (dbus.ObjectPath, error)
	update(ctx context.Context, path dbus.ObjectPath, s nmMap) error
	// reactivate: activate the profile again on the modem's NetworkManager device (Udi = the ModemManager object
	// path), so that a new APN or roaming takes effect now; nothing if that device is not there
	reactivate(ctx context.Context, path dbus.ObjectPath, mmPath string) error
}

// profileUUID: a fixed uuid (v5-like, from SHA-1) for a modem's profile.
func profileUUID(id string) string {
	h := sha1.Sum([]byte("eirl-modem-profile:" + id))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// gsmProfile: what the app wants in a modem's profile.
type gsmProfile struct {
	ID       string // the modem's id
	DeviceID string // ModemManager's DeviceIdentifier ("" = any modem: not used)
	// SimID: the SIM's ICCID. The profile only applies to that SIM (gsm.sim-id), so a stored PIN is never offered to
	// another one: a swapped SIM with a PIN of its own would end up blocked (PUK) after three starts. "" = unknown:
	// then no PIN is kept at all.
	SimID   string
	APN     string // "" = the network's default
	Roaming bool
	PIN     string // a PIN to keep ("" = keep the stored one, if it was for this SIM)
	DropPIN bool   // forget the stored PIN (the SIM's PIN was turned off)
}

func (p gsmProfile) settings(keepPIN string) nmMap {
	gsm := map[string]dbus.Variant{"home-only": dbus.MakeVariant(!p.Roaming)}
	if p.DeviceID != "" {
		gsm["device-id"] = dbus.MakeVariant(p.DeviceID)
	}
	if p.SimID != "" {
		gsm["sim-id"] = dbus.MakeVariant(p.SimID)
	}
	if p.APN != "" {
		gsm["apn"] = dbus.MakeVariant(p.APN)
	}
	pin := p.PIN
	if pin == "" {
		pin = keepPIN
	}
	if pin != "" && p.SimID != "" && !p.DropPIN {
		gsm["pin"] = dbus.MakeVariant(pin)
		gsm["pin-flags"] = dbus.MakeVariant(uint32(0)) // system-owned: in NetworkManager's root keyfile
	}
	return nmMap{
		"connection": {"id": dbus.MakeVariant(profilePref + p.ID), "uuid": dbus.MakeVariant(profileUUID(p.ID)),
			"type": dbus.MakeVariant("gsm"), "autoconnect": dbus.MakeVariant(true)},
		"gsm":  gsm,
		"ipv4": {"method": dbus.MakeVariant("auto")},
		// IPv6 off: SRTLA and the dispatcher are IPv4, and this way no IPv6 routes sneak in (MODEMS.md §10)
		"ipv6": {"method": dbus.MakeVariant("ignore")},
	}
}

// ensureProfile creates or updates a modem's profile and, if it changed (or always, with reactivate) and the modem
// is there, activates it again. It returns whether the profile changed.
func ensureProfile(ctx context.Context, bus nmBus, p gsmProfile, mmPath string, reactivate bool) (bool, error) {
	if bus == nil {
		return false, errors.New("modem: no NetworkManager")
	}
	path, err := bus.connectionByUUID(ctx, profileUUID(p.ID))
	if err != nil {
		return false, err
	}
	if path == "" {
		np, err := bus.add(ctx, p.settings(""))
		if err != nil {
			return false, fmt.Errorf("add profile: %w", err)
		}
		_ = bus.reactivate(ctx, np, mmPath)
		return true, nil
	}
	cur, err := bus.settings(ctx, path)
	if err != nil {
		return false, err
	}
	if p.PIN == "" && !p.DropPIN && same(cur, p) {
		if reactivate {
			_ = bus.reactivate(ctx, path, mmPath)
		}
		return false, nil
	}
	// The stored PIN is kept only for the same SIM. Read only when it is needed, and a failed read stops the update:
	// rewriting the profile without it would lose the PIN without a word (D-M6)
	keep := ""
	curSim, _ := cur["gsm"]["sim-id"].Value().(string)
	if p.PIN == "" && !p.DropPIN && p.SimID != "" && curSim == p.SimID {
		sec, err := bus.secrets(ctx, path, "gsm")
		if err != nil && !noSecrets(err) {
			return false, fmt.Errorf("profile secrets: %w", err)
		}
		keep, _ = sec["gsm"]["pin"].Value().(string)
	}
	if err := bus.update(ctx, path, p.settings(keep)); err != nil {
		return false, fmt.Errorf("update profile: %w", err)
	}
	_ = bus.reactivate(ctx, path, mmPath)
	return true, nil
}

// noSecrets: NetworkManager's answer for a profile that has no secrets stored (not a failure).
func noSecrets(err error) bool {
	var de dbus.Error
	return errors.As(err, &de) && (strings.HasSuffix(de.Name, ".NoSecrets") || strings.HasSuffix(de.Name, ".InvalidSetting"))
}

// same: the stored profile already says what p asks (the PIN aside).
func same(cur nmMap, p gsmProfile) bool {
	g := cur["gsm"]
	apn, _ := g["apn"].Value().(string)
	homeOnly, _ := g["home-only"].Value().(bool)
	dev, _ := g["device-id"].Value().(string)
	sim, _ := g["sim-id"].Value().(string)
	auto, _ := cur["connection"]["autoconnect"].Value().(bool)
	if _, set := cur["connection"]["autoconnect"]; !set {
		auto = true // NetworkManager's default
	}
	return apn == p.APN && homeOnly == !p.Roaming && dev == p.DeviceID && sim == p.SimID && auto
}

// --- the real bus -------------------------------------------------------------------------------------------

type nmSystem struct {
	mu   sync.Mutex
	conn *dbus.Conn
}

func (n *nmSystem) bus() (*dbus.Conn, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn != nil && n.conn.Connected() {
		return n.conn, nil
	}
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	n.conn = c
	return c, nil
}

func (n *nmSystem) call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) *dbus.Call {
	c, err := n.bus()
	if err != nil {
		return &dbus.Call{Err: err}
	}
	return c.Object(nmDest, path).CallWithContext(ctx, method, 0, args...)
}

func (n *nmSystem) connectionByUUID(ctx context.Context, uuid string) (dbus.ObjectPath, error) {
	var p dbus.ObjectPath
	err := n.call(ctx, nmSettings, nmDest+".Settings.GetConnectionByUuid", uuid).Store(&p)
	var de dbus.Error
	if errors.As(err, &de) && strings.HasSuffix(de.Name, ".InvalidConnection") {
		return "", nil
	}
	return p, err
}

func (n *nmSystem) settings(ctx context.Context, path dbus.ObjectPath) (nmMap, error) {
	var s nmMap
	err := n.call(ctx, path, nmConn+".GetSettings").Store(&s)
	return s, err
}

func (n *nmSystem) secrets(ctx context.Context, path dbus.ObjectPath, setting string) (nmMap, error) {
	var s nmMap
	err := n.call(ctx, path, nmConn+".GetSecrets", setting).Store(&s)
	return s, err
}

func (n *nmSystem) add(ctx context.Context, s nmMap) (dbus.ObjectPath, error) {
	var p dbus.ObjectPath
	err := n.call(ctx, nmSettings, nmDest+".Settings.AddConnection", s).Store(&p)
	return p, err
}

func (n *nmSystem) update(ctx context.Context, path dbus.ObjectPath, s nmMap) error {
	return n.call(ctx, path, nmConn+".Update", s).Err
}

func (n *nmSystem) reactivate(ctx context.Context, path dbus.ObjectPath, mmPath string) error {
	if mmPath == "" {
		return nil
	}
	var devs []dbus.ObjectPath
	if err := n.call(ctx, nmPath, nmDest+".GetDevices").Store(&devs); err != nil {
		return err
	}
	for _, d := range devs {
		var v dbus.Variant
		if err := n.call(ctx, d, "org.freedesktop.DBus.Properties.Get", nmDevice, "Udi").Store(&v); err != nil {
			continue
		}
		if udi, _ := v.Value().(string); udi == mmPath {
			return n.call(ctx, nmPath, nmDest+".ActivateConnection", path, d, dbus.ObjectPath("/")).Err
		}
	}
	return nil // the modem is not a NetworkManager device yet: autoconnect will do it
}
