package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/links"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/modem"
)

// --- modems (MODEMS.md §4) -----------------------------------------------------------------------------

// getModems: the cellular modems and how they are doing (the same as /status modems[]), without whole
// identifiers: only the last 4 digits of the IMEI and the ICCID. Their configuration: putModem and postModem.
func (s *Server) getModems(w http.ResponseWriter, r *http.Request) {
	mods := s.linksSnapshot().Modems
	if mods == nil {
		mods = []modem.Modem{}
	}
	writeJSON(w, 200, map[string]any{"modems": mods})
}

// linksSnapshot: the latest round of the links service (zero without it, or before its first round).
func (s *Server) linksSnapshot() links.Snapshot {
	if s.Links == nil {
		return links.Snapshot{}
	}
	return s.Links.Current()
}

// overSRTLA: the running stream goes over SRTLA right now, which is what makes the in-use file count. The one
// rule for /status (collect) and the links service (StreamingSRTLA). output: the mode in force (with the mixer,
// the engine's, which switches hot).
func overSRTLA(running bool, output string, rs runState) bool {
	return running && output == "on" && rs.Transport == "srtla"
}

// StreamingSRTLA: overSRTLA for the links service's 2-second round. "Running" is the engine's run state being
// there instead of asking systemd (a `systemctl show` every 2 s): the engine's RuntimeDirectory only exists while
// the unit runs. The output mode is read as collect reads it.
func (s *Server) StreamingSRTLA() bool {
	rs, running := s.runState()
	output := rs.Output
	if running && rs.Mixer == "1" && s.Mixer != nil {
		if m, err := s.Mixer.Read(); err == nil && m.Output != nil {
			output = m.Output.Mode
		}
	}
	return overSRTLA(running, output, rs)
}

// modems: the modems' manager (nil without the links service).
func (s *Server) modems() *modem.Manager {
	if s.Links == nil {
		return nil
	}
	return s.Links.Modems
}

// lastLink: changing that modem now would cut the stream: it is the only link in use while streaming over SRTLA.
func (s *Server) lastLink(id string) bool {
	snap := s.linksSnapshot()
	if !snap.Streaming {
		return false
	}
	used, ours := 0, false
	for _, u := range snap.Uplinks {
		if u.InUse {
			used++
			ours = ours || u.ModemID == id
		}
	}
	return ours && used == 1
}

// modemCan: that modem, as last seen, lets that be done from here.
func (s *Server) modemCan(id, what string) bool {
	for _, md := range s.linksSnapshot().Modems {
		if md.ID == id {
			for _, c := range md.Can {
				if c == what {
					return true
				}
			}
			return false
		}
	}
	return true // not seen yet: the manager answers (modem_unknown)
}

// refreshSoon: after an order, the modems and the links again now (not at the next round): the web and the screen
// see the change within a second.
func (s *Server) refreshSoon() {
	if s.Links == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if s.Links.Modems != nil {
			s.Links.Modems.Refresh(ctx)
		}
		s.Links.Refresh(ctx)
	}()
}

// modemFail answers a modem order's error.
func (s *Server) modemFail(w http.ResponseWriter, r *http.Request, id, what string, err error) {
	switch {
	case errors.Is(err, modem.ErrUnknown):
		fail(w, r, 404, "modem_unknown", "modems.unknown")
	case errors.Is(err, modem.ErrConfig):
		fail(w, r, 400, "validation", "modems.invalid")
	case errors.Is(err, modem.ErrCannot):
		fail(w, r, 409, "not_supported", "modems.not_supported")
	case errors.Is(err, modem.ErrBusy):
		fail(w, r, 409, "busy", "modems.busy")
	case errors.Is(err, modem.ErrWrongPIN):
		fail(w, r, 400, "pin_wrong", "modems.pin_wrong")
	case errors.Is(err, modem.ErrPUK):
		fail(w, r, 409, "pin_puk", "modems.pin_puk")
	case errors.Is(err, modem.ErrNotLocked):
		fail(w, r, 409, "pin_not_needed", "modems.pin_not_needed")
	case errors.Is(err, modem.ErrNotHere):
		fail(w, r, 409, "modem_unavailable", "modems.unavailable")
	default:
		s.Log.Warn("modem order failed", "modem", id, "order", what, "err", err)
		fail(w, r, 502, "modem_failed", "modems.failed")
	}
}

