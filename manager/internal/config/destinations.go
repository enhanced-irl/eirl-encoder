package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// Saved destinations (OUTPUTS.md §9): several per device, one active. <dir of engine.env>/destinations.json (0600,
// the app's only; the engine never reads it) keeps them all; engine.env holds the ACTIVE one flattened (Flatten), so
// activating or editing it means rendering engine.env again. Output (on/off), EDID_PREFERRED and the input stay
// global.

// DestinationsFile: the store's name, next to engine.env.
const DestinationsFile = "destinations.json"

// Limits: 16 destinations in all, one of them always kept for the cloud's (MaxLocalDestinations); names 1-32
// characters.
const (
	MaxDestinations      = 16
	MaxLocalDestinations = MaxDestinations - 1
	MaxNameLen           = 32
)

// CloudID: the id of the cloud's destination (template eirl, managed). The rest are "d-" + 8 hex.
const CloudID = "eirl"

var reDestID = regexp.MustCompile(`^(eirl|d-[0-9a-f]{8})$`)

// ValidID: an id of a saved destination.
func ValidID(id string) bool { return reDestID.MatchString(id) }

// Destination: one saved destination with its own encoding settings. Secrets (StreamID, Passphrase, Key, Password)
// leave in clear only towards engine.env and the screen's eye (GET /destinations/{id}/secrets).
type Destination struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Template string `json:"template"`
	Managed  bool   `json:"managed"` // the cloud's (id eirl): read-only here
	Protocol string `json:"protocol"`
	// URL: srt(la)://host:port as ParseURL leaves it, or the rtmp(s)/rist/udp/rtp URL as given (output.go)
	URL  string `json:"url"`
	Mode string `json:"mode"` // caller | listener (srt only)
	// LatencyMs: SRT's latency (srt, srtla) or RIST's buffer; 0 for the other protocols
	LatencyMs    int    `json:"latencyMs"`
	StreamID     string `json:"streamId"`   // srt, srtla
	Passphrase   string `json:"passphrase"` // srt, srtla (SRT's) and rist (the pre-shared key)
	Key          string `json:"key"`        // rtmp, rtmps
	RISTProfile  string `json:"ristProfile"`
	Username     string `json:"username"` // rist main profile (SRP), with Password
	Password     string `json:"password"`
	VideoCodec   string `json:"videoCodec"`
	VideoKbps    int    `json:"videoBitrateKbps"`
	VideoMinKbps int    `json:"videoMinBitrateKbps"`
	ABR          bool   `json:"adaptiveBitrate"`
	AudioKbps    int    `json:"audioBitrateKbps"`
	OutputSize   string `json:"outputSize"` // "" (the program's) | 1280x720 | 960x540
	OutputFPS    string `json:"outputFps"`  // full | half
}

// Validate checks one destination against its protocol and its template. Errors are FieldErrors with the API's
// field names.
func (d Destination) Validate() error {
	errs := FieldErrors{}
	d.validate(errs)
	if len(errs) > 0 {
		return errs
	}
	return nil
}

