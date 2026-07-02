package divmmc

import "testing"

// Covers Pager accessors that wrap RAM/ROM access, port I/O, and
// runtime hooks.

func TestReadWriteRAM_FlatAddressing(t *testing.T) {
	p := New(nil)
	// Write into the start of bank 0, end of bank 0, start of bank 1.
	p.WriteRAM(0x0000, 0xAA)
	p.WriteRAM(0x1FFF, 0xBB)
	p.WriteRAM(0x2000, 0xCC)
	if got := p.ReadRAM(0x0000); got != 0xAA {
		t.Errorf("ReadRAM(0x0000) = %#x, want 0xAA", got)
	}
	if got := p.ReadRAM(0x1FFF); got != 0xBB {
		t.Errorf("ReadRAM(0x1FFF) = %#x, want 0xBB", got)
	}
	if got := p.ReadRAM(0x2000); got != 0xCC {
		t.Errorf("ReadRAM(0x2000) = %#x, want 0xCC", got)
	}
}

func TestWriteRAM_StubProtection(t *testing.T) {
	p := New(nil)
	p.SetStubProtected(true)
	if !p.StubProtected() {
		t.Fatal("StubProtected() should reflect SetStubProtected")
	}
	// Bank 1 offset $0009 — the protected stub byte. The dropped write
	// leaves the byte at its prior (power-on zero) value.
	addr := BankSize + 0x0009
	before := p.ReadRAM(addr)
	p.WriteRAM(addr, 0x42)
	if got := p.ReadRAM(addr); got != before {
		t.Errorf("protected write should be dropped: ReadRAM = %#x, want %#x", got, before)
	}
	// Same bank, different offset — should still write.
	p.WriteRAM(BankSize+0x000A, 0x55)
	if got := p.ReadRAM(BankSize + 0x000A); got != 0x55 {
		t.Errorf("non-protected offset: ReadRAM = %#x, want 0x55", got)
	}
}

func TestWriteRAM_LoggerHook(t *testing.T) {
	p := New(nil)
	type log struct {
		bank int
		addr uint16
		val  byte
	}
	var got log
	count := 0
	p.SetWriteLogger(func(b int, a uint16, v byte) {
		got = log{b, a, v}
		count++
	})
	p.WriteRAM(BankSize+0x0123, 0x77)
	if count != 1 {
		t.Errorf("logger calls = %d, want 1", count)
	}
	if got.bank != 1 || got.addr != 0x2123 || got.val != 0x77 {
		t.Errorf("logger args = %+v, want {bank:1 addr:0x2123 val:0x77}", got)
	}
}

func TestRAMBank_BoundsAndAliasing(t *testing.T) {
	p := New(nil)
	if p.RAMBank(-1) != nil {
		t.Error("RAMBank(-1) should return nil")
	}
	if p.RAMBank(NumBanks) != nil {
		t.Errorf("RAMBank(%d) should return nil", NumBanks)
	}
	// Writes via RAMBank should be visible via ReadRAM (aliasing).
	bank := p.RAMBank(2)
	if len(bank) != BankSize {
		t.Fatalf("bank len = %d, want %d", len(bank), BankSize)
	}
	bank[0x42] = 0xEE
	if got := p.ReadRAM(2*BankSize + 0x42); got != 0xEE {
		t.Errorf("RAMBank alias write not visible: ReadRAM = %#x, want 0xEE", got)
	}
}

func TestReadROMByte_OutOfRange(t *testing.T) {
	p := New([]byte{0x11, 0x22, 0x33})
	if got := p.ReadROMByte(0); got != 0x11 {
		t.Errorf("ReadROMByte(0) = %#x, want 0x11", got)
	}
	if got := p.ReadROMByte(-1); got != 0xFF {
		t.Errorf("ReadROMByte(-1) = %#x, want 0xFF (open bus)", got)
	}
	if got := p.ReadROMByte(0x10000); got != 0xFF {
		t.Errorf("ReadROMByte(0x10000) = %#x, want 0xFF", got)
	}
}

