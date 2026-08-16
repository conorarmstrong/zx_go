package sprite

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"testing"
)

// The sprite engine is the device that proves why a rewind needs more than the
// picture's ingredients.
//
// The 128 attribute records and the 16 KB pattern RAM are what a guest ends up
// with. They are not what the engine is doing: a game uploads its whole sprite
// bank every frame through two byte streams, and a capture taken between two
// bytes of one record has to remember which sprite that record belongs to, how
// far into it the stream has got, and where in pattern RAM the other stream is
// writing. Restore those at rest and the machine resumes by putting the rest of
// the record into the wrong sprite and the rest of the pattern into the wrong
// slot.
//
// So these tests are a replay property rather than a field-by-field comparison,
// and the fixture is deliberately stopped MID-TRANSFER on both streams. A
// field-by-field test only checks the fields someone remembered to add, and a
// fixture captured at rest proves nothing about resuming.

const stateTestWidth = 320

// primed returns an engine part way through both uploads, with both sticky
// $303B latches raised, and with a sprite bank whose rendering depends on the
// clip window, the priority mode and the anchor path.
func primed() *Engine {
	e := New()
	e.SetEnabled(true)
	e.SetZeroOnTop(true)
	e.SetOverBorder(true)
	e.SetBorderClip(true)
	// Resolves to x 0..111, y 40..95: narrow enough that it cuts the sprites
	// below, so the clip fields are load-bearing in the rendered output.
	e.SetClip(0, 55, 40, 95)
	e.SetLineClockBudget(1200)

	// Pattern slots 1 and 2. Every byte is non-zero on purpose: palette index 0
	// is transparent, so a pattern with zeroes in it would quietly take pixels
	// out of the rendered output and collisions out of the collision detector.
	e.SetPatternAddr(1 * 256)
	for i := 0; i < 512; i++ {
		e.WritePatternByte(byte(i%251) + 1)
	}

	// Sprite 0 is an anchor, sprite 1 is relative to it (byte-4 bits 7:6 == 01),
	// sprite 2 overlaps both. The overlap is what exercises the priority mode
	// and raises the collision latch.
	e.Set(0, Attr{X: 100, Y: 60, Pattern: 1, Palette: 1, Visible: true})
	e.Set(1, Attr{X: 8, Y: 4, Pattern: 1, Palette: 4, Visible: true, Extended: true, Byte4: 0x40})
	e.Set(2, Attr{X: 108, Y: 62, Pattern: 1, Palette: 2, Visible: true})
	// Sprite 3 sits across the top-left corner of the clip window (x 8..23,
	// y 32..47, against a window starting at x 0 / y 40), so the x1 and y1 clip
	// edges cut it. Without it those two edges could be left out of the restore
	// and nothing drawn would move.
	e.Set(3, Attr{X: 8, Y: 32, Pattern: 1, Palette: 6, Visible: true})

	// Raise both sticky latches: an overlap for the collision bit, then a line
	// with no time in it for the max-sprites-per-line bit.
	dst := make([]byte, stateTestWidth)
	e.RenderScanline(64, dst, stateTestWidth)
	e.SetLineClockBudget(10)
	e.RenderScanline(64, dst, stateTestWidth)
	e.SetLineClockBudget(1200)

	// Pattern upload stopped in the middle of slot 2, at offset 612.
	e.SelectSlot(0x02)
	for i := 0; i < 100; i++ {
		e.WritePatternByte(byte(0x40 + i))
	}

	// Attribute upload stopped after byte 3 of sprite 9's record, with byte 3
	// bit 6 set: a fifth byte is still to come, so the cursor is parked at 4 and
	// the sprite has not auto-advanced yet.
	e.SelectSprite(9)
	e.WriteAttr(60)   // byte 0: X low
	e.WriteAttr(64)   // byte 1: Y
	e.WriteAttr(0x30) // byte 2: palette 3, no mirror/rotate, X8 clear
	e.WriteAttr(0xC1) // byte 3: visible + fifth byte follows + pattern slot 1
	return e
}

// drive runs the engine forward the way the guest would and records everything
// observable: the sticky latches as the capture left them, then the frame drawn
// after both interrupted uploads finish, then the latches the replay raised.
func drive(e *Engine) []byte {
	out := make([]byte, 0, 72*stateTestWidth+2)
	out = append(out, e.ReadStatus())

	// Finish sprite 9's half-written record, then upload a whole four-byte
	// record for the sprite the cursor auto-advances to. Byte 4 carries a 2x X
	// scale rather than zero, so that a byte landing on the wrong sprite shows
	// up in the picture instead of writing a value the sprite already held.
	e.WriteAttr(0x08) // byte 4: 8bpp, X scale 2x, Y MSB clear
	for _, b := range []byte{32, 80, 0x50, 0x82} {
		e.WriteAttr(b)
	}

	// Finish the interrupted pattern upload: rows 6..9 of slot 2, which is what
	// the sprite that record describes draws from.
	for i := 0; i < 60; i++ {
		e.WritePatternByte(byte(0xA4 + i))
	}

	dst := make([]byte, stateTestWidth)
	for y := 28; y < 100; y++ {
		for i := range dst {
			dst[i] = 0
		}
		e.RenderScanline(y, dst, stateTestWidth)
		out = append(out, dst...)
	}
	out = append(out, e.ReadStatus())
	return out
}

