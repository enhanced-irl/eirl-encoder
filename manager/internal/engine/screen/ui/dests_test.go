package ui

import (
	"strings"
	"testing"
	"time"
)

// The app's saved destinations (OUTPUTS.md §9.3): the cloud's over SRTLA, Twitch over RTMP (in use) and a RIST one.
func destsApp() *DestList {
	d := func(id, name, tmpl, proto, url string) DestInfo {
		return DestInfo{ID: id, Name: name, Template: tmpl, Protocol: proto, URL: url, Mode: "caller", LatencyMs: 2000,
			VideoCodec: "h264", VideoKbps: 6000, VideoMinKbps: 1000, ABR: true, AudioKbps: 128, OutputFps: "full"}
	}
	eirl := d("eirl", "EnhancedIRL", "eirl", "srtla", "srtla://eu1.relay.enhancedirl.com:5001")
	eirl.Managed, eirl.StreamIDSet, eirl.VideoCodec = true, true, "h265"
	tw := d("d-3f9a1c2b", "Twitch", "twitch", "rtmp", "rtmp://live.twitch.tv/app")
	tw.Active, tw.KeySet = true, true
	rist := d("d-7c21e0a4", "Plató RIST", "custom", "rist", "rist://203.0.113.40:8200")
	rist.LatencyMs, rist.RistProfile, rist.PassphraseSet = 1000, "main", true
	return &DestList{Active: tw.ID, ManagedBy: "local", Destinations: []DestInfo{eirl, tw, rist},
		Templates: []DestTemplate{
			{ID: "eirl", Protocols: []string{"srtla", "srt"}, Codecs: []string{"h265", "h264"}},
			{ID: "twitch", Protocols: []string{"rtmp", "rtmps"}, URL: "rtmp://live.twitch.tv/app", Codecs: []string{"h264"},
				MaxVideoKbps: 6000, KeyRequired: true},
			{ID: "youtube", Protocols: []string{"rtmps", "rtmp"}, URL: "rtmps://a.rtmps.youtube.com:443/live2",
				Codecs: []string{"h264", "h265"}, MaxVideoKbps: 20000, KeyRequired: true},
			{ID: "custom", Protocols: []string{"srt", "srtla", "rtmp", "rtmps", "rist", "udp"},
				Codecs: []string{"h264", "h265"}, MaxVideoKbps: 20000}}}
}

// destsHarness: Settings open on Stream → Destination with the app's saved destinations.
func destsHarness(t *testing.T) (*harness, *fakeApp) {
	t.Helper()
	h, app := pagesHarness(t, "dest")
	app.snap.Dests, app.snap.DestsAPI = destsApp(), Yes
	h.u.Tick()
	return h, app
}

func (h *harness) streamingOn() {
	st := *h.u.st
	st.Output.Mode = "on"
	h.u.SetStatus(&st)
}

func TestDestsListActivate(t *testing.T) {
	h, app := destsHarness(t)
	if !app.watch().Dest {
		t.Fatalf("watch %+v", app.watch())
	}
	keys := h.u.settingsKeys(&NetSnapshot{})
	for _, k := range []string{"s_dl_0", "s_dlo_0", "s_dl_2", "s_dl_add"} {
		if !contains(keys, k) {
			t.Fatalf("no %s in %v", k, keys)
		}
	}
	if contains(keys, "s_dl_3") || contains(keys, "s_dl_prev") || contains(keys, "s_dest_url") {
		t.Fatalf("%v", keys)
	}
	// Not streaming: a touch activates it at once
	h.press("s_dl_2", 0.1)
	a := app.lastDid()
	if a.What != "dest_activate" || a.ID != "d-7c21e0a4" || a.Body["apply"] != true || h.u.dl.ed != nil {
		t.Fatalf("%+v", a)
	}
	// Streaming: the output restarts, a second touch
	app.did = nil
	h.streamingOn()
	h.press("s_dl_0", 0.1)
	if len(app.did) != 0 || !h.u.confirming("dl_act:eirl") {
		t.Fatal("activated at the first touch while streaming")
	}
	h.press("s_dl_0", 0.1)
	if a := app.lastDid(); a.What != "dest_activate" || a.ID != "eirl" {
		t.Fatalf("%+v", app.did)
	}
	// The active one's row opens its page; so does a row's «›»
	h.press("s_dl_1", 0.1)
	if h.u.dl.ed == nil || h.u.dl.ed.id != "d-3f9a1c2b" {
		t.Fatal("the active one's page did not open")
	}
	if title, back, ok := h.u.destsTitle(); !ok || !back || title != "Twitch" {
		t.Fatalf("%q %v %v", title, back, ok)
	}
	h.press("s_back", 0.1)
	if h.u.dl.ed != nil || app.dropped == 0 {
		t.Fatal("← did not go back to the list (dropping what was revealed)")
	}
	h.press("s_dlo_2", 0.1)
	if h.u.dl.ed == nil || h.u.dl.ed.id != "d-7c21e0a4" {
		t.Fatal("«›» did not open its page")
	}
	// Closing Settings closes it
	h.press("s_close", 0.1)
	if h.u.dl.ed != nil {
		t.Fatal("the destination's page stays open")
	}
}

