package splash

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The splash's extlinux entry: a copy of the default entry with fbcon=map:9, so the text console
// does not take the screen. The board rules ask for a NEW entry (never touch the one that boots
// today) and a backup before writing: the latter is done by whoever writes the file.
//
// entryBegin, entryEnd and the MENU LABEL stay byte-for-byte as they were: boards already have them
// in /boot/extlinux/extlinux.conf, removeEntry finds the block by its markers, and any change would
// rewrite /boot on the next install (or leave a duplicate block).
const (
	EntryLabel = "eirl-splash"
	entryBegin = "# --- eirl-splash: pantalla de carga de EnhancedIRL (la escribe `eirl-splash boot-entry`; ver SPLASH.md) ---"
	entryEnd   = "# --- eirl-splash: fin ---"
	// fbcon=map:9: the console binds to no framebuffer (there is no fb9). It goes last because
	// ${cbootargs} already carries fbcon=map:0 (set by CBoot) and the last one wins: fb_console_setup
	// processes each fbcon= in order and each map: overrides the previous one.
	entryArgs = "fbcon=map:9"
	// TIMEOUT in tenths of a second (U-Boot rounds it to seconds): 1 s of menu instead of 3 (D3).
	// The menu is only visible on the serial console; on screen that time is the NVIDIA logo.
	entryTimeout = "10"
)

// The B01 device trees with the TC358743 (UPDATES.md §12.11). BaseFDT is the frozen base's (CAM1 only), which
// every entry but ours keeps booting. DualFDT (a chip on CAM0, on CAM1 or on both) comes with the system layer:
// our entry boots it instead of BaseFDT once the layer has installed it.
const (
	BaseFDT = "/boot/tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb"
	DualFDT = "/boot/tegra210-p3448-0000-p3449-0000-b00-tc358743-dual.dtb"
)

// FDTSwap: our entry boots To where its base entry boots From. The zero value changes nothing.
type FDTSwap struct{ From, To string }

// LayerFDT is the swap our entry makes once the system layer has installed DualFDT: the file under root is the one
// the layer wrote (owned: path → sha256, the Files of its state.json) and a whole flattened device tree. Otherwise
// the zero swap, and our entry boots what its base entry boots: a board whose layer was skipped or failed, or the
// test board's hand-installed copy, keeps its boot as it is.
func LayerFDT(root string, owned map[string]string) FDTSwap {
	want := owned[DualFDT]
	if want == "" {
		return FDTSwap{}
	}
	b, err := os.ReadFile(filepath.Join(root, DualFDT))
	if err != nil || len(b) < 40 || binary.BigEndian.Uint32(b) != 0xd00dfeed || int(binary.BigEndian.Uint32(b[4:])) != len(b) {
		return FDTSwap{}
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != want {
		return FDTSwap{}
	}
	return FDTSwap{From: BaseFDT, To: DualFDT}
}

// EntryFDT is the device tree our entry boots in conf ("" without the entry or without an FDT line).
func EntryFDT(conf string) string {
	body, err := labelBody(splitLines(conf), EntryLabel)
	if err != nil {
		return ""
	}
	for _, l := range body {
		if k, rest := keyRest(l); k == "FDT" {
			return rest
		}
	}
	return ""
}

// BootPrev: what was there before installing the entry, to undo it. Timeout "" = there was none.
type BootPrev struct{ Default, Timeout string }

func (p BootPrev) String() string { return "DEFAULT=" + p.Default + "\nTIMEOUT=" + p.Timeout + "\n" }

func ParseBootPrev(s string) (BootPrev, error) {
	var p BootPrev
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, _ := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch k {
		case "DEFAULT":
			p.Default = v
		case "TIMEOUT":
			p.Timeout = v
		}
	}
	if p.Default == "" || p.Default == EntryLabel {
		return BootPrev{}, fmt.Errorf("previous state without a valid DEFAULT")
	}
	return p, nil
}

