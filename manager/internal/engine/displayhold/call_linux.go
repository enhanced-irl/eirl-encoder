//go:build linux && cgo

package displayhold

/*
#include <dlfcn.h>
#include <errno.h>
#include <linux/fb.h>
#include <stdint.h>
#include <stdlib.h>

int ioctl(int fd, unsigned long req, ...);

static int call(int fd, unsigned long req, uintptr_t arg, int *err) {
	errno = 0;
	int rc = ioctl(fd, req, (void *)arg);
	*err = errno;
	return rc;
}

// Like libnvdc: a library loaded at run time calls ioctl through its own PLT.
static int call_lib(const char *path, const char *fn, int fd, int *err) {
	void *h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (!h)
		return -2;
	int (*f)(int) = (int (*)(int))dlsym(h, fn);
	if (!f) {
		dlclose(h);
		return -3;
	}
	errno = 0;
	int rc = f(fd);
	*err = errno;
	dlclose(h);
	return rc;
}
*/
import "C"

import (
	"syscall"
	"unsafe"
)

// Requests and levels from <linux/fb.h>, for the tests.
const (
	fbioBlank         = C.FBIOBLANK
	fbioPutVScreen    = C.FBIOPUT_VSCREENINFO
	fbBlankUnblank    = C.FB_BLANK_UNBLANK
	fbBlankNormal     = C.FB_BLANK_NORMAL
	fbBlankVSync      = C.FB_BLANK_VSYNC_SUSPEND
	fbBlankPowerdown  = C.FB_BLANK_POWERDOWN
	fionread          = 0x541B
	fbVarScreenInfoSz = C.sizeof_struct_fb_var_screeninfo
)

// call goes through the engine's ioctl like a display library would.
func call(fd int, req uint64, arg uintptr) (int, syscall.Errno) {
	var e C.int
	rc := C.call(C.int(fd), C.ulong(req), C.uintptr_t(arg), &e)
	return int(rc), syscall.Errno(e)
}

// callLib loads the shared library at path and calls its fn(fd); -2 and -3 if it cannot load it or find fn.
func callLib(path, fn string, fd int) (int, syscall.Errno) {
	cp, cf := C.CString(path), C.CString(fn)
	defer C.free(unsafe.Pointer(cp))
	defer C.free(unsafe.Pointer(cf))
	var e C.int
	rc := C.call_lib(cp, cf, C.int(fd), &e)
	return int(rc), syscall.Errno(e)
}
