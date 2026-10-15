// Package config loads and validates the configuration of eirl-engine, exactly as the bash launcher
// did (scripts/eirl-engine.sh:22-237, 475-492, 689-703), and renders what the engine derives from
// it: the SRT target, the program size and cadence, and the /run/eirl-engine/state file.
//
// Sources, in order of precedence (the last one wins):
//  1. the process environment (systemd EnvironmentFile=/etc/default/eirl-engine);
//  2. the app file EIRL_CONF (normally /etc/eirl-encoder/engine.env), read as DATA and never as
//     environment: only the 16 whitelisted keys are taken, so whoever can write it cannot inject
//     BASH_ENV, LD_PRELOAD, GST_PLUGIN_PATH or paths and run code as the streaming user.
//
// Every value ends inside a GStreamer pipeline or the query of the SRT URL, so values are matched
// against the launcher's regexes and rejected, never sanitised. An invalid value is an
// *InvalidError naming the variable; the caller logs `event=config_invalid key=<VAR>` and exits 64.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// Paths and defaults the launcher hard-codes.
const (
	// DefaultScriptDir is where the launcher and the EDID files are installed: the default EDID is
	// <ScriptDir>/edid-<EDID_PREFERRED>-lpcm48k.bin (sh:82, sh:184; ExecStart in eirl-engine.service).
	DefaultScriptDir = "/usr/lib/eirl-encoder/engine"
	// DefaultNetConf is what engine_net.py assumes when EIRL_CONF is unset (engine_net.py:53).
	DefaultNetConf = "/etc/eirl-encoder/engine.env"
	// DefaultNetSyncMs is MIXER_NET_SYNC when it is missing or out of range (engine_net.py:22).
	DefaultNetSyncMs = 150
)

// Whitelist is the set of keys taken from EIRL_CONF (sh:43-44). Everything else in that file,
// including EIRL_MANAGED_BY that the app writes, is ignored.
var Whitelist = []string{
	"SRT_URL", "SRT_MODE", "SRT_LATENCY", "SRT_STREAMID", "SRT_PASSPHRASE", "VBITRATE", "ABITRATE", "OUTPUT",
	"ABR", "VBITRATE_MIN", "EDID_PREFERRED", "SOURCE", "VIDEO_DEVICE", "VIDEO_FORMAT", "VIDEO_MODE", "AUDIO_SOURCE",
	// The output protocols and encoding settings (OUTPUTS.md §3)
	"OUTPUT_PROTOCOL", "OUTPUT_URL", "OUTPUT_KEY", "OUTPUT_PASSPHRASE", "OUTPUT_USER", "OUTPUT_PASSWORD",
	"RIST_PROFILE", "OUTPUT_LATENCY", "OUTPUT_VCODEC", "OUTPUT_SIZE", "OUTPUT_FPS",
}

// verbatimKeys are read without the launcher's «A=b=» quirk (ParseConfFile): they never went through
// it, and a stream key may end in «=».
var verbatimKeys = map[string]bool{
	"OUTPUT_PROTOCOL": true, "OUTPUT_URL": true, "OUTPUT_KEY": true, "OUTPUT_PASSPHRASE": true, "OUTPUT_USER": true,
	"OUTPUT_PASSWORD": true, "RIST_PROFILE": true, "OUTPUT_LATENCY": true, "OUTPUT_VCODEC": true, "OUTPUT_SIZE": true,
	"OUTPUT_FPS": true,
}

// InvalidError is a configuration the launcher would refuse with exit 64. It never carries the
// value (it could be a secret), only the variable and, sometimes, why.
type InvalidError struct {
	key    string
	reason string
}

// Key is the variable to report as `event=config_invalid key=<VAR>`.
func (e *InvalidError) Key() string { return e.key }

func (e *InvalidError) Error() string {
	if e.reason != "" {
		return "invalid configuration: " + e.key + " (" + e.reason + ")"
	}
	return "invalid configuration: " + e.key
}

func invalid(key, reason string) error { return &InvalidError{key: key, reason: reason} }

// Options are the outside world Load reads. Every nil field falls back to the real system.
type Options struct {
	// LookupEnv reads the process environment (os.LookupEnv). Unset and empty differ for the keys the
	// launcher defaults with ${VAR-…}: UPLINKS_DISABLED, UPLINKS_NOINTERNET, SRT_STATS, THUMB_FILE.
	LookupEnv func(string) (string, bool)
	// ReadFile reads EIRL_CONF (os.ReadFile).
	ReadFile func(string) ([]byte, error)
	// Executable is the launcher's `[ -x path ]`, used for SRTLA_BIN (access(2) with X_OK).
	Executable func(string) bool
	// FindVDEV returns the TC358743 capture node when VDEV is empty (sysfs scan, sh:142-147).
	FindVDEV func() string
	// ScriptDir replaces DefaultScriptDir (the launcher's `dirname $0`).
	ScriptDir string
}

