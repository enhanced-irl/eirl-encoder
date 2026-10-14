#!/usr/bin/env bash
# Installs the engine (eirl-engine-go, built and tested with build/engine.sh, and its start script) on the
# board from this tree, without ever cutting a live stream (ENGINE_GO.md). It also removes what the Python
# engine, retired on Oct 14, left installed.
#   tools/deploy-engine.sh               → tests, copies, installs and restarts eirl-engine
#   tools/deploy-engine.sh --unit        → and also the eirl-engine.service and eirl-hdmi-init.service units
#   tools/deploy-engine.sh --no-restart  → installs (with the units) but does not restart (used when
#                                          migrating a board from the engine's old name, hdmi2srt)
#   tools/deploy-engine.sh --check       → only says what is installed and whether it is streaming
#   JETSON=<host> for another board (default: the `jetson` alias of ~/.ssh/config)
#
# Rules it applies:
# - If the board is streaming, it installs NOTHING and exits with an error. The touch screen can start a
#   stream without going through anyone: the state is checked in the same command that restarts. It also
#   checks the engine under its old name (/run/hdmi2srt), in case the board has not been migrated yet.
# - The code goes to /usr/lib/eirl-encoder/engine (root, 0755) and eirl-enginectl to /usr/local/bin. The
#   engine runs without write access there.
# - A copy of every file it replaces goes to /usr/lib/eirl-encoder/engine/<file>.bak-<date-time>.
# - It replaces with install + mv (new inode): a running script or binary keeps its old file.
# - It leaves in /usr/lib/eirl-encoder/engine/VERSION what was installed (commit, branch, whether there
#   were uncommitted changes, who and when).
# - It checks that eirl-engine is active again, with no panic at start-up.
# - It installs the EDID files next to the engine (build/edid.sh, from edid/*.hex): eirl-hdmi-init.sh loads one
#   at boot and the engine when the preferred mode changes.
# It does not touch /etc/default/eirl-engine or the app (manager/build.sh).
set -euo pipefail
HOST=${JETSON:-jetson}
cd "$(dirname "$0")/.."
DEST=/usr/lib/eirl-encoder/engine

state() {
  ssh "$HOST" 'eirl-enginectl state 2>/dev/null | python3 -c "import json,sys; o=json.load(sys.stdin).get(\"output\",{}); print(o.get(\"mode\",\"?\"))" || echo "?"'
}

if [ "${1:-}" = --check ]; then
  ssh "$HOST" "cat $DEST/VERSION 2>/dev/null || echo 'no VERSION (installed by hand)'; systemctl is-active eirl-engine"
  echo "output: $(state)"
  exit 0
fi

UNIT=0; RESTART=1
case "${1:-}" in
  --unit) UNIT=1 ;;
  --no-restart) UNIT=1; RESTART=0 ;;
esac
build/engine.sh >/dev/null   # engine tests + build/out/eirl-engine (cgo, arm64)
build/edid.sh >/dev/null     # build/out/edid-*.bin, from edid/*.hex

ver="$(git rev-parse --short HEAD) $(git rev-parse --abbrev-ref HEAD)"
git diff --quiet HEAD -- scripts/ manager/ || ver="$ver +uncommitted changes"
ver="$ver · $(whoami)@$(hostname -s) · $(date '+%Y-%m-%d %H:%M')"

FILES755="eirl-engine-start eirl-enginectl eirl-hdmi-init.sh eirl-engine-go"
FILES644=""   # none today (the Python engine had its modules here)
# The Python engine's files (retired on Oct 14): removed from the board, with their .bak copies
RETIRED="eirl-engine.sh engine-run.py engine_mixer.py engine_output.py engine_audio.py engine_phones.py engine_usb.py engine_net.py engine_app.py eirl-logo.png __pycache__"
UNITS="eirl-engine.service eirl-hdmi-init.service"
# The EDIDs of the TC358743 (build/edid.sh → build/out; data, 0644). The modes are the app's (config.EDIDModes)
EDIDS="edid-1080p30-lpcm48k.bin edid-1080p50-lpcm48k.bin edid-720p60-lpcm48k.bin"
# (tools/release.sh reads FILES755, FILES644, RETIRED, UNITS and EDIDS from here: one list for both)
tmp=$(ssh "$HOST" mktemp -d /tmp/eirl-engine.XXXXXX)
# shellcheck disable=SC2086
scp -q $(for f in ${FILES755/ eirl-engine-go/}; do printf 'scripts/%s ' "$f"; done) "$HOST:$tmp/"
scp -q build/out/eirl-engine "$HOST:$tmp/eirl-engine-go"
# shellcheck disable=SC2086
scp -q $(for f in $EDIDS; do printf 'build/out/%s ' "$f"; done) "$HOST:$tmp/"
# shellcheck disable=SC2086
[ "$UNIT" = 1 ] && scp -q $(for u in $UNITS; do printf 'systemd/%s ' "$u"; done) "$HOST:$tmp/"

