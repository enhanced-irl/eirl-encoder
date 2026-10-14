package usbhub

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// New: engine_usb.py main(): usage, port, cycle refused while emitting (or unknown), --force.
func TestMainCLI(t *testing.T) {
	s, f := newFake(t)
	h := NewHubs(s)
	asked := 0
	emitting := func(on, known bool) func() (bool, bool) {
		return func() (bool, bool) { asked++; return on, known }
	}
	run := func(em func() (bool, bool), args ...string) (int, string, string) {
		var out, errb strings.Builder
		rc := Main(args, &out, &errb, h, em)
		return rc, out.String(), errb.String()
	}
	if rc, _, e := run(emitting(false, true), "cycle"); rc != 64 || !strings.HasPrefix(e, "usage:") {
		t.Errorf("usage: %d %q", rc, e)
	}
	if rc, _, _ := run(emitting(false, true), "reset", "1-2.1"); rc != 64 {
		t.Errorf("unknown subcommand: %d", rc)
	}
	rc, out, _ := run(emitting(false, true), "port", "1-2.1")
	if rc != 0 || out != "1-2.1: port 1 of 1-2+2-1\nalso powered off: 1-2.3 2-1.4\n" || len(f.calls) != 0 {
		t.Errorf("port: %d %q %v", rc, out, f.calls)
	}
	if rc, out, _ := run(emitting(false, true), "port", "1-2.3"); rc != 1 || out != "1-2.3: not a camera or a capture card\n" {
		t.Errorf("touch screen: %d %q", rc, out)
	}
	if rc, out, _ := run(emitting(false, true), "port", "/dev/video7"); rc != 1 || out != ": not a USB device\n" {
		t.Errorf("unknown node: %d %q", rc, out)
	}
	// Dropped off the bus: by its sysfs name it is taken as a camera
	if rc, _, _ := run(emitting(false, true), "port", "2-1.2"); rc != 0 {
		t.Errorf("by name: %d", rc)
	}
	asked = 0
	for _, em := range []func() (bool, bool){emitting(true, true), emitting(false, false)} {
		if rc, _, e := run(em, "cycle", "1-2.1"); rc != 3 || !strings.Contains(e, "--force") || len(f.calls) != 0 {
			t.Errorf("cycle while emitting/unknown: %d %q", rc, e)
		}
	}
	rc, out, _ = run(emitting(false, true), "cycle", "1-2.1")
	if rc != 0 || !strings.HasSuffix(out, "power cut and given back on 1-2+2-1\n") || len(f.calls) != 4 {
		t.Errorf("cycle: %d %q %v", rc, out, f.calls)
	}
	if asked != 3 {
		t.Errorf("asked %d", asked)
	}
	f.calls = nil
	f.fail[node12+" clear"], f.fail[node21+" clear"] = true, true
	rc, out, e := run(emitting(true, true), "--force", "cycle", "1-2.1")
	if rc != 1 || !strings.HasSuffix(out, "power cut and given back on none\n") || strings.Count(e, "error: ") != 2 || asked != 3 {
		t.Errorf("forced, failing: %d %q %q (asked %d)", rc, out, e, asked)
	}
}

// New: emitting() asks the engine's socket.
func TestEngineEmitting(t *testing.T) {
	dir, err := os.MkdirTemp("", "uh") // short: a socket path has ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	if on, known := EngineEmitting(sock)(); on || known {
		t.Error("no socket")
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	replies := make(chan string, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			if line != `{"id":1,"cmd":"get"}`+"\n" {
				c.Close()
				continue
			}
			c.Write([]byte(<-replies + "\n"))
			c.Close()
		}
	}()
	for _, c := range []struct {
		reply     string
		on, known bool
	}{
		{`{"id":1,"ok":true,"state":{"output":{"mode":"on","state":"connecting"}}}`, true, true},
		{`{"id":1,"ok":true,"state":{"output":{"mode":"off"}}}`, false, true},
		{`{"id":1,"ok":true,"state":{"sources":[]}}`, false, false},
		{`not json`, false, false},
	} {
		replies <- c.reply
		if on, known := EngineEmitting(sock)(); on != c.on || known != c.known {
			t.Errorf("%s: %v %v", c.reply, on, known)
		}
	}
}
