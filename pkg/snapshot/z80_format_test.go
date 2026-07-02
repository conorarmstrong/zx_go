package snapshot

// Spec-faithful tests for the .Z80 snapshot format.
//
// Oracle: the published .Z80 format specification (World of Spectrum /
// Sinclair Wiki "Z80 format" reference). Pinned facts:
//
//   - RLE compression: a run of >=5 identical bytes is coded as the
//     4-byte sequence ED ED <count> <value>. EXCEPTION: a run of ED
//     bytes is coded this way even at length >=2, because a literal ED
//     followed by another ED would otherwise look like the start of a
//     code. A single ED is stored verbatim (it does not start a code).
//   - Version 1 compressed data ends with the marker 00 ED ED 00.
//   - Version detection: PC at header bytes 6-7 != 0 => version 1
//     (uncompressed/RLE memory follows). PC == 0 => version 2/3, with a
//     2-byte additional-header length: 23 => v2, 54 or 55 => v3.
//   - Version-1 flags byte (offset 12): bit 0 = bit 7 of R, bits 1-3 =
//     border, bit 5 = memory is compressed. A value of 255 is treated
//     as 1 for compatibility with very old files.
//   - Hardware-mode byte: v2 mode 3/4 = 128K; v3 modes 4/5 (128K,
//     128K+IF1), 7/8 (+3, including the "bugged" variant), and 12/13
//     (+2, +2A) are 128K-family. Other 128K-capable hosts the emulator
//     doesn't model (Pentagon, Scorpion, 128K+MGT) fall back to 48K.
//   - Memory blocks (v2/v3): block length 0xFFFF is a sentinel meaning
//     "16384 raw uncompressed bytes follow". Page numbers map to banks:
//     48K uses pages 8/4/5 -> banks 5/2/0; 128K uses page = bank + 3.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// Z80 RLE compression (the ED ED run-length codec)
// ---------------------------------------------------------------------------

// TestZ80CompressRunOf5Encodes pins that exactly 5 identical bytes become
// a single ED ED 05 <value> code.
func TestZ80CompressRunOf5Encodes(t *testing.T) {
	s := New()
	in := bytes.Repeat([]byte{0xAB}, 5)
	got := s.compressZ80(in)
	want := []byte{0xED, 0xED, 0x05, 0xAB}
	if !bytes.Equal(got, want) {
		t.Errorf("compress(5xAB) = % X, want % X", got, want)
	}
}

// TestZ80CompressRunOf4StaysLiteral pins that a run of 4 (below the
// threshold) is NOT compressed — the spec only codes runs of >=5.
func TestZ80CompressRunOf4StaysLiteral(t *testing.T) {
	s := New()
	in := bytes.Repeat([]byte{0xAB}, 4)
	got := s.compressZ80(in)
	if !bytes.Equal(got, in) {
		t.Errorf("compress(4xAB) = % X, want literal % X", got, in)
	}
}

// TestZ80CompressTwoEDsEncoded pins the ED special case: even a run of two
// ED bytes must be coded as ED ED 02 ED (a literal ED ED would be
// mis-parsed as the start of a run code).
func TestZ80CompressTwoEDsEncoded(t *testing.T) {
	s := New()
	got := s.compressZ80([]byte{0xED, 0xED})
	want := []byte{0xED, 0xED, 0x02, 0xED}
	if !bytes.Equal(got, want) {
		t.Errorf("compress(ED ED) = % X, want % X", got, want)
	}
}

// TestZ80CompressLongRunSplitsAt255 pins that runs longer than 255 are
// split across multiple codes (the count field is a single byte).
func TestZ80CompressLongRunSplitsAt255(t *testing.T) {
	s := New()
	in := bytes.Repeat([]byte{0x00}, 300)
	got := s.compressZ80(in)
	want := []byte{0xED, 0xED, 0xFF, 0x00, 0xED, 0xED, 0x2D, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("compress(300x00) = % X, want % X", got, want)
	}
}

