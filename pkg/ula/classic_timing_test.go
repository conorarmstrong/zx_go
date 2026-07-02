package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Classic ULA display-timing conformance, pinned to the canonical, well-
// documented Spectrum timing references (NOT a competitor emulator):
//
//   - Sean Young, "Z80 Family CPU Undocumented Features" / "The Undocumented
//     Z80 Documented" — ULA contention timing notes.
//   - The "Contended memory" reference page cited inside the FPGA core itself
//     (video/zxula.vhd:34, faqwiki "Contended_memory").
//   - Ramsoft, "The ZX Spectrum floating bus explained" (1999) — the
//     unattached-port read value algorithm.
//   - Chris Smith, "The ZX Spectrum ULA" (ISBN 978-0-9565071-0-5), cited in
//     video/zxula_timing.vhd:6.
//
// These pin the per-machine line length, the contention window start T-state
// and delay pattern, and the floating-bus value — the classic-machine facts
// the FPGA route cannot pin for 48K/128K/+3 (the FPGA frame origin and
// synchronous wait differ by design; see fpga_timing_golden_test.go).

// TestClassic48KFrameGeometry pins the documented 48K frame geometry:
// 312 scanlines of 224 T-states = 69888 T-states/frame, with 64 lines of top
// border before the 192-line display. The 128K family is 311 lines of 228 =
// 70908. (Sean Young timing notes; Chris Smith, "The ZX Spectrum ULA".)
func TestClassic48KFrameGeometry(t *testing.T) {
	cases := []struct {
		model         roms.SpectrumModel
		tPerLine      int
		linesPerFrame int
		frameTotal    int
	}{
		{roms.Model48K, 224, 312, 69888},
		{roms.Model128K, 228, 311, 70908},
		{roms.ModelPlus2, 228, 311, 70908},
		{roms.ModelPlus3, 228, 311, 70908},
	}
	for _, c := range cases {
		got := TStatesPerLineFor(c.model)
		if got != c.tPerLine {
			t.Errorf("TStatesPerLineFor(%v) = %d, want %d", c.model, got, c.tPerLine)
		}
		if c.tPerLine*c.linesPerFrame != c.frameTotal {
			t.Errorf("%v geometry inconsistent: %d*%d=%d, want %d",
				c.model, c.tPerLine, c.linesPerFrame, c.tPerLine*c.linesPerFrame, c.frameTotal)
		}
	}
}

