package record

import "golang.org/x/sys/unix"

// clockSynced: the kernel's clock is synchronised (NTP clears STA_UNSYNC once it set the time). A board
// recording without internet keeps the time it saved at shutdown: the date would lie.
func clockSynced() bool {
	var t unix.Timex
	st, err := unix.Adjtimex(&t)
	return err == nil && st != unix.TIME_ERROR && t.Status&unix.STA_UNSYNC == 0
}
