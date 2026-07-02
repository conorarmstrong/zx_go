package rzx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadFile is a thin wrapper around Read that loads the file from
// disk first. Mirrors the Load(path) shape of pkg/snapshot.Snapshot
// so RZX file handling matches the rest of the emulator's load idioms.
func ReadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rzx: read %s: %w", path, err)
	}
	return Read(data)
}

// ErrSignature is returned when the file doesn't start with the "RZX!"
// magic. Callers can use this to distinguish "definitely not an RZX
// file" from "RZX file with a structural problem".
var ErrSignature = errors.New("rzx: file does not start with RZX! magic")

// Read parses an RZX file from a byte slice. The format is documented
// in libspectrum's rzx.c (the canonical implementation): a 10-byte
// header followed by a chain of typed blocks. Unknown block IDs are
// rejected to match libspectrum's behaviour — RZX has no skip-unknown
// rule like SZX does.
func Read(data []byte) (*File, error) {
	if len(data) < headerSize {
		return nil, fmt.Errorf("rzx: file too short: %d bytes (need %d for header)", len(data), headerSize)
	}
	if !bytes.HasPrefix(data, magic) {
		return nil, ErrSignature
	}

	// Header layout: 4 bytes magic, 1 byte major, 1 byte minor,
	// 4 bytes flags. We don't reject unknown versions — libspectrum
	// itself only writes major=0 minor=12/13 but accepts everything.
	_ = data[4] // major version, currently unused
	_ = data[5] // minor version, currently unused
	_ = binary.LittleEndian.Uint32(data[6:10])

	f := &File{}

	// Walk blocks until we run out of bytes. Each block is
	// 1-byte ID + 4-byte length (LE) + (length-5)-byte payload.
	pos := headerSize
	for pos < len(data) {
		id := BlockType(data[pos])
		pos++

		// Every block has a 4-byte length immediately after the
		// ID byte. The length is INCLUSIVE of the ID and length
		// bytes themselves, so the payload is length-5.
		if pos+4 > len(data) {
			return nil, fmt.Errorf("rzx: block 0x%02x: truncated length field at offset %d", id, pos)
		}
		blockLen := binary.LittleEndian.Uint32(data[pos : pos+4])
		pos += 4
		if blockLen < 5 {
			return nil, fmt.Errorf("rzx: block 0x%02x: length %d below minimum 5", id, blockLen)
		}
		payloadLen := int(blockLen) - 5
		if pos+payloadLen > len(data) {
			return nil, fmt.Errorf("rzx: block 0x%02x: payload length %d runs past end of file (have %d)", id, payloadLen, len(data)-pos)
		}
		payload := data[pos : pos+payloadLen]

		var block Block
		var err error
		switch id {
		case BlockCreator:
			block, err = readCreator(payload)
		case BlockSnapshot:
			block, err = readSnapshot(payload)
		case BlockInput:
			block, err = readInput(payload)
		case BlockSignStart:
			block, err = readSignStart(payload)
		case BlockSignEnd:
			block, err = readSignEnd(payload)
		default:
			return nil, fmt.Errorf("rzx: unknown block ID 0x%02x at offset %d", id, pos-5)
		}
		if err != nil {
			return nil, fmt.Errorf("rzx: block 0x%02x: %w", id, err)
		}
		f.Blocks = append(f.Blocks, block)

		pos += payloadLen
	}

	return f, nil
}

// readCreator parses a 0x10 creator block: 20 bytes program name +
// 2 bytes major + 2 bytes minor + N bytes custom data.
func readCreator(payload []byte) (Block, error) {
	if len(payload) < 24 {
		return Block{}, fmt.Errorf("creator: payload %d bytes < 24", len(payload))
	}
	prog := nullTerminated(payload[0:20])
	major := binary.LittleEndian.Uint16(payload[20:22])
	minor := binary.LittleEndian.Uint16(payload[22:24])
	custom := append([]byte(nil), payload[24:]...)
	return Block{
		Type:    BlockCreator,
		Creator: &CreatorBlock{Program: prog, Major: major, Minor: minor, Custom: custom},
	}, nil
}

// readSnapshot parses a 0x30 snapshot block: 4 bytes flags + 4 bytes
// type identifier (3-char NUL-terminated) + 4 bytes uncompressed length
// + N bytes snapshot data (zlib if flag bit 1 set). External-link
// snapshots (flag bit 0) are returned as a SnapshotBlock with empty
// Data — matches libspectrum which silently skips them.
func readSnapshot(payload []byte) (Block, error) {
	if len(payload) < 12 {
		return Block{}, fmt.Errorf("snapshot: payload %d bytes < 12", len(payload))
	}
	flags := binary.LittleEndian.Uint32(payload[0:4])
	format := nullTerminated(payload[4:8])
	uncompressedLen := binary.LittleEndian.Uint32(payload[8:12])
	body := payload[12:]

	if flags&snapFlagExternal != 0 {
		// libspectrum skips external-link snaps without erroring.
		// Preserve the format identifier so a round-trip writer
		// could in principle re-emit the link, even though we don't.
		return Block{
			Type:     BlockSnapshot,
			Snapshot: &SnapshotBlock{Format: format},
		}, nil
	}

	var data []byte
	if flags&snapFlagCompressed != 0 {
		// The block declares its own uncompressed length, so inflation
		// only ever needs to produce uncompressedLen+1 bytes: exactly
		// that on a well-formed file, or one byte past it to prove a
		// mismatch. Bounding the read this way means a small, hostile
		// zlib stream can't be used to inflate an arbitrarily large
		// buffer before the length check below rejects it.
		raw, err := zlibInflateLimit(body, uncompressedLen)
		if err != nil {
			return Block{}, fmt.Errorf("snapshot: zlib inflate: %w", err)
		}
		if uint32(len(raw)) != uncompressedLen {
			return Block{}, fmt.Errorf("snapshot: inflated length %d != declared %d", len(raw), uncompressedLen)
		}
		data = raw
	} else {
		if uint32(len(body)) != uncompressedLen {
			return Block{}, fmt.Errorf("snapshot: stored length %d != declared %d", len(body), uncompressedLen)
		}
		data = append([]byte(nil), body...)
	}

	return Block{
		Type:     BlockSnapshot,
		Snapshot: &SnapshotBlock{Format: format, Data: data},
	}, nil
}

