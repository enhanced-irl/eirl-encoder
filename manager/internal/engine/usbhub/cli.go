package usbhub

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Socket is the engine's command socket (MIXER.md §5).
const Socket = "/run/eirl-engine/mixer.sock"

// Main is engine_usb.py's command line, for the engine's tools (`eirl-enginectl usb …`):
//
//	port <video node or sysfs device (1-2.1)>            → hub and port, and what else powers off
//	cycle [--force] <video node or sysfs device>         → cuts and gives back the power
//
// args are the arguments after the program (and subcommand) name. A device that has already dropped
// off the bus (the -71 case) has no video node: its sysfs name also works. `cycle` does not cut while
// the engine is emitting, or does not answer, unless --force: the cut powers off the whole hub.
// emitting is EngineEmitting(Socket) outside tests. Exit codes: 0 done, 1 cannot, 3 emitting, 64 usage.
func Main(args []string, stdout, stderr io.Writer, h *Hubs, emitting func() (on, known bool)) int {
	force := false
	var rest []string
	for _, a := range args {
		if a == "--force" {
			force = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 2 || (rest[0] != "port" && rest[0] != "cycle") {
		fmt.Fprintln(stderr, "usage: port|cycle [--force] <video node or sysfs device (1-2.1)>")
		return 64
	}
	byName := reDev.MatchString(rest[1])
	dev := rest[1]
	if !byName {
		dev = h.Sys.DeviceOf(rest[1])
	}
	plan := h.Plan(dev, byName)
	fmt.Fprintln(stdout, plan.Text())
	if plan.OK() && len(plan.Affected) > 0 {
		fmt.Fprintln(stdout, "also powered off: "+strings.Join(plan.Affected, " "))
	}
	if rest[0] == "port" || !plan.OK() {
		if plan.OK() {
			return 0
		}
		return 1
	}
	if !force {
		if on, known := emitting(); on || !known {
			fmt.Fprintln(stderr, "the engine is emitting (or does not answer): the cut powers off the whole hub. With --force, anyway")
			return 3
		}
	}
	done, errs := h.Sys.Cycle(plan, OffFor)
	for _, e := range errs {
		fmt.Fprintln(stderr, "error: "+e)
	}
	cut := strings.Join(done, "+")
	if cut == "" {
		cut = "none"
	}
	fmt.Fprintln(stdout, "power cut and given back on "+cut)
	if len(done) > 0 && len(errs) == 0 {
		return 0
	}
	return 1
}

// EngineEmitting asks the engine's socket whether it is emitting (`get`, state.output.mode == "on").
// known=false if it does not answer within 3 s or the answer lacks that field.
func EngineEmitting(sock string) func() (on, known bool) {
	return func() (bool, bool) {
		c, err := net.DialTimeout("unix", sock, 3*time.Second)
		if err != nil {
			return false, false
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.WriteString(c, `{"id":1,"cmd":"get"}`+"\n"); err != nil {
			return false, false
		}
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			return false, false
		}
		var reply struct {
			State *struct {
				Output *struct {
					Mode *string `json:"mode"`
				} `json:"output"`
			} `json:"state"`
		}
		if json.Unmarshal(line, &reply) != nil || reply.State == nil || reply.State.Output == nil || reply.State.Output.Mode == nil {
			return false, false
		}
		return *reply.State.Output.Mode == "on", true
	}
}
