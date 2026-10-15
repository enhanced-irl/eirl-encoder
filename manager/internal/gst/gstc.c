//go:build linux && cgo

#include "gstc.h"

#include <errno.h>
#include <stdio.h>
#include <dirent.h>
#include <limits.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <time.h>
#include <unistd.h>
#include <fcntl.h>
#include <linux/videodev2.h>

// --- generic helpers ---

GstElement *eg_parse(const char *desc, char **err)
{
	GError *e = NULL;
	GstElement *p = gst_parse_launch(desc, &e);
	if (e != NULL) {
		*err = g_strdup(e->message);
		g_error_free(e);
		if (p != NULL)
			gst_object_unref(p);
		return NULL;
	}
	*err = NULL;
	return p;
}

GstElement *eg_by_name(GstElement *bin, const char *name)
{
	return gst_bin_get_by_name(GST_BIN(bin), name);
}

int eg_set_state(GstElement *el, int state)
{
	return gst_element_set_state(el, (GstState)state);
}

int eg_get_state(GstElement *el, gint64 timeout_ns)
{
	GstState cur = GST_STATE_VOID_PENDING;
	if (gst_element_get_state(el, &cur, NULL, (GstClockTime)timeout_ns) == GST_STATE_CHANGE_FAILURE)
		return -1;
	return cur;
}

// The system clock and a fixed base_time: one pts is valid in every pipeline that gets the same base
// (the program, the screen, the output). Before PLAYING.
void eg_fix_base(GstElement *pipe, GstClockTime base)
{
	GstClock *clk = gst_system_clock_obtain();
	gst_pipeline_use_clock(GST_PIPELINE(pipe), clk);
	gst_object_unref(clk);
	gst_element_set_start_time(pipe, GST_CLOCK_TIME_NONE);
	gst_element_set_base_time(pipe, base);
}

GstClockTime eg_base_time(GstElement *el)
{
	return gst_element_get_base_time(el);
}

GstClockTime eg_now(void)
{
	GstClock *clk = gst_system_clock_obtain();
	GstClockTime t = gst_clock_get_time(clk);
	gst_object_unref(clk);
	return t;
}

// gst_util_set_object_arg converts the string to the property's type (ints, enums, flags, bools).
// 0 when the property does not exist.
int eg_set_arg(GstElement *el, const char *prop, const char *value)
{
	if (g_object_class_find_property(G_OBJECT_GET_CLASS(el), prop) == NULL)
		return 0;
	gst_util_set_object_arg(G_OBJECT(el), prop, value);
	return 1;
}

int eg_set_pad_arg(GstElement *el, const char *pad, const char *prop, const char *value)
{
	GstPad *p = gst_element_get_static_pad(el, pad);
	if (p == NULL)
		return 0;
	int ok = g_object_class_find_property(G_OBJECT_GET_CLASS(p), prop) != NULL;
	if (ok)
		gst_util_set_object_arg(G_OBJECT(p), prop, value);
	gst_object_unref(p);
	return ok;
}

void eg_set_bool(GstElement *el, const char *prop, int v)
{
	g_object_set(el, prop, (gboolean)(v != 0), NULL);
}

void eg_set_double(GstElement *el, const char *prop, double v)
{
	g_object_set(el, prop, v, NULL);
}

int eg_has_factory(const char *name)
{
	GstElementFactory *f = gst_element_factory_find(name);
	if (f == NULL)
		return 0;
	gst_object_unref(f);
	return 1;
}

const char *eg_name(GstElement *el)
{
	return GST_OBJECT_NAME(el);
}

int eg_pop(GstElement *pipe, gint64 timeout_ns, int types, EgMessage *out)
{
	GstBus *bus = gst_element_get_bus(pipe);
	GstMessage *msg = gst_bus_timed_pop_filtered(bus, (GstClockTime)timeout_ns, (GstMessageType)types);
	gst_object_unref(bus);
	if (msg == NULL)
		return 0;
	memset(out, 0, sizeof(*out));
	out->type = GST_MESSAGE_TYPE(msg);
	out->src = g_strdup(GST_MESSAGE_SRC(msg) ? GST_OBJECT_NAME(GST_MESSAGE_SRC(msg)) : "");
	GError *e = NULL;
	gchar *dbg = NULL;
	switch (GST_MESSAGE_TYPE(msg)) {
	case GST_MESSAGE_ERROR:
		gst_message_parse_error(msg, &e, &dbg);
		break;
	case GST_MESSAGE_WARNING:
		gst_message_parse_warning(msg, &e, &dbg);
		break;
	default: {
		const GstStructure *s = gst_message_get_structure(msg);
		if (s != NULL) {
			out->text = g_strdup(gst_structure_get_name(s));
			out->debug = gst_structure_to_string(s);
		}
	}
	}
	if (e != NULL) {
		out->text = g_strdup(e->message);
		g_error_free(e);
	}
	if (dbg != NULL)
		out->debug = dbg;
	gst_message_unref(msg);
	return 1;
}

