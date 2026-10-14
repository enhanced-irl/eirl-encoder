// eirl-netsetup: the root helper of the encoder's camera network (F7, NETWORK_SOURCES.md §6.2).
//
//	eirl-netsetup apply     sets up the access point, its DHCP server, the firewall and MediaMTX as
//	                        /etc/eirl-encoder/{cameras.json, network.json, ap.psk, media-api.key} ask
//
// eirl-ap.service launches it: when the board boots and when the app asks for it (`sudo -n systemctl start
// eirl-ap.service`, an exact sudoers rule). Idempotent: it compares what the files ask for with what is
// there and makes them match. With some camera, the Wi-Fi in cameras mode (if it is on in network.json) and
// MediaMTX, which with «por cable» ("over cable") also listens on eth0 (§6.11); with none, everything down.
// With reception off (§6.12), MediaMTX down and the Wi-Fi as «por wifi» ("over Wi-Fi") says.
//
// The app (user eirl-encoder) writes the files: here they are read as DATA, without following
// links, with a size cap and with netcfg's rules. Nothing they carry is executed or interpolated
// outside those allowlists (the rule of MANAGER.md §5).
//
// With EIRL_NETSETUP_DRYRUN=1 it touches nothing: it prints the commands (tests without root, like the
// uplinks dispatcher).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/enhanced-irl/eirl-encoder/manager/internal/netcfg"
)

var version = "dev"

// State: what it leaves in <run>/state.json and on stdout (the app reads it). State, Mode… belong to the
// Wi-Fi; LAN, to the wired cameras.
type State struct {
	State   string `json:"state"`          // on | off | error
	Mode    string `json:"mode,omitempty"` // normal (formerly «cameras»; the cloud will add the setup/rescue mode)
	AP      string `json:"ap,omitempty"`   // the effective AP policy: always | rescue (WIFI.md W7)
	SSID    string `json:"ssid,omitempty"`
	Channel int    `json:"channel,omitempty"`
	// Error and LANError: a stable code, "code" or "code:detail" (no_wifi, subnet_in_use:wwan0…), that the app
	// translates for the user (internal/api netcodes.go). Never free text: this helper has no languages
	Error    string `json:"error,omitempty"`
	LAN      string `json:"lan"` // on | off | error
	LANError string `json:"lanError,omitempty"`
	At       string `json:"at"` // when this apply started (RFC 3339 with ns: the app knows whether it is later than its change)
}

type env struct {
	confDir  string // /etc/eirl-encoder
	runDir   string // /run/eirl-ap
	mediaDir string // /run/eirl-media
	sysNet   string // /sys/class/net
	procSys  string // /proc/sys
	keyfile  string // NetworkManager profile
	dry      bool
	out      io.Writer
	mediaGID int // eirl-media group (reads the MediaMTX configuration)
	// ownerUID: only files owned by this user count (eirl-encoder, the app's). The directory is 3770
	// and the engine is in the group: whatever someone else plants does not count. -1 = not checked.
	ownerUID int
	// dryFail: in dry-run mode, which commands fail (tests: what happens if the firewall cannot be set up?)
	dryFail func(name string, args []string, stdin string) bool
	// dryMediaUp: in dry-run mode, eirl-media.service does not stop (tests)
	dryMediaUp bool
}

func main() {
	fs := flag.NewFlagSet("eirl-netsetup", flag.ContinueOnError)
	confDir := fs.String("config-dir", "/etc/eirl-encoder", "app configuration")
	runDir := fs.String("run-dir", "/run/eirl-ap", "access point state")
	if len(os.Args) < 2 || os.Args[1] != "apply" {
		fmt.Fprintf(os.Stderr, "eirl-netsetup %s\nusage: eirl-netsetup apply\n", version)
		os.Exit(64)
	}
	if err := fs.Parse(os.Args[2:]); err != nil || fs.NArg() > 0 {
		os.Exit(64)
	}
	e := &env{confDir: *confDir, runDir: *runDir, mediaDir: "/run/eirl-media", sysNet: "/sys/class/net", procSys: "/proc/sys",
		keyfile: netcfg.NMKeyfile, dry: os.Getenv("EIRL_NETSETUP_DRYRUN") == "1", out: os.Stdout, mediaGID: -1, ownerUID: -1}
	if g, err := lookupID("/etc/group", "eirl-media"); err == nil {
		e.mediaGID = g
	}
	if u, err := lookupID("/etc/passwd", "eirl-encoder"); err == nil {
		e.ownerUID = u
	} else if !e.dry {
		// Without the app's user there is nobody to trust: nothing counts and everything goes down
		e.ownerUID = -2
	}
	st := e.apply()
	b, _ := json.Marshal(st)
	fmt.Println(string(b))
	if st.State == "error" || st.LAN == "error" {
		os.Exit(1)
	}
}

