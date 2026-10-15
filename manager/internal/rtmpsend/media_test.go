package rtmpsend

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts"
	tscodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts/codecs"
)

// Parameter sets of real streams: H.264 baseline 1920x1080, H.265 Main 960x540.
var (
	h264SPS = []byte{0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02, 0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04,
		0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9, 0x20}
	h264PPS = []byte{0x68, 0xce, 0x3c, 0x80}
	h265VPS = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x02, 0x20, 0x00, 0x00, 0x03, 0x00, 0xb0, 0x00, 0x00, 0x03,
		0x00, 0x00, 0x03, 0x00, 0x7b, 0x18, 0xb0, 0x24}
	h265SPS = []byte{0x42, 0x01, 0x01, 0x02, 0x20, 0x00, 0x00, 0x03, 0x00, 0xb0, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03,
		0x00, 0x7b, 0xa0, 0x07, 0x82, 0x00, 0x88, 0x7d, 0xb6, 0x71, 0x8b, 0x92, 0x44, 0x80, 0x53, 0x88, 0x88, 0x92,
		0xcf, 0x24, 0xa6, 0x92, 0x72, 0xc9, 0x12, 0x49, 0x22, 0xdc, 0x91, 0xaa, 0x48, 0xfc, 0xa2, 0x23, 0xff, 0x00,
		0x01, 0x00, 0x01, 0x6a, 0x02, 0x02, 0x02, 0x01}
	h265PPS = []byte{0x44, 0x01, 0xc0, 0x25, 0x2f, 0x05, 0x32, 0x40}

	h264IDR   = []byte{0x65, 0x88, 0x84, 0x00}
	h264Slice = []byte{0x41, 0x9a, 0x02}
	h265IDR   = []byte{0x26, 0x01, 0xaf, 0x00} // IDR_W_RADL
	h265Slice = []byte{0x02, 0x01, 0xd0, 0x00} // TRAIL_R
	aacFrame  = []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}
)

