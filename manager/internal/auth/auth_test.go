package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) *Auth {
	p := filepath.Join(t.TempDir(), "admin.hash")
	h, err := HashPassword("correct-123")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteHash(p, h); err != nil {
		t.Fatal(err)
	}
	return New(p)
}

func TestLoginRateLimitAndSessions(t *testing.T) {
	a := setup(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := a.Login("10.0.0.2", "wrong", now); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := a.Login("10.0.0.2", "correct-123", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("should be rate limited: %v", err)
	}
	tok, err := a.Login("10.0.0.3", "correct-123", now) // another IP is not limited
	if err != nil || !a.Valid(tok, now) {
		t.Fatalf("login: %v", err)
	}
	if _, err := a.Login("10.0.0.2", "correct-123", now.Add(61*time.Second)); err != nil {
		t.Fatalf("should be allowed after a minute: %v", err)
	}
	if a.Valid("made-up", now) || a.Valid(tok, now.Add(SessionTTL+time.Hour)) {
		t.Error("made-up or expired token accepted")
	}
}

func TestChangePasswordRevokesOthers(t *testing.T) {
	a := setup(t)
	now := time.Now()
	t1, _ := a.Login("ip", "correct-123", now)
	t2, _ := a.Login("ip", "correct-123", now)
	if err := a.ChangePassword(t1, "wrong", "new-secret-456"); !errors.Is(err, ErrBadPassword) {
		t.Fatal(err)
	}
	if err := a.ChangePassword(t1, "correct-123", "short"); !errors.Is(err, ErrTooShort) {
		t.Fatal(err)
	}
	if err := a.ChangePassword(t1, "correct-123", "new-secret-456"); err != nil {
		t.Fatal(err)
	}
	if !a.Valid(t1, now) || a.Valid(t2, now) {
		t.Error("must keep the current session and close the others")
	}
	if _, err := New(a.HashPath).Login("ip", "new-secret-456", now); err != nil {
		t.Errorf("the new password must persist: %v", err)
	}
}

func TestNoPassword(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "no"))
	if a.Configured() {
		t.Fatal("no file means no password")
	}
	if _, err := a.Login("ip", "x", time.Now()); !errors.Is(err, ErrNoPassword) {
		t.Fatal(err)
	}
}

// In parallel every request counts: no more than 5 passwords per IP and minute get through.
func TestRateLimitUnderConcurrency(t *testing.T) {
	a := setup(t)
	now := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	tried := 0
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Login("10.0.0.9", "wrong", now); errors.Is(err, ErrBadPassword) {
				mu.Lock()
				tried++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if tried != maxAttempts {
		t.Errorf("passwords tried in parallel: %d, max %d", tried, maxAttempts)
	}
	// global limit: many different IPs do not exceed maxGlobal either
	tried = 0
	for i := 0; i < 40; i++ {
		if _, err := a.Login("10.1.0."+string(rune('a'+i)), "wrong", now); errors.Is(err, ErrBadPassword) {
			tried++
		}
	}
	if tried+maxAttempts > maxGlobal {
		t.Errorf("global: %d + %d > %d", tried, maxAttempts, maxGlobal)
	}
}

// The global limit (20 a minute) does not hold back a client that signed in recently: someone on the LAN cannot
// lock the owner out by spending it. The client's own limit still applies, and it is forgotten after KnownTTL.
func TestGlobalLimitSparesKnownClients(t *testing.T) {
	a := setup(t)
	now := time.Now()
	if _, err := a.Login("10.0.0.9", "correct-123", now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxGlobal; i++ {
		a.Login(fmt.Sprintf("10.0.1.%d", i/maxAttempts), "wrong", now)
	}
	if _, err := a.Login("10.0.2.1", "correct-123", now); !errors.Is(err, ErrRateLimited) {
		t.Errorf("a new client past the global limit: %v", err)
	}
	if _, err := a.Login("10.0.0.9", "correct-123", now); err != nil {
		t.Errorf("the known client: %v", err)
	}
	for i := 0; i < maxAttempts; i++ {
		a.Login("10.0.0.9", "wrong", now)
	}
	if _, err := a.Login("10.0.0.9", "correct-123", now); !errors.Is(err, ErrRateLimited) {
		t.Errorf("the known client past its own limit: %v", err)
	}
	if !a.Known("10.0.0.9", now.Add(KnownTTL-time.Minute)) || a.Known("10.0.0.9", now.Add(KnownTTL)) {
		t.Error("KnownTTL")
	}
	// Bounded: the oldest goes first
	for i := 0; i < maxKnown+5; i++ {
		a.MarkKnown(fmt.Sprintf("10.9.0.%d", i), now.Add(time.Duration(i)*time.Second))
	}
	if len(a.known) != maxKnown || a.Known("10.9.0.0", now) || !a.Known(fmt.Sprintf("10.9.0.%d", maxKnown+4), now) {
		t.Errorf("known: %d", len(a.known))
	}
	a.MarkKnown("", now)
	if a.Known("", now) {
		t.Error("an empty key is never known")
	}
}

func TestCheckPasswordAndHashMode(t *testing.T) {
	a := setup(t)
	if a.CheckPassword("correct-123") != nil || !errors.Is(a.CheckPassword("wrong"), ErrBadPassword) {
		t.Error("CheckPassword")
	}
	if fi, err := os.Stat(a.HashPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("admin.hash mode: %v %v", fi.Mode(), err)
	}
	// An earlier version's 0640 is tightened when the app reads it
	os.Chmod(a.HashPath, 0o640)
	if !New(a.HashPath).Configured() {
		t.Fatal("not read")
	}
	if fi, _ := os.Stat(a.HashPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("not tightened: %v", fi.Mode())
	}
	if !errors.Is(New(filepath.Join(t.TempDir(), "none")).CheckPassword("x"), ErrNoPassword) {
		t.Error("no password")
	}
}
