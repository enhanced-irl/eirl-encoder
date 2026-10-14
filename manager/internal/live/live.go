// Package live is the WebRTC live preview. The engine sends over RTP to localhost a small copy
// of what goes on air (H.264 Baseline 640x360, hardware encoded, and the audio in Opus); the app
// forwards those packets untouched to the browsers that are watching. Delay of ~0.2–0.4 s, versus
// the 3–6 s of HLS.
//
// Like the VU meter, the RTP sockets are only open while someone is watching: with nobody, the
// kernel drops the datagrams and the app does not wake up. Local network only: ICE lite and host
// candidates, no STUN/TURN (the browser connects to the Jetson's IP and the app answers).
//
// Sync: the browser aligns audio and video with the Sender Reports (RTP ↔ wall-clock pairs). pion's
// use the ARRIVAL time of each packet, and the audio arrives later than the video relative to its
// capture (the script's mixer adds 100 ms): it would drift out of sync. Here they are computed from
// the RTP timestamps, which in the script come from the same pipeline clock (timestamp-offset=0):
// the same sync as the SRT output.
package live

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

var (
	ErrBusy     = errors.New("too many previews open")
	ErrBadOffer = errors.New("invalid WebRTC offer")
)

const (
	connectTimeout = 15 * time.Second // from the answer to the established connection
	checkEvery     = 20 * time.Second // like the SSE: a closed session also cuts the preview
	reportEvery    = time.Second
)

// Relay fans the local RTP out to everyone watching (one shared video track and one shared audio track).
type Relay struct {
	VideoAddr  string // 127.0.0.1:5006 (H.264)
	AudioAddr  string // 127.0.0.1:5008 (Opus)
	MaxViewers int
	Log        *slog.Logger

	api    *webrtc.API
	tracks [2]*track // video, audio

	mu       sync.Mutex
	viewers  map[*webrtc.PeerConnection]*viewer
	changed  chan struct{} // signals (without blocking) when someone arrives or leaves: Changed
	cancel   context.CancelFunc
	reportMu sync.Mutex // two runs may overlap for an instant (one leaves and another arrives)
	aligned  [2]int     // generation of each track already aligned
	halfway  time.Time  // since when only one track is on a new generation
}

type viewer struct {
	key     string // web session (see Answer)
	senders [2]*webrtc.RTPSender
}

// track: one track and what its Sender Report needs.
type track struct {
	local *webrtc.TrackLocalStaticRTP
	rate  float64
	step  uint32 // one frame (video at 30 fps) or 20 ms of Opus: continuity after a restart

	mu      sync.Mutex
	rw      rewriter
	ext     int64     // SOURCE RTP timestamp extended to 64 bits (does not wrap at 13 h)
	outTS   uint32    // the same packet, already rewritten
	arrival time.Time // when it arrived
	packets uint32
	octets  uint32
	seen    bool
	gen     int // goes up with each script start (new SSRC)
}

func New(videoAddr, audioAddr string, maxViewers int, log *slog.Logger) (*Relay, error) {
	m := &webrtc.MediaEngine{}
	// What the script sends: H.264 Baseline in FU-A (packetization-mode=1) and stereo Opus
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1"},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	// NACK: resends what is lost over Wi-Fi. Without the reports interceptor (see above).
	ir := &interceptor.Registry{}
	if err := webrtc.ConfigureNack(m, ir); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.SetLite(true)
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	se.SetInterfaceFilter(func(name string) bool {
		// Internal bridges (docker, L4T's USB): the browser cannot reach them
		return !strings.HasPrefix(name, "docker") && !strings.HasPrefix(name, "veth") && !strings.HasPrefix(name, "l4tbr")
	})
	se.SetICETimeouts(5*time.Second, 10*time.Second, 2*time.Second)
	// Fixed ports: the cameras' access point firewall only opens these
	// (NETWORK_SOURCES.md §6.2)
	if err := se.SetEphemeralUDPPortRange(netcfg.PreviewPortMin, netcfg.PreviewPortMax); err != nil {
		return nil, err
	}

	video, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "eirl")
	if err != nil {
		return nil, err
	}
	audio, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "eirl")
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Relay{
		VideoAddr: videoAddr, AudioAddr: audioAddr, MaxViewers: maxViewers, Log: log,
		api:     webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se)),
		tracks:  [2]*track{{local: video, rate: 90000, step: 3000}, {local: audio, rate: 48000, step: 960}},
		viewers: map[*webrtc.PeerConnection]*viewer{},
		changed: make(chan struct{}, 1),
	}, nil
}

