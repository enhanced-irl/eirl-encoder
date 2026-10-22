// eirl-ui-render draws the touch screen's interface into PNGs without the engine or the screen, to see a
// change before installing it. It started as the Go equivalent of the former tools/ui_render.py (same
// variants and modifiers), to compare the Go and Python screens side by side (ENGINE_GO.md, M3 gate). The
// video is painted grey; the VU meters, with fixed values; the app's network state and the wifi are FAKE
// (neither the board's network nor its password).
//
//	eirl-ui-render [-state /run/eirl-engine/mixer.json] [-out .] [-fonts dir] [-lang es,en,fr] [-frames dir] [-bench N] variant[+mod…]…
//
// Without -lang the screen is Spanish and the files are ui-<variant>.png (the Python comparison); with it, one
// file per language: <variant>-<lang>.png. testdata/mixer.json is a state to render from without a board.
// -frames paints pictures where the video goes instead of grey (the README's screenshots): program.png in the
// program and tile-1.png … tile-4.png in the tiles, each scaled to its box; a missing one stays grey.
//
// Variants: idle · live · connecting · error · pip · audio · audiomix · menu · locked · off · wifi · wifilive ·
// settings · settingsoff · settingscable · settingsnone · settingsbusy · settingsonair · settingsold · settingsload ·
// settingsapoff · settingsapbusy · settingswrong · settingsnoinet · settingsnets · settingsnets2 · settingsnetsbusy ·
// settingsnetswrong · settingsnetsforget · settingskb · settingskbupper · settingskbsym · settingskbsym2 ·
// settingskbwrong · settingsapoffname · settingsssidkb · settingsssidkbbad · settingsrenaming · settingsrenameonair ·
// settingsrenamed · audioset (the audio view's settings) · audiosetpick (a camera's audio being chosen) · audioseterr (the
// engine refused a delay). Modifiers: +wifi +srtla (2 uplinks) +srtla1 +rtmp +rtmps +rist (the header's protocol) +h265 (the
// output's codec) +tls +clock (the output's reason: tls_failed, clock_not_synced) +dots +dots6 (the connections' dots) +press +presstile +noclient +camstab +noalias +details +phoff +phwait
// +pherr +phmute +phbt (the headphones play over Bluetooth: «BT» and the device's name; BLUETOOTH.md §3.6) +btin (a
// Bluetooth input, a phone sending nothing, in the free strip)
// +gone (audioset: a camera linked to an input that is unplugged) +nophones +ant3 +ant1 +antnoinet +antsearch +anterror (the former tools/ui_render.py's header explained each) +apoff
// («Create my Wi-Fi» off, its name known: e.g. wizard-3-network+apoff). The Settings pages' variants (settings-…) are
// listed in app.go, stream.go and bluetooth.go (settings-bluetooth…: Settings › Wireless › Bluetooth).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg" // -frames may be JPEG
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// fakeQR is ui_render.py's fake QR (random.Random(7), 29×29 with the three corner squares): only its shape.
var fakeQR = []string{
	"11111110111110110001001111111", "10000010110010010000101000001", "10111010000010000110101011101",
	"10111010110011110011101011101", "10111010111111111110001011101", "10000010001010000111001000001",
	"11111110100100011111001111111", "00000000110011100100000000000", "10011110111010001000011110100",
	"01000101100000100000000000100", "01111110001001001010001101110", "10111001000111011100110001101",
	"10001010010011011111110101111", "10010010100100010001111101110", "00111011100010111100101111111",
	"10000001101001000001000000000", "11111000000100000101110100010", "00001110101110001000101001000",
	"11101010010000100110011110100", "10001110011111010110110000000", "01000011010110010101111001001",
	"00000000001101111010001111100", "11111110100001110000000010100", "10000010011011000001100110011",
	"10111010000101011010011100111", "10111010001100101111111010110", "10111010011001110100110010111",
	"10000010011100100000111011011", "11111110100111000001010101111",
}

// fakeNet is the app's network state, fixed (no goroutine, no socket).
type fakeNet struct {
	snap ui.NetSnapshot
	app  *fakeApp
}

