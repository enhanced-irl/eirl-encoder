// eirl-splash: the EnhancedIRL boot splash on the encoder's HDMI output (see SPLASH.md).
//
//	eirl-splash [-scene boot]              boot: logo + bar until SIGTERM (leaves the still logo)
//	eirl-splash -scene reboot|poweroff|ready   paints that scene and exits (the image stays)
//	eirl-splash -console                   hands the text console back to the screen (failed boot)
//	eirl-splash -png f.png [-size WxH]     draws the scene into a PNG (tests, screen untouched)
//	eirl-splash boot-entry install|uninstall|show [-dry]   extlinux entry (fbcon=map:9, TIMEOUT 1 s)
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/splash"
)

var version = "dev" // -ldflags "-X main.version=…"

const sysVT = "/sys/class/vtconsole"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey { // the journal already adds the time
				return slog.Attr{}
			}
			return a
		},
	})))
	if len(os.Args) > 1 && os.Args[1] == "boot-entry" {
		os.Exit(bootEntry(os.Args[2:]))
	}
	dev := flag.String("fb", "/dev/fb0", "framebuffer of the HDMI output")
	sceneName := flag.String("scene", "boot", "boot | ready | reboot | poweroff")
	console := flag.Bool("console", false, "hand the text console back to the screen and exit")
	timeout := flag.Duration("timeout", 3*time.Minute, "boot: if nobody removes the splash within this time, boot is considered failed and the console is handed back")
	fps := flag.Int("fps", 20, "boot: frames per second of the bar")
	force := flag.Bool("force", false, "paint even if the text console has the screen (manual tests)")
	pngOut := flag.String("png", "", "draw the scene into this PNG instead of the screen")
	size := flag.String("size", "", "with -png: size WxH (default: the current mode of -fb)")
	showVersion := flag.Bool("version", false, "print the version")
	deviceJSON := flag.String("device-json", "/etc/eirl-encoder/device.json", "the app's device settings: the language of the reboot/poweroff texts")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *console {
		if err := splash.MapConsole(*dev); err != nil {
			slog.Error("could not hand the console back", "err", err)
			os.Exit(1)
		}
		slog.Info("text console handed back to the screen", "fb", *dev)
		return
	}
	scene, err := splash.ParseScene(*sceneName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
	r, err := splash.NewRenderer()
	if err != nil {
		slog.Error("resources", "err", err)
		os.Exit(1)
	}
	r.Lang = splash.DeviceLanguage(*deviceJSON)
	if *pngOut != "" {
		os.Exit(writePNG(r, scene, *dev, *size, *pngOut))
	}
	if !*force && splash.ConsoleHasFB(sysVT) {
		// Booted without the extlinux entry (fbcon=map:9): the console would write over the logo.
		slog.Info("the text console has the screen: not painting", "scene", scene)
		return
	}
	if scene == splash.SceneBoot {
		os.Exit(runBoot(r, *dev, *timeout, *fps))
	}
	os.Exit(runStatic(r, *dev, scene))
}

// runStatic paints a scene and exits: the image stays on the screen until something else paints
// over it (on poweroff, until the power goes).
func runStatic(r *splash.Renderer, dev string, scene splash.Scene) int {
	fb, err := splash.Open(dev)
	if err != nil {
		slog.Error("framebuffer", "fb", dev, "err", err)
		return 1
	}
	defer fb.Close()
	_ = fb.Show()
	g := fb.Geometry()
	r.Layout(g.Width, g.Height)
	img := r.Frame(scene)
	fb.Blit(img, img.Bounds())
	slog.Info("scene painted", "scene", scene, "mode", g.String(), "uptime", uptime())
	return 0
}

// runBoot: logo and animated bar until SIGTERM (eirl-splash-quit.service once eirl-encoderd is
// running, or the poweroff). Then it leaves the still logo and releases the screen. It repaints when
// the mode changes and, in case something wipes it without changing the mode, fully every 2 s.
func runBoot(r *splash.Renderer, dev string, timeout time.Duration, fps int) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	fb, err := splash.Open(dev)
	if err != nil {
		slog.Error("framebuffer", "fb", dev, "err", err)
		return 1
	}
	defer fb.Close()
	_ = fb.Show()

	var base, bar *image.RGBA
	paintAll := func() {
		g := fb.Geometry()
		r.Layout(g.Width, g.Height)
		base = r.Frame(splash.SceneBoot)
		bar = image.NewRGBA(r.BarBounds())
		fb.Blit(base, base.Bounds())
	}
	paintAll()
	slog.Info("boot splash", "mode", fb.Geometry().String(), "uptime", uptime())

	start := time.Now()
	tick := time.NewTicker(time.Second / time.Duration(max(1, fps)))
	defer tick.Stop()
	deadline := time.NewTimer(timeout)
	lastCheck, lastFull := start, start
	paused := false
	for {
		select {
		case s := <-sigs:
			if !paused {
				img := r.Frame(splash.SceneReady)
				fb.Blit(img, img.Bounds())
			}
			slog.Info("boot finished: logo kept, screen released", "signal", s.String(),
				"duration", time.Since(start).Round(time.Millisecond).String(), "uptime", uptime())
			return 0
		case <-deadline.C:
			slog.Warn("nobody removed the boot splash: boot failed, handing the text console back", "timeout", timeout)
			fb.Close()
			if err := splash.MapConsole(dev); err != nil {
				slog.Error("could not hand the console back", "err", err)
				return 1
			}
			return 0
		case now := <-tick.C:
			if now.Sub(lastCheck) >= 250*time.Millisecond {
				lastCheck = now
				changed, err := fb.Refresh()
				switch {
				case err != nil && !paused:
					paused = true
					slog.Warn("invalid screen mode: paused until it comes back", "err", err, "uptime", uptime())
				case err == nil && (changed || paused):
					paused = false
					_ = fb.Show()
					paintAll()
					lastFull = now
					slog.Info("screen mode", "mode", fb.Geometry().String(), "uptime", uptime())
				}
			}
			if paused {
				continue
			}
			if now.Sub(lastFull) >= 2*time.Second {
				fb.Blit(base, base.Bounds())
				lastFull = now
			}
			r.DrawBar(bar, now.Sub(start))
			fb.Blit(bar, bar.Bounds())
		}
	}
}