// Changed: one signal each time the number of viewers changes (those arriving together are merged into one).
func (r *Relay) Changed() <-chan struct{} { return r.changed }

func (r *Relay) notify() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// Viewers: how many are watching now.
func (r *Relay) Viewers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.viewers)
}

// Answer accepts a browser's SDP offer and returns the answer (no trickle: with ICE lite the
// browser's candidates are not needed and ours are known at once). key groups by web session: a new
// offer from the same session replaces the previous one (when hiding and showing the tab again, or
// reconnecting after a Wi-Fi drop, the old one takes ~5 s to go away and would otherwise take a
// slot). alive is checked every 20 s: if the web session is no longer valid, it is cut.
func (r *Relay) Answer(key, offer string, alive func() bool) (string, error) {
	if !strings.HasPrefix(offer, "v=0") || len(offer) > 64<<10 {
		return "", ErrBadOffer
	}
	r.mu.Lock()
	var old []*webrtc.PeerConnection
	for pc, vw := range r.viewers {
		if vw.key == key {
			old = append(old, pc)
		}
	}
	r.mu.Unlock()
	for _, pc := range old {
		r.drop(pc)
	}
	r.mu.Lock()
	if len(r.viewers) >= r.MaxViewers {
		r.mu.Unlock()
		return "", ErrBusy
	}
	pc, err := r.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	v := &viewer{key: key}
	r.viewers[pc] = v
	r.mu.Unlock()
	r.notify()

	answer, err := r.negotiate(pc, v, offer)
	if err != nil {
		r.drop(pc)
		return "", err
	}
	// The sockets open with the first one that gets to negotiate (not with garbage offers)
	r.mu.Lock()
	if r.cancel == nil && r.viewers[pc] != nil {
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		go r.run(ctx)
	}
	r.mu.Unlock()

	connected := make(chan struct{})
	var once sync.Once
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateConnected:
			once.Do(func() { close(connected) })
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateClosed:
			r.drop(pc)
		}
	})
	go func() {
		select {
		case <-connected:
		case <-time.After(connectTimeout):
			r.drop(pc)
			return
		}
		t := time.NewTicker(checkEvery)
		defer t.Stop()
		for range t.C {
			if !r.has(pc) {
				return
			}
			if !alive() {
				r.drop(pc)
				return
			}
		}
	}()
	return answer, nil
}

func (r *Relay) negotiate(pc *webrtc.PeerConnection, v *viewer, offer string) (string, error) {
	var senders [2]*webrtc.RTPSender
	defer func() { // report() reads them under r.mu from another viewer's run
		r.mu.Lock()
		v.senders = senders
		r.mu.Unlock()
	}()
	for i, t := range r.tracks {
		sender, err := pc.AddTrack(t.local)
		if err != nil {
			return "", err
		}
		senders[i] = sender
		// Incoming RTCP must be read for the NACK interceptor to process it
		go func() {
			buf := make([]byte, 1500)
			for {
				if _, _, err := sender.Read(buf); err != nil {
					return
				}
			}
		}()
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", ErrBadOffer
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", ErrBadOffer
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	select {
	case <-gathered:
	case <-time.After(3 * time.Second):
	}
	return pc.LocalDescription().SDP, nil
}

func (r *Relay) has(pc *webrtc.PeerConnection) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewers[pc] != nil
}

func (r *Relay) drop(pc *webrtc.PeerConnection) {
	r.mu.Lock()
	_, ok := r.viewers[pc]
	delete(r.viewers, pc)
	if ok && len(r.viewers) == 0 && r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.mu.Unlock()
	if ok {
		r.notify()
		go pc.Close() // Close waits for the callbacks: never from inside one
	}
}

