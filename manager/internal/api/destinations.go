package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// --- saved destinations (OUTPUTS.md §9; MANAGER.md §3, "Destination") --------------------------------------------
//
// Several destinations per device, one active: what engine.env holds. Activating or editing the active one renders
// engine.env (config.Store.UpdateDestinations) and, with apply, applies it (applyAfterSave: hot through the engine's
// output_config when only the output changed). The cloud's destination (id eirl) can be activated here, never
// edited or deleted.

var (
	errManaged  = i18n.E("destination.managed")
	errNotFound = i18n.E("destination.not_found")
	errActive   = i18n.E("destination.active")
	errTooMany  = i18n.E("destination.too_many", config.MaxLocalDestinations)
)

// destinationsView: GET /destinations and the answer of POST /destinations/{id}/activate.
type destinationsView struct {
	Active         *string                  `json:"active"`
	ManagedBy      string                   `json:"managedBy"`
	Destinations   []config.DestinationView `json:"destinations"`
	Templates      []config.Template        `json:"templates"`
	PendingRestart bool                     `json:"pendingRestart"`
	Restarted      bool                     `json:"restarted,omitempty"`
}

// oneDestinationView: the answer of POST /destinations and PUT /destinations/{id}.
type oneDestinationView struct {
	config.DestinationView
	PendingRestart bool `json:"pendingRestart"`
	Restarted      bool `json:"restarted,omitempty"`
}

func (s *Server) pendingNow(r *http.Request) bool {
	st, err := s.Unit.Status(r.Context())
	return err == nil && s.pending(st.State)
}

func (s *Server) listView(r *http.Request, ds config.Destinations) destinationsView {
	v := destinationsView{ManagedBy: ds.ManagedBy(), Templates: config.Templates, PendingRestart: s.pendingNow(r),
		Destinations: make([]config.DestinationView, 0, len(ds.Destinations))}
	if ds.Active != "" {
		a := ds.Active
		v.Active = &a
	}
	for _, d := range ds.Destinations {
		v.Destinations = append(v.Destinations, d.View(d.ID == ds.Active))
	}
	return v
}

func (s *Server) getDestinations(w http.ResponseWriter, r *http.Request) {
	ds, _, err := s.Config.Destinations()
	if err != nil {
		s.Log.Error("saved destinations", "err", err)
		fail(w, r, 500, "internal", "config.read_failed")
		return
	}
	writeJSON(w, 200, s.listView(r, ds))
}

// postDestination: a new destination from a template and a protocol (not activated).
func (s *Server) postDestination(w http.ResponseWriter, r *http.Request) {
	var p config.DestinationPatch
	if decode(r, &p) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	var t config.Template
	switch ok := false; {
	case p.Template == nil:
		fieldError(w, r, "template", "config.one_of", templateList())
		return
	case p.Protocol == nil:
		fieldError(w, r, "protocol", "config.one_of", strings.Join(config.Protocols, ", "))
		return
	default:
		if t, ok = config.TemplateByID(*p.Template); !ok {
			fieldError(w, r, "template", "config.one_of", templateList())
			return
		}
	}
	if t.ID == config.TemplateEIRL { // only the cloud creates it
		failErr(w, r, 409, "managed", errManaged)
		return
	}
	var created config.Destination
	ds, _, _, ok := s.commitDestinations(w, r, false, func(ds *config.Destinations, _ *config.Settings) error {
		if ds.LocalCount() >= config.MaxLocalDestinations {
			return errTooMany
		}
		base := config.NewDestination(t, *p.Protocol)
		base.ID = ds.NewID()
		d, err := p.ApplyTo(base)
		if err != nil {
			return err
		}
		ds.Put(d)
		created = d
		return nil
	})
	if ok {
		writeJSON(w, 201, oneDestinationView{DestinationView: created.View(created.ID == ds.Active), PendingRestart: s.pendingNow(r)})
	}
}

// putDestinationByID: changes one destination; if it is the active one, engine.env with it (and apply).
func (s *Server) putDestinationByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p config.DestinationPatch
	if decode(r, &p) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	var updated config.Destination
	ds, _, restarted, ok := s.commitDestinations(w, r, p.Apply, func(ds *config.Destinations, _ *config.Settings) error {
		cur, found := ds.Get(id)
		if !found {
			return errNotFound
		}
		d, err := patchDestination(p, cur)
		if err != nil {
			return err
		}
		ds.Put(d)
		updated = d
		return nil
	})
	if ok {
		writeJSON(w, 200, oneDestinationView{DestinationView: updated.View(id == ds.Active), PendingRestart: s.pendingNow(r),
			Restarted: restarted})
	}
}