func (d Destination) validate(errs FieldErrors) {
	if n := utf8.RuneCountInString(d.Name); n < 1 || n > MaxNameLen || strings.TrimSpace(d.Name) != d.Name ||
		strings.ContainsFunc(d.Name, unicode.IsControl) {
		errs["name"] = i18n.Ref("config.name", MaxNameLen)
	}
	t, ok := TemplateByID(d.Template)
	if !ok {
		errs["template"] = i18n.Ref("config.one_of", strings.Join(templateIDs(), ", "))
		return
	}
	if !slices.Contains(Protocols, d.Protocol) {
		errs["protocol"] = i18n.Ref("config.one_of", strings.Join(Protocols, ", "))
		return
	}
	if !slices.Contains(t.Protocols, d.Protocol) {
		errs["protocol"] = i18n.Ref("config.protocol_unsupported")
	}
	p := d.Protocol
	switch {
	case d.URL == "":
		errs["url"] = i18n.Ref("config.server_missing")
	case schemeProtocol(d.URL) != p:
		errs["url"] = i18n.Ref("config.url_protocol", strings.Replace(p, ProtoUDP, "udp:// · rtp", 1))
	case usesSRT(p):
		if host, _, _, err := splitURL(d.URL); err != nil {
			errs["url"] = i18n.RefOf(err)
		} else if p == ProtoSRTLA && d.Mode != "caller" {
			errs["mode"] = i18n.Ref("config.srtla_caller")
		} else if d.Mode == "listener" && host != "" {
			errs["url"] = i18n.Ref("config.listener_host")
		} else if d.Mode == "caller" && host == "" {
			errs["url"] = i18n.Ref("config.caller_host")
		}
	default:
		if _, err := checkOutputURL(d.URL); err != nil {
			errs["url"] = i18n.RefOf(err)
		}
	}
	switch {
	case p == ProtoSRT && d.Mode != "caller" && d.Mode != "listener":
		errs["mode"] = i18n.Ref("config.mode")
	case p != ProtoSRT && d.Mode != "caller" && errs["mode"] == "":
		errs["mode"] = i18n.Ref("config.caller_only")
	}
	switch {
	case usesSRT(p) && (d.LatencyMs < 20 || d.LatencyMs > 8000):
		errs["latencyMs"] = i18n.Ref("config.latency", 20, 8000)
	case p == ProtoRIST && (d.LatencyMs < RISTLatencyMin || d.LatencyMs > RISTLatencyMax):
		errs["latencyMs"] = i18n.Ref("config.latency", RISTLatencyMin, RISTLatencyMax)
	case !usesLatency(p) && d.LatencyMs != 0:
		errs["latencyMs"] = i18n.Ref("config.not_for_protocol")
	}
	// Secrets and fields of other protocols are not kept (Apply clears them when the protocol changes)
	unused := func(field, v string, used bool) bool {
		if !used && v != "" {
			errs[field] = i18n.Ref("config.not_for_protocol")
		}
		return used && v != ""
	}
	if unused("streamId", d.StreamID, usesSRT(p)) && !reStreamID.MatchString(d.StreamID) {
		errs["streamId"] = i18n.Ref("config.stream_id", 512)
	}
	if unused("passphrase", d.Passphrase, usesPassphrase(p)) && !rePassphrase.MatchString(d.Passphrase) {
		errs["passphrase"] = i18n.Ref("config.passphrase", 10, 79)
	}
	if unused("key", d.Key, usesKey(p)) && !reStreamKey.MatchString(d.Key) {
		errs["key"] = i18n.Ref("config.stream_key", 512)
	}
	unused("ristProfile", d.RISTProfile, p == ProtoRIST)
	if unused("username", d.Username, p == ProtoRIST) && !reRISTUser.MatchString(d.Username) {
		errs["username"] = i18n.Ref("config.rist_user", 64)
	}
	if unused("password", d.Password, p == ProtoRIST) && !reRISTPass.MatchString(d.Password) {
		errs["password"] = i18n.Ref("config.rist_password", 128)
	}
	if p == ProtoRIST {
		switch {
		case !slices.Contains(RISTProfiles, d.RISTProfile):
			errs["ristProfile"] = i18n.Ref("config.one_of", strings.Join(RISTProfiles, ", "))
		case (d.Username == "") != (d.Password == ""):
			errs["username"] = i18n.Ref("config.rist_credentials")
		case d.RISTProfile == "simple":
			if errs["url"] == "" {
				ristSimple(d.URL, d.Passphrase, d.Username, d.Password, errs)
			} else {
				ristSimple("", d.Passphrase, d.Username, d.Password, errs)
			}
		}
	}
	switch {
	case !slices.Contains(Codecs, d.VideoCodec):
		errs["videoCodec"] = i18n.Ref("config.one_of", strings.Join(Codecs, ", "))
	case !slices.Contains(t.Codecs, d.VideoCodec):
		errs["videoCodec"] = i18n.Ref("config.codec_unsupported")
	}
	if d.VideoKbps < 500 || d.VideoKbps > t.MaxVideoKbps {
		errs["videoBitrateKbps"] = i18n.Ref("config.kbps_range", 500, t.MaxVideoKbps)
	}
	if d.VideoMinKbps < 200 || d.VideoMinKbps > t.MaxVideoKbps {
		errs["videoMinBitrateKbps"] = i18n.Ref("config.kbps_range", 200, t.MaxVideoKbps)
	} else if d.ABR && d.VideoMinKbps > d.VideoKbps {
		errs["videoMinBitrateKbps"] = i18n.Ref("config.min_above_max")
	}
	if !audioRates[d.AudioKbps] {
		errs["audioBitrateKbps"] = i18n.Ref("config.one_of", "64, 96, 128, 160, 192, 256")
	}
	if !slices.Contains(OutputSizes, d.OutputSize) {
		errs["outputSize"] = i18n.Ref("config.output_size")
	}
	if !slices.Contains(OutputFPSs, d.OutputFPS) {
		errs["outputFps"] = i18n.Ref("config.one_of", strings.Join(OutputFPSs, ", "))
	}
}

