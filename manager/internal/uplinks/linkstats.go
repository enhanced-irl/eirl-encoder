package uplinks

import (
	"encoding/json"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LinkStat is one link of the engine's srtla-links.json (SRTLA.md §9.3), validated.
type LinkStat struct {
	IP        string
	Bond      string // active | stalled | weak | down
	RTTMs     int
	SendKbps  int
	NakPerSec float64
}

const (
	linkStatsFresh = 5 * time.Second // older: the session ended or the engine stopped writing
	linkStatsMax   = 16
	linkStatsSize  = 64 << 10
)

var bonds = map[string]bool{"active": true, "stalled": true, "weak": true, "down": true}

func (r *Reader) linkStatsPath() string {
	if r.LinkStats != "" || r.InUse == "" {
		return r.LinkStats
	}
	return filepath.Join(filepath.Dir(r.InUse), "srtla-links.json")
}

// ReadLinkStats reads the engine's statistics per link, by interface: nothing if the file is missing, older than
// linkStatsFresh at now, too big or not version 1. Written by another user (the engine), so it is read as data: a
// link with a bad name, address, state or number is left out.
func ReadLinkStats(path string, now time.Time) map[string]LinkStat {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > linkStatsSize || now.Sub(fi.ModTime()) > linkStatsFresh {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, linkStatsSize))
	if err != nil {
		return nil
	}
	var doc struct {
		V     int `json:"v"`
		Links []struct {
			Iface     string   `json:"iface"`
			IP        string   `json:"ip"`
			Bond      string   `json:"bond"`
			RTTMs     *float64 `json:"rttMs"`
			SendKbps  *float64 `json:"sendKbps"`
			NakPerSec *float64 `json:"nakPerSec"`
		} `json:"links"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.V != 1 {
		return nil
	}
	out := map[string]LinkStat{}
	for i, l := range doc.Links {
		if i == linkStatsMax {
			break
		}
		ip := net.ParseIP(l.IP)
		if !ValidName(l.Iface) || ip == nil || ip.To4() == nil || !bonds[l.Bond] ||
			!inRange(l.RTTMs, 60000) || !inRange(l.SendKbps, 1000000) || !inRange(l.NakPerSec, 100000) {
			continue
		}
		out[l.Iface] = LinkStat{IP: l.IP, Bond: l.Bond, RTTMs: int(math.Round(*l.RTTMs)),
			SendKbps: int(math.Round(*l.SendKbps)), NakPerSec: math.Round(*l.NakPerSec*10) / 10}
	}
	return out
}

func inRange(v *float64, hi float64) bool {
	return v != nil && *v >= 0 && *v <= hi && !math.IsNaN(*v)
}