// patchDestination: the patch on a destination of the device (never the cloud's, nor to become one).
func patchDestination(p config.DestinationPatch, cur config.Destination) (config.Destination, error) {
	if cur.Managed {
		return cur, errManaged
	}
	if p.Template != nil && *p.Template == config.TemplateEIRL {
		return cur, config.FieldErrors{"template": i18n.Ref("config.template_cloud")}
	}
	return p.ApplyTo(cur)
}

func (s *Server) deleteDestination(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, _, _, ok := s.commitDestinations(w, r, false, func(ds *config.Destinations, _ *config.Settings) error {
		cur, found := ds.Get(id)
		switch {
		case !found:
			return errNotFound
		case cur.Managed:
			return errManaged
		case ds.Active == id:
			return errActive
		}
		ds.Remove(id)
		return nil
	})
	if ok {
		w.WriteHeader(204)
	}
}

// activateDestination: {"apply": bool} makes it the active one (engine.env with it). While on air, applying it
// restarts the output (the screen and the web confirm first).
func (s *Server) activateDestination(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Apply bool `json:"apply"`
	}
	if decode(r, &body) != nil {
		fail(w, r, 400, "bad_request", "common.bad_body_shape", `{"apply": true|false}`)
		return
	}
	ds, _, restarted, ok := s.commitDestinations(w, r, body.Apply, func(ds *config.Destinations, _ *config.Settings) error {
		if _, found := ds.Get(id); !found {
			return errNotFound
		}
		ds.Active = id
		return nil
	})
	if ok {
		v := s.listView(r, ds)
		v.Restarted = restarted
		writeJSON(w, 200, v)
	}
}

// getDestinationSecretsByID: GET /destinations/{id}/secrets, only over the local socket (the screen's eye): the four
// secrets in clear. no-store, and never logged.
func (s *Server) getDestinationSecretsByID(w http.ResponseWriter, r *http.Request) {
	ds, _, err := s.Config.Destinations()
	if err != nil {
		fail(w, r, 500, "internal", "config.read_failed")
		return
	}
	d, found := ds.Get(r.PathValue("id"))
	if !found {
		failErr(w, r, 404, "not_found", errNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, d.Secrets())
}

// --- the active destination, as before (GET/PUT /destination) ----------------------------------------------------

// destinationView: GET/PUT /destination. engine.env's destination (config.Public) plus which saved one it is.
type destinationView struct {
	config.Public
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	Template       string `json:"template,omitempty"`
	PendingRestart bool   `json:"pendingRestart"`
	Restarted      bool   `json:"restarted,omitempty"`
}

func (s *Server) destinationView(r *http.Request, ds config.Destinations, cfg config.Settings) destinationView {
	v := destinationView{Public: cfg.Public(), PendingRestart: s.pendingNow(r)}
	if d, ok := ds.ActiveDestination(); ok {
		v.ID, v.Name, v.Template = d.ID, d.Name, d.Template
	}
	return v
}

func (s *Server) getDestination(w http.ResponseWriter, r *http.Request) {
	ds, cfg, err := s.Config.Destinations()
	if err != nil {
		s.Log.Error("saved destinations", "err", err)
		fail(w, r, 500, "internal", "config.read_failed")
		return
	}
	writeJSON(w, 200, s.destinationView(r, ds, cfg))
}

// putDestination: the patch on the ACTIVE destination (409 managed when it is the cloud's). url "" leaves nothing
// active (the destination stays saved). With nothing active, a URL creates a custom destination and activates it;
// without one only the bitrates are saved.
func (s *Server) putDestination(w http.ResponseWriter, r *http.Request) {
	var p config.DestinationPatch
	if err := decode(r, &p); err != nil {
		fail(w, r, 400, "bad_request", "common.bad_body")
		return
	}
	clearURL := p.URL != nil && strings.TrimSpace(*p.URL) == ""
	ds, cfg, restarted, ok := s.commitDestinations(w, r, p.Apply, func(ds *config.Destinations, st *config.Settings) error {
		active, isActive := ds.ActiveDestination()
		switch {
		case isActive && active.Managed:
			return errManaged
		case clearURL:
			ds.Active = ""
		case isActive:
			d, err := patchDestination(p, active)
			if err != nil {
				return err
			}
			ds.Put(d)
		case p.URL != nil:
			proto := config.ProtocolOf(*p.URL)
			if p.Protocol != nil {
				proto = *p.Protocol
			}
			if proto == "" {
				return config.FieldErrors{"url": i18n.Ref("config.url_any")}
			}
			if ds.LocalCount() >= config.MaxLocalDestinations {
				return errTooMany
			}
			t, _ := config.TemplateByID(config.TemplateCustom)
			base := config.NewDestination(t, proto)
			base.ID = ds.NewID()
			d, err := patchDestination(p, base)
			if err != nil {
				return err
			}
			ds.Put(d)
			ds.Active = d.ID
		case p.Connection():
			return config.FieldErrors{"url": i18n.Ref("config.url_missing")}
		default:
			// Nothing active: the bitrates are kept for the next destination's defaults
			if p.VideoKbps != nil {
				st.VideoKbps = *p.VideoKbps
			}
			if p.AudioKbps != nil {
				st.AudioKbps = *p.AudioKbps
			}
			if p.ABR != nil {
				st.ABR = *p.ABR
			}
			if p.VideoMin != nil {
				st.VideoMinKbps = *p.VideoMin
			}
		}
		return nil
	})
	if ok {
		v := s.destinationView(r, ds, cfg)
		v.Restarted = restarted
		writeJSON(w, 200, v)
	}
}

// getDestinationSecrets: GET /destination/secrets, only over the socket: the active destination's secrets in clear
// ("" when unset), for the screen's eye button. Also with the destination managed by EnhancedIRL (the owner's
// decision: the screen shows everything; the web never reveals them). no-store, and never logged.
func (s *Server) getDestinationSecrets(w http.ResponseWriter, r *http.Request) {
	ds, _, err := s.Config.Destinations()
	if err != nil {
		fail(w, r, 500, "internal", "config.read_failed")
		return
	}
	d, _ := ds.ActiveDestination()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, d.Secrets())
}

