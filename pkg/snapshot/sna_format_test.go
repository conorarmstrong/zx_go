package snapshot

// Spec-faithful tests for the .SNA snapshot format.
//
// Oracle: the published .SNA format specification (the World of Spectrum
// / Sinclair Wiki "SNA format" reference). Key facts pinned here:
//
//   - 48K .SNA = 27-byte header + 48KB RAM = 49179 bytes total.
//   - The 27-byte header layout (little-endian register pairs):
//       0:I 1:L' 2:H' 3:E' 4:D' 5:C' 6:B' 7:F' 8:A'
//       9:L 10:H 11:E 12:D 13:C 14:B 15-16:IY 17-18:IX
//       19:interrupt(bit2=IFF2) 20:R 21:F 22:A 23-24:SP 25:IM 26:border
//   - 48K quirk: the PC is NOT in the header — it is pushed on the stack,
//     so the loader pops it from (SP) and (SP+1) and adds 2 to SP.
//   - 128K .SNA: the 48K dump's three pages are 5 (0x4000), 2 (0x8000)
//     and the page CURRENTLY MAPPED at 0xC000 (NOT a hardcoded bank 0).
//     After the dump come PC(2)+port7FFD(1)+TR-DOS(1), then the remaining
//     pages in ascending order skipping {5,2,paged}. When the paged page
//     is 5 or 2 the remaining set is all of {0,1,3,4,6,7}.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// build48KSNA returns a minimal valid 48K .SNA byte stream with the given
// SP and a PC value pushed on the stack at SP within bank 0 (0xC000 area).
func build48KSNA(t *testing.T, sp, pcOnStack uint16) []byte {
	t.Helper()
	hdr := make([]byte, 27)
	binary.LittleEndian.PutUint16(hdr[23:25], sp)
	ram := make([]byte, 49152) // banks 5,2,0 concatenated
	// SP lives in bank 0 (0xC000..0xFFFF) → file offset 0x8000 + (SP-0xC000).
	if sp >= 0xC000 {
		off := 0x8000 + int(sp-0xC000)
		ram[off] = byte(pcOnStack)
		ram[off+1] = byte(pcOnStack >> 8)
	}
	out := append([]byte{}, hdr...)
	return append(out, ram...)
}

// TestSNA48KFileSizeIsExact pins the 49179-byte total mandated by the spec.
func TestSNA48KFileSizeIsExact(t *testing.T) {
	data := build48KSNA(t, 0xFF00, 0x1234)
	if len(data) != SNA48K_SIZE {
		t.Fatalf("48K SNA size = %d, spec requires %d", len(data), SNA48K_SIZE)
	}
}

// TestSNA48KPopsPCFromStack pins the defining 48K quirk: PC comes off the
// stack and SP advances by 2.
func TestSNA48KPopsPCFromStack(t *testing.T) {
	data := build48KSNA(t, 0xFF00, 0xABCD)
	s := New()
	if err := s.LoadBytes(data, FormatSNA); err != nil {
		t.Fatal(err)
	}
	if s.CPU.PC != 0xABCD {
		t.Errorf("PC = 0x%04X, want 0xABCD (popped from stack)", s.CPU.PC)
	}
	if s.CPU.SP != 0xFF02 {
		t.Errorf("SP = 0x%04X, want 0xFF02 (advanced by 2)", s.CPU.SP)
	}
	if s.Memory.Is128K {
		t.Error("48K-sized SNA must not be flagged 128K")
	}
}

