package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/btaudio"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/config"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/sources"
)

// Exit codes of `probe` (the engine interprets them, internal/engine/probe)
const (
	probeOK         = 0
	probeUsage      = 64 // like the script with an invalid configuration; NOT 2: that is a Go panic's
	probeNoDevice   = 3  // not connected (the script waits)
	probeNotOffered = 4  // connected, but it does not offer that format or mode now (the script waits)
)

// probe chooses what to capture a USB source with and prints it as KEY=value for the engine. It is
// the same rule the web shows (sources.Choose): so "auto" is not reimplemented in bash.
//
//	eirl-encoderd probe [-device auto|/dev/v4l/…] [-format auto|NV12|…] [-mode auto|WxH@fps]
//	eirl-encoderd probe -all [-max N] [-keep ID=FORMAT:WIDTHxHEIGHT@NUM/DEN]… [-want ID]…   (see probeAll)
//	eirl-encoderd probe -list      catalogue in JSON (diagnostics)
func probe(args []string) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	device := fs.String("device", "auto", "VIDEO_DEVICE")
	format := fs.String("format", "auto", "VIDEO_FORMAT")
	mode := fs.String("mode", "auto", "VIDEO_MODE")
	list := fs.Bool("list", false, "source catalogue in JSON")
	all := fs.Bool("all", false, "every USB source at once, sharing the bus (mixer)")
	limit := fs.Int("max", 3, "with -all: at most this many sources")
	var keep, want []sources.Keep
	fs.Func("keep", "with -all: ID=FORMAT:WIDTHxHEIGHT@NUM/DEN of a source already being captured (repeatable)", func(v string) error {
		k, err := parseKeep(v)
		keep = append(keep, k)
		return err
	})
	fs.Func("want", "with -all: ID of a source about to be opened, right after the -keep ones (repeatable)", func(v string) error {
		if !validID(v) {
			return fmt.Errorf("-want %q: a device of /dev/v4l/by-id or /dev/v4l/by-path", v)
		}
		want = append(want, sources.Keep{ID: v})
		return nil
	})
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return probeUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *all && (set["device"] || set["format"] || set["mode"] || set["list"]) ||
		!*all && (set["max"] || set["keep"] || set["want"]) || *limit < 1 || *limit > 4 {
		fmt.Fprintln(os.Stderr, "probe: -all does not go with -device/-format/-mode/-list; -max (1-4), -keep and -want only with -all")
		return probeUsage
	}
	sc := sources.New()
	cat := sc.Scan()
	if *list {
		// The Bluetooth inputs ready now (the app's file): the engine opens them like a loose USB microphone
		cat.Audio = append(cat.Audio, sources.BluetoothAudio(btaudio.Read(btaudio.Path))...)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cat)
		return probeOK
	}
	if *all {
		probeAll(os.Stdout, cat, append(keep, want...), *limit)
		return probeOK
	}
	// The same whitelists as the configuration: what arrives here comes from the app's file
	s := config.Defaults()
	s.Source, s.VideoDevice, s.VideoFormat, s.VideoMode = "usb", *device, *format, *mode
	var fe config.FieldErrors
	if err := s.Validate(); errors.As(err, &fe) {
		bad := config.FieldErrors{}
		for _, k := range []string{"device", "videoFormat", "videoMode"} {
			if fe[k] != "" {
				bad[k] = fe[k]
			}
		}
		if len(bad) > 0 {
			fmt.Fprintln(os.Stderr, "probe:", bad.Error())
			return probeUsage
		}
	}
	src, ok := sources.Find(cat, *device, *format, *mode, sc.DevRoot)
	if !ok {
		if *device == "auto" {
			fmt.Fprintln(os.Stderr, "no USB capture device or camera connected")
		} else {
			fmt.Fprintf(os.Stderr, "USB device %s not connected\n", *device)
		}
		return probeNoDevice
	}
	c, err := sources.Choose(src, *format, *mode)
	if err != nil {
		hint := ""
		switch {
		case src.Error != "":
			hint = ": cannot be read (" + src.Error + ")"
		case slices.Contains(src.Warnings, sources.WarnOnlyAbove):
			hint = ": the camera sends more than 1080p, set it to 1080p"
		}
		fmt.Fprintf(os.Stderr, "%s: %v (format %s, mode %s)%s\n", src.Name, err, *format, *mode, hint)
		return probeNotOffered
	}
	ow, oh := sources.OutputSize(c.Width, c.Height)
	alsa, speed := "", 0
	if src.Audio != nil {
		alsa = src.Audio.Card
	}
	if src.USB != nil {
		speed = src.USB.SpeedMbps
	}
	for _, kv := range [][2]string{
		{"NODE", src.Node}, {"DEVICE", src.ID}, {"NAME", src.Name}, {"FORMAT", c.Format},
		{"WIDTH", fmt.Sprint(c.Width)}, {"HEIGHT", fmt.Sprint(c.Height)},
		{"FPS_NUM", fmt.Sprint(c.Rate.Num)}, {"FPS_DEN", fmt.Sprint(c.Rate.Den)},
		{"OUT_WIDTH", fmt.Sprint(ow)}, {"OUT_HEIGHT", fmt.Sprint(oh)},
		{"ALSA_CARD", alsa}, {"USB_SPEED", fmt.Sprint(speed)}, {"WARNINGS", strings.Join(src.Warnings, ",")},
	} {
		fmt.Printf("%s=%s\n", kv[0], kv[1])
	}
	return probeOK
}

