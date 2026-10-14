#!/usr/bin/env bash
# install.sh — installs or updates the encoder software on the Jetson. Idempotent. As root:
#   sudo ./install.sh                    # from a package directory (manager/build.sh) or a release bundle
#   sudo ./install.sh --restart-engine   # also restarts the engine if its drop-in or its files changed
#                                        # (never while streaming)
#   ./install.sh --list-files            # (no root) prints every path this package installs: files.txt
#
# What it does (and how to undo it, in MANAGER.md §11):
#   system user eirl-encoder (groups video, i2c, systemd-journal) · /etc/eirl-encoder · udev rule of the
#   USB capture devices (no autosuspend; the board's hub writable by the group eirl-usbhub, which only
#   eirl-engine has, to recover a hung device) · app binary · srtla_send + NetworkManager dispatcher
#   (SRTLA) · narrow sudoers · polkit · tmpfiles of the power readings and /run/eirl-uplinks · network
#   cameras (eirl-netsetup, MediaMTX and their units; F7) · app unit (+ /etc/default/eirl-encoderd) ·
#   eirl-engine drop-in · the engine itself when the package carries it (a release bundle: engine/) ·
#   the update helper (eirl-update and its units, UPDATES.md) · one-time migrations (hdmi2srt →
#   eirl-engine; the destination from /etc/default/eirl-engine to /etc/eirl-encoder/engine.env) · boot
#   splash (splash/install.sh: binary, units and extlinux entry; seen at the next boot, SPLASH.md) ·
#   /usr/lib/eirl-encoder/{VERSION,BUILD,files.txt}.
# It does NOT restart the engine unless --restart-engine: the drop-in and the engine files apply when
# the engine restarts. The OTA helper (eirl-update) runs it without options and restarts the engine
# itself, checking that nothing streams in the same step.
#
# ⚠️ Every file installed goes through put() and MUST also be listed in list_files(), and so must every file it
# removes: that list is the bundle's files.txt, what the OTA helper backs up before an update and restores on
# a rollback.
set -euo pipefail
cd "$(dirname "$0")"
SRC=.
[ -f ./deploy/eirl-encoderd.service ] && D=./deploy || D=.
RESTART_ENGINE=""
LIST=""
for a in "$@"; do
  case "$a" in
    --restart-engine) RESTART_ENGINE=1 ;;
    --list-files) LIST=1 ;;
    *) echo "unknown option: $a" >&2; exit 64 ;;
  esac
done

LIB=/usr/lib/eirl-encoder
ENGINE_DIR=$LIB/engine
DROPIN=/etc/systemd/system/eirl-engine.service.d/eirl-encoder.conf
PKLA_DIR=/etc/polkit-1/localauthority/50-local.d
NET_UNITS="eirl-ap.service eirl-ap-dhcp.service eirl-media.service"
UPDATE_UNITS="eirl-update.service eirl-update-recover.service"
DOC=/usr/share/doc/eirl-encoder

