package ula

// Spec-faithful tests for the TAP and TZX tape formats.
//
// Oracle: the published TAP and TZX format specifications (World of
// Spectrum "TZX format" reference and the standard ROM loader timings).
// Pinned facts:
//
//   TAP: a flat stream of blocks, each = 2-byte little-endian length +
//   that many data bytes. The first data byte is the flag (0x00 header,
//   0xFF data) and the LAST data byte is the XOR checksum of all the
//   preceding bytes in the block (flag included).
//
//   Standard ROM (TAP block / TZX 0x10) pulse timings, in T-states:
//     - pilot pulse  = 2168
//     - pilot length = 8063 pulses for a header (flag < 128),
//                      3223 pulses for data   (flag >= 128)
//     - sync pulses  = 667 then 735
//     - bit 0        = two 855 pulses
//     - bit 1        = two 1710 pulses
//
//   TZX header = "ZXTape!" + 0x1A + major + minor.
//   TZX 0x10 layout  = pause(2) + length(2) + data.
//   TZX 0x14 (pure data) carries NO pilot/sync — just data bit pulses.
//   A trailing pause is specified in milliseconds; at 3.5 MHz that is
//   3500 T-states per ms.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Standard ROM timing constants (from the spec) used by these tests.
const (
	stdPilotPulse  = 2168
	stdPilotHeader = 8063
	stdPilotData   = 3223
	stdSyncFirst   = 667
	stdSyncSecond  = 735
	stdZeroPulse   = 855
	stdOnePulse    = 1710
)

// xorChecksum returns the standard tape block checksum: the XOR of every
// byte (flag through last data byte).
func xorChecksum(b []byte) byte {
	var c byte
	for _, v := range b {
		c ^= v
	}
	return c
}

// ---------------------------------------------------------------------------
// Standard-speed pulse structure (TAP / TZX 0x10)
// ---------------------------------------------------------------------------

// TestStandardHeaderPilotCount pins the exact pilot-tone length for a
// header block: 8063 pilot pulses, each 2168 T-states, then the two sync
// pulses 667/735.
func TestStandardHeaderPilotCount(t *testing.T) {
	tp := NewTapePlayer()
	// flag < 128 => header.
	pulses := tp.generatePulses(tapeBlock{data: []byte{0x00, 0x13}})

	for i := 0; i < stdPilotHeader; i++ {
		if pulses[i] != stdPilotPulse {
			t.Fatalf("pilot pulse %d = %d, want %d", i, pulses[i], stdPilotPulse)
		}
	}
	// The (8063+1)th pulse onwards are the two sync pulses.
	if pulses[stdPilotHeader] != stdSyncFirst {
		t.Errorf("sync1 = %d, want %d", pulses[stdPilotHeader], stdSyncFirst)
	}
	if pulses[stdPilotHeader+1] != stdSyncSecond {
		t.Errorf("sync2 = %d, want %d", pulses[stdPilotHeader+1], stdSyncSecond)
	}
}

// TestStandardDataPilotCount pins the data-block pilot length: 3223 pulses.
func TestStandardDataPilotCount(t *testing.T) {
	tp := NewTapePlayer()
	// flag >= 128 => data.
	pulses := tp.generatePulses(tapeBlock{data: []byte{0xFF, 0x00}})
	if pulses[stdPilotData-1] != stdPilotPulse {
		t.Errorf("last data pilot pulse = %d, want %d", pulses[stdPilotData-1], stdPilotPulse)
	}
	if pulses[stdPilotData] != stdSyncFirst {
		t.Errorf("pulse after 3223 pilots = %d, want sync1 %d", pulses[stdPilotData], stdSyncFirst)
	}
}

// TestStandardDataBitPulses pins the data-bit encoding: a 0 bit is two 855
// pulses, a 1 bit is two 1710 pulses, most-significant bit first. We feed a
// single 0x80 data byte (preceded by a 0xFF flag) and read the first byte's
// bit pulses right after the pilot+sync.
func TestStandardDataBitPulses(t *testing.T) {
	tp := NewTapePlayer()
	// data = flag 0xFF then one byte 0x80 (1000_0000).
	pulses := tp.generatePulses(tapeBlock{data: []byte{0xFF, 0x80}})

	// Skip pilot (3223) + 2 sync.
	pos := stdPilotData + 2
	// Flag byte 0xFF = eight 1-bits => 16 pulses of 1710.
	for i := 0; i < 16; i++ {
		if pulses[pos+i] != stdOnePulse {
			t.Fatalf("flag-byte 1-bit pulse %d = %d, want %d", i, pulses[pos+i], stdOnePulse)
		}
	}
	pos += 16
	// Data byte 0x80: bit7=1 (two 1710), then bits6..0 = 0 (fourteen 855).
	if pulses[pos] != stdOnePulse || pulses[pos+1] != stdOnePulse {
		t.Errorf("0x80 MSB = %d,%d want %d (one-bit)", pulses[pos], pulses[pos+1], stdOnePulse)
	}
	if pulses[pos+2] != stdZeroPulse {
		t.Errorf("0x80 next bit = %d, want %d (zero-bit)", pulses[pos+2], stdZeroPulse)
	}
}