// probeAll: for the mixer, what to capture the USB sources with at the same time (sources.ChooseAll).
// Always exits 0, also with none: the engine waits. Indexes from 0:
//
//	COUNT=n
//	SRC<i>_ID  NODE NAME FORMAT WIDTH HEIGHT FPS_NUM FPS_DEN OUT_WIDTH OUT_HEIGHT ALSA_CARD
//	           USB_SPEED WARNINGS BUS_SHARE   (all with the prefix SRC<i>_)
//
// A source that is not captured comes out with FORMAT and the mode empty and the reason in WARNINGS
// (no_bus, only_4k, no_format, unreadable). BUS_SHARE: % of its bus, rounded.
func probeAll(w io.Writer, cat sources.Catalog, keep []sources.Keep, limit int) {
	picks := sources.ChooseAll(cat, keep, limit)
	fmt.Fprintf(w, "COUNT=%d\n", len(picks))
	for i, p := range picks {
		alsa, speed := "", 0
		if p.Src.Audio != nil {
			alsa = p.Src.Audio.Card
		}
		if p.Src.USB != nil {
			speed = p.Src.USB.SpeedMbps
		}
		mode := [7]string{}
		if p.OK {
			c := p.Choice
			ow, oh := sources.OutputSize(c.Width, c.Height)
			mode = [7]string{c.Format, fmt.Sprint(c.Width), fmt.Sprint(c.Height), fmt.Sprint(c.Rate.Num),
				fmt.Sprint(c.Rate.Den), fmt.Sprint(ow), fmt.Sprint(oh)}
		}
		for _, kv := range [][2]string{
			{"ID", p.Src.ID}, {"NODE", p.Src.Node}, {"NAME", p.Src.Name}, {"FORMAT", mode[0]},
			{"WIDTH", mode[1]}, {"HEIGHT", mode[2]}, {"FPS_NUM", mode[3]}, {"FPS_DEN", mode[4]},
			{"OUT_WIDTH", mode[5]}, {"OUT_HEIGHT", mode[6]}, {"ALSA_CARD", alsa},
			{"USB_SPEED", fmt.Sprint(speed)}, {"WARNINGS", strings.Join(p.Src.Warnings, ",")},
			{"BUS_SHARE", fmt.Sprint(int(math.Round(p.Share * 100)))},
		} {
			fmt.Fprintf(w, "SRC%d_%s=%s\n", i, kv[0], kv[1])
		}
	}
}

var reKeepSpec = regexp.MustCompile(`^(NV12|YUY2|I420|UYVY|MJPG):([0-9]{2,5})x([0-9]{2,5})@([0-9]{1,6})/([0-9]{1,6})$`)

// validID: a USB source ID with the same whitelist as VIDEO_DEVICE (without "auto": it is a concrete
// source).
func validID(id string) bool {
	s := config.Defaults()
	s.Source, s.VideoDevice = "usb", id
	var fe config.FieldErrors
	return id != "auto" && !(errors.As(s.Validate(), &fe) && fe["device"] != "")
}

// parseKeep: ID=FORMAT:WIDTHxHEIGHT@NUM/DEN.
func parseKeep(v string) (sources.Keep, error) {
	id, spec, ok := strings.Cut(v, "=")
	m := reKeepSpec.FindStringSubmatch(spec)
	if !ok || m == nil || !validID(id) {
		return sources.Keep{}, fmt.Errorf("-keep %q: ID=FORMAT:WIDTHxHEIGHT@NUM/DEN", v)
	}
	n := make([]int, 4)
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+2])
	}
	if n[3] == 0 {
		return sources.Keep{}, fmt.Errorf("-keep %q: denominator 0", v)
	}
	return sources.Keep{ID: id, Choice: sources.Choice{Format: m[1], Width: n[0], Height: n[1], Rate: sources.Rate{Num: n[2], Den: n[3]}}}, nil
}
