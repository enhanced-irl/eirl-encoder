package rtmpsend

import (
	"bytes"
	"errors"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/bluenviron/gortmplib/pkg/rawmessage"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
)

// The media goes as raw RTMP messages (FLV tag bodies) and not through the library's Writer: its Enhanced
// RTMP frames carry no frame type, and H.265 needs keyframes marked as such (ingests that use FFmpeg find
// them by it). Chunk streams and the message stream are the library's own.
const (
	msgStreamID = 0x1000000 // stream 1, as the library encodes it
	dataCSID    = message.AudioChunkStreamID
)

// FLV / Enhanced RTMP video tag header.
const (
	flvKey      = 1 << 4
	flvInter    = 2 << 4
	flvAVC      = 7
	flvExHeader = 0x80
	exSeqStart  = 0
	exCoded     = 1
	exFramesX   = 3
	aacHeader   = 0xaf // AAC, 44 kHz (always 3 for AAC), 16 bit, stereo
)

var fourCCHEVC = []byte("hvc1")

// muxer turns frames into RTMP messages: on the first one, the metadata and the sequence headers; again
// the video one if the parameter sets change. Timestamps start at 0 at the first keyframe.
type muxer struct {
	started bool
	base    int64 // dts of the first frame, 90 kHz
	ps      [][]byte
}

// messages is what to send for f (none for audio older than the first keyframe).
func (m *muxer) messages(f *frame) ([]*rawmessage.Message, error) {
	if !m.started {
		if !f.video || !f.key {
			return nil, nil // the queue gives a keyframe first; just in case
		}
		m.started, m.base = true, f.dts
	}
	if f.dts < m.base {
		return nil, nil
	}
	ts := ticksToDuration(f.dts - m.base)
	var out []*rawmessage.Message
	if f.video && f.key && !sameSets(f.ps, m.ps) {
		if m.ps == nil {
			meta, err := metadata(f)
			if err != nil {
				return nil, err
			}
			out = append(out, &rawmessage.Message{ChunkStreamID: dataCSID, Timestamp: ts,
				Type: uint8(message.TypeDataAMF0), MessageStreamID: msgStreamID, Body: meta})
		}
		seq, err := videoSequenceHeader(f.info.codec, f.ps)
		if err != nil {
			return nil, err
		}
		out = append(out, videoMessage(ts, seq))
		if m.ps == nil && f.info.audio != nil {
			asc, err := f.info.audio.Marshal()
			if err != nil {
				return nil, err
			}
			out = append(out, audioMessage(ts, append([]byte{aacHeader, 0}, asc...)))
		}
		m.ps = f.ps
	}
	if !f.video {
		return append(out, audioMessage(ts, append([]byte{aacHeader, 1}, f.data...))), nil
	}
	cts := (f.pts - f.dts) / 90 // ms
	return append(out, videoMessage(ts, videoFrame(f.info.codec, f.key, cts, f.data))), nil
}

func ticksToDuration(t int64) time.Duration {
	return time.Duration(t) * time.Second / 90000
}

func videoMessage(ts time.Duration, body []byte) *rawmessage.Message {
	return &rawmessage.Message{ChunkStreamID: message.VideoChunkStreamID, Timestamp: ts,
		Type: uint8(message.TypeVideo), MessageStreamID: msgStreamID, Body: body}
}

func audioMessage(ts time.Duration, body []byte) *rawmessage.Message {
	return &rawmessage.Message{ChunkStreamID: message.AudioChunkStreamID, Timestamp: ts,
		Type: uint8(message.TypeAudio), MessageStreamID: msgStreamID, Body: body}
}

// videoFrame is the tag body of an access unit: legacy AVC for H.264, Enhanced RTMP (hvc1) for H.265.
func videoFrame(codec string, key bool, ctsMs int64, avcc []byte) []byte {
	ft := byte(flvInter)
	if key {
		ft = flvKey
	}
	cts := []byte{byte(ctsMs >> 16), byte(ctsMs >> 8), byte(ctsMs)}
	var b []byte
	switch {
	case codec == "h264":
		b = append([]byte{ft | flvAVC, 1}, cts...)
	case ctsMs == 0:
		b = append([]byte{flvExHeader | ft | exFramesX}, fourCCHEVC...)
	default:
		b = append(append([]byte{flvExHeader | ft | exCoded}, fourCCHEVC...), cts...)
	}
	return append(b, avcc...)
}

