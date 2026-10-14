package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultManifestURL: the latest GitHub release of the encoder. GitHub answers with a redirect to the
// release's tag (github.com/…/releases/download/vX.Y.Z/manifest.json) and then to its storage host.
const DefaultManifestURL = "https://github.com/enhanced-irl/eirl-encoder/releases/latest/download/manifest.json"

// Fetcher downloads manifests and bundles. The signature is what makes a release trusted; the transport
// rules here (timeouts, size limits, no HTTPS→HTTP redirect) only keep a bad network from wasting time,
// memory or disk.
type Fetcher struct {
	HTTP      *http.Client
	UserAgent string
	// Stall: a download that receives nothing for this long is aborted (default 60 s).
	Stall time.Duration
}

// NewHTTPClient: dial/TLS/header timeouts; no overall timeout (a bundle over a slow link takes minutes:
// the caller's context and the stall watchdog bound it). At most 5 redirects, never from HTTPS to HTTP.
func NewHTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
	}
	return &http.Client{Transport: tr, CheckRedirect: checkRedirect}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("redirect from https to " + req.URL.Scheme)
	}
	return nil
}

func (f *Fetcher) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return NewHTTPClient()
}

func (f *Fetcher) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return nil, redactErr(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &statusError{status: resp.StatusCode, msg: fmt.Sprintf("GET %s: HTTP %d", redact(u), resp.StatusCode)}
	}
	return resp, nil
}

// statusError: the server answered, with something other than 200.
type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

// ErrNoRelease: the release channel answers, but has no manifest (nothing published yet). Not a failure: the
// encoder is as up to date as it can be. Only the manifest's own 404 counts; a release without its signature
// is a broken release.
var ErrNoRelease = errors.New("no release published yet")

// Fetched is a manifest as downloaded: its exact bytes (what the signature covers), the signature and
// the base URL the bundle is fetched from.
type Fetched struct {
	Raw, Sig []byte
	Base     *url.URL
}

// FetchManifest downloads manifest.json and manifest.json.sig. Both, and later the bundle, come from the
// same directory: that of the last redirect that stayed on the manifest URL's host. With GitHub's
// releases/latest/… that is the release's own tag, so a release published in between does not mix files.
func (f *Fetcher) FetchManifest(ctx context.Context, manifestURL string) (*Fetched, error) {
	u, err := url.Parse(manifestURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errf(CodeNetwork, "bad manifest URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := f.get(ctx, manifestURL)
	if se := (*statusError)(nil); errors.As(err, &se) && se.status == http.StatusNotFound {
		return nil, ErrNoRelease
	}
	if err != nil {
		return nil, &Error{Code: CodeNetwork, Err: err}
	}
	raw, err := readLimited(resp.Body, MaxManifestSize)
	resp.Body.Close()
	if err != nil {
		return nil, &Error{Code: CodeNetwork, Err: fmt.Errorf("manifest: %w", err)}
	}
	base := baseOf(u, resp)
	sigURL := base.ResolveReference(&url.URL{Path: "manifest.json.sig"})
	resp, err = f.get(ctx, sigURL.String())
	if err != nil {
		return nil, &Error{Code: CodeNetwork, Err: err}
	}
	sig, err := readLimited(resp.Body, MaxSigSize)
	resp.Body.Close()
	if err != nil {
		return nil, &Error{Code: CodeNetwork, Err: fmt.Errorf("signature: %w", err)}
	}
	return &Fetched{Raw: raw, Sig: sig, Base: base}, nil
}

// baseOf: the directory of the last URL in the redirect chain that is on the original host.
func baseOf(orig *url.URL, resp *http.Response) *url.URL {
	chosen := orig
	var chain []*url.URL
	for r := resp.Request; r != nil; {
		chain = append([]*url.URL{r.URL}, chain...)
		if r.Response == nil {
			break
		}
		r = r.Response.Request
	}
	for _, c := range chain {
		if c.Host == orig.Host && c.Scheme == orig.Scheme {
			chosen = c
		}
	}
	b := *chosen
	b.RawQuery, b.Fragment = "", ""
	b.Path = path.Dir(b.Path) + "/"
	b.RawPath = ""
	return &b
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %d bytes", max)
	}
	return b, nil
}

// BundleURL: where the bundle of m is, next to the manifest.
func (fd *Fetched) BundleURL(m *Manifest) string {
	return fd.Base.ResolveReference(&url.URL{Path: m.Bundle.Name}).String()
}

// Download fetches u into dst, checking the exact size and sha256 while writing. It writes dst+".part"
// and renames on success only; on any error nothing is left at dst. progress (optional) gets the bytes
// received so far.
func (f *Fetcher) Download(ctx context.Context, u, dst string, size int64, sum string, progress func(int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resp, err := f.get(ctx, u)
	if err != nil {
		return &Error{Code: CodeNetwork, Err: err}
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 && resp.ContentLength != size {
		return errf(CodeChecksum, "server announces %d bytes, manifest says %d", resp.ContentLength, size)
	}
	part := dst + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(part)
		}
	}()
	stall := f.Stall
	if stall <= 0 {
		stall = 60 * time.Second
	}
	var stalled atomic.Bool
	timer := time.AfterFunc(stall, func() { stalled.Store(true); cancel() })
	defer timer.Stop()
	h := sha256.New()
	var n int64
	buf := make([]byte, 64<<10)
	body := io.LimitReader(resp.Body, size+1)
	for {
		k, rerr := body.Read(buf)
		if k > 0 {
			timer.Reset(stall)
			n += int64(k)
			if n > size {
				return errf(CodeChecksum, "bundle larger than %d bytes", size)
			}
			h.Write(buf[:k])
			if _, err := out.Write(buf[:k]); err != nil {
				return err
			}
			if progress != nil {
				progress(n)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if stalled.Load() {
				return errf(CodeNetwork, "no data for %s", stall)
			}
			if ctx.Err() != nil {
				return ctx.Err() // cancelled by the caller (e.g. streaming started)
			}
			return &Error{Code: CodeNetwork, Err: redactErr(rerr)}
		}
	}
	if n != size {
		return errf(CodeChecksum, "bundle has %d bytes, manifest says %d", n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return errf(CodeChecksum, "bundle sha256 %s, manifest says %s", got, sum)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	ok = true
	syncDir(filepath.Dir(dst))
	return nil
}

// FileSHA256 hashes a file, refusing it if it is not exactly size bytes.
func FileSHA256(p string, size int64) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, size+1))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", errf(CodeChecksum, "%s has %d bytes, want %d", filepath.Base(p), n, size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// redactErr drops the query of the URL a *url.Error carries (the client's errors name the URL of the last
// redirect, a signed storage URL with its token in the query).
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = redact(ue.URL)
	}
	return err
}

// redact drops the query of a URL for logs (signed storage URLs carry tokens there).
func redact(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}