// flatten writes the destination into the engine settings (OUTPUTS.md §3): srt and srtla keep SRT_URL and an empty
// OUTPUT_URL; every other protocol an empty SRT_URL (an older engine then answers no_destination instead of
// streaming somewhere else), with SRT's keys at their defaults.
func (d Destination) flatten(s *Settings) {
	clearDestination(s)
	s.Protocol = d.Protocol
	if usesSRT(d.Protocol) {
		s.URL, s.Mode, s.LatencyMs, s.StreamID, s.Passphrase = d.URL, d.Mode, d.LatencyMs, d.StreamID, d.Passphrase
	} else {
		s.OutputURL, s.Key = d.URL, d.Key
	}
	if d.Protocol == ProtoRIST {
		s.OutputPassphrase, s.OutputUser, s.OutputPassword = d.Passphrase, d.Username, d.Password
		s.RISTProfile, s.OutputLatencyMs = d.RISTProfile, d.LatencyMs
	}
	s.VideoCodec, s.OutputSize, s.OutputFPS = d.VideoCodec, d.OutputSize, d.OutputFPS
	s.VideoKbps, s.VideoMinKbps, s.ABR, s.AudioKbps = d.VideoKbps, d.VideoMinKbps, d.ABR, d.AudioKbps
}

// clearDestination: the settings without a destination (nothing active). The bitrates stay: they are only used
// with a destination, which brings its own.
func clearDestination(s *Settings) {
	def := Defaults()
	s.URL, s.Mode, s.LatencyMs, s.StreamID, s.Passphrase = "", def.Mode, def.LatencyMs, "", ""
	s.Protocol, s.OutputURL, s.Key, s.OutputPassphrase, s.OutputUser, s.OutputPassword = "", "", "", "", "", ""
	s.RISTProfile, s.OutputLatencyMs, s.VideoCodec, s.OutputSize, s.OutputFPS = "", 0, "", "", ""
}

// copyDestination: the destination fields of src into s (Store.Update keeps them: they only change through the
// saved destinations).
func copyDestination(s *Settings, src Settings) {
	s.URL, s.Mode, s.LatencyMs, s.StreamID, s.Passphrase = src.URL, src.Mode, src.LatencyMs, src.StreamID, src.Passphrase
	s.Protocol, s.OutputURL, s.Key, s.OutputPassphrase = src.Protocol, src.OutputURL, src.Key, src.OutputPassphrase
	s.OutputUser, s.OutputPassword, s.RISTProfile, s.OutputLatencyMs = src.OutputUser, src.OutputPassword, src.RISTProfile, src.OutputLatencyMs
	s.VideoCodec, s.OutputSize, s.OutputFPS = src.VideoCodec, src.OutputSize, src.OutputFPS
	s.VideoKbps, s.VideoMinKbps, s.ABR, s.AudioKbps = src.VideoKbps, src.VideoMinKbps, src.ABR, src.AudioKbps
	s.ManagedBy = src.ManagedBy
}

