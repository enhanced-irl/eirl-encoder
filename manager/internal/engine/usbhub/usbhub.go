// Package usbhub recovers a hung USB capture card by cutting the power of its hub port (MIXER.md §16,
// scripts/engine_usb.py).
//
// The Cam Link and the Logi sometimes hang with `-71` (the device or its power, not the bus): they stop
// giving frames or do not enumerate again, and resetting the USB by software does not bring them back.
// Unplugging them, that is, removing VBUS, does. The board's hub (Realtek RTS5411, its USB 2 and USB 3
// halves) declares per-port power switching, so this does what `uhubctl` does: ClearPortFeature
// (PORT_POWER), a pause and SetPortFeature(PORT_POWER), on the port and on its twin in the other hub (a
// USB 3 connector is two logical ports sharing one VBUS).
//
// Measured on the board (13-oct, MIXER.md §16): although the descriptor says "per port", cutting port 1
// powers off the WHOLE hub: the touch screen (1-2.3) and the Logi (2-1.4) drop and enumerate again
// 3-5 s later. So it is assumed for any hub: a cut powers off everything under it (Plan.Affected), and
// the engine does not cut while emitting if that would power off something on air (Blockers).
//
// Without root: the capture cards' udev rule (manager/deploy/70-eirl-capture.rules) gives the group
// eirl-usbhub, which only the engine's service has, write access to the hub's /dev/bus/usb nodes. It only
// cuts for a device with a video interface (never, for example, for the touch screen).
package usbhub

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hub class requests (USB 2.0 §11.24).
const (
	clearFeature = 1
	setFeature   = 3
	getStatus    = 0
	portPower    = 8
)

// OffFor is how long the port stays without VBUS: plenty for the device to power off completely.
const OffFor = 2 * time.Second

var (
	reDev  = regexp.MustCompile(`^[0-9]+-[0-9]+(\.[0-9]+)*$`)
	reCard = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	reNode = regexp.MustCompile(`^/dev/(video[0-9]{1,3})$`)
	reSnd  = regexp.MustCompile(`^card[0-9]{1,2}$`)
)

// ControlFunc does one control transfer on a hub's usbfs node (/dev/bus/usb/BBB/DDD) and returns the
// data stage (length bytes; nil when length is 0). Tests replace it.
type ControlFunc func(node string, reqType, req uint8, value, index uint16, length int) ([]byte, error)

// Sys is where the board exposes its USB devices; tests point Root at a temporary tree and replace
// Control and Sleep.
type Sys struct {
	Root    string      // "" = /: prefix of /sys, /proc and /dev
	Control ControlFunc // nil = the USBDEVFS_CONTROL ioctl
	Sleep   func(time.Duration)
}

func (s Sys) path(p string) string { return filepath.Join(s.Root, p) }

// dev is a device's directory in sysfs.
func (s Sys) dev(name string) string { return filepath.Join(s.Root, "/sys/bus/usb/devices", name) }

func (s Sys) control(node string, reqType, req uint8, value, index uint16, length int) ([]byte, error) {
	if s.Control != nil {
		return s.Control(node, reqType, req, value, index, length)
	}
	return usbfsControl(s.path(node), reqType, req, value, index, length)
}

func (s Sys) sleep(d time.Duration) {
	if s.Sleep != nil {
		s.Sleep(d)
		return
	}
	time.Sleep(d)
}