void eg_message_free(EgMessage *m)
{
	g_free(m->src);
	g_free(m->text);
	g_free(m->debug);
	memset(m, 0, sizeof(*m));
}

int eg_pull_bytes(GstElement *appsink, gint64 timeout_ns, guint8 **data, gsize *len)
{
	GstSample *s = gst_app_sink_try_pull_sample(GST_APP_SINK(appsink), (GstClockTime)timeout_ns);
	if (s == NULL)
		return 0;
	GstMapInfo m;
	int ok = 0;
	GstBuffer *buf = gst_sample_get_buffer(s);
	if (buf != NULL && gst_buffer_map(buf, &m, GST_MAP_READ)) {
		*data = g_memdup(m.data, m.size);
		*len = m.size;
		gst_buffer_unmap(buf, &m);
		ok = 1;
	}
	gst_sample_unref(s);
	return ok;
}

// A still image for the program (slate, black, pause): kept by Go and pushed many times. Its pts is
// removed: the still pipeline's timestamps mean nothing in the program (MIXER.md §3, trap 21).
GstBuffer *eg_pull_buffer(GstElement *appsink, gint64 timeout_ns, int *width, int *height)
{
	GstSample *s = gst_app_sink_try_pull_sample(GST_APP_SINK(appsink), (GstClockTime)timeout_ns);
	if (s == NULL)
		return NULL;
	GstBuffer *buf = gst_sample_get_buffer(s);
	if (buf != NULL)
		buf = gst_buffer_ref(buf);
	GstCaps *caps = gst_sample_get_caps(s);
	if (caps != NULL && gst_caps_get_size(caps) > 0) {
		const GstStructure *st = gst_caps_get_structure(caps, 0);
		gst_structure_get_int(st, "width", width);
		gst_structure_get_int(st, "height", height);
	}
	gst_sample_unref(s);
	if (buf != NULL) {
		GST_BUFFER_PTS(buf) = GST_CLOCK_TIME_NONE;
		GST_BUFFER_DTS(buf) = GST_CLOCK_TIME_NONE;
	}
	return buf;
}

void eg_buffer_unref(GstBuffer *b)
{
	if (b != NULL)
		gst_buffer_unref(b);
}

// --- the bridge ---

#define JUDGE_WINDOW 60
#define MS ((gint64)GST_MSECOND)

struct EgBridge {
	int kind;
	GstElement *pgm_pipe; // its base_time is the common timeline
	GstElement *dst;
	GstClock *clock;
	GMutex lock;          // everything below; one frame at a time, and the stills from Go
	GstElement *current;  // the appsink whose frames count; the others are closing pipelines
	GstElement *mv_dst, *thumb_dst;
	gint64 mv_gap_ns, thumb_every_ns, mv_last, thumb_last;
	gint64 sync_ns, extra_ns, early_ns, judge_limit_ns;
	int raw_clock;
	gint64 last_pts;      // never backwards, across reopenings of the source (Engine.stamp)
	gint64 prev_arrival;
	int judge_n;
	gint64 judge_min;
	int every;            // pass bridges: one frame of every `every` (the output at half the rate); ≤ 1 = all
	EgBridgeStats st;
};

static void timing_reset(EgBridgeStats *s)
{
	s->n = 0;
	s->age_min_ns = G_MAXINT64;
	s->age_max_ns = G_MININT64;
	s->age_sum_ns = 0;
	s->max_gap_ns = 0;
}

EgBridge *eg_bridge_new(int kind, GstElement *pgm_pipe, GstElement *dst)
{
	EgBridge *b = g_new0(EgBridge, 1);
	b->kind = kind;
	b->pgm_pipe = gst_object_ref(pgm_pipe);
	b->dst = dst ? gst_object_ref(dst) : NULL;
	b->clock = gst_system_clock_obtain();
	g_mutex_init(&b->lock);
	b->last_pts = -1;
	b->prev_arrival = -1;
	b->judge_min = G_MAXINT64;
	b->judge_limit_ns = 150 * MS;
	b->mv_last = b->thumb_last = G_MININT64 / 2;
	b->st.judge = -1;
	b->st.lag_ns = G_MAXINT64;
	timing_reset(&b->st);
	return b;
}

// Only once every pipeline that could call back into it is in NULL (bridges live as long as their slot)
void eg_bridge_free(EgBridge *b)
{
	if (b->dst)
		gst_object_unref(b->dst);
	if (b->mv_dst)
		gst_object_unref(b->mv_dst);
	if (b->thumb_dst)
		gst_object_unref(b->thumb_dst);
	if (b->current)
		gst_object_unref(b->current);
	gst_object_unref(b->pgm_pipe);
	gst_object_unref(b->clock);
	g_mutex_clear(&b->lock);
	g_free(b);
}

