package ui

import (
	"bytes"
	"image"
	"slices"
	"testing"
)

// The Bluetooth page (Settings › Wireless › Bluetooth, BLUETOOTH.md §3.6) and the headphones button's mark.

const (
	btPhonesAddr  = "AC:80:0A:12:34:56" // paired, connected, the monitor plays through it
	btHeadsetAddr = "70:BF:92:0C:4E:11" // paired, not connected
	btSpeakerAddr = "F8:DF:15:A1:B2:C3" // found
	btPhoneAddr   = "5C:F7:E6:01:02:03" // found
)

func fakeBT() *BTState {
	return &BTState{Available: true, On: true, Powered: true, Name: "Estudio 2", Devices: []BTDevice{
		{Address: btPhonesAddr, Name: "WH-1000XM4", Kind: "headphones", Paired: true, Connected: true, Ready: true,
			Use: "listen", Uses: []string{"listen", "mic"}, Output: true},
		{Address: btHeadsetAddr, Name: "Jabra Talk 45", Kind: "headset", Paired: true, Use: "mic", Uses: []string{"mic"}},
		{Address: btSpeakerAddr, Name: "JBL Flip 5", Kind: "speaker", Uses: []string{"listen"}},
		{Address: btPhoneAddr, Name: "iPhone de Ana", Kind: "phone", Uses: []string{"input"}},
	}}
}

// btHarness: Settings open on the Bluetooth page, drawn (the rows' keys come from what was drawn).
func btHarness(t *testing.T, mod func(*BTState)) (*harness, *fakeApp) {
	t.Helper()
	h, app := pagesHarness(t, "bluetooth")
	app.snap.Bluetooth, app.snap.BluetoothAPI = fakeBT(), Yes
	if mod != nil {
		mod(app.snap.Bluetooth)
	}
	h.u.Fonts, h.u.Levels = fonts(t), &levels{pgm: -9, in: -12}
	h.u.LinkInfo = func() (string, int) { return "srt", 0 }
	h.u.SetLayout(h.lay)
	h.u.Dirty = true
	h.u.Tick()
	return h, app
}

// redraw draws the page again (what the screen does every second and after a touch).
func (h *harness) redraw() {
	h.u.Dirty = true
	h.u.Tick()
}

func TestBluetoothInWireless(t *testing.T) {
	if len(SettingsGroups) != 7 {
		t.Fatalf("%d groups: the list takes seven", len(SettingsGroups))
	}
	g := groupOf("bluetooth")
	if g == nil || g.ID != "wifi" || !slices.Equal(g.Pages, []string{"wifi", "bluetooth"}) {
		t.Fatalf("group %+v", g)
	}
	for code, want := range map[string]string{"es": "Inalámbrico", "en": "Wireless", "fr": "Sans fil"} {
		if got := Lang(code).GroupTitle["wifi"]; got != want {
			t.Errorf("%s: %q", code, got)
		}
	}
	// The Wi-Fi's dot stays (the group's entry shows its pages' dots)
	h, _ := btHarness(t, nil)
	down := &NetSnapshot{API: Yes, Status: &NetStatus{AccessPoint: AccessPoint{Enabled: bp(true), Policy: "always",
		State: "error"}}}
	if h.u.pageDot("wifi", down, &AppSnapshot{}, nil) != "warn" || h.u.pageDot("bluetooth", down, &AppSnapshot{}, nil) != "" {
		t.Error("the dots")
	}
}

func TestBluetoothWatch(t *testing.T) {
	h, app := btHarness(t, nil)
	if !app.watch().Bluetooth {
		t.Fatal("the page does not ask for the Bluetooth")
	}
	h.press("s_tab_wifi", 0.1)
	h.u.Tick()
	if app.watch().Bluetooth {
		t.Fatal("asked with the page gone")
	}
}

