#!/bin/bash
# tools/ota-sim/run.sh — runs INSIDE the container of tools/ota-sim.sh (a fake board: Ubuntu 18.04 arm64 without systemd). Real install.sh, eirl-encoderd
# and eirl-update; fake systemctl/sudo (shims). Scenarios: first install by hand (A), OTA to B (healthy),
# C (the new app never starts → rollback), D (install.sh fails → rollback), streaming refusal, manual rollback,
# interrupted install; with /s/rel/E and F (engine-carrying releases): engine install, engine rollback, manual
# rollback of the engine. The engine is emulated by the fake systemctl; every scenario checks how many times
# it was restarted (an app-only bundle never restarts it).
set -uo pipefail
cd /
fail() { echo "FAIL: $*"; tail -40 /var/log/eirl-update.log 2>/dev/null; exit 1; }
ver() { head -1 /usr/lib/eirl-encoder/VERSION 2>/dev/null; }
restarts() { cat /run/fake-engine.n 2>/dev/null || echo 0; }
# engine_restarts N WHAT: the engine was restarted exactly N times since the last call
engine_restarts() {
  local now; now=$(restarts)
  [ $((now - ${R0:-0})) = "$1" ] || fail "$2: the engine was restarted $((now - ${R0:-0})) times, want $1"
  echo "engine restarts: $1 ($2)"
  R0=$now
}
helper_state() { python3 -c 'import json;d=json.load(open("/var/lib/eirl-update/status.json"));print(d["state"], d.get("error",""), d.get("to",""), "|", d.get("reason","")[:200])'; }
wait_helper() {
  for i in $(seq 1 120); do
    s=$(python3 -c 'import json;print(json.load(open("/var/lib/eirl-update/status.json"))["state"])' 2>/dev/null)
    case "$s" in installed|rolled_back|failed|refused) return 0 ;; esac
    sleep 1
  done
  return 1
}
serve() {
  pkill -f 'http.server --bind 127.0.0.1 8000' 2>/dev/null
  sleep 0.5
  (cd "/s/rel/$1" && exec python3 -m http.server --bind 127.0.0.1 8000 >/dev/null 2>&1) &
  sleep 1
}

