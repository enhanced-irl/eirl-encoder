#!/bin/bash
# tools/ota-sim/system.sh — runs INSIDE the container of tools/system-sim.sh: the root filesystem of a published SD card
# image (a real encoder system: L4T, dpkg, systemd's systemctl offline), without systemd running. The update helper
# runs for real, with the fake systemctl and sudo of tools/ota-sim/shims (the app runs for real as eirl-encoder).
# Scenarios of the system layer (UPDATES.md §12): A (the code of an older release, no layer) installed by hand, then
#   1. a power cut in the middle of B's system layer (packages being installed): recovered at boot;
#   2. A → B by A's (older) helper: the layer is applied, the update installed;
#   3. B → C, whose app never starts: the app is rolled back, the committed layer stays (forward only);
#   4. B → E, whose layer has a file that does not match it: refused before changing anything, rolled back;
#   5. streaming: refused before anything.
set -uo pipefail
cd /
fail() { echo "FAIL: $*"; tail -40 /var/log/eirl-update.log 2>/dev/null; exit 1; }
ver() { head -1 /usr/lib/eirl-encoder/VERSION 2>/dev/null; }
helper_state() { python3 -c 'import json;d=json.load(open("/var/lib/eirl-update/status.json"));print(d["state"], d.get("error",""), d.get("to",""), "|", d.get("reason","")[:160])'; }
layer() { python3 -c 'import json,sys;d=json.load(open("/var/lib/eirl-system/state.json"));print(d.get("release",""), "|", d.get("error",""), "|", d.get("failed",""))' 2>/dev/null; }
pkg() { dpkg-query -W -f '${db:Status-Abbrev}' "$1" 2>/dev/null | cut -c2; }
# stage NAME: what the app leaves in its update directory for the helper (a verified download and the request)
stage() {
  local d=/s/rel/$1 u=/var/lib/eirl-encoder/update
  local v; v=$(python3 -c 'import json;print(json.load(open("'"$d"'/manifest.json"))["version"])')
  install -d -o eirl-encoder -g eirl-encoder -m 0750 /var/lib/eirl-encoder $u
  install -o eirl-encoder -g eirl-encoder -m 0640 "$d/manifest.json" $u/manifest.json
  install -o eirl-encoder -g eirl-encoder -m 0640 "$d/manifest.json.sig" $u/manifest.json.sig
  install -o eirl-encoder -g eirl-encoder -m 0640 "$d"/eirl-encoder-*-linux-arm64.tar.gz $u/bundle.tar.gz
  printf '{"version":"%s","requestedAt":"2026-10-21T00:00:00Z"}' "$v" > /tmp/req
  install -o eirl-encoder -g eirl-encoder -m 0640 /tmp/req $u/request.json
  echo "$v"
}
helper() { /usr/local/sbin/eirl-update -health-timeout 25s -settle 3s install >> /var/log/eirl-update.log 2>&1; }

