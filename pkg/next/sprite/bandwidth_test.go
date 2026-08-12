package sprite

import "testing"

// The sprite engine has a fixed time budget per scanline, and real hardware
// runs out of it. sprites.vhd walks the sprite list from a state machine
// clocked at the 28 MHz master clock:
//
//	S_START   -> S_QUALIFY                         (one clock)
//	S_QUALIFY -> one clock per sprite examined; S_PROCESS if it draws here
//	S_PROCESS -> one clock per pixel emitted, then back to S_QUALIFY
//
// and the overflow is reported, not hidden (sprites.vhd:977, 990):
//
//	sprites_overtime <= '1' when state_s /= S_IDLE and line_reset_re = "01"
//	status_reg_s(1)  <= status_reg_s(1) or sprites_overtime;
//
// i.e. if the walk has not finished by the time the line resets, bit 1 of the
// $303B status port latches. A line holds 2 columns per T-state and 4 master
// clocks per column, so the budget is TStatesPerLine*8 clocks.
//
// Neither half was modelled: the limit was unenforced and bit 1 always read 0,
// so software that reads the flag to throttle its own sprite use saw a machine
// that never saturates, and scenes that should visibly drop sprites rendered
// complete.

// fillLine makes n sprites visible on scanline 0, each 16 pixels wide.
func fillLine(e *Engine, n int) {
	e.SetEnabled(true)
	e.pattern[0] = 0x90 // one opaque pixel is enough to make it drawn
	for i := 0; i < n; i++ {
		s := e.Sprite(i)
		s.Visible, s.X, s.Y, s.Pattern = true, int16(i%300), 0, 0
	}
}

// TestModestSpriteCountDoesNotOvertime pins the normal case: a handful of
// sprites on a line must not report overflow.
func TestModestSpriteCountDoesNotOvertime(t *testing.T) {
	e := New()
	fillLine(e, 8)
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if e.ReadStatus()&0x02 != 0 {
		t.Error("8 sprites on a line reported max-per-line overflow")
	}
}

// TestTooManySpritesOnALineOvertimes pins the flag. 128 sprites 16 pixels wide
// need 128 qualify clocks plus 128*16 = 2048 emit clocks, well past the ~1824
// a 228-T-state line allows.
func TestTooManySpritesOnALineOvertimes(t *testing.T) {
	e := New()
	fillLine(e, MaxSprites)
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if e.ReadStatus()&0x02 == 0 {
		t.Error("128 full-width sprites on one line did not report max-per-line overflow")
	}
}

// TestOvertimeClearsOnRead pins the port semantics: like the collision bit,
// reading $303B clears it.
func TestOvertimeClearsOnRead(t *testing.T) {
	e := New()
	fillLine(e, MaxSprites)
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)
	if e.ReadStatus()&0x02 == 0 {
		t.Fatal("precondition: overflow should have latched")
	}
	if e.ReadStatus()&0x02 != 0 {
		t.Error("max-per-line bit did not clear on read")
	}
}

// TestSpritesBeyondTheBudgetAreDropped pins the visible consequence, which is
// the point: the hardware stops drawing when it runs out of line time, so a
// saturated scene loses sprites rather than rendering them all.
func TestSpritesBeyondTheBudgetAreDropped(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	e.pattern[0] = 0x90
	// Every sprite at a distinct x so a dropped one is visible as a gap.
	for i := 0; i < MaxSprites; i++ {
		s := e.Sprite(i)
		s.Visible, s.X, s.Y, s.Pattern = true, int16(i*2), 0, 0
	}
	dst := make([]byte, 320)
	e.RenderScanline(0, dst, 320)

	drawn := 0
	for i := 0; i < MaxSprites; i++ {
		if x := i * 2; x < len(dst) && dst[x] != 0 {
			drawn++
		}
	}
	if drawn == MaxSprites {
		t.Error("every one of 128 sprites was drawn; the line budget is not being enforced")
	}
	if drawn == 0 {
		t.Error("no sprites were drawn at all; the budget is too tight")
	}
	t.Logf("%d of %d sprites drawn before the line budget ran out", drawn, MaxSprites)
}