// scramble carries the engine somewhere else entirely, so that every captured
// field differs from the live one at the moment of restore. Without it a field
// left out of LoadState would keep whatever value it already had, and half the
// capture would go untested.
func scramble(e *Engine) {
	e.SetEnabled(false)
	e.SetZeroOnTop(false)
	e.SetOverBorder(false)
	e.SetBorderClip(false)
	e.SetClip(10, 20, 30, 40)
	e.SetLineClockBudget(37)
	for i := 0; i < MaxSprites; i++ {
		e.Set(i, Attr{})
	}
	e.SetPatternAddr(1 * 256)
	for i := 0; i < 600; i++ {
		e.WritePatternByte(0x5A)
	}
	e.SelectSprite(77)
	e.WriteAttr(0x11)
	e.ReadStatus() // clears both latches, so a restored latch has to come back
}

// The property that matters: from a captured state, the engine must draw what
// it drew the first time, having resumed both interrupted uploads.
func TestReplayingFromCapturedStateReproducesTheSameFrames(t *testing.T) {
	e := primed()

	st := e.SaveState()
	first := drive(e)

	scramble(e)
	if err := e.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := drive(e)

	if !bytes.Equal(first, second) {
		t.Error("the sprite engine drew a different frame on replay from the same captured state: " +
			"some part of the engine is not being captured")
	}
}

// The negative that gives the test above its teeth. The attribute bank and the
// pattern RAM do round-trip, so a capture of those alone would still pass a
// weak test. This proves the drawn frame depends on more than them.
func TestAttributesAndPatternsAloneAreNotEnough(t *testing.T) {
	src := primed()
	st := src.SaveState()
	want := drive(src)

	// Rebuild an engine from the bank and the registers only, the way a capture
	// that stopped at "what the guest configured" would.
	pristine := primed()
	b := New()
	b.SetEnabled(true)
	b.SetZeroOnTop(true)
	b.SetOverBorder(true)
	b.SetBorderClip(true)
	b.SetClip(0, 55, 40, 95)
	b.SetLineClockBudget(1200)
	for i := 0; i < MaxSprites; i++ {
		b.Set(i, *pristine.Sprite(i))
	}
	b.SetPatternAddr(0)
	for i := 0; i < PatternRAMSize; i++ {
		b.WritePatternByte(pristine.PatternByte(uint16(i)))
	}

	if bytes.Equal(want, drive(b)) {
		t.Fatal("the bank alone reproduced the frame, so the replay test above is not measuring the cursors and latches")
	}

	// ...and the full capture does what the bank alone could not.
	if err := src.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !bytes.Equal(want, drive(src)) {
		t.Error("full state capture failed to reproduce the frame it was taken from")
	}
}

// The fixture has to be what it claims to be. The trap this guards against is a
// fixture that looks busy but draws nothing and stops mid-nothing: every replay
// comparison would pass on a blank frame and prove exactly nothing.
func TestPrimedFixtureIsMidTransferAndActuallyDraws(t *testing.T) {
	e := primed()

	if e.attrCursor != 4 || !e.attrExtended {
		t.Errorf("fixture is not mid-record: attrCursor=%d attrExtended=%v, want 4/true",
			e.attrCursor, e.attrExtended)
	}
	if e.selSprite != 9 {
		t.Errorf("fixture selSprite = %d, want 9", e.selSprite)
	}
	if e.selPattAddr != 612 {
		t.Errorf("fixture is not mid-pattern-upload: selPattAddr = %d, want 612", e.selPattAddr)
	}
	if !e.collided || !e.overtime {
		t.Errorf("fixture latches: collided=%v overtime=%v, want both set", e.collided, e.overtime)
	}

	out := drive(primed())
	if out[0] != 0x03 {
		t.Errorf("first status read = %#02x, want 0x03 (both latches): the latches are not reaching the output", out[0])
	}
	drawn := 0
	for _, b := range out {
		if b != 0 {
			drawn++
		}
	}
	if drawn < 400 {
		t.Errorf("the fixture drew %d non-zero bytes: a replay comparison over a near-blank frame proves nothing", drawn)
	}
}

