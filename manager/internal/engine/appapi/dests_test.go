package appapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The saved destinations (OUTPUTS.md §9.3) against the fake app.

func destView(id, name, tmpl, proto, url string, active bool) map[string]any {
	return map[string]any{"id": id, "name": name, "template": tmpl, "managed": id == "eirl", "active": active,
		"protocol": proto, "url": url, "urlInvalid": false, "mode": "caller", "latencyMs": 2000,
		"streamId": map[string]any{"set": id == "eirl", "masked": "publish:a1…"}, "passphrase": map[string]any{"set": false},
		"key": map[string]any{"set": proto == "rtmp", "masked": "live…wXyZ"}, "ristProfile": "main", "username": "",
		"password": map[string]any{"set": false}, "videoCodec": "h264", "videoBitrateKbps": 6000,
		"videoMinBitrateKbps": 1000, "adaptiveBitrate": true, "audioBitrateKbps": 128, "outputSize": "", "outputFps": "full"}
}

func destsBody() map[string]any {
	bad := destView("../etc", "x", "custom", "srt", "srt://x:1", false) // an id that does not fit: left out
	weird := destView("d-0000aaaa", strings.Repeat("n", 40), "toaster", "gopher", "rist://h:1", false)
	weird["videoCodec"], weird["outputSize"], weird["outputFps"], weird["username"] = "vp9", "4096x2160", "double", "a b"
	return map[string]any{"active": "d-3f9a1c2b", "managedBy": "local", "pendingRestart": false,
		"destinations": []any{
			destView("eirl", "EnhancedIRL", "eirl", "srtla", "srtla://eu1.relay.enhancedirl.com:5001", false),
			destView("d-3f9a1c2b", "Twitch", "twitch", "rtmp", "rtmp://live.twitch.tv/app", true),
			bad, weird,
			destView("d-3f9a1c2b", "Twitch again", "twitch", "rtmp", "rtmp://x/app", false), // a repeated id
		},
		"templates": []any{
			map[string]any{"id": "twitch", "protocols": []any{"rtmp", "rtmps", "rtmp", "gopher"}, "url": "rtmp://live.twitch.tv/app",
				"codecs": []any{"h264", "vp9"}, "maxVideoKbps": 6000, "keyRequired": true},
			map[string]any{"id": "custom", "protocols": []any{"srt", "srtla", "rtmp", "rtmps", "rist", "udp"},
				"codecs": []any{"h264", "h265"}, "maxVideoKbps": 20000, "keyRequired": false},
			map[string]any{"id": "myspace", "protocols": []any{"rtmp"}},
			map[string]any{"id": "kick", "protocols": []any{}}}}
}

func TestDestsRead(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /destinations", 200, destsBody())
	d.PollDest()
	s := d.AppSnapshot()
	l := s.Dests
	if s.DestsAPI != ui.Yes || l == nil || l.Active != "d-3f9a1c2b" || len(l.Destinations) != 3 {
		t.Fatalf("%+v %+v", s.DestsAPI, l)
	}
	if len(a.allSeen()) != 1 {
		t.Fatalf("the single destination asked too: %+v", a.allSeen())
	}
	tw := l.ByID("d-3f9a1c2b")
	if tw == nil || !tw.Active || tw.Protocol != "rtmp" || !tw.KeySet || tw.KeyMasked != "live…wXyZ" || tw.Name != "Twitch" ||
		tw.Template != "twitch" || tw.VideoCodec != "h264" || tw.OutputFps != "full" || tw.RistProfile != "main" {
		t.Fatalf("%+v", tw)
	}
	if e := l.ByID("eirl"); e == nil || !e.Managed || e.Active || e.Protocol != "srtla" || !e.StreamIDSet {
		t.Fatalf("%+v", e)
	}
	// What does not fit is left out field by field (the destination stays: its id is valid)
	w := l.ByID("d-0000aaaa")
	if w == nil || w.Name != "" || w.Template != "" || w.Protocol != "" || w.VideoCodec != "" || w.OutputSize != "" ||
		w.OutputFps != "" || w.Username != "" {
		t.Fatalf("%+v", w)
	}
	if len(l.Templates) != 2 || !reflect.DeepEqual(l.Templates[0], ui.DestTemplate{ID: "twitch",
		Protocols: []string{"rtmp", "rtmps"}, URL: "rtmp://live.twitch.tv/app", Codecs: []string{"h264"}, MaxVideoKbps: 6000,
		KeyRequired: true}) {
		t.Fatalf("%+v", l.Templates)
	}
	// At most 16; an active one that is not in the list is none
	many := []any{}
	for i := range 20 {
		many = append(many, destView("d-"+strings.Repeat("0", 6)+string(rune('a'+i/16))+string("0123456789abcdef"[i%16]),
			"n", "custom", "srt", "srt://x:1", false))
	}
	a.route("GET /destinations", 200, map[string]any{"active": "d-99999999", "destinations": many, "templates": []any{}})
	d.PollDest()
	if l := d.AppSnapshot().Dests; len(l.Destinations) != MaxDests || l.Active != "" {
		t.Fatalf("%d %q", len(l.Destinations), l.Active)
	}
}

