package rzx

// Spec-faithful byte-layout tests for the .RZX input-recording format.
//
// Oracle: the RZX format as implemented by libspectrum's rzx.c (the
// canonical reference). Pinned facts:
//
//   - 10-byte header: "RZX!" magic + 1 byte major + 1 byte minor + 4-byte
//     little-endian flags. Unsigned files use minor 12, signed 13.
//   - Each block = 1-byte ID + 4-byte little-endian length, where the
//     length is INCLUSIVE of the ID and length bytes (payload = len - 5).
//   - Snapshot block (0x30): flags(4) + format(4, 3-char NUL-padded) +
//     uncompressed-length(4) + data (zlib if flag bit 1 set; external
//     link if flag bit 0 set).
//   - Input block / IRB (0x80): frame-count(4) + frame-size byte(1,
//     always 0) + T-states(4) + flags(4) + frame stream.
//   - Each frame = instruction-count(2) + IN-count(2) + IN bytes. An
//     IN-count of 0xFFFF means "repeat the previous non-repeated frame's
//     IN reads" and is followed by no bytes.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// TestHeaderByteLayout pins the exact 10-byte header for an unsigned file.
func TestHeaderByteLayout(t *testing.T) {
	f := &File{}
	data, err := f.Write(WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < headerSize {
		t.Fatalf("output %d bytes < header %d", len(data), headerSize)
	}
	if !bytes.Equal(data[0:4], []byte("RZX!")) {
		t.Errorf("magic = % X, want 'RZX!'", data[0:4])
	}
	if data[4] != versionMajor {
		t.Errorf("major = %d, want %d", data[4], versionMajor)
	}
	if data[5] != versionMinorPlay {
		t.Errorf("minor = %d, want %d (unsigned)", data[5], versionMinorPlay)
	}
	if flags := binary.LittleEndian.Uint32(data[6:10]); flags != 0 {
		t.Errorf("flags = %#x, want 0 (unsigned)", flags)
	}
}

// TestHeaderSignedFlagsAndVersion pins that the signed option flips the
// minor version to 13 and sets the signed header flag.
func TestHeaderSignedFlagsAndVersion(t *testing.T) {
	f := &File{}
	data, err := f.Write(WriteOptions{Signed: true})
	if err != nil {
		t.Fatal(err)
	}
	if data[5] != versionMinorSign {
		t.Errorf("minor = %d, want %d (signed)", data[5], versionMinorSign)
	}
	if flags := binary.LittleEndian.Uint32(data[6:10]); flags&headerFlagSigned == 0 {
		t.Errorf("signed flag not set in header flags %#x", flags)
	}
}

// TestBlockLengthIsInclusive pins the inclusive-length rule: the 4-byte
// length after each block ID counts the ID and the length field itself.
func TestBlockLengthIsInclusive(t *testing.T) {
	f := &File{Blocks: []Block{
		{Type: BlockSignEnd, SignEnd: &SignEndBlock{Signature: []byte{0x01, 0x02, 0x03}}},
	}}
	data, err := f.Write(WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// First block starts right after the 10-byte header.
	if BlockType(data[headerSize]) != BlockSignEnd {
		t.Fatalf("block id = 0x%02X, want 0x%02X", data[headerSize], byte(BlockSignEnd))
	}
	blockLen := binary.LittleEndian.Uint32(data[headerSize+1 : headerSize+5])
	// 1 (ID) + 4 (length) + 3 (signature) = 8.
	if blockLen != 8 {
		t.Errorf("block length = %d, want 8 (inclusive of ID+length)", blockLen)
	}
}

// TestSnapshotBlockByteLayout pins the 0x30 snapshot block payload layout
// for an uncompressed embed.
func TestSnapshotBlockByteLayout(t *testing.T) {
	snapData := bytes.Repeat([]byte{0x42}, 64)
	f := &File{Blocks: []Block{
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: "Z80", Data: snapData}},
	}}
	data, err := f.Write(WriteOptions{Compress: false})
	if err != nil {
		t.Fatal(err)
	}
	// Block payload starts at header(10) + id(1) + length(4) = 15.
	p := data[15:]
	flags := binary.LittleEndian.Uint32(p[0:4])
	if flags != 0 {
		t.Errorf("snapshot flags = %#x, want 0 (uncompressed embed)", flags)
	}
	if got := nullTerminated(p[4:8]); got != "Z80" {
		t.Errorf("format = %q, want Z80", got)
	}
	// Format field is exactly 4 bytes, 3-char + NUL.
	if p[7] != 0 {
		t.Errorf("format field 4th byte = 0x%02X, want NUL pad", p[7])
	}
	uncompLen := binary.LittleEndian.Uint32(p[8:12])
	if uncompLen != uint32(len(snapData)) {
		t.Errorf("uncompressed length = %d, want %d", uncompLen, len(snapData))
	}
	if !bytes.Equal(p[12:12+len(snapData)], snapData) {
		t.Error("embedded snapshot bytes not stored verbatim")
	}
}

