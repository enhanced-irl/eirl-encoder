package splash

import (
	"bufio"
	"fmt"
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
// needed to reinstall when DEFAULT is already ours. The base is the current default entry.
func InstallEntry(conf string, prev *BootPrev) (string, BootPrev, error) {
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
		if key, rest := keyRest(l); key == "APPEND" && !strings.Contains(" "+rest+" ", " "+entryArgs+" ") {
			l = strings.TrimRight(l, " \t") + " " + entryArgs
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
