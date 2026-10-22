package syslayer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The layer's directory on the board (UPDATES.md §12.3): state.json (0644, the app reads it), the marker of
// an apply in progress and its copies (0700).
const (
	DefaultStateDir = "/var/lib/eirl-system"
	StateFile       = "state.json"
	markerFile      = "in-progress.json"
	txnDir          = "txn"
	lockFile        = "lock"
)

// State is state.json: what the layer owns on this board after its last apply, and what waits for a
// restart. Additive: the app ignores what it does not know.
type State struct {
	V         int               `json:"v"`
	Release   string            `json:"release,omitempty"`  // the release whose layer is applied
	AppliedAt time.Time         `json:"appliedAt,omitzero"` // when it was (last) applied
	Image     bool              `json:"image,omitempty"`    // applied while the SD card image was built
	BootID    string            `json:"bootId,omitempty"`   // the boot it was applied in: Pending is for that boot only
	Files     map[string]string `json:"files"`              // path → sha256 of what the layer wrote
	Packages  map[string]string `json:"packages"`           // name → version the layer lists
	Units     map[string]string `json:"units"`              // unit → state the layer fixes
	// Pending: what acts only after a restart (UPDATES.md §12.5), for BootID's boot
	Pending []Pending `json:"pending,omitempty"`
	Drift   *Drift    `json:"drift,omitempty"`
	// Skipped: why the last layer was not applied (a board that does not come from the encoder's image)
	Skipped string `json:"skipped,omitempty"`
	// Error: the last apply failed and was undone (code: reason). The previous layer's state is kept
	Error    string    `json:"error,omitempty"`
	FailedAt time.Time `json:"failedAt,omitzero"`
	Failed   string    `json:"failed,omitempty"` // the release whose layer failed
}

// Pending is one change that acts after a restart.
type Pending struct {
	Area   string `json:"area"`   // hdmi | bluetooth | uplinks | system
	What   string `json:"what"`   // the module, unit or package
	Effect string `json:"effect"` // reboot
}

// ReadState reads state.json from dir (a zero State, never nil, when there is none).
func ReadState(dir string) *State {
	st := &State{V: Format}
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil || len(b) > 4<<20 {
		return st
	}
	if json.Unmarshal(b, st) != nil {
		return &State{V: Format}
	}
	return st
}

// PendingNow: what still waits for a restart: Pending, if this is still the boot it was recorded in.
func (s *State) PendingNow(currentBootID string) []Pending {
	if s == nil || s.BootID == "" || s.BootID != currentBootID {
		return nil
	}
	return s.Pending
}

// CurrentBootID: this boot's id on the board.
func CurrentBootID() string { return bootID("/proc") }

func (s *State) write(dir string) error {
	if s.Files == nil {
		s.Files = map[string]string{}
	}
	if s.Packages == nil {
		s.Packages = map[string]string{}
	}
	if s.Units == nil {
		s.Units = map[string]string{}
	}
	s.V = Format
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, StateFile), append(b, '\n'), 0o644)
}

// marker is in-progress.json: written once every copy is safe and before anything changes; removed at the
// commit or once undone. Present at boot = an apply was cut: eirl-update recover undoes it.
type marker struct {
	V         int         `json:"v"`
	Release   string      `json:"release"`
	StartedAt time.Time   `json:"startedAt"`
	Image     bool        `json:"image,omitempty"`
	Files     []savedFile `json:"files"`
	Depmod    bool        `json:"depmod,omitempty"`
	Kernel    string      `json:"kernel"`
	Units     []unitOp    `json:"units,omitempty"`
	New       []string    `json:"new,omitempty"`     // packages dpkg did not have installed: purged when undone
	Upgrade   []upgrade   `json:"upgrade,omitempty"` // packages it upgrades: completed when undone (no older .deb)
	PolicyRcD bool        `json:"policyRcD,omitempty"`
	State     bool        `json:"state,omitempty"` // state.json was there: its copy is txn/state.json
}

// savedFile: a file the apply changes and how to put it back.
type savedFile struct {
	Path   string `json:"path"`
	Copy   string `json:"copy,omitempty"` // the copy under txn/files ("" = it did not exist: removed when undone)
	Mode   uint32 `json:"mode,omitempty"`
	Module bool   `json:"module,omitempty"`
}

type upgrade struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Deb     string `json:"deb"` // its copy under txn/debs
}
