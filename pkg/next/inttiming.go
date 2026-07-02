package next

// Frame-interrupt timing model, transcribed from the FPGA core so the
// whole class of INT-timing behaviour conforms in one place (timing.md
// §1a/§1c). See inttiming_test.go for the conformance matrix.
//
// The maskable frame INT (`int_ula`) asserts for a narrow pulse when the
// 7 MHz video counters reach (hc=c_int_h, vc=c_int_v)
// (video/zxula_timing.vhd:551). The pulse is held for a fixed number of
// CPU cycles — 32 for 48K/+3, 36 for 128K/Pentagon (zxnext.vhd:2014-2033).
//
// FrameIntTiming maps the NR$03 machine-timing field (bits 2:0; 00X=48K,
// 010=128K, 011=+3, 100=Pentagon — zxnext.vhd:149) and the 50/60 Hz flag
// (NR$05) to:
//   - assertTstate: the frame-relative tstate (3.5 MHz) of the INT pulse,
//     = (c_int_v·(c_max_hc+1) + c_int_h) / 2 (the video grid runs at 7 MHz,
//     so two 7 MHz ticks per 3.5 MHz T-state).
//   - pulseTstates: the pulse width in CPU cycles.
//
// The per-mode (c_int_h, c_int_v, c_max_hc) constants are zxula_timing.vhd
// lines 155-298.
func FrameIntTiming(nr03MachineTiming byte, sixtyHz bool) (assertTstate, pulseTstates int) {
	switch nr03MachineTiming & 0x07 {
	case 0x02: // 128K
		pulseTstates = 36
		if sixtyHz {
			assertTstate = (0*456 + 128) / 2 // c_int_v=0
		} else {
			assertTstate = (1*456 + 128) / 2 // c_int_v=1 → 292
		}
	case 0x03: // +3 / +2A
		pulseTstates = 32
		if sixtyHz {
			assertTstate = (0*456 + 126) / 2 // 63
		} else {
			assertTstate = (1*456 + 126) / 2 // 291
		}
	case 0x04: // Pentagon (always one timing)
		pulseTstates = 36
		assertTstate = (319*448 + 439) / 2 // 71675
	default: // 48K (00X)
		pulseTstates = 32
		assertTstate = (0*448 + 116) / 2 // 58
	}
	return assertTstate, pulseTstates
}
