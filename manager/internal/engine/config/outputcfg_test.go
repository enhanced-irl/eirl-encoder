package config

import "testing"

var outputBins = map[string]bool{"/usr/local/bin/eirl-send": true, "/usr/local/bin/ristsender": true,
	"/usr/local/bin/srtla_send": true}

// confOf is an app file with these lines, read through EIRL_CONF.
func confOf(lines string) fake {
	return fake{env: map[string]string{"EIRL_CONF": confPath}, conf: &lines, exec: outputBins}
}

// The output protocols' keys (OUTPUTS.md §3): valid destinations land in their fields.
func TestOutputKeys(t *testing.T) {
	c := mustLoad(t, confOf("OUTPUT_PROTOCOL=rtmps\nOUTPUT_URL=rtmps://live-api-s.facebook.com:443/rtmp/\n"+
		"OUTPUT_KEY=FB-123?s_bl=1&a=Ab=\nOUTPUT_VCODEC=h265\nOUTPUT_SIZE=1280x720\nOUTPUT_FPS=half\nSRT_URL=\n"))
	if c.Transport != ProtoRTMPS || c.OutputURL != "rtmps://live-api-s.facebook.com:443/rtmp/" ||
		c.OutputKey.Reveal() != "FB-123?s_bl=1&a=Ab=" || c.VCodec != "h265" || c.OutputSize != "1280x720" || !c.OutputHalf {
		t.Fatalf("rtmps: %+v", *c)
	}
	if !c.HasDestination() || len(c.Secrets()) != 1 || c.ABRLatencyMs() != 2000 {
		t.Errorf("rtmps: destination %v, secrets %d, abr %d", c.HasDestination(), len(c.Secrets()), c.ABRLatencyMs())
	}
	if w, h := c.OutputDims(1920, 1080); w != 1280 || h != 720 {
		t.Errorf("dims %dx%d", w, h)
	}
	if w, h := c.OutputDims(1280, 720); w != 1280 || h != 720 {
		t.Errorf("never upscales: %dx%d", w, h)
	}

	c = mustLoad(t, confOf("OUTPUT_PROTOCOL=rist\nOUTPUT_URL=rist://[2001:db8::1]:8193\nRIST_PROFILE=main\n"+
		"OUTPUT_LATENCY=1500\nOUTPUT_PASSPHRASE=abcdefghijk\nOUTPUT_USER=enc\nOUTPUT_PASSWORD=pw.1\n"))
	if c.Transport != ProtoRIST || c.OutputLatencyMs != 1500 || c.OutputUser != "enc" || len(c.Secrets()) != 2 ||
		c.ABRLatencyMs() != 1500 {
		t.Fatalf("rist: %+v", *c)
	}

	c = mustLoad(t, confOf("OUTPUT_PROTOCOL=udp\nOUTPUT_URL=udp://239.0.0.1:5000\n"))
	if c.Transport != ProtoUDP || !c.HasDestination() {
		t.Fatalf("udp: %+v", *c)
	}

	// Legacy: no OUTPUT_PROTOCOL, the protocol from SRT_URL; empty keys take their defaults
	c = mustLoad(t, confOf("SRT_URL=srtla://eu1.relay.example.com:5001\nOUTPUT_VCODEC=\nOUTPUT_LATENCY=\n"))
	if c.Transport != ProtoSRTLA || c.VCodec != "h264" || c.OutputLatencyMs != 1000 || c.RISTProfile != "main" {
		t.Fatalf("legacy: %+v", *c)
	}
	if c = mustLoad(t, confOf("OUTPUT_PROTOCOL=rtmp\n")); c.HasDestination() {
		t.Error("rtmp without OUTPUT_URL has a destination")
	}
}