// TestInputBlockByteLayout pins the 0x80 IRB header layout and the frame
// stream encoding for a couple of frames.
func TestInputBlockByteLayout(t *testing.T) {
	f := &File{Blocks: []Block{
		{Type: BlockInput, Input: &InputBlock{
			Tstates: 0x11223344,
			Frames: []Frame{
				{Instructions: 0x0102, InBytes: []byte{0xAA, 0xBB}},
				{Instructions: 0x0304, RepeatLast: true},
			},
		}},
	}}
	data, err := f.Write(WriteOptions{Compress: false})
	if err != nil {
		t.Fatal(err)
	}
	p := data[15:] // IRB payload
	if fc := binary.LittleEndian.Uint32(p[0:4]); fc != 2 {
		t.Errorf("frame count = %d, want 2", fc)
	}
	if p[4] != 0 {
		t.Errorf("frame-size byte = %d, want 0 (always zero on the wire)", p[4])
	}
	if ts := binary.LittleEndian.Uint32(p[5:9]); ts != 0x11223344 {
		t.Errorf("T-states = %#x, want 0x11223344", ts)
	}
	if fl := binary.LittleEndian.Uint32(p[9:13]); fl != 0 {
		t.Errorf("IRB flags = %#x, want 0 (uncompressed)", fl)
	}
	// Frame stream begins at payload offset 13.
	fs := p[13:]
	// Frame 0: instr(2) = 0x0102, in-count(2) = 2, then AA BB.
	if binary.LittleEndian.Uint16(fs[0:2]) != 0x0102 {
		t.Errorf("frame0 instr = %#x, want 0x0102", binary.LittleEndian.Uint16(fs[0:2]))
	}
	if binary.LittleEndian.Uint16(fs[2:4]) != 2 {
		t.Errorf("frame0 in-count = %d, want 2", binary.LittleEndian.Uint16(fs[2:4]))
	}
	if fs[4] != 0xAA || fs[5] != 0xBB {
		t.Errorf("frame0 IN bytes = % X, want AA BB", fs[4:6])
	}
	// Frame 1: instr(2) = 0x0304, in-count(2) = 0xFFFF (repeat marker), no bytes.
	if binary.LittleEndian.Uint16(fs[6:8]) != 0x0304 {
		t.Errorf("frame1 instr = %#x, want 0x0304", binary.LittleEndian.Uint16(fs[6:8]))
	}
	if binary.LittleEndian.Uint16(fs[8:10]) != repeatFrameMarker {
		t.Errorf("frame1 in-count = %#x, want repeat marker %#x",
			binary.LittleEndian.Uint16(fs[8:10]), repeatFrameMarker)
	}
}