func TestBluetoothSwitchSearchVisible(t *testing.T) {
	h, app := btHarness(t, nil)
	h.press("s_bt_power", 0.1)
	if a := app.lastDid(); a.What != "bt_power" || a.Body["on"] != false || h.u.btp.powerTo {
		t.Fatalf("%+v", a)
	}
	// While the app has it, the row shows it half-way and another tap does nothing
	app.snap.Pending = "bt_power"
	n := len(app.did)
	h.press("s_bt_power", 0.1)
	if len(app.did) != n {
		t.Fatalf("a second order while one runs: %+v", app.did)
	}
	app.snap.Pending = ""
	h.press("s_bt_scan", 0.1)
	if a := app.lastDid(); a.What != "bt_scan" || a.Body != nil {
		t.Fatalf("%+v", a)
	}
	h.press("s_bt_visible", 0.1)
	if a := app.lastDid(); a.What != "bt_visible" || a.Body["on"] != true {
		t.Fatalf("%+v", a)
	}
	// Visible: the tap closes the window; searching: no second search
	app.snap.Bluetooth.Visible, app.snap.Bluetooth.VisibleLeft, app.snap.Bluetooth.Scanning = true, 90, true
	h.redraw()
	n = len(app.did)
	h.press("s_bt_scan", 0.1)
	h.press("s_bt_visible", 0.1)
	if len(app.did) != n+1 || app.lastDid().What != "bt_visible" || app.lastDid().Body["on"] != false {
		t.Fatalf("%+v", app.did[n:])
	}
	// Off: only the switch
	app.snap.Bluetooth.On, app.snap.Bluetooth.Powered = false, false
	h.redraw()
	if keys := h.u.btKeys(&app.snap); !slices.Equal(keys, []string{"s_bt_power"}) {
		t.Fatalf("off: %v", keys)
	}
}

func TestBluetoothUnavailable(t *testing.T) {
	for _, reason := range []string{"no_adapter", "no_stack", "no_audio", ""} {
		h, app := btHarness(t, func(b *BTState) { *b = BTState{Reason: reason} })
		if keys := h.u.btKeys(&app.snap); len(keys) != 0 {
			t.Errorf("%s: keys %v", reason, keys)
		}
		h.press("s_bt_power", 0.1) // where the switch would be: nothing
		if len(app.did) != 0 {
			t.Errorf("%s: %+v", reason, app.did)
		}
	}
	// An app without the route: the page says so
	h, app := btHarness(t, nil)
	app.snap.Bluetooth, app.snap.BluetoothAPI = nil, No
	h.redraw()
	if keys := h.u.btKeys(&app.snap); len(keys) != 0 {
		t.Errorf("an older app: keys %v", keys)
	}
}

func TestBluetoothPairFromTheList(t *testing.T) {
	h, app := btHarness(t, nil)
	// The rows: the two paired, then the two found (each found one with its «Pair» tried first)
	if !slices.Equal(h.u.btp.shown, []string{btPhonesAddr, btHeadsetAddr, btSpeakerAddr, btPhoneAddr}) {
		t.Fatalf("rows %v", h.u.btp.shown)
	}
	keys := h.u.btKeys(&app.snap)
	if slices.Contains(keys, "s_btpair_0") || slices.Index(keys, "s_btpair_2") > slices.Index(keys, "s_btrow_2") {
		t.Fatalf("keys %v", keys)
	}
	h.press("s_btpair_2", 0.1)
	if a := app.lastDid(); a.What != "bt_device" || a.ID != btSpeakerAddr || a.Value != "pair" {
		t.Fatalf("%+v", a)
	}
	// The whole row pairs too
	h.press("s_btrow_3", 0.1)
	if a := app.lastDid(); a.ID != btPhoneAddr || a.Value != "pair" {
		t.Fatalf("%+v", a)
	}
	// While an order on a device runs, the others wait (greyed out); also the app's own reconnection at power-on
	// (a pending connect)
	for _, run := range []struct {
		i     int
		order string
	}{{2, "pair"}, {1, "connect"}} {
		app.snap.Bluetooth.Devices[run.i].Pending = run.order
		h.redraw()
		n := len(app.did)
		h.press("s_btpair_3", 0.1)
		if len(app.did) != n {
			t.Fatalf("an order while a %s runs: %+v", run.order, app.did[n:])
		}
		app.snap.Bluetooth.Devices[run.i].Pending = ""
	}
}

