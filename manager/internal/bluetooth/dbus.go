package bluetooth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// dbusBT: BlueZ (5.48 on the board) and BlueALSA (4.1) over the system bus. One connection for everything: the agent
// is exported on it and a search belongs to the connection that started it (BlueZ stops it if the connection goes).
// Permissions: the D-Bus policy eirl-encoder-bluetooth.conf (BLUETOOTH.md §3.7).
type dbusBT struct {
	mu   sync.Mutex
	conn *dbus.Conn
}

const (
	bluezDest    = "org.bluez"
	bluealsaDest = "org.bluealsa"
	ifAdapter    = bluezDest + ".Adapter1"
	ifDevice     = bluezDest + ".Device1"
	ifAgentMgr   = bluezDest + ".AgentManager1"
	ifAgent      = bluezDest + ".Agent1"
	ifPCM        = bluealsaDest + ".PCM1"
	ifProps      = "org.freedesktop.DBus.Properties"
	ifObjMgr     = "org.freedesktop.DBus.ObjectManager"

	agentPath = dbus.ObjectPath("/com/enhancedirl/encoder/agent")
	// The agent never asks the user anything (D4): audio devices pair with "just works"
	agentCapability = "NoInputNoOutput"
)

func (d *dbusBT) bus() (*dbus.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil && d.conn.Connected() {
		return d.conn, nil
	}
	return nil, errors.New("bluetooth: not connected to the system bus")
}

func (d *dbusBT) obj(dest string, path dbus.ObjectPath) (dbus.BusObject, error) {
	c, err := d.bus()
	if err != nil {
		return nil, err
	}
	return c.Object(dest, path), nil
}

func (d *dbusBT) call(ctx context.Context, dest string, path dbus.ObjectPath, method string, args ...any) *dbus.Call {
	o, err := d.obj(dest, path)
	if err != nil {
		return &dbus.Call{Err: err}
	}
	return o.CallWithContext(ctx, method, 0, args...)
}

// owner: the unique name of whoever holds a well-known name ("" = nobody: the daemon is not running).
func (d *dbusBT) owner(ctx context.Context, name string) string {
	c, err := d.bus()
	if err != nil {
		return ""
	}
	var o string
	if c.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&o) != nil {
		return ""
	}
	return o
}

func (d *dbusBT) snapshot(ctx context.Context) (snapshot, error) {
	owner := d.owner(ctx, bluezDest)
	if owner == "" {
		return snapshot{}, errNoStack
	}
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := d.call(ctx, bluezDest, "/", ifObjMgr+".GetManagedObjects").Store(&objs); err != nil {
		return snapshot{}, err
	}
	s := snapshot{Owner: owner}
	var adapters []string
	for path, ifs := range objs {
		if _, ok := ifs[ifAdapter]; ok {
			adapters = append(adapters, string(path))
		}
	}
	if len(adapters) == 0 {
		return s, nil
	}
	sort.Strings(adapters) // hci0 first
	ap := adapters[0]
	a := objs[dbus.ObjectPath(ap)][ifAdapter]
	s.Adapter = &adapter{Path: ap, Address: str(a, "Address"), Alias: str(a, "Alias"), Powered: boolean(a, "Powered"),
		Discoverable: boolean(a, "Discoverable"), Pairable: boolean(a, "Pairable"), Discovering: boolean(a, "Discovering")}
	for path, ifs := range objs {
		p, ok := ifs[ifDevice]
		if !ok || !strings.HasPrefix(string(path), ap+"/") {
			continue
		}
		dv := rawDevice{Path: string(path), Address: str(p, "Address"), Icon: str(p, "Icon"), Paired: boolean(p, "Paired"),
			Trusted: boolean(p, "Trusted"), Connected: boolean(p, "Connected"), Blocked: boolean(p, "Blocked")}
		if n, ok := p["Name"]; ok {
			dv.Name, _ = n.Value().(string)
			dv.HasName = true
		}
		if al := str(p, "Alias"); al != "" && (dv.HasName || dv.Paired) {
			dv.Name = al // the name the user gave it, or BlueZ's copy of Name
		}
		if c, ok := p["Class"].Value().(uint32); ok {
			dv.Class = c
		}
		if u, ok := p["UUIDs"].Value().([]string); ok {
			dv.UUIDs = u
		}
		if r, ok := p["RSSI"].Value().(int16); ok {
			v := int(r)
			dv.RSSI = &v
		}
		s.Devices = append(s.Devices, dv)
	}
	return s, nil
}

