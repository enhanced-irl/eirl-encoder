package modem

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
)

// What the app changes on a HiLink modem (MODEMS.md §7), through its web API: the APN (a profile of its own,
// «EnhancedIRL»), data roaming, the network mode, the PIN, a reboot and its LAN subnet (two identical sticks hand out
// the same addresses). Written from the API as the open-source huawei-lte-api library uses it (E3372h-320, E8372 and
// others); fields differ between firmware versions: to be checked with real modems (M0).
//
// Every order asks for a fresh session and token (some firmware accepts each token once). The PIN goes encrypted with
// the modem's own RSA key (webserver/publickey) when it has one, as its web page does.

const profileName = "EnhancedIRL"

// ErrHiLink: the modem refused an order; the code is the modem's.
type ErrHiLink struct{ Code string }

func (e ErrHiLink) Error() string { return "hilink: refused (" + e.Code + ")" }

// hl: one modem's API, from the link's own IP.
type hl struct {
	h    *HiLink
	cl   *http.Client
	base string
}

func (h *HiLink) api(md Modem) (*hl, error) {
	if md.gateway == "" {
		return nil, ErrNotHere
	}
	base := "http://" + md.gateway
	if h.baseURL != nil {
		base = h.baseURL(md.gateway)
	}
	return &hl{h: h, cl: h.client(md.localIP), base: base}, nil
}

// session: a fresh cookie and token.
func (a *hl) session(ctx context.Context) (http.Header, error) {
	ses, err := a.h.get(ctx, a.cl, a.base+"/api/webserver/SesTokInfo", nil)
	if err != nil {
		return nil, err
	}
	hdr := http.Header{}
	if v := ses["SesInfo"]; v != "" {
		hdr.Set("Cookie", v)
	}
	if v := ses["TokInfo"]; v != "" {
		hdr.Set("__RequestVerificationToken", v)
	}
	return hdr, nil
}

// get: a reading, with a session.
func (a *hl) get(ctx context.Context, path string) (map[string]string, error) {
	hdr, err := a.session(ctx)
	if err != nil {
		return nil, err
	}
	return a.h.get(ctx, a.cl, a.base+path, hdr)
}