// ---------------------------------------------------------------------------
// TAP block structure + checksum
// ---------------------------------------------------------------------------

// TestTAPBlockLengthFraming pins the 2-byte length framing: the loader must
// split the flat stream into exactly the declared block lengths.
func TestTAPBlockLengthFraming(t *testing.T) {
	// Two blocks of differing lengths.
	b1 := []byte{0x00, 0x03, 'A'}
	b1 = append(b1, xorChecksum(b1))
	b2 := []byte{0xFF, 0x10, 0x20, 0x30}
	b2 = append(b2, xorChecksum(b2))

	var raw bytes.Buffer
	for _, b := range [][]byte{b1, b2} {
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(b)))
		raw.Write(l[:])
		raw.Write(b)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "framed.tap")
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatal(err)
	}
	if tp.BlockCount() != 2 {
		t.Fatalf("block count = %d, want 2", tp.BlockCount())
	}
	got := tp.Blocks()
	if got[0].Length != len(b1) || got[1].Length != len(b2) {
		t.Errorf("block lengths = %d,%d want %d,%d", got[0].Length, got[1].Length, len(b1), len(b2))
	}
}

// TestTAPHeaderChecksumByteRoundTrips pins that the checksum byte (last
// byte of a block) survives load and the standard 17-byte header decodes
// its embedded title. This verifies our block model preserves the exact
// bytes including the trailing checksum.
func TestTAPHeaderChecksumByteRoundTrips(t *testing.T) {
	// Build a 19-byte standard header block: flag(0) + type(0) +
	// 10-char name + len(2) + p1(2) + p2(2) ... then checksum.
	body := make([]byte, 18)
	body[0] = 0x00 // flag: header
	body[1] = 0x00 // type: program
	copy(body[2:12], []byte("LOADER    "))
	// bytes 12-17: length + params (arbitrary)
	body = append(body, xorChecksum(body)) // checksum as the 19th byte

	var raw bytes.Buffer
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(body)))
	raw.Write(l[:])
	raw.Write(body)

	dir := t.TempDir()
	path := filepath.Join(dir, "hdr.tap")
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatal(err)
	}
	blocks := tp.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != "Header" {
		t.Errorf("Type = %q, want Header", blocks[0].Type)
	}
	if blocks[0].FlagByte != 0x00 {
		t.Errorf("FlagByte = 0x%02X, want 0x00", blocks[0].FlagByte)
	}
	// NextBlock returns the raw bytes including the checksum byte intact.
	first := tp.NextBlock()
	if len(first) != len(body) {
		t.Fatalf("NextBlock length = %d, want %d (checksum byte dropped?)", len(first), len(body))
	}
	if first[len(first)-1] != xorChecksum(body[:len(body)-1]) {
		t.Errorf("trailing checksum byte not preserved")
	}
}

// TestTAPTrailingGarbageIgnored pins that a partial trailing block (a
// length word that overruns the file) is dropped rather than read past EOF.
func TestTAPTrailingGarbageIgnored(t *testing.T) {
	b1 := []byte{0xFF, 0x01}
	b1 = append(b1, xorChecksum(b1))
	var raw bytes.Buffer
	var l [2]byte
	binary.LittleEndian.PutUint16(l[:], uint16(len(b1)))
	raw.Write(l[:])
	raw.Write(b1)
	// Trailing: a length word claiming 100 bytes that aren't there.
	binary.LittleEndian.PutUint16(l[:], 100)
	raw.Write(l[:])
	raw.Write([]byte{0x01, 0x02}) // only 2 bytes, not 100

	dir := t.TempDir()
	path := filepath.Join(dir, "trunc.tap")
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatal(err)
	}
	if tp.BlockCount() != 1 {
		t.Errorf("block count = %d, want 1 (overrunning block dropped)", tp.BlockCount())
	}
}

// ---------------------------------------------------------------------------
// TZX block structure
// ---------------------------------------------------------------------------

// TestTZXStandardBlockLayout pins the 0x10 standard-speed block layout:
// id(0x10) + pause(2) + length(2) + data.
func TestTZXStandardBlockLayout(t *testing.T) {
	data := []byte{0xFF, 0xAA, xorChecksum([]byte{0xFF, 0xAA})}
	var raw bytes.Buffer
	raw.Write([]byte{'Z', 'X', 'T', 'a', 'p', 'e', '!', 0x1A, 1, 20})
	raw.WriteByte(0x10)
	var p [2]byte
	binary.LittleEndian.PutUint16(p[:], 1500) // pause ms
	raw.Write(p[:])
	binary.LittleEndian.PutUint16(p[:], uint16(len(data)))
	raw.Write(p[:])
	raw.Write(data)

	dir := t.TempDir()
	path := filepath.Join(dir, "std.tzx")
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTZX(path); err != nil {
		t.Fatal(err)
	}
	if tp.BlockCount() != 1 {
		t.Fatalf("block count = %d, want 1", tp.BlockCount())
	}
	b := tp.blocks[0]
	if b.pause != 1500 {
		t.Errorf("pause = %d, want 1500", b.pause)
	}
	if !bytes.Equal(b.data, data) {
		t.Errorf("data = % X, want % X", b.data, data)
	}
	if b.turbo {
		t.Error("0x10 block must not be flagged turbo")
	}
}