# retired_names: the names of engine/retired.txt that are plain file names (nothing else is ever removed)
retired_names() {
  [ -f "$SRC/engine/retired.txt" ] || return 0
  while read -r f; do
    case "$f" in ""|.|..|*/*|*[!A-Za-z0-9._-]*) continue ;; esac
    echo "$f"
  done < "$SRC/engine/retired.txt"
}

# list_files: every destination path, in the order the steps below install them. Uses only what is in
# the package (a package without the engine does not list it). bash 3.2 compatible (it runs on the
# release machine too).
list_files() {
  echo /usr/local/bin/eirl-encoderd
  [ -f "$SRC/srtla_send" ] && echo /usr/local/bin/srtla_send
  echo /etc/NetworkManager/dispatcher.d/90-eirl-uplinks
  if [ -f "$SRC/eirl-netsetup" ]; then
    echo /usr/local/sbin/eirl-netsetup
    [ -f "$SRC/mediamtx" ] && echo /usr/local/bin/mediamtx
    for u in $NET_UNITS; do echo "/etc/systemd/system/$u"; done
  fi
  echo /etc/sudoers.d/eirl-encoder
  for p in "$D"/*.pkla; do [ -f "$p" ] && echo "$PKLA_DIR/$(basename "$p")"; done
  echo /etc/tmpfiles.d/eirl-encoder.conf
  echo /etc/udev/rules.d/70-eirl-capture.rules
  echo "$DROPIN"
  echo /etc/systemd/system/eirl-encoderd.service
  if [ -d "$SRC/engine/lib" ]; then
    for f in "$SRC"/engine/lib/*; do [ -f "$f" ] && echo "$ENGINE_DIR/$(basename "$f")"; done
    [ -f "$SRC/engine/lib/eirl-enginectl" ] && echo /usr/local/bin/eirl-enginectl
    for f in "$SRC"/engine/units/*; do [ -f "$f" ] && echo "/etc/systemd/system/$(basename "$f")"; done
    # The retired files it removes: listed so that an update backs them up and a rollback puts them back
    # (directories such as __pycache__ are not backed up: nothing needs them back)
    for f in $(retired_names); do [ "$f" = __pycache__ ] || echo "$ENGINE_DIR/$f"; done
  fi
  if [ -f "$SRC/eirl-update" ]; then
    echo /usr/local/sbin/eirl-update
    for u in $UPDATE_UNITS; do echo "/etc/systemd/system/$u"; done
  fi
  if [ -d "$SRC/splash" ]; then
    echo /usr/local/bin/eirl-splash
    for u in "$SRC"/splash/*.service; do [ -f "$u" ] && echo "/etc/systemd/system/$(basename "$u")"; done
    echo /boot/extlinux/extlinux.conf # the splash's boot entry (splash/install.sh keeps its own copy too)
  fi
  if [ -d "$SRC/doc" ]; then
    (cd "$SRC/doc" && find . -type f | sed "s|^\.|$DOC|")
  fi
  for f in VERSION BUILD files.txt; do [ -f "$SRC/$f" ] && echo "$LIB/$f"; done
  return 0
}
if [ -n "$LIST" ]; then
  list_files | LC_ALL=C sort -u
  exit 0
fi

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
USER_=eirl-encoder
CONF=/etc/eirl-encoder
APPENV=$CONF/engine.env
DEF=/etc/default/eirl-engine
stamp=$(date +%Y%m%d-%H%M%S)

# put MODE SRC DEST: installs SRC as DEST (root:root, MODE) through a hidden temporary name in the same
# directory and a rename: a binary or script in use is never overwritten in place (bash reads scripts
# while it runs them), a power cut never leaves half a file, and the temporary name (leading dot, with
# a dot inside) is ignored by sudo, systemd, udev, tmpfiles, polkit and the NetworkManager dispatcher.
# Records DEST in CHANGED when its content differs.
CHANGED=""
put() {
  local mode=$1 src=$2 dst=$3 tmp
  tmp="$(dirname "$dst")/.$(basename "$dst").eirl-new"
  cmp -s "$src" "$dst" 2>/dev/null || CHANGED="$CHANGED $dst"
  install -D -m "$mode" -o root -g root "$src" "$tmp"
  mv -f "$tmp" "$dst"
}

# Is the engine streaming? Like the OTA helper (internal/update.Streaming): the live state (mixer.json)
# decides when it has the output; otherwise OUTPUT=on in the launcher's state file.
engine_streaming() {
  # The engine's user writes these files: no links, no FIFOs, regular files of at most 1 MiB
  python3 - <<'PY' 2>/dev/null
import json, os, stat, sys
def read(p):
    try:
        fd = os.open(p, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError:
        return None
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            return None
        b = os.read(fd, (1 << 20) + 1)
        return None if len(b) > (1 << 20) else b.decode('utf-8', 'replace')
    finally:
        os.close(fd)
o = None
m = read('/run/eirl-engine/mixer.json')
if m is not None:
    try:
        o = json.loads(m).get('output')
    except Exception:
        o = None
if isinstance(o, dict):
    sys.exit(0 if o.get('mode') == 'on' or o.get('state') in ('on', 'connecting') else 1)
st = read('/run/eirl-engine/state')
sys.exit(0 if st is not None and 'OUTPUT=on' in st.split() else 1)
PY
}

# 1. User and groups (v4l2, I2C of the TC358743, journal; the app only uses them to read)
if ! id "$USER_" >/dev/null 2>&1; then
  useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$USER_"
  echo "user $USER_ created"
fi
usermod -a -G video,i2c,systemd-journal "$USER_"

# 2. Configuration directory: the app creates and renames files here (atomic writes).
#    ⚠️ It is writable by the app's user: root does NOT operate inside it (no create, chown or chmod),
#    because a symlink planted at the right moment would make it touch another file. Everything inside is
#    done as the user (runuser).
#    Sticky bit (3770): the engine is in the group (to read engine.env), and without it the group could also
#    unlink or rename the app's files (admin.hash, setup-code, cloud.json, identity). With it only their
#    owner (the app) or root can; the app's own atomic renames keep working.
install -d -m 3770 -o root -g "$USER_" "$CONF"
# A file there that is not the app's cannot be replaced by the app any more (sticky): say so (reading only)
others=$(find "$CONF" -mindepth 1 -maxdepth 1 ! -user "$USER_" -printf '%f ' 2>/dev/null || true)
[ -z "$others" ] || echo "WARNING: in $CONF, not owned by $USER_: $others— the app cannot replace them; give them to $USER_ by hand" >&2
as_app() { runuser -u "$USER_" -- "$@"; }

# 3. App binary
put 0755 "$SRC/eirl-encoderd" /usr/local/bin/eirl-encoderd

# 3b. SRTLA: srtla_send and the NetworkManager dispatcher that sets the source routing of each link and
#     leaves the list in /run/eirl-uplinks/available (SRTLA.md). It is run once now (step 5) for the
#     links already up; afterwards NetworkManager runs it on every change.
[ -f "$SRC/srtla_send" ] && put 0755 "$SRC/srtla_send" /usr/local/bin/srtla_send
put 0755 "$D/90-eirl-uplinks" /etc/NetworkManager/dispatcher.d/90-eirl-uplinks

# 3c. Network cameras (F7, NETWORK_SOURCES.md §6): the root helper eirl-netsetup, MediaMTX (own user
#     eirl-media, unprivileged) and their units. Nothing comes up by itself: the access point only comes
#     up with a camera registered (eirl-ap.service = eirl-netsetup apply, at the end).
if [ -f "$SRC/eirl-netsetup" ]; then
  if ! id eirl-media >/dev/null 2>&1; then
    useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin eirl-media
    echo "user eirl-media created"
  fi
  put 0755 "$SRC/eirl-netsetup" /usr/local/sbin/eirl-netsetup
  [ -f "$SRC/mediamtx" ] && put 0755 "$SRC/mediamtx" /usr/local/bin/mediamtx
  for u in $NET_UNITS; do put 0644 "$D/$u" "/etc/systemd/system/$u"; done
fi

# 3d. Options of the app (its unit reads them). Only created if missing: on the test board it points to
#     the dev backend. Owned by root (the app cannot switch backends or update servers).
if [ ! -e /etc/default/eirl-encoderd ]; then
  cat > /etc/default/eirl-encoderd <<'OPTS'
# Options of eirl-encoderd (read by eirl-encoderd.service). Empty = production
# (-cloud-api https://api.enhancedirl.com -cloud-app https://app.enhancedirl.com, updates from GitHub).
# Test board (dev):
#   EIRL_ENCODERD_OPTS=-cloud-api https://api.dev.enhancedirl.com -cloud-app https://app.dev.enhancedirl.com
# Without the EnhancedIRL link: EIRL_ENCODERD_OPTS=-cloud-api=
# A local update server (UPDATES.md §9): -update-url http://<host>:<port>/manifest.json
EIRL_ENCODERD_OPTS=
OPTS
  chmod 0644 /etc/default/eirl-encoderd
  echo "created /etc/default/eirl-encoderd (production by default)"
fi

# 4. sudoers: validated BEFORE installing it (a broken sudoers leaves the whole board without sudo);
#    put() makes the switch atomic
visudo -cf "$D/sudoers" >/dev/null
put 0440 "$D/sudoers" /etc/sudoers.d/eirl-encoder

# 5. polkit (every deploy/*.pkla: the client Wi-Fi through NetworkManager, WIFI.md §4, and whatever else
#    the app is granted). polkit 0.105 rereads the directory by itself
for p in "$D"/*.pkla; do [ -f "$p" ] && put 0644 "$p" "$PKLA_DIR/$(basename "$p")"; done
# Power readings (ina3221) readable now and at every boot; /run/eirl-uplinks and /run/eirl-ap
put 0644 "$D/tmpfiles.conf" /etc/tmpfiles.d/eirl-encoder.conf
systemd-tmpfiles --create /etc/tmpfiles.d/eirl-encoder.conf || echo "warning: tmpfiles not applied (another ina3221?)" >&2
/etc/NetworkManager/dispatcher.d/90-eirl-uplinks none resync && echo "SRTLA links: $(tr '\n' ' ' < /run/eirl-uplinks/available)"

# 5b. USB capture devices and cameras (SOURCES.md): no autosuspend, and their audio out of PulseAudio. The
#     rule applies to the ones plugged in later; the ones already plugged in get it here (without
#     `udevadm trigger`, which would rerun the modems' rules).
put 0644 "$D/70-eirl-capture.rules" /etc/udev/rules.d/70-eirl-capture.rules
udevadm control --reload || true
# PULSE_IGNORE on the sound cards already connected: only "change" events of the sound subsystem
udevadm trigger --action=change --subsystem-match=sound || true
for i in /sys/bus/usb/devices/*:*; do
  [ "$(cat "$i/bInterfaceClass" 2>/dev/null)" = 0e ] && [ -w "$i/../power/control" ] && echo on > "$i/../power/control"
done
# The board's hub, already enumerated: its node for eirl-usbhub (only eirl-engine), as the rule will do
getent group eirl-usbhub >/dev/null || groupadd --system eirl-usbhub
for h in /sys/bus/usb/devices/*; do
  [ "$(cat "$h/idVendor" 2>/dev/null)" = 0bda ] || continue
  case "$(cat "$h/idProduct" 2>/dev/null)" in 5411|0411) ;; *) continue ;; esac
  n=$(printf '/dev/bus/usb/%03d/%03d' "$(cat "$h/busnum")" "$(cat "$h/devnum")") || continue
  [ -c "$n" ] && chgrp eirl-usbhub "$n" && chmod 0664 "$n"
done

# 6. Units BEFORE the migrations: if something fails later, eirl-engine already reads the new file
dropin_changed=""
cmp -s "$D/eirl-engine-eirl-encoder.conf" "$DROPIN" 2>/dev/null || dropin_changed=1
put 0644 "$D/eirl-engine-eirl-encoder.conf" "$DROPIN"
put 0644 "$D/eirl-encoderd.service" /etc/systemd/system/eirl-encoderd.service

# 6a. The engine, when the package carries it (a release bundle; by hand it is tools/deploy-engine.sh,
#     which also checks that nothing streams). Code in /usr/lib/eirl-encoder/engine (root, 0755). What the
#     engine no longer uses (engine/retired.txt, tools/deploy-engine.sh's RETIRED: the Python engine) is
#     removed, with its old .bak copies, as deploy-engine.sh does.
engine_changed=""
if [ -d "$SRC/engine/lib" ]; then
  before=$CHANGED
  install -d -m 0755 -o root -g root "$LIB" "$ENGINE_DIR"
  for f in "$SRC"/engine/lib/*; do
    [ -f "$f" ] || continue
    m=0644; [ -x "$f" ] && m=0755
    put "$m" "$f" "$ENGINE_DIR/$(basename "$f")"
  done
  if [ -f "$ENGINE_DIR/eirl-enginectl" ]; then
    ln -sfn "$ENGINE_DIR/eirl-enginectl" /usr/local/bin/.eirl-enginectl.eirl-new
    mv -Tf /usr/local/bin/.eirl-enginectl.eirl-new /usr/local/bin/eirl-enginectl
  fi
  for f in $(retired_names); do
    if [ -e "$ENGINE_DIR/$f" ] || ls "$ENGINE_DIR/$f".bak-* >/dev/null 2>&1; then
      rm -rf "${ENGINE_DIR:?}/$f" "${ENGINE_DIR:?}/$f".bak-*
      CHANGED="$CHANGED $ENGINE_DIR/$f"
      echo "engine: removed the retired $f"
    fi
  done
  # The engine's own system user (no login, no sudo; home = its state directory). The unit gives it its
  # groups; they also go in /etc/group so `sudo -u eirl-engine …` behaves like the service. Same as
  # tools/deploy-engine.sh (keep both identical)
  if ! getent passwd eirl-engine >/dev/null; then
    useradd --system --user-group --home-dir /var/lib/eirl-engine --no-create-home \
      --shell /usr/sbin/nologin --comment "EnhancedIRL encoder engine" eirl-engine
  fi
  for g in video audio input i2c; do
    getent group "$g" >/dev/null && usermod -aG "$g" eirl-engine
  done
  for f in "$SRC"/engine/units/*; do [ -f "$f" ] && put 0644 "$f" "/etc/systemd/system/$(basename "$f")"; done
  [ "$CHANGED" != "$before" ] && engine_changed=1
  echo "engine: $(head -1 "$ENGINE_DIR/VERSION" 2>/dev/null)"
fi

# 6b. Update helper (UPDATES.md §7): the root binary, its oneshot (the app starts it through sudoers)
#     and the boot unit that undoes an interrupted update. /var/lib/eirl-update: status (readable),
#     backups and staging (0700, created by the helper).
if [ -f "$SRC/eirl-update" ]; then
  put 0755 "$SRC/eirl-update" /usr/local/sbin/eirl-update
  for u in $UPDATE_UNITS; do put 0644 "$D/$u" "/etc/systemd/system/$u"; done
  install -d -m 0755 -o root -g root /var/lib/eirl-update
fi
systemctl daemon-reload
[ -f "$SRC/eirl-update" ] && systemctl enable eirl-update-recover.service >/dev/null 2>&1

# 6c. The engine was called hdmi2srt until Oct 13: its app file becomes engine.env (moved by the app's
#     user, like everything inside $CONF, see step 2) and the board settings of /etc/default/hdmi2srt
#     go to $DEF, with a copy. The old ones stay for going back.
#     It must run BEFORE step 7, which would otherwise create engine.env from scratch and lose the
#     destination.
if [ ! -e "$APPENV" ] && [ -e "$CONF/hdmi2srt.env" ]; then
  as_app cp -p "$CONF/hdmi2srt.env" "$APPENV"
  echo "app configuration: $CONF/hdmi2srt.env → $APPENV"
fi
if [ ! -e "$DEF" ] && [ -f /etc/default/hdmi2srt ]; then
  cp -p /etc/default/hdmi2srt "$DEF"
  echo "board settings: /etc/default/hdmi2srt → $DEF"
fi

# 7. Migration of the destination (first time only): the keys the app manages move from
#    /etc/default/eirl-engine to the app's file and are commented out there (with a copy). An SRT_URL
#    with a query (?streamid=…&latency=…), which the old script accepted, is split.
KEYS="SRT_URL SRT_MODE SRT_LATENCY SRT_STREAMID SRT_PASSPHRASE VBITRATE ABITRATE OUTPUT"
if [ ! -e "$APPENV" ]; then
  # (without $DEF, a new device: nothing to migrate; sed must not fail the pipeline under pipefail)
  get() { [ -f "$DEF" ] || return 0; sed -nE "s/^[[:space:]]*$1=[\"']?([^\"']*)[\"']?[[:space:]]*$/\1/p" "$DEF" | tail -1; }
  declare -A val
  for k in $KEYS; do val[$k]=$(get "$k"); done
  url=${val[SRT_URL]}
  if [[ $url == *\?* ]]; then
    IFS='&' read -ra kvs <<<"${url#*\?}"
    url=${url%%\?*}
    for kv in "${kvs[@]}"; do
      case "${kv%%=*}" in
        streamid)   val[SRT_STREAMID]=${val[SRT_STREAMID]:-${kv#*=}} ;;
        passphrase) val[SRT_PASSPHRASE]=${val[SRT_PASSPHRASE]:-${kv#*=}} ;;
        latency)    val[SRT_LATENCY]=${val[SRT_LATENCY]:-${kv#*=}} ;;
        mode)       val[SRT_MODE]=${val[SRT_MODE]:-${kv#*=}} ;;
      esac
    done
    val[SRT_URL]=$url
  fi
  : "${val[SRT_MODE]:=caller}" "${val[SRT_LATENCY]:=200}" "${val[VBITRATE]:=6000}" "${val[ABITRATE]:=128}" "${val[OUTPUT]:=on}"
  tmp=$(mktemp)   # outside the app's directory, root only
  {
    echo "# Written by eirl-encoderd (the encoder's management app). Do not edit by hand: it is overwritten."
    echo "# Migrated from $DEF on $stamp."
    echo "EIRL_MANAGED_BY=local"
    for k in $KEYS; do echo "$k=${val[$k]}"; done
  } > "$tmp"
  # Written AS THE APP'S USER (see step 2)
  as_app sh -c 't=$(mktemp "$1/.engine.env.XXXXXX") && cat > "$t" && chmod 0640 "$t" && mv -f "$t" "$1/engine.env"' sh "$CONF" < "$tmp"
  rm -f "$tmp"
  if [ -f "$DEF" ]; then
    cp -p "$DEF" "$DEF.bak-$stamp"
    for k in $KEYS; do
      sed -i -E "s|^([[:space:]]*$k=)|# (migrated to $APPENV) \1|" "$DEF"
    done
  fi
  echo "destination migrated to $APPENV (copy of $DEF in $DEF.bak-$stamp)"
fi
# Files of earlier versions (0600): the engine reads them through the group
as_app chmod 0640 "$APPENV" "$CONF/admin.hash" 2>/dev/null || true

# 7b. Boot splash (SPLASH.md). Idempotent: with everything up to date it does not touch /boot; if the
#     extlinux entry changes, it copies it first. Restarts nothing: it shows at the next boot.
if [ -x "$SRC/splash/install.sh" ]; then
  "$SRC/splash/install.sh"
else
  echo "WARNING: the package has no splash/: the boot splash is not installed or updated" >&2
fi

# 7c. Licences of what is installed (the package's doc/: ours, the Go modules, MediaMTX, srtla_send and
#     where its source is)
if [ -d "$SRC/doc" ]; then
  (cd "$SRC/doc" && find . -type f) | while read -r f; do put 0644 "$SRC/doc/$f" "$DOC/${f#./}"; done
fi

# 7d. What is installed (UPDATES.md §2): the release, its commit and files.txt. Last: VERSION only says
#     a version once everything of it is in place.
install -d -m 0755 -o root -g root "$LIB"
for f in BUILD files.txt VERSION; do [ -f "$SRC/$f" ] && put 0644 "$SRC/$f" "$LIB/$f"; done

# 8. App
systemctl enable eirl-encoderd.service >/dev/null 2>&1
systemctl restart eirl-encoderd.service

# 8b. Network of the cameras as their files ask (without cameras: nothing). At boot eirl-ap.service does it
if [ -x /usr/local/sbin/eirl-netsetup ]; then
  systemctl enable eirl-ap.service >/dev/null 2>&1
  systemctl start eirl-ap.service || true
  echo "network cameras: $(cat /run/eirl-ap/state.json 2>/dev/null)"
fi

# 9. The engine applies a new drop-in or new files when it restarts
if [ -n "$dropin_changed" ] || [ -n "$engine_changed" ]; then
  if [ -n "$RESTART_ENGINE" ]; then
    if engine_streaming; then
      echo "WARNING: the engine is STREAMING: not restarted; the new engine files apply at its next restart" >&2
    else
      echo "restarting eirl-engine to load its new drop-in or files (a few seconds without the engine)"
      # The state directory belongs to the unit's User= (the login user until Oct 14, eirl-engine since);
      # only now, after the last output check (same as tools/deploy-engine.sh)
      owner=$(systemctl show -p User --value eirl-engine)
      [ -n "$owner" ] && [ -d /var/lib/eirl-engine ] && chown -R "$owner:" /var/lib/eirl-engine
      systemctl restart eirl-engine.service
    fi
  elif [ -z "${EIRL_OTA:-}" ]; then
    echo "WARNING: the eirl-engine drop-in or files changed: they apply when the engine restarts"
  fi
fi
[ -s "$CONF/admin.hash" ] || echo "NO admin password yet: sudo -u $USER_ eirl-encoderd passwd && sudo systemctl restart eirl-encoderd"
echo "eirl-encoderd $(systemctl is-active eirl-encoderd.service) · release $(head -1 "$LIB/VERSION" 2>/dev/null || echo '?')"
