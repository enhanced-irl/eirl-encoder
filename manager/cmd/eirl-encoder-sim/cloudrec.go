package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

// --- the local recording as the cloud sees it (cloud mode; RECORDING.md §6.8, ENCODER_CLOUD.md §14) ----------------
//
// -recording sets REC_MODE (off, manual or with_output) and announces the recording capability; without it the
// simulated device is one without the recording. The disk is a word in a control file next to -state
// (<state>.disk), read every second; without the file, -rec-disk:
//
//	echo ready > /tmp/sim1.json.disk     a 64 GB disk ready to record (~5 h at 25 Mbps)
//	echo low > /tmp/sim1.json.disk       ready with 3 minutes of room: the warning, then disk_full
//	echo full > /tmp/sim1.json.disk      no room: REC answers disk_full
//	echo none > /tmp/sim1.json.disk      pulled out (while recording: disk_removed, and REC stays on)
//
// Also checking, unsupported, unformatted, error and ejected, as the storage helper says them. REC from the app
// (record_start, record_stop) and, with with_output, Go live and Stop streaming start and stop it like the engine.

// simRecRoom: the room of a ready disk, in minutes at the high quality (64 GB at 25 Mbps + 256 kbps audio)
const simRecRoom = 326

// simRecDisks: the words the disk can be: the storage helper's states, plus low and full (ready with little or none
// room). Anything else would reach the app as a disk state the cloud drops, with the whole block.
var simRecDisks = map[string]bool{"none": true, "checking": true, "ready": true, "unsupported": true, "unformatted": true,
	"error": true, "ejected": true, "low": true, "full": true}

type simRec struct {
	setting string
	ctl     string // the control file; "" = only -rec-disk
	log     *slog.Logger
	badCtl  string // the last bad word logged

	mu        sync.Mutex
	disk      string // the storage helper's state, or low / full (ready with little or no room)
	on        bool   // REC asked for
	byOutput  bool   // Go live started it (with_output): Stop streaming stops it
	since     time.Time
	used      time.Duration // what the recordings on this disk took, before the current one
	reason    string
	lowSince  time.Time
	lastWrite time.Time
}

func newSimRec(setting, disk, ctl string, powerLost bool, log *slog.Logger) (*simRec, error) {
	if disk == "" {
		disk = "ready"
	}
	if !simRecDisks[disk] {
		return nil, fmt.Errorf("disk %q: none, checking, ready, low, full, unsupported, unformatted, error or ejected", disk)
	}
	r := &simRec{setting: setting, disk: disk, ctl: ctl, log: log}
	if powerLost {
		r.reason = "power_lost"
	}
	return r, nil
}

// poll reads the control file (every second, from the device's run).
func (r *simRec) poll(now time.Time) {
	if r.ctl == "" {
		return
	}
	b, err := os.ReadFile(r.ctl)
	if err != nil {
		return
	}
	f := strings.Fields(strings.ToLower(string(b)))
	if len(f) == 0 {
		return
	}
	if !simRecDisks[f[0]] {
		if f[0] != r.badCtl && r.log != nil {
			r.log.Warn("sim: disk word not known, left as it was", "file", r.ctl, "word", f[0])
		}
		r.badCtl = f[0]
		return
	}
	r.setDisk(f[0], now)
}

func (r *simRec) setDisk(disk string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if disk == r.disk {
		return
	}
	wasReady := r.readyLocked()
	r.disk = disk
	if disk == "low" {
		r.lowSince = now
	}
	if r.recordingLocked() && wasReady && !r.readyLocked() {
		r.stopFileLocked(now, "disk_removed") // REC stays on: a new file when a disk is ready again
		return
	}
	if r.on && r.since.IsZero() && r.readyLocked() {
		r.used, r.reason, r.since = 0, "", now // another disk: a new file
	}
}

func (r *simRec) readyLocked() bool { return r.disk == "ready" || r.disk == "low" || r.disk == "full" }

