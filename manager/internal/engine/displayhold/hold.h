// The engine's own ioctl, which keeps the board's HDMI output lit (hold.c). Built only on linux with cgo.
#ifndef EIRL_DISPLAYHOLD_H
#define EIRL_DISPLAYHOLD_H

// 1 if the process resolves ioctl to the engine's (the executable exports it), 0 if libc's wins.
int eirl_hold_active(void);

// Counters since the start: blanks held back, let through just before a mode set, cancelled by an unblank.
void eirl_hold_counts(unsigned long *held, unsigned long *released, unsigned long *cancelled);

#endif
