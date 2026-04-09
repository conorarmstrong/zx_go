package testharness

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestKempstonMouseReadsFromPeripheralBus enables the Kempston
// mouse, moves it via the peripheral manager, presses a button,
// and verifies that reading the canonical Kempston ports from
// CPU memory space returns the expected values. This exercises
// the full IN-port → PeripheralManager → kempmouse path.
func TestKempstonMouseReadsFromPeripheralBus(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.peripherals.EnableKempstonMouse()
	h.peripherals.KempstonMouseMove(42, 17)
	h.peripherals.KempstonMouseButton(1, true) // left button down

	// Poll the IN port directly through the ULA — that's what a
	// Z80 IN instruction ends up doing. The ULA dispatches to
	// the peripheral manager, which dispatches to kempmouse.
	xVal, ok := h.ula.ReadPort(0xFBDF)
	if !ok || xVal != 42 {
		t.Errorf("IN 0xFBDF: %02X ok=%v, want 42 true", xVal, ok)
	}
	// Y is inverted (host +Y = screen down → Kempston -Y).
	yVal, ok := h.ula.ReadPort(0xFFDF)
	if !ok || yVal != 239 { // 0 - 17 mod 256
		t.Errorf("IN 0xFFDF: %02X ok=%v, want 239 true", yVal, ok)
	}
	btnVal, ok := h.ula.ReadPort(0xFADF)
	if !ok || btnVal != 0xFD { // bit 1 low (left pressed)
		t.Errorf("IN 0xFADF: %02X ok=%v, want 0xFD true", btnVal, ok)
	}
}

// TestZXPrinterPortDispatch verifies the ZX Printer is reachable
// through the ULA → PeripheralManager → zxprinter dispatch path.
// We start the motor by writing to 0xFB, advance a few frames,
// and stop the motor — expecting at least one line to accumulate
// in the printer's output buffer.
func TestZXPrinterPortDispatch(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.peripherals.EnableZXPrinter()
	printer := h.peripherals.ZXPrinter()
	if printer == nil {
		t.Fatal("ZXPrinter() returned nil after Enable")
	}

	// Start the motor (bit 2 clear) with stylus dark (bit 7 set).
	// Port 0xFB has bit 2 clear, matching the printer decode.
	h.ula.WritePort(0xFB, 0x80)

	// Drive a small number of port writes spaced apart by frames
	// so the printer's T-state delta advances the drum. One full
	// line at speed 2 takes about 70k T-states; we walk past
	// that by running 3 frames (~210k T-states).
	for i := 0; i < 3; i++ {
		h.RunFrames(1)
		h.ula.WritePort(0xFB, 0x80) // keep stylus dark, motor on
	}

	// Stop the motor (bit 2 set) — flushes the current line if
	// we're mid-line.
	h.ula.WritePort(0xFB, 0x84)

	if printer.Rows() == 0 {
		t.Error("expected at least one row printed; got 0")
	}
	t.Logf("printer accumulated %d rows", printer.Rows())
}

// TestZXPrinterCOPYCommand is the gold-standard end-to-end test
// for the ZX Printer: boot 48K BASIC, type PRINT 123 to put known
// content on the screen, then execute COPY (Z in K mode) to send
// it to the printer. We verify both the row count (drum timing +
// line wrapping) and the dark-vs-light pixel balance (stylus
// state correctly applied to the screen bitmap).
//
// We can't just press Z right after boot because the boot banner
// sits on row 23 and gets overwritten by the edit-line cursor,
// leaving the screen effectively blank by the time COPY runs.
// PRINT 123 first puts "123" at the top of the screen on row 0,
// which survives the next edit-line operation and becomes the
// content that COPY streams to the printer.
func TestZXPrinterCOPYCommand(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.peripherals.EnableZXPrinter()

	// Wait for the BASIC copyright banner — signals K mode ready.
	if _, err := h.RunUntilText("1982", 200); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Type PRINT 123 and execute. P in K mode types PRINT, then
	// the digits, then ENTER runs it — putting "123" at row 0.
	h.TapKey(fyne.KeyP)
	h.TapKey(fyne.Key1)
	h.TapKey(fyne.Key2)
	h.TapKey(fyne.Key3)
	h.TapKey(fyne.KeyReturn)
	h.RunFrames(20)

	// Confirm "123" reached the screen before we COPY it.
	pre := h.ScreenText()
	if !strings.Contains(pre, "123") {
		t.Fatalf("PRINT 123 didn't put 123 on screen; got:\n%s", pre)
	}

	// Now type COPY and execute. Z in K mode types the keyword.
	h.TapKey(fyne.KeyZ)
	h.TapKey(fyne.KeyReturn)

	// COPY takes ~3 seconds of simulated time at fast motor speed
	// to walk through 192 scanlines. 2500 frames (~50s) is ample.
	printer := h.peripherals.ZXPrinter()
	h.RunFrames(2500)

	// Dump the output on any assertion failure for human inspection.
	defer func() {
		if t.Failed() {
			_ = printer.Save("zxprinter_copy_failure.png")
			t.Logf("saved failure image to zxprinter_copy_failure.png")
		}
	}()

	rows := printer.Rows()
	t.Logf("ZX Printer accumulated %d rows after COPY", rows)
	// 192 is the ideal row count (one per Spectrum scanline); the
	// 150 floor allows for drum-timing slop that shaves a handful
	// of lines off the total. Observed: ~176 rows.
	if rows < 150 {
		t.Errorf("expected at least 150 rows from COPY (192 visible scanlines), got %d", rows)
	}

	// Count dark and light pixels. "123" on row 0 gives a small
	// but non-zero dark-pixel count against the otherwise-blank
	// screen. All-dark or all-light would mean the drum/stylus
	// model is broken.
	img := printer.Bitmap()
	darkCount, lightCount := 0, 0
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			if img.GrayAt(x, y).Y == 0 {
				darkCount++
			} else {
				lightCount++
			}
		}
	}
	t.Logf("dark pixels: %d  light pixels: %d", darkCount, lightCount)
	if darkCount == 0 {
		t.Error("expected dark pixels in COPY output (from '123' on screen); got zero")
	}
	if lightCount == 0 {
		t.Error("expected light pixels in COPY output; got zero (everything darkened)")
	}

	// Sanity check: BASIC should have returned to the "0 OK" prompt.
	post := h.ScreenText()
	if !strings.Contains(post, "0 OK") {
		t.Errorf("expected '0 OK' in screen text after COPY, got:\n%s", post)
	}
}

// TestZXPrinterPortDecodeRejected confirms that writes to ports
// with bit 2 set (e.g. 0xFE, the ULA port) do NOT reach the
// printer. Bit 2 is the printer's decode gate.
func TestZXPrinterPortDecodeRejected(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.peripherals.EnableZXPrinter()
	printer := h.peripherals.ZXPrinter()

	// Write to 0xFE (ULA, bit 2 set) — should not touch the printer.
	h.ula.WritePort(0xFE, 0x80)
	h.RunFrames(10)
	h.ula.WritePort(0xFE, 0x84)

	if printer.Rows() != 0 {
		t.Errorf("after non-printer port writes: rows=%d, want 0", printer.Rows())
	}
}
