// Package service maneja eirl-engine.service: estado (systemctl show, sin privilegios), arrancar,
// parar y reiniciar (sudo -n con los comandos EXACTOS de /etc/sudoers.d/eirl-encoder) y journal.
package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Status struct {
	State    string    `json:"state"` // active | activating | deactivating | inactive | failed
	SubState string    `json:"subState"`
	Since    time.Time `json:"since,omitzero"`
	Restarts int       `json:"restarts"`
	// Started: cuándo arrancó el proceso actual (para saber si la configuración guardada ya está aplicada)
	Started time.Time `json:"-"`
}

type Unit struct {
	Name string // eirl-engine.service
}

func (u Unit) Status(ctx context.Context) (Status, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", u.Name,
		"-p", "ActiveState", "-p", "SubState", "-p", "NRestarts",
		"-p", "ActiveEnterTimestampMonotonic", "-p", "ExecMainStartTimestampMonotonic").Output()
	if err != nil {
		return Status{}, fmt.Errorf("systemctl show: %w", err)
	}
	return parseShow(string(out), bootTime()), nil
}

func parseShow(out string, boot time.Time) Status {
	var st Status
	mono := func(v string) time.Time {
		us, err := strconv.ParseInt(v, 10, 64)
		if err != nil || us == 0 || boot.IsZero() {
			return time.Time{}
		}
		return boot.Add(time.Duration(us) * time.Microsecond).UTC()
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "ActiveState":
			st.State = v
		case "SubState":
			st.SubState = v
		case "NRestarts":
			st.Restarts, _ = strconv.Atoi(v)
		case "ActiveEnterTimestampMonotonic":
			st.Since = mono(v).Truncate(time.Second)
		case "ExecMainStartTimestampMonotonic":
			st.Started = mono(v)
		}
	}
	return st
}

// bootTime = ahora − uptime: los timestamps monotónicos de systemd no dependen de la zona horaria
// ni de que el reloj (sin RTC en la Jetson) se haya sincronizado tarde.
func bootTime() time.Time {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return time.Time{}
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return time.Time{}
	}
	up, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return time.Time{}
	}
	return time.Now().Add(-time.Duration(up * float64(time.Second)))
}

var ErrBadAction = errors.New("acción desconocida")

// Control ejecuta `sudo -n /bin/systemctl <acción> <unidad>`. Los argumentos son literales y
// coinciden con la regla de sudoers; cualquier otra combinación la rechazaría sudo.
func (u Unit) Control(ctx context.Context, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return ErrBadAction
	}
	out, err := exec.CommandContext(ctx, "sudo", "-n", "/bin/systemctl", action, u.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", action, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Reboot reinicia la placa: `sudo -n /bin/systemctl reboot`, también un comando EXACTO de
// /etc/sudoers.d/eirl-encoder (orden reboot de la nube).
func (u Unit) Reboot(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "sudo", "-n", "/bin/systemctl", "reboot").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl reboot: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Poweroff apaga la placa: `sudo -n /bin/systemctl poweroff`, comando EXACTO de
// /etc/sudoers.d/eirl-encoder (el menú Power de la pantalla táctil).
func (u Unit) Poweroff(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "sudo", "-n", "/bin/systemctl", "poweroff").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl poweroff: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CanReboot: nil si la regla de sudoers para `/bin/systemctl reboot` existe (`sudo -n -l` con el
// comando exacto sale con 0 si está permitido y con 1 si no; -n: nunca pide contraseña).
func (u Unit) CanReboot(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "sudo", "-n", "-l", "/bin/systemctl", "reboot").CombinedOutput()
	if err != nil {
		return fmt.Errorf("sin permiso para /bin/systemctl reboot: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Tail devuelve las últimas n líneas útiles del journal de la unidad (redactadas, sin ruido).
func (u Unit) Tail(ctx context.Context, n int) ([]Entry, error) {
	// Se piden más de n porque el ruido de NVIDIA/GStreamer se descarta después
	cmd := exec.CommandContext(ctx, "journalctl", "-u", u.Name, "-o", "json", "--no-pager", "-n", strconv.Itoa(n*3))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	var entries []Entry
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if e, ok := ParseJournal(sc.Bytes()); ok {
			entries = append(entries, e)
		}
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}

// Follow manda las líneas nuevas del journal hasta que se cancela ctx.
func (u Unit) Follow(ctx context.Context, fn func(Entry)) error {
	cmd := exec.CommandContext(ctx, "journalctl", "-u", u.Name, "-o", "json", "--no-pager", "-f", "-n", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if e, ok := ParseJournal(sc.Bytes()); ok {
			fn(e)
		}
	}
	return cmd.Wait()
}
