package pipeline

import (
	"fmt"
	"strings"
)

// Program is what the program pipeline depends on (the launcher's mixer mode).
type Program struct {
	Width, Height, FPS int
	Slots              int   // video slots m0…m<Slots-1>; the Pause layer mp goes on top
	AudioSlots         int   // audio slots aa0…
	LatencyNs          int64 // compositor latency (MIXER_LATENCY)

	Live                    bool // WebRTC preview, behind the livevalve/liveavalve valves
	LiveWidth, LiveHeight   int
	LiveVKbps, LiveAKbps    int
	LiveVPort, LiveAPort    int
	Levels                  bool // program PCM to the app's VU meter
	LevelsPort              int
	Thumb                   bool // RUN_DIR/thumb.jpg for the cloud
	ThumbWidth, ThumbHeight int
	ThumbEvery              int // s
	ThumbFile               string
	Screen                  bool // pgmtap: the program, small, for the screen's multiview
}

// ScreenFPS is the multiview's rate (MV_FPS): a 7" screen to look at, half the conversions.
const ScreenFPS = 30

// Program builds the program pipeline exactly like eirl-engine.sh with MIXER=1:
//   - video: one appsrc per slot → RGBA → the compositor `pgm` (+ the Pause layer `mp` on top) → `vsrc`
//     (measured fps) → NV12 (12 buffers: the preview, the thumbnail, the screen and the output hang from
//     it) → tee `vt`;
//   - audio: silence + one appsrc per audio slot → `amix` → `pgmlevel` → tee `at`;
//   - branches: `outv`/`outa` (hot output), `cadvalve`→`cadence` (new frames), `phvalve`→`phtap`
//     (headphones), the preview, the VU meter, the thumbnail and `pgmtap` (screen).
//
// Queues of 6 and 10 buffers in the conversions and the compositor latency: without them the compositor
// dropped frames as late (MIXER.md §3, trap 6). appsrc with max-latency 500 ms (with 0 the latency
// cannot be configured, trap 3). Every branch ends in a leaky queue: no branch ever slows the program.
func (p Platform) Program(c Program) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...); b.WriteByte(' ') }
	nv12 := fmt.Sprintf("%s,format=NV12,width=%d,height=%d,framerate=%d/1", p.Mem, c.Width, c.Height, c.FPS)
	src := "appsrc name=%s is-live=true format=time do-timestamp=true min-latency=0 max-latency=500000000 " +
		"max-bytes=0 block=false caps=%s"
	for i := 0; i < c.Slots; i++ {
		w(src+" ! queue max-size-buffers=10 leaky=downstream ! %s output-buffers=10 ! %s,format=RGBA ! pgm.sink_%d",
			fmt.Sprintf("m%d", i), nv12, p.Convert, p.Mem, i)
	}
	w(src+" ! queue max-size-buffers=6 leaky=downstream ! %s output-buffers=10 ! %s,format=RGBA ! pgm.sink_%d",
		"mp", nv12, p.Convert, p.Mem, c.Slots)
	w("%s name=pgm background=black latency=%d ! %s,format=RGBA,width=%d,height=%d,framerate=%d/1 ! identity name=vsrc "+
		"! %s output-buffers=12 ! %s,format=NV12,width=%d,height=%d ! tee name=vt",
		p.Compositor, c.LatencyNs, p.Mem, c.Width, c.Height, c.FPS, p.Convert, p.Mem, c.Width, c.Height)

	acaps := "audio/x-raw,format=S16LE,rate=48000,channels=2"
	w("audiotestsrc is-live=true wave=silence ! %s ! amix.", acaps)
	for i := 0; i < c.AudioSlots; i++ {
		w("appsrc name=aa%d is-live=true format=time do-timestamp=true min-latency=0 max-latency=500000000 "+
			"max-bytes=0 block=false caps=%s,layout=interleaved ! queue name=aq%d max-size-time=1000000000 leaky=downstream ! amix.",
			i, acaps, i)
	}
	w("audiomixer name=amix latency=100000000 output-buffer-duration=40000000 ! audioconvert ! audioresample ! %s "+
		"! level name=pgmlevel interval=50000000 post-messages=true ! tee name=at", acaps)

	w("vt. ! queue leaky=downstream max-size-buffers=2 " +
		"! appsink name=outv emit-signals=false sync=false async=false max-buffers=1 drop=true enable-last-sample=false")
	w("vt. ! queue leaky=downstream max-size-buffers=2 ! valve name=cadvalve drop=true "+
		"! %s ! video/x-raw,format=GRAY8,width=160,height=90 "+
		"! appsink name=cadence emit-signals=false sync=false async=false max-buffers=2 drop=true enable-last-sample=false", p.Convert)
	w("at. ! queue leaky=downstream max-size-buffers=12 " +
		"! appsink name=outa emit-signals=false sync=false async=false max-buffers=12 drop=true enable-last-sample=false")
	w("at. ! queue leaky=downstream max-size-buffers=12 ! valve name=phvalve drop=true " +
		"! appsink name=phtap emit-signals=false sync=false async=false max-buffers=12 drop=true enable-last-sample=false")

	if c.Live {
		// A second, small hardware encode (Baseline, IDR every second so whoever opens the preview does
		// not wait) and the program audio in Opus, by RTP to localhost; the app forwards them. The app
		// closes the valves while nobody watches (`preview` command): no scaling, no encode, no Opus.
		w("vt. ! queue leaky=downstream max-size-buffers=1 ! valve name=livevalve drop=false "+
			"! %s ! %s,width=%d,height=%d,format=NV12,framerate=%d/1 "+
			"! %s control-rate=1 bitrate=%d iframeinterval=%d idrinterval=%d profile=0 preset-level=1 insert-sps-pps=true poc-type=2 "+
			"! h264parse config-interval=-1 ! rtph264pay config-interval=-1 mtu=1200 pt=96 timestamp-offset=0 "+
			"! udpsink host=127.0.0.1 port=%d sync=false async=false",
			p.Convert, p.Mem, c.LiveWidth, c.LiveHeight, c.FPS, p.H264Enc, c.LiveVKbps*1000, c.FPS, c.FPS, c.LiveVPort)
		w("at. ! queue leaky=downstream max-size-buffers=20 ! valve name=liveavalve drop=false "+
			"! opusenc bitrate=%d frame-size=20 complexity=5 ! rtpopuspay pt=111 timestamp-offset=0 "+
			"! udpsink host=127.0.0.1 port=%d sync=false async=false", c.LiveAKbps*1000, c.LiveAPort)
	}
	if c.Levels {
		w("at. ! queue leaky=downstream max-size-buffers=20 ! udpsink host=127.0.0.1 port=%d sync=false async=false", c.LevelsPort)
	}
	if c.Thumb {
		// One per second first and THEN convert (converting 60/s to drop 59 was the old cost); jpegenc on
		// the CPU, never the hardware JPEG encoder (it doubles host1x interrupts, MANAGER.md §4)
		w("vt. ! queue leaky=downstream max-size-buffers=1 ! videorate drop-only=true max-rate=1 "+
			"! %s ! video/x-raw,width=%d,height=%d,format=I420 "+
			"! videorate drop-only=true ! video/x-raw,framerate=1/%d "+
			"! jpegenc quality=75 ! multifilesink location=%s sync=false async=false",
			p.Convert, c.ThumbWidth, c.ThumbHeight, c.ThumbEvery, c.ThumbFile)
	}
	if c.Screen {
		w("vt. ! queue leaky=downstream max-size-buffers=1 ! videorate drop-only=true max-rate=%d "+
			"! %s output-buffers=4 ! %s,format=RGBA,width=960,height=540 "+
			"! appsink name=pgmtap emit-signals=false sync=false async=false max-buffers=1 drop=true enable-last-sample=false",
			ScreenFPS, p.Convert, p.Mem)
	}
	return strings.TrimSpace(b.String())
}

