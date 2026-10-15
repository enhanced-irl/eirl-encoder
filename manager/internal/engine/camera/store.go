package camera

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// FileName is the cameras' saved values in the engine's state directory (/var/lib/eirl-engine).
const FileName = "cameras.json"

const (
	// MaxCameras: cameras remembered; past it, the one changed longest ago is forgotten
	MaxCameras = 16
	maxIDLen   = 256
	maxFile    = 64 << 10
)

// saveDelay: changes are written at most this often (a burst of taps is one write).
var saveDelay = time.Second

// Store keeps, per source id, the values the user set that differ from the camera's default (SOURCES.md §7.4).
// It is safe from any goroutine; writes are batched (Flush at the engine's stop). A nil Store keeps nothing.
type Store struct {
	path  string // "" = in memory only (no state directory)
	mu    sync.Mutex
	cams  map[string]*entry
	dirty bool
	timer *time.Timer
	bad   bool // the last write failed: logged once per streak
}

type entry struct {
	At     int64          `json:"at"` // Unix seconds of the last change
	Values map[string]int `json:"values"`
}

type storeFile struct {
	V       int               `json:"v"`
	Cameras map[string]*entry `json:"cameras"`
}

// NewStore loads the saved values (a missing or broken file: none; what does not fit the rules is dropped).
func NewStore(path string) *Store {
	s := &Store{path: path, cams: map[string]*entry{}}
	if path == "" {
		return s
	}
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			elog.Event("mixer", "camera_settings_unreadable", "path", path, "err", err)
		}
		return s
	}
	defer f.Close()
	var sf storeFile
	if err := json.NewDecoder(io.LimitReader(f, maxFile)).Decode(&sf); err != nil {
		elog.Event("mixer", "camera_settings_unreadable", "path", path, "err", err)
		return s
	}
	for id, e := range sf.Cameras {
		if e == nil || !validID(id) {
			continue
		}
		vals := map[string]int{}
		for k, v := range e.Values {
			if Known(k) && len(vals) < len(specs) {
				vals[k] = v
			}
		}
		if len(vals) > 0 {
			s.cams[id] = &entry{At: e.At, Values: vals}
		}
	}
	s.prune()
	return s
}

// validID: a source id as the engine has them (USB by-id/by-path links), without control characters.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLen || !strings.HasPrefix(id, "/dev/") {
		return false
	}
	return strings.IndexFunc(id, unicode.IsControl) < 0
}

// Values: the saved values of a camera (a copy; nil without any).
func (s *Store) Values(id string) map[string]int {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.cams[id]; e != nil {
		return maps.Clone(e.Values)
	}
	return nil
}

// Set saves a control's value for a camera: a value equal to its default is forgotten.
func (s *Store) Set(id string, c Control, v int) {
	if s == nil || !validID(id) || !Known(c.ID) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.cams[id]
	if v == c.Default {
		if e == nil {
			return
		}
		if _, ok := e.Values[c.ID]; !ok {
			return
		}
		delete(e.Values, c.ID)
		if len(e.Values) == 0 {
			delete(s.cams, id)
		}
	} else {
		if e == nil {
			e = &entry{Values: map[string]int{}}
			s.cams[id] = e
		}
		e.Values[c.ID] = v
		e.At = time.Now().Unix()
		s.prune()
	}
	s.changedLocked()
}

// Forget drops a camera's values (camera_reset).
func (s *Store) Forget(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cams[id]; ok {
		delete(s.cams, id)
		s.changedLocked()
	}
}

// prune keeps MaxCameras: the ones changed longest ago go.
func (s *Store) prune() {
	for len(s.cams) > MaxCameras {
		oldest, at := "", int64(0)
		for id, e := range s.cams {
			if oldest == "" || e.At < at || e.At == at && id < oldest {
				oldest, at = id, e.At
			}
		}
		delete(s.cams, oldest)
	}
}

func (s *Store) changedLocked() {
	s.dirty = true
	if s.path != "" && s.timer == nil {
		s.timer = time.AfterFunc(saveDelay, s.Flush)
	}
}

// Flush writes the file now if something changed (the engine's stop; the batched writes).
func (s *Store) Flush() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if !s.dirty || s.path == "" {
		return
	}
	b, err := json.Marshal(storeFile{V: 1, Cameras: s.cams})
	if err == nil {
		err = sysx.WriteAtomic(s.path, b, 0o644)
	}
	if err != nil {
		if !s.bad {
			elog.Event("mixer", "camera_settings_save_failed", "path", s.path, "err", err)
		}
		s.bad = true
		return
	}
	s.dirty, s.bad = false, false
}
