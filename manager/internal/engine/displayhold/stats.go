package displayhold

// Stats: whether the display libraries reach the engine's ioctl, and how many blanks of the HDMI output it
// has held back, let through just before a mode set, and seen cancelled by an unblank.
type Stats struct {
	Active                    bool
	Held, Released, Cancelled uint64
}