func (r *simRec) recordingLocked() bool { return r.on && !r.since.IsZero() }

// minutesLeftLocked: the room left at the quality's bitrate.
func (r *simRec) minutesLeftLocked(now time.Time) int {
	room := simRecRoom
	switch r.disk {
	case "low":
		room = 3
	case "full":
		room = 0
	}
	used := r.used
	if !r.since.IsZero() {
		used += now.Sub(r.since)
	}
	if r.disk == "low" && !r.lowSince.IsZero() {
		used = 0
		if !r.since.IsZero() {
			used = now.Sub(maxTime(r.since, r.lowSince))
		}
	}
	return max(0, room-int(used/time.Minute))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (r *simRec) stopFileLocked(now time.Time, reason string) {
	if !r.since.IsZero() {
		r.used += now.Sub(r.since)
	}
	r.since, r.reason = time.Time{}, reason
}

// set: the engine's record command (setRecord): the same refusals.
func (r *simRec) set(on bool, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on && r.setting == "off" {
		return &cloud.ActionError{Code: "disabled", Message: i18n.Msg(i18n.EN, "record.disabled")}
	}
	if on && !r.recordingLocked() && !r.readyLocked() {
		return &cloud.ActionError{Code: "no_disk", Message: i18n.Msg(i18n.EN, "record.no_disk")}
	}
	if on && !r.recordingLocked() && r.minutesLeftLocked(now) < 1 {
		return &cloud.ActionError{Code: "disk_full", Message: i18n.Msg(i18n.EN, "record.disk_full")}
	}
	r.toggleLocked(on, now)
	r.byOutput = false
	return nil
}

func (r *simRec) toggleLocked(on bool, now time.Time) {
	switch {
	case on && !r.on:
		r.on, r.reason = true, ""
		switch {
		case !r.readyLocked():
			r.reason = "no_disk" // it waits for a disk
		case r.minutesLeftLocked(now) < 1:
			r.reason = "disk_full"
		default:
			r.since = now
		}
	case !on && r.on:
		r.stopFileLocked(now, "")
		r.on = false
	}
}

// engineStopped: the engine stops (a restart): the recording stops with it and is not resumed.
func (r *simRec) engineStopped(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toggleLocked(false, now)
	r.byOutput = false
}

// output: Go live and Stop streaming with with_output (recordWithOutput): Go live starts it, also without a disk
// (it waits for one and says no_disk; the stream goes on regardless); Stop streaming stops only the one Go live
// started. An engine that starts on air does not (D6).
func (r *simRec) output(on bool, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setting != "with_output" {
		return
	}
	switch {
	case on && !r.on:
		r.toggleLocked(true, now)
		r.byOutput = true
	case !on && r.on && r.byOutput:
		r.toggleLocked(false, now)
		r.byOutput = false
	}
}

// status: status.recording, and whether it changed since the last call (the device then leaves a journal line).
func (r *simRec) status(now time.Time) *cloud.StatusRecording {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordingLocked() && r.minutesLeftLocked(now) < 1 {
		r.stopFileLocked(now, "disk_full") // D7: it stops and says why
		r.on = false
	}
	st := &cloud.StatusRecording{Setting: r.setting, Quality: "high", Mode: "off", State: "off", Reason: r.reason}
	disk := r.disk
	if disk == "low" || disk == "full" {
		disk = "ready"
	}
	st.Disk.State = disk
	if r.readyLocked() {
		m := r.minutesLeftLocked(now)
		st.Disk.MinutesLeft = &m
	}
	if r.on {
		st.Mode = "on"
		if r.since.IsZero() {
			st.State = "error"
		} else {
			st.State = "on"
			el, dropped := int(now.Sub(r.since)/time.Second), 0
			st.ElapsedS, st.DroppedS = &el, &dropped
		}
	}
	return st
}
