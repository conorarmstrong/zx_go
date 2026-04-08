package testharness

import (
	"testing"

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
