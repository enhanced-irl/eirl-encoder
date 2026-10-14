// Package network: the network cameras, their access point and the wired cameras, as seen from the
// app (F7, NETWORK_SOURCES.md §6 and §6.11). It adds, changes and removes cameras and the network in
// /etc/eirl-encoder (netcfg), asks eirl-netsetup (root, sudo with an exact rule) to bring it up and
// reports how each camera is doing through MediaMTX's local API.
//
// The cameras' keys and the wifi password only leave through Credentials (the web shows them when
// «Show» is pressed): never in Status, which also goes to /status and to the cloud.
package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/wifi"
)

type Manager struct {
	Dir        string                                                 // /etc/eirl-encoder
	StatePath  string                                                 // /run/eirl-ap/state.json (left by eirl-netsetup)
	MediaAPI   string                                                 // http://127.0.0.1:9997
	HardwareID string                                                 // for the default SSID (EnhancedIRL-XXXX)
	Max        int                                                    // maximum cameras (netcfg.DefaultMax)
	Apply      func(ctx context.Context) error                        // sudo -n systemctl start eirl-ap.service
	Write      func(path string, data []byte, mode os.FileMode) error // atomic write
	Clients    func(ctx context.Context) (int, bool)                  // devices on the wifi (nil = iw)
	LANAddr    func() string                                          // eth0's IPv4 (nil = the system's; "" = no IP)
	// ScreenFile: the network for the touch screen's «Wifi» panel (/run/eirl-encoder/wifi.json,
	// 0640: the engine reads it through the group). It carries the password: the screen shows it
	// (whoever stands in front of it can join a camera). Not the cameras' keys. Empty = no panel.
	ScreenFile string
	Now        func() time.Time
	// Client: the client wifi (wlan0, through NetworkManager; WIFI.md §4). nil = none
	Client *wifi.Client
	// NoInternetFile: /run/eirl-encoder/uplinks-nointernet, the client wifi without internet (the engine leaves it out
	// of the SRTLA split; WIFI.md §13). Empty = not written
	NoInternetFile string

	mu       sync.Mutex // serializes read, change, write and apply
	applying atomic.Bool
	cmu      sync.Mutex
	prev     map[string]sample // bytes received per camera, for the kbps
	hcOnce   sync.Once
	hc       *http.Client
}

type sample struct {
	bytes uint64
	at    time.Time
}

var (
	ErrInvalid = netcfg.ErrInvalid
	ErrLimit   = netcfg.ErrLimit
	ErrUnknown = netcfg.ErrUnknown
	ErrApply   = errors.New("network: could not apply the network")
	// ErrNoInput: switching off the wifi and the cable at once (the cameras would have no way in)
	ErrNoInput = errors.New("network: neither wifi nor cable")
	// ErrDisabled: adding a camera with the main switch off (it would never get in)
	ErrDisabled = errors.New("network: network cameras switched off")
)

// --- status (no secrets) ------------------------------------------------------------------------

type APStatus struct {
	Enabled bool    `json:"enabled"` // the reception's «over wifi» switch (§6.11): cameras through the AP
	Policy  string  `json:"policy"`  // «Create my wifi»: always (up) | rescue (down except for rescue). WIFI.md W7
	State   string  `json:"state"`   // off | starting | on | error
	Mode    *string `json:"mode"`    // normal (formerly cameras) | null
	SSID    *string `json:"ssid"`    // the name in eirl-netsetup's state (the one it is up, or failed, with): null with it off
	// Name: the AP's name as saved (network.json), also with it off: the one it comes up with and the one to change
	// (PUT /network/ap {"ssid"}). Null before network.json exists (an app older than EnsureNetwork's first start)
	Name    *string `json:"name"`
	Channel *int    `json:"channel"`
	Band    string  `json:"band"` // 2.4
	IP      string  `json:"ip"`
	Clients int     `json:"clients"`
	Error   string  `json:"error,omitempty"`
}

// LANStatus: the wired cameras (over eth0's network, §6.11).
type LANStatus struct {
	Enabled bool    `json:"enabled"`
	State   string  `json:"state"` // off | starting | on | error
	Iface   string  `json:"iface"` // eth0
	IP      *string `json:"ip"`    // its IPv4 (the one the cameras point at), or null
	Error   string  `json:"error,omitempty"`
}

type CameraStatus struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Connected bool      `json:"connected"`
	Protocol  *string   `json:"protocol"` // srt | rtmp
	Codec     *string   `json:"codec"`    // h264 | h265
	Remote    *string   `json:"remote"`   // sender's IP (on the wifi or on eth0's network)
	Via       *string   `json:"via"`      // wifi | lan: the way it comes in (null without a sender)
	Kbps      *int      `json:"kbps"`
	RTTMs     *float64  `json:"rttMs"`   // SRT only
	LossPct   *float64  `json:"lossPct"` // SRT only
}

type CamerasStatus struct {
	Max   int            `json:"max"`
	Items []CameraStatus `json:"items"`
}

type Status struct {
	// Enabled: the RTMP/SRT reception (§6.12). Off, MediaMTX is stopped (it is not queried: the cameras show
	// without a sender) and the cable, «off»; the board's wifi stays as «over wifi» says
	Enabled     bool          `json:"enabled"`
	AccessPoint APStatus      `json:"accessPoint"`
	LAN         LANStatus     `json:"lan"`
	Cameras     CamerasStatus `json:"cameras"`
}

