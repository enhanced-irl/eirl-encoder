package config

import (
	"strconv"
	"strings"
)

// The output protocols (OUTPUTS.md §3).
const (
	ProtoSRT   = "srt"
	ProtoSRTLA = "srtla"
	ProtoRTMP  = "rtmp"
	ProtoRTMPS = "rtmps"
	ProtoRIST  = "rist"
	ProtoUDP   = "udp"
)

// validateOutput checks the keys of the output protocols and the encoding settings (OUTPUTS.md §3). The
// srt and srtla destinations keep the launcher's checks of SRT_URL (validate); here only that
// OUTPUT_PROTOCOL agrees with them. A sender that is not installed is not a reason to refuse the whole
// engine (unlike srtla_send's legacy check): the output fails with it (internal) and the rest runs.
func validateOutput(raw map[string]string) error {
	var err error
	re := func(k string, r interface{ MatchString(string) bool }) {
		if err == nil && !r.MatchString(raw[k]) {
			err = invalid(k, "")
		}
	}
	re("OUTPUT_PROTOCOL", reProtocol)
	re("OUTPUT_KEY", reOutputKey)
	re("OUTPUT_PASSPHRASE", rePassphrase)
	re("OUTPUT_USER", reRISTUser)
	re("OUTPUT_PASSWORD", reRISTPass)
	re("RIST_PROFILE", reRISTProf)
	re("OUTPUT_LATENCY", reOptUint5)
	re("OUTPUT_VCODEC", reVCodec)
	re("OUTPUT_SIZE", reOutputSize)
	re("OUTPUT_FPS", reOutputFPS)
	re("SEND_BIN", rePath)
	re("RIST_BIN", rePath)
	if err != nil {
		return err
	}
	if n, ok := bashArith(raw["RIST_LOCAL_PORT"]); !ok || n < 1 || n > 65535 {
		return invalid("RIST_LOCAL_PORT", "")
	}
	if n, _ := strconv.Atoi(raw["OUTPUT_LATENCY"]); n < 50 || n > 8000 {
		return invalid("OUTPUT_LATENCY", "between 50 and 8000 ms")
	}
	url := raw["OUTPUT_URL"]
	switch p := raw["OUTPUT_PROTOCOL"]; p {
	case "":
	case ProtoSRT, ProtoSRTLA:
		if srtla := strings.HasPrefix(raw["SRT_URL"], "srtla://"); raw["SRT_URL"] != "" && srtla != (p == ProtoSRTLA) {
			return invalid("OUTPUT_PROTOCOL", "does not match SRT_URL")
		}
	case ProtoRTMP, ProtoRTMPS:
		if url != "" {
			m := reRTMPURL.FindStringSubmatch(url)
			if m == nil || m[1] != p || !portOK(m[3]) {
				return invalid("OUTPUT_URL", "")
			}
		}
	case ProtoRIST:
		if url != "" && (!reRISTURL.MatchString(url) || !portOK(url[strings.LastIndex(url, ":"):])) {
			return invalid("OUTPUT_URL", "")
		}
		if (raw["OUTPUT_USER"] == "") != (raw["OUTPUT_PASSWORD"] == "") {
			return invalid("OUTPUT_USER", "with OUTPUT_PASSWORD")
		}
		if raw["RIST_PROFILE"] == "simple" {
			// No encryption nor SRP in the simple profile; its RTCP goes to the next port (OUTPUTS.md §6)
			if raw["OUTPUT_USER"] != "" || raw["OUTPUT_PASSPHRASE"] != "" {
				return invalid("RIST_PROFILE", "simple has no passphrase nor user")
			}
			if n, _ := strconv.Atoi(url[strings.LastIndex(url, ":")+1:]); url != "" && n%2 != 0 {
				return invalid("OUTPUT_URL", "simple profile: an even port")
			}
		}
	case ProtoUDP:
		if url != "" && (!reUDPURL.MatchString(url) || !portOK(url[strings.LastIndex(url, ":"):])) {
			return invalid("OUTPUT_URL", "")
		}
	}
	return nil
}

// portOK takes ":<port>" (or "" for a URL's default port) and checks the range.
func portOK(s string) bool {
	if s == "" {
		return true
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, ":"))
	return err == nil && n >= 1 && n <= 65535
}

// HasDestination reports whether there is somewhere to send to: SRT_URL for srt and srtla, OUTPUT_URL
// for the others.
func (c *Config) HasDestination() bool {
	switch c.Transport {
	case ProtoSRT, ProtoSRTLA:
		return c.SRTURL != ""
	}
	return c.OutputURL != ""
}

// Secrets are the values the output masks wherever they appear.
func (c *Config) Secrets() []Secret {
	var out []Secret
	for _, s := range []Secret{c.SRTStreamID, c.SRTPassphrase, c.OutputKey, c.OutputPassphrase, c.OutputPassword} {
		if !s.IsZero() {
			out = append(out, s)
		}
	}
	return out
}

// OutputDims returns the output's size for a program of w×h: OUTPUT_SIZE when it is smaller than the
// program, else the program's (the output never upscales).
func (c *Config) OutputDims(w, h int) (int, int) {
	ow, oh, ok := strings.Cut(c.OutputSize, "x")
	if !ok {
		return w, h
	}
	x, _ := strconv.Atoi(ow)
	y, _ := strconv.Atoi(oh)
	if x <= 0 || y <= 0 || x >= w || y >= h {
		return w, h
	}
	return x, y
}

// ABRLatencyMs is the latency budget of the adaptive bitrate (OUTPUTS.md §4): SRT_LATENCY for srt and
// srtla, OUTPUT_LATENCY for rist, 2000 ms for rtmp(s).
func (c *Config) ABRLatencyMs() int {
	switch c.Transport {
	case ProtoRIST:
		return c.OutputLatencyMs
	case ProtoRTMP, ProtoRTMPS:
		return 2000
	}
	return c.SRTLatencyMs
}

// SameDevice reports whether n differs from c only in what output_config takes live (OUTPUTS.md §8):
// the app's keys that are not the output's. The rest comes from the environment, which a reload does not
// change.
func (c *Config) SameDevice(n *Config) bool {
	return c.EDIDPreferred == n.EDIDPreferred && c.Source == n.Source && c.VideoDevice == n.VideoDevice &&
		c.VideoFormat == n.VideoFormat && c.VideoMode == n.VideoMode && c.AudioSource == n.AudioSource
}

// WithOutput is a copy of c with the output configuration of n: the destination, the bitrates and the
// encoding settings, and n's fingerprint. OutputAtBoot stays: emitting is switched apart (output).
func (c *Config) WithOutput(n *Config) *Config {
	x := *c
	x.ConfSHA = n.ConfSHA
	x.VBitrate, x.VBitrateMin, x.ABR, x.ABitrate = n.VBitrate, n.VBitrateMin, n.ABR, n.ABitrate
	x.SRTURL, x.SRTMode, x.SRTLatencyMs, x.SRTPassphrase, x.SRTStreamID = n.SRTURL, n.SRTMode, n.SRTLatencyMs, n.SRTPassphrase, n.SRTStreamID
	x.Transport, x.OutputURL, x.OutputKey, x.OutputPassphrase = n.Transport, n.OutputURL, n.OutputKey, n.OutputPassphrase
	x.OutputUser, x.OutputPassword, x.RISTProfile, x.OutputLatencyMs = n.OutputUser, n.OutputPassword, n.RISTProfile, n.OutputLatencyMs
	x.VCodec, x.OutputSize, x.OutputHalf = n.VCodec, n.OutputSize, n.OutputHalf
	return &x
}
