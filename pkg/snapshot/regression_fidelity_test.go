package snapshot

import (
	"bytes"
	"testing"
)

// A .z80 must round-trip R exactly. The format splits R across two
// bytes: the low 7 bits live in header[11], and bit 7 travels in bit
// 0 of the flags byte at header[12]. saveZ80 seeded that flags byte
// with 0x01 before OR-ing the real bit in, so bit 0 was set whatever
// R held and every reload came back with R|0x80.
func TestZ80RoundTripsRWithBit7Clear(t *testing.T) {
	for _, r := range []byte{0x00, 0x05, 0x7F, 0x80, 0x85, 0xFF} {
		s := New()
		s.CPU.R = r
		s.CPU.PC = 0x8000
		s.CPU.SP = 0xFF00

		var buf bytes.Buffer
		if err := s.saveZ80(&buf); err != nil {
			t.Fatalf("saveZ80 R=%#02x: %v", r, err)
		}
		got := New()
		if err := got.loadZ80(bytes.NewReader(buf.Bytes())); err != nil {
			t.Fatalf("loadZ80 R=%#02x: %v", r, err)
		}
		if got.CPU.R != r {
			t.Errorf("R round trip: wrote %#02x, read back %#02x", r, got.CPU.R)
		}
	}
}

// The 48K SNA format has no PC field: PC is pushed onto the guest's
// stack and SP in the header points at it. loadSNA pops it, so
// saveSNA has to push it, or the reload resumes at whatever two
// bytes happened to be under SP.
func TestSNA48KRoundTripsPC(t *testing.T) {
	s := New()
	s.Memory.Is128K = false
	s.CPU.PC = 0xABCD
	s.CPU.SP = 0xFFF0
	// Something recognisable under the stack, so a failure to push
	// shows up as this value rather than as zero.
	s.Memory.RAM[0][0xFFEE-0xC000] = 0x11
	s.Memory.RAM[0][0xFFEF-0xC000] = 0x22

	var buf bytes.Buffer
	if err := s.saveSNA(&buf); err != nil {
		t.Fatalf("saveSNA: %v", err)
	}
	got := New()
	if err := got.loadSNA(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("loadSNA: %v", err)
	}
	if got.CPU.PC != 0xABCD {
		t.Errorf("PC round trip: wrote %#04x, read back %#04x", 0xABCD, got.CPU.PC)
	}
	if got.CPU.SP != 0xFFF0 {
		t.Errorf("SP round trip: wrote %#04x, read back %#04x", 0xFFF0, got.CPU.SP)
	}
}

// szxWithMachineID builds the smallest valid SZX: the 8-byte header
// and no blocks. Only the machine-ID classification is under test.
func szxWithMachineID(id byte) []byte {
	return []byte{'Z', 'X', 'S', 'T', 1, 4, id, 0}
}

// A Pentagon 128 or +3e snapshot is a 128K-family machine. Classifying
// it as 48K places only banks 5/2/0 and pages nothing, so the program
// crashes on resume.
func TestSZXClassifies128KFamilyMachines(t *testing.T) {
	is128 := map[byte]bool{
		ZXSTMID_16K:          false,
		ZXSTMID_48K:          false,
		ZXSTMID_NTSC48K:      false,
		ZXSTMID_TC2048:       false,
		ZXSTMID_128K:         true,
		ZXSTMID_PLUS2:        true,
		ZXSTMID_PLUS2A:       true,
		ZXSTMID_PLUS3:        true,
		ZXSTMID_PLUS3E:       true,
		ZXSTMID_128KE:        true,
		ZXSTMID_PENTAGON128:  true,
		ZXSTMID_PENTAGON512:  true,
		ZXSTMID_PENTAGON1024: true,
		ZXSTMID_SCORPION:     true,
		ZXSTMID_SE:           true,
	}
	for id, want := range is128 {
		s := New()
		if err := s.loadSZX(bytes.NewReader(szxWithMachineID(id))); err != nil {
			t.Fatalf("machine id %d: %v", id, err)
		}
		if s.Memory.Is128K != want {
			t.Errorf("machine id %d: Is128K = %v, want %v", id, s.Memory.Is128K, want)
		}
	}
}

