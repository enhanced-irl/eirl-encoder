package record

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
)

// What mp4mux sends in fragmented mode (fragment-duration, streamable; measured on the board, RECORDING.md
// §3): ftyp and moov, then per track and per second a moof and its mdat, and at the end an mfra and the moov
// again (with the durations, to go over the first one in place: the file is not seekable here). The
// track fragments carry no absolute offsets (tfhd without base-data-offset: their data is found from their
// own moof), so a whole moof+mdat pair can be dropped, or a new file started at one, without touching
// anything else. Nearly every video fragment starts with a keyframe: when the encoder's falls a frame or two
// after the muxer's second, the muxer first closes a fragment of just those frames (19 in the 4-hour run,
// RECORDING.md §8.2). A resume or a split waits for one that does (fragmentKey).

// box is one top-level box, header included.
type box struct {
	typ  string
	data []byte
}

// errBadBox is a box the splitter cannot follow (a size it cannot be): the stream is not what mp4mux sends.
var errBadBox = errors.New("mp4: bad box size")

// splitter turns what the muxer sends, in buffers of any size, into whole top-level boxes.
type splitter struct{ buf []byte }

// push adds bytes and returns the boxes they completed.
func (s *splitter) push(b []byte) ([]box, error) {
	s.buf = append(s.buf, b...)
	var out []box
	for {
		size, hdr, ok := boxSize(s.buf)
		if !ok {
			break
		}
		if size < uint64(hdr) || size > maxBox {
			return out, errBadBox
		}
		if uint64(len(s.buf)) < size {
			break
		}
		n := int(size)
		if n == len(s.buf) {
			// The usual case: the muxer pushes whole boxes, and this one is the whole buffer
			out = append(out, box{typ: string(s.buf[4:8]), data: s.buf})
			s.buf = nil
			break
		}
		out = append(out, box{typ: string(s.buf[4:8]), data: append([]byte(nil), s.buf[:n]...)})
		s.buf = s.buf[n:]
	}
	return out, nil
}

// maxBox bounds one box: a fragment of 1 s at the highest quality is ~5 MB.
const maxBox = 256 << 20

// boxSize reads a box header: its size, the header's length and whether the header is all there. A size of
// 0 ("to the end of the file") is not something a stream can have: it reads as a bad size.
func boxSize(b []byte) (size uint64, hdr int, ok bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	size = uint64(binary.BigEndian.Uint32(b))
	if size == 1 {
		if len(b) < 16 {
			return 0, 0, false
		}
		return binary.BigEndian.Uint64(b[8:16]), 16, true
	}
	return size, 8, true
}

// children walks the boxes inside a box's payload (from off to end), calling fn with each one's type and
// bounds; fn returns false to stop.
func children(b []byte, off, end int, fn func(typ string, start, stop int) bool) {
	for off+8 <= end {
		size, hdr, ok := boxSize(b[off:end])
		if !ok || size < uint64(hdr) || uint64(end-off) < size {
			return
		}
		if !fn(string(b[off+4:off+8]), off, off+int(size)) {
			return
		}
		off += int(size)
	}
}

// videoTrack is the track id of the video track in a moov (its trak's hdlr is "vide"); 0 if none.
func videoTrack(moov []byte) uint32 {
	var id uint32
	children(moov, 8, len(moov), func(typ string, s, e int) bool {
		if typ != "trak" {
			return true
		}
		var tid uint32
		video := false
		children(moov, s+8, e, func(typ string, s2, e2 int) bool {
			switch typ {
			case "tkhd":
				// version 0: 4 bytes of flags, creation and modification (4 each), then the track id
				if e2-s2 >= 24 {
					v, at := moov[s2+8], s2+20
					if v == 1 {
						at = s2 + 28
					}
					if at+4 <= e2 {
						tid = binary.BigEndian.Uint32(moov[at:])
					}
				}
			case "mdia":
				children(moov, s2+8, e2, func(typ string, s3, e3 int) bool {
					if typ == "hdlr" && e3-s3 >= 20 && string(moov[s3+16:s3+20]) == "vide" {
						video = true
					}
					return true
				})
			}
			return true
		})
		if video {
			id = tid
			return false
		}
		return true
	})
	return id
}

