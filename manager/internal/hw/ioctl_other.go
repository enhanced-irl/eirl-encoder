//go:build !linux

package hw

import "errors"

var errUnsupported = errors.New("Linux only")

func QueryDVTimings(string) (Signal, error)         { return Signal{}, errUnsupported }
func ReadReg8(string, uint16, uint16) (byte, error) { return 0, errUnsupported }
