#!/usr/bin/env python3
# En la placa: medida del sistema base durante N s (BASE.md, B0). Solo lee /proc, /sys y el estado
# del motor. Complementa a engine-cpu.sh: desglosa lo de FUERA del motor (servicios, hilos del
# kernel, IRQs), la RAM por servicio, las escrituras en la SD y el consumo.
#
#   sudo python3 base-measure.py <segundos> <etiqueta> [salida.json]
#   python3 base-measure.py --compare a.json b.json
#
# Con sudo para leer smaps e io de todos los procesos. Python 3.6: sin f-strings ni dataclasses.
import glob
import json
import os
import subprocess
import sys
import time

TCK = os.sysconf('SC_CLK_TCK')
CG_CPU = '/sys/fs/cgroup/cpu,cpuacct'
INA = glob.glob('/sys/bus/i2c/drivers/ina3221x/6-0040/iio:device*')
RAILS = ('5V_IN', '5V_GPU', '5V_CPU')
MIXERCTL = 'eirl-enginectl'


def read(path, default=''):
    try:
        # errors='replace': smaps y cmdline pueden traer bytes que no son UTF-8 (nombres de mapeos)
        with open(path, errors='replace') as f:
            return f.read()
    except (IOError, OSError):
        return default


def cpu_stat():
    """/proc/stat: por núcleo, ticks de user nice system idle iowait irq softirq steal."""
    out, ctxt, intr = {}, 0, 0
    for line in read('/proc/stat').splitlines():
        p = line.split()
        if p[0].startswith('cpu'):
            out[p[0]] = [int(x) for x in p[1:9]]
        elif p[0] == 'ctxt':
            ctxt = int(p[1])
        elif p[0] == 'intr':
            intr = int(p[1])
    return out, ctxt, intr


def irq_table(path):
    """/proc/interrupts o /proc/softirqs: nombre -> contadores por núcleo."""
    lines = read(path).splitlines()
    ncpu = len(lines[0].split())
    out = {}
    for line in lines[1:]:
        p = line.split()
        if len(p) < 2:
            continue
        key = p[0].rstrip(':')
        counts = []
        for x in p[1:1 + ncpu]:
            if not x.isdigit():
                break
            counts.append(int(x))
        if len(counts) != ncpu:
            continue
        name = ' '.join(p[1 + ncpu:]) or key
        out[key] = (name, counts)
    return out


def units_cpu():
    """CPU acumulada (ns) por unidad, del cgroup cpuacct: cuenta también los procesos que duran poco."""
    out = {}
    for d in glob.glob(CG_CPU + '/system.slice/*') + glob.glob(CG_CPU + '/user.slice'):
        if os.path.isdir(d):
            v = read(d + '/cpuacct.usage').strip()
            if v.isdigit():
                out[os.path.basename(d)] = int(v)
    v = read(CG_CPU + '/cpuacct.usage').strip()
    out['_total'] = int(v) if v.isdigit() else 0
    return out


def unit_of(pid):
    for line in read('/proc/%s/cgroup' % pid).splitlines():
        if ':name=systemd:' in line:
            path = line.split(':', 2)[2]
            if path == '/':
                return 'kernel'
            if path.startswith('/user.slice'):
                return 'user.slice'
            return path.rstrip('/').split('/')[-1]
    return 'kernel'


def procs():
    """pid -> (comm, ppid, ticks, write_bytes, unidad)."""
    out = {}
    me = os.getpid()
    for d in glob.glob('/proc/[0-9]*'):
        pid = int(d[6:])
        if pid == me:
            continue
        st = read(d + '/stat')
        if not st:
            continue
        comm = st[st.index('(') + 1:st.rindex(')')]
        rest = st[st.rindex(')') + 2:].split()
        ppid, ticks = int(rest[1]), int(rest[11]) + int(rest[12])
        wb = 0
        for line in read(d + '/io').splitlines():
            if line.startswith('write_bytes:'):
                wb = int(line.split()[1])
        out[pid] = (comm, ppid, ticks, wb, unit_of(pid))
    return out


def sd_sectors():
    for line in read('/proc/diskstats').splitlines():
        p = line.split()
        if len(p) > 9 and p[2] == 'mmcblk0':
            return int(p[9])
    return 0


def power():
    if not INA:
        return None
    return [int(read('%s/in_power%d_input' % (INA[0], i), '0').strip() or 0) for i in range(3)]


def meminfo():
    out = {}
    for line in read('/proc/meminfo').splitlines():
        k, v = line.split(':', 1)
        if k in ('MemTotal', 'MemAvailable', 'Cached', 'NvMapMemUsed', 'Slab', 'SwapFree', 'SwapTotal'):
            out[k] = int(v.split()[0]) // 1024
    return out