// Config is the validated configuration. Field comments give the variable and its default.
type Config struct {
	// ConfPath is EIRL_CONF ("" = no app file). ConfSHA is CONF_SHA: ConfHash of the file, or
	// NoConfSHA when the variable is empty or the file does not exist.
	ConfPath string
	ConfSHA  string
	// NetConfDir is where engine_net.py looks for cameras-public.json and media-api.key: the
	// directory of EIRL_CONF, /etc/eirl-encoder when unset (Python os.path.dirname semantics).
	NetConfDir string

	// SingleSource is MIXER=0. It is valid configuration, but the engine has no single-source mode
	// (decision E5): it logs event=single_source_unsupported and runs the mixer anyway, so every
	// MIXER=1 rule applies (CONF_SHA without OUTPUT, URL checks, MIXER=1 in the state file).
	SingleSource bool

	Source      string // SOURCE: test | hdmi | usb (test)
	VDEV        string // VDEV: TC358743 capture node (sysfs lookup, else /dev/video0)
	VideoDevice string // VIDEO_DEVICE: auto | /dev/v4l/by-(id|path)/… (auto)
	VideoFormat string // VIDEO_FORMAT (auto); single-source only, validated
	VideoMode   string // VIDEO_MODE (auto); single-source only, validated
	AudioSource string // AUDIO_SOURCE: auto | none | i2s | alsa:<card> (auto)
	// AudioEnv is AUDIO from the environment, unvalidated as in the launcher; it only shows up in
	// the "starting" state line (with the mixer the "ready" line says mezclador:<main>).
	AudioEnv string

	MixerSlots       int    // MIXER_SLOTS 1-4 (4)
	MixerLatencyMs   int    // MIXER_LATENCY (60)
	MixerSyncMs      int    // MIXER_SYNC (120)
	MixerScreen      bool   // MIXER_SCREEN (1)
	MixerHDMICapture string // MIXER_HDMI_CAPTURE: nvcam | v4l2 (nvcam)
	MixerAudioSlots  int    // MIXER_AUDIO_SLOTS 1-4 (4)
	MixerNetSyncMs   int    // MIXER_NET_SYNC 0-1000, else 150 (environment only, engine_net.py)
	ProbeBin         string // PROBE_BIN (/usr/local/bin/eirl-encoderd)

	EDIDPreferred string // EDID_PREFERRED: 1080p30 | 1080p50 | 720p60 (1080p30)
	EDID          string // EDID (<ScriptDir>/edid-<EDID_PREFERRED>-lpcm48k.bin)

	ALSACard         string // ALSA_CARD (tegrasndt210ref)
	ALSAAdmaif       int    // ALSA_ADMAIF (2)
	SignalTimeout    int    // SIGNAL_TIMEOUT s (60); single-source only, validated
	DeviceTimeout    int    // DEVICE_TIMEOUT s (SIGNAL_TIMEOUT); single-source only, validated
	WatchdogMs       int    // WATCHDOG (3000); single-source only, validated
	Overlay          bool   // OVERLAY (1); single-source only, validated
	TCI2C            int    // TC_I2C (8)
	AudioLockTimeout int    // AUDIO_LOCK_TIMEOUT s (10)
	HWResetInterval  int    // HW_RESET_INTERVAL s (300)
	StateDir         string // STATE_DIR (/var/lib/eirl-engine)
	RunDir           string // RUN_DIR (/run/eirl-engine)

	// Res is RES ("" = 1920x1080), FPS and GOP the overrides (FPSSet/GOPSet false = derived).
	// Program applies the launcher's rule.
	Res    string
	FPS    int
	FPSSet bool
	GOP    int
	GOPSet bool

	VBitrate     int    // VBITRATE kbps (6000)
	VBitrateMin  int    // VBITRATE_MIN kbps (1000)
	ABR          bool   // ABR (0)
	ABitrate     int    // ABITRATE kbps (128)
	AENC         string // AENC: voaacenc | avenc_aac (voaacenc)
	Profile      int    // PROFILE (4)
	H264Preset   int    // H264_PRESET 1-4 (2)
	H264QPRange  string // H264_QP_RANGE ("")
	H264TwoPass  bool   // H264_TWOPASS (1)
	OutputAtBoot bool   // OUTPUT: on | off (on)
	Record       string // RECORD ("" = off)

	SRTURL        string // SRT_URL ("" = no destination)
	SRTMode       string // SRT_MODE: caller | listener (caller)
	SRTLatencyMs  int    // SRT_LATENCY (200)
	SRTPassphrase Secret // SRT_PASSPHRASE ("")
	SRTStreamID   Secret // SRT_STREAMID ("")
	// Transport is the output protocol (OUTPUTS.md §3): OUTPUT_PROTOCOL, or srt / srtla from SRT_URL when it
	// is empty (the legacy configuration).
	Transport string
	// The destination of every protocol but srt and srtla, which keep SRT_URL (OUTPUTS.md §3)
	OutputURL        string // OUTPUT_URL: rtmp(s)://host[:port]/path · rist://host:port · udp|rtp://host:port
	OutputKey        Secret // OUTPUT_KEY: the RTMP stream key
	OutputPassphrase Secret // OUTPUT_PASSPHRASE: RIST's pre-shared key
	OutputUser       string // OUTPUT_USER: RIST main profile (SRP)
	OutputPassword   Secret // OUTPUT_PASSWORD
	RISTProfile      string // RIST_PROFILE: simple | main (main)
	OutputLatencyMs  int    // OUTPUT_LATENCY: RIST's buffer (1000)
	// Encoding (OUTPUTS.md §7)
	VCodec            string // OUTPUT_VCODEC: h264 | h265 (h264)
	OutputSize        string // OUTPUT_SIZE: "" (the program's) | 1280x720 | 960x540
	OutputHalf        bool   // OUTPUT_FPS=half: half the program's frame rate
	SendBin           string // SEND_BIN (/usr/local/bin/eirl-send), the RTMP sender
	RISTBin           string // RIST_BIN (/usr/local/bin/ristsender)
	RISTLocalPort     int    // RIST_LOCAL_PORT (6100): where the output pipeline hands the TS to ristsender
	LinksFile         string // LINKS_FILE (/run/eirl-encoder/links.json with EIRL_CONF): the links' health, to pick RTMP's
	SRTLABin          string // SRTLA_BIN (/usr/local/bin/srtla_send)
	SRTLALocalPort    int    // SRTLA_LOCAL_PORT (6000)
	UplinksFile       string // UPLINKS_FILE (/run/eirl-uplinks/available)
	UplinkWait        int    // UPLINK_WAIT s (30)
	UplinksDisabled   string // UPLINKS_DISABLED (<dir of EIRL_CONF>/uplinks-disabled)
	UplinksNoInternet string // UPLINKS_NOINTERNET (/run/eirl-encoder/uplinks-nointernet with EIRL_CONF)
	SRTStats          string // SRT_STATS (<RUN_DIR>/srt-stats.json; "" = off)

	Levels       bool   // LEVELS (1)
	LevelsPort   int    // LEVELS_PORT (5005)
	Live         bool   // LIVE (1)
	LiveSize     string // LIVE_SIZE (640x360)
	LiveVBitrate int    // LIVE_VBITRATE kbps (800)
	LiveABitrate int    // LIVE_ABITRATE kbps (96)
	LiveVPort    int    // LIVE_VPORT (5006)
	LiveAPort    int    // LIVE_APORT (5008)

	Thumb      bool   // THUMB (1)
	ThumbSize  string // THUMB_SIZE (320x180)
	ThumbEvery int    // THUMB_EVERY s ≥ 1 (5)
	ThumbFile  string // THUMB_FILE (<RUN_DIR>/thumb.jpg; "" = off)

	PulseServer string // PULSE_SERVER (unix:/nonexistent), exported to the children
}

