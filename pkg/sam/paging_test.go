package sam

import "testing"

func emptyROMHalves() ([]byte, []byte) {
	return make([]byte, PageSize), make([]byte, PageSize)
}

// markMem tags ROM and RAM pages with recognisable markers at offset 0.
func markMem(m *Memory) *Memory {
	m.rom0[0] = 0xF3
	m.rom1[0] = 0x11
	for p := range m.ram {
		m.ram[p][0] = byte(0x80 | p)
	}
	for p := range m.extRAM {
		m.extRAM[p][0] = byte(p) // external markers (0-based)
	}
	return m
}

func TestRegisterReadback(t *testing.T) {
	r0, r1 := emptyROMHalves()
	m := NewMemory(r0, r1)

	m.SetLMPR(0x6A)
	m.SetHMPR(0x95)
	m.SetLEPR(0x5A)
	m.SetHEPR(0xA5)
	if m.LMPR() != 0x6A || m.HMPR() != 0x95 || m.LEPR() != 0x5A || m.HEPR() != 0xA5 {
		t.Errorf("register readback mismatch: LMPR=%#02x HMPR=%#02x LEPR=%#02x HEPR=%#02x",
			m.LMPR(), m.HMPR(), m.LEPR(), m.HEPR())
	}

	// VMPR stores only bits 0-6; reads OR in RXMIDI (bit 7).
	m.SetVMPR(0xFF)
	if m.VMPR() != 0xFF { // 0x7F stored | 0x80 RXMIDI
		t.Errorf("VMPR(0xFF) readback = %#02x, want 0xFF", m.VMPR())
	}
	m.SetVMPR(0x45)
	if m.VMPR() != 0xC5 { // 0x45 | 0x80
		t.Errorf("VMPR(0x45) readback = %#02x, want 0xC5", m.VMPR())
	}
}

func TestExternalRAMPaging(t *testing.T) {
	r0, r1 := emptyROMHalves()
	m := markMem(NewMemoryWithRAM(r0, r1, 512, 1)) // 1 MB external = 64 pages
	m.SetHMPR(hmprMCNTRL)                          // external RAM in C/D
	m.SetLEPR(5)                                   // section C -> ext page 5
	m.SetHEPR(7)                                   // section D -> ext page 7

	if got := m.Read(0x8000); got != 5 {
		t.Errorf("section C should be external page 5, got %#02x", got)
	}
	if got := m.Read(0xC000); got != 7 {
		t.Errorf("section D should be external page 7, got %#02x", got)
	}
	m.Write(0x8001, 0xEE)
	if got := m.Read(0x8001); got != 0xEE {
		t.Errorf("external RAM write/read failed, got %#02x", got)
	}
}

func TestMCNTRLOverridesROM1InSectionD(t *testing.T) {
	r0, r1 := emptyROMHalves()
	m := markMem(NewMemoryWithRAM(r0, r1, 512, 1))
	m.SetLMPR(lmprROM1)   // would put ROM1 in D...
	m.SetHMPR(hmprMCNTRL) // ...but MCNTRL takes priority -> external
	m.SetHEPR(9)
	if got := m.Read(0xC000); got != 9 {
		t.Errorf("MCNTRL must override ROM1 in D: got %#02x, want external page 9", got)
	}
}

func Test256KMasking(t *testing.T) {
	r0, r1 := emptyROMHalves()
	m := markMem(NewMemoryWithRAM(r0, r1, 256, 0)) // only pages 0-15 real
	// Page 5 (present) in section A.
	m.SetLMPR(lmprROM0Off | 5)
	if got := m.Read(0x0000); got != (0x80 | 5) {
		t.Errorf("present page 5 should read its marker, got %#02x", got)
	}
	// Page 20 (absent on 256K) -> scratch: reads 0, writes dropped.
	m.SetLMPR(lmprROM0Off | 20)
	if got := m.Read(0x0000); got != 0x00 {
		t.Errorf("absent page should read scratch (0), got %#02x", got)
	}
	m.Write(0x0000, 0x42)
	if got := m.Read(0x0000); got != 0x00 {
		t.Errorf("write to absent page should be dropped, got %#02x", got)
	}
}

func TestVMPRScreenModeAndPage(t *testing.T) {
	r0, r1 := emptyROMHalves()
	m := NewMemory(r0, r1)
	for mode, bits := range map[int]byte{1: 0x00, 2: 0x20, 3: 0x40, 4: 0x60} {
		m.SetVMPR(bits | 0x05) // page 5
		if m.ScreenMode() != mode {
			t.Errorf("VMPR %#02x: ScreenMode = %d, want %d", bits|5, m.ScreenMode(), mode)
		}
	}
	// Modes 1/2: page used as-is. Modes 3/4: forced even (24K, two pages).
	m.SetVMPR(0x00 | 0x05)
	if m.VisibleScreenPage() != 5 {
		t.Errorf("mode 1 page = %d, want 5", m.VisibleScreenPage())
	}
	m.SetVMPR(0x60 | 0x05)
	if m.VisibleScreenPage() != 4 {
		t.Errorf("mode 4 page (5) should force even to 4, got %d", m.VisibleScreenPage())
	}
}

func TestVideoMemByteSpansTwoPages(t *testing.T) {
	r0, r1 := emptyROMHalves()
	m := markMem(NewMemory(r0, r1))
	m.ram[2][0] = 0xAA
	m.ram[3][0] = 0xBB
	m.ram[3][3] = 0xCC
	m.SetVMPR(0x60 | 0x02) // mode 4, page 2 (even)
	if m.VideoMemByte(0) != 0xAA {
		t.Errorf("VideoMemByte(0) = %#02x, want 0xAA (page 2)", m.VideoMemByte(0))
	}
	if m.VideoMemByte(PageSize) != 0xBB {
		t.Errorf("VideoMemByte(16384) = %#02x, want 0xBB (page 3, second half)", m.VideoMemByte(PageSize))
	}
	if m.VideoMemByte(PageSize+3) != 0xCC {
		t.Errorf("VideoMemByte(16387) = %#02x, want 0xCC", m.VideoMemByte(PageSize+3))
	}
}

func TestVideoWriteHook(t *testing.T) {
	r0, r1 := emptyROMHalves()
	m := NewMemory(r0, r1)
	fired := 0
	m.SetVideoWriteHook(func() { fired++ })

	// Mode 1, display page 0. At reset section C ($8000) = RAM page 0 (displayed),
	// section B ($4000) = RAM page 1 (not displayed).
	m.SetVMPR(0x00) // mode 1, page 0
	m.Write(0x8000, 0x01)
	if fired != 1 {
		t.Errorf("write to displayed page should fire hook once, got %d", fired)
	}
	fired = 0
	m.Write(0x4000, 0x02) // page 1, not displayed in mode 1
	if fired != 0 {
		t.Errorf("write to non-displayed page should not fire hook, got %d", fired)
	}

	// Mode 4: bitmap spans page 0 AND page 1, so section B (page 1) is now video.
	m.SetVMPR(0x60) // mode 4, page 0 (+page 1)
	fired = 0
	m.Write(0x4000, 0x03)
	if fired != 1 {
		t.Errorf("mode 3/4 second page should be video memory, hook fired %d times", fired)
	}
}
