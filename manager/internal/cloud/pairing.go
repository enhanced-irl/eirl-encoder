package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var (
	// ErrRateLimited: the backend answers 429 (10/min per IP, 5 pending pairings per origin).
	ErrRateLimited = errors.New("too many pairing attempts: wait a minute")
	// ErrUnreachable: no answer from the backend (no internet, DNS, TLS, clock not synced…).
	ErrUnreachable = errors.New("cannot reach EnhancedIRL: does the device have internet access?")
	// ErrAlreadyLinked: there is already a link; to make another, unlink first.
	ErrAlreadyLinked = errors.New("the device is already linked")
	ErrNoHardwareID  = errors.New("the device has no identifier (/etc/machine-id)")
	ErrNoIdentity    = errors.New("cannot read or create the device identity")
)

// Denials (§3.1). Any other reason the backend sends is reported as "denied".
var denyReasons = map[string]bool{
	"limit_reached": true, "email_not_verified": true, "subscription_required": true,
	"feature_disabled": true, "hardware_claimed": true, "denied": true,
}

var (
	rePairingID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	reToken     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,512}$`)
	reEncoderID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type pairingResp struct {
	PairingID  string `json:"pairingId"`
	Code       string `json:"code"`
	PollSecret string `json:"pollSecret"`
	ExpiresIn  int    `json:"expiresIn"`
	Interval   int    `json:"interval"`
}

// requestPairing: POST /encoders/pairing (201). identity is secret: it goes in the body and nowhere
// else.
func (c *Client) requestPairing(ctx context.Context, info DeviceInfo, identity string) (pairingResp, error) {
	var out pairingResp
	if !reHardwareID.MatchString(info.HardwareID) {
		return out, ErrNoHardwareID
	}
	if !reIdentity.MatchString(identity) {
		return out, ErrNoIdentity
	}
	body := map[string]string{
		"hardwareId":      info.HardwareID,
		"identity":        identity,
		"model":           cleanText(info.Model, 64),
		"firmwareVersion": cleanText(info.FirmwareVersion, 64),
	}
	if n := cleanText(info.Name, 64); n != "" {
		body["name"] = n
	}
	code, err := c.postJSON(ctx, joinBase(c.APIBase, "/encoders/pairing"), "", body, &out)
	switch {
	case err != nil:
		return out, err
	case code == http.StatusTooManyRequests:
		return out, ErrRateLimited
	case code != http.StatusCreated && code != http.StatusOK:
		return out, fmt.Errorf("the backend answered %d when requesting the code", code)
	}
	out.Code = FormatCode(out.Code)
	if out.Code == "" || !rePairingID.MatchString(out.PairingID) || out.PollSecret == "" || len(out.PollSecret) > 512 {
		return out, errors.New("invalid pairing response")
	}
	if out.ExpiresIn <= 0 || out.ExpiresIn > 3600 {
		out.ExpiresIn = 600
	}
	if out.Interval < 1 || out.Interval > 60 {
		out.Interval = 3
	}
	return out, nil
}

type pollResult struct {
	Status       string `json:"status"` // pending | approved | denied | gone
	EncoderID    string `json:"encoderId"`
	Token        string `json:"token"`
	AccountLabel string `json:"accountLabel"`
	Reason       string `json:"reason"`
}

// pollToken: POST /encoders/pairing/:pairingId/token. 202 pending · 200 approved (only once) ·
// 403 denied · 410 expired or already collected · 429 slow down.
func (c *Client) pollToken(ctx context.Context, pairingID, pollSecret string) (pollResult, error) {
	var r pollResult
	u := joinBase(c.APIBase, "/encoders/pairing/"+url.PathEscape(pairingID)+"/token")
	code, err := c.postJSON(ctx, u, "", map[string]string{"pollSecret": pollSecret}, &r)
	if err != nil {
		return r, err
	}
	switch code {
	case http.StatusAccepted:
		r.Status = "pending"
	case http.StatusOK:
		if r.Status != "approved" || !reToken.MatchString(r.Token) || !reEncoderID.MatchString(r.EncoderID) {
			return r, errors.New("invalid approval response")
		}
		r.AccountLabel = cleanText(r.AccountLabel, 128)
	case http.StatusForbidden:
		r.Status = "denied"
		if !denyReasons[r.Reason] {
			r.Reason = "denied"
		}
	case http.StatusGone, http.StatusNotFound:
		r.Status = "gone"
	case http.StatusTooManyRequests:
		return r, ErrRateLimited
	default:
		return r, fmt.Errorf("the backend answered %d when polling the pairing", code)
	}
	return r, nil
}

// deleteSelf: DELETE /encoders/self (revokes the token in the cloud). 204 or 401 (no longer valid) = done.
func (c *Client) deleteSelf(ctx context.Context, st State) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, joinBase(st.APIBase, "/encoders/self"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+st.Token)
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return ErrUnreachable
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusUnauthorized:
		return nil
	}
	return fmt.Errorf("the backend answered %d when unlinking", resp.StatusCode)
}

// postJSON sends JSON and decodes the response (≤ 64 KB) into out if it is JSON. Returns the HTTP
// status; err only if there was no response.
func (c *Client) postJSON(ctx context.Context, u, bearer string, body, out any) (int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, ErrUnreachable
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		json.Unmarshal(data, out) // a non-JSON body leaves out empty: the caller validates it
	}
	return resp.StatusCode, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

var defaultHTTP = &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirects}

// noRedirects: a redirect is treated as a response (a backend error), not followed. A 307 would
// resend the body (identity, pollSecret) to another host, and a downgrade to http would also leak the
// Authorization header.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return "eirl-encoderd"
}
