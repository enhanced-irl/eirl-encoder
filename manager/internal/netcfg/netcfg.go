// Package netcfg: the encoder's camera network (F7, NETWORK_SOURCES.md §6). The configuration
// files, their validation and what is generated from them (the MediaMTX configuration, the access
// point profile and how each camera connects).
//
// Two pieces with different privileges use it:
//   - eirl-encoderd (user eirl-encoder) WRITES the files in /etc/eirl-encoder;
//   - eirl-netsetup (root) READS them as data with these same rules. Whatever does not fit is
//     rejected: nothing is ever executed or interpolated outside the allowlists.
package netcfg

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Access point network. Fixed: the firewall, the DHCP server, MediaMTX and what the web shows use them.
const (
	APIface   = "ap0"
	APAddr    = "10.42.0.1"
	APPrefix  = 24
	APNet     = "10.42.0.0/24"
	DHCPFirst = "10.42.0.10"
	DHCPLast  = "10.42.0.99"
	RTMPPort  = 1935
	SRTPort   = 8890
	RTSPPort  = 8554
	APIPort   = 9997
	// UDP ports of the WebRTC preview (live.Relay): the AP firewall only opens these
	PreviewPortMin = 50000
	PreviewPortMax = 50099
	// LANIface: the one for wired cameras (§6.11). Only this one, by name: a USB-ethernet adapter is
	// usually a phone sharing its data (an SRTLA uplink), and nobody gets in through there
	LANIface = "eth0"
	// AnyAddr: where RTMP and SRT listen with the cable on (MediaMTX accepts one address per
	// protocol); the EIRL-MEDIA firewall drops whatever does not arrive through lo, ap0 or eth0
	AnyAddr = "0.0.0.0"
)

// Files (in the app's configuration directory, /etc/eirl-encoder)
const (
	FileCameras = "cameras.json"
	FileNetwork = "network.json"
	FilePSK     = "ap.psk"
	// FileMediaKey: the key of MediaMTX's local API (0640: the app and the engine read it, through the
	// group; MediaMTX only keeps its hash). Without it, the API is open to nobody
	FileMediaKey = "media-api.key"
	// Cap on what is read from each one (eirl-netsetup never reads more than this)
	MaxFileSize = 16 << 10
)

// Cameras: ids cam1…cam8 (fixed: they are the MediaMTX path and the engine's source id, net:camN).
const (
	MaxCameras = 8
	// DefaultMax: how many fit in v1 without the local sources dropping frames (maintainer's
	// decision, 13-oct; NETWORK_SOURCES.md §2)
	DefaultMax  = 1
	KeyLen      = 20
	PSKLen      = 20
	MediaKeyLen = 32
	// APIUser: the MediaMTX API user (with FileMediaKey)
	APIUser = "eirl-app"
)

var (
	ErrInvalid = errors.New("netcfg: invalid configuration")
	ErrLimit   = errors.New("netcfg: no room for more cameras")
	ErrUnknown = errors.New("netcfg: unknown camera")

	reCamID   = regexp.MustCompile(`^cam[1-8]$`)
	reKey     = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)
	rePSK     = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)
	reMKey    = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	reSSID    = regexp.MustCompile(`^[A-Za-z0-9_.-]([A-Za-z0-9 _.-]{0,30}[A-Za-z0-9_.-])?$`)
	reName    = regexp.MustCompile(`^[\p{L}\p{N} ._()'-]{1,32}$`)
	reHwSuffx = regexp.MustCompile(`^[0-9A-Fa-f]{4,}$`)
)

// Camera: a registered network camera. Key is its publishing credential (never shown in /status
// nor in logs; only in GET /network/credentials).
type Camera struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
}

type Cameras struct {
	V       int      `json:"v"`
	Cameras []Camera `json:"cameras"`
}

// AP: the access point. Channel: "auto" or "1"…"11" (2.4 GHz: with iwlwifi all of 5 GHz is No-IR).
// Enabled: the cameras' Wi-Fi; without the field (files from before §6.11), on.
type AP struct {
	SSID    string `json:"ssid"`
	Channel string `json:"channel"`
	Enabled *bool  `json:"enabled,omitempty"`
}

func (a AP) On() bool { return a.Enabled == nil || *a.Enabled }

// LAN: the wired cameras (through eth0's network, §6.11). Off by default.
type LAN struct {
	Enabled bool `json:"enabled"`
}