func (e *env) apply() State {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if !e.dry {
		if err := os.MkdirAll(e.runDir, 0o755); err != nil {
			return State{State: "error", Error: "cannot_create:" + e.runDir, At: now}
		}
		// One apply at a time (the app and the board's boot)
		lk, err := os.OpenFile(filepath.Join(e.runDir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil {
			defer lk.Close()
			_ = unix.Flock(int(lk.Fd()), unix.LOCK_EX)
		}
	}
	st := e.reconcile()
	st.At = now
	e.writeState(st)
	return st
}

// reconcile: what the files ask for → the Wi-Fi, MediaMTX and its firewall, up or down. The access point
// follows its policy (WIFI.md W3: it belongs to the Wi-Fi, not to the cameras); MediaMTX, the reception.
func (e *env) reconcile() State {
	cams, err := netcfg.ParseCameras(e.read(netcfg.FileCameras))
	if err != nil {
		e.down()
		return State{State: "error", LAN: "off", Error: "cameras_invalid"}
	}
	hasCams := len(cams.Cameras) > 0
	n, okN, errN := netcfg.ParseNetwork(e.read(netcfg.FileNetwork))
	if errN != nil || (!okN && hasCams) {
		e.down()
		return State{State: "error", LAN: "off", Error: "network_invalid"}
	}
	if !okN {
		e.down() // neither a configured network nor cameras: nothing to set up
		return State{State: "off", LAN: "off"}
	}
	policy := n.APPolicy(hasCams)
	st := State{State: "off", LAN: "off", AP: policy}
	apWanted := policy == netcfg.APAlways
	// Where the cameras come in: over the Wi-Fi only with the AP requested and «por wifi»; over the cable with
	// «por cable». Both, only with cameras and reception on (§6.12)
	recv := hasCams && n.On()
	viaAP, lan := recv && apWanted && n.AP.On(), recv && n.LANOn()
	if !viaAP && !lan {
		// No reception: MediaMTX and its firewall down, first (if it was listening on 0.0.0.0, eth0 closes at
		// once); the Wi-Fi, according to its policy
		e.mediaDown()
		if apWanted {
			e.wifiOn(n, &st, false)
		} else {
			e.wifiDown()
		}
		return st
	}
	// The MediaMTX API key: if missing or invalid, the API is not opened (the cameras work the same, but
	// the app does not see their state)
	mkey, _, errK := netcfg.ParseMediaKey(e.read(netcfg.FileMediaKey))
	if errK != nil {
		mkey = ""
	}
	w := wifiOff
	if apWanted {
		w = e.wifiOn(n, &st, viaAP)
	}
	// MediaMTX: with the cable, on every IPv4, but only if its firewall is in place; otherwise, on the AP's IP
	// with the Wi-Fi up. If the Wi-Fi fails, the wired ones carry on
	listen := ""
	fwDone := false
	if lan {
		if err := e.mediaFirewall(true, viaAP); err != nil {
			st.LAN, st.LANError = "error", "lan_firewall"
		} else {
			listen, st.LAN, fwDone = netcfg.AnyAddr, "on", true
		}
	}
	if listen == "" && viaAP {
		switch w {
		case wifiUp:
			listen = netcfg.APAddr
		case wifiFailed:
			// As before §6.11, if the Wi-Fi did not come up MediaMTX stays as it was… unless it was listening on
			// all addresses (the earlier cable, or its firewall that now cannot be set up): eth0 out at once and
			// MediaMTX down. Otherwise, with the cable off and the Wi-Fi failing, eth0 stayed open (and Retry
			// did not fix it)
			fwErr := e.mediaFirewall(false, true)
			if e.mediaListensAll() || (fwErr != nil && lan) {
				e.mediaDown()
			}
			return st
		}
	}
	if listen == "" {
		e.mediaDown()
	} else {
		// Without the cable, the chain without eth0 BEFORE going back to 10.42.0.1: it cuts off anyone publishing
		// over the cable at once. If it fails it is not serious: 10.42.0.1 is only reachable from ap0 (EIRL-AP-X)
		if !fwDone {
			_ = e.mediaFirewall(false, viaAP)
		}
		if err := e.mediaUp(cams.Cameras, mkey, listen); err != nil {
			if listen == netcfg.AnyAddr {
				st.LAN, st.LANError = "error", err.Error()
			}
			if w == wifiUp {
				st.State, st.Error = "error", err.Error()
			}
		}
	}
	// The Wi-Fi off, AFTER MediaMTX: the wired cameras are not cut off
	if !apWanted {
		e.wifiDown()
	}
	return st
}

const (
	wifiOff    = iota // off in network.json, or brought down by an invalid configuration
	wifiUp            // up
	wifiFailed        // did not come up (whatever is there stays as it was)
)

// wifiOn: the access point up; fills in its part of st. media: with the cameras coming in through it (its
// firewall opens RTMP and SRT); otherwise, only DHCP, the web, the preview and ping.
func (e *env) wifiOn(n netcfg.Network, st *State, media bool) int {
	st.Mode, st.SSID = "normal", n.AP.SSID
	psk, okP, errP := netcfg.ParsePSK(e.read(netcfg.FilePSK))
	if errP != nil || !okP {
		e.wifiDown()
		st.State, st.Error = "error", "psk_invalid"
		return wifiOff
	}
	st.Channel = e.channel(n.AP.Channel)
	kf, err := netcfg.APKeyfile(n.AP.SSID, psk, st.Channel)
	if err != nil {
		e.wifiDown()
		st.State, st.Error = "error", "ap_config_invalid"
		return wifiOff
	}
	if err := e.apUp(kf, media); err != nil {
		st.State, st.Error = "error", err.Error()
		return wifiFailed
	}
	st.State = "on"
	return wifiUp
}

func (e *env) apUp(keyfile []byte, media bool) error {
	// The AP subnet taken by another link (e.g. a modem or another Linux's «shared» hotspot, which uses
	// 10.42.0.0/24 with gateway 10.42.0.1): streaming comes first, so the AP does not come up
	if out, err := e.run("ip", "-o", "-4", "addr", "show"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) >= 4 && f[1] != netcfg.APIface && strings.HasPrefix(f[3], "10.42.0.") {
				return errors.New("subnet_in_use:" + f[1])
			}
		}
	}
	if err := e.ensureIface(); err != nil {
		return err
	}
	e.noIPv6()
	// The firewall BEFORE bringing the AP up: if it cannot be set up, the AP does not come up (and if it was
	// already up, it goes down). The other way round, an iptables failure (the xtables lock with docker at
	// boot) left the Wi-Fi open to everything, SSH included
	if err := e.firewallOn(media); err != nil {
		if e.active() {
			_, _ = e.run("nmcli", "connection", "down", netcfg.NMConnID)
		}
		return errors.New("ap_firewall")
	}
	// Did the profile change? Against the fingerprint of the last applied one (in /run), not against the
	// file: if NetworkManager normalizes it, comparing it would say «changed» on every apply and the cameras
	// would reconnect for no reason
	sum := fmt.Sprintf("%x\n", sha256.Sum256(keyfile))
	sumPath := filepath.Join(e.runDir, "keyfile.sha256")
	last, _ := os.ReadFile(sumPath)
	_, statErr := os.Stat(e.keyfile)
	changed := string(last) != sum || statErr != nil || !e.profileExists()
	if changed {
		if _, err := e.writeIfChanged(e.keyfile, keyfile, 0o600, -1); err != nil {
			return errors.New("ap_profile_write")
		}
		if _, err := e.run("nmcli", "connection", "load", e.keyfile); err != nil {
			return errors.New("ap_profile_load")
		}
		if _, err := e.writeIfChanged(sumPath, []byte(sum), 0o600, -1); err != nil {
			return errors.New("cannot_write:" + sumPath)
		}
	}
	if changed || !e.active() {
		if err := e.readyIface(); err != nil {
			return err
		}
		if _, err := e.run("nmcli", "--wait", "30", "connection", "up", netcfg.NMConnID); err != nil {
			return errors.New("ap_up")
		}
	}
	e.endScanRetry()
	if _, err := e.run("systemctl", "start", "eirl-ap-dhcp.service"); err != nil {
		return errors.New("ap_dhcp")
	}
	return nil
}

