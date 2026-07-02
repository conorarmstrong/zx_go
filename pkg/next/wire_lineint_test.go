package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// TestWireLineInterruptNR22EnableComputesOffset verifies the
// NR$22/$23 → cpu.LineIntOffsetTstates pipeline. Per nextreg.txt 0x22
// bit 1 enables line interrupt. The target line is relative to the
// 256×192 active area (vpos = target-1+min_vactive, min_vactive=64 =
// the top border), measured from frameStart (absolute line 0). So
// target 192 → (192-1+64) × 228.
func TestWireLineInterruptNR22EnableComputesOffset(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.Select(0x23)
	disp.WriteData(192) // line LSB = 192, MSB stays 0
	disp.Select(0x22)
	disp.WriteData(0x02) // bit 1 = enable, bit 0 = 0

	want := uint64(192-1+64) * 228
	if got := cpu.LineIntOffsetTstates; got != want {
		t.Errorf("LineIntOffsetTstates = %d, want %d", got, want)
	}
	if cpu.FrameIntDisabled {
		t.Errorf("FrameIntDisabled = true, want false (bit 2 not set)")
	}
}

// TestWireLineInterruptNR22DisableClearsOffset checks that clearing
// bit 1 of NR$22 zeros the cached offset.
func TestWireLineInterruptNR22DisableClearsOffset(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.Select(0x23)
	disp.WriteData(50)
	disp.Select(0x22)
	disp.WriteData(0x02) // enable (bit 1)
	if cpu.LineIntOffsetTstates == 0 {
		t.Fatalf("precondition: expected non-zero offset after enable")
	}
	disp.Select(0x22)
	disp.WriteData(0x00) // disable
	if cpu.LineIntOffsetTstates != 0 {
		t.Errorf("after NR$22=0: LineIntOffsetTstates = %d, want 0",
			cpu.LineIntOffsetTstates)
	}
}

// TestWireLineInterruptNR22FrameDisable covers bit 2: setting it
// propagates to cpu.FrameIntDisabled.
func TestWireLineInterruptNR22FrameDisable(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.Select(0x22)
	disp.WriteData(0x04) // bit 2 = frame INT disable
	if !cpu.FrameIntDisabled {
		t.Errorf("FrameIntDisabled = false, want true after NR$22 bit 2")
	}
}

// TestWireLineInterruptNR22MSBCombines verifies bit 0 of NR$22
// concatenates with NR$23 to form the 9-bit target. Line 256
// (MSB=1, LSB=0) with bit 1 enable → (256-1+64) × 228 T-states.
func TestWireLineInterruptNR22MSBCombines(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.Select(0x23)
	disp.WriteData(0x00)
	disp.Select(0x22)
	disp.WriteData(0x03) // bit 1 enable + bit 0 MSB

	want := uint64(256-1+64) * 228
	if got := cpu.LineIntOffsetTstates; got != want {
		t.Errorf("LineIntOffsetTstates = %d, want %d", got, want)
	}
}

// TestWireLineInterruptScalesWithSpeed locks in NR$07 speed-multiplier
// scaling. At 28 MHz (multiplier 8), line 100 → (100-1+64) × 228 × 8.
func TestWireLineInterruptScalesWithSpeed(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.Select(0x23)
	disp.WriteData(100)
	disp.Select(0x22)
	disp.WriteData(0x02) // bit 1 enable
	disp.Select(0x07)
	disp.WriteData(0x03) // 28 MHz, multiplier 8

	want := uint64(100-1+64) * 228 * 8
	if got := cpu.LineIntOffsetTstates; got != want {
		t.Errorf("LineIntOffsetTstates @ 28MHz = %d, want %d", got, want)
	}
}
