package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// trdosTestROM returns a 16K blob with a recognisable per-byte pattern so a
// read can be attributed to the TR-DOS ROM rather than the system ROM.
func trdosTestROM() []byte {
	rom := make([]byte, PageSize)
	for i := range rom {
		rom[i] = byte((i + 0x40) & 0xFF)
	}
	return rom
}

// When the Beta ROM is active, $0000-$3FFF reads come from the TR-DOS ROM;
// $4000+ is untouched, and deactivating restores the system ROM.
func TestBetaROMReadOverride(t *testing.T) {
	m := newTestMemory(t, roms.Model48K)
	trdos := trdosTestROM()
	m.EnableBeta(trdos)

	m.SetBetaActive(true)
	for _, addr := range []uint16{0x0000, 0x0100, 0x3DFF, 0x3FFF} {
		if got := m.Read(addr); got != trdos[addr] {
			t.Errorf("active: Read($%04X) = %02X, want TR-DOS %02X", addr, got, trdos[addr])
		}
	}
	// RAM above the ROM window is never overridden.
	m.Write(0x4000, 0xAB)
	if got := m.Read(0x4000); got != 0xAB {
		t.Errorf("active: Read($4000) = %02X, want RAM 0xAB", got)
	}

	m.SetBetaActive(false)
	if got := m.Read(0x0100); got == trdos[0x0100] {
		t.Errorf("inactive: Read($0100) still returns TR-DOS byte %02X", got)
	}
}

// The auto-paging state machine: an instruction fetch in $3Dxx pages TR-DOS in
// (while the 48 BASIC ROM is mapped); a fetch at $4000+ pages it out.
func TestBetaAutoPagingStateMachine(t *testing.T) {
	m := newTestMemory(t, roms.Model48K) // 48 BASIC always mapped at $0000
	m.EnableBeta(trdosTestROM())

	if m.IsBetaActive() {
		t.Fatal("Beta should start inactive")
	}
	m.BetaPreFetch(0x3D00) // enter TR-DOS entry vector
	if !m.IsBetaActive() {
		t.Error("fetch at $3D00 did not page TR-DOS in")
	}
	// Executing within the ROM window keeps it paged.
	m.BetaPreFetch(0x0123)
	if !m.IsBetaActive() {
		t.Error("Beta paged out while executing in the ROM window")
	}
	m.BetaPreFetch(0x4000) // return to RAM
	if m.IsBetaActive() {
		t.Error("fetch at $4000 did not page TR-DOS out")
	}
}

// On a 128K-class machine the auto-paging only triggers while the 48 BASIC ROM
// is selected — not while the 128 editor ROM (ROM 0) is mapped.
func TestBetaAutoPagingGatedByBasicROM(t *testing.T) {
	m := newTestMemory(t, roms.Model128K)
	m.EnableBeta(trdosTestROM())

	// 128K boots with the editor ROM (ROM 0) mapped: $3Dxx must NOT page in.
	m.BetaPreFetch(0x3D00)
	if m.IsBetaActive() {
		t.Error("Beta paged in while the 128 editor ROM was mapped")
	}

	// Page in the 48 BASIC ROM (7FFD bit 4), then $3Dxx pages TR-DOS in.
	m.PageMemory(0x10)
	m.BetaPreFetch(0x3D00)
	if !m.IsBetaActive() {
		t.Error("Beta did not page in once the 48 BASIC ROM was selected")
	}
}

// With Beta disabled the pre-fetch hook is inert.
func TestBetaDisabledIsInert(t *testing.T) {
	m := newTestMemory(t, roms.Model48K)
	m.BetaPreFetch(0x3D00)
	if m.IsBetaActive() {
		t.Error("Beta activated despite never being enabled")
	}
}