// fromSettings: the destination engine.env holds (the migration). Without id, name or template.
func fromSettings(s Settings) Destination {
	p := s.EffectiveProtocol()
	d := Destination{Protocol: p, Mode: "caller", VideoCodec: s.Codec(), VideoKbps: s.VideoKbps,
		VideoMinKbps: s.VideoMinKbps, ABR: s.ABR, AudioKbps: s.AudioKbps, OutputSize: s.OutputSize, OutputFPS: s.OutputFPS}
	if d.OutputFPS == "" {
		d.OutputFPS = "full"
	}
	switch {
	case usesSRT(p):
		d.URL, d.Mode, d.LatencyMs, d.StreamID, d.Passphrase = s.URL, s.Mode, s.LatencyMs, s.StreamID, s.Passphrase
	case p == ProtoRIST:
		d.URL, d.Passphrase, d.Username, d.Password = s.OutputURL, s.OutputPassphrase, s.OutputUser, s.OutputPassword
		d.RISTProfile, d.LatencyMs = s.RISTProfile, s.OutputLatencyMs
		if d.RISTProfile == "" {
			d.RISTProfile = "main"
		}
		if d.LatencyMs == 0 {
			d.LatencyMs = RISTLatencyDefault
		}
	default:
		d.URL, d.Key = s.OutputURL, s.Key
	}
	return d
}

// --- the list ----------------------------------------------------------------------------------------------------

// Destinations: the store's content (destinations.json). Active "" = nothing active.
type Destinations struct {
	V            int           `json:"v"`
	Active       string        `json:"active"`
	Destinations []Destination `json:"destinations"`
}

// Empty: a list with nothing.
func Empty() Destinations { return Destinations{V: 1, Destinations: []Destination{}} }

func (ds Destinations) Clone() Destinations {
	ds.Destinations = slices.Clone(ds.Destinations)
	if ds.Destinations == nil {
		ds.Destinations = []Destination{}
	}
	return ds
}

// Get: the destination with that id.
func (ds Destinations) Get(id string) (Destination, bool) {
	i := slices.IndexFunc(ds.Destinations, func(d Destination) bool { return d.ID == id })
	if i < 0 {
		return Destination{}, false
	}
	return ds.Destinations[i], true
}

// ActiveDestination: the active one, if any.
func (ds Destinations) ActiveDestination() (Destination, bool) {
	if ds.Active == "" {
		return Destination{}, false
	}
	return ds.Get(ds.Active)
}

// Put replaces the destination with the same id or adds it at the end.
func (ds *Destinations) Put(d Destination) {
	if i := slices.IndexFunc(ds.Destinations, func(x Destination) bool { return x.ID == d.ID }); i >= 0 {
		ds.Destinations[i] = d
		return
	}
	ds.Destinations = append(ds.Destinations, d)
}

// Remove deletes it; if it was the active one, nothing is active (wasActive).
func (ds *Destinations) Remove(id string) (removed, wasActive bool) {
	n := len(ds.Destinations)
	ds.Destinations = slices.DeleteFunc(ds.Destinations, func(d Destination) bool { return d.ID == id })
	if ds.Active == id {
		ds.Active, wasActive = "", true
	}
	return len(ds.Destinations) != n, wasActive
}

// LocalCount: the destinations that are not the cloud's.
func (ds Destinations) LocalCount() int {
	n := 0
	for _, d := range ds.Destinations {
		if d.ID != CloudID {
			n++
		}
	}
	return n
}

// ManagedBy: eirl when the ACTIVE destination is the cloud's, else local (EIRL_MANAGED_BY and the API's managedBy).
func (ds Destinations) ManagedBy() string {
	if ds.Active == CloudID {
		return "eirl"
	}
	return "local"
}

