// Package device keeps the device settings of eirl-encoderd (SETUP.md §2): language, display name and
// first-boot setup state in /etc/eirl-encoder/device.json, the setup code (§2.1) and, through the
// System interface, what lives in the operating system: time zone, automatic time (NTP), the clock
// and the hostname.
//
// device.json is owned by the app and written atomically (temporary file + rename, 0640 like the rest of
// the setgid /etc/eirl-encoder: the group reads it, only the app writes it). Time zone, NTP and the clock are never stored here: they are read from
// and written to the system.
package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// Settings is device.json.
type Settings struct {
	V        int    `json:"v"`
	Language string `json:"language"` // es | en | fr
	Name     string `json:"name"`     // display name (ValidName)
	Setup    Setup  `json:"setup"`
}

// Setup is the first-boot wizard state.
type Setup struct {
	Done        bool       `json:"done"`
	CompletedAt *time.Time `json:"completedAt"`
	// Skipped: the step ids the user skipped
	Skipped []string `json:"skipped"`
	// Completed: the step ids marked done (additive to the contract: the wizard resumes where it was
	// after a restart)
	Completed []string `json:"completed"`
}

// Step ids, in the wizard's order on the touch screen. The web asks for the password right after the
// language (SETUP.md §4): before it, the web may change nothing but the language.
const (
	StepLanguage = "language"
	StepTime     = "time"
	StepNetwork  = "network"
	StepPassword = "password"
	StepName     = "name"
	StepCloud    = "cloud"
)

// Steps lists the step ids in the screen's order.
var Steps = []string{StepLanguage, StepTime, StepNetwork, StepPassword, StepName, StepCloud}

const (
	// DefaultName: the display name of a fresh device
	DefaultName = "EIRL Encoder"
	// NameMax: runes of a display name
	NameMax = 40
	// HostnameMax: characters of the derived hostname
	HostnameMax = 40
	// FileMode of device.json: the group (eirl-encoder, from the setgid directory) may read it, like
	// engine.env and admin.hash
	FileMode os.FileMode = 0o640
)

// apostrophes: what phone keyboards type for ' (smart punctuation): right and left single quotes, the
// modifier letter apostrophe and the reversed one.
var apostrophes = strings.NewReplacer("\u2019", "'", "\u2018", "'", "\u02BC", "'", "\u201B", "'")

// NormalizeText: user-typed text (a device name, a source alias) trimmed and with typographic apostrophes as
// a plain ' (the one the rules allow), so "Plató d’Ana" from a phone is accepted and stored as "Plató d'Ana".
// Typographic double quotes are left alone: no rule allows " either.
func NormalizeText(s string) string { return strings.TrimSpace(apostrophes.Replace(s)) }

// ValidName: 1-NameMax runes after trimming, each a Unicode letter or digit, a space or one of -_.'
func ValidName(n string) bool {
	n = strings.TrimSpace(n)
	if n == "" || !utf8.ValidString(n) || utf8.RuneCountInString(n) > NameMax {
		return false
	}
	for _, r := range n {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -_.'", r) {
			return false
		}
	}
	return true
}

// translit: the Latin letters with diacritics that a name in es/en/fr usually carries, to ASCII.
var translit = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'ā': "a",
	'ç': "c", 'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'ē': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ñ': "n",
	'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ø': "o",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y",
	'ß': "ss", 'æ': "ae", 'œ': "oe",
}

// Hostname derives the hostname from a display name: lowercase ASCII [a-z0-9-], 1-HostnameMax
// characters, with "eirl-" in front if it does not start with a letter ("Estudio 2" → estudio-2,
// "2º plató" → eirl-2-plato). An empty result is "eirl-encoder".
func Hostname(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case translit[r] != "":
			b.WriteString(translit[r])
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = true
		}
	}
	h := strings.Trim(b.String(), "-")
	if h == "" {
		return "eirl-encoder"
	}
	if h[0] < 'a' || h[0] > 'z' {
		h = "eirl-" + h
	}
	if len(h) > HostnameMax {
		h = strings.TrimRight(h[:HostnameMax], "-")
	}
	return h
}

// Defaults: a fresh device (the wizard has not run).
func Defaults() Settings {
	return Settings{V: 1, Language: i18n.EN, Name: DefaultName, Setup: Setup{Skipped: []string{}, Completed: []string{}}}
}

// Validate checks a whole settings value (from the file or before saving).
func (s Settings) Validate() error {
	switch {
	case s.V != 1:
		return errors.New("device.json: v must be 1")
	case !i18n.Supported(s.Language):
		return fmt.Errorf("device.json: unknown language %q", s.Language)
	case !ValidName(s.Name):
		return errors.New("device.json: invalid name")
	}
	for _, list := range [][]string{s.Setup.Skipped, s.Setup.Completed} {
		for _, id := range list {
			if !slices.Contains(Steps, id) {
				return fmt.Errorf("device.json: unknown step %q", id)
			}
		}
	}
	return nil
}

