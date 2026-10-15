// eirl-send: the encoder's own senders, started by the engine's output session (OUTPUTS.md §4-5).
//
//	eirl-send rtmp -url rtmp[s]://host[:port]/app [-bind <ipv4>] [-stats-fd 3] [-stats-ms 250]
//	               [-max-queue-ms 3000] [-connect-timeout 10s] [-ca-file <pem>]
//
// It reads MPEG-TS on stdin and publishes it; the stream key comes in EIRL_SEND_KEY (never in argv).
// stderr: one `event=…` line per event; exit 0 at the end of the input, 1 on failure.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/rtmpsend"
)

var version = "dev" // -ldflags "-X main.version=…"

const usage = `usage: eirl-send rtmp -url rtmp[s]://host[:port]/app [-bind <ipv4>] [-stats-fd N] [-stats-ms N]
                     [-max-queue-ms N] [-connect-timeout D] [-ca-file PEM]
env EIRL_SEND_KEY   the stream key (may be empty)
stdin               MPEG-TS (H.264 or H.265, AAC)
`

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "rtmp" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet("eirl-send rtmp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := rtmpsend.Config{Key: os.Getenv("EIRL_SEND_KEY")}
	fs.StringVar(&cfg.URL, "url", "", "rtmp[s]://host[:port]/app")
	fs.StringVar(&cfg.Bind, "bind", "", "IPv4 source address")
	statsFD := fs.Int("stats-fd", 0, "file descriptor for the stats lines (0: none)")
	statsMs := fs.Int("stats-ms", 250, "stats interval, ms")
	maxQueueMs := fs.Int("max-queue-ms", 3000, "media queued beyond this is dropped up to the next keyframe, ms")
	fs.DurationVar(&cfg.ConnectTimeout, "connect-timeout", 10*time.Second, "time to connect and publish")
	fs.StringVar(&cfg.CAFile, "ca-file", "", "an extra CA certificate, PEM (tests)")
	err := fs.Parse(os.Args[2:])
	switch {
	case err != nil:
	case cfg.URL == "":
		err = fmt.Errorf("-url is required")
	case fs.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case *statsMs < 20 || *maxQueueMs < 100 || cfg.ConnectTimeout <= 0:
		err = fmt.Errorf("-stats-ms ≥ 20, -max-queue-ms ≥ 100 and -connect-timeout > 0")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "event=failed reason=internal detail=%q\n", "usage: "+err.Error())
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	cfg.StatsInterval = time.Duration(*statsMs) * time.Millisecond
	cfg.MaxQueue = time.Duration(*maxQueueMs) * time.Millisecond

	var stats io.Writer
	if *statsFD > 2 {
		if f := os.NewFile(uintptr(*statsFD), "stats"); f != nil {
			if _, err := f.Stat(); err == nil {
				stats = f
			}
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := rtmpsend.Run(ctx, cfg, os.Stdin, os.Stderr, stats)
	stop()
	os.Exit(code)
}
