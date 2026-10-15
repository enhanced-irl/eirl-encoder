package rtmpsend

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/bluenviron/gortmplib/pkg/rawmessage"
)

const (
	chunkSize    = 65536            // what the RTMP library announces in its connect
	writeTimeout = 10 * time.Second // a message that does not leave in this long: the link is gone
	drainTimeout = 2 * time.Second  // at the end of the input, what is queued gets this long to leave
	dropEvery    = 10 * time.Second // at most one `event=dropped` this often
	readerGrace  = 200 * time.Millisecond
)

// Config is the command line of `eirl-send rtmp`.
type Config struct {
	URL            string
	Key            string // the stream key: never printed
	Bind           string // IPv4 source address, empty for the default route
	CAFile         string // an extra CA (tests)
	ConnectTimeout time.Duration
	MaxQueue       time.Duration
	StatsInterval  time.Duration
}

// Run publishes the MPEG-TS of in to cfg.URL until in ends (0, `event=eof`), something fails (1,
// `event=failed`) or ctx is cancelled (0, no event). It writes the events to stderr and, when stats is
// not nil, one stats line per cfg.StatsInterval once connected.
func Run(ctx context.Context, cfg Config, in io.Reader, stderr, stats io.Writer) int {
	ev := &events{w: stderr, key: cfg.Key}
	fail := func(f failure) int {
		ev.failed(f)
		return 1
	}
	t, err := ParseTarget(cfg.URL, cfg.Key)
	if err != nil {
		return fail(failure{ReasonInternal, err.Error()})
	}
	var bind net.IP
	if cfg.Bind != "" {
		if bind = net.ParseIP(cfg.Bind).To4(); bind == nil {
			return fail(failure{ReasonInternal, "the bind address must be IPv4"})
		}
	}
	roots, err := loadRoots(cfg.CAFile)
	if err != nil {
		return fail(failure{ReasonInternal, clip("ca file: " + err.Error())})
	}

	q := newQueue(cfg.MaxQueue)
	go readTS(in, q)

	ev.emit("connecting", "host", t.Host, "port", strconv.Itoa(t.Port), "tls", strconv.FormatBool(t.TLS),
		"bind", cfg.Bind)
	l, client, timedOut, err := connect(ctx, t, bind, roots, cfg.ConnectTimeout, q)
	if err != nil {
		if ctx.Err() != nil {
			return 0
		}
		if _, end := q.ended(); end != nil {
			if f := inputEnd(end); f != nil {
				return fail(*f)
			}
			ev.emit("eof")
			return 0
		}
		return fail(classifySetup(err, time.Now(), timedOut))
	}

	s := &session{ev: ev, q: q, l: l, client: client, publishedAt: time.Now(),
		done: make(chan struct{}), readerDone: make(chan struct{})}
	ev.emit("connected")
	go s.readLoop()
	go s.statsLoop(stats, cfg.StatsInterval)
	go func() {
		select {
		case <-ctx.Done():
			s.stop(nil)
		case <-s.done:
		}
	}()
	s.writeLoop()
	<-s.done
	switch {
	case s.end != nil:
		return fail(*s.end)
	case ctx.Err() != nil:
		return 0
	}
	ev.emit("eof")
	return 0
}

// connect dials and does the RTMP handshake, connect and publish within timeout; the end of the input
// cancels it. timedOut: the timeout expired.
func connect(ctx context.Context, t Target, bind net.IP, roots *x509.CertPool, timeout time.Duration,
	q *queue) (l *link, c *gortmplib.Client, timedOut bool, err error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() {
		select {
		case <-q.done:
			cancel()
		case <-cctx.Done():
		}
	}()
	c = &gortmplib.Client{URL: t.url, Publish: true,
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var err error
			if l, err = dial(ctx, t, bind, roots); err != nil {
				return nil, err
			}
			return l.conn, nil
		}}
	err = c.Initialize(cctx)
	return l, c, errors.Is(cctx.Err(), context.DeadlineExceeded), err
}

// inputEnd is the failure of the input's end: nil for the end of the input (EOF).
func inputEnd(err error) *failure {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return &failure{ReasonInternal, clip("input: " + err.Error())}
}

// session is a publish accepted by the server: a writer (the caller of writeLoop), a reader of the
// server's messages and the stats. The first end wins.
type session struct {
	ev          *events
	q           *queue
	l           *link
	client      *gortmplib.Client
	publishedAt time.Time

	once       sync.Once
	end        *failure // nil: a clean end
	done       chan struct{}
	readerDone chan struct{}
}

func (s *session) stop(f *failure) {
	s.once.Do(func() {
		s.end = f
		close(s.done)
		s.l.conn.Close()
	})
}

