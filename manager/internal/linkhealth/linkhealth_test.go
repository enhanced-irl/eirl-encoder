package linkhealth

import (
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func lte(rsrp, sinr float64) *Modem {
	return &Modem{State: "connected", SIM: "ready", Registration: "home",
		Radio: Radio{Tech: "4g", RSRP: f(rsrp), SINR: f(sinr)}}
}

func TestJudge(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Result
	}{
		{"cable ok", Input{Kind: "ethernet", Available: true}, Result{Good, CodeOK}},
		{"switched off wins over everything", Input{Kind: "ethernet", Disabled: true, Conflict: true}, Result{Off, CodeOff}},
		{"repeated IP", Input{Kind: "usb", Available: true, Conflict: true}, Result{Bad, CodeConflict}},
		{"unplugged cable", Input{Kind: "ethernet"}, Result{Bad, CodeDisconnected}},
		{"no internet", Input{Kind: "ethernet", Available: true, NoInternet: true}, Result{Bad, CodeNoInternet}},
		{"streaming, not used yet", Input{Kind: "ethernet", Available: true, Streaming: true}, Result{Weak, CodeConnecting}},
		{"streaming and used", Input{Kind: "ethernet", Available: true, Streaming: true, InUse: true}, Result{Good, CodeOK}},

		{"wifi strong", Input{Kind: "wifi", Available: true, WiFiPercent: i(80)}, Result{Good, CodeOK}},
		{"wifi at the antenna's 3-arc threshold", Input{Kind: "wifi", Available: true, WiFiPercent: i(67)}, Result{Good, CodeOK}},
		{"wifi weak", Input{Kind: "wifi", Available: true, WiFiPercent: i(48)}, Result{Weak, CodeSignalWeak}},
		{"wifi minimal", Input{Kind: "wifi", Available: true, WiFiPercent: i(20)}, Result{Bad, CodeSignalMinimal}},
		{"wifi without a reading", Input{Kind: "wifi", Available: true}, Result{Good, CodeOK}},

		{"lte good", Input{Kind: "modem", Available: true, Modem: lte(-88, 14)}, Result{Good, CodeOK}},
		{"lte at the green edge", Input{Kind: "modem", Available: true, Modem: lte(-100, 5)}, Result{Good, CodeOK}},
		{"lte weak rsrp", Input{Kind: "modem", Available: true, Modem: lte(-106, 12)}, Result{Weak, CodeSignalWeak}},
		{"lte weak sinr", Input{Kind: "modem", Available: true, Modem: lte(-90, 3)}, Result{Weak, CodeSignalWeak}},
		{"lte minimal rsrp", Input{Kind: "modem", Available: true, Modem: lte(-117, 8)}, Result{Bad, CodeSignalMinimal}},
		{"lte negative sinr", Input{Kind: "modem", Available: true, Modem: lte(-95, -2)}, Result{Bad, CodeSignalMinimal}},
		{"lte bad rsrq only", Input{Kind: "modem", Available: true,
			Modem: &Modem{SIM: "ready", Radio: Radio{Tech: "4g", RSRP: f(-90), SINR: f(10), RSRQ: f(-17)}}}, Result{Weak, CodeSignalWeak}},
		{"rssi only", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready", Radio: Radio{RSSI: f(-80)}}}, Result{Weak, CodeSignalWeak}},
		{"bars only", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready", Radio: Radio{Bars: i(5)}}}, Result{Good, CodeOK}},
		{"one bar", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready", Radio: Radio{Bars: i(1)}}}, Result{Bad, CodeSignalMinimal}},
		{"quality only", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready", Radio: Radio{Quality: i(50)}}}, Result{Weak, CodeSignalWeak}},
		{"3g good signal", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready", Radio: Radio{Tech: "3g", RSSI: f(-60)}}}, Result{Weak, CodeSlowTech}},
		{"3g bad signal stays signal", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready", Radio: Radio{Tech: "3g", RSSI: f(-95)}}}, Result{Bad, CodeSignalMinimal}},
		{"2g", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready", Radio: Radio{Tech: "2g", RSSI: f(-60)}}}, Result{Bad, CodeSlowTech}},
		{"no metrics at all", Input{Kind: "modem", Available: true, Modem: &Modem{SIM: "ready"}}, Result{Good, CodeOK}},

		{"sim asks for its pin", Input{Kind: "modem", Modem: &Modem{State: "locked", SIM: "pin"}}, Result{Bad, CodeSIMPIN}},
		{"puk", Input{Kind: "modem", Modem: &Modem{State: "locked", SIM: "puk"}}, Result{Bad, CodeSIMPUK}},
		{"no sim", Input{Kind: "modem", Modem: &Modem{State: "failed", SIM: "missing"}}, Result{Bad, CodeSIMMissing}},
		{"modem failed", Input{Kind: "modem", Modem: &Modem{State: "failed", SIM: "ready"}}, Result{Bad, CodeModemFailed}},
		{"searching", Input{Kind: "modem", Modem: &Modem{State: "searching", SIM: "ready", Registration: "searching"}}, Result{Bad, CodeSearching}},
		{"denied", Input{Kind: "modem", Modem: &Modem{State: "registered", SIM: "ready", Registration: "denied"}}, Result{Bad, CodeRegDenied}},
		{"roaming without data", Input{Kind: "modem", Modem: &Modem{State: "registered", SIM: "ready", Registration: "roaming"}}, Result{Bad, CodeRoamingOff}},
		{"roaming with data is judged by signal", Input{Kind: "modem", Available: true,
			Modem: &Modem{State: "connected", SIM: "ready", Registration: "roaming", Radio: Radio{Tech: "4g", RSRP: f(-85), SINR: f(15)}}}, Result{Good, CodeOK}},
		{"registered, no bearer yet", Input{Kind: "modem", Modem: &Modem{State: "registered", SIM: "ready", Registration: "home"}}, Result{Bad, CodeDisconnected}},
		{"pin wins over switched off? no: off first", Input{Kind: "modem", Disabled: true, Modem: &Modem{SIM: "pin"}}, Result{Off, CodeOff}},
		{"modem without internet", Input{Kind: "modem", Available: true, NoInternet: true, Modem: lte(-80, 20)}, Result{Bad, CodeNoInternet}},
		{"modem weak and streaming unused keeps weak signal", Input{Kind: "modem", Available: true, Streaming: true, Modem: lte(-106, 10)}, Result{Weak, CodeSignalWeak}},
	}
	for _, c := range cases {
		if got := Judge(c.in); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestBars(t *testing.T) {
	cases := []struct {
		r    Radio
		want int
	}{
		{Radio{RSRP: f(-80)}, 4}, {Radio{RSRP: f(-95)}, 3}, {Radio{RSRP: f(-106)}, 2}, {Radio{RSRP: f(-117)}, 1}, {Radio{RSRP: f(-125)}, 0},
		{Radio{RSSI: f(-60)}, 4}, {Radio{RSSI: f(-80)}, 2}, {Radio{Bars: i(5)}, 4}, {Radio{Bars: i(2)}, 2}, {Radio{Bars: i(0)}, 0},
		{Radio{Quality: i(100)}, 4}, {Radio{Quality: i(0)}, 0}, {Radio{}, 0},
	}
	for _, c := range cases {
		if got := Bars(c.r); got != c.want {
			t.Errorf("Bars(%+v) = %d, want %d", c.r, got, c.want)
		}
	}
}

func TestTrackerHysteresis(t *testing.T) {
	var tr Tracker
	t0 := time.Unix(1_000_000, 0)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	good, weak, bad := Result{Good, CodeOK}, Result{Weak, CodeSignalWeak}, Result{Bad, CodeSignalMinimal}
	step := func(s float64, in, want Result) {
		t.Helper()
		if got := tr.Update("wwan0", in, at(s)); got != want {
			t.Fatalf("t=%vs in %v: shown %v, want %v", s, in, got, want)
		}
	}
	step(0, good, good)  // first sight: at once
	step(2, weak, good)  // one weak sample is not enough
	step(4, weak, weak)  // two in a row over ≥3 s: worse
	step(6, good, weak)  // better: waits 10 s
	step(10, good, weak) // 4 s
	step(12, weak, weak) // back to weak: the wait restarts
	step(14, good, weak)
	step(24, good, good) // 10 s of good
	// worse, mixing amber and red samples, still counts as worse
	step(26, weak, good)
	step(28, bad, bad)
	// a hard fact is shown at once, and so is its end
	step(29, Result{Bad, CodeSIMPIN}, Result{Bad, CodeSIMPIN})
	step(30, good, good)
	step(31, Result{Off, CodeOff}, Result{Off, CodeOff})
	step(32, weak, weak)
	// same colour, another reason: the reason changes at once
	step(33, Result{Weak, CodeConnecting}, Result{Weak, CodeConnecting})
}

func TestTrackerKeep(t *testing.T) {
	var tr Tracker
	now := time.Unix(0, 0)
	tr.Update("a", Result{Good, CodeOK}, now)
	tr.Update("b", Result{Good, CodeOK}, now)
	tr.Keep(map[string]bool{"a": true})
	// "b" forgotten: a weak result is shown at once (first sight)
	if got := tr.Update("b", Result{Weak, CodeSignalWeak}, now.Add(time.Second)); got.Level != Weak {
		t.Fatalf("b after Keep: %v", got)
	}
	if got := tr.Update("a", Result{Weak, CodeSignalWeak}, now.Add(time.Second)); got.Level != Good {
		t.Fatalf("a kept its hysteresis: %v", got)
	}
}

func TestWiFiBarsMatchTheColour(t *testing.T) {
	for pct := 0; pct <= 100; pct++ {
		lvl := wifi(pct).Level
		b := WiFiBars(pct)
		switch {
		case lvl == Good && b != 4, lvl == Weak && (b < 2 || b > 3), lvl == Bad && b > 1:
			t.Errorf("%d %%: %s with %d bars", pct, lvl, b)
		}
	}
}
