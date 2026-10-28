package appapi

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// Local recording for the screen's Recording page (RECORDING.md §6.2): GET /recording only while the page is on the
// screen, and its three actions (rec_settings, rec_eject, rec_format). REC itself is the engine's record command, not
// the app's. What the app says is checked against closed lists (the disk's model and label are third-party data: the
// text rule) and whatever does not fit is left out; what the screen asks is checked too before it goes: only its
// route's fields, each of the right shape.

const (
	RecordingEvery = 2 * time.Second // the Recording page
	// The app waits for the storage helper up to 90 s (eject) and 120 s (format): a little more here
	EjectTimeout  = 100 * time.Second
	FormatTimeout = 130 * time.Second
	// MaxRecQualities: the most qualities kept (the app lists four)
	MaxRecQualities = 8
)

var (
	recModes     = set("off", "manual", "with_output")
	recQualities = set("standard", "high", "max", "efficient")
	recCodecs    = set("h264", "h265")
	diskStates   = set("none", "checking", "ready", "unsupported", "unformatted", "error", "ejected")
	diskReasons  = set("fs_ntfs", "fs_unknown", "too_small", "mount_failed", "readonly", "check_failed")
	reDisk       = regexp.MustCompile(`^sd[a-z]$`)
	reFS         = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
)

// isRec: one of the Recording page's actions.
func isRec(what string) bool { return strings.HasPrefix(what, "rec_") }

// recRequest checks one of the page's actions and builds its body: rec_settings, a known mode and/or quality and
// nothing else; rec_eject, nothing; rec_format, the seq the question named. ok false: it is not sent.
func recRequest(a ui.AppAction) (body []byte, ok bool) {
	if a.ID != "" || a.Value != "" || len(a.Secrets) > 0 {
		return nil, false
	}
	switch a.What {
	case "rec_settings":
		out := map[string]string{}
		for k, v := range a.Body {
			s, _ := v.(string)
			if !(k == "mode" && recModes[s]) && !(k == "quality" && recQualities[s]) {
				return nil, false
			}
			out[k] = s
		}
		if len(out) == 0 {
			return nil, false
		}
		b, _ := json.Marshal(out)
		return b, true
	case "rec_eject":
		return []byte("{}"), len(a.Body) == 0
	case "rec_format":
		seq, isInt := a.Body["seq"].(int64)
		if len(a.Body) != 1 || !isInt || seq < 0 {
			return nil, false
		}
		return []byte(`{"seq":` + strconv.FormatInt(seq, 10) + `}`), true
	}
	return nil, false
}

// recTimeout: how long a recording action is waited for (eject and format wait for the storage helper).
func recTimeout(what string, def time.Duration) time.Duration {
	switch what {
	case "rec_eject":
		return max(def, EjectTimeout)
	case "rec_format":
		return max(def, FormatTimeout)
	}
	return def
}

// int64In is v as a whole JSON number in [lo, hi].
func int64In(v any, lo, hi int64) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || !reInt.MatchString(string(n)) {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || i < lo || i > hi {
		return 0, false
	}
	return i, true
}

// storageInfo is the storage object (GET /storage, eject's and format's answer, GET /recording's storage); ok false
// without a known state.
func storageInfo(v any) (ui.StorageInfo, bool) {
	m := obj(v)
	st := oneOf(m, "state", diskStates)
	if m == nil || st == "" {
		return ui.StorageInfo{}, false
	}
	// The model and the label: the rule of a device's name (as the app cleans them)
	s := ui.StorageInfo{State: st, Reason: oneOf(m, "reason", diskReasons), Model: btName(m, "model"),
		Label: btName(m, "label")}
	s.Seq, _ = int64In(m["seq"], 0, 1<<53)
	if dev, _ := m["device"].(string); reDisk.MatchString(dev) {
		s.Device = dev
	}
	if other, _ := m["anotherDisk"].(string); reDisk.MatchString(other) {
		s.AnotherDisk = other
	}
	if fs, _ := m["fs"].(string); reFS.MatchString(fs) {
		s.FS = fs
	}
	s.SizeBytes, _ = int64In(m["sizeBytes"], 0, 1<<53)
	s.ReadOnly, _ = boolOf(m["readOnly"])
	s.Dirty, _ = boolOf(m["dirty"])
	s.WriteMBps, _ = floatIn(m["writeMBps"], 0, 100000)
	return s, true
}

// recordingInfo is GET /recording (and PUT /recording's answer); nil if it is not one (no mode or no storage).
func recordingInfo(v any) *ui.RecordingInfo {
	m := obj(v)
	mode := oneOf(m, "mode", recModes)
	st, ok := storageInfo(m["storage"])
	if m == nil || mode == "" || !ok {
		return nil
	}
	r := &ui.RecordingInfo{Mode: mode, Quality: oneOf(m, "quality", recQualities), Storage: st}
	qs, _ := m["qualities"].([]any)
	for _, x := range qs {
		qm := obj(x)
		id := oneOf(qm, "id", recQualities)
		if id == "" || r.QualityOf(id) != nil || len(r.Qualities) >= MaxRecQualities {
			continue
		}
		q := ui.RecQuality{ID: id, Codec: oneOf(qm, "codec", recCodecs)}
		q.VideoKbps, _ = intIn(qm["videoKbps"], 0, 1000000)
		q.AudioKbps, _ = intIn(qm["audioKbps"], 0, 10000)
		r.Qualities = append(r.Qualities, q)
	}
	return r
}

// withStorage is the recording view with another disk (eject's and format's answer): a copy, the screen may be
// reading the old one.
func withStorage(r *ui.RecordingInfo, s ui.StorageInfo) *ui.RecordingInfo {
	if r == nil {
		return nil
	}
	n := *r
	n.Storage = s
	return &n
}

// pollRecording asks GET /recording.
func (d *DeviceState) pollRecording(ui.AppWatch) {
	_, v, e := d.call("", "GET", "/recording", nil, d.getTimeout)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case e == nil:
		if r := recordingInfo(v); r != nil {
			d.snap.Recording, d.snap.RecordingAPI = r, ui.Yes
		}
	case d.gone(e):
		d.snap.Recording, d.snap.RecordingAPI = nil, ui.No
	}
	// Passing (no answer, a 5xx): the last one stays
}

// PollRecording is pollRecording for tests and tools.
func (d *DeviceState) PollRecording() { d.pollRecording(ui.AppWatch{}) }
