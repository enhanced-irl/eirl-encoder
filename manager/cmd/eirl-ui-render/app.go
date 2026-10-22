package main

import (
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The app's device API, FAKE, for the Settings pages and the wizard (CONTRACT §1-§5): a device, the setup done,
// an update, the time zones, the slots and the link. Variants:
//
//	settings-wifi · settings-cams · settings-networks · settings-keyboard · settings-language · settings-time ·
//	settings-time-manual · settings-zones · settings-updates · settings-updates-available ·
//	settings-updates-downloading · settings-updates-confirm · settings-updates-live · settings-updates-rolledback ·
//	settings-updates-error · settings-updates-blocked · settings-updates-undone · settings-updates-installed ·
//	settings-updates-testbuild · settings-updates-restart (the system layer waits for a restart) ·
//	settings-updates-restart-confirm · settings-updates-restart-live · settings-updates-restart-notes (with a release
//	available, its download waiting for Wi-Fi and the system layer failed: everything at once) · settings-updates-metered
//	(the download waits for Wi-Fi or a cable) · settings-updates-sysfailed · settings-updates-sysskipped ·
//	settings-about · settings-old (an app without the device API) · settings-sources · settings-sources-edited ·
//	settings-sources-pick · settings-sources-alias · settings-sources-onair · settings-sources-dup ·
//	settings-sources-oldengine · settings-sources-hidden (slot 2 hidden; hiding the one on air refused) ·
//	settings-input (the board's HDMI, another EDID in use) · settings-input-usb (a USB card saved, in use) ·
//	settings-input-auto (the first USB there is) · settings-input-test · settings-input-gone (a saved USB unplugged,
//	the test pattern's audio card gone) · settings-input-warn (a USB 2 card with warnings) · settings-input-edited ·
//	settings-input-pick · settings-input-modes · settings-input-audio · settings-input-confirm (streaming: «Save and
//	apply» waiting for its second tap) · settings-input-error (the app's field errors) · settings-input-saved ·
//	settings-input-loading · settings-input-old · wizard-1-language · wizard-2-time · wizard-2-zones · wizard-3-network ·
//	wizard-3-networks · wizard-3-network-ssid-kb · wizard-4-password · wizard-4-password-kb · wizard-4-password-again · wizard-4-password-set ·
//	wizard-5-name · wizard-5-name-kb · wizard-6-cloud · wizard-6-cloud-pairing · wizard-6-cloud-linked · wizard-7-done

// fakeZones is a part of the tz database, enough to page through.
var fakeZones = func() []ui.TimeZone {
	var out []ui.TimeZone
	add := func(off string, ids ...string) {
		for _, id := range ids {
			out = append(out, ui.TimeZone{ID: id, Offset: off})
		}
	}
	add("+00:00", "Africa/Abidjan", "Africa/Accra", "Africa/Dakar")
	add("+01:00", "Africa/Algiers", "Africa/Lagos", "Africa/Casablanca")
	add("+02:00", "Africa/Cairo", "Africa/Johannesburg")
	add("+03:00", "Africa/Nairobi")
	add("-03:00", "America/Argentina/Buenos_Aires", "America/Sao_Paulo", "America/Montevideo")
	add("-05:00", "America/Bogota", "America/Lima", "America/New_York", "America/Toronto", "America/Panama")
	add("-06:00", "America/Mexico_City", "America/Chicago", "America/Guatemala", "America/Costa_Rica")
	add("-04:00", "America/Caracas", "America/Santiago", "America/La_Paz", "America/Halifax", "America/Santo_Domingo")
	add("-07:00", "America/Denver", "America/Phoenix")
	add("-08:00", "America/Los_Angeles", "America/Tijuana", "America/Vancouver")
	add("+08:00", "Asia/Shanghai", "Asia/Singapore", "Asia/Manila")
	add("+09:00", "Asia/Tokyo", "Asia/Seoul")
	add("+05:30", "Asia/Kolkata")
	add("+04:00", "Asia/Dubai")
	add("-01:00", "Atlantic/Azores")
	add("+00:00", "Atlantic/Canary", "Atlantic/Reykjavik")
	add("+10:00", "Australia/Sydney", "Australia/Brisbane")
	add("+00:00", "Europe/Dublin", "Europe/Lisbon", "Europe/London")
	add("+01:00", "Europe/Amsterdam", "Europe/Andorra", "Europe/Belgrade", "Europe/Berlin", "Europe/Brussels",
		"Europe/Budapest", "Europe/Copenhagen", "Europe/Gibraltar", "Europe/Luxembourg", "Europe/Madrid", "Europe/Malta",
		"Europe/Monaco", "Europe/Oslo", "Europe/Paris", "Europe/Prague", "Europe/Rome", "Europe/Stockholm",
		"Europe/Vienna", "Europe/Warsaw", "Europe/Zurich")
	add("+02:00", "Europe/Athens", "Europe/Bucharest", "Europe/Helsinki", "Europe/Kyiv", "Europe/Riga")
	add("+03:00", "Europe/Istanbul", "Europe/Moscow")
	add("+12:00", "Pacific/Auckland", "Pacific/Fiji")
	add("+00:00", "UTC")
	// The offsets as on 14 October (summer time in the north)
	for i, z := range out {
		switch {
		case strings.HasPrefix(z.ID, "Europe/") && z.Offset == "+01:00":
			out[i].Offset = "+02:00"
		case strings.HasPrefix(z.ID, "Europe/") && z.Offset == "+00:00":
			out[i].Offset = "+01:00"
		}
	}
	return out
}()

// fakeNotes are the release notes of the fake update, by language.
var fakeNotes = map[string]string{
	"es": "Ajustes con una lista de páginas en la pantalla.\nLa pantalla y la web en español, inglés y francés.\nCada fuente con su alias.",
	"en": "Settings as a list of pages on the screen.\nThe screen and the web page in Spanish, English and French.\nAn alias for each source.",
	"fr": "Réglages sous forme de liste de pages à l'écran.\nL'écran et la page web en espagnol, anglais et français.\nUn alias pour chaque source.",
}

// The app's words for the system layer and a download that waits (its i18n catalog, update.*), by language.
var (
	fakeMetered = map[string]string{
		"es": "la actualización se descargará sola cuando haya wifi o cable: búscala para descargarla ya por datos móviles",
		"en": "the update will download by itself on Wi-Fi or a cable: check for it to download it now over mobile data",
		"fr": "la mise à jour se téléchargera d’elle-même en Wi-Fi ou par câble : recherchez-la pour la télécharger maintenant en données mobiles"}
	fakeSysFailed = map[string]string{
		"es": "la parte del sistema de la última actualización no se pudo instalar: sigue la anterior",
		"en": "the system part of the last update could not be installed: the previous one stays",
		"fr": "la partie système de la dernière mise à jour n’a pas pu être installée : la précédente reste en place"}
)

// fakeRestart is a system layer installed whose HDMI input and modems wait for a restart.
func fakeRestart(lang string) *ui.UpdateSystem {
	return &ui.UpdateSystem{State: "ok", RestartRequired: true, Pending: []string{"hdmi", "uplinks"},
		RestartMessage: map[string]string{
			"es": "reinicia el codificador para terminar la actualización (entrada HDMI, módems e iPhone)",
			"en": "restart the encoder to finish the update (HDMI input, modems and iPhone)",
			"fr": "redémarrez l’encodeur pour terminer la mise à jour (entrée HDMI, modems et iPhone)"}[lang]}
}

// appState is the fake device API for a variant, at the screen's clock now.
func appState(variant, lang string, now time.Time) ui.AppSnapshot {
	loc := time.FixedZone("CEST", 2*3600)
	dev := &ui.DeviceInfo{Language: lang, Languages: []string{"es", "en", "fr"}, Name: "Estudio 2",
		Hostname: "estudio-2", Timezone: "Europe/Madrid", Now: time.Date(2026, 10, 14, 10, 42, 0, 0, loc), At: now,
		NTP: true, Synced: true, Release: "0.1.0", AppVer: "0.1.0", EngineVer: "0.1.0"}
	check := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	snap := ui.AppSnapshot{Device: dev, DeviceAPI: ui.Yes,
		Setup: &ui.SetupInfo{Done: true}, SetupAPI: ui.Yes,
		Update: &ui.UpdateInfo{Current: "0.1.0", State: "idle", LastCheck: &check}, UpdateAPI: ui.Yes,
		Zones: fakeZones, Commit: "0123abcd", IPs: []string{"192.168.1.20", "10.42.0.1"}}
	avail := &ui.UpdateRelease{Version: "0.2.0", Notes: fakeNotes[lang], Size: 23_400_000}
	snap.Slots, snap.SlotsAPI = fakeSlots(), ui.Yes
	snap.Status, snap.StatusAPI = fakeStatus(), ui.Yes
	snap.Sources, snap.SourcesAPI = fakeSources(lang), ui.Yes
	inputVariant(variant, lang, now, &snap)
	streamState(variant, lang, now, &snap)
	last := func(code, msg string, fields map[string]ui.AppFieldError) {
		snap.Last = &ui.AppResult{What: "slots", Status: 409, Code: code, Message: msg, Fields: fields, At: now}
	}
	switch variant {
	case "settings-sources-onair":
		last("on_air", map[string]string{"es": "Deja de emitir para mover la fuente que está al aire",
			"en": "Stop streaming to move the source on air", "fr": "Arrêtez la diffusion pour déplacer la source à l'antenne"}[lang], nil)
	case "settings-sources-dup":
		last("validation", "", map[string]ui.AppFieldError{"slots.4.assign": {Code: "duplicate",
			Message: map[string]string{"es": "esa fuente ya está fijada en la fuente 2", "en": "that source is already pinned to source 2",
				"fr": "cette source est déjà fixée sur la source 2"}[lang]}})
		snap.Last.Status = 400
	case "settings-sources-oldengine":
		snap.Slots.EngineSupports = false
	case "settings-time-manual":
		dev.NTP, dev.Synced = false, false
	case "settings-updates-available", "settings-updates-confirm", "settings-updates-live":
		snap.Update.State, snap.Update.Available = "available", avail
	case "settings-updates-downloading":
		snap.Update.State, snap.Update.Available, snap.Update.Progress = "downloading", avail, 0.45
	case "settings-updates-rolledback":
		snap.Update.State = "rolled_back"
	case "settings-updates-error": // the app's words (its i18n catalog), not the code
		snap.Update.State, snap.Update.Error, snap.Update.Message = "error", "network", map[string]string{
			"es": "no se puede contactar con el servidor de actualizaciones", "en": "the update server cannot be reached",
			"fr": "le serveur de mises à jour est injoignable"}[lang]
	case "settings-updates-blocked":
		no := false
		snap.Update.State, snap.Update.Available, snap.Update.CanInstall, snap.Update.Blocked = "ready", avail, &no, "streaming"
		snap.Update.BlockedMessage = map[string]string{"es": "deja de emitir para actualizar el codificador",
			"en": "stop streaming to update the encoder", "fr": "arrêtez la diffusion pour mettre à jour l’encodeur"}[lang]
	case "settings-updates-undone":
		snap.Update.State = "rolled_back"
		snap.Update.LastResult = &ui.UpdateRun{State: "rolled_back", From: "0.1.0", To: "0.2.0", Error: "unhealthy",
			Message: map[string]string{"es": "la versión nueva no arrancó bien: se ha vuelto a la anterior",
				"en": "the new version did not start correctly: the previous one was restored",
				"fr": "la nouvelle version n’a pas démarré correctement : la précédente a été restaurée"}[lang]}
	case "settings-updates-installed":
		snap.Update.Current = "0.2.0"
		snap.Update.LastResult = &ui.UpdateRun{State: "installed", From: "0.1.0", To: "0.2.0"}
	case "settings-updates-testbuild":
		snap.Update.TestBuild = true
	case "settings-updates-restart", "settings-updates-restart-confirm", "settings-updates-restart-live":
		snap.Update.Current = "0.2.0"
		snap.Update.LastResult = &ui.UpdateRun{State: "installed", From: "0.1.0", To: "0.2.0"}
		snap.Update.System = fakeRestart(lang)
	case "settings-updates-restart-notes":
		snap.Update.State, snap.Update.Available = "available", avail
		snap.Update.DownloadWaits, snap.Update.DownloadWaitsMessage = "metered", fakeMetered[lang]
		snap.Update.System = fakeRestart(lang)
		snap.Update.System.State, snap.Update.System.Error, snap.Update.System.Message = "failed", "apt", fakeSysFailed[lang]
	case "settings-updates-metered":
		snap.Update.State, snap.Update.Available = "available", avail
		snap.Update.DownloadWaits, snap.Update.DownloadWaitsMessage = "metered", fakeMetered[lang]
	case "settings-updates-sysfailed":
		snap.Update.System = &ui.UpdateSystem{State: "failed", Error: "apt", Message: fakeSysFailed[lang]}
	case "settings-updates-sysskipped":
		snap.Update.System = &ui.UpdateSystem{State: "skipped", Message: map[string]string{
			"es": "este aparato no usa el sistema del codificador: solo se actualiza su software",
			"en": "this device does not run the encoder's system: only its software is updated",
			"fr": "cet appareil n’utilise pas le système de l’encodeur : seul son logiciel est mis à jour"}[lang]}
	case "settings-old":
		return ui.AppSnapshot{DeviceAPI: ui.No, SetupAPI: ui.No, UpdateAPI: ui.No, SlotsAPI: ui.No}
	}
	return snap
}

// settingsPage is the page a settings-… variant opens, and its view.
func settingsPage(variant string) (page, view string) {
	rest, ok := strings.CutPrefix(variant, "settings-")
	if !ok {
		return "", ""
	}
	switch rest {
	case "networks", "keyboard":
		return "wifi", rest
	case "zones":
		return "time", "zones"
	case "old":
		return "updates", ""
	}
	page, _, _ = strings.Cut(rest, "-")
	if page == "dests" { // the saved destinations: the Destination page with the app's list
		page = "dest"
	}
	return page, ""
}

// fakeSlots are the fixture's sources as slots: HDMI with an alias, the two USB, and a Cam Link pinned to slot 4
// but unplugged.
func fakeSlots() *ui.SlotsInfo {
	usb1 := "/dev/v4l/by-path/platform-70090000.xusb-usb-0:2.4:1.0-video-index0"
	logi := "/dev/v4l/by-id/usb-046d_Logi_4K_Stream_Edition_0000ABCD-video-index0"
	camlink := "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0000EFGH-video-index0"
	return &ui.SlotsInfo{EngineSupports: true,
		Slots: []ui.SlotInfo{
			{Slot: 1, Assign: "hdmi", Alias: "Cámara principal", Current: &ui.SlotSource{ID: "hdmi", Name: "HDMI de la placa", Kind: "hdmi-csi", State: "live"}},
			{Slot: 2, Assign: "auto", Current: &ui.SlotSource{ID: usb1, Name: "USB3. 0 capture", Kind: "usb", State: "live"}},
			{Slot: 3, Assign: "auto", Alias: "Plano general", Current: &ui.SlotSource{ID: logi, Name: "Logi 4K Stream Edition", Kind: "usb", State: "live"}},
			{Slot: 4, Assign: camlink}},
		Candidates: []ui.SlotCandidate{{ID: "hdmi", Name: "HDMI de la placa", Kind: "hdmi-csi", Connected: true},
			{ID: usb1, Name: "USB3. 0 capture", Kind: "usb", Connected: true},
			{ID: logi, Name: "Logi 4K Stream Edition", Kind: "usb", Connected: true},
			{ID: "net:cam1", Name: "Móvil de Ana", Kind: "network", Connected: true},
			{ID: camlink, Name: "Cam Link 4K", Kind: "usb", Connected: false}}}
}

// wizardStep is the step a wizard-N-… variant shows (0-based), or -1.
func wizardStep(variant string) int {
	rest, ok := strings.CutPrefix(variant, "wizard-")
	if !ok || rest == "" || rest[0] < '1' || rest[0] > '7' {
		return -1
	}
	return int(rest[0] - '1')
}

// wizardState is the fake setup for a wizard variant: the steps before it done (the network one skipped), the
// password still to set until step 4 is past, a setup code, and the link as the variant says.
func wizardState(variant string, snap *ui.AppSnapshot) {
	step := wizardStep(variant)
	setup := &ui.SetupInfo{NeedsPassword: true, Code: "ABCD2345"}
	for i, id := range ui.WizardSteps[:len(ui.WizardSteps)-1] {
		st := "todo"
		if i < step {
			st = "done"
			if id == "network" || id == "cloud" {
				st = "skipped"
			}
		}
		setup.Steps = append(setup.Steps, ui.SetupStep{ID: id, State: st})
	}
	if step > 3 || strings.HasSuffix(variant, "-set") {
		setup.NeedsPassword, setup.Code = false, ""
	}
	snap.Setup = setup
	snap.Cloud, snap.CloudAPI = &ui.CloudInfo{State: "unlinked"}, ui.Yes
	switch {
	case strings.HasSuffix(variant, "-pairing"):
		exp := time.Date(2026, 10, 14, 8, 52, 0, 0, time.UTC)
		link := "https://app.enhancedirl.com/encoders/link?pairing=K7QM-2XVD"
		snap.Cloud = &ui.CloudInfo{State: "pairing", Code: "K7QM-2XVD", ExpiresAt: &exp, LinkURL: link, LinkQR: qrRows(link)}
	case strings.HasSuffix(variant, "-linked"):
		snap.Cloud = &ui.CloudInfo{State: "linked", AccountLabel: "estudio@example.com"}
	}
}

// qrRows is a text's QR in rows of 0 and 1.
func qrRows(text string) []string {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil
	}
	rows := make([]string, c.Size)
	for y := range rows {
		b := make([]byte, c.Size)
		for x := range b {
			b[x] = '0'
			if c.Black(x, y) {
				b[x] = '1'
			}
		}
		rows[y] = string(b)
	}
	return rows
}

