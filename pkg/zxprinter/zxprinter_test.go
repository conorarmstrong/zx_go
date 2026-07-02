package zxprinter

import (
	"testing"
)

func TestNewIsIdle(t *testing.T) {
	p := New()
	if p.Rows() != 0 {
		t.Errorf("new printer: %d rows, want 0", p.Rows())
	}
}

// TestPortDecodeRejectsNonPrinterPorts verifies that IN / OUT to
// ports with bit 2 set don't touch the printer. The ZX Printer
// decodes (port & 0x04) == 0 only.
func TestPortDecodeRejectsNonPrinterPorts(t *testing.T) {
	p := New()
	if _, ok := p.HandlePortRead(0xFF, 0); ok { // bit 2 set → not printer
		t.Error("0xFF read: printer claimed it")
	}
	if p.HandlePortWrite(0xFF, 0x80, 0) {
		t.Error("0xFF write: printer claimed it")
	}
	// Canonical printer port 0xFB has bit 2 clear — should be claimed.
	if _, ok := p.HandlePortRead(0xFB, 0); !ok {
		t.Error("0xFB read: printer did not claim it")
	}
}

// TestMotorOffReadsIdle checks that before the motor has started
// the status port returns the idle value (0x3e) regardless of any
// other state.
func TestMotorOffReadsIdle(t *testing.T) {
	p := New()
	val, ok := p.HandlePortRead(0xFB, 1000)
	if !ok || val != 0x3e {
		t.Errorf("idle read: %02X ok=%v, want 0x3e true", val, ok)
	}
}

// TestMotorOnThenOffProducesLine verifies that starting the motor,
// writing a full line's worth of "stylus dark" via advancing
// T-states, then stopping the motor produces one row in the
// printer's output buffer. This is the minimum viable end-to-end
// print test.
func TestMotorOnThenOffProducesLine(t *testing.T) {
	p := New()

	// Start motor with speed 2 (fast — 220 T-states per pixel).
	// bit 2 = 0 (motor on), bit 1 = 0 (speed=2), bit 7 = 1 (stylus dark)
	p.HandlePortWrite(0xFB, 0x80, 0)

	// Advance the T-state clock past the end of the visible
	// line. At speed 2, cpp = 220; the drum hits x=256 at
	// (256+64)*220 = 70400 cycles from the anchor. Write at
	// that point with the motor-off bit set.
	p.HandlePortWrite(0xFB, 0x84, 70400)

	if p.Rows() != 1 {
		t.Errorf("after full line + motor-off: %d rows, want 1", p.Rows())
	}

	// The line should be all-dark since the stylus was held high
	// the whole time.
	img := p.Bitmap()
	if img.Bounds().Dx() != LineWidth {
		t.Errorf("image width %d, want %d", img.Bounds().Dx(), LineWidth)
	}
	if img.Bounds().Dy() != 1 {
		t.Errorf("image height %d, want 1", img.Bounds().Dy())
	}
	// Check a few pixels are actually dark.
	darkCount := 0
	for x := 0; x < LineWidth; x++ {
		if img.GrayAt(x, 0).Y == 0 {
			darkCount++
		}
	}
	if darkCount < LineWidth/2 {
		t.Errorf("expected most of the line to be dark, got %d/256 dark pixels", darkCount)
	}
}

// TestClearDropsRows verifies Clear drops the accumulated output.
func TestClearDropsRows(t *testing.T) {
	p := New()
	p.HandlePortWrite(0xFB, 0x80, 0)
	p.HandlePortWrite(0xFB, 0x84, 70400)
	if p.Rows() != 1 {
		t.Fatalf("setup: rows = %d, want 1", p.Rows())
	}
	p.Clear()
	if p.Rows() != 0 {
		t.Errorf("after Clear: %d rows, want 0", p.Rows())
	}
}

// ========================================================================
// Additional ZX Printer tests.
// ========================================================================

// TestResetClearsStateButPreservesOutput documents Reset()'s
// current behaviour: it re-initialises the state machine (speed,
// pixel cursor, stylus state) but DOES NOT drop accumulated print
// output. Use Clear() to throw away the printed image.
func TestResetClearsStateButPreservesOutput(t *testing.T) {
	p := New()
	p.HandlePortWrite(0xFB, 0x80, 0)
	p.HandlePortWrite(0xFB, 0x84, 70400)
	if p.Rows() != 1 {
		t.Fatalf("setup: rows = %d, want 1", p.Rows())
	}
	p.Reset()
	if p.Rows() != 1 {
		t.Errorf("after Reset: %d rows, want 1 (output preserved)", p.Rows())
	}
}

// TestPortDecodesAllEvenPorts verifies that any port with bit 2
// clear is decoded as the printer (the ZX Printer's bus decoder).
func TestPortDecodesAllEvenPorts(t *testing.T) {
	p := New()
	// Sample a few ports that satisfy port&0x04 == 0.
	for _, port := range []uint16{0xFB, 0x00FB, 0x40FB, 0x7BFB, 0x80FB} {
		if _, ok := p.HandlePortRead(port, 0); !ok {
			t.Errorf("port $%04X: HandlePortRead = false, want true", port)
		}
	}
	// Ports with bit 2 set must NOT claim.
	for _, port := range []uint16{0xFF, 0x04, 0x40FF, 0x07} {
		if _, ok := p.HandlePortRead(port, 0); ok {
			t.Errorf("port $%04X: HandlePortRead = true, want false (bit 2 set)",
				port)
		}
	}
}

// TestStylusOff_ProducesBlankLine — write to printer with bit 7=0
// (stylus low) for a full line should produce an all-white row.
func TestStylusOff_ProducesBlankLine(t *testing.T) {
	p := New()
	// Stylus off, motor on (bit 2 = 0).
	p.HandlePortWrite(0xFB, 0x00, 0)
	p.HandlePortWrite(0xFB, 0x04, 70400) // motor off
	if p.Rows() != 1 {
		t.Fatalf("rows = %d, want 1", p.Rows())
	}
	img := p.Bitmap()
	darkCount := 0
	for x := 0; x < LineWidth; x++ {
		if img.GrayAt(x, 0).Y == 0 {
			darkCount++
		}
	}
	if darkCount > LineWidth/8 {
		t.Errorf("stylus-off line had %d dark pixels — should be mostly blank",
			darkCount)
	}
}

// TestFrameIsNoop verifies the Frame() hook doesn't crash and
// doesn't add spurious rows.
func TestFrameIsNoop(t *testing.T) {
	p := New()
	before := p.Rows()
	p.Frame()
	if p.Rows() != before {
		t.Errorf("Frame: rows = %d → %d", before, p.Rows())
	}
}