void eg_bridge_params(EgBridge *b, gint64 sync_ns, gint64 extra_ns, gint64 early_ns, int raw_clock,
		      gint64 judge_limit_ns)
{
	g_mutex_lock(&b->lock);
	b->sync_ns = sync_ns;
	b->extra_ns = extra_ns;
	b->early_ns = early_ns;
	b->raw_clock = raw_clock;
	b->judge_limit_ns = judge_limit_ns > 0 ? judge_limit_ns : 150 * MS;
	g_mutex_unlock(&b->lock);
}

static void swap_ref(GstElement **slot, GstElement *el)
{
	if (*slot)
		gst_object_unref(*slot);
	*slot = el ? gst_object_ref(el) : NULL;
}

void eg_bridge_mv(EgBridge *b, GstElement *mv_dst, gint64 gap_ns)
{
	g_mutex_lock(&b->lock);
	swap_ref(&b->mv_dst, mv_dst);
	b->mv_gap_ns = gap_ns;
	g_mutex_unlock(&b->lock);
}

void eg_bridge_every(EgBridge *b, int every)
{
	g_mutex_lock(&b->lock);
	b->every = every;
	g_mutex_unlock(&b->lock);
}

void eg_bridge_thumb(EgBridge *b, GstElement *thumb_dst, gint64 every_ns)
{
	g_mutex_lock(&b->lock);
	swap_ref(&b->thumb_dst, thumb_dst);
	b->thumb_every_ns = every_ns;
	g_mutex_unlock(&b->lock);
}

static gint64 mono_ns(void)
{
	return g_get_monotonic_time() * 1000;
}

// CLOCK_MONOTONIC - CLOCK_MONOTONIC_RAW: the Tegra CSI driver stamps with the RAW clock and v4l2src
// takes it for the monotonic one (MIXER.md §3, trap 24)
static gint64 raw_offset(void)
{
	struct timespec a, r;
	clock_gettime(CLOCK_MONOTONIC, &a);
	clock_gettime(CLOCK_MONOTONIC_RAW, &r);
	return ((gint64)a.tv_sec - r.tv_sec) * 1000000000 + (a.tv_nsec - r.tv_nsec);
}

// CaptureJudge: are this source's capture times usable? Window minimum of the age (the closest to the
// real delay: hiccups only make it bigger), decided every JUDGE_WINDOW frames.
int eg_judge_step(EgJudge *j, gint64 limit, gint64 age)
{
	if (age < j->min)
		j->min = age;
	if (++j->n >= JUDGE_WINDOW) {
		j->ok = j->min >= -5 * MS && j->min <= limit;
		j->last_min = j->min;
		j->n = 0;
		j->min = G_MAXINT64;
	}
	return j->ok == 1;
}

static int judge_add(EgBridge *b, gint64 age)
{
	EgJudge j = {b->judge_n, b->judge_min, b->st.judge, b->st.judge_min_ns};
	int ok = eg_judge_step(&j, b->judge_limit_ns, age);
	b->judge_n = j.n;
	b->judge_min = j.min;
	b->st.judge = j.ok;
	b->st.judge_min_ns = j.last_min;
	return ok;
}

gint64 eg_capture_time(gint64 pts, gint64 base, gint64 early, gint64 raw_off)
{
	return pts + base - early + raw_off;
}

// The program pts for a buffer captured at `at` (or now). Never backwards: the compositor keeps the
// newest, so a repeated pts would drop the frame. -1 = no pts (the appsrc stamps it on arrival).
static gint64 next_pts(EgBridge *b, gint64 at, gint64 extra)
{
	if (b->sync_ns == 0 && b->kind == 0)
		return -1;
	gint64 pts = at - (gint64)gst_element_get_base_time(b->pgm_pipe) + extra;
	if (b->last_pts >= 0 && pts <= b->last_pts)
		pts = b->last_pts + MS;
	b->last_pts = pts;
	return pts;
}

static void set_pts(GstBuffer *buf, gint64 pts)
{
	GST_BUFFER_PTS(buf) = pts < 0 ? GST_CLOCK_TIME_NONE : (GstClockTime)pts;
	GST_BUFFER_DTS(buf) = GST_CLOCK_TIME_NONE;
}

static void timing_add(EgBridge *b, gint64 now, gboolean have_age, gint64 age)
{
	if (b->prev_arrival >= 0 && now - b->prev_arrival > b->st.max_gap_ns)
		b->st.max_gap_ns = now - b->prev_arrival;
	b->prev_arrival = now;
	if (!have_age)
		return;
	b->st.n++;
	b->st.age_sum_ns += age;
	if (age < b->st.age_min_ns)
		b->st.age_min_ns = age;
	if (age > b->st.age_max_ns)
		b->st.age_max_ns = age;
	if (age < b->st.lag_ns)
		b->st.lag_ns = age;
}

