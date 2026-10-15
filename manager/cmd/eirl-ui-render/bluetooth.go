package main

import (
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The FAKE Bluetooth of Settings › Wireless › Bluetooth (BLUETOOTH.md §3.6):
//
//	settings-bluetooth (on, two paired, one of them playing the program, and three found) ·
//	settings-bluetooth-unavailable (no module) · settings-bluetooth-nostack · settings-bluetooth-noaudio (devices
//	managed, not heard: a warning over the controls) · settings-bluetooth-powerfailed (the adapter does not turn on) ·
//	settings-bluetooth-off · settings-bluetooth-scanning (a search with one found so far) ·
//	settings-bluetooth-visible (the 2-minute window, 1:47 left) · settings-bluetooth-busy (a pairing under way, an
//	error, the other orders greyed out) · settings-bluetooth-many (the list in pages) · settings-bluetooth-page2 ·
//	settings-bluetooth-device (a paired device's view: its two uses) · settings-bluetooth-forget (Forget waiting for
//	its second tap)
//
// The audio view's headphones on Bluetooth: the modifier +phbt (audio+phbt).

// btHeadphones is the fake headphones' address (the device view opens them).
const btHeadphones = "AC:80:0A:12:34:56"

func btFake(variant string, snap *ui.AppSnapshot) {
	rssi := func(v int) *int { return &v }
	st := &ui.BTState{Available: true, On: true, Powered: true, Name: "Estudio 2", Devices: []ui.BTDevice{
		{Address: btHeadphones, Name: "WH-1000XM4", Kind: "headphones", Paired: true, Connected: true, Ready: true,
			Use: "listen", Uses: []string{"listen", "mic"}, Output: true},
		{Address: "70:BF:92:0C:4E:11", Name: "Jabra Talk 45", Kind: "headset", Paired: true, Use: "mic",
			Uses: []string{"mic"}},
		{Address: "F8:DF:15:A1:B2:C3", Name: "JBL Flip 5", Kind: "speaker", Uses: []string{"listen"}, RSSI: rssi(-58)},
		{Address: "5C:F7:E6:01:02:03", Name: "iPhone de Ana", Kind: "phone", Uses: []string{"input"}, RSSI: rssi(-64)},
		{Address: "4C:87:5D:9A:8B:7C", Name: "Bose QC35 II", Kind: "headphones", Uses: []string{"listen", "mic"},
			RSSI: rssi(-77)},
	}}
	snap.Bluetooth, snap.BluetoothAPI = st, ui.Yes
	switch variant {
	case "settings-bluetooth-unavailable":
		*st = ui.BTState{Reason: "no_adapter"}
	case "settings-bluetooth-nostack":
		*st = ui.BTState{Reason: "no_stack"}
	case "settings-bluetooth-noaudio": // it works, its audio does not: the warning over the controls
		st.Reason = "no_audio"
	case "settings-bluetooth-powerfailed": // the adapter does not turn on (blocked, or its firmware fails)
		st.Powered, st.PowerFailed = false, true
	case "settings-bluetooth-off":
		st.On, st.Powered, st.Devices = false, false, nil
	case "settings-bluetooth-scanning":
		st.Scanning = true
		st.Devices = st.Devices[:3]
	case "settings-bluetooth-visible":
		st.Visible, st.VisibleLeft = true, 107
		st.Devices = st.Devices[:2]
	case "settings-bluetooth-busy":
		st.Devices[2].Pending = "pair"
		st.Devices[1].Error = "not_reachable"
	case "settings-bluetooth-many", "settings-bluetooth-page2":
		for _, n := range []struct{ addr, name, kind string }{{"00:1B:66:AA:01:02", "Sennheiser HD 450BT", "headphones"},
			{"00:1B:66:AA:01:03", "Altavoz de la cocina", "speaker"}, {"00:1B:66:AA:01:04", "Galaxy S23", "phone"},
			{"00:1B:66:AA:01:05", "Rode Wireless GO II", "headset"}} {
			st.Devices = append(st.Devices, ui.BTDevice{Address: n.addr, Name: n.name, Kind: n.kind, Uses: []string{"listen"},
				RSSI: rssi(-80)})
		}
	}
}

// btPreview sets what a variant needs on the screen itself (a list's page, a device's view, a confirmation).
func btPreview(p ui.Preview, variant string) {
	switch variant {
	case "settings-bluetooth-page2":
		p.BTListPage(1)
	case "settings-bluetooth-device":
		p.OpenBTDevice(btHeadphones)
	case "settings-bluetooth-forget":
		p.OpenBTDevice(btHeadphones)
		p.Confirming("bt_forget:" + btHeadphones)
	}
}
