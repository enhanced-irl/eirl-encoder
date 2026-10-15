package ui

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Stream › Destination with saved destinations (OUTPUTS.md §11), when the app has them (GET /destinations; an older
// app keeps the single destination of dest.go). The page is a list: each row its name, protocol and server, the
// active one marked and the cloud's with its read-only mark; a touch on a row streams to it (POST …/activate; a second
// touch while streaming, the output restarts 1-2 s), its «›» opens its page, and «Add» picks a template (the
// cloud's is not creatable) and opens a new destination's page. A destination's page has two sections: Connection
// (name, protocol, server and what its protocol needs) and Video and audio (codec, bitrates and adaptive bitrate,
// output size and frame rate, audio). It is a draft until «Save» (POST /destinations for a new one, PUT
// /destinations/{id}) or, for the active one, «Save and apply»; its secrets are typed on the secret keyboard, kept in
// the draft until the app saves them and shown behind their eye (GET /destinations/{id}/secrets). «Delete» (a second
// touch) is there for a saved one that is neither the active one nor the cloud's, which is read-only.

// DestListRows is how many destinations the list shows per page (16 at most: four pages).
const DestListRows = 5

// MaxDests is the most saved destinations the app keeps (OUTPUTS.md §9.1).
const MaxDests = 16

// RistBufferMin is the least RIST buffer the app takes (OUTPUTS.md §3; its maximum is LatencyMax).
const RistBufferMin = 50

// templateNames are the templates by their names; custom's is translated (Dests.Custom).
var templateNames = map[string]string{"eirl": "EnhancedIRL", "twitch": "Twitch", "youtube": "YouTube", "kick": "Kick",
	"facebook": "Facebook"}

// The characters the app takes in a key, a RIST passphrase or password and a RIST user (OUTPUTS.md §3).
var (
	reKeyChar      = regexp.MustCompile(`^[A-Za-z0-9._~!$()*+,;=:@%/?&-]$`)
	reRistPassChar = regexp.MustCompile(`^[A-Za-z0-9_.~-]$`)
	reRistUserChar = regexp.MustCompile(`^[A-Za-z0-9_.-]$`)
)

func allowRe(re *regexp.Regexp) func(r rune) bool {
	return func(r rune) bool { return re.MatchString(string(r)) }
}

// destsState is the list's and the open destination's state.
type destsState struct {
	ed    *destEditor
	page  int
	actID string // the one being activated
}

// destEditor is a destination's page: its draft (API field → value) over the app's view, or over a new one's starting
// values. The typed secrets live here until the app saves them or the page goes, and are wiped then.
type destEditor struct {
	id, tmpl  string // id "" = a new destination of template tmpl
	section   string // conn | video
	base      DestInfo
	fields    map[string]any
	secrets   map[string][]byte // streamId | passphrase | key | password
	clears    map[string]bool   // typed empty: remove it
	savedAt   time.Time
	synced    time.Time
	sentAt    time.Time
	dirtyAt   time.Time
	sentApply bool
}

// clearDraft wipes the typed secrets and forgets the draft (the page stays).
func (e *destEditor) clearDraft() {
	for k, b := range e.secrets {
		clear(b)
		delete(e.secrets, k)
	}
	e.fields, e.secrets, e.clears = nil, nil, nil
}

// edited: something was changed on the page.
func (e *destEditor) edited() bool {
	return len(e.fields) > 0 || len(e.secrets) > 0 || len(e.clears) > 0
}

// dirty: «Save» has something to send (a new destination always has).
func (e *destEditor) dirty() bool { return e.id == "" || e.edited() }

// destsOn: the app has the saved destinations (else the single destination of an older app).
func destsOn(app *AppSnapshot) bool { return app.DestsAPI == Yes && app.Dests != nil }

// destsLayout places the list and a destination's page.
func destsLayout(l *Layout) {
	c := l.SettingsPage
	b := l.Buttons
	gap := l.M / 2
	foot := max(52, l.HB+6)
	bottom := Rect{c.X, c.Y + c.H - foot, c.W, foot}
	// The list: rows with their «›» inside at the right, then ‹ › and «Add»
	rh := max(48, min(64, (c.H-foot-l.M-(DestListRows-1)*gap)/DestListRows))
	ob := max(48, l.IB)
	oh := max(48, min(rh-8, l.HB+6))
	for i := range DestListRows {
		r := Rect{c.X, c.Y + i*(rh+gap), c.W, rh}
		b["s_dl_"+strconv.Itoa(i)] = r
		b["s_dlo_"+strconv.Itoa(i)] = Rect{r.X + r.W - l.M/2 - ob, r.Y + (rh-oh)/2, ob, oh}
	}
	pw := max(56, l.IB)
	b["s_dl_prev"] = Rect{bottom.X, bottom.Y, pw, foot}
	b["s_dl_next"] = Rect{bottom.X + pw + gap, bottom.Y, pw, foot}
	aw := max(200, c.W/4)
	b["s_dl_add"] = Rect{bottom.X + bottom.W - aw, bottom.Y, aw, foot}
	// A destination's page: the sections' chips, five rows, and the bottom
	ch := 48
	cw := (c.W - gap) / 2
	b["s_de_sec_conn"] = Rect{c.X, c.Y, cw, ch}
	b["s_de_sec_video"] = Rect{c.X + cw + gap, c.Y, c.W - cw - gap, ch}
	top := c.Y + ch + l.M
	avail := bottom.Y - l.M - top
	erh := max(48, min(64, (avail-4*gap)/5))
	row := func(i int) Rect { return Rect{c.X, top + i*(erh+gap), c.W, erh} }
	half := func(i int) (Rect, Rect) { return l.halves(row(i)) }
	// Connection
	b["s_de_name"], b["s_de_proto"] = half(0)
	b["s_de_url"] = row(1)
	sid, pass := half(2)
	b["s_de_sid"] = l.eyeKey(sid, "s_de_sid_eye")
	b["s_de_pass"] = l.eyeKey(pass, "s_de_pass_eye")
	b["s_de_key"] = l.eyeKey(row(2), "s_de_key_eye")
	b["s_de_prof"], b["s_de_buf"] = half(2)
	l.stepperKeys(b["s_de_buf"], "s_de_buf_m", "s_de_buf_p")
	b["s_de_lat"], _ = half(3)
	l.stepperKeys(b["s_de_lat"], "s_de_lat_m", "s_de_lat_p")
	rpass, user := half(3)
	b["s_de_rpass"] = l.eyeKey(rpass, "s_de_rpass_eye")
	b["s_de_user"] = user
	pw4, _ := half(4)
	b["s_de_pw"] = l.eyeKey(pw4, "s_de_pw_eye")
	// Video and audio
	b["s_de_codec"], b["s_de_size"] = half(0)
	b["s_de_vid"], b["s_de_vmin"] = half(1)
	l.stepperKeys(b["s_de_vid"], "s_de_vid_m", "s_de_vid_p")
	l.stepperKeys(b["s_de_vmin"], "s_de_vmin_m", "s_de_vmin_p")
	b["s_de_abr"] = row(2)
	b["s_de_fps"], b["s_de_aud"] = half(3)
	l.stepperKeys(b["s_de_aud"], "s_de_aud_m", "s_de_aud_p")
	// The bottom: «Delete» on the left; «Save» and «Save and apply» on the right («Save» alone: s_de_save1)
	sw2, sw := max(240, bottom.W*2/5), max(160, bottom.W/4)
	b["s_de_apply"] = Rect{bottom.X + bottom.W - sw2, bottom.Y, sw2, foot}
	b["s_de_save"] = Rect{bottom.X + bottom.W - sw2 - l.M - sw, bottom.Y, sw, foot}
	b["s_de_save1"] = Rect{bottom.X + bottom.W - sw, bottom.Y, sw, foot}
	b["s_de_delete"] = Rect{bottom.X, bottom.Y, max(160, bottom.W/4), foot}
}

// templateName is a template's name in the screen's language.
func (u *UI) templateName(id string) string {
	if n := templateNames[id]; n != "" {
		return n
	}
	return u.Tx.Dests.Custom
}

// destTemplateOf is a destination's template as the app gave it; one that allows what it has, if the app did not.
func destTemplateOf(app *AppSnapshot, d *DestInfo) DestTemplate {
	if t := app.Dests.Template(d.Template); t != nil {
		return *t
	}
	t := DestTemplate{ID: d.Template, Codecs: []string{"h264", "h265"}}
	if d.Protocol != "" {
		t.Protocols = []string{d.Protocol}
	}
	return t
}

// maxVideo is the most video a template takes (kbps).
func maxVideo(t DestTemplate) int {
	if t.MaxVideoKbps > 0 {
		return min(t.MaxVideoKbps, VideoMax)
	}
	return VideoMax
}

// defaultLatency is a new destination's latency (srt, srtla) or buffer (rist).
func defaultLatency(protocol string) int {
	if protocol == "rist" {
		return 1000
	}
	return 2000
}

// --- the editor's draft over the app's view ---

// deBase is what the open page starts from: the app's view of it, or a new one's starting values (nil: gone).
func (u *UI) deBase(app *AppSnapshot) *DestInfo {
	ed := u.dl.ed
	if ed == nil {
		return nil
	}
	if ed.id == "" {
		return &ed.base
	}
	return app.Dests.ByID(ed.id)
}

// deNow is what the page shows: the draft over its base; ok false if the destination is gone.
func (u *UI) deNow(app *AppSnapshot) (DestInfo, bool) {
	b := u.deBase(app)
	if b == nil {
		return DestInfo{}, false
	}
	n := *b
	ed := u.dl.ed
	for k, v := range ed.fields {
		switch x := v.(type) {
		case string:
			switch k {
			case "name":
				n.Name = x
			case "protocol":
				n.Protocol = x
			case "url":
				n.URL = x
			case "ristProfile":
				n.RistProfile = x
			case "username":
				n.Username = x
			case "videoCodec":
				n.VideoCodec = x
			case "outputSize":
				n.OutputSize = x
			case "outputFps":
				n.OutputFps = x
			}
		case int:
			switch k {
			case "latencyMs":
				n.LatencyMs = x
			case "videoBitrateKbps":
				n.VideoKbps = x
			case "videoMinBitrateKbps":
				n.VideoMinKbps = x
			case "audioBitrateKbps":
				n.AudioKbps = x
			}
		case bool:
			if k == "adaptiveBitrate" {
				n.ABR = x
			}
		}
	}
	for k, set := range map[string]*bool{"streamId": &n.StreamIDSet, "passphrase": &n.PassphraseSet, "key": &n.KeySet,
		"password": &n.PasswordSet} {
		switch {
		case ed.clears[k]:
			*set = false
		case ed.secrets[k] != nil:
			*set = true
		}
	}
	return n, true
}

// deEdit is the draft, marked as edited now.
func (u *UI) deEdit() *destEditor {
	ed := u.dl.ed
	ed.dirtyAt = u.now()
	if ed.fields == nil {
		ed.fields = map[string]any{}
	}
	return ed
}

// deSet changes a field of the draft (back to the base's value: no change).
func (u *UI) deSet(app *AppSnapshot, k string, v any) {
	ed := u.deEdit()
	ed.fields[k] = v
	if b := u.deBase(app); b != nil && ed.id != "" {
		if base, ok := destField(b, k); ok && base == v {
			delete(ed.fields, k)
		}
	}
	u.Dirty = true
}

// destField is a field of a view by its API name.
func destField(d *DestInfo, k string) (any, bool) {
	switch k {
	case "name":
		return d.Name, true
	case "protocol":
		return d.Protocol, true
	case "url":
		return d.URL, true
	case "ristProfile":
		return d.RistProfile, true
	case "username":
		return d.Username, true
	case "videoCodec":
		return d.VideoCodec, true
	case "outputSize":
		return d.OutputSize, true
	case "outputFps":
		return d.OutputFps, true
	case "latencyMs":
		return d.LatencyMs, true
	case "videoBitrateKbps":
		return d.VideoKbps, true
	case "videoMinBitrateKbps":
		return d.VideoMinKbps, true
	case "audioBitrateKbps":
		return d.AudioKbps, true
	case "adaptiveBitrate":
		return d.ABR, true
	}
	return nil, false
}

// deSecret keeps a typed secret in the draft: empty removes the saved one (if there is one).
func (u *UI) deSecret(k string, b []byte, wasSet bool) {
	ed := u.deEdit()
	if ed.secrets == nil {
		ed.secrets = map[string][]byte{}
	}
	if ed.clears == nil {
		ed.clears = map[string]bool{}
	}
	clear(ed.secrets[k])
	delete(ed.secrets, k)
	delete(ed.clears, k)
	switch {
	case len(b) > 0:
		ed.secrets[k] = append([]byte(nil), b...)
	case wasSet && ed.id != "":
		ed.clears[k] = true
	}
	u.Dirty = true
}

// destFieldsOf are the fields a protocol uses besides the common ones (its secrets: destSecretsOf).
func destFieldsOf(protocol string) []string {
	common := []string{"name", "protocol", "url", "videoCodec", "videoBitrateKbps", "videoMinBitrateKbps",
		"adaptiveBitrate", "audioBitrateKbps", "outputSize", "outputFps"}
	switch protocol {
	case "srt", "srtla":
		return append(common, "latencyMs")
	case "rist":
		return append(common, "latencyMs", "ristProfile", "username")
	}
	return common
}

// destSecretsOf are the secrets a protocol uses.
func destSecretsOf(protocol string) []string {
	switch protocol {
	case "srt", "srtla":
		return []string{"streamId", "passphrase"}
	case "rtmp", "rtmps":
		return []string{"key"}
	case "rist":
		return []string{"passphrase", "password"}
	}
	return nil
}

// adaptURL is the server for a new protocol: the template's if it is of that protocol, else the same server with
// the scheme of its family (srt ↔ srtla, rtmp ↔ rtmps, with the default ports), else none (the user types it).
func adaptURL(url, from, to string, t DestTemplate) string {
	if u := t.URLs[to]; u != "" && (url == "" || url == t.URLs[from] || url == t.URL) {
		return u // the template's own server for that protocol (YouTube's RTMP and RTMPS hosts differ)
	}
	if t.URL != "" && strings.HasPrefix(t.URL, to+"://") {
		return t.URL
	}
	family := map[string]string{"srt": "srt", "srtla": "srt", "rtmp": "rtmp", "rtmps": "rtmp"}
	rest, ok := strings.CutPrefix(url, from+"://")
	if !ok || family[from] == "" || family[from] != family[to] {
		return ""
	}
	host, path, cut := strings.Cut(rest, "/")
	switch {
	case to == "rtmps" && strings.HasSuffix(host, ":1935"):
		host = strings.TrimSuffix(host, ":1935") + ":443"
	case to == "rtmp" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443") + ":1935"
	}
	if cut {
		host += "/" + path
	}
	return to + "://" + host
}

// hostOf is a URL's server (host and port) for the list.
func hostOf(url string) string {
	_, rest, ok := strings.Cut(url, "://")
	if !ok {
		return url
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// --- opening and closing ---

// openDest shows a saved destination's page.
func (u *UI) openDest(id string) {
	u.dl.ed = &destEditor{id: id, section: "conn"}
	u.confirm, u.pageSince = nil, u.now()
	u.Dirty = true
}

// openNewDest shows a new destination's page, from a template's starting values.
func (u *UI) openNewDest(app *AppSnapshot, tmpl string) {
	t := app.Dests.Template(tmpl)
	if t == nil || len(t.Protocols) == 0 {
		return
	}
	p := t.Protocols[0]
	b := DestInfo{Name: u.templateName(tmpl), Template: tmpl, Protocol: p, Mode: "caller", LatencyMs: defaultLatency(p),
		VideoCodec: "h264", VideoKbps: min(6000, maxVideo(*t)), VideoMinKbps: 1000, ABR: true, AudioKbps: 128,
		OutputFps: "full", RistProfile: "main"}
	if strings.HasPrefix(t.URL, p+"://") {
		b.URL = t.URL
	}
	if len(t.Codecs) > 0 {
		b.VideoCodec = t.Codecs[0]
	}
	u.dl.ed = &destEditor{tmpl: tmpl, section: "conn", base: b}
	u.confirm, u.pageSince = nil, u.now()
	u.Dirty = true
}

// closeDest goes back to the list: the draft and anything secret it showed go.
func (u *UI) closeDest() {
	if u.dl.ed != nil {
		u.dl.ed.clearDraft()
	}
	u.dl.ed = nil
	u.dropSecrets()
	clear(u.reveal)
	u.confirm, u.pageSince = nil, u.now()
	u.Dirty = true
}

// destsLeave: the page goes (another page, Settings closed, the screen locked).
func (u *UI) destsLeave() {
	if u.dl.ed != nil {
		u.dl.ed.clearDraft()
	}
	u.dl.ed, u.dl.page = nil, 0
}

// destsTitle is a destination's page's title row: its name (a new one: «New destination») and ←.
func (u *UI) destsTitle() (string, bool, bool) {
	app := u.app().AppSnapshot()
	if u.dl.ed == nil || !destsOn(&app) {
		return "", false, false
	}
	if u.dl.ed.id == "" {
		return u.Tx.Dests.AddTitle + u.Tx.Sep + u.templateName(u.dl.ed.tmpl), true, true
	}
	n, ok := u.deNow(&app)
	if !ok || n.Name == "" {
		return u.templateName(n.Template), true, true
	}
	return n.Name, true, true
}

// --- the list ---

// destShown are the destinations of the list's page on view.
func (u *UI) destShown(app *AppSnapshot) []DestInfo {
	list := app.Dests.Destinations
	u.dl.page = max(0, min(u.dl.page, destPages(app)-1))
	lo := u.dl.page * DestListRows
	return list[lo:min(len(list), lo+DestListRows)]
}

func (u *UI) destListKeys(app *AppSnapshot) []string {
	var keys []string
	for i := range u.destShown(app) {
		keys = append(keys, "s_dlo_"+strconv.Itoa(i), "s_dl_"+strconv.Itoa(i)) // «›» before its row: it is inside
	}
	if pages := destPages(app); pages > 1 {
		keys = append(keys, "s_dl_prev", "s_dl_next")
	}
	return append(keys, "s_dl_add")
}

func destPages(app *AppSnapshot) int {
	return max(1, (len(app.Dests.Destinations)+DestListRows-1)/DestListRows)
}

// canAdd: a destination can be added (room left, and a template that can be created).
func canAdd(app *AppSnapshot) bool {
	for _, t := range app.Dests.Templates {
		if t.ID != "eirl" {
			return true
		}
	}
	return false
}

func (u *UI) drawDestList(d *dc, app *AppSnapshot) {
	lay, tx, dt, c, s := u.Lay, u.Tx, u.Tx.Dests, d.c, d.s
	m := float64(lay.M)
	list := app.Dests.Destinations
	pages := destPages(app)
	if len(list) == 0 {
		pg := lay.SettingsPage
		_, bottom := lay.FormRows(1)
		cy := float64(pg.Y) + float64(bottom.Y-pg.Y)/2
		d.text(float64(pg.X)+float64(pg.W)/2, cy-6*s, dt.ListEmpty, 19*s, textOpt{col: cInk, bold: true, align: center,
			width: float64(pg.W) - 24*s})
		d.text(float64(pg.X)+float64(pg.W)/2, cy+22*s, dt.ListEmptySub, 15*s, textOpt{col: cQuiet, align: center,
			width: float64(pg.W) - 24*s})
	}
	for i, dst := range u.destShown(app) {
		key, ok := "s_dl_"+strconv.Itoa(i), "s_dlo_"+strconv.Itoa(i)
		r := lay.Buttons[key]
		q := rrOf(r, 10*s)
		if dst.Active {
			c.FillRR(q, cBrand.WithAlpha(0.24))
			c.StrokeRR(q, 2, cBrand)
		} else {
			c.FillRR(q, cPanel)
			c.StrokeRR(q, 1.25, cLine)
		}
		if d.pk != nil && d.pk.Kind == HitButton && d.pk.Key == key {
			d.lit(r, 10*s, 0.12)
		}
		if u.confirming("dl_act:" + dst.ID) {
			d.frameR(r, cWarn, 2, 10*s)
		}
		mid := float64(r.Y) + float64(r.H)/2
		icon, ic := "stream", cQuiet
		switch {
		case dst.Active:
			icon, ic = "check", cBrand
		case dst.Managed:
			icon = "cloud"
		}
		u.icon(c, icon, float64(r.X)+m+12*s, mid, 12*s, max(2.0, 2.6*s), ic)
		x0 := float64(r.X) + m + 34*s
		right := float64(lay.Buttons[ok].X) - m
		if dst.Managed { // the cloud's: read-only here (a padlock at the end of its title)
			u.icon(c, "lock", right-9*s, mid-8*s, 8*s, max(1.5, 2*s), cQuiet)
			right -= 24 * s
		}
		name := dst.Name
		if name == "" {
			name = u.templateName(dst.Template)
		}
		host := hostOf(dst.URL)
		if dst.URL == "" {
			host = tx.Stream.DestNone
		}
		parts := []string{ProtocolNames[dst.Protocol], host}
		subCol := cQuiet
		switch {
		case app.Pending == "dest_activate" && u.dl.actID == dst.ID:
			parts, subCol = []string{dt.Activating}, cWarn
		case dst.Active:
			parts = append([]string{dt.ActiveTag}, parts...)
		}
		if dst.Managed {
			parts = append(parts, dt.ManagedTag)
		}
		d.text(x0, mid-2*s, name, 19*s, textOpt{col: cInk, bold: dst.Active, width: right - x0})
		d.text(x0, mid+17*s, joinNonEmpty(parts, tx.Sep), 14*s, textOpt{col: subCol, width: float64(lay.Buttons[ok].X) - m - x0})
		d.button(ok, btn{icon: "next"})
	}
	// The bottom: ‹ ›, the note, «Add»
	left := float64(lay.SettingsPage.X)
	if pages > 1 {
		d.button("s_dl_prev", btn{icon: "back", dim: u.dl.page == 0})
		d.button("s_dl_next", btn{icon: "next", dim: u.dl.page >= pages-1})
		nb := lay.Buttons["s_dl_next"]
		left = float64(nb.X + nb.W)
	}
	d.button("s_dl_add", btn{label: "+ " + dt.Add, dim: !canAdd(app) || len(list) >= MaxDests})
	note, warn := u.destListNote(app, pages)
	u.bottomNoteIn(d, note, warn, left, float64(lay.Buttons["s_dl_add"].X))
}

// destListNote is the list's bottom line: a confirmation, a notice, a refusal, the pages, else how it works.
func (u *UI) destListNote(app *AppSnapshot, pages int) (string, bool) {
	tx := u.Tx
	if u.confirmingPrefix("dl_act:") {
		return tx.Dests.ActivateConfirm, true
	}
	if t, warn := u.pageNoteText(); t != "" {
		return t, warn
	}
	if t := u.resultNote(app, "dest_activate", "dest_delete", "dest_create"); t != "" {
		return t, true
	}
	if pages > 1 {
		return sprintf(tx.PageF, u.dl.page+1, pages), false
	}
	if len(app.Dests.Destinations) == 0 {
		return "", false
	}
	return tx.Dests.ListHint, false
}

func (u *UI) destListTouch(app *AppSnapshot, what string) bool {
	rowID := func(prefix string) (string, bool) {
		s, ok := strings.CutPrefix(what, prefix)
		if !ok {
			return "", false
		}
		shown := u.destShown(app)
		i, err := strconv.Atoi(s)
		if err != nil || i >= len(shown) {
			return "", true
		}
		return shown[i].ID, true
	}
	if id, ok := rowID("s_dlo_"); ok {
		if id != "" {
			u.openDest(id)
		}
		return true
	}
	if id, ok := rowID("s_dl_"); ok && id != "" {
		dst := app.Dests.ByID(id)
		switch {
		case dst == nil:
		case dst.Active: // the one in use: its page
			u.openDest(id)
		case app.Pending == "dest_activate":
		default:
			if u.streaming() && !u.confirmTap("dl_act:"+id) {
				return true
			}
			u.confirm = nil
			if u.app().Do(AppAction{What: "dest_activate", ID: id, Body: map[string]any{"apply": true}}) {
				u.dl.actID = id
			} else {
				u.appBusy(false)
			}
		}
		return true
	}
	switch what {
	case "s_dl_prev":
		u.dl.page = max(0, u.dl.page-1)
	case "s_dl_next":
		u.dl.page = min(destPages(app)-1, u.dl.page+1)
	case "s_dl_add":
		switch {
		case len(app.Dests.Destinations) >= MaxDests:
			u.notePage(sprintf(u.Tx.Dests.AddFullF, MaxDests), true)
		case canAdd(app):
			u.openTemplatePicker(app)
		}
	default:
		return false
	}
	return true
}

// openTemplatePicker: «Add» chooses the new destination's template (all but the cloud's), then opens its page.
func (u *UI) openTemplatePicker(app *AppSnapshot) {
	var opts []pickOpt
	for _, t := range app.Dests.Templates {
		if t.ID == "eirl" {
			continue
		}
		var protos []string
		for _, p := range t.Protocols {
			protos = append(protos, ProtocolNames[p])
		}
		sub := strings.Join(protos, u.Tx.Sep)
		if t.URL != "" {
			sub += u.Tx.Sep + u.Tx.Dests.TemplateSub
		}
		opts = append(opts, pickOpt{ID: t.ID, Title: u.templateName(t.ID), Sub: sub})
	}
	u.openPicker(u.Tx.Dests.AddTitle, "", opts, func(u *UI, id string) {
		app := u.app().AppSnapshot()
		if destsOn(&app) {
			u.openNewDest(&app, id)
		}
	})
}

// --- a destination's page ---

// deReadOnly: the cloud's destination (activated from the list, never edited here).
func deReadOnly(n *DestInfo) bool { return n.Managed }

// deEyes are the page's eyes and the secret each one shows.
var deEyes = map[string]string{"s_de_sid_eye": "streamId", "s_de_pass_eye": "passphrase", "s_de_rpass_eye": "passphrase",
	"s_de_key_eye": "key", "s_de_pw_eye": "password"}

func (u *UI) destEditorKeys(app *AppSnapshot) []string {
	keys := []string{"s_back"}
	n, ok := u.deNow(app)
	if !ok {
		return keys
	}
	ed := u.dl.ed
	ro := deReadOnly(&n)
	keys = append(keys, "s_de_sec_conn", "s_de_sec_video")
	if ed.section == "video" {
		if !ro {
			if len(destTemplateOf(app, &n).Codecs) > 1 {
				keys = append(keys, "s_de_codec")
			}
			keys = append(keys, "s_de_size", "s_de_vid_m", "s_de_vid_p", "s_de_abr", "s_de_fps", "s_de_aud_m", "s_de_aud_p")
			if n.ABR {
				keys = append(keys, "s_de_vmin_m", "s_de_vmin_p")
			}
		}
	} else {
		var eyes, fields []string
		switch n.Protocol {
		case "srt", "srtla":
			eyes = []string{"s_de_sid_eye", "s_de_pass_eye"}
			fields = []string{"s_de_sid", "s_de_pass", "s_de_lat_m", "s_de_lat_p"}
		case "rtmp", "rtmps":
			eyes, fields = []string{"s_de_key_eye"}, []string{"s_de_key"}
		case "rist":
			eyes = []string{"s_de_rpass_eye", "s_de_pw_eye"}
			fields = []string{"s_de_prof", "s_de_buf_m", "s_de_buf_p", "s_de_rpass", "s_de_user", "s_de_pw"}
			if n.RistProfile == "simple" { // no passphrase nor user in the simple profile (OUTPUTS.md §6)
				eyes, fields = nil, []string{"s_de_prof", "s_de_buf_m", "s_de_buf_p"}
			}
		}
		keys = append(keys, eyes...)
		if !ro {
			keys = append(keys, "s_de_name", "s_de_url")
			if len(destTemplateOf(app, &n).Protocols) > 1 {
				keys = append(keys, "s_de_proto")
			}
			keys = append(keys, fields...)
		}
	}
	if ro {
		return keys
	}
	return append(keys, u.deBottomKeys(app, &n)...)
}

// deBottomKeys are the bottom's buttons: «Save» (and «Save and apply» on the active one) with a draft; else «Apply»
// a saved change not applied (the active one), or «Delete» (a saved one not in use).
func (u *UI) deBottomKeys(app *AppSnapshot, n *DestInfo) []string {
	ed := u.dl.ed
	switch {
	case ed.dirty() && n.Active:
		return []string{"s_de_save", "s_de_apply"}
	case ed.dirty():
		return []string{"s_de_save1"}
	case n.Active && app.Dests.PendingRestart:
		return []string{"s_de_apply"}
	case !n.Active && ed.id != "":
		return []string{"s_de_delete"}
	}
	return nil
}

// deSecretLen is how many dots a secret draws with its eye closed: the typed one's length, the revealed one's, else
// 0 (secretText draws eight).
func (u *UI) deSecretLen(app *AppSnapshot, k string) int {
	if b := u.dl.ed.secrets[k]; b != nil {
		return len(b)
	}
	return len(u.deSaved(app, k))
}

// deSecretText is what a secret's eye shows: the typed one (not saved yet), else the saved one the app revealed (""
// while it has not answered).
func (u *UI) deSecretText(app *AppSnapshot, k string) string {
	if b := u.dl.ed.secrets[k]; b != nil {
		return string(b)
	}
	return u.deSaved(app, k)
}

// deSaved is a saved secret of the open destination as the app revealed it ("" none, or another's).
func (u *UI) deSaved(app *AppSnapshot, k string) string {
	ed := u.dl.ed
	s := app.DestSecrets
	if s == nil || ed.id == "" || s.ID != ed.id {
		return ""
	}
	return map[string]string{"streamId": s.StreamID, "passphrase": s.Passphrase, "key": s.Key, "password": s.Password}[k]
}

func (u *UI) drawDestEditor(d *dc, app *AppSnapshot) {
	tx, st, dt := u.Tx, u.Tx.Stream, u.Tx.Dests
	n, ok := u.deNow(app)
	if !ok {
		u.pageMessage(d, dt.Gone, "", false)
		return
	}
	u.deSync(app)
	if n2, ok := u.deNow(app); ok {
		n = n2
	} else {
		return
	}
	ed := u.dl.ed
	ro := deReadOnly(&n)
	fe := u.deFieldErrors(app)
	tpl := destTemplateOf(app, &n)
	d.button("s_de_sec_conn", btn{label: dt.SecConnection, active: ed.section != "video", size: 18})
	d.button("s_de_sec_video", btn{label: dt.SecVideo, active: ed.section == "video", size: 18})
	changed := func(k string) bool { _, c := ed.fields[k]; return c }
	if ed.section == "video" {
		u.drawDestVideo(d, &n, tpl, ro, fe, changed)
	} else {
		u.field(d, "s_de_name", dt.Name, n.Name, fieldOpt{chevron: !ro, dim: ro, changed: changed("name"), warn: fe["name"] != ""})
		u.field(d, "s_de_proto", dt.Protocol, ProtocolNames[n.Protocol], fieldOpt{chevron: !ro && len(tpl.Protocols) > 1,
			dim: ro || len(tpl.Protocols) < 2, changed: changed("protocol"), warn: fe["protocol"] != ""})
		url := n.URL
		if url == "" {
			url = tx.NotSet
			if n.URLInvalid && !changed("url") {
				url = st.DestURLInvalid
			}
		}
		u.field(d, "s_de_url", dt.Server, url, fieldOpt{chevron: !ro, dim: ro, changed: changed("url"), mono: n.URL != "",
			tail: true, placeholder: n.URL == "", warn: fe["url"] != ""})
		secret := func(key, eye, k, title string) {
			set := map[string]bool{"streamId": n.StreamIDSet, "passphrase": n.PassphraseSet, "key": n.KeySet,
				"password": n.PasswordSet}[k]
			shown := u.revealed(eye)
			val := tx.NotSet
			switch {
			case set && shown:
				if val = u.deSecretText(app, k); val == "" { // the app has not answered yet
					val = "…"
				}
			case set: // dots: only its length counts (the secret itself never becomes a string with the eye closed)
				val = strings.Repeat("·", u.deSecretLen(app, k))
			}
			u.field(d, key, title, val, fieldOpt{chevron: !ro, dim: ro, changed: ed.secrets[k] != nil || ed.clears[k],
				secret: true, shown: shown, set: set, warn: fe[k] != ""})
			u.eye(d, eye, shown)
		}
		switch n.Protocol {
		case "srt", "srtla":
			secret("s_de_sid", "s_de_sid_eye", "streamId", st.DestStreamID)
			secret("s_de_pass", "s_de_pass_eye", "passphrase", st.DestPassphrase)
			u.deStepper(d, "s_de_lat", st.DestLatency, n.LatencyMs, LatencyMin, LatencyMax, changed("latencyMs"), ro, false,
				fe["latencyMs"])
		case "rtmp", "rtmps":
			secret("s_de_key", "s_de_key_eye", "key", dt.Key)
		case "rist":
			prof := n.RistProfile
			if prof == "" {
				prof = "main"
			}
			u.field(d, "s_de_prof", dt.Profile, dt.ProfileNames[prof], fieldOpt{chevron: !ro, dim: ro,
				changed: changed("ristProfile"), warn: fe["ristProfile"] != ""})
			u.deStepper(d, "s_de_buf", dt.Buffer, n.LatencyMs, RistBufferMin, LatencyMax, changed("latencyMs"), ro, false,
				fe["latencyMs"])
			if prof == "simple" { // no passphrase nor user in the simple profile (OUTPUTS.md §6)
				break
			}
			secret("s_de_rpass", "s_de_rpass_eye", "passphrase", st.DestPassphrase)
			user := n.Username
			if user == "" {
				user = tx.NotSet
			}
			u.field(d, "s_de_user", dt.User, user, fieldOpt{chevron: !ro, dim: ro, changed: changed("username"),
				placeholder: n.Username == "", warn: fe["username"] != ""})
			secret("s_de_pw", "s_de_pw_eye", "password", dt.Password)
		}
	}
	// The bottom: the buttons, and the note at their left
	busy := app.Pending == "dest_update" || app.Pending == "dest_create" || app.Pending == "restart"
	lay := u.Lay
	left, right := float64(lay.SettingsPage.X), float64(lay.SettingsPage.X+lay.SettingsPage.W)
	if !ro {
		for _, k := range u.deBottomKeys(app, &n) {
			switch k {
			case "s_de_save", "s_de_save1":
				d.button(k, btn{label: tx.Save, dim: busy})
			case "s_de_apply":
				label, what := tx.SaveApply, "de_apply"
				if !ed.dirty() {
					label, what = tx.Apply, "de_restart"
				}
				col := cBrand
				if u.confirming(what) || !ed.dirty() {
					col = cWarn
				}
				if u.confirming(what) {
					label = tx.TapAgain
				}
				d.button(k, btn{label: label, active: true, dim: busy, col: colp(col)})
			case "s_de_delete":
				label := dt.Delete
				if u.confirming("de_delete") {
					label = tx.TapAgain
				}
				d.button(k, btn{label: label, col: colp(cBad), active: u.confirming("de_delete"),
					dim: app.Pending == "dest_delete"})
				r := lay.Buttons[k]
				left = float64(r.X + r.W)
				continue
			}
			right = min(right, float64(lay.Buttons[k].X))
		}
	}
	note, warn := u.deNote(app, &n, fe)
	u.bottomNoteIn(d, note, warn, left, right)
}

// drawDestVideo is the Video and audio section.
func (u *UI) drawDestVideo(d *dc, n *DestInfo, tpl DestTemplate, ro bool, fe map[string]string, changed func(string) bool) {
	st, dt := u.Tx.Stream, u.Tx.Dests
	codec := n.VideoCodec
	if codec == "" {
		codec = "h264"
	}
	u.field(d, "s_de_codec", dt.Codec, CodecNames[codec], fieldOpt{chevron: !ro && len(tpl.Codecs) > 1,
		dim: ro || len(tpl.Codecs) < 2, changed: changed("videoCodec"), warn: fe["videoCodec"] != ""})
	size := n.OutputSize
	if size == "" {
		size = "program"
	}
	u.field(d, "s_de_size", dt.Size, dt.SizeNames[size], fieldOpt{chevron: !ro, dim: ro, changed: changed("outputSize"),
		warn: fe["outputSize"] != ""})
	mv := maxVideo(tpl)
	u.deStepper(d, "s_de_vid", st.DestVideo, n.VideoKbps, VideoMin, mv, changed("videoBitrateKbps"), ro, false,
		fe["videoBitrateKbps"])
	u.deStepper(d, "s_de_vmin", st.DestVideoMin, n.VideoMinKbps, VideoMinMin, n.VideoKbps, changed("videoMinBitrateKbps"),
		ro, !n.ABR, fe["videoMinBitrateKbps"])
	abrSub := st.DestABROff
	if n.ABR {
		abrSub = st.DestABROn
	}
	kn := knob(n.ABR)
	if ro {
		kn = KnobNone
	}
	u.row(d, "s_de_abr", "stream", st.DestABR, RowState{kn, false, abrSub, "quiet"}, false, 0)
	if changed("adaptiveBitrate") {
		d.frameR(u.Lay.Buttons["s_de_abr"], cBrand, 2, 10*d.s)
	}
	fps := n.OutputFps
	if fps == "" {
		fps = "full"
	}
	u.field(d, "s_de_fps", dt.Fps, dt.FpsNames[fps], fieldOpt{chevron: !ro, dim: ro, changed: changed("outputFps"),
		warn: fe["outputFps"] != ""})
	u.stepperTitle(d, u.Lay.Buttons["s_de_aud"], "s_de_aud_m", st.DestAudio, fe["audioBitrateKbps"])
	if changed("audioBitrateKbps") {
		d.frameR(u.Lay.Buttons["s_de_aud"], cBrand, 2, 10*d.s)
	}
	u.stepper(d, "s_de_aud_m", "s_de_aud_p", itoa(n.AudioKbps), ro || n.AudioKbps <= AudioRates[0],
		ro || n.AudioKbps >= AudioRates[len(AudioRates)-1])
}

// deStepper draws a stepper field (its box, its title, − value +) inside [lo, hi].
func (u *UI) deStepper(d *dc, box, title string, v, lo, hi int, changed, ro, off bool, errText string) {
	u.stepperTitle(d, u.Lay.Buttons[box], box+"_m", title, errText)
	if changed {
		d.frameR(u.Lay.Buttons[box], cBrand, 2, 10*d.s)
	}
	u.stepper(d, box+"_m", box+"_p", itoa(v), ro || off || v <= lo, ro || off || v >= hi)
}

// deFieldErrors are the app's field errors of the last save of this page (field → message), while fresh.
func (u *UI) deFieldErrors(app *AppSnapshot) map[string]string {
	out := map[string]string{}
	l := app.Last
	ed := u.dl.ed
	if l == nil || l.OK || (l.What != "dest_update" && l.What != "dest_create") || !l.At.After(ed.savedAt) ||
		l.At.Before(u.pageSince) {
		return out
	}
	for k, f := range l.Fields {
		out[k] = f.Message
		if out[k] == "" {
			out[k] = sprintf(u.Tx.AppMessageF, f.Code)
		}
	}
	return out
}

// deNote is the page's bottom line.
func (u *UI) deNote(app *AppSnapshot, n *DestInfo, fe map[string]string) (string, bool) {
	tx, dt := u.Tx, u.Tx.Dests
	ed := u.dl.ed
	if deReadOnly(n) {
		return tx.ManagedByCloud, false
	}
	if t, warn := u.pageNoteText(); t != "" {
		return t, warn
	}
	switch {
	case u.confirming("de_apply") || u.confirming("de_restart"):
		return tx.ApplyCuts, true
	case u.confirming("de_delete"):
		return dt.DeleteConfirm, true
	}
	if t := u.resultNote(app, "dest_secrets"); t != "" { // an eye that could not be opened
		return t, true
	}
	l := app.Last
	fresh := l != nil && (l.What == "dest_update" || l.What == "restart") && l.At.After(ed.savedAt) &&
		!l.At.Before(u.pageSince) && u.now().Sub(l.At) < 8*time.Second
	switch {
	case len(fe) > 0:
		order := append(append([]string{"name", "protocol", "url"}, destSecretsOf(n.Protocol)...), "latencyMs",
			"ristProfile", "username", "videoCodec", "outputSize", "videoBitrateKbps", "videoMinBitrateKbps",
			"outputFps", "audioBitrateKbps")
		for _, k := range order {
			if fe[k] != "" {
				return fe[k], true
			}
		}
		keys := make([]string, 0, len(fe))
		for k := range fe {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return fe[keys[0]], true
	case l != nil && !l.OK && l.What == "dest_create" && l.At.After(ed.savedAt) && !l.At.Before(u.pageSince):
		return u.resultText(l), true
	case fresh && !l.OK:
		return u.resultText(l), true
	case fresh && l.What == "restart":
		return tx.SavedRestarting, false
	case fresh && l.Restarted != nil && *l.Restarted:
		return tx.SavedRestarting, false
	case fresh && ed.sentApply:
		return tx.SavedStopped, false
	case fresh && n.Active:
		return tx.SavedLater, false
	case fresh:
		return tx.Saved, false
	case ed.id == "":
		return dt.NewUnsaved, false
	case ed.edited():
		return tx.Unsaved, false
	case n.Active && app.Dests.PendingRestart:
		return tx.PendingApply, false
	}
	// RTMP is one TCP connection; RIST bonds the links like SRTLA (OUTPUTS.md §6)
	if (n.Protocol == "rtmp" || n.Protocol == "rtmps") && u.usableLinks() > 1 {
		return sprintf(dt.SingleLinkF, ProtocolNames[n.Protocol]), false
	}
	return "", false
}

// usableLinks is how many internet connections the header's dots count as usable (0: not known).
func (u *UI) usableLinks() int {
	dots, ok := u.linkDots()
	if !ok {
		return 0
	}
	n := 0
	for _, l := range dots {
		if !l.Disabled && l.Health != "off" {
			n++
		}
	}
	return n
}

// deSync: after a save the app answered OK. A new one goes back to the list (where a touch streams to it); a saved
// one's draft goes, if nothing was changed since it was sent.
func (u *UI) deSync(app *AppSnapshot) {
	ed := u.dl.ed
	l := app.Last
	if ed == nil || l == nil || !l.OK || (l.What != "dest_update" && l.What != "dest_create") ||
		!l.At.After(ed.savedAt) || ed.synced.Equal(l.At) || l.At.Before(u.pageSince) {
		return
	}
	ed.synced = l.At
	if l.What == "dest_create" {
		if ed.id == "" {
			u.closeDest()
			u.notePage(u.Tx.Dests.Created, false)
		}
		return
	}
	if !ed.dirtyAt.After(ed.sentAt) { // nothing edited since that save
		ed.clearDraft()
	}
	// What an eye showed is the old one (the client dropped it): asked again if an eye is open
	for eye := range deEyes {
		if u.revealed(eye) && app.Pending != "dest_secrets" {
			u.app().Do(AppAction{What: "dest_secrets", ID: ed.id})
			break
		}
	}
}

// deSave sends the draft. A new one: its template and every field its protocol uses (what the page showed); a saved
// one: only what changed. The typed secrets go as secrets (Do copies them); the draft keeps its own until the app
// saves them (deSync), so a refused save does not make the user type a long key again.
func (u *UI) deSave(apply bool) {
	app := u.app().AppSnapshot()
	ed := u.dl.ed
	n, ok := u.deNow(&app)
	if ed == nil || !ok || !ed.dirty() || app.Pending == "dest_update" || app.Pending == "dest_create" {
		return
	}
	body := map[string]any{}
	sec := map[string][]byte{}
	for _, k := range destSecretsOf(n.Protocol) {
		switch {
		case ed.clears[k]:
			body[k] = nil
		case ed.secrets[k] != nil:
			sec[k] = ed.secrets[k]
		}
	}
	a := AppAction{What: "dest_update", ID: ed.id, Body: body, Secrets: sec}
	if ed.id == "" {
		a.What = "dest_create"
		body["template"] = ed.tmpl
		for _, k := range destFieldsOf(n.Protocol) {
			v, _ := destField(&n, k)
			body[k] = v
		}
		apply = false
	} else {
		for k, v := range ed.fields {
			body[k] = v
		}
		body["apply"] = apply
	}
	if url, ok := body["url"].(string); ok {
		body["url"] = strings.TrimSpace(url)
	}
	ed.savedAt, ed.sentAt, ed.sentApply = u.now().Add(-time.Millisecond), u.now(), apply
	u.appBusy(u.app().Do(a))
}

func (u *UI) destEditorTouch(app *AppSnapshot, what string) bool {
	ed := u.dl.ed
	if what == "s_back" {
		u.closeDest()
		return true
	}
	n, ok := u.deNow(app)
	if !ok {
		return false
	}
	st, dt := u.Tx.Stream, u.Tx.Dests
	tpl := destTemplateOf(app, &n)
	if k, eye := deEyes[what]; eye {
		// Opened: the saved ones, asked if the app has not given them (or they were dropped, or are another's)
		s := app.DestSecrets
		if u.toggleReveal(what) && ed.id != "" && (s == nil || s.ID != ed.id) && app.Pending != "dest_secrets" &&
			ed.secrets[k] == nil {
			u.appBusy(u.app().Do(AppAction{What: "dest_secrets", ID: ed.id}))
		}
		return true
	}
	secretKB := func(k, title, help string, maxLen int, allow func(rune) bool, check func([]byte) string, wasSet bool) {
		u.openSpecKeyboard(&kbSpec{title: title, help: help, secret: true, max: maxLen, allow: allow,
			done: func(u *UI, _ string, b []byte) string {
				if check != nil && len(b) > 0 {
					if e := check(b); e != "" {
						return e
					}
				}
				u.deSecret(k, b, wasSet)
				return ""
			}}, "")
	}
	passCheck := func(b []byte) string {
		if len(b) < PassphraseMin || len(b) > PassphraseMax {
			return sprintf(st.DestPassphraseBadF, PassphraseMin, PassphraseMax)
		}
		return ""
	}
	cur := func(k string) bool { // the saved one is set (a typed empty removes it)
		if b := u.deBase(app); b != nil {
			return map[string]bool{"streamId": b.StreamIDSet, "passphrase": b.PassphraseSet, "key": b.KeySet,
				"password": b.PasswordSet}[k]
		}
		return false
	}
	switch what {
	case "s_de_sec_conn":
		ed.section = "conn"
	case "s_de_sec_video":
		ed.section = "video"
	case "s_de_name":
		u.openSpecKeyboard(&kbSpec{title: dt.Name, help: dt.NameHelp, max: 32, min: 1,
			done: func(u *UI, t string, _ []byte) string {
				a := u.app().AppSnapshot()
				u.deSet(&a, "name", strings.TrimSpace(t))
				return ""
			}}, n.Name)
	case "s_de_url":
		u.openSpecKeyboard(&kbSpec{title: dt.Server, help: dt.ServerHelp[n.Protocol], max: DestURLMax,
			done: func(u *UI, t string, _ []byte) string {
				a := u.app().AppSnapshot()
				u.deSet(&a, "url", strings.TrimSpace(t))
				return ""
			}}, n.URL)
	case "s_de_proto":
		var opts []pickOpt
		for _, p := range tpl.Protocols {
			opts = append(opts, pickOpt{ID: p, Title: ProtocolNames[p], Sub: dt.ProtocolSub[p]})
		}
		from := n
		u.openPicker(dt.Protocol, n.Protocol, opts, func(u *UI, p string) {
			if p == from.Protocol {
				return
			}
			a := u.app().AppSnapshot()
			u.deSet(&a, "protocol", p)
			u.deSet(&a, "url", adaptURL(from.URL, from.Protocol, p, tpl))
			if (p == "rist") != (from.Protocol == "rist") { // the latency's meaning changes: its default
				u.deSet(&a, "latencyMs", defaultLatency(p))
			}
		})
	case "s_de_sid":
		secretKB("streamId", st.DestStreamID, st.DestStreamIDHelp, StreamIDMax, nil, nil, cur("streamId"))
	case "s_de_pass":
		secretKB("passphrase", st.DestPassphrase, st.DestPassphraseHelp, PassphraseMax, nil, passCheck, cur("passphrase"))
	case "s_de_rpass":
		secretKB("passphrase", st.DestPassphrase, st.DestPassphraseHelp, PassphraseMax, allowRe(reRistPassChar), passCheck,
			cur("passphrase"))
	case "s_de_key":
		secretKB("key", dt.Key, dt.KeyHelp, StreamIDMax, allowRe(reKeyChar), nil, cur("key"))
	case "s_de_pw":
		secretKB("password", dt.Password, dt.PasswordHelp, 128, allowRe(reRistPassChar), nil, cur("password"))
	case "s_de_user":
		u.openSpecKeyboard(&kbSpec{title: dt.User, help: dt.UserHelp, max: 64, allow: allowRe(reRistUserChar),
			done: func(u *UI, t string, _ []byte) string {
				a := u.app().AppSnapshot()
				u.deSet(&a, "username", t)
				return ""
			}}, n.Username)
	case "s_de_prof":
		cur := n.RistProfile
		if cur == "" {
			cur = "main"
		}
		var opts []pickOpt
		for _, p := range []string{"main", "simple"} {
			opts = append(opts, pickOpt{ID: p, Title: dt.ProfileNames[p], Sub: dt.ProfileSub[p]})
		}
		u.openPicker(dt.Profile, cur, opts, func(u *UI, p string) {
			a := u.app().AppSnapshot()
			u.deSet(&a, "ristProfile", p)
		})
	case "s_de_lat_m", "s_de_lat_p":
		u.deSet(app, "latencyMs", stepIn(n.LatencyMs, LatencyStep, LatencyMin, LatencyMax, dir(what)))
	case "s_de_buf_m", "s_de_buf_p":
		u.deSet(app, "latencyMs", stepIn(n.LatencyMs, LatencyStep, RistBufferMin, LatencyMax, dir(what)))
	case "s_de_codec":
		var opts []pickOpt
		for _, c := range tpl.Codecs {
			opts = append(opts, pickOpt{ID: c, Title: CodecNames[c], Sub: dt.CodecSub[c]})
		}
		u.openPicker(dt.Codec, n.VideoCodec, opts, func(u *UI, c string) {
			a := u.app().AppSnapshot()
			u.deSet(&a, "videoCodec", c)
		})
	case "s_de_size":
		cur := n.OutputSize
		if cur == "" {
			cur = "program"
		}
		var opts []pickOpt
		for _, z := range []string{"program", "1280x720", "960x540"} {
			opts = append(opts, pickOpt{ID: z, Title: dt.SizeNames[z]})
		}
		u.openPicker(dt.Size, cur, opts, func(u *UI, z string) {
			if z == "program" {
				z = ""
			}
			a := u.app().AppSnapshot()
			u.deSet(&a, "outputSize", z)
		})
	case "s_de_fps":
		cur := n.OutputFps
		if cur == "" {
			cur = "full"
		}
		var opts []pickOpt
		for _, f := range []string{"full", "half"} {
			opts = append(opts, pickOpt{ID: f, Title: dt.FpsNames[f]})
		}
		u.openPicker(dt.Fps, cur, opts, func(u *UI, f string) {
			a := u.app().AppSnapshot()
			u.deSet(&a, "outputFps", f)
		})
	case "s_de_vid_m", "s_de_vid_p":
		v := stepIn(n.VideoKbps, VideoStep, VideoMin, maxVideo(tpl), dir(what))
		u.deSet(app, "videoBitrateKbps", v)
		if n.ABR && n.VideoMinKbps > v { // the minimum never above the maximum
			u.deSet(app, "videoMinBitrateKbps", v)
		}
	case "s_de_vmin_m", "s_de_vmin_p":
		u.deSet(app, "videoMinBitrateKbps", stepIn(n.VideoMinKbps, VideoMinStep, VideoMinMin, n.VideoKbps, dir(what)))
	case "s_de_aud_m", "s_de_aud_p":
		u.deSet(app, "audioBitrateKbps", stepList(n.AudioKbps, AudioRates, dir(what)))
	case "s_de_abr":
		u.deSet(app, "adaptiveBitrate", !n.ABR)
	case "s_de_save", "s_de_save1":
		u.deSave(false)
	case "s_de_apply":
		switch {
		case ed.dirty():
			if u.streaming() && !u.confirmTap("de_apply") {
				return true
			}
			u.confirm = nil
			u.deSave(true)
		case app.Dests.PendingRestart:
			if u.streaming() && !u.confirmTap("de_restart") {
				return true
			}
			u.confirm = nil
			ed.savedAt = u.now().Add(-time.Millisecond)
			u.appBusy(u.app().Do(AppAction{What: "restart"}))
		}
	case "s_de_delete":
		if !u.confirmTap("de_delete") {
			return true
		}
		id := ed.id
		u.closeDest()
		u.appBusy(u.app().Do(AppAction{What: "dest_delete", ID: id}))
	default:
		return false
	}
	u.Dirty = true
	return true
}

// --- the page's entry points (dest.go's page) ---

func (u *UI) drawDests(d *dc, app *AppSnapshot) {
	if u.dl.ed != nil {
		u.drawDestEditor(d, app)
		return
	}
	u.drawDestList(d, app)
}

func (u *UI) destsKeys(app *AppSnapshot) []string {
	if u.dl.ed != nil {
		return u.destEditorKeys(app)
	}
	return u.destListKeys(app)
}

func (u *UI) destsTouch(what string) bool {
	app := u.app().AppSnapshot()
	if !destsOn(&app) {
		return false
	}
	if u.dl.ed != nil {
		return u.destEditorTouch(&app, what)
	}
	return u.destListTouch(&app, what)
}

// Preview (ui-render): a saved destination's page on a section, or the template picker.

// OpenDest shows a saved destination's page on a section (conn | video).
func (p Preview) OpenDest(id, section string) {
	p.u.openDest(id)
	p.u.dl.ed.section = section
}

// AddDest opens «Add»'s template picker.
func (p Preview) AddDest() {
	app := p.u.app().AppSnapshot()
	if destsOn(&app) {
		p.u.openTemplatePicker(&app)
	}
}

// NewDest shows a new destination's page of a template (as if picked after «Add»).
func (p Preview) NewDest(tmpl string) {
	app := p.u.app().AppSnapshot()
	if destsOn(&app) {
		p.u.openNewDest(&app, tmpl)
	}
}
