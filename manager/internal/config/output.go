package config

import (
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// Output protocols and encoding settings (OUTPUTS.md §3): the engine.env keys of the active destination besides
// the SRT ones. The same whitelist the engine checks (internal/engine/config); what does not fit is rejected.

// Protocols of a destination. srt and srtla keep SRT_URL; the others go in OUTPUT_URL.
const (
	ProtoSRT   = "srt"
	ProtoSRTLA = "srtla"
	ProtoRTMP  = "rtmp"
	ProtoRTMPS = "rtmps"
	ProtoRIST  = "rist"
	ProtoUDP   = "udp" // udp:// (MPEG-TS over UDP) or rtp:// (RTP/MPEG-TS)
)

// Protocols in the order the API lists them.
var Protocols = []string{ProtoSRT, ProtoSRTLA, ProtoRTMP, ProtoRTMPS, ProtoRIST, ProtoUDP}

// Codecs, output sizes and frame rates of a destination (OUTPUTS.md §7).
var (
	Codecs      = []string{"h264", "h265"}
	OutputSizes = []string{"", "1280x720", "960x540"}
	OutputFPSs  = []string{"full", "half"}
)

// RIST profiles: "" in engine.env is main.
var RISTProfiles = []string{"simple", "main"}

// Latency bounds of RIST's buffer (OUTPUT_LATENCY) and its default.
const (
	RISTLatencyMin     = 50
	RISTLatencyMax     = 8000
	RISTLatencyDefault = 1000
)

var (
	reStreamKey  = regexp.MustCompile(`^[A-Za-z0-9._~!$()*+,;=:@%/?&-]{1,512}$`)
	reRISTUser   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	reRISTPass   = regexp.MustCompile(`^[A-Za-z0-9_.~-]{1,128}$`)
	reRTMPPath   = regexp.MustCompile(`^[A-Za-z0-9._~!$'()*+,;=:@%/-]{1,400}$`)
	reOutputSize = regexp.MustCompile(`^[0-9]{2,5}x[0-9]{2,5}$`)
)

// usesSRT: the protocol goes through SRT_URL (srt, srtla), with its stream ID.
func usesSRT(p string) bool { return p == ProtoSRT || p == ProtoSRTLA }

// usesPassphrase: SRT's passphrase or RIST's pre-shared key.
func usesPassphrase(p string) bool { return usesSRT(p) || p == ProtoRIST }

// usesKey: the RTMP stream key.
func usesKey(p string) bool { return p == ProtoRTMP || p == ProtoRTMPS }

// usesLatency: the protocol has a latency (SRT's) or a buffer (RIST's).
func usesLatency(p string) bool { return usesSRT(p) || p == ProtoRIST }

// schemeProtocol: the protocol of a URL's scheme ("" if it is none of ours). rtp:// is the udp protocol.
func schemeProtocol(raw string) string {
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	switch scheme {
	case "rtp":
		return ProtoUDP
	case ProtoSRT, ProtoSRTLA, ProtoRTMP, ProtoRTMPS, ProtoRIST, ProtoUDP:
		return scheme
	}
	return ""
}

// ProtocolOf: the protocol a destination URL implies ("" if none).
func ProtocolOf(raw string) string { return schemeProtocol(strings.TrimSpace(raw)) }

// checkOutputURL validates a non-SRT destination URL (OUTPUTS.md §3), the same way the engine does, and returns
// its host. Errors are *i18n.Error.
//   - rtmp://host[:port]/path and rtmps://…: the path is the application (app, live2, rtmp/); the key goes apart;
//   - rist://host:port;
//   - udp://host:port and rtp://host:port (a multicast address is allowed).
//
// The host is a DNS name, an IPv4 or an [IPv6].
func checkOutputURL(raw string) (host string, err error) {
	for _, c := range raw {
		if c <= ' ' || c > '~' {
			return "", i18n.E("config.url_chars")
		}
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || schemeProtocol(raw) == "" || usesSRT(scheme) {
		return "", i18n.E("config.url_any")
	}
	if scheme == ProtoRTMP || scheme == ProtoRTMPS {
		hostport, path, ok := strings.Cut(rest, "/")
		if !ok {
			return "", i18n.E("config.url_rtmp", scheme)
		}
		if !reRTMPPath.MatchString(path) {
			return "", i18n.E("config.url_path")
		}
		return hostPort(hostport, false, scheme)
	}
	if strings.ContainsAny(rest, "/?#@") {
		return "", i18n.E("config.url_format", scheme)
	}
	return hostPort(rest, true, scheme)
}

// hostPort: host[:port] with an optional (or required) port 1-65535; IPv6 only in brackets.
func hostPort(hp string, needPort bool, scheme string) (string, error) {
	host, port := hp, ""
	switch {
	case strings.HasPrefix(hp, "["):
		end := strings.Index(hp, "]")
		if end < 0 {
			return "", i18n.E("config.host")
		}
		host, port = hp[1:end], hp[end+1:]
		if ip := net.ParseIP(host); ip == nil || ip.To4() != nil {
			return "", i18n.E("config.host")
		}
		if port != "" {
			if !strings.HasPrefix(port, ":") {
				return "", i18n.E("config.url_format", scheme)
			}
			port = port[1:]
		}
	case strings.Count(hp, ":") > 1:
		return "", i18n.E("config.host") // a bare IPv6: only in brackets
	default:
		host, port, _ = strings.Cut(hp, ":")
		if strings.Contains(hp, ":") && port == "" {
			return "", i18n.E("config.port")
		}
	}
	if host == "" || !validHost(host) {
		return "", i18n.E("config.host")
	}
	if port == "" {
		if needPort {
			return "", i18n.E("config.url_format", scheme)
		}
		return host, nil
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || port[0] == '0' {
		return "", i18n.E("config.port")
	}
	return host, nil
}

// urlPort: the port of a host:port URL (0 if it has none or it is invalid).
func urlPort(raw string) int {
	_, rest, _ := strings.Cut(raw, "://")
	_, port, err := net.SplitHostPort(rest)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(port)
	return n
}

// ristSimple: the RIST simple profile's limits (librist 0.2.20, OUTPUTS.md §6): no pre-shared key and no SRP
// user (they are the main profile's), and an even port (RTCP goes to port+1). Field → message.
func ristSimple(url, passphrase, user, password string, errs FieldErrors) {
	if passphrase != "" {
		errs["passphrase"] = i18n.Ref("config.rist_main_only")
	}
	if user != "" {
		errs["username"] = i18n.Ref("config.rist_main_only")
	}
	if password != "" {
		errs["password"] = i18n.Ref("config.rist_main_only")
	}
	if p := urlPort(url); p != 0 && p%2 != 0 {
		errs["url"] = i18n.Ref("config.rist_even_port")
	}
}

// URLHost: the host of any destination URL ("" for an SRT listener or an invalid URL). The rule "changing the
// server asks for the secrets again" compares it.
func URLHost(raw string) string {
	if usesSRT(schemeProtocol(raw)) {
		return HostOf(raw)
	}
	h, err := checkOutputURL(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(h)
}

// EffectiveProtocol: OUTPUT_PROTOCOL, or the legacy one SRT_URL implies (srt | srtla); "" without a destination.
func (s Settings) EffectiveProtocol() string {
	if s.Protocol != "" {
		return s.Protocol
	}
	if s.URL == "" {
		return ""
	}
	return Transport(s.URL)
}

// HasDestination: there is somewhere to stream to.
func (s Settings) HasDestination() bool {
	if usesSRT(s.EffectiveProtocol()) || s.Protocol == "" {
		return s.URL != ""
	}
	return s.OutputURL != ""
}

// Codec: the active destination's video codec (h264 when unset, the engine's default).
func (s Settings) Codec() string {
	if s.VideoCodec == "" {
		return "h264"
	}
	return s.VideoCodec
}

// validateOutput: the keys of OUTPUTS.md §3 and their agreement with SRT_URL. Field names are the API's.
func (s Settings) validateOutput(errs FieldErrors) {
	p := s.Protocol
	if p != "" && !slices.Contains(Protocols, p) {
		errs["protocol"] = i18n.Ref("config.one_of", strings.Join(Protocols, ", "))
		return
	}
	switch {
	case p == "" || usesSRT(p):
		if s.OutputURL != "" {
			errs["url"] = i18n.Ref("config.not_for_protocol")
		}
		if p != "" && s.URL != "" && schemeProtocol(s.URL) != p {
			errs["url"] = i18n.Ref("config.url_protocol", p)
		}
	default:
		if s.URL != "" { // SRT_URL is written empty for every other protocol
			errs["url"] = i18n.Ref("config.not_for_protocol")
		} else if s.OutputURL != "" {
			if _, err := checkOutputURL(s.OutputURL); err != nil {
				errs["url"] = i18n.RefOf(err)
			} else if schemeProtocol(s.OutputURL) != p {
				errs["url"] = i18n.Ref("config.url_protocol", p)
			}
		}
	}
	if s.Key != "" && !reStreamKey.MatchString(s.Key) {
		errs["key"] = i18n.Ref("config.stream_key", 512)
	}
	if s.OutputPassphrase != "" && !rePassphrase.MatchString(s.OutputPassphrase) {
		errs["passphrase"] = i18n.Ref("config.passphrase", 10, 79)
	}
	if s.OutputUser != "" && !reRISTUser.MatchString(s.OutputUser) {
		errs["username"] = i18n.Ref("config.rist_user", 64)
	}
	if s.OutputPassword != "" && !reRISTPass.MatchString(s.OutputPassword) {
		errs["password"] = i18n.Ref("config.rist_password", 128)
	}
	if (s.OutputUser == "") != (s.OutputPassword == "") {
		errs["username"] = i18n.Ref("config.rist_credentials")
	}
	if s.RISTProfile != "" && !slices.Contains(RISTProfiles, s.RISTProfile) {
		errs["ristProfile"] = i18n.Ref("config.one_of", strings.Join(RISTProfiles, ", "))
	} else if s.RISTProfile == "simple" {
		ristSimple(s.OutputURL, s.OutputPassphrase, s.OutputUser, s.OutputPassword, errs)
	}
	if s.OutputLatencyMs != 0 && (s.OutputLatencyMs < RISTLatencyMin || s.OutputLatencyMs > RISTLatencyMax) {
		errs["latencyMs"] = i18n.Ref("config.latency", RISTLatencyMin, RISTLatencyMax)
	}
	if s.VideoCodec != "" && !slices.Contains(Codecs, s.VideoCodec) {
		errs["videoCodec"] = i18n.Ref("config.one_of", strings.Join(Codecs, ", "))
	}
	if !slices.Contains(OutputSizes, s.OutputSize) {
		errs["outputSize"] = i18n.Ref("config.output_size")
	}
	if s.OutputFPS != "" && !slices.Contains(OutputFPSs, s.OutputFPS) {
		errs["outputFps"] = i18n.Ref("config.one_of", strings.Join(OutputFPSs, ", "))
	}
}

// outputKeys: the engine.env keys the engine takes hot with {"cmd":"output_config"} (OUTPUTS.md §8): the
// destination, its bitrates and the new output keys. EIRL_MANAGED_BY goes with the destination (the engine ignores
// it). OUTPUT is apart: starting and stopping the stream has its own command (output).
var outputKeys = map[string]bool{
	"EIRL_MANAGED_BY": true, "SRT_URL": true, "SRT_MODE": true, "SRT_LATENCY": true, "SRT_STREAMID": true,
	"SRT_PASSPHRASE": true, "VBITRATE": true, "VBITRATE_MIN": true, "ABR": true, "ABITRATE": true,
	"OUTPUT_PROTOCOL": true, "OUTPUT_URL": true, "OUTPUT_KEY": true, "OUTPUT_PASSPHRASE": true, "OUTPUT_USER": true,
	"OUTPUT_PASSWORD": true, "RIST_PROFILE": true, "OUTPUT_LATENCY": true, "OUTPUT_VCODEC": true,
	"OUTPUT_SIZE": true, "OUTPUT_FPS": true,
}

// DiffEnv compares two readings of the app file (Store.Env), OUTPUT aside: changed = some other key differs;
// onlyOutput = every key that differs is an output key. Without a reading (nil) nothing can be told: not
// onlyOutput.
func DiffEnv(before, after map[string]string) (changed, onlyOutput bool) {
	if before == nil || after == nil {
		return true, false
	}
	onlyOutput = true
	seen := func(k string) {
		if k == "OUTPUT" || before[k] == after[k] {
			return
		}
		_, inBefore := before[k]
		_, inAfter := after[k]
		if !inBefore && !inAfter {
			return
		}
		changed = true
		if !outputKeys[k] {
			onlyOutput = false
		}
	}
	for k := range before {
		seen(k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			seen(k)
		}
	}
	return changed, changed && onlyOutput
}
