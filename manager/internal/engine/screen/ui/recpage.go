package ui

import (
	"strconv"
	"strings"
	"time"
)

// The Recording page (Stream › Recording, RECORDING.md §6): the web's Recording card without REC (that is the main
// screen's button). The mode and the quality (PUT /recording; the quality cannot change while recording), and the
// disk: its name, filesystem, room and speed, the web's warnings, Eject (POST /storage/eject) and Format (POST
// /storage/format with the seq of the disk the question named, behind a question that names it and says everything
// on it is erased). Turning the recording off while it records is asked first too. The settings and the disk are the
// app's (GET /recording, every ~2 s while the page is on view); what the engine does now (recording, the room left),
// its state's.

// recPage is the page's own state: the question on view (format | off, "" none) and the disk it names.
type recPage struct {
	ask string
	seq int64
	who string
}

// RecModes and RecQualities are the closed lists of REC_MODE and REC_QUALITY, in the order the pickers show them.
var (
	RecModes     = []string{"off", "manual", "with_output"}
	RecQualities = []string{"standard", "high", "max", "efficient"}
)

// fsNames are the filesystems by their name; another is shown as it comes.
var fsNames = map[string]string{"exfat": "exFAT", "fat32": "FAT32", "fat16": "FAT16", "fat12": "FAT12", "ext4": "ext4",
	"ntfs": "NTFS"}

func init() {
	registerPage("recording", &pageDef{
		layout: func(l *Layout) {
			rows, bottom := l.FormRows(5)
			l.Buttons["s_rec_mode"] = rows[0]
			l.Buttons["s_rec_quality"] = rows[1]
			bw := max(180, bottom.W/4)
			l.Buttons["s_rec_format"] = Rect{bottom.X + bottom.W - bw, bottom.Y, bw, bottom.H}
			l.Buttons["s_rec_eject"] = Rect{bottom.X + bottom.W - 2*bw - l.M, bottom.Y, bw, bottom.H}
			// The question: its answer at the right (wider: «Borrar y formatear»), «Cancel» at its left
			yw := max(260, bottom.W*2/5)
			l.Buttons["s_rec_yes"] = Rect{bottom.X + bottom.W - yw, bottom.Y, yw, bottom.H}
			l.Buttons["s_rec_no"] = Rect{bottom.X + bottom.W - yw - l.M - bw, bottom.Y, bw, bottom.H}
		},
		draw:  (*UI).drawRecPage,
		keys:  (*UI).recKeys,
		touch: (*UI).recTouch,
		watch: func(_ *UI, w *AppWatch) { w.Recording = true },
		leave: func(u *UI) { u.rp = recPage{} },
	})
}

// recDisk is the disk as the page shows it: the app's (its name, model, speed, seq) under the engine's live figures
// (its state, the room left). While the app's is behind (another state: it is asked again), only the engine's.
type recDisk struct {
	StorageInfo
	FreeBytes   int64
	MinutesLeft *int
}

func (u *UI) recDisk(app *AppSnapshot) recDisk {
	var d recDisk
	if app.Recording != nil {
		d.StorageInfo = app.Recording.Storage
	}
	r := u.st.Record
	if r == nil || r.Disk.State == "" {
		return d
	}
	live := r.Disk
	if live.State != d.State {
		d = recDisk{StorageInfo: StorageInfo{State: live.State, Reason: live.Reason, FS: live.FS, Label: live.Label,
			Seq: d.Seq, Dirty: live.Dirty}}
		if live.SizeBytes != nil {
			d.SizeBytes = *live.SizeBytes
		}
	}
	if live.FreeBytes != nil {
		d.FreeBytes = *live.FreeBytes
	}
	d.MinutesLeft = live.MinutesLeft
	d.Dirty = d.Dirty || live.Dirty
	return d
}

// tiny: a disk under 1 GB is not worth formatting (it would still be too small); locked: a write-protected one cannot be.
func (d *recDisk) tiny() bool {
	return d.State == "unsupported" && d.Reason == "too_small" && d.SizeBytes > 0 && d.SizeBytes < 1e9
}
func (d *recDisk) locked() bool { return d.ReadOnly || (d.State == "error" && d.Reason == "readonly") }

