// Package netcam is what the engine asks the local MediaMTX about network cameras (F7,
// NETWORK_SOURCES.md §6): is a camera publishing, with which codecs, and the camera list's stamp. The
// app owns MediaMTX and the list; the engine only reads.
package netcam

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	API     = "http://127.0.0.1:9997" // MediaMTX API, localhost only (netcfg.APIPort)
	RTSP    = "rtsp://127.0.0.1:8554/"
	APIUser = "eirl-app" // netcfg.APIUser
	// JudgeLimit: their timestamps come from RTP, believable with more margin
	JudgeLimit = 400 * time.Millisecond
	// MaxLag: what arrives carries seconds of backlog (the CPU cannot keep up): reopen to empty it
	MaxLag = 2 * time.Second
)

var (
	reID     = regexp.MustCompile(`^net:cam[1-8]$`)
	rePath   = regexp.MustCompile(`^cam[1-8]$`)
	reAPIKey = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	reWidth  = regexp.MustCompile(`width=\(int\)(\d+)`)
	reHeight = regexp.MustCompile(`height=\(int\)(\d+)`)
	reRate   = regexp.MustCompile(`framerate=\(fraction\)(\d+)/(\d+)`)
)

// Video and audio codecs MediaMTX names, and the short name the engine uses.
var (
	VideoCodecs = map[string]string{"H264": "h264", "H265": "h265"}
	AudioCodecs = map[string]string{"MPEG-4 Audio": "aac", "Opus": "opus"}
)

// Valid is a network camera of the catalogue: id net:camN and its exact read URL.
func Valid(id, node string) bool {
	return reID.MatchString(id) && node == RTSP+id[4:]
}

// PathOf: net:cam1 → cam1 (the MediaMTX path).
func PathOf(id string) string {
	if len(id) < 4 {
		return ""
	}
	return id[4:]
}

// CleanName is the name the user gave, for the log, the web and the screen (≤48): letters (with accents),
// digits and ` ._()'+-`.
func CleanName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || strings.ContainsRune(" ._()'+-", r) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if r := []rune(s); len(r) > 48 {
		s = string(r[:48])
	}
	if s = strings.TrimSpace(s); s == "" {
		return "Cámara wifi"
	}
	return s
}

// Path is the state of a MediaMTX path.
type Path struct {
	Ready     bool
	Video     string // h264 | h265 | "" (none, or not readable)
	Audio     string // aac | opus | ""
	Publisher string
	Tracks    []string
}

// ParsePath reads GET /v3/paths/get/<path>. ok=false if it is not an object.
func ParsePath(body []byte) (Path, bool) {
	var d map[string]any
	if json.Unmarshal(body, &d) != nil || d == nil {
		return Path{}, false
	}
	var p Path
	if ts, ok := d["tracks"].([]any); ok {
		for _, t := range ts {
			if s, ok := t.(string); ok {
				p.Tracks = append(p.Tracks, s)
			}
		}
	}
	for _, t := range p.Tracks {
		if c, ok := VideoCodecs[t]; ok && p.Video == "" {
			p.Video = c
		}
		if c, ok := AudioCodecs[t]; ok && p.Audio == "" {
			p.Audio = c
		}
	}
	if src, ok := d["source"].(map[string]any); ok {
		if id, ok := src["id"].(string); ok && len(id) <= 64 {
			p.Publisher = id
		}
	}
	p.Ready = d["ready"] == true
	return p, true
}

// Client asks MediaMTX. KeyFile is media-api.key next to the app's config.
type Client struct {
	KeyFile string
	HTTP    *http.Client // nil: 1 s timeout
	BaseURL string       // "" = API

	mu       sync.Mutex
	keyStamp string
	key      string
}

// key reads media-api.key again only when it changes (path, mtime, size).
func (c *Client) apiKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := os.Stat(c.KeyFile)
	if err != nil {
		c.keyStamp, c.key = "", ""
		return ""
	}
	stamp := c.KeyFile + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 10) + "|" + strconv.FormatInt(st.Size(), 10)
	if stamp != c.keyStamp {
		k := ""
		if f, err := os.Open(c.KeyFile); err == nil {
			b := make([]byte, 64)
			n, _ := io.ReadFull(f, b)
			f.Close()
			if s := strings.TrimSpace(string(b[:n])); reAPIKey.MatchString(s) {
				k = s
			}
		}
		c.keyStamp, c.key = stamp, k
	}
	return c.key
}

// ErrUnavailable: MediaMTX does not answer (off: no cameras, or the access point down) or refuses.
var ErrUnavailable = errors.New("camera receiver unavailable")

// State says whether the camera is publishing and with what. A path that does not exist (404: the
// camera was just removed) is like no publisher.
func (c *Client) State(path string) (Path, error) {
	if !rePath.MatchString(path) {
		return Path{}, ErrUnavailable
	}
	base := c.BaseURL
	if base == "" {
		base = API
	}
	req, err := http.NewRequest(http.MethodGet, base+"/v3/paths/get/"+path, nil)
	if err != nil {
		return Path{}, ErrUnavailable
	}
	if k := c.apiKey(); k != "" {
		req.SetBasicAuth(APIUser, k)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Path{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Path{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Path{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Path{}, ErrUnavailable
	}
	p, ok := ParsePath(body)
	if !ok {
		return Path{}, ErrUnavailable
	}
	return p, nil
}

// CamerasFile is the app's camera list without keys, next to its config (EIRL_CONF's directory).
func CamerasFile(confDir string) string { return filepath.Join(confDir, "cameras-public.json") }

// KeyFile is the MediaMTX API key next to the app's config.
func KeyFile(confDir string) string { return filepath.Join(confDir, "media-api.key") }

// Stamp is (mtime, size) of a file, "" if it does not exist: the engine probes again when it changes.
func Stamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(st.ModTime().UnixNano(), 10) + "|" + strconv.FormatInt(st.Size(), 10)
}

// DecodedMode reads (width, height, fps) from the decoder's caps string; ok=false without a size.
func DecodedMode(caps string) (w, h int, fps float64, ok bool) {
	mw, mh := reWidth.FindStringSubmatch(caps), reHeight.FindStringSubmatch(caps)
	if mw == nil || mh == nil {
		return 0, 0, 0, false
	}
	w, _ = strconv.Atoi(mw[1])
	h, _ = strconv.Atoi(mh[1])
	if f := reRate.FindStringSubmatch(caps); f != nil {
		n, _ := strconv.Atoi(f[1])
		d, _ := strconv.Atoi(f[2])
		if d != 0 {
			fps = float64(n) / float64(d)
		}
	}
	return w, h, fps, true
}

// ModeText is "WxH@F" (fps without trailing zeros) or "WxH" without fps.
func ModeText(w, h int, fps float64) string {
	if fps == 0 {
		return strconv.Itoa(w) + "x" + strconv.Itoa(h)
	}
	f := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(fps, 'f', 2, 64), "0"), ".")
	return strconv.Itoa(w) + "x" + strconv.Itoa(h) + "@" + f
}
