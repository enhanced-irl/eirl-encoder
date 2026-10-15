// Package config reads and writes the eirl-engine configuration that the app manages.
//
// There are two files in systemd EnvironmentFile format:
//   - /etc/default/eirl-engine: what does not belong to the user (SOURCE, watchdog thresholds…). Read only.
//   - /etc/eirl-encoder/engine.env: destination, bitrates and mode. The app writes it and a drop-in of
//     eirl-engine.service loads it AFTER the previous one, so it wins.
//
// Every value ends up in the gst-launch `eval` or in the query of the script's SRT URL, so only
// whitelisted values are accepted (the same whitelist the engine checks, internal/engine/config). Anything else is
// rejected, never "cleaned".
//
// User-visible validation messages are i18n refs (i18n.Ref): FieldErrors stays a map of strings for
// the packages that build or read it (internal/cloud) and the API renders each value per request.
package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// Settings is what the app manages: engine.env, that is the ACTIVE destination flattened (destinations.go) plus
// the global keys (OUTPUT, EDID_PREFERRED and the input). The secrets (StreamID, Passphrase, Key,
// OutputPassphrase, OutputPassword) go out in clear only to the screen, over the local socket (GET
// /destination/secrets, its eye); the web and the cloud get Public().
type Settings struct {
	ManagedBy  string // local | eirl: the active destination is the cloud's (destinations.go)
	URL        string // SRT_URL: srt://host:port · listener: srt://:port · srtla://host:port (bonding); "" for other protocols
	Mode       string // caller | listener
	LatencyMs  int
	StreamID   string
	Passphrase string
	VideoKbps  int
	AudioKbps  int
	Output     string // on | off (not streaming)
	// Adaptive video bitrate (internal/engine/output): goes down with congestion to VideoMinKbps and slowly
	// back up to VideoKbps. Essential with SRTLA over mobile networks.
	ABR          bool
	VideoMinKbps int
	// EDIDPreferred: preferred mode of the EDID announced to the source (one of EDIDModes). The list
	// of modes is the same in all of them; what changes is which one the device asks for.
	EDIDPreferred string
	// Input (SOURCES.md): where video and audio come from. See input.go.
	Source      string // hdmi | usb | test
	VideoDevice string // auto | /dev/v4l/by-id/… | /dev/v4l/by-path/… (with usb)
	VideoFormat string // auto | NV12 | YUY2 | I420 | UYVY | MJPG (with usb)
	VideoMode   string // auto | WxH@fps (with usb)
	AudioSource string // auto | none | i2s | alsa:<card id>

	// Output protocols and encoding settings of the active destination (OUTPUTS.md §3, output.go). "" = the
	// engine's default.
	Protocol         string // OUTPUT_PROTOCOL: "" (legacy: from SRT_URL) | srt | srtla | rtmp | rtmps | rist | udp
	OutputURL        string // OUTPUT_URL: rtmp(s)://host[:port]/app · rist://host:port · udp:// or rtp://host:port
	Key              string // OUTPUT_KEY: the RTMP stream key (secret)
	OutputPassphrase string // OUTPUT_PASSPHRASE: RIST's pre-shared key (secret)
	OutputUser       string // OUTPUT_USER: RIST main profile (SRP)
	OutputPassword   string // OUTPUT_PASSWORD (secret)
	RISTProfile      string // RIST_PROFILE: "" (main) | simple | main
	OutputLatencyMs  int    // OUTPUT_LATENCY: RIST's buffer, 0 = "" (the engine's 1000)
	VideoCodec       string // OUTPUT_VCODEC: "" (h264) | h264 | h265
	OutputSize       string // OUTPUT_SIZE: "" (the program's) | 1280x720 | 960x540
	OutputFPS        string // OUTPUT_FPS: "" (full) | full | half
}

// EDIDModes: the EDIDs the device installs (edid-<mode>-lpcm48k.bin next to the engine). The same
// whitelist the engine checks.
var EDIDModes = []string{"1080p30", "1080p50", "720p60"}

func validEDID(m string) bool { return slices.Contains(EDIDModes, m) }