// canEject and canFormat: their buttons are on the page (they wait while recording or busy).
func (d *recDisk) canEject() bool { return d.State == "ready" }
func (d *recDisk) canFormat() bool {
	return d.State != "none" && d.State != "checking" && d.State != "" && !d.tiny() && !d.locked()
}

func (u *UI) recKeys(app *AppSnapshot) []string {
	if app.Recording == nil {
		return nil
	}
	if u.rp.ask != "" {
		return []string{"s_rec_no", "s_rec_yes"}
	}
	keys := []string{"s_rec_mode", "s_rec_quality"}
	d := u.recDisk(app)
	if d.canEject() {
		keys = append(keys, "s_rec_eject")
	}
	if d.canFormat() {
		keys = append(keys, "s_rec_format")
	}
	return keys
}

// recBusy: an action of the page under way (or queued).
func recBusy(app *AppSnapshot) bool {
	return strings.HasPrefix(app.Pending, "rec_")
}

// recQualityName and recQualityRate: «Alta»; «25 Mbps · 11,4 GB/h» (the codec first when it is not H.264).
func (u *UI) recQualityName(id string) string {
	if n, ok := u.Tx.Rec.Qualities[id]; ok {
		return n
	}
	return id
}

func (u *UI) recQualityRate(q *RecQuality) string {
	tx := &u.Tx.Rec
	rate := sprintf(tx.RateF, (q.VideoKbps+500)/1000)
	if q.Codec != "" && q.Codec != "h264" {
		if c := CodecNames[q.Codec]; c != "" {
			rate = c + " " + rate
		}
	}
	gb := float64(q.VideoKbps+q.AudioKbps) * 3600 / 8 / 1e6
	gbs := strings.ReplaceAll(strconv.FormatFloat(gb, 'f', 1, 64), ".", u.Tx.Decimal)
	return rate + u.Tx.Sep + sprintf(tx.GBhF, gbs)
}

// recQualityLabel is the quality as its field says it: «Alta · 25 Mbps · 11,4 GB/h».
func (u *UI) recQualityLabel(info *RecordingInfo, id string) string {
	if q := info.QualityOf(id); q != nil {
		return u.recQualityName(id) + u.Tx.Sep + u.recQualityRate(q)
	}
	return u.recQualityName(id)
}

func (u *UI) diskName(d *recDisk) string {
	switch {
	case d.Label != "":
		return d.Label
	case d.Model != "":
		return d.Model
	}
	return u.Tx.Rec.Unnamed
}

// diskWho is the disk in the format question: «EIRL-REC» (DataTraveler 3.0 · 62 GB).
func (u *UI) diskWho(d *recDisk) string {
	name := sprintf(u.Tx.Rec.QuotedF, u.diskName(d))
	model := ""
	if d.Label != "" {
		model = d.Model
	}
	if more := joinNonEmpty([]string{model, diskSize(d.SizeBytes, u.Tx)}, u.Tx.Sep); more != "" {
		return name + " (" + more + ")"
	}
	return name
}

func (u *UI) fsName(d *recDisk) string {
	if n, ok := fsNames[d.FS]; ok {
		return n
	}
	if d.FS == "" && d.State == "unsupported" && d.Reason == "fs_unknown" {
		return u.Tx.Rec.FSUnknown
	}
	return d.FS
}

// recWarnings are the disk's warnings, as the web's card says them.
func (u *UI) recWarnings(d *recDisk) []string {
	tx := &u.Tx.Rec
	var out []string
	warn := func(k string) { out = append(out, tx.Warns[k]) }
	switch d.State {
	case "unsupported":
		switch {
		case d.Reason == "fs_ntfs":
			warn("ntfs")
		case d.Reason == "too_small" && d.tiny():
			warn("tooSmall")
		case d.Reason == "too_small":
			warn("tooSmallPart")
		default:
			warn("unknownFs")
		}
	case "unformatted":
		warn("unformatted")
	case "error":
		switch d.Reason {
		case "readonly":
			warn("readonly")
		case "check_failed":
			warn("checkFailed")
		default:
			warn("mountFailed")
		}
	}
	if d.locked() && !(d.State == "error" && d.Reason == "readonly") {
		warn("readonly")
	}
	if d.State == "ready" && strings.HasPrefix(d.FS, "fat") {
		out = append(out, sprintf(tx.FatF, u.fsName(d)))
	}
	if d.Dirty {
		warn("dirty")
	}
	if d.State == "ready" && d.MinutesLeft != nil && *d.MinutesLeft >= 1 && *d.MinutesLeft < 10 {
		warn("low")
	}
	if d.AnotherDisk != "" {
		warn("another")
	}
	return out
}

