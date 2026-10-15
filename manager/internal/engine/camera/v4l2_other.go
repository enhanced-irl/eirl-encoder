//go:build !linux

package camera

import "errors"

func open(string) (Device, error) { return nil, errors.New("only on Linux") }
