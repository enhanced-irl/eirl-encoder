package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

type fakeWatch struct {
	n  atomic.Int32
	ch chan struct{}
}

func (f *fakeWatch) Viewers() int             { return int(f.n.Load()) }
func (f *fakeWatch) Changed() <-chan struct{} { return f.ch }

// Fake engine that only records the `on` of preview commands (and answers code if it is not "")
func previewEngine(t *testing.T, code string) (*mixer.Client, func() []bool) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mxp") // short path (Unix socket limit on macOS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var got []bool
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				var req struct {
					Cmd string `json:"cmd"`
					On  bool   `json:"on"`
				}
				json.Unmarshal(sc.Bytes(), &req)
				if req.Cmd == "preview" {
					mu.Lock()
					got = append(got, req.On)
					mu.Unlock()
				}
				if code == "" {
					c.Write([]byte(`{"id":1,"ok":true}` + "\n"))
				} else {
					c.Write([]byte(`{"id":1,"ok":false,"error":"` + code + `"}` + "\n"))
				}
			}
			c.Close()
		}
	}()
	return &mixer.Client{Sock: filepath.Join(dir, "s")}, func() []bool {
		mu.Lock()
		defer mu.Unlock()
		return append([]bool(nil), got...)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("did not happen: %s", what)
}

func TestFollowPreview(t *testing.T) {
	cl, got := previewEngine(t, "")
	s := &Server{Mixer: cl, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := &fakeWatch{ch: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.FollowPreview(ctx, w)
	// Nobody: it is stopped as soon as the app starts
	waitFor(t, "preview stopped on start", func() bool { g := got(); return len(g) == 1 && !g[0] })
	// Someone arrives: it turns on right away, without waiting for the tick
	w.n.Store(1)
	w.ch <- struct{}{}
	waitFor(t, "preview on when someone arrives", func() bool { g := got(); return len(g) == 2 && g[1] })
	// They leave: it stays on for previewLinger (the command is not repeated)
	w.n.Store(0)
	w.ch <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	if g := got(); len(g) != 2 {
		t.Fatalf("it stopped without waiting: %v", g)
	}
}

func TestFollowPreviewOldEngine(t *testing.T) {
	// An engine older than the command: tried once, not insisted on every round
	cl, got := previewEngine(t, "unknown_command")
	s := &Server{Mixer: cl, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := &fakeWatch{ch: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.FollowPreview(ctx, w)
	waitFor(t, "one attempt", func() bool { return len(got()) == 1 })
	w.ch <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	if n := len(got()); n != 1 {
		t.Fatalf("insists with an engine that lacks it: %d commands", n)
	}
}

func TestPreviewOn(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		seen time.Time
		want bool
	}{
		{time.Time{}, false}, // nobody since the app started
		{now, true},          // someone now
		{now.Add(-previewLinger + time.Second), true}, // left recently: stays on
		{now.Add(-previewLinger), false},              // after the linger: stops
	} {
		if got := previewOn(c.seen, now); got != c.want {
			t.Errorf("previewOn(%v ago) = %v", now.Sub(c.seen), got)
		}
	}
}
