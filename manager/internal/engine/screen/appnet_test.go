package screen

import (
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/appapi"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The Network cameras page's data reaches the screen: the cameras with their state, the cable and the most there
// can be, and the Wi-Fi's saved channel.
func TestAppNetCameras(t *testing.T) {
	kb, loss, rtt := 4600, 0.3, 7.0
	s := appapi.Snapshot{API: appapi.APIUp,
		Status: &appapi.Status{Enabled: true, AccessPoint: appapi.AccessPoint{Enabled: true, Policy: "always", State: "on"},
			LAN: appapi.LAN{Enabled: true, State: "on", Iface: "eth0", IP: "192.168.1.20"},
			Cameras: appapi.Cameras{Max: 2, Items: []appapi.Camera{{ID: "cam1", Name: "Móvil de Ana", Connected: true,
				Via: "wifi", Protocol: "srt", Codec: "h265", Kbps: &kb, RTTMs: &rtt, LossPct: &loss}}}},
		WiFi: &appapi.WiFi{SSID: "EnhancedIRL-A1B2", Password: "abcdEFGH1234abcdEFGH", Channel: "6"}}
	n := fromApp(s)
	st := n.Status
	if st == nil || st.CamMax != 2 || st.Cameras != 1 || !st.LANEnabled ||
		st.LAN != (ui.LANStatus{State: "on", Iface: "eth0", IP: "192.168.1.20"}) || len(st.CamItems) != 1 {
		t.Fatalf("%+v", st)
	}
	c := st.CamItems[0]
	if c.ID != "cam1" || c.Name != "Móvil de Ana" || !c.Connected || c.Via != "wifi" || c.Protocol != "srt" ||
		c.Codec != "h265" || *c.Kbps != 4600 || *c.RTTMs != 7 || *c.LossPct != 0.3 {
		t.Errorf("%+v", c)
	}
	if n.Wifi == nil || n.Wifi.Channel != "6" {
		t.Errorf("%+v", n.Wifi)
	}
	// No cameras: an empty list (the app gave it), not «not given»
	s.Status.Cameras.Items = []appapi.Camera{}
	if st := fromApp(s).Status; st.CamItems == nil || len(st.CamItems) != 0 {
		t.Errorf("%+v", st.CamItems)
	}
}