func (f *fakeNet) Snapshot() ui.NetSnapshot    { return f.snap }
func (f *fakeNet) SetPanel(bool)               {}
func (f *fakeNet) SetScan(bool)                {}
func (f *fakeNet) SetEnabled(bool) bool        { return true }
func (f *fakeNet) SetAP(bool) bool             { return true }
func (f *fakeNet) SetClient(bool) bool         { return true }
func (f *fakeNet) SetSSID(string) bool         { return true }
func (f *fakeNet) Connect(string, []byte) bool { return true }
func (f *fakeNet) Forget(string) bool          { return true }
func (f *fakeNet) Say(string)                  {}

// App: the fake device API (fakeApp), through the Net like the engine's client.
func (f *fakeNet) App() ui.App { return f.app }

// fakeApp is the app's device API, fixed.
type fakeApp struct{ snap ui.AppSnapshot }

func (a *fakeApp) AppSnapshot() ui.AppSnapshot { return a.snap }
func (a *fakeApp) Watch(ui.AppWatch)           {}
func (a *fakeApp) Do(ui.AppAction) bool        { return true }
func (a *fakeApp) SetupPassword([]byte) bool   { return true }

type levels struct {
	pgm    []float64
	inputs map[string]float64
}

func (l *levels) ProgramPeak(ch int) (float64, bool) {
	if len(l.pgm) == 0 {
		return 0, false
	}
	return l.pgm[min(ch, len(l.pgm)-1)], true
}

func (l *levels) InputPeak(id string) (float64, bool) { v, ok := l.inputs[id]; return v, ok }

func ip(v int) *int   { return &v }
func bp(v bool) *bool { return &v }
func m(v ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(v); i += 2 {
		out[v[i].(string)] = v[i+1]
	}
	return out
}

func main() {
	state := flag.String("state", "/run/eirl-engine/mixer.json", "the engine's state (mixer.json)")
	out := flag.String("out", ".", "where the PNGs go (ui-<variant>.png)")
	fontDir := flag.String("fonts", "", "a directory with the DejaVu fonts (else "+ui.BoardFontDir+")")
	bench := flag.Int("bench", 0, "also time N full drawings of each variant")
	langs := flag.String("lang", "", "languages to draw, comma separated (es,en,fr); empty: Spanish, ui-<variant>.png")
	frames := flag.String("frames", "", "a directory with program.png and tile-N.png to paint where the video goes (else grey)")
	flag.Parse()
	variants := flag.Args()
	if len(variants) == 0 {
		variants = []string{"idle"}
	}
	fonts, err := ui.LoadFonts(*fontDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fonts:", err)
		os.Exit(1)
	}
	raw, err := os.ReadFile(*state)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	codes := []string{""}
	if *langs != "" {
		codes = strings.Split(*langs, ",")
	}
	for _, v := range variants {
		for _, code := range codes {
			if code != "" && !ui.KnownLanguage(code) {
				fmt.Fprintln(os.Stderr, "unknown language:", code)
				os.Exit(2)
			}
			name, dur, err := render(raw, v, code, fonts, *out, *frames, *bench)
			if err != nil {
				fmt.Fprintln(os.Stderr, v+":", err)
				os.Exit(1)
			}
			if *bench > 0 {
				fmt.Printf("ok %s (full drawing: %s)\n", name, dur)
			} else {
				fmt.Println("ok", name)
			}
		}
	}
}