func str(m map[string]dbus.Variant, k string) string {
	v, _ := m[k].Value().(string)
	return v
}

func boolean(m map[string]dbus.Variant, k string) bool {
	v, _ := m[k].Value().(bool)
	return v
}

// pcms: BlueALSA's PCMs (4.1: its ObjectManager on /org/bluealsa). An error: BlueALSA is not running.
func (d *dbusBT) pcms(ctx context.Context) ([]pcm, error) {
	if d.owner(ctx, bluealsaDest) == "" {
		return nil, errNoAudio
	}
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := d.call(ctx, bluealsaDest, "/org/bluealsa", ifObjMgr+".GetManagedObjects").Store(&objs); err != nil {
		return nil, err
	}
	var out []pcm
	for _, ifs := range objs {
		p, ok := ifs[ifPCM]
		if !ok {
			continue
		}
		dev, _ := p["Device"].Value().(dbus.ObjectPath)
		delay, _ := p["Delay"].Value().(uint16)
		if x, ok := pcmOf(string(dev), str(p, "Transport"), str(p, "Mode"), int(delay)/10); ok {
			x.Running = boolean(p, "Running")
			out = append(out, x)
		}
	}
	return out, nil
}

// pcmOf: what a BlueALSA PCM is, from its properties. Transport is the role the board plays (A2DP-source: we send to
// headphones; A2DP-sink: a phone sends to us; HFP-AG / HSP-AG: we are the gateway of a headset); Mode is the PCM's
// side (sink: we write it, source: we read it).
func pcmOf(devPath, transport, mode string, delayMs int) (pcm, bool) {
	addr := addrOfPath(devPath)
	if addr == "" {
		return pcm{}, false
	}
	p := pcm{Address: addr, DelayMs: max(delayMs, 0)}
	switch {
	case strings.HasPrefix(transport, "A2DP"):
		p.Profile = "a2dp"
	case strings.HasPrefix(transport, "HFP") || strings.HasPrefix(transport, "HSP"):
		p.Profile = "sco"
	default:
		return pcm{}, false
	}
	switch mode {
	case "sink":
		p.Out = true
	case "source":
	default:
		return pcm{}, false
	}
	return p, true
}

// addrOfPath: the address of a BlueZ device path (/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF).
func addrOfPath(p string) string {
	i := strings.LastIndex(p, "/dev_")
	if i < 0 {
		return ""
	}
	a := strings.ReplaceAll(p[i+5:], "_", ":")
	if !validAddr(a) {
		return ""
	}
	return a
}

func (d *dbusBT) setAdapter(ctx context.Context, path, prop string, v any) error {
	return d.call(ctx, bluezDest, dbus.ObjectPath(path), ifProps+".Set", ifAdapter, prop, dbus.MakeVariant(v)).Err
}

func (d *dbusBT) discovery(ctx context.Context, path string, on bool) error {
	p := dbus.ObjectPath(path)
	if !on {
		return d.call(ctx, bluezDest, p, ifAdapter+".StopDiscovery").Err
	}
	// Audio is classic Bluetooth (BR/EDR): no LE advertisers in the list
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant("bredr")}
	if err := d.call(ctx, bluezDest, p, ifAdapter+".SetDiscoveryFilter", filter).Err; err != nil {
		return err
	}
	return d.call(ctx, bluezDest, p, ifAdapter+".StartDiscovery").Err
}

func (d *dbusBT) pair(ctx context.Context, dev string) error {
	return d.call(ctx, bluezDest, dbus.ObjectPath(dev), ifDevice+".Pair").Err
}

func (d *dbusBT) connect(ctx context.Context, dev string) error {
	return d.call(ctx, bluezDest, dbus.ObjectPath(dev), ifDevice+".Connect").Err
}

func (d *dbusBT) disconnect(ctx context.Context, dev string) error {
	return d.call(ctx, bluezDest, dbus.ObjectPath(dev), ifDevice+".Disconnect").Err
}