// Regexes of the launcher, same anchors. Go's `$` without (?m) matches only at the end of the text,
// like the POSIX ERE of bash's [[ =~ ]] (a trailing "\n" does not match), and with LC_ALL=C bash's
// [A-Za-z0-9] ranges are ASCII as in RE2.
var (
	reUint6       = regexp.MustCompile(`^[0-9]{1,6}$`)
	reOptUint4    = regexp.MustCompile(`^([0-9]{1,4})?$`)
	reOneToFour   = regexp.MustCompile(`^[1-4]$`)
	reBit         = regexp.MustCompile(`^[01]$`)
	reQPRange     = regexp.MustCompile(`^([0-9]{1,2},[0-9]{1,2}:[0-9]{1,2},[0-9]{1,2}:[0-9]{1,2},[0-9]{1,2})?$`)
	reOptSize     = regexp.MustCompile(`^([0-9]{2,5}x[0-9]{2,5})?$`)
	reSize        = regexp.MustCompile(`^[0-9]{2,5}x[0-9]{2,5}$`)
	reEDID        = regexp.MustCompile(`^(1080p30|1080p50|720p60)$`)
	rePath        = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	reOptPath     = regexp.MustCompile(`^([A-Za-z0-9_./-]+)?$`)
	reSource      = regexp.MustCompile(`^(test|hdmi|usb)$`)
	reVideoDevice = regexp.MustCompile(`^(auto|/dev/v4l/by-(id|path)/[A-Za-z0-9_:-][A-Za-z0-9_.:-]{0,199})$`)
	reVideoFormat = regexp.MustCompile(`^(auto|NV12|YUY2|I420|UYVY|MJPG)$`)
	reVideoMode   = regexp.MustCompile(`^(auto|[0-9]{2,5}x[0-9]{2,5}@[0-9]{1,3})$`)
	reAudioSource = regexp.MustCompile(`^(auto|none|i2s|alsa:[A-Za-z0-9_-]{1,32})$`)
	reUint4       = regexp.MustCompile(`^[0-9]{1,4}$`)
	reUint3       = regexp.MustCompile(`^[0-9]{1,3}$`)
	reHDMICapture = regexp.MustCompile(`^(nvcam|v4l2)$`)
	reOutput      = regexp.MustCompile(`^(on|off)$`)
	reSRTMode     = regexp.MustCompile(`^(caller|listener)$`)
	reStreamID    = regexp.MustCompile(`^([A-Za-z0-9_:./-]{1,512})?$`)
	rePassphrase  = regexp.MustCompile(`^([A-Za-z0-9_.~-]{10,79})?$`)
	reSRTLAURL    = regexp.MustCompile(`^srtla://[A-Za-z0-9.-]{1,253}:[0-9]{1,5}$`)
	reSRTURL      = regexp.MustCompile(`^srt://(\[[0-9A-Fa-f:.]{2,45}\]|[A-Za-z0-9.-]{1,253})?:[0-9]{1,5}$`)
	reThumbSize   = regexp.MustCompile(`^[0-9]{2,4}x[0-9]{2,4}$`)

	// OUTPUTS.md §3. Hosts: a DNS name, IPv4 or [IPv6]; the app checks the same.
	reProtocol   = regexp.MustCompile(`^(srt|srtla|rtmp|rtmps|rist|udp)?$`)
	reRTMPURL    = regexp.MustCompile(`^(rtmps?)://(\[[0-9A-Fa-f:.]{2,45}\]|[A-Za-z0-9.-]{1,253})(:[0-9]{1,5})?/[A-Za-z0-9._~!$'()*+,;=:@%/-]{1,400}$`)
	reRISTURL    = regexp.MustCompile(`^rist://(\[[0-9A-Fa-f:.]{2,45}\]|[A-Za-z0-9.-]{1,253}):[0-9]{1,5}$`)
	reUDPURL     = regexp.MustCompile(`^(udp|rtp)://(\[[0-9A-Fa-f:.]{2,45}\]|[A-Za-z0-9.-]{1,253}):[0-9]{1,5}$`)
	reOutputKey  = regexp.MustCompile(`^([A-Za-z0-9._~!$()*+,;=:@%/?&-]{1,512})?$`)
	reRISTUser   = regexp.MustCompile(`^([A-Za-z0-9_.-]{1,64})?$`)
	reRISTPass   = regexp.MustCompile(`^([A-Za-z0-9_.~-]{1,128})?$`)
	reRISTProf   = regexp.MustCompile(`^(simple|main)?$`)
	reOptUint5   = regexp.MustCompile(`^([0-9]{1,5})?$`)
	reVCodec     = regexp.MustCompile(`^(h264|h265)?$`)
	reOutputSize = regexp.MustCompile(`^(1280x720|960x540)?$`)
	reOutputFPS  = regexp.MustCompile(`^(full|half)?$`)
)