ssh "$HOST" bash -s -- "$tmp" "'$ver'" "'$FILES755'" "'$FILES644 $EDIDS'" "$UNIT" "$RESTART" "'$UNITS'" "$DEST" "'$RETIRED'" <<'EOF'
set -eu
tmp=$1; ver=$2; f755=$3; f644=$4; unit=$5; restart=$6; units=$7; D=$8; retired=$9
# Streaming? With the current name and with the old one (a board not migrated yet)
emitting() {
  local d
  for d in /run/eirl-engine /run/hdmi2srt; do
    grep -q '^OUTPUT=on' "$d/state" 2>/dev/null && return 0
    python3 -c 'import json,sys; o=json.load(open(sys.argv[1])).get("output") or {}; sys.exit(0 if o.get("mode") in ("on","connecting") else 1)' \
      "$d/mixer.json" 2>/dev/null && return 0
  done
  return 1
}
sudo -n install -d -m 0755 -o root -g root "$D"
stamp=$(date +%Y%m%d-%H%M)
for f in $f755 $f644; do                      # everything staged alongside (.new) before checking the output
  m=0644; case " $f755 " in *" $f "*) m=0755;; esac
  sudo -n install -m$m -o root -g root "$tmp/$f" "$D/$f.new"
done
if emitting; then
  for f in $f755 $f644; do sudo -n rm -f "$D/$f.new"; done
  echo "STREAMING: not installing. Ask the board's operator for a time slot." >&2
  rm -rf "$tmp"; exit 3
fi
for f in $f755 $f644; do
  [ -f "$D/$f" ] && sudo -n cp -p "$D/$f" "$D/$f.bak-$stamp"
  sudo -n mv "$D/$f.new" "$D/$f"
done
sudo -n ln -sfn "$D/eirl-enginectl" /usr/local/bin/eirl-enginectl
for f in $retired; do                          # the Python engine's leftovers and their old copies
  sudo -n rm -rf "$D/$f" "$D/$f".bak-*
done
if [ "$unit" = 1 ]; then
  # The engine's own system user (no login, no sudo; home = its state directory). The unit gives it its
  # groups; they also go in /etc/group so `sudo -u eirl-engine …` behaves like the service
  if ! getent passwd eirl-engine >/dev/null; then
    sudo -n useradd --system --user-group --home-dir /var/lib/eirl-engine --no-create-home \
      --shell /usr/sbin/nologin --comment "EnhancedIRL encoder engine" eirl-engine
  fi
  for g in video audio input i2c; do
    getent group "$g" >/dev/null && sudo -n usermod -aG "$g" eirl-engine
  done
  # Operator tools (eirl-enginectl, engine-watch.py) talk to mixer.sock (0660, group eirl-encoder) as the
  # login user: on a development board that user joins the group (from its next login)
  if getent group eirl-encoder >/dev/null && ! id -nG "$USER" | grep -qw eirl-encoder; then
    sudo -n usermod -aG eirl-encoder "$USER" && echo "added $USER to eirl-encoder (effective from the next login)"
  fi
  for u in $units; do
    [ -f "/etc/systemd/system/$u" ] && sudo -n cp -p "/etc/systemd/system/$u" "/etc/systemd/system/$u.bak-$stamp"
    sudo -n install -m0644 "$tmp/$u" "/etc/systemd/system/$u"
  done
  sudo -n systemctl daemon-reload
fi
rm -rf "$tmp"
[ -f "$D/VERSION" ] && sudo -n cp -p "$D/VERSION" "$D/VERSION.bak-$stamp"
echo "$ver" | sudo -n tee "$D/VERSION" >/dev/null
if [ "$restart" = 0 ]; then
  echo "installed, not restarted: $ver (copies .bak-$stamp)"
  exit 0
fi
# With --unit, copying and daemon-reload take a few seconds: the output is checked again right before
if emitting; then
  for f in $f755 $f644; do [ -f "$D/$f.bak-$stamp" ] && sudo -n mv "$D/$f.bak-$stamp" "$D/$f"; done
  if [ "$unit" = 1 ]; then
    for u in $units; do
      [ -f "/etc/systemd/system/$u.bak-$stamp" ] && sudo -n mv "/etc/systemd/system/$u.bak-$stamp" "/etc/systemd/system/$u"
    done
    sudo -n systemctl daemon-reload
  fi
  [ -f "$D/VERSION.bak-$stamp" ] && sudo -n mv "$D/VERSION.bak-$stamp" "$D/VERSION"
  echo "STREAMING since a moment ago: leaving everything as it was, not restarting." >&2; exit 3
fi
# The state directory belongs to the unit's User= (it changed from the login user to eirl-engine on Oct 14;
# a restored .bak unit takes it back). Only now, after the last output check: the running engine keeps it
owner=$(systemctl show -p User --value eirl-engine)
[ -n "$owner" ] && [ -d /var/lib/eirl-engine ] && sudo -n chown -R "$owner:" /var/lib/eirl-engine
sudo -n systemctl restart eirl-engine          # milliseconds after checking the output
inv=$(systemctl show -p InvocationID --value eirl-engine)
sleep 25
systemctl is-active eirl-engine
# Only the journal of THIS run: with --since, the stop of the previous engine got in; it already reads the
# new files from disk, and its traceback looked like the new one's (13 Oct 2026)
run_log() { journalctl _SYSTEMD_INVOCATION_ID="$inv" --no-pager -o cat; }
if run_log | grep -q -E 'Traceback|^panic:|^fatal error:'; then
  run_log | grep -A12 -E 'Traceback|^panic:|^fatal error:' | head -30
  echo "PANIC at start-up: restore with the .bak-$stamp copies" >&2; exit 4
fi
if [ "$(systemctl show -p InvocationID --value eirl-engine)" != "$inv" ]; then
  echo "eirl-engine restarted by itself: see journalctl -u eirl-engine; copies .bak-$stamp" >&2; exit 4
fi
run_log | grep -q '^\[engine\]' || { echo "no engine start line in its journal: cannot tell whether it started fine" >&2; exit 4; }
echo "installed: $ver (copies .bak-$stamp)"
EOF
