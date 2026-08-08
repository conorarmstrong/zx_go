package ula

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"testing"
)

// TZX 0x18 (CSW recording) and 0x19 (generalised data block) were the last
// two block types with no case at all: the parser skipped them via their
// length field, so any tape relying on them lost that content silently.

// cswBlock assembles a 0x18 block around an already-encoded CSW stream.
func cswBlock(sampleRate int, compression byte, pulses uint32, data []byte, pauseMS uint16) []byte {
	body := new(bytes.Buffer)
	_ = binary.Write(body, binary.LittleEndian, pauseMS)
	body.Write([]byte{byte(sampleRate), byte(sampleRate >> 8), byte(sampleRate >> 16)})
	body.WriteByte(compression)
	_ = binary.Write(body, binary.LittleEndian, pulses)
	body.Write(data)

	out := new(bytes.Buffer)
	out.WriteByte(0x18)
	_ = binary.Write(out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// TestTZXCSWRLEPulses pins the CSW RLE stream: each byte is a pulse length in
// SAMPLES, and a zero byte escapes to a following 32-bit length. Lengths are
// converted to T-states against the sampling rate.
func TestTZXCSWRLEPulses(t *testing.T) {
	// 44100 Hz: one sample is 3500000/44100 = 79 T-states (truncated).
	const rate = 44100
	stream := []byte{
		2, 4, // 2 and 4 samples
		0, 0x00, 0x01, 0x00, 0x00, // escape: 256 samples
	}
	b := newTZXBuilder()
	b.raw(cswBlock(rate, 0x01, 3, stream, 0)...)
	tp := loadTZXBytes(t, b)

	if tp.BlockCount() != 1 {
		t.Fatalf("block count = %d, want 1", tp.BlockCount())
	}
	pulses := tp.generatePulses(tp.blocks[0])
	perSample := uint32(3500000 / rate)
	want := []uint32{2 * perSample, 4 * perSample, 256 * perSample}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Errorf("pulse %d = %d, want %d", i, pulses[i], want[i])
		}
	}
}

// TestTZXCSWZRLEPulses pins the Z-RLE variant: the same stream, deflated.
func TestTZXCSWZRLEPulses(t *testing.T) {
	const rate = 44100
	raw := []byte{3, 5, 7}

	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b := newTZXBuilder()
	b.raw(cswBlock(rate, 0x02, 3, z.Bytes(), 0)...)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	perSample := uint32(3500000 / rate)
	want := []uint32{3 * perSample, 5 * perSample, 7 * perSample}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Errorf("pulse %d = %d, want %d", i, pulses[i], want[i])
		}
	}
}

// TestTZXCSWHonoursPause pins that the block's trailing pause is played.
func TestTZXCSWHonoursPause(t *testing.T) {
	b := newTZXBuilder()
	b.raw(cswBlock(44100, 0x01, 1, []byte{10}, 100)...)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	if len(pulses) != 2 {
		t.Fatalf("pulses = %v, want one pulse plus one pause", pulses)
	}
	if want := uint32(100 * 3500); pulses[1] != want {
		t.Errorf("pause = %d T-states, want %d", pulses[1], want)
	}
}

// TestTZXCSWBadCompressionIsSkippedNotFatal pins that an unknown compression
// type leaves the rest of the tape parseable rather than aborting the load.
func TestTZXCSWBadCompressionIsSkippedNotFatal(t *testing.T) {
	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA})
	b.raw(cswBlock(44100, 0x7F, 1, []byte{1, 2, 3}, 0)...)
	b.standardBlock([]byte{0xBB})
	tp := loadTZXBytes(t, b)

	if got := tp.BlockCount(); got != 2 {
		t.Errorf("block count = %d, want 2 (the bad CSW block contributes nothing)", got)
	}
}

// TestTZXCSWCorruptZRLEIsSkipped pins the same for a Z-RLE payload that does
// not inflate.
func TestTZXCSWCorruptZRLEIsSkipped(t *testing.T) {
	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA})
	b.raw(cswBlock(44100, 0x02, 1, []byte{0xDE, 0xAD, 0xBE, 0xEF}, 0)...)
	b.standardBlock([]byte{0xBB})
	tp := loadTZXBytes(t, b)

	if got := tp.BlockCount(); got != 2 {
		t.Errorf("block count = %d, want 2", got)
	}
}
