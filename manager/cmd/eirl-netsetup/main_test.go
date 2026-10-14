package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

const psk = "AbCdEfGhIjKlMnOpQrSt"

func testEnv(t *testing.T) (*env, *bytes.Buffer) {
	t.Helper()
	d := t.TempDir()
	sys := filepath.Join(d, "sys")
	os.MkdirAll(filepath.Join(sys, "wlan0"), 0o755)
	os.WriteFile(filepath.Join(sys, "wlan0", "address"), []byte("40:1c:83:81:09:cf\n"), 0o644)
	conf := filepath.Join(d, "conf")
	os.MkdirAll(conf, 0o755)
	var out bytes.Buffer
	return &env{confDir: conf, runDir: filepath.Join(d, "run"), mediaDir: filepath.Join(d, "media"), sysNet: sys, procSys: filepath.Join(d, "proc"),
		keyfile: filepath.Join(d, "eirl-ap"), dry: true, out: &out, mediaGID: -1, ownerUID: os.Getuid()}, &out
}

func writeConf(t *testing.T, e *env, ncams int) {
	t.Helper()
	writeNet(t, e, ncams, true, false)
}

// writeNet: the cameras and the network with its two switches (Wi-Fi, cable)
func writeNet(t *testing.T, e *env, ncams int, wifi, lan bool) {
	t.Helper()
	cs := netcfg.Cameras{}
	for i := 1; i <= ncams; i++ {
		cs.Cameras = append(cs.Cameras, netcfg.Camera{ID: "cam" + string(rune('0'+i)), Name: "Phone", Key: psk, CreatedAt: time.Now()})
	}
	os.WriteFile(filepath.Join(e.confDir, netcfg.FileCameras), cs.Render(), 0o640)
	n := netcfg.Network{AP: netcfg.AP{SSID: "EnhancedIRL-1A2B", Channel: "auto", Enabled: &wifi}, LAN: &netcfg.LAN{Enabled: lan}}
	os.WriteFile(filepath.Join(e.confDir, netcfg.FileNetwork), n.Render(), 0o640)
	os.WriteFile(filepath.Join(e.confDir, netcfg.FilePSK), []byte(psk+"\n"), 0o640)
	os.WriteFile(filepath.Join(e.confDir, netcfg.FileMediaKey), []byte(apiKey+"\n"), 0o640)
}

const apiKey = "ApiKeyApiKeyApiKeyApiKeyApiKey12"

func TestNoCamerasAllDown(t *testing.T) {
	e, out := testEnv(t)
	st := e.apply()
	if st.State != "off" || st.LAN != "off" {
		t.Fatalf("state %+v", st)
	}
	s := out.String()
	for _, want := range []string{"+ systemctl stop eirl-media.service\n", "+ systemctl stop eirl-ap-dhcp.service\n", "+ iptables -w -X EIRL-AP\n",
		"+ iptables -w -X EIRL-AP-X", "+ iptables -w -X EIRL-MEDIA", "+ iptables -w -D INPUT -j EIRL-MEDIA\n",
		"+ ip6tables -w -D INPUT -i ap0 -j DROP"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "interface add") {
		t.Error("without cameras ap0 is not created")
	}
}

