package ui

import "testing"

// The board's access point is changed by two clients (NetState: its name; the device client: its channel, its
// password, the cameras' way over it): never two changes at once.
func TestAPChangesOneAtATime(t *testing.T) {
	h, app := pagesHarness(t, "wifi")
	net := h.wifiUp()
	app.snap.Pending = "ap_channel"
	h.u.renameAP()
	if h.u.kb != nil {
		t.Fatal("rename opened while the channel changes")
	}
	app.snap.Pending = ""
	net.snap.Pending = &Pending{What: "ssid"}
	n := len(app.did)
	h.press("s_wifi_rotate", 0.1)
	h.press("s_wifi_rotate", 0.1)
	h.u.openChannelPicker()
	if len(app.did) != n || h.u.settingsView == "pick" {
		t.Fatalf("an AP change went while the name changes: %+v", app.did)
	}
}

// In a camera's view only its own buttons answer (like the network list): the list's entries do not, ← goes back.
func TestCamViewOnlyItsButtons(t *testing.T) {
	h, app, _ := camsPage(t)
	h.u.openCam("cam1")
	h.u.toggleReveal(camSRTEye)
	h.press("s_page_stream", 0.1)
	if h.u.settingsPage != "cams" || h.u.camp.view != "cam1" {
		t.Fatal("a list entry answered in a camera's view")
	}
	h.press("s_back", 0.1)
	if h.u.camp.view != "" || h.u.revealed(camSRTEye) || app.dropped == 0 {
		t.Fatalf("view %q eye %v dropped %d", h.u.camp.view, h.u.revealed(camSRTEye), app.dropped)
	}
}

// Without cameras and with the reception off: its switch turns it on, and «Add camera» says it has to be on first
// (the app refuses a camera with it off).
func TestAddCameraNeedsReception(t *testing.T) {
	h, _, net := camsPage(t)
	st := net.snap.Status
	st.Cameras, st.CamItems, st.Enabled = 0, []NetCamera{}, false
	h.press("s_cams_add", 0.1)
	if h.u.kb != nil {
		t.Fatal("the name keyboard opened with the reception off")
	}
	if txt, warn := h.u.pageNoteText(); txt != Spanish.NetX.AddNeedsReception || !warn {
		t.Fatalf("%q", txt)
	}
	if !NetSwitch(&net.snap) {
		t.Fatal("no switch to turn the reception on")
	}
}