// SRTLALatencyMs: the latency set when switching to SRTLA unless another one is given. With mobile
// uplinks 200-500 ms falls short; 2000 is what Moblin uses.
const SRTLALatencyMs = 2000

// Defaults: the same as the engine when the variable is missing.
func Defaults() Settings {
	return Settings{ManagedBy: "local", Mode: "caller", LatencyMs: 200, VideoKbps: 6000, AudioKbps: 128, Output: "on", VideoMinKbps: 1000, EDIDPreferred: "1080p30",
		Source: "hdmi", VideoDevice: "auto", VideoFormat: "auto", VideoMode: "auto", AudioSource: "auto"}
}

var (
	reHost       = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	reStreamID   = regexp.MustCompile(`^[A-Za-z0-9_:./-]{1,512}$`)
	rePassphrase = regexp.MustCompile(`^[A-Za-z0-9_.~-]{10,79}$`)
	audioRates   = map[int]bool{64: true, 96: true, 128: true, 160: true, 192: true, 256: true}
)

// FieldErrors: API field → user-visible message, as an i18n ref (or plain text from other
// packages). Render it per request with Render; Error() is English (logs).
type FieldErrors map[string]string

func (e FieldErrors) Error() string { return i18n.Msg(i18n.EN, "config.invalid", e.join(i18n.EN)) }

// Message: the whole error as one sentence in lang (cloud command results, logs).
func (e FieldErrors) Message(lang string) string {
	return i18n.Msg(lang, "config.invalid", e.join(lang))
}

func (e FieldErrors) join(lang string) string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(e))
	for _, k := range keys {
		parts = append(parts, k+": "+i18n.Text(lang, e[k]))
	}
	return strings.Join(parts, "; ")
}

// Render: the same map with every message rendered in lang (the "fields" of a 400 validation).
func (e FieldErrors) Render(lang string) map[string]string {
	out := make(map[string]string, len(e))
	for k, v := range e {
		out[k] = i18n.Text(lang, v)
	}
	return out
}

// Validate checks the whole configuration.
func (s Settings) Validate() error {
	errs := FieldErrors{}
	if s.ManagedBy != "local" && s.ManagedBy != "eirl" {
		errs["managedBy"] = i18n.Ref("config.unknown")
	}
	if s.Mode != "caller" && s.Mode != "listener" {
		errs["mode"] = i18n.Ref("config.mode")
	}
	if s.Output != "on" && s.Output != "off" {
		errs["output"] = i18n.Ref("config.output")
	}
	if !validEDID(s.EDIDPreferred) {
		errs["edidPreferred"] = i18n.Ref("config.one_of", strings.Join(EDIDModes, ", "))
	}
	if s.Output == "on" && !s.HasDestination() {
		errs["url"] = i18n.Ref("config.url_missing")
	}
	if s.URL == "" {
		// no SRT destination (none, or another protocol: validateOutput)
	} else if host, _, scheme, err := splitURL(s.URL); err != nil {
		errs["url"] = i18n.RefOf(err)
	} else if scheme == "srtla" && s.Mode != "caller" {
		errs["mode"] = i18n.Ref("config.srtla_caller")
	} else if s.Mode == "listener" && host != "" {
		errs["url"] = i18n.Ref("config.listener_host")
	} else if s.Mode == "caller" && host == "" {
		errs["url"] = i18n.Ref("config.caller_host")
	}
	if s.LatencyMs < 20 || s.LatencyMs > 8000 {
		errs["latencyMs"] = i18n.Ref("config.latency", 20, 8000)
	}
	if s.StreamID != "" && !reStreamID.MatchString(s.StreamID) {
		errs["streamId"] = i18n.Ref("config.stream_id", 512)
	}
	if s.Passphrase != "" && !rePassphrase.MatchString(s.Passphrase) {
		errs["passphrase"] = i18n.Ref("config.passphrase", 10, 79)
	}
	if s.VideoKbps < 500 || s.VideoKbps > 20000 {
		errs["videoBitrateKbps"] = i18n.Ref("config.kbps_range", 500, 20000)
	}
	if !audioRates[s.AudioKbps] {
		errs["audioBitrateKbps"] = i18n.Ref("config.one_of", "64, 96, 128, 160, 192, 256")
	}
	if s.VideoMinKbps < 200 || s.VideoMinKbps > 20000 {
		errs["videoMinBitrateKbps"] = i18n.Ref("config.kbps_range", 200, 20000)
	} else if s.ABR && s.VideoMinKbps > s.VideoKbps {
		errs["videoMinBitrateKbps"] = i18n.Ref("config.min_above_max")
	}
	s.validateInput(errs)
	s.validateOutput(errs)
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// splitURL validates an already normalised URL (srt://host:port or srtla://host:port, no query) and
// returns host, port and scheme. SRTLA needs a host (it always goes to a relay) and no IPv6:
// srtla_send only binds IPv4.
func splitURL(raw string) (host string, port int, scheme string, err error) {
	scheme, hostport, ok := strings.Cut(raw, "://")
	if !ok || (scheme != "srt" && scheme != "srtla") {
		return "", 0, "", i18n.E("config.url_scheme")
	}
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, "", i18n.E("config.url_format", scheme)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, "", i18n.E("config.port")
	}
	if host != "" && !validHost(host) {
		return "", 0, "", i18n.E("config.host")
	}
	if scheme == "srtla" && (host == "" || strings.Contains(host, ":")) {
		return "", 0, "", i18n.E("config.srtla_host")
	}
	return host, port, scheme, nil
}