// Status: the network snapshot. It queries the wifi with the AP up, and MediaMTX if there is any camera and the
// reception is on.
func (m *Manager) Status(ctx context.Context) Status {
	cams, _ := m.readCameras()
	ap, lan := m.states()
	st := Status{Enabled: m.network().On(), AccessPoint: ap, LAN: lan, Cameras: CamerasStatus{Max: m.max(), Items: []CameraStatus{}}}
	// The devices on the wifi, with the AP up (with or without cameras: WIFI.md W3)
	if st.AccessPoint.State == "on" {
		if n, ok := m.clients(ctx); ok {
			st.AccessPoint.Clients = n
		}
	}
	if len(cams.Cameras) == 0 {
		return st
	}
	if !st.Enabled {
		for _, c := range cams.Cameras {
			st.Cameras.Items = append(st.Cameras.Items, CameraStatus{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt})
		}
		return st
	}
	live := m.mediaStatus(ctx)
	_, apNet, _ := net.ParseCIDR(netcfg.APNet)
	for _, c := range cams.Cameras {
		cs := CameraStatus{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt}
		if l, ok := live[c.ID]; ok {
			cs = l
			cs.ID, cs.Name, cs.CreatedAt = c.ID, c.Name, c.CreatedAt
			// Over the wifi only if it is up and the sender is on its network (with the wifi off, eth0 could be
			// on 10.42.0.0/24); otherwise, over the cable. From the board itself (tests), neither
			if ip := net.ParseIP(ptrStr(cs.Remote)); ip != nil && !ip.IsLoopback() {
				if st.AccessPoint.State == "on" && apNet.Contains(ip) {
					cs.Via = ptr("wifi")
				} else {
					cs.Via = ptr("lan")
				}
			}
		}
		st.Cameras.Items = append(st.Cameras.Items, cs)
	}
	return st
}

func (m *Manager) max() int {
	switch {
	case m.Max <= 0:
		return netcfg.DefaultMax
	case m.Max > netcfg.MaxCameras:
		return netcfg.MaxCameras
	}
	return m.Max
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) apStatus() APStatus {
	ap, _ := m.states()
	return ap
}

func (m *Manager) lanStatus() LANStatus {
	_, lan := m.states()
	return lan
}

// states: what eirl-netsetup left in its last apply (plus «starting» while applying), with the
// switches of network.json.
func (m *Manager) states() (APStatus, LANStatus) {
	n := m.network()
	policy := n.APPolicy(m.HasCameras())
	st := APStatus{Enabled: n.AP.On(), Policy: policy, State: "off", Band: "2.4", IP: netcfg.APAddr}
	if name := n.AP.SSID; name != "" {
		st.Name = &name
	}
	lan := LANStatus{Enabled: n.LANOn(), State: "off", Iface: netcfg.LANIface}
	if ip := m.lanIP(); ip != "" {
		lan.IP = &ip
	}
	var f struct {
		State, Mode, SSID, Error string
		Channel                  int
		LAN                      string `json:"lan"`
		LANError                 string `json:"lanError"`
	}
	if b, err := os.ReadFile(m.StatePath); err == nil && json.Unmarshal(b, &f) == nil {
		switch f.State {
		case "on", "off", "error":
			st.State = f.State
		}
		switch f.LAN {
		case "on", "off", "error":
			lan.State = f.LAN
		}
		if lan.State == "error" && len(f.LANError) <= 200 {
			lan.Error = f.LANError
		}
		if f.Mode == "normal" || f.Mode == "cameras" { // cameras: the one from an older eirl-netsetup
			st.Mode = &f.Mode
		}
		if netcfg.ValidSSID(f.SSID) {
			st.SSID = &f.SSID
		}
		if f.Channel >= 1 && f.Channel <= 11 {
			st.Channel = &f.Channel
		}
		if st.State == "error" && len(f.Error) <= 200 {
			st.Error = f.Error
		}
	}
	// While applying, «starting» only for what is going to come up and is not up: switching the reception off does not touch the wifi
	if m.applying.Load() {
		if policy == netcfg.APAlways && st.State != "on" {
			st.State = "starting"
		}
		if lan.Enabled && n.On() && lan.State != "on" {
			lan.State = "starting"
		}
	}
	return st, lan
}

// network: network.json, or the defaults (wifi on, cable off) if it is missing.
func (m *Manager) network() netcfg.Network {
	n, _, _ := netcfg.ParseNetwork(m.read(netcfg.FileNetwork))
	return n
}

// lanIP: eth0's first IPv4 ("" if it has none or does not exist).
func (m *Manager) lanIP() string {
	if m.LANAddr != nil {
		return m.LANAddr()
	}
	ifc, err := net.InterfaceByName(netcfg.LANIface)
	if err != nil {
		return ""
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			return ipn.IP.To4().String()
		}
	}
	return ""
}

// connect: how the camera connects; over the cable only with the cable on and eth0 with an IP.
func (m *Manager) connect(c netcfg.Camera) netcfg.Connect {
	ip := ""
	if m.network().LANOn() {
		ip = m.lanIP()
	}
	return c.Connect(ip)
}