// TestZ80DecompressCanonicalSingleED pins decoding of the spec's canonical
// "literal ED then run" example: ED 00 ED ED 05 00 => ED + six 00 bytes.
// Other emulators emit this exact encoding, so our DECOMPRESSOR must
// accept it even though our COMPRESSOR chooses a different (also-valid)
// encoding.
func TestZ80DecompressCanonicalSingleED(t *testing.T) {
	s := New()
	got, err := s.decompressZ80([]byte{0xED, 0x00, 0xED, 0xED, 0x05, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xED}, bytes.Repeat([]byte{0x00}, 6)...)
	if !bytes.Equal(got, want) {
		t.Errorf("decompress(canonical) = % X, want % X", got, want)
	}
}

// TestZ80DecompressTrailingLiteralED pins that a single ED at the very end
// of the stream is treated as a literal (it cannot start a 4-byte code).
func TestZ80DecompressTrailingLiteralED(t *testing.T) {
	s := New()
	got, err := s.decompressZ80([]byte{0x01, 0x02, 0xED})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{0x01, 0x02, 0xED}) {
		t.Errorf("decompress(.. ED) = % X, want 01 02 ED", got)
	}
}

// TestZ80DecompressZeroCountEmitsNothing pins the strict spec reading of a
// zero repeat count: ED ED 00 yy means "yy repeated 0 times", so it emits
// nothing and consumes all four code bytes. A compliant compressor never
// produces this; the case is decoded for robustness only.
func TestZ80DecompressZeroCountEmitsNothing(t *testing.T) {
	s := New()
	got, err := s.decompressZ80([]byte{0xED, 0xED, 0x00, 0xAB, 0x42})
	if err != nil {
		t.Fatal(err)
	}
	// ED ED 00 AB -> nothing; trailing 0x42 -> literal.
	want := []byte{0x42}
	if !bytes.Equal(got, want) {
		t.Errorf("decompress(ED ED 00 AB 42) = % X, want % X", got, want)
	}
}

