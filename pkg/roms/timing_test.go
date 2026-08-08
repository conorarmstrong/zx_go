package roms

import "testing"

// These pin the per-model raster geometry against the FPGA core. Both were
// previously assumed to be "64 lines from the interrupt to the display, at
// 228 T-states per line" for every machine, which is only true of the 48K
// (and even there only by coincidence of the line length).

// TestTStatesPerLine pins the scanline length from zxula_timing.vhd c_max_hc
// (447 on the 48K and Pentagon = 448 columns, 455 on the 128K family = 456),
// with hc clocked at 7 MHz so two columns make one 3.5 MHz T-state.
func TestTStatesPerLine(t *testing.T) {
	for _, tc := range []struct {
		model SpectrumModel
		want  int
	}{
		{Model48K, 224},
		{Model128K, 228},
		{ModelPlus2, 228},
		{ModelPlus2A, 228},
		{ModelPlus3, 228},
		{ModelNext, 228}, // boots in 128K/+3 timing
		{ModelPentagon, 224},
	} {
		if got := TStatesPerLine(tc.model); got != tc.want {
			t.Errorf("TStatesPerLine(%s) = %d, want %d", GetModelName(tc.model), got, tc.want)
		}
	}
}

// TestDisplayStartTState pins the T-state of the first paper fetch, measured
// from the frame interrupt.
//
// Derived from the FPGA rather than from folklore. In zxula_timing.vhd the
// ULA-relative counters are offset from the raw ones:
//
//	hc_ula resets to 0 at raw hc = c_min_hactive - 12   (line 422)
//	vc_ula resets to 0 on the line where raw vc = c_min_vactive (line 424)
//
// and the fetch window that drives contention is hc_ula in [0,256) over
// vc_ula in [0,192) (zxula.vhd:414, 583). The interrupt fires at
// (raw vc = c_int_v, raw hc = c_int_h). So the distance from the interrupt to
// the first fetch, in 7 MHz columns, is
//
//	(c_min_vactive - c_int_v) * (c_max_hc+1) - (c_int_h - (c_min_hactive-12))
//
// halved to reach 3.5 MHz T-states:
//
//	48K   c_int_v=0 c_int_h=116 c_min_hactive=128 -> INT sits exactly at
//	      hc_ula 0, so 64*448/2                      = 14336
//	128K  c_int_v=1 c_int_h=128 c_min_hactive=136 -> INT at hc_ula 4,
//	      so (63*456 - 4)/2                          = 14362
//	+2A/3 c_int_h=126 -> INT at hc_ula 2,
//	      so (63*456 - 2)/2                          = 14363
//	Pent. c_int_v=319 c_int_h=439 c_min_vactive=80, wrapping vc 319->80,
//	      so (81*448 - 323)/2                        = 17982
//
// The 48K and 128K results reproduce the documented 14336 / 14362 exactly,
// which is what validates the derivation. The +2A/+3 differs from the
// commonly-quoted 128K figure by one T-state, because the FPGA gives it its
// own c_int_h; the core is the better authority there.
func TestDisplayStartTState(t *testing.T) {
	for _, tc := range []struct {
		model SpectrumModel
		want  int
	}{
		{Model48K, 14336},
		{Model128K, 14362},
		{ModelPlus2, 14362},
		{ModelPlus2A, 14363},
		{ModelPlus3, 14363},
		{ModelNext, 14362},
		{ModelPentagon, 17982},
	} {
		if got := DisplayStartTState(tc.model); got != tc.want {
			t.Errorf("DisplayStartTState(%s) = %d, want %d", GetModelName(tc.model), got, tc.want)
		}
	}
}

// TestDisplayStartIsNotAFlat64Lines guards the specific bug: taking
// 64 * TStatesPerLine for every model puts the whole 128K family a full
// scanline late against the interrupt.
func TestDisplayStartIsNotAFlat64Lines(t *testing.T) {
	for _, m := range []SpectrumModel{Model128K, ModelPlus2, ModelPlus2A, ModelPlus3, ModelNext} {
		flat := 64 * TStatesPerLine(m)
		got := DisplayStartTState(m)
		if got == flat {
			t.Errorf("%s: display start is still the flat 64-line %d", GetModelName(m), flat)
		}
		if drift := flat - got; drift < 200 || drift > 260 {
			t.Errorf("%s: display start %d is %d T-states from the flat %d; expected the ~230 correction",
				GetModelName(m), got, drift, flat)
		}
	}
	// The 48K genuinely is 64 whole lines: its interrupt lands exactly on
	// hc_ula 0 (c_int_h == c_min_hactive-12).
	if got, want := DisplayStartTState(Model48K), 64*TStatesPerLine(Model48K); got != want {
		t.Errorf("48K display start = %d, want %d (64 whole lines)", got, want)
	}
}

// TestFirstContendedTState pins the contention origin: the ULA prefetches one
// T-state ahead of the first paper fetch, the canonical "-1". The 48K and
// 128K values reproduce the documented 14335 and 14361.
func TestFirstContendedTState(t *testing.T) {
	for _, tc := range []struct {
		model SpectrumModel
		want  int
	}{
		{Model48K, 14335},
		{Model128K, 14361},
		{ModelPlus2, 14361},
		{ModelPlus2A, 14362},
		{ModelPlus3, 14362},
	} {
		if got := FirstContendedTState(tc.model); got != tc.want {
			t.Errorf("FirstContendedTState(%s) = %d, want %d", GetModelName(tc.model), got, tc.want)
		}
	}
}
