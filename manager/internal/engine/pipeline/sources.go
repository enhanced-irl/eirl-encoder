package pipeline

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Timings is an HDMI mode as VIDIOC_QUERY_DV_TIMINGS gives it.
type Timings struct {
	W, H, FPS  int
	Interlaced bool
	HTotal     int
	PClk       int64 // Hz
}

// Mode is "WxH@F".
func (t Timings) Mode() string { return fmt.Sprintf("%dx%d@%d", t.W, t.H, t.FPS) }

// CSILoad is the % of each line the mode takes in the Nano's 2 CSI lanes (the launcher's rule):
// 1080p50 takes 89 %, 1080p60 107 %. 0 without data.
func CSILoad(t Timings) int64 {
	if t.HTotal == 0 || t.PClk == 0 {
		return 0
	}
	return int64(t.W) * 16 * t.PClk * 100 / (int64(t.HTotal) * 2 * 972000000)
}

// CSIFramesLate is how many frames nvv4l2camerasrc's arrival stamps are behind the capture (MIXER.md §17).
const CSIFramesLate = 2

// CSILatency is the time from capture to arrival in nvv4l2camerasrc (66.8 ms at 30 fps).
func CSILatency(fps int) time.Duration {
	if fps < 1 {
		fps = 1
	}
	return time.Duration(CSIFramesLate * int64(1e9/float64(fps)))
}

// SinkTail is the common end of every source: NV12 in GPU memory at the program size, to an appsink. A
// leaky queue first: if the conversion stalls for a moment the capture does not wait and frames do not
// pile up behind (the board's HDMI arrived ~0.5 s late, MIXER.md §3 trap 23). The buffers are held by
// the appsink (2), the program's queue (10) and the screen's (6): 20 leave room.
func (p Platform) SinkTail(width, height, buffers int) string {
	return fmt.Sprintf(" ! queue max-size-buffers=2 max-size-bytes=0 max-size-time=0 leaky=downstream"+
		" ! %s output-buffers=%d ! %s,format=NV12,width=%d,height=%d "+
		"! appsink name=snk emit-signals=false sync=false max-buffers=2 drop=true enable-last-sample=false",
		p.Convert, buffers, p.Mem, width, height)
}

// CSI is the board's HDMI (TC358743). nvcam: with the platform's capture element, which leaves UYVY in
// GPU memory; otherwise v4l2src, and the converter copies 4 MB per frame with the CPU (~20 points of a
// core at 1080p30). The caps' framerate is not checked by the element, the size is: with a size other
// than the signal's it waits forever without an error (the engine unsticks it, MIXER.md §17).
func (p Platform) CSI(node string, t Timings, width, height int, nvcam bool) string {
	var head string
	if nvcam {
		head = fmt.Sprintf("%s name=cap device=%s ! %s,format=UYVY,width=%d,height=%d,framerate=%d/1",
			p.CSICapture, node, p.Mem, t.W, t.H, t.FPS)
	} else {
		head = fmt.Sprintf("v4l2src name=cap device=%s do-timestamp=true ! video/x-raw,format=UYVY,width=%d,height=%d,framerate=%d/1",
			node, t.W, t.H, t.FPS)
	}
	return head + p.SinkTail(width, height, 20)
}

// Probe is what `eirl-encoderd probe` chose for a USB source (already validated by the caller).
type Probe struct {
	Node, Name, Format string
	Width, Height      int
	FPSNum, FPSDen     int
	ALSACard, Warnings string
	USBSpeed           int
}

// Rate is the probe's framerate as a fraction.
func (pr Probe) Rate() string { return strconv.Itoa(pr.FPSNum) + "/" + strconv.Itoa(pr.FPSDen) }

// FPS is the framerate as a number.
func (pr Probe) FPS() float64 {
	if pr.FPSDen == 0 {
		return 0
	}
	return float64(pr.FPSNum) / float64(pr.FPSDen)
}

// Mode is "WxH@F" with the fps without trailing zeros ("1920x1080@59.94").
func (pr Probe) Mode() string {
	f := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(pr.FPS(), 'f', 2, 64), "0"), ".")
	return fmt.Sprintf("%dx%d@%s", pr.Width, pr.Height, f)
}

// USB is a capture card or camera. MJPEG goes through the hardware JPEG decoder, which is stable in its
// own pipeline (trap 8). Without jpegparse: v4l2src already gives one whole JPEG per buffer, and parsing
// it on the CPU cost 6-11 points of a core per camera (MIXER.md §17). parse: with jpegparse, for webcams
// that send JPEGs without an end marker (the source asks for it if the decoder fails).
func (p Platform) USB(pr Probe, width, height int, parse bool) string {
	var head string
	if pr.Format == "MJPG" {
		jp := ""
		if parse {
			jp = "! jpegparse "
		}
		head = fmt.Sprintf("v4l2src name=cap device=%s do-timestamp=true ! image/jpeg,width=%d,height=%d,framerate=%s %s! %s",
			pr.Node, pr.Width, pr.Height, pr.Rate(), jp, p.MJPEGDec)
	} else {
		head = fmt.Sprintf("v4l2src name=cap device=%s do-timestamp=true ! video/x-raw,format=%s,width=%d,height=%d,framerate=%s",
			pr.Node, pr.Format, pr.Width, pr.Height, pr.Rate())
	}
	return head + p.SinkTail(width, height, 20)
}

