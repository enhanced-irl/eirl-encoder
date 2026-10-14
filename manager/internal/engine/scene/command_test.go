package scene

import (
	"encoding/json"
	"strings"
	"testing"
)

type cmdCase struct {
	msg         map[string]any
	wantChanged bool
	wantCode    string
}

func runCmds(t *testing.T, s *Scene, known map[string]bool, cases []cmdCase) {
	t.Helper()
	for _, c := range cases {
		changed, code := ApplyCommand(s, c.msg, known)
		if changed != c.wantChanged || code != c.wantCode {
			t.Errorf("%v = (%v, %q), want (%v, %q)", c.msg, changed, code, c.wantChanged, c.wantCode)
		}
	}
}

// msg decodes a command as the socket does, so numbers arrive as float64.
func msg(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCommandCommands(t *testing.T) {
	s := New("a")
	known := map[string]bool{"a": true, "b": true}
	runCmds(t, &s, known, []cmdCase{
		{msg(t, `{"cmd":"take","source":"b"}`), true, ""},
		{msg(t, `{"cmd":"take","source":"zz"}`), false, CodeUnknownSource},
		{msg(t, `{"cmd":"take","source":["b"]}`), false, CodeUnknownSource},
		{msg(t, `{"cmd":"pip","size":[1]}`), false, CodeBadRequest},
		{msg(t, `{"cmd":"layout","layout":"pip"}`), true, ""},
		{msg(t, `{"cmd":"layout","layout":"pip"}`), false, ""},
		{msg(t, `{"cmd":"layout","layout":"x"}`), false, CodeBadLayout},
		{msg(t, `{"cmd":"transition","transition":"fade"}`), true, ""},
		{msg(t, `{"cmd":"swap"}`), true, ""},
	})
	if s.Main != "a" {
		t.Errorf("main after swap: %q", s.Main)
	}
	runCmds(t, &s, known, []cmdCase{{msg(t, `{"cmd":"reboot"}`), false, CodeUnknownCommand}})
}

func TestCommandPipAndPause(t *testing.T) {
	s := New("a")
	none := map[string]bool{}
	runCmds(t, &s, none, []cmdCase{
		{msg(t, `{"cmd":"pip","corner":"tl"}`), true, ""},
		{msg(t, `{"cmd":"pip","size":25}`), true, ""},
	})
	if s.Pip != (Pip{"tl", 25}) {
		t.Errorf("pip %+v", s.Pip)
	}
	runCmds(t, &s, none, []cmdCase{{msg(t, `{"cmd":"pip","corner":"tl","size":25}`), false, ""}})
	for _, bad := range []string{`{}`, `{"corner":"x"}`, `{"size":50}`, `{"size":true}`, `{"size":"33"}`} {
		m := msg(t, bad)
		m["cmd"] = "pip"
		runCmds(t, &s, none, []cmdCase{{m, false, CodeBadRequest}})
	}
	runCmds(t, &s, none, []cmdCase{
		{msg(t, `{"cmd":"pause","on":true}`), true, ""},
		{msg(t, `{"cmd":"pause","text":" Ya vuelvo ","mute":false}`), true, ""},
	})
	want := Pause{On: true, Text: "Ya vuelvo", Mute: false}
	if s.Pause != want {
		t.Errorf("pause %+v", s.Pause)
	}
	for _, bad := range []string{`{}`, `{"on":1}`, `{"mute":"x"}`, `{"text":"<i>"}`, `{"text":""}`} {
		m := msg(t, bad)
		m["cmd"] = "pause"
		runCmds(t, &s, none, []cmdCase{{m, false, CodeBadRequest}})
	}
	if s.Pause != want {
		t.Errorf("a rejected pause changed it: %+v", s.Pause)
	}
}

func TestCommandHide(t *testing.T) {
	s := New("a")
	s.Secondary = "b"
	known := map[string]bool{"a": true, "b": true, "c": true}
	runCmds(t, &s, known, []cmdCase{
		{msg(t, `{"cmd":"hide","source":"a","hidden":true}`), false, CodeOnAir},
		{msg(t, `{"cmd":"hide","source":"b","hidden":1}`), false, CodeBadRequest},
		{msg(t, `{"cmd":"hide","source":"b","hidden":true}`), true, ""},
	})
	if s.Secondary != "" { // a hidden secondary stops being one
		t.Errorf("secondary %q", s.Secondary)
	}
	runCmds(t, &s, known, []cmdCase{
		{msg(t, `{"cmd":"take","source":"b"}`), false, CodeHiddenSource},
		{msg(t, `{"cmd":"secondary","source":"b"}`), false, CodeHiddenSource},
		{msg(t, `{"cmd":"hide","source":"b","hidden":true}`), false, ""},
		{msg(t, `{"cmd":"hide","source":"b","hidden":false}`), true, ""},
		{msg(t, `{"cmd":"take","source":"b"}`), true, ""},
	})
	fresh := New("a")
	runCmds(t, &fresh, known, []cmdCase{{msg(t, `{"cmd":"swap"}`), false, CodeNoSecondary}})
}

// The order of the checks decides the code when several things are wrong at once.
func TestCommandCheckOrder(t *testing.T) {
	s := New("a")
	known := map[string]bool{"a": true, "b": true}
	runCmds(t, &s, known, []cmdCase{
		{msg(t, `{"cmd":"hide","source":"zz","hidden":"x"}`), false, CodeUnknownSource}, // source before hidden
		{msg(t, `{"cmd":"hide","source":"a","hidden":"x"}`), false, CodeBadRequest},     // hidden before on_air
		{msg(t, `{"cmd":"hide","source":7}`), false, CodeUnknownSource},
		{msg(t, `{"cmd":"secondary"}`), false, CodeUnknownSource},
		{msg(t, `{"cmd":"transition","transition":"wipe"}`), false, CodeBadTransition},
		{msg(t, `{"cmd":"transition"}`), false, CodeBadTransition},
		{msg(t, `{"cmd":"layout","layout":null}`), false, CodeBadLayout},
		{msg(t, `{"cmd":"pip","corner":null,"size":null}`), false, CodeBadRequest}, // null = absent
		{msg(t, `{"cmd":"pip","corner":"x","size":25}`), false, CodeBadRequest},
		{msg(t, `{"cmd":"pause","on":null,"text":null,"mute":null}`), false, CodeBadRequest},
		{msg(t, `{"cmd":"pause","on":true,"text":"<b>"}`), false, CodeBadRequest}, // nothing applied
		{msg(t, `{"cmd":7}`), false, CodeUnknownCommand},
		{msg(t, `{}`), false, CodeUnknownCommand},
	})
	if s.Pause.On {
		t.Error("a rejected pause turned it on")
	}
	// The main as secondary without a secondary: a swap that cannot happen, so no change and no error
	runCmds(t, &s, known, []cmdCase{{msg(t, `{"cmd":"secondary","source":"a"}`), false, ""}})
}

func TestCommandPipSizeIsAnInt(t *testing.T) {
	// With UseNumber the literal survives, so 33.0 is a float as in Python; a plain float64 cannot
	// tell 33 from 33.0 and accepts it
	dec := json.NewDecoder(strings.NewReader(`{"cmd":"pip","size":33.0} {"cmd":"pip","size":33}`))
	dec.UseNumber()
	s := New("a")
	s.Pip.Size = 25
	for _, want := range []cmdCase{{nil, false, CodeBadRequest}, {nil, true, ""}} {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if changed, code := ApplyCommand(&s, m, nil); changed != want.wantChanged || code != want.wantCode {
			t.Errorf("%v = (%v, %q)", m, changed, code)
		}
	}
	// In-process callers (the touch screen) build commands with Go ints
	if changed, code := ApplyCommand(&s, map[string]any{"cmd": "pip", "size": 25}, nil); !changed || code != "" {
		t.Errorf("int size = (%v, %q)", changed, code)
	}
	for _, v := range []any{25.5, 1e300, -25.0} {
		if _, code := ApplyCommand(&s, map[string]any{"cmd": "pip", "size": v}, nil); code != CodeBadRequest {
			t.Errorf("size %v accepted", v)
		}
	}
}
