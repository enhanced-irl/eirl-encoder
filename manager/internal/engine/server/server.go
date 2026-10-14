// Package server is the engine's command socket and state publisher (MIXER.md §5): a Unix socket that
// speaks JSON, one object per line, plus the atomic copy of the state in <run_dir>/mixer.json. It is
// the Go port of class Server and Engine._publish in scripts/engine_mixer.py.
//
// Requests: {"id": 7, "cmd": "take", "source": "hdmi"} → {"id": 7, "ok": true} or
// {"id": 7, "ok": false, "error": "unknown_source"}. get and subscribe are answered here from the last
// published state; {"cmd": "subscribe"} also pushes every later change as
// {"event": "state", "state": {…}}.
//
// # The engine as an actor
//
// Every other command goes to the engine through Requests(). The engine owns its state and reads that
// channel from its own loop, so commands never run concurrently with each other nor with the rest of
// the loop (the Python engine ran them on the GLib loop with idle_add):
//
//	srv := server.New(server.Config{SocketPath: run + "/mixer.sock", StatePath: run + "/mixer.json"})
//	_ = srv.Listen() // already logged; the engine goes on without a socket, like Python
//	defer srv.Close()
//	for {
//		select {
//		case req := <-srv.Requests():
//			req.Run(eng.command) // or: ok, code := eng.command(req.Cmd, req.Msg); req.Reply(ok, code)
//			srv.Publish(eng.state())
//		case <-tick.C:
//			srv.Publish(eng.state())
//		}
//	}
//
// The client's goroutine waits CommandTimeout for the reply; past that the client gets
// {"ok": false, "error": "timeout"} and the request may still run later: Reply never blocks and a late
// reply is discarded. Requests() is never closed; the engine stops reading it when it stops.
//
// Publish can be called from any goroutine. It only writes mixer.json and pushes to the subscribers
// when the state's JSON differs from the previous one.
//
// The socket is local and group-restricted (eirl-encoder), but every request is still validated and
// bounded: MaxClients connections, MaxLine bytes per line, MaxOut bytes of pending output per client.
// Request bodies are never logged: commands can carry text typed by the user.
package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/engine/elog"
)

const (
	// MaxClients: connections at once; one more is accepted and closed straight away.
	MaxClients = 8
	// MaxLine: bytes per request line, without the '\n'. A longer line (or a partial line that is
	// already longer) closes that client without a reply.
	MaxLine = 4096
	// MaxOut: bytes of output waiting for a client. A client that does not read is dropped.
	MaxOut = 256 << 10
	// CommandTimeout: how long a client waits for the engine to run its command.
	CommandTimeout = 2 * time.Second
	// DefaultGroup: the socket's group when it exists (the app's user is in it).
	DefaultGroup = "eirl-encoder"

	maxCmd     = 32          // runes of cmd
	maxID      = 64          // characters of id
	writeChunk = 64 << 10    // bytes per write, so pending output is measured closely
	closeWait  = time.Second // Close waits at most this for the client goroutines
)

// Error codes the server answers by itself.
const (
	ErrBadJSON    = "bad_json"
	ErrBadRequest = "bad_request"
	ErrTimeout    = "timeout"
)

// Config of a Server.
type Config struct {
	SocketPath string // <run_dir>/mixer.sock
	StatePath  string // <run_dir>/mixer.json; "" = no file
	Group      string // the socket's group; "" = DefaultGroup
}

// Reply: the engine's answer to one command. Error is only sent when not empty.
type Reply struct {
	OK    bool
	Error string
}

// Handler runs one command in the engine's goroutine: the same shape as Engine.command in Python.
type Handler func(cmd string, msg map[string]any) (ok bool, code string)

// Request: one command for the engine.
type Request struct {
	Cmd string
	// Msg: the whole request object, cmd and id included. Numbers are json.Number, so the engine can
	// tell 25 from 25.0 (int and float in Python).
	Msg   map[string]any
	reply chan Reply
}

