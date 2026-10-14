package api

import (
	"net/http"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/links"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/modem"
)

// --- modems (MODEMS.md §4) -----------------------------------------------------------------------------

// getModems: the cellular modems and how they are doing (the same as /status modems[]), without whole
// identifiers: only the last 4 digits of the IMEI and the ICCID. Read-only for now (configuration: phase M4).
func (s *Server) getModems(w http.ResponseWriter, r *http.Request) {
	mods := s.linksSnapshot().Modems
	if mods == nil {
		mods = []modem.Modem{}
	}
	writeJSON(w, 200, map[string]any{"modems": mods})
}

// linksSnapshot: the latest round of the links service (zero without it, or before its first round).
func (s *Server) linksSnapshot() links.Snapshot {
	if s.Links == nil {
		return links.Snapshot{}
	}
	return s.Links.Current()
}

// overSRTLA: the running stream goes over SRTLA right now, which is what makes the in-use file count. The one
// rule for /status (collect) and the links service (StreamingSRTLA). output: the mode in force (with the mixer,
// the engine's, which switches hot).
func overSRTLA(running bool, output string, rs runState) bool {
	return running && output == "on" && rs.Transport == "srtla"
}

// StreamingSRTLA: overSRTLA for the links service's 2-second round. "Running" is the engine's run state being
// there instead of asking systemd (a `systemctl show` every 2 s): the engine's RuntimeDirectory only exists while
// the unit runs. The output mode is read as collect reads it.
func (s *Server) StreamingSRTLA() bool {
	rs, running := s.runState()
	output := rs.Output
	if running && rs.Mixer == "1" && s.Mixer != nil {
		if m, err := s.Mixer.Read(); err == nil && m.Output != nil {
			output = m.Output.Mode
		}
	}
	return overSRTLA(running, output, rs)
}
