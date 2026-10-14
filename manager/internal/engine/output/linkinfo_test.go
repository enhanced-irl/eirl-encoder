package output

import (
	"path/filepath"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/config"
)

// LinkInfo: SRT does not count uplinks; SRTLA counts those it would use now (not the disabled ones, nor the wifi
// without internet), the same rule as the header's text had.
func TestLinkInfo(t *testing.T) {
	d := t.TempDir()
	a, dis, nonet := filepath.Join(d, "available"), filepath.Join(d, "disabled"), filepath.Join(d, "nointernet")
	write(t, a, "eth0 192.168.1.20\nwlan0 172.20.10.2\nusb0 10.0.0.2\n")
	write(t, dis, "usb0\n")
	write(t, nonet, "")
	o := &Output{cfg: &config.Config{Transport: "srt", UplinksFile: a, UplinksDisabled: dis, UplinksNoInternet: nonet},
		runDir: d}
	if tr, n := o.LinkInfo(); tr != "srt" || n != 0 {
		t.Errorf("srt: %s %d", tr, n)
	}
	o.cfg.Transport = "srtla"
	if tr, n := o.LinkInfo(); tr != "srtla" || n != 2 {
		t.Errorf("srtla: %s %d", tr, n)
	}
	write(t, nonet, "wlan0\n")
	if tr, n := o.LinkInfo(); tr != "srtla" || n != 1 {
		t.Errorf("wifi without internet: %s %d", tr, n)
	}
}