// getRaw: a reading whose answer is nested (the APN profiles), with a session.
func (a *hl) getRaw(ctx context.Context, path string) ([]byte, error) {
	hdr, err := a.session(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := a.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hilink: http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	// An <error> answer (an expired token…) is not an empty list: acting on it would duplicate or keep our profile
	if _, err := flatXML(bytes.NewReader(b)); err != nil {
		var code errHiLink
		if errors.As(err, &code) {
			return nil, ErrHiLink{Code: string(code)}
		}
		return nil, err
	}
	return b, nil
}

// field: one element of an order. Its value is escaped; a nested order uses inner instead.
type field struct {
	name, value string
	inner       []field
}

func requestXML(fields []field) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><request>`)
	var write func([]field)
	write = func(fs []field) {
		for _, f := range fs {
			b.WriteString("<" + f.name + ">")
			if f.inner != nil {
				write(f.inner)
			} else {
				xml.EscapeText(&b, []byte(f.value))
			}
			b.WriteString("</" + f.name + ">")
		}
	}
	write(fields)
	b.WriteString("</request>")
	return b.Bytes()
}

// post: one order. encrypt: with the modem's RSA key, if it has one (the PIN).
func (a *hl) post(ctx context.Context, path string, fields []field, encrypt bool) error {
	hdr, err := a.session(ctx)
	if err != nil {
		return err
	}
	body := requestXML(fields)
	ctype := "application/xml"
	if encrypt {
		if enc, ok := a.encrypt(ctx, body); ok {
			body, ctype = enc, "application/x-www-form-urlencoded; charset=UTF-8;enc"
			hdr.Set("encrypt_transmit", "encrypt_transmit")
		}
		if hdr2, err := a.session(ctx); err == nil { // the key readings may have used the token up
			hdr.Set("Cookie", hdr2.Get("Cookie"))
			hdr.Set("__RequestVerificationToken", hdr2.Get("__RequestVerificationToken"))
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := a.cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hilink: http %d", resp.StatusCode)
	}
	_, err = flatXML(io.LimitReader(resp.Body, 64<<10))
	var code errHiLink
	if errors.As(err, &code) {
		return ErrHiLink{Code: string(code)}
	}
	return err
}

// encrypt: body as the modem's web page sends secrets: base64, in chunks, each encrypted with its RSA key
// (PKCS#1 v1.5, or OAEP with SHA-1 when user/state-login says rsapadingtype 1), hex. false: the modem has no key
// (older firmware): sent plain.
func (a *hl) encrypt(ctx context.Context, body []byte) ([]byte, bool) {
	key, err := a.get(ctx, "/api/webserver/publickey")
	if err != nil {
		return nil, false
	}
	n, okN := new(big.Int).SetString(key["encpubkeyn"], 16)
	e, okE := new(big.Int).SetString(key["encpubkeye"], 16)
	if !okN || !okE || !e.IsInt64() || n.BitLen() < 1024 {
		return nil, false
	}
	pub := &rsa.PublicKey{N: n, E: int(e.Int64())}
	oaep := false
	if st, err := a.get(ctx, "/api/user/state-login"); err == nil && st["rsapadingtype"] == "1" {
		oaep = true
	}
	return rsaChunks(pub, body, oaep)
}

func rsaChunks(pub *rsa.PublicKey, body []byte, oaep bool) ([]byte, bool) {
	b64 := []byte(base64.StdEncoding.EncodeToString(body))
	size := pub.Size() - 11 // PKCS#1 v1.5
	if oaep {
		size = pub.Size() - 2*sha1.Size - 2
	}
	var out []byte
	for len(b64) > 0 {
		chunk := b64[:min(size, len(b64))]
		b64 = b64[len(chunk):]
		var enc []byte
		var err error
		if oaep {
			enc, err = rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, chunk, nil)
		} else {
			enc, err = rsa.EncryptPKCS1v15(rand.Reader, pub, chunk)
		}
		if err != nil {
			return nil, false
		}
		out = append(out, enc...)
	}
	h := hex.EncodeToString(out)
	if len(h)%2 == 1 {
		h = "0" + h
	}
	return []byte(h), true
}

// --- the orders -----------------------------------------------------------------------------------------------

// Apply: APN, roaming and mode as c says (each only if it differs from what the modem has).
func (h *HiLink) Apply(ctx context.Context, md Modem, c Config) error {
	a, err := h.api(md)
	if err != nil {
		return err
	}
	if err := a.setAPN(ctx, c.APN); err != nil {
		return fmt.Errorf("apn: %w", err)
	}
	if c.Roaming != md.Roaming {
		if err := a.setRoaming(ctx, c.Roaming); err != nil {
			return fmt.Errorf("roaming: %w", err)
		}
	}
	if c.Mode != "" && c.Mode != md.Mode { // "": nobody chose one, the stick's stays
		if err := a.setMode(ctx, c.Mode); err != nil {
			return fmt.Errorf("mode: %w", err)
		}
	}
	h.forget(md) // the next reading asks for the configuration again
	return nil
}

type hlProfiles struct {
	Current  string `xml:"CurrentProfile"`
	Profiles []struct {
		Index   string `xml:"Index"`
		Name    string `xml:"Name"`
		ApnName string `xml:"ApnName"`
	} `xml:"Profiles>Profile"`
}

// setAPN: "" = automatic (the modem's own profiles: ours goes); else our profile, created or changed, as default.
func (a *hl) setAPN(ctx context.Context, apn string) error {
	raw, err := a.getRaw(ctx, "/api/dialup/profiles")
	if err != nil {
		return err
	}
	var ps hlProfiles
	if err := xml.Unmarshal(raw, &ps); err != nil {
		return err
	}
	ours := ""
	for _, p := range ps.Profiles {
		if p.Name == profileName {
			if apn != "" && p.ApnName == apn && ps.Current == p.Index {
				return nil // already
			}
			ours = p.Index
		}
	}
	if apn == "" {
		if ours == "" {
			return nil
		}
		if err := a.post(ctx, "/api/dialup/profiles", []field{{"SetDefault", "0", nil}, {"Delete", ours, nil}, {"Modify", "0", nil}}, false); err != nil {
			return err
		}
		if ps.Current != ours {
			return nil
		}
		// Ours was the default: the stick's own first profile takes its place (as huawei-lte-api's set_default_profile)
		for _, p := range ps.Profiles {
			if p.Index != ours && p.Index != "" {
				return a.post(ctx, "/api/dialup/profiles", []field{{"SetDefault", p.Index, nil}, {"Delete", "0", nil}, {"Modify", "0", nil}}, false)
			}
		}
		return nil
	}
	modify, setDefault, index := "1", "1", ""
	if ours != "" {
		modify, setDefault, index = "2", ours, ours
	}
	return a.post(ctx, "/api/dialup/profiles", []field{{"SetDefault", setDefault, nil}, {"Delete", "0", nil}, {"Modify", modify, nil},
		{"Profile", "", []field{{"Index", index, nil}, {"IsValid", "1", nil}, {"Name", profileName, nil}, {"ApnIsStatic", "1", nil},
			{"ApnName", apn, nil}, {"DialupNum", "*99#", nil}, {"Username", "", nil}, {"Password", "", nil}, {"AuthMode", "0", nil},
			{"IpIsStatic", "", nil}, {"IpAddress", "", nil}, {"DnsIsStatic", "", nil}, {"PrimaryDns", "", nil},
			{"SecondaryDns", "", nil}, {"ReadOnly", "0", nil}, {"iptype", "0", nil}}}}, false) // iptype 0: IPv4 (SRTLA)
}

// setRoaming: dialup/connection, the rest of its fields as they are.
func (a *hl) setRoaming(ctx context.Context, on bool) error {
	cur, err := a.get(ctx, "/api/dialup/connection")
	if err != nil {
		return err
	}
	val := func(k, def string) string {
		if v, ok := cur[k]; ok && v != "" {
			return v
		}
		return def
	}
	r := "0"
	if on {
		r = "1"
	}
	return a.post(ctx, "/api/dialup/connection", []field{{"RoamAutoConnectEnable", r, nil}, {"MaxIdelTime", val("MaxIdelTime", "0"), nil},
		{"ConnectMode", val("ConnectMode", "0"), nil}, {"MTU", val("MTU", "1500"), nil}, {"auto_dial_switch", val("auto_dial_switch", "1"), nil},
		{"pdp_always_on", val("pdp_always_on", "0"), nil}}, false)
}

// hilinkModes: our modes in Huawei's codes (5G is not offered: no 5G HiLink is supported yet).
var hilinkModes = map[string]string{"auto": "00", "4g": "03"}

// setMode: net/net-mode, its bands as they are.
func (a *hl) setMode(ctx context.Context, mode string) error {
	code, ok := hilinkModes[mode]
	if !ok {
		return ErrConfig
	}
	cur, err := a.get(ctx, "/api/net/net-mode")
	if err != nil {
		return err
	}
	nb, lb := cur["NetworkBand"], cur["LTEBand"]
	if nb == "" {
		nb = "3FFFFFFF"
	}
	if lb == "" {
		lb = "7FFFFFFFFFFFFFFF"
	}
	return a.post(ctx, "/api/net/net-mode", []field{{"NetworkMode", code, nil}, {"NetworkBand", nb, nil}, {"LTEBand", lb, nil}}, false)
}

// Unlock: pin/operate, OperateType 0 (enter the PIN; the stick keeps it until it powers off) or 2 (turn the SIM's PIN
// off: the stick never asks again; what D-M6 needs on a HiLink, which has no profile to keep it in). A SIM asking for
// its PIN is unlocked first, then turned off.
func (h *HiLink) Unlock(ctx context.Context, md Modem, _ Config, pin string, remove bool) error {
	a, err := h.api(md)
	if err != nil {
		return err
	}
	if !remove && md.SIM == "ready" {
		return ErrNotLocked
	}
	ops := []string{"0"}
	switch {
	case remove && md.SIM == "pin":
		ops = []string{"0", "2"}
	case remove:
		ops = []string{"2"}
	}
	for i, op := range ops {
		err = a.post(ctx, "/api/pin/operate", []field{{"OperateType", op, nil}, {"CurrentPin", pin, nil}, {"NewPin", "", nil},
			{"PukCode", "", nil}}, true)
		if err == nil {
			continue
		}
		h.forget(md)
		var hlErr ErrHiLink
		switch {
		case i > 0: // unlocked, but the PIN is still on: not a wrong PIN
			return fmt.Errorf("unlocked, but the PIN was not turned off: %w", err)
		case errors.As(err, &hlErr) && hilinkPINRefused[hlErr.Code]:
			if st, e2 := a.get(ctx, "/api/pin/status"); e2 == nil && hilinkSIM(st["SimState"]) == "puk" {
				return ErrPUK
			}
			return ErrWrongPIN
		case errors.As(err, &hlErr) && hlErr.Code == "103004":
			return ErrPUK
		}
		return err // a session or busy error is not the user's PIN
	}
	h.forget(md)
	return nil
}

// hilinkPINRefused: pin/operate's answers to a PIN the SIM did not take (validate, and enable/disable).
var hilinkPINRefused = map[string]bool{"103002": true, "103003": true}

// Reset: device/control, Control 1 (reboot; never 2, which is a factory reset).
func (h *HiLink) Reset(ctx context.Context, md Modem) error {
	a, err := h.api(md)
	if err != nil {
		return err
	}
	err = a.post(ctx, "/api/device/control", []field{{"Control", "1", nil}}, false)
	if err != nil && isConnReset(err) {
		return nil // it went down while answering: that is the reboot
	}
	return err
}

// MoveSubnet: the modem's LAN to 192.168.<third>.0/24 (dhcp/settings, the rest as it is). Two identical HiLink sticks
// hand out the same addresses and the second one cannot be routed (MODEMS.md §7): moving the FIRST one, which is
// routed, separates them. The modem renews its LAN: the link goes for a few seconds and comes back with a new IP.
func (h *HiLink) MoveSubnet(ctx context.Context, md Modem, third int) error {
	if third < 1 || third > 254 {
		return ErrConfig
	}
	a, err := h.api(md)
	if err != nil {
		return err
	}
	cur, err := a.get(ctx, "/api/dhcp/settings")
	if err != nil {
		return err
	}
	old := net.ParseIP(cur["DhcpIPAddress"]).To4()
	if old == nil {
		return ErrNotHere
	}
	nw := fmt.Sprintf("192.168.%d.%d", third, old[3])
	swap := func(v string) string {
		ip := net.ParseIP(v).To4()
		if ip == nil || !ip.Mask(net.CIDRMask(24, 32)).Equal(old.Mask(net.CIDRMask(24, 32))) {
			return v
		}
		return fmt.Sprintf("192.168.%d.%d", third, ip[3])
	}
	val := func(k, def string) string {
		if v, ok := cur[k]; ok && v != "" {
			return v
		}
		return def
	}
	err = a.post(ctx, "/api/dhcp/settings", []field{{"DhcpIPAddress", nw, nil}, {"DhcpLanNetmask", val("DhcpLanNetmask", "255.255.255.0"), nil},
		{"DhcpStatus", val("DhcpStatus", "1"), nil}, {"DhcpStartIPAddress", swap(cur["DhcpStartIPAddress"]), nil},
		{"DhcpEndIPAddress", swap(cur["DhcpEndIPAddress"]), nil}, {"DhcpLeaseTime", val("DhcpLeaseTime", "86400"), nil},
		{"DnsStatus", val("DnsStatus", "1"), nil}, {"PrimaryDns", swap(cur["PrimaryDns"]), nil}, {"SecondaryDns", swap(cur["SecondaryDns"]), nil}}, false)
	if err != nil && isConnReset(err) {
		return nil
	}
	return err
}

func isConnReset(err error) bool {
	s := err.Error()
	return strings.Contains(s, "connection reset") || strings.Contains(s, "EOF") || strings.Contains(s, "broken pipe")
}

// hilinkMode: Huawei's NetworkMode in our words.
func hilinkMode(code string) string {
	if code == "03" {
		return "4g"
	}
	return "auto"
}
