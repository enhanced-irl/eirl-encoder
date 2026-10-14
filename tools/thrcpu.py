# CPU por hilo de un proceso durante N segundos (de /proc, sin engancharse al proceso).
import os, sys, time
pid, secs = sys.argv[1], float(sys.argv[2]) if len(sys.argv) > 2 else 10
hz = os.sysconf('SC_CLK_TCK')
def snap():
    out = {}
    for t in os.listdir('/proc/%s/task' % pid):
        try:
            s = open('/proc/%s/task/%s/stat' % (pid, t)).read()
        except OSError:
            continue
        name = s[s.index('(') + 1:s.rindex(')')]
        f = s[s.rindex(')') + 2:].split()
        out[t] = (name, int(f[11]), int(f[12]))
    return out
a = snap(); time.sleep(secs); b = snap()
rows = []
for t, (name, u, s) in b.items():
    if t in a:
        du, ds = u - a[t][1], s - a[t][2]
        rows.append(((du + ds) * 100.0 / hz / secs, du * 100.0 / hz / secs, ds * 100.0 / hz / secs, name, t))
rows.sort(reverse=True)
tot = sum(r[0] for r in rows)
print('total %.0f %% (usuario %.0f, sistema %.0f) en %d hilos' % (tot, sum(r[1] for r in rows), sum(r[2] for r in rows), len(rows)))
for r in rows[:22]:
    print('%5.1f %%  (u %4.1f s %4.1f)  %-16s %s' % r)
