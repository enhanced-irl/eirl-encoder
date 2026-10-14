//go:build !linux

package hdmi

import "github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"

func queryIoctl(string) (pipeline.Timings, bool, error) {
	return pipeline.Timings{}, false, ErrNotImplemented
}
