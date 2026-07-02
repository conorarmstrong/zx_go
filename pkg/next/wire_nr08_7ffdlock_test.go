package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

func nr08Harness(t *testing.T) (*nextregs.Dispatcher, *memory.Memory, *z80.CPU) {
	t.Helper()
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	WireContentionDisable(disp, mem)
	WireReset(disp, mem, cpu, nil)
	return disp, mem, cpu
}

// TestNR08Bit7UnlocksPaging — NR$08 write with bit 7 set clears the
// $7FFD paging lock (zxnext.vhd:3654-3656: nr_08_we and dat(7) →
// port_7ffd_reg(5) <= '0'). NextZXOS's staging NR-pair list relies
// on this RMW to keep paging unlocked across the engine install.
func TestNR08Bit7UnlocksPaging(t *testing.T) {
	d, mem, _ := nr08Harness(t)

	mem.PageMemory(0x20) // bit 5 = lock
	if mem.PagingEnabled {
		t.Fatal("paging should be locked after 7FFD bit-5 write")
	}
	d.WriteReg(0x08, 0xFE) // bit 7 set → unlock
	if !mem.PagingEnabled {
		t.Error("NR$08 bit 7 write must unlock 7FFD paging")
	}

	mem.PageMemory(0x20)
	d.WriteReg(0x08, 0x7E) // bit 7 clear → lock untouched
	if mem.PagingEnabled {
		t.Error("NR$08 write with bit 7 clear must NOT unlock paging")
	}
}

// TestNR08ReadBit7IsNotLocked — NR$08 read bit 7 = NOT
// port_7ffd_locked, a LIVE view of the lock, not a stored bit
// (zxnext.vhd:5906). The OS's read-modify-write of NR$08 preserves
// the lock state through this bit.
func TestNR08ReadBit7IsNotLocked(t *testing.T) {
	d, mem, _ := nr08Harness(t)

	if d.ReadReg(0x08)&0x80 == 0 {
		t.Error("unlocked: NR$08 bit 7 must read 1")
	}
	mem.PageMemory(0x20) // lock
	if d.ReadReg(0x08)&0x80 != 0 {
		t.Error("locked: NR$08 bit 7 must read 0")
	}
}

// TestSoftResetClearsPagingLock — the FPGA clears port_7ffd_reg on
// EVERY reset (zxnext.vhd:3646-3648, reset = hard or soft), which
// unlocks paging. NextZXOS's staging soft-reset depends on coming
// back up unlocked.
func TestSoftResetClearsPagingLock(t *testing.T) {
	d, mem, _ := nr08Harness(t)

	mem.PageMemory(0x20)
	if mem.PagingEnabled {
		t.Fatal("precondition: locked")
	}
	d.WriteReg(0x02, 0x01) // soft reset
	if !mem.PagingEnabled {
		t.Error("soft reset must clear the 7FFD paging lock")
	}
	if p7, _, _ := mem.GetPortState(); p7 != 0 {
		t.Errorf("soft reset must clear port_7ffd_reg, got %#02x", p7)
	}
}