func TestWithCameraAPUp(t *testing.T) {
	e, out := testEnv(t)
	writeConf(t, e, 1)
	st := e.apply()
	if st.State != "on" || st.Mode != "normal" || st.AP != "always" || st.SSID != "EnhancedIRL-1A2B" || st.Channel != 1 || st.LAN != "off" {
		t.Fatalf("state %+v\n%s", st, out)
	}
	s := out.String()
	for _, want := range []string{
		"+ iw dev wlan0 interface add ap0 type __ap addr 42:1c:83:81:09:cf",
		"+ nmcli connection load " + e.keyfile,
		"+ iw dev ap0 set type __ap",
		"+ nmcli --wait 30 connection up eirl-ap",
		"+ iptables-restore --noflush <<\n*filter\n:EIRL-AP - [0:0]\n",
		"-A EIRL-AP -d 10.42.0.1 -p tcp --dport 1935 -j ACCEPT\n",
		"-A EIRL-AP -d 10.42.0.1 -p udp --dport 8890 -j ACCEPT\n",
		"-A EIRL-AP -j DROP\n",
		"-A EIRL-AP-X -i lo -j RETURN\n-A EIRL-AP-X -i ap0 -j RETURN\n-A EIRL-AP-X -j DROP\nCOMMIT\n",
		"+ iptables -w -I INPUT 1 -d 10.42.0.1/32 -j EIRL-AP-X",
		"+ iptables -w -I INPUT 1 -i ap0 -j EIRL-AP",
		"+ iptables -w -I FORWARD 1 -i ap0 -j DROP",
		"+ ip6tables -w -I INPUT 1 -i ap0 -j DROP",
		"net/ipv6/conf/ap0/disable_ipv6",
		"mediamtx.yml",
		"+ systemctl start eirl-ap-dhcp.service\n",
		"+ systemctl start eirl-media.service\n",
		// The MediaMTX ports, only from the board and the Wi-Fi; without the cable, not eth0
		"*filter\n:EIRL-MEDIA - [0:0]\n-A EIRL-MEDIA -i lo -j RETURN\n-A EIRL-MEDIA -i ap0 -j RETURN\n" +
			"-A EIRL-MEDIA -p tcp --dport 1935 -j DROP\n-A EIRL-MEDIA -p udp --dport 8890 -j DROP\nCOMMIT\n",
		"+ iptables -w -I INPUT 1 -j EIRL-MEDIA\n",
		"+ MediaMTX: RTMP and SRT on 10.42.0.1\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, psk) {
		t.Error("the password shows up in the output")
	}
	if strings.Contains(s, "--dport 22") {
		t.Error("SSH open on the AP")
	}
	// The firewall and IPv6 off, BEFORE bringing the AP up (if they fail, the Wi-Fi does not open)
	up := strings.Index(s, "connection up")
	if i := strings.Index(s, "iptables-restore"); i < 0 || i > up || strings.Index(s, "disable_ipv6") > up {
		t.Error("the AP comes up before the firewall")
	}
	if strings.Contains(s, apiKey) {
		t.Error("the API key shows up in the output")
	}
	if strings.Contains(s, "eth0") {
		t.Error("without the cable, eth0 is in nothing")
	}
}

// Wi-Fi and cable (§6.11): the chain with eth0 BEFORE MediaMTX listens on 0.0.0.0
func TestWifiAndCable(t *testing.T) {
	e, out := testEnv(t)
	writeNet(t, e, 1, true, true)
	st := e.apply()
	if st.State != "on" || st.LAN != "on" {
		t.Fatalf("state %+v\n%s", st, out)
	}
	s := out.String()
	chainEth := "-A EIRL-MEDIA -i lo -j RETURN\n-A EIRL-MEDIA -i ap0 -j RETURN\n-A EIRL-MEDIA -i eth0 -j RETURN\n-A EIRL-MEDIA -p tcp --dport 1935 -j DROP\n"
	fw, listen := strings.Index(s, chainEth), strings.Index(s, "+ MediaMTX: RTMP and SRT on 0.0.0.0\n")
	if fw < 0 || listen < 0 || fw > listen {
		t.Errorf("firewall %d, listen %d:\n%s", fw, listen, s)
	}
	if !strings.Contains(s, "+ nmcli --wait 30 connection up eirl-ap") || !strings.Contains(s, "+ systemctl start eirl-ap-dhcp.service") {
		t.Error("the Wi-Fi does not come up")
	}
	if strings.Contains(s, "eth0 -j ACCEPT") || strings.Contains(s, "-o eth0") {
		t.Error("the cable only lets incoming traffic continue: it neither opens nor forwards")
	}
}

// Cable only: no Wi-Fi (nor password), MediaMTX on 0.0.0.0 and the Wi-Fi down AFTERWARDS
func TestCableOnly(t *testing.T) {
	e, out := testEnv(t)
	writeNet(t, e, 1, false, true)
	os.Remove(filepath.Join(e.confDir, netcfg.FilePSK))
	st := e.apply()
	if st.State != "off" || st.LAN != "on" || st.SSID != "" {
		t.Fatalf("state %+v\n%s", st, out)
	}
	s := out.String()
	if strings.Contains(s, "interface add") || strings.Contains(s, "connection up") || strings.Contains(s, "start eirl-ap-dhcp") {
		t.Errorf("the Wi-Fi comes up without being asked:\n%s", s)
	}
	start, stop := strings.Index(s, "+ systemctl start eirl-media.service"), strings.Index(s, "+ systemctl stop eirl-ap-dhcp.service")
	if start < 0 || stop < start || !strings.Contains(s, "-A EIRL-MEDIA -i eth0 -j RETURN") || !strings.Contains(s, "on 0.0.0.0") {
		t.Errorf("MediaMTX over the cable, then the Wi-Fi down:\n%s", s)
	}
	if strings.Contains(s, "stop eirl-media") {
		t.Error("it stopped MediaMTX")
	}
}

