package modem

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HiLink: Huawei modems with a router inside (E3372h-320, E8372…). Linux sees an Ethernet card (cdc_ether,
// RNDIS or NCM) behind which the modem does NAT and serves a web API on its gateway (192.168.8.1 by default).
// That API gives the signal, network, SIM and connection. The fields differ between firmware versions:
// everything is optional, and an interface that does not answer like a HiLink is not asked again for a minute.
//
// Requests leave from the link's own IP, so the dispatcher's source routing sends them through that very
// interface (two HiLink modems on different subnets do not mix; on the same subnet the second one is a
// `conflict` and is not routed: MODEMS.md §7).
type HiLink struct {
	SysNet    string // /sys/class/net
	RouteFile string // /proc/net/route
	// Client: an HTTP client whose connections leave from ip (nil = the default one)
	Client func(ip string) *http.Client
	Now    func() time.Time

	baseURL func(gw string) string // tests

	mu    sync.Mutex
	skip  map[string]time.Time  // interface|MAC that never answered like a HiLink → until when it is not asked
	known map[string]knownModem // interface|MAC that did: its last good reading
	cfg   map[string]hlConfig   // interface|MAC → its mode and roaming, read every cfgEvery
}

// hlConfig: what a HiLink has configured, read less often than its signal.
type hlConfig struct {
	mode    string
	roaming bool
	at      time.Time
}

const cfgEvery = time.Minute

// forget: the next reading asks for the modem's configuration again (after changing it).
func (h *HiLink) forget(md Modem) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.cfg {
		if strings.HasPrefix(k, md.Iface+"|") {
			delete(h.cfg, k)
		}
	}
}

type knownModem struct {
	md Modem
	at time.Time
}

// NewHiLink: the backend on the real system.
func NewHiLink() *HiLink { return &HiLink{SysNet: "/sys/class/net", RouteFile: "/proc/net/route"} }

func (h *HiLink) Name() string { return "hilink" }

// hilinkDrivers: the USB network drivers a HiLink modem shows up with.
var hilinkDrivers = map[string]bool{"cdc_ether": true, "rndis_host": true, "cdc_ncm": true}

const (
	notHiLinkFor = time.Minute
	// keepFor: a HiLink that stops answering (busy, rebooting its web server) keeps its last reading this long,
	// so that its dot does not flicker; then it is a plain USB link until it answers again
	keepFor = 30 * time.Second
	// requestTimeout: the modem is one USB cable away; a slow answer is a busy modem
	requestTimeout = 1500 * time.Millisecond
)

