package appapi

import (
	"io"
	"os"
	"sync"
)

// WiFiFile is the network for the screen when the app has no local API (an older app): written by the app, 0640,
// group eirl-encoder (NETWORK_SOURCES.md §6.4, «Reserva»).
const WiFiFile = "/run/eirl-encoder/wifi.json"

// FileWiFi is the wifi of wifi.json: the wifi to join and whether it is on.
type FileWiFi struct {
	WiFi
	On bool // false only with "on": false (an older app without it: on)
}

var wifiCache struct {
	mu    sync.Mutex
	has   bool
	path  string
	mtime int64 // ns
	size  int64
	info  *FileWiFi
}

// WiFiInfo is the cameras' wifi for the screen (engine_net.wifi_info): nil without cameras or with a file that does
// not fit. path "" is WiFiFile. It is read again only when it changes (path, mtime, size): the screen asks every
// second. The result is shared and read-only.
func WiFiInfo(path string) *FileWiFi {
	if path == "" {
		path = WiFiFile
	}
	c := &wifiCache
	c.mu.Lock()
	defer c.mu.Unlock()
	fi, err := os.Stat(path)
	if err != nil {
		c.has, c.info = false, nil
		return nil
	}
	mtime, size := fi.ModTime().UnixNano(), fi.Size()
	if c.has && c.path == path && c.mtime == mtime && c.size == size {
		return c.info
	}
	c.has, c.path, c.mtime, c.size, c.info = true, path, mtime, size, readWiFiFile(path)
	return c.info
}

func readWiFiFile(path string) *FileWiFi {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(maxQRFileRead)))
	if err != nil || len(raw) == 0 {
		return nil
	}
	v, ok := parseJSON(raw)
	d := obj(v)
	if !ok || d == nil {
		return nil
	}
	w, ok := validWiFi(d["ssid"], d["password"], d["qr"])
	if !ok {
		return nil
	}
	on, isBool := d["on"].(bool)
	return &FileWiFi{WiFi: w, On: !isBool || on}
}