def pss_by_unit():
    """Pss y memoria anónima (MB) por unidad. Kernel 4.9: sin smaps_rollup, se suma smaps entero."""
    out = {}
    for d in glob.glob('/proc/[0-9]*'):
        pss = anon = 0
        for line in read(d + '/smaps').splitlines():
            if line.startswith('Pss:'):
                pss += int(line.split()[1])
            elif line.startswith('Anonymous:'):
                anon += int(line.split()[1])
        if pss:
            u = unit_of(d[6:])
            a = out.setdefault(u, [0, 0])
            a[0] += pss
            a[1] += anon
    return dict((u, {'pssMB': round(v[0] / 1024.0, 1), 'anonMB': round(v[1] / 1024.0, 1)})
                for u, v in out.items())


def temps():
    out = {}
    for z in glob.glob('/sys/class/thermal/thermal_zone*'):
        t = read(z + '/temp').strip()
        if t.lstrip('-').isdigit():
            out[read(z + '/type').strip()] = round(int(t) / 1000.0, 1)
    return out


def engine_context():
    """Las mismas condiciones que anota engine-cpu.sh: salida, fuentes, pantalla y preview."""
    try:
        raw = subprocess.check_output([MIXERCTL, 'state'], timeout=5)
        s = json.loads(raw.decode('utf-8'))
    except Exception as e:  # el motor puede estar parado: la medida sigue valiendo para la base
        return {'error': str(e)}
    return {
        'output': s.get('output', {}).get('mode'),
        'sources': dict((x.get('slot'), x.get('state')) for x in s.get('sources', [])),
        'screen': s.get('screen', {}).get('state'),
        'preview': s.get('preview'),
        'newFps': s.get('program', {}).get('newFps'),
    }


def measure(secs, label):
    ctx0 = engine_context()
    c0, x0, i0 = cpu_stat()
    irq0, sirq0 = irq_table('/proc/interrupts'), irq_table('/proc/softirqs')
    u0, p0, sd0 = units_cpu(), procs(), sd_sectors()
    pw = []
    t0 = time.time()
    while time.time() - t0 < secs:
        s = power()
        if s:
            pw.append(s)
        time.sleep(1)
    dt = time.time() - t0
    c1, x1, i1 = cpu_stat()
    irq1, sirq1 = irq_table('/proc/interrupts'), irq_table('/proc/softirqs')
    u1, p1, sd1 = units_cpu(), procs(), sd_sectors()
    ctx1 = engine_context()

    # CPU en % de UN núcleo (el sistema entero llega a 400). Ocupado = todo menos idle e iowait.
    cores = {}
    for k in sorted(c1):
        d = [b - a for a, b in zip(c0[k], c1[k])]
        tot = float(sum(d)) or 1.0
        scale = (400.0 if k == 'cpu' else 100.0) / tot
        cores[k] = {
            'busy': round((tot - d[3] - d[4]) * scale, 1),
            'user': round((d[0] + d[1]) * scale, 1), 'sys': round(d[2] * scale, 1),
            'irq': round(d[5] * scale, 1), 'softirq': round(d[6] * scale, 1),
            'iowait': round(d[4] * scale, 1),
        }

    # Por unidad (cgroup), en % de un núcleo.
    units = {}
    for k, v in u1.items():
        if k != '_total' and k in u0 and v > u0[k]:
            units[k] = round((v - u0[k]) / 1e9 / dt * 100, 1)
    # Hilos del kernel (hijos de kthreadd) y procesos sueltos fuera de cgroup de unidad.
    kth = {}
    for pid, (comm, ppid, ticks, _, unit) in p1.items():
        if unit != 'kernel':
            continue
        before = p0[pid][2] if pid in p0 else 0
        d = ticks - before
        if d > 0:
            key = comm.split('/')[0] + '/' + comm.split('/')[1].split(':')[0] if '/' in comm else comm
            kth[key] = kth.get(key, 0) + d
    kth = dict((k, round(v * 100.0 / TCK / dt, 1)) for k, v in kth.items())
    units['kernel (hilos)'] = round(sum(kth.values()), 1)

    # Escrituras por unidad (bytes que llegan al bloque; los procesos que acaban dentro no cuentan).
    wr = {}
    for pid, (comm, _, _, wb, unit) in p1.items():
        d = wb - (p0[pid][3] if pid in p0 else 0)
        if d > 0:
            key = unit if unit != 'kernel' else 'kernel:' + comm
            wr[key] = wr.get(key, 0) + d
    wr = dict((k, round(v / 1024.0 / dt, 2)) for k, v in wr.items())

    def rates(t0_, t1_, top):
        out = []
        for k, (name, cnt) in t1_.items():
            if k not in t0_:
                continue
            d = [b - a for a, b in zip(t0_[k][1], cnt)]
            if sum(d):
                out.append({'irq': k, 'name': name, 'perSec': round(sum(d) / dt),
                            'perCpu': [round(x / dt) for x in d]})
        out.sort(key=lambda r: -r['perSec'])
        return out[:top]

    res = {
        'label': label, 'at': time.strftime('%Y-%m-%d %H:%M:%S'), 'seconds': round(dt, 1),
        'engineVersion': read('/usr/lib/eirl-encoder/engine/VERSION').strip(),
        'uptimeS': int(float(read('/proc/uptime', '0').split()[0])),
        'context': {'start': ctx0, 'end': ctx1},
        'cpu': cores,
        'units': dict(sorted(units.items(), key=lambda kv: -kv[1])),
        'kernelThreads': dict(sorted(kth.items(), key=lambda kv: -kv[1])[:12]),
        'ctxtPerSec': round((x1 - x0) / dt), 'intrPerSec': round((i1 - i0) / dt),
        'irqs': rates(irq0, irq1, 12), 'softirqs': rates(sirq0, sirq1, 10),
        'mem': meminfo(), 'pss': pss_by_unit(),
        'sdKBs': round((sd1 - sd0) * 512 / 1024.0 / dt, 2),
        'writesKBs': dict(sorted(wr.items(), key=lambda kv: -kv[1])[:10]),
        'temps': temps(),
    }
    if pw:
        res['powerMw'] = dict((RAILS[i], round(sum(s[i] for s in pw) / float(len(pw))))
                              for i in range(3))
    return res