func TestOutputKeysInvalid(t *testing.T) {
	cases := []struct{ conf, key string }{
		{"OUTPUT_PROTOCOL=webrtc\n", "OUTPUT_PROTOCOL"},
		{"OUTPUT_PROTOCOL=rtmp\nOUTPUT_URL=rtmps://a.example.com/app\n", "OUTPUT_URL"},
		{"OUTPUT_PROTOCOL=rtmp\nOUTPUT_URL=rtmp://a.example.com\n", "OUTPUT_URL"},
		{"OUTPUT_PROTOCOL=rtmp\nOUTPUT_URL=rtmp://a.example.com:99999/app\n", "OUTPUT_URL"},
		{"OUTPUT_PROTOCOL=rtmp\nOUTPUT_URL=rtmp://a b/app\n", "OUTPUT_URL"},
		{"OUTPUT_KEY=two words\n", "OUTPUT_KEY"},
		{"OUTPUT_PROTOCOL=rist\nOUTPUT_URL=rist://h.example.com\n", "OUTPUT_URL"},
		{"OUTPUT_PROTOCOL=rist\nOUTPUT_URL=rist://h.example.com:1\nOUTPUT_USER=u\n", "OUTPUT_USER"},
		{"OUTPUT_PROTOCOL=rist\nOUTPUT_URL=rist://h.example.com:2\nRIST_PROFILE=simple\nOUTPUT_USER=u\nOUTPUT_PASSWORD=p\n", "RIST_PROFILE"},
		{"OUTPUT_PROTOCOL=rist\nOUTPUT_URL=rist://h.example.com:2\nRIST_PROFILE=simple\nOUTPUT_PASSPHRASE=abcdefghijk\n", "RIST_PROFILE"},
		{"OUTPUT_PROTOCOL=rist\nOUTPUT_URL=rist://h.example.com:5001\nRIST_PROFILE=simple\n", "OUTPUT_URL"},
		{"OUTPUT_PASSPHRASE=short\n", "OUTPUT_PASSPHRASE"},
		{"OUTPUT_LATENCY=10\n", "OUTPUT_LATENCY"},
		{"OUTPUT_PROTOCOL=udp\nOUTPUT_URL=srt://h:1\n", "OUTPUT_URL"},
		{"OUTPUT_PROTOCOL=srtla\nSRT_URL=srt://h.example.com:1\n", "OUTPUT_PROTOCOL"},
		{"OUTPUT_PROTOCOL=srt\nSRT_URL=srtla://h.example.com:1\n", "OUTPUT_PROTOCOL"},
		{"OUTPUT_VCODEC=av1\n", "OUTPUT_VCODEC"},
		{"OUTPUT_SIZE=3840x2160\n", "OUTPUT_SIZE"},
		{"OUTPUT_FPS=quarter\n", "OUTPUT_FPS"},
	}
	for _, c := range cases {
		_, err := load(t, confOf(c.conf))
		wantInvalid(t, err, c.key)
	}
	// A sender that is not installed does not stop the engine: its output fails instead
	f := confOf("OUTPUT_PROTOCOL=rtmp\nOUTPUT_URL=rtmp://a.example.com/app\n")
	f.exec = nil
	if _, err := load(t, f); err != nil {
		t.Errorf("missing eirl-send: %v", err)
	}
}

// output_config's rule: only the output's keys are taken live.
func TestSameDeviceAndWithOutput(t *testing.T) {
	cur := mustLoad(t, confOf("SRT_URL=srt://a.example.com:5000\nSOURCE=hdmi\nVBITRATE=6000\n"))
	cur.SRTStats = "" // what main's PrepareRunDir may have changed stays
	next := mustLoad(t, confOf("OUTPUT_PROTOCOL=rtmp\nOUTPUT_URL=rtmp://b.example.com/app\nOUTPUT_KEY=k\nSOURCE=hdmi\n"+
		"VBITRATE=4000\nOUTPUT_VCODEC=h265\nOUTPUT=off\n"))
	if !cur.SameDevice(next) {
		t.Fatal("only output keys changed")
	}
	x := cur.WithOutput(next)
	if x.Transport != ProtoRTMP || x.OutputKey.Reveal() != "k" || x.VBitrate != 4000 || x.VCodec != "h265" ||
		x.ConfSHA != next.ConfSHA || x.SRTStats != "" || x.OutputAtBoot != cur.OutputAtBoot || x.Source != "hdmi" {
		t.Errorf("with output: %+v", *x)
	}
	if cur.Transport != ProtoSRT {
		t.Error("WithOutput changed the original")
	}
	other := mustLoad(t, confOf("SRT_URL=srt://a.example.com:5000\nSOURCE=usb\n"))
	if cur.SameDevice(other) {
		t.Error("SOURCE changed: a restart")
	}
}
