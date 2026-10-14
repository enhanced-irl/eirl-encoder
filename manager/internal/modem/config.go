package modem

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sync"
	"unicode/utf8"
)

// Config: what the user chose for one modem (MODEMS.md §7), by its stable id. Kept by the app in
// /etc/eirl-encoder/modems.json; never secrets (the PIN lives only in NetworkManager's profile, root).
type Config struct {
	Alias string `json:"alias,omitempty"` // the name on the screen and the web (≤ AliasMax characters)
	// APN: "" = automatic (the operator's, from mobile-broadband-provider-info; else the network's default)
	APN string `json:"apn,omitempty"`
	// Roaming: data roaming allowed. Off by default (D-M8): roaming without it is a red dot, «roaming is off»
	Roaming bool `json:"roaming,omitempty"`
	// Mode: auto | 4g | 5g ("" = auto)
	Mode string `json:"mode,omitempty"`
}

// AliasMax: the alias's length in characters (it shares the header with up to five others).
const AliasMax = 12

// Modes: what Mode can be.
var Modes = []string{"auto", "4g", "5g"}

var (
	// ErrConfig: a field that does not fit (the API answers 400 with it)
	ErrConfig = errors.New("modem: invalid configuration")
	// An APN is DNS-like labels (3GPP TS 23.003 §9): letters, digits, hyphens, dots
	reAPN = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,61}[A-Za-z0-9])?$`)
)

// ValidAlias: printable, no control characters, ≤ AliasMax characters ("" = none).
func ValidAlias(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > AliasMax {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

// ValidAPN: "" (automatic) or an APN.
func ValidAPN(s string) bool { return s == "" || reAPN.MatchString(s) }

// ValidMode: "" or one of Modes.
func ValidMode(s string) bool {
	if s == "" {
		return true
	}
	for _, m := range Modes {
		if m == s {
			return true
		}
	}
	return false
}

// Valid: every field fits.
func (c Config) Valid() bool { return ValidAlias(c.Alias) && ValidAPN(c.APN) && ValidMode(c.Mode) }

// Store: modems.json. Read and written whole, one change at a time.
type Store struct {
	Path  string
	Read  func(path string) ([]byte, error)                      // nil = os.ReadFile
	Write func(path string, data []byte, mode os.FileMode) error // atomic (config.WriteAtomic)

	mu sync.Mutex
}

type storeFile struct {
	Modems map[string]Config `json:"modems"`
}

// MaxStored: configurations kept (a modem not seen for a long time keeps its own; this only bounds the file).
const MaxStored = 32

// All: every stored configuration (empty when there is no file).
func (s *Store) All() (map[string]Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() (map[string]Config, error) {
	read := s.Read
	if read == nil {
		read = os.ReadFile
	}
	b, err := read(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return map[string]Config{}, nil // a broken file is no configuration (it is rewritten at the next change)
	}
	out := map[string]Config{}
	for id, c := range f.Modems {
		if reID.MatchString(id) && c.Valid() {
			out[id] = c
		}
	}
	return out, nil
}

// Get: one modem's configuration (zero = defaults).
func (s *Store) Get(id string) Config {
	all, _ := s.All()
	return all[id]
}

// Update changes one modem's configuration with fn (on top of what the file holds) and saves it.
func (s *Store) Update(id string, fn func(*Config)) (Config, error) {
	if !reID.MatchString(id) {
		return Config{}, ErrConfig
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Config{}, err
	}
	c := all[id]
	fn(&c)
	if !c.Valid() {
		return Config{}, ErrConfig
	}
	if c == (Config{}) {
		delete(all, id)
	} else {
		all[id] = c
	}
	if len(all) > MaxStored {
		return Config{}, ErrConfig
	}
	b, err := json.MarshalIndent(storeFile{Modems: all}, "", "  ")
	if err != nil {
		return Config{}, err
	}
	return c, s.Write(s.Path, append(b, '\n'), 0o640)
}

// reID: a modem id as stableID makes it.
var reID = regexp.MustCompile(`^[mh]-[0-9a-f]{12}$`)

// ValidID: the shape of a modem id.
func ValidID(id string) bool { return reID.MatchString(id) }
