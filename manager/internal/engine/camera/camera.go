// Package camera reads and changes a USB camera's image settings (SOURCES.md §7): the standard UVC controls
// that uvcvideo turns into V4L2 ones (brightness, exposure, white balance, focus…), a closed list of them. The
// vendors' extension units are never touched: some are firmware or factory controls. Store keeps the values each
// camera was given, so the engine puts them back every time it opens it.
//
// Every call on a Device but Close may be a USB request to the camera, which takes the driver's timeout when the
// camera stops answering: the engine makes them from the source's goroutines, never from its actor.
package camera

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"
)

// Commands of the socket (MIXER.md §5, SOURCES.md §7.3).
const (
	CmdControl = "camera_control"
	CmdReset   = "camera_reset"
)

// IsCommand: one of the camera's commands.
func IsCommand(cmd string) bool { return cmd == CmdControl || cmd == CmdReset }

// Error codes of the commands (besides the engine's unknown_source, unsupported and busy).
const (
	CodeUnknownControl = "unknown_control"
	CodeInactive       = "inactive"
	CodeBadRequest     = "bad_request"
	CodeCameraError    = "camera_error"
)

// Types of a control in the state.
const (
	TypeInt  = "int"
	TypeBool = "bool"
	TypeMenu = "menu"
)

// Control is one setting as the state carries it (SOURCES.md §7.3). Value and Default are integers, 0 or 1 for
// a bool; Inactive: its automatic is on and the camera ignores a manual value now.
type Control struct {
	ID       string `json:"id"`
	Group    string `json:"group"`
	Type     string `json:"type"`
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	Step     int    `json:"step"`
	Default  int    `json:"default"`
	Value    int    `json:"value"`
	Inactive bool   `json:"inactive,omitempty"`
	Options  []int  `json:"options,omitempty"`

	cid  uint32
	auto int32 // exposure_auto: the menu value of the camera's automatic mode
}

// spec is a control of the table: its id, its group, its V4L2 id and the automatic that makes it inactive.
type spec struct {
	id, group string
	cid       uint32
	master    string
}

// specs is the whitelist, in the order the state carries it (SOURCES.md §7.2).
var specs = []spec{
	{"brightness", "image", 0x980900, ""},
	{"contrast", "image", 0x980901, ""},
	{"saturation", "image", 0x980902, ""},
	{"hue", "image", 0x980903, ""},
	{"gamma", "image", 0x980910, ""},
	{"sharpness", "image", 0x98091b, ""},
	{"exposure_auto", "exposure", cidExposureAuto, ""},
	{"exposure", "exposure", 0x9a0902, "exposure_auto"},
	{"gain", "exposure", 0x980913, ""},
	{"backlight", "exposure", 0x98091c, ""},
	{"power_line", "exposure", 0x980918, ""},
	{"wb_auto", "white_balance", 0x98090c, ""},
	{"wb_temperature", "white_balance", 0x98091a, "wb_auto"},
	{"focus_auto", "framing", 0x9a090c, ""},
	{"focus", "framing", 0x9a090a, "focus_auto"},
	{"zoom", "framing", 0x9a090d, ""},
	{"pan", "framing", 0x9a0908, ""},
	{"tilt", "framing", 0x9a0909, ""},
}

// Known: the control ids of the table.
func Known(id string) bool { return specOf(id) != nil }

// IDs are the table's control ids, in its order. The app's whitelist (mixer.ControlIDs) is checked against them.
func IDs() []string {
	out := make([]string, len(specs))
	for i, sp := range specs {
		out[i] = sp.id
	}
	return out
}

// Master is the automatic whose being on makes a control inactive ("" none): the screen and the simulator use it.
func Master(id string) string {
	if sp := specOf(id); sp != nil {
		return sp.master
	}
	return ""
}

func specOf(id string) *spec {
	for i := range specs {
		if specs[i].id == id {
			return &specs[i]
		}
	}
	return nil
}

