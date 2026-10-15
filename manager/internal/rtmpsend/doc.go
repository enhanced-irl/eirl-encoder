// Package rtmpsend publishes the encoder's MPEG-TS (H.264 or H.265 video, AAC audio) to an RTMP or RTMPS
// server: the sender of the rtmp and rtmps outputs (OUTPUTS.md §4-5), run by the engine's output session as
// `eirl-send rtmp`.
//
// The TS is demuxed (mediacommon) into a queue; the writer sends it once the server has accepted the
// publish (gortmplib does the handshake, connect and publish), starting at a keyframe with its parameter
// sets. H.264 goes as classic FLV video, H.265 as Enhanced RTMP (hvc1), with keyframes marked. When the
// media waiting goes over the queue's maximum, it drops up to the next keyframe. The connection's
// statistics come out in srt-live-transmit's JSON shape, so the engine's ABR reads every sender alike.
//
// # eirl-send
//
//	eirl-send rtmp -url rtmp[s]://host[:port]/app [-bind <ipv4>] [-stats-fd 3] [-stats-ms 250]
//	               [-max-queue-ms 3000] [-connect-timeout 10s] [-ca-file <pem>]
//	env EIRL_SEND_KEY   the stream key (may be empty: then the url's last element is the stream)
//	stdin               MPEG-TS
//
// stderr, one event per line (values with spaces quoted with %q; the key is never printed):
//
//	event=connecting host=… port=… tls=true|false bind=…   before resolving
//	event=connected                                       the server accepted connect and publish
//	event=publishing codec=h264|h265                      the first keyframe went out
//	event=dropped frames=<n> queue_ms=<n>                 frames dropped since the last one (≤ 1 per 10 s)
//	event=failed reason=<reason> detail="…"               then exit 1
//	event=eof                                             stdin ended (what was queued got 2 s); exit 0
//
// Reasons: relay_unreachable (DNS, connect, timeout), rejected (connect or publish refused, or the server
// closed within 5 s of accepting it), tls_failed, clock_not_synced (the certificate is not valid yet),
// connection_lost, internal (bad arguments or input). SIGTERM or SIGINT: exit 0 without an event.
//
// Stats on -stats-fd, every -stats-ms once connected, counters of the last interval:
//
//	{"link":{"rtt":<ms>},"send":{"bytes":<n>,"msBuf":<ms>,"packetsDropped":<frames>,"mbitRate":<Mbps>}}
//
// rtt and the kernel's unsent bytes come from TCP_INFO and SIOCOUTQNSD (Linux; 0 elsewhere); bytes are
// those the server acknowledged (Linux), else those written; msBuf is the media in the queue plus the
// unsent bytes at the recent send rate.
package rtmpsend
