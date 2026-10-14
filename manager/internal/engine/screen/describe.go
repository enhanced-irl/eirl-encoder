package screen

import (
	"fmt"
	"strings"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/pipeline"
	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/screen/ui"
)

// The multiview's timing (engine_mixer.py).
const (
	// MVGap: a source's frame goes to its tile if MVGap has passed since the last one (from 60/s one in two,
	// from 30/s all of them even with some jitter). The engine gives it to each source bridge's SetScreen.
	MVGap = 25 * time.Millisecond
	// MVMargin: on top of the program's latency for the program on the screen (what the compositor takes to
	// give it out). The engine's pgmtap → mvp bridge (BridgeShared) uses Extra = ProgramDelay.
	MVMargin = 40 * time.Millisecond

	// UIFPS: the UI layer's nominal rate (the VU meters, 20 a second).
	UIFPS = 20
)

// ProgramDelay is the Extra of the pgmtap → mvp bridge: the program compositor's latency plus MVMargin,
// on top of each program frame's pts on the screen.
func ProgramDelay(latency time.Duration) time.Duration { return latency + MVMargin }

// TileDelay is the pad offset of each tile's mvd<i> queue, so that a change in a source appears on its tile
// and on the program in the same frame of the screen. The program tile shows a program frame ProgramDelay
// after its pts, and a program frame holds the source frames that start before its end: up to one program
// frame (1/fps) newer than its pts. So the tiles go ProgramDelay minus a program frame late. Both come from
// the configuration, not from the sources; what is left is where each source frame falls (MIXER.md §22.2).
func TileDelay(latency time.Duration, fps int) time.Duration {
	return ProgramDelay(latency) - time.Second/time.Duration(max(1, fps))
}

// tileQueue sizes a tile's mvd<i> queue for the frames waiting for their (delayed) turn after their
// conversion (small RGBA from the converter's own pool, never the source's buffers, which the program
// shares). A tile frame arrives up to MIXER_SYNC ahead of its time, and the offset adds TileDelay: at most
// one frame every MVGap for that long, plus two. The converter's pool holds those, what the
// compositor's pad keeps (up to its latency), the frame on screen and the one being converted.
func tileQueue(sync, latency time.Duration, fps int) (queue, buffers int) {
	queue = int((sync+TileDelay(latency, fps))/MVGap) + 2
	return queue, queue + 6
}

// Pipeline is what Describe needs from the engine's configuration.
type Pipeline struct {
	Width, Height, FPS int           // the program
	Latency            time.Duration // the compositors' latency (MIXER_LATENCY)
	Sync               time.Duration // capture → program (MIXER_SYNC): how early a tile's frame arrives
	Snapshot           bool          // the 1 fps JPEG of what the screen shows (RUN_DIR/screen.jpg)
}

// Describe is the screen's pipeline (Screen.describe): the program from pgmtap (appsrc mvp, 960x540 RGBA in
// GPU memory), one tile per slot (appsrc mvs<i>, NV12 at the program size, from each source's bridge), and the
// UI layer on top (appsrc ui, RGBA from Go); the compositor to the HDMI (a copy with its own 8 buffers and
// sync=true: without it, glitches with movement, MIXER.md §3 trap 4) and, if asked, a 1 fps half-size JPEG
// for the web (appsink snap). Every appsrc has max-latency 500 ms: with 0 the compositor does not set up
// its latency (jerks and «clock problem», trap 3). Each tile ends in a queue mvd<i> whose src pad the
// screen shifts by TileDelay.
func Describe(p pipeline.Platform, lay *ui.Layout, c Pipeline) string {
	fps := min(c.FPS, pipeline.ScreenFPS)
	appsrc := func(name, caps string) string {
		return fmt.Sprintf(`appsrc name=%s is-live=true format=time do-timestamp=true min-latency=0 max-latency=500000000 `+
			`max-bytes=0 block=false caps="%s"`, name, caps)
	}
	const q = "queue max-size-buffers=6 leaky=downstream"
	tq, tb := tileQueue(c.Sync, c.Latency, c.FPS)
	var parts []string
	pg := lay.PGM
	pads := []string{fmt.Sprintf("sink_0::xpos=%d sink_0::ypos=%d sink_0::width=%d sink_0::height=%d", pg.X, pg.Y, pg.W, pg.H)}
	parts = append(parts, appsrc("mvp", fmt.Sprintf("%s,format=RGBA,width=960,height=540,framerate=%d/1", p.Mem, fps))+
		fmt.Sprintf(" ! queue max-size-buffers=2 leaky=downstream ! %s output-buffers=10 ! %s,format=RGBA,width=%d,height=%d ! mv.sink_0",
			p.Convert, p.Mem, pg.W, pg.H))
	for i, r := range lay.Tiles {
		n := i + 1
		pads = append(pads, fmt.Sprintf("sink_%d::xpos=%d sink_%d::ypos=%d sink_%d::width=%d sink_%d::height=%d",
			n, r.X, n, r.Y, n, r.W, n, r.H))
		parts = append(parts, appsrc(fmt.Sprintf("mvs%d", i), fmt.Sprintf("%s,format=NV12,width=%d,height=%d,framerate=%d/1",
			p.Mem, c.Width, c.Height, fps))+
			fmt.Sprintf(" ! %s ! %s output-buffers=%d ! %s,format=RGBA,width=%d,height=%d ! queue name=mvd%d max-size-buffers=%d leaky=downstream ! mv.sink_%d",
				q, p.Convert, tb, p.Mem, r.W, r.H, i, tq, n))
	}
	n := len(lay.Tiles) + 1
	pads = append(pads, fmt.Sprintf("sink_%d::zorder=10", n))
	// The UI layer: RGBA from Go (no conversion on the CPU), 20 a second for the meters
	parts = append(parts, appsrc("ui", fmt.Sprintf("video/x-raw,format=RGBA,width=%d,height=%d,framerate=%d/1", lay.DW, lay.DH, UIFPS))+
		fmt.Sprintf(" ! queue max-size-buffers=2 leaky=downstream ! %s ! %s,format=RGBA ! mv.sink_%d", p.Convert, p.Mem, n))
	parts = append(parts, fmt.Sprintf("%s name=mv background=black latency=%d %s ! %s,width=%d,height=%d,framerate=%d/1 "+
		"! tee name=mvt mvt. ! queue max-size-buffers=2 ! %s output-buffers=8 ! %s,format=RGBA ! %s sync=true",
		p.Compositor, c.Latency.Nanoseconds(), strings.Join(pads, " "), p.Mem, lay.DW, lay.DH, fps, p.Convert, p.Mem, p.Display))
	if c.Snapshot {
		parts = append(parts, fmt.Sprintf("mvt. ! queue leaky=downstream max-size-buffers=1 ! videorate drop-only=true max-rate=1 "+
			"! %s ! video/x-raw,format=I420,width=%d,height=%d ! jpegenc quality=70 "+
			"! appsink name=snap emit-signals=false sync=false async=false max-buffers=1 drop=true",
			p.Convert, lay.DW/2/2*2, lay.DH/2/2*2))
	}
	return strings.Join(parts, " ")
}
