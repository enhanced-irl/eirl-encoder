package wifi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeNM: a fake NetworkManager. It keeps the passwords it receives to check that they only arrive here.
type fakeNM struct {
	mu        sync.Mutex
	dev       devState
	aps       []apInfo
	profs     []profile
	psk       map[string]string // profile → password
	scans     int
	autoconn  *bool
	discs     int
	activated []string
	sigs      chan stateChange // nil: no signal (the client polls)
	devErr    error            // device() fails (NetworkManager not answering)
}

func (f *fakeNM) stateChanges(context.Context) (<-chan stateChange, error) {
	if f.sigs == nil {
		return nil, errors.New("no signals")
	}
	return f.sigs, nil
}

func (f *fakeNM) device(context.Context) (devState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dev, f.devErr
}
func (f *fakeNM) requestScan(context.Context) error {
	f.mu.Lock()
	f.scans++
	f.mu.Unlock()
	return errors.New("Scanning not allowed immediately")
}
func (f *fakeNM) accessPoints(context.Context) ([]apInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apInfo(nil), f.aps...), nil
}
func (f *fakeNM) profiles(context.Context) ([]profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]profile(nil), f.profs...), nil
}
func (f *fakeNM) addAndActivate(_ context.Context, p newProfile, ap string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := "/conn/" + p.ID
	f.profs = append(f.profs, profile{Path: path, ID: p.ID, SSID: p.SSID})
	if f.psk == nil {
		f.psk = map[string]string{}
	}
	f.psk[path] = p.PSK
	f.activated = append(f.activated, path+"@"+ap)
	return nil
}
func (f *fakeNM) update(_ context.Context, path string, p newProfile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.psk[path] = p.PSK
	return nil
}
func (f *fakeNM) activate(_ context.Context, path, ap string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activated = append(f.activated, path+"@"+ap)
	return nil
}
func (f *fakeNM) remove(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.profs {
		if p.Path == path {
			f.profs = append(f.profs[:i], f.profs[i+1:]...)
			return nil
		}
	}
	return errors.New("does not exist")
}
func (f *fakeNM) disconnect(context.Context) error { f.mu.Lock(); f.discs++; f.mu.Unlock(); return nil }
func (f *fakeNM) setAutoconnect(_ context.Context, on bool) error {
	f.mu.Lock()
	f.autoconn = &on
	f.mu.Unlock()
	return nil
}
func (f *fakeNM) set(d devState) { f.mu.Lock(); f.dev = d; f.mu.Unlock() }

const psk, rsn = 0x100, 0x200

func testClient() (*Client, *fakeNM) {
	f := &fakeNM{dev: devState{State: devDisconnected}, aps: []apInfo{
		{Path: "/ap/1", SSID: []byte("Corner Cafe"), Strength: 70, Freq: 2437, RSN: psk},
		{Path: "/ap/2", SSID: []byte("Corner Cafe"), Strength: 40, Freq: 5180, RSN: psk},
		{Path: "/ap/3", SSID: []byte("Hotel"), Strength: 55, Freq: 2412},
		{Path: "/ap/4", SSID: []byte("Office"), Strength: 50, Freq: 2462, RSN: rsn},
		{Path: "/ap/5", SSID: []byte("WPA3 only"), Strength: 45, Freq: 5200, RSN: 0x400},
		{Path: "/ap/6", SSID: []byte(""), Strength: 90, Freq: 2412, RSN: psk},                 // hidden
		{Path: "/ap/7", SSID: []byte("EnhancedIRL-A1B2"), Strength: 99, Freq: 2412, RSN: psk}, // ours
		{Path: "/ap/8", SSID: []byte("Old"), Strength: 20, Freq: 2412, Flags: 1},
	}}
	c := &Client{Iface: "wlan0", b: f, OwnSSID: func() string { return "EnhancedIRL-A1B2" }}
	return c, f
}

func TestValidation(t *testing.T) {
	for _, s := range []string{"Corner Cafe", "Zoë 5G", "x", "12345678901234567890123456789012"} {
		if !ValidSSID(s) {
			t.Errorf("SSID %q rejected", s)
		}
	}
	for _, s := range []string{"", "123456789012345678901234567890123", "a\nb", string([]byte{0xff, 0xfe})} {
		if ValidSSID(s) {
			t.Errorf("SSID %q accepted", s)
		}
	}
	for _, p := range []string{"12345678", "with spaces ?!", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if !ValidPassword(p) {
			t.Errorf("password %q rejected", p)
		}
	}
	for _, p := range []string{"1234567", "pässword1", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg", string(make([]byte, 64))} {
		if ValidPassword(p) {
			t.Errorf("password %q accepted", p)
		}
	}
}

