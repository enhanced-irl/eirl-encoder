// eirl-storage: the root helper that mounts the USB disk of local recordings (RECORDING.md §4).
//
//	eirl-storage attach <disk>   a USB disk appeared: check it, mount it, measure it (eirl-storage@<disk>.service)
//	eirl-storage detach <disk>   its unit stops (the disk pulled out): unmount, lazily if it is gone
//	eirl-storage eject           sync and unmount the disk so that it can be pulled out (the app, through sudo)
//	eirl-storage format          one partition with exFAT labelled EIRL-REC, then mounted again (the app, through sudo)
//	eirl-storage status          print the state file
//
// It says what it found in /run/eirl-storage/state.json; the engine and the app only read it. One disk at a time.
// eject and format, run from the app's sudo, start eirl-storage-eject.service / eirl-storage-format@<seq>.service, which run
// them in the system's mount namespace (the app's sandbox would hide an unmount or a mount from the engine), and return
// their exit status: 0 done, 1 failed, 2 no disk, 3 the engine records, 4 busy (a disk being checked), 64 usage.
// Log lines: `[storage] event=<code> key=value…` on stderr (the journal).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/storage"
)

var version = "dev"

const (
	runDir      = "/run/eirl-storage"
	mountPoint  = "/media/eirl-rec"
	engineState = "/run/eirl-engine/mixer.json"
	engineUser  = "eirl-engine"
	appGroup    = "eirl-encoder"
)

func usage() {
	fmt.Fprintf(os.Stderr, "eirl-storage %s\nusage: eirl-storage attach <disk> | detach <disk> | eject | format (seq on stdin) | status\n", version)
	os.Exit(storage.ExitUsage)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[storage] "+format+"\n", args...)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	wantArgs := map[string]int{"attach": 1, "detach": 1, "eject": 0, "format": 0, "status": 0}
	direct := os.Getenv("EIRL_STORAGE_DIRECT") == "1"
	if cmd == "format" && direct {
		wantArgs["format"] = 1 // its unit, eirl-storage-format@<seq>.service, passes the seq
	}
	n, ok := wantArgs[cmd]
	if !ok || len(args) != n {
		usage()
	}
	if cmd == "status" {
		st, err := storage.ReadState(runDir + "/state.json")
		if err != nil {
			logf("event=state_unreadable detail=%q", err.Error())
		}
		b, _ := json.Marshal(st)
		fmt.Println(string(b))
		return
	}
	if os.Geteuid() != 0 {
		logf("event=refused detail=%q", "run as root")
		os.Exit(storage.ExitFailed)
	}
	// systemd stops the unit with SIGTERM (the disk pulled out while it is checked): the commands are killed and attach
	// leaves nothing behind
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	h := &storage.Helper{
		SysDir: "/sys", ProcDir: "/proc", DevDir: "/dev", RunDir: runDir, MountPoint: mountPoint, EngineState: engineState,
		Runner: storage.ExecRunner{}, Log: logf, Ctx: ctx,
		Direct: direct,
		Unit:   "eirl-storage@%s.service",
	}
	if cmd == "attach" || ((cmd == "eject" || cmd == "format") && h.Direct) {
		uid, err1 := lookupID("/etc/passwd", engineUser)
		gid, err2 := lookupID("/etc/group", appGroup)
		if err1 != nil || err2 != nil {
			// Without them a FAT disk would be mounted for root only: nobody could record
			logf("event=refused detail=%q", "no user "+engineUser+" or group "+appGroup)
			os.Exit(storage.ExitFailed)
		}
		h.EngineUID, h.EncoderGID = uid, gid
	}
	var rc int
	switch cmd {
	case "attach":
		rc = h.Attach(args[0])
	case "detach":
		rc = h.Detach(args[0])
	case "eject":
		rc = h.Eject()
	case "format":
		// The seq of the disk the user confirmed: from the app on stdin (the sudoers rule stays one exact command), from
		// the unit as its argument
		s := ""
		if direct {
			s = args[0]
		} else {
			b, _ := io.ReadAll(io.LimitReader(os.Stdin, 32))
			s = strings.TrimSpace(string(b))
		}
		seq, ok := storage.ParseSeq(s)
		if !ok {
			logf("event=bad_request detail=%q", "format needs the seq of the disk confirmed")
			os.Exit(storage.ExitUsage)
		}
		rc = h.Format(seq)
	}
	os.Exit(rc)
}

// lookupID: the numeric id of a user (/etc/passwd) or group (/etc/group), third field.
func lookupID(file, name string) (int, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return -1, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 3 && f[0] == name {
			return strconv.Atoi(f[2])
		}
	}
	return -1, fmt.Errorf("%s not in %s", name, file)
}
