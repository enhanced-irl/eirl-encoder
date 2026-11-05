// C side of the engine's GStreamer layer (ENGINE_GO.md §3): helpers that are awkward through cgo and
// the per-frame work, which never enters Go. Built only on linux with cgo (GStreamer 1.14, GLib 2.56).
#ifndef EIRL_GSTC_H
#define EIRL_GSTC_H

#include <gst/gst.h>
#include <gst/app/gstappsink.h>
#include <gst/app/gstappsrc.h>

// --- generic helpers ---
GstElement *eg_parse(const char *desc, char **err);
GstElement *eg_by_name(GstElement *bin, const char *name);
int eg_set_state(GstElement *el, int state);
int eg_get_state(GstElement *el, gint64 timeout_ns);
void eg_fix_base(GstElement *pipe, GstClockTime base);
GstClockTime eg_base_time(GstElement *el);
int eg_send_eos(GstElement *pipe);
// A recording's timeline starts at its first video buffer (RECORDING.md §5.4); 0 if a source is missing
int eg_zero_start(GstElement *pipe, const char *video, const char *audio);
GstClockTime eg_now(void);
int eg_set_arg(GstElement *el, const char *prop, const char *value);
int eg_set_pad_arg(GstElement *el, const char *pad, const char *prop, const char *value);
void eg_set_bool(GstElement *el, const char *prop, int v);
void eg_set_double(GstElement *el, const char *prop, double v);
int eg_has_factory(const char *name);
const char *eg_name(GstElement *el);

// A bus message, copied out so Go never holds GStreamer objects
typedef struct {
	int type;        // GstMessageType
	char *src;       // source element name
	char *text;      // error/warning message or structure name
	char *debug;     // error/warning debug, or structure as string for element messages
} EgMessage;
int eg_pop(GstElement *pipe, gint64 timeout_ns, int types, EgMessage *out);
void eg_message_free(EgMessage *m);

// Samples pulled by Go at low rates (JPEG thumbnails, stills): copied bytes or a held buffer
int eg_pull_bytes(GstElement *appsink, gint64 timeout_ns, guint8 **data, gsize *len);
GstBuffer *eg_pull_buffer(GstElement *appsink, gint64 timeout_ns, int *width, int *height);
void eg_buffer_unref(GstBuffer *b);

// --- the bridge: source appsink -> program appsrc (+ screen, + thumbnails) ---
typedef struct {
	guint64 frames;          // frames since the bridge was created
	gint64 last_frame_ns;    // monotonic time of the last frame (0 = none yet)
	// window since the last eg_bridge_take_timing
	guint64 n;
	gint64 age_min_ns, age_max_ns, age_sum_ns;  // capture -> arrival (ns)
	gint64 max_gap_ns;       // longest time between two arrivals
	int judge;               // capture times usable: -1 undecided, 0 no, 1 yes
	gint64 judge_min_ns;     // last decided window minimum
	gint64 lag_ns;           // minimum age since the last take (network cameras' backlog)
} EgBridgeStats;

// The tile's gate (MIXER.md §24): a source's frame goes to its tile if it is the first one that falls in a
// new frame of the screen, by its pts on the screen: pts + offset on the grid of fps frames a second from 0
// (offset = the tile's pad offset minus the phase of the compositor's frames).
// That is what the compositor uses to pick a pad's buffer for each frame it makes, so it gets exactly one
// per frame (a 60 or a 50 one, 30 a second), whatever the frames' arrival jitter. Without a pts (-1: the
// appsrc stamps it on arrival), by arrival: at most one every 3/4 of a screen frame. 1 = send it.
typedef struct {
	gint64 slot;      // the screen frame of the last one sent (G_MININT64: none yet)
	gint64 last_pts;  // its pts (-1: none), to place it again on another grid of the same screen
	gint64 last_mono; // its arrival (monotonic ns), for the frames without a pts
} EgMvGate;
void eg_mv_gate_reset(EgMvGate *g);
int eg_mv_gate_step(EgMvGate *g, int fps, gint64 offset_ns, gint64 pts, gint64 mono);
// The same screen with another grid (its phase, measured): the last frame sent keeps its screen frame, so the
// next one in it does not go through too
void eg_mv_gate_regrid(EgMvGate *g, int fps, gint64 offset_ns);
// A still went to the tile with this pts: its screen frame is taken (stills always go, the gate never stops them)
void eg_mv_gate_mark(EgMvGate *g, int fps, gint64 offset_ns, gint64 pts);

