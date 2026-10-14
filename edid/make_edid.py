#!/usr/bin/env python3
"""EDID para el TC358743: solo audio LPCM estéreo 48 kHz 16 bit y solo los modos progresivos que caben
en 2 lanes CSI a 972 Mbps en UYVY. La fuente elige entre ellos (normalmente el preferido) y el script
emite lo que llegue:

  1080p50 · 1080p30 · 1080p25 · 1080p24 · 720p60 · 720p50 · 576p50 · 480p60 · VGA

Nunca 1080p60 (no cabe: el CSI necesitaría el 107 % de cada línea) ni modos entrelazados (el VI del
Nano no los captura).

  make_edid.py <salida.bin> [1080p30|1080p50|720p60|480p]

El segundo argumento es el modo PREFERIDO (1080p30 por defecto); la lista es la misma en todos menos
en 480p, que es solo 480p60 (diagnóstico)."""
import sys

def dtd(pclk_khz, ha, hb, hfp, hsw, va, vb, vfp, vsw, hmm=160, vmm=90, flags=0x1E):
    p = pclk_khz // 10
    return bytes([p & 0xFF, p >> 8,
        ha & 0xFF, hb & 0xFF, ((ha >> 8) << 4) | (hb >> 8),
        va & 0xFF, vb & 0xFF, ((va >> 8) << 4) | (vb >> 8),
        hfp & 0xFF, hsw & 0xFF, ((vfp & 0xF) << 4) | (vsw & 0xF),
        ((hfp >> 8) << 6) | ((hsw >> 8) << 4) | ((vfp >> 4) << 2) | (vsw >> 4),
        hmm & 0xFF, vmm & 0xFF, ((hmm >> 8) << 4) | (vmm >> 8), 0, 0, flags])

def text_desc(tag, txt):
    b = txt.encode()[:13]
    if len(b) < 13: b += b"\n" + b" " * (12 - len(b))
    return bytes([0, 0, 0, tag, 0]) + b

def csum(block):
    return block + bytes([(-sum(block)) & 0xFF])

# Timings CTA-861: (DTD, VIC)
MODES = {
    "1080p50": (dtd(148500, 1920, 720, 528, 44, 1080, 45, 4, 5), 31),
    "1080p30": (dtd(74250, 1920, 280, 88, 44, 1080, 45, 4, 5), 34),
    "720p60": (dtd(74250, 1280, 370, 110, 40, 720, 30, 5, 5), 4),
    "480p": (dtd(27000, 720, 138, 16, 62, 480, 45, 9, 6, flags=0x18), 3),  # -hsync -vsync
}
# Orden de la lista de modos (VIC): 1080p50, 1080p30, 1080p25, 1080p24, 720p60, 720p50,
# 576p50 16:9, 480p 16:9 y 4:3, VGA
VICS = [31, 34, 33, 32, 4, 19, 18, 3, 2, 1]

PROFILE = sys.argv[2] if len(sys.argv) > 2 else "1080p30"
if PROFILE not in MODES:
    sys.exit("perfil desconocido: %s (1080p30, 1080p50, 720p60 o 480p)" % PROFILE)

base = bytearray()
base += bytes([0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0])
base += bytes([0x31, 0xD8])          # fabricante "LNX"
base += bytes([0x43, 0x74])          # producto 0x7443
base += bytes([0, 0, 0, 0])          # serie
base += bytes([40, 36])              # semana 40, 2026
base += bytes([1, 3])                # EDID 1.3
base += bytes([0x80, 16, 9, 0x78, 0x0A])   # digital, 16x9 cm, gamma 2.2, RGB + timing preferido
base += bytes([0xEE, 0x91, 0xA3, 0x54, 0x4C, 0x99, 0x26, 0x0F, 0x50, 0x54])  # cromaticidad sRGB
base += bytes([0x20, 0, 0])          # established: 640x480@60
base += bytes([1, 1] * 8)            # sin standard timings
if PROFILE == "480p":
    base += MODES["480p"][0] * 2                 # solo 480p60 (27 MHz) para diagnóstico
    vics, max_mhz = [3, 2], 30
else:
    # Dos DTD: el preferido y, de respaldo para fuentes que no leen la extensión, el otro de 1080p
    # (o 1080p30 si el preferido es 720p60)
    second = "1080p30" if PROFILE != "1080p30" else "1080p50"
    base += MODES[PROFILE][0] + MODES[second][0]
    vics = [MODES[PROFILE][1]] + [v for v in VICS if v != MODES[PROFILE][1]]
    max_mhz = 150
base += text_desc(0xFC, "JetsonHDMI-IN")
# límites: 24-61 Hz vertical, 15-70 kHz horizontal, reloj de píxel máx. (en 10 MHz)
base += bytes([0, 0, 0, 0xFD, 0, 24, 61, 15, 70, max_mhz // 10, 0, 0x0A, 0x20, 0x20, 0x20, 0x20, 0x20, 0x20])
base += bytes([1])                   # 1 extensión
assert len(base) == 127
block0 = csum(bytes(base))

vdb = bytes([0x40 | len(vics), 0x80 | vics[0]] + vics[1:])   # el primero, nativo
adb = bytes([0x20 | 3, (1 << 3) | 1, 0x04, 0x01])            # LPCM, 2 canales, solo 48 kHz, solo 16 bit
sadb = bytes([0x80 | 3, 0x01, 0, 0])                          # altavoces FL/FR
# VSDB HDMI, PA 1.0.0.0, TMDS máx. en 5 MHz: 150 MHz (1080p50 va a 148,5) o 30 MHz en 480p
hdmi = bytes([0x60 | 7, 0x03, 0x0C, 0x00, 0x10, 0x00, 0x00, max_mhz // 5])
data = vdb + adb + sadb + hdmi
ext = bytes([0x02, 0x03, 4 + len(data), 0x70]) + data         # basic audio + YCbCr 4:4:4/4:2:2
ext += bytes(127 - len(ext))
block1 = csum(ext)

edid = block0 + block1
assert sum(block0) % 256 == 0 and sum(block1) % 256 == 0
open(sys.argv[1] if len(sys.argv) > 1 else "edid.bin", "wb").write(edid)
