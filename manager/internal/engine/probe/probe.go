// Package probe reads what `eirl-encoderd probe` says: the source catalogue (-list) and the mode chosen
// for each USB source (-device, -all). It is another program's output: read as data, through a
// whitelist, and nothing that does not fit is used (SOURCES.md §1).
package probe

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
)

var keys = map[string]*regexp.Regexp{
	"NODE":      regexp.MustCompile(`^/dev/video[0-9]{1,3}$`),
	"NAME":      regexp.MustCompile(`^.{1,64}$`),
	"FORMAT":    regexp.MustCompile(`^(NV12|YUY2|I420|UYVY|MJPG)$`),
	"WIDTH":     regexp.MustCompile(`^[0-9]{2,5}$`),
	"HEIGHT":    regexp.MustCompile(`^[0-9]{2,5}$`),
	"FPS_NUM":   regexp.MustCompile(`^[1-9][0-9]{0,5}$`),
	"FPS_DEN":   regexp.MustCompile(`^[1-9][0-9]{0,5}$`),
	"ALSA_CARD": regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`),
	"USB_SPEED": regexp.MustCompile(`^[0-9]{1,6}$`),
	"WARNINGS":  regexp.MustCompile(`^[a-z0-9_,]{0,128}$`),
}

var (
	reNameBad  = regexp.MustCompile(`[^A-Za-z0-9 ._()+-]`)
	reSourceID = regexp.MustCompile(`^/dev/v4l/by-(id|path)/[A-Za-z0-9_:.-]{1,200}$`)
	reAllLine  = regexp.MustCompile(`^SRC([0-9])_([A-Z_]+)=(.*)$`)
	reNode     = regexp.MustCompile(`^/dev/video[0-9]{1,3}$`)
	reALSA     = regexp.MustCompile(`^alsa:[A-Za-z0-9_-]{1,32}$`)
)

// CleanName keeps the characters a name may have in logs, the web and the screen (≤48).
func CleanName(s string) string {
	s = reNameBad.ReplaceAllString(s, "")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// Result is one parsed probe answer: the chosen mode, or only the warnings when nothing can be captured.
type Result struct {
	pipeline.Probe
	Complete bool // NODE, FORMAT, WIDTH, HEIGHT and the fps are there
}

// HasWarning says whether the comma list of warnings has w (usb2, no_bus, no_format…).
func (r Result) HasWarning(w string) bool {
	return strings.Contains(","+r.Warnings+",", ","+w+",")
}

// Parse reads `probe -device` (KEY=VALUE lines). ok=false if something essential is missing.
func Parse(text string) (Result, bool) {
	m := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		k, v, _ := strings.Cut(line, "=")
		re, known := keys[k]
		if !known || !re.MatchString(v) {
			continue
		}
		if k == "NAME" {
			v = CleanName(v)
		}
		m[k] = v
	}
	return fromMap(m)
}

func fromMap(m map[string]string) (Result, bool) {
	atoi := func(k string) int { n, _ := strconv.Atoi(m[k]); return n }
	r := Result{Probe: pipeline.Probe{
		Node: m["NODE"], Name: m["NAME"], Format: m["FORMAT"],
		Width: atoi("WIDTH"), Height: atoi("HEIGHT"), FPSNum: atoi("FPS_NUM"), FPSDen: atoi("FPS_DEN"),
		ALSACard: m["ALSA_CARD"], Warnings: m["WARNINGS"], USBSpeed: atoi("USB_SPEED"),
	}}
	for _, k := range []string{"NODE", "FORMAT", "WIDTH", "HEIGHT", "FPS_NUM", "FPS_DEN"} {
		if _, ok := m[k]; !ok {
			return r, false
		}
	}
	r.Complete = true
	return r, true
}

// ParseAll reads `probe -all` (SOURCES.md §1): one result per source id. A source that does not fit or
// cannot be captured comes without FORMAT and with the reason in WARNINGS (Complete=false).
func ParseAll(text string) map[string]Result {
	blocks := map[string]map[string]string{}
	var order []string
	for _, line := range strings.Split(text, "\n") {
		g := reAllLine.FindStringSubmatch(line)
		if g == nil {
			continue
		}
		b, ok := blocks[g[1]]
		if !ok {
			b = map[string]string{}
			blocks[g[1]] = b
			order = append(order, g[1])
		}
		b[g[2]] = g[3]
	}
	out := map[string]Result{}
	for _, n := range order {
		b := blocks[n]
		sid := b["ID"]
		if !reSourceID.MatchString(sid) {
			continue
		}
		var lines []string
		for k, v := range b {
			lines = append(lines, k+"="+v)
		}
		r, ok := Parse(strings.Join(lines, "\n"))
		if !ok {
			r = Result{}
			if w, has := b["WARNINGS"]; has && keys["WARNINGS"].MatchString(w) {
				r.Warnings = w
			}
		}
		out[sid] = r
	}
	return out
}

// KeepArg is the -keep value for a USB source already live, so the bus split respects it.
func KeepArg(id string, p pipeline.Probe) string {
	return fmt.Sprintf("%s=%s:%dx%d@%d/%d", id, p.Format, p.Width, p.Height, p.FPSNum, p.FPSDen)
}

// Entry is one video source of the catalogue.
type Entry struct {
	ID, Kind, Name, Node string // Kind: hdmi-csi | usb | network | test
	// Audio is the audio input of the same device ("alsa:<card>" of a USB camera, the network camera's
	// own id), "" if none: the audio of a camera follows its video slot (Engine.syncAudio)
	Audio string
}

// AudioEntry is one audio input of the catalogue (used by the audio phase). Profile: a Bluetooth input's
// (a2dp | sco; checked where the device is built).
type AudioEntry struct {
	ID, Kind, Name, Profile string
}

// Catalogue is `probe -list`. OK=false when the answer is not a catalogue: then nothing is removed or
// renamed.
type Catalogue struct {
	Sources []Entry
	Audio   []AudioEntry
	OK      bool
}

// NetworkValid checks a network camera of the catalogue: its id and its exact read URL.
type NetworkValid func(id, node string) bool

// NetworkName cleans a camera name typed by the user.
type NetworkName func(name string) string

// ParseList reads `probe -list` like Engine.discovered: the board's HDMI first, then USB and network
// cameras in catalogue order, the bars first of all when test is on. Entries that do not fit are skipped.
func ParseList(out string, test bool, netValid NetworkValid, netName NetworkName) Catalogue {
	var raw struct {
		Sources []map[string]any `json:"sources"`
		Audio   []map[string]any `json:"audio"`
	}
	var c Catalogue
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &probe) == nil {
		if src, ok := probe["sources"]; ok && json.Unmarshal(src, &raw.Sources) == nil {
			c.OK = true
		}
		if a, ok := probe["audio"]; ok {
			_ = json.Unmarshal(a, &raw.Audio)
		}
	}
	var want []Entry
	if test {
		want = append(want, Entry{ID: "test", Kind: "test", Name: "Barras"})
	}
	str := func(m map[string]any, k string) (string, bool) { s, ok := m[k].(string); return s, ok }
	for _, s := range raw.Sources {
		id, ok := str(s, "id")
		if !ok {
			continue
		}
		kind, _ := str(s, "kind")
		node, _ := str(s, "node")
		switch kind {
		case "hdmi-csi":
			if reNode.MatchString(node) {
				want = append([]Entry{{ID: "hdmi", Kind: "hdmi-csi", Name: "HDMI de la placa", Node: node}}, want...)
			}
		case "usb":
			if reSourceID.MatchString(id) {
				name := "USB"
				if n, ok := s["name"]; ok {
					name = fmt.Sprint(n)
				}
				if name = CleanName(name); name == "" {
					name = "USB"
				}
				e := Entry{ID: id, Kind: "usb", Name: name}
				if a, ok := s["audio"].(map[string]any); ok {
					if aid, _ := a["id"].(string); reALSA.MatchString(aid) {
						e.Audio = aid
					}
				}
				want = append(want, e)
			}
		case "network":
			if netValid != nil && netValid(id, node) {
				name, _ := str(s, "name")
				want = append(want, Entry{ID: id, Kind: "network", Name: netName(name), Node: node, Audio: id})
			}
		}
	}
	c.Sources = want
	for _, a := range raw.Audio {
		id, _ := str(a, "id")
		kind, _ := str(a, "kind")
		name, _ := str(a, "name")
		profile, _ := str(a, "profile")
		if id != "" {
			c.Audio = append(c.Audio, AudioEntry{ID: id, Kind: kind, Name: name, Profile: profile})
		}
	}
	return c
}