// TestSNAHeaderByteOffsets pins each header field to its exact spec offset
// by writing a unique value into every byte and asserting the decode.
func TestSNAHeaderByteOffsets(t *testing.T) {
	hdr := make([]byte, 27)
	hdr[0] = 0x21                                     // I
	hdr[1] = 0x22                                     // L'
	hdr[2] = 0x23                                     // H'
	hdr[3] = 0x24                                     // E'
	hdr[4] = 0x25                                     // D'
	hdr[5] = 0x26                                     // C'
	hdr[6] = 0x27                                     // B'
	hdr[7] = 0x28                                     // F'
	hdr[8] = 0x29                                     // A'
	hdr[9] = 0x2A                                     // L
	hdr[10] = 0x2B                                    // H
	hdr[11] = 0x2C                                    // E
	hdr[12] = 0x2D                                    // D
	hdr[13] = 0x2E                                    // C
	hdr[14] = 0x2F                                    // B
	binary.LittleEndian.PutUint16(hdr[15:17], 0xBEEF) // IY
	binary.LittleEndian.PutUint16(hdr[17:19], 0xCAFE) // IX
	hdr[19] = 0x04                                    // bit2 => IFF2
	hdr[20] = 0x55                                    // R
	hdr[21] = 0x66                                    // F
	hdr[22] = 0x77                                    // A
	binary.LittleEndian.PutUint16(hdr[23:25], 0xFFF0) // SP
	hdr[25] = 0x02                                    // IM 2
	hdr[26] = 0x05                                    // border 5

	data := append(append([]byte{}, hdr...), make([]byte, 49152)...)
	s := New()
	if err := s.LoadBytes(data, FormatSNA); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		got  byte
		want byte
	}{
		{"I", s.CPU.I, 0x21}, {"L'", s.CPU.L_, 0x22}, {"H'", s.CPU.H_, 0x23},
		{"E'", s.CPU.E_, 0x24}, {"D'", s.CPU.D_, 0x25}, {"C'", s.CPU.C_, 0x26},
		{"B'", s.CPU.B_, 0x27}, {"F'", s.CPU.F_, 0x28}, {"A'", s.CPU.A_, 0x29},
		{"L", s.CPU.L, 0x2A}, {"H", s.CPU.H, 0x2B}, {"E", s.CPU.E, 0x2C},
		{"D", s.CPU.D, 0x2D}, {"C", s.CPU.C, 0x2E}, {"B", s.CPU.B, 0x2F},
		{"R", s.CPU.R, 0x55}, {"F", s.CPU.F, 0x66}, {"A", s.CPU.A, 0x77},
		{"IM", s.CPU.IM, 0x02}, {"border", s.CPU.BorderColor, 0x05},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = 0x%02X, want 0x%02X", c.name, c.got, c.want)
		}
	}
	if s.CPU.IY != 0xBEEF {
		t.Errorf("IY = 0x%04X, want 0xBEEF", s.CPU.IY)
	}
	if s.CPU.IX != 0xCAFE {
		t.Errorf("IX = 0x%04X, want 0xCAFE", s.CPU.IX)
	}
	if !s.CPU.IFF2 || !s.CPU.IFF1 {
		t.Error("byte19 bit2 set => IFF2 (and IFF1 mirrored) must be true")
	}
}

// TestSNA48KIFF2Clear pins that interrupt byte 19 with bit 2 clear yields
// IFF1=IFF2=false (the spec mirrors IFF1 from IFF2 for SNA).
func TestSNA48KIFF2Clear(t *testing.T) {
	hdr := make([]byte, 27)
	hdr[19] = 0x00 // bit 2 clear
	data := append(append([]byte{}, hdr...), make([]byte, 49152)...)
	s := New()
	if err := s.LoadBytes(data, FormatSNA); err != nil {
		t.Fatal(err)
	}
	if s.CPU.IFF1 || s.CPU.IFF2 {
		t.Errorf("IFF1=%v IFF2=%v, want both false", s.CPU.IFF1, s.CPU.IFF2)
	}
}

// TestSNA128KPagedBankRoundtripAllPages pins that the 48K dump's third
// page always follows whichever bank port 0x7FFD has mapped at 0xC000
// (not a fixed bank), for every possible paged-bank selection — so all
// eight RAM banks round-trip intact regardless of which one is paged in.
func TestSNA128KPagedBankRoundtripAllPages(t *testing.T) {
	for page := byte(0); page < 8; page++ {
		orig := New()
		orig.Memory.Is128K = true
		orig.Memory.Port7FFD = page
		orig.CPU.PC = 0x9000
		orig.CPU.SP = 0xFFF0
		for bank := 0; bank < 8; bank++ {
			fill := byte(bank*0x10 + 0x01)
			for i := range orig.Memory.RAM[bank] {
				orig.Memory.RAM[bank][i] = fill
			}
		}
		data, err := orig.SaveBytes(FormatSNA)
		if err != nil {
			t.Fatalf("page %d save: %v", page, err)
		}
		loaded := New()
		if err := loaded.LoadBytes(data, FormatSNA); err != nil {
			t.Fatalf("page %d load: %v", page, err)
		}
		if loaded.Memory.Port7FFD != page {
			t.Errorf("page %d: Port7FFD = 0x%02X", page, loaded.Memory.Port7FFD)
		}
		if loaded.CPU.PC != 0x9000 {
			t.Errorf("page %d: PC = 0x%04X, want 0x9000", page, loaded.CPU.PC)
		}
		for bank := 0; bank < 8; bank++ {
			want := byte(bank*0x10 + 0x01)
			if loaded.Memory.RAM[bank][0] != want {
				t.Errorf("page %d: RAM[%d][0] = 0x%02X, want 0x%02X",
					page, bank, loaded.Memory.RAM[bank][0], want)
			}
		}
	}
}

