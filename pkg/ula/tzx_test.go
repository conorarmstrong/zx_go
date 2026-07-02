package ula

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createTestTZX writes a small TZX file with one 0x10 standard
// block, one 0x11 turbo block, and one 0x14 pure-data block, so
// the loader / saver round-trip exercises all three block types
// SaveTZX emits.
func createTestTZX(t *testing.T, dir string) string {
	t.Helper()
	var buf bytes.Buffer

	// Header
	buf.Write([]byte{'Z', 'X', 'T', 'a', 'p', 'e', '!', 0x1A, 1, 20})

	// 0x10 standard-speed block: pause + length + data
	standard := []byte{0xAA, 0x55, 0x00, 0xFF}
	buf.WriteByte(0x10)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1000)) // pause
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(standard)))
	buf.Write(standard)

	// 0x11 turbo-speed block: all timing params + length + data
	turbo := []byte{0x01, 0x02, 0x03}
	buf.WriteByte(0x11)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2168)) // pilotPulse
	_ = binary.Write(&buf, binary.LittleEndian, uint16(667))  // syncFirst
	_ = binary.Write(&buf, binary.LittleEndian, uint16(735))  // syncSecond
	_ = binary.Write(&buf, binary.LittleEndian, uint16(855))  // zeroPulse
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1710)) // onePulse
	_ = binary.Write(&buf, binary.LittleEndian, uint16(3220)) // pilotLen
	buf.WriteByte(8)                                          // usedBits
	_ = binary.Write(&buf, binary.LittleEndian, uint16(500))  // pause
	// 3-byte length, little-endian
	buf.Write([]byte{byte(len(turbo)), 0, 0})
	buf.Write(turbo)

	// 0x14 pure-data block: zeroPulse + onePulse + usedBits + pause + length + data
	pure := []byte{0xC3, 0x3C}
	buf.WriteByte(0x14)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(855))  // zeroPulse
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1710)) // onePulse
	buf.WriteByte(8)                                          // usedBits
	_ = binary.Write(&buf, binary.LittleEndian, uint16(750))  // pause
	buf.Write([]byte{byte(len(pure)), 0, 0})
	buf.Write(pure)

	path := filepath.Join(dir, "test.tzx")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveTZXRoundtrip(t *testing.T) {
	dir := t.TempDir()
	srcPath := createTestTZX(t, dir)

	tp1 := NewTapePlayer()
	if err := tp1.LoadTZX(srcPath); err != nil {
		t.Fatalf("LoadTZX (initial): %v", err)
	}
	if got := tp1.BlockCount(); got != 3 {
		t.Fatalf("loaded %d blocks, want 3", got)
	}

	outPath := filepath.Join(dir, "out.tzx")
	if err := tp1.SaveTZX(outPath); err != nil {
		t.Fatalf("SaveTZX: %v", err)
	}

	// Reload from the saved file — block count, data, and timing
	// params must all round-trip.
	tp2 := NewTapePlayer()
	if err := tp2.LoadTZX(outPath); err != nil {
		t.Fatalf("LoadTZX (after save): %v", err)
	}
	if got := tp2.BlockCount(); got != 3 {
		t.Fatalf("reloaded %d blocks, want 3", got)
	}

	for i := range tp1.blocks {
		a, b := tp1.blocks[i], tp2.blocks[i]
		if !bytes.Equal(a.data, b.data) {
			t.Errorf("block %d data mismatch: %v vs %v", i, a.data, b.data)
		}
		if a.pause != b.pause {
			t.Errorf("block %d pause: %d vs %d", i, a.pause, b.pause)
		}
		if a.turbo != b.turbo {
			t.Errorf("block %d turbo: %v vs %v", i, a.turbo, b.turbo)
		}
		if a.turbo {
			if a.pilotPulse != b.pilotPulse || a.syncFirst != b.syncFirst ||
				a.syncSecond != b.syncSecond || a.zeroPulse != b.zeroPulse ||
				a.onePulse != b.onePulse || a.pilotLen != b.pilotLen ||
				a.usedBits != b.usedBits {
				t.Errorf("block %d turbo params mismatch:\n  orig: %+v\n  rt:   %+v", i, a, b)
			}
		}
	}
}