// Reply answers the request. It never blocks: only the first reply counts, and one that arrives
// after the client gave up (timeout) is discarded.
func (r *Request) Reply(ok bool, code string) {
	select {
	case r.reply <- Reply{OK: ok, Error: code}:
	default:
	}
}

// Run calls h with the request and replies with its result.
func (r *Request) Run(h Handler) {
	ok, code := h(r.Cmd, r.Msg)
	r.Reply(ok, code)
}

// Server: the socket, its clients and the last published state.
type Server struct {
	cfg     Config
	timeout time.Duration
	reqs    chan *Request
	done    chan struct{}

	pubMu     sync.Mutex // serialises Publish: compare, file, cache and broadcast in one order
	last      []byte     // last published JSON (under pubMu)
	writeFail bool       // the last write of StatePath failed (under pubMu): log once per streak

	mu      sync.Mutex // clients, their output and the cache
	cache   []byte
	clients map[*client]struct{}
	closed  bool
	ln      *net.UnixListener

	wg        sync.WaitGroup
	closeOnce sync.Once
}

type client struct {
	conn       net.Conn
	out        []byte // queued output (under Server.mu)
	inflight   int    // bytes handed to the current Write (under Server.mu)
	subscribed bool   // (under Server.mu)
	closed     bool   // (under Server.mu)
	wake       chan struct{}
	quit       chan struct{}
}

// New returns a server that publishes state but has no socket yet (see Listen).
func New(cfg Config) *Server {
	if cfg.Group == "" {
		cfg.Group = DefaultGroup
	}
	return &Server{
		cfg:     cfg,
		timeout: CommandTimeout,
		reqs:    make(chan *Request, MaxClients),
		done:    make(chan struct{}),
		cache:   []byte("{}"),
		clients: map[*client]struct{}{},
	}
}

// Requests: the commands for the engine, to be read from the engine's own loop.
func (s *Server) Requests() <-chan *Request { return s.reqs }

// Listen creates the socket (removing a stale one), 0660 and group Config.Group when it exists, and
// starts accepting clients. A failure is logged (socket_unavailable) and returned; Publish still works.
func (s *Server) Listen() error {
	err := s.listen()
	if err != nil {
		elog.Event("mixer", "socket_unavailable", "path", s.cfg.SocketPath, "err", err)
	}
	return err
}

func (s *Server) listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("server closed")
	}
	if s.ln != nil {
		return errors.New("already listening")
	}
	path := s.cfg.SocketPath
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	ln.SetUnlinkOnClose(true) // Close removes the socket
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return err
	}
	if g, err := user.LookupGroup(s.cfg.Group); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			_ = os.Chown(path, -1, gid) // without it (by hand, in tests): only this user
		}
	}
	s.ln = ln
	s.wg.Add(1)
	go s.acceptLoop(ln)
	return nil
}

func (s *Server) acceptLoop(ln *net.UnixListener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond) // EMFILE and the like: try again, like Python's select loop
			continue
		}
		s.mu.Lock()
		if s.closed || len(s.clients) >= MaxClients {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		c := &client{conn: conn, wake: make(chan struct{}, 1), quit: make(chan struct{})}
		s.clients[c] = struct{}{}
		s.wg.Add(2)
		s.mu.Unlock()
		go s.readLoop(c)
		go s.writeLoop(c)
	}
}

// Close closes the listener (removing the socket) and every client, and waits a bounded time for
// their goroutines. Requests waiting for the engine give up at once.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.done)
		for c := range s.clients {
			s.dropLocked(c)
		}
		ln := s.ln
		s.mu.Unlock()
		if ln != nil {
			err = ln.Close()
		}
		finished := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(closeWait):
		}
	})
	return err
}

// State: the last published state ({} before the first Publish). The caller must not modify it.
func (s *Server) State() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache
}

