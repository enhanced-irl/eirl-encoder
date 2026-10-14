package wifi

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// dbusNM: NetworkManager (1.10 on the board) over the system bus. Permissions come from polkit (.pkla for the
// eirl-encoder user: network-control and settings.modify.system; WIFI.md §4).
type dbusNM struct {
	iface string

	mu   sync.Mutex
	conn *dbus.Conn
}

const (
	nmDest     = "org.freedesktop.NetworkManager"
	nmPath     = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmSettings = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	ifDevice   = nmDest + ".Device"
	ifWireless = nmDest + ".Device.Wireless"
	ifAP       = nmDest + ".AccessPoint"
	ifConn     = nmDest + ".Settings.Connection"
	ifIP4      = nmDest + ".IP4Config"
	ifProps    = "org.freedesktop.DBus.Properties"
)

// bus: the system bus connection (reopened if it dropped).
func (d *dbusNM) bus() (*dbus.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil && d.conn.Connected() {
		return d.conn, nil
	}
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	d.conn = c
	return c, nil
}

func (d *dbusNM) call(ctx context.Context, path dbus.ObjectPath, method string, args ...any) *dbus.Call {
	c, err := d.bus()
	if err != nil {
		return &dbus.Call{Err: err}
	}
	return c.Object(nmDest, path).CallWithContext(ctx, method, 0, args...)
}

func (d *dbusNM) prop(ctx context.Context, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := d.call(ctx, path, ifProps+".Get", iface, name).Store(&v)
	return v, err
}

func (d *dbusNM) props(ctx context.Context, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var m map[string]dbus.Variant
	err := d.call(ctx, path, ifProps+".GetAll", iface).Store(&m)
	return m, err
}

func (d *dbusNM) devPath(ctx context.Context) (dbus.ObjectPath, error) {
	var p dbus.ObjectPath
	err := d.call(ctx, nmPath, nmDest+".GetDeviceByIpIface", d.iface).Store(&p)
	return p, err
}

func u32(m map[string]dbus.Variant, k string) uint32 {
	v, _ := m[k].Value().(uint32)
	return v
}

func (d *dbusNM) apInfo(ctx context.Context, path dbus.ObjectPath) (apInfo, error) {
	m, err := d.props(ctx, path, ifAP)
	if err != nil {
		return apInfo{}, err
	}
	ssid, _ := m["Ssid"].Value().([]byte)
	str, _ := m["Strength"].Value().(byte)
	return apInfo{Path: string(path), SSID: ssid, Strength: str, Freq: u32(m, "Frequency"),
		Flags: u32(m, "Flags"), WPA: u32(m, "WpaFlags"), RSN: u32(m, "RsnFlags")}, nil
}

func (d *dbusNM) device(ctx context.Context) (devState, error) {
	dev, err := d.devPath(ctx)
	if err != nil {
		return devState{}, err
	}
	m, err := d.props(ctx, dev, ifDevice)
	if err != nil {
		return devState{}, err
	}
	st := devState{State: u32(m, "State")}
	if r, ok := m["StateReason"].Value().([]any); ok && len(r) == 2 {
		st.Reason, _ = r[1].(uint32)
	}
	if v, err := d.prop(ctx, dev, ifWireless, "ActiveAccessPoint"); err == nil {
		if p, ok := v.Value().(dbus.ObjectPath); ok && p != "/" {
			if a, err := d.apInfo(ctx, p); err == nil {
				st.Active = &a
			}
		}
	}
	if p, ok := m["Ip4Config"].Value().(dbus.ObjectPath); ok && p != "/" {
		if v, err := d.prop(ctx, p, ifIP4, "AddressData"); err == nil {
			if list, ok := v.Value().([]map[string]dbus.Variant); ok && len(list) > 0 {
				st.IP, _ = list[0]["address"].Value().(string)
			}
		}
	}
	return st, nil
}

func (d *dbusNM) requestScan(ctx context.Context) error {
	dev, err := d.devPath(ctx)
	if err != nil {
		return err
	}
	return d.call(ctx, dev, ifWireless+".RequestScan", map[string]dbus.Variant{}).Err
}

func (d *dbusNM) accessPoints(ctx context.Context) ([]apInfo, error) {
	dev, err := d.devPath(ctx)
	if err != nil {
		return nil, err
	}
	var paths []dbus.ObjectPath
	if err := d.call(ctx, dev, ifWireless+".GetAllAccessPoints").Store(&paths); err != nil {
		return nil, err
	}
	out := make([]apInfo, 0, len(paths))
	for _, p := range paths {
		if a, err := d.apInfo(ctx, p); err == nil { // an AP that vanishes in between: skipped
			out = append(out, a)
		}
	}
	return out, nil
}

type nmSettingsMap = map[string]map[string]dbus.Variant

