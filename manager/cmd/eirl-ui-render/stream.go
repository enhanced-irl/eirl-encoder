package main

import (
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The FAKE app state of MIXER.md §21's pages of the Stream group, EnhancedIRL and Device → Name & password:
//
//	settings-dest · settings-dest-reveal · settings-dest-edited · settings-dest-managed · settings-dest-pending ·
//	settings-dest-error · settings-uplinks · settings-uplinks-single · settings-uplinks-none · settings-uplinks-health ·
//	settings-uplinks-modems · settings-uplinks-modem-page · settings-uplinks-modem-pin · settings-uplinks-modem-unlock · settings-pause ·
//	settings-cloud · settings-cloud-pairing · settings-cloud-linked · settings-cloud-unlink · settings-account ·
//	settings-account-kb
func streamState(variant, lang string, now time.Time, snap *ui.AppSnapshot) {
	snap.Dest, snap.DestAPI = &ui.DestInfo{ManagedBy: "local", URL: "srtla://eu1.relay.enhancedirl.com:5000",
		Mode: "caller", LatencyMs: 2000, StreamIDSet: true, StreamIDMasked: "publish:a1b2…", PassphraseSet: false,
		VideoKbps: 6000, AudioKbps: 128, ABR: true, VideoMinKbps: 1500, Transport: "srtla", Output: "on"}, ui.Yes
	ups := []ui.UplinkInfo{
		{Name: "eth0", IP: "192.168.1.20", Kind: "ethernet", InUse: true, TxKbps: 3150},
		{Name: "usb0", IP: "192.168.8.100", Kind: "usb", InUse: true, TxKbps: 2480},
		{Name: "wlan0", IP: "10.12.0.33", Kind: "wifi", NoInternet: true},
		{Name: "usb1", IP: "192.168.8.100", Kind: "usb", Conflict: true},
	}
	if snap.Status == nil {
		snap.Status, snap.StatusAPI = &ui.StatusInfo{Output: "on"}, ui.Yes
	}
	snap.Status.Transport, snap.Status.HasUplinks, snap.Status.Uplinks = "srtla", true, ups // the input's stays
	exp := now.Add(9 * time.Minute)
	snap.Cloud, snap.CloudAPI = &ui.CloudInfo{State: "unlinked"}, ui.Yes
	switch {
	case variant == "settings-dest-reveal":
		snap.DestSecrets = &ui.DestSecrets{StreamID: "publish:a1b2c3d4e5f6EjemploNoEsLaReal"}
	case variant == "settings-dest-managed":
		snap.Dest.ManagedBy = "eirl"
	case variant == "settings-dest-pending":
		snap.Dest.PendingRestart = true
	case variant == "settings-dest-error":
		snap.Last = &ui.AppResult{What: "dest_save", Status: 400, Code: "validation", At: now.Add(time.Second),
			Fields: map[string]ui.AppFieldError{"url": {Message: map[string]string{
				"es": "falta el puerto: srt://host:puerto", "en": "the port is missing: srt://host:port",
				"fr": "le port manque : srt://hôte:port"}[lang]}}}
	case variant == "settings-uplinks-single":
		snap.Status.Uplinks = ups[:1]
	case strings.HasPrefix(variant, "settings-uplinks-modem"): // modems: their rows, a modem's page (MODEMS.md §7)
		snap.Status.Uplinks, snap.Status.Modems, snap.Status.Links = fakeModems()
	case variant == "settings-uplinks-health": // an app with modems and the health of each link (MODEMS.md §5)
		snap.Status.Uplinks = []ui.UplinkInfo{
			{Name: "eth0", IP: "192.168.1.20", Kind: "ethernet", InUse: true, TxKbps: 2100, Health: "good", HealthCode: "ok"},
			{Name: "wlan0", IP: "10.12.0.33", Kind: "wifi", InUse: true, TxKbps: 600, Health: "weak", HealthCode: "signal_weak"},
			{Name: "wwan0", IP: "10.64.12.7", Kind: "modem", Label: "Movistar", InUse: true, TxKbps: 2400, Health: "good", HealthCode: "ok"},
			{Name: "wwan1", IP: "10.70.3.9", Kind: "modem", Label: "Vodafone", InUse: true, TxKbps: 700, Health: "weak", HealthCode: "signal_weak"},
			{Name: "enx0c5b8f279a64", IP: "192.168.9.100", Kind: "modem", Label: "Orange", InUse: true, Health: "bad", HealthCode: "signal_minimal"},
		}
	case variant == "settings-uplinks-none":
		snap.Status.Uplinks = nil
	case strings.HasPrefix(variant, "settings-cloud-pairing"):
		snap.Cloud = &ui.CloudInfo{State: "pairing", Code: "K7QM-2XVD", ExpiresAt: &exp,
			LinkURL: "https://app.enhancedirl.com/encoders/link?pairing=K7QM-2XVD", LinkQR: fakeQR}
	case variant == "settings-cloud-linked", variant == "settings-cloud-unlink":
		on := true
		snap.Cloud = &ui.CloudInfo{State: "linked", AccountLabel: "estudio@example.com", Online: &on}
	}
}

// streamPreview sets what a variant needs on the screen itself (an eye open, a draft, a confirmation, a keyboard).
func streamPreview(p ui.Preview, variant string) {
	switch variant {
	case "settings-dest-reveal":
		p.Reveal("s_dest_sid_eye")
	case "settings-dest-edited":
		p.Touch("s_dest_lat_p")
		p.Touch("s_dest_abr")
	case "settings-cloud-unlink":
		p.Confirming("cloud_unlink")
	case "settings-account-kb":
		p.Touch("s_acct_pw")
	case "settings-uplinks-modem-page":
		p.OpenModem("m-0c7e44a19f02")
	case "settings-uplinks-modem-pin":
		p.OpenModem("m-7d3e2a1b9c04")
	case "settings-uplinks-modem-unlock":
		p.OpenModem("m-7d3e2a1b9c04")
		p.Touch("s_mdm_pin")
	}
}

// fakeModems: three modems on air and one whose SIM asks for its PIN.
func fakeModems() ([]ui.UplinkInfo, []ui.ModemInfo, []ui.LinkDot) {
	f := func(v float64) *float64 { return &v }
	all := []string{"apn", "roaming", "mode", "pin", "reset"}
	ups := []ui.UplinkInfo{
		{Name: "eth0", IP: "192.168.1.20", Kind: "ethernet", InUse: true, TxKbps: 2100, Health: "good", HealthCode: "ok"},
		{Name: "wwan0", IP: "10.64.12.7", Kind: "modem", ModemID: "m-5a1d0c3e9b21", Label: "Movistar", InUse: true, TxKbps: 2400, Health: "good", HealthCode: "ok"},
		{Name: "wwan1", IP: "10.70.3.9", Kind: "modem", ModemID: "m-0c7e44a19f02", Label: "Vodafone", InUse: true, TxKbps: 700, Health: "weak", HealthCode: "signal_weak"},
		{Name: "enx0c5b8f279a64", IP: "192.168.9.100", Kind: "modem", ModemID: "h-9b2c41d07e5a", Label: "Orange", InUse: true, Health: "bad", HealthCode: "signal_minimal"},
	}
	mods := []ui.ModemInfo{
		{ID: "m-5a1d0c3e9b21", Iface: "wwan0", Manufacturer: "Quectel", Model: "RM520N-GL", State: "connected", SIM: "ready",
			Registration: "home", Operator: "Movistar", Tech: "5g-nsa", RSRP: f(-88), RSRQ: f(-9), SINR: f(14), IMEITail: "4412",
			ICCIDTail: "0193", APNAuto: "telefonica.es", Mode: "auto", Modes: []string{"auto", "4g", "5g"}, Can: all},
		{ID: "m-0c7e44a19f02", Iface: "wwan1", Manufacturer: "Quectel", Model: "EG25", State: "connected", SIM: "ready",
			Registration: "home", Operator: "Vodafone", Tech: "4g", RSRP: f(-106), RSRQ: f(-13), SINR: f(3.2), IMEITail: "5531",
			ICCIDTail: "4821", APNAuto: "airtelwap.es", Mode: "auto", Modes: []string{"auto", "4g"}, Can: all},
		{ID: "h-9b2c41d07e5a", Iface: "enx0c5b8f279a64", Manufacturer: "Huawei", Model: "HiLink", State: "connected", SIM: "ready",
			Registration: "home", Operator: "Orange", Tech: "4g", RSRP: f(-117), RSRQ: f(-16), SINR: f(-1), Mode: "auto",
			Modes: []string{"auto", "4g"}, Can: append(all, "subnet")},
		{ID: "m-7d3e2a1b9c04", Backend: "mm", Manufacturer: "Quectel", Model: "EC25", State: "locked", SIM: "pin", IMEITail: "9907",
			Mode: "auto", Modes: []string{"auto", "4g"}, Can: all},
	}
	links := []ui.LinkDot{{Name: "m-7d3e2a1b9c04", Kind: "modem", Health: "bad", Code: "sim_pin"}}
	return ups, mods, links
}
