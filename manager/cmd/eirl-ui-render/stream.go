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
//	settings-account-kb ·
//	settings-dests-list · settings-dests-templates · settings-dests-confirm · settings-dests-rtmp ·
//	settings-dests-srt · settings-dests-rist · settings-dests-video · settings-dests-managed · settings-dests-edited ·
//	settings-dests-new · settings-dests-delete (the saved destinations, OUTPUTS.md §11)
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
	if strings.HasPrefix(variant, "settings-dests") { // saved destinations (OUTPUTS.md §11)
		snap.Dests, snap.DestsAPI = fakeDests(variant), ui.Yes
	}
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

// fakeDests: three saved destinations (the cloud's over SRTLA, Twitch over RTMP and in use, a RIST one) with the app's
// templates; settings-dests-srt adds a custom SRT one.
func fakeDests(variant string) *ui.DestList {
	set := true
	d := func(id, name, tmpl, proto, url string) ui.DestInfo {
		return ui.DestInfo{ID: id, Name: name, Template: tmpl, Protocol: proto, URL: url, Mode: "caller", LatencyMs: 2000,
			VideoCodec: "h264", VideoKbps: 6000, VideoMinKbps: 1000, ABR: true, AudioKbps: 128, OutputFps: "full",
			ManagedBy: "local"}
	}
	eirl := d("eirl", "EnhancedIRL", "eirl", "srtla", "srtla://eu1.relay.enhancedirl.com:5001")
	eirl.Managed, eirl.StreamIDSet, eirl.StreamIDMasked, eirl.VideoCodec = true, set, "publish:a1b2…", "h265"
	twitch := d("d-3f9a1c2b", "Twitch", "twitch", "rtmp", "rtmp://live.twitch.tv/app")
	twitch.Active, twitch.KeySet, twitch.KeyMasked = true, set, "live…wXyZ"
	rist := d("d-7c21e0a4", "Studio RIST", "custom", "rist", "rist://203.0.113.40:8200")
	rist.LatencyMs, rist.RistProfile, rist.Username, rist.PassphraseSet, rist.PasswordSet = 1000, "main", "estudio", set, set
	rist.VideoCodec, rist.VideoKbps, rist.OutputSize, rist.OutputFps = "h265", 8000, "1280x720", "half"
	list := []ui.DestInfo{eirl, twitch, rist}
	if variant == "settings-dests-srt" {
		srt := d("d-0b9e55d1", "Relay propio", "custom", "srt", "srt://198.51.100.7:9000")
		srt.StreamIDSet, srt.PassphraseSet, srt.LatencyMs = set, set, 1500
		list = append(list, srt)
	}
	return &ui.DestList{Active: twitch.ID, ManagedBy: "local", Destinations: list, Templates: []ui.DestTemplate{
		{ID: "eirl", Protocols: []string{"srtla", "srt"}, Codecs: []string{"h265", "h264"}},
		{ID: "twitch", Protocols: []string{"rtmp", "rtmps"}, URL: "rtmp://live.twitch.tv/app", Codecs: []string{"h264"},
			MaxVideoKbps: 6000, KeyRequired: true},
		{ID: "youtube", Protocols: []string{"rtmps", "rtmp"}, URL: "rtmps://a.rtmps.youtube.com:443/live2",
			Codecs: []string{"h264", "h265"}, MaxVideoKbps: 20000, KeyRequired: true},
		{ID: "kick", Protocols: []string{"rtmps"}, Codecs: []string{"h264"}, MaxVideoKbps: 8000, KeyRequired: true},
		{ID: "facebook", Protocols: []string{"rtmps"}, URL: "rtmps://live-api-s.facebook.com:443/rtmp/",
			Codecs: []string{"h264"}, MaxVideoKbps: 6000, KeyRequired: true},
		{ID: "custom", Protocols: []string{"srt", "srtla", "rtmp", "rtmps", "rist", "udp"}, Codecs: []string{"h264", "h265"},
			MaxVideoKbps: 20000}}}
}

// streamPreview sets what a variant needs on the screen itself (an eye open, a draft, a confirmation, a keyboard).
func streamPreview(p ui.Preview, variant string) {
	switch variant {
	case "settings-dests-templates":
		p.AddDest()
	case "settings-dests-confirm": // a touch on the RIST one while streaming: it waits for the second
		p.Confirming("dl_act:d-7c21e0a4")
	case "settings-dests-rtmp":
		p.OpenDest("d-3f9a1c2b", "conn")
	case "settings-dests-srt":
		p.OpenDest("d-0b9e55d1", "conn")
	case "settings-dests-rist":
		p.OpenDest("d-7c21e0a4", "conn")
	case "settings-dests-video":
		p.OpenDest("d-7c21e0a4", "video")
	case "settings-dests-managed":
		p.OpenDest("eirl", "conn")
	case "settings-dests-edited": // the active one with a change: «Save» and «Save and apply»
		p.OpenDest("d-3f9a1c2b", "video")
		p.Touch("s_de_vid_m")
		p.Touch("s_de_abr")
	case "settings-dests-new":
		p.NewDest("youtube")
	case "settings-dests-delete":
		p.OpenDest("d-7c21e0a4", "conn")
		p.Confirming("de_delete")
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