static GstFlowReturn on_sample(GstAppSink *snk, gpointer data)
{
	EgBridge *b = data;
	GstSample *s = gst_app_sink_pull_sample(snk);
	if (s == NULL)
		return GST_FLOW_FLUSHING;
	GstBuffer *buf = gst_sample_get_buffer(s);
	if (buf == NULL) {
		gst_sample_unref(s);
		return GST_FLOW_OK;
	}
	buf = gst_buffer_ref(buf);
	gst_sample_unref(s);

	g_mutex_lock(&b->lock);
	if ((GstElement *)snk != b->current || b->dst == NULL) {
		// From a pipeline that is closing, or nowhere to go
		g_mutex_unlock(&b->lock);
		gst_buffer_unref(buf);
		return GST_FLOW_OK;
	}
	gint64 now = (gint64)gst_clock_get_time(b->clock);
	b->st.frames++;
	b->st.last_frame_ns = mono_ns();

	if (b->kind == 2) {
		timing_add(b, now, FALSE, 0);
		// Half the rate (OUTPUTS.md §7) here and not with videorate in the output: its frames keep the
		// program's timestamps, minutes past the output's segment start, and videorate in drop-only mode
		// never catches up with them, so it let every frame through
		if (b->every > 1 && b->st.frames % (guint64)b->every != 0) {
			g_mutex_unlock(&b->lock);
			gst_buffer_unref(buf);
			return GST_FLOW_OK;
		}
		gst_app_src_push_buffer(GST_APP_SRC(b->dst), buf);
		g_mutex_unlock(&b->lock);
		return GST_FLOW_OK;
	}

	gint64 at = now;
	if (GST_BUFFER_PTS_IS_VALID(buf)) {
		gint64 base = (gint64)gst_element_get_base_time(GST_ELEMENT(snk));
		gint64 cap = eg_capture_time((gint64)GST_BUFFER_PTS(buf), base, b->early_ns, b->raw_clock ? raw_offset() : 0);
		gint64 age = now - cap;
		timing_add(b, now, TRUE, age);
		if (b->kind == 1) {
			if (b->sync_ns != 0)
				at = cap; // same timeline: the pts is already right
		} else if (b->sync_ns != 0 && judge_add(b, age)) {
			at = cap;
		}
	} else {
		timing_add(b, now, FALSE, 0);
	}
	gint64 extra = b->kind == 0 ? b->sync_ns + b->extra_ns : b->extra_ns;
	// The pts goes on the buffer itself: a copy would share its NVMM memory and the original would go
	// back to its pool while the copy is still queued (MIXER.md §3, trap 23)
	set_pts(buf, next_pts(b, at, extra));

	GstBuffer *mv = NULL, *th = NULL;
	gint64 mono = b->st.last_frame_ns;
	if (b->mv_dst != NULL && mono - b->mv_last >= b->mv_gap_ns) {
		b->mv_last = mono;
		mv = gst_buffer_ref(buf);
	}
	if (b->thumb_dst != NULL && b->thumb_every_ns > 0 && mono - b->thumb_last >= b->thumb_every_ns) {
		b->thumb_last = mono;
		th = gst_buffer_ref(buf);
	}
	gst_app_src_push_buffer(GST_APP_SRC(b->dst), buf);
	if (mv != NULL)
		gst_app_src_push_buffer(GST_APP_SRC(b->mv_dst), mv);
	if (th != NULL)
		gst_app_src_push_buffer(GST_APP_SRC(b->thumb_dst), th);
	g_mutex_unlock(&b->lock);
	return GST_FLOW_OK;
}

// Another destination from now on (the hot output's appsrc of each session); NULL = drop
void eg_bridge_dst(EgBridge *b, GstElement *dst)
{
	g_mutex_lock(&b->lock);
	swap_ref(&b->dst, dst);
	g_mutex_unlock(&b->lock);
}

// A new pipeline of this source: its frames count from now, with a fresh judge (its timestamps may
// have changed) and a fresh lag
void eg_bridge_attach(EgBridge *b, GstElement *appsink)
{
	g_mutex_lock(&b->lock);
	swap_ref(&b->current, appsink);
	b->judge_n = 0;
	b->judge_min = G_MAXINT64;
	b->st.judge = -1;
	b->st.lag_ns = G_MAXINT64;
	b->prev_arrival = -1;
	g_mutex_unlock(&b->lock);
	GstAppSinkCallbacks cb;
	memset(&cb, 0, sizeof(cb));
	cb.new_sample = on_sample;
	gst_app_sink_set_callbacks(GST_APP_SINK(appsink), &cb, b, NULL);
}

void eg_bridge_detach(EgBridge *b)
{
	g_mutex_lock(&b->lock);
	swap_ref(&b->current, NULL);
	g_mutex_unlock(&b->lock);
}

