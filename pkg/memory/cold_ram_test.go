package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Cold-boot RAM must default to ZERO, matching the reference emulator (whose
// SDRAM powers up zeroed) and the de-facto faithful power-on state. An earlier
// pseudo-random ($C0FFEE xorshift) fill was a workaround for a since-fixed
// RAM-sizing banking bug; it made ours diverge from the reference across the whole
// 2 MB pool. The random pattern remains available behind ZX_GO_COLD_RANDOM
// for the uninitialised-read detector's "looks like garbage" use case.
func TestColdRAMZeroByDefault(t *testing.T) {
	t.Setenv("ZX_GO_COLD_RANDOM", "") // empty = disabled (and auto-restored)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	// An unloaded extended bank must be entirely zero.
	pg := m.GetPage(75)
	if pg == nil {
		t.Fatal("GetPage(75) nil — extended banks not allocated")
	}
	for _, off := range []int{0, 0x1234, 0x3FFF} {
		if pg[off] != 0x00 {
			t.Fatalf("cold RAM bank 75 [%#x] = %#x, want 0x00 (zero-fill default)", off, pg[off])
		}
	}
}

func TestColdRAMRandomOptIn(t *testing.T) {
	t.Setenv("ZX_GO_COLD_RANDOM", "1")
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	pg := m.GetPage(75)
	nonzero := false
	for _, b := range pg {
		if b != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("ZX_GO_COLD_RANDOM=1 should seed a non-zero cold-RAM pattern")
	}
}
