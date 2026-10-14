#!/usr/bin/env python3
"""Desfase A/V con el test de 'Audio Signal Suite' (pelota que bota + pitido en el bote).
Uso: avsync_ball.py recorte.ts   (recorte hecho con -c copy, sin -ss al analizar)."""
import subprocess, sys, struct, statistics
f = sys.argv[1]
def probe(sel, entry):
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", sel, "-show_entries", entry,
                          "-of", "csv=p=0", f], capture_output=True, text=True).stdout
    return [float(x.strip(",")) for x in out.split() if x.strip(",")]
W, H = 55, 195
raw = subprocess.run(["ffmpeg", "-v", "error", "-i", f, "-map", "0:v", "-vf",
       "crop=220:780:150:150,scale=%d:%d,format=gray" % (W, H), "-f", "rawvideo", "-"], capture_output=True).stdout
vpts = probe("v", "frame=pts_time")
nf = min(len(raw) // (W*H), len(vpts))
ys = []
for i in range(nf):
    fr = raw[i*W*H:(i+1)*W*H]
    pix = [k // W for k, v in enumerate(fr) if v > 200]
    ys.append(statistics.mean(pix) if len(pix) > 20 else None)
vals = [y for y in ys if y is not None]; lo, hi = min(vals), max(vals)
bounces = [vpts[i] for i in range(1, nf-1) if None not in (ys[i-1], ys[i], ys[i+1])
           and ys[i] >= ys[i-1] and ys[i] > ys[i+1] and ys[i] > lo + 0.8*(hi-lo)]
astart = probe("a", "stream=start_time")[0]
araw = subprocess.run(["ffmpeg", "-v", "error", "-i", f, "-map", "0:a", "-ac", "1", "-ar", "8000",
                       "-f", "s16le", "-"], capture_output=True).stdout
s = struct.unpack("<%dh" % (len(araw)//2), araw); Wn = 40
env = [(sum(x*x for x in s[i:i+Wn])/Wn) ** 0.5 for i in range(0, len(s)-Wn, Wn)]
emed = statistics.median(env); thr = emed + (max(env)-emed)*0.3
beeps = [astart + i*Wn/8000.0 for i in range(1, len(env)) if env[i] >= thr and env[i-1] < thr]
offs = []
for b in bounces:
    c = min(beeps, key=lambda x: abs(x-b), default=None)
    if c is not None and abs(c-b) < 0.4: offs.append((c-b)*1000)
print("frames=%d botes=%d pitidos=%d pares=%d  (altura pelota %.0f..%.0f)" % (nf, len(bounces), len(beeps), len(offs), lo, hi))
if offs:
    print("desfase audio−vídeo: mediana %+.0f ms (min %+.0f, max %+.0f; >0 = audio RETRASADO; resolución ±33 ms)"
          % (statistics.median(offs), min(offs), max(offs)))
    print("pares:", " ".join("%+.0f" % o for o in offs))
