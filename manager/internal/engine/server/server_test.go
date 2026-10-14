package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// tempDir: a short path (a Unix socket path takes ~104 bytes at most on macOS; t.TempDir is longer)
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "srv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// engineLoop: the engine's actor loop of the tests, until the test ends
func engineLoop(t *testing.T, s *Server, h Handler) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case req := <-s.Requests():
				req.Run(h)
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}

// start: a listening server in a temp dir, with the handler of ServerTest (take → ok, else unknown_command)
func start(t *testing.T, h Handler) (*Server, string) {
	t.Helper()
	dir := tempDir(t)
	s := New(Config{SocketPath: SocketPath(dir), StatePath: StatePath(dir)})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if h == nil {
		h = func(cmd string, msg map[string]any) (bool, string) {
			if cmd == "take" {
				return true, ""
			}
			return false, "unknown_command"
		}
	}
	engineLoop(t, s, h)
	return s, dir
}

type conn struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func dial(t *testing.T, s *Server) *conn {
	t.Helper()
	c, err := net.Dial("unix", s.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &conn{t, c, bufio.NewReaderSize(c, 1<<20)}
}

func (c *conn) send(raw string) {
	c.t.Helper()
	if _, err := c.c.Write([]byte(raw)); err != nil {
		c.t.Fatal(err)
	}
}

// line: the next line, without '\n'; err on timeout or EOF
func (c *conn) line(timeout time.Duration) (string, error) {
	c.c.SetReadDeadline(time.Now().Add(timeout))
	b, err := c.r.ReadString('\n')
	if err != nil {
		return b, err
	}
	return strings.TrimSuffix(b, "\n"), nil
}

func (c *conn) ask(raw string) string {
	c.t.Helper()
	c.send(raw)
	l, err := c.line(3 * time.Second)
	if err != nil {
		c.t.Fatalf("ask %.60q: %v", raw, err)
	}
	return l
}

// sameJSON: equal as JSON values (key order and spacing apart)
func sameJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %q: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// --- Ported from scripts/test_engine_mixer.py -------------------------------------------------------

// ServerTest.test_protocol
func TestProtocol(t *testing.T) {
	var mu sync.Mutex
	var calls []map[string]any
	s, _ := start(t, func(cmd string, msg map[string]any) (bool, string) {
		mu.Lock()
		calls = append(calls, msg)
		mu.Unlock()
		if cmd == "take" {
			return true, ""
		}
		return false, "unknown_command"
	})
	state := `{"program":{"main":"a"}}`
	if _, err := s.Publish(json.RawMessage(state)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o660 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode %v, want 0660 socket", fi.Mode())
	}
	c := dial(t, s)
	if got := c.ask(`{"id": 1, "cmd": "take", "source": "b"}` + "\n"); got != `{"id":1,"ok":true}` {
		t.Fatalf("take: %s", got)
	}
	if got := c.ask(`{"cmd": "nope"}` + "\n"); got != `{"ok":false,"error":"unknown_command"}` {
		t.Fatalf("nope: %s", got)
	}
	if got := c.ask("no es json\n"); got != `{"ok":false,"error":"bad_json"}` {
		t.Fatalf("bad json: %s", got)
	}
	if got := c.ask("[1, 2]\n"); got != `{"ok":false,"error":"bad_request"}` {
		t.Fatalf("array: %s", got)
	}
	if got := c.ask(`{"id": 2, "cmd": "get"}` + "\n"); got != `{"id":2,"ok":true,"state":`+state+`}` {
		t.Fatalf("get: %s", got)
	}
	var r struct{ OK bool }
	if err := json.Unmarshal([]byte(c.ask(`{"cmd": "subscribe"}`+"\n")), &r); err != nil || !r.OK {
		t.Fatalf("subscribe: %v %v", r, err)
	}
	if _, err := s.Publish(map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	got, err := c.line(3 * time.Second)
	if err != nil || got != `{"event":"state","state":{"x":1}}` {
		t.Fatalf("event: %q %v", got, err)
	}
	// The engine got the whole request, numbers as json.Number
	mu.Lock()
	defer mu.Unlock()
	want := []map[string]any{{"id": json.Number("1"), "cmd": "take", "source": "b"}, {"cmd": "nope"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}
}

// ServerTest.test_deep_json_does_not_kill_the_socket. Python sends 3000 levels (6000 bytes): with the
// MaxLine tightening that line closes the client, so here the nesting fills a whole line instead.
func TestDeepJSONDoesNotKillTheSocket(t *testing.T) {
	s, _ := start(t, nil)
	c := dial(t, s)
	n := MaxLine / 2
	r := c.ask(strings.Repeat("[", n) + strings.Repeat("]", n) + "\n")
	if r != `{"ok":false,"error":"bad_json"}` && r != `{"ok":false,"error":"bad_request"}` {
		t.Fatalf("deep: %s", r)
	}
	if got := c.ask(`{"id": 3, "cmd": "get"}` + "\n"); !strings.HasPrefix(got, `{"id":3,`) {
		t.Fatalf("get after deep: %s", got)
	}
}

// ServerTest.test_long_line_closes
func TestLongLineCloses(t *testing.T) {
	s, _ := start(t, nil)
	c := dial(t, s)
	c.send(strings.Repeat("x", MaxLine+10))
	time.Sleep(300 * time.Millisecond)
	if l, err := c.line(3 * time.Second); !closedErr(err) || l != "" {
		t.Fatalf("got %q %v, want EOF", l, err)
	}
}

// test_socket_drops_clients_that_do_not_read (EngineTest)
func TestSocketDropsClientsThatDoNotRead(t *testing.T) {
	s := New(Config{})
	mk := func(out []byte, sub bool) (*client, net.Conn) {
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		c := &client{conn: a, out: out, subscribed: sub, wake: make(chan struct{}, 1), quit: make(chan struct{})}
		s.clients[c] = struct{}{}
		return c, b
	}
	slow, slowPeer := mk(bytes.Repeat([]byte("x"), MaxOut), true)
	ok, _ := mk(nil, true)
	other, _ := mk(nil, false)
	if _, err := s.Publish(map[string]any{"event": "state"}); err != nil {
		t.Fatal(err)
	}
	if _, in := s.clients[slow]; in || !slow.closed {
		t.Fatal("the client that does not read must be out, without waiting until it can be written")
	}
	slowPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := slowPeer.Read(make([]byte, 1)); !closedErr(err) {
		t.Fatalf("slow client's connection not closed: %v", err)
	}
	if !bytes.HasSuffix(ok.out, []byte("\n")) {
		t.Fatalf("subscribed client: %q", ok.out)
	}
	if len(other.out) != 0 {
		t.Fatalf("not subscribed: %q", other.out)
	}
	s.drop(ok)
	if len(s.clients) != 1 {
		t.Fatalf("clients %d, want only the unsubscribed one", len(s.clients))
	}
	if _, in := s.clients[other]; !in {
		t.Fatal("unsubscribed client gone")
	}
}

// --- Added -------------------------------------------------------------------------------------------

func TestNinthClientIsClosed(t *testing.T) {
	s, _ := start(t, nil)
	var cs []*conn
	for i := range MaxClients {
		c := dial(t, s)
		if got := c.ask(fmt.Sprintf(`{"id":%d,"cmd":"get"}`+"\n", i)); !strings.HasPrefix(got, fmt.Sprintf(`{"id":%d,`, i)) {
			t.Fatalf("client %d: %s", i, got)
		}
		cs = append(cs, c)
	}
	ninth := dial(t, s)
	// The server closes it at once: the write may already fail (EPIPE), else the read sees EOF
	if _, err := ninth.c.Write([]byte(`{"id":9,"cmd":"get"}` + "\n")); err == nil {
		if l, err := ninth.line(3 * time.Second); !closedErr(err) {
			t.Fatalf("9th client: %q %v, want EOF", l, err)
		}
	}
	for i, c := range cs { // the others keep working
		if got := c.ask(`{"id":1,"cmd":"take"}` + "\n"); got != `{"id":1,"ok":true}` {
			t.Fatalf("client %d after the 9th: %s", i, got)
		}
	}
	cs[0].c.Close() // a place is freed once the server sees it gone
	deadline := time.Now().Add(3 * time.Second)
	for {
		c := dial(t, s)
		c.send(`{"id":10,"cmd":"get"}` + "\n")
		if l, err := c.line(time.Second); err == nil {
			if !strings.HasPrefix(l, `{"id":10,`) {
				t.Fatalf("new client: %s", l)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the freed place is never reused")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestIDRules(t *testing.T) {
	s, _ := start(t, nil)
	c := dial(t, s)
	id64 := strings.Repeat("ñ", 64) // characters, not bytes
	digits64 := "1" + strings.Repeat("0", 63)
	cases := []struct{ req, want string }{
		{`{"id": 1, "cmd": "take"}`, `{"id":1,"ok":true}`},
		{`{"id": -5, "cmd": "take"}`, `{"id":-5,"ok":true}`},
		{`{"id": "abc", "cmd": "take"}`, `{"id":"abc","ok":true}`},
		{`{"id": "` + id64 + `", "cmd": "take"}`, `{"id":"` + id64 + `","ok":true}`},
		{`{"id": "` + id64 + `x", "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": "", "cmd": "take"}`, `{"id":"","ok":true}`},
		{`{"id": ` + digits64 + `, "cmd": "take"}`, `{"id":` + digits64 + `,"ok":true}`}, // verbatim, beyond int64
		{`{"id": ` + digits64 + `0, "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 1.5, "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 1.0, "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`}, // a float in Python too
		{`{"id": 1e3, "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": true, "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`}, // deliberate: Python took it
		{`{"id": false, "cmd": "get"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": null, "cmd": "take"}`, `{"ok":true}`},
		{`{"id": [1], "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": {}, "cmd": "take"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 7, "cmd": "nope"}`, `{"id":7,"ok":false,"error":"unknown_command"}`},
		{`{"id": 8, "cmd": "get"}`, `{"id":8,"ok":true,"state":{}}`}, // {} before the first Publish
	}
	for _, tc := range cases {
		if got := c.ask(tc.req + "\n"); got != tc.want {
			t.Errorf("%s → %s, want %s", tc.req, got, tc.want)
		}
	}
}

func TestRequestRules(t *testing.T) {
	s, _ := start(t, nil)
	c := dial(t, s)
	cmd32 := strings.Repeat("ñ", 32) // runes, like Python's len()
	cases := []struct{ req, want string }{
		{"no es json", `{"ok":false,"error":"bad_json"}`},
		{`{"cmd": "get"} x`, `{"ok":false,"error":"bad_json"}`},
		{`{"cmd": "get"}{"cmd": "get"}`, `{"ok":false,"error":"bad_json"}`},
		{"{\"id\": 1, \"cmd\": \"take\", \"source\": \"\xff\"}", `{"ok":false,"error":"bad_json"}`},
		{"\ufeff{\"cmd\": \"get\"}", `{"ok":false,"error":"bad_json"}`},
		{"\u00a0", `{"ok":false,"error":"bad_json"}`},                                // not ASCII whitespace: not ignored
		{`{"id": 1, "cmd": "take", "gain": NaN}`, `{"ok":false,"error":"bad_json"}`}, // Python parsed NaN
		{`"take"`, `{"ok":false,"error":"bad_request"}`},
		{`3`, `{"ok":false,"error":"bad_request"}`},
		{`null`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 1}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 1, "cmd": 5}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 1, "cmd": null}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 1, "cmd": "` + cmd32 + `x"}`, `{"ok":false,"error":"bad_request"}`},
		{`{"id": 1, "cmd": "` + cmd32 + `"}`, `{"id":1,"ok":false,"error":"unknown_command"}`},
		{`{"id": 2, "cmd": "take"}` + "\r", `{"id":2,"ok":true}`},
		{`  {"id": 3, "cmd": "take"}  `, `{"id":3,"ok":true}`},
		{`{"id": 4, "cmd": "take", "text": "<b>&</b>"}`, `{"id":4,"ok":true}`},
	}
	for _, tc := range cases {
		if got := c.ask(tc.req + "\n"); got != tc.want {
			t.Errorf("%q → %s, want %s", tc.req, got, tc.want)
		}
	}
	// Empty and whitespace-only lines get no answer
	c.send("\n   \n\t\r\n\v\f\n")
	if got := c.ask(`{"id": 5, "cmd": "take"}` + "\n"); got != `{"id":5,"ok":true}` {
		t.Fatalf("after empty lines: %s", got)
	}
	// A line of exactly MaxLine bytes is still a request
	pad := MaxLine - len(`{"id":6,"cmd":"take","p":""}`)
	if got := c.ask(`{"id":6,"cmd":"take","p":"` + strings.Repeat("a", pad) + `"}` + "\n"); got != `{"id":6,"ok":true}` {
		t.Fatalf("MaxLine line: %s", got)
	}
	// Several requests in one write, each answered in order
	c.send(`{"id":7,"cmd":"take"}` + "\n" + `{"id":8,"cmd":"nope"}` + "\n")
	for _, want := range []string{`{"id":7,"ok":true}`, `{"id":8,"ok":false,"error":"unknown_command"}`} {
		if got, err := c.line(3 * time.Second); err != nil || got != want {
			t.Fatalf("pipelined: %q %v, want %s", got, err, want)
		}
	}
}

func TestLineOneByteOverMaxLineCloses(t *testing.T) {
	s, _ := start(t, nil)
	c := dial(t, s)
	pad := MaxLine + 1 - len(`{"id":6,"cmd":"take","p":""}`)
	c.send(`{"id":6,"cmd":"take","p":"` + strings.Repeat("a", pad) + `"}` + "\n")
	if l, err := c.line(3 * time.Second); !closedErr(err) {
		t.Fatalf("got %q %v, want EOF without a reply", l, err)
	}
}

func TestHandlerTimeout(t *testing.T) {
	dir := tempDir(t)
	s := New(Config{SocketPath: SocketPath(dir)})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	release := make(chan struct{})
	ran := make(chan map[string]any, 4)
	engineLoop(t, s, func(cmd string, msg map[string]any) (bool, string) {
		if cmd == "slow" {
			<-release // the engine's loop is stuck in this command
		}
		ran <- msg
		return true, ""
	})
	c1, c2 := dial(t, s), dial(t, s)
	c1.send(`{"id": 1, "cmd": "slow"}` + "\n")
	time.Sleep(100 * time.Millisecond) // the engine is inside "slow"
	t0 := time.Now()
	c2.send(`{"id": 5, "cmd": "take"}` + "\n") // queued behind it
	got, err := c2.line(4 * time.Second)
	if el := time.Since(t0); el < CommandTimeout-100*time.Millisecond || el > CommandTimeout+700*time.Millisecond {
		t.Fatalf("answered after %v, want ~%v", el, CommandTimeout)
	}
	if err != nil || got != `{"id":5,"ok":false,"error":"timeout"}` {
		t.Fatalf("queued: %q %v", got, err)
	}
	if got, err := c1.line(time.Second); err != nil || got != `{"id":1,"ok":false,"error":"timeout"}` {
		t.Fatalf("stuck: %q %v", got, err)
	}
	close(release)
	// Both still run later; their late replies are dropped
	for _, want := range []string{"1", "5"} {
		select {
		case msg := <-ran:
			if msg["id"] != json.Number(want) {
				t.Fatalf("ran %v, want id %s", msg, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("request %s never ran", want)
		}
	}
	// The next answer is for the next request, not a stale one
	if got := c2.ask(`{"id": 6, "cmd": "take"}` + "\n"); got != `{"id":6,"ok":true}` {
		t.Fatalf("after timeout: %s", got)
	}
}

func TestReplyNeverBlocks(t *testing.T) {
	r := &Request{reply: make(chan Reply, 1)}
	r.Reply(false, "on_air")
	r.Reply(true, "") // second reply: discarded, no block
	if got := <-r.reply; got != (Reply{OK: false, Error: "on_air"}) {
		t.Fatalf("got %+v", got)
	}
}

type testState struct {
	V       int    `json:"v"`
	Program string `json:"program"`
}

func TestPublishDedup(t *testing.T) {
	dir := tempDir(t)
	path := StatePath(dir)
	s := New(Config{StatePath: path})
	for i, step := range []struct {
		st   any
		want bool
	}{
		{testState{1, "a"}, true},
		{testState{1, "a"}, false},
		{json.RawMessage(`{"v": 1, "program": "a"}`), false}, // same JSON once compacted
		{[]byte(`{"v":1,"program":"b"}`), true},
		{map[string]any{"program": "b", "v": 1}, true}, // bytes compared: another key order is a change
		{testState{1, "<&>"}, true},
	} {
		changed, err := s.Publish(step.st)
		if err != nil || changed != step.want {
			t.Fatalf("step %d: changed %v err %v, want %v", i, changed, err, step.want)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != `{"v":1,"program":"<&>"}` {
		t.Fatalf("mixer.json %q %v", b, err) // no HTML escaping, like Python's ensure_ascii=False
	}
	if string(s.State()) != string(b) {
		t.Fatalf("cache %s", s.State())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp left behind: %v", err)
	}
	for _, bad := range []any{nil, []int{1}, json.RawMessage(`"x"`), json.RawMessage(`{`), func() {}} {
		if changed, err := s.Publish(bad); err == nil || changed {
			t.Fatalf("Publish(%v): %v %v, want an error", bad, changed, err)
		}
	}
}

func TestStateFileModeAndAtomicity(t *testing.T) {
	dir := tempDir(t)
	path := StatePath(dir)
	s := New(Config{StatePath: path})
	old := syscall.Umask(0o077) // the mode must not depend on the umask
	defer syscall.Umask(old)
	if _, err := s.Publish(testState{1, "a"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v %v, want 0644", fi.Mode(), err)
	}
	f, err := os.Open(path) // a reader with the old file open
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := s.Publish(testState{2, "b"}); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	if string(b) != `{"v":1,"program":"a"}` {
		t.Fatalf("the open file changed under the reader: %q (not replaced by rename)", b)
	}
	b, _ = os.ReadFile(path)
	if string(b) != `{"v":2,"program":"b"}` {
		t.Fatalf("new file %q", b)
	}
	fi, _ = os.Stat(path)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode after replace %v", fi.Mode())
	}
}

func TestStateWriteFailureStillBroadcasts(t *testing.T) {
	var logb bytes.Buffer
	elog.SetOutput(&logb)
	defer elog.SetOutput(nil)
	dir := tempDir(t)
	s := New(Config{SocketPath: SocketPath(dir), StatePath: filepath.Join(dir, "missing", "mixer.json")})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := dial(t, s)
	c.ask(`{"cmd":"subscribe"}` + "\n")
	for i := range 3 {
		if changed, err := s.Publish(testState{i, "a"}); !changed || err != nil {
			t.Fatalf("publish %d: %v %v", i, changed, err)
		}
		got, err := c.line(3 * time.Second)
		if err != nil || got != fmt.Sprintf(`{"event":"state","state":{"v":%d,"program":"a"}}`, i) {
			t.Fatalf("event %d: %q %v", i, got, err)
		}
	}
	if n := strings.Count(logb.String(), "event=state_write_failed"); n != 1 {
		t.Fatalf("logged %d times, want once per streak:\n%s", n, logb.String())
	}
}

func TestListenFailureKeepsPublishing(t *testing.T) {
	var logb bytes.Buffer
	elog.SetOutput(&logb)
	defer elog.SetOutput(nil)
	dir := tempDir(t)
	s := New(Config{SocketPath: filepath.Join(dir, "missing", "mixer.sock"), StatePath: StatePath(dir)})
	if err := s.Listen(); err == nil {
		t.Fatal("Listen in a missing dir must fail")
	}
	if !strings.Contains(logb.String(), "[mixer] event=socket_unavailable") {
		t.Fatalf("log: %q", logb.String())
	}
	if changed, err := s.Publish(testState{1, "a"}); !changed || err != nil {
		t.Fatalf("publish: %v %v", changed, err)
	}
	if _, err := os.Stat(StatePath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	dir := tempDir(t)
	path := SocketPath(dir)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SocketPath: path})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Listen(); err == nil {
		t.Fatal("a second Listen must fail")
	}
	c := dial(t, s)
	if got := c.ask(`{"cmd":"get"}` + "\n"); got != `{"ok":true,"state":{}}` {
		t.Fatalf("get: %s", got)
	}
}

func TestSubscribeGetsStateNowAndOnChange(t *testing.T) {
	s, _ := start(t, nil)
	sub, other := dial(t, s), dial(t, s)
	if got := sub.ask(`{"id":"s","cmd":"subscribe"}` + "\n"); got != `{"id":"s","ok":true,"state":{}}` {
		t.Fatalf("subscribe before any publish: %s", got)
	}
	if got := other.ask(`{"cmd":"get"}` + "\n"); got != `{"ok":true,"state":{}}` {
		t.Fatalf("get: %s", got)
	}
	expect := func(c *conn, want string) {
		t.Helper()
		if got, err := c.line(3 * time.Second); err != nil || got != want {
			t.Fatalf("got %q %v, want %s", got, err, want)
		}
	}
	quiet := func(c *conn) {
		t.Helper()
		if got, err := c.line(200 * time.Millisecond); err == nil {
			t.Fatalf("unexpected line %s", got)
		}
	}
	s.Publish(testState{1, "a"})
	expect(sub, `{"event":"state","state":{"v":1,"program":"a"}}`)
	quiet(other) // get does not subscribe
	s.Publish(testState{1, "a"})
	quiet(sub) // no change, no event
	s.Publish(testState{2, "b"})
	expect(sub, `{"event":"state","state":{"v":2,"program":"b"}}`)
	late := dial(t, s)
	if got := late.ask(`{"id":3,"cmd":"subscribe"}` + "\n"); got != `{"id":3,"ok":true,"state":{"v":2,"program":"b"}}` {
		t.Fatalf("late subscribe: %s", got)
	}
	s.Publish(testState{3, "c"})
	expect(sub, `{"event":"state","state":{"v":3,"program":"c"}}`)
	expect(late, `{"event":"state","state":{"v":3,"program":"c"}}`)
	sub.ask(`{"cmd":"subscribe"}` + "\n") // subscribing twice does not duplicate events
	s.Publish(testState{4, "d"})
	expect(sub, `{"event":"state","state":{"v":4,"program":"d"}}`)
	quiet(sub)
}

func TestConcurrentPublishWithSlowClient(t *testing.T) {
	s, _ := start(t, nil)
	slow := dial(t, s)
	slow.ask(`{"cmd":"subscribe"}` + "\n") // and never reads again
	good := dial(t, s)
	good.ask(`{"cmd":"subscribe"}` + "\n")
	asker := dial(t, s)

	// Phase 1: big states, each one read by the good client before the next. The slow one fills its
	// socket buffer (a few KB on macOS, ~200 KB on Linux) and then MaxOut: dropped.
	big := strings.Repeat("b", 200<<10)
	for i := range 6 {
		s.Publish(map[string]any{"i": i, "big": big})
		if l, err := good.line(5 * time.Second); err != nil || !strings.Contains(l, fmt.Sprintf(`"i":%d`, i)) {
			t.Fatalf("good client, big state %d: %.80q %v", i, l, err)
		}
	}
	waitClients := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			s.mu.Lock()
			n := len(s.clients)
			s.mu.Unlock()
			if n == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d clients, want %d", n, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitClients(2) // the slow one is out, the good one and the asker are in

	// Phase 2: concurrent publishers, a client reading events and one asking all the time (-race)
	last := make(chan string, 1)
	go func() { // keeps the last event; 2 s of quiet = the end
		var l string
		for {
			got, err := good.line(2 * time.Second)
			if err != nil {
				last <- l
				return
			}
			l = got
		}
	}()
	stopAsk := make(chan struct{})
	askDone := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stopAsk:
				askDone <- nil
				return
			default:
			}
			asker.send(fmt.Sprintf(`{"id":%d,"cmd":"get"}`+"\n", i))
			if _, err := asker.line(3 * time.Second); err != nil {
				askDone <- err
				return
			}
		}
	}()
	pad := strings.Repeat("p", 512)
	finished := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				for i := range 100 {
					if _, err := s.Publish(map[string]any{"g": g, "i": i, "pad": pad}); err != nil {
						t.Error(err)
					}
					time.Sleep(2 * time.Millisecond)
				}
			})
		}
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("Publish blocked")
	}
	close(stopAsk)
	if err := <-askDone; err != nil {
		t.Fatalf("the asking client was disturbed: %v", err)
	}
	final := `{"event":"state","state":` + string(s.State()) + `}`
	if l := <-last; l != final {
		t.Fatalf("the good client's last event is not the last state:\n%.120s\n%.120s", l, final)
	}
	waitClients(2)
	// The slow one was closed: once drained, EOF
	slow.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, slow.r); err != nil {
		t.Fatalf("slow client: %v, want EOF", err)
	}
}

func TestCloseIsBounded(t *testing.T) {
	dir := tempDir(t)
	s := New(Config{SocketPath: SocketPath(dir)})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	// Nobody reads Requests(): a command waits for the engine
	waiting := dial(t, s)
	waiting.send(`{"id":1,"cmd":"take"}` + "\n")
	// A subscriber that never reads, with its writer stuck in Write
	stuck := dial(t, s)
	stuck.ask(`{"cmd":"subscribe"}` + "\n")
	big := strings.Repeat("x", 100<<10)
	s.Publish(map[string]any{"a": big})
	s.Publish(map[string]any{"b": big})
	time.Sleep(200 * time.Millisecond)
	t0 := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(t0); el > closeWait {
		t.Fatalf("Close took %v", el)
	}
	if _, err := os.Stat(SocketPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
	if l, err := waiting.line(time.Second); !closedErr(err) {
		t.Fatalf("waiting client: %q %v, want EOF", l, err)
	}
	stuck.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.Copy(io.Discard, stuck.r); err != nil {
		t.Fatalf("stuck client: %v, want EOF", err)
	}
	if err := s.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
	if _, err := net.Dial("unix", SocketPath(dir)); err == nil {
		t.Fatal("still accepting after Close")
	}
}

// The app's real client (manager/internal/mixer) against this server: get, subscribe, commands.
func TestMixerClientRoundTrip(t *testing.T) {
	dir := tempDir(t)
	s := New(Config{SocketPath: SocketPath(dir), StatePath: StatePath(dir)})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	str := func(v string) *string { return &v }
	st := mixer.State{V: 1, Program: mixer.Program{Main: str("hdmi"), Layout: "full", Transition: "cut",
		Hidden: []string{}, Width: 1920, Height: 1080, FPS: 30}, Slots: 4,
		Sources: []mixer.Source{{ID: "hdmi", Slot: 1, Kind: "hdmi-csi", Name: "HDMI de la placa", State: "live"},
			{ID: "test", Slot: 2, Kind: "test", Name: "Barras", State: "live"}},
		Screen: mixer.Screen{State: "off"}, Output: &mixer.Output{Mode: "off", State: "off", By: "start"}}
	if _, err := s.Publish(st); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []map[string]any
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() { // the engine: owns st, runs commands and publishes after each one
		for {
			select {
			case req := <-s.Requests():
				mu.Lock()
				got = append(got, req.Msg)
				mu.Unlock()
				src, _ := req.Msg["source"].(string)
				if req.Cmd != "take" || (src != "hdmi" && src != "test") {
					req.Reply(false, "unknown_source")
					continue
				}
				st.Program.Main = &src
				req.Reply(true, "")
				s.Publish(st)
			case <-stop:
				return
			}
		}
	}()

	cl := &mixer.Client{Sock: SocketPath(dir), StatePath: StatePath(dir), RunDir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states := make(chan *mixer.State, 16)
	watchErr := make(chan error, 1)
	go func() { watchErr <- cl.Watch(ctx, func(st *mixer.State) { states <- st }) }()
	next := func() *mixer.State {
		t.Helper()
		select {
		case st := <-states:
			return st
		case <-time.After(3 * time.Second):
			t.Fatal("no state from Watch")
			return nil
		}
	}
	if first := next(); first.V != 1 || first.Program.Main == nil || *first.Program.Main != "hdmi" || len(first.Sources) != 2 {
		t.Fatalf("first state: %+v", first)
	}
	if err := cl.Do(ctx, mixer.Command{Cmd: "take", Source: "test"}); err != nil {
		t.Fatalf("take: %v", err)
	}
	if after := next(); *after.Program.Main != "test" || after.Output == nil || after.Output.By != "start" {
		t.Fatalf("pushed state: %+v", after)
	}
	read, err := cl.Read()
	if err != nil || *read.Program.Main != "test" {
		t.Fatalf("Read: %+v %v", read, err)
	}
	var ce *mixer.CommandError
	if err := cl.Do(ctx, mixer.Command{Cmd: "take", Source: "net:cam1"}); !errors.As(err, &ce) || ce.Code != "unknown_source" {
		t.Fatalf("take unknown: %v", err)
	}
	mu.Lock()
	want := []map[string]any{{"id": json.Number("1"), "cmd": "take", "source": "test"},
		{"id": json.Number("1"), "cmd": "take", "source": "net:cam1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("engine got %v, want %v", got, want)
	}
	mu.Unlock()
	select {
	case st := <-states:
		t.Fatalf("a rejected command published a state: %+v", st)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	if err := <-watchErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch: %v", err)
	}
	s.Close()
	if _, err := cl.Read(); err != nil { // mixer.json stays: the RuntimeDirectory removes it, not us
		t.Fatalf("Read after Close: %v", err)
	}
	if err := cl.Do(context.Background(), mixer.Command{Cmd: "swap"}); !errors.Is(err, mixer.ErrOff) {
		t.Fatalf("Do after Close: %v, want ErrOff", err)
	}
}

// closedErr: the server closed the connection. Linux answers a close with unread input with a reset
// (ECONNRESET) instead of a FIN, so the client sees either.
func closedErr(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET)
}
