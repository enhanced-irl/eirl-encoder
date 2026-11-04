package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/cloud"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// RECORDING.md §6.8: the recording reaches the cloud in short, without the file's name or the disk's label, model or
// size; nothing with an engine older than its settings or without the mixer.
func TestCloudStatusRecording(t *testing.T) {
	el, dropped, left, size := 75, 2, 200, int64(62008590336)
	rec := &mixer.Record{Mode: "on", State: "on", Setting: "with_output", Quality: "max", Reason: "disk_slow", By: "output",
		Seq: 3, ElapsedS: &el, DroppedS: &dropped, File: "2026-11-04_1712.mp4",
		Disk: mixer.RecordDisk{State: "ready", FS: "exfat", Label: "EIRL-REC", SizeBytes: &size, MinutesLeft: &left, Dirty: true}}
	st := Status{Service: ServiceView{State: "active", Phase: "streaming"}, Mixer: &mixer.State{Record: rec}}
	b, _ := json.Marshal(CloudStatus(st, time.Now()).Recording)
	want := `{"setting":"with_output","quality":"max","mode":"on","state":"on","reason":"disk_slow","elapsedS":75,"droppedS":2,` +
		`"disk":{"state":"ready","minutesLeft":200}}`
	if string(b) != want {
		t.Errorf("recording\n got %s\nwant %s", b, want)
	}
	// Stopped, no disk: only the states
	st.Mixer.Record = &mixer.Record{Mode: "off", State: "off", Setting: "manual", Quality: "high", Disk: mixer.RecordDisk{State: "none"}}
	b, _ = json.Marshal(CloudStatus(st, time.Now()).Recording)
	if string(b) != `{"setting":"manual","quality":"high","mode":"off","state":"off","disk":{"state":"none"}}` {
		t.Errorf("stopped: %s", b)
	}
	// An engine before setting/quality, one without a recorder, no mixer: no block at all
	for _, m := range []*mixer.State{{Record: &mixer.Record{Mode: "off", State: "off"}}, {}, nil} {
		st.Mixer = m
		if b, _ := json.Marshal(CloudStatus(st, time.Now())); strings.Contains(string(b), `"recording"`) {
			t.Errorf("%+v: %s", m, b)
		}
	}
}

// REC from the app: the engine's record command, with its refusals as the action's code and the message in the
// device language.
func TestCloudDeviceRecord(t *testing.T) {
	e := setup(t, true)
	d := &CloudDevice{Srv: e.srv}
	ctx := context.Background()
	var ae *cloud.ActionError
	if err := d.Action(ctx, "record_start", ""); !errors.As(err, &ae) || ae.Code != "unsupported" {
		t.Errorf("no mixer in the app: %v", err)
	}
	run := t.TempDir()
	cl, eng := startRecEngine(t, run)
	e.srv.Mixer = cl
	if err := d.Action(ctx, "record_start", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.Action(ctx, "record_stop", ""); err != nil {
		t.Fatal(err)
	}
	if got := eng.got(); len(got) != 2 || got[0] != `{"id":1,"cmd":"record","on":true}` || got[1] != `{"id":1,"cmd":"record","on":false}` {
		t.Errorf("to the engine %v", got)
	}
	for _, c := range []struct{ engine, code, msg string }{
		{"disabled", "disabled", "Local recording is off"},
		{"no_disk", "no_disk", "connect a USB disk"},
		{"disk_full", "disk_full", "the disk is full"},
		{"unsupported", "unsupported", "cannot record"},
		{"unknown_command", "unsupported", "cannot record"}, // an engine older than the recording
		{"timeout", "timeout", "did not answer"},
	} {
		eng.answer = func(string) string { return c.engine }
		err := d.Action(ctx, "record_start", "")
		if !errors.As(err, &ae) || ae.Code != c.code || !strings.Contains(strings.ToLower(ae.Message), strings.ToLower(c.msg)) {
			t.Errorf("%s: %v", c.engine, err)
		}
	}
	e.srv.Mixer = &mixer.Client{Sock: filepath.Join(run, "none.sock"), StatePath: filepath.Join(run, "none.json")}
	if err := d.Action(ctx, "record_start", ""); !errors.As(err, &ae) || ae.Code != "mixer_off" {
		t.Errorf("engine stopped: %v", err)
	}
}