func TestSaveTZXEmptyFails(t *testing.T) {
	tp := NewTapePlayer()
	dir := t.TempDir()
	err := tp.SaveTZX(filepath.Join(dir, "empty.tzx"))
	if err == nil {
		t.Error("SaveTZX with no blocks should return an error")
	}
}

// TestSaveTZXProducesExactBytes pins the SaveTZX wire format against
// a hand-constructed reference. The previous roundtrip test would
// pass even if every field's encoding was *symmetrically* wrong (e.g.
// two uint16 fields swapped on both sides); this test catches that
// class of bug by comparing the saved bytes to a known-good layout
// derived from the TZX 1.20 spec.
func TestSaveTZXProducesExactBytes(t *testing.T) {
	// One 0x10 standard-speed block, one 0x14 pure-data block, one
	// 0x11 turbo block. Specific magic numbers chosen so a swap of
	// any two adjacent uint16 fields shows up obviously.
	tp := &TapePlayer{
		blocks: []tapeBlock{
			{
				// 0x10 standard: pause=0x1234, data=[0xAA, 0xBB, 0xCC].
				data:  []byte{0xAA, 0xBB, 0xCC},
				pause: 0x1234,
			},
			{
				// 0x14 pure-data (turbo + no pilot): all-distinct
				// timing values, used-bits=7, pause=0x5678,
				// data=[0xDD].
				data:      []byte{0xDD},
				turbo:     true,
				pilotLen:  0, // pure-data
				zeroPulse: 0x1111,
				onePulse:  0x2222,
				usedBits:  7,
				pause:     0x5678,
			},
			{
				// 0x11 turbo: every uint16 distinct so a swap is
				// visible. used-bits=8.
				data:       []byte{0xEE, 0xFF},
				turbo:      true,
				pilotPulse: 0x0AAA,
				syncFirst:  0x0BBB,
				syncSecond: 0x0CCC,
				zeroPulse:  0x0DDD,
				onePulse:   0x0EEE,
				pilotLen:   0x0FFF,
				usedBits:   8,
				pause:      0x9ABC,
			},
		},
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "ref.tzx")
	if err := tp.SaveTZX(out); err != nil {
		t.Fatalf("SaveTZX: %v", err)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}

	want := []byte{
		// Header: "ZXTape!" + 0x1A + major + minor
		'Z', 'X', 'T', 'a', 'p', 'e', '!', 0x1A, 1, 20,

		// Block 0x10: id(1) + pause LE(2) + len LE(2) + data
		0x10,
		0x34, 0x12, // pause = 0x1234
		0x03, 0x00, // length = 3
		0xAA, 0xBB, 0xCC,

		// Block 0x14: id(1) + zeroPulse(2) + onePulse(2) + usedBits(1)
		// + pause(2) + length(3) + data
		0x14,
		0x11, 0x11, // zeroPulse = 0x1111
		0x22, 0x22, // onePulse  = 0x2222
		0x07,       // usedBits  = 7
		0x78, 0x56, // pause     = 0x5678
		0x01, 0x00, 0x00, // length = 1 (3-byte LE)
		0xDD,

		// Block 0x11: id(1) + pilotPulse(2) + syncFirst(2)
		// + syncSecond(2) + zeroPulse(2) + onePulse(2)
		// + pilotLen(2) + usedBits(1) + pause(2) + length(3) + data
		0x11,
		0xAA, 0x0A, // pilotPulse = 0x0AAA
		0xBB, 0x0B, // syncFirst  = 0x0BBB
		0xCC, 0x0C, // syncSecond = 0x0CCC
		0xDD, 0x0D, // zeroPulse  = 0x0DDD
		0xEE, 0x0E, // onePulse   = 0x0EEE
		0xFF, 0x0F, // pilotLen   = 0x0FFF
		0x08,       // usedBits   = 8
		0xBC, 0x9A, // pause      = 0x9ABC
		0x02, 0x00, 0x00, // length = 2 (3-byte LE)
		0xEE, 0xFF,
	}

	if !bytes.Equal(got, want) {
		// Find first-divergence offset to make the failure actionable.
		minLen := len(got)
		if len(want) < minLen {
			minLen = len(want)
		}
		divAt := -1
		for i := 0; i < minLen; i++ {
			if got[i] != want[i] {
				divAt = i
				break
			}
		}
		if divAt < 0 && len(got) != len(want) {
			divAt = minLen
		}
		t.Errorf("SaveTZX wire format mismatch at byte %d\n  got  (%d bytes): %x\n  want (%d bytes): %x",
			divAt, len(got), got, len(want), want)
	}
}