// putModem: {"alias"?, "apn"? ("" = automatic), "roaming"?, "mode"? (auto | 4g | 5g), "force"?} → the modem as GET
// /modems shows it. The modem first, then modems.json. Changing APN, roaming or mode reconnects the modem: refused
// with 409 last_link if it is the only link carrying the stream, unless force.
func (s *Server) putModem(w http.ResponseWriter, r *http.Request) {
	m := s.modems()
	if m == nil {
		fail(w, r, 503, "modems_disabled", "modems.disabled")
		return
	}
	id := r.PathValue("id")
	var body struct {
		Alias   *string `json:"alias"`
		APN     *string `json:"apn"`
		Roaming *bool   `json:"roaming"`
		Mode    *string `json:"mode"`
		Force   bool    `json:"force"`
	}
	if err := decode(r, &body); err != nil || body.Alias == nil && body.APN == nil && body.Roaming == nil && body.Mode == nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"alias"?, "apn"?, "roaming"?, "mode"?}`)
		return
	}
	if !modem.ValidID(id) {
		fail(w, r, 404, "modem_unknown", "modems.unknown")
		return
	}
	switch {
	case body.Alias != nil && !modem.ValidAlias(*body.Alias):
		fieldError(w, r, "alias", "modems.bad_alias", modem.AliasMax)
		return
	case body.APN != nil && !modem.ValidAPN(*body.APN):
		fieldError(w, r, "apn", "modems.bad_apn")
		return
	case body.Mode != nil && !modem.ValidMode(*body.Mode):
		fieldError(w, r, "mode", "modems.bad_mode")
		return
	}
	reconnects := body.APN != nil || body.Roaming != nil || body.Mode != nil
	if reconnects && !body.Force && s.lastLink(id) {
		fail(w, r, 409, "last_link", "modems.last_link")
		return
	}
	md, err := m.Configure(r.Context(), id, modem.Change{Alias: body.Alias, APN: body.APN, Roaming: body.Roaming, Mode: body.Mode})
	if err != nil {
		s.modemFail(w, r, id, "configure", err)
		return
	}
	s.refreshSoon()
	s.Log.Info("modem configured", "modem", id, "alias", body.Alias != nil, "apn", body.APN != nil, "roaming", body.Roaming != nil,
		"mode", body.Mode != nil)
	writeJSON(w, 200, md)
}

// postModem: pin {"pin", "remove"?} (enter the SIM's PIN, or turn it off), reset {"force"?} and subnet {"force"?}
// (move a HiLink's LAN). 202 for reset and subnet: the modem goes away for a while. A subnet move cuts the stick it
// really reaches (its twin, for one in conflict): that one is checked for being the last link.
func (s *Server) postModem(w http.ResponseWriter, r *http.Request) {
	m := s.modems()
	if m == nil {
		fail(w, r, 503, "modems_disabled", "modems.disabled")
		return
	}
	id, action := r.PathValue("id"), r.PathValue("action")
	if !modem.ValidID(id) {
		fail(w, r, 404, "modem_unknown", "modems.unknown")
		return
	}
	var body struct {
		PIN    string `json:"pin"`
		Remove bool   `json:"remove"`
		Force  bool   `json:"force"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			fail(w, r, 400, "bad_request", "common.bad_body")
			return
		}
	}
	switch action {
	case "pin":
		if !modem.ValidPIN(body.PIN) {
			fieldError(w, r, "pin", "modems.bad_pin")
			return
		}
		if err := m.Unlock(r.Context(), id, body.PIN, body.Remove); err != nil {
			s.modemFail(w, r, id, "pin", err) // never the PIN in the log
			return
		}
		s.refreshSoon()
		s.Log.Info("modem PIN", "modem", id, "removed", body.Remove)
		w.WriteHeader(204)
	case "reset", "subnet":
		if !s.modemCan(id, action) { // what it cannot do says so before anything about the stream
			s.modemFail(w, r, id, action, modem.ErrCannot)
			return
		}
		cut := id
		if action == "subnet" {
			cut = m.SubnetTarget(id)
		}
		if !body.Force && s.lastLink(cut) {
			fail(w, r, 409, "last_link", "modems.last_link")
			return
		}
		var err error
		if action == "reset" {
			err = m.Reset(r.Context(), id)
		} else {
			err = m.MoveSubnet(r.Context(), id)
		}
		if err != nil {
			s.modemFail(w, r, id, action, err)
			return
		}
		s.refreshSoon()
		s.Log.Info("modem "+action, "modem", id)
		writeJSON(w, 202, map[string]any{"id": id, "action": action})
	default:
		fail(w, r, 404, "unknown_route", "common.unknown_route")
	}
}
