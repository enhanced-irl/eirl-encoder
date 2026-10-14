package api

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/service"
)

// hub fans out the status (every 2 s) and the journal (live) to the SSE clients. It only works while
// there is at least one: the first starts polling and `journalctl -f`; the last one stops them.
type hub struct {
	srv *Server

	mu     sync.Mutex
	subs   map[chan sseEvent]struct{}
	cancel context.CancelFunc
}

type sseEvent struct {
	name string
	data []byte
}

const statusInterval = 2 * time.Second

func newHub(s *Server) *hub { return &hub{srv: s, subs: map[chan sseEvent]struct{}{}} }

func (h *hub) subscribe() (chan sseEvent, func()) {
	ch := make(chan sseEvent, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	if len(h.subs) == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.pollStatus(ctx)
		go h.followJournal(ctx)
		go h.followMixer(ctx)
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		if len(h.subs) == 0 && h.cancel != nil {
			h.cancel()
			h.cancel = nil
		}
		h.mu.Unlock()
	}
}

func (h *hub) broadcast(name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- sseEvent{name, b}:
		default: // slow client: it loses this event, it does not hold back the others
		}
	}
}

func (h *hub) pollStatus(ctx context.Context) {
	t := time.NewTicker(statusInterval)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, statusInterval)
		st := h.srv.collect(cctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		h.broadcast("status", st)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// followMixer: the mixer state as soon as it changes (a tap on the board's screen, another tab),
// without waiting for the 2 s poll. With no mixer running, it retries every 2 s.
func (h *hub) followMixer(ctx context.Context) {
	if h.srv.Mixer == nil {
		return
	}
	for ctx.Err() == nil {
		h.srv.Mixer.Watch(ctx, func(st *mixer.State) { h.broadcast("mixer", st) })
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (h *hub) followJournal(ctx context.Context) {
	for ctx.Err() == nil {
		h.srv.Unit.Follow(ctx, func(e service.Entry) { h.broadcast("log", e) })
		select { // journalctl died: retry without hammering
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}