// The list: one per SSID (the strongest), without the hidden one or ours, with its security, band and channel
func TestScan(t *testing.T) {
	c, f := testClient()
	nets, _, err := c.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Network{}
	for _, n := range nets {
		got[n.SSID] = n
	}
	if len(nets) != 5 || got["Corner Cafe"].Signal != 70 || got["Corner Cafe"].Band != "2.4" || got["Corner Cafe"].Channel != 6 {
		t.Fatalf("list: %+v", nets)
	}
	for ssid, sec := range map[string]string{"Corner Cafe": SecWPA2, "Hotel": SecOpen, "Office": SecEnterprise, "WPA3 only": SecWPA3, "Old": SecWEP} {
		if got[ssid].Security != sec {
			t.Errorf("%s: %s, not %s", ssid, got[ssid].Security, sec)
		}
	}
	if _, ok := got["EnhancedIRL-A1B2"]; ok {
		t.Error("our AP is listed")
	}
	// It requests a scan in the background, at most every 10 s (NM's rejection does not matter)
	time.Sleep(20 * time.Millisecond)
	c.Scan(context.Background())
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	n := f.scans
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("scans requested: %d", n)
	}
	// Nothing yet: scan_busy
	c2, f2 := testClient()
	f2.aps = nil
	if _, _, err := c2.Scan(context.Background()); !errors.Is(err, ErrScanBusy) {
		t.Errorf("no networks: %v", err)
	}
}

// Connect: the password only goes to NetworkManager; a known one without a password uses its profile; unsupported ones, no
func TestConnect(t *testing.T) {
	c, f := testClient()
	ctx := context.Background()
	pw := "secret123"
	short := "short"
	for _, tc := range []struct {
		ssid string
		pw   *string
		err  error
	}{
		{"Corner Cafe", nil, ErrInvalid}, // secured, unknown and without a password
		{"Corner Cafe", &short, ErrInvalid},
		{"Missing", &pw, ErrNotFound},
		{"Office", &pw, ErrUnsupported},
		{"WPA3 only", &pw, ErrUnsupported},
		{"Old", &pw, ErrUnsupported},
	} {
		if err := c.Connect(ctx, tc.ssid, tc.pw); !errors.Is(err, tc.err) {
			t.Errorf("%s: %v, not %v", tc.ssid, err, tc.err)
		}
	}
	if err := c.Connect(ctx, "Corner Cafe", &pw); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.profs) != 1 || f.profs[0].ID != "eirl-wifi-1" || f.psk["/conn/eirl-wifi-1"] != pw || f.activated[0] != "/conn/eirl-wifi-1@/ap/1" {
		t.Fatalf("profile: %+v %v %v", f.profs, f.psk, f.activated)
	}
	f.mu.Unlock()
	// Known, without a password: the saved profile (its password untouched)
	if err := c.Connect(ctx, "Corner Cafe", nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.profs) != 1 || f.psk["/conn/eirl-wifi-1"] != pw || len(f.activated) != 2 {
		t.Errorf("known without a password: %+v %v %v", f.profs, f.psk, f.activated)
	}
	f.mu.Unlock()
	// Known with another password: it is changed
	pw2 := "otherkey99"
	c.Connect(ctx, "Corner Cafe", &pw2)
	f.mu.Lock()
	if f.psk["/conn/eirl-wifi-1"] != pw2 {
		t.Error("the password was not changed")
	}
	f.mu.Unlock()
	// Open: no password, profile without security
	if err := c.Connect(ctx, "Hotel", nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.psk["/conn/eirl-wifi-2"] != "" {
		t.Error("an open one with a password")
	}
	f.mu.Unlock()
	// Forget
	if err := c.Forget(ctx, "Hotel"); err != nil || c.Forget(ctx, "Hotel") != ErrNotFound {
		t.Errorf("forget: %v", err)
	}
}

