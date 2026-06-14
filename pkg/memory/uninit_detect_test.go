package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// iter 371: uninitialised-RAM read detector. After
// EnableUninitTracking, a guest Write marks the byte written; a Read
// of a never-written byte fires UninitReadObserver. The power-on
// cold-RAM fill must NOT count as written (else the detector would be
// silent). Default (tracking off) must fire nothing — zero overhead.

func TestUninitDetect_FiresOnUnwrittenReadOnly(t *testing.T) {
	dir := "test_roms_uninit"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)
	m, err := New(dir, roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	// Map RAM bank 0 into slot 3 ($C000-$FFFF) via 128K paging so
	// reads/writes there hit a RAM bank (slot 0-2 may be ROM/screen).
	m.PageMemory(0) // 7FFD=0 → bank 0 at $C000

	type hit struct {
		addr uint16
		bank int
		off  uint16
	}
	var hits []hit
	m.UninitReadObserver = func(addr uint16, bank int, off uint16) {
		hits = append(hits, hit{addr, bank, off})
	}

	// Tracking OFF → reads fire nothing.
	_ = m.Read(0xC000)
	if len(hits) != 0 {
		t.Fatalf("tracking off: observer fired %d times, want 0", len(hits))
	}

	m.EnableUninitTracking()

	// Write one byte, read it back → no fire (it is now written).
	m.Write(0xC000, 0xAB)
	if got := m.Read(0xC000); got != 0xAB {
		t.Fatalf("readback = $%02X, want $AB", got)
	}
	for _, h := range hits {
		if h.addr == 0xC000 {
			t.Errorf("written byte $C000 fired uninit observer")
		}
	}

	// Read a DIFFERENT, never-written byte → must fire.
	before := len(hits)
	_ = m.Read(0xC001)
	if len(hits) != before+1 {
		t.Fatalf("unwritten $C001 read: fired %d, want exactly 1 new", len(hits)-before)
	}
	if hits[len(hits)-1].addr != 0xC001 {
		t.Errorf("fired for addr $%04X, want $C001", hits[len(hits)-1].addr)
	}

	// Reading the same unwritten byte again still fires (every read,
	// not just first) — caller dedups if it wants.
	before = len(hits)
	_ = m.Read(0xC001)
	if len(hits) != before+1 {
		t.Errorf("second unwritten read fired %d, want 1", len(hits)-before)
	}
}

// Power-on cold-RAM fill must not be counted as guest writes — a
// freshly tracked, never-guest-written byte fires regardless of the
// non-zero fill value.
func TestUninitDetect_ColdFillNotCountedAsWritten(t *testing.T) {
	dir := "test_roms_uninit2"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)
	m, err := New(dir, roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	m.PageMemory(0)
	m.EnableUninitTracking()
	fired := false
	m.UninitReadObserver = func(uint16, int, uint16) { fired = true }
	_ = m.Read(0xC123) // never written by guest
	if !fired {
		t.Error("cold-fill byte read did not fire — fill wrongly counted as written")
	}
}
