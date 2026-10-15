package rtmpsend

import (
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		raw, key string
		tls      bool
		host     string
		port     int
		lib      string // what the RTMP library gets (the key as fragment)
		err      bool
	}{
		{raw: "rtmp://live.twitch.tv/app", key: "live_1", host: "live.twitch.tv", port: 1935,
			lib: "rtmp://live.twitch.tv/app#live_1"},
		{raw: "rtmps://a.rtmps.youtube.com:443/live2", key: "k", tls: true, host: "a.rtmps.youtube.com", port: 443,
			lib: "rtmps://a.rtmps.youtube.com:443/live2#k"},
		{raw: "rtmps://live-api-s.facebook.com:443/rtmp/", key: "FB-1?s_bl=1", tls: true,
			host: "live-api-s.facebook.com", port: 443, lib: "rtmps://live-api-s.facebook.com:443/rtmp#FB-1?s_bl=1"},
		{raw: "rtmps://ingest.example.com/app", key: "k", tls: true, host: "ingest.example.com", port: 443,
			lib: "rtmps://ingest.example.com/app#k"},
		{raw: "rtmp://[2001:db8::1]:1936/live/sub", key: "k", host: "2001:db8::1", port: 1936,
			lib: "rtmp://[2001:db8::1]:1936/live/sub#k"},
		{raw: "rtmp://10.0.0.5/live/stream", host: "10.0.0.5", port: 1935, lib: "rtmp://10.0.0.5/live#stream"},
		{raw: "rtmp://10.0.0.5/live", err: true},              // no key and no stream name
		{raw: "rtmp://10.0.0.5", key: "k", err: true},         // no application
		{raw: "srt://10.0.0.5:5000", key: "k", err: true},     // another protocol
		{raw: "rtmp://u:p@10.0.0.5/app", key: "k", err: true}, // credentials
		{raw: "rtmp://10.0.0.5/app#x", key: "k", err: true},
		{raw: "rtmp://10.0.0.5:0/app", key: "k", err: true},
		{raw: "rtmp://10.0.0.5:99999/app", key: "k", err: true},
		{raw: "rtmp:///app", key: "k", err: true},
		{raw: "::bad", key: "k", err: true},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.raw, c.key)
		if c.err {
			if err == nil {
				t.Errorf("%s: no error", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		if got.TLS != c.tls || got.Host != c.host || got.Port != c.port || got.url.String() != c.lib {
			t.Errorf("%s: got tls=%v %s %d %s", c.raw, got.TLS, got.Host, got.Port, got.url)
		}
	}
}

func TestParseTargetErrorsHideKey(t *testing.T) {
	for _, raw := range []string{"rtmp://10.0.0.5", "bogus://x/y", "rtmp://10.0.0.5:0/a"} {
		if _, err := ParseTarget(raw, "secret-key"); err == nil || strings.Contains(err.Error(), "secret-key") {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