// List: the HiLink modems among the available links, read at the same time. If ctx ends first it is an error:
// the Manager then keeps the previous list instead of a short one.
func (h *HiLink) List(ctx context.Context, links []Link) ([]Modem, error) {
	now := h.now()
	type result struct {
		key string
		md  Modem
		ok  bool
	}
	var wg sync.WaitGroup
	results := make(chan result, len(links))
	// Two identical sticks hand out the same address: the dispatcher routes the first by name (its /sys/class/net
	// order) and marks the other one in conflict. Asking from that address reaches the first, so the other one is not
	// asked at all (its readings would be its twin's, and an order for it would go to its twin)
	byIP := map[string]string{}
	type twin struct {
		l       Link
		gw, mac string
		of      string // the reachable one's interface
	}
	var twins []twin
	sorted := append([]Link(nil), links...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, l := range sorted {
		if !hilinkDrivers[h.driver(l.Name)] {
			continue
		}
		mac := h.mac(l.Name)
		key := l.Name + "|" + mac
		gw := gateway(h.RouteFile, l.Name)
		if gw == "" {
			continue
		}
		if first, dup := byIP[l.IP]; dup && l.IP != "" {
			twins = append(twins, twin{l, gw, mac, first})
			continue
		}
		byIP[l.IP] = l.Name
		h.mu.Lock()
		until, skipped := h.skip[key]
		h.mu.Unlock()
		if skipped && now.Before(until) {
			continue
		}
		wg.Add(1)
		go func(l Link, gw, mac, key string) {
			defer wg.Done()
			md, ok := h.read(ctx, l, gw, mac)
			results <- result{key, md, ok}
		}(l, gw, mac, key)
	}
	wg.Wait()
	close(results)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.skip == nil {
		h.skip, h.known = map[string]time.Time{}, map[string]knownModem{}
	}
	var out []Modem
	for r := range results {
		switch k, wasKnown := h.known[r.key]; {
		case r.ok:
			delete(h.skip, r.key)
			h.known[r.key] = knownModem{r.md, now}
			out = append(out, r.md)
		case wasKnown && now.Sub(k.at) < keepFor:
			out = append(out, k.md)
		case wasKnown:
			delete(h.known, r.key) // gone quiet for too long: a plain USB link until it answers again
		default:
			h.skip[r.key] = now.Add(notHiLinkFor)
		}
	}
	// A twin of a HiLink is taken for one (same driver, same address): all it can do is move a network apart
	for _, t := range twins {
		for _, md := range out {
			if md.Iface == t.of {
				out = append(out, Modem{Backend: "hilink", ID: stableID("h-", t.mac), Iface: t.l.Name, Manufacturer: "Huawei",
					Model: "HiLink", State: "unknown", SIM: "unknown", Mode: "auto", gateway: t.gw, localIP: t.l.IP,
					twinOf: md.ID, Can: []string{"subnet"}})
				break
			}
		}
	}
	return out, nil
}

// read: one HiLink modem. ok = it answered the session request like a HiLink. The four readings go at the
// same time (each one optional): a slow modem costs one timeout, not four.
func (h *HiLink) read(ctx context.Context, l Link, gw, mac string) (Modem, bool) {
	base := "http://" + gw
	if h.baseURL != nil {
		base = h.baseURL(gw)
	}
	cl := h.client(l.IP)
	ses, err := h.get(ctx, cl, base+"/api/webserver/SesTokInfo", nil)
	if err != nil || ses["SesInfo"] == "" && ses["TokInfo"] == "" {
		return Modem{}, false
	}
	hdr := http.Header{}
	if v := ses["SesInfo"]; v != "" {
		hdr.Set("Cookie", v)
	}
	if v := ses["TokInfo"]; v != "" {
		hdr.Set("__RequestVerificationToken", v)
	}
	paths := []string{"/api/monitoring/status", "/api/device/signal", "/api/net/current-plmn", "/api/pin/status"}
	key := l.Name + "|" + mac
	h.mu.Lock()
	cfg, haveCfg := h.cfg[key]
	h.mu.Unlock()
	if !haveCfg || h.now().Sub(cfg.at) >= cfgEvery {
		paths = append(paths, "/api/net/net-mode", "/api/dialup/connection")
	}
	pages := make([]map[string]string, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			if m, err := h.get(ctx, cl, base+path, hdr); err == nil {
				pages[i] = m
			}
		}(i, path)
	}
	wg.Wait()
	md := Modem{Backend: "hilink", ID: stableID("h-", mac), Iface: l.Name, Manufacturer: "Huawei", Model: "HiLink",
		State: "unknown", SIM: "unknown", gateway: gw, localIP: l.IP, Modes: []string{"auto", "4g"},
		Can: []string{"apn", "roaming", "mode", "pin", "reset", "subnet"}}
	if st := pages[0]; st != nil {
		hilinkStatus(&md, st)
	}
	if sig := pages[1]; sig != nil {
		hilinkSignal(&md, sig)
	}
	if plmn := pages[2]; plmn != nil {
		if v := plmn["FullName"]; v != "" {
			md.Operator = v
		} else {
			md.Operator = plmn["ShortName"]
		}
	}
	if pin := pages[3]; pin != nil {
		if s := hilinkSIM(pin["SimState"]); s != "" {
			md.SIM = s
		}
	}
	if len(pages) > 4 && pages[4] != nil && pages[5] != nil {
		cfg = hlConfig{mode: hilinkMode(pages[4]["NetworkMode"]), roaming: pages[5]["RoamAutoConnectEnable"] == "1", at: h.now()}
		haveCfg = true
		h.mu.Lock()
		if h.cfg == nil {
			h.cfg = map[string]hlConfig{}
		}
		h.cfg[key] = cfg
		h.mu.Unlock()
	}
	md.Mode, md.Roaming = "auto", false
	if haveCfg {
		md.Mode, md.Roaming = cfg.mode, cfg.roaming
	}
	return md, true
}

// hilinkStatus: /api/monitoring/status.
func hilinkStatus(md *Modem, st map[string]string) {
	switch atoi(st["ConnectionStatus"]) {
	case 901:
		md.State = "connected"
	case 900:
		md.State = "connecting"
	case 0:
	default: // 902 disconnected, 903 disconnecting, and the failure codes
		md.State = "disconnected"
	}
	nt := atoi(st["CurrentNetworkTypeEx"])
	if nt == 0 {
		nt = atoi(st["CurrentNetworkType"])
	}
	md.Tech = hilinkTech(nt)
	switch {
	case st["ServiceStatus"] != "" && atoi(st["ServiceStatus"]) != 2:
		md.Registration = "searching"
	case st["RoamingStatus"] == "1":
		md.Registration = "roaming"
	case st["ServiceStatus"] == "2":
		md.Registration = "home"
	}
	if md.State == "unknown" && md.Registration == "searching" {
		md.State = "searching"
	}
	if v, ok := st["SignalIcon"]; ok && v != "" {
		md.Signal.Bars = ptrI(atoi(v))
	}
	// SimStatus: 1 valid; 0 (or 255) no valid SIM. /api/pin/status, when it answers, says more
	switch st["SimStatus"] {
	case "1":
		md.SIM = "ready"
	case "0", "255":
		md.SIM = "missing"
	}
}

