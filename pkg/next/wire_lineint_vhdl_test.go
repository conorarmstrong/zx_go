package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// The line-interrupt target counts from the top of the paper, not from the
// frame interrupt.
//
// zxula_timing.vhd:
//
//	ula_max_hc      <= '1' when hc = ula_min_hactive else '0';  -- c_min_hactive - 12
//	ula_min_vactive <= '1' when vc = c_min_vactive else '0';
//	...
//	if ula_max_hc = '1' then
//	   if ula_min_vactive = '1' then
//	      cvc <= unsigned('0' & i_cu_offset);
//	   elsif cvc = c_max_vc then
//	      cvc <= (others => '0');
//	   else
//	      cvc <= cvc + 1;
//	...
//	if i_int_line = 0 then int_line_num <= c_max_vc;
//	else                   int_line_num <= unsigned(i_int_line) - 1;
//	...
//	if (i_inten_line = '1') and (hc_ula = 255) and (cvc = int_line_num) then
//
// cvc is 0 on the first paper line and runs through every line of the frame
// before it is reloaded, so target N fires on paper row N-1, at hc_ula 255.
// At 128K 50 Hz the frame interrupt is at vc 1, hc 128 (c_int_v, c_int_h) and
// the paper starts at vc 64 (c_min_vactive); hc_ula 255 is hc 380. So from
// the frame interrupt the fire point is ((N + 62) mod 311) lines plus
// (380 - 128) / 2 = 126 T-states.
//
// Every target is reachable: line 310 of 311 (Eternal Battle, frame INT off)
// lands 61 lines after the frame interrupt. An earlier reading added the
// 64-line paper offset without wrapping it, asked for line 373, and hung.
func TestLineInterruptTargetCountsFromThePaper(t *testing.T) {
	cases := []struct {
		name       string
		nr23, nr22 byte
		wantLine   uint64
	}{
		{"target 1 is paper row 0", 1, 0x02, 63},
		{"target 193 is just below the paper", 193, 0x02, 255},
		{"target 248 is the last line of the frame", 248, 0x02, 310},
		{"target 249 wraps to the first", 249, 0x02, 0},
		{"target 310 via MSB", 0x36, 0x03, 61}, // Eternal Battle
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

			want := (tc.wantLine*228 + 126) * uint64(cpu.SpeedMultiplier())
			if got := cpu.LineIntOffsetTstates; got != want {
				t.Errorf("LineIntOffsetTstates = %d, want %d (line %d after the frame INT)",
					got, want, tc.wantLine)
			}
		})
	}
}

// A target of 0 selects cvc = c_max_vc, the paper row before row 0: 62 lines
// after the frame interrupt.
func TestLineInterruptTargetZeroIsTheLineBeforeThePaper(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.Select(0x23)
	disp.WriteData(0)
	disp.Select(0x22)
	disp.WriteData(0x02) // enable, MSB 0 → target 0

	want := uint64(62*228+126) * uint64(cpu.SpeedMultiplier())
	if got := cpu.LineIntOffsetTstates; got != want {
		t.Errorf("LineIntOffsetTstates = %d, want %d", got, want)
	}
}

// At 60 Hz the frame interrupt is at vc 0 and the paper at vc 40 of 264, so
// target N is ((N + 39) mod 264) lines after the frame interrupt.
func TestLineInterruptAt60Hz(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu, func() bool { return true })

	for _, tc := range []struct {
		target   byte
		wantLine uint64
	}{{1, 40}, {193, 232}, {225, 0}} {
		disp.Select(0x23)
		disp.WriteData(tc.target)
		disp.Select(0x22)
		disp.WriteData(0x02)
		want := uint64(tc.wantLine*228 + 126)
		if got := cpu.LineIntOffsetTstates; got != want {
			t.Errorf("60 Hz target %d: LineIntOffsetTstates = %d, want %d (line %d)",
				tc.target, got, want, tc.wantLine)
		}
	}
}