func validHost(h string) bool {
	if ip := net.ParseIP(h); ip != nil {
		return true
	}
	return reHost.MatchString(h) && !strings.Contains(h, "..")
}

func joinURL(scheme, host string, port int) string {
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// Transport: "srtla" if the URL is srtla://, else "srt".
func Transport(raw string) string {
	if strings.HasPrefix(raw, "srtla://") {
		return "srtla"
	}
	return "srt"
}

// Pasted is what comes out of splitting a pasted URL (e.g. the one from the EnhancedIRL panel:
// srt://host:port?streamid=publish:<key> or srtla://host:port?streamid=…). Empty/0 fields were not in
// the URL.
type Pasted struct {
	URL        string
	Mode       string
	LatencyMs  int
	StreamID   string
	Passphrase string
}

// ParseURL splits an SRT or SRTLA URL with an optional query (streamid, passphrase, latency, mode).
// Errors are *i18n.Error.
func ParseURL(raw string) (Pasted, error) {
	raw = strings.TrimSpace(raw)
	var p Pasted
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "srt" && u.Scheme != "srtla") || u.Opaque != "" {
		return p, i18n.E("config.url_kind")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.Fragment != "" {
		return p, i18n.E("config.url_only", u.Scheme)
	}
	// Host and port with the same rules as Validate (splitURL), on the normalised URL
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return p, i18n.E("config.port")
	}
	p.URL = joinURL(u.Scheme, u.Hostname(), port)
	if _, _, _, err := splitURL(p.URL); err != nil {
		return Pasted{}, err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return p, i18n.E("config.url_query")
	}
	for k, vs := range q {
		if len(vs) != 1 {
			return p, i18n.E("config.param_repeated", k)
		}
		v := vs[0]
		switch k {
		case "streamid":
			if strings.HasPrefix(v, "read:") {
				return p, i18n.E("config.read_url")
			}
			p.StreamID = v
		case "passphrase":
			p.Passphrase = v
		case "mode":
			if u.Scheme == "srtla" && v != "caller" {
				return p, i18n.E("config.srtla_caller")
			}
			p.Mode = v
		case "latency":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return p, i18n.E("config.bad_latency")
			}
			// libsrt uses ms; ffmpeg (and the panel's read URLs) microseconds. No sensible buffer is
			// over 20 s, so a larger value is µs.
			if n > 20000 {
				n /= 1000
			}
			p.LatencyMs = n
		default:
			return p, i18n.E("config.param_unknown", k)
		}
	}
	return p, nil
}

// --- Files ---------------------------------------------------------------------