func render(raw []byte, arg, lang string, fonts *ui.Fonts, outDir, frames string, bench int) (string, string, error) {
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		return "", "", err
	}
	parts := strings.Split(arg, "+")
	variant, mods := parts[0], parts[1:]
	has := func(mod string) bool { return slices.Contains(mods, mod) }
	prog := st["program"].(map[string]any)
	sources, _ := st["sources"].([]any)
	if variant == "live" || variant == "pip" || variant == "wifilive" {
		st["output"] = m("mode", "on", "state", "on", "kbps", 5800, "sentKbps", 5800, "rttMs", 57)
	}
	if variant == "connecting" {
		st["output"] = m("mode", "on", "state", "connecting", "reason", "relay_unreachable")
	}
	if variant == "error" {
		st["output"] = m("mode", "on", "state", "error", "reason", "rejected")
	}
	if out, _ := st["output"].(map[string]any); out != nil { // the output's codec and new reasons (OUTPUTS.md §8)
		if has("h265") {
			out["codec"] = "h265"
		}
		if has("tls") {
			out["reason"] = "tls_failed"
		}
		if has("clock") {
			out["reason"] = "clock_not_synced"
		}
	}
	if variant == "pip" {
		prog["layout"] = "pip"
		prog["secondary"] = nil
		for _, s := range sources {
			if id := s.(map[string]any)["id"]; id != prog["main"] {
				prog["secondary"] = id
				break
			}
		}
	}
	audio, _ := st["audio"].(map[string]any)
	audioSet := strings.HasPrefix(variant, "audioset")
	if variant == "audio" || variant == "audiomix" || audioSet {
		if audio == nil {
			audio = map[string]any{}
			st["audio"] = audio
		}
		audio["mode"] = "single"
		if variant == "audiomix" {
			audio["mode"] = "mix"
		}
		as, _ := audio["sources"].([]any)
		for n, a := range as {
			am := a.(map[string]any)
			am["inMix"] = n != 1
			am["gainDb"] = []float64{0, -6, 4, -12}[n%4]
			am["videoSlot"] = n + 1
		}
		if len(as) > 1 {
			as[1].(map[string]any)["delayMs"] = 120
		}
		// The audio settings: camera 2 with the Logi's microphone by hand; with +gone, camera 3 with one unplugged
		if links, _ := audio["links"].(map[string]any); audioSet && links != nil {
			for _, x := range sources {
				switch src := x.(map[string]any); src["slot"] {
				case 2.0:
					links[src["id"].(string)] = "alsa:Logi"
				case 3.0:
					if has("gone") {
						links[src["id"].(string)] = "alsa:USB_Mic"
					}
				}
			}
		}
	}
	if (variant == "audio" || variant == "audiomix" || audioSet) && !has("nophones") {
		ph, _ := audio["phones"].(map[string]any)
		if ph == nil {
			ph = m("on", true, "gainDb", -12, "state", "on")
			audio["phones"] = ph
		}
		if has("phoff") {
			ph["on"], ph["state"] = false, "off"
		}
		if has("phwait") {
			ph["state"], ph["detail"] = "waiting", "La pantalla no admite audio"
		}
		if has("pherr") {
			ph["state"], ph["detail"] = "error", "Device or resource busy"
		}
		if has("phmute") {
			ph["gainDb"] = -40
		}
		if has("phbt") { // the monitor plays over Bluetooth: a name long enough to be shortened
			ph["target"], ph["device"] = "bluetooth", "Auriculares WH-1000XM4"
		}
		if has("btin") { // a phone over Bluetooth as an input, connected and not playing
			as, _ := audio["sources"].([]any)
			audio["sources"] = append(as, m("id", "bt:5CF7E6010203", "kind", "bluetooth", "name", "iPhone de Ana",
				"state", "no_signal", "detail", "el dispositivo Bluetooth no manda audio", "detailCode", "bt_no_audio"))
		}
	} else if has("nophones") && audio != nil {
		delete(audio, "phones")
	}
	if variant == "locked" {
		st["screen"].(map[string]any)["locked"] = true
	}
	settings := strings.HasPrefix(variant, "settings")
	wizard := strings.HasPrefix(variant, "wizard-")
	var net *fakeNet
	var legacy *ui.WifiCreds
	now := time.Now()
	withoutNetwork := func() {
		var keep []any
		for _, s := range sources {
			if s.(map[string]any)["kind"] != "network" {
				keep = append(keep, s)
			}
		}
		sources = keep
		st["sources"] = keep
	}
	if variant == "wifi" || variant == "wifilive" || (settings && variant != "settingsnone") || wizard || has("wifi") {
		// A network camera (a portrait phone) in the first free slot, and the wifi with FAKE data
		used := map[float64]bool{}
		for _, s := range sources {
			used[s.(map[string]any)["slot"].(float64)] = true
		}
		free := 4.0
		for i := 1.0; i <= 4; i++ {
			if !used[i] {
				free = i
				break
			}
		}
		var keep []any
		for _, s := range sources {
			if s.(map[string]any)["slot"].(float64) != free {
				keep = append(keep, s)
			}
		}
		sources = append(keep, m("id", "net:cam1", "slot", free, "kind", "network", "name", "Móvil de Ana", "state", "live",
			"detail", "", "format", "H265", "mode", "1080x1920@30", "fps", 30.0))
		st["sources"] = sources
		wifi := &ui.WifiCreds{SSID: "EnhancedIRL-0000", Password: "Ejempl0lI1NoEsLaReal", QR: fakeQR}
		status := &ui.NetStatus{Enabled: true, Cameras: 1,
			AccessPoint: ui.AccessPoint{Enabled: bp(true), Policy: "always", State: "on", SSID: wifi.SSID, Channel: ip(6),
				Clients: ip(1)}}
		var client *ui.WifiClient
		if !has("noclient") {
			client = &ui.WifiClient{Enabled: true, State: "connected", SSID: "Bar Ejemplo", Signal: ip(72), Band: "5",
				Internet: bp(true)}
		}
		net = &fakeNet{snap: ui.NetSnapshot{API: ui.Yes, Status: status, Wifi: wifi, Client: client}}
		if variant == "settingsapoff" || variant == "settingsapbusy" || variant == "settingsapoffname" || has("apoff") {
			status.AccessPoint = ui.AccessPoint{Enabled: bp(true), Policy: "rescue", State: "off", Clients: ip(0)}
			if variant == "settingsapoff" || variant == "settingsapbusy" {
				net.snap.Wifi = nil // an app without the name from the first start
			}
		}
		if variant == "settingswrong" && client != nil {
			client.State, client.Error, client.Signal, client.Band = "error", "wrong_password", nil, ""
		}
		if variant == "settingsnoinet" && client != nil {
			client.Internet, client.Band, client.Signal = bp(false), "2.4", ip(40)
		}
		if strings.HasPrefix(variant, "settingsnets") || strings.HasPrefix(variant, "settingskb") ||
			variant == "settings-networks" || variant == "settings-keyboard" || variant == "wizard-3-networks" {
			mk := func(ssid string, sig int, sec, band string, known, conn bool) ui.ScanNet {
				return ui.ScanNet{SSID: ssid, Signal: sig, Security: sec, Band: band, Known: known, Connected: conn}
			}
			net.snap.Scan = []ui.ScanNet{mk("Bar Ejemplo", 72, "wpa2", "5", true, true),
				mk("Hotspot del móvil", 58, "wpa2", "2.4", true, false), mk("Red de Ejemplo 5G", 81, "wpa2", "5", false, false),
				mk("Wifi Gratis Plaza", 64, "open", "2.4", false, false), mk("Oficina Ejemplo", 47, "enterprise", "5", false, false),
				mk("Casa_Ejemplo_2G", 33, "wpa3", "2.4", false, false), mk("Vecino 1234", 22, "wpa", "2.4", false, false),
				mk("Impresora-Ejemplo", 15, "wpa2", "2.4", false, false)}
			if variant == "settingsnetsbusy" {
				net.snap.Scan, net.snap.ScanBusy = nil, true
			}
			if variant == "settingsnetswrong" {
				client.State, client.Error, client.SSID = "error", "wrong_password", "Red de Ejemplo 5G"
				net.snap.Scan[0].Connected = false
			}
		}
		if variant == "settingsoff" {
			status.Enabled = false
			withoutNetwork()
		}
		if variant == "settingscable" {
			status.AccessPoint = ui.AccessPoint{Enabled: bp(false), Policy: "rescue", State: "off", Clients: ip(0)}
			status.LANEnabled = true
			net.snap.Wifi = nil
		}
		if variant == "settingsold" {
			net = &fakeNet{snap: ui.NetSnapshot{API: ui.No}}
			w := *wifi
			w.On = true
			legacy = &w
		}
		if variant == "settingsload" {
			net = &fakeNet{snap: ui.NetSnapshot{API: ui.Unknown}}
		}
	} else if variant == "settingsnone" {
		withoutNetwork()
		net = &fakeNet{snap: ui.NetSnapshot{API: ui.Yes, Status: &ui.NetStatus{Enabled: true,
			AccessPoint: ui.AccessPoint{Enabled: bp(true), Policy: "rescue", State: "off"}}}}
	}
	if net == nil {
		net = &fakeNet{}
	}
	tx := ui.Spanish
	if lang != "" {
		tx = ui.Lang(lang)
	}
	app := &fakeApp{snap: ui.AppSnapshot{DeviceAPI: ui.Yes, Device: &ui.DeviceInfo{Language: tx.Code}}}
	if strings.HasPrefix(variant, "settings-") || strings.HasPrefix(variant, "wizard-") {
		app.snap = appState(variant, tx.Code, now)
		if strings.HasPrefix(variant, "wizard-") {
			wizardState(variant, &app.snap)
		}
		if variant == "settings-old" {
			app.snap.Device = &ui.DeviceInfo{Language: tx.Code} // only so the language stays
		}
		btFake(variant, &app.snap)
	}
	if has("noalias") && app.snap.Slots != nil {
		for i := range app.snap.Slots.Slots {
			app.snap.Slots.Slots[i].Alias = ""
		}
	}
	if variant == "settings-updates-live" || variant == "settings-updates-restart-live" {
		st["output"] = m("mode", "on", "state", "on", "kbps", 5800, "sentKbps", 5800, "rttMs", 57)
	}
	net.app = app
	if strings.HasPrefix(variant, "settings") {
		camsFake(variant, tx.Code, &net.snap, &app.snap, now)
	}
	if has("details") { // sources and audio inputs that are not live, with the engine's detail codes (ENGINE_GO.md §13)
		set := func(x map[string]any, state, det, code string, args map[string]any) {
			x["state"], x["detail"], x["detailCode"] = state, det, code
			if args != nil {
				x["detailArgs"] = args
			}
		}
		codes := []struct {
			state, det, code string
			args             map[string]any
		}{{"no_signal", "sin señal HDMI", "hdmi_no_signal", nil},
			{"error", "colgada: reiniciarla apagaría HDMI de la placa (al aire)", "hung_on_air", m("video", "hdmi", "audio", "i2s")},
			{"gone", "desconectada", "gone", nil},
			{"no_signal", "va 2.4 s atrasada", "net_behind", m("seconds", "2.4")}}
		for i, x := range sources {
			c := codes[min(i, len(codes)-1)]
			if x.(map[string]any)["kind"] == "network" {
				c = codes[3]
			}
			set(x.(map[string]any), c.state, c.det, c.code, c.args)
		}
		if audio != nil {
			acodes := []struct {
				state, det, code string
				args             map[string]any
			}{{"no_signal", "sin audio HDMI", "hdmi_no_audio", nil}, {"gone", "desconectada", "gone", nil},
				{"error", "sin muestras 3.0 s", "no_samples", m("seconds", "3.0")}}
			as, _ := audio["sources"].([]any)
			for i, a := range as {
				c := acodes[min(i, len(acodes)-1)]
				set(a.(map[string]any), c.state, c.det, c.code, c.args)
			}
			if ph, _ := audio["phones"].(map[string]any); ph != nil {
				ph["state"], ph["detail"], ph["detailCode"] = "waiting", "La placa no tiene salida de audio HDMI", "phones_no_device"
			}
		}
	}
	if variant == "settings-sources-hidden" { // slot 2's source hidden (the one on air cannot be)
		for _, x := range sources {
			if x.(map[string]any)["slot"].(float64) == 2 {
				x.(map[string]any)["state"] = "hidden"
			}
		}
	}
	if variant == "settings-input-confirm" || variant == "settings-input-error" || variant == "settings-input-saved" {
		st["output"] = m("mode", "on", "state", "on", "kbps", 5800, "sentKbps", 5800, "rttMs", 57)
	}
	if has("noalias") { // the engine's names (translated on the screen) instead of the fixture's aliases
		for _, x := range sources {
			x.(map[string]any)["alias"] = ""
		}
	}
	cameraFake(variant, sources)
	b, _ := json.Marshal(st)
	status, err := ui.ParseStatus(b)
	if err != nil {
		return "", "", err
	}
	lv := &levels{pgm: []float64{-9, -14}, inputs: map[string]float64{}}
	if status.Audio != nil {
		for n, a := range status.Audio.Sources {
			lv.inputs[a.ID] = -12.0 - 9*float64(n)
		}
	}
	transport, links := "srt", 0
	switch {
	case has("srtla"):
		transport, links = "srtla", 2
	case has("srtla1"):
		transport, links = "srtla", 1
	case has("rtmps"): // the other protocols (OUTPUTS.md §8): the header words them
		transport = "rtmps"
	case has("rtmp"):
		transport = "rtmp"
	case has("rist"):
		transport = "rist"
	}
	lay := ui.NewLayout(1024, 600, 4)
	u := ui.New(lay, tx, net)
	u.Fonts, u.Levels, u.Logo, u.PauseSupported = fonts, lv, ui.DefaultLogo(), true
	u.LinkInfo = func() (string, int) { return transport, links }
	// +dots: the connections' dots of the header (MODEMS.md §6); +dots6: six of them (names shortened to fit)
	if has("dots") || has("dots6") {
		dots := []ui.LinkDot{{Name: "eth0", Kind: "ethernet", Health: "good"}, {Name: "wlan0", Kind: "wifi", Health: "weak"},
			{Name: "wwan0", Kind: "modem", Label: "Movistar", Tech: "5g-nsa", Health: "good"},
			{Name: "wwan1", Kind: "modem", Label: "Vodafone", Tech: "4g", Health: "weak"},
			{Name: "enx1", Kind: "modem", Label: "Orange", Tech: "4g", Health: "bad"}}
		if has("dots6") {
			dots = append(dots, ui.LinkDot{Name: "wwan2", Kind: "modem", Label: "Digi", Tech: "4g", Health: "off", Disabled: true})
		}
		u.Links = func() ([]ui.LinkDot, bool) { return dots, true }
	}
	if legacy != nil {
		u.Legacy = func() *ui.WifiCreds { return legacy }
	}
	clock := now
	u.Clock = func() time.Time { return clock }
	u.SetLayout(lay)
	u.SetStatus(status)
	u.SetLiveSince(now.Add(-2537500 * time.Millisecond))
	p := u.Preview()
	if variant == "audio" || variant == "audiomix" {
		p.AudioView()
	}
	if audioSet {
		link := -1
		if variant == "audiosetpick" {
			link = 1 // camera 2's (with +gone, camera 3's: its audio unplugged)
			if has("gone") {
				link = 2
			}
		}
		p.AudioSettings(link)
		if variant == "audioseterr" { // the engine refused a delay: the view says why
			u.CommandDone(ui.Command{Msg: m("cmd", "audio_delay", "source", "i2s", "ms", 130), Origin: "audioset"}, false,
				"unknown_source")
		}
	}
	if variant == "menu" {
		p.OpenMenu()
		p.Holding("poweroff", 900*time.Millisecond)
	}
	if variant == "off" {
		p.Powering("poweroff")
	}
	if settings {
		p.OpenSettings()
	}
	if has("camstab") {
		p.SettingsPage("cams")
	}
	if page, view := settingsPage(variant); page != "" {
		p.SettingsPage(page)
		switch view {
		case "networks":
			p.OpenNetworks()
		case "keyboard":
			p.OpenKeyboard("Red de Ejemplo 5G", "", "NoEsLaReal12", "")
		case "zones":
			p.OpenZones()
		}
		switch variant {
		case "settings-sources-edited":
			p.EditSlot(2, "off", "")
			p.EditSlot(3, "", "Plano general · grúa")
		case "settings-sources-pick":
			p.PickSlot(2)
		case "settings-sources-alias":
			p.AliasKeyboard(1, "Cámara principal")
		case "settings-updates-confirm":
			p.ConfirmUpdate()
		case "settings-updates-restart-confirm":
			p.ConfirmReboot()
		case "settings-language":
			p.Press(ui.HitButton, "s_lang_"+tx.Code, 0)
		case "settings-sources-hidden": // a tap on Hide of the source on air: the engine refuses it
			u.CommandDone(ui.Command{Msg: map[string]any{"cmd": "hide"}, Origin: "page"}, false, "on_air")
		case "settings-input-edited":
			p.InputEdit("usb|"+fakeLogi, "MJPG|3840x2160@30", "", "none")
		case "settings-input-pick":
			p.InputPick("s_inp_source")
		case "settings-input-modes":
			p.InputPick("s_inp_mode")
		case "settings-input-audio":
			p.InputPick("s_inp_audio")
		case "settings-input-confirm":
			p.InputEdit("", "MJPG|1920x1080@60", "", "")
			p.InputConfirmApply()
		case "settings-input-error":
			p.InputEdit("", "", "1080p50", "")
		}
		streamPreview(p, variant)
		camsPreview(variant, p)
		cameraPreview(p, variant)
		btPreview(p, variant)
	}
	if strings.HasPrefix(variant, "settingsnets") {
		p.OpenNetworks()
		if variant == "settingsnets2" {
			p.ListPage(1)
		}
		if variant == "settingsnetsforget" {
			p.ConfirmForget("Hotspot del móvil")
		}
	}
	if strings.HasPrefix(variant, "settingskb") {
		// A FAKE password (only dots are drawn): 12 characters
		errText, typed := "", "NoEsLaReal12"
		if variant == "settingskbwrong" {
			errText, typed = tx.WrongPasswordAgain, ""
		}
		layer := map[string]string{"settingskbsym": "sym1", "settingskbsym2": "sym2", "settingskbupper": "upper"}[variant]
		p.OpenKeyboard("Red de Ejemplo 5G", errText, typed, layer)
		if variant == "settingskb" {
			p.Press(ui.HitKey, "k", 0)
		}
	}
	if variant == "settingsbusy" {
		net.snap.Pending = &ui.Pending{What: "net", On: false}
	}
	if variant == "settingsapbusy" {
		net.snap.Pending = &ui.Pending{What: "ap", On: true}
	}
	// Renaming «Create my Wi-Fi»: its keyboard (a valid name, or one with a character it does not take), the change
	// under way and the app's refusal with a camera on air over the Wi-Fi (its message, in the device language)
	switch variant {
	case "settingsssidkb":
		p.SSIDKeyboard("Studio 2", "")
	case "settingsssidkbbad":
		p.SSIDKeyboard(map[string]string{"es": "Plató 2", "en": "Stage #2", "fr": "Plateau n°2"}[tx.Code], tx.SSIDBad)
	case "settingsrenaming":
		net.snap.Pending = &ui.Pending{What: "ssid", SSID: "Studio 2"}
	case "settingsrenameonair":
		msg := map[string]string{
			"es": "«Móvil de Ana» entra por esta wifi y está al aire, y se está emitiendo: cambiarle el nombre la reinicia y la cámara se caería. Pon otra fuente (y su audio) o para de emitir antes",
			"en": "“Móvil de Ana” comes in over this Wi-Fi, is on air and streaming: renaming the Wi-Fi restarts it and the camera would drop. Put another source (and its audio) on air or stop streaming first",
			"fr": "« Móvil de Ana » arrive par ce Wi-Fi, est à l’antenne et en diffusion : le renommer le redémarre et la caméra décrocherait. Mettez une autre source (et son audio) ou arrêtez la diffusion avant"}[tx.Code]
		net.snap.Message, net.snap.MsgUntil = msg, now.Add(6*time.Second)
	case "settingsrenamed":
		msg := map[string]string{"es": "Wifi renombrada: une de nuevo las cámaras y los móviles a «Studio 2»",
			"en": "Wi-Fi renamed: join the cameras and phones to “Studio 2” again",
			"fr": "Wi-Fi renommé : reconnectez les caméras et les téléphones à « Studio 2 »"}[tx.Code]
		net.snap.Message, net.snap.MsgUntil = msg, now.Add(6*time.Second)
		net.snap.Wifi.SSID, net.snap.Status.AccessPoint.SSID = "Studio 2", "Studio 2"
	}
	if variant == "settingsonair" {
		// The app's message, in the device language (the app renders it)
		msg := map[string]string{"es": "hay una cámara de red al aire", "en": "a network camera is on air",
			"fr": "une caméra réseau est à l'antenne"}[tx.Code]
		net.snap.Message, net.snap.MsgUntil = msg, now.Add(6*time.Second)
	}
	ants := map[string]ui.WifiClient{"ant3": {State: "connected", Signal: ip(80), Internet: bp(true)},
		"ant1": {State: "connected", Signal: ip(20)}, "antnoinet": {State: "connected", Signal: ip(70), Internet: bp(false)},
		"antsearch": {State: "disconnected"}, "anterror": {State: "error", Error: "wrong_password"}}
	for _, k := range []string{"ant3", "ant1", "antnoinet", "antsearch", "anterror"} { // ui_render.py's order
		if has(k) {
			cl := ants[k]
			cl.Enabled, cl.SSID = true, "Bar Ejemplo"
			net.snap.Client = &cl
			u.Sensors = func() (float64, bool, float64, bool) { return 36, true, 5.18, true } // as on the board
		}
	}
	if i := wizardStep(variant); i >= 0 {
		p.WizardStep(i)
		switch {
		case strings.HasSuffix(variant, "-zones"):
			p.OpenZones()
		case strings.HasSuffix(variant, "-networks"):
			p.OpenNetworks()
		case strings.HasSuffix(variant, "-password-kb"):
			p.WizardKeyboard("webpw", "NoEsLaReal")
		case strings.HasSuffix(variant, "-password-again"):
			p.WizardKeyboard("webpw2", "NoEsLa")
		case strings.HasSuffix(variant, "-name-kb"):
			p.WizardKeyboard("name", "Estudio 2")
		case strings.HasSuffix(variant, "-ssid-kb"):
			p.SSIDKeyboard("Studio 2", "")
		}
	}
	if has("press") {
		p.Press(ui.HitButton, "fade", 0)
	}
	if has("presstile") {
		p.Press(ui.HitTile, "", 1)
	}
	// The drawing, then the meters a few times (ui_render.py: compose() three times)
	u.Tick()
	for range 3 {
		clock = clock.Add(50 * time.Millisecond)
		u.Tick()
	}
	dur := ""
	if bench > 0 {
		t0 := time.Now()
		for range bench {
			u.Dirty = true
			u.Tick()
		}
		dur = (time.Since(t0) / time.Duration(bench)).String()
	}
	name := strings.Join(append([]string{variant}, mods...), "+")
	file := "ui-" + name + ".png"
	if lang != "" {
		name += "-" + lang
		file = name + ".png"
	}
	img, err := composite(u.Image(), lay, frames)
	if err != nil {
		return "", "", err
	}
	f, err := os.Create(filepath.Join(outDir, file))
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return "", "", err
	}
	return name, dur, nil
}

