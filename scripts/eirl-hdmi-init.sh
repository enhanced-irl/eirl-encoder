#!/usr/bin/env bash
# eirl-hdmi-init.sh — preparación del TC358743 al arrancar (lo ejecuta eirl-hdmi-init.service, como root).
#  1. Devuelve los pines DAP4 (J4-J7 = I2S4 del conector de 40 pines) al I2S: el BOOTLOADER los deja
#     en modo GPIO (bit CNF a 1) y ni el pinmux del DT ni liberar el GPIO lo deshacen en este kernel;
#     sin esto el I2S4 no recibe reloj ("Failed at I2S4 DAP RX sw reset"). Escribe el registro con
#     máscara MSK_CNF del puerto J (solo toca los bits 4-7).
#  2. Carga el EDID, para que la fuente HDMI vea el monitor aunque no se esté emitiendo (el EDID del
#     TC358743 vive en RAM y se pierde en cada arranque).
# There may be a TC358743 on CAM0, on CAM1 or on both (DEVELOPMENT.md): every chip gets the EDID, and the
# setting of which one has the audio and reset wires (HDMI_WIRED) becomes the driver's reset_port.
set -euo pipefail
# The EDID the app has chosen (EDID_PREFERRED in its configuration; whitelisted, like the
# engine), so that the source does not have to renegotiate again when the stream starts.
mode=$(sed -nE 's/^EDID_PREFERRED=(1080p30|1080p50|720p60)$/\1/p' /etc/eirl-encoder/engine.env 2>/dev/null | tail -1)
EDID="${EDID:-/usr/lib/eirl-encoder/engine/edid-${mode:-1080p30}-lpcm48k.bin}"

python3 - <<'PY'
import mmap, os, struct
GPIO = 0x6000d000; CNF_J = 0x204; MSK_CNF_J = 0x284; DAP4 = 0xF0   # puerto J, bits 4-7
fd = os.open("/dev/mem", os.O_RDWR | os.O_SYNC)
m = mmap.mmap(fd, 0x1000, mmap.MAP_SHARED, mmap.PROT_READ | mmap.PROT_WRITE, offset=GPIO)
rd = lambda o: struct.unpack("<I", m[o:o + 4])[0]
before = rd(CNF_J) & 0xff
m[MSK_CNF_J:MSK_CNF_J + 4] = struct.pack("<I", DAP4 << 8)          # máscara bits 4-7, valor 0 = SFIO
after = rd(CNF_J) & 0xff
print("eirl-hdmi-init: GPIO_CNF puerto J 0x%02x -> 0x%02x" % (before, after))
raise SystemExit(0 if after & DAP4 == 0 else 1)
PY

# HDMI_WIRED: the board whose chip has the I2S audio and the reset pin wired (cam0 | cam1; empty = auto: the only
# chip, or CAM0's with two). The board settings, then the app's file (it wins, as in the engine), whitelisted.
# The driver takes the reset pin when it probes, so its choice (module parameter reset_port) applies from the
# next boot.
wired=$({ sed -nE 's/^HDMI_WIRED=(cam0|cam1)?$/x\1/p' /etc/default/eirl-engine 2>/dev/null || true
          sed -nE 's/^HDMI_WIRED=(cam0|cam1)?$/x\1/p' /etc/eirl-encoder/engine.env 2>/dev/null || true; } | tail -1)
wired=${wired#x}
conf=/etc/modprobe.d/eirl-tc358743.conf
case "$wired" in
  cam0) want="options tc358743 reset_port=0" ;;
  cam1) want="options tc358743 reset_port=1" ;;
  *) want="" ;;
esac
have=$(grep -v '^#' "$conf" 2>/dev/null || true)
if [ "$want" != "$have" ]; then
  if [ -n "$want" ]; then
    printf '# Written by eirl-hdmi-init.sh from HDMI_WIRED: the TC358743 that holds the reset pin\n%s\n' "$want" > "$conf.tmp"
    mv "$conf.tmp" "$conf"
  else
    rm -f "$conf"
  fi
  echo "eirl-hdmi-init: reset pin for HDMI_WIRED=${wired:-auto} from the next boot"
fi

# Which device tree booted (UPDATES.md §12.11): the dual one has a TC358743 node behind both channels of the mux
dt=/proc/device-tree/cam_i2cmux
if [ -d "$dt/i2c@0/tc358743@f" ] && [ -d "$dt/i2c@1/tc358743@f" ]; then
  echo "eirl-hdmi-init: device tree with a TC358743 on CAM0 and on CAM1 (the dual one)"
else
  echo "eirl-hdmi-init: device tree with one TC358743 connector (the base's, or a board's own)"
fi

# The chips' subdevices (the module loads through udev, and only a chip that answers gets one). Once the first
# one is there, give the other connector's probe a moment (an empty connector takes ~1.5 s to give up).
find_subs() {
  subs=()
  for n in /sys/class/video4linux/v4l-subdev*/name; do
    grep -q '^tc358743 ' "$n" 2>/dev/null && subs+=("/dev/$(basename "$(dirname "$n")")")
  done
  return 0
}
subs=()
for _ in $(seq 1 30); do
  find_subs
  [ ${#subs[@]} -gt 0 ] && break; sleep 1
done
[ ${#subs[@]} -gt 0 ] || { echo "eirl-hdmi-init: no TC358743 subdevice (none on CAM0 or CAM1)" >&2; exit 1; }
sleep 2; find_subs
rc=0
for sub in "${subs[@]}"; do
  name=$(cat "/sys/class/video4linux/$(basename "$sub")/name")
  if v4l2-ctl -d "$sub" --set-edid=pad=0,file="$EDID",format=raw >/dev/null; then
    echo "eirl-hdmi-init: EDID $(basename "$EDID") loaded into $sub ($name)"
  else
    echo "eirl-hdmi-init: EDID not loaded into $sub ($name)" >&2; rc=1
  fi
done
exit $rc
