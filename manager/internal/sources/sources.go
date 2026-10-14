// Package sources discovers the device's video sources (the board's HDMI over CSI and USB capture
// cards and cameras) and their audio inputs, and picks the capture format and mode for a source.
// That choice (the "auto" rule) is used by the API and by the engine, through
// `eirl-encoderd probe`: so it lives in one place. Queries only: nothing here captures or changes a
// device's configuration.
//
// Everything that comes from a USB device (name, vendor) is third-party data: it is sanitized before
// it leaves this package.
package sources

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

// Default names of the catalogue. They are data (the engine shows them, probe -list carries them), kept as they
// were; the app's API translates them for the request language (api.sourceName).
const (
	HDMIName      = "HDMI de la placa"
	HDMIAudioName = "Audio del HDMI de la placa"
	USBNamePrefix = "Cámara USB " // + vendor:product, when the device reports no name
)

type Kind string

const (
	KindHDMI Kind = "hdmi-csi" // the TC358743 over CSI (SOURCE=hdmi)
	KindUSB  Kind = "usb"      // UVC capture card or camera (SOURCE=usb)
	KindTest Kind = "test"     // test pattern (SOURCE=test)
	// KindNetwork: network camera through the access point (F7, NETWORK_SOURCES.md §6.4). Mixer only:
	// in v1 it cannot be the main source (SOURCE) nor can its audio be AUDIO_SOURCE
	KindNetwork Kind = "network"
)

// Rate: frames per second as a fraction (60000/1001 = 59.94). The inverse of the V4L2 interval.
type Rate struct {
	Num int `json:"num"`
	Den int `json:"den"`
}

func (r Rate) FPS() float64 {
	if r.Den == 0 {
		return 0
	}
	return float64(r.Num) / float64(r.Den)
}

func (r Rate) String() string { return strconv.Itoa(r.Num) + "/" + strconv.Itoa(r.Den) }

type Mode struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Rates  []Rate `json:"rates"`
}

// Format: a pixel format the device offers. Name is what GStreamer and the script call it (NV12,
// YUY2, I420, UYVY, MJPG); empty and Supported=false if the encoder cannot capture it (native H.264
// and the rest).
type Format struct {
	FourCC    string `json:"fourcc"`
	Name      string `json:"name,omitempty"`
	Supported bool   `json:"supported"`
	Modes     []Mode `json:"modes"`
}

// fourccName: V4L2 formats the encoder can capture → name in GStreamer and in the script.
// Uncompressed ones go to nvvidconv (it accepts them from system memory); MJPEG, to the hardware JPEG
// decoder. Camera H.264 is out of v1 (it would have to be decoded anyway: adaptive bitrate needs our
// own encoder).
var fourccName = map[string]string{"NV12": "NV12", "YUYV": "YUY2", "YU12": "I420", "UYVY": "UYVY", "MJPG": "MJPG"}

func newFormat(fcc string, modes []Mode) Format {
	n := fourccName[fcc]
	return Format{FourCC: fcc, Name: n, Supported: n != "", Modes: modes}
}

type USBInfo struct {
	VendorID  string `json:"vendorId"`
	ProductID string `json:"productId"`
	SpeedMbps int    `json:"speedMbps"` // 480 = USB 2, 5000 = USB 3
	Bus       int    `json:"bus"`       // busnum: devices on the same bus share bandwidth (ChooseAll)
	Serial    bool   `json:"serial"`    // has its own serial number (otherwise it is identified by its port)
}

// AudioInput: an audio input the stream can use. ID is the AUDIO_SOURCE value.
type AudioInput struct {
	ID   string `json:"id"`   // i2s | alsa:<card id> | net:camN
	Kind string `json:"kind"` // i2s | usb | network
	Name string `json:"name"`
	Card string `json:"card,omitempty"` // ALSA id (hw:CARD=<card>)
}

// Source: a video source. ID is what is stored in VIDEO_DEVICE: a /dev/v4l path that does not change
// when the device is replugged (by-id if the device has a serial number, by-path —the port—
// otherwise: cheap capture cards repeat serial numbers). For the board's HDMI, "hdmi".
type Source struct {
	ID       string      `json:"id"`
	Kind     Kind        `json:"kind"`
	Name     string      `json:"name"`
	Node     string      `json:"node"`
	USB      *USBInfo    `json:"usb,omitempty"`
	Formats  []Format    `json:"formats,omitempty"`
	Audio    *AudioInput `json:"audio,omitempty"` // the one of the same USB device (the Cam Link carries the HDMI audio)
	Warnings []string    `json:"warnings,omitempty"`
	Error    string      `json:"error,omitempty"` // could not be queried (busy, no permission, re-enumerating)

	serialKey string // vendor:product:serial, to detect repeated serial numbers
}

