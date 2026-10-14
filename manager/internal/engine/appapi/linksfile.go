package appapi

import (
	"io"
	"os"
	"sync"
	"time"
)

// LinksFile: what the app writes about every internet connection, for the header's dots (MODEMS.md §4.2).
const LinksFile = "/run/eirl-encoder/links.json"

// linksStale: a links.json older than this is not trusted: the app is not running (it rewrites it at least
// every 5 s).
const linksStale = 15 * time.Second

// MaxLinks: the header has room for this many dots at most.
const MaxLinks = 8

// Link: one connection, validated (what the app wrote is not trusted: the engine runs as another user).
type Link struct {
	Name, Kind, Label, Tech string
	Health, Code            string // good | weak | bad | off, and why (MODEMS.md §5)
	Available, InUse        bool
	Disabled                bool
}

var (
	linkKinds  = set("ethernet", "usb", "wifi", "modem")
	linkTechs  = set("2g", "3g", "4g", "5g-nsa", "5g")
	linksCache struct {
		mu    sync.Mutex
		has   bool
		path  string
		mtime int64
		size  int64
		at    time.Time
		links []Link
	}
)

// Links are the connections for the header, and whether they can be trusted now (the file is there, fits and is
// fresh). path "" is LinksFile. It is read again only when it changes (path, mtime, size): the screen asks at each
// drawing. The result is shared and read-only.
func Links(path string, now time.Time) ([]Link, bool) {
	if path == "" {
		path = LinksFile
	}
	c := &linksCache
	c.mu.Lock()
	defer c.mu.Unlock()
	fi, err := os.Stat(path)
	if err != nil {
		c.has, c.links = false, nil
		return nil, false
	}
	mtime, size := fi.ModTime().UnixNano(), fi.Size()
	if !c.has || c.path != path || c.mtime != mtime || c.size != size {
		c.has, c.path, c.mtime, c.size = true, path, mtime, size
		c.at, c.links = readLinksFile(path)
	}
	if c.at.IsZero() || now.Sub(c.at) > linksStale || c.at.Sub(now) > linksStale {
		return nil, false
	}
	return c.links, true
}

func readLinksFile(path string) (time.Time, []Link) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil {
		return time.Time{}, nil
	}
	raw, ok := parseJSON(b)
	m := obj(raw)
	if !ok {
		return time.Time{}, nil
	}
	if m == nil {
		return time.Time{}, nil
	}
	if v, ok := intIn(m["v"], 1, 1); !ok || v != 1 { // another format: not understood
		return time.Time{}, nil
	}
	ts, _ := m["at"].(string)
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, nil
	}
	list, _ := m["links"].([]any)
	out := []Link{}
	for _, x := range list {
		lm := obj(x)
		name, _ := lm["name"].(string)
		if lm == nil || !reUplink.MatchString(name) || len(out) >= MaxLinks {
			continue
		}
		l := Link{Name: name, Kind: oneOf(lm, "kind", linkKinds), Label: textField(lm, "label", 32),
			Tech: oneOf(lm, "tech", linkTechs), Health: oneOf(lm, "health", healthLevels)}
		if l.Kind == "" || l.Health == "" {
			continue
		}
		l.Code = oneOf(lm, "code", healthCodes)
		l.Available, _ = boolOf(lm["available"])
		l.InUse, _ = boolOf(lm["inUse"])
		l.Disabled, _ = boolOf(lm["disabled"])
		out = append(out, l)
	}
	return at, out
}