// composite is the preview: black, grey (or the -frames pictures) where the video goes, and the UI layer on top.
func composite(ui0 *image.RGBA, lay *ui.Layout, frames string) (*image.RGBA, error) {
	out := image.NewRGBA(ui0.Rect)
	c := ui.NewCanvas(out, nil)
	c.Paint(ui.Color{R: 0, G: 0, B: 0, A: 1})
	grey := ui.Color{R: 0.22, G: 0.24, B: 0.27, A: 1}
	for i, r := range append([]ui.Rect{lay.PGM}, lay.Tiles...) {
		name := "program.png"
		if i > 0 {
			name = fmt.Sprintf("tile-%d.png", i)
		}
		pic, err := loadFrame(frames, name)
		if err != nil {
			return nil, err
		}
		if pic == nil {
			c.FillRect(float64(r.X), float64(r.Y), float64(r.W), float64(r.H), grey)
			continue
		}
		xdraw.CatmullRom.Scale(out, image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H), pic, pic.Bounds(), xdraw.Src, nil)
	}
	c.DrawImage(ui0, 0, 0)
	return out, nil
}

// loadFrame reads one -frames picture; nil without -frames or when the file is not there.
func loadFrame(dir, name string) (image.Image, error) {
	if dir == "" {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pic, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return pic, nil
}
