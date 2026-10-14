package scene

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// FileName is the scene file inside the engine's state directory.
const FileName = "mixer-scene.json"

// Extra holds the fields of mixer-scene.json that belong to later phases (audio), kept verbatim so a
// load/save round trip does not lose them. Nil = absent; JSON null is kept as `null`.
type Extra struct {
	Audio         json.RawMessage
	ConfigAudio   json.RawMessage
	AudioSettings json.RawMessage
}

// Saved is what Load recovers from mixer-scene.json.
type Saved struct {
	Scene  Scene
	Locked bool
	// Restored: the file's configMain matched, so its whole scene was applied (Python logs
	// "escena recuperada"); otherwise only pip and pause were.
	Restored bool
	Extra    Extra
}

// file is mixer-scene.json as written: the scene keys in the Python order, then configMain and
// locked, then the audio keys. No other keys (the Python engine, retired on Oct 14, read this file too).
type file struct {
	Snapshot
	ConfigMain    string          `json:"configMain"`
	Locked        bool            `json:"locked"`
	Audio         json.RawMessage `json:"audio,omitempty"`
	ConfigAudio   json.RawMessage `json:"configAudio,omitempty"`
	AudioSettings json.RawMessage `json:"audioSettings,omitempty"`
}

// Load reads the scene saved at path for the engine started with main source configMain. The saved
// scene holds while the configured main source (the web's "Input" card) does not change; if it
// changed, the new one rules and only pip, pause and locked (which do not depend on the source)
// are kept. A missing, unreadable or invalid file gives the defaults; so does a top-level value
// that is not an object (the Python engine crashes on a list). An empty path means no persistence.
func Load(path, configMain string) Saved {
	sv := Saved{Scene: New(configMain)}
	if path == "" {
		return sv
	}
	data, err := os.ReadFile(path)
	// Python decodes the file as strict UTF-8 and rejects it as a whole; Go would substitute U+FFFD
	if err != nil || !utf8.Valid(data) {
		return sv
	}
	var d map[string]any
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &d) != nil || d == nil || json.Unmarshal(data, &raw) != nil {
		return sv
	}
	if cm, ok := d["configMain"].(string); ok && cm == configMain {
		sv.Scene.LoadMap(d)
		sv.Restored = true
	} else {
		sv.Scene.LoadMap(map[string]any{"pip": d["pip"], "pause": d["pause"]})
	}
	if l, ok := d["locked"].(bool); ok {
		sv.Locked = l
	}
	sv.Extra = Extra{Audio: raw["audio"], ConfigAudio: raw["configAudio"], AudioSettings: raw["audioSettings"]}
	return sv
}

// Save writes the scene to path atomically (mkdir -p, write path.tmp, rename): whoever reads it never
// sees half a file. An empty path is a no-op.
func Save(path, configMain string, s *Scene, locked bool, extra Extra) error {
	if path == "" {
		return nil
	}
	data, err := Marshal(configMain, s, locked, extra)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Marshal is the content of mixer-scene.json, byte for byte what Python's json.dumps writes: ", "
// and ": " separators and non-ASCII escaped as \uXXXX.
func Marshal(configMain string, s *Scene, locked bool, extra Extra) ([]byte, error) {
	f := file{
		Snapshot:      s.Snapshot(),
		ConfigMain:    configMain,
		Locked:        locked,
		Audio:         extra.Audio,
		ConfigAudio:   extra.ConfigAudio,
		AudioSettings: extra.AudioSettings,
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // Python leaves < > & alone
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("scene: %w", err)
	}
	return pythonStyle(bytes.TrimSuffix(b.Bytes(), []byte("\n"))), nil
}

// pythonStyle turns compact JSON into json.dumps' default form. Outside strings only structure can
// appear, so every ',' and ':' there is a separator; inside strings non-ASCII becomes \uXXXX
// (surrogate pairs above the BMP), lowercase as Python writes it. A DEL right after a backslash
// cannot happen: Go never escapes it.
func pythonStyle(compact []byte) []byte {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(compact)+len(compact)/4)
	inString, escaped := false, false
	for i := 0; i < len(compact); {
		c := compact[i]
		if !inString {
			out = append(out, c)
			switch c {
			case '"':
				inString = true
			case ',', ':':
				out = append(out, ' ')
			}
			i++
			continue
		}
		if c == 0x7f { // Python escapes everything outside ' '…'~'; Go leaves DEL raw
			out = append(out, `\u007f`...)
			i++
			continue
		}
		if c < utf8.RuneSelf {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(compact[i:])
		i += size
		units := []rune{r}
		if r > 0xffff {
			r -= 0x10000
			units = []rune{0xd800 + r>>10, 0xdc00 + r&0x3ff}
		}
		for _, u := range units {
			out = append(out, '\\', 'u', hex[u>>12&0xf], hex[u>>8&0xf], hex[u>>4&0xf], hex[u&0xf])
		}
	}
	return out
}
