package main

import (
	"errors"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
)

func actionCode(err error) string {
	var ae *cloud.ActionError
	if errors.As(err, &ae) {
		return ae.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func mustSimRec(t *testing.T, setting, disk string, powerLost bool) *simRec {
	t.Helper()
	r, err := newSimRec(setting, disk, "", powerLost, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The recording as the cloud sees it in cloud mode (cloudrec.go): REC, the disk going and coming back, a full disk and
// Go live with with_output, like the engine's record (RECORDING.md §5.2).
func TestSimCloudRecording(t *testing.T) {
	t0 := time.Now()
	r := mustSimRec(t, "manual", "none", false)
	if code := actionCode(r.set(true, t0)); code != "no_disk" {
		t.Errorf("REC without a disk: %q", code)
	}
	r.setDisk("ready", t0)
	if err := r.set(true, t0); err != nil {
		t.Fatal(err)
	}
	st := r.status(t0.Add(90 * time.Second))
	if st.Mode != "on" || st.State != "on" || *st.ElapsedS != 90 || *st.Disk.MinutesLeft != simRecRoom-1 || st.Disk.State != "ready" {
		t.Errorf("recording: %+v %+v", st, st.Disk)
	}
	// Pulled out while recording: disk_removed and REC stays on; a disk back is a new file
	r.setDisk("none", t0.Add(2*time.Minute))
	if st = r.status(t0.Add(2 * time.Minute)); st.Mode != "on" || st.State != "error" || st.Reason != "disk_removed" || st.Disk.MinutesLeft != nil {
		t.Errorf("pulled out: %+v", st)
	}
	r.setDisk("ready", t0.Add(3*time.Minute))
	if st = r.status(t0.Add(3*time.Minute + 5*time.Second)); st.State != "on" || st.Reason != "" || *st.ElapsedS != 5 {
		t.Errorf("back: %+v", st)
	}
	if err := r.set(false, t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st = r.status(t0.Add(4 * time.Minute)); st.Mode != "off" || st.State != "off" || st.ElapsedS != nil {
		t.Errorf("stopped: %+v", st)
	}
	// A full disk refuses REC; a low one stops on its own with disk_full (D7)
	r.setDisk("full", t0)
	if code := actionCode(r.set(true, t0)); code != "disk_full" {
		t.Errorf("full: %q", code)
	}
	r.setDisk("low", t0)
	if err := r.set(true, t0); err != nil {
		t.Fatal(err)
	}
	if st = r.status(t0.Add(4 * time.Minute)); st.Mode != "off" || st.Reason != "disk_full" {
		t.Errorf("low: %+v", st)
	}
	// Off in the settings
	if code := actionCode(mustSimRec(t, "off", "ready", false).set(true, t0)); code != "disabled" {
		t.Errorf("off: %q", code)
	}
	// with_output: Go live starts it and Stop streaming stops only the one Go live started
	w := mustSimRec(t, "with_output", "ready", true)
	if st = w.status(t0); st.Reason != "power_lost" {
		t.Errorf("power cut at start: %+v", st)
	}
	w.output(true, t0)
	if st = w.status(t0); st.State != "on" || st.Reason != "" {
		t.Errorf("Go live: %+v", st)
	}
	w.output(false, t0)
	if st = w.status(t0); st.Mode != "off" {
		t.Errorf("Stop streaming: %+v", st)
	}
	w.set(true, t0)
	w.output(false, t0)
	if st = w.status(t0); st.Mode != "on" {
		t.Errorf("REC is not stopped by Stop streaming: %+v", st)
	}
	// Go live without a disk: REC on, waiting for one with no_disk; the disk comes and it records
	w.set(false, t0)
	w.setDisk("none", t0)
	w.output(true, t0)
	if st = w.status(t0); st.Mode != "on" || st.State != "error" || st.Reason != "no_disk" {
		t.Errorf("Go live without a disk: %+v", st)
	}
	w.setDisk("ready", t0)
	if st = w.status(t0); st.State != "on" {
		t.Errorf("the disk comes: %+v", st)
	}
	// Restarting the engine stops it (D6)
	w.engineStopped(t0)
	if st = w.status(t0); st.Mode != "off" {
		t.Errorf("restart: %+v", st)
	}
	// A disk word outside the helper's states is refused: it would reach the app as a state the cloud drops
	if _, err := newSimRec("manual", "exfat", "", false, nil); err == nil {
		t.Error("-rec-disk exfat accepted")
	}
}
