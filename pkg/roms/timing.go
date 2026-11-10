package roms

// Per-model raster geometry, in 3.5 MHz T-states measured from the frame
// interrupt. This is the single source of truth: pkg/memory keys its
// contention window off it and pkg/ula keys the floating bus and the
// mid-frame border tracking off it, so the two can never drift apart.
//
// All of it is derived from the FPGA core's video timing
// (video/zxula_timing.vhd), not from folklore constants — see
// DisplayStartTState for the derivation.

// TStatesPerLine returns the model's scanline length.
//
// zxula_timing.vhd sets c_max_hc to 447 for the 48K and Pentagon (448
// columns) and 455 for the 128K family (456). The horizontal counter runs
// from i_CLK_7, so two columns make one 3.5 MHz T-state.
func TStatesPerLine(model SpectrumModel) int {
	switch model {
	case Model48K, ModelPentagon:
		return 224
	default:
		return 228
	}
}

// DisplayStartTState returns the 50 Hz T-state, measured from the frame
// interrupt, at which the ULA fetches the first paper byte of the first display
// line. It is the compatibility wrapper for DisplayStartTStateFor(model, false).
//
// zxula_timing.vhd derives the ULA-relative counters from the raw ones:
//
//	hc_ula resets to 0 at raw hc = c_min_hactive - 12   (line 422)
//	vc_ula resets to 0 on the line where raw vc = c_min_vactive (line 424)
//
// and the fetch window driving contention is hc_ula in [0,256) over vc_ula in
// [0,192) (zxula.vhd:414, 583). The interrupt fires at (raw vc = c_int_v,
// raw hc = c_int_h), so the gap to the first fetch in 7 MHz columns is
//
//	(c_min_vactive - c_int_v) * (c_max_hc+1) - (c_int_h - (c_min_hactive-12))
//
// halved to reach T-states. Per personality at 50 Hz:
//
//	48K       c_int_v=0   c_int_h=116  c_min_hactive=128  c_min_vactive=64
//	          the interrupt lands exactly on hc_ula 0, so 64*448/2 = 14336
//	128K/+2   c_int_v=1   c_int_h=128  c_min_hactive=136
//	          interrupt at hc_ula 4, so (63*456 - 4)/2      = 14362
//	+2A/+3    c_int_v=1   c_int_h=126  c_min_hactive=136
//	          interrupt at hc_ula 2, so (63*456 - 2)/2      = 14363
//	Pentagon  c_int_v=319 c_int_h=439  c_min_vactive=80
//	          vc wraps 319 -> 80, so (81*448 - 323)/2       = 17982
//
// At 60 Hz the vertical counters change (c_max_vc=263, c_min_vactive=40,
// c_int_v=0) and so does the display start:
//
//	48K 60 Hz       40*448/2                              =  8960
//	128K 60 Hz      (40*456 - 4)/2                        =  9118
//	+2A/+3 60 Hz    (40*456 - 2)/2                        =  9119
//
// The 48K and 128K 50 Hz results reproduce the documented 14336 and 14362
// exactly. Cross-checked by SIMULATION, not just by reading the source: a GHDL
// testbench drives the real zxula_timing.vhd, waits for its o_int_ula pulse,
// and counts 7 MHz ticks until the ULA fetch counters first reach (0,0). It
// returns 14336 / 14362 / 14363 / 17982 for 48K / 128K / +3 / Pentagon,
// matching every figure below. The testbench is dev-only, under
// _tools/rastertiming-vhdl-test.
func DisplayStartTState(model SpectrumModel) int {
	return DisplayStartTStateFor(model, false)
}

// DisplayStartTStateFor is DisplayStartTState with an explicit 50/60 Hz
// video mode.
func DisplayStartTStateFor(model SpectrumModel, sixtyHz bool) int {
	if sixtyHz {
		switch model {
		case Model48K:
			return 8960
		case ModelPlus2A, ModelPlus3:
			return 9119
		case Model128K, ModelPlus2, ModelNext:
			return 9118
		}
		// Pentagon has no 60 Hz timing in zxula_timing.vhd.
	}
	switch model {
	case Model48K:
		return 14336
	case ModelPlus2A, ModelPlus3:
		return 14363
	case ModelPentagon:
		return 17982
	default:
		// 128K, +2, and the Next (which boots in 128K timing on the
		// path TX-1696 and the raster-wait loops use).
		return 14362
	}
}

// FirstContendedTState returns the first T-state of the frame on which a
// contended access is stalled. The ULA asserts contention one T-state ahead
// of the fetch it is covering — the canonical "-1" — so this is simply the
// display start less one. Reproduces the documented 14335 (48K) and 14361
// (128K).
func FirstContendedTState(model SpectrumModel) int {
	return FirstContendedTStateFor(model, false)
}

// FirstContendedTStateFor is FirstContendedTState with an explicit
// 50/60 Hz video mode.
func FirstContendedTStateFor(model SpectrumModel, sixtyHz bool) int {
	return DisplayStartTStateFor(model, sixtyHz) - 1
}

// FrameTStates returns the model's whole 50 Hz frame length in 3.5 MHz
// T-states: 312 lines of 224 on the 48K, 311 of 228 on the 128K family, and
// 320 of 224 on the Pentagon (zxula_timing.vhd c_max_vc / c_max_hc per
// personality). It is the compatibility wrapper for FrameTStatesFor(model,
// false).
//
// Dividing the running T-state counter by this gives a frame number that
// advances whether or not anything renders — which is what the raster journal
// needs to scope its entries.
func FrameTStates(model SpectrumModel) int {
	return frameTStates50(model)
}

// frameTStates50 is the 50 Hz frame length, used by FrameTStates and by
// FrameTStatesFor's non-60 Hz path.
func frameTStates50(model SpectrumModel) int {
	switch model {
	case Model48K:
		return 69888
	case ModelPentagon:
		return 71680
	default:
		return 70908
	}
}

// FrameTStatesFor is FrameTStates with an explicit 50/60 Hz video mode.
// At 60 Hz zxula_timing.vhd sets c_max_vc=263 (264 lines) for both the
// 48K and the 128K family, while the line length is unchanged:
//
//	48K 60 Hz        264 * 448 / 2 = 59136
//	128K/+3 60 Hz    264 * 456 / 2 = 60192
//
// Pentagon has no 60 Hz mode; its 50 Hz length is returned.
func FrameTStatesFor(model SpectrumModel, sixtyHz bool) int {
	if !sixtyHz || model == ModelPentagon {
		return frameTStates50(model)
	}
	switch model {
	case Model48K:
		return 59136
	default:
		return 60192
	}
}

// LinesPerFrameFor returns the scanline count of one frame for a machine
// model, optionally at 60 Hz. The 50 Hz counts are 312 for the 48K, 311 for
// the 128K family / Next and 320 for the Pentagon; at 60 Hz the 48K and the
// 128K family both use 264 lines (zxula_timing.vhd c_max_vc=263).
func LinesPerFrameFor(model SpectrumModel, sixtyHz bool) int {
	if sixtyHz && model != ModelPentagon {
		return 264
	}
	switch model {
	case Model48K:
		return 312
	case ModelPentagon:
		return 320
	default:
		return 311
	}
}
