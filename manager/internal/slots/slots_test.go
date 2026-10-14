package slots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/i18n"
)

const cam = "/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0"

func TestValidate(t *testing.T) {
	list, fe, _ := Validate([]Slot{
		{Slot: 3, Assign: "net:cam1", Alias: "  Phone  "},
		{Slot: 1, Assign: "auto", Alias: "Main camera"},
		{Slot: 2, Assign: cam},
	}, 4)
	if fe != nil {
		t.Fatalf("valid list: %v", fe)
	}
	want := []Slot{{1, "auto", "Main camera"}, {2, cam, ""}, {3, "net:cam1", "Phone"}, {4, "auto", ""}}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("slot %d: %+v, want %+v", i+1, list[i], want[i])
		}
	}

	for _, c := range []struct {
		name  string
		list  []Slot
		field string
		code  string
	}{
		{"duplicate", []Slot{{1, "hdmi", ""}, {3, "hdmi", ""}}, "slots.3.assign", CodeDuplicate},
		{"usb with a prefix", []Slot{{2, "usb:" + cam, ""}}, "slots.2.assign", CodeInvalid},
		{"bad id", []Slot{{1, "/dev/video0", ""}}, "slots.1.assign", CodeInvalid},
		{"long alias", []Slot{{1, "auto", strings.Repeat("a", 25)}}, "slots.1.alias", CodeInvalid},
		{"control char", []Slot{{1, "auto", "a\tb"}}, "slots.1.alias", CodeInvalid},
		{"markup", []Slot{{1, "auto", "<b>"}}, "slots.1.alias", CodeInvalid},
		{"slot out of range", []Slot{{5, "auto", ""}}, "slots", CodeInvalid},
		{"slot beyond n", []Slot{{3, "auto", ""}}, "slots", CodeInvalid},
		{"repeated slot", []Slot{{1, "auto", ""}, {1, "off", ""}}, "slots", CodeInvalid},
	} {
		n := 4
		if c.name == "slot beyond n" {
			n = 2
		}
		_, fe, codes := Validate(c.list, n)
		if fe == nil || fe[c.field] == "" || codes[c.field] != c.code {
			t.Errorf("%s: fields %v codes %v, want %s=%s", c.name, fe, codes, c.field, c.code)
		}
	}
	// The duplicate message names the first slot, in each language
	_, fe, _ = Validate([]Slot{{2, "net:cam1", ""}, {4, "net:cam1", ""}}, 4)
	if got := i18n.Text(i18n.EN, fe["slots.4.assign"]); got != "that source is already pinned to source 2" {
		t.Errorf("duplicate message: %q", got)
	}
	// A phone's typographic apostrophe is stored as a plain one
	list, fe, _ = Validate([]Slot{{1, "auto", "Cam d\u2019Ana"}, {2, "hdmi", "\u2018Main\u2019"}}, 4)
	if fe != nil || list[0].Alias != "Cam d'Ana" || list[1].Alias != "'Main'" {
		t.Errorf("typographic apostrophes: %v %v", list, fe)
	}
	// An alias of 24 letters with accents is fine (runes, not bytes); "off" and "auto" may repeat
	if _, fe, _ := Validate([]Slot{{1, "off", strings.Repeat("é", 24)}, {2, "off", ""}, {3, "auto", ""}, {4, "auto", ""}}, 4); fe != nil {
		t.Errorf("repeated off/auto: %v", fe)
	}
}

func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slots.json")
	if list, err := Load(path, 4); err != nil || len(list) != 4 || list[3].Assign != "auto" {
		t.Fatalf("missing file: %v %v", list, err)
	}
	in := []Slot{{3, "net:cam1", "Phone"}, {1, "hdmi", "Main"}, {2, "off", ""}, {4, "auto", ""}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "{\n  \"v\": 1,") || strings.Index(string(b), `"slot": 1`) > strings.Index(string(b), `"slot": 2`) {
		t.Errorf("file: %s", b)
	}
	list, err := Load(path, 4)
	if err != nil || list[0].Assign != "hdmi" || list[2].Alias != "Phone" {
		t.Errorf("load: %v %v", list, err)
	}
	// Slots beyond n are ignored; missing ones are auto
	if list, err := Load(path, 2); err != nil || len(list) != 2 || list[1].Assign != "off" {
		t.Errorf("n=2: %v %v", list, err)
	}
	for _, bad := range []string{
		`{"v":2,"slots":[]}`, `{"slots":[]}`, `not json`,
		`{"v":1,"slots":[{"slot":1,"assign":"hdmi"},{"slot":2,"assign":"hdmi"}]}`,
		`{"v":1,"slots":[{"slot":5,"assign":"auto"}]}`,
		`{"v":1,"slots":[{"slot":1,"assign":"usb:/dev/v4l/by-id/x"}]}`,
		`{"v":1,"slots":[{"slot":1,"assign":"auto","alias":"<x>"}]}`,
	} {
		os.WriteFile(path, []byte(bad), 0o640)
		if list, err := Load(path, 4); err == nil || len(list) != 4 || list[0].Assign != "auto" {
			t.Errorf("%s: %v %v, want all auto and an error", bad, list, err)
		}
	}
}