// videoSequenceHeader is the tag body with the decoder configuration (avcC or hvcC).
func videoSequenceHeader(codec string, ps [][]byte) ([]byte, error) {
	var conf mp4.IImmutableBox
	var err error
	var b []byte
	if codec == "h264" {
		conf, err = avcC(ps[0], ps[1])
		b = []byte{flvKey | flvAVC, 0, 0, 0, 0}
	} else {
		conf, err = hvcC(ps[0], ps[1], ps[2])
		b = append([]byte{flvExHeader | flvKey | exSeqStart}, fourCCHEVC...)
	}
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write(b)
	if _, err := mp4.Marshal(&buf, conf, mp4.Context{}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func avcC(sps, pps []byte) (*mp4.AVCDecoderConfiguration, error) {
	var s h264.SPS
	if err := s.Unmarshal(sps); err != nil {
		return nil, err
	}
	if len(sps) < 4 {
		return nil, errors.New("short SPS")
	}
	c := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
		ConfigurationVersion:       1,
		Profile:                    sps[1],
		ProfileCompatibility:       sps[2],
		Level:                      sps[3],
		Reserved:                   0b111111,
		LengthSizeMinusOne:         3,
		Reserved2:                  0b111,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets:      []mp4.AVCParameterSet{{Length: uint16(len(sps)), NALUnit: sps}},
		NumOfPictureParameterSets:  1,
		PictureParameterSets:       []mp4.AVCParameterSet{{Length: uint16(len(pps)), NALUnit: pps}},
	}
	switch sps[1] {
	case 100, 110, 122, 144: // the High profiles carry their chroma format and bit depths (ISO 14496-15)
		c.HighProfileFieldsEnabled = true
		c.Reserved3, c.Reserved4, c.Reserved5 = 0b111111, 0b11111, 0b11111
		c.ChromaFormat = uint8(s.ChromaFormatIdc)
		c.BitDepthLumaMinus8 = uint8(s.BitDepthLumaMinus8)
		c.BitDepthChromaMinus8 = uint8(s.BitDepthChromaMinus8)
	}
	return c, nil
}

func hvcC(vps, sps, pps []byte) (*mp4.HvcC, error) {
	var s h265.SPS
	if err := s.Unmarshal(sps); err != nil {
		return nil, err
	}
	if len(sps) < 13 {
		return nil, errors.New("short SPS")
	}
	ptl := s.ProfileTierLevel
	var nested uint8
	if s.TemporalIDNestingFlag {
		nested = 1
	}
	array := func(t h265.NALUType, n []byte) mp4.HEVCNaluArray {
		return mp4.HEVCNaluArray{NaluType: byte(t), NumNalus: 1,
			Nalus: []mp4.HEVCNalu{{Length: uint16(len(n)), NALUnit: n}}}
	}
	return &mp4.HvcC{
		ConfigurationVersion:        1,
		GeneralProfileSpace:         ptl.GeneralProfileSpace,
		GeneralTierFlag:             ptl.GeneralTierFlag != 0,
		GeneralProfileIdc:           ptl.GeneralProfileIdc,
		GeneralProfileCompatibility: ptl.GeneralProfileCompatibilityFlag,
		// the 48 bits after the profile compatibility flags (2-byte NAL header + 1 + 4 bytes before)
		GeneralConstraintIndicator: [6]uint8{sps[7], sps[8], sps[9], sps[10], sps[11], sps[12]},
		GeneralLevelIdc:            ptl.GeneralLevelIdc,
		Reserved1:                  0b1111,
		Reserved2:                  0b111111,
		Reserved3:                  0b111111,
		ChromaFormatIdc:            uint8(s.ChromaFormatIdc),
		Reserved4:                  0b11111,
		BitDepthLumaMinus8:         uint8(s.BitDepthLumaMinus8),
		Reserved5:                  0b11111,
		BitDepthChromaMinus8:       uint8(s.BitDepthChromaMinus8),
		NumTemporalLayers:          s.MaxSubLayersMinus1 + 1,
		TemporalIdNested:           nested,
		LengthSizeMinusOne:         3,
		NumOfNaluArrays:            3,
		NaluArrays: []mp4.HEVCNaluArray{array(h265.NALUType_VPS_NUT, vps), array(h265.NALUType_SPS_NUT, sps),
			array(h265.NALUType_PPS_NUT, pps)},
	}, nil
}

// metadata is the `@setDataFrame onMetaData` of the stream: size and frame rate from the SPS, the codecs
// (the hvc1 FourCC as videocodecid, as Enhanced RTMP says) and the audio's format.
func metadata(f *frame) ([]byte, error) {
	var w, h int
	var fps float64
	var codecID float64
	if f.info.codec == "h264" {
		var s h264.SPS
		if err := s.Unmarshal(f.ps[0]); err == nil {
			w, h, fps = s.Width(), s.Height(), s.FPS()
		}
		codecID = flvAVC
	} else {
		var s h265.SPS
		if err := s.Unmarshal(f.ps[1]); err == nil {
			w, h, fps = s.Width(), s.Height(), s.FPS()
		}
		codecID = float64(message.FourCCHEVC)
	}
	obj := amf0.Object{
		{Key: "width", Value: float64(w)},
		{Key: "height", Value: float64(h)},
		{Key: "videocodecid", Value: codecID},
	}
	if fps > 0 {
		obj = append(obj, amf0.ObjectEntry{Key: "framerate", Value: fps})
	}
	if a := f.info.audio; a != nil {
		obj = append(obj,
			amf0.ObjectEntry{Key: "audiocodecid", Value: float64(10)},
			amf0.ObjectEntry{Key: "audiosamplerate", Value: float64(a.SampleRate)},
			amf0.ObjectEntry{Key: "audiosamplesize", Value: float64(16)},
			amf0.ObjectEntry{Key: "stereo", Value: a.ChannelConfig >= 2})
	}
	obj = append(obj, amf0.ObjectEntry{Key: "encoder", Value: "eirl-send"})
	return amf0.Data{"@setDataFrame", "onMetaData", obj}.Marshal()
}