// writeLoop sends the queue's frames until the input ends or the session stops.
func (s *session) writeLoop() {
	var buf bytes.Buffer
	w := &rawmessage.Writer{BW: &buf}
	w.Initialize()
	w.SetChunkSize(chunkSize)
	var mux muxer
	publishing := false
	s.q.goLive()
	for {
		f, err := s.q.pop(s.done)
		if errors.Is(err, errStopped) {
			return
		}
		if err != nil {
			s.stop(inputEnd(err))
			return
		}
		msgs, err := mux.messages(f)
		if err != nil {
			s.stop(&failure{ReasonInternal, clip("muxing: " + err.Error())})
			return
		}
		for _, m := range msgs {
			buf.Reset()
			if err := w.Write(m); err != nil {
				s.stop(&failure{ReasonInternal, clip("muxing: " + err.Error())})
				return
			}
			if err := s.l.conn.writeMessage(buf.Bytes(), writeTimeout); err != nil {
				s.writeFailed(err)
				return
			}
		}
		if len(msgs) > 0 && !publishing {
			publishing = true
			s.ev.emit("publishing", "codec", f.info.codec)
		}
		if at, end := s.q.ended(); end != nil && time.Since(at) > drainTimeout {
			s.stop(inputEnd(end))
			return
		}
	}
}

// writeFailed ends the session after a write error, unless the reader explains it (the server closed or
// refused): it gets a moment to say so first.
func (s *session) writeFailed(err error) {
	select {
	case <-s.done:
		return
	case <-s.readerDone:
	case <-time.After(readerGrace):
	}
	f := classifyLive(err, time.Since(s.publishedAt))
	s.stop(&f)
}

// readLoop reads the server's messages (the library answers its pings and acks) until the connection
// fails or the server reports an error.
func (s *session) readLoop() {
	defer close(s.readerDone)
	for {
		msg, err := s.client.Read()
		if err == nil {
			if cmd, ok := msg.(*message.CommandAMF0); ok {
				if se := statusErrorOf(cmd); se != nil {
					err = se
				}
			}
		}
		if err != nil {
			f := classifyLive(err, time.Since(s.publishedAt))
			s.stop(&f)
			return
		}
	}
}

// statusErrorOf is the error an `onStatus` or `_error` of level «error» carries, nil otherwise.
func statusErrorOf(cmd *message.CommandAMF0) *statusError {
	if cmd.Name != "onStatus" && cmd.Name != "_error" {
		return nil
	}
	for _, a := range cmd.Arguments {
		var o amf0.Object
		switch v := a.(type) {
		case amf0.Object:
			o = v
		case amf0.ECMAArray:
			o = amf0.Object(v)
		default:
			continue
		}
		if level, _ := o.GetString("level"); level == "error" {
			code, _ := o.GetString("code")
			desc, _ := o.GetString("description")
			return &statusError{code: code, description: desc}
		}
	}
	return nil
}

// statsLoop writes a stats line every interval (if stats is not nil) and the `event=dropped` lines. The
// bytes of an interval are those the server acknowledged when the kernel says so (Linux: smooth, the
// network's real pace), else those written to the socket (in bursts, as the socket buffer frees).
func (s *session) statsLoop(stats io.Writer, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	last := time.Now()
	_, lastDropped, lastPushed, _ := s.q.snapshot()
	reported := lastDropped
	lastSent := s.sent(sockStats(s.l.tcp))
	var lastEvent time.Time
	var sendRate, inRate rateMeter
	for {
		select {
		case <-s.done:
			return
		case now := <-tick.C:
			dt := now.Sub(last)
			last = now
			sock := sockStats(s.l.tcp)
			sent := s.sent(sock)
			bytes := max(sent-lastSent, 0)
			lastSent = sent
			waiting, dropped, pushed, dropMs := s.q.snapshot()
			rate := sendRate.add(bytes, dt)
			if in := inRate.add(pushed-lastPushed, dt); rate <= 0 {
				rate = in // stalled: what is unsent is worth what the encoder makes
			}
			lastPushed = pushed
			if stats != nil {
				line := formatStats(sock.rttMs, bytes, bufferMs(waiting, sock.unsent, rate), dropped-lastDropped, dt)
				if _, err := stats.Write(append(line, '\n')); err != nil {
					stats = nil // nobody reads them any more
				}
			}
			lastDropped = dropped
			if dropped > reported && now.Sub(lastEvent) >= dropEvery {
				s.ev.emit("dropped", "frames", strconv.FormatInt(dropped-reported, 10),
					"queue_ms", strconv.FormatInt(dropMs, 10))
				reported, lastEvent = dropped, now
			}
		}
	}
}

// sent is the bytes sent so far: acknowledged when the kernel says, else written.
func (s *session) sent(sock sockInfo) int64 {
	if sock.acked > 0 {
		return sock.acked
	}
	return s.l.wire.written.Load()
}
