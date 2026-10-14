package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/mixer"
)

// simEngine: a mixer engine on a Unix socket, enough for the web: a catalogue of four sources, the slots command
// (with the on_air rule), take/secondary/layout/output, and the state in mixer.json and to subscribers. Other commands
// are accepted and change nothing.
type simEngine struct {
	sock, statePath string
	log             *slog.Logger

	mu        sync.Mutex
	slots     []mixer.SlotAssign
	main      string
	secondary string
	layout    string
	output    bool
	subs      map[chan []byte]struct{}
}

var simCatalog = []mixer.CatalogEntry{
	{ID: "hdmi", Name: "HDMI (board)", Kind: "hdmi-csi"},
	{ID: "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0", Name: "Cam Link 4K", Kind: "usb"},
	{ID: "net:cam1", Name: "Phone", Kind: "network"},
	{ID: "test", Name: "Test pattern", Kind: "test"},
}

func startSimEngine(ctx context.Context, dir string, log *slog.Logger) (*simEngine, error) {
	e := &simEngine{sock: filepath.Join(dir, "mixer.sock"), statePath: filepath.Join(dir, "mixer.json"), log: log,
		main: "hdmi", layout: "full", subs: map[chan []byte]struct{}{}}
	for i := 1; i <= mixer.MaxSlots; i++ {
		e.slots = append(e.slots, mixer.SlotAssign{Slot: i, Assign: mixer.AssignAuto})
	}
	ln, err := net.Listen("unix", e.sock)
	if err != nil {
		return nil, err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	e.publish()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go e.serve(ctx, c)
		}
	}()
	return e, nil
}

func (e *simEngine) client() *mixer.Client {
	return &mixer.Client{Sock: e.sock, StatePath: e.statePath, RunDir: filepath.Dir(e.sock)}
}

// setOutput: what the engine streams at start (OUTPUT of the saved configuration).
func (e *simEngine) setOutput(on bool) {
	e.mu.Lock()
	e.output = on
	e.mu.Unlock()
	e.publish()
}

func (e *simEngine) streaming() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.output
}

// place: the source in each slot (slot index → catalogue entry), with the engine's rules: a pinned source goes to its
// slot, off takes nothing, auto takes the rest in catalogue order.
func place(cfg []mixer.SlotAssign) []*mixer.CatalogEntry {
	out := make([]*mixer.CatalogEntry, len(cfg))
	pinned := map[string]bool{}
	for _, c := range cfg {
		pinned[c.Assign] = true
	}
	used := map[string]bool{}
	for i, c := range cfg {
		for j := range simCatalog {
			if simCatalog[j].ID == c.Assign {
				out[i], used[c.Assign] = &simCatalog[j], true
			}
		}
	}
	for i, c := range cfg {
		if c.Assign != mixer.AssignAuto {
			continue
		}
		for j := range simCatalog {
			id := simCatalog[j].ID
			if !used[id] && !pinned[id] {
				out[i], used[id] = &simCatalog[j], true
				break
			}
		}
	}
	return out
}

func (e *simEngine) stateLocked() mixer.State {
	placed := place(e.slots)
	st := mixer.State{V: 1, Slots: len(e.slots), Catalog: slices.Clone(simCatalog), Sources: []mixer.Source{},
		Program: mixer.Program{Layout: e.layout, Transition: "cut", Hidden: []string{}, Width: 1920, Height: 1080, FPS: 30},
		Screen:  mixer.Screen{State: "off"},
		Output:  &mixer.Output{Mode: "off", State: "off", By: "socket"}}
	if e.output {
		st.Output.Mode, st.Output.State = "on", "on"
	}
	main, sec := e.main, e.secondary
	st.Program.Main = &main
	if sec != "" {
		st.Program.Secondary = &sec
	}
	slotOf := map[string]int{}
	for i, c := range e.slots {
		st.SlotConfig = append(st.SlotConfig, mixer.SlotState{SlotAssign: c, Present: placed[i] != nil})
		if p := placed[i]; p != nil {
			src := mixer.Source{ID: p.ID, Slot: c.Slot, Kind: p.Kind, Name: p.Name, Alias: c.Alias,
				State: "live", Format: "NV12", Mode: "1920x1080@30", FPS: 30}
			if p.Kind == "network" {
				// A phone streaming a codec the engine cannot decode: a detail code with arguments (ENGINE_GO.md §13)
				src.State, src.Format, src.Mode, src.FPS = "no_signal", "", "", 0
				src.Detail = "la cámara no manda H.264 ni H.265 (MPEG-4)"
				src.DetailCode, src.DetailArgs = "net_no_video", map[string]string{"tracks": "MPEG-4"}
			}
			st.Sources = append(st.Sources, src)
			slotOf[p.ID] = c.Slot
		}
	}
	st.Audio = simAudio(slotOf)
	return st
}

