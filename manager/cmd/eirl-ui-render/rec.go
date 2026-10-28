package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The FAKE local recording (RECORDING.md §6, MIXER.md §23): the engine's record object and the app's GET /recording.
//
// The main screen (rec-…; a disk «EIRL-REC», exFAT, 3 h 20 min left at High):
//
//	rec (Independent, a disk ready: REC at Go live's right) · rec-live (live and recording: the header's second line
//	«● REC 01:12:34 · 3 h 20 min libres», REC in red) · rec-only (recording without streaming) · rec-low (8 min
//	left: amber) · rec-slow (the disk cannot keep up) · rec-starting · rec-hold (REC held halfway) ·
//	rec-withoutput (With the stream: «EMITIR + GRABAR») · rec-withoutput-live (Stop stops both) · rec-nodisk (REC
//	disabled: «Conecta un disco USB») · rec-withoutput-nodisk (Go live streams and says it will not record) ·
//	rec-full (the disk full) · rec-powerlost (at start after a power cut: the notice) · rec-diskremoved (live, the disk
//	pulled out while recording: in red, waiting for it) · rec-refused (the engine said no to REC: the hint) ·
//	rec-plugwarn (a disk plugged in while live: the warning over the program) · rec-off (the recording off: no REC)
//
// Settings › Stream › Recording (settings-recording-…):
//
//	settings-recording (ready, idle) · settings-recording-on (recording: the quality locked, the disk's buttons wait) ·
//	settings-recording-fat32 (FAT32, not ejected properly, another disk: the warnings) · settings-recording-ntfs ·
//	settings-recording-unformatted · settings-recording-readonly · settings-recording-none (how to connect one) ·
//	settings-recording-ejected · settings-recording-format (the question that names the disk) ·
//	settings-recording-formatting (under way) · settings-recording-formatted · settings-recording-modes (the mode's
//	picker) · settings-recording-qualities (the quality's picker) · settings-recording-offask (off while recording) ·
//	settings-recording-powerlost (why the last recording stopped) · settings-recording-old (an app without the routes)

// recSeq is the fake disk's seq (the format question names it).
const recSeq = 7

// recVariant: a variant of the recording (the main screen's or its page).
func recVariant(variant string) bool {
	return variant == "rec" || strings.HasPrefix(variant, "rec-") || strings.HasPrefix(variant, "settings-recording")
}

// recState sets the engine's record object (and the output, live) for a recording variant.
func recState(variant string, st map[string]any) {
	if !recVariant(variant) {
		return
	}
	disk := m("state", "ready", "fs", "exfat", "label", "EIRL-REC", "sizeBytes", int64(62008590336),
		"freeBytes", int64(41_200_000_000), "minutesLeft", 200)
	r := m("mode", "off", "state", "off", "setting", "manual", "quality", "high", "by", "start", "seq", 0, "disk", disk)
	recording := func() {
		r["mode"], r["state"], r["by"], r["seq"] = "on", "on", "screen", 3
		r["elapsedS"], r["kbps"], r["droppedS"], r["file"] = 4354, 25280, 0, "2026-10-28_1730.mp4"
	}
	live := func() { st["output"] = m("mode", "on", "state", "on", "kbps", 5800, "sentKbps", 5800, "rttMs", 57) }
	noDisk := func() { r["disk"] = m("state", "none") }
	switch strings.TrimPrefix(variant, "settings-recording") {
	case "rec-live", "-on":
		live()
		recording()
	case "rec-only":
		recording()
	case "rec-plugwarn":
		live()
	case "rec-low":
		recording()
		disk["minutesLeft"], disk["freeBytes"] = 8, int64(1_520_000_000)
	case "rec-slow":
		live()
		recording()
		r["reason"], r["droppedS"] = "disk_slow", 3
	case "rec-starting":
		r["mode"], r["state"] = "on", "starting"
	case "rec-withoutput":
		r["setting"] = "with_output"
	case "rec-withoutput-live":
		r["setting"] = "with_output"
		live()
		recording()
		r["by"] = "output"
	case "rec-nodisk", "-none":
		noDisk()
	case "rec-withoutput-nodisk":
		r["setting"] = "with_output"
		noDisk()
	case "rec-full":
		disk["minutesLeft"], disk["freeBytes"] = 0, int64(40_000_000)
	case "rec-powerlost", "-powerlost":
		r["reason"] = "power_lost"
	case "rec-diskremoved":
		live()
		r["mode"], r["state"], r["reason"], r["seq"] = "on", "error", "disk_removed", 3
		noDisk()
	case "rec-off":
		r["setting"] = "off"
	case "-fat32":
		disk["fs"], disk["label"], disk["dirty"] = "fat32", "SANDISK", true
		disk["sizeBytes"], disk["freeBytes"], disk["minutesLeft"] = int64(31_000_000_000), int64(29_800_000_000), 145
	case "-ntfs":
		r["disk"] = m("state", "unsupported", "reason", "fs_ntfs", "fs", "ntfs", "label", "Seagate Backup",
			"sizeBytes", int64(1_000_204_886_016))
	case "-unformatted":
		r["disk"] = m("state", "unformatted", "sizeBytes", int64(128_043_712_512))
	case "-readonly":
		r["disk"] = m("state", "error", "reason", "readonly", "fs", "exfat", "label", "EIRL-REC", "sizeBytes",
			int64(62008590336))
	case "-ejected":
		r["disk"] = m("state", "ejected")
	case "-offask":
		recording()
	}
	st["record"] = r
}

