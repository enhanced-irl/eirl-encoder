package hw

import "sync"

// Board groups the device's readings: the TC358743 (v4l2 subdev + I2C) and sysfs.
type Board struct {
	Chip   string // subdev name: tc358743
	I2CBus string // /dev/i2c-8 (CAM1)
	Addr   uint16 // 0x0f

	mu     sync.Mutex
	subdev string
	sys    Sysfs
}

// Signal: no signal (or no subdev) → Present=false, no error.
func (b *Board) Signal() Signal {
	b.mu.Lock()
	if b.subdev == "" {
		b.subdev, _ = FindSubdev(b.Chip)
	}
	sd := b.subdev
	b.mu.Unlock()
	if sd == "" {
		return Signal{}
	}
	s, err := QueryDVTimings(sd)
	if err != nil {
		return Signal{}
	}
	return s
}

func (b *Board) Audio() Audio {
	v, err := ReadReg8(b.I2CBus, b.Addr, 0x8523) // AU_STATUS0
	if err != nil {
		return Audio{State: "unknown"}
	}
	return AudioFromStatus(v)
}

func (b *Board) System() System { return b.sys.Read() }
