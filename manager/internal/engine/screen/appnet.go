package screen

import (
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/appapi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// AppNet plugs the app's local API client (appapi.NetState, the Go port of engine_app.NetState) into the
// screen: the same methods, with its snapshot in the UI's terms. The ui package stays free of appapi so the
// drawing and its tests do not depend on the client.
func AppNet(n appapi.Net) ui.Net { return appNet{n} }

type appNet struct{ n appapi.Net }

func (a appNet) SetPanel(on bool)                    { a.n.SetPanel(on) }
func (a appNet) SetScan(on bool)                     { a.n.SetScan(on) }
func (a appNet) SetEnabled(on bool) bool             { return a.n.SetEnabled(on) }
func (a appNet) SetAP(on bool) bool                  { return a.n.SetAP(on) }
func (a appNet) SetClient(on bool) bool              { return a.n.SetClient(on) }
func (a appNet) SetSSID(name string) bool            { return a.n.SetSSID(name) }
func (a appNet) Connect(ssid string, pw []byte) bool { return a.n.Connect(ssid, pw) }
func (a appNet) Forget(ssid string) bool             { return a.n.Forget(ssid) }
func (a appNet) Say(text string)                     { a.n.Say(text) }

func (a appNet) Snapshot() ui.NetSnapshot { return fromApp(a.n.Snapshot()) }

// App is the app's device API of the same client (ui.AppProvider): the language, the wizard, the updates and
// the sources pages. nil if the client has none.
func (a appNet) App() ui.App {
	if p, ok := a.n.(interface{ App() ui.App }); ok {
		return p.App()
	}
	return nil
}

func fromApp(s appapi.Snapshot) ui.NetSnapshot {
	out := ui.NetSnapshot{Error: s.Error, Warning: s.Warning, ScanBusy: s.ScanBusy, ScanError: s.ScanError,
		Message: s.Message.Text, MsgUntil: s.Message.Until}
	switch s.API {
	case appapi.APIUp:
		out.API = ui.Yes
	case appapi.APIGone:
		out.API = ui.No
	}
	if st := s.Status; st != nil {
		ap := st.AccessPoint
		var enabled *bool
		if ap.Enabled {
			t := true
			enabled = &t
		}
		out.Status = &ui.NetStatus{Enabled: st.Enabled, LANEnabled: st.LAN.Enabled, Cameras: len(st.Cameras.Items),
			AccessPoint: ui.AccessPoint{Enabled: enabled, Policy: ap.Policy, State: ap.State, SSID: ap.SSID,
				Channel: ap.Channel, Clients: ap.Clients, Error: ap.Error}}
	}
	if w := s.WiFi; w != nil {
		out.Wifi = &ui.WifiCreds{SSID: w.SSID, Password: w.Password, QR: w.QR}
	}
	if c := s.Client; c != nil {
		out.Client = &ui.WifiClient{Enabled: c.Enabled, State: c.State, SSID: c.SSID, Signal: c.Signal, Band: c.Band,
			Internet: c.Internet, Error: c.Error}
	}
	if s.Scan != nil {
		out.Scan = make([]ui.ScanNet, len(s.Scan))
		for i, n := range s.Scan {
			out.Scan[i] = ui.ScanNet{SSID: n.SSID, Security: n.Security, Signal: n.Signal, Band: n.Band, Known: n.Known,
				Connected: n.Connected}
		}
	}
	if p := s.Pending; p != nil {
		out.Pending = &ui.Pending{What: p.What, On: p.On, SSID: p.Value}
	}
	if lp := s.LastPost; lp != nil {
		out.LastPost = &ui.LastPost{What: lp.What, SSID: lp.SSID, OK: lp.OK, At: lp.At}
	}
	return out
}

// legacyWifi is wifi.json for an older app without the local API (appapi.WiFiInfo, read again only when it
// changes).
func legacyWifi(path string) func() *ui.WifiCreds {
	return func() *ui.WifiCreds {
		w := appapi.WiFiInfo(path)
		if w == nil {
			return nil
		}
		return &ui.WifiCreds{SSID: w.SSID, Password: w.Password, QR: w.QR, On: w.On}
	}
}
