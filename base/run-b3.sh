#!/bin/bash
# B3 de una vez, EN la placa y desacoplado del ssh (setsid nohup): purga los grupos en orden con
# purge.sh. Si un plan ya no coincide (lo ha cambiado la purga anterior), lo vuelve a simular y solo
# sigue si el plan nuevo NO trae paquetes que no estuvieran en el revisado. Cualquier fallo, para.
#   sudo setsid nohup base/run-b3.sh sdk docker apps extra > ~/eirl-base/b3.log 2>&1 < /dev/null &
set -u
export LC_ALL=C   # comm y sort con el mismo orden que purge.sh
HERE=$(cd "$(dirname "$0")" && pwd)
ST=/var/lib/eirl-base
for g in "$@"; do
    echo "===== $(date +%H:%M:%S) $g"
    cp "$ST/$g.plan" "$ST/$g.plan.revisado"
    if ! "$HERE/purge.sh" --apply "$g"; then
        echo "--- $g: vuelvo a simular"
        "$HERE/purge.sh" --plan "$g" || { echo "PARO: el plan nuevo de $g no es seguro"; exit 1; }
        nuevos=$(comm -13 "$ST/$g.plan.revisado" "$ST/$g.plan")
        if [ -n "$nuevos" ]; then
            echo "PARO: el plan nuevo de $g trae paquetes no revisados:"; echo "$nuevos"; exit 1
        fi
        "$HERE/purge.sh" --apply "$g" || { echo "PARO: $g ha fallado"; exit 1; }
    fi
    echo "--- $(date +%H:%M:%S) $g hecho · libre en /: $(df -h / | awk 'NR==2{print $4}') · fallidas: $(systemctl --failed --no-legend | wc -l)"
done
echo "===== $(date +%H:%M:%S) FIN B3 ($*)"