// Neither Wi-Fi nor cable (the app does not allow it, but the file rules): everything down
func TestNeitherWifiNorCable(t *testing.T) {
	e, out := testEnv(t)
	writeNet(t, e, 1, false, false)
	st := e.apply()
	if st.State != "off" || st.LAN != "off" {
		t.Fatalf("state %+v", st)
	}
	if s := out.String(); !strings.Contains(s, "stop eirl-media") || strings.Contains(s, "start eirl-media") {
		t.Errorf("everything should be down:\n%s", s)
	}
}

// Reception off (§6.12) with Wi-Fi and cable on: MediaMTX and its firewall down (before anything else) and
// the board's Wi-Fi UP, with its DHCP server; without «por wifi», everything down
func TestReceptionOff(t *testing.T) {
	e, out := testEnv(t)
	writeNet(t, e, 1, true, true)
	n, _, _ := netcfg.ParseNetwork(e.read(netcfg.FileNetwork))
	off := false
	n.Enabled = &off
	os.WriteFile(filepath.Join(e.confDir, netcfg.FileNetwork), n.Render(), 0o640)
	st := e.apply()
	if st.State != "on" || st.LAN != "off" || st.SSID != "EnhancedIRL-1A2B" {
		t.Fatalf("state %+v", st)
	}
	s := out.String()
	stop, up := strings.Index(s, "+ systemctl stop eirl-media.service\n"), strings.Index(s, "connection up")
	if stop < 0 || up < stop || !strings.Contains(s, "+ iptables -w -X EIRL-MEDIA") || !strings.Contains(s, "start eirl-ap-dhcp") {
		t.Errorf("MediaMTX down and then the Wi-Fi up:\n%s", s)
	}
	for _, bad := range []string{"start eirl-media", "stop eirl-ap-dhcp", "-i eth0 -j RETURN"} {
		if strings.Contains(s, bad) {
			t.Errorf("reception off, but %q:\n%s", bad, s)
		}
	}
	// Without «por wifi»: everything down
	e, out = testEnv(t)
	writeNet(t, e, 1, false, true)
	n, _, _ = netcfg.ParseNetwork(e.read(netcfg.FileNetwork))
	n.Enabled = &off
	os.WriteFile(filepath.Join(e.confDir, netcfg.FileNetwork), n.Render(), 0o640)
	if st := e.apply(); st.State != "off" || st.LAN != "off" {
		t.Fatalf("no Wi-Fi: %+v", st)
	}
	if s := out.String(); !strings.Contains(s, "stop eirl-ap-dhcp") || strings.Contains(s, "start eirl-media") || strings.Contains(s, "interface add") {
		t.Errorf("no Wi-Fi, everything down:\n%s", s)
	}
}

// If the chain with eth0 cannot be set up, MediaMTX does not move to 0.0.0.0: with the Wi-Fi, it stays on
// 10.42.0.1; without it, down
func TestCableWithoutFirewall(t *testing.T) {
	failEth := func(name string, _ []string, stdin string) bool {
		return name == "iptables-restore" && strings.Contains(stdin, "-i eth0")
	}
	e, out := testEnv(t)
	e.dryFail = failEth
	writeNet(t, e, 1, true, true)
	st := e.apply()
	if st.State != "on" || st.LAN != "error" || st.LANError == "" {
		t.Fatalf("state %+v", st)
	}
	if s := out.String(); strings.Contains(s, "on 0.0.0.0") || !strings.Contains(s, "+ MediaMTX: RTMP and SRT on 10.42.0.1") {
		t.Errorf("listening without a firewall:\n%s", s)
	}
	e, out = testEnv(t)
	e.dryFail = failEth
	writeNet(t, e, 1, false, true)
	st = e.apply()
	if st.LAN != "error" || strings.Contains(out.String(), "+ MediaMTX:") || !strings.Contains(out.String(), "stop eirl-media") {
		t.Fatalf("cable only without a firewall: %+v\n%s", st, out)
	}
}