// The device is fixed when the finger goes down: a search reorders the list before it is lifted.
func TestBluetoothPickedOnDown(t *testing.T) {
	h, app := btHarness(t, nil)
	x, y := h.at("s_btpair_2")
	h.u.OnTouch("down", x, y, 0)
	d := app.snap.Bluetooth.Devices
	d[2], d[3] = d[3], d[2] // the phone moves up while the finger is on the speaker
	h.redraw()
	if h.u.btp.shown[2] != btPhoneAddr {
		t.Fatal("not reordered")
	}
	h.u.OnTouch("up", x, y, secs(0.1))
	if a := app.lastDid(); a.ID != btSpeakerAddr {
		t.Fatalf("paired %s, not what was under the finger", a.ID)
	}
}

func TestBluetoothDeviceView(t *testing.T) {
	h, app := btHarness(t, nil)
	h.press("s_btrow_0", 0.1)
	if h.u.btp.view != btPhonesAddr {
		t.Fatalf("view %q", h.u.btp.view)
	}
	h.redraw()
	snap := h.net.Snapshot()
	keys := h.u.settingsKeys(&snap)
	for _, k := range []string{"s_back", "s_btuse_0", "s_btuse_1", "s_btd_conn", "s_btd_forget"} {
		if !slices.Contains(keys, k) {
			t.Errorf("no %s in %v", k, keys)
		}
	}
	if slices.Contains(keys, "s_page_system") || slices.Contains(keys, "s_btuse_2") {
		t.Errorf("keys %v", keys)
	}
	// Its other use
	h.press("s_btuse_1", 0.1)
	if a := app.lastDid(); a.What != "bt_use" || a.ID != btPhonesAddr || a.Body["use"] != "mic" {
		t.Fatalf("%+v", a)
	}
	n := len(app.did)
	h.press("s_btuse_0", 0.1) // the one it has: nothing
	if len(app.did) != n {
		t.Fatal("the same use sent")
	}
	// Disconnecting what the monitor plays through: no question
	h.press("s_btd_conn", 0.1)
	if a := app.lastDid(); a.What != "bt_device" || a.Value != "disconnect" {
		t.Fatalf("%+v", a)
	}
	// Forget: a second tap
	n = len(app.did)
	h.press("s_btd_forget", 0.1)
	if len(app.did) != n || !h.u.confirming("bt_forget:"+btPhonesAddr) {
		t.Fatal("forgotten at the first tap")
	}
	h.press("s_btd_forget", 0.1)
	if a := app.lastDid(); a.Value != "forget" || a.ID != btPhonesAddr {
		t.Fatalf("%+v", a)
	}
	// Forgotten: back to the list
	app.snap.Bluetooth.Devices = app.snap.Bluetooth.Devices[1:]
	h.redraw()
	if h.u.btp.view != "" {
		t.Fatal("the view of a forgotten device stays")
	}
	// A disconnected one connects; one order at a time
	h.press("s_btrow_0", 0.1)
	if h.u.btp.view != btHeadsetAddr {
		t.Fatalf("view %q", h.u.btp.view)
	}
	h.redraw()
	h.press("s_btd_conn", 0.1)
	if a := app.lastDid(); a.Value != "connect" || a.ID != btHeadsetAddr {
		t.Fatalf("%+v", a)
	}
	app.snap.Pending = "bt_device"
	n = len(app.did)
	h.press("s_btd_forget", 0.1)
	h.press("s_btd_forget", 0.1)
	if len(app.did) != n {
		t.Fatal("an order while another is queued")
	}
	// Settings closes (in a view only its own buttons and the X answer): back to the list next time
	h.press("s_close", 0.1)
	if h.u.settingsOpen || h.u.btp.view != "" {
		t.Fatal("the view outlived its page")
	}
}