// Close cuts all previews (when the app stops).
func (r *Relay) Close() {
	r.mu.Lock()
	pcs := make([]*webrtc.PeerConnection, 0, len(r.viewers))
	for pc := range r.viewers {
		pcs = append(pcs, pc)
	}
	r.mu.Unlock()
	for _, pc := range pcs {
		r.drop(pc)
	}
}

func (r *Relay) run(ctx context.Context) {
	for _, t := range r.tracks {
		t.reset()
	}
	go r.forward(ctx, r.VideoAddr, r.tracks[0])
	go r.forward(ctx, r.AudioAddr, r.tracks[1])
	tick := time.NewTicker(reportEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.report()
		}
	}
}

func (r *Relay) forward(ctx context.Context, addr string, t *track) {
	// Retries: if the last one leaves and another arrives right away, the previous socket is closing in parallel
	var conn *net.UDPConn
	// and, if another process holds it, keep trying every second while someone is watching.
	for i := 0; ; i++ {
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err == nil {
			conn, err = net.ListenUDP("udp", ua)
		}
		if err == nil {
			break
		}
		wait := 100 * time.Millisecond
		if i >= 10 {
			if i == 10 {
				r.Log.Warn("live preview: cannot open the RTP port; still trying", "addr", addr, "err", err)
			}
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	conn.SetReadBuffer(1 << 20) // bursts of each IDR (capped by net.core.rmem_max; plenty at 800 kbps)
	go func() { <-ctx.Done(); conn.Close() }()
	buf := make([]byte, 1500)
	var pkt rtp.Packet
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if pkt.Unmarshal(buf[:n]) != nil {
			continue
		}
		t.received(&pkt, n, time.Now())
		t.local.WriteRTP(&pkt)
	}
}

func (t *track) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rw, t.seen, t.packets, t.octets = rewriter{step: t.step}, false, 0, 0
}

// received records what the Sender Report needs and rewrites sequence and timestamp.
func (t *track) received(p *rtp.Packet, size int, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	restarted := t.seen && p.SSRC != t.rw.ssrc
	orig := p.Timestamp
	switch {
	case !t.seen || restarted:
		t.ext = int64(orig)
		t.gen++
	default:
		t.ext += int64(int32(orig - uint32(t.ext))) // signed difference: tolerates reordering and wraps
	}
	t.rw.apply(p, now, t.rate)
	t.seen = true
	t.outTS, t.arrival = p.Timestamp, now
	t.packets++
	t.octets += uint32(size - 12)
}

type snapshot struct {
	ok              bool
	gen             int
	ext             int64
	outTS           uint32
	arrival         time.Time
	packets, octets uint32
}

func (t *track) shift(cycles int64) {
	t.mu.Lock()
	t.ext += cycles << 32
	t.mu.Unlock()
}

func (t *track) snap() snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return snapshot{t.seen, t.gen, t.ext, t.outTS, t.arrival, t.packets, t.octets}
}