// isAuto: an automatic, which other controls depend on (written before them).
func isAuto(id string) bool {
	for _, sp := range specs {
		if sp.master == id {
			return true
		}
	}
	return false
}

// V4L2 (linux/videodev2.h).
const (
	ctrlTypeInteger = 1
	ctrlTypeBoolean = 2
	ctrlTypeMenu    = 3

	flagDisabled  = 0x0001
	flagReadOnly  = 0x0004
	flagInactive  = 0x0010
	flagWriteOnly = 0x0040

	cidExposureAuto         = 0x9a0901
	cidExposureAutoPriority = 0x9a0903

	exposureManual = 1 // V4L2_EXPOSURE_MANUAL; 0 auto, 2 shutter priority, 3 aperture priority

	maxOptions = 8  // menu entries reported
	maxMenu    = 16 // menu indices asked about
	maxFails   = 2  // requests that fail before the camera is taken as not answering
)

// listBudget bounds a List: past it the camera is taken as not answering (a variable for the tests). Below the
// socket's 2 s: a command's answer is not left behind a camera that hardly answers.
var listBudget = 1500 * time.Millisecond

// Info is what VIDIOC_QUERYCTRL says of a control.
type Info struct {
	Type                    uint32
	Min, Max, Step, Default int32
	Flags                   uint32
}

// Device is an open camera node.
type Device interface {
	// Query is VIDIOC_QUERYCTRL: ErrAbsent when the camera does not have the control
	Query(cid uint32) (Info, error)
	// HasMenu is VIDIOC_QUERYMENU: whether the camera has that entry of a menu
	HasMenu(cid uint32, index int32) bool
	Get(cid uint32) (int32, error)
	Set(cid uint32, v int32) error
	Close() error
}

var (
	// ErrAbsent: the camera does not have the control.
	ErrAbsent = errors.New("camera: no such control")
	// ErrGone: the camera is not there any more.
	ErrGone = errors.New("camera: gone")
	// ErrNotAnswering: requests keep failing, or a List took too long.
	ErrNotAnswering = errors.New("camera: not answering")
)

// Open opens a camera's node (a variable for the tests).
var Open = open

// reader reads the table's controls from one camera, giving up once it stops answering.
type reader struct {
	d     Device
	fails int
	until time.Time
	last  error
}

func (r *reader) failed(err error) error {
	r.fails++
	r.last = err
	if errors.Is(err, ErrGone) || r.fails >= maxFails || time.Now().After(r.until) {
		return errors.Join(ErrNotAnswering, err)
	}
	return nil
}

// List reads the controls of the table the camera has, in its order, with their values. Read-only, disabled and
// write-only ones are left out, and so is one whose value cannot be read. An error: the camera stopped answering.
func List(d Device) ([]Control, error) {
	r := &reader{d: d, until: time.Now().Add(listBudget)}
	var out []Control
	for _, sp := range specs {
		if time.Now().After(r.until) {
			return nil, ErrNotAnswering
		}
		c, ok, err := r.read(sp)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, c)
		}
	}
	markInactive(out)
	return out, nil
}