// Network: Enabled is the network cameras' RTMP/SRT reception (NETWORK_SOURCES.md §6.12): off, neither
// receiver nor mixer source, even if cameras are registered (they are kept with their keys). Without the field
// (files from before §6.12), on. WiFi: the board's Wi-Fi (WIFI.md §2); without the block, the previous behaviour.
type Network struct {
	V       int   `json:"v"`
	Enabled *bool `json:"enabled,omitempty"`
	WiFi    *WiFi `json:"wifi,omitempty"`
	AP      AP    `json:"ap"`
	LAN     *LAN  `json:"lan,omitempty"`
}

func (n Network) On() bool { return n.Enabled == nil || *n.Enabled }

// WiFi: the board's Wi-Fi (WIFI.md §2). AP: «Crear mi wifi» ("Create my Wi-Fi"), APAlways or APRescue (empty =
// not chosen: APPolicy's W4 rule). Client: «Conectarse a una wifi» ("Connect to a Wi-Fi"; phase W-B; without the
// field, on).
type WiFi struct {
	Client *bool  `json:"client,omitempty"`
	AP     string `json:"ap,omitempty"`
}

// Access point policies, the same ones the cloud sends (`accessPoint` of config.set; WIFI.md W7)
const (
	APAlways = "always" // «Crear mi wifi» on: up, with or without cameras
	APRescue = "rescue" // off: only to rescue the board (once rescue mode exists, cloud F3)
)

func ValidAPPolicy(p string) bool { return p == APAlways || p == APRescue }

// APPolicy: the effective access point policy (WIFI.md W4 and W7). The chosen one or, if none was chosen (older
// files), the historical one: APAlways if cameras are registered and «por wifi» ("over Wi-Fi") is on, APRescue
// otherwise. eirl-netsetup, the app and what the app reports to the cloud all use it: a single rule.
func (n Network) APPolicy(hasCameras bool) string {
	if n.WiFi != nil && ValidAPPolicy(n.WiFi.AP) {
		return n.WiFi.AP
	}
	if hasCameras && n.AP.On() {
		return APAlways
	}
	return APRescue
}

// ClientOn: «Conectarse a una wifi» (phase W-B). If not chosen, on: without profiles it connects to nothing.
func (n Network) ClientOn() bool { return n.WiFi == nil || n.WiFi.Client == nil || *n.WiFi.Client }

func (n Network) LANOn() bool { return n.LAN != nil && n.LAN.Enabled }

// ValidCameraID / ValidName / ValidSSID / ValidChannel: the rules of the API and of eirl-netsetup.
func ValidCameraID(id string) bool { return reCamID.MatchString(id) }

func ValidName(n string) bool {
	return utf8.ValidString(n) && reName.MatchString(n) && strings.TrimSpace(n) == n
}

func ValidSSID(s string) bool { return reSSID.MatchString(s) }

func ValidChannel(c string) bool {
	if c == "auto" {
		return true
	}
	n, err := strconv.Atoi(c)
	return err == nil && n >= 1 && n <= 11 && strconv.Itoa(n) == c
}

func (c Camera) valid() bool {
	return ValidCameraID(c.ID) && ValidName(c.Name) && reKey.MatchString(c.Key)
}