// Fake devices of the Input page: the fixture's two USB cards and a Cam Link 4K.
const (
	fakeUSB1    = "/dev/v4l/by-path/platform-70090000.xusb-usb-0:2.4:1.0-video-index0"
	fakeLogi    = "/dev/v4l/by-id/usb-046d_Logi_4K_Stream_Edition_0000ABCD-video-index0"
	fakeCamLink = "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0000EFGH-video-index0"
)

// fakeStatus is GET /status's input: the board's HDMI saved with 1080p30, another EDID running.
func fakeStatus() *ui.StatusInfo {
	return &ui.StatusInfo{Transport: "srt", Output: "off", Input: ui.InputInfo{Source: "hdmi", Device: "auto",
		Format: "auto", Mode: "auto", Audio: "auto", EDIDPreferred: "1080p30", EDIDActive: "1080p50"}}
}

// fakeSources is GET /sources (the app names the HDMI and the test pattern in the device language).
func fakeSources(lang string) *ui.SourcesInfo {
	opt := func(format, mode string, fps float64, ow, oh int, share float64) ui.CaptureOption {
		return ui.CaptureOption{Format: format, Mode: mode, FPS: fps, OutWidth: ow, OutHeight: oh, BusShare: share}
	}
	logi := []ui.CaptureOption{opt("MJPG", "1920x1080@60", 60, 1920, 1080, 0), opt("MJPG", "3840x2160@30", 30, 1920, 1080, 0),
		opt("YUY2", "1920x1080@30", 30, 1920, 1080, 0.62), opt("NV12", "1280x720@60", 60, 1280, 720, 0.55),
		opt("YUY2", "1920x1080@60000/1001", 59.94, 1920, 1080, 0.93), opt("MJPG", "1280x720@60", 60, 1280, 720, 0)}
	usb1 := []ui.CaptureOption{opt("MJPG", "1920x1080@60", 60, 1920, 1080, 0), opt("YUY2", "1920x1080@5", 5, 1920, 1080, 0.31)}
	return &ui.SourcesInfo{
		Sources: []ui.SourceChoice{
			{ID: "hdmi", Kind: "hdmi-csi", Name: map[string]string{"es": "HDMI de la placa", "en": "Board HDMI", "fr": "HDMI de la carte"}[lang]},
			{ID: fakeUSB1, Kind: "usb", Name: "USB3. 0 capture", USB3: false, USBKnown: true, Auto: &usb1[0], Options: usb1,
				AudioName: "USB3. 0 capture", Warnings: []string{"usb2"}},
			{ID: fakeLogi, Kind: "usb", Name: "Logi 4K Stream Edition", USB3: true, USBKnown: true, Auto: &logi[0], Options: logi},
			{ID: "test", Kind: "test", Name: map[string]string{"es": "Patrón de prueba", "en": "Test pattern", "fr": "Mire de test"}[lang]}},
		Audio: []ui.AudioChoice{{ID: "i2s", Kind: "i2s", Name: "HDMI de la placa"},
			{ID: "alsa:capture", Kind: "usb", Name: "USB3. 0 capture"}, {ID: "net:cam1", Kind: "network", Name: "Móvil de Ana"}}}
}