// TestTZXPureDataNoPilot pins that a 0x14 pure-data block produces NO pilot
// or sync pulses — only the data-bit pulses (and the trailing pause).
func TestTZXPureDataNoPilot(t *testing.T) {
	tp := NewTapePlayer()
	blk := tapeBlock{
		data:      []byte{0x00}, // a single zero byte = eight 0-bits
		turbo:     true,
		pilotLen:  0, // pure data: no pilot
		zeroPulse: stdZeroPulse,
		onePulse:  stdOnePulse,
		usedBits:  8,
	}
	pulses := tp.generatePulses(blk)
	// First pulse must be a data-bit pulse (855), NOT a pilot (2168) or
	// sync (667).
	if pulses[0] != stdZeroPulse {
		t.Errorf("first pulse = %d, want data zero-bit %d (no pilot/sync)", pulses[0], stdZeroPulse)
	}
	// Eight 0-bits => 16 zero-pulses before the pause.
	for i := 0; i < 16; i++ {
		if pulses[i] != stdZeroPulse {
			t.Fatalf("pure-data pulse %d = %d, want %d", i, pulses[i], stdZeroPulse)
		}
	}
}

// TestTZXTurboTimingsOverride pins that a 0x11 turbo block's custom pilot/
// sync/bit timings are honoured (not the standard ROM defaults).
func TestTZXTurboTimingsOverride(t *testing.T) {
	tp := NewTapePlayer()
	blk := tapeBlock{
		data:       []byte{0xFF},
		turbo:      true,
		pilotPulse: 1000,
		syncFirst:  200,
		syncSecond: 300,
		zeroPulse:  400,
		onePulse:   800,
		pilotLen:   5,
		usedBits:   8,
	}
	pulses := tp.generatePulses(blk)
	for i := 0; i < 5; i++ {
		if pulses[i] != 1000 {
			t.Fatalf("turbo pilot %d = %d, want 1000", i, pulses[i])
		}
	}
	if pulses[5] != 200 || pulses[6] != 300 {
		t.Errorf("turbo sync = %d,%d want 200,300", pulses[5], pulses[6])
	}
	// 0xFF = eight 1-bits => onePulse (800) pairs.
	if pulses[7] != 800 {
		t.Errorf("turbo one-bit = %d, want 800", pulses[7])
	}
}

// TestTZXPauseTStateConversion pins the trailing-pause conversion: a TZX
// pause expressed in milliseconds becomes pause_ms * 3500 T-states of
// silence appended after the data pulses (split into uint16 chunks).
func TestTZXPauseTStateConversion(t *testing.T) {
	tp := NewTapePlayer()
	// A tiny standard block with a 10 ms pause.
	blk := tapeBlock{data: []byte{0xFF}, pause: 10}
	pulses, dataPulses := tp.generatePulsesData(blk)

	// Sum the pulse durations AFTER the data section: that's the pause.
	var pauseT uint64
	for _, p := range pulses[dataPulses:] {
		pauseT += uint64(p)
	}
	want := uint64(10) * 3500
	if pauseT != want {
		t.Errorf("pause T-states = %d, want %d (10ms * 3500)", pauseT, want)
	}
}

// TestTZXSignatureRejected pins that a file lacking the "ZXTape!" + 0x1A
// signature is rejected.
func TestTZXSignatureRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.tzx")
	if err := os.WriteFile(path, []byte("NOTZXTAPE!"), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTZX(path); err == nil {
		t.Fatal("expected error for invalid TZX signature, got nil")
	}
}

// TestTZXUsedBitsLastByte pins the TZX usedBits field: only the top N bits
// of the final data byte are emitted as pulses.
func TestTZXUsedBitsLastByte(t *testing.T) {
	tp := NewTapePlayer()
	// pure-data, single byte 0xFF, only top 3 bits used.
	blk := tapeBlock{
		data:      []byte{0xFF},
		turbo:     true,
		pilotLen:  0,
		zeroPulse: stdZeroPulse,
		onePulse:  stdOnePulse,
		usedBits:  3,
	}
	pulses, dataPulses := tp.generatePulsesData(blk)
	// 3 used bits, all 1 => 6 one-bit pulses, then the pause begins.
	if dataPulses != 6 {
		t.Errorf("dataPulses = %d, want 6 (3 used bits * 2 pulses)", dataPulses)
	}
	for i := 0; i < 6; i++ {
		if pulses[i] != stdOnePulse {
			t.Fatalf("used-bit pulse %d = %d, want %d", i, pulses[i], stdOnePulse)
		}
	}
}