// If the Wi-Fi does not come up and there is cable, the wired ones carry on (MediaMTX on 0.0.0.0)
func TestWifiFailsCableStays(t *testing.T) {
	e, out := testEnv(t)
	e.dryFail = func(name string, args []string, _ string) bool {
		return name == "nmcli" && strings.Contains(strings.Join(args, " "), "connection up")
	}
	writeNet(t, e, 1, true, true)
	st := e.apply()
	if st.State != "error" || st.LAN != "on" || !strings.Contains(out.String(), "on 0.0.0.0") {
		t.Fatalf("state %+v\n%s", st, out)
	}
	// Without cable, as before: MediaMTX is not touched
	e, out = testEnv(t)
	e.dryFail = func(name string, args []string, _ string) bool {
		return name == "nmcli" && strings.Contains(strings.Join(args, " "), "connection up")
	}
	writeConf(t, e, 1)
	if st := e.apply(); st.State != "error" || strings.Contains(out.String(), "eirl-media") {
		t.Fatalf("no cable: %+v\n%s", st, out)
	}
}

// With the AP up, a manual (passive) scan on ap0 ends wpa_supplicant's retry (trap 17); if the AP does not come
// up, nothing
func TestEndsScanRetry(t *testing.T) {
	scan := "+ busctl --system --timeout=5 call fi.w1.wpa_supplicant1 /fi/w1/wpa_supplicant1/Interfaces/N fi.w1.wpa_supplicant1.Interface Scan a{sv} 1 Type s passive\n"
	e, out := testEnv(t)
	writeConf(t, e, 1)
	if st := e.apply(); st.State != "on" {
		t.Fatalf("state %+v", st)
	}
	s := out.String()
	up, get, sc, dhcp := strings.Index(s, "connection up eirl-ap"), strings.Index(s, "GetInterface s ap0\n"), strings.Index(s, scan), strings.Index(s, "start eirl-ap-dhcp")
	if up < 0 || get < up || sc < get || dhcp < sc {
		t.Errorf("the manual scan goes after bringing the AP up:\n%s", s)
	}
	e, out = testEnv(t)
	e.dryFail = func(name string, args []string, _ string) bool {
		return name == "nmcli" && strings.Contains(strings.Join(args, " "), "connection up")
	}
	writeConf(t, e, 1)
	if st := e.apply(); st.State != "error" || strings.Contains(out.String(), "busctl") {
		t.Fatalf("without the AP wpa_supplicant is not touched: %+v\n%s", st, out)
	}
}

// If wlan0's current MAC is already local (random), ap0 cannot end up the same
func TestMACDiffersFromWlan0(t *testing.T) {
	e, out := testEnv(t)
	os.WriteFile(filepath.Join(e.sysNet, "wlan0", "address"), []byte("42:1c:83:81:09:cf\n"), 0o644)
	if err := e.ensureIface(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "add ap0 type __ap addr 42:1c:83:81:09:ce") {
		t.Errorf("ap0 MAC:\n%s", out)
	}
}

func TestInvalidFiles(t *testing.T) {
	e, _ := testEnv(t)
	writeConf(t, e, 1)
	os.WriteFile(filepath.Join(e.confDir, netcfg.FilePSK), []byte("short"), 0o640)
	if st := e.apply(); st.State != "error" {
		t.Fatalf("invalid psk: %+v", st)
	}
	os.Remove(filepath.Join(e.confDir, netcfg.FileNetwork))
	os.WriteFile(filepath.Join(e.confDir, netcfg.FilePSK), []byte(psk), 0o640)
	if st := e.apply(); st.State != "error" {
		t.Fatalf("no network.json: %+v", st)
	}
	os.WriteFile(filepath.Join(e.confDir, netcfg.FileCameras), []byte(`{"v":1,"cameras":[{"id":"cam1","name":"x","key":"`+psk+`","runOnReady":"sh"}]}`), 0o640)
	if st := e.apply(); st.State != "error" || strings.Contains(st.Error, "runOnReady") {
		t.Fatalf("unknown field: %+v", st)
	}
}

// The directory is writable by the app: a link or a FIFO is not followed (as if it were not there)
func TestDoesNotFollowLinksOrFIFOs(t *testing.T) {
	e, _ := testEnv(t)
	secret := filepath.Join(t.TempDir(), "other")
	os.WriteFile(secret, []byte("{}"), 0o600)
	os.Symlink(secret, filepath.Join(e.confDir, netcfg.FileCameras))
	if b := e.read(netcfg.FileCameras); b != nil {
		t.Fatal("it followed the link")
	}
	if err := syscall.Mkfifo(filepath.Join(e.confDir, netcfg.FileNetwork), 0o600); err == nil {
		if b := e.read(netcfg.FileNetwork); b != nil {
			t.Fatal("it read a FIFO")
		}
	}
	big := bytes.Repeat([]byte("x"), netcfg.MaxFileSize+1)
	os.WriteFile(filepath.Join(e.confDir, netcfg.FilePSK), big, 0o600)
	if b := e.read(netcfg.FilePSK); b != nil {
		t.Fatal("it read a file that is too big")
	}
}