// simAudio: the board HDMI's audio playing, the capture card's silent (a detail code with arguments) and the screen
// headphones waiting for a screen, so the interfaces can show the engine's detail codes (ENGINE_GO.md §13).
func simAudio(slotOf map[string]int) *mixer.Audio {
	main, follow, peak := "i2s", false, -18.0
	slot := func(id string) *int {
		if n, ok := slotOf[id]; ok {
			return &n
		}
		return nil
	}
	return &mixer.Audio{Main: &main, Mode: "single", Follow: &follow,
		Sources: []mixer.AudioInput{
			{ID: "i2s", Kind: "i2s", Name: "HDMI de la placa", State: "live", PeakDb: &peak, VideoSlot: slot("hdmi")},
			{ID: "alsa:C4K", Kind: "usb", Name: "Cam Link 4K", State: "no_signal", Detail: "sin muestras 3 s",
				DetailCode: "no_samples", DetailArgs: map[string]string{"seconds": "3"},
				VideoSlot: slot("/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0")},
		},
		Phones: &mixer.Phones{On: true, GainDb: -12, State: "waiting", Detail: "Esperando a la pantalla",
			DetailCode: "phones_waiting_screen"},
	}
}

// publish writes mixer.json (rename) and sends the state to the subscribers.
func (e *simEngine) publish() {
	e.mu.Lock()
	b, _ := json.Marshal(e.stateLocked())
	subs := make([]chan []byte, 0, len(e.subs))
	for ch := range e.subs {
		subs = append(subs, ch)
	}
	e.mu.Unlock()
	tmp := e.statePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, e.statePath)
	}
	for _, ch := range subs {
		select {
		case ch <- b:
		default:
		}
	}
}

func (e *simEngine) serve(ctx context.Context, c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), 256<<10)
	for sc.Scan() {
		var req struct {
			ID int `json:"id"`
			mixer.Command
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			c.Write([]byte(`{"ok":false,"error":"bad_json"}` + "\n"))
			continue
		}
		if req.Cmd == "subscribe" {
			e.subscribe(ctx, c, req.ID)
			return
		}
		code := e.command(req.Command)
		reply := map[string]any{"id": req.ID, "ok": code == ""}
		if code != "" {
			reply["error"] = code
		}
		b, _ := json.Marshal(reply)
		c.Write(append(b, '\n'))
	}
}

func (e *simEngine) subscribe(ctx context.Context, c net.Conn, id int) {
	ch := make(chan []byte, 8)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	first, _ := json.Marshal(e.stateLocked())
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.subs, ch); e.mu.Unlock() }()
	send := func(st []byte) bool {
		_, err := c.Write([]byte(`{"id":` + itoa(id) + `,"ok":true,"event":"state","state":` + string(st) + "}\n"))
		return err == nil
	}
	if !send(first) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case st := <-ch:
			if !send(st) {
				return
			}
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// command applies one command; "" = ok, else the engine's error code.
func (e *simEngine) command(c mixer.Command) string {
	if c.Validate() != nil {
		return "bad_request"
	}
	e.mu.Lock()
	changed := true
	code := ""
	switch c.Cmd {
	case "take", "secondary":
		found := false
		for _, s := range e.stateLocked().Sources {
			found = found || s.ID == c.Source
		}
		switch {
		case !found:
			code = "unknown_source"
		case c.Cmd == "take":
			e.main = c.Source
		default:
			e.secondary = c.Source
		}
	case "layout":
		e.layout = c.Layout
	case "output":
		e.output = *c.On
	case "slots":
		cfg := make([]mixer.SlotAssign, len(e.slots))
		for i := range cfg {
			cfg[i] = mixer.SlotAssign{Slot: i + 1, Assign: mixer.AssignAuto}
		}
		for _, s := range c.Slots {
			if s.Slot <= len(cfg) {
				cfg[s.Slot-1] = s
			}
		}
		if e.output && e.displacesOnAir(cfg) {
			code = "on_air"
			break
		}
		e.slots = cfg
	default:
		changed = false // preview, pip, pause…: accepted, nothing to show here
	}
	e.mu.Unlock()
	if code == "" && changed {
		e.log.Info("engine command", "cmd", c.Cmd)
		e.publish()
	}
	return code
}

// displacesOnAir: the new configuration would move or switch off the main source (or the secondary in pip/side).
func (e *simEngine) displacesOnAir(cfg []mixer.SlotAssign) bool {
	slotOf := func(list []*mixer.CatalogEntry, id string) int {
		for i, p := range list {
			if p != nil && p.ID == id {
				return i
			}
		}
		return -1
	}
	before, after := place(e.slots), place(cfg)
	ids := []string{e.main}
	if e.layout != "full" && e.secondary != "" {
		ids = append(ids, e.secondary)
	}
	for _, id := range ids {
		if b := slotOf(before, id); b >= 0 && slotOf(after, id) != b {
			return true
		}
	}
	return false
}