// hilinkTech: Huawei's network type codes. Unknown codes (newer firmware) leave the technology unknown.
func hilinkTech(nt int) string {
	switch nt {
	case 19, 101, 1011: // LTE, LTE (Ex), LTE+ (carrier aggregation)
		return "4g"
	case 111: // NR (5G HiLink firmware; not verified on a device yet)
		return "5g"
	case 1, 2, 3, 21, 22, 23:
		return "2g"
	case 4, 5, 6, 7, 8, 9, 17, 18, 41, 44, 45, 46, 61, 62, 63, 64, 65, 81:
		return "3g"
	}
	return ""
}

// hilinkSIM: /api/pin/status SimState.
func hilinkSIM(v string) string {
	switch atoi(v) {
	case 257, 258: // ready (PIN off or already entered)
		return "ready"
	case 255:
		return "missing"
	case 260:
		return "pin"
	case 261:
		return "puk"
	case 256:
		return "error"
	}
	return ""
}

// hilinkSignal: /api/device/signal. Values come with their unit and sometimes a comparator ("-95dBm",
// ">=-51dBm", "-11dB").
func hilinkSignal(md *Modem, sig map[string]string) {
	for key, dst := range map[string]**float64{"rsrp": &md.Signal.RSRP, "rsrq": &md.Signal.RSRQ, "sinr": &md.Signal.SINR, "rssi": &md.Signal.RSSI} {
		if v, ok := number(sig[key]); ok {
			*dst = ptrF(v)
		}
	}
}

var reNumber = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

func number(s string) (float64, bool) {
	m := reNumber.FindString(s)
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(m, 64)
	return v, err == nil && v > -500 && v < 500
}

func atoi(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

// errHiLink: the API answered <error><code>…</code></error> (login required, wrong token…).
type errHiLink string

func (e errHiLink) Error() string { return "hilink error " + string(e) }

// get: one request; the answer's <response> children as a flat map.
func (h *HiLink) get(ctx context.Context, cl *http.Client, url string, hdr http.Header) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return flatXML(io.LimitReader(resp.Body, 64<<10))
}

// flatXML: the text of each child of the root element. An <error> root is an error with its code.
func flatXML(r io.Reader) (map[string]string, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	out := map[string]string{}
	depth := 0
	root, cur := "", ""
	var text bytes.Buffer
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch depth {
			case 1:
				root = t.Name.Local
			case 2:
				cur = t.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if depth == 2 {
				text.Write(t)
			}
		case xml.EndElement:
			if depth == 2 {
				out[cur] = strings.TrimSpace(text.String())
			}
			depth--
		}
	}
	switch root {
	case "response":
		return out, nil
	case "error":
		return nil, errHiLink(out["code"])
	}
	return nil, fmt.Errorf("not a HiLink answer (%q)", root)
}

func (h *HiLink) client(ip string) *http.Client {
	if h.Client != nil {
		return h.Client(ip)
	}
	d := &net.Dialer{Timeout: requestTimeout}
	if a := net.ParseIP(ip); a != nil {
		d.LocalAddr = &net.TCPAddr{IP: a}
	}
	return &http.Client{Timeout: requestTimeout, Transport: &http.Transport{DialContext: d.DialContext, Proxy: nil,
		DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (h *HiLink) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// driver: the kernel driver behind an interface (cdc_ether, rndis_host, qmi_wwan…), or "".
func (h *HiLink) driver(name string) string {
	p, err := filepath.EvalSymlinks(filepath.Join(h.SysNet, name, "device", "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(p)
}

func (h *HiLink) mac(name string) string {
	b, err := os.ReadFile(filepath.Join(h.SysNet, name, "address"))
	if err != nil {
		return name
	}
	return strings.TrimSpace(string(b))
}

// gateway: the default gateway of an interface in the main table (/proc/net/route), lowest metric first.
func gateway(routeFile, iface string) string {
	b, err := os.ReadFile(routeFile)
	if err != nil {
		return ""
	}
	best, bestMetric := "", -1
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[0] != iface || f[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&0x2 == 0 { // RTF_GATEWAY
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		metric, _ := strconv.Atoi(f[6])
		if bestMetric >= 0 && metric >= bestMetric {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(raw))
		best, bestMetric = ip.String(), metric
	}
	return best
}