// read is a sysfs attribute without surrounding whitespace, "" if it cannot be read.
func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// realBase is the last element of path with its links resolved, "" if it does not resolve.
func realBase(path string) string {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return filepath.Base(p)
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// ValidDevice says whether name is a sysfs USB device name ("1-2.1").
func ValidDevice(name string) bool { return reDev.MatchString(name) }

// DeviceOf is the USB device ("1-2.1") a video node hangs from, "" if none. node: /dev/videoN or a
// /dev/v4l/by-id link. /sys/class/video4linux/videoN/device is the interface ("1-2.1:1.0").
func (s Sys) DeviceOf(node string) string {
	real, err := filepath.EvalSymlinks(s.path(node))
	if err != nil {
		return ""
	}
	if s.Root != "" {
		root, err := filepath.EvalSymlinks(s.Root)
		if err != nil {
			return ""
		}
		if real, err = filepath.Rel(root, real); err != nil {
			return ""
		}
		real = "/" + real
	}
	m := reNode.FindStringSubmatch(real)
	if m == nil {
		return ""
	}
	iface := realBase(s.path("/sys/class/video4linux/" + m[1] + "/device"))
	dev, _, _ := strings.Cut(iface, ":")
	if !reDev.MatchString(dev) || !isDir(s.dev(dev)) {
		return ""
	}
	return dev
}

// DeviceOfCard is the USB device of an ALSA card by its name ("MS2109" → "1-2.1"), "" if none.
// /proc/asound/<name> links to cardN, and /sys/class/sound/cardN/device is the interface ("1-2.1:1.2").
func (s Sys) DeviceOfCard(card string) string {
	if !reCard.MatchString(card) {
		return ""
	}
	n := realBase(s.path("/proc/asound/" + card))
	if !reSnd.MatchString(n) {
		return ""
	}
	dev, _, _ := strings.Cut(realBase(s.path("/sys/class/sound/"+n+"/device")), ":")
	if !reDev.MatchString(dev) || !isDir(s.dev(dev)) {
		return ""
	}
	return dev
}

// SplitPort: "1-2.1" → ("1-2", 1): the hub and its port. A device straight on a root port ("1-2") has no
// hub to cut it: ok=false.
func SplitPort(dev string) (hub string, port int, ok bool) {
	if !reDev.MatchString(dev) || !strings.Contains(dev, ".") {
		return "", 0, false
	}
	i := strings.LastIndexByte(dev, '.')
	port, err := strconv.Atoi(dev[i+1:])
	if err != nil {
		return "", 0, false
	}
	return dev[:i], port, true
}

// HasVideo: does the device have a video interface (class 0e)? What has none is never cut.
func (s Sys) HasVideo(dev string) bool {
	d := s.dev(dev)
	ents, err := os.ReadDir(d)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), dev+":") && read(filepath.Join(d, e.Name(), "bInterfaceClass")) == "0e" {
			return true
		}
	}
	return false
}

// HubPowerMode: bits 0-1 of wHubCharacteristics in the hub descriptor (USB 2: 0x29, USB 3: 0x2a);
// 01 = per port.
func HubPowerMode(desc []byte) bool {
	return len(desc) >= 5 && (desc[1] == 0x29 || desc[1] == 0x2a) && desc[3]&0x03 == 0x01
}

