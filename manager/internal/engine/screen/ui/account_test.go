package ui

import (
	"strings"
	"testing"
	"time"
)

// Device → Name & password: the name through the keyboard (the wizard's rule) and the web password: the current one,
// then the new one, both secret; they reach the app as secrets and leave the screen's memory.
func TestAccountPage(t *testing.T) {
	h, app := pagesHarness(t, "account")
	h.press("s_acct_name", 0.1)
	if h.u.kb == nil || string(h.u.kb.text) != "Estudio 2" {
		t.Fatalf("%+v", h.u.kb)
	}
	h.tapKey("BKSP", 1)
	for _, c := range "plato" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if a := app.lastDid(); a.What != "name" || a.Value != "plato" {
		t.Fatalf("%+v", a)
	}
	// The password: the current one, then the new one (at least 10)
	h.press("s_acct_pw", 0.1)
	if h.u.kb == nil || !h.u.kb.secret() {
		t.Fatal("no secret keyboard")
	}
	for _, c := range "viejaclave" {
		h.tapKey(string(c), 0.1)
	}
	first := h.u.kb.buf
	h.tapKey("OK", 0.1)
	if h.u.kb == nil || h.u.kb.spec == nil || h.u.kb.spec.min != WebPasswordMin || !allZero(first) {
		t.Fatalf("no new password's keyboard, or the current one stays in the first: %+v", h.u.kb)
	}
	cur := h.u.acct.cur
	for _, c := range "corta" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if len(app.did) != 1 {
		t.Fatal("a short password was sent")
	}
	for _, c := range "nuevaa" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	// Typed twice: a different one goes back to the new one's keyboard, saying why
	if len(app.did) != 1 || h.u.kb == nil || h.u.kb.spec.title != Spanish.WizPwAgain {
		t.Fatalf("no repetition: %+v", h.u.kb)
	}
	for _, c := range "otracosa" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	if len(app.did) != 1 || h.u.kb == nil || h.u.kb.spec.title != Spanish.Account.New || h.u.kb.err != Spanish.WizPwMismatch {
		t.Fatalf("a different repetition was taken: %+v", h.u.kb)
	}
	for _, s := range []string{"cortanuevaa", "cortanuevaa"} {
		for _, c := range s {
			h.tapKey(string(c), 0.1)
		}
		h.tapKey("OK", 0.1)
	}
	a := app.lastDid()
	sec := app.secrets[len(app.secrets)-1]
	if a.What != "password" || sec["current"] != "viejaclave" || sec["new"] != "cortanuevaa" {
		t.Fatalf("%+v %+v", a, sec)
	}
	if !allZero(cur) || h.u.acct.cur != nil || h.u.kb != nil {
		t.Fatal("the passwords stay in memory")
	}
	app.snap.Last = &AppResult{What: "password", OK: true, At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	if note, warn := h.u.accountNote(&app.snap); warn || note != Spanish.Account.PasswordChanged {
		t.Fatalf("%q %v", note, warn)
	}
	// Cancelling the second keyboard wipes the first password
	h.press("s_acct_pw", 0.1)
	for _, c := range "viejaclave" {
		h.tapKey(string(c), 0.1)
	}
	h.tapKey("OK", 0.1)
	cur = h.u.acct.cur
	h.tapKey("k_cancel", 0.1)
	if !allZero(cur) || h.u.acct.cur != nil {
		t.Fatal("cancelled, the current password stays")
	}
	// No password yet (the setup's): only the new one, through the setup
	app.snap.Setup.NeedsPassword = true
	h.press("s_acct_pw", 0.1)
	for range 2 {
		for _, c := range "primeraclave" {
			h.tapKey(string(c), 0.1)
		}
		h.tapKey("OK", 0.1)
	}
	if len(app.pw) != 1 || app.pw[0] != "primeraclave" {
		t.Fatalf("%+v", app.pw)
	}
}

// The secret keyboards draw dots unless their eye is open.
func TestAccountKeyboardEye(t *testing.T) {
	h, _ := drawHarness(t)
	pageApp(h, "account", nil)
	h.u.pageTouch("s_acct_pw", "", false)
	for _, c := range "secretazo" {
		h.u.kbKey(string(c), 0)
	}
	var drawn []string
	drawnText = func(s string) { drawn = append(drawn, s) }
	defer func() { drawnText = nil }()
	h.u.Dirty = true
	h.u.Tick()
	if strings.Contains(strings.Join(drawn, "|"), "secretazo") {
		t.Fatal("drawn with its eye closed")
	}
	h.u.kbKey("k_eye", 0)
	drawn = nil
	h.u.Dirty = true
	h.u.Tick()
	if !strings.Contains(strings.Join(drawn, "|"), "secretazo") {
		t.Fatalf("not shown with its eye open: %q", drawn)
	}
}

func TestCloudPage(t *testing.T) {
	h, app := pagesHarness(t, "cloud")
	h.u.Tick()
	if !app.watch().Cloud {
		t.Fatalf("%+v", app.watch())
	}
	app.snap.Cloud, app.snap.CloudAPI = &CloudInfo{State: "unlinked"}, Yes
	h.press("s_cloud_pair", 0.1)
	if app.lastDid().What != "cloud_pair" {
		t.Fatalf("%+v", app.did)
	}
	// Pairing: a new code, or cancel at once
	app.snap.Cloud = &CloudInfo{State: "pairing", Code: "K7QM-2XVD"}
	h.press("s_cloud_unlink", 0.1)
	if app.lastDid().What != "cloud_unlink" {
		t.Fatal("cancel")
	}
	// Linked: unlinking needs a second tap
	app.snap.Cloud = &CloudInfo{State: "linked", AccountLabel: "a@b.c"}
	app.did = nil
	h.press("s_cloud_leave", 0.1)
	if len(app.did) != 0 || !h.u.confirming("cloud_unlink") {
		t.Fatal("unlinked at the first tap")
	}
	h.press("s_cloud_leave", 0.1)
	if app.lastDid().What != "cloud_unlink" {
		t.Fatalf("%+v", app.did)
	}
	if h.u.cloudError("expired") == "expired" || h.u.cloudError("zzz") != "zzz" {
		t.Error("cloudError")
	}
}

func TestRenderCloudAndAccount(t *testing.T) {
	exp := time.Unix(100000, 0).Add(9 * time.Minute)
	on, off := true, false
	cloud := func(c *CloudInfo, api Tri) func(h *harness) {
		return func(h *harness) {
			pageApp(h, "cloud", func(a *AppSnapshot) { a.Cloud, a.CloudAPI = c, api })
		}
	}
	checkRenders(t, []renderView{
		{"cloud-asking", cloud(nil, Unknown)},
		{"cloud-old", cloud(nil, No)},
		{"cloud-disabled", cloud(&CloudInfo{State: "disabled"}, Yes)},
		{"cloud-unlinked", cloud(&CloudInfo{State: "unlinked"}, Yes)},
		{"cloud-unlinked-error", cloud(&CloudInfo{State: "unlinked", LastError: "hardware_claimed"}, Yes)},
		{"cloud-pairing", cloud(&CloudInfo{State: "pairing", Code: "K7QM-2XVD", ExpiresAt: &exp,
			LinkURL: "https://app.enhancedirl.com/encoders/link?pairing=K7QM-2XVD", LinkQR: testQR()}, Yes)},
		{"cloud-linked", cloud(&CloudInfo{State: "linked", AccountLabel: "estudio@example.com", Online: &on}, Yes)},
		{"cloud-offline", cloud(&CloudInfo{State: "linked", Online: &off}, Yes)},
		{"cloud-unlink", func(h *harness) {
			cloud(&CloudInfo{State: "linked", AccountLabel: "estudio@example.com", Online: &on}, Yes)(h)
			h.u.confirmTap("cloud_unlink")
		}},
		{"account", func(h *harness) { pageApp(h, "account", nil) }},
		{"account-nopw", func(h *harness) {
			pageApp(h, "account", func(a *AppSnapshot) { a.Setup.NeedsPassword = true })
		}},
		{"account-current", func(h *harness) {
			pageApp(h, "account", nil)
			h.u.pageTouch("s_acct_pw", "", false)
		}},
		{"account-new", func(h *harness) {
			pageApp(h, "account", nil)
			h.u.pageTouch("s_acct_pw", "", false)
			h.u.kbKey("x", 0)
			h.u.kbKey("OK", 0)
		}},
	})
}

// The rescue on the screen (MANAGER.md §6): the setup code behind its eye (asked of the app only when opened, never
// drawn with it closed, forgotten when it closes or the page goes) and a new web password without the current one.
func TestAccountRescue(t *testing.T) {
	// Drawn: never the code with the eye closed, even if the app still holds it
	d, _ := drawHarness(t)
	da := pageApp(d, "account", func(a *AppSnapshot) { a.SetupCode = "K7QM2XVD" })
	var drawn []string
	drawnText = func(s string) { drawn = append(drawn, s) }
	defer func() { drawnText = nil }()
	d.u.Dirty = true
	d.u.Tick()
	if j := strings.Join(drawn, "|"); strings.Contains(j, "K7QM") || !strings.Contains(j, Spanish.Account.SetupCode) {
		t.Fatalf("eye closed: %q", drawn)
	}
	d.u.pageTouch(codeEye, "", false)
	drawn = nil
	d.u.Dirty = true
	d.u.Tick()
	if !strings.Contains(strings.Join(drawn, "|"), "K7QM-2XVD") || len(da.did) != 0 {
		t.Fatalf("eye open: %q %+v", drawn, da.did)
	}

	h, app := pagesHarness(t, "account")
	h.press(codeEye, 0.1)
	if a := app.lastDid(); a.What != "setup_code" || !h.u.revealed(codeEye) {
		t.Fatalf("%+v", a)
	}
	app.snap.SetupCode = "K7QM2XVD"
	dropped := app.dropped
	h.press(codeEye, 0.1)
	if h.u.revealed(codeEye) || app.dropped != dropped+1 {
		t.Fatal("closing the eye does not forget the code")
	}
	h.press(codeEye, 0.1) // open again: the app still has it, nothing asked
	n := len(app.did)
	h.u.leavePage("account")
	if h.u.revealed(codeEye) || app.dropped != dropped+2 || len(app.did) != n {
		t.Fatal("leaving the page does not close the eye")
	}
	// Forgot the web password: the new one twice, sent as a reset (not the setup's, not PUT /password)
	h.press("s_acct_reset", 0.1)
	for range 2 {
		for _, c := range "rescatadaa" {
			h.tapKey(string(c), 0.1)
		}
		h.tapKey("OK", 0.1)
	}
	a := app.lastDid()
	sec := app.secrets[len(app.secrets)-1]
	if a.What != "password_reset" || sec["password"] != "rescatadaa" || len(app.pw) != 0 || h.u.acct.next != nil || h.u.acct.reset {
		t.Fatalf("%+v %+v %v", a, sec, app.pw)
	}
	app.snap.Last = &AppResult{What: "password_reset", OK: true, At: h.now().Add(time.Millisecond)}
	h.advance(10 * time.Millisecond)
	if note, warn := h.u.accountNote(&app.snap); warn || note != Spanish.Account.PasswordChanged {
		t.Fatalf("%q %v", note, warn)
	}
	// Then the web password's row is the usual one (current first), not a reset
	h.press("s_acct_pw", 0.1)
	if h.u.kb == nil || h.u.kb.spec.title != Spanish.Account.Current || h.u.acct.reset {
		t.Fatalf("%+v", h.u.kb)
	}
	h.tapKey("k_cancel", 0.1)
	// No password yet: neither the code nor the reset rows answer
	app.snap.Setup.NeedsPassword = true
	if keys := pageDefs["account"].keys(h.u, &app.snap); len(keys) != 2 {
		t.Fatalf("%v", keys)
	}
}