// ParseCameras: strict cameras.json (unknown fields, repeated ids or more than MaxCameras →
// ErrInvalid). Empty = no cameras.
func ParseCameras(b []byte) (Cameras, error) {
	out := Cameras{V: 1, Cameras: []Camera{}}
	if len(bytes.TrimSpace(b)) == 0 {
		return out, nil
	}
	if err := strictJSON(b, &out); err != nil || out.V != 1 || len(out.Cameras) > MaxCameras {
		return Cameras{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range out.Cameras {
		if !c.valid() || seen[c.ID] {
			return Cameras{}, ErrInvalid
		}
		seen[c.ID] = true
	}
	sort.Slice(out.Cameras, func(i, j int) bool { return out.Cameras[i].ID < out.Cameras[j].ID })
	return out, nil
}

func (c Cameras) Render() []byte {
	if c.Cameras == nil {
		c.Cameras = []Camera{}
	}
	c.V = 1
	b, _ := json.MarshalIndent(c, "", "  ")
	return append(b, '\n')
}

// Find: the camera with that id.
func (c Cameras) Find(id string) (Camera, bool) {
	for _, x := range c.Cameras {
		if x.ID == id {
			return x, true
		}
	}
	return Camera{}, false
}

// NextID: the first free id (cam1, cam2…), or ErrLimit if there are already max.
func (c Cameras) NextID(max int) (string, error) {
	if len(c.Cameras) >= max || len(c.Cameras) >= MaxCameras {
		return "", ErrLimit
	}
	for i := 1; i <= MaxCameras; i++ {
		id := "cam" + strconv.Itoa(i)
		if _, ok := c.Find(id); !ok {
			return id, nil
		}
	}
	return "", ErrLimit
}

// ParseNetwork: strict network.json. Empty or missing = not configured (ok = false).
func ParseNetwork(b []byte) (n Network, ok bool, err error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return Network{}, false, nil
	}
	if err := strictJSON(b, &n); err != nil || n.V != 1 || !ValidSSID(n.AP.SSID) || !ValidChannel(n.AP.Channel) ||
		(n.WiFi != nil && n.WiFi.AP != "" && !ValidAPPolicy(n.WiFi.AP)) {
		return Network{}, false, ErrInvalid
	}
	return n, true, nil
}

// Render: with the Wi-Fi and cable switches written out (visible in the file, even when they are the
// defaults). The master switch, only when off: that way a file with it on is also valid for an older app,
// which reads it strictly (NETWORK_SOURCES.md §6.12).
func (n Network) Render() []byte {
	n.V = 1
	if n.On() {
		n.Enabled = nil
	} else {
		off := false
		n.Enabled = &off
	}
	on := n.AP.On()
	n.AP.Enabled = &on
	n.LAN = &LAN{Enabled: n.LANOn()}
	if n.WiFi != nil && n.WiFi.Client == nil && n.WiFi.AP == "" {
		n.WiFi = nil // nothing chosen, as before (valid for an older app)
	}
	b, _ := json.MarshalIndent(n, "", "  ")
	return append(b, '\n')
}

// ParsePSK: the Wi-Fi password (one line). Empty = not generated yet.
func ParsePSK(b []byte) (psk string, ok bool, err error) {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", false, nil
	}
	if !rePSK.MatchString(s) {
		return "", false, ErrInvalid
	}
	return s, true, nil
}

// LooseSSID: the access point's name in a network.json that ParseNetwork rejects (another version's fields), if it has a
// valid one; "" otherwise. Only to keep the name when the file is rebuilt: nothing else of that file is trusted.
func LooseSSID(b []byte) string {
	var n struct {
		AP struct {
			SSID string `json:"ssid"`
		} `json:"ap"`
	}
	if len(b) > MaxFileSize || json.Unmarshal(b, &n) != nil || !ValidSSID(n.AP.SSID) {
		return ""
	}
	return n.AP.SSID
}

// DefaultSSID: "EnhancedIRL-" + the last 4 characters of the hardwareId (like the mDNS of cloud phase 3,
// ENCODER_CLOUD.md §8). Without a usable hardwareId, "EnhancedIRL-Cam". The app writes it at its first start
// (network.Manager.EnsureNetwork); from then on, the name is network.json's and can be changed (PUT /network/ap).
func DefaultSSID(hardwareID string) string {
	h := strings.ToUpper(hardwareID)
	if !reHwSuffx.MatchString(h) {
		return "EnhancedIRL-Cam"
	}
	return "EnhancedIRL-" + h[len(h)-4:]
}

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Secret: n uniform alphanumeric characters (crypto/rand). 20 ≈ 119 bits.
// ParseMediaKey: the MediaMTX API key (FileMediaKey). ok = false if there is none.
func ParseMediaKey(b []byte) (key string, ok bool, err error) {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", false, nil
	}
	if !reMKey.MatchString(s) {
		return "", false, ErrInvalid
	}
	return s, true, nil
}

func Secret(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alnum)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alnum[v.Int64()]
	}
	return string(b), nil
}

func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return ErrInvalid
	}
	return nil
}

// --- how a camera connects ---------------------------------------------------------------------

// Connect: what the web shows to configure the camera. SRT is the recommended one (500 ms latency
// on the sender: with 200, Moblin's adaptive bitrate stays under ~0.5 Mbps; NETWORK_SOURCES.md §3); RTMP, for
// those that have nothing else. Those that ask for server and key separately concatenate RTMPServer + RTMPKey.
// At the top level, over the Wi-Fi (10.42.0.1); in LAN, over the cable (eth0's IP), or null if the cable is
// off or eth0 has no IP.
type Connect struct {
	ID string `json:"id"`
	URLs
	LAN *URLs `json:"lan"`
}

