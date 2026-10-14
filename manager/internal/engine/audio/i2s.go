package audio

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/sysx"
)

// MuxI2S routes the I2S4 (the TC358743's audio) to the capture ADMAIF, which the "HDMI de la placa"
// input needs. The launcher's step (eirl-engine.sh, mixer path):
//
//	[ -e /proc/asound/$ALSA_CARD ] && amixer -q -c $ALSA_CARD cset name="ADMAIF${ALSA_ADMAIF} Mux" I2S4 || true
//
// The engine calls it once at start, before the program and the inputs. Without the card it does
// nothing; a failure is logged and ignored (the board's HDMI input then stays without sound, as before).
// asound "" = /proc/asound. Reports whether amixer ran and succeeded.
func MuxI2S(run sysx.Runner, asound, card string, admaif int) bool {
	if asound == "" {
		asound = "/proc/asound"
	}
	if card == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(asound, card)); err != nil {
		return false
	}
	r := run(5*time.Second, "amixer", "-q", "-c", card, "cset", "name=ADMAIF"+strconv.Itoa(admaif)+" Mux", "I2S4")
	if r.RC != 0 {
		elog.Event("audio", "i2s_mux_failed", "card", card, "admaif", admaif, "rc", r.RC, "err", trim(r.Err, 160))
		return false
	}
	return true
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
