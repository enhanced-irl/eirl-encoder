package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// The slots 1-4 as the user arranges them (contract agreed with the app, 14-oct): each slot takes any
// source (auto), none (off) or one pinned source, and has an optional alias. The app writes
// <dir of EIRL_CONF>/slots.json atomically and then sends {"cmd":"slots"}; the engine reads the file as
// data at start and never writes it.
//
//	{"v":1,"slots":[{"slot":1,"assign":"auto","alias":"Main camera"},{"slot":3,"assign":"net:cam1","alias":"Phone"}]}

// SlotConfig is one slot's arrangement.
type SlotConfig struct {
	Slot   int    `json:"slot"`   // 1-based
	Assign string `json:"assign"` // auto | off | a source id, exactly as in sources[].id
	Alias  string `json:"alias"`  // 0-24 characters; "" = the device's name
}

const (
	AssignAuto = "auto"
	AssignOff  = "off"
	aliasMax   = 24
	slotsFile  = "slots.json"
)

var reSlotSource = regexp.MustCompile(`^(hdmi|test|/dev/v4l/by-(id|path)/[A-Za-z0-9_:.-]{1,200}|net:cam[1-8])$`)

var errSlots = errors.New("invalid slot configuration")

func validAlias(a string) bool {
	if !utf8.ValidString(a) || utf8.RuneCountInString(a) > aliasMax {
		return false
	}
	for _, r := range a {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -_.'&()#+/", r) {
			return false
		}
	}
	return true
}

// defaultSlots is every slot on auto, without alias.
func defaultSlots(n int) []SlotConfig {
	c := make([]SlotConfig, n)
	for i := range c {
		c[i] = SlotConfig{Slot: i + 1, Assign: AssignAuto}
	}
	return c
}

// parseSlots validates a decoded "slots" list (from the file or the socket; numbers may be float64 or
// json.Number) into n slots: entries beyond n are ignored, missing ones are auto. A repeated or out of
// range slot, a repeated pinned source, a bad assign or a bad alias make the whole list invalid.
func parseSlots(list []any, n int) ([]SlotConfig, error) {
	out := defaultSlots(n)
	seenSlot := map[int]bool{}
	seenID := map[string]bool{}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errSlots
		}
		slot, ok := intOf(m["slot"])
		if !ok || slot < 1 || slot > 4 || seenSlot[slot] {
			return nil, errSlots
		}
		seenSlot[slot] = true
		assign, ok := m["assign"].(string)
		if !ok || (assign != AssignAuto && assign != AssignOff && !reSlotSource.MatchString(assign)) {
			return nil, errSlots
		}
		if assign != AssignAuto && assign != AssignOff {
			if seenID[assign] {
				return nil, errSlots
			}
			seenID[assign] = true
		}
		alias := ""
		if a, present := m["alias"]; present {
			if alias, ok = a.(string); !ok || !validAlias(alias) {
				return nil, errSlots
			}
		}
		if slot <= n {
			out[slot-1] = SlotConfig{Slot: slot, Assign: assign, Alias: alias}
		}
	}
	return out, nil
}

func intOf(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// loadSlots reads slots.json at start: missing = all auto; unreadable or invalid = all auto and
// event=slots_config_invalid.
func (e *Engine) loadSlots() {
	e.slotCfg = defaultSlots(e.nslots)
	if e.cfg.NetConfDir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(e.cfg.NetConfDir, slotsFile))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var f map[string]any
	if err == nil {
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		err = d.Decode(&f)
	}
	var cfg []SlotConfig
	if err == nil {
		list, ok := f["slots"].([]any)
		if v, present := f["v"]; !ok || (present && fmt.Sprint(v) != "1") {
			err = errSlots
		} else {
			cfg, err = parseSlots(list, e.nslots)
		}
	}
	if err != nil {
		elog.Event("mixer", "slots_config_invalid", "file", slotsFile)
		return
	}
	e.slotCfg = cfg
}

// pinnedSlot is the slot a source id is pinned to.
func (e *Engine) pinnedSlot(id string) (int, bool) {
	for i, c := range e.slotCfg {
		if c.Assign == id {
			return i, true
		}
	}
	return -1, false
}

// belongs: may source id sit in slot i under the configuration?
func (e *Engine) belongs(i int, id string) bool {
	c := e.slotCfg[i]
	if c.Assign == id {
		return true
	}
	if c.Assign != AssignAuto {
		return false
	}
	_, pinned := e.pinnedSlot(id)
	return !pinned
}

// onAirIDs are the sources that are seen: the main one and, in PiP or side by side, the secondary.
func (e *Engine) onAirIDs() []string {
	ids := []string{e.scene.Main}
	if e.scene.Layout != "full" && e.scene.Secondary != "" {
		ids = append(ids, e.scene.Secondary)
	}
	return ids
}

// displaces: would the configuration cfg move or switch off source id from where it is now? Moving a
// source reopens its capture: ~2 s of slate in its place.
func (e *Engine) displaces(cfg []SlotConfig, id string) bool {
	for i, s := range e.sources {
		if s == nil || s.id != id {
			continue
		}
		c := cfg[i]
		switch c.Assign {
		case id:
			return false
		case AssignAuto:
			for _, o := range cfg {
				if o.Assign == id {
					return true // pinned elsewhere
				}
			}
			return false
		default:
			return true // off, or pinned to another source
		}
	}
	return false
}

// setSlots is the `slots` command: validated like the file; while emitting, never moving what is on air
// (on_air, like hiding the main source).
func (e *Engine) setSlots(msg map[string]any) (bool, string) {
	list, ok := msg["slots"].([]any)
	if !ok {
		return false, "bad_request"
	}
	cfg, err := parseSlots(list, e.nslots)
	if err != nil {
		return false, "bad_request"
	}
	if e.emitting() {
		for _, id := range e.onAirIDs() {
			if id != "" && e.displaces(cfg, id) {
				return false, "on_air"
			}
		}
	}
	same, moves := true, false
	for i := range cfg {
		if cfg[i] != e.slotCfg[i] {
			same = false
		}
		if cfg[i].Assign != e.slotCfg[i].Assign {
			moves = true
		}
	}
	if same {
		return true, ""
	}
	e.slotCfg = cfg
	e.logCommand(msg)
	if moves {
		e.reassign()
	}
	e.publish()
	return true, ""
}

// reassign takes out of their slots the sources that no longer belong there and places everything
// again from the last catalogue.
func (e *Engine) reassign() {
	retired := map[int]*Source{}
	byID := map[string]*Source{}
	for i, s := range e.sources {
		if s == nil || e.belongs(i, s.id) {
			continue
		}
		elog.Event("mixer", "source_moved_out", "source", s.name, "slot", i+1)
		s.stop()
		retired[i], byID[s.id] = s, s
		e.sources[i] = nil
		e.bridges[i].PushStill(e.black)
	}
	e.place(e.lastCat, retired, byID)
	if e.fading == nil {
		e.applyScene()
	}
	e.changed()
}

// slotStates is slotConfig in the state: every slot, with whether a source is in it now.
func (e *Engine) slotStates() []SlotState {
	out := make([]SlotState, len(e.slotCfg))
	for i, c := range e.slotCfg {
		out[i] = SlotState{SlotConfig: c, Present: e.sources[i] != nil}
	}
	return out
}

// catalogState is the last good catalogue, also the sources without a slot (the app's slot editor).
func (e *Engine) catalogState() []CatalogEntry {
	out := []CatalogEntry{}
	for _, w := range e.lastCat.Sources {
		out = append(out, CatalogEntry{ID: w.ID, Name: w.Name, Kind: w.Kind})
	}
	return out
}
