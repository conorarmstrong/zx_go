package snapshot

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildV2_128K builds a minimal v2 .z80 snapshot for a 128K machine with the
// eight RAM pages 3..10 present, each page filled with a unique marker byte
// equal to its page number. Blocks are compressed via the package's own
// compressor so the test drives the real decode path.
func buildV2_128K(t *testing.T, hwMode, port7FFD byte) []byte {
	t.Helper()
	s := New()
	var buf bytes.Buffer

	hdr := make([]byte, Z80_V1_HEADER_SIZE)
	// PC == 0 in the v1 header signals "extended (v2/v3) format follows".
	hdr[6], hdr[7] = 0, 0
	buf.Write(hdr)

	// Additional header length 23 => version 2.
	buf.Write([]byte{23, 0})
	ext := make([]byte, 23)
	ext[0], ext[1] = 0x3d, 0x1f // real PC = 0x1f3d
	ext[2] = hwMode
	ext[3] = port7FFD
	buf.Write(ext)

	for pg := 3; pg <= 10; pg++ {
		page := make([]byte, 16384)
		for i := range page {
			page[i] = byte(pg)
		}
		comp := s.compressZ80(page)
		var ln [2]byte
		binary.LittleEndian.PutUint16(ln[:], uint16(len(comp)))
		buf.Write(ln[:])
		buf.WriteByte(byte(pg))
		buf.Write(comp)
	}
	return buf.Bytes()
}

// TestLoadZ80_128KBankMapping pins the .z80 spec page→bank mapping for 128K
// snapshots: page N holds RAM bank N-3 (page 3→bank 0 … page 10→bank 7).
// Applying the 48K page interpretation instead (pages 4/5 → banks 2/0) to a
// 128K snapshot would leave bank 1 empty and overwrite bank 0, scrambling
// memory under the running program.
func TestLoadZ80_128KBankMapping(t *testing.T) {
	data := buildV2_128K(t, Z80_HW_128K, 0x10)

	s := New()
	if err := s.LoadBytes(data, FormatZ80); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if !s.Memory.Is128K {
		t.Fatalf("Is128K = false, want true for hwMode %d", Z80_HW_128K)
	}
	if s.Memory.Port7FFD != 0x10 {
		t.Errorf("Port7FFD = %#x, want 0x10", s.Memory.Port7FFD)
	}
	if s.CPU.PC != 0x1f3d {
		t.Errorf("PC = %#x, want 0x1f3d", s.CPU.PC)
	}
	for bank := 0; bank < 8; bank++ {
		want := byte(bank + 3) // bank b came from page b+3
		got := s.Memory.RAM[bank][0]
		if got != want {
			t.Errorf("RAM bank %d: marker = %d, want %d (page %d)", bank, got, want, bank+3)
		}
	}
}

// TestLoadZ80_V2HardwareMode3Is128K pins the version-dependent meaning of the
// hardware-mode byte: in a v2 snapshot (ext header length 23) mode 3 is 128K,
// whereas the same value is 48K+MGT in v3. A v2 128K snapshot using mode 3
// must load as 128K (and use the 128K page→bank mapping), not 48K.
func TestLoadZ80_V2HardwareMode3Is128K(t *testing.T) {
	data := buildV2_128K(t, 3, 0x10) // v2 hardware mode 3 = 128K

	s := New()
	if err := s.LoadBytes(data, FormatZ80); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if !s.Memory.Is128K {
		t.Fatal("v2 hardware mode 3 must be detected as 128K")
	}
	// 128K mapping must then apply: bank 1 comes from page 4 (marker 4).
	if got := s.Memory.RAM[1][0]; got != 4 {
		t.Errorf("RAM bank 1 marker = %d, want 4 — 128K page mapping not applied", got)
	}
}

// TestLoadZ80_UncompressedBlock pins the .z80 "0xFFFF means 16384 bytes
// uncompressed" sentinel. The block length 0xFFFF is NOT a real length — the
// loader must read exactly 16384 raw bytes, not 65535, or it over-reads into
// (and corrupts) the following block.
func TestLoadZ80_UncompressedBlock(t *testing.T) {
	s := New()
	var buf bytes.Buffer

	hdr := make([]byte, Z80_V1_HEADER_SIZE)
	hdr[6], hdr[7] = 0, 0 // v2/v3
	buf.Write(hdr)
	buf.Write([]byte{23, 0})
	ext := make([]byte, 23)
	ext[2] = Z80_HW_128K
	buf.Write(ext)

	// Page 3: uncompressed (0xFFFF sentinel) + exactly 16384 bytes of 0x33.
	buf.Write([]byte{0xFF, 0xFF, 3})
	page3 := make([]byte, 16384)
	for i := range page3 {
		page3[i] = 0x33
	}
	buf.Write(page3)

	// Page 4: a normal compressed block right after, to catch over-reads.
	page4 := make([]byte, 16384)
	for i := range page4 {
		page4[i] = 0x44
	}
	comp := s.compressZ80(page4)
	var ln [2]byte
	binary.LittleEndian.PutUint16(ln[:], uint16(len(comp)))
	buf.Write(ln[:])
	buf.WriteByte(4)
	buf.Write(comp)

	s2 := New()
	if err := s2.LoadBytes(buf.Bytes(), FormatZ80); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if s2.Memory.RAM[0][0] != 0x33 || s2.Memory.RAM[0][16383] != 0x33 {
		t.Errorf("bank 0 (uncompressed page 3): [0]=%#x [16383]=%#x, want all 0x33",
			s2.Memory.RAM[0][0], s2.Memory.RAM[0][16383])
	}
	if s2.Memory.RAM[1][0] != 0x44 {
		t.Errorf("bank 1 (compressed page 4 after the uncompressed block) = %#x, want 0x44 (over-read corruption)",
			s2.Memory.RAM[1][0])
	}
}
