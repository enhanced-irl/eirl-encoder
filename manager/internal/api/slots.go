package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/slots"
)

// --- source slots (SETUP.md §5): GET/PUT /sources/slots ------------------------------------------
//
// The app owns /etc/eirl-encoder/slots.json: it validates, writes it atomically and then sends the
// `slots` command to the engine (which reads the file at start and on that command). An engine older
// than the command answers unknown_command: the file is kept (a new engine uses it at start) and
// engineSupports is false. While streaming, an engine answers on_air if the change would move or switch
// off what is on air: then the previous file is put back and the answer is 409 on_air.

type slotView struct {
	Slot    int          `json:"slot"`
	Assign  string       `json:"assign"`
	Alias   string       `json:"alias"`
	Current *currentView `json:"current"` // the source in the slot now (null = empty, or reserved and absent)
}

type currentView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}

type candidateView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Connected bool   `json:"connected"`
}

type slotsView struct {
	Slots      []slotView      `json:"slots"`
	Candidates []candidateView `json:"candidates"`
	// EngineSupports: the running engine applies slots live (it reports slotConfig, and did not answer
	// unknown_command). False with an older engine or with the engine stopped: the file is used at start
	EngineSupports bool `json:"engineSupports"`
	// Applied: only in the PUT answer: the engine accepted the command now (additive to the contract)
	Applied *bool `json:"applied,omitempty"`
}

// slotsOff: true (and answers 503) without a slots file path.
func (s *Server) slotsOff(w http.ResponseWriter, r *http.Request) bool {
	if s.SlotsPath == "" {
		fail(w, r, 503, "slots_unavailable", "slots.unavailable")
		return true
	}
	return false
}

// mixerState: the engine state, nil if there is no mixer running.
func (s *Server) mixerState() *mixer.State {
	if s.Mixer == nil {
		return nil
	}
	st, err := s.Mixer.Read()
	if err != nil {
		if !errors.Is(err, mixer.ErrOff) {
			s.Log.Warn("source slots: mixer state", "err", err)
		}
		return nil
	}
	return st
}

// slotCount: the engine's slots (MIXER_SLOTS), or mixer.MaxSlots without a running engine.
func slotCount(st *mixer.State) int {
	if st != nil && st.Slots >= 1 && st.Slots <= mixer.MaxSlots {
		return st.Slots
	}
	return mixer.MaxSlots
}

func (s *Server) loadSlots(n int) []slots.Slot {
	list, err := slots.Load(s.SlotsPath, n)
	if err != nil {
		s.Log.Warn("slots.json: using all auto", "err", err)
	}
	return list
}

func (s *Server) slotsView(r *http.Request, list []slots.Slot, st *mixer.State) slotsView {
	ctx, lang := r.Context(), reqLang(r)
	v := slotsView{Slots: []slotView{}, Candidates: []candidateView{}, EngineSupports: st != nil && st.SlotConfig != nil}
	for _, sl := range list {
		sv := slotView{Slot: sl.Slot, Assign: sl.Assign, Alias: sl.Alias}
		if st != nil {
			for _, src := range st.Sources {
				if src.Slot == sl.Slot {
					sv.Current = &currentView{ID: src.ID, Name: sourceName(lang, src.ID, src.Name), Kind: src.Kind, State: src.State}
					break
				}
			}
		}
		v.Slots = append(v.Slots, sv)
	}
	v.Candidates = s.candidates(ctx, list, st)
	for i := range v.Candidates {
		v.Candidates[i].Name = sourceName(lang, v.Candidates[i].ID, v.Candidates[i].Name)
	}
	return v
}

// candidates: the engine's catalogue (every connected source, also those without a slot) or, with an
// older engine, the sources in the slots now; plus the pinned ids that are not connected (connected
// false), so the editor can show them.
func (s *Server) candidates(ctx context.Context, list []slots.Slot, st *mixer.State) []candidateView {
	out := []candidateView{}
	seen := map[string]bool{}
	if st != nil && st.Catalog != nil {
		for _, c := range st.Catalog {
			out = append(out, candidateView{ID: c.ID, Name: c.Name, Kind: c.Kind, Connected: true})
			seen[c.ID] = true
		}
	} else if st != nil {
		for _, src := range st.Sources {
			if !seen[src.ID] && mixer.ValidSourceID(src.ID) {
				out = append(out, candidateView{ID: src.ID, Name: src.Name, Kind: src.Kind, Connected: src.State != "gone"})
				seen[src.ID] = true
			}
		}
	}
	for _, sl := range list {
		if sl.Assign == mixer.AssignAuto || sl.Assign == mixer.AssignOff || seen[sl.Assign] {
			continue
		}
		seen[sl.Assign] = true
		out = append(out, candidateView{ID: sl.Assign, Name: s.absentName(ctx, sl.Assign), Kind: kindOf(sl.Assign)})
	}
	return out
}

