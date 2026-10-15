package rtmpsend

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts"
	tscodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mpegts/codecs"
)

// streamInfo is what the TS's program says: the video codec and, if there is AAC, its configuration
// (from the ADTS headers).
type streamInfo struct {
	codec string // "h264" or "h265"
	audio *mpeg4audio.AudioSpecificConfig
}

// errNoVideo: the input has no H.264 or H.265 track.
var errNoVideo = errors.New("the input has no H.264 or H.265 video")

// readTS demuxes the MPEG-TS of r into q until it ends; q.finish gets io.EOF at the end of the input.
func readTS(r io.Reader, q *queue) {
	q.finish(demux(r, q.push))
}

func demux(r io.Reader, push func(*frame)) error {
	tr := &mpegts.Reader{R: bufio.NewReaderSize(r, 64<<10)}
	if err := tr.Initialize(); err != nil {
		return err
	}
	tr.OnDecodeError(func(error) {})

	info := &streamInfo{}
	var video, audio *mpegts.Track
	for _, t := range tr.Tracks() {
		switch c := t.Codec.(type) {
		case *tscodecs.H264:
			if video == nil {
				video, info.codec = t, "h264"
			}
		case *tscodecs.H265:
			if video == nil {
				video, info.codec = t, "h265"
			}
		case *tscodecs.MPEG4Audio:
			if audio == nil {
				audio, info.audio = t, &c.Config
			}
		}
	}
	if video == nil {
		return errNoVideo
	}

	// One decoder for every track, so that they share the origin; it unwraps the 33-bit timestamps.
	var td mpegts.TimeDecoder
	var ps [][]byte
	onVideo := func(pts, dts int64, au [][]byte) error {
		dts = td.Decode(dts)
		pts = td.Decode(pts)
		if p := paramSets(info.codec, au); p != nil && !sameSets(p, ps) {
			ps = p
		}
		avcc, err := h264.AVCC(au).Marshal()
		if err != nil {
			return nil // an empty access unit: skip it
		}
		f := &frame{video: true, dts: dts, pts: pts, data: avcc, info: info}
		if ps != nil && isRandomAccess(info.codec, au) {
			f.key, f.ps = true, ps
		}
		push(f)
		return nil
	}
	if info.codec == "h264" {
		tr.OnDataH264(video, onVideo)
	} else {
		tr.OnDataH265(video, onVideo)
	}
	if audio != nil {
		rate := int64(info.audio.SampleRate)
		tr.OnDataMPEG4Audio(audio, func(pts int64, aus [][]byte) error {
			pts = td.Decode(pts)
			for i, au := range aus {
				ts := pts
				if rate > 0 {
					ts += int64(i) * 1024 * 90000 / rate // AAC-LC: 1024 samples per frame
				}
				push(&frame{dts: ts, pts: ts, data: bytes.Clone(au), info: info})
			}
			return nil
		})
	}
	for {
		if err := tr.Read(); err != nil {
			return err
		}
	}
}

// paramSets returns the parameter sets of an access unit (VPS, SPS, PPS for H.265; SPS, PPS for H.264),
// copied, or nil if it does not carry all of them.
func paramSets(codec string, au [][]byte) [][]byte {
	var vps, sps, pps []byte
	for _, n := range au {
		if len(n) == 0 {
			continue
		}
		if codec == "h264" {
			switch h264.NALUType(n[0] & 0x1f) {
			case h264.NALUTypeSPS:
				sps = n
			case h264.NALUTypePPS:
				pps = n
			}
			continue
		}
		switch h265.NALUType((n[0] >> 1) & 0x3f) {
		case h265.NALUType_VPS_NUT:
			vps = n
		case h265.NALUType_SPS_NUT:
			sps = n
		case h265.NALUType_PPS_NUT:
			pps = n
		}
	}
	switch {
	case sps == nil || pps == nil:
		return nil
	case codec == "h264":
		return [][]byte{bytes.Clone(sps), bytes.Clone(pps)}
	case vps == nil:
		return nil
	}
	return [][]byte{bytes.Clone(vps), bytes.Clone(sps), bytes.Clone(pps)}
}

func sameSets(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func isRandomAccess(codec string, au [][]byte) bool {
	if codec == "h264" {
		return h264.IsRandomAccess(au)
	}
	return h265.IsRandomAccess(au)
}