// mediaUp: the MediaMTX configuration (listening on listen) and the service up.
func (e *env) mediaUp(cams []netcfg.Camera, apiKey, listen string) error {
	cfg, err := netcfg.MediaMTXConfig(cams, apiKey, listen)
	if err != nil {
		return errors.New("media_config_invalid")
	}
	if e.dry {
		fmt.Fprintf(e.out, "+ MediaMTX: RTMP and SRT on %s\n", listen)
	} else {
		if err := os.MkdirAll(e.mediaDir, 0o750); err != nil {
			return errors.New("cannot_create:" + e.mediaDir)
		}
		if e.mediaGID >= 0 {
			_ = os.Chown(e.mediaDir, 0, e.mediaGID)
		}
	}
	// MediaMTX hot-reloads its configuration when the file changes, but skips changes arriving less
	// than 1 s after its last reload (confwatcher, minInterval, v1.21): without spacing the writes, a new
	// key right after adding a camera left the old one in force
	mtx := filepath.Join(e.mediaDir, "mediamtx.yml")
	if cur, err := os.ReadFile(mtx); err == nil && !bytes.Equal(cur, cfg) && !e.dry {
		if fi, err := os.Stat(mtx); err == nil {
			if wait := reloadGap - time.Since(fi.ModTime()); wait > 0 {
				time.Sleep(wait)
			}
		}
	}
	if _, err := e.writeIfChanged(mtx, cfg, 0o640, e.mediaGID); err != nil {
		return errors.New("media_config_write")
	}
	if _, err := e.run("systemctl", "start", "eirl-media.service"); err != nil {
		return errors.New("media_start")
	}
	return nil
}

