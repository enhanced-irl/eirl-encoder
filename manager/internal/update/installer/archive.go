package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update"
)

// Limits of a bundle's contents (it is signed, but a mistake must not fill the SD card).
const (
	maxBundleFiles = 10000
	maxBundleBytes = 1 << 30
)

// extractBundle unpacks a .tar.gz into dst: regular files and directories only, relative names without
// "..", modes reduced to 0755/0644. Anything else makes the whole bundle invalid.
func extractBundle(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files int
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("bundle: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\x00\\") {
			return fmt.Errorf("bundle: bad entry name %q", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			total += h.Size
			if files > maxBundleFiles || total > maxBundleBytes {
				return errors.New("bundle: too many files or too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return fmt.Errorf("bundle: %w", err)
			}
			_, err = io.Copy(out, io.LimitReader(tr, h.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("bundle: entry %q of type %q not allowed", h.Name, string(h.Typeflag))
		}
	}
}

// backupMeta is the first entry of a backup (and repeated in the in-progress marker).
type backupMeta struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	CreatedAt time.Time `json:"createdAt"`
	// Absent: paths of the new release that did not exist before: a restore removes them.
	Absent []string `json:"absent"`
}

const metaName = ".eirl-update-backup.json"

// writeBackup archives the existing paths (under root) into dst (tar, written to a temporary file,
// fsynced and renamed). Regular files and symlinks are kept with owner, mode and time; directories and
// anything else are skipped. Paths of newPaths that do not exist are recorded as absent.
func writeBackup(root, dst string, paths []string, newPaths map[string]bool, meta backupMeta, logf func(string, ...any)) (backupMeta, error) {
	var present []string
	for _, p := range paths {
		fi, err := os.Lstat(filepath.Join(root, p))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if newPaths[p] {
				meta.Absent = append(meta.Absent, p)
			}
		case err != nil:
			return meta, err
		case fi.Mode().IsRegular() || fi.Mode()&fs.ModeSymlink != 0:
			present = append(present, p)
		default:
			logf("backup: skipping %s (%s)", p, fi.Mode().Type())
		}
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return meta, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	tw := tar.NewWriter(f)
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: metaName, Mode: 0o600, Size: int64(len(mb)), ModTime: meta.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
		return meta, err
	}
	if _, err := tw.Write(mb); err != nil {
		return meta, err
	}
	for _, p := range present {
		if err := addToTar(tw, root, p); err != nil {
			return meta, fmt.Errorf("backup %s: %w", p, err)
		}
	}
	if err := tw.Close(); err != nil {
		return meta, err
	}
	if err := f.Sync(); err != nil {
		return meta, err
	}
	if err := f.Close(); err != nil {
		return meta, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return meta, err
	}
	ok = true
	syncDir(filepath.Dir(dst))
	return meta, nil
}

func addToTar(tw *tar.Writer, root, p string) error {
	full := filepath.Join(root, p)
	fi, err := os.Lstat(full)
	if err != nil {
		return err
	}
	link := ""
	if fi.Mode()&fs.ModeSymlink != 0 {
		if link, err = os.Readlink(full); err != nil {
			return err
		}
	}
	h, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	h.Name = strings.TrimPrefix(p, "/")
	h.Uname, h.Gname = "", ""
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		h.Uid, h.Gid = int(st.Uid), int(st.Gid)
	}
	h.Format = tar.FormatPAX
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	src, err := os.Open(full)
	if err != nil {
		return err
	}
	defer src.Close()
	n, err := io.Copy(tw, src)
	if err == nil && n != fi.Size() {
		err = fmt.Errorf("changed while reading (%d of %d bytes)", n, fi.Size())
	}
	return err
}