// Disconnecting a device whose audio is on air (an input in the program while streaming) asks for a second tap.
func TestBluetoothDisconnectOnAir(t *testing.T) {
	h, app := btHarness(t, func(b *BTState) {
		b.Devices[1].Connected, b.Devices[1].Ready = true, true
	})
	st := *h.u.st
	id := "bt:70BF920C4E11"
	st.Audio = &AudioStatus{Mode: "single", Main: &id, Sources: []AudioInput{{ID: id, Name: "Jabra", State: "live"}}}
	st.Output = OutputStatus{Mode: "on", State: "on"}
	h.u.SetStatus(&st)
	h.press("s_btrow_1", 0.1)
	h.redraw()
	n := len(app.did)
	h.press("s_btd_conn", 0.1)
	if len(app.did) != n || !h.u.confirming("bt_disconnect:"+btHeadsetAddr) {
		t.Fatal("disconnected what is on air at the first tap")
	}
	h.press("s_btd_conn", 0.1)
	if a := app.lastDid(); a.Value != "disconnect" || a.ID != btHeadsetAddr {
		t.Fatalf("%+v", a)
	}
}

func TestBluetoothPages(t *testing.T) {
	h, app := btHarness(t, func(b *BTState) {
		for i := range 6 {
			b.Devices = append(b.Devices, BTDevice{Address: "00:1B:66:AA:01:0" + string(rune('0'+i)), Name: "Extra",
				Kind: "speaker", Uses: []string{"listen"}})
		}
	})
	rows := h.lay.btRows()
	if rows < 5 {
		t.Fatalf("%d rows at 1024×600", rows)
	}
	if h.u.btp.pages != 2 || len(h.u.btp.shown) != rows {
		t.Fatalf("pages %d, rows %d", h.u.btp.pages, len(h.u.btp.shown))
	}
	h.press("s_next", 0.1)
	h.redraw()
	if h.u.btp.page != 1 || len(h.u.btp.shown) != 10-rows {
		t.Fatalf("page %d, rows %d", h.u.btp.page, len(h.u.btp.shown))
	}
	if keys := h.u.btKeys(&app.snap); slices.Contains(keys, "s_next") || !slices.Contains(keys, "s_prev") {
		t.Fatalf("keys %v", keys)
	}
}

// Every row a finger hits is at least 48 px high, and nothing of the page overlaps.
func TestBluetoothLayout(t *testing.T) {
	for _, c := range []struct {
		sz   [2]int
		warn bool
	}{{[2]int{1024, 600}, false}, {[2]int{1280, 720}, false}, {[2]int{1920, 1080}, false}, {[2]int{1024, 600}, true},
		{[2]int{1920, 1080}, true}} {
		sz := c.sz
		l := NewLayout(sz[0], sz[1], 4)
		l.btPlace(c.warn)
		if c.warn { // the band over the controls, and the list one row shorter
			if band := l.SettingsPage.Y + l.btWarnH(); l.Buttons["s_bt_power"].Y <= band {
				t.Errorf("%v: the switch under the band: %+v", sz, l.Buttons["s_bt_power"])
			}
			if _, stale := l.Buttons["s_btrow_"+string(rune('0'+l.btRows()))]; stale {
				t.Errorf("%v: a row left where the band took it", sz)
			}
		}
		var keys []string
		for i := range l.btRows() {
			k := "s_btrow_" + string(rune('0'+i))
			keys = append(keys, k)
			if r := l.Buttons[k]; r.H < 48 || r.Y+r.H > l.Buttons["s_prev"].Y {
				t.Errorf("%v: %s %+v", sz, k, r)
			}
		}
		keys = append(keys, "s_bt_power", "s_bt_scan", "s_bt_visible", "s_prev", "s_next")
		for i, a := range keys {
			for _, b := range keys[i+1:] {
				if overlaps(l.Buttons[a], l.Buttons[b]) {
					t.Errorf("%v: %s and %s overlap", sz, a, b)
				}
			}
		}
		view := []string{"s_btuse_0", "s_btuse_1", "s_btuse_2", "s_btd_conn", "s_btd_forget"}
		for i, a := range view {
			for _, b := range view[i+1:] {
				if overlaps(l.Buttons[a], l.Buttons[b]) {
					t.Errorf("%v: %s and %s overlap", sz, a, b)
				}
			}
		}
	}
}