// Flatten: the settings with the active destination in them (or none) and ManagedBy.
func (ds Destinations) Flatten(s Settings) Settings {
	if d, ok := ds.ActiveDestination(); ok {
		d.flatten(&s)
	} else {
		clearDestination(&s)
	}
	s.ManagedBy = ds.ManagedBy()
	return s
}

// NewID: a free "d-" + 8 hex id.
func (ds Destinations) NewID() string {
	for {
		var b [4]byte
		rand.Read(b[:])
		id := "d-" + hex.EncodeToString(b[:])
		if _, taken := ds.Get(id); !taken {
			return id
		}
	}
}

// Validate checks the whole list: version, limits, ids, the cloud's destination and every destination.
func (ds Destinations) Validate() error {
	if ds.V != 1 {
		return fmt.Errorf("destinations: version %d", ds.V)
	}
	if len(ds.Destinations) > MaxDestinations {
		return fmt.Errorf("destinations: more than %d", MaxDestinations)
	}
	seen := map[string]bool{}
	for _, d := range ds.Destinations {
		if !ValidID(d.ID) || seen[d.ID] {
			return fmt.Errorf("destinations: invalid or repeated id %q", d.ID)
		}
		seen[d.ID] = true
		if cloud := d.ID == CloudID; cloud != (d.Template == TemplateEIRL) || cloud != d.Managed {
			return fmt.Errorf("destinations: %s: only the cloud's destination is eirl and managed", d.ID)
		}
		if err := d.Validate(); err != nil {
			return err
		}
	}
	if ds.Active != "" && !seen[ds.Active] {
		return errors.New("destinations: the active one does not exist")
	}
	return nil
}

// Migrate: the list the first time (no file yet), from the configuration engine.env holds (OUTPUTS.md §9.1):
//   - a destination the cloud set (cloudOwned) → the cloud's (eirl), active, codec H.265;
//   - another one → a custom one named after its protocol, active, with the codec it streamed with (H.264);
//   - nothing (or one that is not valid: it could not be streamed to) → an empty list.
func Migrate(s Settings, cloudOwned bool) Destinations {
	ds := Empty()
	if !s.HasDestination() {
		return ds
	}
	d := fromSettings(s)
	if cloudOwned {
		d.ID, d.Name, d.Template, d.Managed, d.VideoCodec = CloudID, templateNames[TemplateEIRL], TemplateEIRL, true, "h265"
	} else {
		d.ID, d.Name, d.Template = ds.NewID(), protocolNames[d.Protocol], TemplateCustom
	}
	if d.Validate() != nil {
		return ds
	}
	ds.Destinations, ds.Active = append(ds.Destinations, d), d.ID
	return ds
}

// --- the patch (POST /destinations, PUT /destinations/{id}, PUT /destination) -----------------------------------

// DestinationPatch: the body of the routes that create or change a destination. Missing fields are left alone;
// the four secrets with Optional (absent: leave it, null: clear it, a value: change it).
type DestinationPatch struct {
	Name        *string  `json:"name"`
	Template    *string  `json:"template"`
	Protocol    *string  `json:"protocol"`
	URL         *string  `json:"url"`
	Mode        *string  `json:"mode"`
	LatencyMs   *int     `json:"latencyMs"`
	StreamID    Optional `json:"streamId"`
	Passphrase  Optional `json:"passphrase"`
	Key         Optional `json:"key"`
	Password    Optional `json:"password"`
	RISTProfile *string  `json:"ristProfile"`
	Username    *string  `json:"username"`
	VideoCodec  *string  `json:"videoCodec"`
	VideoKbps   *int     `json:"videoBitrateKbps"`
	VideoMin    *int     `json:"videoMinBitrateKbps"`
	ABR         *bool    `json:"adaptiveBitrate"`
	AudioKbps   *int     `json:"audioBitrateKbps"`
	OutputSize  *string  `json:"outputSize"`
	OutputFPS   *string  `json:"outputFps"`
	Apply       bool     `json:"apply"`
}