// recAskStale: the question no longer applies (a recording started, the disk changed or went; turning off: the
// recording stopped). It goes by itself.
func (u *UI) recAskStale(app *AppSnapshot) bool {
	d := u.recDisk(app)
	switch u.rp.ask {
	case "format":
		return recOn(u.st.Record) || d.Seq != u.rp.seq || !d.canFormat()
	case "off":
		return !recOn(u.st.Record)
	}
	return false
}

func (u *UI) drawRecPage(d *dc, app *AppSnapshot) {
	if !u.devicePageReady(d, app.RecordingAPI, app.Recording != nil) {
		return
	}
	if u.rp.ask != "" && u.recAskStale(app) {
		u.rp = recPage{}
	}
	if u.rp.ask != "" {
		u.drawRecAsk(d)
		return
	}
	lay, tx := u.Lay, &u.Tx.Rec
	info := app.Recording
	on := recOn(u.st.Record)
	busy := recBusy(app)
	mode := info.Mode
	u.field(d, "s_rec_mode", tx.Mode, tx.Modes[mode], fieldOpt{sub: tx.ModeHints[mode], chevron: !busy, dim: busy})
	qsub := tx.QualityHint
	if on {
		qsub = tx.QualityLocked
	}
	u.field(d, "s_rec_quality", tx.Quality, u.recQualityLabel(info, info.Quality),
		fieldOpt{sub: qsub, chevron: !on && !busy, dim: on || busy})
	disk := u.recDisk(app)
	u.drawRecDisk(d, &disk)
	// The disk's buttons (waiting while recording or busy) and the page's note at their left
	right := float64(lay.SettingsPage.X + lay.SettingsPage.W)
	if disk.canFormat() {
		d.button("s_rec_format", btn{label: tx.Format, col: colp(cBad), dim: on || busy})
		right = float64(lay.Buttons["s_rec_format"].X)
	}
	if disk.canEject() {
		d.button("s_rec_eject", btn{label: tx.Eject, dim: on || busy})
		right = float64(lay.Buttons["s_rec_eject"].X)
	}
	u.bottomNote(d, u.recNote(app, &disk, on), u.recNoteWarn(app, on), right)
}

// recNote is the page's note: what is under way, how the last action went, why the disk's buttons wait.
func (u *UI) recNote(app *AppSnapshot, disk *recDisk, on bool) string {
	tx := &u.Tx.Rec
	switch app.Pending {
	case "rec_format":
		return tx.Formatting
	case "rec_eject":
		return tx.Ejecting
	}
	if t := u.resultNote(app, "rec_settings", "rec_eject", "rec_format"); t != "" {
		return t
	}
	if l := app.Last; l != nil && l.OK && u.now().Sub(l.At) <= 8*time.Second && !l.At.Before(u.pageSince) {
		switch l.What {
		case "rec_format":
			return tx.Formatted
		case "rec_eject":
			return tx.Ejected
		}
	}
	if t, _ := u.pageNoteText(); t != "" {
		return t
	}
	if on && (disk.canEject() || disk.canFormat()) {
		return tx.Warns["busyRec"]
	}
	return ""
}

func (u *UI) recNoteWarn(app *AppSnapshot, on bool) bool {
	if app.Pending == "rec_format" || app.Pending == "rec_eject" {
		return false
	}
	if u.resultNote(app, "rec_settings", "rec_eject", "rec_format") != "" {
		return true
	}
	if t, warn := u.pageNoteText(); t != "" {
		return warn
	}
	return false
}