type URLs struct {
	SRT        string `json:"srt"`
	RTMP       string `json:"rtmp"`
	RTMPServer string `json:"rtmpServer"`
	RTMPKey    string `json:"rtmpKey"`
}

// Connect: over the Wi-Fi; an empty lanIP (eth0's IPv4) = no wired URL.
func (c Camera) Connect(lanIP string) Connect {
	out := Connect{ID: c.ID, URLs: c.URLsAt(APAddr)}
	if ip := net.ParseIP(lanIP); ip != nil && ip.To4() != nil {
		u := c.URLsAt(ip.To4().String())
		out.LAN = &u
	}
	return out
}

// URLsAt: the URLs to reach MediaMTX at that IPv4.
func (c Camera) URLsAt(host string) URLs {
	q := "?user=" + c.ID + "&pass=" + c.Key
	return URLs{
		SRT:        fmt.Sprintf("srt://%s:%d?streamid=publish:%s:%s:%s", host, SRTPort, c.ID, c.ID, c.Key),
		RTMP:       fmt.Sprintf("rtmp://%s:%d/%s%s", host, RTMPPort, c.ID, q),
		RTMPServer: fmt.Sprintf("rtmp://%s:%d/", host, RTMPPort),
		RTMPKey:    c.ID + q,
	}
}

// ReadURL: where the engine reads from (no credentials: MediaMTX only allows reading from 127.0.0.1).
func ReadURL(id string) string { return fmt.Sprintf("rtsp://127.0.0.1:%d/%s", RTSPPort, id) }

// WifiQR: the QR text to join the Wi-Fi (ZXing's WIFI: format, which the iOS and Android cameras
// read). Escapes \ ; , : and ".
func WifiQR(ssid, psk string) string {
	esc := strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, `:`, `\:`, `"`, `\"`)
	return "WIFI:T:WPA;S:" + esc.Replace(ssid) + ";P:" + esc.Replace(psk) + ";;"
}

// --- MediaMTX ----------------------------------------------------------------------------------

// KeyHash: the key as MediaMTX stores it (base64 sha256 with the "sha256:" prefix). That way its
// configuration carries no key in clear text.
func KeyHash(key string) string {
	s := sha256.Sum256([]byte(key))
	return "sha256:" + base64.StdEncoding.EncodeToString(s[:])
}

// MediaMTXConfig: a FIXED template; from the cameras only their (validated) id and their key's hash go in.
// RTMP and SRT listen on listen: the AP's IP (Wi-Fi only) or AnyAddr (with the cable, §6.11: then publishing
// is not limited to the AP network, and the EIRL-MEDIA firewall is what filters by interface). RTSP and the
// API, only on localhost. Everything else, off (MoQ is on by default in 1.21). No runOn*: MediaMTX executes
// nothing.
// apiKey: the one from FileMediaKey; empty = the API is open to nobody (without it, any process on the board
// could change the configuration on the fly: addresses, users or runOn*).
func MediaMTXConfig(cams []Camera, apiKey, listen string) ([]byte, error) {
	if (apiKey != "" && !reMKey.MatchString(apiKey)) || (listen != APAddr && listen != AnyAddr) {
		return nil, ErrInvalid
	}
	var b strings.Builder
	fmt.Fprintf(&b, `# Written by eirl-netsetup (root) from /etc/eirl-encoder/cameras.json. Do not edit: it is rewritten.
# NETWORK_SOURCES.md §6.3
logLevel: warn
logDestinations: [stdout]
readTimeout: 10s
writeTimeout: 10s
writeQueueSize: 512
udpMaxPayloadSize: 1452
authMethod: internal
authInternalUsers:
  # The engine reads the video from the board itself, without credentials
  - user: any
    pass:
    ips: ["127.0.0.1"]
    permissions:
      - action: read
`)
	if apiKey != "" {
		// The API (camera state, kicking a publisher), only with the key: the app and the engine
		fmt.Fprintf(&b, `  - user: %s
    pass: %s
    ips: ["127.0.0.1"]
    permissions:
      - action: api
`, APIUser, KeyHash(apiKey))
	}
	for _, c := range cams {
		if !c.valid() {
			return nil, ErrInvalid
		}
		ips := ""
		if listen == APAddr {
			ips = fmt.Sprintf("    ips: [%q]\n", APNet)
		}
		fmt.Fprintf(&b, `  - user: %s
    pass: %s
%s    permissions:
      - action: publish
        path: %s
`, c.ID, KeyHash(c.Key), ips, c.ID)
	}
	fmt.Fprintf(&b, `api: true
apiAddress: 127.0.0.1:%d
metrics: false
pprof: false
playback: false
rtsp: true
rtspTransports: [tcp]
rtspEncryption: "no"
rtspAddress: 127.0.0.1:%d
rtmp: true
rtmpEncryption: "no"
rtmpAddress: %s:%d
hls: false
webrtc: false
srt: true
srtAddress: %s:%d
moq: false
pathDefaults:
  source: publisher
  overridePublisher: true
  record: false
paths:
`, APIPort, RTSPPort, listen, RTMPPort, listen, SRTPort)
	if len(cams) == 0 {
		b.WriteString("  {}\n")
	}
	for _, c := range cams {
		fmt.Fprintf(&b, "  %s: {}\n", c.ID)
	}
	return []byte(b.String()), nil
}

