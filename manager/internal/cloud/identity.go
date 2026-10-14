package cloud

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/safefile"
)

// IdentityFile: the device's secret (§3.1), in /etc/eirl-encoder/identity (0600, the app's user).
// 32 random bytes in base64url, generated the first time they are needed (when requesting a code).
// It proves to the cloud that it is the SAME device: re-linking a hardwareId that is still active in
// another account is only allowed with it (otherwise `hardware_claimed`). That is why it is NOT
// deleted on unlink (neither from the web, nor with 4401, nor with unlink): it only changes if the
// system is reinstalled. Never to the logs nor to the web.
type IdentityFile struct {
	Path string

	mu sync.Mutex
}

var reIdentity = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`) // 32 bytes in base64url without padding

// LoadOrCreate returns the stored identity or, if there is no file, generates and stores it. A file
// that exists but is not valid is an error: regenerating it would change the device's identity and
// stop it from reclaiming itself; it is fixed by hand (deleting it, which is the same as reinstalling).
func (f *IdentityFile) LoadOrCreate() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := safefile.ReadOwned(f.Path, 4<<10)
	switch {
	case errors.Is(err, safefile.ErrUntrusted):
		// Someone else created the name (to choose or learn the secret): never used, never overwritten
		return "", fmt.Errorf("%s is not the app's file: remove it", f.Path)
	case err == nil:
		id := strings.TrimSpace(string(b))
		if !reIdentity.MatchString(id) {
			return "", fmt.Errorf("%s is not a valid identity (delete it to generate a new one: the device will count as new)", f.Path)
		}
		return id, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	if err := config.WriteAtomic(f.Path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}