func (r *reader) read(sp spec) (Control, bool, error) {
	d := r.d
	info, err := d.Query(sp.cid)
	if errors.Is(err, ErrAbsent) {
		return Control{}, false, nil
	}
	if err != nil {
		return Control{}, false, r.failed(err)
	}
	if info.Flags&(flagDisabled|flagReadOnly|flagWriteOnly) != 0 {
		return Control{}, false, nil
	}
	c := Control{ID: sp.id, Group: sp.group, cid: sp.cid, Step: 1}
	switch info.Type {
	case ctrlTypeInteger:
		if info.Min > info.Max {
			return Control{}, false, nil
		}
		c.Type, c.Min, c.Max = TypeInt, int(info.Min), int(info.Max)
		c.Step = max(1, int(info.Step))
		if c.Min == 0 && c.Max == 1 {
			c.Type = TypeBool
		}
		c.Default = clamp(int(info.Default), c.Min, c.Max)
	case ctrlTypeBoolean:
		c.Type, c.Min, c.Max, c.Default = TypeBool, 0, 1, b01(info.Default != 0)
	case ctrlTypeMenu:
		if sp.cid == cidExposureAuto {
			// A switch: the camera's automatic (aperture priority, else auto, else shutter priority) or manual
			auto := int32(-1)
			for _, m := range []int32{3, 0, 2} {
				if m >= info.Min && m <= info.Max && d.HasMenu(sp.cid, m) {
					auto = m
					break
				}
			}
			if auto < 0 || !d.HasMenu(sp.cid, exposureManual) {
				return Control{}, false, nil
			}
			c.Type, c.Min, c.Max, c.auto = TypeBool, 0, 1, auto
			c.Default = b01(info.Default != exposureManual)
			break
		}
		for i := info.Min; i <= info.Max && i-info.Min < maxMenu && len(c.Options) < maxOptions; i++ {
			if d.HasMenu(sp.cid, i) {
				c.Options = append(c.Options, int(i))
			}
		}
		if len(c.Options) == 0 {
			return Control{}, false, nil
		}
		c.Type, c.Min, c.Max, c.Default = TypeMenu, int(info.Min), int(info.Max), int(info.Default)
	default:
		return Control{}, false, nil
	}
	v, err := d.Get(sp.cid)
	if err != nil {
		if ferr := r.failed(err); ferr != nil {
			return Control{}, false, ferr
		}
		return Control{}, false, nil
	}
	c.Value = c.fromRaw(v)
	c.Inactive = info.Flags&flagInactive != 0
	return c, true, nil
}

// markInactive: a manual value whose automatic is on, also if the driver does not flag it.
func markInactive(list []Control) {
	for i := range list {
		if sp := specOf(list[i].ID); sp != nil && sp.master != "" {
			if m := find(list, sp.master); m != nil && m.Value == 1 {
				list[i].Inactive = true
			}
		}
	}
}