func (m *Manager) clients(ctx context.Context) (int, bool) {
	if m.Clients != nil {
		return m.Clients(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "iw", "dev", netcfg.APIface, "station", "dump").Output()
	if err != nil {
		return 0, false
	}
	return strings.Count(string(out), "Station "), true
}

// mediaStatus: each camera that is publishing, according to MediaMTX's local API (no credentials: it
// can only be used from 127.0.0.1). The connections' query fields carry the key: they are not read.
func (m *Manager) mediaStatus(ctx context.Context) map[string]CameraStatus {
	out := map[string]CameraStatus{}
	if m.MediaAPI == "" {
		return out
	}
	var paths struct {
		Items []struct {
			Name   string `json:"name"`
			Ready  bool   `json:"ready"`
			Source *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"source"`
			Tracks        []string `json:"tracks"`
			BytesReceived uint64   `json:"bytesReceived"`
		} `json:"items"`
	}
	if m.getJSON(ctx, "/v3/paths/list", &paths) != nil {
		return out
	}
	now := m.now()
	m.cmu.Lock()
	defer m.cmu.Unlock()
	if m.prev == nil {
		m.prev = map[string]sample{}
	}
	for _, p := range paths.Items {
		if !netcfg.ValidCameraID(p.Name) || !p.Ready || p.Source == nil {
			continue
		}
		cs := CameraStatus{Connected: true}
		switch p.Source.Type {
		case "srtConn":
			cs.Protocol = ptr("srt")
		case "rtmpConn":
			cs.Protocol = ptr("rtmp")
		}
		for _, t := range p.Tracks {
			switch t {
			case "H264":
				cs.Codec = ptr("h264")
			case "H265":
				cs.Codec = ptr("h265")
			}
		}
		if pv, ok := m.prev[p.Name]; ok && p.BytesReceived >= pv.bytes && now.Sub(pv.at) > 500*time.Millisecond {
			k := int(float64(p.BytesReceived-pv.bytes) * 8 / 1000 / now.Sub(pv.at).Seconds())
			cs.Kbps = &k
		}
		m.prev[p.Name] = sample{p.BytesReceived, now}
		if p.Source.ID != "" && cs.Protocol != nil {
			m.connStatus(ctx, *cs.Protocol, p.Source.ID, &cs)
		}
		out[p.Name] = cs
	}
	return out
}

func (m *Manager) connStatus(ctx context.Context, proto, id string, cs *CameraStatus) {
	if strings.ContainsAny(id, "/?#%") || len(id) > 64 {
		return
	}
	var c struct {
		RemoteAddr          string   `json:"remoteAddr"`
		MsRTT               *float64 `json:"msRTT"`
		PacketsReceived     uint64   `json:"packetsReceived"`
		PacketsReceivedLoss uint64   `json:"packetsReceivedLoss"`
	}
	if m.getJSON(ctx, "/v3/"+proto+"conns/get/"+id, &c) != nil {
		return
	}
	if host, _, err := net.SplitHostPort(c.RemoteAddr); err == nil && net.ParseIP(host) != nil {
		cs.Remote = &host
	}
	if proto == "srt" {
		if c.MsRTT != nil {
			r := round1(*c.MsRTT)
			cs.RTTMs = &r
		}
		if c.PacketsReceived > 0 {
			l := round1(100 * float64(c.PacketsReceivedLoss) / float64(c.PacketsReceived+c.PacketsReceivedLoss))
			cs.LossPct = &l
		}
	}
}

// kickDelay: how long to wait before kicking the sender after a new key (see kickPublisher)
var kickDelay = 1500 * time.Millisecond

// kickPublisher: kicks whoever is publishing on the camera's path after giving it a new key. MediaMTX
// loads the new key on the fly, but does not cut whoever already got in with the old one (board,
// 13-oct). The reload arrives ~10 ms after eirl-netsetup writes (and it spaces them out so MediaMTX
// does not skip any); wait a bit longer just in case: on reconnecting, the old key no longer gets in.
func (m *Manager) kickPublisher(ctx context.Context, id string) {
	if m.MediaAPI == "" {
		return
	}
	select {
	case <-time.After(kickDelay):
	case <-ctx.Done():
		return
	}
	for _, proto := range []string{"rtmp", "srt"} {
		var l struct {
			Items []struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			} `json:"items"`
		}
		if m.getJSON(ctx, "/v3/"+proto+"conns/list", &l) != nil {
			continue
		}
		for _, c := range l.Items {
			if c.Path != id || c.ID == "" || strings.ContainsAny(c.ID, "/?#%") || len(c.ID) > 64 {
				continue
			}
			_ = m.do(ctx, http.MethodPost, "/v3/"+proto+"conns/kick/"+c.ID, nil)
		}
	}
}

func (m *Manager) getJSON(ctx context.Context, path string, v any) error {
	return m.do(ctx, http.MethodGet, path, v)
}

// do: one request to MediaMTX's API (localhost only); v = nil, no body to read.
func (m *Manager) do(ctx context.Context, method, path string, v any) error {
	m.hcOnce.Do(func() { m.hc = &http.Client{Timeout: 2 * time.Second} })
	req, err := http.NewRequestWithContext(ctx, method, m.MediaAPI+path, nil)
	if err != nil {
		return err
	}
	if k, ok, _ := netcfg.ParseMediaKey(m.read(netcfg.FileMediaKey)); ok {
		req.SetBasicAuth(netcfg.APIUser, k)
	}
	resp, err := m.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("mediamtx %d", resp.StatusCode)
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(v)
}

// --- cameras --------------------------------------------------------------------------------------

// Created: what adding a camera returns (with its credential: this is the moment to copy it).
type Created struct {
	Camera  CameraStatus   `json:"camera"`
	Connect netcfg.Connect `json:"connect"`
	AP      APStatus       `json:"accessPoint"`
}

// CreateCamera: the next free camera with a new key. With the first one, the network (default SSID)
// and its password are generated, and the access point comes up.
func (m *Manager) CreateCamera(ctx context.Context, name string) (Created, error) {
	if !netcfg.ValidName(name) {
		return Created{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.network().On() {
		return Created{}, ErrDisabled
	}
	cams, err := m.readCameras()
	if err != nil {
		return Created{}, err
	}
	id, err := cams.NextID(m.max())
	if err != nil {
		return Created{}, err
	}
	key, err := netcfg.Secret(netcfg.KeyLen)
	if err != nil {
		return Created{}, err
	}
	if err := m.ensureNetwork(); err != nil {
		return Created{}, err
	}
	c := netcfg.Camera{ID: id, Name: name, Key: key, CreatedAt: m.now().UTC().Truncate(time.Second)}
	cams.Cameras = append(cams.Cameras, c)
	if err := m.saveCameras(cams); err != nil {
		return Created{}, err
	}
	aerr := m.apply(ctx)
	return Created{Camera: CameraStatus{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt}, Connect: m.connect(c), AP: m.apStatus()}, aerr
}

// UpdateCamera: name and/or new key (a camera streaming with the old one gets disconnected).
func (m *Manager) UpdateCamera(ctx context.Context, id string, name *string, rotate bool) (Created, error) {
	if !netcfg.ValidCameraID(id) || (name != nil && !netcfg.ValidName(*name)) || (name == nil && !rotate) {
		return Created{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cams, err := m.readCameras()
	if err != nil {
		return Created{}, err
	}
	i := -1
	for j, c := range cams.Cameras {
		if c.ID == id {
			i = j
		}
	}
	if i < 0 {
		return Created{}, ErrUnknown
	}
	if name != nil {
		cams.Cameras[i].Name = *name
	}
	if rotate {
		k, err := netcfg.Secret(netcfg.KeyLen)
		if err != nil {
			return Created{}, err
		}
		cams.Cameras[i].Key = k
	}
	if err := m.saveCameras(cams); err != nil {
		return Created{}, err
	}
	var aerr error
	if rotate { // the name changes nothing in MediaMTX or the AP
		aerr = m.apply(ctx)
		// Also if the apply fails after writing the configuration (e.g. DHCP): a new key usually means
		// the old one leaked, and whoever streams with it must not stay on air
		m.kickPublisher(ctx, id)
	}
	c := cams.Cameras[i]
	return Created{Camera: CameraStatus{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt}, Connect: m.connect(c), AP: m.apStatus()}, aerr
}

// DeleteCamera: with the last one, the access point goes down.
func (m *Manager) DeleteCamera(ctx context.Context, id string) error {
	if !netcfg.ValidCameraID(id) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cams, err := m.readCameras()
	if err != nil {
		return err
	}
	next := cams.Cameras[:0:0]
	for _, c := range cams.Cameras {
		if c.ID != id {
			next = append(next, c)
		}
	}
	if len(next) == len(cams.Cameras) {
		return ErrUnknown
	}
	cams.Cameras = next
	if err := m.saveCameras(cams); err != nil {
		return err
	}
	return m.apply(ctx)
}

// PutAP: SSID, channel, switching the wifi on or off and/or a new password. Applied if there is something up or to
// bring up (Wanted): the AP comes back with the change and every device on it reconnects (with a new name, each one
// has to join it again). Switching it off with the cable off and the reception on: ErrNoInput.
//
// check (or nil): when the name changes, it is called with the lock held and before changing anything, as in PutWiFi;
// if it returns an error, nothing is touched and PutAP returns it as is. The app checks there that no camera coming in
// over the wifi is on air: the rename restarts the AP and that camera would drop off the program.
func (m *Manager) PutAP(ctx context.Context, ssid, channel *string, enabled *bool, rotate bool, check func() error) (APStatus, error) {
	if (ssid != nil && !netcfg.ValidSSID(*ssid)) || (channel != nil && !netcfg.ValidChannel(*channel)) || (ssid == nil && channel == nil && enabled == nil && !rotate) {
		return APStatus{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ssid != nil && *ssid != m.network().AP.SSID && check != nil {
		if err := check(); err != nil {
			return APStatus{}, err
		}
	}
	if err := m.ensureNetwork(); err != nil {
		return APStatus{}, err
	}
	n, _, err := netcfg.ParseNetwork(m.read(netcfg.FileNetwork))
	if err != nil {
		return APStatus{}, err
	}
	if enabled != nil && !*enabled && !n.LANOn() && n.On() {
		return APStatus{}, ErrNoInput
	}
	if ssid != nil {
		n.AP.SSID = *ssid
	}
	if channel != nil {
		n.AP.Channel = *channel
	}
	if enabled != nil {
		// Without «Create my wifi» chosen (older files), the policy comes from «over wifi» (W4): the current one is
		// pinned before changing it, because «over wifi» now belongs only to the reception and cannot switch the AP (W3)
		if n.WiFi == nil || n.WiFi.AP == "" {
			w := netcfg.WiFi{}
			if n.WiFi != nil {
				w = *n.WiFi
			}
			w.AP = n.APPolicy(m.HasCameras())
			n.WiFi = &w
		}
		n.AP.Enabled = enabled
	}
	if err := m.Write(filepath.Join(m.Dir, netcfg.FileNetwork), n.Render(), 0o640); err != nil {
		return APStatus{}, err
	}
	if rotate {
		psk, err := netcfg.Secret(netcfg.PSKLen)
		if err != nil {
			return APStatus{}, err
		}
		if err := m.Write(filepath.Join(m.Dir, netcfg.FilePSK), []byte(psk+"\n"), 0o600); err != nil {
			return APStatus{}, err
		}
	}
	var aerr error
	if m.applyNeeded() {
		aerr = m.apply(ctx)
	} else {
		m.writeScreen()
	}
	return m.apStatus(), aerr
}

// PutLAN: switch the wired cameras on or off (§6.11). Applied if there are cameras: switching it off cuts
// the one streaming over the cable. Switching it off with the wifi off and the reception on: ErrNoInput.
func (m *Manager) PutLAN(ctx context.Context, enabled bool) (LANStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureNetwork(); err != nil {
		return LANStatus{}, err
	}
	n, _, err := netcfg.ParseNetwork(m.read(netcfg.FileNetwork))
	if err != nil {
		return LANStatus{}, err
	}
	if !enabled && !n.AP.On() && n.On() {
		return LANStatus{}, ErrNoInput
	}
	n.LAN = &netcfg.LAN{Enabled: enabled}
	if err := m.Write(filepath.Join(m.Dir, netcfg.FileNetwork), n.Render(), 0o640); err != nil {
		return LANStatus{}, err
	}
	var aerr error
	if m.applyNeeded() {
		aerr = m.apply(ctx)
	}
	return m.lanStatus(), aerr
}

// PutEnabled: the RTMP/SRT reception (§6.12). Off: no receiver (MediaMTX) and no cameras in the mixer; the
// board's wifi (client or access point) stays as it is, and the cameras, their keys and the network stay
// saved. Switching on brings everything back as it was; with neither wifi nor cable to come in through,
// ErrNoInput. The network part is only applied if there are cameras.
// check (or nil): when switching off, it is called with the lock held and before changing anything; if it
// returns an error, nothing is touched and PutEnabled returns it as is. The app checks there that no network
// camera is on air: with another network change being applied, the lock can take minutes, and meanwhile
// someone could put it on air from the screen.
func (m *Manager) PutEnabled(ctx context.Context, enabled bool, check func() error) (Status, error) {
	m.mu.Lock()
	err := m.putEnabled(ctx, enabled, check)
	m.mu.Unlock()
	if err != nil && !errors.Is(err, ErrApply) {
		return Status{}, err
	}
	return m.Status(ctx), err
}

func (m *Manager) putEnabled(ctx context.Context, enabled bool, check func() error) error {
	if !enabled && check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	cams, err := m.readCameras()
	if err != nil {
		return err
	}
	if err := m.ensureNetwork(); err != nil {
		return err
	}
	path := filepath.Join(m.Dir, netcfg.FileNetwork)
	prev := m.read(netcfg.FileNetwork)
	n, _, err := netcfg.ParseNetwork(prev)
	if err != nil {
		return err
	}
	if enabled && !n.AP.On() && !n.LANOn() {
		return ErrNoInput
	}
	n.Enabled = &enabled
	if err := m.Write(path, n.Render(), 0o640); err != nil {
		return err
	}
	// The engine's list BEFORE the network: when switching off, the mixer releases the cameras and then the
	// receiver stops (when switching on, the camera waits a few seconds for MediaMTX, as when it is added). If it
	// cannot be written, the switch goes back to how it was: nothing half-applied
	if err := m.savePublic(cams, enabled); err != nil {
		_ = m.Write(path, prev, 0o640)
		return err
	}
	if !m.applyNeeded() {
		m.writeScreen()
		return nil
	}
	return m.apply(ctx)
}

// --- the board's wifi (WIFI.md) ----------------------------------------------------------------------------

var (
	// ErrNoClient: no client wifi on this device (no NetworkManager)
	ErrNoClient = errors.New("network: no client wifi")
	// ErrClientOff: connecting with «Connect to a wifi» switched off
	ErrClientOff = errors.New("network: client wifi switched off")
)

// WiFiStatus: GET /network/wifi (WIFI.md §5), without secrets. Client: null without the client wifi on this device.
// Warning: two_channels if the client and the AP are on different channels (the radio is shared: W2).
type WiFiStatus struct {
	Client  *ClientStatus `json:"client"`
	AP      WiFiAP        `json:"ap"`
	Warning *string       `json:"warning"`
}

// ClientStatus: «Connect to a wifi» and how it is doing: off | disconnected | connecting | connected | error.
type ClientStatus struct {
	Enabled bool `json:"enabled"`
	wifi.State
}

// WiFiAP: «Create my wifi» and how the access point is doing.
type WiFiAP struct {
	Policy  string  `json:"policy"` // always | rescue
	State   string  `json:"state"`  // off | starting | on | error
	SSID    *string `json:"ssid"`
	Channel *int    `json:"channel"`
	Clients int     `json:"clients"`
	Error   string  `json:"error,omitempty"`
}

// WiFi: the snapshot of the board's wifi.
func (m *Manager) WiFi(ctx context.Context) WiFiStatus {
	ap, _ := m.states()
	out := WiFiStatus{AP: WiFiAP{Policy: ap.Policy, State: ap.State, SSID: ap.SSID, Channel: ap.Channel, Error: ap.Error}}
	if ap.State == "on" {
		if n, ok := m.clients(ctx); ok {
			out.AP.Clients = n
		}
	}
	if m.Client == nil {
		return out
	}
	cs := &ClientStatus{Enabled: m.network().ClientOn(), State: wifi.State{State: "off"}}
	if cs.Enabled {
		st, err := m.Client.State(ctx)
		if err != nil {
			st = wifi.State{State: "error", Error: "unavailable"} // NetworkManager does not answer
		}
		cs.State = st
	}
	out.Client = cs
	// Both at once on different channels (the client on 5 GHz, or another 2.4 channel): the radio is shared
	if cs.State.State == "connected" && cs.Channel != nil && ap.State == "on" && ap.Channel != nil && *cs.Channel != *ap.Channel {
		w := "two_channels"
		out.Warning = &w
	}
	return out
}

// WiFiChange: what PUT /network/wifi changes (at least one).
type WiFiChange struct {
	AP     *string // «Create my wifi»: always | rescue
	Client *bool   // «Connect to a wifi»
}

// PutWiFi: «Create my wifi» (WIFI.md W3 and W7) and/or «Connect to a wifi» (W-B). AP always brings it up even with
// no cameras (the first time, the network and its password are generated, as when adding the first camera); rescue
// takes it down; the cameras do not force it. Client: off disconnects and does not reconnect on its own (the
// profiles are kept).
//
// check (or nil): when switching the AP off, it is called with the lock held and before changing anything, as in
// PutEnabled; if it returns an error, nothing is touched. The app checks there that no camera coming in over the
// wifi is on air.
func (m *Manager) PutWiFi(ctx context.Context, ch WiFiChange, check func() error) (WiFiStatus, error) {
	if (ch.AP == nil && ch.Client == nil) || (ch.AP != nil && !netcfg.ValidAPPolicy(*ch.AP)) {
		return WiFiStatus{}, ErrInvalid
	}
	if ch.Client != nil && m.Client == nil {
		return WiFiStatus{}, ErrNoClient
	}
	m.mu.Lock()
	var err error
	if ch.AP != nil && *ch.AP == netcfg.APRescue && check != nil {
		err = check()
	}
	if err == nil {
		err = m.putWiFi(ctx, ch)
	}
	m.mu.Unlock()
	if err != nil && !errors.Is(err, ErrApply) {
		return WiFiStatus{}, err
	}
	return m.WiFi(ctx), err
}

func (m *Manager) putWiFi(ctx context.Context, ch WiFiChange) error {
	if err := m.ensureNetwork(); err != nil {
		return err
	}
	n, _, err := netcfg.ParseNetwork(m.read(netcfg.FileNetwork))
	if err != nil {
		return err
	}
	w := netcfg.WiFi{}
	if n.WiFi != nil {
		w = *n.WiFi
	}
	if ch.AP != nil {
		w.AP = *ch.AP
	}
	if ch.Client != nil {
		w.Client = ch.Client
	}
	n.WiFi = &w
	if err := m.Write(filepath.Join(m.Dir, netcfg.FileNetwork), n.Render(), 0o640); err != nil {
		return err
	}
	if ch.Client != nil {
		if err := m.Client.SetEnabled(ctx, *ch.Client); err != nil {
			return fmt.Errorf("%w: %v", ErrApply, err)
		}
	}
	if ch.AP != nil {
		return m.apply(ctx)
	}
	return nil
}

// InitWiFi: at app start, «Connect to a wifi» as network.json says (NetworkManager does not remember it).
func (m *Manager) InitWiFi(ctx context.Context) error {
	if m.Client == nil {
		return nil
	}
	return m.Client.SetEnabled(ctx, m.network().ClientOn())
}

// internetEvery: how often the client wifi's internet access is checked
const internetEvery = 30 * time.Second

// MonitorWiFi: checks the client wifi's internet access every 30 s (also with nobody watching) and leaves its
// interface in NoInternetFile while it is «without internet» (empty otherwise): the engine takes it out of the
// SRTLA split and puts it back (WIFI.md §13). Until ctx ends.
func (m *Manager) MonitorWiFi(ctx context.Context, logf func(string, ...any)) {
	if m.Client == nil || m.NoInternetFile == "" {
		return
	}
	m.Client.Monitor(ctx, internetEvery, func(off bool) error {
		data := []byte{}
		if off {
			data = []byte(m.Client.Iface + "\n")
		}
		if err := m.Write(m.NoInternetFile, data, 0o640); err != nil {
			if logf != nil {
				logf("client wifi: could not write the no-internet list (will retry)", "err", err)
			}
			return err
		}
		if logf != nil {
			logf("client wifi", "noInternet", off)
		}
		return nil
	})
}

// ScanWiFi: the networks the client wifi sees (WIFI.md §4).
func (m *Manager) ScanWiFi(ctx context.Context) ([]wifi.Network, time.Time, error) {
	if m.Client == nil {
		return nil, time.Time{}, ErrNoClient
	}
	return m.Client.Scan(ctx)
}

// ConnectWiFi: connect the client wifi to a network (with «Connect to a wifi» on). The password only goes to
// NetworkManager.
func (m *Manager) ConnectWiFi(ctx context.Context, ssid string, password *string) error {
	if m.Client == nil {
		return ErrNoClient
	}
	m.mu.Lock() // one at a time with «Connect to a wifi» (otherwise it would connect right after switching it off)
	defer m.mu.Unlock()
	if !m.network().ClientOn() {
		return ErrClientOff
	}
	return m.Client.Connect(ctx, ssid, password)
}

// ForgetWiFi: forget a known network.
func (m *Manager) ForgetWiFi(ctx context.Context, ssid string) error {
	if m.Client == nil {
		return ErrNoClient
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Client.Forget(ctx, ssid)
}

// SSID: the AP's name (so it is not shown in the client wifi's network list), or "".
func (m *Manager) SSID() string {
	return m.network().AP.SSID
}

// Credentials: the wifi password and how each camera connects. Only for the web, when «Show» is
// pressed (GET /network/credentials, Cache-Control: no-store).
type Credentials struct {
	WiFi    *WiFiCredentials `json:"wifi"`
	Cameras []netcfg.Connect `json:"cameras"`
}

type WiFiCredentials struct {
	SSID     string `json:"ssid"`
	Channel  string `json:"channel"` // the saved one: auto | 1-11 (the one in use goes in Status)
	Password string `json:"password"`
	QR       string `json:"qr"`
	// QRRows: the QR as rows of '1' and '0' (as in wifi.json), only if asked for (?qr=rows): the screen draws it
	// without generating the QR
	QRRows []string `json:"qrRows,omitempty"`
}

func (m *Manager) Credentials() (Credentials, error) {
	cams, err := m.readCameras()
	if err != nil {
		return Credentials{}, err
	}
	out := Credentials{Cameras: []netcfg.Connect{}}
	for _, c := range cams.Cameras {
		out.Cameras = append(out.Cameras, m.connect(c))
	}
	n, okN, _ := netcfg.ParseNetwork(m.read(netcfg.FileNetwork))
	psk, okP, _ := netcfg.ParsePSK(m.read(netcfg.FilePSK))
	if okN && okP {
		out.WiFi = &WiFiCredentials{SSID: n.AP.SSID, Channel: n.AP.Channel, Password: psk, QR: netcfg.WifiQR(n.AP.SSID, psk)}
	}
	return out, nil
}

// CameraConnect: how a camera connects (with its key), for its QR.
func (m *Manager) CameraConnect(id string) (netcfg.Connect, error) {
	c, err := m.Camera(id)
	if err != nil {
		return netcfg.Connect{}, err
	}
	return m.connect(c), nil
}

// Camera: one camera (with its key).
func (m *Manager) Camera(id string) (netcfg.Camera, error) {
	cams, err := m.readCameras()
	if err != nil {
		return netcfg.Camera{}, err
	}
	c, ok := cams.Find(id)
	if !ok {
		return netcfg.Camera{}, ErrUnknown
	}
	return c, nil
}

// HasCameras: at least one has been added.
func (m *Manager) HasCameras() bool {
	cams, err := m.readCameras()
	return err == nil && len(cams.Cameras) > 0
}

// Wanted: there is something of the network to bring up (cameras, or the AP requested): it is re-applied at app start.
func (m *Manager) Wanted() bool {
	return m.HasCameras() || m.APPolicy() == netcfg.APAlways
}

// APPolicy: the effective access point policy (netcfg.Network.APPolicy, with the real cameras): the one in
// GET /network and the one reported to the cloud (WIFI.md §7). Read only.
func (m *Manager) APPolicy() string {
	return m.network().APPolicy(m.HasCameras())
}

// applyNeeded: whether a network change must be applied. With something requested (Wanted) or something up to
// take down; otherwise it is only saved.
func (m *Manager) applyNeeded() bool {
	if m.Wanted() {
		return true
	}
	ap, lan := m.states()
	return ap.State != "off" || lan.State != "off"
}

// Reapply: applies the network again (at app start, in case eirl-ap.service failed at power-on).
func (m *Manager) Reapply(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apply(ctx)
}

// --- helpers --------------------------------------------------------------------------------------

// ScreenWiFi: what the screen reads (NETWORK_SOURCES.md §6.4). QR as rows of '1' and '0'.
type ScreenWiFi struct {
	V        int      `json:"v"`
	SSID     string   `json:"ssid"`
	Password string   `json:"password"`
	QR       []string `json:"qr"`
	On       bool     `json:"on"` // the AP is on (otherwise the screen says so instead of inviting to connect)
}

// writeScreen: leaves the screen's file while the AP is requested («Create my wifi», with or without cameras;
// otherwise it removes it and the «Wifi» button does not show). The reception does not count: the board's wifi is
// separate. A failure does not stop anything.
func (m *Manager) writeScreen() {
	if m.ScreenFile == "" {
		return
	}
	cams, _ := m.readCameras()
	n, okN, _ := netcfg.ParseNetwork(m.read(netcfg.FileNetwork))
	psk, okP, _ := netcfg.ParsePSK(m.read(netcfg.FilePSK))
	if !okN || !okP || n.APPolicy(len(cams.Cameras) > 0) != netcfg.APAlways {
		_ = os.Remove(m.ScreenFile)
		return
	}
	rows, err := QRRows(netcfg.WifiQR(n.AP.SSID, psk))
	if err != nil {
		return
	}
	b, _ := json.Marshal(ScreenWiFi{V: 1, SSID: n.AP.SSID, Password: psk, QR: rows, On: m.apStatus().State == "on"})
	_ = m.Write(m.ScreenFile, append(b, '\n'), 0o640)
}

func (m *Manager) apply(ctx context.Context) error {
	defer m.writeScreen()
	if m.Apply == nil {
		return nil
	}
	if err := m.ensureMediaKey(); err != nil {
		return fmt.Errorf("%w: %v", ErrApply, err)
	}
	m.applying.Store(true)
	defer m.applying.Store(false)
	if err := m.Apply(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrApply, err)
	}
	return nil
}

// EnsureNetwork: at the app's start, the access point's name (netcfg.DefaultSSID: "EnhancedIRL-" + the end of the
// hardwareId) and its password, if they do not exist yet. That way the name is there from the first boot (the setup
// wizard shows it and lets it be changed) instead of appearing with the first camera or «Create my Wi-Fi».
// Idempotent and create-only: a file that is already there is never rewritten, not even an invalid one (another
// version's; it is left for the next network change, which keeps its name: ensureNetwork). Nothing is applied: with
// no cameras and «Create my Wi-Fi» off, nothing comes up. created: something was written.
func (m *Manager) EnsureNetwork() (created bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	missing := func(name string) bool {
		_, err := os.Lstat(filepath.Join(m.Dir, name))
		return errors.Is(err, fs.ErrNotExist)
	}
	if missing(netcfg.FileNetwork) {
		n := netcfg.Network{AP: netcfg.AP{SSID: netcfg.DefaultSSID(m.HardwareID), Channel: "auto"}}
		if err := m.Write(filepath.Join(m.Dir, netcfg.FileNetwork), n.Render(), 0o640); err != nil {
			return false, err
		}
		created = true
	}
	if missing(netcfg.FilePSK) {
		psk, err := netcfg.Secret(netcfg.PSKLen)
		if err != nil {
			return created, err
		}
		if err := m.Write(filepath.Join(m.Dir, netcfg.FilePSK), []byte(psk+"\n"), 0o600); err != nil {
			return created, err
		}
		created = true
	}
	return created, nil
}

// ensureNetwork: network.json (default SSID, automatic channel) and ap.psk, if missing or not valid. A file that is not
// valid (another version's) keeps its name if it has a valid one: the SSID is never replaced by the default.
func (m *Manager) ensureNetwork() error {
	b := m.read(netcfg.FileNetwork)
	if _, ok, err := netcfg.ParseNetwork(b); err != nil || !ok {
		ssid := netcfg.DefaultSSID(m.HardwareID)
		if s := netcfg.LooseSSID(b); s != "" {
			ssid = s
		}
		n := netcfg.Network{AP: netcfg.AP{SSID: ssid, Channel: "auto"}}
		if err := m.Write(filepath.Join(m.Dir, netcfg.FileNetwork), n.Render(), 0o640); err != nil {
			return err
		}
	}
	if _, ok, err := netcfg.ParsePSK(m.read(netcfg.FilePSK)); err != nil || !ok {
		psk, err := netcfg.Secret(netcfg.PSKLen)
		if err != nil {
			return err
		}
		if err := m.Write(filepath.Join(m.Dir, netcfg.FilePSK), []byte(psk+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// saveCameras: the keys in cameras.json (0600: not even the engine, which is in the group, reads them)
// and the keyless list for the sources catalogue (0640).
func (m *Manager) saveCameras(cams netcfg.Cameras) error {
	if err := m.Write(filepath.Join(m.Dir, netcfg.FileCameras), cams.Render(), 0o600); err != nil {
		return err
	}
	return m.savePublic(cams, m.network().On())
}

// savePublic: the catalogue list, EMPTY with the main switch off (§6.12). The engine re-reads the catalogue
// when this file changes and releases the slot of each camera that is gone (it stops querying MediaMTX for
// it and stops showing it); when switched on, they come back.
func (m *Manager) savePublic(cams netcfg.Cameras, on bool) error {
	pub := cams.Public()
	if !on {
		pub = netcfg.Public{}
	}
	return m.Write(filepath.Join(m.Dir, netcfg.FilePublic), pub.Render(), 0o640)
}

func (m *Manager) readCameras() (netcfg.Cameras, error) {
	return netcfg.ParseCameras(m.read(netcfg.FileCameras))
}

// ensureMediaKey: the MediaMTX API key (0640: the app and the engine, through the group). Generated the
// first time; eirl-netsetup puts its hash in the configuration.
func (m *Manager) ensureMediaKey() error {
	if _, ok, err := netcfg.ParseMediaKey(m.read(netcfg.FileMediaKey)); err == nil && ok {
		return nil
	}
	k, err := netcfg.Secret(netcfg.MediaKeyLen)
	if err != nil {
		return err
	}
	return m.Write(filepath.Join(m.Dir, netcfg.FileMediaKey), []byte(k+"\n"), 0o640)
}

func (m *Manager) read(name string) []byte {
	b, err := safefile.ReadOwned(filepath.Join(m.Dir, name), netcfg.MaxFileSize)
	if err != nil {
		return nil
	}
	return b
}

// SystemdApply: what the app uses on the board. It starts eirl-ap.service (oneshot: eirl-netsetup apply)
// with the exact sudoers rule, instead of running the helper as a child: that way it runs as root in
// systemd's environment and does not inherit the app's sandbox (ProtectSystem=strict left /run and
// /etc/NetworkManager read-only) nor its CPU and memory limits. The app's calls go one at a time
// (Manager.mu): none merges with another half-way. If it fails, the reason comes from its state.
func SystemdApply(ctx context.Context, statePath string) error {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	t0 := time.Now()
	var err error
	for try := 0; try < 2; try++ {
		err = exec.CommandContext(ctx, "sudo", "-n", "/bin/systemctl", "start", "eirl-ap.service").Run()
		// A start while another is running (the boot one, install.sh's) merges with it and returns ITS
		// result, which read the files before this change: if the state is older, try again
		var st struct {
			Error, At string
			LANError  string `json:"lanError"`
		}
		b, rerr := os.ReadFile(statePath)
		if rerr == nil && json.Unmarshal(b, &st) == nil {
			if at, perr := time.Parse(time.RFC3339Nano, st.At); perr == nil && at.Before(t0) {
				continue
			}
			// The reason: the wifi's, the cable's or both
			if msg := strings.Trim(st.Error+" · "+st.LANError, " ·"); err != nil && msg != "" {
				return errors.New(msg)
			}
		}
		return err
	}
	return err
}

// Retry: with cameras and the wifi or the cable in error (e.g. the wifi came up late at boot, or the xtables
// lock), it tries again every `every` until it comes up. And the other way round: if the wifi or the cable stay
// up without being requested (no cameras, switched off, or the cable with the reception off; an apply that never
// got to run: sudo or systemd failing do not touch its state), it takes them down again. Otherwise, nothing.
func (m *Manager) Retry(ctx context.Context, every time.Duration, logf func(error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ap, lan := m.states()
		n, cams := m.network(), m.HasCameras()
		wantAP, wantLAN := ap.Policy == netcfg.APAlways, cams && n.On() && n.LANOn()
		// What was requested (or, with cameras, anything) in error, or up without being requested
		retry := func(want bool, state string) bool {
			return (want || cams) && state == "error" || !want && state == "on"
		}
		if !retry(wantAP, ap.State) && !retry(wantLAN, lan.State) {
			continue
		}
		if err := m.Reapply(ctx); err != nil && logf != nil {
			logf(err)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func round1(f float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 1, 64), 64)
	return v
}
