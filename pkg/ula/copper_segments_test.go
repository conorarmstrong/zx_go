package ula

import "testing"

// The Copper used to be stepped once per scanline, at end-of-line hcount, so
// every MOVE on a line landed before the row was composited and a WAIT for a
// mid-line column had no effect at all.
//
// It does not need per-pixel rendering to fix. The WAIT column field is 6 bits
// taken as 8-pixel units (copper.vhd:94, WaitHThreshold = x<<3 + 12), so the
// Copper's own horizontal resolution IS 8 pixels. Stepping it and compositing
// in 8-pixel segments is therefore exact at the hardware's granularity, not an
// approximation.

// recordingCopper notes the raster positions it is stepped at.
type recordingCopper struct {
	steps []struct{ scanline, hcount uint16 }
}

func (c *recordingCopper) Step(scanline, hcount uint16, maxInstr int) int {
	c.steps = append(c.steps, struct{ scanline, hcount uint16 }{scanline, hcount})
	return 0
}

// TestCopperIsSteppedAcrossTheLine pins that the Copper sees intra-line raster
// positions, at the 8-pixel granularity its WAIT field resolves to.
func TestCopperIsSteppedAcrossTheLine(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &recordingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.applyNextCompositor()

	if len(c.steps) == 0 {
		t.Fatal("the Copper was never stepped")
	}

	// Collect the distinct hcounts used on the first display row.
	seen := map[uint16]bool{}
	for _, s := range c.steps {
		if s.scanline == 0 {
			seen[s.hcount] = true
		}
	}
	if len(seen) < ScreenWidth/8 {
		t.Errorf("row 0 stepped at %d distinct hcounts, want at least %d (one per 8-pixel segment)",
			len(seen), ScreenWidth/8)
	}
	// The segment boundaries themselves must be present.
	for _, want := range []uint16{0, 8, 64, 128, 248} {
		if !seen[want] {
			t.Errorf("row 0 was never stepped at hcount %d", want)
		}
	}
	// And the line must still be finished off at end-of-line, so a WAIT for a
	// column beyond the visible area still releases within its own line.
	if !seen[511] {
		t.Error("row 0 was never stepped at end-of-line hcount 511")
	}
}

// TestCopperStepsStayWithinTheLineBudget pins that segmenting does not
// multiply the per-scanline instruction budget: the whole line still shares
// one allowance.
func TestCopperStepsStayWithinTheLineBudget(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &budgetCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.applyNextCompositor()

	perLine := TStatesPerLineFor(u.mem.GetCurrentModel()) * 2 * 4 / 2
	if c.maxPerLine > perLine {
		t.Errorf("a single scanline retired %d instructions, want at most %d",
			c.maxPerLine, perLine)
	}
}

// budgetCopper executes one instruction per step and counts how many it is
// allowed to retire within a single scanline. The offered cap is not the
// interesting number — a Copper that executes nothing is correctly offered the
// full remainder again — so this measures actual consumption.
type budgetCopper struct {
	line       uint16
	inLine     int
	maxPerLine int
}

func (c *budgetCopper) Step(scanline, hcount uint16, maxInstr int) int {
	if scanline != c.line {
		c.line, c.inLine = scanline, 0
	}
	if maxInstr <= 0 {
		return 0
	}
	c.inLine++
	if c.inLine > c.maxPerLine {
		c.maxPerLine = c.inLine
	}
	return 1
}

// stubCompositor satisfies the ULA's compositor contract without drawing.
type stubCompositor struct{}

func (stubCompositor) ComposeScanline(int, []byte, []byte)                {}
func (stubCompositor) ComposeScanlineRange(int, []byte, []byte, int, int) {}
func (stubCompositor) ComposeBorderRow(int, []byte, func(int) bool)       {}
func (stubCompositor) ComposeSpriteBorderRow(int, []byte, func(int) bool) {}
func (stubCompositor) ComposeWideTilemapRow(int, []byte)                  {}
func (stubCompositor) ComposeWideLayer2Row(int, []byte)                   {}
func (stubCompositor) HasActiveTilemap() bool                             { return false }
func (stubCompositor) HasActiveSprites() bool                             { return false }
func (stubCompositor) TilemapIs80Col() bool                               { return false }
func (stubCompositor) HiResLayer2Active() bool                            { return false }
func (stubCompositor) Layer2Width() int                                   { return 256 }
