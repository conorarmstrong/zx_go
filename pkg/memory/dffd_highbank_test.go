package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestDFFDHighBankReadsRAMNotROM locks in that the $DFFD-extended RAM bank
// in the $C000 slot is READ as RAM. port_7ffd_bank is a 7-bit value
// (zxnext.vhd: port_dffd_reg(3:0) & port_7ffd_reg(2:0)), so slot 3 can hold
// banks 0..127 — but the classic read dispatch used ">= 16 means ROM" for
// every 16K slot, which is only true of slot 0 ($0000-$3FFF).
//
// Before the fix, banks 16..19 silently returned ROM 0 bytes (the write had
// landed in RAM, so read and write disagreed) and banks 20+ indexed past the
// four-entry ROM array and panicked — a guest-triggerable crash, since a
// plain `ld bc,$DFFD : out (c),a` reaches it.
func TestDFFDHighBankReadsRAMNotROM(t *testing.T) {
	for _, dffd := range []byte{0, 1, 2, 3, 5, 15} {
		m, err := New("", roms.ModelNext)
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		m.SetDFFD(dffd)
		bank := int(dffd)<<3 | int(m.port7FFD&0x07)

		m.Write(0xC000, 0xAB)
		if got := m.Read(0xC000); got != 0xAB {
			t.Errorf("DFFD=%d (bank %d): Read($C000) = %02X, want AB", dffd, bank, got)
		}
		if got := m.ram[bank][0]; got != 0xAB {
			t.Errorf("DFFD=%d: Write($C000) landed elsewhere; ram[%d][0] = %02X, want AB", dffd, bank, got)
		}
	}
}

// TestClassicROMSlotEncodingUnchanged guards the other side of the fix: the
// ">= 16 means ROM" page-map encoding still applies to every non-Next slot.
// The ZX80/ZX81 mirror their ROM into slot 2 ($8000-$BFFF), so narrowing the
// ROM branch to "$0000-$3FFF only" would have read unallocated RAM there.
func TestClassicROMSlotEncodingUnchanged(t *testing.T) {
	for _, model := range []roms.SpectrumModel{roms.ModelZX81, roms.ModelZX80} {
		m, err := New("", model)
		if err != nil {
			t.Skipf("%v ROM unavailable: %v", model, err)
		}
		if got, want := m.Read(0x8000), m.Read(0x0000); got != want {
			t.Errorf("%v: Read($8000) = %02X, want the ROM mirror %02X", model, got, want)
		}
	}
}

// TestDFFDHighBankROMHalfSlot locks in the same rule for the 8K MMU
// "ROM half" ($FF) path: MMU value $FF only means ROM in slots 0/1
// ($0000-$3FFF). A $FF in slots 2..7 must not resolve through the classic
// ROM dispatch, which would index m.rom with a $DFFD-extended RAM bank.
func TestDFFDHighBankROMHalfSlot(t *testing.T) {
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	m.SetDFFD(5) // slot 3 = bank 40
	m.SetMMU(6, 0xFF)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MMU slot 6 = $FF with DFFD bank 40: PANIC: %v", r)
		}
	}()
	_ = m.Read(0xC000)
}