// Integer keys checked with ^[0-9]{1,6}$, in the launcher's order (sh:175-177).
var uintKeys = []string{
	"VBITRATE", "ABITRATE", "SRT_LATENCY", "PROFILE", "WATCHDOG", "SIGNAL_TIMEOUT", "AUDIO_LOCK_TIMEOUT",
	"HW_RESET_INTERVAL", "TC_I2C", "ALSA_ADMAIF", "LEVELS_PORT", "LIVE_VBITRATE", "LIVE_ABITRATE", "LIVE_VPORT",
	"LIVE_APORT", "VBITRATE_MIN", "SRTLA_LOCAL_PORT", "UPLINK_WAIT", "DEVICE_TIMEOUT",
}

// env is the merged view: whitelisted keys of the app file over the process environment.
type env struct {
	lookup func(string) (string, bool)
	file   map[string]string
}

func (e env) get(k string) (string, bool) {
	if v, ok := e.file[k]; ok {
		return v, true
	}
	return e.lookup(k)
}

// or is ${K:-def}: unset or empty → def.
func (e env) or(k, def string) string {
	if v, _ := e.get(k); v != "" {
		return v
	}
	return def
}

// unsetOr is ${K-def}: only unset → def (set but empty stays empty).
func (e env) unsetOr(k, def string) string {
	if v, ok := e.get(k); ok {
		return v
	}
	return def
}

// ParseConfFile extracts the whitelisted keys from the content of EIRL_CONF with the launcher's
// quirks (`IFS='=' read -r k v` per line, sh:40-46): NUL bytes are dropped (bash's $(cat)), the key
// is the text before the first '=' (the whole line when there is none, which sets the key to ""),
// the value is the rest with no quote stripping and no trimming, except that a single '=' that is
// the last character and the only '=' of the rest is dropped (bash's read: "A=b=" gives "b"). The
// last occurrence of a key wins.
func ParseConfFile(content []byte) map[string]string {
	text := strings.ReplaceAll(string(content), "\x00", "")
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		k, v, _ := strings.Cut(line, "=")
		if !isWhitelisted(k) {
			continue
		}
		if !verbatimKeys[k] && strings.Count(v, "=") == 1 && strings.HasSuffix(v, "=") {
			v = strings.TrimSuffix(v, "=")
		}
		out[k] = v
	}
	return out
}

func isWhitelisted(k string) bool {
	for _, w := range Whitelist {
		if k == w {
			return true
		}
	}
	return false
}