// profiles: only the app's (eirl-wifi-*, wifi type). Secrets are not requested (GetSettings does not return them).
func (d *dbusNM) profiles(ctx context.Context) ([]profile, error) {
	var paths []dbus.ObjectPath
	if err := d.call(ctx, nmSettings, nmDest+".Settings.ListConnections").Store(&paths); err != nil {
		return nil, err
	}
	var out []profile
	for _, p := range paths {
		var s nmSettingsMap
		if err := d.call(ctx, p, ifConn+".GetSettings").Store(&s); err != nil {
			continue
		}
		id, _ := s["connection"]["id"].Value().(string)
		typ, _ := s["connection"]["type"].Value().(string)
		if typ != "802-11-wireless" || !strings.HasPrefix(id, ProfilePrefix) {
			continue
		}
		ssid, _ := s["802-11-wireless"]["ssid"].Value().([]byte)
		out = append(out, profile{Path: string(p), ID: id, SSID: ssid})
	}
	return out, nil
}

// settings: the NetworkManager profile. System-wide (no permissions: it is stored in its root keyfile) and bound to
// wlan0 (never to ap0). IPv4 by DHCP; no IPv6 (SRTLA uses IPv4, and this way no routes sneak in).
func (d *dbusNM) settings(p newProfile) nmSettingsMap {
	s := nmSettingsMap{
		"connection": {"id": dbus.MakeVariant(p.ID), "uuid": dbus.MakeVariant(p.UUID), "type": dbus.MakeVariant("802-11-wireless"),
			"autoconnect": dbus.MakeVariant(true), "interface-name": dbus.MakeVariant(p.Iface)},
		"802-11-wireless": {"ssid": dbus.MakeVariant(p.SSID), "mode": dbus.MakeVariant("infrastructure")},
		"ipv4":            {"method": dbus.MakeVariant("auto")},
		"ipv6":            {"method": dbus.MakeVariant("ignore")},
	}
	if p.PSK != "" {
		s["802-11-wireless"]["security"] = dbus.MakeVariant("802-11-wireless-security")
		s["802-11-wireless-security"] = map[string]dbus.Variant{"key-mgmt": dbus.MakeVariant("wpa-psk"), "psk": dbus.MakeVariant(p.PSK)}
	}
	return s
}

func (d *dbusNM) addAndActivate(ctx context.Context, p newProfile, ap string) error {
	dev, err := d.devPath(ctx)
	if err != nil {
		return err
	}
	return d.call(ctx, nmPath, nmDest+".AddAndActivateConnection", d.settings(p), dev, dbus.ObjectPath(ap)).Err
}

// update: the whole profile again (it keeps its uuid), with the new password or without one (open network).
func (d *dbusNM) update(ctx context.Context, path string, p newProfile) error {
	if p.UUID == "" {
		var s nmSettingsMap
		if err := d.call(ctx, dbus.ObjectPath(path), ifConn+".GetSettings").Store(&s); err != nil {
			return err
		}
		p.UUID, _ = s["connection"]["uuid"].Value().(string)
		if p.UUID == "" {
			return errors.New("wifi: profile without uuid")
		}
	}
	return d.call(ctx, dbus.ObjectPath(path), ifConn+".Update", d.settings(p)).Err
}

func (d *dbusNM) activate(ctx context.Context, path, ap string) error {
	dev, err := d.devPath(ctx)
	if err != nil {
		return err
	}
	return d.call(ctx, nmPath, nmDest+".ActivateConnection", dbus.ObjectPath(path), dev, dbus.ObjectPath(ap)).Err
}

func (d *dbusNM) remove(ctx context.Context, path string) error {
	return d.call(ctx, dbus.ObjectPath(path), ifConn+".Delete").Err
}

// disconnect: Device.Disconnect. If it was not connected, NetworkManager returns NotActive: not an error.
func (d *dbusNM) disconnect(ctx context.Context) error {
	dev, err := d.devPath(ctx)
	if err != nil {
		return err
	}
	err = d.call(ctx, dev, ifDevice+".Disconnect").Err
	var de dbus.Error
	if errors.As(err, &de) && strings.HasSuffix(de.Name, ".NotActive") {
		return nil
	}
	return err
}

// stateChanges: wlan0's Device.StateChanged signal (new, old, reason), over a dedicated bus connection that is
// closed when ctx ends.
func (d *dbusNM) stateChanges(ctx context.Context) (<-chan stateChange, error) {
	dev, err := d.devPath(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(dev), dbus.WithMatchInterface(ifDevice), dbus.WithMatchMember("StateChanged")); err != nil {
		conn.Close()
		return nil, err
	}
	sig := make(chan *dbus.Signal, 32)
	conn.Signal(sig)
	out := make(chan stateChange, 32)
	go func() {
		defer close(out)
		defer conn.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-sig:
				if !ok || s == nil {
					return
				}
				if s.Path != dev || s.Name != ifDevice+".StateChanged" || len(s.Body) != 3 {
					continue
				}
				n, _ := s.Body[0].(uint32)
				r, _ := s.Body[2].(uint32)
				select {
				case out <- stateChange{State: n, Reason: r}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (d *dbusNM) setAutoconnect(ctx context.Context, on bool) error {
	dev, err := d.devPath(ctx)
	if err != nil {
		return err
	}
	return d.call(ctx, dev, ifProps+".Set", ifDevice, "Autoconnect", dbus.MakeVariant(on)).Err
}
