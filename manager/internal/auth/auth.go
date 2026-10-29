// Package auth: one administrator password (bcrypt in /etc/eirl-encoder/admin.hash), in-memory
// sessions and the login's attempt limits (MANAGER.md §6): per IP, and a global one that a client which
// signed in recently does not count against (so nobody on the LAN can lock the owner out by burning it).
// Restarting the app closes all sessions and forgets the known clients.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
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
	// KnownTTL: a client (IP, or /64) that signed in successfully this recently is "known": the global
	// limit does not apply to it (its own per-IP limit still does). As long as a session lasts.
	KnownTTL = SessionTTL
	maxKnown = 32 // known clients kept (the oldest goes first)
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
	known    map[string]time.Time // client key → its last successful sign-in (MarkKnown)
	bcryptMu sync.Mutex           // one bcrypt at a time: no bursts of attempts, no Nano CPU at full load
}

func New(hashPath string) *Auth {
	a := &Auth{HashPath: hashPath, sessions: map[string]time.Time{}, attempts: map[string][]time.Time{},
		known: map[string]time.Time{}}
	b, err := safefile.ReadOwned(hashPath, 4<<10) // not the app's (nor root's) file: as if there were none
	switch {
	case err == nil:
		a.hash = []byte(strings.TrimSpace(string(b)))
		tightenHash(hashPath)
	case !errors.Is(err, os.ErrNotExist):
		a.LoadErr = err
	}
	return a
}

// tightenHash: an admin.hash of an earlier version (0640, readable by the directory's group) becomes HashMode,
// if it is the app's own regular file (the sticky directory keeps anyone else from swapping it in between). A
// device updated over the air also gets it from install.sh; this covers one whose install did not run.
func tightenHash(path string) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() == HashMode {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return
	}
	os.Chmod(path, HashMode)
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

// HashMode: admin.hash is the app's alone. The engine is in the directory's group (to read engine.env) and
// has no business with the hash: 0600 keeps it from copying it for an offline guess.
const HashMode os.FileMode = 0o600

// WriteHash writes the hash with HashMode. Run as the app's user (`sudo -u eirl-encoder eirl-encoderd
// passwd`): a file root wrote would be root's 0600 and the app could not read it.
func WriteHash(path string, hash []byte) error {
	return config.WriteAtomic(path, append(hash, '\n'), HashMode)
}

// Login checks the password and opens a session. key groups the attempts (IP, or /64 for IPv6).
// The attempt is recorded BEFORE bcrypt: with parallel requests, every one counts.
func (a *Auth) Login(key, pw string, now time.Time) (string, error) {
	a.mu.Lock()
	if len(a.hash) == 0 {
		a.mu.Unlock()
		return "", ErrNoPassword
	}
	if a.recent(key, now) >= maxAttempts || (!a.knownLocked(key, now) && a.recent(totalKey, now) >= maxGlobal) {
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
	a.markKnownLocked(key, now)
	return a.newSession(now), nil
}

// MarkKnown records that key proved itself now (a sign-in, the setup code): for KnownTTL the global limits
// (the login's here, the setup code's in the API) do not apply to it.
func (a *Auth) MarkKnown(key string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.markKnownLocked(key, now)
}

func (a *Auth) markKnownLocked(key string, now time.Time) {
	if key == "" {
		return
	}
	a.known[key] = now
	for len(a.known) > maxKnown {
		oldest, at := "", now
		for k, t := range a.known {
			if oldest == "" || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(a.known, oldest)
	}
}

// Known: key signed in successfully (or used the setup code) within KnownTTL.
func (a *Auth) Known(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.knownLocked(key, now)
}

func (a *Auth) knownLocked(key string, now time.Time) bool {
	t, ok := a.known[key]
	if ok && now.Sub(t) >= KnownTTL {
		delete(a.known, key)
		return false
	}
	return ok && key != ""
}

// CheckPassword compares pw with the saved password (one bcrypt at a time), without a session or a limit:
// the caller limits the attempts (the API's re-authentication for unlinking and linking).
func (a *Auth) CheckPassword(pw string) error {
	a.mu.Lock()
	hash := a.hash
	a.mu.Unlock()
	if len(hash) == 0 {
		return ErrNoPassword
	}
	a.bcryptMu.Lock()
	err := bcrypt.CompareHashAndPassword(hash, []byte(pw))
	a.bcryptMu.Unlock()
	if err != nil {
		return ErrBadPassword
	}
	return nil
}

// OpenSession opens a session without a password (the caller has already proved itself: the setup code).
func (a *Auth) OpenSession(now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.newSession(now)
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