// recApp is the app's GET /recording for the page's variants: what the engine's object says, with the disk's model,
// speed and seq.
func recApp(variant string, st map[string]any, snap *ui.AppSnapshot, now time.Time) {
	if !strings.HasPrefix(variant, "settings-recording") {
		return
	}
	if variant == "settings-recording-old" {
		snap.RecordingAPI = ui.No
		return
	}
	r := st["record"].(map[string]any)
	b, _ := json.Marshal(r["disk"])
	var disk struct {
		State, Reason, FS, Label string
		SizeBytes                int64
		Dirty                    bool
	}
	json.Unmarshal(b, &disk)
	storage := ui.StorageInfo{State: disk.State, Reason: disk.Reason, Seq: recSeq, FS: disk.FS, Label: disk.Label,
		SizeBytes: disk.SizeBytes, Dirty: disk.Dirty}
	if disk.State != "none" && disk.State != "ejected" {
		storage.Device, storage.Model = "sda", "DataTraveler 3.0"
	}
	switch variant {
	case "settings-recording", "settings-recording-on", "settings-recording-format", "settings-recording-formatting",
		"settings-recording-formatted", "settings-recording-modes", "settings-recording-qualities",
		"settings-recording-offask", "settings-recording-powerlost":
		storage.WriteMBps = 31.2
	case "settings-recording-fat32":
		storage.Model, storage.WriteMBps, storage.AnotherDisk = "Ultra Fit", 18.4, "sdb"
	case "settings-recording-ntfs":
		storage.Model = "Expansion HDD"
	case "settings-recording-unformatted":
		storage.Model = "Portable SSD T7"
	case "settings-recording-readonly":
		storage.ReadOnly = true
	}
	snap.Recording, snap.RecordingAPI = &ui.RecordingInfo{Mode: r["setting"].(string), Quality: r["quality"].(string),
		Qualities: []ui.RecQuality{{ID: "standard", Codec: "h264", VideoKbps: 15000, AudioKbps: 192},
			{ID: "high", Codec: "h264", VideoKbps: 25000, AudioKbps: 256},
			{ID: "max", Codec: "h264", VideoKbps: 40000, AudioKbps: 256},
			{ID: "efficient", Codec: "h265", VideoKbps: 20000, AudioKbps: 256}},
		Storage: storage}, ui.Yes
	switch variant {
	case "settings-recording-formatting":
		snap.Pending = "rec_format"
	case "settings-recording-formatted":
		snap.Last = &ui.AppResult{What: "rec_format", OK: true, At: now}
	}
}

// recPreview sets what a variant needs on the screen itself: a held REC, a refusal, a disk plugged in while live, the
// page's questions and pickers.
func recPreview(u *ui.UI, p ui.Preview, variant string, status *ui.Status) {
	switch variant {
	case "rec-hold":
		p.Holding("rec", 500*time.Millisecond)
	case "rec-refused":
		u.CommandDone(ui.Command{Msg: m("cmd", "record", "on", true, "by", "screen"), Origin: "rec"}, false, "disk_full")
	case "rec-plugwarn": // live, and the disk was not there a moment ago
		before := *status
		r := *status.Record
		r.Disk = ui.RecordDisk{State: "none"}
		before.Record = &r
		u.SetStatus(&before)
		u.SetStatus(status)
	case "settings-recording-format":
		p.RecAsk("format")
	case "settings-recording-offask":
		p.RecAsk("off")
	case "settings-recording-modes":
		p.Touch("s_rec_mode")
	case "settings-recording-qualities":
		p.Touch("s_rec_quality")
	}
}
