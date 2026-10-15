package engine

import (
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/camera"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

// A USB camera's image settings (SOURCES.md §7): camera_control and camera_reset. The actor only checks a command
// against the controls last read and queues it; the camera is asked from the source's own goroutine (cameraLoop),
// because a request to a camera that stopped answering takes the driver's timeout. The reply comes from there,
// once the state already carries the new values.

// camQueue: commands waiting for one camera; past it, busy (a finger on − or + sends a few per second).
const camQueue = 8

// camJob is one command for a camera, and how to answer it (once, from any goroutine).
type camJob struct {
	reset   bool
	control string
	value   int
	msg     map[string]any // for the log
	reply   func(ok bool, code string)
}

// cameraCommand runs on the actor: unknown_source, unsupported (no controls now: not a USB camera, hidden, gone or
// without any of the table's), unknown_control, inactive and bad_request are answered here; the rest goes to the
// camera's goroutine, or busy.
func (e *Engine) cameraCommand(cmd string, msg map[string]any, reply func(ok bool, code string)) {
	id, _ := msg["source"].(string)
	var src *Source
	for _, s := range e.sources {
		if s != nil && s.id == id {
			src = s
			break
		}
	}
	if src == nil {
		reply(false, "unknown_source")
		return
	}
	list := src.cameraControls()
	if src.camJobs == nil || len(list) == 0 {
		reply(false, "unsupported")
		return
	}
	j := camJob{reset: cmd == camera.CmdReset, msg: msg, reply: reply}
	if !j.reset {
		ctl, ok := msg["control"].(string)
		v, isInt := camera.IntArg(msg["value"])
		if !ok || !isInt {
			reply(false, camera.CodeBadRequest)
			return
		}
		if _, _, code := camera.Check(list, ctl, v); code != "" {
			reply(false, code)
			return
		}
		j.control, j.value = ctl, v
	}
	select {
	case src.camJobs <- j:
	default:
		reply(false, "busy")
	}
}

func (s *Source) cameraControls() []camera.Control {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controls
}

func (s *Source) setControls(node string, list []camera.Control) {
	s.mu.Lock()
	s.camNode, s.controls = node, list
	s.mu.Unlock()
	s.e.changed()
}

// updateControls: what a command read back, unless the camera left the state meanwhile (hidden, gone, opened again).
func (s *Source) updateControls(node string, list []camera.Control) {
	s.mu.Lock()
	if s.camNode != node {
		s.mu.Unlock()
		return
	}
	s.controls = list
	s.mu.Unlock()
	s.e.changed()
}

// clearControls: the camera is gone or hidden; its settings leave the state until it opens again.
func (s *Source) clearControls() {
	s.mu.Lock()
	had := s.controls != nil
	s.mu.Unlock()
	if had {
		s.setControls("", nil)
	}
}

// openCamera puts the camera's saved settings back and reads its controls, every time the source opens it
// (SOURCES.md §7.4). A camera that does not answer is left without settings in the state; capturing goes on.
func (s *Source) openCamera(node, name string) {
	saved := s.e.cams.Values(s.id)
	s.camMu.Lock()
	var list []camera.Control
	d, err := camera.Open(node)
	if err == nil {
		list, err = camera.Apply(d, saved, func(id string, err error) {
			elog.Event("mixer", "camera_setting_refused", "source", name, "control", id, "err", err)
		})
		d.Close()
	}
	s.camMu.Unlock()
	if err != nil {
		elog.Event("mixer", "camera_unreadable", "source", name, "err", err)
	}
	s.setControls(node, list)
}

// cameraLoop runs the camera's commands one after the other, until the source stops; those still waiting then get
// unsupported.
func (s *Source) cameraLoop() {
	for {
		select {
		case <-s.stopCh:
			for {
				select {
				case j := <-s.camJobs:
					j.reply(false, "unsupported")
				default:
					return
				}
			}
		case j := <-s.camJobs:
			s.cameraJob(j)
		}
	}
}

func (s *Source) cameraJob(j camJob) {
	// The controls as the last command left them (each one updates them before the next starts): checked against
	// those, no full read of the camera before writing
	s.mu.Lock()
	node, name, list := s.camNode, s.name, s.controls
	s.mu.Unlock()
	if node == "" || list == nil {
		j.reply(false, "unsupported")
		return
	}
	var r camera.Result
	s.camMu.Lock()
	d, err := camera.Open(node)
	switch {
	case err != nil:
		r = camera.Result{Code: camera.CodeCameraError, Err: err}
	case j.reset:
		r = camera.Reset(d, list)
	default:
		r = camera.Set(d, list, j.control, j.value)
	}
	if d != nil {
		d.Close()
	}
	s.camMu.Unlock()
	if r.List != nil {
		s.updateControls(node, r.List)
	}
	if r.Code == camera.CodeCameraError {
		elog.Event("mixer", "camera_command_failed", "source", name, "control", j.control, "reset", j.reset, "err", r.Err)
	}
	switch {
	case j.reset:
		// Forgotten even if the camera refused some default: the user asked for the camera as it came, and what was
		// saved would come back at the next opening
		s.e.cams.Forget(s.id)
	case r.Code == "":
		s.e.cams.Set(s.id, r.Control, r.Value)
	}
	if r.Code == "" {
		s.e.logCommand(j.msg)
	}
	// On the actor: the state with the new values is published before whoever sent it gets its answer
	s.e.post(func() {
		s.e.publish()
		j.reply(r.Code == "", r.Code)
	})
}