// Origin says how Open got the settings.
type Origin string

const (
	Loaded   Origin = "loaded"   // device.json was there and valid
	Migrated Origin = "migrated" // no device.json but an admin password: a board set up before the wizard
	Fresh    Origin = "fresh"    // neither: the wizard runs
	Invalid  Origin = "invalid"  // device.json unreadable or invalid: the migration rule in memory, not written
)

// Store serialises reads and writes of device.json.
type Store struct {
	Path string

	mu  sync.Mutex
	cur Settings
}

// Open loads device.json or, when it is missing, applies the migration rule and writes it: with an
// admin password (a board configured before the wizard existed) → Spanish, setup done, name = the
// hostname; without one → a fresh device in English with the wizard to run. An invalid file is
// reported (Invalid) and left alone: the same rule applies in memory until the next save.
func Open(path string, passwordExists bool, hostname string, now time.Time) (*Store, Origin, error) {
	st := &Store{Path: path}
	b, err := safefile.ReadOwned(path, 0) // not the app's file: as if missing
	switch {
	case err == nil:
		var s Settings
		if derr := json.Unmarshal(b, &s); derr == nil {
			s.normalize()
			if verr := s.Validate(); verr == nil {
				st.cur = s
				return st, Loaded, nil
			} else {
				err = verr
			}
		} else {
			err = fmt.Errorf("device.json: %w", derr)
		}
		st.cur = migration(passwordExists, hostname, now)
		return st, Invalid, err
	case !errors.Is(err, os.ErrNotExist):
		st.cur = migration(passwordExists, hostname, now)
		return st, Invalid, err
	}
	st.cur = migration(passwordExists, hostname, now)
	origin := Fresh
	if passwordExists {
		origin = Migrated
	}
	if err := st.write(st.cur); err != nil {
		return st, origin, err
	}
	return st, origin, nil
}

func migration(passwordExists bool, hostname string, now time.Time) Settings {
	s := Defaults()
	if passwordExists {
		s.Language = i18n.ES
		if ValidName(hostname) {
			s.Name = hostname
		}
		at := now.UTC().Truncate(time.Second)
		s.Setup.Done, s.Setup.CompletedAt = true, &at
	}
	return s
}

// normalize: nil lists as empty ones (the file shape is stable).
func (s *Settings) normalize() {
	if s.Setup.Skipped == nil {
		s.Setup.Skipped = []string{}
	}
	if s.Setup.Completed == nil {
		s.Setup.Completed = []string{}
	}
}

// Get returns a copy of the current settings.
func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur.clone()
}

// Language: the device language (es | en | fr).
func (st *Store) Language() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur.Language
}

// Update applies fn on a copy of the current settings, validates and writes the result atomically.
// If fn or the write fails, nothing changes.
func (st *Store) Update(fn func(*Settings) error) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := st.cur.clone()
	if err := fn(&next); err != nil {
		return st.cur.clone(), err
	}
	next.normalize()
	if err := next.Validate(); err != nil {
		return st.cur.clone(), err
	}
	if err := st.write(next); err != nil {
		return st.cur.clone(), err
	}
	st.cur = next
	return next.clone(), nil
}

func (st *Store) write(s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteAtomic(st.Path, append(b, '\n'), FileMode)
}

func (s Settings) clone() Settings {
	c := s
	c.Setup.Skipped = slices.Clone(s.Setup.Skipped)
	c.Setup.Completed = slices.Clone(s.Setup.Completed)
	if s.Setup.CompletedAt != nil {
		t := *s.Setup.CompletedAt
		c.Setup.CompletedAt = &t
	}
	return c
}

// StepState: todo | done | skipped of one step. The password step is done whenever an admin password
// exists; with the setup done, every step not skipped counts as done (migrated boards).
func (s Settings) StepState(id string, passwordExists bool) string {
	switch {
	case slices.Contains(s.Setup.Skipped, id):
		return "skipped"
	case id == StepPassword && passwordExists:
		return "done"
	case s.Setup.Done, slices.Contains(s.Setup.Completed, id):
		return "done"
	}
	return "todo"
}

// MarkStep records a step as done or skipped (removing the other mark).
func (s *Settings) MarkStep(id, state string) {
	s.Setup.Skipped = slices.DeleteFunc(s.Setup.Skipped, func(x string) bool { return x == id })
	s.Setup.Completed = slices.DeleteFunc(s.Setup.Completed, func(x string) bool { return x == id })
	if state == "skipped" {
		s.Setup.Skipped = append(s.Setup.Skipped, id)
	} else {
		s.Setup.Completed = append(s.Setup.Completed, id)
	}
}