// inputVariant sets the app's state of an Input page's variant.
func inputVariant(variant, lang string, now time.Time, snap *ui.AppSnapshot) {
	in := &snap.Status.Input
	switch variant {
	case "settings-input-usb", "settings-input-modes", "settings-input-confirm":
		*in = ui.InputInfo{Source: "usb", Device: fakeLogi, Format: "YUY2", Mode: "1920x1080@30", Audio: "auto",
			EDIDPreferred: "1080p30", ActiveState: "ready", ActiveName: "Logi 4K Stream Edition", ActiveFormat: "YUY2",
			ActiveMode: "1920x1080@30"}
		if variant == "settings-input-confirm" {
			snap.Status.Output = "on"
		}
	case "settings-input-auto":
		*in = ui.InputInfo{Source: "usb", Device: "auto", Format: "auto", Mode: "auto", Audio: "auto", EDIDPreferred: "1080p30"}
	case "settings-input-test":
		*in = ui.InputInfo{Source: "test", Device: "auto", Format: "auto", Mode: "auto", Audio: "none", EDIDPreferred: "1080p30"}
	case "settings-input-gone":
		*in = ui.InputInfo{Source: "usb", Device: fakeCamLink, Format: "MJPG", Mode: "1920x1080@60", Audio: "alsa:Link4K",
			EDIDPreferred: "1080p30", ActiveState: "waiting_device"}
	case "settings-input-warn", "settings-input-audio":
		*in = ui.InputInfo{Source: "usb", Device: fakeUSB1, Format: "auto", Mode: "auto", Audio: "alsa:capture",
			EDIDPreferred: "1080p30", ActiveState: "ready", ActiveName: "USB3. 0 capture", ActiveFormat: "MJPG",
			ActiveMode: "1920x1080@60"}
	case "settings-input-error":
		snap.Status.Output = "on"
		snap.Last = &ui.AppResult{What: "input_save", Status: 400, Code: "validation", At: now.Add(time.Second),
			Message: map[string]string{"es": "Hay campos con errores", "en": "Some fields are wrong",
				"fr": "Certains champs sont incorrects"}[lang],
			Fields: map[string]ui.AppFieldError{
				"edidPreferred": {Code: "invalid", Message: map[string]string{"es": "debe ser uno de: 1080p30, 1080p50, 720p60",
					"en": "must be one of: 1080p30, 1080p50, 720p60", "fr": "doit être l'un de : 1080p30, 1080p50, 720p60"}[lang]},
				"url": {Code: "invalid", Message: "falta la URL"}}}
	case "settings-input-saved":
		yes := true
		snap.Status.Output = "on"
		snap.Last = &ui.AppResult{What: "input_save", OK: true, Status: 200, Restarted: &yes, At: now.Add(time.Second)}
	case "settings-input-loading":
		snap.Status, snap.StatusAPI, snap.Sources, snap.SourcesAPI = nil, ui.Unknown, nil, ui.Unknown
	case "settings-input-old":
		snap.Status, snap.StatusAPI, snap.Sources, snap.SourcesAPI = nil, ui.No, nil, ui.No
	}
}

