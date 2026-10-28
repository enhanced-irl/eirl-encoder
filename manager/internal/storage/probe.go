package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// MinSize: partitions and whole-disk filesystems smaller than this are ignored (a Windows installer's 1 MB
// UEFI_NTFS), and so is a disk smaller than this (RECORDING.md §4.2).
const MinSize = 1 << 30

// mbrLimit: the largest disk one MBR partition covers (2^32 sectors of 512 bytes). A larger disk is formatted with GPT.
const mbrLimit = int64(1) << 41

var (
	reDisk    = regexp.MustCompile(`^sd[a-z]$`)
	reUSBBus  = regexp.MustCompile(`^usb[0-9]+$`)
	reFsToken = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)
)

// ValidDisk: the only names the helper accepts, sda…sdz.
func ValidDisk(name string) bool { return reDisk.MatchString(name) }

// ValidFS: a filesystem's name as the state file carries it: ours (exfat, fat32…, ext4), or blkid's type for a disk the
// encoder does not use.
func ValidFS(fs string) bool { return reFsToken.MatchString(fs) }

// Disk: what sysfs says about a disk.
type Disk struct {
	Name     string
	Size     int64 // bytes
	Model    string
	USB      bool // under a USB bus in sysfs
	ReadOnly bool // write-protected (/sys/block/<disk>/ro)
	DevNum   string
	Parts    []Part
}

// Part: a partition in sysfs.
type Part struct {
	Name   string
	Size   int64
	DevNum string
}

// Candidate: a partition, or the whole disk, with what blkid found on it.
type Candidate struct {
	Name    string // sda1, or sda for a whole-disk filesystem
	Size    int64
	Type    string // blkid's ID_FS_TYPE: exfat, vfat, ext4, ntfs…; "" for nothing
	Version string // ID_FS_VERSION (FAT32, FAT16…)
	Label   string
	UUID    string
}

// readDisk reads /sys/block/<name> under sysDir. fs.ErrNotExist when the disk is not there.
func readDisk(sysDir, name string) (Disk, error) {
	d := Disk{Name: name}
	base := filepath.Join(sysDir, "block", name)
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		return d, err
	}
	for _, c := range strings.Split(filepath.ToSlash(real), "/") {
		if reUSBBus.MatchString(c) {
			d.USB = true
			break
		}
	}
	sectors, err := readInt(filepath.Join(base, "size"))
	if err != nil {
		return d, fmt.Errorf("size of %s: %w", name, err)
	}
	d.Size = sectors * 512
	if ro, err := readInt(filepath.Join(base, "ro")); err == nil && ro != 0 {
		d.ReadOnly = true
	}
	d.DevNum = readTrim(filepath.Join(base, "dev"), 32)
	d.Model = printable(readTrim(filepath.Join(base, "device", "model"), 128), 64)
	ents, err := os.ReadDir(base)
	if err != nil {
		return d, err
	}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, name) || len(n) == len(name) {
			continue
		}
		if _, err := strconv.Atoi(n[len(name):]); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, n, "partition")); err != nil {
			continue
		}
		s, err := readInt(filepath.Join(base, n, "size"))
		if err != nil {
			continue
		}
		d.Parts = append(d.Parts, Part{Name: n, Size: s * 512, DevNum: readTrim(filepath.Join(base, n, "dev"), 32)})
	}
	sort.Slice(d.Parts, func(i, j int) bool {
		a, _ := strconv.Atoi(d.Parts[i].Name[len(name):])
		b, _ := strconv.Atoi(d.Parts[j].Name[len(name):])
		return a < b
	})
	return d, nil
}

// ParseBlkid parses `blkid -p -o udev` (KEY=value lines; the _ENC values carry \xNN escapes).
func ParseBlkid(out string) (typ, version, label, uuid string) {
	vals := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok {
			vals[k] = v
		}
	}
	typ = vals["ID_FS_TYPE"]
	if !reFsToken.MatchString(typ) {
		typ = ""
		if vals["ID_FS_TYPE"] != "" {
			typ = "unknown"
		}
	}
	version = printable(vals["ID_FS_VERSION"], 16)
	label = vals["ID_FS_LABEL"]
	if enc, ok := vals["ID_FS_LABEL_ENC"]; ok {
		label = decodeEnc(enc)
	}
	label = printable(label, 64)
	uuid = vals["ID_FS_UUID"]
	if enc, ok := vals["ID_FS_UUID_ENC"]; ok {
		uuid = decodeEnc(enc)
	}
	uuid = printable(uuid, 64)
	return
}

