#!/bin/bash
# After each B3 group: does the engine still have everything it uses? Read-only. The list of elements
# comes from the mixer work (13 Oct 2026): checking the loaded libraries is not enough, because GStreamer
# loads each plugin when the element is created (MJPEG, network camera… may not be loaded right now).
# Run it as the engine's user (User= of eirl-engine.service: same GStreamer registry). Exits 1 if anything
# is missing.
# As the engine's user, the same registry as the service (CacheDirectory= of eirl-engine.service)
[ "$(id -un)" = eirl-engine ] && export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/var/cache/eirl-engine}
fail=0
for e in nvvidconv nvv4l2h264enc nvv4l2decoder nvv4l2camerasrc nvcompositor nvjpegdec nvjpegenc \
    nvdrmvideosink v4l2src alsasrc appsrc appsink compositor audiomixer audioconvert audioresample \
    level volume valve tee queue videorate videoconvert videotestsrc audiotestsrc textoverlay jpegenc \
    jpegparse multifilesink udpsink rtph264pay rtpopuspay opusenc opusdec rtspsrc rtph264depay \
    rtph265depay h264parse h265parse aacparse mpegtsmux tsdemux voaacenc avenc_aac avdec_aac identity \
    fakesink filesink; do
    gst-inspect-1.0 "$e" >/dev/null 2>&1 || { echo "MISSING element $e"; fail=1; }
done
# srtsink does not exist in GStreamer 1.14: the SRT output is srt-live-transmit (not a failure).
python3 -c "import gi, cairo; gi.require_version('Gst','1.0'); from gi.repository import Gst, GLib" 2>/dev/null ||
    { echo "MISSING python3 gi/cairo/Gst"; fail=1; }
for c in v4l2-ctl amixer xxd srt-live-transmit srtla_send i2ctransfer iw ip iptables ethtool nmcli dnsmasq \
    mediamtx ffmpeg gst-launch-1.0 gst-inspect-1.0; do
    p=$(command -v "$c") || { echo "MISSING $c"; fail=1; continue; }
    # 🪤 «--version» is no good: v4l2-ctl 1.14 does not have it and amixer --version exits with 1. What
    # matters is that no library is missing.
    ldd "$p" 2>/dev/null | grep -q 'not found' && { echo "MISSING library for $c"; fail=1; }
done
# Dynamic linking of NVIDIA's plugins and libraries (does not cover what is opened with dlopen).
for f in /usr/lib/aarch64-linux-gnu/gstreamer-1.0/libgstnv*.so /usr/lib/aarch64-linux-gnu/tegra/*.so \
    /usr/local/bin/srt-live-transmit /usr/local/bin/srtla_send; do
    ldd "$f" 2>/dev/null | grep -q 'not found' && { echo "MISSING library for $f: $(ldd "$f" | grep 'not found' | tr -s ' ' | tr '\n' ';')"; fail=1; }
done
fc-match -f '%{file}\n' Sans | grep -q DejaVuSans || { echo "MISSING DejaVu Sans (fc-match Sans → $(fc-match Sans))"; fail=1; }
fc-match -f '%{file}\n' Monospace | grep -qi dejavu || echo "warning: Monospace → $(fc-match Monospace)"
[ $fail -eq 0 ] && echo "OK: the engine has everything it uses"
exit $fail