// The headphones button over Bluetooth: «BT» and the device's name, drawn inside the button only (outside the
// program's level meters, MIXER.md §17), and as before without a target.
func TestPhonesBluetoothMark(t *testing.T) {
	h, _ := drawHarness(t)
	ph := h.lay.Buttons["a_phones"]
	for _, s := range h.lay.Strips {
		if overlaps(ph, s.Meter) {
			t.Fatal("the headphones button is over a level meter")
		}
	}
	draw := func(target, device string) *image.RGBA {
		g := -12
		st := &Status{}
		st.Program.Layout = "full"
		st.Audio = &AudioStatus{Mode: "single", Phones: &PhonesStatus{On: true, GainDb: &g, State: "on", Target: target,
			Device: device}}
		h.u.SetStatus(st)
		h.u.view = "audio"
		h.u.Dirty = true
		h.u.Tick()
		img := h.u.Image()
		return img.SubImage(image.Rect(0, 0, img.Rect.Dx(), img.Rect.Dy())).(*image.RGBA)
	}
	pix := func(img *image.RGBA, r Rect) []byte {
		var out []byte
		for y := r.Y; y < r.Y+r.H; y++ {
			out = append(out, img.Pix[img.PixOffset(r.X, y):img.PixOffset(r.X+r.W, y)]...)
		}
		return out
	}
	screen := pix(draw("", ""), ph)
	if !bytes.Equal(screen, pix(draw("screen", ""), ph)) {
		t.Error("the screen's target is drawn differently from no target")
	}
	bt := pix(draw("bluetooth", "WH-1000XM4"), ph)
	if bytes.Equal(screen, bt) {
		t.Error("Bluetooth looks like the screen's jack")
	}
}

// No text of the tables is cut on the page, in any language.
func TestBluetoothRenders(t *testing.T) {
	page := func(mod func(b *BTState), view, confirm string) func(h *harness) {
		return func(h *harness) {
			pageApp(h, "bluetooth", func(a *AppSnapshot) {
				a.Bluetooth, a.BluetoothAPI = fakeBT(), Yes
				if mod != nil {
					mod(a.Bluetooth)
				}
			})
			h.u.btp.view = view
			if confirm != "" {
				h.u.confirmTap(confirm)
			}
		}
	}
	views := []renderView{
		{"list", page(nil, "", "")},
		{"off", page(func(b *BTState) { b.On, b.Powered, b.Devices = false, false, nil }, "", "")},
		{"empty", page(func(b *BTState) { b.Devices = nil }, "", "")},
		{"searching", page(func(b *BTState) { b.Devices, b.Scanning = nil, true }, "", "")},
		{"visible", page(func(b *BTState) { b.Visible, b.VisibleLeft = true, 107 }, "", "")},
		{"visible-noname", page(func(b *BTState) { b.Visible, b.VisibleLeft, b.Name = true, 5, "" }, "", "")},
		{"busy", page(func(b *BTState) {
			b.Devices[2].Pending = "pair"
			b.Devices[1].Error = "not_reachable"
			b.Devices[0].Connected, b.Devices[0].Ready, b.Devices[0].Output = true, false, false
		}, "", "")},
		{"device", page(nil, btPhonesAddr, "")},
		{"device-one-use", page(nil, btHeadsetAddr, "")},
		{"device-forget", page(nil, btPhonesAddr, "bt_forget:"+btPhonesAddr)},
		{"device-disconnect", page(nil, btPhonesAddr, "bt_disconnect:"+btPhonesAddr)},
	}
	warn := func(b *BTState) { b.Reason = "no_audio" }
	views = append(views,
		renderView{"no-audio", page(warn, "", "")},
		renderView{"no-audio-off", page(func(b *BTState) { warn(b); b.On, b.Powered, b.Devices = false, false, nil }, "", "")},
		renderView{"no-audio-pages", page(func(b *BTState) {
			warn(b)
			for i := range 6 {
				b.Devices = append(b.Devices, BTDevice{Address: "00:1B:66:AA:01:0" + string(rune('0'+i)), Name: "Extra",
					Kind: "speaker", Uses: []string{"listen"}})
			}
		}, "", "")},
		renderView{"power-failed", page(func(b *BTState) { b.Powered, b.PowerFailed = false, true }, "", "")},
		renderView{"power-failed-off", page(func(b *BTState) { b.On, b.PowerFailed = false, true }, "", "")})
	for _, reason := range []string{"no_adapter", "no_stack", "no_audio"} {
		views = append(views, renderView{"unavailable-" + reason, page(func(b *BTState) { *b = BTState{Reason: reason} }, "", "")})
	}
	for _, e := range []string{"rejected", "timeout", "not_reachable", "gone", "failed"} {
		views = append(views, renderView{"error-" + e, page(func(b *BTState) { b.Devices[3].Error = e }, "", "")})
	}
	for _, p := range []string{"pair", "connect", "disconnect", "forget"} {
		views = append(views, renderView{"pending-" + p, page(func(b *BTState) { b.Devices[1].Pending = p }, "", "")})
	}
	checkRenders(t, views)
}