// --- access point profile ------------------------------------------------------------------------

// NetworkManager profile of the AP. It goes as a keyfile (0600, root) and not as nmcli arguments: that way
// the password is not visible in the process list. Fixed UUID: the profile is always the same one.
const (
	NMConnID   = "eirl-ap"
	NMConnUUID = "5f0c2b8e-7e1a-4c3d-9b6f-0e4a8d2c1f73"
	NMKeyfile  = "/etc/NetworkManager/system-connections/eirl-ap"
)

// APKeyfile: recipe from ENCODER_CLOUD.md §8 (the cloud spike of 11-oct) and NETWORK_SOURCES.md §1:
// AP mode on ap0, 2.4 GHz, WPA2 (rsn/ccmp), fixed IP without a gateway and WITHOUT `shared` (it leaves
// ip_forward=1 and NAT towards the LAN and the modems), no autoconnect (eirl-netsetup brings it up).
func APKeyfile(ssid, psk string, channel int) ([]byte, error) {
	if !ValidSSID(ssid) || !rePSK.MatchString(psk) || channel < 1 || channel > 11 {
		return nil, ErrInvalid
	}
	return []byte(fmt.Sprintf(`[connection]
id=%s
uuid=%s
type=wifi
interface-name=%s
autoconnect=false
permissions=

[wifi]
band=bg
channel=%d
mode=ap
ssid=%s

[wifi-security]
group=ccmp;
key-mgmt=wpa-psk
pairwise=ccmp;
proto=rsn;
psk=%s

[ipv4]
address1=%s/%d
dns-search=
method=manual
never-default=true

[ipv6]
method=ignore
`, NMConnID, NMConnUUID, APIface, channel, ssid, psk, APAddr, APPrefix)), nil
}

// --- public list for the engine ------------------------------------------------------------------

// FilePublic: the cameras WITHOUT keys (id and name), 0640: read by the source catalogue
// (eirl-encoderd probe -list) that the engine launches; the engine is in the group but must not read the
// keys (cameras.json and ap.psk are 0600).
const FilePublic = "cameras-public.json"

type PublicCamera struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Public struct {
	V       int            `json:"v"`
	Cameras []PublicCamera `json:"cameras"`
}

func (c Cameras) Public() Public {
	p := Public{V: 1, Cameras: []PublicCamera{}}
	for _, x := range c.Cameras {
		p.Cameras = append(p.Cameras, PublicCamera{ID: x.ID, Name: x.Name})
	}
	return p
}

func (p Public) Render() []byte {
	p.V = 1
	if p.Cameras == nil {
		p.Cameras = []PublicCamera{}
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return append(b, '\n')
}

// ParsePublic: strict, like ParseCameras.
func ParsePublic(b []byte) (Public, error) {
	out := Public{V: 1, Cameras: []PublicCamera{}}
	if len(bytes.TrimSpace(b)) == 0 {
		return out, nil
	}
	if err := strictJSON(b, &out); err != nil || out.V != 1 || len(out.Cameras) > MaxCameras {
		return Public{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range out.Cameras {
		if !ValidCameraID(c.ID) || !ValidName(c.Name) || seen[c.ID] {
			return Public{}, ErrInvalid
		}
		seen[c.ID] = true
	}
	sort.Slice(out.Cameras, func(i, j int) bool { return out.Cameras[i].ID < out.Cameras[j].ID })
	return out, nil
}
