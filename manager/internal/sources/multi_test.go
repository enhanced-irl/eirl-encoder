package sources

import (
	"math"
	"slices"
	"testing"
)

// Cam Link: only the camera's mode (1080p60), uncompressed. NV12 1080p60 = 186.6 MB/s (62 % of the
// 300 of USB 3).
func camLink(id string, bus int) Source {
	return Source{ID: id, Kind: KindUSB, Name: id, USB: &USBInfo{SpeedMbps: 5000, Bus: bus}, Formats: []Format{
		newFormat("NV12", modesOf(1920, 1080, r60)),
		newFormat("YUYV", modesOf(1920, 1080, r60)),
	}}
}

// Webcam: NV12 1080p30 (93 MB/s, 31 %) or MJPEG 1080p30.
func webcam(id string, bus int) Source {
	return Source{ID: id, Kind: KindUSB, Name: id, USB: &USBInfo{SpeedMbps: 5000, Bus: bus}, Formats: []Format{
		newFormat("NV12", modesOf(1920, 1080, r30)),
		newFormat("MJPG", modesOf(1920, 1080, r30)),
	}}
}

func picked(p Pick) string {
	if !p.OK {
		return p.Src.ID + " -"
	}
	return p.Src.ID + " " + p.Choice.Format + " " + p.Choice.Mode()
}

func checkPicks(t *testing.T, got []Pick, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d sources, want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if s := picked(got[i]); s != want[i] {
			t.Errorf("%d: %s, want %s", i, s, want[i])
		}
	}
}

func TestChooseAllTwoCamLinksDoNotFit(t *testing.T) {
	cat := Catalog{Sources: []Source{{ID: "hdmi", Kind: KindHDMI}, camLink("a", 2), camLink("b", 2)}}
	p := ChooseAll(cat, nil, 3)
	// The HDMI does not count; the first gets its best mode even though the second does not fit in any way
	checkPicks(t, p, "a NV12 1920x1080@60", "b -")
	if math.Abs(p[0].Share-0.622) > 0.001 {
		t.Errorf("share: %v", p[0].Share)
	}
	if !slices.Equal(p[1].Src.Warnings, []string{WarnNoBus}) {
		t.Errorf("warnings: %v", p[1].Src.Warnings)
	}
	if len(cat.Sources[2].Warnings) != 0 {
		t.Error("ChooseAll must not touch the catalogue")
	}
	// On different buses, both
	checkPicks(t, ChooseAll(Catalog{Sources: []Source{camLink("a", 2), camLink("b", 4)}}, nil, 3),
		"a NV12 1920x1080@60", "b NV12 1920x1080@60")
}

func TestChooseAllLeavesRoomForLaterSources(t *testing.T) {
	// Two webcams before a Cam Link: the second drops to MJPEG so the Cam Link (uncompressed only)
	// fits
	cat := Catalog{Sources: []Source{webcam("w1", 2), webcam("w2", 2), camLink("c", 2)}}
	p := ChooseAll(cat, nil, 3)
	checkPicks(t, p, "w1 NV12 1920x1080@30", "w2 MJPG 1920x1080@30", "c NV12 1920x1080@60")
	sum := 0.0
	for _, x := range p {
		sum += x.Share
	}
	if sum > 1 {
		t.Errorf("the bus adds up to %v", sum)
	}
}

func TestChooseAllKeep(t *testing.T) {
	// The Cam Link is already on air in NV12; a webcam that comes earlier in the catalogue is plugged
	// in: the Cam Link stays the same and first
	cat := Catalog{Sources: []Source{webcam("w", 2), camLink("c", 2)}}
	keep := []Keep{{ID: "c", Choice: Choice{Format: "NV12", Width: 1920, Height: 1080, Rate: r60}}}
	checkPicks(t, ChooseAll(cat, keep, 3), "c NV12 1920x1080@60", "w NV12 1920x1080@30")

	// On air in YUY2 (248.8 MB/s, what someone picked by hand): it is kept and the webcam drops to MJPEG
	keep[0].Choice.Format = "YUY2"
	checkPicks(t, ChooseAll(cat, keep, 3), "c YUY2 1920x1080@60", "w MJPG 1920x1080@30")

	// The Cam Link's camera switches to 4K: it no longer offers what it had → it is chosen again (and
	// cannot be: only_4k, not no_bus); it keeps first place
	cl4k := camLink("c", 2)
	cl4k.Formats = []Format{newFormat("NV12", modesOf(3840, 2160, r30))}
	cl4k.Warnings = []string{WarnOnlyAbove}
	p := ChooseAll(Catalog{Sources: []Source{webcam("w", 2), cl4k}}, keep, 3)
	checkPicks(t, p, "c -", "w NV12 1920x1080@30")
	if !slices.Equal(p[0].Src.Warnings, []string{WarnOnlyAbove}) {
		t.Errorf("warnings: %v", p[0].Src.Warnings)
	}

	// A disconnected keep takes no room
	checkPicks(t, ChooseAll(Catalog{Sources: []Source{webcam("w", 2)}}, keep, 3), "w NV12 1920x1080@30")
}

func TestChooseAllLimitAndOrder(t *testing.T) {
	broken := Source{ID: "x", Kind: KindUSB, USB: &USBInfo{SpeedMbps: 5000, Bus: 2}, Error: "busy", Warnings: []string{WarnUnread}}
	cat := Catalog{Sources: []Source{broken, webcam("w1", 2), webcam("w2", 2), webcam("w3", 2)}}
	// The ones that cannot be captured go last: with 3 it is left out
	checkPicks(t, ChooseAll(cat, nil, 3), "w1 NV12 1920x1080@30", "w2 NV12 1920x1080@30", "w3 NV12 1920x1080@30")
	p := ChooseAll(cat, nil, 4)
	checkPicks(t, p, "w1 NV12 1920x1080@30", "w2 NV12 1920x1080@30", "w3 NV12 1920x1080@30", "x -")
	if !slices.Equal(p[3].Src.Warnings, []string{WarnUnread}) {
		t.Errorf("warnings: %v", p[3].Src.Warnings)
	}
	// The one about to be opened (Keep without Choice) gets in even if the limit would leave it out, after the keep ones
	keep := []Keep{{ID: "w2", Choice: Choice{Format: "NV12", Width: 1920, Height: 1080, Rate: r30}}, {ID: "w3"}}
	checkPicks(t, ChooseAll(cat, keep, 2), "w2 NV12 1920x1080@30", "w3 NV12 1920x1080@30")
	if len(ChooseAll(Catalog{Sources: []Source{{ID: "hdmi", Kind: KindHDMI}}}, nil, 3)) != 0 {
		t.Error("without USB, none")
	}
}

func TestChooseAllFakeBoard(t *testing.T) {
	// Cam Link on bus 2 (USB 3) and MS2109 on bus 1 (USB 2): separate budgets
	s, _ := fakeBoard(t, "5000")
	cat := s.Scan()
	if cat.Sources[1].USB.Bus != 2 || cat.Sources[2].USB.Bus != 1 {
		t.Fatalf("busnum: %+v %+v", cat.Sources[1].USB, cat.Sources[2].USB)
	}
	checkPicks(t, ChooseAll(cat, nil, 3),
		"/dev/v4l/by-id/usb-Elgato_Cam_Link_4K_0005AB12-video-index0 NV12 1920x1080@60",
		"/dev/v4l/by-path/platform-70090000.xusb-usb-0:2.3:1.0-video-index0 MJPG 1920x1080@30")
}
