//go:build !linux

package splash

import (
	"errors"
	"image"
)

var errUnsupported = errors.New("Linux only")

type FB struct{}

func Open(string) (*FB, error)                { return nil, errUnsupported }
func (*FB) Geometry() Geometry                { return Geometry{} }
func (*FB) Refresh() (bool, error)            { return false, errUnsupported }
func (*FB) Blit(*image.RGBA, image.Rectangle) {}
func (*FB) Show() error                       { return errUnsupported }
func (*FB) Close() error                      { return nil }
func MapConsole(string) error                 { return errUnsupported }
