package storage

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Runner runs a command. code is its exit status; err says it could not be run, timed out or died of a signal.
type Runner interface {
	Run(ctx context.Context, stdin string, name string, args ...string) (out string, code int, err error)
}

// ExecRunner runs commands for real: absolute paths, a fixed PATH and locale, output bounded.
type ExecRunner struct{}

// maxOutput: what is kept of a command's output (for the log).
const maxOutput = 64 << 10

type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// Run: the context bounds it. A FUSE mount helper leaves its daemon running with our pipes possibly still open: WaitDelay
// stops waiting for them once the command itself has exited, and that is not an error.
func (ExecRunner) Run(ctx context.Context, stdin string, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out capped
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		err = nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.Exited() && ctx.Err() == nil {
		return out.String(), ee.ExitCode(), nil
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return out.String(), -1, err
	}
	return out.String(), 0, nil
}
