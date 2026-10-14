package phones

import (
	"sync"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/gst"
)

// Tap carries the program audio from the appsink phtap into the listening pipeline's appsrc, in C, and
// measures how late each buffer reaches phtap with respect to its mark. What the Python engine does per
// buffer in on_sample:
//
//   - late = clock now − (program base_time + pts), for every buffer with a pts, at most MaxLates kept
//     between two takes;
//   - push the buffer as it is to the appsrc, if any. As it is: the tee `at` shares it with the output
//     (outa) and the preview, so neither its pts nor anything else may be written. The listening
//     pipeline has the program's clock and base_time, so its pts is already right there.
//
// A Tap is attached to phtap from its creation until Free: the appsink must always have someone pulling,
// or with the valve open it would keep a backlog of max-buffers chunks and play them that late.
type Tap interface {
	// Connect sends what reaches phtap to appsrc from now on; nil = only measure (and drop).
	Connect(appsrc gst.Element)
	// TakeLate is the P90 of the lateness since the previous take, which starts a new window; 0 without
	// samples (or without a measure).
	TakeLate() time.Duration
	// Free once the program pipeline is in NULL (nothing can call into it any more).
	Free()
}

// NewBridgeTap is the Tap on top of today's gst.Bridge. It plays exactly as the Python engine does
// (BridgePass: buffers untouched, pts on the shared program timeline) but cannot measure the lateness:
// no bridge kind records the age of a buffer it passes as is, and the ones that do (BridgeShared)
// rewrite the pts and DTS of a buffer the tee shares with the output. So TakeLate is always 0 and the
// latency falls back to the screen's, which on the real engine leaves the headphones silent
// (HEADPHONES.md §1.3): a stopgap until internal/gst has a Tap in C (see the package's report).
//
// The bridge's destination is fixed when it is made, so each Connect makes a new one; the replaced
// ones are kept until Free (a call in flight may still be using them).
func NewBridgeTap(media gst.Media, pgm gst.Pipeline, phtap gst.Element) Tap {
	t := &bridgeTap{media: media, pgm: pgm, phtap: phtap}
	t.idle = media.NewBridge(gst.BridgePass, pgm, nil) // nowhere to go: pulls and drops
	t.idle.Attach(phtap)
	return t
}

type bridgeTap struct {
	media   gst.Media
	pgm     gst.Pipeline
	phtap   gst.Element
	mu      sync.Mutex
	idle    gst.Bridge
	cur     gst.Bridge // the one attached, toward an appsrc; nil = idle attached
	retired []gst.Bridge
}

func (t *bridgeTap) Connect(appsrc gst.Element) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur != nil {
		t.cur.Detach()
		t.retired = append(t.retired, t.cur)
		t.cur = nil
	}
	if appsrc == nil {
		t.idle.Attach(t.phtap)
		return
	}
	t.cur = t.media.NewBridge(gst.BridgePass, t.pgm, appsrc)
	t.cur.Attach(t.phtap)
}

func (t *bridgeTap) TakeLate() time.Duration { return 0 }

func (t *bridgeTap) Free() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range append(t.retired, t.cur, t.idle) {
		if b != nil {
			b.Free()
		}
	}
	t.retired, t.cur, t.idle = nil, nil, nil
}