// TestRepeatMarkerRoundTrip pins that a RepeatLast frame survives
// write->read with no IN bytes and the flag intact.
func TestRepeatMarkerRoundTrip(t *testing.T) {
	f := &File{Blocks: []Block{
		{Type: BlockInput, Input: &InputBlock{
			Frames: []Frame{
				{Instructions: 10, InBytes: []byte{0xFE}},
				{Instructions: 20, RepeatLast: true},
			},
		}},
	}}
	data, err := f.Write(WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	back, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	frames := back.Blocks[0].Input.Frames
	if !frames[1].RepeatLast {
		t.Error("frame 1 RepeatLast not preserved")
	}
	if len(frames[1].InBytes) != 0 {
		t.Errorf("repeat frame carried %d IN bytes, want 0", len(frames[1].InBytes))
	}
}

// TestReadRejectsBlockLengthBelowMinimum pins that a block whose declared
// length is below 5 (the ID + length bytes alone) is rejected.
func TestReadRejectsBlockLengthBelowMinimum(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(magic)
	buf.WriteByte(versionMajor)
	buf.WriteByte(versionMinorPlay)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	buf.WriteByte(byte(BlockCreator))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(4)) // < 5
	if _, err := Read(buf.Bytes()); err == nil {
		t.Fatal("expected error for block length < 5, got nil")
	}
}

// TestReadRejectsPayloadPastEOF pins that a block claiming more payload
// than the file contains is rejected (no over-read).
func TestReadRejectsPayloadPastEOF(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(magic)
	buf.WriteByte(versionMajor)
	buf.WriteByte(versionMinorPlay)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	buf.WriteByte(byte(BlockSignEnd))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(105)) // claims 100-byte payload
	buf.Write([]byte{0x01, 0x02})                            // only 2 bytes present
	if _, err := Read(buf.Bytes()); err == nil {
		t.Fatal("expected error for payload past EOF, got nil")
	}
}

// TestReadRejectsImplausibleFrameCount pins that an input block's
// declared frame count is sanity-checked against the actual size of the
// (decompressed) frame stream before it is used to size the Frames
// slice. Each frame needs at least 4 bytes (instruction count + IN
// count), so a declared count far exceeding data-length/4 is rejected
// up front rather than driving an oversized allocation for a tiny file.
func TestReadRejectsImplausibleFrameCount(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(magic)
	buf.WriteByte(versionMajor)
	buf.WriteByte(versionMinorPlay)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))

	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.LittleEndian, uint32(1000000)) // frame count
	payload.WriteByte(0)                                             // frame size byte
	_ = binary.Write(&payload, binary.LittleEndian, uint32(0))       // tstates
	_ = binary.Write(&payload, binary.LittleEndian, uint32(0))       // flags (uncompressed)
	payload.Write([]byte{0x01, 0x02})                                // 2 stray bytes, nowhere near 4,000,000

	buf.WriteByte(byte(BlockInput))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(5+payload.Len()))
	buf.Write(payload.Bytes())

	_, err := Read(buf.Bytes())
	if err == nil {
		t.Fatal("expected error for implausible frame count, got nil")
	}
	if !strings.Contains(err.Error(), "implausible") {
		t.Errorf("error = %q, want it to mention the count was rejected as implausible", err.Error())
	}
}

// TestReadBoundsSnapshotDecompressionByDeclaredLength pins that inflating
// a compressed snapshot body stops just past the block's own declared
// uncompressed-length field rather than fully materializing however much
// the compressed stream claims to contain. Without this, a small,
// highly-compressible ("bomb") stream paired with a small declared
// length would still be inflated in full before the length mismatch is
// detected.
func TestReadBoundsSnapshotDecompressionByDeclaredLength(t *testing.T) {
	bomb := bytes.Repeat([]byte{0}, 5*1024*1024)
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write(bomb); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.LittleEndian, snapFlagCompressed)
	payload.Write([]byte("SZX\x00"))
	_ = binary.Write(&payload, binary.LittleEndian, uint32(100)) // far below the real inflate size
	payload.Write(compressed.Bytes())

	_, err := readSnapshot(payload.Bytes())
	if err == nil {
		t.Fatal("expected a length-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "inflated length 101") {
		t.Errorf("error = %q, want inflation capped just above the declared length (101 bytes), not the full 5MB bomb", err.Error())
	}
}

