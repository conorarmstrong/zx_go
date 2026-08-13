package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// The line-interrupt target is an ABSOLUTE raster line, not a line relative to
// the 256x192 active area.
//
// zxula_timing.vhd is explicit (the target register feeds i_int_line):
//
//	if i_int_line = 0 then
//	   int_line_num <= c_max_vc;
//	else
//	   int_line_num <= unsigned(i_int_line) - 1;
//	end if;
//	...
//	if (i_inten_line = '1') and (hc_ula = 255) and (cvc = int_line_num) then
//
// and cvc is a plain 0..c_max_vc raster counter with no active-area offset.
// So the fire line is target-1, full stop.
//
// Adding a 64-line "min_vactive" offset pushed any target near the end of the
// frame past the last line, where it could never match. Eternal Battle
// (SpecNext) disables the frame interrupt via NR$22 bit 2 and asks for line
// 310 of 311; with the offset it waited on line 373, which does not exist, so
// the CPU sat halted in IM 2 forever on a black screen.
func TestLineInterruptTargetIsAnAbsoluteRasterLine(t *testing.T) {
	cases := []struct {
		name       string
		nr23, nr22 byte
		wantLine   uint64
	}{
		{"target 192", 192, 0x02, 191},
		{"target 310 via MSB", 0x36, 0x03, 309}, // Eternal Battle
		{"target 1 is the first line", 1, 0x02, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cpu := z80.New(minimalMem{}, minimalULA{})
			disp := nextregs.New()
			WireCPUSpeed(disp, cpu)
			WireLineInterrupt(disp, cpu)

			disp.Select(0x23)
			disp.WriteData(tc.nr23)
			disp.Select(0x22)
			disp.WriteData(tc.nr22)

			want := tc.wantLine * 228 * uint64(cpu.SpeedMultiplier())
			if got := cpu.LineIntOffsetTstates; got != want {
				t.Errorf("LineIntOffsetTstates = %d (line %d), want %d (line %d)",
					got, got/228/uint64(cpu.SpeedMultiplier()), want, tc.wantLine)
			}
		})
	}
}

// A target of 0 selects the last line of the frame, per the VHDL's
// "if i_int_line = 0 then int_line_num <= c_max_vc".
func TestLineInterruptTargetZeroIsTheLastLine(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.Select(0x23)
	disp.WriteData(0)
	disp.Select(0x22)
	disp.WriteData(0x02) // enable, MSB 0 → target 0

	want := uint64(linesPerFrame-1) * 228 * uint64(cpu.SpeedMultiplier())
	if got := cpu.LineIntOffsetTstates; got != want {
		t.Errorf("LineIntOffsetTstates = %d, want %d (the last line of the frame)", got, want)
	}
}