// TestClassicContentionWindowAndPattern pins, as a spec test, the documented
// 48K contention window and delay sequence:
//
//   - The first contended T-state of the frame is 14335 (the T-state at which
//     the ULA begins to hold the CPU for the first display line). Documented
//     by the "Contended memory" reference and Sean Young's notes.
//   - The contention applies for the first 128 T-states of each of the 192
//     display lines.
//   - The per-T-state delay pattern repeats every 8 T-states as
//     6,5,4,3,2,1,0,0.
//
// This is the ULA-side timing fact; the per-opcode application lives in
// pkg/z80 and is tested there (not duplicated here).
func TestClassicContentionWindowAndPattern(t *testing.T) {
	want := [8]int{6, 5, 4, 3, 2, 1, 0, 0}
	if classicContentionPattern != want {
		t.Fatalf("classic contention pattern = %v, want %v", classicContentionPattern, want)
	}

	// Cross-check the documented active-run structure: 6 nonzero (contended)
	// followed by 2 zero (uncontended) per 8-T period.
	nonzero := 0
	for _, d := range classicContentionPattern {
		if d != 0 {
			nonzero++
		}
	}
	if nonzero != 6 {
		t.Errorf("documented pattern has %d contended T-states per 8, want 6", nonzero)
	}

	// The documented window: first contended T-state 14335, 192 lines, 128 T
	// per line on a 224-T/line frame. Confirm the constants compose.
	const firstContended = 14335
	const topBorderLines = 64
	const tPerLine48K = 224
	// 14335 is one T-state before the 64th line's display fetch begins; the
	// ULA prefetches the first byte one T-state early (the canonical "-1").
	if firstContended != topBorderLines*tPerLine48K-1 {
		t.Errorf("firstContended %d != 64*224-1 = %d", firstContended, topBorderLines*tPerLine48K-1)
	}

	// Tie the spec to PRODUCTION: drive the real memory backend's
	// ContendMemory at the documented window start and confirm the delay it
	// charges follows 6,5,4,3,2,1,0,0. This proves ours' shipping contention
	// (pkg/memory) matches the documented pattern, not just a local mirror.
	testDir := "test_roms_classic_contend"
	createTestROMs(t, testDir)
	t.Cleanup(func() { cleanupTestROMs(testDir) })
	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	var ts uint64
	mem.SetTStatePtr(&ts) // enables contention on 48K

	for i, want := range classicContentionPattern {
		ts = uint64(firstContended + i)
		before := ts
		mem.ContendMemory(0x4000) // a contended screen-RAM access
		got := int(ts - before)
		if got != want {
			t.Errorf("ContendMemory at T=%d (pattern slot %d): delay %d, want %d",
				firstContended+i, i, got, want)
		}
	}

	// One T-state before the window: no contention.
	ts = uint64(firstContended - 1)
	b := ts
	mem.ContendMemory(0x4000)
	if d := int(ts - b); d != 0 {
		t.Errorf("ContendMemory at T=%d (pre-window): delay %d, want 0", firstContended-1, d)
	}

	// An uncontended address inside the window: no contention.
	ts = uint64(firstContended)
	b = ts
	mem.ContendMemory(0x8000) // not screen RAM
	if d := int(ts - b); d != 0 {
		t.Errorf("ContendMemory(0x8000) in-window: delay %d, want 0 (uncontended addr)", d)
	}
}

// TestClassicFloatingBus48KOrigin pins the documented 48K floating-bus origin:
// the first display byte the floating bus can return is fetched at T-state
// 14336 (64 lines * 224 T). Ramsoft's algorithm and Sean Young's notes place
// the first paper fetch there. With ours' (previously 128K-sized) 228-T line
// the origin landed at 14592 — wrong by a full 256 T-states for 48K, the kind
// of error that mis-times an Arkanoid/Cobra floating-bus read.
func TestClassicFloatingBus48KOrigin(t *testing.T) {
	u, mem := newFloatingBusULA(t)

	const tPerLine48K = 224
	// One T-state before the first paper fetch: still idle/border → 0xFF.
	*mem.TStates = uint64(64*tPerLine48K - 1)
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("T=14335 (pre-display): got %#x, want 0xFF", got)
	}

	// The first paper line, first display column. The fetch phase pattern
	// (idle,idle,bitmap,attr,bitmap,attr,idle,idle) must apply at the
	// 224-T-line origin, not the 228-T one.
	base := uint64(64 * tPerLine48K)
	const leftBorder = 24
	for offset, want := range []byte{0xFF, 0xFF, 0xA5, 0x5A, 0xA5, 0x5A, 0xFF, 0xFF} {
		*mem.TStates = base + uint64(leftBorder) + uint64(offset)
		got := u.floatingBusByte()
		if got != want {
			t.Errorf("48K display t-offset %d (origin 14336): got %#x, want %#x", offset, got, want)
		}
	}

	// The bottom border (after 192 display lines from the 224-T origin) must
	// return 0xFF, not screen data.
	*mem.TStates = base + uint64((192)*tPerLine48K)
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("bottom border (224-T origin): got %#x, want 0xFF", got)
	}
}

