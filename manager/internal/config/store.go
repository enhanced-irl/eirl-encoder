package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// Store serialises reads and writes of the app's configuration.
type Store struct {
	DefaultPath string // /etc/default/eirl-engine (read only)
	AppPath     string // /etc/eirl-encoder/engine.env
	// ManagedBy, when set, decides who manages the destination ("local" | "eirl") instead of the
	// file's EIRL_MANAGED_BY key: the source of truth is the cloud link (cloud.json). So linking or
	// unlinking does not rewrite the .env, whose fingerprint would change and the web would warn of
	// "unapplied changes" that do not exist. The file's key is rewritten with the right value on the
	// next save. Called with the lock held: it cannot call the Store.
	ManagedBy func() string

	mu sync.Mutex
}

func (st *Store) Load() (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.load()
}

func (st *Store) load() (Settings, error) {
	s, err := Load(st.DefaultPath, st.AppPath)
	if st.ManagedBy != nil {
		s.ManagedBy = st.ManagedBy()
	}
	return s, err
}

// Update applies fn on the current configuration and saves the result if fn does not fail.
func (st *Store) Update(fn func(Settings) (Settings, error)) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cur, err := st.load()
	if err != nil {
		return cur, err
	}
	next, err := fn(cur)
	if err != nil {
		return cur, err
	}
	if err := Save(st.AppPath, next); err != nil {
		return cur, err
	}
	return next, nil
}

// Hash: fingerprint of the app's file with the same normalisation as the engine (content without
// trailing newlines + "\n"), which leaves its own in /run/eirl-engine/state at start. If they differ,
// there is saved configuration not applied yet. "none" without a file (like the script).
func (st *Store) Hash() string { return st.Hashes().Full }

// Hashes: the fingerprint of the whole app file (Full, the one the engine leaves without the mixer)
// and without the OUTPUT line (NoOutput, the one it leaves with the mixer: starting and stopping the
// stream are live and not "unapplied changes"). Same rule as the script: content without trailing
// newlines + "\n".
type Hashes struct{ Full, NoOutput string }

func (st *Store) Hashes() Hashes {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, err := safefile.ReadOwned(st.AppPath, 0)
	if err != nil {
		return Hashes{"none", "none"}
	}
	content := strings.TrimRight(string(b), "\n")
	var kept []string
	for _, l := range strings.Split(content, "\n") {
		if !strings.HasPrefix(l, "OUTPUT=") {
			kept = append(kept, l)
		}
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	return Hashes{Full: sum(content + "\n"), NoOutput: sum(strings.Join(kept, "\n") + "\n")}
}
