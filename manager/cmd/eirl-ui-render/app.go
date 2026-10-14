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
//	settings-updates-testbuild ·
//	settings-about · settings-old (an app without the device API) · settings-sources · settings-sources-edited ·
//	settings-sources-pick · settings-sources-alias · settings-sources-onair · settings-sources-dup ·
//	settings-sources-oldengine · wizard-1-language · wizard-2-time · wizard-2-zones · wizard-3-network ·
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
