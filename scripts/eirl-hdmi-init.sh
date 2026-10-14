#!/usr/bin/env bash
# eirl-hdmi-init.sh — preparación del TC358743 al arrancar (lo ejecuta eirl-hdmi-init.service, como root).
#  1. Devuelve los pines DAP4 (J4-J7 = I2S4 del conector de 40 pines) al I2S: el BOOTLOADER los deja
#     en modo GPIO (bit CNF a 1) y ni el pinmux del DT ni liberar el GPIO lo deshacen en este kernel;
#     sin esto el I2S4 no recibe reloj ("Failed at I2S4 DAP RX sw reset"). Escribe el registro con
#     máscara MSK_CNF del puerto J (solo toca los bits 4-7).
#  2. Carga el EDID, para que la fuente HDMI vea el monitor aunque no se esté emitiendo (el EDID del
#     TC358743 vive en RAM y se pierde en cada arranque).
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

# Esperar al subdev del TC358743 (el módulo carga por udev)
sub=""
for _ in $(seq 1 30); do
  for n in /sys/class/video4linux/v4l-subdev*/name; do
    grep -q tc358743 "$n" 2>/dev/null && sub="/dev/$(basename "$(dirname "$n")")"
  done
  [ -n "$sub" ] && break; sleep 1
done
[ -n "$sub" ] || { echo "eirl-hdmi-init: no aparece el subdev del TC358743" >&2; exit 1; }
v4l2-ctl -d "$sub" --set-edid=pad=0,file="$EDID",format=raw >/dev/null
echo "eirl-hdmi-init: EDID $(basename "$EDID") cargado en $sub"