// Load reads and validates the configuration. The first invalid key, in the launcher's order, is
// returned as *InvalidError.
func Load(o Options) (*Config, error) {
	o = o.withDefaults()
	c := &Config{ConfSHA: NoConfSHA}
	c.PulseServer = (env{lookup: o.LookupEnv}).or("PULSE_SERVER", "unix:/nonexistent")

	// EIRL_CONF as data (sh:36-57). [ -e ] false → ignored; exists but unreadable → exit 64.
	e := env{lookup: o.LookupEnv, file: map[string]string{}}
	c.ConfPath, _ = o.LookupEnv("EIRL_CONF")
	if c.ConfPath != "" {
		b, err := o.ReadFile(c.ConfPath)
		switch {
		case err == nil:
			e.file = ParseConfFile(b)
			c.ConfSHA = ConfHash(b)
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		default:
			return nil, invalid("EIRL_CONF", "cannot read the app configuration file")
		}
	}
	if p, ok := o.LookupEnv("EIRL_CONF"); ok {
		c.NetConfDir = pyDirname(p)
	} else {
		c.NetConfDir = pyDirname(DefaultNetConf)
	}

	// Defaults (sh:59-136, 689-692), raw.
	raw := map[string]string{
		"SOURCE":             e.or("SOURCE", "test"),
		"VDEV":               e.or("VDEV", ""),
		"VIDEO_DEVICE":       e.or("VIDEO_DEVICE", "auto"),
		"VIDEO_FORMAT":       e.or("VIDEO_FORMAT", "auto"),
		"VIDEO_MODE":         e.or("VIDEO_MODE", "auto"),
		"AUDIO_SOURCE":       e.or("AUDIO_SOURCE", "auto"),
		"MIXER":              e.or("MIXER", "1"),
		"MIXER_SLOTS":        e.or("MIXER_SLOTS", "4"),
		"MIXER_LATENCY":      e.or("MIXER_LATENCY", "60"),
		"MIXER_SYNC":         e.or("MIXER_SYNC", "120"),
		"MIXER_SCREEN":       e.or("MIXER_SCREEN", "1"),
		"MIXER_HDMI_CAPTURE": e.or("MIXER_HDMI_CAPTURE", "nvcam"),
		"MIXER_AUDIO_SLOTS":  e.or("MIXER_AUDIO_SLOTS", "4"),
		"PROBE_BIN":          e.or("PROBE_BIN", "/usr/local/bin/eirl-encoderd"),
		"EDID_PREFERRED":     e.or("EDID_PREFERRED", "1080p30"),
		"EDID":               e.or("EDID", ""),
		"AUDIO":              e.or("AUDIO", ""),
		"ALSA_CARD":          e.or("ALSA_CARD", "tegrasndt210ref"),
		"ALSA_ADMAIF":        e.or("ALSA_ADMAIF", "2"),
		"SIGNAL_TIMEOUT":     e.or("SIGNAL_TIMEOUT", "60"),
		"WATCHDOG":           e.or("WATCHDOG", "3000"),
		"TC_I2C":             e.or("TC_I2C", "8"),
		"AUDIO_LOCK_TIMEOUT": e.or("AUDIO_LOCK_TIMEOUT", "10"),
		"HW_RESET_INTERVAL":  e.or("HW_RESET_INTERVAL", "300"),
		"STATE_DIR":          e.or("STATE_DIR", "/var/lib/eirl-engine"),
		"RES":                e.or("RES", ""),
		"FPS":                e.or("FPS", ""),
		"VBITRATE":           e.or("VBITRATE", "6000"),
		"GOP":                e.or("GOP", ""),
		"PROFILE":            e.or("PROFILE", "4"),
		"H264_PRESET":        e.or("H264_PRESET", "2"),
		"H264_QP_RANGE":      e.or("H264_QP_RANGE", ""),
		"H264_TWOPASS":       e.or("H264_TWOPASS", "1"),
		"ABITRATE":           e.or("ABITRATE", "128"),
		"AENC":               e.or("AENC", "voaacenc"),
		"SRT_URL":            e.or("SRT_URL", ""),
		"SRT_MODE":           e.or("SRT_MODE", "caller"),
		"SRT_LATENCY":        e.or("SRT_LATENCY", "200"),
		"SRT_PASSPHRASE":     e.or("SRT_PASSPHRASE", ""),
		"SRT_STREAMID":       e.or("SRT_STREAMID", ""),
		"ABR":                e.or("ABR", "0"),
		"VBITRATE_MIN":       e.or("VBITRATE_MIN", "1000"),
		"SRTLA_BIN":          e.or("SRTLA_BIN", "/usr/local/bin/srtla_send"),
		"SRTLA_LOCAL_PORT":   e.or("SRTLA_LOCAL_PORT", "6000"),
		"UPLINKS_FILE":       e.or("UPLINKS_FILE", "/run/eirl-uplinks/available"),
		"UPLINK_WAIT":        e.or("UPLINK_WAIT", "30"),
		"OVERLAY":            e.or("OVERLAY", "1"),
		"OUTPUT":             e.or("OUTPUT", "on"),
		"RUN_DIR":            e.or("RUN_DIR", "/run/eirl-engine"),
		"LEVELS":             e.or("LEVELS", "1"),
		"LEVELS_PORT":        e.or("LEVELS_PORT", "5005"),
		"LIVE":               e.or("LIVE", "1"),
		"LIVE_SIZE":          e.or("LIVE_SIZE", "640x360"),
		"LIVE_VBITRATE":      e.or("LIVE_VBITRATE", "800"),
		"LIVE_ABITRATE":      e.or("LIVE_ABITRATE", "96"),
		"LIVE_VPORT":         e.or("LIVE_VPORT", "5006"),
		"LIVE_APORT":         e.or("LIVE_APORT", "5008"),
		"RECORD":             e.or("RECORD", ""),
		"THUMB":              e.or("THUMB", "1"),
		"THUMB_SIZE":         e.or("THUMB_SIZE", "320x180"),
		"THUMB_EVERY":        e.or("THUMB_EVERY", "5"),
		"OUTPUT_PROTOCOL":    e.or("OUTPUT_PROTOCOL", ""),
		"OUTPUT_URL":         e.or("OUTPUT_URL", ""),
		"OUTPUT_KEY":         e.or("OUTPUT_KEY", ""),
		"OUTPUT_PASSPHRASE":  e.or("OUTPUT_PASSPHRASE", ""),
		"OUTPUT_USER":        e.or("OUTPUT_USER", ""),
		"OUTPUT_PASSWORD":    e.or("OUTPUT_PASSWORD", ""),
		"RIST_PROFILE":       e.or("RIST_PROFILE", "main"),
		"OUTPUT_LATENCY":     e.or("OUTPUT_LATENCY", "1000"),
		"OUTPUT_VCODEC":      e.or("OUTPUT_VCODEC", "h264"),
		"OUTPUT_SIZE":        e.or("OUTPUT_SIZE", ""),
		"OUTPUT_FPS":         e.or("OUTPUT_FPS", "full"),
		"SEND_BIN":           e.or("SEND_BIN", "/usr/local/bin/eirl-send"),
		"RIST_BIN":           e.or("RIST_BIN", "/usr/local/bin/ristsender"),
		"RIST_LOCAL_PORT":    e.or("RIST_LOCAL_PORT", "6100"),
	}
	raw["DEVICE_TIMEOUT"] = e.or("DEVICE_TIMEOUT", raw["SIGNAL_TIMEOUT"])
	uplinksDisabled, uplinksNoInternet := "", ""
	if c.ConfPath != "" {
		uplinksDisabled = shDirname(c.ConfPath) + "/uplinks-disabled"
		uplinksNoInternet = "/run/eirl-encoder/uplinks-nointernet"
	}
	raw["UPLINKS_DISABLED"] = e.unsetOr("UPLINKS_DISABLED", uplinksDisabled)
	raw["UPLINKS_NOINTERNET"] = e.unsetOr("UPLINKS_NOINTERNET", uplinksNoInternet)
	linksFile := ""
	if c.ConfPath != "" {
		linksFile = "/run/eirl-encoder/links.json"
	}
	raw["LINKS_FILE"] = e.unsetOr("LINKS_FILE", linksFile)
	raw["SRT_STATS"] = e.unsetOr("SRT_STATS", raw["RUN_DIR"]+"/srt-stats.json")
	raw["THUMB_FILE"] = e.unsetOr("THUMB_FILE", raw["RUN_DIR"]+"/thumb.jpg")
	if raw["VDEV"] == "" {
		raw["VDEV"] = o.FindVDEV()
	}

	if err := validate(raw, o); err != nil {
		return nil, err
	}
	c.fill(raw)
	mns, _ := o.LookupEnv("MIXER_NET_SYNC")
	c.MixerNetSyncMs = NetSyncMs(mns)
	return c, nil
}

