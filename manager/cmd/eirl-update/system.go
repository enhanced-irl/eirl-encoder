package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update/syslayer"
)

// systemCmd: eirl-update system … (UPDATES.md §12). Exit 0 done (or skipped: not the encoder's base), 2 not
// applied or undone (a stable code in the log and in state.json), 1 any other error, 64 usage.
func systemCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: eirl-update system apply|plan|recover|status|fingerprint|compare [flags]")
		return 64
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("system "+sub, flag.ContinueOnError)
	root := fs.String("root", "/", "the root filesystem (a fingerprint of a mounted image; tests)")
	stateDir := fs.String("state-dir", syslayer.DefaultStateDir, "the layer's state directory")
	layerDir := fs.String("layer", "", "the layer's directory (the bundle's system/)")
	image := fs.Bool("image", false, "apply: into an SD card image being built (its chroot, no systemd running)")
	out := fs.String("out", "", "fingerprint: write it here instead of stdout")
	a := fs.String("a", "", "compare: the first fingerprint")
	b := fs.String("b", "", "compare: the second fingerprint")
	expected := fs.String("expected", "", "compare: the differences that are allowed (patterns)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 64
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(_ []string, at slog.Attr) slog.Attr {
		if at.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return at
	}}))
	ap := &syslayer.Applier{Root: *root, StateDir: *stateDir, Image: *image, Log: log, Proc: "/proc", Sys: "/sys"}
	if *root != "/" {
		ap.Proc, ap.Sys = "", ""
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if sub == "apply" || sub == "recover" {
		// An apply or a recovery goes to its end (or undoes itself) whatever signal comes: its own deadline bounds it
		// (UPDATES.md §12.4). Stopping it halfway would leave the board half changed
		stop()
		signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		ctx = context.Background()
	}
	switch sub {
	case "apply", "plan":
		if *layerDir == "" {
			fmt.Fprintln(os.Stderr, "eirl-update system "+sub+": -layer DIR is required")
			return 64
		}
		if sub == "plan" {
			p, skipped, err := ap.Plan(*layerDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "eirl-update system plan:", err)
				return 1
			}
			if skipped != "" {
				fmt.Println("skipped:", skipped)
				return 0
			}
			fmt.Print(p.String())
			return 0
		}
		res, err := ap.Apply(ctx, *layerDir)
		if err != nil {
			var f *syslayer.Failure
			if errors.As(err, &f) {
				log.Error("system layer: not applied", "code", f.Code, "reason", f.Reason)
				return 2
			}
			log.Error("system layer: error", "err", err)
			return 1
		}
		switch {
		case res.Skipped != "":
			fmt.Println("system layer skipped:", res.Skipped)
		case res.Changed:
			fmt.Printf("system layer applied; %d change(s) wait for a restart\n", len(res.Pending))
		default:
			fmt.Println("system layer: up to date")
		}
		return 0
	case "recover":
		was, err := ap.Recover(ctx)
		if err != nil {
			log.Error("system layer: recovery failed", "err", err)
			return 1
		}
		if !was {
			fmt.Println("no interrupted system layer apply")
		}
		return 0
	case "status":
		b, err := os.ReadFile(filepath.Join(*root, *stateDir, syslayer.StateFile))
		if err != nil {
			fmt.Println(`{"v":1}`)
			return 0
		}
		os.Stdout.Write(b)
		return 0
	case "fingerprint":
		var l *syslayer.Layer
		if *layerDir != "" {
			var err error
			if l, err = syslayer.LoadLayer(*layerDir); err != nil {
				fmt.Fprintln(os.Stderr, "eirl-update system fingerprint:", err)
				return 1
			}
		}
		fp, err := syslayer.TakeFingerprint(*root, syslayer.BaseKernel, l)
		if err != nil {
			fmt.Fprintln(os.Stderr, "eirl-update system fingerprint:", err)
			return 1
		}
		j, _ := json.MarshalIndent(fp, "", "  ")
		j = append(j, '\n')
		if *out != "" {
			if err := os.WriteFile(*out, j, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "eirl-update system fingerprint:", err)
				return 1
			}
			return 0
		}
		os.Stdout.Write(j)
		return 0
	case "compare":
		fa, err1 := readFingerprint(*a)
		fb, err2 := readFingerprint(*b)
		if err := errors.Join(err1, err2); err != nil {
			fmt.Fprintln(os.Stderr, "eirl-update system compare:", err)
			return 64
		}
		var patterns []string
		if *expected != "" {
			e, err := os.ReadFile(*expected)
			if err != nil {
				fmt.Fprintln(os.Stderr, "eirl-update system compare:", err)
				return 64
			}
			patterns = syslayer.ParseExpected(e)
		}
		diffs := syslayer.Compare(fa, fb)
		bad := syslayer.Unexpected(diffs, patterns)
		fmt.Printf("%d differences, %d expected, %d unexpected\n", len(diffs), len(diffs)-len(bad), len(bad))
		for _, d := range bad {
			fmt.Println("UNEXPECTED", d)
		}
		if len(bad) > 0 {
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "eirl-update system: unknown command", sub)
	return 64
}

func readFingerprint(p string) (*syslayer.Fingerprint, error) {
	if p == "" {
		return nil, errors.New("-a and -b are required")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var fp syslayer.Fingerprint
	return &fp, json.Unmarshal(b, &fp)
}
