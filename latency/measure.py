#!/usr/bin/env python3
"""Measures the end-to-end latency: screen of the HDMI source → Jetson → SRT → receiver.

The source shows clock.html (served by clock_server.py on the Jetson) full screen on the output that goes
to the TC358743. This script reads the stream, decodes in each frame the Jetson's time that was on the
screen and compares it with the frame's arrival time (converted to the Jetson's time with a /now request
compensated for RTT). It depends neither on the source's clock nor on this computer's.

Usage: measure.py <stream url> <http://jetson:8080> [seconds]
  e.g. measure.py rtsp://127.0.0.1:8554/jetson http://jetson.local:8080 30"""
import statistics, subprocess, sys, time, urllib.request

url, clock, dur = sys.argv[1], sys.argv[2].rstrip('/'), float(sys.argv[3]) if len(sys.argv) > 3 else 30
W, N = 1920, 32

def offset():
    """The Jetson's time - time.time(), in ms (best of 15 requests by RTT)."""
    best = None
    for _ in range(15):
        t0 = time.time() * 1000
        srv = float(urllib.request.urlopen(clock + '/now', timeout=2).read())
        t1 = time.time() * 1000
        if not best or t1 - t0 < best[0]:
            best = (t1 - t0, srv + (t1 - t0) / 2 - t1)
    return best

rtt, off = offset()
print(f'offset to the Jetson {off:+.1f} ms (RTT {rtt:.1f} ms)')
ff = subprocess.Popen(['ffmpeg', '-v', 'error', '-fflags', 'nobuffer', '-flags', 'low_delay',
                       '-rtsp_transport', 'tcp', '-i', url, '-an',
                       '-vf', f'scale={W}:1080,crop={W}:1:0:810,format=gray', '-f', 'rawvideo', '-'],
                      stdout=subprocess.PIPE)
lat, bad, end = [], 0, time.time() + dur
while time.time() < end:
    row = ff.stdout.read(W)
    if len(row) < W:
        break
    now = time.time() * 1000 + off
    bits = [1 if row[int((i + 0.5) * W / N)] > 128 else 0 for i in range(N)]
    v = int(''.join(map(str, bits[2:26])), 2)
    c = int(''.join(map(str, bits[26:30])), 2)
    if bits[:2] != [1, 0] or bits[30:] != [0, 1] or c != v % 13:
        bad += 1
        continue
    d = (now - v) % 16777216            # undo the mod 2^24, centred (±2.3 h)
    lat.append(d - 16777216 if d > 8388608 else d)
ff.kill()
if not lat:
    sys.exit(f'no decodable frame ({bad} discarded): is clock.html full screen?')
q = statistics.quantiles(lat, n=20)
print(f'{len(lat)} frames ({bad} discarded): median {statistics.median(lat):.0f} ms, '
      f'p5 {q[0]:.0f}, p95 {q[-1]:.0f}, min {min(lat):.0f}, max {max(lat):.0f}')