// report sends one Sender Report per track to each viewer. Common base for both tracks: the
// arrival time of the last video packet minus its source timestamp. Any error in that base affects
// both equally, so the sync between them is the pipeline's.
func (r *Relay) report() {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	v, a := r.tracks[0].snap(), r.tracks[1].snap()
	if !v.ok {
		return
	}
	if a.ok && (v.gen != r.aligned[0] || a.gen != r.aligned[1]) {
		// After a script restart, one track arrives before the other: aligning a new one with an
		// old one would give an absurd time. Wait until both change (no Sender Reports
		// meanwhile), except the first time or if the other does not change within 3 s.
		both := v.gen != r.aligned[0] && a.gen != r.aligned[1]
		first := r.aligned == [2]int{}
		if !both && !first {
			if r.halfway.IsZero() {
				r.halfway = time.Now()
			}
			if time.Since(r.halfway) < 3*time.Second {
				return
			}
		}
		r.align(v, a)
		r.aligned, r.halfway = [2]int{v.gen, a.gen}, time.Time{}
		v, a = r.tracks[0].snap(), r.tracks[1].snap()
	}
	base := v.arrival.Add(-time.Duration(float64(v.ext) / r.tracks[0].rate * float64(time.Second)))
	var pkts [2]rtcp.Packet
	for i, s := range []snapshot{v, a} {
		if !s.ok {
			continue
		}
		at := base.Add(time.Duration(float64(s.ext) / r.tracks[i].rate * float64(time.Second)))
		pkts[i] = &rtcp.SenderReport{NTPTime: ntp(at), RTPTime: s.outTS, PacketCount: s.packets, OctetCount: s.octets}
	}
	r.mu.Lock()
	type out struct {
		sender *webrtc.RTPSender
		sr     rtcp.SenderReport
	}
	var outs []out
	for _, vw := range r.viewers {
		for i, sender := range vw.senders {
			if sender == nil || pkts[i] == nil {
				continue
			}
			enc := sender.GetParameters().Encodings
			if len(enc) == 0 {
				continue
			}
			sr := *pkts[i].(*rtcp.SenderReport)
			sr.SSRC = uint32(enc[0].SSRC)
			outs = append(outs, out{sender, sr})
		}
	}
	r.mu.Unlock()
	for _, o := range outs {
		o.sender.Transport().WriteRTCP([]rtcp.Packet{&o.sr})
	}
}

// align: the first timestamp of each track is taken with no wraps, but the video one wraps every
// 13.25 h and the audio one every 24.9 h, so after a long stream they do not match. It looks for the
// wraps of each that put the same pipeline instant at the same time (they arrive tenths of a second
// apart, not hours). 15 video wraps = 8 audio wraps (8.3 days): unique until then.
func (r *Relay) align(v, a snapshot) {
	cv, ca := float64(1<<32)/r.tracks[0].rate, float64(1<<32)/r.tracks[1].rate
	lag := a.arrival.Sub(v.arrival).Seconds()
	ta := float64(a.ext) / r.tracks[1].rate
	best, bn, bk := math.Inf(1), int64(0), int64(0)
	for n := int64(0); n < 15; n++ {
		tv := float64(v.ext)/r.tracks[0].rate + float64(n)*cv
		k := math.Round((tv + lag - ta) / ca)
		if d := math.Abs(ta + k*ca - tv - lag); d < best {
			best, bn, bk = d, n, int64(k)
		}
	}
	r.tracks[0].shift(bn)
	r.tracks[1].shift(bk)
}

// ntp: NTP 64-bit format (seconds since 1900 and fraction).
func ntp(t time.Time) uint64 {
	const unixToNTP = 2208988800
	ns := t.UnixNano()
	secs := uint64(ns/1e9) + unixToNTP
	frac := uint64(ns%1e9) << 32 / 1e9
	return secs<<32 | frac
}

// rewriter keeps sequence and timestamps continuous even if the script restarts (new SSRC,
// sequence and timestamps): such a jump freezes the browser's decoder until it reconnects. The
// timestamp advances by the real time elapsed between the last old packet and the first new one: if
// it advanced by just one frame, the Sender Reports' RTP ↔ wall-clock line would break and the
// browser would desync audio and video for several seconds.
type rewriter struct {
	step            uint32 // minimum: one frame or one Opus packet
	started         bool
	ssrc            uint32
	seqOff          uint16
	tsOff           uint32
	lastSeq, lastTS uint32 // output
	lastAt          time.Time
}

func (w *rewriter) apply(p *rtp.Packet, now time.Time, rate float64) {
	if !w.started {
		w.started, w.ssrc = true, p.SSRC
	} else if p.SSRC != w.ssrc {
		gap := w.step
		if g := now.Sub(w.lastAt).Seconds() * rate; g > float64(gap) {
			gap = uint32(g)
		}
		w.ssrc = p.SSRC
		w.seqOff = uint16(w.lastSeq) + 1 - p.SequenceNumber
		w.tsOff = w.lastTS + gap - p.Timestamp
	}
	p.SequenceNumber += w.seqOff
	p.Timestamp += w.tsOff
	w.lastSeq, w.lastTS, w.lastAt = uint32(p.SequenceNumber), p.Timestamp, now
}
