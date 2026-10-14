package api

import (
	"regexp"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
)

// The access point and wired-camera errors come from eirl-netsetup (a root helper without languages) as stable codes,
// "code" or "code:detail" (subnet_in_use:wwan0). The API renders them in the request language (catalog netsetup.*);
// anything else (the free text of an older eirl-netsetup) is shown as is.

var reNetCode = regexp.MustCompile(`^([a-z_]{2,32})(?::([A-Za-z0-9_./:-]{1,120}))?$`)

func netError(lang, s string) string {
	m := reNetCode.FindStringSubmatch(s)
	if m == nil || !i18n.Has("netsetup."+m[1]) {
		return s
	}
	if m[2] != "" {
		return i18n.Msg(lang, "netsetup."+m[1], m[2])
	}
	return i18n.Msg(lang, "netsetup."+m[1])
}

// netStatus: GET /network and the like, with the errors in lang.
func netStatus(lang string, st network.Status) network.Status {
	st.AccessPoint.Error = netError(lang, st.AccessPoint.Error)
	st.LAN.Error = netError(lang, st.LAN.Error)
	return st
}

// wifiStatus: GET /network/wifi and the like, with the access point's error in lang.
func wifiStatus(lang string, st network.WiFiStatus) network.WiFiStatus {
	st.AP.Error = netError(lang, st.AP.Error)
	return st
}
