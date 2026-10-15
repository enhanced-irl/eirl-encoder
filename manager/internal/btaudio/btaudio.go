// Package btaudio is the file through which the app tells the engine which Bluetooth devices are ready for audio
// (BLUETOOTH.md §3.2): /run/eirl-encoder/bluetooth.json, written by eirl-encoderd and read by the engine (the
// monitor's target) and by `probe -list` (the audio inputs). No D-Bus here: the engine only reads a file.
//
// The reader trusts nothing: an address of the exact shape, one of the two profiles, a cleaned name and bounded
// numbers; it builds the ALSA device itself from them, so the file can never name another device.
package btaudio

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Path is where eirl-encoderd leaves the file (its RuntimeDirectory; the engine is in its group).
const Path = "/run/eirl-encoder/bluetooth.json"

// Version is the file's format: it only changes with an incompatible one (a reader ignores another).
const Version = 1

// Profiles: A2DP (stereo, one way) and hands-free (SCO: mono, phone quality, both ways).
const (
	ProfileA2DP = "a2dp"
	ProfileSCO  = "sco"
)

// MaxDelayMs bounds the delay a device reports (what Bluetooth adds after the ALSA device).
const MaxDelayMs = 1000

// MaxInputs bounds how many inputs the file carries (the engine has four audio slots).
const MaxInputs = 4

// MaxSize bounds the file.
const MaxSize = 8 << 10

// Device is a Bluetooth device ready for audio in one direction.
type Device struct {
	Addr    string `json:"addr"`              // AA:BB:CC:DD:EE:FF
	Name    string `json:"name"`              // for the web, the screen and the logs (cleaned)
	Profile string `json:"profile"`           // a2dp | sco
	DelayMs int    `json:"delayMs,omitempty"` // what the device adds after the ALSA device, as BlueALSA reports it
}

// File is the whole file. Out is where the monitor plays (nil: the screen's jack); In, the inputs.
type File struct {
	V   int      `json:"v"`
	Out *Device  `json:"out"`
	In  []Device `json:"in"`
}

var reAddr = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)

// nameSigns: the punctuation a device's name keeps besides letters, digits and spaces.
const nameSigns = "._()+'-&,!?#/:@"

// ValidAddr: a Bluetooth address as BlueZ writes it (upper case, with colons).
func ValidAddr(a string) bool { return reAddr.MatchString(a) }

// CleanName: a device's name as the web, the screen and the logs may show it (third-party data): letters and digits
// of any script (a phone called «Écouteurs de Zoé» keeps its accents), spaces and a few signs; no control or format
// characters, no symbols; at most 48 characters. The same rule as mixer.CleanDeviceName, and the adapter's alias.
func CleanName(s string) string {
	s = strings.ToValidUTF8(s, " ")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || strings.ContainsRune(nameSigns, r):
			return r
		}
		return ' '
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 48 {
		s = strings.TrimSpace(string([]rune(s)[:48]))
	}
	return s
}

// ID is the audio input id of a device: bt:<address without colons> (the engine's and the app's audio ids).
func ID(addr string) string { return "bt:" + strings.ReplaceAll(addr, ":", "") }

// AddrOf is the address of an audio id bt:<12 hex digits>, or "" if it is not one.
func AddrOf(id string) string {
	h, ok := strings.CutPrefix(id, "bt:")
	if !ok || len(h) != 12 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	if a := b.String(); ValidAddr(a) {
		return a
	}
	return ""
}

// ALSADevice is the ALSA device of the BlueALSA plugin for a valid device: built here, never taken from the file.
func ALSADevice(d Device) string {
	return fmt.Sprintf("bluealsa:DEV=%s,PROFILE=%s", d.Addr, d.Profile)
}

// clean checks one device; false: it is left out.
func clean(d Device) (Device, bool) {
	if !ValidAddr(d.Addr) || d.Profile != ProfileA2DP && d.Profile != ProfileSCO {
		return Device{}, false
	}
	d.Name = CleanName(d.Name)
	if d.Name == "" {
		d.Name = d.Addr
	}
	d.DelayMs = min(max(d.DelayMs, 0), MaxDelayMs)
	return d, true
}

// Parse reads the file's content: what is valid in it (an input that does not check out is left out, as is a
// repeated one; an output that does not check out is none). ok false: not a file of this version (nothing ready).
func Parse(data []byte) (File, bool) {
	var f File
	if len(data) > MaxSize || json.Unmarshal(data, &f) != nil || f.V != Version {
		return File{V: Version}, false
	}
	out := File{V: Version}
	if f.Out != nil {
		if d, ok := clean(*f.Out); ok {
			out.Out = &d
		}
	}
	seen := map[string]bool{}
	for _, d := range f.In {
		d, ok := clean(d)
		if !ok || seen[d.Addr] || len(out.In) >= MaxInputs {
			continue
		}
		seen[d.Addr] = true
		out.In = append(out.In, d)
	}
	return out, true
}

// Read reads the file at path: a missing or broken file is no device ready.
func Read(path string) File {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{V: Version}
	}
	f, _ := Parse(data)
	return f
}

// Marshal is the file's content for f (the app's side), with a final newline. The same f gives the same bytes, so
// the app can skip writing when nothing changed.
func Marshal(f File) []byte {
	f.V = Version
	if f.In == nil {
		f.In = []Device{}
	}
	b, _ := json.Marshal(f)
	return append(b, '\n')
}

// InputsKey: what of the file the engine's discovery cares about (does it need to probe again?): the inputs, by
// address and profile. Headphones connecting, a name or a delay changing do not make it probe.
func InputsKey(f File) string {
	var b strings.Builder
	for _, d := range f.In {
		b.WriteString(d.Addr + "/" + d.Profile + ";")
	}
	return b.String()
}

// Reader is the engine's side of the file: it reads it again only when it changed (its size or its time: the app
// replaces it whole), and a file that is missing or broken KEEPS the last good one. The app restarting (systemd
// empties its RuntimeDirectory) must not cut a Bluetooth input or the headphones on air: a device really gone,
// BlueALSA closes, and the engine's pipeline errs on its own. With no file ever read: nothing ready.
type Reader struct {
	path string
	mu   sync.Mutex
	key  string
	last File
}

// NewReader reads path (Path when "").
func NewReader(path string) *Reader {
	if path == "" {
		path = Path
	}
	return &Reader{path: path, last: File{V: Version}}
}

// Get is the file as last read (a copy).
func (r *Reader) Get() File {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, err := os.Stat(r.path); err == nil {
		key := fmt.Sprintf("%d/%d", st.Size(), st.ModTime().UnixNano())
		if key != r.key {
			if data, err := os.ReadFile(r.path); err == nil {
				r.key = key
				if f, ok := Parse(data); ok {
					r.last = f
				}
			}
		}
	}
	f := r.last
	if f.Out != nil {
		o := *f.Out
		f.Out = &o
	}
	f.In = append([]Device(nil), f.In...)
	return f
}
