package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Systemctl is the real Systemd (/bin/systemctl).
type Systemctl struct{}

func (Systemctl) Run(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "/bin/systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (Systemctl) Show(ctx context.Context, unit, prop string) (string, error) {
	out, err := exec.CommandContext(ctx, "/bin/systemctl", "show", "-p", prop, "--value", unit).Output()
	return strings.TrimSpace(string(out)), err
}

// RunInstallScript runs <dir>/install.sh as root, non-interactive, with a fixed PATH and a minimal
// environment (nothing of the caller's environment leaks into it).
func RunInstallScript(ctx context.Context, dir string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "install.sh"))
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/root",
		"EIRL_OTA=1", // install.sh: run by the update helper (no hints for a human)
	}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// CheckApp returns an AppHealth that asks the app over its local socket (/run/eirl-encoder/api.sock)
// for GET /api/v1/update. Any HTTP answer means the app is up; if it is the update status, the app's
// own version ("app", the binary's) must be the expected release.
func CheckApp(socket string) func(ctx context.Context, expect string) error {
	cl := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}}
	return func(ctx context.Context, expect string) error {
		if _, err := os.Stat(socket); err != nil {
			return fmt.Errorf("no socket: %w", err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://eirl-encoderd/api/v1/update", nil)
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil // up (an app without /update on its socket); its version is not checked
		}
		var st struct {
			App     string `json:"app"`
			Current string `json:"current"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&st); err != nil {
			return nil
		}
		v := st.App
		if v == "" {
			v = st.Current
		}
		if v != "" && v != expect {
			return fmt.Errorf("the app runs %s", v)
		}
		return nil
	}
}