// z80V3WithHardwareMode builds a .z80 v3 header far enough for the
// hardware-mode byte to be read. PC = 0 in the v1 header is what
// signals v2/v3; the extended header length then picks the version.
func z80V3WithHardwareMode(mode byte) []byte {
	buf := make([]byte, Z80_V1_HEADER_SIZE)
	// header[6:8] = PC = 0 -> version 2/3.
	b := append([]byte(nil), buf...)
	ext := make([]byte, 2+54)
	ext[0] = 54 // extended header length, little-endian uint16
	ext[1] = 0
	ext[2+2] = mode
	return append(b, ext...)
}

func TestZ80V3Classifies128KFamilyHardwareModes(t *testing.T) {
	is128 := map[byte]bool{
		Z80_HW_48K:       false,
		Z80_HW_48K_IF1:   false,
		Z80_HW_48K_MGT:   false,
		Z80_HW_128K:      true,
		Z80_HW_128K_IF1:  true,
		Z80_HW_128K_MGT:  true,
		Z80_HW_PLUS3:     true,
		Z80_HW_PLUS3_BUG: true,
		Z80_HW_PENTAGON:  true,
		Z80_HW_SCORPION:  true,
		Z80_HW_PLUS2:     true,
		Z80_HW_PLUS2A:    true,
	}
	for mode, want := range is128 {
		s := New()
		// Memory blocks are absent, so the read past the header fails;
		// the classification has already happened by then.
		_ = s.loadZ80(bytes.NewReader(z80V3WithHardwareMode(mode)))
		if s.Memory.Is128K != want {
			t.Errorf("hardware mode %d: Is128K = %v, want %v", mode, s.Memory.Is128K, want)
		}
	}
}

// SZX has a $1FFD slot in its SPCR block and we were writing a
// hardcoded zero into it, so a +2A/+3 save lost special paging and
// the ROM 2/3 selection.
func TestSZXRoundTripsPort1FFD(t *testing.T) {
	s := New()
	s.Memory.Is128K = true
	s.Memory.Port7FFD = 0x02
	s.Memory.Port1FFD = 0x05
	s.CPU.PC = 0x8000

	var buf bytes.Buffer
	if err := s.saveSZX(&buf); err != nil {
		t.Fatalf("saveSZX: %v", err)
	}
	got := New()
	if err := got.loadSZX(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("loadSZX: %v", err)
	}
	if got.Memory.Port1FFD != 0x05 {
		t.Errorf("Port1FFD: got %#02x, want %#02x", got.Memory.Port1FFD, 0x05)
	}
	if got.Memory.Port7FFD != 0x02 {
		t.Errorf("Port7FFD: got %#02x, want %#02x", got.Memory.Port7FFD, 0x02)
	}
}

// A .z80 v3 written for a +3 carries $1FFD in a 55-byte extended
// header, at header offset 86 (extended-header index 54).
func TestZ80V3ReadsPort1FFD(t *testing.T) {
	buf := make([]byte, Z80_V1_HEADER_SIZE) // PC = 0 selects v2/v3
	ext := make([]byte, 2+55)
	ext[0] = 55
	ext[2+2] = Z80_HW_PLUS3 // hardware mode
	ext[2+3] = 0x02         // $7FFD
	ext[2+54] = 0x07        // $1FFD
	s := New()
	// No memory blocks follow, so the read past the header fails; the
	// header fields are already in place by then.
	_ = s.loadZ80(bytes.NewReader(append(buf, ext...)))
	if s.Memory.Port1FFD != 0x07 {
		t.Errorf("Port1FFD: got %#02x, want %#02x", s.Memory.Port1FFD, 0x07)
	}
	if s.Memory.Port7FFD != 0x02 {
		t.Errorf("Port7FFD: got %#02x, want %#02x", s.Memory.Port7FFD, 0x02)
	}
}