// down: everything down. Each step runs even if the previous one fails (it tries to leave the board clean).
func (e *env) down() {
	e.mediaDown()
	e.wifiDown()
}

// mediaDown: MediaMTX stopped and, afterwards, its firewall removed. If it has not stopped (a stop that
// times out), the firewall stays: it could still be listening on 0.0.0.0.
func (e *env) mediaDown() {
	_, _ = e.run("systemctl", "stop", "eirl-media.service")
	if !e.dry {
		_ = os.Remove(filepath.Join(e.mediaDir, "mediamtx.yml"))
	}
	if e.mediaActive() {
		return
	}
	e.unjump(mediaJumps)
	e.dropChains(chainM)
}

// mediaActive: eirl-media.service is still running (in dry-run mode, whatever dryMediaUp says).
func (e *env) mediaActive() bool {
	if e.dry {
		return e.dryMediaUp
	}
	_, err := e.run("systemctl", "is-active", "--quiet", "eirl-media.service")
	return err == nil
}

// mediaListensAll: the current MediaMTX configuration listens on every IPv4 (the cable on).
func (e *env) mediaListensAll() bool {
	b, err := os.ReadFile(filepath.Join(e.mediaDir, "mediamtx.yml"))
	return err == nil && bytes.Contains(b, []byte("\nrtmpAddress: "+netcfg.AnyAddr+":"))
}

// wifiDown: the AP, its DHCP server and its firewall down (MediaMTX, not).
func (e *env) wifiDown() {
	_, _ = e.run("systemctl", "stop", "eirl-ap-dhcp.service")
	if e.profileExists() {
		_, _ = e.run("nmcli", "connection", "delete", netcfg.NMConnID)
	}
	if !e.dry {
		_ = os.Remove(e.keyfile)
		_ = os.Remove(filepath.Join(e.runDir, "keyfile.sha256"))
	}
	if e.exists(netcfg.APIface) {
		_, _ = e.run("iw", "dev", netcfg.APIface, "del")
	}
	// If ap0 is still there (NetworkManager hung, iw failing), the firewall stays: without it, the Wi-Fi
	// would be open to everything
	if e.dry || !e.exists(netcfg.APIface) {
		e.firewallOff()
	}
}

// ensureIface: creates ap0 on wlan0's radio (client and AP at once: #{managed} ≤ 1,
// #{AP} ≤ 1, #channels ≤ 2) with a local MAC, and waits for NetworkManager to see it (~10 s).
func (e *env) ensureIface() error {
	if e.exists(netcfg.APIface) {
		return nil
	}
	// At boot, the card (iwlwifi) may show up after NetworkManager: up to 30 s
	for i := 0; i < 60 && !e.dry && !e.exists("wlan0"); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !e.exists("wlan0") {
		return errors.New("no_wifi")
	}
	// From the permanent MAC (the current one may be random and already local: ap0 would come out the same
	// as wlan0 and would not come up), and never equal to wlan0's current one
	base := e.readSys("wlan0", "address")
	if out, err := e.run("ethtool", "-P", "wlan0"); err == nil {
		if _, perm, ok := strings.Cut(strings.TrimSpace(out), "address: "); ok {
			base = perm
		}
	}
	mac, err := localMAC(base)
	if err != nil {
		return errors.New("wlan_mac")
	}
	if cur, err := net.ParseMAC(strings.TrimSpace(e.readSys("wlan0", "address"))); err == nil && cur.String() == mac {
		hw, _ := net.ParseMAC(mac)
		hw[5] ^= 0x01
		mac = hw.String()
	}
	if _, err := e.run("iw", "dev", "wlan0", "interface", "add", netcfg.APIface, "type", "__ap", "addr", mac); err != nil {
		return errors.New("iface_create:" + netcfg.APIface)
	}
	return nil
}

