package output

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// The output's child processes: srt-live-transmit and srtla_send. They live with the session, and stop
// with TERM, then KILL after termGrace (engine_output.py: terminate, wait(2), kill).

var termGrace = 2 * time.Second

// childEnvDrop are environment variables a child never inherits: the launcher of the Python engine
// passed the target and the output pipeline this way, and a drop-in may still do it.
var childEnvDrop = []string{"EIRL_ENGINE_SRT_TARGET=", "EIRL_ENGINE_OUTPUT_PIPELINE="}

// child is one running process.
type child struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	code int // after done: the exit code, or minus the signal (Python's returncode)
}

// startChild starts bin with args. stdin may be nil (/dev/null); out receives stdout and stderr when
// withStdout, stderr only otherwise (the read end is returned, for a reader goroutine); extra are the
// child's fds 3, 4…; env is added to the inherited environment (a secret for the child: eirl-send's key).
//
// The arguments may hold the SRT target (srt-live-transmit has no other way to take it, as with the
// Python engine): they never go to a log or an error. Errors name the program only.
func startChild(name, bin string, args []string, stdin *os.File, withStdout bool, extra []*os.File, env []string) (*child, *os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = stdin
	cmd.Stderr = w
	if withStdout {
		cmd.Stdout = w
	}
	cmd.ExtraFiles = extra
	cmd.Env = append(childEnv(os.Environ()), env...)
	// Python's subprocess closes every other descriptor (close_fds): Go only spares those it opened
	// itself, so GStreamer's (a capture device, a socket) would leak into the child
	cloexecAll()
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, nil, &startError{name: name, err: err}
	}
	w.Close()
	c := &child{name: name, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		c.code = exitCode(cmd.ProcessState)
		close(c.done)
	}()
	return c, r, nil
}

// startError says which program did not start, without its arguments.
type startError struct {
	name string
	err  error
}

func (e *startError) Error() string {
	var ee *exec.Error
	if errors.As(e.err, &ee) {
		return e.name + ": " + ee.Err.Error()
	}
	var pe *os.PathError
	if errors.As(e.err, &pe) {
		return e.name + ": " + pe.Err.Error()
	}
	return e.name + ": cannot start"
}

func childEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		drop := false
		for _, p := range childEnvDrop {
			if strings.HasPrefix(kv, p) {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return ps.ExitCode()
}

func (c *child) Done() <-chan struct{} { return c.done }

func (c *child) exited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *child) Hangup() error {
	if c.exited() {
		return os.ErrProcessDone
	}
	return c.cmd.Process.Signal(syscall.SIGHUP)
}

func (c *child) Terminate() {
	if !c.exited() {
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
	}
}

// stopChildren terminates them all at once, gives them termGrace together and kills the rest.
func stopChildren(cs []*child) {
	for _, c := range cs {
		c.Terminate()
	}
	if !waitChildren(cs, termGrace) {
		for _, c := range cs {
			if !c.exited() {
				elog.Event("engine", "output_process_killed", "process", c.name, "pid", c.cmd.Process.Pid)
				_ = c.cmd.Process.Kill()
			}
		}
		// A process stuck in the kernel: do not wait for ever
		waitChildren(cs, time.Second)
	}
}

// waitChildren waits up to d for all of them; false if one is still alive.
func waitChildren(cs []*child, d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for _, c := range cs {
		select {
		case <-c.done:
		case <-deadline.C:
			return false
		}
	}
	return true
}

// cloexecAll marks every descriptor of the process above stderr close-on-exec, so that a child only
// gets what it is given (stdin, stdout, stderr and ExtraFiles, which exec duplicates). Harmless for the
// engine itself: it never execs anything that should inherit a descriptor.
func cloexecAll() {
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if fd, err := strconv.Atoi(e.Name()); err == nil && fd > 2 {
				syscall.CloseOnExec(fd)
			}
		}
		return
	}
}

var sigpipeOnce sync.Once

// survivePipes makes a write to a pipe without a reader fail with EPIPE instead of killing the process.
// The output's filesink writes to the FIFO from a GStreamer thread: when srt-live-transmit dies, the
// SIGPIPE lands on a thread Go does not know, and without a handler for it the runtime re-raises it
// with the default action (Python ignores SIGPIPE from the start). Notify, not Ignore: an ignored
// signal would stay ignored in the children too.
func survivePipes() {
	sigpipeOnce.Do(func() { signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE) })
}
