package rzx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"os"
)

// WriteOptions tunes how Write serializes the file. The zero value is
// valid: no compression, no signing, no creator block injected.
type WriteOptions struct {
	// Compress controls whether snapshot bodies and IRB frame
	// streams are zlib-compressed. libspectrum's "rzx_compression"
	// setting maps to this. Compression is best-effort: if zlib
	// produces a stream LARGER than the input (rare but possible
	// for tiny inputs), the writer falls back to uncompressed for
	// that block, matching libspectrum's rzx_compress fallback at
	// rzx.c:1391.
	Compress bool

	// Signed controls the version-byte and header-flags fields. The
	// caller is responsible for actually adding sign-start /
	// sign-end blocks; this flag just toggles the header bookkeeping
	// so a verifier knows to look for them.
	Signed bool
}

// WriteFile serialises the file with the supplied options and writes
// it to disk in one shot. Wrapper around Write + os.WriteFile so the
// caller doesn't have to manage the buffer round-trip.
func (f *File) WriteFile(path string, opts WriteOptions) error {
	data, err := f.Write(opts)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Write serializes the file to a fresh byte slice. The output starts
// with the 10-byte RZX header and contains every block in f.Blocks in
// order. The function does not mutate the receiver.
func (f *File) Write(opts WriteOptions) ([]byte, error) {
	var buf bytes.Buffer
	writeHeader(&buf, opts.Signed)

	for i, block := range f.Blocks {
		var err error
		switch block.Type {
		case BlockCreator:
			if block.Creator == nil {
				return nil, fmt.Errorf("rzx: block %d: creator block has nil payload", i)
			}
			err = writeCreator(&buf, block.Creator)
		case BlockSnapshot:
			if block.Snapshot == nil {
				return nil, fmt.Errorf("rzx: block %d: snapshot block has nil payload", i)
			}
			err = writeSnapshot(&buf, block.Snapshot, opts.Compress)
		case BlockInput:
			if block.Input == nil {
				return nil, fmt.Errorf("rzx: block %d: input block has nil payload", i)
			}
			err = writeInput(&buf, block.Input, opts.Compress)
		case BlockSignStart:
			if block.SignStart == nil {
				return nil, fmt.Errorf("rzx: block %d: sign-start block has nil payload", i)
			}
			err = writeSignStart(&buf, block.SignStart)
		case BlockSignEnd:
			if block.SignEnd == nil {
				return nil, fmt.Errorf("rzx: block %d: sign-end block has nil payload", i)
			}
			err = writeSignEnd(&buf, block.SignEnd)
		default:
			return nil, fmt.Errorf("rzx: block %d: unknown type 0x%02x", i, block.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("rzx: block %d (0x%02x): %w", i, block.Type, err)
		}
	}

	return buf.Bytes(), nil
}

// writeHeader emits the 10-byte file header. The minor version field
// flips between 12 (unsigned) and 13 (signed) to match libspectrum's
// convention at rzx.c:1329.
func writeHeader(buf *bytes.Buffer, signed bool) {
	buf.Write(magic)
	buf.WriteByte(versionMajor)
	if signed {
		buf.WriteByte(versionMinorSign)
		_ = binary.Write(buf, binary.LittleEndian, headerFlagSigned)
	} else {
		buf.WriteByte(versionMinorPlay)
		_ = binary.Write(buf, binary.LittleEndian, uint32(0))
	}
}

// writeBlock emits a generic block: the 1-byte ID, the 4-byte
// inclusive-of-header length, then the payload. blockLen MUST equal
// 1 (ID) + 4 (length) + len(payload) — the helper computes that for
// callers so they can't get it wrong.
func writeBlock(buf *bytes.Buffer, id BlockType, payload []byte) {
	buf.WriteByte(byte(id))
	_ = binary.Write(buf, binary.LittleEndian, uint32(5+len(payload)))
	buf.Write(payload)
}

func writeCreator(buf *bytes.Buffer, c *CreatorBlock) error {
	var p bytes.Buffer
	prog := []byte(c.Program)
	if len(prog) > 20 {
		prog = prog[:20]
	}
	p.Write(prog)
	for i := len(prog); i < 20; i++ {
		p.WriteByte(0)
	}
	_ = binary.Write(&p, binary.LittleEndian, c.Major)
	_ = binary.Write(&p, binary.LittleEndian, c.Minor)
	p.Write(c.Custom)
	writeBlock(buf, BlockCreator, p.Bytes())
	return nil
}

// writeSnapshot emits a 0x30 snapshot block. The body is optionally
// zlib-compressed; the writer falls back to uncompressed if zlib would
// produce a larger stream (matching libspectrum's rzx_compress at
// rzx.c:1391).
func writeSnapshot(buf *bytes.Buffer, s *SnapshotBlock, compress bool) error {
	if len(s.Format) == 0 {
		return fmt.Errorf("snapshot: empty format identifier")
	}

	body := s.Data
	flags := uint32(0)
	if compress && len(body) > 0 {
		compressed, ok := zlibCompress(body)
		if ok {
			body = compressed
			flags |= snapFlagCompressed
		}
	}

	var p bytes.Buffer
	_ = binary.Write(&p, binary.LittleEndian, flags)
	writeFixedString(&p, s.Format, 4)
	_ = binary.Write(&p, binary.LittleEndian, uint32(len(s.Data))) // uncompressed length
	p.Write(body)
	writeBlock(buf, BlockSnapshot, p.Bytes())
	return nil
}

// writeInput emits a 0x80 input recording block. The frame stream is
// flattened first, then optionally zlib-compressed, then wrapped in
// the IRB header (count, frame-size byte, T-states, flags).
func writeInput(buf *bytes.Buffer, in *InputBlock, compress bool) error {
	var stream bytes.Buffer
	for i, frame := range in.Frames {
		_ = binary.Write(&stream, binary.LittleEndian, frame.Instructions)
		if frame.RepeatLast {
			_ = binary.Write(&stream, binary.LittleEndian, repeatFrameMarker)
			continue
		}
		if len(frame.InBytes) > 0xFFFE {
			return fmt.Errorf("frame %d: in-count %d exceeds max 0xFFFE", i, len(frame.InBytes))
		}
		_ = binary.Write(&stream, binary.LittleEndian, uint16(len(frame.InBytes)))
		stream.Write(frame.InBytes)
	}

	body := stream.Bytes()
	flags := uint32(0)
	if compress && len(body) > 0 {
		compressed, ok := zlibCompress(body)
		if ok {
			body = compressed
			flags |= irbFlagCompressed
		}
	}

	var p bytes.Buffer
	_ = binary.Write(&p, binary.LittleEndian, uint32(len(in.Frames)))
	p.WriteByte(0) // frame size — undefined, always zero on the wire
	_ = binary.Write(&p, binary.LittleEndian, in.Tstates)
	_ = binary.Write(&p, binary.LittleEndian, flags)
	p.Write(body)
	writeBlock(buf, BlockInput, p.Bytes())
	return nil
}

func writeSignStart(buf *bytes.Buffer, s *SignStartBlock) error {
	var p bytes.Buffer
	_ = binary.Write(&p, binary.LittleEndian, s.KeyID)
	_ = binary.Write(&p, binary.LittleEndian, s.WeekCode)
	p.Write(s.Extra)
	writeBlock(buf, BlockSignStart, p.Bytes())
	return nil
}

func writeSignEnd(buf *bytes.Buffer, s *SignEndBlock) error {
	writeBlock(buf, BlockSignEnd, s.Signature)
	return nil
}

// writeFixedString writes a string padded with NULs to exactly width
// bytes, truncating if the string is longer. Used for snapshot format
// identifiers ("Z80\0", "SZX\0", "SNA\0").
func writeFixedString(buf *bytes.Buffer, s string, width int) {
	b := []byte(s)
	if len(b) > width {
		b = b[:width]
	}
	buf.Write(b)
	for i := len(b); i < width; i++ {
		buf.WriteByte(0)
	}
}

// zlibCompress runs the data through zlib and returns the compressed
// bytes. If compression would produce a larger stream than the input
// (rare for tiny inputs), it returns ok=false and the caller emits the
// uncompressed bytes instead — matching libspectrum's rzx_compress at
// rzx.c:1391.
func zlibCompress(data []byte) (compressed []byte, ok bool) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return nil, false
	}
	if err := w.Close(); err != nil {
		return nil, false
	}
	if buf.Len() >= len(data) {
		return nil, false
	}
	return buf.Bytes(), true
}