# The board's environment
cp /s/sim/shims/* /bin/
for g in i2c systemd-journal video audio input; do groupadd -f "$g"; done
mkdir -p /boot/extlinux /etc/NetworkManager/dispatcher.d /var/log
cat > /boot/extlinux/extlinux.conf <<'EXT'
TIMEOUT 30
DEFAULT primary

MENU TITLE L4T boot options

LABEL primary
      MENU LABEL primary kernel
      LINUX /boot/Image
      INITRD /boot/initrd
      APPEND ${cbootargs} quiet root=/dev/mmcblk0p1 rw rootwait
EXT
cat > /etc/default/eirl-encoderd <<'OPTS'
EIRL_ENCODERD_OPTS="-listen 127.0.0.1:8080 -cloud-api= -live-max 0 -update-url http://127.0.0.1:8000/manifest.json -update-check=false"
OPTS

echo "=== 1. first install of A by hand (0.1.1-test.1)"
mkdir -p /tmp/a && tar -xzf /s/rel/A/eirl-encoder-0.1.1-test.1-linux-arm64.tar.gz -C /tmp/a
/tmp/a/install.sh > /var/log/install-a.log 2>&1 || { tail -30 /var/log/install-a.log; fail "install.sh of A"; }
tail -3 /var/log/install-a.log
[ "$(ver)" = 0.1.1-test.1 ] || fail "VERSION after A: $(ver)"
for p in $(cat /usr/lib/eirl-encoder/files.txt); do [ -e "$p" ] || fail "listed but not installed: $p"; done
echo "files.txt: $(wc -l < /usr/lib/eirl-encoder/files.txt) paths, all present"
stat -c '%a %U' /var/lib/eirl-update /etc/sudoers.d/eirl-encoder /usr/local/sbin/eirl-update
grep -q 'eirl-update.service' /etc/sudoers.d/eirl-encoder || fail "sudoers without the update rule"
ls /usr/share/doc/eirl-encoder/licenses/srtla/

# The sudo broker (root): runs only the exact rules of the installed sudoers
mkdir -p /run/fake-sudo/q && chmod 1777 /run/fake-sudo/q
sed -n 's/.*NOPASSWD: //p' /etc/sudoers.d/eirl-encoder | sed 's/, /\n/g' > /run/fake-sudo/allowed
( while true; do
    for f in /run/fake-sudo/q/*.req; do
      [ -f "$f" ] || continue
      c=$(cat "$f"); rm -f "$f"
      $c; echo $? > "${f%.req}.rc"
    done
    sleep 0.1
  done ) &
printf '%s\n' test-password-123 | /bin/asuser eirl-encoder /usr/local/bin/eirl-encoderd passwd >/dev/null 2>&1
systemctl restart eirl-encoderd.service
sleep 2
# The engine as tools/deploy-engine.sh leaves it (Go only; not listed in A's files.txt: A is app-only), plus
# what an older board still has from the retired Python engine, and running
E=/usr/lib/eirl-encoder/engine
mkdir -p $E/__pycache__
echo "abc1234 main · deploy-engine" > $E/VERSION
printf '#!/bin/sh\n# Go engine deployed by hand\n' > $E/eirl-engine-go
printf '#!/bin/sh\nexec "$(dirname "$0")/eirl-engine-go"\n' > $E/eirl-engine-start
chmod 0755 $E/eirl-engine-go $E/eirl-engine-start
echo "# retired" > $E/eirl-engine.sh; echo "# retired" > $E/engine_mixer.py; echo "# old copy" > $E/engine_mixer.py.bak-20261013-1200
echo pyc > $E/__pycache__/engine_mixer.cpython-36.pyc
systemctl start eirl-engine.service; R0=$(restarts)
echo "config dir: $(stat -c '%a %U:%G' /etc/eirl-encoder)"
[ "$(stat -c %a /etc/eirl-encoder)" = 3770 ] || fail "/etc/eirl-encoder is not 3770"
python3 /s/sim/client.py device-writes || fail "the app cannot replace its files in /etc/eirl-encoder"
ls -l /etc/eirl-encoder | tail -n +2 | awk '{print "  " $1, $3, $9}'

echo "=== 2. OTA A → B (healthy)"
serve B
python3 /s/sim/client.py check-install || fail "check/install B"
wait_helper || fail "helper did not finish"
echo "helper: $(helper_state)"
[ "$(ver)" = 0.1.1-test.2 ] || fail "VERSION after B: $(ver)"
ls /var/lib/eirl-update/backup/
python3 /s/sim/client.py status | grep -E '"(current|state|app)"|lastResult|"to"|status\.' | head -8
engine_restarts 0 "app-only update"

echo "=== 3. OTA B → C (the new app never starts: rollback)"
serve C
python3 /s/sim/client.py check-install || fail "check/install C"
wait_helper || fail "helper did not finish"
echo "helper: $(helper_state)"
[ "$(ver)" = 0.1.1-test.2 ] || fail "VERSION after the rollback of C: $(ver)"
file /usr/local/bin/eirl-encoderd | grep -q ELF || fail "the broken app was not replaced back"
sleep 2
python3 /s/sim/client.py status | grep -E '"(current|state|error|message)"' | head -6
engine_restarts 0 "app-only rollback"

echo "=== 4. OTA B → D (install.sh fails: rollback)"
serve D
python3 /s/sim/client.py check-install || fail "check/install D"
wait_helper || fail "helper did not finish"
echo "helper: $(helper_state)"
[ "$(ver)" = 0.1.1-test.2 ] || fail "VERSION after the rollback of D: $(ver)"
sleep 2
engine_restarts 0 "app-only install.sh failure"

echo "=== 5. streaming: install refused by the app (409) and by the helper"
mkdir -p /run/eirl-engine && echo '{"output":{"mode":"on","state":"on"}}' > /run/eirl-engine/mixer.json
python3 /s/sim/client.py install-expect-409 || fail "install while streaming was not refused"
echo '{"version":"0.1.1-test.4","requestedAt":"2026-10-14T00:00:00Z"}' > /tmp/req && install -o eirl-encoder -g eirl-encoder -m 0640 /tmp/req /var/lib/eirl-encoder/update/request.json
/usr/local/sbin/eirl-update install >/dev/null 2>&1; echo "helper exit $? → $(helper_state)"
echo '{"output":{"mode":"off","state":"off"}}' > /run/eirl-engine/mixer.json
engine_restarts 0 "refused while streaming"

echo "=== 6. manual rollback to the kept backup (B → A)"
/usr/local/sbin/eirl-update rollback >> /var/log/eirl-update.log 2>&1 || fail "manual rollback"
echo "helper: $(helper_state)"
[ "$(ver)" = 0.1.1-test.1 ] || fail "VERSION after the manual rollback: $(ver)"
[ -e /usr/local/sbin/eirl-update ] || fail "helper missing after rollback"
python3 /s/sim/client.py status | grep -E '^ "(state|error)"' | head -2
engine_restarts 0 "app-only manual rollback"

echo "=== 7. interrupted update: marker present at boot → recover"
serve B
python3 /s/sim/client.py check-install >/dev/null || fail "check/install B again"
# kill the helper while install.sh runs (as a power cut would)
for i in $(seq 1 100); do
  grep -q '"phase": "install"' /var/lib/eirl-update/status.json 2>/dev/null && break; sleep 0.1
done
kill -9 "$(cat /run/fake-eirl-update.service.pid)" 2>/dev/null; pkill -9 -f '^/bin/bash /var/lib/eirl-update/staging/bundle/install.sh' 2>/dev/null
sleep 1
[ -f /var/lib/eirl-update/in-progress.json ] || echo "(the install finished before the kill: marker gone, VERSION $(ver))"
/usr/local/sbin/eirl-update recover >> /var/log/eirl-update.log 2>&1 || fail "recover"
echo "after recover: VERSION $(ver), helper: $(helper_state)"
engine_restarts 0 "app-only interrupted install"

if [ -d /s/rel/E ]; then
  echo "=== 8. OTA A → E (carries the engine: installed, engine restarted once)"
  serve E
  python3 /s/sim/client.py check-install || fail "check/install E"
  wait_helper || fail "helper did not finish"
  echo "helper: $(helper_state)"
  [ "$(ver)" = 0.1.1-test.5 ] || fail "VERSION after E: $(ver)"
  ev=$(head -c 12 /usr/lib/eirl-encoder/engine/VERSION)
  [ "$ev" = 0.1.1-test.5 ] || fail "engine VERSION after E: $ev"
  getent passwd eirl-engine >/dev/null || fail "no eirl-engine user"
  [ -L /usr/local/bin/eirl-enginectl ] || fail "no eirl-enginectl link"
  grep -q 'msg=healthy version=0.1.1-test.5 engine=true' /var/log/eirl-update.log || fail "the engine was not health-checked"
  for f in eirl-engine.sh engine_mixer.py engine_mixer.py.bak-20261013-1200 __pycache__; do
    [ ! -e "$E/$f" ] || fail "the retired $f was left in $E"
  done
  grep -q 'deployed by hand' $E/eirl-engine-go && fail "the release's eirl-engine-go was not installed"
  echo "retired Python engine removed: $(ls $E | tr '\n' ' ')"
  echo "engine: $(cat /usr/lib/eirl-encoder/engine/VERSION) · user eirl-engine · $(grep -c '^/usr/lib/eirl-encoder/engine/' /usr/lib/eirl-encoder/files.txt) engine files in files.txt"
  engine_restarts 1 "engine install"

  echo "=== 9. OTA E → F (carries the engine, its app never starts: rollback, engine restarted twice)"
  serve F
  python3 /s/sim/client.py check-install || fail "check/install F"
  wait_helper || fail "helper did not finish"
  echo "helper: $(helper_state)"
  [ "$(ver)" = 0.1.1-test.5 ] || fail "VERSION after the rollback of F: $(ver)"
  ev=$(head -c 12 /usr/lib/eirl-encoder/engine/VERSION)
  [ "$ev" = 0.1.1-test.5 ] || fail "engine VERSION after the rollback of F: $ev"
  engine_restarts 2 "engine rollback (the new engine, then the restored one)"

  echo "=== 10. manual rollback E → A (puts back the hand-deployed engine: restarted once)"
  /usr/local/sbin/eirl-update rollback >> /var/log/eirl-update.log 2>&1 || fail "manual rollback to A"
  echo "helper: $(helper_state)"
  [ "$(ver)" = 0.1.1-test.1 ] || fail "VERSION after the manual rollback to A: $(ver)"
  grep -q '^abc1234 main' $E/VERSION || fail "the hand-deployed engine was not restored: $(cat $E/VERSION 2>&1)"
  grep -q 'deployed by hand' $E/eirl-engine-go || fail "the hand-deployed eirl-engine-go was not restored"
  # The retired files the release removed come back too (they were backed up); their .bak copies and
  # __pycache__ do not (nothing needs them)
  [ -f $E/eirl-engine.sh ] && [ -f $E/engine_mixer.py ] || fail "the retired files were not restored"
  echo "engine: $(cat $E/VERSION) · $(ls $E | tr '\n' ' ')"
  engine_restarts 1 "manual rollback of the engine"
fi
echo "ALL OK"