void eg_bridge_detach_if(EgBridge *b, GstElement *appsink)
{
	g_mutex_lock(&b->lock);
	if (b->current == appsink)
		swap_ref(&b->current, NULL);
	g_mutex_unlock(&b->lock);
}

// A still (slate, black, pause): a shallow copy with its own pts (not a pool buffer, so sharing its
// memory is fine), to the program and always to the screen
void eg_bridge_push_still(EgBridge *b, GstBuffer *still)
{
	if (still == NULL)
		return;
	g_mutex_lock(&b->lock);
	if (b->dst == NULL) {
		g_mutex_unlock(&b->lock);
		return;
	}
	GstBuffer *c = gst_buffer_copy(still);
	gint64 extra = b->kind == 0 ? b->sync_ns + b->extra_ns : b->extra_ns;
	gint64 pts = b->sync_ns == 0 && b->kind == 0 ? -1 : next_pts(b, (gint64)gst_clock_get_time(b->clock), extra);
	set_pts(c, pts);
	GstBuffer *mv = b->mv_dst != NULL ? gst_buffer_ref(c) : NULL;
	gst_app_src_push_buffer(GST_APP_SRC(b->dst), c);
	if (mv != NULL)
		gst_app_src_push_buffer(GST_APP_SRC(b->mv_dst), mv);
	g_mutex_unlock(&b->lock);
}

// take: bit 0 = start a new timing window, bit 1 = start a new lag measure
void eg_bridge_stats(EgBridge *b, EgBridgeStats *out, int take)
{
	g_mutex_lock(&b->lock);
	*out = b->st;
	if (take & 1)
		timing_reset(&b->st);
	if (take & 2)
		b->st.lag_ns = G_MAXINT64;
	g_mutex_unlock(&b->lock);
}

// --- counters ---

struct EgCadence {
	GMutex lock;
	guint8 *prev;
	gsize prev_len;
	guint64 all, fresh;
};

// New frames of the program: the compositor repeats a frame byte by byte (GRAY8 160x90)
static GstFlowReturn on_cadence(GstAppSink *snk, gpointer data)
{
	EgCadence *c = data;
	GstSample *s = gst_app_sink_pull_sample(snk);
	if (s == NULL)
		return GST_FLOW_FLUSHING;
	GstMapInfo m;
	GstBuffer *buf = gst_sample_get_buffer(s);
	if (buf != NULL && gst_buffer_map(buf, &m, GST_MAP_READ)) {
		g_mutex_lock(&c->lock);
		gboolean fresh = c->prev == NULL || c->prev_len != m.size || memcmp(c->prev, m.data, m.size) != 0;
		if (c->prev_len != m.size) {
			g_free(c->prev);
			c->prev = g_malloc(m.size);
			c->prev_len = m.size;
		}
		memcpy(c->prev, m.data, m.size);
		c->all++;
		c->fresh += fresh;
		g_mutex_unlock(&c->lock);
		gst_buffer_unmap(buf, &m);
	}
	gst_sample_unref(s);
	return GST_FLOW_OK;
}

EgCadence *eg_cadence_new(GstElement *appsink)
{
	EgCadence *c = g_new0(EgCadence, 1);
	g_mutex_init(&c->lock);
	GstAppSinkCallbacks cb;
	memset(&cb, 0, sizeof(cb));
	cb.new_sample = on_cadence;
	gst_app_sink_set_callbacks(GST_APP_SINK(appsink), &cb, c, NULL);
	return c;
}

void eg_cadence_take(EgCadence *c, guint64 *all, guint64 *fresh)
{
	g_mutex_lock(&c->lock);
	*all = c->all;
	*fresh = c->fresh;
	c->all = c->fresh = 0;
	g_mutex_unlock(&c->lock);
}

struct EgCounter {
	GMutex lock;
	guint64 buffers, bytes;
	GstPad *pad;
	gulong probe;
};

static GstPadProbeReturn on_count(GstPad *pad, GstPadProbeInfo *info, gpointer data)
{
	EgCounter *c = data;
	GstBuffer *buf = GST_PAD_PROBE_INFO_BUFFER(info);
	if (buf != NULL) {
		g_mutex_lock(&c->lock);
		c->buffers++;
		c->bytes += gst_buffer_get_size(buf);
		g_mutex_unlock(&c->lock);
	}
	return GST_PAD_PROBE_OK;
}

EgCounter *eg_counter_new(GstElement *el, const char *pad)
{
	GstPad *p = gst_element_get_static_pad(el, pad);
	if (p == NULL)
		return NULL;
	EgCounter *c = g_new0(EgCounter, 1);
	g_mutex_init(&c->lock);
	c->pad = p; // keeps the reference
	c->probe = gst_pad_add_probe(p, GST_PAD_PROBE_TYPE_BUFFER, on_count, c, NULL);
	return c;
}

