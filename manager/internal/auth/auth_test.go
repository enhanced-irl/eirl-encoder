package auth

import (
	"errors"
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