func TestDestsAddAndCreate(t *testing.T) {
	h, app := destsHarness(t)
	h.press("s_dl_add", 0.1)
	if h.u.settingsView != "pick" || h.u.chooser == nil {
		t.Fatal("no template picker")
	}
	var ids []string
	for _, o := range h.u.chooser.opts {
		ids = append(ids, o.ID)
	}
	if strings.Join(ids, ",") != "twitch,youtube,custom" {
		t.Fatalf("templates %v (the cloud's is not creatable)", ids)
	}
	h.u.pickerTouch("s_pick_1", "youtube", true)
	ed := h.u.dl.ed
	if ed == nil || ed.id != "" || ed.tmpl != "youtube" {
		t.Fatalf("%+v", ed)
	}
	n, _ := h.u.deNow(&app.snap)
	if n.Protocol != "rtmps" || n.URL != "rtmps://a.rtmps.youtube.com:443/live2" || n.Name != "YouTube" ||
		n.VideoCodec != "h264" || n.VideoKbps != 6000 {
		t.Fatalf("%+v", n)
	}
	keys := h.u.settingsKeys(&NetSnapshot{})
	if !contains(keys, "s_de_key") || !contains(keys, "s_de_proto") || !contains(keys, "s_de_save1") ||
		contains(keys, "s_de_apply") || contains(keys, "s_de_delete") || contains(keys, "s_de_sid") {
		t.Fatalf("%v", keys)
	}
	// The key on the secret keyboard (only the allowed characters)
	h.press("s_de_key", 0.1)
	if h.u.kb == nil || !h.u.kb.secret() {
		t.Fatal("no secret keyboard")
	}
	for _, c := range "liveabc" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if string(h.u.dl.ed.secrets["key"]) != "liveabc" {
		t.Fatalf("%q", h.u.dl.ed.secrets["key"])
	}
	h.press("s_de_save1", 0.1)
	a := app.lastDid()
	if a.What != "dest_create" || a.ID != "" || a.Body["template"] != "youtube" || a.Body["protocol"] != "rtmps" ||
		a.Body["url"] != "rtmps://a.rtmps.youtube.com:443/live2" || a.Body["name"] != "YouTube" ||
		a.Body["videoBitrateKbps"] != 6000 || a.Body["videoCodec"] != "h264" || a.Body["adaptiveBitrate"] != true {
		t.Fatalf("%+v", a.Body)
	}
	if _, ok := a.Body["latencyMs"]; ok {
		t.Fatal("an RTMP destination sends a latency")
	}
	if _, ok := a.Body["key"]; ok || app.secrets[len(app.secrets)-1]["key"] != "liveabc" {
		t.Fatalf("the key went as %+v / %+v", a.Body, app.secrets)
	}
	// The app created it: back to the list, which says how to use it; the typed key is wiped
	typed := h.u.dl.ed.secrets["key"]
	app.snap.Last = &AppResult{What: "dest_create", OK: true, ID: "d-11223344", At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	drawOnce(h)
	if h.u.dl.ed != nil || !allZero(typed) {
		t.Fatal("still on the new destination's page, or the key stays")
	}
	if note, _ := h.u.destListNote(&app.snap, 1); note != h.u.Tx.Dests.Created {
		t.Fatalf("%q", note)
	}
}

func TestDestsUpdateAndApply(t *testing.T) {
	h, app := destsHarness(t)
	// A saved one not in use: only «Save»; RIST's buffer and user
	h.press("s_dlo_2", 0.1)
	keys := h.u.settingsKeys(&NetSnapshot{})
	for _, k := range []string{"s_de_prof", "s_de_buf_m", "s_de_rpass", "s_de_user", "s_de_pw", "s_de_rpass_eye", "s_de_delete"} {
		if !contains(keys, k) {
			t.Fatalf("no %s in %v", k, keys)
		}
	}
	h.press("s_de_buf_p", 0.1)
	h.press("s_de_user", 0.1)
	for _, c := range "cam" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	h.press("s_de_rpass", 0.1)
	h.tapKey("OK", 0.1) // empty: the saved passphrase is removed
	keys = h.u.settingsKeys(&NetSnapshot{})
	if !contains(keys, "s_de_save1") || contains(keys, "s_de_apply") || contains(keys, "s_de_delete") {
		t.Fatalf("%v", keys)
	}
	h.press("s_de_save1", 0.1)
	a := app.lastDid()
	if a.What != "dest_update" || a.ID != "d-7c21e0a4" || a.Body["latencyMs"] != 1100 || a.Body["username"] != "cam" ||
		a.Body["apply"] != false || len(a.Body) != 4 {
		t.Fatalf("%+v", a)
	}
	if v, ok := a.Body["passphrase"]; !ok || v != nil {
		t.Fatal("the passphrase is not removed (null)")
	}
	// The app saved it: the draft goes
	app.snap.Last = &AppResult{What: "dest_update", OK: true, At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	drawOnce(h)
	if h.u.dl.ed == nil || h.u.dl.ed.edited() {
		t.Fatal("the draft stays after the app saved it")
	}
	// The one in use: «Save and apply», a second touch while streaming
	h.press("s_back", 0.1)
	h.press("s_dlo_1", 0.1)
	h.press("s_de_sec_video", 0.1)
	h.press("s_de_aud_p", 0.1)
	h.streamingOn()
	app.did = nil
	h.press("s_de_apply", 0.1)
	if len(app.did) != 0 || !h.u.confirming("de_apply") {
		t.Fatal("applied at the first touch while streaming")
	}
	h.press("s_de_apply", 0.1)
	if a := app.lastDid(); a.What != "dest_update" || a.Body["apply"] != true || a.Body["audioBitrateKbps"] != 160 {
		t.Fatalf("%+v", a)
	}
}

func TestDestsTemplateBounds(t *testing.T) {
	h, app := destsHarness(t)
	h.press("s_dlo_1", 0.1) // Twitch: H.264 only, 6000 kbps at most
	h.press("s_de_sec_video", 0.1)
	keys := h.u.settingsKeys(&NetSnapshot{})
	if contains(keys, "s_de_codec") || !contains(keys, "s_de_size") || !contains(keys, "s_de_fps") {
		t.Fatalf("%v", keys)
	}
	h.press("s_de_vid_p", 0.1)
	if n, _ := h.u.deNow(&app.snap); n.VideoKbps != 6000 || h.u.dl.ed.edited() {
		t.Fatalf("over the template's maximum: %+v", n)
	}
	for range 30 {
		h.press("s_de_vid_m", 0.1)
	}
	if n, _ := h.u.deNow(&app.snap); n.VideoKbps != VideoMin || n.VideoMinKbps > n.VideoKbps {
		t.Fatalf("%+v", n)
	}
	// The output size and frame rate: their pickers
	h.press("s_de_size", 0.1)
	h.u.pickerTouch("s_pick_2", "960x540", true)
	h.press("s_de_fps", 0.1)
	h.u.pickerTouch("s_pick_1", "half", true)
	if n, _ := h.u.deNow(&app.snap); n.OutputSize != "960x540" || n.OutputFps != "half" {
		t.Fatalf("%+v", n)
	}
	h.press("s_de_size", 0.1)
	h.u.pickerTouch("s_pick_0", "program", true)
	if _, ok := h.u.dl.ed.fields["outputSize"]; ok {
		t.Fatal("back to the program's size is still a change")
	}
	// The protocol: the template's; RTMPS keeps the server with its scheme
	h.press("s_de_sec_conn", 0.1)
	h.press("s_de_proto", 0.1)
	if len(h.u.chooser.opts) != 2 {
		t.Fatalf("%+v", h.u.chooser.opts)
	}
	h.u.pickerTouch("s_pick_1", "rtmps", true)
	if n, _ := h.u.deNow(&app.snap); n.Protocol != "rtmps" || n.URL != "rtmps://live.twitch.tv/app" {
		t.Fatalf("%+v", n)
	}
}

func TestAdaptURL(t *testing.T) {
	custom := DestTemplate{ID: "custom"}
	yt := DestTemplate{URL: "rtmps://a.rtmps.youtube.com:443/live2"}
	for _, c := range []struct {
		url, from, to string
		t             DestTemplate
		want          string
	}{
		{"srt://relay:5000", "srt", "srtla", custom, "srtla://relay:5000"},
		{"rtmp://host:1935/app", "rtmp", "rtmps", custom, "rtmps://host:443/app"},
		{"rtmps://host:443/app/", "rtmps", "rtmp", custom, "rtmp://host:1935/app/"},
		{"rtmp://x/live2", "rtmp", "rtmps", yt, "rtmps://a.rtmps.youtube.com:443/live2"},
		{"srt://relay:5000", "srt", "rist", custom, ""},
		{"", "rtmp", "rtmps", custom, ""},
	} {
		if got := adaptURL(c.url, c.from, c.to, c.t); got != c.want {
			t.Errorf("%s %s→%s: %q, want %q", c.url, c.from, c.to, got, c.want)
		}
	}
	if hostOf("rtmp://live.twitch.tv/app") != "live.twitch.tv" || hostOf("srt://h:1?streamid=x") != "h:1" {
		t.Error("hostOf")
	}
}

func TestDestsManagedAndDelete(t *testing.T) {
	h, app := destsHarness(t)
	// The cloud's: its eyes, nothing to change
	h.press("s_dlo_0", 0.1)
	keys := h.u.pageKeys("dest", &NetSnapshot{})
	want := "s_back,s_de_sec_conn,s_de_sec_video,s_de_sid_eye,s_de_pass_eye"
	if strings.Join(keys, ",") != want {
		t.Fatalf("%v", keys)
	}
	h.press("s_de_sec_video", 0.1)
	if keys := h.u.pageKeys("dest", &NetSnapshot{}); len(keys) != 3 {
		t.Fatalf("%v", keys)
	}
	h.press("s_back", 0.1)
	// The one in use is not deleted; another one, with a second touch
	h.press("s_dlo_1", 0.1)
	if contains(h.u.pageKeys("dest", &NetSnapshot{}), "s_de_delete") {
		t.Fatal("the one in use can be deleted")
	}
	h.press("s_back", 0.1)
	h.press("s_dlo_2", 0.1)
	h.press("s_de_delete", 0.1)
	if len(app.did) != 0 || !h.u.confirming("de_delete") {
		t.Fatal("deleted at the first touch")
	}
	h.press("s_de_delete", 0.1)
	if a := app.lastDid(); a.What != "dest_delete" || a.ID != "d-7c21e0a4" || h.u.dl.ed != nil {
		t.Fatalf("%+v", app.did)
	}
}

func TestDestsEyes(t *testing.T) {
	h, app := destsHarness(t)
	var drawn []string
	drawnText = func(s string) { drawn = append(drawn, s) }
	defer func() { drawnText = nil }()
	app.snap.DestSecrets = &DestSecrets{ID: "d-7c21e0a4", Passphrase: "PassDeVerdad-01"}
	h.press("s_dlo_2", 0.1)
	drawOnce(h)
	if strings.Contains(strings.Join(drawn, "|"), "PassDeVerdad") {
		t.Fatal("the passphrase is drawn with its eye closed")
	}
	// Another destination's secrets are not this one's: the eye asks for its own
	app.snap.DestSecrets = &DestSecrets{ID: "eirl", Passphrase: "OtraDeOtro-0123"}
	h.press("s_de_rpass_eye", 0.1)
	if a := app.lastDid(); a.What != "dest_secrets" || a.ID != "d-7c21e0a4" {
		t.Fatalf("%+v", app.did)
	}
	drawn = nil
	drawOnce(h)
	if strings.Contains(strings.Join(drawn, "|"), "OtraDeOtro") {
		t.Fatal("another destination's passphrase is shown")
	}
	app.snap.DestSecrets = &DestSecrets{ID: "d-7c21e0a4", Passphrase: "PassDeVerdad-01"}
	drawn = nil
	drawOnce(h)
	if !strings.Contains(strings.Join(drawn, "|"), "PassDeVerdad-01") {
		t.Fatalf("not shown with its eye open: %q", drawn)
	}
	// A typed one shows only behind its eye, and goes with the page
	h.press("s_de_pw", 0.1)
	for _, c := range "Secreto" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	typed := h.u.dl.ed.secrets["password"]
	drawn = nil
	drawOnce(h)
	if strings.Contains(strings.Join(drawn, "|"), "Secreto") {
		t.Fatal("the typed password is drawn with its eye closed")
	}
	h.press("s_back", 0.1)
	if !allZero(typed) || h.u.revealed("s_de_rpass_eye") {
		t.Fatal("the typed password or an open eye stays after leaving")
	}
}

// An older app (no GET /destinations): the single destination, as before.
func TestDestsOlderApp(t *testing.T) {
	h, app := pagesHarness(t, "dest")
	app.snap.Dest, app.snap.DestAPI, app.snap.DestsAPI = destApp(), Yes, No
	h.u.Tick()
	keys := h.u.settingsKeys(&NetSnapshot{})
	if !contains(keys, "s_dest_url") || contains(keys, "s_dl_add") {
		t.Fatalf("%v", keys)
	}
	if _, _, ok := h.u.destsTitle(); ok {
		t.Fatal("a sub-view without saved destinations")
	}
}

func TestDestsLayout(t *testing.T) {
	groups := map[string][]string{
		"list": {"s_dl_add", "s_dl_prev", "s_dl_next"},
		"conn-srt": {"s_de_sec_conn", "s_de_sec_video", "s_de_name", "s_de_proto", "s_de_url", "s_de_sid",
			"s_de_sid_eye", "s_de_pass", "s_de_pass_eye", "s_de_lat_m", "s_de_lat_p", "s_de_apply", "s_de_save"},
		"conn-rtmp": {"s_de_key", "s_de_key_eye", "s_de_save1", "s_de_delete"},
		"conn-rist": {"s_de_prof", "s_de_buf_m", "s_de_buf_p", "s_de_rpass", "s_de_rpass_eye", "s_de_user", "s_de_pw",
			"s_de_pw_eye", "s_de_delete"},
		"video": {"s_de_codec", "s_de_size", "s_de_vid_m", "s_de_vid_p", "s_de_vmin_m", "s_de_vmin_p", "s_de_abr",
			"s_de_fps", "s_de_aud_m", "s_de_aud_p", "s_de_save", "s_de_apply"},
	}
	for i := range DestListRows {
		n := itoa(i)
		groups["list"] = append(groups["list"], "s_dl_"+n)
	}
	for _, sz := range [][2]int{{1024, 600}, {1280, 720}, {1920, 1080}} {
		lay := NewLayout(sz[0], sz[1], 4)
		box := lay.SettingsPage
		for name, keys := range groups {
			for i, k := range keys {
				r, ok := lay.Buttons[k]
				if !ok {
					t.Fatalf("%v: no %s", sz, k)
				}
				if r.X < box.X || r.Y < box.Y || r.X+r.W > box.X+box.W || r.Y+r.H > box.Y+box.H {
					t.Errorf("%v %s: %s %v outside %v", sz, name, k, r, box)
				}
				if r.H < 48 || r.W < 48 {
					t.Errorf("%v %s: %s %v too small", sz, name, k, r)
				}
				for _, k2 := range keys[i+1:] {
					if overlaps(r, lay.Buttons[k2]) {
						t.Errorf("%v %s: %s overlaps %s", sz, name, k, k2)
					}
				}
			}
		}
		// A row's «›» is inside its row (tried first), and at least 48 px
		for i := range DestListRows {
			o, row := lay.Buttons["s_dlo_"+itoa(i)], lay.Buttons["s_dl_"+itoa(i)]
			if o.X < row.X || o.Y < row.Y || o.X+o.W > row.X+row.W || o.Y+o.H > row.Y+row.H || o.H < 48 {
				t.Errorf("%v: «›» %v outside its row %v", sz, o, row)
			}
		}
	}
}

func TestRenderDests(t *testing.T) {
	dests := func(mod func(h *harness)) func(h *harness) {
		return func(h *harness) {
			pageApp(h, "dest", func(a *AppSnapshot) { a.Dests, a.DestsAPI = destsApp(), Yes })
			if mod != nil {
				mod(h)
			}
		}
	}
	open := func(id, section string) func(h *harness) {
		return func(h *harness) {
			h.u.openDest(id)
			h.u.dl.ed.section = section
		}
	}
	checkRenders(t, []renderView{
		{"dests", dests(nil)},
		{"dests-empty", func(h *harness) {
			pageApp(h, "dest", func(a *AppSnapshot) {
				a.Dests, a.DestsAPI = destsApp(), Yes
				a.Dests.Destinations = nil
			})
		}},
		{"dests-confirm", dests(func(h *harness) { h.u.confirmTap("dl_act:d-7c21e0a4") })},
		{"dests-templates", dests(func(h *harness) {
			app := h.u.app().AppSnapshot()
			h.u.openTemplatePicker(&app)
		})},
		{"dests-twitch", dests(open("d-3f9a1c2b", "conn"))},
		{"dests-rist", dests(open("d-7c21e0a4", "conn"))},
		{"dests-video", dests(open("d-7c21e0a4", "video"))},
		{"dests-managed", dests(open("eirl", "conn"))},
		{"dests-delete", dests(func(h *harness) {
			open("d-7c21e0a4", "conn")(h)
			h.u.confirmTap("de_delete")
		})},
		{"dests-edited", dests(func(h *harness) {
			open("d-3f9a1c2b", "video")(h)
			h.u.pageTouch("s_de_abr", "", false)
		})},
		{"dests-new", dests(func(h *harness) {
			app := h.u.app().AppSnapshot()
			h.u.openNewDest(&app, "custom")
		})},
		{"dests-new-rist", dests(func(h *harness) {
			app := h.u.app().AppSnapshot()
			h.u.openNewDest(&app, "custom")
			h.u.deSet(&app, "protocol", "rist")
		})},
	})
}

// The header words every protocol, and the codec goes with the rate while live.
func TestHeaderProtocolAndCodec(t *testing.T) {
	h, _ := drawHarness(t)
	for proto, want := range map[string]string{"rtmp": "RTMP", "rtmps": "RTMPS", "rist": "RIST", "udp": "UDP", "srt": "SRT",
		"": "SRT"} {
		if got := h.u.linkText(proto, 0); got != want {
			t.Errorf("%q: %q", proto, got)
		}
	}
	var shown []string
	drawnText = func(s string) { shown = append(shown, s) }
	defer func() { drawnText = nil }()
	st := *h.u.st
	kbps, rtt := 4200, 40
	st.Output = OutputStatus{Mode: "on", State: "on", SentKbps: &kbps, RTTMs: &rtt, Protocol: "rtmps", Codec: "h265"}
	h.u.SetStatus(&st)
	h.u.LinkInfo = func() (string, int) { return "", 0 } // an engine that does not say: the state's protocol
	h.u.Dirty = true
	h.u.Tick()
	found := false
	for _, s := range shown {
		found = found || strings.HasPrefix(s, "H.265 · 4,2 Mbps · RTMPS")
	}
	if !found {
		t.Fatalf("the header: %q", shown)
	}
	for _, lang := range Languages {
		for _, r := range []string{"tls_failed", "clock_not_synced"} {
			if Lang(lang).Reasons[r] == "" {
				t.Errorf("%s: no reason %s", lang, r)
			}
		}
	}
}

// A template with a server per protocol gives that one when the protocol changes (YouTube's hosts differ).
func TestAdaptURLTemplateServers(t *testing.T) {
	yt := DestTemplate{ID: "youtube", URL: "rtmps://a.rtmps.youtube.com:443/live2",
		URLs: map[string]string{"rtmps": "rtmps://a.rtmps.youtube.com:443/live2", "rtmp": "rtmp://a.rtmp.youtube.com/live2"}}
	if got := adaptURL("rtmps://a.rtmps.youtube.com:443/live2", "rtmps", "rtmp", yt); got != "rtmp://a.rtmp.youtube.com/live2" {
		t.Errorf("youtube rtmp: %q", got)
	}
	// A server typed by hand keeps its host
	if got := adaptURL("rtmps://my.example.com:443/app", "rtmps", "rtmp", yt); got != "rtmp://my.example.com:1935/app" {
		t.Errorf("own server: %q", got)
	}
}
