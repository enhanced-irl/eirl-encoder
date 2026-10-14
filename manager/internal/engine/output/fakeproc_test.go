package output

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests run the output's processes for real: the test binary plays srt-live-transmit and srtla_send
// when it is started with fakeEnv set (TestMain), driven by environment variables.
const (
	fakeEnv      = "EIRL_OUTPUT_FAKE_DIR" // where the fakes leave what they saw
	fakeSLTMode  = "EIRL_OUTPUT_FAKE_SLT"
	fakeSRTLA    = "EIRL_OUTPUT_FAKE_SRTLA"
	fakeStatsEnv = "EIRL_OUTPUT_FAKE_STATS" // the JSON line the fake srt-live-transmit repeats
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeEnv); dir != "" && len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		fakeMain(dir, os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func fakeMain(dir string, args []string) {
	role := "srtla"
	if strings.HasPrefix(args[0], "-chunk") {
		role = "slt"
	}
	_ = os.WriteFile(filepath.Join(dir, role+".args"), []byte(strings.Join(args, "\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, role+".env"), []byte(strings.Join(os.Environ(), "\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, role+".pid"), []byte(fmt.Sprint(os.Getpid())), 0o644)
	if role == "slt" {
		fakeSLT(args)
	} else {
		fakeSRTLAMain(dir)
	}
}

func fakeSLT(args []string) {
	target := args[len(args)-1]
	var stats *os.File
	for _, a := range args {
		if a == "-statsout:/dev/fd/3" {
			stats = os.NewFile(3, "stats")
		}
	}
	say := func(s string) { fmt.Fprintln(os.Stderr, s) }
	say("Media path: 'file://con' --> '" + target + "'")
	switch os.Getenv(fakeSLTMode) {
	case "connect", "noread":
		say("SRT target connected")
		if stats != nil {
			line := os.Getenv(fakeStatsEnv)
			if line == "" {
				line = `{"link":{"rtt":40.4},"send":{"msBuf":45,"packetsDropped":0,"bytes":1316}}`
			}
			go func() {
				for {
					fmt.Fprintln(stats, line)
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
		if os.Getenv(fakeSLTMode) == "connect" {
			_, _ = io.Copy(io.Discard, os.Stdin)
		}
	case "reject":
		for i := 0; ; i++ {
			say(fmt.Sprintf("14:29:44.%06d/SRT:RcvQ:w%d!W:SRT.cn: @3200190%d: processConnectResponse: rejecting per "+
				"reception of a rejection HS response: ERROR:BADSECRET for %s", i, i%3, i%10, target))
			time.Sleep(10 * time.Millisecond)
		}
	case "exit":
		say("Connection setup failure: connection timed out (" + target + ")")
		os.Exit(1)
	}
	block() // silent: neither connected nor failing, as srt-live-transmit retrying without a relay
}

// block until killed (not select{}: the runtime would call it a deadlock)
func block() {
	for {
		time.Sleep(time.Hour)
	}
}

func fakeSRTLAMain(dir string) {
	hup := make(chan os.Signal, 8)
	signal.Notify(hup, syscall.SIGHUP)
	switch os.Getenv(fakeSRTLA) {
	case "fail":
		fmt.Println("192.168.1.232: connection failed, attempting to reconnect")
		fmt.Println("Failed to establish any initial connections to 203.0.113.20 (0x7f3a2c)")
		os.Exit(2)
	default:
		fmt.Println("192.168.1.232: connection established")
	}
	go func() {
		n := 0
		for range hup {
			n++
			_ = os.WriteFile(filepath.Join(dir, "srtla.hups"), []byte(fmt.Sprint(n)), 0o644)
		}
	}()
	block()
}
