// Package sysx holds the engine's small system helpers: running a program with a deadline and writing
// a file atomically. Kept apart so every part of the engine does both the same way.
package sysx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Result is what a program said.
type Result struct {
	RC       int // 127 if it could not be started or timed out (like the Python engine's run)
	Out, Err string
}

// Runner runs programs; tests replace it.
type Runner func(timeout time.Duration, name string, args ...string) Result

// Run runs a program without a shell. stderr is trimmed; output is taken as UTF-8.
func Run(timeout time.Duration, name string, args ...string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	r := Result{Out: out.String(), Err: strings.TrimSpace(errb.String())}
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		r.RC, r.Err = 127, "timeout after "+timeout.String()
	case errors.As(err, &ee):
		r.RC = ee.ExitCode()
	case err != nil:
		r.RC, r.Err = 127, err.Error()
	}
	return r
}

// WriteAtomic writes path through path.tmp and a rename: whoever reads (the app) never sees half a
// file. mode is set explicitly (not left to the umask).
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ReadKV reads KEY=VALUE lines (the engine's run files: video, abr). Missing file: empty map.
func ReadKV(path string) map[string]string {
	b, err := os.ReadFile(path)
	m := map[string]string{}
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}

// Exists says whether path exists (following links).
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Glob is filepath.Glob without the error (bad patterns are programming errors).
func Glob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}