// Catalog: what GET /api/v1/sources returns.
type Catalog struct {
	Sources []Source     `json:"sources"`
	Audio   []AudioInput `json:"audio"`
}

// Warnings (stable keys; the web supplies the text)
const (
	WarnUSB2      = "usb2"       // USB 3 capture card plugged into a USB 2 port or cable
	WarnNoFormat  = "no_format"  // offers no format the encoder can capture
	WarnOnlyAbove = "only_4k"    // only offers modes above 1080p that do not fit in the USB: the camera must be set to 1080p
	WarnUnread    = "unreadable" // could not be queried right now (see Error)
)

// aboveOnly: every capturable mode is above 1080p (the Cam Link delivers the camera's mode: with a
// camera at 4K it only offers 4K, which uncompressed does not fit in the Nano's USB).
func aboveOnly(formats []Format) bool {
	any := false
	for _, f := range formats {
		if !f.Supported {
			continue
		}
		for _, m := range f.Modes {
			if m.Width <= maxOutW && m.Height <= maxOutH {
				return false
			}
			any = true
		}
	}
	return any
}

// Enumerator queries a V4L2 node (ioctls; see v4l2_linux.go). ErrNotCapture if it does not capture video.
type Enumerator interface {
	Formats(node string) ([]Format, error)
}

var ErrNotCapture = errors.New("not a video capture node")

// Scanner walks sysfs and /dev. SysRoot and DevRoot are "/sys" and "/dev" on the board; a fake tree
// in tests.
type Scanner struct {
	SysRoot string
	DevRoot string
	Enum    Enumerator
	// Cameras: the network cameras list WITHOUT keys (/etc/eirl-encoder/cameras-public.json);
	// empty = no network cameras
	Cameras string

	mu    sync.Mutex
	cache map[string][]Format // by node + USB device identity (bus/dev changes on re-enumeration)
}

func New() *Scanner {
	return &Scanner{SysRoot: "/sys", DevRoot: "/dev", Enum: v4l2Enum{}, Cameras: "/etc/eirl-encoder/" + netcfg.FilePublic}
}

// Scan returns the sources and audio inputs connected right now.
func (s *Scanner) Scan() Catalog {
	cat := Catalog{Sources: []Source{}, Audio: []AudioInput{}}
	cards := s.soundCards()
	nodes, _ := filepath.Glob(filepath.Join(s.SysRoot, "class/video4linux/video*"))
	sort.Strings(nodes)
	seen := map[string]bool{}
	hdmi := false
	for _, n := range nodes {
		node := filepath.Base(n)
		name := readTrim(filepath.Join(n, "name"))
		if idx := readTrim(filepath.Join(n, "index")); idx != "" && idx != "0" {
			continue // on newer kernels, metadata nodes of the same device
		}
		dev, err := filepath.EvalSymlinks(filepath.Join(n, "device"))
		if err != nil {
			continue
		}
		usbDir := usbDeviceDir(dev)
		if usbDir == "" {
			if strings.Contains(name, "tc358743") && !hdmi {
				hdmi = true
				cat.Sources = append(cat.Sources, Source{ID: "hdmi", Kind: KindHDMI, Name: HDMIName, Node: "/dev/" + node})
			}
			continue
		}
		if seen[usbDir] {
			continue // a device with two streaming interfaces: the first one
		}
		formats, err := s.formats(node, usbDir)
		if errors.Is(err, ErrNotCapture) {
			continue
		}
		seen[usbDir] = true
		src := s.usbSource(node, name, usbDir, formats, err)
		if c, ok := cards[usbDir]; ok {
			a := c
			src.Audio = &a
		}
		cat.Sources = append(cat.Sources, src)
	}
	if hdmi {
		cat.Audio = append(cat.Audio, AudioInput{ID: "i2s", Kind: "i2s", Name: HDMIAudioName})
	}
	usbCards := make([]AudioInput, 0, len(cards))
	for _, c := range cards {
		usbCards = append(usbCards, c)
	}
	sort.Slice(usbCards, func(i, j int) bool { return usbCards[i].ID < usbCards[j].ID })
	cat.Audio = append(cat.Audio, usbCards...)
	s.dedupSerials(cat.Sources)
	// Network cameras: after the USB ones (the mixer slots follow this order). They are listed even
	// when not sending: the engine shows «Waiting for the camera»
	if s.Cameras != "" {
		if b, err := os.ReadFile(s.Cameras); err == nil && len(b) <= netcfg.MaxFileSize {
			if pub, err := netcfg.ParsePublic(b); err == nil {
				for _, c := range pub.Cameras {
					a := AudioInput{ID: "net:" + c.ID, Kind: string(KindNetwork), Name: c.Name}
					cat.Sources = append(cat.Sources, Source{ID: "net:" + c.ID, Kind: KindNetwork, Name: c.Name,
						Node: netcfg.ReadURL(c.ID), Audio: &a})
					cat.Audio = append(cat.Audio, a)
				}
			}
		}
	}
	return cat
}