// TestCompressedSnapshotFlagAndInflate pins the compressed-snapshot path:
// the compressed flag bit is set, the uncompressed-length field reflects
// the ORIGINAL size, and the bytes inflate back to the original.
func TestCompressedSnapshotFlagAndInflate(t *testing.T) {
	// Highly compressible payload so zlib definitely shrinks it.
	orig := bytes.Repeat([]byte{0x00}, 4096)
	f := &File{Blocks: []Block{
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: "SZX", Data: orig}},
	}}
	data, err := f.Write(WriteOptions{Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	p := data[15:]
	flags := binary.LittleEndian.Uint32(p[0:4])
	if flags&snapFlagCompressed == 0 {
		t.Fatal("compressed flag not set on a highly-compressible payload")
	}
	if got := binary.LittleEndian.Uint32(p[8:12]); got != uint32(len(orig)) {
		t.Errorf("uncompressed-length field = %d, want %d (original size)", got, len(orig))
	}
	// Full parse must inflate it back to the original bytes.
	back, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Blocks[0].Snapshot.Data, orig) {
		t.Error("inflated snapshot bytes != original")
	}
}

// TestSnapshotEmbedRoundTripViaSnapshotPackage pins the bridge between the
// snapshot package and an RZX snapshot block: encode a real snapshot,
// embed it, write the RZX, read it back, decode, and confirm a CPU
// register survives the whole round trip.
func TestSnapshotEmbedRoundTripViaSnapshotPackage(t *testing.T) {
	snap := snapshot.New()
	snap.CPU.PC = 0x1234
	snap.CPU.SP = 0xFF00
	snap.CPU.A = 0x99
	snap.CPU.IM = 1
	snap.CPU.BorderColor = 3

	blk, err := EncodeSnapshot(snap, "Z80", false)
	if err != nil {
		t.Fatal(err)
	}
	f := &File{Blocks: []Block{{Type: BlockSnapshot, Snapshot: blk}}}
	data, err := f.Write(WriteOptions{Compress: true})
	if err != nil {
		t.Fatal(err)
	}
	back, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSnapshot(back.Blocks[0].Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CPU.PC != 0x1234 {
		t.Errorf("PC = %#04x, want 0x1234", decoded.CPU.PC)
	}
	if decoded.CPU.A != 0x99 {
		t.Errorf("A = %#02x, want 0x99", decoded.CPU.A)
	}
	if decoded.CPU.BorderColor != 3 {
		t.Errorf("border = %d, want 3", decoded.CPU.BorderColor)
	}
}

// TestExternalSnapshotLinkSkipped pins that a snapshot block flagged as an
// external link (flag bit 0) parses with empty Data (libspectrum skips
// such links) rather than erroring.
func TestExternalSnapshotLinkSkipped(t *testing.T) {
	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.LittleEndian, snapFlagExternal)
	payload.Write([]byte{'Z', '8', '0', 0})                    // format
	_ = binary.Write(&payload, binary.LittleEndian, uint32(0)) // uncompressed length
	payload.Write([]byte("c:\\game.z80\x00"))                  // external path

	var buf bytes.Buffer
	buf.Write(magic)
	buf.WriteByte(versionMajor)
	buf.WriteByte(versionMinorPlay)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	buf.WriteByte(byte(BlockSnapshot))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(5+payload.Len()))
	buf.Write(payload.Bytes())

	f, err := Read(buf.Bytes())
	if err != nil {
		t.Fatalf("external-link snapshot should parse, got %v", err)
	}
	sb := f.Blocks[0].Snapshot
	if sb == nil {
		t.Fatal("snapshot block missing")
	}
	if len(sb.Data) != 0 {
		t.Errorf("external link should have empty Data, got %d bytes", len(sb.Data))
	}
	if sb.Format != "Z80" {
		t.Errorf("format = %q, want Z80", sb.Format)
	}
}
