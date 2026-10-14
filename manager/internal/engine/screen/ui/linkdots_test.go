package ui

import (
	"testing"
)

func fiveDots() []LinkDot {
	return []LinkDot{{Name: "eth0", Kind: "ethernet", Health: "good"}, {Name: "wlan0", Kind: "wifi", Health: "weak"},
		{Name: "wwan0", Kind: "modem", Label: "Movistar", Tech: "5g-nsa", Health: "good"},
		{Name: "wwan1", Kind: "modem", Label: "Vodafone", Tech: "4g", Health: "weak"},
		{Name: "enx1", Kind: "modem", Label: "Orange", Tech: "4g", Health: "bad"}}
}

func liveHeader(h *harness, dots []LinkDot, ok bool) {
	st := *h.u.st
	k, r := 5800, 57
	st.Output = OutputStatus{Mode: "on", State: "on", SentKbps: &k, RTTMs: &r}
	h.u.SetStatus(&st)
	h.u.LinkInfo = func() (string, int) { return "srtla", len(dots) }
	h.u.Sensors = func() (float64, bool, float64, bool) { return 36, true, 5.18, true }
	h.u.Links = func() ([]LinkDot, bool) { return dots, ok }
}

// The header's dots in every language: with names, with long names (smaller, then technology, then dots only),
// and an app without links.json (the header as before)
func TestLinkDotsRender(t *testing.T) {
	eight := append(fiveDots(),
		LinkDot{Name: "wwan2", Kind: "modem", Label: "Telefónica Empresas", Tech: "4g", Health: "off", Disabled: true},
		LinkDot{Name: "usb0", Kind: "usb", Health: "good"}, LinkDot{Name: "wwan3", Kind: "modem", Tech: "3g", Health: "weak"})
	checkRenders(t, []renderView{
		{"dots", func(h *harness) { liveHeader(h, fiveDots(), true) }},
		{"dots-eight", func(h *harness) { liveHeader(h, eight, true) }},
		{"dots-stale", func(h *harness) { liveHeader(h, fiveDots(), false) }},
		{"dots-idle", func(h *harness) { h.u.Links = func() ([]LinkDot, bool) { return fiveDots()[:2], true } }},
	})
}

// With the app's judgement, every row of Streaming › Connections has its dot and its reason, every code translated
func TestConnectionsPageRender(t *testing.T) {
	codes := []struct{ health, code string }{{"good", "ok"}, {"off", "off"}, {"bad", "ip_conflict"},
		{"bad", "disconnected"}, {"bad", "searching"}, {"bad", "sim_missing"}, {"bad", "sim_pin"}, {"bad", "sim_puk"},
		{"bad", "sim_error"}, {"bad", "modem_failed"}, {"bad", "denied"}, {"bad", "roaming_off"}, {"bad", "no_internet"},
		{"bad", "signal_minimal"}, {"weak", "signal_weak"}, {"weak", "slow_tech"}, {"weak", "connecting"},
		{"weak", "link_weak"}}
	var views []renderView
	for i := 0; i < len(codes); i += UplinkRows {
		var list []UplinkInfo
		for _, c := range codes[i:min(i+UplinkRows, len(codes))] {
			list = append(list, UplinkInfo{Name: "wwan" + c.code[:2], IP: "10.64.12.7", Kind: "modem", Label: "Telefónica Empresas",
				InUse: true, TxKbps: 2400, Health: c.health, HealthCode: c.code})
		}
		views = append(views, renderView{"connections-" + codes[i].code, func(h *harness) {
			pageApp(h, "uplinks", func(a *AppSnapshot) {
				a.Status, a.StatusAPI = &StatusInfo{Transport: "srtla", HasUplinks: true, Uplinks: list}, Yes
			})
		}})
	}
	checkRenders(t, views)
	for _, code := range Languages {
		tx := Lang(code)
		for _, c := range codes {
			if tx.Stream.UplinkHealth[c.code] == "" {
				t.Errorf("%s: no text for %s", code, c.code)
			}
		}
	}
}

// A tap on the dots opens Settings › Streaming › Connections; without dots that area does nothing
func TestLinkDotsTapOpensConnections(t *testing.T) {
	h, _ := drawHarness(t)
	liveHeader(h, fiveDots(), true)
	h.u.Dirty = true
	h.u.Tick()
	z := h.u.linksZone
	if z.W == 0 || z.H == 0 {
		t.Fatal("no zone for the dots")
	}
	x, y := (float64(z.X)+float64(z.W)/2)/1024, (float64(z.Y)+float64(z.H)/2)/600
	h.u.OnTouch("down", x, y, 0)
	h.u.OnTouch("up", x, y, 0)
	if !h.u.settingsOpen || h.u.settingsPage != "uplinks" {
		t.Fatalf("settings %v, page %q", h.u.settingsOpen, h.u.settingsPage)
	}

	h2, _ := drawHarness(t)
	liveHeader(h2, nil, false)
	h2.u.Dirty = true
	h2.u.Tick()
	if h2.u.linksZone.W != 0 {
		t.Fatalf("a zone without dots: %+v", h2.u.linksZone)
	}
	h2.u.OnTouch("down", x, y, 0)
	h2.u.OnTouch("up", x, y, 0)
	if h2.u.settingsOpen {
		t.Fatal("the header's middle opened Settings without dots")
	}
}

// Locked, the dots do nothing (like the gear)
func TestLinkDotsLocked(t *testing.T) {
	h, _ := drawHarness(t)
	liveHeader(h, fiveDots(), true)
	h.setLocked(true)
	h.u.Dirty = true
	h.u.Tick()
	z := h.u.linksZone
	x, y := (float64(z.X)+float64(z.W)/2)/1024, (float64(z.Y)+float64(z.H)/2)/600
	h.u.OnTouch("down", x, y, 0)
	h.u.OnTouch("up", x, y, 0)
	if h.u.settingsOpen {
		t.Fatal("locked, the dots opened Settings")
	}
}