// Once its pipeline is in NULL (no callback can be running any more)
void eg_counter_free(EgCounter *c)
{
	gst_pad_remove_probe(c->pad, c->probe);
	gst_object_unref(c->pad);
	g_mutex_clear(&c->lock);
	g_free(c);
}

void eg_counter_take(EgCounter *c, guint64 *buffers, guint64 *bytes)
{
	g_mutex_lock(&c->lock);
	*buffers = c->buffers;
	*bytes = c->bytes;
	c->buffers = c->bytes = 0;
	g_mutex_unlock(&c->lock);
}

struct EgFeeder {
	GstElement *appsrc;
	GstBufferPool *pool;
	gsize size;
};

EgFeeder *eg_feeder_new(GstElement *appsrc, gsize size, guint buffers)
{
	EgFeeder *f = g_new0(EgFeeder, 1);
	f->appsrc = gst_object_ref(appsrc);
	f->size = size;
	f->pool = gst_buffer_pool_new();
	GstStructure *cfg = gst_buffer_pool_get_config(f->pool);
	gst_buffer_pool_config_set_params(cfg, NULL, (guint)size, buffers, buffers);
	if (!gst_buffer_pool_set_config(f->pool, cfg) || !gst_buffer_pool_set_active(f->pool, TRUE)) {
		gst_object_unref(f->pool);
		f->pool = NULL;
	}
	return f;
}

int eg_feeder_push(EgFeeder *f, const guint8 *data, gsize len)
{
	if (f->pool == NULL || len != f->size)
		return 0;
	GstBufferPoolAcquireParams params = {0};
	params.flags = GST_BUFFER_POOL_ACQUIRE_FLAG_DONTWAIT;
	GstBuffer *buf = NULL;
	if (gst_buffer_pool_acquire_buffer(f->pool, &buf, &params) != GST_FLOW_OK || buf == NULL)
		return 0;
	gst_buffer_fill(buf, 0, data, len);
	GST_BUFFER_PTS(buf) = GST_CLOCK_TIME_NONE;
	GST_BUFFER_DTS(buf) = GST_CLOCK_TIME_NONE;
	return gst_app_src_push_buffer(GST_APP_SRC(f->appsrc), buf) == GST_FLOW_OK;
}

void eg_feeder_free(EgFeeder *f)
{
	if (f->pool != NULL) {
		gst_buffer_pool_set_active(f->pool, FALSE);
		gst_object_unref(f->pool);
	}
	gst_object_unref(f->appsrc);
	g_free(f);
}

#define TAP_SAMPLES 400

struct EgTap {
	GstElement *appsink;
	GstElement *dst;
	GstClock *clock;
	GMutex lock;
	gint64 late[TAP_SAMPLES];
	guint n;     // samples in the window (≤ TAP_SAMPLES)
	guint64 seq; // all samples seen in the window: the ring keeps the newest
};

static GstFlowReturn on_tap(GstAppSink *snk, gpointer data)
{
	EgTap *t = data;
	GstSample *s = gst_app_sink_pull_sample(snk);
	if (s == NULL)
		return GST_FLOW_FLUSHING;
	GstBuffer *buf = gst_sample_get_buffer(s);
	if (buf == NULL) {
		gst_sample_unref(s);
		return GST_FLOW_OK;
	}
	buf = gst_buffer_ref(buf);
	gst_sample_unref(s);
	g_mutex_lock(&t->lock);
	if (GST_BUFFER_PTS_IS_VALID(buf)) {
		gint64 now = (gint64)gst_clock_get_time(t->clock);
		gint64 late = now - ((gint64)gst_element_get_base_time(GST_ELEMENT(snk)) + (gint64)GST_BUFFER_PTS(buf));
		t->late[t->seq % TAP_SAMPLES] = late;
		t->seq++;
		if (t->n < TAP_SAMPLES)
			t->n++;
	}
	GstElement *dst = t->dst ? gst_object_ref(t->dst) : NULL;
	g_mutex_unlock(&t->lock);
	if (dst != NULL) {
		gst_app_src_push_buffer(GST_APP_SRC(dst), buf); // untouched: the tee shares it with the output
		gst_object_unref(dst);
	} else {
		gst_buffer_unref(buf);
	}
	return GST_FLOW_OK;
}

EgTap *eg_tap_new(GstElement *appsink)
{
	EgTap *t = g_new0(EgTap, 1);
	t->appsink = gst_object_ref(appsink);
	t->clock = gst_system_clock_obtain();
	g_mutex_init(&t->lock);
	GstAppSinkCallbacks cb;
	memset(&cb, 0, sizeof(cb));
	cb.new_sample = on_tap;
	gst_app_sink_set_callbacks(GST_APP_SINK(appsink), &cb, t, NULL);
	return t;
}

