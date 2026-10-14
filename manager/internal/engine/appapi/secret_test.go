package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// noLeak fails if s shows up in v printed or marshalled in any way.
func noLeak(t *testing.T, s string, vs ...any) {
	t.Helper()
	for _, v := range vs {
		j, _ := json.Marshal(v)
		for _, out := range []string{string(j), fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v)} {
			if strings.Contains(out, s) {
				t.Errorf("%q leaks in %s", s, out)
			}
		}
	}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// The typed password (WIFI.md §6) never appears in a snapshot, an error string, a log line or a panic message, and
// its bytes are zeroed once sent.
func TestPasswordNeverLeaks(t *testing.T) {
	const pw = "Secreto-4242"
	var logs syncBuf
	elog.SetOutput(&logs)
	defer elog.SetOutput(nil)
	a := newFakeApp(t)
	a.route("POST /network/wifi/connect", 409, map[string]any{"error": "uplink_in_use", "message": "la wifi lleva la emisión"})
	n := New(nil, a.sock)
	n.PollStatus()
	typed := []byte(pw)
	if !n.Connect("Bar", typed) {
		t.Fatal("connect")
	}
	clear(typed) // the screen wipes its own: NetState has a copy
	n.mu.Lock()
	held := n.secret.b
	n.mu.Unlock()
	if string(held) != pw {
		t.Fatalf("copy %q", held)
	}
	noLeak(t, pw, n.Snapshot(), n.secret, &n.secret)
	n.post(n.wanted(t))
	if a.last().body.(map[string]any)["password"] != pw {
		t.Fatal("the app did not get the password")
	}
	if !allZero(held) {
		t.Error("the password's bytes were not wiped after sending")
	}
	noLeak(t, pw, n.Snapshot())
	// Errors: the app does not answer, hangs, or refuses: never the body
	body := connectBody("Bar", newSecret([]byte(pw)))
	_, _, err := Call(context.Background(), filepath.Join(a.dir, "no"), "POST", "/network/wifi/connect", body, 0)
	if err == nil || strings.Contains(err.Error(), pw) {
		t.Errorf("error %v", err)
	}
	if !allZero(body) {
		t.Error("Call did not wipe the body")
	}
	dead := filepath.Join(a.dir, "h.sock")
	ls, err := net.Listen("unix", dead)
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()
	body = connectBody("Bar", newSecret([]byte(pw)))
	_, _, err = Call(context.Background(), dead, "POST", "/network/wifi/connect", body, 200*time.Millisecond)
	var e *APIError
	if !errors.As(err, &e) || e.Status != 0 || strings.Contains(err.Error()+e.Message, pw) || !allZero(body) {
		t.Errorf("hung: %#v, wiped %v", err, allZero(body))
	}
	gone := New(nil, filepath.Join(a.dir, "no"))
	gone.status = &Status{Enabled: true}
	gone.Connect("Bar", []byte(pw))
	gone.post(gone.wanted(t))
	noLeak(t, pw, gone.Snapshot())
	// A panic in the goroutine, even carrying the password, logs only its type
	m := New(nil, a.sock)
	m.PollStatus()
	m.Connect("Bar", []byte(pw))
	m.mu.Lock()
	held = m.secret.b
	m.mu.Unlock()
	var boom atomic.Int32
	m.postFn = func(c Change) {
		boom.Add(1)
		panic(fmt.Errorf("post %s: %s", c.Value, pw))
	}
	m.Start()
	deadline := time.Now().Add(3 * time.Second)
	for (boom.Load() == 0 || m.Snapshot().Pending != nil) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	m.Stop()
	if !allZero(held) {
		t.Error("the password's bytes were not wiped after the panic")
	}
	if l := logs.String(); l != "[screen] event=app_api_panic type=*errors.errorString\n" {
		t.Errorf("log %q", l)
	}
}

// A connect still queued when Settings closes is dropped with its password: it never reaches the app.
func TestQueuedConnectDroppedOnPanelClose(t *testing.T) {
	a := newFakeApp(t)
	n := New(nil, a.sock)
	n.PollStatus()
	n.SetPanel(true)
	if !n.Connect("Bar", []byte("Secreto-0000")) {
		t.Fatal("connect")
	}
	n.mu.Lock()
	held := n.secret.b
	n.mu.Unlock()
	n.SetPanel(false)
	if !allZero(held) {
		t.Error("the queued password was not wiped")
	}
	n.Start()
	deadline := time.Now().Add(3 * time.Second)
	for len(a.allSeen()) < 2 && time.Now().Before(deadline) { // the first GET /network of the goroutine
		time.Sleep(20 * time.Millisecond)
	}
	n.Stop()
	for _, r := range a.allSeen() {
		if r.method == "POST" {
			t.Errorf("sent %+v", r)
		}
	}
	if n.Snapshot().Pending != nil {
		t.Error("pending kept")
	}
	// And it can be asked again
	if !n.Connect("Bar", nil) {
		t.Error("a new change was refused")
	}
}

func TestConnectBody(t *testing.T) {
	for _, c := range []struct {
		ssid, pw string
		want     map[string]any
	}{
		{"Bar", "", map[string]any{"ssid": "Bar"}},
		{"Mi \"red\"", `a"b\c` + "\x01d", map[string]any{"ssid": "Mi \"red\"", "password": `a"b\c` + "\x01d"}},
	} {
		var got map[string]any
		if err := json.Unmarshal(connectBody(c.ssid, newSecret([]byte(c.pw))), &got); err != nil {
			t.Fatalf("%q: %v", c.pw, err)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%q → %v", c.pw, got)
		}
	}
}

// The 1 MiB cap: a longer answer does not parse and counts as {}.
func TestCallResponseCap(t *testing.T) {
	a := newFakeApp(t)
	a.route("GET /network", 200, map[string]any{"enabled": true, "pad": strings.Repeat("x", MaxBody)})
	n := New(nil, a.sock)
	n.lang.set("es")
	n.PollStatus()
	if s := n.Snapshot(); s.Status != nil || s.Error != "La app no da el estado de la red: respuesta inesperada" {
		t.Errorf("%+v", s)
	}
}
