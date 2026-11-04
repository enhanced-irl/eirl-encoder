package screen

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHasKey(t *testing.T) {
	// L4T's gpio-keys on the Jetson Nano: KEY_POWER (116) and BTN_1 (0x101, Force recovery)
	bm := "2 0 0 10000000000000 0"
	for code, want := range map[int]bool{keyPower: true, 0x101: true, 115: false, 0: false, 1000: false, -1: false} {
		if hasKey(bm, code) != want {
			t.Errorf("%d: %v", code, !want)
		}
	}
	if hasKey("", keyPower) || hasKey("zz 0", keyPower) {
		t.Fatal("a bad bitmap")
	}
}

func TestFindPowerKey(t *testing.T) {
	root := t.TempDir()
	mk := func(ev, name, keys string) {
		d := filepath.Join(root, "sys/class/input", ev, "device")
		if err := os.MkdirAll(filepath.Join(d, "capabilities"), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "name"), []byte(name+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "capabilities/key"), []byte(keys+"\n"), 0o644)
	}
	if findPowerKey(root) != "" {
		t.Fatal("found in nothing")
	}
	mk("event0", "USB Keyboard", "10000000000000 0") // a keyboard's power key is not the board's
	mk("event1", "gpio-keys", "0")                   // gpio-keys without KEY_POWER
	if p := findPowerKey(root); p != "" {
		t.Fatalf("%q", p)
	}
	mk("event2", "gpio-keys", "2 0 0 10000000000000 0")
	if p := findPowerKey(root); !strings.HasSuffix(p, "/dev/input/event2") {
		t.Fatalf("%q", p)
	}
}

func TestPowerKeyEvent(t *testing.T) {
	for _, c := range []struct {
		typ, code uint16
		val       int32
		down, ok  bool
	}{
		{evKey, keyPower, 1, true, true},
		{evKey, keyPower, 0, false, true},
		{evKey, keyPower, 2, false, false}, // autorepeat
		{evKey, 0x101, 1, false, false},    // Force recovery
		{evSyn, synReport, 0, false, false},
	} {
		if d, ok := powerKeyEvent(c.typ, c.code, c.val); d != c.down || ok != c.ok {
			t.Errorf("%+v: %v %v", c, d, ok)
		}
	}
}

func TestPowerKeyReaderRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan bool, 8)
	stop := make(chan struct{})
	p := &powerKeyReader{out: out, stop: stop}
	ev := func(typ, code uint16, val int32) []byte {
		e := make([]byte, eventSize)
		binary.LittleEndian.PutUint16(e[16:], typ)
		binary.LittleEndian.PutUint16(e[18:], code)
		binary.LittleEndian.PutUint32(e[20:], uint32(val))
		return e
	}
	var b []byte
	b = append(b, ev(evKey, keyPower, 1)...)
	b = append(b, ev(evSyn, synReport, 0)...)
	b = append(b, ev(evKey, keyPower, 2)...) // autorepeat: no second down
	b = append(b, ev(evKey, keyPower, 0)...)
	b = append(b, ev(evKey, keyPower, 1)...) // down when the device goes
	w.Write(b)
	w.Close()
	done := make(chan bool)
	go func() { done <- p.read(r) }()
	select {
	case down := <-done:
		if !down {
			t.Fatal("the last state was down")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read does not end at EOF")
	}
	close(out)
	var got []bool
	for d := range out {
		got = append(got, d)
	}
	if len(got) != 3 || !got[0] || got[1] || !got[2] {
		t.Fatalf("%v", got)
	}
}