// kindOf: the kind of a source from its id.
func kindOf(id string) string {
	switch {
	case id == "hdmi":
		return "hdmi-csi"
	case id == "test":
		return "test"
	case strings.HasPrefix(id, "net:"):
		return "network"
	}
	return "usb"
}

// absentName: a readable name for a pinned source that is not connected: a network camera's name, or
// the USB link without its decoration (usb-Elgato_Cam_Link_4K_0005AB12-video-index0 → Elgato Cam Link
// 4K 0005AB12).
func (s *Server) absentName(ctx context.Context, id string) string {
	switch {
	case strings.HasPrefix(id, "net:"):
		if s.Network != nil {
			for _, c := range s.Network.Status(ctx).Cameras.Items {
				if "net:"+c.ID == id {
					return c.Name
				}
			}
		}
		return strings.TrimPrefix(id, "net:")
	case strings.HasPrefix(id, "/dev/v4l/"):
		base := id[strings.LastIndex(id, "/")+1:]
		base = strings.TrimPrefix(base, "usb-")
		if i := strings.LastIndex(base, "-video-index"); i > 0 {
			base = base[:i]
		}
		return strings.ReplaceAll(base, "_", " ")
	}
	return id
}

func (s *Server) getSlots(w http.ResponseWriter, r *http.Request) {
	if s.slotsOff(w, r) {
		return
	}
	st := s.mixerState()
	writeJSON(w, 200, s.slotsView(r, s.loadSlots(slotCount(st)), st))
}

// putSlots: {"slots": [every slot]} (missing ones are auto).
func (s *Server) putSlots(w http.ResponseWriter, r *http.Request) {
	if s.slotsOff(w, r) {
		return
	}
	var body struct {
		Slots []slots.Slot `json:"slots"`
	}
	if decode(r, &body) != nil || body.Slots == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"slots": [{"slot", "assign", "alias"}, …]}`)
		return
	}
	st := s.mixerState()
	list, fe, codes := slots.Validate(body.Slots, slotCount(st))
	if fe != nil {
		writeJSON(w, 400, map[string]any{"error": "validation", "fields": fe.Render(reqLang(r)), "codes": codes})
		return
	}
	prev, prevErr := safefile.ReadOwned(s.SlotsPath, 0)
	if err := slots.Save(s.SlotsPath, list); err != nil {
		s.Log.Error("save slots.json", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
		return
	}
	// Put the previous file back: the engine refused the change and keeps the old arrangement
	rollback := func() {
		var err error
		if prevErr == nil {
			err = writeFile(s.SlotsPath, prev)
		} else if errors.Is(prevErr, os.ErrNotExist) {
			err = os.Remove(s.SlotsPath)
		}
		if err != nil {
			s.Log.Error("slots.json: could not put the previous file back", "err", err)
		}
	}
	applied := false
	supports := st != nil && st.SlotConfig != nil
	if s.Mixer != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := s.Mixer.Do(ctx, mixer.Command{Cmd: "slots", Slots: slots.Assignments(list)})
		cancel()
		var ce *mixer.CommandError
		switch {
		case err == nil:
			applied, supports = true, true
		case errors.Is(err, mixer.ErrOff):
			// Engine stopped: it reads the file when it starts
		case errors.As(err, &ce) && ce.Code == "unknown_command":
			supports = false // an older engine: the file waits for a new one
		case errors.As(err, &ce) && ce.Code == "on_air":
			rollback()
			fail(w, r, 409, "on_air", "slots.on_air")
			return
		case errors.As(err, &ce):
			rollback()
			s.Log.Error("source slots: the engine refused them", "code", ce.Code)
			fail(w, r, 422, ce.Code, "slots.rejected")
			return
		default:
			// Saved; the engine did not answer (restarting?): it reads the file when it starts
			s.Log.Warn("source slots: engine command", "err", err)
		}
	}
	s.Log.Info("source slots saved", "slots", slotsLog(list), "applied", applied, "via", via(r))
	v := s.slotsView(r, list, s.mixerState())
	v.EngineSupports, v.Applied = supports, &applied
	writeJSON(w, 200, v)
}

// writeFile: an atomic write with the slots file mode.
func writeFile(path string, b []byte) error { return config.WriteAtomic(path, b, 0o640) }

// slotsLog: "1=auto 2=off 3=net:cam1" (assignments only: aliases are the user's text).
func slotsLog(list []slots.Slot) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = strconv.Itoa(s.Slot) + "=" + s.Assign
	}
	return strings.Join(parts, " ")
}
