package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestScreenAddrForRowColMatchesSpec exercises the scrambled
// Spectrum screen-memory address formula against well-known
// reference values from the ZX Spectrum technical literature.
// (0,0) → 0; (8,0) → 0x20; (1,0) → 0x100; (64,0) → 0x800;
// (191,31) → 0x57FF (last byte of the bitmap area).
func TestScreenAddrForRowColMatchesSpec(t *testing.T) {
	cases := []struct {
		row, col int
		want     int
	}{
		{0, 0, 0x0000},
		{1, 0, 0x0100},
		{2, 0, 0x0200},
		{8, 0, 0x0020},
		{64, 0, 0x0800},
		{128, 0, 0x1000},
		{191, 31, 0x17FF},
		{0, 31, 0x001F},
	}
	for _, c := range cases {
		got := screenAddrForRowCol(c.row, c.col)
		if got != c.want {
			t.Errorf("screenAddrForRowCol(%d, %d) = %#04x, want %#04x", c.row, c.col, got, c.want)
		}
	}
}

// newFloatingBusULA returns a 48K ULA with screen RAM
// pre-populated with a known pattern so floating-bus reads
// can be checked against expected values.
func newFloatingBusULA(t *testing.T) (*ULA, *memory.Memory) {
	t.Helper()
	testDir := "test_roms_floatingbus"
	createTestROMs(t, testDir)
	t.Cleanup(func() { cleanupTestROMs(testDir) })

	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}

	// Plant known bytes: bitmap area filled with 0xA5, attribute
	// area filled with 0x5A. Bank 5 is the displayed screen on
	// 48K.
	page := mem.GetPage(5)
	for i := 0; i < 0x1800; i++ {
		page[i] = 0xA5 // bitmap region
	}
	for i := 0x1800; i < 0x1B00; i++ {
		page[i] = 0x5A // attribute region
	}

	kbd := keyboard.New()
	u := New(mem, kbd)
	// Wire the T-state counter — without this the floating bus
	// returns 0xFF because there's no concept of "where in the
	// frame are we".
	var tstates uint64
	mem.TStates = &tstates
	return u, mem
}

// TestFloatingBusInBorderReturns0xFF verifies all border / retrace
// phases return 0xFF rather than randomly indexing into screen RAM.
func TestFloatingBusInBorderReturns0xFF(t *testing.T) {
	u, mem := newFloatingBusULA(t)

	// The 48K ULA uses 224 T-states/line (the documented value); the floating
	// bus origin is 64*224 = 14336.
	tpl := TStatesPerLineFor(roms.Model48K)

	// Top border: any T-state before line 64 starts.
	*mem.TStates = 0
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("top border (t=0): got %#x, want 0xFF", got)
	}
	*mem.TStates = uint64(63*tpl + 100)
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("top border end: got %#x, want 0xFF", got)
	}

	// Bottom border: after line 256.
	*mem.TStates = uint64(256 * tpl)
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("bottom border: got %#x, want 0xFF", got)
	}

	// Left border within a display line.
	*mem.TStates = uint64(64*tpl + 5)
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("left border: got %#x, want 0xFF", got)
	}

	// Right border within a display line.
	*mem.TStates = uint64(64*tpl + 24 + 128 + 5)
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("right border: got %#x, want 0xFF", got)
	}
}

// TestFloatingBusInDisplayReturnsScreenData verifies that during
// display T-states the floating bus returns either a bitmap byte
// (0xA5) or an attribute byte (0x5A), not 0xFF or random data.
func TestFloatingBusInDisplayReturnsScreenData(t *testing.T) {
	u, mem := newFloatingBusULA(t)

	// First display line, first display column. The 48K floating-bus origin
	// is 64*224 + leftBorder = 14336 + 24 (224-T-state lines).
	const leftBorder = 24
	base := uint64(64*TStatesPerLineFor(roms.Model48K) + leftBorder)

	// t%8 = 0,1,6,7: idle 0xFF; 2,4: bitmap (0xA5); 3,5: attribute (0x5A).
	for offset, want := range []byte{0xFF, 0xFF, 0xA5, 0x5A, 0xA5, 0x5A, 0xFF, 0xFF} {
		*mem.TStates = base + uint64(offset)
		got := u.floatingBusByte()
		if got != want {
			t.Errorf("display t-offset %d: got %#x, want %#x", offset, got, want)
		}
	}
}

// TestFloatingBusOnPlus3Returns0xFF verifies that +3 (and +2A)
// disable the floating bus per real hardware behaviour.
func TestFloatingBusOnPlus3Returns0xFF(t *testing.T) {
	testDir := "test_roms_floatingbus_plus3"
	createTestROMs(t, testDir)
	t.Cleanup(func() { cleanupTestROMs(testDir) })

	mem, err := memory.New(testDir, roms.ModelPlus3)
	if err != nil {
		t.Fatal(err)
	}
	page := mem.GetPage(5)
	for i := range page {
		page[i] = 0xAA // poison
	}
	kbd := keyboard.New()
	u := New(mem, kbd)
	var tstates uint64
	mem.TStates = &tstates

	// Position right where 48K would return 0xAA: middle of display.
	const leftBorder = 24
	*mem.TStates = uint64(64*TStatesPerLine + leftBorder + 4)
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("Plus3 should always return 0xFF, got %#x", got)
	}
}