// TestClassicBorderChangeScanlineUsesPerModelLineLength pins that a port
// 0xFE border-colour write is converted to a scanline using the model's own
// T-states-per-line (TStatesPerLineFor: 224 on 48K, 228 on 128K+) — the same
// per-model line length the floating bus uses (TestClassicFloatingBus48KOrigin).
// A 48K border write timestamped at exactly scanline 50 (50*224 T-states)
// must take effect starting at that scanline's row, not one row earlier
// (which is what dividing by the 128K's 228-T line would give).
func TestClassicBorderChangeScanlineUsesPerModelLineLength(t *testing.T) {
	testDir := "test_roms_classic_border_scanline"
	createTestROMs(t, testDir)
	t.Cleanup(func() { cleanupTestROMs(testDir) })

	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	var ts uint64
	mem.TStates = &ts
	u := New(mem, keyboard.New())

	const tPerLine48K = 224
	const targetScanline = 50 // within the displayed top border (frame scanlines 40..63)
	ts = uint64(targetScanline * tPerLine48K)
	u.WritePort(0xFE, 0x03) // border = magenta

	img := u.Render()

	const wantRow = targetScanline - (64 - BorderTop) // 50 - 40 = 10
	gotNew := img.RGBAAt(0, wantRow)
	wantNew := u.palette[0x03]
	if gotNew != wantNew {
		t.Errorf("row %d (the new-colour scanline): got %v, want new border colour %v", wantRow, gotNew, wantNew)
	}
	gotOld := img.RGBAAt(0, wantRow-1)
	wantOld := u.palette[0] // initial BorderColour is 0 (black)
	if gotOld != wantOld {
		t.Errorf("row %d (one scanline before the change): got %v, want old border colour %v (the change landed a row early)", wantRow-1, gotOld, wantOld)
	}
}

// TestClassicBorderColourCarriesOverBetweenFrames pins two related facts
// about the per-scanline border map: a mid-frame border write must not
// retroactively paint the scanlines before it (BorderColour is mutated live
// by WritePort, so naively using it as the row-0 baseline paints the whole
// border with the new colour from the start of the frame), and a frame with
// no border writes at all must render the colour the previous frame ended on.
func TestClassicBorderColourCarriesOverBetweenFrames(t *testing.T) {
	testDir := "test_roms_classic_border_carryover"
	createTestROMs(t, testDir)
	t.Cleanup(func() { cleanupTestROMs(testDir) })

	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	var ts uint64
	mem.TStates = &ts
	u := New(mem, keyboard.New())

	// Frame 1: border starts black; a write partway through the frame
	// changes it to red. Row 0 (before the change) must still be black.
	const tPerLine48K = 224
	ts = uint64(100 * tPerLine48K)
	u.WritePort(0xFE, 0x02) // border = red
	img := u.Render()
	if got, want := img.RGBAAt(0, 0), u.palette[0]; got != want {
		t.Errorf("frame 1, row 0 (before the change): got %v, want black %v (retroactively painted)", got, want)
	}

	// Frame 2: no border writes at all. The whole border must render red —
	// the colour frame 1 ended on — not black.
	ts = 0
	img = u.Render()
	if got, want := img.RGBAAt(0, 0), u.palette[0x02]; got != want {
		t.Errorf("frame 2, row 0 (no writes this frame): got %v, want carried-over red %v", got, want)
	}
}

// TestClassicFloatingBusPlus3Disabled pins that the +2A/+3 ULA has no floating
// bus (always 0xFF) — a documented hardware difference (the +3 ULA gates the
// data bus). Mirrors the existing TestFloatingBusOnPlus3Returns0xFF but states
// the classic-route rationale explicitly.
func TestClassicFloatingBusPlus3Disabled(t *testing.T) {
	testDir := "test_roms_classic_p3"
	createTestROMs(t, testDir)
	t.Cleanup(func() { cleanupTestROMs(testDir) })

	mem, err := memory.New(testDir, roms.ModelPlus3)
	if err != nil {
		t.Fatal(err)
	}
	page := mem.GetPage(5)
	for i := range page {
		page[i] = 0xAA
	}
	u := New(mem, keyboard.New())
	var ts uint64
	mem.TStates = &ts
	*mem.TStates = uint64(64*224 + 24 + 2) // would be a bitmap fetch on 48K
	if got := u.floatingBusByte(); got != 0xFF {
		t.Errorf("+3 floating bus: got %#x, want 0xFF (no floating bus on +2A/+3)", got)
	}
}