// Output is the hot output pipeline (F3): fed by outv/outa, encoded and muxed to MPEG-TS, and handed to
// its sender (OUTPUTS.md §2): the FIFO of srt-live-transmit or eirl-send (@FIFO@, replaced at each
// session), or UDP (ristsender on this machine, or a receiver on the network).
type Output struct {
	Width, Height, FPS  int // the program's: the appsrc's caps
	OutWidth, OutHeight int // the output's (0 = the program's): the converter scales
	Half                bool
	Codec               string // h264 | h265 ("" = h264)
	VKbps, AKbps        int
	GOP                 int // frames at the output's rate
	Profile, Preset     int // Profile is H.264's (H.265 is always Main)
	TwoPass             bool
	QPRange             string // validated by the config: digits, commas and colons
	AEnc                string // voaacenc | avenc_aac
	Record              string // optional local copy (diagnostics)
	Sink                Sink
}

// Sink is where the muxed TS goes. Host is validated by the config (a DNS name or an IP address, without
// the brackets of an IPv6 URL).
type Sink struct {
	Kind string // fifo (the default) | udp | rtp
	Host string
	Port int
}

// FIFOPlaceholder is replaced by the FIFO's path at each session.
const FIFOPlaceholder = "@FIFO@"

// Output builds the output pipeline like the launcher's out_pipeline. The converter to I420 before the
// encoder copies each frame into the output's own pool: if the encoder imported the program's buffers it
// kept them, and stopping the service after emitting hung (6.8 s, or SIGKILL with the output on). It also
// scales to the output's size (the VIC, no CPU). Half the rate is dropped before the output, by its bridge.
// audioconvert: voaacenc wants a channel-mask in stereo and the appsrc's fixed caps have none.
func (p Platform) Output(c Output) string {
	tune := ""
	if c.TwoPass {
		tune += " EnableTwopassCBR=true"
	}
	if c.QPRange != "" {
		tune += fmt.Sprintf(` qp-range="%s"`, c.QPRange)
	}
	fps := c.FPS
	if c.Half {
		// The output's bridge passes one frame of two (gst.Bridge.SetEvery): the appsrc says the rate the
		// encoder gets, which its rate control needs
		fps = max(1, c.FPS/2)
	}
	size := ""
	if c.OutWidth > 0 && c.OutHeight > 0 && (c.OutWidth != c.Width || c.OutHeight != c.Height) {
		size = fmt.Sprintf(",width=%d,height=%d", c.OutWidth, c.OutHeight)
	}
	var enc string
	if c.Codec == "h265" {
		// No poc-type in nvv4l2h265enc; Main is its default profile (OUTPUTS.md §7)
		enc = fmt.Sprintf(`%s name=venc control-rate=1 bitrate=%d iframeinterval=%d idrinterval=%d `+
			`preset-level=%d maxperf-enable=true insert-sps-pps=true insert-vui=true%s `+
			`! h265parse config-interval=-1`, p.H265Enc, c.VKbps*1000, c.GOP, c.GOP, c.Preset, tune)
	} else {
		enc = fmt.Sprintf(`%s name=venc control-rate=1 bitrate=%d iframeinterval=%d idrinterval=%d `+
			`profile=%d preset-level=%d maxperf-enable=true insert-sps-pps=true insert-vui=true poc-type=2%s `+
			`! h264parse config-interval=-1`, p.H264Enc, c.VKbps*1000, c.GOP, c.GOP, c.Profile, c.Preset, tune)
	}
	d := fmt.Sprintf(`appsrc name=ov is-live=true format=time do-timestamp=false max-bytes=0 block=false `+
		`caps="%s,format=NV12,width=%d,height=%d,framerate=%d/1" `+
		`! queue max-size-buffers=4 leaky=downstream ! %s output-buffers=6 ! %s,format=I420%s `+
		`! %s ! queue ! mux. `+
		`appsrc name=oa is-live=true format=time do-timestamp=false max-bytes=0 block=false `+
		`caps="audio/x-raw,format=S16LE,rate=48000,channels=2,layout=interleaved" `+
		`! queue max-size-buffers=100 leaky=downstream ! audioconvert ! %s bitrate=%d ! aacparse ! queue ! mux. `+
		`mpegtsmux name=mux alignment=7`,
		p.Mem, c.Width, c.Height, fps, p.Convert, p.Mem, size, enc, c.AEnc, c.AKbps*1000)
	var sink string
	switch c.Sink.Kind {
	case "udp":
		sink = fmt.Sprintf("udpsink host=%s port=%d sync=false async=false", c.Sink.Host, c.Sink.Port)
	case "rtp":
		sink = fmt.Sprintf("rtpmp2tpay ! udpsink host=%s port=%d sync=false async=false", c.Sink.Host, c.Sink.Port)
	default:
		sink = "filesink location=" + FIFOPlaceholder + " buffer-mode=unbuffered sync=false async=false"
	}
	if c.Record != "" {
		return d + " ! tee name=rt rt. ! queue ! " + sink + " rt. ! queue ! filesink location=" + c.Record + " sync=false async=false"
	}
	return d + " ! " + sink
}