// Connection: the patch carries something of the connection (not only the encoding settings).
func (p DestinationPatch) Connection() bool {
	return p.Protocol != nil || p.URL != nil || p.Mode != nil || p.LatencyMs != nil || p.StreamID.Set ||
		p.Passphrase.Set || p.Key.Set || p.Password.Set || p.RISTProfile != nil || p.Username != nil
}

// ApplyTo applies the patch on cur and validates the result (cur is returned on error).
//   - A pasted srt(la) URL with its query fills the SRT fields as before (ParseURL; its streamid, passphrase,
//     latency and mode win over the separate fields), and switching to SRTLA raises the latency to 2000 ms unless
//     the same request set another one.
//   - The protocol follows the URL's scheme unless the patch gives it (then they must agree).
//   - Changing the protocol clears what the new one does not use; a value given for one of them is an error.
//   - Changing the server asks for the secrets again (or their removal): otherwise a stolen session could send the
//     saved key to its own server by changing only the URL.
func (p DestinationPatch) ApplyTo(cur Destination) (Destination, error) {
	d := cur
	str := func(v *string, dst *string) {
		if v != nil {
			*dst = *v
		}
	}
	num := func(v *int, dst *int) {
		if v != nil {
			*dst = *v
		}
	}
	opt := func(o Optional, dst *string) {
		if o.Null {
			*dst = ""
		} else if o.Set {
			*dst = o.Value
		}
	}
	if p.Name != nil {
		d.Name = strings.TrimSpace(*p.Name)
	}
	str(p.Template, &d.Template)
	str(p.Protocol, &d.Protocol)
	str(p.Mode, &d.Mode)
	num(p.LatencyMs, &d.LatencyMs)
	opt(p.StreamID, &d.StreamID)
	opt(p.Passphrase, &d.Passphrase)
	opt(p.Key, &d.Key)
	opt(p.Password, &d.Password)
	str(p.RISTProfile, &d.RISTProfile)
	str(p.Username, &d.Username)
	str(p.VideoCodec, &d.VideoCodec)
	num(p.VideoKbps, &d.VideoKbps)
	num(p.VideoMin, &d.VideoMinKbps)
	num(p.AudioKbps, &d.AudioKbps)
	str(p.OutputSize, &d.OutputSize)
	str(p.OutputFPS, &d.OutputFPS)
	if p.ABR != nil {
		d.ABR = *p.ABR
	}
	urlLatency := false
	if p.URL != nil {
		raw := strings.TrimSpace(*p.URL)
		scheme := schemeProtocol(raw)
		if p.Protocol != nil && scheme != "" && scheme != *p.Protocol {
			return cur, FieldErrors{"url": i18n.Ref("config.url_protocol", *p.Protocol)}
		}
		if scheme != "" {
			d.Protocol = scheme
		}
		switch {
		case raw == "":
			d.URL = ""
		case usesSRT(scheme):
			u, err := ParseURL(raw)
			if err != nil {
				return cur, FieldErrors{"url": i18n.RefOf(err)}
			}
			d.URL = u.URL
			// The mode comes from the URL (with a host: caller to the relay; without: listener) unless the query
			// says otherwise. So pasting a key's URL while in listener mode does not fail.
			switch {
			case u.Mode != "":
				d.Mode = u.Mode
			case strings.HasPrefix(u.URL, "srt://:"):
				d.Mode = "listener"
			default:
				d.Mode = "caller"
			}
			if u.LatencyMs != 0 {
				d.LatencyMs, urlLatency = u.LatencyMs, true
			}
			if u.StreamID != "" {
				d.StreamID = u.StreamID
			}
			if u.Passphrase != "" {
				d.Passphrase = u.Passphrase
			}
		default:
			d.URL = raw // validated below (checkOutputURL)
		}
	}
	errs := FieldErrors{}
	d.normalize(cur, p, urlLatency, errs)
	// Changing the server: the secrets again. Switching to an SRT listener does not expose them (the caller sets
	// the stream ID)
	if newHost := URLHost(d.URL); newHost != "" && newHost != URLHost(cur.URL) {
		for _, sec := range []struct {
			field, cur, next string
			given            bool
		}{{"streamId", cur.StreamID, d.StreamID, p.StreamID.Set}, {"passphrase", cur.Passphrase, d.Passphrase, p.Passphrase.Set},
			{"key", cur.Key, d.Key, p.Key.Set}, {"password", cur.Password, d.Password, p.Password.Set}} {
			if sec.cur != "" && sec.next == sec.cur && !sec.given {
				errs[sec.field] = i18n.Ref("config.new_server_" + strings.ToLower(sec.field))
			}
		}
	}
	if len(errs) > 0 {
		return cur, errs
	}
	if err := d.Validate(); err != nil {
		return cur, err
	}
	return d, nil
}