// dedupSerials: two devices with the same vendor, product and serial number (cheap capture cards)
// share the by-id link, which points to whichever udev linked last: stored like that, the stream
// could switch cameras on its own after a replug. Those are identified by their port.
func (s *Scanner) dedupSerials(srcs []Source) {
	n := map[string]int{}
	for _, src := range srcs {
		if src.serialKey != "" {
			n[src.serialKey]++
		}
	}
	for i := range srcs {
		if k := srcs[i].serialKey; k != "" && n[k] > 1 {
			if id := s.stableID(filepath.Base(srcs[i].Node), false); id != "" {
				srcs[i].ID = id
			}
		}
	}
}

func (s *Scanner) usbSource(node, v4lName, usbDir string, formats []Format, readErr error) Source {
	serial := readTrim(filepath.Join(usbDir, "serial"))
	info := &USBInfo{
		VendorID:  readTrim(filepath.Join(usbDir, "idVendor")),
		ProductID: readTrim(filepath.Join(usbDir, "idProduct")),
		Serial:    serial != "",
	}
	info.SpeedMbps, _ = strconv.Atoi(readTrim(filepath.Join(usbDir, "speed")))
	info.Bus, _ = strconv.Atoi(readTrim(filepath.Join(usbDir, "busnum")))
	name := sanitize(readTrim(filepath.Join(usbDir, "product")))
	if name == "" {
		name = sanitize(v4lName)
	}
	if name == "" {
		name = USBNamePrefix + info.VendorID + ":" + info.ProductID
	}
	src := Source{ID: s.stableID(node, info.Serial), Kind: KindUSB, Name: name, Node: "/dev/" + node, USB: info, Formats: formats}
	if src.ID == "" {
		src.ID = "/dev/" + node // no udev links (should not happen): at least make it visible
	}
	if serial != "" {
		src.serialKey = info.VendorID + ":" + info.ProductID + ":" + serial
	}
	if readErr != nil {
		src.Error = sanitize(readErr.Error())
		src.Warnings = append(src.Warnings, WarnUnread)
	} else if _, err := Choose(src, "auto", "auto"); err != nil {
		if aboveOnly(formats) {
			src.Warnings = append(src.Warnings, WarnOnlyAbove)
		} else {
			src.Warnings = append(src.Warnings, WarnNoFormat)
		}
	}
	if info.SpeedMbps > 0 && info.SpeedMbps < 5000 && usb3Only(info.VendorID, info.ProductID) {
		src.Warnings = append(src.Warnings, WarnUSB2)
	}
	return src
}

// usb3Only: devices that only work well on USB 3 (Elgato Cam Link 4K and its revisions; the same PIDs
// BELABOX watches for).
func usb3Only(vid, pid string) bool {
	return vid == "0fd9" && (pid == "0066" || pid == "0067" || pid == "007b" || pid == "0085")
}

// stableID: the udev link in /dev/v4l that points to this node. by-id only if the device has a
// serial number: without one, two identical devices share the name and the link belongs to the last.
func (s *Scanner) stableID(node string, serial bool) string {
	order := []string{"by-path"}
	if serial {
		order = []string{"by-id", "by-path"}
	}
	for _, d := range order {
		links, _ := filepath.Glob(filepath.Join(s.DevRoot, "v4l", d, "*"))
		sort.Strings(links)
		for _, l := range links {
			if t, err := os.Readlink(l); err == nil && filepath.Base(t) == node {
				return "/dev/v4l/" + d + "/" + filepath.Base(l)
			}
		}
	}
	return ""
}

func (s *Scanner) formats(node, usbDir string) ([]Format, error) {
	key := node + "|" + readTrim(filepath.Join(usbDir, "busnum")) + "." + readTrim(filepath.Join(usbDir, "devnum"))
	s.mu.Lock()
	if f, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return f, nil
	}
	s.mu.Unlock()
	f, err := s.Enum.Formats(filepath.Join(s.DevRoot, node))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.cache == nil || len(s.cache) > 64 {
		s.cache = map[string][]Format{}
	}
	s.cache[key] = f
	s.mu.Unlock()
	return f, nil
}