// drawRecDisk is the disk, under the mode and the quality: why a recording stopped (if it did), the disk's state, its
// name, what it is and how long it can record, its warnings; or how to connect one.
func (u *UI) drawRecDisk(d *dc, disk *recDisk) {
	lay, tx, s := u.Lay, &u.Tx.Rec, d.s
	m := float64(lay.M)
	rows, bottom := lay.FormRows(5)
	top := float64(rows[2].Y)
	bot := float64(bottom.Y) - m
	pg := lay.SettingsPage
	x0 := float64(pg.X) + 4*s
	w := float64(pg.W) - 8*s
	y := top + 6*s
	line := func(t string, size float64, col Color, bold bool, gap float64) {
		style := Sans
		if bold {
			style = SansBold
		}
		for _, l := range d.wrap(t, size, style, w) {
			if y+size > bot { // what does not fit is left out
				return
			}
			y += size
			d.text(x0, y, l, size, textOpt{col: col, bold: bold, width: w})
			y += gap
		}
		y += 2 * s
	}
	// Why the recording stopped (the header says it only for a while)
	if r := u.st.Record; r != nil && r.State != "on" && r.State != "starting" && r.Reason != "" {
		col := cWarn
		if r.Reason == "disk_removed" {
			col = cBad
		}
		why := tx.Reasons[r.Reason]
		if why == "" {
			why = tx.Reasons["internal"]
		}
		line(why, 16*s, col, true, 5*s)
		if r.Mode == "on" {
			wait := tx.Retrying
			if r.Reason == "disk_removed" || r.Reason == "no_disk" {
				wait = tx.WaitDisk
			}
			line(wait, 14*s, cQuiet, false, 4*s)
		}
		y += 6 * s
	}
	// «Disco · Listo»
	state := tx.DiskStates[disk.State]
	if state == "" {
		state = tx.DiskStates["none"]
	}
	scol := cQuiet
	switch disk.State {
	case "ready":
		scol = cGood
	case "unsupported", "unformatted", "error":
		scol = cWarn
	}
	y += 15 * s
	adv := d.text(x0, y, tx.Disk+u.Tx.Sep, 15*s, textOpt{col: cQuiet})
	d.text(x0+adv, y, state, 15*s, textOpt{col: scol, bold: true, width: w - adv})
	y += 8 * s
	switch disk.State {
	case "none", "":
		line(tx.NoneHint, 15*s, cQuiet, false, 6*s)
		return
	case "ejected":
		line(tx.EjectedHint, 15*s, cQuiet, false, 6*s)
		return
	}
	// Its name (and model), then «exFAT · 41 GB libres de 62 GB · escribe a 31 MB/s», then how long it can record
	name := u.diskName(disk)
	y += 20 * s
	nadv := d.text(x0, y, name, 20*s, textOpt{col: cInk, bold: true, width: w})
	if disk.Label != "" && disk.Model != "" && nadv < w-60*s {
		d.text(x0+nadv+m, y, disk.Model, 15*s, textOpt{col: cQuiet, width: w - nadv - m})
	}
	y += 6 * s
	size := diskSize(disk.SizeBytes, u.Tx)
	free := ""
	if disk.State == "ready" {
		free = diskSize(disk.FreeBytes, u.Tx)
	}
	space := size
	switch {
	case free != "" && size != "":
		space = sprintf(tx.FreeOfF, free, size)
	case free != "":
		space = sprintf(tx.FreeF, free)
	}
	speed := ""
	if disk.WriteMBps > 0 {
		dec := 1
		if disk.WriteMBps >= 10 {
			dec = 0
		}
		speed = sprintf(tx.WritesF, strings.ReplaceAll(strconv.FormatFloat(disk.WriteMBps, 'f', dec, 64), ".",
			u.Tx.Decimal))
	}
	line(joinNonEmpty([]string{u.fsName(disk), space, speed}, u.Tx.Sep), 15*s, cQuiet, false, 5*s)
	if disk.State == "ready" && disk.MinutesLeft != nil && *disk.MinutesLeft >= 1 {
		q := ""
		if app := u.app().AppSnapshot(); app.Recording != nil {
			q = u.recQualityName(app.Recording.Quality)
		}
		line(sprintf(tx.TimeAtF, recSpan(*disk.MinutesLeft, tx), inSentence(q)), 15*s, cInk, false, 5*s)
	}
	// What to know before using it (or why it cannot be used)
	y += 4 * s
	for _, wt := range u.recWarnings(disk) {
		line(wt, 14*s, cWarn, false, 4*s)
	}
}