func TestWriteROMByte_LazyAllocAndBounds(t *testing.T) {
	p := New(nil)                 // no ROM at construction
	p.WriteROMByte(-1, 0xFF)      // OOB low — silently dropped
	p.WriteROMByte(ROMSize, 0xFF) // OOB high — silently dropped
	p.WriteROMByte(0x100, 0xAB)   // triggers lazy alloc
	if got := p.ReadROMByte(0x100); got != 0xAB {
		t.Errorf("after lazy alloc + write: ReadROMByte = %#x, want 0xAB", got)
	}
	// Untouched offset should be 0xFF from the lazy fill.
	if got := p.ReadROMByte(0x101); got != 0xFF {
		t.Errorf("lazy fill byte = %#x, want 0xFF", got)
	}
}

func TestEntryPoints0And1_RoundTrip(t *testing.T) {
	p := New(nil)
	p.SetEntryPoints0(0xA5)
	if p.EntryPoints0() != 0xA5 {
		t.Errorf("EntryPoints0 = %#x, want 0xA5", p.EntryPoints0())
	}
	p.SetEntryPoints1(0x5A)
	if p.EntryPoints1() != 0x5A {
		t.Errorf("EntryPoints1 = %#x, want 0x5A", p.EntryPoints1())
	}
}

func TestSetFramesBumper_AssignsHook(t *testing.T) {
	p := New(nil)
	called := 0
	p.SetFramesBumper(func() { called++ })
	// We can't easily trigger the bumper without exercising the IM-1
	// path; just confirm the assignment doesn't panic and the
	// closure stays referenced.
	if p.framesBump == nil {
		t.Error("framesBump should be set after SetFramesBumper")
	}
	_ = called
}

func TestReadPort_E3AndEB(t *testing.T) {
	p := New(nil)
	// $E3 returns last-written byte.
	p.WritePort(0xE3, 0x42)
	if v, ok := p.ReadPort(0xE3); !ok || v != 0x42 {
		t.Errorf("ReadPort(0xE3) = (%#x, %v), want (0x42, true)", v, ok)
	}
	// $EB with no card → open-bus 0xFF.
	if v, ok := p.ReadPort(0xEB); !ok || v != 0xFF {
		t.Errorf("ReadPort(0xEB) no-card = (%#x, %v), want (0xFF, true)", v, ok)
	}
	// Unrelated port → not ours.
	if _, ok := p.ReadPort(0xFE); ok {
		t.Errorf("ReadPort(0xFE) ok = true, want false (not divMMC)")
	}
}

// stubCard implements CardSlot to verify SetCard wiring + SPI port
// routing through both WritePort and ReadPort.
type stubCard struct {
	cs       byte
	written  []byte
	readNext byte
}

func (s *stubCard) WriteCS(b byte)   { s.cs = b }
func (s *stubCard) WriteData(b byte) { s.written = append(s.written, b) }
func (s *stubCard) ReadData() byte   { return s.readNext }

func TestSetCard_RoutesSPIPorts(t *testing.T) {
	p := New(nil)
	c := &stubCard{readNext: 0x33}
	p.SetCard(c)
	p.WritePort(0xE7, 0x01)
	p.WritePort(0xEB, 0xAA)
	p.WritePort(0xEB, 0xBB)
	if c.cs != 0x01 {
		t.Errorf("SD CS = %#x, want 0x01", c.cs)
	}
	if len(c.written) != 2 || c.written[0] != 0xAA || c.written[1] != 0xBB {
		t.Errorf("SD writes = %v, want [0xAA 0xBB]", c.written)
	}
	if v, ok := p.ReadPort(0xEB); !ok || v != 0x33 {
		t.Errorf("ReadPort(0xEB) = (%#x, %v), want (0x33, true)", v, ok)
	}
	// Detach card — port reads revert to open bus.
	p.SetCard(nil)
	if v, ok := p.ReadPort(0xEB); !ok || v != 0xFF {
		t.Errorf("ReadPort(0xEB) after SetCard(nil) = (%#x, %v), want (0xFF, true)", v, ok)
	}
	// Writes also become drops, not panics.
	p.WritePort(0xEB, 0x99)
	p.WritePort(0xE7, 0x01)
}
