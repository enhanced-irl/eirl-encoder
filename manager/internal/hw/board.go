package hw

import (
	"strconv"
	"sync"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/tcchip"
)

// Board groups the device's readings: the TC358743 (v4l2 subdev + I2C) and sysfs. The chip is the one
// wired to the board's HDMI audio (tcchip.Wired): on CAM0 or CAM1, found in sysfs, not fixed.
type Board struct {
	Chip   string // subdev name: tc358743 (unused: the chips come from tcchip)
	I2CBus string // the chip's I2C bus, "/dev/i2c-N"; "" = the wired chip's (tcchip)
	Addr   uint16 // 0x0f
	// Wired is HDMI_WIRED (cam0 | cam1 | "" = auto); nil = auto
	Wired func() string
	// Root is the filesystem root for sysfs ("" = /; tests point it at a temporary tree)
	Root string

	mu   sync.Mutex
	chip *tcchip.Chip
	sys  Sysfs
}

// wired is the chip the board reads, found once (the CSI connectors are not hot-pluggable).
func (b *Board) wired() (tcchip.Chip, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.chip == nil {
		w := ""
		if b.Wired != nil {
			w = b.Wired()
		}
		c, ok := tcchip.Wired(tcchip.Find(b.Root), w)
		if !ok {
			return tcchip.Chip{}, false
		}
		b.chip = &c
	}
	return *b.chip, true
}

// Signal: no signal (or no subdev) → Present=false, no error.
func (b *Board) Signal() Signal {
	c, ok := b.wired()
	if !ok {
		return Signal{}
	}
	s, err := QueryDVTimings(c.Subdev)
	if err != nil {
		return Signal{}
	}
	return s
}

func (b *Board) Audio() Audio {
	bus := b.I2CBus
	if bus == "" {
		c, ok := b.wired()
		if !ok {
			return Audio{State: "unknown"}
		}
		bus = "/dev/i2c-" + strconv.Itoa(c.Bus)
	}
	v, err := ReadReg8(bus, b.Addr, 0x8523) // AU_STATUS0
	if err != nil {
		return Audio{State: "unknown"}
	}
	return AudioFromStatus(v)
}

func (b *Board) System() System { return b.sys.Read() }