// attrExtended is the one captured field whose restore cannot be observed, and
// this is why rather than an excuse for it: WriteAttr sets the flag only at
// byte 3, and the only state it can be true in is the one where the cursor is
// parked at 4 — where the next write auto-advances to the following sprite
// whether the record was four bytes or five. So the flag is exactly
// (attrCursor == 4), and a restore that dropped it would be corrected by the
// cursor. It is still captured, because keeping the two in step is WriteAttr's
// invariant to hold and not the capture's to assume; this test is what says so.
func TestAttrExtendedIsExactlyACursorParkedAtFour(t *testing.T) {
	e := New()
	check := func(after string) {
		t.Helper()
		if e.attrExtended != (e.attrCursor == 4) {
			t.Fatalf("after %s: attrCursor=%d attrExtended=%v — the two have come apart, "+
				"so attrExtended is now observable state and needs a mutation test of its own",
				after, e.attrCursor, e.attrExtended)
		}
	}
	// Byte 3 decides the record length, so drive records of both lengths, and
	// keep writing past the end of each so the wrap is covered too.
	for _, byte3 := range []byte{0x00, 0x40, 0x81, 0xC1} {
		e.SelectSprite(9)
		check("SelectSprite")
		for i, v := range []byte{0x11, 0x22, 0x33, byte3, 0x44, 0x55, 0x66, 0x77, byte3, 0x88} {
			e.WriteAttr(v)
			check(fmt.Sprintf("write %d of the byte3=%#02x record", i, byte3))
		}
	}
}

// A capture must not alias the live engine's pattern RAM or attribute bank.
// The registry copies the blob too, but a device handing back a view of its own
// memory is a bug worth catching where it would be written.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	e := primed()
	st := e.SaveState()

	wantPattern := e.PatternByte(300)
	wantAttr := *e.Sprite(0)

	e.SetPatternAddr(0)
	for i := 0; i < PatternRAMSize; i++ {
		e.WritePatternByte(0xFF)
	}
	scramble(e)

	if err := e.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := e.PatternByte(300); got != wantPattern {
		t.Errorf("pattern byte 300 = %#02x after restore, want %#02x", got, wantPattern)
	}
	if got := *e.Sprite(0); got != wantAttr {
		t.Errorf("sprite 0 = %+v after restore, want %+v", got, wantAttr)
	}
}

// A capture taken before the guest's first NR$19 write describes an engine
// with no clip window at all, and restoring it has to take the window away
// again. No port write clears one, so that direction exists only through a
// rewind — which is exactly how a field like this gets left out of a restore
// and never noticed.
func TestRestoringAPreClipCaptureRemovesTheClipWindow(t *testing.T) {
	e := New()
	e.SetEnabled(true)
	e.SetPatternAddr(1 * 256)
	for i := 0; i < 256; i++ {
		e.WritePatternByte(byte(i%251) + 1)
	}
	e.Set(0, Attr{X: 100, Y: 60, Pattern: 1, Palette: 1, Visible: true})

	unclipped := make([]byte, stateTestWidth)
	e.RenderScanline(64, unclipped, stateTestWidth)
	st := e.SaveState()

	e.SetClip(0, 40, 0, 191) // the guest clips the layer at x 81

	if err := e.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, _, _, _, set := e.Clip(); set {
		t.Error("restoring a capture taken before any NR$19 write left the clip window in place")
	}
	got := make([]byte, stateTestWidth)
	e.RenderScanline(64, got, stateTestWidth)
	if !bytes.Equal(unclipped, got) {
		t.Error("the restored engine still draws a clipped sprite")
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "next.sprite" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.sprite")
	}
}

func TestLoadStateRejectsRubbishWithoutHalfApplying(t *testing.T) {
	e := primed()
	before := e.SaveState()

	if err := e.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Fatal("a malformed state blob must be reported, not half-applied")
	}
	if !bytes.Equal(before, e.SaveState()) {
		t.Error("a refused state blob still changed the engine")
	}
}

func TestLoadStateRejectsAnEmptyBlob(t *testing.T) {
	if err := New().LoadState(nil); err == nil {
		t.Error("an empty state blob must be reported, not treated as a zeroed engine")
	}
}

// A blob whose cursor points outside pattern RAM must be refused rather than
// applied: WritePatternByte indexes the array directly, so accepting it would
// surface as a panic on the next upload byte with nothing left pointing at the
// cause.
func TestLoadStateRefusesAnImpossiblePatternCursor(t *testing.T) {
	e := primed()
	before := e.SaveState()

	var buf bytes.Buffer
	bad := spriteState{Pattern: make([]byte, PatternRAMSize), SelPattAddr: 0xFFFF}
	if err := gob.NewEncoder(&buf).Encode(bad); err != nil {
		t.Fatalf("encoding the bad state: %v", err)
	}

	if err := e.LoadState(buf.Bytes()); err == nil {
		t.Fatal("a pattern cursor outside pattern RAM must be refused")
	}
	if !bytes.Equal(before, e.SaveState()) {
		t.Error("a refused state blob still changed the engine")
	}
}

// Same for a truncated pattern RAM: copying it would leave the tail of the live
// pattern RAM holding present-day bytes under a restored attribute bank.
func TestLoadStateRefusesATruncatedPatternRAM(t *testing.T) {
	e := primed()
	before := e.SaveState()

	var buf bytes.Buffer
	bad := spriteState{Pattern: make([]byte, 64)}
	if err := gob.NewEncoder(&buf).Encode(bad); err != nil {
		t.Fatalf("encoding the bad state: %v", err)
	}

	if err := e.LoadState(buf.Bytes()); err == nil {
		t.Fatal("a short pattern RAM must be refused")
	}
	if !bytes.Equal(before, e.SaveState()) {
		t.Error("a refused state blob still changed the engine")
	}
}
