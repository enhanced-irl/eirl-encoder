package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/network"
)

// --- the board's access point, simulated (local mode) ------------------------------------------------------------
//
// The real network.Manager over the simulator's data directory, with a fake eirl-netsetup underneath: «Create my
// Wi-Fi», its name, channel and password, and the network cameras' switches work in the web and over the screen's
// socket (WIFI.md, NETWORK_SOURCES.md §6). No MediaMTX (no camera ever connects), no Wi-Fi client (no wlan0) and no
// cable IP.

// simNetwork: the network manager of the simulator. hwID gives the default name (EnhancedIRL-<its last 4>).
func simNetwork(dir, hwID string) *network.Manager {
	statePath := filepath.Join(dir, "ap-state.json")
	return &network.Manager{Dir: dir, StatePath: statePath, HardwareID: hwID,
		Apply:   func(ctx context.Context) error { return simNetsetup(ctx, dir, statePath) },
		Write:   func(p string, b []byte, mode os.FileMode) error { return config.WriteAtomic(p, b, mode) },
		Clients: func(context.Context) (int, bool) { return 1, true },
		LANAddr: func() string { return "" },
	}
}

// simHardwareID: a stable hardwareId for a data directory (the -hardware-id flag wins).
func simHardwareID(dataDir string) string {
	abs, _ := filepath.Abs(dataDir)
	sum := sha256.Sum256([]byte("eirl-encoder-sim:" + abs))
	return hex.EncodeToString(sum[:8])
}

// simNetsetup: what `eirl-netsetup apply` leaves in its state, from the same files and rules (netcfg), after a pause
// like bringing ap0 up. The AP comes up with «Create my Wi-Fi» (or the W4 rule of older files) on its channel, 6 with
// «auto».
func simNetsetup(ctx context.Context, dir, statePath string) error {
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	read := func(name string) []byte {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return b
	}
	cams, _ := netcfg.ParseCameras(read(netcfg.FileCameras))
	st := map[string]any{"state": "off", "lan": "off", "at": time.Now().UTC().Format(time.RFC3339Nano)}
	if n, ok, err := netcfg.ParseNetwork(read(netcfg.FileNetwork)); err == nil && ok {
		policy := n.APPolicy(len(cams.Cameras) > 0)
		st["ap"] = policy
		if policy == netcfg.APAlways {
			ch, err := strconv.Atoi(n.AP.Channel)
			if err != nil {
				ch = 6
			}
			st["state"], st["mode"], st["ssid"], st["channel"] = "on", "normal", n.AP.SSID, ch
		}
	}
	b, _ := json.Marshal(st)
	return config.WriteAtomic(statePath, append(b, '\n'), 0o644)
}
