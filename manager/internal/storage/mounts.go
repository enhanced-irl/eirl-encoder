package storage

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strconv"
	"strings"
)

// Mount: a line of /proc/<pid>/mountinfo.
type Mount struct {
	DevNum     string   // major:minor (0:N for FUSE and other anonymous devices)
	Mountpoint string   // unescaped
	MountOpts  []string // per-mount options: rw/ro, nosuid, nodev, noexec, noatime…
	FSType     string   // ext4, vfat, fuseblk…
	Source     string   // /dev/sda1 (exfat-fuse names it so too)
	SuperOpts  []string // the filesystem's options
}

// ParseMountinfo parses /proc/<pid>/mountinfo (proc(5)).
func ParseMountinfo(s string) []Mount {
	var ms []Mount
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 7 {
			continue
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(f) {
			continue
		}
		m := Mount{
			DevNum:     f[2],
			Mountpoint: unescapeOctal(f[4]),
			MountOpts:  strings.Split(f[5], ","),
			FSType:     f[sep+1],
			Source:     unescapeOctal(f[sep+2]),
		}
		if sep+3 < len(f) {
			m.SuperOpts = strings.Split(f[sep+3], ",")
		}
		ms = append(ms, m)
	}
	return ms
}

// unescapeOctal undoes the kernel's \NNN escapes of spaces, tabs, newlines and backslashes.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hasOpt(opts []string, o string) bool {
	for _, x := range opts {
		if x == o {
			return true
		}
	}
	return false
}

// MissingFlags: which of nosuid, nodev and noexec a mount lacks (exfat-fuse may not pass them all to the kernel).
func MissingFlags(m Mount) []string {
	var miss []string
	for _, o := range []string{"nosuid", "nodev", "noexec"} {
		if !hasOpt(m.MountOpts, o) {
			miss = append(miss, o)
		}
	}
	return miss
}

// MountReadOnly: the mount, or its filesystem (the kernel remounts a damaged one read-only), is read-only.
func MountReadOnly(m Mount) bool { return hasOpt(m.MountOpts, "ro") || hasOpt(m.SuperOpts, "ro") }

// maxEngineFile: the largest engine state file that is read.
const maxEngineFile = 1 << 20

// Recording says whether the engine records, from its live state (/run/eirl-engine/mixer.json, written by the engine's
// user: read without following links, a regular file, bounded): record.mode "on", or record.state "starting" or "on".
// No file (no engine) is not recording; neither is a file that cannot be parsed (the engine writes it whole), which is
// said in the second value so that the caller logs it.
func Recording(path string) (bool, string) {
	b, err := readRegular(path, maxEngineFile)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "no engine state"
	}
	if err != nil {
		return false, "engine state unreadable: " + err.Error()
	}
	var st struct {
		Record *struct {
			Mode  string `json:"mode"`
			State string `json:"state"`
		} `json:"record"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return false, "engine state unparsable"
	}
	switch {
	case st.Record == nil:
		return false, "engine without a recorder"
	case st.Record.Mode == "on":
		return true, "record.mode=on"
	case st.Record.State == "starting" || st.Record.State == "on":
		return true, "record.state=" + st.Record.State
	}
	return false, "not recording"
}
