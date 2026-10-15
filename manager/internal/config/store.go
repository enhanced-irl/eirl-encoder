package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// Store serialises reads and writes of the app's configuration: engine.env and the saved destinations next to it
// (destinations.json), under one lock.
type Store struct {
	DefaultPath string // /etc/default/eirl-engine (read only)
	AppPath     string // /etc/eirl-encoder/engine.env
	// CloudOwned: the destination engine.env holds was set by the cloud (cloud.json's cloudDestination). Only the
	// migration to destinations.json reads it, the first time: such a destination becomes the cloud's (eirl).
	// nil = not linked. Called with the lock held: it cannot call the Store.
	CloudOwned func() bool

	mu sync.Mutex
}

// maxDestFile: destinations.json's size limit (16 destinations with every secret at its longest stay well under it).
const maxDestFile = 128 << 10

// DestPath: destinations.json, next to engine.env.
func (st *Store) DestPath() string { return filepath.Join(filepath.Dir(st.AppPath), DestinationsFile) }

func (st *Store) Load() (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.load()
}

// load: engine.env as systemd and the engine see it, with ManagedBy from the saved destinations (the active one is
// the cloud's or not). If they cannot be read, the file's EIRL_MANAGED_BY.
func (st *Store) load() (Settings, error) {
	s, err := Load(st.DefaultPath, st.AppPath)
	if err != nil {
		return s, err
	}
	if ds, err := st.dests(s, false); err == nil {
		s.ManagedBy = ds.ManagedBy()
	}
	return s, nil
}

// dests: the saved destinations, or the migration of what engine.env holds the first time (persisted with persist;
// otherwise it is only computed, and its ids are not stable).
func (st *Store) dests(s Settings, persist bool) (Destinations, error) {
	b, err := safefile.ReadOwned(st.DestPath(), maxDestFile)
	if errors.Is(err, os.ErrNotExist) {
		ds := Migrate(s, st.CloudOwned != nil && st.CloudOwned())
		if persist {
			if err := st.writeDest(ds); err != nil {
				return ds, err
			}
		}
		return ds, nil
	}
	if err != nil {
		return Destinations{}, err
	}
	var ds Destinations
	if err := json.Unmarshal(b, &ds); err != nil {
		return Destinations{}, fmt.Errorf("%s: %w", DestinationsFile, err)
	}
	if err := ds.Validate(); err != nil {
		return Destinations{}, fmt.Errorf("%s: %w", DestinationsFile, err)
	}
	return ds.Clone(), nil
}

// writeDest: 0600, only the app's (it holds every destination's secrets; the engine never reads it).
func (st *Store) writeDest(ds Destinations) error {
	b, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(st.DestPath(), append(b, '\n'), 0o600)
}

// Destinations: the saved destinations (migrated and saved the first time) and the current settings.
func (st *Store) Destinations() (Destinations, Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, err := st.load()
	if err != nil {
		return Destinations{}, s, err
	}
	ds, err := st.dests(s, true)
	if err != nil {
		return ds, s, err
	}
	s.ManagedBy = ds.ManagedBy()
	return ds, s, nil
}

// UpdateDestinations applies fn on the saved destinations and the current settings and, if it does not fail and
// everything is valid, saves both: destinations.json, then engine.env with the active destination flattened into it
// (Flatten). fn changes the global keys (Output…) on s; the destination's are overwritten by the active one.
func (st *Store) UpdateDestinations(fn func(ds *Destinations, s *Settings) error) (Destinations, Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cur, err := st.load()
	if err != nil {
		return Destinations{}, cur, err
	}
	old, err := st.dests(cur, false)
	if err != nil {
		return old, cur, err
	}
	ds, s := old.Clone(), cur
	if err := fn(&ds, &s); err != nil {
		return old, cur, err
	}
	if err := ds.Validate(); err != nil {
		return old, cur, err
	}
	s = ds.Flatten(s)
	if err := s.Validate(); err != nil {
		return old, cur, err
	}
	if err := st.writeDest(ds); err != nil {
		return old, cur, err
	}
	if err := Save(st.AppPath, s); err != nil {
		// engine.env is what streams: the list goes back to what it had, so that they keep agreeing
		if rerr := st.writeDest(old); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return old, cur, err
	}
	return ds, s, nil
}

// Update applies fn on the current configuration and saves the result if fn does not fail. Only for the global
// keys (OUTPUT, EDID_PREFERRED, the input): the destination's are kept as they are (they change through
// UpdateDestinations).
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
	copyDestination(&next, cur)
	if err := Save(st.AppPath, next); err != nil {
		return cur, err
	}
	return next, nil
}

// Env: the app file's keys as they are written (nil without the file): before and after a save, DiffEnv tells
// whether only output keys changed (the hot output_config, OUTPUTS.md §9.4).
func (st *Store) Env() map[string]string {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, err := safefile.ReadOwned(st.AppPath, 0)
	if err != nil {
		return nil
	}
	env, err := parseEnv(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return env
}

// Snapshot: the app file before a save, for the apply rule (Hashes for the restart, Env for the hot change).
type Snapshot struct {
	Hashes Hashes
	Env    map[string]string
}

func (st *Store) Snapshot() Snapshot { return Snapshot{Hashes: st.Hashes(), Env: st.Env()} }

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
