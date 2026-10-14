//go:build !linux

package sources

import "errors"

type v4l2Enum struct{}

func (v4l2Enum) Formats(string) ([]Format, error) { return nil, errors.New("only on Linux") }