// IsMJPEGWithoutParse says whether a USB description could need jpegparse (Source.needs_jpegparse).
func (p Platform) IsMJPEGWithoutParse(desc string) bool {
	return strings.Contains(desc, p.MJPEGDec) && !strings.Contains(desc, "jpegparse")
}

// Test is the bars source (SOURCE=test).
func (p Platform) Test(width, height, fps int) string {
	return fmt.Sprintf("videotestsrc name=cap is-live=true pattern=smpte ! video/x-raw,width=1280,height=720,framerate=%d/1 "+
		`! timeoverlay halignment=left valignment=top font-desc="Sans 28" `+
		`! clockoverlay halignment=right valignment=top time-format="%%H:%%M:%%S" font-desc="Sans 28"`, fps) +
		p.SinkTail(width, height, 8)
}

// RTSPLatencyMs is rtspsrc's jitterbuffer for network cameras: wifi hiccups of up to ~200 ms.
const RTSPLatencyMs = 100

// NetworkDepay is the depayloader chain per codec. H.265 goes through MPEG-TS and back: the decoder
// only output the IRAPs of some H.265 streams that arrive over RTSP (NETWORK_SOURCES.md §3, trap 15).
var NetworkDepay = map[string]string{
	"h264": "rtph264depay ! h264parse",
	"h265": "rtph265depay ! h265parse ! mpegtsmux ! tsdemux ! h265parse",
}

// Network is a network camera from the local MediaMTX, to the decoder (name=dec, to read the decoded
// size). drop-on-latency: if what follows cannot keep up, the jitterbuffer drops what is older than its
// latency instead of piling it up without limit (trap 16).
func (p Platform) Network(location, codec string, width, height int) string {
	return fmt.Sprintf("rtspsrc name=cap location=%s protocols=tcp latency=%d drop-on-latency=true ! %s ! %s name=dec",
		location, RTSPLatencyMs, NetworkDepay[codec], p.Decoder) + p.SinkTail(width, height, 20)
}

// Still kinds.
const (
	StillBlack = "black"
	StillPause = "pause"
	StillSlate = "slate"
)

// Still is a still image of the program up to I420 at w×h; StillTail takes it into GPU memory. The text
// is never here: it is the `text` property of the textoverlay `txt` (the Pause text is typed by the
// user). The slate is videotestsrc's SMPTE with two changes: the bottom right corner in black, like the
// original (videotestsrc puts noise there, which does not compress and "breathes" at every keyframe),
// and the text in a black box, painted in BGRx (in YUV textoverlay only darkens the luma and the box
// came out tinted by each bar). Built once, at start.
func Still(kind string, w, h int, text bool) string {
	if kind == StillSlate {
		x, y := w*3/4, h*3/4 // where the noise starts (what videotestsrc computes)
		return fmt.Sprintf("videotestsrc num-buffers=1 pattern=smpte ! video/x-raw,format=I420,width=%d,height=%d ! c.sink_0 "+
			"videotestsrc num-buffers=1 pattern=black ! video/x-raw,format=I420,width=%d,height=%d ! c.sink_1 "+
			"compositor name=c background=black sink_1::xpos=%d sink_1::ypos=%d "+
			"! video/x-raw,format=I420,width=%d,height=%d ! videoconvert ! video/x-raw,format=BGRx "+
			`! textoverlay name=txt font-desc="Sans Bold %d" valignment=center halignment=center wrap-mode=word `+
			"line-alignment=center shaded-background=true shading-value=255 auto-resize=false "+
			"! videoconvert ! video/x-raw,format=I420", w, h, w-x, h-y, x, y, w, h, h/12)
	}
	color := "0xff000000"
	if kind == StillPause {
		color = "0xff10141c"
	}
	d := fmt.Sprintf("videotestsrc num-buffers=1 pattern=solid-color foreground-color=%s ! video/x-raw,format=I420,width=%d,height=%d", color, w, h)
	if kind == StillPause && text {
		// No auto-resize (textoverlay scales the font with the image and it would not fit): ~1/13 of the
		// height, wrapping words
		d += fmt.Sprintf(` ! textoverlay name=txt font-desc="Sans Bold %d" valignment=center halignment=center `+
			"wrap-mode=word line-alignment=center auto-resize=false", h/13)
	}
	return d
}

// StillTail takes a still into GPU memory at the program size, to an appsink.
func (p Platform) StillTail(w, h int) string {
	return fmt.Sprintf(" ! %s ! %s,format=NV12,width=%d,height=%d ! appsink name=snk sync=false", p.Convert, p.Mem, w, h)
}

// Thumbs is the pipeline of the sources' JPEG thumbnails for the web: one appsrc per slot, one frame per
// second, converted by the VIC and compressed on the CPU (320x180, ~2 ms). appsink async=false: slots
// without a source never get a frame and, with preroll, the pipeline would never reach PLAYING.
func (p Platform) Thumbs(slots, width, height int) string {
	parts := make([]string, slots)
	for i := range parts {
		parts[i] = fmt.Sprintf(`appsrc name=th%d is-live=true format=time do-timestamp=true max-bytes=0 block=false `+
			`caps="%s,format=NV12,width=%d,height=%d,framerate=1/1" ! queue max-size-buffers=1 leaky=downstream `+
			`! %s ! video/x-raw,format=I420,width=320,height=180 ! jpegenc quality=70 `+
			`! appsink name=thj%d emit-signals=false sync=false async=false max-buffers=1 drop=true`, i, p.Mem, width, height, p.Convert, i)
	}
	return strings.Join(parts, " ")
}
