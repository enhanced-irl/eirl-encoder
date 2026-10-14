package appapi

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// AppApiTest.test_call
func TestCall(t *testing.T) {
	a := newFakeApp(t)
	ctx := context.Background()
	code, d, err := Call(ctx, a.sock, "GET", "/network", nil, 0)
	if err != nil || code != 200 || obj(d)["enabled"] != true {
		t.Fatalf("GET: %d %v %v", code, d, err)
	}
	_, _, err = Call(ctx, a.sock, "PUT", "/network", []byte(`{"enabled":false}`), 0)
	var e *APIError
	if !errors.As(err, &e) || e.Status != 409 || e.Kind != "on_air" || e.Message != "hay una cámara de red al aire" {
		t.Fatalf("PUT: %#v", err)
	}
	if got := a.last(); !reflect.DeepEqual(got, seenReq{"PUT", "/api/v1/network", map[string]any{"enabled": false}}) {
		t.Errorf("seen %#v", got)
	}
	// No app: status 0, nothing of the request
	_, _, err = Call(ctx, filepath.Join(a.dir, "no"), "PUT", "/network", []byte(`{"secreto":"x"}`), 0)
	if !errors.As(err, &e) || e.Status != 0 || e.Kind != "unavailable" {
		t.Fatalf("no app: %#v", err)
	}
	if strings.Contains(err.Error()+e.Message, "secreto") {
		t.Errorf("the request leaks into the error: %q %q", err.Error(), e.Message)
	}
}

// AppApiTest.test_wifi_creds
func TestWifiCreds(t *testing.T) {
	a := newFakeApp(t)
	_, d, err := Call(context.Background(), a.sock, "GET", "/network/credentials?qr=rows", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w := wifiCreds(d); w == nil || w.SSID != "EnhancedIRL-A1B2" || len(w.QR) != 21 {
		t.Fatalf("%+v", w)
	}
	short := `["` + strings.Join(qrRows[:20], `","`) + `"]`
	full := `["` + strings.Join(qrRows, `","`) + `"]`
	for _, bad := range []string{`{"wifi": null}`,
		`{"wifi": {"ssid": "x<y", "password": "abcdEFGH1234", "qrRows": ` + full + `}}`,
		`{"wifi": {"ssid": "Red", "password": "corta", "qrRows": ` + full + `}}`,
		`{"wifi": {"ssid": "Red", "password": "abcdEFGH1234", "qrRows": ` + short + `}}`,
		`[]`, `"no"`} {
		if w := wifiCreds(js(t, bad)); w != nil {
			t.Errorf("%s → %+v", bad, w)
		}
	}
}
