package cloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
)

// Device: what the cloud client needs from the device. eirl-encoderd implements it with the Jetson
// (api.CloudDevice) and eirl-encoder-sim with a simulated one.
type Device interface {
	Info() DeviceInfo
	// LanURLs: how the local web is reached from the LAN (reported in hello).
	LanURLs() []string
	// Status: the current snapshot, without secrets.
	Status(ctx context.Context) EncoderStatus
	// Reported: the effective configuration, without secrets. AccessPoint: the real access point policy
	// ("Create my Wi-Fi", WIFI.md §7) if the device has one; empty, the client fills in the one it stored
	// from the cloud.
	Reported() (ReportedConfig, error)
	// ApplyConfig validates (ApplyWire), saves and, with apply, applies. Validation errors are
	// config.FieldErrors with wire paths. With ctx cancelled (unlinked) it writes nothing.
	// ApplyResult.Saved says whether the .env was written even if something failed afterwards.
	ApplyConfig(ctx context.Context, w WireConfig, apply bool) (ApplyResult, error)
	// Action: start | stop | rehearsal | restart | reboot | set_local_password. Errors meant for the
	// user as *ActionError.
	Action(ctx context.Context, name, password string) error
	// CanReboot: nil if the device can be rebooted remotely (the means and the permission exist).
	// Checked BEFORE answering ok to `reboot`.
	CanReboot(ctx context.Context) error
	// Thumbnail: the latest thumbnail (ok=false if there is none or it is stale).
	Thumbnail() (Thumbnail, bool)
	// Follow sends the new lines of the streaming journal until ctx is cancelled.
	Follow(ctx context.Context, fn func(service.Entry)) error
	// Uplinks: names of the available internet uplinks (cheap: checked every few seconds).
	Uplinks() []string
	// Unlinked: the device is no longer linked. clearDestination: the cloud set the destination and it
	// must be deleted (and streaming with that key must stop).
	Unlinked(ctx context.Context, clearDestination bool) error
}

// ApplyResult: what happened with a config.set.
type ApplyResult struct {
	Saved     bool // the new configuration is already on disk
	Restarted bool
}

type DeviceInfo struct {
	HardwareID      string // [0-9a-f]{16,64}: the deviceId of /api/v1/info
	Model           string
	FirmwareVersion string
	Name            string // optional: name proposed when linking
	Capabilities    []string
}

// ActionError: an action failure with a stable code and a message for the user.
type ActionError struct{ Code, Message string }

func (e *ActionError) Error() string { return e.Code + ": " + e.Message }

// Thumbnail: the thumbnail JPEG.
type Thumbnail struct {
	JPEG          []byte
	Width, Height int
	At            time.Time
}

// ReadThumbnail reads the thumbnail the engine leaves behind. multifilesink does not write it
// atomically: a half-written JPEG (without the end marker) is dropped and the next one is sent. A
// stale one (streaming stopped) or one that does not fit in the message is not sent either.
func ReadThumbnail(path string, maxAge time.Duration, now time.Time) (Thumbnail, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 4 || fi.Size() > MaxThumbnail || now.Sub(fi.ModTime()) > maxAge {
		return Thumbnail{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > MaxThumbnail || !completeJPEG(b) {
		return Thumbnail{}, false
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 1920 || cfg.Height > 1080 {
		return Thumbnail{}, false
	}
	return Thumbnail{JPEG: b, Width: cfg.Width, Height: cfg.Height, At: fi.ModTime().UTC()}, true
}

func completeJPEG(b []byte) bool {
	return len(b) >= 4 && b[0] == 0xff && b[1] == 0xd8 && b[len(b)-2] == 0xff && b[len(b)-1] == 0xd9
}

// DeviceID: stable device identifier = hash of /etc/machine-id (not exposed as is).
// "" if it cannot be read.
func DeviceID(machineIDPath string) string {
	b, err := os.ReadFile(machineIDPath)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return ""
	}
	h := sha256.Sum256([]byte("eirl-encoder:" + strings.TrimSpace(string(b))))
	return hex.EncodeToString(h[:8])
}

var (
	reHardwareID = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
	reLabel      = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

// LanURLs: http://<hostname>.local and the interfaces' private or link-local IPv4s, up to 4
// (§3.1). port 0 or 80 = no port in the URL.
var _, apNet, _ = net.ParseCIDR(netcfg.APNet)

func LanURLs(hostname string, port int) []string {
	suffix := ""
	if port != 0 && port != 80 {
		suffix = ":" + strconv.Itoa(port)
	}
	var out []string
	if h := strings.ToLower(hostname); reLabel.MatchString(h) {
		out = append(out, "http://"+h+".local"+suffix)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := n.IP.To4()
		if ip == nil || !(ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			continue
		}
		// The cameras' Wi-Fi (F7): only reachable from it, not from the user's network
		if apNet.Contains(ip) {
			continue
		}
		if len(out) >= 4 {
			break
		}
		out = append(out, "http://"+ip.String()+suffix)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// ValidateBase: the backend base URL. Always https, except http against loopback (tests) or with
// allowHTTP (the simulator against a local backend). No user, query or fragment.
func ValidateBase(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid URL (https://host[/path])")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !allowHTTP && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("https only (http only against localhost)")
		}
	default:
		return fmt.Errorf("scheme %q not supported", u.Scheme)
	}
	return nil
}

func joinBase(base, path string) string { return strings.TrimRight(base, "/") + path }

func wsURL(base string) string {
	u := joinBase(base, "/encoders/connect")
	if strings.HasPrefix(u, "https://") {
		return "wss://" + strings.TrimPrefix(u, "https://")
	}
	return "ws://" + strings.TrimPrefix(u, "http://")
}

// backoff: exponential wait min·2^n up to max, with ±30 % jitter (§4).
func backoff(attempt int, min, max time.Duration) time.Duration {
	d := min
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return time.Duration(float64(d) * (0.7 + 0.6*rand.Float64()))
}

// FormatCode: the pairing code is shown as XXXX-XXXX. Accepts the code with or without the dash; ""
// if it is not 8 alphanumeric characters.
func FormatCode(code string) string {
	c := strings.ToUpper(strings.ReplaceAll(code, "-", ""))
	if len(c) != 8 {
		return ""
	}
	for _, r := range c {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return c[:4] + "-" + c[4:]
}

// cleanText: text ≤ max without control characters (model, firmwareVersion, name, accountLabel).
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	if len(s) > max {
		s = s[:max] // a character cut in half is removed by ToValidUTF8
	}
	return strings.ToValidUTF8(s, "")
}
