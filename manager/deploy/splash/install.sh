#!/usr/bin/env bash
# install.sh — installs or undoes the EnhancedIRL boot splash (SPLASH.md). Idempotent. As root:
#   sudo ./install.sh          # binary, units and extlinux entry (DEFAULT, TIMEOUT 1 s)
#   sudo ./install.sh --undo   # removes the entry, the units and the binary (--deshacer: the old name)
# It touches /boot: a copy of extlinux.conf before writing (extlinux.conf.bak-<date>-splash) and a NEW
# entry; the entry that booted until now is not touched. Everything shows at the next boot, not before.
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
cd "$(dirname "$0")"
UNITS="eirl-splash.service eirl-splash-quit.service eirl-splash-console.service eirl-splash-reboot.service eirl-splash-poweroff.service"

case "${1:-}" in
--undo|--deshacer)
  # The entry first: if something fails afterwards, the board already boots as before. If it cannot be
  # undone (no previous state), it stops here with the binary in place: restore a .bak-*-splash copy.
  [ -x /usr/local/bin/eirl-splash ] && /usr/local/bin/eirl-splash boot-entry uninstall
  systemctl disable $UNITS >/dev/null 2>&1 || true
  for u in $UNITS; do rm -f "/etc/systemd/system/$u"; done
  systemctl daemon-reload
  rm -f /usr/local/bin/eirl-splash
  echo "boot splash removed (shows at the next boot)"
  exit 0 ;;
"") ;;
*) echo "usage: install.sh [--undo]" >&2; exit 64 ;;
esac

# The binary through mv: one in use is never overwritten
install -m 0755 eirl-splash /usr/local/bin/eirl-splash.new
mv -f /usr/local/bin/eirl-splash.new /usr/local/bin/eirl-splash
for u in $UNITS; do install -m 0644 "$u" "/etc/systemd/system/$u"; done
# EIRL_IMAGE=1 (the encoder's install.sh --image): an SD card image with no systemd running; enable works offline
[ "${EIRL_IMAGE:-}" = 1 ] || systemctl daemon-reload
systemctl enable $UNITS >/dev/null 2>&1
/usr/local/bin/eirl-splash boot-entry install
echo "boot splash $(/usr/local/bin/eirl-splash -version) installed: it shows at the next boot"