// readInput parses a 0x80 input recording block: 4 bytes frame count +
// 1 byte frame size (always 0) + 4 bytes T-state counter + 4 bytes
// flags + N bytes frame stream (zlib if flag bit 1 set).
func readInput(payload []byte) (Block, error) {
	if len(payload) < 13 {
		return Block{}, fmt.Errorf("input: payload %d bytes < 13", len(payload))
	}
	frameCount := binary.LittleEndian.Uint32(payload[0:4])
	// payload[4] is the "frame size" byte — undefined in the spec,
	// always written as 0 by libspectrum, ignored on read.
	tstates := binary.LittleEndian.Uint32(payload[5:9])
	flags := binary.LittleEndian.Uint32(payload[9:13])
	body := payload[13:]

	var frameBytes []byte
	if flags&irbFlagCompressed != 0 {
		raw, err := zlibInflate(body)
		if err != nil {
			return Block{}, fmt.Errorf("input: zlib inflate: %w", err)
		}
		frameBytes = raw
	} else {
		frameBytes = body
	}

	frames, err := readFrames(frameBytes, int(frameCount))
	if err != nil {
		return Block{}, err
	}
	return Block{
		Type:  BlockInput,
		Input: &InputBlock{Tstates: tstates, Frames: frames},
	}, nil
}

// readFrames parses `count` frames from a flat byte slice. Each frame
// is 2 bytes instruction count + 2 bytes IN count, optionally followed
// by IN-count bytes. IN count = 0xFFFF means "repeat the last
// non-repeated frame's IN reads" and has no following byte stream.
func readFrames(data []byte, count int) ([]Frame, error) {
	// Every frame needs at least 4 bytes (instruction count + IN count),
	// so a declared count exceeding that is rejected before it is used
	// to size the Frames slice — guards against a corrupt or hostile
	// frame-count field driving a huge allocation for a tiny stream.
	if count > len(data)/4 {
		return nil, fmt.Errorf("frame count %d implausible for a %d-byte stream", count, len(data))
	}
	frames := make([]Frame, count)
	pos := 0
	for i := 0; i < count; i++ {
		if pos+4 > len(data) {
			return nil, fmt.Errorf("frame %d: truncated header (%d bytes left)", i, len(data)-pos)
		}
		instructions := binary.LittleEndian.Uint16(data[pos : pos+2])
		inCount := binary.LittleEndian.Uint16(data[pos+2 : pos+4])
		pos += 4

		if inCount == repeatFrameMarker {
			frames[i] = Frame{Instructions: instructions, RepeatLast: true}
			continue
		}
		if pos+int(inCount) > len(data) {
			return nil, fmt.Errorf("frame %d: in-count %d runs past end of stream", i, inCount)
		}
		var inBytes []byte
		if inCount > 0 {
			inBytes = append([]byte(nil), data[pos:pos+int(inCount)]...)
		}
		frames[i] = Frame{Instructions: instructions, InBytes: inBytes}
		pos += int(inCount)
	}
	return frames, nil
}

// readSignStart parses a 0x20 sign-start block: 4 bytes key ID + 4
// bytes week code + N bytes (preserved verbatim).
func readSignStart(payload []byte) (Block, error) {
	if len(payload) < 8 {
		return Block{}, fmt.Errorf("sign-start: payload %d bytes < 8", len(payload))
	}
	keyID := binary.LittleEndian.Uint32(payload[0:4])
	weekCode := binary.LittleEndian.Uint32(payload[4:8])
	extra := append([]byte(nil), payload[8:]...)
	return Block{
		Type:      BlockSignStart,
		SignStart: &SignStartBlock{KeyID: keyID, WeekCode: weekCode, Extra: extra},
	}, nil
}

// readSignEnd parses a 0x21 sign-end block: the entire payload is the
// DSA signature (r and s MPIs concatenated). We preserve it verbatim.
func readSignEnd(payload []byte) (Block, error) {
	return Block{
		Type:    BlockSignEnd,
		SignEnd: &SignEndBlock{Signature: append([]byte(nil), payload...)},
	}, nil
}

// nullTerminated copies a fixed-width byte slice into a Go string,
// stopping at the first NUL. Used for the creator program name and the
// snapshot format identifier.
func nullTerminated(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// zlibInflate decompresses a zlib (RFC 1950) stream into a fresh byte
// slice. Used for compressed input frame streams, which have no
// declared uncompressed length to bound the read by.
func zlibInflate(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// zlibInflateLimit decompresses a zlib stream like zlibInflate, but
// stops reading at limit+1 bytes. Used for compressed snapshot bodies,
// where the block header already declares the expected uncompressed
// length: the caller compares the returned length against that
// declaration, so reading one byte past it is enough to prove a
// mismatch without inflating the rest of a hostile stream.
func zlibInflateLimit(data []byte, limit uint32) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	return out, nil
}