// InstallEntry returns extlinux.conf with the entry (re)generated as DEFAULT and a short TIMEOUT,
// plus what was there before. prev (may be nil) is the saved state of a previous install: it is
// needed to reinstall when DEFAULT is already ours. The base is the current default entry. fdt changes
// our entry's FDT when the base's is fdt.From; every other entry, the base's too, stays as it is, so
// U-Boot falls back to them if ours cannot load its files.
func InstallEntry(conf string, prev *BootPrev, fdt FDTSwap) (string, BootPrev, error) {
	lines, err := removeEntry(splitLines(conf))
	if err != nil {
		return "", BootPrev{}, err
	}
	def, di := globalKey(lines, "DEFAULT")
	timeout, ti := globalKey(lines, "TIMEOUT")
	var p BootPrev
	switch {
	case def != "" && def != EntryLabel:
		p = BootPrev{Default: def, Timeout: timeout}
	case prev != nil && prev.Default != "" && prev.Default != EntryLabel:
		p = *prev
	default:
		return "", BootPrev{}, fmt.Errorf("DEFAULT is %q and there is no previous state: no entry to start from", def)
	}
	base, err := labelBody(lines, p.Default)
	if err != nil {
		return "", BootPrev{}, err
	}

	block := []string{"", entryBegin, "LABEL " + EntryLabel,
		"      MENU LABEL EnhancedIRL: pantalla de carga (base: " + p.Default + ")"}
	for _, l := range base {
		key, rest := keyRest(l)
		switch {
		case key == "APPEND" && !strings.Contains(" "+rest+" ", " "+entryArgs+" "):
			l = strings.TrimRight(l, " \t") + " " + entryArgs
		case key == "FDT" && fdt.From != "" && fdt.To != "" && rest == fdt.From:
			l = "FDT " + fdt.To
		}
		block = append(block, "      "+l)
	}
	block = append(block, entryEnd)

	lines = setGlobal(lines, di, "DEFAULT "+EntryLabel)
	_, ti = globalKey(lines, "TIMEOUT") // setGlobal may have inserted a line above
	lines = setGlobal(lines, ti, "TIMEOUT "+entryTimeout)
	lines = append(lines, block...)
	out := joinLines(lines)

	// Check the result before returning it
	if d, _ := globalKey(splitLines(out), "DEFAULT"); d != EntryLabel {
		return "", BootPrev{}, fmt.Errorf("result without DEFAULT %s", EntryLabel)
	}
	if _, err := labelBody(splitLines(out), EntryLabel); err != nil {
		return "", BootPrev{}, err
	}
	return out, p, nil
}

// UninstallEntry removes the entry and puts DEFAULT and TIMEOUT back as they were. If someone changed
// DEFAULT to another entry afterwards, that is respected. With DEFAULT ours and no previous state,
// error.
func UninstallEntry(conf string, prev *BootPrev) (string, error) {
	lines, err := removeEntry(splitLines(conf))
	if err != nil {
		return "", err
	}
	def, di := globalKey(lines, "DEFAULT")
	if def == EntryLabel || def == "" {
		if prev == nil || prev.Default == "" {
			return "", fmt.Errorf("DEFAULT is %q and there is no previous state: restore an extlinux.conf.bak-* backup", def)
		}
		lines = setGlobal(lines, di, "DEFAULT "+prev.Default)
	}
	if prev != nil {
		if timeout, ti := globalKey(lines, "TIMEOUT"); timeout == entryTimeout {
			if prev.Timeout == "" {
				lines = append(lines[:ti], lines[ti+1:]...)
			} else {
				lines[ti] = "TIMEOUT " + prev.Timeout
			}
		}
	}
	return joinLines(lines), nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") + "\n" }

// keyRest: keyword (upper-cased) and rest of a line; comments have no keyword.
func keyRest(line string) (string, string) {
	t := strings.TrimSpace(line)
	if t == "" || t[0] == '#' {
		return "", ""
	}
	i := strings.IndexAny(t, " \t")
	if i < 0 {
		return strings.ToUpper(t), ""
	}
	return strings.ToUpper(t[:i]), strings.TrimSpace(t[i+1:])
}

// globalKey: value and index of the first line with that key before the first LABEL (-1 if absent).
func globalKey(lines []string, key string) (string, int) {
	for i, l := range lines {
		k, rest := keyRest(l)
		if k == "LABEL" {
			break
		}
		if k == key {
			return rest, i
		}
	}
	return "", -1
}

// setGlobal replaces line i or, if i < 0, puts it at the top of the file.
func setGlobal(lines []string, i int, line string) []string {
	if i >= 0 {
		lines[i] = line
		return lines
	}
	return append([]string{line}, lines...)
}

// labelBody: boot lines (LINUX, FDT, INITRD, APPEND…) of entry name, without indentation.
func labelBody(lines []string, name string) ([]string, error) {
	var body []string
	in, found := false, false
	for _, l := range lines {
		k, rest := keyRest(l)
		if k == "LABEL" {
			in = rest == name
			found = found || in
			continue
		}
		if in {
			switch k {
			case "LINUX", "KERNEL", "FDT", "FDTDIR", "INITRD", "APPEND":
				body = append(body, strings.TrimSpace(l))
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("no LABEL %s in extlinux.conf", name)
	}
	var hasKernel, hasAppend bool
	for _, l := range body {
		k, _ := keyRest(l)
		hasKernel = hasKernel || k == "LINUX" || k == "KERNEL"
		hasAppend = hasAppend || k == "APPEND"
	}
	if !hasKernel || !hasAppend {
		return nil, fmt.Errorf("entry %s has no LINUX and APPEND", name)
	}
	return body, nil
}

// removeEntry removes our block (and the blank line put before it).
func removeEntry(lines []string) ([]string, error) {
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if lines[i] != entryBegin {
			out = append(out, lines[i])
			continue
		}
		if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
			out = out[:n-1]
		}
		for i < len(lines) && lines[i] != entryEnd {
			i++
		}
		if i == len(lines) {
			return nil, fmt.Errorf("unterminated eirl-splash block in extlinux.conf")
		}
	}
	return out, nil
}
