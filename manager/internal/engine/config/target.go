package config

import (
	"regexp"
	"strconv"
	"strings"
)

// srtlaConnTimeoutMs: the SRT handshake towards the local srtla_send waits for it to register the
// uplinks (seconds over a mobile network), sh:486-489.
const srtlaConnTimeoutMs = 10000

// tsPacket is the size of an MPEG-TS packet: an SRT packet carries a whole number of them.
const tsPacket = 188

// SRTQuery is the query of the SRT target (sh:481-483, SRTLA.md §9.5):
// mode=<SRT_MODE>&latency=<SRT_LATENCY>&payloadsize=<SRT_PAYLOAD>[&passphrase=<…>&pbkeylen=16][&streamid=<…>].
func (c *Config) SRTQuery() Secret {
	q := "mode=" + c.SRTMode + "&latency=" + strconv.Itoa(c.SRTLatencyMs) + "&payloadsize=" + strconv.Itoa(c.srtPayload())
	if !c.SRTPassphrase.IsZero() {
		q += "&passphrase=" + c.SRTPassphrase.Reveal() + "&pbkeylen=16"
	}
	if !c.SRTStreamID.IsZero() {
		q += "&streamid=" + c.SRTStreamID.Reveal()
	}
	return NewSecret(q)
}

// SRTTarget is the URL srt-live-transmit sends to (sh:484-492), "" without SRT_URL:
//   - srt: <SRT_URL>?<query>
//   - srtla: srt://127.0.0.1:<SRTLA_LOCAL_PORT>?<query>&conntimeo=10000 (the local srtla_send)
//
// It carries the stream id and the passphrase: pass it to the output by environment, never argv.
func (c *Config) SRTTarget() Secret { return c.SRTTargetAt("") }

// SRTTargetAt is SRTTarget with the relay's host replaced by addr, an IPv4 address the output resolved
// (SRTLA.md §9.7): only for srt, whose target names the relay; "" keeps the URL as it is.
func (c *Config) SRTTargetAt(addr string) Secret {
	if c.SRTURL == "" {
		return Secret{}
	}
	q := c.SRTQuery().Reveal()
	if c.Transport == "srtla" {
		return NewSecret("srt://127.0.0.1:" + strconv.Itoa(c.SRTLALocalPort) + "?" + q +
			"&conntimeo=" + strconv.Itoa(srtlaConnTimeoutMs))
	}
	u := c.SRTURL
	if host := c.RelayHost(); addr != "" && host != "" {
		u = "srt://" + addr + strings.TrimPrefix(u, "srt://"+host)
	}
	return NewSecret(u + "?" + q)
}

// RelayHost is the name (or address) of the relay srt and srtla send to: the host of srtla://host:port,
// and of srt://host:port in caller mode. "" when there is none to resolve: other protocols, a listener,
// srt://:port, or an IPv6 address in brackets (sent as it is).
func (c *Config) RelayHost() string {
	switch {
	case c.SRTURL == "":
		return ""
	case c.Transport == "srtla":
		return c.SRTLAHost()
	case c.Transport != "srt" || c.SRTMode != "caller":
		return ""
	}
	hp := strings.TrimPrefix(c.SRTURL, "srt://")
	i := strings.LastIndex(hp, ":")
	if i <= 0 || strings.HasPrefix(hp, "[") {
		return ""
	}
	return hp[:i]
}

// DefaultSRTPayload is SRT_PAYLOAD when it is not set: 6 TS packets (SRTLA.md §9.5).
const DefaultSRTPayload = 6 * tsPacket

// srtPayload is SRT_PAYLOAD, DefaultSRTPayload when unset (a Config built by hand, in tests).
func (c *Config) srtPayload() int {
	if c.SRTPayload <= 0 {
		return DefaultSRTPayload
	}
	return c.SRTPayload
}

// SRTPayloadBytes is the payload of each SRT packet: srt-live-transmit's -chunk.
func (c *Config) SRTPayloadBytes() int { return c.srtPayload() }