// soundCards: ALSA cards of USB devices, by device directory (to pair them with their video). The
// board's own cards (HDA of the HDMI output, the I2S one) are left out: the I2S goes as "i2s".
func (s *Scanner) soundCards() map[string]AudioInput {
	out := map[string]AudioInput{}
	cards, _ := filepath.Glob(filepath.Join(s.SysRoot, "class/sound/card*"))
	for _, c := range cards {
		dev, err := filepath.EvalSymlinks(filepath.Join(c, "device"))
		if err != nil {
			continue
		}
		usbDir := usbDeviceDir(dev)
		id := readTrim(filepath.Join(c, "id"))
		if usbDir == "" || !reCardID.MatchString(id) {
			continue
		}
		name := sanitize(readTrim(filepath.Join(usbDir, "product")))
		if name == "" {
			name = id
		}
		if _, dup := out[usbDir]; !dup {
			out[usbDir] = AudioInput{ID: "alsa:" + id, Kind: "usb", Name: name, Card: id}
		}
	}
	return out
}

// usbDeviceDir: the sysfs directory of the USB device an interface hangs from (the one with
// idVendor), or "" if it is not USB.
func usbDeviceDir(dev string) string {
	d := dev
	for i := 0; i < 3 && d != "/" && d != "."; i++ {
		if _, err := os.Stat(filepath.Join(d, "idVendor")); err == nil {
			return d
		}
		d = filepath.Dir(d)
	}
	return ""
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

var (
	reCardID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	reNameBad  = regexp.MustCompile(`[^A-Za-z0-9 ._()+-]+`)
	reSpaces   = regexp.MustCompile(`\s+`)
	reModeSpec = regexp.MustCompile(`^([0-9]{2,5})x([0-9]{2,5})@([0-9]{1,3})$`)
)

// sanitize: a device name for display (set by its vendor: third-party data).
func sanitize(s string) string {
	s = strings.TrimSpace(reSpaces.ReplaceAllString(reNameBad.ReplaceAllString(s, " "), " "))
	if len(s) > 48 {
		s = strings.TrimSpace(s[:48])
	}
	return s
}

// --- Format and mode choice ------------------------------------------------------------------

// Choice: what a source is captured with.
type Choice struct {
	Format string // NV12 | YUY2 | I420 | UYVY | MJPG
	Width  int
	Height int
	Rate   Rate
}

func (c Choice) Mode() string {
	return fmt.Sprintf("%dx%d@%d", c.Width, c.Height, int(math.Round(c.Rate.FPS())))
}

var (
	ErrNoMode      = errors.New("el dispositivo no ofrece ningún modo que se pueda capturar")
	ErrNotOffered  = errors.New("el dispositivo no ofrece ahora ese formato o modo")
	ErrBadModeSpec = errors.New("modo no válido: WxH@fps")
)

// Preference between formats with the same mode: uncompressed before MJPEG (no extra loss nor
// decoding latency); NV12 first because it is what NVENC expects and it moves 25 % less data than
// YUY2. To be revisited with the phase 0 measurements (SOURCES.md).
var formatRank = map[string]int{"NV12": 5, "YUY2": 4, "I420": 3, "UYVY": 2, "MJPG": 1}

// bytesPerPixel of the uncompressed formats (for the USB bus budget).
var bytesPerPixel = map[string]float64{"NV12": 1.5, "I420": 1.5, "YUY2": 2, "UYVY": 2}

const (
	maxFPS     = 60.5
	minGoodFPS = 23.5 // below this (YUY2 at 1080p over USB 2 gives ~5 fps) only if nothing else
	maxOutW    = 1920
	maxOutH    = 1080
)

// busBudget: bytes/s considered usable on the bus for uncompressed video. The Nano's USB 3: ~367 MB/s
// real, shared by the four ports (a single link behind the hub); USB 2: the high-bandwidth isochronous
// maximum (3 × 1024 B per microframe ≈ 24.5 MB/s).
func busBudget(speedMbps int) float64 {
	switch {
	case speedMbps >= 5000:
		return 300e6
	case speedMbps >= 480:
		return 24e6
	default:
		return 1e6
	}
}

// Choose picks format and mode. format: auto or one of the formatRank names; mode: auto or WxH@fps
// (fps rounded: 60 covers 59.94).
func Choose(src Source, format, mode string) (Choice, error) {
	wantW, wantH, wantF := 0, 0, 0
	if mode != "" && mode != "auto" {
		m := reModeSpec.FindStringSubmatch(mode)
		if m == nil {
			return Choice{}, ErrBadModeSpec
		}
		wantW, _ = strconv.Atoi(m[1])
		wantH, _ = strconv.Atoi(m[2])
		wantF, _ = strconv.Atoi(m[3])
	}
	all := candidates(src)
	for _, c := range all { // already sorted: the first match is the best
		if format != "" && format != "auto" && c.Format != format {
			continue
		}
		if wantW != 0 && (c.Width != wantW || c.Height != wantH || int(math.Round(c.Rate.FPS())) != wantF) {
			continue
		}
		return c.Choice, nil
	}
	if len(all) > 0 {
		return Choice{}, ErrNotOffered
	}
	return Choice{}, ErrNoMode
}

// Options: everything a source can be asked for (what Choose accepts), without repeating format and
// mode (60/1 and 60000/1001 are the same "@60") and in the order of the "auto" rule: the first one is
// what "auto" picks. It is what the web offers in the mode selector.
func Options(src Source) []Choice {
	seen := map[string]bool{}
	var out []Choice
	for _, c := range candidates(src) {
		k := c.Format + " " + c.Mode()
		if !seen[k] {
			seen[k] = true
			out = append(out, c.Choice)
		}
	}
	return out
}

type candidate struct {
	Choice
	key [6]float64
}

// candidates: every format × size × fps that can be captured (supported format, up to 60 fps and,
// uncompressed, within the bus budget), best to worst by the "auto" rule.
func candidates(src Source) []candidate {
	speed := 5000
	if src.USB != nil && src.USB.SpeedMbps > 0 {
		speed = src.USB.SpeedMbps
	}
	var out []candidate
	for _, f := range src.Formats {
		if !f.Supported {
			continue
		}
		for _, m := range f.Modes {
			for _, r := range m.Rates {
				fps := r.FPS()
				if fps <= 0 || fps > maxFPS {
					continue
				}
				if bpp, raw := bytesPerPixel[f.Name]; raw && float64(m.Width*m.Height)*bpp*fps > busBudget(speed) {
					continue
				}
				fits := m.Width <= maxOutW && m.Height <= maxOutH
				h := float64(m.Height)
				if !fits {
					h = -h // above 1080p, the smallest (it gets scaled)
				}
				out = append(out, candidate{
					Choice: Choice{Format: f.Name, Width: m.Width, Height: m.Height, Rate: r},
					key:    [6]float64{b2f(fits), b2f(fps >= minGoodFPS), h, math.Round(fps), float64(formatRank[f.Name]), fps},
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return less(out[j].key, out[i].key) })
	return out
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func less(a, b [6]float64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// OutputSize: the size that is streamed. Up to 1920×1080, the capture's; above, scaled keeping the
// aspect ratio (and even: NVENC and NV12 require it).
func OutputSize(w, h int) (int, int) {
	if w <= maxOutW && h <= maxOutH {
		return w, h
	}
	k := math.Min(float64(maxOutW)/float64(w), float64(maxOutH)/float64(h))
	return int(float64(w)*k) &^ 1, int(float64(h)*k) &^ 1
}

// RawBusShare: fraction of the USB 3 budget an uncompressed capture takes (0 if compressed). For the
// web's warning: two such sources do not fit (for several at once, ChooseAll).
func RawBusShare(c Choice) float64 { return rawBytes(c) / busBudget(5000) }

// Find looks up the USB source VIDEO_DEVICE asks for: "auto" (or empty) is the first USB source that
// offers the requested format and mode (auto: any that can be captured); otherwise, the one with that
// ID or whose link points to the same node.
func Find(cat Catalog, device, format, mode, devRoot string) (Source, bool) {
	if device == "" || device == "auto" {
		// The first one that can be captured; if none, the first USB one anyway: so the caller says
		// why it cannot be captured (e.g. the camera at 4K) instead of «none connected»
		var first *Source
		for i, s := range cat.Sources {
			if s.Kind != KindUSB {
				continue
			}
			if _, err := Choose(s, format, mode); err == nil {
				return s, true
			}
			if first == nil {
				first = &cat.Sources[i]
			}
		}
		if first != nil {
			return *first, true
		}
		return Source{}, false
	}
	node := ""
	if rel, ok := strings.CutPrefix(device, "/dev/"); ok {
		if t, err := os.Readlink(filepath.Join(devRoot, rel)); err == nil {
			node = filepath.Base(t)
		}
	}
	for _, s := range cat.Sources {
		if s.Kind == KindUSB && (s.ID == device || (node != "" && filepath.Base(s.Node) == node)) {
			return s, true
		}
	}
	return Source{}, false
}
