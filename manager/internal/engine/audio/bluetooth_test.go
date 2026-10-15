package audio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/probe"
)

// Bluetooth inputs (BLUETOOTH.md §3.3): a phone (A2DP) or a headset's microphone (hands-free), from the app's file.

const (
	phoneID   = "bt:A4C3F0552102"
	phoneAddr = "A4:C3:F0:55:21:02"
)

func btRig(t *testing.T, slots int) (*rig, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bluetooth.json")
	return newRig(t, slots, Config{Bluetooth: path}), path
}

func writeFile(t *testing.T, path string, f btaudio.File) {
	t.Helper()
	if err := os.WriteFile(path, btaudio.Marshal(f), 0o644); err != nil {
		t.Fatal(err)
	}
}

func phoneEntry(profile string) probe.AudioEntry {
	return probe.AudioEntry{ID: phoneID, Kind: KindBluetooth, Name: "Pixel <8>", Profile: profile}
}

func TestInputBluetoothLifecycle(t *testing.T) {
	r, path := btRig(t, 1)
	writeFile(t, path, btaudio.File{In: []btaudio.Device{{Addr: phoneAddr, Name: "Pixel 8", Profile: "a2dp"}}})
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{phoneEntry("a2dp")}})
	in := r.bus.slots[0]
	if in == nil || in.kind != KindBluetooth || in.Name() != "Pixel 8" {
		t.Fatalf("input %+v", in)
	}
	p := r.opened(t, "audio1", nil)
	if !strings.HasPrefix(p.Desc, `alsasrc device="bluealsa:DEV=A4:C3:F0:55:21:02,PROFILE=a2dp" provide-clock=false `+
		`latency-time=40000 buffer-time=200000 ! audioconvert ! audioresample ! `) {
		t.Errorf("desc: %s", p.Desc)
	}
	// A phone not playing sends nothing: no signal, and it is NOT reopened (the read just waits)
	waitState(t, in, StateNoSignal, "el dispositivo Bluetooth no manda audio")
	r.media.Advance(10 * time.Second)
	time.Sleep(3 * popEvery)
	if q := r.media.Find("audio1"); q != p {
		t.Error("reopened while the phone is silent")
	}
	r.media.Bridges[0].Frames(1)
	waitState(t, in, StateLive, "")
	// The app restarting (its file gone a while) does not cut it
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * popEvery)
	if s, _ := stateOf(in); s != StateLive || r.media.Find("audio1") != p {
		t.Error("the app's restart cut the input", s)
	}
	// Disconnected (gone from the file): gone, and DropGone frees its slot after the grace
	writeFile(t, path, btaudio.File{})
	waitState(t, in, StateGone, detailGone)
	if dropped := r.bus.DropGone(0, nil); len(dropped) != 1 || dropped[0] != phoneID || r.bus.slots[0] != nil {
		t.Error("not dropped", dropped)
	}
	if !strings.Contains(r.logs.String(), `event=audio_input_removed slot=1 input="Pixel 8" reason=unplugged`) {
		t.Error(r.logs.String())
	}
}

func TestDiscoverBluetoothChecksAndProfileChange(t *testing.T) {
	r, path := btRig(t, 3)
	// Nothing that does not check out gets a slot: a lower-case id, an unknown profile, the wrong kind
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{
		{ID: "bt:a4c3f0552102", Kind: KindBluetooth, Profile: "a2dp"},
		{ID: phoneID, Kind: KindBluetooth, Profile: "hw:0,0"},
		{ID: phoneID, Kind: KindUSB, Profile: "a2dp"},
		{ID: "bt:A4C3F055210", Kind: KindBluetooth, Profile: "a2dp"},
	}})
	for i, in := range r.bus.slots {
		if in != nil {
			t.Errorf("slot %d: %s", i+1, in.id)
		}
	}
	writeFile(t, path, btaudio.File{In: []btaudio.Device{{Addr: phoneAddr, Name: "Headset", Profile: "a2dp"}}})
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{phoneEntry("a2dp")}})
	first := r.bus.slots[0]
	if first == nil || first.device != "bluealsa:DEV=A4:C3:F0:55:21:02,PROFILE=a2dp" {
		t.Fatalf("%+v", first)
	}
	// The same device used as a microphone now: it opens again on its hands-free device, in its slot
	writeFile(t, path, btaudio.File{In: []btaudio.Device{{Addr: phoneAddr, Name: "Headset", Profile: "sco"}}})
	r.bus.Discover(probe.Catalogue{OK: true, Audio: []probe.AudioEntry{phoneEntry("sco")}})
	again := r.bus.slots[0]
	if again == nil || again == first || again.device != "bluealsa:DEV=A4:C3:F0:55:21:02,PROFILE=sco" || !stopped(first) {
		t.Errorf("profile change: %+v", again)
	}
	if !strings.Contains(r.logs.String(), `reason=profile_changed`) {
		t.Error(r.logs.String())
	}
}

func TestCardsChangedFollowsTheBluetoothFile(t *testing.T) {
	r, path := btRig(t, 1)
	r.bus.CardsChanged()
	if r.bus.CardsChanged() {
		t.Error("nothing changed")
	}
	writeFile(t, path, btaudio.File{In: []btaudio.Device{{Addr: phoneAddr, Name: "Pixel 8", Profile: "a2dp"}}})
	if !r.bus.CardsChanged() || r.bus.CardsChanged() {
		t.Error("a Bluetooth device ready must probe again, once")
	}
}
