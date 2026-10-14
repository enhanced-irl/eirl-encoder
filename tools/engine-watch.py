#!/usr/bin/env python3
"""Lectura del motor durante una prueba, SOLO LEE (no manda órdenes ni toca servicios).
Cada 30 s: RAM del motor y su pendiente, CPU del motor y del sistema, salida, fuente al aire, frames
nuevos/s del programa y fps de cada fuente. Al final, los «tiempos» del motor del journal (dibujo,
programa→pantalla) y sus avisos. Uso: engine-watch.py [minutos] (por defecto 35). Escribe en ~/eirl-watch-2d/."""
import collections, json, os, re, subprocess, sys, time
MINS = float(sys.argv[1]) if len(sys.argv) > 1 else 35
DIR = os.path.expanduser('~/eirl-watch-2d')
os.makedirs(DIR, exist_ok=True)
OUT = os.path.join(DIR, 'watch-%s.log' % time.strftime('%m%d-%H%M'))
HZ = os.sysconf('SC_CLK_TCK')
start_wall = time.strftime('%Y-%m-%d %H:%M:%S')

def out(line):
    with open(OUT, 'a') as f:
        f.write(line + '\n')

def engine_pid():
    # The Go engine (the Python one, engine-run.py, was retired on Oct 14). Since it runs as its own user,
    # reading its /proc (smaps, fds) may need sudo; stat and status are readable
    r = subprocess.run(['pgrep', '-f', '^/usr/lib/eirl-encoder/engine/eirl-engine-go'], stdout=subprocess.PIPE, universal_newlines=True)
    return int(r.stdout.split()[0]) if r.stdout.split() else None

def proc_cpu(pid):
    try:
        with open('/proc/%d/stat' % pid) as f:
            p = f.read().rsplit(')', 1)[1].split()
        return int(p[11]) + int(p[12])
    except (OSError, IndexError):
        return None

def rss_mb(pid):
    try:
        with open('/proc/%d/status' % pid) as f:
            return int(re.search(r'VmRSS:\s+(\d+)', f.read()).group(1)) / 1024.0
    except (OSError, AttributeError):
        return None

def sys_cpu():
    with open('/proc/stat') as f:
        v = [int(x) for x in f.readline().split()[1:]]
    return sum(v) - v[3] - v[4], sum(v)

def state():
    try:
        r = subprocess.run([os.path.expanduser('eirl-enginectl'), 'state'], stdout=subprocess.PIPE,
                           stderr=subprocess.DEVNULL, universal_newlines=True, timeout=5)
        s = json.loads(r.stdout)
        src = ' '.join('%s:%s/%.0f' % (x['slot'], x['state'][:4], x.get('fps') or 0) for x in s.get('sources', []))
        pg = s.get('program') or {}
        return '%s aire=%s nuevos=%s %s' % ((s.get('output') or {}).get('mode'), (pg.get('main') or '?')[-14:], pg.get('newFps'), src)
    except Exception as e:  # noqa
        return 'estado ? (%s)' % type(e).__name__

out('# lectura desde %s, %g min, cada 30 s (solo lee)' % (start_wall, MINS))
out('# hora     RAM MB  MB/min(5 min)  motor %  sistema %(de 400)  salida aire nuevos/s fuentes(hueco:estado/fps)')
end = time.time() + MINS * 60
pid = engine_pid()
hist = collections.deque()   # (hora, RAM): pendiente sobre los últimos 5 min (el arranque la sube sola)
while time.time() < end:
    p0, (b0, t0), w0 = (proc_cpu(pid) if pid else None), sys_cpu(), time.time()
    time.sleep(30)
    now_pid = engine_pid()
    if now_pid != pid:
        out('%s  el motor ha cambiado de proceso: %s -> %s' % (time.strftime('%H:%M:%S'), pid, now_pid))
        pid = now_pid
        hist.clear()
        continue
    if pid is None:
        out('%s  sin motor' % time.strftime('%H:%M:%S')); continue
    p1, (b1, t1), w1 = proc_cpu(pid), sys_cpu(), time.time()
    rss = rss_mb(pid)
    if rss is not None:
        hist.append((w1, rss))
    while len(hist) > 2 and w1 - hist[1][0] >= 300:
        hist.popleft()
    slope = (hist[-1][1] - hist[0][1]) / ((hist[-1][0] - hist[0][0]) / 60.0) if len(hist) > 1 else 0
    motor = 100.0 * (p1 - p0) / HZ / (w1 - w0) if p0 is not None and p1 is not None else -1
    out('%s  %6.1f  %+7.1f  %6.0f  %6.0f   %s' % (time.strftime('%H:%M:%S'), rss or -1, slope, motor,
                                                   400.0 * (b1 - b0) / max(t1 - t0, 1), state()))
j = subprocess.run(['journalctl', '-u', 'eirl-engine', '--since', start_wall, '--no-pager', '-o', 'short-iso'],
                   stdout=subprocess.PIPE, universal_newlines=True).stdout.splitlines()
out('# tiempos del motor (cada ~60 s): programa→pantalla y motor máx')
for line in j:
    m = re.search(r'(programa→pantalla: [^·(]*).*(motor máx: .*)$', line)
    if m:
        out('%s  %s| %s' % (line[11:19], m.group(1), m.group(2)[:120]))
out('# avisos')
for line in j:
    if re.search(r'frames nuevos|fin de la fuente|NO fiables|Traceback|ha fallado|salida en caliente|Started|Stopped', line):
        out('%s  %s' % (line[11:19], line.split(': ', 1)[-1][:200]))
out('# fin %s' % time.strftime('%H:%M:%S'))