// readyIface: ap0 ready to bring up the AP profile. 🪤 #{managed} ≤ 1: NetworkManager makes every new
// Wi-Fi interface managed, and also the AP one when its profile is deactivated (also when reactivating it
// with another channel or password); with wlan0 managed the kernel does not let it come up («Device or
// resource busy») and wpa_supplicant, after two retries, leaves it unavailable. Release it, set it to __ap
// and take it again: NM has it disconnected in ~1 s (board, 13-oct). Cuts off whoever is connected.
func (e *env) readyIface() error {
	for i := 0; i < 10 && !e.dry && e.nmState() == ""; i++ {
		time.Sleep(500 * time.Millisecond) // just created: NM takes a moment to see it
	}
	for try := 0; try < 3; try++ {
		_, _ = e.run("nmcli", "device", "set", netcfg.APIface, "managed", "no")
		if !e.dry {
			time.Sleep(time.Second) // wpa_supplicant releases it at its own pace: without waiting, the 1st try fails
		}
		_, _ = e.run("ip", "link", "set", "dev", netcfg.APIface, "down") // while up, the type does not change (EBUSY)
		_, _ = e.run("iw", "dev", netcfg.APIface, "set", "type", "__ap")
		_, _ = e.run("nmcli", "device", "set", netcfg.APIface, "managed", "yes")
		if e.dry {
			return nil
		}
		for i := 0; i < 10; i++ {
			switch e.nmState() {
			case "", "unavailable", "unmanaged":
			default:
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	return errors.New("iface_busy:" + netcfg.APIface)
}

var reWpasIface = regexp.MustCompile(`^o "(/fi/w1/wpa_supplicant1/Interfaces/[0-9]+)"$`)

// endScanRetry: with the AP up, ends ap0's loop of failed scans (trap 17 of NETWORK_SOURCES.md §3).
// 🪤 When ap0 goes to disconnected, NetworkManager requests a scan. If the radio is busy (with wlan0's, which
// always scans at boot), it is queued and runs with the AP already broadcasting: the kernel rejects it (-95,
// EOPNOTSUPP) and wpa_supplicant 2.6 retries it every second while the AP stays up (one
// CTRL-EVENT-SCAN-FAILED line per second in the journal). A manual scan is not retried, and requesting one
// replaces the pending retry: it fails once, without leaving the channel or cutting off the cameras, and that is
// it. It goes over D-Bus, like nmcli, with busctl (from systemd, always present). If it fails, the AP is unchanged.
func (e *env) endScanRetry() {
	const wpas = "fi.w1.wpa_supplicant1"
	out, err := e.run("busctl", "--system", "--timeout=5", "call", wpas, "/fi/w1/wpa_supplicant1", wpas, "GetInterface", "s", netcfg.APIface)
	path := "/fi/w1/wpa_supplicant1/Interfaces/N" // in dry-run mode there is no reply
	if !e.dry {
		m := reWpasIface.FindStringSubmatch(strings.TrimSpace(out))
		if err != nil || m == nil {
			fmt.Fprintln(os.Stderr, "eirl-netsetup: wpa_supplicant does not report "+netcfg.APIface+": its scan retry is not ended")
			return
		}
		path = m[1]
	}
	// "passive" without channels: the only type that goes through the normal queue as manual (MANUAL_SCAN_REQ);
	// "active" does not remove the pending retry
	if _, err := e.run("busctl", "--system", "--timeout=5", "call", wpas, path, wpas+".Interface", "Scan", "a{sv}", "1", "Type", "s", "passive"); err != nil {
		fmt.Fprintln(os.Stderr, "eirl-netsetup: could not end the scan retry of "+netcfg.APIface)
	}
}

// noIPv6: no IPv6 on ap0. The iptables firewall only covers IPv4, and with `ipv6 ignore` the kernel still
// gives it an fe80::: SSH was reachable from the Wi-Fi (sshd listens on ::). Besides, ip6tables drops
// everything from ap0 (firewallOn), in case someone turns it back on.
func (e *env) noIPv6() {
	p := filepath.Join(e.procSys, "net/ipv6/conf", netcfg.APIface, "disable_ipv6")
	if e.dry {
		fmt.Fprintf(e.out, "+ echo 1 > %s\n", p)
		return
	}
	_ = os.WriteFile(p, []byte("1\n"), 0o644)
}

// nmState: ap0's state for NetworkManager ("" if it does not list it yet).
func (e *env) nmState() string {
	out, _ := e.run("nmcli", "-t", "-f", "DEVICE,STATE", "device")
	for _, l := range strings.Split(out, "\n") {
		if st, ok := strings.CutPrefix(l, netcfg.APIface+":"); ok {
			return st
		}
	}
	return ""
}

// localMAC: wlan0's with the locally administered address bit set (and the multicast bit cleared).
func localMAC(s string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return "", errors.New("MAC")
	}
	hw[0] = (hw[0] | 0x02) &^ 0x01
	return hw.String(), nil
}

var reFreq = regexp.MustCompile(`(?m)^\s*freq: (\d+)`)
var reChan = regexp.MustCompile(`(?m)^\s*channel (\d+) `)

// channel: the requested one or, with "auto", the Wi-Fi client's if it is on 2.4 GHz (they share the
// radio) and otherwise the least busy of 1, 6 and 11 according to NetworkManager's last scan.
func (e *env) channel(want string) int {
	if n, err := strconv.Atoi(want); err == nil {
		return n
	}
	// With the AP already on, the channel it has, ALWAYS: the AP never moves by itself (WIFI.md §3). Recomputing the
	// least busy one on every apply moved it (from 6 to 1 when rotating a key, board 13-oct), and so did following the
	// Wi-Fi client when it changes network: moving the AP drops the cameras, and one on air would go black. If the
	// client ends up on another channel, the radio is shared (the two_channels warning) until the AP is turned on again
	if out, err := e.run("iw", "dev", netcfg.APIface, "info"); err == nil && strings.Contains(out, "type AP") {
		if m := reChan.FindStringSubmatch(out); m != nil {
			if c, _ := strconv.Atoi(m[1]); c >= 1 && c <= 11 {
				return c
			}
		}
	}
	// When turning it on: the Wi-Fi client's if it is on 2.4 GHz (a single channel, without sharing the radio)
	if out, err := e.run("iw", "dev", "wlan0", "link"); err == nil {
		if m := reFreq.FindStringSubmatch(out); m != nil {
			// 12 and 13 (common in Spain) are not accepted by the profile: the radio supports two channels at once
			if f, _ := strconv.Atoi(m[1]); f >= 2412 && f <= 2462 {
				return (f - 2407) / 5
			}
		}
	}
	busy := map[int]int{1: 0, 6: 0, 11: 0}
	// What wlan0 sees: without ifname our own AP is included too (and it waits for ap0's scan, which fails)
	if out, err := e.run("nmcli", "-t", "-f", "CHAN", "device", "wifi", "list", "ifname", "wlan0"); err == nil {
		for _, l := range strings.Fields(out) {
			c, err := strconv.Atoi(l)
			if err != nil || c > 14 {
				continue
			}
			for k := range busy {
				if c >= k-2 && c <= k+2 {
					busy[k]++
				}
			}
		}
	}
	best := 1
	for _, k := range []int{6, 11} {
		if busy[k] < busy[best] {
			best = k
		}
	}
	return best
}

// --- firewall -----------------------------------------------------------------------------------

const (
	chain  = "EIRL-AP"
	chainX = "EIRL-AP-X"  // what goes to 10.42.0.1 without coming from ap0
	chainM = "EIRL-MEDIA" // the MediaMTX ports: only from lo, ap0 and (with the cable) eth0
)

// reloadGap: the minimum time between two writes of the MediaMTX configuration (its minInterval is 1 s)
const reloadGap = 1200 * time.Millisecond

// firewallOn: only what is needed gets in on ap0 (DHCP, the web, RTMP, SRT, the WebRTC preview and ping);
// the rest, SSH included, is dropped. Nothing is forwarded from or to ap0 (ip_forward stays at 0; this is
// in case someone turns it on). In IPv6, nothing at all.
func (e *env) firewallOn(media bool) error {
	ap := netcfg.APAddr
	var rules [][]string
	// RTMP and SRT, only with the cameras coming in over the Wi-Fi. Otherwise, closed BEFORE letting established
	// traffic through: a camera that was publishing over the Wi-Fi is cut off at once (its flow is still
	// «established» for conntrack, and MediaMTX may still be listening on 0.0.0.0 for the cable), whether it goes to
	// 10.42.0.1 or to any other IP of the board
	if !media {
		rules = append(rules,
			[]string{"-p", "tcp", "--dport", strconv.Itoa(netcfg.RTMPPort), "-j", "DROP"},
			[]string{"-p", "udp", "--dport", strconv.Itoa(netcfg.SRTPort), "-j", "DROP"})
	}
	rules = append(rules,
		[]string{"-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
		[]string{"-p", "udp", "--dport", "67", "-j", "ACCEPT"},
		[]string{"-d", ap, "-p", "tcp", "--dport", "80", "-j", "ACCEPT"})
	if media {
		rules = append(rules,
			[]string{"-d", ap, "-p", "tcp", "--dport", strconv.Itoa(netcfg.RTMPPort), "-j", "ACCEPT"},
			[]string{"-d", ap, "-p", "udp", "--dport", strconv.Itoa(netcfg.SRTPort), "-j", "ACCEPT"})
	}
	rules = append(rules,
		[]string{"-d", ap, "-p", "udp", "--dport", fmt.Sprintf("%d:%d", netcfg.PreviewPortMin, netcfg.PreviewPortMax), "-j", "ACCEPT"},
		[]string{"-d", ap, "-p", "icmp", "--icmp-type", "echo-request", "-j", "ACCEPT"},
		[]string{"-j", "DROP"})
	// The chain is flushed and refilled in ONE operation (iptables-restore: with separate -F and -A, and the
	// INPUT jump already in place, ap0 was left open in between on every apply). --noflush: the rest is untouched
	var b strings.Builder
	b.WriteString("*filter\n:" + chain + " - [0:0]\n:" + chainX + " - [0:0]\n")
	for _, r := range rules {
		b.WriteString("-A " + chain + " " + strings.Join(r, " ") + "\n")
	}
	// To 10.42.0.1 only from ap0 (or the board itself): Linux accepts packets to any of its IPs on any
	// interface, and MediaMTX was reachable from the LAN with a route to 10.42.0.1 via the Jetson
	b.WriteString("-A " + chainX + " -i lo -j RETURN\n-A " + chainX + " -i " + netcfg.APIface + " -j RETURN\n-A " + chainX + " -j DROP\n")
	b.WriteString("COMMIT\n")
	if err := e.restore(b.String()); err != nil {
		return err
	}
	return e.jump(fwJumps)
}

func (e *env) firewallOff() {
	e.unjump(fwJumps)
	e.dropChains(chain, chainX)
}

// mediaFirewall: RTMP and SRT (the MediaMTX ports) only from the board itself, the cameras' Wi-Fi and, with
// lan, eth0; from any other interface (the Wi-Fi client, SRTLA's USB modems and phones, docker), dropped.
// It only filters (RETURN): what arrives on ap0 is still decided by EIRL-AP. It is set whenever MediaMTX runs,
// and before it listens on 0.0.0.0 (§6.11). 🪤 The ports go INSIDE the chain and the INPUT jump has no
// match: on the board (iptables 1.6.1, L4T) `-C` and `-D` do not find a rule with `--dport` even when it is
// there (the jumps were duplicated on every apply and could not be removed).
func (e *env) mediaFirewall(lan, ap bool) error {
	var b strings.Builder
	b.WriteString("*filter\n:" + chainM + " - [0:0]\n")
	ifs := []string{"lo"}
	if ap {
		ifs = append(ifs, netcfg.APIface)
	}
	if lan {
		ifs = append(ifs, netcfg.LANIface)
	}
	for _, i := range ifs {
		b.WriteString("-A " + chainM + " -i " + i + " -j RETURN\n")
	}
	fmt.Fprintf(&b, "-A %s -p tcp --dport %d -j DROP\n-A %s -p udp --dport %d -j DROP\nCOMMIT\n", chainM, netcfg.RTMPPort, chainM, netcfg.SRTPort)
	if err := e.restore(b.String()); err != nil {
		return err
	}
	return e.jump(mediaJumps)
}

// restore: whole chains at once (iptables-restore --noflush: the rest is untouched).
func (e *env) restore(rules string) error {
	var err error
	for try := 0; try < 3; try++ {
		// iptables-restore 1.6 does not wait for the xtables lock (-w): if someone else (docker) holds it, try again
		if _, err = e.runIn(rules, "iptables-restore", "--noflush"); err == nil || e.dry {
			break
		}
		time.Sleep(time.Second)
	}
	return err
}

// jump: the jumps to our chains, at the start of the system ones (if not already there).
func (e *env) jump(js []fwJump) error {
	for _, j := range js {
		if _, err := e.run(j.cmd, append([]string{"-w", "-C"}, j.rule...)...); err == nil {
			continue
		}
		if _, err := e.run(j.cmd, append([]string{"-w", "-I", j.rule[0], "1"}, j.rule[1:]...)...); err != nil {
			return err
		}
	}
	return nil
}

func (e *env) unjump(js []fwJump) {
	for _, j := range js {
		// In case repeated rules slipped in: until none is left (with a cap)
		for i := 0; i < 8; i++ {
			if _, err := e.run(j.cmd, append([]string{"-w", "-D"}, j.rule...)...); err != nil || e.dry {
				break
			}
		}
	}
}

func (e *env) dropChains(cs ...string) {
	for _, c := range cs {
		_, _ = e.run("iptables", "-w", "-F", c)
		_, _ = e.run("iptables", "-w", "-X", c)
	}
}

type fwJump struct {
	cmd  string
	rule []string
}

// fwJumps: ap0's jumps and blocks in the system chains (chain first)
var fwJumps = []fwJump{
	{"iptables", []string{"INPUT", "-i", netcfg.APIface, "-j", chain}},
	{"iptables", []string{"INPUT", "-d", netcfg.APAddr + "/32", "-j", chainX}},
	{"iptables", []string{"FORWARD", "-i", netcfg.APIface, "-j", "DROP"}},
	{"iptables", []string{"FORWARD", "-o", netcfg.APIface, "-j", "DROP"}},
	{"ip6tables", []string{"INPUT", "-i", netcfg.APIface, "-j", "DROP"}},
	{"ip6tables", []string{"FORWARD", "-i", netcfg.APIface, "-j", "DROP"}},
	{"ip6tables", []string{"FORWARD", "-o", netcfg.APIface, "-j", "DROP"}},
}

// mediaJumps: everything incoming goes through EIRL-MEDIA, which only looks at the MediaMTX ports
var mediaJumps = []fwJump{
	{"iptables", []string{"INPUT", "-j", chainM}},
}

// --- utilities ----------------------------------------------------------------------------------

// read: a file of the app's configuration as data. Without following links (the directory is writable
// by the app), only regular files and with a size cap. Missing or odd = empty.
func (e *env) read(name string) []byte {
	fd, err := unix.Open(filepath.Join(e.confDir, name), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size > netcfg.MaxFileSize {
		return nil
	}
	if e.ownerUID != -1 && int(st.Uid) != e.ownerUID {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, netcfg.MaxFileSize))
	if err != nil {
		return nil
	}
	return b
}

func (e *env) readSys(iface, attr string) string {
	b, _ := os.ReadFile(filepath.Join(e.sysNet, iface, attr))
	return string(b)
}

func (e *env) exists(iface string) bool {
	_, err := os.Stat(filepath.Join(e.sysNet, iface))
	return err == nil
}

func (e *env) active() bool {
	out, err := e.run("nmcli", "-t", "-f", "NAME,DEVICE", "connection", "show", "--active")
	return err == nil && strings.Contains("\n"+out+"\n", "\n"+netcfg.NMConnID+":"+netcfg.APIface+"\n")
}

func (e *env) profileExists() bool {
	out, err := e.run("nmcli", "-t", "-f", "NAME", "connection", "show")
	return err == nil && strings.Contains("\n"+out+"\n", "\n"+netcfg.NMConnID+"\n")
}

// writeIfChanged: atomic write only if it changes (gid < 0 = root). Returns whether it changed.
func (e *env) writeIfChanged(path string, data []byte, mode os.FileMode, gid int) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	if e.dry {
		fmt.Fprintf(e.out, "+ write %s (%d bytes, %o)\n", path, len(data), mode)
		return true, nil
	}
	// Hidden temporary file: NetworkManager does not load files starting with a dot
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return false, err
	}
	if gid >= 0 {
		_ = f.Chown(0, gid)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, err
	}
	f.Close()
	return true, os.Rename(tmp, path)
}

func (e *env) writeState(st State) {
	b, _ := json.Marshal(st)
	if e.dry {
		fmt.Fprintf(e.out, "+ state %s\n", b)
		return
	}
	_, _ = e.writeIfChanged(filepath.Join(e.runDir, "state.json"), append(b, '\n'), 0o644, -1)
}

// run: a command with a fixed PATH and a time limit. In dry-run mode it only prints it.
func (e *env) run(name string, args ...string) (string, error) {
	if e.dry {
		fmt.Fprintf(e.out, "+ %s %s\n", name, strings.Join(args, " "))
		if (name == "iptables" || name == "ip6tables") && len(args) > 1 && args[1] == "-C" {
			return "", errors.New("dry run: rule not present") // to see what it would add
		}
		if e.dryFail != nil && e.dryFail(name, args, "") {
			return "", errors.New("dry run: failure")
		}
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runIn: like run, with data on standard input (iptables-restore).
func (e *env) runIn(stdin, name string, args ...string) (string, error) {
	if e.dry {
		fmt.Fprintf(e.out, "+ %s %s <<\n%s", name, strings.Join(args, " "), stdin)
		if e.dryFail != nil && e.dryFail(name, args, stdin) {
			return "", errors.New("dry run: failure")
		}
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// lookupID: the numeric id of a user (/etc/passwd) or group (/etc/group), third field.
func lookupID(file, name string) (int, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return -1, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 3 && f[0] == name {
			return strconv.Atoi(f[2])
		}
	}
	return -1, errors.New("not found")
}