// validate applies the launcher's checks in its order (sh:173-218, 626-630, 693-694), so the same
// key is reported first. raw["EDID"] gets its default once EDID_PREFERRED is known (sh:184).
func validate(raw map[string]string, o Options) error {
	var err error
	re := func(k string, r *regexp.Regexp) {
		if err == nil && !r.MatchString(raw[k]) {
			err = invalid(k, "")
		}
	}
	for _, k := range uintKeys {
		re(k, reUint6)
	}
	re("FPS", reOptUint4)
	re("GOP", reOptUint4)
	re("H264_PRESET", reOneToFour)
	re("H264_TWOPASS", reBit)
	re("H264_QP_RANGE", reQPRange)
	for _, k := range []string{"LEVELS_PORT", "LIVE_VPORT", "LIVE_APORT", "SRTLA_LOCAL_PORT"} {
		if err != nil {
			break
		}
		if n, ok := bashArith(raw[k]); !ok || n < 1 || n > 65535 {
			err = invalid(k, "")
		}
	}
	re("RES", reOptSize)
	re("LIVE_SIZE", reSize)
	re("EDID_PREFERRED", reEDID)
	if err != nil {
		return err
	}
	if raw["EDID"] == "" {
		raw["EDID"] = o.ScriptDir + "/edid-" + raw["EDID_PREFERRED"] + "-lpcm48k.bin"
	}
	for _, k := range []string{"VDEV", "EDID", "ALSA_CARD", "STATE_DIR", "RUN_DIR", "SRTLA_BIN", "UPLINKS_FILE", "PROBE_BIN"} {
		re(k, rePath)
	}
	re("SOURCE", reSource)
	re("VIDEO_DEVICE", reVideoDevice)
	re("VIDEO_FORMAT", reVideoFormat)
	re("VIDEO_MODE", reVideoMode)
	re("AUDIO_SOURCE", reAudioSource)
	for _, k := range []string{"RECORD", "SRT_STATS", "UPLINKS_DISABLED", "UPLINKS_NOINTERNET", "LINKS_FILE"} {
		re(k, reOptPath)
	}
	for _, k := range []string{"OVERLAY", "LEVELS", "LIVE", "ABR", "MIXER", "MIXER_SCREEN"} {
		re(k, reBit)
	}
	re("MIXER_SLOTS", reOneToFour)
	re("MIXER_LATENCY", reUint4)
	re("MIXER_AUDIO_SLOTS", reOneToFour)
	re("MIXER_SYNC", reUint3)
	re("MIXER_HDMI_CAPTURE", reHDMICapture)
	re("OUTPUT", reOutput)
	re("SRT_MODE", reSRTMode)
	re("SRT_STREAMID", reStreamID)
	re("SRT_PASSPHRASE", rePassphrase)
	if err != nil {
		return err
	}
	if raw["ABR"] == "1" {
		lo, ok1 := bashArith(raw["VBITRATE_MIN"])
		hi, ok2 := bashArith(raw["VBITRATE"])
		if !ok1 || !ok2 || lo < 200 || lo > hi {
			return invalid("VBITRATE_MIN", "between 200 and VBITRATE")
		}
	}
	// Destination (sh:205-218). With the mixer an empty SRT_URL is allowed (Emit answers
	// no_destination); a non-empty one is always checked.
	if u := raw["SRT_URL"]; strings.HasPrefix(u, "srtla://") {
		re("SRT_URL", reSRTLAURL)
		if err != nil {
			return err
		}
		if raw["SRT_MODE"] != "caller" {
			return invalid("SRT_MODE", "SRTLA only sends to a relay: caller")
		}
		if !o.Executable(raw["SRTLA_BIN"]) {
			return invalid("SRTLA_BIN", "srtla_send is not installed")
		}
	} else if u != "" {
		re("SRT_URL", reSRTURL)
	}
	if err == nil {
		err = validateOutput(raw)
	}
	if err == nil && raw["AENC"] != "voaacenc" && raw["AENC"] != "avenc_aac" {
		err = invalid("AENC", "voaacenc or avenc_aac")
	}
	re("THUMB", reBit)
	re("THUMB_SIZE", reThumbSize)
	re("THUMB_EVERY", reUint4)
	re("THUMB_FILE", reOptPath)
	if err == nil {
		if n, ok := bashArith(raw["THUMB_EVERY"]); !ok || n < 1 {
			err = invalid("THUMB_EVERY", "at least 1 s")
		}
	}
	return err
}