// --- the Wi-Fi page's channel and new password, and the Network cameras page (MIXER.md §21) ---
//
//	settings-wifi-eye (the password shown) · settings-wifi-channel (its picker) · settings-wifi-rotate (waiting for
//	the second tap) · settings-wifi-error (the app refused the channel) · settings-cams-none · settings-cams-several
//	(three cameras, by cable too) · settings-cams-many (more than fit: pages) · settings-cams-viaoff (turning the
//	Wi-Fi way off: second tap) · settings-cams-cam (a camera's view) · settings-cams-cam-eye (its URLs shown) ·
//	settings-cams-cam-cable (by cable, its URLs shown) · settings-cams-cam-nocable (by cable without an address) ·
//	settings-cams-cam-remove (waiting for the second tap) · settings-cams-cam-reading (the app asked)

// fakeKey is a camera's FAKE key (only to see the drawing).
const fakeKey = "EjemploNoEsLaClave01"

// fakeCamURLs are a camera's FAKE URLs over an address.
func fakeCamURLs(ip, id string) ui.CamURLs {
	srt := "srt://" + ip + ":8890?streamid=publish:" + id + ":" + id + ":" + fakeKey
	return ui.CamURLs{SRT: srt, RTMP: "rtmp://" + ip + ":1935/" + id + "?user=" + id + "&pass=" + fakeKey,
		RTMPServer: "rtmp://" + ip + ":1935/", RTMPKey: id + "?user=" + id + "&pass=" + fakeKey, SRTQR: qrRows(srt)}
}

