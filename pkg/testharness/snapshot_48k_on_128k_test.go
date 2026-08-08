package testharness

import (
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// A 48K snapshot restored on a 128K-family machine has to run against the 48K
// BASIC ROM with paging locked — the same state the 128K's own "48 BASIC"
// menu entry puts the machine in. Restoring only the RAM left whatever ROM
// happened to be paged, so the program executed against the 128K editor ROM
// and every ROM call (notably the character set and the print routines)
// produced garbage.
//
// Found by adding 128K entries to the golden corpus: the same conformance
// snapshot that renders readable text on the 48K rendered an illegible mess
// on the 128K.

// romAt returns the first n bytes the CPU sees at 0x0000.
func romAt(h *Harness, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = h.Memory(uint16(i))
	}
	return out
}

func TestSnapshot48KOnA128KMachinePagesTheBASICROM(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model roms.SpectrumModel
		rom   roms.ROMType
	}{
		{"128K", roms.Model128K, roms.ROM128K_1},
		{"+2", roms.ModelPlus2, roms.ROMPLUS2_1},
		{"+2A", roms.ModelPlus2A, roms.ROMPLUS2A_3},
		{"+3", roms.ModelPlus3, roms.ROMPLUS3_3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := New(tc.model)
			if err != nil {
				t.Fatalf("New(%v): %v", tc.model, err)
			}
			// Any 48K snapshot will do; this one is vendored already.
			path := filepath.Join("testdata", "corpus", "bin", "DIHalt.sna")
			if err := h.LoadSnapshot(path); err != nil {
				t.Fatalf("LoadSnapshot: %v", err)
			}

			want, ok := h.MemoryBus().GetROMManager().GetROM(tc.rom)
			if !ok {
				t.Skipf("%v ROM not available", tc.rom)
			}
			got := romAt(h, 16)
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("ROM at 0x%04X = %#02x, want %#02x — the 48 BASIC ROM is not paged in",
						i, got[i], want[i])
				}
			}
		})
	}
}

// TestSnapshot48KOnA128KMachineLocksPaging pins the other half: 48K software
// must not be able to page the machine out from under itself, so the restore
// sets the $7FFD paging-lock bit.
func TestSnapshot48KOnA128KMachineLocksPaging(t *testing.T) {
	h, err := New(roms.Model128K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.LoadSnapshot(filepath.Join("testdata", "corpus", "bin", "DIHalt.sna")); err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}

	before := romAt(h, 16)
	// Try to select ROM 0 (the 128K editor). With paging locked this must be
	// refused, exactly as on hardware.
	h.MemoryBus().PageMemory(0x00)
	if after := romAt(h, 16); !equalBytes(before, after) {
		t.Error("a $7FFD write changed the ROM after a 48K snapshot restore; paging was not locked")
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