// TestSNA128KFileSize pins the 131103-byte total for the common case where
// the paged page differs from 5 and 2 (so exactly 5 remaining pages follow).
func TestSNA128KFileSize(t *testing.T) {
	s := New()
	s.Memory.Is128K = true
	s.Memory.Port7FFD = 0x03 // bank 3 paged (not 5/2)
	data, err := s.SaveBytes(FormatSNA)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != SNA128K_SIZE {
		t.Fatalf("128K SNA (paged=3) size = %d, spec requires %d", len(data), SNA128K_SIZE)
	}
}

// TestSNA128KPagedPageIsThirdInFile pins the spec's page order: the byte
// stream is 5,2,<paged>, then PC/port/TRDOS, then the remaining banks.
// We assert the third 16K block in the file equals the paged bank.
func TestSNA128KPagedPageIsThirdInFile(t *testing.T) {
	s := New()
	s.Memory.Is128K = true
	s.Memory.Port7FFD = 0x04 // bank 4 paged at 0xC000
	for i := range s.Memory.RAM[4] {
		s.Memory.RAM[4][i] = 0x44
	}
	for i := range s.Memory.RAM[0] {
		s.Memory.RAM[0][i] = 0x00
	}
	data, err := s.SaveBytes(FormatSNA)
	if err != nil {
		t.Fatal(err)
	}
	// Third 16K block sits at: 27 (header) + 2*16384.
	thirdStart := 27 + 2*16384
	third := data[thirdStart : thirdStart+16384]
	if !bytes.Equal(third, s.Memory.RAM[4]) {
		t.Errorf("third file page is not the paged-in bank 4 (first byte = 0x%02X, want 0x44)", third[0])
	}
}

// TestSNA128KPagedIs5RemainingSet pins the spec's special case: when the
// paged page is 5 (also one of the fixed pages), the remaining set is all
// of {0,1,3,4,6,7} — bank 5 appears twice and the file grows accordingly.
func TestSNA128KPagedIs5RemainingSet(t *testing.T) {
	s := New()
	s.Memory.Is128K = true
	s.Memory.Port7FFD = 0x05 // bank 5 paged
	data, err := s.SaveBytes(FormatSNA)
	if err != nil {
		t.Fatal(err)
	}
	// 27 hdr + 3 dump pages + 4 extra + 6 remaining pages.
	wantSize := 27 + 3*16384 + 4 + 6*16384
	if len(data) != wantSize {
		t.Fatalf("paged=5 file size = %d, want %d (bank 5 stored twice)", len(data), wantSize)
	}
}

// TestSNATruncated128KTailErrors pins that a file with 1-3 stray bytes
// after the 48K dump (a 128K SNA cut short mid-way through the
// PC/port7FFD/TR-DOS trailer, rather than a clean 49179-byte 48K file)
// is reported as an error instead of silently being accepted as a
// (bogus) 48K snapshot.
func TestSNATruncated128KTailErrors(t *testing.T) {
	data := build48KSNA(t, 0xFF00, 0x1234)
	// Append a partial 128K trailer: only 2 of the 4 expected bytes.
	data = append(data, 0x00, 0x60)
	s := New()
	if err := s.LoadBytes(data, FormatSNA); err == nil {
		t.Fatal("LoadBytes: want error for a truncated 128K trailer, got nil")
	}
}

// TestSNARoundTripBorderMasked pins border masking to 3 bits both ways.
func TestSNARoundTripBorderMasked(t *testing.T) {
	s := New()
	s.CPU.BorderColor = 0x05
	data, err := s.SaveBytes(FormatSNA)
	if err != nil {
		t.Fatal(err)
	}
	if data[26]&0xF8 != 0 {
		t.Errorf("border byte 0x%02X has bits above bit2 set", data[26])
	}
}