// An attempt's outcome: connected, or error with the reason (and the profile stays)
func TestConnectWatch(t *testing.T) {
	old := connectWatch
	connectWatch = 5 * time.Second
	defer func() { connectWatch = old }()
	c, f := testClient()
	ctx := context.Background()
	pw := "wrongpass1"
	c.Connect(ctx, "Corner Cafe", &pw)
	f.set(devState{State: 70, Active: &f.aps[0]})
	time.Sleep(700 * time.Millisecond)
	if st, _ := c.State(ctx); st.State != "connecting" || st.SSID == nil || *st.SSID != "Corner Cafe" {
		t.Fatalf("connecting: %+v", st)
	}
	f.set(devState{State: devDisconnected, Reason: reasonNoSecrets})
	time.Sleep(700 * time.Millisecond)
	st, _ := c.State(ctx)
	if st.State != "error" || st.Error != "wrong_password" {
		t.Fatalf("wrong password: %+v", st)
	}
	f.mu.Lock()
	n := len(f.profs)
	f.mu.Unlock()
	if n != 1 {
		t.Error("the profile does not stay to be corrected")
	}
	// Corrected: connected, without the error
	pw = "rightpass1"
	c.Connect(ctx, "Corner Cafe", &pw)
	f.set(devState{State: devActivated, Active: &f.aps[0], IP: "192.168.1.50"})
	time.Sleep(700 * time.Millisecond)
	st, _ = c.State(ctx)
	if st.State != "connected" || st.Error != "" || *st.IP != "192.168.1.50" || *st.Channel != 6 || *st.Band != "2.4" || st.Internet != nil {
		t.Fatalf("connected: %+v", st)
	}
}

// Internet: Monitor checks it from the client wifi's IP; «no internet» after two failures in a row, back with good
// ones; null until known, and cleared on disconnect
func TestInternet(t *testing.T) {
	old := checkInternet
	defer func() { checkInternet = old }()
	var from, to string
	ok := false
	checkInternet = func(_ context.Context, url, ip string) bool { from, to = ip, url; return ok }
	c, f := testClient()
	c.InternetURL = func() string { return "https://eu1.relay.example/ping" }
	f.set(devState{State: devActivated, Active: &f.aps[0], IP: "192.168.1.50"})
	ctx := context.Background()
	if st, _ := c.State(ctx); st.Internet != nil {
		t.Fatal("before checking, null")
	}
	if c.checkNow(ctx) {
		t.Error("a single failure already takes it out")
	}
	if st, _ := c.State(ctx); st.Internet != nil {
		t.Error("with one failure, still null")
	}
	if !c.checkNow(ctx) || from != "192.168.1.50" || to != "https://eu1.relay.example/ping" {
		t.Fatalf("two failures: out (%s → %s)", from, to)
	}
	if st, _ := c.State(ctx); st.Internet == nil || *st.Internet {
		t.Error("state: no internet")
	}
	// Coming back also takes two good ones in a row and 60 s since the last change (hysteresis: each change shakes the stream)
	t0 := time.Now()
	c.Now = func() time.Time { return t0.Add(30 * time.Second) }
	ok = true
	if !c.checkNow(ctx) || !c.checkNow(ctx) {
		t.Error("comes back before 60 s")
	}
	c.Now = func() time.Time { return t0.Add(70 * time.Second) }
	if c.checkNow(ctx) {
		t.Error("two good in a row and over 60 s: it comes back")
	}
	if st, _ := c.State(ctx); st.Internet == nil || !*st.Internet {
		t.Error("state: with internet")
	}
	// And to go out again: two bad in a row and 60 s; a good one in between breaks the streak
	c.Now = func() time.Time { return t0.Add(140 * time.Second) }
	ok = false
	c.checkNow(ctx)
	ok = true
	c.checkNow(ctx)
	ok = false
	if c.checkNow(ctx) {
		t.Error("the streak was not broken")
	}
	if !c.checkNow(ctx) {
		t.Error("two bad in a row and over 60 s: out")
	}
	// Monitor (new client): notifies at the start and on every change; disconnected, it is not «no internet»
	c2, f2 := testClient()
	c2.InternetURL = c.InternetURL
	f2.set(devState{State: devActivated, Active: &f2.aps[0], IP: "192.168.1.51"})
	ok = false
	var got []bool
	var mu sync.Mutex
	mctx, cancel := context.WithCancel(ctx)
	go c2.Monitor(mctx, 20*time.Millisecond, func(off bool) error { mu.Lock(); got = append(got, off); mu.Unlock(); return nil })
	time.Sleep(100 * time.Millisecond)
	f2.set(devState{State: devDisconnected})
	time.Sleep(60 * time.Millisecond)
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0] || !got[1] || got[2] {
		t.Errorf("notifications: %v (want: false at the start, true after two failures, false on disconnect)", got)
	}
}

// «Join a wifi»: off disconnects and does not reconnect on its own; on, it does
func TestSetEnabled(t *testing.T) {
	c, f := testClient()
	ctx := context.Background()
	c.SetEnabled(ctx, false)
	if f.discs != 1 || f.autoconn == nil || *f.autoconn {
		t.Errorf("switch off: %d %v", f.discs, f.autoconn)
	}
	c.SetEnabled(ctx, true)
	if !*f.autoconn {
		t.Error("switch on")
	}
}