// HubNode is the hub's usbfs node, "" if sysfs does not say its bus and number.
func (s Sys) HubNode(hub string) string {
	d := s.dev(hub)
	bus, num := read(d+"/busnum"), read(d+"/devnum")
	if !digits(bus) || !digits(num) {
		return ""
	}
	b, _ := strconv.Atoi(bus)
	n, _ := strconv.Atoi(num)
	return fmt.Sprintf("/dev/bus/usb/%03d/%03d", b, n)
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// TwinHubs are the hub's twins (the other half, USB 2 or USB 3, of the same chip): same vendor and
// number of ports, another speed, at the same level. On the board, "1-2" (0bda:5411) ↔ "2-1" (0bda:0411).
func (s Sys) TwinHubs(hub string) []string {
	info := func(h string) [4]string {
		d := s.dev(h)
		return [4]string{read(d + "/idVendor"), read(d + "/maxchild"), read(d + "/speed"), strconv.Itoa(strings.Count(h, "."))}
	}
	me := info(hub)
	ents, err := os.ReadDir(s.path("/sys/bus/usb/devices"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if n == hub || !reDev.MatchString(n) || read(filepath.Join(s.dev(n), "bDeviceClass")) != "09" {
			continue
		}
		o := info(n)
		if o[0] == me[0] && o[1] == me[1] && o[2] != me[2] && o[3] == me[3] {
			out = append(out, n)
		}
	}
	return out
}

// Under is every device under the hub (also under its child hubs), sorted.
func (s Sys) Under(hub string) []string {
	ents, err := os.ReadDir(s.path("/sys/bus/usb/devices"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if n := e.Name(); reDev.MatchString(n) && strings.HasPrefix(n, hub+".") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// PortPower is SetPortFeature / ClearPortFeature(PORT_POWER) on port `port` of the hub (a class request
// to the "other" recipient = the port, like uhubctl).
func (s Sys) PortPower(hub string, port int, on bool) error {
	node := s.HubNode(hub)
	if node == "" {
		return fmt.Errorf("hub %s has no node", hub)
	}
	req := uint8(clearFeature)
	if on {
		req = setFeature
	}
	_, err := s.control(node, 0x23, req, portPower, uint16(port), 0)
	return err
}

// PortStatus is the port's wPortStatus (bit 0 = something connected); ok=false if it cannot be read.
func (s Sys) PortStatus(hub string, port int) (status uint16, ok bool) {
	node := s.HubNode(hub)
	if node == "" {
		return 0, false
	}
	raw, err := s.control(node, 0xA3, getStatus, 0, uint16(port), 4)
	if err != nil || len(raw) < 2 {
		return 0, false
	}
	return uint16(raw[0]) | uint16(raw[1])<<8, true
}

// Plugged: is there something on the device's port? Enumerated (it is in sysfs) or, if it has dropped
// off the bus, what the hub says (connection bit). known=false if it cannot tell.
func (s Sys) Plugged(dev string) (plugged, known bool) {
	if dev != "" && reDev.MatchString(dev) && isDir(s.dev(dev)) {
		return true, true
	}
	hub, port, ok := SplitPort(dev)
	if !ok {
		return false, false
	}
	st, ok := s.PortStatus(hub, port)
	if !ok {
		return false, false
	}
	return st&1 != 0, true
}

// Ident is what tells one USB device from another on the same port: "vendor:product:serial" from sysfs
// ("vendor:product:" without a serial), "" if dev is not enumerated.
func (s Sys) Ident(dev string) string {
	if !reDev.MatchString(dev) || !isDir(s.dev(dev)) {
		return ""
	}
	v, p := read(filepath.Join(s.dev(dev), "idVendor")), read(filepath.Join(s.dev(dev), "idProduct"))
	if v == "" || p == "" {
		return ""
	}
	return v + ":" + p + ":" + read(filepath.Join(s.dev(dev), "serial"))
}

// Plan is what to cut for a device: the hubs (the device's and its twins that switch power) and the
// port. Why: why it cannot be done ("" = it can). Affected: the OTHER devices that power off with the
// cut (everything under those hubs: see the package comment).
type Plan struct {
	Dev      string
	Hubs     []string
	Port     int
	Why      string
	Affected []string
}

// NewPlan plans the cut for dev. knownVideo: it is already known to be a camera (its video node was
// seen) although it has now dropped off the bus and is not in sysfs. powerCheck says whether a hub
// switches power per port (Hubs.PerPortPower).
func NewPlan(s Sys, dev string, knownVideo bool, powerCheck func(hub string) bool) Plan {
	p := Plan{Dev: dev}
	hub, port, ok := SplitPort(dev)
	switch {
	case dev == "":
		p.Why = "not a USB device"
	case !ok:
		p.Why = "connected without a hub in between"
	case !videoOrKnown(s, dev, knownVideo):
		p.Why = "not a camera or a capture card"
	case !powerCheck(hub):
		p.Why = "its hub does not switch power per port"
	default:
		p.Port = port
		twins := s.TwinHubs(hub)
		p.Hubs = []string{hub}
		for _, t := range twins {
			if powerCheck(t) {
				p.Hubs = append(p.Hubs, t)
			}
		}
		// The twin counts even if it does not switch: they share the connector's power
		for _, h := range append([]string{hub}, twins...) {
			for _, n := range s.Under(h) {
				if n != dev && !strings.HasPrefix(n, dev+".") {
					p.Affected = append(p.Affected, n)
				}
			}
		}
		sort.Strings(p.Affected)
	}
	return p
}

func videoOrKnown(s Sys, dev string, known bool) bool {
	if isDir(s.dev(dev)) {
		return s.HasVideo(dev)
	}
	return known
}

// OK says whether the cut can be done.
func (p Plan) OK() bool { return p.Why == "" }

// Text is the plan for the log and the command line.
func (p Plan) Text() string {
	if !p.OK() {
		return p.Dev + ": " + p.Why
	}
	return fmt.Sprintf("%s: port %d of %s", p.Dev, p.Port, strings.Join(p.Hubs, "+"))
}

// Blockers is what is on air and would power off with the cut (onAir: {device: what}), sorted as shown
// and then by id (the same list every time: recovery compares it); empty if nothing.
func Blockers(p Plan, onAir map[string]OnAir) []OnAir {
	set := map[OnAir]bool{}
	for dev, o := range onAir {
		for _, a := range p.Affected {
			if a == dev {
				set[o] = true
			}
		}
	}
	out := make([]OnAir, 0, len(set))
	for o := range set {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Text(), out[j].Text(); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Hubs is the engine-wide side of the recovery, shared by every source: the hubs' power mode (the
// descriptor does not change) and the last cut of each hub.
type Hubs struct {
	Sys Sys

	mu    sync.Mutex
	power map[string]bool      // hub node → per-port power?
	cuts  map[string]time.Time // hub → last cut, by any source
}

// NewHubs makes the engine's one Hubs.
func NewHubs(s Sys) *Hubs {
	return &Hubs{Sys: s, power: map[string]bool{}, cuts: map[string]time.Time{}}
}

// PerPortPower: does the hub declare per-port power switching? It asks the hub (class GET_DESCRIPTOR):
// sysfs does not keep it. false if it cannot ask; that is not remembered (the udev rule may come later).
func (h *Hubs) PerPortPower(hub string) bool {
	s := h.Sys
	node := s.HubNode(hub)
	if node == "" || read(filepath.Join(s.dev(hub), "bDeviceClass")) != "09" {
		return false
	}
	h.mu.Lock()
	v, ok := h.power[node]
	h.mu.Unlock()
	if ok {
		return v
	}
	kind := uint16(0x29)
	if sp := read(filepath.Join(s.dev(hub), "speed")); sp == "5000" || sp == "10000" {
		kind = 0x2a
	}
	desc, err := s.control(node, 0xA0, 6, kind<<8, 0, 12)
	if err != nil {
		return false
	}
	v = HubPowerMode(desc)
	h.mu.Lock()
	h.power[node] = v
	h.mu.Unlock()
	return v
}

// Plan is NewPlan with the hubs' real power mode.
func (h *Hubs) Plan(dev string, knownVideo bool) Plan {
	return NewPlan(h.Sys, dev, knownVideo, h.PerPortPower)
}

// Claim asks for the turn to cut the plan's hubs: busy=false if it may (and it is noted down), or the
// time of the last cut of one of them if that was less than quiet ago. Each source recovers from its own
// goroutine: without this, two hung sources on the same hub would cut it one after the other, and the
// second cut would catch everything else coming back from the first.
func (h *Hubs) Claim(p Plan, now time.Time, quiet time.Duration) (last time.Time, busy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, hub := range p.Hubs {
		if t, ok := h.cuts[hub]; ok && t.After(last) {
			last = t
		}
	}
	if !last.IsZero() && now.Sub(last) < quiet {
		return last, true
	}
	for _, hub := range p.Hubs {
		h.cuts[hub] = now
	}
	return time.Time{}, false
}

// LastCut is the last cut of any hub (zero if none): everything under it drops off the bus for a few
// seconds and comes back by itself, so that is no camera being unplugged.
func (h *Hubs) LastCut() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	var last time.Time
	for _, t := range h.cuts {
		if t.After(last) {
			last = t
		}
	}
	return last
}

// Cycle cuts the port's VBUS on the plan's hubs, waits off and gives it back, in reverse order. It
// always tries to give the power back, even if cutting failed on one of them. done: the hubs where the
// cut worked.
func (s Sys) Cycle(p Plan, off time.Duration) (done []string, errs []string) {
	for _, hub := range p.Hubs {
		if err := s.PortPower(hub, p.Port, false); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", hub, err))
			continue
		}
		done = append(done, hub)
	}
	s.sleep(off)
	for i := len(p.Hubs) - 1; i >= 0; i-- {
		if err := s.PortPower(p.Hubs[i], p.Port, true); err != nil {
			errs = append(errs, fmt.Sprintf("%s (powering back on): %v", p.Hubs[i], err))
		}
	}
	return done, errs
}
