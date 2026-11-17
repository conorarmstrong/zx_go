package next

import "github.com/conorarmstrong/zx_go/pkg/roms"

// Frame-interrupt timing model, transcribed from the FPGA core so the
// whole class of INT-timing behaviour conforms in one place.
//
// The maskable frame INT (int_ula) asserts for a narrow pulse when the 7 MHz
// video counters reach (hc=c_int_h, vc=c_int_v) (video/zxula_timing.vhd:551).
// The pulse is held for a fixed number of CPU cycles: 32 for 48K/+3, 36 for
// 128K/Pentagon (zxnext.vhd:2014-2033).
//
// The emulator's frame origin IS that moment. Every video and contention
// figure is measured from the interrupt: the display start (pkg/roms/timing.go,
// 14362 T on 128K timing, cross-checked by simulating zxula_timing.vhd from
// o_int_ula to the first ULA fetch), the contention window, the beam, the line
// interrupt. So the pulse asserts at frame T-state 0 on every timing.
//
// It used to assert at the raw counter position of the pulse,
// (c_int_v*(c_max_hc+1)+c_int_h)/2, which is 291 on +3 timing. That is the
// distance from the raw counter's (0,0), a point nothing else in the emulator
// counts from, and it put the paper 291 T early relative to the interrupt on
// the 128K, +3 and Next.
//
// FrameIntTiming maps the NR$03 machine-timing field (bits 2:0; 00X=48K,
// 010=128K, 011=+3, 100=Pentagon, zxnext.vhd:149) and the 50/60 Hz flag to
// the assert T-state (always 0) and the pulse width in CPU cycles.
func FrameIntTiming(nr03MachineTiming byte, sixtyHz bool) (assertTstate, pulseTstates int) {
	switch nr03MachineTiming & 0x07 {
	case 0x02, 0x04: // 128K, Pentagon
		return 0, 36
	default: // 48K (00X), +3 (011)
		return 0, 32
	}
}

// MachineTimingFor maps a classic machine model to the NR$03 machine-timing
// code its frame-INT pulse derives from, reporting false for the ZX8x and SAM
// machines, which have no narrow-pulse model here. The 48K has one: its ULA's
// /INT is a 32 T-state pulse (zxnext.vhd:2014-2033 for the Next's 48K timing),
// and holding it for the whole frame let a long prefix block that spans the
// pulse take the interrupt late.
//
// Shared so every entry point configures the CPU identically. The GUI applied
// this at construction while pkg/testharness did not, which left the golden
// corpus running the 128K family and the Next on held-INT — a configuration
// no user ever sees, and the wrong one for judging INT-timing conformance.
func MachineTimingFor(model roms.SpectrumModel) (nr03 byte, ok bool) {
	switch model {
	case roms.Model48K:
		return 0x00, true
	case roms.Model128K, roms.ModelPlus2:
		return 0x02, true
	case roms.ModelPlus2A, roms.ModelPlus3:
		return 0x03, true
	case roms.ModelPentagon:
		return 0x04, true
	case roms.ModelNext:
		// NextZXOS boots in +3/128K timing (NR$03 default "011").
		return 0x03, true
	}
	return 0, false
}