// Publish takes the whole state (a struct, a map, or a pre-marshalled json.RawMessage / []byte with a
// JSON object). Only when its JSON differs from the previous one it writes StatePath atomically
// (0644), updates the cache served by get and subscribe, and pushes it to the subscribers. It reports
// whether the state changed; an error means the state is not a JSON object and nothing was done.
func (s *Server) Publish(state any) (bool, error) {
	b, err := marshalState(state)
	if err != nil {
		return false, err
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if bytes.Equal(b, s.last) {
		return false, nil
	}
	s.last = b
	if s.cfg.StatePath != "" {
		if err := writeAtomic(s.cfg.StatePath, b); err != nil {
			if !s.writeFail {
				elog.Event("mixer", "state_write_failed", "path", s.cfg.StatePath, "err", err)
			}
			s.writeFail = true
		} else {
			s.writeFail = false
		}
	}
	line := encodeLine(struct {
		Event string          `json:"event"`
		State json.RawMessage `json:"state"`
	}{"state", b})
	s.mu.Lock()
	s.cache = b
	for c := range s.clients {
		if c.subscribed {
			s.enqueueLocked(c, line)
		}
	}
	s.mu.Unlock()
	return true, nil
}

func marshalState(state any) ([]byte, error) {
	switch v := state.(type) {
	case []byte:
		state = json.RawMessage(v)
	case nil:
		return nil, errors.New("server: nil state")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // like Python's json.dumps: <, > and & as they are
	if err := enc.Encode(state); err != nil {
		return nil, fmt.Errorf("server: state: %w", err)
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(b) == 0 || b[0] != '{' {
		return nil, errors.New("server: state is not a JSON object")
	}
	return b, nil
}

// writeAtomic: tmp + rename, so a reader (the app) never sees half a file. 0644 explicitly, whatever
// the umask: the app runs as another user.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func encodeLine(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Only our own response types get here, all marshallable.
		panic(err)
	}
	return buf.Bytes()
}

// enqueueLocked queues one line for c and drops c when its pending output goes over MaxOut: a client
// that never reads would make it grow forever.
func (s *Server) enqueueLocked(c *client, line []byte) {
	if c.closed {
		return
	}
	c.out = append(c.out, line...)
	if c.inflight+len(c.out) > MaxOut {
		s.dropLocked(c)
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (s *Server) send(c *client, v any) {
	line := encodeLine(v)
	s.mu.Lock()
	s.enqueueLocked(c, line)
	s.mu.Unlock()
}

// dropLocked: out of the list and closed (which also unblocks its Read and Write).
func (s *Server) dropLocked(c *client) {
	if c.closed {
		return
	}
	c.closed = true
	delete(s.clients, c)
	c.out = nil
	c.conn.Close()
	close(c.quit)
}

func (s *Server) drop(c *client) {
	s.mu.Lock()
	s.dropLocked(c)
	s.mu.Unlock()
}

func (s *Server) writeLoop(c *client) {
	defer s.wg.Done()
	for {
		select {
		case <-c.wake:
		case <-c.quit:
			return
		}
		for {
			s.mu.Lock()
			if c.closed {
				s.mu.Unlock()
				return
			}
			if len(c.out) == 0 {
				c.out = nil
				s.mu.Unlock()
				break
			}
			n := min(len(c.out), writeChunk)
			chunk := c.out[:n:n] // later appends go past the end of c.out, never into chunk
			c.out = c.out[n:]
			c.inflight = n
			s.mu.Unlock()

			_, err := c.conn.Write(chunk)

			s.mu.Lock()
			c.inflight = 0
			if err != nil || len(c.out) > MaxOut {
				s.dropLocked(c)
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
		}
	}
}

func (s *Server) readLoop(c *client) {
	defer s.wg.Done()
	defer s.drop(c)
	defer func() {
		// A client can never bring the socket down. Only the error goes to the log, never the request.
		if r := recover(); r != nil {
			elog.Event("mixer", "socket_client_error", "err", fmt.Sprint(r))
		}
	}()
	// One byte more than MaxLine for the '\n': a full buffer without it is a line that is too long.
	br := bufio.NewReaderSize(c.conn, MaxLine+1)
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return // EOF, closed, or bufio.ErrBufferFull: too long, out without a reply
		}
		s.onLine(c, line[:len(line)-1])
	}
}

// response: key order as in Python ({"id", "ok", "error" | "state"}).
type response struct {
	ID    json.RawMessage `json:"id,omitempty"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	State json.RawMessage `json:"state,omitempty"`
}

func (s *Server) onLine(c *client, line []byte) {
	if len(bytes.Trim(line, " \t\r\n\v\f")) == 0 { // Python's bytes.strip(): ASCII whitespace only
		return
	}
	msg, id, code := parseRequest(line)
	if code != "" {
		s.send(c, response{Error: code}) // no id: it may be the very thing that is wrong
		return
	}
	cmd := msg["cmd"].(string)
	out := response{ID: id}
	switch cmd {
	case "get", "subscribe":
		// Under the lock, the reply carries the latest state and every later change follows it.
		s.mu.Lock()
		out.OK, out.State = true, s.cache
		if cmd == "subscribe" {
			c.subscribed = true
		}
		s.enqueueLocked(c, encodeLine(out))
		s.mu.Unlock()
		return
	}
	out.OK, out.Error = s.dispatch(cmd, msg)
	s.send(c, out)
}

// parseRequest returns the request object and its id as raw JSON (nil when absent or null), or the
// error code for the client.
func parseRequest(line []byte) (map[string]any, json.RawMessage, string) {
	// encoding/json would turn invalid UTF-8 into U+FFFD; Python's decode('utf-8') rejects it.
	// json.Valid also bounds the nesting (Go's scanner stops at 10000 levels; MaxLine allows ~2048).
	if !utf8.Valid(line) || !json.Valid(line) {
		return nil, nil, ErrBadJSON
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, ErrBadJSON
	}
	msg, ok := v.(map[string]any)
	if !ok {
		return nil, nil, ErrBadRequest
	}
	cmd, ok := msg["cmd"].(string)
	if !ok || utf8.RuneCountInString(cmd) > maxCmd {
		return nil, nil, ErrBadRequest
	}
	id, ok := checkID(msg["id"])
	if !ok {
		return nil, nil, ErrBadRequest
	}
	return msg, id, ""
}

// checkID: absent or null → no id; an integer or a string of at most 64 characters → echoed as it
// came. Anything else (a float, a bool, an object…) is a bad request. Python also took booleans (bool
// is an int there); this port does not: the contract says number or string.
func checkID(v any) (json.RawMessage, bool) {
	switch x := v.(type) {
	case nil:
		return nil, true
	case json.Number:
		if !isInteger(string(x)) || len(x) > maxID {
			return nil, false
		}
		return json.RawMessage(x), true
	case string:
		if utf8.RuneCountInString(x) > maxID {
			return nil, false
		}
		b, err := json.Marshal(x)
		if err != nil {
			return nil, false
		}
		return b, true
	}
	return nil, false
}

// isInteger: a JSON number without fraction or exponent (Python's json gives an int only for those).
func isInteger(n string) bool {
	if n == "" {
		return false
	}
	if n[0] == '-' {
		n = n[1:]
	}
	if n == "" {
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return false
		}
	}
	return true
}

// dispatch hands the command to the engine's loop and waits for its reply, at most s.timeout in all
// (queueing included). Past that: timeout, and the engine may still run it later.
func (s *Server) dispatch(cmd string, msg map[string]any) (bool, string) {
	req := &Request{Cmd: cmd, Msg: msg, reply: make(chan Reply, 1)}
	t := time.NewTimer(s.timeout)
	defer t.Stop()
	select {
	case s.reqs <- req:
	case <-t.C:
		return false, ErrTimeout
	case <-s.done:
		return false, ErrTimeout
	}
	select {
	case r := <-req.reply:
		return r.OK, r.Error
	case <-t.C:
		return false, ErrTimeout
	case <-s.done:
		return false, ErrTimeout
	}
}

// StatePath: <run_dir>/mixer.json.
func StatePath(runDir string) string { return filepath.Join(runDir, "mixer.json") }

// SocketPath: <run_dir>/mixer.sock.
func SocketPath(runDir string) string { return filepath.Join(runDir, "mixer.sock") }
