package api

import "testing"

// eirl-netsetup's codes in the request language; the free text of an older helper as is
func TestNetError(t *testing.T) {
	for in, want := range map[string]string{
		"no_wifi":                                "pas de Wi-Fi (wlan0)",
		"subnet_in_use:wwan0":                    "le réseau 10.42.0.0/24 est déjà utilisé par wwan0",
		"cannot_create:/run/eirl-ap":             "impossible de créer /run/eirl-ap",
		"no se pudo levantar el punto de acceso": "no se pudo levantar el punto de acceso",
		"unknown_code":                           "unknown_code",
		"":                                       "",
		"no_wifi:<script>":                       "no_wifi:<script>",
	} {
		if got := netError("fr", in); got != want {
			t.Errorf("netError(%q) = %q, want %q", in, got, want)
		}
	}
}