func TestSaveTZXHeaderIsValid(t *testing.T) {
	dir := t.TempDir()
	srcPath := createTestTZX(t, dir)

	tp := NewTapePlayer()
	if err := tp.LoadTZX(srcPath); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.tzx")
	if err := tp.SaveTZX(outPath); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 10 {
		t.Fatalf("output too short: %d bytes", len(data))
	}
	if string(data[0:7]) != "ZXTape!" || data[7] != 0x1A {
		t.Errorf("invalid TZX signature in output: %x", data[0:8])
	}
	if data[8] != tzxVersionMajor || data[9] != tzxVersionMinor {
		t.Errorf("version: %d.%d, want %d.%d", data[8], data[9], tzxVersionMajor, tzxVersionMinor)
	}
}

// =========================================================================
// TZX per-block parser tests.
//
// LoadTZX advances `offset` past every block type the spec defines. The
// SaveTZX round-trip tests elsewhere only cover the three "loadable" types
// (0x10, 0x11, 0x14); these tests verify the OTHER block IDs (0x12, 0x13,
// 0x15, 0x20-0x25, 0x2A, 0x2B, 0x30-0x33, 0x35, 0x5A) parse without
// corrupting the loadable-blocks list and that a final 0x10 block at the
// end is still found.
//
// Reference: TZX format spec at https://worldofspectrum.net/TZXformat.html.
// Each test builds a minimal valid TZX with the block under test
// sandwiched between two 0x10 standard blocks, then asserts both
// standard blocks loaded.
// =========================================================================

// tzxBuilder helps assemble TZX byte streams for parser tests.
type tzxBuilder struct{ buf bytes.Buffer }

func newTZXBuilder() *tzxBuilder {
	b := &tzxBuilder{}
	b.buf.Write([]byte{'Z', 'X', 'T', 'a', 'p', 'e', '!', 0x1A, 1, 20})
	return b
}

func (b *tzxBuilder) standardBlock(data []byte) *tzxBuilder {
	b.buf.WriteByte(0x10)
	_ = binary.Write(&b.buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&b.buf, binary.LittleEndian, uint16(len(data)))
	b.buf.Write(data)
	return b
}

func (b *tzxBuilder) raw(bs ...byte) *tzxBuilder { b.buf.Write(bs); return b }

// writeAndLoad writes the builder's bytes to a temp .tzx then runs
// LoadTZX. Returns the resulting block count.
func (b *tzxBuilder) writeAndLoad(t *testing.T) (int, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "x.tzx")
	if err := os.WriteFile(p, b.buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTZX(p); err != nil {
		return 0, err
	}
	return tp.BlockCount(), nil
}

// blockSandwich runs a block-spec test: load a TZX with two standard
// 0x10 blocks bracketing the block-under-test. Both standards should
// be present iff the parser correctly advanced past the test block.
func blockSandwich(t *testing.T, name string, blockBytes []byte) {
	t.Helper()
	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA})
	b.raw(blockBytes...)
	b.standardBlock([]byte{0xBB})
	n, err := b.writeAndLoad(t)
	if err != nil {
		t.Fatalf("%s: LoadTZX failed: %v", name, err)
	}
	if n != 2 {
		t.Errorf("%s: block count = %d, want 2 (sandwich block confused the parser)",
			name, n)
	}
}