func (d *dbusBT) setTrusted(ctx context.Context, dev string, on bool) error {
	return d.call(ctx, bluezDest, dbus.ObjectPath(dev), ifProps+".Set", ifDevice, "Trusted", dbus.MakeVariant(on)).Err
}

func (d *dbusBT) remove(ctx context.Context, adapterPath, dev string) error {
	return d.call(ctx, bluezDest, dbus.ObjectPath(adapterPath), ifAdapter+".RemoveDevice", dbus.ObjectPath(dev)).Err
}

// registerAgent registers our agent (exported by watch) as BlueZ's default one. Again after bluetoothd restarts: the
// old registration went with it.
func (d *dbusBT) registerAgent(ctx context.Context) error {
	c := d.call(ctx, bluezDest, "/org/bluez", ifAgentMgr+".RegisterAgent", agentPath, agentCapability)
	if c.Err != nil && !isDBusError(c.Err, "org.bluez.Error.AlreadyExists") {
		return c.Err
	}
	return d.call(ctx, bluezDest, "/org/bluez", ifAgentMgr+".RequestDefaultAgent", agentPath).Err
}

// watch opens the connection, exports the agent on it and subscribes to what BlueZ and BlueALSA signal (objects
// added and removed, properties changed, the daemons starting and stopping). A tick per signal; the channel closes
// when the connection is lost or ctx ends.
func (d *dbusBT) watch(ctx context.Context, ag *agent) (<-chan struct{}, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	if err := conn.Export(ag, agentPath, ifAgent); err != nil {
		conn.Close()
		return nil, err
	}
	for _, opts := range [][]dbus.MatchOption{
		{dbus.WithMatchSender(bluezDest), dbus.WithMatchInterface(ifObjMgr)},
		{dbus.WithMatchSender(bluezDest), dbus.WithMatchInterface(ifProps), dbus.WithMatchMember("PropertiesChanged")},
		{dbus.WithMatchSender(bluealsaDest), dbus.WithMatchInterface(ifObjMgr)},
		{dbus.WithMatchSender(bluealsaDest), dbus.WithMatchInterface(ifProps), dbus.WithMatchMember("PropertiesChanged")},
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, bluezDest)},
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, bluealsaDest)},
	} {
		if err := conn.AddMatchSignal(opts...); err != nil {
			conn.Close()
			return nil, err
		}
	}
	d.mu.Lock()
	if d.conn != nil {
		d.conn.Close()
	}
	d.conn = conn
	d.mu.Unlock()
	sig := make(chan *dbus.Signal, 64)
	conn.Signal(sig)
	out := make(chan struct{}, 1)
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
				select {
				case out <- struct{}{}:
				default: // one pending tick is enough: a refresh reads everything
				}
			}
		}
	}()
	return out, nil
}

func isDBusError(err error, name string) bool {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name == name
	}
	var dp *dbus.Error
	if errors.As(err, &dp) {
		return dp.Name == name
	}
	return false
}

// dbusErrorName: the D-Bus error's name, or "".
func dbusErrorName(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name
	}
	var dp *dbus.Error
	if errors.As(err, &dp) {
		return dp.Name
	}
	return ""
}

// failOf: why an order failed, for the web and the screen (Fail*), from BlueZ's error.
func failOf(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return FailTimeout
	}
	name := dbusErrorName(err)
	msg := strings.ToLower(err.Error())
	switch {
	case name == "org.bluez.Error.AuthenticationRejected" || name == "org.bluez.Error.AuthenticationCanceled" ||
		name == "org.bluez.Error.AuthenticationFailed" || name == "org.bluez.Error.Rejected":
		return FailRejected
	// A page timeout is the device not answering the call at all (off, far, not in pairing mode): before "timeout"
	case name == "org.bluez.Error.ConnectionAttemptFailed" || strings.Contains(msg, "host is down") ||
		strings.Contains(msg, "page timeout") || strings.Contains(msg, "no route"):
		return FailNotReachable
	case name == "org.bluez.Error.AuthenticationTimeout" || strings.Contains(msg, "timeout"):
		return FailTimeout
	case name == "org.freedesktop.DBus.Error.UnknownObject" || name == "org.bluez.Error.DoesNotExist":
		return FailGone
	}
	return FailOther
}
