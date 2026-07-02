package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestWireROMBankRoutesNR8EToExtendedHandler verifies the
// dispatcher's NR$8E OnWrite goes through the extended
// (full-bit-semantics) path, not the legacy bits-1:0-only path.
// Writing $08 (bit 3 only) must reset slot 3's RAM bank to 0,
// matching the NextZXOS boot sequence. Without WireROMBank's
// extension, the write would select ROM bank 0 but leave the
// previously-set RAM bank in place.
func TestWireROMBankRoutesNR8EToExtendedHandler(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-set RAM bank 7 at slot 3 via the classic port path so the
	// test would fail under the old behaviour (where NR$8E ignored
	// bits 6:4).
	mem.PageMemory(0x07)

	disp := nextregs.New()
	WireROMBank(disp, mem)

	disp.Select(0x8E)
	disp.WriteData(0x08)

	if got := disp.Raw(0x8E); got != 0x08 {
		t.Errorf("dispatcher storage: Raw($8E) = %#x, want $08", got)
	}
	// Slot 3 should now be RAM bank 0 (bits 6:4 of NR$8E = 0).
	port7FFD, _, _ := mem.GetPortState()
	if got := port7FFD & 0x07; got != 0 {
		t.Errorf("after NR$8E=$08: port_7FFD[2:0] = %d, want 0", got)
	}
}

// TestWireROMBank_NR8E_7A covers NextZXOS's mid-boot pattern:
// NR$8E=$7A = ROM bank 2 + RAM bank 7 at slot 3 + 1FFD bit 0 = 0.
func TestWireROMBank_NR8E_7A(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireROMBank(disp, mem)

	disp.Select(0x8E)
	disp.WriteData(0x7A)

	port7FFD, port1FFD, _ := mem.GetPortState()
	if got := port7FFD & 0x07; got != 7 {
		t.Errorf("NR$8E=$7A: port_7FFD[2:0] = %d, want 7", got)
	}
	if got := port1FFD & 0x01; got != 0 {
		t.Errorf("NR$8E=$7A: port_1FFD[0] = %d, want 0", got)
	}
}