// keys: script variable ↔ field. The order is the one of the written file.
var keys = []string{"EIRL_MANAGED_BY", "SRT_URL", "SRT_MODE", "SRT_LATENCY", "SRT_STREAMID", "SRT_PASSPHRASE", "VBITRATE", "ABITRATE", "OUTPUT", "ABR", "VBITRATE_MIN", "EDID_PREFERRED",
	"SOURCE", "VIDEO_DEVICE", "VIDEO_FORMAT", "VIDEO_MODE", "AUDIO_SOURCE",
	"OUTPUT_PROTOCOL", "OUTPUT_URL", "OUTPUT_KEY", "OUTPUT_PASSPHRASE", "OUTPUT_USER", "OUTPUT_PASSWORD",
	"RIST_PROFILE", "OUTPUT_LATENCY", "OUTPUT_VCODEC", "OUTPUT_SIZE", "OUTPUT_FPS"}

// ReadEnv reads an EnvironmentFile. Only the subset we write and the one /etc/default/eirl-engine has:
// KEY=value, comments and empty lines; optional outer quotes.
func ReadEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseEnv(f)
}

// readEnvOwned: ReadEnv of a configuration file that must be the app's (or root's): see safefile.
func readEnvOwned(path string) (map[string]string, error) {
	b, err := safefile.ReadOwned(path, 0)
	if err != nil {
		return nil, err
	}
	return parseEnv(bytes.NewReader(b))
}

func parseEnv(r io.Reader) (map[string]string, error) {
	env := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env, sc.Err()
}

// Load returns the effective configuration: defaults ← /etc/default/eirl-engine ← the app's file, as
// systemd sees it. A missing file is not an error.
func Load(defaultPath, appPath string) (Settings, error) {
	s := Defaults()
	for _, p := range []string{defaultPath, appPath} {
		env, err := readEnvOwned(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return s, err
		}
		apply(&s, env)
	}
	return s, nil
}