// TestZ80CompressDecompressFuzzRoundtrip exercises the codec over a range
// of ED-heavy and run-heavy patterns; every pattern must survive a
// compress->decompress round-trip byte-for-byte.
func TestZ80CompressDecompressFuzzRoundtrip(t *testing.T) {
	s := New()
	patterns := [][]byte{
		{},
		{0xED},
		{0xED, 0xED, 0xED},
		bytes.Repeat([]byte{0xED}, 260),
		append(append([]byte{0xED}, bytes.Repeat([]byte{0x00}, 7)...), 0xED),
		bytes.Repeat([]byte{0xFF}, 1000),
		{0x00, 0x00, 0x00, 0x00, 0xED, 0xED, 0x12, 0x34, 0x12, 0x34},
	}
	for i, p := range patterns {
		comp := s.compressZ80(p)
		out, err := s.decompressZ80(comp)
		if err != nil {
			t.Fatalf("pattern %d: %v", i, err)
		}
		if !bytes.Equal(out, p) {
			t.Errorf("pattern %d roundtrip mismatch: in=% X out=% X", i, p, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Z80 version 1 end-marker + flags byte
// ---------------------------------------------------------------------------

// TestZ80V1EndMarkerStripped builds a compressed v1 file ending in the
// 00 ED ED 00 marker and confirms the marker is NOT decoded into RAM.
func TestZ80V1EndMarkerStripped(t *testing.T) {
	hdr := make([]byte, Z80_V1_HEADER_SIZE)
	binary.LittleEndian.PutUint16(hdr[6:8], 0x8000) // PC != 0 => v1
	hdr[12] = 0x20                                  // bit 5 set: compressed

	// 49152 bytes of 0xAA, RLE-compressed, then the v1 end marker.
	s0 := New()
	body := s0.compressZ80(bytes.Repeat([]byte{0xAA}, 49152))
	body = append(body, 0x00, 0xED, 0xED, 0x00) // v1 end marker

	data := append(append([]byte{}, hdr...), body...)
	s := New()
	if err := s.LoadBytes(data, FormatZ80); err != nil {
		t.Fatal(err)
	}
	// Every RAM byte in the 48K image must be 0xAA — the marker must not
	// have leaked any bytes into RAM.
	for _, bank := range []int{5, 2, 0} {
		for i, b := range s.Memory.RAM[bank] {
			if b != 0xAA {
				t.Fatalf("bank %d byte %d = 0x%02X, want 0xAA (end marker leaked?)", bank, i, b)
			}
		}
	}
}

// TestZ80V1FlagsByteBitLayout pins the flags byte decode: bit 0 -> R bit 7,
// bits 1-3 -> border.
func TestZ80V1FlagsByteBitLayout(t *testing.T) {
	hdr := make([]byte, Z80_V1_HEADER_SIZE)
	binary.LittleEndian.PutUint16(hdr[6:8], 0x4000) // v1
	hdr[11] = 0x00                                  // R low 7 bits
	// flags: bit0=1 (R bit7), border=6 (bits1-3 = 110), bit5=0 (uncompressed)
	hdr[12] = 0x01 | (6 << 1)
	body := make([]byte, 49152) // uncompressed
	data := append(append([]byte{}, hdr...), body...)

	s := New()
	if err := s.LoadBytes(data, FormatZ80); err != nil {
		t.Fatal(err)
	}
	if s.CPU.R&0x80 == 0 {
		t.Errorf("R bit7 = 0, want 1 (from flags bit0); R=0x%02X", s.CPU.R)
	}
	if s.CPU.BorderColor != 6 {
		t.Errorf("border = %d, want 6", s.CPU.BorderColor)
	}
}

// TestZ80V1FlagsByte255TreatedAs1 pins the legacy compatibility rule that
// a flags byte of 255 is treated as 1.
func TestZ80V1FlagsByte255TreatedAs1(t *testing.T) {
	hdr := make([]byte, Z80_V1_HEADER_SIZE)
	binary.LittleEndian.PutUint16(hdr[6:8], 0x4000) // v1
	hdr[11] = 0x00
	hdr[12] = 0xFF // 255 => treat as 1 (so border=0, R bit7=1, uncompressed)
	body := make([]byte, 49152)
	data := append(append([]byte{}, hdr...), body...)

	s := New()
	if err := s.LoadBytes(data, FormatZ80); err != nil {
		t.Fatal(err)
	}
	if s.CPU.BorderColor != 0 {
		t.Errorf("border = %d, want 0 (255 treated as flags=1)", s.CPU.BorderColor)
	}
	if s.CPU.R&0x80 == 0 {
		t.Errorf("R bit7 = 0, want 1 (255 treated as flags=1)")
	}
}

// ---------------------------------------------------------------------------
// Z80 version detection (v1 vs v2 vs v3) and hardware-mode byte
// ---------------------------------------------------------------------------

// TestZ80V2AdditionalHeaderLength23 pins that an additional-header length
// of 23 selects version-2 semantics (mode 3/4 = 128K).
func TestZ80V2AdditionalHeaderLength23(t *testing.T) {
	s := buildZ80V2V3(t, 23, 3 /*mode 3 = 128K in v2*/, 0x10)
	if !s.Memory.Is128K {
		t.Error("v2 mode 3 must be detected as 128K")
	}
}

// TestZ80V2Mode0Is48K pins v2 mode 0 = plain 48K.
func TestZ80V2Mode0Is48K(t *testing.T) {
	s := buildZ80V2V3(t, 23, 0, 0)
	if s.Memory.Is128K {
		t.Error("v2 mode 0 must be 48K")
	}
}

// TestZ80V3Mode4Is128K pins v3 mode 4 = 128K (the meaning of mode 3/4
// differs between v2 and v3).
func TestZ80V3Mode4Is128K(t *testing.T) {
	s := buildZ80V2V3(t, 54, Z80_HW_128K, 0x10)
	if !s.Memory.Is128K {
		t.Error("v3 mode 4 must be detected as 128K")
	}
}

// TestZ80V3Mode3Is48K pins that v3 mode 3 (48K+MGT) is NOT 128K — the
// classic v2/v3 divergence trap.
func TestZ80V3Mode3Is48K(t *testing.T) {
	s := buildZ80V2V3(t, 54, 3, 0)
	if s.Memory.Is128K {
		t.Error("v3 mode 3 must be 48K (not 128K like v2 mode 3)")
	}
}

// TestZ80V3Plus3Is128K pins that the +3 hardware mode is treated as
// 128K-family.
func TestZ80V3Plus3Is128K(t *testing.T) {
	s := buildZ80V2V3(t, 54, Z80_HW_PLUS3, 0x10)
	if !s.Memory.Is128K {
		t.Error("v3 +3 must be 128K-family")
	}
}

// TestZ80V3Plus3ModeValueIs7 pins the on-disk value of the +3 hardware
// mode byte to 7, per the .z80 v3 specification (values 7 and 8 both
// denote a Spectrum +3; 8 is the "bugged" variant some old tools wrote).
// Mode 14 is a different, non-128K machine (TC2048) and must not be
// mistaken for +3.
func TestZ80V3Plus3ModeValueIs7(t *testing.T) {
	if Z80_HW_PLUS3 != 7 {
		t.Fatalf("Z80_HW_PLUS3 = %d, want 7", Z80_HW_PLUS3)
	}

	s := buildZ80V2V3(t, 54, 7, 0x10)
	if !s.Memory.Is128K {
		t.Error("v3 mode 7 (+3) must be 128K-family")
	}

	sBugged := buildZ80V2V3(t, 54, 8, 0x10)
	if !sBugged.Memory.Is128K {
		t.Error("v3 mode 8 (+3, bugged variant) must be 128K-family")
	}
}

// TestZ80V3Mode14IsTC2048Not128K pins that hardware mode 14 (TC2048, a
// 48K-RAM Timex clone with no 128K-style bank switching) is NOT
// classified as 128K — it must not be confused with the +3 mode.
func TestZ80V3Mode14IsTC2048Not128K(t *testing.T) {
	s := buildZ80V2V3(t, 54, 14, 0)
	if s.Memory.Is128K {
		t.Error("v3 mode 14 (TC2048) must not be treated as 128K-family")
	}
}

// TestZ80V3HeaderLength55Accepted pins that the 55-byte v3 additional
// header (the +3 variant) is also accepted as version 3.
func TestZ80V3HeaderLength55Accepted(t *testing.T) {
	s := buildZ80V2V3(t, 55, Z80_HW_128K, 0x10)
	if !s.Memory.Is128K {
		t.Error("v3 (len 55) mode 4 must be 128K")
	}
}

// TestZ80V2V3UncompressedSentinelBlock pins the 0xFFFF block-length
// sentinel: it means "16384 raw bytes follow", not a literal length.
func TestZ80V2V3UncompressedSentinelBlock(t *testing.T) {
	var buf bytes.Buffer
	// v1 header with PC=0 => version 2/3.
	hdr := make([]byte, Z80_V1_HEADER_SIZE)
	buf.Write(hdr)
	// additional header length 23 (v2), mode 0 (48K).
	var lenField [2]byte
	binary.LittleEndian.PutUint16(lenField[:], 23)
	buf.Write(lenField[:])
	ext := make([]byte, 23)
	binary.LittleEndian.PutUint16(ext[0:2], 0x6000) // PC
	ext[2] = 0                                      // 48K
	buf.Write(ext)

	// One memory block: page 8 (=> bank 5), length 0xFFFF (raw 16384).
	raw := bytes.Repeat([]byte{0x5A}, 16384)
	var blkHdr [3]byte
	binary.LittleEndian.PutUint16(blkHdr[0:2], 0xFFFF) // sentinel
	blkHdr[2] = 8                                      // page 8 -> bank 5
	buf.Write(blkHdr[:])
	buf.Write(raw)

	s := New()
	if err := s.LoadBytes(buf.Bytes(), FormatZ80); err != nil {
		t.Fatal(err)
	}
	if s.CPU.PC != 0x6000 {
		t.Errorf("PC = 0x%04X, want 0x6000", s.CPU.PC)
	}
	for i, b := range s.Memory.RAM[5] {
		if b != 0x5A {
			t.Fatalf("bank 5 byte %d = 0x%02X, want 0x5A (sentinel mis-read?)", i, b)
		}
	}
}

// TestZ80V2V3TruncatedTrailingBlockHeaderErrors pins that a file cut off
// partway through a memory-block header (1 or 2 stray bytes after the
// last complete block, rather than a clean end-of-file) is reported as
// an error instead of being silently accepted as if those bytes were
// never there.
func TestZ80V2V3TruncatedTrailingBlockHeaderErrors(t *testing.T) {
	var buf bytes.Buffer
	hdr := make([]byte, Z80_V1_HEADER_SIZE)
	buf.Write(hdr)
	var lenField [2]byte
	binary.LittleEndian.PutUint16(lenField[:], 23)
	buf.Write(lenField[:])
	ext := make([]byte, 23)
	binary.LittleEndian.PutUint16(ext[0:2], 0x6000) // PC
	ext[2] = 0                                      // 48K
	buf.Write(ext)

	// One complete, valid memory block.
	raw := bytes.Repeat([]byte{0x5A}, 16384)
	var blkHdr [3]byte
	binary.LittleEndian.PutUint16(blkHdr[0:2], 0xFFFF) // sentinel
	blkHdr[2] = 8                                      // page 8 -> bank 5
	buf.Write(blkHdr[:])
	buf.Write(raw)

	// A stray 2-byte fragment of a would-be next block header: not a
	// clean EOF, and not a full 3-byte header either.
	buf.Write([]byte{0x01, 0x02})

	s := New()
	if err := s.LoadBytes(buf.Bytes(), FormatZ80); err == nil {
		t.Fatal("LoadBytes: want error for a truncated trailing block header, got nil")
	}
}

// buildZ80V2V3 assembles a minimal v2/v3 file (one compressed bank) and
// returns the loaded snapshot. addlLen selects the version (23=v2,
// 54/55=v3); hwMode is the hardware-mode byte; port7FFD is stored when 128K.
func buildZ80V2V3(t *testing.T, addlLen int, hwMode, port7FFD byte) *Snapshot {
	t.Helper()
	var buf bytes.Buffer
	hdr := make([]byte, Z80_V1_HEADER_SIZE) // PC bytes 6-7 stay 0 => v2/v3
	buf.Write(hdr)

	var lenField [2]byte
	binary.LittleEndian.PutUint16(lenField[:], uint16(addlLen))
	buf.Write(lenField[:])

	ext := make([]byte, addlLen)
	binary.LittleEndian.PutUint16(ext[0:2], 0x7000) // PC
	ext[2] = hwMode
	if addlLen > 3 {
		ext[3] = port7FFD
	}
	buf.Write(ext)

	// One memory block (page 8 -> bank 5 in 48K, page 8 is out of the
	// 3..10 range for 128K and is skipped — fine for detection tests).
	s0 := New()
	comp := s0.compressZ80(bytes.Repeat([]byte{0x11}, 16384))
	var blkHdr [3]byte
	binary.LittleEndian.PutUint16(blkHdr[0:2], uint16(len(comp)))
	blkHdr[2] = 8
	buf.Write(blkHdr[:])
	buf.Write(comp)

	s := New()
	if err := s.LoadBytes(buf.Bytes(), FormatZ80); err != nil {
		t.Fatal(err)
	}
	return s
}