func templateList() string {
	ids := make([]string, len(config.Templates))
	for i, t := range config.Templates {
		ids[i] = t.ID
	}
	return strings.Join(ids, ", ")
}

// --- saving and applying ----------------------------------------------------------------------------------------

// commitDestinations: UpdateDestinations, its errors answered, and with apply the common rule (applyAfterSave).
// ok=false: already answered.
func (s *Server) commitDestinations(w http.ResponseWriter, r *http.Request, apply bool,
	fn func(*config.Destinations, *config.Settings) error) (config.Destinations, config.Settings, bool, bool) {
	var ds config.Destinations
	cfg, restarted, ok := s.commit(w, r, apply, func() (config.Settings, error) {
		var cfg config.Settings
		var err error
		ds, cfg, err = s.Config.UpdateDestinations(fn)
		return cfg, err
	})
	return ds, cfg, restarted, ok
}

// commit runs a save, answers its errors and, with apply, applies it with the common rule. ok=false: already answered.
func (s *Server) commit(w http.ResponseWriter, r *http.Request, apply bool, save func() (config.Settings, error)) (cfg config.Settings, restarted, ok bool) {
	before := s.Config.Snapshot()
	cfg, err := save()
	if err != nil {
		saveError(w, r, s, err)
		return cfg, false, false
	}
	s.logConfig("configuration saved", cfg)
	if apply {
		// Not cut if the caller hangs up: over the socket the caller is the engine, which this restart stops
		ctx, cancel := s.applyCtx(r)
		defer cancel()
		if restarted, err = s.applyAfterSave(ctx, before); err != nil {
			s.Log.Error("restart", "err", err)
			fail(w, r, 500, "restart_failed", "apply.restart_failed")
			return cfg, false, false
		}
	}
	return cfg, restarted, true
}

func saveError(w http.ResponseWriter, r *http.Request, s *Server, err error) {
	var fe config.FieldErrors
	switch {
	case errors.As(err, &fe):
		fieldErrors(w, r, fe)
	case errors.Is(err, errManaged):
		failErr(w, r, 409, "managed", err)
	case errors.Is(err, errTooMany):
		failErr(w, r, 409, "too_many", err)
	case errors.Is(err, errActive):
		failErr(w, r, 409, "active", err)
	case errors.Is(err, errNotFound):
		failErr(w, r, 404, "not_found", err)
	default:
		s.Log.Error("save configuration", "err", err)
		fail(w, r, 500, "internal", "common.save_failed")
	}
}
