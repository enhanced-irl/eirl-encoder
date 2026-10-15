//go:build linux && cgo

// The engine's ioctl: it keeps the board's HDMI output lit when the screen's pipeline closes (MIXER.md §3,
// trap 27).
//
// nvdrmvideosink goes through NVIDIA's libdrm, which treats taking the last plane off the head as switching
// the head off: libnvdc's nvdcDPMS sends FBIOBLANK with a suspend or power-down level to /dev/fb0 and tegradc
// turns the HDMI off (tegra_fb_blank → tegra_dc_disable). The panel shows "no signal" and drops its hot plug
// ~4 s later, and the next screen fails with "Failed to set plane".
//
// The executable exports this ioctl (-Wl,--export-dynamic), so the libraries the engine loads resolve to it
// before libc's. It holds back those blanks and lets everything else through. A mode set on the same fd
// (FBIOPUT_VSCREENINFO, which nvdcSetMode sends between a power-down and an unblank) gets the held blank just
// before, exactly as it was asked for; an unblank on that fd cancels it.

#define _GNU_SOURCE
#include "hold.h"

#include <dlfcn.h>
#include <errno.h>
#include <linux/fb.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdint.h>

typedef int (*ioctl_fn)(int, unsigned long, ...);

static ioctl_fn real_ioctl;
static pthread_once_t resolve_once = PTHREAD_ONCE_INIT;
static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static int pending_fd = -1; // the fd whose blank is held back, -1 if none
static unsigned long held, released, cancelled;

static void resolve(void) { real_ioctl = (ioctl_fn)dlsym(RTLD_NEXT, "ioctl"); }

int ioctl(int fd, unsigned long req, ...) {
	va_list ap;
	va_start(ap, req);
	void *arg = va_arg(ap, void *);
	va_end(ap);
	pthread_once(&resolve_once, resolve);
	if (!real_ioctl) {
		errno = ENOSYS;
		return -1;
	}
	if (req == FBIOBLANK) {
		// FB_BLANK_NORMAL only hides the windows; the suspend levels and power-down switch the head off
		if ((uintptr_t)arg >= FB_BLANK_VSYNC_SUSPEND) {
			pthread_mutex_lock(&mu);
			pending_fd = fd;
			held++;
			pthread_mutex_unlock(&mu);
			return 0;
		}
		pthread_mutex_lock(&mu);
		if (pending_fd == fd) {
			pending_fd = -1;
			cancelled++;
		}
		pthread_mutex_unlock(&mu);
	} else if (req == FBIOPUT_VSCREENINFO) {
		int flush = 0;
		pthread_mutex_lock(&mu);
		if (pending_fd == fd) {
			pending_fd = -1;
			released++;
			flush = 1;
		}
		pthread_mutex_unlock(&mu);
		if (flush)
			real_ioctl(fd, FBIOBLANK, (void *)(uintptr_t)FB_BLANK_POWERDOWN);
	}
	return real_ioctl(fd, req, arg);
}

int eirl_hold_active(void) { return dlsym(RTLD_DEFAULT, "ioctl") == (void *)ioctl; }

void eirl_hold_counts(unsigned long *h, unsigned long *r, unsigned long *c) {
	pthread_mutex_lock(&mu);
	*h = held;
	*r = released;
	*c = cancelled;
	pthread_mutex_unlock(&mu);
}
