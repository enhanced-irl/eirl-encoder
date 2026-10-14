package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/links"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/modem"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/uplinks"
)

// Local mode's internet connections (MODEMS.md): a cable, a Wi-Fi and three modems with different signals, through
// the app's real links service (health, hysteresis, /status, GET /modems, links.json), with fake files and a fake
// modem backend. links.json goes to the data directory, for the screen's renderer or a local engine.

type simModems struct{}

func (simModems) Name() string { return "sim" }

func (simModems) List(context.Context, []modem.Link) ([]modem.Modem, error) {
	f := func(v float64) *float64 { return &v }
	q := func(v int) *int { return &v }
	return []modem.Modem{
		{ID: "m-5a1d0c3e9b21", Backend: "mm", Iface: "wwan0", Manufacturer: "Quectel", Model: "RM520N-GL", State: "connected",
			SIM: "ready", Registration: "home", Operator: "Movistar", Tech: "5g-nsa", IMEITail: "4412", ICCIDTail: "0193",
			Signal: modem.Signal{RSRP: f(-88), RSRQ: f(-9), SINR: f(14), RSSI: f(-61), Quality: q(78)}},
		{ID: "m-0c7e44a19f02", Backend: "mm", Iface: "wwan1", Manufacturer: "Quectel", Model: "EG25", State: "connected",
			SIM: "ready", Registration: "home", Operator: "Vodafone", Tech: "4g", IMEITail: "5531", ICCIDTail: "4821",
			Signal: modem.Signal{RSRP: f(-106), RSRQ: f(-13), SINR: f(3.2), RSSI: f(-77), Quality: q(54)}},
		{ID: "h-9b2c41d07e5a", Backend: "hilink", Iface: "enx0c5b8f279a64", Manufacturer: "Huawei", Model: "HiLink",
			State: "connected", SIM: "ready", Registration: "home", Operator: "Orange", Tech: "4g",
			Signal: modem.Signal{RSRP: f(-117), RSRQ: f(-16), SINR: f(-1), RSSI: f(-89), Bars: q(1)}},
	}, nil
}

// simLinks writes the fake dispatcher, sysfs and disabled files under dir and returns the reader and the service.
func simLinks(dir string) (*uplinks.Reader, *links.Service, *modem.Manager, error) {
	sys := filepath.Join(dir, "sysnet")
	type iface struct{ name, ip, kind string }
	all := []iface{{"eth0", "192.168.1.20", "ethernet"}, {"wlan0", "172.20.10.4", "wifi"}, {"wwan0", "10.64.12.7", "usb"},
		{"wwan1", "10.70.3.9", "usb"}, {"enx0c5b8f279a64", "192.168.8.100", "usb"}}
	var avail strings.Builder
	for _, i := range all {
		st := filepath.Join(sys, i.name, "statistics")
		if err := os.MkdirAll(st, 0o755); err != nil {
			return nil, nil, nil, err
		}
		os.WriteFile(filepath.Join(st, "tx_bytes"), []byte("0\n"), 0o644)
		os.WriteFile(filepath.Join(st, "rx_bytes"), []byte("0\n"), 0o644)
		switch i.kind {
		case "wifi":
			os.MkdirAll(filepath.Join(sys, i.name, "wireless"), 0o755)
		case "usb":
			dev := filepath.Join(dir, "devices", "usb1", i.name)
			os.MkdirAll(dev, 0o755)
			os.Symlink(dev, filepath.Join(sys, i.name, "device"))
		}
		avail.WriteString(i.name + " " + i.ip + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "uplinks-available"), []byte(avail.String()), 0o644); err != nil {
		return nil, nil, nil, err
	}
	r := &uplinks.Reader{Available: filepath.Join(dir, "uplinks-available"), InUse: filepath.Join(dir, "uplinks-inuse"),
		Disabled: filepath.Join(dir, "uplinks-disabled"), NoInternet: filepath.Join(dir, "uplinks-nointernet"), SysNet: sys}
	mods := &modem.Manager{Backends: []modem.Backend{simModems{}}}
	sig := 48
	svc := &links.Service{Uplinks: r, Modems: mods, Path: filepath.Join(dir, "links.json"), Write: config.WriteAtomic,
		WiFi: func(context.Context) (string, *int) { return "wlan0", &sig }}
	return r, svc, mods, nil
}
