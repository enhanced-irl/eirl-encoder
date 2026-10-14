package ui

import (
	"strings"
	"testing"
	"time"
)

func destApp() *DestInfo {
	return &DestInfo{ManagedBy: "local", URL: "srtla://eu1.relay.enhancedirl.com:5000", Mode: "caller", LatencyMs: 2000,
		StreamIDSet: true, VideoKbps: 6000, AudioKbps: 128, ABR: true, VideoMinKbps: 1500, Transport: "srtla",
		Output: "on"}
}

// destHarness: Settings open on Stream → Destination with the app's destination.
func destHarness(t *testing.T) (*harness, *fakeApp) {
	t.Helper()
	h, app := pagesHarness(t, "dest")
	app.snap.Dest, app.snap.DestAPI = destApp(), Yes
	h.u.Tick()
	return h, app
}

func TestDestDraftAndSave(t *testing.T) {
	h, app := destHarness(t)
	if !app.watch().Dest {
		t.Fatalf("watch %+v", app.watch())
	}
	// Nothing changed: no Save
	if keys := h.u.settingsKeys(&NetSnapshot{}); contains(keys, "s_dest_save") {
		t.Fatal("Save with nothing to save")
	}
	h.press("s_dest_lat_p", 0.1)
	h.press("s_dest_aud_m", 0.1)
	h.press("s_dest_vid_m", 0.1)
	h.press("s_dest_abr", 0.1)
	if len(app.did) != 0 {
		t.Fatal("a stepper went to the app")
	}
	// The key, on the secret keyboard (dots, its eye), into the draft
	h.press("s_dest_sid", 0.1)
	if h.u.kb == nil || !h.u.kb.secret() {
		t.Fatal("no secret keyboard")
	}
	for _, c := range "publishkey" {
		h.tapKey(string(c), 0.1)
	}
	typed := h.u.kb.buf
	h.tapKey("OK", 0.1)
	if h.u.kb != nil || string(h.u.dst.sid) != "publishkey" || !allZero(typed) {
		t.Fatalf("draft %q, keyboard wiped %v", h.u.dst.sid, allZero(typed))
	}
	draft := h.u.dst.sid
	h.press("s_dest_save", 0.1)
	a := app.lastDid()
	if a.What != "dest_save" || a.Body["latencyMs"] != 2100 || a.Body["audioBitrateKbps"] != 96 ||
		a.Body["videoBitrateKbps"] != 5500 || a.Body["adaptiveBitrate"] != false || a.Body["apply"] != false {
		t.Fatalf("%+v", a.Body)
	}
	if _, ok := a.Body["streamId"]; ok || app.secrets[len(app.secrets)-1]["streamId"] != "publishkey" {
		t.Fatalf("the key went as %+v / %+v", a.Body, app.secrets)
	}
	// Until the app saves it, the draft keeps it (a refused save does not make the user type it again)
	if string(h.u.dst.sid) != "publishkey" {
		t.Fatal("the key left the draft before the app saved it")
	}
	// The app took it: the draft goes, and the key is wiped
	app.snap.Last = &AppResult{What: "dest_save", OK: true, At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	h.u.Dirty = true
	drawOnce(h)
	if h.u.dst.dirty() || !allZero(draft) {
		t.Fatal("the draft or the key stays after the app saved it")
	}
}

func TestDestApplyAndClear(t *testing.T) {
	h, app := destHarness(t)
	st := *h.u.st
	st.Output.Mode = "on"
	h.u.SetStatus(&st)
	h.press("s_dest_pass", 0.1)
	h.tapKey("OK", 0.1) // empty: nothing set, nothing to clear
	if h.u.dst.clearPass || h.u.dst.pass != nil {
		t.Fatal("an unset passphrase is cleared")
	}
	h.press("s_dest_sid", 0.1)
	h.tapKey("OK", 0.1) // empty: the saved key is removed
	if !h.u.dst.clearSID {
		t.Fatal("not cleared")
	}
	// Streaming: «Save and apply» needs a second tap
	h.press("s_dest_apply", 0.1)
	if len(app.did) != 0 || !h.u.confirming("dest_apply") {
		t.Fatal("applied at the first tap while streaming")
	}
	h.press("s_dest_apply", 0.1)
	a := app.lastDid()
	if a.What != "dest_save" || a.Body["apply"] != true || a.Body["streamId"] != nil || len(a.Secrets) != 0 {
		t.Fatalf("%+v", a)
	}
	if _, ok := a.Body["streamId"]; !ok {
		t.Fatal("the key is not cleared (null)")
	}
	// Saved, not applied: «Apply» restarts
	h.u.dst.wipe()
	app.snap.Dest.PendingRestart = true
	app.did = nil
	h.press("s_dest_apply", 0.1)
	h.press("s_dest_apply", 0.1)
	if app.lastDid().What != "restart" {
		t.Fatalf("%+v", app.did)
	}
}

func TestDestEyeAndManaged(t *testing.T) {
	h, app := destHarness(t)
	var drawn []string
	drawnText = func(s string) { drawn = append(drawn, s) }
	defer func() { drawnText = nil }()
	app.snap.DestSecrets = &DestSecrets{StreamID: "publish:SecretoDeVerdad"}
	drawOnce(h)
	if strings.Contains(strings.Join(drawn, "|"), "SecretoDeVerdad") {
		t.Fatal("the key is drawn with its eye closed")
	}
	// The eye asks for it when the app has not given it (a failed reveal is asked again next time)
	app.snap.DestSecrets = nil
	h.press("s_dest_sid_eye", 0.1)
	if app.lastDid().What != "dest_reveal" || !h.u.revealed("s_dest_sid_eye") {
		t.Fatalf("%+v", app.did)
	}
	h.press("s_dest_sid_eye", 0.1)
	h.press("s_dest_sid_eye", 0.1)
	if len(app.did) != 2 {
		t.Fatalf("a reveal that brought nothing is not asked again: %+v", app.did)
	}
	app.snap.DestSecrets = &DestSecrets{StreamID: "publish:SecretoDeVerdad"}
	drawn = nil
	drawOnce(h)
	if !strings.Contains(strings.Join(drawn, "|"), "SecretoDeVerdad") {
		t.Fatalf("not shown with its eye open: %q", drawn)
	}
	// Leaving the page: the eye closes and the app drops what it revealed
	h.openPage("pause")
	if h.u.revealed("s_dest_sid_eye") || app.dropped == 0 {
		t.Fatal("the secret stays")
	}
	// Managed by EnhancedIRL: only the eyes answer
	h.openPage("dest")
	app.snap.Dest.ManagedBy = "eirl"
	keys := h.u.pageKeys("dest", &NetSnapshot{})
	if len(keys) != 2 || !contains(keys, "s_dest_sid_eye") {
		t.Fatalf("%v", keys)
	}
}

func TestDestBounds(t *testing.T) {
	if stepIn(20, 100, 20, 8000, 1) != 100 || stepIn(8000, 100, 20, 8000, 1) != 8000 || stepIn(150, 100, 20, 8000, -1) != 100 {
		t.Error("stepIn")
	}
	if stepList(128, AudioRates, 1) != 160 || stepList(64, AudioRates, -1) != 64 || stepList(100, AudioRates, -1) != 96 {
		t.Error("stepList")
	}
	h, _ := destHarness(t)
	// The minimum never above the maximum
	for range 20 {
		h.press("s_dest_vid_m", 0.1)
	}
	n := h.u.destNow(destApp())
	if n.video != VideoMin || n.vm > n.video {
		t.Fatalf("%+v", n)
	}
}

// drawOnce draws the screen now (the harness's UI with fonts).
func drawOnce(h *harness) {
	if h.u.Fonts == nil {
		h.u.Fonts = testFonts
		if h.u.Fonts == nil {
			f, err := LoadFonts("testdata/fonts")
			if err != nil {
				panic(err)
			}
			testFonts, h.u.Fonts = f, f
		}
		h.u.SetLayout(h.lay)
	}
	h.u.Dirty = true
	h.u.Tick()
}

func TestRenderStreamPages(t *testing.T) {
	dest := func(mod func(a *AppSnapshot)) func(h *harness) {
		return func(h *harness) {
			pageApp(h, "dest", func(a *AppSnapshot) {
				a.Dest, a.DestAPI = destApp(), Yes
				if mod != nil {
					mod(a)
				}
			})
		}
	}
	ups := []UplinkInfo{{Name: "eth0", IP: "192.168.1.20", Kind: "ethernet", InUse: true, TxKbps: 3150},
		{Name: "usb0", IP: "192.168.8.100", Kind: "usb", InUse: true, TxKbps: 2480},
		{Name: "wlan0", IP: "10.12.0.33", Kind: "wifi", NoInternet: true},
		{Name: "usb1", IP: "192.168.8.100", Kind: "usb", Conflict: true},
		{Name: "usb2", IP: "192.168.9.100", Kind: "usb", Disabled: true}}
	uplinks := func(list []UplinkInfo, transport string) func(h *harness) {
		return func(h *harness) {
			pageApp(h, "uplinks", func(a *AppSnapshot) {
				a.Status, a.StatusAPI = &StatusInfo{Transport: transport, HasUplinks: true, Uplinks: list}, Yes
			})
		}
	}
	checkRenders(t, []renderView{
		{"dest", dest(nil)},
		{"dest-asking", func(h *harness) { pageApp(h, "dest", nil) }},
		{"dest-old", dest(func(a *AppSnapshot) { a.Dest, a.DestAPI = nil, No })},
		{"dest-managed", dest(func(a *AppSnapshot) { a.Dest.ManagedBy = "eirl" })},
		{"dest-pending", dest(func(a *AppSnapshot) { a.Dest.PendingRestart = true })},
		{"dest-none", dest(func(a *AppSnapshot) { a.Dest.URL, a.Dest.StreamIDSet, a.Dest.ABR = "", false, false })},
		{"dest-error", dest(func(a *AppSnapshot) {
			a.Last = &AppResult{What: "dest_save", Status: 400, Code: "validation", At: time.Unix(100001, 0),
				Fields: map[string]AppFieldError{"latencyMs": {Message: "de 20 a 8000 ms"}}}
		})},
		{"dest-edited", func(h *harness) {
			dest(nil)(h)
			h.u.pageTouch("s_dest_lat_p", "", false)
		}},
		{"dest-confirm", func(h *harness) {
			dest(nil)(h)
			h.u.pageTouch("s_dest_abr", "", false)
			h.u.confirmTap("dest_apply")
		}},
		{"dest-reveal", func(h *harness) {
			dest(func(a *AppSnapshot) { a.DestSecrets = &DestSecrets{StreamID: "publish:" + strings.Repeat("k", 60)} })(h)
			h.u.toggleReveal("s_dest_sid_eye")
		}},
		{"dest-keyboard", func(h *harness) {
			dest(nil)(h)
			h.u.pageTouch("s_dest_pass", "", false)
		}},
		{"dest-url-keyboard", func(h *harness) {
			dest(nil)(h)
			h.u.pageTouch("s_dest_url", "", false)
		}},
		{"uplinks", uplinks(ups, "srtla")},
		{"uplinks-single", uplinks(ups[:1], "srtla")},
		{"uplinks-nosrtla", uplinks(ups[:2], "srt")},
		{"uplinks-none", uplinks(nil, "srtla")},
		{"uplinks-confirm", func(h *harness) {
			uplinks(ups[:1], "srtla")(h)
			h.u.confirmTap("uplink_last:eth0")
		}},
	})
}