// TSPerPacket is how many TS packets each write of the output's mux carries (its alignment): an SRT
// packet's worth for srt and srtla (SRTLA.md §9.5), 7 for the other protocols.
func (c *Config) TSPerPacket() int {
	if c.Transport == ProtoSRT || c.Transport == ProtoSRTLA {
		return c.srtPayload() / tsPacket
	}
	return 7
}

var reMaskTarget = regexp.MustCompile(`(passphrase|streamid)=[^&]*`)

// MaskedTarget is the SRT target as the launcher's summary line prints it (sh:801, sh:809:
// sed -E 's/(passphrase|streamid)=[^&]*/\1=***/g'), "" without a destination.
func (c *Config) MaskedTarget() string {
	return reMaskTarget.ReplaceAllString(c.SRTTarget().Reveal(), "${1}=***")
}

// SRTLAHost and SRTLAPort are the relay of srtla://host:port (sh:487); "" and 0 for srt, as the
// launcher passes them (--srtla-host "", --srtla-port 0).
func (c *Config) SRTLAHost() string {
	if c.Transport != "srtla" {
		return ""
	}
	hp := strings.TrimPrefix(c.SRTURL, "srtla://")
	return hp[:strings.LastIndex(hp, ":")]
}

func (c *Config) SRTLAPort() int {
	if c.Transport != "srtla" {
		return 0
	}
	n, _ := strconv.Atoi(c.SRTURL[strings.LastIndex(c.SRTURL, ":")+1:])
	return n
}

// Program is the final program geometry and cadence (sh:475-478).
type Program struct {
	Width, Height int
	Size          string // "<W>x<H>", --mixer-size
	FPS           int
	FPSFrac       string // "<FPS>/1" (RATE in the state file)
	GOP           int    // frames: GOP, or 2 s
}

// Program applies the launcher's rule once the sources are known. chosenFPS is the cadence picked
// from the sources (50 or 60, mixer_prepare); an explicit FPS wins over it, and ≤ 0 falls back to
// the launcher's 30. RES defaults to 1920x1080 and GOP to 2*FPS.
func (c *Config) Program(chosenFPS int) Program {
	fps := c.FPS
	if !c.FPSSet {
		fps = chosenFPS
		if fps <= 0 {
			fps = 30
		}
	}
	res := c.Res
	if res == "" {
		res = "1920x1080"
	}
	w, h, _ := strings.Cut(res, "x")
	p := Program{Size: res, FPS: fps, FPSFrac: strconv.Itoa(fps) + "/1", GOP: c.GOP}
	p.Width, _ = strconv.Atoi(w)
	p.Height, _ = strconv.Atoi(h)
	if !c.GOPSet {
		p.GOP = fps * 2
	}
	return p
}

// MixerMain is the source on air at start (sh:461-465): hdmi, test, or for usb the probed device
// (inputDevice, "" if the probe did not answer) else VIDEO_DEVICE, with "auto" as usb:auto.
func (c *Config) MixerMain(inputDevice string) string {
	switch c.Source {
	case "hdmi", "test":
		return c.Source
	}
	m := inputDevice
	if m == "" {
		m = c.VideoDevice
	}
	if m == "auto" {
		return "usb:auto"
	}
	return m
}

// AudioMain is the audio on air at start (sh:601-611): AUDIO_SOURCE, and with auto the main
// source's (i2s for hdmi, alsa:<inputALSA> for a usb main source whose card the probe found).
func (c *Config) AudioMain(inputALSA string) string {
	switch {
	case c.AudioSource == "none", c.AudioSource == "i2s", strings.HasPrefix(c.AudioSource, "alsa:"):
		return c.AudioSource
	case c.Source == "hdmi":
		return "i2s"
	case c.Source == "usb" && inputALSA != "":
		return "alsa:" + inputALSA
	}
	return "none"
}

// MixerTest reports whether the bars source is added (--mixer-test, SOURCE=test).
func (c *Config) MixerTest() bool { return c.Source == "test" }

// I2SDevice is the ALSA device of the board HDMI audio, hw:<ALSA_CARD>,<ALSA_ADMAIF-1> (sh:956).
func (c *Config) I2SDevice() string {
	return "hw:" + c.ALSACard + "," + strconv.Itoa(c.ALSAAdmaif-1)
}
