#!/bin/bash
# Tras cada grupo de la B3, junto a check-engine.sh: carga de verdad las librerías de NVIDIA que se
# abren con dlopen (libnvv4l2, libtegrav4l2, libnvmm, nvjpeg) con pipelines sueltos de ~1 s.
# Comparte NVENC/NVDEC/VIC con el motor: solo con la salida apagada, comprobado AQUÍ mismo. No prueba
# nvv4l2camerasrc (el HDMI es del motor) ni nvdrmvideosink (la pantalla también): esos se ven al
# reiniciar el motor.
# 🪤 13-oct: NVENC y NVDEC en el MISMO pipeline (enc ! h264parse ! dec) se cuelga y gst-launch -e no
# muere con SIGINT. Por separado (a fichero y desde fichero) va bien: por eso van separados y con
# timeout -k (SIGKILL de respaldo).
grep -q '^OUTPUT=on' /run/eirl-engine/state 2>/dev/null && { echo "se está emitiendo: no se prueba"; exit 2; }
mode=$(PYTHONIOENCODING=utf-8 eirl-enginectl state 2>/dev/null |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["output"]["mode"])' 2>/dev/null)
[ "$mode" = off ] || { echo "salida «$mode» (no off): no se prueba"; exit 2; }
f=$(mktemp --suffix=.h264)
run() { timeout -k 3 15 gst-launch-1.0 -q "$@" >/dev/null 2>&1; }
fail=0
run videotestsrc num-buffers=60 ! 'video/x-raw,width=1280,height=720' ! nvvidconv ! \
    'video/x-raw(memory:NVMM),format=NV12' ! nvv4l2h264enc ! h264parse ! \
    'video/x-h264,stream-format=byte-stream' ! filesink location="$f" && [ -s "$f" ] || { echo "FALLA NVENC"; fail=1; }
run filesrc location="$f" ! h264parse ! nvv4l2decoder ! nvvidconv ! 'video/x-raw,format=I420' ! fakesink ||
    { echo "FALLA NVDEC"; fail=1; }
run videotestsrc num-buffers=5 ! nvvidconv ! 'video/x-raw(memory:NVMM),format=I420' ! nvjpegenc ! nvjpegdec ! fakesink ||
    { echo "FALLA nvjpeg"; fail=1; }
run videotestsrc num-buffers=10 ! nvvidconv ! 'video/x-raw(memory:NVMM),format=RGBA' ! nvcompositor ! nvvidconv ! fakesink ||
    { echo "FALLA nvcompositor"; fail=1; }
rm -f "$f"
[ $fail -eq 0 ] && echo OK-NV
exit $fail