func TestLocalMAC(t *testing.T) {
	if m, _ := localMAC("41:1c:83:81:09:cf"); m != "42:1c:83:81:09:cf" {
		t.Errorf("local MAC: %s", m)
	}
	if _, err := localMAC("nothing"); err == nil {
		t.Error("invalid MAC")
	}
}

// A file with another owner (e.g. planted by the engine, which is in the group) does not count
func TestOnlyAppUserFiles(t *testing.T) {
	e, _ := testEnv(t)
	writeConf(t, e, 1)
	e.ownerUID = os.Getuid() + 1
	if st := e.apply(); st.State != "off" {
		t.Fatalf("it should ignore files with another owner: %+v", st)
	}
}

// The jumps to our chains, without a match: on the board (iptables 1.6.1) `-C` and `-D` do not find a rule
// with `-p`/`--dport` even when it is there, and they were duplicated on every apply with no way to remove them
func TestJumpsWithoutMatch(t *testing.T) {
	for _, j := range append(append([]fwJump{}, fwJumps...), mediaJumps...) {
		for _, a := range j.rule {
			if a == "-p" || a == "-m" || strings.HasPrefix(a, "--") {
				t.Errorf("jump with a match: %v", j.rule)
			}
		}
	}
}

// Cable off with the Wi-Fi failing (review of 13-oct): if MediaMTX was listening on all addresses, eth0 out at
// once and MediaMTX down; if it was listening on 10.42.0.1, as before (not touched)
func TestCableOffWithWifiFailing(t *testing.T) {
	failUp := func(name string, args []string, _ string) bool {
		return name == "nmcli" && strings.Contains(strings.Join(args, " "), "connection up")
	}
	e, out := testEnv(t)
	e.dryFail = failUp
	writeNet(t, e, 1, true, false)
	os.MkdirAll(e.mediaDir, 0o755)
	os.WriteFile(filepath.Join(e.mediaDir, "mediamtx.yml"), []byte("logLevel: warn\nrtmpAddress: 0.0.0.0:1935\n"), 0o640)
	st := e.apply()
	s := out.String()
	if st.State != "error" || st.LAN != "off" {
		t.Fatalf("state %+v", st)
	}
	fw, stop := strings.Index(s, ":EIRL-MEDIA - [0:0]\n-A EIRL-MEDIA -i lo -j RETURN\n-A EIRL-MEDIA -i ap0 -j RETURN\n-A EIRL-MEDIA -p tcp"), strings.Index(s, "+ systemctl stop eirl-media.service")
	if fw < 0 || stop < fw || strings.Contains(s, "-i eth0") {
		t.Errorf("eth0 still open or MediaMTX still on all addresses:\n%s", s)
	}
	// Listening on 10.42.0.1: MediaMTX is not touched
	e, out = testEnv(t)
	e.dryFail = failUp
	writeNet(t, e, 1, true, false)
	os.MkdirAll(e.mediaDir, 0o755)
	os.WriteFile(filepath.Join(e.mediaDir, "mediamtx.yml"), []byte("logLevel: warn\nrtmpAddress: 10.42.0.1:1935\n"), 0o640)
	if st := e.apply(); st.State != "error" || strings.Contains(out.String(), "eirl-media.service") {
		t.Fatalf("on 10.42.0.1: %+v\n%s", st, out)
	}
}

// If MediaMTX does not stop, its firewall stays (it could still be listening on 0.0.0.0)
func TestMediaMTXThatDoesNotStop(t *testing.T) {
	e, out := testEnv(t)
	e.dryMediaUp = true
	st := e.apply()
	if st.State != "off" || strings.Contains(out.String(), "-X EIRL-MEDIA") || strings.Contains(out.String(), "-D INPUT -j EIRL-MEDIA") {
		t.Fatalf("it removed the firewall with MediaMTX running: %+v\n%s", st, out)
	}
}

// setPolicy: «Crear mi wifi» chosen in network.json (WIFI.md §2)
func setPolicy(t *testing.T, e *env, policy string) {
	t.Helper()
	n, _, err := netcfg.ParseNetwork(e.read(netcfg.FileNetwork))
	if err != nil {
		t.Fatal(err)
	}
	n.WiFi = &netcfg.WiFi{AP: policy}
	os.WriteFile(filepath.Join(e.confDir, netcfg.FileNetwork), n.Render(), 0o640)
}

