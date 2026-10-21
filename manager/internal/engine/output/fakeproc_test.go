package output

import (
	"bufio"
	"fmt"
	"io"
	"net"
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
	fakeSend     = "EIRL_OUTPUT_FAKE_SEND"  // the fake eirl-send: publish (default) | reject | tls
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
	switch {
	case strings.HasPrefix(args[0], "-chunk"):
		role = "slt"
	case args[0] == "rtmp":
		role = "send"
	case args[0] == "-i":
		role = "rist"
	}
	_ = os.WriteFile(filepath.Join(dir, role+".args"), []byte(strings.Join(args, "\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, role+".env"), []byte(strings.Join(os.Environ(), "\n")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, role+".pid"), []byte(fmt.Sprint(os.Getpid())), 0o644)
	_ = os.WriteFile(filepath.Join(dir, role+".started"), []byte(fmt.Sprint(time.Now().UnixNano())), 0o644)
	switch role {
	case "slt":
		fakeSLT(args)
	case "send":
		fakeSendMain(args)
	case "rist":
		fakeRISTMain()
	default:
		fakeSRTLAMain(dir, args)
	}
}

// fakeRISTMain plays ristsender with a real capture (testdata/rist/normal.log): its log and statistics on
// stderr, again and again.
func fakeRISTMain() {
	b, _ := os.ReadFile(os.Getenv("EIRL_OUTPUT_FAKE_RIST_LOG"))
	for {
		for _, l := range strings.Split(string(b), "\n") {
			if l != "" {
				fmt.Fprintln(os.Stderr, l)
			}
			if strings.Contains(l, "sender-stats") {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
}

// fakeSendMain plays eirl-send (OUTPUTS.md §5): its events on stderr, its statistics on fd 3.
func fakeSendMain(args []string) {
	say := func(s string) { fmt.Fprintln(os.Stderr, s) }
	say("event=connecting host=127.0.0.1 port=1935 tls=false bind=")
	switch os.Getenv(fakeSend) {
	case "reject":
		say(`event=failed reason=rejected detail="NetStream.Publish.BadName"`)
		os.Exit(1)
	case "tls":
		say(`event=failed reason=tls_failed detail="x509: certificate signed by unknown authority"`)
		os.Exit(1)
	}
	say("event=connected")
	say("event=publishing codec=h265")
	for i, a := range args {
		if a == "-stats-fd" && i+1 < len(args) && args[i+1] == "3" {
			stats := os.NewFile(3, "stats")
			go func() {
				for {
					fmt.Fprintln(stats, `{"link":{"rtt":33},"send":{"bytes":40000,"msBuf":120,"packetsDropped":0,"mbitRate":1.2}}`)
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	say("event=eof")
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

func fakeSRTLAMain(dir string, args []string) {
	hup := make(chan os.Signal, 8)
	signal.Notify(hup, syscall.SIGHUP)
	sock := ""
	for i, a := range args {
		if a == "--control-socket" && i+1 < len(args) {
			sock = args[i+1]
		}
	}
	switch os.Getenv(fakeSRTLA) {
	case "fail":
		fmt.Println("192.168.1.232: connection failed, attempting to reconnect")
		fmt.Println("Failed to establish any initial connections to 203.0.113.20 (0x7f3a2c)")
		os.Exit(2)
	case "fail-rs":
		// irlserver's sender: tracing's format, with colours (a NO_COLOR it ignored) (SRTLA.md §9.1)
		fmt.Println("\x1b[2m2026-10-19T18:00:01.000001Z\x1b[0m \x1b[33m WARN\x1b[0m 203.0.113.20:5000 via 192.168.1.232 timed out; attempting full socket reconnection")
		fmt.Println("\x1b[2m2026-10-19T18:00:11.000001Z\x1b[0m \x1b[31mERROR\x1b[0m Failed to establish any initial connections")
		os.Exit(1)
	case "dead-rs":
		// irlserver's sender after losing every uplink: a warning per packet and, every second, the line it never
		// exits after (SRTLA.md §9.1)
		fmt.Println("192.168.1.232: connection established")
		go func() {
			for i := 0; ; i++ {
				for j := 0; j < 20; j++ {
					fmt.Printf(" WARN no available connection to forward packet from 127.0.0.1:%d\n", 40000+j)
				}
				if i%5 == 0 {
					fmt.Println("ERROR Failed to re-establish any connections")
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
	case "late":
		// It registers 300 ms after starting: srt-live-transmit must not start before (SRTLA.md §9.1)
		time.Sleep(300 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, "srtla.registered"), []byte(fmt.Sprint(time.Now().UnixNano())), 0o644)
		fmt.Println("192.168.1.232: connection established")
	case "silent", "unregistered-rs":
		// It never says it registered (unregistered-rs: its statistics say so too, as irlserver's with a relay
		// that does not answer)
	default:
		fmt.Println("192.168.1.232: connection established")
	}
	if sock != "" {
		go fakeControlSocket(sock, os.Getenv(fakeSRTLA) != "unregistered-rs")
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

// fakeControlSocket plays the Rust sender's JSON-RPC socket: after a subscribe to stats, a StatsSnapshot every
// 50 ms with two links, the first one active and taking a NAK each time, the second one stall-gated.
func fakeControlSocket(path string, registered bool) {
	time.Sleep(100 * time.Millisecond) // it appears a moment after the process starts
	ln, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil || !strings.Contains(line, `"subscribe"`) {
				return
			}
			fmt.Fprintln(c, `{"jsonrpc":"2.0","id":1,"result":{"subscription_id":"sub-0"}}`)
			for n := 0; ; n++ {
				if !registered {
					_, err := fmt.Fprintln(c, `{"jsonrpc":"2.0","method":"stats.update","params":{"subscription_id":"sub-0","data":`+
						`{"mode":"classic","links":[{"ip":"192.168.1.232","connected":false,"timed_out":false}]}}}`)
					if err != nil {
						return
					}
					time.Sleep(50 * time.Millisecond)
					continue
				}
				_, err := fmt.Fprintf(c, `{"jsonrpc":"2.0","method":"stats.update","params":{"subscription_id":"sub-0","data":`+
					`{"mode":"classic","links":[`+
					`{"ip":"192.168.1.232","label":"x","connected":true,"timed_out":false,"window":20000,"in_flight":12,"rtt_ms":57,`+
					`"nak_count":%d,"bitrate_bytes_per_sec":675000,"weak":false,"stall_gated":false,"future_field":1},`+
					`{"ip":"10.64.1.2","connected":true,"timed_out":false,"rtt_ms":340,"nak_count":0,`+
					`"bitrate_bytes_per_sec":0,"weak":false,"stall_gated":true},`+
					`{"ip":"10.9.9.9","connected":true,"rtt_ms":1}]}}}`+"\n", n)
				if err != nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}(c)
	}
}