// fill converts the validated raw values. Integers are decimal (see bashArith for the one place
// where the launcher read them as octal).
func (c *Config) fill(raw map[string]string) {
	num := func(k string) int { n, _ := strconv.Atoi(raw[k]); return n }
	bit := func(k string) bool { return raw[k] == "1" }

	c.SingleSource = raw["MIXER"] == "0"
	c.Source = raw["SOURCE"]
	c.VDEV = raw["VDEV"]
	c.VideoDevice = raw["VIDEO_DEVICE"]
	c.VideoFormat = raw["VIDEO_FORMAT"]
	c.VideoMode = raw["VIDEO_MODE"]
	c.AudioSource = raw["AUDIO_SOURCE"]
	c.AudioEnv = raw["AUDIO"]

	c.MixerSlots = num("MIXER_SLOTS")
	c.MixerLatencyMs = num("MIXER_LATENCY")
	c.MixerSyncMs = num("MIXER_SYNC")
	c.MixerScreen = bit("MIXER_SCREEN")
	c.MixerHDMICapture = raw["MIXER_HDMI_CAPTURE"]
	c.MixerAudioSlots = num("MIXER_AUDIO_SLOTS")
	c.ProbeBin = raw["PROBE_BIN"]

	c.EDIDPreferred = raw["EDID_PREFERRED"]
	c.EDID = raw["EDID"]
	c.ALSACard = raw["ALSA_CARD"]
	c.ALSAAdmaif = num("ALSA_ADMAIF")
	c.SignalTimeout = num("SIGNAL_TIMEOUT")
	c.DeviceTimeout = num("DEVICE_TIMEOUT")
	c.WatchdogMs = num("WATCHDOG")
	c.Overlay = bit("OVERLAY")
	c.TCI2C = num("TC_I2C")
	c.AudioLockTimeout = num("AUDIO_LOCK_TIMEOUT")
	c.HWResetInterval = num("HW_RESET_INTERVAL")
	c.StateDir = raw["STATE_DIR"]
	c.RunDir = raw["RUN_DIR"]

	c.Res = raw["RES"]
	c.FPS, c.FPSSet = num("FPS"), raw["FPS"] != ""
	c.GOP, c.GOPSet = num("GOP"), raw["GOP"] != ""

	c.VBitrate = num("VBITRATE")
	c.VBitrateMin = num("VBITRATE_MIN")
	c.ABR = bit("ABR")
	c.ABitrate = num("ABITRATE")
	c.AENC = raw["AENC"]
	c.Profile = num("PROFILE")
	c.H264Preset = num("H264_PRESET")
	c.H264QPRange = raw["H264_QP_RANGE"]
	c.H264TwoPass = bit("H264_TWOPASS")
	c.OutputAtBoot = raw["OUTPUT"] == "on"
	c.Record = raw["RECORD"]

	c.SRTURL = raw["SRT_URL"]
	c.SRTMode = raw["SRT_MODE"]
	c.SRTLatencyMs = num("SRT_LATENCY")
	c.SRTPassphrase = NewSecret(raw["SRT_PASSPHRASE"])
	c.SRTStreamID = NewSecret(raw["SRT_STREAMID"])
	c.Transport = raw["OUTPUT_PROTOCOL"]
	if c.Transport == "" {
		c.Transport = "srt"
		if strings.HasPrefix(c.SRTURL, "srtla://") {
			c.Transport = "srtla"
		}
	}
	c.OutputURL = raw["OUTPUT_URL"]
	c.OutputKey = NewSecret(raw["OUTPUT_KEY"])
	c.OutputPassphrase = NewSecret(raw["OUTPUT_PASSPHRASE"])
	c.OutputUser = raw["OUTPUT_USER"]
	c.OutputPassword = NewSecret(raw["OUTPUT_PASSWORD"])
	c.RISTProfile = raw["RIST_PROFILE"]
	c.OutputLatencyMs = num("OUTPUT_LATENCY")
	c.VCodec = raw["OUTPUT_VCODEC"]
	c.OutputSize = raw["OUTPUT_SIZE"]
	c.OutputHalf = raw["OUTPUT_FPS"] == "half"
	c.SendBin = raw["SEND_BIN"]
	c.RISTBin = raw["RIST_BIN"]
	c.RISTLocalPort = num("RIST_LOCAL_PORT")
	c.LinksFile = raw["LINKS_FILE"]
	c.SRTLABin = raw["SRTLA_BIN"]
	c.SRTLALocalPort = num("SRTLA_LOCAL_PORT")
	c.UplinksFile = raw["UPLINKS_FILE"]
	c.UplinkWait = num("UPLINK_WAIT")
	c.UplinksDisabled = raw["UPLINKS_DISABLED"]
	c.UplinksNoInternet = raw["UPLINKS_NOINTERNET"]
	c.SRTStats = raw["SRT_STATS"]

	c.Levels = bit("LEVELS")
	c.LevelsPort = num("LEVELS_PORT")
	c.Live = bit("LIVE")
	c.LiveSize = raw["LIVE_SIZE"]
	c.LiveVBitrate = num("LIVE_VBITRATE")
	c.LiveABitrate = num("LIVE_ABITRATE")
	c.LiveVPort = num("LIVE_VPORT")
	c.LiveAPort = num("LIVE_APORT")

	c.Thumb = bit("THUMB")
	c.ThumbSize = raw["THUMB_SIZE"]
	c.ThumbEvery = num("THUMB_EVERY")
	c.ThumbFile = raw["THUMB_FILE"]
}