// An older app without GET /destinations: the single destination, and the list is asked again only when the page is
// opened again.
func TestDestsOlderApp(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /destination", 200, destBody())
	d.PollDest()
	s := d.AppSnapshot()
	if s.DestsAPI != ui.No || s.Dests != nil || s.DestAPI != ui.Yes || s.Dest == nil {
		t.Fatalf("%+v %+v", s.DestsAPI, s.DestAPI)
	}
	n := len(a.allSeen())
	d.PollDest()
	if seen := a.allSeen()[n:]; len(seen) != 1 || seen[0].uri != "/api/v1/destination" {
		t.Fatalf("%+v", seen)
	}
	// The app is updated and the page opened again: the list
	a.route("GET /destinations", 200, destsBody())
	d.Watch(ui.AppWatch{})
	d.Watch(ui.AppWatch{Dest: true})
	d.PollDest()
	if s := d.AppSnapshot(); s.DestsAPI != ui.Yes || s.Dests == nil {
		t.Fatalf("%+v", s.DestsAPI)
	}
	// A passing failure keeps what there was
	a.route("GET /destinations", 503, map[string]any{"error": "busy"})
	d.PollDest()
	if s := d.AppSnapshot(); s.DestsAPI != ui.Yes || s.Dests == nil {
		t.Fatalf("%+v", s.DestsAPI)
	}
}

func TestDestsActions(t *testing.T) {
	a, d := newDevice(t)
	a.route("GET /destinations", 200, destsBody())
	d.PollDest()
	created := destView("d-11223344", "YouTube", "youtube", "rtmps", "rtmps://a.rtmps.youtube.com:443/live2", false)
	a.route("POST /destinations", 201, created)
	key := []byte(`live_k"1`)
	act := ui.AppAction{What: "dest_create", Body: map[string]any{"template": "youtube", "protocol": "rtmps",
		"name": "YouTube", "videoBitrateKbps": 6000}, Secrets: map[string][]byte{"key": key}}
	if !d.Do(act) {
		t.Fatal("not queued")
	}
	clear(key)
	q, _, _, _ := d.take()
	d.runAction(*q)
	a.mu.Lock()
	raw := string(a.raws[len(a.raws)-1])
	a.mu.Unlock()
	if raw != `{"key":"live_k\"1","name":"YouTube","protocol":"rtmps","template":"youtube","videoBitrateKbps":6000}` {
		t.Errorf("body %s", raw)
	}
	s := d.AppSnapshot()
	if !s.Last.OK || s.Last.ID != "d-11223344" || s.Dests.ByID("d-11223344") == nil || len(s.Dests.Destinations) != 4 {
		t.Fatalf("%+v %+v", s.Last, s.Dests)
	}
	// Update (a secret cleared is null), activate (the list comes back), the secrets of one, delete
	a.route("PUT /destinations/d-3f9a1c2b", 200, destView("d-3f9a1c2b", "Twitch", "twitch", "rtmps",
		"rtmps://live.twitch.tv/app", true))
	activated := destsBody()
	activated["active"] = "eirl"
	activated["restarted"] = true
	a.route("POST /destinations/eirl/activate", 200, activated)
	a.route("GET /destinations/d-3f9a1c2b/secrets", 200, map[string]any{"streamId": "", "passphrase": "", "key": "live_abc",
		"password": ""})
	a.route("DELETE /destinations/d-11223344", 204, nil)
	cases := []struct {
		act  ui.AppAction
		want seenReq
	}{
		{ui.AppAction{What: "dest_update", ID: "d-3f9a1c2b", Body: map[string]any{"protocol": "rtmps", "key": nil, "apply": true}},
			seenReq{"PUT", "/api/v1/destinations/d-3f9a1c2b", map[string]any{"protocol": "rtmps", "key": nil, "apply": true}}},
		{ui.AppAction{What: "dest_activate", ID: "eirl", Body: map[string]any{"apply": true}},
			seenReq{"POST", "/api/v1/destinations/eirl/activate", map[string]any{"apply": true}}},
		{ui.AppAction{What: "dest_secrets", ID: "d-3f9a1c2b"}, seenReq{"GET", "/api/v1/destinations/d-3f9a1c2b/secrets", nil}},
		{ui.AppAction{What: "dest_delete", ID: "d-11223344"}, seenReq{"DELETE", "/api/v1/destinations/d-11223344", nil}},
	}
	for _, c := range cases {
		r := run(t, d, c.act)
		if !r.OK {
			t.Errorf("%s: %+v", c.act.What, r)
		}
		if got := a.last(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.act.What, got, c.want)
		}
		s := d.AppSnapshot()
		switch c.act.What {
		case "dest_update":
			if dst := s.Dests.ByID("d-3f9a1c2b"); dst == nil || dst.Protocol != "rtmps" {
				t.Errorf("%+v", dst)
			}
		case "dest_activate":
			if s.Dests.Active != "eirl" || r.Restarted == nil || !*r.Restarted {
				t.Errorf("%+v %+v", s.Dests.Active, r)
			}
		case "dest_secrets":
			if s.DestSecrets == nil || s.DestSecrets.ID != "d-3f9a1c2b" || s.DestSecrets.Key != "live_abc" {
				t.Errorf("%+v", s.DestSecrets)
			}
		case "dest_delete":
			if s.Dests.ByID("d-11223344") != nil || s.DestSecrets != nil {
				t.Errorf("still there: %+v", s.Dests)
			}
		}
	}
	// A refusal: the app's field errors and code
	a.route("PUT /destinations/d-3f9a1c2b", 409, map[string]any{"error": "managed", "message": "la gestiona tu cuenta"})
	if r := run(t, d, ui.AppAction{What: "dest_update", ID: "d-3f9a1c2b", Body: map[string]any{"name": "x"}}); r.OK ||
		r.Status != 409 || r.Code != "managed" || r.Message != "la gestiona tu cuenta" {
		t.Errorf("%+v", r)
	}
	// A reveal queued before the page went is not kept
	d.Do(ui.AppAction{What: "dest_secrets", ID: "d-3f9a1c2b"})
	q, _, _, _ = d.take()
	d.DropSecrets()
	d.runAction(*q)
	if d.AppSnapshot().DestSecrets != nil {
		t.Fatal("a reveal from before DropSecrets came back")
	}
}