// TestTZX_Block12_PureTone verifies the parser advances 4 bytes past
// a 0x12 block. Spec: pulse-length (word) + pulse-count (word).
func TestTZX_Block12_PureTone(t *testing.T) {
	blockSandwich(t, "0x12 PureTone", []byte{
		0x12,
		0x14, 0x08, // pulse length = 2068
		0xE7, 0x03, // pulse count = 999
	})
}

// TestTZX_Block13_PulseSequence verifies skip-by-(N*2) for count
// 1-byte + N×word pulse lengths.
func TestTZX_Block13_PulseSequence(t *testing.T) {
	// 3 pulses → 1 + 3*2 = 7 bytes of payload.
	blockSandwich(t, "0x13 PulseSequence", []byte{
		0x13,
		0x03, // count = 3
		0x10, 0x00, 0x20, 0x00, 0x30, 0x00,
	})
}

// TestTZX_Block15_DirectRecording skip-by-(8 + 3-byte length).
func TestTZX_Block15_DirectRecording(t *testing.T) {
	// length = 4 sample bytes
	payload := []byte{
		0x15,
		0x10, 0x00, // T-states per sample (word)
		0xE8, 0x03, // pause after (word)
		0x08,             // used bits in last byte
		0x04, 0x00, 0x00, // length = 4
		0xAA, 0x55, 0xAA, 0x55, // samples
	}
	blockSandwich(t, "0x15 DirectRecording", payload)
}

// TestTZX_Block20_PauseStop verifies skip 2 bytes (pause duration).
func TestTZX_Block20_PauseStop(t *testing.T) {
	blockSandwich(t, "0x20 PauseStop", []byte{0x20, 0xE8, 0x03})
}

// TestTZX_Block21_GroupStart verifies skip 1 + name-length bytes.
func TestTZX_Block21_GroupStart(t *testing.T) {
	name := "hello"
	payload := append([]byte{0x21, byte(len(name))}, []byte(name)...)
	blockSandwich(t, "0x21 GroupStart", payload)
}

// TestTZX_Block22_GroupEnd verifies zero-byte payload doesn't break.
func TestTZX_Block22_GroupEnd(t *testing.T) {
	blockSandwich(t, "0x22 GroupEnd", []byte{0x22})
}

// TestTZX_Block23_JumpToBlock verifies skip 2 bytes.
func TestTZX_Block23_JumpToBlock(t *testing.T) {
	blockSandwich(t, "0x23 JumpToBlock", []byte{0x23, 0x05, 0x00})
}

// TestTZX_Block24_LoopStart verifies skip 2 bytes (loop count).
func TestTZX_Block24_LoopStart(t *testing.T) {
	blockSandwich(t, "0x24 LoopStart", []byte{0x24, 0x0A, 0x00})
}

// TestTZX_Block25_LoopEnd zero-byte payload.
func TestTZX_Block25_LoopEnd(t *testing.T) {
	blockSandwich(t, "0x25 LoopEnd", []byte{0x25})
}

// TestTZX_Block2A_Stop48K verifies skip 4 bytes (length word x 2).
func TestTZX_Block2A_Stop48K(t *testing.T) {
	blockSandwich(t, "0x2A Stop48K", []byte{0x2A, 0x00, 0x00, 0x00, 0x00})
}

// TestTZX_Block2B_SignalLevel verifies skip 5 bytes.
func TestTZX_Block2B_SignalLevel(t *testing.T) {
	blockSandwich(t, "0x2B SignalLevel", []byte{0x2B, 0x01, 0x00, 0x00, 0x00, 0x01})
}

// TestTZX_Block30_TextDescription verifies skip 1 + text-length bytes.
func TestTZX_Block30_TextDescription(t *testing.T) {
	text := "Made by Mike Smith"
	payload := append([]byte{0x30, byte(len(text))}, []byte(text)...)
	blockSandwich(t, "0x30 TextDescription", payload)
}