// The password does not show up in anything the app shows
func TestNoSecrets(t *testing.T) {
	c, f := testClient()
	ctx := context.Background()
	pw := "keepitsafe1"
	c.Connect(ctx, "Corner Cafe", &pw)
	f.set(devState{State: devActivated, Active: &f.aps[0], IP: "10.0.0.2"})
	nets, _, _ := c.Scan(ctx)
	st, _ := c.State(ctx)
	for _, v := range []any{nets, st} {
		if bytes.Contains([]byte(fmtAny(v)), []byte(pw)) {
			t.Errorf("the password shows up in %T", v)
		}
	}
}

func fmtAny(v any) string { return fmt.Sprintf("%+v", v) }

// With the signal: FAILED carries NO_SECRETS and right away moves to DISCONNECTED with reason 0 (NM 1.10). Polling
// missed it and gave connect_failed: with the signal, wrong_password
func TestConnectWatchSignals(t *testing.T) {
	c, f := testClient()
	f.sigs = make(chan stateChange, 8)
	ctx := context.Background()
	pw := "wrongpass1"
	if err := c.Connect(ctx, "Corner Cafe", &pw); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []stateChange{{devPrepare, 0}, {60, 0}, {devFailed, reasonNoSecrets}, {devDisconnected, 0}} {
		f.sigs <- ch
	}
	time.Sleep(100 * time.Millisecond)
	st, _ := c.State(ctx)
	if st.State != "error" || st.Error != "wrong_password" || st.SSID == nil || *st.SSID != "Corner Cafe" {
		t.Fatalf("wrong password through the signal: %+v", st)
	}
}

// When switching networks, the previous one stays activated for a moment: that is not «connected» to the new one
func TestConnectWatchNetworkSwitch(t *testing.T) {
	old := connectWatch
	connectWatch = 5 * time.Second
	defer func() { connectWatch = old }()
	c, f := testClient()
	ctx := context.Background()
	f.set(devState{State: devActivated, Active: &f.aps[2]}) // connected to «Hotel»
	pw := "wrongpass1"
	c.Connect(ctx, "Corner Cafe", &pw)
	time.Sleep(700 * time.Millisecond) // the first look still sees «Hotel» activated
	f.set(devState{State: 70, Active: &f.aps[0]})
	time.Sleep(700 * time.Millisecond)
	f.set(devState{State: devFailed, Reason: reasonNoSecrets})
	time.Sleep(700 * time.Millisecond)
	if st, _ := c.State(ctx); st.State != "error" || st.Error != "wrong_password" {
		t.Fatalf("the new network's failure was lost: %+v", st)
	}
}

// Transient things do not skip the hysteresis: with the wifi out of the split, NetworkManager not answering, a new
// DHCP IP or having nowhere to check against do not put it back in
func TestInternetTransient(t *testing.T) {
	old := checkInternet
	defer func() { checkInternet = old }()
	checkInternet = func(context.Context, string, string) bool { return false }
	c, f := testClient()
	url := "https://eu1.relay.example/ping"
	c.InternetURL = func() string { return url }
	ctx := context.Background()
	f.set(devState{State: devActivated, Active: &f.aps[0], IP: "192.168.1.50"})
	c.checkNow(ctx)
	if !c.checkNow(ctx) {
		t.Fatal("two failures: out")
	}
	f.mu.Lock()
	f.devErr = errors.New("NetworkManager not answering")
	f.mu.Unlock()
	if !c.checkNow(ctx) {
		t.Error("a NetworkManager error puts it back in")
	}
	f.mu.Lock()
	f.devErr = nil
	f.mu.Unlock()
	url = ""
	if !c.checkNow(ctx) {
		t.Error("with nowhere to check against, it puts it back in")
	}
	url = "https://eu1.relay.example/ping"
	f.set(devState{State: devActivated, Active: &f.aps[0], IP: "192.168.1.77"})
	if !c.checkNow(ctx) {
		t.Error("a new DHCP IP puts it back in")
	}
	f.set(devState{State: devDisconnected})
	if c.checkNow(ctx) {
		t.Error("disconnected, still out")
	}
}

// If it cannot write (onChange fails), it is retried on the next round
func TestMonitorRetries(t *testing.T) {
	c, f := testClient()
	f.set(devState{State: devDisconnected})
	var calls int
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	go c.Monitor(ctx, 10*time.Millisecond, func(bool) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return errors.New("disk full")
		}
		return nil
	})
	time.Sleep(150 * time.Millisecond)
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Errorf("calls: %d (two failed and one good, then none without changes)", calls)
	}
}