// camsFake sets the network cameras and the Wi-Fi's channel of a variant.
func camsFake(variant, lang string, net *ui.NetSnapshot, snap *ui.AppSnapshot, now time.Time) {
	st := net.Status
	if st == nil {
		return
	}
	kb, loss := 4600, 0.3
	st.CamMax = 1
	st.LAN = ui.LANStatus{State: "off", Iface: "eth0", IP: "192.168.1.20"}
	st.CamItems = []ui.NetCamera{{ID: "cam1", Name: "Móvil de Ana", Connected: true, Via: "wifi", Protocol: "srt",
		Codec: "h265", Kbps: &kb, LossPct: &loss}}
	if net.Wifi != nil {
		net.Wifi.Channel = "auto"
	}
	cable := func() {
		st.LANEnabled, st.LAN.State = true, "on"
	}
	switch {
	case variant == "settings-cams-none":
		st.CamItems, st.Cameras = []ui.NetCamera{}, 0
	case variant == "settings-cams-several" || variant == "settings-cams-many":
		cable()
		st.CamMax = 8
		lk := 2900
		st.CamItems = append(st.CamItems,
			ui.NetCamera{ID: "cam2", Name: "Cámara del escenario", Connected: true, Via: "lan", Protocol: "rtmp", Codec: "h264", Kbps: &lk},
			ui.NetCamera{ID: "cam3", Name: "GoPro (casco)"})
		if variant == "settings-cams-many" {
			st.CamItems = append(st.CamItems, ui.NetCamera{ID: "cam4", Name: "Dron"}, ui.NetCamera{ID: "cam5", Name: "Móvil de Leo"})
		}
		st.Cameras = len(st.CamItems)
	case strings.HasPrefix(variant, "settings-cams-cam-cable"), variant == "settings-cams-cam-nocable":
		cable()
		if variant == "settings-cams-cam-nocable" {
			st.LAN.IP, st.LAN.State = "", "error"
		}
	case variant == "settings-wifi-error":
		snap.Last = &ui.AppResult{What: "ap_channel", Status: 409, Code: "on_air", At: now,
			Message: map[string]string{"es": "«Móvil de Ana» entra por esta wifi y está al aire: cambiar el canal la reinicia",
				"en": "“Móvil de Ana” comes in over this Wi-Fi and is on air: changing the channel restarts it",
				"fr": "« Móvil de Ana » arrive par ce Wi-Fi et est à l’antenne : changer le canal le redémarre"}[lang]}
	}
	if strings.HasSuffix(variant, "-eye") || strings.HasSuffix(variant, "-cable") {
		lan := fakeCamURLs("192.168.1.20", "cam1")
		snap.CamCreds = []ui.CamConnect{{ID: "cam1", Wifi: fakeCamURLs("10.42.0.1", "cam1"), LAN: &lan}}
	}
	if variant == "settings-cams-cam-reading" {
		snap.Pending = "cam_creds"
	}
}

// camsPreview puts the page of a variant in its state (after the page is open).
func camsPreview(variant string, p ui.Preview) {
	switch variant {
	case "settings-wifi-eye":
		p.NetReveal("s_wifi_pw_eye")
	case "settings-wifi-channel":
		p.NetChannelPicker()
	case "settings-wifi-rotate":
		p.NetConfirm("ap_rotate")
	case "settings-cams-viaoff":
		p.NetConfirm("via_wifi")
	}
	if !strings.HasPrefix(variant, "settings-cams-cam") {
		return
	}
	p.NetView("cam1")
	switch variant {
	case "settings-cams-cam-eye", "settings-cams-cam-reading":
		p.NetReveal("s_camv_srt_eye")
		p.NetReveal("s_camv_rtmp_eye")
	case "settings-cams-cam-cable", "settings-cams-cam-nocable":
		p.NetWay("lan")
		p.NetReveal("s_camv_srt_eye")
		p.NetReveal("s_camv_rtmp_eye")
	case "settings-cams-cam-remove":
		p.NetConfirm("cam_remove")
	}
}