// A Bluetooth audio input's strip carries the «BT» mark (its name, a device's, says nothing of how it comes in), and
// one that is gone is named by its address's end in the audio settings.
func TestBluetoothInputMark(t *testing.T) {
	h, _ := drawHarness(t)
	strip := func(id string) []byte {
		st := &Status{}
		st.Program.Layout = "full"
		st.Audio = &AudioStatus{Mode: "single", Sources: []AudioInput{{ID: id, Name: "iPhone de Ana", State: "no_signal",
			DetailCode: "bt_no_audio"}}}
		h.u.SetStatus(st)
		h.u.view = "audio"
		h.u.Dirty = true
		h.u.Tick()
		img, r := h.u.Image(), h.lay.Strips[0].Name
		var out []byte
		for y := r.Y; y < r.Y+r.H; y++ {
			out = append(out, img.Pix[img.PixOffset(r.X, y):img.PixOffset(r.X+r.W, y)]...)
		}
		return out
	}
	if bytes.Equal(strip("alsa:Phone"), strip("bt:5CF7E6010203")) {
		t.Error("no mark on a Bluetooth input")
	}
	if g := goneName("bt:5CF7E6010203"); g != "Bluetooth …0203" {
		t.Errorf("gone: %q", g)
	}
}

// Bluetooth that works without its audio (no_audio): the warning band over the controls, which move down and still
// answer; the adapter that does not turn on says so instead of «Turning on…».
func TestBluetoothWarnings(t *testing.T) {
	h, app := btHarness(t, func(b *BTState) { b.Reason = "no_audio" })
	if h.lay.btWarn != 1 || h.lay.btRows() != 4 || len(h.u.btp.shown) != 4 { // four rows under the band at 1024×600
		t.Fatalf("band %d, rows %d, shown %d", h.lay.btWarn, h.lay.btRows(), len(h.u.btp.shown))
	}
	h.press("s_bt_scan", 0.1)
	if a := app.lastDid(); a.What != "bt_scan" {
		t.Fatalf("%+v", a)
	}
	// Back to normal: the controls go up again
	app.snap.Bluetooth.Reason = ""
	h.redraw()
	if h.lay.btWarn != 0 || h.lay.Buttons["s_bt_power"].Y != h.lay.SettingsPage.Y {
		t.Fatalf("band %d, switch %+v", h.lay.btWarn, h.lay.Buttons["s_bt_power"])
	}
	// The adapter does not follow «on»
	app.snap.Bluetooth.Powered, app.snap.Bluetooth.PowerFailed = false, true
	h.redraw()
	if keys := h.u.btKeys(&app.snap); !slices.Contains(keys, "s_bt_power") {
		t.Fatalf("keys %v", keys)
	}
}
