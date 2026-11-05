package splash

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/update/syslayer"
)

// imageConf is the extlinux.conf the image's recipe writes (tools/image/stages/rootfs.sh): the frozen base's.
const imageConf = `TIMEOUT 10
DEFAULT tc358743
MENU TITLE L4T boot options

LABEL primary
      MENU LABEL primary kernel (B01 DT with the TC358743)
      LINUX /boot/Image
      FDT /boot/tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb
      INITRD /boot/initrd
      APPEND ${cbootargs} quiet root=/dev/mmcblk0p1 rw rootwait rootfstype=ext4 console=ttyS0,115200n8 console=tty0 fbcon=map:0 net.ifnames=0

LABEL tc358743
      MENU LABEL TC358743 HDMI-in (CAM1)
      LINUX /boot/Image
      FDT /boot/tegra210-p3448-0000-p3449-0000-b00-tc358743.dtb
      INITRD /boot/initrd
      APPEND ${cbootargs} quiet root=/dev/mmcblk0p1 rw rootwait rootfstype=ext4 console=ttyS0,115200n8 console=tty0 fbcon=map:0 net.ifnames=0
`

var dual = FDTSwap{From: BaseFDT, To: DualFDT}

func TestDualFDTIsTheLayers(t *testing.T) {
	if DualFDT != syslayer.DualDTB || !syslayer.AllowedFile(DualFDT, "4.9.337-tegra") || syslayer.AllowedFile(BaseFDT, "4.9.337-tegra") {
		t.Fatal("the splash boots a device tree the system layer does not install, or the layer may replace the base's")
	}
}

func fdtsOf(conf string) map[string]string {
	out := map[string]string{}
	label := ""
	for _, l := range splitLines(conf) {
		switch k, rest := keyRest(l); k {
		case "LABEL":
			label = rest
		case "FDT":
			out[label] = rest
		}
	}
	return out
}

func TestInstallEntryDualFDT(t *testing.T) {
	out, prev, err := InstallEntry(imageConf, nil, dual)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"primary": BaseFDT, "tc358743": BaseFDT, EntryLabel: DualFDT}
	if got := fdtsOf(out); len(got) != len(want) || got["primary"] != want["primary"] ||
		got["tc358743"] != want["tc358743"] || got[EntryLabel] != want[EntryLabel] {
		t.Fatalf("FDTs %v, want %v (only our entry boots the dual DTB; U-Boot falls back to the others)", got, want)
	}
	if EntryFDT(out) != DualFDT {
		t.Fatalf("EntryFDT %q", EntryFDT(out))
	}
	// The next release: idempotent with the saved state
	again, _, err := InstallEntry(out, &prev, dual)
	if err != nil || again != out {
		t.Fatalf("reinstalling changes the file (err %v)", err)
	}
	// A release whose layer did not install it (zero swap): back to the base's DTB, the rest as it was
	base, _, err := InstallEntry(out, &prev, FDTSwap{})
	if err != nil || EntryFDT(base) != BaseFDT {
		t.Fatalf("without the swap: FDT %q (err %v)", EntryFDT(base), err)
	}
	// Undoing the splash gives the base back byte for byte
	back, err := UninstallEntry(out, &prev)
	if err != nil || back != imageConf {
		t.Fatalf("undo (err %v):\n%s", err, back)
	}
}

func TestInstallEntryDualFDTLeavesOtherBoots(t *testing.T) {
	// A board whose default entry boots another DT (built by its owner): our entry copies it as it is
	custom := strings.ReplaceAll(imageConf, "b00-tc358743.dtb", "b00-mine.dtb")
	out, _, err := InstallEntry(custom, nil, dual)
	if err != nil {
		t.Fatal(err)
	}
	if EntryFDT(out) != "/boot/tegra210-p3448-0000-p3449-0000-b00-mine.dtb" || strings.Contains(out, DualFDT) {
		t.Fatalf("a board's own DT was replaced:\n%s", out)
	}
	// The test board (Nov 5): DEFAULT is a hand-made entry that already boots the dual DTB
	board := readJetsonConf(t) + "\nLABEL eirl-splash-dual\n      LINUX /boot/Image\n      FDT " + DualFDT +
		"\n      INITRD /boot/initrd\n      APPEND ${cbootargs} quiet fbcon=map:9\n"
	board = strings.Replace(board, "DEFAULT tc358743", "DEFAULT eirl-splash-dual", 1)
	out, _, err = InstallEntry(board, nil, dual)
	if err != nil || EntryFDT(out) != DualFDT {
		t.Fatalf("test board: FDT %q (err %v)", EntryFDT(out), err)
	}
	if fdtsOf(out)["tc358743"] != BaseFDT {
		t.Fatal("the tc358743 entry was touched")
	}
}

// fdt: a minimal well-formed flattened device tree header of n bytes.
func fdt(n int) []byte {
	b := make([]byte, n)
	binary.BigEndian.PutUint32(b, 0xd00dfeed)
	binary.BigEndian.PutUint32(b[4:], uint32(n))
	return b
}

func TestLayerFDT(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, DualFDT)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	good := fdt(64)
	sum := sha256.Sum256(good)
	owned := map[string]string{DualFDT: hex.EncodeToString(sum[:])}
	if LayerFDT(root, owned) != (FDTSwap{}) {
		t.Error("no file: no swap")
	}
	os.WriteFile(p, good, 0o644)
	if LayerFDT(root, owned) != dual {
		t.Error("the layer's file: the swap")
	}
	if LayerFDT(root, nil) != (FDTSwap{}) || LayerFDT(root, map[string]string{"/lib/firmware/x": "1"}) != (FDTSwap{}) {
		t.Error("a file the layer does not own (the test board's hand-installed copy): no swap")
	}
	bad := map[string][]byte{"another file": fdt(80), "truncated": good[:48], "not a device tree": make([]byte, 64)}
	for name, b := range bad {
		os.WriteFile(p, b, 0o644)
		s := sha256.Sum256(b)
		claimed := owned
		if name != "another file" {
			claimed = map[string]string{DualFDT: hex.EncodeToString(s[:])} // the layer's sha256, but not an FDT
		}
		if LayerFDT(root, claimed) != (FDTSwap{}) {
			t.Errorf("%s: swap", name)
		}
	}
}
