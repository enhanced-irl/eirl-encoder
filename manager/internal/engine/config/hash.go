package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NoConfSHA is CONF_SHA when there is no app configuration file (EIRL_CONF unset or missing).
const NoConfSHA = "none"

// ConfHash is the CONF_SHA of the app configuration file content, as eirl-engine.sh computes it
// with the mixer (scripts/eirl-engine.sh:47-55): the content without its trailing newlines, minus
// the lines that start with "OUTPUT=" (start/stop emitting is applied live, so it is not a pending
// change), plus one "\n", hashed with SHA-256. It is byte-for-byte config.Store.Hashes().NoOutput
// of the app, which compares it with CONF_SHA in /run/eirl-engine/state.
func ConfHash(content []byte) string {
	trimmed := strings.TrimRight(string(content), "\n")
	var kept []string
	for _, l := range strings.Split(trimmed, "\n") {
		if !strings.HasPrefix(l, "OUTPUT=") {
			kept = append(kept, l)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(kept, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}
