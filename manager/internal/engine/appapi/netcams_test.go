package appapi

import (
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// GET /network's cameras and cable, as the Network cameras page lists them: what fits is kept, the rest left out.
func TestNetworkCameraFields(t *testing.T) {
	st := networkStatus(js(t, `{"enabled": true, "accessPoint": {"enabled": true, "state": "on"},
		"lan": {"enabled": true, "state": "on", "iface": "eth0", "ip": "192.168.1.20"},
		"cameras": {"max": 2, "items": [
			{"id": "cam1", "name": "Móvil de Ana", "connected": true, "via": "wifi", "protocol": "srt", "codec": "h265",
			 "kbps": 4600, "rttMs": 7.5, "lossPct": 0.3},
			{"id": "cam2", "name": "GoPro", "connected": false, "via": null, "protocol": null, "codec": null,
			 "kbps": null, "rttMs": null, "lossPct": null}]}}`))
	if st == nil || st.LAN.IP != "192.168.1.20" || st.LAN.Iface != "eth0" || st.LAN.State != "on" || st.Cameras.Max != 2 {
		t.Fatalf("%+v", st)
	}
	c := st.Cameras.Items[0]
	if c.Protocol != "srt" || c.Codec != "h265" || c.Via != "wifi" || c.Kbps == nil || *c.Kbps != 4600 ||
		c.RTTMs == nil || *c.RTTMs != 7.5 || c.LossPct == nil || *c.LossPct != 0.3 {
		t.Errorf("%+v", c)
	}
	if c := st.Cameras.Items[1]; c.Protocol != "" || c.Codec != "" || c.Kbps != nil || c.RTTMs != nil || c.LossPct != nil {
		t.Errorf("a camera without a sender: %+v", c)
	}
	// Odd values: left out, never drawn
	st = networkStatus(js(t, `{"enabled": true, "lan": {"enabled": true, "iface": "eth0; rm", "ip": "fe80::1"},
		"cameras": {"items": [{"id": "cam1", "protocol": "webrtc", "codec": "vp9", "kbps": -1, "rttMs": "7",
		 "lossPct": 101, "via": "usb"}]}}`))
	if st.LAN.Iface != "" || st.LAN.IP != "" {
		t.Errorf("the cable: %+v", st.LAN)
	}
	if c := st.Cameras.Items[0]; c.Protocol != "" || c.Codec != "" || c.Kbps != nil || c.RTTMs != nil || c.LossPct != nil ||
		c.Via != "" {
		t.Errorf("%+v", c)
	}
}

// The Wi-Fi to join carries its saved channel (auto | 1-11); one that does not fit is left out, the rest still counts.
func TestWifiCredsChannel(t *testing.T) {
	creds := func(ch string) *WiFi {
		return wifiCreds(map[string]any{"wifi": map[string]any{"ssid": "EnhancedIRL-A1B2", "password": "abcdEFGH1234abcdEFGH",
			"qrRows": toAny(qrRows), "channel": ch}})
	}
	for ch, want := range map[string]string{"auto": "auto", "1": "1", "6": "6", "11": "11", "12": "", "0": "", "06": "",
		"auto;": ""} {
		w := creds(ch)
		if w == nil || w.Channel != want {
			t.Errorf("%q: %+v", ch, w)
		}
	}
}

func toAny(rows []string) []any {
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}

// A network action done by the device API (a camera added, a new Wi-Fi password…) asks the network again at once;
// another action does not.
func TestNetActionsAskAgain(t *testing.T) {
	a := newFakeApp(t)
	a.route("POST /cameras", 201, map[string]any{"camera": map[string]any{"id": "cam2"}})
	a.route("PUT /device", 200, deviceBody("es"))
	n := New(nil, a.sock)
	d := n.Device()
	n.take() // the first kick
	run := func(act ui.AppAction) {
		t.Helper()
		if !d.Do(act) {
			t.Fatalf("not queued: %+v", act)
		}
		q, _, _, _ := d.take()
		d.runAction(*q)
		d.changed() // as its goroutine does after an action
	}
	run(ui.AppAction{What: "cam_add", Body: map[string]any{"name": "GoPro"}})
	if _, kick, _, _, _ := n.take(); !kick {
		t.Fatal("cam_add did not ask the network again")
	}
	d.changed() // the same result again (a poll's news): no new kick
	if _, kick, _, _, _ := n.take(); kick {
		t.Fatal("kicked twice for one action")
	}
	run(ui.AppAction{What: "language", Value: "es"})
	if _, kick, _, _, _ := n.take(); kick {
		t.Fatal("a device action asked the network again")
	}
	// A refused one too (its state may have changed anyway)
	a.route("PUT /network/ap", 409, map[string]any{"error": "no_input", "message": "x"})
	run(ui.AppAction{What: "ap_enabled", Body: map[string]any{"enabled": false}})
	if _, kick, _, _, _ := n.take(); !kick {
		t.Fatal("ap_enabled did not ask the network again")
	}
}