// normalize: what changes with the protocol (the mode, the latency's default, the fields the protocol does not
// use). errs: values given for a field the protocol does not use.
func (d *Destination) normalize(cur Destination, p DestinationPatch, urlLatency bool, errs FieldErrors) {
	switched := d.Protocol != cur.Protocol
	// A latency counts as chosen if the URL brought it, or the request changed it (the form resends the previous one)
	latencySet := urlLatency || p.LatencyMs != nil && *p.LatencyMs != cur.LatencyMs
	switch d.Protocol {
	case ProtoSRT:
		if d.Mode == "" {
			d.Mode = "caller"
		}
		if !latencySet && !usesSRT(cur.Protocol) {
			d.LatencyMs = Defaults().LatencyMs
		}
	case ProtoSRTLA:
		// With mobile uplinks the LAN SRT latency falls short: up to 2000 ms unless this request set another one
		if !latencySet && cur.Protocol != ProtoSRTLA && d.LatencyMs < DefaultLatencyMs {
			d.LatencyMs = DefaultLatencyMs
		}
	case ProtoRIST:
		if !latencySet && switched {
			d.LatencyMs = RISTLatencyDefault
		}
		if d.RISTProfile == "" {
			d.RISTProfile = "main"
		}
	default:
		d.LatencyMs = 0 // no latency: a value the form sends is ignored
	}
	if d.Protocol != ProtoSRT && p.Mode == nil {
		d.Mode = "caller"
	}
	// Switching protocol on a template's own server: the template's server for the new protocol (Twitch over RTMPS)
	if t, ok := TemplateByID(d.Template); ok && switched && p.URL == nil && cur.URL != "" && cur.URL == t.URLs[cur.Protocol] {
		if u := t.URLs[d.Protocol]; u != "" {
			d.URL = u
		}
	}
	clear := func(field string, used, given bool, dst *string) {
		if used {
			return
		}
		if given && *dst != "" {
			errs[field] = i18n.Ref("config.not_for_protocol")
		}
		*dst = ""
	}
	pr := d.Protocol
	clear("streamId", usesSRT(pr), p.StreamID.Set && !p.StreamID.Null, &d.StreamID)
	clear("passphrase", usesPassphrase(pr), p.Passphrase.Set && !p.Passphrase.Null, &d.Passphrase)
	clear("key", usesKey(pr), p.Key.Set && !p.Key.Null, &d.Key)
	clear("password", pr == ProtoRIST, p.Password.Set && !p.Password.Null, &d.Password)
	clear("username", pr == ProtoRIST, p.Username != nil, &d.Username)
	clear("ristProfile", pr == ProtoRIST, p.RISTProfile != nil, &d.RISTProfile)
	// RIST's simple profile has no passphrase nor user: switching to it drops the saved ones (the screen and the
	// web hide those fields); giving one with it is still refused (Validate)
	if pr == ProtoRIST && d.RISTProfile == "simple" {
		if !p.Passphrase.Set || p.Passphrase.Null {
			d.Passphrase = ""
		}
		if p.Username == nil || *p.Username == "" {
			d.Username = ""
		}
		if !p.Password.Set || p.Password.Null {
			d.Password = ""
		}
	}
}