// fragmentTrack is the track id of a moof's first track fragment (tfhd), 0 if it has none.
func fragmentTrack(moof []byte) uint32 {
	var id uint32
	children(moof, 8, len(moof), func(typ string, s, e int) bool {
		if typ != "traf" {
			return true
		}
		children(moof, s+8, e, func(typ string, s2, e2 int) bool {
			if typ == "tfhd" && e2-s2 >= 16 {
				id = binary.BigEndian.Uint32(moof[s2+12:])
				return false
			}
			return true
		})
		return false
	})
	return id
}

// fragmentKey: whether a moof's first track fragment starts with a sync sample (a keyframe). The first
// sample's flags are the trun's (first-sample or per-sample flags), else the tfhd's default; with none of
// them (the moov's trex then, which mp4mux never leaves it to) it counts as one.
func fragmentKey(moof []byte) bool {
	key := true
	children(moof, 8, len(moof), func(typ string, s, e int) bool {
		if typ != "traf" {
			return true
		}
		var flags uint32
		known := false
		children(moof, s+8, e, func(typ string, s2, e2 int) bool {
			if e2-s2 < 16 {
				return true
			}
			tf := binary.BigEndian.Uint32(moof[s2+8:]) & 0xffffff
			at := s2 + 16
			switch typ {
			case "tfhd": // after the track id: base offset, description, duration, size, flags
				for _, f := range [...]struct {
					bit uint32
					n   int
				}{{0x01, 8}, {0x02, 4}, {0x08, 4}, {0x10, 4}} {
					if tf&f.bit != 0 {
						at += f.n
					}
				}
				if tf&0x20 != 0 && at+4 <= e2 {
					flags, known = binary.BigEndian.Uint32(moof[at:]), true
				}
			case "trun": // after the count: data offset, first-sample flags, then per sample duration, size, flags
				if tf&0x01 != 0 {
					at += 4
				}
				if tf&0x04 == 0 && tf&0x400 != 0 {
					for _, bit := range [...]uint32{0x100, 0x200} {
						if tf&bit != 0 {
							at += 4
						}
					}
				}
				if tf&0x404 != 0 && at+4 <= e2 {
					flags, known = binary.BigEndian.Uint32(moof[at:]), true
				}
				return false
			}
			return true
		})
		key = !known || flags&0x10000 == 0 // sample_is_non_sync_sample
		return false
	})
	return key
}

// completeEnd is where the last complete fragment of a recording ends: after its header and every moof
// followed by its whole mdat. A recording left open by a power cut is cut there (RECORDING.md §5.4):
// QuickTime opens nothing after a fragment cut in half. It reads only the boxes' headers.
func completeEnd(f io.ReaderAt, size int64) (int64, error) {
	var off, end int64
	prev := ""
	hdr := make([]byte, 16)
	for off+8 <= size {
		n, err := f.ReadAt(hdr, off)
		if n < 8 {
			if err != nil && !errors.Is(err, io.EOF) {
				return end, err
			}
			break
		}
		bs, h, ok := boxSize(hdr[:n])
		if !ok || bs < uint64(h) || off+int64(bs) > size {
			break
		}
		typ := string(hdr[4:8])
		switch {
		case typ == "ftyp" || typ == "moov":
			end = off + int64(bs)
		case typ == "mdat" && prev == "moof":
			end = off + int64(bs)
		}
		prev = typ
		off += int64(bs)
	}
	return end, nil
}

// trimFile cuts a recording at its last complete fragment; it returns how many bytes went.
func trimFile(path string) (int64, error) {
	// Never through a link, nor waiting on a FIFO: the name comes from the marker, the file from a disk that may have
	// been swapped for another with the same uuid
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !st.Mode().IsRegular() {
		return 0, errors.New("not a regular file")
	}
	end, err := completeEnd(f, st.Size())
	if err != nil {
		return 0, err
	}
	if end == st.Size() {
		return 0, nil
	}
	if err := f.Truncate(end); err != nil {
		return 0, err
	}
	return st.Size() - end, f.Sync()
}
