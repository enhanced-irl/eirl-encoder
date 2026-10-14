// Package auth: one administrator password (bcrypt in /etc/eirl-encoder/admin.hash), in-memory
// sessions and a per-IP attempt limit. Restarting the app closes all sessions.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

const (
	MinPasswordLen = 10
	MaxPasswordLen = 72                  // bcrypt's limit
	SessionTTL     = 30 * 24 * time.Hour // sliding: renewed on every use
	bcryptCost     = 10                  // ~0.1 s once, on one core of the Nano
	maxAttempts    = 5                   // per IP (IPv6: per /64) and minute
	maxGlobal      = 20                  // across all of them, per minute
	attemptWindow  = time.Minute
	totalKey       = "*" // global counter in the same map (not an IP)
)

// The texts are for logs and the passwd command; the API shows its own translated messages (api.passwordError).
var (
	ErrNoPassword  = errors.New("no administrator password")
	ErrBadPassword = errors.New("wrong password")
	ErrTooShort    = errors.New("the password must be at least 10 characters long")
	// ErrTooLong: bcrypt only looks at the first 72 bytes (and x/crypto rejects longer ones)
	ErrTooLong     = errors.New("the password cannot be longer than 72 bytes")
	ErrRateLimited = errors.New("too many attempts; wait a minute")
)

type Auth struct {
	HashPath string
	// LoadErr: the file exists but could not be read (e.g. permissions). Not the same as "none".
	LoadErr error

	mu       sync.Mutex
	hash     []byte
	sessions map[string]time.Time // token → expiry
	attempts map[string][]time.Time
	bcryptMu sync.Mutex // one bcrypt at a time: no bursts of attempts, no Nano CPU at full load
}

func New(hashPath string) *Auth {
	a := &Auth{HashPath: hashPath, sessions: map[string]time.Time{}, attempts: map[string][]time.Time{}}
	b, err := safefile.ReadOwned(hashPath, 4<<10) // not the app's (nor root's) file: as if there were none
	switch {
	case err == nil:
		a.hash = []byte(strings.TrimSpace(string(b)))
	case !errors.Is(err, os.ErrNotExist):
		a.LoadErr = err
	}
	return a
}

func (a *Auth) Configured() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.hash) > 0
}

// HashPassword is used by the `passwd` subcommand and by password changes through the API.
func HashPassword(pw string) ([]byte, error) {
	if len(pw) < MinPasswordLen {
		return nil, ErrTooShort
	}
	if len(pw) > MaxPasswordLen {
		return nil, ErrTooLong
	}
	return bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
}

// WriteHash: 0640 so the app can read it through the group (eirl-encoder, from the directory's
// setgid) even when `passwd` was run by root.
func WriteHash(path string, hash []byte) error {
	return config.WriteAtomic(path, append(hash, '\n'), 0o640)
}

// Login checks the password and opens a session. key groups the attempts (IP, or /64 for IPv6).
// The attempt is recorded BEFORE bcrypt: with parallel requests, every one counts.
func (a *Auth) Login(key, pw string, now time.Time) (string, error) {
	a.mu.Lock()
	if len(a.hash) == 0 {
		a.mu.Unlock()
		return "", ErrNoPassword
	}
	if a.recent(key, now) >= maxAttempts || a.recent(totalKey, now) >= maxGlobal {
		a.mu.Unlock()
		return "", ErrRateLimited
	}
	a.attempts[key] = append(a.attempts[key], now)
	a.attempts[totalKey] = append(a.attempts[totalKey], now)
	hash := a.hash
	a.mu.Unlock()

	a.bcryptMu.Lock()
	err := bcrypt.CompareHashAndPassword(hash, []byte(pw))
	a.bcryptMu.Unlock()
	if err != nil {
		return "", ErrBadPassword
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, key) // a success clears that IP's attempts (not the total)
	return a.newSession(now), nil
}

func (a *Auth) recent(key string, now time.Time) int {
	kept := a.attempts[key][:0]
	for _, t := range a.attempts[key] {
		if now.Sub(t) < attemptWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(a.attempts, key)
	} else {
		a.attempts[key] = kept
	}
	return len(kept)
}

func (a *Auth) newSession(now time.Time) string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	a.sessions[tok] = now.Add(SessionTTL)
	return tok
}

// Valid checks and renews the session.
func (a *Auth) Valid(tok string, now time.Time) bool {
	if tok == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for t, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, t)
		}
	}
	for t := range a.sessions {
		if subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			a.sessions[t] = now.Add(SessionTTL)
			return true
		}
	}
	return false
}

func (a *Auth) Logout(tok string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, tok)
}

// SetPassword sets a new password without asking for the current one (the cloud's
// set_local_password command, which has already authenticated the owner) and closes ALL local
// sessions.
func (a *Auth) SetPassword(pw string) error {
	a.bcryptMu.Lock() // one bcrypt at a time, here too
	h, err := HashPassword(pw)
	a.bcryptMu.Unlock()
	if err != nil {
		return err
	}
	if err := WriteHash(a.HashPath, h); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hash = h
	a.LoadErr = nil
	clear(a.sessions)
	return nil
}

// ChangePassword requires the current password, saves the new one and closes the other sessions.
func (a *Auth) ChangePassword(keep, current, next string) error {
	a.mu.Lock()
	hash := a.hash
	a.mu.Unlock()
	if bcrypt.CompareHashAndPassword(hash, []byte(current)) != nil {
		return ErrBadPassword
	}
	h, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := WriteHash(a.HashPath, h); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hash = h
	for t := range a.sessions {
		if t != keep {
			delete(a.sessions, t)
		}
	}
	return nil
}
