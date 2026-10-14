#!/bin/bash
# On the board (copy next to thrcpu.py): CPU of the engine (eirl-engine-go) per thread and of the system for N s; source states.
secs=${1:-60}
eng=$(pgrep -f "^/usr/lib/eirl-encoder/engine/eirl-engine-go" | head -1)
eirl-enginectl state | python3 -c "import json,sys; s=json.load(sys.stdin); print('output', s['output']['mode'], '|', ' '.join('%s:%s' % (x['slot'], x['state']) for x in s['sources']), '| screen', s['screen']['state'], '| preview', s.get('preview'), '| newFps', s['program'].get('newFps'))"
# /proc/stat: user nice system idle iowait irq softirq steal. Busy = all but idle and iowait: IRQ and
# softirq included (~20 points on the board: host1x and USB 3 interrupts, on CPU0)
busy_total() { awk '/^cpu /{t = 0; for (i = 2; i <= 9; i++) t += $i; print t - $5 - $6, t}' /proc/stat; }
read -r b1 t1 < <(busy_total)
python3 "$(dirname "$0")/thrcpu.py" "$eng" "$secs" > /tmp/thr.out
read -r b2 t2 < <(busy_total)
head -1 /tmp/thr.out
awk 'NR>1 && NR<=13' /tmp/thr.out
echo "system (4 cores, IRQ and softirq included): $(( (b2 - b1) * 400 / (t2 - t1) )) % of one core"
