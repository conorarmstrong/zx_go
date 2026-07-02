package main

import "github.com/conorarmstrong/zx_go/pkg/next/nextregs"

// directBootNextRegs is the post-core-config NextReg "personality" the
// real ZX Next FPGA core establishes before NextZXOS runs — captured
// from a live NextZXOS boot on the same distro SD card. In direct-core
// boot (no FPGA bootrom, no TBBLUE.FW execution) zx_go must reproduce
// these so NextZXOS init reads the machine it expects (machine
// type/timing, CPU speed, peripheral + port enables, divMMC automap +
// entry-points, interrupt control) and completes its init — otherwise
// it frame-syncs (HALT) before enabling interrupts and hangs
// (PC=$0990, IFF1=0).
//
// Only the core-set "personality" registers are applied. The
// NextZXOS-managed registers (MMU8 $50-$57, palette, Layer 2 banks)
// are left for NextZXOS to set during its own init.
var directBootNextRegs = map[byte]byte{
	0x03: 0x33, // machine type + timing
	0x05: 0x59, // peripheral 1
	0x07: 0x33, // CPU speed (28 MHz) — read at $0991 during init
	0x08: 0xDE, // peripheral 3 (port/DAC enables)
	0x09: 0x03, // peripheral 4
	// NR$0A automap ON at reset (bit4 set + bit0). enNxtmmc's reset
	// stub then runs + hands off to enNextZX at $0001 (PUSH $0001 +
	// page-out + RET). On real HW this runs ONCE; the only way it
	// loops is if SP is in the $2000-$3FFF divMMC-RAM window when the
	// RET pops (then it reads paged-out RAM = $0000 -> re-triggers
	// automap). Applied via WriteReg below.
	0x0A: 0x11, // peripheral 5 — divMMC automap on (bit 4) + bit 0
	0x82: 0xFF, // internal port decode enables
	0x83: 0xFF,
	0x84: 0xFF,
	0x85: 0xFF,
	// divMMC entry points, captured from a live NextZXOS boot. These
	// MUST reach the pager (via WriteReg, below) so the automap engine
	// triggers correctly; in particular B8 bit 7 + B9 bit 7 = 0 makes
	// the IM1 ($0038) interrupt page in the divMMC ROM via the
	// rom3_delayed_on path (zxnext.vhd:2901) so its $25B8 SP-save runs.
	0xB8: 0x82, // entry points 0   ($0008 + $0038)
	0xB9: 0x00, // entry points valid 0  (overrides the $01 reset default)
	0xBA: 0x00, // entry points timing 0
	0xBB: 0xF2, // entry points 1
	0xC0: 0x08, // interrupt control
}

// directBootPagerRegs are the NextRegs that must go through WriteReg
// (not Store) so the dispatcher's OnWrite side-effects fire and the
// live hardware state (divMMC automap enable + entry-point config)
// actually updates — Store only sets the readback value.
var directBootPagerRegs = map[byte]bool{
	// NR$03 MUST go through WriteReg, not Store: WireMachineType's
	// OnWrite is what calls mem.ClearConfigMode(). Seeding it via
	// Store sets the readback byte but leaves configModeActive=true
	// (the Next power-on default), so NextZXOS still sees config mode
	// active, runs its machine-reconfigure path, and soft-resets into
	// a bank-2 $0000 trap. $33 has machine-type bits 2:0 = 3
	// (+2A/+3/Next), which the handler treats as "exit config mode"
	// → ClearConfigMode fires.
	0x03: true, // ClearConfigMode + machine type
	0x0A: true, // SetAutomap
	0xB8: true, // SetEntryPoints0
	0xB9: true, // SetEntryPointsValid0
	0xBA: true, // SetEntryPointsTiming0
	0xBB: true, // SetEntryPoints1
}

// applyDirectBootNextRegs seeds the dispatcher with the post-core-config
// personality. NR$0A (divMMC automap) goes through WriteReg so the
// pager's SetAutomap side effect fires (automap ON at reset, matching
// real hardware); the rest are pure readback values and are Stored.
func applyDirectBootNextRegs(disp *nextregs.Dispatcher) {
	if disp == nil {
		return
	}
	for reg, val := range directBootNextRegs {
		if directBootPagerRegs[reg] {
			disp.WriteReg(reg, val)
		} else {
			disp.Store(reg, val)
		}
	}
}