// CaptureJudge: are a source's capture times usable? The window minimum of the age (the closest to the
// real delay: hiccups only make it bigger), decided every 60 frames: usable if −5 ms ≤ min ≤ limit.
typedef struct {
	int n;           // ages in the current window
	gint64 min;      // their minimum (G_MAXINT64 when empty)
	int ok;          // -1 undecided, 0 no, 1 yes
	gint64 last_min; // the last decided window's minimum
} EgJudge;
int eg_judge_step(EgJudge *j, gint64 limit, gint64 age);
// When a buffer was captured, on the clock: its pts on its pipeline's timeline, minus how far the stamp is
// behind the capture (nvv4l2camerasrc: 2 frames), plus MONOTONIC − MONOTONIC_RAW for the Tegra CSI.
gint64 eg_capture_time(gint64 pts, gint64 base, gint64 early, gint64 raw_off);

typedef struct EgBridge EgBridge;
// kind: 0 = source from another pipeline (capture time, judged), 1 = same timeline (pts + extra),
// 2 = pass as is
EgBridge *eg_bridge_new(int kind, GstElement *pgm_pipe, GstElement *dst);
void eg_bridge_free(EgBridge *b);
void eg_bridge_params(EgBridge *b, gint64 sync_ns, gint64 extra_ns, gint64 early_ns, int raw_clock,
		      gint64 judge_limit_ns);
// The screen's grid for this bridge's tile: the compositor makes fps frames a second, frame k starting at
// k/fps once offset_ns is added to a tile's pts (its pad offset minus the grid's phase). fps 0 = no tile.
void eg_bridge_mv(EgBridge *b, GstElement *mv_dst, int fps, gint64 offset_ns);
void eg_bridge_thumb(EgBridge *b, GstElement *thumb_dst, gint64 every_ns);
void eg_bridge_every(EgBridge *b, int every);
void eg_bridge_dst(EgBridge *b, GstElement *dst);
void eg_bridge_attach(EgBridge *b, GstElement *appsink);
void eg_bridge_hold_if(EgBridge *b, GstElement *appsink, int hold);
void eg_bridge_detach(EgBridge *b);
// Detach only if appsink is still the bridge's input (a newer one may have attached since)
void eg_bridge_detach_if(EgBridge *b, GstElement *appsink);
void eg_bridge_push_still(EgBridge *b, GstBuffer *still);
void eg_bridge_stats(EgBridge *b, EgBridgeStats *out, int take_timing);

// --- counters ---
typedef struct EgCadence EgCadence;
EgCadence *eg_cadence_new(GstElement *appsink);
void eg_cadence_take(EgCadence *c, guint64 *all, guint64 *fresh);

typedef struct EgCounter EgCounter;
EgCounter *eg_counter_new(GstElement *el, const char *pad);
void eg_counter_take(EgCounter *c, guint64 *buffers, guint64 *bytes);
void eg_counter_free(EgCounter *c);

// Frames made in Go (the screen's UI layer) into an appsrc, through a pool of a few buffers: no
// allocation per frame. Push returns 0 when every buffer is still in use downstream (frame dropped).
typedef struct EgFeeder EgFeeder;
EgFeeder *eg_feeder_new(GstElement *appsrc, gsize size, guint buffers);
int eg_feeder_push(EgFeeder *f, const guint8 *data, gsize len);
void eg_feeder_free(EgFeeder *f);