// drawRecAsk is a question of the page in its place: formatting (it names the disk and says everything on it is
// erased) or turning the recording off while it records.
func (u *UI) drawRecAsk(d *dc) {
	lay, tx, s := u.Lay, &u.Tx.Rec, d.s
	pg := lay.SettingsPage
	x0 := float64(pg.X) + 4*s
	w := float64(pg.W) - 8*s
	q, yes := tx.ModeOffAsk, tx.ModeOffYes
	if u.rp.ask == "format" {
		q, yes = sprintf(tx.FormatAskF, u.rp.who), tx.FormatYes
	}
	y := float64(pg.Y) + 40*s
	for _, l := range d.wrap(q, 20*s, SansBold, w) {
		d.text(x0, y, l, 20*s, textOpt{col: cInk, bold: true, width: w})
		y += 28 * s
	}
	d.button("s_rec_no", btn{label: u.Tx.Cancel})
	d.button("s_rec_yes", btn{label: yes, active: true, col: colp(cBad)})
}

func (u *UI) recTouch(what string) bool {
	app := u.app().AppSnapshot()
	info := app.Recording
	if info == nil {
		return false
	}
	tx := &u.Tx.Rec
	on := recOn(u.st.Record)
	busy := recBusy(&app)
	switch what {
	case "s_rec_mode":
		if busy {
			return true
		}
		opts := make([]pickOpt, 0, len(RecModes))
		for _, m := range RecModes {
			opts = append(opts, pickOpt{ID: m, Title: tx.Modes[m], Sub: tx.ModeHints[m]})
		}
		u.openPicker(tx.Mode, info.Mode, opts, func(u *UI, id string) { u.recSetMode(id) })
	case "s_rec_quality":
		switch {
		case on:
			u.notePage(tx.QualityLocked, true)
		case !busy:
			opts := make([]pickOpt, 0, len(info.Qualities))
			for i := range info.Qualities {
				q := &info.Qualities[i]
				opts = append(opts, pickOpt{ID: q.ID, Title: u.recQualityName(q.ID), Sub: u.recQualityRate(q)})
			}
			u.openPicker(tx.Quality, info.Quality, opts, func(u *UI, id string) { u.recSet("quality", id) })
		}
	case "s_rec_eject":
		disk := u.recDisk(&app)
		if on {
			u.notePage(tx.Warns["busyRec"], true)
		} else if disk.canEject() && !busy {
			u.appBusy(u.app().Do(AppAction{What: "rec_eject"}))
		}
	case "s_rec_format":
		disk := u.recDisk(&app)
		if on {
			u.notePage(tx.Warns["busyRec"], true)
		} else if disk.canFormat() && !busy {
			u.rp = recPage{ask: "format", seq: disk.Seq, who: u.diskWho(&disk)}
		}
	case "s_rec_no":
		u.rp = recPage{}
	case "s_rec_yes":
		ask := u.rp
		u.rp = recPage{}
		switch ask.ask {
		case "format":
			u.appBusy(u.app().Do(AppAction{What: "rec_format", Body: map[string]any{"seq": ask.seq}}))
		case "off":
			u.recSet("mode", "off")
		}
	default:
		return false
	}
	return true
}

// recSetMode: a mode chosen in its picker. Off while recording stops the recording: asked first.
func (u *UI) recSetMode(id string) {
	app := u.app().AppSnapshot()
	if app.Recording == nil || id == app.Recording.Mode {
		return
	}
	if id == "off" && recOn(u.st.Record) {
		u.rp = recPage{ask: "off"}
		u.Dirty = true
		return
	}
	u.recSet("mode", id)
}

// recSet saves the mode or the quality (PUT /recording); the same value sends nothing.
func (u *UI) recSet(field, id string) {
	app := u.app().AppSnapshot()
	if app.Recording == nil {
		return
	}
	if (field == "mode" && id == app.Recording.Mode) || (field == "quality" && id == app.Recording.Quality) {
		return
	}
	u.appBusy(u.app().Do(AppAction{What: "rec_settings", Body: map[string]any{field: id}}))
}
