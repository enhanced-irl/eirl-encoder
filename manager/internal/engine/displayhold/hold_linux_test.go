//go:build linux && cgo

package displayhold

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// A pipe stands in for /dev/fb0: libc answers ENOTTY to any framebuffer request on it, so whatever reaches
// libc shows up as ENOTTY and whatever the engine's ioctl keeps answers 0.
func pipeFD(t *testing.T) int {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return int(r.Fd())
}

func TestExported(t *testing.T) {
	if !Read().Active {
		t.Fatal("ioctl is not exported from the executable: the display libraries would get libc's")
	}
}

// What matters on the board: libnvdc, loaded at run time by nvdrmvideosink, reaches this ioctl. A library
// built here does the same as nvdcDPMS (power-down on the framebuffer's fd).
func TestReachesLibrariesLoadedLater(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "dpms.c")
	lib := filepath.Join(dir, "libdpms.so")
	code := "#include <linux/fb.h>\n#include <sys/ioctl.h>\n" +
		"int dpms_off(int fd) { return ioctl(fd, FBIOBLANK, FB_BLANK_POWERDOWN); }\n"
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(cc, "-shared", "-fPIC", "-o", lib, src).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	before := Read()
	rc, e := callLib(lib, "dpms_off", pipeFD(t))
	if rc != 0 || e != 0 {
		t.Fatalf("library's power-down: rc=%d errno=%v, want it held (libc would say ENOTTY)", rc, e)
	}
	if Read().Held-before.Held != 1 {
		t.Fatal("not counted as held")
	}
}

func TestHoldsThePowerDown(t *testing.T) {
	fd := pipeFD(t)
	before := Read()
	for _, level := range []uintptr{fbBlankVSync, fbBlankPowerdown} {
		if rc, e := call(fd, fbioBlank, level); rc != 0 || e != 0 {
			t.Fatalf("blank %d: rc=%d errno=%v, want it held (0)", level, rc, e)
		}
	}
	if got := Read().Held - before.Held; got != 2 {
		t.Fatalf("held %d, want 2", got)
	}
}

func TestLetsTheRestThrough(t *testing.T) {
	fd := pipeFD(t)
	for _, level := range []uintptr{fbBlankUnblank, fbBlankNormal} {
		if _, e := call(fd, fbioBlank, level); e != syscall.ENOTTY {
			t.Errorf("blank %d: errno=%v, want it passed to libc (ENOTTY)", level, e)
		}
	}
	var n int32
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	w.Write([]byte("abc"))
	if rc, e := call(int(r.Fd()), fionread, uintptr(unsafe.Pointer(&n))); rc != 0 || e != 0 || n != 3 {
		t.Fatalf("FIONREAD: rc=%d errno=%v n=%d, want 3 bytes", rc, e, n)
	}
}

func TestUnblankCancels(t *testing.T) {
	fd := pipeFD(t)
	call(fd, fbioBlank, fbBlankPowerdown)
	before := Read()
	call(fd, fbioBlank, fbBlankUnblank)
	var v [fbVarScreenInfoSz]byte
	call(fd, fbioPutVScreen, uintptr(unsafe.Pointer(&v[0])))
	st := Read()
	if st.Cancelled-before.Cancelled != 1 || st.Released != before.Released {
		t.Fatalf("cancelled +%d released +%d, want +1 and +0", st.Cancelled-before.Cancelled, st.Released-before.Released)
	}
}

// nvdcSetMode: power-down, mode, unblank. The held power-down goes out just before the mode, on its fd only.
func TestModeSetGetsTheBlank(t *testing.T) {
	fd, other := pipeFD(t), pipeFD(t)
	var v [fbVarScreenInfoSz]byte
	call(fd, fbioBlank, fbBlankPowerdown)
	before := Read()
	call(other, fbioPutVScreen, uintptr(unsafe.Pointer(&v[0])))
	if Read().Released != before.Released {
		t.Fatal("a mode set on another fd released the blank")
	}
	if _, e := call(fd, fbioPutVScreen, uintptr(unsafe.Pointer(&v[0]))); e != syscall.ENOTTY {
		t.Fatalf("mode set: errno=%v, want it passed to libc", e)
	}
	if Read().Released-before.Released != 1 {
		t.Fatal("the held blank did not go out before the mode set")
	}
	call(fd, fbioPutVScreen, uintptr(unsafe.Pointer(&v[0])))
	if Read().Released-before.Released != 1 {
		t.Fatal("released twice")
	}
}
