package usbhub

import "unsafe"

// ctrlTransfer is struct usbdevfs_ctrltransfer (linux/usbdevice_fs.h): 8 bytes of setup, the timeout
// in ms, padding and the data pointer. 24 bytes on arm64.
type ctrlTransfer struct {
	ReqType uint8
	Req     uint8
	Value   uint16
	Index   uint16
	Length  uint16
	Timeout uint32
	Data    unsafe.Pointer
}

// usbdevfsControl = _IOWR('U', 0, struct usbdevfs_ctrltransfer): 0xC0185500 on arm64.
const usbdevfsControl = (3 << 30) | (uintptr(unsafe.Sizeof(ctrlTransfer{})) << 16) | ('U' << 8)

// controlTimeoutMs is each transfer's timeout.
const controlTimeoutMs = 1000