// W-A: the AP with «Crear mi wifi» and no cameras: up with its DHCP server, no MediaMTX and RTMP and SRT closed on ap0
func TestAPWithoutCameras(t *testing.T) {
	e, out := testEnv(t)
	writeNet(t, e, 0, true, false)
	setPolicy(t, e, netcfg.APAlways)
	st := e.apply()
	if st.State != "on" || st.Mode != "normal" || st.AP != "always" || st.LAN != "off" {
		t.Fatalf("state %+v\n%s", st, out)
	}
	s := out.String()
	for _, want := range []string{"interface add ap0", "connection up", "start eirl-ap-dhcp", "stop eirl-media.service",
		"-A EIRL-AP -d 10.42.0.1 -p tcp --dport 80 -j ACCEPT"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	for _, bad := range []string{"start eirl-media", "--dport 1935 -j ACCEPT", "--dport 8890 -j ACCEPT"} {
		if strings.Contains(s, bad) {
			t.Errorf("no cameras, but %q:\n%s", bad, s)
		}
	}
	// Not chosen and no cameras: as before, everything down
	e, out = testEnv(t)
	writeNet(t, e, 0, true, false)
	if st := e.apply(); st.State != "off" || st.AP != "rescue" || strings.Contains(out.String(), "interface add") {
		t.Errorf("neither chosen nor cameras: %+v\n%s", st, out)
	}
}

// W-A (W3): cameras do not force the AP. With «Crear mi wifi» off, neither AP nor MediaMTX over the Wi-Fi; with the
// cable, the wired ones carry on
func TestRescueWithCameras(t *testing.T) {
	e, out := testEnv(t)
	writeNet(t, e, 1, true, false)
	setPolicy(t, e, netcfg.APRescue)
	if st := e.apply(); st.State != "off" || st.AP != "rescue" || st.LAN != "off" {
		t.Fatalf("state %+v", st)
	}
	if s := out.String(); strings.Contains(s, "interface add") || strings.Contains(s, "start eirl-media") || !strings.Contains(s, "stop eirl-ap-dhcp") {
		t.Errorf("rescue with cameras over the Wi-Fi: everything down\n%s", s)
	}
	e, out = testEnv(t)
	writeNet(t, e, 1, true, true)
	setPolicy(t, e, netcfg.APRescue)
	st := e.apply()
	s := out.String()
	if st.State != "off" || st.LAN != "on" || !strings.Contains(s, "on 0.0.0.0") || strings.Contains(s, "-A EIRL-MEDIA -i ap0") || strings.Contains(s, "interface add") {
		t.Errorf("rescue with cable: MediaMTX over the cable, no AP\n%+v\n%s", st, s)
	}
}

// W-A: the AP up with cameras only over the cable («por wifi» off): RTMP and SRT closed on ap0, in both chains
func TestAPWithCamerasOnlyByCable(t *testing.T) {
	e, out := testEnv(t)
	writeNet(t, e, 1, false, true)
	setPolicy(t, e, netcfg.APAlways)
	st := e.apply()
	s := out.String()
	if st.State != "on" || st.LAN != "on" || !strings.Contains(s, "on 0.0.0.0") {
		t.Fatalf("state %+v\n%s", st, s)
	}
	for _, bad := range []string{"--dport 1935 -j ACCEPT", "--dport 8890 -j ACCEPT", "-A EIRL-MEDIA -i ap0 -j RETURN"} {
		if strings.Contains(s, bad) {
			t.Errorf("the cameras do not come in over the Wi-Fi, but %q:\n%s", bad, s)
		}
	}
	// And what is already established over the Wi-Fi is cut: the RTMP and SRT DROP goes before EIRL-AP's ESTABLISHED
	drop, est := strings.Index(s, "-A EIRL-AP -p udp --dport 8890 -j DROP"), strings.Index(s, "-A EIRL-AP -m state --state ESTABLISHED,RELATED -j ACCEPT")
	if drop < 0 || est < 0 || drop > est {
		t.Errorf("the SRT DROP does not go before the ESTABLISHED:\n%s", s)
	}
	if !strings.Contains(s, "-A EIRL-MEDIA -i eth0 -j RETURN") {
		t.Errorf("without the cable in EIRL-MEDIA:\n%s", s)
	}
}
