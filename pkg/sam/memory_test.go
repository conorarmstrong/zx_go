package sam

import "testing"

// markedMem builds a Memory whose ROM and RAM pages carry recognisable markers
// so tests can tell which physical page a section resolves to.
func markedMem() *Memory {
	rom0 := make([]byte, PageSize)
	rom1 := make([]byte, PageSize)
	rom0[0] = 0xF3 // DI marker
	rom1[0] = 0x11
	m := NewMemory(rom0, rom1)
	for p := 0; p < numRAMPages; p++ {
		m.ram[p][0] = byte(0x80 | p) // page-id marker at offset 0
	}
	return m
}

// TestResetPageMap pins the power-on map: ROM0 / RAM1 / RAM0 / RAM1.
// (LMPR=HMPR=0: A=ROM0, B=page lmpr+1=1, C=page hmpr=0, D=page hmpr+1=1.)
func TestResetPageMap(t *testing.T) {
	m := markedMem()
	cases := []struct {
		addr uint16
		want byte
		desc string
	}{
		{0x0000, 0xF3, "section A = ROM0"},
		{0x4000, 0x81, "section B = RAM page 1"},
		{0x8000, 0x80, "section C = RAM page 0"},
		{0xC000, 0x81, "section D = RAM page 1 (ROM1 off at reset)"},
	}
	for _, c := range cases {
		if got := m.Read(c.addr); got != c.want {
			t.Errorf("%s: Read(%#04x) = %#02x, want %#02x", c.desc, c.addr, got, c.want)
		}
	}
}

// TestROMSectionIsReadOnly confirms ROM0 (section A at reset) ignores writes.
func TestROMSectionIsReadOnly(t *testing.T) {
	m := markedMem()
	m.Write(0x0000, 0x42)
	if got := m.Read(0x0000); got != 0xF3 {
		t.Errorf("ROM write should be ignored: Read($0000) = %#02x, want 0xF3", got)
	}
}

// TestRAMReadWrite confirms RAM sections persist writes.
func TestRAMReadWrite(t *testing.T) {
	m := markedMem()
	m.Write(0x8123, 0x5A) // section C = RAM page 0
	if got := m.Read(0x8123); got != 0x5A {
		t.Errorf("RAM write/read: got %#02x, want 0x5A", got)
	}
}

// TestLMPR_ROM0Off pages RAM into section A; TestLMPR_ROM1 pages ROM1 into D;
// TestLMPR_WriteProtect makes section A read-only even when it is RAM.
func TestLMPRPaging(t *testing.T) {
	t.Run("ROM0 off -> RAM in A", func(t *testing.T) {
		m := markedMem()
		m.SetLMPR(lmprROM0Off) // page bits 0 -> RAM page 0 in section A
		if got := m.Read(0x0000); got != 0x80 {
			t.Errorf("A should be RAM page 0, got %#02x", got)
		}
		m.Write(0x0000, 0x42)
		if got := m.Read(0x0000); got != 0x42 {
			t.Errorf("RAM in A should be writable, got %#02x", got)
		}
	})
	t.Run("ROM1 in D", func(t *testing.T) {
		m := markedMem()
		m.SetLMPR(lmprROM1)
		if got := m.Read(0xC000); got != 0x11 {
			t.Errorf("D should be ROM1 (0x11), got %#02x", got)
		}
	})
	t.Run("write-protect A", func(t *testing.T) {
		m := markedMem()
		m.SetLMPR(lmprROM0Off | lmprWProt) // RAM page 0 in A, but protected
		m.Write(0x0000, 0x42)
		if got := m.Read(0x0000); got != 0x80 {
			t.Errorf("write-protected A should ignore writes, got %#02x", got)
		}
	})
}