def summary(r):
    c = r['cpu']['cpu']
    print('%s · %s · %ss · motor %s · uptime %ss' % (r['label'], r['at'], r['seconds'],
                                                     r['engineVersion'][:7], r['uptimeS']))
    print('contexto: %s' % json.dumps(r['context']['start'], ensure_ascii=False))
    if r['context']['start'] != r['context']['end']:
        print('⚠️  el contexto cambió durante la medida: %s' % json.dumps(r['context']['end'], ensure_ascii=False))
    print('CPU sistema %.1f %% de 400 (user %.1f · sys %.1f · irq %.1f · softirq %.1f · iowait %.1f)' % (
        c['busy'], c['user'], c['sys'], c['irq'], c['softirq'], c['iowait']))
    print('por núcleo: %s' % ' · '.join('%s %.0f' % (k, v['busy']) for k, v in sorted(r['cpu'].items()) if k != 'cpu'))
    print('por unidad (%% de un núcleo): %s' % ' · '.join('%s %.1f' % kv for kv in list(r['units'].items())[:12]))
    print('hilos del kernel: %s' % ' · '.join('%s %.1f' % kv for kv in list(r['kernelThreads'].items())[:8]))
    print('IRQs/s: %s' % ' · '.join('%s %d %s' % (x['name'][-18:], x['perSec'], x['perCpu']) for x in r['irqs'][:6]))
    print('ctxt/s %d · intr/s %d' % (r['ctxtPerSec'], r['intrPerSec']))
    m = r['mem']
    print('RAM disponible %d MB de %d · NvMap %s MB' % (m.get('MemAvailable', 0), m.get('MemTotal', 0), m.get('NvMapMemUsed')))
    top = sorted(r['pss'].items(), key=lambda kv: -kv[1]['pssMB'])[:10]
    print('Pss por unidad (MB): %s' % ' · '.join('%s %.0f' % (k, v['pssMB']) for k, v in top))
    print('SD %.1f KB/s · %s' % (r['sdKBs'], ' · '.join('%s %.1f' % kv for kv in list(r['writesKBs'].items())[:6])))
    if 'powerMw' in r:
        print('consumo (mW): %s' % ' · '.join('%s %d' % kv for kv in sorted(r['powerMw'].items())))


def compare(a, b):
    ra, rb = json.load(open(a)), json.load(open(b))
    print('%-28s %12s %12s %9s' % ('', ra['label'][:12], rb['label'][:12], 'Δ'))

    def row(name, x, y):
        if x is None and y is None:
            return
        x, y = x or 0, y or 0
        print('%-28s %12.1f %12.1f %+9.1f' % (name[:28], x, y, y - x))

    row('CPU sistema (de 400)', ra['cpu']['cpu']['busy'], rb['cpu']['cpu']['busy'])
    for k in ('irq', 'softirq', 'sys', 'user'):
        row('  ' + k, ra['cpu']['cpu'][k], rb['cpu']['cpu'][k])
    for u in sorted(set(ra['units']) | set(rb['units']), key=lambda u: -max(ra['units'].get(u, 0), rb['units'].get(u, 0))):
        row('  ' + u, ra['units'].get(u), rb['units'].get(u))
    row('intr/s', ra['intrPerSec'], rb['intrPerSec'])
    row('ctxt/s', ra['ctxtPerSec'], rb['ctxtPerSec'])
    row('RAM disponible (MB)', ra['mem'].get('MemAvailable'), rb['mem'].get('MemAvailable'))
    row('SD (KB/s)', ra['sdKBs'], rb['sdKBs'])
    for k in RAILS:
        row('consumo ' + k + ' (mW)', ra.get('powerMw', {}).get(k), rb.get('powerMw', {}).get(k))


if __name__ == '__main__':
    if len(sys.argv) == 4 and sys.argv[1] == '--compare':
        compare(sys.argv[2], sys.argv[3])
        sys.exit(0)
    if len(sys.argv) < 3:
        sys.exit(__doc__ or 'uso: sudo python3 base-measure.py <segundos> <etiqueta> [salida.json]')
    r = measure(float(sys.argv[1]), sys.argv[2])
    summary(r)
    if len(sys.argv) > 3:
        with open(sys.argv[3], 'w') as f:
            json.dump(r, f, ensure_ascii=False, indent=1)