# The board: systemd's systemctl stays as systemctl.real (unit files change offline), the rest is faked
[ -x /bin/systemctl ] && ! grep -q 'Fake systemctl' /bin/systemctl && mv /bin/systemctl /bin/systemctl.real
cp /s/sim/shims/* /bin/
mkdir -p /var/log /run
cat > /etc/default/eirl-encoderd <<'OPTS'
EIRL_ENCODERD_OPTS="-listen 127.0.0.1:8080 -cloud-api= -live-max 0 -update-url http://127.0.0.1:8000/manifest.json -update-check=false"
OPTS
echo "base: $(grep -o 'VERSION="[^"]*"' /usr/lib/os-release) · release $(ver) · usbmuxd $(pkg usbmuxd)"

echo "=== 0. A by hand (an older release's code: its helper knows no system layer)"
mkdir -p /tmp/a && tar -xzf /s/rel/A/eirl-encoder-*-linux-arm64.tar.gz -C /tmp/a
/tmp/a/install.sh > /var/log/install-a.log 2>&1 || { tail -20 /var/log/install-a.log; fail "install.sh of A"; }
A=$(ver); echo "A = $A; helper: $(/usr/local/sbin/eirl-update -version 2>/dev/null || echo old)"
/usr/local/sbin/eirl-update system status >/dev/null 2>&1 && fail "A's helper already knows the system layer"
printf '%s\n' test-password-123 | /bin/asuser eirl-encoder /usr/local/bin/eirl-encoderd passwd >/dev/null 2>&1
systemctl restart eirl-encoderd.service
sleep 2

echo "=== 1. power cut in the middle of B's system layer (A's helper runs B's install.sh)"
B=$(stage B)
# dpkg waits after unpacking (before configuring) so that the "power" is cut with the packages half installed
echo 'post-invoke="sleep 30"' > /etc/dpkg/dpkg.cfg.d/zz-eirl-sim-slow
helper &
for i in $(seq 1 900); do [ "$(dpkg-query -W -f '${db:Status-Abbrev}' usbmuxd 2>/dev/null)" = "iU " ] && break; sleep 0.1; done
[ -f /var/lib/eirl-system/in-progress.json ] || fail "the layer never started"
[ "$(dpkg-query -W -f '${db:Status-Abbrev}' usbmuxd 2>/dev/null)" = "iU " ] || fail "usbmuxd never got unpacked"
# The power goes: every process of the update dies at once
pkill -9 -f '^/usr/local/sbin/eirl-update' ; pkill -9 -f 'install.sh' ; pkill -9 -f apt-get ; pkill -9 -f '^/usr/bin/dpkg' ; pkill -9 -x sleep
sleep 1
rm -f /etc/dpkg/dpkg.cfg.d/zz-eirl-sim-slow
echo "cut with usbmuxd $(dpkg-query -W -f '${db:Status-Abbrev}' usbmuxd) (unpacked, not configured)"
echo "cut: helper marker $([ -f /var/lib/eirl-update/in-progress.json ] && echo yes || echo no), layer marker $([ -f /var/lib/eirl-system/in-progress.json ] && echo yes || echo no), ipheth.ko $([ -f /lib/modules/4.9.337-tegra/updates/eirl-modems/ipheth.ko ] && echo yes || echo no)"
# The boot: eirl-update-recover.service runs the helper that is installed now (B's: install.sh put it first)
/usr/local/sbin/eirl-update recover >> /var/log/eirl-update.log 2>&1 || fail "recover"
echo "after recover: release $(ver), layer: $(layer)"
[ "$(ver)" = "$A" ] || fail "the release was not put back: $(ver)"
[ ! -f /var/lib/eirl-system/in-progress.json ] && [ ! -d /var/lib/eirl-system/txn ] || fail "the layer's marker or copies stayed"
[ -z "$(pkg usbmuxd)" ] || [ "$(pkg usbmuxd)" = n ] || fail "usbmuxd stayed: $(pkg usbmuxd)"
[ ! -f /lib/modules/4.9.337-tegra/updates/eirl-modems/ipheth.ko ] || fail "ipheth.ko stayed"
grep -q 'VERSION="0.7.0"' /usr/lib/os-release || fail "os-release not restored: $(grep VERSION= /usr/lib/os-release)"
[ ! -e /usr/sbin/policy-rc.d ] || fail "policy-rc.d left behind"
dpkg --audit | grep -q . && fail "dpkg left half done: $(dpkg --audit | head -3)"
layer | grep -q interrupted || fail "state.json does not say interrupted"
echo "OK: the half-applied layer and the release were undone"

echo "=== 2. A → B by A's helper (the first release with a system layer, on an older board)"
stage B >/dev/null
helper; echo "helper: $(helper_state)"
[ "$(ver)" = "$B" ] || fail "VERSION after B: $(ver)"
[ "$(pkg usbmuxd)" = i ] && [ -f /lib/modules/4.9.337-tegra/updates/eirl-modems/ipheth.ko ] || fail "the layer is not there"
grep -q "VERSION=\"$B\"" /usr/lib/os-release || fail "os-release: $(grep VERSION= /usr/lib/os-release)"
layer | grep -qx "$B |  | " || fail "state.json: $(layer)"
echo "OK: layer $(layer)"

echo "=== 3. B → C, whose app never starts: the app goes back, the layer stays"
C=$(stage C)
helper; echo "helper: $(helper_state)"
[ "$(ver)" = "$B" ] || fail "VERSION after the rollback of C: $(ver)"
helper_state | grep -q '^rolled_back unhealthy' || fail "not rolled back as unhealthy"
layer | grep -qx "$C |  | " || fail "the committed layer of C was undone: $(layer)"
grep -q "VERSION=\"$C\"" /usr/lib/os-release || fail "os-release: $(grep VERSION= /usr/lib/os-release)"
[ "$(pkg usbmuxd)" = i ] || fail "usbmuxd gone"
echo "OK: app $B, layer $C (forward only)"

echo "=== 4. B → E, whose layer has a file that does not match: refused before any change, rolled back"
E=$(stage E)
helper; echo "helper: $(helper_state)"
[ "$(ver)" = "$B" ] || fail "VERSION after the rollback of E: $(ver)"
helper_state | grep -q '^rolled_back install_failed' || fail "not rolled back as install_failed"
layer | grep -q "bad_file" || fail "state.json does not say bad_file: $(layer)"
grep -q "VERSION=\"$C\"" /usr/lib/os-release || fail "the layer changed: $(grep VERSION= /usr/lib/os-release)"
echo "OK: layer $(layer)"

echo "=== 5. streaming: refused, the layer untouched"
mkdir -p /run/eirl-engine && echo '{"output":{"mode":"on","state":"on"}}' > /run/eirl-engine/mixer.json
stage C >/dev/null   # newer than what runs (B), and verified: only streaming can refuse it
before=$(md5sum /var/lib/eirl-system/state.json)
helper; echo "helper: $(helper_state)"
helper_state | grep -q '^refused streaming' || fail "not refused while streaming"
[ "$before" = "$(md5sum /var/lib/eirl-system/state.json)" ] || fail "the layer's state changed"
echo '{"output":{"mode":"off","state":"off"}}' > /run/eirl-engine/mixer.json
echo "ALL OK"
