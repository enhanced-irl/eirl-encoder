package tcchip

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// board writes a fake sysfs: v4l-subdev and video names, and the I2C adapters' names.
func board(t *testing.T, subdevs, videos, adapters map[string]string) string {
	t.Helper()
	root := t.TempDir()
	w := func(p, s string) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for n, name := range subdevs {
		w("sys/class/video4linux/"+n+"/name", name)
	}
	for n, name := range videos {
		w("sys/class/video4linux/"+n+"/name", name)
	}
	for bus, name := range adapters {
		w("sys/bus/i2c/devices/i2c-"+bus+"/name", name)
	}
	return root
}

var nvidiaMux = map[string]string{"6": "Tegra I2C adapter", "7": "i2c-6-mux (chan_id 0)", "8": "i2c-6-mux (chan_id 1)"}

func TestFind(t *testing.T) {
	cam0 := Chip{Port: 0, Bus: 7, Name: "tc358743 7-000f", Subdev: "/dev/v4l-subdev1", Video: "/dev/video0"}
	for _, tc := range []struct {
		name               string
		subdevs, videos, a map[string]string
		want               []Chip
	}{
		{"no chip (empty connectors: the probe failed)", map[string]string{"v4l-subdev0": "nvcsi--1"}, nil, nvidiaMux, nil},
		{"CAM0 only", map[string]string{"v4l-subdev0": "nvcsi--1", "v4l-subdev1": "tc358743 7-000f"},
			map[string]string{"video0": "vi-output, tc358743 7-000f"}, nvidiaMux, []Chip{cam0}},
		{"CAM1 only (the frozen base's DT)", map[string]string{"v4l-subdev0": "nvcsi--1", "v4l-subdev1": "tc358743 8-000f"},
			map[string]string{"video0": "vi-output, tc358743 8-000f"}, nvidiaMux,
			[]Chip{{Port: 1, Bus: 8, Name: "tc358743 8-000f", Subdev: "/dev/v4l-subdev1", Video: "/dev/video0"}}},
		{"the temporary CAM0 DT: bus 8 is channel 0", map[string]string{"v4l-subdev1": "tc358743 8-000f"},
			map[string]string{"video0": "vi-output, tc358743 8-000f"},
			map[string]string{"7": "i2c-6-mux (chan_id 1)", "8": "i2c-6-mux (chan_id 0)"},
			[]Chip{{Port: 0, Bus: 8, Name: "tc358743 8-000f", Subdev: "/dev/v4l-subdev1", Video: "/dev/video0"}}},
		{"both, CAM1's video node first", map[string]string{"v4l-subdev0": "nvcsi--1", "v4l-subdev1": "nvcsi--2",
			"v4l-subdev2": "tc358743 8-000f", "v4l-subdev3": "tc358743 7-000f"},
			map[string]string{"video0": "vi-output, tc358743 8-000f", "video1": "vi-output, tc358743 7-000f", "video2": "Cam Link 4K"}, nvidiaMux,
			[]Chip{{Port: 0, Bus: 7, Name: "tc358743 7-000f", Subdev: "/dev/v4l-subdev3", Video: "/dev/video1"},
				{Port: 1, Bus: 8, Name: "tc358743 8-000f", Subdev: "/dev/v4l-subdev2", Video: "/dev/video0"}}},
		{"unknown connector, and a subdev the VI made no node for", map[string]string{"v4l-subdev1": "tc358743 9-000f", "v4l-subdev2": "tc358743 7-000f"},
			nil, nvidiaMux,
			[]Chip{{Port: 0, Bus: 7, Name: "tc358743 7-000f", Subdev: "/dev/v4l-subdev2"},
				{Port: -1, Bus: 9, Name: "tc358743 9-000f", Subdev: "/dev/v4l-subdev1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Find(board(t, tc.subdevs, tc.videos, tc.a))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Find:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestWired(t *testing.T) {
	c0, c1 := Chip{Port: 0, Bus: 7}, Chip{Port: 1, Bus: 8}
	for _, tc := range []struct {
		chips   []Chip
		setting string
		want    Chip
		ok      bool
	}{
		{nil, WiredAuto, Chip{}, false},
		{[]Chip{c1}, WiredAuto, c1, true},
		{[]Chip{c0}, WiredAuto, c0, true},
		{[]Chip{c0, c1}, WiredAuto, c0, true},
		{[]Chip{c0, c1}, WiredCAM1, c1, true},
		{[]Chip{c0, c1}, WiredCAM0, c0, true},
		{[]Chip{c0}, WiredCAM1, Chip{}, false}, // the wired board is not there: nobody has the HDMI audio
	} {
		if got, ok := Wired(tc.chips, tc.setting); got != tc.want || ok != tc.ok {
			t.Errorf("Wired(%v, %q) = %+v %v, want %+v %v", tc.chips, tc.setting, got, ok, tc.want, tc.ok)
		}
	}
	for s, ok := range map[string]bool{"": true, "cam0": true, "cam1": true, "CAM1": false, "1": false} {
		if ValidWired(s) != ok {
			t.Errorf("ValidWired(%q) = %v", s, !ok)
		}
	}
	if (Chip{Port: 1}).Connector() != "CAM1" || (Chip{Port: -1}).Connector() != "" {
		t.Error("Connector")
	}
}

func TestReadWired(t *testing.T) {
	dir := t.TempDir()
	board, app := filepath.Join(dir, "eirl-engine"), filepath.Join(dir, "engine.env")
	os.WriteFile(board, []byte("# board\nHDMI_WIRED=cam0\nTC_I2C=8\nHDMI_WIRED=cam1\nHDMI_WIRED=cam2\n"), 0o644)
	if got := ReadWired(board); got != "cam1" {
		t.Errorf("one file: %q, want its last valid line", got)
	}
	os.WriteFile(app, []byte("HDMI_WIRED=\n"), 0o644)
	if got := ReadWired(board, app); got != "" {
		t.Errorf("the app's file puts back auto: %q", got)
	}
	if ReadWired(filepath.Join(dir, "none")) != "" {
		t.Error("no file: auto")
	}
}