func apply(s *Settings, env map[string]string) {
	atoi := func(k string, dst *int) {
		if v, ok := env[k]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	str := func(k string, dst *string) {
		if v, ok := env[k]; ok {
			*dst = v
		}
	}
	str("EIRL_MANAGED_BY", &s.ManagedBy)
	str("SRT_URL", &s.URL)
	str("SRT_MODE", &s.Mode)
	atoi("SRT_LATENCY", &s.LatencyMs)
	str("SRT_STREAMID", &s.StreamID)
	str("SRT_PASSPHRASE", &s.Passphrase)
	atoi("VBITRATE", &s.VideoKbps)
	atoi("ABITRATE", &s.AudioKbps)
	str("OUTPUT", &s.Output)
	if v, ok := env["ABR"]; ok {
		s.ABR = v == "1"
	}
	atoi("VBITRATE_MIN", &s.VideoMinKbps)
	str("EDID_PREFERRED", &s.EDIDPreferred)
	str("SOURCE", &s.Source)
	str("VIDEO_DEVICE", &s.VideoDevice)
	str("VIDEO_FORMAT", &s.VideoFormat)
	str("VIDEO_MODE", &s.VideoMode)
	str("AUDIO_SOURCE", &s.AudioSource)
	str("OUTPUT_PROTOCOL", &s.Protocol)
	str("OUTPUT_URL", &s.OutputURL)
	str("OUTPUT_KEY", &s.Key)
	str("OUTPUT_PASSPHRASE", &s.OutputPassphrase)
	str("OUTPUT_USER", &s.OutputUser)
	str("OUTPUT_PASSWORD", &s.OutputPassword)
	str("RIST_PROFILE", &s.RISTProfile)
	if v, ok := env["OUTPUT_LATENCY"]; ok && v == "" {
		s.OutputLatencyMs = 0 // written empty: the engine's default
	} else {
		atoi("OUTPUT_LATENCY", &s.OutputLatencyMs)
	}
	str("OUTPUT_VCODEC", &s.VideoCodec)
	str("OUTPUT_SIZE", &s.OutputSize)
	str("OUTPUT_FPS", &s.OutputFPS)
}

// Render writes EVERY key, also the empty ones: a missing key would let the value of
// /etc/default/eirl-engine through (e.g. a streamid removed here that is still there).
func (s Settings) Render() []byte {
	vals := map[string]string{
		"EIRL_MANAGED_BY": s.ManagedBy, "SRT_URL": s.URL, "SRT_MODE": s.Mode,
		"SRT_LATENCY": strconv.Itoa(s.LatencyMs), "SRT_STREAMID": s.StreamID, "SRT_PASSPHRASE": s.Passphrase,
		"VBITRATE": strconv.Itoa(s.VideoKbps), "ABITRATE": strconv.Itoa(s.AudioKbps), "OUTPUT": s.Output,
		"ABR": map[bool]string{true: "1", false: "0"}[s.ABR], "VBITRATE_MIN": strconv.Itoa(s.VideoMinKbps),
		"EDID_PREFERRED": s.EDIDPreferred,
		"SOURCE":         s.Source, "VIDEO_DEVICE": s.VideoDevice, "VIDEO_FORMAT": s.VideoFormat,
		"VIDEO_MODE": s.VideoMode, "AUDIO_SOURCE": s.AudioSource,
		"OUTPUT_PROTOCOL": s.Protocol, "OUTPUT_URL": s.OutputURL, "OUTPUT_KEY": s.Key,
		"OUTPUT_PASSPHRASE": s.OutputPassphrase, "OUTPUT_USER": s.OutputUser, "OUTPUT_PASSWORD": s.OutputPassword,
		"RIST_PROFILE": s.RISTProfile, "OUTPUT_LATENCY": "", "OUTPUT_VCODEC": s.VideoCodec,
		"OUTPUT_SIZE": s.OutputSize, "OUTPUT_FPS": s.OutputFPS,
	}
	if s.OutputLatencyMs != 0 {
		vals["OUTPUT_LATENCY"] = strconv.Itoa(s.OutputLatencyMs)
	}
	var b strings.Builder
	b.WriteString("# Written by eirl-encoderd (the encoder management app). Do not edit by hand: it is overwritten.\n")
	b.WriteString("# eirl-engine.service loads it after /etc/default/eirl-engine (drop-in eirl-encoder.conf).\n")
	for _, k := range keys {
		b.WriteString(k + "=" + vals[k] + "\n")
	}
	return []byte(b.String())
}

// Save validates and writes the file atomically: temporary file in the same directory + fsync + rename
// + fsync of the directory. There is never a half-written file systemd could read.
func Save(path string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	// 0640: the group (eirl-encoder, from the directory's setgid) is how eirl-engine reads it: it gets
	// that group in its drop-in (SupplementaryGroups). It reads it as data, not as environment.
	return WriteAtomic(path, s.Render(), 0o640)
}

func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // after the rename it no longer exists: a no-op
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// MaskStreamID shows the prefix (publish:) and the ends of the key: publish:AbCd…wXyZ. What follows
// the FIRST ":" is secret as a whole: in a MediaMTX-style streamid (publish:path:user:password) no
// part with ":" is shown.
func MaskStreamID(id string) string {
	if id == "" {
		return ""
	}
	prefix, key := "", id
	if i := strings.Index(id, ":"); i >= 0 {
		prefix, key = id[:i+1], id[i+1:]
	}
	if len(key) < 12 || strings.Contains(key, ":") {
		return prefix + "••••"
	}
	return prefix + key[:4] + "…" + key[len(key)-4:]
}

// SafeURL returns the URL only if it is a valid srt(la)://host:port (no query). A URL saved by hand
// with ?streamid=… must not leave through the API: it is returned empty and marked invalid.
func SafeURL(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if _, _, _, err := splitURL(raw); err != nil {
		return "", false
	}
	return raw, true
}

// SplitURL: host and port of a normalised URL (srt://host:port, srtla://…, no query); ok=false if it
// is invalid or empty.
func SplitURL(raw string) (host string, port int, ok bool) {
	h, p, _, err := splitURL(raw)
	return h, p, err == nil
}

// HostOf: the host of a normalised URL ("" if listener or invalid).
func HostOf(raw string) string {
	h, _, _, _ := splitURL(raw)
	return h
}