func find(list []Control, id string) *Control {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// fromRaw is the camera's value as the state shows it.
func (c Control) fromRaw(v int32) int {
	switch {
	case c.cid == cidExposureAuto:
		return b01(v != exposureManual)
	case c.Type == TypeBool:
		return b01(v != 0)
	case c.Type == TypeMenu:
		return int(v)
	}
	return clamp(int(v), c.Min, c.Max)
}

// raw is a value as the camera takes it.
func (c Control) raw(v int) int32 {
	if c.cid == cidExposureAuto {
		if v == 1 {
			return c.auto
		}
		return exposureManual
	}
	return int32(v)
}

// normalize brings a value to what the control takes: an int inside min…max and on its step; a bool 0 or 1 and
// a menu one of its options, else bad_request.
func (c Control) normalize(v int) (int, string) {
	switch c.Type {
	case TypeBool:
		if v != 0 && v != 1 {
			return 0, CodeBadRequest
		}
		return v, ""
	case TypeMenu:
		if !slices.Contains(c.Options, v) {
			return 0, CodeBadRequest
		}
		return v, ""
	}
	step := max(1, c.Step)
	v = clamp(v, c.Min, c.Max)
	v = c.Min + int(math.Round(float64(v-c.Min)/float64(step)))*step
	if v > c.Max {
		v -= step
	}
	return v, ""
}

// Check is a command's value for a control of list: the control and the value as it will be set, or an error code
// (unknown_control, inactive, bad_request).
func Check(list []Control, id string, value int) (Control, int, string) {
	c := find(list, id)
	if c == nil {
		return Control{}, 0, CodeUnknownControl
	}
	if c.Inactive {
		return *c, 0, CodeInactive
	}
	v, code := c.normalize(value)
	return *c, v, code
}

// Result is what a command did to the camera.
type Result struct {
	List    []Control // the camera's controls afterwards (nil if they could not be read)
	Control Control   // Set: the control
	Value   int       // Set: its value as the state shows it
	Code    string    // "" or an error code (SOURCES.md §7.3)
	Err     error     // what the camera said, behind camera_error
}

// Set changes one control of list (the camera's controls as last read: the commands of one camera run one after
// the other, each leaving them read): it writes the value only if it differs and then reads them again (an
// automatic turns its manual control on or off).
func Set(d Device, list []Control, id string, value int) Result {
	c, v, code := Check(list, id, value)
	r := Result{List: list, Control: c, Value: v, Code: code}
	if code != "" || v == c.Value {
		return r
	}
	if err := d.Set(c.cid, c.raw(v)); err != nil {
		r.Code, r.Err = CodeCameraError, err
	}
	if list, err := List(d); err == nil {
		r.List = list
	} else if r.Code == "" {
		r.Code, r.Err = CodeCameraError, err
	}
	return r
}

// Reset puts every control of list (the camera's as last read) back to its default: the automatics first (a manual
// value is ignored while its automatic is on), then the rest. A control the camera refuses is skipped and reported
// as camera_error.
func Reset(d Device, list []Control) Result {
	r := Result{List: list}
	var err error
	set := func(c Control, v int) {
		if err := d.Set(c.cid, c.raw(v)); err != nil {
			r.Code, r.Err = CodeCameraError, err
		}
	}
	for _, c := range list {
		if isAuto(c.ID) && c.Value != c.Default {
			set(c, c.Default)
		}
	}
	if list, err = List(d); err != nil {
		return Result{Code: CodeCameraError, Err: err}
	}
	for _, c := range list {
		if !isAuto(c.ID) && !c.Inactive && c.Value != c.Default {
			set(c, c.Default)
		}
	}
	if list, err = List(d); err != nil {
		return Result{Code: CodeCameraError, Err: err}
	}
	r.List = list
	return r
}

// Apply prepares a camera that has just been opened (SOURCES.md §7.4): exposure_auto_priority off (with little
// light the automatic exposure would lower the fps), then the saved values, the automatics first; a manual value
// whose automatic is on is not written. A saved value is brought inside the camera's range; a control the camera
// does not have is ignored. failed is told of each value the camera refuses. It returns the camera's controls; an
// error: the camera stopped answering.
func Apply(d Device, saved map[string]int, failed func(id string, err error)) ([]Control, error) {
	if info, err := d.Query(cidExposureAutoPriority); err == nil && info.Flags&(flagDisabled|flagReadOnly) == 0 {
		if err := d.Set(cidExposureAutoPriority, 0); err != nil {
			failed("exposure_auto_priority", err)
		}
	}
	list, err := List(d)
	if err != nil || len(saved) == 0 {
		return list, err
	}
	write := func(autos bool) bool {
		wrote := false
		for _, sp := range specs {
			want, ok := saved[sp.id]
			if !ok || isAuto(sp.id) != autos {
				continue
			}
			c, v, code := Check(list, sp.id, want)
			if code != "" || v == c.Value {
				continue
			}
			if err := d.Set(c.cid, c.raw(v)); err != nil {
				failed(sp.id, err)
				continue
			}
			wrote = true
		}
		return wrote
	}
	if write(true) {
		if list, err = List(d); err != nil {
			return nil, err
		}
	}
	if write(false) {
		return List(d)
	}
	return list, nil
}

// IntArg is a command's integer value: a JSON number without a fraction (json.Number from the socket, float64) or
// a Go integer (the screen), inside the int32 range. Booleans, strings and anything else are not.
func IntArg(v any) (int, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			f = float64(n)
		} else {
			return 0, false
		}
	case float64:
		f = x
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case int32:
		f = float64(x)
	default:
		return 0, false
	}
	if f != math.Trunc(f) || f < math.MinInt32 || f > math.MaxInt32 {
		return 0, false
	}
	return int(f), true
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }

func b01(on bool) int {
	if on {
		return 1
	}
	return 0
}
