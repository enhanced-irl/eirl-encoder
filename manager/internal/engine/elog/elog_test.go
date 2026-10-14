package elog

import (
	"bytes"
	"errors"
	"testing"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		kv   []any
		want string
	}{
		{nil, "[mixer] event=started\n"},
		{[]any{"slot", 1, "state", "live"}, "[mixer] event=started slot=1 state=live\n"},
		{[]any{"detail", "sin señal HDMI"}, "[mixer] event=started detail=\"sin señal HDMI\"\n"},
		{[]any{"err", errors.New(`a "b"`)}, "[mixer] event=started err=\"a \\\"b\\\"\"\n"},
		{[]any{"x", ""}, "[mixer] event=started x=\"\"\n"},
		{[]any{"k=v", 1.5}, "[mixer] event=started k=v=1.5\n"},
		{[]any{"odd"}, "[mixer] event=started extra=odd\n"},
		{[]any{"nl", "a\nb"}, "[mixer] event=started nl=\"a\\nb\"\n"},
	}
	for _, c := range cases {
		if got := Format("mixer", "started", c.kv...); got != c.want {
			t.Errorf("Format(%v) = %q, want %q", c.kv, got, c.want)
		}
	}
}

func TestEvent(t *testing.T) {
	var b bytes.Buffer
	SetOutput(&b)
	defer SetOutput(nil)
	Event("engine", "config_invalid", "key", "VBITRATE")
	if b.String() != "[engine] event=config_invalid key=VBITRATE\n" {
		t.Fatalf("got %q", b.String())
	}
}