// NetSyncMs is engine_net.sync_ms (engine_net.py:57-61): digits and ≤ 1000, else DefaultNetSyncMs.
// Only ASCII digits count (Python's str.isdigit also takes other Unicode digits).
func NetSyncMs(v string) int {
	if v == "" {
		return DefaultNetSyncMs
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return DefaultNetSyncMs
		}
	}
	t := strings.TrimLeft(v, "0")
	if len(t) > 4 {
		return DefaultNetSyncMs
	}
	n, _ := strconv.Atoi("0" + t)
	if n > 1000 {
		return DefaultNetSyncMs
	}
	return n
}

// bashArith is how bash's (( )) reads a value that already matched ^[0-9]+$: a leading 0 means
// octal and an 8 or 9 after it is an error ("value too great for base"), which the launcher turns
// into "configuración no válida" for the range checks (ports, VBITRATE_MIN, THUMB_EVERY).
func bashArith(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	if len(s) > 1 && s[0] == '0' {
		base = 8
	}
	n, err := strconv.ParseInt(s, base, 64)
	return n, err == nil
}

func (o Options) withDefaults() Options {
	if o.LookupEnv == nil {
		o.LookupEnv = os.LookupEnv
	}
	if o.ReadFile == nil {
		o.ReadFile = os.ReadFile
	}
	if o.Executable == nil {
		o.Executable = func(p string) bool { return access(p, accessX) }
	}
	if o.FindVDEV == nil {
		o.FindVDEV = findTC358743
	}
	if o.ScriptDir == "" {
		o.ScriptDir = DefaultScriptDir
	}
	return o
}

// findTC358743 is sh:142-147: the first /sys/class/video4linux/video*/name (glob order) that
// contains "tc358743", else /dev/video0.
func findTC358743() string {
	names, _ := filepath.Glob("/sys/class/video4linux/video*/name")
	for _, n := range names {
		if b, err := os.ReadFile(n); err == nil && strings.Contains(string(b), "tc358743") {
			return "/dev/" + filepath.Base(filepath.Dir(n))
		}
	}
	return "/dev/video0"
}

// access(2) modes, as bash's [ -w ] and [ -x ] use them.
const (
	accessW = 0x2
	accessX = 0x1
)

func access(path string, mode uint32) bool { return syscall.Access(path, mode) == nil }

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// shDirname is coreutils dirname: "engine.env" → ".", "/a/b/" → "/a", "/" → "/".
func shDirname(p string) string {
	t := strings.TrimRight(p, "/")
	if t == "" {
		if p == "" {
			return "."
		}
		return "/"
	}
	i := strings.LastIndex(t, "/")
	if i < 0 {
		return "."
	}
	if d := strings.TrimRight(t[:i], "/"); d != "" {
		return d
	}
	return "/"
}

// shBasename is coreutils `basename path suffix`: the suffix goes only when it is not the whole name.
func shBasename(p, suffix string) string {
	t := strings.TrimRight(p, "/")
	if t == "" {
		if p == "" {
			return ""
		}
		return "/"
	}
	b := t[strings.LastIndex(t, "/")+1:]
	if suffix != "" && b != suffix && strings.HasSuffix(b, suffix) {
		b = strings.TrimSuffix(b, suffix)
	}
	return b
}

// pyDirname is Python's os.path.dirname: "engine.env" → "", "/a/b" → "/a", "/a" → "/".
func pyDirname(p string) string {
	head := p[:strings.LastIndex(p, "/")+1]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}
