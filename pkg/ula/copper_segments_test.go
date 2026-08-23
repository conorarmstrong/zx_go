package ula

import "testing"

// The Copper used to be stepped once per scanline, at end-of-line hcount, so
// every MOVE on a line landed before the row was composited and a WAIT for a
// mid-line column had no effect at all. It was then stepped in 8-pixel
// segments, which resolves a WAIT (the column field is 6 bits of 8-pixel
// units) but not a MOVE: the Copper is clocked at 28 MHz against a 7 MHz
// hcount (zxnext.vhd:43,46,3944), so it retires an instruction every quarter
// of a pixel and a MOVE, which costs two clocks (device/copper.vhd:87,105),
// every half pixel. Its writes therefore land wherever they land across the
// line, not on an 8-pixel grid.

// copperStep is one Step call the render loop made.
type copperStep struct {
	scanline, hcount uint16
	clocks           int
}

// recordingCopper notes the raster positions and clock budgets it is stepped
// with, and spends nothing.
type recordingCopper struct {
	steps []copperStep
}

func (c *recordingCopper) Step(scanline, hcount uint16, maxClocks int) int {
	c.steps = append(c.steps, copperStep{scanline, hcount, maxClocks})
	return 0
}

func (c *recordingCopper) Wrote() bool { return false }

