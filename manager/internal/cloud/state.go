package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// State: the link to the account, in /etc/eirl-encoder/cloud.json (0600, only the app's user: the
// streaming service, which gets in through the group, cannot read it). It holds the token: never log it.
type State struct {
	EncoderID    string `json:"encoderId"`
	Token        string `json:"token"`
	APIBase      string `json:"apiBase"` // the token belongs to THIS backend: this one is used, not the flag's
	AccountLabel string `json:"accountLabel"`
	// AppliedVersion: last applied config.set (reported in hello; if the cloud has a newer one, it
	// resends it on connect).
	AppliedVersion int `json:"appliedVersion"`
	// AccessPoint: rescue | always (D2). Consumed by phase 3 (network and wizard).
	AccessPoint string `json:"accessPoint,omitempty"`
	// CloudDestination: the current destination was set by the cloud (with its key). It is deleted on
	// unlink; a destination the owner set locally before linking is kept.
	CloudDestination bool `json:"cloudDestination,omitempty"`
}

func (s State) linked() bool { return s.Token != "" && s.APIBase != "" && s.EncoderID != "" }

// StateFile reads and writes the state atomically. AllowHTTP: like ValidateBase's flag of the same
// name; only the simulator (against a local backend). On the device, a cloud.json rewritten with
// `http://` to another host does not turn it into a plaintext channel.
type StateFile struct {
	Path      string
	AllowHTTP bool
}

// Load: ok=false if there is no file (device not linked).
func (f *StateFile) Load() (State, bool, error) {
	// Only the app's own file: a cloud.json someone else created before the device was linked is ignored
	b, err := safefile.ReadOwned(f.Path, 0)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, false, fmt.Errorf("%s: %w", f.Path, err)
	}
	if !s.linked() {
		return State{}, false, fmt.Errorf("%s: incomplete", f.Path)
	}
	if err := ValidateBase(s.APIBase, f.AllowHTTP); err != nil {
		return State{}, false, fmt.Errorf("%s: apiBase: %w", f.Path, err)
	}
	return s, true, nil
}

func (f *StateFile) Save(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteAtomic(f.Path, append(b, '\n'), 0o600)
}

func (f *StateFile) Clear() error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