// decodeEnc undoes blkid's \xNN escapes.
func decodeEnc(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// printable: valid UTF-8 without control characters, at most max bytes, trimmed.
func printable(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	for len(s) > max {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

// FSName: the name the state file gives a filesystem: exfat, fat32 (fat16, fat12), ext4, ntfs, or blkid's type.
func FSName(c Candidate) string {
	switch c.Type {
	case "vfat":
		switch strings.ToUpper(c.Version) {
		case "FAT16":
			return "fat16"
		case "FAT12":
			return "fat12"
		}
		return "fat32"
	}
	return c.Type
}

// Supported: a filesystem the encoder records to.
func Supported(c Candidate) bool {
	switch c.Type {
	case "exfat", "vfat", "ext4":
		return true
	}
	return false
}

// Decision: what to do with a disk.
type Decision struct {
	State  string
	Reason string
	// Pick: the candidate to mount (state ready if everything goes well); or, for unsupported, the one the state shows
	Pick *Candidate
}

// Decide chooses, from what blkid found, the partition to mount or what to report (RECORDING.md §4.2). parts are the
// disk's partitions; whole is the disk itself, used only when no partition has a filesystem (a whole-disk filesystem,
// or a FAT "superfloppy" whose boot sector the kernel read as a partition table).
func Decide(diskSize int64, parts []Candidate, whole *Candidate) Decision {
	if diskSize < MinSize {
		return Decision{State: StateUnsupported, Reason: ReasonTooSmall}
	}
	var found []Candidate
	for _, c := range parts {
		if c.Type != "" {
			found = append(found, c)
		}
	}
	if len(found) == 0 && whole != nil && whole.Type != "" {
		found = []Candidate{*whole}
	}
	largest := func(ok func(Candidate) bool) *Candidate {
		var best *Candidate
		for i := range found {
			c := found[i]
			if c.Size < MinSize || !ok(c) {
				continue
			}
			if best == nil || c.Size > best.Size {
				best = &found[i]
			}
		}
		return best
	}
	if c := largest(Supported); c != nil {
		return Decision{State: StateReady, Pick: c}
	}
	if c := largest(func(c Candidate) bool { return c.Type == "ntfs" }); c != nil {
		return Decision{State: StateUnsupported, Reason: ReasonFsNTFS, Pick: c}
	}
	if c := largest(func(Candidate) bool { return true }); c != nil {
		return Decision{State: StateUnsupported, Reason: ReasonFsUnknown, Pick: c}
	}
	if len(found) > 0 {
		return Decision{State: StateUnsupported, Reason: ReasonTooSmall}
	}
	return Decision{State: StateUnformatted}
}

// MountArgs: the type and options of `mount` for a candidate (RECORDING.md §4.2). exFAT and FAT are owned by the
// engine's user and readable by the group eirl-encoder (the app); ext4 keeps its own owners.
func MountArgs(c Candidate, uid, gid int) (fstype, opts string) {
	const common = "nosuid,nodev,noexec,noatime"
	owner := fmt.Sprintf("uid=%d,gid=%d,fmask=0137,dmask=0027", uid, gid)
	switch c.Type {
	case "exfat":
		return "exfat", common + "," + owner
	case "vfat":
		return "vfat", common + "," + owner
	case "ext4":
		// errors=remount-ro over the superblock's own setting: a disk set to panic on an error cannot reboot the board
		return "ext4", common + ",errors=remount-ro"
	}
	return "", ""
}

// readRegular reads a regular file without following a link or blocking on a FIFO, at most max bytes.
func readRegular(path string, max int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, max)
	}
	return b, nil
}

func readTrim(path string, max int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, int64(max)))
	return strings.TrimSpace(string(b))
}

func readInt(path string) (int64, error) {
	s := readTrim(path, 32)
	if s == "" {
		return 0, fmt.Errorf("%s: empty or missing", path)
	}
	return strconv.ParseInt(s, 10, 64)
}