// TestTZX_Block31_Message verifies skip 2 + text-length bytes.
func TestTZX_Block31_Message(t *testing.T) {
	text := "Hello"
	payload := append([]byte{0x31, 0x05, byte(len(text))}, []byte(text)...)
	blockSandwich(t, "0x31 Message", payload)
}

// TestTZX_Block32_ArchiveInfo verifies skip 2-byte len + payload.
func TestTZX_Block32_ArchiveInfo(t *testing.T) {
	payload := []byte{0x32, 0x06, 0x00, 0x00, 0x04, 0x41, 0x42, 0x43, 0x44}
	blockSandwich(t, "0x32 ArchiveInfo", payload)
}

// TestTZX_Block33_HardwareType verifies skip 1 + (count*3).
func TestTZX_Block33_HardwareType(t *testing.T) {
	// 2 entries × 3 bytes each = 6 bytes payload after count.
	payload := []byte{0x33, 0x02, 0x00, 0x00, 0x01, 0x01, 0x02, 0x00}
	blockSandwich(t, "0x33 HardwareType", payload)
}

// TestTZX_Block35_CustomInfo verifies skip 20 + 4-byte length field.
func TestTZX_Block35_CustomInfo(t *testing.T) {
	// 16-byte ID + 4-byte length (4) + 4 payload bytes.
	payload := []byte{0x35}
	for i := 0; i < 16; i++ {
		payload = append(payload, byte('A'+i))
	}
	payload = append(payload, 0x04, 0x00, 0x00, 0x00)
	payload = append(payload, 0xAA, 0xBB, 0xCC, 0xDD)
	blockSandwich(t, "0x35 CustomInfo", payload)
}

// TestTZX_Block5A_Glue verifies skip 9 bytes (block 0x5A is a "glue"
// marker that joins concatenated TZX files; tape data follows).
func TestTZX_Block5A_Glue(t *testing.T) {
	blockSandwich(t, "0x5A Glue", []byte{0x5A, 'X', 'T', 'a', 'p', 'e', '!', 0x1A, 1, 20})
}

// TestTZX_UnknownBlock_SkippedViaLengthField verifies the default-case
// skip path: an unknown block ID with a leading 4-byte little-endian
// length is silently advanced past, so subsequent standard blocks still
// parse.
func TestTZX_UnknownBlock_SkippedViaLengthField(t *testing.T) {
	blockSandwich(t, "unknown 0x99", []byte{
		0x99,                   // unknown ID
		0x04, 0x00, 0x00, 0x00, // payload length = 4
		0xDE, 0xAD, 0xBE, 0xEF,
	})
}

// TestTZX_NoLoadableBlocks_Errors verifies a TZX containing only
// non-loadable blocks (e.g. all 0x20 pause blocks) returns an error
// rather than silently producing an empty tape.
func TestTZX_NoLoadableBlocks_Errors(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x20, 0xE8, 0x03)
	b.raw(0x20, 0x10, 0x00)
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.tzx")
	if err := os.WriteFile(p, b.buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	err := tp.LoadTZX(p)
	if err == nil {
		t.Error("expected error from TZX with no loadable blocks, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "no loadable") {
		t.Errorf("wrong error: %v", err)
	}
}

// TestTZX_TruncatedBlock_HaltsCleanly verifies that a TZX with a
// 0x10 block whose declared length exceeds the file's remaining bytes
// doesn't panic — the parser stops at parseLoop without consuming
// bogus data.
func TestTZX_TruncatedBlock_HaltsCleanly(t *testing.T) {
	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA}) // valid first block
	// Truncated second 0x10 block: claims 1000 bytes, file ends.
	b.raw(0x10, 0x00, 0x00, 0xE8, 0x03) // pause + length(1000)
	dir := t.TempDir()
	p := filepath.Join(dir, "trunc.tzx")
	if err := os.WriteFile(p, b.buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTZX(p); err != nil {
		t.Fatalf("LoadTZX: %v", err)
	}
	if got := tp.BlockCount(); got != 1 {
		t.Errorf("blocks = %d, want 1 (only the first complete block)", got)
	}
}