func writePNG(r *splash.Renderer, scene splash.Scene, dev, size, out string) int {
	var w, h int
	if size != "" {
		if _, err := fmt.Sscanf(size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			fmt.Fprintln(os.Stderr, "-size: WxH, e.g. 1024x600")
			return 64
		}
	} else {
		fb, err := splash.Open(dev)
		if err != nil {
			fmt.Fprintln(os.Stderr, "without -size, must be able to open", dev+":", err)
			return 1
		}
		g := fb.Geometry()
		fb.Close()
		w, h = g.Width, g.Height
		slog.Info("current mode", "mode", g.String())
	}
	r.Layout(w, h)
	img := r.Frame(scene)
	if scene == splash.SceneBoot {
		r.DrawBar(img, splash.BarPeriod/2)
	}
	f, err := os.Create(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// uptime: seconds since the kernel started (to time the boot phases in the journal).
func uptime() string {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "?"
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "?"
	}
	return f[0] + "s"
}

// bootEntry: adds or removes the extlinux entry. Backup before writing and atomic write (a power cut
// halfway does not leave the board unbootable).
func bootEntry(args []string) int {
	fl := flag.NewFlagSet("boot-entry", flag.ExitOnError)
	conf := fl.String("conf", "/boot/extlinux/extlinux.conf", "extlinux file")
	state := fl.String("state", "/var/lib/eirl-splash/extlinux.prev", "previous DEFAULT and TIMEOUT (to undo)")
	dry := fl.Bool("dry", false, "show the result without writing")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: eirl-splash boot-entry install|uninstall|show [-conf f] [-state f] [-dry]")
		return 64
	}
	action := args[0]
	_ = fl.Parse(args[1:])
	cur, err := os.ReadFile(*conf)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var prev *splash.BootPrev
	if b, err := os.ReadFile(*state); err == nil {
		p, err := splash.ParseBootPrev(string(b))
		if err != nil {
			fmt.Fprintln(os.Stderr, *state+":", err)
			return 1
		}
		prev = &p
	} else if !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	switch action {
	case "show":
		fmt.Print(string(cur))
		if prev != nil {
			fmt.Printf("# previous state (%s): DEFAULT=%s TIMEOUT=%s\n", *state, prev.Default, prev.Timeout)
		}
		return 0
	case "install":
		out, p, err := splash.InstallEntry(string(cur), prev)
		if err != nil {
			fmt.Fprintln(os.Stderr, "not installing:", err)
			return 1
		}
		if *dry {
			fmt.Print(out)
			return 0
		}
		if out == string(cur) {
			fmt.Println("extlinux.conf already up to date")
			return 0
		}
		if err := os.MkdirAll(filepath.Dir(*state), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := writeAtomic(*state, []byte(p.String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		bak, err := writeConf(*conf, cur, out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("entry %s installed as DEFAULT (base %s, TIMEOUT %s→1 s); backup in %s\n",
			splash.EntryLabel, p.Default, p.Timeout, bak)
		return 0
	case "uninstall":
		out, err := splash.UninstallEntry(string(cur), prev)
		if err != nil {
			fmt.Fprintln(os.Stderr, "not undoing:", err)
			return 1
		}
		if *dry {
			fmt.Print(out)
			return 0
		}
		if out != string(cur) {
			bak, err := writeConf(*conf, cur, out)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			fmt.Println("entry removed; backup in", bak)
		}
		_ = os.Remove(*state)
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown action:", action)
	return 64
}

func writeConf(path string, cur []byte, out string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	bak := path + ".bak-" + time.Now().Format("20060102-150405") + "-splash"
	if err := writeAtomic(bak, cur, st.Mode().Perm()); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	return bak, writeAtomic(path, []byte(out), st.Mode().Perm())
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".eirl-splash.new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