// TestCopperWalksEveryColumnOfTheLine pins that the Copper is clocked once per
// hcount column of the scanline, over the line's real column count.
//
// Two things this replaces. The walk used to present the raw display x as
// hcount, but the copper's hcount_i is hc_ula, whose zero point is twelve
// columns before the first displayed pixel (video/zxula_timing.vhd:423-424,
// stated outright at video/zxula.vhd:44-46), so every WAIT released twelve
// pixels late. And it used to finish the line at a fixed hcount 511, which no
// line ever reaches: hc_ula wraps at c_max_hc, 448 columns on 48K timing and
// 456 on 128K (video/zxula_timing.vhd:160,196), so a WAIT for a column past
// the end of the line must never release at all.
func TestCopperWalksEveryColumnOfTheLine(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &recordingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.applyNextCompositor()

	if len(c.steps) == 0 {
		t.Fatal("the Copper was never stepped")
	}

	columns := TStatesPerLineFor(u.mem.GetCurrentModel()) * 2
	var row0 []uint16
	for _, s := range c.steps {
		if s.scanline == 0 {
			row0 = append(row0, s.hcount)
		}
	}
	if len(row0) != columns {
		t.Fatalf("row 0 stepped %d times, want %d (one per hcount column)", len(row0), columns)
	}
	for i, got := range row0 {
		if got != uint16(i) {
			t.Fatalf("row 0 step %d was at hcount %d, want %d", i, got, i)
		}
	}
	// Displayed pixel 0 is hcount 12, so the pixel a WAIT for column X
	// releases at is 8X, not 8X+12.
	if row0[copperHCountOrigin] != uint16(copperHCountOrigin) {
		t.Errorf("displayed pixel 0 was stepped at hcount %d, want %d",
			row0[copperHCountOrigin], copperHCountOrigin)
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

// TestCopperIsPacedAtFourClocksPerColumn pins that the line's clock budget is
// handed out column by column rather than in one lump at the start of the row.
//
// The Copper is clocked from i_CLK_28 and its hcount ticks at i_CLK_7
// (zxnext.vhd:43,46,3944), so exactly four copper clocks pass per column and no
// more. Handing the whole line's budget to the first step let a burst of MOVEs
// retire entirely before pixel 0 that on hardware occupies half a pixel each,
// and banking whatever went unspent let a stalled Copper make the time up later.
func TestCopperIsPacedAtFourClocksPerColumn(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &recordingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.applyNextCompositor()

	for _, s := range c.steps {
		if s.clocks != copperClocksPerHCount {
			t.Fatalf("step at row %d hcount %d was offered %d copper clocks, want %d",
				s.scanline, s.hcount, s.clocks, copperClocksPerHCount)
		}
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

func (c *budgetCopper) Wrote() bool { return false }

// writeAtCopper raises its write pulse in exactly one hcount column of every
// row, standing in for a copper list whose MOVE happens to retire there, and
// advances a visible piece of state when it does.
type writeAtCopper struct {
	atHCount uint16
	state    *byte
	wrote    bool
}

func (c *writeAtCopper) Step(_, hcount uint16, _ int) int {
	c.wrote = hcount == c.atHCount
	if c.wrote {
		*c.state++
		return 2 // a MOVE costs two clocks
	}
	return 1
}

func (c *writeAtCopper) Wrote() bool { return c.wrote }

// stateStampCompositor paints the current value of the shared state into every
// pixel of the range it is asked to compose, so a test can read back which
// state each pixel of the finished row was composed under.
type stateStampCompositor struct {
	stubCompositor
	state *byte
}

func (c *stateStampCompositor) ComposeScanlineRange(_ int, _, dst []byte, from, to int) {
	for x := from; x < to; x++ {
		dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = *c.state, 0, 0, 0xFF
	}
}

// TestComposeSplitsAtThePixelTheCopperWroteIn pins that a row is composed as
// the Copper's writes divide it, not on a fixed grid: every pixel before the
// write shows the pre-write state and every pixel from the write on shows the
// post-write one.
//
// The write pulse lands on a single 28 MHz clock (device/copper.vhd:102-104),
// a quarter of a pixel, so the pixel being generated in that column is the
// first that can carry its effect. Composing in 8-pixel segments rounded that
// up to the next segment boundary, so a MOVE at pixel 100 first showed at 104.
func TestComposeSplitsAtThePixelTheCopperWroteIn(t *testing.T) {
	const writePixel = 100

	u, _ := newFloatingBusULA(t)
	var state byte
	u.SetNextCopper(&writeAtCopper{atHCount: writePixel + copperHCountOrigin, state: &state})
	u.SetNextCompositor(&stateStampCompositor{state: &state})

	u.applyNextCompositor()

	row := u.img.Pix[BorderTop*u.img.Stride+BorderLeft*4:]
	for x := 0; x < ScreenWidth; x++ {
		want := byte(0)
		if x >= writePixel {
			want = 1
		}
		if got := row[x*4]; got != want {
			t.Fatalf("row 0 pixel %d composed under state %d, want %d (the MOVE wrote in pixel %d)",
				x, got, want, writePixel)
		}
	}
}

// straddlingCopper spends one MOVE per column, except in column 0 where a WAIT
// releases first and the second MOVE therefore begins with a single clock left
// in the column: five clocks spent against the four the column holds.
type straddlingCopper struct {
	offered []int
}

func (c *straddlingCopper) Step(_, hcount uint16, maxClocks int) int {
	c.offered = append(c.offered, maxClocks)
	if hcount == 0 {
		return 5 // WAIT release + two MOVEs, the second straddling
	}
	return 2 // one MOVE
}

func (c *straddlingCopper) Wrote() bool { return true }

// TestAStraddlingMOVEIsPaidForOutOfTheNextColumn pins that clocks spent past
// the end of a column are taken off the next one.
//
// The copper does not restart its clock at a column boundary: it is clocked
// every 28 MHz tick and a MOVE simply occupies two of them
// (device/copper.vhd:87,105). One begun on a column's last clock finishes in
// the next column and leaves it three clocks, not four. Handing every column a
// fresh four let the Copper gain a quarter-pixel of work on every WAIT release,
// which over a line of releases drifts it above two MOVEs per pixel.
func TestAStraddlingMOVEIsPaidForOutOfTheNextColumn(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &straddlingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.applyNextCompositor()

	want := []int{4, 3, 4, 4}
	for i, w := range want {
		if c.offered[i] != w {
			t.Fatalf("column %d was offered %d clocks, want %d (offered = %v)",
				i, c.offered[i], w, c.offered[:len(want)])
		}
	}
	for i, got := range c.offered {
		if got > copperClocksPerHCount {
			t.Fatalf("column %d was offered %d clocks, more than the %d a column holds",
				i, got, copperClocksPerHCount)
		}
	}
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

// TestCopperIsClockedOnEveryLineOfTheFrame pins that the Copper gets the whole
// frame's clocks, not just the 192 displayed rows.
//
// Its vertical counter is cvc, which is loaded at the first active line and
// then counts EVERY line to c_max_vc (video/zxula_timing.vhd:455-466), so
// display row y is cvc y and rows 192 upward are real copper time. Nothing
// gates the copper on active video on the way: its entity has no display or
// blanking input at all (device/copper.vhd:28-42), its process is a plain
// rising_edge(clock_i) with no clock enable (device/copper.vhd:53-57),
// clock_i is the free-running i_CLK_28 (zxnext.vhd:43,3944), and the only
// enable is copper_en_i = nr_62_copper_mode, written solely from NextReg $62
// (zxnext.vhd:3947,5430). Stopping at row 191 under-clocked the Copper by
// well over a third of every frame.
func TestCopperIsClockedOnEveryLineOfTheFrame(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &recordingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.applyNextCompositor()

	model := u.mem.GetCurrentModel()
	columns := TStatesPerLineFor(model) * 2
	lines := linesPerFrameFor(model)

	perLine := map[uint16]int{}
	for _, s := range c.steps {
		perLine[s.scanline]++
	}
	if len(perLine) != lines {
		t.Fatalf("the Copper was clocked on %d lines, want %d (the whole frame)", len(perLine), lines)
	}
	for y := 0; y < lines; y++ {
		if got := perLine[uint16(y)]; got != columns {
			t.Fatalf("line %d was clocked %d times, want %d (one per column)", y, got, columns)
		}
	}
}

// frameEndStraddlingCopper begins a MOVE on the last clock of the frame's last
// column, so one of its two clocks falls beyond the end of the frame. It notes
// what every frame's first column was offered.
type frameEndStraddlingCopper struct {
	lastLine, lastColumn uint16
	firstColumnOffered   []int
}

func (c *frameEndStraddlingCopper) Step(scanline, hcount uint16, maxClocks int) int {
	if scanline == 0 && hcount == 0 {
		c.firstColumnOffered = append(c.firstColumnOffered, maxClocks)
	}
	if scanline == c.lastLine && hcount == c.lastColumn {
		return 5 // four clocks of work plus a MOVE begun on the last one
	}
	return 1
}

func (c *frameEndStraddlingCopper) Wrote() bool { return false }

// TestAStraddlingMOVEIsPaidForAcrossTheFrameBoundary pins that the straddle
// debt survives the end of the frame.
//
// The copper is clocked from the free-running i_CLK_28 (zxnext.vhd:43,3944) and
// nothing in device/copper.vhd restarts anything on a frame edge: the only
// frame-start action is the mode-11 address reset (device/copper.vhd:80-83),
// which moves the program counter and not the clock. So a MOVE begun on the
// frame's last clock finishes in the next frame's first column and leaves it
// three clocks, exactly as it would at a line or column boundary. Zeroing the
// debt per frame handed the copper a free quarter-pixel every 50th of a second.
func TestAStraddlingMOVEIsPaidForAcrossTheFrameBoundary(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	model := u.mem.GetCurrentModel()
	c := &frameEndStraddlingCopper{
		lastLine:   uint16(linesPerFrameFor(model) - 1),
		lastColumn: uint16(TStatesPerLineFor(model)*2 - 1),
	}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.applyNextCompositor()
	u.applyNextCompositor()

	want := []int{copperClocksPerHCount, copperClocksPerHCount - 1}
	if len(c.firstColumnOffered) != len(want) {
		t.Fatalf("first columns offered = %v, want %d entries", c.firstColumnOffered, len(want))
	}
	for i, w := range want {
		if c.firstColumnOffered[i] != w {
			t.Errorf("frame %d column 0 was offered %d clocks, want %d (offered = %v)",
				i, c.firstColumnOffered[i], w, c.firstColumnOffered)
		}
	}
}
