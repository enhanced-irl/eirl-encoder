// Package slots keeps the source slots of the mixer (SETUP.md §5): what each of the N slots takes (auto,
// off or one pinned source) and its alias, in /etc/eirl-encoder/slots.json.
//
// The app owns the file: it validates, writes it atomically and then sends {"cmd":"slots"} to the engine,
// which reads it at start and on that command (it does not watch the file). The rules are the engine's
// (ENGINE_GO.md §12): a source pinned to one slot at most, aliases of 0-24 letters, digits and a few
// signs.
package slots

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/device"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// Slot is one slot of slots.json.
type Slot struct {
	Slot   int    `json:"slot"`   // 1…N
	Assign string `json:"assign"` // auto | off | a source id exactly as the engine reports it
	Alias  string `json:"alias"`  // 0-24 runes; "" = the UIs show the device's name
}

// File is slots.json.
type File struct {
	V     int    `json:"v"`
	Slots []Slot `json:"slots"`
}

// Validation codes of PUT /sources/slots (next to the translated field messages).
const (
	CodeInvalid   = "invalid"
	CodeDuplicate = "duplicate"
)

// ErrInvalidFile: slots.json exists but does not follow the rules (the engine then uses all auto).
var ErrInvalidFile = errors.New("slots.json: invalid")

// Defaults: n slots on auto, without aliases.
func Defaults(n int) []Slot {
	out := make([]Slot, n)
	for i := range out {
		out[i] = Slot{Slot: i + 1, Assign: mixer.AssignAuto}
	}
	return out
}

// Validate normalises a list for n slots (1 ≤ n ≤ mixer.MaxSlots): aliases trimmed and with plain
// apostrophes (device.NormalizeText), missing slots on
// auto, sorted by slot. Errors carry API field paths (slots.<slot>.assign, slots.<slot>.alias, or slots
// for the numbering) with an i18n message each, and the same paths with a code (invalid | duplicate).
func Validate(list []Slot, n int) ([]Slot, config.FieldErrors, map[string]string) {
	fields, codes := config.FieldErrors{}, map[string]string{}
	bad := func(path, code, key string, args ...any) {
		if _, dup := fields[path]; !dup {
			fields[path], codes[path] = i18n.Ref(key, args...), code
		}
	}
	out := Defaults(n)
	seen := map[int]bool{}
	pinned := map[string]int{} // source id → slot it is pinned to
	for _, s := range list {
		if s.Slot < 1 || s.Slot > n || seen[s.Slot] {
			bad("slots", CodeInvalid, "slots.bad_slot", n)
			continue
		}
		seen[s.Slot] = true
		path := "slots." + strconv.Itoa(s.Slot)
		alias := device.NormalizeText(s.Alias)
		if !mixer.ValidAlias(alias) {
			bad(path+".alias", CodeInvalid, "slots.bad_alias", mixer.AliasMax)
		}
		if !mixer.ValidAssign(s.Assign) {
			bad(path+".assign", CodeInvalid, "slots.bad_assign")
		}
		out[s.Slot-1] = Slot{Slot: s.Slot, Assign: s.Assign, Alias: alias}
	}
	// Duplicates in slot order: the later slot gets the error and names the first one
	for _, s := range out {
		if s.Assign == mixer.AssignAuto || s.Assign == mixer.AssignOff || !mixer.ValidAssign(s.Assign) {
			continue
		}
		if first, dup := pinned[s.Assign]; dup {
			bad("slots."+strconv.Itoa(s.Slot)+".assign", CodeDuplicate, "slots.duplicate", first)
			continue
		}
		pinned[s.Assign] = s.Slot
	}
	if len(fields) > 0 {
		return nil, fields, codes
	}
	return out, nil, nil
}

// Load reads slots.json for n slots: missing = all auto. A file that breaks the rules gives all auto
// and ErrInvalidFile (the engine does the same: event slots_config_invalid). Slots beyond n are ignored.
func Load(path string, n int) ([]Slot, error) {
	b, err := safefile.ReadOwned(path, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(n), nil
	}
	if err != nil {
		return Defaults(n), err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil || f.V != 1 || f.Slots == nil {
		return Defaults(n), ErrInvalidFile
	}
	out := Defaults(n)
	seen, pinned := map[int]bool{}, map[string]bool{}
	for _, s := range f.Slots {
		if s.Slot < 1 || s.Slot > mixer.MaxSlots || seen[s.Slot] || !mixer.ValidAssign(s.Assign) || !mixer.ValidAlias(s.Alias) {
			return Defaults(n), ErrInvalidFile
		}
		seen[s.Slot] = true
		if s.Assign != mixer.AssignAuto && s.Assign != mixer.AssignOff {
			if pinned[s.Assign] {
				return Defaults(n), ErrInvalidFile
			}
			pinned[s.Assign] = true
		}
		if s.Slot <= n {
			out[s.Slot-1] = s
		}
	}
	return out, nil
}

// Save writes slots.json atomically (0640: the engine reads it through the eirl-encoder group).
func Save(path string, list []Slot) error {
	list = slices.Clone(list)
	slices.SortFunc(list, func(a, b Slot) int { return a.Slot - b.Slot })
	b, err := json.MarshalIndent(File{V: 1, Slots: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("slots.json: %w", err)
	}
	return config.WriteAtomic(path, append(b, '\n'), 0o640)
}

// Assignments: the list as the engine command carries it.
func Assignments(list []Slot) []mixer.SlotAssign {
	out := make([]mixer.SlotAssign, len(list))
	for i, s := range list {
		out[i] = mixer.SlotAssign{Slot: s.Slot, Assign: s.Assign, Alias: s.Alias}
	}
	return out
}
