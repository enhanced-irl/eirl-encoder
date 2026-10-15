package rtmpsend

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Target is where to publish, split as the dial and the RTMP connect need it.
type Target struct {
	TLS  bool
	Host string // without the brackets of an IPv6
	Port int

	// url is what the RTMP library takes: scheme://host[:port]/app with the publish name (the key) as
	// fragment. It holds the key: never print it.
	url *url.URL
}

// Addr is host:port for the dial.
func (t Target) Addr() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// ParseTarget reads `rtmp[s]://host[:port]/app` (default ports 1935 and 443) and the stream key, which
// becomes the publish name as RTMP clients do: the URL is the application, the key the stream. A trailing
// `/` of the application is dropped (`…/rtmp/` connects to the application `rtmp`, as librtmp does). With
// an empty key the last element of the path is the publish name (`rtmp://host/app/stream`). Its errors
// never contain the key.
func ParseTarget(raw, key string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, errors.New("invalid url")
	}
	var t Target
	switch u.Scheme {
	case "rtmp":
		t.Port = 1935
	case "rtmps":
		t.TLS, t.Port = true, 443
	default:
		return Target{}, errors.New("the url must be rtmp:// or rtmps://")
	}
	if u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return Target{}, errors.New("the url must be rtmp[s]://host[:port]/app, without credentials or fragment")
	}
	t.Host = u.Hostname()
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Target{}, errors.New("invalid port")
		}
		t.Port = n
	}
	path := strings.TrimRight(u.Path, "/")
	if key == "" {
		i := strings.LastIndex(path, "/")
		path, key = path[:max(i, 0)], path[i+1:]
	}
	if strings.Trim(path, "/") == "" {
		return Target{}, errors.New("the url has no application (or, with no key, no stream name)")
	}
	t.url = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: path, RawQuery: u.RawQuery, Fragment: key}
	return t, nil
}