void eg_tap_connect(EgTap *t, GstElement *appsrc)
{
	g_mutex_lock(&t->lock);
	swap_ref(&t->dst, appsrc);
	g_mutex_unlock(&t->lock);
}

static int cmp_i64(const void *a, const void *b)
{
	gint64 x = *(const gint64 *)a, y = *(const gint64 *)b;
	return (x > y) - (x < y);
}

// P90 of the window (the newest TAP_SAMPLES), and a new window. 0 without samples.
gint64 eg_tap_take_late(EgTap *t)
{
	gint64 v[TAP_SAMPLES];
	g_mutex_lock(&t->lock);
	guint n = t->n;
	memcpy(v, t->late, sizeof(gint64) * n);
	t->n = 0;
	t->seq = 0;
	g_mutex_unlock(&t->lock);
	if (n == 0)
		return 0;
	qsort(v, n, sizeof(gint64), cmp_i64);
	guint i = (guint)(n * 0.9);
	if (i > n - 1)
		i = n - 1;
	return v[i];
}

void eg_tap_free(EgTap *t)
{
	if (t->dst)
		gst_object_unref(t->dst);
	gst_object_unref(t->appsink);
	gst_object_unref(t->clock);
	g_mutex_clear(&t->lock);
	g_free(t);
}

int eg_query_latency(GstElement *pipe, GstClockTime *min)
{
	GstQuery *q = gst_query_new_latency();
	gboolean live = FALSE;
	GstClockTime lo = 0, hi = 0;
	int ok = gst_element_query(pipe, q);
	if (ok)
		gst_query_parse_latency(q, &live, &lo, &hi);
	gst_query_unref(q);
	*min = lo;
	return ok;
}

void eg_set_latency(GstElement *pipe, GstClockTime latency)
{
	gst_pipeline_set_latency(GST_PIPELINE(pipe), latency);
}

char *eg_caps_string(GstElement *el, const char *pad)
{
	GstPad *p = gst_element_get_static_pad(el, pad);
	if (p == NULL)
		return NULL;
	GstCaps *caps = gst_pad_get_current_caps(p);
	gst_object_unref(p);
	if (caps == NULL)
		return NULL;
	char *s = gst_caps_to_string(caps);
	gst_caps_unref(caps);
	return s;
}

// The video pipeline of a network camera must not get its audio (nor leave a pad unlinked), and the
// other way round: rtspsrc asks for each stream (engine_net.want_stream)
static gboolean on_select_stream(GstElement *src, guint num, GstCaps *caps, gpointer data)
{
	const char *want = data;
	char *s = gst_caps_to_string(caps);
	gboolean ok = s != NULL && strstr(s, want) != NULL;
	g_free(s);
	return ok;
}

int eg_select_stream(GstElement *rtspsrc, const char *media)
{
	if (g_signal_lookup("select-stream", G_OBJECT_TYPE(rtspsrc)) == 0)
		return 0;
	char *want = g_strdup_printf("media=(string)%s", media);
	g_signal_connect_data(rtspsrc, "select-stream", G_CALLBACK(on_select_stream), want,
			      (GClosureNotify)g_free, 0);
	return 1;
}

// nvv4l2camerasrc does not unblock when stopped: if the signal is no longer the one it opened, it
// waits for a frame in the driver and set_state(NULL) never returns. STREAMOFF wakes that wait with an
// error; on a descriptor that is not the capture one the driver refuses it and nothing happens.
// Returns on how many descriptors it worked.
int eg_streamoff(const char *node)
{
	char real[PATH_MAX], link[PATH_MAX], path[64];
	if (realpath(node, real) == NULL)
		return 0;
	DIR *d = opendir("/proc/self/fd");
	if (d == NULL)
		return 0;
	int n = 0;
	struct dirent *e;
	while ((e = readdir(d)) != NULL) {
		int fd = atoi(e->d_name);
		if (e->d_name[0] < '0' || e->d_name[0] > '9' || fd == dirfd(d))
			continue;
		snprintf(path, sizeof(path), "/proc/self/fd/%d", fd);
		ssize_t l = readlink(path, link, sizeof(link) - 1);
		if (l <= 0)
			continue;
		link[l] = 0;
		if (strcmp(link, real) != 0)
			continue;
		int type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
		if (ioctl(fd, VIDIOC_STREAMOFF, &type) == 0)
			n++;
	}
	closedir(d);
	return n;
}

// --- the compositor's layers ---

struct EgAnim {
	GMutex lock;          // everything below: the probe (the compositor's thread) and eg_anim_play (Go)
	GstElement *comp;
	GstPad *src;
	gulong probe;
	int npads;
	GstPad **pads;        // sink_0 … (NULL if missing)
	EgLayer *cur;         // what each pad has, once have_cur
	gboolean have_cur;
	gint64 frame_ns;
	EgLayer *steps;       // the table being played (NULL: none)
	int nsteps;
	gint64 start;         // pts of the frame that gets step 0; -1 = the next one
};