// The headphones' tap on phtap: measures how late the program audio arrives (clock now − (base + pts),
// a window of samples) and passes each buffer untouched, by reference, to the current destination
// (none: measure and drop). It stays attached from creation.
typedef struct EgTap EgTap;
EgTap *eg_tap_new(GstElement *appsink);
void eg_tap_connect(EgTap *t, GstElement *appsrc);
gint64 eg_tap_take_late(EgTap *t);
void eg_tap_free(EgTap *t);

void eg_set_latency(GstElement *pipe, GstClockTime latency);
// A latency query on a pipeline: 1 and the minimum latency if it answers
int eg_query_latency(GstElement *pipe, GstClockTime *min);

// Current caps of a pad as a string (g_free), NULL if none
char *eg_caps_string(GstElement *el, const char *pad);
// rtspsrc: keep only the streams of one media (select-stream, answered in C)
int eg_select_stream(GstElement *rtspsrc, const char *media);

// VIDIOC_STREAMOFF on this process' own descriptors open on node (a stuck nvv4l2camerasrc)
int eg_streamoff(const char *node);

// The compositor's layers, set from its own streaming thread. A probe on its src pad applies, after each
// output frame, the step of the current table that belongs to the next frame: a fade gets one step per
// program frame, by the frame's timestamp, and every property of every pad changes between two frames
// (never halfway). A cut is a table of one step. The pads are sink_0 … sink_<npads-1>.
typedef struct {
	gint32 x, y, w, h, z;
	double alpha;
} EgLayer;
typedef struct EgAnim EgAnim;
EgAnim *eg_anim_new(GstElement *comp, int npads, gint64 frame_ns);
// steps × npads layers (copied); replaces whatever is playing. The probe after the next frame out of
// the compositor sets step 0 for the frame after it, and step k for each following frame by its time
void eg_anim_play(EgAnim *a, const EgLayer *steps, int nsteps);
// 1 while a table is still being played
int eg_anim_busy(EgAnim *a);
// Once the compositor's pipeline is in NULL
void eg_anim_free(EgAnim *a);
// A pad's offset (gst_pad_set_offset): its data is that much later on the running time; 0 if no such pad
int eg_set_pad_offset(GstElement *el, const char *pad, gint64 offset_ns);
// A probe on a pad that takes the duration off every buffer through it, if the buffer is writable (nobody
// else holds it); for the pad's life. 0 if no such pad
int eg_drop_duration(GstElement *el, const char *pad);

// What a compositor shows from each of its pads sink_0 … sink_<npads-1> (MIXER.md §24): a probe on its src
// pad reads, after each output frame, the buffer each pad used for it, and one on each sink pad counts what
// reaches it. Only on GStreamer 1.14's videoaggregator (nvcompositor): NULL on anything else.
typedef struct {
	guint64 in;    // buffers that reached the pad
	guint64 fresh; // output frames with a buffer the pad had not shown before
	gint32 hold;   // the longest run of output frames with the same buffer
} EgMeterPad;
typedef struct EgTileMeter EgTileMeter;
EgTileMeter *eg_tile_meter_new(GstElement *comp, int npads, int fps);
// The phase of the compositor's frames: they start at phase + k/fps of the running time. 0 = no frame yet
int eg_tile_meter_phase(EgTileMeter *m, gint64 *phase_ns);
// Since the previous take, which starts a new window: output frames, the phase (-1: none yet), its largest
// change between two frames (0: a steady grid), and npads EgMeterPad
void eg_tile_meter_take(EgTileMeter *m, guint64 *frames, gint64 *phase_ns, gint64 *jump_ns, EgMeterPad *pads);
// Once the compositor's pipeline is in NULL
void eg_tile_meter_free(EgTileMeter *m);

#endif