// tsOf writes a TS with a video track (codec "h264" or "h265") and AAC: two GOPs of three frames, the
// timestamps starting 1 s before the 33-bit wrap.
func tsOf(t *testing.T, codec string) []byte {
	t.Helper()
	var vc tscodecs.Codec = &tscodecs.H264{}
	idr, slice, ps := h264IDR, h264Slice, [][]byte{h264SPS, h264PPS}
	if codec == "h265" {
		vc, idr, slice, ps = &tscodecs.H265{}, h265IDR, h265Slice, [][]byte{h265VPS, h265SPS, h265PPS}
	}
	video := &mpegts.Track{Codec: vc}
	audio := &mpegts.Track{Codec: &tscodecs.MPEG4Audio{Config: mpeg4audio.AudioSpecificConfig{
		Type: mpeg4audio.ObjectTypeAACLC, SampleRate: 48000, ChannelConfig: 2, ChannelCount: 2}}}
	var buf bytes.Buffer
	w := &mpegts.Writer{W: &buf, Tracks: []*mpegts.Track{video, audio}}
	if err := w.Initialize(); err != nil {
		t.Fatal(err)
	}
	start := int64(1<<33 - 90000)
	for i := range 6 {
		ts := (start + int64(i)*3000) & (1<<33 - 1) // 30 fps
		au := [][]byte{slice}
		if i%3 == 0 {
			au = append(append([][]byte{}, ps...), idr)
		}
		var err error
		if codec == "h265" {
			err = w.WriteH265(video, ts, ts, au)
		} else {
			err = w.WriteH264(video, ts, ts, au)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := w.WriteMPEG4Audio(audio, ts, [][]byte{aacFrame, aacFrame}); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestDemuxAndMux(t *testing.T) {
	for _, codec := range []string{"h264", "h265"} {
		var frames []*frame
		err := demux(bytes.NewReader(tsOf(t, codec)), func(f *frame) { frames = append(frames, f) })
		if err == nil || err.Error() != "EOF" {
			t.Fatalf("%s: end %v", codec, err)
		}
		var video, keys, audio int
		var last int64 = -1 << 62
		for _, f := range frames {
			if f.info.codec != codec || f.info.audio == nil || f.info.audio.SampleRate != 48000 {
				t.Fatalf("%s: info %+v", codec, f.info)
			}
			if f.video {
				video++
				if f.dts <= last {
					t.Errorf("%s: video dts not increasing across the wrap: %d after %d", codec, f.dts, last)
				}
				last = f.dts
				if f.key {
					keys++
					if want := map[string]int{"h264": 2, "h265": 3}[codec]; len(f.ps) != want {
						t.Errorf("%s: keyframe with %d parameter sets", codec, len(f.ps))
					}
				}
			} else {
				audio++
			}
		}
		// The writer flushes on the next PES of the track: the last video access unit stays in the muxer.
		if video < 5 || keys != 2 || audio < 10 {
			t.Fatalf("%s: video %d keys %d audio %d", codec, video, keys, audio)
		}

		// The muxer: metadata, video sequence header, AAC config, then the keyframe, from timestamp 0.
		var m muxer
		var first *frame
		for _, f := range frames {
			if f.video && f.key {
				first = f
				break
			}
		}
		msgs, err := m.messages(first)
		if err != nil || len(msgs) != 4 {
			t.Fatalf("%s: %d messages, %v", codec, len(msgs), err)
		}
		types := []message.Type{message.TypeDataAMF0, message.TypeVideo, message.TypeAudio, message.TypeVideo}
		for i, msg := range msgs {
			if message.Type(msg.Type) != types[i] || msg.Timestamp != 0 || msg.MessageStreamID != msgStreamID {
				t.Errorf("%s: message %d: type %d ts %v", codec, i, msg.Type, msg.Timestamp)
			}
		}
		meta, err := amf0.Unmarshal(msgs[0].Body)
		if err != nil || len(meta) != 3 || meta[1] != "onMetaData" {
			t.Fatalf("%s: metadata %v %v", codec, meta, err)
		}
		if w, _ := meta[2].(amf0.Object).GetFloat64("width"); w != map[string]float64{"h264": 1920, "h265": 960}[codec] {
			t.Errorf("%s: width %v", codec, w)
		}
		seq := msgs[1].Body
		if codec == "h264" {
			var c mp4.AVCDecoderConfiguration
			c.SetType(mp4.BoxTypeAvcC())
			if seq[0] != 0x17 || seq[1] != 0 {
				t.Errorf("h264 sequence header % x", seq[:5])
			}
			if _, err := mp4.Unmarshal(bytes.NewReader(seq[5:]), uint64(len(seq)-5), &c, mp4.Context{}); err != nil ||
				!bytes.Equal(c.SequenceParameterSets[0].NALUnit, h264SPS) {
				t.Errorf("avcC: %v", err)
			}
		} else {
			var c mp4.HvcC
			if !bytes.Equal(seq[:5], []byte{0x90, 'h', 'v', 'c', '1'}) {
				t.Errorf("h265 sequence header % x", seq[:5])
			}
			if _, err := mp4.Unmarshal(bytes.NewReader(seq[5:]), uint64(len(seq)-5), &c, mp4.Context{}); err != nil ||
				len(c.NaluArrays) != 3 || !bytes.Equal(c.NaluArrays[1].Nalus[0].NALUnit, h265SPS) {
				t.Errorf("hvcC: %v", err)
			}
		}
		if a := msgs[2].Body; a[0] != 0xaf || a[1] != 0 {
			t.Errorf("%s: aac config % x", codec, a)
		}

		// The next frames: no repeated headers while the parameter sets do not change; later timestamps.
		for _, f := range frames[1:] {
			if f.dts < first.dts {
				continue
			}
			msgs, err := m.messages(f)
			if err != nil || len(msgs) != 1 {
				t.Fatalf("%s: %d messages for a later frame, %v", codec, len(msgs), err)
			}
			if want := (f.dts - first.dts) / 90; msgs[0].Timestamp.Milliseconds() != want {
				t.Errorf("%s: ts %v, want %d ms", codec, msgs[0].Timestamp, want)
			}
		}
	}
}

func TestVideoFrameHeader(t *testing.T) {
	cases := []struct {
		codec string
		key   bool
		cts   int64
		want  []byte
	}{
		{"h264", true, 0, []byte{0x17, 1, 0, 0, 0}},
		{"h264", false, 33, []byte{0x27, 1, 0, 0, 33}},
		{"h265", true, 0, []byte{0x93, 'h', 'v', 'c', '1'}},                     // FramesX, keyframe
		{"h265", false, 0, []byte{0xa3, 'h', 'v', 'c', '1'}},                    // FramesX, inter
		{"h265", false, -1, []byte{0xa1, 'h', 'v', 'c', '1', 0xff, 0xff, 0xff}}, // CodedFrames, negative cts
	}
	for _, c := range cases {
		got := videoFrame(c.codec, c.key, c.cts, []byte{0xee})
		if !bytes.Equal(got, append(c.want, 0xee)) {
			t.Errorf("%s key=%v cts=%d: % x", c.codec, c.key, c.cts, got)
		}
	}
}
