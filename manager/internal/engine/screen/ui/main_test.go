package ui

import (
	"io"
	"os"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

func TestMain(m *testing.M) {
	elog.SetOutput(io.Discard)
	os.Exit(m.Run())
}