// readBackupMeta returns the metadata of a backup without restoring it.
func readBackupMeta(p string) (backupMeta, error) {
	var meta backupMeta
	f, err := os.Open(p)
	if err != nil {
		return meta, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	h, err := tr.Next()
	if err != nil {
		return meta, fmt.Errorf("backup %s: %w", p, err)
	}
	if h.Name != metaName || h.Size > 1<<20 {
		return meta, fmt.Errorf("backup %s: no metadata", p)
	}
	err = json.NewDecoder(io.LimitReader(tr, h.Size)).Decode(&meta)
	return meta, err
}

// restoreBackup puts every file of the backup back (each one through a temporary name in its own
// directory and a rename: a running binary or a script being read is never overwritten in place) and
// removes the paths that the failed release added. Only allowed paths are touched. engine reports whether
// the restore changed the engine's code, units or drop-in (then the engine has to be restarted to run them).
func restoreBackup(root, p string, logf func(string, ...any)) (meta backupMeta, engine bool, err error) {
	meta, err = readBackupMeta(p)
	if err != nil {
		return meta, false, err
	}
	f, err := os.Open(p)
	if err != nil {
		return meta, false, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	asRoot := os.Geteuid() == 0
	var errs []error
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return meta, engine, fmt.Errorf("backup %s: %w", p, err)
		}
		if h.Name == metaName {
			continue
		}
		abs := "/" + h.Name
		if !update.AllowedPath(abs) {
			errs = append(errs, fmt.Errorf("backup entry %q not allowed", h.Name))
			continue
		}
		var r io.Reader = tr
		if isEngineChange(abs) && h.Typeflag == tar.TypeReg && h.Size <= maxEngineCompare {
			// Does the restore change the engine (code, units, drop-in)? Only then does it need a restart
			b, err := io.ReadAll(io.LimitReader(tr, h.Size))
			if err != nil {
				return meta, engine, fmt.Errorf("backup %s: %w", p, err)
			}
			if cur, err := os.ReadFile(filepath.Join(root, abs)); err != nil || !bytes.Equal(cur, b) {
				engine = true
			}
			r = bytes.NewReader(b)
		} else if isEngineChange(abs) {
			engine = true
		}
		if err := restoreEntry(root, abs, h, r, asRoot); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", abs, err))
		}
	}
	for _, a := range meta.Absent {
		if !update.AllowedPath(a) {
			continue
		}
		full := filepath.Join(root, a)
		if fi, err := os.Lstat(full); err == nil && (fi.Mode().IsRegular() || fi.Mode()&fs.ModeSymlink != 0) {
			if err := os.Remove(full); err != nil {
				errs = append(errs, err)
			} else {
				logf("restore: removed %s (added by the failed release)", a)
				engine = engine || isEngineChange(a)
			}
		}
	}
	return meta, engine, errors.Join(errs...)
}

// maxEngineCompare: engine files up to this size are compared before a restore (the Go engine is ~10 MB).
const maxEngineCompare = 64 << 20

// isEngineCode: a path of the engine's code (what makes a bundle "carry the engine").
func isEngineCode(p string) bool { return strings.HasPrefix(p, update.LibDir+"/engine/") }

// isEngineChange: a path whose change needs an engine restart to apply: its code, its units and its drop-in.
// The drop-in is in every bundle (app-only too): the restore compares contents, so an unchanged one does not
// count.
func isEngineChange(p string) bool {
	return isEngineCode(p) || p == "/etc/systemd/system/eirl-engine.service" || p == "/etc/systemd/system/eirl-hdmi-init.service" ||
		strings.HasPrefix(p, "/etc/systemd/system/eirl-engine.service.d/")
}

func restoreEntry(root, abs string, h *tar.Header, r io.Reader, asRoot bool) error {
	target := filepath.Join(root, abs)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(target)+".eirl-restore")
	os.Remove(tmp)
	switch h.Typeflag {
	case tar.TypeSymlink:
		if err := os.Symlink(h.Linkname, tmp); err != nil {
			return err
		}
		if asRoot {
			os.Lchown(tmp, h.Uid, h.Gid)
		}
	case tar.TypeReg:
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(r, h.Size))
		if err == nil && n != h.Size {
			err = io.ErrUnexpectedEOF
		}
		if err == nil && asRoot {
			err = out.Chown(h.Uid, h.Gid)
		}
		if err == nil {
			err = out.Chmod(fs.FileMode(h.Mode) & fs.ModePerm)
		}
		if err == nil {
			err = out.Sync()
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Chtimes(tmp, h.ModTime, h.ModTime)
		}
		if err != nil {
			os.Remove(tmp)
			return err
		}
	default:
		return fmt.Errorf("entry type %q", string(h.Typeflag))
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