static void anim_apply(EgAnim *a, const EgLayer *l)
{
	for (int i = 0; i < a->npads; i++) {
		GstPad *p = a->pads[i];
		if (p == NULL)
			continue;
		const EgLayer *n = &l[i];
		EgLayer *c = &a->cur[i];
		gboolean all = !a->have_cur;
		if (all || n->x != c->x)
			g_object_set(p, "xpos", (gint)n->x, NULL);
		if (all || n->y != c->y)
			g_object_set(p, "ypos", (gint)n->y, NULL);
		if (all || n->w != c->w)
			g_object_set(p, "width", (gint)n->w, NULL);
		if (all || n->h != c->h)
			g_object_set(p, "height", (gint)n->h, NULL);
		if (all || n->alpha != c->alpha)
			g_object_set(p, "alpha", n->alpha, NULL);
		if (all || n->z != c->z)
			g_object_set(p, "zorder", (guint)n->z, NULL);
		*c = *n;
	}
	a->have_cur = TRUE;
}

static GstPadProbeReturn on_anim(GstPad *pad, GstPadProbeInfo *info, gpointer data)
{
	EgAnim *a = data;
	GstBuffer *buf = GST_PAD_PROBE_INFO_BUFFER(info);
	if (buf == NULL || !GST_BUFFER_PTS_IS_VALID(buf))
		return GST_PAD_PROBE_OK;
	gint64 dur = GST_BUFFER_DURATION_IS_VALID(buf) ? (gint64)GST_BUFFER_DURATION(buf) : a->frame_ns;
	gint64 next = (gint64)GST_BUFFER_PTS(buf) + dur;
	g_mutex_lock(&a->lock);
	if (a->steps != NULL) {
		if (a->start < 0)
			a->start = next;
		// by the frame's time: a frame the compositor skips skips its step too
		gint64 k = (next - a->start + a->frame_ns / 2) / a->frame_ns;
		if (k >= a->nsteps - 1) {
			anim_apply(a, a->steps + (gsize)(a->nsteps - 1) * a->npads);
			g_free(a->steps);
			a->steps = NULL;
		} else if (k >= 0) {
			anim_apply(a, a->steps + (gsize)k * a->npads);
		}
	}
	g_mutex_unlock(&a->lock);
	return GST_PAD_PROBE_OK;
}

EgAnim *eg_anim_new(GstElement *comp, int npads, gint64 frame_ns)
{
	EgAnim *a = g_new0(EgAnim, 1);
	g_mutex_init(&a->lock);
	a->comp = gst_object_ref(comp);
	a->npads = npads;
	a->pads = g_new0(GstPad *, npads);
	a->cur = g_new0(EgLayer, npads);
	a->frame_ns = frame_ns > 0 ? frame_ns : GST_SECOND / 60;
	a->start = -1;
	for (int i = 0; i < npads; i++) {
		char name[32];
		snprintf(name, sizeof(name), "sink_%d", i);
		a->pads[i] = gst_element_get_static_pad(comp, name);
	}
	a->src = gst_element_get_static_pad(comp, "src");
	if (a->src != NULL)
		a->probe = gst_pad_add_probe(a->src, GST_PAD_PROBE_TYPE_BUFFER, on_anim, a, NULL);
	return a;
}

void eg_anim_play(EgAnim *a, const EgLayer *steps, int nsteps)
{
	if (nsteps <= 0)
		return;
	EgLayer *copy = g_memdup(steps, (guint)((gsize)nsteps * a->npads * sizeof(EgLayer)));
	g_mutex_lock(&a->lock);
	g_free(a->steps);
	a->steps = copy;
	a->nsteps = nsteps;
	a->start = -1;
	g_mutex_unlock(&a->lock);
}

int eg_anim_busy(EgAnim *a)
{
	g_mutex_lock(&a->lock);
	int busy = a->steps != NULL;
	g_mutex_unlock(&a->lock);
	return busy;
}

void eg_anim_free(EgAnim *a)
{
	if (a->src != NULL) {
		if (a->probe != 0)
			gst_pad_remove_probe(a->src, a->probe);
		gst_object_unref(a->src);
	}
	for (int i = 0; i < a->npads; i++)
		if (a->pads[i] != NULL)
			gst_object_unref(a->pads[i]);
	g_free(a->pads);
	g_free(a->cur);
	g_free(a->steps);
	gst_object_unref(a->comp);
	g_mutex_clear(&a->lock);
	g_free(a);
}

int eg_set_pad_offset(GstElement *el, const char *pad, gint64 offset_ns)
{
	GstPad *p = gst_element_get_static_pad(el, pad);
	if (p == NULL)
		return 0;
	gst_pad_set_offset(p, offset_ns);
	gst_object_unref(p);
	return 1;
}
