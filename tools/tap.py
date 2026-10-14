#!/usr/bin/env python3
# Injects a touch into the board's touch screen: writing to /dev/input/eventN puts it in the device's
# event stream, like evemu-play, and the engine gets it as if it were a finger. Coordinates in screen px
# (1024×600, which is also the touch range). Do NOT use it while someone is using the screen.
#   scp tools/tap.py jetson:/tmp/ && ssh jetson 'sudo python3 /tmp/tap.py <x> <y> [seconds held]'
# Long press (Stream, Power): 2.5 s. Careful: holding «Stream» REALLY STARTS STREAMING.
import glob, os, struct, sys, time
EV = struct.Struct('llHHi')
path = None
for dev in sorted(glob.glob('/sys/class/input/event*')):
    bits = 0
    for word in open(dev + '/device/capabilities/abs').read().split():
        bits = (bits << 64) | int(word, 16)
    if bits >> 0x35 & 1:
        path = '/dev/input/' + os.path.basename(dev)
        break
fd = os.open(path, os.O_WRONLY)
def ev(t, c, v):
    os.write(fd, EV.pack(0, 0, t, c, v))
x, y = int(sys.argv[1]), int(sys.argv[2])
hold = float(sys.argv[3]) if len(sys.argv) > 3 else 0.1
ev(3, 0x39, 77); ev(3, 0x35, x); ev(3, 0x36, y); ev(1, 0x14a, 1); ev(0, 0, 0)
time.sleep(hold)
ev(3, 0x39, -1); ev(1, 0x14a, 0); ev(0, 0, 0)
