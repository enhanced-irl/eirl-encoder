package phones

import (
	"os"
	"strings"
	"testing"
	"time"

	"path/filepath"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// --- the monitor on Bluetooth headphones (BLUETOOTH.md §3.3) ---

func TestPureTargetDelayBluetooth(t *testing.T) {
	// The headphones add 150 ms after the ALSA device: the screen's 160 comes down to 10, and the arrival rules
	// with the Bluetooth buffer: 165 + 40 + 30
	if d := TargetDelayFor(160*ms-150*ms, 165*ms, BTSinkBuffer); d != 235*ms {
		t.Error(d)
	}
	// Below zero (headphones slower than the screen) and nothing measured: never negative
	if d := TargetDelayFor(-40*ms, 0, BTSinkBuffer); d != 0 {
		t.Error(d)
	}
	if !sameBT(nil, nil) || sameBT(&btaudio.Device{Addr: "A"}, nil) ||
		!sameBT(&btaudio.Device{Addr: "A", Profile: "a2dp", Name: "x"}, &btaudio.Device{Addr: "A", Profile: "a2dp", Name: "y"}) ||
		sameBT(&btaudio.Device{Addr: "A", Profile: "a2dp"}, &btaudio.Device{Addr: "A", Profile: "sco"}) {
		t.Error("sameBT")
	}
}

func writeBT(t *testing.T, path string, f btaudio.File) {
	t.Helper()
	if err := os.WriteFile(path, btaudio.Marshal(f), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitInfo(t *testing.T, p *Phones, ok func(mixer.Phones) bool) mixer.Phones {
	t.Helper()
	var i mixer.Phones
	for n := 0; n < 400; n++ {
		if i = p.Info(); ok(i) {
			return i
		}
		time.Sleep(10 * ms)
	}
	t.Fatalf("state never came: %+v", i)
	return i
}

func TestListenBluetoothAndBackToScreen(t *testing.T) {
	logs := quiet(t)
	p, r, _ := makePhones(t, "")
	lm := &latMedia{Media: r.media}
	p.media = lm
	p.measure = 10 * ms
	r.tap.lates = []time.Duration{0, 165 * ms}
	// The screen's HDMI is there too: it is where the monitor goes back to
	_ = os.WriteFile(filepath.Join(p.asound, "pcm"), []byte(pcm), 0o644)
	_ = os.WriteFile(filepath.Join(p.asound, "cards"), []byte(cards), 0o644)
	_ = os.MkdirAll(filepath.Join(p.asound, "card0"), 0o755)
	_ = os.WriteFile(filepath.Join(p.asound, "card0", "eld#3.0"), []byte(eldOK), 0o644)
	var amixer int
	p.digital = func(int) { amixer++ }
	hp := btaudio.Device{Addr: "38:18:4C:12:A0:01", Name: "WH-1000XM4", Profile: "a2dp", DelayMs: 150}
	writeBT(t, r.bt, btaudio.File{Out: &hp})
	p.Start()
	i := waitInfo(t, p, func(i mixer.Phones) bool { return i.State == StateOn })
	if i.Target != mixer.PhonesBluetooth || i.Device != "WH-1000XM4" {
		t.Error(i)
	}
	lm.mu.Lock()
	lp := lm.last
	lm.mu.Unlock()
	if lp.El("phsink").Prop("device") != "bluealsa:DEV=38:18:4C:12:A0:01,PROFILE=a2dp" ||
		lp.El("phsink").Prop("buffer-time") != "40000" || lp.El("phsink").Prop("slave-method") != "none" {
		t.Error(lp.El("phsink").Props)
	}
	// screen 160 − headphones 150 = 10; late 165 → 165 + 40 + 30 = 235
	if l := lm.latencies(); len(l) != 1 || l[0] != 235*ms {
		t.Error(l)
	}
	if amixer != 0 {
		t.Error("amixer on Bluetooth")
	}
	// The app restarting (its file gone for a while) does not move the monitor
	if err := os.Remove(r.bt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(TargetPoll + 200*ms)
	if i := p.Info(); i.Target != mixer.PhonesBluetooth || i.State != StateOn {
		t.Error("the app's restart moved the monitor", i)
	}
	// The headphones go: down, closed, and over to the screen's jack within a target poll
	writeBT(t, r.bt, btaudio.File{})
	waitInfo(t, p, func(i mixer.Phones) bool { return i.Target == mixer.PhonesScreen })
	var lp2 = lp
	for n := 0; n < 300 && lp2 == lp; n++ {
		time.Sleep(10 * ms)
		lm.mu.Lock()
		lp2 = lm.last
		lm.mu.Unlock()
	}
	if !lp.Closed || lp2 == lp || lp2.El("phsink").Prop("device") != "hw:tegrahda,3" {
		t.Error("not switched", lp.Closed, lp2.El("phsink").Props)
	}
	p.Stop()
	waitDone(t, p, CloseWait+time.Second)
	out := logs.String()
	for _, want := range []string{
		`[headphones] event=phones_target target=bluetooth device=38:18:4C:12:A0:01 profile=a2dp`,
		`[headphones] event=phones_playing device="bluealsa:DEV=38:18:4C:12:A0:01,PROFILE=a2dp" gain_db=-12 delay_ms=235`,
		`[headphones] event=phones_switch from=bluetooth:38:18:4C:12:A0:01 to=screen`,
		`[headphones] event=phones_target target=screen`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

// Off, the state still says where it would play (the web and the screen show it).
func TestPhonesOffShowsBluetoothTarget(t *testing.T) {
	quiet(t)
	p, r, _ := makePhones(t, `{"on": false}`)
	writeBT(t, r.bt, btaudio.File{Out: &btaudio.Device{Addr: "38:18:4C:12:A0:01", Name: "WH-1000XM4", Profile: "sco"}})
	p.Start()
	waitInfo(t, p, func(i mixer.Phones) bool { return i.Target == mixer.PhonesBluetooth && i.Device == "WH-1000XM4" })
	p.Stop()
	waitDone(t, p, time.Second)
}
